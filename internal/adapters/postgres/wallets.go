package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/money"
	"github.com/lavarini/backend-challenge-go/internal/wallet"
)

const walletColumns = `id::text, player_id::text, currency, balance_minor, version, created_at, updated_at`

type walletRepo struct {
	q pgx.Tx
}

func (r walletRepo) Insert(ctx context.Context, w *wallet.Wallet) error {
	s := w.Snapshot()
	_, err := r.q.Exec(ctx, `INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		s.ID, s.PlayerID, string(s.Balance.Currency()), s.Balance.Minor(), s.Version, s.CreatedAt, s.UpdatedAt)
	return classify(err)
}

func (r walletRepo) Get(ctx context.Context, id string) (*wallet.Wallet, error) {
	return scanWallet(r.q.QueryRow(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1`, id))
}

// GetForUpdate locks the wallet row: the per-wallet serialization point.
func (r walletRepo) GetForUpdate(ctx context.Context, id string) (*wallet.Wallet, error) {
	return scanWallet(r.q.QueryRow(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1 FOR UPDATE`, id))
}

// UpdateBalance also predicates on the previous version as a second line of
// defense against lost updates; under the row lock it always matches.
func (r walletRepo) UpdateBalance(ctx context.Context, w *wallet.Wallet) error {
	s := w.Snapshot()
	tag, err := r.q.Exec(ctx, `UPDATE wallets SET balance_minor = $2, version = $3, updated_at = $4
		WHERE id = $1 AND version = $3 - 1`, s.ID, s.Balance.Minor(), s.Version, s.UpdatedAt)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: wallet %s changed concurrently", app.ErrInvariantViolation, s.ID)
	}
	return nil
}

func scanWallet(row pgx.Row) (*wallet.Wallet, error) {
	var s wallet.Snapshot
	var currency string
	var minor int64
	if err := row.Scan(&s.ID, &s.PlayerID, &currency, &minor, &s.Version, &s.CreatedAt, &s.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, app.ErrWalletNotFound
		}
		return nil, classify(err)
	}
	c, err := money.ParseCurrency(currency)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", app.ErrInvariantViolation, err)
	}
	if s.Balance, err = money.FromMinor(minor, c); err != nil {
		return nil, fmt.Errorf("%w: %w", app.ErrInvariantViolation, err)
	}
	w, err := wallet.Rehydrate(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", app.ErrInvariantViolation, err)
	}
	return w, nil
}
