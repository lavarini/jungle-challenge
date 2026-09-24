//go:build e2e

package crash

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lavarini/backend-challenge-go/test/testenv"
)

func token(t *testing.T, client, secret string) string {
	t.Helper()
	tok, err := env.Keycloak.Token(context.Background(), client, secret)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func post(t *testing.T, p *proc, path, bearer, key string, body any, out any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "http://"+p.addr+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+bearer)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

type wallet struct {
	ID       string `json:"id"`
	PlayerID string `json:"playerId"`
}

func openWallet(t *testing.T, api *proc, balance string) wallet {
	t.Helper()
	var w wallet
	if s := post(t, api, "/wallets", token(t, testenv.InternalID, testenv.InternalSecret), "",
		map[string]any{"playerId": uuid.NewString(), "initialBalance": map[string]string{"amount": balance, "currency": "BRL"}}, &w); s != http.StatusCreated {
		t.Fatalf("open wallet: %d", s)
	}
	return w
}

func operation(w wallet, kind, amount, externalID, reference string) map[string]any {
	op := map[string]any{
		"providerId": "provider-a", "externalTransactionId": externalID, "playerId": w.PlayerID, "walletId": w.ID,
		"roundId": "round-1", "gameId": "game-1", "kind": kind, "money": map[string]string{"amount": amount, "currency": "BRL"},
	}
	if reference != "" {
		op["referenceExternalTransactionId"] = reference
	}
	return op
}

func balance(t *testing.T, pool *pgxpool.Pool, walletID string) int64 {
	t.Helper()
	var b int64
	if err := pool.QueryRow(context.Background(), `SELECT balance_minor FROM wallets WHERE id = $1`, walletID).Scan(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

// Enunciado 13.5: consumer dies after the commit and before deleting the
// message; the redelivery is answered by the inbox, never applied twice.
func TestConsumerCrashAfterCommitIsRedeliveredWithoutDoubleDebit(t *testing.T) {
	pool := dbPool(t)
	api := spawn(t, "api", "api")
	api.waitReady(t)
	w := openWallet(t, api, "100.00")
	crashing := spawn(t, "consumer-crash", "consumer", "FAILPOINT=consumer.after_commit=exit")

	op := operation(w, "BET", "10.00", uuid.NewString(), "")
	op["idempotencyKey"] = "provider-a:" + op["externalTransactionId"].(string)
	msg := uuid.NewString()
	body, _ := json.Marshal(map[string]any{"messageId": msg, "type": "WagerTransactionRequested",
		"occurredAt": time.Now().UTC().Format(time.RFC3339Nano), "data": op})
	client, _ := env.LocalStack.SQS(context.Background(), "111111111111")
	if _, err := client.SendMessage(context.Background(), &sqs.SendMessageInput{
		QueueUrl: aws.String(queues.in), MessageBody: aws.String(string(body)),
		MessageGroupId: aws.String(w.ID), MessageDeduplicationId: aws.String(uuid.NewString()),
	}); err != nil {
		t.Fatal(err)
	}

	if code := crashing.waitExit(t, 60*time.Second); code != 137 {
		t.Fatalf("consumer exit code %d, want 137 from the failpoint", code)
	}
	if n := count(t, pool, `SELECT count(*) FROM inbox_messages WHERE message_id = $1`, msg); n != 1 {
		t.Fatalf("commit before the crash expected; inbox rows = %d", n)
	}

	spawn(t, "consumer", "consumer")
	// Visibility timeout is 30 s: the message comes back once and is answered by
	// the inbox. Queue-wide attributes are this test's own signal: the crash
	// package provisions wager-transactions.fifo once for the whole package and
	// runs its tests sequentially, so no other test or process touches this
	// queue while this one is running.
	eventually(t, 60*time.Second, func() bool {
		out, err := client.GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{
			QueueUrl: aws.String(queues.in), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible},
		})
		return err == nil && out.Attributes["ApproximateNumberOfMessages"] == "0" && out.Attributes["ApproximateNumberOfMessagesNotVisible"] == "0"
	})
	if b := balance(t, pool, w.ID); b != 9000 {
		t.Fatalf("balance %d, want 9000 (one debit)", b)
	}
}

// Enunciado 13.6: relay dies after publishing and before confirming; another
// relay republishes the same eventId and SNS FIFO deduplicates it.
func TestRelayCrashAfterPublishIsRepublishedWithTheSameEventID(t *testing.T) {
	pool := dbPool(t)
	api := spawn(t, "api", "api")
	api.waitReady(t)
	crashing := spawn(t, "relay-crash", "outbox-relay", "FAILPOINT=outbox.after_publish=exit")
	w := openWallet(t, api, "100.00")

	if code := crashing.waitExit(t, 60*time.Second); code != 137 {
		t.Fatalf("relay exit code %d", code)
	}
	spawn(t, "relay", "outbox-relay")
	eventually(t, 60*time.Second, func() bool {
		return count(t, pool, `SELECT count(*) FROM outbox_events WHERE partition_key = $1 AND published_at IS NULL`, w.ID) == 0
	})
	if n := count(t, pool, `SELECT count(*) FROM outbox_events WHERE partition_key = $1 AND attempts >= 2`, w.ID); n < 1 {
		t.Fatal("the event published before the crash must have been claimed again")
	}
}

// Enunciado 13.7 and 13.8: resolver dies after claiming; after the lease,
// another instance resolves the pending refund.
func TestResolverCrashAfterClaimIsResumedByAnotherInstance(t *testing.T) {
	pool := dbPool(t)
	api := spawn(t, "api", "api")
	api.waitReady(t)
	provider := token(t, testenv.ProviderAID, testenv.ProviderASecret)
	w := openWallet(t, api, "100.00")
	betID := uuid.NewString()
	var pending struct {
		TransactionID string `json:"transactionId"`
	}
	refundID := uuid.NewString()
	if s := post(t, api, "/wagering/transactions", provider, "provider-a:"+refundID, operation(w, "REFUND", "30.00", refundID, betID), &pending); s != http.StatusAccepted {
		t.Fatalf("refund: %d", s)
	}
	if s := post(t, api, "/wagering/transactions", provider, "provider-a:"+betID, operation(w, "BET", "30.00", betID, ""), nil); s != http.StatusCreated {
		t.Fatalf("bet: %d", s)
	}

	crashing := spawn(t, "resolver-crash", "reference-worker", "FAILPOINT=resolver.after_claim=exit")
	if code := crashing.waitExit(t, 60*time.Second); code != 137 {
		t.Fatalf("resolver exit code %d", code)
	}
	spawn(t, "resolver", "reference-worker")
	eventually(t, 60*time.Second, func() bool {
		return count(t, pool, `SELECT count(*) FROM wager_transactions WHERE id = $1 AND status = 'PROCESSED'`, pending.TransactionID) == 1
	})
	if b := balance(t, pool, w.ID); b != 10000 {
		t.Fatalf("balance %d", b)
	}
}

// Enunciado 13.8: after a kill -9 of every process, idempotency, pending
// operations and balances survive.
func TestRestartPreservesIdempotencyAndPendingOperations(t *testing.T) {
	pool := dbPool(t)
	provider := token(t, testenv.ProviderAID, testenv.ProviderASecret)
	first := spawn(t, "api-1", "api")
	first.waitReady(t)
	w := openWallet(t, first, "100.00")
	betID := uuid.NewString()
	var original struct {
		TransactionID string `json:"transactionId"`
	}
	post(t, first, "/wagering/transactions", provider, "provider-a:"+betID, operation(w, "BET", "10.00", betID, ""), &original)
	refundID := uuid.NewString()
	laterBet := uuid.NewString()
	post(t, first, "/wagering/transactions", provider, "provider-a:"+refundID, operation(w, "REFUND", "20.00", refundID, laterBet), nil)
	_ = first.cmd.Process.Signal(syscall.SIGKILL)
	first.waitExit(t, 10*time.Second)

	second := spawn(t, "api-2", "all")
	second.waitReady(t)
	var replay struct {
		TransactionID    string `json:"transactionId"`
		IdempotentReplay bool   `json:"idempotentReplay"`
	}
	if s := post(t, second, "/wagering/transactions", provider, "provider-a:"+betID, operation(w, "BET", "10.00", betID, ""), &replay); s != http.StatusOK ||
		!replay.IdempotentReplay || replay.TransactionID != original.TransactionID {
		t.Fatalf("replay after restart: %d %+v", s, replay)
	}
	post(t, second, "/wagering/transactions", provider, "provider-a:"+laterBet, operation(w, "BET", "20.00", laterBet, ""), nil)
	eventually(t, 30*time.Second, func() bool { return balance(t, pool, w.ID) == 9000 })
}

// requestOutcome carries one in-flight request's result to the test goroutine;
// only that goroutine calls t.Fatal, so this type touches no testing.T.
type requestOutcome struct {
	status int
	err    error
}

// SIGTERM reports 503 on /health/ready before the listener closes (the
// SHUTDOWN_READINESS_DELAY window), lets every request already in flight
// finish with 201, drains and exits cleanly with exactly one debit per
// accepted request.
func TestSIGTERMDrainsAndExitsCleanly(t *testing.T) {
	pool := dbPool(t)
	api := spawn(t, "api", "all", "SHUTDOWN_READINESS_DELAY=2s")
	api.waitReady(t)
	provider := token(t, testenv.ProviderAID, testenv.ProviderASecret)

	const n = 20
	wallets := make([]wallet, n)
	for i := range wallets {
		wallets[i] = openWallet(t, api, "100.00")
	}

	outcomes := make([]requestOutcome, n)
	var sent, wg sync.WaitGroup
	sent.Add(n)
	wg.Add(n)
	for i, w := range wallets {
		go func(i int, w wallet) {
			defer wg.Done()
			id := uuid.NewString()
			b, _ := json.Marshal(operation(w, "BET", "1.00", id, ""))
			req, _ := http.NewRequest(http.MethodPost, "http://"+api.addr+"/wagering/transactions", bytes.NewReader(b))
			req.Header.Set("Authorization", "Bearer "+provider)
			req.Header.Set("Idempotency-Key", "provider-a:"+id)
			sent.Done() // signals the request is about to be dispatched, before SIGTERM is sent below
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				outcomes[i] = requestOutcome{err: err}
				return
			}
			defer resp.Body.Close()
			outcomes[i] = requestOutcome{status: resp.StatusCode}
		}(i, w)
	}
	sent.Wait()
	time.Sleep(50 * time.Millisecond) // let the dispatched requests actually reach the server
	_ = api.cmd.Process.Signal(syscall.SIGTERM)

	// The readiness delay must report 503 before the listener closes: poll
	// well inside the 2 s window and fail if the connection is refused first.
	sawUnavailable := false
	for deadline := time.Now().Add(1500 * time.Millisecond); time.Now().Before(deadline); {
		resp, err := http.Get("http://" + api.addr + "/health/ready")
		if err != nil {
			t.Fatalf("/health/ready: connection failed before a 503 was observed: %v", err)
		}
		status := resp.StatusCode
		resp.Body.Close()
		if status == http.StatusServiceUnavailable {
			sawUnavailable = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !sawUnavailable {
		t.Fatal("/health/ready never answered 503 during the readiness delay")
	}

	wg.Wait()
	if code := api.waitExit(t, 20*time.Second); code != 0 {
		t.Fatalf("exit code %d after SIGTERM", code)
	}

	// Every request above began before SIGTERM was sent (guarded by sent.Wait
	// plus the buffer): all of them must have completed with 201, none refused.
	created := 0
	walletIDs := make([]string, 0, n)
	for i, o := range outcomes {
		if o.err != nil {
			t.Fatalf("request %d: connection error: %v", i, o.err)
		}
		if o.status != http.StatusCreated {
			t.Fatalf("request %d: status %d, want 201 (sent before SIGTERM)", i, o.status)
		}
		created++
		walletIDs = append(walletIDs, wallets[i].ID)
	}
	if debits := count(t, pool, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = ANY($1::uuid[]) AND direction = 'DEBIT'`, walletIDs); debits != created {
		t.Fatalf("debit rows = %d, want %d (one per accepted request)", debits, created)
	}

	for _, w := range wallets {
		var stored, ledger int64
		if err := pool.QueryRow(context.Background(), `SELECT w.balance_minor,
			COALESCE(SUM(CASE e.direction WHEN 'CREDIT' THEN e.amount_minor ELSE -e.amount_minor END), 0)::bigint
			FROM wallets w LEFT JOIN wallet_ledger_entries e ON e.wallet_id = w.id WHERE w.id = $1 GROUP BY w.balance_minor`, w.ID).Scan(&stored, &ledger); err != nil {
			t.Fatal(err)
		}
		if stored != ledger {
			t.Fatalf("wallet %s: stored %d ledger %d", w.ID, stored, ledger)
		}
	}
}
