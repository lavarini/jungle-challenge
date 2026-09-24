package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

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
