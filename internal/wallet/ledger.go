package wallet

import (
	"fmt"
	"time"

	"github.com/lavarini/backend-challenge-go/internal/money"
)

type Direction string

const (
	Debit  Direction = "DEBIT"
	Credit Direction = "CREDIT"
)

func (d Direction) Valid() bool { return d == Debit || d == Credit }

type LedgerEntryParams struct {
	ID, WalletID, TransactionID string
	Direction                   Direction
	Amount                      money.Money
	BalanceBefore               money.Money
	BalanceAfter                money.Money
	WalletVersion               int64
	CreatedAt                   time.Time
}

// LedgerEntry is the immutable proof of one balance movement.
type LedgerEntry struct {
	p LedgerEntryParams
}

// NewLedgerEntry validates balanceAfter = balanceBefore ± amount by direction.
func NewLedgerEntry(p LedgerEntryParams) (LedgerEntry, error) {
	if p.ID == "" || p.WalletID == "" || p.TransactionID == "" || p.CreatedAt.IsZero() {
		return LedgerEntry{}, fmt.Errorf("%w: missing identity or timestamp", ErrInvalidEntry)
	}
	if !p.Direction.Valid() {
		return LedgerEntry{}, fmt.Errorf("%w: direction %q", ErrInvalidEntry, p.Direction)
	}
	if p.WalletVersion < 1 {
		return LedgerEntry{}, fmt.Errorf("%w: wallet version %d", ErrInvalidEntry, p.WalletVersion)
	}
	if !p.Amount.IsPositive() {
		return LedgerEntry{}, fmt.Errorf("%w: amount must be positive", ErrInvalidEntry)
	}
	if p.BalanceBefore.IsNegative() || p.BalanceAfter.IsNegative() {
		return LedgerEntry{}, fmt.Errorf("%w: negative balance", ErrInvalidEntry)
	}
	var expected money.Money
	var err error
	if p.Direction == Credit {
		expected, err = p.BalanceBefore.Add(p.Amount)
	} else {
		expected, err = p.BalanceBefore.Sub(p.Amount)
	}
	if err != nil {
		return LedgerEntry{}, fmt.Errorf("%w: %w", ErrInvalidEntry, err)
	}
	if c, err := expected.Cmp(p.BalanceAfter); err != nil || c != 0 {
		return LedgerEntry{}, fmt.Errorf("%w: balance after does not match movement", ErrInvalidEntry)
	}
	p.CreatedAt = p.CreatedAt.UTC()
	return LedgerEntry{p: p}, nil
}

func (e LedgerEntry) ID() string                 { return e.p.ID }
func (e LedgerEntry) WalletID() string           { return e.p.WalletID }
func (e LedgerEntry) TransactionID() string      { return e.p.TransactionID }
func (e LedgerEntry) Direction() Direction       { return e.p.Direction }
func (e LedgerEntry) Amount() money.Money        { return e.p.Amount }
func (e LedgerEntry) BalanceBefore() money.Money { return e.p.BalanceBefore }
func (e LedgerEntry) BalanceAfter() money.Money  { return e.p.BalanceAfter }
func (e LedgerEntry) WalletVersion() int64       { return e.p.WalletVersion }
func (e LedgerEntry) CreatedAt() time.Time       { return e.p.CreatedAt }
