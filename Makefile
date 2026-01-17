SHELL := /bin/bash

.PHONY: build clean run ctl fmt vet test tidy

clean:
	rm -f keystone keystonectl keystoneserver

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
LDFLAGS := -ldflags "-X github.com/carlosprados/keystone/internal/version.Version=$(VERSION) -X github.com/carlosprados/keystone/internal/version.Commit=$(COMMIT)"

build:
	go build $(LDFLAGS) ./cmd/keystone
	go build $(LDFLAGS) ./cmd/keystonectl
	go build $(LDFLAGS) ./cmd/keystoneserver

run:
	go run $(LDFLAGS) ./cmd/keystone --http :8080

ctl:
	go build $(LDFLAGS) -o keystonectl ./cmd/keystonectl

fmt:
	go fmt ./...

vet:
	go vet ./...

test:
	go test ./...

tidy:
	go mod tidy

plan-dry:
	@test -n "$(PLAN)" || (echo "Usage: make plan-dry PLAN=path/to/plan.toml" && exit 1)
	go run ./cmd/keystone --apply $(PLAN) --dry-run --http :0

.PHONY: hooks
hooks:
	@chmod +x .githooks/pre-commit scripts/setup-git-hooks.sh || true
	@./scripts/setup-git-hooks.sh
