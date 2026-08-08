package ai

// clampSearchLimit bounds a backend candidate request to the contract shared
// by every retrieval backend (repository scan, Qdrant, pgvector):
//   - limit <= 0  -> default 10 (unchanged legacy semantics);
//   - limit > 100 -> capped at 100, the maximum a Search request can produce
//     (K is validated <= 100 and K*2 is capped in searchVector).
//
// The >100 case is a cap (min(limit,100)), not a fallback to the default, so
// K in (50,100] never collapses to 10 candidates.
func clampSearchLimit(limit int) int {
	if limit <= 0 {
		return 10
	}
	if limit > 100 {
		return 100
	}
	return limit
}
