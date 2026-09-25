package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 15 * time.Second}

type money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type wallet struct {
	ID       string `json:"id"`
	PlayerID string `json:"playerId"`
}

// submitRequest mirrors components/schemas/SubmitRequest in docs/openapi.yaml.
type submitRequest struct {
	ProviderID            string `json:"providerId"`
	ExternalTransactionID string `json:"externalTransactionId"`
	PlayerID              string `json:"playerId"`
	WalletID              string `json:"walletId"`
	RoundID               string `json:"roundId"`
	GameID                string `json:"gameId"`
	Kind                  string `json:"kind"`
	Money                 money  `json:"money"`
}

type reconciliationResult struct {
	WalletID   string `json:"walletId"`
	Consistent bool   `json:"consistent"`
}

// fetchToken obtains an access token via client_credentials, mirroring scripts/smoke.sh.
func fetchToken(ctx context.Context, issuerURL, clientID, secret string) (string, error) {
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {clientID}, "client_secret": {secret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, issuerURL+"/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("token endpoint %s: status %d: %s", issuerURL, resp.StatusCode, body)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("token endpoint %s: empty access_token", issuerURL)
	}
	return out.AccessToken, nil
}

// postJSON performs one request with a bearer token, optionally decoding a JSON response body.
// It always drains and closes the response body so the connection can be reused.
func postJSON(ctx context.Context, method, target, bearer, idemKey string, body, out any) (int, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return 0, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, target, &buf)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil && err != io.EOF {
			io.Copy(io.Discard, resp.Body) //nolint:errcheck // best-effort drain on decode failure
			return resp.StatusCode, fmt.Errorf("decode response body: %w", err)
		}
		return resp.StatusCode, nil
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck // draining lets the transport reuse the connection
	return resp.StatusCode, nil
}

// openWallet opens a wallet with the given initial balance, as the internal role.
func openWallet(ctx context.Context, api, bearer, playerID, balance string) (wallet, error) {
	var w wallet
	status, err := postJSON(ctx, http.MethodPost, api+"/wallets", bearer, "", map[string]any{
		"playerId":       playerID,
		"initialBalance": money{Amount: balance, Currency: currency},
	}, &w)
	if err != nil {
		return wallet{}, err
	}
	if status != http.StatusCreated {
		return wallet{}, fmt.Errorf("open wallet: status %d", status)
	}
	return w, nil
}

// reconcileWallet calls POST /wallets/{id}/reconciliation as the internal role.
func reconcileWallet(ctx context.Context, api, bearer, walletID string) (reconciliationResult, error) {
	var r reconciliationResult
	status, err := postJSON(ctx, http.MethodPost, api+"/wallets/"+walletID+"/reconciliation", bearer, "", nil, &r)
	if err != nil {
		return reconciliationResult{}, err
	}
	if status != http.StatusOK {
		return reconciliationResult{}, fmt.Errorf("reconciliation %s: status %d", walletID, status)
	}
	return r, nil
}

// submitBet sends one BET transaction as the provider role and reports only the outcome:
// the load loop cares about status codes and latency, not the response payload.
func submitBet(ctx context.Context, api, bearer, idemKey string, body submitRequest) (int, error) {
	return postJSON(ctx, http.MethodPost, api+"/wagering/transactions", bearer, idemKey, body, nil)
}
