//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"

	"github.com/lavarini/backend-challenge-go/internal/adapters/sqsin"
	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/platform/runner"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

type queues struct {
	in, dlq string
}

func consumerQueues(t *testing.T) queues {
	t.Helper()
	ctx := context.Background()
	in, err := env.LocalStack.QueueURL(ctx, "wager-transactions.fifo")
	if err != nil {
		t.Fatal(err)
	}
	dlq, err := env.LocalStack.QueueURL(ctx, "wager-transactions-dlq.fifo")
	if err != nil {
		t.Fatal(err)
	}
	return queues{in: in, dlq: dlq}
}

// startConsumer runs a real consumer until the test ends.
func startConsumer(t *testing.T, s stack, q queues) {
	t.Helper()
	client, err := env.LocalStack.SQS(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	c := sqsin.New(client, s.submit, sqsin.Config{
		QueueURL: q.in, DLQURL: q.dlq, Senders: map[string]string{"111111111111": "provider-a"},
		MaxMessages: 10, WaitSeconds: 1, MaxVisibility: 2 * time.Second,
	}, quietLog)
	r := runner.Start(c.Loop)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = r.Stop(ctx)
	})
}

func produce(t *testing.T, accessKey, queueURL string, c app.SubmitCommand, messageID string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"messageId": messageID, "type": "WagerTransactionRequested", "occurredAt": time.Now().UTC().Format(time.RFC3339Nano),
		"data": map[string]any{
			"providerId": c.ProviderID, "externalTransactionId": c.ExternalTransactionID, "idempotencyKey": c.IdempotencyKey,
			"playerId": c.PlayerID, "walletId": c.WalletID, "roundId": c.RoundID, "gameId": c.GameID, "kind": string(c.Kind),
			"money": map[string]string{"amount": c.Money.String(), "currency": string(c.Money.Currency())},
		},
	})
	client, err := env.LocalStack.SQS(context.Background(), accessKey)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.SendMessage(context.Background(), &sqs.SendMessageInput{
		QueueUrl: aws.String(queueURL), MessageBody: aws.String(string(body)),
		MessageGroupId: aws.String(c.WalletID), MessageDeduplicationId: aws.String(uuid.NewString()),
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func waitFor(t *testing.T, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", within)
}

// deadLetters drains the DLQ and returns failureCode by message group.
func deadLetters(t *testing.T, dlq string) map[string]string {
	t.Helper()
	client, _ := env.LocalStack.SQS(context.Background(), "test")
	out := map[string]string{}
	for i := 0; i < 3; i++ {
		res, err := client.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(dlq), MaxNumberOfMessages: 10, WaitTimeSeconds: 1, MessageAttributeNames: []string{"All"},
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameMessageGroupId},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range res.Messages {
			out[m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]] = aws.ToString(m.MessageAttributes["failureCode"].StringValue)
			_, _ = client.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{QueueUrl: aws.String(dlq), ReceiptHandle: m.ReceiptHandle})
		}
	}
	return out
}

func TestConsumerAppliesAMessageOnceEvenWhenRedelivered(t *testing.T) {
	s := newStack(t)
	q := consumerQueues(t)
	startConsumer(t, s, q)
	w := openWallet(t, s, "100.00")
	c := command(t, w, wagering.Bet, "10.00", uuid.NewString())
	msg := uuid.NewString()

	produce(t, "111111111111", q.in, c, msg)
	produce(t, "111111111111", q.in, c, msg) // same envelope messageId, new SQS dedup id: a redelivery
	waitFor(t, 15*time.Second, func() bool {
		return count(t, s.pool, `SELECT count(*) FROM inbox_messages WHERE message_id = $1`, msg) == 1 && balanceOf(t, s, w.ID) == "90.00"
	})
	time.Sleep(2 * time.Second)
	if got := balanceOf(t, s, w.ID); got != "90.00" {
		t.Fatalf("balance %s after redelivery", got)
	}
}

func TestHTTPThenSQSForTheSameOperationDebitsOnce(t *testing.T) {
	s := newStack(t)
	q := consumerQueues(t)
	startConsumer(t, s, q)
	w := openWallet(t, s, "100.00")
	c := command(t, w, wagering.Bet, "10.00", uuid.NewString())
	if _, err := s.submit.Execute(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	msg := uuid.NewString()
	produce(t, "111111111111", q.in, c, msg)
	waitFor(t, 15*time.Second, func() bool {
		return count(t, s.pool, `SELECT count(*) FROM inbox_messages WHERE message_id = $1`, msg) == 1
	})
	if got := balanceOf(t, s, w.ID); got != "90.00" {
		t.Fatalf("balance %s", got)
	}
}

func TestConsumerDeadLettersUnauthorizedAndInvalidMessages(t *testing.T) {
	s := newStack(t)
	q := consumerQueues(t)
	deadLetters(t, q.dlq) // start from an empty DLQ
	startConsumer(t, s, q)
	w1 := openWallet(t, s, "100.00")
	w2 := openWallet(t, s, "100.00")

	// provider-b's credential claiming provider-a.
	produce(t, "222222222222", q.in, command(t, w1, wagering.Bet, "10.00", uuid.NewString()), uuid.NewString())
	// Wallet that does not exist: corrigible, never retried.
	missing := command(t, w2, wagering.Bet, "10.00", uuid.NewString())
	missing.WalletID = uuid.NewString()
	produce(t, "111111111111", q.in, missing, uuid.NewString())

	var got map[string]string
	waitFor(t, 20*time.Second, func() bool {
		for k, v := range deadLetters(t, q.dlq) {
			if got == nil {
				got = map[string]string{}
			}
			got[k] = v
		}
		return len(got) >= 2
	})
	if got[w1.ID] != "PROVIDER_NOT_AUTHORIZED" || got[missing.WalletID] != "WALLET_NOT_FOUND" {
		t.Fatalf("dead letters %v", got)
	}
	if b := balanceOf(t, s, w1.ID); b != "100.00" {
		t.Fatalf("unauthorized message moved money: %s", b)
	}
}
