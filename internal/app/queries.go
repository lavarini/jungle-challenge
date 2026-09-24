package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/lavarini/backend-challenge-go/internal/money"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

type GetTransaction struct {
	uow UnitOfWork
}

func NewGetTransaction(uow UnitOfWork) *GetTransaction { return &GetTransaction{uow: uow} }

// ByID returns a transaction. A non-empty providerScope hides other
// providers' transactions: they read as not found (ADR 0015).
func (g *GetTransaction) ByID(ctx context.Context, id, providerScope string) (*wagering.Transaction, error) {
	var t *wagering.Transaction
	err := g.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		t, err = tx.Transactions().Get(ctx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	if t == nil || (providerScope != "" && t.Provider().ProviderID != providerScope) {
		return nil, ErrTransactionNotFound
	}
	return t, nil
}

func (g *GetTransaction) ByExternalID(ctx context.Context, providerID, externalID string) (*wagering.Transaction, error) {
	var t *wagering.Transaction
	err := g.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		t, err = tx.Transactions().FindByExternalID(ctx, providerID, externalID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, ErrTransactionNotFound
	}
	return t, nil
}

const (
	DefaultLedgerLimit = 50
	MaxLedgerLimit     = 100
	cursorPrefix       = "v1:"
)

type LedgerPage struct {
	Rows       []LedgerRow
	NextCursor string
}

type ListLedger struct {
	uow UnitOfWork
}

func NewListLedger(uow UnitOfWork) *ListLedger { return &ListLedger{uow: uow} }

// Execute pages the ledger in seq order with an opaque cursor.
func (l *ListLedger) Execute(ctx context.Context, walletID, cursor string, limit int) (LedgerPage, error) {
	if limit == 0 {
		limit = DefaultLedgerLimit
	}
	if limit < 1 || limit > MaxLedgerLimit {
		return LedgerPage{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidInput, MaxLedgerLimit)
	}
	after, err := decodeCursor(cursor)
	if err != nil {
		return LedgerPage{}, err
	}
	var page LedgerPage
	err = l.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := tx.Wallets().Get(ctx, walletID); err != nil {
			return err
		}
		rows, err := tx.Ledger().Page(ctx, walletID, after, limit+1)
		if err != nil {
			return err
		}
		if len(rows) > limit {
			rows = rows[:limit]
			page.NextCursor = encodeCursor(rows[len(rows)-1].Seq)
		}
		page.Rows = rows
		return nil
	})
	return page, err
}

func encodeCursor(seq int64) string { return encodeRaw(cursorPrefix + strconv.FormatInt(seq, 10)) }

func encodeRaw(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func decodeCursor(c string) (int64, error) {
	if c == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil || !strings.HasPrefix(string(raw), cursorPrefix) {
		return 0, fmt.Errorf("%w: invalid cursor", ErrInvalidInput)
	}
	seq, err := strconv.ParseInt(strings.TrimPrefix(string(raw), cursorPrefix), 10, 64)
	if err != nil || seq < 0 {
		return 0, fmt.Errorf("%w: invalid cursor", ErrInvalidInput)
	}
	return seq, nil
}

type ReconciliationTotals struct {
	Stored             money.Money
	CreditsMinusDebits int64
	Entries            int
}

type Reconciliation struct {
	WalletID       string
	Stored         money.Money
	Calculated     money.Money
	Difference     money.Money
	Consistent     bool
	CheckedEntries int
}

type Reconcile struct {
	reader Reconciler
	log    *slog.Logger
}

func NewReconcile(r Reconciler, log *slog.Logger) *Reconcile { return &Reconcile{reader: r, log: log} }

// Execute rebuilds the balance from the ledger and compares; it never writes.
// Difference is stored minus calculated.
func (r *Reconcile) Execute(ctx context.Context, walletID string) (Reconciliation, error) {
	tot, err := r.reader.Totals(ctx, walletID)
	if err != nil {
		return Reconciliation{}, err
	}
	calc, err := money.FromMinor(tot.CreditsMinusDebits, tot.Stored.Currency())
	if err != nil {
		return Reconciliation{}, fmt.Errorf("%w: %w", ErrInvariantViolation, err)
	}
	diff, err := tot.Stored.Sub(calc)
	if err != nil {
		return Reconciliation{}, fmt.Errorf("%w: %w", ErrInvariantViolation, err)
	}
	res := Reconciliation{WalletID: walletID, Stored: tot.Stored, Calculated: calc, Difference: diff, Consistent: diff.IsZero(), CheckedEntries: tot.Entries}
	if !res.Consistent {
		r.log.ErrorContext(ctx, "reconciliation divergence", "walletId", walletID,
			"stored", tot.Stored.String(), "calculated", calc.String(), "difference", diff.String())
	}
	return res, nil
}
