//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lavarini/backend-challenge-go/test/testenv"
)

type moneyJSON struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type walletJSON struct {
	ID       string    `json:"id"`
	PlayerID string    `json:"playerId"`
	Balance  moneyJSON `json:"balance"`
	Version  int64     `json:"version"`
}

type submitJSON struct {
	TransactionID    string     `json:"transactionId"`
	Status           string     `json:"status"`
	FailureCode      string     `json:"failureCode"`
	Balance          *moneyJSON `json:"balance"`
	IdempotentReplay bool       `json:"idempotentReplay"`
}

func token(t *testing.T, client, secret string) string {
	t.Helper()
	tok, err := env.Keycloak.Token(context.Background(), client, secret)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// send performs one request without touching testing.T, so goroutines can use it.
func send(p *process, method, path, bearer, idemKey string, body any, out any) (int, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return 0, err
		}
	}
	req, err := http.NewRequest(method, "http://"+p.addr+path, &buf)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

// call is send for the test goroutine: it fails the test on transport errors.
func call(t *testing.T, p *process, method, path, bearer, idemKey string, body any, out any) int {
	t.Helper()
	status, err := send(p, method, path, bearer, idemKey, body, out)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return status
}

func openWallet(t *testing.T, p *process, internal, balance string) walletJSON {
	t.Helper()
	var w walletJSON
	status := call(t, p, http.MethodPost, "/wallets", internal, "", map[string]any{
		"playerId":       uuid.NewString(),
		"initialBalance": moneyJSON{Amount: balance, Currency: "BRL"},
	}, &w)
	if status != http.StatusCreated {
		t.Fatalf("open wallet: %d", status)
	}
	return w
}

func bet(w walletJSON, externalID, amount string) map[string]any {
	return map[string]any{
		"providerId": "provider-a", "externalTransactionId": externalID, "playerId": w.PlayerID,
		"walletId": w.ID, "roundId": "round-1", "gameId": "game-1", "kind": "BET",
		"money": moneyJSON{Amount: amount, Currency: "BRL"},
	}
}

func tokens(t *testing.T) (provider, internal string) {
	return token(t, testenv.ProviderAID, testenv.ProviderASecret), token(t, testenv.InternalID, testenv.InternalSecret)
}

func dbPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), env.Postgres.AppDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func countRows(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// requireReconciled checks stored balance = credits - debits in the ledger.
func requireReconciled(t *testing.T, pool *pgxpool.Pool, walletID string) {
	t.Helper()
	var stored, computed int64
	err := pool.QueryRow(context.Background(), `SELECT w.balance_minor,
		COALESCE(SUM(CASE e.direction WHEN 'CREDIT' THEN e.amount_minor ELSE -e.amount_minor END), 0)::bigint
		FROM wallets w LEFT JOIN wallet_ledger_entries e ON e.wallet_id = w.id
		WHERE w.id = $1 GROUP BY w.balance_minor`, walletID).Scan(&stored, &computed)
	if err != nil {
		t.Fatal(err)
	}
	if stored != computed {
		t.Fatalf("wallet %s: stored %d, ledger %d", walletID, stored, computed)
	}
}
