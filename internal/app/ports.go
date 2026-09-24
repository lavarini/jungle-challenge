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
	FindByIdempotencyKey(ctx context.Context, providerID, key string) (*wagering.Transaction, error)
	FindByExternalID(ctx context.Context, providerID, externalID string) (*wagering.Transaction, error)
}

type LedgerRepository interface {
	Insert(ctx context.Context, e wallet.LedgerEntry) error
}

type OutboxRepository interface {
	Append(ctx context.Context, evs ...events.Envelope) error
}
