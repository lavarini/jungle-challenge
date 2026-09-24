package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/lavarini/backend-challenge-go/internal/app"
)

type inboxRepo struct {
	q pgx.Tx
}

func (r inboxRepo) Find(ctx context.Context, consumer, messageID string) (*app.InboxRecord, error) {
	var rec app.InboxRecord
	var txID *string
	err := r.q.QueryRow(ctx, `SELECT consumer_name, message_id, payload_hash, transaction_id::text, outcome, received_at, completed_at
		FROM inbox_messages WHERE consumer_name = $1 AND message_id = $2`, consumer, messageID).
		Scan(&rec.Consumer, &rec.MessageID, &rec.PayloadHash, &txID, &rec.Outcome, &rec.ReceivedAt, &rec.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, classify(err)
	}
	rec.TransactionID = deref(txID)
	return &rec, nil
}

func (r inboxRepo) Insert(ctx context.Context, rec app.InboxRecord) error {
	_, err := r.q.Exec(ctx, `INSERT INTO inbox_messages (consumer_name, message_id, payload_hash, transaction_id, outcome, received_at, completed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		rec.Consumer, rec.MessageID, rec.PayloadHash, nullString(rec.TransactionID), rec.Outcome, rec.ReceivedAt, rec.CompletedAt)
	return classify(err)
}
