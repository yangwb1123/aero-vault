-- Cumulative transient-retry window anchor (contract implementation-gate.md:21
-- item 1 "瞬态有界重试 cap 300s"): first_attempt_at_ns records the first claim
-- time of the row and is set exactly once, inside the fenced claim UPDATE via
-- CASE WHEN first_attempt_at_ns=0 (idempotent across lease re-claims and
-- ack-lost re-claims — retry/fail/complete never reset it). The relay
-- terminalizes a transient-only failure stream once
-- now - first_attempt_at_ns > AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS (the
-- cumulative window == the per-attempt cap); zero is the safe default — an
-- un-anchored row (pre-upgrade, or read-before-first-claim) is never
-- window-terminal (safe direction for anchor loss / clock skew).
--
-- The 0043 pending predicate (delivered_at_ns=0 AND failed_at_ns=0) is
-- unchanged; the column is deliberately not indexed (read via heap in the
-- claim RETURNING; the 0043 partial indexes still serve the plan).
ALTER TABLE audit_governance_outbox
  ADD COLUMN first_attempt_at_ns INTEGER NOT NULL DEFAULT 0;
