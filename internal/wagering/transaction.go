// Package wagering models provider operations, their state machine and the
// pure financial decision for each kind.
package wagering

import (
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/lavarini/backend-challenge-go/internal/money"
)

// ProviderRef is the external identity of an operation. Empty for OPENING.
type ProviderRef struct {
	ProviderID          string
	ExternalID          string
	IdempotencyKey      string
	PayloadHash         []byte
	RoundID             string
	GameID              string
	ReferenceExternalID string
}

type Transaction struct {
	id                  string
	origin              Origin
	kind                Kind
	status              Status
	walletID, playerID  string
	amount              money.Money
	provider            ProviderRef
	referenceTxID       string
	failureCode         FailureCode
	resultBalance       money.Money
	resultWalletVersion int64
	correlationID       string
	attempts            int
	nextAttemptAt       time.Time
	deadlineAt          time.Time
	createdAt           time.Time
	updatedAt           time.Time
	completedAt         time.Time
}

type NewExternalParams struct {
	ID, WalletID, PlayerID, CorrelationID string
	Kind                                  Kind
	Amount                                money.Money
	Provider                              ProviderRef
	Now                                   time.Time
}

// NewExternal builds a provider operation in the in-memory PENDING state.
func NewExternal(p NewExternalParams) (*Transaction, error) {
	if p.ID == "" || p.WalletID == "" || p.PlayerID == "" || p.CorrelationID == "" || p.Now.IsZero() {
		return nil, fmt.Errorf("%w: missing identity or timestamp", ErrInvalidTransaction)
	}
	if p.Kind == Opening {
		return nil, ErrOpeningNotExternal
	}
	if _, err := ParseExternalKind(string(p.Kind)); err != nil {
		return nil, err
	}
	r := p.Provider
	if r.ProviderID == "" || r.ExternalID == "" || r.IdempotencyKey == "" || r.RoundID == "" || r.GameID == "" || len(r.PayloadHash) != sha256.Size {
		return nil, fmt.Errorf("%w: missing provider identity", ErrInvalidTransaction)
	}
	if err := checkAmount(p.Kind, p.Amount); err != nil {
		return nil, err
	}
	if p.Kind.RequiresReference() && r.ReferenceExternalID == "" {
		return nil, fmt.Errorf("%w: %s", ErrReferenceRequired, p.Kind)
	}
	r.PayloadHash = append([]byte(nil), r.PayloadHash...)
	now := p.Now.UTC()
	return &Transaction{
		id: p.ID, origin: OriginExternal, kind: p.Kind, status: Pending,
		walletID: p.WalletID, playerID: p.PlayerID, amount: p.Amount, provider: r,
		correlationID: p.CorrelationID, createdAt: now, updatedAt: now,
	}, nil
}

// NewOpening builds the internal credit that records a positive initial balance.
func NewOpening(id, walletID, playerID, correlationID string, amount money.Money, now time.Time) (*Transaction, error) {
	if id == "" || walletID == "" || playerID == "" || correlationID == "" || now.IsZero() {
		return nil, fmt.Errorf("%w: missing identity or timestamp", ErrInvalidTransaction)
	}
	if err := checkAmount(Opening, amount); err != nil {
		return nil, err
	}
	now = now.UTC()
	return &Transaction{
		id: id, origin: OriginInternal, kind: Opening, status: Pending,
		walletID: walletID, playerID: playerID, amount: amount,
		correlationID: correlationID, createdAt: now, updatedAt: now,
	}, nil
}

func checkAmount(k Kind, m money.Money) error {
	if !m.Valid() {
		return fmt.Errorf("%w: missing amount", ErrInvalidAmountForKind)
	}
	if k == Loss {
		if !m.IsZero() {
			return fmt.Errorf("%w: LOSS requires 0.00", ErrInvalidAmountForKind)
		}
		return nil
	}
	if !m.IsPositive() {
		return fmt.Errorf("%w: %s requires a positive amount", ErrInvalidAmountForKind, k)
	}
	return nil
}

// Process records a successful conclusion and the wallet state observed by it.
func (t *Transaction) Process(balance money.Money, walletVersion int64, now time.Time) error {
	return t.complete(Processed, "", balance, walletVersion, now)
}

// Reject records a definitive business rejection and the observed wallet state.
func (t *Transaction) Reject(code FailureCode, balance money.Money, walletVersion int64, now time.Time) error {
	if code == "" {
		return fmt.Errorf("%w: rejection requires a failure code", ErrInvalidTransition)
	}
	return t.complete(Rejected, code, balance, walletVersion, now)
}

func (t *Transaction) complete(to Status, code FailureCode, balance money.Money, walletVersion int64, now time.Time) error {
	if t.status.IsTerminal() {
		return fmt.Errorf("%w: %s", ErrTerminal, t.status)
	}
	if !balance.Valid() || balance.Currency() != t.amount.Currency() || walletVersion < 1 || now.IsZero() {
		return fmt.Errorf("%w: invalid result snapshot", ErrInvalidTransition)
	}
	now = now.UTC()
	t.status, t.failureCode = to, code
	t.resultBalance, t.resultWalletVersion = balance, walletVersion
	t.completedAt, t.updatedAt = now, now
	return nil
}

// AwaitReference persists the dependency on a reference that is not available yet.
func (t *Transaction) AwaitReference(nextAttemptAt, deadlineAt, now time.Time) error {
	if t.status != Pending {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, t.status, PendingReference)
	}
	if t.provider.ReferenceExternalID == "" {
		return fmt.Errorf("%w: no reference to wait for", ErrInvalidTransition)
	}
	if nextAttemptAt.IsZero() || deadlineAt.IsZero() || now.IsZero() {
		return fmt.Errorf("%w: missing schedule", ErrInvalidTransition)
	}
	t.status = PendingReference
	t.attempts = 0
	t.nextAttemptAt, t.deadlineAt, t.updatedAt = nextAttemptAt.UTC(), deadlineAt.UTC(), now.UTC()
	return nil
}

// Fail records a permanent failure of an asynchronous resolution (ADR 0011).
func (t *Transaction) Fail(code FailureCode, now time.Time) error {
	if t.status != PendingReference {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, t.status, Failed)
	}
	if code == "" || now.IsZero() {
		return fmt.Errorf("%w: failure requires code and timestamp", ErrInvalidTransition)
	}
	now = now.UTC()
	t.status, t.failureCode = Failed, code
	t.completedAt, t.updatedAt = now, now
	return nil
}

func (t *Transaction) ID() string               { return t.id }
func (t *Transaction) Origin() Origin           { return t.origin }
func (t *Transaction) Kind() Kind               { return t.kind }
func (t *Transaction) Status() Status           { return t.status }
func (t *Transaction) WalletID() string         { return t.walletID }
func (t *Transaction) PlayerID() string         { return t.playerID }
func (t *Transaction) Amount() money.Money      { return t.amount }
func (t *Transaction) FailureCode() FailureCode { return t.failureCode }
func (t *Transaction) CorrelationID() string    { return t.correlationID }
func (t *Transaction) CreatedAt() time.Time     { return t.createdAt }
func (t *Transaction) CompletedAt() time.Time   { return t.completedAt }

func (t *Transaction) Provider() ProviderRef {
	r := t.provider
	r.PayloadHash = append([]byte(nil), r.PayloadHash...)
	return r
}

// Result returns the wallet balance and version observed at conclusion.
func (t *Transaction) Result() (money.Money, int64, bool) {
	return t.resultBalance, t.resultWalletVersion, t.resultWalletVersion > 0
}

type Snapshot struct {
	ID                  string
	Origin              Origin
	Kind                Kind
	Status              Status
	WalletID, PlayerID  string
	Amount              money.Money
	Provider            ProviderRef
	ReferenceTxID       string
	FailureCode         FailureCode
	ResultBalance       money.Money
	ResultWalletVersion int64
	CorrelationID       string
	Attempts            int
	NextAttemptAt       time.Time
	DeadlineAt          time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
	CompletedAt         time.Time
}

func (t *Transaction) Snapshot() Snapshot {
	return Snapshot{
		ID: t.id, Origin: t.origin, Kind: t.kind, Status: t.status,
		WalletID: t.walletID, PlayerID: t.playerID, Amount: t.amount, Provider: t.Provider(),
		ReferenceTxID: t.referenceTxID, FailureCode: t.failureCode,
		ResultBalance: t.resultBalance, ResultWalletVersion: t.resultWalletVersion,
		CorrelationID: t.correlationID, Attempts: t.attempts,
		NextAttemptAt: t.nextAttemptAt, DeadlineAt: t.deadlineAt,
		CreatedAt: t.createdAt, UpdatedAt: t.updatedAt, CompletedAt: t.completedAt,
	}
}

// Rehydrate restores persisted state without re-running transitions.
// PENDING is never persisted, so it is rejected here.
func Rehydrate(s Snapshot) (*Transaction, error) {
	switch s.Status {
	case PendingReference, Processed, Rejected, Failed:
	default:
		return nil, fmt.Errorf("%w: status %q is not persistable", ErrInvalidTransaction, s.Status)
	}
	switch s.Origin {
	case OriginInternal:
		if s.Kind != Opening {
			return nil, fmt.Errorf("%w: internal %s", ErrInvalidTransaction, s.Kind)
		}
	case OriginExternal:
		if _, err := ParseExternalKind(string(s.Kind)); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidTransaction, err)
		}
	default:
		return nil, fmt.Errorf("%w: origin %q", ErrInvalidTransaction, s.Origin)
	}
	if s.ID == "" || s.WalletID == "" || s.PlayerID == "" || s.CorrelationID == "" || !s.Amount.Valid() || s.CreatedAt.IsZero() {
		return nil, fmt.Errorf("%w: missing fields", ErrInvalidTransaction)
	}
	if s.Status.IsTerminal() && s.CompletedAt.IsZero() {
		return nil, fmt.Errorf("%w: terminal without completion time", ErrInvalidTransaction)
	}
	if (s.Status == Rejected || s.Status == Failed) && s.FailureCode == "" {
		return nil, fmt.Errorf("%w: %s without failure code", ErrInvalidTransaction, s.Status)
	}
	p := s.Provider
	p.PayloadHash = append([]byte(nil), p.PayloadHash...)
	return &Transaction{
		id: s.ID, origin: s.Origin, kind: s.Kind, status: s.Status,
		walletID: s.WalletID, playerID: s.PlayerID, amount: s.Amount, provider: p,
		referenceTxID: s.ReferenceTxID, failureCode: s.FailureCode,
		resultBalance: s.ResultBalance, resultWalletVersion: s.ResultWalletVersion,
		correlationID: s.CorrelationID, attempts: s.Attempts,
		nextAttemptAt: s.NextAttemptAt, deadlineAt: s.DeadlineAt,
		createdAt: s.CreatedAt, updatedAt: s.UpdatedAt, completedAt: s.CompletedAt,
	}, nil
}
