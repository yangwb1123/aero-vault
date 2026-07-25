# 高价值扩展方向：多后端存储编排、SSE 订阅过滤、作业基础设施、跨协议命名空间一致性、存储能力契约

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — `cmd/server/main.go` + `internal/` 全部 30+ 子包（237 个 Go 源文件），3 套 SDK，MCP 双模式，Web UI，48 对迁移文件，`deploy/` 全套配置，`HARNESS.md`，`AGENTS.md`，ROADMAP.md，CHANGELOG.md  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。选取**代码中存在明确锚点、有实质性产品/架构影响、且在过往 91 轮分析中未被深度独立覆盖**的方向。每个方向包含：代码证据 → 产品价值 → 架构权衡 → 边界情况。

---

## 去重验证

对 `docs/requirements/` 下全部 91 份既有分析文档逐方向进行关键词正则 + 语义交叉验证：

| 方向 | 既往覆盖情况 |
|------|-------------|
| **多后端存储编排引擎** | ✅ **零实质性覆盖** — 全量 91 份文档正则搜索 `multi.backend\|backend.router\|backend.dispatch\|backend.routing\|capabilities.*Storage\|Storage.*capabilities` → **0 命中**。v91 覆盖了存储类生命周期转换（STANDARD → STANDARD_IA → GLACIER），但这是**单后端内的存储类迁移**，与多后端并行 + 能力感知路由**完全不同** |
| **SSE 订阅过滤谓词** | ✅ **浅层提及无架构** — 4 份文档合计 `<5` 行提及 "SSE filter" 或 "EventSource subscription"，均为概念性列举，**无代码锚点驱动的实现路径分析** |
| **作业可观测性与控制平面** | ✅ **零实质性覆盖** — 3 份文档极少量提及（v13 方向表 1 行 "job queue depth metrics"；v40 1 行 "job admin panel"；v63 2 行 "per-type job latency"），**无实现分析** |
| **跨协议文件夹/目录语义统一** | ✅ **仅协议层提及无存储层** — v19 分析了多协议一致性但聚焦 API 语义差异（版本元数据、条件请求），**从未触及文件夹标记对象 vs 虚拟目录 vs 真实目录的底层存储表示不一致** |
| **存储后端能力契约** | ✅ **零覆盖** — 全量 91 份文档正则搜索 `Capabilities\|capability.*Storage\|Storage.*capability\|can.*Support\|Support.*method` → **0 命中** |
| 访问日志管线（WriteAccessLog）| ⚠️ v17 方向五已覆盖，排除 |
| 写路径补偿事务 | ⚠️ v86/v88 已覆盖，排除 |
| 配置校验 | ⚠️ 已部分实现（`config.go:Validate()`），排除 |

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 |
|---|------|------|--------|---------|---------|
| **1** | **多后端存储编排引擎 (Multi-Backend Storage Orchestration Engine)** | 架构扩展/成本优化 | **P1** | `Storage` 接口是单后端抽象；`storage_class` 写入后永不用于路由决策；系统无法同时运行 local+S3+OSS 等异构后端并按存储类/成本/性能需求路由对象 | `internal/storage/storage.go:Storage`（单接口无路由抽象）；`internal/storage/factory.go:NewFromConfig`（单后端工厂，switch-case 返回唯一实例）；`internal/storage/circuitbreaker.go`（装饰模式存在但仅包裹单实例）；`internal/repository/repository.go:Object.StorageClass`（写入后永不用于存储选择）；`cmd/server/main.go:buildStorage`（`initInfrastructure` 内只建一个 backend） |
| **2** | **SSE 事件订阅过滤与多租户流控 (SSE Event Subscription with Filter Predicates)** | 性能/可扩展性 | **P2** | `GET /v1/events/stream` 无过滤能力：广播全部事件给全部订阅者，租户过滤在客户端进行。100+ 并发 SSE 连接 × 高事件吞吐 = 严重带宽/CPU 浪费；无回压信令 | `internal/api/rest/sse.go:liveStream`（`for{select{...case e,ok:=<-sub:}}` — 全量接收，`e.TenantID!=tenant` 静默跳过）；`internal/events/bus.go:Publish`（`broadcast` — 扇出给所有订阅者，无过滤谓词）；`internal/events/bus.go:Subscribe`（返回 `<-chan repository.Event`，无参数）；`internal/events/bus.go:broadcast`（线性遍历全部 subs，无优先级或过滤） |
| **3** | **后台作业基础设施：可观测性、管理面与智能节流 (Background Job Infrastructure: Observability, Control Plane & Intelligent Throttling)** | 运维/韧性 | **P2** | `jobs.Pool` 具备成熟的领用/执行/重试/收割机制，但 admin API 仅暴露 `ListJobs` + `RetryJob`。缺少：按类型耗时直方图、队列深度仪表盘、暂停/恢复/取消操作、作业依赖、失败告警 Webhook、基于错误率的智能节流 | `internal/jobs/jobs.go:Pool`（完整的 claim/execute/retry/reap 循环但 `telemetry.IncJobCompleted`/`IncJobFailed`/`IncJobRetried` 仅计数）；`internal/jobs/jobs.go:runOne`（`MaxAttempts` 耗尽后 `FailJob` — 无通知路由）；`internal/api/rest/admin_jobs.go:ListJobs`（返回 stats + jobs list 但无状态变更操作）；`internal/jobs/jobs.go:NewPool`（`baseBackoff`/`maxBackoff` 是静态的，不根据实际失败率动态调整）；`internal/telemetry/metrics.go`（`mJobsCompleted`/`mJobsFailed`/`mJobsRetried` — 仅 counter，无 histogram） |
| **4** | **跨协议文件夹/目录命名空间语义统一 (Cross-Protocol Namespace Consistency for Folder/Directory Operations)** | 产品特性/协议完备 | **P2** | REST 使用零字节标记对象（`application/x-directory` + 尾随 `/`），S3 使用隐式虚拟目录（prefix + delimiter），WebDAV 使用操作系统目录语义。三种表示不一致：MKCOL 创建 WebDAV 目录 vs POST /folders 创建 REST 目录 vs S3 客户端看到标记对象为常规文件。无统一 Namespace 层 | `internal/api/rest/handler.go:CreateFolder`（PUT 零字节对象，`ContentType: "application/x-directory"`）；`internal/api/rest/handler.go:ListFolders`（基于 prefix + `/` 分割推导虚拟目录）；`internal/api/webdav/dav.go:davFS.OpenFile`（`strings.HasSuffix(name,"/")` 判断目录 — 不同判定逻辑）；`internal/api/s3compat/handler.go:ListObjectsV2`（`delimiter=/` 隐含虚拟目录 — S3 客户端期望的无标记行为）；`internal/api/rest/router.go`（`ListFolders` / `CreateFolder` / `DeleteFolder` 独立路由 — 与 List 不一致） |
| **5** | **存储后端能力契约 (Storage Capabilities Contract & Adaptive Client)** | 架构/性能 | **P1** | `Storage` 接口对全部后端要求同一组方法，但不同后端能力迥异：local FS 的 presign 依赖 `PublicURL`+`SignKey` 配置，S3/OSS/COS 各有不同的 multipart 语义和一致性模型。无 `Capabilities()` 方法导致调用方只能尝试-失败，无法自适应 | `internal/storage/storage.go:Storage`（无 `Capabilities() Supported() error` 方法）；`internal/storage/local.go:PresignGet`（条件性失败当 `PublicURL==""` — 调用方无法提前知晓）；`internal/storage/s3.go:CopyObject`（委托给 AWS SDK 但 aero-vault 的 copyObject 未使用服务端拷贝）；`internal/storage/local_read.go:Get`（全量读取到内存再解密 — 不适合大对象）；`internal/api/s3compat/extra.go:copyObject`（读取到内存后 PUT — 没有检查后端是否支持服务端拷贝）；`internal/storage/contract_test.go:RunContract`（统一契约测试但无能力标记测试） |

---

## 方向一：多后端存储编排引擎

### 现状

当前系统在启动时根据 `STORAGE_BACKEND` 环境变量选择一个后端：

```go
// cmd/server/main.go:initInfrastructure
store, err := buildStorage(ctx, cfg)
// buildStorage -> buildStorageFrom -> NewFromConfig
// NewFromConfig 根据 cfg.Kind 创建唯一后端
```

```go
// internal/storage/factory.go:NewFromConfig
switch cfg.Kind {
case BackendLocal:
    store, err = NewLocal(cfg.Local)
case BackendS3:
    store, err = NewS3(ctx, bc)
case BackendOSS:
    store, err = NewOSS(bc)
case BackendCOS:
    store, err = NewCOS(bc)
}
// 返回一个 Storage 实例，可选的 CircuitBreaker 包裹
```

`FileService` 持有一个 `Storage` 接口引用，所有对象写入同一后端，无论其 `storage_class` 值为何：

```go
// internal/repository/repository.go:Object
type Object struct {
    // ...
    StorageClass string // e.g. STANDARD, STANDARD_IA, GLACIER
    // 但永不用于存储选择 —— 所有对象都在同一后端
}
```

迁移 `0021_storage_class` 在 schema 层面增加了 `storage_class` 列，S3 API 也能正确解析 `x-amz-storage-class` 请求头，但**这个值从未驱动任何存储位置决策**。

### 产品价值

1. **成本优化**：热数据放 local SSD 或 S3 Standard，冷数据放 COS Archive 或 S3 Glacier Instant Retrieval，无需迁移脚本
2. **地理分布**：按对象或 bucket 将数据路由到离用户最近的区域后端
3. **供应商中立**：迁移时无需一次性全量迁移——新旧后端可并行运行，逐步将数据迁移到新后端
4. **弹性分级**：性能敏感数据走本地 NVMe，合规归档走远端对象存储

### 架构权衡

- **写入路径扩展点**：`FileService.Put` 或 `Storage` 层需要根据 `storage_class` + bucket 策略 → 选择后端 → 路由写入。切入点应在 `FileService` 和 `Storage` 之间插入 `TieredRouter`
- **读路径复杂度**：读操作需要知道对象在哪个后端。当前 `Object.StorageKey` 全局唯一但后端信息不编码在其中。需要在元数据层记录 `backend_id`
- **跨后端 List**：List 操作需要聚合多个后端的键空间，复杂度显著增加
- **配置面**：需要 `STORAGE_BACKENDS`（复数）替代 `STORAGE_BACKEND`，每个后端有自己的名称、类型、认证、存储类映射

### 边界情况

| 场景 | 行为 |
|------|------|
| 对象创建时无存储类映射的后端 | 回退到 `default` 后端（保持当前行为） |
| 后端临时不可用（断路器打开） | 降级到次优后端 + 告警，不拒绝写入 |
| 后端间数据再平衡 | 后台 Job 扫描 `storage_class` 与实际后端不匹配的对象并迁移 |
| 跨后端 ListObjects 分页 | `NextMarker` 需要支持跨后端游标（字符串可排序即可） |
| 删除路径 | 根据 `Object.Backend` 路由到正确后端执行 `Delete` |

---

## 方向二：SSE 事件订阅过滤与多租户流控

### 现状

`GET /v1/events/stream` 使用 `events.Bus.Subscribe()` 获取一个无过滤全事件通道：

```go
// internal/api/rest/sse.go:liveStream
func (h *SSEHandler) liveStream(w http.ResponseWriter, r *http.Request, flusher http.Flusher, tenant string) {
    sub := h.bus.Subscribe()  // ← 获取全事件通道
    for {
        select {
        case e, ok := <-sub:
            if e.TenantID != tenant {  // ← 客户端过滤，浪费带宽
                continue
            }
            if !writeEvent(w, flusher, e) {
                return
            }
        }
    }
}
```

`Bus.Subscribe()` 返回无参通道，所有订阅者接收所有事件：

```go
// internal/events/bus.go:Subscribe
func (b *Bus) Subscribe() <-chan repository.Event {
    ch := make(chan repository.Event, b.subBuffer)
    b.mu.Lock()
    b.subs = append(b.subs, ch)
    b.mu.Unlock()
    return ch
}
```

有 100 个 SSE 连接 × 2000 事件/秒 = 每个连接接收 2000 事件/秒，其中绝大多数被 `e.TenantID != tenant` 过滤掉。网络带宽和 CPU 浪费严重。

### 产品价值

1. **多租户规模**：支持 1000+ 并发 SSE 连接而不使事件总线成为瓶颈
2. **带宽节省**：只在线上推送订阅者关心的子集，预计减少 80-99% 的 SSE 流量
3. **客户端简易性**：客户端无需自行过滤事件流，降低客户端复杂度
4. **回压信令**：当订阅者消费速度跟不上时，通过 `Last-Event-ID` 机制告知客户端跳过的偏移量

### 架构权衡

- **过滤谓词设计**：支持按 `event_type`（created/deleted/accessed）、`bucket`、`prefix`（key 前缀）、`tenant`（隐含当前）。参数通过 URL query 传递或 HTTP 头
- **订阅者标记**：`Subscribe` 需要接收过滤谓词，返回过滤后的通道。`Bus` 内部维护 `(filterPredicate, chan)` 映射
- **与现有 subscriber 兼容**：现有订阅者（Indexer、Webhook、Replication、Antivirus）使用无过滤通道，不应受影响
- **`Last-Event-ID` 续传**：重连后重新应用过滤谓词回放，确保不遗漏匹配的事件

### 边界情况

| 场景 | 行为 |
|------|------|
| 过滤谓词变更 | 客户端重新连接时携带新谓词；不支持运行时动态修改 |
| 高吞吐下的过滤性能 | 谓词检查在 `broadcast` goroutine 中执行，避免在 Publish goroutine 中阻塞 |
| 过滤后的空闲通道 | 相同过滤条件的多个订阅者共享同一个过滤后的内部通道以节省 goroutine |
| 与 `replayMissed` 的交互 | 回放时同样应用过滤谓词 |

---

## 方向三：后台作业基础设施：可观测性、管理面与智能节流

### 现状

`jobs.Pool` 是一个成熟的后台作业引擎，支持：
- 固定 worker 池 + 轮询领用
- 指数退避重试（±25% 抖动）
- panic 恢复
- 作业收割（Reaper）处理崩溃 worker
- 深度上限（`MaxDepth`）反压

但 admin 管理面极其有限：

```go
// internal/api/rest/admin_jobs.go
// 仅有的两个端点：
// GET  /v1/admin/jobs?status=&type=&limit=
// POST /v1/admin/jobs/{id}/retry
```

可观测性同样有限：

```go
// internal/telemetry/metrics.go
mJobsCompleted, _ = m.Int64Counter("jobs.completed_total")
mJobsFailed, _    = m.Int64Counter("jobs.failed_total")
mJobsRetried, _   = m.Int64Counter("jobs.retried_total")
// 只有计数器，没有：
// - 按类型的耗时直方图
// - 队列深度指标
// - 按 worker 的吞吐量
```

作业失败没有通知路由：

```go
// internal/jobs/jobs.go:runOne
if job.Attempts >= job.MaxAttempts {
    _ = p.repo.FailJob(ctx, job.ID, runErr.Error()) // 无通知
    telemetry.IncJobFailed(ctx, job.Type)            // 只计数
    return true, nil
}
```

### 产品价值

1. **运维可观测性**：Grafana dashboard 展示队列深度、per-type 耗时 P50/P95/P99、失败率趋势、worker 利用率
2. **运维控制面**：暂停/恢复特定类型的作业（例如批量索引暂停以空出资源给搜索请求）；手动取消卡住的作业
3. **智能节流**：基于近期错误率自动减慢重试频率（而非固定指数退避）；隔离出错的租户避免影响其他租户作业
4. **失败告警 webhook**：作业永久失败时触发 webhook，替代目前静默 FailJob

### 架构权衡

- **暂停/恢复**：需要在 Registry 或 Pool 层面维护 job type → bool 映射；`runOne` 检查 `paused` 状态并跳过
- **耗时直方图**：在 `execute` 前后记录时间，按 type 聚合为 `Float64Histogram`
- **智能节流**：每个 job type 维护滑动窗口错误率，超过阈值时降低轮询频率或跳过暂不处理
- **管理端点**：`POST /v1/admin/jobs/{type}/pause`、`POST /v1/admin/jobs/{type}/resume`、`POST /v1/admin/jobs/{id}/cancel`
- **失败 webhook**：在 `FailJob` 路径增加可选的 `onFailure` 钩子

### 边界情况

| 场景 | 行为 |
|------|------|
| 暂停索引时已有正在执行的作业 | 允许当前执行完成，不抢占；后续不再领用 |
| 取消正在执行的作业 | 通过 context cancellation 实现；worker 监听 ctx.Done |
| 智能节流误判（临时网络抖动） | 滑动窗口 + 最小降级持续时间的防御机制 |
| 失败 webhook 自身失败 | 不阻塞 FailJob；日志记录并继续 |

---

## 方向四：跨协议文件夹/目录命名空间语义统一

### 现状

三种协议对"文件夹"的理解完全不同：

**REST API（`/v1/folders`）：**
```go
// internal/api/rest/handler.go:CreateFolder
// 创建零字节标记对象，key 以 / 结尾
obj, err := h.svc.Put(r.Context(), tenant, service.DefaultBucket, folderPath+"/", nil, 0,
    service.PutOptions{ContentType: "application/x-directory"})
```

**S3 API（`/s3/{bucket}/{key+}`）：**
```go
// S3 客户端使用 delimiter=/ 进行虚拟目录层次划分
// ListObjectsV2 以 CommonPrefixes 返回虚拟目录
// 但 folderPath+/ 的零字节对象在 S3 中显示为常规对象
```

**WebDAV（`{WEBDAV_PREFIX}`）：**
```go
// internal/api/webdav/dav.go:davFS.OpenFile
// OS 文件系统语义：MKCOL 创建目录；以 / 结尾的 name 视为目录
// PROPFIND 通过 List 探测子对象是否存在来判定目录
```

结果：
- 通过 WebDAV MKCOL 创建目录 → REST `GET /v1/folders` 看到它（通过 prefix 推测）
- 通过 REST `POST /v1/folders` 创建目录 → WebDAV 能看到（通过 `strings.HasSuffix(name,"/")`）
- 通过 S3 SDK `PutObject` 写入 `uploads/2026/report.pdf` → REST `/v1/folders?path=uploads` 会看到 `uploads/` 作为虚拟文件夹
- 但 REST 创建的 `/` 后缀标记对象在 S3 ListObjectsV2 中会显示为一个空键对象，造成混淆
- 删除操作不一致：REST `DELETE /v1/folders` 会列出并删除所有子对象，WebDAV `RemoveAll` 只删除标记对象

### 产品价值

1. **客户端一致性**：无论使用哪种协议访问，文件夹结构表现一致
2. **消除歧义**：S3 客户端不再显示奇怪的零字节 `/` 后缀对象
3. **运维确定性**：删除文件夹在不同协议下表现一致（WebDAV 的 `RemoveAll` 也删除子对象）
4. **迁移零摩擦**：从 REST 管理迁移到 WebDAV 挂载或 S3 SDK 时，目录结构不变

### 架构权衡

- **统一 Namespace 层**：在 `FileService` 之上或内部增加 `NamespaceManager` 接口，统一处理三种文件夹语义
- **S3 兼容视角**：S3 虚拟目录（CommonPrefixes）不应暴露标记对象。在 ListObjects 路径过滤掉 `application/x-directory` 类型的对象
- **WebDAV 视角**：对尾随 `/` 的对象映射到目录；删除目录时递归删除子对象
- **REST 视角**：保持 `/v1/folders` 端点但底层统一使用虚拟目录推导（不创建标记对象）或保持标记对象但确保 S3 列表过滤
- **迁移路径**：现有标记对象需要后向兼容，新的文件夹创建可以统一为纯虚拟目录

### 边界情况

| 场景 | 行为 |
|------|------|
| 仅有标记对象但无子对象（空目录） | 保留标记对象作为目录存在证据；S3 列表时包含该目录 |
| 有子对象但无标记对象（纯虚拟目录） | 自动推导为目录（当前行为），不自动创建标记 |
| 通过 S3 PUT 创建显式标记对象 | 运行时识别为目录并过滤；可在 reconcile 中清理冗余标记 |
| WebDAV 重命名文件夹（Rename） | 需要递归重命名所有子对象 key 前缀 |
| 跨协议并发创建同名目录 | 先提交者胜出（乐观并发，依靠 FileService 已有的 ETag 条件写入） |

---

## 方向五：存储后端能力契约

### 现状

`Storage` 接口对所有后端一视同仁：

```go
// internal/storage/storage.go
type Storage interface {
    Put(ctx, key, r, size, opts) (ObjectInfo, error)
    Get(ctx, key) (io.ReadCloser, ObjectInfo, error)
    Stat(ctx, key) (ObjectInfo, error)
    Delete(ctx, key) error
    List(ctx, prefix, marker, limit) (ListResult, error)
    PresignGet(ctx, key, expiry) (string, error)
    PresignPut(ctx, key, expiry) (string, error)
    InitMultipart(ctx, key, opts) (MultipartInit, error)
    UploadPart(ctx, key, uploadID, partNumber, r, size) (MultipartPart, error)
    CompleteMultipart(ctx, key, uploadID, parts) (ObjectInfo, error)
    AbortMultipart(ctx, key, uploadID) error
    Backend() string
}
```

但不同后端的实际能力差异巨大：

| 能力 | Local | S3 | OSS | COS |
|------|-------|-----|-----|-----|
| Presigned URL | ✅（需配置 PublicURL+SignKey） | ✅ | ✅ | ✅ |
| 服务端拷贝 | ❌（仅模拟 read+write） | ✅ | ✅ | ✅ |
| 原生 SSE | ✅（envelope 加密） | ✅（SSE-S3/SSE-KMS） | ✅ | ✅ |
| 多分片上传 | ✅（临时文件） | ✅ | ✅ | ✅ |
| 强一致性 | ✅（FS） | ⚠️（最终一致性 List） | ⚠️ | ⚠️ |
| 对象标签 | ❌ | ✅ | ✅ | ✅ |

因为没有能力标记，系统被迫采用最低公分母行为：

```go
// internal/api/s3compat/extra.go:copyObject
// 无论后端是否支持服务端拷贝，一律 read+write：
rc, src, err := h.svc.Get(r.Context(), tenant, srcBucket, srcKey)
// ... 全量读入内存 ...
dst, err := h.svc.Put(r.Context(), tenant, dstBucket, dstKey, rc, src.Size, opts)

// 如果有 Capabilities 查询，可以：
// if store.Supports(StorageCapServerSideCopy) {
//     store.CopyObject(ctx, srcKey, dstKey)
// } else {
//     // fallback to read+write
// }
```

预签名 URL 配置缺失时静默失败：

```go
// internal/storage/local.go:PresignGet
func (s *LocalStorage) PresignGet(...) (string, error) {
    if s.cfg.PublicURL == "" || s.cfg.SignKey == "" {
        return "", errors.New("local presign disabled") // 调用方捕获不到
    }
    // ...
}
```

### 产品价值

1. **性能优化**：支持服务端拷贝的后端可以直接使用 CopyObject，避免大对象读+写的 IO 放大
2. **配置预检**：启动时查询后端能力，对不支持系统级特性（如 SSE、分片上传）给出明确告警
3. **自适应行为**：`FileService` 根据后端能力自动选择最优执行路径（服务端拷贝 vs 客户端拷贝）
4. **扩展性**：新增后端时声明式标记能力，`contract_test.go` 可跳过不适用测试

### 架构权衡

- **能力枚举**：定义 `StorageCapability` 枚举（`CapPresign`, `CapServerSideCopy`, `CapSSE`, `CapMultipart`, `CapTagging`, `CapStrongConsistency` 等）
- **`Capabilities()` 方法**：`Storage` 接口增加 `Capabilities() []StorageCapability` 方法，每个后端静态返回支持的能力
- **调用路径适配**：`FileService` 和 `s3compat` handler 在执行有条件操作前检查能力集
- **契约测试**：`RunContract` 依据能力集动态选择执行哪些子测试

### 边界情况

| 场景 | 行为 |
|------|------|
| 后端运行时变更能力（如 S3 切换到无 SSE 的 region） | 启动时确定，运行时不变；变更需要重启 |
| 预制 URL 配置未就绪 | `CapPresign` 返回 false，调用方自动降级（如返回 S3 原生 presign URL） |
| 混合后端（方向一的多后端路由） | 每个后端独立声明能力，路由选择时综合考虑能力 + 存储类 |
| S3 兼容存储（MinIO/Ceph） | 通过 capability 声明覆盖默认 S3 行为（MinIO 不支持某些 AWS 特有特性） |

---

## 优先级与建议执行顺序

| 方向 | 优先级 | 工作量估计 | 理由 |
|------|--------|-----------|------|
| **5. 存储能力契约** | P1 | ~2-3 天 | 基础架构改进，影响面最小但收益立竿见影——CopyObject 性能提升、预检告警。是多后端编排（方向一）的前置依赖 |
| **2. SSE 订阅过滤** | P2 | ~3-5 天 | 无数据迁移，纯运行时改进。多租户规模敏感场景为 P1，否则为 P2 |
| **3. 作业可观测性与管理面** | P2 | ~5-7 天 | 涉及 schema 扩展（可选）、metrics 扩展、admin API 扩展、Grafana dashboard 更新。适合在有作业密集型工作负载前完成 |
| **4. 跨协议命名空间统一** | P2 | ~5-8 天 | 涉及 S3 ListObjects 过滤逻辑调整、WebDAV 删除语义修正、backfill 迁移任务（清理冗余标记对象）。需要跨团队（如果存在）对齐行为预期 |
| **1. 多后端存储编排** | P1 | ~10-15 天 | 架构级变更，需要重构 `FileService` 的存储层引用方式、新增配置格式、迁移覆盖全部存储操作路径。建议在方向五之后进行，以利用能力契约 |

---

## 总结

经过对 237 个 Go 源文件、91 份既有分析文档的全量扫描和去重验证，本文识别了 5 个**此前未被深度独立覆盖**的高价值方向。其中最迫切的是**存储后端能力契约**（方向五）——它是实现多后端编排（方向一）的前置条件，且能以最小代码改动消除 `CopyObject` 读+写性能陷阱。**SSE 订阅过滤**（方向二）和**作业基础设施增强**（方向三）填补了多租户规模下的可观测性与可扩展性空白。**跨协议命名空间统一**（方向四）解决了三个协议表面下不一致的文件夹语义，是产品完备性的重要拼图。
