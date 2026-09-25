package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/lavarini/backend-challenge-go/internal/money"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

func TestSubmitCommandValidation(t *testing.T) {
	if err := sampleCommand(t).validate(); err != nil {
		t.Fatalf("valid command rejected: %v", err)
	}
	long := strings.Repeat("x", 256)
	cases := map[string]func(c *SubmitCommand){
		"missing provider":    func(c *SubmitCommand) { c.ProviderID = "" },
		"missing external":    func(c *SubmitCommand) { c.ExternalTransactionID = "" },
		"missing key":         func(c *SubmitCommand) { c.IdempotencyKey = "" },
		"missing wallet":      func(c *SubmitCommand) { c.WalletID = "" },
		"missing round":       func(c *SubmitCommand) { c.RoundID = "" },
		"missing correlation": func(c *SubmitCommand) { c.CorrelationID = "" },
		"missing kind":        func(c *SubmitCommand) { c.Kind = "" },
		"missing money":       func(c *SubmitCommand) { c.Money = money.Money{} },
		"key too long":        func(c *SubmitCommand) { c.IdempotencyKey = long },
		"game too long":       func(c *SubmitCommand) { c.GameID = long },
		"bet with reference":  func(c *SubmitCommand) { c.Kind = wagering.Bet; c.ReferenceExternalTransactionID = "tx-1" },
		"loss with reference": func(c *SubmitCommand) { c.Kind = wagering.Loss; c.ReferenceExternalTransactionID = "tx-1" },
	}
	for name, mutate := range cases {
		c := sampleCommand(t)
		mutate(&c)
		if err := c.validate(); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: error = %v, want ErrInvalidInput", name, err)
		}
	}
}

func TestSubmitCommandValidatesInbox(t *testing.T) {
	c := sampleCommand(t)
	c.Inbox = &InboxRef{Consumer: "wager-transactions-consumer", MessageID: "msg-1", PayloadHash: make([]byte, 32)}
	if err := c.validate(); err != nil {
		t.Fatalf("valid inbox rejected: %v", err)
	}
	c.Inbox.PayloadHash = []byte{1}
	if err := c.validate(); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("short hash error = %v", err)
	}
	c.Inbox = &InboxRef{Consumer: "wager-transactions-consumer", PayloadHash: make([]byte, 32)}
	if err := c.validate(); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing message id error = %v", err)
	}
}

// Only kinds whose rule reads a reference may carry one: REFUND and ROLLBACK
// require it, WIN may name its BET (design.md §3). BET and LOSS never use one, so a
// reference there is a correctable input error, not a 24h pending operation.
func TestOnlyReferenceKindsMayCarryAReference(t *testing.T) {
	for _, k := range []wagering.Kind{wagering.Win, wagering.Refund, wagering.Rollback} {
		c := sampleCommand(t)
		c.Kind, c.ReferenceExternalTransactionID = k, "tx-1"
		if err := c.validate(); err != nil {
			t.Errorf("%s with a reference rejected: %v", k, err)
		}
	}
	c := sampleCommand(t)
	c.Kind = wagering.Win
	if err := c.validate(); err != nil {
		t.Errorf("WIN without a reference rejected: %v", err)
	}
}

func TestMaxFieldLengthIsExported(t *testing.T) {
	c := sampleCommand(t)
	c.RoundID = strings.Repeat("x", MaxFieldLength)
	if err := c.validate(); err != nil {
		t.Fatalf("a field of exactly MaxFieldLength rejected: %v", err)
	}
}
