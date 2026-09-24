// Package events defines the integration events written to the outbox. Each
// constructor fixes type and version; the envelope is an immutable snapshot.
package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lavarini/backend-challenge-go/internal/money"
)

const (
	TypeWagerTransactionProcessed        = "WagerTransactionProcessed"
	TypeWagerTransactionRejected         = "WagerTransactionRejected"
	TypeWalletBalanceChanged             = "WalletBalanceChanged"
	TypeWagerTransactionPendingReference = "WagerTransactionPendingReference"

	schemaVersion = 1
)

var ErrInvalidEvent = errors.New("events: invalid event")

type Meta struct {
	EventID       string
	CorrelationID string
	CausationID   string
	OccurredAt    time.Time
}

type TransactionData struct {
	TransactionID                  string      `json:"transactionId"`
	Origin                         string      `json:"origin"`
	Kind                           string      `json:"kind"`
	Status                         string      `json:"status"`
	WalletID                       string      `json:"walletId"`
	PlayerID                       string      `json:"playerId"`
	ProviderID                     string      `json:"providerId,omitempty"`
	ExternalTransactionID          string      `json:"externalTransactionId,omitempty"`
	RoundID                        string      `json:"roundId,omitempty"`
	GameID                         string      `json:"gameId,omitempty"`
	Money                          money.Money `json:"money"`
	ReferenceExternalTransactionID string      `json:"referenceExternalTransactionId,omitempty"`
	FailureCode                    string      `json:"failureCode,omitempty"`
	Balance                        money.Money `json:"balance"`
	WalletVersion                  int64       `json:"walletVersion"`
}

type BalanceChangedData struct {
	WalletID      string      `json:"walletId"`
	TransactionID string      `json:"transactionId"`
	Direction     string      `json:"direction"`
	Money         money.Money `json:"money"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	WalletVersion int64       `json:"walletVersion"`
}

// Envelope is immutable: fields are private and data is held by value.
type Envelope struct {
	eventID       string
	eventType     string
	aggregateID   string
	partitionKey  string
	correlationID string
	causationID   string
	occurredAt    time.Time
	version       int
	data          any
}

func NewWagerTransactionProcessed(m Meta, d TransactionData) (Envelope, error) {
	if d.Status != "PROCESSED" {
		return Envelope{}, fmt.Errorf("%w: processed event with status %q", ErrInvalidEvent, d.Status)
	}
	return build(m, TypeWagerTransactionProcessed, d.TransactionID, d.WalletID, d)
}

func NewWagerTransactionRejected(m Meta, d TransactionData) (Envelope, error) {
	if d.Status != "REJECTED" || d.FailureCode == "" {
		return Envelope{}, fmt.Errorf("%w: rejected event needs status REJECTED and a failure code", ErrInvalidEvent)
	}
	return build(m, TypeWagerTransactionRejected, d.TransactionID, d.WalletID, d)
}

func NewWalletBalanceChanged(m Meta, d BalanceChangedData) (Envelope, error) {
	if d.Direction != "DEBIT" && d.Direction != "CREDIT" {
		return Envelope{}, fmt.Errorf("%w: direction %q", ErrInvalidEvent, d.Direction)
	}
	return build(m, TypeWalletBalanceChanged, d.WalletID, d.WalletID, d)
}

func build(m Meta, eventType, aggregateID, partitionKey string, data any) (Envelope, error) {
	if m.EventID == "" || m.CorrelationID == "" || m.OccurredAt.IsZero() {
		return Envelope{}, fmt.Errorf("%w: missing metadata", ErrInvalidEvent)
	}
	if aggregateID == "" || partitionKey == "" {
		return Envelope{}, fmt.Errorf("%w: missing aggregate or partition", ErrInvalidEvent)
	}
	return Envelope{
		eventID: m.EventID, eventType: eventType, aggregateID: aggregateID, partitionKey: partitionKey,
		correlationID: m.CorrelationID, causationID: m.CausationID,
		occurredAt: m.OccurredAt.UTC(), version: schemaVersion, data: data,
	}, nil
}

func (e Envelope) EventID() string       { return e.eventID }
func (e Envelope) EventType() string     { return e.eventType }
func (e Envelope) AggregateID() string   { return e.aggregateID }
func (e Envelope) PartitionKey() string  { return e.partitionKey }
func (e Envelope) OccurredAt() time.Time { return e.occurredAt }

func (e Envelope) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		EventID       string    `json:"eventId"`
		EventType     string    `json:"eventType"`
		AggregateID   string    `json:"aggregateId"`
		CorrelationID string    `json:"correlationId"`
		CausationID   string    `json:"causationId,omitempty"`
		OccurredAt    time.Time `json:"occurredAt"`
		Version       int       `json:"version"`
		Data          any       `json:"data"`
	}{e.eventID, e.eventType, e.aggregateID, e.correlationID, e.causationID, e.occurredAt, e.version, e.data})
}
