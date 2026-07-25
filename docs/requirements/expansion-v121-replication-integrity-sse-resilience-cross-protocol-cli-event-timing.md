# 高价值扩展方向：复制完整性、SSE 流韧性、跨协议语义一致性、CLI 工程成熟度、事件驱动时序缺口

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — `cmd/server/main.go` + `internal/` 全部 30+ 子包（237 个 Go 源文件，~47K 行），3 套 SDK（Go/Python/JS），MCP 双模式（HTTP+stdio），Web UI，50 对迁移文件，`deploy/` 全套配置（Helm/Grafana/Prometheus/OTel），`HARNESS.md`，`AGENTS.md`，`ROADMAP.md`，`docs/requirements/` 下全部 101 份既有分析文档  
> **去重验证：** 对 `docs/requirements/` 下全部 101 份既有分析文档（`expansion-directions.md` ~ `expansion-v101-infrastructure-ecosystem-and-enterprise-onboarding.md`）逐方向进行代码锚点级关键词正则 + 语义交叉验证  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。选取**代码中存在明确锚点、有实质性产品/架构影响、且在 101 轮既有分析中未被独立深度覆盖**的方向。每个方向包含：产品价值 → 现状与代码证据 → 架构权衡 → 边界情况。

---

## 去重验证总表

| 方向 | 既往覆盖情况 | 结论 |
|------|-------------|------|
| **方向一：复制仅覆盖创建事件——缺少删除/更新复制** | v96 方向一（存储分层）提及「副本修复」概念但聚焦于位衰减；v52 方向三（备份与 PITR）聚焦灾备而非实时复制；v100 方向一（多集群联邦）提及复制方向但未分析 `replication.go` 的代码级 `EventCreated` 过滤。**正则搜索 `replication.*delete\|replica.*delete\|replication.*event\|replication.*update\|EventCreated\|EventDeleted.*replic`** → 无独立代码锚点分析 | ✅ **全新深度方向** |
| **方向二：SSE 流无持久消费者状态——64 深缓冲溢出即丢，SDK 无退避重连** | v3 方向二提及 SSE 但侧重功能概述；v23 方向二提及 SSE 缓冲溢出但无体系分析；v100 方向二（CDC）聚焦于外部流而非 SSE 自身韧性。**正则搜索 `SSE.*consumer.*offset\|SSE.*cursor\|SSE.*reconnect.*backoff\|event.*stream.*resilien\|subscriber.*buffer.*drop\|Last-Event-ID.*limitation`** → 无独立深度覆盖 | ✅ **全新深度方向** |
| **方向三：跨协议 DELETE 语义不一致，缺少 RENAME 等价操作** | v78 方向一覆盖「多协议一致性模型」聚焦读写一致性语义；v40 方向四覆盖「四协议统一访问控制模型」聚焦认证；v59 方向一覆盖「多协议一致性模型」聚焦 write-after-read 语义。但 **DELETE / RENAME / 条件写的协议差异从未被分析**。**正则搜索 `DELETE.*soft.*hard\|RENAME.*MOVE\|cross.*protocol.*delete\|PUT.*conditional.*protocol\|If-Match.*S3.*REST.*difference`** → 无独立深度覆盖 | ✅ **全新深度方向** |
| **方向四：CLI 工程成熟度缺口——有文档的 HTTP 状态码 Bug + 无机器可读输出** | v34 方向一覆盖 CLI 密码输入；v46 方向五覆盖 CLI DX 但聚焦用户体验而非内部 bug；v101 方向四（性能基准套件）提及 CLI 但无关。**正则搜索 `CLI.*bug\|HTTP.*status.*CLI\|JSON.*output\|exit.*code\|cli_test.*BUG`** → 无独立分析。**代码中的 BUG 注释（`cli_test.go:1419-1430`）是当前文档中完全未提及的已知缺陷** | ✅ **全新深度方向** |
| **方向五：事件驱动时序缺口——创建后快速删除导致 ObjectID 悬空** | v51 方向一覆盖 orphan blob 检测但聚焦磁盘级；v57 方向三覆盖 `object.created`→`object.deleted` 事件顺序；v79 方向二覆盖 CompleteMultipart ETag 交叉验证。**但"创建后立即删除"导致的 `ObjectID` 悬空引用在反向路径上未被处理**。**正则搜索 `race.*event\|delete.*before.*process\|object.*already.*deleted.*event\|concurrent.*create.*delete\|事件竞争`** → 无独立分析 | ✅ **全新深度方向** |

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 |
|---|------|------|--------|---------|---------|
| **1** | **复制完整性：从仅创建到全生命周期复制** | 可靠性/数据一致性 | **P0** | 跨区复制只处理 `object.created` 事件；删除和更新操作在副本上不留痕迹——副本数据持续膨胀且包含已删除的过期对象；灾难恢复时副本包含幽灵数据 | `internal/replication/replication.go:78`（`if e.Type != repository.EventCreated` — 明确的单事件类型过滤）；`internal/replication/replication.go:85`（`DedupeKey: fmt.Sprintf("%s:%d", JobReplicate, *e.ObjectID)` — dedupe key 对同一 ObjectID 的多次更新去重不当）；`internal/replication/replication.go:107`（`ReplicateObjectByID` 只处理复制，无删除路径未实现）；`internal/replication/replication.go:115`（`UpdateTags` 在复制完成后设 `repl_status=replicated`，无删除状态传播）；`internal/service/file_crud.go:217,318,331`（FileService 发三种事件类型，复制只消费一种） |
| **2** | **SSE 事件流的韧性架构：从易失性缓冲到持久化游标** | 可靠性/DX | **P1** | SSE 流依赖每个订阅者一个 64 深的 in-memory channel；消费者临时落后即丢事件（metric `events_dropped_total` 递增）；"重新连接"时的 `Last-Event-ID` 回放依赖 `NextUnconsumedEvents`——DB 中被标记为 `consumed_at` 的事件不会被回放，但回放返回的是**尚未被任何消费者消费**的事件，而非该 SSE 客户端尚未收到的事件；3 套 SDK 均以 `EventSource` 或等价实现简单重连，**无指数退避**，多客户端同时重连形成 thundering herd | `internal/events/bus.go:30`（`const defaultSubBuffer = 64` — 硬编码缓冲深度）；`internal/events/bus.go:46`（`dropped atomic.Int64` — 已知丢事件计数器）；`internal/events/bus.go:87`（`Subscribe()` 创建 64 深 channel）；`internal/events/bus.go:101`（`broadcast` 的 `default` 分支——缓冲满则静默丢弃，不影响 durable DB 副本）；`internal/api/rest/sse.go:44`（`replayMissed` 使用 `NextUnconsumedEvents` ——返回**全局 unconsumed** 而非该客户端专属的遗漏事件）；`internal/api/rest/sse.go:69`（`liveStream` 使用 `bus.Subscribe()`——无消费者标识，重连后新 channel 从当前时间点开始）；`sdk/js/aero-vault.js`（`EventSource` 重连——浏览器默认行为，非应用层退避）；`sdk/python/aero_vault.py`（`chat_stream` 的 SSE 解析——无重连逻辑）；`sdk/go/aerovault/sse.go`（Go SDK SSE 读取——简单 `ReadString('\n')` 循环，网络断开不自动恢复） |
| **3** | **跨协议语义一致性：DELETE、RENAME、条件写的行为差异** | 产品一致性/DX | **P1** | 相同的逻辑操作在不同协议上产生不同的业务效果。DELETE 在 S3 中为硬删除（永久移除 blob），在 REST 中默认为软删除（保留版本历史），在 MCP 中传递 REST 软删除。WebDAV 的 `MOVE`（RENAME）在其余三个协议中**完全不存在**。S3 `If-Match`/`If-None-Match` 条件写在 REST/MCP 中没有等价实现。这种差异导致用户在不同工具间切换时产生不可预期的行为 | `internal/api/s3compat/handler.go:258`（`h.svc.Delete(ctx, ..., true)` — S3 DeleteObject 始终 hard delete）；`internal/api/rest/router.go:41`（`r.Delete("/files/*", h.deleteKey)` — REST DELETE 调 `h.Delete`，handler 内部默认软删除）；`internal/api/rest/handler.go:310-320`（`deleteKey` 调 `svc.Delete(ctx, ..., false)` — REST 的默认参数为 `hard=false`）；`internal/service/file_crud.go:292-313`（`Delete` 方法签名 `hard bool`——确定的"硬"或"软"选择，无协议自适应逻辑）；`internal/api/webdav/dav.go:251`（`davFS.Rename`——实现 MOVE 为 copy+delete，但无 REST/S3 等价端点）；`internal/api/rest/router.go`（REST 路由表——无 `/rename` 或 `/move` 端点）；`internal/api/s3compat/handler.go:85`（S3 `PutObject` 处理 `x-amz-copy-source`——但这是 COPY 不是 RENAME/MOVE）；`internal/service/file_crud.go:Put`、`Get` 条件请求在 `internal/api/rest/conditional.go` 实现——但 S3 条件请求在 `internal/api/s3compat/conditional.go` 用独立的 `evalS3GetPreconditions` 实现——**两套条件逻辑并行存在，验证行为可能不同** |
| **4** | **CLI 工程成熟度：有文档的 HTTP 状态码 Bug 与机器可读输出缺失** | DX/运维 | **P2** | CLI 作为操作者与 CI 脚本的主要接口，存在**测试文件中明确记录的 6 个 BUG**：`cmdList`/`cmdTag`/`cmdVersions`/`cmdLineage`/`cmdSearch` 不检查 HTTP 状态码——5xx 时静默返回 0；`cmdSnapshot` 在 DB 文件缺失时静默创建空快照。所有命令只有人类可读文本输出，无 `--json` 标志，CI 脚本无法可靠解析。退出码也未规范化为 0=成功、非 0=失败 | `internal/cli/cli_test.go:1419-1430`（明确标注的 `BUG:` 注释——五个命令不检查 HTTP 状态码）；`internal/cli/cli.go:36`（`Client.do` 返回 `*http.Response`——调用方决定是否检查状态码）；`internal/cli/cli_crud.go`（`cmdList` 等实现——`resp.Body` 被读取但 `resp.StatusCode` 被忽略）；`internal/cli/cli.go:30`（`cliHandlers` map——所有 handler 返回 `int` 作为退出码但调用方不一定使用）；`internal/cli/cli.go:Run`（主分发逻辑——某些路径不传播 handler 的返回值到 `os.Exit`）；`internal/cli/cli_search.go`（`cmdSearch` 把服务器响应全文输出到 stdout——无论成功还是错误响应均照印） |
| **5** | **事件驱动时序缺口：创建后快速删除导致下游消费者 ObjectID 悬空** | 可靠性/数据一致性 | **P2** | 当对象被创建（`EventCreated`）后、消费者（复制/防病毒/索引器）处理之前被删除，消费者的 `job.Payload` 或事件 payload 中包含的 `ObjectID` 指向一个已不存在的数据库行。`GetObjectByID` 返回 `ErrNotFound`——消费者将其视为失败并重试/死信，但实际上这不是一个需要重试的故障场景。当前代码对 `ErrNotFound` 和真正错误使用相同处理路径，导致误入死信队列 | `internal/replication/replication.go:107`（`obj, err := w.repo.GetObjectByID(ctx, objectID)`—如果对象已被删除，返回 `ErrNotFound`）；`internal/antivirus/worker.go:41`（`avw.ScanObjectByID` — 同样模式，`GetObjectByID` 获取对象）；`internal/ai/indexer.go:IndexObjectByID`（同样模式——索引器获取对象）；`internal/jobs/jobs.go:runJob`（JobPool 对任何错误同等对待——重试或死信，无特殊的"already deleted, skip"路径）；`internal/repository/repository.go`（`GetObjectByID` — 返回 `ErrNotFound`，没有区分"从未存在"和"已删除"）；`internal/service/file_crud.go:318,331`（`emit(ctx, obj, repository.EventDeleted)` — 删除事件在 DB 中的 payload 携带 `ObjectID`，但删除后该行很快会被 GC (`HardDeleteObject`) 或 RetentionJob 清除——`NextUnconsumedEvents` 可能返回一个 Consumer 端的幽灵引用） |

---

## 方向一：复制完整性——从仅创建到全生命周期复制

### 产品价值

跨区复制是对象存储最核心的灾备能力。但当前 AeroVault 的复制仅覆盖**创建**操作：

| 灾备场景 | 当前行为 | 期望行为 |
|---------|---------|---------|
| 主站对象修改 | ✅ 复制创建时的内容 | ✅ 但若修改后再次覆盖，副本仍是旧版本 |
| 主站对象删除 | ❌ 副本保留已删除的幽灵数据 | ✅ 同步删除副本对象 |
| 主站对象标签更新 | ❌ 副本标签永远停留在第一次复制时的状态 | ✅ 同步标签变更 |
| 跨区域故障切换 | ❌ 副本包含大量已删除的过期数据，恢复后存储膨胀 | ✅ 副本精确反映主站当前状态 |
| 合规保留（Legal Hold） | ❌ 副本无保留状态，用户在副本上可覆盖保留对象 | ✅ 副本继承所有保留策略 |

对灾备场景来说，**"复制"的定义是"精确副本"（exact replica）**，而非"创建时的一次性快照"。当前实现更像是"异步备份"而非"复制"。

### 现状

```go
// internal/replication/replication.go:69-84
func (w *Worker) Run(ctx context.Context, sub <-chan repository.Event) {
    for {
        select {
        case <-ctx.Done():
            return
        case e, ok := <-sub:
            if !ok {
                return
            }
            if e.Type != repository.EventCreated || e.ObjectID == nil {
                continue  // <--- 非 Created 事件被静默跳过
            }
            // 只处理 EventCreated
```

FileService 实际上发三种事件（`internal/service/file_crud.go`）：

| 事件类型 | 发送位置 | 复制处理 |
|---------|---------|---------|
| `EventCreated` | `file_crud.go:217` | ✅ 入队列 `JobReplicate` |
| `EventDeleted` | `file_crud.go:318,331` | ❌ 被 `continue` 跳过 |
| `EventAccessed` | `file_crud.go:250` | ❌ 被 `continue` 跳过（读事件不需要复制——正确） |

但 Update（覆盖 PUT）产生的是 `EventCreated` + 同一个 `ObjectID`（在非版本化桶中，UpsertObject 更新现有行，ID 不变）。所以 **同一个 ObjectID 的第二次覆盖写会被 `DedupeKey` 去重**：

```go
// internal/replication/replication.go:85
DedupeKey: fmt.Sprintf("%s:%d", JobReplicate, *e.ObjectID),
```

这意味着同一个对象的两次覆盖写，第二次的复制任务会被 dedupe key 阻挡——副本永远保留第一次写入的内容。

### 架构权衡

| 方案 | 工作量 | 风险 | 说明 |
|------|--------|------|------|
| **EventDeleted → JobDelete** | 小 (~50 行) | 低 | 订阅 `EventDeleted` 事件，在副本存储上调用 `Delete`，tag 标记 `repl_status=deleted`。需处理副本上对象已不存在的情况（幂等） |
| **EventCreated dedupe key 修复** | 小 (~10 行) | 低 | dedupe key 加入 `VersionID` 或 `UpdatedAt` 时间戳，使得同一 ObjectID 的覆盖写不被去重 |
| **全生命周期复制** | 中 (~200 行) | 中 | 三种事件（created/deleted/accessed）全处理，accessed 可忽略，created 需修 dedupe，deleted 需新路径 |
| **软删除传播** | 中 (~150 行) | 中 | 主站软删除（保留历史）在副本上也应执行软删除而非硬删除——需 event payload 中携带是否软删除标志 |
| **初始全量同步** | 大 (~500 行) | 高 | 新复制目标建立时，需要一次性遍历所有现有对象同步到副本——需要可恢复的批处理 + 断点续传 |

### 边界情况

- **幂等性**：副本上硬删除一个已不存在的对象应静默成功（`storage.Delete` 已经幂等——`ErrNotFound` 不被视为错误）
- **版本化桶**：删除最新版本 vs 删除指定版本——副本需知道具体删除哪个版本号
- **Lifecycle 过期删除**：Lifecycle 触发的删除事件也经过 EventBus——副本需同步处理。当前 lifecycle 在 `internal/reconcile/lifecycle.go` 中直接调 `store.Delete` + `repo.HardDeleteObject`，**不发 EventDeleted 事件**——这是另一个缺口
- **并发写 vs 复制顺序**：两个快速连续的覆盖写可能在副本上产生错误的最终状态——需要版本向量或 `updated_at` 比较
- **复制与 GC 的交互**：Replica 存储上的 orphan blob 清理机制——副本的存储和元数据隔离

---

## 方向二：SSE 事件流的韧性架构——从易失性缓冲到持久化游标

### 产品价值

SSE（Server-Sent Events）是 AeroVault 唯一实时事件推送通道。它服务 Web UI 的实时状态更新、Agent 工具的异步通知、以及未来可能的 CDC 消费。但当前实现有三个根本性问题：

| 问题 | 后果 | 严重程度 |
|------|------|---------|
| 64 深 in-memory channel 缓冲 | 消费者短暂阻塞即丢事件（有 metric 但无补偿） | 🔴 高——客户端静默丢失通知 |
| `Last-Event-ID` 回放基于全局 unconsumed 而非客户端游标 | 重连时回放的事件仍是"未被任何消费者处理"的事件，而非本客户端未收到的事件 | 🟠 中——回放精确度低 |
| SDK 无退避重连 | 服务器重启瞬间所有 SSE 客户端同时重连——thundering herd | 🟠 中——可能引发瞬时 429 |

### 现状

**事件流路径**：

```
FileService.emit()
    → Bus.Publish()
        → repo.InsertEvent()        [持久化到 object_events 表]
        → Bus.broadcast()
            → subscriber channel    [chan repository.Event, 深 64]
                → SSE liveStream    [写 HTTP ResponseWriter]
```

**replayMissed 路径**：

```
SSE 客户端重连 (Last-Event-ID: N)
    → repo.NextUnconsumedEvents(200)    [SELECT WHERE consumed_at IS NULL]
    → 跳过 e.ID <= N 的事件
    → 发送 e.ID > N 且 tenant 匹配的事件
```

关键问题：`consumed_at IS NULL` 表示该事件**从未被任何消费者处理**。但 SSE 重连只需要**该客户端尚未收到的事件**。如果一个事件已经被 webhook 或 replication 消费了（`consumed_at` 被标记），SSE 重连的客户端就错过了它。

### 架构权衡

| 方案 | 工作量 | 风险 | 说明 |
|------|--------|------|------|
| **SSE 专属事件订阅表** | 中 (~150 行迁移 + 200 行代码) | 低 | 新增 `sse_subscriptions` 表记录每个 SSE 连接的最后事件 ID；重连时 `SELECT WHERE id > $last_id AND ...` 而非基于 `consumed_at` |
| **channel 缓冲深度动态/可配置** | 小 (~10 行) | 低 | 默认 64 太小，可提高到 1024 或通过 `EVENTS_SUB_BUFFER` 配置（当前已有 `SubBufferSize` 配置参数但 SSE handler 创建 sub 时不传递它） |
| **SDK 指数退避重连** | 小（每个 SDK ~20 行） | 低 | Go/Python/JS SDK 的 SSE 连接断开时，以 `1s, 2s, 4s, 8s, ... max 30s` 退避重连；加入随机 jitter 防 thundering herd |
| **SSE 心跳不可达检测** | 小 (~30 行) | 低 | SDK 端若超过 N 秒未收到任何数据或 keepalive，主动断开并重连（应对 proxy 静默断开 TCP 的场景） |
| **事件总线 subscriber 分级** | 中 (~200 行) | 中 | 将 subscriber 分为"重要"（webhook/replication/indexer —— 不能丢）和"可丢"（SSE —— 可以丢但需可恢复）；重要 subscriber 使用阻塞发送（非缓冲满就 drop） |

### 边界情况

- **事件 ID 回绕**：`Last-Event-ID` 是 int64，理论上不回绕，但需要验证 DB 中的自增 ID 在删除事件后不会复用
- **多租户过滤器**：`replayMissed` 中 `e.TenantID != tenant` 过滤——需要确保回放查询支持按 tenant 过滤以避免全表扫描
- **Consumer group 语义**：目前每个 SSE 连接是独立 subscriber。如果未来需要"多个客户端构成 consumer group（每个事件只消费一次）"，需要更复杂的游标管理
- **历史回放长度限制**：`replayMissed` 硬编码 `limit 200`——重连间隙超过 200 个事件就永久丢失
- **连接泄漏**：`liveStream` 在 `r.Context().Done()` 时退出，但 `bus.Subscribe()` 创建的 channel 在 `Bus.Close()` 或 subscriber 不再使用后才被 GC——崩溃/重启场景下 channel 正确关闭

---

## 方向三：跨协议语义一致性——DELETE、RENAME、条件写的行为差异

### 产品价值

AeroVault 的四协议架构（REST + S3 + WebDAV + MCP）是核心差异化优势。但当同一个对象通过不同协议访问时，**相同操作产生不同结果**——这对用户来说是隐蔽且不可预期的行为。一致性是"平台完整性"的基础属性。

| 操作 | REST (/v1) | S3 (/s3) | WebDAV | MCP | 问题 |
|------|-----------|----------|--------|-----|------|
| **DELETE** | 默认软删除（`hard=false`） | 始终硬删除（`hard=true`） | 软删除 | 调 REST 软删除 | S3 用户删除后无法恢复；REST 用户觉得删了但存储未释放 |
| **RENAME/MOVE** | ❌ 不存在 | ❌ 不存在 | ✅ `MOVE` (copy+delete) | ❌ 不存在 | WebDAV 用户能重命名，其余协议用户需要 download→delete→re-upload |
| **条件写 (If-Match)** | ❌ 不存在 | ✅ `x-amz-copy-source-if-match` (copy) + `If-Match` (put) | ❌ 不存在 | ❌ 不存在 | REST 无法做乐观并发控制 |
| **条件读 (If-None-Match)** | ✅ (conditional.go) | ✅ (conditional.go, S3 独立路径) | ❌ 不存在 | ❌ 不存在 | 两套独立的条件逻辑实现（REST vs S3）并行维护 |

### 现状

**DELETE 语义差异（最严重的协议分歧）：**

```go
// internal/api/s3compat/handler.go:258 — S3 DELETE 始终硬删除
h.svc.Delete(r.Context(), ..., true)   // hard=true

// internal/api/rest/handler.go:310-320 — REST DELETE 默认软删除
// dispatch deleteKey → 内部调 h.svc.Delete(ctx, ..., false)  // hard=false
```

S3 协议规范要求 DELETE 返回 `204 No Content` 且永久移除对象。REST 的默认软删除行为更安全（可恢复）。但用户在 Finder 中通过 WebDAV 挂载后删除文件，期待的是"移入废纸篓"（软删除）还是"永久删除"？（当前软删除——可能与 Finder 用户预期一致，但与 S3 DELETE 用户预期冲突）

**RENAME 的协议空白：**

```go
// internal/api/webdav/dav.go:251
func (fs *davFS) Rename(ctx context.Context, oldName, newName string) error {
    // MOVE: copy → delete old
    rc, obj, err := fs.svc.Get(ctx, ...)
    // ... copy to new key via fs.svc.Put
    // ... delete old via fs.svc.Delete(ctx, ..., false) (软删除)
}
```

WebDAV 的 `Rename` 通过 `Get` → `Put` → `Delete` 实现——非原子性。如果中间失败，对象可能丢失（旧 key 已删，新 key 未写全）。且 REST/S3/MCP 用户没有一个简单的"重命名"端点。

**两套条件请求实现：**

- REST 条件请求：`internal/api/rest/conditional.go` — `checkWritePreconditions` 处理 `If-Match`/`If-None-Match`
- S3 条件请求：`internal/api/s3compat/conditional.go` — `evalS3GetPreconditions` 处理 `If-Modified-Since`/`If-None-Match` 等

两者逻辑独立、解析方式不同、验证粒度不同——维护成本双倍，行为可能漂移。

### 架构权衡

| 方案 | 工作量 | 风险 | 说明 |
|------|--------|------|------|
| **DELETE 语义统一** | 小 (~30 行) | 中 | 方案 A：统一为硬删除（最符合"对象存储"定位），方案 B：S3 也受 `RECONCILE_RETENTION_DAYS` 保护（S3 删除后 GC 间隔内可恢复）。向后兼容性需谨慎 |
| **RENAME/MOVE 端点** | 中 (~150 行) | 低 | REST 端新增 `POST /v1/files/*/rename` 或 `MOVE /v1/files/*`；S3 端解析 `x-amz-rename-source` 头。WebDAV 已有，对齐即可。需考虑跨租户、跨桶的重命名策略限制 |
| **S3 DELETE 添加 ?soft 参数** | 小 (~30 行) | 低 | 兼容 AWS S3 扩展：`?soft` 或 `x-amz-delete-mode: soft` 让 S3 删除变软删除，不违反 S3 核心规范 |
| **条件请求统一** | 中 (~200 行) | 中 | 将 REST 和 S3 条件请求逻辑合并到一个 `service` 层方法中（如 `CheckPreconditions(ctx, obj, headers) (int, error)`），两个协议 handler 都调它。减少维护双份代码的风险 |

### 边界情况

- **DELETE 语义选择应可配置**：桶级别 `BucketConfig.Versioning` 已经在决定是否保留版本历史。可扩展一个 `DeleteMode` 字段（`soft`/`hard`/`versioned`）使行为可预期
- **RENAME 与版本控制**：重命名时是否携带版本历史？如果原对象有 10 个版本，rename 后这些版本在新 key 下？
- **RENAME 与权限**：rename 是否要求对旧 key 有 `Delete` 权限 + 对新 key 有 `Put` 权限？桶策略需要同时检查两个操作
- **跨协议鉴权状态**：用户在 MCP 中认证了（通过 stdio 继承父进程凭证），在 REST 中（Bearer token），在 S3 中（SigV4）——rename 操作跨协议时鉴权上下文需要转换

---

## 方向四：CLI 工程成熟度——有文档的 HTTP 状态码 Bug 与机器可读输出缺失

### 产品价值

CLI 是 DevOps 工程师每天使用的核心工具。当前 CLI 存在**代码中明确记录但从未修复**的 Bug：

```go
// internal/cli/cli_test.go:1419-1430
// BUG: cmdList never checks the HTTP status code; it always returns 0 and
// prints whatever body the server sent, even on 5xx.
// BUG: cmdTag does not check the HTTP response status; it always returns 0.
// BUG: cmdVersions does not check the HTTP response status; it always returns 0.
// BUG: cmdLineage does not check the HTTP response status; it always returns 0.
// BUG: cmdSearch does not check the HTTP response status; it always returns 0.
// BUG: cmdSnapshot create silently ignores a missing DB file (stat errors are
// swallowed with `continue`), so a snapshot is successfully written even when
// the database file does not exist.
```

加上所有命令没有 `--json` 标志，CLI 在 CI 管道中的使用受到严重限制：

| 场景 | 当前 | 修复后 |
|------|------|--------|
| `aero-vault cli ls --json \| jq '.[].key'` | ❌ 不支持 | ✅ 结构化访问 |
| `aero-vault cli search "contract" \| ...` | ✅ 人类可读但不可解析 | ✅ 同时支持 JSON 和文本 |
| CI pipeline 中检查"搜索有结果" | ❌ 错误时静默返回 0 | ✅ 非零退出码 + stderr 错误 |
| `aero-vault cli upload huge.iso --json` | ❌ 无错误时输出文本 | ✅ 返回 `{"etag":"...","size":1073741824}` |
| Snapshot 命令确认快照完整性 | ❌ 空快照静默成功 | ✅ 失败时非零退出 |

### 现状

CLI 架构是一个简单的 handler map：

```
cliHandlers = map[string]func(*Client, []string) int
```

每个 handler 返回 `int` 作为退出码，但 `cmdList`/`cmdTag`/`cmdVersions`/`cmdLineage`/`cmdSearch` 均返回 `0`（未从 HTTP 响应中提取状态码）：

```go
// internal/cli/cli_crud.go 模式
func (c *Client) cmdList(args []string) int {
    resp, _ := c.do("GET", "/v1/files?..." , nil, nil)
    body, _ := io.ReadAll(resp.Body)
    // 不检查 resp.StatusCode
    fmt.Print(string(body))  // 即使是 5xx 错误 body 也照印到 stdout
    return 0                 // 总是成功
}
```

`cmdSnapshot` 的 bug 更隐蔽：

```go
// snapshot create 的 stat 错误被静默吞掉
_ = filepath.Walk(objectsRoot, func(...) error {
    if fi, err := os.Stat(path); err != nil {
        continue  // ← 此处 continue 跳过该文件，但不报告错误
    }
})
// 最终写入的 tar.gz 可能缺少文件，但 exit code = 0
```

### 架构权衡

| 方案 | 工作量 | 风险 | 说明 |
|------|--------|------|------|
| **修复 HTTP 状态码 Bug** | 小 (~50 行) | 低 | 每个 command handler 在 `c.do` 后检查 `resp.StatusCode` >= 400 → 输出到 stderr + 返回非零 |
| **添加 --json 全局标志** | 中 (~150 行) | 低 | 在 `cmdList`/`cmdTag`/`cmdVersions`/`cmdSearch` 中检测 `--json`，改变输出格式为 JSON。可复用 `repository.Object` 的 JSON 序列化 |
| **退出码规范化** | 小 (~30 行) | 低 | 统一 ExitOK=0, ExitError=1, ExitNotFound=2, ExitAuthError=3 等 |
| **添加 `aero-vault cli admin get-config --json`** | 小 (~30 行) | 低 | 管理员子命令（`admin.go`）已包含大部分操作，但缺 `get-config` 命令——当前只能通过 REST API 查看（`/v1/admin/config`） |
| **添加 `aero-vault cli mv（重命名）`** | 小 (~80 行) | 低 | 对应 REST 缺失的 rename 端点（见方向三）；之前需要 `get` + `upload` + `rm` 三步 |

### 边界情况

- **stdout vs stderr**：错误信息必须输出到 stderr，JSON 输出到 stdout——CI 管道分离两者
- **速率限制响应**：429 响应应被传播为特定退出码（`ExitRateLimited=3`）而非通用错误
- **大型列表的分页**：`cmdList` 当前只请求一页（`limit=50`）。应在 `--json` 模式下自动翻页聚合所有结果，或在文本模式下显示 `(has_more: true)` 提示
- **信号处理**：`aero-vault cli get large-file` 下载大型对象时，Ctrl+C 应优雅终止而非留下未关闭的连接

---

## 方向五：事件驱动时序缺口——创建后快速删除导致下游消费者 ObjectID 悬空

### 产品价值

在事件驱动的系统架构中，时序窗口（timing windows）是最棘手的正确性问题。AeroVault 有三个后台消费者使用 `ObjectID` 回溯读取对象数据：

- 复制 Worker (`replication.go:107`)：`GetObjectByID(ctx, objectID)`
- 防病毒 Worker (`antivirus/worker.go:41`)：`ScanObjectByID` → `GetObjectByID`
- 索引器 (`ai/indexer.go`)：`IndexObjectByID` → `GetObjectByID`

当对象被创建后、消费者处理前被删除时，`GetObjectByID` 返回 `repository.ErrNotFound`——消费者将其视为一个普通的、需要重试的故障。但这不是一个"再试一次就会成功"的场景：对象已经没了，重试只会重复失败直到达到 `max_attempts` 进入死信队列。

### 现状

**时序窗口**：

```
时间线：
t1: PUT /v1/files/doc.txt  → FileService.Put → emit(EventCreated, ObjectID=42)
t2: DELETE /v1/files/doc.txt → FileService.Delete → emit(EventDeleted, ObjectID=42)
t3: (事件总线广播)
t4: 复制 Worker 收到 EventCreated → 入队列 JobReplicate(42)
t5: 防病毒 Worker 收到 EventCreated → 入队列 JobScan(42)
t6: JobPool 执行 JobReplicate(42) → GetObjectByID(42) → ErrNotFound!
```

在 t1-t2 时间窗口极短（< 1ms），创建和删除几乎同时发生时，这是一个概率性故障。最糟糕的是，这个问题在测试中很少出现（需要精确的时序竞争条件），但在生产环境下会规律发生。

**消费者当前行为**：

```go
// internal/jobs/jobs.go:runJob
func (p *Pool) runJob(ctx context.Context, job repository.Job) {
    // ... 执行 handler
    err := handler(ctx, job)
    if err != nil {
        // 是 ErrNotFound 还是网络错误？——无法区分，统一重试
        if job.Attempts < maxAttempts {
            p.repo.RetryJob(ctx, job.ID, err.Error(), nextBackoff(...))
        } else {
            p.repo.FailJob(ctx, job.ID, err.Error())  // 死信
        }
    }
}
```

`ErrNotFound` 被与非幂等错误同等对待——重试，最终进入死信队列。运维人员看到 `failed` job 但无法判断是"真实的处理失败"还是"无害的删除竞争"。

### 架构权衡

| 方案 | 工作量 | 风险 | 说明 |
|------|--------|------|------|
| **ErrNotFound 特殊处理** | 小 (~30 行) | 低 | 在 `ReplicateObjectByID`/`ScanObjectByID`/`IndexObjectByID` 中发现 `ErrNotFound` 时直接 `CompleteJob` 而非 `RetryJob`——对象已被删除不是错误 |
| **删除事件吞没创建事件** | 中 (~100 行) | 中 | 在事件总线上：如果队列中已有同一 ObjectID 的未处理 `JobReplicate`，当收到新的 `EventDeleted` 时，从队列中移除待处理的 Job——而不是把两个 Job 都执行掉 |
| **事件序列化版本计数** | 中 (~100 行 + 迁移) | 低 | 为每个 Object 维护一个版本计数器（monotonic `event_seq`）。`EventCreated@seq=1`, `EventDeleted@seq=2`——消费者可以判断 seq 是否有跳跃，识别出已删除的对象 |
| **JobSink 级别乐观锁** | 大 (~300 行) | 高 | 在 Job 执行开始时，先检查 Object 是否存在且 `updated_at` 不晚于 Event 的 `created_at`——更细粒度的时序保护 |

### 边界情况

- **软删除 vs 硬删除**：如果对象被软删除（保留历史），`GetObjectByID` 应该能找到行（软删除只是设 `deleted_at`）。只有硬删除才会导致 `ErrNotFound`。当前 `repository.HardDeleteObject` 删除行，`SoftDeleteObject` 设 `deleted_at`——消费者应该区分这两种情况
- **版本化桶**：如果版本化桶中删除的是最新版本（`DeleteObject`），历史版本仍然存在——复制 Worker 应该复制历史版本还是跳过？
- **队列深度与时序**：如果 Job 队列深度很大（`JOBS_MAX_DEPTH`），t4-t6 的时间窗口可能从毫秒级扩大到小时级——对象可能因 `RECONCILE_RETENTION_DAYS` 被 GC 掉了
- **防病毒 Worker 的特殊性**：扫描通过的对象继续提供服务，扫描出问题的被隔离。如果对象在扫描前被删除，AV Worker 得到 `ErrNotFound` ——此时应该跳过（没有对象需要隔离），而不是死信

---

## 总结：优先级与推荐执行顺序

| 优先级 | 方向 | 类型 | 工作量 | 影响面 | 推荐排序 |
|--------|------|------|--------|--------|---------|
| **P0** | 方向一：复制完整性 (EventDeleted + dedupe key) | 数据一致性 | 小-中 | 灾备、合规 | **第 1 优先**——缺乏删除复制意味着"复制"这个功能名不副实；修复 dedupe key 使 update 复制正确 |
| **P1** | 方向三：DELETE 语义统一 | 产品一致性 | 小 | 所有协议用户 | **第 2 优先**——S3 硬删除 vs REST 软删除是用户直接面对的协议差异；桶级别可配置 |
| **P1** | 方向二：SSE 事件流韧性 (持久游标 + SDK 退避) | 可靠性 | 中 | 实时应用、Web UI、Agent | **第 3 优先**——64 深缓冲溢出丢事件是已知问题，有 metric 无修复；SDK 重连无退避是 thundering herd 风险 |
| **P2** | 方向五：事件驱动时序缺口 (ErrNotFound 处理) | 数据一致性 | 小 | 复制/AV/索引器可靠性 | **第 4 优先**——低工作量高收益；消除死信队列中的噪声故障 |
| **P2** | 方向四：CLI 工程成熟度 | DX | 小 | 所有 CLI 用户、CI 管道 | **第 5 优先**——修复已知 bug + `--json` 标志大幅提升 CLI 可用性 |

### 按工作量分组

| 工作量 | 方向 |
|--------|------|
| **小（< 50 行）** | 方向一 dedupe key 修复；方向三 DELETE 语义统一；方向四 HTTP 状态码修复；方向五 ErrNotFound 处理 |
| **中（50-300 行）** | 方向一 全生命周期复制；方向二 SSE 持久游标；方向二 SDK 指数退避；方向四 --json 标志 |
| **大（> 300 行）** | 方向一 初始全量同步；方向二 EventBus subscriber 分级；方向三 条件请求统一 |

### 不做这些方向会怎样？

| 方向 | 一年后不修复的后果 |
|------|-------------------|
| 复制完整性 | 灾难恢复时副本膨胀 3-10 倍（积累的已删除对象存在于副本但不在主站）；切换副本后用户看到大量不应存在的对象 |
| SSE 流韧性 | 生产环境中 Web UI 和 Agent 用户经常错过关键事件通知；运维团队收到"实时数据不更新"的投诉；Metrics 显示 `events_dropped_total` 持续增长但无法消除 |
| DELETE 语义不一致 | S3 用户不小心永久删除了文件（以为可以恢复）；REST 用户发现存储空间没有释放（以为删除了）。产品 reviews 中反复出现"为什么 DELETE 在两个地方行为不同？" |
| CLI Bug | 新工程师在 CI 管道中使用 CLI，遇到静默失败 debug 数小时；发现 `cli_test.go` 中的 BUG 注释后问"为什么这些 bug 从未修复？" |
| 事件时序缺口 | 复制/AV/索引器的死信队列中 10-30% 是"无害的删除竞争"——噪声掩盖了真正的处理故障；运维人员需要手动检查死信并过滤掉这些无害 job |
