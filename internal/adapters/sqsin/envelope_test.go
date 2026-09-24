package sqsin

import (
	"crypto/sha256"
	"errors"
	"strings"
	"testing"

	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

const validBody = `{"messageId":"msg-123","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00.000Z",` +
	`"data":{"providerId":"provider-a","externalTransactionId":"transaction-123","idempotencyKey":"provider-a:transaction-123",` +
	`"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37",` +
	`"roundId":"round-987","gameId":"fortune-chimp","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}}`

func TestEnvelopeBecomesCommand(t *testing.T) {
	env, err := parseEnvelope(validBody)
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := env.command(validBody)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(validBody))
	if cmd.ProviderID != "provider-a" || cmd.IdempotencyKey != "provider-a:transaction-123" || cmd.Kind != wagering.Bet ||
		cmd.Money.String() != "25.00" || cmd.Source != app.SourceSQS || cmd.CorrelationID != "msg-123" || cmd.CausationID != "msg-123" {
		t.Fatalf("command %+v", cmd)
	}
	if cmd.Inbox == nil || cmd.Inbox.Consumer != ConsumerName || cmd.Inbox.MessageID != "msg-123" || string(cmd.Inbox.PayloadHash) != string(sum[:]) {
		t.Fatalf("inbox %+v", cmd.Inbox)
	}
}

func TestEnvelopeRejectsInvalidMessages(t *testing.T) {
	cases := map[string]string{
		"not json":        `nope`,
		"unknown field":   strings.Replace(validBody, `"type"`, `"extra":1,"type"`, 1),
		"wrong type":      strings.Replace(validBody, "WagerTransactionRequested", "Other", 1),
		"missing message": strings.Replace(validBody, `"messageId":"msg-123",`, "", 1),
		"bad occurredAt":  strings.Replace(validBody, "2026-09-08T12:00:00.000Z", "yesterday", 1),
		"trailing data":   validBody + `{}`,
	}
	for name, body := range cases {
		if _, err := parseEnvelope(body); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	commandCases := map[string]string{
		"opening kind":     strings.Replace(validBody, `"BET"`, `"OPENING"`, 1),
		"non canonical":    strings.Replace(validBody, `"25.00"`, `"25"`, 1),
		"uppercase uuid":   strings.Replace(validBody, "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1", "0192F28F-5DC0-7D58-BDB2-814AD6A0F4A1", 1),
		"missing idem key": strings.Replace(validBody, `"idempotencyKey":"provider-a:transaction-123",`, "", 1),
	}
	for name, body := range commandCases {
		env, err := parseEnvelope(body)
		if err != nil {
			t.Errorf("%s: envelope rejected early: %v", name, err)
			continue
		}
		if _, err := env.command(body); !errors.Is(err, app.ErrInvalidInput) {
			t.Errorf("%s: error = %v", name, err)
		}
	}
}
