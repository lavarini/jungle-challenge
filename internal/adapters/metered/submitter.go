// Package metered records metrics around use cases, keeping app free of
// instrumentation. Label values come only from closed sets (kind, status,
// source, reason): never ids (spec, section 7).
package metered

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/lavarini/backend-challenge-go/internal/app"
)

type Submitter interface {
	Execute(ctx context.Context, cmd app.SubmitCommand) (app.SubmitResult, error)
}

// Recorder is the part of metrics.Metrics the submitter uses.
type Recorder interface {
	ObserveProcessing(source, status string, d time.Duration)
	Transaction(kind, status, source string)
	Replay(source string, fromInbox bool)
	LockConflict(reason string)
	InvariantViolation(path string)
}

type submitter struct {
	next Submitter
	m    Recorder
}

func NewSubmitter(next Submitter, m Recorder) Submitter { return submitter{next: next, m: m} }

func (s submitter) Execute(ctx context.Context, cmd app.SubmitCommand) (app.SubmitResult, error) {
	start := time.Now()
	res, err := s.next.Execute(ctx, cmd)
	status := string(res.Status)
	if err != nil {
		status = "error"
	}
	source := string(cmd.Source)
	s.m.ObserveProcessing(source, status, time.Since(start))
	if err == nil {
		// A replay repeats an outcome already counted; it has its own
		// counter (wager_idempotent_replays_total).
		if res.IdempotentReplay {
			s.m.Replay(source, res.FromInbox)
		} else {
			s.m.Transaction(string(cmd.Kind), status, source)
		}
		return res, nil
	}
	switch {
	case pgCode(err) == "55P03":
		s.m.LockConflict("lock_timeout")
	case errors.Is(err, app.ErrUniqueConflict):
		// SubmitWager already retried once; this is the conflict that
		// survived the retry and reached the caller.
		s.m.LockConflict("unique_retry")
	case errors.Is(err, app.ErrInvariantViolation):
		s.m.InvariantViolation("sync")
	}
	return res, err
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
