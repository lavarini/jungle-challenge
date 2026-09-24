package snsout

import (
	"context"
	"errors"
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
