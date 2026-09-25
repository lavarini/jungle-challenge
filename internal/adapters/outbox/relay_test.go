package outbox

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
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
	mu      sync.Mutex // Ack, Retry and Dead run concurrently under Config.Concurrency
	msgs    []Message
	calls   []call
	applied bool
	err     error
}

func (f *fakeStore) ClaimHeads(_ context.Context, _ time.Time, _ int, _ string, _ time.Time) ([]Message, error) {
	m := f.msgs
	f.msgs = nil
	return m, nil
}
func (f *fakeStore) Ack(_ context.Context, seq int64, claim string, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{op: "ack", seq: seq, claim: claim})
	return f.applied, f.err
}
func (f *fakeStore) Retry(_ context.Context, seq int64, claim string, next time.Time, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{op: "retry", seq: seq, claim: claim, next: next})
	return f.applied, f.err
}
func (f *fakeStore) Dead(_ context.Context, seq int64, claim string, _ time.Time, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{op: "dead", seq: seq, claim: claim})
	return f.applied, f.err
}

type fakePublisher struct{ errs map[int64]error }

func (f fakePublisher) Publish(_ context.Context, m Message) error { return f.errs[m.Seq] }

var t0 = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

// identityJitter keeps backoff assertions exact in tests that don't exercise
// jitter itself.
func identityJitter(d time.Duration) time.Duration { return d }

func newTestRelay(store Store, pub Publisher) *Relay {
	return New(store, pub, Config{Interval: time.Millisecond, Lease: 30 * time.Second, Batch: 10, MaxPermanentAttempts: 3,
		InitialBackoff: time.Second, MaxBackoff: time.Minute, Jitter: identityJitter},
		func() time.Time { return t0 }, func() string { return "claim-1" }, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestTickRoutesEachOutcome(t *testing.T) {
	// Deliberately out of seq order: Tick must sort by Seq before delivering,
	// so the outcomes below still land in commit order (1, 2, 3, 4).
	store := &fakeStore{applied: true, msgs: []Message{
		{Seq: 3, Attempts: 1},
		{Seq: 1, Attempts: 1},
		{Seq: 4, Attempts: 3},
		{Seq: 2, Attempts: 2},
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
	if len(store.calls) != len(want) {
		t.Fatalf("store.calls = %+v, want %d calls", store.calls, len(want))
	}
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

// recordingHandler captures emitted records so a test can assert on them
// instead of only on side effects it cannot observe otherwise.
type recordingHandler struct{ records []slog.Record }

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return nil
}
func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

// A nil Jitter must default to app.UpToTwentyPercent (ADR 0014), not to no
// jitter at all.
func TestNewDefaultsJitterToUpToTwentyPercent(t *testing.T) {
	r := New(&fakeStore{}, fakePublisher{}, Config{Interval: time.Millisecond, Lease: 30 * time.Second, Batch: 10, MaxPermanentAttempts: 3,
		InitialBackoff: time.Second, MaxBackoff: time.Minute},
		func() time.Time { return t0 }, func() string { return "claim-1" }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for i := 0; i < 20; i++ {
		if got := r.backoff(1); got < time.Second || got > time.Second+time.Second/5 {
			t.Fatalf("backoff(1) = %s, want within [1s, 1.2s] (0-20%% jitter)", got)
		}
	}
}

func TestLostClaimIsNotAnError(t *testing.T) {
	store := &fakeStore{applied: false, msgs: []Message{{Seq: 1, Attempts: 1}}}
	rec := &recordingHandler{}
	r := New(store, fakePublisher{}, Config{Interval: time.Millisecond, Lease: 30 * time.Second, Batch: 10, MaxPermanentAttempts: 3,
		InitialBackoff: time.Second, MaxBackoff: time.Minute, Jitter: identityJitter},
		func() time.Time { return t0 }, func() string { return "claim-1" }, slog.New(rec))

	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatalf("a fenced ack must be logged, not failed: %v", err)
	}
	if len(store.calls) != 1 || store.calls[0].op != "ack" {
		t.Fatalf("calls = %+v, want a single ack attempt", store.calls)
	}
	found := false
	for _, entry := range rec.records {
		if entry.Message == "claim lost to another relay; its outcome stands" {
			found = true
		}
	}
	if !found {
		t.Fatal("a fenced write (applied=false) must log that the claim was lost")
	}
}

// hungPublisher blocks like an SNS call into a network black hole: it returns
// only when its context ends.
type hungPublisher struct{ started chan struct{} }

func (p hungPublisher) Publish(ctx context.Context, _ Message) error {
	close(p.started)
	<-ctx.Done()
	return ctx.Err()
}

// A publish is bounded by the lease and by the work context: once shutdown
// cancels work, a hung publish returns at once, and the relay leaves the
// lease to expire instead of writing bookkeeping with a dead context.
func TestWorkCancellationAbortsAHungPublish(t *testing.T) {
	store := &fakeStore{applied: true, msgs: []Message{{Seq: 1, Attempts: 1}, {Seq: 2, Attempts: 1}}}
	pub := hungPublisher{started: make(chan struct{})}
	// A real clock keeps the lease 30s ahead, so only work can end the publish.
	r := New(store, pub, Config{Interval: time.Millisecond, Lease: 30 * time.Second, Batch: 10, MaxPermanentAttempts: 3,
		InitialBackoff: time.Second, MaxBackoff: time.Minute, Jitter: identityJitter},
		time.Now, func() string { return "claim-1" }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	run, stopRun := context.WithCancel(context.Background())
	work, stopWork := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Loop(run, work) }()

	<-pub.started
	stopRun()
	stopWork()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a hung publish outlived the work cancellation")
	}
	if len(store.calls) != 0 {
		t.Fatalf("calls = %+v, want none: the lease expires and the event is retried", store.calls)
	}
}

type countingObserver struct {
	published, retried, quarantined, claimLost, bookkeeping, publishes int
}

func (o *countingObserver) Published()                    { o.published++ }
func (o *countingObserver) Retried()                      { o.retried++ }
func (o *countingObserver) Quarantined()                  { o.quarantined++ }
func (o *countingObserver) ClaimLost()                    { o.claimLost++ }
func (o *countingObserver) BookkeepingFailed()            { o.bookkeeping++ }
func (o *countingObserver) PublishDuration(time.Duration) { o.publishes++ }

func observedRelay(store Store, pub Publisher, o Observer, log *slog.Logger) *Relay {
	return New(store, pub, Config{Interval: time.Millisecond, Lease: 30 * time.Second, Batch: 10, MaxPermanentAttempts: 3,
		InitialBackoff: time.Second, MaxBackoff: time.Minute, Jitter: identityJitter, Observer: o},
		func() time.Time { return t0 }, func() string { return "claim-1" }, log)
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestObserverSeesEachAppliedOutcome(t *testing.T) {
	o := &countingObserver{}
	store := &fakeStore{applied: true, msgs: []Message{{Seq: 1, Attempts: 1}, {Seq: 2, Attempts: 1}, {Seq: 3, Attempts: 3}}}
	pub := fakePublisher{errs: map[int64]error{2: errors.New("throttled"), 3: ErrPermanent}}
	if _, err := observedRelay(store, pub, o, quietLog()).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if o.published != 1 || o.retried != 1 || o.quarantined != 1 || o.claimLost != 0 || o.publishes != 3 {
		t.Fatalf("observer = %+v", *o)
	}
}

// A Dead fenced off by another relay did not quarantine anything: the other
// relay's outcome stands, so it counts as a lost claim, not as a quarantine.
func TestFencedDeadIsNotAQuarantine(t *testing.T) {
	o := &countingObserver{}
	store := &fakeStore{applied: false, msgs: []Message{{Seq: 1, Attempts: 3}}}
	pub := fakePublisher{errs: map[int64]error{1: ErrPermanent}}
	if _, err := observedRelay(store, pub, o, quietLog()).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if o.quarantined != 0 || o.claimLost != 1 {
		t.Fatalf("observer = %+v", *o)
	}
}

func TestFailedBookkeepingIsCounted(t *testing.T) {
	o := &countingObserver{}
	store := &fakeStore{err: errors.New("db down"), msgs: []Message{{Seq: 1, Attempts: 1}}}
	if _, err := observedRelay(store, fakePublisher{}, o, quietLog()).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if o.bookkeeping != 1 || o.published != 0 {
		t.Fatalf("observer = %+v", *o)
	}
}

// While SNS is down every head fails on every tick; the retry warning is rate
// limited so the outage does not flood the logs.
func TestRetryLogIsRateLimited(t *testing.T) {
	rec := &recordingHandler{}
	var msgs []Message
	errs := map[int64]error{}
	for i := int64(1); i <= 10; i++ {
		msgs = append(msgs, Message{Seq: i, Attempts: 1})
		errs[i] = errors.New("sns unavailable")
	}
	store := &fakeStore{applied: true, msgs: msgs}
	if _, err := observedRelay(store, fakePublisher{errs: errs}, nil, slog.New(rec)).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var retries int
	for _, r := range rec.records {
		if r.Message == "publish failed; will retry" {
			retries++
		}
	}
	if retries != 1 {
		t.Fatalf("retry warnings = %d, want 1 within the rate limit window", retries)
	}
	if len(store.calls) != 10 {
		t.Fatalf("calls = %d, want every event rescheduled", len(store.calls))
	}
}

// A publish cut short by shutdown did not really take that long to fail; it
// must not skew the latency histogram.
func TestInterruptedPublishIsNotObserved(t *testing.T) {
	o := &countingObserver{}
	store := &fakeStore{applied: true, msgs: []Message{{Seq: 1, Attempts: 1}}}
	pub := hungPublisher{started: make(chan struct{})}
	r := New(store, pub, Config{Interval: time.Millisecond, Lease: 30 * time.Second, Batch: 10, MaxPermanentAttempts: 3,
		InitialBackoff: time.Second, MaxBackoff: time.Minute, Jitter: identityJitter, Observer: o},
		time.Now, func() string { return "claim-1" }, quietLog())
	work, stop := context.WithCancel(context.Background())
	go func() { <-pub.started; stop() }()
	if _, err := r.Tick(work); err != nil {
		t.Fatal(err)
	}
	if o.publishes != 0 {
		t.Fatalf("publish durations observed = %d, want 0", o.publishes)
	}
}

// barrierPublisher lets a publish return only once `want` publishes are in
// flight at the same time, so it completes only under concurrent delivery.
type barrierPublisher struct {
	want    int
	mu      sync.Mutex
	arrived int
	release chan struct{}
}

func (p *barrierPublisher) Publish(ctx context.Context, _ Message) error {
	p.mu.Lock()
	p.arrived++
	if p.arrived == p.want {
		close(p.release)
	}
	p.mu.Unlock()
	select {
	case <-p.release:
		return nil
	case <-time.After(2 * time.Second):
		return errors.New("publishes were not concurrent")
	}
}

// Heads of different partitions are independent, so one tick publishes them
// concurrently up to Config.Concurrency: a tick costs about one broker round
// trip instead of one per partition, and each partition still has at most
// one event in flight.
func TestTickPublishesHeadsConcurrently(t *testing.T) {
	const heads = 4
	var msgs []Message
	for i := int64(1); i <= heads; i++ {
		msgs = append(msgs, Message{Seq: i, PartitionKey: string(rune('a' + i)), Attempts: 1})
	}
	store := &fakeStore{applied: true, msgs: msgs}
	pub := &barrierPublisher{want: heads, release: make(chan struct{})}
	r := New(store, pub, Config{Interval: time.Millisecond, Lease: 30 * time.Second, Batch: 10, Concurrency: heads,
		MaxPermanentAttempts: 3, InitialBackoff: time.Second, MaxBackoff: time.Minute, Jitter: identityJitter},
		func() time.Time { return t0 }, func() string { return "claim-1" }, quietLog())

	n, err := r.Tick(context.Background())
	if err != nil || n != heads {
		t.Fatalf("Tick = %d, %v", n, err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.calls) != heads {
		t.Fatalf("calls = %+v, want %d", store.calls, heads)
	}
	for _, c := range store.calls {
		if c.op != "ack" {
			t.Fatalf("call %+v: every head must be published and acked within the tick", c)
		}
	}
}

// drainingStore hands out one head per claim, like a single hot partition
// whose next event becomes the head as soon as the previous one is acked.
type drainingStore struct {
	fakeStore
	left int
}

func (s *drainingStore) ClaimHeads(_ context.Context, _ time.Time, _ int, _ string, _ time.Time) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.left == 0 {
		return nil, nil
	}
	s.left--
	return []Message{{Seq: int64(100 - s.left), PartitionKey: "hot", Attempts: 1}}, nil
}

func (s *drainingStore) acks() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// A tick that delivered anything is followed by another one right away, even
// when the batch was not full: a backlog confined to a few partitions (one
// hot wallet) must drain at broker speed, not at one event per poll interval.
func TestLoopClaimsAgainRightAfterAPartialBatch(t *testing.T) {
	store := &drainingStore{fakeStore: fakeStore{applied: true}, left: 5}
	r := New(store, fakePublisher{}, Config{Interval: time.Hour, Lease: 30 * time.Second, Batch: 10,
		MaxPermanentAttempts: 3, InitialBackoff: time.Second, MaxBackoff: time.Minute, Jitter: identityJitter},
		time.Now, func() string { return "claim-1" }, quietLog())
	run, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Loop(run, context.Background()) }()
	defer func() { stop(); <-done }()

	deadline := time.Now().Add(2 * time.Second)
	for store.acks() < 5 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := store.acks(); n != 5 {
		t.Fatalf("acked %d of 5 events within 2s; the loop waited a poll interval after a partial batch", n)
	}
}
