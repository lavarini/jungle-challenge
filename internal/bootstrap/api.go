package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"go.uber.org/fx"

	"github.com/lavarini/backend-challenge-go/internal/adapters/httpapi"
	"github.com/lavarini/backend-challenge-go/internal/adapters/metered"
	"github.com/lavarini/backend-challenge-go/internal/adapters/oidc"
	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/platform/config"
	"github.com/lavarini/backend-challenge-go/internal/platform/health"
)

var apiModule = fx.Module("api",
	fx.Provide(newVerifier, newHandler),
	fx.Invoke(runHTTPServer),
)

func newVerifier(cfg config.Config) (*oidc.Verifier, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return oidc.NewVerifier(ctx, oidc.Config{
		IssuerURL: cfg.OIDC.IssuerURL, DiscoveryURL: cfg.OIDC.DiscoveryURL,
		Audience: cfg.OIDC.Audience, ClockSkew: cfg.OIDC.ClockSkew,
	})
}

func newHandler(v *oidc.Verifier, open *app.OpenWallet, get *app.GetWallet, submit metered.Submitter, tx *app.GetTransaction, ledger *app.ListLedger, rec *app.Reconcile, r *health.Readiness, l *slog.Logger) http.Handler {
	return httpapi.NewHandler(httpapi.Deps{
		Verifier: v, OpenWallet: open, GetWallet: get, SubmitWager: submit,
		Transactions: tx, Ledger: ledger, Reconcile: rec,
		Readiness: r, Logger: l,
	})
}

// runHTTPServer binds synchronously so a busy port fails startup. Its stop
// belongs to the drain, which reports not-ready first and then drains
// in-flight requests concurrently with the workers.
func runHTTPServer(lc fx.Lifecycle, cfg config.Config, h http.Handler, d *drain, l *slog.Logger) {
	srv := &http.Server{
		Addr: cfg.HTTPAddr, Handler: h,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
	}
	lc.Append(fx.StartHook(func() error {
		ln, err := net.Listen("tcp", srv.Addr)
		if err != nil {
			return err
		}
		l.Info("http server listening", "addr", ln.Addr().String())
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				l.Error("http server stopped", "error", err.Error())
			}
		}()
		d.add("http", func(ctx context.Context) error {
			err := srv.Shutdown(ctx)
			if err != nil {
				// Past the graceful deadline: close the connections so the
				// requests still running see their contexts cancelled.
				_ = srv.Close()
			}
			return err
		})
		return nil
	}))
}
