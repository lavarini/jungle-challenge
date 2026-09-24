package postgres

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lavarini/backend-challenge-go/internal/adapters/outbox"
)

const maxErrorLength = 1000

// HeadsCTE selects the oldest unpublished, non-dead row of each partition:
// the candidate heads a claim may lease. Exported so tests can compose the
// exact production query around an injected delay instead of hand-copying
// it: a query built from these
// fragments can't silently drift from ClaimHeads.
const HeadsCTE = `SELECT DISTINCT ON (partition_key) seq
		FROM outbox_events
		WHERE published_at IS NULL AND dead_at IS NULL
		ORDER BY partition_key, seq`

// DueRecheck re-applies the published_at/dead_at/lease predicate to each
// snapshot head right before FOR UPDATE takes its lock. Under READ
// COMMITTED, a row FOR UPDATE re-evaluates its WHERE clause against the
// latest committed version once the lock is acquired (EvalPlanQual), so a
// row Acked or quarantined by a concurrent relay between the heads snapshot
// and this lock must be rejected here too, or it would be reclaimed after it
// was already settled. $1 is "now".
const DueRecheck = `o.published_at IS NULL AND o.dead_at IS NULL
		  AND o.next_attempt_at <= $1 AND (o.claim_expires_at IS NULL OR o.claim_expires_at < $1)`

// claimHeadsSQL is ClaimHeads' query, assembled from HeadsCTE and DueRecheck
// so both stay byte-identical to what a test composes from the same two
// exported fragments.
var claimHeadsSQL = `WITH heads AS (` + HeadsCTE + `), due AS (
		SELECT o.seq
		FROM outbox_events o JOIN heads h ON h.seq = o.seq
		WHERE ` + DueRecheck + `
		ORDER BY o.seq
		LIMIT $2
		FOR UPDATE OF o SKIP LOCKED
	)
	UPDATE outbox_events o SET claim_id = $3, claim_expires_at = $4, attempts = o.attempts + 1
	FROM due WHERE o.seq = due.seq
	RETURNING o.seq, o.event_id::text, o.partition_key, o.event_type, o.payload::text, o.attempts`

// OutboxStore runs each relay step in its own short statement; no transaction
// stays open while the broker is called (ADR 0014).
type OutboxStore struct {
	pool *pgxpool.Pool
}

func NewOutboxStore(pool *pgxpool.Pool) *OutboxStore { return &OutboxStore{pool: pool} }

// ClaimHeads leases the oldest unpublished event of each partition, when it
// is due and not leased by someone else. The published_at/dead_at check is
// repeated in `due`, not just in `heads`: under READ COMMITTED, a row FOR
// UPDATE re-evaluates its WHERE clause against the latest committed version
// once the lock is acquired (EvalPlanQual), so a row Acked or quarantined by
// a concurrent relay between the `heads` snapshot and the lock must be
// rejected there too, or it would be reclaimed after it was already settled.
func (s *OutboxStore) ClaimHeads(ctx context.Context, now time.Time, limit int, claimID string, leaseUntil time.Time) ([]outbox.Message, error) {
	rows, err := s.pool.Query(ctx, claimHeadsSQL, now, limit, claimID, leaseUntil)
	if err != nil {
		return nil, classify(err)
	}
	msgs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (outbox.Message, error) {
		var m outbox.Message
		var payload string
		err := row.Scan(&m.Seq, &m.EventID, &m.PartitionKey, &m.EventType, &payload, &m.Attempts)
		m.Payload = []byte(payload)
		return m, err
	})
	return msgs, classify(err)
}

func (s *OutboxStore) Ack(ctx context.Context, seq int64, claimID string, now time.Time) (bool, error) {
	return s.fenced(ctx, `UPDATE outbox_events SET published_at = $3, claim_id = NULL, claim_expires_at = NULL, last_error = NULL
		WHERE seq = $1 AND claim_id = $2 AND published_at IS NULL AND dead_at IS NULL`, seq, claimID, now)
}

func (s *OutboxStore) Retry(ctx context.Context, seq int64, claimID string, next time.Time, lastErr string) (bool, error) {
	return s.fenced(ctx, `UPDATE outbox_events SET next_attempt_at = $3, claim_id = NULL, claim_expires_at = NULL, last_error = $4
		WHERE seq = $1 AND claim_id = $2 AND published_at IS NULL AND dead_at IS NULL`, seq, claimID, next, truncate(lastErr))
}

func (s *OutboxStore) Dead(ctx context.Context, seq int64, claimID string, now time.Time, lastErr string) (bool, error) {
	return s.fenced(ctx, `UPDATE outbox_events SET dead_at = $3, claim_id = NULL, claim_expires_at = NULL, last_error = $4
		WHERE seq = $1 AND claim_id = $2 AND published_at IS NULL AND dead_at IS NULL`, seq, claimID, now, truncate(lastErr))
}

func (s *OutboxStore) fenced(ctx context.Context, sql string, args ...any) (bool, error) {
	tag, err := s.pool.Exec(ctx, sql, args...)
	if err != nil {
		return false, classify(err)
	}
	return tag.RowsAffected() == 1, nil
}

// truncate cuts s to at most maxErrorLength bytes, backing off to the
// nearest rune boundary so it never splits a multi-byte character.
func truncate(s string) string {
	if len(s) <= maxErrorLength {
		return s
	}
	cut := s[:maxErrorLength]
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size != 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return cut
}
