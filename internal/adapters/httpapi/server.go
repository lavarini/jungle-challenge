// Package httpapi exposes the use cases over HTTP. It authenticates, decodes,
// authorizes and maps errors; it holds no financial rule.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/lavarini/backend-challenge-go/internal/app"
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

type Deps struct {
	Verifier    TokenVerifier
	OpenWallet  WalletOpener
	GetWallet   WalletReader
	SubmitWager WagerSubmitter
	Readiness   ReadinessChecker
	Logger      *slog.Logger
}

type api struct {
	verifier  TokenVerifier
	opener    WalletOpener
	reader    WalletReader
	submitter WagerSubmitter
	readiness ReadinessChecker
	log       *slog.Logger
}

const maxBodyBytes = 64 << 10

func NewHandler(d Deps) http.Handler {
	a := &api{verifier: d.Verifier, opener: d.OpenWallet, reader: d.GetWallet, submitter: d.SubmitWager, readiness: d.Readiness, log: d.Logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", a.live)
	mux.HandleFunc("GET /health/ready", a.ready)
	mux.Handle("POST /wallets", a.require(app.RoleInternal, a.openWallet))
	mux.Handle("GET /wallets/{walletId}", a.require(app.RoleInternal, a.getWallet))
	mux.Handle("POST /wagering/transactions", a.require(app.RoleProvider, a.submitWager))
	return withCorrelation(withBodyLimit(mux))
}
