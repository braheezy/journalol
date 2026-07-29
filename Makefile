JOURNALOL_ADDR ?= 127.0.0.1:8080
JOURNALOL_DB_PATH ?= ./journalol.db
JOURNALOL_TIMEZONE ?= UTC
JOURNALOL_DEMO ?= true

.PHONY: run demo build test fmt vet check docker-up docker-down docker-logs

run:
	JOURNALOL_ADDR="$(JOURNALOL_ADDR)" \
	JOURNALOL_DB_PATH="$(JOURNALOL_DB_PATH)" \
	JOURNALOL_TIMEZONE="$(JOURNALOL_TIMEZONE)" \
	JOURNALOL_DEMO="$(JOURNALOL_DEMO)" \
	go run ./cmd/journalol

demo:
	JOURNALOL_ADDR="$(JOURNALOL_ADDR)" \
	JOURNALOL_DB_PATH="$(JOURNALOL_DB_PATH)" \
	JOURNALOL_TIMEZONE="$(JOURNALOL_TIMEZONE)" \
	JOURNALOL_DEMO=true \
	go run ./cmd/journalol

build:
	mkdir -p bin
	go build -o bin/journalol ./cmd/journalol

test:
	go test ./...

fmt:
	go fmt ./...

vet:
	go vet ./...

check: fmt vet test

docker-up:
	docker compose up --build

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f app
