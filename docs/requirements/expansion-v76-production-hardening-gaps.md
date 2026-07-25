# AeroVault 生产硬化缺口 — 架构师/产品经理视角（第 76 轮）

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描（50K+ 行 Go 代码，`cmd/server/`，`internal/*` 全部 23+ 子包，三套 SDK，MCP 双模式，Web UI，48 对迁移文件）  
> **去重验证：** 对 `docs/requirements/` 下全部 75 份既有分析文档进行逐方向 `grep` 正则交叉验证 + 语义扫描，确保每个方向在既有分析中 **零实质性架构覆盖**  
> **日期：** 2026-07-11  
> **核心原则：** 选取代码中存在具体、可量化的产品/架构空洞，且对**协议准确性、生产可靠性、资源消耗**有显著杠杆作用的 4 个方向。每个方向均以代码锚点定位，包含跨层分析（协议适配层 → 服务层 → 持久化层），不含模糊概念。

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 | 既有分析覆盖 |
|---|------|------|--------|---------|---------|-------------|
| **1** | **S3 `ListParts` 全表加载 → Go 层分页：大量分片场景下的性能反模式** | 性能/资源 | **P2** — `ListParts` 从数据库查询所有分片记录（最多 10,000 行）到应用内存，然后在 Go 代码中分页。每个 `ListParts` 请求都做一次全量 SQL 查询 + 全量内存分配，即使客户端只请求前几条 | `internal/repository/sql_uploads.go:108`（`ListParts` 无 SQL 分页参数）；`internal/api/s3compat/extra.go:230-250`（全量获取后 Go 层分页）；`internal/service/file_multipart.go:93`（CompleteMultipart 也全量加载） | ✅ **完全去重**（v71/v54/v48/v6/v51 提及 `ListParts` 仅在 SDK 层"断点续传需 ListParts API"场景，**零服务器端性能分析**） |
| **2** | **CopyObject 忽略 `?versionId` 参数：跨版本复制静默降级为当前版本** | 数据正确性/协议 | **P2** — S3 `x-amz-copy-source` 支持 `?versionId=<id>` 指定复制特定版本。当前 `parseCopySource` 直接剥离 query string，用户指定复制历史版本时静默复制当前版本。版本控制下无警告、无错误，导致用户以为复制了旧版本但实际得到的是最新版本 | `internal/api/s3compat/extra.go:48-58`（`parseCopySource` 剥离 query string）；`internal/api/s3compat/extra.go:39-65`（`copyObject` 无 versionId 参数）；`internal/api/s3compat/handler.go:76-77`（PUT 时检测 `x-amz-copy-source` header 调用 `copyObject`） | ✅ **完全去重**（v11 方向表中一行路过列出"跨版本复制"概念概念，**零架构分析、零代码锚点、零实施路径**；其余 74 份文档零覆盖此方向） |
| **3** | **SSE Event Stream 无连接级资源保护：无上限的订阅者通道泄漏风险** | 可靠性/资源 | **P2** — `/v1/events/stream` 每来一个 SSE 客户端就在 `bus.subs` 中创建一个永久 channel（无 `Unsubscribe` 机制）。客户端断开后 channel 永不被移除，**每个 SSE 连接永久泄漏一个订阅者通道**。广播循环随泄漏通道数线性增长。无并发连接上限、无速率限制、无按租户配额 | `internal/api/rest/sse.go:67-91`（`liveStream` 调用 `bus.Subscribe()` 但不释放）；`internal/events/bus.go:76-83`（`Subscribe` 仅 append、无删除路径）；`internal/events/bus.go:45-48`（`Close` 只关闭全量 channels，非逐个释放）；`internal/api/rest/router.go:67`（`/events/stream` 无中间件保护） | ✅ **完全去重**（v47/v60/v39 覆盖"goroutine 泄漏"和"Worker 健康"，聚焦**后台 Worker 泄漏**而非 SSE 客户端连接泄漏；v55 覆盖"订阅者健康管理"聚焦**慢消费丢弃事件**而非**连接数无上限**。本方向专指 SSE 端点**连接级资源耗尽风险 + 永久 channel 泄漏**，与前 75 份文档正交） |
| **4** | **WebDAV Rename 非原子性：crash 窗口期产生重复对象，WORM 锁导致部分失败** | 数据一致性/协议 | **P2** — WebDAV `Rename` 实现为 copy-then-delete（先读入 spill buffer，再写入目标，再删除源）。服务 crash 在写入目标后、删除源前，两个对象永久共存。若源对象有 WORM 锁，delete 失败返回错误但目标已写入——回滚可能再次失败，导致数据重复。重试重命名会创建第三个副本 | `internal/api/webdav/dav.go:157-202`（copy-then-delete 模式）；`internal/api/webdav/dav.go:193-200`（回滚代码：删除目标再返回源错误）；`internal/api/webdav/dav.go:182-192`（未检查源对象的 LockedUntil）；`internal/service/file_crud.go`（`Put` 路径无 CAS/If-Match 检测） | ✅ **完全去重**（v55 覆盖"WebDAV 全链路中间件绕过"—聚焦安全，非数据一致性。v38 提及"WebDAV rename 原子性"作为方向表一行概念，**零代码锚点、零分析、零架构方案**。其余 73 份文档零覆盖） |

---

## 方向一：S3 `ListParts` 全表加载 → Go 层分页

### 现状

`ListParts` API（S3 `GET /{bucket}/{key}?uploadId=...` 的响应）当前实现路径：

```
S3 ListParts 请求
       │
       ▼
  s3compat/extra.go:230-250
       │ ListParts(ctx, uploadID)
       ▼
  repository/sql_uploads.go:108
       │ SELECT … FROM multipart_parts WHERE upload_id=$1 ORDER BY part_number
       ▼
  全量 []PartRecord 返回（最多 10,000 行）
       │
       ▼
  extra.go:236-250
  在 Go 代码中按 part-number-marker / max-parts 分页
  （遍历全量 slice，跳过 ≤marker 的，取前 maxParts 个）
```

**代码证据链：**

```go
// ① 存储层 — 无分页参数
// internal/repository/sql_uploads.go:108
func (s *sqlStore) ListParts(ctx context.Context, uploadID string) ([]PartRecord, error) {
    rows, err := s.db.QueryContext(ctx, s.rebind(
        `SELECT upload_id, part_number, etag, size, created_at
         FROM multipart_parts WHERE upload_id=$1 ORDER BY part_number`),
        uploadID)
    // 返回全部行 ↓
    defer rows.Close()
    for rows.Next() { ... parts = append(parts, p) ... }
    return parts, nil  // 一次性返回所有 PartRecord
}

// ② API 层 — Go 内存层分页
// internal/api/s3compat/extra.go:231-250
parts, err := h.svc.Repo().ListParts(r.Context(), uploadID)
// parts 已包含全部 10,000 行
out := listPartsResult{...}
for _, p := range parts {
    if p.PartNumber <= marker { continue }
    if len(out.Parts) >= maxParts { break }  // ← 在 Go 层分页
    out.Parts = append(out.Parts, ...)
}

// ③ 服务层 — CompleteMultipart 也全量加载
// internal/service/file_multipart.go:93
parts, err := s.repo.ListParts(ctx, uploadID)
// 同样全量加载，用于 buildPartList
```

**影响量化：**

| 场景 | 分片数 | 每次 ListParts 返回数据量 | 实际请求页大小 | 浪费比例 |
|------|--------|--------------------------|---------------|---------|
| 典型大文件上传 | 1,000 | ~32KB（1,000 × ~32 bytes/record） | 一般默认 1000 | 持平（全量=页大小） |
| 极端大文件（10TB, 10MiB/part） | 10,000 | ~320KB | 100 | **32× 浪费** |
| 分页浏览（每页 100 条） | 5,000 | ~160KB | 100 | **50× 浪费** |
| SDK 断点续传（需 ListParts 查询已上传分片） | 10,000 | ~320KB | 10,000 | 持平（需要全量） |

### 根因

`ListParts` 的 Repository 接口签名（`repository/repository.go:292`）：
```go
ListParts(ctx context.Context, uploadID string) ([]PartRecord, error)
```

没有 `marker`、`limit` 等分页参数。调用方（complete 路径和 list 路径）都一次性获取全部数据。

### 为什么需要

| 理由 | 说明 |
|------|------|
| **S3 协议合规** | AWS S3 `ListParts` 支持 `max-parts`（默认 1000, 最大 1000）和 `part-number-marker` 分页。当前实现在 SQL 层面返回全部再在 Go 层分页，当分片数超过 `max-parts`（1000）时 SQL 传输了比协议承诺多 10× 的数据 |
| **内存安全** | 10,000 条 `PartRecord` 约 320KB——单次调用不高。但如果上传了大文件且多个客户端同时 ListParts (SDK 重试、管理控制台刷新)，N 个并发请求产生 N×320KB 瞬时内存分配 |
| **CompleteMultipart 路径** | `CompleteMultipart` 也全量加载所有 part 记录。这是必要的（需要验证所有分片已上传），但可以通过流式处理（分批 fetch 边验证边释放）降低峰值内存 |

### 建议方向

**Phase 1（Repository 层分页）：**
- 为 `ListParts` 增加 `marker int32` 和 `limit int` 参数
- SQL 添加 `AND part_number > $2 ORDER BY part_number LIMIT $3`
- 保持向后兼容：现有调用方（CompleteMultipart）传 `marker=0, limit=10000` 行为不变

**Phase 2（API 层流式分页）：**
- `listParts` handler 传 `part-number-marker` / `max-parts` 到 Repository
- 避免全量加载后再遍历 skip

**Phase 3（CompleteMultipart 流式处理）：**
- `CompleteMultipart` 可以流式验证 part 完整性：分批 fetch 1000 条，验证，释放，再 fetch 下一批
- 降低峰值内存从 `O(n)` 到 `O(pageSize)`

| 指标 | 估计 |
|------|------|
| Phase 1 代码量 | ~30 行（Repository 接口 + SQL + 调用方适配） |
| Phase 2 代码量 | ~20 行（handler 参数透传） |
| Phase 3 代码量 | ~40 行（CompleteMultipart 分批处理） |
| 风险 | **低** — 纯性能优化，不改变语义；现有测试保底 |

---

## 方向二：CopyObject 忽略 `?versionId` 参数

### 现状

S3 `CopyObject` 通过 `x-amz-copy-source` header 指定源对象，AWS 规范支持附加 `?versionId=<id>` 来复制特定版本：

```
x-amz-copy-source: /bucket/source-key?versionId=abc123
```

当前实现：

```go
// internal/api/s3compat/extra.go:48-58
func parseCopySource(s string) (bucket, key string, ok bool) {
    s = strings.TrimPrefix(s, "/")
    if i := strings.IndexByte(s, '?'); i >= 0 {
        s = s[:i]          // ← 直接剥离 query string，丢弃 versionId
    }
    if dec, err := url.QueryUnescape(s); err == nil {
        s = dec
    }
    parts := strings.SplitN(s, "/", 2)
    // ...
}
```

**影响路径：**

```
客户端请求：
  PUT /dst-bucket/dst-key
  x-amz-copy-source: /src-bucket/src-key?versionId=abc123
       │
       ▼
  handler.go:76-77 检测到 x-amz-copy-source header
       │
       ▼
  extra.go:39 copyObject(w, r, dstBucket, dstKey, copySource)
       │
       ▼
  extra.go:48 parseCopySource(copySource)
       │ "?versionId=abc123" 被剥离
       ▼
  extra.go:55 svc.Get(ctx, tenant, srcBucket, srcKey)  ← 只传 bucket+key
       │ 默认返回当前版本（非 abc123）
       ▼
  用户以为复制了版本 abc123，实际复制了 CURRENT 版本
```

### 影响场景

| 用户操作 | 期望行为 | 实际行为 |
|---------|---------|---------|
| `aws s3 cp s3://bucket/key s3://bucket/key2 --source-version-id abc123` | 复制历史版本 abc123 | 复制当前版本（静默） |
| S3 Console "Copy" on a version's context menu | 复制所选版本 | 复制当前版本 |
| SDK `copy_object(Bucket, Key, CopySource={"Bucket":b,"Key":k,"VersionId":"abc123"})` | 复制指定版本 | 复制当前版本 |
| 恢复删除的对象：复制已删除版本的 contents | 复制删除前的数据 | 复制当前版本（可能是另一个数据） |

### 为什么需要

| 理由 | 说明 |
|------|------|
| **数据正确性** | 这不是特性缺失——这是静默行为错误。用户明确要求复制特定版本，系统无警告无错误地做了不同的事。版本控制下的数据操作必须精确 |
| **S3 协议一致性** | AWS S3、MinIO、Ceph RGW 都支持 `CopyObject` 的 `versionId`。不支持意味着 `aws s3 cp` 等标准工具的 `--source-version-id` flag 在此系统上产生错误结果 |
| **合规场景** | 保留法律证据需要精确版本控制。复制"当前版本"而不是"指定版本"可能导致证据链断裂 |

### 建议方向

```go
// parseCopySource 改造
func parseCopySource(s string) (bucket, key, versionID string, ok bool) {
    s = strings.TrimPrefix(s, "/")
    var query string
    if i := strings.IndexByte(s, '?'); i >= 0 {
        query = s[i+1:]     // 保留 query string
        s = s[:i]
    }
    // 解析 versionId
    if q, err := url.ParseQuery(query); err == nil {
        versionID = q.Get("versionId")
    }
    // ...
}
```

**完整路径：**
1. `parseCopySource` 返回 `versionID string`
2. `copyObject` 检测 `versionID`：
   - 空 → 当前行为（`svc.Get` 默认）
   - 非空 → `svc.GetVersion(ctx, tenant, bucket, key, versionID)`
3. `FileService.GetVersion` 实现或 `Repo.GetObjectByVersion`
4. 写入目标：与现有逻辑一致，从源读取 body 写入目标

| 指标 | 估计 |
|------|------|
| 代码量 | ~40 行（parseCopySource 改造 + copyObject versionId 路由 + GetVersion 方法） |
| 修改文件 | `internal/api/s3compat/extra.go`、`internal/service/file_crud.go`（或 `file_features.go`） |
| 风险 | **低** — `?versionId` 传递只影响 CopyObject；`svc.Get` 默认行为不变 |

---

## 方向三：SSE Event Stream 无连接级资源保护

### 现状

`/v1/events/stream` 是实现服务端推送事件（Server-Sent Events）的关键端点。其实现路径为：

```go
// internal/api/rest/sse.go:67-91
func (h *SSEHandler) liveStream(w http.ResponseWriter, r *http.Request, flusher http.Flusher, tenant string) {
    sub := h.bus.Subscribe()                   // ← ① 创建永久 channel，添加到 bus.subs
    keepalive := time.NewTicker(15 * time.Second)
    defer keepalive.Stop()
    for {
        select {
        case <-r.Context().Done():             // 客户端断开
            return                             // ← ② goroutine 退出，但 channel 未从 bus.subs 移除
        case e, ok := <-sub:
            // 发送事件...
        case <-keepalive.C:
            // 发送 keepalive...
        }
    }
}
```

**问题链：**

```
① bus.Subscribe() → bus.subs = append(bus.subs, ch)
        ↓
② 客户端断开 → goroutine 退出
        ↓
③ sub channel 永不被移除（无 Unsubscribe 方法）
        ↓
④ bus.broadcast() 继续向所有 subs 发送
        ↓
⑤ 死 channel 无接收者 → default 分支丢弃事件
        ↓
⑥ 每个 SSE 连接永久泄漏 1 个 channel + 64 个 slot 的 buffer
```

**无连接级资源保护：**

```
/events/stream ← 无中间件保护
无限制地创建 SSE 连接
        ↓
每个连接：
  - 1 个 goroutine（liveStream）
  - 1 个 chan repository.Event（buffer=64）
  - 1 个 time.Ticker
  - bus.subs 中 +1 个条目（永久）
        ↓
1000 个并发 SSE 连接：
  - ~1000 goroutines
  - ~64KB channel buffer 空间（永久泄漏）
  - 广播循环遍历 1000 个 channel（含 1000 个死 channel）
```

### 为什么需要

| 理由 | 说明 |
|------|------|
| **资源耗尽安全** | 任何知道端点 URL 的客户端（或恶意脚本）可以打开无限个 SSE 连接。没有上限意味着内存/gouroutine 线性增长，最终 OOM。注意 `/events/stream` 在 router 中注册在 `r.Use(mw.Auth)` 之后（受 auth 保护），但 auth 可被绕过（匿名公读）或恶意的合法用户也可滥用 |
| **生产可靠性** | 浏览器端的 `EventSource` 自动重连（默认 3 秒退避）。如果用户打开多个标签页，每个标签页创建一个持久连接。100 个员工打开 2 个标签页=200 个连接=200 个永久泄漏的 channel |
| **可观测性** | 当前无任何指标跟踪 SSE 连接数、连接持续时间、或按租户的连接分布。运维无法判断"正常连接数是多少"或"是否有客户端泄漏连接" |
| **按租户隔离** | 一个租户的 50 个连接不应耗尽其他租户的连接预算 |

### 建议方向

**Phase 1（最小修复——连接数上限）：**
- 为 `SSEHandler` 添加 `maxConns int` 和 `activeConns atomic.Int64`
- `Stream` 方法检入时检查 `activeConns.Load() >= maxConns` → 503
- `liveStream` 退出时 `activeConns.Add(-1)`

```go
// Phase 1 核心变更示意
type SSEHandler struct {
    bus         *events.Bus
    repo        repository.Repository
    logger      *slog.Logger
    maxConns    int64
    activeConns atomic.Int64
}

func (h *SSEHandler) Stream(w http.ResponseWriter, r *http.Request) {
    if h.maxConns > 0 && h.activeConns.Load() >= h.maxConns {
        http.Error(w, "too many SSE connections", http.StatusServiceUnavailable)
        return
    }
    h.activeConns.Add(1)
    defer h.activeConns.Add(-1)
    // ... existing logic ...
}
```

**Phase 2（channel 生命周期管理）：**
- 为 `Bus` 添加 `Unsubscribe(ch)` 方法：从 `b.subs` 中移除并 close channel
- `SSEHandler.liveStream` 在 `defer` 中调用 `bus.Unsubscribe(sub)`

```go
// Phase 2 核心变更示意
func (b *Bus) Unsubscribe(ch <-chan repository.Event) {
    b.mu.Lock()
    defer b.mu.Unlock()
    for i, c := range b.subs {
        if c == ch {
            close(c)
            b.subs = append(b.subs[:i], b.subs[i+1:]...)
            return
        }
    }
}
```

**Phase 3（可观测性 + 按租户配额）：**
- 添加 OTel gauge `sse_connections_active{tenant}`
- 按租户配额：`SSEHandler` 维护 `map[string]int64` 按租户计数
- 添加 Config 项 `EVENTS_SSE_MAX_CONNS_PER_TENANT`（默认 20）

| 指标 | 估计 |
|------|------|
| Phase 1 代码量 | ~20 行（计数器 + Stream 方法检查） |
| Phase 2 代码量 | ~15 行（Bus.Unsubscribe + SSEHandler defer） |
| Phase 3 代码量 | ~40 行（按租户配额 + OTel 指标） |
| 风险 | **低** — `Unsubscribe` 是纯新增方法；已有 `Close()` 逻辑兼容 |

---

## 方向四：WebDAV Rename 非原子性

### 现状

WebDAV `Rename`（MOVE 方法）是文件拖拽重命名/移动的底层操作。当前实现：

```go
// internal/api/webdav/dav.go:157-202
func (f *davFS) Rename(ctx context.Context, oldName, newName string) error {
    tenant := f.tenant(ctx)

    // Step 1: 读取源对象到 spill buffer（内存或临时文件）
    rc, src, err := f.svc.Get(ctx, tenant, service.DefaultBucket, strings.TrimPrefix(oldName, "/"))
    // ...
    buf := newSpillBuffer()
    err = buf.fill(rc)
    _ = rc.Close()

    // Step 2: 写入目标
    _, err := f.svc.Put(ctx, tenant, service.DefaultBucket, dst, buf, buf.Len(), opts)
    if err != nil {
        return err
    }
    //                ← CRASH WINDOW: 目标已写入，源尚未删除

    // Step 3: 删除源
    if err := f.svc.Delete(ctx, tenant, service.DefaultBucket, src2, true); err != nil {
        // 回滚：尝试删除目标
        if delErr := f.svc.Delete(ctx, tenant, service.DefaultBucket, dst, true); delErr != nil {
            f.logger.Warn("webdav rename rollback failed", ...)
        }
        return err
    }
    return nil
}
```

**三个阶段的数据一致性风险：**

```
时间线 ────────────────────────────────────────────────►

Step 1          Step 2              [CRASH]          Step 3
读取源到缓存 → 写入目标完成 →  服务崩溃在此处  →   删除源（未执行）
                                                           │
                              ┌────────────────────────────┘
                              ▼
                    结果：源和目标同时存在
                    用户看到两个"相同"的对象
                    数据被复制而非移动
```

**WORM 锁导致的特殊失败路径：**

```
Step 2: 写入目标成功（目标无锁）
Step 3: 删除源失败（源有 WORM 锁 locked_until > now）
        ↓
       尝试回滚：删除目标
        ↓
       回滚成功 → 数据安全但 rename 失败报错（正确行为）
       回滚失败 → 两个对象共存（数据重复）
```

### 影响场景

| 场景 | 当前行为 | 正确行为 |
|------|---------|---------|
| Finder 拖拽重命名文件（网络闪断） | 源和目标都保留 | 要么全部完成（只在目标），要么全部回滚（只在源） |
| 重命名包含 WORM 锁定的文件 | 目标已写入，删除源失败，回滚目标可能也失败 → 数据重复 | 先检测源的锁状态，锁定则拒绝 rename |
| 重试重命名（第一次 crash 后） | 源仍在，目标也存在（从第一次 crash），第三次写入 → 3 个副本 | 幂等 rename：检测目标已存在且内容一致 → 直接删除源 |
| 大文件重命名（10GB，spill 到 temp file） | 两次全量复制（读入 spill + 写入目标）= 20GB I/O | S3 CopyObject 风格的服务端副本（如果后端支持） |

### 为什么需要

| 理由 | 说明 |
|------|------|
| **数据完整性** | WebDAV 是 Mac Finder / Windows Explorer / rclone 的标准挂载协议。用户在 Finder 中拖拽重命名文件时，期望的是原子操作。数据重复比操作失败更糟糕——用户可能不会立即发现 |
| **WORM 合规** | 对象锁定的文件不可删除。rename 必须在其语义层面考虑锁——要么拒绝 rename（安全），要么先验证锁状态。当前实现忽略锁检查，产生部分失败 |
| **幂等性** | crash 后重试 rename 不应产生更多副本。需要检测"目标已存在且内容相同"的场景来安全短路 |

### 建议方向

**Phase 1（锁预检 + 错误处理增强）：**
- Rename 开始时检查源对象是否有 WORM 锁 → `ErrLocked` 提前拒绝
- 增强回滚日志：回滚失败时包含源/目标 key 和租户，输出到结构化日志以便运维手动修复

```go
// Phase 1 核心变更
func (f *davFS) Rename(ctx context.Context, oldName, newName string) error {
    tenant := f.tenant(ctx)
    src := strings.TrimPrefix(oldName, "/")
    dst := strings.TrimPrefix(newName, "/")

    // 预检：源是否存在、是否有锁
    srcObj, err := f.svc.Stat(ctx, tenant, service.DefaultBucket, src)
    if err != nil {
        return err
    }
    if srcObj.LockedUntil != nil && srcObj.LockedUntil.After(time.Now()) {
        return os.ErrPermission  // 或 service.ErrLocked
    }

    // ... 后续重命名逻辑不变 ...
}
```

**Phase 2（幂等检测）：**
- 在 Step 2（Put）之前，检查目标是否已存在且内容与源一致
- 如果目标存在且 ETag 与已知的源匹配，直接执行 Step 3（删除源）完成 rename
- 这要求 spill buffer 或源 ETag 在写入前可用

**Phase 3（后端原生 rename）：**
- 为 `Storage` 接口增加 `Rename(ctx, oldKey, newKey) error` 方法
- 本地存储实现为 `os.Rename`（同一文件系统下原子操作）
- S3 存储实现为 CopyObject + DeleteObject（与当前相同但由后端控制）
- WebDAV 优先使用后端原生 rename，回退到 copy-then-delete

| 指标 | 估计 |
|------|------|
| Phase 1 代码量 | ~30 行（锁预检 + 增强日志） |
| Phase 2 代码量 | ~40 行（幂等检测逻辑） |
| Phase 3 代码量 | ~60 行（Storage 接口扩展 + local 实现 + s3 实现 + fallback） |
| 风险 | **中** — Phase 1/2 纯新增逻辑；Phase 3 需变更 `Storage` 接口（所有后端需实现或返回 `ErrNotSupported`） |

---

## 各方向既有分析去重声明

| 方向 | 验证方式 | 结果 |
|------|---------|------|
| **方向一：ListParts 全量加载** | `grep -rl "ListParts\|listParts\|list.*parts.*memory\|parts.*pagination\|PartRecord.*pagina" docs/requirements/` → 命中 5 份文档（v71/v54/v48/v6/v51）。阅读每个命中处：全部在 SDK 特性规范（"客户端断点续传需 ListParts API"）或功能清单中一行带过，**零服务器端性能架构分析** | ✅ **完全去重** |
| **方向二：CopyObject versionId** | `grep -rl "versionId.*ignor\|ignor.*versionId\|CopySource.*version\|parseCopySource\|silently.*version\|version.*silent\|versionId.*copy\|copy.*versionId" docs/requirements/` → 零命中。补充验证 `grep -rl "跨版本复制\|CopyObject.*version\|version.*CopyObject"` → v11 方向表一行概念"跨版本复制"，**零代码锚点、零架构分析** | ✅ **完全去重** |
| **方向三：SSE 连接保护** | `grep -rl "SSE.*connection.*limit\|SSE.*rate.*limit\|events/stream.*limit\|concurrent.*SSE\|SSE.*concurrent\|SSE.*resource.*protect\|events.*stream.*resource\|max.*SSE\|SSE.*max\|sse_connect" docs/requirements/` → 零命中。补充验证 v47/v60/v39 关于"goroutine 泄漏"→聚焦后台 Worker 而非 SSE 客户端连接泄漏 | ✅ **完全去重** |
| **方向四：WebDAV Rename 原子性** | `grep -rl "rename.*atomic\|atomic.*rename\|copy.*then.*delete.*crash\|crash.*rename\|rename.*crash\|rename.*duplicate\|Rename.*非原子" docs/requirements/` → v38 方向表中一行"WebDAV rename 原子性"概念，**零代码锚点、零分析**。v55 覆盖 WebDAV auth 绕过，非数据一致性 | ✅ **完全去重** |
