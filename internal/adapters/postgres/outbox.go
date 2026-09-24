package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/lavarini/backend-challenge-go/internal/events"
)

type outboxRepo struct {
	q pgx.Tx
}

// Append stores immutable event snapshots in the same transaction as the
// financial effect. Publication happens later, from the relay (ADR 0014).
func (r outboxRepo) Append(ctx context.Context, evs ...events.Envelope) error {
	for _, e := range evs {
		payload, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("outbox: marshal %s: %w", e.EventType(), err)
		}
		_, err = r.q.Exec(ctx, `INSERT INTO outbox_events (event_id, partition_key, event_type, aggregate_id, payload, occurred_at, next_attempt_at)
			VALUES ($1, $2, $3, $4, $5, $6, $6)`,
			e.EventID(), e.PartitionKey(), e.EventType(), e.AggregateID(), json.RawMessage(payload), e.OccurredAt())
		if err != nil {
			return classify(err)
		}
	}
	return nil
}
