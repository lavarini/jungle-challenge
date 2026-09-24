#!/usr/bin/env bash
# Exercises the running Compose stack with real Keycloak tokens:
# open a wallet, place a bet, replay it. Fails on any unexpected status.
set -euo pipefail

API=${API:-http://localhost:8080}
KC=${KC:-http://localhost:8081/realms/wagering}
OUT=$(mktemp)
trap 'rm -f "$OUT"' EXIT

token() {
  curl -fsS -X POST "$KC/protocol/openid-connect/token" \
    -d grant_type=client_credentials -d client_id="$1" -d client_secret="$2" |
    sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p'
}

request() { # method path token expected-status [body] [idempotency-key]
  local status
  status=$(curl -sS -o "$OUT" -w '%{http_code}' -X "$1" "$API$2" \
    -H "Authorization: Bearer $3" -H 'Content-Type: application/json' \
    ${6:+-H "Idempotency-Key: $6"} ${5:+-d "$5"})
  echo "$1 $2 -> $status $(cat "$OUT")"
  [ "$status" = "$4" ] || { echo "expected $4" >&2; exit 1; }
}

INTERNAL=$(token wallet-internal wallet-internal-dev-secret)
PROVIDER=$(token provider-a provider-a-dev-secret)
PLAYER=$(uuidgen | tr 'A-Z' 'a-z')

request POST /wallets "$INTERNAL" 201 \
  "{\"playerId\":\"$PLAYER\",\"initialBalance\":{\"amount\":\"1000.00\",\"currency\":\"BRL\"}}"
WALLET_ID=$(sed -n 's/.*"id":"\([^"]*\)".*/\1/p' "$OUT")

EXT="smoke-$(date +%s)"
BET="{\"providerId\":\"provider-a\",\"externalTransactionId\":\"$EXT\",\"playerId\":\"$PLAYER\",\"walletId\":\"$WALLET_ID\",\"roundId\":\"round-1\",\"gameId\":\"game-1\",\"kind\":\"BET\",\"money\":{\"amount\":\"25.00\",\"currency\":\"BRL\"}}"
request POST /wagering/transactions "$PROVIDER" 201 "$BET" "provider-a:$EXT"
request POST /wagering/transactions "$PROVIDER" 200 "$BET" "provider-a:$EXT"
request GET "/wallets/$WALLET_ID" "$INTERNAL" 200
request POST /wallets "$PROVIDER" 403 "{}"

echo "smoke ok"
