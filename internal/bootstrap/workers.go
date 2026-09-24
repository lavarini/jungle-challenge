package bootstrap

import (
	"context"
	"log/slog"

	"go.uber.org/fx"

	"github.com/lavarini/backend-challenge-go/internal/adapters/refworker"
	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/platform/config"
	"github.com/lavarini/backend-challenge-go/internal/platform/runner"
)

var referenceWorkerModule = fx.Module("reference-worker",
	fx.Provide(app.NewResolvePending),
	fx.Invoke(runReferenceWorker),
)

func runReferenceWorker(lc fx.Lifecycle, cfg config.Config, r *app.ResolvePending, l *slog.Logger) {
	w := refworker.New(r, refworker.Config{Interval: cfg.Workers.PollInterval, Lease: cfg.Workers.Lease, Batch: cfg.Workers.Batch}, l)
	runLoop(lc, w.Loop)
}

// runLoop ties a worker loop to the Fx lifecycle. Registered after the pool,
// so it stops before the pool closes.
func runLoop(lc fx.Lifecycle, loop runner.Loop) {
	var r *runner.Runner
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error { r = runner.Start(loop); return nil },
		OnStop:  func(ctx context.Context) error { return r.Stop(ctx) },
	})
}
