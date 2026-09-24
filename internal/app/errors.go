package app

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidInput               = errors.New("invalid input")
	ErrWalletNotFound             = errors.New("wallet not found")
	ErrWalletMismatch             = errors.New("wallet does not match player or currency")
	ErrWalletExists               = errors.New("wallet already exists for player and currency")
	ErrIdempotencyPayloadMismatch = errors.New("idempotency key reused with a different payload")
	ErrIdempotencyKeyMismatch     = errors.New("operation already registered under another idempotency key")
	// ErrUniqueConflict is a concurrent insert of the same identity; retried once.
	ErrUniqueConflict     = errors.New("concurrent unique conflict")
	ErrTransient          = errors.New("transient infrastructure failure")
	ErrInvariantViolation = errors.New("database invariant violation")
	ErrNotImplemented     = errors.New("not implemented")
	ErrUnauthenticated    = errors.New("unauthenticated")
)

// KeyMismatchError carries the id of the operation registered under another key.
type KeyMismatchError struct {
	ExistingTransactionID string
}

func (e *KeyMismatchError) Error() string {
	return fmt.Sprintf("%s: existing transaction %s", ErrIdempotencyKeyMismatch, e.ExistingTransactionID)
}

func (e *KeyMismatchError) Unwrap() error { return ErrIdempotencyKeyMismatch }
