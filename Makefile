SHELL := /bin/bash

.PHONY: build run ctl fmt vet test tidy

build:
	go build ./cmd/keystone
	go build ./cmd/keystonectl

run:
	go run ./cmd/keystone --http :8080

ctl:
	go build -o keystonectl ./cmd/keystonectl

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
