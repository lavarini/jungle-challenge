package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

// ResolvePending finishes operations waiting for a reference. Any instance
// may run it; claims are leased in the database (ADR 0010).
type ResolvePending struct {
	uow     UnitOfWork
	clock   Clock
	policy  ReferencePolicy
	settler settler
	log     *slog.Logger
}

func NewResolvePending(uow UnitOfWork, clock Clock, ids IDGenerator, policy ReferencePolicy, log *slog.Logger) *ResolvePending {
	return &ResolvePending{uow: uow, clock: clock, policy: policy, settler: newSettler(ids, policy), log: log}
}

// Claim leases due pending operations so other instances skip them until
// the lease expires.
func (r *ResolvePending) Claim(ctx context.Context, limit int, lease time.Duration) ([]string, error) {
	now := r.clock.Now()
	var ids []string
	err := r.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		ids, err = tx.Transactions().ClaimDuePending(ctx, now, now.Add(lease), limit)
		return err
	})
	return ids, err
}

// Resolve re-evaluates one pending operation. A database invariant violation
// is permanent: the operation becomes FAILED and is not retried (ADR 0011).
func (r *ResolvePending) Resolve(ctx context.Context, id string) error {
	err := r.uow.Do(ctx, func(ctx context.Context, tx Tx) error { return r.resolve(ctx, tx, id) })
	if !errors.Is(err, ErrInvariantViolation) {
		return err
	}
	r.log.ErrorContext(ctx, "pending operation violated a database invariant", "transactionId", id, "error", err.Error(), "class", "permanent")
	if ferr := r.fail(ctx, id); ferr != nil {
		return errors.Join(err, ferr)
	}
	return err
}

func (r *ResolvePending) resolve(ctx context.Context, tx Tx, id string) error {
	peek, err := tx.Transactions().Get(ctx, id)
	if err != nil || peek == nil || peek.Status() != wagering.PendingReference {
		return err
	}
	// Lock order is always wallet, then transaction (ADR 0008).
	w, err := tx.Wallets().GetForUpdate(ctx, peek.WalletID())
	if err != nil {
		return err
	}
	t, err := tx.Transactions().GetForUpdate(ctx, id)
	if err != nil || t == nil || t.Status() != wagering.PendingReference {
		return err
	}
	now := r.clock.Now()
	d, ref, err := decide(ctx, tx, t, w.Balance())
	if err != nil {
		return err
	}
	if d.Status == wagering.PendingReference {
		if now.Before(t.DeadlineAt()) {
			next := r.policy.NextAttempt(t.Attempts()+1, now)
			if next.After(t.DeadlineAt()) {
				next = t.DeadlineAt()
			}
			if err := t.Reschedule(next, now); err != nil {
				return fmt.Errorf("%w: %w", ErrInvariantViolation, err)
			}
			return tx.Transactions().Update(ctx, t)
		}
		d = wagering.Decision{Status: wagering.Rejected, FailureCode: wagering.ReferenceNotFound}
	}
	causation := ""
	if ref != nil {
		causation = ref.TransactionID
	}
	return r.settler.settle(ctx, tx, w, t, d, causation, now, tx.Transactions().Update)
}

func (r *ResolvePending) fail(ctx context.Context, id string) error {
	return r.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
		t, err := tx.Transactions().GetForUpdate(ctx, id)
		if err != nil || t == nil || t.Status() != wagering.PendingReference {
			return err
		}
		if err := t.Fail(wagering.InvariantViolation, r.clock.Now()); err != nil {
			return err
		}
		return tx.Transactions().Update(ctx, t)
	})
}
