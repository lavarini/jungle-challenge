package runner

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestStopLetsInFlightWorkFinish(t *testing.T) {
	var finished atomic.Bool
	r := Start(func(run, work context.Context) {
		<-run.Done()
		select {
		case <-time.After(50 * time.Millisecond):
			finished.Store(true)
		case <-work.Done():
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if !finished.Load() {
		t.Fatal("in-flight work was aborted before the deadline")
	}
}

func TestStopAbortsWorkAtTheDeadline(t *testing.T) {
	var aborted atomic.Bool
	r := Start(func(run, work context.Context) {
		<-run.Done()
		<-work.Done()
		aborted.Store(true)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := r.Stop(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if !aborted.Load() {
		t.Fatal("loop did not observe the work cancellation")
	}
}
