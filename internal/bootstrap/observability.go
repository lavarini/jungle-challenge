package bootstrap

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/lavarini/backend-challenge-go/internal/adapters/metered"
	"github.com/lavarini/backend-challenge-go/internal/adapters/postgres"
	"github.com/lavarini/backend-challenge-go/internal/adapters/sqsin"
	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/platform/config"
	"github.com/lavarini/backend-challenge-go/internal/platform/metrics"
)

var observabilityModule = fx.Module("observability",
	fx.Provide(newMetrics, newMeteredSubmitter, newReconcile),
	fx.Invoke(runAdminServer),
)

func newMetrics() *metrics.Metrics {
	m := metrics.New()
	m.PresetDLQReasons(sqsin.FailureCodes...)
	return m
}

func newMeteredSubmitter(s *app.SubmitWager, m *metrics.Metrics) metered.Submitter {
	return metered.NewSubmitter(s, m)
}

func newReconcile(r app.Reconciler, l *slog.Logger, m *metrics.Metrics) *app.Reconcile {
	return app.NewReconcile(r, l, m.ReconciliationDivergences.Inc)
}

// backlogInterval is how often worker roles refresh the backlog gauges.
const backlogInterval = 15 * time.Second

// snapshotTimeout bounds one backlog query so a stuck database cannot stall
// the loop past the next refresh.
const snapshotTimeout = 5 * time.Second

// runBacklogGauges refreshes the backlog gauges (worker roles only, so API
// replicas do not repeat the queries).
func runBacklogGauges(lc fx.Lifecycle, d *drain, cfg config.Config, pool *pgxpool.Pool, clock app.Clock, m *metrics.Metrics, l *slog.Logger) {
	stats := postgres.NewStats(pool)
	nearDeadline := cfg.Reference.TTL / 10
	runLoop(lc, d, "backlog-gauges", func(run, work context.Context) {
		for run.Err() == nil {
			ctx, cancel := context.WithTimeout(work, snapshotTimeout)
			st, err := stats.Snapshot(ctx, clock.Now(), nearDeadline)
			cancel()
			if err == nil {
				m.SetPendingReferences(st.PendingReferences, st.PendingNearDeadline)
				m.SetOutbox(st.OutboxPending, st.OutboxOldestMillis)
			} else if work.Err() == nil {
				l.Warn("backlog gauges failed", "error", err.Error())
			}
			select {
			case <-run.Done():
				return
			case <-time.After(backlogInterval):
			}
		}
	})
}
