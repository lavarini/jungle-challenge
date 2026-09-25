// Command wagerd runs the wagering service. `wagerd migrate up|down` applies
// migrations with MIGRATE_DATABASE_URL (owner role). `wagerd healthcheck`
// probes readiness on ADMIN_ADDR.
package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"go.uber.org/fx"

	"github.com/lavarini/backend-challenge-go/internal/adapters/postgres"
	"github.com/lavarini/backend-challenge-go/internal/bootstrap"
	"github.com/lavarini/backend-challenge-go/internal/platform/config"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) > 0 && args[0] == "migrate" {
		return migrate(args[1:])
	}
	if len(args) > 0 && args[0] == "healthcheck" {
		return healthcheck(os.Getenv)
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	app := fx.New(bootstrap.Options(cfg), fx.StopTimeout(cfg.ShutdownTimeout))
	if err := app.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	app.Run()
	return 0
}

func migrate(args []string) int {
	if len(args) != 1 || (args[0] != "up" && args[0] != "down") {
		fmt.Fprintln(os.Stderr, "usage: wagerd migrate up|down")
		return 2
	}
	dsn := os.Getenv("MIGRATE_DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "MIGRATE_DATABASE_URL is required")
		return 2
	}
	if err := postgres.Migrate(dsn, args[0]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("migrate", args[0], "ok")
	return 0
}

// healthcheck probes the admin readiness endpoint so the distroless image,
// which ships no curl, can back a Compose healthcheck. Every role runs the
// admin server, so this works for worker-only processes too.
func healthcheck(getenv func(string) string) int {
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(adminURL(getenv))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "not ready:", resp.Status)
		return 1
	}
	return 0
}

func adminURL(getenv func(string) string) string {
	addr := getenv("ADMIN_ADDR")
	if addr == "" {
		addr = ":9090"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		port = "9090"
	}
	return "http://127.0.0.1:" + port + "/health/ready"
}
