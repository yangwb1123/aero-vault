文档已保存至 **`docs/requirements/expansion-v124-auth-blindspots-ai-persistence-preview-governance.md`**（746 行，25KB）。

这是在现有 123 份分析文档之后的第 124 份。文件命名遵循已有惯例 `expansion-v{number}-{slug}.md`。

### 已保存文件概要

| 方向 | 类型 | 优先级 | 核心代码锚点 |
|------|------|--------|-------------|
| **1 — MCP/WebDAV 认证盲区** | 安全/P0 | 最高 | `internal/mcp/server.go:35` 硬编码 tenant；`internal/mcp/transport.go:38` 裸 HTTP handler；`internal/api/webdav/dav.go:37` 无 middleware |
| **2 — BM25 持久性缺口** | AI 运营/P1 | 高 | `internal/ai/bm25.go` 无 Save/Load 方法；`cmd/server/main.go:145` 启动全量 BuildFromRepo |
| **3 — 内容预览断层** | 产品/P2 | 中 | `internal/webui/static/` 仅 JSON.stringify；`internal/mcp/server.go:92` 4MB 截断纯文本；缩略图能力未被消费 |
| **4 — Webhook 死信语义** | 运营/P2 | 中 | `internal/events/webhook.go:161` 注释承认 succeeded=true 语义污染 |
| **5 — 租户自助 API** | 平台/P3 | 中低 | `internal/api/rest/router.go:80` 全部挂 admin scope；无 `/me/*` 路由或无 `self-service` scope |

建议按方向一（安全修复）→ 方向四（数据质量）→ 方向二（AI 生产就绪）→ 方向五（平台化）→ 方向三（产品体验）的顺序实施。
