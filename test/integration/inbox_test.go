//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

func viaInbox(c app.SubmitCommand, messageID, body string) app.SubmitCommand {
	sum := sha256.Sum256([]byte(body))
	c.Source, c.CausationID = app.SourceSQS, messageID
	c.Inbox = &app.InboxRef{Consumer: "wager-transactions-consumer", MessageID: messageID, PayloadHash: sum[:]}
	return c
}

func TestRedeliveredMessageIsAnsweredFromTheInbox(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	w := openWallet(t, s, "100.00")
	msg := uuid.NewString()
	c := viaInbox(command(t, w, wagering.Bet, "10.00", uuid.NewString()), msg, "body-1")

	first, err := s.submit.Execute(ctx, c)
	if err != nil || first.IdempotentReplay || first.FromInbox {
		t.Fatalf("first %+v %v", first, err)
	}
	again, err := s.submit.Execute(ctx, c)
	if err != nil || !again.IdempotentReplay || !again.FromInbox || again.TransactionID != first.TransactionID || again.Balance.String() != "90.00" {
		t.Fatalf("redelivery %+v %v", again, err)
	}
	if n := count(t, s.pool, `SELECT count(*) FROM inbox_messages WHERE message_id = $1 AND transaction_id = $2 AND outcome = 'PROCESSED'`, msg, first.TransactionID); n != 1 {
		t.Fatalf("inbox rows = %d", n)
	}
	if n := count(t, s.pool, `SELECT count(*) FROM wallet_ledger_entries WHERE transaction_id = $1`, first.TransactionID); n != 1 {
		t.Fatalf("ledger entries = %d", n)
	}
}

func TestMessageIDReusedWithAnotherBodyIsRefused(t *testing.T) {
	s := newStack(t)
	w := openWallet(t, s, "100.00")
	msg := uuid.NewString()
	if _, err := s.submit.Execute(context.Background(), viaInbox(command(t, w, wagering.Bet, "10.00", uuid.NewString()), msg, "body-1")); err != nil {
		t.Fatal(err)
	}
	_, err := s.submit.Execute(context.Background(), viaInbox(command(t, w, wagering.Bet, "10.00", uuid.NewString()), msg, "body-2"))
	if !errors.Is(err, app.ErrInboxPayloadMismatch) {
		t.Fatalf("error = %v, want ErrInboxPayloadMismatch", err)
	}
}

// A new message carrying an operation already applied (by HTTP or by another
// message) is a financial replay, and its inbox row is recorded too.
func TestNewMessageForAnAppliedOperationIsAReplay(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	w := openWallet(t, s, "100.00")
	c := command(t, w, wagering.Bet, "10.00", uuid.NewString())
	httpRes, err := s.submit.Execute(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	msg := uuid.NewString()
	sqsRes, err := s.submit.Execute(ctx, viaInbox(c, msg, "body"))
	if err != nil || !sqsRes.IdempotentReplay || sqsRes.FromInbox || sqsRes.TransactionID != httpRes.TransactionID {
		t.Fatalf("sqs after http %+v %v", sqsRes, err)
	}
	if n := count(t, s.pool, `SELECT count(*) FROM inbox_messages WHERE message_id = $1`, msg); n != 1 {
		t.Fatalf("inbox rows = %d", n)
	}
	if got := balanceOf(t, s, w.ID); got != "90.00" {
		t.Fatalf("balance %s", got)
	}
}

func TestConcurrentDeliveriesOfOneMessageApplyOnce(t *testing.T) {
	a, b := newStack(t), newStack(t)
	w := openWallet(t, a, "100.00")
	c := viaInbox(command(t, w, wagering.Bet, "10.00", uuid.NewString()), uuid.NewString(), "body")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, st := range []stack{a, b} {
		wg.Add(1)
		go func(i int, st stack) {
			defer wg.Done()
			_, errs[i] = st.submit.Execute(context.Background(), c)
		}(i, st)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := balanceOf(t, a, w.ID); got != "90.00" {
		t.Fatalf("balance %s", got)
	}
}

func TestFailedHandlingRecordsNoInbox(t *testing.T) {
	s := newStack(t)
	w := openWallet(t, s, "100.00")
	c := command(t, w, wagering.Bet, "10.00", uuid.NewString())
	c.WalletID = uuid.NewString()
	msg := uuid.NewString()
	if _, err := s.submit.Execute(context.Background(), viaInbox(c, msg, "body")); !errors.Is(err, app.ErrWalletNotFound) {
		t.Fatalf("error = %v", err)
	}
	if n := count(t, s.pool, `SELECT count(*) FROM inbox_messages WHERE message_id = $1`, msg); n != 0 {
		t.Fatalf("corrigible failure recorded %d inbox rows", n)
	}
}
