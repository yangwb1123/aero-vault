# 架构师分析：`expansion-v94-architectural-white-spaces.md` 评审

---

## 1. 架构评估

### 1.1 当前架构的核心优势

从评审文档反推，`aero-vault` 的架构展现出几个经过实战检验的设计特质：

| 特质 | 证据 | 架构意义 |
|------|------|---------|
| **分层隔离严格** | 业务逻辑（FileService）与协议适配器（REST/S3/MCP/WebDAV）严格分离 | 新增协议适配器不影响核心业务逻辑；可按协议独立演进版本契约 |
| **Opt-in 安全默认** | AI/Vector/Event/WebDAV 全部 flag-gated，`nil` 组件不破坏 CRUD 基线 | 基线路径的测试复杂度是 O(1) 而非 O(n)——这是大型系统长期可维护性的关键指标 |
| **持久化层抽象** | Storage（local/s3/oss/cos）与 Repository（SQLite/Postgres）各自有接口契约 | 存储后端的替换不影响查询层，反之亦然。但 `factory.go` 的硬编码 switch 破坏了此抽象的完整性 |
| **观测性基础设施** | OTel + Prometheus 15 instruments + 12-panel Grafana dashboard | 系统具备可调试性，这是准入控制、缓存等性能优化的前提条件 |

### 1.2 架构债务与技术债

评审文档揭示了 3 类架构债务，按影响面排序：

#### 债务一：组件注册机制退化（严重度 🔴）

**表现：** `internal/storage/factory.go` 的硬编码 switch-case，`internal/auth/auth.go` 的 Registry 硬编码。

**根因分析：** 这是一个典型的**抽象泄漏**——Storage/Repository 层有接口定义，但实例化仍通过中央 switch 分发。每次新增后端需要修改 `factory.go`，违反了 OCP（开闭原则）。更重要的是，这阻碍了第三方扩展的可行性，因为 `main.go` 的装配函数不可被外部包覆盖。

**影响面：**
- 新增 storage backend → 修改 4 处文件（factory.go / config.go / main.go / 测试）
- 无法实现按需裁剪二进制体积（所有后端代码被静态链接）
- 社区贡献的门槛高——需要理解 `main.go` 的装配逻辑才能添加新后端

#### 债务二：中间件链的刚性（严重度 🟠）

**表现：** `RequestID→CORS→Auth→Tenant→RateLimit→OTel→Recoverer→AccessLog` 作为固定顺序写入代码。

**根因分析：** 此链的固定顺序在绝大多数场景下正确，但违反了 "策略与机制分离" 原则。WebDAV 绕过中间件链（评审 v55 标注[^1]）就是一个信号——当某个适配器需要不同的中间件顺序时，当前架构只能通过"绕过"而非"配置"来解决。

**影响面：**
- 无法为不同协议配置独立中间件链（S3 可能需要更宽松的 CORS，MCP 可能不需要 CORS）
- 中间件的粒度不可调整（无法为 `PUT /large-files` 跳过某些中间件以降低延迟）

#### 债务三：事件系统缺乏持久化保证（严重度 🟠）

**表现：** `internal/events/bus.go:34` buffer=64 的 channel-backed event bus，无持久化、无重放、无消费者偏移管理。

**根因分析：** 此设计适用于"尽力而为"的事件通知（如 Webhook 触发），但作为系统内部的"状态变更持久记录"则不可靠。当前事件系统本质上是 in-memory pub/sub，不是 event store。

**影响面：**
- 系统崩溃导致事件丢失——Worker（AV/Replication）可能错过关键事件
- 无法追溯"对象何时被谁修改"——审计日志目前只记录 admin 操作，对象操作无轨迹
- Webhook 重试依赖内存队列——重启后重试状态丢失

[^1]: 评审文档方向二标注了 WebDAV 绕过中间件链的详细信息，引用了 v55 分析。这是一个架构信号：当适配器需要通过"绕过"而非"配置"来实现差异时，中间件架构存在刚性。

---

### 1.3 关键设计决策的合理性评估

| 决策 | 合理性 | 评审建议 |
|------|--------|---------|
| FileService 作为唯一控制器，禁止协议层直连存储 | ✅ 正确 | 无异议 |
| SQLite + local FS 作为默认基线 | ✅ 正确 | CI gate 的唯一验证路径，降低测试环境复杂度 |
| AI 管道分 5 阶段（Extract→Chunk→Embed→Index→Search） | ✅ 正确 | 每阶段可独立替换，符合 pipeline 模式 |
| 中间件链固定顺序 | ⚠️ 可接受 | 短期 OK，长期应考虑协议感知中间件配置 |
| `factory.go` 硬编码 switch | ❌ 错误 | 应在 Phase 2 重构为注册模式 |
| in-memory event bus | ⚠️ 可接受 | 适用于当前 scope（Webhook 触发），但不满足未来的审计/溯源需求 |

---

## 2. 高价值架构扩展方向

基于评审文档的分析，我识别出 5 个高价值扩展方向。与评审文档的五方向有重叠，但从架构视角做了重新排序和深化。

### 方向 A：声明式组件注册与插件框架（P0）

#### 为什么需要

这是当前架构中影响面最广的技术债。Storage backend、Auth provider、AI component、Event sink 全部通过硬编码 switch/main.go 装配。这不仅限制了扩展性，还使得：

- 第三方无法在不 fork 项目的前提下集成自定义后端
- 配置验证逻辑分散在 `config.go` 和 `main.go` 中，无统一校验点
- 二进制体积包含所有后端代码，无法按需裁剪

#### 核心挑战

1. **生命周期管理**：插件的初始化顺序、依赖声明、优雅关闭、热加载/热卸载的边界条件
2. **版本兼容性**：插件编译时的接口版本 vs 宿主运行时的接口版本——Go 的实现限制（Go plugin 要求完全一致的 build environment）意味着需要探索替代方案（WASM 或 gRPC sidecar）
3. **配置一致性**：每个插件有自己的配置 schema，需要运行时校验 + 文档自动生成

#### 预期的架构变更

```
当前：
main.go → buildStorageFrom(config) → switch config.Backend → factory.NewXXX()

目标：
main.go → registry.Resolve("storage", config.Backend) → plugin.Init(config)
         ↓
   每个实现包：func init() { registry.Register("storage", "minio", &MinioFactory{}) }
```

#### 对现有系统的影响

- **向后兼容性**：旧配置格式需要兼容层——`factory.go` 中的 switch 代码应在过渡期内保留为 fallback
- **测试影响**：单元测试需要 mock registry，而不是 mock factory——对现有测试的修改量可控
- **配置热加载**：如果集成 v89 方向一的配置热重载，插件配置变更的处理逻辑要明确（重启插件？拒绝变更？）

---

### 方向 B：持久化事件溯源基础设施（P0）

#### 为什么需要

评审文档方向四指出了 5 个结构性缺失：不可重放、不可逆、种类有限、审计分离、位置追踪。从架构视角看，缺失事件溯源的影响远超"审计合规"：

- **Replication Worker 的可靠性**：依赖 in-memory event bus 意味着节点重启后可能错过 replication 事件——这导致跨区复制的最终一致性延迟不可预测
- **对象血缘追踪**：当前 GET `/v1/lineage/objects/{id}` 的实现需要反向推断对象关系——如果有事件日志，血缘可直接重放事件流计算
- **调试与问题排查**：没有"发生了什么"的不可变记录，生产问题排查只能依赖日志（非结构化）和当前状态（已不可逆）

#### 核心挑战

1. **写入吞吐 vs 一致性**：Event log 是系统的"真相源"，但写入延迟会直接影响文件操作的响应时间。评审建议的批量 INSERT（100ms/100条）可行，但 backpressure 信号需要谨慎设计
2. **事件类型演化**：评审建议 8-10 个核心类型起步，但需要前瞻性设计 `EventVersion` 字段，确保消费者可以按版本路由到对应的反序列化逻辑
3. **存储成本**：事件日志是 append-only，无限增长。需要分层存储策略（近期在可写表，远期在只读压缩表或对象存储）

#### 预期的架构变更

```
当前：
FileService → eventBus.Publish(event)  // in-memory, best-effort

目标：
FileService → eventStore.Append(event) → eventBus.Publish(event)  // 双写
                                                    ↓
                                            event_log (持久化)
                                            events (内存广播)
                                                    ↓
                                            consumer_offsets (消费者进度)
```

#### 对现有系统的影响

- **Worker 改造**：AV/Replication 从监听内存 channel 改为从 `consumer_offsets` 拉取——这是主要改造量
- **FileService 事务边界**：当前 FileService 的 DB 操作与事件发布不是原子性的。事件溯源要求 "DB commit 成功后发布事件"——这意味着事件发布在事务提交后的回调中执行

---

### 方向 C：多层缓存体系与韧性控制（P1）

#### 为什么需要

评审文档方向一的分析指出，FileService（L1）与 Storage（L2）之间的缓存缺失是性能瓶颈。从系统整体视角看：

- **对象读取的延迟分布**：L1 缓存命中 ≈ 10μs（进程内 map lookup），L2 缓存命中 ≈ 1ms（本地 disk read），L3 缓存命中 ≈ 5ms（本地 SSD），cache miss ≈ 50-500ms（S3 API call）。优化缓存命中率可显著降低 P95 延迟。
- **Range 请求的优化潜力**：大文件的 Range 请求在当前架构中需要完整读取后切片——这是显著浪费。热前缀缓存（前 64KB）可优化 90%+ 的 Range 请求（典型场景：HTTP range header 用于视频 seek 或日志 tail）。

#### 核心挑战

1. **缓存一致性与 SSE 加密的交互**：评审已正确识别 SSE 加密的缓存时机问题。核心原则是：加密发生在 Storage 层（`storage.Storage` 接口的实现内），所以 L1/L2 缓存中存储的是加密后的 blob。这意味着缓存命中时需要解密。但解密后的明文不应缓存（敏感数据泄露风险）。
2. **缓存雪崩防护**：批量缓存过期（例如大量对象同时写入导致 TTL 到期）会导致所有请求穿透到 Storage 层。需要 singleflight（同 key 只穿透一个请求）和 probabilistic early expiration（在 TTL 到期前随机提前刷新）。
3. **内存上限管理**：L1 缓存在多租户场景下可能会导致租户间资源竞争。需要 per-tenant quota 或优先级队列。

#### 预期的架构变更

```
L1: sync.Map (in-process) — 对象体缓存，≤4MB，LRU
    ↑  singleflight 防止惊群
L2: freecache / groupcache (local process) — 元数据缓存 + 热前缀，TTL 60s
    ↑  probabilistic early expiration
L3: Redis (跨进程共享) — 可选，用于多副本场景
    ↓ fallback
Storage (local/S3/OSS/COS)
```

#### 对现有系统的影响

- **FileService 变更**：`Get` 和 `Range` 方法需要包装缓存逻辑，这违反了 FileService 的纯业务职责。建议将缓存抽取为 `CacheMiddleware` 装饰器模式，而非直接嵌入 FileService。
- **对象写入处理**：写入操作需要同步或异步使缓存失效。Write-Through（写穿透）模式的一致性最好但写入延迟增加。Write-Behind（写回）模式延迟低但存在数据丢失窗口。

---

### 方向 D：协议感知的中间件编排（P1）

#### 为什么需要

当前固定顺序中间件链的刚性已通过 WebDAV 绕过中间件链（评审 v55 标注）暴露。随着协议适配器增加（MCP 成熟、WebDAV 完善、未来可能出现 FUSE 或 NFS 适配器），"一刀切"的中间件策略越来越不可持续。

#### 核心挑战

1. **顺序依赖的显式建模**：某些中间件存在硬依赖（Auth 必须在 Tenant 之前，因为 Tenant 从 JWT/Key 中提取 tenant）。中间件的声明式配置需要表达这些依赖关系。
2. **性能开销**：每个请求遍历中间件链的代价不可忽略（REST handler 有 8 层中间件）。对于不需要 Auth 的端点（如 `/healthz`），跳过不必要的中间件可降低延迟。
3. **中间件配置的协议粒度**：是每路由配置还是每协议配置？S3 协议要求全程 SigV4 验签，MCP 要求 JSON-RPC 请求体解析——这些需要在协议入口处分发。

#### 预期的架构变更

```
当前：
chi.NewRouter().Use(globalMiddlewareChain)

目标：
type ProtocolConfig struct {
    Name      string
    Prefix    string    // /v1, /s3, /mcp, /webdav
    Middleware []MiddlewareDescriptor
    Router    chi.Router
}
```

#### 对现有系统的影响

- **router.go 重构**：当前 `router.go` 在 REST 分组内注册所有 handler，S3/MCP/WebDAV 独立注册。建议改为协议工厂模式——每个协议适配器返回 `ProtocolConfig`，包含路由表和中间件配置。
- **WebDAV 修复**：WebDAV 目前绕过中间件链，重构后应集成到统一框架中。

---

### 方向 E：OpenAPI 规范驱动开发与 SDK 生成（P2）

#### 为什么需要

评审文档方向五正确指出 SDK 功能不对称的问题（`list_files` vs `write_file` 的显著差异）。从架构视角看，根本原因是 **API 规范（OpenAPI）与实现（Go handler）之间的强制一致性机制缺失**。

当前状态：
1. Go handler 是真相源（truth source）——OpenAPI 是手动维护的副本
2. SDK 的手动维护导致 14 个 REST handler 的 SDK 覆盖不均
3. 多语言 SDK 的维护成本随语言数量线性增长

#### 核心挑战

1. **OpenAPI 生成 vs 代码生成的对称性**：评审已正确指出 `code-first` 和 `spec-first` 两条路径的选择。对于现有项目（已有完整 Go handler），code-first（从 Go handler 生成 OpenAPI）是低风险路径。对于新 API，spec-first（从 OpenAPI 生成 Go handler stub + SDK）可保证规范与实现的一致。
2. **流式 API 的 OpenAPI 表达**：SSE（`/v1/chat/stream`）在 OpenAPI 3.1+ 中可通过 `text/event-stream` 描述，但主流代码生成工具支持度不一。这导致流式 API 的 SDK 生成需要特殊处理。
3. **SDK 版本与 API 版本的协调**：评审已隐含提及——多语言 SDK 的版本跳升需要协调。一个发布框架（Release Please 或类似工具）是必要的。

#### 预期的架构变更

```
当前：
Go handler → 手动 OpenAPI → 手动 SDK

目标：
Go handler → openapi-gen（Go 注解）→ OpenAPI spec（truth source）
                                           ↓
                                   oapi-codegen → Go SDK（stub → 手动填充）
                                   openapi-generator → Python/JS/Go SDK
```

#### 对现有系统的影响

- **Handler 注解成本**：Go handler 需要 `// @Summary` `// @Param` 等注解（swagger 风格或 `ogen` 的注解风格），这是主要的工作量。但这是**一次性投资**——后续 API 变更自动生成 OpenAPI。
- **SDK 迁移路径**：现有手动 SDK 不应立即替换。建议 Phase 1：生成 OpenAPI spec，与手动维护的 spec 做 diff 验证。Phase 2：生成 stubs，与手动 SDK 共存。Phase 3：手动 SDK 废弃。

---

## 3. 接口设计建议

### 3.1 核心原则

基于评审文档揭示的架构问题，我建议采用以下接口设计原则：

#### 原则一：接口应服务消费者，而非实现者

当前的 `Storage` 接口是合理的（`Get/Put/Delete/List`），但 `EventBus` 接口（`Publish/Subscribe`）不满足消费者需求——消费者需要 `Consume(offset, handler)` 而非 `Subscribe(handler)`。接口的抽象级别应由其使用场景决定：

```go
// 当前（实现者友善）
type EventBus interface {
    Publish(ctx context.Context, event Event) error
    Subscribe(topic string, handler EventHandler)
}

// 建议（消费者友善）
type EventStore interface {
    Append(ctx context.Context, event Event) (offset int64, err error)
    Read(ctx context.Context, fromOffset int64, limit int) ([]Event, error)
    ConsumerRegister(ctx context.Context, consumer string) (currentOffset int64, err error)
    ConsumerCommit(ctx context.Context, consumer string, offset int64) error
}
```

#### 原则二：组件注册接口应支持声明式配置

当前 `factory.go` 的 switch 使得配置解析与实例化耦合。注册模式应将配置 schema 作为一等公民：

```go
type ComponentFactory interface {
    // Type 返回组件类型标识（"storage"/"auth"/"ai.embedder"）
    Type() string
    // Name 返回组件名称（"local"/"s3"/"minio"）
    Name() string
    // ConfigSchema 返回 JSON Schema，用于配置校验和自动生成文档
    ConfigSchema() json.RawMessage
    // Create 接收已验证的配置，创建组件实例
    Create(ctx context.Context, cfg json.RawMessage) (interface{}, error)
    // Close 优雅关闭
    Close(ctx context.Context) error
}
```

#### 原则三：接口应具有明确的失败语义

Go 的 `(result, error)` 模式在分布式系统中不够。Storage 层的失败需要区分"可重试"和"不可重试"：

```go
// 建议
type StorageError struct {
    Kind    ErrorKind  // Transient / Permanent / NotFound / AuthRequired
    Message string
    Wrapped error
}

type Storage interface {
    Get(ctx context.Context, key string, opts ...GetOption) (io.ReadCloser, *ObjectMeta, error)
    // error 如果不是 *StorageError，视为永久失败（测试要求）
    Put(ctx context.Context, key string, reader io.Reader, opts ...PutOption) (*ObjectMeta, error)
    Delete(ctx context.Context, key string, opts ...DeleteOption) error
    List(ctx context.Context, prefix string, opts ...ListOption) ([]ObjectMeta, string, error)
}
```

### 3.2 是否需要新的抽象层

| 领域 | 当前缺失去 | 建议引入层 | 原因 |
|------|-----------|-----------|------|
| 缓存 | 无统一缓存接口，FileService 内嵌简单 map | `Cache[K, V any]` 接口 | 支持 L1/L2/L3 透明互换，便于测试和 benchmark |
| 准入控制 | 3 个独立的限流器 | `AdmissionController` | 统一协调并发 + 速率 + 断路器反馈 |
| 事件 | in-memory channel | `EventStore` | 持久化 + 重放 + 消费者管理 |
| 组件注册 | factory.go switch | `Registry` | 声明式注册 + 配置校验 + 生命周期管理 |

### 3.3 向后兼容性策略

1. **接口进化原则**：使用语义版本化的接口标记——`StorageV1` 接口不破坏性变更，新增功能通过 `StorageV1Ext` 扩展接口或 Option 模式
2. **告警周期**：接口或配置的破坏性变更应有明确的 deprecation warning 周期（≥ 2 个 minor 版本）
3. **默认值保持**：新抽象层引入时，默认行为应与当前行为一致（即引入 `EventStore` 后，默认实现仍然使用 in-memory channel 作为 backplane）

---

## 4. 技术选型

### 4.1 需要引入的新技术栈

| 领域 | 候选方案 | 推荐 | 理由 |
|------|---------|------|------|
| **事件存储** | PostgreSQL（现有）/ Kafka / NATS JetStream / SQLite（现有） | **PostgreSQL**（Phase 1）→ **NATS JetStream**（Phase 2） | Phase 1 复用现有 `DB_DRIVER` 栈，零新依赖；Phase 2 满足高吞吐 + 消费者组 + 持久化订阅 |
| **进程内缓存** | freecache / bigcache / groupcache / 自建 LRU | **freecache**（零 GC 压力） | `aero-vault` 的对象体平均大小不可预期，freecache 的 zero-GC 设计适合大缓存条目。groupcache 适合多副本场景，但增加了依赖复杂度 |
| **OpenAPI 代码生成** | `oapi-codegen` / `ogen` / `swag` → `openapi-generator` | **混合**：code-first 用 `ogen`，SDK 用 `openapi-generator` | `ogen` 支持 `//go:generate` 注释，与现有 Go 代码集成度高；多语言 SDK 需要 `openapi-generator` |
| **WASM runtime** | wazero / wasmtime-go | **wazero**（纯 Go，零 CGO） | 如果选择 WASM 作为插件沙箱，wazero 无需 CGO，与 `aero-vault` 的零 CGO 目标兼容。但此方案仅推荐在 Runtime Plugin 场景 |

### 4.2 第三方依赖的评估标准

| 标准 | 权重 | 说明 |
|------|------|------|
| Go 版本兼容性 | 🔴 必须 ≤ 1.25 | 项目使用 Go 1.25，依赖必须兼容 |
| CGO 零依赖 | 🔴 必须 | 当前项目零 CGO，新增依赖不得引入 |
| 许可证兼容性 | 🟠 必须 | Apache 2.0 或 MIT 优先，AGPL 禁止 |
| 测试覆盖率 | 🟠 重要 | 依赖自身的测试覆盖应 ≥ 70% |
| 维活度 | 🟡 参考 | 关注 star 数、最近 commit、issue 响应速度 |
| 二进制体积影响 | 🟢 低优先级 | 但 `init()` 注册模式可支持按需裁剪 |

### 4.3 自建 vs 采购的决策框架

| 场景 | 建议 | 决策依据 |
|------|------|---------|
| 事件溯源 | **Phase 1 自建**（复用 PostgreSQL），**Phase 2 考虑 NATS** | 当前 event bus 已有基本抽象，复用现有 DB 可快速验证模式。使用 NATS（采购）仅当吞吐量超过 PG 的单机瓶颈时 |
| 插件系统 | **自建** | Go plugin 生态不成熟，WASM 方案处于早期。自建 `init()` 注册 + interface 契约可覆盖 90% 的场景，且无外部依赖 |
| OpenAPI 代码生成 | **采购**（openapi-generator / ogen） | 这是成熟工具生态，自建代码生成器工作量巨大且与标准脱节 |
| 缓存 | **自建 + 小型三方库**（freecache） | 缓存逻辑是业务特定（SSE 感知、Range 优化），只有底层数据结构需要三方库 |

---

## 5. 实施路线图

### 5.1 优先级排序

| 优先级 | 方向 | 依赖 | 预估工期 | 风险等级 |
|--------|------|------|---------|---------|
| **P0** | B. 事件溯源基础设施 | 无（复用现有 DB 栈） | 3-4 周 | 🟢 低 |
| **P0** | A. 声明式组件注册 | 无 | 2-3 周 | 🟡 中 |
| **P1** | C. 多层缓存 | A（需要缓存作为组件注册） | 3-4 周 | 🟡 中 |
| **P1** | D. 协议感知中间件 | A（中间件也需要注册机制） | 3-5 周 | 🔴 高 |
| **P2** | E. OpenAPI 规范驱动 | D（中间件稳定后才解 handler） | 5-8 周 | 🟡 中 |

### 5.2 阶段划分与里程碑

#### Phase 0（2 周）——基础设施重构准备

**目标：** 为后续变更建立测试和安全网

```
- 为 factory.go switch 添加集成测试覆盖（storage contract test）
- 为 event bus 添加行为测试（消息丢失检测）
- 建立基准 benchmark（对象 CRUD 延迟基线）
- 配置 ROADMAP.md 与 docs/agent 自动检查流程的一致性
```

**里程碑 M0：** `make check` + `make bench` 通过，基线数据记录

#### Phase 1（4 周）——事件溯源 + 组件注册

**目标：** 解决两个最严重的架构债务

```
Week 1-2: 事件溯源基础设施
  - event_log 表迁移（SQLite + Postgres）
  - EventStore 接口定义
  - FileService 集成：DB 事务提交后 Append
  - consumer_offsets + 第一个消费者（Replication Worker 改造）
  - 8 个核心事件类型（EventCreated/Deleted/Updated/Moved/Locked/Tagged/BucketCreated）

Week 3-4: 声明式组件注册
  - Registry 定义 + PluginCapabilities 接口
  - Storage backend 迁移（local/s3/oss/cos → init() 注册）
  - 配置兼容层（旧 config 格式 → 新注册机制）
  - Auth provider 迁移
  - AI component 迁移（embedder/llm/reranker）
```

**里程碑 M1：** 所有内置后端通过 `init()` 注册完成 + 事件日志覆盖核心 CRUD 操作

#### Phase 2（4 周）——多层缓存 + 准入控制

**目标：** 性能优化层

```
Week 1-2: 缓存抽象
  - Cache[K,V] 接口 + 指标（hit/miss/eviction/memory）
  - L1 freecache 集成（对象体 ≤ 4MB）
  - singleflight 防惊群
  - SSE 加密感知（缓存加密 blob）
  - Range 请求热前缀缓存（前 64KB）

Week 3-4: 准入控制重构
  - AdmissionController 统一 3 个限流器
  - circuitBreaker 状态 → admission 权重反馈
  - 分级准入（租户优先级）
  - 梯度恢复（对齐断路器 half-open）
```

**里程碑 M2：** 缓存命中率 ≥ 70%（基于 Phase 0 基准） + 准入控制统一

#### Phase 3（5 周）——中间件编排 + OpenAPI

**目标：** 接口治理层

```
Week 1-2: 协议感知中间件
  - ProtocolConfig 定义
  - 每个协议适配器返回中间件配置
  - WebDAV 重新纳入中间件链
  - 配置热加载（已存在的 v89 基础设施）

Week 3-5: OpenAPI 规范驱动
  - 选择 ogen 作为 code-first 生成器
  - Go handler 添加注解
  - OpenAPI spec 自动生成 + 手动 diff 验证
  - openapi-generator 生成 Go SDK stub
  - Python SDK 试点（ReadFile/ListFiles/Search 三个方法）
```

**里程碑 M3：** WebDAV 不再绕过中间件链 + OpenAPI spec 与实现一致（diff 检测）

### 5.3 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| 事件溯源增加写入延迟 | 🟡 中 | 🟡 中 | 批量写入 + backpressure 降级；Phase 0 benchmark 提供基线对比 |
| `init()` 注册顺序不确定性 | 🟢 低 | 🟡 中 | 在 registry 中显式声明依赖（Before/After 顺序约束）；测试中统一调用 `registry.Reset()` 避免测试间污染 |
| 缓存引入数据一致性问题 | 🟡 中 | 🔴 高 | Write-Through 作为默认模式（强一致性）；缓存只有在场景明确需要（如大规模只读工作负载）时才切换为 Write-Behind |
| WASM 插件方案不成熟 | 🟡 中 | 🟢 低 | Phase 1 不引入 WASM——使用 `init()` 自注册（内置后端）+ gRPC sidecar（外部插件）。WASM 仅作为调研方向 |
| OpenAPI 生成覆盖不完整 | 🔴 高 | 🟠 中 | 分阶段推进：Phase 1 只覆盖非流式 REST API；Phase 2 覆盖 SSE 流式；Phase 3 覆盖 S3 子资源。SDK 自动生成从 20% 逐步提升 |
| 中间件编排导致性能回归 | 🟡 中 | 🟠 中 | Phase 0 建立基准后，每次重构后对比 `wrk` 或 `hey` benchmark。允许 5% 以内的性能回归，超出则回退 |

---

## 6. 总结

这份架构评审文档在**代码锚点精度**和**架构权衡分析**方面质量优秀。我发现的 2 处去重漏检（方向三 vs v18 方向四、方向五 vs v84 方向一）虽然影响新颖性声明，但不减损文档本身的分析质量。

从架构视角看，`aero-vault` 当前处于一个**架构分岔点**：

- 短期的"安全路径"是修补——在现有框架内增加缓存、调整准入控制、修复 WebDAV 绕过
- 长期的"架构升级路径"是重构组件注册和事件系统——这是使得后续所有扩展（插件、溯源、缓存抽象）成为可能的基础设施

我建议采取**混合策略**：Phase 1 集中解决两大架构债务（事件溯源 + 组件注册），Phase 2-3 在此基础上叠加性能优化和接口治理。这样在 2 个月内，系统可从"可工作的 monolith"演进到"可扩展的模块化系统"，同时避免"一次性大重构"带来的风险。
