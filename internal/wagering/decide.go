package wagering

import (
	"fmt"

	"github.com/lavarini/backend-challenge-go/internal/money"
)

type Effect int

const (
	NoEffect Effect = iota
	DebitEffect
	CreditEffect
)

type Decision struct {
	Status      Status
	FailureCode FailureCode
	Effect      Effect
}

// Decide is the financial rule for operations that do not depend on a
// reference. It performs no I/O. Operations with a reference return
// ErrNeedsReference and are decided with the resolved reference.
func Decide(t *Transaction, available money.Money) (Decision, error) {
	if t.status.IsTerminal() {
		return Decision{}, fmt.Errorf("%w: %s", ErrTerminal, t.status)
	}
	if t.kind.RequiresReference() || t.provider.ReferenceExternalID != "" {
		return Decision{}, ErrNeedsReference
	}
	switch t.kind {
	case Bet:
		c, err := available.Cmp(t.amount)
		if err != nil {
			return Decision{}, err
		}
		if c < 0 {
			return Decision{Status: Rejected, FailureCode: BetInsufficientFunds}, nil
		}
		return Decision{Status: Processed, Effect: DebitEffect}, nil
	case Win:
		return Decision{Status: Processed, Effect: CreditEffect}, nil
	case Loss:
		return Decision{Status: Processed, Effect: NoEffect}, nil
	}
	return Decision{}, fmt.Errorf("%w: %s is not decided here", ErrUnknownKind, t.kind)
}
