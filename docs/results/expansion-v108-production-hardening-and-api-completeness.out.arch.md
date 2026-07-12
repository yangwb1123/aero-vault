现在我已经对整个系统有了透彻的理解。以下是我的架构分析。

---

# 架构分析：Aero-Vault 系统进行五个扩展方向

## 1. 架构评估

### 优势

当前系统展现出了精湛的工程实践，值得强调：

**清晰的端口与适配器（六边形）架构。** `FileService` 是核心业务控制器，与协议（REST、S3、WebDAV、MCP）、持久化（`Storage` 接口、`Repository` 接口）以及可观测性（OpenTelemetry 连接点）完全解耦。这种分层在 `main.go` 的启动序列中得到了精确体现：`config → storage → repo → service → workers → middleware → router`。

**通过接口实现依赖倒置是亮点。** `Storage` 接口定义了 11 个方法，目前有 4 个后端（local、S3、OSS、COS），通过工厂模式构建。`Repository` 接口（~65 个方法）以 SQLite 和 Postgres 双实现提供持久化抽象。这两者都支持不修改核心业务逻辑即可插入新实现。

**事件驱动的工作流设计执行到位。** `EventBus` 与 `jobs.Pool` 的结合允许异步处理（索引、反病毒、复制、webhook），与同步请求路径解耦。`jobs` 包（带注册表 + 工作池 + 重试退避 + 回收）是一个精心设计的最小但完整的后台工作队列，无外部依赖。

**最优默认的安全架构。** AI、pgvector、Qdrant、事件、集群模式、WebDAV 全部默认关闭（`opt-in`）。`nil` 嵌入器/LLM/重排序器不会破坏核心 CRUD 路径。这遵循了稳健安全原则。

**针对性的可测试性基础设施。** 独立的合同测试（`storage/contract_test.go`）、mock AI 组件（`MockLLM`、`HashEmbedder`）、用于 handler 测试的 `httptest` 夹具，以及 SQLite 内存数据库进行单元测试——所有这些都确认了基线验证路径。

### 局限性

**身份验证和 API 密钥管理与服务共享同一进程空间。** `Auth` 注册表内置于 `main.go` 中，中间件链是固定的。虽然目前这没问题，但没有用于独立身份验证网关（如 OAuth2 代理或外部 IDP 的 Sidecar）的扩展点。`JWTIssuer` 字段暗示了多发行者支持，但尚未实现——这是一个未完成的抽象，如果引入外部身份提供者可能会产生问题。

**`SecretProvider` 接口是只读的，没有运行时生命周期。** 具体的实现——`envProvider`、`keyRingProvider`、`newHTTPProvider`——都是在启动时构造的，一旦加载就无法轮换密钥。`keyRingProvider` 支持版本化（会查找 `primary` ID 和其他已记录的版本），但这个接口只在启动时初始化。没有 `Rotate()`、`Revoke(id)`、`ListVersions()` 或 `Health()`。`DataKeyWrapper` 同样如此——当底层 KMS 本身可操作时，没有运行时运行状况检查。

**如果密钥提供者配置不合理，`Object.DeleteChunks` 的失败日志但非致命设计可能导致静默数据不一致。** 虽然索引器跳过在逻辑上是合理的（索引失败不应阻塞上传），但需要一种机制来检测长期的索引与存储偏移——目前不存在这种机制。

**`Repository` 接口因为包含几乎所有操作而过于庞大（~65 个方法）。** 这违反了接口分离原则（ISP）。影响如下：
- 任何后端实现（SQLite、Postgres）都必须实现大量与上下文无关的方法。
- 单元测试需要一个完整的模拟，而不是只模拟所需方法的窄接口。
- 添加新的存储后端需要实现全部 65 个方法。

**没有租户级别的资源隔离。** 并发限制器（`PerTenantConcurrencyLimiter`）和速率限制器（`RateLimiter`）存在，但所有租户共享相同的 `jobs.Pool`（`cfg.Jobs.Workers` 是一个全局数字）。一个租户如果提交大量后台作业（例如，批量上传 → 索引作业），可能会使租户的工作池资源枯竭。需要一个租户作业优先级或租户级作业工作池。

**缺少服务网格集成点。** 没有 gRPC 接口，没有用于运行状况检查的专用 `health` 协议（`/healthz` 和 `/readyz` 是基本的 HTTP get 端点），也没有对 Kubernetes 原生化支持（如 `startupProbe`、`livenessProbe` 处理程序）的考虑。

### 架构债务

1.  **`repository.Repository` 接口膨胀。** 65 个方法违反了 ISP。重构建议：拆分为 `ObjectRepository`、`BucketRepository`、`ChunkRepository`、`JobRepository`、`AuthRepository`、`AuditRepository`、`QuotaRepository`。具体实现（SQLite struct）可以嵌入所有小型接口，但消费者可以只针对他们需要的接口编程。

2.  **`service/file.go` 靠近 250 行限制。** 如 `AGENTS.md` 所定义，单文件限制为 500 行。`file.go`（248 行）虽然目前还未超限，但已经是系统中最大的文件之一。其增长轨迹表明需要按领域拆分包（例如 `service/object.go`、`service/bucket.go`、`service/admin.go`）。

3.  **配置结构设计为平面枚举模式。** `Config` 结构体将所有内容都作为环境变量拉取，没有嵌套结构体进行分组。例如，与 AI 相关的键（`AIEmbed*`、`AIChat*`、`AISearch*`、`AIVector*`）是 `AIConfig` 的扁平字段，而不是嵌套的 `EmbedConfig`、`ChatConfig` 等。随着每个方向增加配置，这可能会失控。

4.  **MCP 工具列表被硬编码在 `internal/mcp/server.go`。** 如果添加新工具，它们必须在 switch 语句中手动注册。没有通过反射或声明式工具清单自动发现的机制。

5.  **`WebUI` 被嵌入为静态文件服务。** 虽然这对部署有利，但它失去了与 API 版本控制（UI 可能依赖于 `/v1/search`，如果 API 演进出 v2，没有版本路由映射）和安全 origin 检查的协调。

---

## 2. 扩展方向

### 方向 A：TLS 与安全传输层（P0）

**为什么需要：**

缺少 TLS 使得在生产中部署该系统于未经 TLS 终止的反向代理之后成为唯一路径。虽然 Kubernetes 入口/负载均衡器通常处理 TLS，但独立部署（例如裸机、小型 VPS）或处理敏感工作负载的内部网络需要原生的 TLS 支持。此外，完全缺少安全标头（HSTS、CSP、XFO）使得客户端容易受到点击劫持和 MIME 嗅探攻击。

缺少的具体内容：
- `ListenAndServeTLS()` 而非 `ListenAndServe()`
- 用于 HSTS、CSP、X-Frame-Options、X-Content-Type-Options、Referrer-Policy 的安全标头中间件
- 证书热重载（用于 Let's Encrypt 自动轮换）
- mTLS 用于工作负载身份（介于服务本身与其存储后端或复制目标之间）

**核心挑战与技术难点：**

1.  **证书轮换而不停机。** HTTP `Server` 不原生支持证书热重载。需要在 `fsnotify` 上包装 `tls.GetCertificate` 回调或在信号处理程序中重新加载。对于启用 mTLS 的客户端证书轮换，这更加复杂。
2.  **TLS 配置的向后兼容性。** 当前系统没有 `AppConfig.TLS` 字段。添加一个时必须与现有部署完全兼容——现有用户不应被强制设置 TLS 配置。
3.  **`ListenAndServe` 与 `ListenAndServeTLS` 的互斥性。** 标准的 Go 模式是选择调用其中一个，但可能存在需要同时支持 HTTP 和 HTTPS 的场景（例如 HTTP→HTTPS 重定向）。这需要一个包装器来管理两个监听器。
4.  **mTLS 的客户端证书验证会影响中间件顺序。** 中间件链当前在 HTTP 处理程序层运行；如果 mTLS 是在传输层处理的，则证书信息必须通过 `context` 传递，以便下游中间件（特别是 `Tenant` 提取器）可以使用它进行身份验证。这改变了 `Tenant` 中间件与 `Auth` 的关系，为安全模型增加了新的维度。

**预期架构变更：**

- 新增 `internal/tls` 包含 `Manager` 接口（带 `fsnotify` 或 `GetCertificate` 回调实现）
- 在 `AppConfig` 中新增 TLS 配置块（证书路径/密钥路径/CA 包/最小版本/密码套件）
- 在 `main.go` 中新增 `buildTLSConfig()` 方法，可选地包装 `srv.ListenAndServeTLS()`
- 新增 `SecurityHeaders` 中间件，插入在中间件链的 `Recoverer` 之后
- 测试策略：为 TLS 配置和不配置两种情况添加集成测试

**对现有系统的影响：**

- 零：TLS 完全可选。现有部署不受影响。
- 安全标头中间件默认开启但可配置（适用于本地开发）。
- 唯一需要注意的地方是：如果启用了 HSTS（`max-age` 较大），开发人员在 HTTP 上进行本地测试时需要禁用中间件。

### 方向 B：WebSocket 实时通信（P2）

**为什么需要：**

SSE 是单方向的（服务器 → 客户端）。对于文件上传进度报告、实时编辑协作、管理仪表板中的交互式 shell 会话，或者 MCP 传输的双向 RPC 等场景，需要 WebSocket。当前系统在 SSE 中有 `Subscribe() <-chan Event` 模式，但这是一个 go 通道，不是网络可寻址的。WebSocket 允许客户端发出命令并通过同一连接接收流式响应。

**核心挑战与技术难点：**

1.  **清理程序与身份验证上下文。** WebSocket 连接是长连接的。如果 JWT/API 密钥在连接生命周期内过期，服务器必须优雅地终止套接字。这需要一个在 gorilla/websocket 读取循环中运行的周期性令牌验证 goroutine。
2.  **连接背压与队列管理。** SSE 由 go 通道提供支持；WebSocket 需要一个写入队列，如果客户端消耗跟不上，这个队列会自然反压到事件发布器。设计一个无界通道可能导致 OOM 崩溃。
3.  **HTTP 升级是全局的。** 由于 `buildDispatcher` 在之前检查 WebDAV，WebSocket 升级需要在歧义解决方面有清晰的路径。是将 `/ws` 添加为专用的 chi 路由，还是使用 `http.Hijacker` 进行协议升级？
4.  **与 SSE 的共存与检测。** 前端（目前使用 `EventSource`）不应需要同时支持 SSE 和 WebSocket 的代码路径。需要一个特性检测垫片来优雅降级。

**预期架构变更：**

- 新增 `internal/ws` 包，包含 Hub（连接管理器）每个 WebSocket 客户端处理程序
- 向 `EventBus` 添加 `SubscribeBuffered(capacity int)` 方法，以防止慢速消费者反压事件循环
- 在 `router.go` 中新增路由（例如 `GET /ws/events`）
- 前端变更：将 `EventSource` 替换为 `WebSocket` 客户端，并自动降级到 SSE

**对现有系统的影响：**

- 中等。现有 SSE 实现保持不变；WebSocket 是一个新的替代传输。
- 如果 WebSocket 处理程序在 SSE 端点之前匹配（优先级路由），则现有 `EventSource` 客户端继续通过 SSE 工作。
- 需要添加 `gorilla/websocket` 作为 `go.mod` 依赖。

### 方向 C：密钥管理 API（P1—从第二批升级）

**为什么需要：**

`SecretProvider` 已经支持版本化密钥（`keyRingProvider.Current()` 返回 `(id, key)`，`Resolve(id)` 处理旧版本），但密钥轮换需要重启。运行时 API 端点允许操作员在不中断服务的情况下轮换、撤销和审计密钥。这直接满足了合规要求（PCI-DSS 密钥轮换、SOC2 密钥管理）。

此外，`DataKeyWrapper`（用于远程 KMS）没有运行时健康检查端点。在生产事件（KMS 停机、网络分区）期间，操作员无法在不检查应用日志的情况下区分包装器故障和正常故障。

**核心挑战与技术难点：**

1.  **线程安全的密钥轮换。** `keyRingProvider` 的状态在启动时是一次性设置的，并在整个生命周期内被视为不可变。使其可变需要原子交换（`atomic.Pointer`）和读-复制-更新模式，以避免对现有读取器造成锁竞争。
2.  **被轮换密钥的宽限期与实际失效。** 撤销密钥时，何时可以安全删除旧密钥材料？只有在 ConfirmNoObjectsReferenceKeyID(id) 之后——这需要扫描 envelope 元数据，而目前元数据不按密钥 ID 索引。
3.  **审计与问责。** 密钥管理 API 的每个操作都必须被审计（`RecordAudit`），并且身份验证必须严格限于 operator 作用域密钥或 mTLS 客户端证书。密钥轮换没有 API 速率限制。
4.  **自动轮换调度。** 对于全自动合规，需要一个类似于 cron 的调度器，例如 `KeyRotationPolicy{Interval: 90d}`。这需要一个 `cron` 包或一个用于 `key_rotation` 作业类型的作业处理程序。

**预期架构变更：**

- 扩展 `SecretProvider` 以包含 `Rotate(id, keyMaterial) error`、`Revoke(id) error`、`ListVersions() []KeyVersion`、`Health() error`
- 新增 `internal/crypto/admin.go` 拥有 REST 处理程序（`POST /v1/admin/crypto/rotate`、`POST /v1/admin/crypto/revoke`、`GET /v1/admin/crypto/versions`、`GET /v1/admin/crypto/health`）
- 新增 `key_versions` 仓库表，用于持久化密钥元数据（ID、创建时间、状态、指纹）
- 新增后台作业类型 `crypto.rewrap`，用于在密钥轮换后按需重包装对象

**对现有系统的影响：**

- 最小。向后兼容：`envProvider`（单密钥，无版本化）仍可工作。所有新方法都可以有默认的 no-op 实现。
- `keyRingProvider` 需要迁移到并发安全的数据结构。
- 现有的 `maybeRewrapSSE` 启动时重包装保持不变，但也可以作为 API 操作触发。
- 影响为零：代码路径仅在通过 API 调用时执行。

### 方向 D：S3 桶级子资源完备性（P1）

**为什么需要：**

`dispatchBucketSubresource` 缺少 5 个标准 S3 子资源（`?tagging`、`?encryption`、`?website`、`?inventory`、`?requestPayment`）。虽然许多 S3 客户端可能不需要这些，但桶级 `?tagging` 是最常请求的功能——并且后端代码已经存在（对象级标签使用相同的 `repository.UpdateTags` 模式）。桶级标签用于成本分配和账单报告，这是 S3 兼容存储系统的一个关键用例。

`getBucketAccelerate` 返回硬编码的 `Suspended`，忽略桶参数。这是一个次要协议合规性问题——它应该至少验证桶是否存在并返回有效的 XML。

**核心挑战与技术难点：**

1.  **桶级标签需要存储模式变更。** 对象标签存储在 `tags` 列中，但桶是一个不同的域（`BucketConfig`）。桶标签可以存储在 `bucket_tags` 表中，也可以作为 `BucketConfig` 上的 JSON 列。由于桶的数量远少于对象，JSON 列更简单。
2.  **`?website` 是一个有状态的服务模拟。** 真正的 S3 静态网站托管需要 DNS 解析、自定义错误文档和指标。对于 Aero-Vault 来说，这是一个范围扩展。应该被标记为“未实现”并返回 `501 NotImplemented`，而不是悄无声息地忽略。
3.  **`?inventory` 需要调度器基础设施。** 清单报告是定时生成的（每日/每周）。这需要一个作业处理程序和一个清单输出格式（CSV/Parquet）。一个最简单的实现可以生成一个 CSV 对象并存储在存储桶中。
4.  **XML 序列化。** 每个子资源都需要一个 XML struct 用于响应序列化。当前 `xml.go` 没有 tagging、encryption 或 website 结构体。在严重程度上这是一个小问题（结构体很简单），但必须为每个子资源添加。

**预期架构变更：**

- 向 `BucketConfig` 添加 `Tags map[string]string` 字段
- 在 `repository.Repository` 中添加 `PutBucketTags(ctx, tenant, bucket, tags)` 和 `GetBucketTags(ctx, tenant, bucket)` 操作
- 向 `dispatchBucketSubresource` 添加新的 switch 分支
- 为生成的子资源（例如 `BucketTagging`、`ServerSideEncryptionConfiguration`）添加 XML 结构体
- 对于 `?website`、`?inventory`、`?requestPayment`：返回带有相应错误码的有效 `S3ErrorResponse`

**对现有系统的影响：**

- 最小。新子资源不会影响现有 S3 操作路径。
- `BucketConfig` 模式变更可以通过复合类型列添加标签，从而避免迁移。
- 现有的 `BucketConfig.ToXML()` 序列化程序会影响所有桶描述——需要小心处理以确保标签输出包含在内。

### 方向 E：异步操作模式与长任务 API（P1）

**为什么需要：**

批删除、批标签、多分片完成以及恢复操作都同步阻塞，直到所有子任务完成。对于大量操作（例如删除 1000 个对象），HTTP 连接保持打开状态，这会耗尽连接池。异步模式（`202 Accepted` + `Location: /v1/jobs/{id}`）允许客户端轮询进度。

基础设施已经存在：`jobs.Pool`、`jobs.Queue`、`jobs.Registry` 都已完备。缺失的部分是一个公共的 `GET /v1/jobs/{id}` 端点，用于轮询作业状态，以及对长期运行的操作返回 `202` 的处理程序。

**核心挑战与技术难点：**

1.  **作用域与租户隔离。** 作业池由所有租户共享。一个 `/v1/jobs/{id}` 端点必须验证请求的租户是否与创建作业的租户匹配。这需要在创建作业时存储 `tenant_id`（目前是 `repository.Job` 的一部分，但需要填充）。
2.  **作业结果与回调。** 作业完成后，客户端需要能够检索结果（例如失败列表、总处理时间）。这需要一个 `result` 列（目前存在：`CompleteJob(ctx, id, result)`），但需要结构化（例如 JSON 有效负载）。
3.  **Webhook 回调。** 除了轮询，客户端可能希望注册一个回调 URL，以便在作业完成时接收通知。这需要向 `EnqueueJob` 添加一个 `callbackURL` 参数，并让作业池在完成时触发 webhook。
4.  **取消长时间运行的作业。** 如果客户端在作业完成后不再关心，应该能够取消它。这需要 `CancelJob(id)` 操作和作业处理程序中的取消信号集成。

**预期架构变更：**

- 在 REST 路由器中新增 `GET /v1/jobs/{id}`（公开，从 admin 子路由提升）
- 修改批处理处理程序（`BatchDelete`、`BatchTag`、`Restore`）以分发作业并返回 `202`
- 向 `Job` 添加 `callbackURL` 和 `tenantID` 字段（在 `repository.Job` 中已存在 `TenantID`）
- 在作业完成时，在 `jobs.Pool` 中添加可选的回调执行

**对现有系统的影响：**

- 现有批处理端点仍然可以同步工作（向后兼容模式），或者通过配置标志迁移到异步模式（首选：`ASYNC_BATCH_OPS=true`）。
- 数据库模式：`jobs` 表已经有一个 `result` 列，但可能需要添加 `callback_url`。
- 前端变更：上传/删除操作需要显示轮询状态指示器。

---

## 3. 接口设计建议

### 关键设计原则

1.  **在现有边界处引入抽象，而不是创建新的。** `SecretProvider` 已经是一个边界。扩展它——无需创建新的 `KeyManager` 接口。`Storage` 已经是一个边界。如果需要 S3 兼容性，则扩展子资源处理程序，而不是创建一个新的 S3 兼容层。

2.  **组合优于继承。** 所有新的接口都应该是可组合的。示例：
    - `SecretProvider` 扩展：`VersionedKeyProvider` 嵌入 `SecretProvider` 并添加 `Rotate/Revoke/ListVersions`。
    - `AsyncOperation`：一个封装 `Enqueue` + `StatusURL` + `CallbackURL` 的接口，而不是向 `FileService` 添加同步+异步方法。

3.  **默认行为不变。** 每个新接口都必须为旧有默认行为提供一个零值实现：
    - `TLSConfig{}` → `ListenAndServe()`（当前行为）
    - `SecretProvider` 扩展默认返回 `ErrNotSupported`
    - 异步模式回退到同步（当前行为）

4.  **配置应该是声明式的，而不是命令式的。** 例如，密钥轮换策略不应该通过 API 调用来调度，而应该通过配置声明：`KEY_ROTATION_INTERVAL=90d`。

### 新的抽象层

**建议引入 `internal/tls` 包**，用于管理：

```go
type Manager interface {
    GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error)
    Reload() error  // 从磁盘重载文件
    Healthy() bool
}
```

这抽象了证书文件监控和 Let's Encrypt 自动续期之间的差异。

**建议引入 `internal/jobs/types.go`**，用于定义公共作业类型和结构体：

```go
type JobStatus struct {
    ID        int64     `json:"id"`
    Type      string    `json:"type"`
    TenantID  string    `json:"tenant_id"`
    Status    string    `json:"status"`
    Progress  float64   `json:"progress"` // 0.0–1.0
    Result    string    `json:"result,omitempty"`
    Error     string    `json:"error,omitempty"`
    CreatedAt time.Time `json:"created_at"`
    CompletedAt *time.Time `json:"completed_at,omitempty"`
}
```

这为公开的作业轮询端点提供了明确的结构化响应。

**不建议引入单独的 `KeyManagement` 包。** 由于 `SecretProvider` 和 `DataKeyWrapper` 都驻留在 `internal/storage` 中，添加密钥管理 API 端点最自然的归属是：

- Cryptography 处理程序：`internal/api/rest/admin.go`（管理作用域）
- 操作实现：`internal/storage/secret.go`（扩展 `SecretProvider`）

这最大限度地减少了新包的创建，并保持了内聚性。

### 向后兼容性策略

| 扩展 | 兼容策略 |
|----------|-----------------------|
| TLS | 配置检测：如果 `app.tls.cert_file` 为空 → `ListenAndServe()`。 |
| WebSocket | 新路由 `/ws/*` 不会干扰现有的 SSE。 |
| 密钥管理 API | `SecretProvider.Rotate()`、`Revoke()`、`ListVersions()` 使用默认的 no-op 实现，返回 `ErrNotSupported`。 |
| S3 子资源 | 缺失的子资源静默忽略（当前行为），或显式地返回 `501 NotImplemented`。 |
| 异步模式 | 配置检测：`ASYNC_BATCH_OPS=false` → 同步（当前行为）。 |

---

## 4. 技术选型

### 依赖评估

验证报告正确指出当前代码库没有外部 WebSocket 或高级 KMS SDK 依赖。以下是对建议依赖的评估：

| 依赖 | 用途 | 理由/标准 |
|----------|------|--------------|
| `golang.org/x/crypto` | ACME 自动续期，mTLS CA 验证 | 已经通过 Go 模块间接使用。用于 `tls.Certificate` 解析。 |
| `gorilla/websocket` | WebSocket 升级 + 连接管理 | 成熟的、标准的 Go WebSocket 库（5.9k GitHub stars）。验证报告说它不存在；需要添加。没有意义重新发明。 |
| `go.step.sm/crypto` | 用于 KMS 操作的 X.509 + JOSE/JWE | 可选的。如果 `DataKeyWrapper` 需要与外部 KMS（Azure Key Vault、AWS KMS）集成。仅在 KMS 包装器中需要。 |
| `fsnotify/fsnotify` | 证书文件变更监控 | 可选的。如果实现证书热重载需要。如果只使用 `GetCertificate` 回调（每次接受时读取文件），则不需要。 |

**自我构建决策点：**

| 组件 | 自建 | 采购 | 理由 |
|----------|---------|-----------|---------|
| TLS 证书重载 | ✅ | | 小抽象（~80 行 `fsnotify` 或每次接受读取文件）。Go 的 `tls.Config` 通过 `GetCertificate` 支持这一点。 |
| 密钥轮换逻辑 | ✅ | | 专门针对 `keyRingProvider` 数据模型。没有通用库匹配 `keyIDPattern` + JSON 密钥环。 |
| WebSocket Hub | | ✅ | `gorilla/websocket` 连接管理和广播模式已被大量使用且经过实战检验。内置 Hub 会是一个常见的重复造轮子。 |
| 作业回调调度 | ✅ | | 建立在已有的 `EventBus` 之上。核心逻辑是几行代码。 |

### 架构影响评估

**大规模操作的责任划分（~100k RPS 或 ~1 PB 存储）：**

| 组件 | 当前设计 | 扩展瓶颈 | 建议演进 |
|----------|---------------|-----------------|---------------------|
| 作业池 | 单进程，~N 个工作线程 | 所有作业共享一个 SQLite 后端 → 连接池耗尽 | 对于 >10 个节点，通过 Postgres `LISTEN/NOTIFY` 进行集群作业调度 |
| WebSocket 连接 | 每进程内存映射 | 几万个 WebSocket 连接消耗内存 | 使用 Redis Pub/Sub 支持水平缩放 |
| 密钥管理 | 进程本地密钥环 | 密钥轮换不是原子的 | 使用 Postgres 咨询锁 + 持久化密钥版本表进行外部协调 |
| TLS 终止 | 无（缺失） | 证书轮换需要进程重启 | 使用 `GetCertificate` 回调进行无服务器证书管理（对于大容量场景，配置反向代理） |

验证报告提到的中间件顺序（`AccessLog → concurrencyMW → Recoverer → OTel → RateLimit → Tenant → Auth → CORS → RequestID`）是通过 `applyMiddleware` 函数构建为嵌套包装器的。这在当某个中间件崩溃（`Recoverer` 在最外层）或超过速率限制（`RateLimit` 在 `Auth` 之前）时，对诊断问题有细微的影响——如果请求被速率限制而没有通过身份验证，`Tenant` 上下文将是 `default` 而不是真正的租户。这是可以接受的，因为无论租户如何，速率限制都应该使用相同的桶——但逐租户速率限制需要将 `Tenant` 放在 `RateLimit` 之前，这与当前的顺序相矛盾。这是一个值得注意的细微设计决策。

---

## 5. 实施路线图

### 优先级分层

| 层级 | 方向 | 基本原理 |
|-------|-----------|--------------|
| **P0** | TLS/安全（#1） | 安全基础——没有 TLS 的生产环境是不可接受的。影响：所有 HTTP 流量。依赖于：无。 |
| **P1** | 密钥管理 API（#3） | 与现有 SSE 基础设施紧密耦合；零新依赖。第一性原理用例：操作员需要在零停机时间内轮换密钥。 |
| **P1** | 异步操作（#5） | 启用 #3 的重包装 API，以提高用户对后台作业的体验质量。依赖于：作业基础设施（已存在）。 |
| **P1** | S3 子资源（#4） | 协议合规性。桶级 `?tagging` 具有最高的成本分配商业价值。 |
| **P2** | WebSocket（#2） | 最大的依赖冲击（新包、前端重构、连接状态管理）。增量价值在实时 UI 功能中最为明显。 |

### 阶段划分

**阶段 1（P0—1 周）：TLS 与安全标头**

- 里程碑：服务器支持 `LISTEN_AND_SERVE_TLS` 环境变量
- 可交付物：
  - `internal/tls` 包含配置 + 证书重载
  - `SecurityHeaders` 中间件
  - `AppConfig.TLS` 配置块
  - 更新 `/docs` 和 `README.md`
- 风险：证书路径配置错误导致启动失败。缓解：在无法加载 TLS 配置时回退到明文 HTTP 并发出警告日志。

**阶段 2（P1—2 周）：密钥管理 API**

- 里程碑：密钥可以通过 API 轮换，零停机
- 可交付物：
  - 扩展 `SecretProvider`：`Rotate() error`、`Revoke(id) error`、`ListVersions() []KeyVersion`
  - `keyRingProvider` 迁移到并发安全的 `atomic.Pointer`
  - REST 端点：`POST /v1/admin/crypto/rotate`、`POST /v1/admin/crypto/revoke`、`GET /v1/admin/crypto/versions`
  - 持久化密钥版本表 + 迁移
  - 审计日志记录所有密钥操作
- 风险：密钥轮换后现有对象的密钥 ID 引用变为孤儿。缓解：添加 `ConfirmNoObjectsReferenceKeyID(id)` 查询操作；如果对象仍然引用它，则拒绝撤销。

**阶段 3（P1—1 周）：面向租户的异步操作**

- 里程碑：批删除返回 `202 Accepted` 并带有可轮询的作业 ID
- 可交付物：
  - `GET /v1/jobs/{id}` 端点（从管理员范围提升）
  - `BatchDelete` 和 `BatchTag` 返回 `202` 以及作业元数据
  - 作业完成时的 Webhook 回调（可选）
  - 前端作业状态轮询指示器
- 风险：`ASYNC_BATCH_OPS=false` 的降级兼容性。缓解：所有现有客户端不会中断；如果标志关闭，它们将获得同步行为。

**阶段 4（P1—1 周）：S3 桶级子资源**

- 里程碑：在 `dispatchBucketSubresource` 中处理 `?tagging`、`?encryption`、`?website`、`?inventory`、`?requestPayment` 并返回正确的 XML
- 可交付物：
  - 桶级标签（存储、检索、序列化）
  - 新的 XML 结构体 + 序列化方法
  - `?website`、`?inventory`、`?requestPayment` 的存根错误处理
- 风险：桶级标签的模式变更与现有 JSON 序列化冲突最小。缓解：将标签存储为 `BucketConfig` 上的 JSON 列。

**阶段 5（P2—2 周）：WebSocket 实时通信**

- 里程碑：实时事件流使用 WebSocket，回退到 SSE
- 可交付物：
  - 添加 `gorilla/websocket` 依赖
  - `internal/ws/hub.go`：连接管理器
  - `internal/ws/handler.go`：升级 + 读取循环
  - 从 EdgeSource → WebSocket 的前端变更
  - 令牌过期优雅断开连接的处理
- 风险：更高的资源消耗（每个连接一个 goroutine）。缓解：实现空闲超时 + 最大连接限制。

### 风险矩阵

| 风险 | 可能性 | 影响 | 缓解 |
|------|----------|----------|-------------|
| TLS 证书轮换在生产中失败 | 低 | 高（停机） | 在 `GetCertificate` 回调中优雅降级到上次加载的证书；检测证书过期。 |
| 密钥轮换使对象无法读取 | 低 | 高（数据丢失） | 在销毁旧密钥之前严格检查 `ConfirmNoObjectsReferenceKeyID`。回滚按钮。 |
| WebSocket 连接消耗的内存 > 预期 | 中 | 中（OOM） | 最大连接数 + 写入缓冲区池。使用 `golang.org/x/net/websocket` 作为更轻量级的替代方案（如果 gorilla 太重）。 |
| 异步作业后端被批处理操作淹没 | 中 | 中（延迟增加） | 回退到限制作业入队速率的 `ErrQueueFull`（已实现）。指标用于检测潜在的拥塞。 |
| S3 客户端期望不存在的子资源返回特定错误 | 低 | 低（互操作） | 对于不支持的特性明确返回 `501 NotImplemented`。不静默忽略。 |

### 验证与测试策略

| 方向 | 测试策略 |
|----------|---------------|
| TLS | 单元测试用于配置解析 + 证书重载。集成测试使用 `httptest.NewTLSServer` 和自签名证书。 |
| 密钥管理 API | 单元测试用于并发安全的密钥轮换。集成测试用于完整的 API 循环（轮换 → 使用新密钥写入 → 使用旧密钥读取 → 撤销）。 |
| S3 子资源 | 单元测试用于 XML 序列化。集成测试使用 `minio-go` 客户端针对子资源端点。 |
| WebSocket | 单元测试用于连接管理。集成测试用于升级 + 消息交换 + 断线重连。 |
| 异步操作 | 单元测试用于作业分发和结果检索。集成测试用于完整的工作流：`202` → 轮询 → `200`。 |

### 最终建议

**在执行中最重要的架构决策是：按照以下顺序实施这些方向：`TLS → Key Management → Async Ops → S3 Sub-resources → WebSocket`。**

验证报告的替代顺序（`#1 → #3 → #5 → #4 → #2`）是合理的，我支持将密码学从 P2 提升到 P1。原因如下：

1.  **密码学与现有 SSE 基础设施的耦合是深度的、跨领域的。** `SecretProvider`、`DataKeyWrapper`、`RewrapStale`、`keyRingProvider` 已经驻留在同一个 `internal/storage` 包中。给它们一个运行时管理接口需要在包内统一重构代码——但不需要在外部进行重大的架构变更。

2.  **异步模式不是独立存在的；它是一个支持层。** 一旦密钥轮换有一个 API 端点，`POST /v1/admin/crypto/rewrap-all` 长时间运行（扫描所有对象并重新加密）将*需要*异步作业模式。所以 #5 在 #3 之后顺理成章。它们是互为条件的。

3.  **S3 子资源（#4）的独立程度最高。** 它们可以按任何顺序实施，没有耦合。这使你可以在阶段之间用它们来填充时间，而其他四个方向则按依赖顺序实施。

4.  **WebSocket（#2）仍然是最具风险的方向**，不是因为代码质量，而是因为它引入了对外部依赖（`gorilla/websocket`）、前端重构（SPA 重写）和新的状态管理（客户端维持的连接）的侵蚀。它安全地排在最后。

评审组提出的一项补充建议是：**考虑在阶段 1 之前先进行两个小时的架构清理**，用于拆分 `repository.Repository` 接口。65 个方法的接口是一个已知的债务项，不需要完整的里程碑时间表即可修复。将其拆分为 6 个有针对性接口（`ObjectRepository`、`BucketRepository` 等）将长期提高所有五个方向的可测试性和可维护性。由于每个方向都会添加新的存储库操作，最好先清理干净，以免产生更复杂的合并冲突。
