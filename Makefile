SHELL := /bin/bash
GOBIN ?= $(shell go env GOPATH)/bin

.PHONY: help tools generate sqlc build test test-rls lint run migrate-up migrate-down bootstrap css tidy check

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-14s\033[0m %s\n", $$1, $$2}'

tools: ## Install code generation and lint tools
	go install github.com/a-h/templ/cmd/templ@latest
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	go install github.com/pressly/goose/v3/cmd/goose@latest
	go install honnef.co/go/tools/cmd/staticcheck@latest

generate: ## Generate templ views and sqlc queries
	$(GOBIN)/templ generate
	$(GOBIN)/sqlc generate

css: ## Build the Tailwind stylesheet
	cd web && npx --yes tailwindcss@3.4.17 -i ./static/css/input.css -o ./static/css/app.css --minify

build: generate css ## Build server and migration binaries
	CGO_ENABLED=0 go build -trimpath -o bin/tablekit ./cmd/server
	CGO_ENABLED=0 go build -trimpath -o bin/tablekit-migrate ./cmd/migrate
	CGO_ENABLED=0 go build -trimpath -o bin/tablekit-bootstrap ./cmd/bootstrap
	CGO_ENABLED=0 go build -trimpath -o bin/tablekit-seed ./cmd/seed

test: ## Run the unit test suite
	go test ./...

test-rls: ## Run the row-level-security isolation tests (needs both DSNs)
	go test ./internal/db/ -run RLS -v

lint: ## Run vet and staticcheck
	go vet ./...
	$(GOBIN)/staticcheck ./...

tidy:
	go mod tidy

migrate-up: ## Apply migrations
	go run ./cmd/migrate up

migrate-down: ## Roll back the most recent migration
	go run ./cmd/migrate down

bootstrap: ## Seed a merchant and owner (see .env.example)
	go run ./cmd/bootstrap

demo-data: ## Load demo menu and orders for a merchant (TABLEKIT_BOOTSTRAP_SLUG)
	go run ./cmd/seed

run: ## Run the API server locally
	go run ./cmd/server

check: lint test ## Lint and test
