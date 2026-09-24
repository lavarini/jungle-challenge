package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type StatsSnapshot struct {
	PendingReferences int64
	// PendingNearDeadline counts pending operations whose deadline is less
	// than the given window away.
	PendingNearDeadline int64
	OutboxPending       int64
	OutboxOldestMillis  int64
}

type Stats struct {
	pool *pgxpool.Pool
}

func NewStats(pool *pgxpool.Pool) *Stats { return &Stats{pool: pool} }

// Snapshot reads the backlog gauges. Every count runs on a partial index
// (wager_tx_pending_due, outbox_unpublished_by_partition), so its cost grows
// with the backlog, not with the history.
func (s *Stats) Snapshot(ctx context.Context, now time.Time, nearDeadline time.Duration) (StatsSnapshot, error) {
	var st StatsSnapshot
	err := s.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM wager_transactions WHERE status = 'PENDING_REFERENCE'),
		(SELECT count(*) FROM wager_transactions WHERE status = 'PENDING_REFERENCE' AND deadline_at <= $2),
		(SELECT count(*) FROM outbox_events WHERE published_at IS NULL AND dead_at IS NULL),
		COALESCE((SELECT (EXTRACT(EPOCH FROM ($1 - min(occurred_at))) * 1000)::bigint
		          FROM outbox_events WHERE published_at IS NULL AND dead_at IS NULL), 0)`, now, now.Add(nearDeadline)).
		Scan(&st.PendingReferences, &st.PendingNearDeadline, &st.OutboxPending, &st.OutboxOldestMillis)
	return st, classify(err)
}
