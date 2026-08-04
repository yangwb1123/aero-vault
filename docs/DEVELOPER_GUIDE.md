# Developer Guide — aero-vault

> Engineering CLI, code standards, and workflow reference.

## Prerequisites

```bash
# Install Go 1.26.1+, Python 3.10+, and Docker (optional).

# One-time setup (installs gocyclo, golangci-lint, pre-commit hook)
python3 cli.py setup
```

## Quick Start

```bash
make build           # compile server binary
make run             # start server (default :8080)
make test            # run all Go tests
python3 cli.py accept  # full engineering gates
```

## Engineering CLI

All code-quality gates are centralized in `python3 cli.py`.

| Command | What it checks | Gate |
|---------|---------------|------|
| `check` | filesize + vet | 🔴 pre-commit |
| `harness` | filesize + complexity + architecture | 🔴 pre-commit |
| `accept` | full HARNESS.md pipeline | 🔴 pre-commit |
| `invariants` | I1–I6 invariants | 🔴 pre-commit |
| `adr-compliance` | ADR-001–005 | 🟡 review |
| `check-exemptions` | no stale file exemptions | 🟡 review |
| `filesize` | ≤ 500 lines per file | 🔴 |
| `complexity` | cyclomatic ≤ 10 (WARN) | 🟡 |
| `architecture` | dependency direction | 🟡 |
| `root-policy` | root dir has no business code | 🔴 |
| `build` | all binaries compile | 🔴 |
| `coverage` | ≥ 50% per package | 📊 |
| `diagnose` | system health check | 📊 |
| `health-report` | project health overview | 📊 |
| `setup` | install dev tools + hooks | 🔧 |

Thresholds are declared in `engineering.yaml`.

## Pre-commit Hook

After `python3 cli.py setup`, `git commit` runs `python3 cli.py accept`.
Skip with `git commit --no-verify` (emergency only).

## Code Standards

1. **Files ≤ 500 lines** — refactor before adding new code
2. **Cyclomatic complexity ≤ 10** — WARN only, address during review
3. **No utils/common/helper packages** — organize by domain
4. **Stdlib first** — new `go.mod` dependency needs justification
5. **SQL placeholders are unique** — each `$N` appears once per query
6. **Migration files are paired** — every `.up.sql` needs `.down.sql`
7. **Handler tests use httptest** — no middleware chain in unit tests
8. **Acceptance must pass** before commit

## Troubleshooting

```bash
# gocyclo not found
python3 cli.py setup

# "gofmt: command not found"
# Install Go: https://go.dev/dl/

# Tests fail with "too many open files"
ulimit -n 4096
```
