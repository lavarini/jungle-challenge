// Package refworker polls due pending operations and resolves them.
package refworker

import (
	"context"
	"log/slog"
	"time"

	"github.com/lavarini/backend-challenge-go/internal/platform/failpoint"
)

type Resolver interface {
	Claim(ctx context.Context, limit int, lease time.Duration) ([]string, error)
	Resolve(ctx context.Context, id string) error
}

type Config struct {
	Interval time.Duration
	Lease    time.Duration
	Batch    int
}

type Worker struct {
	resolver Resolver
	cfg      Config
	log      *slog.Logger
}

func New(r Resolver, cfg Config, log *slog.Logger) *Worker {
	return &Worker{resolver: r, cfg: cfg, log: log}
}

// Loop claims with the work context and stops claiming when run ends; a
// claimed item left unfinished is retried after its lease.
func (w *Worker) Loop(run, work context.Context) {
	for {
		n := w.tick(run, work)
		if n == w.cfg.Batch {
			if run.Err() != nil {
				return
			}
			continue
		}
		select {
		case <-run.Done():
			return
		case <-time.After(w.cfg.Interval):
		}
	}
}

func (w *Worker) tick(run, work context.Context) int {
	if run.Err() != nil {
		return 0
	}
	ids, err := w.resolver.Claim(work, w.cfg.Batch, w.cfg.Lease)
	if err != nil {
		if work.Err() == nil {
			w.log.WarnContext(work, "claim pending operations failed", "error", err.Error(), "class", "transient")
		}
		return 0
	}
	if len(ids) > 0 {
		failpoint.Hit("resolver.after_claim")
	}
	for _, id := range ids {
		if run.Err() != nil {
			return len(ids)
		}
		if err := w.resolver.Resolve(work, id); err != nil && work.Err() == nil {
			w.log.WarnContext(work, "resolve pending operation failed", "transactionId", id, "error", err.Error())
		}
	}
	return len(ids)
}
