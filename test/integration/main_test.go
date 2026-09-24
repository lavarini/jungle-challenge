//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/lavarini/backend-challenge-go/internal/adapters/postgres"
	"github.com/lavarini/backend-challenge-go/test/testenv"
)

var env *testenv.Env

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	started, err := testenv.Start(ctx)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "start test environment:", err)
		os.Exit(1)
	}
	env = started
	if err := postgres.Migrate(env.Postgres.MigratorDSN, "up"); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		env.Terminate(context.Background())
		os.Exit(1)
	}
	code := m.Run()
	env.Terminate(context.Background())
	os.Exit(code)
}
