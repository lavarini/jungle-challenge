package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/money"
)

type Reconciler struct {
	pool *pgxpool.Pool
}

func NewReconciler(pool *pgxpool.Pool) *Reconciler { return &Reconciler{pool: pool} }

// Totals reads the wallet and the ledger from one REPEATABLE READ, read-only
// snapshot: the arithmetic sum, the chain (last entry's balance_after), the
// wallet version and the currency of every entry, all against the same view.
func (r *Reconciler) Totals(ctx context.Context, walletID string) (app.ReconciliationTotals, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return app.ReconciliationTotals{}, classify(err)
	}
	defer func() {
		rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rbCtx)
	}()
	var balance, version int64
	var currency string
	err = tx.QueryRow(ctx, `SELECT balance_minor, currency, version FROM wallets WHERE id = $1`, walletID).Scan(&balance, &currency, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ReconciliationTotals{}, app.ErrWalletNotFound
	}
	if err != nil {
		return app.ReconciliationTotals{}, classify(err)
	}
	var sum string
	var entries int
	var maxVersion, lastBalanceAfter int64
	var currencyMismatches int
	err = tx.QueryRow(ctx, `SELECT
			COALESCE(SUM(CASE direction WHEN 'CREDIT' THEN amount_minor::numeric ELSE -amount_minor::numeric END), 0)::text,
			count(*),
			COALESCE(max(wallet_version), 0),
			COALESCE((SELECT balance_after FROM wallet_ledger_entries WHERE wallet_id = $1 ORDER BY wallet_version DESC LIMIT 1), 0),
			count(*) FILTER (WHERE currency <> $2)
		FROM wallet_ledger_entries WHERE wallet_id = $1`, walletID, currency).
		Scan(&sum, &entries, &maxVersion, &lastBalanceAfter, &currencyMismatches)
	if err != nil {
		return app.ReconciliationTotals{}, classify(err)
	}
	calculated, err := strconv.ParseInt(sum, 10, 64)
	if err != nil {
		return app.ReconciliationTotals{}, fmt.Errorf("%w: ledger total %s does not fit int64", app.ErrInvariantViolation, sum)
	}
	c, err := money.ParseCurrency(currency)
	if err != nil {
		return app.ReconciliationTotals{}, fmt.Errorf("%w: %w", app.ErrInvariantViolation, err)
	}
	stored, err := money.FromMinor(balance, c)
	if err != nil {
		return app.ReconciliationTotals{}, fmt.Errorf("%w: %w", app.ErrInvariantViolation, err)
	}
	// LastBalanceAfter is compared to Stored (same currency c) purely to
	// detect a chain break; a currency-per-entry mismatch is reported
	// separately via CurrencyMismatches.
	lastAfter, err := money.FromMinor(lastBalanceAfter, c)
	if err != nil {
		return app.ReconciliationTotals{}, fmt.Errorf("%w: %w", app.ErrInvariantViolation, err)
	}
	return app.ReconciliationTotals{
		Stored: stored, CreditsMinusDebits: calculated, Entries: entries,
		WalletVersion: version, MaxLedgerVersion: maxVersion, LastBalanceAfter: lastAfter,
		CurrencyMismatches: currencyMismatches,
	}, nil
}
