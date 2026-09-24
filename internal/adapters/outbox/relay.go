// Package outbox publishes committed events. Only the head of each partition
// (wallet) is claimable, so events of one wallet leave in commit order even
// with several relays (ADR 0014).
package outbox

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/platform/failpoint"
)

var ErrPermanent = errors.New("outbox: permanent publish failure")

type Message struct {
	Seq          int64
	EventID      string
	PartitionKey string
	EventType    string
	Payload      []byte
	Attempts     int // including the current claim
}

// Store operations other than ClaimHeads apply only while claimID still owns
// the row; they report false when another relay took it over (fencing).
type Store interface {
	ClaimHeads(ctx context.Context, now time.Time, limit int, claimID string, leaseUntil time.Time) ([]Message, error)
	Ack(ctx context.Context, seq int64, claimID string, now time.Time) (bool, error)
	Retry(ctx context.Context, seq int64, claimID string, next time.Time, lastErr string) (bool, error)
	Dead(ctx context.Context, seq int64, claimID string, now time.Time, lastErr string) (bool, error)
}

type Publisher interface {
	Publish(ctx context.Context, m Message) error
}

type Config struct {
	Interval             time.Duration
	Lease                time.Duration
	Batch                int
	MaxPermanentAttempts int
	InitialBackoff       time.Duration
	MaxBackoff           time.Duration
	// Jitter stretches the backoff to spread retries (ADR 0014); nil
	// defaults to app.UpToTwentyPercent.
	Jitter func(time.Duration) time.Duration
	// Observer receives outcomes for metrics; nil disables it.
	Observer Observer
	// RetryLogEvery bounds the "publish failed; will retry" warning to one
	// per window while the broker is down; 0 defaults to 10s.
	RetryLogEvery time.Duration
}

// Observer receives relay outcomes for metrics. Published, Retried and
// Quarantined are reported only when the bookkeeping was applied under this
// relay's claim; a fenced write is ClaimLost.
type Observer interface {
	Published()
	Retried()
	Quarantined()
	ClaimLost()
	BookkeepingFailed()
	PublishDuration(d time.Duration)
}

type nopObserver struct{}

func (nopObserver) Published()                    {}
func (nopObserver) Retried()                      {}
func (nopObserver) Quarantined()                  {}
func (nopObserver) ClaimLost()                    {}
func (nopObserver) BookkeepingFailed()            {}
func (nopObserver) PublishDuration(time.Duration) {}

type Relay struct {
	store Store
	pub   Publisher
	cfg   Config
	now   func() time.Time
	newID func() string
	log   *slog.Logger

	retryLog rateLimit
}

func New(store Store, pub Publisher, cfg Config, now func() time.Time, newID func() string, log *slog.Logger) *Relay {
	if cfg.Jitter == nil {
		cfg.Jitter = app.UpToTwentyPercent
	}
	if cfg.Observer == nil {
		cfg.Observer = nopObserver{}
	}
	if cfg.RetryLogEvery <= 0 {
		cfg.RetryLogEvery = 10 * time.Second
	}
	return &Relay{store: store, pub: pub, cfg: cfg, now: now, newID: newID, log: log, retryLog: rateLimit{every: cfg.RetryLogEvery}}
}

func (r *Relay) Loop(run, work context.Context) {
	for run.Err() == nil {
		n, err := r.Tick(work)
		if err != nil && work.Err() == nil {
			r.log.WarnContext(work, "outbox claim failed", "error", err.Error(), "class", "transient")
		}
		if n == r.cfg.Batch {
			continue
		}
		select {
		case <-run.Done():
			return
		case <-time.After(r.cfg.Interval):
		}
	}
}

// Tick claims a batch of partition heads and delivers them in seq order.
func (r *Relay) Tick(ctx context.Context) (int, error) {
	now := r.now()
	claim := r.newID()
	leaseUntil := now.Add(r.cfg.Lease)
	msgs, err := r.store.ClaimHeads(ctx, now, r.cfg.Batch, claim, leaseUntil)
	if err != nil {
		return 0, err
	}
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].Seq < msgs[j].Seq })
	for _, m := range msgs {
		// Once ctx is cancelled, further publishes would just time out one
		// by one; stop and let the leases expire instead of logging a burst
		// of misleading "publish failed" warnings.
		if ctx.Err() != nil {
			break
		}
		r.deliver(ctx, m, claim, leaseUntil)
	}
	return len(msgs), nil
}

func (r *Relay) deliver(ctx context.Context, m Message, claim string, leaseUntil time.Time) {
	log := r.log.With("eventId", m.EventID, "eventType", m.EventType, "walletId", m.PartitionKey, "attempts", m.Attempts)
	pctx, cancel := context.WithDeadline(ctx, leaseUntil)
	start := time.Now()
	perr := r.pub.Publish(pctx, m)
	r.cfg.Observer.PublishDuration(time.Since(start))
	cancel()
	if perr != nil && ctx.Err() != nil {
		// Shutdown cut the publish short: the event did not fail, and
		// bookkeeping under a cancelled context would fail too. The lease
		// expires and the event is claimed again, with the same eventId.
		log.InfoContext(context.WithoutCancel(ctx), "publish interrupted by shutdown; the lease will expire", "error", perr.Error())
		return
	}
	var applied bool
	var serr error
	var outcome func()
	switch {
	case perr == nil:
		failpoint.Hit("outbox.after_publish")
		applied, serr = r.store.Ack(ctx, m.Seq, claim, r.now())
		outcome = r.cfg.Observer.Published
	case errors.Is(perr, ErrPermanent) && m.Attempts >= r.cfg.MaxPermanentAttempts:
		applied, serr = r.store.Dead(ctx, m.Seq, claim, r.now(), perr.Error())
		if serr == nil && applied {
			log.ErrorContext(ctx, "event quarantined", "error", perr.Error(), "class", "permanent")
		}
		outcome = r.cfg.Observer.Quarantined
	default:
		if ok, suppressed := r.retryLog.allow(time.Now()); ok {
			log.WarnContext(ctx, "publish failed; will retry", "error", perr.Error(), "suppressed", suppressed)
		}
		applied, serr = r.store.Retry(ctx, m.Seq, claim, r.now().Add(r.backoff(m.Attempts)), perr.Error())
		outcome = r.cfg.Observer.Retried
	}
	if serr != nil {
		log.WarnContext(ctx, "outbox bookkeeping failed; the lease will expire and the event will be retried", "error", serr.Error())
		r.cfg.Observer.BookkeepingFailed()
		return
	}
	if !applied {
		log.WarnContext(ctx, "claim lost to another relay; its outcome stands")
		r.cfg.Observer.ClaimLost()
		return
	}
	outcome()
}

// rateLimit lets one event through per window and counts the ones it held
// back, so a broker outage logs one warning per window instead of one per
// event per tick.
type rateLimit struct {
	every time.Duration

	mu         sync.Mutex
	last       time.Time
	suppressed int
}

func (l *rateLimit) allow(now time.Time) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.last.IsZero() && now.Sub(l.last) < l.every {
		l.suppressed++
		return false, 0
	}
	n := l.suppressed
	l.last, l.suppressed = now, 0
	return true, n
}

// backoff doubles from InitialBackoff per attempt after the first, up to
// MaxBackoff, then applies Jitter to spread concurrent retries.
func (r *Relay) backoff(attempts int) time.Duration {
	d := r.cfg.InitialBackoff
	for i := 1; i < attempts && d < r.cfg.MaxBackoff; i++ {
		d *= 2
	}
	if d > r.cfg.MaxBackoff {
		d = r.cfg.MaxBackoff
	}
	return r.cfg.Jitter(d)
}
