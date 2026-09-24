package app

import (
	"fmt"
	"time"

	"github.com/lavarini/backend-challenge-go/internal/events"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
	"github.com/lavarini/backend-challenge-go/internal/wallet"
)

type eventFactory struct {
	ids IDGenerator
}

func (f eventFactory) meta(correlationID, causationID string, now time.Time) events.Meta {
	return events.Meta{EventID: f.ids.New(), CorrelationID: correlationID, CausationID: causationID, OccurredAt: now}
}

func transactionData(t *wagering.Transaction) events.TransactionData {
	balance, version, _ := t.Result()
	p := t.Provider()
	return events.TransactionData{
		TransactionID: t.ID(), Origin: string(t.Origin()), Kind: string(t.Kind()), Status: string(t.Status()),
		WalletID: t.WalletID(), PlayerID: t.PlayerID(), ProviderID: p.ProviderID,
		ExternalTransactionID: p.ExternalID, RoundID: p.RoundID, GameID: p.GameID, Money: t.Amount(),
		ReferenceExternalTransactionID: p.ReferenceExternalID, FailureCode: string(t.FailureCode()),
		Balance: balance, WalletVersion: version,
	}
}

// outcome returns the events of a concluded transaction in publication order:
// the transaction outcome first, then the balance change when there is one.
func (f eventFactory) outcome(t *wagering.Transaction, entry *wallet.LedgerEntry, causationID string, now time.Time) ([]events.Envelope, error) {
	var out []events.Envelope
	m := f.meta(t.CorrelationID(), causationID, now)
	switch t.Status() {
	case wagering.Processed:
		e, err := events.NewWagerTransactionProcessed(m, transactionData(t))
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	case wagering.Rejected:
		e, err := events.NewWagerTransactionRejected(m, transactionData(t))
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	default:
		return nil, fmt.Errorf("%w: no outcome event for status %s", ErrInvariantViolation, t.Status())
	}
	if entry != nil {
		e, err := events.NewWalletBalanceChanged(f.meta(t.CorrelationID(), causationID, now), events.BalanceChangedData{
			WalletID: entry.WalletID(), TransactionID: entry.TransactionID(), Direction: string(entry.Direction()),
			Money: entry.Amount(), BalanceBefore: entry.BalanceBefore(), BalanceAfter: entry.BalanceAfter(),
			WalletVersion: entry.WalletVersion(),
		})
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func (f eventFactory) pendingReference(t *wagering.Transaction, causationID string, now time.Time) (events.Envelope, error) {
	p := t.Provider()
	return events.NewWagerTransactionPendingReference(f.meta(t.CorrelationID(), causationID, now), events.PendingReferenceData{
		TransactionID: t.ID(), Kind: string(t.Kind()), WalletID: t.WalletID(), PlayerID: t.PlayerID(),
		ProviderID: p.ProviderID, ExternalTransactionID: p.ExternalID, RoundID: p.RoundID, GameID: p.GameID,
		Money: t.Amount(), ReferenceExternalTransactionID: p.ReferenceExternalID, Attempts: t.Attempts(),
		NextAttemptAt: t.NextAttemptAt(), DeadlineAt: t.DeadlineAt(),
	})
}
