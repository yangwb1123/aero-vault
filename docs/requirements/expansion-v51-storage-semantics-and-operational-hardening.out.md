The document at `expansion-v51-storage-semantics-and-operational-hardening.md` already exists on disk and appears to match what you've shared. I've read through all five directions.

This is a sharp analysis — it shifts the frame from "what features are missing" to "is what's already built actually correct under edge conditions." The five directions systematically target:

1. **Crash safety windows** in local storage (data/meta write ordering, multipart merge atomicity)
2. **Delete Marker semantics** lacking for versioned bucket S3 compliance
3. **CompleteMultipartUpload race conditions** and missing part-sequence validation
4. **On-read integrity verification** (MD5 check on every GET, not just scrub)
5. **Cross-protocol execution context inconsistency** (tenant resolution, conditional headers, idempotency)

The prioritization also rings true — **Phase 1** (directions 1 & 3) directly fortify the default CI-gate path (local FS + SQLite) with no new dependencies, making them the highest leverage.

What would you like me to do next?

- **Validate the claims** against the actual codebase (read the mentioned source files to confirm the crash windows, race conditions, etc.)
- **Save the analysis** (already on disk)
- **Generate implementation tickets/ADRs** for the Phase 1 directions
- **Begin implementing** one of the fixes (e.g., fsync guarantees in `local_write.go`, CompleteMultipartUpload concurrency protection)
- **Cross-reference** with the existing `HARNESS.md` / `AGENTS.md` constraints to plan safe changes

Let me know which path to take.
