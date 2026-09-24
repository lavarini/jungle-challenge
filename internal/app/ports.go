// Package app holds the use cases. It depends only on the domain and on the
// ports declared here; adapters implement the ports.
package app

import (
	"context"
	"time"

	"github.com/lavarini/backend-challenge-go/internal/events"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
	"github.com/lavarini/backend-challenge-go/internal/wallet"
)

type Clock interface{ Now() time.Time }

type IDGenerator interface{ New() string }

// UnitOfWork runs fn in one database transaction: commit when fn returns nil,
// rollback otherwise. Returned errors are classified with this package's sentinels.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

type Tx interface {
	Wallets() WalletRepository
	Transactions() TransactionRepository
	Ledger() LedgerRepository
	Outbox() OutboxRepository
	Inbox() InboxRepository
}

type WalletRepository interface {
	Insert(ctx context.Context, w *wallet.Wallet) error
	Get(ctx context.Context, id string) (*wallet.Wallet, error)
	GetForUpdate(ctx context.Context, id string) (*wallet.Wallet, error)
	UpdateBalance(ctx context.Context, w *wallet.Wallet) error
}

// TransactionRepository finders return (nil, nil) when nothing matches.
type TransactionRepository interface {
	Insert(ctx context.Context, t *wagering.Transaction) error
	Update(ctx context.Context, t *wagering.Transaction) error
	Get(ctx context.Context, id string) (*wagering.Transaction, error)
	GetForUpdate(ctx context.Context, id string) (*wagering.Transaction, error)
	FindByIdempotencyKey(ctx context.Context, providerID, key string) (*wagering.Transaction, error)
	FindByExternalID(ctx context.Context, providerID, externalID string) (*wagering.Transaction, error)
	// HasProcessedReversal reports whether a PROCESSED REFUND or ROLLBACK
	// references the transaction (ADR 0005).
	HasProcessedReversal(ctx context.Context, referenceTxID string) (bool, error)
	// WakePending makes operations waiting for (providerID, referenceExternalID)
	// due now (ADR 0010).
	WakePending(ctx context.Context, providerID, referenceExternalID string, now time.Time) error
	// ClaimDuePending leases up to limit due PENDING_REFERENCE operations by
	// moving next_attempt_at to leaseUntil; concurrent claimers skip them.
	ClaimDuePending(ctx context.Context, now, leaseUntil time.Time, limit int) ([]string, error)
}

type LedgerRow struct {
	Seq   int64
	Entry wallet.LedgerEntry
}

type LedgerRepository interface {
	Insert(ctx context.Context, e wallet.LedgerEntry) error
	// Page returns up to limit entries of the wallet with wallet_version >
	// afterVersion, in wallet_version order.
	Page(ctx context.Context, walletID string, afterVersion int64, limit int) ([]LedgerRow, error)
}

// Reconciler reads the stored balance and the ledger totals from one
// consistent snapshot.
type Reconciler interface {
	Totals(ctx context.Context, walletID string) (ReconciliationTotals, error)
}

type OutboxRepository interface {
	Append(ctx context.Context, evs ...events.Envelope) error
}

// InboxRepository records handled messages; Find returns (nil, nil) when absent.
type InboxRepository interface {
	Find(ctx context.Context, consumer, messageID string) (*InboxRecord, error)
	Insert(ctx context.Context, r InboxRecord) error
}
