package health

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type checkerFunc func(context.Context) error

func (f checkerFunc) Ready(ctx context.Context) error { return f(ctx) }

func TestReadyHandler(t *testing.T) {
	l := slog.New(slog.NewTextHandler(io.Discard, nil))
	for name, tc := range map[string]struct {
		err  error
		code int
		body string
	}{
		"ready":     {nil, http.StatusOK, `{"status":"ok"}` + "\n"},
		"not ready": {errors.New("db down"), http.StatusServiceUnavailable, `{"status":"unavailable"}` + "\n"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ReadyHandler(checkerFunc(func(context.Context) error { return tc.err }), l).
				ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
			if rec.Code != tc.code || rec.Body.String() != tc.body {
				t.Fatalf("got %d %q, want %d %q", rec.Code, rec.Body.String(), tc.code, tc.body)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Fatalf("content-type %q", ct)
			}
		})
	}
}

func TestLiveHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	LiveHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"status":"ok"}`+"\n" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}
