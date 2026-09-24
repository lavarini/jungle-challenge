package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/money"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

const transactionColumns = `id::text, origin, kind, status, wallet_id::text, player_id::text, currency, amount_minor,
	provider_id, external_id, idempotency_key, payload_hash, round_id, game_id, reference_external_id,
	reference_tx_id::text, failure_code, result_balance_minor, result_wallet_version, correlation_id,
	attempts, next_attempt_at, deadline_at, created_at, updated_at, completed_at`

type transactionRepo struct {
	q pgx.Tx
}

func (r transactionRepo) Insert(ctx context.Context, t *wagering.Transaction) error {
	s := t.Snapshot()
	var resultBalance, resultVersion any
	if s.ResultWalletVersion > 0 {
		resultBalance, resultVersion = s.ResultBalance.Minor(), s.ResultWalletVersion
	}
	_, err := r.q.Exec(ctx, `INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, currency,
		amount_minor, provider_id, external_id, idempotency_key, payload_hash, round_id, game_id, reference_external_id,
		reference_tx_id, failure_code, result_balance_minor, result_wallet_version, correlation_id, attempts,
		next_attempt_at, deadline_at, created_at, updated_at, completed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26)`,
		s.ID, string(s.Origin), string(s.Kind), string(s.Status), s.WalletID, s.PlayerID, string(s.Amount.Currency()), s.Amount.Minor(),
		nullString(s.Provider.ProviderID), nullString(s.Provider.ExternalID), nullString(s.Provider.IdempotencyKey),
		nullBytes(s.Provider.PayloadHash), nullString(s.Provider.RoundID), nullString(s.Provider.GameID),
		nullString(s.Provider.ReferenceExternalID), nullString(s.ReferenceTxID), nullString(string(s.FailureCode)),
		resultBalance, resultVersion, s.CorrelationID, s.Attempts, nullTime(s.NextAttemptAt), nullTime(s.DeadlineAt),
		s.CreatedAt, s.UpdatedAt, nullTime(s.CompletedAt))
	return classify(err)
}

func (r transactionRepo) FindByIdempotencyKey(ctx context.Context, providerID, key string) (*wagering.Transaction, error) {
	return scanTransaction(r.q.QueryRow(ctx, `SELECT `+transactionColumns+` FROM wager_transactions
		WHERE origin = 'EXTERNAL' AND provider_id = $1 AND idempotency_key = $2`, providerID, key))
}

func (r transactionRepo) FindByExternalID(ctx context.Context, providerID, externalID string) (*wagering.Transaction, error) {
	return scanTransaction(r.q.QueryRow(ctx, `SELECT `+transactionColumns+` FROM wager_transactions
		WHERE origin = 'EXTERNAL' AND provider_id = $1 AND external_id = $2`, providerID, externalID))
}

// scanTransaction returns (nil, nil) when the row does not exist.
func scanTransaction(row pgx.Row) (*wagering.Transaction, error) {
	var (
		s                                                             wagering.Snapshot
		origin, kind, status, currency                                string
		amount                                                        int64
		providerID, externalID, key, roundID, gameID, refExt, refTxID *string
		failure                                                       *string
		hash                                                          []byte
		resultBalance, resultVersion                                  *int64
		nextAttempt, deadline, completed                              *time.Time
	)
	err := row.Scan(&s.ID, &origin, &kind, &status, &s.WalletID, &s.PlayerID, &currency, &amount,
		&providerID, &externalID, &key, &hash, &roundID, &gameID, &refExt,
		&refTxID, &failure, &resultBalance, &resultVersion, &s.CorrelationID,
		&s.Attempts, &nextAttempt, &deadline, &s.CreatedAt, &s.UpdatedAt, &completed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, classify(err)
	}
	c, err := money.ParseCurrency(currency)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", app.ErrInvariantViolation, err)
	}
	if s.Amount, err = money.FromMinor(amount, c); err != nil {
		return nil, fmt.Errorf("%w: %w", app.ErrInvariantViolation, err)
	}
	if resultBalance != nil {
		if s.ResultBalance, err = money.FromMinor(*resultBalance, c); err != nil {
			return nil, fmt.Errorf("%w: %w", app.ErrInvariantViolation, err)
		}
	}
	if resultVersion != nil {
		s.ResultWalletVersion = *resultVersion
	}
	s.Origin, s.Kind, s.Status = wagering.Origin(origin), wagering.Kind(kind), wagering.Status(status)
	s.Provider = wagering.ProviderRef{
		ProviderID: deref(providerID), ExternalID: deref(externalID), IdempotencyKey: deref(key),
		PayloadHash: hash, RoundID: deref(roundID), GameID: deref(gameID), ReferenceExternalID: deref(refExt),
	}
	s.ReferenceTxID, s.FailureCode = deref(refTxID), wagering.FailureCode(deref(failure))
	s.NextAttemptAt, s.DeadlineAt, s.CompletedAt = derefTime(nextAttempt), derefTime(deadline), derefTime(completed)
	t, err := wagering.Rehydrate(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", app.ErrInvariantViolation, err)
	}
	return t, nil
}
