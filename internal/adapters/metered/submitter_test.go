package metered

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/platform/metrics"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

type stub struct {
	res app.SubmitResult
	err error
}

func (s stub) Execute(context.Context, app.SubmitCommand) (app.SubmitResult, error) {
	return s.res, s.err
}

func errorsJoin(a, b error) error { return fmt.Errorf("%w: %w", a, b) }

func TestSubmitterRecordsOutcomes(t *testing.T) {
	m := metrics.New()
	cmd := app.SubmitCommand{Kind: wagering.Bet, Source: app.SourceSQS}

	_, _ = NewSubmitter(stub{res: app.SubmitResult{Status: wagering.Processed}}, m).Execute(context.Background(), cmd)
	_, _ = NewSubmitter(stub{res: app.SubmitResult{Status: wagering.Processed, IdempotentReplay: true, FromInbox: true}}, m).Execute(context.Background(), cmd)
	// A replay answered by the financial idempotency (a new message id for an
	// operation already applied) is a replay, not an inbox duplicate.
	_, _ = NewSubmitter(stub{res: app.SubmitResult{Status: wagering.Processed, IdempotentReplay: true}}, m).Execute(context.Background(), cmd)
	lockTimeout := &pgconn.PgError{Code: "55P03"}
	_, _ = NewSubmitter(stub{err: errorsJoin(app.ErrTransient, lockTimeout)}, m).Execute(context.Background(), cmd)
	_, _ = NewSubmitter(stub{err: app.ErrUniqueConflict}, m).Execute(context.Background(), cmd)
	_, _ = NewSubmitter(stub{err: app.ErrInvariantViolation}, m).Execute(context.Background(), cmd)

	// Replays have their own counter; only the first outcome is a transaction.
	if got := testutil.ToFloat64(m.Transactions.WithLabelValues("BET", "PROCESSED", "sqs")); got != 1 {
		t.Errorf("transactions = %v, want 1 (replays excluded)", got)
	}
	if got := testutil.ToFloat64(m.Replays.WithLabelValues("sqs")); got != 2 {
		t.Errorf("replays = %v", got)
	}
	if got := testutil.ToFloat64(m.InboxDuplicates); got != 1 {
		t.Errorf("inbox duplicates = %v", got)
	}
	if got := testutil.ToFloat64(m.LockConflicts.WithLabelValues("lock_timeout")); got != 1 {
		t.Errorf("lock conflicts = %v", got)
	}
	if got := testutil.ToFloat64(m.LockConflicts.WithLabelValues("unique_retry")); got != 1 {
		t.Errorf("unique retries = %v", got)
	}
	if got := testutil.ToFloat64(m.InvariantViolations.WithLabelValues("sync")); got != 1 {
		t.Errorf("invariant violations = %v", got)
	}
	if got := testutil.CollectAndCount(m.Processing); got != 2 {
		t.Errorf("processing series = %d, want PROCESSED and error", got)
	}
}

type stubResolver struct{ err error }

func (s stubResolver) Claim(context.Context, int, time.Duration) ([]string, error) { return nil, s.err }
func (s stubResolver) Resolve(context.Context, string) error                       { return s.err }

func TestResolverCountsLockConflictsAndDeadlocks(t *testing.T) {
	m := metrics.New()
	for _, code := range []string{"55P03", "40P01", "23505"} {
		err := errorsJoin(app.ErrTransient, &pgconn.PgError{Code: code})
		_ = NewResolver(stubResolver{err: err}, m).Resolve(context.Background(), "t1")
	}
	_ = NewResolver(stubResolver{}, m).Resolve(context.Background(), "t1")
	if got := testutil.ToFloat64(m.ReferenceLockConflicts.WithLabelValues("lock_timeout")); got != 1 {
		t.Errorf("lock_timeout = %v", got)
	}
	if got := testutil.ToFloat64(m.ReferenceLockConflicts.WithLabelValues("deadlock")); got != 1 {
		t.Errorf("deadlock = %v", got)
	}
}
