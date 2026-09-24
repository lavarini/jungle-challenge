//go:build e2e

// Package e2e runs three independent wagerd processes against real
// PostgreSQL, Keycloak and LocalStack.
package e2e

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"testing"

	"github.com/lavarini/backend-challenge-go/internal/adapters/postgres"
	"github.com/lavarini/backend-challenge-go/test/testenv"
)

type process struct {
	name string
	addr string
	cmd  *exec.Cmd
	log  string
}

var (
	env   *testenv.Env
	procs []*process
)

func TestMain(m *testing.M) { os.Exit(run(m)) }

func run(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var err error
	if env, err = testenv.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "start environment:", err)
		return 1
	}
	defer env.Terminate(context.Background())
	if err := postgres.Migrate(env.Postgres.MigratorDSN, "up"); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		return 1
	}

	dir, err := os.MkdirTemp("", "wagerd-e2e")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	binary := filepath.Join(dir, "wagerd")
	build := exec.Command("go", "build", "-o", binary, "./cmd/wagerd")
	build.Dir = repoRoot()
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintln(os.Stderr, "build:", err, string(out))
		return 1
	}
	queueURL, err := env.LocalStack.QueueURL(ctx, "wager-transactions.fifo")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	dlqURL, err := env.LocalStack.QueueURL(ctx, "wager-transactions-dlq.fifo")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	topicARN := "arn:aws:sns:us-east-1:" + testenv.Account + ":wallet-events.fifo"

	for i := 1; i <= 3; i++ {
		p, err := start(binary, dir, fmt.Sprintf("wagerd-%d", i), queueURL, dlqURL, topicARN)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			stopAll()
			return 1
		}
		procs = append(procs, p)
	}
	for _, p := range procs {
		if err := waitReady(p, 60*time.Second); err != nil {
			fmt.Fprintln(os.Stderr, err)
			dumpLogs()
			stopAll()
			return 1
		}
	}

	code := m.Run()
	if code != 0 {
		dumpLogs()
	}
	stopAll()
	return code
}

func start(binary, dir, name, queueURL, dlqURL, topicARN string) (*process, error) {
	addr, err := freeAddr()
	if err != nil {
		return nil, err
	}
	adminAddr, err := freeAddr()
	if err != nil {
		return nil, err
	}
	logPath := filepath.Join(dir, name+".log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(binary)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.Env = append(os.Environ(),
		"WAGERD_ROLE=all",
		"HTTP_ADDR="+addr,
		"ADMIN_ADDR="+adminAddr,
		"DATABASE_URL="+env.Postgres.AppDSN,
		"OIDC_ISSUER_URL="+env.Keycloak.IssuerURL,
		"OIDC_CLOCK_SKEW=1s",
		"AWS_REGION=us-east-1",
		"AWS_ENDPOINT_URL="+env.LocalStack.Endpoint,
		"AWS_ACCESS_KEY_ID=test",
		"AWS_SECRET_ACCESS_KEY=test",
		"SQS_WAGER_QUEUE_URL="+queueURL,
		"SQS_WAGER_DLQ_URL="+dlqURL,
		"SQS_SENDER_PROVIDERS=111111111111=provider-a,222222222222=provider-b",
		"SNS_EVENTS_TOPIC_ARN="+topicARN,
		"WORKER_POLL_INTERVAL=100ms",
		"SHUTDOWN_TIMEOUT=10s",
	)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	return &process{name: name, addr: addr, cmd: cmd, log: logPath}, nil
}

func waitReady(p *process, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + p.addr + "/health/ready")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("%s not ready after %s", p.name, timeout)
}

// stopAll sends SIGTERM and waits for a graceful exit.
func stopAll() {
	for _, p := range procs {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
	}
	for _, p := range procs {
		done := make(chan struct{})
		go func() { _ = p.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = p.cmd.Process.Kill()
		}
	}
}

func dumpLogs() {
	for _, p := range procs {
		b, _ := os.ReadFile(p.log)
		fmt.Fprintf(os.Stderr, "----- %s -----\n%s\n", p.name, b)
	}
}

func freeAddr() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer ln.Close()
	return ln.Addr().String(), nil
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}
