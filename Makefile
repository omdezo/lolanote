# QomraNote — operational commands.
# Windows users: run these from Git Bash, or install make via `winget install
# ezwinports.make`; every target is a thin wrapper you can also run by hand.

COMPOSE := docker compose
GO      := go
NPM     := npm

.PHONY: help up down restart rebuild logs ps \
        dev-api dev-web tidy build test vet migrate seed \
        typecheck web-build clean

help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS=":.*## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

## ---- full stack (docker) ----

up: ## Build and start the whole stack (web :3000, api :8080, keycloak :8081)
	$(COMPOSE) up -d --build

down: ## Stop the stack (volumes survive)
	$(COMPOSE) down

restart: ## Restart the stack
	$(COMPOSE) restart

rebuild: ## Force-rebuild images and restart
	$(COMPOSE) up -d --build --force-recreate

logs: ## Tail all service logs
	$(COMPOSE) logs -f --tail=100

ps: ## Show service status
	$(COMPOSE) ps

## ---- backend (Go) ----

dev-api: ## Run the API locally against the dockerized mongo/keycloak
	cd backend && $(GO) run ./cmd/qomranote serve

tidy: ## go mod tidy
	cd backend && $(GO) mod tidy

build: ## Compile the backend binary
	cd backend && $(GO) build -o bin/qomranote ./cmd/qomranote

test: ## Run backend tests
	cd backend && $(GO) test ./...

vet: ## Static-check the backend
	cd backend && $(GO) vet ./...

migrate: ## Ensure Mongo indexes + purge expired trash
	cd backend && $(GO) run ./cmd/qomranote migrate

seed: ## Seed the built-in template board library
	cd backend && $(GO) run ./cmd/qomranote seed

agent-check: ## Run the AI agent live against a synthetic board (needs a provider key)
	cd backend && $(GO) run ./cmd/qomranote agent-check

agent-check-dry: ## Print the context the agent would see, without calling a model
	cd backend && $(GO) run ./cmd/qomranote agent-check --dry-run

fmt: ## Format the backend
	cd backend && gofmt -w ./internal ./cmd

## ---- frontend (TypeScript) ----

dev-web: ## Vite dev server on :5173 (proxies to the local API)
	cd frontend && $(NPM) run dev

typecheck: ## Frontend type-check
	cd frontend && $(NPM) run typecheck

web-build: ## Production frontend build
	cd frontend && $(NPM) run build

## ---- remote access ----

allow-origin: ## Let an extra origin log in: make allow-origin ORIGIN=https://x.trycloudflare.com
	./deploy/allow-origin.sh $(ORIGIN)

tunnel: ## Public HTTPS URL for port 3000 (needs cloudflared on PATH)
	cloudflared tunnel --url http://localhost:3000 --no-autoupdate

## ---- gates ----

verify: ## Everything CI runs: format check, vet, tests, type-check, web build
	cd backend && test -z "$$(gofmt -l ./internal ./cmd)" || (echo "gofmt: files need formatting" && exit 1)
	cd backend && $(GO) vet ./...
	cd backend && $(GO) test ./...
	cd frontend && $(NPM) run typecheck
	cd frontend && $(NPM) run build

## ---- housekeeping ----

clean: ## Remove build artifacts
	rm -rf backend/bin frontend/dist
