SHELL := /bin/bash
BIN   := bin/aero-vault
PKG   := ./cmd/server
GOBIN := $(shell go env GOPATH)/bin

.PHONY: build run test cover test-integration test-integration-qdrant tidy clean check fmt lint vet vet-integration complexity-lines coverage docker compose-up compose-down web-install web-test web-build web-check

AERO_PG_DSN ?= postgres://aero:aero@localhost:55432/aero?sslmode=disable

build:
	@mkdir -p bin
	go build -trimpath -ldflags "-s -w" -o $(BIN) $(PKG)

run:
	go run $(PKG)

# Iris UI is consumed from its published npm packages and embedded into the Go
# binary from internal/webui/static/app. It remains outside the zero-network Go
# `make check` gate; release builds should run `make web-check` first.
web-install:
	cd web && pnpm install --frozen-lockfile

web-test:
	cd web && pnpm test

web-build:
	cd web && pnpm build

web-check: web-test web-build

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

# Runtime-verify the Qdrant adapter against a throwaway Qdrant container.
# Excluded from the default `test` gate (needs Docker + Qdrant).
AERO_QDRANT_URL ?= http://localhost:6333

test-integration-qdrant:
	@docker rm -f aero-qdrant >/dev/null 2>&1 || true
	docker run -d --name aero-qdrant -p 6333:6333 qdrant/qdrant >/dev/null
	@echo "waiting for qdrant..."; \
	for i in $$(seq 1 30); do curl -sf http://localhost:6333/readyz >/dev/null 2>&1 && break; sleep 1; done
	AERO_QDRANT_URL="$(AERO_QDRANT_URL)" go test -tags=integration ./internal/integration/ -v -run TestQdrant; \
	  rc=$$?; docker rm -f aero-qdrant >/dev/null 2>&1 || true; exit $$rc

# ──────────────────────────────────────────────
# Engineering CLI (python cli.py)
# ──────────────────────────────────────────────

# The cli.py entry point delegates to checks/ modules.
# Use `python cli.py help` for full command list.

ENGINE := python3 cli.py

cli-check:
	@$(ENGINE) check

cli-harness:
	@$(ENGINE) harness

cli-health:
	@$(ENGINE) health-report

cli-fmt:
	@$(ENGINE) fmt

cli-build:
	@$(ENGINE) build

cli-invariants:
	@$(ENGINE) invariants

cli-accept:
	@$(ENGINE) accept

# Run E2E tests against a real server (auto-starts binary)
test-e2e:
	@echo "=== E2E Tests ==="
	@python3 tests/run_all.py --manage

# Full acceptance: aligns with HARNESS.md
accept: cli-accept

# ──────────────────────────────────────────────
# Coverage
# ──────────────────────────────────────────────

cover:
	@mkdir -p bin
	go test -coverprofile=bin/cover.out ./...
	@echo ""
	go tool cover -func=bin/cover.out | sort -k3 -rn | head -30
	@echo ""
	go tool cover -func=bin/cover.out | tail -3

.PHONY: cover

cover-html: cover
	go tool cover -html=bin/cover.out -o bin/coverage.html
	@echo "HTML 报告: bin/coverage.html"

.PHONY: cover-html

# ──────────────────────────────────────────────
# Harness — 提交前必须通过
# ──────────────────────────────────────────────

test-race:
	@echo "[check] data race detection (this may take 2-5x longer) ..."
	# cmd/server included: package-main E2E suites (governance_e2e_test.go,
	# http_test.go, readyz_drill_test.go, mcp-governance E2E) exercise the
	# production wiring order and must run under the detector (measured +20s
	# vs ./internal/... alone, 2026-08-08).
	go test -race -count=1 -timeout 300s ./cmd/server/ ./internal/...
	@echo "  OK (no races detected)"

.PHONY: test-race

# Scoped race detection for the metadata-key atomicity packages (repository +
# its scrub caller) plus the audit-governance degraded-cache contract (the
# (degraded, age) pair discipline is provable only under -race). Full
# `test-race` stays opt-in (takes ~5-10 min); both use 300s per-package —
# the full-gate 120s budget provably fails under -race on 4 heavy packages
# (repository alone needs ~124s, verified 2026-08-08).
test-race-meta:
	@echo "[check] data race detection (metadata atomicity + audit-governance cache) ..."
	go test -race -count=1 -timeout 300s ./internal/repository/ ./internal/reconcile/ ./internal/auditgovernance/
	@echo "  OK (no races detected)"

.PHONY: test-race-meta

# Scoped race detection for the thumbnail decode semaphore (short mode: the
# deterministic slot-contract tests run; the 8192²×16 aggregate-memory test
# needs ~1.2 GiB and ~97s under -race, so it stays behind testing.Short()).
test-race-thumbnail:
	@echo "[check] data race detection (thumbnail decode semaphore) ..."
	go test -race -short -count=1 -timeout 180s ./internal/thumbnail/
	@echo "  OK (no races detected)"

.PHONY: test-race-thumbnail

# REST cache path under -race: the server-side thumbnail LRU is consulted on
# the request path (Get/Put under a mutex) and the cache-specific handler
# tests pin the concurrency contract; race them without the full (slow) REST
# suite by scoping to the cache test names.
test-race-rest-cache:
	@echo "[check] data race detection (REST thumbnail cache path) ..."
	go test -race -count=1 -timeout 300s -run 'ThumbnailCache|Thumbnail.*(Cache|Revalid)' ./internal/api/rest/
	@echo "  OK (no races detected)"

.PHONY: test-race-rest-cache

check: fmt vet vet-integration build test test-race-meta test-race-thumbnail test-race-rest-cache cli-check

.PHONY: dev

dev: build
	@echo "  Starting server (Ctrl+C to stop)..."
	@./bin/aero-vault

.PHONY: check

fmt:
	@echo "[check] gofmt ..."; \
	output=$$(gofmt -l .); \
	if [ -n "$$output" ]; then \
		echo "  gofmt 需要格式化以下文件:"; \
		echo "$$output"; \
		exit 1; \
	fi; \
	echo "  OK"

vet:
	@echo "[check] go vet ..."; \
	go vet ./...; \
	echo "  OK"

# Zero-Docker compile gate for -tags=integration test files. `go vet`
# type-checks *_test.go (which `go build` never does), so integration-tagged
# tests (e.g. internal/integration G2b/G2c) cannot rot un-compiled. Compile-
# only: no Docker, no network, no DB connection — consistent with the
# zero-Docker `make check` gate. Runtime PG coverage stays opt-in:
# `make test-integration` (local Docker) / workflow_dispatch CI job.
vet-integration:
	@echo "[check] go vet -tags=integration ..."; \
	go vet -tags=integration ./...; \
	echo "  OK"

complexity-lines:
	@echo "[check] 圈复杂度 (max 10, 仅生产代码) ..."; \
	files=$$(find . -name '*.go' -not -name '*_test.go' -not -path './vendor/*'); \
	if [ -z "$$files" ]; then echo "  no files"; exit 0; fi; \
	output=$$($(GOBIN)/gocyclo -over 10 $$files 2>/dev/null); \
	if [ -n "$$output" ]; then \
		echo "$$output" | head -20; \
		count=$$(echo "$$output" | wc -l); \
		echo "  WARN: 存在 $$count 个圈复杂度 > 10 的函数（建议后续重构）"; \
	else \
		echo "  OK"; \
	fi
	@echo "[check] 单文件行数 (max 500) ..."; \
	output=$$(find . -name '*.go' -not -name '*_test.go' -not -path './vendor/*' -exec awk 'NR==1{lines=0} {lines++} ENDFILE{if(lines>500) print FILENAME":"lines"行"}' {} +); \
	if [ -n "$$output" ]; then \
		echo "$$output"; \
		echo "  FAIL: 存在超过 500 行的文件"; \
		exit 1; \
	fi; \
	echo "  OK"

tidy:
	go mod tidy

install-tools:
	@which gocyclo >/dev/null 2>&1 || go install github.com/fzipp/gocyclo/cmd/gocyclo@latest

clean:
	rm -rf bin var

docker:
	docker build -t aero-vault:dev .

compose-up:
	docker compose up --build -d

compose-down:
	docker compose down -v
