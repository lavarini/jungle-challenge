package app

import (
	"crypto/sha256"
	"encoding/json"
)

// canonicalJSON renders the business fields with sorted keys (encoding/json
// sorts map keys). The idempotency key and transport metadata are excluded;
// Money is already canonical (ADR 0006), so no further normalization applies.
func canonicalJSON(c SubmitCommand) ([]byte, error) {
	fields := map[string]any{
		"providerId":            c.ProviderID,
		"externalTransactionId": c.ExternalTransactionID,
		"playerId":              c.PlayerID,
		"walletId":              c.WalletID,
		"roundId":               c.RoundID,
		"gameId":                c.GameID,
		"kind":                  string(c.Kind),
		"money":                 map[string]string{"amount": c.Money.String(), "currency": string(c.Money.Currency())},
	}
	if c.ReferenceExternalTransactionID != "" {
		fields["referenceExternalTransactionId"] = c.ReferenceExternalTransactionID
	}
	return json.Marshal(fields)
}

// CanonicalHash is the SHA-256 of the canonical JSON. HTTP and SQS share it.
func CanonicalHash(c SubmitCommand) ([]byte, error) {
	b, err := canonicalJSON(c)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	return sum[:], nil
}
