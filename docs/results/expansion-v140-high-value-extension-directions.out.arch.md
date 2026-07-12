现在我已拥有全部上下文。以下是完整的架构分析报告。

---

# 架构分析报告：AeroVault 存储分层、元数据查询、事件工作流、读缓存、PITR

> **视角：** 资深架构师  
> **基础：** 全代码库验证结果 + v91/v140/v139 既有分析 + ROADMAP.md + architecture.md + AGENTS.md  
> **范围：** 五个验证方向（生命周期、元数据查询、事件触发器、读缓存、快照）的系统性架构分析  
> **核心原则：** 不编写代码。聚焦架构决策、接口设计、依赖关系、实施优先级。

---

## 1. 架构评估

### 1.1 当前架构的结构性优势

AeroVault 的架构在当前阶段表现出一系列值得肯定的设计决策：

| 优势 | 具体表现 | 架构价值 |
|------|---------|---------|
| **三严格分层** | Protocol(thin) → Service → Storage/Repository | 替换协议适配器或存储后端时，其他两层完全不受影响。已验证：4 种存储后端 + 2 种数据库可互换。 |
| **单入口 FileService** | 所有协议、worker、测试都经过同一入口 | 业务规则（配额、版本控制、WORM）一次编写到处生效。这是整个架构最核心的正确决策。 |
| **Opt-in 安全默认** | AI、pgvector、Qdrant、事件、集群功能全部 flag-gated | CI gate 基线路径（SQLite + local FS + 无鉴权）零外部依赖，确保 `go test ./...` 在任何环境可运行。 |
| **Migrate 双轨制** | `migrations/{sqlite,postgres}/` 独立迁移对 | SQLite 开发体验与 Postgres 生产部署可独立演进，且可验证迁移文件不会破坏任意一端。 |
| **事件持久化 + 广播分离** | `events` 表持久化 + 内存广播 | 即使进程崩溃，事件不会丢失；重启后可重放。 |

### 1.2 架构局限性（按严重程度排序）

```
严重 (Blocking)         中等 (Slowing)           轻微 (Cosmetic)
─────────────────────  ─────────────────────  ─────────────────────
Storage 接无生命周期语义  读路径零缓存              BucketConfig 扁平化
EventBus 无路由能力      ListObjects 仅前缀匹配     快照仅 SQLite+local
                        通知规则 schema 空壳运行
```

#### 严重级：直接影响产品竞争力的架构缺口

**1.2.1 `Storage` 接口缺少生命周期语义**

验证确认：`reconcile/lifecycle.go` 的 `sweepExpired()` 是唯一的 sweep 方法，零 Transition。`BucketConfig` 的 `ExpireAfterDays` + `ExpireAction` 是唯一的生命周期配置。

架构影响：当前 `Storage` 接口的设计哲学是"最小化方法集"，这在初期是正确决策。然而经过 4 个后端实现的经验验证，接口已经稳定到可以安全扩展。问题是**扩展方式的选择**——是增加专有方法 (`TransitionStorageClass`, `RestoreFromGlacier`)，还是泛化已有方法 (`Copy` 增加 storage class 参数)？

**核心架构决策**：两种方案的权衡将在 3.1 节详细分析。

**1.2.2 事件总线无路由能力，通知规则 schema 空壳运行**

验证确认：`events/bus.go` 是纯广播模式，所有 subscriber 接收全部事件。`notification_rules` JSON 列（migration 0024）已存在，CRUD 端点已注册，但**零执行**。

架构影响：这是一个"只建了存储层没建执行层"的典型架构债务。`BucketConfig.Notifications` 字段有存储和序列化逻辑，但没有任何消费者。这意味着即使通知规则 schema 改变，也没有迁移压力——但也意味着引入执行引擎时，schema 可能与执行需求不匹配。

**核心发现**：通知规则 schema 的设计假定"通知目标是单一字段（QueueARN/TopicARN/LambdaARN）"，但产品化方案需要"通知目标是多条规则（HTTP endpoint + 前缀/后缀过滤 + 速率限制）"。这意味着 schema 可能需要重构而非简单填充。

#### 中级：限制扩展速度的架构缺口

**1.2.3 查询能力单一**

验证确认：`sql_objects.go` 中的 `ListObjects` SQL 仅 `WHERE bucket=? AND key LIKE ?`。验证同时发现 S3 兼容路径存在 `ListObjectsByTag`，但使用**客户端过滤**——先执行无 tag 条件的 SQL 查询，再在 Go 层按 tag 过滤。这个验证发现值得注意：S3 路径已经实现 tag 过滤的 API 骨架，但性能不可扩展。

架构影响：这个缺口的影响比表面看起来大——`GET /v1/files` 当前的 prefix/marker/limit 模式对 metadata 查询完全不够。但解决方案不是替换 `ListObjects`，而是**增量扩展**：新增过滤参数，不加过滤参数时行为与当前完全一致。

**1.2.4 读路径零缓存**

验证确认：`storage.go` 的 `Storage` 接口无缓存装饰器，`local_read.go` 每次 `os.Open`。验证确认 `factory.go` 无 wrap-with-cache 逻辑。

架构影响：这不是紧急问题——对于单实例部署和低频访问场景，直接读取存储后端的延迟是可接受的。它变成瓶颈的条件是：(1) 高频读取热对象 (>100 req/s)，(2) 多区域部署，(3) 云存储后端（每次 GET 有 API 费用）。这些条件在项目早期可能不满足，但随着用户增长会逐步出现。

#### 轻微级：在特定场景下限制的架构缺口

**1.2.5 快照仅限 SQLite + local FS**

验证确认：`snapshot.go:23` 的注释明确声明"only sqlite local snapshots are supported"，`dbFileFromDSN` 对 Postgres DSN 返回空字符串。

架构影响：这是明确的"文档即承诺"的设计约束——不是意外的缺口，而是故意的范围限制。产品化 Postgres + S3 的快照需要外部工具链（`pg_dump` + `s3 sync`），而这两个工具在各自领域都很成熟。这个方向的价值取决于：(1) 用户是否需要一键快照的体验，(2) 合规场景是否需要自动化的、可验证的快照流程。

### 1.3 设计决策合理性审查

| 决策 | 验证结果 | 评价 |
|------|---------|------|
| `FileService` 唯一入口 | ✅ v91 分析确认 | **无可替代的方案**——这是整个架构最核心的正确决策。没有设计替代方案的必要。 |
| Storage 接口最小化 | ✅ 验证确认 4 后端全部实现 | 初期的过度工程预防是对的。现在可以考虑安全扩展。 |
| EventBus 同步广播 | ✅ 验证确认纯广播模式 | 对于当前规模（内部 subscriber 硬编码）是完全合理的。**但必须确保方向三不在此路径同步执行规则匹配**。 |
| Metadata/元数据 schema 就绪 | ✅ 验证确认 `Tags`/`Metadata` 字段存在 | 数据模型完整但查询能力为零——这是"投资了存储层但没投资查询层"的经典不平衡。 |
| 通知规则 schema 先建 | ✅ 验证确认 migration 0024 存在 | **建筑学上的技债**——schema 先于执行引擎存在，导致设计可能不匹配实际执行需求。值得跟踪是否需要在引入执行引擎前重构 schema。 |
| 分层装饰器（RateLimiter） | ✅ 验证确认 middleware 链顺序固定 | 装饰器模式已被验证为 Storage 接口的可扩展机制。**建议统一装饰器链的装配和顺序规范**。 |

### 1.4 去重验证的价值

验证结果确认了 v91/v140 分析中五个方向的代码锚点准确性，同时发现一个值得补充的细节：

> **S3 兼容路径存在 `ListObjectsByTag`**（`internal/api/s3compat/handler.go:471-479`），但使用**客户端内存过滤**——先在 SQL 层执行无 tag 条件的 `ListObjects`，再在 Go 层按 tag 筛选。这证实了"SQL 级无 tags 过滤"的分析，但也表明 S3 路径已经有 tag 查询的 API 骨架——只是实现方式是 O(n) 的，不可扩展。

这个发现的架构含义：方向二的 REST API 实现（`GET /v1/files?tag.dept=finance`）应该使用 SQL 级过滤（`json_extract` + 索引），而不是复制 S3 路径的客户端过滤模式。S3 路径的 `ListObjectsByTag` 也应该在 SQL 级过滤实现后**重构为 SQL 级**——这是清理历史实现的选择。

---

## 2. 扩展方向：跨方向依赖分析与高价值路径

### 2.1 跨方向依赖图谱

五个方向不是独立的——它们之间有依赖关系和潜在的冲突：

```mermaid
flowgraph TD
    D2["方向二: 元数据查询"] --> D1["方向一: 生命周期"]
    D1 --> D3["方向三: 事件工作流"]
    D1 --> D5["方向五: PITR"]
    D3 --> D4["方向四: 读缓存"]
    D5 -.-> D3["(PITR 需要事件 CDC)"]
    
    style D2 fill:#a8d5a2
    style D1 fill:#ffd966
    style D3 fill:#f4a261
    style D4 fill:#e9c46a
    style D5 fill:#e76f51
```

**依赖解释：**

| 依赖 | 理由 | 强度 |
|------|------|------|
| 方向二 → 方向一 | 元数据查询（`ListObjectsFiltered`）的 `sort_by`/过滤条件可复用于 `ListTransitionDue` 的查询。先做方向二，Repository 层条件构建器的经验直接用于方向一。 | **弱依赖**——可并行 |
| 方向一 → 方向三 | lifecycle transition 的失败重试策略（异步 worker 模式）可直接复用于通知规则的异步匹配。方向三也依赖方向一修改的 `BucketConfig` 结构作为规则存储基础。 | **强依赖**——推荐顺序 |
| 方向一 → 方向五 | 生命周期 transition 引入了 storage_class 变更，快照 manifest 需要记录 storage_class。方向一的 `ListActiveObjects` 查询可复用于方向五的 manifest 生成。 | **弱依赖**——可并行 |
| 方向一 → 方向四 | 生命周期中 GLACIER restore 的 GET 路径更复杂（需返回 `InvalidObjectState` 或自动触发 restore），缓存层需要理解 storage_class 的 restore 状态。 | **弱依赖**——方向四可先做非 GLACIER 场景 |
| 方向三 → 方向四 | 事件路由的可靠性和重试策略与缓存层的故障降级策略可以共享设计模式（断路器配置）。 | **弱依赖**——可并行 |

**核心建议**：方向二和方向四可以最先独立启动（零外部依赖），方向一可以在方向二之后启动（复用条件构建器经验），方向三和方向五依赖于前序方向的架构基础。

### 2.2 五个方向的系统性架构模式

从五个方向中提炼出的通用架构模式：

**模式一：异步 Worker 循环**

```
方向一: reconcile/lifecycle 循环扫描 → 执行 transition
方向三: notification worker 循环扫描 events 表 → 匹配规则 → 发送
方向五: snapshot worker = 事务级读取 → 生成 manifest
```

当前 `reconcile` 周期（默认 ~5 分钟）可以作为这种模式的参考实现。**建议提取通用接口**：

```go
type SweepJob interface {
    Name() string
    Interval() time.Duration
    Sweep(ctx context.Context) (int, error) // 返回处理数
}
```

这可以使三个方向共享同一个调度框架（间隔配置、集群单例、指标追踪），而不是各自实现定时器。

**模式二：条件构建器**

```
方向二: ListObjectsFiltered → ObjectFilter → SQL WHERE 构建
方向三: EventFilter → 规则匹配
方向五: ManifestFilter → 对象查询
```

方向二的 SQL 条件构建器（处理 SQLite vs Postgres JSON 函数差异）可以复用为通用的过滤表达式系统。建议在 `internal/repository/sql.go` 中提取 `FilterBuilder`。

**模式三：装饰器链**

```
方向四: CacheStorage(Storage) → 读缓存
现有:   RateLimitStorage(Storage) → 限流
建议:   MetricsStorage(Storage) → 指标
```

建议统一装饰器链的装配顺序规范。当前 `factory.go` 中 `RateLimiter` 是直接硬编码包装的。

### 2.3 第 6 个方向建议：装饰器链装配与接口治理

以上分析揭示了一个跨所有方向的系统性缺口：**Storage 接口缺少装饰器链的显式装配机制**。

当前 `factory.go` 中的 Storage 构建逻辑：

```go
// 当前: 硬编码装饰
store := buildRawBackend(config)
store = WrapWithRateLimiter(store, config) // 硬编码
```

方向四引入 `CacheStorage` 后，装饰器链变为：

```
CacheStorage(RateLimitStorage(S3Storage))
```

方向三引入 `RetryStorage` 后变为：

```
MetricsStorage(CacheStorage(RetryStorage(RateLimitStorage(S3Storage))))
```

**问题**：装饰器顺序是重要的——`MetricsStorage` 应是最外层（记录所有经过的请求），`RetryStorage` 应在 `CacheStorage` 之外（缓存命中时不需要重试）。当前无任何顺序保障机制。

**建议**：引入 `StorageMiddleware` 概念：

```go
type StorageMiddleware func(Storage) Storage

// 在 factory.go 中:
chain := []StorageMiddleware{
    MetricsMiddleware(config),
    RetryMiddleware(config),
    CacheMiddleware(config),
    RateLimitMiddleware(config),
}

var store Storage = buildRawBackend(config)
// 按顺序从外到内装配
for _, mw := range chain {
    store = mw(store)
}
```

这个方向单独提出是因为它不直接产生用户可见的新功能，但**对架构可维护性有深远影响**——它决定了所有未来装饰器（缓存、重试、熔断、指标）能否有序组合。

### 2.4 高价值补充方向：Lock-Free Metadata 读取路径

**方向六：Metadata 读路径分层（Read Replica Awareness）**

当前所有 metadata 查询（`Stat`/`Head`/`ListObjects`）都直接访问 Repository 的主实例。对于 Postgres 部署，这意味着：
- 读查询和写查询共用同一连接池
- 大量 HEAD 请求（检查 ETag/Size/ContentType）占用主库连接
- 无法水平扩展读能力

与方向四（内容缓存）不同，这是 metadata 层的读扩展，不涉及对象内容的缓存。

**建议**：在 `Repository` 接口新增 `ReadReplica` 可选项：

```go
type Repository interface {
    // 主实例（写入）
    CreateObject(...) 
    DeleteObject(...)
    
    // 读副本（自动路由）
    GetObject(ctx, tenant, bucket, key string, opts ...ReadOption) (Object, error)
    // opts 可以包含 WithConsistency(Strong | Eventual)
}
```

**与方向四的关系**：方向四缓存对象内容（`Storage.Get` 的 body 缓存），方向六缓存/路由 metadata（`Repository.Stat` 的读副本路由）。两者互补但不重叠。

---

## 3. 接口设计建议

### 3.1 Storage 接口扩展原则

**核心决策：新增专用方法 vs 泛化已有方法**

```
选项 A: 新增 TransitionStorageClass / RestoreFromGlacier
  优点: 语义明确、容易测试、后端可选择性实现
  缺点: Storage 接口方法数增加

选项 B: 扩展 Copy 语义（opts 中含 StorageClass）
  优点: 不增加方法数、S3 的 CopyObject 本来就支持 StorageClass 参数
  缺点: 语义混叠——Copy 可能含有/不含 storage class 变更

选项 C: 拆分子接口
  type TieredStorage interface {
      Storage
      TransitionStorageClass(ctx, key, targetClass) (ObjectInfo, error)
      RestoreFromGlacier(ctx, key, days) error
  }
  优点: 接口隔离——不需要分层的后端（如 local）不需要实现
  缺点: 调用方需要类型断言检查，失去编译期类型安全
```

**推荐：选项 A（新增方法）+ 选项 C（子接口分离远期）**

短期（方向一实现）采用选项 A，因为：
1. 方向一要求尽快交付，选项 A 实现路径最清晰
2. `TransitionStorageClass` 和 `RestoreFromGlacier` 语义与 `Copy` 完全不同——Transition 不涉及元数据复制、ACL 继承等
3. Local 后端可以立即实现（文件重命名到子目录），验证接口设计

长期（方向一之后）应考虑选项 C，因为：
- `Storage` 接口当前已有 ~12 个方法。如果后续增加 `Backup`, `Restore`, `Checksum`, `BatchDelete` 等，接口将膨胀到不可维护
- 子接口模式允许不同后端提供不同能力集合

**默认 fallback 模式**（立即实施）：

```go
// 在 storage.go 中定义
var ErrNotImplemented = errors.New("storage: not implemented")

// 嵌入到 baseStorage 中提供默认实现
type baseStorage struct{}
func (b *baseStorage) TransitionStorageClass(ctx, key, targetClass string, opts TransitionOpts) (ObjectInfo, error) {
    return ObjectInfo{}, fmt.Errorf("%w: TransitionStorageClass", ErrNotImplemented)
}
func (b *baseStorage) RestoreFromGlacier(ctx, key string, days int) error {
    return fmt.Errorf("%w: RestoreFromGlacier", ErrNotImplemented)
}

// 后端嵌入 baseStorage 并覆写需要的方法
type LocalStorage struct {
    baseStorage  // 自动获得默认 ErrNotImplemented 实现
    ...
}
func (l *LocalStorage) TransitionStorageClass(...) (ObjectInfo, error) {
    // Local 实际实现
}
```

### 3.2 Repository 查询扩展原则

**核心模式：Filter 对象 + 条件构建器**

```go
// 在 sql.go 中（隐藏 SQLite vs Postgres 差异）
type FilterBuilder struct {
    dialect string // "sqlite" | "postgres"
    where   []string
    args    []interface{}
}

func (f *FilterBuilder) AddTag(key, value string) *FilterBuilder {
    if f.dialect == "postgres" {
        f.where = append(f.where, fmt.Sprintf("tags @> $%d", len(f.args)+1))
        f.args = append(f.args, fmt.Sprintf(`{"%s":"%s"}`, key, value))
    } else {
        f.where = append(f.where, fmt.Sprintf("json_extract(tags, '$.\"%s\"') = $%d", key, len(f.args)+1))
        f.args = append(f.args, value)
    }
    return f
}

func (f *FilterBuilder) AddSizeRange(min, max int64) *FilterBuilder {
    if min > 0 {
        f.where = append(f.where, fmt.Sprintf("size >= $%d", len(f.args)+1))
        f.args = append(f.args, min)
    }
    if max > 0 {
        f.where = append(f.where, fmt.Sprintf("size <= $%d", len(f.args)+1))
        f.args = append(f.args, max)
    }
    return f
}
```

**关键约束**：
1. 所有用户输入通过参数化查询绑定，严格遵守 I1 规则（每个 `$N` 独立编号）
2. `sort_by` 字段白名单校验（仅允许 `key/size/created_at/updated_at/content_type`）
3. `tag.<k>` 的 key 校验正则 `^[a-zA-Z0-9_-]{1,128}$`
4. 自动 `LIMIT` 上限（建议 1000），使用 `marker` 游标分页

### 3.3 EventBus 扩展原则

**核心决策：同步 vs 异步规则匹配**

| 维度 | 同步匹配（不推荐） | **异步匹配（推荐）** |
|------|-------------------|--------------------|
| `Publish` 延迟 | 增加 O(规则数) | **零增加** |
| 事件持久化 | 已入 events 表 | 已入 events 表 |
| 匹配时机 | 写入时 | **从 events 表消费时** |
| 失败影响 | 阻塞写入 | 仅阻塞通知路径 |
| 规则复杂性限制 | 必须在毫秒级完成 | 无严格时限 |
| 与 reconcile 模式一致性 | 不匹配 | **与方向一一致** |

**推荐异步匹配**——与方向一的 reconcile 循环使用相同的模式：`NotificationRouter` 作为 EventBus 的一个 subscriber（非内置），内部定时从 `events` 表消费未处理事件。

### 3.4 新抽象层评估

| 抽象层 | 需要 | 引入时机 | 理由 |
|--------|------|---------|------|
| `SweepJob` 接口 | ✅ 建议 | 方向一实现前 | 三个方向共享调度框架 |
| `FilterBuilder` | ✅ 需要 | 方向二实现前 | 跨 SQLite/Postgres 的双实现 |
| `StorageMiddleware` | ⚠️ 建议 | 方向四实现前 | 装饰器链顺序保障 |
| `NotificationRouter` | ✅ 需要 | 方向三实现时 | 独立 subscriber 而非 EventBus 内置 |
| `Snapshotter` 接口 | ⚠️ 建议 | 方向五 Phase 1 | Phase 1 可用函数，Phase 2 再抽象 |
| 规则引擎 DSL | ❌ 不必要 | — | JSON 规则格式已足够 |

---

## 4. 技术选型

### 4.1 依赖评估

| 方向 | 所需技术 | 实现策略 | 新增依赖 |
|------|---------|---------|---------|
| 方向一·生命周期 | storage class transition | Storage 接口新增方法 | **零新增** |
| 方向一·GLACIER restore | restore 状态跟踪 | 数据库列 + reconcile 状态机 | **零新增** |
| 方向二·元数据查询 | SQLite JSON 函数 / Postgres JSONB | 数据库内置功能 | **零新增** |
| 方向二·LRU 缓存 | 内存 LRU | `container/list` + `sync.RWMutex` | **零新增** |
| 方向三·HTTP 目标 | HTTP POST | `net/http`（已有） | **零新增** |
| 方向三·SQS/SNS/Lambda | AWS SDK | 暂缓——方向三第一步只用 HTTP | 未来新增 |
| 方向三·规则级限流 | 令牌桶 | 复用 `middleware/ratelimit.go` 模式 | **零新增** |
| 方向四·内存缓存 | LRU + TTL | `container/list` + `sync.RWMutex` + `time` | **零新增** |
| 方向四·Redis 缓存 | Redis 客户端 | 视需求引入 `go-redis` | 未来按需 |
| 方向五·Postgres 快照 | pg_dump | 调用外部命令 | **零新增** |
| 方向五·S3 内容复制 | S3 CopyObject | 已有 S3 SDK | **零新增** |

**关键发现**：五个方向都可以用现有技术栈实现，零新增核心依赖。这是架构抽象质量的重要指标——良好的抽象边界使得新功能可以在不引入外部依赖的情况下实现。

### 4.2 自建 vs 采购决策

| 组件 | 决策 | 理由 | 风险评估 |
|------|------|------|---------|
| LRU 缓存 | **自建** | 约 50 行 Go 代码；`container/list` + `sync.Mutex` 完全可胜任 | 低——比引入外部 LRU 库更简单 |
| 通知规则引擎 | **自建** | 核心差异化能力；无合适开源替代 | 中——需重试、限流、持久化 |
| Postgres 快照 | **调用 pg_dump** | pg_dump 有 15+ 年生产验证；不需要重复实现 | 低——进程调用模式简单 |
| Redis 缓存 | **开源库** | Redis 集成不是差异化功能 | 低——`go-redis` 成熟数据驱动 |
| 权限策略引擎 | **未来评估** | 如果方向需要 Casbin/OPA 集成 | 中——策略引擎是复杂的独立领域 |

### 4.3 不使用新技术栈的理由

五个方向均不需要引入：
- **消息队列**（Kafka/NATS/RabbitMQ）：数据库 `events` 表 + `SKIP LOCKED` 轮询已足够
- **缓存数据库**（Redis/Memcached）：LRU 内存缓存作为第一步，Redis 作为按需升级
- **配置中心**（etcd/Consul）：环境变量 + 数据库持久配置已覆盖
- **容器编排**（Kubernetes CRD）：Helm chart 是部署工具，不在运行时架构范围内

---

## 5. 实施路线图

### 5.1 优先级与依赖矩阵

```
依赖关系:
  D2(元数据查询) ──→ D1(生命周期分层)
                        │
                    D1 ──→ D3(事件工作流)
                    D1 ──→ D5(PITR)
  D4(读缓存) 独立于 D1/D2/D3

建议执行顺序:
  Sprint N+1: D2(元数据查询) + D4(内存缓存起步)
  Sprint N+2: D1(生命周期分层)
  Sprint N+3: D3(事件工作流)
  Sprint N+4: D5(PITR Phase 1) + D4(Redis 升级，可选)
```

**排序依据**：

| 方向 | 优先级 | 风险调整 |
|------|--------|---------|
| **D2·元数据查询** | **P1** | 零架构风险，低复杂度(1-2周)，高用户感知价值。**建议最先启动** |
| **D1·生命周期分层** | **P1** | 架构影响最大。但 Storage 接口扩展的破坏性可以通过默认 fallback 缓解。 |
| **D3·事件工作流** | **P2** | 通知规则 schema 已存在但需要重构——这是最大的不确定性 |
| **D4·读缓存** | **P2** | 装饰器模式风险最低——但它依赖场景（用户需要高频读取）才具有实际价值 |
| **D5·PITR** | **P3** | 技术复杂度最高但操作频率最低；外部工具链可作为 workaround |

### 5.2 阶段划分

**阶段一：基础查询能力 + 缓存骨架（Sprint N+1）**

| 方向 | 交付物 | 独立交付价值 |
|------|--------|-------------|
| D2 | `GET /v1/files?tag.*&metadata.*&size_min&created_after&sort_by` | ✅ 用户可以按标签/元数据搜索对象了 |
| D2 | `FilterBuilder` 抽象（sql.go） | ✅ 为 D1 提供可复用的条件构建器 |
| D4 | `CacheStorage` 装饰器（内存 LRU） | ✅ 高频读取场景延迟降低 |
| D4 | `StorageMiddleware` 装配框架 | ✅ 为未来的装饰器提供有序链 |

**阶段二：生命周期引擎（Sprint N+2）**

| 方向 | 交付物 | 独立交付价值 |
|------|--------|-------------|
| D1 | `TransitionStorageClass` + `RestoreFromGlacier` Storage 方法 | ✅ 存储成本可降低 |
| D1 | `BucketConfig.LifecycleRules` 完整 S3 兼容 | ✅ S3 兼容性提升 |
| D1 | `sweepTransitions` + `sweepNoncurrent` + `sweepAbortedMPU` | ✅ 自动声明式生命周期管理 |
| D1 | `SweepJob` 接口抽象 | ✅ 为 D3 提供调度框架 |

**阶段三：事件通知引擎（Sprint N+3）**

| 方向 | 交付物 | 独立交付价值 |
|------|--------|-------------|
| D3 | `NotificationRouter` 异步 worker | ✅ 通知规则从空壳变为可执行 |
| D3 | HTTP 目标分发 + 规则级限流 | ✅ 外部系统可原生集成 |
| D3 | 与全局 webhook 共存策略 | ✅ 系统级监控与业务级集成分离 |

**阶段四：生产级快照（Sprint N+4）**

| 方向 | 交付物 | 独立交付价值 |
|------|--------|-------------|
| D5 | `aero-vault snapshot create --manifest-only` | ✅ 审计清单可用 |
| D5 | `aero-vault snapshot restore --manifest` | ✅ 对象完整性验证 |
| D5 | Manifest 格式 | ✅ 为增量快照和 CDC 提供基础 |

### 5.3 风险矩阵与缓解

| 风险 | 概率 | 影响 | 触发条件 | 缓解策略 |
|------|------|------|---------|---------|
| **Storage 接口膨胀到不可维护** | 中 | 高 | 方向一引入 2 个新方法 + 后续各方向再增 | 方向一之后评估子接口拆分（`TieredStorage`） |
| **JSON 查询在全表扫描时性能灾难** | 高 | 中 | 无索引的大表执行多条件查询 | 查询计划检测 + metrics 告警 + 索引指南 |
| **通知规则 schema 不匹配执行需求** | 高 | 中 | 方向三实施时发现 schema 字段不足 | 在方向一之后、方向三之前评审 schema；增加向后兼容字段 |
| **装饰器链顺序错误导致行为异常** | 中 | 高 | 多装饰器装配后出现竞态或顺序敏感 | 方向四实施前建立装饰器顺序规范+链式装配框架 |
| **多方向并行开发上下文切换成本** | 中 | 中 | 方向二和方向四并行开始 | 控制并行方向数≤2；方向二低风险先交付积累 momentum |
| **方向一 Transition 与方向三事件的路由冲突** | 低 | 中 | 同一对象同时触发 lifecycle transition 和事件通知 | 方向一先行确定事件 payload 格式；方向三兼容该格式 |

### 5.4 明确的 No-Go

| 建议排除 | 理由 | 替代方案 |
|---------|------|---------|
| 方向三的 SQS/SNS/Lambda 目标（第一步） | 增加云厂商绑定和依赖复杂度 | **HTTP 目标覆盖大部分场景**，ARN 目标可后续按需实现 |
| 方向四的 Redis 缓存（第一步） | 未验证是否需要 | LRU 内存缓存作为第一步，Redis 按需升级 |
| 方向五的 Phase 2/3（第一步） | 复杂度高且用户价值未验证 | Phase 1 manifest-only 提供基础审计能力 |
| 方向六的读副本（当前阶段） | 依赖 Postgres 流复制配置 | 先做方向四的内容缓存，读副本视为远期 |
| 独立的规则引擎 DSL | S3 兼容 JSON 格式已足够 | `notification_rules` JSON 标准格式 |

---

## 总结

| 维度 | 评价 |
|------|------|
| **架构健康度** | **优秀**。核心的三层解耦 + FileService 单一入口 + Storage/Repository 接口抽象经过五个方向的验证，显示出良好的弹性。 |
| **最大债务** | `notification_rules` schema 已存在但无执行引擎——最早需要清偿的技债。但 schema 可能不匹配执行需求，需要在实施方向三前仔细评审。 |
| **最高价值 / 最低风险** | **方向二·元数据查询**——1-2 周完成，零架构风险，Schema 就绪，高用户感知。建议作为切入点。 |
| **最需谨慎设计** | **方向一·生命周期**——Storage 接口扩展 ripple effect 最大。推荐从 Local 后端开始验证，再扩展到 S3/OSS/COS。 |
| **最大架构影响决策** | EventBus Publish 路径的**异步规则匹配**——这是防止方向三反向影响核心写入延迟的关键决策。与方向一的 reconcile 模式一致。 |
| **推荐实施顺序** | D2(元数据查询) → D1(生命周期) → D3(事件工作流) → D4(读缓存) → D5(PITR Phase 1) |
| **去重验证价值** | 确认 S3 路径存在 `ListObjectsByTag`（客户端过滤），建议方向二使用 SQL 级过滤而非复制该模式 |
