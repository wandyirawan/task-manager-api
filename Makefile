.PHONY: run build test vet tidy setup check-env \
        docker-build docker-push deploy run-prod run-prod-down \
        migrate-up migrate-down

# ---- image / registry config ----
IMAGE    ?= task-manager-api
TAG      ?= latest
REGISTRY ?=

# ---- database / migrations ----
# Connection string used by the golang-migrate CLI (PostgreSQL).
DB_URL ?= postgres://tm_user:tm_pass@localhost:5432/tmapi?sslmode=disable

# Prefer the migrate CLI on PATH; fall back to GOPATH/bin (installed via
# `go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest`).
MIGRATE ?= $(shell command -v migrate 2>/dev/null || echo "$(shell go env GOPATH)/bin/migrate")

# ---- prerequisites ----------------------------------------------------------
# setup installs the local tooling this project needs. Docker Compose is the
# supported path for the database itself, so the only hard Go-side extra is
# the migrate CLI (and only if you plan to run migrations against an external
# database — the binary auto-migrates at boot anyway).
setup:
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
	@echo "==> done. Next: cp .env.example .env, edit JWT_SECRET, then make run-prod"

run:
	PORT=8080 \
	ENV=dev \
	JWT_SECRET=demo-secret-for-testing \
	go run ./cmd/api

build:
	go build -o bin/api ./cmd/api

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

# ---- docker / deploy ----
docker-build:
	docker build -t $(IMAGE):$(TAG) .

docker-push:
	@if [ -z "$(REGISTRY)" ]; then \
		echo "REGISTRY is empty — skipping docker push (set REGISTRY=host/user to push)"; \
	else \
		docker tag $(IMAGE):$(TAG) $(REGISTRY)/$(IMAGE):$(TAG) && \
		docker push $(REGISTRY)/$(IMAGE):$(TAG); \
	fi

# deploy = build + push the image (NOT run).
deploy: docker-build docker-push

# run-prod brings up the full API + Postgres stack via compose.
run-prod:
	docker compose up -d --build

run-prod-down:
	docker compose down

# ---- migrations (manual / external DB; the binary auto-migrates at boot) ----
migrate-up:
	$(MIGRATE) -path migrations -database "$(DB_URL)" up

migrate-down:
	$(MIGRATE) -path migrations -database "$(DB_URL)" down
