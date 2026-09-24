package app

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"time"
)

var ErrInboxPayloadMismatch = errors.New("message id reused with a different payload")

// InboxRef identifies one delivered message for one logical consumer.
type InboxRef struct {
	Consumer    string
	MessageID   string
	PayloadHash []byte
}

type InboxRecord struct {
	Consumer      string
	MessageID     string
	PayloadHash   []byte
	TransactionID string
	Outcome       string
	ReceivedAt    time.Time
	CompletedAt   time.Time
}

func (r *InboxRef) validate() error {
	if r.Consumer == "" || r.MessageID == "" || len(r.MessageID) > maxFieldLength || len(r.PayloadHash) != sha256.Size {
		return fmt.Errorf("%w: inbox reference needs consumer, message id and a SHA-256 payload hash", ErrInvalidInput)
	}
	return nil
}
