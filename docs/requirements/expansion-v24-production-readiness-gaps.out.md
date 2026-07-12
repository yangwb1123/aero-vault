这份文档已经存在于 `expansion-v24-production-readiness-gaps.md`（2026-07-10），内容与你粘贴的一致。没有 `.out.md` 讨论文件。

下面是我的审阅反馈：

---

## 对 v24 的审阅反馈

这是一份扎实的方向分析文档。但我注意到几个值得深挖的点：

### 1. MCP 安全 — 盲区比文档指出的更深

文档正确指出了 MCP handler 跳过 middleware 链的问题。但还有一个结构性问题未提及：

**HTTP MCP 实际上**`无法简单地移入 middleware 链`，因为 `chi.Method` 是在顶层 router 注册的，要进入 middleware 需要在 `r.Group` 内注册。当前 `main.go` 中的注册位置：

```go
r.Method(http.MethodPost, "/mcp", mcp.HTTPHandler(mcpServer))
```

如果只是把这一行移入 `r.Group(func(r chi.Router) { /* ... */ })`，`mcp.HTTPHandler` 中自己调用 `jsonrpc` 的 serve，而 `jsonrpc` 包不会像标准 HTTP handler 那样自动从 context 中提取 tenant/auth。这意味着**即使加了 middleware，MCP handler 内部也需要改造才能利用 middleware 注入的上下文**。

这实际上提高了实现难度：需要改动 `internal/mcp/server.go` 来从 `ctx` 读取 `mw.TenantFrom(ctx)` 和 scope 信息，而当前 `HTTPHandler` 只是把请求转给 `jsonrpc.Handler`。

### 2. SDK DX — 缺少一个关键维度

文档列了 9 项缺失模式，但缺了 **安装体验（Install Experience）**：

| SDK | 安装方式 | 问题 |
|-----|---------|------|
| Go | `go get github.com/aero-vault/aero-vault/sdk/go/aerovault` | 依赖整个 monorepo；用户无法只获取 SDK |
| Python | pypi 包？ | 需要检查是否有发布 |
| JS | npm 包？ | 需要检查 |

如果 Go SDK 依赖整个 monorepo（而不是独立的 `go.mod`），用户 build 时会拉取大量无关依赖。这会影响首次使用体验。建议在 v24 的基础上增加 **SDK 包独立发布** 作为方向。

### 3. 管理控制台 — 低估了工程投入

文档建议"UI 侧不需要新的 API——18+ admin 端点已完整实现"。但实际上：

- **分页/排序/过滤**：admin 端点当前是否支持分页？如果 GET `/v1/admin/keys` 返回所有 key 而不支持 `?offset=&limit=`，UI 无法做列表展示。
- **批量操作**：UI 中"吊销 Key"是一个按钮 + 确认对话框，但后端 DELETE 端点是单项的。批量操作需要后端配合。
- **WebSocket/SSE 状态推送**：作业队列需要实时状态更新（pending→running→completed），当前 admin 端点只支持轮询。

建议在投入 UI 前先审计 admin API 的分页查询能力。

### 4. 结构化错误协议 — 优先级应提至 P1

文档排序 P1 合理，但我认为**结构化错误协议是 #2 SDK DX 的前提条件**——没有统一错误码，SDK 的自动重试、错误类型映射无法可靠实现。建议在 roadmap 中把 #4 列为 #2 的依赖前置。

### 5. API 版本化 — 与 #4 的依赖关系

同样，版本化策略依赖于 #4 的错误协议。版本协商如果返回 `406 Not Acceptable`，错误响应格式需要统一。这两者可以设计为同一个工程阶段。

### 6. 边缘问题中值得升级的

- **#1 MCP write_file 无大小限制 → OOM 风险**：这是安全 + 可靠性的双重问题，建议升级为 P1 级别的子任务
- **#7 MCP 资源 URI 格式未文档化**：如果 MCP 是核心集成协议，URI 格式应该放在 `docs/api.md` 中作为协议规范的一部分

---

**总结**：v24 识别了 5 个真实的有效缺口，且与前 23 期分析不重复。建议在后续实施前补充：

1. 确认 **#4（错误协议）+ #5（版本化）** 作为 #2（SDK DX）的前置依赖
2. 评估 #3（管理控制台）实现前 admin API 的分页查询能力
3. 补充 **SDK 独立发布** 作为 #2 的子方向

是否需要我针对以上任意方向生成更详细的技术方案（包含影响范围、文件清单和实现步骤）？
