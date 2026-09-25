package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/lavarini/backend-challenge-go/internal/platform/config"
	"github.com/lavarini/backend-challenge-go/internal/platform/health"
)

// drain owns the shutdown of everything that serves traffic or does work
// (design.md §4 and §7). Fx stops hooks one by one on a shared deadline, so a hook
// per component would let the first slow one starve the rest and flip
// readiness last. Instead components register here, and one stop hook:
//
//  1. reports not-ready (503) before anything drains, then waits the
//     readiness delay (SHUTDOWN_READINESS_DELAY) so a load balancer sees the
//     503 before the port closes;
//  2. stops the HTTP server and every worker concurrently;
//  3. once all of them returned, stops the components registered with
//     addLast (the admin server: metrics, pprof and, for worker-only roles,
//     readiness stay reachable for the whole drain);
//  4. returns.
//
// Its hook is appended after the pool's, so the pool closes only after the
// drain returned. If the drain overruns Fx's deadline, Fx skips the remaining
// hooks and the pool is never closed under a component still using it.
type drain struct {
	ready interface{ SetDraining() }
	delay time.Duration
	log   *slog.Logger

	mu    sync.Mutex
	stops []namedStop
	last  []namedStop
}

type namedStop struct {
	name string
	stop func(context.Context) error
}

// newDrain takes the pool only to be constructed after it: Fx then appends
// the drain hook after the pool hook and runs it first on stop.
func newDrain(lc fx.Lifecycle, _ *pgxpool.Pool, r *health.Readiness, cfg config.Config, l *slog.Logger) *drain {
	return registerDrain(lc, r, cfg.ShutdownReadinessDelay, l)
}

func registerDrain(lc fx.Lifecycle, ready interface{ SetDraining() }, delay time.Duration, l *slog.Logger) *drain {
	d := &drain{ready: ready, delay: delay, log: l}
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

// addLast registers a component stopped only after every add-ed component
// returned, with the rest of the budget.
func (d *drain) addLast(name string, stop func(context.Context) error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.last = append(d.last, namedStop{name: name, stop: stop})
}

func (d *drain) stop(ctx context.Context) error {
	d.ready.SetDraining()
	d.mu.Lock()
	stops := append([]namedStop(nil), d.stops...)
	last := append([]namedStop(nil), d.last...)
	d.mu.Unlock()

	d.waitReadinessDelay(ctx)
	errs := d.stopAll(ctx, stops)
	for _, s := range last {
		if err := s.stop(ctx); err != nil {
			d.log.Warn("component did not drain in time", "component", s.name, "error", err.Error())
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// waitReadinessDelay waits the delay, but never more than half of what is
// left of the budget: the drain itself must still fit.
func (d *drain) waitReadinessDelay(ctx context.Context) {
	delay := d.delay
	if deadline, ok := ctx.Deadline(); ok {
		delay = min(delay, time.Until(deadline)/2)
	}
	if delay <= 0 {
		return
	}
	d.log.Info("reporting not ready before draining", "delay", delay.String())
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (d *drain) stopAll(ctx context.Context, stops []namedStop) []error {
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
	return errs
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
