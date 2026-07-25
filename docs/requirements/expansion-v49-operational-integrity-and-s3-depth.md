# AeroVault 高价值扩展方向 v49 — 操作完整性 & S3 语义深度

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部子包，~50K `.go` 代码 + `sdk/*` 三套客户端 + `deploy/*` + 48 对迁移文件 + 全部 `docs/requirements/` 已有 48 份分析文档 + `ROADMAP.md` + `CHANGELOG.md` + `TODO.md` + `adr/DECISIONS.md`）
>
> **分析视角：** 资深架构师 / 产品经理 — 在累计 48 期分析（250+ 方向）基础上，寻找此前从未作为独立架构方向实质性分析过的操作完整性与 S3 语义深度缺口
>
> **分析日期：** 2026-07-10
>
> **去重验证：** 对 `docs/requirements/` 下全部 48 份既有分析文档进行穷尽式搜索。每个方向在既有文档中 **零实质性独立架构分析**（表格一行路过引用、举例提及、单一子点均不构成）。

---

## 前言

经过 48 轮 expansion 分析，AeroVault 的功能维度、架构维度、执行层维度、生产就绪度维度已被深度覆盖：

```
v1–v42：功能实现广度               ❌ 不支持  →  ✅ 已实现
v43–v44：架构系统性与交叉缺口       ✅ 有 CRUD →  ⚠️ 运行时行为完整
v45–v46：交叉架构与产品成熟度       ✅ 各功能独立正确 →  ✅ 交叉面一致
v47：生产信任维度                   ✅ 功能完整+交叉一致 →  ❌ 缺乏可验证保障
v48：执行层行为完整性               ✅ API 可配置 →  ❌ 后台无对应执行流水线
```

本期（v49）聚焦于此前扫描中 **始终未被触及** 的五个方向。它们的特点是：

1. **不是"加功能"，而是"让已有功能在真实负载下可信"**
2. **涉及数据安全、成本控制、一致性与可恢复性**
3. **产品差异化与生产就绪度的最后天花板**

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心矛盾 | 48 期覆盖 |
|---|------|------|--------|---------|-----------|
| 1 | **Multipart Upload 过期清理 & Orphan Parts GC** | 可靠性/成本 | **P1** — 未完成的 multipart upload 产生的部分数据在存储后端永久残留，永不清理；多个不完整的上传可产生大量无效存储消耗和可计费成本 | ❌ **零覆盖**（v48 方向三讨论了 multipart 并发一致性，但未涉及过期生命周期管理） |
| 2 | **S3 Object Lock Retention Mode（GOVERNANCE / COMPLIANCE）分层语义** | 兼容性/合规 | **P2** — S3 API 接受 GOVERNANCE 模式值，但运行时将其降级为二进制锁（仅检查 locked_until），无模式语义区分；GOVERNANCE 模式下的许可绕过（需要特定权限缩短保留期）与 COMPLIANCE 模式下的绝对锁定无差异 | ❌ **零覆盖**（s3compat 测试硬编码 GOVERNANCE，但运行时忽略模式值，仅做时间比较） |
| 3 | **跨协议一致性与 Read-Your-Writes 保障** | 可靠性 | **P2** — REST / S3 / WebDAV 三条协议路径均经过同一 FileService，但在 S3 存储后端 + Postgres 配置下存在写入窗口：storage blob 先写入远端，然后 repo 写入本地，其间并发读取可能看到过期或丢失的元数据 | ❌ **零覆盖**（v19 覆盖多协议 feature 面，但未分析写后读一致性的真实保障） |
| 4 | **事件流交付保障与 SSE Reconnection 协议** | 可靠性/产品 | **P2** — EventBus 64 深度缓冲区在 subscriber 慢速时静默丢弃事件；SSE 端点实现了 `Last-Event-ID` 回放但 replay 仅查询未消费事件且不持续追踪消费进度；无 subscriber 健康检测、无死信通知、无消费确认 | ❌ **零覆盖**（v48 方向一覆盖通知路由，方向五覆盖健康 API，但未分析事件交付保障模型） |
| 5 | **S3 批量操作 API & 对象级元数据搜索** | 兼容性/产品 | **P2** — REST 有 batch/delete 和 batch/tag，但 S3 协议没有完整的 Batch Operations API（Invoke/ListJobs/DescribeJob）；Search API 只检索分块后的内容，不支持按 metadata/tag/storage_class 等对象属性进行结构化搜索 | ⚠️ `extensions.md` 早期方向表格一行提及"batch operations"，但 **零实质性架构分析**；metadata search 仅在 v12/v20/v22 作为概念方向子点出现 |

---

## 方向一：Multipart Upload 过期清理 & Orphan Parts GC

### 现状

当前 multipart upload 的生命周期完全由客户端控制：

```go
// internal/service/file_multipart.go
func (s *FileService) InitMultipart(ctx context.Context, ...) (repository.Upload, error)
func (s *FileService) UploadPart(ctx context.Context, ...) (repository.PartRecord, error)
func (s *FileService) CompleteMultipart(ctx context.Context, ...) (repository.Object, error)
func (s *FileService) AbortMultipart(ctx context.Context, ...) error
```

流程：

```
Client InitMultipart →  Upload 行创建 + 存储后端初始化
Client UploadPart    →  Part 行记录 + 存储后端 part 写入
Client CompleteMultipart → 合并 → 对象元数据写入 → Upload 行删除 + Part 行遗留
  或
Client AbortMultipart    → 存储后端清理 → Upload 行删除 + Part 行遗留
```

问题在于：

1. **Part 行残留**：`CompleteMultipart` 和 `AbortMultipart` 均调用 `repo.DeleteUpload` 删除 upload 行，但 **不删除 `parts` 表中的 part 记录**（`repository.Upload` 删除后 parts 成为孤儿数据）。查看 `repository/sql_uploads.go`：`DeleteUpload` 仅有 `DELETE FROM uploads WHERE id = ?`，无级联 `DELETE FROM parts WHERE upload_id = ?`。

2. **存储后端 part 残留**：客户端发起 `InitMultipart` 后崩溃或断开连接，upload 行保留在 DB，part 数据保留在存储后端，永不清除。AWS S3 默认 7 天后自动中止过期 multipart upload。AeroVault **完全没有**过期清理机制。

3. **存储后端 part 的 ABORT 遗漏**：如果 `AbortMultipart` 因网络或后端错误失败，upload 行可能被删除但底层 part blob 依然存在（`s.store.AbortMultipart` 失败后仍执行 `repo.DeleteUpload`）。

### 影响

| 场景 | 后果 |
|------|------|
| 客户端在 InitMultipart 后崩溃 | 存储后端的 part 数据和 DB 的 upload/part 行永久残留 |
| 客户端网络断开，未 Abort | 同上，且用户无法恢复或清理（除非记住 upload ID） |
| 频繁 InitMultipart 但不 Complete | 存储成本线性增长，无可见告警 |
| S3 客户端 SDK 默认行为（未显式 Abort 的连接断开） | 企业用户面临不可预见的存储账单膨胀 |

### 缺失的能力

1. **`ListUploads` 过期判定**（部分存在）：

   ```go
   // repository/sql_uploads.go 已有 ListUploads
   func (r *repo) ListUploads(ctx context.Context, tenant, bucket, keyMarker, uploadIDMarker string, limit int) ([]Upload, error)
   ```

   需要加上 `created_before` 过滤条件以查找过期 upload。

2. **Stale Multipart Cleaner 后台 Worker**：

   ```go
   type StaleMultipartCleaner struct {
       repo    repository.Repository
       store   storage.Storage
       maxAge  time.Duration // 默认 7 天，与 AWS S3 一致；可配置
       logger  *slog.Logger
   }
   
   func (c *StaleMultipartCleaner) Run(ctx context.Context) {
       // 定期扫描超过 maxAge 的 uploads
       // 对每个 stale upload 执行 AbortMultipart 语义
       // 删除 upload 行 + parts 行 + 存储后端 part
   }
   ```

3. **Parts 级联删除修复**：

   ```go
   // repository/sql_uploads.go 中 DeleteUpload 应级联删除 parts
   func (r *repo) DeleteUpload(ctx context.Context, uploadID string) error {
       _, err := r.db.Exec(ctx, "DELETE FROM parts WHERE upload_id = $1", uploadID)
       if err != nil {
           return err
       }
       _, err = r.db.Exec(ctx, "DELETE FROM uploads WHERE id = $1", uploadID)
       return err
   }
   ```

4. **迁移修复**：为 parts 表添加 `ON DELETE CASCADE` 外键约束，防止 upload 行删除后 parts 行残留。

5. **暴露过期统计**：`telemetry.IncStaleUploadAborted` 计数器，和 `telemetry.StaleUploadGauge` 暴露当前过期 upload 数量。

### 工程估算

| 组件 | 新增/修改 | 行数估计 |
|------|----------|---------|
| `repository/sql_uploads.go` — `DeleteUpload` 级联清理 | 修改 | +5 行 |
| `repository/sql_uploads.go` — `ListStaleUploads` | 新增 | +20 行 |
| `repository/migrations/sqlite/0025_parts_cascade.up.sql` | 新增 | +5 行 DDL |
| `repository/migrations/postgres/0025_parts_cascade.up.sql` | 新增 | +5 行 DDL |
| `reconcile/stale_uploads.go` — StaleMultipartCleaner | 新增 | ~120 行 |
| `cmd/server/main.go` — 启动 cleaner | 修改 | +5 行 |
| `service/file_multipart.go` — `AbortMultipart` 错误处理增强 | 修改 | +10 行 |
| 测试 | 新增 | ~80 行 |
| **总计** | | **~250 行** |

---

## 方向二：S3 Object Lock Retention Mode 分层语义

### 现状

S3 兼容端点接受 `GOVERNANCE` 作为默认保留模式：

```go
// internal/api/s3compat/bucketconfig.go:183
out.Rule = &objectLockRule{DefaultRetention: objectLockRetention{Mode: "GOVERNANCE", Days: days}}
```

运行时检查只有二进制时间比较：

```go
// internal/service/file_crud.go:93-96
if cur.LockedUntil != nil && cur.LockedUntil.After(time.Now()) {
    return fmt.Errorf("%w: overwrite blocked until %s", ErrLocked, cur.LockedUntil.Format(time.RFC3339))
}

// internal/service/file_crud.go:299-301  — hard delete 检查
if obj.Metadata["_aero_legal_hold"] == "ON" {
    return fmt.Errorf("%w: object is under legal hold", ErrLocked)
}
```

**缺失的语义：**

| S3 模式 | 当前行为 | 期望行为 |
|---------|---------|---------|
| `GOVERNANCE` | `locked_until` 检查，所有用户均不可覆盖/删除 | 有 `admin` scope 的用户可缩短保留期或删除 |
| `COMPLIANCE` | `locked_until` 检查，所有用户均不可覆盖/删除 | **任何用户均不可**缩短保留期或删除，包括 root/operator |
| Legal Hold | `_aero_legal_hold` 元标记检查，硬删除阻断 | 独立于 retention date 的永久标记，**任何用户均不可删除**直到移除 Legal Hold |

### 为什么需要

1. **合规性**：SOC2、PCI DSS、SEC 17a-4 等法规要求区分 GOVERNANCE（管理员可绕过）和 COMPLIANCE（绝对不可绕过）的保留策略。无此区分无法通过合规审计。

2. **用户信任**：当用户配置 COMPLIANCE 模式时，他们期望即使系统管理员也无法删除数据。当前的二进制实现无法满足这一承诺。

3. **S3 兼容性**：AWS S3 的 `PUT ObjectLegalHold`、`PUT ObjectRetention`、`GetObjectLegalHold`、`GetObjectRetention` 四个端点均未实现。使用这些 API 的 SDK/工具无法工作。

### 缺失的能力

1. **`retention_mode` 字段存储**：

   ```go
   // repository.Object 增加
   RetentionMode string // "GOVERNANCE" | "COMPLIANCE" | ""
   ```

   需要一个迁移（0025）为 `objects` 表添加 `retention_mode` 列。

2. **S3 API 端点实现**：

   ```go
   // PUT /s3/{bucket}/{key}?legal-hold  — 设置 Legal Hold 状态
   // GET /s3/{bucket}/{key}?legal-hold  — 查询 Legal Hold 状态
   // PUT /s3/{bucket}/{key}?retention   — 设置对象级保留（覆盖桶默认值）
   // GET /s3/{bucket}/{key}?retention   — 查询对象级保留
   ```

3. **运行时模式区分**：

   ```go
   // 在 checkLockBeforeOverwrite / hardDeleteObject 中:
   func (s *FileService) checkLock(ctx context.Context, obj repository.Object, userScopes []string) error {
       if obj.LockedUntil == nil || obj.LockedUntil.Before(time.Now()) {
           return nil
       }
       if obj.RetentionMode == "COMPLIANCE" {
           return ErrLocked // 任何人不可绕过
       }
       // GOVERNANCE 模式：admin scope 用户可以缩短/删除
       if !hasAdminScope(userScopes) {
           return ErrLocked
       }
       return nil
   }
   ```

4. **Legal Hold 强化**：当前 Legal Hold 仅用 `_aero_legal_hold` 元数据标记。应使用专用字段并确保其在所有协议层面一致执行（REST、S3、WebDAV 的 DELETE 路径均需检查）。

### 工程估算

| 组件 | 新增/修改 | 行数估计 |
|------|----------|---------|
| `repository.Object` — `RetentionMode` 字段 | 修改 | +1 行 |
| 迁移 0025 — `retention_mode` 列 | 新增 | 4 文件（sqlite/postgres up/down） |
| `repository/sql_objects.go` — 读写 `retention_mode` | 修改 | +10 行 |
| `api/s3compat/handler.go` — `?legal-hold` / `?retention` 端点 | 新增 | ~150 行 |
| `api/s3compat/xml.go` — LegalHold / Retention XML 编解码 | 新增 | ~80 行 |
| `service/file_crud.go` — `checkLock` 模式感知 | 修改 | +25 行 |
| `service/file_features.go` — `SetObjectLegalHold` / `SetObjectRetention` | 新增 | ~40 行 |
| `internal/api/rest/` — 对应 REST 端点 | 新增 | ~80 行 |
| 测试 | 新增 | ~150 行 |
| **总计** | | **~540 行** |

---

## 方向三：跨协议一致性与 Read-Your-Writes 保障

### 现状

当前系统有三个协议路径，共享同一 `FileService` 核心：

```
REST   /v1/*  ──┐
S3     /s3/*   ──┤──▶  FileService ──▶ Storage (local/S3/OSS/COS)
WebDAV /webdav ──┘                   ──▶ Repository (SQLite/Postgres)
```

写入流程：
```
1. FileService.Put()
2.   → s.store.Put(ctx, storageKey, reader, ...)  // 写入存储后端
3.   → s.repo.UpsertObject(ctx, obj)               // 写入元数据
4.   → s.emit(ctx, saved, EventCreated)            // 发布事件
```

**一致性问题：**

| 配置 | 写入步骤 2 与 3 的窗口 | 后果 |
|------|----------------------|------|
| local FS + SQLite（默认） | 无窗口（本地原子操作） | 一致 ✅ |
| **S3 + Postgres** | **步骤 2（S3 HTTP PUT）成功后步骤 3（DB INSERT）未完成时，并发 GET 可能：** (a) 通过 DB 找不到对象 → 404；(b) 通过存储后端直接读取到部分或全部 blob | ❌ 不一致 |
| 任意 + Postgres 复制延迟 | Postgres 主从复制期间，读副本可能看不到刚写入的行 | ❌ 最终一致（无保证） |

对于 S3 兼容性，AWS S3 提供 **read-after-write consistency for PUT of new objects** 和 **eventual consistency for overwrite PUTs and DELETEs**。当前系统未定义或文档化其一致性模型。

### 为什么需要

1. **用户期望**：用户写入一个文件后立即读取，期望看到刚写入的内容。在 S3 后端 + Postgres 配置下，无法保证这一点。

2. **S3 兼容性契约**：AWS S3 的一致性模型是已知且文档化的。AeroVault 需要明确其自身的一致性模型，否则用户无法判断其行为是否适合他们的应用。

3. **跨协议场景增加问题复杂度**：
   - 通过 REST PUT 写入 → 通过 S3 GET 读取
   - 通过 S3 PUT 写入 → 通过 WebDAV PROPFIND 读取
   - 通过 WebDAV PUT 写入 → 通过 REST GET 读取（带 Range 请求）

   每一步都经过 FileService，但相同的写入窗口存在。

### 缺失的能力

1. **一致性模型文档化**：

   在 `docs/api.md` 或 `docs/architecture.md` 中增加明确的一致性契约：

   ```markdown
   ## 一致性模型

   ### SQLite + local FS（默认配置）
   - 新对象 PUT：**Read-After-Write 一致性**
   - 对象覆盖 PUT：**Read-After-Write 一致性**
   - 对象 DELETE：**Read-After-Write 一致性**
   
   原因：SQLite 事务 + 本地文件系统写入在同一进程内，无网络窗口。

   ### Postgres + S3（生产配置）
   - 新对象 PUT：**最终一致性**（写入窗口 = S3 PUT 完成到 DB INSERT 的时间）
   - 对象覆盖 PUT：**最终一致性**
   - 对象 DELETE：**最终一致性**
   
   建议：使用 Postgres 同步提交 + 应用层读后写确认。
   ```

2. **读后写确认（可选，高性能配置）**：

   ```go
   // FileService.Put 返回后，确保对象可读
   func (s *FileService) Put(ctx context.Context, ...) (repository.Object, error) {
       // ... 现有写入逻辑 ...
       // 可选：写后确认读取
       if cfg.ReadAfterWriteVerify {
           if err := s.waitForConsistency(ctx, obj); err != nil {
               return repository.Object{}, fmt.Errorf("consistency check failed: %w", err)
           }
       }
       return saved, nil
   }
   
   func (s *FileService) waitForConsistency(ctx context.Context, obj repository.Object) error {
       // 最多重试 3 次，最多等待 2s
       return retry.WithBackoff(ctx, 3, 100*time.Millisecond, func() error {
           _, err := s.repo.GetObject(ctx, obj.TenantID, obj.Bucket, obj.Key)
           return err
       })
   }
   ```

3. **Postgres 同步提交配置指导**：文档化如何在 Postgres 侧启用 `synchronous_commit = on` 以减少故障切换时的数据丢失窗口。

4. **一致性验证集成测试**：

   ```go
   // internal/integration/consistency_test.go
   func TestReadAfterWriteConsistency(t *testing.T) {
       // 1. 通过 REST PUT 写入对象
       // 2. 立即通过 S3 GET 读取（相同 tenant/bucket/key）
       // 3. 验证内容、ETag、元数据一致
       // 4. 通过 WebDAV PROPFIND 验证
       // 5. 覆盖写入后再次验证
   }
   ```

### 工程估算

| 组件 | 新增/修改 | 行数估计 |
|------|----------|---------|
| `docs/api.md` — 一致性模型章节 | 新增 | ~50 行文档 |
| `docs/architecture.md` — 一致性模型补充 | 修改 | +30 行 |
| `internal/service/file_crud.go` — 可选一致性确认 | 新增 | +30 行 |
| `internal/config/config.go` — `ReadAfterWriteVerify` 配置项 | 新增 | +5 行 |
| 一致性集成测试 | 新增 | ~80 行 |
| **总计** | | **~195 行** |

> **注意：** 这个方向主要是"文档化 + 可选增强"，工程成本低但产品信任价值高。

---

## 方向四：事件流交付保障与 SSE Reconnection 协议

### 现状

事件系统当前架构：

```go
// internal/events/bus.go
const defaultSubBuffer = 64  // 每个 subscriber 通道 64 深度

func (b *Bus) Subscribe() <-chan repository.Event {
    ch := make(chan repository.Event, b.subBuffer)
    b.subs = append(b.subs, ch)
    return ch
}

func (b *Bus) broadcast(e repository.Event) {
    for _, ch := range b.subs {
        select {
        case ch <- e:
        default:
            b.dropped.Add(1)  // 静默丢弃
        }
    }
}
```

SSE 端点实现了 `Last-Event-ID` 回放，但回放逻辑仅查询未消费事件：

```go
// internal/api/rest/sse.go
func (h *SSEHandler) replayMissed(w http.ResponseWriter, flusher http.Flusher, r *http.Request, tenant string, lastID int64) {
    backlog, err := h.repo.NextUnconsumedEvents(r.Context(), 200)  // 最多 200 条
    // ...
    for _, e := range backlog {
        if e.ID <= lastID || e.TenantID != tenant {
            continue
        }
        writeEvent(w, flusher, e)
    }
}
```

**问题清单：**

| 问题 | 严重程度 | 说明 |
|------|---------|------|
| 1. 64 深度缓冲区静默丢弃 | 🔴 高 | 当 SSE 客户端或 worker 消费慢时，事件丢弃无反馈、无告警 |
| 2. `replayMissed` 仅查 200 条 | 🟠 中 | 断连时间长时，回放不完整且不翻页 |
| 3. 无消费确认机制 | 🟠 中 | 无法判断事件是否被消费者处理完成 |
| 4. 无 subscriber 健康检测 | 🟠 中 | 死 subscriber（goroutine 泄漏）无法自动清理 |
| 5. 无事件投递延迟指标 | 🟡 低 | 无法度量从事件产生到消费者接收的端到端延迟 |
| 6. `EventBus.Close` 无 drain 支持 | 🟡 低 | 关闭时立即切断 subscriber，已入队但未消费的事件丢失 |

### 为什么需要

1. **可靠性断言 vs 最终行为**：系统文档声称"durable events"，但运行时行为是"best-effort delivery"。在 64 深度缓冲区填满后，事件被静默丢弃且不影响 DB 中的持久化副本——但 SSE 消费者永远收不到这些事件。

2. **生产可运维性**：运维人员需要在事件交付延迟或丢弃时收到告警，而非静默丢失。这对事件驱动架构的可靠性至关重要。

3. **客户端体验**：SSE 消费者（Web UI、自动化工具有 Agent 客户端）断连后重新连接，期望无缝恢复，而不是丢失中间事件。

### 缺失的能力

1. **Subscriber 压力告警**：

   ```go
   // 在 broadcast 丢弃时增加结构化指标
   telemetry.IncEventDropped(ctx, "subscriber_buffer_full")
   telemetry.ObserveEventDeliveryLag(ctx, time.Since(e.CreatedAt))
   ```

   并且暴露 Prometheus 告警规则：`events_dropped_total > 0` → Warning，`events_dropped_total > 100` → Critical。

2. **可配置 subscriber buffer 深度**：

   ```go
   // 为每个 subscriber 类型设置不同 buffer
   // SSE 客户端：更大的 buffer（512）容忍网络抖动
   // Worker 消费者：默认 64
   type SubscriberOption struct {
       BufferSize int
       Name       string
   }
   func (b *Bus) SubscribeWith(opts SubscriberOption) <-chan repository.Event
   ```

3. **Replay 翻页与限界**：

   ```go
   func (h *SSEHandler) replayMissed(w http.ResponseWriter, flusher http.Flusher, r *http.Request, tenant string, lastID int64) {
       if lastID <= 0 {
           return
       }
       var maxID int64
       for {
           backlog, err := h.repo.NextUnconsumedEvents(r.Context(), 1000, lastID)  // 从 lastID 开始
           if err != nil || len(backlog) == 0 {
               break
           }
           for _, e := range backlog {
               if e.ID <= lastID || e.TenantID != tenant {
                   continue
               }
               if !writeEvent(w, flusher, e) {
                   return
               }
               maxID = e.ID
           }
           if len(backlog) < 1000 {
               break
           }
           lastID = maxID  // 翻页
       }
   }
   ```

4. **Consumer 健康检测与自动清理**：

   ```go
   // 对长时间未从 subscriber 通道读取的消费者进行日志警告或自动移除
   type trackedSubscriber struct {
       ch       chan repository.Event
       name     string
       lastRead time.Time
   }
   
   func (b *Bus) healthCheck(ctx context.Context) {
       // 定期检查所有 subscriber 的最后读取时间
       // 超过阈值（如 5 分钟）的 subscriber 记录警告
       // 超过硬限（如 30 分钟）的 subscriber 自动移除并关闭通道
   }
   ```

5. **Graceful Drain**：

   ```go
   func (b *Bus) Shutdown(ctx context.Context) error {
       // 等待所有 subscriber 消费完当前 buffer 中的事件
       // 超时后强制关闭
       done := make(chan struct{})
       go func() {
           b.waitForDrain()
           close(done)
       }()
       select {
       case <-done:
       case <-ctx.Done():
       }
       b.Close()
       return nil
   }
   ```

### 工程估算

| 组件 | 新增/修改 | 行数估计 |
|------|----------|---------|
| `internal/events/bus.go` — SubscriberOption、trackedSubscriber、healthCheck、Shutdown | 修改 | +120 行 |
| `internal/telemetry/metrics.go` — 事件相关指标增强 | 修改 | +20 行 |
| `internal/api/rest/sse.go` — replay 翻页 | 修改 | +30 行 |
| `internal/config/config.go` — SSE buffer 配置项 | 新增 | +5 行 |
| `deploy/prometheus/alerts.yml` — 事件丢弃告警 | 修改 | +10 行 |
| 测试 | 修改 | ~80 行 |
| **总计** | | **~265 行** |

---

## 方向五：S3 批量操作 API & 对象级元数据搜索

### 现状

**批量操作：**

REST 端点有 batch delete 和 batch tag：

```go
// internal/api/rest/router.go
r.Post("/batch/delete", h.BatchDelete)
r.Post("/batch/tag", h.BatchTag)
```

但 S3 协议没有对应的批量操作 API。当前 S3 批量删除通过 `POST /{bucket}?delete` 实现（标准 S3 API），但更广泛的 S3 Batch Operations API（提交作业、查询作业状态、列出作业）完全不存在。

**元数据搜索：**

```go
// internal/ai/search.go — Search.Query 只搜索分块后的内容
func (s *Search) Query(ctx context.Context, req Request) ([]Hit, error)
```

没有按以下维度的结构化搜索：

- `metadata.key = "value"`（用户元数据查询）
- `tag.key = "value"`（S3 标签查询）
- `storage_class = "STANDARD_IA"`
- `size > 1048576`（大于 1MB 的对象）
- `created_at > "2026-01-01"`
- 组合过滤：`tag:project=alpha AND size > 1MB`

### 为什么需要

1. **S3 兼容性**：S3 Batch Operations 是企业级大规模数据管理的标准工具。缺少它意味着用户无法通过标准 S3 SDK 或 AWS CLI 执行大规模操作。

2. **运营效率**：当前 `batch/delete` 和 `batch/tag` 是同步的、单次请求的。对于数十万对象的操作（如将所有对象的存储类从 STANDARD 改为 STANDARD_IA），需要一个异步、可追踪的批处理框架。

3. **元数据搜索是 AI 管线的自然补充**：当前 Search API 已能按语义搜索内容，但无法按元数据过滤。结合语义搜索 + 结构化的元数据过滤是一个强大的产品能力，可与其他 RAG 平台形成差异化。

### 缺失的能力

1. **S3 Batch Operations 框架**：

   ```go
   // 提交批处理作业
   // POST /v1/admin/batch  → {operation: "put_object_tagging", targets: [...], ...}
   // → 创建 BatchJob 记录 → 返回 job ID
   
   // 查询作业状态
   // GET /v1/admin/batch/{jobID} → {status, progress, completed_count, failed_count, ...}
   
   // 列出作业
   // GET /v1/admin/batch → 分页列出所有批处理作业
   
   // 支持的操作：
   // - put_object_tagging: 批量设置标签
   // - put_object_acl: 批量设置 ACL
   // - restore_object: 批量恢复归档对象
   // - copy_object: 批量复制对象（跨桶/跨区）
   // - delete_object: 批量删除对象
   // - replace_metadata: 批量替换元数据
   ```

2. **元数据搜索 API**：

   ```go
   // POST /v1/search/metadata
   // {
   //   "filters": [
   //     {"field": "tag", "key": "project", "op": "eq", "value": "alpha"},
   //     {"field": "size", "op": "gt", "value": 1048576},
   //     {"field": "storage_class", "op": "eq", "value": "STANDARD_IA"}
   //   ],
   //   "limit": 100,
   //   "offset": 0
   // }
   
   // 返回匹配的对象列表 + 总数
   ```

   这可以通过在 Repository 层实现一个灵活的查询构建器来实现：

   ```go
   type MetadataQuery struct {
       Tenant   string
       Bucket   string
       Filters  []MetadataFilter
       Limit    int
       Offset   int
       OrderBy  string // "created_at", "size", "key"
       OrderDir string // "asc", "desc"
   }
   
   type MetadataFilter struct {
       Field string // "tag", "metadata", "size", "storage_class", "created_at", "content_type"
       Key   string // 用于 tag/metadata 字段
       Op    string // "eq", "neq", "gt", "gte", "lt", "lte", "contains", "exists"
       Value string
   }
   
   func (r *repo) SearchMetadata(ctx context.Context, q MetadataQuery) ([]Object, int64, error)
   ```

3. **批量作业持久化与执行**：重用 `jobs` 表作为批量作业的执行引擎。每个批处理作业拆分为多个子作业（每个子作业处理一批对象），通过现有 job queue 分布式执行。

### 工程估算

| 组件 | 新增/修改 | 行数估计 |
|------|----------|---------|
| `BatchJob` 模型 + DB 迁移 0025/0026 | 新增 | ~80 行（模型）+ 8 迁移文件 |
| `repository/sql_batch.go` — 批处理作业 CRUD | 新增 | ~100 行 |
| `service/batch.go` — 批处理服务 | 新增 | ~200 行 |
| `api/rest/batch.go` — REST 批处理端点 | 新增 | ~120 行 |
| 元数据搜索 Repository 层 | 新增 | ~150 行 |
| 元数据搜索 REST 端点 | 新增 | ~80 行 |
| 测试 | 新增 | ~200 行 |
| **总计** | | **~930 行** |

> **备注：** 这是五个方向中工程量最大的，但同时也是产品差异化最显著的。如果优先实现元数据搜索（约 250 行），可快速获得产品价值，批处理框架后续逐步完善。

---

## 综合优先级建议

```
方向一：Multipart 过期清理          ████████████████████  P1  250 行  ← 成本控制 + 数据安全
方向四：事件流交付保障              ████████████████      P2  265 行  ← 事件驱动架构可信度
方向二：Object Lock 模式语义        ████████████          P2  540 行  ← 合规 + S3 深度
方向三：跨协议一致性                ██████████            P2  195 行  ← 产品信任
方向五：S3 批量操作 + 元数据搜索     ████████              P2  930 行  ← 差异化 + 运营效率
```

**建议执行顺序：**

1. **第 1 批（本周）**：方向一（Multipart 过期清理）— 工程最小、影响立即可见。修复 `DeleteUpload` 级联清理是 5 行改动，防范数据泄露。
2. **第 2 批（下周）**：方向三（跨协议一致性文档化）— 纯文档 + 可选确认，零风险。方向四（事件交付保障）— 按需增强。
3. **第 3 批（下月）**：方向二（Object Lock 模式）— 合规关键功能。方向五前半部分（元数据搜索）— 与 AI Search 形成互补。
4. **第 4 批（季度）**：方向五后半部分（Batch Operations 框架）— 大规模运营需求。

---

## 结论

这五个方向标志着 AeroVault 从"功能完整"到"运营可信"的最后跨越：

- **方向一 (Multipart GC)** 解决了无声的成本泄漏
- **方向二 (Object Lock 模式)** 弥合了合规性的语义鸿沟
- **方向三 (跨协议一致性)** 定义了产品应许的信任基线
- **方向四 (事件交付保障)** 将事件系统从 best-effort 提升到 at-least-once
- **方向五 (批处理 & 元数据搜索)** 打开了企业级运营效率的大门

其中方向一和方向四是"不能让系统在无人知晓的情况下出问题"的信任基线；方向二和方向三是"说到的必须做到"的合规基线；方向五是"让用户在 AeroVault 上完成真实工作"的效率基线。
