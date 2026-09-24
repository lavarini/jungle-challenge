package outbox

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type call struct {
	op    string
	seq   int64
	claim string
	next  time.Time
}

type fakeStore struct {
	msgs    []Message
	calls   []call
	applied bool
}

func (f *fakeStore) ClaimHeads(_ context.Context, _ time.Time, _ int, _ string, _ time.Time) ([]Message, error) {
	m := f.msgs
	f.msgs = nil
	return m, nil
}
func (f *fakeStore) Ack(_ context.Context, seq int64, claim string, _ time.Time) (bool, error) {
	f.calls = append(f.calls, call{op: "ack", seq: seq, claim: claim})
	return f.applied, nil
}
func (f *fakeStore) Retry(_ context.Context, seq int64, claim string, next time.Time, _ string) (bool, error) {
	f.calls = append(f.calls, call{op: "retry", seq: seq, claim: claim, next: next})
	return f.applied, nil
}
func (f *fakeStore) Dead(_ context.Context, seq int64, claim string, _ time.Time, _ string) (bool, error) {
	f.calls = append(f.calls, call{op: "dead", seq: seq, claim: claim})
	return f.applied, nil
}

type fakePublisher struct{ errs map[int64]error }

func (f fakePublisher) Publish(_ context.Context, m Message) error { return f.errs[m.Seq] }

var t0 = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func newTestRelay(store Store, pub Publisher) *Relay {
	return New(store, pub, Config{Interval: time.Millisecond, Lease: 30 * time.Second, Batch: 10, MaxPermanentAttempts: 3,
		InitialBackoff: time.Second, MaxBackoff: time.Minute},
		func() time.Time { return t0 }, func() string { return "claim-1" }, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestTickRoutesEachOutcome(t *testing.T) {
	store := &fakeStore{applied: true, msgs: []Message{
		{Seq: 1, Attempts: 1},
		{Seq: 2, Attempts: 2},
		{Seq: 3, Attempts: 1},
		{Seq: 4, Attempts: 3},
	}}
	pub := fakePublisher{errs: map[int64]error{
		2: errors.New("throttled"),
		3: ErrPermanent,
		4: ErrPermanent,
	}}
	n, err := newTestRelay(store, pub).Tick(context.Background())
	if err != nil || n != 4 {
		t.Fatalf("Tick = %d, %v", n, err)
	}
	want := []string{"ack", "retry", "retry", "dead"}
	for i, c := range store.calls {
		if c.op != want[i] || c.claim != "claim-1" {
			t.Fatalf("call %d = %+v, want %s with the tick's claim", i, c, want[i])
		}
	}
	if got := store.calls[1].next.Sub(t0); got != 2*time.Second {
		t.Fatalf("transient backoff after 2 attempts = %s, want 2s", got)
	}
}

func TestBackoffIsCapped(t *testing.T) {
	r := newTestRelay(&fakeStore{}, fakePublisher{})
	cases := map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 4: 8 * time.Second, 20: time.Minute}
	for attempts, want := range cases {
		if got := r.backoff(attempts); got != want {
			t.Errorf("backoff(%d) = %s, want %s", attempts, got, want)
		}
	}
}

func TestLostClaimIsNotAnError(t *testing.T) {
	store := &fakeStore{applied: false, msgs: []Message{{Seq: 1, Attempts: 1}}}
	if _, err := newTestRelay(store, fakePublisher{}).Tick(context.Background()); err != nil {
		t.Fatalf("a fenced ack must be logged, not failed: %v", err)
	}
}
