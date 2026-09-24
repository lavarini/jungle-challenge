// Package sqsin consumes wager operations from SQS. Correctness comes from
// the database (inbox and idempotency); the queue is at-least-once transport.
package sqsin

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/platform/failpoint"
)

type API interface {
	ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, opts ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, in *sqs.DeleteMessageInput, opts ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(ctx context.Context, in *sqs.ChangeMessageVisibilityInput, opts ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
	SendMessage(ctx context.Context, in *sqs.SendMessageInput, opts ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
}

type Submitter interface {
	Execute(ctx context.Context, cmd app.SubmitCommand) (app.SubmitResult, error)
}

type Config struct {
	QueueURL      string
	DLQURL        string
	Senders       map[string]string // SenderId -> providerId (ADR 0016)
	MaxMessages   int32
	WaitSeconds   int32
	MaxVisibility time.Duration
	// MaxReceives caps how many times the consumer retries a transient
	// failure before dead-lettering the message itself, well before the
	// queue's native redrive (ADR 0013).
	MaxReceives int32
	// Observer receives delivery outcomes for metrics; nil disables it.
	Observer Observer
}

// Observer receives delivery outcomes for metrics. Arguments are stable codes,
// never message or wallet ids.
type Observer interface {
	DeadLettered(code string)
	Retried()
	DLQCopyFailed()
	DeleteFailed()
	VisibilityFailed()
}

type nopObserver struct{}

func (nopObserver) DeadLettered(string) {}
func (nopObserver) Retried()            {}
func (nopObserver) DLQCopyFailed()      {}
func (nopObserver) DeleteFailed()       {}
func (nopObserver) VisibilityFailed()   {}

type Consumer struct {
	api    API
	submit Submitter
	cfg    Config
	log    *slog.Logger
}

func New(api API, s Submitter, cfg Config, log *slog.Logger) *Consumer {
	if cfg.Observer == nil {
		cfg.Observer = nopObserver{}
	}
	return &Consumer{api: api, submit: s, cfg: cfg, log: log}
}

// Loop polls while run is alive; message handling uses work so a stop lets
// messages in flight finish until the deadline.
func (c *Consumer) Loop(run, work context.Context) {
	for run.Err() == nil {
		if err := c.Poll(run, work); err != nil && run.Err() == nil {
			c.log.WarnContext(work, "receive failed", "error", err.Error(), "class", "transient")
			select {
			case <-run.Done():
			case <-time.After(time.Second):
			}
		}
	}
}

// Poll receives one batch and handles it: groups in parallel, messages of a
// group in order.
func (c *Consumer) Poll(run, work context.Context) error {
	out, err := c.api.ReceiveMessage(run, &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(c.cfg.QueueURL), MaxNumberOfMessages: c.cfg.MaxMessages, WaitTimeSeconds: c.cfg.WaitSeconds,
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{
			types.MessageSystemAttributeNameSenderId,
			types.MessageSystemAttributeNameMessageGroupId,
			types.MessageSystemAttributeNameApproximateReceiveCount,
		},
	})
	if err != nil {
		if run.Err() != nil {
			return nil
		}
		return err
	}
	var order []string
	groups := map[string][]types.Message{}
	for _, m := range out.Messages {
		g := m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]
		if _, seen := groups[g]; !seen {
			order = append(order, g)
		}
		groups[g] = append(groups[g], m)
	}
	var wg sync.WaitGroup
	for _, g := range order {
		wg.Add(1)
		go func(msgs []types.Message) {
			defer wg.Done()
			for i, m := range msgs {
				if !c.handle(work, m) {
					c.release(msgs[i+1:])
					return
				}
			}
		}(groups[g])
	}
	wg.Wait()
	return nil
}

// handle returns false when the message stays in the queue for a retry.
func (c *Consumer) handle(ctx context.Context, m types.Message) bool {
	body := aws.ToString(m.Body)
	env, err := parseEnvelope(body)
	if err != nil {
		return c.deadLetter(m, "", "INVALID_MESSAGE", err)
	}
	log := c.log.With("messageId", env.MessageID, "providerId", env.Data.ProviderID, "walletId", env.Data.WalletID)
	sender := m.Attributes[string(types.MessageSystemAttributeNameSenderId)]
	if provider, ok := c.cfg.Senders[sender]; !ok || provider != env.Data.ProviderID {
		return c.deadLetter(m, env.MessageID, "PROVIDER_NOT_AUTHORIZED", errors.New("sender "+sender+" may not act for "+env.Data.ProviderID))
	}
	cmd, err := env.command(body)
	if err != nil {
		return c.deadLetter(m, env.MessageID, "INVALID_MESSAGE", err)
	}
	res, err := c.submit.Execute(ctx, cmd)
	if err == nil {
		log.InfoContext(ctx, "message handled", "transactionId", res.TransactionID, "outcome", string(res.Status), "replay", res.IdempotentReplay)
		failpoint.Hit("consumer.after_commit")
		c.delete(m)
		return true
	}
	code, permanent := permanentCode(err)
	if permanent {
		return c.deadLetter(m, env.MessageID, code, err)
	}
	// Shutdown cut the handling short (spec §4): the message never really
	// failed, so it goes back at once and does not spend its retry budget.
	// Any non-permanent failure once work is cancelled counts, not only one
	// carrying context.Canceled: a statement the server cancelled (57014)
	// or a connection closed under it reach here without it.
	if ctx.Err() != nil {
		log.InfoContext(context.WithoutCancel(ctx), "message interrupted by shutdown; released", "error", err.Error())
		c.setVisibility(m, 0)
		return false
	}
	// A transient failure that has already exhausted the consumer's own
	// retry budget is dead-lettered explicitly, with the last error as the
	// reason, instead of relying on the queue's native redrive (which has no
	// reason and, at a low threshold, would also catch messages released
	// behind a failing group head that never actually failed themselves).
	if receiveCount(m) >= c.cfg.MaxReceives {
		return c.deadLetter(m, env.MessageID, "RETRIES_EXHAUSTED", err)
	}
	log.WarnContext(ctx, "message will be retried", "error", err.Error(), "class", "transient")
	c.retryLater(m)
	return false
}

// FailureCodes lists every reason the consumer dead-letters with; metrics
// preset their series from it.
var FailureCodes = []string{
	"INVALID_MESSAGE", "PROVIDER_NOT_AUTHORIZED", "WALLET_NOT_FOUND", "WALLET_MISMATCH",
	"IDEMPOTENCY_PAYLOAD_MISMATCH", "IDEMPOTENCY_KEY_MISMATCH", "INBOX_PAYLOAD_MISMATCH",
	"INVARIANT_VIOLATION", "RETRIES_EXHAUSTED",
}

// permanentCode classifies errors that no retry can fix (ADR 0013).
func permanentCode(err error) (string, bool) {
	switch {
	case errors.Is(err, app.ErrWalletNotFound):
		return "WALLET_NOT_FOUND", true
	case errors.Is(err, app.ErrWalletMismatch):
		return "WALLET_MISMATCH", true
	case errors.Is(err, app.ErrIdempotencyPayloadMismatch):
		return "IDEMPOTENCY_PAYLOAD_MISMATCH", true
	case errors.Is(err, app.ErrIdempotencyKeyMismatch):
		return "IDEMPOTENCY_KEY_MISMATCH", true
	case errors.Is(err, app.ErrInboxPayloadMismatch):
		return "INBOX_PAYLOAD_MISMATCH", true
	case errors.Is(err, app.ErrInvalidInput):
		return "INVALID_MESSAGE", true
	case errors.Is(err, app.ErrInvariantViolation):
		return "INVARIANT_VIOLATION", true
	}
	return "", false
}

// deadLetter copies the message to the DLQ with its reason, then removes it
// from the input queue. If the copy fails, the message is retried instead.
// The dedup id is the SQS message's own MessageId, not the envelope's
// messageId: the latter is chosen by the producer, so a second legitimate
// dead letter carrying the same envelope messageId would otherwise be
// silently dropped by SQS within the FIFO dedup window.
func (c *Consumer) deadLetter(m types.Message, messageID, code string, cause error) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	group := m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]
	if group == "" {
		group = "unknown"
	}
	dedup := aws.ToString(m.MessageId)
	reason := truncate(cause.Error(), maxReasonLength)
	_, err := c.api.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: aws.String(c.cfg.DLQURL), MessageBody: m.Body,
		MessageGroupId: aws.String(group), MessageDeduplicationId: aws.String(dedup),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"failureCode": {DataType: aws.String("String"), StringValue: aws.String(code)},
			"reason":      {DataType: aws.String("String"), StringValue: aws.String(reason)},
		},
	})
	if err != nil {
		c.log.Error("dead-letter copy failed; message will be retried", "messageId", messageID, "error", err.Error())
		c.cfg.Observer.DLQCopyFailed()
		c.retryLater(m)
		return false
	}
	c.log.Warn("message dead-lettered", "messageId", messageID, "failureCode", code, "class", "permanent")
	c.cfg.Observer.DeadLettered(code)
	c.delete(m)
	return true
}

// maxReasonLength bounds the DLQ reason attribute.
const maxReasonLength = 256

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence: the
// reason can carry producer text, and SQS rejects invalid attribute strings.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size != 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return cut
}

// delete runs detached from the work context: after a commit the message must
// be removed even if shutdown began.
func (c *Consumer) delete(m types.Message) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.api.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(c.cfg.QueueURL), ReceiptHandle: m.ReceiptHandle}); err != nil {
		c.log.Warn("delete failed; redelivery will be answered by the inbox", "error", err.Error())
		c.cfg.Observer.DeleteFailed()
	}
}

// receiveCount reads ApproximateReceiveCount; unset or unparsable counts as 0.
func receiveCount(m types.Message) int32 {
	n, _ := strconv.Atoi(m.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
	return int32(n)
}

// retryLater hides the message for min(2^receiveCount s, MaxVisibility).
func (c *Consumer) retryLater(m types.Message) {
	n := receiveCount(m)
	delay := time.Second
	for i := int32(0); i < n && delay < c.cfg.MaxVisibility; i++ {
		delay *= 2
	}
	if delay > c.cfg.MaxVisibility {
		delay = c.cfg.MaxVisibility
	}
	c.cfg.Observer.Retried()
	c.setVisibility(m, int32(delay/time.Second))
}

// release returns the rest of a group to the queue immediately, keeping order.
func (c *Consumer) release(msgs []types.Message) {
	for _, m := range msgs {
		c.setVisibility(m, 0)
	}
}

func (c *Consumer) setVisibility(m types.Message, seconds int32) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.api.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl: aws.String(c.cfg.QueueURL), ReceiptHandle: m.ReceiptHandle, VisibilityTimeout: seconds,
	}); err != nil {
		c.log.Warn("change visibility failed", "error", err.Error())
		c.cfg.Observer.VisibilityFailed()
	}
}
