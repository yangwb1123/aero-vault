# Requirements Specification — `internal/access` + `internal/repository`: fix SQL LIKE-wildcard leakage in folder-ACL prefix matching

**Module:** `internal/access` (+ `internal/repository/sql_access_acl.go`)
**Direction:** "Fix SQL LIKE-wildcard leakage in folder-ACL prefix matching (keys containing % or _ match unintended siblings)"
**Source analysis:** `docs/auto/analyses/internal-access-f4571c58.json` (direction 1)
**Date:** 2026-08-07 · **HEAD:** `acfaaf4` (verification basis = this checkout)
**Score:** value 9 / risk reduction 9 / effort 3 / confidence 9

---

## 1. Scope

`ListApplicableACL` (`internal/repository/sql_access_acl.go:55-63`) resolves inherited folder ACLs with:

```sql
OR (resource_kind='folder' AND (resource_key=$4 OR (inherit_acl=1 AND $5 LIKE resource_key || '%')))
```

The `LIKE resource_key || '%'` branch has **no `ESCAPE` clause** (`:61`). Because the pattern is built from a *column* (`resource_key`) rather than a bound literal, any `%` or `_` stored inside a folder ACL key acts as a SQL wildcard at query time. `normalizeACLResource` (`internal/access/manager.go:182`) only trims slashes and appends a trailing `/` for folders — it never validates or escapes wildcard characters, so **any principal with `ActionManageACL` on one folder can write an ACL whose effect silently widens to sibling keys they do not own** (cross-object ACL boundary breach): an allow/deny on folder `report_2026/` also governs `reportX2026/…`, and `50%/` matches every key starting `50`.

This spec scopes exactly: **(1)** replace the vulnerable `LIKE` prefix test with a literal prefix comparison in `ListApplicableACL`, **(2)** reject `%`/`_` in folder ACL keys at `PutACL` time (defense-in-depth), **(3)** the two regression tests demanded by the direction's acceptance criteria. Out of scope (see §4): migrations, bucket/object branches, ordering, config surface, the empty-folder-key edge (pre-existing, behavior-preserved), and the perf-probe's alternative `fixedSQL` design.

---

## 2. Evidence verification

Every citation in the direction was checked against the repository on this commit.

| # | Direction citation | Verified location | Verdict |
|---|---|---|---|
| E1 | `internal/repository/sql_access_acl.go:61` — `$5 LIKE resource_key || '%'`, no ESCAPE | `ListApplicableACL` `:55-63`; vulnerable clause at `:61` exactly as cited | ✅ **exact**. This is the **only** production `LIKE` on resource keys without an escape: `sql_objects_list.go:26,64` and `sql_objects_versions.go:91` all use the repo's `ESCAPE '!'` idiom (caller-side escaping of bound parameters) — the ACL query is the outlier. |
| E2 | `internal/access/manager.go:182` — `normalizeACLResource` only trims slashes | Func def at `:182`; called from `PutACL` at `:125` **before** `validateACL` (`:196`). Body: `TrimPrefix("/")` + trailing-`/` append for folders; no wildcard validation/escaping | ✅ **exact**. `validateACL` (`:196-215`) checks tenant/bucket/action/kind/effect/principal — no key-content rule. |
| E3 | `internal/access/authorizer.go:49` | `ListApplicableACL` call site is at **`:42`** (`entries, err := m.store.ListApplicableACL(...)`); `:49` is the `ListSubjectDepartments` call in the same `Authorize` function | ✅ **symbol correct, line drifted by 7**. The direction's claim "any `ActionManageACL` holder on one folder widens grants" flows through `PutACL` → `m.require(ActionManageACL, …)` (`manager.go:139-141`) → `Authorize` → `ListApplicableACL`, so the boundary breach path is fully cited. |

**Empirical verification (SQLite, reproduced on this machine):**

| Expression | Result | Meaning |
|---|---|---|
| `'ab/x' LIKE 'a_/%'` | **true** | folder ACL `a_/` applies to object `ab/x` — `_` wildcard leak confirmed |
| `'ax/y' LIKE 'a%/%'` | **true** | folder ACL `a%/` applies to `ax/y` — `%` wildcard leak confirmed |
| `'reportX2026/x' LIKE 'report_2026/%'` | **true** | the direction's named example confirmed verbatim |
| `'50x/y' LIKE '50%/%'` | **true** | folder ACL `50%/` matches every key starting `50` |
| `'ab/x' LIKE 'a\_/%' ESCAPE '\'` | false | the ESCAPE alternative works |
| `substr('ab/x',1,length('a_/')) = 'a_/'` | false | literal prefix comparison rejects the sibling — fix verified |

**Problem-statement checks:**

| Statement | Verdict |
|---|---|
| "`%`/`_` inside a folder key act as SQL wildcards; ACL on `report_2026/` applies to `reportX2026/…`" | ✅ **holds** (rows 1-4 above). |
| "`normalizeACLResource` never validates or escapes `%`/`_`" | ✅ **holds** (E2). |
| "Any user with `ActionManageACL` on one folder can widen grants/denies to sibling keys" | ✅ **holds** — `PutACL` requires `ActionManageACL` only on the *folder being written* (`manager.go:139`), and the widened match happens in the store layer (`sql_access_acl.go:61`), outside the caller's control. |
| "Both SQLite and Postgres are affected" | ✅ **holds** — `LIKE` default (no ESCAPE) treats `%`/`_` as wildcards in both dialects; the query is shared (`s.rebind` only rewrites placeholders, I1). |

**Bonus findings (not cited by the direction):**

- `internal/repository/perf_probe_test.go:55-60` embeds the **exact vulnerable clause** in a `const currentSQL` (benchmark baseline) — after REQ-1 the production query and this "current" baseline diverge, so the const must be synced or the probe silently benchmarks the patched-away vulnerability.
- `perf_probe_test.go:62` (`fixedSQL`) and `:83`/`:102` (`filterApplicableACL` / `folderPrefix`) already implement **literal-prefix semantics in Go** (`strings.HasPrefix(key, folderPrefix(entry.Key))`) — independent corroboration from the repo's own perf probe that prefix-with-slash-boundary is the intended matching semantics.
- `idx_resource_acls_lookup` is `(tenant_id, bucket, resource_key, resource_kind)` (migration `0037_enterprise_access.up.sql:47-48`). The folder-prefix branch is **non-sargable today** (pattern from column) and remains non-sargable under REQ-1 — no index regression.
- Edge case, behavior-preserved: a folder ACL with empty key (`resource_key=''`, reachable only by PUTting folder key `/` with bucket-level `ActionManageACL`) matches all keys both before (`'' LIKE '%'`) and after (`substr($5,1,0)='' = ''`) the fix. Pre-existing, requires bucket-wide ACL power to create, unchanged — recorded here so reviewers know it is deliberate.

---

## 3. Requirements

### REQ-1 — Literal prefix comparison in `ListApplicableACL` (the fix)

In `internal/repository/sql_access_acl.go:61`, replace the LIKE branch:

```sql
-- before
OR (resource_kind='folder' AND (resource_key=$4 OR (inherit_acl=1 AND $5 LIKE resource_key || '%')))
-- after
OR (resource_kind='folder' AND (resource_key=$4 OR (inherit_acl=1 AND substr($5, 1, length(resource_key)) = resource_key)))
```

Constraints:

- **Boundary slash preserved.** `normalizeACLResource` guarantees folder keys end in `/` (`manager.go:182-190`), so `substr(key, 1, length(folder_key)) = folder_key` requires the slash: `a/` matches `a/x`, never `ab/x` — identical boundary semantics to today's pattern for well-formed keys, and now literal.
- **`inherit_acl=1` gating, exact-match branch, and `ORDER BY LENGTH(resource_key) DESC` unchanged.** Same 5 placeholders, same bind order → I1 (placeholder rebind) untouched.
- **Portable.** `substr`/`length` on text are standard SQL with identical character semantics in SQLite and Postgres; the `postgres.go` path shares this file.
- **No migration, no data rewrite.** Existing rows with wildcard characters in folder keys remain valid keys; their ACLs simply stop leaking to siblings (backward-compatible hardening).
- **Sync the benchmark baseline:** update `const currentSQL` in `internal/repository/perf_probe_test.go:55-60` to the new clause (same shape, same bind order) so the probe continues to measure the production query.

### REQ-2 — Reject `%`/`_` in folder ACL keys at `PutACL` time (defense-in-depth)

In `validateACL` (`internal/access/manager.go:196-215`), add for `ResourceKind == ResourceFolder`:

> `entry.Key` must not contain `%` or `_` → return `ErrInvalidArgument` with a message naming the offending key and the reason.

Rationale and scope decisions:

- After REQ-1, wildcard characters are **inert** in matching — REQ-2 adds no marginal security against the current query. Its purpose is to hold the invariant *"folder ACL keys never contain SQL wildcard metacharacters"*, so that a future re-introduction of LIKE (or the probe's `fixedSQL` shape) cannot silently regress into this CVE-class hole.
- Applied uniformly to **all** folder ACLs (not only `inherit_acl=true`): a non-inherited folder ACL is exact-match only and harmless today, but a uniform rule is simpler to reason about than a conditional one. Object and bucket keys are **not** restricted — they participate only in exact matches (`resource_key=$3` / bucket branch), where `%`/`_` are inert.
- Trade-off (accepted, documented): a folder literally named with `_` (e.g. `report_2026/`) can no longer receive a **folder-level** ACL via `manager.PutACL`. Object-level ACLs, bucket ACLs, and all file operations on such folders are unaffected; principals with `ActionManageACL` can still grant per-object. See D3 and §6.

### REQ-3 — Regression tests (acceptance AC-1/AC-2, see §5)

- **Repository test** — new file `internal/repository/sql_access_acl_test.go` (package `repository`, sqlite `t.TempDir()` + `Migrate` idiom per `access_cleanup_test.go:16-21`): seed via `store.PutACLEntry`, query via `store.ListApplicableACL`.
- **Manager test** — extend `internal/access/access_test.go` (uses existing `testManager` helper): seed the folder ACL via the repo-backed `access.Store` (REQ-2 forbids `manager.PutACL` for wildcard folder keys — asserted separately), then exercise `manager.Authorize`.

---

## 4. Decisions & non-goals

- **D1 — Prefix comparison over `LIKE … ESCAPE`.** The direction offers either; the repo's own ESCAPE idiom (`ESCAPE '!'`, `sql_objects_list.go:26`) is caller-side (pattern bound as a parameter), which does not fit a *column-derived* pattern. Escaping inside SQL (`replace(replace(resource_key,'!','!!'),'_','!_') || '%' ESCAPE '!'`) is more complex, and escaping-at-write would corrupt stored keys for every exact-match reader (`ListResourceACL`, `GetACLEntry`, `DeleteACL`, cleanup). `substr`/`length` is simpler, portable, and makes the stored key the single source of truth.
- **D2 — Reject, not escape, at PutACL (the "reject/escape" arm).** Escaping at write time would change stored keys and require unescaping at every read site — rejected. Rejection (REQ-2) is the predictable, self-documenting arm.
- **D3 — REQ-2 is the security-engineering call, not a functional mandate.** If operators report that `_`-named folders need folder-level ACLs, REQ-2 can be dropped *without* weakening REQ-1 — the direction's core breach is closed by the query fix alone. This escape hatch is recorded so the trade-off is a conscious one.
- **Non-goals:** no migration/backfill (none needed); no change to bucket/object branches, `ORDER BY`, `resource_acls` schema, or indexes; no change to the empty-folder-key semantics (pre-existing, §2); the perf probe's `fixedSQL` + Go-side `filterApplicableACL` design is not adopted (REQ-1 achieves the same semantics in SQL); no config/env/docs surface; no changes in `internal/service` or adapters (they only consume `Manager.Authorize` / `ListApplicableACL`).

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**AC-1 — New repository test (REQ-3): `ListApplicableACL` with a folder key containing `_` or `%` returns no entries for sibling keys.**
*Testable:* `TestListApplicableACLFolderWildcardIsLiteral` in `internal/repository/sql_access_acl_test.go`. Seed (all `tenant="acme"`, `bucket="default"`, `Inherit: true`, `CreatedAt: now`):
1. `PutACLEntry` folder `report_2026/` (allow read, user alice);
2. `PutACLEntry` folder `50%/` (deny read, user alice).
Assert:
- `ListApplicableACL(ctx, "acme", "default", "reportX2026/x")` → **0 entries** (currently 1 — the `_` leak; fails pre-fix, passes post-fix);
- `ListApplicableACL(ctx, "acme", "default", "50x/y")` → **0 entries** (currently 1 — the `%` leak);
- positive controls: `ListApplicableACL(ctx, "acme", "default", "report_2026/x")` and `…"50%/x"` each → **the respective entry** (proves genuine children still inherit; guards the boundary slash).

**AC-2 — New manager test (REQ-3): `Authorize` (allow on folder `a_/`) denies `ActionRead` on object `ab/x`.**
*Testable:* `TestFolderACLWildcardDoesNotLeakToSiblings` in `internal/access/access_test.go`, using `testManager(t, access.DefaultDeny)`. Seed the `a_/` allow-read-for-alice folder ACL via the repo-backed store (`repo.(access.Store).PutACLEntry`) — **required because REQ-2 forbids `manager.PutACL` for wildcard folder keys** (that rejection is asserted in the same test: `manager.PutACL(admin, folder "a_/", …)` → `errors.Is(err, access.ErrInvalidArgument)`). Then:
- `Authorize(ctx, alice{user}, ActionRead, {acme, default, "ab/x", Object})` → **denied** (`!decision.Allowed`; reason-agnostic — `default_deny` or `resource_acl_no_match` both acceptable) — currently *allowed* via the leak, so the test fails pre-fix;
- positive control: `Authorize(ctx, alice, ActionRead, {acme, default, "a_/x", Object})` → **allowed** (`acl_allow`).

**AC-3 — `go test ./internal/repository ./internal/access -count=1` passes.** (Both new tests above, plus the full existing suites — no regressions.)

**AC-4 — `go vet ./... && gofmt -l internal/access internal/repository` has no output.** (The touched files stay gofmt-clean; `go vet` stays clean — no new symbols are exported.)

All four are the direction's acceptance verbatim, made concrete; they are a subset of the `make check` gate (gofmt/vet/build/test; both changed files remain well under the 500-line limit — `sql_access_acl.go` is 95 lines, the new test file ~70).

---

## 6. Risks

- **REQ-2 functional regression for `_`-named folders** (e.g. `report_2026/` loses folder-level ACLs). Accepted trade-off per D3; the escape hatch (drop REQ-2, keep REQ-1) preserves the security fix. Callers receive `ErrInvalidArgument` → HTTP 400, surfaced by existing adapter error mapping — no new error type.
- **Stale benchmark baseline** — mitigated by the REQ-1 requirement to sync `currentSQL` in `perf_probe_test.go`; forgetting it would leave a probe labeled "current" measuring the old vulnerable query.
- **Postgres parity** — `substr`/`length` on text are identical in both dialects, but the Postgres path is only exercised by the `//go:build integration` gate; run `make test-integration` (or a manual `ListApplicableACL` smoke on Postgres) to confirm before release.
- **Query plan** — the folder-prefix branch is non-sargable before and after (column-derived pattern/comparison; `idx_resource_acls_lookup` cannot serve it either way), so no index regression; `perf_probe_test.go` exists to verify cost parity if it ever matters.
- **Empty-folder-key behavior** — unchanged by design (§2); flagged here because reviewers may notice `substr(key,1,0)=''` is always true. It is not a new hole (the LIKE form matched `'%'` identically) and is only creatable with bucket-wide `ActionManageACL`.

*Verification basis: all line numbers re-confirmed on this checkout (`acfaaf4`); the two wildcard leaks and the proposed fix were empirically reproduced against SQLite during spec writing (§2).*
