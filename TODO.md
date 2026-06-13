# TODO

Current backlog, prioritized. Update this file after each completed item.

---

## Completed (move here when done)

- [x] Indexer skip metric (`indexer_skip_total{reason}`)
- [x] AI-specific rate limiting (`AI_RATE_LIMIT_RPS` / `AI_RATE_LIMIT_BURST`)
- [x] MCP write_file / delete_file / chat tools
- [x] `AI_AGENT_MAX_STEPS` configurable
- [x] `AI_CHUNK_WINDOW` / `AI_CHUNK_OVERLAP` configurable
- [x] ChatStream structured `event: error` frames
- [x] PII credit card Luhn validation
- [x] Qdrant integration test + `make test-integration-qdrant`
- [x] Grafana dashboard 12 panels
- [x] Prometheus ai-latency alert group
- [x] All 5 ROADMAP directions (see ROADMAP.md)
- [x] RRF hybrid sort secondary tiebreaker (score DESC, chunkID ASC)
- [x] BM25 hard-delete synchronous chunk cleanup via FileService.WithChunkCleaner
- [x] Web UI: file upload + chat panel (drag-and-drop, SSE streaming, tenant selector)
- [x] SDK sync: Python + JS admin methods (add_key, list_keys, revoke_key, issue_jwt, list_webhook_failures, list_jobs, retry_job, create_tenant, list_tenants, delete_tenant, set_tenant_status, list_audit, set_quota, set_budget)
- [x] BM25 async warm-up signal
- [x] `RemoteExtractor` configurable timeout (`AI_EXTRACTOR_TIMEOUT_SECONDS`)
- [x] Grafana tenant template variable uses storage_bytes source
- [x] Go SDK admin methods (AddKey, ListKeys, RevokeKey, IssueJWT, ListWebhookFailures, ListJobs, RetryJob, CreateTenant, ListTenants, DeleteTenant, SetTenantStatus, ListAudit, SetQuota, SetBudget)
