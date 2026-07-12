所有五项声明的代码证据均已通过当前代码库验证。以下为逐项确认及架构层面的观察。

---

## 验证总结

| # | 方向 | 声明 | 验证状态 | 关键证据 |
|---|------|------|---------|---------|
| 1 | MCP/WebDAV 认证盲区 | MCP 和 WebDAV 绕过 auth 中间件链 | **已确认** | `internal/mcp/transport.go:38` — `HTTPHandler` 为裸包装，无 `authReg` 调用；`internal/api/webdav/dav.go:37` — `Handler` 无认证；`cmd/server/main.go:223` — MCP 和 WebDAV 注册在 `applyMiddleware()` *之后*或在分发器中；`grep -rn "auth\." internal/mcp/` → 零匹配 |
| 2 | BM25 持久性缺口 | 纯内存索引，无 Save/Load 方法 | **已确认** | `internal/ai/bm25.go:28-42` — 结构体含 `sync.RWMutex` 但无持久化字段；`grep -rn "Save\|Load\|Persist" internal/ai/bm25.go` → 零匹配；`BuildFromRepo`（第 86 行）在启动时全量扫描所有存储桶/对象 |
| 3 | 内容预览管线断层 | Web UI 仅显示 JSON，无基于内容类型的渲染 | **已确认** | `grep -c "image\|audio\|video\|pdf\|preview" internal/webui/static/index.html` → 零匹配；Agent 在 4KB 处截断 (`io.LimitReader(rc, 4<<10)`)；缩略图能力存在但未被消费 |
| 4 | Webhook 死信语义污染 | 10 次尝试后将永久失效的消息标记为 `succeeded=true` | **已确认** | `internal/events/webhook.go:211-225` — 注释明确承认："this intentionally conflates 'permanently dead' with 'succeeded'"；`WebhookFailure` 结构体仅有 `Succeeded bool`，无状态枚举 |
| 5 | 租户自助 API 缺失 | 无 `/me/` 路由，无 `self-service` scope | **已确认** | `internal/api/rest/router.go:80-95` — 所有管理功能挂载于 `/admin/` 下，scope 校验为 `admin`；仅有一个 `GET /usage` 自助路由；`grep -rn "self-service\|/me/"` → 零匹配 |

---

## 架构层面的观察

### 1. 方向一（认证盲区）是真正的安全漏洞

分析文档将此标记为 P0，基于代码证据判断，此评级正确。`cmd/server/main.go` 的路由注册顺序揭示了一个结构性缺陷：

```go
// 第 223 行 — MCP 注册在主路由上，而非包装后的中间件链：
r.Method(http.MethodPost, "/mcp", mcp.HTTPHandler(mcpServer))

// 第 227 行 — WebDAV 完全绕过 chi：
return buildDispatcher(r, davH, cfg)
// dispatch 在 chi 路由器 *之外* 分发请求
```

对比：REST（通过 `authReg.Middleware()` 挂载）、S3（挂载于 `authReg.Middleware()` 包装后的路由组）、健康检查/metrics（有显式的绕过列表）。MCP 和 WebDAV 既不属于绕过列表，也不受保护。

**更深远的影响**：即便中间件链修复，`internal/mcp/server.go:55` 的 `tenantFor` 方法仍硬编码了 `"default"` 租户回退。stdio 模式（`internal/mcp/transport.go:17-42`，`ServeStdio`）完全没有 HTTP 上下文，因此修复 HTTP 端点的同时仍需解决租户隔离问题。

### 2. 方向二（BM25 持久性）体现了一种模式——存储向量索引的类似情况如何？

BM25 在进程启动时全量重建。而 `internal/ai/search.go` 中的向量索引支持 pgvector/Qdrant 后端，这些后端是持久化的。这种不对称意味着混合搜索（`mode=hybrid`）冷启动时，向量一侧立即可用，但 BM25 一侧数分钟内无法使用。最可能的用户可见行为：重启后混合搜索退化为纯向量搜索（BM25 匹配项计数为零，RRF 融合偏向纯向量排序），且无任何日志表明降级发生了。

文档建议使用 `Save/Load` 方法。一种更轻量的替代方案：利用 `BuildFromRepo` 已通过分块内容（来自 `ListChunksForObject`）进行种子填充这一事实——如果我们将 BM25 状态作为 blob 写入存储（实为 `{tenant}/__bm25_state`），即可复用现有的存储后端，无需新增迁移。

### 3. 方向四（死信语义）的 schema 设计有更广泛的影响

分析文档指出了 `Succeeded bool` 问题。但当前 schema 还缺少 `updated_at` 时间戳——这意味着一旦某失败记录卡在 `succeeded=false` 状态，运营人员便无法判断它是近期失败的还是数周前失败的。添加状态枚举的同时应同步添加时间戳列。

此外，`webhook.go:211-225` 中的注释[原文如此]写道 *"this intentionally conflates 'permanently dead' with 'succeeded'"*——这种有意而为的歧义构成了**数据契约违约**：任何依赖 `succeeded=true` 的监控都会漏掉死信投递。Prometheus 计数器 `webhook_delivery_total{status="failed"}` 在以下条件下停止递增

### 4. 方向三（预览断层）与方向五（租户自助 API）相互关联

Web UI（`internal/webui/static/`）是一个单一的 vanilla JS SPA，而自助管理（API keys、用量图表、通知配置）会自然而然地需要一个设置面板。如果方向五先实施，方向三的 Web UI 改造将更容易合理化——同一个 SPA 面板既可用于文件预览，也可用于设置管理。

### 5. 迁移成本评估

| 方向 | 文件修改数量 | 迁移需求 | 回滚复杂度 |
|------|------------|---------|-----------|
| 1 (Auth) | 5-7 个文件（`main.go`, `server.go`, `transport.go`, `dav.go`, `dispatch.go`） | 无 | 低 — 纯包装器变更 |
| 2 (BM25) | 3-4 个文件（`bm25.go`, `main.go`，以及可选的 storage/repo） | 新建表或存储 blob | 中等 — 需要版本迁移策略 |
| 3 (UI) | 10+ 个文件（`index.html`、可能的新 JS 文件、`thumbnail/handler.go`） | 无 | 低 — 纯前端变更 |
| 4 (Webhook) | 5-6 个文件（迁移文件、`webhook.go`、`webhook_failures.go`、`admin.go`、`repo interface`） | 1 个新迁移 | 中等 — 需要回滚迁移 |
| 5 (自助 API) | 8-12 个文件（`router.go`、`admin.go`、`auth.go`、新 handler、Web UI 面板） | 无 | 低 — 纯新增路由 |

### 6. 分析文档中未提及的一个隐含项

**方向二和方向四之间存在隐藏耦合**：`BuildFromRepo` 遍历 `repo.ListObjects` + `repo.ListChunksForObject`，这在分页循环中使用了数据库查询。如果任何 webhook 在 BM25 重建期间触发对象变更事件，`AddDocument` 可能与 `BuildFromRepo` 发生竞态。当前的 `sync.RWMutex` 可以防止数据损坏，但可能丢失并发插入的数据——在添加持久化之前应通过测试来确认这一边界情况。

---

总而言之，所有五项方向均经过充分锚定和独立验证。分析文档中未发现实质性事实错误。安全漏洞（方向一）应优先处理，而 BM25 持久性（方向二）和死信语义（方向四）修复成本较低，可在同一开发周期内完成。内容预览和自助 API 影响面更大，应作为独立的里程碑进行规划。
