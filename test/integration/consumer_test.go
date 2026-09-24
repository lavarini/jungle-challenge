//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
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

// consumerQueues creates a fresh FIFO queue and DLQ for this test, with the
// same attributes as the real ones. A shared queue let an abandoned long poll
// from a previous test receive the next test's message, which then stayed
// invisible for the full VisibilityTimeout; per-test queues remove that.
func consumerQueues(t *testing.T) queues {
	t.Helper()
	ctx := context.Background()
	client, err := env.LocalStack.SQS(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	suffix := uuid.NewString()[:8]

	dlqOut, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName:  aws.String("wager-dlq-" + suffix + ".fifo"),
		Attributes: map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false"},
	})
	if err != nil {
		t.Fatal(err)
	}
	dlqAttrs, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: dlqOut.QueueUrl, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	if err != nil {
		t.Fatal(err)
	}
	redrive, _ := json.Marshal(map[string]string{
		"deadLetterTargetArn": dlqAttrs.Attributes["QueueArn"], "maxReceiveCount": "20",
	})
	inOut, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String("wager-in-" + suffix + ".fifo"),
		Attributes: map[string]string{
			"FifoQueue": "true", "ContentBasedDeduplication": "false",
			"VisibilityTimeout": "30", "ReceiveMessageWaitTimeSeconds": "20",
			"RedrivePolicy": string(redrive),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: inOut.QueueUrl})
		_, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: dlqOut.QueueUrl})
	})
	return queues{in: aws.ToString(inOut.QueueUrl), dlq: aws.ToString(dlqOut.QueueUrl)}
}

// tLogWriter routes consumer log lines through t.Log, so a failing test shows
// what the consumer actually did instead of nothing.
type tLogWriter struct{ t *testing.T }

func (w tLogWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func testLogger(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(tLogWriter{t}, nil))
}

// startConsumer runs a real consumer until the test ends.
func startConsumer(t *testing.T, s stack, q queues) {
	t.Helper()
	client, err := env.LocalStack.SQS(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	c := sqsin.New(client, s.submit, sqsin.Config{
		QueueURL: q.in, DLQURL: q.dlq, Senders: map[string]string{"111111111111": "provider-a", "333333333333": "provider-b"},
		MaxMessages: 10, WaitSeconds: 1, MaxVisibility: 2 * time.Second, MaxReceives: 5,
	}, testLogger(t))
	r := runner.Start(c.Loop)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = r.Stop(ctx)
	})
}

// envelope builds the WagerTransactionRequested body for c under messageID.
func envelope(c app.SubmitCommand, messageID string) string {
	body, _ := json.Marshal(map[string]any{
		"messageId": messageID, "type": "WagerTransactionRequested", "occurredAt": time.Now().UTC().Format(time.RFC3339Nano),
		"data": map[string]any{
			"providerId": c.ProviderID, "externalTransactionId": c.ExternalTransactionID, "idempotencyKey": c.IdempotencyKey,
			"playerId": c.PlayerID, "walletId": c.WalletID, "roundId": c.RoundID, "gameId": c.GameID, "kind": string(c.Kind),
			"money": map[string]string{"amount": c.Money.String(), "currency": string(c.Money.Currency())},
		},
	})
	return string(body)
}

// send publishes body to queueURL under group, with a fresh dedup id: SQS
// treats it as a new delivery even when the body (and hence the envelope's
// messageId) is unchanged.
func send(t *testing.T, accessKey, queueURL, group, body string) {
	t.Helper()
	client, err := env.LocalStack.SQS(context.Background(), accessKey)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.SendMessage(context.Background(), &sqs.SendMessageInput{
		QueueUrl: aws.String(queueURL), MessageBody: aws.String(body),
		MessageGroupId: aws.String(group), MessageDeduplicationId: aws.String(uuid.NewString()),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func produce(t *testing.T, accessKey, queueURL string, c app.SubmitCommand, messageID string) string {
	t.Helper()
	body := envelope(c, messageID)
	send(t, accessKey, queueURL, c.WalletID, body)
	return body
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

// queueDepth returns the input queue's visible plus in-flight message count,
// so a caller can prove a message was actually received and processed (and
// then deleted, per Consumer.handle/delete) rather than just never delivered
// -- a 2s sleep before checking downstream state cannot tell those apart.
func queueDepth(t *testing.T, queueURL string) int {
	t.Helper()
	client, err := env.LocalStack.SQS(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	out, err := client.GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(queueURL),
		AttributeNames: []types.QueueAttributeName{
			types.QueueAttributeNameApproximateNumberOfMessages,
			types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	visible, _ := strconv.Atoi(out.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessages)])
	inFlight, _ := strconv.Atoi(out.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessagesNotVisible)])
	return visible + inFlight
}

func TestConsumerAppliesAMessageOnceEvenWhenRedelivered(t *testing.T) {
	s := newStack(t)
	q := consumerQueues(t)
	startConsumer(t, s, q)
	w := openWallet(t, s, "100.00")
	c := command(t, w, wagering.Bet, "10.00", uuid.NewString())
	msg := uuid.NewString()
	body := envelope(c, msg)

	send(t, "111111111111", q.in, c.WalletID, body)
	send(t, "111111111111", q.in, c.WalletID, body) // same body, new SQS dedup id: a real redelivery
	waitFor(t, 15*time.Second, func() bool {
		return count(t, s.pool, `SELECT count(*) FROM inbox_messages WHERE message_id = $1`, msg) == 1 && balanceOf(t, s, w.ID) == "90.00"
	})
	// Both deliveries must have actually reached the consumer and been
	// deleted (success or dead letter both delete): a passing inbox count of
	// 1 alone cannot distinguish "the second delivery was deduplicated" from
	// "the second delivery was never consumed".
	waitFor(t, 15*time.Second, func() bool { return queueDepth(t, q.in) == 0 })
	if got := balanceOf(t, s, w.ID); got != "90.00" {
		t.Fatalf("balance %s after redelivery", got)
	}
	if dl := deadLetters(t, q.dlq); dl[w.ID] != "" {
		t.Fatalf("a genuine redelivery must not be dead-lettered: %v", dl)
	}
}

func TestConsumerDeadLettersAReusedMessageIdWithADifferentBody(t *testing.T) {
	s := newStack(t)
	q := consumerQueues(t)
	startConsumer(t, s, q)
	w := openWallet(t, s, "100.00")
	c := command(t, w, wagering.Bet, "10.00", uuid.NewString())
	msg := uuid.NewString()
	produce(t, "111111111111", q.in, c, msg)
	waitFor(t, 15*time.Second, func() bool {
		return count(t, s.pool, `SELECT count(*) FROM inbox_messages WHERE message_id = $1`, msg) == 1
	})

	changed := c
	changed.Money = mustBRL(t, "20.00") // same messageId, different body: a reused id, not a redelivery
	send(t, "111111111111", q.in, c.WalletID, envelope(changed, msg))

	var got map[string]string
	waitFor(t, 15*time.Second, func() bool {
		got = deadLetters(t, q.dlq)
		return got[w.ID] != ""
	})
	if got[w.ID] != "INBOX_PAYLOAD_MISMATCH" {
		t.Fatalf("dead letters %v", got)
	}
	if b := balanceOf(t, s, w.ID); b != "90.00" {
		t.Fatalf("balance changed by a mismatched reused messageId: %s", b)
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
	startConsumer(t, s, q)
	w1 := openWallet(t, s, "100.00")
	w2 := openWallet(t, s, "100.00")
	w3 := openWallet(t, s, "100.00")

	// Sender not bound to any provider.
	produce(t, "222222222222", q.in, command(t, w1, wagering.Bet, "10.00", uuid.NewString()), uuid.NewString())
	// Wallet that does not exist: corrigible, never retried.
	missing := command(t, w2, wagering.Bet, "10.00", uuid.NewString())
	missing.WalletID = uuid.NewString()
	produce(t, "111111111111", q.in, missing, uuid.NewString())
	// Sender bound to provider-b, envelope claims provider-a.
	produce(t, "333333333333", q.in, command(t, w3, wagering.Bet, "10.00", uuid.NewString()), uuid.NewString())

	var got map[string]string
	waitFor(t, 20*time.Second, func() bool {
		for k, v := range deadLetters(t, q.dlq) {
			if got == nil {
				got = map[string]string{}
			}
			got[k] = v
		}
		return len(got) >= 3
	})
	if got[w1.ID] != "PROVIDER_NOT_AUTHORIZED" || got[missing.WalletID] != "WALLET_NOT_FOUND" || got[w3.ID] != "PROVIDER_NOT_AUTHORIZED" {
		t.Fatalf("dead letters %v", got)
	}
	if b := balanceOf(t, s, w1.ID); b != "100.00" {
		t.Fatalf("unauthorized message moved money: %s", b)
	}
	if b := balanceOf(t, s, w3.ID); b != "100.00" {
		t.Fatalf("mismatched-provider message moved money: %s", b)
	}
}
