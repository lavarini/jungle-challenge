package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/lavarini/backend-challenge-go/internal/money"
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
	}
	for name, mutate := range cases {
		c := sampleCommand(t)
		mutate(&c)
		if err := c.validate(); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: error = %v, want ErrInvalidInput", name, err)
		}
	}
}
