//go:build e2e

// Package crash kills wagerd processes at exact failure windows and proves
// another process finishes the work without duplicating money.
package crash

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lavarini/backend-challenge-go/internal/adapters/postgres"
	"github.com/lavarini/backend-challenge-go/test/testenv"
)

var (
	env    *testenv.Env
	binary string
	dir    string
	queues struct{ in, dlq string }
)

func TestMain(m *testing.M) { os.Exit(run(m)) }

func run(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var err error
	if env, err = testenv.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer env.Terminate(context.Background())
	if err := postgres.Migrate(env.Postgres.MigratorDSN, "up"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if dir, err = os.MkdirTemp("", "wagerd-crash"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	binary = filepath.Join(dir, "wagerd")
	build := exec.Command("go", "build", "-tags", "failpoint", "-o", binary, "./cmd/wagerd")
	_, file, _, _ := runtime.Caller(0)
	build.Dir = filepath.Join(filepath.Dir(file), "..", "..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintln(os.Stderr, "build:", err, string(out))
		return 1
	}
	if queues.in, err = env.LocalStack.QueueURL(ctx, "wager-transactions.fifo"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if queues.dlq, err = env.LocalStack.QueueURL(ctx, "wager-transactions-dlq.fifo"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}

type proc struct {
	name string
	addr string
	cmd  *exec.Cmd
	log  string
	done chan error
}

// spawn starts a wagerd process with role and extra environment. It is killed
// when the test ends if still running.
func spawn(t *testing.T, name, role string, extra ...string) *proc {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	logPath := filepath.Join(dir, fmt.Sprintf("%s-%d.log", name, time.Now().UnixNano()))
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.Env = append(os.Environ(),
		"WAGERD_ROLE="+role, "HTTP_ADDR="+addr, "ADMIN_ADDR=127.0.0.1:0",
		"DATABASE_URL="+env.Postgres.AppDSN, "OIDC_ISSUER_URL="+env.Keycloak.IssuerURL,
		"AWS_REGION=us-east-1", "AWS_ENDPOINT_URL="+env.LocalStack.Endpoint,
		"AWS_ACCESS_KEY_ID=test", "AWS_SECRET_ACCESS_KEY=test",
		"SQS_WAGER_QUEUE_URL="+queues.in, "SQS_WAGER_DLQ_URL="+queues.dlq,
		"SQS_SENDER_PROVIDERS=111111111111=provider-a",
		"SNS_EVENTS_TOPIC_ARN=arn:aws:sns:us-east-1:"+testenv.Account+":wallet-events.fifo",
		"WORKER_POLL_INTERVAL=100ms", "WORKER_LEASE=2s", "SHUTDOWN_TIMEOUT=10s",
	)
	cmd.Env = append(cmd.Env, extra...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &proc{name: name, addr: addr, cmd: cmd, log: logPath, done: make(chan error, 1)}
	go func() { p.done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		if t.Failed() {
			b, _ := os.ReadFile(logPath)
			t.Logf("----- %s -----\n%s", name, b)
		}
	})
	return p
}

// waitReady polls readiness; only processes with the api role serve HTTP.
func (p *proc) waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get("http://" + p.addr + "/health/ready"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("%s not ready", p.name)
}

// waitExit waits for the process to end and returns its exit code.
func (p *proc) waitExit(t *testing.T, within time.Duration) int {
	t.Helper()
	select {
	case err := <-p.done:
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return 0
	case <-time.After(within):
		t.Fatalf("%s still running after %s", p.name, within)
		return -1
	}
}

func dbPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), env.Postgres.AppDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func eventually(t *testing.T, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", within)
}
