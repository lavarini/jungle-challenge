package snsout

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/smithy-go"

	"github.com/lavarini/backend-challenge-go/internal/adapters/outbox"
)

type fakeSNS struct {
	in  *sns.PublishInput
	err error
}

func (f *fakeSNS) Publish(_ context.Context, in *sns.PublishInput, _ ...func(*sns.Options)) (*sns.PublishOutput, error) {
	f.in = in
	return &sns.PublishOutput{}, f.err
}

func TestPublishMapsTheEvent(t *testing.T) {
	f := &fakeSNS{}
	err := New(f, "arn:topic").Publish(context.Background(), outbox.Message{EventID: "e1", PartitionKey: "w1", EventType: "WalletBalanceChanged", Payload: []byte(`{"a":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if aws.ToString(f.in.TopicArn) != "arn:topic" || aws.ToString(f.in.Message) != `{"a":1}` ||
		aws.ToString(f.in.MessageGroupId) != "w1" || aws.ToString(f.in.MessageDeduplicationId) != "e1" ||
		aws.ToString(f.in.MessageAttributes["eventType"].StringValue) != "WalletBalanceChanged" {
		t.Fatalf("input %+v", f.in)
	}
}

func TestPublishClassifiesPermanentErrors(t *testing.T) {
	for _, code := range []string{"InvalidParameter", "ParameterValueInvalid"} {
		err := New(&fakeSNS{err: &smithy.GenericAPIError{Code: code, Message: "bad"}}, "arn").Publish(context.Background(), outbox.Message{})
		if !errors.Is(err, outbox.ErrPermanent) {
			t.Fatalf("%s error = %v, want permanent", code, err)
		}
	}
}

// Configuration errors (missing topic, denied caller) can heal without
// touching the event, so they must retry instead of quarantining it.
func TestPublishTreatsConfigurationErrorsAsTransient(t *testing.T) {
	for _, code := range []string{"NotFound", "AuthorizationError", "Throttling"} {
		err := New(&fakeSNS{err: &smithy.GenericAPIError{Code: code, Message: "not now"}}, "arn").Publish(context.Background(), outbox.Message{})
		if err == nil || errors.Is(err, outbox.ErrPermanent) {
			t.Fatalf("%s error = %v, want transient (non-nil, not ErrPermanent)", code, err)
		}
	}
}

type fakeTopics struct {
	attrs map[string]string
	err   error
	arn   string
}

func (f *fakeTopics) GetTopicAttributes(_ context.Context, in *sns.GetTopicAttributesInput, _ ...func(*sns.Options)) (*sns.GetTopicAttributesOutput, error) {
	f.arn = aws.ToString(in.TopicArn)
	if f.err != nil {
		return nil, f.err
	}
	return &sns.GetTopicAttributesOutput{Attributes: f.attrs}, nil
}

// A wrong or standard topic would reject every Publish with InvalidParameter
// and quarantine the whole stream, so the relay refuses to start instead.
func TestVerifyFIFOTopic(t *testing.T) {
	ok := &fakeTopics{attrs: map[string]string{"FifoTopic": "true", "ContentBasedDeduplication": "false"}}
	if err := VerifyFIFOTopic(context.Background(), ok, "arn:events.fifo"); err != nil || ok.arn != "arn:events.fifo" {
		t.Fatalf("FIFO topic: err %v, asked for %q", err, ok.arn)
	}
	bad := map[string]*fakeTopics{
		"standard topic": {attrs: map[string]string{"TopicArn": "arn:events"}},
		"fifo false":     {attrs: map[string]string{"FifoTopic": "false"}},
		"missing topic":  {err: &smithy.GenericAPIError{Code: "NotFound", Message: "Topic does not exist"}},
	}
	for name, f := range bad {
		err := VerifyFIFOTopic(context.Background(), f, "arn:events")
		if err == nil || !strings.Contains(err.Error(), "arn:events") {
			t.Errorf("%s: error = %v, want a startup error naming the topic", name, err)
		}
	}
}
