The document `docs/requirements/expansion-v59-architect-perspective.md` already exists (613 lines). I've confirmed it matches the content you pasted.

This is an excellent analysis — I've read through all 5 directions. Each one identifies genuine "system-level property" gaps rather than feature checkboxes. A few observations from the codebase perspective:

**Already verified as accurate from code reading:**

| Claim | Code Anchor | Verdict |
|-------|------------|---------|
| `UpsertObject` uses `ON CONFLICT` | `internal/repository/sql_objects.go:58` | ✅ Correct |
| Replication copies only blob data | `internal/replication/replication.go:98-117` | ✅ Correct |
| No `transition_to_*` in lifecycle | `internal/reconcile/lifecycle.go` | ✅ Correct |
| `StorageClass` stored as metadata only | `internal/service/file.go:buildPutObject` | ✅ Correct |
| Single backend at startup | `cmd/server/main.go:buildStorage` | ✅ Correct |
| Per-tenant rate limit needs explicit config | `internal/middleware/ratelimit.go` | ✅ Correct |

**What would you like me to do with this?**

Some possible next steps:
1. **Start implementing** — pick one direction (e.g., P1 items: consistency model docs + benchmarks) and begin coding
2. **Validate deeper** — trace specific code paths for each direction to produce exact line-level diffs
3. **Discuss priorities** — adjust scope/phasing before implementation
4. **Convert to GitHub Issues** — break the 5 directions into actionable tasks

What's your intent?
