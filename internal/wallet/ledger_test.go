package wallet

import (
	"errors"
	"testing"
	"time"

	"github.com/lavarini/backend-challenge-go/internal/money"
)

var t0 = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func brl(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func validParams(t *testing.T) LedgerEntryParams {
	return LedgerEntryParams{
		ID: "e1", WalletID: "w1", TransactionID: "t1", Direction: Debit,
		Amount: brl(t, "80.00"), BalanceBefore: brl(t, "100.00"), BalanceAfter: brl(t, "20.00"),
		WalletVersion: 2, CreatedAt: t0,
	}
}

func TestNewLedgerEntryAcceptsConsistentArithmetic(t *testing.T) {
	e, err := NewLedgerEntry(validParams(t))
	if err != nil {
		t.Fatal(err)
	}
	if e.BalanceAfter().String() != "20.00" || e.Direction() != Debit || e.WalletVersion() != 2 {
		t.Fatalf("unexpected entry %+v", e)
	}
}

func TestNewLedgerEntryRejectsInconsistency(t *testing.T) {
	cases := map[string]func(p *LedgerEntryParams){
		"after does not match": func(p *LedgerEntryParams) { p.BalanceAfter = brl(t, "30.00") },
		"wrong direction":      func(p *LedgerEntryParams) { p.Direction = Credit },
		"unknown direction":    func(p *LedgerEntryParams) { p.Direction = "SIDEWAYS" },
		"zero amount":          func(p *LedgerEntryParams) { p.Amount = brl(t, "0.00") },
		"negative after": func(p *LedgerEntryParams) {
			p.BalanceBefore = brl(t, "10.00")
			p.BalanceAfter, _ = money.FromMinor(-7000, money.BRL)
		},
		"missing id":            func(p *LedgerEntryParams) { p.ID = "" },
		"missing transaction":   func(p *LedgerEntryParams) { p.TransactionID = "" },
		"version zero":          func(p *LedgerEntryParams) { p.WalletVersion = 0 },
		"missing timestamp":     func(p *LedgerEntryParams) { p.CreatedAt = time.Time{} },
		"currency mismatch":     func(p *LedgerEntryParams) { p.Amount, _ = money.Parse("80.00", "USD") },
		"uninitialized balance": func(p *LedgerEntryParams) { p.BalanceAfter = money.Money{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := validParams(t)
			mutate(&p)
			if _, err := NewLedgerEntry(p); !errors.Is(err, ErrInvalidEntry) {
				t.Fatalf("error = %v, want ErrInvalidEntry", err)
			}
		})
	}
}
