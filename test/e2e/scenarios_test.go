//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// 100.00 and two concurrent 80.00 bets, each sent to a different process.
func TestTwoConcurrentBetsAcrossProcesses(t *testing.T) {
	provider, internal := tokens(t)
	pool := dbPool(t)
	w := openWallet(t, procs[0], internal, "100.00")
	bets := []map[string]any{bet(w, uuid.NewString(), "80.00"), bet(w, uuid.NewString(), "80.00")}

	statuses := make([]int, 2)
	results := make([]submitJSON, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range bets {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "provider-a:" + bets[i]["externalTransactionId"].(string)
			statuses[i], errs[i] = send(procs[i+1], http.MethodPost, "/wagering/transactions", provider, key, bets[i], &results[i])
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	created, rejected := 0, 0
	for i, s := range statuses {
		switch s {
		case http.StatusCreated:
			created++
		case http.StatusUnprocessableEntity:
			rejected++
			if results[i].FailureCode != "BET_INSUFFICIENT_FUNDS" {
				t.Fatalf("failure code %q", results[i].FailureCode)
			}
		default:
			t.Fatalf("unexpected status %d", s)
		}
	}
	if created != 1 || rejected != 1 {
		t.Fatalf("created %d rejected %d", created, rejected)
	}

	// Resends through the third process change nothing.
	for i := range bets {
		var r submitJSON
		key := "provider-a:" + bets[i]["externalTransactionId"].(string)
		call(t, procs[2], http.MethodPost, "/wagering/transactions", provider, key, bets[i], &r)
		if !r.IdempotentReplay {
			t.Fatalf("resend %d not a replay: %+v", i, r)
		}
	}
	var final walletJSON
	call(t, procs[2], http.MethodGet, "/wallets/"+w.ID, internal, "", nil, &final)
	if final.Balance.Amount != "20.00" {
		t.Fatalf("final balance %s", final.Balance.Amount)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'`, w.ID); n != 1 {
		t.Fatalf("debits = %d", n)
	}
	requireReconciled(t, pool, w.ID)
}

// The same bet 50 times in parallel, spread over the three processes.
func TestFiftyIdenticalBetsAcrossProcesses(t *testing.T) {
	provider, internal := tokens(t)
	pool := dbPool(t)
	w := openWallet(t, procs[0], internal, "1000.00")
	b := bet(w, uuid.NewString(), "10.00")
	key := "provider-a:" + b["externalTransactionId"].(string)

	statuses := make([]int, 50)
	results := make([]submitJSON, 50)
	errs := make([]error, 50)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i], errs[i] = send(procs[i%3], http.MethodPost, "/wagering/transactions", provider, key, b, &results[i])
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	created := 0
	for i := range statuses {
		switch statuses[i] {
		case http.StatusCreated:
			created++
		case http.StatusOK:
		default:
			t.Fatalf("attempt %d: status %d", i, statuses[i])
		}
		if results[i].TransactionID != results[0].TransactionID || results[i].Balance == nil || results[i].Balance.Amount != "990.00" {
			t.Fatalf("attempt %d diverged: %+v", i, results[i])
		}
	}
	if created != 1 {
		t.Fatalf("created = %d, want 1", created)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'`, w.ID); n != 1 {
		t.Fatalf("debits = %d", n)
	}
	requireReconciled(t, pool, w.ID)
}

// Independent wallets progress in parallel across processes.
func TestDistinctWalletsAcrossProcesses(t *testing.T) {
	provider, internal := tokens(t)
	pool := dbPool(t)
	wallets := make([]walletJSON, 10)
	for i := range wallets {
		wallets[i] = openWallet(t, procs[i%3], internal, "100.00")
	}

	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i, w := range wallets {
		for j := 0; j < 5; j++ {
			wg.Add(1)
			go func(i, j int, w walletJSON) {
				defer wg.Done()
				b := bet(w, uuid.NewString(), "10.00")
				key := "provider-a:" + b["externalTransactionId"].(string)
				s, err := send(procs[(i+j)%3], http.MethodPost, "/wagering/transactions", provider, key, b, nil)
				if err != nil || s != http.StatusCreated {
					errs <- fmt.Errorf("wallet %d bet %d: status %d, error %v", i, j, s, err)
				}
			}(i, j, w)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	for _, w := range wallets {
		var final walletJSON
		call(t, procs[0], http.MethodGet, "/wallets/"+w.ID, internal, "", nil, &final)
		if final.Balance.Amount != "50.00" || final.Version != 6 {
			t.Errorf("wallet %s: balance %s version %d", w.ID, final.Balance.Amount, final.Version)
		}
		requireReconciled(t, pool, w.ID)
	}
}
