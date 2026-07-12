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

## ---- frontend (TypeScript) ----

dev-web: ## Vite dev server on :5173 (proxies to the local API)
	cd frontend && $(NPM) run dev

typecheck: ## Frontend type-check
	cd frontend && $(NPM) run typecheck

web-build: ## Production frontend build
	cd frontend && $(NPM) run build

## ---- housekeeping ----

clean: ## Remove build artifacts
	rm -rf backend/bin frontend/dist
