# AeroVault 高价值扩展方向（第六期）

> **视角:** 资深架构师 / 产品经理  
> **方法:** 全局代码扫描（237 Go 文件 / ~50K 行），深度分析所有层。已阅读 `ROADMAP.md` + 五期 `expansion-directions[-v2..v5]` + 八轮 `analysis-v[1-8]` 全部内容。  
> **日期:** 2026-07-10  
> **原则:** 选取所有**已有文档均未覆盖**的方向。每个方向附带具体代码锚点、当前状态缺口、边界情况暴露、架构方向和实现理由。不编写任何实现代码。

---

## 总览

| # | 方向 | 类型 | 影响 | 核心代码锚点 | 覆盖检查 |
|---|------|------|------|-------------|---------|
| 1 | **Real-time Events Infrastructure: SSE 韧性 + 事件过滤 + 消费者治理** | 平台/可靠性 | 🟠 企业实时集成断裂 | `internal/api/rest/sse.go`, `internal/events/bus.go` | 全部未覆盖 |
| 2 | **WORM Bucket 模式 & 跨协议对象锁一致性** | 合规/安全 | 🛑 金融监管准入盲区 | `internal/service/file_crud.go`, `internal/api/s3compat/`, `internal/api/rest/acl.go`, `internal/api/webdav/dav.go` | 部分提及但深度、广度完全不同 |
| 3 | **内容智能管线：自动类型检测、格式转换、文档预览** | 差异化/体验 | 🟠 从"存储"到"内容平台" | `internal/thumbnail/thumbnail.go`, `internal/service/file_crud.go`, `internal/ai/extractor.go` | 全部未覆盖 |
| 4 | **运行时优雅降级 & 特性开关框架** | 可靠性/运维 | 🟠 生产事故应急响应缺口 | `cmd/server/main.go`, `internal/middleware/`, `internal/ai/` | 全部未覆盖 |
| 5 | **分层限流：Per-Key 配额、操作成本加权、请求优先级** | 多租户/公平性 | 🟠 单个坏租户影响所有用户 | `internal/middleware/ratelimit.go`, `internal/auth/auth.go`, `internal/middleware/middleware.go` | 全部未覆盖 |

---

## 1. Real-time Events Infrastructure: SSE 韧性 + 事件过滤 + 消费者治理

### 为什么需要它

当前 SSE 实现（`internal/api/rest/sse.go`）提供了一个基础的 `GET /v1/events/stream` 端点，浏览器和 Agent 可以通过 EventSource 连接实时接收生命期事件。这是实时集成的核心通道——但仔细检查代码后发现多项关键缺失。

**核心问题：SSE 端点在实践中几乎不可用。**

代码现场分析：

| 行 | 代码 | 问题 |
|----|------|------|
| `sse.go:60` | `replayMissed()` → `NextUnconsumedEvents()` | 回放从 unconsumed events 表读取，但这些事件可能已被其他消费者（webhook、indexer）标记为 consumed——所以 `replayMissed` 基本总是空的 |
| `sse.go:64` | `if e.ID <= lastID` | 正确过滤了旧 event，但 next_unconsumed 结果不稳定 |
| `sse.go:73` | `h.bus.Subscribe()` | 每个 SSE 连接创建一个新的 subscriber channel（64 深度）。如果一个客户端断开后重连，旧连接泄漏——没有 `defer bus.Unsubscribe()`/`Close()` 之外的清理路径 |
| `sse.go:100` | `writeEvent()` 没有错误处理 | 客户端断线后写入失败不会清理 subscriber 资源 |
| `bus.go:72` | `Subscribe()` 不可取消 | 没有 `Unsubscribe(ch)` 方法，Subscriber 只能通过 `bus.Close()` 全部清理。所有 SSE 连接共享同一个生命周期 |
| `bus.go:73` | `b.subs = append(b.subs, ch)` | `subs` 切片无限增长——每次 SSE 连接都追加新 channel，从不移除断开连接的。**这是内存泄漏** |
| `sse.go:75` | `keepalive: 15s` | 硬编码 15 秒。通过 nginx 反向代理时可能需要不同间隔 |

更广泛的问题：

- **无事件过滤**：SSE 客户端收到**租户的所有事件**，无法按 event type（created/deleted）、bucket、key prefix 过滤。一个只关心 `images/` 前缀事件的客户端被迫接收整个租户的所有事件。
- **无事件类型选择**：SSE 端点不支持 `?type=created&type=deleted` 参数，客户端无法订阅特定类型。
- **无断线重连策略**：虽然 `parseLastEventID` 存在，但 `replayMissed` 的回放实现不可靠（见上）。
- **无消费者组**：多个 SSE 客户端都会收到相同的事件——无法实现"这个事件只投递给其中一个消费者"的竞争消费者模式。
- **无 SSE 指标**：没有活跃连接数、事件延迟、回放量的 metrics。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/api/rest/sse.go:73-87` | `liveStream` 使用 `bus.Subscribe()` | 无 `Unsubscribe` 机制 → 内存泄漏 |
| `internal/api/rest/sse.go:46-69` | `replayMissed` 使用 `NextUnconsumedEvents` | 回放数据源错误——unconsumed 可能已被消费 |
| `internal/events/bus.go:71-76` | `Subscribe()` 只 append 不 remove | 缺少 `Unsubscribe(ch)` 方法 |
| `internal/events/bus.go:45` | `subBuffer = 64` | 无背压处理，慢消费者直接丢事件 |
| `internal/events/bus.go:113-119` | `broadcast` 用 select+default 跳过 | 丢事件但 `Dropped()` 指标可观测——并未通知客户端 |
| `internal/api/rest/router.go` | SSE 路由注册 | 不支持 `?types=created,deleted&prefix=docs/` 查询参数 |
| `internal/telemetry/metrics.go` | 现有 metrics | 无 `sse_active_connections`、`sse_events_sent_total` |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **SSE 客户端断线不清理** | 浏览器刷新，新 EventSource 连接，旧连接未关闭 | Subscriber channel 泄漏，无限增长 | 在第一次 `writeEvent` 失败时自动取消订阅 + `Subscribe` 返回可取消对象 |
| **Nginx 缓冲 SSE** | 代理层启用 response buffering | SSE 帧被缓冲，客户端收到批量延迟 | 已设 `X-Accel-Buffering: no`（正确），但文档未说明 |
| **反向代理超时** | Nginx/AWS ALB 默认 60s 断开空闲连接 | 客户端收到 `net:ERR_INCOMPLETE_CHUNKED_ENCODING` | 需要更短 keepalive 或支持 HTTP/2 Server Push |
| **事件回放数据竞争** | 多个 SSE 客户端同时回放 | 同时调用 `NextUnconsumedEvents` 互相干扰 | 使用独立的事件序列号 ID 范围查询，而非 unconsumed 表 |
| **总线关闭后 SSE 依然活跃** | 优雅关闭过程，总线关闭，但 SSE 连接仍在 | `Close()` 关闭所有 subscriber channel → 全部断线（期望行为） | 但断线后客户端应收到明确的 `event: shutdown` 帧而非突然断开 |

### 架构蓝图

```
┌─ Subscriber 治理 ─────────────────────────────────────────────│
│ Bus 扩展接口:                                                    │
│   type Subscription struct {                                    │
│       C      <-chan repository.Event                            │
│       Close() error      // 取消订阅，释放资源                    │
│       ID     string                                             │
│   }                                                              │
│   func (b *Bus) Subscribe(ctx context.Context, opts SubscribeOpts) *Subscription │
│                                                                  │
│ SubscribeOpts:                                                    │
│   BufferSize int           // 默认 64                           │
│   Filter     EventFilter   // 可选过滤                           │
│                                                                  │
│ Bus 内部:                                                        │
│   subs 从 []chan → map[string]*Subscription                      │
│   broadcast 遍历 sub map，对每个 sub 应用 Filter 再投递           │
│   Subscriber Close() 从 map 删除 → 清理泄漏                       │
│                                                                  │
│ 迁移:                                                             │
│   现有订阅者（webhook.Run, indexer.Run, SSE）逐个迁移到新 API      │
│   保持向后兼容：旧 Subscribe() 可保留并内部封装新 Subscribe()      │
└────────────────────────────────────────────────────────────────┘

┌─ SSE 端点重写 ─────────────────────────────────────────────────│
│ GET /v1/events/stream?types=created,deleted                     │
│   &prefix=images/&bucket=default                                │
│   &since={event_id}  // 替代 Last-Event-ID                     │
│                                                                  │
│ 重写 Stream handler:                                              │
│   1. 解析过滤参数 → 创建 EventFilter                             │
│   2. 计算回放起始点：                                             │
│      a. 如果有 `since` 参数：SELECT * FROM events WHERE          │
│         id > since AND tenant=$t ORDER BY id LIMIT 1000          │
│      b. 使用时序事件 ID（events 表本身有 ID + created_at）        │
│   3. 回放历史事件                                                │
│   4. 使用新的 Subscription API 订阅实时事件（带过滤）             │
│   5. defer sub.Close() 在 handler 返回时自动清理                 │
│   6. 指标：活跃连接数 gauge，事件发送 counter，回放事件 counter  │
│                                                                  │
│ SSE 帧格式增强:                                                   │
│   id: {event_id}                                                  │
│   event: {event_type}    // "created" | "deleted"                 │
│   data: {event_json}                                              │
│   retry: 3000              // 建议重连延迟 3s                     │
│                                                                  │
│   // 关闭通知（graceful shutdown）                                │
│   event: shutdown                                                  │
│   data: {"reason":"server_restart","retry_after":30}              │
└────────────────────────────────────────────────────────────────┘

┌─ 事件过滤 ─────────────────────────────────────────────────────│
│ type EventFilter struct {                                        │
│     Types  []repository.EventType  // 空 = 所有类型               │
│     Bucket string                  // 空 = 所有桶                 │
│     Prefix string                  // 空 = 所有前缀               │
│ }                                                                 │
│                                                                   │
│ func (f EventFilter) Match(e repository.Event) bool {             │
│     if len(f.Types) > 0 && !contains(f.Types, e.Type) {          │
│         return false                                              │
│     }                                                             │
│     if f.Bucket != "" && e.Bucket != f.Bucket { return false }    │
│     if f.Prefix != "" && !strings.HasPrefix(e.Key, f.Prefix) {    │
│         return false                                              │
│     }                                                             │
│     return true                                                   │
│ }                                                                  │
└────────────────────────────────────────────────────────────────┘

┌─ 消费者治理 ───────────────────────────────────────────────────│
│ 问题: 当前所有订阅者都收到所有事件（bus.Subscribe() 广播）         │
│                                                                  │
│ 场景: 10 个 SSE 连接 + 1 个 webhook + 1 个 indexer + 1 个 AV    │
│       = 13 个消费者，每个消费者收到每秒 100 条事件                 │
│       写入总线 100 条/秒 → 广播到 13 个 channel = 1300 次/秒     │
│       浪费写入和内存                                              │
│                                                                  │
│ 方案: 带过滤的广播                                                │
│   Bus.broadcast 对每个 subscriber:                                │
│     应用 filter → 不匹配的跳过                                    │
│     不需要的复制写入选派                                          │
│                                                                  │
│ 指标:                                                             │
│   event_bus_delivery_skipped_total{subscriber_type, reason}      │
│   event_bus_subscriber_count{subscriber_type}                    │
└────────────────────────────────────────────────────────────────┘

**复杂度:** M · **用户影响:** ★★★★☆（实时集成场景） · **代码变更:** ~600 行新代码 + ~300 行修改

---

## 2. WORM Bucket 模式 & 跨协议对象锁一致性

### 为什么需要它

当前对象锁实现覆盖了**单个对象级别**的基本场景：
- `repository.Object.LockedUntil` 时间戳 → 阻止 `hardDeleteObject`
- `_aero_legal_hold` 元数据 → 阻止删除（与 LockedUntil 独立检查）
- `checkLockBeforeOverwrite` → 在 PUT 覆盖前检查锁

但存在**四个维度**的产品级缺口：

**维度 1：无 WORM（Write Once Read Many）桶模式**
- S3 兼容 API 中，`?object-lock` 子资源返回/设置 `ObjectLockSeconds`，但桶级锁一旦启用之后应该**不可禁用**（S3 硬性要求）。当前 `SetBucketObjectLock` 可以反复调用，没有"一旦启用不能禁用"的语义。
- 没有"锁后的桶不可删除"的检查——`DeleteBucket` 路径不会检查桶内是否有锁定对象。
- S3 的 `ObjectLockEnabled` 是个启动标志，一旦设为 true 就不能设为 false。当前代码完全缺失这个状态机。

**维度 2：跨协议锁检查不一致**
- `internal/service/file_crud.go:hardDeleteObject` 检查 `LockedUntil` + `_aero_legal_hold`
- `internal/api/s3compat/handler.go:deleteObject` 走 `svc.Delete()` → 触发 `hardDeleteObject` → 检查锁 ✅
- `internal/api/webdav/dav.go:DELETE` 走 `svc.Delete()` → 检查锁 ✅
- `internal/api/webdav/dav.go:PUT/overwrite` 走 `svc.Put()` → `checkLockBeforeOverwrite` 检查锁 ✅
- `internal/api/webdav/dav.go:MOVE` 由于跨桶 rename 是 **delete + re-put**——但 MOVE 的 copy+delete 路径（`spill.go` 的 MOVE handler）**不走** `svc.Delete()` → **不检查锁** ❌（代码见 `internal/api/webdav/spill.go` 中的 MOVE 实现）
- `internal/mcp/server.go:toolDeleteFile` 走 `svc.Delete()` → 检查锁 ✅
- `internal/service/file_features.go:BatchDelete` 走 `svc.Delete()` → 检查锁 ✅
- **Buckets 本身**的 `DeleteBucket` 不检查桶内是否有锁定的对象 —— 锁定对象所在的桶也可能被删除 ❌

**维度 3：无锁保留期限延长/缩短的规范**
- S3 允许任何人**延长**保留期限（包括 COMPLIANCE 模式下的对象）
- 但只有具有 `s3:BypassGovernanceRetention` 权限的用户可以**缩短** GOVERNANCE 模式的保留期限
- 当前 `LockObject` 仅是 `SetLockedUntil`——没有模式区分。

**维度 4：生命周期与锁的交互未定义**
- `internal/reconcile/lifecycle.go:handleExpiredObject` 检查了 `LockedUntil`（硬删除时跳过），但**软删除路径不检查锁**——可能将锁定对象标记为软删除（虽然软删除不真正删除数据，但语义上错误）。
- 生命周期应该完全跳过锁定的对象（包括软删除）。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/service/file_crud.go:hardDeleteObject` | 检查 `LockedUntil` + `_aero_legal_hold` | 无 GOVERNANCE bypass 路径 |
| `internal/service/file_crud.go:checkLockBeforeOverwrite` | 检查 LockedUntil | 无模式区分（GOVERNANCE 可 bypass） |
| `internal/service/file_features.go:DeleteBucket` | 直接删除桶行 | 不检查桶内锁定对象 |
| `internal/api/webdav/spill.go:rename/move` | MOVE 使用 copy+delete 路径 | 可能绕过锁检查 |
| `internal/repository/sql_objects.go:SetLockedUntil` | 设置保留时间 | 不存储 lock_mode |
| `internal/repository/repository.go:BucketConfig.ObjectLockSeconds` | 桶级保留秒数 | 无 `LockEnabled` 锁定不可逆标志 |
| `internal/reconcile/lifecycle.go:handleExpiredObject` | 软删除不检查锁 | 可能软删除锁定对象 |
| `internal/api/rest/acl.go` | ACL handler | 无对象锁管理 API |
| `internal/api/s3compat/bucketconfig.go:handlePutObjectLock` | S3 `?object-lock` 子资源 | 缺少 `ObjectLockEnabled=true` 的不可逆语义 |

### 架构蓝图

```
┌─ WORM Bucket 状态机 ──────────────────────────────────────────│
│ 桶生命周期:                                                     │
│                                                                  │
│  ┌──────────────┐                                               │
│  │ 常规桶        │ ←── CreateBucket                              │
│  │ LockEnabled:  │                                               │
│  │ false         │                                               │
│  └──────┬───────┘                                               │
│         │ PUT /s3/{bucket}?object-lock                          │
│         │ { "ObjectLockConfiguration": {                         │
│         │   "ObjectLockEnabled": "Enabled",                      │
│         │   "Rule": {"DefaultRetention": { ... }} } }            │
│         ▼                                                        │
│  ┌──────────────┐  ←── 一旦启用，永不可逆                        │
│  │ WORM 桶      │                                               │
│  │ LockEnabled: │                                               │
│  │ true         │────→ 任何 DeleteBucket 调用返回 BucketNotEmpty │
│  └──────┬───────┘                                               │
│         │                                                        │
│         ├──→ 所有新 PUT 自动检查 DefaultRetention                 │
│         ├──→ 桶内对象最多能被锁保留，不阻塞覆盖（合规模式）         │
│         └──→ 生命周期跳过所有锁定对象                             │
│                                                                  │
│ Repository 扩展:                                                  │
│   BucketConfig.LockEnabled bool  // 一旦 true，不可回退          │
│   BucketConfig.DefaultRetentionMode string // GOVERNANCE|COMPLIANCE│
│   BucketConfig.DefaultRetentionDays int                          │
└────────────────────────────────────────────────────────────────┘

┌─ 对象锁模式 ───────────────────────────────────────────────────│
│ Object 扩展字段:                                                 │
│   LockMode     string  // "" | "GOVERNANCE" | "COMPLIANCE"      │
│   LegalHold    bool    // 独立标记，不与 LockMode 互斥            │
│   RetainUntil  *time.Time  // 保留到期时间                       │
│                                                                  │
│ 锁状态机:                                                        │
│   ┌─ 无锁 ───────────────────────────────────────────────┐      │
│   │ 新 PUT 不携带 x-amz-object-lock-* 头                  │      │
│   │ 可任意覆盖/删除                                       │      │
│   └──────────────────────────────────────────────────────┘      │
│                                                                  │
│   PUT with x-amz-object-lock-mode: GOVERNANCE                    │
│   ┌─ GOVERNANCE 模式 ──────────────────────────────────┐        │
│   │ 删除/覆盖需要:                                       │        │
│   │   1. x-amz-bypass-governance-retention: true 头      │        │
│   │   2. 请求方有 "bypass-governance-retention" 权限     │        │
│   │ 保留期限可延长不可缩短（管理员可绕过）                 │        │
│   └──────────────────────────────────────────────────────┘        │
│                                                                  │
│   PUT with x-amz-object-lock-mode: COMPLIANCE                    │
│   ┌─ COMPLIANCE 模式 ──────────────────────────────────┐        │
│   │ 删除/覆盖: 不可绕过！                                │        │
│   │ 保留期限可延长不可缩短（任何人不允许缩短）             │        │
│   │ 桶删除也不影响该对象的保留                            │        │
│   └──────────────────────────────────────────────────────┘        │
└────────────────────────────────────────────────────────────────┘

┌─ 跨协议一致性检查清单 ────────────────────────────────────────│
│ 协议路径             锁检查          现状                       │
│ ─────────────         ───────        ────                       │
│ REST DELETE /v1        ✅            正确                       │
│ REST PUT /v1           ✅ (覆盖前)    正确                       │
│ S3 DELETE /s3          ✅            正确                       │
│ S3 PUT /s3             ✅ (覆盖前)    正确                       │
│ S3 BatchDelete         ✅            正确                       │
│ WebDAV DELETE          ✅            正确                       │
│ WebDAV PUT             ✅            正确                       │
│ WebDAV MOVE            ❌            绕过（spill.go 直接读写）    │
│ MCP delete_file        ✅            正确                       │
│ Lifecycle sweep        ⚠️           硬删除检查，软删除跳过       │
│ DeleteBucket           ❌            不检查桶内对象              │
│ Replication delete     ⚠️           不检查目标桶的锁状态         │
└────────────────────────────────────────────────────────────────┘

┌─ Lock Bypass & Audit ─────────────────────────────────────────│
│ REST API:                                                       │
│   DELETE /v1/files/{key}?bypass-governance-retention=true        │
│   DELETE /v1/files/{key}?bypass-governance-retention=true       │
│     &reason="project_completed"  // 强制写入审计日志             │
│                                                                  │
│ 审计日志记录:                                                    │
│   每次 bypass 操作必须记录不可变的审计条目：                       │
│     actor, action="bypass_governance_retention",                 │
│     target=tenant/bucket/key, reason, bypass_time                │
│                                                                  │
│ 权限检查:                                                        │
│   当前 scope=read/write/admin 不足以表达 bypass 权限              │
│   需要新增 `bypass-governance-retention` scope 或 IAM action    │
└────────────────────────────────────────────────────────────────┘

**边界情况：**
- COMPLIANCE 模式对象即使**桶被删除**也保留——需要在 `DeleteBucket` 路径检查 `LockEnabled` + 剩余锁定对象
- 延长保留期限的请求应永远成功（任何锁模式下都可延长）
- MOVE 是跨协议锁一致性的最大盲点——需在 MOVE 路径中增加 `copy + source_delete` 之前检查源对象的锁
- 生命周期 sweep 对所有锁定对象（包括锁已到期的，只要 LockMode 还在）应整体跳过
- 已锁定的对象在版本化桶中的覆盖行为：GOVERNANCE 模式可以通过 bypass 头覆盖（创建新版本，旧版本保留），COMPLIANCE 模式完全拒绝覆盖

**复杂度:** M · **用户影响:** ★★★★★（合规准入） · **代码变更:** ~800 行新代码 + ~400 行修改

---

## 3. 内容智能管线：自动类型检测、格式转换、文档预览

### 为什么需要它

当前代码库对内容的处理停留在**完全信任用户输入**的阶段。生成缩略图的 `internal/thumbnail/thumbnail.go` 是唯一的内容处理机制。这产生了以下产品级缺口：

**缺口 1：无 Content-Type 自动检测**
- `internal/service/file_crud.go:Put` 中的 `buildPutObject` 从选项接受 `ContentType` 并直接存储
- 如果用户上传一个 PNG 文件但声称 `Content-Type: text/plain`——代码信任它，不验证
- 如果用户不提供 Content-Type（空字符串）——代码原样存储空值
- GET 时不进行任何 MIME 修正
- **后果**：浏览器下载文件时可能无法正确渲染，Web UI 预览显示乱码，MCP 客户端无法判断文件类型

**缺口 2：无 on-the-fly 格式转换**
- `GET /thumbnail?w=256&h=256` 支持 JPEG/PNG/GIF 缩略图
- 但缺少：WEBP/AVIF 现代格式输出、HEIC 输入支持、PDF 预览、文档（DOCX/XLSX/PPTX）转换、视频截图
- **后果**：Web UI 中无法预览 PDF 文档、无法查看 Office 文件、图片缩略图格式固定为 JPEG

**缺口 3：无多格式请求（Content Negotiation）**
- 不支持 `Accept` 头的格式协商——用户请求 `image/webp` 但总是收到 `image/jpeg`
- 不支持分辨率感知：`Accept: image/webp; w=320` 之类的客户端提示

**缺口 4：Extractor 层与内容管线的隔离**
- `internal/ai/extractor.go` 已经能从 PDF/DOCX 等格式提取文本——说明文件格式解析能力已经存在
- 但这个能力**仅限于 AI 管线**使用，不能通过 REST API 输出为结构化文档
- Extractor 的实现（`DefaultExtractor`）支持 `application/pdf`、`text/plain`、`application/rtf`，但缺少：`application/vnd.openxmlformats-officedocument.*`（Office 2007+）、`application/vnd.ms-*`（旧 Office）、`text/html`、`text/csv` 的**结构化解构**（提取表格、元数据、样式）

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/service/file_crud.go:Put` | 原样存储 Content-Type | 无 magic byte 嗅探修正 |
| `internal/service/file_crud.go:buildPutObject` | Content-Type 字段直接取用 | 空值时无默认值推导 |
| `internal/thumbnail/thumbnail.go` | JPEG/PNG/GIF → JPEG 缩略图 | 无 WebP/AVIF 输出，无 HEIC/BMP 输入 |
| `internal/ai/extractor.go` | PDF/DOCX/RTF/TXT 文本提取 | 仅限于 AI，无 REST API 输出 |
| `internal/api/rest/router.go` | `/thumbnail` 路由 | 无 `/preview` 路由 |
| `internal/api/rest/handler.go:Get` | 直接流式输出 | 无 Accept 头协商 |
| `internal/webui/static/index.html` | Web UI | 无文件预览面板 |
| `internal/api/rest/openapi.json` | OpenAPI 规范 | 无 `/convert` `/preview` 端点 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **MIME 嗅探绕过** | 上传 .exe 伪装成 .jpg | Content-Type: image/jpeg 原样存储 | Magic byte 检测 + 拒绝伪装的可执行文件 |
| **缺失 Content-Type** | SDK 上传不带 Content-Type 头 | 存储为 ""，浏览器下载提示"未知文件类型" | magic byte 检测 + 自动赋值 |
| **SVG 上传** | SVG 图片包含 XSS 脚本 | 原样存储，Web UI 直接渲染 → XSS 攻击 | SVG 内容消毒（strip scripts）+ CSP 头 |
| **大图片缩略图** | 100MB JPEG 请求 ?w=256 | 完全 decode 到内存再压缩 → OOM | 逐行 decode + 尺寸限制 + 超时 |
| **PDF 预览** | 用户想不下载就查看 PDF 内容 | 无——必须下载整个文件 | 服务端提取前 3 页转为图片 |
| **Office 文档预览** | DOCX/XLSX 文件无法在线查看 | 只能提取为纯文本（AI 管线内） | HTTP API 输出为 HTML/Markdown + 表格 |
| **HEIC 图片** | iPhone 上传的 .heic 文件 | Thumbnail 无法 decode | 添加 libheif CGO 绑定或转码为 JPEG |

### 架构蓝图

```
┌─ 自动 Content-Type 检测 ───────────────────────────────────────│
│ 位置: service/file_crud.go:Put                                   │
│ 新增步骤:                                                        │
│   1. 读取对象前 512 bytes（HTTP 请求的 peek）                     │
│   2. 运行 magic byte 检测: net/http#DetectContentType             │
│   3. 如果用户提供的 Content-Type 与检测结果冲突:                  │
│      a. 是否允许覆盖？配置: `CONTENT_TYPE_OVERRIDE=false`         │
│      b. 如果是可执行/危险类型（application/x-executable,         │
│         text/html with <script>）→ 拒绝上传                       │
│      c. 如果用户为提供 × 检测成功 → 自动赋值                      │
│   4. 将 sniffed_type 存入 metadata（`_aero_sniffed_type`）       │
│                                                                  │
│ 响应增强:                                                         │
│   X-Content-Type-Options: nosniff 头                              │
│   下载 API 默认使用 sniffed_type（如果存在）                      │
│                                                                  │
│ 配置:                                                             │
│   CONTENT_TYPE_SNIFF=true              // 默认开启               │
│   CONTENT_TYPE_REJECT_EXECUTABLE=true // 拒接 exe/bin            │
│   CONTENT_TYPE_DEFAULT=application/octet-stream                  │
└────────────────────────────────────────────────────────────────┘

┌─ 格式转换管线（/v1/convert）──────────────────────────────────│
│ POST /v1/convert/{bucket}/{key}                                  │
│   Accept: image/webp                                             │
│   ?format=webp&width=800&quality=85                              │
│   → 流式返回转换后的文件（不修改原始对象）                         │
│                                                                  │
│ 支持的输入 → 输出:                                                │
│   image/jpeg     → webp, avif, png, jpeg                         │
│   image/png      → webp, avif, jpeg                              │
│   image/gif      → webp, mp4 (animated), jpeg (first frame)      │
│   image/webp     → jpeg, png                                     │
│   image/heic     → jpeg, webp (需 CGO 或外部 CLI)                 │
│   image/bmp      → jpeg, png                                     │
│   image/svg+xml  → png (rasterize), sanitized svg                │
│   application/pdf → jpeg (前 3 页), text (提取全文)               │
│   text/*         → html (markdown 渲染)                          │
│                                                                  │
│ 架构:                                                             │
│   新包: internal/convert/                                         │
│     convert.go → 主入口，按 Content-Type 分发                     │
│     image.go   → image.Image → 目标格式编码                       │
│     pdf.go     → PDF 渲染（goroutine 池 + 64MB 缓冲限制）        │
│     doc.go     → 文档文本提取（复用 extractor 的现有能力）        │
│     sanitize.go → SVG/XSS 消毒                                   │
│                                                                  │
│ 限制:                                                             │
│   输入文件 ≤ 100MB（config: CONVERT_MAX_INPUT_BYTES）             │
│   每个格式转换超时 30s（config: CONVERT_TIMEOUT_SECONDS）          │
│   输出质量参数控制文件大小上限                                     │
│                                                                  │
│ 指标:                                                             │
│   convert_requests_total{source_format, target_format}           │
│   convert_duration_ms{source_format, target_format}              │
│   convert_bytes_saved_total  // 原始大小 vs 输出大小              │
└────────────────────────────────────────────────────────────────┘

┌─ 文档预览端点（/v1/preview）───────────────────────────────────│
│ GET /v1/preview/{bucket}/{key}                                   │
│   自动选择预览模式:                                               │
│     图片 → 缩略图（复用 thumbnail 逻辑）                          │
│     PDF → 返回前 3 页为 JPEG（第一页优先）                        │
│     文档 → 返回 HTML/Markdown（复用 extractor 提取的文本）        │
│     视频 → 返回 MOV 格式的第一帧截图（需要 ffmpeg 集成）          │
│     代码/文本 → 返回语法高亮的 HTML                               │
│     其他 → 返回 415 Unsupported Media Type                        │
│                                                                  │
│ 预览响应:                                                         │
│   Content-Type: text/html  / image/jpeg / multipart/mixed         │
│   X-Preview-Of: {bucket}/{key}                                    │
│   X-Preview-Type: thumbnail | pdf-preview | text-preview          │
│                                                                  │
│ Web UI 集成:                                                      │
│   对象详情面板中增加"预览"标签页                                    │
│   首次加载时调用 /v1/preview 异步渲染                               │
└────────────────────────────────────────────────────────────────┘

┌─ 可执行文件安全 ───────────────────────────────────────────────│
│ Magic byte 检测 + 黑名单:                                        │
│   - MZ (PE .exe/.dll)                                            │
│   - ELF (Linux 可执行文件)                                        │
│   - Mach-O (macOS 可执行文件)                                     │
│   - Java class 文件                                               │
│   - 带有 script 标签的 HTML                                       │
│                                                                  │
│ 配置:                                                             │
│   CONTENT_TYPE_REJECT_LIST="application/x-executable,             │
│     application/x-dosexec, application/x-mach-binary"            │
│   拒绝的资源将返回 400 + `ContentRejected` 错误码                 │
└────────────────────────────────────────────────────────────────┘

**复杂度:** M-L · **用户影响:** ★★★★☆（用户体验 + 安全性） · **代码变更:** ~1500 行新代码 + ~300 行修改

---

## 4. 运行时优雅降级 & 特性开关框架

### 为什么需要它

**问题：当前系统是全有或全无的。** 任何依赖后端（LLM、Embedder、Reranker、S3 存储）的故障都可能触发级联效应，而运维人员没有运行时工具来止血。

具体痛点：

**痛点 1：无运行时特性开关**
- `internal/api/rest/search.go` 中的 `POST /v1/search`、`POST /v1/chat`、`POST /v1/agent` **没有运行时开关**。如果 LLM 提供商中断，运维人员只能重启服务并修改环境变量。
- `internal/api/rest/handler.go` 中的对象操作也不能按需禁用——即使后续发现某个操作有 bug（如批量删除的竞态条件），也无法在不重启的情况下禁用 `/v1/batch-delete`。
- 所有 `AI_*` 配置都是启动时读取的——需要重启才能调整。

**痛点 2：无降级模式传播**
- `config/config.go` 有 `AI_DEGRADED_MODE` 配置项（`AI.DegradedMode`），`cmd/server/main.go` 中传到 `NewRouter`
- 查看 `internal/api/rest/router.go` 和 `internal/api/rest/search.go` —— **`DegradedMode` 没有被任何 handler 检查和使用** ❌
- 这意味着 `AI_DEGRADED_MODE=true` 配置了但不生效——这是一个存根

**痛点 3：无健康代理检测**
- Embedder、LLM、Reranker 启用了但不可用时，每次请求等待超时（30s timeout）。没有健康检查 goroutine 来提前检测并降级。
- 错误被记录为 warn log，但不会触发自动降级。

**痛点 4：无运行时缓存操作**
- 结果缓存（`result_cache.go`）无法在不重启的情况下清除——如果索引数据变了，缓存可能返回过期结果
- 密钥缓存（`auth/key_cache.go`）支持 TTL 过期但无手动失效

**痛点 5：无紧急流量整形**
- 如果某个租户开始异常消耗资源（10K QPS 搜索），运维无法立即限制它（只能改配置重启）
- 没有全局降级开关（"拒绝所有写入，只允许读取"）

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/config/config.go:AI.DegradedMode` | 配置项存在 | 没有任何 handler 读取它——**存根** |
| `cmd/server/main.go:buildRouter` | 传入 `cfg.AI.DegradedMode` | 但 `rest.NewRouter` 未使用该参数 |
| `internal/api/rest/search.go` | Search/Chat/Agent handler | 无降级检查 |
| `internal/api/rest/handler.go` | 所有 CRUD handler | 无运行时开关 |
| `internal/jobs/jobs.go:Pool` | Job pool | 无暂停/恢复命令 |
| `internal/storage/circuitbreaker.go` | 存储后端的 CB | 无应用层的 CB |
| `internal/ai/chat.go` | LLM 调用 | 无健康检测，故障超时 30s |
| `internal/ai/embedder.go` | Embedder HTTP 调用 | 无健康检测 |
| `internal/ai/rerank.go` | Reranker 调用 | 降级到原始排序（有）但无指标告警 |
| `internal/webui/web.go` | Web UI | 无管理控制面板可操作开关 |

### 架构蓝图

```
┌─ 特性开关框架 ─────────────────────────────────────────────────│
│ 新包: internal/feature/                                          │
│                                                                  │
│ type Switch struct {                                              │
│     Name        string                                           │
│     Enabled     bool                                             │
│     Description string                                           │
│ }                                                                 │
│                                                                  │
│ type Manager struct {                                             │
│     mu    sync.RWMutex                                           │
│     flags map[string]*Switch                                     │
│     repo  repository.Repository  // 可选持久化                   │
│ }                                                                 │
│                                                                  │
│ 预定义开关:                                                       │
│   feature.CRUD        = "api:crud"       // 对象 CRUD            │
│   feature.Search      = "api:search"     // 语义搜索             │
│   feature.Chat        = "api:chat"       // RAG Chat              │
│   feature.Agent       = "api:agent"      // AI Agent              │
│   feature.Indexer     = "ai:indexer"     // 索引管线              │
│   feature.BatchOps    = "api:batch"      // 批量操作              │
│   feature.Upload      = "api:upload"     // 分片上传              │
│   feature.Presign     = "api:presign"    // 预签名 URL            │
│   feature.WebDAV      = "proto:webdav"   // WebDAV 协议           │
│   feature.MCP         = "proto:mcp"      // MCP 协议              │
│   feature.S3Compat    = "proto:s3"       // S3 兼容               │
│   feature.Replication = "sys:replication" // 复制工人              │
│   feature.Webhook     = "sys:webhook"    // Webhook 投递           │
│                                                                  │
│ 启动时: feature.Init(defaults, repo)                             │
│   从 env + repo 表加载开关状态                                     │
│                                                                  │
│ 运行时:                                                           │
│   PUT /v1/admin/features/{name} {"enabled": false}               │
│   GET /v1/admin/features → 列出所有开关及状态                     │
│                                                                  │
│ 中间件:                                                           │
│   feature.Gate("api:search") → 对匹配路由返回 503 Service Unavailable │
└────────────────────────────────────────────────────────────────┘

┌─ 降级模式架构 ─────────────────────────────────────────────────│
│ 问题: `DegradedMode` 配置项存在但不生效                             │
│                                                                  │
│ 修复:                                                             │
│   internal/api/rest/search.go:                                    │
│     func (h *SearchHandler) Search(w, r) {                        │
│         if h.degradedMode {                                        │
│             // 返回空结果 + X-Aero-Degraded: true header          │
│             // 不调用 embedder/retrieval                          │
│         }                                                          │
│     }                                                              │
│     func (h *ChatHandler) Chat(w, r) {                             │
│         if h.degradedMode {                                        │
│             // 返回 {"answer":"AI is offline for maintenance",    │
│             //  "degraded": true}                                  │
│         }                                                          │
│     }                                                              │
│                                                                  │
│ 运行时动态降级:                                                    │
│   POST /v1/admin/degrade {"component": "ai", "action": "drain"}  │
│     → 停止新的 AI 请求，等待当前请求完成（存活连接继续）             │
│   POST /v1/admin/degrade {"component": "ai", "action": "off"}    │
│     → 立即拒绝所有 AI 请求（返回 503）                             │
│   POST /v1/admin/degrade {"component": "ai", "action": "on"}    │
│     → 恢复正常                                                   │
└────────────────────────────────────────────────────────────────┘

┌─ 健康代理检测 ─────────────────────────────────────────────────│
│ type HealthProbe struct {                                        │
│     Name   string           // "llm" | "embedder"               │
│     Check  func() error     // 调用后端健康端点或判断            │
│     Period time.Duration    // 探测间隔                          │
│     Timeout time.Duration   // 单次探测超时                       │
│     Fails   int64           // 连续失败次数（internal）           │
│     FailThreshold int64     // 超过此阈值标记为 DOWN             │
│ }                                                                 │
│                                                                  │
│ 状态: UP / DEGRADED / DOWN                                       │
│                                                                  │
│ LLM 健康探测:                                                     │
│   每隔 10s 调用一次 LLM 端点 /health（或一个最小请求）            │
│   连续 3 次失败 → 标记 DOWN                                       │
│   → 触发自动降级: chat/agent 返回 503                             │
│   → metric: ai_backend_health{backend="llm"} 1=UP 0=DOWN         │
│                                                                  │
│ Embedder 健康探测:                                                │
│   每隔 10s 发送一个简单的 embed 请求（如 "health"）               │
│   连续 3 次失败 → 标记 DOWN                                       │
│   → 触发自动降级: 搜索降级为纯 BM25（降级不是关掉）                 │
│   → metric: ai_backend_health{backend="embedder"} 1=UP 0=DOWN   │
│                                                                  │
│ 恢复:                                                             │
│   连续 2 次成功 → 标记 UP → 自动恢复                              │
│   手动: POST /v1/admin/health/{component}/reset                  │
└────────────────────────────────────────────────────────────────┘

┌─ 运行时缓存管理 ───────────────────────────────────────────────│
│ POST /v1/admin/cache/evict                                      │
│   {"cache": "search_results", "tenant": "acme"}                  │
│   → 清除指定租户的搜索结果缓存                                    │
│                                                                  │
│ POST /v1/admin/cache/evict?all=true                              │
│   → 清除所有缓存（搜索 + 嵌入 + 密钥）                            │
│                                                                  │
│ POST /v1/admin/cache/warm                                        │
│   {"cache": "search_results", "query": "most popular queries"}   │
│   → 预热热门查询的缓存                                            │
│                                                                  │
│ 用现有 infrastructure:                                            │
│   search.resultCache → 添加 Clear() 和 ClearForTenant()          │
│   embeddingCache → 添加 Clear()                                  │
│   auth.KeyCache → 添加 InvalidateAll()                           │
└────────────────────────────────────────────────────────────────┘

┌─ 紧急流量整形（Emergency Traffic Shaping）─────────────────────│
│ POST /v1/admin/emergency/shape                                    │
│   { "mode": "read_only", "reason": "storage backend degraded" }  │
│   → PUT/POST/DELETE/Batch 返回 503 + Retry-After                 │
│                                                                  │
│ POST /v1/admin/emergency/shape                                    │
│   { "mode": "tenant_quota", "max_rps": 10, "tenant": "rogue" }   │
│   → 临时降低特定租户的 RPS 上限（覆盖现有配置）                    │
│                                                                  │
│ POST /v1/admin/emergency/shape                                    │
│   { "mode": "normal" }  // 恢复正常                               │
│                                                                  │
│ 持久化: 存储在内存 + 可选持久化（生效期间重启也保留）              │
└────────────────────────────────────────────────────────────────┘

**边界情况：**
- 降级期间新请求返回清晰错误（503）而非长时间超时。这是最重要的边界情况——超时导致客户端重试加剧负载
- 从降级恢复时，缓存的过期内容不应立即回流（预热过程）
- 特性开关的变更应写入审计日志（谁在什么时候关闭了什么）
- 读只模式不应中断正在进行的 job（已完成一半的分片上传应允许完成或中止）
- 多个降级动作叠加（如：读只 + AI 离线 + 特定租户限流）要能正确组合

**复杂度:** M · **用户影响:** ★★★★★（生产可靠性） · **代码变更:** ~1200 行新代码 + ~500 行修改

---

## 5. 分层限流：Per-Key 配额、操作成本加权、请求优先级

### 为什么需要它

当前限流系统（`internal/middleware/ratelimit.go`）是一个 per-tenant token-bucket：

```go
type RateLimiter struct {
    tenants *sync.Map  // map[string]*rateLimiter
    rps     float64
    burst   int
}
```

对于单租户场景这可能足够了，但在多租户 + 多密钥 + 多操作的场景下，有三个根本问题没有解决：

**问题 1：Per-Key 限流缺失**
- 一个租户可以创建多个 API Key（`POST /v1/admin/keys`）
- 所有 Key 共享同一个 tenant-level token bucket
- 如果一个 Key 被泄露或被滥用（客户端 bug 导致每秒 10000 个请求），这个 Key 消耗掉整个 Tenant 的 quota → **其他使用不同 Key 的合法用户也被限流**
- **应该**：每个 Key 有自己的 token bucket，Key 的配额是 tenant 配额的子集

**问题 2：操作成本不加权**
- 当前：一次 light GET（耗费 ~1ms DB 查询）和一次 heavy 语义搜索（耗费 ~200ms embed + 20ms BM25 + 50ms rerank）**各消耗 1 个 token**
- 这意味着用户可以用满 RPS 但全部发搜索请求——导致后端 embedding 服务被攻击
- AWS/Azure/GCP 的所有 API 都有**操作加权**（Operation Cost Weighting）
- **应该**：PUT=1 token, GET=1 token, DELETE=1 token, Search=10 tokens, Chat=20 tokens, Agent=50 tokens, IndexObject=5 tokens（后台 job）

**问题 3：无请求优先级**
- 当前：所有请求在 token bucket 前平等竞争
- 管理操作（`GET /v1/admin/audit`、`POST /v1/admin/keys`）与用户操作（`GET /v1/files/bigfile.iso`）共享同一 bucket
- 在高负载下，管理员可能被自己的限流锁在门外——**管理平面被数据平面阻塞**
- **应该**：为 admin scope 的请求保留一个独立的更高优先级的配额池

**问题 4：限流指标不丰富**
- 当前：RateLimiter 记录请求数但不记录拒绝数
- 没有 `rate_limit_exceeded_total{tenant, key, operation}` 指标
- 运维无法识别"哪个租户的哪个 key 在触发限流"

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/middleware/ratelimit.go` | Per-tenant token-bucket | 无 per-key、无操作加权、无优先级 |
| `internal/middleware/ratelimit.go:Allow` | 检查 + 消耗 | 不返回剩余 token 数 |
| `internal/middleware/middleware.go` | 中间件链 | 限流中间件在优先级路由之前 |
| `internal/auth/auth.go:Registry.Middleware` | 认证后设置 context scope | scope 信息不传给限流层 |
| `internal/middleware/ratelimit_test.go` | 单元测试 | 只测 per-tenant 基本场景 |
| `internal/telemetry/metrics.go` | 现有指标 | 无 `rate_limiter_exceeded_total` |

### 架构蓝图

```
┌─ 分层限流架构 ─────────────────────────────────────────────────│
│                                                                  │
│  请求到达                                                         │
│     │                                                             │
│     ▼                                                             │
│ ┌────────────────────┐                                           │
│ │ 1. 全局限流层        │ ← 所有请求的总入口                       │
│ │ 全局 RPS token      │   保护服务器不被完全打满                   │
│ │ bucket (global)     │                                           │
│ └────────┬───────────┘                                           │
│          │ passed                                                 │
│          ▼                                                        │
│ ┌────────────────────┐                                           │
│ │ 2. 管理平面优先      │ ← admin scope 的请求走独立路径             │
│ │ admin 保留池        │   高负载时 admin 操作不受影响              │
│ │ 独立 token bucket  │                                           │
│ └────────┬───────────┘                                           │
│          │ 非 admin 或 admin 池空                                 │
│          ▼                                                        │
│ ┌────────────────────┐                                           │
│ │ 3. 租户层           │ ← per-tenant token bucket                 │
│ │ tenant token       │   现有行为                                  │
│ │ bucket             │                                           │
│ └────────┬───────────┘                                           │
│          │ passed                                                 │
│          ▼                                                        │
│ ┌────────────────────┐                                           │
│ │ 4. API Key 层       │ ← per-key token bucket                    │
│ │ key token bucket   │   key 的配额 = tenant 配额 / N（可配置）    │
│ └────────┬───────────┘                                           │
│          │ passed                                                 │
│          ▼                                                        │
│ ┌────────────────────┐                                           │
│ │ 5. 操作成本加权      │ ← 不同操作消耗不同数量的 token            │
│ │ operation costing  │   Store.GET=1, AI.Search=10               │
│ └────────┬───────────┘                                           │
│          │ passed (消耗 token)                                    │
│          ▼                                                        │
│       handler                                                     │
│                                                                  │
│ 任何层拒绝 → 返回 429 + Retry-After + 层级信息头                   │
└────────────────────────────────────────────────────────────────┘

┌─ 操作成本表 ───────────────────────────────────────────────────│
│ Operation                  Cost (tokens)  | 理由                  │
│ ─────────────────────      ─────────────  | ─────                │
│ GET /v1/files/{key}            1           | 轻量 DB + 流式读取    │
│ HEAD /v1/files/{key}           1           | 仅 DB 查询            │
│ PUT /v1/files/{key} (small)    2           | DB + 存储写入         │
│ PUT /v1/files/{key} (large)    2 + size/MB | 大文件加权            │
│ DELETE /v1/files/{key}         1           | DB + 存储删除         │
│ GET /v1/files?prefix=...       1           | Listing               │
│ POST /v1/search (vector)      10           | Embedding 调用        │
│ POST /v1/search (bm25)         3           | BM25 检索             │
│ POST /v1/search (hybrid)      12           | 两种模式的代价        │
│ POST /v1/chat                 20           | 搜索 + LLM 调用       │
│ POST /v1/chat/stream          30           | 搜索 + 流式 LLM       │
│ POST /v1/agent                50           | 多轮 LLM + 工具       │
│ GET /v1/events/stream          0           | SSE 有自己的限制      │
│ GET /v1/admin/*                0           | 在管理池中计量        │
│ ListUploads/ListParts          1           | Listing               │
│ UploadPart                     1           | 存储写入              │
│ CompleteMultipartUpload        2           | 合并                  │
│ BatchDelete (per key)          1           | 每个 key 的代价       │
└────────────────────────────────────────────────────────────────┘

┌─ Per-Key 限流实现 ─────────────────────────────────────────────│
│ 扩展 auth.Registry:                                              │
│   在认证中间件中，将 key 的身份（hash 或 label）写入 context       │
│   context.WithValue(ctx, keyIDKey, "key_label_or_hash")          │
│                                                                  │
│ 扩展 RateLimiter:                                                │
│   type KeyLimiter struct {                                       │
│       tenantLimit  *rate.Limiter  // 父级限制                    │
│       keyLimit     *rate.Limiter  // key 级限制                  │
│       keyShare     float64        // key 分享的 tenant quota %   │
│   }                                                              │
│                                                                  │
│ 配置:                                                             │
│   RATE_LIMIT_PER_KEY=true                // 开启 per-key 限流     │
│   RATE_LIMIT_KEY_SHARE_FACTOR=0.5        // key 最多消耗 50%     │
│   // 即：tenant 有 100 RPS，每个 key 最多 50 RPS                 │
│                                                                  │
│ 管理 API:                                                         │
│   POST /v1/admin/keys 扩展:                                       │
│     新增可选字段: "rate_limit_rps": 20  // key 独立限流            │
│   GET /v1/admin/keys/{hash}/limits → 当前限流状态                  │
│   DELETE /v1/admin/keys/{hash}/limits → 重置限流计数              │
└────────────────────────────────────────────────────────────────┘

┌─ 限流响应头标准化 ─────────────────────────────────────────────│
│ 429 Too Many Requests:                                            │
│   Retry-After: 5                                                  │
│   X-RateLimit-Limit: 100          // 每窗口上限                   │
│   X-RateLimit-Remaining: 23       // 剩余                         │
│   X-RateLimit-Reset: 1594814400  // 窗口重置时间                  │
│   X-RateLimit-Layer: tenant       // 哪层限流了: global|tenant    │
│                                  //   |admin|key|operation        │
│   X-RateLimit-Exceeded-By: key    // 触发限流的主体                │
│   X-RateLimit-Cost: 10            // 请求的操作成本               │
│                                                                  │
│ 正常响应（所有请求都包含）:                                        │
│   X-RateLimit-Limit: 100                                         │
│   X-RateLimit-Remaining: 87                                      │
│   X-RateLimit-Resource: "api"  // api | ai (区分两类)             │
└────────────────────────────────────────────────────────────────┘

┌─ 管理平面保留池 ───────────────────────────────────────────────│
│ 实现:                                                             │
│   admin 路由组使用独立的 chi router                                │
│   在中间件链中，在全局限流之后检测 scope：                           │
│     if ctx.Scopes 包含 "admin": → 使用 admin 池                   │
│     admin 池有独立 token bucket（RATE_LIMIT_ADMIN_RPS）            │
│                                                                  │
│ 配置:                                                             │
│   RATE_LIMIT_ADMIN_RPS=20         // admin 操作专用配额            │
│   RATE_LIMIT_ADMIN_BURST=5                                       │
│                                                                  │
│ 保护场景:                                                         │
│   租户用 10K QPS 的批量 GET 打满全局 quota → admin 请求仍可通行   │
└────────────────────────────────────────────────────────────────┘

┌─ 限流指标 ─────────────────────────────────────────────────────│
│ rate_limiter_requests_total{layer, tenant, key, operation,       │
│   decision}  // decision="allowed"|"denied"                      │
│ rate_limiter_current_tokens{layer, tenant, key}                  │
│ rate_limiter_wait_duration_ms{layer, tenant}                    │
│ rate_limiter_key_count{tenant}          // 每租户的活跃 key 数   │
│                                                                  │
│ Granfa 面板:                                                      │
│   按 tenant 的限流触发率（denied/total）                          │
│   限流触发的 tenant 排行榜                                         │
│   操作成本分布（哪个操作消耗最多）                                   │
└────────────────────────────────────────────────────────────────┘

**边界情况：**
- Per-key 限流的 key 数可能很多（一个租户 1000 个 key）→ 使用 LRU 淘汰不活跃 key 的 limiter
- admin 池空时的行为：fallback 到 tenant 池（不低于普通用户）
- 操作成本加权不能让简单的请求也更简单——但可以防止一个用户用搜索打满整个租户的 quota
- 如果租户 A 有 100 RPS，其 10 个 key 应各自有上限但不一定等于 10 RPS（建议每个 key 50% tenant limit = 50 RPS，最多 2 个 key 可同时满载）
- 批量删除/批量标签操作的成本应为 sum(per_key_cost) ——而不是固定 1 token（否则批量 1000 个 key 消耗 1 token 相当于免费）
- 预热场景：高 TPS 但 token bucket 初始为空时允许 burst

**复杂度:** M · **用户影响:** ★★★★☆（多租户公平性） · **代码变更:** ~1000 行新代码 + ~400 行修改

---

## 附录：第二轮值得关注但本次未选入的改进

| 问题 | 位置 | 说明 | 可能路径 |
|------|------|------|---------|
| **SSE 段中 `context.Background()` 的使用** | `internal/api/webdav/dav.go:302,381` | WebDAV PROPFIND 和 MOVE 使用 `context.Background()` 而非请求上下文——这意味着这些操作不会随请求取消而终止 | 改为 `r.Context()` |
| **事件表无限增长** | `internal/events/bus.go` + `internal/repository/sql_events.go` | `events` 表持续 INSERT 不清理——所有事件永远保留 | 事件 TTL + 后台清理（如 `RECONCILE_EVENT_RETENTION_DAYS` 选项 + reconcile worker 扩展） |
| **Indexer telemetry 中 `context.Background()`** | `internal/ai/indexer.go:313,316` | 指标记录使用 context.Background() 而非传入 ctx——丢失 trace 上下文 | 改进为透传 ctx |
| **Lifecycle 软删除路径不检查对象锁** | `internal/reconcile/lifecycle.go:handleExpiredObject` | 软删除路径直接调用 `SoftDeleteObject`，不检查 LockedUntil | 在 soft delete 路径增加锁检查或在 lifecycle 中统一跳过锁定对象 |
| **Presign URL 的固定过期时间** | `internal/service/file_features.go:PresignGet/PresignPut` | 当前 `PresignGet` 直接调用存储后端的 `PresignGet`——local 后端使用 HMAC 签名，s3 后端使用 S3 PresignURL——但前者对 `expiry` 的语义在不同后端不一致 | 统一 `Min(expiry, backend_max_expiry)` 或文档化后端的差异 |
| **MCP 客户端断线资源清理** | `internal/mcp/transport.go` | HTTP MCP 没有连接池限制或长连接清理——如果大量客户端同时连接可能导致 goroutine 泄漏 | 添加 `http.Client.Timeout` + 连接数限制 |
| **多地址 Webhook 无隔离** | `internal/events/webhook.go:deliver` | 当配置多个 webhook URL（逗号分隔），一个 URL 不可达（DNS 解析失败）不会影响其他 URL 的投递——但目前是串行的 | 应该改为并行投递，使用 `sync.WaitGroup` 或 goroutine per URL |

---

## 决策记录

| 决策 | 选项 | 选择 | 理由 |
|------|------|------|------|
| SSE 事件过滤的粒度 | (a) 服务端过滤，客户端不可见 (b) 客户端指定过滤参数 | **(b)** | 灵活性和网络带宽节省超过实现复杂度；S3 Event Notifications 也支持 FilterKey |
| 对象锁模式存储位置 | (a) 单独表 (b) 在 Repository.Object 加字段 | **(b)** | 95% 的锁操作读取对象时一起读取——分开表意味着额外的 JOIN |
| 格式转换限制 | (a) 全内存 buffer (b) 流式转换 | **(a) for images, (b) for docs** | 图片可在内存中处理（JPEG decoder 要求全量），文档转换走流 |
| 特性开关持久化 | (a) 仅内存 (b) 仅 DB (c) 两者 | **(c) 内存+DB** | 启动时从 DB 加载，运行时先改内存再写 DB——重启不丢失状态 |
| 操作成本定义位置 | (a) 中间件静态映射 (b) handler 自报告 | **(a) 中间件静态映射 + handler 可选覆盖** | 保障数据一致性，同时允许特殊 handler 调整默认值 |
