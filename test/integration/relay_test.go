//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

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

// unpublishedCount is unpublished's error-returning twin, safe to call off
// the test goroutine (t.Fatal must only run on the goroutine running the test).
func unpublishedCount(ctx context.Context, s stack, walletID string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE partition_key = $1 AND published_at IS NULL AND dead_at IS NULL`, walletID).Scan(&n)
	return n, err
}

// auditEvents drains the audit queue and returns, per message group, the
// event ids in delivery order.
func auditEvents(t *testing.T, groups map[string][]string, within time.Duration, done func() bool) {
	t.Helper()
	url, err := env.LocalStack.QueueURL(context.Background(), "wallet-events-audit.fifo")
	if err != nil {
		t.Fatal(err)
	}
	client, err := env.LocalStack.SQS(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
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
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
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
	g, gctx := errgroup.WithContext(context.Background())
	for _, r := range []*outbox.Relay{a, b} {
		g.Go(func() error {
			// t.Fatal must only run on the goroutine executing the test, so
			// this loop reports infrastructure errors instead of calling it;
			// the main goroutine re-checks the outcome after g.Wait().
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				n, err := unpublishedCount(gctx, s, w.ID)
				if err != nil {
					return err
				}
				if n == 0 {
					return nil
				}
				_, _ = r.Tick(gctx)
				time.Sleep(20 * time.Millisecond)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}
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

// TestClaimHeadsRechecksPublicationUnderConcurrentAck reproduces the
// EvalPlanQual race the recheck in ClaimHeads' `due` WHERE guards against:
// under READ COMMITTED, a row FOR UPDATE re-evaluates its WHERE clause
// against the latest committed version once the lock is acquired. If a
// concurrent relay Acks the head between this query's snapshot and its lock
// attempt, the row must not be reclaimed and requeued.
//
// It runs ClaimHeads' own query, composed from the exported HeadsWith and
// DueRecheck fragments (outbox_store.go) instead of a hand copy, with one
// test-only addition: a `slowed` CTE stage that pg_sleeps between the heads
// snapshot and the due lock, to widen that otherwise microsecond-scale race
// window deterministically. Production has no such sleep. Because the WHERE
// clause is the same Go constant the production query uses, a change that
// drops the recheck predicates there makes this test fail too (see the
// report for the final-fix-B proof: removing the predicates from DueRecheck
// made this test fail, and restoring them made it pass again).
func TestClaimHeadsRechecksPublicationUnderConcurrentAck(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	store := postgres.NewOutboxStore(s.pool)
	w := openWallet(t, s, "100.00")
	head := dbOrder(t, s, w.ID)[0]

	// Claim the head under a known claim id, with a lease that is already
	// expired, so it is otherwise eligible for a reclaim.
	claim := uuid.NewString()
	msgs, err := store.ClaimHeads(ctx, time.Now(), 1, claim, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].EventID != head {
		t.Fatalf("setup claim: msgs=%v err=%v", msgs, err)
	}
	target := msgs[0]

	type row struct {
		seq int64
		id  string
	}
	result := make(chan []row, 1)
	errc := make(chan error, 1)
	// slowClaimSQL is ClaimHeads' own query with one test-only stage spliced
	// in between the heads snapshot and the due lock: `slowed` pg_sleeps
	// before `due` runs, widening the reclaim race deterministically. The
	// CTE name `due` filters by (`slowed` instead of `heads`) is the only
	// difference from outbox_store.claimHeadsSQL; HeadsWith and DueRecheck
	// are the same Go constants the production query uses, so this cannot
	// drift from it.
	slowClaimSQL := postgres.HeadsWith + `, slowed AS (
			SELECT seq FROM heads, pg_sleep(0.5)
		), due AS (
			SELECT o.seq
			FROM outbox_events o
			WHERE o.seq = ANY (ARRAY(SELECT seq FROM slowed)) AND ` + postgres.DueRecheck + `
			ORDER BY o.seq
			LIMIT $2
			FOR UPDATE OF o SKIP LOCKED
		)
		UPDATE outbox_events o SET claim_id = $3, claim_expires_at = $4, attempts = o.attempts + 1
		FROM due WHERE o.seq = due.seq
		RETURNING o.seq, o.event_id::text`
	go func() {
		rows, err := s.pool.Query(context.Background(), slowClaimSQL, time.Now(), 50, uuid.NewString(), time.Now().Add(30*time.Second))
		if err != nil {
			errc <- err
			return
		}
		defer rows.Close()
		var got []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.seq, &r.id); err != nil {
				errc <- err
				return
			}
			got = append(got, r)
		}
		if err := rows.Err(); err != nil {
			errc <- err
			return
		}
		result <- got
	}()

	time.Sleep(150 * time.Millisecond) // land inside the delayed reclaim's pg_sleep window
	if applied, err := store.Ack(ctx, target.Seq, claim, time.Now()); err != nil || !applied {
		t.Fatalf("racing ack: applied=%v err=%v", applied, err)
	}

	select {
	case got := <-result:
		for _, r := range got {
			if r.id == head {
				t.Fatalf("reclaimed the already-acked head %s (seq %d): the due CTE's published_at/dead_at recheck did not exclude it under EvalPlanQual", head, r.seq)
			}
		}
	case err := <-errc:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the delayed reclaim query")
	}

	var published bool
	if err := s.pool.QueryRow(ctx, `SELECT published_at IS NOT NULL FROM outbox_events WHERE event_id = $1`, head).Scan(&published); err != nil {
		t.Fatal(err)
	}
	if !published {
		t.Fatal("head must remain published after the race")
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

// TestClaimHeadsStaysFastWithStaleStatistics reproduces the load-test stall:
// right after startup the planner's statistics describe an empty (or fully
// published) outbox, so it estimates one pending row and the old DISTINCT ON
// heads subquery ended up rescanned once per pending row, quadratic in the
// backlog (14.8 s for 6000 pending rows, SQLSTATE 57014 under the pool's
// statement_timeout). The claim must stay bounded by the number of
// partitions whatever the statistics say, and still return exactly the
// oldest pending event of each partition.
func TestClaimHeadsStaysFastWithStaleStatistics(t *testing.T) {
	pg := testDatabase(t)
	ctx := context.Background()
	super := connect(t, pg.SuperDSN)
	const partitions, perPartition = 50, 120
	if _, err := super.Exec(ctx, `ALTER TABLE outbox_events SET (autovacuum_enabled = false)`); err != nil {
		t.Fatal(err)
	}
	if _, err := super.Exec(ctx, `ANALYZE outbox_events`); err != nil {
		t.Fatal(err)
	}
	if _, err := super.Exec(ctx, `INSERT INTO outbox_events (event_id, partition_key, event_type, aggregate_id, payload, occurred_at, next_attempt_at)
		SELECT gen_random_uuid(), 'wallet-' || (i % $1::int), 'test', 'agg', '{}'::jsonb, now(), now()
		FROM generate_series(1, $1::int * $2::int) i`, partitions, perPartition); err != nil {
		t.Fatal(err)
	}

	pool, err := postgres.NewPool(ctx, postgres.PoolConfig{
		DSN: pg.AppDSN, MaxConns: 2, LockTimeout: time.Second, StatementTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	msgs, err := postgres.NewOutboxStore(pool).ClaimHeads(ctx, time.Now(), 100, uuid.NewString(), time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatalf("claim with stale statistics: %v", err)
	}
	want := map[int64]bool{}
	rows, err := super.Query(ctx, `SELECT min(seq) FROM outbox_events GROUP BY partition_key`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			t.Fatal(err)
		}
		want[seq] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != partitions {
		t.Fatalf("claimed %d events, want one head per partition (%d)", len(msgs), partitions)
	}
	for _, m := range msgs {
		if !want[m.Seq] {
			t.Fatalf("claimed seq %d of %s, which is not its partition's oldest pending event", m.Seq, m.PartitionKey)
		}
	}
}
