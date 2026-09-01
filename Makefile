# Every target runs through Docker — no Go toolchain needed on the host.

# Pick up local overrides (API_HOST_PORT etc.) the same way docker compose does.
-include .env

GO_IMAGE ?= golang:1.27-alpine
API_HOST_PORT ?= 5000
MONGO_DB ?= slowhorses
BASE_URL ?= http://localhost:$(API_HOST_PORT)
# Reused module cache so containerised go commands stay fast.
GO_RUN = docker run --rm -v "$(PWD)":/src -w /src -v slow-horses-gomod:/go/pkg/mod $(GO_IMAGE)

.PHONY: help up down restart logs ps watch reseed tidy vet build smoke mongosh

help: ## Show available targets
	@grep -hE '^[a-z-]+:.*##' $(firstword $(MAKEFILE_LIST)) | sed 's/:.*##/\t/' | awk -F'\t' '{printf "  %-10s %s\n", $$1, $$2}'

up: ## Build and start api + mongo + swagger-ui
	docker compose up -d --build
	docker compose ps

down: ## Stop everything, keep the database volume
	docker compose down

restart: ## Rebuild and restart just the API after a code change
	docker compose up -d --build api

logs: ## Follow the API logs
	docker compose logs -f api

ps: ## Show service status
	docker compose ps

watch: ## Auto-rebuild the API on file changes
	docker compose watch

reseed: ## Wipe the database volume and re-run mongo-init.js
	docker compose down -v
	docker compose up -d --build
	docker compose ps

tidy: ## go mod tidy (in a container)
	$(GO_RUN) go mod tidy

vet: ## go vet ./... (in a container)
	$(GO_RUN) go vet ./...

build: ## go build ./... (in a container)
	$(GO_RUN) go build ./...

mongosh: ## Open a mongo shell on the application database
	docker compose exec mongo mongosh $(MONGO_DB)

smoke: ## Exercise every endpoint against a running stack
	@./scripts/smoke.sh $(BASE_URL)
