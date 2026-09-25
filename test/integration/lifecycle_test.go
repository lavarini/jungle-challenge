//go:build integration

package integration

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"go.uber.org/goleak"

	"github.com/lavarini/backend-challenge-go/internal/bootstrap"
	"github.com/lavarini/backend-challenge-go/internal/platform/config"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func processEnv(t *testing.T, addr string) map[string]string {
	t.Helper()
	queueURL, err := env.LocalStack.QueueURL(context.Background(), "wager-transactions.fifo")
	if err != nil {
		t.Fatal(err)
	}
	dlqURL, err := env.LocalStack.QueueURL(context.Background(), "wager-transactions-dlq.fifo")
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"WAGERD_ROLE": "all", "HTTP_ADDR": addr, "DATABASE_URL": env.Postgres.AppDSN,
		"OIDC_ISSUER_URL": env.Keycloak.IssuerURL, "AWS_REGION": "us-east-1",
		"SQS_WAGER_QUEUE_URL": queueURL, "SHUTDOWN_TIMEOUT": "10s",
		"SQS_WAGER_DLQ_URL": dlqURL, "SQS_SENDER_PROVIDERS": "111111111111=provider-a",
		"SNS_EVENTS_TOPIC_ARN": "arn:aws:sns:us-east-1:000000000000:wallet-events.fifo",
		"ADMIN_ADDR":           freeAddr(t),
	}
}

// The composed app starts against real dependencies, serves readiness, and on
// stop closes the server and the pool without leaking goroutines.
func TestFxLifecycleStartsServesAndStopsCleanly(t *testing.T) {
	addr := freeAddr(t)
	vars := processEnv(t, addr)
	t.Setenv("AWS_ENDPOINT_URL", env.LocalStack.Endpoint)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	cfg, err := config.Load(func(k string) string { return vars[k] })
	if err != nil {
		t.Fatal(err)
	}

	ignore := goleak.IgnoreCurrent()
	var pool *pgxpool.Pool
	app := fx.New(bootstrap.Options(cfg), fx.Populate(&pool), fx.NopLogger)
	startCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := app.Start(startCtx); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(fmt.Sprintf("http://%s/health/ready", addr))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ready = %d", resp.StatusCode)
	}

	// The admin port serves metrics (design.md §7 names, preset at zero) and
	// readiness for worker-only roles.
	admin := vars["ADMIN_ADDR"]
	body := get(t, fmt.Sprintf("http://%s/metrics", admin), http.StatusOK)
	for _, name := range []string{"wager_transactions_total", "outbox_publish_total", "sqs_dlq_total", "pending_references", "outbox_lag_seconds", "go_goroutines"} {
		if !strings.Contains(body, name) {
			t.Errorf("/metrics lacks %s", name)
		}
	}
	get(t, fmt.Sprintf("http://%s/health/ready", admin), http.StatusOK)
	get(t, fmt.Sprintf("http://%s/debug/pprof/", admin), http.StatusOK)

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelStop()
	if err := app.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if _, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		t.Fatal("server still accepting connections after stop")
	}
	if _, err := net.DialTimeout("tcp", admin, time.Second); err == nil {
		t.Fatal("admin server still accepting connections after stop")
	}
	if err := pool.Ping(context.Background()); err == nil {
		t.Fatal("pool still open after stop")
	}
	goleak.VerifyNone(t, ignore,
		goleak.IgnoreAnyFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreAnyFunction("internal/poll.runtime_pollWait"),
	)
}

func get(t *testing.T, url string, want int) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != want {
		t.Fatalf("GET %s = %d, want %d", url, resp.StatusCode, want)
	}
	return string(b)
}
