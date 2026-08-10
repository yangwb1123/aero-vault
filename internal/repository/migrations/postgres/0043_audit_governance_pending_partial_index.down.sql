-- Reversible: no column changes — dropping both partial indexes restores the
-- pre-0043 schema. IF EXISTS matches the repo convention (0036) and keeps a
-- manual partial re-run replay-safe (first DROP ok, second re-run errors).
DROP INDEX IF EXISTS audit_governance_pending_lag_idx;
DROP INDEX IF EXISTS audit_governance_pending_claim_idx;
