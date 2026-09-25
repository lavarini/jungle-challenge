package bootstrap

import (
	"context"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/lavarini/backend-challenge-go/internal/adapters/metered"
	"github.com/lavarini/backend-challenge-go/internal/adapters/outbox"
	"github.com/lavarini/backend-challenge-go/internal/adapters/postgres"
	"github.com/lavarini/backend-challenge-go/internal/adapters/refworker"
	"github.com/lavarini/backend-challenge-go/internal/adapters/snsout"
	"github.com/lavarini/backend-challenge-go/internal/adapters/sqsin"
	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/platform"
	"github.com/lavarini/backend-challenge-go/internal/platform/config"
	"github.com/lavarini/backend-challenge-go/internal/platform/metrics"
	"github.com/lavarini/backend-challenge-go/internal/platform/runner"
)

var referenceWorkerModule = fx.Module("reference-worker",
	fx.Provide(newResolvePending),
	fx.Invoke(runReferenceWorker),
)

func newResolvePending(uow app.UnitOfWork, clock app.Clock, ids app.IDGenerator, policy app.ReferencePolicy, l *slog.Logger, m *metrics.Metrics) *app.ResolvePending {
	return app.NewResolvePending(uow, clock, ids, policy, l, app.ResolveHooks{
		Concluded:         m.ResolverConcluded,
		InvariantViolated: func() { m.InvariantViolation("async") },
	})
}

func runReferenceWorker(lc fx.Lifecycle, d *drain, cfg config.Config, r *app.ResolvePending, m *metrics.Metrics, l *slog.Logger) {
	w := refworker.New(metered.NewResolver(r, m), refworker.Config{Interval: cfg.Workers.PollInterval, Lease: cfg.Workers.Lease, Batch: cfg.Workers.Batch}, l)
	runLoop(lc, d, "reference-worker", w.Loop)
}

var consumerModule = fx.Module("consumer",
	fx.Invoke(runConsumer),
)

func runConsumer(lc fx.Lifecycle, d *drain, cfg config.Config, client *sqs.Client, submit metered.Submitter, m *metrics.Metrics, l *slog.Logger) {
	c := sqsin.New(client, submit, sqsin.Config{
		QueueURL: cfg.AWS.WagerQueueURL, DLQURL: cfg.AWS.DLQURL, Senders: cfg.AWS.Senders,
		MaxMessages: 10, WaitSeconds: 20, MaxVisibility: 60 * time.Second, MaxReceives: cfg.AWS.MaxReceives,
		Observer: metrics.ConsumerObserver{M: m},
	}, l)
	runLoop(lc, d, "consumer", c.Loop)
}

var outboxRelayModule = fx.Module("outbox-relay",
	fx.Invoke(runOutboxRelay),
)

func runOutboxRelay(lc fx.Lifecycle, d *drain, cfg config.Config, pool *pgxpool.Pool, clock app.Clock, m *metrics.Metrics, l *slog.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := platform.NewSNSClient(ctx, cfg.AWS.Region)
	if err != nil {
		return err
	}
	// Fail startup on a wrong or standard topic instead of quarantining
	// every partition head with InvalidParameter.
	if err := snsout.VerifyFIFOTopic(ctx, client, cfg.AWS.EventsTopicARN); err != nil {
		return err
	}
	r := outbox.New(postgres.NewOutboxStore(pool), snsout.New(client, cfg.AWS.EventsTopicARN), outbox.Config{
		Interval: cfg.Workers.PollInterval, Lease: cfg.Workers.Lease, Batch: cfg.Workers.Batch,
		Concurrency: cfg.Workers.OutboxConcurrency, MaxPermanentAttempts: 5, InitialBackoff: time.Second, MaxBackoff: 5 * time.Minute,
		Observer: metrics.RelayObserver{M: m},
	}, clock.Now, uuid.NewString, l)
	runLoop(lc, d, "outbox-relay", r.Loop)
	return nil
}

// runLoop starts a worker loop with the app and hands its two-phase stop to
// the drain, which stops every worker and the HTTP server concurrently.
func runLoop(lc fx.Lifecycle, d *drain, name string, loop runner.Loop) {
	lc.Append(fx.StartHook(func() {
		r := runner.Start(loop)
		d.add(name, r.Stop)
	}))
}
