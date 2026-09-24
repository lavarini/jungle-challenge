//go:build e2e

package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lavarini/backend-challenge-go/test/testenv"
)

func TestAuthorizationRefusalsHaveNoFinancialEffect(t *testing.T) {
	_, internal := tokens(t)
	providerB := token(t, testenv.ProviderBID, testenv.ProviderBSecret)
	providerAShort := token(t, testenv.ProviderAShortID, testenv.ProviderAShortSecret)
	pool := dbPool(t)

	// Setup: a wallet the refusals below could, if buggy, act against.
	w := openWallet(t, procs[0], internal, "100.00")

	counts := func() (tx, ledger, outbox int) {
		return countRows(t, pool, `SELECT count(*) FROM wager_transactions`),
			countRows(t, pool, `SELECT count(*) FROM wallet_ledger_entries`),
			countRows(t, pool, `SELECT count(*) FROM outbox_events`)
	}
	beforeTx, beforeLedger, beforeOutbox := counts()

	// Let the short-lived token actually expire before we use it below. The
	// e2e harness sets OIDC_CLOCK_SKEW=1s, so the server tolerates the 2s
	// lifespan plus 1s of skew; wait past that with a small margin.
	time.Sleep(5 * time.Second)

	// No Authorization header at all: build the request directly, since call/send always sends a bearer.
	req, err := http.NewRequest(http.MethodGet, "http://"+procs[0].addr+"/wallets/"+w.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no auth header: status %d, want 401", resp.StatusCode)
	}

	if status := call(t, procs[0], http.MethodGet, "/wallets/"+w.ID, "garbage", "", nil, nil); status != http.StatusUnauthorized {
		t.Errorf("garbage bearer: status %d, want 401", status)
	}

	// Hit an endpoint the provider role could otherwise use, so a wrongly
	// accepted token would show up as something other than a role failure.
	expiredBody := bet(w, uuid.NewString(), "10.00")
	if status := call(t, procs[0], http.MethodPost, "/wagering/transactions", providerAShort, "provider-a-short:"+uuid.NewString(), expiredBody, nil); status != http.StatusUnauthorized {
		t.Errorf("expired token: status %d, want 401", status)
	}

	// provider-b submits a body claiming to be provider-a.
	var mismatch struct {
		Code string `json:"code"`
	}
	b := bet(w, uuid.NewString(), "10.00")
	if status := call(t, procs[0], http.MethodPost, "/wagering/transactions", providerB, "provider-b:"+uuid.NewString(), b, &mismatch); status != http.StatusForbidden {
		t.Errorf("provider mismatch: status %d, want 403", status)
	} else if mismatch.Code != "PROVIDER_MISMATCH" {
		t.Errorf("provider mismatch: code %q, want PROVIDER_MISMATCH", mismatch.Code)
	}

	// The internal token has no provider role.
	if status := call(t, procs[0], http.MethodPost, "/wagering/transactions", internal, "internal:"+uuid.NewString(), bet(w, uuid.NewString(), "10.00"), nil); status != http.StatusForbidden {
		t.Errorf("internal on submit: status %d, want 403", status)
	}

	// A provider token has no internal role.
	providerA := token(t, testenv.ProviderAID, testenv.ProviderASecret)
	if status := call(t, procs[0], http.MethodPost, "/wallets", providerA, "", map[string]any{
		"playerId":       uuid.NewString(),
		"initialBalance": moneyJSON{Amount: "1.00", Currency: "BRL"},
	}, nil); status != http.StatusForbidden {
		t.Errorf("provider on open wallet: status %d, want 403", status)
	}
	if status := call(t, procs[0], http.MethodGet, "/wallets/"+w.ID, providerA, "", nil, nil); status != http.StatusForbidden {
		t.Errorf("provider on get wallet: status %d, want 403", status)
	}

	afterTx, afterLedger, afterOutbox := counts()
	if afterTx != beforeTx {
		t.Errorf("wager_transactions changed: before %d, after %d", beforeTx, afterTx)
	}
	if afterLedger != beforeLedger {
		t.Errorf("wallet_ledger_entries changed: before %d, after %d", beforeLedger, afterLedger)
	}
	if afterOutbox != beforeOutbox {
		t.Errorf("outbox_events changed: before %d, after %d", beforeOutbox, afterOutbox)
	}
}
