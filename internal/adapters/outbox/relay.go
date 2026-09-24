// Package outbox publishes committed events. Only the head of each partition
// (wallet) is claimable, so events of one wallet leave in commit order even
// with several relays (ADR 0014).
package outbox

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/lavarini/backend-challenge-go/internal/app"
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
}

type Relay struct {
	store Store
	pub   Publisher
	cfg   Config
	now   func() time.Time
	newID func() string
	log   *slog.Logger
}

func New(store Store, pub Publisher, cfg Config, now func() time.Time, newID func() string, log *slog.Logger) *Relay {
	if cfg.Jitter == nil {
		cfg.Jitter = app.UpToTwentyPercent
	}
	return &Relay{store: store, pub: pub, cfg: cfg, now: now, newID: newID, log: log}
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
	perr := r.pub.Publish(pctx, m)
	cancel()
	var applied bool
	var serr error
	switch {
	case perr == nil:
		applied, serr = r.store.Ack(ctx, m.Seq, claim, r.now())
	case errors.Is(perr, ErrPermanent) && m.Attempts >= r.cfg.MaxPermanentAttempts:
		log.ErrorContext(ctx, "event quarantined", "error", perr.Error(), "class", "permanent")
		applied, serr = r.store.Dead(ctx, m.Seq, claim, r.now(), perr.Error())
	default:
		log.WarnContext(ctx, "publish failed; will retry", "error", perr.Error())
		applied, serr = r.store.Retry(ctx, m.Seq, claim, r.now().Add(r.backoff(m.Attempts)), perr.Error())
	}
	if serr != nil {
		log.WarnContext(ctx, "outbox bookkeeping failed; the lease will expire and the event will be retried", "error", serr.Error())
		return
	}
	if !applied {
		log.WarnContext(ctx, "claim lost to another relay; its outcome stands")
	}
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
