package app

import (
	"context"
	"fmt"

	"github.com/lavarini/backend-challenge-go/internal/money"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
	"github.com/lavarini/backend-challenge-go/internal/wallet"
)

type OpenWalletCommand struct {
	PlayerID       string
	InitialBalance money.Money
	CorrelationID  string
}

type WalletView struct {
	ID       string
	PlayerID string
	Balance  money.Money
	Version  int64
}

func viewOf(w *wallet.Wallet) WalletView {
	return WalletView{ID: w.ID(), PlayerID: w.PlayerID(), Balance: w.Balance(), Version: w.Version()}
}

type OpenWallet struct {
	uow    UnitOfWork
	clock  Clock
	ids    IDGenerator
	events eventFactory
}

func NewOpenWallet(uow UnitOfWork, clock Clock, ids IDGenerator) *OpenWallet {
	return &OpenWallet{uow: uow, clock: clock, ids: ids, events: eventFactory{ids: ids}}
}

// Execute creates the wallet. A positive initial balance also creates the
// OPENING transaction, its credit and both events, in the same commit.
func (o *OpenWallet) Execute(ctx context.Context, cmd OpenWalletCommand) (WalletView, error) {
	if cmd.CorrelationID == "" {
		return WalletView{}, fmt.Errorf("%w: correlationId is required", ErrInvalidInput)
	}
	now := o.clock.Now()
	w, err := wallet.Open(o.ids.New(), cmd.PlayerID, cmd.InitialBalance, now)
	if err != nil {
		return WalletView{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	err = o.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
		if err := tx.Wallets().Insert(ctx, w); err != nil {
			return err
		}
		if !w.Balance().IsPositive() {
			return nil
		}
		t, err := wagering.NewOpening(o.ids.New(), w.ID(), w.PlayerID(), cmd.CorrelationID, w.Balance(), now)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvariantViolation, err)
		}
		if err := t.Process(w.Balance(), w.Version(), now); err != nil {
			return fmt.Errorf("%w: %w", ErrInvariantViolation, err)
		}
		if err := tx.Transactions().Insert(ctx, t); err != nil {
			return err
		}
		entry, err := w.OpeningEntry(wallet.EntryRef{EntryID: o.ids.New(), TransactionID: t.ID()}, now)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvariantViolation, err)
		}
		if err := tx.Ledger().Insert(ctx, entry); err != nil {
			return err
		}
		evs, err := o.events.outcome(t, &entry, "", now)
		if err != nil {
			return err
		}
		return tx.Outbox().Append(ctx, evs...)
	})
	if err != nil {
		return WalletView{}, err
	}
	return viewOf(w), nil
}
