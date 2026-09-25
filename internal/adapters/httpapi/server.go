// Package httpapi exposes the use cases over HTTP. It authenticates, decodes,
// authorizes and maps errors; it holds no financial rule.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/platform/health"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

type TokenVerifier interface {
	Verify(ctx context.Context, raw string) (app.Principal, error)
}

type WalletOpener interface {
	Execute(ctx context.Context, cmd app.OpenWalletCommand) (app.WalletView, error)
}

type WalletReader interface {
	Execute(ctx context.Context, walletID string) (app.WalletView, error)
}

type WagerSubmitter interface {
	Execute(ctx context.Context, cmd app.SubmitCommand) (app.SubmitResult, error)
}

type ReadinessChecker interface {
	Ready(ctx context.Context) error
}

type TransactionReader interface {
	ByID(ctx context.Context, id, providerScope string) (*wagering.Transaction, error)
	ByExternalID(ctx context.Context, providerID, externalID string) (*wagering.Transaction, error)
}

type LedgerLister interface {
	Execute(ctx context.Context, walletID, cursor string, limit int) (app.LedgerPage, error)
}

type WalletReconciler interface {
	Execute(ctx context.Context, walletID string) (app.Reconciliation, error)
}

type Deps struct {
	Verifier     TokenVerifier
	OpenWallet   WalletOpener
	GetWallet    WalletReader
	SubmitWager  WagerSubmitter
	Transactions TransactionReader
	Ledger       LedgerLister
	Reconcile    WalletReconciler
	Readiness    ReadinessChecker
	Logger       *slog.Logger
}

type api struct {
	verifier     TokenVerifier
	opener       WalletOpener
	reader       WalletReader
	submitter    WagerSubmitter
	transactions TransactionReader
	ledger       LedgerLister
	reconciler   WalletReconciler
	readiness    ReadinessChecker
	log          *slog.Logger
}

const maxBodyBytes = 64 << 10

func NewHandler(d Deps) http.Handler {
	a := &api{
		verifier: d.Verifier, opener: d.OpenWallet, reader: d.GetWallet, submitter: d.SubmitWager,
		transactions: d.Transactions, ledger: d.Ledger, reconciler: d.Reconcile,
		readiness: d.Readiness, log: d.Logger,
	}
	mux := http.NewServeMux()
	mux.Handle("GET /health/live", health.LiveHandler())
	mux.Handle("GET /health/ready", health.ReadyHandler(a.readiness, a.log))
	mux.Handle("POST /wallets", a.require(app.RoleInternal, a.openWallet))
	mux.Handle("GET /wallets/{walletId}", a.require(app.RoleInternal, a.getWallet))
	mux.Handle("POST /wagering/transactions", a.require(app.RoleProvider, a.submitWager))
	mux.Handle("GET /wagering/transactions/{transactionId}", a.requireAny(a.getTransaction, app.RoleProvider, app.RoleInternal))
	mux.Handle("GET /providers/{providerId}/wagering/transactions/{externalTransactionId}", a.requireAny(a.getByExternalID, app.RoleProvider, app.RoleInternal))
	mux.Handle("GET /wallets/{walletId}/ledger", a.require(app.RoleInternal, a.listLedger))
	mux.Handle("POST /wallets/{walletId}/reconciliation", a.require(app.RoleInternal, a.reconcile))
	return withCorrelation(withBodyLimit(mux))
}
