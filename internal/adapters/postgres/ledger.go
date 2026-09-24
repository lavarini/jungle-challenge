package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/money"
	"github.com/lavarini/backend-challenge-go/internal/wallet"
)

type ledgerRepo struct {
	q pgx.Tx
}

func (r ledgerRepo) Insert(ctx context.Context, e wallet.LedgerEntry) error {
	_, err := r.q.Exec(ctx, `INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount_minor,
		currency, balance_before, balance_after, wallet_version, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		e.ID(), e.WalletID(), e.TransactionID(), string(e.Direction()), e.Amount().Minor(),
		string(e.Amount().Currency()), e.BalanceBefore().Minor(), e.BalanceAfter().Minor(), e.WalletVersion(), e.CreatedAt())
	return classify(err)
}

func (r ledgerRepo) Page(ctx context.Context, walletID string, afterSeq int64, limit int) ([]app.LedgerRow, error) {
	rows, err := r.q.Query(ctx, `SELECT seq, id::text, wallet_id::text, transaction_id::text, direction, amount_minor, currency,
		balance_before, balance_after, wallet_version, created_at
		FROM wallet_ledger_entries WHERE wallet_id = $1 AND seq > $2 ORDER BY seq LIMIT $3`, walletID, afterSeq, limit)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	var out []app.LedgerRow
	for rows.Next() {
		var (
			row                   app.LedgerRow
			p                     wallet.LedgerEntryParams
			direction, currency   string
			amount, before, after int64
			createdAt             time.Time
		)
		if err := rows.Scan(&row.Seq, &p.ID, &p.WalletID, &p.TransactionID, &direction, &amount, &currency,
			&before, &after, &p.WalletVersion, &createdAt); err != nil {
			return nil, classify(err)
		}
		c, err := money.ParseCurrency(currency)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", app.ErrInvariantViolation, err)
		}
		p.Direction, p.CreatedAt = wallet.Direction(direction), createdAt
		p.Amount, _ = money.FromMinor(amount, c)
		p.BalanceBefore, _ = money.FromMinor(before, c)
		p.BalanceAfter, _ = money.FromMinor(after, c)
		if row.Entry, err = wallet.NewLedgerEntry(p); err != nil {
			return nil, fmt.Errorf("%w: %w", app.ErrInvariantViolation, err)
		}
		out = append(out, row)
	}
	return out, classify(rows.Err())
}
