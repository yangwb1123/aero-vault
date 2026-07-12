Thank you for the context. I can see this is a substantial Go project (237 source files) with a well-defined architecture per the `AGENTS.md` you've shared. 

However, I notice you've posted the **Staff Engineer review framework** without specifying **which subsystem or code** you'd like me to review. The project has several major components:

| Layer | Package | Files |
|-------|---------|-------|
| API/REST | `internal/api/rest/` | ~25 files |
| S3 Compat | `internal/api/s3compat/` | ~10 files |
| WebDAV | `internal/api/webdav/` | ~3 files |
| Service | `internal/service/` | ~9 files |
| AI/RAG | `internal/ai/` | ~35 files |
| Auth | `internal/auth/` | ~15 files |
| Storage | `internal/storage/` | ~28 files |
| Repository | `internal/repository/` | ~30 files |
| Configuration | `internal/config/` | ~8 files |
| CLI | `internal/cli/` | ~8 files |
| MCP | `internal/mcp/` | ~6 files |
| Middleware | `internal/middleware/` | ~6 files |

**Could you please tell me which component(s) you'd like me to review?** 

Options I'd suggest:
1. **AI/RAG pipeline** (`internal/ai/`) — a complex subsystem with many moving parts
2. **FileService** (`internal/service/`) — the core controller
3. **Storage backends** (`internal/storage/`) — the persistence layer
4. **Auth system** (`internal/auth/`) — security-critical code
5. **Repository** (`internal/repository/`) — data access layer
6. **All of the above** (full codebase scan — will produce a comprehensive report)

Pick one (or a few) and I'll do a thorough Staff Engineer-level review with concrete findings, code examples, and actionable recommendations.
