package platform

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// NewSQSClient uses the SDK's standard environment: credentials and
// AWS_ENDPOINT_URL (LocalStack locally, unset in AWS).
func NewSQSClient(ctx context.Context, region string) (*sqs.Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, err
	}
	return sqs.NewFromConfig(cfg), nil
}

// SQSProbe checks that the queue exists and is reachable.
func SQSProbe(client *sqs.Client, queueURL string) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
			QueueUrl:       aws.String(queueURL),
			AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
		})
		return err
	}
}
