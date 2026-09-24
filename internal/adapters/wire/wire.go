// Package wire holds the parsing shared by the HTTP and SQS edges, so both
// transports turn the same input into the same command (and the same hash).
package wire

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/money"
)

// ParseUUID accepts only the canonical lowercase 36-character form.
func ParseUUID(field, s string) (string, error) {
	u, err := uuid.Parse(s)
	if err != nil || len(s) != 36 || u.String() != s {
		return "", fmt.Errorf("%w: %s must be a canonical lowercase UUID", app.ErrInvalidInput, field)
	}
	return s, nil
}

type Money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func (m Money) Parse(field string) (money.Money, error) {
	v, err := money.Parse(m.Amount, m.Currency)
	if err != nil {
		return money.Money{}, fmt.Errorf("%w: %s: %w", app.ErrInvalidInput, field, err)
	}
	return v, nil
}
