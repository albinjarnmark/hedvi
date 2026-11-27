# Hedvi developer commands. Run `make help` for a list.
SHELL := /bin/bash
API := apps/api
WEB := apps/web

.PHONY: help up down reset migrate seed dev api worker web test test-go test-web lint lint-go lint-web contracts build fmt

help: ## Show commands
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

up: ## Start PostgreSQL and Valkey (docker compose)
	docker compose up -d --wait

down: ## Stop local dependencies
	docker compose down

reset: ## Delete local database and blobs (destructive, local only)
	docker compose down -v && rm -rf $(API)/.data

migrate: ## Apply database migrations
	cd $(API) && go run ./cmd/hedvi migrate

seed: ## Create the local demo account (prints keys)
	cd $(API) && go run ./cmd/hedvi admin seed-local

dev: up ## Run API + workers with the local SES/SQS simulator
	cd $(API) && go run ./cmd/hedvi dev

api: ## Run only the API
	cd $(API) && go run ./cmd/hedvi api

worker: ## Run only the workers
	cd $(API) && go run ./cmd/hedvi worker

web: ## Run the Next.js web app on :3030
	cd $(WEB) && pnpm dev

test: test-go test-web ## Run all tests

test-go: up ## Go unit and integration tests (needs `make up`)
	cd $(API) && go test -race -count=1 ./...

test-web: ## Web type check, lint and unit tests
	cd $(WEB) && pnpm typecheck && pnpm lint && pnpm test

lint: lint-go lint-web ## Linters

lint-go:
	cd $(API) && gofmt -l . | (! grep .) && go vet ./...

lint-web:
	cd $(WEB) && pnpm lint

contracts: ## Pinned Resend SDK contract tests against a running `make dev`
	cd contracts/node && pnpm test
	cd contracts/python && ./run.sh

build: ## Build the Go binary and the web app
	cd $(API) && CGO_ENABLED=0 go build -trimpath -o bin/hedvi ./cmd/hedvi
	cd $(WEB) && pnpm build

fmt: ## Format code
	cd $(API) && gofmt -w .
