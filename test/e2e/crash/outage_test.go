//go:build e2e

package crash

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"testing"
	"time"

	"github.com/lavarini/backend-challenge-go/test/testenv"
)

func docker(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker %v: %v %s", args, err, out)
	}
}

// With PostgreSQL unreachable, a bet must fail fast with a retryable 503 and
// no effect; the provider's retry with the same key after recovery must
// apply it exactly once.
func TestDatabaseOutageReturnsRetryable503WithoutDuplicateEffect(t *testing.T) {
	api := spawn(t, "outage-api", "api")
	api.waitReady(t)
	provider := token(t, testenv.ProviderAID, testenv.ProviderASecret)
	w := openWallet(t, api, "100.00")
	ext := fmt.Sprintf("outage-%d", time.Now().UnixNano())
	body := operation(w, "BET", "10.00", ext, "")
	key := "provider-a:" + ext

	id := env.PostgresContainerID()
	docker(t, "pause", id)
	paused := true
	t.Cleanup(func() {
		if paused {
			_ = exec.Command("docker", "unpause", id).Run()
		}
	})

	// A transport error or client timeout here is a failure of the contract,
	// not of the harness: post would t.Fatal without the status and latency.
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "http://"+api.addr+"/wagering/transactions", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+provider)
	req.Header.Set("Idempotency-Key", key)
	client := &http.Client{Timeout: 20 * time.Second}
	start := time.Now()
	resp, err := client.Do(req)
	took := time.Since(start)
	if err != nil {
		t.Fatalf("during outage: no response after %s: %v", took, err)
	}
	var prob struct {
		Retryable bool `json:"retryable"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&prob)
	resp.Body.Close()
	t.Logf("during outage: status %d retryable=%v in %s", resp.StatusCode, prob.Retryable, took)
	if resp.StatusCode != http.StatusServiceUnavailable || !prob.Retryable {
		t.Fatalf("during outage: status %d retryable=%v, want 503 retryable", resp.StatusCode, prob.Retryable)
	}
	if took > 15*time.Second {
		t.Fatalf("503 took %s; the provider would time out first", took)
	}

	docker(t, "unpause", id)
	paused = false

	pool := dbPool(t)
	eventually(t, 30*time.Second, func() bool {
		code := post(t, api, "/wagering/transactions", provider, key, body, nil)
		return code == http.StatusCreated || code == http.StatusOK
	})
	if n := count(t, pool, `SELECT count(*) FROM wallet_ledger_entries l JOIN wager_transactions x ON x.id = l.transaction_id
		WHERE x.provider_id = 'provider-a' AND x.external_id = $1`, ext); n != 1 {
		t.Fatalf("ledger entries for %s: %d, want 1", ext, n)
	}
	if b := balance(t, pool, w.ID); b != 9000 {
		t.Fatalf("balance %d minor units, want 9000", b)
	}
}
