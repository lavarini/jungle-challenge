package sqsin

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lavarini/backend-challenge-go/internal/adapters/wire"
	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

const (
	ConsumerName = "wager-transactions-consumer"
	MessageType  = "WagerTransactionRequested"
)

var errInvalidEnvelope = errors.New("invalid envelope")

type envelope struct {
	MessageID  string       `json:"messageId"`
	Type       string       `json:"type"`
	OccurredAt string       `json:"occurredAt"`
	Data       envelopeData `json:"data"`
}

type envelopeData struct {
	ProviderID                     string     `json:"providerId"`
	ExternalTransactionID          string     `json:"externalTransactionId"`
	IdempotencyKey                 string     `json:"idempotencyKey"`
	PlayerID                       string     `json:"playerId"`
	WalletID                       string     `json:"walletId"`
	RoundID                        string     `json:"roundId"`
	GameID                         string     `json:"gameId"`
	Kind                           string     `json:"kind"`
	Money                          wire.Money `json:"money"`
	ReferenceExternalTransactionID string     `json:"referenceExternalTransactionId,omitempty"`
}

// parseEnvelope accepts exactly one envelope object with known fields only.
func parseEnvelope(body string) (envelope, error) {
	var e envelope
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return envelope{}, fmt.Errorf("%w: %v", errInvalidEnvelope, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return envelope{}, fmt.Errorf("%w: trailing data", errInvalidEnvelope)
	}
	if e.Type != MessageType {
		return envelope{}, fmt.Errorf("%w: type %q", errInvalidEnvelope, e.Type)
	}
	if e.MessageID == "" || len(e.MessageID) > 128 {
		return envelope{}, fmt.Errorf("%w: messageId is required (at most 128 characters)", errInvalidEnvelope)
	}
	if _, err := time.Parse(time.RFC3339Nano, e.OccurredAt); err != nil {
		return envelope{}, fmt.Errorf("%w: occurredAt must be RFC 3339", errInvalidEnvelope)
	}
	return e, nil
}

// command builds the same command HTTP builds, plus the inbox reference. The
// idempotency key is the one the producer sent; it is never recomputed.
func (e envelope) command(body string) (app.SubmitCommand, error) {
	d := e.Data
	kind, err := wagering.ParseExternalKind(d.Kind)
	if err != nil {
		return app.SubmitCommand{}, fmt.Errorf("%w: kind: %w", app.ErrInvalidInput, err)
	}
	playerID, err := wire.ParseUUID("playerId", d.PlayerID)
	if err != nil {
		return app.SubmitCommand{}, err
	}
	walletID, err := wire.ParseUUID("walletId", d.WalletID)
	if err != nil {
		return app.SubmitCommand{}, err
	}
	m, err := d.Money.Parse("money")
	if err != nil {
		return app.SubmitCommand{}, err
	}
	if d.IdempotencyKey == "" {
		return app.SubmitCommand{}, fmt.Errorf("%w: data.idempotencyKey is required", app.ErrInvalidInput)
	}
	sum := sha256.Sum256([]byte(body))
	return app.SubmitCommand{
		ProviderID: d.ProviderID, ExternalTransactionID: d.ExternalTransactionID, IdempotencyKey: d.IdempotencyKey,
		PlayerID: playerID, WalletID: walletID, RoundID: d.RoundID, GameID: d.GameID, Kind: kind, Money: m,
		ReferenceExternalTransactionID: d.ReferenceExternalTransactionID,
		CorrelationID:                  e.MessageID, CausationID: e.MessageID, Source: app.SourceSQS,
		Inbox: &app.InboxRef{Consumer: ConsumerName, MessageID: e.MessageID, PayloadHash: sum[:]},
	}, nil
}
