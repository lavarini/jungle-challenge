GO ?= go

.PHONY: fmt-check vet lint test test-race test-failpoint test-integration test-e2e test-crash evidence up up-multi down smoke

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet ./...
	$(GO) vet -tags=integration ./...
	$(GO) vet -tags=e2e ./...

lint: fmt-check vet

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

test-failpoint:
	$(GO) test -race -tags=failpoint ./internal/platform/failpoint/

test-integration:
	$(GO) test -race -tags=integration -count=1 -timeout=15m ./test/integration/...

test-e2e:
	$(GO) test -tags=e2e -count=1 -timeout=20m ./test/e2e/

test-crash:
	$(GO) test -tags=e2e -count=1 -timeout=20m ./test/e2e/crash/...

evidence:
	./scripts/evidence.sh

up:
	docker compose up --build

up-multi:
	docker compose --profile multi up --build

down:
	docker compose --profile multi down -v

smoke:
	./scripts/smoke.sh
