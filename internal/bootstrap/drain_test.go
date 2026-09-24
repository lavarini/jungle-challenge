package bootstrap

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

type fakeReadiness struct{ draining atomic.Bool }

func (f *fakeReadiness) SetDraining() { f.draining.Store(true) }

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Two stoppers that each wait for the other to start can only both finish
// when the drain runs them concurrently; each also checks that readiness had
// already flipped when it began.
func TestDrainFlipsReadinessFirstAndStopsConcurrently(t *testing.T) {
	ready := &fakeReadiness{}
	d := &drain{ready: ready, log: quietLogger()}
	a, b := make(chan struct{}), make(chan struct{})
	var notReadyFirst atomic.Bool
	partner := func(mine, other chan struct{}) func(context.Context) error {
		return func(context.Context) error {
			if !ready.draining.Load() {
				notReadyFirst.Store(true)
			}
			close(mine)
			select {
			case <-other:
				return nil
			case <-time.After(2 * time.Second):
				return errors.New("the other stopper never started: stops ran sequentially")
			}
		}
	}
	d.add("http", partner(a, b))
	d.add("consumer", partner(b, a))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.stop(ctx); err != nil {
		t.Fatal(err)
	}
	if notReadyFirst.Load() {
		t.Fatal("a component began draining while readiness still reported ready")
	}
}

// A stopper that hangs until its deadline must not keep the others from
// being stopped, and it gets a graceful deadline that leaves part of the
// budget for the forced phase.
func TestSlowStopperDoesNotStarveTheOthers(t *testing.T) {
	d := &drain{ready: &fakeReadiness{}, log: quietLogger()}
	var mu sync.Mutex
	stopped := map[string]bool{}
	var slowDeadline time.Time
	d.add("outbox-relay", func(ctx context.Context) error {
		slowDeadline, _ = ctx.Deadline()
		<-ctx.Done()
		return ctx.Err()
	})
	for _, name := range []string{"consumer", "reference-worker", "http"} {
		d.add(name, func(context.Context) error {
			mu.Lock()
			stopped[name] = true
			mu.Unlock()
			return nil
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	outer, _ := ctx.Deadline()

	err := d.stop(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want the slow component's deadline", err)
	}
	for _, name := range []string{"consumer", "reference-worker", "http"} {
		if !stopped[name] {
			t.Errorf("%s was not stopped", name)
		}
	}
	if !slowDeadline.Before(outer) {
		t.Fatalf("graceful deadline %s, want before the stop budget %s", slowDeadline, outer)
	}
	if ctx.Err() != nil {
		t.Fatal("the drain used the whole budget; nothing is left for the hooks after it")
	}
}

// Wired through Fx, the drain hook runs before the pool's hook, which was
// appended first, and the pool closes only after every component returned.
func TestPoolClosesAfterTheDrain(t *testing.T) {
	var mu sync.Mutex
	var order []string
	record := func(s string) {
		mu.Lock()
		order = append(order, s)
		mu.Unlock()
	}
	lc := fxtest.NewLifecycle(t)
	lc.Append(fx.StopHook(func() { record("pool") }))
	ready := &fakeReadiness{}
	d := registerDrain(lc, ready, quietLogger())
	d.add("http", func(context.Context) error {
		time.Sleep(20 * time.Millisecond)
		record("http")
		return nil
	})
	lc.RequireStart().RequireStop()
	if !ready.draining.Load() {
		t.Fatal("readiness was not flipped")
	}
	if len(order) != 2 || order[0] != "http" || order[1] != "pool" {
		t.Fatalf("stop order = %v, want [http pool]", order)
	}
}
