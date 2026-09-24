package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/lavarini/backend-challenge-go/internal/platform/health"
)

// drain owns the shutdown of everything that serves traffic or does work
// (spec §4 and §7). Fx stops hooks one by one on a shared deadline, so a hook
// per component would let the first slow one starve the rest and flip
// readiness last. Instead components register here, and one stop hook:
//
//  1. reports not-ready (503) before anything drains;
//  2. stops the HTTP server and every worker concurrently;
//  3. returns only when all of them returned.
//
// Its hook is appended after the pool's, so the pool closes only after the
// drain returned. If the drain overruns Fx's deadline, Fx skips the remaining
// hooks and the pool is never closed under a component still using it.
type drain struct {
	ready interface{ SetDraining() }
	log   *slog.Logger

	mu    sync.Mutex
	stops []namedStop
}

type namedStop struct {
	name string
	stop func(context.Context) error
}

// newDrain takes the pool only to be constructed after it: Fx then appends
// the drain hook after the pool hook and runs it first on stop.
func newDrain(lc fx.Lifecycle, _ *pgxpool.Pool, r *health.Readiness, l *slog.Logger) *drain {
	return registerDrain(lc, r, l)
}

func registerDrain(lc fx.Lifecycle, ready interface{ SetDraining() }, l *slog.Logger) *drain {
	d := &drain{ready: ready, log: l}
	lc.Append(fx.StopHook(d.stop))
	return d
}

// add registers a component that has started; stop receives the graceful
// deadline and must return soon after it expires (runners cancel work, the
// HTTP server closes its connections).
func (d *drain) add(name string, stop func(context.Context) error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stops = append(d.stops, namedStop{name: name, stop: stop})
}

func (d *drain) stop(ctx context.Context) error {
	d.ready.SetDraining()
	d.mu.Lock()
	stops := append([]namedStop(nil), d.stops...)
	d.mu.Unlock()

	graceful, cancel := gracefulContext(ctx)
	defer cancel()
	errs := make([]error, len(stops))
	var wg sync.WaitGroup
	for i, s := range stops {
		wg.Go(func() {
			if err := s.stop(graceful); err != nil {
				d.log.Warn("component did not drain in time", "component", s.name, "error", err.Error())
				errs[i] = err
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// forcedShare is the part of the stop budget kept for the forced phase:
// after the graceful deadline, runners cancel work in flight (and, for
// example, release SQS messages) and the HTTP server closes connections,
// still inside the budget Fx gives the stop hooks.
const forcedShare = 5

// gracefulContext ends when ctx does, minus 1/forcedShare of the remaining
// budget. Without a deadline it is ctx itself.
func gracefulContext(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}
	reserve := time.Until(deadline) / forcedShare
	return context.WithDeadline(ctx, deadline.Add(-reserve))
}
