package wagering

import (
	"errors"
	"testing"

	"github.com/lavarini/backend-challenge-go/internal/money"
)

func withReference(t *testing.T, kind Kind, amount string) *Transaction {
	t.Helper()
	p := externalParams(t, kind, amount)
	p.Provider.ReferenceExternalID = "ref-1"
	tx, err := NewExternal(p)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func processedRef(t *testing.T, kind Kind, amount string) *Reference {
	return &Reference{
		TransactionID: "r1", Kind: kind, Status: Processed,
		ProviderID: "provider-a", PlayerID: "p1", WalletID: "w1", RoundID: "r1",
		Amount: brl(t, amount),
	}
}

func TestDecideWithReference(t *testing.T) {
	cases := []struct {
		name      string
		kind      Kind
		amount    string
		available string
		ref       func() *Reference
		want      Decision
	}{
		{"missing reference waits", Refund, "10.00", "0.00",
			func() *Reference { return nil }, Decision{Status: PendingReference}},
		{"pending reference waits", Refund, "10.00", "0.00",
			func() *Reference { r := processedRef(t, Bet, "10.00"); r.Status = PendingReference; return r },
			Decision{Status: PendingReference}},
		{"rejected reference", Refund, "10.00", "0.00",
			func() *Reference { r := processedRef(t, Bet, "10.00"); r.Status = Rejected; return r },
			Decision{Status: Rejected, FailureCode: ReferenceNotProcessed}},
		{"failed reference", Rollback, "10.00", "0.00",
			func() *Reference { r := processedRef(t, Bet, "10.00"); r.Status = Failed; return r },
			Decision{Status: Rejected, FailureCode: ReferenceNotProcessed}},
		{"refund of bet credits", Refund, "10.00", "0.00",
			func() *Reference { return processedRef(t, Bet, "10.00") }, Decision{Status: Processed, Effect: CreditEffect}},
		{"refund of win is a mismatch", Refund, "10.00", "0.00",
			func() *Reference { return processedRef(t, Win, "10.00") }, Decision{Status: Rejected, FailureCode: ReferenceMismatch}},
		{"refund with other amount", Refund, "9.99", "0.00",
			func() *Reference { return processedRef(t, Bet, "10.00") }, Decision{Status: Rejected, FailureCode: ReferenceMismatch}},
		{"refund already reversed", Refund, "10.00", "0.00",
			func() *Reference { r := processedRef(t, Bet, "10.00"); r.Reversed = true; return r },
			Decision{Status: Rejected, FailureCode: ReversalAlreadyApplied}},
		{"rollback of bet credits", Rollback, "10.00", "0.00",
			func() *Reference { return processedRef(t, Bet, "10.00") }, Decision{Status: Processed, Effect: CreditEffect}},
		{"rollback of win debits", Rollback, "10.00", "50.00",
			func() *Reference { return processedRef(t, Win, "10.00") }, Decision{Status: Processed, Effect: DebitEffect}},
		{"rollback of refund debits", Rollback, "10.00", "10.00",
			func() *Reference { return processedRef(t, Refund, "10.00") }, Decision{Status: Processed, Effect: DebitEffect}},
		{"rollback of win without funds", Rollback, "10.00", "9.99",
			func() *Reference { return processedRef(t, Win, "10.00") },
			Decision{Status: Rejected, FailureCode: ReversalInsufficientFunds}},
		{"rollback of loss is a mismatch", Rollback, "10.00", "10.00",
			func() *Reference { return processedRef(t, Loss, "0.00") }, Decision{Status: Rejected, FailureCode: ReferenceMismatch}},
		{"rollback of rollback is a mismatch", Rollback, "10.00", "10.00",
			func() *Reference { return processedRef(t, Rollback, "10.00") }, Decision{Status: Rejected, FailureCode: ReferenceMismatch}},
		{"rollback already reversed", Rollback, "10.00", "10.00",
			func() *Reference { r := processedRef(t, Bet, "10.00"); r.Reversed = true; return r },
			Decision{Status: Rejected, FailureCode: ReversalAlreadyApplied}},
		{"win referencing a bet credits", Win, "30.00", "0.00",
			func() *Reference { return processedRef(t, Bet, "10.00") }, Decision{Status: Processed, Effect: CreditEffect}},
		{"win referencing a win is a mismatch", Win, "30.00", "0.00",
			func() *Reference { return processedRef(t, Win, "10.00") }, Decision{Status: Rejected, FailureCode: ReferenceMismatch}},
		{"win ignores reversal of its bet", Win, "30.00", "0.00",
			func() *Reference { r := processedRef(t, Bet, "10.00"); r.Reversed = true; return r },
			Decision{Status: Processed, Effect: CreditEffect}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := DecideWithReference(withReference(t, c.kind, c.amount), brl(t, c.available), c.ref())
			if err != nil || got != c.want {
				t.Fatalf("DecideWithReference = %+v, %v; want %+v", got, err, c.want)
			}
		})
	}
}

func TestDecideWithReferenceChecksContext(t *testing.T) {
	mutations := map[string]func(r *Reference){
		"provider": func(r *Reference) { r.ProviderID = "provider-b" },
		"player":   func(r *Reference) { r.PlayerID = "p2" },
		"wallet":   func(r *Reference) { r.WalletID = "w2" },
		"round":    func(r *Reference) { r.RoundID = "r2" },
		"currency": func(r *Reference) { r.Amount, _ = money.Parse("10.00", "USD") },
	}
	for name, mutate := range mutations {
		ref := processedRef(t, Bet, "10.00")
		mutate(ref)
		got, err := DecideWithReference(withReference(t, Refund, "10.00"), brl(t, "0.00"), ref)
		if err != nil || got.Status != Rejected || got.FailureCode != ReferenceMismatch {
			t.Errorf("%s mismatch: %+v, %v", name, got, err)
		}
	}
}

func TestDecideWithReferenceRequiresReferenceAndOpenTransaction(t *testing.T) {
	plain, _ := NewExternal(externalParams(t, Win, "10.00"))
	if _, err := DecideWithReference(plain, brl(t, "0.00"), nil); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("no reference error = %v", err)
	}
	tx := withReference(t, Refund, "10.00")
	_ = tx.Process(brl(t, "10.00"), 2, t0)
	if _, err := DecideWithReference(tx, brl(t, "0.00"), processedRef(t, Bet, "10.00")); !errors.Is(err, ErrTerminal) {
		t.Fatalf("terminal error = %v", err)
	}
}

func TestReferenceOf(t *testing.T) {
	bet, _ := NewExternal(externalParams(t, Bet, "10.00"))
	_ = bet.Process(brl(t, "90.00"), 2, t0)
	ref := ReferenceOf(bet, true)
	want := Reference{
		TransactionID: bet.ID(), Kind: Bet, Status: Processed, ProviderID: "provider-a",
		PlayerID: "p1", WalletID: "w1", RoundID: "r1", Amount: brl(t, "10.00"), Reversed: true,
	}
	if ref != want {
		t.Fatalf("ReferenceOf = %+v, want %+v", ref, want)
	}
}
