package app

import (
	"bytes"
	"testing"

	"github.com/lavarini/backend-challenge-go/internal/money"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

func sampleCommand(t *testing.T) SubmitCommand {
	t.Helper()
	m, err := money.Parse("25.00", "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return SubmitCommand{
		ProviderID: "provider-a", ExternalTransactionID: "transaction-123", IdempotencyKey: "provider-a:transaction-123",
		PlayerID: "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1", WalletID: "0192f291-27dd-7d3f-8071-5f8685deef37",
		RoundID: "round-987", GameID: "fortune-chimp", Kind: wagering.Bet, Money: m,
		CorrelationID: "corr-1", CausationID: "msg-1", Source: SourceHTTP,
	}
}

func TestCanonicalJSONIsSortedAndExcludesTransport(t *testing.T) {
	got, err := canonicalJSON(sampleCommand(t))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"externalTransactionId":"transaction-123","gameId":"fortune-chimp","kind":"BET",` +
		`"money":{"amount":"25.00","currency":"BRL"},"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",` +
		`"providerId":"provider-a","roundId":"round-987","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37"}`
	if string(got) != want {
		t.Fatalf("canonical JSON\n got %s\nwant %s", got, want)
	}
}

func TestHashIsEqualAcrossTransports(t *testing.T) {
	httpCmd := sampleCommand(t)
	sqsCmd := httpCmd
	sqsCmd.Source, sqsCmd.CorrelationID, sqsCmd.CausationID = SourceSQS, "other", "msg-9"
	sqsCmd.IdempotencyKey = "another-key"
	a, _ := CanonicalHash(httpCmd)
	b, _ := CanonicalHash(sqsCmd)
	if !bytes.Equal(a, b) || len(a) != 32 {
		t.Fatalf("hashes differ across transports")
	}
}

func TestHashChangesWithAnyBusinessField(t *testing.T) {
	base, _ := CanonicalHash(sampleCommand(t))
	mutations := map[string]func(c *SubmitCommand){
		"provider": func(c *SubmitCommand) { c.ProviderID = "provider-b" },
		"external": func(c *SubmitCommand) { c.ExternalTransactionID = "x" },
		"player":   func(c *SubmitCommand) { c.PlayerID = "p" },
		"wallet":   func(c *SubmitCommand) { c.WalletID = "w" },
		"round":    func(c *SubmitCommand) { c.RoundID = "r" },
		"game":     func(c *SubmitCommand) { c.GameID = "g" },
		"kind":     func(c *SubmitCommand) { c.Kind = wagering.Win },
		"amount":   func(c *SubmitCommand) { c.Money, _ = money.Parse("25.01", "BRL") },
		"currency": func(c *SubmitCommand) { c.Money, _ = money.Parse("25.00", "USD") },
		"ref":      func(c *SubmitCommand) { c.ReferenceExternalTransactionID = "bet-1" },
	}
	for name, mutate := range mutations {
		c := sampleCommand(t)
		mutate(&c)
		h, _ := CanonicalHash(c)
		if bytes.Equal(h, base) {
			t.Errorf("changing %s did not change the hash", name)
		}
	}
}
