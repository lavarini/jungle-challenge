package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/money"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

const (
	playerID = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"
	walletID = "0192f291-27dd-7d3f-8071-5f8685deef37"
)

type fakeVerifier map[string]app.Principal

func (f fakeVerifier) Verify(_ context.Context, raw string) (app.Principal, error) {
	p, ok := f[raw]
	if !ok {
		return app.Principal{}, app.ErrUnauthenticated
	}
	return p, nil
}

type fakeSubmitter struct {
	calls  int
	got    app.SubmitCommand
	result app.SubmitResult
	err    error
}

func (f *fakeSubmitter) Execute(_ context.Context, cmd app.SubmitCommand) (app.SubmitResult, error) {
	f.calls++
	f.got = cmd
	return f.result, f.err
}

type fakeOpener struct {
	calls int
	view  app.WalletView
	err   error
}

func (f *fakeOpener) Execute(_ context.Context, _ app.OpenWalletCommand) (app.WalletView, error) {
	f.calls++
	return f.view, f.err
}

type fakeReader struct{}

func (fakeReader) Execute(context.Context, string) (app.WalletView, error) {
	return app.WalletView{}, app.ErrWalletNotFound
}

type fakeReady struct{ err error }

func (f fakeReady) Ready(context.Context) error { return f.err }

type fixture struct {
	handler   http.Handler
	submitter *fakeSubmitter
	opener    *fakeOpener
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{submitter: &fakeSubmitter{}, opener: &fakeOpener{}}
	f.handler = NewHandler(Deps{
		Verifier: fakeVerifier{
			"provider-a": {ProviderID: "provider-a", ClientID: "provider-a", Roles: []app.Role{app.RoleProvider}},
			"internal":   {ClientID: "wallet-internal", Roles: []app.Role{app.RoleInternal}},
		},
		OpenWallet: f.opener, GetWallet: fakeReader{}, SubmitWager: f.submitter,
		Readiness: fakeReady{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return f
}

func brl(t *testing.T, s string) money.Money {
	t.Helper()
	m, err := money.Parse(s, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func betBody(provider string) string {
	return `{"providerId":"` + provider + `","externalTransactionId":"transaction-123","playerId":"` + playerID +
		`","walletId":"` + walletID + `","roundId":"round-987","gameId":"fortune-chimp","kind":"BET",` +
		`"money":{"amount":"25.00","currency":"BRL"}}`
}

func (f *fixture) do(method, path, token, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func problemCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	return p.Code
}

var idem = map[string]string{"Idempotency-Key": "provider-a:transaction-123"}

func TestAuthentication(t *testing.T) {
	f := newFixture(t)
	if rec := f.do(http.MethodPost, "/wagering/transactions", "", betBody("provider-a"), idem); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: %d", rec.Code)
	}
	rec := f.do(http.MethodPost, "/wagering/transactions", "forged", betBody("provider-a"), idem)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("invalid token: %d", rec.Code)
	}
	if f.submitter.calls != 0 {
		t.Fatal("use case reached without authentication")
	}
}

func TestAuthorizationByRoute(t *testing.T) {
	f := newFixture(t)
	if rec := f.do(http.MethodPost, "/wallets", "provider-a", `{}`, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("provider opening wallet: %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, "/wallets/"+walletID, "provider-a", "", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("provider reading wallet: %d", rec.Code)
	}
	if rec := f.do(http.MethodPost, "/wagering/transactions", "internal", betBody("provider-a"), idem); rec.Code != http.StatusForbidden {
		t.Fatalf("internal submitting wager: %d", rec.Code)
	}
	rec := f.do(http.MethodPost, "/wagering/transactions", "provider-a", betBody("provider-b"), idem)
	if rec.Code != http.StatusForbidden || problemCode(t, rec) != "PROVIDER_MISMATCH" {
		t.Fatalf("provider mismatch: %d %s", rec.Code, rec.Body)
	}
	if f.submitter.calls != 0 || f.opener.calls != 0 {
		t.Fatal("use case reached without authorization")
	}
}

func TestSubmitValidation(t *testing.T) {
	f := newFixture(t)
	cases := map[string]struct {
		body    string
		headers map[string]string
	}{
		"missing idempotency key": {betBody("provider-a"), nil},
		"unknown field":           {strings.Replace(betBody("provider-a"), `"kind"`, `"extra":1,"kind"`, 1), idem},
		"trailing data":           {betBody("provider-a") + `{}`, idem},
		"non canonical money":     {strings.Replace(betBody("provider-a"), `"25.00"`, `"25"`, 1), idem},
		"opening kind":            {strings.Replace(betBody("provider-a"), `"BET"`, `"OPENING"`, 1), idem},
		"non canonical uuid":      {strings.Replace(betBody("provider-a"), playerID, strings.ToUpper(playerID), 1), idem},
		"not json":                {`nope`, idem},
	}
	for name, c := range cases {
		rec := f.do(http.MethodPost, "/wagering/transactions", "provider-a", c.body, c.headers)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, body %s", name, rec.Code, rec.Body)
		}
	}
	if f.submitter.calls != 0 {
		t.Fatalf("invalid input reached the use case %d times", f.submitter.calls)
	}
}

func TestSubmitMapsCommandAndResults(t *testing.T) {
	f := newFixture(t)
	f.submitter.result = app.SubmitResult{TransactionID: "t1", Status: wagering.Processed, Balance: brl(t, "975.00"), WalletVersion: 2}
	rec := f.do(http.MethodPost, "/wagering/transactions", "provider-a", betBody("provider-a"),
		map[string]string{"Idempotency-Key": "provider-a:transaction-123", "X-Correlation-Id": "corr-1"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	want := `{"transactionId":"t1","status":"PROCESSED","balance":{"amount":"975.00","currency":"BRL"},"idempotentReplay":false}`
	if strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("body %s", rec.Body)
	}
	if rec.Header().Get("X-Correlation-Id") != "corr-1" {
		t.Fatal("correlation id not echoed")
	}
	got := f.submitter.got
	if got.ProviderID != "provider-a" || got.IdempotencyKey != "provider-a:transaction-123" || got.Kind != wagering.Bet ||
		got.Money.String() != "25.00" || got.PlayerID != playerID || got.CorrelationID != "corr-1" || got.Source != app.SourceHTTP {
		t.Fatalf("command %+v", got)
	}

	f.submitter.result.IdempotentReplay = true
	if rec := f.do(http.MethodPost, "/wagering/transactions", "provider-a", betBody("provider-a"), idem); rec.Code != http.StatusOK {
		t.Fatalf("replay status %d", rec.Code)
	}

	f.submitter.result = app.SubmitResult{TransactionID: "t2", Status: wagering.Rejected, FailureCode: wagering.BetInsufficientFunds, Balance: brl(t, "20.00"), WalletVersion: 2}
	rec = f.do(http.MethodPost, "/wagering/transactions", "provider-a", betBody("provider-a"), idem)
	if rec.Code != http.StatusUnprocessableEntity || !bytes.Contains(rec.Body.Bytes(), []byte(`"failureCode":"BET_INSUFFICIENT_FUNDS"`)) {
		t.Fatalf("rejection %d %s", rec.Code, rec.Body)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		err       error
		status    int
		code      string
		retryable bool
	}{
		{app.ErrInvalidInput, 400, "INVALID_REQUEST", false},
		{app.ErrWalletMismatch, 400, "WALLET_MISMATCH", false},
		{app.ErrWalletNotFound, 404, "WALLET_NOT_FOUND", false},
		{app.ErrIdempotencyPayloadMismatch, 409, "IDEMPOTENCY_PAYLOAD_MISMATCH", false},
		{&app.KeyMismatchError{ExistingTransactionID: "t9"}, 409, "IDEMPOTENCY_KEY_MISMATCH", false},
		{app.ErrTransient, 503, "SERVICE_UNAVAILABLE", true},
		{app.ErrUniqueConflict, 503, "SERVICE_UNAVAILABLE", true},
		{app.ErrNotImplemented, 501, "NOT_IMPLEMENTED", false},
		{app.ErrInvariantViolation, 500, "INVARIANT_VIOLATION", false},
		{errors.New("boom"), 500, "INTERNAL_ERROR", false},
	}
	for _, c := range cases {
		f := newFixture(t)
		f.submitter.err = c.err
		rec := f.do(http.MethodPost, "/wagering/transactions", "provider-a", betBody("provider-a"), idem)
		var p struct {
			Code          string `json:"code"`
			Retryable     bool   `json:"retryable"`
			TransactionID string `json:"transactionId"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if rec.Code != c.status || p.Code != c.code || p.Retryable != c.retryable {
			t.Errorf("%v: got %d %s %v, want %d %s %v", c.err, rec.Code, p.Code, p.Retryable, c.status, c.code, c.retryable)
		}
		if rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%v: content type %q", c.err, rec.Header().Get("Content-Type"))
		}
		if c.status == 503 && rec.Header().Get("Retry-After") == "" {
			t.Errorf("%v: missing Retry-After", c.err)
		}
		if c.code == "IDEMPOTENCY_KEY_MISMATCH" && p.TransactionID != "t9" {
			t.Errorf("key mismatch must carry the existing transaction id")
		}
	}
}

func TestOpenWallet(t *testing.T) {
	f := newFixture(t)
	f.opener.view = app.WalletView{ID: walletID, PlayerID: playerID, Balance: brl(t, "1000.00"), Version: 1}
	body := `{"playerId":"` + playerID + `","initialBalance":{"amount":"1000.00","currency":"BRL"}}`
	rec := f.do(http.MethodPost, "/wallets", "internal", body, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	want := `{"id":"` + walletID + `","playerId":"` + playerID + `","balance":{"amount":"1000.00","currency":"BRL"},"version":1}`
	if strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("body %s", rec.Body)
	}
	f.opener.err = app.ErrWalletExists
	if rec := f.do(http.MethodPost, "/wallets", "internal", body, nil); rec.Code != http.StatusConflict || problemCode(t, rec) != "WALLET_ALREADY_EXISTS" {
		t.Fatalf("duplicate: %d %s", rec.Code, rec.Body)
	}
}

func TestHealth(t *testing.T) {
	f := newFixture(t)
	if rec := f.do(http.MethodGet, "/health/live", "", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("live %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, "/health/ready", "", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("ready %d", rec.Code)
	}
	down := NewHandler(Deps{Verifier: fakeVerifier{}, Readiness: fakeReady{err: errors.New("postgres: down")},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	rec := httptest.NewRecorder()
	down.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready when down: %d", rec.Code)
	}
}

func TestBodyLimit(t *testing.T) {
	f := newFixture(t)
	huge := `{"providerId":"` + strings.Repeat("a", 70<<10) + `"}`
	if rec := f.do(http.MethodPost, "/wagering/transactions", "provider-a", huge, idem); rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized body: %d", rec.Code)
	}
}

// stuckSubmitter blocks like a query on a database that stopped answering
// (TCP open, no reply) and returns what the postgres adapter makes of the
// context error once it expires.
type stuckSubmitter struct{}

func (stuckSubmitter) Execute(ctx context.Context, _ app.SubmitCommand) (app.SubmitResult, error) {
	<-ctx.Done()
	return app.SubmitResult{}, fmt.Errorf("%w: %w", app.ErrTransient, ctx.Err())
}

func TestRequestTimeoutAnswersRetryable503WhenTheDatabaseHangs(t *testing.T) {
	h := NewHandler(Deps{
		Verifier:    fakeVerifier{"provider-a": {ProviderID: "provider-a", ClientID: "provider-a", Roles: []app.Role{app.RoleProvider}}},
		SubmitWager: stuckSubmitter{}, Readiness: fakeReady{},
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		RequestTimeout: 50 * time.Millisecond,
	})
	req := httptest.NewRequest(http.MethodPost, "/wagering/transactions", strings.NewReader(betBody("provider-a")))
	req.Header.Set("Authorization", "Bearer provider-a")
	req.Header.Set("Idempotency-Key", "provider-a:transaction-123")
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() { h.ServeHTTP(rec, req); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler still blocked: no per-request deadline reaches the database call")
	}
	if rec.Code != http.StatusServiceUnavailable || problemCode(t, rec) != "SERVICE_UNAVAILABLE" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("got %d %s, want retryable 503", rec.Code, rec.Body.String())
	}
}
