package bootstrap

import (
	"context"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/lavarini/backend-challenge-go/internal/adapters/outbox"
	"github.com/lavarini/backend-challenge-go/internal/adapters/postgres"
	"github.com/lavarini/backend-challenge-go/internal/adapters/refworker"
	"github.com/lavarini/backend-challenge-go/internal/adapters/snsout"
	"github.com/lavarini/backend-challenge-go/internal/adapters/sqsin"
	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/platform"
	"github.com/lavarini/backend-challenge-go/internal/platform/config"
	"github.com/lavarini/backend-challenge-go/internal/platform/runner"
)

var referenceWorkerModule = fx.Module("reference-worker",
	fx.Provide(app.NewResolvePending),
	fx.Invoke(runReferenceWorker),
)

func runReferenceWorker(lc fx.Lifecycle, d *drain, cfg config.Config, r *app.ResolvePending, l *slog.Logger) {
	w := refworker.New(r, refworker.Config{Interval: cfg.Workers.PollInterval, Lease: cfg.Workers.Lease, Batch: cfg.Workers.Batch}, l)
	runLoop(lc, d, "reference-worker", w.Loop)
}

var consumerModule = fx.Module("consumer",
	fx.Invoke(runConsumer),
)

func runConsumer(lc fx.Lifecycle, d *drain, cfg config.Config, client *sqs.Client, submit *app.SubmitWager, l *slog.Logger) {
	c := sqsin.New(client, submit, sqsin.Config{
		QueueURL: cfg.AWS.WagerQueueURL, DLQURL: cfg.AWS.DLQURL, Senders: cfg.AWS.Senders,
		MaxMessages: 10, WaitSeconds: 20, MaxVisibility: 60 * time.Second, MaxReceives: cfg.AWS.MaxReceives,
	}, l)
	runLoop(lc, d, "consumer", c.Loop)
}

var outboxRelayModule = fx.Module("outbox-relay",
	fx.Invoke(runOutboxRelay),
)

func runOutboxRelay(lc fx.Lifecycle, d *drain, cfg config.Config, pool *pgxpool.Pool, clock app.Clock, l *slog.Logger) error {
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
		MaxPermanentAttempts: 5, InitialBackoff: time.Second, MaxBackoff: 5 * time.Minute,
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
