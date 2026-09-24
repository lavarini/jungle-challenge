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
	hooks   ResolveHooks
}

// ResolveHooks let the composition root observe outcomes without app knowing
// metrics. Nil funcs are skipped. They run after the commit.
type ResolveHooks struct {
	// Concluded reports an operation that left PENDING_REFERENCE: settled
	// (PROCESSED or REJECTED), expired (REJECTED, REFERENCE_NOT_FOUND) or
	// failed (FAILED, ADR 0011).
	Concluded func(kind wagering.Kind, status wagering.Status)
	// InvariantViolated reports a database invariant violation met while
	// resolving (the asynchronous path).
	InvariantViolated func()
}

func NewResolvePending(uow UnitOfWork, clock Clock, ids IDGenerator, policy ReferencePolicy, log *slog.Logger, hooks ResolveHooks) *ResolvePending {
	return &ResolvePending{uow: uow, clock: clock, policy: policy, settler: newSettler(ids, policy), log: log, hooks: hooks}
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
	var concluded *wagering.Transaction
	err := r.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		concluded, err = r.resolve(ctx, tx, id)
		return err
	})
	if err == nil {
		r.concluded(concluded)
	}
	if !errors.Is(err, ErrInvariantViolation) {
		return err
	}
	r.log.ErrorContext(ctx, "pending operation violated a database invariant", "transactionId", id, "error", err.Error(), "class", "permanent")
	if r.hooks.InvariantViolated != nil {
		r.hooks.InvariantViolated()
	}
	failed, ferr := r.fail(ctx, id)
	if ferr != nil {
		return errors.Join(err, ferr)
	}
	r.concluded(failed)
	return err
}

func (r *ResolvePending) concluded(t *wagering.Transaction) {
	if t != nil && r.hooks.Concluded != nil {
		r.hooks.Concluded(t.Kind(), t.Status())
	}
}

// resolve returns the transaction when it left PENDING_REFERENCE, nil when it
// was rescheduled or was no longer pending.
func (r *ResolvePending) resolve(ctx context.Context, tx Tx, id string) (*wagering.Transaction, error) {
	peek, err := tx.Transactions().Get(ctx, id)
	if err != nil || peek == nil || peek.Status() != wagering.PendingReference {
		return nil, err
	}
	// Lock order is always wallet, then transaction (ADR 0008).
	w, err := tx.Wallets().GetForUpdate(ctx, peek.WalletID())
	if err != nil {
		return nil, err
	}
	t, err := tx.Transactions().GetForUpdate(ctx, id)
	if err != nil || t == nil || t.Status() != wagering.PendingReference {
		return nil, err
	}
	now := r.clock.Now()
	d, ref, err := decide(ctx, tx, t, w.Balance())
	if err != nil {
		return nil, err
	}
	if d.Status == wagering.PendingReference {
		if now.Before(t.DeadlineAt()) {
			next := r.policy.NextAttempt(t.Attempts()+1, now)
			if next.After(t.DeadlineAt()) {
				next = t.DeadlineAt()
			}
			if err := t.Reschedule(next, now); err != nil {
				return nil, fmt.Errorf("%w: %w", ErrInvariantViolation, err)
			}
			return nil, tx.Transactions().Update(ctx, t)
		}
		d = wagering.Decision{Status: wagering.Rejected, FailureCode: wagering.ReferenceNotFound}
	}
	causation := ""
	if ref != nil {
		causation = ref.TransactionID
	}
	return t, r.settler.settle(ctx, tx, w, t, d, causation, now, tx.Transactions().Update)
}

// fail returns the transaction it moved to FAILED, nil when it was no longer
// pending.
func (r *ResolvePending) fail(ctx context.Context, id string) (*wagering.Transaction, error) {
	var failed *wagering.Transaction
	err := r.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
		failed = nil
		t, err := tx.Transactions().GetForUpdate(ctx, id)
		if err != nil || t == nil || t.Status() != wagering.PendingReference {
			return err
		}
		if err := t.Fail(wagering.InvariantViolation, r.clock.Now()); err != nil {
			return err
		}
		if err := tx.Transactions().Update(ctx, t); err != nil {
			return err
		}
		failed = t
		return nil
	})
	if err != nil {
		return nil, err
	}
	return failed, nil
}
