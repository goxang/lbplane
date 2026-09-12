GO ?= go

.PHONY: build test lint e2e demo down

build:
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/lbplane ./cmd/lbplane

test:
	$(GO) test -race -count=1 ./...

lint:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	$(GO) vet ./...
	$(GO) run honnef.co/go/tools/cmd/staticcheck@2024.1.1 ./...

e2e:
	test/e2e.sh

demo:
	scripts/gen-cert.sh checkout.pay.test deploy/certs
	docker compose -f deploy/docker-compose.yml up -d --build

down:
	docker compose -f deploy/docker-compose.yml down -v
