//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lavarini/backend-challenge-go/internal/adapters/oidc"
	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/test/testenv"
)

func keycloakVerifier(t *testing.T) *oidc.Verifier {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	v, err := oidc.NewVerifier(ctx, oidc.Config{IssuerURL: env.Keycloak.IssuerURL, Audience: "wagering-api"})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestKeycloakTokensMapToPrincipals(t *testing.T) {
	v := keycloakVerifier(t)
	ctx := context.Background()

	providerToken, err := env.Keycloak.Token(ctx, testenv.ProviderAID, testenv.ProviderASecret)
	if err != nil {
		t.Fatal(err)
	}
	p, err := v.Verify(ctx, providerToken)
	if err != nil {
		t.Fatal(err)
	}
	if p.ProviderID != "provider-a" || !p.Has(app.RoleProvider) || p.Has(app.RoleInternal) {
		t.Fatalf("provider principal %+v", p)
	}

	internalToken, err := env.Keycloak.Token(ctx, testenv.InternalID, testenv.InternalSecret)
	if err != nil {
		t.Fatal(err)
	}
	p, err = v.Verify(ctx, internalToken)
	if err != nil {
		t.Fatal(err)
	}
	if p.ProviderID != "" || !p.Has(app.RoleInternal) || p.Has(app.RoleProvider) {
		t.Fatalf("internal principal %+v", p)
	}
}

func TestKeycloakExpiredTokenIsRejected(t *testing.T) {
	v := keycloakVerifier(t)
	ctx := context.Background()
	token, err := env.Keycloak.Token(ctx, testenv.ProviderAShortID, testenv.ProviderAShortSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(ctx, token); err != nil {
		t.Fatalf("fresh short token must verify: %v", err)
	}
	time.Sleep(3 * time.Second)
	if _, err := v.Verify(ctx, token); !errors.Is(err, app.ErrUnauthenticated) {
		t.Fatalf("expired token error = %v", err)
	}
}

func TestTamperedKeycloakTokenIsRejected(t *testing.T) {
	v := keycloakVerifier(t)
	ctx := context.Background()
	token, _ := env.Keycloak.Token(ctx, testenv.ProviderAID, testenv.ProviderASecret)
	tampered := token[:len(token)-4] + "AAAA"
	if _, err := v.Verify(ctx, tampered); !errors.Is(err, app.ErrUnauthenticated) {
		t.Fatalf("tampered token error = %v", err)
	}
}
