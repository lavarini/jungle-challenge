//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"

	"github.com/lavarini/backend-challenge-go/internal/adapters/outbox"
	"github.com/lavarini/backend-challenge-go/internal/adapters/postgres"
	"github.com/lavarini/backend-challenge-go/internal/adapters/snsout"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

func topicPublisher(t *testing.T) outbox.Publisher {
	t.Helper()
	client, err := env.LocalStack.SNS(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	topics, err := client.ListTopics(context.Background(), &sns.ListTopicsInput{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tp := range topics.Topics {
		if arn := aws.ToString(tp.TopicArn); strings.HasSuffix(arn, ":wallet-events.fifo") {
			return snsout.New(client, arn)
		}
	}
	t.Fatal("topic wallet-events.fifo not provisioned")
	return nil
}

func newRelay(s stack, pub outbox.Publisher, lease time.Duration, maxPermanent int) *outbox.Relay {
	return outbox.New(postgres.NewOutboxStore(s.pool), pub, outbox.Config{
		Interval: 20 * time.Millisecond, Lease: lease, Batch: 50, MaxPermanentAttempts: maxPermanent,
		InitialBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond,
	}, time.Now, uuid.NewString, quietLog)
}

func unpublished(t *testing.T, s stack, walletID string) int {
	return count(t, s.pool, `SELECT count(*) FROM outbox_events WHERE partition_key = $1 AND published_at IS NULL AND dead_at IS NULL`, walletID)
}

// auditEvents drains the audit queue and returns, per message group, the
// event ids in delivery order.
func auditEvents(t *testing.T, groups map[string][]string, within time.Duration, done func() bool) {
	t.Helper()
	url, err := env.LocalStack.QueueURL(context.Background(), "wallet-events-audit.fifo")
	if err != nil {
		t.Fatal(err)
	}
	client, _ := env.LocalStack.SQS(context.Background(), "test")
	deadline := time.Now().Add(within)
	for !done() && time.Now().Before(deadline) {
		out, err := client.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(url), MaxNumberOfMessages: 10, WaitTimeSeconds: 1,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameMessageGroupId},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range out.Messages {
			g := m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]
			groups[g] = append(groups[g], eventIDOf(aws.ToString(m.Body)))
			_, _ = client.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{QueueUrl: aws.String(url), ReceiptHandle: m.ReceiptHandle})
		}
	}
}

func eventIDOf(body string) string {
	var e struct {
		EventID string `json:"eventId"`
	}
	_ = json.Unmarshal([]byte(body), &e)
	return e.EventID
}

func dbOrder(t *testing.T, s stack, walletID string) []string {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT event_id::text FROM outbox_events WHERE partition_key = $1 ORDER BY seq`, walletID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids
}

func TestTwoRelaysPublishEachWalletInCommitOrder(t *testing.T) {
	s := newStack(t)
	pub := topicPublisher(t)
	w := openWallet(t, s, "100.00")
	for i := 0; i < 3; i++ {
		submit(t, s, command(t, w, wagering.Bet, "1.00", uuid.NewString()))
	}
	want := dbOrder(t, s, w.ID)

	a, b := newRelay(s, pub, 5*time.Second, 5), newRelay(s, pub, 5*time.Second, 5)
	var wg sync.WaitGroup
	for _, r := range []*outbox.Relay{a, b} {
		wg.Add(1)
		go func(r *outbox.Relay) {
			defer wg.Done()
			deadline := time.Now().Add(20 * time.Second)
			for unpublished(t, s, w.ID) > 0 && time.Now().Before(deadline) {
				_, _ = r.Tick(context.Background())
			}
		}(r)
	}
	wg.Wait()
	if n := unpublished(t, s, w.ID); n != 0 {
		t.Fatalf("%d events left unpublished", n)
	}

	groups := map[string][]string{}
	auditEvents(t, groups, 20*time.Second, func() bool { return len(groups[w.ID]) >= len(want) })
	got := groups[w.ID]
	if len(got) != len(want) {
		t.Fatalf("delivered %d events, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delivery order %v, want commit order %v", got, want)
		}
	}
}

// Crash between publish and ack: the lease expires, another relay republishes
// with the same eventId; the stale relay's ack is fenced out.
func TestExpiredClaimIsRepublishedWithTheSameEventID(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	pub := topicPublisher(t)
	store := postgres.NewOutboxStore(s.pool)
	w := openWallet(t, s, "100.00")
	head := dbOrder(t, s, w.ID)[0]

	var claimed outbox.Message
	staleClaim := uuid.NewString()
	deadline := time.Now().Add(10 * time.Second)
	for claimed.EventID != head && time.Now().Before(deadline) {
		msgs, err := store.ClaimHeads(ctx, time.Now(), 1000, staleClaim, time.Now().Add(300*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range msgs {
			if m.EventID == head {
				claimed = m
			}
		}
	}
	if claimed.EventID != head {
		t.Fatal("could not claim the wallet's head event")
	}
	if err := pub.Publish(ctx, claimed); err != nil { // published, then "crashed" before ack
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)

	r := newRelay(s, pub, 5*time.Second, 5)
	deadline = time.Now().Add(10 * time.Second)
	for count(t, s.pool, `SELECT count(*) FROM outbox_events WHERE event_id = $1 AND published_at IS NOT NULL`, head) == 0 && time.Now().Before(deadline) {
		_, _ = r.Tick(ctx)
	}
	var attempts int
	if err := s.pool.QueryRow(ctx, `SELECT attempts FROM outbox_events WHERE event_id = $1`, head).Scan(&attempts); err != nil || attempts < 2 {
		t.Fatalf("attempts %d (%v): the event must have been claimed again", attempts, err)
	}
	if applied, err := store.Ack(ctx, claimed.Seq, staleClaim, time.Now()); err != nil || applied {
		t.Fatalf("stale ack applied=%v err=%v; fencing failed", applied, err)
	}

	groups := map[string][]string{}
	auditEvents(t, groups, 5*time.Second, func() bool { return false })
	copies := 0
	for _, id := range groups[w.ID] {
		if id == head {
			copies++
		}
	}
	if copies != 1 {
		t.Fatalf("audit queue received %d copies of %s; SNS FIFO deduplication by eventId expected 1", copies, head)
	}
}

type failingFor struct {
	next    outbox.Publisher
	eventID string
}

func (f failingFor) Publish(ctx context.Context, m outbox.Message) error {
	if m.EventID == f.eventID {
		return errors.Join(outbox.ErrPermanent, errors.New("payload rejected"))
	}
	return f.next.Publish(ctx, m)
}

func TestPermanentFailureQuarantinesAndUnblocksThePartition(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	w := openWallet(t, s, "100.00")
	order := dbOrder(t, s, w.ID)
	r := newRelay(s, failingFor{next: topicPublisher(t), eventID: order[0]}, 5*time.Second, 2)

	deadline := time.Now().Add(15 * time.Second)
	for unpublished(t, s, w.ID) > 0 && time.Now().Before(deadline) {
		_, _ = r.Tick(ctx)
		time.Sleep(30 * time.Millisecond)
	}
	var dead bool
	var lastErr string
	if err := s.pool.QueryRow(ctx, `SELECT dead_at IS NOT NULL, coalesce(last_error, '') FROM outbox_events WHERE event_id = $1`, order[0]).Scan(&dead, &lastErr); err != nil {
		t.Fatal(err)
	}
	if !dead || lastErr == "" {
		t.Fatalf("head dead=%v last_error=%q", dead, lastErr)
	}
	if n := count(t, s.pool, `SELECT count(*) FROM outbox_events WHERE event_id = $1 AND published_at IS NOT NULL`, order[1]); n != 1 {
		t.Fatal("the next event of the partition must be published after the quarantine")
	}
}
