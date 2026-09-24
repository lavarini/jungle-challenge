package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

type SubmitWager struct {
	uow     UnitOfWork
	clock   Clock
	ids     IDGenerator
	settler settler
}

func NewSubmitWager(uow UnitOfWork, clock Clock, ids IDGenerator, policy ReferencePolicy) *SubmitWager {
	return &SubmitWager{uow: uow, clock: clock, ids: ids, settler: newSettler(ids, policy)}
}

// Execute applies a provider operation exactly once. HTTP and SQS share it
// (ADR 0008). A concurrent insert of the same identity is retried once and
// resolves as a replay.
func (s *SubmitWager) Execute(ctx context.Context, cmd SubmitCommand) (SubmitResult, error) {
	if err := cmd.validate(); err != nil {
		return SubmitResult{}, err
	}
	hash, err := CanonicalHash(cmd)
	if err != nil {
		return SubmitResult{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	var result SubmitResult
	for attempt := 0; ; attempt++ {
		err = s.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
			if cmd.Inbox != nil {
				res, handled, err := s.fromInbox(ctx, tx, cmd.Inbox)
				if err != nil || handled {
					result = res
					return err
				}
			}
			var inner error
			result, inner = s.submit(ctx, tx, cmd, hash)
			if inner != nil || cmd.Inbox == nil {
				return inner
			}
			now := s.clock.Now()
			return tx.Inbox().Insert(ctx, InboxRecord{
				Consumer: cmd.Inbox.Consumer, MessageID: cmd.Inbox.MessageID, PayloadHash: cmd.Inbox.PayloadHash,
				TransactionID: result.TransactionID, Outcome: string(result.Status), ReceivedAt: now, CompletedAt: now,
			})
		})
		if errors.Is(err, ErrUniqueConflict) && attempt == 0 {
			continue
		}
		return result, err
	}
}

// fromInbox answers a redelivered message from the inbox. A message id reused
// with another body is refused; the financial idempotency still guards a new
// message id carrying an operation already applied.
func (s *SubmitWager) fromInbox(ctx context.Context, tx Tx, ref *InboxRef) (SubmitResult, bool, error) {
	rec, err := tx.Inbox().Find(ctx, ref.Consumer, ref.MessageID)
	if err != nil || rec == nil {
		return SubmitResult{}, false, err
	}
	if !bytes.Equal(rec.PayloadHash, ref.PayloadHash) {
		return SubmitResult{}, false, ErrInboxPayloadMismatch
	}
	t, err := tx.Transactions().Get(ctx, rec.TransactionID)
	if err != nil {
		return SubmitResult{}, false, err
	}
	if t == nil {
		return SubmitResult{}, false, fmt.Errorf("%w: inbox points to missing transaction %s", ErrInvariantViolation, rec.TransactionID)
	}
	res := resultOf(t, true)
	res.FromInbox = true
	return res, true, nil
}

func (s *SubmitWager) submit(ctx context.Context, tx Tx, cmd SubmitCommand, hash []byte) (SubmitResult, error) {
	if res, found, err := s.existing(ctx, tx, cmd, hash); err != nil || found {
		return res, err
	}
	w, err := tx.Wallets().GetForUpdate(ctx, cmd.WalletID)
	if err != nil {
		return SubmitResult{}, err
	}
	// Re-check under the wallet lock: a concurrent twin may have committed.
	if res, found, err := s.existing(ctx, tx, cmd, hash); err != nil || found {
		return res, err
	}
	if w.PlayerID() != cmd.PlayerID {
		return SubmitResult{}, fmt.Errorf("%w: player", ErrWalletMismatch)
	}
	if w.Currency() != cmd.Money.Currency() {
		return SubmitResult{}, fmt.Errorf("%w: currency", ErrWalletMismatch)
	}

	now := s.clock.Now()
	t, err := wagering.NewExternal(wagering.NewExternalParams{
		ID: s.ids.New(), WalletID: w.ID(), PlayerID: w.PlayerID(), CorrelationID: cmd.CorrelationID,
		Kind: cmd.Kind, Amount: cmd.Money, Now: now,
		Provider: wagering.ProviderRef{
			ProviderID: cmd.ProviderID, ExternalID: cmd.ExternalTransactionID, IdempotencyKey: cmd.IdempotencyKey,
			PayloadHash: hash, RoundID: cmd.RoundID, GameID: cmd.GameID,
			ReferenceExternalID: cmd.ReferenceExternalTransactionID,
		},
	})
	if err != nil {
		return SubmitResult{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	decision, _, err := decide(ctx, tx, t, w.Balance())
	if err != nil {
		return SubmitResult{}, err
	}
	if err := s.settler.settle(ctx, tx, w, t, decision, cmd.CausationID, now, tx.Transactions().Insert); err != nil {
		return SubmitResult{}, err
	}
	return resultOf(t, false), nil
}

// existing resolves idempotency: same key and hash replays; same key with a
// different hash conflicts; same operation under another key conflicts (ADR 0007).
func (s *SubmitWager) existing(ctx context.Context, tx Tx, cmd SubmitCommand, hash []byte) (SubmitResult, bool, error) {
	byKey, err := tx.Transactions().FindByIdempotencyKey(ctx, cmd.ProviderID, cmd.IdempotencyKey)
	if err != nil {
		return SubmitResult{}, false, err
	}
	if byKey != nil {
		if !bytes.Equal(byKey.Provider().PayloadHash, hash) {
			return SubmitResult{}, false, ErrIdempotencyPayloadMismatch
		}
		return resultOf(byKey, true), true, nil
	}
	byExternal, err := tx.Transactions().FindByExternalID(ctx, cmd.ProviderID, cmd.ExternalTransactionID)
	if err != nil {
		return SubmitResult{}, false, err
	}
	if byExternal != nil {
		// Under READ COMMITTED, each lookup above takes its own snapshot: a
		// concurrent twin using the same key can commit between them, so the
		// key lookup above misses it while this one finds it. Judge the
		// mismatch by the row's own key, not by which query surfaced it.
		if byExternal.Provider().IdempotencyKey == cmd.IdempotencyKey {
			if !bytes.Equal(byExternal.Provider().PayloadHash, hash) {
				return SubmitResult{}, false, ErrIdempotencyPayloadMismatch
			}
			return resultOf(byExternal, true), true, nil
		}
		return SubmitResult{}, false, &KeyMismatchError{ExistingTransactionID: byExternal.ID()}
	}
	return SubmitResult{}, false, nil
}

func resultOf(t *wagering.Transaction, replay bool) SubmitResult {
	balance, version, _ := t.Result()
	return SubmitResult{
		TransactionID: t.ID(), Status: t.Status(), FailureCode: t.FailureCode(),
		Balance: balance, WalletVersion: version, IdempotentReplay: replay,
	}
}
