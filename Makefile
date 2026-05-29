SHELL := /bin/bash
BIN   := bin/aero-vault
PKG   := ./cmd/server

.PHONY: build run test test-integration tidy clean docker compose-up compose-down

AERO_PG_DSN ?= postgres://aero:aero@localhost:55432/aero?sslmode=disable

build:
	@mkdir -p bin
	go build -trimpath -ldflags "-s -w" -o $(BIN) $(PKG)

run:
	go run $(PKG)

test:
	go test ./...

# Runtime-verify the opt-in Postgres-backed adapters (pgvector, pgFTS,
# LISTEN/NOTIFY) against a throwaway pgvector Postgres container. Excluded from
# the default `test` gate (needs Docker + Postgres, unlike the SQLite CI gate).
test-integration:
	@docker rm -f aero-pg >/dev/null 2>&1 || true
	docker run -d --name aero-pg -e POSTGRES_USER=aero -e POSTGRES_PASSWORD=aero \
	  -e POSTGRES_DB=aero -p 55432:5432 pgvector/pgvector:pg16 >/dev/null
	@echo "waiting for postgres..."; \
	for i in $$(seq 1 30); do docker exec aero-pg pg_isready -U aero -d aero >/dev/null 2>&1 && break; sleep 1; done
	AERO_PG_DSN="$(AERO_PG_DSN)" go test -tags=integration ./internal/integration/ -v; \
	  rc=$$?; docker rm -f aero-pg >/dev/null 2>&1 || true; exit $$rc

tidy:
	go mod tidy

clean:
	rm -rf bin var

docker:
	docker build -t aero-vault:dev .

compose-up:
	docker compose up --build -d

compose-down:
	docker compose down -v
