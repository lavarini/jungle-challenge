// Package runner runs a background loop with a two-phase stop: first stop
// fetching new work, then, only if the deadline expires, abort work in flight.
package runner

import "context"

// Loop fetches work while run is alive and does each unit of work under work.
type Loop func(run, work context.Context)

type Runner struct {
	cancelRun  context.CancelFunc
	cancelWork context.CancelFunc
	done       chan struct{}
}

func Start(loop Loop) *Runner {
	run, cancelRun := context.WithCancel(context.Background())
	work, cancelWork := context.WithCancel(context.Background())
	r := &Runner{cancelRun: cancelRun, cancelWork: cancelWork, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		loop(run, work)
	}()
	return r
}

// Stop stops fetching and waits for the loop. When ctx expires first, it
// cancels the work in flight, waits for the loop to return and reports the
// deadline.
func (r *Runner) Stop(ctx context.Context) error {
	r.cancelRun()
	select {
	case <-r.done:
		r.cancelWork()
		return nil
	case <-ctx.Done():
		r.cancelWork()
		<-r.done
		return ctx.Err()
	}
}
