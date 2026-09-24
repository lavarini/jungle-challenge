package bootstrap

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/lavarini/backend-challenge-go/internal/adapters/postgres"
	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/platform"
	"github.com/lavarini/backend-challenge-go/internal/platform/config"
	"github.com/lavarini/backend-challenge-go/internal/platform/health"
)

var coreModule = fx.Module("core",
	fx.Provide(
		func() *slog.Logger { return platform.NewLogger(os.Stdout) },
		newPool,
		newSQSClient,
		newReadiness,
		fx.Annotate(postgres.NewUnitOfWork, fx.As(new(app.UnitOfWork))),
		fx.Annotate(platform.NewSystemClock, fx.As(new(app.Clock))),
		fx.Annotate(platform.NewUUIDv7, fx.As(new(app.IDGenerator))),
		app.DefaultReferencePolicy,
		app.NewOpenWallet,
		app.NewGetWallet,
		app.NewSubmitWager,
	),
)

// newPool is provided before every component that uses it, so its stop hook
// runs last: the pool closes only after servers and workers have stopped.
func newPool(lc fx.Lifecycle, cfg config.Config) (*pgxpool.Pool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := postgres.NewPool(ctx, postgres.PoolConfig{
		DSN: cfg.Database.URL, MaxConns: cfg.Database.MaxConns,
		LockTimeout: cfg.Database.LockTimeout, StatementTimeout: cfg.Database.StatementTimeout,
	})
	if err != nil {
		return nil, err
	}
	lc.Append(fx.StopHook(pool.Close))
	return pool, nil
}

func newSQSClient(cfg config.Config) (*sqs.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return platform.NewSQSClient(ctx, cfg.AWS.Region)
}

func newReadiness(pool *pgxpool.Pool, client *sqs.Client, cfg config.Config) *health.Readiness {
	return health.NewReadiness(
		health.Check{Name: "postgres", Probe: pool.Ping},
		health.Check{Name: "sqs", Probe: platform.SQSProbe(client, cfg.AWS.WagerQueueURL)},
	)
}
