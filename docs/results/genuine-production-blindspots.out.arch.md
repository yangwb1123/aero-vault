Now I have comprehensive understanding of the codebase, architecture, and existing analyses. Let me produce the architectural analysis.

---

# 架构分析报告

## 1. 架构评估

### 1.1 当前架构的核心优势

AeroVault 的架构经过多轮演进已形成**清晰的层次化设计**，其优势体现在：

| 优势维度 | 具体表现 |
|---------|---------|
| **协议适配器模式** | REST、S3、WebDAV、MCP 四套协议均为薄适配器，共享同一 `FileService` 核心。写入一条数据后四协议立即可见，无协议特定的业务逻辑泄漏 |
| **存储抽象层** | `storage.Storage` 接口极小（~15 方法），local/S3/OSS/COS 四个实现均通过同一契约测试 `contract_test.go`。新增后端只需实现接口 + 注册工厂 |
| **仓库层双实现** | SQLite（纯 Go，零依赖）+ Postgres（生产级），共享 `sql.go` 公共 SQL 骨架。迁移系统双文件（sqlite + postgres），schema 版本化自动执行 |
| **事件总线** | 在进程内 pub/sub + 持久化 DB 写入。非阻塞 broadcast（channel full → drop + counter），设计上保证业务请求不被事件消费延迟影响 |
| **Opt-in 安全默认** | AI/pgvector/Qdrant/events/cluster/retention/WebDAV 全部默认关闭。`nil` embedder/llm/reranker 不破坏核心 CRUD 路径 |
| **多租户模型** | 存储 key 为 `tenant/bucket/key` 三元组，一次 List 即可穷举全量；租户隔离在存储层和仓库层同时保障 |

### 1.2 局限性（架构债务）

通过交叉验证代码库 + 既有分析文档，存在以下结构性缺口：

#### 缺口 A：单后端存储抽象 vs 存储类语义断裂（严重度：高）

`Object.StorageClass` 在 schema 迁移 `0021`、写入路径 `file_crud.go:buildPutObject`、S3 协议解析 `x-amz-storage-class` 中完整实现，但 **永不用于存储路由**。

```go
// FileService 持有一个 storage.Storage 实例
type FileService struct {
    store storage.Storage  // 单一后端，无论 storage_class 为何
    // ...
}
```

`main.go:buildStorage` 在启动时根据 `STORAGE_BACKEND` 选择唯一后端。这意味着：

- 配置 `STANDARD_IA` 的对象与 `STANDARD` 的对象在同一个磁盘/桶
- S3 协议的 `x-amz-storage-class` 被解析、存储、返回，但行为上无区别
- 无法实现成本分层（热数据本地 SSD ↔ 冷数据 S3 Glacier）

**关联分析：** v92 方向一已完整覆盖此缺口（~200 行方案），提供 `TieredRouter` + `map[string]Storage` 的路由方案 A/B。本方向不应作为独立分析重复产出，而应引用 v92 方案落地。

#### 缺口 B：WebDAV 绕过中间件链（严重度：高）

`buildDispatcher` 在 chi 路由**之前**截获 WebDAV 请求：

```go
func buildDispatcher(r *chi.Mux, davH http.Handler, cfg *config.Config) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
        if davH != nil && cfg.WebDAV.Prefix != "" {
            p := req.URL.Path
            if p == cfg.WebDAV.Prefix || strings.HasPrefix(p, cfg.WebDAV.Prefix+"/") {
                davH.ServeHTTP(w, req)  // ← 绕过 applyMiddleware 中的全部中间件
                return
            }
        }
        r.ServeHTTP(w, req)  // ← 只有非 WebDAV 请求经过中间件链
    })
}
```

**后果：** WebDAV 请求绕过 Auth、Tenant 提取、RateLimiter（全局 + AI）、OTel 记录、AccessLog、CORS。这是一个设计层面的"后门"，尤其是在多租户生产部署中构成安全风险。

#### 缺口 C：Event RequestID 被全部异步 Worker 丢弃（严重度：中-高）

`repository.Event` 结构体包含 `RequestID string`，`FileService.emit` 会正确填充 `middleware.RequestIDFrom(ctx)`。但 **Indexer、Replication、Antivirus 三个 Worker 全部丢弃此字段**：

| Worker | 入口签名 | RequestID 使用 |
|--------|---------|---------------|
| Indexer | `IndexObjectByID(ctx, objectID)` | 从 ctx 重新生成新的 trace ID，不传递原始的 `e.RequestID` |
| Replication | `ReplicateObjectByID(ctx, objectID)` | 同上 |
| Antivirus | `ScanObjectByID(ctx, objectID)` | 同上 |
| Webhook | `POST HMAC payload` | ✅ 唯一正确使用 `"request_id": e.RequestID` |

这意味着从"用户发起 PUT"到"索引完成/复制完成/扫描完成"之间的链路在观测层面完全断裂。运维人员无法回答"这个搜索结果的索引是由哪个请求触发的"。

#### 缺口 D：元数据单点故障（严重度：高）

默认 SQLite 数据库为单文件 `./var/aero.db`。损坏即全部元数据丢失。当前：

- `Storage.List` 可扫描所有 blob（key/size/ETag/LastModified）
- `storageKey(tenant, bucket, key)` 规则为 `path.Join(tenant, bucket, key)`
- 但 `VersionID`、`Tags`、`LockedUntil`、`ACL`、`Backend` 等字段**仅存于 DB**，`@v<id>` 后缀不可逆向解析为版本元数据
- 无 `recover metadata --scan` 之类的重建 CLI

#### 缺口 E：策略-动作物化鸿沟（严重度：中）

此为 v138 方向三揭示的模式：**S3 协议接受并持久化了多种策略配置，但系统从不读取这些配置来驱动对应动作**。具体投射：

| 子系统 | 配置完备度 | 执行缺失度 |
|--------|-----------|-----------|
| 桶通知 (`NotificationRule`) | ✅ CRUD 完整 | ❌ `Bus.Publish` 不读取通知规则 |
| 访问日志 (`LoggingConfig`) | ✅ 存储完整 | ❌ `WriteAccessLog` 实现存在但零调用方 |
| 存储类 (`StorageClass`)| ✅ 解析+存储 | ❌ 无服务端路由/转换 |
| Legal Hold | ✅ 设置+存储 | ❌ GET/Stat 路径不检查 |

#### 缺口 F：连接韧性不足（严重度：中）

SSE 事件流、ChatStream、MCP stdio 三个长连接通道均缺少客户端断连检测、心跳保活、慢消费者隔离机制。`bus.Subscribe` 返回无过滤全事件通道，100 个 SSE 连接 × 2000 事件/秒 = 99% 带宽浪费（在客户端过滤）。

---

## 2. 高价值架构扩展方向

基于上述缺口分析、现有文档去重验证、以及代码锚点的实际深度，推荐以下 **3 个 P0 + 2 个 P1** 方向：

### 方向 A（P0）：多后端存储编排 + 存储能力契约

**为什么需要：**

存储类语义断裂是系统架构中最根本的不一致性：配置完备但执行缺失。`StorageClass` 是用户明确指定的意图（成本、性能、耐用性），但系统将其降级为元数据标签。同时，缺少能力契约导致 `CopyObject` 在所有后端上执行 read+write（即使 S3/OSS/COS 支持服务端拷贝），List 一致性模型无差异化处理。

**核心挑战：**

| 挑战 | 难度 | 说明 |
|------|------|------|
| `FileService.store` 从单实例 → `map[string]Storage` | 中 | 需要 `TieredRouter` 在 FileService 和 Storage 之间插入 |
| 写入路径：根据 `StorageClass` + bucket 策略路由 | 低-中 | v92 方案 A 提供了清晰的 router 实现 |
| 读取路径：需知道对象在哪个后端 | 中 | 需要在 `Object` 层面记录 `Backend` 字段（已有 `repository.Object.Backend`） |
| 跨后端 List 聚合 | 高 | 多个后端的 key 空间需要分页游标统一。方案：后端级联查询 + merge sort |
| 配置面：从 `STORAGE_BACKEND` 到 `STORAGE_BACKENDS` | 中 | 需要复数配置 + 命名后端 + 存储类映射表 |
| 能力契约：`Capabilities()` 方法 | 低 | v92 方向五（~100 行方案） |
| CopyObject 自适应优化 | 低 | 检查 `CapServerSideCopy` 后选择服务端拷贝 |

**预期的架构变更：**

```
// before
FileService.store → storage.Storage (single)
// after
FileService.router → TieredRouter
  TieredRouter {
    backends  map[string]storage.Storage  // "hot", "cold", "archive"
    default   string
    mapping   map[string]string           // StorageClass → backend name
    resolver  StorageClassResolver        // bucket-level policy + StorageClass → backend
  }
```

**对现有系统的影响：**

- **向后兼容**：`STORAGE_BACKEND` 配置继续生效 → 等价于单后端 + 单存储类映射
- **现有对象**：现有 blob 的 `Backend` 字段为旧值，读取路径可以继续从 DB 记录的 `Backend` 路由
- **迁移路径**：后台 Job 扫描 `Backend != 目标后端` 的对象，跨后端 Copy+Delete

### 方向 B（P0）：异步管线请求追踪连续性

**为什么需要：**

RequestID 跨越整个中间件链（RequestID → Auth → Tenant → RateLimit → OTel → AccessLog），但在进入异步工作者后断裂。这意味着：

- 索引延迟问题无法追溯到特定用户请求
- 复制失败无法关联到源请求
- 运维人员无法构建"请求 → 事件 → 后台作业 → 完成"的全链路视图
- SLA 衡量（"PUT 后多久可搜索"）无法按租户或请求类型分解

**核心挑战：**

| 挑战 | 难度 | 说明 |
|------|------|------|
| Worker Job 结构体增加 `RequestID` | 低 | `internal/jobs/jobs.go` 的 Job 结构体 + migrations 增加字段 |
| Worker handler 从 context 提取 | 低 | 每个 Worker 的 `runOne` 注入 `ctxWithReqID` |
| Indexer: 事件 → Job 时传递 `RequestID` | 低 | `Indexer.handleEvent` 从 `e.RequestID` 填充 Job 参数 |
| 搜索/chat 响应关联 | 中 | 检索路径目前无 RequestID 记录；可在 SearchResult 增加字段 |
| 跨进程传播（Postgres 事件传输） | 低 | `events.PostgresTransport` 传递 Event 时需要包含 RequestID |

**最小变更集（每个 Worker 3-5 行）：**

```go
// Indexer/Replication/Antivirus 的 handleEvent 中：
job, _ := p.queue.Enqueue(ctx, jobType, job.Args{
    "object_id": e.ObjectID,
    "request_id": e.RequestID,  // ← 新增
})
```

**对现有系统的影响：** 零。纯新增字段，现有 Job 的 `RequestID` 为空时不影响行为。

### 方向 C（P0）：元数据灾难恢复 L1/L2/L3

**为什么需要：**

默认部署 SQLite 单文件，DB 损坏 = 全部元数据丢失。当前系统：

- 存储层保留了 blob 实体（local 文件 / S3 对象 / OSS 对象等）
- `Storage.List` 可以遍历所有 blob，返回 key/size/ETag/LastModified
- `storageKey(tenant, bucket, key)` 规则为 `path.Join(tenant, bucket, key)`，可从 key 逆向推导 tenant + bucket
- 但 `VersionID`、`Tags`、`LockedUntil`、`ACL`、`Lifecycle` 等元数据**仅存于 DB**

L1 方案（`recover metadata --scan` CLI）可以在数小时内恢复核心元数据，避免完全数据丢失。

**核心挑战：**

| 挑战 | 难度 | 说明 |
|------|------|------|
| L1: `Storage.List` + storageKey 反推 | **低** | 核心对象的 tenant/bucket/key/size/ETag 可恢复，无 version/ACL/tag |
| L1: 需要 CLI 子命令接入 | **低** | `internal/cli/` 新增 `recover.go` |
| L2: 从 `@v<id>` 后缀识别版本 | **中** | versioned blob 的 key 带 `@v<id>` 后缀，但无法判断哪个版本是 `is_latest`。需要启发式规则（写入时间最新 = is_latest）|
| L3: 加密上下文恢复 | **高** | SSE 加密对象的 envelope 存储在 `.meta.json` sidecar 中，但 key 的 id 映射依赖于 DB 记录。如果 DB 丢失但 keyfile 存在，可通过 sidecar 中的 key_id 恢复 |
| L3: 上传记录（uploads 表）不可重建 | **高** | multipart 上传的碎片在存储层无完整记录，这些上传会停留在 pending 状态但数据完整 |
| 恢复的元数据如何回写 DB | 中 | 重建流程：创建新 DB → Migrate → 逐项 Insert（需处理 ID 自增冲突） |

**L1 方案细节：**

```bash
aero-vault recover metadata --scan --tenant default
# 输出:
#   scanned: 1,245 objects
#   recovered: 1,245 objects (100%)
#   unrecoverable: 0 (acls: 1,245, tags: 0, versions: 18 — partial data)
```

**L2 方案细节：**

```
versioned_blob: acme/docs/report.pdf@v001
  → 存储为 Object{ID: ?, TenandID: "acme", Bucket: "docs", Key: "report.pdf", VersionID: "v001", IsLatest: false?}
  → 无法确定 IsLatest —— 需要对照所有版本的 LastModified 取最大者
  → version 的 ETag 可从存储层获取，但 `version_id` 字符串本身无 semantic
```

**对现有系统的影响：** 零。纯新增工具，不修改现有运行路径。L1 作为 CLI 独立运行，用户仅在需要时手动执行。

### 方向 D（P1）：策略-动作物化闭环

**为什么需要：**

这是 v138 方向三揭示的结构性模式：4 个子系统的配置 CRUD 路径完整但执行引擎缺失。闭环的优先级依影响面排序：

| 子系统 | 修复成本 | 影响面 | 建议优先级 |
|--------|---------|--------|-----------|
| 访问日志 | 低（~50 行，`WriteAccessLog` 已实现） | 合规（SOC2/HIPAA 要求的日志记录） | **P1** |
| Legal Hold GET 拦截 | 极低（~10 行） | 安全（锁定的文件不应被读取） | **P1** |
| 桶通知引擎 | 中（~500 行，事件路由） | 产品完备性（S3 通知是协议基线） | **P2** |
| StorageClass 生命周期转换 | 高（依赖多后端编排基础设施） | 成本优化 | **P3**（依赖方向 A）|

**核心挑战：**

| 挑战 | 难度 | 说明 |
|------|------|------|
| 访问日志：中间件调用 `WriteAccessLog` | **低** | 但需防止递归（访问日志写入 → 触发 `EventAccessed` → 触发写访问日志 → 无限循环） |
| Legal Hold：`file_crud.go:Get` 加检查 | **极低** | 复用 `checkCorrupt` 模式 |
| 通知引擎：`Bus.Publish` 查询通知规则 | **中** | 需要 `NotificationDestination` 接口（SQS/SNS/Lambda/webhook 适配器）+ 失败记录到 `webhook_failures` 表复用现有重试 |

**法律声明：** 本方向分析直接引用 v138 方向三的完整架构方案。独特贡献在于提出**访问日志闭环（P1 快速胜利）+ Legal Hold（P1 快速胜利）** 作为独立于完整通知引擎的独立实现单元。

### 方向 E（P1）：运维韧性成熟度

**为什么需要：**

当前的 `runServer` 关闭路径存在 5 个缺口（v138 方向一已完整分析）：

| 缺口 | 影响 | 修复成本 |
|------|------|---------|
| 无排空窗口 → 负载均衡器尚未摘除时新请求被拒绝 | 生产滚动更新时请求中断 | 低 |
| 后台 worker 不排空 → 正在索引/扫描/复制的任务被随机终止 | 数据不一致（半索引对象） | 低-中 |
| 存储后端不关闭 → 内存 buffered I/O 可能丢失最后几 KB | 数据丢失 | 低 |
| 无配置热加载 → 修改 `APP_LOG_LEVEL` 需重启进程 | 运维不便利 | 中 |
| Readiness 无语义深度 → 迁移中 / 索引器落后时仍返回 200 | 流量路由失效 | 低 |

**最小可行变更（第一阶段 2-3 天）：**

1. `sync.WaitGroup` 追踪所有后台 goroutine + `DRAINING` 状态标记
2. `/readyz?full=1` 返回组件级语义状态（DB 延迟、索引器滞后、存储延迟 P99）
3. 后台工作者 context 感知：`select case <-ctx.Done()` → 回滚未提交的 chunk 写入

---

## 3. 接口设计建议

### 3.1 关键模块接口设计原则

| 原则 | 理由 | 具体应用 |
|------|------|---------|
| **最小接口** | 每个抽象层只暴露调用方真正需要的方法 | `Storage` 接口当前约 15 方法，新增 `Capabilities()` 是合理扩展；不应把 List 的过滤/聚合逻辑推到 Storage 层 |
| **组合优于继承** | 行为扩展通过装饰器（Decorator）实现 | `CircuitBreaker` 已遵循此模式；`CompressReader`、`DualWriteStorage` 均应作为 Storage 的包装器 |
| **出错不丢上下文** | 异步路径的错误信息需要携带 RequestID 链 | Job 的 `ErrorDetails` 应包含 `OriginalRequestID` |
| **配置驱动而非代码驱动** | 新增后端/策略/规则不应改代码 | `StorageClass → Backend` 映射从配置读取，而非 switch-case |

### 3.2 是否引入新抽象层

**`TieredRouter` + `Capabilities` 抽象层（建议引入）：**

```
FileService
  → TieredRouter (new)
    → Storage (multiple instances, named)
      → Capabilities (each Storage implements)
```

**理由：** 当前 `FileService` 直接持有 `storage.Storage`，没有路由层。引入 `TieredRouter` 将存储选择从 `FileService` 中解耦，同时为后续的迁移路由（方向 C 的 `DualWriteStorage`）提供基础设施。

**注意：** 这不是新增一个层，而是将现有的 `FileService.store` 引用从"直接持有"改为"持有 Router"。Router 本身也实现 `Storage` 接口，对 FileService 透明。

### 3.3 向后兼容性策略

| 变更类型 | 兼容策略 |
|---------|---------|
| `Storage` 接口新增 `Capabilities()` | 提供默认实现返回空集；所有现有后端运行时无感知 |
| 配置从 `STORAGE_BACKEND` 到 `STORAGE_BACKENDS` | `STORAGE_BACKEND` 继续支持，内部映射为单后端配置 |
| Job 结构体新增字段 | 使用 `COALESCE` 或零值表示"历史数据无此字段" |
| 路由变更 | WebDAV 归入 chi 中间件链需先验证现有行为不变（response 格式、header） |

---

## 4. 技术选型

### 4.1 是否有必要引入新技术栈

经评估，**方向 A-E 均不需要引入新的外部依赖**：

| 方向 | 需要的技术 | Go 标准库支持 | 外部依赖建议 |
|------|-----------|--------------|-------------|
| A: 多后端路由 | 路由/配置/映射 | ✅ 完全可自建 | **不需要** |
| B: 追踪连续性 | context propagation | ✅ `context.WithValue` | **不需要** |
| C: 元数据恢复 | List + re-insert | ✅ | **不需要** |
| D: 策略-动作闭环 | Webhook 路由 | ✅ 复用已有 `events/webhook.go` | **不需要** |
| E: 运维韧性 | graceful shutdown | ✅ `sync.WaitGroup` + `context.Context` | **不需要** |

这与项目的 I6 原则（Stdlib 优先）一致。

### 4.2 存储后端能力契约是否需要 protobuf/IDL？

**不需要。** 能力集是静态枚举，Go 的 `iota` 枚举 + 位掩码或字符串切片即可。跨语言 SDK 反射能力时，可在 REST API 上暴露 `GET /v1/admin/capabilities` 返回 JSON。

### 4.3 自建 vs 采购的决策

| 场景 | 自建理由 | 何时考虑采购/集成 |
|------|---------|-----------------|
| 元数据恢复工具 | 复杂度低（L1 ~300 行 CLI），与现有 `Storage.List` 强耦合 | 需要支持 100+ 后端时考虑通用方案 |
| 通知引擎（SQS/SNS/Lambda） | S3 协议规范确定，适配器模式清晰 | 需要大规模事件驱动编排时集成 Temporal/Cadence |
| 身份联邦 | 项目已有 Auth 中间件架构，OIDC/LDAP 集成模式成熟 | 需要 SAML 或复杂 SCIM 时考虑 Keycloak/Auth0 代理 |
| 配置热加载 | 复杂度中等，但 `config.Reload()` 接口设计可渐进实现 | 需要分布式配置中心时集成 etcd/Consul，非替代方案 |

---

## 5. 实施路线图

### 5.1 优先级矩阵

```
          Importance to Production Maturity
                    Low    Medium    High
                  ┌──────┬─────────┬────────┐
Operational       │      │    E    │   C     │  P0
Complexity   Low  │      │  (ops)  │  (DR)   │
                  ├──────┼─────────┼────────┤
                  │      │   B     │   A     │  P1
            Medium│      │ (trace) │ (multi)  │
                  ├──────┼─────────┼────────┤
                  │      │         │   D     │  P1
            High  │      │         │ (policy)│
                  └──────┴─────────┴────────┘
```

| 方向 | 优先级 | 工作量估计 | 依赖关系 |
|------|--------|-----------|---------|
| A: 多后端编排 + 能力契约 | **P0** | ~15 天 | 方向 5（能力契约）是前置条件（2-3 天）|
| B: 追踪连续性 | **P0** | ~2 天 | 无 |
| C: 元数据恢复 L1 | **P0** | ~3 天 | 无（L1 独立）|
| E: 运维韧性 Phase 1 | **P1** | ~3 天 | 无 |
| D: 策略-动作闭环（访问日志 + Legal Hold） | **P1** | ~2 天 | 无 |
| D: 策略-动作闭环（通知引擎） | **P2** | ~5 天 | 无 |
| E: 运维韧性 Phase 2（配置热加载） | **P2** | ~5 天 | Phase 1 |

### 5.2 阶段划分

#### Phase 1（第一周）— 快速胜利，消除最大风险

```
Day 1-2:  方向 B（追踪连续性）
           - 每个 Worker 3-5 行：IndexObjectByID / ReplicateObjectByID / ScanObjectByID
             从 Event.RequestID 提取并注入上下文
           - Job 结构体增加 RequestID 字段 + migration
           - 验证：PUT → 查看 indexer log 包含原始 RequestID

Day 2-3:  方向 C L1（元数据恢复 CLI）
           - `aero-vault recover metadata --scan` CLI 子命令
           - 复用 Storage.List + storageKey 反推 tenant/bucket/key
           - 恢复 size/ETag/LastModified 到新 DB
           - 测试：删除 DB → 运行 recover → 验证 GET 仍可访问

Day 3-5:  方向 D Phase 1（策略-动作快速闭环）
           - Legal Hold GET 拦截（~10 行）
           - 访问日志 middleware 调用 WriteAccessLog（~50 行）
           - 测试：x-amz-object-lock-legal-hold: ON → GET 返回 423
```

#### Phase 2（第二周）— 架构核心增强

```
Day 1-3:  方向 A Phase 1（存储能力契约）
           - Storage 接口增加 Capabilities() method
           - 各后端静态声明能力（local/s3/oss/cos）
           - CopyObject 根据能力选择服务端拷贝或 client read+write
           - contract_test.go 按能力动态选择测试

Day 3-5:  方向 E Phase 1（运维韧性）
           - sync.WaitGroup 追踪后台 goroutine
           - runServer 增加排空窗口 + DRAINING 状态
           - /readyz?full=1 返回组件级语义状态
           - 后台工作者 context 感知：select case <-ctx.Done() 回滚
```

#### Phase 3（第三周）— 架构深度重构

```
Day 1-5:  方向 A Phase 2（多后端存储编排）
           - 配置从 STORAGE_BACKEND 到 STORAGE_BACKENDS
           - TieredRouter 实现（实现 storage.Storage 接口）
           - FileService 从直接持有 store 改为持有 TieredRouter
           - 写入路径：StorageClass + bucket 策略 → routing
           - 读取路径：从 Object.Backend 路由

Day 3-5:  方向 E Phase 2（配置热加载 — 可选）
           - /debug/reload 端点触发 config.Reload()
           - 优先实现 rate_limiter.Reload、log_level.Reload
           - 先验证（dry-run）再 apply
```

#### Phase 4（第四周+）— 完成与深化

```
方向 A Phase 3: 跨后端 List 聚合（merge sort）
方向 D Phase 2: 通知引擎骨架（Bus.Publish 查询 NotificationRule 路由到全局 webhook）
方向 C L2/L3: 版本对象恢复（@v<id> 后缀启发式解析）、加密上下文恢复
```

### 5.3 风险点与缓解

| 风险 | 可能性 | 影响 | 缓解策略 |
|------|-------|------|---------|
| `Capabilities()` 引入后，外部调用方期望后端能力运行时变化 | 低 | 中 | 明确文档：能力集在启动时确定，运行时不变；新增后端需重启 |
| 多后端后 List 聚合性能退化 | 中 | 高 | 后端级联查询（非并行） + 分页游标 unified marker；性能监控告警 |
| 元数据恢复产生的不完整数据被用于生产 | 中 | 高 | recover CLI 输出只读模式警告；恢复后建议进行全量 reconcile verify |
| WebDAV 归入 chi 中间件链后，依赖 WebDAV 的客户端行为变化 | 中 | 中 | URL 结构不变；response header 集变化但 WebDAV 协议规范内兼容 |
| 配置热加载中错误配置导致系统进入不一致状态 | 低 | 高 | 先 dry-run 验证再 apply；失败自动回滚旧配置 + 日志告警 |

---

## 总结

AeroVault 当前架构在"四协议共享核心"和"插件化存储"两个维度已建立良好基础，但也积累了**存储类语义断裂、异步管线观测断裂、元数据单点故障、策略-动作执行鸿沟、WebDAV 安全后门、运维韧性缺口**等六类架构债务。

**建议的执行顺序：** 

1. **追踪连续性（2 天）** — 成本最低、影响面横跨全部异步 Worker
2. **元数据恢复 L1 CLI（3 天）** — 消除单文件 SQLite 部署的核心风险
3. **策略-动作闭环 Phase 1（2 天）** — Legal Hold + 访问日志，合规基线
4. **存储能力契约（3 天）** — CopyObject 性能优化，多后端的基础设施前置依赖
5. **运维韧性 Phase 1（3 天）** — 生产滚动更新基础
6. **多后端编排 Phase 1（5 天）** — 核心架构增强，需能力契约前置
