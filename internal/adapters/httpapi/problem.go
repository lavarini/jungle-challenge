package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/lavarini/backend-challenge-go/internal/app"
)

// problem follows RFC 9457 with two extensions: a stable code and whether the
// caller may retry the same request.
type problem struct {
	Type          string `json:"type"`
	Title         string `json:"title"`
	Status        int    `json:"status"`
	Code          string `json:"code"`
	Detail        string `json:"detail,omitempty"`
	Retryable     bool   `json:"retryable"`
	TransactionID string `json:"transactionId,omitempty"`
}

func writeProblem(w http.ResponseWriter, status int, code, detail string, retryable bool, transactionID string) {
	w.Header().Set("Content-Type", "application/problem+json")
	if retryable {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{
		Type: "about:blank", Title: http.StatusText(status), Status: status,
		Code: code, Detail: detail, Retryable: retryable, TransactionID: transactionID,
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeError maps app errors to the HTTP contract (spec, section 3).
func (a *api) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var keyMismatch *app.KeyMismatchError
	switch {
	case errors.As(err, &keyMismatch):
		writeProblem(w, http.StatusConflict, "IDEMPOTENCY_KEY_MISMATCH", "operation already registered under another idempotency key", false, keyMismatch.ExistingTransactionID)
	case errors.Is(err, app.ErrIdempotencyPayloadMismatch):
		writeProblem(w, http.StatusConflict, "IDEMPOTENCY_PAYLOAD_MISMATCH", "idempotency key reused with a different payload", false, "")
	case errors.Is(err, app.ErrWalletExists):
		writeProblem(w, http.StatusConflict, "WALLET_ALREADY_EXISTS", "a wallet already exists for this player and currency", false, "")
	case errors.Is(err, app.ErrWalletNotFound):
		writeProblem(w, http.StatusNotFound, "WALLET_NOT_FOUND", "wallet not found", false, "")
	case errors.Is(err, app.ErrTransactionNotFound):
		writeProblem(w, http.StatusNotFound, "TRANSACTION_NOT_FOUND", "transaction not found", false, "")
	case errors.Is(err, app.ErrWalletMismatch):
		writeProblem(w, http.StatusBadRequest, "WALLET_MISMATCH", err.Error(), false, "")
	case errors.Is(err, app.ErrInvalidInput):
		writeProblem(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error(), false, "")
	case errors.Is(err, app.ErrNotImplemented):
		writeProblem(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", err.Error(), false, "")
	case errors.Is(err, app.ErrTransient), errors.Is(err, app.ErrUniqueConflict):
		a.log.WarnContext(r.Context(), "transient failure", "correlationId", correlationID(r.Context()), "error", err.Error(), "class", "transient")
		writeProblem(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "temporarily unavailable; retry with the same idempotency key", true, "")
	case errors.Is(err, app.ErrInvariantViolation):
		a.log.ErrorContext(r.Context(), "invariant violation", "correlationId", correlationID(r.Context()), "error", err.Error(), "class", "permanent")
		writeProblem(w, http.StatusInternalServerError, "INVARIANT_VIOLATION", "", false, "")
	default:
		a.log.ErrorContext(r.Context(), "unexpected error", "correlationId", correlationID(r.Context()), "error", err.Error(), "class", "permanent")
		writeProblem(w, http.StatusInternalServerError, "INTERNAL_ERROR", "", false, "")
	}
}
