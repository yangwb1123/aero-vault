---
name: project-structure-cleanup
description: Systematic root-directory cleanup for Go/Node/Rust projects — move agent context files, deployment configs, and leaked binaries into proper subdirectories with full reference tracking
source: auto-skill
extracted_at: '2026-06-18T08:39:14.546Z'
---

# Skill: Project Structure Cleanup (Structural Refactoring)

## Goal

Reorganize a project's root directory to minimize clutter, moving scattered files into proper subdirectories without breaking any references or build pipelines.

## When to Use

- Root directory has >15 non-essential files
- Agent context files (BOOTSTRAP.md, CURRENT_SPRINT.md, TASK.md) clutter root
- Deployment configs scattered across root instead of `deploy/`
- Leaked build artifacts (binaries, .pyc, dist/) in root
- Before starting a new Sprint to clean up accumulated mess

## Allowed Root Files (Allowlist)

Only these file types belong in root:

| Allowed | Examples |
|---------|---------|
| Project manifest | go.mod, package.json, pyproject.toml, Cargo.toml |
| Build entry | Makefile, Dockerfile, docker-compose.yml |
| Config template | .env.example |
| VCS | .gitignore |
| Docs | README.md, AGENTS.md, HARNESS.md |
| Entry dirs | cmd/, src/, internal/, app/ |
| Tool config | .github/, .qwen/, .claude/ |

Everything else must move to a subdirectory.

## Procedure

### Phase 1: Analyze

1. List all root-level files and directories
2. Classify each as: ✅ allowed / 🟡 movable / 🔴 must-delete
3. Identify leaked artifacts: `file <name>` to detect binaries, check `git ls-files <name>` for tracking status
4. Propose target structure and get confirmation

### Phase 2: Execute Moves

For each moved file, follow this **mandatory reference-tracking sequence**:

#### Step A: Move the file

```bash
mkdir -p <target-dir>
mv <file> <target-dir>/
```

#### Step B: Find ALL references across the entire codebase

```bash
grep -r '<old-filename>' --include='*.go' --include='*.md' --include='*.yml' \
  --include='*.yaml' --include='Makefile' --include='*.sh' .
```

**Critical**: grep across ALL file types, not just code. Markdown docs, CI configs, shell scripts, and compose files all may reference the old path.

#### Step C: Update references

For each reference found, update the path. Common patterns:

| File type | What to update |
|-----------|---------------|
| AGENTS.md | Context chain loading paths |
| README.md | `docker compose -f` commands, file mentions |
| docs/*.md | File references and code examples |
| docker-compose.yml | `build:` context, volume mount paths |
| Shell scripts | File paths in commands |
| CI workflows | File paths in steps |

#### Step D: Fix relative paths inside moved files

When moving docker-compose files:
- `build: .` → `build: ..` (context moves up one level)
- `./deploy/config.yaml` → `./config.yaml` (paths now relative to new location)
- Comments referencing old paths need updating too

When moving markdown files:
- Cross-references to other docs may need path adjustment

### Phase 3: Update Defenses

1. Add deleted artifacts to `.gitignore` (e.g., `/server` for leaked binaries)
2. Verify no tracked files were accidentally deleted

### Phase 4: Verify

Run the **full** verification chain — not just build:

```bash
# 1. Format check
gofmt -l .

# 2. Build
go build ./...

# 3. Static analysis
go vet ./...

# 4. Tests
go test ./...
```

**Pre-existing bugs**: Verification often reveals build errors that existed before the refactor (e.g., function signature mismatches). Fix them as part of the same pass — do NOT blame the refactor or leave them for later.

### Phase 5: Fix any discovered issues

- gofmt violations → `gofmt -w <files>`
- Build errors → fix signature mismatches, missing imports
- Test failures → update test helpers with new signatures

## Common Pitfalls

| Pitfall | Consequence | Prevention |
|---------|------------|------------|
| Moving compose file without updating `build:` path | Docker build fails | Always check `build:` and volume mounts |
| Forgetting to grep markdown docs | Stale references in documentation | grep ALL file types, not just code |
| Not adding deleted binary to .gitignore | Binary reappears next build | Update .gitignore immediately |
| Skipping gofmt after structural changes | CI format check fails | Run `gofmt -l .` as first verification step |
| Leaving pre-existing build errors unfixed | Verification appears to fail due to refactor | Fix everything in the same pass |

## Output Format

```markdown
## Refactoring Summary

**New directories:** ...
**Moved files:** ...
**Modified files:** ... (with reason for each)
**Deleted files:** ...

## Architecture Impact

**Problems solved:** ...
**Remaining issues:** ...
**Suggested follow-ups:** ...
```

## Key Insight

> The hardest part of structural refactoring is NOT moving files — it's finding and updating every reference. A single missed reference in a doc or compose file breaks the user experience silently. Always grep before and after.
