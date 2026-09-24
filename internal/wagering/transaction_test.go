package wagering

import (
	"bytes"
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

func externalParams(t *testing.T, kind Kind, amount string) NewExternalParams {
	return NewExternalParams{
		ID: "t1", WalletID: "w1", PlayerID: "p1", CorrelationID: "c1", Kind: kind, Amount: brl(t, amount), Now: t0,
		Provider: ProviderRef{
			ProviderID: "provider-a", ExternalID: "tx-1", IdempotencyKey: "provider-a:tx-1",
			PayloadHash: bytes.Repeat([]byte{1}, 32), RoundID: "r1", GameID: "g1",
		},
	}
}

func TestNewExternalStartsPending(t *testing.T) {
	tx, err := NewExternal(externalParams(t, Bet, "25.00"))
	if err != nil {
		t.Fatal(err)
	}
	if tx.Status() != Pending || tx.Origin() != OriginExternal || tx.Kind() != Bet {
		t.Fatalf("unexpected %+v", tx.Snapshot())
	}
	if _, _, ok := tx.Result(); ok {
		t.Fatal("pending transaction must not have a result")
	}
}

func TestNewExternalZeroPolicy(t *testing.T) {
	cases := []struct {
		kind   Kind
		amount string
		ok     bool
	}{
		{Loss, "0.00", true},
		{Loss, "1.00", false},
		{Bet, "0.00", false},
		{Win, "0.00", false},
		{Bet, "0.01", true},
		{Win, "10.00", true},
	}
	for _, c := range cases {
		_, err := NewExternal(externalParams(t, c.kind, c.amount))
		if c.ok && err != nil {
			t.Errorf("%s %s: unexpected error %v", c.kind, c.amount, err)
		}
		if !c.ok && !errors.Is(err, ErrInvalidAmountForKind) {
			t.Errorf("%s %s: error = %v, want ErrInvalidAmountForKind", c.kind, c.amount, err)
		}
	}
}

func TestNewExternalValidation(t *testing.T) {
	cases := map[string]struct {
		mutate func(p *NewExternalParams)
		want   error
	}{
		"opening":            {func(p *NewExternalParams) { p.Kind = Opening }, ErrOpeningNotExternal},
		"unknown kind":       {func(p *NewExternalParams) { p.Kind = "DEPOSIT" }, ErrUnknownKind},
		"missing provider":   {func(p *NewExternalParams) { p.Provider.ProviderID = "" }, ErrInvalidTransaction},
		"missing round":      {func(p *NewExternalParams) { p.Provider.RoundID = "" }, ErrInvalidTransaction},
		"short hash":         {func(p *NewExternalParams) { p.Provider.PayloadHash = []byte{1} }, ErrInvalidTransaction},
		"missing wallet":     {func(p *NewExternalParams) { p.WalletID = "" }, ErrInvalidTransaction},
		"refund without ref": {func(p *NewExternalParams) { p.Kind = Refund }, ErrReferenceRequired},
		"rollback no ref":    {func(p *NewExternalParams) { p.Kind = Rollback }, ErrReferenceRequired},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := externalParams(t, Bet, "10.00")
			c.mutate(&p)
			if _, err := NewExternal(p); !errors.Is(err, c.want) {
				t.Fatalf("error = %v, want %v", err, c.want)
			}
		})
	}
}

func TestProcessAndRejectAreTerminal(t *testing.T) {
	tx, _ := NewExternal(externalParams(t, Bet, "10.00"))
	if err := tx.Process(brl(t, "90.00"), 2, t0); err != nil {
		t.Fatal(err)
	}
	balance, version, ok := tx.Result()
	if !ok || balance.String() != "90.00" || version != 2 || tx.Status() != Processed || tx.CompletedAt().IsZero() {
		t.Fatalf("after process: %+v", tx.Snapshot())
	}
	if err := tx.Process(brl(t, "80.00"), 3, t0); !errors.Is(err, ErrTerminal) {
		t.Fatalf("second process error = %v", err)
	}
	if err := tx.Reject(BetInsufficientFunds, brl(t, "80.00"), 3, t0); !errors.Is(err, ErrTerminal) {
		t.Fatalf("reject after process error = %v", err)
	}
}

func TestRejectRequiresCode(t *testing.T) {
	tx, _ := NewExternal(externalParams(t, Bet, "10.00"))
	if err := tx.Reject("", brl(t, "5.00"), 1, t0); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("error = %v", err)
	}
	if err := tx.Reject(BetInsufficientFunds, brl(t, "5.00"), 1, t0); err != nil {
		t.Fatal(err)
	}
	if tx.Status() != Rejected || tx.FailureCode() != BetInsufficientFunds {
		t.Fatalf("after reject: %+v", tx.Snapshot())
	}
}

func TestResultCurrencyMustMatch(t *testing.T) {
	tx, _ := NewExternal(externalParams(t, Bet, "10.00"))
	usd, _ := money.Parse("1.00", "USD")
	if err := tx.Process(usd, 2, t0); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("error = %v", err)
	}
}

func TestPendingReferenceTransitions(t *testing.T) {
	p := externalParams(t, Refund, "10.00")
	p.Provider.ReferenceExternalID = "bet-1"
	tx, err := NewExternal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Fail(InvariantViolation, t0); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("fail from pending error = %v", err)
	}
	if err := tx.AwaitReference(t0.Add(time.Second), t0.Add(24*time.Hour), t0); err != nil {
		t.Fatal(err)
	}
	if tx.Status() != PendingReference {
		t.Fatalf("status %s", tx.Status())
	}
	if err := tx.AwaitReference(t0, t0, t0); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("await twice error = %v", err)
	}
	if err := tx.Fail(InvariantViolation, t0); err != nil {
		t.Fatal(err)
	}
	if !tx.Status().IsTerminal() {
		t.Fatal("FAILED must be terminal")
	}
}

func TestAwaitReferenceRequiresReference(t *testing.T) {
	tx, _ := NewExternal(externalParams(t, Bet, "10.00"))
	if err := tx.AwaitReference(t0, t0, t0); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("error = %v", err)
	}
}

func TestOpening(t *testing.T) {
	tx, err := NewOpening("t0", "w1", "p1", "c1", brl(t, "100.00"), t0)
	if err != nil {
		t.Fatal(err)
	}
	if tx.Origin() != OriginInternal || tx.Kind() != Opening || tx.Provider().ProviderID != "" {
		t.Fatalf("unexpected %+v", tx.Snapshot())
	}
	if _, err := NewOpening("t0", "w1", "p1", "c1", brl(t, "0.00"), t0); !errors.Is(err, ErrInvalidAmountForKind) {
		t.Fatalf("zero opening error = %v", err)
	}
}

func TestResolveReferenceAndReschedule(t *testing.T) {
	tx := withReference(t, Refund, "10.00")
	if err := tx.ResolveReference(""); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("empty reference error = %v", err)
	}
	if err := tx.Reschedule(t0, t0); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("reschedule before waiting error = %v", err)
	}
	if err := tx.AwaitReference(t0.Add(time.Second), t0.Add(time.Hour), t0); err != nil {
		t.Fatal(err)
	}
	next := t0.Add(4 * time.Second)
	if err := tx.Reschedule(next, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if tx.Attempts() != 1 || !tx.NextAttemptAt().Equal(next) || !tx.DeadlineAt().Equal(t0.Add(time.Hour)) {
		t.Fatalf("schedule: attempts %d next %v deadline %v", tx.Attempts(), tx.NextAttemptAt(), tx.DeadlineAt())
	}
	if err := tx.ResolveReference("bet-internal-id"); err != nil {
		t.Fatal(err)
	}
	if tx.ReferenceTxID() != "bet-internal-id" {
		t.Fatalf("reference %q", tx.ReferenceTxID())
	}
	if err := tx.Process(brl(t, "10.00"), 2, t0); err != nil {
		t.Fatal(err)
	}
	if err := tx.ResolveReference("other"); !errors.Is(err, ErrTerminal) {
		t.Fatalf("resolve after terminal error = %v", err)
	}
	if err := tx.Reschedule(next, t0); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("reschedule after terminal error = %v", err)
	}
}

func TestRehydrateRoundTrip(t *testing.T) {
	tx, _ := NewExternal(externalParams(t, Bet, "10.00"))
	_ = tx.Process(brl(t, "90.00"), 2, t0)
	back, err := Rehydrate(tx.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if back.Status() != Processed || back.ID() != tx.ID() {
		t.Fatalf("rehydrated %+v", back.Snapshot())
	}
	pending := tx.Snapshot()
	pending.Status = Pending
	if _, err := Rehydrate(pending); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("PENDING must never be rehydrated, error = %v", err)
	}
	noCode := tx.Snapshot()
	noCode.Status = Rejected
	if _, err := Rehydrate(noCode); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("REJECTED without code error = %v", err)
	}
}
