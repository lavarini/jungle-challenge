// Package snsout publishes outbox events to the SNS FIFO topic (ADR 0012).
package snsout

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/aws/smithy-go"

	"github.com/lavarini/backend-challenge-go/internal/adapters/outbox"
)

type API interface {
	Publish(ctx context.Context, in *sns.PublishInput, opts ...func(*sns.Options)) (*sns.PublishOutput, error)
}

type Publisher struct {
	api      API
	topicARN string
}

func New(api API, topicARN string) *Publisher { return &Publisher{api: api, topicARN: topicARN} }

// permanentCodes are request-shape errors a retry cannot fix: the payload or
// its attributes are malformed, so republishing the same message would fail
// again forever. "NotFound" (missing topic) and "AuthorizationError" are
// left out on purpose: those can be transient misconfiguration (a topic or
// policy not yet propagated) that heals without touching the event, so they
// retry like any other transient error instead of quarantining the event.
var permanentCodes = map[string]bool{
	"InvalidParameter":      true, // InvalidParameterException
	"ParameterValueInvalid": true, // InvalidParameterValueException (message too large, bad attribute, ...)
}

// Publish sends the event with the wallet as message group (order per wallet)
// and the event id as deduplication id (republications collapse in SNS's window).
func (p *Publisher) Publish(ctx context.Context, m outbox.Message) error {
	_, err := p.api.Publish(ctx, &sns.PublishInput{
		TopicArn: aws.String(p.topicARN), Message: aws.String(string(m.Payload)),
		MessageGroupId: aws.String(m.PartitionKey), MessageDeduplicationId: aws.String(m.EventID),
		MessageAttributes: map[string]snstypes.MessageAttributeValue{
			"eventType": {DataType: aws.String("String"), StringValue: aws.String(m.EventType)},
		},
	})
	if err == nil {
		return nil
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && permanentCodes[apiErr.ErrorCode()] {
		return fmt.Errorf("%w: %w", outbox.ErrPermanent, err)
	}
	return err
}

// TopicAPI is the part of SNS the startup check needs.
type TopicAPI interface {
	GetTopicAttributes(ctx context.Context, in *sns.GetTopicAttributesInput, opts ...func(*sns.Options)) (*sns.GetTopicAttributesOutput, error)
}

// VerifyFIFOTopic fails when the topic cannot be read or is not FIFO. Run at
// relay startup: a wrong or standard topic answers every Publish that carries
// a MessageGroupId with InvalidParameter, which is permanent for a real
// request-shape error and would otherwise quarantine every partition head.
func VerifyFIFOTopic(ctx context.Context, api TopicAPI, topicARN string) error {
	out, err := api.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{TopicArn: aws.String(topicARN)})
	if err != nil {
		return fmt.Errorf("snsout: read attributes of topic %s: %w", topicARN, err)
	}
	if out.Attributes["FifoTopic"] != "true" {
		return fmt.Errorf("snsout: topic %s is not a FIFO topic (FifoTopic=%q)", topicARN, out.Attributes["FifoTopic"])
	}
	return nil
}
