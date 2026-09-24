//go:build integration

package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lavarini/backend-challenge-go/internal/adapters/postgres"
	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/platform"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

func referencing(t *testing.T, w app.WalletView, kind wagering.Kind, amount, referenceExternalID string) app.SubmitCommand {
	c := command(t, w, kind, amount, uuid.NewString())
	c.ReferenceExternalTransactionID = referenceExternalID
	return c
}

func submit(t *testing.T, s stack, c app.SubmitCommand) app.SubmitResult {
	t.Helper()
	r, err := s.submit.Execute(context.Background(), c)
	if err != nil {
		t.Fatalf("%s %s: %v", c.Kind, c.ExternalTransactionID, err)
	}
	return r
}

func balanceOf(t *testing.T, s stack, walletID string) string {
	t.Helper()
	v, err := s.get.Execute(context.Background(), walletID)
	if err != nil {
		t.Fatal(err)
	}
	return v.Balance.String()
}

func TestRefundRestoresTheBetAndLinksTheReference(t *testing.T) {
	s := newStack(t)
	w := openWallet(t, s, "100.00")
	bet := command(t, w, wagering.Bet, "30.00", uuid.NewString())
	betRes := submit(t, s, bet)

	refund := submit(t, s, referencing(t, w, wagering.Refund, "30.00", bet.ExternalTransactionID))
	if refund.Status != wagering.Processed || refund.Balance.String() != "100.00" {
		t.Fatalf("refund %+v", refund)
	}
	var ref string
	if err := s.pool.QueryRow(context.Background(), `SELECT reference_tx_id::text FROM wager_transactions WHERE id = $1`,
		refund.TransactionID).Scan(&ref); err != nil || ref != betRes.TransactionID {
		t.Fatalf("reference_tx_id = %q (%v), want %s", ref, err, betRes.TransactionID)
	}
	assertReconciled(t, s.pool, w.ID)
}

func TestRefundAndRollbackShareOneReversalSlot(t *testing.T) {
	for _, order := range [][2]wagering.Kind{{wagering.Refund, wagering.Rollback}, {wagering.Rollback, wagering.Refund}} {
		t.Run(string(order[0])+"_then_"+string(order[1]), func(t *testing.T) {
			s := newStack(t)
			w := openWallet(t, s, "100.00")
			bet := command(t, w, wagering.Bet, "30.00", uuid.NewString())
			submit(t, s, bet)
			first := submit(t, s, referencing(t, w, order[0], "30.00", bet.ExternalTransactionID))
			second := submit(t, s, referencing(t, w, order[1], "30.00", bet.ExternalTransactionID))
			if first.Status != wagering.Processed {
				t.Fatalf("first reversal %+v", first)
			}
			if second.Status != wagering.Rejected || second.FailureCode != wagering.ReversalAlreadyApplied {
				t.Fatalf("second reversal %+v", second)
			}
			if got := balanceOf(t, s, w.ID); got != "100.00" {
				t.Fatalf("balance %s: the bet was returned twice", got)
			}
			assertReconciled(t, s.pool, w.ID)
		})
	}
}

func TestRollbackOfRefundDebitsAgain(t *testing.T) {
	s := newStack(t)
	w := openWallet(t, s, "100.00")
	bet := command(t, w, wagering.Bet, "30.00", uuid.NewString())
	submit(t, s, bet)
	refund := referencing(t, w, wagering.Refund, "30.00", bet.ExternalTransactionID)
	submit(t, s, refund)
	rollback := submit(t, s, referencing(t, w, wagering.Rollback, "30.00", refund.ExternalTransactionID))
	if rollback.Status != wagering.Processed || rollback.Balance.String() != "70.00" {
		t.Fatalf("rollback of refund %+v", rollback)
	}
	assertReconciled(t, s.pool, w.ID)
}

func TestRollbackOfWinWithoutFundsIsItsOwnRejection(t *testing.T) {
	s := newStack(t)
	w := openWallet(t, s, "0.00")
	win := command(t, w, wagering.Win, "50.00", uuid.NewString())
	submit(t, s, win)
	submit(t, s, command(t, w, wagering.Bet, "40.00", uuid.NewString()))
	rollback := submit(t, s, referencing(t, w, wagering.Rollback, "50.00", win.ExternalTransactionID))
	if rollback.Status != wagering.Rejected || rollback.FailureCode != wagering.ReversalInsufficientFunds {
		t.Fatalf("rollback %+v", rollback)
	}
	if got := balanceOf(t, s, w.ID); got != "10.00" {
		t.Fatalf("balance %s", got)
	}
}

func TestReferenceMismatchAndRejectedReference(t *testing.T) {
	s := newStack(t)
	w := openWallet(t, s, "100.00")
	bet := command(t, w, wagering.Bet, "30.00", uuid.NewString())
	submit(t, s, bet)
	mismatch := submit(t, s, referencing(t, w, wagering.Refund, "20.00", bet.ExternalTransactionID))
	if mismatch.Status != wagering.Rejected || mismatch.FailureCode != wagering.ReferenceMismatch {
		t.Fatalf("partial refund %+v", mismatch)
	}

	rejectedBet := command(t, w, wagering.Bet, "1000.00", uuid.NewString())
	if r := submit(t, s, rejectedBet); r.Status != wagering.Rejected {
		t.Fatalf("setup: bet without funds %+v", r)
	}
	notProcessed := submit(t, s, referencing(t, w, wagering.Refund, "1000.00", rejectedBet.ExternalTransactionID))
	if notProcessed.Status != wagering.Rejected || notProcessed.FailureCode != wagering.ReferenceNotProcessed {
		t.Fatalf("refund of rejected bet %+v", notProcessed)
	}
	if got := balanceOf(t, s, w.ID); got != "70.00" {
		t.Fatalf("balance %s", got)
	}
}

// The initial backoff is 1h, not DefaultReferencePolicy's 1s: under load, or
// with clock drift in the Docker VM, more than 1s can pass between submit and
// the "still waiting" assertion below, which would make the assertion flake
// against the container's now() even though nothing was actually woken
// A 1h backoff makes "still waiting"
// unambiguous, and comparing the wake against the recorded next_attempt_at
// instead of wall-clock now() keeps the assertion meaningful regardless of
// how much time elapses around it.
func TestRefundBeforeBetWaitsAndIsWokenByTheBet(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	slow := s
	slow.submit = app.NewSubmitWager(postgres.NewUnitOfWork(s.pool), platform.NewSystemClock(), platform.NewUUIDv7(),
		app.ReferencePolicy{TTL: time.Hour, InitialBackoff: time.Hour, MaxBackoff: time.Hour})
	w := openWallet(t, s, "100.00")
	betExternalID := uuid.NewString()

	pending := submit(t, slow, referencing(t, w, wagering.Refund, "30.00", betExternalID))
	if pending.Status != wagering.PendingReference || pending.Balance.Valid() {
		t.Fatalf("pending %+v", pending)
	}
	if n := count(t, s.pool, `SELECT count(*) FROM outbox_events WHERE aggregate_id = $1 AND event_type = 'WagerTransactionPendingReference'`, pending.TransactionID); n != 1 {
		t.Fatalf("pending events = %d", n)
	}
	if n := count(t, s.pool, `SELECT count(*) FROM wallet_ledger_entries WHERE transaction_id = $1`, pending.TransactionID); n != 0 {
		t.Fatalf("pending produced %d ledger entries", n)
	}
	var before time.Time
	if err := s.pool.QueryRow(ctx, `SELECT next_attempt_at FROM wager_transactions WHERE id = $1`, pending.TransactionID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if !before.After(time.Now().Add(30 * time.Minute)) {
		t.Fatalf("new pending operation must be scheduled far in the future, next_attempt_at=%s", before)
	}

	bet := command(t, w, wagering.Bet, "30.00", betExternalID)
	submit(t, s, bet)
	var after time.Time
	if err := s.pool.QueryRow(ctx, `SELECT next_attempt_at FROM wager_transactions WHERE id = $1`, pending.TransactionID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.Before(before) {
		t.Fatalf("processing the reference must wake the pending operation: before=%s after=%s", before, after)
	}
}

func TestConcurrentReversalsOnlyOneSucceeds(t *testing.T) {
	a, b := newStack(t), newStack(t)
	w := openWallet(t, a, "100.00")
	bet := command(t, w, wagering.Bet, "30.00", uuid.NewString())
	submit(t, a, bet)

	cmds := []app.SubmitCommand{
		referencing(t, w, wagering.Refund, "30.00", bet.ExternalTransactionID),
		referencing(t, w, wagering.Rollback, "30.00", bet.ExternalTransactionID),
	}
	results := make([]app.SubmitResult, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, st := range []stack{a, b} {
		wg.Add(1)
		go func(i int, st stack) {
			defer wg.Done()
			results[i], errs[i] = st.submit.Execute(context.Background(), cmds[i])
		}(i, st)
	}
	wg.Wait()
	processed := 0
	for i := range results {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if results[i].Status == wagering.Processed {
			processed++
		} else if results[i].FailureCode != wagering.ReversalAlreadyApplied {
			t.Fatalf("loser %+v", results[i])
		}
	}
	if processed != 1 {
		t.Fatalf("processed reversals = %d", processed)
	}
	if got := balanceOf(t, a, w.ID); got != "100.00" {
		t.Fatalf("balance %s", got)
	}
	assertReconciled(t, a.pool, w.ID)
}
