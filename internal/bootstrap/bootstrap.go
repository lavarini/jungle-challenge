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
		observabilityModule,
	}
	if cfg.Role.Runs(config.RoleAPI) {
		opts = append(opts, apiModule)
	}
	if cfg.Role.Runs(config.RoleReferenceWorker) {
		opts = append(opts, referenceWorkerModule)
	}
	if cfg.Role.Runs(config.RoleConsumer) {
		opts = append(opts, consumerModule)
	}
	if cfg.Role.Runs(config.RoleOutboxRelay) {
		opts = append(opts, outboxRelayModule)
	}
	if cfg.Role.Runs(config.RoleOutboxRelay) || cfg.Role.Runs(config.RoleReferenceWorker) {
		opts = append(opts, fx.Invoke(runBacklogGauges))
	}
	return fx.Options(opts...)
}
