package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/lavarini/backend-challenge-go/internal/money"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

var discardLog = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeReconciler struct{ totals ReconciliationTotals }

func (f fakeReconciler) Totals(context.Context, string) (ReconciliationTotals, error) {
	return f.totals, nil
}

func TestReconcileReportsDifferenceAsStoredMinusCalculated(t *testing.T) {
	stored, _ := money.Parse("100.00", "BRL")
	r := NewReconcile(fakeReconciler{ReconciliationTotals{Stored: stored, CreditsMinusDebits: 9000, Entries: 3}}, discardLog)
	got, err := r.Execute(context.Background(), "w1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Consistent || got.Difference.String() != "10.00" || got.Calculated.String() != "90.00" || got.CheckedEntries != 3 {
		t.Fatalf("reconciliation %+v", got)
	}
	consistent := NewReconcile(fakeReconciler{ReconciliationTotals{Stored: stored, CreditsMinusDebits: 10000, Entries: 2}}, discardLog)
	got, _ = consistent.Execute(context.Background(), "w1")
	if !got.Consistent || !got.Difference.IsZero() {
		t.Fatalf("consistent reconciliation %+v", got)
	}
}

func TestReconcileDetectsVersionChainAndCurrencyDivergence(t *testing.T) {
	stored, _ := money.Parse("100.00", "BRL")
	lastAfter, _ := money.Parse("100.00", "BRL")
	base := ReconciliationTotals{
		Stored: stored, CreditsMinusDebits: 10000, Entries: 2,
		WalletVersion: 2, MaxLedgerVersion: 2, LastBalanceAfter: lastAfter,
	}
	if got, err := NewReconcile(fakeReconciler{base}, discardLog).Execute(context.Background(), "w1"); err != nil || !got.Consistent {
		t.Fatalf("baseline should be consistent: %+v %v", got, err)
	}

	versionMismatch := base
	versionMismatch.MaxLedgerVersion = 1
	if got, err := NewReconcile(fakeReconciler{versionMismatch}, discardLog).Execute(context.Background(), "w1"); err != nil || got.Consistent || !got.VersionMismatch {
		t.Fatalf("version mismatch not detected: %+v %v", got, err)
	}

	chainMismatch := base
	chainMismatch.LastBalanceAfter, _ = money.Parse("90.00", "BRL")
	if got, err := NewReconcile(fakeReconciler{chainMismatch}, discardLog).Execute(context.Background(), "w1"); err != nil || got.Consistent || !got.ChainMismatch {
		t.Fatalf("chain mismatch not detected: %+v %v", got, err)
	}

	currencyMismatch := base
	currencyMismatch.CurrencyMismatches = 1
	if got, err := NewReconcile(fakeReconciler{currencyMismatch}, discardLog).Execute(context.Background(), "w1"); err != nil || got.Consistent || got.CurrencyMismatches != 1 {
		t.Fatalf("currency mismatch not detected: %+v %v", got, err)
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

// fakeUow and fakeTx let GetTransaction.ByID be unit-tested without a
// database: only Transactions() is exercised.
type fakeUow struct{ tx fakeTx }

func (f fakeUow) Do(ctx context.Context, fn func(context.Context, Tx) error) error {
	return fn(ctx, f.tx)
}

type fakeTx struct{ transactions TransactionRepository }

func (f fakeTx) Wallets() WalletRepository           { return nil }
func (f fakeTx) Transactions() TransactionRepository { return f.transactions }
func (f fakeTx) Ledger() LedgerRepository            { return nil }
func (f fakeTx) Outbox() OutboxRepository            { return nil }
func (f fakeTx) Inbox() InboxRepository              { return nil }

type fakeTransactionRepo struct{ tx *wagering.Transaction }

func (f fakeTransactionRepo) Insert(context.Context, *wagering.Transaction) error { return nil }
func (f fakeTransactionRepo) Update(context.Context, *wagering.Transaction) error { return nil }
func (f fakeTransactionRepo) Get(context.Context, string) (*wagering.Transaction, error) {
	return f.tx, nil
}
func (f fakeTransactionRepo) GetForUpdate(context.Context, string) (*wagering.Transaction, error) {
	return f.tx, nil
}
func (f fakeTransactionRepo) FindByIdempotencyKey(context.Context, string, string) (*wagering.Transaction, error) {
	return nil, nil
}
func (f fakeTransactionRepo) FindByExternalID(context.Context, string, string) (*wagering.Transaction, error) {
	return nil, nil
}
func (f fakeTransactionRepo) HasProcessedReversal(context.Context, string) (bool, error) {
	return false, nil
}
func (f fakeTransactionRepo) WakePending(context.Context, string, string, time.Time) error {
	return nil
}
func (f fakeTransactionRepo) ClaimDuePending(context.Context, time.Time, time.Time, int) ([]string, error) {
	return nil, nil
}

func TestGetTransactionByIDScopesToProvider(t *testing.T) {
	amount, err := money.Parse("10.00", "BRL")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := wagering.NewExternal(wagering.NewExternalParams{
		ID: "t1", WalletID: "w1", PlayerID: "p1", CorrelationID: "c1", Kind: wagering.Bet, Amount: amount,
		Provider: wagering.ProviderRef{
			ProviderID: "provider-a", ExternalID: "e1", IdempotencyKey: "provider-a:e1",
			RoundID: "r1", GameID: "g1", PayloadHash: make([]byte, sha256.Size),
		},
		Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	g := NewGetTransaction(fakeUow{tx: fakeTx{transactions: fakeTransactionRepo{tx: tx}}})

	if got, err := g.ByID(context.Background(), "t1", "provider-a"); err != nil || got.ID() != "t1" {
		t.Fatalf("own provider read: %v %v", got, err)
	}
	if _, err := g.ByID(context.Background(), "t1", "provider-b"); !errors.Is(err, ErrTransactionNotFound) {
		t.Fatalf("another provider's transaction must read as not found, got %v", err)
	}
	if got, err := g.ByID(context.Background(), "t1", ""); err != nil || got.ID() != "t1" {
		t.Fatalf("internal (unscoped) read: %v %v", got, err)
	}
}
