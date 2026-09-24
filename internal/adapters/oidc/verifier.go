// Package oidc verifies OAuth 2.0 access tokens issued by the external IdP.
package oidc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"

	"github.com/lavarini/backend-challenge-go/internal/app"
)

type Config struct {
	// IssuerURL is the iss claim tokens carry.
	IssuerURL string
	// DiscoveryURL is where metadata is fetched; defaults to IssuerURL. Differs
	// inside Compose, where the IdP is reached by service name.
	DiscoveryURL string
	Audience     string
	ClockSkew    time.Duration
}

type Verifier struct {
	verifier *gooidc.IDTokenVerifier
}

// NewVerifier fails closed: incomplete config or unreachable discovery
// prevents the process from starting.
func NewVerifier(ctx context.Context, cfg Config) (*Verifier, error) {
	if cfg.IssuerURL == "" || cfg.Audience == "" {
		return nil, errors.New("oidc: issuer and audience are required")
	}
	discovery := cfg.DiscoveryURL
	if discovery == "" {
		discovery = cfg.IssuerURL
	}
	// The key set keeps this context for later JWKS refreshes, so it must not
	// be the (short-lived) startup context. The HTTP client bounds each request.
	keyCtx := gooidc.ClientContext(context.Background(), &http.Client{Timeout: 5 * time.Second})
	if discovery != cfg.IssuerURL {
		keyCtx = gooidc.InsecureIssuerURLContext(keyCtx, cfg.IssuerURL)
	}
	provider, err := discoverWithDeadline(ctx, keyCtx, discovery)
	if err != nil {
		return nil, fmt.Errorf("oidc: discovery %s: %w", discovery, err)
	}
	skew := cfg.ClockSkew
	return &Verifier{verifier: provider.Verifier(&gooidc.Config{
		ClientID:             cfg.Audience,
		SupportedSigningAlgs: []string{gooidc.RS256},
		Now:                  func() time.Time { return time.Now().Add(-skew) },
	})}, nil
}

// discoverWithDeadline honors the caller's deadline without binding the
// provider's key set to it.
func discoverWithDeadline(ctx, keyCtx context.Context, url string) (*gooidc.Provider, error) {
	type result struct {
		p   *gooidc.Provider
		err error
	}
	done := make(chan result, 1)
	go func() {
		p, err := gooidc.NewProvider(keyCtx, url)
		done <- result{p, err}
	}()
	select {
	case r := <-done:
		return r.p, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type tokenClaims struct {
	Subject     string `json:"sub"`
	AZP         string `json:"azp"`
	ProviderID  string `json:"provider_id"`
	RealmAccess struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

// Verify checks signature (RS256 via JWKS), issuer, audience and expiry, and
// maps the claims to a Principal. Unknown roles are dropped.
func (v *Verifier) Verify(ctx context.Context, rawToken string) (app.Principal, error) {
	tok, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return app.Principal{}, fmt.Errorf("%w: %w", app.ErrUnauthenticated, err)
	}
	var c tokenClaims
	if err := tok.Claims(&c); err != nil {
		return app.Principal{}, fmt.Errorf("%w: claims: %w", app.ErrUnauthenticated, err)
	}
	p := app.Principal{Subject: c.Subject, ClientID: c.AZP, ProviderID: c.ProviderID}
	for _, r := range c.RealmAccess.Roles {
		switch role := app.Role(r); role {
		case app.RoleProvider, app.RoleInternal:
			p.Roles = append(p.Roles, role)
		}
	}
	if p.Has(app.RoleProvider) && p.ProviderID == "" {
		return app.Principal{}, fmt.Errorf("%w: provider token without provider_id", app.ErrUnauthenticated)
	}
	return p, nil
}
