package events

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lavarini/backend-challenge-go/internal/money"
)

var occurred = time.Date(2026, 9, 24, 12, 0, 0, 0, time.FixedZone("BRT", -3*3600))

func brl(t *testing.T, s string) money.Money {
	t.Helper()
	m, err := money.Parse(s, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func meta() Meta {
	return Meta{EventID: "e1", CorrelationID: "c1", CausationID: "m1", OccurredAt: occurred}
}

func processedData(t *testing.T) TransactionData {
	return TransactionData{
		TransactionID: "t1", Origin: "EXTERNAL", Kind: "BET", Status: "PROCESSED",
		WalletID: "w1", PlayerID: "p1", ProviderID: "provider-a", ExternalTransactionID: "tx-1",
		RoundID: "r1", GameID: "g1", Money: brl(t, "25.00"), Balance: brl(t, "975.00"), WalletVersion: 2,
	}
}

func TestProcessedEnvelopeShape(t *testing.T) {
	e, err := NewWagerTransactionProcessed(meta(), processedData(t))
	if err != nil {
		t.Fatal(err)
	}
	if e.PartitionKey() != "w1" || e.AggregateID() != "t1" || e.EventType() != TypeWagerTransactionProcessed {
		t.Fatalf("routing fields: %s %s %s", e.PartitionKey(), e.AggregateID(), e.EventType())
	}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["occurredAt"] != "2026-09-24T15:00:00Z" {
		t.Fatalf("occurredAt must be RFC 3339 UTC, got %v", got["occurredAt"])
	}
	if got["version"] != float64(1) || got["eventId"] != "e1" || got["causationId"] != "m1" {
		t.Fatalf("envelope: %s", raw)
	}
	data := got["data"].(map[string]any)
	if data["money"].(map[string]any)["amount"] != "25.00" || data["balance"].(map[string]any)["amount"] != "975.00" {
		t.Fatalf("money must be decimal strings: %s", raw)
	}
	if _, ok := got["partitionKey"]; ok {
		t.Fatal("partition key is transport metadata, not payload")
	}
}

func TestRejectedRequiresFailureCode(t *testing.T) {
	d := processedData(t)
	d.Status = "REJECTED"
	if _, err := NewWagerTransactionRejected(meta(), d); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("error = %v", err)
	}
	d.FailureCode = "BET_INSUFFICIENT_FUNDS"
	if _, err := NewWagerTransactionRejected(meta(), d); err != nil {
		t.Fatal(err)
	}
}

func TestConstructorsCheckStatus(t *testing.T) {
	d := processedData(t)
	d.Status = "REJECTED"
	if _, err := NewWagerTransactionProcessed(meta(), d); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("error = %v", err)
	}
}

func TestBalanceChanged(t *testing.T) {
	e, err := NewWalletBalanceChanged(meta(), BalanceChangedData{
		WalletID: "w1", TransactionID: "t1", Direction: "DEBIT",
		Money: brl(t, "25.00"), BalanceBefore: brl(t, "1000.00"), BalanceAfter: brl(t, "975.00"), WalletVersion: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if e.AggregateID() != "w1" || e.PartitionKey() != "w1" {
		t.Fatalf("aggregate %s partition %s", e.AggregateID(), e.PartitionKey())
	}
	raw, _ := json.Marshal(e)
	var got struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(raw, &got)
	for _, k := range []string{"walletId", "transactionId", "direction", "money", "balanceBefore", "balanceAfter", "walletVersion"} {
		if _, ok := got.Data[k]; !ok {
			t.Errorf("WalletBalanceChanged missing %s: %s", k, raw)
		}
	}
	if _, err := NewWalletBalanceChanged(meta(), BalanceChangedData{WalletID: "w1", TransactionID: "t1", Direction: "UP"}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("bad direction error = %v", err)
	}
}

func TestPendingReferenceEvent(t *testing.T) {
	next := time.Date(2026, 9, 24, 12, 0, 1, 0, time.UTC)
	d := PendingReferenceData{
		TransactionID: "t1", Kind: "REFUND", WalletID: "w1", PlayerID: "p1", ProviderID: "provider-a",
		ExternalTransactionID: "tx-2", RoundID: "r1", GameID: "g1", Money: brl(t, "10.00"),
		ReferenceExternalTransactionID: "tx-1", NextAttemptAt: next, DeadlineAt: next.Add(24 * time.Hour),
	}
	e, err := NewWagerTransactionPendingReference(meta(), d)
	if err != nil {
		t.Fatal(err)
	}
	if e.EventType() != TypeWagerTransactionPendingReference || e.AggregateID() != "t1" || e.PartitionKey() != "w1" {
		t.Fatalf("routing: %s %s %s", e.EventType(), e.AggregateID(), e.PartitionKey())
	}
	raw, _ := json.Marshal(e)
	var got struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(raw, &got)
	if got.Data["referenceExternalTransactionId"] != "tx-1" || got.Data["deadlineAt"] != "2026-09-25T12:00:01Z" {
		t.Fatalf("payload: %s", raw)
	}
	d.ReferenceExternalTransactionID = ""
	if _, err := NewWagerTransactionPendingReference(meta(), d); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("missing reference error = %v", err)
	}
}

func TestMetaIsRequired(t *testing.T) {
	m := meta()
	m.EventID = ""
	if _, err := NewWagerTransactionProcessed(m, processedData(t)); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("error = %v", err)
	}
	m = meta()
	m.OccurredAt = time.Time{}
	if _, err := NewWagerTransactionProcessed(m, processedData(t)); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("error = %v", err)
	}
}
