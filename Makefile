SHELL := /bin/bash
GO ?= go

.PHONY: test build lint generate tidy e2e

test:
	$(GO) test ./...

build:
	$(GO) build ./...

lint:
	$(GO) vet ./...

generate:
	buf generate

tidy:
	$(GO) mod tidy
