package wagering

import "fmt"

type Kind string

const (
	Opening  Kind = "OPENING"
	Bet      Kind = "BET"
	Win      Kind = "WIN"
	Loss     Kind = "LOSS"
	Refund   Kind = "REFUND"
	Rollback Kind = "ROLLBACK"
)

// ParseExternalKind accepts the kinds a provider may send. OPENING is internal.
func ParseExternalKind(s string) (Kind, error) {
	switch k := Kind(s); k {
	case Bet, Win, Loss, Refund, Rollback:
		return k, nil
	case Opening:
		return "", ErrOpeningNotExternal
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownKind, s)
}

func (k Kind) RequiresReference() bool { return k == Refund || k == Rollback }

type Origin string

const (
	OriginInternal Origin = "INTERNAL"
	OriginExternal Origin = "EXTERNAL"
)

type Status string

const (
	Pending          Status = "PENDING"
	PendingReference Status = "PENDING_REFERENCE"
	Processed        Status = "PROCESSED"
	Rejected         Status = "REJECTED"
	Failed           Status = "FAILED"
)

func (s Status) IsTerminal() bool { return s == Processed || s == Rejected || s == Failed }

// FailureCode is a stable public code. The class of each code is documented in
// ADR 0009: corrigible inputs are never persisted, so every code here is final.
type FailureCode string

const (
	BetInsufficientFunds      FailureCode = "BET_INSUFFICIENT_FUNDS"
	ReversalInsufficientFunds FailureCode = "REVERSAL_INSUFFICIENT_FUNDS"
	ReversalAlreadyApplied    FailureCode = "REVERSAL_ALREADY_APPLIED"
	ReferenceMismatch         FailureCode = "REFERENCE_MISMATCH"
	ReferenceNotProcessed     FailureCode = "REFERENCE_NOT_PROCESSED"
	ReferenceNotFound         FailureCode = "REFERENCE_NOT_FOUND"
	InvariantViolation        FailureCode = "INVARIANT_VIOLATION"
)
