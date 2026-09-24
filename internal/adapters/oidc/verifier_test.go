package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/lavarini/backend-challenge-go/internal/app"
)

type fakeIdP struct {
	srv *httptest.Server
	key *rsa.PrivateKey
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &fakeIdP{key: key}
	mux := http.NewServeMux()
	idp.srv = httptest.NewServer(mux)
	t.Cleanup(idp.srv.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                idp.srv.URL,
			"jwks_uri":                              idp.srv.URL + "/jwks",
			"authorization_endpoint":                idp.srv.URL + "/auth",
			"token_endpoint":                        idp.srv.URL + "/token",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"},
		}})
	})
	return idp
}

type claims struct {
	jwt.Claims
	AZP         string         `json:"azp,omitempty"`
	ProviderID  string         `json:"provider_id,omitempty"`
	RealmAccess map[string]any `json:"realm_access,omitempty"`
}

func (idp *fakeIdP) sign(t *testing.T, c claims) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: idp.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.Signed(signer).Claims(c).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func (idp *fakeIdP) providerClaims(expiry time.Time) claims {
	return claims{
		Claims: jwt.Claims{
			Issuer: idp.srv.URL, Subject: "svc-provider-a", Audience: jwt.Audience{"wagering-api"},
			Expiry: jwt.NewNumericDate(expiry), IssuedAt: jwt.NewNumericDate(time.Now()),
		},
		AZP: "provider-a", ProviderID: "provider-a",
		RealmAccess: map[string]any{"roles": []string{"wager:provider", "offline_access"}},
	}
}

func newTestVerifier(t *testing.T, idp *fakeIdP) *Verifier {
	t.Helper()
	v, err := NewVerifier(context.Background(), Config{IssuerURL: idp.srv.URL, Audience: "wagering-api"})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestVerifyValidProviderToken(t *testing.T) {
	idp := newFakeIdP(t)
	v := newTestVerifier(t, idp)
	p, err := v.Verify(context.Background(), idp.sign(t, idp.providerClaims(time.Now().Add(time.Minute))))
	if err != nil {
		t.Fatal(err)
	}
	if p.ProviderID != "provider-a" || p.ClientID != "provider-a" || !p.Has(app.RoleProvider) || p.Has(app.RoleInternal) {
		t.Fatalf("principal %+v", p)
	}
	if len(p.Roles) != 1 {
		t.Fatalf("unknown roles must be dropped: %v", p.Roles)
	}
}

func TestVerifyRejectsInvalidTokens(t *testing.T) {
	idp := newFakeIdP(t)
	v := newTestVerifier(t, idp)
	valid := idp.providerClaims(time.Now().Add(time.Minute))

	expired := idp.providerClaims(time.Now().Add(-time.Minute))
	wrongAud := valid
	wrongAud.Audience = jwt.Audience{"other-api"}
	wrongIss := valid
	wrongIss.Issuer = "https://evil.example"
	providerWithoutID := valid
	providerWithoutID.ProviderID = ""

	hsSigner, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte("0123456789abcdef0123456789abcdef")}, nil)
	hsToken, _ := jwt.Signed(hsSigner).Claims(valid).Serialize()

	payload, _ := json.Marshal(valid)
	noneToken := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + "."

	cases := map[string]string{
		"expired":             idp.sign(t, expired),
		"wrong audience":      idp.sign(t, wrongAud),
		"wrong issuer":        idp.sign(t, wrongIss),
		"provider without id": idp.sign(t, providerWithoutID),
		"hs256":               hsToken,
		"alg none":            noneToken,
		"malformed":           "not-a-token",
		"empty":               "",
	}
	for name, token := range cases {
		if _, err := v.Verify(context.Background(), token); !errors.Is(err, app.ErrUnauthenticated) {
			t.Errorf("%s: error = %v, want ErrUnauthenticated", name, err)
		}
	}
}

func TestClockSkewToleratesRecentExpiry(t *testing.T) {
	idp := newFakeIdP(t)
	v, err := NewVerifier(context.Background(), Config{IssuerURL: idp.srv.URL, Audience: "wagering-api", ClockSkew: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), idp.sign(t, idp.providerClaims(time.Now().Add(-10*time.Second)))); err != nil {
		t.Fatalf("token expired 10s ago must pass with 30s skew: %v", err)
	}
}

func TestNewVerifierFailsClosed(t *testing.T) {
	if _, err := NewVerifier(context.Background(), Config{Audience: "wagering-api"}); err == nil {
		t.Fatal("missing issuer must fail")
	}
	if _, err := NewVerifier(context.Background(), Config{IssuerURL: "http://127.0.0.1:1", Audience: "wagering-api"}); err == nil {
		t.Fatal("unreachable discovery must fail")
	}
	idp := newFakeIdP(t)
	if _, err := NewVerifier(context.Background(), Config{IssuerURL: idp.srv.URL}); err == nil {
		t.Fatal("missing audience must fail")
	}
}
