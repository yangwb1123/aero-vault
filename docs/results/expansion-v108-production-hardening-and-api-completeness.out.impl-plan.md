Now I have a complete picture. Here is my Tech Lead analysis:

---

# Tech Lead 分析报告：五个方向的功能缺口评估

## 1. 任务分解

以下将 5 个方向拆解为可执行的工程任务，每个任务预估 2-6 小时，符合《AGENTS.md》的工程约束。

### 方向一：原生 TLS/HTTPS

| 任务 ID | 标题 | 涉及文件 | 前置 | 工时 | 验收标准 |
|---------|------|---------|------|------|---------|
| TASK-001 | 配置层：新增 TLS 配置结构体 | `internal/config/config.go`, `internal/config/config_tls.go`（新建） | 无 | 2h | `Config.TLS` 含 `Enabled`, `CertFile`, `KeyFile`, `MinVersion`, `CipherSuites`，环境变量前缀 `TLS_*`；`Validate()` 检查 cert+key 配对 |
| TASK-002 | 安全头中间件 | `internal/middleware/security.go`（新建） | 无 | 2h | 中间件设置 `Strict-Transport-Security`, `Content-Security-Policy`, `X-Content-Type-Options`, `X-Frame-Options`, `X-XSS-Protection`，可从 config 控制开关 |
| TASK-003 | 服务端启动：条件开启 `ListenAndServeTLS` | `cmd/server/main.go` | TASK-001 | 2h | `cfg.TLS.Enabled` 时调用 `srv.ListenAndServeTLS()`，否则保持现有 `ListenAndServe()` |
| TASK-004 | 安全头中间件接入中间件链 | `cmd/server/main.go` | TASK-002 | 1h | 在 CORS 中间件之后、Auth 之前插入 `SecurityHeaders` |
| TASK-005 | 集成测试：TLS 握手与安全头验证 | `cmd/server/main_test.go`（新建或扩展） | TASK-001~004 | 2h | `go test` 中使用 `httptest.NewTLSServer`，验证安全头存在、TLS 版本不低于 TLS 1.2 |

**小计：9h**

### 方向三：服务端加密密钥管理 API（按建议方案提前至第二批首位）

| 任务 ID | 标题 | 涉及文件 | 前置 | 工时 | 验收标准 |
|---------|------|---------|------|------|---------|
| TASK-006 | `SecretProvider` 扩展：新增运行时管理接口 | `internal/storage/secret.go` | 无 | 2h | 新增 `Rotate(id string, key []byte) error`、`ListVersions() []string`、`Revoke(id string) error`，原有 `Current()`/`Resolve()` 行为不变 |
| TASK-007 | 版本化密钥持久化层 | `internal/repository/repository.go`, `internal/repository/sql.go`, `migrations/{sqlite,postgres}/NNNN_crypto_keys.{up,down}.sql` | TASK-006 | 3h | 新增 `sse_keys` 表（id, tenant_id, key_material_hash, created_at, revoked_at），Repository 接口 `PutSSEKey`/`GetSSEKey`/`ListSSEKeys`/`RevokeSSEKey`，遵循 I1（占位符独立编号）和 I2（双文件迁移） |
| TASK-008 | 密钥管理 REST API 端点 | `internal/api/rest/admin.go`, `internal/api/rest/router.go` | TASK-007 | 3h | `POST /v1/admin/crypto/keys`（Create）, `GET /v1/admin/crypto/keys`（List）, `DELETE /v1/admin/crypto/keys/{id}`（Revoke）, `POST /v1/admin/crypto/rewrap`（按需重包装）；admin scope 鉴权；审计日志记录 |
| TASK-009 | 按需重包装 Job 类型 | `internal/storage/rewrap.go`, `internal/storage/rewrap_job.go`（新建） | TASK-008 | 2h | 注册 `JobRewrapKeys` job handler，调用 `RewrapStale` 逻辑；通过 `Queue.Enqueue` 下发 |
| TASK-010 | keyRingProvider 运行时热加载 | `internal/storage/secret.go` | TASK-006 | 3h | `keyRingProvider` 支持 `AddKey`/`RevokeKey` 方法更新内存 map；新增 `LoadFromRepo` 方法从持久化层加载 |
| TASK-011 | 密钥健康检查和自动轮换提醒 | `internal/storage/secret.go`, `cmd/server/main.go` | TASK-007 | 2h | 启动时 log key 版本数；`/healthz` 暴露 `"sse_key_versions": N`；`SSE_REWRAP_INTERVAL` env var 支持定时自动重包装 |

**小计：15h**

### 方向五：异步操作模式与长任务 API（紧随 #3）

| 任务 ID | 标题 | 涉及文件 | 前置 | 工时 | 验收标准 |
|---------|------|---------|------|------|---------|
| TASK-012 | 租户面 Job 状态查询端点 | `internal/api/rest/admin_jobs.go`, `internal/api/rest/router.go` | 无 | 2h | `GET /v1/jobs/{id}` 返回 job 状态（pending/running/completed/failed），含 `created_at`, `attempts`, `error` 字段；租户隔离——只返回当前 tenant 的 job |
| TASK-013 | 异步操作基础设施抽象 | `internal/service/async.go`（新建） | TASK-012 | 3h | `AsyncOp` 结构体封装 `EnqueueJob` + `GET /v1/jobs/{id}` 模式；`Location` header 自动设置；支持 `Retry-After` 和轮询建议 |
| TASK-014 | `restoreObject` 返回 `Location` header | `internal/api/s3compat/handler.go`, `internal/api/rest/handler.go` | TASK-012 | 2h | S3 `POST ?restore` 和 REST `POST /files/*/restore` 返回 `202 Accepted` + `Location: /v1/jobs/{id}` + `Retry-After: 2` |
| TASK-015 | 批删除异步化（可选模式） | `internal/api/rest/handler.go`, `internal/api/s3compat/handler.go` | TASK-013 | 3h | `POST /v1/batch/delete?async=true` 返回 `202` + `Location: /v1/jobs/{id}`；同步模式保持原有行为。`POST /{bucket}?delete`（S3）保持同步（S3 规范要求同步返回逐 key 结果） |
| TASK-016 | Agent Chat 长任务异步支持 | `internal/ai/agent.go`, `internal/api/rest/ai.go` | TASK-013 | 2h | `POST /v1/agent?async=true` 返回 `202 + Location`；后台执行 agent 循环后写 result 到 job result |

**小计：12h**

### 方向四：S3 桶级子资源完备性

| 任务 ID | 标题 | 涉及文件 | 前置 | 工时 | 验收标准 |
|---------|------|---------|------|------|---------|
| TASK-017 | 桶级 Tagging XML 类型 | `internal/api/s3compat/xml.go` | 无 | 1h | 新增 `bucketTagging` XML struct，复用 `s3Tag`；`tagging` 结构已存在但需要确认 bucket 上下文 |
| TASK-018 | 桶级 Tagging 路由分发 | `internal/api/s3compat/handler.go`（`dispatchBucketSubresource`） | TASK-017 | 2h | `case q.Has("tagging")` 分支，调用 `h.getBucketTagging`/`h.putBucketTagging`/`h.deleteBucketTagging`；对象级 tagging 逻辑复用（差异在于 label 从 object 到 bucket） |
| TASK-019 | 桶级 Tagging 后端逻辑 | `internal/api/s3compat/handler.go`, `internal/service/service.go` | TASK-018 | 2h | `GetBucketTagging`/`PutBucketTagging`/`DeleteBucketTagging` 方法；bucket tags 持久化（可复用 `BucketConfig` 的 tags 字段或新增表） |
| TASK-020 | 缺失子资源：`?cors`（桶级 CORS） | `internal/api/s3compat/handler.go` | 无 | 2h | `case q.Has("cors")` → `h.getBucketCORS`/`h.putBucketCORS`/`h.deleteBucketCORS`；REST API 已有 CORS 实现（`router.go:81-83`），可复用 `BucketConfig.CORSRules` |
| TASK-021 | 缺失子资源：`?encryption` | `internal/api/s3compat/handler.go`, internal/api/s3compat/xml.go | 无 | 2h | `GET/PUT/DELETE ?encryption` 路由 + XML struct；存储桶级 SSE 配置（可复用已有 SSE 配置） |
| TASK-022 | 缺失子资源：`?website` | `internal/api/s3compat/handler.go`, internal/api/s3compat/xml.go | 无 | 2h | `GET/PUT/DELETE ?website` 路由 + XML struct；WebsiteConfiguration 存储 |
| TASK-023 | `getBucketAccelerate` 从硬编码改为真实配置 | `internal/api/s3compat/handler.go` | 无 | 1h | 读取 bucket config 的 accelerate 状态；默认 `Suspended`，可通过 config 或 API 启用 |
| TASK-024 | 桶级 Inventory / RequestPayment stubs | `internal/api/s3compat/handler.go` | 无 | 1h | 添加 `?inventory` 和 `?requestPayment` 的 `NotImplemented` stub（S3 规范要求明确返回 `NotImplemented` 而非 404） |

**小计：13h**

### 方向二：WebSocket 实时双向通信 API

| 任务 ID | 标题 | 涉及文件 | 前置 | 工时 | 验收标准 |
|---------|------|---------|------|------|---------|
| TASK-025 | WebSocket 依赖引入 | 无（`go get github.com/coder/websocket`） | 无 | 1h | `go mod tidy`；选择 `nhooyr.io/websocket`（比 gorilla/websocket 更现代，context-aware，性能更好）；验证 CI 通道 |
| TASK-026 | WebSocket 连接管理中间件 | `internal/api/rest/ws.go`（新建） | TASK-025 | 3h | `WSHandler` 负责握手、鉴权复用 `mw.Auth` 上下文、租户提取、心跳检测（ping/pong）、优雅关闭 |
| TASK-027 | 双向事件通道（服务端→客户端 + 客户端→服务器） | `internal/api/rest/ws.go`, `internal/events/bus.go` | TASK-026 | 4h | 客户端→服务器：`{"type":"subscribe","bucket":"*"}` / `{"type":"unsubscribe","bucket":"..."}` / `{"type":"ping"}`；服务端→客户端：现有 `repository.Event` + `{"type":"ack","id":"..."}`。支持 Bucket 级过滤 |
| TASK-028 | WebSocket 路由注册 | `internal/api/rest/router.go` | TASK-027 | 1h | `r.Get("/ws", wsHandler.Serve)`，与 SSE 共存（`/v1/events/stream` 保留不变） |
| TASK-029 | WebSocket 重连恢复（Last-Event-ID） | `internal/api/rest/ws.go` | TASK-027 | 2h | 客户端连上来时发送 `{"type":"resume","last_id":123}`，服务端回放遗漏事件（复用 SSE 的 `replayMissed` 逻辑） |
| TASK-030 | Web UI EventSource 迁移到 WebSocket | `internal/webui/static/index.html` | TASK-028 | 3h | 前端新增 `connectWS()`，在原 EventSource 逻辑旁加 ws 路径；保留 SSE 作为降级；WebSocket 断开自动重连 |
| TASK-031 | chat stream WebSocket 通道 | `internal/api/rest/ai.go`, `internal/api/rest/ws.go` | TASK-027 | 3h | `/v1/chat/stream` 现有 SSE，增加 WebSocket 传输路径：客户端发 `{"type":"chat","query":"..."}`，服务端逐 token 推 |
| TASK-032 | 集成测试：WebSocket 通信 | `internal/api/rest/ws_test.go`（新建） | TASK-026~028 | 2h | 使用 `httptest.Server` + ws 客户端，验证订阅/推送/重连/鉴权 |

**小计：19h**

---

## 2. 执行顺序与依赖图

```
graph TD
    subgraph 第一批[第一批 P0: ~5天]
        T001[TASK-001 TLS Config] --> T003[TASK-003 ListenAndServeTLS]
        T002[TASK-002 Security Headers] --> T004[TASK-004 接入中间件链]
        T001 --> T005[TASK-005 TLS 集成测试]
        T003 --> T005
        T004 --> T005
    end

    subgraph 第二批[第二批 P1: ~12天]
        T006[TASK-006 SecretProvider 扩展] --> T007[TASK-007 密钥持久化]
        T006 --> T010[TASK-010 keyRingProvider 热加载]
        T007 --> T008[TASK-008 密钥管理 REST API]
        T008 --> T009[TASK-009 按需重包装 Job]
        T007 --> T011[TASK-011 密钥健康检查]

        T012[TASK-012 租户面 Job 查询] -.-> T008
        T012 --> T013[TASK-013 异步操作抽象]
        T013 --> T014[TASK-014 restore Location header]
        T013 --> T015[TASK-015 批删除异步化]
        T013 --> T016[TASK-016 Agent Chat 异步]
    end

    subgraph 第三批[第三批 P1-P2: ~10天]
        T017[TASK-017 Bucket Tagging XML] --> T018[TASK-018 Tagging 路由]
        T018 --> T019[TASK-019 Tagging 后端逻辑]
        T020[TASK-020 CORS 子资源] --> T019
        T021[TASK-021 Encryption 子资源]
        T022[TASK-022 Website 子资源]
        T023[TASK-023 Accelerate 去硬编码]
        T024[TASK-024 Inventory/ReqPay stubs]
    end

    subgraph 第四批[第四批 P2: ~12天]
        T025[TASK-025 WS 依赖] --> T026[TASK-026 WS 连接管理]
        T026 --> T027[TASK-027 双向事件通道]
        T027 --> T028[TASK-028 WS 路由]
        T027 --> T029[TASK-029 WS 重连恢复]
        T028 --> T030[TASK-030 WebUI WS 迁移]
        T028 --> T031[TASK-031 Chat Stream WS]
        T027 --> T032[TASK-032 WS 集成测试]
    end

    T008 -.->|#5 顺序依赖| T012
    T009 -.->|复用异步基础设施| T013
    T030 -.->|等待 WS 基础设施| T031
```

### 并行组

| 并行组 | 任务 | 原因 |
|--------|------|------|
| **组 A** | TASK-001 + TASK-002 | 互无依赖，可并行（TLS 配置 vs 安全头中间件） |
| **组 B** | TASK-006 + TASK-012 | SecretProvider 扩展和租户面 Job 查询互不依赖 |
| **组 C** | TASK-017 + TASK-020 + TASK-021 + TASK-022 + TASK-023 + TASK-024 | S3 子资源相互独立，可全并行 |
| **组 D** | TASK-011 + TASK-009 | 密钥健康检查与按需重包装互不依赖，均只依赖 TASK-007 |

---

## 3. 技术风险

### 高风险项

| 风险 | 方向 | 级别 | 分析 | 缓解策略 |
|------|------|------|------|---------|
| **keyRingProvider 运行时热加载的并发安全** | #3 | 🔴 高 | `SecretProvider` 被 `envelopeEncrypter` 在每次读写时调用。`keyRingProvider.keys` 是 `map[string][]byte`，Go map 非并发安全。当前代码无锁（仅启动时写一次），运行时轮换需要加 `sync.RWMutex` | 详见下方"性能瓶颈"部分；使用 `sync.RWMutex` 或 `atomic.Value` 包装不可变快照 |
| **WebSocket 与 SSE 的双轨长期维护成本** | #2 | 🔴 高 | 引入 WebSocket 后，项目同时维护 SSE 和 WS 两个实时通道。SSE 是"半标准"（HTTP 标准早已标准化了 `text/event-stream`），WS 是全双工。两者各有适用场景但代码复用率低 | 建议 `events.Bus` 下层统一，上层适配 SSE 和 WS 两种输出格式。核心事件分发逻辑只写一次 |
| **密钥管理 API 的安全审计缺口** | #3 | 🔴 高 | 密钥轮换/吊销 API 若缺乏审计日志，会成为安全合规的盲区。当前审计日志仅记录 admin 操作，需确保密钥管理操作同样被覆盖 | TASK-008 验收标准中明确要求审计日志写入，review 时确认 |
| **S3 子资源的语义一致性** | #4 | 🟡 中 | S3 `?tagging` 的桶级与对象级行为差异微妙：桶级 tagging 的标签是 bucket label（用于成本分配/权限），对象级是 metadata tag。将对象级 tagging 逻辑直接复用到桶级可能产生语义偏差 | 桶级 tags 以 `BucketConfig.Tags` 持久化，与对象 tags 分开存储 |

### 中风险项

| 风险 | 方向 | 级别 | 分析 | 缓解策略 |
|------|------|------|------|---------|
| **`ListenAndServeTLS` 与现有 HTTP/2 的兼容性** | #1 | 🟡 中 | Go 的 HTTP/2 在 `ListenAndServeTLS` 时自动启用。需确保现有 `ReadHeaderTimeout`/`IdleTimeout` 设置对 HTTP/2 无副作用。HTTP/2 的并发请求可能增加中间件层的并发压力 | 集成测试中验证 HTTP/2 连接；压测中间件链的并发性能 |
| **异步操作与现有幂等性机制的交互** | #5 | 🟡 中 | 异步模式中，客户端可能异步 job 的 `Idempotency-Key` 如何管理？`202 Accepted` 后客户端可能轮询 `GET /v1/jobs/{id}`，但 `Idempotency-Key` 的 4096 字符限制可能不适合存储大型 job result | 异步 job 的幂等性以 job ID 为锚点，不依赖 `Idempotency-Key` |
| **迁移双文件约束（I2）增加密钥 schema 变更成本** | #3 | 🟡 中 | 每次密钥表 schema 变更需要同时更新 sqlite 和 postgres 双份迁移文件。这点已为工程约束（I2），但需要确保新迁移文件编号正确且不在已应用范围内 | 在 KEY_* 命名空间预留迁移编号范围（如 `9000_*`），避免与未来其他迁移冲突 |

### 低风险项

| 风险 | 方向 | 级别 | 分析 | 缓解策略 |
|------|------|------|------|---------|
| `getBucketAccelerate` 硬编码的客户端兼容性 | #4 | 🟢 低 | 当前返回 `Status: "Suspended"` 符合 S3 规范，客户端会 fallback 到常规端点。但未读取 bucket config 可能使期望配置加速的客户端困惑 | 非阻塞修复 |
| `?website` 在存储服务中的实际意义 | #4 | 🟢 低 | 静态网站托管需要完整的 HTTP 路由层改造，超出当前范围。S3 兼容 stub 返回 `NotImplemented` 即可 | 明确标记为 `NotImplemented` |

### 性能瓶颈

| 瓶颈 | 方向 | 说明 | 优化策略 |
|------|------|------|---------|
| **`keyRingProvider.keys` 运行时并发读** | #3 | 每次 SSE 写入都调用 `Current()`，每次读取都调用 `Resolve()`。当前在热路径上。`map[string][]byte` 非并发安全 | ① 使用 `sync.RWMutex`（读多写少场景友好）；② 或使用 `atomic.Value` 包装 `map` 的不可变快照（无锁读，轮换时 `Store` 新快照） |
| **`RewrapStale` 全表扫描** | #3 | 当前实现遍历所有对象 key（`store.List` + page），对百万级对象会产生大量 API 调用 | ① `RewrapStale` 的 job 应分桶、分批处理；② 增加 `last_id` 断点续扫；③ 考虑数据库辅助标记（`sse_key_version` 列索引） |
| **WebSocket 连接数集群级扩展** | #2 | 单实例 WebSocket 连接数受限于 `MAX_INFLIGHT_REQUESTS`（当前默认 0=unlimited），可能导致连接泄露 | ① `PerTenantConcurrencyLimiter` 可用于 WS；② 实现 WS 层面的 `max_connections` 和 `per_tenant_max_connections`；③ 使用 `coder/websocket` 的 context 取消机制 |

### 测试难点

| 难点 | 方向 | 说明 | 策略 |
|------|------|------|------|
| **密钥轮换后的数据一致性验证** | #3 | 轮换密钥后，需验证旧密钥加密的数据仍可读取，新写入使用新密钥 | 实现一个"密钥轮换演练"集成测试：构造多版本密钥 ring→写入对象→轮换→验证旧对象可读→验证新写入使用新 kid |
| **WebSocket 鉴权上下文传递** | #2 | WS 升级时 `http.Hijacker` 后 HTTP 中间件链已执行完毕，auth/tenant 上下文需通过 gorilla 的 `Upgrader.CheckOrigin` 或 `coder/websocket` 的 `AcceptOptions` 中验证 | 在 WS 的 ServeHTTP 中复用 `mw.Auth` 和 `mw.Tenant`，包装 WS 升级 handler |
| **异步 Job 的端到端延迟测试** | #5 | 异步 job 的完成时间不确定，测试需要轮询或等待机制 | 在测试中注册快速完成（synchronous mock）的 job handler 来控制时序 |

---

## 4. 资源评估

### 人员技能要求

| 角色 | 所需技能 | 负责方向 | 人数 |
|------|---------|---------|------|
| **平台工程师** | Go 网络编程（TLS、中间件）、安全最佳实践 | #1 TLS + Security Headers | 1 |
| **存储/加密工程师** | Go 加密库、密钥管理设计、数据库 schema 设计 | #3 密钥管理 | 1 |
| **协议兼容工程师** | S3 API 规范、XML、REST | #4 S3 子资源 | 1 |
| **全栈工程师** | Go WebSocket、JavaScript/EventSource/WebSocket | #2 WebSocket + WebUI | 1 |
| **后端工程师** | Go 并发、Job 队列、异步模式 | #5 异步操作 | 1 |

**最佳团队配置：** 2-3 人（每人兼顾 2 个相邻方向）
- 工程师 A：方向 #1 + #4（安全基础 + S3 兼容，共享 HTTP 路由知识）
- 工程师 B：方向 #3 + #5（密钥管理 + 异步模式，共享 JobPool 和 Repository 层）
- 工程师 C：方向 #2（WebSocket，独立的前后端改造量较大）

### 关键里程碑

| 里程碑 | 时间节点 | 交付物 | 依赖 |
|--------|---------|--------|------|
| M1: 安全基础 | 第 1 周结束 | TLS 启动、安全头、集成测试通过 | TASK-001~005 |
| M2: 密钥管理就绪 | 第 3 周结束 | 密钥 CRUD API、持久化、健康检查；job pool 改造完成 | TASK-006~011 |
| M3: 异步操作模式 | 第 4 周结束 | `GET /v1/jobs/{id}` 租户面端点、`Location` header、异步抽象 | TASK-012~016 |
| M4: S3 桶级完备 | 第 5 周结束 | tagging/cors/encryption/website 子资源响应、accelerate 去硬编码 | TASK-017~024 |
| M5: WebSocket 就绪 | 第 6-7 周结束 | WS 连接、双向通道、WebUI 迁移、chat stream WS | TASK-025~032 |

### 阻塞点与解决策略

| 阻塞点 | 说明 | 解决策略 |
|--------|------|---------|
| **coder/websocket 的引入** | 当前 `go.mod` 无 WebSocket 依赖。选择 `coder/websocket` vs `gorilla/websocket` 需要论证 | 优先 `coder/websocket`（context-aware、纯 Go 实现、更少 goroutine 泄漏风险）。需验证 CI 通道 |
| **密钥管理 API 与已有 SSE 基础设施的集成测试** | SSE 密钥轮换的集成测试需要模拟 `SecretProvider` 的版本化演进 | 在 `secret_test.go` 中实现 `rotatingProvider` 模拟器，依次模拟 V1→V2→轮换→V1 吊销→验证新对象用 V2 写→验证 V1 对象读失败→恢复 V1→验证一致性 |
| **`dispatchBucketSubresource` 的 switch 扩展** | 当前使用 `if-else` 顺序匹配（非 switch），已实现 10 个分支。再增加 5 个会使 `dispatchBucketSubresource` 接近圈复杂度上限（当前实测 ~8/10，新增后可能触线） | 按AGENTS.md约束（圈复杂度 ≤ 10），新增超过 2 个分支时需重构为 map-dispatch pattern：`var subResourceHandlers = map[string]func(...)` |

---

## 5. 质量保证

### 单元测试覆盖要求

| 方向 | 关键测试模块 | 目标覆盖率 | 测试策略 |
|------|------------|-----------|---------|
| **#1** | `middleware/security.go` | 100% | 逐 header 验证；TLS 最低版本验证用 `httptest.NewTLSServer` |
| **#3** | `storage/secret.go`, `storage/rewrap.go` | 90%+ | `SecretProvider` 各实现（env/keyfile/http）的 `Current`/`Resolve`；密钥轮换前后数据可读性验证 |
| **#3** | `storage/encrypt.go` | 95%+ | 使用多版本 key ring 写入→读取→轮换→验证旧 key 可读 |
| **#4** | `api/s3compat/handler.go` | 80%+ | 每个新子资源的 GET/PUT/DELETE 请求 + XML 响应解析 |
| **#5** | `service/async.go` | 90%+ | Job 枚举+查询+超时+取消 |
| **#2** | `api/rest/ws.go` | 75%+ | 连接升级、消息收发、重连恢复；使用 `httptest.Server` + `nhooyr.io/websocket` 测试客户端 |

### 集成测试策略

| 测试套件 | 触发条件 | 描述 |
|---------|---------|------|
| `make test` | 每次提交 | SQLite + local FS 基线路径（零网络、零 Docker）。所有单元测试全绿 |
| `make test-integration` | PR 合并前（CI） | Postgres/pgvector 集成测试（Docker）。密钥持久化 + 异步 Job 端到端验证 |
| `make test-integration-tls` | PR（#1 方向） | `httptest.NewTLSServer` 验证 TLS 1.2/1.3 握手、证书验证、安全头 |
| `make test-integration-ws` | PR（#2 方向） | WebSocket 端到端：升级、鉴权、双向消息、重连恢复 |
| `make test-integration-s3` | PR（#4 方向） | `minio/mc` 客户端对 S3 子资源的 GET/PUT/DELETE 操作验证 |

### 代码审查要点

| 审查关注点 | 方向 | 具体要求 |
|-----------|------|---------|
| **安全** | #1 | 安全头中间件不得影响现有 CORS header；TLS 配置缺省值安全（MinVersion ≥ 1.2） |
| **接口契约** | #3 | `SecretProvider` 扩展不得破坏现有 `envProvider` / `keyRingProvider` / `httpKMS` 的 `Current()`/`Resolve()` 行为 |
| **并发安全** | #3 | `keyRingProvider` 运行时轮换必须加锁。code review 时特别关注 `map` 的并发读写路径 |
| **迁移相容** | #3 | 迁移文件编号不在已应用范围内（不能 edit 或 renumber 已有迁移文件，I2 约束） |
| **圈复杂度** | #4 | `dispatchBucketSubresource` 新增 5+ 分支后必须重构为 map-dispatch（≤10 约束） |
| **nil 安全** | #3,#5 | embedder/llm/reranker 为 nil 时不影响 core CRUD（I5 约束） |
| **错误处理** | #5 | 异步 job 的 result 序列化/反序列化边界——不能因为 job result 过大导致 OOM |
| **前端降级** | #2 | WebSocket 不可用时自动降级到 EventSource（SSE）；WebSocket 断开后重试间隔指数退避 |

### 性能测试需求

| 测试场景 | 方向 | 基准 | 目标 |
|---------|------|------|------|
| TLS 握手延迟 | #1 | 当前 HTTP 平均延迟 | TLS 1.3 延迟增量 ≤ 5ms（P99） |
| SSE 加密写入吞吐 | #3 | 当前无加密写入 TPS | 密钥轮换场景下 TPS 下降 ≤ 3% |
| WebSocket 并发连接 | #2 | N/A | 500 并发连接稳定 30 分钟，内存增量 ≤ 200MB |
| S3 桶级子资源响应 | #4 | N/A | 每个子资源请求 ≤ 50ms（P95）|
| 异步 Job 轮询吞吐 | #5 | 当前同步请求延迟 | `GET /v1/jobs/{id}` 支持 100 RPS 轮询 |

---

## 6. 实施计划

### 甘特图概览

```
周次       1     2     3     4     5     6     7
Phase 1   ████████████
  T001    ████
  T002    ████
  T003      ████
  T004        ██
  T005        ████

Phase 2                ██████████████████████████
  T006                 ████
  T007                 ██████
  T008                    ██████
  T009                       ████
  T010                 ██████
  T011                       ████
  T012                 ████
  T013                    ██████
  T014                       ████
  T015                          ██████
  T016                          ████

Phase 3                               ████████████████████
  T017                                ██
  T018                                 ████
  T019                                   ████
  T020                                ████
  T021                                ████
  T022                                ████
  T023                                  ██
  T024                                  ██

Phase 4                                            ████████████████████
  T025                                             ██
  T026                                               ██████
  T027                                                 ████████
  T028                                                     ██
  T029                                                     ████
  T030                                                       ██████
  T031                                                       ██████
  T032                                                         ████
```

### 阶段 1：安全基础（第 1 周，~9h 净工时）

**目标：** 为所有传输层加上 TLS 和安全头，建立基础安全态势。

```
Day 1-2:  TASK-001 TLS Config + TASK-002 Security Headers（并行）
Day 3:    TASK-003 ListenAndServeTLS
Day 4:    TASK-004 中间件链接入 + TASK-005 集成测试
Day 5:    Code review + 修复 + `make check` 全绿
```

**交付物：**
- `config.Config.TLS` 结构体，环境变量 `TLS_*`
- `middleware.SecurityHeaders` 中间件
- `cmd/server/main.go` 支持双模式启动（HTTP / HTTPS）
- 集成测试验证 TLS 1.2+ 和安全头

### 阶段 2：密钥管理和异步模式（第 2-4 周，~27h 净工时）

**目标：** 建立密钥管理全生命周期 API + 异步操作基础架构。

```
Week 2:
  Day 1-2:  TASK-006 SecretProvider 扩展 + TASK-012 租户面 Job 查询（并行）
  Day 3-4:  TASK-007 密钥持久化层 + 迁移文件
  Day 5:    Code review + schema 迁移验证

Week 3:
  Day 1-2:  TASK-008 密钥管理 REST API
  Day 3-4:  TASK-010 keyRingProvider 热加载 + TASK-013 异步操作抽象
  Day 5:    TASK-009 按需重包装 Job + TASK-011 密钥健康检查

Week 4:
  Day 1-2:  TASK-014 restoreObject Location header
  Day 3-4:  TASK-015 批删除异步化 + TASK-016 Agent Chat 异步
  Day 5:    阶段集成测试 + code review
```

**交付物：**
- `POST/GET/DELETE /v1/admin/crypto/keys` API
- `POST /v1/admin/crypto/rewrap` 异步 job
- `keyRingProvider` 运行时轮换支持
- `GET /v1/jobs/{id}` 租户面端点
- `Location` header 和 `202 Accepted` 模式
- 密钥健康检查指标（`/healthz` + OTel）
- `sse_keys` 迁移文件（sqlite + postgres）

### 阶段 3：S3 桶级子资源完备（第 5 周，~13h 净工时）

**目标：** 补齐 5 个缺失的 S3 桶级子资源。

```
Week 5:
  Day 1:    TASK-017 Tagging XML + TASK-020 CORS + TASK-023 Accelerate fix（并行）
  Day 2-3:  TASK-018 Tagging 路由 + TASK-019 Tagging 后端 + TASK-024 stubs
  Day 3-4:  TASK-021 Encryption + TASK-022 Website
  Day 4-5:  `dispatchBucketSubresource` 重构为 map-dispatch
  Day 5:    S3 集成测试（minio/mc 客户端验证）
```

**交付物：**
- 桶级 `?tagging`（完整的 GET/PUT/DELETE）
- 桶级 `?cors`（跨域配置，复用 REST API 实现）
- 桶级 `?encryption`（SSE 配置）
- 桶级 `?website`（NotImplemented 或 stub）
- 桶级 `?inventory` / `?requestPayment`（NotImplemented stubs）
- `getBucketAccelerate` 从硬编码改为真实配置读取
- `dispatchBucketSubresource` 圈复杂度 ≤ 10

### 阶段 4：WebSocket 实时通信（第 6-7 周，~19h 净工时）

**目标：** 实现全双工实时通信通道，WebUI 迁移到 WebSocket。

```
Week 6:
  Day 1:    TASK-025 WS 依赖引入
  Day 2-3:  TASK-026 WS 连接管理（鉴权、心跳、优雅关闭）
  Day 4-5:  TASK-027 双向事件通道

Week 7:
  Day 1:    TASK-028 WS 路由 + TASK-029 重连恢复
  Day 2-3:  TASK-030 WebUI WS 迁移 + TASK-031 Chat Stream WS
  Day 4:    TASK-032 WS 集成测试
  Day 5:    `make check` + 端到端验证 + code review
```

**交付物：**
- `GET /v1/ws` WebSocket 端点（与 SSE 共存）
- 客户端→服务器消息格式（subscribe/unsubscribe/chat/ping）
- 重连恢复（Last-Event-ID 兼容）
- WebUI 自动选择 WebSocket / EventSource（降级策略）
- Chat Stream 支持 WebSocket 传输
- 500 并发连接稳定性测试

---

## 汇总

| 方向 | 任务数 | 净工时 | 建议批次 | 风险等级 |
|------|--------|--------|---------|---------|
| #1 TLS | 5 | 9h | P0 第一批 | 🟢 低 |
| #3 密钥管理 | 6 | 15h | P1 第二批 | 🔴 高（并发安全） |
| #5 异步操作 | 5 | 12h | P1 第二批 | 🟡 中 |
| #4 S3 子资源 | 8 | 13h | P1→P2 第三批 | 🟢 低 |
| #2 WebSocket | 8 | 19h | P2 第四批 | 🔴 高（依赖引入+双轨维护） |
| **总计** | **32** | **68h** | **~7 周** | — |

**核心建议：**
1. **采纳建议方案的批次顺序**（#3 → #5 → #4 → #2），密钥管理与已有 SSE 基础设施耦合最紧，复用收益最大
2. **`dispatchBucketSubresource` 尽早重构为 map-dispatch**，避免圈复杂度触碰 10 的上限
3. **`keyRingProvider` 用 `atomic.Value` + 不可变快照**而非 `sync.RWMutex`，避免热路径上的锁竞争
4. **WebSocket 保留 SSE 作为降级路径**，不在本次替换 EventSource，而是让 WebUI 自动选择
5. **每次修改后运行 `make check`**（I0 约束，不通过不提交）
