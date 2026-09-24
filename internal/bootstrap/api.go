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

func newHandler(v *oidc.Verifier, open *app.OpenWallet, get *app.GetWallet, submit *app.SubmitWager, r *health.Readiness, l *slog.Logger) http.Handler {
	return httpapi.NewHandler(httpapi.Deps{Verifier: v, OpenWallet: open, GetWallet: get, SubmitWager: submit, Readiness: r, Logger: l})
}

// runHTTPServer binds synchronously so a busy port fails startup. On stop it
// reports not-ready first, then drains in-flight requests within the deadline.
func runHTTPServer(lc fx.Lifecycle, cfg config.Config, h http.Handler, r *health.Readiness, l *slog.Logger) {
	srv := &http.Server{
		Addr: cfg.HTTPAddr, Handler: h,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
	}
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
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
			return nil
		},
		OnStop: func(ctx context.Context) error {
			r.SetDraining()
			return srv.Shutdown(ctx)
		},
	})
}
