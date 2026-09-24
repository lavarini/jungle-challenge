package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/lavarini/backend-challenge-go/internal/app"
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
