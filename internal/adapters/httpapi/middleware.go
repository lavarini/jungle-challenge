package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lavarini/backend-challenge-go/internal/app"
)

type ctxKey int

const (
	principalKey ctxKey = iota
	correlationKey
)

func withBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// withDeadline gives the request context a deadline. pgx honours it on a
// connection that stopped answering, and the postgres adapter classifies the
// resulting context error as transient, so the caller gets a retryable 503
// instead of waiting on the database forever.
func withDeadline(d time.Duration, next http.Handler) http.Handler {
	if d <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withCorrelation accepts a caller-provided X-Correlation-Id (printable, at
// most 128 bytes) or generates one, and echoes it in the response.
func withCorrelation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Correlation-Id")
		if !validCorrelation(id) {
			id = uuid.NewString()
		}
		w.Header().Set("X-Correlation-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), correlationKey, id)))
	})
}

func validCorrelation(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

func correlationID(ctx context.Context) string {
	id, _ := ctx.Value(correlationKey).(string)
	return id
}

func principalFrom(ctx context.Context) app.Principal {
	p, _ := ctx.Value(principalKey).(app.Principal)
	return p
}

// require authenticates the bearer token and checks the role before any use
// case runs. Rejections never reach the database.
func (a *api) require(role app.Role, next http.HandlerFunc) http.Handler {
	return a.requireAny(next, role)
}

// requireAny admits a caller holding at least one of roles.
func (a *api) requireAny(next http.HandlerFunc, roles ...app.Role) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, raw, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || raw == "" {
			a.writeUnauthenticated(w, r)
			return
		}
		p, err := a.verifier.Verify(r.Context(), raw)
		if err != nil {
			a.log.InfoContext(r.Context(), "authentication rejected", "correlationId", correlationID(r.Context()), "reason", err.Error())
			a.writeUnauthenticated(w, r)
			return
		}
		for _, role := range roles {
			if p.Has(role) {
				next(w, r.WithContext(context.WithValue(r.Context(), principalKey, p)))
				return
			}
		}
		writeProblem(w, http.StatusForbidden, "FORBIDDEN", "caller lacks the required role", false, "")
	})
}

func (a *api) writeUnauthenticated(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="wagering"`)
	writeProblem(w, http.StatusUnauthorized, "UNAUTHENTICATED", "missing or invalid bearer token", false, "")
}
