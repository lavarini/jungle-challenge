package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/lavarini/backend-challenge-go/internal/money"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
	"github.com/lavarini/backend-challenge-go/internal/wallet"
)

type SubmitWager struct {
	uow    UnitOfWork
	clock  Clock
	ids    IDGenerator
	events eventFactory
}

func NewSubmitWager(uow UnitOfWork, clock Clock, ids IDGenerator) *SubmitWager {
	return &SubmitWager{uow: uow, clock: clock, ids: ids, events: eventFactory{ids: ids}}
}

// Execute applies a provider operation exactly once. HTTP and SQS share it
// (ADR 0008). A concurrent insert of the same identity is retried once and
// resolves as a replay.
func (s *SubmitWager) Execute(ctx context.Context, cmd SubmitCommand) (SubmitResult, error) {
	if err := cmd.validate(); err != nil {
		return SubmitResult{}, err
	}
	if cmd.Kind.RequiresReference() || cmd.ReferenceExternalTransactionID != "" {
		return SubmitResult{}, fmt.Errorf("%w: operations with references", ErrNotImplemented)
	}
	hash, err := CanonicalHash(cmd)
	if err != nil {
		return SubmitResult{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	var result SubmitResult
	for attempt := 0; ; attempt++ {
		err = s.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
			var inner error
			result, inner = s.submit(ctx, tx, cmd, hash)
			return inner
		})
		if errors.Is(err, ErrUniqueConflict) && attempt == 0 {
			continue
		}
		return result, err
	}
}

func (s *SubmitWager) submit(ctx context.Context, tx Tx, cmd SubmitCommand, hash []byte) (SubmitResult, error) {
	if res, found, err := s.existing(ctx, tx, cmd, hash); err != nil || found {
		return res, err
	}
	w, err := tx.Wallets().GetForUpdate(ctx, cmd.WalletID)
	if err != nil {
		return SubmitResult{}, err
	}
	// Re-check under the wallet lock: a concurrent twin may have committed.
	if res, found, err := s.existing(ctx, tx, cmd, hash); err != nil || found {
		return res, err
	}
	if w.PlayerID() != cmd.PlayerID {
		return SubmitResult{}, fmt.Errorf("%w: player", ErrWalletMismatch)
	}
	if w.Currency() != cmd.Money.Currency() {
		return SubmitResult{}, fmt.Errorf("%w: currency", ErrWalletMismatch)
	}

	now := s.clock.Now()
	t, err := wagering.NewExternal(wagering.NewExternalParams{
		ID: s.ids.New(), WalletID: w.ID(), PlayerID: w.PlayerID(), CorrelationID: cmd.CorrelationID,
		Kind: cmd.Kind, Amount: cmd.Money, Now: now,
		Provider: wagering.ProviderRef{
			ProviderID: cmd.ProviderID, ExternalID: cmd.ExternalTransactionID, IdempotencyKey: cmd.IdempotencyKey,
			PayloadHash: hash, RoundID: cmd.RoundID, GameID: cmd.GameID,
			ReferenceExternalID: cmd.ReferenceExternalTransactionID,
		},
	})
	if err != nil {
		return SubmitResult{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	decision, err := wagering.Decide(t, w.Balance())
	if err != nil {
		return SubmitResult{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}

	var entry *wallet.LedgerEntry
	ref := wallet.EntryRef{EntryID: s.ids.New(), TransactionID: t.ID()}
	switch decision.Effect {
	case wagering.DebitEffect:
		e, err := w.Debit(t.Amount(), ref, now)
		if err != nil {
			return SubmitResult{}, movementError(err)
		}
		entry = &e
	case wagering.CreditEffect:
		e, err := w.Credit(t.Amount(), ref, now)
		if err != nil {
			return SubmitResult{}, movementError(err)
		}
		entry = &e
	}

	if decision.Status == wagering.Processed {
		err = t.Process(w.Balance(), w.Version(), now)
	} else {
		err = t.Reject(decision.FailureCode, w.Balance(), w.Version(), now)
	}
	if err != nil {
		return SubmitResult{}, fmt.Errorf("%w: %w", ErrInvariantViolation, err)
	}

	// Write order is a contract with the ledger trigger (ADR 0004):
	// transaction, then wallet, then ledger entry.
	if err := tx.Transactions().Insert(ctx, t); err != nil {
		return SubmitResult{}, err
	}
	if entry != nil {
		if err := tx.Wallets().UpdateBalance(ctx, w); err != nil {
			return SubmitResult{}, err
		}
		if err := tx.Ledger().Insert(ctx, *entry); err != nil {
			return SubmitResult{}, err
		}
	}
	evs, err := s.events.outcome(t, entry, cmd.CausationID, now)
	if err != nil {
		return SubmitResult{}, err
	}
	if err := tx.Outbox().Append(ctx, evs...); err != nil {
		return SubmitResult{}, err
	}
	return resultOf(t, false), nil
}

// existing resolves idempotency: same key and hash replays; same key with a
// different hash conflicts; same operation under another key conflicts (ADR 0007).
func (s *SubmitWager) existing(ctx context.Context, tx Tx, cmd SubmitCommand, hash []byte) (SubmitResult, bool, error) {
	byKey, err := tx.Transactions().FindByIdempotencyKey(ctx, cmd.ProviderID, cmd.IdempotencyKey)
	if err != nil {
		return SubmitResult{}, false, err
	}
	if byKey != nil {
		if !bytes.Equal(byKey.Provider().PayloadHash, hash) {
			return SubmitResult{}, false, ErrIdempotencyPayloadMismatch
		}
		return resultOf(byKey, true), true, nil
	}
	byExternal, err := tx.Transactions().FindByExternalID(ctx, cmd.ProviderID, cmd.ExternalTransactionID)
	if err != nil {
		return SubmitResult{}, false, err
	}
	if byExternal != nil {
		return SubmitResult{}, false, &KeyMismatchError{ExistingTransactionID: byExternal.ID()}
	}
	return SubmitResult{}, false, nil
}

func resultOf(t *wagering.Transaction, replay bool) SubmitResult {
	balance, version, _ := t.Result()
	return SubmitResult{
		TransactionID: t.ID(), Status: t.Status(), FailureCode: t.FailureCode(),
		Balance: balance, WalletVersion: version, IdempotentReplay: replay,
	}
}

// movementError maps a wallet movement failure. Decide already checked funds,
// so only an amount that overflows the balance is an input problem.
func movementError(err error) error {
	if errors.Is(err, money.ErrOverflow) {
		return fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	return fmt.Errorf("%w: %w", ErrInvariantViolation, err)
}
