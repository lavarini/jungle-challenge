package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
	"github.com/lavarini/backend-challenge-go/internal/wallet"
)

const txID = "0192f298-345e-7e38-af88-e43f851a819d"

type fakeTransactions struct {
	scope string
	tx    *wagering.Transaction
}

func (f *fakeTransactions) ByID(_ context.Context, id, scope string) (*wagering.Transaction, error) {
	f.scope = scope
	if f.tx != nil {
		return f.tx, nil
	}
	return nil, app.ErrTransactionNotFound
}

func (f *fakeTransactions) ByExternalID(context.Context, string, string) (*wagering.Transaction, error) {
	if f.tx != nil {
		return f.tx, nil
	}
	return nil, app.ErrTransactionNotFound
}

// fakeLedger stands in for app.ListLedger: it validates limit and cursor the
// same way the real use case does (bounds via the exported constants, cursor
// as opaque base64), so the HTTP layer's error mapping can be tested without
// a database.
type fakeLedger struct {
	limit int
	page  app.LedgerPage
}

func (f *fakeLedger) Execute(_ context.Context, _, cursor string, limit int) (app.LedgerPage, error) {
	f.limit = limit
	if limit < 1 || limit > app.MaxLedgerLimit {
		return app.LedgerPage{}, app.ErrInvalidInput
	}
	if cursor != "" {
		if _, err := base64.RawURLEncoding.DecodeString(cursor); err != nil {
			return app.LedgerPage{}, app.ErrInvalidInput
		}
	}
	return f.page, nil
}

type fakeReconcile struct{}

func (fakeReconcile) Execute(_ context.Context, walletID string) (app.Reconciliation, error) {
	return app.Reconciliation{}, app.ErrWalletNotFound
}

func queryHandler(tx *fakeTransactions, ledger *fakeLedger) http.Handler {
	return NewHandler(Deps{
		Verifier: fakeVerifier{
			"provider-a": {ProviderID: "provider-a", Roles: []app.Role{app.RoleProvider}},
			"internal":   {Roles: []app.Role{app.RoleInternal}},
		},
		Transactions: tx, Ledger: ledger, Reconcile: fakeReconcile{},
		Readiness: fakeReady{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func get(h http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestTransactionReadIsScopedToTheProvider(t *testing.T) {
	tx := &fakeTransactions{}
	h := queryHandler(tx, &fakeLedger{})
	if rec := get(h, "/wagering/transactions/"+txID, "provider-a"); rec.Code != http.StatusNotFound || tx.scope != "provider-a" {
		t.Fatalf("provider read: %d scope %q", rec.Code, tx.scope)
	}
	if get(h, "/wagering/transactions/"+txID, "internal"); tx.scope != "" {
		t.Fatalf("internal read must be unscoped, got %q", tx.scope)
	}
	if rec := get(h, "/wagering/transactions/not-a-uuid", "provider-a"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", rec.Code)
	}
}

func TestExternalReadRefusesAnotherProvidersPath(t *testing.T) {
	h := queryHandler(&fakeTransactions{}, &fakeLedger{})
	rec := get(h, "/providers/provider-b/wagering/transactions/tx-1", "provider-a")
	if rec.Code != http.StatusForbidden || problemCode(t, rec) != "PROVIDER_MISMATCH" {
		t.Fatalf("other provider path: %d %s", rec.Code, rec.Body)
	}
	if rec := get(h, "/providers/provider-b/wagering/transactions/tx-1", "internal"); rec.Code != http.StatusNotFound {
		t.Fatalf("internal read: %d", rec.Code)
	}
}

func TestExternalReadRejectsOversizeExternalID(t *testing.T) {
	h := queryHandler(&fakeTransactions{}, &fakeLedger{})
	oversized := strings.Repeat("a", 256)
	if rec := get(h, "/providers/provider-a/wagering/transactions/"+oversized, "provider-a"); rec.Code != http.StatusBadRequest {
		t.Fatalf("oversize external id: %d", rec.Code)
	}
	fits := strings.Repeat("a", 255)
	if rec := get(h, "/providers/provider-a/wagering/transactions/"+fits, "provider-a"); rec.Code != http.StatusNotFound {
		t.Fatalf("external id at the limit: %d", rec.Code)
	}
}

func TestLedgerAndReconciliationAreInternal(t *testing.T) {
	ledger := &fakeLedger{}
	h := queryHandler(&fakeTransactions{}, ledger)
	if rec := get(h, "/wallets/"+walletID+"/ledger", "provider-a"); rec.Code != http.StatusForbidden {
		t.Fatalf("provider ledger: %d", rec.Code)
	}
	if rec := get(h, "/wallets/"+walletID+"/ledger?limit=7", "internal"); rec.Code != http.StatusOK || ledger.limit != 7 {
		t.Fatalf("ledger: %d limit %d", rec.Code, ledger.limit)
	}
	if rec := get(h, "/wallets/"+walletID+"/ledger?limit=abc", "internal"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad limit: %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/wallets/"+walletID+"/reconciliation", bytes.NewReader(nil))
	req.Header.Set("Authorization", "Bearer internal")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("reconciliation of missing wallet: %d", rec.Code)
	}
}

func TestReconciliationRequiresInternalRole(t *testing.T) {
	h := queryHandler(&fakeTransactions{}, &fakeLedger{})
	req := httptest.NewRequest(http.MethodPost, "/wallets/"+walletID+"/reconciliation", bytes.NewReader(nil))
	req.Header.Set("Authorization", "Bearer provider-a")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("provider reconciliation: %d", rec.Code)
	}
}

func TestListLedgerValidatesLimitAndCursor(t *testing.T) {
	ledger := &fakeLedger{}
	h := queryHandler(&fakeTransactions{}, ledger)
	for _, raw := range []string{"101", "-1", "0"} {
		if rec := get(h, "/wallets/"+walletID+"/ledger?limit="+raw, "internal"); rec.Code != http.StatusBadRequest {
			t.Errorf("limit=%s: %d", raw, rec.Code)
		}
	}
	if rec := get(h, "/wallets/"+walletID+"/ledger?cursor=%21%21%21", "internal"); rec.Code != http.StatusBadRequest {
		t.Fatalf("garbage cursor: %d", rec.Code)
	}
	if rec := get(h, "/wallets/"+walletID+"/ledger", "internal"); rec.Code != http.StatusOK || ledger.limit != app.DefaultLedgerLimit {
		t.Fatalf("absent limit must default: %d limit %d", rec.Code, ledger.limit)
	}
}

func sampleTransaction(t *testing.T) *wagering.Transaction {
	t.Helper()
	tx, err := wagering.NewExternal(wagering.NewExternalParams{
		ID: txID, WalletID: walletID, PlayerID: playerID, CorrelationID: "corr-1",
		Kind: wagering.Bet, Amount: brl(t, "10.00"),
		Provider: wagering.ProviderRef{
			ProviderID: "provider-a", ExternalID: "ext-1", IdempotencyKey: "provider-a:ext-1",
			RoundID: "r1", GameID: "g1", PayloadHash: make([]byte, sha256.Size),
		},
		Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Process(brl(t, "990.00"), 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	return tx
}

func sampleLedgerEntry(t *testing.T) wallet.LedgerEntry {
	t.Helper()
	e, err := wallet.NewLedgerEntry(wallet.LedgerEntryParams{
		ID: "0192f299-0000-7000-8000-000000000001", WalletID: walletID, TransactionID: txID,
		Direction: wallet.Debit, Amount: brl(t, "10.00"),
		BalanceBefore: brl(t, "1000.00"), BalanceAfter: brl(t, "990.00"),
		WalletVersion: 2, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestTransactionAndLedgerHappyPathBodyShape(t *testing.T) {
	tx := &fakeTransactions{tx: sampleTransaction(t)}
	ledger := &fakeLedger{page: app.LedgerPage{Rows: []app.LedgerRow{{Seq: 1, Entry: sampleLedgerEntry(t)}}, NextCursor: "cursor-1"}}
	h := queryHandler(tx, ledger)

	rec := get(h, "/wagering/transactions/"+txID, "provider-a")
	if rec.Code != http.StatusOK {
		t.Fatalf("transaction status %d %s", rec.Code, rec.Body)
	}
	var txBody map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &txBody); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"transactionId", "origin", "kind", "status", "walletId", "playerId", "money", "createdAt", "balance", "walletVersion", "completedAt"} {
		if _, ok := txBody[field]; !ok {
			t.Errorf("transaction body missing %q: %s", field, rec.Body)
		}
	}
	if txBody["transactionId"] != txID || txBody["status"] != "PROCESSED" {
		t.Fatalf("transaction body %s", rec.Body)
	}

	rec = get(h, "/wallets/"+walletID+"/ledger", "internal")
	if rec.Code != http.StatusOK {
		t.Fatalf("ledger status %d %s", rec.Code, rec.Body)
	}
	var ledgerBody struct {
		Entries []struct {
			ID            string `json:"id"`
			TransactionID string `json:"transactionId"`
			Direction     string `json:"direction"`
			WalletVersion int64  `json:"walletVersion"`
		} `json:"entries"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ledgerBody); err != nil {
		t.Fatal(err)
	}
	if len(ledgerBody.Entries) != 1 || ledgerBody.Entries[0].Direction != "DEBIT" ||
		ledgerBody.Entries[0].TransactionID != txID || ledgerBody.Entries[0].WalletVersion != 2 || ledgerBody.NextCursor != "cursor-1" {
		t.Fatalf("ledger body %s", rec.Body)
	}
}
