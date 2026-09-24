package app

import (
	"fmt"

	"github.com/lavarini/backend-challenge-go/internal/money"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

type Source string

const (
	SourceHTTP Source = "http"
	SourceSQS  Source = "sqs"
)

const maxFieldLength = 255

// SubmitCommand is the transport-independent form of a provider operation.
// Edges normalize UUIDs to lowercase before building it.
type SubmitCommand struct {
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PlayerID                       string
	WalletID                       string
	RoundID                        string
	GameID                         string
	Kind                           wagering.Kind
	Money                          money.Money
	ReferenceExternalTransactionID string
	CorrelationID                  string
	CausationID                    string
	Source                         Source
}

type SubmitResult struct {
	TransactionID    string
	Status           wagering.Status
	FailureCode      wagering.FailureCode
	Balance          money.Money
	WalletVersion    int64
	IdempotentReplay bool
}

func (c SubmitCommand) validate() error {
	required := map[string]string{
		"providerId": c.ProviderID, "externalTransactionId": c.ExternalTransactionID,
		"idempotencyKey": c.IdempotencyKey, "playerId": c.PlayerID, "walletId": c.WalletID,
		"roundId": c.RoundID, "gameId": c.GameID, "correlationId": c.CorrelationID, "kind": string(c.Kind),
	}
	for field, v := range required {
		if v == "" {
			return fmt.Errorf("%w: %s is required", ErrInvalidInput, field)
		}
		if len(v) > maxFieldLength {
			return fmt.Errorf("%w: %s exceeds %d characters", ErrInvalidInput, field, maxFieldLength)
		}
	}
	if len(c.ReferenceExternalTransactionID) > maxFieldLength {
		return fmt.Errorf("%w: referenceExternalTransactionId exceeds %d characters", ErrInvalidInput, maxFieldLength)
	}
	if !c.Money.Valid() {
		return fmt.Errorf("%w: money is required", ErrInvalidInput)
	}
	return nil
}
