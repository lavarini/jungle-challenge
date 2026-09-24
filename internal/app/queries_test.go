package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/lavarini/backend-challenge-go/internal/money"
)

type fakeReconciler struct{ totals ReconciliationTotals }

func (f fakeReconciler) Totals(context.Context, string) (ReconciliationTotals, error) {
	return f.totals, nil
}

func TestReconcileReportsDifferenceAsStoredMinusCalculated(t *testing.T) {
	stored, _ := money.Parse("100.00", "BRL")
	r := NewReconcile(fakeReconciler{ReconciliationTotals{Stored: stored, CreditsMinusDebits: 9000, Entries: 3}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	got, err := r.Execute(context.Background(), "w1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Consistent || got.Difference.String() != "10.00" || got.Calculated.String() != "90.00" || got.CheckedEntries != 3 {
		t.Fatalf("reconciliation %+v", got)
	}
	consistent := NewReconcile(fakeReconciler{ReconciliationTotals{Stored: stored, CreditsMinusDebits: 10000, Entries: 2}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	got, _ = consistent.Execute(context.Background(), "w1")
	if !got.Consistent || !got.Difference.IsZero() {
		t.Fatalf("consistent reconciliation %+v", got)
	}
}

func TestLedgerCursorRoundTripAndValidation(t *testing.T) {
	c := encodeCursor(42)
	if seq, err := decodeCursor(c); err != nil || seq != 42 {
		t.Fatalf("decode(%q) = %d, %v", c, seq, err)
	}
	if seq, err := decodeCursor(""); err != nil || seq != 0 {
		t.Fatalf("empty cursor = %d, %v", seq, err)
	}
	for _, bad := range []string{"42", "!!!", encodeRaw("v2:1"), encodeRaw("v1:-1"), encodeRaw("v1:x")} {
		if _, err := decodeCursor(bad); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("decodeCursor(%q) error = %v", bad, err)
		}
	}
}
