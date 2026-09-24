#!/usr/bin/env bash
# Runs every suite with -json and renders docs/EVIDENCIAS.md.
#
# Mirrors the Makefile's test-race, test-integration, test-e2e (only
# ./test/e2e/) and test-crash (./test/e2e/crash/...) targets, adding -json so
# cmd/evidence can render the requirement matrix from every suite's results.
set -uo pipefail
OUT=$(mktemp -d)
trap 'rm -rf "$OUT"' EXIT
# The suite runs keep `|| true`: a failing suite must not abort this script,
# because the generator itself (run below) now turns any missing, skipped or
# package-level-failed test into a gap instead of a false pass.
go test -race -json -count=1 ./... >"$OUT/unit.json" || true
go test -race -json -tags=integration -count=1 -timeout=15m ./test/integration/... >"$OUT/integration.json" || true
go test -json -tags=e2e -count=1 -timeout=20m ./test/e2e/ >"$OUT/e2e.json" || true
go test -json -tags=e2e -count=1 -timeout=20m ./test/e2e/crash/... >"$OUT/crash.json" || true

commit="$(git rev-parse --short HEAD)"
if [ -n "$(git status --porcelain)" ]; then
  commit="${commit}-dirty"
fi

if ! go run ./cmd/evidence -commit "$commit" -go "$(go env GOVERSION)" -date "$(date -u +%Y-%m-%dT%H:%MZ)" \
  "$OUT/unit.json" "$OUT/integration.json" "$OUT/e2e.json" "$OUT/crash.json"; then
  echo "evidence: generator failed, docs/EVIDENCIAS.md not updated" >&2
  exit 1
fi
echo "docs/EVIDENCIAS.md updated"
