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

func runReferenceWorker(lc fx.Lifecycle, cfg config.Config, r *app.ResolvePending, l *slog.Logger) {
	w := refworker.New(r, refworker.Config{Interval: cfg.Workers.PollInterval, Lease: cfg.Workers.Lease, Batch: cfg.Workers.Batch}, l)
	runLoop(lc, w.Loop)
}

var consumerModule = fx.Module("consumer",
	fx.Invoke(runConsumer),
)

func runConsumer(lc fx.Lifecycle, cfg config.Config, client *sqs.Client, submit *app.SubmitWager, l *slog.Logger) {
	c := sqsin.New(client, submit, sqsin.Config{
		QueueURL: cfg.AWS.WagerQueueURL, DLQURL: cfg.AWS.DLQURL, Senders: cfg.AWS.Senders,
		MaxMessages: 10, WaitSeconds: 20, MaxVisibility: 60 * time.Second,
	}, l)
	runLoop(lc, c.Loop)
}

var outboxRelayModule = fx.Module("outbox-relay",
	fx.Invoke(runOutboxRelay),
)

func runOutboxRelay(lc fx.Lifecycle, cfg config.Config, pool *pgxpool.Pool, clock app.Clock, l *slog.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := platform.NewSNSClient(ctx, cfg.AWS.Region)
	if err != nil {
		return err
	}
	r := outbox.New(postgres.NewOutboxStore(pool), snsout.New(client, cfg.AWS.EventsTopicARN), outbox.Config{
		Interval: cfg.Workers.PollInterval, Lease: cfg.Workers.Lease, Batch: cfg.Workers.Batch,
		MaxPermanentAttempts: 5, InitialBackoff: time.Second, MaxBackoff: 5 * time.Minute,
	}, clock.Now, uuid.NewString, l)
	runLoop(lc, r.Loop)
	return nil
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
