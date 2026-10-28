# Product usability v1 — bounded file-service delivery

## Authorization and product boundary
User authorized local gated commit/merge into main and bounded 24h continuation on 2026-10-28. No push/deploy.
Window: 2026-10-28T08:54:43Z → 2026-10-29T08:54:43Z; preparation counts. Model/provider: openai-codex/gpt-6.1-sol, thinking=max, two isolated writers.
Goal: small-team file storage, find, controlled sharing and recovery. No Space grants, employee policy, KB/Processing/Gateway platform or new services.
Historical Wave7 source is inherited, not accepted; old red gates remain evidence. This document is requirements, not a PASS claim.

## Persistence and compatibility
Reuse existing FileService, repository, storage, authentication and frontend request fences. No new schema/migrations, dependencies, configuration names, model/Storage/Repository/spaces.Store interface changes.
Token remains memory-only. UI capability hints are not authorization. Bound/fault fails closed; unknown Blob/chunk/index ownership is never inferred or automatically deleted.
Preserve all old test oracles, values, scales, timeouts, PRAGMA/sync/connections/TMPDIR; no schema caching or fixture timing/compression changes to green.
Copy/move semantics, if selected, remain explicit; no atomic-rename claim. Downloads handed to browser are not confirmed saved. Cancellation is not rollback.

## Exact scheduled source/test slices

### Round 1: Resolve inherited acceptance uncertainty and make primary navigation accurately describe current capabilities.
- A: Bounded REST race-budget diagnosis; conditional evidence-backed repair
- `internal/api/rest/idempotency.go`
- `internal/api/rest/idempotency_boundary.go`
- `internal/api/rest/idempotency_cached_response.go`
- `internal/api/rest/router_boundary.go`
- `internal/api/rest/space_boundary_fixture_test.go`
- `internal/api/rest/space_boundary_replay_test.go`
- `internal/api/rest/idempotency_boundary_fixture_test.go`
- `internal/api/rest/idempotency_boundary_test.go`
- `internal/api/rest/rest_race_budget_test.go`
- B: Session-aware navigation and honest storage/export context
- `web/src/Shell.tsx`
- `web/src/navigation.ts`
- `web/src/navigation.test.ts`
- `web/src/Shell.navigation.test.tsx`
- `web/src/hooks/useSessionNavigation.ts`
- `web/src/hooks/useSessionNavigation.test.tsx`
- `web/src/pages/OverviewPage.tsx`
- `web/src/pages/OverviewPage.context.test.tsx`
- `web/src/pages/access/AccessPage.tsx`
- `web/src/pages/access/BackupPanel.tsx`
- `web/src/pages/access/BackupPanel.naming.test.tsx`
- Acceptance: COMMON, including independent review of the entire inherited 72-path source plus approved round deltas.
- Acceptance: REST report distinguishes cumulative package exhaustion from the interrupted test; all old thumbnail bytes and original full-race settings remain unchanged.
- Acceptance: Any repair has before/after causal evidence and preserves replay denial, provider faults, original identities, policy checks and side-effect counts.
- Acceptance: Navigation tests cover reader, writer, actual management eligibility, anonymous/unknown/error states, direct routes and stale session completion.
- Acceptance: Export wording changes preserve archive behavior and existing download/progress/error oracles.
- Acceptance: If original full race remains red, HOLD; neither B completion nor diagnosis prose permits acceptance or round 02.

### Round 2: Preserve file-navigation context and make read failures actionable without replaying writes.
- A: Directory/view/recycle routes and exact object return navigation
- `web/src/Shell.tsx`
- `web/src/router.ts`
- `web/src/router.context.test.ts`
- `web/src/Shell.filesNavigation.test.tsx`
- `web/src/pages/FilesPage.tsx`
- `web/src/pages/FilesPage.location.test.tsx`
- `web/src/pages/files/fileLocation.ts`
- `web/src/pages/files/fileLocation.test.ts`
- `web/src/pages/files/FolderBrowser.tsx`
- `web/src/pages/files/literalFolderPath.ts`
- `web/src/pages/files/literalFolderPath.test.ts`
- B: Readable API failures with request-ID support
- `web/src/api/transport.ts`
- `web/src/api/transport.test.ts`
- `web/src/api/errorPresentation.ts`
- `web/src/api/errorPresentation.test.ts`
- `web/src/components/Page.tsx`
- `web/src/components/PageError.test.tsx`
- Acceptance: COMMON on the serial integrated candidate based on accepted round 01.
- Acceptance: Tests cover nested paths, Unicode, literal percent, plus, question/hash characters, whitespace Keys, malformed route payloads and legacy links.
- Acceptance: Enter directory, select mine/recycle, open object, return, Back/Forward and restore retain the intended context without stale rows/actions.
- Acceptance: Existing folder-input normalization and all previous cancellation, download, sharing, recovery and version tests remain unchanged and pass.
- Acceptance: Error tests cover envelope/header request IDs, malformed/non-JSON errors, plain network errors, cancellation and no automatic write retry.

### Round 3: Find files beyond the first loaded page and verify authenticated storage/sharing/recovery loops in a real local browser.
- A: Non-AI, server-paged literal Key-prefix find
- `web/src/Shell.tsx`
- `web/src/router.ts`
- `web/src/router.find.test.ts`
- `web/src/Shell.filesFindNavigation.test.tsx`
- `web/src/pages/FilesPage.tsx`
- `web/src/pages/FilesPage.find.test.tsx`
- `web/src/pages/files/fileLocation.ts`
- `web/src/pages/files/fileLocation.test.ts`
- `web/src/pages/files/FileFindPanel.tsx`
- `web/src/pages/files/FileFindPanel.test.tsx`
- `web/src/pages/files/FileFindResults.tsx`
- `web/src/hooks/useFileFind.ts`
- `web/src/hooks/useFileFind.test.tsx`
- B: Hermetic authenticated browser business E2E
- `scripts/web-product-e2e.mjs`
- `scripts/web-product-e2e-cdp.mjs`
- `scripts/web-product-e2e-flow.mjs`
- `scripts/web-product-e2e-server.mjs`
- `scripts/web-product-e2e.test.mjs`
- `scripts/web-product-e2e-server.test.mjs`
- Acceptance: COMMON on the final integrated candidate; previous Shell/file-location paths are intentionally extended only after round 02 acceptance.
- Acceptance: Find tests prove discovery after the first 200 objects, exact backend prefix/marker propagation, empty continuations, exhaustion, bad cursors and stale-client/query fencing.
- Acceptance: Find is accurately labeled prefix-only and works without AI; opening and returning preserves its scope.
- Acceptance: Run node --test scripts/web-product-e2e.test.mjs scripts/web-product-e2e-server.test.mjs.
- Acceptance: Controller runs node scripts/web-product-e2e.mjs --bin bin/aero-vault --chrome <installed-browser> --report-dir <R>/rounds/03/e2e after the controlled build.
- Acceptance: Browser evidence uses real local responses and bytes, checks permissions and share-use effects, retains failures, and explicitly excludes real OIDC/provider validation.
- Acceptance: After acceptance, queue is exhausted: idle/observe until deadline, with no additional feature rounds.

## Validation and controlled integration
Each writer has exact disjoint paths, independent worktree, actual source snapshot and report; no model Git writes. Controller alone assembles and merges.
Before source acceptance: controlled UI build, make check, python3 cli.py accept, original full make test-race, make web-check → make build → make web-smoke, diff/scope/hash and a fresh adapter-enforced read-only review.
Go uses GOMAXPROCS=4 GOFLAGS=-p=1, tests umask022, original package300s race budget. No test skips, weakened assertions or larger budgets.
Vite-generated embedded UI artifacts are controller build outputs under internal/webui/static/app, never hand-edited by writers; their exact add/delete/hash manifest is captured.
Failed work/source/branches stay preserved. Only source with green current gates and review is committed and fast-forward merged into local main. No force/reset/clean/stash or remote fetch/push.
Watcher may recover verified process crashes with bounded backoff, not deterministic gate/auth/scope failures. It never extends the deadline or refills invocation budget.
External PG/Qdrant/identity/storage/KMS, production, sustained real users and business browser E2E are unverified unless explicitly recorded. Costs unknown. Full B/Space product is not claimed.
