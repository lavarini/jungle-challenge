//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
)

func eventually(t *testing.T, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", within)
}

func sendSQS(t *testing.T, accessKey string, body map[string]any) {
	t.Helper()
	ctx := context.Background()
	url, err := env.LocalStack.QueueURL(ctx, "wager-transactions.fifo")
	if err != nil {
		t.Fatal(err)
	}
	client, err := env.LocalStack.SQS(ctx, accessKey)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(body)
	data := body["data"].(map[string]any)
	if _, err := client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: aws.String(url), MessageBody: aws.String(string(raw)),
		MessageGroupId: aws.String(data["walletId"].(string)), MessageDeduplicationId: aws.String(uuid.NewString()),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSameOperationViaHTTPAndSQSDebitsOnce(t *testing.T) {
	provider, internal := tokens(t)
	pool := dbPool(t)
	w := openWallet(t, procs[0], internal, "100.00")
	b := bet(w, uuid.NewString(), "10.00")
	key := "provider-a:" + b["externalTransactionId"].(string)
	if s := call(t, procs[1], http.MethodPost, "/wagering/transactions", provider, key, b, nil); s != http.StatusCreated {
		t.Fatalf("http submit: %d", s)
	}
	data := map[string]any{}
	for k, v := range b {
		data[k] = v
	}
	data["idempotencyKey"] = key
	msg := uuid.NewString()
	sendSQS(t, "111111111111", map[string]any{"messageId": msg, "type": "WagerTransactionRequested",
		"occurredAt": time.Now().UTC().Format(time.RFC3339Nano), "data": data})

	eventually(t, 20*time.Second, func() bool {
		return countRows(t, pool, `SELECT count(*) FROM inbox_messages WHERE message_id = $1`, msg) == 1
	})
	var final walletJSON
	call(t, procs[2], http.MethodGet, "/wallets/"+w.ID, internal, "", nil, &final)
	if final.Balance.Amount != "90.00" {
		t.Fatalf("balance %s", final.Balance.Amount)
	}
	requireReconciled(t, pool, w.ID)
}

func TestRefundBeforeBetResolvesAcrossProcesses(t *testing.T) {
	provider, internal := tokens(t)
	w := openWallet(t, procs[0], internal, "100.00")
	betExternalID := uuid.NewString()
	refund := bet(w, uuid.NewString(), "30.00")
	refund["kind"] = "REFUND"
	refund["referenceExternalTransactionId"] = betExternalID

	var pending submitJSON
	refundKey := "provider-a:" + refund["externalTransactionId"].(string)
	if s := call(t, procs[0], http.MethodPost, "/wagering/transactions", provider, refundKey, refund, &pending); s != http.StatusAccepted {
		t.Fatalf("refund before bet: %d %+v", s, pending)
	}
	if s := call(t, procs[1], http.MethodPost, "/wagering/transactions", provider, "provider-a:"+betExternalID, bet(w, betExternalID, "30.00"), nil); s != http.StatusCreated {
		t.Fatalf("bet: %d", s)
	}
	eventually(t, 20*time.Second, func() bool {
		var tx struct {
			Status string `json:"status"`
		}
		call(t, procs[2], http.MethodGet, "/wagering/transactions/"+pending.TransactionID, provider, "", nil, &tx)
		return tx.Status == "PROCESSED"
	})
	var final walletJSON
	call(t, procs[2], http.MethodGet, "/wallets/"+w.ID, internal, "", nil, &final)
	if final.Balance.Amount != "100.00" {
		t.Fatalf("balance %s", final.Balance.Amount)
	}
}

func TestEventsReachTheSubscriberInWalletOrder(t *testing.T) {
	provider, internal := tokens(t)
	pool := dbPool(t)
	w := openWallet(t, procs[0], internal, "100.00")
	for i := 0; i < 3; i++ {
		b := bet(w, uuid.NewString(), "1.00")
		call(t, procs[i], http.MethodPost, "/wagering/transactions", provider, "provider-a:"+b["externalTransactionId"].(string), b, nil)
	}
	rows, err := pool.Query(context.Background(), `SELECT event_id::text FROM outbox_events WHERE partition_key = $1 ORDER BY seq`, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		want = append(want, id)
	}

	url, _ := env.LocalStack.QueueURL(context.Background(), "wallet-events-audit.fifo")
	client, _ := env.LocalStack.SQS(context.Background(), "test")
	var got []string
	eventually(t, 30*time.Second, func() bool {
		out, err := client.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(url), MaxNumberOfMessages: 10, WaitTimeSeconds: 1,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameMessageGroupId},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range out.Messages {
			if m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)] == w.ID {
				var e struct {
					EventID string `json:"eventId"`
				}
				_ = json.Unmarshal([]byte(aws.ToString(m.Body)), &e)
				got = append(got, e.EventID)
			}
			_, _ = client.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{QueueUrl: aws.String(url), ReceiptHandle: m.ReceiptHandle})
		}
		return len(got) >= len(want)
	})
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("subscriber order %v, want %v", got, want)
		}
	}
}
