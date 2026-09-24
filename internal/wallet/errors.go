package wallet

import "errors"

var (
	ErrInvalidWallet     = errors.New("wallet: invalid wallet")
	ErrInsufficientFunds = errors.New("wallet: insufficient funds")
	ErrCurrencyMismatch  = errors.New("wallet: currency mismatch")
	ErrNonPositiveAmount = errors.New("wallet: amount must be positive")
	ErrInvalidEntry      = errors.New("wallet: invalid ledger entry")
)
