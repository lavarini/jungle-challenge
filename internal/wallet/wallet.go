// Package wallet holds the financial aggregate: balance, version and the
// ledger entries that prove every movement.
package wallet

import (
	"fmt"
	"time"

	"github.com/lavarini/backend-challenge-go/internal/money"
)

type Wallet struct {
	id, playerID string
	balance      money.Money
	version      int64
	createdAt    time.Time
	updatedAt    time.Time
}

type Snapshot struct {
	ID, PlayerID string
	Balance      money.Money
	Version      int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type EntryRef struct {
	EntryID, TransactionID string
}

// Open creates a wallet at version 1 holding the initial balance.
func Open(id, playerID string, initial money.Money, now time.Time) (*Wallet, error) {
	if id == "" || playerID == "" || now.IsZero() {
		return nil, fmt.Errorf("%w: missing identity or timestamp", ErrInvalidWallet)
	}
	if !initial.Valid() || initial.IsNegative() {
		return nil, fmt.Errorf("%w: initial balance must be a non-negative amount", ErrInvalidWallet)
	}
	now = now.UTC()
	return &Wallet{id: id, playerID: playerID, balance: initial, version: 1, createdAt: now, updatedAt: now}, nil
}

// Rehydrate restores persisted state without replaying movements.
func Rehydrate(s Snapshot) (*Wallet, error) {
	if s.ID == "" || s.PlayerID == "" || s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		return nil, fmt.Errorf("%w: missing identity or timestamp", ErrInvalidWallet)
	}
	if !s.Balance.Valid() || s.Balance.IsNegative() {
		return nil, fmt.Errorf("%w: invalid balance", ErrInvalidWallet)
	}
	if s.Version < 1 {
		return nil, fmt.Errorf("%w: version %d", ErrInvalidWallet, s.Version)
	}
	return &Wallet{id: s.ID, playerID: s.PlayerID, balance: s.Balance, version: s.Version, createdAt: s.CreatedAt, updatedAt: s.UpdatedAt}, nil
}

func (w *Wallet) Snapshot() Snapshot {
	return Snapshot{ID: w.id, PlayerID: w.playerID, Balance: w.balance, Version: w.version, CreatedAt: w.createdAt, UpdatedAt: w.updatedAt}
}

func (w *Wallet) ID() string               { return w.id }
func (w *Wallet) PlayerID() string         { return w.playerID }
func (w *Wallet) Currency() money.Currency { return w.balance.Currency() }
func (w *Wallet) Balance() money.Money     { return w.balance }
func (w *Wallet) Version() int64           { return w.version }

func (w *Wallet) Debit(amount money.Money, ref EntryRef, now time.Time) (LedgerEntry, error) {
	return w.apply(Debit, amount, ref, now)
}

func (w *Wallet) Credit(amount money.Money, ref EntryRef, now time.Time) (LedgerEntry, error) {
	return w.apply(Credit, amount, ref, now)
}

// OpeningEntry is the credit that proves the initial balance of a new wallet.
// It does not advance the version: the wallet is created at version 1.
func (w *Wallet) OpeningEntry(ref EntryRef, now time.Time) (LedgerEntry, error) {
	if w.version != 1 || !w.balance.IsPositive() {
		return LedgerEntry{}, fmt.Errorf("%w: opening entry requires a new wallet with positive balance", ErrInvalidEntry)
	}
	zero, err := money.Zero(w.Currency())
	if err != nil {
		return LedgerEntry{}, err
	}
	return NewLedgerEntry(LedgerEntryParams{
		ID: ref.EntryID, WalletID: w.id, TransactionID: ref.TransactionID, Direction: Credit,
		Amount: w.balance, BalanceBefore: zero, BalanceAfter: w.balance, WalletVersion: 1, CreatedAt: now,
	})
}

func (w *Wallet) apply(dir Direction, amount money.Money, ref EntryRef, now time.Time) (LedgerEntry, error) {
	if amount.Currency() != w.Currency() {
		return LedgerEntry{}, fmt.Errorf("%w: wallet %s, amount %s", ErrCurrencyMismatch, w.Currency(), amount.Currency())
	}
	if !amount.IsPositive() {
		return LedgerEntry{}, ErrNonPositiveAmount
	}
	var after money.Money
	var err error
	if dir == Debit {
		after, err = w.balance.Sub(amount)
	} else {
		after, err = w.balance.Add(amount)
	}
	if err != nil {
		return LedgerEntry{}, err
	}
	if after.IsNegative() {
		return LedgerEntry{}, ErrInsufficientFunds
	}
	entry, err := NewLedgerEntry(LedgerEntryParams{
		ID: ref.EntryID, WalletID: w.id, TransactionID: ref.TransactionID, Direction: dir,
		Amount: amount, BalanceBefore: w.balance, BalanceAfter: after, WalletVersion: w.version + 1, CreatedAt: now,
	})
	if err != nil {
		return LedgerEntry{}, err
	}
	w.balance = after
	w.version++
	w.updatedAt = now.UTC()
	return entry, nil
}
