package wagering

import (
	"fmt"

	"github.com/lavarini/backend-challenge-go/internal/money"
)

// Reference is what a decision needs to know about the referenced operation.
type Reference struct {
	TransactionID string
	Kind          Kind
	Status        Status
	ProviderID    string
	PlayerID      string
	WalletID      string
	RoundID       string
	Amount        money.Money
	// Reversed reports whether a PROCESSED REFUND or ROLLBACK already
	// references this operation (ADR 0005: one reversal slot).
	Reversed bool
}

func ReferenceOf(t *Transaction, reversed bool) Reference {
	return Reference{
		TransactionID: t.id, Kind: t.kind, Status: t.status,
		ProviderID: t.provider.ProviderID, PlayerID: t.playerID, WalletID: t.walletID,
		RoundID: t.provider.RoundID, Amount: t.amount, Reversed: reversed,
	}
}

// DecideWithReference is the financial rule for REFUND, ROLLBACK and WIN
// with a reference. A nil ref means the reference does not exist yet.
func DecideWithReference(t *Transaction, available money.Money, ref *Reference) (Decision, error) {
	if t.status.IsTerminal() {
		return Decision{}, fmt.Errorf("%w: %s", ErrTerminal, t.status)
	}
	if t.provider.ReferenceExternalID == "" {
		return Decision{}, fmt.Errorf("%w: operation has no reference", ErrInvalidTransaction)
	}
	if ref == nil || ref.Status == PendingReference {
		return Decision{Status: PendingReference}, nil
	}
	switch ref.Status {
	case Rejected, Failed:
		return rejectWith(ReferenceNotProcessed), nil
	case Processed:
	default:
		return Decision{}, fmt.Errorf("%w: reference status %q", ErrInvalidTransaction, ref.Status)
	}
	if !referenceable(t.kind, ref.Kind) || !sameContext(t, ref) {
		return rejectWith(ReferenceMismatch), nil
	}
	if t.kind == Refund || t.kind == Rollback {
		if c, err := t.amount.Cmp(ref.Amount); err != nil || c != 0 {
			return rejectWith(ReferenceMismatch), nil
		}
		if ref.Reversed {
			return rejectWith(ReversalAlreadyApplied), nil
		}
	}
	effect := effectOf(t.kind, ref.Kind)
	if effect == DebitEffect {
		c, err := available.Cmp(t.amount)
		if err != nil {
			return Decision{}, err
		}
		if c < 0 {
			return rejectWith(ReversalInsufficientFunds), nil
		}
	}
	return Decision{Status: Processed, Effect: effect}, nil
}

func rejectWith(code FailureCode) Decision { return Decision{Status: Rejected, FailureCode: code} }

// referenceable: REFUND reverses a BET; ROLLBACK reverses a BET, WIN or
// REFUND; WIN may point at the BET of its round.
func referenceable(kind, refKind Kind) bool {
	switch kind {
	case Refund, Win:
		return refKind == Bet
	case Rollback:
		return refKind == Bet || refKind == Win || refKind == Refund
	}
	return false
}

func sameContext(t *Transaction, ref *Reference) bool {
	return ref.ProviderID == t.provider.ProviderID &&
		ref.PlayerID == t.playerID &&
		ref.WalletID == t.walletID &&
		ref.RoundID == t.provider.RoundID &&
		ref.Amount.Currency() == t.amount.Currency()
}

// effectOf: ROLLBACK moves opposite to the original movement.
func effectOf(kind, refKind Kind) Effect {
	if kind == Rollback && (refKind == Win || refKind == Refund) {
		return DebitEffect
	}
	return CreditEffect
}
