// Command wagerd runs the wagering service. `wagerd migrate up|down` applies
// migrations with MIGRATE_DATABASE_URL (owner role).
package main

import (
	"fmt"
	"os"

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
