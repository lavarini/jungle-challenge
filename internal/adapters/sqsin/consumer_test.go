package sqsin

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

type fakeSQS struct {
	mu         sync.Mutex
	batch      []types.Message
	deleted    []string
	dlq        []*sqs.SendMessageInput
	visibility map[string]int32
}

func (f *fakeSQS) ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &sqs.ReceiveMessageOutput{Messages: f.batch}
	f.batch = nil
	return out, nil
}

func (f *fakeSQS) DeleteMessage(ctx context.Context, in *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, aws.ToString(in.ReceiptHandle))
	return &sqs.DeleteMessageOutput{}, nil
}

func (f *fakeSQS) ChangeMessageVisibility(ctx context.Context, in *sqs.ChangeMessageVisibilityInput, _ ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.visibility[aws.ToString(in.ReceiptHandle)] = in.VisibilityTimeout
	return &sqs.ChangeMessageVisibilityOutput{}, nil
}

func (f *fakeSQS) SendMessage(ctx context.Context, in *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dlq = append(f.dlq, in)
	return &sqs.SendMessageOutput{}, nil
}

type fakeSubmitter struct {
	mu     sync.Mutex
	calls  []app.SubmitCommand
	errFor map[string]error
}

func (f *fakeSubmitter) Execute(_ context.Context, c app.SubmitCommand) (app.SubmitResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	if err := f.errFor[c.ExternalTransactionID]; err != nil {
		return app.SubmitResult{}, err
	}
	return app.SubmitResult{TransactionID: "t", Status: wagering.Processed}, nil
}

func message(handle, group, sender, receiveCount, body string) types.Message {
	return types.Message{
		ReceiptHandle: aws.String(handle), MessageId: aws.String("sqs-" + handle), Body: aws.String(body),
		Attributes: map[string]string{
			string(types.MessageSystemAttributeNameSenderId):                sender,
			string(types.MessageSystemAttributeNameMessageGroupId):          group,
			string(types.MessageSystemAttributeNameApproximateReceiveCount): receiveCount,
		},
	}
}

func bodyWith(messageID, externalID string) string {
	b := strings.Replace(validBody, `"msg-123"`, `"`+messageID+`"`, 1)
	return strings.Replace(b, `"transaction-123",`, `"`+externalID+`",`, 1)
}

func newTestConsumer(f *fakeSQS, s *fakeSubmitter) *Consumer {
	return New(f, s, Config{
		QueueURL: "in", DLQURL: "dlq", Senders: map[string]string{"111111111111": "provider-a", "333333333333": "provider-b"},
		MaxMessages: 10, WaitSeconds: 0, MaxVisibility: time.Minute, MaxReceives: 5,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func poll(t *testing.T, c *Consumer) {
	t.Helper()
	if err := c.Poll(context.Background(), context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestValidMessageIsSubmittedAndDeleted(t *testing.T) {
	f := &fakeSQS{visibility: map[string]int32{}}
	s := &fakeSubmitter{}
	f.batch = []types.Message{message("h1", "w1", "111111111111", "1", bodyWith("m1", "e1"))}
	poll(t, newTestConsumer(f, s))
	if len(s.calls) != 1 || s.calls[0].Inbox == nil || s.calls[0].Inbox.MessageID != "m1" {
		t.Fatalf("calls %+v", s.calls)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "h1" || len(f.dlq) != 0 {
		t.Fatalf("deleted %v dlq %d", f.deleted, len(f.dlq))
	}
}

func TestInvalidOrUnauthorizedMessagesGoToTheDLQWithTheirReason(t *testing.T) {
	f := &fakeSQS{visibility: map[string]int32{}}
	s := &fakeSubmitter{errFor: map[string]error{"e3": app.ErrWalletNotFound}}
	f.batch = []types.Message{
		message("h1", "w1", "111111111111", "1", "not json"),
		message("h2", "w2", "222222222222", "1", bodyWith("m2", "e2")),
		message("h3", "w3", "111111111111", "1", bodyWith("m3", "e3")),
	}
	poll(t, newTestConsumer(f, s))
	codes := map[string]string{}
	for _, in := range f.dlq {
		if aws.ToString(in.QueueUrl) != "dlq" {
			t.Fatalf("sent to %s", aws.ToString(in.QueueUrl))
		}
		codes[aws.ToString(in.MessageGroupId)] = aws.ToString(in.MessageAttributes["failureCode"].StringValue)
		if in.MessageAttributes["reason"].StringValue == nil {
			t.Fatal("dead letter without reason")
		}
	}
	want := map[string]string{"w1": "INVALID_MESSAGE", "w2": "PROVIDER_NOT_AUTHORIZED", "w3": "WALLET_NOT_FOUND"}
	for group, code := range want {
		if codes[group] != code {
			t.Errorf("group %s: code %q, want %q", group, codes[group], code)
		}
	}
	if len(f.deleted) != 3 {
		t.Fatalf("dead-lettered messages must leave the input queue: deleted %v", f.deleted)
	}
	for _, c := range s.calls {
		if c.ExternalTransactionID == "e2" {
			t.Fatal("unauthorized sender reached the use case")
		}
	}
}

func TestTransientFailureBacksOffAndReleasesTheRestOfItsGroup(t *testing.T) {
	f := &fakeSQS{visibility: map[string]int32{}}
	s := &fakeSubmitter{errFor: map[string]error{"e1": app.ErrTransient}}
	f.batch = []types.Message{
		message("h1", "w1", "111111111111", "3", bodyWith("m1", "e1")),
		message("h2", "w1", "111111111111", "1", bodyWith("m2", "e2")),
		message("h3", "w2", "111111111111", "1", bodyWith("m3", "e3")),
	}
	poll(t, newTestConsumer(f, s))
	if f.visibility["h1"] != 8 {
		t.Fatalf("head visibility %d, want 2^3 = 8", f.visibility["h1"])
	}
	if v, ok := f.visibility["h2"]; !ok || v != 0 {
		t.Fatalf("tail of the failed group must be released (visibility 0), got %d %v", v, ok)
	}
	for _, c := range s.calls {
		if c.ExternalTransactionID == "e2" {
			t.Fatal("message behind a failed head was processed out of order")
		}
	}
	if len(f.deleted) != 1 || f.deleted[0] != "h3" {
		t.Fatalf("other groups must proceed: deleted %v", f.deleted)
	}
}

func TestVisibilityBackoffIsCapped(t *testing.T) {
	f := &fakeSQS{visibility: map[string]int32{}}
	s := &fakeSubmitter{errFor: map[string]error{"e1": errors.New("connection reset")}}
	f.batch = []types.Message{message("h1", "w1", "111111111111", "20", bodyWith("m1", "e1"))}
	// A high MaxReceives isolates the visibility cap from the retries-exhausted
	// gate: both act on the same ApproximateReceiveCount, but they are distinct
	// mechanisms.
	c := New(f, s, Config{
		QueueURL: "in", DLQURL: "dlq", Senders: map[string]string{"111111111111": "provider-a"},
		MaxMessages: 10, WaitSeconds: 0, MaxVisibility: time.Minute, MaxReceives: 100,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	poll(t, c)
	if f.visibility["h1"] != 60 {
		t.Fatalf("visibility %d, want cap 60", f.visibility["h1"])
	}
}

func TestTransientFailureAtMaxReceivesGoesToDLQAsRetriesExhausted(t *testing.T) {
	f := &fakeSQS{visibility: map[string]int32{}}
	s := &fakeSubmitter{errFor: map[string]error{"e1": app.ErrTransient}}
	f.batch = []types.Message{message("h1", "w1", "111111111111", "5", bodyWith("m1", "e1"))}
	poll(t, newTestConsumer(f, s))
	if len(f.dlq) != 1 || aws.ToString(f.dlq[0].MessageAttributes["failureCode"].StringValue) != "RETRIES_EXHAUSTED" {
		t.Fatalf("dlq %+v", f.dlq)
	}
	if aws.ToString(f.dlq[0].MessageAttributes["reason"].StringValue) == "" {
		t.Fatal("dead letter without reason")
	}
	if len(f.deleted) != 1 || f.deleted[0] != "h1" {
		t.Fatalf("dead-lettered message must leave the input queue: deleted %v", f.deleted)
	}
	if _, changed := f.visibility["h1"]; changed {
		t.Fatal("a message exhausted by retries must not also be retried via visibility")
	}
}

func TestSenderBoundToAnotherProviderIsRejected(t *testing.T) {
	f := &fakeSQS{visibility: map[string]int32{}}
	s := &fakeSubmitter{}
	// 333333333333 is bound to provider-b; the envelope claims provider-a.
	f.batch = []types.Message{message("h1", "w1", "333333333333", "1", bodyWith("m1", "e1"))}
	poll(t, newTestConsumer(f, s))
	if len(f.dlq) != 1 || aws.ToString(f.dlq[0].MessageAttributes["failureCode"].StringValue) != "PROVIDER_NOT_AUTHORIZED" {
		t.Fatalf("dlq %+v", f.dlq)
	}
	if got := aws.ToString(f.dlq[0].MessageDeduplicationId); got != "sqs-h1" {
		t.Fatalf("dedup id = %q, want the SQS MessageId", got)
	}
	if len(s.calls) != 0 {
		t.Fatal("mismatched provider reached the use case")
	}
	if len(f.deleted) != 1 || f.deleted[0] != "h1" {
		t.Fatalf("dead-lettered message must leave the input queue: deleted %v", f.deleted)
	}
}
