package httpapi

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

const txID = "0192f298-345e-7e38-af88-e43f851a819d"

type fakeTransactions struct{ scope string }

func (f *fakeTransactions) ByID(_ context.Context, id, scope string) (*wagering.Transaction, error) {
	f.scope = scope
	return nil, app.ErrTransactionNotFound
}

func (f *fakeTransactions) ByExternalID(context.Context, string, string) (*wagering.Transaction, error) {
	return nil, app.ErrTransactionNotFound
}

type fakeLedger struct{ limit int }

func (f *fakeLedger) Execute(_ context.Context, _, _ string, limit int) (app.LedgerPage, error) {
	f.limit = limit
	return app.LedgerPage{}, nil
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
