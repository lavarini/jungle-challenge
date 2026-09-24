#!/usr/bin/env bash
# Runs every suite with -json and renders docs/EVIDENCIAS.md.
#
# Mirrors the Makefile's test-race, test-integration, test-e2e (only
# ./test/e2e/) and test-crash (./test/e2e/crash/...) targets, adding -json so
# cmd/evidence can render the requirement matrix from every suite's results.
set -uo pipefail
OUT=$(mktemp -d)
trap 'rm -rf "$OUT"' EXIT
go test -race -json ./... >"$OUT/unit.json" || true
go test -race -json -tags=integration -count=1 -timeout=15m ./test/integration/... >"$OUT/integration.json" || true
go test -json -tags=e2e -count=1 -timeout=20m ./test/e2e/ >"$OUT/e2e.json" || true
go test -json -tags=e2e -count=1 -timeout=20m ./test/e2e/crash/... >"$OUT/crash.json" || true
go run ./cmd/evidence -commit "$(git rev-parse --short HEAD)" -go "$(go env GOVERSION)" -date "$(date -u +%Y-%m-%dT%H:%MZ)" \
  "$OUT/unit.json" "$OUT/integration.json" "$OUT/e2e.json" "$OUT/crash.json"
echo "docs/EVIDENCIAS.md updated"
