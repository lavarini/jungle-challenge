//go:build integration

package integration

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lavarini/backend-challenge-go/internal/adapters/postgres"
	"github.com/lavarini/backend-challenge-go/internal/adapters/refworker"
	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/platform"
	"github.com/lavarini/backend-challenge-go/internal/platform/runner"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

func resolverFor(s stack, policy app.ReferencePolicy) *app.ResolvePending {
	return app.NewResolvePending(postgres.NewUnitOfWork(s.pool), platform.NewSystemClock(), platform.NewUUIDv7(), policy, quietLog, app.ResolveHooks{})
}

// hookRecord captures what ResolvePending reports to its hooks (metrics).
type hookRecord struct {
	mu         sync.Mutex
	concluded  []wagering.Status
	invariants int
}

func (h *hookRecord) hooks() app.ResolveHooks {
	return app.ResolveHooks{
		Concluded: func(_ wagering.Kind, st wagering.Status) {
			h.mu.Lock()
			h.concluded = append(h.concluded, st)
			h.mu.Unlock()
		},
		InvariantViolated: func() { h.mu.Lock(); h.invariants++; h.mu.Unlock() },
	}
}

func (h *hookRecord) snapshot() ([]wagering.Status, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]wagering.Status(nil), h.concluded...), h.invariants
}

func hookedResolver(s stack, policy app.ReferencePolicy, h *hookRecord) *app.ResolvePending {
	return app.NewResolvePending(postgres.NewUnitOfWork(s.pool), platform.NewSystemClock(), platform.NewUUIDv7(), policy, quietLog, h.hooks())
}

func shortPolicy(ttl time.Duration) app.ReferencePolicy {
	return app.ReferencePolicy{TTL: ttl, InitialBackoff: 50 * time.Millisecond, MaxBackoff: 200 * time.Millisecond}
}

func statusOf(t *testing.T, s stack, id string) (wagering.Status, string) {
	t.Helper()
	var status string
	var code *string
	if err := s.pool.QueryRow(context.Background(), `SELECT status, failure_code FROM wager_transactions WHERE id = $1`, id).Scan(&status, &code); err != nil {
		t.Fatal(err)
	}
	if code == nil {
		return wagering.Status(status), ""
	}
	return wagering.Status(status), *code
}

// resolveUntilSettled claims and resolves until id leaves PENDING_REFERENCE.
func resolveUntilSettled(t *testing.T, s stack, r *app.ResolvePending, id string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		ids, err := r.Claim(context.Background(), 100, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		for _, claimed := range ids {
			if err := r.Resolve(context.Background(), claimed); err != nil {
				t.Logf("resolve %s: %v", claimed, err)
			}
		}
		if st, _ := statusOf(t, s, id); st != wagering.PendingReference {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("transaction %s still pending after %s", id, within)
}

func TestPendingRefundIsResolvedAfterTheBet(t *testing.T) {
	s := newStack(t)
	hooks := &hookRecord{}
	r := hookedResolver(s, shortPolicy(time.Hour), hooks)
	w := openWallet(t, s, "100.00")
	betExternalID := uuid.NewString()
	pending := submit(t, s, referencing(t, w, wagering.Refund, "30.00", betExternalID))
	submit(t, s, command(t, w, wagering.Bet, "30.00", betExternalID))

	resolveUntilSettled(t, s, r, pending.TransactionID, 5*time.Second)
	if st, _ := statusOf(t, s, pending.TransactionID); st != wagering.Processed {
		t.Fatalf("status %s", st)
	}
	if concluded, _ := hooks.snapshot(); len(concluded) != 1 || concluded[0] != wagering.Processed {
		t.Fatalf("concluded = %v, want [PROCESSED]", concluded)
	}
	if got := balanceOf(t, s, w.ID); got != "100.00" {
		t.Fatalf("balance %s", got)
	}
	if n := count(t, s.pool, `SELECT count(*) FROM outbox_events WHERE aggregate_id = $1 AND event_type = 'WagerTransactionProcessed'`, pending.TransactionID); n != 1 {
		t.Fatalf("processed events = %d", n)
	}
	replay, err := s.submit.Execute(context.Background(), referencingSame(t, s, pending.TransactionID))
	if err != nil || !replay.IdempotentReplay || replay.Status != wagering.Processed || replay.Balance.String() != "100.00" {
		t.Fatalf("replay after resolution %+v, %v", replay, err)
	}
	assertReconciled(t, s.pool, w.ID)
}

// referencingSame rebuilds the original command of a stored transaction so a
// replay can be sent.
func referencingSame(t *testing.T, s stack, id string) app.SubmitCommand {
	t.Helper()
	var c app.SubmitCommand
	var kind, amount string
	err := s.pool.QueryRow(context.Background(), `SELECT provider_id, external_id, idempotency_key, player_id::text,
		wallet_id::text, round_id, game_id, kind, (amount_minor / 100)::text || '.' || lpad((amount_minor % 100)::text, 2, '0'),
		reference_external_id FROM wager_transactions WHERE id = $1`, id).Scan(
		&c.ProviderID, &c.ExternalTransactionID, &c.IdempotencyKey, &c.PlayerID, &c.WalletID,
		&c.RoundID, &c.GameID, &kind, &amount, &c.ReferenceExternalTransactionID)
	if err != nil {
		t.Fatal(err)
	}
	c.Kind, c.Money, c.CorrelationID, c.Source = wagering.Kind(kind), mustBRL(t, amount), uuid.NewString(), app.SourceHTTP
	return c
}

func TestPendingExpiresAsReferenceNotFound(t *testing.T) {
	s := newStack(t)
	stackWithShortTTL := s
	stackWithShortTTL.submit = app.NewSubmitWager(postgres.NewUnitOfWork(s.pool), platform.NewSystemClock(), platform.NewUUIDv7(), shortPolicy(300*time.Millisecond))
	hooks := &hookRecord{}
	r := hookedResolver(s, shortPolicy(300*time.Millisecond), hooks)
	w := openWallet(t, s, "100.00")
	pending := submit(t, stackWithShortTTL, referencing(t, w, wagering.Refund, "30.00", uuid.NewString()))

	resolveUntilSettled(t, s, r, pending.TransactionID, 5*time.Second)
	st, code := statusOf(t, s, pending.TransactionID)
	if st != wagering.Rejected || code != string(wagering.ReferenceNotFound) {
		t.Fatalf("status %s code %s", st, code)
	}
	// Reschedules before the deadline are not conclusions; the expiry is.
	if concluded, _ := hooks.snapshot(); len(concluded) != 1 || concluded[0] != wagering.Rejected {
		t.Fatalf("concluded = %v, want [REJECTED]", concluded)
	}
	if n := count(t, s.pool, `SELECT count(*) FROM outbox_events WHERE aggregate_id = $1 AND event_type = 'WagerTransactionRejected'`, pending.TransactionID); n != 1 {
		t.Fatalf("rejected events = %d", n)
	}
	if got := balanceOf(t, s, w.ID); got != "100.00" {
		t.Fatalf("balance %s", got)
	}
}

func TestUnresolvedPendingIsRescheduled(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	fast := s
	fast.submit = app.NewSubmitWager(postgres.NewUnitOfWork(s.pool), platform.NewSystemClock(), platform.NewUUIDv7(), shortPolicy(time.Hour))
	r := resolverFor(s, shortPolicy(time.Hour))
	w := openWallet(t, s, "100.00")
	pending := submit(t, fast, referencing(t, w, wagering.Refund, "30.00", uuid.NewString()))

	time.Sleep(100 * time.Millisecond)
	ids, err := r.Claim(ctx, 1000, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if err := r.Resolve(ctx, id); err != nil {
			t.Fatalf("resolve %s: %v", id, err)
		}
	}
	var attempts int
	var future bool
	if err := s.pool.QueryRow(ctx, `SELECT attempts, next_attempt_at > now() FROM wager_transactions WHERE id = $1`,
		pending.TransactionID).Scan(&attempts, &future); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || !future {
		t.Fatalf("attempts %d, next attempt in the future %v", attempts, future)
	}
}

func TestConcurrentClaimsAreDisjoint(t *testing.T) {
	a, b := newStack(t), newStack(t)
	fast := a
	fast.submit = app.NewSubmitWager(postgres.NewUnitOfWork(a.pool), platform.NewSystemClock(), platform.NewUUIDv7(), shortPolicy(time.Hour))
	w := openWallet(t, a, "100.00")
	for i := 0; i < 10; i++ {
		submit(t, fast, referencing(t, w, wagering.Refund, "1.00", uuid.NewString()))
	}
	time.Sleep(100 * time.Millisecond)

	ra, rb := resolverFor(a, shortPolicy(time.Hour)), resolverFor(b, shortPolicy(time.Hour))
	var wg sync.WaitGroup
	var idsA, idsB []string
	var errA, errB error
	wg.Add(2)
	go func() { defer wg.Done(); idsA, errA = ra.Claim(context.Background(), 1000, time.Minute) }()
	go func() { defer wg.Done(); idsB, errB = rb.Claim(context.Background(), 1000, time.Minute) }()
	wg.Wait()
	if errA != nil || errB != nil {
		t.Fatal(errA, errB)
	}
	seen := map[string]bool{}
	for _, id := range append(idsA, idsB...) {
		if seen[id] {
			t.Fatalf("transaction %s claimed twice", id)
		}
		seen[id] = true
	}
	// Scoped to this wallet: Claim is global within a database, and this
	// test's own two pools (a, b) share one per-test database, so idsA/idsB
	// may also contain the wallet's own rows claimed out of order.
	rows, err := a.pool.Query(context.Background(), `SELECT id::text FROM wager_transactions WHERE wallet_id = $1 AND status = 'PENDING_REFERENCE'`, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	mine, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 10 {
		t.Fatalf("pending rows for the wallet = %d, want 10", len(mine))
	}
	claimed := 0
	for _, id := range mine {
		if seen[id] {
			claimed++
		}
	}
	if claimed != len(mine) {
		t.Fatalf("idsA+idsB claimed %d of the wallet's %d pending rows, so some were claimed by nobody", claimed, len(mine))
	}
}

// ADR 0011: the ledger trigger refuses a ROLLBACK whose reference has no
// ledger entry. The resolver records FAILED instead of retrying forever.
func TestInvariantViolationBecomesFailed(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	hooks := &hookRecord{}
	r := hookedResolver(s, shortPolicy(time.Hour), hooks)
	w := openWallet(t, s, "100.00")
	betExternalID := uuid.NewString()
	pending := submit(t, s, referencing(t, w, wagering.Rollback, "30.00", betExternalID))

	// A PROCESSED BET without its ledger entry: only possible by writing SQL
	// directly, which is exactly the class of bug the trigger exists for.
	_, err := s.pool.Exec(ctx, `INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, currency,
		amount_minor, provider_id, external_id, idempotency_key, payload_hash, round_id, game_id, correlation_id,
		result_balance_minor, result_wallet_version, created_at, updated_at, completed_at)
		SELECT $1::uuid, 'EXTERNAL', 'BET', 'PROCESSED', id, player_id, currency, 3000, 'provider-a', $2::text, $2::text, $3,
		       'round-1', 'game-1', 'corrupt', balance_minor, version, now(), now(), now()
		FROM wallets WHERE id = $4`, uuid.NewString(), betExternalID, bytes.Repeat([]byte{9}, 32), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE wager_transactions SET next_attempt_at = now() WHERE id = $1`, pending.TransactionID); err != nil {
		t.Fatal(err)
	}

	resolveUntilSettled(t, s, r, pending.TransactionID, 5*time.Second)
	st, code := statusOf(t, s, pending.TransactionID)
	if st != wagering.Failed || code != string(wagering.InvariantViolation) {
		t.Fatalf("status %s code %s", st, code)
	}
	if concluded, invariants := hooks.snapshot(); invariants != 1 || len(concluded) != 1 || concluded[0] != wagering.Failed {
		t.Fatalf("hooks: concluded %v invariants %d, want [FAILED] and 1", concluded, invariants)
	}
	if got := balanceOf(t, s, w.ID); got != "100.00" {
		t.Fatalf("balance %s: a failed resolution moved money", got)
	}
	ids, err := r.Claim(ctx, 1000, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if id == pending.TransactionID {
			t.Fatalf("FAILED operation %s claimed again", id)
		}
	}
	// 1 row is expected: the WagerTransactionPendingReference event emitted
	// at submission. The FAILED transition itself emits no event.
	if n := count(t, s.pool, `SELECT count(*) FROM outbox_events WHERE aggregate_id = $1`, pending.TransactionID); n != 1 {
		t.Fatalf("outbox events for a FAILED operation = %d, want 1 (only the pending-reference event)", n)
	}
	if n := count(t, s.pool, `SELECT count(*) FROM wallet_ledger_entries WHERE transaction_id = $1`, pending.TransactionID); n != 0 {
		t.Fatalf("ledger entries for a FAILED operation = %d", n)
	}
}

func TestWorkerResolvesInTheBackgroundAndStops(t *testing.T) {
	s := newStack(t)
	r := resolverFor(s, shortPolicy(time.Hour))
	w := openWallet(t, s, "100.00")
	betExternalID := uuid.NewString()
	pending := submit(t, s, referencing(t, w, wagering.Refund, "30.00", betExternalID))

	worker := refworker.New(r, refworker.Config{Interval: 50 * time.Millisecond, Lease: time.Second, Batch: 50}, quietLog)
	run := runner.Start(worker.Loop)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = run.Stop(ctx)
	})
	submit(t, s, command(t, w, wagering.Bet, "30.00", betExternalID))

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st, _ := statusOf(t, s, pending.TransactionID); st == wagering.Processed {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := run.Stop(ctx); err != nil {
		t.Fatalf("worker stop: %v", err)
	}
	if st, _ := statusOf(t, s, pending.TransactionID); st != wagering.Processed {
		t.Fatalf("worker left the operation %s", st)
	}
}
