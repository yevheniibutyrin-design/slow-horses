# Every target runs through Docker — no Go toolchain needed on the host.

# Pick up local overrides (API_HOST_PORT etc.) the same way docker compose does.
-include .env

GO_IMAGE ?= golang:1.27-alpine
API_HOST_PORT ?= 5000
MONGO_DB ?= slowhorses
BASE_URL ?= http://localhost:$(API_HOST_PORT)
# Reused module cache so containerised go commands stay fast.
GO_RUN = docker run --rm -v "$(PWD)":/src -w /src -v slow-horses-gomod:/go/pkg/mod $(GO_IMAGE)

.PHONY: help up down stop start restart logs ps watch reseed tidy vet build test smoke seed seed-fresh mongosh

help: ## Show available targets
	@grep -hE '^[a-z-]+:.*##' $(firstword $(MAKEFILE_LIST)) | sed 's/:.*##/\t/' | awk -F'\t' '{printf "  %-10s %s\n", $$1, $$2}'

up: ## Build and start api + mongo + swagger-ui
	docker compose up -d --build
	docker compose ps

down: ## Stop everything, keep the database volume
	docker compose down

stop: ## Stop the containers but keep them, so `make start` resumes in place
	docker compose stop
	docker compose ps

start: ## Start containers stopped by `make stop`, without rebuilding
	docker compose start
	docker compose ps

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

test: ## go test ./... against the compose Mongo (in a container)
	docker compose up -d mongo
	@docker run --rm -v "$(PWD)":/src -w /src -v slow-horses-gomod:/go/pkg/mod \
	  --network "container:$$(docker compose ps -q mongo)" \
	  -e MONGO_TEST_URI=mongodb://localhost:27017 \
	  $(GO_IMAGE) go test ./...

mongosh: ## Open a mongo shell on the application database
	docker compose exec mongo mongosh $(MONGO_DB)

smoke: ## Drive a full duel against a running stack
	@./scripts/smoke.sh $(BASE_URL)

seed: ## Add ten open duels via the API (additive)
	@./scripts/seed.sh $(BASE_URL)

seed-fresh: ## Empty the rooms collection, then seed ten open duels
	@echo "Emptying the rooms collection in $(MONGO_DB)..."
	@docker compose exec -T mongo mongosh $(MONGO_DB) --quiet \
	  --eval 'print("  removed " + db.rooms.deleteMany({}).deletedCount + " rooms")'
	@./scripts/seed.sh $(BASE_URL)
