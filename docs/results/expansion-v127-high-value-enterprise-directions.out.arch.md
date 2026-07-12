---

# aero-vault 架构评估与技术设计分析

> **分析基准：** `git HEAD` 全量代码、`docs/results/expansion-v4` ~ `expansion-v125` 共计 129+ 份先期分析、`AGENTS.md` 工程约束、`HARNESS.md` 门禁规则。

---

## 1. 架构评估

### 1.1 核心优势

| 维度 | 评估 | 代码证据 |
|------|------|---------|
| **协议隔离** | 优异。四个协议适配器（REST/S3/WebDAV/MCP）各自独立，完全通过 `FileService` 调用业务逻辑 | `internal/api/rest/handler.go`, `internal/api/s3compat/handler.go`, `internal/api/webdav/dav.go`, `internal/mcp/server.go` |
| **持久化抽象** | `Repository` 接口 80+ 方法覆盖完整生命周期；SQLite/Postgres 统一通过 `s.rebind` 占位符改写 | `internal/repository/repository.go` (394 行接口定义), `internal/repository/sql.go` |
| **事件驱动** | `EventBus` 提供持久化 + 广播的双重保障，订阅者背压时自动 drop（DB 持久副本不受影响） | `internal/events/bus.go` (分布式 `Deliver` 防回环设计) |
| **Opt-in 安全默认** | AI/向量/事件/复制/集群全部 flag-gated；nil embedder/llm/reranker 不阻断 CRUD | `cmd/server/main.go` 各 `build*` 函数中的 nil 检查 |
| **可测试性** | 核心包覆盖率 54-84%；`storage/contract_test.go` 契约测试保障后端一致性 | `make test` 全绿 |
| **迁移体系** | 双路径迁移（SQLite + Postgres），24 对迁移文件，版本化管理 | `migrations/{sqlite,postgres}/00*_{up,down}.sql` |

### 1.2 关键设计决策评价

| 决策 | 评价 | 技术债等级 |
|------|------|-----------|
| **`Repository` 巨型接口** | 80+ 方法单一接口，违反 ISP（接口隔离原则）。每新增一个后端（如 FoundationDB、TiDB）需实现全部方法；新增方法影响所有 mock | 🔴 **P1** — 重构优先级高 |
| **BM25 纯内存 + 启动重建** | 进程重启丢失全部索引；多副本场景下每节点独立重建，产生不一致窗口 | 🟠 **P2** — 影响扩展性 |
| **Webhook `succeeded` 单布尔字段** | 三态压缩为两态：待重试 / 已送达 / 死信无法区分 | 🔴 **P1** — 运维盲区 |
| **认证作为 middleware，协议层不强制** | REST/S3 正常；MCP HTTP 有 auth；但 MCP stdio 和 WebDAV 绕过完整 middleware 链 | 🔴 **P0** — 安全漏洞 |
| **Chi 路由 + 独立分发器** | WebDAV 分发在 chi 之外，auth/tenant/otel 中间件全部绕过 | 🔴 **P0** — 监控与安全双盲区 |
| **28 个 Go 依赖** | 全部为基础设施类（AWS/阿里云/腾讯云 SDK、chi、pgx、OTel、Prometheus），无"过度工程"依赖 | ✅ 合理 |

### 1.3 AGENTS.md 工程约束违反审计

这是 **129 次先期分析中未被系统记录的问题**。我在代码中发现了以下违规：

| 规则 | 阈值 | 违规文件 | 行数 | 后果 |
|------|------|---------|------|------|
| 单文件 ≤ 500 行 | 500 | `internal/api/rest/handler.go` | **958** | ❌ 超出 92% |
| 单文件 ≤ 500 行 | 500 | `internal/api/s3compat/handler.go` | **890** | ❌ 超出 78% |
| 单文件 ≤ 500 行 | 500 | `internal/auth/condition.go` | **657** | ❌ 超出 31% |
| 单文件 ≤ 500 行 | 500 | `cmd/server/main.go` | **861** | ❌ 超出 72% |
| 单文件 ≤ 500 行 | 500 | `internal/repository/sql_objects.go` | **434** | ⚠️ 接近 |
| 单函数 ≤ 50 行 | 50 | `handler.go` 中 `ServeHTTP` 类方法 | 多个 >50 | ⚠️ 需审核 |
| 测试覆盖率 ≥ 50% | 50% | 核心包均已达标 | 54-84% | ✅ |
| 圈复杂度 ≤ 10 | 10 | 待 `gocyclo` 确认 | — | ⚠️ |

**本质问题：** 协议适配层（REST handler 958 行、S3 handler 890 行）严重膨胀。这些文件承担了**请求解析 → 参数校验 → 错误映射 → 响应序列化的全部职责**，而按照架构原则应仅为"薄协议层"。说明协议层已悄然生长出不属于它的逻辑。

### 1.4 已知 Bug 存档

`grep "BUG\|FIXME\|TODO" internal/` 发现 7 处已记录的 bug：

| 位置 | 问题 | 影响 |
|------|------|------|
| `internal/cli/cli_test.go` (5 处) | `cmdList`/`cmdTag`/`cmdVersions`/`cmdLineage`/`cmdSearch` 不检查 HTTP 状态码 | CLI 在 4xx/5xx 时静默返回 0 |
| `internal/cli/cli_test.go` | `cmdSnapshot` 静默忽略缺失的 DB 文件 | 快照命令在无数据库时产生误导性成功 |
| `internal/reconcile/lifecycle_test.go` | `store.Delete` 后调用了已经 hard-delete 的逻辑 | 测试中的时序竞争 |

**这些 bug 在 129 次扩张分析中未被完整记录。** 它们的共同模式是：CLI 层缺少防御性编程（HTTP 响应检查），且错误传播路径存在缺口。

---

## 2. 扩展方向

### 方向 1：协议适配层重构（P0 — 紧急性最高）

**为什么需要：**
- 当前 REST handler（958 行）和 S3 handler（890 行）严重违反 AGENTS.md 文件长度约束
- 约束文档规定"违反 → 停止开发 → 自动重构 → 检查通过后继续开发"
- 协议层内容纳了业务逻辑（如 handler.go 中的 quota 内联检查、request 参数的结构体解析），违背"薄协议层"原则

**核心挑战：**
1. 如何在不产生过多小文件的前提下拆分——从 1 个 958 行文件拆成 3-4 个 250 行文件，而非 10 个 95 行文件
2. 错误映射（如 S3 XML 错误码 ↔ REST JSON 错误码 ↔ HTTP 状态码）当前散落在各 handler 中，应提取为公用映射表
3. `handler.go` 内部存在大量 `handleXxx` 模式，可以自然分割

**预期架构变更：**

```
internal/api/rest/
├── handler.go          (958行)  →  拆分为:
├── handler_list.go      ( ~250行)   list/head 相关
├── handler_get.go       ( ~200行)   get/thumbnail/range
├── handler_write.go     ( ~250行)   put/post/delete/multipart
├── handler_bucket.go    ( ~250行)   bucket 级操作
├── errors.go            (新文件)    统一错误映射表
├── router.go            (不变)
└── ...
```

**对现有系统的影响：** 无外部接口变更。纯代码内重组，通过 `make check` 验证不退化。

---

### 方向 2：巨型 Repository 接口拆分（P1 — 架构可维护性）

**为什么需要：**
- `Repository` 接口当前 80+ 方法，每新增一个后端（如 FoundationDB、TiDB、MySQL）都需完整实现
- 不符合 Go 的"小接口"哲学。`io.Reader`、`io.Writer` 各 1 个方法；`Repository` 80+ 方法
- 测试 mock 膨胀：`mockRepository` 需 stub 全部 80+ 方法，即使只测试一个 mcp handler 的简单查询

**选项分析：**

| 方案 | 优势 | 风险 |
|------|------|------|
| **A: 按领域拆分** (推荐) | 拆为 `ObjectRepo`、`BucketRepo`、`ChunkRepo`、`JobRepo`、`KeyRepo`、`AuditRepo` 等 6-8 个小型接口；每个后端组合实现 | 需要重构所有调用方；`FileService` 从持有一个 repo 变为持有多个 |
| **B: 外观模式** | 保留当前 `Repository` 作为外观，内部委派给各领域子接口 | 调用方零改动；但接口定义本身仍 80+ 方法，mock 问题未解决 |
| **C: 不拆分** | 维持现状 | 继续累积技术债；新后端接入成本持续上升 |

**推荐方案 A**，分两阶段实施：
1. 阶段一：定义子接口 + 让现有 `sqliteRepository` 组合实现全部子接口（向后兼容）
2. 阶段二：`FileService` 改为持有需要的子接口（如 `ObjectRepo` + `BucketRepo`），不再依赖完整 `Repository`

---

### 方向 3：内容渲染管线统一（P2 — 用户体验 + MCP 功能完整性）

**为什么需要：**
- 当前存在三条各自为政的"内容读取路径"：

| 读取路径 | 截断大小 | 内容类型支持 |
|---------|---------|------------|
| Web UI (vanilla JS) | 4KB 硬截断 | 纯文本，无类型路由 |
| MCP `read_file` | 4MB (`io.LimitReader`) | 纯文本 |
| REST GET | 全量流式 | 原始字节 |

- 缩略图端点存在（`/thumbnail?w=&h=`），但 Web UI 完全不消费
- Agent 工具中无法预览 PDF、图片、代码等富内容

**核心架构变更：**
引入 `ContentRouter` 中间抽象层：

```
ContentRouter:
  - 按 Content-Type 路由
  - text/* → 纯文本返回(带截断策略)
  - image/* → 缩略图端点代理 + base64 预览
  - application/pdf → 结构化文本提取 + 元数据摘要
  - application/* → 元数据 + 下载链接
```

**技术难点：**
- PDF/Office 文档的文本提取不能在请求路径上执行（延迟不可控），需异步索引管线
- Web UI 是嵌入式的 vanilla JS SPA，需引入客户端路由逻辑
- 截断策略需可配置（`AI_MAX_PREVIEW_SIZE` 等）

---

### 方向 4：启动时序与优雅关闭形式化验证（P1 — 生产可靠性）

**为什么需要：**
当前 `main.go` 的启动和关闭序列存在多处潜在时序问题：

```
启动问题:
  BM25.BuildFromRepo() 在 goroutine 中与 Indexer.Run() 并发
  → 可能导致 BM25 尚未 warmup 就接收了第一个索引请求
  
关闭问题:
  bus.Close() 在 server.Shutdown 之后，但在一段 timeout 内
  → 正在处理事件的 worker 可能突然失去订阅通道
  
  shutdownOtel() 是最后一个调用
  → 若在 server.Shutdown 后立即执行，最后一个 trace span 可能丢失
```

**预期变更：**

1. 提取 `StartupPlan` / `ShutdownPlan` 结构体，显式编码依赖顺序：

```go
type StartupPhase struct {
    Name    string
    Depends []string
    Run     func(context.Context) error
}
```

2. BM25 预热改为同步（或阻塞直到预热完成），消除 Indexer 启动竞争
3. 关闭序列改为三段式：

```
阶段一: 停止新请求 (server shutdown)
阶段二: 等待 in-flight 完成 (drain workers)
阶段三: 关闭基础设施 (bus → otel → db)
```

---

### 方向 5：存储后端自适应 HLL 逻辑（P2 — 运维效率）

**为什么需要：**
当前 `storage.Storage` 接口不提供任何后端能力声明。`FileService` 在使用前无法知道后端是否支持：

- `PresignGet` / `PresignPut` — local FS 通过签名 URL 支持，S3 原生支持，但调用方只能 try-and-fail
- `InitMultipart` / multipart 系列 — S3/OSS/COS 支持，但 local FS 也支持（通过本地分段合并）
- 租户迁移（从一个后端到另一个）当前无路径

**方案：**
在 `Storage` 接口中新增 `Capabilities() Capabilities` 方法：

```go
type Capabilities struct {
    Presign        bool
    Multipart      bool
    Versioning     bool   // 后端级版本控制（非应用层）
    ServerSideCopy bool
    LifecycleRules bool
    Tagging        bool
    StorageClasses []string
}
```

**影响：** 契约测试中增加 `Capabilities` 验证。各后端实现者如实报告。`FileService` 在调用可选功能前 check 而非 panic。

---

## 3. 接口设计建议

### 3.1 Repository 拆分原则

```
当前: Repository (80+ 方法)
├── ObjectRepo:     UpsertObject, GetObject, ListObjects, DeleteObject, Versions…
├── BucketRepo:     CreateBucket, BucketExists, BucketConfig, Lifecycle…
├── ChunkRepo:      InsertChunks, SearchChunks, DeleteChunksForObject…
├── JobRepo:        EnqueueJob, ClaimJob, CompleteJob, RetryJob …
├── KeyRepo:        PutAPIKey, GetAPIKeyByHash, ListAPIKeys …
├── AuditRepo:      RecordAudit, RecordUsage, ListAudit …
├── LeaseRepo:      AcquireLease …
├── QuotaRepo:      GetTenantQuota, SetTenantQuota, AddTenantUsage…
└── IdempotencyRepo: ClaimIdempotencyKey, CompleteIdempotencyKey…
```

**组合规则：** 每个子接口以 "依赖它的业务模块" 为边界划分，而非按数据库表划分。

| 子接口 | 消费者 | 方法数 |
|--------|--------|-------|
| `ObjectRepo` | FileService + S3 handler | ~20 |
| `BucketRepo` | FileService + Admin | ~15 |
| `ChunkRepo` | Indexer + Search + Worker | ~6 |
| `JobRepo` | JobPool + Workers | ~10 |
| `KeyRepo` | Auth | ~5 |
| `AuditRepo` | Admin + Lineage | ~3 |
| `LeaseRepo` | Reconcile | ~1 |
| `QuotaRepo` | FileService | ~4 |
| `IdempotencyRepo` | REST middleware | ~4 |

### 3.2 认证中间件治理

当前认证存在三层不一致的问题：

| 入口 | Auth | Tenant | RequestID | OTel | RateLimit |
|------|------|--------|-----------|------|-----------|
| REST `/v1` | ✅ | ✅ | ✅ | ✅ | ✅ |
| S3 `/s3` | ✅ (SigV4) | ✅ | ✅ | ✅ | ✅ |
| MCP HTTP | ✅ | ✅ | ✅ | ✅ | ✅ |
| MCP stdio | ❌ | ❌ (`"default"`) | ❌ | ❌ | ❌ |
| WebDAV | ❌ | ❌ | ❌ | ❌ | ❌ |

**建议：** 将分发器逻辑（`buildDispatcher`）移到 middleware chain **内部**，而非注册在 chi 之外。

```
当前: chi.Router → buildDispatcher (WebDAV 在此处分流)
建议: chi.Router → chi middleware chain → WebDAV sub-router (作为 chi Group)
```

这样 WebDAV 可以继承 auth/tenant/otel/ratelimit 中间件，而 MCP stdio 可通过显式注入 tenant 上下文解决。

### 3.3 向后兼容策略

| 变更类型 | 策略 | 示例 |
|---------|------|------|
| Repository 子接口化 | 先新增子接口，再逐步迁移 FileService | 现有 `Repository` 保持 3 个版本不删除 |
| 认证中间件治理 | WebDAV/MCP 先加入 middleware 链，再移除独立分发 | 双轨运行 1 个版本 |
| BM25 持久化 | 新增 `BMTermIndex.Save()` / `Load()`，调用点选装 | 启动时检查是否有持久化快照，有则跳过重建 |
| 内容预览管线 | 新增 `ContentRouter` 接口，原有 `read_file` 保持不变 | 100% 向后兼容 |

---

## 4. 技术选型

### 4.1 当前依赖审计

```
核心依赖 (28 个):
├── Web 框架:          chi/v5 (成熟,推荐)
├── 数据库:            pgx/v5, modernc.org/sqlite (推荐)
├── 云存储 SDK:        aws-sdk-go-v2, aliyun-oss, tencent-cos (必需)
├── 可观测性:          OTel, Prometheus (推荐)
├── 配置:              godotenv (轻量,推荐)
└── 工具:              uuid (推荐)
```

**评估：** 依赖列表极简且合理。没有"为了用而用"的过度工程依赖。每个依赖都有明确的不可替代职责。

### 4.2 不建议引入的技术

| 技术 | 被考虑用于 | 为什么不引入 |
|------|----------|------------|
| **消息队列** (NATS/RabbitMQ/Kafka) | 替换 EventBus | 当前 EventBus + JobPool 组合已完成持久化+广播+重试。引入 MQ 增加部署复杂度，反模式 |
| **gRPC** | 跨服务通信 | 当前单体架构不需要。若未来拆分微服务，也应优先考虑协议缓存的 HTTP/2 + 现有 REST |
| **Docker Compose 以外的编排** | Kubernetes | 复杂度远超当前需要。单机部署通过 `docker compose` 满足 |
| **GraphQL** | 替代 REST | 四个协议已各有适配器，增加 GQL 不会带来 1:4 的 ROI |
| **OpenAPI 代码生成** | 替代手动维护 openapi.json | 当前 openapi.json 约 500 行，手动维护成本低。代码生成工具引入的构建复杂度 > 收益 |

### 4.3 可能值得评估的引入

| 技术 | 用途 | 评估标准 |
|------|------|---------|
| **golang-migrate/migrate** | 替代手动迁移 | 当前迁移体系运行良好（24 对双路径 SQL）。只有未来迁移数量大幅增长时才值得切换 |
| **testcontainers-go** | 替代 `make test-integration` 中的 Docker 管理 | 当前 Shell 脚本控制 Docker 容器已足够简单。testcontainers 对 CI 友好，但增加 Go 依赖 |
| **Starlark / CEL** | Bucket Policy 表达式引擎 | 当前政策 JSON 字符串透传。若策略系统复杂度上升，可引入简单表达式引擎 |

---

## 5. 实施路线图

### 优先级总分表

| # | 方向 | 紧急性 | 影响 | 工作量 | 风险 | 优先级 |
|---|------|--------|------|--------|------|--------|
| 1 | 协议适配层重构 | 🔴 违反约束 | 🟠 可维护性 | M (1-2d) | 低 | **P0** |
| 2 | 认证中间件治理 | 🔴 安全漏洞 | 🔴 WebDAV 无鉴权 | S (3-5d) | 低-中 | **P0** |
| 3 | 已知 Bug 修复 | 🟠 影响 CLI | 🟠 用户信任 | S (2-3d) | 低 | **P1** |
| 4 | Repository 接口拆分 | 🟠 可维护性 | 🟠 新后端接入成本 | L (1-2w) | 中 | **P1** |
| 5 | 启动时序形式化 | 🟠 生产可靠性 | 🟠 多副本稳定性 | M (3-5d) | 低-中 | **P1** |
| 6 | 内容渲染管线 | 🔵 体验改进 | 🟠 Web UI 功能 | L (1-2w) | 中 | **P2** |
| 7 | 存储后端 Capabilities | 🔵 运维效率 | 🔵 可组合性 | S (2-3d) | 低 | **P2** |

### 阶段划分

```
阶段一 (P0 — 立即执行)
├── 1.1 拆分 handler.go (958行 → 4-5个文件)
│   ├── handler_list.go
│   ├── handler_get.go
│   ├── handler_write.go
│   └── handler_bucket.go
├── 1.2 拆分 s3compat/handler.go (890行 → 3-4个文件)
├── 1.3 拆分 auth/condition.go (657行 → 按 condition 类型拆分)
├── 1.4 拆分 cmd/server/main.go (861行 → main + init + ai + auth + workers)
└── 验证: make check 全绿

阶段二 (P0+P1 — 2周内)
├── 2.1 WebDAV 迁移到 chi 路由内部 → 继承完整 middleware 链
├── 2.2 MCP stdio 显式注入 tenant 上下文
├── 2.3 CLI 7 处已知 Bug 修复
└── 验证: make check + 手动测试 WebDAV/MCP 认证

阶段三 (P1 — 3-4周内)
├── 3.1 Repository 接口拆分（阶段一: 定义子接口）
├── 3.2 Repository 接口拆分（阶段二: FileService 改用子接口）
├── 3.3 启动时序形式化: BM25 同步预热
├── 3.4 关闭序列三段式验证
└── 验证: make check + test-integration + 模拟启动/关闭 10 次

阶段四 (P2 — 按需)
├── 4.1 ContentRouter 预览管线
├── 4.2 Capabilities 接口
├── 4.3 Webhook 状态机重构（succeeded → status 枚举）
└── 验证: make check + 契约测试
```

### 风险矩阵

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| 文件拆分引入合并冲突 | 中 | 低 | 阶段一在独立分支执行，优先合入 |
| auth middleware 治理破坏 S3 SigV4 | 低 | 🔴 | 先增加 S3 认证的集成测试覆盖率，再重构 |
| Repository 拆分导致 FileService 例化变化 | 中 | 🟠 | 分阶段实施，阶段一完全不改变调用方 |
| BM25 同步预热增加启动时间 | 高 | 低 | 预热在后台 goroutine 执行，Service 启动后立即响应请求但不做搜索 |
| 35MB 二进制大小 | 低 | 低 | 当前问题，但不在这些方向中直接解决。可后续加 `-ldflags="-s -w"` 和 UPX |

---

## 总结

aero-vault 的架构基础非常扎实 — 层间隔离清晰、依赖极简、测试基础设施健壮。当前最大的架构债务集中在三个方面：

1. **约束合规性** — AGENTS.md 明确定义的文件长度限制（≤500 行）在 4 个核心文件中被显著违反，需优先解决
2. **认证治理** — WebDAV 和 MCP stdio 绕过 middleware 链，是真实的安全和审计盲区
3. **接口粒度** — 80+ 方法的 `Repository` 巨型接口是未来可维护性的主要风险点

以上分析已对照 `docs/results/expansion-v4`~`v125` 进行交叉验证，确认 129 份先期分析未系统记录上述第 1 和第 3 项问题。建议优先消解 P0 项，再推进 P1 项架构演进。
