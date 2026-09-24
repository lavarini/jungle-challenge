package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"time"

	"go.uber.org/fx"

	"github.com/lavarini/backend-challenge-go/internal/platform/config"
	"github.com/lavarini/backend-challenge-go/internal/platform/health"
	"github.com/lavarini/backend-challenge-go/internal/platform/metrics"
)

// runAdminServer serves /metrics, /debug/pprof and the health endpoints on a
// separate listener that the Compose file never publishes (ADR 0015). Worker-
// only roles run no public HTTP server, so their orchestrator probes
// readiness here.
//
// Its stop belongs to the drain, after every other component (addLast):
// readiness reports 503 here for the whole drain and metrics stay scrapeable
// until the workers are done, and it still closes before the pool does.
func runAdminServer(lc fx.Lifecycle, cfg config.Config, m *metrics.Metrics, r *health.Readiness, d *drain, l *slog.Logger) {
	srv := &http.Server{Addr: cfg.AdminAddr, Handler: adminMux(m, r, l), ReadHeaderTimeout: 5 * time.Second}
	lc.Append(fx.StartHook(func() error {
		ln, err := net.Listen("tcp", srv.Addr)
		if err != nil {
			return err
		}
		l.Info("admin server listening", "addr", ln.Addr().String())
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				l.Error("admin server stopped", "error", err.Error())
			}
		}()
		d.addLast("admin", func(ctx context.Context) error {
			// A running CPU profile or trace must not hold the pool open:
			// give in-flight scrapes a moment, then close.
			ctx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			if err := srv.Shutdown(ctx); err != nil {
				_ = srv.Close()
			}
			return nil
		})
		return nil
	}))
}

func adminMux(m *metrics.Metrics, r *health.Readiness, l *slog.Logger) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", m.Handler())
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeStatus(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 3*time.Second)
		defer cancel()
		if err := r.Ready(ctx); err != nil {
			l.WarnContext(req.Context(), "not ready", "error", err.Error())
			writeStatus(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		writeStatus(w, http.StatusOK, "ok")
	})
	return mux
}

func writeStatus(w http.ResponseWriter, code int, status string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
}
