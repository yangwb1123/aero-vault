Now I have a thorough understanding of the codebase state and available documents. Let me provide my recommendation.

---

## Recommendation: Which documents to implement

I recommend **`expansion-v103-security-edge-foundations`** as the primary document to implement. Here's my analysis:

### Why this one?

This document targets the most critical security and data-integrity issues identified across the entire codebase. The architecture analysis (`.arch.md`) identified **P0-level vulnerabilities** that are actively exploitable, and the implementation plan (`.impl-plan.md`) provides a detailed, phased task breakdown.

### Confirmed vulnerabilities in current codebase

I verified the codebase state and can confirm these issues are **still present**:

| Issue | Current State | Severity |
|-------|--------------|----------|
| **D1: `_aero_` injection** | `extractMetadataHeaders` (handler.go:892) and `extractMetaHeaders` (s3compat/handler.go:700) accept ALL user-supplied metadata without filtering `_aero_` prefix; `validateMetadata` (file.go:124) skips `_aero_` keys but **does not reject** them | **Critical** — user can set `X-Meta-_aero_content_encoding: gzip` to cause client-side decompression of raw data |
| **D2: gzip+Range offset** | `range.go:77-90` calls `Get` (which returns decompressed stream) then `io.CopyN(Discard, offset)` on already-decompressed stream | **High** — Range requests return wrong byte ranges for gzip objects |
| **D3: key safety** | `path.Join` strips trailing slashes → `foo/` and `foo` map to same storageKey | **Medium** — directory simulation collision |
| **D4: multipart orphans** | No `expires_at` in `multipart_uploads` table; no sweep logic | **Medium** — storage leak |
| **handler.go: 958 lines** | Exceeds 500-line limit by 92% | **Enforcement** — blocks CI per AGENTS.md |

### Recommended implementation order

**Phase 0 (Day 1-2) — P0 Security Fixes:**
| Task | File(s) | Effort |
|------|---------|--------|
| T-001: Filter `_aero_` in REST `extractMetadataHeaders` | `handler.go` | 1h |
| T-002: Filter `_aero_` in S3 `extractMetaHeaders` | `s3compat/handler.go` | 1h |
| T-003: Reject `_aero_` keys in `validateMetadata` | `file.go` | 0.5h |
| T-004: Unit tests for above | `*_test.go` | 2h |
| T-005: Split `handler.go` + `s3compat/handler.go` ≤500 lines | Both handler files | 8h |
| T-006: Fix `GetRange` gzip offset | `range.go`, `file_crud.go` | 6h |
| T-007: Fix `Content-Encoding` response behavior | Both handler files | 4h |
| T-008: gzip+Range integration tests | `range_test.go` | 4h |

**Phase 1 (Day 3-4) — P1 Hardening:**
| Task | Effort |
|------|--------|
| T-009→T-012: Object key validation (validateKey, storageKey trailing slash) | 9h |
| T-013→T-018: Multipart orphan cleanup (migration 0025, reconcile sweep, LocalStorage GC) | 24h |

**Phase 2 (Day 5-7) — P2 Enhancement:**
| Task | Effort |
|------|--------|
| T-019→T-023: Web UI admin panel (audit log filtering, Admin tab) | 23h |

### Secondary recommendation

After completing the above, **`expansion-v118-security-benchmark-config-gaps`** is the natural follow-up — it covers:
- S3 PublicAccessBlock security governance (the "veto-before-authorize" layer)
- Configuration validation (`config.Validate()`)
- Performance benchmarks (CI performance regression gates)
- ACL opt-out (BucketOwnerEnforced)

### What I need from you

Shall I proceed with implementing **`expansion-v103-security-edge-foundations`** starting with **Phase 0 (D1: `_aero_` injection defense + D2: gzip+Range fix)**? I'll begin by reading the existing security review at:

`docs/results/expansion-v103-security-edge-foundations.out.arch.code.security-review.md`
`docs/results/expansion-v103-security-edge-foundations.out.impl-plan.code.security-review.md`

...to incorporate prior findings, then produce production-ready code following all AGENTS.md constraints.
