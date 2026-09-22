.PHONY: help setup dev dev-down dev-restart test test-race test-all \
        check lint build run watch docker-build docker-push deploy \
        run-prod run-prod-down migrate-up migrate-down

# Default target: show the workflow instead of doing something surprising.
.DEFAULT_GOAL := help

# ---- image / registry config ----
IMAGE    ?= task-manager-api
TAG      ?= latest
REGISTRY ?=

# ---- database / migrations ----
# Connection string used by the golang-migrate CLI (PostgreSQL).
DB_URL ?= postgres://tm_user:tm_pass@localhost:5432/tmapi?sslmode=disable

# TEST_DB_URL points at the ADMIN database — the suite creates its own
# throwaway databases (tmapi_test_* / tmapi_race_*) and drops them after.
TEST_DB_URL ?= postgres://tm_user:tm_pass@localhost:5432/postgres?sslmode=disable

# Prefer the migrate CLI on PATH; fall back to GOPATH/bin (installed via
# `go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest`).
MIGRATE ?= $(shell command -v migrate 2>/dev/null || echo "$(shell go env GOPATH)/bin/migrate")

# ---- help -------------------------------------------------------------------
# help lists every target grouped by workflow. It is the default `make` target
# so newcomers see the intended order before they run anything.
help:
	@echo "Task Manager API — development workflow"
	@echo ""
	@echo "  First time setup (once):"
	@echo "    1. make setup      — check go/docker, install migrate CLI"
	@echo "    2. cp .env.example .env && edit JWT_SECRET"
	@echo ""
	@echo "  Daily development loop:"
	@echo "    make dev           — start Postgres (docker), ready for tests and 'make run'"
	@echo "    make check         — vet + test + build (fast local gate, what CI runs)"
	@echo "    make test-race     — full suite with -race (the real gate)"
	@echo "    make run           — run API locally on :8080 (Swagger at /swagger/index.html)"
	@echo ""
	@echo "  Full targets:"
	@$(MAKE) -s --no-print-directory list-targets 2>/dev/null || true
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "    \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# ---- prerequisites ----------------------------------------------------------
# setup installs the local tooling this project needs. Docker Compose is the
# supported path for the database itself, so the only hard Go-side extra is
# the migrate CLI (and only if you plan to run migrations against an external
# database — the binary auto-migrates at boot anyway).
setup: ## check prerequisites and install the migrate CLI if missing
	@echo "==> Checking prerequisites..."
	@command -v go >/dev/null || { echo "MISSING: Go 1.27+ — https://go.dev/dl/"; exit 1; }
	@command -v docker >/dev/null || { echo "MISSING: Docker — https://docs.docker.com/get-docker/"; exit 1; }
	@echo "==> go:      $$(go version)"
	@echo "==> docker:  $$(docker --version | cut -d' ' -f1-3)"
	@if command -v migrate >/dev/null || [ -x "$(MIGRATE)" ]; then \
		echo "==> migrate: $$(migrate --version 2>/dev/null || echo "$(MIGRATE)")"; \
	else \
		echo "==> migrate: not found — installing via go install..."; \
		go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest; \
		echo "==> migrate: installed to $(shell go env GOPATH)/bin/migrate"; \
	fi
	@echo "==> done. Next: cp .env.example .env, edit JWT_SECRET, then make dev"

# ---- development database ----------------------------------------------------
# dev starts ONLY the Postgres service — the API itself runs on the host with
# `make run` for a fast edit-restart loop. Compose health-gates on pg_isready,
# so when this returns the DB is actually ready to accept queries.
dev: ## start local Postgres via docker compose (only the db service)
	@docker compose up -d db
	@echo "==> Postgres ready on localhost:5432 (tmapi / tm_user)"

dev-down: ## stop the local Postgres service
	@docker compose rm -sf db

# dev-restart wipes the Postgres volume for a clean database state. Destructive!
dev-restart: ## recreate Postgres with a FRESH volume (drops all data)
	@docker compose down -v
	@docker compose up -d db
	@echo "==> Postgres recreated with a fresh volume."

# ---- testing -----------------------------------------------------------------
# test runs the fast suite. DB-backed packages skip cleanly when TEST_DB_URL
# is unreachable, so this works even before `make dev`.
test: ## run unit tests (DB-backed tests skip without Postgres)
	TEST_DB_URL="$(TEST_DB_URL)" go test ./...

# test-race is the real gate: race detector over the full suite.
test-race: ## run full test suite with -race
	TEST_DB_URL="$(TEST_DB_URL)" go test -race ./...

# test-all adds the anti-flaky -count=2 pass on the race suite.
test-all: ## test + race + anti-flaky (-count=2) — what CI should run
	TEST_DB_URL="$(TEST_DB_URL)" go test ./...
	TEST_DB_URL="$(TEST_DB_URL)" go test -race ./...
	TEST_DB_URL="$(TEST_DB_URL)" go test -race -count=2 ./internal/api/handler -run TestRace_

# check is the quick pre-push gate: static check + tests + build.
check: ## vet + test + build (quick gate before pushing)
	go vet ./...
	$(MAKE) -s test
	go build ./...
	@echo "==> check passed."

lint: vet ## alias: static check only

# ---- build / run -------------------------------------------------------------
build: ## build the API binary to bin/api
	go build -o bin/api ./cmd/api

run: ## run the API locally on :8080 (needs `make dev` once for Postgres)
	PORT=8080 \
	ENV=dev \
	JWT_SECRET=demo-secret-for-testing \
	go run ./cmd/api

# watch is a convenience alias for the common air-style loop (optional tool).
watch: ## rebuild+restart on save (requires air: go install github.com/air-verse/air@latest)
	air --build.cmd "go build -o bin/api ./cmd/api" --build.bin "./bin/api"

tidy:
	go mod tidy

# ---- docker / deploy ----
docker-build: ## build the docker image
	docker build -t $(IMAGE):$(TAG) .

docker-push: ## push the image when REGISTRY is set (skip otherwise)
	@if [ -z "$(REGISTRY)" ]; then \
		echo "REGISTRY is empty — skipping docker push (set REGISTRY=host/user to push)"; \
	else \
		docker tag $(IMAGE):$(TAG) $(REGISTRY)/$(IMAGE):$(TAG) && \
		docker push $(REGISTRY)/$(IMAGE):$(TAG); \
	fi

# deploy = build + push the image (NOT run).
deploy: docker-build docker-push ## build + push image (deploy artifact only)

# run-prod brings up the full API + Postgres stack via compose.
run-prod: ## run the full stack (api + postgres) via docker compose
	docker compose up -d --build

run-prod-down: ## stop the full stack
	docker compose down

# ---- migrations (manual / external DB; the binary auto-migrates at boot) ----
migrate-up: ## apply migrations to DB_URL (manual; auto-migrate runs at boot)
	$(MIGRATE) -path migrations -database "$(DB_URL)" up

migrate-down: ## roll back the last migration on DB_URL
	$(MIGRATE) -path migrations -database "$(DB_URL)" down
