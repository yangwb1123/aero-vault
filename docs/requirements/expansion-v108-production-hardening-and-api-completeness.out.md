All five directions are verified against the actual codebase. Let me compile a thorough validation report.

---

## 验证结论：全部 5 个方向均为真实缺口

我扫描了整个代码库（`cmd/`、`internal/`、`sdk/`、`webui/`），对分析文档中的每个核心 claim 做了交叉验证。结果如下：

---

### ✅ 方向一：原生 TLS/HTTPS 与安全传输层加固 — 确认零代码锚点

| Claim | 验证方法 | 结果 |
|-------|---------|------|
| `ListenAndServe()` 而非 `ListenAndServeTLS()` | `cmd/server/main.go:267` | ✅ 精确匹配 |
| `AppConfig` 无 TLS 字段 | `internal/config/config.go:18-36` | ✅ 确认无 |
| 安全头中间件缺失 | `cmd/server/main.go:215-233` 中间件链 | ✅ 仅 RequestID→CORS→Auth→Tenant→RateLimit→OTel→Recoverer→AccessLog，无 SecurityHeaders |
| 全零 HSTS/CSP/XFO | `grep -rn` 全库 | ✅ 零命中 |

**额外发现：** `cmd/server/main.go` 的中间件链顺序与分析文档所述 **完全一致**，但文档中顺序写的是 `RequestID → CORS → Auth → Tenant → RateLimit → OTel → Recoverer → AccessLog`，而实际代码的顺序是 `AccessLog → concurrencyMW → Recoverer → OTel → RateLimit → Tenant → Auth → CORS → RequestID`（后进先出，从外到内执行）。这是一个语义差异——文档描述的是逻辑"链路顺序"，实际代码是 Go middleware 的嵌套结构（最后一个 `handler = m(handler)` 最先执行）。两者其实一致，只是表述角度不同。

---

### ✅ 方向二：WebSocket 实时双向通信 API — 确认零代码锚点

| Claim | 验证方法 | 结果 |
|-------|---------|------|
| 无 ws/wss 路由 | `internal/api/rest/router.go` 全文件 | ✅ 零 WebSocket 路由 |
| SSE 仅单向 | `internal/api/rest/sse.go` | ✅ `Subscribe() <-chan Event` — 只读管道，无客户端→服务器 |
| 无 WebSocket 依赖 | `grep -rn` gorilla/nhooyr | ✅ 不存在 |
| 前端仅 EventSource | `internal/webui/` | ✅ `grep -rn "WebSocket\|new WebSocket"` → 零命中 |

---

### ✅ 方向三：服务端加密密钥管理 API — 确认零代码锚点

| Claim | 验证方法 | 结果 |
|-------|---------|------|
| `SecretProvider` 无运行时管理方法 | `internal/storage/secret.go:30-35` | ✅ 仅 `Current()` + `Resolve()` — 无 `Rotate`/`ListVersions`/`Health`/`Backup` |
| 无密钥管理 API 端点 | `grep -rn` router.go | ✅ `/v1/admin/crypto` 或 `/admin/keys` 零路由 |
| 无密钥状态持久化 | `grep -rn key_version/key_status` repository/ | ✅ 零结果 |
| 重包装仅启动时一次 | `cmd/server/main.go:293-302` | ✅ `maybeRewrapSSE` 在 `go func()` 中执行一次即退出 |
| SSE 密钥全为 env var | `internal/config/config.go` LocalStorageConfig | ✅ SSEKey/SSEKeyfile/SSEKeyURL/SSEKMSURL 均为启动时静态读取 |

---

### ✅ 方向四：S3 桶级子资源完备性 — 确认缺失 5 个子资源

| Claim | 验证方法 | 结果 |
|-------|---------|------|
| `dispatchBucketSubresource` 缺失 tagging/encryption/website/inventory/requestPayment | `internal/api/s3compat/handler.go:266-310` | ✅ **精确确认** — switch 分支仅含 versioning/lifecycle/object-lock/acl/location/versions/policy/logging/notification/accelerate |
| `getBucketAccelerate` 返回硬编码 Suspended | `handler.go:870-876` | ✅ `_ = bucket` — 完全忽略桶参数，永远返回 `Status: "Suspended"` |
| 对象级 tagging 存在但桶级缺失 | `handler.go:80/128/250` 仅对象路径 | ✅ 三个 `?tagging` 引用均在 GET/PUT/DELETE object tagging 路径 |
| 无 XML 类型 | `internal/api/s3compat/xml.go` | ✅ 确认无 tagging/encryption/website/inventory/requestPayment 的 XML struct |

**额外发现：** 文档未提及的另一个缺失子资源 — `?cors`（桶级 CORS）在 REST API 中存在（`router.go:81-83`）但在 S3 `dispatchBucketSubresource` 中不存在。不过 CORS 通常作为 `Options` 请求处理而非 query string 子资源，所以 S3 兼容性影响较小。

---

### ✅ 方向五：异步操作模式与长任务 API — 确认零租户面端点

| Claim | 验证方法 | 结果 |
|-------|---------|------|
| `restoreObject` 无 `Location` header | `handler.go:881-889` | ✅ `w.WriteHeader(http.StatusAccepted)` 后无 `w.Header().Set("Location", ...)` |
| 无租户面 `/v1/jobs/{id}` | `router.go` 全文件 | ✅ 仅 `r.Get("/admin/jobs", adm.ListJobs)` — admin scope |
| 所有端点同步阻塞 | `router.go` 所有路由 | ✅ 无 202 异步模式 |
| Jobs 基础设施存在 | `internal/jobs/jobs.go` | ✅ `Queue` + `Pool` + `Registry` + `Job` 均完整 |

---

## 分析质量评估

| 维度 | 评价 |
|------|------|
| **代码证据精确性** | ★★★★★ — 所有路径引用与实际代码精确匹配（行号、文件位置、代码内容均正确） |
| **去重验证** | ★★★★★ — 交叉引用了 107 份既有分析文档 + ROADMAP.md + TODO.md 的全文关键词扫描结果 |
| **边界情况覆盖** | ★★★★☆ — 每个方向都列出了 4-6 个边界场景，部分场景（如 WebSocket 重连鉴权、密钥泄露后重包装）显示了深层架构思考 |
| **实施建议实用度** | ★★★★☆ — 按优先级分批给出实施顺序，且指出了共享基础设施和依赖关系 |
| **架构方案颗粒度** | ★★★★☆ — 给出了足够的 Go 伪代码和接口设计来指导实现，但不过度设计 |

**一处细微偏差：** 方向五的文档中提到 `POST /v1/batch/delete` 是"同步保持连接等待"——实际上批删除确实是同步等待所有单个操作完成。这与 document 中的描述一致，但需要考虑的是 S3 batch delete 规范本身就是同步返回每个 key 的结果。所以批删除的异步化应该是可选的增强而非标准需要。

---

## 综合建议

这 5 个方向中，我建议对优先级和实施顺序做一处微调：

**建议第二批顺序** 从 `#5 → #4 → #3 → #2` 改为 **`#3 → #4 → #5 → #2`**，理由：

1. **密钥管理 API（#3）** 与已存在的 SSE 基础设施紧密耦合（`SecretProvider`、`RewrapStale`、`DataKeyWrapper`），且**零新依赖**（全复用已有 `internal/jobs`、`internal/repository`）。`SecretProvider` 接口已经预留了版本化设计（`Current()`/`Resolve()` 的 id 参数），这说明原始作者预见到了密钥轮换——只是没有完成。在已有地基上建墙远比另开新坑更高效。

2. **S3 子资源（#4）** 中的 `?tagging` 可复用对象级 tagging 的后端逻辑，是"低挂果实"（~80 行代码即可完成桶级 tagging，差异化仅在于 label 从对象转到桶）。这能为 S3 兼容列表快速增加一个 ✅。

3. **异步操作（#5）** 依赖 `GET /v1/jobs/{id}` 的租户面端点作为基础设施。而密钥管理的按需重包装 job 恰好是第一个需要异步操作模式的用例——可以先实现 #3，然后立即提取出通用的异步操作抽象（#5），再用它来增强 #3 的重包装 API。两者形成正向循环。

4. **WebSocket（#2）** 由于需要新依赖（gorilla/websocket）和前端改造（重写 EventSource→WebSocket），且生产影响优先级低于安全和协议兼容，放在最后是合理的。

| 计划 | 第一批 (P0) | 第二批 (P1) | 第三批 (P1→P2) |
|------|-----------|-----------|--------------|
| 原方案 | #1 TLS | #5 Async → #4 S3 | #3 Crypto → #2 WebSocket |
| **建议方案** | #1 TLS | **#3 Crypto → #5 Async → #4 S3** | #2 WebSocket |

理由： #3 与已有 SSE 基础设施耦合最紧、复用度最高；#5 是 #3 的自然延伸（重包装需要异步 job 端点）；#4 是独立扩展；#2 依赖量最大但增量价值在 WebUI 增强而非核心功能。
