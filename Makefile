# Everything CI runs has a target here, so a red build can be reproduced
# locally with one command instead of by reading a workflow file.

SHELL := /usr/bin/env bash
GO ?= go
GOLANGCI_LINT_VERSION ?= v2.13.2
COVERAGE_THRESHOLD ?= 60

.PHONY: build test vet fmt-check tidy-check shellcheck lint-deps lint go.test.coverage e2e demo down

build:
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/lbplane ./cmd/lbplane

test:
	$(GO) test -race -shuffle=on -count=1 ./...

vet:
	$(GO) vet ./...

fmt-check:
	@out=$$(gofmt -l .); \
	if [[ -n "$$out" ]]; then echo "not gofmt'd:"; echo "$$out"; exit 1; fi

tidy-check:
	@cp go.mod go.mod.bak; cp go.sum go.sum.bak; \
	$(GO) mod tidy; \
	status=0; \
	diff -q go.mod go.mod.bak >/dev/null || { echo "go mod tidy changed go.mod; commit the result"; status=1; }; \
	diff -q go.sum go.sum.bak >/dev/null || { echo "go mod tidy changed go.sum; commit the result"; status=1; }; \
	mv go.mod.bak go.mod; mv go.sum.bak go.sum; \
	exit $$status

shellcheck:
	shellcheck scripts/*.sh test/*.sh

lint-deps:
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

lint:
	golangci-lint run

go.test.coverage:
	$(GO) test -coverprofile=coverage.out -covermode=atomic ./...
	$(GO) tool cover -func=coverage.out
	@$(GO) tool cover -func=coverage.out | awk '/^total:/ { sub(/%/, "", $$3); \
		if ($$3 + 0 < $(COVERAGE_THRESHOLD)) { printf "coverage %.1f%% below $(COVERAGE_THRESHOLD)%% gate\n", $$3; exit 1 } }'

e2e:
	test/e2e.sh

demo:
	scripts/gen-cert.sh checkout.pay.test deploy/certs
	docker compose -f deploy/docker-compose.yml up -d --build

down:
	docker compose -f deploy/docker-compose.yml down -v
