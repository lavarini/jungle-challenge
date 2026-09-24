package wagering

import (
	"errors"
	"testing"
)

func TestDecide(t *testing.T) {
	cases := []struct {
		name      string
		kind      Kind
		amount    string
		available string
		want      Decision
	}{
		{"bet with funds", Bet, "80.00", "100.00", Decision{Status: Processed, Effect: DebitEffect}},
		{"bet exact balance", Bet, "100.00", "100.00", Decision{Status: Processed, Effect: DebitEffect}},
		{"bet without funds", Bet, "80.00", "20.00", Decision{Status: Rejected, FailureCode: BetInsufficientFunds}},
		{"win credits", Win, "50.00", "0.00", Decision{Status: Processed, Effect: CreditEffect}},
		{"loss has no effect", Loss, "0.00", "0.00", Decision{Status: Processed, Effect: NoEffect}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tx, err := NewExternal(externalParams(t, c.kind, c.amount))
			if err != nil {
				t.Fatal(err)
			}
			got, err := Decide(tx, brl(t, c.available))
			if err != nil || got != c.want {
				t.Fatalf("Decide = %+v, %v; want %+v", got, err, c.want)
			}
		})
	}
}

func TestDecideDefersReferences(t *testing.T) {
	p := externalParams(t, Win, "10.00")
	p.Provider.ReferenceExternalID = "bet-1"
	tx, _ := NewExternal(p)
	if _, err := Decide(tx, brl(t, "0.00")); !errors.Is(err, ErrNeedsReference) {
		t.Fatalf("error = %v", err)
	}
}

func TestDecideRejectsTerminal(t *testing.T) {
	tx, _ := NewExternal(externalParams(t, Bet, "10.00"))
	_ = tx.Process(brl(t, "0.00"), 2, t0)
	if _, err := Decide(tx, brl(t, "10.00")); !errors.Is(err, ErrTerminal) {
		t.Fatalf("error = %v", err)
	}
}
