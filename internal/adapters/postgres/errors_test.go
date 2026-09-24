package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/lavarini/backend-challenge-go/internal/app"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want error
	}{
		{"duplicate wallet", &pgconn.PgError{Code: "23505", ConstraintName: "wallets_player_currency_key"}, app.ErrWalletExists},
		{"duplicate identity", &pgconn.PgError{Code: "23505", ConstraintName: "wager_tx_idempotency_key"}, app.ErrUniqueConflict},
		{"check violation", &pgconn.PgError{Code: "23514"}, app.ErrInvariantViolation},
		{"fk violation", &pgconn.PgError{Code: "23503"}, app.ErrInvariantViolation},
		{"lock timeout", &pgconn.PgError{Code: "55P03"}, app.ErrTransient},
		{"statement timeout", &pgconn.PgError{Code: "57014"}, app.ErrTransient},
		{"serialization", &pgconn.PgError{Code: "40001"}, app.ErrTransient},
		{"deadlock", &pgconn.PgError{Code: "40P01"}, app.ErrTransient},
		{"admin shutdown", &pgconn.PgError{Code: "57P01"}, app.ErrTransient},
		{"connection exception", &pgconn.PgError{Code: "08006"}, app.ErrTransient},
		{"deadline", context.DeadlineExceeded, app.ErrTransient},
		{"passthrough", app.ErrWalletNotFound, app.ErrWalletNotFound},
	}
	for _, c := range cases {
		if got := classify(c.in); !errors.Is(got, c.want) {
			t.Errorf("%s: classify = %v, want %v", c.name, got, c.want)
		}
	}
	if classify(nil) != nil {
		t.Fatal("classify(nil) must be nil")
	}
}
