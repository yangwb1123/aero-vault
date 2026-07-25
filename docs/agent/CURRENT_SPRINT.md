# CURRENT_SPRINT — 当前 Sprint 目标

> 更新于: 2026-06-16
> 下个 Sprint 计划见 `docs/ROADMAP.md`

---

## Sprint 目标: 端到端集成验证

本轮 Sprint 聚焦**全服务集成验证与测试覆盖率提升**，确保系统在真实 HTTP 请求流下的正确性。

### 范围

| 领域 | 目标 | 状态 |
|------|------|------|
| 全服务 HTTP 集成测试 | 启动完整服务器（SQLite+local+全协议），验证所有协议互操作 | ✅ 14 测试通过 |
| 协议互操作验证 | REST PUT → S3 GET → MCP list 同对象路径 | ✅ 完成 |
| 测试覆盖率提升 | 整体覆盖率 ≥70% | ✅ 70.2% |
| CI 集成测试门禁 | 全服务测试加入 CI 流程 | ✅ 完成 |

### 集成测试覆盖

新增 `internal/integration/fullserver_test.go` 覆盖：

| 测试 | 验证点 |
|------|--------|
| `Healthz` | 存活检查返回 200 |
| `Readyz` | 就绪检查（含 DB ping）返回 200 |
| `REST_CRUD` | PUT/GET/HEAD/LIST/DELETE + 404 + ETag |
| `Tags` | PUT/GET tags 完整生命周期 |
| `S3Compat` | S3 路径式 PUT/GET/HEAD |
| `SearchDisabled` | AI 未配置时返回 503 |
| `MCP` | JSON-RPC tools/list 返回工具列表 |
| `WebUI` | `/ui` 返回 HTML 内容 |
| `OpenAPI` | `/openapi.json` 返回 200 |
| `CORS` | OPTIONS 预检请求通过 |
| `ProtocolInterop` | REST 写入 → S3 读取 → MCP list 同对象 |
| `RangeRequest` | Range header → 206 Partial Content |
| `AdminEndpoints` | admin API 无 500 错误 |
| `ConcurrentCRUD` | 5 个 goroutine 并发写入和读取 |

### 禁止修改

- `internal/ai/` 中 AI 模型接口（Embedder / LLM / Reranker）
- `internal/storage/storage.go` 的 `Storage` 接口
- `internal/repository/repository.go` 的 `Repository` 接口
- SDK (`sdk/`) 代码
- 数据库迁移文件 (`internal/repository/migrations/`)

### 完成标准

- [x] 全服务集成测试通过
- [x] 协议互操作已验证（REST↔S3↔MCP 同对象）
- [x] 测试覆盖率 ≥70% ✅ 70.2%
- [x] `make check` + `go test ./internal/integration/...` 通过
