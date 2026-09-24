package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/lavarini/backend-challenge-go/internal/app"
)

// classify maps database errors to app sentinels, keeping the original error
// in the chain. Errors that are already app sentinels pass through.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23505" && pgErr.ConstraintName == "wallets_player_currency_key":
			return fmt.Errorf("%w: %w", app.ErrWalletExists, err)
		case pgErr.Code == "23505":
			return fmt.Errorf("%w: %w", app.ErrUniqueConflict, err)
		case strings.HasPrefix(pgErr.Code, "23"):
			return fmt.Errorf("%w: %w", app.ErrInvariantViolation, err)
		case strings.HasPrefix(pgErr.Code, "08"):
			return fmt.Errorf("%w: %w", app.ErrTransient, err)
		case strings.HasPrefix(pgErr.Code, "53"):
			return fmt.Errorf("%w: %w", app.ErrTransient, err)
		}
		switch pgErr.Code {
		case "55P03", "57014", "40001", "40P01", "57P01", "57P02", "57P03":
			return fmt.Errorf("%w: %w", app.ErrTransient, err)
		}
		return fmt.Errorf("postgres: %w", err)
	}
	var connectErr *pgconn.ConnectError
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || errors.Is(err, pgconn.ErrConnClosed) ||
		errors.As(err, &connectErr) || errors.As(err, &netErr) || pgconn.SafeToRetry(err) || pgconn.Timeout(err) {
		return fmt.Errorf("%w: %w", app.ErrTransient, err)
	}
	return err
}
