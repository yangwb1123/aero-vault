Thank you for providing this context. You've given me the role of a **Staff Engineer** to conduct a code quality review, but I need one critical piece of information:

**What specific subsystem or feature would you like me to review?**

The aero-vault project has the following subsystems available for review:

| # | Subsystem | Package | Files | Description |
|---|-----------|---------|-------|-------------|
| 1 | **FileService** | `internal/service/` | Core CRUD controller |
| 2 | **REST API** | `internal/api/rest/` | JSON protocol adapter |
| 3 | **S3 Compat API** | `internal/api/s3compat/` | S3 protocol adapter |
| 4 | **WebDAV** | `internal/api/webdav/` | WebDAV protocol adapter |
| 5 | **MCP** | `internal/mcp/` | MCP protocol adapter |
| 6 | **AI/RAG Pipeline** | `internal/ai/` | Indexer, Search, Chat, Agent |
| 7 | **Storage Layer** | `internal/storage/` | Local/S3/OSS/COS backends |
| 8 | **Repository** | `internal/repository/` | SQLite/Postgres data access |
| 9 | **Auth/Middleware** | `internal/auth/`, `internal/middleware/` | JWT, Key, Rate limiting |
| 10 | **Telemetry** | `internal/telemetry/` | OTel, Prometheus metrics |
| 11 | **Configuration** | `internal/config/` | Config loading/validation |
| 12 | **Event System** | `internal/events/` | EventBus, Webhooks |
| 13 | **Other** | `internal/antivirus/`, `internal/jobs/`, `internal/reconcile/`, `internal/replication/`, `internal/shutdown/`, `internal/snapshot/`, `internal/thumbnail/`, `internal/cluster/`, `internal/cli/` | Supporting modules |
| 14 | **Main entrypoint** | `cmd/server/main.go` | Application assembly |

**Please tell me:**
- Which subsystem(s) you'd like me to review
- Whether you have specific design docs (`.arch.md`) or implementation plans (`.impl-plan.md`) you want me to reference
- Any specific concerns you already have about that code

Once you point me at a target, I'll do a thorough Staff Engineer-level review following the full checklist format you provided (Code Organization, Naming, Error Handling, Logging, Testing, Technical Debt, Quality Metrics).
