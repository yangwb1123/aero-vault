现在我已掌握项目的充分背景信息。以下是我的架构分析。

---

# AeroVault 架构深度分析报告

## 1. 架构评估

### 1.1 当前架构优势

AeroVault 的架构设计在同类项目中属于**上乘水平**。其核心优势在于：

| 维度 | 优势 | 具体体现 |
|------|------|---------|
| **分层纯度** | 协议适配器极薄，无业务逻辑泄漏 | REST/S3/WebDAV/MCP 四个入口共享同一 `FileService`，一次实现四端可见 |
| **抽象 seam 质量** | `Storage` / `Repository` / `VectorIndex` 接口设计精良 | 每个抽象背后有 2-3 个实现（local/s3/oss/cos, sqlite/postgres, brute-force/pgvector/Qdrant） |
| **增量部署能力** | 所有高级功能 Opt-in 默认关闭 | AI/pgvector/Qdrant/events/cluster/WebDAV 均 flag-gated，基线路径零依赖 |
| **事件驱动架构** | 持久化 event bus + job queue 模式 | 解耦了 FileService 与所有异步算子（indexer/antivirus/replication/webhook） |
| **多租户模型** | tenant-scoped storage key + 元数据行隔离 | 单 bucket 可服务无限租户，`*` operator key 模式优雅 |

**尤其值得称道的是**：`storageKey(tenant, bucket, key)` 的设计 —— 用 `path.Join` 做物理 key 映射，看似简单却同时实现了①租户隔离 ②桶命名空间 ③S3 flat namespace 兼容。这是"足够简单，足够正确"的典范。

### 1.2 架构债务与技术债

尽管整体架构优秀，但存在**若干系统性债务**：

#### 债务 #1：运行时单点假设（Severity: 🔴 High）

这是架构中最根本的债务——系统本质上假设单进程运行：

| 子系统 | 单点体现 | 后果 |
|--------|---------|------|
| Event bus | 64-deep 环形缓冲区，纯内存 | 事件丢失、SSE 分区（client 只能看到所在 instance 的事件） |
| In-memory BM25 | 每个 instance 独立重建 | 无法共享索引状态，N 个 replica = N 倍内存 |
| Auth registry | `map[string]Key` 纯内存 | 密钥变更丢失、不共享（虽已增加 `AUTH_PERSIST_KEYS` 缓解，但读路径仍有缓存不一致窗口） |
| Reconcile singletons | 每个 replica 都在跑 | 孤儿清理/生命周期检查相互覆盖——虽已增加 `RECONCILE_CLUSTER_SINGLETON`，但仅适用于 Postgres 部署 |

**根因**：架构从单节点起步，单点假设嵌入多个子系统。当前通过 Postgres LISTEN/NOTIFY + lease table 逐个修补，但修补的速度跟不上新增的组件。

#### 债务 #2：缺乏防护性中间件（Severity: 🟠 Medium-High）

```
当前中间件链: RequestID → CORS → Auth → Tenant → RateLimit → OTel → Recoverer → AccessLog
缺少的防护:
  - Circuit breaker        (存储后端故障 → 全量请求阻塞)
  - Request timeout        (默认 http.Client 无超时)
  - In-flight limiter      (无全局/每租户并发上限)
  - Audit middleware       (无对象级访问追踪)
  - Graceful shutdown      (无 draining 状态机)
```

**根因**：中间件链的设计遵循了"最小必要集合"原则，尚未到达"生产防护完备"阶段。

#### 债务 #3：加密半成品（Severity: 🟠 Medium）

SSE-C headers 被 S3 handler **完整解析但丢弃**。这是比缺失更危险的状态：
- 用户发送了加密密钥 → 相信数据受自己密钥保护
- 实际以服务端密钥加密存储 → 客户密钥从未使用
- 从 S3 兼容性角度，这是**静默违背语义**

同时，`envelopeEncrypter` 的加密结构不支持请求级密钥覆盖——需要重构 `PutOptions`/`GetOptions` 的密钥传递路径。

#### 债务 #4：关闭机制脆弱（Severity: 🟠 Medium）

```go
// 当前关闭逻辑的核心问题：
<-ctx.Done()
srv.Shutdown(15s timeout)  // 硬编码 15s
```

- 无 readiness 联动（K8s 滚动更新时 LB 继续路由请求到关闭中的 Pod）
- 无 in-flight 请求追踪（大文件下载被截断）
- 无 worker 排空（Job 丢失、webhook 截断）
- 无关闭钩子注册机制（新增组件无法注册关闭回调）

### 1.3 关键设计决策评估

| 决策 | 选项 | 选择 | 评价 |
|------|------|------|------|
| 协议适配 vs 协议统一 | 统一对象模型 vs 各自独立 | 统一 `FileService` | ✅ **正确**。`FileService` 的纯度是架构最稳固的基石 |
| In-process event bus vs 外部队列 | 内存 vs Redis/NATS | 内存（默认） | ⚠️ **可接受**。64-deep buffer 是合理警告线，Postgres transport 已提供升级路径 |
| Single binary vs 微服务 | 单体 vs 分布式 | 单体 | ✅ **正确**。运维简化，部署单一。微服务拆分应等流量和团队规模证明必要性 |
| SQLite default vs Postgres default | 默认嵌入式 vs 默认分布式 | SQLite | ✅ **正确**。零配置，CI gate 可复现。Postgres 升级路径清晰 |
| Opt-in AI vs 内置 AI | feature gate vs 内置 | Opt-in | ✅ **正确**。核心 CRUD 路径不受 AI 影响，即使 embedder/llm 为 nil |
| Brute-force vector search vs 外置索引 | 内存暴力 vs pgvector/Qdrant | 内存默认，外置可选 | ✅ **正确**。100K chunks 阈值合理。外置适配器 seam 干净 |

---

## 2. 扩展方向

基于上述架构评估和已有分析文档，我提出以下 **5 个架构扩展方向**。与第十期文档的重叠是刻意的——我补充了架构层面的深度分析。

### 方向 #1：生产级韧性基础设施（P0）

**为什么需要：** 当前架构缺少一套**防护性基础层**。这不是"锦上添花"而是"run 起来不崩溃"的前提。没有断路器、超时、draining，任何生产部署都会在首次流量抖动或部署时暴露。

**核心挑战：**

| 挑战 | 技术难点 | 方案权衡 |
|------|---------|---------|
| 断路器阈值设定 | 对 local/s3/oss/cos 的延迟特征差异大 | 每个 backend 独立断路配置，自动学习基线（滑动窗口 + P99） |
| In-flight 追踪与关闭协同 | 需要同时覆盖 HTTP + SSE + WebSocket + Job worker 四种连接类型 | `TrackInFlight` 中间件 + `ShutdownManager` 的状态机协作 |
| Readiness 状态机 | 需区分 Liveness（进程健康） vs Readiness（流量就绪） vs Startup（初始化完成） | Kubernetes 三种探针对映 |

**架构变更：**

```
┌─ internal/server/  (新包)
│   ShutdownManager — 生命周期状态机
│   └─ RegisterHook(name, func, priority)
│   └─ TrackInFlight() / Done()
│   └─ Shutdown(ctx)  // 多阶段：Draining → Stopping → Stopped
│
┌─ internal/resilience/  (新包)
│   CircuitBreaker — per-backend 滑动窗口断路器
│   TimeoutClient — 带 connect/read/write 超时的 http.Client
│   ConcurrencyLimiter — 加权信号量 (GET=1, PUT/DELETE=2)
│
┌─ middleware 链扩展
│   TrackInFlight → CircuitBreaker → Timeout → ConcurrencyLimit → (现有链)
```

**对现有系统的影响：**
- `internal/storage/s3.go` / `oss.go` / `cos.go` 替换 `http.DefaultClient` 为 `resilience.TimeoutClient`
- `cmd/server/main.go` 关闭逻辑替换为 `ShutdownManager`
- 零 handler 变更（所有防护在中间件 + Storage 层）

### 方向 #2：运行时多副本一致性（P0）

**为什么需要：** 债务 #1 的根源修复。当前"逐个修补"的方式（Postgres transport、lease table、persist keys）在增加第 N 个组件时仍需要相同的修补模式。需要一个**系统性的跨副本协调框架**。

**核心挑战：**

| 挑战 | 技术难点 |
|------|---------|
| 跨副本事件序 | 无法保证全局全序，只能保证每个 key 的因果序 |
| 共享索引一致性 | BM25 的 term frequency 是状态化累积，多副本写入无法合并 |
| 配置变更传播 | 密钥变更需要毫秒级传播，不是秒级 |

**架构变更：**

```go
// 新增 abstraction: internal/cluster/ 包
type Coordinator interface {
    // 领导人选举
    ElectLeader(name string) (isLeader bool, err error)
    
    // 跨副本广播
    Broadcast(event ClusterEvent) error
    Subscribe(kind ClusterEventKind, handler func(ClusterEvent))
    
    // 共享配置
    ConfigStore() ConfigStore  // TTL-cached, read-through
    
    // 分级: Standalone (默认) / Postgres / Redis / NATS / Etcd
    Kind() string
}
```

**为什么不是已有的 component 逐个修补：**
当前每个跨副本问题都有一个专项修复：Postgres transport 解决事件广播、lease table 解决 singleton、persist keys 解决密钥共享、Qdrant/pgvector 解决共享索引。但新组件（如 audit 表、circuit breaker state、rate limit state）仍需要同样模式的修复。`Coordinator` 抽象将跨副本坐标提取为独立 seam，新增组件只需注入 Coordinator。

**现有资产的复用：**
- `cluster.Singleton` 已是 `Coordinator.ElectLeader` 的雏形
- `events.PostgresTransport` 已是 `Coordinator.Broadcast` 的雏形
- `AUTH_KEY_CACHE_TTL_SECONDS` 的缓存失效模式可泛化为 `Coordinator.ConfigStore`

### 方向 #3：对象级加密语义统一（P1）

**为什么需要：** 当前加密架构有**三种交错的路由**：

```
SSE-S3 路径:  PUT → envelopeEncrypter.Encrypt → storage.Put
SSE-KMS 路径: PUT → DataKeyWrapper.Wrap → envelopeEncrypter.Encrypt → storage.Put
SSE-C 路径:   PUT → (headers discarded) → envelopeEncrypter.Encrypt → storage.Put  // 错误!
```

SSE-C 的缺失不是"新增功能"，而是**修复错误的加密路由**。同时，三种加密模式的共存需要一个统一的密钥管理层。

**架构变更：**

```
┌─ internal/storage/crypto/  (新包，替代 encrypt.go)
│   EncryptMode  enum: SSES3 | SSEKMS | SSEC
│
│   Encoder interface {
│       Encrypt(plaintext io.Reader, opts *EncryptOptions) -> 
│           ciphertext io.Reader, metadata EncryptionMeta, error
│       Decrypt(ciphertext io.Reader, meta EncryptionMeta, opts *DecryptOptions) -> 
│           plaintext io.Reader, error
│   }
│
│   Implementations:
│     envelopeEncrypter (for SSE-S3 / SSE-KMS) — 现有逻辑迁移
│     customerKeyEncrypter (for SSE-C) — 新增
│
│   EncryptionMeta struct {
│       Mode     EncryptMode
│       KeyHash  string   // SSE-C: sha256(key); SSE-S3/KMS: kid
│       Algorithm string  // "AES-GCM-256"
│   }
```

**关键设计决策：** `EncryptionMeta` 写入 Object 元数据（repository），而非存储为 sidecar。好处是：①跨协议统一（REST/S3 都读元数据决定解密路径）；②加密模式在列表响应中可见；③GC 无需读取 sidecar 即可判断加密模式。

**影响：**
- `internal/service/file_crud.go` 需要传递加密模式到 FileService
- `internal/api/s3compat/handler.go` 的 SSE-C 解析从"丢弃"改为"传递"
- `internal/storage/local_write.go` / `local_read.go` 增加 `customerKeyEncrypter` 路径
- `internal/storage/local.go` 的 `*.meta.json` sidecar 可逐步淘汰

### 方向 #4：存储引擎层架构抽象与数据面扩展（P1）

**为什么需要：** 当前 `Storage` 接口是**控制面和数据面耦合**的。`Put`/`Get`（数据面）和 `List`/`Stat`/`Presign`（控制面）在同一接口中。这导致：

1. 新增存储后端必须实现全部方法（即使某些操作有标准实现）
2. 数据迁移无法利用控制面接口（迁移需要迭代+校验，但 `Storage` 不提供分批/校验能力）
3. 存储 tiering 无法表达（当前 `Storage` 无 storage class 感知）

**架构变更：**

```
┌─ 拆分为三个独立 seam:

   1. DataPlane (数据面) — 增删改查数据路径
      Get/Put/Delete/Multipart*
      职责: 对象数据的实际读写

   2. ControlPlane (控制面) — 元数据操作 + 管理
      List/Stat/PresignGet/PresignPut
      职责: 不触及对象数据，仅操作元数据/签名

   3. AdminPlane (管理面) — 跨后端操作
      ListObjects(ctx, filter) -> []ObjectMeta  // 分批迭代
      VerifyObject(ctx, key, expectedETag) -> bool
      CopyObject(ctx, srcKey, dstKey) -> error
      职责: 迁移、校验、同步、备份

```

**为什么拆分而非保留单体接口：**

| 场景 | 当前接口 | 拆分后 |
|------|---------|--------|
| 增加 CDN backend | 需实现全部 15 个方法 | 仅需实现 DataPlane（5 个方法）+ ControlPlane 用默认 Presign 实现 |
| 数据迁移 | 需要自建迭代框架 | `AdminPlane.ListObjects` 提供标准分批 API |
| 存储 tiering | 无表达方式 | `ControlPlane.Stat` 可 extend 返回 `StorageClass` |

**注意：** 这是**重构而非新增**。现有 backend 实现三个接口的组合，内部复用当前代码。对外暴露的 `Storage` 外观模式保持现有 Consumer 代码不变。

### 方向 #5：平台契约基础设施（P2）

**为什么需要：** 当前 3 个 SDK + MCP + OpenAPI 的手动同步模式不可持续。平台扩展的瓶颈从"能否实现功能"转变为"能否以兼容方式暴露功能"。

**核心挑战：**

| 挑战 | 技术难点 |
|------|---------|
| API 版本协商 | 不仅是 header 解析，需要版本化 DTO 映射层 |
| SDK 生成 | OpenAPI 到多语言 SDK 的全自动管道 |
| Breaking change 检测 | CI 中自动检测 OpenAPI 差异并阻断 |

**架构变更：**

```
┌─ internal/api/versioning/  (新包)
│   VersionNegotiator — 从 Accept header / URL prefix 解析版本
│   VersionedResponse — 按版本选择 DTO 映射器
│
┌─ CI 管道新增:
│   openapi-diff — 对比当前分支与主分支的 OpenAPI 差异
│   sdk-contract — 对最新 server 运行 SDK 测试套件
│
┌─ SDK 生成策略:
│   短期: OpenAPI generator (openapi-generator / ogen) 
│   长期: 自定义 codegen（针对 Go/Python/JS 的 AeroVault 惯用法）
```

**为什么优先级低于上述四个方向：**
版本治理解决的是"长期可维护性"问题，而前四个方向解决的是"能否在 K8s 上正常跑"和"数据是否安全"的问题。版本治理可在功能稳定后的过渡期内从容搭建。

---

## 3. 接口设计建议

### 3.1 关键接口设计原则

基于现有架构的质量和债务，我的接口设计原则排序是：

| 优先级 | 原则 | 适用场景 |
|--------|------|---------|
| **P0** | 可测性优先 | 所有新 seam（已定义的接口必须能 mock 测试） |
| **P0** | 安全默认（Secure by default） | 加密/审计/鉴权接口 |
| **P1** | 最小 seam = 最大灵活 | `Storage` / `Repository` 的设计是良好范例，新 seam 遵循相同模式 |
| **P1** | 向下兼容 | 现有 consumer 不变，新增 seam 通过组合而非继承 |
| **P2** | 错误即类型（Errors as types） | 替代当前 string error 模式 |

### 3.2 新增抽象层

| 抽象层 | 理由 | 风险 |
|--------|------|------|
| `cluster.Coordinator` | 将跨副本协调从"逐个修补"提升为"系统性 seam" | 过度抽象风险。如果只停在 Postgres 部署，可能不需要通用 Coordinator。**缓解**：先只实现 Postgres variant，等需要 Redis/NATS 时再提取接口 |
| `resilience.CircuitBreaker` | 当前存储后端调用无防护 | 低风险。断路器是成熟模式，可参考 `gobreaker` 或 `sony/gobreaker` 但不引入依赖 |
| `audit.AuditLogger` | 将对象级审计从"散落在 FileService 各处"提升为"独立 seam" | 中风险。审计写入路径可能成为性能瓶颈。**缓解**：异步批量写入 + 独立审计存储 |
| `crypto.Encrypter` | 统一三种加密模式 | 低风险。当前 `envelopeEncrypter` 已是内部抽象，拆分 `customerKeyEncrypter` 是自然演进 |

### 3.3 向后兼容策略

| 变更类型 | 兼容策略 | 示例 |
|---------|---------|------|
| **新增 seam** | 无影响（新包、新接口、新 consumer 按需接入） | `cluster.Coordinator` / `resilience.CircuitBreaker` |
| **接口扩展** | 默认实现 + Optional interface check | `Storage` 扩展存储类字段 → 新增 `StorageClasser` 接口，`if sc, ok := store.(StorageClasser); ok { ... }` |
| **接口拆解** | 外观模式包装 + 旧接口保留 | DataPlane/ControlPlane/AdminPlane 拆解 → `Storage` 外观组合三者，consumer 代码零变更 |
| **配置迁移** | 旧配置 deprecated → 新配置优先 | 如 `STORAGE_LOCAL_SSE_KEY` → `STORAGE_CRYPTO_MASTER_KEY`，前者仍生效但 warn |

---

## 4. 技术选型

### 4.1 需要引入的新技术栈

| 新增依赖 | 用于 | 评估 | 推荐替代方案 |
|---------|------|------|-------------|
| **无额外依赖** | Circuit breaker | 可以自建（滑动窗口 + atomic 计数器，~200 行 Go） | 不引入第三方 |
| **无额外依赖** | ShutdownManager | 本质是 `sync.WaitGroup` + `atomic.Int32` + 超时控制，~300 行 | 不引入第三方 |
| **已有依赖（pgx）** | Coordinator (Postgres variant) | `pg_notify`/`LISTEN` 已实现，扩展示例 | 内部实现 |
| **无额外依赖** | Object-level audit | 独立表 + 异步写入，纯 SQL | 内部实现 |
| **`openapi-generator`（dev 依赖）** | SDK 生成 | CI 中运行，非运行时依赖 | 可选：`ogen`（Go 代码生成）或 `oapi-codegen` |
| **可选：Qdrant Go client** | Vector index | 当前已用纯 HTTP REST，无需官方 SDK | 保持当前方案 |

**核心决策：零新增运行时依赖。**

所有上述方向都可以用标准库 + 现有依赖（pgx、modernc/sqlite）实现。这不只是"节省 go.mod 行数"——它意味着：

1. **没有上游版本绑架**：不依赖第三方项目的发布节奏
2. **审计友好的代码量**：每个自建组件的代码量（200-500 行）远小于引入 SDK 的"黑盒风险"
3. **精确匹配架构**：自建组件完美适配现有 seam 和 panic/defer 模式

### 4.2 第三方依赖评估标准

| 评估维度 | 是否引入 | 阈值 |
|---------|---------|------|
| **运行时安全** | 否 | 需引入 CVE 管理流程 |
| **内部可实现** | 否 | ≤500 行纯 Go 即可实现 |
| **版本稳定性** | 是 | 仅引入 v1 稳定版 |
| **Go 标准库竞品** | 否 | 标准库能实现的绝不引入 |
| **协议对接（非业务）** | 谨慎评估 | 如 S3 SDK（aws-sdk-go-v2）已引入，OAuth2 库待评估 |

### 4.3 自建 vs 采购/引入的决策依据

| 组件 | 决策 | 理由 |
|------|------|------|
| Circuit breaker | **自建** 🏗️ | ~200 行，Go 标准库足够 |
| 关闭管理器 | **自建** 🏗️ | ~300 行，sync.WaitGroup + channels |
| 审计日志引擎 | **自建** 🏗️ | ~500 行，SQL INSERT + 异步 channel |
| SDK 生成 | **引入工具** 📦 | openapi-generator / ogen，dev 依赖 |
| SSE-C 加密 | **自建** 🏗️ | 复用现有 AES-GCM + 元数据模式字段 |
| 集群协调 | **内部实现** 🏗️ | Postgres variant 为主，Redis variant 按需 |

---

## 5. 实施路线图

### 5.1 优先级排序

```
P0 (K8s 部署准入):
  ├── Resilience infrastructure (断路器 + 超时 + In-flight 追踪) — ~2 周
  └── Graceful shutdown + readiness 状态机 — ~1 周

P1 (数据安全 + 兼容性):
  ├── SSE-C 客户加密 (修复安全错觉) — ~2 周
  ├── Object 级访问审计 (合规准入) — ~2 周
  └── Storage 接口拆解 (为迁移/tiering 铺路) — ~1.5 周

P2 (平台长期健康):
  ├── API 版本治理骨架 (版本协商 + 废弃 header + OpenAPI diff) — ~1.5 周
  ├── 跨后端数据迁移 — ~2 周
  └── Cluster.Coordinator 抽象 — ~1.5 周
```

### 5.2 阶段划分

```
Phase 1 — 安全基石 (3 周)
  Week 1:  Resilience infrastructure（断路器+超时+ConcurrencyLimiter）
           → 非侵入式 middleware，不改变任何 handler 逻辑
           → 可单独启停（flag-gated）
           → 环境要求: 零额外依赖
  Week 2:  Graceful shutdown + readiness 状态机
           → internal/server/ShutdownManager
           → /readyz 状态机 + K8s preStop hook 文档
           → SSE event: shutdown + Job worker 排空
  Week 3:  SSE-C 客户加密
           → crypto.Encrypter 统一 seam → customerKeyEncrypter
           → S3 handler 从"丢弃"改为"传递"
           → Object 元数据 EncryptionMode 字段

Phase 2 — 合规与可观测 (2 周)
  Week 4:  Object 级访问审计
           → access_audit_log 表 + AuditMiddleware + FileService 细粒度点
           → 异步批量写入 + 保留策略
  Week 5:  Storage 接口拆解 + DataPlane/ControlPlane/AdminPlane
           → 外观模式保持旧接口不变
           → AdminPlane.ListObjects 提供分批迭代

Phase 3 — 平台契约 (3 周)
  Week 6:  API 版本治理骨架
           → VersionNegotiator + DeprecationMiddleware
           → OpenAPI diff CI 检查
  Week 7:  跨后端数据迁移
           → migration table + Job implementation + admin API
  Week 8:  Cluster.Coordinator 抽象 + 现有组件迁移
           → 将 cluster.Singleton / events.PostgresTransport 纳入 Coordinator
```

### 5.3 风险点和缓解策略

| 风险 | 等级 | 缓解策略 |
|------|------|---------|
| **断路器误判** — 滑动窗口阈值设置不当导致正常流量被阻断 | 🟠 高 | ①默认不开启（需显式配置 `CIRCUIT_BREAKER_ENABLED`）；②开启后默认半开间隔 30s；③仪表板显示断路器状态 |
| **审计日志性能** — 高吞吐路径（1000 PUT/s）产生同量级审计写 | 🟠 高 | ①异步批量写入（1s 或 1000 条刷入）；②独立审计存储（配置 `AUDIT_DB_DSN` 分离）；③写失败降级（warn log，不阻断请求） |
| **SSE-C 兼容性缺口** — 与已有 SSE-S3 加密对象混合使用 | 🟡 中 | `EncryptionMode` 字段确保读取路径准确选择解密策略；版本控制测试覆盖 3 种加密模式的交叉场景 |
| **ShutdownManager 与现有组件整合** — 遗漏未注册的 goroutine | 🟡 中 | Phase 1 交付时增加 `internal/server/` 包的 goroutine 普查 CI 检查（grep 新增的 `go func` 必须在 ShutdownManager 注册） |
| **Phase 3 Coordinator 过度抽象** — 最终仅需要 Postgres variant | 🟡 中 | 接口最小化设计（3 个方法）。明确标注"目前仅 Postgres 实现可用，Redis/NATS 按需贡献" |
| **版本治理增加路由复杂度** — Accept-Version 协商导致 URL 路由逻辑膨胀 | 🟢 低 | 只做 version negotiator middleware（一个函数），不做版本化 handler 分发。版本适配在 DTO 层做，不在路由层做 |

### 5.4 里程碑定义

| 里程碑 | 时间 | 判断标准 |
|--------|------|---------|
| **M1: 生产就绪** | Phase 1 完成 | K8s 滚动更新 50 个 Pod 零中断、存储后端故障隔离不扩散、SSE-C 加解密完整测试通过 |
| **M2: 合规就绪** | Phase 2 完成 | SOC2 审计流程可追踪对象级访问、跨后端数据迁移验证通过 |
| **M3: 平台成熟** | Phase 3 完成 | OpenAPI breaking change 自动检测 CI 阻断、集群协调 seam 覆盖所有跨副本组件 |

---

## 总结

AeroVault 的架构基础非常扎实——Clean Layered Architecture、精心设计的 seam、严格的 opt-in 安全默认。但生产化道路上的瓶颈已经从"功能的广度"转向了**"在不可靠环境中的可靠性"**。

我的核心建议可以概括为三点：

1. **"堵漏"先于"盖楼"** — Phase 1 的三个方向（韧性基础设施 + 优雅关闭 + SSE-C）解决的是当前架构中真正的**生产堵点**。它们比 API 版本治理或数据迁移更有短期价值。

2. **"系统性避免"优于"逐个修补"** — `cluster.Coordinator` 抽象的存在意义不是增加新的 seam，而是**停止逐一修复每个跨副本问题的模式**。新增组件时，跨副本协调是"内置能力"而非"事后补丁"。

3. **"架构拆解"先于"功能扩展"** — Storage 接口拆解（DataPlane/ControlPlane/AdminPlane）是后续数据迁移、storage tiering、内容校验等功能的基础设施前提。在当前单体接口上叠加这些功能会导致 seam 污染。
