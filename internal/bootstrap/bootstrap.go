// Package bootstrap is the only package that imports Fx. It composes the
// modules selected by the process role (ADR 0002, ADR 0020).
package bootstrap

import (
	"log/slog"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/lavarini/backend-challenge-go/internal/platform/config"
)

func Options(cfg config.Config) fx.Option {
	opts := []fx.Option{
		fx.Supply(cfg),
		fx.WithLogger(func(l *slog.Logger) fxevent.Logger { return &fxevent.SlogLogger{Logger: l} }),
		coreModule,
	}
	if cfg.Role.Runs(config.RoleAPI) {
		opts = append(opts, apiModule)
	}
	if cfg.Role.Runs(config.RoleReferenceWorker) {
		opts = append(opts, referenceWorkerModule)
	}
	return fx.Options(opts...)
}
