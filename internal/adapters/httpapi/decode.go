package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"

	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/money"
)

// decodeJSON accepts exactly one JSON object with known fields only.
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("%w: body: %v", app.ErrInvalidInput, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: body must contain a single JSON object", app.ErrInvalidInput)
	}
	return nil
}

// parseUUID accepts only the canonical lowercase 36-character form, so the
// idempotency hash never sees two spellings of the same id.
func parseUUID(field, s string) (string, error) {
	u, err := uuid.Parse(s)
	if err != nil || len(s) != 36 || u.String() != s {
		return "", fmt.Errorf("%w: %s must be a canonical lowercase UUID", app.ErrInvalidInput, field)
	}
	return s, nil
}

type moneyDTO struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func (d moneyDTO) toMoney(field string) (money.Money, error) {
	m, err := money.Parse(d.Amount, d.Currency)
	if err != nil {
		return money.Money{}, fmt.Errorf("%w: %s: %w", app.ErrInvalidInput, field, err)
	}
	return m, nil
}
