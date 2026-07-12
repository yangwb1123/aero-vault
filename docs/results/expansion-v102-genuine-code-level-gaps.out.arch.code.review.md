I'm ready to perform a Staff Engineer code quality review! But I need to know **which subsystem** you'd like me to review. Based on the project structure defined in `AGENTS.md`, here are the major subsystems available:

| Subsystem | Package | Description |
|-----------|---------|-------------|
| **FileService** | `internal/service/` | Core controller, CRUD, quotas, locking |
| **REST API** | `internal/api/rest/` | JSON protocol adapter |
| **S3 Compat** | `internal/api/s3compat/` | S3-compatible protocol adapter |
| **WebDAV** | `internal/api/webdav/` | WebDAV protocol adapter |
| **MCP** | `internal/mcp/` | Model Context Protocol |
| **AI/RAG Pipeline** | `internal/ai/` | Indexing, search, chat, agent |
| **Storage** | `internal/storage/` | Storage backends (local, s3, oss, cos) |
| **Repository** | `internal/repository/` | Database layer (SQLite, Postgres) |
| **EventBus/Workers** | `internal/events/`, `internal/worker/` | Event system & background jobs |
| **Auth/Middleware** | `internal/api/middleware/` | Auth, tenant, rate limiting |
| **Config** | `internal/config/` | Configuration management |

---

**Please tell me which subsystem(s) you'd like reviewed**, and I'll dive into the code and produce a comprehensive report covering:

1. ✅ Code Organization
2. ✅ Naming & Documentation
3. ✅ Error Handling
4. ✅ Logging
5. ✅ Testing Practices
6. ✅ Technical Debt
7. ✅ Code Quality Metrics
8. ✅ Technical Debt Register
9. ✅ Final Summary with actionable recommendations

*(Or if you'd like me to review the entire project holistically, I can do that too — just let me know the scope and I'll get started!)*
