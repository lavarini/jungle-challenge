package metered

import (
	"context"
	"time"
)

// Resolver is the reference worker's port (refworker.Resolver).
type Resolver interface {
	Claim(ctx context.Context, limit int, lease time.Duration) ([]string, error)
	Resolve(ctx context.Context, id string) error
}

// ResolverRecorder is the part of metrics.Metrics the resolver uses.
type ResolverRecorder interface {
	ReferenceLockConflict(reason string)
}

type resolver struct {
	next Resolver
	m    ResolverRecorder
}

// NewResolver counts the lock timeouts (55P03) and deadlocks (40P01) the
// reference worker meets; both are retried after the claim's lease.
func NewResolver(next Resolver, m ResolverRecorder) Resolver { return resolver{next: next, m: m} }

func (r resolver) Claim(ctx context.Context, limit int, lease time.Duration) ([]string, error) {
	ids, err := r.next.Claim(ctx, limit, lease)
	r.record(err)
	return ids, err
}

func (r resolver) Resolve(ctx context.Context, id string) error {
	err := r.next.Resolve(ctx, id)
	r.record(err)
	return err
}

func (r resolver) record(err error) {
	switch pgCode(err) {
	case "55P03":
		r.m.ReferenceLockConflict("lock_timeout")
	case "40P01":
		r.m.ReferenceLockConflict("deadlock")
	}
}
