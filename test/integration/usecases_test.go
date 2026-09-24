//go:build integration

package integration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lavarini/backend-challenge-go/internal/adapters/postgres"
	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/money"
	"github.com/lavarini/backend-challenge-go/internal/platform"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
	"github.com/lavarini/backend-challenge-go/test/testenv"
)

type stack struct {
	pool   *pgxpool.Pool
	open   *app.OpenWallet
	get    *app.GetWallet
	submit *app.SubmitWager
}

// testDBs memoizes the per-test database keyed by *testing.T, so every call
// to newStack(t) or testDatabase(t) within the same test shares the one
// database cloned for it (like several processes talking to the same
// database), while different tests never see each other's rows. A subtest
// (t.Run) has its own *testing.T and so gets its own clone.
var testDBs sync.Map // map[*testing.T]testenv.Postgres

// testDatabase returns the Postgres DSNs of the database isolated to t,
// cloning it from the migrated template on first use.
func testDatabase(t *testing.T) testenv.Postgres {
	t.Helper()
	if v, ok := testDBs.Load(t); ok {
		return v.(testenv.Postgres)
	}
	pg := env.NewDatabase(context.Background(), t)
	testDBs.Store(t, pg)
	t.Cleanup(func() { testDBs.Delete(t) })
	return pg
}

// newStack builds an independent pool, like a separate process would, bound
// to this test's own database.
func newStack(t *testing.T) stack {
	t.Helper()
	pg := testDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := postgres.NewPool(ctx, postgres.PoolConfig{
		DSN: pg.AppDSN, MaxConns: 20, LockTimeout: 2 * time.Second, StatementTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	uow := postgres.NewUnitOfWork(pool)
	clock, ids := platform.NewSystemClock(), platform.NewUUIDv7()
	return stack{pool: pool, open: app.NewOpenWallet(uow, clock, ids), get: app.NewGetWallet(uow), submit: app.NewSubmitWager(uow, clock, ids, app.DefaultReferencePolicy())}
}

func mustBRL(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func openWallet(t *testing.T, s stack, balance string) app.WalletView {
	t.Helper()
	v, err := s.open.Execute(context.Background(), app.OpenWalletCommand{
		PlayerID: uuid.NewString(), InitialBalance: mustBRL(t, balance), CorrelationID: uuid.NewString(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func command(t *testing.T, w app.WalletView, kind wagering.Kind, amount, externalID string) app.SubmitCommand {
	return app.SubmitCommand{
		ProviderID: "provider-a", ExternalTransactionID: externalID, IdempotencyKey: "provider-a:" + externalID,
		PlayerID: w.PlayerID, WalletID: w.ID, RoundID: "round-1", GameID: "game-1", Kind: kind,
		Money: mustBRL(t, amount), CorrelationID: uuid.NewString(), Source: app.SourceHTTP,
	}
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// assertReconciled checks stored balance = credits - debits from the ledger.
func assertReconciled(t *testing.T, pool *pgxpool.Pool, walletID string) {
	t.Helper()
	var stored, computed int64
	err := pool.QueryRow(context.Background(), `SELECT w.balance_minor,
		COALESCE(SUM(CASE e.direction WHEN 'CREDIT' THEN e.amount_minor ELSE -e.amount_minor END), 0)::bigint
		FROM wallets w LEFT JOIN wallet_ledger_entries e ON e.wallet_id = w.id
		WHERE w.id = $1 GROUP BY w.balance_minor`, walletID).Scan(&stored, &computed)
	if err != nil {
		t.Fatal(err)
	}
	if stored != computed {
		t.Fatalf("wallet %s: stored %d, ledger %d", walletID, stored, computed)
	}
}

func TestOpenWalletCommitsOpeningLedgerAndEvents(t *testing.T) {
	s := newStack(t)
	v := openWallet(t, s, "1000.00")
	if v.Version != 1 || v.Balance.String() != "1000.00" {
		t.Fatalf("view %+v", v)
	}
	if n := count(t, s.pool, `SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND kind = 'OPENING' AND status = 'PROCESSED'`, v.ID); n != 1 {
		t.Fatalf("opening transactions = %d", n)
	}
	if n := count(t, s.pool, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'CREDIT'`, v.ID); n != 1 {
		t.Fatalf("opening entries = %d", n)
	}
	if n := count(t, s.pool, `SELECT count(*) FROM outbox_events WHERE partition_key = $1
		AND event_type IN ('WagerTransactionProcessed', 'WalletBalanceChanged')`, v.ID); n != 2 {
		t.Fatalf("outbox events = %d", n)
	}
	assertReconciled(t, s.pool, v.ID)
}

func TestOpenWalletWithZeroBalanceHasNoFinancialRecords(t *testing.T) {
	s := newStack(t)
	v := openWallet(t, s, "0.00")
	for _, table := range []string{"wager_transactions", "wallet_ledger_entries"} {
		if n := count(t, s.pool, `SELECT count(*) FROM `+table+` WHERE wallet_id = $1`, v.ID); n != 0 {
			t.Fatalf("%s rows = %d", table, n)
		}
	}
	if n := count(t, s.pool, `SELECT count(*) FROM outbox_events WHERE partition_key = $1`, v.ID); n != 0 {
		t.Fatalf("outbox rows = %d", n)
	}
}

func TestOpenWalletTwiceConflicts(t *testing.T) {
	s := newStack(t)
	player := uuid.NewString()
	cmd := app.OpenWalletCommand{PlayerID: player, InitialBalance: mustBRL(t, "10.00"), CorrelationID: "c"}
	if _, err := s.open.Execute(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	if _, err := s.open.Execute(context.Background(), cmd); !errors.Is(err, app.ErrWalletExists) {
		t.Fatalf("error = %v, want ErrWalletExists", err)
	}
}

func TestBetDebitsAndReplayReturnsOriginalBalance(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	v := openWallet(t, s, "1000.00")
	bet := command(t, v, wagering.Bet, "25.00", uuid.NewString())

	first, err := s.submit.Execute(ctx, bet)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != wagering.Processed || first.Balance.String() != "975.00" || first.IdempotentReplay {
		t.Fatalf("first %+v", first)
	}
	if _, err := s.submit.Execute(ctx, command(t, v, wagering.Win, "100.00", uuid.NewString())); err != nil {
		t.Fatal(err)
	}
	replay, err := s.submit.Execute(ctx, bet)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.IdempotentReplay || replay.TransactionID != first.TransactionID || replay.Balance.String() != "975.00" {
		t.Fatalf("replay must return the balance observed originally: %+v", replay)
	}
	if n := count(t, s.pool, `SELECT count(*) FROM wallet_ledger_entries WHERE transaction_id = $1`, first.TransactionID); n != 1 {
		t.Fatalf("entries for bet = %d", n)
	}
	assertReconciled(t, s.pool, v.ID)
}

func TestIdempotencyConflicts(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	v := openWallet(t, s, "100.00")
	ext := uuid.NewString()
	bet := command(t, v, wagering.Bet, "10.00", ext)
	first, err := s.submit.Execute(ctx, bet)
	if err != nil {
		t.Fatal(err)
	}

	changed := bet
	changed.Money = mustBRL(t, "11.00")
	if _, err := s.submit.Execute(ctx, changed); !errors.Is(err, app.ErrIdempotencyPayloadMismatch) {
		t.Fatalf("same key, other payload: %v", err)
	}

	otherKey := bet
	otherKey.IdempotencyKey = "another-key-" + ext
	_, err = s.submit.Execute(ctx, otherKey)
	var mismatch *app.KeyMismatchError
	if !errors.As(err, &mismatch) || mismatch.ExistingTransactionID != first.TransactionID {
		t.Fatalf("same operation, other key: %v", err)
	}
}

func TestInsufficientFundsIsAPersistedRejection(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	v := openWallet(t, s, "20.00")
	bet := command(t, v, wagering.Bet, "80.00", uuid.NewString())

	res, err := s.submit.Execute(ctx, bet)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != wagering.Rejected || res.FailureCode != wagering.BetInsufficientFunds || res.Balance.String() != "20.00" {
		t.Fatalf("result %+v", res)
	}
	replay, err := s.submit.Execute(ctx, bet)
	if err != nil || !replay.IdempotentReplay || replay.Status != wagering.Rejected {
		t.Fatalf("replay %+v, %v", replay, err)
	}
	if n := count(t, s.pool, `SELECT count(*) FROM wallet_ledger_entries WHERE transaction_id = $1`, res.TransactionID); n != 0 {
		t.Fatalf("rejected bet produced %d entries", n)
	}
	if n := count(t, s.pool, `SELECT count(*) FROM outbox_events WHERE aggregate_id = $1 AND event_type = 'WagerTransactionRejected'`, res.TransactionID); n != 1 {
		t.Fatalf("rejected events = %d", n)
	}
}

func TestLossDoesNotMoveBalanceOrVersion(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	v := openWallet(t, s, "50.00")
	res, err := s.submit.Execute(ctx, command(t, v, wagering.Loss, "0.00", uuid.NewString()))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != wagering.Processed || res.WalletVersion != 1 {
		t.Fatalf("result %+v", res)
	}
	after, _ := s.get.Execute(ctx, v.ID)
	if after.Version != 1 || after.Balance.String() != "50.00" {
		t.Fatalf("wallet %+v", after)
	}
	if n := count(t, s.pool, `SELECT count(*) FROM outbox_events WHERE partition_key = $1 AND event_type = 'WalletBalanceChanged'`, v.ID); n != 1 {
		t.Fatalf("LOSS must not emit WalletBalanceChanged (only the opening one exists), got %d", n)
	}
}

func TestWalletMismatchAndMissingWalletAreNotPersisted(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	v := openWallet(t, s, "50.00")

	wrongPlayer := command(t, v, wagering.Bet, "1.00", uuid.NewString())
	wrongPlayer.PlayerID = uuid.NewString()
	if _, err := s.submit.Execute(ctx, wrongPlayer); !errors.Is(err, app.ErrWalletMismatch) {
		t.Fatalf("player mismatch: %v", err)
	}
	missing := command(t, v, wagering.Bet, "1.00", uuid.NewString())
	missing.WalletID = uuid.NewString()
	if _, err := s.submit.Execute(ctx, missing); !errors.Is(err, app.ErrWalletNotFound) {
		t.Fatalf("missing wallet: %v", err)
	}
	if n := count(t, s.pool, `SELECT count(*) FROM wager_transactions WHERE external_id = ANY($1)`,
		[]string{wrongPlayer.ExternalTransactionID, missing.ExternalTransactionID}); n != 0 {
		t.Fatalf("corrigible errors persisted %d rows", n)
	}
}

// Mandatory scenario: 100.00, two concurrent bets of 80.00, independent pools.
func TestTwoConcurrentBetsOnSameWallet(t *testing.T) {
	a, b := newStack(t), newStack(t)
	v := openWallet(t, a, "100.00")
	bets := []app.SubmitCommand{
		command(t, v, wagering.Bet, "80.00", uuid.NewString()),
		command(t, v, wagering.Bet, "80.00", uuid.NewString()),
	}
	results := make([]app.SubmitResult, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, st := range []stack{a, b} {
		wg.Add(1)
		go func(i int, st stack) {
			defer wg.Done()
			results[i], errs[i] = st.submit.Execute(context.Background(), bets[i])
		}(i, st)
	}
	wg.Wait()
	processed, rejected := 0, 0
	for i := range results {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		switch results[i].Status {
		case wagering.Processed:
			processed++
		case wagering.Rejected:
			rejected++
			if results[i].FailureCode != wagering.BetInsufficientFunds {
				t.Fatalf("failure code %s", results[i].FailureCode)
			}
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatalf("processed %d rejected %d", processed, rejected)
	}
	final, _ := a.get.Execute(context.Background(), v.ID)
	if final.Balance.String() != "20.00" {
		t.Fatalf("final balance %s", final.Balance)
	}
	if n := count(t, a.pool, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'`, v.ID); n != 1 {
		t.Fatalf("debits = %d", n)
	}
	for _, bet := range bets {
		r, err := b.submit.Execute(context.Background(), bet)
		if err != nil || !r.IdempotentReplay {
			t.Fatalf("resend %+v %v", r, err)
		}
	}
	final, _ = a.get.Execute(context.Background(), v.ID)
	if final.Balance.String() != "20.00" {
		t.Fatalf("resends changed balance to %s", final.Balance)
	}
	assertReconciled(t, a.pool, v.ID)
}

// Mandatory scenario: the same bet 50 times in parallel debits once.
func TestFiftyIdenticalBetsDebitOnce(t *testing.T) {
	stacks := []stack{newStack(t), newStack(t), newStack(t)}
	v := openWallet(t, stacks[0], "1000.00")
	bet := command(t, v, wagering.Bet, "10.00", uuid.NewString())

	var wg sync.WaitGroup
	results := make([]app.SubmitResult, 50)
	errs := make([]error, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = stacks[i%3].submit.Execute(context.Background(), bet)
		}(i)
	}
	wg.Wait()
	fresh := 0
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("attempt %d: %v", i, errs[i])
		}
		if results[i].TransactionID != results[0].TransactionID || results[i].Balance.String() != "990.00" {
			t.Fatalf("attempt %d diverged: %+v", i, results[i])
		}
		if !results[i].IdempotentReplay {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("non-replay results = %d, want 1", fresh)
	}
	if n := count(t, stacks[0].pool, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'`, v.ID); n != 1 {
		t.Fatalf("debits = %d", n)
	}
	assertReconciled(t, stacks[0].pool, v.ID)
}

// No global lock: while wallet A is locked, wallet B still commits, and the
// blocked writer on A surfaces a transient error after lock_timeout.
func TestDistinctWalletsProgressWhileOneIsLocked(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	a := openWallet(t, s, "100.00")
	b := openWallet(t, s, "100.00")

	holder := connect(t, testDatabase(t).AppDSN)
	lockTx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback(ctx)
	if _, err := lockTx.Exec(ctx, `SELECT 1 FROM wallets WHERE id = $1 FOR UPDATE`, a.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := s.submit.Execute(ctx, command(t, b, wagering.Bet, "10.00", uuid.NewString())); err != nil {
		t.Fatalf("wallet B blocked by a lock on wallet A: %v", err)
	}
	_, err = s.submit.Execute(ctx, command(t, a, wagering.Bet, "10.00", uuid.NewString()))
	if !errors.Is(err, app.ErrTransient) {
		t.Fatalf("locked wallet error = %v, want ErrTransient", err)
	}
}

// A panic inside fn must still release the row lock: UnitOfWork.Do's deferred
// rollback runs unconditionally, not only when fn returns a non-nil error.
func TestPanicInsideUnitOfWorkReleasesTheLock(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	v := openWallet(t, s, "100.00")
	uow := postgres.NewUnitOfWork(s.pool)

	panicked := func() (panicked bool) {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		_ = uow.Do(ctx, func(ctx context.Context, tx app.Tx) error {
			if _, err := tx.Wallets().GetForUpdate(ctx, v.ID); err != nil {
				t.Fatal(err)
			}
			panic("boom: simulated crash while holding the wallet lock")
		})
		return false
	}()
	if !panicked {
		t.Fatal("expected fn to panic")
	}

	fresh := newStack(t)
	res, err := fresh.submit.Execute(ctx, command(t, v, wagering.Bet, "10.00", uuid.NewString()))
	if err != nil {
		t.Fatalf("wallet still locked after a panic in a previous transaction: %v", err)
	}
	if res.Status != wagering.Processed || res.Balance.String() != "90.00" {
		t.Fatalf("result %+v", res)
	}
}
