//go:build e2e

package crash

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"

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

func readyStatus(p *proc) int {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("http://" + p.addr + "/health/ready")
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

func (p *proc) alive() bool {
	select {
	case err := <-p.done:
		p.done <- err
		return false
	default:
		return true
	}
}

// Enunciado §3: with SQS and SNS (LocalStack) unreachable, three independent
// processes stay up and report not-ready; HTTP operations still commit because
// the database is up, and their events wait in the outbox. After recovery the
// queue is consumed again, a message delivered twice is applied once, every
// event confirmed during the outage is published, and the wallet reconciles.
func TestQueueOutageKeepsProcessesUpAndLosesNothing(t *testing.T) {
	pool := dbPool(t)
	procs := []*proc{spawn(t, "sqs-outage-1", "all"), spawn(t, "sqs-outage-2", "all"), spawn(t, "sqs-outage-3", "all")}
	for _, p := range procs {
		p.waitReady(t)
	}
	api := procs[0]
	provider := token(t, testenv.ProviderAID, testenv.ProviderASecret)
	internal := token(t, testenv.InternalID, testenv.InternalSecret)
	w := openWallet(t, api, "100.00")

	id := env.LocalStackContainerID()
	docker(t, "pause", id)
	paused := true
	t.Cleanup(func() {
		if paused {
			_ = exec.Command("docker", "unpause", id).Run()
		}
	})

	for _, p := range procs {
		eventually(t, 30*time.Second, func() bool { return readyStatus(p) == http.StatusServiceUnavailable })
	}

	httpExt := fmt.Sprintf("sqs-outage-http-%d", time.Now().UnixNano())
	var bet struct {
		TransactionID string `json:"transactionId"`
	}
	if code := post(t, api, "/wagering/transactions", provider, "provider-a:"+httpExt,
		operation(w, "BET", "10.00", httpExt, ""), &bet); code != http.StatusCreated {
		t.Fatalf("HTTP bet during the broker outage: status %d, want 201 (the database is up)", code)
	}
	// Several relay ticks (WORKER_POLL_INTERVAL=100ms) run against the paused
	// broker: the bet's event must still be waiting, neither published nor
	// quarantined.
	time.Sleep(3 * time.Second)
	if n := count(t, pool, `SELECT count(*) FROM outbox_events
		WHERE aggregate_id = $1 AND event_type = 'WagerTransactionProcessed' AND published_at IS NULL AND dead_at IS NULL`,
		bet.TransactionID); n != 1 {
		t.Fatalf("the bet committed during the outage should wait in the outbox; waiting events: %d", n)
	}
	for _, p := range procs {
		if !p.alive() {
			t.Fatalf("%s exited during the broker outage", p.name)
		}
	}

	docker(t, "unpause", id)
	paused = false
	for _, p := range procs {
		eventually(t, 60*time.Second, func() bool { return readyStatus(p) == http.StatusOK })
	}

	// The same message sent twice, with distinct deduplication ids so that SQS
	// FIFO does not hide the duplicate: the inbox must answer the second one.
	sqsExt := uuid.NewString()
	op := operation(w, "BET", "10.00", sqsExt, "")
	op["idempotencyKey"] = "provider-a:" + sqsExt
	msg := uuid.NewString()
	body, _ := json.Marshal(map[string]any{"messageId": msg, "type": "WagerTransactionRequested",
		"occurredAt": time.Now().UTC().Format(time.RFC3339Nano), "data": op})
	client, err := env.LocalStack.SQS(context.Background(), "111111111111")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := client.SendMessage(context.Background(), &sqs.SendMessageInput{
			QueueUrl: aws.String(queues.in), MessageBody: aws.String(string(body)),
			MessageGroupId: aws.String(w.ID), MessageDeduplicationId: aws.String(uuid.NewString()),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Queue-wide attributes are this test's own signal: the crash package runs
	// its tests sequentially against one queue.
	eventually(t, 60*time.Second, func() bool {
		out, err := client.GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{
			QueueUrl: aws.String(queues.in), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible},
		})
		return err == nil && out.Attributes["ApproximateNumberOfMessages"] == "0" && out.Attributes["ApproximateNumberOfMessagesNotVisible"] == "0"
	})
	if n := count(t, pool, `SELECT count(*) FROM inbox_messages WHERE message_id = $1`, msg); n != 1 {
		t.Fatalf("inbox rows for the message: %d, want 1", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, w.ID); n != 3 {
		t.Fatalf("ledger entries: %d, want 3 (opening and two bets)", n)
	}
	if b := balance(t, pool, w.ID); b != 8000 {
		t.Fatalf("balance %d minor units, want 8000", b)
	}

	eventually(t, 60*time.Second, func() bool {
		return count(t, pool, `SELECT count(*) FROM outbox_events WHERE partition_key = $1 AND published_at IS NULL`, w.ID) == 0
	})
	if n := count(t, pool, `SELECT count(*) FROM outbox_events WHERE partition_key = $1 AND dead_at IS NOT NULL`, w.ID); n != 0 {
		t.Fatalf("%d events quarantined; a broker outage is transient and must never quarantine", n)
	}

	var rec struct {
		Consistent     bool `json:"consistent"`
		CheckedEntries int  `json:"checkedEntries"`
	}
	if code := post(t, api, "/wallets/"+w.ID+"/reconciliation", internal, "", nil, &rec); code != http.StatusOK || !rec.Consistent || rec.CheckedEntries != 3 {
		t.Fatalf("reconciliation: status %d consistent=%v entries=%d", code, rec.Consistent, rec.CheckedEntries)
	}
}
