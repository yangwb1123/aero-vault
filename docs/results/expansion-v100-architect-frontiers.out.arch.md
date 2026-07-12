现在我对完整的上下文有了清晰理解。以下是基于这 5 项 grep 验证发现的结构化架构分析。

---

# 架构分析：桶级子系统的"配置完备-执行缺失"鸿沟

> **分析对象：** `docs/requirements/expansion-v99-dead-code-paths-and-governance-gaps.md` 的核心断言
> **验证方式：** 全库 grep 交叉检查（5/5 断言已确认）
> **视角：** 资深架构师

---

## 1. 架构评估

### 1.1 当前架构的优势

**强分层的协议适配器模式**是这些发现能够存在的前提——S3-compat 层、REST 层、持久化层各自独立完成本职工作：

- S3 handler 正确解析 XML、正确持久化配置
- Repository 层提供完整的 CRUD 接口
- 迁移文件覆盖 SQLite + Postgres 双后端

这恰恰说明分层架构在第一阶段（"写"路径）执行良好。问题出在第二阶段（"读"路径/执行路径）。

### 1.2 结构性架构债务

5 项发现指向同一个结构性模式：

| 子系统 | 配置接受 | 配置持久化 | 运行时消费 | 状态 |
|--------|---------|-----------|-----------|------|
| 桶通知规则 | ✅ S3 XML 解析 | ✅ JSON 列 | ❌ EventBus 广播忽略 | 断线 |
| 服务端访问日志 | ✅ S3 `?logging` | ✅ `logging_target` 列 | ❌ `WriteAccessLog` 空存根 | 断线 |
| 桶策略 | ✅ S3 `?policy` | ✅ JSON 列 | ❌ REST/WebDAV/MCP 不评估 | 半断线 |
| 生命周期转换 | ✅ `?lifecycle` | ✅ `expire_action` 字段 | ❌ 仅 `soft_delete`/`hard_delete` | 半实现 |
| 服务端拷贝 | ❌ 无接口 | — | ❌ 仅客户端流式 | 未实现 |

这是**架构层面的"写-读不对称"（Write-Read Asymmetry）**：写路径（配置接收→持久化）享有完整实现优先级，读路径（配置加载→运行时执行）被系统性延迟。

### 1.3 根因分析

这个模式的出现不是偶然的。它反映了：

1. **S3 协议优先的演进路径：** 先实现协议可观测性（能接受并保存配置），再补全运行时语义——这在 MVP 阶段是合理选择，但持续未闭环就形成债务。

2. **EventBus 的抽象层级过低：** `Bus.Publish` 直接 `broadcast` 给所有 subscriber，没有在总线上引入路由层或过滤器。每次新增 subscriber 隐含地承诺"全量事件"，但桶级过滤本应在总线层而非 subscriber 层实现。

3. **Storage 接口缺少 Copy 语义：** 当前接口设计的假设是所有数据移动都是客户端驱动的（Get → Put），没有表达"存储中介执行的操作"这一概念。这限制了服务端 COPY、服务端转换、存储类转换等能力的自然生长。

---

## 2. 高价值扩展方向

### 方向一：桶级策略执行引擎 — 补齐"配置→运行时"管线

**为什么需要：** 5/5 的发现都依赖同一个基础设施——"运行时，根据桶级配置执行动作"的通用引擎。没有这个引擎，每补齐一个功能（通知路由、访问日志、策略执行）都需重复造轮子。

**核心挑战：**
- 策略评估频率 vs 性能瓶颈（每请求查 DB 读取桶配置不可接受）
- 配置缓存一致性与更新时效性（S3 配置变更后何时生效）
- 不同策略的执行顺序与优先级（通知 vs 访问日志 vs 策略 vs 锁）

**建议的架构变更：**

```
┌────────────────────────────────────────────────┐
│              Bucket Config Cache                │
│  in-memory LRU: (tenant, bucket) → ConfigBundle │
│  包含: Policy/Logging/Notification/Lifecycle/Lock│
└──────────────────────┬─────────────────────────┘
                       │ 订阅配置变更事件
                       ▼
┌────────────────────────────────────────────────┐
│          Policy Execution Middleware             │
│  - 请求进入时加载缓存，评估 BucketPolicy         │
│  - 请求完成后调用 WriteAccessLog                 │
│  - 调用 EventBus.Publish (已携带规则)            │
└────────────────────────────────────────────────┘
```

**对现有系统的影响：** 低到中。需要：
- 新增 `ConfigBundle` 结构与缓存层（~200 行）
- 修改 `AccessLog` middleware 调用 `WriteAccessLog`（~30 行）
- 在 `EventBus.Publish` 签名中增加 `Bucket` 字段，出口端过滤（~50 行）
- 不需要改 migration 或 handler 层

**争议点：** 缓存放置在 middleware 层 vs Service 层？
- Middleware 层：对全协议生效（S3/REST/WebDAV/MCP统一受益），但 middleware 原则上不应持有业务逻辑
- Service 层：架构更纯净，但需确保所有协议入口都经过 FileService

**建议：** 在 Service 层实现 `FileService.loadBucketConfig(ctx, tenant, bucket) → *BucketConfigBundle`，middleware 通过 Service 间接调用。这保持 middleware 的协议无关性。

---

### 方向二：EventBus 路由能力引入 — 从广播到定向投递

**为什么需要：** 当前所有 subscriber 接收所有事件，导致：
- Antivirus Worker 收到仅为通知规则触发的事件
- Webhook 收到索引器跳过事件
- SSE 端点无法按桶过滤

**核心挑战：**
- 桶级过滤：subscriber 注册时声明感兴趣的事件类型+桶范围
- 匹配性能：每条事件 O(N) 匹配 subscriber 的过滤规则
- 向后兼容：所有现有 subscriber 在迁移期间继续全量接收

**建议的架构变更：**

```go
// 在 events 包中引入订阅过滤器
type SubscriptionFilter struct {
    TenantIDs []string          // 空 = 所有租户
    Buckets   []string          // 空 = 所有桶
    EventTypes []repository.EventType  // 空 = 所有类型
}

// Subscribe 增加过滤器参数
func (b *Bus) Subscribe(filter SubscriptionFilter) (<-chan repository.Event, func())
```

**对现有系统的影响：** 中等。
- 需要改 `Bus.Subscribe` 签名（所有调用点同步更新）
- 新增 `matchEvent` 过滤器（~60 行）
- 后续可对接 `NotificationRule` 使桶级通知自动路由

**争议点：** 过滤器在 Bus 端执行 vs Subscriber 端执行？
- Bus 端：减少 goroutine 上下文切换，但增加发布延迟
- Subscriber 端：零影响发布路径，但浪费 channel 带宽

**建议：** 两阶段过滤——Bus 做粗粒度（按tenant+bucket）过滤到 channel，subscriber 做细粒度（按事件类型+规则）筛选。

---

### 方向三：Storage 接口引入 Copy/Move 语义

**为什么需要：** 当前 `copyObject` 通过 Get→Put 实现，对于：
- 同后端同 bucket COPY：浪费 100% 带宽（S3 服务端 COPY 是零数据移动）
- 大对象（>5GB）：全量内存缓冲有 OOM 风险
- 存储类转换：无法表达"转换存储类但不移动内容"

**核心挑战：**
- 接口设计：Copy 是必选方法还是可选方法？
- 策略选择：同后端→服务端 COPY vs 跨后端→流式 COPY
- 原子性：Copy 应与存储后端的原子性保证对齐

**建议的架构变更：**

```go
type Storage interface {
    // 现有方法...
    SupportsCopy(ctx context.Context, srcKey, dstKey string) bool
    Copy(ctx context.Context, srcKey, dstKey string, opts CopyOptions) (ObjectInfo, error)
}
```

策略模式的选择逻辑：

```
同一后端 + 后端支持 ServerSideCopy → 调用后端 CopyObject API（零数据经过服务器）
同一后端 + 后端不支持 → 流式 Copy（但保持后端内）
跨后端 → 复用 Replication worker 模式（分块 Copy + 进度追踪 + 幂等）
```

**对现有系统的影响：** 中到高。
- Storage 接口新增 2 个方法（所有 backend 需实现或返回 fallback）
- S3 backend 可调用 AWS SDK `CopyObject`（~50 行）
- Local backend 继续用流式（已有代码）
- FileService 层新增 `CopyObject` 方法（~80 行）
- 需要改 `copyObject` handler 使用新接口

**争议点：** 引入 `Copy` 方法会打破无约定的接口契约（当前所有 backend 只需实现 Get/Put/Delete/List）。可选方案：
- **方案 A（激进）：** 直接加入接口 → 所有 backend 必须实现（可以 return `ErrNotImplemented` fallback 到流式）
- **方案 B（保守）：** 在 Service 层做策略判断，backend 通过接口断言或 `CopyCapabilities` 方法声明支持

**建议：** 方案 B。Storage 接口保留纯 CRUD 语义，新增可选的 `CopyCopable` 接口：

```go
type CopyCopable interface {
    Copy(ctx context.Context, srcKey, dstKey string, opts CopyOptions) (ObjectInfo, error)
}
```

Service 层：`if c, ok := store.(CopyCopable); ok { use server-side copy } else { fallback to stream }`

---

### 方向四：生命周期状态机 — 从二元删除到多层次转换

**为什么需要：** 存储成本优化是对象存储最直接的价值主张。无 `STANDARD→STANDARD_IA→GLACIER→DEEP_ARCHIVE` 转换路径，生命周期对成本数据库毫无意义。

**核心挑战：**
- 状态机表达：S3 生命周期是规则驱动的状态转换
- 执行时机：定时扫描 vs 事件驱动
- 非当前版本与未完成分片上传的处理
- 前端预热：冷存储恢复需要时间

**建议的架构变更：**

```
┌──────────────────────┐
│  Lifecycle Config    │  rule: { days: 30, action: "to_infrequent_access" }
└──────┬───────────────┘
       │ Reconcile 循环扫描
       ▼
┌──────────────────────┐
│  Lifecycle Executor  │  → 读取存储类映射 → 调用 Storage.Transition
└──────┬───────────────┘
       │
       ▼
┌──────────────────────┐
│  Storage.Transition  │  → 不同后端行为不同：
│                      │     S3: CopyObject + StorageClass 参数
│                      │     Local: 移动目录（或更新元数据）
│                      │     Noop: 仅更新 storage_class 字段
└──────────────────────┘
```

**对现有系统的影响：** 中。
- 需要扩展 `LifecycleRule` 结构体（新增 `to_infrequent_access`、`to_archive`、`to_deep_archive` 等动作）
- Reconcile 的 `sweepExpired` 需扩展为驱动状态机
- Storage 接口新增 `Transition(ctx, key, targetClass) error`
- REST/S3 API 层需解析 Transition 而非仅 Expiration

---

### 方向五：跨协议评估层的统一策略引擎

**为什么需要：** `checkBucketPolicy` 仅在 S3 handler 层执行，REST/WebDAV/MCP 完全绕过桶策略。这意味着多协议场景下安全模型不完整——用户通过 WebDAV 上传的文件不受桶策略约束，这是安全审计的重大隐患。

**核心挑战：**
- 策略语言是 S3 IAM 风格，不同协议的操作名映射不一致
- 性能：每请求策略评估的开销
- 缓存与传播：策略变更后多久生效

**建议的架构变更：**

将策略评估从 `s3compat/handler.go` 提升到 FileService 层或 middleware 层：

```
请求 → 协议适配器 → 中间件层（策略评估） → FileService
                          ↑
                    BucketPolicy Cache
```

操作映射表：

| S3 操作 | REST 操作 | WebDAV 操作 | MCP 操作 |
|---------|-----------|-------------|----------|
| s3:PutObject | files:write | PROPFIND | write_file |
| s3:GetObject | files:read | GET / GET range | read_file |
| s3:DeleteObject | files:delete | DELETE | delete_file |
| s3:ListBucket | files:list | PROPFIND | list_files |

**对现有系统的影响：** 中。
- 将 `policy.Eval` 从 S3 handler 上提至 middleware
- 新增操作映射表
- 修改 `middleware.go` 以集成策略评估
- 需要留意与现有 Auth middleware 的执行顺序（策略评估应在 Auth 之后、Tenant 解析之后）

---

## 3. 接口设计原则

### 3.1 新接口引入的指导原则

| 原则 | 含义 | 示例适用 |
|------|------|----------|
| **Optional Interface Assertion** | 接口增长通过可选接口断言（`if x, ok := store.(Copier); ok`），不强制所有实现 | Storage.Copy |
| **Config Bundle, Not Individual Configs** | 桶级配置作为聚合体读取，避免逐字段查 DB | 方向一的配置缓存 |
| **Action Before Storage** | 先定义执行语义再扩展存储接口 | 生命周期转换 |
| **Protocol-Neutral at Core** | 核心接口不暴露协议细节（无 XML、无 S3 操作名） | 策略引擎的协议映射层 |

### 3.2 需要引入的抽象层

**Bucket Config Cache：**
```go
type ConfigBundle struct {
    Policy         *BucketPolicy
    Logging        *BucketLogging
    Notification   []NotificationRule
    Lifecycle      *LifecycleRule
    ObjectLock     *ObjectLockConfig
    Versioning     VersioningConfig
}

type ConfigCache interface {
    Get(ctx, tenant, bucket string) (*ConfigBundle, error)
    Invalidate(tenant, bucket string)
    Subscribe() <-chan ConfigChangeEvent  // 配置变更通知
}
```

**Event Router（作为 EventBus 的增强）：**
```go
type EventRouter interface {
    Route(event repository.Event) []string  // 返回匹配的 subscriber IDs
    Register(subscriberID string, filter SubscriptionFilter)
    Unregister(subscriberID string)
}
```

**Storage Capabilities（作为 Storage 的元接口）：**
```go
type Capabilities struct {
    SupportsCopy           bool
    SupportsTransition     bool
    SupportsMultipart      bool
    SupportsServerSideCopy bool
    MaxCopySize           int64  // 0 = 无限制
}
```

### 3.3 向后兼容策略

1. **`Bus.Subscribe` 签名变更：** 将 `func Subscribe() <-chan Event` 改为 `func Subscribe(opts ...SubscribeOption) <-chan Event`，现有调用传空（全量订阅），不破坏编译。
2. **Storage 接口扩展：** 通过可选的 `CopyCopable` 接口断言，不修改现有 `Storage` 接口签名。
3. **AccessLog middleware 激活：** `WriteAccessLog` 当前是空存根，激活它时先通过 env flag `ACCESS_LOG_ENABLED` 控制，避免存量用户意外产生日志量爆炸。

---

## 4. 技术选型

### 4.1 是否需要引入新技术栈

| 方向 | 现有方案绰绰有余 | 可能需要引入 | 建议 |
|------|-----------------|-------------|------|
| 配置缓存 | ✅ 用 `sync.Map` + TTL 即可 | 需要外部分布式缓存（如 Redis）的场景下才引入 | 阶段一：进程内 LRU；阶段二：可选 Redis |
| Event 路由 | ✅ Bus 层新增订阅过滤器即可 | 复杂规则引擎（如基于内容的路由）才需 | 当前无需引入 |
| 存储类转换 | ✅ Storage.Transition 可选方法 | — | 无需新依赖 |
| 策略引擎 | ✅ 现有 policy 包可扩展 | — | 无需新依赖 |
| 服务端 COPY | ✅ 取决于后端 | S3/OSS/COS 由 SDK 提供 | 无需新依赖 |

**结论：** 所有 5 个方向在现有技术栈内可完成，无需引入任何新的第三方依赖。新增代码预计总量在 800-1500 行之间，各方向独立可增量交付。

### 4.2 第三方依赖评估标准

如果未来需要引入依赖（如 Redis 做分布式配置缓存），评估标准：

| 维度 | 阈值 | 否决条件 |
|------|------|----------|
| 代码量节省 | >300 行自建代码 | <200 行可自建 |
| 运维复杂度 | 已有运维能力（如 k8s operator 管理） | 需要新增基础设施组件团队负责 |
| 传输成本 | 本地通信可用 | 跨区域网络依赖性 |
| 许可证 | Apache 2.0 / MIT / BSD | AGPL / SSPL / 非商业许可证 |
| Go 生态成熟度 | >1000 GitHub stars, >2 年维护 | 实验阶段/未标记 v1.0 |

---

## 5. 实施路线图

### 优先级评估矩阵

| 方向 | 投资回报 | 技术风险 | 外部依赖 | 影响范围 | 优先级 |
|------|---------|---------|---------|---------|--------|
| ① 配置执行引擎 | 极高（激活 3+ 断线子系统的共同基础） | 低 | 无 | 全协议 | **P0** |
| ② EventBus 路由 | 高（消除广播浪费，使通知规则可消费） | 中（签名变更需协调） | 无 | 全后台 | **P1** |
| ③ Storage Copy | 高（S3 合规、大对象性能） | 中（接口设计选择） | 后端原生 API | Storage 层 | **P1** |
| ④ 生命周期状态机 | 高（成本优化直接可量化） | 中（状态机设计） | 无 | Reconcile + Storage | **P1** |
| ⑤ 跨协议策略评估 | 高（安全合规） | 低（复用已有 Eval） | 无 | Middleware | **P1** |

### 阶段划分

**阶段一（P0，约 1-2 天）：Bucket Config Cache**

- 实现 `ConfigBundle` 结构体
- 实现进程内 LRU + TTL 缓存
- 在 FileService 新增 `loadBucketConfig` 方法
- 修改 `AccessLog` middleware 激活 `WriteAccessLog`（受控 flag）
- **可验证的成果：** S3 `?logging` 配置生效后，访问日志出现在目标桶中

**阶段二（P1，约 2-3 天）：EventBus 路由 + 通知规则消费**

- 引入 `SubscriptionFilter`，修改 `Bus.Subscribe`
- 更新所有 subscriber 调用
- 对接 `NotificationRule`：规则匹配 → 触发 Webhook
- **可验证的成果：** S3 `?notification` 配置的规则触发对应 Webhook

**阶段三（P1，约 2-3 天）：Storage Copy + 生命周期转换**

- 引入 `CopyCopable` 可选接口
- S3 backend 实现服务端 COPY
- 扩展生命周期状态机（新增 `Transition` 动作）
- `reconcile/lifecycle.go` 支持存储类转换
- **可验证的成果：** S3 同 bucket COPY 零数据经过服务器；30 天后对象自动转 IA

**阶段四（P1，约 1-2 天）：跨协议策略评估**

- 将 `checkBucketPolicy` 从 S3 handler 上提到 middleware
- 实现操作映射表（S3↔REST↔WebDAV↔MCP）
- 引入策略缓存到 `ConfigBundle`
- **可验证的成果：** REST API 请求被桶策略拒绝

### 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|----------|
| 配置缓存一致性问题（跨实例） | 中 | 策略变更后旧缓存导致权限不一致 | 短 TTL（30s）+ 配置变更事件主动失效；Postgres 使用 LISTEN/NOTIFY |
| EventBus 过滤器增加发布延迟 | 低 | 事件吞吐量下降 | 粗粒度过滤在 Bus 端做（O(1) map 查找），细粒度在 subscriber 端做 |
| 存储类转换回滚困难 | 低 | 对象从 GLACIER 转换回 STANDARD 有恢复费用 | 转换操作幂等；先提供 dry-run 模式；转换前记录审计日志 |
| 跨协议策略映射遗漏 | 中 | 某个操作的策略评估被跳过 | 新增 operation 注册表（map[string][]string），每个 handler 注册对应操作名 |
| `WriteAccessLog` 激活后日志量爆炸 | 高 | 存储暴增 | 默认 flag=false；提供采样率（`ACCESS_LOG_SAMPLE_RATE=0.1`）；限单桶日日志量 |

---

## 关于保存文件的建议

关于你的建议——将这份分析保存为 `expansion-v121-architect-frontiers-event-log-lifecycle-policy-copy.md`：

**不建议创建新文件。** 验证结果显示，这 5 个方向的完整分析已存在于 `expansion-v99-dead-code-paths-and-governance-gaps.md`（732 行），而跨域"配置完整-执行缺失"的模式分析存在于 `expansion-v138-architecture-blindspots-and-extension-directions.md`（方向三）。如果需要一个聚合这些交叉发现的新文档，更适合创建一份轻量级的 **`TECHNICAL_DEBT.md`**（或统合到现有的 `docs/architecture.md`），列出所有已知的"断线管线"及其状态追踪，作为工程 backlog 而非重复的分析文档。
