# AeroVault 高价值扩展方向 v50 — 经 49 轮分析后仍未被触及的真实盲区

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 23 个子包，~50K `.go` + 三套 SDK + `deploy/*` + 49 对迁移文件 + 全部已有 49 份 `docs/requirements/*.md` 分析文档 + `ROADMAP.md` + `CHANGELOG.md` + `TODO.md`）
>
> **分析视角：** 资深架构师 / 产品经理 — 在累计 **49 期 expansion 分析（250+ 方向，~550,000+ 字分析文本）** 基础上，寻找 **49 轮穷举后依然未被触及** 的真实架构盲区
>
> **去重方法：** 对 `docs/requirements/` 下全部 49 份既有分析文档 + `ROADMAP.md` + `CHANGELOG.md` + `TODO.md` + `extensions*.md` 进行穷尽式关键词验证。每个方向在此前文档中 **零实质性独立架构分析**（即：不在核心方向列表中作为独立小节/独立方向出现，仅表格一行过路、举例提及、或单一子点提及均不构成实质性分析）
>
> **分析日期：** 2026-07-10

---

## 前言

经过 49 期、250+ 方向的穷举分析，AeroVault 的代码库已被反复扫描。功能维度、执行层维度、交叉架构维度、产品成熟度、生产就绪度、操作完整性与 S3 语义深度均已深度覆盖：

```
v1–v42: 功能实现广度               ❌ 不支持 → ✅ 已实现
v43–v45: 架构系统性与交叉缺口       ✅ 各功能独立正确 → ✅ 功能交叉面一致
v46–v47: 产品成熟度 & 生产就绪度    ✅ 功能完整 → ⚠️ 缺乏可验证保障 / 开发者体验
v48–v49: 执行层行为完整性 & S3 深度  ✅ API 可配置 → ⚠️ 后台执行流水线 / 操作语义深度
```

然而，在 49 轮分析的边缘地带，依然有 **5 个方向** 从未被作为独立架构方向实质性触及。它们的共同特征是：

1. **不是"加一个端点"，而是构建一个全新的产品层/平台层/信任层**
2. **涉及跨组件、跨异步边界、跨区域的系统级能力**
3. **在已有功能基础设施之上，开辟一个之前未识别的新维度**
4. **每一条都是 from-scratch 的新方向，在 v1–v49 中零实质性架构分析**

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心矛盾 | 49 期覆盖 |
|---|------|------|--------|---------|-----------|
| **1** | **内容寻址存储 / 对象级去重（Content-Addressable Storage）** | 效率/成本/差异化 | **P1** — 相同内容被上传多次时重复存储、重复计费；无内容指纹识别，无法跨桶/跨租户共享物理副本 | ❌ **零实质性分析**（v25 方向表格一行提及"内容寻址模式"概念但无架构分析；v32 聚焦块级去重且注明"v7 覆盖对象级 CAS"——但 v7 仅有方向表格一行路过提及，从未被实质性分析） |
| **2** | **分布式链路追踪与异步上下文传播（Distributed Tracing & Async Context Propagation）** | 可观测性/运维 | **P1** — OTel SDK 已完成初始化（`otel.go` 初始化了 TracerProvider + MeterProvider），但整个代码库 **只有一处创建 span**（HTTP middleware `http.go:17`），异步路径（Indexer、Replication、Antivirus、Webhook、JobPool、Reconcile）**零 span、零上下文传播**、`context.Background()` 在多处使用 | ⚠️ v11 一行观察"无 span 采样策略"，v23 一段指出"tracing 通道未被利用"，v38 表格一行提及"background worker 的 trace parent 注入"——**均非独立方向分析，从未作为实质性架构方向被展开** |
| **3** | **多区域 Active-Active 双向复制（Multi-Region Geo-Replication）** | 架构/灾备/合规 | **P2** — 当前复制是单向、单目标、无冲突检测的；多区域部署时无法就近读取、无法处理并发写入冲突、无法保证数据驻留合规 | ⚠️ v9 ~50 行、v21 ~20 行提及 active-active/CRDT 概念但聚焦 federated query 与 federation 层，**从未作为独立架构方向分析复制层本身的冲突模型与双向同步** |
| **4** | **细粒度配额体系：Per-Bucket / Per-Key / Per-Path 多维资源治理（Granular Quota System）** | 多租户/运维 | **P1** — 当前配额仅作用于 `(tenant)` 维度（字节+对象数），无法对桶级、API-Key 级、路径前缀级维度的资源消耗进行限制、预警或计费 | ❌ **零实质性分析**（v47 方向三部分覆盖"对象存储边界治理"但聚焦全局大小限制与容量感知，非多维配额体系） |
| **5** | **统一内容处理管线：从存储平台到内容平台（Unified Content Processing Pipeline）** | 产品差异化/体验 | **P2** — 当前仅实现了 JPEG/PNG/GIF 缩略图生成；无图片优化（WebP/AVIF）、无文档预览（PDF→PNG）、无视频处理、无内容类型检测与验证、无处理队列化与异步编排 | ⚠️ v6 方向三（~100 行）覆盖了图片格式转换与协商，但范围限于图片，且仅作为独立方向出现一次；**后续 43 期分析从未将此议题作为持续方向推进或拓展到文档/视频/编排领域** |

---

## 方向一：内容寻址存储 / 对象级去重（Content-Addressable Storage / Object Deduplication）

### 现状

当前存储模型是基于位置寻址的：

```go
// internal/service/file.go:72-74
func storageKey(tenant, bucket, key string) string {
    return path.Join(tenant, bucket, key)
}
```

写入流程：
```
用户 PUT /v1/files/images/logo.png  (body = 0xABCD...)
  → storageKey("default", "default", "images/logo.png")
  → 存储 key = "default/default/images/logo.png"
  → 每次 PUT 都是独立 blob，即使 body 与已存在的对象完全一致
```

这意味着：

| 场景 | 当前行为 | 理想行为 |
|------|---------|---------|
| 同一个文件上传 10 次（不同 key） | 10 份独立 blob，10× 存储成本 | 1 份物理 blob，10 个 metadata 指针 |
| 两个租户上传完全相同的文件 | 2 份独立 blob | 1 份物理 blob + 引用计数 |
| 同一文件的多个版本（versioning） | 每个版本独立 blob | 基于 chunk 的增量去重 |

**问题规模：** 对于典型的内容平台（文档管理、AI 训练数据、媒体资产），对象级去重可节省 **40–60%** 存储成本。在 AI 训练场景中，同一语料库被多个租户独立上传的情况极为常见——去重收益可达 **80%+**。

### 为什么之前不做

内容寻址存储的复杂度远高于简单的位置寻址：

1. **引用计数与 GC**：物理 blob 被多个 metadata 行引用时，删除一个 metadata 行不能删除物理 blob（参考计数归零才能删）
2. **跨租户共享的安全边界**：租户 A 上传的文件，租户 B 必须恰好具有相同的字节流才能共享同一物理副本——但需要确保不存在信息泄露的侧信道
3. **部分更新语义**：已存在的 blob 不能原地更新（内容改变 = 新的 content hash）
4. **存储后端兼容性**：S3/OSS/COS 没有原生 CAS，需要在应用层实现
5. **加密与去重的矛盾**：SSE 加密后相同内容的密文不同——去重必须在加密前或使用确定性加密

### 架构设计

```
存储模型的三层抽象：

┌───────────────────────────────────────────┐
│  Metadata Layer (repository)               │
│  ┌─────────────────────────────────────┐  │
│  │ Object{Key, Tenant, Bucket, ...,    │  │
│  │        ContentHash, RefCount}       │  │
│  └─────────────────────────────────────┘  │
├───────────────────────────────────────────┤
│  Content Address Table (repository)        │
│  ┌─────────────────────────────────────┐  │
│  │ Content{Hash, Size, StorageKey,     │  │
│  │         RefCount, CreatedAt}        │  │
│  └─────────────────────────────────────┘  │
├───────────────────────────────────────────┤
│  Physical Layer (storage)                  │
│  ┌─────────────────────────────────────┐  │
│  │ StorageKey = sha256(content)         │  │
│  │ 或 StorageKey = prefix/sha256[:2]/  │  │
│  │                sha256                │  │
│  └─────────────────────────────────────┘  │
└───────────────────────────────────────────┘
```

### 核心变更

| 组件 | 变更 | 行数估计 |
|------|------|---------|
| `repository/repository.go` — `Content` 模型 + `ContentAddressTable` | 新增 | ~60 行模型 + 迁移 |
| `repository/sql_objects.go` — 写入时检测 content hash 是否存在 | 修改 | +30 行 |
| `repository/sql_cas.go` — `InsertContent`、`DeleteContent`（引用计数） | 新增 | ~80 行 |
| `service/file_crud.go` — `Put` 路径可选 CAS 分支 | 修改 | +40 行 |
| `service/file_crud.go` — `Delete` 路径引用计数递减 | 修改 | +25 行 |
| `reconcile/cas_gc.go` — 引用计数归零的 blob 后台清理 | 新增 | ~100 行 |
| 迁移文件 — `contents` 表 | 新增 | 4 文件 |
| 配置项 — `STORAGE_CAS_ENABLED` | 新增 | +5 行 |
| 测试 | 新增 | ~150 行 |
| **总计** | | **~490 行** |

### 工程估算

- 核心 CAS 逻辑：~200 行（Repository CRUD + FileService 分支）
- GC：~100 行
- 迁移与配置：~40 行
- 测试：~150 行
- **合计：~490 行**

### 为什么不直接做块级去重

v32 已覆盖块级去重（分块、增量压缩、差分编码）。对象级去重是块级去重的前置依赖——没有对象级去重的稳定语义，块级去重会导致块引用图过于复杂。**建议先做对象级（P1），再做块级（P2）。**

---

## 方向二：分布式链路追踪与异步上下文传播（Distributed Tracing & Async Context Propagation）

### 现状

OTel SDK 正确初始化了 TracerProvider：

```go
// internal/telemetry/otel.go:48-51
tp := sdktrace.NewTracerProvider(
    sdktrace.WithBatcher(traceExp, sdktrace.WithBatchTimeout(5*time.Second)),
    sdktrace.WithResource(res),
)
otel.SetTracerProvider(tp)
```

但是整个代码库中 **仅有一处创建 span**：

```go
// internal/telemetry/http.go:17
tracer := otel.Tracer("aero-vault/http")
// ...
ctx, span := tracer.Start(r.Context(), r.Method+" "+r.URL.Path, ...)
defer span.End()
```

这意味着：

| 组件 | 是否有 span | 是否有 trace context 传播 | 后果 |
|------|------------|------------------------|------|
| HTTP handler | ✅ 一个粗粒度 span | ✅ 传播到 request context | ⚠️ 无法区分内部操作的耗时分布 |
| FileService.Get/Put/Delete | ❌ 无 | ❌ | 无法定位慢操作发生在存储层还是 DB 层 |
| Storage backend (local/s3/oss/cos) | ❌ 无 | ❌ | S3 慢请求不可追踪 |
| Repository (SQL) | ❌ 无 | ❌ | 慢查询不可关联到请求 |
| Indexer | ❌ 无 | ❌ 使用 `context.Background()` | 索引慢无法和触发事件关联 |
| Replication Worker | ❌ 无 | ❌ | 复制慢无法和原始请求关联 |
| JobPool (Queue) | ❌ 无 | ❌ 入队上下文中断 | 作业失败无法回溯到触发源 |
| Webhook delivery | ❌ 无 | ❌ | 回传失败无法追踪 |
| Chat/Agent LLM 调用 | ❌ 无 | ❌ | AI 成本无法关联到用户请求 |
| SSE Event stream | ❌ 无 | ❌ 使用 `context.Background()` | 事件投递无法追踪 |

**最严重的问题：** `context.Background()` 在异步路径中广泛使用。索引器、复制 worker、webhook 重试、reconcile 都用 `context.Background()`——和父请求的 trace context 完全断裂。

```go
// internal/ai/indexer.go — 多个异步路径
func (idx *Indexer) Run(ctx context.Context, sub <-chan repository.Event) {
    // ctx 是 main 传人的根 context，不是任何请求的派生 context
}
// internal/replication/replication.go:51
// Run 函数的 ctx 也是根 context
```

### 为什么需要

**在不读代码的情况下回答以下问题：**

| 问题 | 当前能否回答 | 有 trace 后 |
|------|------------|------------|
| 用户报 GET 慢 5s，是存储后端慢还是 DB 慢？ | ❌ 只能猜 | ✅ Span 树一目了然 |
| 索引器处理某个文件花了 30s，是 extractor 慢还是 embedder 慢？ | ❌ 只能加日志重试 | ✅ IndexObject span 下挂 Extract + Embed sub-span |
| 复制 worker 失败是因为 replicate job 还是因为存储后端不可用？ | ❌ 只能看日志 | ✅ Span 记录错误 + 原因 |
| 某个 webhook 事件从产生到送达花了多久？ | ❌ 无法关联 | ✅ 事件产生 span → 投递 span |
| Chat 请求的 LLM 耗时占比多少？ | ❌ 只知道总 latency | ✅ LLM span + Embed span + Search span |

### 架构设计

```
一个 HTTP 请求的完整 trace 树（当前 vs 目标）：

当前：
HTTP GET /v1/files/photo.jpg  ───────────────┐
                                              │  一个粗粒度 span
                                              ▼
                                          ~500ms（无法分解）

目标：
HTTP GET /v1/files/photo.jpg  ───────────────────────── span A
  ├─ auth.Lookup(token) ──── span B (5ms)
  ├─ repo.GetObject ──────── span C (2ms)
  ├─ store.Get ───────────── span D (450ms ← S3 慢！)
  │   └─ s3.GetObject ────── span E (450ms)
  └─ emit(event) ─────────── span F (1ms)
```

### 核心变更

| 组件 | 变更 | 行数估计 |
|------|------|---------|
| `internal/ai/indexer.go` — `context.Background()` → `ctx` + span | 修改 | +30 行 |
| `internal/ai/chat.go` — LLM 调用 span | 新增 | +10 行 |
| `internal/ai/search.go` — search span | 新增 | +15 行 |
| `internal/ai/agent.go` — agent step span | 新增 | +10 行 |
| `internal/replication/replication.go` — 复制 span | 新增 | +15 行 |
| `internal/events/webhook.go` — 投递 span | 新增 | +10 行 |
| `internal/jobs/jobs.go` — job execution span | 新增 | +15 行 |
| `internal/service/file_crud.go` — Put/Get/Delete span | 新增 | +30 行 |
| `internal/storage/*.go` — storage backend span | 新增 | +40 行 |
| `internal/repository/sql_objects.go` — SQL query span | 新增 | +20 行 |
| 作业队列入队时注入 trace context | 新增 | +20 行 |
| 事件发布时携带 trace context | 新增 | +15 行 |
| 测试 | 新增 | ~60 行 |
| **总计** | | **~290 行** |

### 不变量与设计约束

- **绝不阻塞请求路径**：span 创建是零成本的（当 OTel exporter 未配置时 no-op）
- **异步上下文必须显式传递**：Job 入队时序列化 `traceparent`（W3C Trace Context），出队时恢复
- **Event Bus 携带 trace context**：`repository.Event` 增加 `TraceParent` 字段，透传到所有 subscriber
- **不增加新的外部依赖**：OTel SDK 已经导入

---

## 方向三：多区域 Active-Active 双向复制（Multi-Region Geo-Replication）

### 现状

当前复制是 **单向、单目标、无冲突检测** 的：

```go
// internal/replication/replication.go
// Worker 仅响应 EventCreated，从 primary 复制到 replica（一个目标）
func (w *Worker) ReplicateObjectByID(ctx context.Context, objectID int64) error {
    // 1. primary.Get(storageKey)
    // 2. replica.Put(storageKey, ...)
    // 3. 标记 repl_status=replicated
}
```

缺失的核心能力：
- ❌ **反向复制**： replica 的变更不会回传至 primary
- ❌ **双向同步**： 两个区域各自接受写入，需要双向 reconcile
- ❌ **冲突检测**： 区域 A 和区域 B 同时写入同一 key，静默覆盖
- ❌ **就近读取**： 客户端总是读取 primary，无法路由到最近的区域
- ❌ **区域故障转移**： primary 不可用时，无法自动切换到 replica 提供服务
- ❌ **数据驻留合规**： 无法控制哪些数据可以跨区域复制

### 为什么需要

1. **全球用户就近访问**：用户在欧盟上传 100MB 文件，却要从美东区域下载，延迟 200ms+ 而非 20ms
2. **区域级故障的 RPO/RTO**：AWS us-east-1 级别的故障对单区域系统可能是致命打击
3. **数据驻留合规**：GDPR（欧盟）、PIPL（中国）要求用户数据不得离开特定地理边界
4. **灾备切换时效**：当前复制无健康检测、无自动故障转移

### 架构设计

```
┌─────────────────────┐         ┌─────────────────────┐
│   Region A (us-east) │         │   Region B (eu-west) │
│                      │         │                      │
│  ┌──────┐  ┌──────┐  │  async  │  ┌──────┐  ┌──────┐  │
│  │ Store│  │  DB  │  │◄────────►│  │ Store│  │  DB  │  │
│  └──┬───┘  └──┬───┘  │  bidir  │  └──┬───┘  └──┬───┘  │
│     │         │       │  sync   │     │         │       │
│  ┌──┴─────────┴──┐   │         │  ┌──┴─────────┴──┐   │
│  │ FileService   │   │         │  │ FileService   │   │
│  └───────┬───────┘   │         │  └───────┬───────┘   │
│          │           │         │          │           │
│  ┌───────┴───────┐   │         │  ┌───────┴───────┐   │
│  │   Router A    │   │         │  │   Router B    │   │
│  │ us-east.api   │   │         │  │ eu-west.api   │   │
│  └───────┬───────┘   │         │  └───────┬───────┘   │
└──────────┼───────────┘         └──────────┼───────────┘
           │                                │
           └────────── Global DNS ──────────┘
                  (geo-routing / latency-based)
```

### 冲突模型

| 冲突类型 | 检测方式 | 解决策略 | RTO |
|---------|---------|---------|-----|
| 同时写入同一 key | `last_modified` 比较 + vector clock | Last-Writer-Wins（默认）或 Conflict Marked → 人工解决 | 秒级 |
| 区域 A 删除 + 区域 B 更新 | Tombstone + version vector | 删除优先（Delete Wins）或 标记为冲突 | 秒级 |
| 网络分区后恢复 | 全量 checksum reconcile | 差异同步 + 冲突标记 | 分钟级（取决于数据量） |

### 核心变更

| 组件 | 变更 | 行数估计 |
|------|------|---------|
| `internal/replication/replication.go` — 双向复制 worker | 重写 | +200 行 |
| `internal/replication/conflict.go` — 冲突检测与标记 | 新增 | ~150 行 |
| `internal/cluster/georeplication.go` — 区域拓扑与健康 | 新增 | ~120 行 |
| `internal/api/rest/georouter.go` — 就近读取中间件 | 新增 | ~80 行 |
| `internal/repository/sql_objects.go` — version vector 存储 | 修改 | +30 行 |
| `internal/reconcile/crossregion.go` — 跨区域 reconcile | 新增 | ~150 行 |
| 迁移文件 — region/version_vector 字段 | 新增 | 4 文件 |
| `internal/service/file_features.go` — 数据驻留策略 | 新增 | ~60 行 |
| 配置项 — `REPLICATION_*` 扩展 | 修改 | +30 行 |
| 文档 — 多区域部署指南 | 新增 | ~80 行 |
| 测试 | 新增 | ~200 行 |
| **总计** | | **~1,100 行** |

### 注意

这是所有方向中工程量最大的（~1,100 行），建议分阶段实施：
- **Phase 1**：双向同步 + LWW 冲突解决（~400 行）
- **Phase 2**：就近读取 + 区域健康检测（~300 行）
- **Phase 3**：冲突标记 + 人工解决界面（~400 行）

---

## 方向四：细粒度配额体系（Granular Multi-Dimensional Quota System）

### 现状

当前配额模型仅有 `(tenant) → (max_bytes, max_objects)`：

```go
// internal/repository/tenants.go 中的 TenantQuota
type TenantQuota struct {
    TenantID    string
    MaxBytes    int64
    MaxObjects  int64
    UsedBytes   int64
    UsedObjects int64
}
```

支持的维度：
```
✅ tenant-level  bytes + objects   （已实现）
❌ bucket-level  bytes + objects   （未实现）
❌ api-key-level bytes + objects   （未实现）
❌ path-prefix   bytes + objects   （未实现）
❌ storage-class bytes per tier    （未实现）
❌ egress        bytes per month   （未实现）
❌ API call rate per api-key       （未实现）
❌ API call rate per path          （未实现）
❌ concurrent connections per api-key （未实现）
```

### 为什么需要

| 场景 | 当前处理 | 理想处理 |
|------|---------|---------|
| 租户 A 有一个"日志上传"的 API Key，本应只写 `logs/` 前缀 | 可用该 key 写入任何路径，消耗整个租户的配额 | 限制该 key 只能写 `logs/` 前缀，配额独立 |
| 租户 B 有 3 个部门，各部门预算独立 | 所有部门消耗同一租户配额，无法区分 | 每个部门有自己的路径级配额 |
| 运营团队需要限制某个 bucket 的存储总大小 | 无此能力，只能通过全局租户配额 | 按 bucket 独立配额，超限 403 |
| 一个 API Key 被泄露 | 只能用撤销整个 key 来止损 | 可以在 key 级别限速 + 限写路径 + 限存储，降低泄露影响 |
| 租户的 egress 流量需要计费 | 无此能力 | 按 API Key / Bucket 跟踪 egress 字节 |
| 某个高流量路径需要独立限流 | 只能用全局 RPS | 按路径前缀的 token bucket |

**核心矛盾：** 当前系统有完善的多租户能力（租户 CRUD、key 管理、scope 体系），但 **资源治理只有单一维度**。多租户 SaaS 最基础的能力——"不同客户不同规格、不同部门不同预算、不同 key 不同权限"——无法实现。

### 架构设计

```
配额层级树状结构（自上而下）：

Tenant  ──────────────────  max: 100GB, 10M objects
  ├── Bucket "data"  ─────  max:  50GB,  5M objects
  │     ├── API Key "upload" ─  max:  20GB,  1M objects, RPS: 100
  │     └── API Key "readonly"  max:  egress 10GB/month, RPS: 1000
  └── Bucket "archive" ─  max: 100GB (cold tier)
```

### 配额维度扩展

```go
type ResourceQuota struct {
    Scope     string // "tenant" | "bucket" | "api_key" | "path"
    ScopeID   string // e.g. "default", "data", "key_hash", "/logs/"
    LimitBytes     int64   // 0 = unlimited
    LimitObjects   int64   // 0 = unlimited
    LimitEgress    int64   // 0 = unlimited, per billing period
    LimitRPS       float64 // per-scope rate limit
    LimitConcurrency int   // 0 = unlimited
    LimitStorageClass string // 可选：仅限制特定存储类
    UsedBytes     int64
    UsedObjects   int64
    UsedEgress    int64
    EffectiveSince time.Time // 用于结算周期
}
```

### 核心变更

| 组件 | 变更 | 行数估计 |
|------|------|---------|
| 迁移文件 — `resource_quotas` 表 | 新增 | 4 文件 |
| `repository/sql_quotas.go` — 多维配额 CRUD | 新增 | ~150 行 |
| `service/quota.go` — 分层配额检查引擎 | 新增 | ~120 行 |
| `service/file_crud.go` — 集成配额检查 | 修改 | +30 行 |
| `auth/auth.go` — API Key 级别配额绑定 | 修改 | +20 行 |
| `middleware/ratelimit.go` — API Key 级别限流 | 修改 | +40 行 |
| `api/rest/admin.go` — 设置/查询多维配额 | 新增 | ~80 行 |
| `api/s3compat/handler.go` — S3 egress 跟踪 | 修改 | +15 行 |
| `reconcile/quota_reporter.go` — 配额使用报告 | 新增 | ~60 行 |
| 测试 | 新增 | ~150 行 |
| **总计** | | **~670 行** |

### 设计要点

- **检查顺序**：API key → Path → Bucket → Tenant，先触碰限制者生效
- **继承语义**：未设置的维度继承父级限制（API key 未设限 → 使用 bucket 限制 → 使用 tenant 限制）
- **计费周期**：egress 配额按月滚动（`EffectiveSince`），存储配额为实时硬限制
- **告警阈值**：支持在 80%/90%/100% 的使用率时发出日志/事件/Webhook

---

## 方向五：统一内容处理管线（Unified Content Processing Pipeline）

### 现状

当前的内容处理能力：

```
✅ 缩略图生成： JPEG/PNG/GIF → JPEG (max 256x256 / 2048x2048)
   └── internal/thumbnail/thumbnail.go（标准库 image 包，bilinear 缩放）

❌ 图片优化： 无 WebP/AVIF 输出，无质量协商，无自适应分辨率
❌ 文档预览： 无 PDF → PNG/JPEG，无 Office 文档（docx/xlsx）渲染
❌ 视频处理： 无缩略图生成（视频首帧提取），无转码，无 HLS/DASH
❌ 音频处理： 无波形生成，无转码
❌ 内容类型检测： 无 Magic Byte 校验（上传的 .exe 可伪装成 .jpg）
❌ 处理编排： 无异步处理流水线，无 webhook 回调，无进度通知
❌ 缓存层： 处理结果不缓存（每次请求重新生成缩略图）
```

**最明显的问题：** 上传一个 20MB 的 PDF，当前系统只能存储和下载它。不能预览、不能搜索 PDF 内容（虽然有文本提取管道用于 AI 索引，但无法在 UI 中渲染）、不能生成 PDF 的缩略图。

### 为什么需要

1. **产品差异化**：从"文件存储"到"内容平台"的跨越。用户选择平台时，"能否预览我的 PDF" 往往是决定因素
2. **带宽节省**：WebP/AVIF 比 JPEG 小 25–35%，自适应分辨率可节省移动端 60%+ 带宽
3. **安全基线**：Magic Byte 检测是内容安全的最低要求——上传伪装成图片的可执行文件是常见攻击向量
4. **用户体验**：Web UI 中无法预览文档，用户只能下载后用本地软件打开——在移动端体验极差

### 架构设计

```
处理管线编排：

Upload → Content Type Detection (magic bytes)
  → Processing Pipeline
      ├── Image  → Optimize (WebP/AVIF/JPEG-XL, 多分辨率)
      │           → Generate Thumbnails (已有)
      ├── Document → Render Preview (PDF→PNG, Office→PDF→PNG)
      │           → Extract Text (已有 extractor)
      ├── Video  → Extract Thumbnail (首帧)
      │           → Transcode (HLS/DASH, 多码率)
      └── Audio  → Generate Waveform → Transcode
  → Store Processed Artifacts (同一个 bucket, `_processed/` 前缀)
  → Emit Events (processing.completed / .failed)
```

### 异步处理模型

```go
// 基于现有 Job Queue 的异步处理
const JobProcessObject = "process"

type ProcessRequest struct {
    Tenant     string   `json:"tenant"`
    Bucket     string   `json:"bucket"`
    Key        string   `json:"key"`
    Operations []string `json:"operations"` // ["thumbnail", "webp", "preview", "transcode"]
    WebhookURL string   `json:"webhook_url,omitempty"` // 处理完成回调
}
```

### Magic Byte 检测（最优先、最轻量）

```go
// Content-Type 检测与验证
var magicTable = []struct {
    magic []byte
    mime  string
}{
    {[]byte{0xFF, 0xD8, 0xFF}, "image/jpeg"},
    {[]byte{0x89, 0x50, 0x4E, 0x47}, "image/png"},
    {[]byte{0x25, 0x50, 0x44, 0x46}, "application/pdf"},
    {[]byte{0x50, 0x4B, 0x03, 0x04}, "application/zip"}, // docx/xlsx/pptx
    // ...
}

func DetectContentType(r io.Reader) (string, error) {
    header := make([]byte, 512)
    n, _ := io.ReadFull(r, header)
    // 比对 magic bytes
    // 返回检测到的 MIME type + 可选验证与声明 Content-Type 不一致
}
```

### 核心变更

| 组件 | 变更 | 行数估计 |
|------|------|---------|
| `internal/processing/detect.go` — Magic Byte 检测 + MIME 验证 | 新增 | ~80 行 |
| `internal/processing/optimize.go` — 图片优化（WebP/AVIF） | 新增 | ~120 行 |
| `internal/processing/preview.go` — 文档预览（PDF→PNG，调用外部 CLI） | 新增 | ~100 行 |
| `internal/processing/video.go` — 视频缩略图提取 | 新增 | ~50 行 |
| `internal/processing/pipeline.go` — 处理管线编排 | 新增 | ~150 行 |
| `internal/processing/worker.go` — 异步处理 worker（基于 JobPool） | 新增 | ~100 行 |
| `internal/processing/cache.go` — 处理结果缓存层 | 新增 | ~60 行 |
| `service/file_features.go` — 触发处理流程 | 修改 | +40 行 |
| `internal/api/rest/router.go` — 处理结果 GET 端点 | 新增 | +30 行 |
| `internal/config/config_ai.go` — 处理配置项 | 修改 | +20 行 |
| 测试 | 新增 | ~150 行 |
| **总计** | | **~900 行** |

### 依赖策略

- **Magic Byte 检测**：纯标准库，零依赖
- **WebP/AVIF 输出**：Go 标准库不支持。选项：(a) `libvips` CGO 绑定；(b) `cwebp`/`avifenc` CLI 子进程；(c) 外部处理服务。推荐 (b) 作为初始实现
- **文档预览**：推荐 `pdftoppm`（Poppler CLI）和 `libreoffice --headless`，通过子进程调用
- **视频缩略图**：推荐 `ffmpeg` 子进程

### 实施顺序建议

```
Phase 1 (P1, ~200 行): Magic Byte 检测 + Content-Type 验证
  └── 纯 Go，零依赖，安全收益立即可见

Phase 2 (P1, ~150 行): 图片优化 (WebP/AVIF) + 缓存
  └── 需要 cwebp/avifenc CLI，带宽收益显著

Phase 3 (P2, ~250 行): 文档预览 (PDF) + 异步处理编排
  └── 需要 pdftoppm CLI，产品体验提升

Phase 4 (P3, ~300 行): 视频缩略图 + 外部处理服务接口
  └── 需要 ffmpeg CLI，差异化功能
```

---

## 综合优先级建议

```
方向一：内容寻址存储 / 去重       ████████████████████  P1  ~490 行  成本节约 40-60%
方向四：细粒度配额体系            ██████████████████    P1  ~670 行  多租户 SaaS 基石
方向二：分布式链路追踪            ██████████████        P1  ~290 行  运维可观测性基座
方向五：内容处理管线（Phase 1-2） ██████████████        P1  ~350 行  安全 + 带宽 + 体验
方向三：多区域 Active-Active      ████████              P2  ~1,100 行  全球级架构

     Phase 1 (2周)                     Phase 2 (1月)                      Phase 3 (2月+)
  ┌─────────────────┐          ┌────────────────────┐          ┌──────────────────────┐
  │ 方向二 (Tracing) │          │ 方向四 (细粒度配额)  │          │ 方向三 (多区域)       │
  │ 方向一 (去重)     │          │ 方向五 Ph2 (图片优化) │          │ 方向五 Ph3-4 (文档/视频)│
  │ 方向五 Ph1 (Magic)│          │ 方向五 Ph3 (文档预览) │          │                      │
  └─────────────────┘          └────────────────────┘          └──────────────────────┘
```

### 建议执行序列

1. **第 1 周（方向二 + 方向五 Ph1）**：
   - 为 FileService、Storage、Repository 注入 span（~200 行）→ **可观测性基座**
   - Magic Byte 检测（~80 行）→ **安全基线**
   - 两者均不依赖外部工具、不新增依赖、可增量部署

2. **第 2–3 周（方向一）**：
   - CAS 核心逻辑 + 迁移 + GC（~490 行）
   - 在 `STORAGE_CAS_ENABLED` flag 后默认关闭
   - 先在非生产环境运行验证引用计数正确性

3. **第 4–6 周（方向四）**：
   - 多维配额表 + 层级检查引擎（~670 行）
   - 向后兼容：未配置维度的 scope 行为同当前（unlimited）

4. **第 7–10 周（方向五 Ph2-3 + 方向三 Phase 1）**：
   - 图片优化 + 文档预览 + 异步编排（~500 行）
   - 双向复制 + LWW（~400 行）

---

## 结论

这 5 个方向标志着 AeroVault 从"功能完整的中型系统"向"平台级、全球级、安全可信的生产系统"的最后跨越：

| 方向 | 解决的问题 | 对产品定位的贡献 |
|------|-----------|----------------|
| **内容寻址存储** | 存储成本随数据量线性增长的不可持续 | 用 1 份物理副本服务所有相同内容 → 成本下降 40–60% |
| **分布式链路追踪** | 黑盒运维——不知道慢在哪里、错在哪里 | 异步路径全文可见 → 生产排障时间从小时级降到分钟级 |
| **多区域 Active-Active** | 单一区域既是延迟瓶颈又是故障单点 | 全球节点就近接入 + 区域故障自动切换 |
| **细粒度配额体系** | 多租户只有 tenant 维度限制→无法落地 SaaS 规格 | 支持任意维度的资源治理→真正可销售的多租户 |
| **统一内容处理管线** | 只有原始存储和下载，没有"内容平台"体验 | Magic Byte 检测（安全）+ 图片优化（带宽）+ 文档预览（体验）→ 从存储到内容平台 |

这 5 个方向中，方向一（去重）和方向二（Tracing）是"改造成本最低、性价比最高"的起点——前者直接改变成本结构，后者直接改变运维认知。
