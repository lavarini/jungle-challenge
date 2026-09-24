//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/lavarini/backend-challenge-go/internal/adapters/postgres"
	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

func TestTransactionReadsRespectProviderScope(t *testing.T) {
	s := newStack(t)
	q := app.NewGetTransaction(postgres.NewUnitOfWork(s.pool))
	w := openWallet(t, s, "100.00")
	bet := command(t, w, wagering.Bet, "10.00", uuid.NewString())
	res := submit(t, s, bet)

	if tx, err := q.ByID(context.Background(), res.TransactionID, "provider-a"); err != nil || tx.ID() != res.TransactionID {
		t.Fatalf("own read %v %v", tx, err)
	}
	if _, err := q.ByID(context.Background(), res.TransactionID, "provider-b"); !errors.Is(err, app.ErrTransactionNotFound) {
		t.Fatalf("other provider read error = %v", err)
	}
	if tx, err := q.ByExternalID(context.Background(), "provider-a", bet.ExternalTransactionID); err != nil || tx.ID() != res.TransactionID {
		t.Fatalf("external read %v %v", tx, err)
	}
	if _, err := q.ByExternalID(context.Background(), "provider-b", bet.ExternalTransactionID); !errors.Is(err, app.ErrTransactionNotFound) {
		t.Fatalf("external read under another provider error = %v", err)
	}
}

func TestLedgerPaginatesInSeqOrder(t *testing.T) {
	s := newStack(t)
	l := app.NewListLedger(postgres.NewUnitOfWork(s.pool))
	w := openWallet(t, s, "100.00")
	for i := 0; i < 4; i++ {
		submit(t, s, command(t, w, wagering.Bet, "1.00", uuid.NewString()))
	}
	var versions []int64
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		page, err := l.Execute(context.Background(), w.ID, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range page.Rows {
			versions = append(versions, row.Entry.WalletVersion())
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	want := []int64{1, 2, 3, 4, 5}
	if len(versions) != len(want) {
		t.Fatalf("versions %v", versions)
	}
	for i := range want {
		if versions[i] != want[i] {
			t.Fatalf("versions %v, want %v", versions, want)
		}
	}
	if _, err := l.Execute(context.Background(), w.ID, "garbage", 2); !errors.Is(err, app.ErrInvalidInput) {
		t.Fatalf("bad cursor error = %v", err)
	}
	if _, err := l.Execute(context.Background(), uuid.NewString(), "", 2); !errors.Is(err, app.ErrWalletNotFound) {
		t.Fatalf("missing wallet error = %v", err)
	}
}

func TestReconciliationMatchesTheLedger(t *testing.T) {
	s := newStack(t)
	r := app.NewReconcile(postgres.NewReconciler(s.pool), quietLog)
	w := openWallet(t, s, "1000.00")
	submit(t, s, command(t, w, wagering.Bet, "25.00", uuid.NewString()))
	got, err := r.Execute(context.Background(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Consistent || got.Stored.String() != "975.00" || got.Calculated.String() != "975.00" ||
		!got.Difference.IsZero() || got.CheckedEntries != 2 {
		t.Fatalf("reconciliation %+v", got)
	}
}
