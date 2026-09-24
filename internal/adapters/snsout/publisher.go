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

// permanentCodes are request errors a retry cannot fix.
var permanentCodes = map[string]bool{
	"InvalidParameter": true, "InvalidParameterValue": true, "NotFound": true, "AuthorizationError": true,
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
