package app

import (
	"errors"
	"math/rand/v2"
	"time"
)

// ReferencePolicy schedules retries of operations waiting for a reference.
// The deadline (TTL) is the only terminal criterion; attempts are a
// consequence of the backoff (ADR 0010).
type ReferencePolicy struct {
	TTL            time.Duration
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	// Jitter stretches a delay to spread retries; nil disables it.
	Jitter func(time.Duration) time.Duration
}

func DefaultReferencePolicy() ReferencePolicy {
	return ReferencePolicy{TTL: 24 * time.Hour, InitialBackoff: time.Second, MaxBackoff: 5 * time.Minute, Jitter: UpToTwentyPercent}
}

// UpToTwentyPercent adds between 0 and 20% to d.
func UpToTwentyPercent(d time.Duration) time.Duration {
	return d + time.Duration(rand.Int64N(int64(d)/5+1))
}

func (p ReferencePolicy) Validate() error {
	if p.TTL <= 0 || p.InitialBackoff <= 0 || p.MaxBackoff < p.InitialBackoff {
		return errors.New("reference policy: TTL and backoff must be positive and max backoff >= initial")
	}
	return nil
}

// NextAttempt returns when to retry after the given number of failed attempts.
func (p ReferencePolicy) NextAttempt(attempts int, now time.Time) time.Time {
	d := p.InitialBackoff
	for i := 0; i < attempts && d < p.MaxBackoff; i++ {
		d *= 2
	}
	if d > p.MaxBackoff {
		d = p.MaxBackoff
	}
	if p.Jitter != nil {
		d = p.Jitter(d)
	}
	return now.Add(d)
}
