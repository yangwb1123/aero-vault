# Design: 单一 versioned-delete 契约 —— 所有 S3 删除路径（含 multipart abort）统一汇入 `vault.file.deleted@1.1` 事务性 outbox

> **Companion spec:** `docs/requirements/durable-async-delete-outbox-s3compat-v1.md`（FR-1…FR-5, AC-1…AC-5）· **Module:** `internal/api/s3compat`（组合面：`internal/service` + `internal/repository` + `internal/events`）· **Status:** design（实现未开始）· **Baseline:** HEAD `acfaaf4` + in-flight WIP（fail-closed/authz 批次）· **Gates:** `make check` green · stdlib only（I6）· I1/I2 discipline（**零 DB 迁移**）· **零 wire-level API 变更** · 单文件 ≤ 500 行 / 单函数 ≤ 50 行

---

## 1. Evidence re-verification（对照 HEAD 独立复核）

对 requirements 规格的全部 11 条引用 + 现状基础设施主张逐条复核（读源码 + 实跑测试）。**全部成立**；仅 4 处行号漂移（无实质影响）。实测：`go build ./...` 0 · `gofmt -l` 空 · `go test ./internal/repository/ -run 'TestDeleteObjectWithAudit_OneTx|TestEventOutbox'` ok · `./internal/events -run 'Outbox|EventSchema'` ok · `./internal/integration -run 'TestDeleteResponse_DoesNotBlockOnDelivery|TestComposition_DeleteDeliversBothFacts'` ok · `./internal/api/s3compat/` ok。

| # | Spec 引用 | 复核结果 |
|---|----------|---------|
| E1 | `s3compat/delete.go:11` `deleteS3Object` 三分支漏斗（versionID→`DeleteVersion`；versioned→`CreateDeleteMarker`；否则 `svc.Delete(hard=true)`） | ✅ 精确 :11-34；handler 不自造删除逻辑 |
| E2 | `s3compat/extra.go:379/430` abort / batch | ✅ 精确 :379 `abortMultipartUpload`（`ErrUploadNotFound` 吞掉→204，无事件）、:430 `deleteObjects`（逐对象 `authorizeDelete` + `deleteS3Object`） |
| E3 | `s3compat/handler.go:265` `DeleteObject` | ✅ :265（spec 引 :264，漂移 +1）；`?tagging` / `?uploadId` / 默认三分支 |
| E4 | `service/file.go:297` `emit` 最小 payload、吞错 | ✅ :297-314；payload `{backend,size,etag,content_type}` |
| E5 | `service/file_delete.go` `Delete` :147 / `DeleteVersion` :174 | ✅ 精确 :147/:174；:123 `deleteFacts`、:100 `deleteAuditEntry`、:212 遗留 emit、:214-215 promoted `EventCreated`；`DeleteVersion` 调 `repo.DeleteObjectVersion`（无 facts/audit） |
| E6 | `service/delete_marker.go:58` marker 创建发 `EventDeleted`，`InsertDeleteMarker` 事务内无 audit/facts | ✅ :58；`sql_objects_versions.go:10-71` 复核：tombstone UPDATE + access-state 清理 + INSERT RETURNING id，无 audit/facts |
| E7 | `service/file_multipart.go:263` `AbortMultipartFor` 无 `s.emit` | ✅ :263；`grep -n "s.emit" file_multipart.go` 零命中；幂等链 :277-309（Claim → storage abort → Complete → `DeleteUpload`（sql_uploads.go:45 单语句无事务）→ 失败 `DeleteIdempotencyKey` 允许重试） |
| E8 | `repository/repository.go:175-185` `Event` 扁平无版本 | ✅ :175-185；`EventType` 仅 4 值 :190-194 |
| E9 | `events/bus.go` `Publish` 吞错 | ✅ :80-81 注释原文；`InsertEvent`（sql_events.go:12-38）在业务事务外 |
| E10 | outbox 已落地：`EventTypeFileDeleted11/Notify11` :22-25、`validateOutboxFacts` :61（`OriginID≤0` 拒绝 :73-76）、`HardDeleteObjectWithEvent` :102 / `SoftDeleteObjectWithEvent` :147（同事务 audit+facts、零行回滚 `ErrNotFound`）、relay `Run` :111 / `deliverBatch` :140 / `deliverFact` :171 / `deliverDeleted` :190 / `deliverNotify` :236 / `prune` :362、`cmd/server/workers.go:158` 常开 | ✅ 全部精确；迁移 `0041_event_outbox.{up,down}.sql` 双方言存在 |
| E11 | payload builders / goldens / 测试基建 | ✅ `payload.go`：`deletedFact` :33、`BuildDeletedFact` :109、`BuildNotifyFact` :137（spec 引 :105/:132，漂移 4-5）；golden 常量 :15 / `TestEventSchema_GoldenJSON` :31 / `TestEventSchema_Deleted11Envelope` :96；`authz_gate_test.go` `newAuthzServer` :66（spec 引 :68，漂移 2）/ `outboxCount` :146 / 拒绝侧 0 行断言 :354（`TestDeniedDeleteWritesNoOutboxRows` :324）；`fullserver_test.go` `TestDeleteResponse_DoesNotBlockOnDelivery` :685 / `TestComposition_DeleteDeliversBothFacts` 注释 :867 / func :876；`event_outbox_test.go` `TestDeleteObjectWithAudit_OneTx` :71 / `TestEventOutboxClaimLeaseExpiryRedelivers` :259；`notifier.go:74` D2 去重 | ✅ |
| E12 | **真实缺口**：五条 S3 删除路径仅 P1 写 outbox 事实；P2/P3/P5 无 outbox 事实 + 无 audit 行，P5 连遗留事件都没有 | ✅ 复核确认（P2 `InsertDeleteMarker` 事务无 audit/facts；P3 `DeleteObjectVersion`（sql_objects_maint.go:143-190）无 audit/facts；P5 全静默） |

**方向文陈旧主张的修正（spec §1 已做，复核认同）：** "internal/ 中无 'vault.file.' 字符串"已不成立——outbox 机制（类型、builders、relay、WithEvent 同事务方法）前序 campaign 已落地且全绿。本设计**不是绿地设计**：机制核心复用，工作 = P2/P3/P5 三个事务方法 + 载荷加性字段 + 服务层接线 + 测试。

---

## 2. API changes

### 2.1 Wire-level（协议/配置）—— **none**

| S3 路径 | 请求 | 响应契约（设计前后逐字节相同） |
|---------|------|------------------------------|
| P1 单删（非 versioned） | `DELETE /s3/{b}/{k}` | 204；无版本头 |
| P2 marker 创建 | 同上（versioned 桶） | 204 + `x-amz-version-id` + `x-amz-delete-marker: true` |
| P3 版本删除 | `DELETE /s3/{b}/{k}?versionId=v` | 204 + `x-amz-version-id`（+`x-amz-delete-marker` 当被删版本是 marker） |
| P4 批量 | `POST /s3/{b}?delete` | 200 shell，`<Deleted>`/`<Error>` 逐项（含 per-key AccessDenied，WIP 语义不变） |
| P5 abort | `DELETE /s3/{b}/{k}?uploadId=u` | 204（含 `ErrUploadNotFound` no-op 语义） |

无新路由、无 OpenAPI diff、无新 env/flag、无 SSE 帧变化、无新迁移。outbox 事实是内部持久化 + relay 投递；`schema_version` 保持 `"1.1"`。

### 2.2 Go-level（完整破坏面）

**新增 repository 接口（`repository_interface.go` +3）：**

```go
InsertDeleteMarkerWithEvent(ctx context.Context, obj Object, entry AuditEntry, facts []OutboxFact) (Object, error)
DeleteObjectVersionWithEvent(ctx context.Context, tenant, bucket, key, versionID string, entry AuditEntry, facts []OutboxFact) error
DeleteUploadWithEvent(ctx context.Context, uploadID string, entry AuditEntry, facts []OutboxFact) error
```

**`internal/repository/event_outbox.go`（最小改动）：**

```go
type OutboxOriginKind string
const (
    OriginKindObject OutboxOriginKind = ""       // 零值 = objects.id 语义（既有调用方零改动）
    OriginKindUpload OutboxOriginKind = "upload" // multipart upload 无 objects 行
)
// OutboxFact 增字段（不持久化——仅 enqueue 时校验用）：
type OutboxFact struct {
    EventType  OutboxEventType
    OriginID   int64
    OriginKind OutboxOriginKind // 新增
    TenantID   string
    Payload    []byte
}
```

`validateOutboxFacts` 放宽（fail-closed 形状）：`OriginKind==OriginKindUpload` 时 `OriginID` 可为 0（tenant/payload 校验不变），**且强制** `EventType==EventTypeFileDeleted11`（upload-origin notify 事实无意义，拒绝）；其他非零 kind 拒绝；`OriginKindObject`（零值）维持 `OriginID>0`。relay 的 claim/complete/retry/prune 只读行 id + payload → **零改动**。

**新增 `internal/repository/event_outbox_objects.go`（三个事务方法，各自 ≤50 行）：**

- `InsertDeleteMarkerWithEvent`：事务 = 现 `InsertDeleteMarker` 全流程（tombstone UPDATE + `deleteObjectAccessState` + marker INSERT RETURNING id, created_at, updated_at）→ **用返回行 id 回填 `facts[i].OriginID`**（共享底层数组的元素赋值，注释明示；对象实体在构建期不存在，payload `object_id:0`，见 D-1）→ `validateOutboxFacts`（事务内，D-2 先例）→ `insertAuditEntry` → `insertOutboxFacts` → Commit；返回完整 marker 行（ID/CreatedAt/UpdatedAt/DeletedAt）供 service 继续遗留 `s.emit`。
- `DeleteObjectVersionWithEvent`：事务 = 现 `DeleteObjectVersion` 全流程（SELECT id+wasCurrent（ErrNotFound 守卫）+ legal-hold 检查 + wasCurrent 时 access-state 清理 + `DELETE FROM objects WHERE id=$1` + promote + 空桶清理）**+ 新增 `RowsAffected()==0 → ErrNotFound` 守卫**（Postgres 并发双删防幽灵事实，D-2）→ validate → audit → facts → Commit。
- `DeleteUploadWithEvent`：事务 = `DELETE FROM multipart_uploads WHERE upload_id=$1`，`RowsAffected()==0 → ErrUploadNotFound`（杜绝幽灵事实）→ validate → audit → facts → Commit。

**`internal/events/payload.go`（加性，golden 恒等）：**

```go
type deletedFact struct {
    // …既有字段顺序不变（golden 字节依赖）…
    Reason       string `json:"reason,omitempty"`
    DeleteMarker bool   `json:"delete_marker,omitempty"` // 新增，结构体末尾
    Origin       string `json:"origin,omitempty"`        // 新增，结构体末尾
}
func BuildDeleteMarkerFact(obj repository.Object, actor, requestID, tenant string) []byte
func BuildUploadDeletedFact(u repository.Upload, actor, requestID, tenant string) []byte
// 内部共享一个私有 marshal 助手；每个导出函数 ≤50 行
```

- `BuildDeleteMarkerFact`：同一 `deletedFact`，`DeleteMarker:true`。
- `BuildUploadDeletedFact`：`ObjectID:0`、`VersionID:u.VersionID`、`Size:0`、`ETag:""`、`Backend:u.Backend`、`Origin:"multipart_upload"`。

**`internal/service`（三个调用点切换 + 三个事实构建器）：**

| 位置 | 改动 |
|------|------|
| `file_delete.go` | 新增 `versionDeleteFacts(ctx, obj, tenant)` = deleted@1.1（`IsDeleteMarker(obj)` 时用 `BuildDeleteMarkerFact`，否则 `BuildDeletedFact`）+ notify@1.1（镜像 `deleteFacts` 形状）；`deleteFacts`（P1/REST）**零改动**（D-4）；`DeleteVersion` 把 `repo.DeleteObjectVersion` 换成 `DeleteObjectVersionWithEvent(ctx, …, s.deleteAuditEntry(ctx, obj, obj.TenantID, true), facts)`；chunkCleaner/store.Delete 在事务外先行的既有顺序不动 |
| `delete_marker.go` | 新增 `deleteMarkerFacts(ctx, marker, tenant)` = 恰 1 条 deleted@1.1（`BuildDeleteMarkerFact`，无 notify）；`CreateDeleteMarker` 预生成 `marker.VersionID = repository.NewVersionID()`（导出，先例 file_multipart.go:66）→ 构建 facts/audit → `InsertDeleteMarkerWithEvent` → 遗留 `s.emit` 原样 |
| `file_multipart.go` | 新增 `uploadAbortFacts(ctx, u, tenant)` = 恰 1 条 deleted@1.1（`BuildUploadDeletedFact`，无 notify）；`AbortMultipartFor` claimed 分支最后一步换 `DeleteUploadWithEvent`；`errors.Is(err, repository.ErrUploadNotFound) → return nil`（缓存保留，204 语义不变，D-6）；其他错误沿用 `DeleteIdempotencyKey` 允许重试 |
| `file_delete.go` | `deleteAuditEntry` 内部抽取共享核心 `auditEntryFor(ctx, bucket, key, tenant, detail string)`；P2 detail `"delete_marker"`、P5 detail `"multipart_abort"`、P3 复用 `"hard"`（D-3） |

**测试基建复用（不改动）：** `newAuthzServer`（无 relay，天然满足 AC-2 的"删除响应先于 relay"信号）· `outboxCount`（`origin_id=?` 查询，P5 传 0 即可）· relay 测试模式（`NewEventOutboxRelay` + 注入 `AuditSink`）· `fullserver_test.go` harness（`startFullServerWithRelay` :55 等）。

---

## 3. Compatibility constraints

| # | 约束 | 机制 |
|---|------|------|
| C-1 | **S3 wire 契约不变（硬）**：五路径状态码/响应头逐字节相同（§2.1 表） | 零 handler 改动；`deleteS3Object` 漏斗原样 |
| C-2 | **遗留 `object_events`/SSE/webhook 流不变**：P1 :53、P2 :58、P3 :212 + promoted `EventCreated` :214-215 的 `s.emit` 原样保留；P5 保持静默；`Bus.Publish` 签名与吞错语义、订阅者缓冲、`IncEventDropped` 计数均不变 | outbox 是加性持久化路径；不新增/不删除任何 `s.emit` 调用 |
| C-3 | **D2 通知去重契约**：P3 新增 notify@1.1 事实后，notifier（notifier.go:74）会在 P3 的 `EventDeleted` 上跳过 bus 路径（WithEvent 事务先于 `s.emit` 提交，无竞态）→ P3 的 bucket 通知投递面从"bus 尽力而为"迁移到"relay 持久投递"，**仍恰好一次**，`eventName` 仍为 `s3:ObjectRemoved:Delete`；P2/P5 不产生 notify 事实 → notifier bus 路径原样 | relay `deliverNotify` 字节原样 POST + D2 skip |
| C-4 | **Golden 字节恒等**：新字段在结构体末尾 + omitempty → `schema_test.go` 既有 golden（:15/:17）与 `TestEventSchema_GoldenJSON` 原样通过 | 字段顺序固定 + 加性字段 |
| C-5 | **零迁移（I2）**：`OriginKind` 不持久化（enqueue 时断言）；`event_outbox` 表结构不变；`0041_event_outbox` 双方言已存在 | — |
| C-6 | **abort 幂等**：重放/重试调用不产生第二条事实（idempotency 缓存分支在任何 DB 写前短路） | 既有 Claim/Complete 流程不动 |
| C-7 | **错误身份保留**：新方法返回 `ErrNotFound`/`ErrUploadNotFound` → 适配器 no-op→204 映射不变（handler.go:269-271 / extra.go:383-385） | 哨兵复用 |
| C-8 | **既有调用方零改动**：`OutboxFact.OriginKind` 零值 = object 语义 → REST `deleteFacts`、AV 隔离等全部现有构造点不变；`InsertDeleteMarker`/`DeleteObjectVersion`/`DeleteUpload` 无事件变体**保留**（UPLOAD_GC/reconcile 等其余调用方不受影响） | 新方法加性，不替换旧方法 |
| C-9 | **relay 零改动**：claim/complete/retry/prune/`deliverDeleted`/`deliverNotify` 只读行 id + payload；新载荷字段随 payload 字节透传（L2 AuditSink 收到加性 JSON 字段，容忍） | — |
| C-10 | **工程门禁**：单文件 ≤500 行（新方法独立文件）；单函数 ≤50 行；I1（rebind 不复用占位符）；I6（纯 stdlib）；Postgres 方言差异沿用既有 `WithEvent` 形状 | 新增文件 `event_outbox_objects.go` |

---

## 4. Failure modes

| # | 触发 | 可观察 | 行为 | 设计响应 |
|---|------|--------|------|---------|
| FM-1 | P2 事务中途 DB 错误（断连/锁） | S3 5xx；marker 行、audit、facts 全回滚 | all-or-nothing（AC-1 负钉形状） | 单事务方法；客户端重试无残留 |
| FM-2 | P3 并发双删同一 version | 第二删 204（no-op 语义） | `RowsAffected==0 → ErrNotFound` 回滚 → 无幽灵 audit/facts（**比现状更强**：现状 SELECT 后 DELETE 无行数守卫） | D-2 守卫 |
| FM-3 | P3 blob 已删、事务失败（blob-gone-row-remains 窗口） | 版本行残留 + blob 缺失 | **现状形状不变**（chunkCleaner/store.Delete 在事务外先行，P1 同构）；reconcile scrub 既有回收 | 不改变既有顺序 |
| FM-4 | P5 零行（UPLOAD_GC 抢先删了 upload 行） | 204（no-op） | `ErrUploadNotFound → return nil`：idempotency 缓存保留为完成态，重放 204 短路；无事实 | D-6 |
| FM-5 | P5 事实校验失败（payload 非法） | 5xx + idempotency key 释放 | 事务回滚（storage abort 已发生，与现状同构）→ 重试收敛到**恰好一条**事实 | 既有 release 链 |
| FM-6 | P5 崩溃于 CompleteIdempotencyKey 与 DeleteUploadWithEvent 之间 | upload 行 + 完成缓存并存 | 重试 204 走缓存；行由 UPLOAD_GC 回收；**无事实**（删除未完成）——与现状窗口同形 | 不加新恢复代码 |
| FM-7 | relay 投递中断/claim 租约过期 | 行 pending→inflight→pending 循环 | 既有 at-least-once；complete 后恰好一次（`event_outbox_delivered` 同事务） | 既有机制，AC-2 钉死 |
| FM-8 | P3 notify 投递 terminal-failed（MaxAttempts 耗尽） | 通知丢失 | 迁移后比现状**更强**：现状 notifier bus 路径单次 POST 无重试；relay 有退避重试至 failed | C-3 记录 |
| FM-9 | `OriginKind` 误用（upload kind 配非 deleted 类型 / 未知 kind / object kind 配 OriginID=0） | 事务回滚 + 5xx | fail-closed 校验（D-8） | validate 内强制 |
| FM-10 | audit_log 增长（P2/P3/P5 新增行） | 表增长 | 既有 audit governance 保留策略覆盖；无新表 | — |

---

## 5. Migration steps

1. **零 DB 迁移**（I2）：`event_outbox` 表结构不变；`OriginKind` 仅内存断言。部署 = 替换二进制。
2. **部署后行为变化**：P2/P3/P5 路径在首次使用时开始写入 `audit_log` 行 + `event_outbox` 行；relay 已常开（workers.go:158），新行自动进入 claim→deliver→complete 生命周期，无操作。
3. **无 backfill**：历史删除（部署前）不补事实——契约是前向的（AC 全部针对新删除）。已存在的 `pending`/`delivered`/`failed` 行不受影响。
4. **验证 SQL**（每路径一次）：`SELECT count(*) FROM event_outbox WHERE event_type='vault.file.deleted@1.1'`（P2/P3 可加 `origin_id` 条件；P5 为 0）与 `SELECT count(*) FROM audit_log WHERE action='file.delete'`；L2 目标日志确认 deleted@1.1 POST。
5. **回滚**：仅回退二进制。旧版 relay 会照常 claim 新版写入的行（payload schema 1.1 未变，加性字段原样透传）；L2 AuditSink 目标需容忍额外 JSON 字段（`delete_marker`/`origin`）——加性，不破坏解析。

---

## 6. Testable acceptance mapping

### AC-1 单元：五条 S3 删除路径各产生**恰好一条** deleted@1.1 行

`internal/api/s3compat/delete_outbox_test.go`（新文件，表驱动；每行路径独立 `newAuthzServer`/DSN）：

| 路径 | 前置 | 请求 |
|------|------|------|
| P1 | 非 versioned 桶 | PUT 对象 → `DELETE /s3/{b}/{k}` → 204 |
| P2 | versioned 桶（PUT 对象后开 versioning） | `DELETE /s3/{b}/{k}` → 204 + `x-amz-delete-marker:true` |
| P3 | 上述桶再 PUT 第二版本 | `DELETE /s3/{b}/{k}?versionId=v2` → 204 |
| P4 | versioned 桶 2 对象（1 无 versionId → 继承 P2 marker 路径；1 带 `?versionId` → 继承 P3 路径） | `POST /s3/{b}?delete` → 200，`<Deleted>` 2 项 |
| P5 | `POST /s3/{b}/{k}?uploads` | `DELETE /s3/{b}/{k}?uploadId=…` → 204 |

断言（每路径，独立 DSN 上事件类型计数无歧义）：`count(event_outbox WHERE event_type='vault.file.deleted@1.1') == 1`（P1/P3 另以 SQL 查得 `origin_id` 断言归属；P2 origin_id=marker 行 id、P5 origin_id=0——复用 `outboxCount`，originID 传 0 即可）；行 payload 解码断言 `schema_version=="1.1"`、`event_type`、`tenant/bucket/key/version_id/actor/request_id/etag/size` 字段存在且类型正确（AC-3 schema）。

负例钉：
- a) 不存在的 key 单删 → 204 no-op → **0 行**（`TestS3Delete_NoOpKeyWritesZeroRows`）
- b) 鉴权拒绝 → **0 行**（既有 `TestDeniedDeleteWritesNoOutboxRows` authz_gate_test.go:324 保持绿）
- c) P5 幂等重放：同一 uploadId 二次 abort → 仍 204 → **仍恰 1 行**（`TestS3Abort_ReplayWritesNoSecondFact`）

### AC-2 投递：删除响应先于 relay + 重启模拟排空 pending→delivered

`internal/api/s3compat/delete_outbox_test.go`：

```go
func TestS3Delete_DurableAsyncAndRestartDelivery(t *testing.T) {
    // 1) newAuthzServer 不启动 relay（构造上不可能同步 flush）→ P2（或 P3）DELETE
    //    → 断言 204 已返回（信号式判别，镜像 fullserver_test.go:685 模式）
    // 2) 删除后（relay 未启动）：event_outbox 行 status=='pending'；
    //    audit_log 行已存在（L0 不受投递影响）
    // 3) "重启"：同一 repo 上 events.NewEventOutboxRelay(repo, logger, opts)
    //    opts.AuditSink = 测试 sink（实现 AuditSink.DeliverDeleted，httptest 目标）
    //    → 轮询至 status=='delivered'；httptest 收到恰 1 次 POST，body == 行 payload 原样
    // 4) 再次 ClaimEventOutbox → 该行不再出现（complete 后恰好一次）
}
```

### AC-3 事件 schema：加性字段 + delete-marker 可区分 + golden 恒等

`internal/events/schema_test.go`（扩展）：
- 既有 `TestEventSchema_GoldenJSON`（:31）/ `TestEventSchema_Deleted11Envelope`（:96）**不改动、原样通过**（新字段 omitempty + 结构体末尾 → 字节恒等，C-4）。
- 新增 `TestEventSchema_DeleteMarkerDistinguishable`：
  1. `BuildDeleteMarkerFact(goldenObject(), …)` 输出含 `"delete_marker":true`，其余字段与普通载荷一致；新 golden 常量钉死字节。
  2. `BuildUploadDeletedFact(upload, …)` 输出含 `"origin":"multipart_upload"`、`"size":0`、`"etag":""`、`"object_id":0`、`version_id==upload.VersionID`、`schema_version=="1.1"`；新 golden 常量钉死。
- 组合断言（s3compat 层，AC-1 表内）：P2 与 P3-删-marker-版本两条路径的 outbox 行 payload 含 `delete_marker:true`；P1 普通删 payload 无该字段。

### AC-4 组合 e2e：REST 与 S3 同形事实、同一投递面、bus drop 不受影响

`internal/integration/fullserver_test.go`（扩展，镜像 `TestComposition_DeleteDeliversBothFacts` :867）：

```go
func TestComposition_RestAdminDeleteAndS3DeleteSameShape(t *testing.T) {
    // 1) 同一对象：REST DELETE /v1/files/{k}?hard=1 vs S3 DELETE /s3/{b}/{k}
    //    （固定 actor；request_id 各异 → 结构化比较忽略该字段）
    //    → 两条 deleted@1.1 载荷同 schema 同字段值
    // 2) 两路径各产生 notify@1.1 → relay 向 notification 目标（httptest）POST 恰 1 次/路径，
    //    records 形状一致：eventName=="s3:ObjectRemoved:Delete"、bucket/key/versionId/sequencer
    // 3) 遗留面：两路径 object_events 各恰 1 行（InsertEvent 仍调）；happy path 下
    //    IncEventDropped 不触发（telemetry 计数器断言）；既有订阅者行为不变
    // 4) P5 abort：无 notify@1.1、无遗留事件（静默面维持），但 deleted@1.1 照常投递
}
```

### AC-5 既有行为不回归

- `go test ./internal/api/s3compat/ ./internal/service/ ./internal/repository/ ./internal/events/ ./internal/integration/` 全绿；`make check` 全绿（gofmt/build/vet）。
- 断言面：`InsertDeleteMarker`/`DeleteObjectVersion`/`DeleteUpload` 无事件变体保留且签名不变（UPLOAD_GC `DeleteUploadCascade` :138 等其余调用方零改动）；relay claim/retry/complete/prune 零改动（`TestEventOutboxClaimLeaseExpiryRedelivers` 等保持绿）；`notifier.go` D2 去重逻辑不变（P2/P5 不产生 notify 事实，不触碰该路径）；`Bus.Publish` 签名与吞错语义不变；鉴权门（WIP `authorizeDelete`）与 403 语义不变（AC-1 负例 b 钉死）。

---

## 7. Decisions taken where the spec left freedom

| # | 决策 | 理由 / 被否选项 |
|---|------|----------------|
| D-1 | P2 事实**预构建**（payload `object_id:0`），repo 在事务内回填 `facts[i].OriginID=marker.ID`；`marker.VersionID` 由 service 预生成（`repository.NewVersionID()` 导出） | 与 `HardDeleteObjectWithEvent` 签名族一致（spec 建议签名原样）；被否：向 repo 传 `func(Object) []OutboxFact` 闭包（可得完整 object_id，但引入代码库无先例的回调签名，且 P5 本就文档化 `object_id:0`——"事实来源行在构建期不存在"是统一语义） |
| D-2 | P3 增加 `DELETE … RowsAffected==0 → ErrNotFound` 守卫；validate 移入事务内 | 并发双删防幽灵 audit/facts（现状 SELECT 守卫在 Postgres 下有竞态）；形状先例 `SoftDeleteObjectByIDWithEvent`（D-2 注释） |
| D-3 | audit detail 词汇扩展：P2 `"delete_marker"`、P5 `"multipart_abort"`、P3 复用 `"hard"`；抽取 `auditEntryFor(ctx, bucket, key, tenant, detail)` 共享核心 | `audit_log.detail` 是自由文本列，无 schema 变更；`deleteAuditEntry` 签名不动（REST 侧零漂移） |
| D-4 | P1 `deleteFacts` **零改动**（不做 marker-aware 化） | REST/P1 载荷字节级不变（C-4/AC-4）；marker 标志只出现在 P2 与 P3-删-marker 路径（spec FR-5 原意） |
| D-5 | P3 事实含 notify@1.1（`versionDeleteFacts` 镜像 `deleteFacts`）；P2/P5 只有 deleted@1.1 | spec §6 边界原样（通知词汇表无 `DeleteMarkerCreated`；abort 无成品对象）；P3 被删版本行有完整 size/etag → notify 载荷有效 |
| D-6 | P5 零行（`ErrUploadNotFound`）→ service 返回 nil、**保留** idempotency 缓存；其他失败沿用 `DeleteIdempotencyKey` 释放 | 零行 = abort 已终态（GC 抢先）→ 204 no-op 且缓存防重放；释放缓存会导致重试循环（load 失败→释放→重试），收敛更差 |
| D-7 | P5 事实 `OriginID=0` + `OriginKind=upload`，而非用 `multipart_uploads.id` | `origin_id` 语义是 objects.id；复用 upload.id 会与对象 id 空间碰撞（`HasEventOutboxFact`/AC-1 计数按 origin_id 查询） |
| D-8 | upload kind 强制 `EventType==deleted@1.1`（fail-closed） | upload-origin notify 无意义；防未来误用 |
| D-9 | AC-1 每路径独立 server/DSN，事件类型计数无歧义；P5 计数复用 `outboxCount`（originID=0） | 既有 harness 零改动 |

---

## 8. Out of scope（spec §5，不变）

`repository.Event`/`object_events` 加 version · marker 创建（P2）与 abort（P5）产生 notify@1.1 · abort 产生遗留 `s.emit` · relay/claim/complete/retry/prune 改动 · L2 AuditSink 协议 · `?tagging`/bucket 删除/`?restore`/WebDAV/REST 删除路径（REST 侧回归钉在 AC-4）· 幂等键/abort 流程重构 · actor 身份管线。
