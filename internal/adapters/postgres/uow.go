// Package postgres implements the app ports with pgx and explicit SQL.
package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lavarini/backend-challenge-go/internal/app"
)

type UnitOfWork struct {
	pool *pgxpool.Pool
}

func NewUnitOfWork(pool *pgxpool.Pool) *UnitOfWork { return &UnitOfWork{pool: pool} }

// Do runs fn in one READ COMMITTED transaction. A failed commit is reported as
// transient: its outcome is unknown and a retry resolves through idempotency.
func (u *UnitOfWork) Do(ctx context.Context, fn func(ctx context.Context, tx app.Tx) error) (err error) {
	tx, err := u.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return classify(err)
	}
	defer func() {
		if err != nil {
			// Detached from ctx so the rollback runs even after cancellation.
			rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rbCtx)
		}
	}()
	if err = fn(ctx, pgTx{tx: tx}); err != nil {
		return classify(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return classify(err)
	}
	return nil
}

type pgTx struct {
	tx pgx.Tx
}

func (t pgTx) Wallets() app.WalletRepository           { return walletRepo{q: t.tx} }
func (t pgTx) Transactions() app.TransactionRepository { return transactionRepo{q: t.tx} }
func (t pgTx) Ledger() app.LedgerRepository            { return ledgerRepo{q: t.tx} }
func (t pgTx) Outbox() app.OutboxRepository            { return outboxRepo{q: t.tx} }
