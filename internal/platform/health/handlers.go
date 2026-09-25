package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// Checker is what the readiness endpoint asks; *Readiness satisfies it.
type Checker interface{ Ready(context.Context) error }

// LiveHandler reports that the process is up; it checks nothing else.
func LiveHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { write(w, http.StatusOK, "ok") })
}

// ReadyHandler reports 503 while any dependency check fails or the process
// is draining, so a balancer or container platform stops routing to it.
func ReadyHandler(c Checker, l *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := c.Ready(ctx); err != nil {
			l.WarnContext(r.Context(), "not ready", "error", err.Error())
			write(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		write(w, http.StatusOK, "ok")
	})
}

func write(w http.ResponseWriter, code int, status string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
}
