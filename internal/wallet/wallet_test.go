package wallet

import (
	"errors"
	"testing"
	"time"

	"github.com/lavarini/backend-challenge-go/internal/money"
)

func TestOpenStartsAtVersionOne(t *testing.T) {
	w, err := Open("w1", "p1", brl(t, "100.00"), t0)
	if err != nil {
		t.Fatal(err)
	}
	if w.Version() != 1 || w.Balance().String() != "100.00" || w.Currency() != money.BRL {
		t.Fatalf("unexpected wallet %+v", w.Snapshot())
	}
}

func TestOpenRejectsInvalidInput(t *testing.T) {
	neg, _ := money.FromMinor(-1, money.BRL)
	cases := map[string]func() (*Wallet, error){
		"missing id":       func() (*Wallet, error) { return Open("", "p1", brl(t, "1.00"), t0) },
		"missing player":   func() (*Wallet, error) { return Open("w1", "", brl(t, "1.00"), t0) },
		"negative balance": func() (*Wallet, error) { return Open("w1", "p1", neg, t0) },
		"zero money value": func() (*Wallet, error) { return Open("w1", "p1", money.Money{}, t0) },
		"missing time":     func() (*Wallet, error) { return Open("w1", "p1", brl(t, "1.00"), time.Time{}) },
	}
	for name, open := range cases {
		if _, err := open(); !errors.Is(err, ErrInvalidWallet) {
			t.Errorf("%s: error = %v, want ErrInvalidWallet", name, err)
		}
	}
}

func TestDebitProducesEntryAndAdvancesVersion(t *testing.T) {
	w, _ := Open("w1", "p1", brl(t, "100.00"), t0)
	later := t0.Add(time.Minute)
	e, err := w.Debit(brl(t, "80.00"), EntryRef{EntryID: "e1", TransactionID: "t1"}, later)
	if err != nil {
		t.Fatal(err)
	}
	if w.Balance().String() != "20.00" || w.Version() != 2 {
		t.Fatalf("wallet after debit: %+v", w.Snapshot())
	}
	if e.BalanceBefore().String() != "100.00" || e.BalanceAfter().String() != "20.00" || e.WalletVersion() != 2 || e.Direction() != Debit {
		t.Fatalf("entry: %+v", e)
	}
	if !w.Snapshot().UpdatedAt.Equal(later) {
		t.Fatalf("updatedAt not advanced")
	}
}

func TestDebitWithoutFundsLeavesWalletUntouched(t *testing.T) {
	w, _ := Open("w1", "p1", brl(t, "20.00"), t0)
	_, err := w.Debit(brl(t, "80.00"), EntryRef{EntryID: "e1", TransactionID: "t1"}, t0)
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("error = %v", err)
	}
	if w.Balance().String() != "20.00" || w.Version() != 1 {
		t.Fatalf("wallet changed: %+v", w.Snapshot())
	}
}

func TestMovementRejectsCurrencyMismatchAndNonPositive(t *testing.T) {
	w, _ := Open("w1", "p1", brl(t, "20.00"), t0)
	usd, _ := money.Parse("1.00", "USD")
	ref := EntryRef{EntryID: "e1", TransactionID: "t1"}
	if _, err := w.Credit(usd, ref, t0); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("credit usd error = %v", err)
	}
	if _, err := w.Credit(brl(t, "0.00"), ref, t0); !errors.Is(err, ErrNonPositiveAmount) {
		t.Errorf("credit zero error = %v", err)
	}
	if w.Version() != 1 {
		t.Fatal("version changed on rejected movement")
	}
}

func TestCreditOverflowIsRejected(t *testing.T) {
	maxM, _ := money.Parse("92233720368547758.07", "BRL")
	w, _ := Open("w1", "p1", maxM, t0)
	if _, err := w.Credit(brl(t, "0.01"), EntryRef{EntryID: "e1", TransactionID: "t1"}, t0); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("error = %v, want money.ErrOverflow", err)
	}
}

func TestOpeningEntry(t *testing.T) {
	w, _ := Open("w1", "p1", brl(t, "1000.00"), t0)
	e, err := w.OpeningEntry(EntryRef{EntryID: "e0", TransactionID: "t0"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if e.Direction() != Credit || e.BalanceBefore().String() != "0.00" || e.BalanceAfter().String() != "1000.00" || e.WalletVersion() != 1 {
		t.Fatalf("opening entry: %+v", e)
	}
	if w.Version() != 1 {
		t.Fatal("opening entry must not advance the version")
	}

	empty, _ := Open("w2", "p1", brl(t, "0.00"), t0)
	if _, err := empty.OpeningEntry(EntryRef{EntryID: "e0", TransactionID: "t0"}, t0); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("zero opening error = %v", err)
	}
}

func TestRehydrateDoesNotApplyMovements(t *testing.T) {
	s := Snapshot{ID: "w1", PlayerID: "p1", Balance: brl(t, "20.00"), Version: 7, CreatedAt: t0, UpdatedAt: t0}
	w, err := Rehydrate(s)
	if err != nil {
		t.Fatal(err)
	}
	if w.Snapshot() != s {
		t.Fatalf("rehydrated %+v, want %+v", w.Snapshot(), s)
	}
	bad := s
	bad.Version = 0
	if _, err := Rehydrate(bad); !errors.Is(err, ErrInvalidWallet) {
		t.Fatalf("version 0 error = %v", err)
	}
	bad = s
	bad.Balance, _ = money.FromMinor(-1, money.BRL)
	if _, err := Rehydrate(bad); !errors.Is(err, ErrInvalidWallet) {
		t.Fatalf("negative error = %v", err)
	}
}
