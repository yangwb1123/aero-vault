好的，我已完整阅读 `docs/requirements/expansion-v92-multi-backend-sse-filters-job-ops-cross-protocol-folder-storage-capabilities.md`（405 行）。以下从架构师视角进行分析。

---

# 架构分析报告：基于 v92 扩展方向评估

## 1. 架构评估

### 1.1 当前架构的核心优势

当前架构在 v1→v92 演进中形成了清晰的**分层隔离 + 事件驱动 + 特征门控**模式，有几点值得肯定：

| 优势 | 体现 |
|------|------|
| **协议适配器薄层化** | REST/S3/WebDAV/MCP 各为独立薄层，共享 `FileService` 单一入口，避免了每个协议实现一套完整业务逻辑。这是 "Hexagonal Architecture" 的干净实践 |
| **持久化层可替换** | `Storage` 接口与 `Repository` 接口各自拥有多种实现（local/S3/OSS/COS + SQLite/Postgres），且通过工厂函数 `NewFromConfig` 装配，符合依赖反转 |
| **事件总线解耦** | `EventBus` 将同步操作（CRUD）与异步副作用（Indexer/Webhook/Replication/Antivirus）解耦，写入路径不会被慢速附加流程阻塞 |
| **特征门控安全默认** | AI/pgvector/Qdrant/WebDAV/events 全部默认关闭，`nil` embedder/llm/reranker 不破坏核心 CRUD — 这是长时间运行系统应有的防御姿态 |
| **契约测试套件** | `storage/contract_test.go` 的存在表明团队理解"先契约后实现"的价值 |

### 1.2 架构局限性

通过 v92 分析文档结合对代码库结构（`AGENTS.md` + `cmd/server/main.go` 装配模式）的审视，我识别出以下结构性局限性：

**局限一：存储层缺乏自描述能力**

`Storage` 接口对所有后端一视同仁，没有能力自省（introspection）。这导致系统被迫采用"最低公分母"行为——`copyObject` 永远走 read+write，即使后端原生支持服务端拷贝；`PresignGet` 在 `PublicURL=""` 时静默失败。这在面向多后端的架构中是不可持续的。

**局限二：单后端假设渗透全局**

从 `main.go:buildStorage` → `factory.go:NewFromConfig` → `FileService` 构造函数的签名，整个代码链假设"一个系统 = 一个存储后端"。`Object.StorageClass` 列在 schema 中属于"写入后永不读取"的幽灵字段——数据存在但从不参与路由。这是**半完成的功能债务**：迁移 `0021_storage_class` 以为后续会用，但路由逻辑从未跟进。

**局限三：事件总线是内存广播总线**

`Bus.Publish` 使用 goroutine 扇出给全部订阅者，`Subscribe` 返回无过滤通道。这在 10 个订阅者时尚可，但 100+ SSE 连接 × 2000 事件/秒的场景下，每个连接都在接收全量事件再在客户端丢弃——CPU 和带宽浪费比是 `O(n_subscribers)` 的。更大的问题是：事件无持久化，重启后丢失，`Last-Event-ID` 续传机制缺少基础存储设施。

**局限四：文件夹语义泄漏到三个协议接口**

这是一个典型的**抽象泄漏（Leaky Abstraction）** 问题。REST 用 `application/x-directory` 标记对象 + 尾随 `/`，S3 用隐式虚拟目录（`delimiter=/` 推导 `CommonPrefixes`），WebDAV 用 OS 文件系统语义。三者在存储层都映射到同一个 `Storage` 接口，但协议层的解释不一致——S3 客户端能看到标记对象为常规文件，WebDAV 的 `RemoveAll` 不递归删除子对象。这导致运维行为不可预测。

**局限五：Job Pool 有引擎无力表**

`jobs.Pool` 实现了完整的领用/执行/重试/收割循环，是技术上的好东西。但管理面只有两个端点（List + Retry），可观测性只有三个计数器（completed/failed/retried），没有按类型的耗时直方图、队列深度仪表盘、暂停/恢复操作、失败通知路由。这是"发动机装好了但仪表盘和方向盘没有"的状态。

### 1.3 架构债务清单

| # | 债务描述 | 类型 | 严重度 | 引入时机 |
|---|---------|------|--------|---------|
| D1 | `Object.StorageClass` 列写入后永不用于路由 | 半完成功能 | 中 | 迁移 0021 |
| D2 | `copyObject` 全量读入内存再 PUT（IO 放大 2x+） | 性能债务 | 高 | v1 初始实现 |
| D3 | `LocalStorage.PresignGet` 配置缺失时返回 `errors.New` 而非接口级预检 | 设计缺陷 | 中 | v1 初始实现 |
| D4 | SSE 无过滤谓词 | 可扩展性债务 | 中→高（随租户数增长） | v1 初始实现 |
| D5 | 文件夹语义三协议不一致 | 产品完备性债务 | 中 | 随协议叠加积累 |
| D6 | 作业池仅计数器无直方图 | 可观测性债务 | 低→中（随作业量增长） | v1 初始实现 |

---

## 2. 扩展方向

> v92 文档已识别 5 个方向。我在其基础上补充**全局视角的评估**和**额外的 2 个方向**，并重新进行优先级权衡。

### 2.1 文档已识别的 5 个方向

#### 方向一：多后端存储编排引擎（P1，10-15 天）

**为什么需要：** 
- **业务价值：** 热数据放 local SSD，冷数据放 COS Archive——无需迁移脚本即可实现成本优化
- **技术价值：** 供应商中立，新旧后端并行运行，逐步迁移无 downtime

**核心架构挑战：**
- **写路径分支：** 在 `FileService.Put` 中需要根据 `storage_class` + bucket 策略 → 选择后端 → 写入。关键问题是：这个选择逻辑放哪里？放 `FileService` 会使其膨胀，放 `TieredRouter` 需要引入新抽象
- **读路径寻址：** 读取时必须知道对象在哪个后端。有三种方案：
  | 方案 | 方式 | 优点 | 缺点 |
  |------|------|------|------|
  | A. 元数据 `backend_id` 列 | Object 行增加 `backend_id` | 精确寻址，无歧义 | schema 变更，存量数据需 backfill |
  | B. `StorageKey` 编码后端标识 | key 前缀编码 `{backend}/{tenant}/{bucket}/{key}` | 无需 schema 变更 | key 长度膨胀，GC 逻辑复杂化 |
  | C. 存储类→后端映射 | 配置声明 `STANDARD→s3-hot, GLACIER→cos-cold` | 无 schema 变更 | 无法支持同一存储类多后端 |
- **跨后端 List 聚合：** 需要遍历所有活跃后端并合并排序结果，分页游标需跨后端一致

**对现有系统影响：**
- 配置格式从 `STORAGE_BACKEND` 单值变为 `STORAGE_BACKENDS` 复数配置（YAML 或 JSON 更合适）
- `FileService` 构造函数签名变化：`Storage` → `*TieredRouter`
- 需要 backfill 任务为存量对象补充 `backend_id`
- 所有存储操作路径需要审计：后端选择是否正确

#### 方向二：SSE 事件订阅过滤（P2，3-5 天）

**为什么需要：**
- **业务价值：** 支持 1000+ 并发 SSE 连接而不使事件总线成为瓶颈
- **技术价值：** 减少 80-99% 无意义事件传输，释放 CPU/带宽

**核心架构挑战：**
- **过滤谓词语义：** 需要决定谓词表达式语法（简单 URL query 还是复杂表达式）
  - 推荐：初始版本用 `?types=created,deleted&bucket=my-bucket&prefix=uploads/` 简单键值对，后续可扩展
- **订阅者标记：** `Subscribe` 签名从 `() <-chan Event` 变为 `(predicate FilterPredicate) <-chan Event`
- **共享过滤通道：** 相同谓词的订阅者应共享同一个内部通道，节省 goroutine

#### 方向三：作业可观测性与管理面（P2，5-7 天）

**为什么需要：**
- **业务价值：** 运维人员需要暂停索引任务、取消卡住的作业、在作业失败时收到告警
- **技术价值：** 智能节流可防止错误风暴下作业队列雪崩

**核心架构挑战：**
- **暂停/恢复语义：** 允许当前执行中的作业完成，但不再领用新的。需要 `context.Context` 传递取消信号
- **直方图指标：** OpenTelemetry `Float64Histogram` 按 `job.Type` 属性聚合，配合 Exemplar 记录典型作业 ID

#### 方向四：跨协议命名空间统一（P2，5-8 天）

**为什么需要：**
- **业务价值：** 客户端从 REST 管理迁移到 WebDAV 挂载或 S3 SDK 时，目录结构应一致
- **技术价值：** 消除三个协议语义不一致导致的运维事故

**核心架构挑战：**
- **统一 Namespace 层 vs 各协议分别适配：**
  | 方案 | 优点 | 缺点 |
  |------|------|------|
  | A. `NamespaceManager` 新抽象层 | 一致性最强，消除重复逻辑 | 需要重构三个协议适配器 |
  | B. S3/REST/WebDAV 各自修正 | 改动量小，风险可控 | 逻辑重复，长期仍可能漂移 |
  | C. 虚拟目录优先 + 标记对象回退 | 兼容现有数据，渐进式迁移 | 新旧模式并存期间语义复杂 |

  推荐：**方案 B 作为过渡 + 方案 A 作为长期目标**。短期内在 S3 ListObjects 路径过滤 `application/x-directory` 对象，修复 WebDAV `RemoveAll` 语义；长期提取 `NamespaceManager` 接口。

#### 方向五：存储能力契约（P1，2-3 天）

**为什么需要：**
- **业务价值：** `CopyObject` 性能提升（消除大对象的 IO 放大 2x），配置预检减少运行时错误
- **技术价值：** 方向一的前置依赖——多后端路由需要知道每个后端的能力

**核心架构挑战：**
- **能力枚举粒度：** 过粗（如 `CapS3Compatible`）无法区分版本差异，过细（如 `CapMultipartPartSizeGT5GB`）不可维护
  - 推荐：中等粒度，约 10-15 个枚举值，覆盖 presign/server-side-copy/SSE/multipart/tagging/consistency。具体值随着方向一实施可扩展
- **能力 vs 配置：** 有些能力是后端本质属性（S3 原生支持 server-side-copy），有些是配置决定的（presign 依赖 PublicURL+SignKey 是否配置）

### 2.2 额外识别的高价值方向

#### 方向六：事件持久化与回放引擎（Event Durability & Replay Engine）

**优先级：P2（初始）/ P1（若有合规需求时）**  
**工作量：~8-12 天**

**为什么需要：**
- 目前事件总线是纯内存的——重启后所有未处理事件丢失
- `Last-Event-ID` 续传机制无持久化存储支持，SSE 重连后无法回放
- 审计合规场景需要不可篡改的事件日志
- 跨区域复制需要可靠的事件顺序保证

**核心架构挑战：**
- **事件存储选型：**
  | 方案 | 优点 | 缺点 |
  |------|------|------|
  | A. 事件表（现有数据库） | 零新依赖，事务一致性 | 写放大，SQLite 高吞吐瓶颈 |
  | B. 专用事件存储（Kafka/Redis Streams） | 高吞吐，持久化，分区 | 运维复杂度，分布式一致性问题 |
  | C. WAL 日志附加 | 写入快，顺序性好 | 读取和回放不便，GC 复杂 |

  推荐：**方案 A 初始（复用 `sql.go` 的 `rebind` 机制，新增 `events` 表）**，后续流量增大后按需升级到方案 B。

- **事件 GC 策略：** 已确认被所有订阅者消费的事件可以清理；需要 `acked_offset` 跟踪每个订阅者的进度

#### 方向七：存储级多租户隔离与 QoS（Storage-Level Multi-Tenant Isolation & QoS）

**优先级：P3**  
**工作量：~15-20 天**

**为什么需要：**
- 当前多租户隔离在 Auth→Tenant 中间件层，但存储层不感知租户——一个租户的突发写入会影响其他租户的读取延迟
- 没有按租户的存储配额硬限制（当前配额在业务层，非存储层）
- 无法为高价值租户提供 IOPS/吞吐量保证

**核心架构挑战：**
- **租户感知存储后端：** 每个租户可以映射到不同后端（金租户→NVMe SSD，普通租户→S3 Standard，免费租户→S3 Glacier）
- **租户级限速：** 在 `Storage` 接口包装 `RateLimitedStorage` 装饰器，按租户配置 token bucket
- **存储层 QoS：** 需要后端支持优先级队列（local FS 可通过 goroutine 调度实现；S3 等外部服务无法控制）

---

## 3. 接口设计建议

### 3.1 核心原则

1. **先能力查询后行为调用**（Ask before Act）：消除 try-fail 模式，改为查能力表后决策
2. **行为扩展用装饰器**（Decorator）：`CircuitBreakerStorage` 已是先例；`RateLimitedStorage`、`InstrumentedStorage`、`RetryableStorage` 同理
3. **配置面与行为面分离**：`Capabilities()` 反映后端本质能力，`Config()` 反映当前配置状态；两者组合决定行为
4. **向后兼容默认路径**：引入 `Capabilities()` 后，现有不检查能力的代码继续工作（默认走到当前行为）

### 3.2 建议的接口变更

#### `Storage` 接口扩展

```go
// 现有方法不变 + 新增
type Storage interface {
    // 全部现有方法不变（Put, Get, Stat, Delete, List, PresignGet, PresignPut,
    // InitMultipart, UploadPart, CompleteMultipart, AbortMultipart, Backend）

    // 新增：能力声明
    Capabilities() []Capability
}

type Capability string

const (
    CapPresign            Capability = "presign"
    CapServerSideCopy     Capability = "server_side_copy"
    CapSSE                Capability = "sse"
    CapMultipart          Capability = "multipart"
    CapTagging            Capability = "tagging"
    CapStrongConsistency  Capability = "strong_consistency"
    CapObjectLock         Capability = "object_lock"
    CapLifecycle          Capability = "lifecycle"
)
```

**为什么不做成 `bool` 方法返回？** 枚举数组比 `CanPresign() bool`、`CanServerSideCopy() bool` 扩展性更好——新能力只需加常量，不需要改接口。

#### 新增 `TieredRouter` 抽象

```go
// 位于 internal/storage/tiered.go
type TieredRouter struct {
    backends map[string]Storage  // name → Storage 实例
    defaultBackend string        // 回退后端
}

// 将 FileService 对 Storage 的引用改为对 *TieredRouter 的引用
```

**关键设计决策：路由策略注入**

路由逻辑不应硬编码在 `TieredRouter` 内部，而应通过策略模式注入：

```go
type RouteStrategy interface {
    SelectBackend(ctx context.Context, obj ObjectMeta, capabilities []Capability) (string, Storage, error)
}
```

初始实现可以只支持 `StorageClassStrategy`（根据 `storage_class` 映射后端），后续可扩展 `GeoStrategy`、`TenantStrategy`。

#### `Subscribe` 签名变更（向后兼容）

```go
// 旧签名（保留，内部转为无过滤谓词）
func (b *Bus) Subscribe() <-chan repository.Event

// 新签名
type FilterPredicate func(repository.Event) bool

func (b *Bus) SubscribeWithFilter(pred FilterPredicate) <-chan repository.Event
```

#### 新增 `NamespaceManager`（长期目标）

```go
type NamespaceManager interface {
    IsDirectory(ctx, tenant, bucket, key) (bool, error)
    ListDirectory(ctx, tenant, bucket, prefix) ([]Entry, error)
    CreateDirectory(ctx, tenant, bucket, path) error
    DeleteDirectory(ctx, tenant, bucket, path, recursive) error
    RenameDirectory(ctx, tenant, bucket, oldPath, newPath) error
}
```

### 3.3 向后兼容策略

| 变更 | 兼容策略 |
|------|---------|
| `Storage` 增加 `Capabilities()` | 新增方法，不影响现有调用方；不检查能力的代码继续使用 try-fail 旧路径 |
| `SubscribeWithFilter` | 保留 `Subscribe()` 旧签名，内部委托到 `SubscribeWithFilter(func(_ Event) bool { return true })` |
| `TieredRouter` | 实现 `Storage` 接口，可作为 `Storage` 的 drop-in 替换；单后端场景可以继续使用 `LocalStorage` 或 `S3Storage` 直接 |
| `Object.BackendID` | 新增字段，零值表示默认后端；存量数据 backfill 为默认 |

---

## 4. 技术选型

### 4.1 哪些需要引入新依赖

| 场景 | 是否需要新依赖 | 推荐方案 | 论证 |
|------|--------------|---------|------|
| 多后端配置格式 | 不需要 | 扩展现有 `config.go`，支持 `STORAGE_BACKENDS_JSON` 环境变量 | 避免引入 YAML/Toml 解析器，JSON 与环境变量互转友好 |
| 事件持久化（初期） | 不需要 | 复用现有 Repository 的 SQLite/Postgres 连接，新建 `events` 表 | 零新依赖，利用已有的 `rebind`、迁移机制 |
| 事件持久化（高吞吐） | 需要评估 | Kafka 或 Redis Streams | 写入量 > 10K events/s 时考虑；在此之前 SQLite 足够 |
| 作业耗时直方图 | 不需要 | OpenTelemetry 已有 `Float64Histogram` | 无新依赖，只需注册新 instrument |
| 智能节流滑动窗口 | 不需要 | Go 标准库 `sync.Map` + `time.Ticker` | 内存数据结构即可，无外部依赖 |
| 分布式后端路由建议 | 不需要 | 配置文件静态声明 | 多后端配置在启动时确定，运行时不变；无需服务发现 |

**核心原则：保持与 AGENTS.md 第 I6 条一致——stdlib 优先。** 新依赖需论证必要性。

### 4.2 能力测试框架

对于方向五（能力契约），测试策略应使用"能力标记的契约测试"而非传统 mock：

```go
// contract_test.go 扩展
func RunContract(t *testing.T, s Storage) {
    caps := s.Capabilities()
    
    t.Run("Put/Get/Delete", testPutGetDelete(s)) // 所有后端必须
    t.Run("List", testList(s))                     // 所有后端必须
    
    if hasCap(caps, CapPresign) {
        t.Run("Presign", testPresign(s))
    }
    if hasCap(caps, CapMultipart) {
        t.Run("Multipart", testMultipart(s))
    }
    if hasCap(caps, CapServerSideCopy) {
        t.Run("ServerSideCopy", testServerSideCopy(s))
    }
}
```

这种方式比 "try-fail-skip" 更明确：测试报告清晰显示哪些能力被测试、哪些被跳过。

### 4.3 自建 vs 采购/集成

对于扩展中涉及的组件：

| 组件 | 建议 | 理由 |
|------|------|------|
| 事件持久化 | **自建**（SQLite 表初始，Kafka 可选升级） | 事件模型简单（`repository.Event`），不需要复杂流处理 |
| 多后端路由 | **自建** | `TieredRouter` 是逻辑层而非基础设施，无现成的 Go 库可复用 |
| SSE 过滤 | **自建** | 过滤谓词逻辑简单（事件字段匹配），不需要规则引擎 |
| 智能节流 | **自建** | 滑动窗口 + 错误率阈值判断，50 行代码即可实现 |
| Grafana Dashboard | **直接使用 Prometheus 数据源** | 已有 OTel 指标导出，无需额外采集器 |

---

## 5. 实施路线图

### 5.1 整体优先级矩阵

综合**技术依赖关系**、**业务影响**、**风险等级**后，我建议的优先级如下：

```
P0（必须优先完成）：无
P1（本季度）：    方向五 → 方向一 → 方向二
P2（下季度）：    方向三 → 方向四
P3（备选）：      方向六 → 方向七
```

**与 v92 文档的差异：** 我将方向二（SSE 过滤）从 P2 提到 P1 前提——如果已知近期有多租户大规模部署计划。否则保持 P2。我同时将方向三和方向四放在同一优先级，但方向四建议先于方向三如果产品侧有明确的跨协议一致性需求。

### 5.2 阶段划分

#### 阶段一：基础能力层重构（预计 5-8 天）

**包含：方向五（存储能力契约） + 方向一的前置配置变更**

| 步骤 | 任务 | 产出 | 依赖 |
|------|------|------|------|
| 1.1 | 定义 `Capability` 枚举 + `Storage.Capabilities()` 接口 | 各后端静态声明能力 | 无 |
| 1.2 | 各后端实现：`LocalStorage`、`S3Storage`、`OSSStorage`、`COSStorage` 返回能力集 | 4 份实现 | 1.1 |
| 1.3 | 扩展契约测试：按能力标记动态选择子测试 | `contract_test.go` 能力感知 | 1.2 |
| 1.4 | 优化 `copyObject`：检查 `CapServerSideCopy` → 使用服务端拷贝 | 消除 IO 放大 | 1.2 |
| 1.5 | 配置格式变更：支持 `STORAGE_BACKENDS_JSON` 复数格式 | 配置兼容 + 迁移 | 1.1 |
| 1.6 | `TieredRouter` 基础实现：构造 + 默认路由 | 可运行的多后端路由 | 1.5 |

**里程碑 M1：** `make check` 全绿，`copyObject` 不再全量读入内存，支持多后端配置声明

#### 阶段二：多后端路由与使用（预计 5-7 天）

**包含：方向一的剩余部分 + `storage_class` 路由激活**

| 步骤 | 任务 | 产出 | 依赖 |
|------|------|------|------|
| 2.1 | `RouteStrategy` 接口 + `StorageClassStrategy` 实现 | 按存储类路由 | 1.6 |
| 2.2 | `Object.BackendID` 列 + 迁移文件 | schema 变更 | 1.5 |
| 2.3 | `FileService` 改造：写路径按策略路由、读路径按 `BackendID` 寻址 | 全路径多后端 | 2.1, 2.2 |
| 2.4 | backfill Job：存量数据 `BackendID` = 默认后端 | 数据一致 | 2.2 |
| 2.5 | 跨后端 List 合并 | list 准确性 | 2.3 |

**里程碑 M2：** 对象可按 `storage_class` 路由到不同后端，读/写/列全路径验证通过

#### 阶段三：SSE 过滤 + 作业可观测性（预计 5-8 天）

**包含：方向二 + 方向三**

| 步骤 | 任务 | 产出 | 依赖 |
|------|------|------|------|
| 3.1 | `SubscribeWithFilter` 接口 + `FilterPredicate` | 过滤订阅 | 无（与阶段一二并行） |
| 3.2 | SSE handler 传递 URL query 参数作为谓词 | 客户端可用过滤 | 3.1 |
| 3.3 | 相同谓词的订阅者共享通道 | 性能优化 | 3.1 |
| 3.4 | 作业耗时直方图 + 队列深度仪表盘 | Prometheus/Grafana 面板 | 无 |
| 3.5 | 暂停/恢复/取消 admin 端点 | admin API 扩展 | 无 |
| 3.6 | 滑动窗口智能节流 | 动态 backoff | 无 |

**里程碑 M3：** SSE 连接减少 80%+ 无意义流量，作业可观测性达到生产标准

#### 阶段四：跨协议命名空间统一（预计 5-8 天）

**包含：方向四**

| 步骤 | 任务 | 产出 | 依赖 |
|------|------|------|------|
| 4.1 | S3 ListObjects 过滤 `application/x-directory` 对象 | S3 客户端不再看到标记对象 | 无 |
| 4.2 | WebDAV `RemoveAll` 递归删除子对象 | 删除语义一致 | 无 |
| 4.3 | REST /v1/folders 统一使用虚拟目录推导（减少标记对象创建） | 新目录不创建标记对象 | 无 |
| 4.4 | 可选 backfill：重建时无标记对象的目录标记 | 数据整洁 | 4.3 |

**里程碑 M4：** 三种协议的文件夹操作行为一致，存量数据兼容

### 5.3 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| **R1：** 多后端配置格式选择不当，后期无法扩展 | 中 | 高 | 使用 JSON 而非环境变量单值，JSON schema 可版本化；启动时做全量配置校验 |
| **R2：** `BackendID` backfill 期间新对象写入与旧对象读取路径不一致 | 中 | 中 | backfill 前不允许非默认后端的写入；backfill Job 使用软锁防止并发冲突 |
| **R3：** SSE 过滤性能下降（谓词评估成为瓶颈） | 低 | 中 | 谓词评估在 broadcast goroutine 而非 Publish goroutine；预编译 FilterPredicate |
| **R4：** 跨后端 List 分页游标无法统一排序 | 中 | 高 | 方案 A：要求各后端 key 排序一致（都是字典序）；方案 B：聚合后内存排序 + 游标编码（如 `backendA:lastKey`） |
| **R5：** 方向四的标记对象清理影响正在使用的 S3 客户端 | 中 | 高 | 不要在阶段四一次性改变 S3 ListObjects 行为，采用 feature flag 逐步推送：先增加 `?show_markers` 参数做兼容，默认过滤 |
| **R6：** 多个方向并行开发导致互相阻塞 | 高 | 中 | 严格按照依赖顺序排期；方向二/三/四与方向五并行不悖（无代码冲突）；方向一必须等待方向五完成 |

### 5.4 建议的追踪方式

每个方向创建独立的 ADR 记录到 `docs/adr/` 目录：

```
docs/adr/
├── 0005-storage-capability-contract.md      # 方向五
├── 0006-multi-backend-orchestration.md       # 方向一（依赖 0005）
├── 0007-sse-subscription-filter.md           # 方向二
├── 0008-job-observability-control-plane.md   # 方向三
├── 0009-cross-protocol-namespace.md          # 方向四
```

每个 ADR 记录：**Context → Decision → Consequences → Status**。

---

## 总结

v92 分析文档识别的 5 个方向合理且相互独立，我在其基础上补充了：

1. **架构评估**：识别了 6 项架构债务，其中 `copyObject` 全量读入内存（D2）和 `StorageClass` 未路由（D1）是最需优先解决的
2. **额外方向**：事件持久化与回放（方向六）和多租户存储 QoS（方向七），后者为 P3 备选
3. **接口设计**：推荐 `Capabilities() []Capability` 枚举而非单个 bool 方法，`TieredRouter` + `RouteStrategy` 策略模式，以及 `SubscribeWithFilter` 向后兼容签名
4. **技术选型**：保持 stdlib 优先原则，唯一可能需要评估的新依赖是 Kafka/Redis Streams（方向六，P2/P3，非近期）
5. **四阶段路线图**：从能力契约（~2-3天）→ 多后端路由（~5-7天）→ SSE+作业（~5-8天）→ 命名空间统一（~5-8天），每阶段有清晰的里程碑和风险缓解策略

**建议立即开始的方向五（存储能力契约）**——它独立于其他方向、改动量最小（~2-3 天）、消除 `copyObject` IO 放大的收益立即可见，且为方向一铺平道路。
