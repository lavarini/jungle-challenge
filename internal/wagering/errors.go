package wagering

import "errors"

var (
	ErrUnknownKind          = errors.New("wagering: unknown kind")
	ErrOpeningNotExternal   = errors.New("wagering: OPENING is internal only")
	ErrInvalidTransaction   = errors.New("wagering: invalid transaction")
	ErrInvalidAmountForKind = errors.New("wagering: amount not allowed for kind")
	ErrReferenceRequired    = errors.New("wagering: reference required")
	ErrNeedsReference       = errors.New("wagering: decision depends on a reference")
	ErrTerminal             = errors.New("wagering: transaction is terminal")
	ErrInvalidTransition    = errors.New("wagering: invalid transition")
)
