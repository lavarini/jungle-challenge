//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/lavarini/backend-challenge-go/internal/adapters/postgres"
)

func connect(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("error = %v, want SQLSTATE %s", err, code)
	}
	if pgErr.Code != code {
		t.Fatalf("SQLSTATE = %s (%s), want %s", pgErr.Code, pgErr.Message, code)
	}
}

// seedWallet creates, as the runtime role, a wallet whose opening balance is
// proven by an OPENING transaction and its ledger entry.
func seedWallet(t *testing.T, conn *pgx.Conn, balance int64) (walletID, openingID string) {
	t.Helper()
	ctx := context.Background()
	walletID, openingID = uuid.NewString(), uuid.NewString()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
		VALUES ($1, $2, 'BRL', $3, 1, now(), now())`, walletID, uuid.NewString(), balance)
	if err != nil {
		t.Fatal(err)
	}
	if balance > 0 {
		_, err = tx.Exec(ctx, `INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, currency,
			amount_minor, correlation_id, result_balance_minor, result_wallet_version, created_at, updated_at, completed_at)
			SELECT $1, 'INTERNAL', 'OPENING', 'PROCESSED', id, player_id, currency, $2, 'seed', $2, 1, now(), now(), now()
			FROM wallets WHERE id = $3`, openingID, balance, walletID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount_minor,
			currency, balance_before, balance_after, wallet_version, created_at)
			VALUES ($1, $2, $3, 'CREDIT', $4, 'BRL', 0, $4, 1, now())`, uuid.NewString(), walletID, openingID, balance)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return walletID, openingID
}

// insertExternal inserts a PROCESSED external transaction row (no ledger).
func insertExternal(ctx context.Context, q interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, walletID, kind string, amount, resultBalance, resultVersion int64, referenceTxID any) (string, error) {
	id := uuid.NewString()
	var refExternal any
	if kind == "REFUND" || kind == "ROLLBACK" {
		refExternal = "ref-" + id
	}
	_, err := q.Exec(ctx, `INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, currency,
		amount_minor, provider_id, external_id, idempotency_key, payload_hash, round_id, game_id,
		reference_external_id, reference_tx_id, correlation_id, result_balance_minor, result_wallet_version,
		created_at, updated_at, completed_at)
		SELECT $1::uuid, 'EXTERNAL', $2, 'PROCESSED', id, player_id, currency, $3, 'provider-a', $1::text, $1::text, $4, 'r1', 'g1',
		       $5, $6, 'c1', $7, $8, now(), now(), now()
		FROM wallets WHERE id = $9`,
		id, kind, amount, bytes.Repeat([]byte{7}, 32), refExternal, referenceTxID, resultBalance, resultVersion, walletID)
	return id, err
}

func TestRuntimeRoleCannotRewriteLedger(t *testing.T) {
	app := connect(t, env.Postgres.AppDSN)
	ctx := context.Background()
	walletID, _ := seedWallet(t, app, 10000)

	_, err := app.Exec(ctx, `UPDATE wallet_ledger_entries SET amount_minor = 1 WHERE wallet_id = $1`, walletID)
	requireCode(t, err, "42501")
	_, err = app.Exec(ctx, `DELETE FROM wallet_ledger_entries WHERE wallet_id = $1`, walletID)
	requireCode(t, err, "42501")
	_, err = app.Exec(ctx, `TRUNCATE wallet_ledger_entries`)
	requireCode(t, err, "42501")
	_, err = app.Exec(ctx, `ALTER TABLE wallet_ledger_entries DISABLE TRIGGER ALL`)
	requireCode(t, err, "42501")
}

func TestLedgerIsAppendOnlyEvenForOwner(t *testing.T) {
	app := connect(t, env.Postgres.AppDSN)
	owner := connect(t, env.Postgres.MigratorDSN)
	ctx := context.Background()
	walletID, _ := seedWallet(t, app, 10000)

	_, err := owner.Exec(ctx, `UPDATE wallet_ledger_entries SET amount_minor = 1 WHERE wallet_id = $1`, walletID)
	requireCode(t, err, "23514")
	_, err = owner.Exec(ctx, `DELETE FROM wallet_ledger_entries WHERE wallet_id = $1`, walletID)
	requireCode(t, err, "23514")
	_, err = owner.Exec(ctx, `TRUNCATE wallet_ledger_entries CASCADE`)
	requireCode(t, err, "23514")
}

func TestBalanceCannotGoNegative(t *testing.T) {
	app := connect(t, env.Postgres.AppDSN)
	walletID, _ := seedWallet(t, app, 10000)
	_, err := app.Exec(context.Background(), `UPDATE wallets SET balance_minor = -1, version = 2 WHERE id = $1`, walletID)
	requireCode(t, err, "23514")
}

func TestBalanceChangeWithoutLedgerFailsAtCommit(t *testing.T) {
	app := connect(t, env.Postgres.AppDSN)
	ctx := context.Background()
	walletID, _ := seedWallet(t, app, 10000)

	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE wallets SET balance_minor = balance_minor + 1, version = version + 1 WHERE id = $1`, walletID); err != nil {
		t.Fatalf("update itself must succeed; the check is deferred: %v", err)
	}
	requireCode(t, tx.Commit(ctx), "23514")
}

func TestVersionMustAdvanceByExactlyOne(t *testing.T) {
	app := connect(t, env.Postgres.AppDSN)
	ctx := context.Background()
	walletID, _ := seedWallet(t, app, 10000)

	tx, _ := app.Begin(ctx)
	_, _ = tx.Exec(ctx, `UPDATE wallets SET version = version + 2 WHERE id = $1`, walletID)
	requireCode(t, tx.Commit(ctx), "23514")
}

func TestLedgerDirectionMustMatchKind(t *testing.T) {
	app := connect(t, env.Postgres.AppDSN)
	ctx := context.Background()
	walletID, _ := seedWallet(t, app, 10000)

	tx, _ := app.Begin(ctx)
	defer tx.Rollback(ctx)
	// A BET credited instead of debited: arithmetic and chain are consistent,
	// only the direction contradicts the kind.
	betID, err := insertExternal(ctx, tx, walletID, "BET", 3000, 13000, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE wallets SET balance_minor = 13000, version = 2 WHERE id = $1`, walletID); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount_minor,
		currency, balance_before, balance_after, wallet_version, created_at)
		VALUES ($1, $2, $3, 'CREDIT', 3000, 'BRL', 10000, 13000, 2, now())`, uuid.NewString(), walletID, betID)
	requireCode(t, err, "23514")
}

func TestLedgerMustMatchWalletState(t *testing.T) {
	app := connect(t, env.Postgres.AppDSN)
	ctx := context.Background()
	walletID, _ := seedWallet(t, app, 10000)

	tx, _ := app.Begin(ctx)
	defer tx.Rollback(ctx)
	betID, _ := insertExternal(ctx, tx, walletID, "BET", 3000, 7000, 2, nil)
	// Wallet not updated before the entry: the entry claims a state that does not exist.
	_, err := tx.Exec(ctx, `INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount_minor,
		currency, balance_before, balance_after, wallet_version, created_at)
		VALUES ($1, $2, $3, 'DEBIT', 3000, 'BRL', 10000, 7000, 2, now())`, uuid.NewString(), walletID, betID)
	requireCode(t, err, "23514")
}

func TestLedgerChainMustBeContinuous(t *testing.T) {
	app := connect(t, env.Postgres.AppDSN)
	ctx := context.Background()
	walletID, _ := seedWallet(t, app, 10000)

	tx, _ := app.Begin(ctx)
	defer tx.Rollback(ctx)
	betID, _ := insertExternal(ctx, tx, walletID, "BET", 3000, 6000, 2, nil)
	_, _ = tx.Exec(ctx, `UPDATE wallets SET balance_minor = 6000, version = 2 WHERE id = $1`, walletID)
	// balance_before 9000 does not continue from the previous balance_after 10000.
	_, err := tx.Exec(ctx, `INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount_minor,
		currency, balance_before, balance_after, wallet_version, created_at)
		VALUES ($1, $2, $3, 'DEBIT', 3000, 'BRL', 9000, 6000, 2, now())`, uuid.NewString(), walletID, betID)
	requireCode(t, err, "23514")
}

func TestTerminalTransactionIsImmutable(t *testing.T) {
	app := connect(t, env.Postgres.AppDSN)
	owner := connect(t, env.Postgres.MigratorDSN)
	ctx := context.Background()
	_, openingID := seedWallet(t, app, 10000)

	_, err := app.Exec(ctx, `UPDATE wager_transactions SET status = 'REJECTED', failure_code = 'X' WHERE id = $1`, openingID)
	requireCode(t, err, "23514")
	_, err = app.Exec(ctx, `UPDATE wager_transactions SET amount_minor = 1 WHERE id = $1`, openingID)
	requireCode(t, err, "42501")
	_, err = owner.Exec(ctx, `DELETE FROM wager_transactions WHERE id = $1`, openingID)
	requireCode(t, err, "23514")
}

func TestUniquenessConstraints(t *testing.T) {
	app := connect(t, env.Postgres.AppDSN)
	ctx := context.Background()
	walletID, _ := seedWallet(t, app, 10000)

	// Second OPENING for the same wallet.
	_, err := app.Exec(ctx, `INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, currency,
		amount_minor, correlation_id, result_balance_minor, result_wallet_version, created_at, updated_at, completed_at)
		SELECT $1, 'INTERNAL', 'OPENING', 'PROCESSED', id, player_id, currency, 1, 'c', 1, 1, now(), now(), now()
		FROM wallets WHERE id = $2`, uuid.NewString(), walletID)
	requireCode(t, err, "23505")

	// Single reversal slot: REFUND then ROLLBACK over the same BET.
	betID, err := insertExternal(ctx, app, walletID, "BET", 1000, 9000, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insertExternal(ctx, app, walletID, "REFUND", 1000, 10000, 3, betID); err != nil {
		t.Fatal(err)
	}
	_, err = insertExternal(ctx, app, walletID, "ROLLBACK", 1000, 9000, 4, betID)
	requireCode(t, err, "23505")
}

func TestOriginAndAmountShape(t *testing.T) {
	app := connect(t, env.Postgres.AppDSN)
	ctx := context.Background()
	walletID, _ := seedWallet(t, app, 10000)

	// INTERNAL row carrying provider data.
	_, err := app.Exec(ctx, `INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, currency,
		amount_minor, provider_id, correlation_id, result_balance_minor, result_wallet_version, created_at, updated_at, completed_at)
		SELECT $1, 'INTERNAL', 'OPENING', 'PROCESSED', id, player_id, currency, 1, 'provider-a', 'c', 1, 1, now(), now(), now()
		FROM wallets WHERE id = $2`, uuid.NewString(), walletID)
	requireCode(t, err, "23514")

	// LOSS with a non-zero amount.
	_, err = insertExternal(ctx, app, walletID, "LOSS", 100, 10000, 1, nil)
	requireCode(t, err, "23514")

	// BET with zero amount.
	_, err = insertExternal(ctx, app, walletID, "BET", 0, 10000, 1, nil)
	requireCode(t, err, "23514")
}

func TestOutboxSnapshotIsImmutable(t *testing.T) {
	app := connect(t, env.Postgres.AppDSN)
	owner := connect(t, env.Postgres.MigratorDSN)
	ctx := context.Background()
	eventID := uuid.NewString()
	_, err := app.Exec(ctx, `INSERT INTO outbox_events (event_id, partition_key, event_type, aggregate_id, payload, occurred_at, next_attempt_at)
		VALUES ($1, 'w', 'T', 'a', '{"a":1}', now(), now())`, eventID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.Exec(ctx, `UPDATE outbox_events SET payload = '{"a":2}' WHERE event_id = $1`, eventID)
	requireCode(t, err, "42501")
	_, err = owner.Exec(ctx, `UPDATE outbox_events SET payload = '{"a":2}' WHERE event_id = $1`, eventID)
	requireCode(t, err, "23514")
	if _, err := app.Exec(ctx, `UPDATE outbox_events SET published_at = now() WHERE event_id = $1`, eventID); err != nil {
		t.Fatalf("delivery columns must stay writable: %v", err)
	}
}

// Up, down and up again on a scratch database, so the shared one is untouched.
func TestMigrationsUpDownUp(t *testing.T) {
	ctx := context.Background()
	super := connect(t, env.Postgres.SuperDSN)
	name := "migtest_" + uuid.NewString()[:8]
	if _, err := super.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s OWNER wager_migrator`, name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := super.Exec(context.Background(), fmt.Sprintf(`DROP DATABASE %s WITH (FORCE)`, name)); err != nil {
			t.Logf("testenv: drop database %s: %v", name, err)
		}
	})

	dsn := fmt.Sprintf("postgres://wager_migrator:migrator-dev-only@%s:%s/%s?sslmode=disable", env.Postgres.Host, env.Postgres.Port, name)
	for _, dir := range []string{"up", "down", "up"} {
		if err := postgres.Migrate(dsn, dir); err != nil {
			t.Fatalf("migrate %s: %v", dir, err)
		}
	}
	conn := connect(t, dsn)
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'
		AND table_name IN ('wallets','wager_transactions','wallet_ledger_entries','inbox_messages','outbox_events')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("tables after up/down/up = %d, want 5", n)
	}
}
