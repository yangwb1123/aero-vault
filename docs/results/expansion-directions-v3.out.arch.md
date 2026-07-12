# 架构分析报告：Aero-Vault

## 1. 架构评估

### 1.1 整体架构评估：成功但有裂痕的三层模型

当前系统采用经典的三层抽象：**Protocol Adapters → FileService → (Storage + Repository)**。这一模型在单体起步期是合理选择，但代码验证揭示了一系列结构性张力。

| 维度 | 评估 | 关键证据 |
|------|------|---------|
| **模块化** | 良好分层，但边界已出现松动 | Handler 禁止直连 Storage 的规则未被强制实施 |
| **可扩展性** | 存储后端支持 S3/OSS/COS/Local，Repository 支持 SQLite/Postgres | 无 Storage 健康检查接口，存储层的抽象不足以覆盖生命周期管理 |
| **可测试性** | 支持 Mock LLM、SQLite 内存、临时目录 | CI 基线（SQLite+Local FS+零网络）设计精良 |
| **健壮性** | 事件总线 channel 全满时静默丢弃 | EventBus 无背压或无持久化重试 — 生产环境隐患 |
| **安全性** | AES-256-GCM 加密、JWT、SigV4、Key/Scope 体系 | 桶级加密策略完全缺失，加密策略与存储层解耦不够 |
| **运维能力** | 指标、告警、Grafana 面板齐全 | 桶级访问日志占位实现、无健康检查写入路径 |

### 1.2 关键设计决策评价

| 决策 | 评价 | 是否需重新评估 |
|------|------|--------------|
| EventBus + channel 广播 + `default: drop` | **早期阶段可接受，但达到一定规模后必须重写** | 是 — 建议改为持久化 WAL + 消费者组 |
| FileService 作为唯一核心控制器 | **正确决策** — 防止协议层膨胀，所有跨层操作协调点在 Service | 否 — 保留 |
| AI 管线全量 opt-in + nil 安全 | **正确决策** — 不影响基线 CI gate | 否 — 保留 |
| `$N` 占位符改写为 `?` 机制 | **必要但脆弱的抽象** — Postgres 用 `$N` 位置的特性与 SQLite `?` 的不兼容性，通过 `s.rebind` 转换。但 I1 规则表明这是容易出错的模式 | 需工具化（lint 规则）或考虑改用 `?` 统一的 ORM 包装层 |
| Handler 不自挂 Middleware 链 | **设计上合理**（隔离测试可跳过 auth/tenant），**但风险在于遗漏 auth 校验** | 需要有自动检测：若 handler 在非测试调用链中缺少 auth 则失败 |
| Migration 双文件 | **正确但昂贵** — 每次 schema 变更需维护 4 个 SQL 文件 | 建议增加 migration 代码生成器 |
| `ChunkCleaner` 失败不阻断硬删除 | **务实** — 索引数据不应阻止已有主数据删除 | 保留 |
| 桶级配置（versioning/lock/lifecycle/logging/notification）写入 DB 但部分无消费端 | **架构半成品** — S3 兼容 API 的表面积只实现了一半 | 需要明确的 feature gate 或急须补全 |

### 1.3 架构债务（Architecture Debt）

按严重程度排序：

1. **🔴 EventBus 无持久化** — `broadcast` 函数 `default: // drop` 意味着任何生产事件都被视为尽力而为。对于需要出站 Webhook、复制、反病毒扫描的存储系统，这是不可接受的。事件丢失后没有任何补偿机制。
2. **🔴 桶级配置写入了但未消费** — bucket 的 LoggingConfig、NotificationRule 完整地存储了，但对应的消费端（访问日志写入器、事件投递引擎）完全缺失。这是最典型的**半拉子架构**：实现了存储接口，跳过了核心业务逻辑。
3. **🔴 Storage 抽象缺乏操作接口** — `Storage` 接口假设所有后端提供相同的操作。但生命周期管理需要 `Restore`（从冷存储取回）、`Transition`（在层级间移动）、`ColdStorageClass` 等。当前接口不足以支撑 #1 方向。
4. **🟠 桶删除不安全** — `DeleteBucket` 先检查 `BucketStats` 再删除对象，但非原子操作。在高并发下可能漏删。
5. **🟠 `DeleteFolder` 内存模型** — `allKeys := []string{}` 无分页/流式，大目录必然 OOM。
6. **🟠 加密策略不可变且无桶级** — 加密是全局启动参数，无法按桶或按租户配置。ROT 仅启动时触发，无定期策略。

---

## 2. 高价值扩展方向

基于验证结果，我推荐以下优先顺序（与技术文档的原始建议一致，但增加了一个关键基础设施方向）：

### 方向 A（P0）：事件持久化 + 投递引擎

**为什么需要：**
- 当前 EventBus 的 `drop` 语义意味着跨区复制、Webhook 通知、AV 扫描等所有依赖事件的特性在生产中都不可靠
- S3 事件通知是 S3 兼容的核心特性 — AWS S3 用户将事件通知视为基本功能，而非增值功能
- 这是 #3（事件通知）和 #4（访问日志）共同的底层基础设施

**核心挑战：**
- 需要从内存 channel 迁移到持久化 WAL（如 NATS、Kafka 或 SQLite-based WAL）
- 需要定义 `at-least-once` 投递语义，同时引入去重机制应对重试
- 需要优雅地处理消费者崩溃和重启后的重播
- 架构必须与租户隔离、速率限制兼容

**预期架构变更：**
```
当前: handler → EventBus.broadcast → channel → consumer (drop on full)
目标: handler → EventBus.Enqueue → WAL(持久化) → Dispatcher → consumer groups
        └─ NotificationMatcher → 按 NotificationRule 过滤 → HTTP POST
        └─ AccessLogWriter → 按 LoggingConfig 路由 → 目标桶
        └─ ReplicationWorker → 跨区副本
```

| 组件 | 变更类型 | 影响范围 |
|------|---------|---------|
| `EventBus` 接口 | 破坏性变更 — 从同步发到持久化队列 | 所有 producer/consumer |
| `main.go` 装配 | 新增 — 需初始化 WAL backend | `main.go` |
| `NotificationRule` 结构 | 无需变更 | — |
| 当前 handler 调用 | 兼容 — 仅改调用底层 | 已在 bus.go 中注册 |

**对现有系统的影响：**
- 所有现有 consumer（webhook、replication、av）需要新 consumer interface
- `EventBus` 的 `Publish` 方法从 O(1) channel 写入变为 O(1) WAL append，延迟略增但可靠性大幅提升
- 监控指标需增加：队列深度、投递延迟、重试次数、死信队列计数

**选项讨论：**

| 方案 | 优点 | 缺点 |
|------|------|------|
| SQLite-backed WAL（单节点） | 零依赖、复用现有 DB 引擎、迁移路径短 | 性能上限 ~5K msg/s、无法水平扩展 |
| NATS JetStream | 原生 Go、轻量、持久化、去重特性、水平扩展 | 增加运维依赖、集群需额外部署 |
| 嵌入 Kafka（Redpanda） | 功能最完备、生态丰富 | 资源占用大、运维复杂度高、对单体来说过于重量级 |

**建议：** 优先选 SQLite WAL（配合当前 SQLite 默认基线），暴露 `EVENT_BUS_BACKEND` 配置项，为日后平滑切换到 NATS 预留接口抽象。这是与当前架构哲学（默认零外部依赖）最一致的做法。

---

### 方向 B（P0.5）：分层存储 / 生命周期转换

**为什么需要：**
- 成本架构是存储系统最核心的竞争力之一。无生命周期管理意味着所有对象必须停留在同一存储层，对于冷数据存储成本不可控。
- AWS S3 的生命周期策略是 S3 最广泛使用的功能之一 — 用户的文件越多，成本敏感度越高。
- 当前 `Object.StorageClass` 字段已存在但从未被使用（audit 确认），说明这是预留接口但未完成。

**核心挑战：**
- `Storage` 接口需要扩展以支持 `Transition`（在当前后端层级间移动对象）和 `Restore`（从归档层取回）
- 跨存储后端转换（如 S3→Local）需要网络传输和一致性保证
- 需要定义 `TransitionRule` 的 DSL（何时、从何、到何）
- 对象转换期间需要处理并发读写冲突
- 需要可配置的恢复延迟（`RestoreInProgress` 状态 + 恢复完成通知）

**预期架构变更：**
```
┌──────────────────────────────────┐
│  LifecycleEngine (reconcile)      │  ← 新增
│  ┌─ 按 bucket 读取 LifecycleRule  │
│  ├─ 扫描旧对象（分页/流式）       │
│  ├─ 执行 Transition(object)       │
│  │   └─ Storage.Transition(source,dest,class) → copy + delete + metadata update
│  ├─ 执行 Expiration(object)       │
│  │   └─ 保留已有的 soft_delete/hard_delete
│  └─ 输出 telemetry                │
└──────────────────────────────────┘
```

| 组件 | 变更类型 | 影响范围 |
|------|---------|---------|
| `Storage` 接口 | 增加 `Transition`, `Restore`, `ColdStorageClass` | 所有 backend（local/s3/oss/cos） |
| `StorageClass` 枚举 | 扩展：STANDARD, INFREQUENT_ACCESS, ARCHIVE, DEEP_ARCHIVE | 全局引用 `Object.StorageClass` 的地方 |
| `LifecycleRule` 结构 | 扩展：增加 `Transition []TransitionRule` | `LifecycleConfig` 可能已支持 |
| `reconcile/lifecycle.go` | 从仅 clean 变为 clean + transition | reconcile worker 逻辑 |
| `FileService` | 新增 `RestoreObject` 方法，改动 `GetObject` 以检查状态 | service 层 |

**对现有系统的影响：**
- 大：所有 Storage Backend 需要实现新接口。自建 Local 和 S3 适配器相对容易，OSS/COS 需要调研各自 API 是否支持分层。
- 中：`GetObject` 需要检查对象是否是 `RestoreInProgress` 或已过期（归档中）。
- 小：REST/S3 handler 需要新增 `POST /restore` 端点（S3 兼容的 `POST ?restore`）。

**选项讨论：**

| 策略 | 优点 | 缺点 |
|------|------|------|
| 单存储后端内分层 | 实现简单、无网络传输、无跨后端一致性 | 仅限于后端自身层级（如 S3 Standard→S3 Glacier） |
| 跨存储后端动态转换 | 灵活性最大、成本最优 | 实现复杂、需要数据搬迁、有网络成本和时间延迟 |

**建议：** 分两步走。第一步：在 reconcile worker 中支持**单后端内**的生命周期转换（例如 S3 Standard→S3 Glacier 由 AWS 侧处理，Local 后端则存文件系统属性）。第二步：引入 `StorageRouter` 抽象来支持跨后端转换。这在 P1 阶段完成。

---

### 方向 C（P1）：桶级加密策略 + Key Management Service

**为什么需要：**
- 安全合规是政企客户的硬性要求。当前全局 AES-256-GCM 行为不可按桶或租户配置。
- 企业需要场景：Bucket A 使用 AWS KMS 的 CMK，Bucket B 使用本地 HSM，Bucket C 不加密（开发环境）。
- 当前 `BucketConfig` 中无 `DefaultEncryption`，这是桶级差异化的基础字段。

**核心挑战：**
- 加密策略的存储与执行解耦：策略记录在哪里（DB？），实际加密在哪里执行（Storage 层？）
- 策略变更后已有对象的处理：是否要重新加密？是否支持？
- KMS 的集成需要定义 provider 接口（AWS KMS / GCP Cloud KMS / Azure Key Vault / HashiCorp Vault / local keyfile）
- 加密上下文的传递链路：handler → FileService → Storage 需要传递加密配置

**预期架构变更：**

```
┌─────────────────────────────────┐
│  EncryptionStrategyResolver     │  ← 新增
│  ┌─ BucketDefaultEncryption     │
│  ├─ TenantDefaultEncryption     │
│  └─ GlobalDefault               │
└──────────┬──────────────────────┘
           │
    ┌──────▼──────┐
    │ Encryptor    │  ← 重构：当前是全局单例
    │ ┌─ Provider  │    变为按请求实例化
    │ ├─ AesGCM    │
    │ └─ KMS       │
    └──────┬──────┘
           │
    ┌──────▼──────┐
    │ Storage SSE  │  ← 写入/读取时传入 KeyID
    └─────────────┘
```

| 组件 | 变更类型 | 影响范围 |
|------|---------|---------|
| `BucketConfig` | 增加 `DefaultEncryption *BucketEncryption` | 表迁移 + repository 方法新增 |
| `encrypt.go` | 重构为 `Encryptor` 接口 + 按需实例化 | 所有加密路径 |
| `FileService` | 增加加密策略解析 → 传递到 Storage | 写入/读取路径 |
| REST API | 新增 `PUT /v1/admin/buckets/{name}/encryption`, `DELETE`, `GET` | router + handler + OpenAPI |
| KMS Provider 接口 | 新增 — 支持模拟/AWS/Vault/Local | `internal/crypto/kms/` |

**对现有系统的影响：**
- 中：当前 `encrypt.go` 是单例模块级变量。重构为可注入接口影响所有调用链。
- 极小：桶级策略是可选的 — 若 `BucketConfig.DefaultEncryption == nil`，回退到全局配置。向后兼容。
- 新端点需 scope 授权，已有 `admin` scope 可以复用。

**建议：** 增量实现。先改 `BucketConfig` 加字段（schema migration），再实现 `EncryptionStrategyResolver`，最后暴露 API。当前 `encrypt.go` 的 KMS 集成可以复用。

---

### 方向 D（P1）：存储适配器健康检查 + 自动化故障转移

**为什么需要：**
- 当前 `readyzHandler` 仅 `Stat("@healthz/probe")`，不能验证写入能力。生产环境需要存储后端健康探测。
- 多后端配置时（主 S3、备 Local），健康检查是自动故障转移的前提。
- 这是生产就绪度的关键差距（audit 已指出）。

**核心挑战：**
- 健康检查必须与后端真实行为一致（S3 可能返回 200 但实际无法写入）
- 故障转移的粒度：全桶级？租户级？对象级？
- 故障转移后的恢复：何时切换回？如何保证数据一致性？

**架构设计方向：**

```
Storage 接口扩展:
  Health(ctx) *StorageHealth  // 新增接口方法
  └─ type StorageHealth struct {
      Writable bool
      Latency  time.Duration
      LastCheck time.Time
      Details   map[string]any
  }

HealthMonitor (新增协程):
  每 N 秒对所有注册的 Storage Backend 执行 Health()
  暴露 StorageHealthGauge (0/1) 到 OTel Metrics
  故障时触发 EventBus 事件 (storage.unhealthy / storage.recovered)

StorageRouter (可选):
  根据健康状态分配请求
  路由策略: primary-only / failover / weighted
```

---

### 方向 E（P2）：企业级 Web Admin Dashboard

**为什么需要：**
- 当前 Web UI 是面向最终用户的 4-tab SPA。缺少管理员面板意味着所有管理操作依赖 REST API 或 CLI。
- 审计日志查看、作业管理、租户配额管理、系统监控面板等需要图形化界面。

**核心挑战：**
- 前后端分离需要定义清晰的 Admin API 边界。已有 REST API 可以复用，但需要枚举哪些 admin 端点暴露给 Web UI。
- 权限治理：Web UI 需要自己的 session 管理，不能完全依赖 Bearer Token（用户浏览器安全）。
- 不需要从零开始：现有 OpenAPI 定义可作为 UI 自动生成的 Schema。

**架构建议：**
- 增量式开发，先做一个只读的管理面板（审计日志、作业列表、指标展示），再做可写功能
- Web UI 与 Admin UX 分离：`/ui` 目录加子路由 `/ui/admin`
- 利用现有 Grafana 面板做嵌入或链接，避免重复实现监控图
- session 管理用简单 cookie + CSRF token，不要引入 OAuth 2.0 重量级方案

---

## 3. 接口设计建议

### 3.1 关键抽象层接口设计原则

**当前的问题：** 接口定义过于粗粒度。`Storage` 接口假设 `Put(ctx, key, reader, opts) → error` 适用于所有情况，但冷存储和普通存储的写入语义完全不同。

**建议引入的新接口或接口扩展：**

```
// 核心 Storage 接口 — 扩展后
type Storage interface {
    // 已有的
    Get(ctx, key, opts) (io.ReadCloser, error)
    Put(ctx, key, reader, opts) error
    Delete(ctx, key) error
    Stat(ctx, key) (ObjectInfo, error)
    List(ctx, prefix, opts) ([]ObjectInfo, error)

    // 新增 — 生命周期支持
    Transition(ctx, sourceKey, destKey, StorageClass) error
    Restore(ctx, key, days int) error
    ColdStorageClass() []StorageClass  // 返回该后端支持的分层

    // 新增 — 运维支持
    Health(ctx) (*StorageHealth, error)
}
```

**关于 Repository 接口：** 当前接口定义合理。关注点应在 Repository 方法命名一致性上。建议建立 `Repository` 接口的契约测试（类似 `storage/contract_test.go`），确保 Postgres 和 SQLite 的行为一致。

### 3.2 是否需要新的抽象层

**是，需要以下新的抽象层：**

| 抽象层 | 理由 | 紧急程度 |
|--------|------|---------|
| `EventBus` → `EventStore` / `EventDispatcher` | 当前 `broadcast-drop` 不可接受，需要 WAL + 消费者组 | P0 — 立即 |
| `EncryptionStrategyResolver` | 解耦加密策略的决定与执行，支持桶级和租户级策略 | P1 — 方向 C 的前提 |
| `StorageRouter` | 支持跨后端生命周期转换和健康故障转移 | P2 — 可选，方向 B 的第二期 |
| `NotificationRuleMatcher` | 将事件匹配和投递逻辑从 EventBus 中分离出来，使事件投递可测试 | P0 — 方向 A 的子任务 |

### 3.3 向后兼容性策略

1. **EventBus 接口破坏性变更**：需要版本化接口。定义 `EventBusV1`（当前 `Publish(ctx, ...)`) 作为 shim 包装新 `EventStore`。所有现有 handler 保持不变，内部调用新实现。
2. **Storage 接口扩展**：用 Go 接口的 zero-value 兼容模式。新方法加默认行为（如 `Transition` 返回 `ErrNotImplemented`）。运行期检查：`if st, ok := storage.(Transitionable); ok { ... }`。不要一次性改所有 Backend。
3. **BucketConfig 字段增加**：zero-value = nil/空 = 兼容当前行为（全局默认）。
4. **数据库迁移**：遵守 I2 规则：双文件 + 不可逆变 + 启动时自动执行。

---

## 4. 技术选型

### 4.1 是否需要引入新框架/库

| 场景 | 推荐方案 | 理由 | 风险 |
|------|---------|------|------|
| 事件持久化 | **SQLite WAL 自建 / NATS JetStream** | Go 生态成熟，NATS 是 Cloud Native 生态主流选择 | NATS 增加外部依赖 |
| 桶级加密 KMS | **Go stdlib crypto + AWS SDK for KMS (可选)** | 不需引入新框架；KMS 集成只需 HTTP/gRPC 调用 | — |
| Web Admin UI | **Vue 3 / React w/ TypeScript** 或 **htmx + Go templates** | 选择取决于团队前端能力。HTMX 更轻量，React 更灵活 | 增加前端技能要求 |
| 生命周期调度 | **现有 reconcile 框架 + cron 调度** | 不需要新框架；复用现有 `RECONCILE_INTERVAL_MINUTES` | 更精确调度需增强 |
| 多后端 StorageRouter | **Go interface + registry pattern** | 不需要第三方 | — |
| 配置管理 | 当前 viper 满足，**不需更换** | — | — |

### 4.2 自建 vs 采购/集成

| 决策 | 建议 | 依据 |
|------|------|------|
| 事件系统 | **自建**（基于 SQLite WAL，接口抽象，预留 NATS 适配） | 系统复杂度可控，不是核心差异化；WAL 设计本身就是存储系统的核心能力 |
| KMS 集成 | **适配现有 KMS**（AWS KMS / Vault / Local keyfile） | 不应自建 KMS；只需定义 Provider 接口，按需实现 |
| 加密算法 | **使用 Go stdlib crypto/aes + crypto/cipher** | 不做自定义加密算法，这是安全原则 |
| 前端框架 | **推荐 htmx + Go templates**（如果团队 Go 为主的话） | 减少前后端分离的通信开销；Web Admin 是内部工具，不是面向终端的 SPA |

### 4.3 判断新依赖的标准

```
每个新 go.mod 依赖必须回答：
1. 能不能用 stdlib 实现？（I6 优先）
2. 有没有具体、不可简化的功能需求？
3. 维护者活跃度？社区采用率？
4. 是否能够在不引入全局状态或 init() 函数的情况下使用？
5. 是否有现成的抽象层可以隔离依赖？如果不能，是否能水平扩展？
```

---

## 5. 实施路线图

### 总体原则

- **P0：** 基础设施补齐（事件持久化）— 是 #3、#4、跨区复制、AV 扫描的共同基石
- **P0.5：** 成本架构（生命周期）— 最复杂的架构变更，需要最多设计时间
- **P1：** 安全合规（桶级加密）— 中等复杂度，清晰的设计模式
- **P1：** 运维就绪（存储健康检查）— 低风险、高回报
- **P2：** 产品体验（Web Admin Dashboard）— 非核心、可推迟

### 分阶段实施计划

#### 阶段 1（Sprint 1-2）：事件持久化 + 投递引擎 ⬆️ P0

| 任务 | 产出 | 预计 |
|------|------|------|
| 定义 `EventStore` 接口（WAL 抽象） | `internal/eventbus/store.go` — 接口 + 内存实现（测试用）+ SQLite 实现（生产） | 2-3 天 |
| 重构 `EventBus` 使用 `EventStore` | 改 `bus.go`；所有现有 consumer 迁移 | 1-2 天 |
| 实现 `NotificationRuleMatcher` + 投递引擎 | 读取 `NotificationRule` + 事件过滤 + HTTP POST（含重试、HMAC） | 3-4 天 |
| 实现 `AccessLogWriter` | 读取 `LoggingConfig` + 写入目标桶文件 | 2-3 天 |
| 集成测试 + 文档 | CI 覆盖持久化事件路径 | 1 天 |

**风险：** 改造 EventBus 是破坏性变更，需确保所有 consumer handler 并行迁移。缓解：提供 `PublishV1` shim。

**里程碑 M1：** EventBus 不再丢事件。桶级通知和访问日志可工作。

#### 阶段 2（Sprint 3-4）：生命周期转换 ⬆️ P0.5

| 任务 | 产出 | 预计 |
|------|------|------|
| 设计 `StorageClass` 枚举 + 转换规则模型 | `internal/service/lifecycle.go` — 数据结构 | 1 天 |
| 扩展 `Storage` 接口（`Transition`, `Restore`, `ColdStorageClass`) | 接口扩展 + 现有 backend 的 `ErrNotImplemented` | 1 天 |
| 实现 Local 后端的分层（基于文件系统目录或属性） | local storage 扩展 | 2 天 |
| 实现 S3 后端分层（S3 Standard → S3 Glacier 等） | s3 storage 扩展 | 2 天 |
| 改 `reconcile/lifecycle.go` 添加 Transition 逻辑 | lifecycle 引擎 | 3 天 |
| 新增 `POST /restore` 端点（S3 + REST） | handler + service | 2 天 |
| 集成测试（多后端、转换后读、恢复中状态） | 完整测试套件 | 2 天 |

**风险：** 跨后端转换（如 S3→Local）实现复杂，建议 P1 阶段再引入。缓解：第一期只做单后端内转换。

**里程碑 M2：** 生命周期策略可执行：90 天后自动将对象转换为低频，365 天后转到归档。

#### 阶段 3（Sprint 5-6）：桶级加密策略 + 存储健康检查 ⬆️ P1

| 任务 | 产出 | 预计 |
|------|------|------|
| `BucketConfig.DefaultEncryption` 字段 + schema migration | DB 迁移 + repository 方法 | 1 天 |
| 重构 `encrypt.go` 为可注入接口 | `Encryptor` + `EncryptionStrategyResolver` | 3 天 |
| KMS Provider 接口 + Local keyfile 实现 | `internal/crypto/kms/` | 2 天 |
| REST API: `PUT/GET/DELETE /v1/buckets/{name}/encryption` | handler + router + scope + OpenAPI | 2 天 |
| 扩展 `Storage` 接口: `Health(ctx)` | 接口 + Local/S3 实现 | 1 天 |
| `HealthMonitor` 协程 + OTel 指标 + 事件 | 协程 + 仪表盘面板 | 2 天 |
| 集成测试 | 多策略、KMS 模拟、故障注入 | 2 天 |

**风险：** 加密重构影响所有读写路径。缓解：每步增量、各单元测试覆盖。`EncryptionStrategyResolver` 返回 nil 时回退到全局配置。

**里程碑 M3：** 桶级加密策略可用。存储健康检查集成到 readiness probe 和指标。

#### 阶段 4（Sprint 7+）：Web Admin Dashboard ⬆️ P2

| 任务 | 产出 | 预计 |
|------|------|------|
| 设计 Admin UI 路由和权限模型 | 设计文档 | 1 天 |
| session 管理（cookie + CSRF） | `internal/api/auth/session.go` | 2 天 |
| 审计日志页面 + 搜索/过滤 | `/ui/admin/audit` | 3 天 |
| 作业列表页面（jobs table 显示） | `/ui/admin/jobs` | 2 天 |
| 租户管理页面（已有 API 的 CRUD 界面） | `/ui/admin/tenants` | 3 天 |
| 集成 Grafana 面板嵌入 | iframe 或代理 | 1 天 |

**风险：** 前端技术栈选择影响交付速度。缓解：先纯 Go templates + htmx，后续如果需要再升级为 SPA 框架。

**里程碑 M4：** Admin Dashboard MVP 可用 — 审计日志浏览、作业监控、租户管理。

### 风险矩阵总览

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|---------|
| EventBus 改造引入回归 | 中 | 高 — 影响所有事件依赖的功能 | 并行运行新旧实现 + feature flag |
| 生命周期转换后的数据一致性问题 | 中 | 高 — 数据丢失风险 | 幂等 transition + 转换前先 copy 后 delete + 事务性元数据更新 |
| Local 后端没有真正的分层存储 | 高 | 低 — Local 用于开发测试，不用于生产 | 基于文件属性或目录划分模拟层级 |
| 加密重构导致性能下降 | 低 | 中 — 加密层加间接调用 | Benchmark 对比 + 缓存策略解析结果 |
| 前端能力不足影响 Web Admin 交付 | 中 | 中 — P2 特性可推迟 | 先用 Go templates，降级到只读面板 |

---

## 总结

这份架构分析文档识别出了代码库中 5 个高价值方向，全部经代码审计确认。我建议的路线图以**先修基础设施，再建业务能力**为原则：

1. **先让事件不丢**（EventBus 持久化）→ 这是桶级通知、访问日志、跨区复制、AV 扫描的基石
2. **再管理数据成本**（生命周期转换）→ 最复杂的架构变更需要最多设计迭代
3. **再加固安全合规**（桶级加密 + 健康检查）→ 高客户价值、中等复杂度
4. **最后做产品体验**（Admin Dashboard）→ 非核心、可推迟

从架构债务角度，**事件系统是唯一真正的架构级问题**。生命周期和加密策略是功能扩展，不是架构修复。

如果需要继续深入某个方向的设计细节，我可以进一步细化。
