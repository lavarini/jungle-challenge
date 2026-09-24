//go:build integration

package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"

	"github.com/lavarini/backend-challenge-go/test/testenv"
)

// SPIKE (ADR 0012): SNS FIFO fans out to a subscribed SQS FIFO queue with raw
// delivery, keeps message attributes and deduplicates by MessageDeduplicationId.
func TestSpikeSNSFIFOFanOutAndDeduplication(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	snsClient, err := env.LocalStack.SNS(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	topics, err := snsClient.ListTopics(ctx, &sns.ListTopicsInput{})
	if err != nil {
		t.Fatal(err)
	}
	var topicARN string
	for _, tp := range topics.Topics {
		if strings.HasSuffix(aws.ToString(tp.TopicArn), ":wallet-events.fifo") {
			topicARN = aws.ToString(tp.TopicArn)
		}
	}
	if topicARN == "" {
		t.Fatal("topic wallet-events.fifo not provisioned")
	}

	eventID := uuid.NewString()
	body := `{"eventId":"` + eventID + `"}`
	for i := 0; i < 2; i++ {
		_, err := snsClient.Publish(ctx, &sns.PublishInput{
			TopicArn:               aws.String(topicARN),
			Message:                aws.String(body),
			MessageGroupId:         aws.String("wallet-" + eventID),
			MessageDeduplicationId: aws.String(eventID),
			MessageAttributes: map[string]snstypes.MessageAttributeValue{
				"eventType": {DataType: aws.String("String"), StringValue: aws.String("WalletBalanceChanged")},
			},
		})
		if err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	sqsClient, err := env.LocalStack.SQS(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	auditURL, err := env.LocalStack.QueueURL(ctx, "wallet-events-audit.fifo")
	if err != nil {
		t.Fatal(err)
	}
	var received []sqstypes.Message
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		out, err := sqsClient.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:              aws.String(auditURL),
			MaxNumberOfMessages:   10,
			WaitTimeSeconds:       2,
			MessageAttributeNames: []string{"All"},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range out.Messages {
			if aws.ToString(m.Body) == body {
				received = append(received, m)
			}
			_, _ = sqsClient.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(auditURL), ReceiptHandle: m.ReceiptHandle})
		}
	}
	if len(received) != 1 {
		t.Fatalf("received %d copies, want exactly 1 (raw delivery + dedup)", len(received))
	}
	attr, ok := received[0].MessageAttributes["eventType"]
	if !ok || aws.ToString(attr.StringValue) != "WalletBalanceChanged" {
		t.Fatalf("eventType attribute missing on delivered message: %+v", received[0].MessageAttributes)
	}
}

// SPIKE (ADR 0016): SenderId reflects the sender's credential, so the consumer
// can bind a broker identity to a provider.
func TestSpikeSQSSenderIDReflectsCredential(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	queueURL, err := env.LocalStack.QueueURL(ctx, "wager-transactions.fifo")
	if err != nil {
		t.Fatal(err)
	}
	marker := uuid.NewString()
	for _, key := range []string{"111111111111", "222222222222"} {
		client, err := env.LocalStack.SQS(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.SendMessage(ctx, &sqs.SendMessageInput{
			QueueUrl:               aws.String(queueURL),
			MessageBody:            aws.String(marker + "|" + key),
			MessageGroupId:         aws.String("spike-" + key),
			MessageDeduplicationId: aws.String(marker + key),
		})
		if err != nil {
			t.Fatalf("send with %s: %v", key, err)
		}
	}

	reader, err := env.LocalStack.SQS(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	senders := map[string]string{}
	deadline := time.Now().Add(15 * time.Second)
	for len(senders) < 2 && time.Now().Before(deadline) {
		out, err := reader.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:                    aws.String(queueURL),
			MaxNumberOfMessages:         10,
			WaitTimeSeconds:             2,
			MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{sqstypes.MessageSystemAttributeNameSenderId},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range out.Messages {
			body := aws.ToString(m.Body)
			if strings.HasPrefix(body, marker+"|") {
				senders[strings.TrimPrefix(body, marker+"|")] = m.Attributes[string(sqstypes.MessageSystemAttributeNameSenderId)]
			}
			_, _ = reader.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(queueURL), ReceiptHandle: m.ReceiptHandle})
		}
	}
	t.Logf("SenderId by access key: %v", senders)
	if len(senders) != 2 {
		t.Fatalf("received %d of 2 messages", len(senders))
	}
	if senders["111111111111"] == "" || senders["111111111111"] == senders["222222222222"] {
		t.Fatalf("SenderId does not distinguish credentials: %v", senders)
	}
}

// SPIKE (ADR 0015): Keycloak issues tokens with audience, roles and provider_id.
func TestSpikeKeycloakTokenClaims(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cases := []struct {
		client, secret, provider, role string
	}{
		{testenv.ProviderAID, testenv.ProviderASecret, "provider-a", "wager:provider"},
		{testenv.ProviderBID, testenv.ProviderBSecret, "provider-b", "wager:provider"},
		{testenv.InternalID, testenv.InternalSecret, "", "wallet:internal"},
	}
	for _, c := range cases {
		token, err := env.Keycloak.Token(ctx, c.client, c.secret)
		if err != nil {
			t.Fatalf("%s: %v", c.client, err)
		}
		claims := decodeClaims(t, token)
		if claims.Issuer != env.Keycloak.IssuerURL {
			t.Errorf("%s: iss %q, want %q", c.client, claims.Issuer, env.Keycloak.IssuerURL)
		}
		if !slices.Contains(claims.Audience(), "wagering-api") {
			t.Errorf("%s: aud %v lacks wagering-api", c.client, claims.Audience())
		}
		if claims.ProviderID != c.provider {
			t.Errorf("%s: provider_id %q, want %q", c.client, claims.ProviderID, c.provider)
		}
		if !slices.Contains(claims.RealmAccess.Roles, c.role) {
			t.Errorf("%s: roles %v lack %s", c.client, claims.RealmAccess.Roles, c.role)
		}
	}
}

type tokenClaims struct {
	Issuer      string          `json:"iss"`
	RawAudience json.RawMessage `json:"aud"`
	ProviderID  string          `json:"provider_id"`
	RealmAccess struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

func (c tokenClaims) Audience() []string {
	var many []string
	if json.Unmarshal(c.RawAudience, &many) == nil {
		return many
	}
	var one string
	_ = json.Unmarshal(c.RawAudience, &one)
	return []string{one}
}

// decodeClaims reads the payload without verifying it; verification is
// covered by the oidc adapter tests.
func decodeClaims(t *testing.T, token string) tokenClaims {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("malformed token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var c tokenClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		t.Fatal(err)
	}
	return c
}
