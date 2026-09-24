package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lavarini/backend-challenge-go/internal/money"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
	"github.com/lavarini/backend-challenge-go/internal/wallet"
)

// writeFunc persists the transaction row: Insert for a new operation,
// Update for a pending one being resolved.
type writeFunc func(ctx context.Context, t *wagering.Transaction) error

// settler applies a decision. SubmitWager and ResolvePending share it so both
// paths move money, write the ledger and emit events identically.
type settler struct {
	ids    IDGenerator
	events eventFactory
	policy ReferencePolicy
}

func newSettler(ids IDGenerator, policy ReferencePolicy) settler {
	return settler{ids: ids, events: eventFactory{ids: ids}, policy: policy}
}

// settle persists in the order the ledger trigger requires (ADR 0004):
// transaction, wallet, ledger entry; then outbox events; then wakes the
// operations waiting for this one.
func (st settler) settle(ctx context.Context, tx Tx, w *wallet.Wallet, t *wagering.Transaction, d wagering.Decision, causationID string, now time.Time, write writeFunc) error {
	if d.Status == wagering.PendingReference {
		return st.await(ctx, tx, t, causationID, now, write)
	}

	var entry *wallet.LedgerEntry
	ref := wallet.EntryRef{EntryID: st.ids.New(), TransactionID: t.ID()}
	switch d.Effect {
	case wagering.DebitEffect:
		e, err := w.Debit(t.Amount(), ref, now)
		if err != nil {
			return movementError(err)
		}
		entry = &e
	case wagering.CreditEffect:
		e, err := w.Credit(t.Amount(), ref, now)
		if err != nil {
			return movementError(err)
		}
		entry = &e
	}

	var err error
	if d.Status == wagering.Processed {
		err = t.Process(w.Balance(), w.Version(), now)
	} else {
		err = t.Reject(d.FailureCode, w.Balance(), w.Version(), now)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvariantViolation, err)
	}

	if err := write(ctx, t); err != nil {
		return err
	}
	if entry != nil {
		if err := tx.Wallets().UpdateBalance(ctx, w); err != nil {
			return err
		}
		if err := tx.Ledger().Insert(ctx, *entry); err != nil {
			return err
		}
	}
	evs, err := st.events.outcome(t, entry, causationID, now)
	if err != nil {
		return err
	}
	if err := tx.Outbox().Append(ctx, evs...); err != nil {
		return err
	}
	if t.Origin() != wagering.OriginExternal {
		return nil
	}
	p := t.Provider()
	return tx.Transactions().WakePending(ctx, p.ProviderID, p.ExternalID, now)
}

func (st settler) await(ctx context.Context, tx Tx, t *wagering.Transaction, causationID string, now time.Time, write writeFunc) error {
	if err := t.AwaitReference(st.policy.NextAttempt(0, now), now.Add(st.policy.TTL), now); err != nil {
		return fmt.Errorf("%w: %w", ErrInvariantViolation, err)
	}
	if err := write(ctx, t); err != nil {
		return err
	}
	ev, err := st.events.pendingReference(t, causationID, now)
	if err != nil {
		return err
	}
	return tx.Outbox().Append(ctx, ev)
}

// decide applies the rule for t, resolving its reference when it has one.
// It returns the reference it used (nil when absent or not needed).
func decide(ctx context.Context, tx Tx, t *wagering.Transaction, available money.Money) (wagering.Decision, *wagering.Reference, error) {
	p := t.Provider()
	if !t.Kind().RequiresReference() && p.ReferenceExternalID == "" {
		d, err := wagering.Decide(t, available)
		if err != nil {
			return wagering.Decision{}, nil, fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		return d, nil, nil
	}
	ref, err := loadReference(ctx, tx, p.ProviderID, p.ReferenceExternalID)
	if err != nil {
		return wagering.Decision{}, nil, err
	}
	if ref != nil && ref.Status != wagering.PendingReference {
		if err := t.ResolveReference(ref.TransactionID); err != nil {
			return wagering.Decision{}, nil, fmt.Errorf("%w: %w", ErrInvariantViolation, err)
		}
	}
	d, err := wagering.DecideWithReference(t, available, ref)
	if err != nil {
		return wagering.Decision{}, nil, fmt.Errorf("%w: %w", ErrInvariantViolation, err)
	}
	return d, ref, nil
}

// loadReference resolves (providerID, externalID); nil when it does not exist.
func loadReference(ctx context.Context, tx Tx, providerID, externalID string) (*wagering.Reference, error) {
	refTx, err := tx.Transactions().FindByExternalID(ctx, providerID, externalID)
	if err != nil || refTx == nil {
		return nil, err
	}
	reversed := false
	if refTx.Status() == wagering.Processed {
		if reversed, err = tx.Transactions().HasProcessedReversal(ctx, refTx.ID()); err != nil {
			return nil, err
		}
	}
	ref := wagering.ReferenceOf(refTx, reversed)
	return &ref, nil
}

// movementError maps a wallet movement failure. Decisions already checked
// funds, so only an amount that overflows the balance is an input problem.
func movementError(err error) error {
	if errors.Is(err, money.ErrOverflow) {
		return fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	return fmt.Errorf("%w: %w", ErrInvariantViolation, err)
}
