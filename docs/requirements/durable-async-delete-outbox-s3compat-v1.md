# 方向：单一 versioned-delete 契约 —— 所有 S3 删除路径（含 multipart abort）统一汇入 `vault.file.deleted@1.1` 事务性 outbox

> **模块：** `internal/api/s3compat`（组合面：`internal/service` + `internal/repository` + `internal/events`）· **来源分析：** `docs/auto/analyses/internal-api-s3compat-eeefa063.json`（方向 1）· **日期：** 2026-08-06
> **评分：** 价值 9 / 风险降低 9 / 工作量 7 / 置信度 8
> **验证基准：** 工作树 = HEAD `acfaaf4` + 未提交 WIP（fail-closed/authz 批次，`extra.go` 的 `authorizeDelete` 每键门控等）。本文所有引用均已对照该基准逐行验证；`go build ./...`、`gofmt -l`、`go vet ./...` 全绿，outbox 相关测试（`internal/repository`、`internal/events`、`internal/integration`）全绿（§2.3）。
>
> **本文是增量规格：** 方向 acceptance 要求的机制核心（事务性 outbox、claim/retry/complete relay、@1.1 自包含 schema、durable_async 不阻塞、删除+审计+事实单事务）**已由前序 campaign 落地**（`internal/repository/event_outbox.go`、`internal/events/event_outbox_relay.go`、`internal/events/payload.go`，提交 `fb74b19`/`4cca6db`，详见 `docs/requirements/durable-async-delete-outbox-rest-v1.md`）。本文的职责 = ①逐条验证方向文 11 条引用并修正已过时主张；②把"所有 S3 删除路径（单删 / `?versionId` / delete-marker / 批量 / multipart abort）统一汇入 @1.1 outbox"这一**真实缺口**规格化（FR-1…FR-4）；③把 4 条 acceptance 映射为可执行测试（AC-1…AC-4）。**不是绿地设计。**

---

## 1. 问题陈述（方向文 vs 仓库现状）

方向文写于分析快照（`internal-api-s3compat-eeefa063.json`），其问题描述的核心主张需分两类核对：

| 方向文主张 | 现状（已验证） |
|-----------|---------------|
| "multipart abort（extra.go:379 → `service.AbortMultipartFor`）不发任何生命周期事件（`file_multipart.go` 无 `s.emit`）" | ✅ **仍成立**：`AbortMultipartFor`（file_multipart.go:263）只做 storage abort + 幂等键 + `DeleteUpload`（sql_uploads.go:45，单语句无事务），全文件无 `s.emit`（grep 验证）；**且无 outbox 事实**——最宽删除面上的最静默路径 |
| "delete-marker 创建对 marker 行本身发 `EventDeleted`（delete_marker.go:58）" | ✅ **仍成立**（:58），但**只发遗留事件**：`InsertDeleteMarker`（sql_objects_versions.go:10）事务内**不写** audit/outbox 事实 |
| "batch `?delete` 在每次元数据变更后逐对象发事件" | ✅ **仍成立**：`deleteObjects`（extra.go:430）逐对象调 `deleteS3Object`，事件在 service 内逐对象发出 |
| "`repository.Event`（repository.go:175）是扁平、无版本的结构体" | ✅ **仍成立**（:175-185，无 version 字段）——版本化已落在 outbox 载荷（`schema_version:"1.1"`），`Event` 结构体不动 |
| "**不存在** `vault.file.deleted@1.1` schema（internal/ 中无 'vault.file.' 字符串）" | ❌ **已过时**：`event_outbox.go:22-25` 定义 `EventTypeFileDeleted11="vault.file.deleted@1.1"` / `EventTypeFileNotify11`；`payload.go:105/132` 的 `BuildDeletedFact`/`BuildNotifyFact` 已实现并 golden 钉死（schema_test.go:31）；relay 已常开（workers.go:158） |
| "`Publish` 在元数据事务提交后执行，错误只记日志不传播（bus.go），崩溃窗口丢事件——非事务性 outbox" | ✅ **对遗留 `object_events` 流仍成立**（bus.go:80 注释原文），**但权威持久路径已转移**：`HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent`（event_outbox.go:102/147）把元数据删除 + `audit_log` 行 + outbox 事实放入**同一事务**；`s.emit` 退化为本地广播（SSE/indexer/webhook 等，语义不变） |

**仍然真实的问题残余（本方向规格的对象）：** S3 适配器是**最宽的删除面**（单删 / `?versionId` / delete-marker / 批量 `?delete` / multipart abort 五条路径），但 outbox 契约只覆盖了其中一条：

| # | S3 删除路径 | 适配器入口 | service 路径 | outbox 事实 | audit 行 |
|---|------------|-----------|-------------|------------|---------|
| P1 | 单删（非 versioned 桶） | `DeleteObject` → `deleteS3Object` → `svc.Delete(hard=true)` | `hardDeleteObject`（file_delete.go:53） | ✅ deleted@1.1 + notify@1.1 同事务 | ✅ |
| P2 | 单删（versioned 桶 → delete-marker 创建） | 同上 | `CreateDeleteMarker`（delete_marker.go:58） | ❌ 无 | ❌ |
| P3 | `DELETE ?versionId=` | 同上（versionID 非空分支） | `DeleteVersion`（file_delete.go:174） | ❌ 无 | ❌ |
| P4 | 批量 `?delete` | `deleteObjects`（extra.go:430） | 逐对象 P1/P2/P3 | 继承 P1✅ / P2,P3❌ | 继承 |
| P5 | multipart abort（`?uploadId`） | `abortMultipartUpload`（extra.go:379） | `AbortMultipartFor`（file_multipart.go:263） | ❌ 无（连遗留事件都没有） | ❌ |

即：**方向文的"proposed fix"（@1.1 载荷一次构建、与元数据删除同事务插入 outbox 行、异步 dispatcher 排空、适配器保持 thin）在 P1 已落地，P2/P3/P5 仍是真空**。本规格把该契约推广为**单一 versioned-delete 契约**：每条 S3 删除路径对"被移除实体"产生**恰好一条** `vault.file.deleted@1.1` outbox 事实，与元数据变更同事务提交，遗留广播行为不变。

### 触发场景（真实工作流）

1. 客户对 versioned 桶 `DELETE /s3/b/k`（无 versionId）→ 今天只产生一条**遗留** `EventDeleted`（marker 行），审计与 outbox 全无；合规查询 `ListAudit` 查不到该删除。
2. `DELETE /s3/b/k?versionId=v3` → 版本被永久删除，但**零持久事件**；`object_events` 只有遗留行（可被 SSE 回放，但不可恢复投递）。
3. 上传中止 `DELETE /s3/b/k?uploadId=…` → **完全静默**：无事件、无审计、无 outbox；COMPOSE-2026-017 的 durable_async 义务在 S3 面上缺失。

---

## 2. 现状与代码证据（方向文 11 条引用逐条验证）

### 2.1 验证表

| # | 方向文引用 | 验证结果 |
|---|-----------|---------|
| E1 | `s3compat/delete.go:11` — `deleteS3Object`（version-aware 删除语义 + 返回 versionID/是否 marker） | ✅ 精确 :11-34；三分支：`versionID≠"" → DeleteVersion`；versioned 桶 → `CreateDeleteMarker`；否则 → `svc.Delete(hard=true)`。**适配器已天然是漏斗**，无需新增入口 |
| E2 | `s3compat/extra.go:430` — `deleteObjects`（批量） | ✅ 精确 :430；逐对象 `authorizeDelete`（WIP）+ `deleteS3Object`，事件在 service 内逐对象发出 |
| E3 | `s3compat/extra.go:379` — `abortMultipartUpload` | ✅ 精确 :379；`AbortMultipartFor` 失败仅 `ErrUploadNotFound` 吞掉 → 204；**无任何事件** |
| E4 | `s3compat/handler.go:264` — `DeleteObject` | ✅ 实际 :265（漂移 +1）；`?tagging` → 仅元数据删除（**不在本方向范围**）；`?uploadId` → abort；否则 `deleteS3Object` → 204 + `x-amz-version-id`/`x-amz-delete-marker` |
| E5 | `service/file.go:297` — `emit`（最小 payload） | ✅ 精确 :297-318；`EventDeleted` 遗留载荷（backend/size/etag/content_type），`sink.Publish` 吞错——**遗留广播面** |
| E6 | `service/file_delete.go:96/123` — Delete/DeleteVersion | ⚠️ 行号漂移：实际 `Delete` :147、`DeleteVersion` :174；:123 是 `deleteFacts`（两条 @1.1 事实构建）、:96 是 `softDeleteObject` 体。**语义主张成立**：`Delete` 走 WithEvent 事务（✅ facts+audit），`DeleteVersion`（:174-215）调 `repo.DeleteObjectVersion`（**无 facts/audit**）+ 遗留 emit（:212，另对 promoted 版本发 `EventCreated` :214-215） |
| E7 | `service/delete_marker.go:58` — marker 创建发 `EventDeleted` | ✅ 精确 :58；`InsertDeleteMarker`（sql_objects_versions.go:10-71）事务内无 audit/facts；`IsDeleteMarker` 依据 `Metadata["_aero_delete_marker"]=="true"`（delete_marker.go:76-78） |
| E8 | `service/file_multipart.go:263` — 无 emit（已验证） | ✅ 精确 :263（`AbortMultipartFor`）；`grep -n "s\.emit" file_multipart.go` 无命中（exit 1）。幂等流程：`ClaimIdempotencyKey` → storage abort → `CompleteIdempotencyKey` → `DeleteUpload`（失败则 `DeleteIdempotencyKey` 允许重试，:306-309） |
| E9 | `repository/repository.go:175-195` — `Event` 扁平无版本 | ✅ :175-185；`EventType` 仅 created/updated/deleted/accessed（:190-194）。**方向文"@1.1 加到 envelope"已在 outbox 载荷落地**（§2.3），本方向不改 `Event` |
| E10 | `events/bus.go` — `Publish` 从不传播错误 | ✅ :80 注释原文 *"Errors are logged but never propagated — lifecycle events must not break user requests"*；`InsertEvent`（sql_events.go:12-38）在业务事务外——遗留流语义不变 |
| E11 | `repository/sql_events.go` | ✅ `InsertEvent`/`NextUnconsumedEvents`/`ListEventsAfter` 均读 `object_events`；outbox 是**独立表**（`event_outbox`，迁移 0041），互不干扰 |

### 2.2 现状基础设施（前序 campaign 已落地，本方向复用）

| 组件 | 位置 | 验证 |
|------|------|------|
| outbox 事实类型 | `event_outbox.go:22-25` `EventTypeFileDeleted11`/`EventTypeFileNotify11`；:61-73 `validateOutboxFacts`（**OriginID≤0 拒绝**——P5 需放宽，见 FR-4） | ✅ |
| 原子删除+事实 | `HardDeleteObjectWithEvent` :102（legal-hold 检查 + access-state 清理 + DELETE + audit + facts 单事务，零行删除回滚 ErrNotFound）；`SoftDeleteObjectWithEvent` :147 | ✅ |
| 事实载荷构建 | `payload.go:105` `BuildDeletedFact(obj, actor, requestID, tenant, reason…)` → `deletedFact`（schema_version/event_type/tenant/bucket/key/object_id/version_id/size/etag/backend/request_id/actor/reason）；:132 `BuildNotifyFact` | ✅ |
| 异步 dispatcher | `event_outbox_relay.go:111` `Run` → `deliverBatch` :140（claim 栅栏 owner+token+lease）→ `deliverFact` :171 按类型分发 → `deliverDeleted` :190（L2 AuditSink 或 complete）/ `deliverNotify` :236（按 bucket notification 规则 POST `s3:ObjectRemoved:Delete`）→ complete/retry（退避+jitter）/prune :362 | ✅ 常开（cmd/server/workers.go:158） |
| 服务层事实构建 | `file_delete.go:123-141` `deleteFacts`：deleted@1.1 + notify@1.1，actor 取 `access.PrincipalFrom(ctx)`、request_id 取中间件值 | ✅ |
| 测试基建 | `event_outbox_test.go`（`TestDeleteObjectWithAudit_OneTx` :71、`TestEventOutboxClaimLeaseExpiryRedelivers` :259）；`schema_test.go`（golden :31、`TestEventSchema_Deleted11Envelope` :96）；`fullserver_test.go`（`TestDeleteResponse_DoesNotBlockOnDelivery` :685、`TestComposition_DeleteDeliversBothFacts` :867）；**s3compat 自有 harness**：`authz_gate_test.go` `newAuthzServer` :68（repo+DSN 返回，可裸 SQL 数 outbox 行）、`outboxCount` :146 | ✅ |

### 2.3 实测（本规格验证时运行）

`go build ./...` 退出码 0；`gofmt -l` 无输出；`go test ./internal/repository/ -run 'TestDeleteObjectWithAudit_OneTx|TestEventOutbox'`、`go test ./internal/events/ -run 'Outbox|EventSchema'`、`go test ./internal/integration/ -run 'TestDeleteResponse_DoesNotBlockOnDelivery'` 全部 `ok`。

---

## 3. 需求规格

### FR-1：单一 versioned-delete 契约（所有 S3 删除路径 → 恰好一条 deleted@1.1 事实，同事务）

**契约定义（每条 S3 删除路径必须满足，P1 已满足、P2/P3/P5 补齐）：**

- **C-1 每路径一条事实：** 对每条实际发生删除的 S3 路径（P1 单删 / P2 marker 创建 / P3 `?versionId` / P4 批量逐对象 / P5 abort），`event_outbox` 表中 `event_type='vault.file.deleted@1.1'` 的行**恰好 1 条**（P5 是唯一 outbox 行；P1/P3 另有 notify@1.1 行属不同 schema，不计入）。不存在的 key 删除（S3 语义 204 no-op）与鉴权拒绝：**0 条**（authz_gate_test.go:354 已有拒绝侧断言先例，本方向补 no-op 侧）。
- **C-2 同事务原子：** outbox 行与元数据变更（`DELETE FROM objects` / marker `INSERT` / `DELETE FROM objects WHERE version_id=…` / `DELETE FROM multipart_uploads`）**同一事务**提交；事务内任一失败（含事实校验失败、零行变更）→ 整体回滚（P1 的 GAP-4 零行回滚守卫形状，`event_outbox.go:102-141`，推广到 P2/P3/P5 新方法）。
- **C-3 单一构建器：** 事实载荷只由 `events.BuildDeletedFact` 一族构建（P2/P3 复用 `deleteFacts` 形状；P5 用 upload 形状，FR-4）。**适配器零改动**：`deleteS3Object` 已是唯一漏斗（E1），所有补齐在 service/repository 层，handler 保持 thin。
- **C-4 遗留面不变：** 每条路径既有的 `s.emit`（遗留 `object_events`/SSE/webhook/Indexer/AV/Replication 广播）**原样保留**（P1 :53、P2 :58、P3 :212 + promoted `EventCreated` :214-215、P5 保持静默）；outbox 是**加性**路径，不改变 `Bus.Publish` 语义与 `IncEventDropped` 计数（AC-4）。
- **C-5 audit 行：** P2/P3/P5 的删除同样写入 `audit_log` 行（L0 always-on，与 P1 的 `HardDeleteObjectWithEvent` 形状一致；`deleteAuditEntry` file_delete.go:100-117 复用，`Action=AuditActionFileDelete`，actor 允许空串）。

### FR-2：delete-marker 创建（P2）→ `InsertDeleteMarkerWithEvent`

- 新增 repository 方法（签名建议，实现侧可等价变形）：`InsertDeleteMarkerWithEvent(ctx, obj Object, entry AuditEntry, facts []OutboxFact) (Object, error)`——事务 = 现有 `InsertDeleteMarker` 全流程（tombstone UPDATE + access-state 清理 + marker INSERT RETURNING id，sql_objects_versions.go:10-71）**+ `insertAuditEntry` + `insertOutboxFacts`**；返回 marker 行（含 ID），供 service 层继续遗留 `s.emit`。
- `service.CreateDeleteMarker` 改用新方法；facts 由 `deleteFacts` 形状构建但**只含 deleted@1.1**（不产生 notify@1.1——见 §6 边界），且必须带 marker 标志（FR-5）。
- 零行/失败回滚语义与 P1 一致。

### FR-3：`?versionId` 删除（P3）→ `DeleteObjectVersionWithEvent`

- 新增 repository 方法（建议）：`DeleteObjectVersionWithEvent(ctx, tenant, bucket, key, versionID string, entry AuditEntry, facts []OutboxFact) error`——事务 = 现有 `DeleteObjectVersion` 全流程（SELECT id/current + legal-hold 检查 + access-state 清理（current 时）+ DELETE version + promote + 空桶清理，sql_objects_maint.go:143-190）**+ audit + facts**。
- `service.DeleteVersion` 改用新方法；facts = deleted@1.1 **+ notify@1.1**（镜像 `deleteFacts`：被删版本行有完整 size/etag/version_id，notify 载荷有效；AWS 对版本删除发 `s3:ObjectRemoved:Delete` 语义一致）。被删版本若是 marker（`DELETE ?versionId=<marker>`）→ deleted@1.1 带 marker 标志（FR-5）。
- 遗留行为原样：`s.emit(obj, EventDeleted)` 与 promoted 版本的 `s.emit(EventCreated)`（:212-215）不动。

### FR-4：multipart abort（P5）→ `DeleteUploadWithEvent` + upload-origin 事实规则

- **新增 repository 方法（建议）：** `DeleteUploadWithEvent(ctx, uploadID string, entry AuditEntry, facts []OutboxFact) error`——事务 = `DELETE FROM multipart_uploads WHERE upload_id=$1`（RowsAffected==0 → 回滚 + 错误，杜绝幽灵事实）+ audit + facts；替代现 `DeleteUpload`（sql_uploads.go:45，单语句无事务）在本路径的调用。
- **upload-origin 事实规则（唯一需要放宽的 outbox 校验）：** multipart upload 无 `objects` 行，`OutboxFact.OriginID`（int64 objects.id）不可用。新增 `OutboxFact.OriginKind` 字段（`OutboxOriginKind`；零值="object" 保持现状，新常量 `OriginKindUpload`），`validateOutboxFacts`（event_outbox.go:61-79）放宽为：`OriginKind=="upload"` 时 `OriginID` 可为 0（tenant 与 payload 校验不变）；其余规则不变。**OriginKind 不持久化**（enqueue 时断言；relay 的 claim/complete/retry/prune 只读行 id 与 payload，零改动，无迁移）。
- **abort 事实形状：** 恰 1 条 deleted@1.1（**不产生 notify@1.1**，见 §6 边界），payload：`tenant/bucket/key` 取 upload 行，`version_id` 取 `Upload.VersionID`（file_multipart.go:87 创建时已生成），`size:0`、`etag:""`（upload 无成品对象），`object_id:0`（无 objects 行，文档化），**新增 `origin:"multipart_upload"` 载荷字段**（omitempty，加性）使 abort 事实可与对象删除区分（AC-3 语义的对称要求）。
- **幂等性：** 现有 abort 幂等键流程不变（ClaimIdempotencyKey → storage abort → `CompleteIdempotencyKey` → `DeleteUploadWithEvent`；失败 → `DeleteIdempotencyKey` 允许重试，file_multipart.go:306-309 保留）；重放/重试调用**不产生第二条事实**（C-1 的"恰好一条"含幂等重放断言，AC-1 ⑤）。storage abort 仍在事务外先行（与 P1 的 blob 删除同构）。
- `service.AbortMultipartFor` 在 claimed 分支调用新方法；`ErrUploadNotFound` 语义与 204 响应不变（extra.go:383-385）。

### FR-5：@1.1 载荷 —— delete-marker 可区分（加性字段，golden 不变）

- `deletedFact`（payload.go:33-46）新增 `DeleteMarker bool \`json:"delete_marker,omitempty"\``：普通对象删除**不出现该字段**（omitempty → 既有 REST/S3 golden 字节恒等，schema_test.go:31 的 `goldenDeletedFact` 不变）；marker 相关删除（P2 marker 创建、P3 删 marker 版本）出现 `"delete_marker":true`。
- 构建方式（建议）：service 层按 `IsDeleteMarker(obj)`（delete_marker.go:76-78）选择——`BuildDeletedFact` 或新增 `BuildDeleteMarkerFact`（同一结构体，仅置位）；**不**把 `_aero_delete_marker` 常量引入 `internal/events`（避免包间耦合）。
- 必填字段集不变（`schema_version/event_type/tenant/bucket/key/object_id/version_id/size/etag/backend/request_id/actor`，schema_test.go:96 既有断言保留）；abort 事实另含 `origin:"multipart_upload"`（加性）。
- 方向文 acceptance 明列的 `{tenant,bucket,key,version_id,actor,request_id,etag,size}` 全部已在既有 schema 中（E9/G3 已由前序 campaign 补齐 `object_id`），本方向**不再增删必填字段**。

### 非功能约束

- `make check` 全绿（`gofmt -l` 无输出 · `go build ./...` · `go vet ./...` · `go test ./...`）；单文件 ≤ 500 行（新增 repository 方法建议独立文件，`event_outbox.go` 已 414 行）。
- 纯 stdlib（I6）；SQL 遵守 I1（占位符不复用，`s.rebind`）；**无新迁移**（I2——`OriginKind` 不持久化，`event_outbox` 表结构不变）；不触碰中间件链（I4）。
- 基线路径（SQLite + local FS + AI off）全绿；Postgres 方言差异沿用既有 `WithEvent` 方法形状（双方言 SQL）。
- `EventBus` drop 计数不受影响（AC-4 断言）：不新增遗留 `s.emit` 调用、不改变订阅者缓冲语义。

---

## 4. 验收标准（可测试；逐条映射方向 acceptance）

> 测试基建：s3compat 层用 `authz_gate_test.go` 的 `newAuthzServer`（:68，返回 server+repo+DSN+svc）与 `outboxCount`（:146）扩展；relay 用 `internal/events/event_outbox_relay_test.go` 既有模式（httptest 目标 + 注入 token）；e2e 镜像 `internal/integration/fullserver_test.go`。新测试文件建议 `internal/api/s3compat/delete_outbox_test.go`（≤500 行）。

### AC-1 单元：五条 S3 删除路径各产生**恰好一条** `vault.file.deleted@1.1` outbox 行（abort 包含）

```go
// internal/api/s3compat/delete_outbox_test.go（表驱动，每条路径独立 server/DSN）
// 路径与前置：
//  P1 单删（非 versioned 桶）            DELETE /s3/{b}/{k}            → 204
//  P2 单删（versioned 桶 → marker）      PUT 对象 → 开启 versioning → DELETE（无 versionId）→ 204 + x-amz-delete-marker:true
//  P3 版本删除                           上述桶再 PUT 第二版本 → DELETE ?versionId=v2 → 204
//  P4 批量（2 对象，混合：1 个普通删 + 1 个 ?versionId 删）→ POST ?delete → 200（Deleted 2 项）
//  P5 abort                              CreateMultipartUpload（?uploads）→ DELETE ?uploadId=… → 204
// 断言（每路径）：count(event_outbox WHERE event_type='vault.file.deleted@1.1' [AND origin_id=obj.ID]) == 1
//  且该行 payload：schema_version=="1.1"、event_type=="vault.file.deleted@1.1"、
//  tenant/bucket/key/version_id/actor/request_id/etag/size 字段齐备（类型正确，AC-3 schema）
//  P1 是既有行为的回归钉（现即满足）；P2/P3/P5 为新增
// 负例钉：a) 不存在的 key 单删 → 204 no-op → 0 行；b) 鉴权拒绝（既有 authz_gate_test.go:354 先例）→ 0 行；
//        c) P5 幂等重放（同一 uploadId 再 abort，仍 204）→ 仍恰 1 行（不翻倍）
```

### AC-2 outbox 投递：崩溃恢复（元数据删除后、dispatcher 排空前）+ 删除响应永不等待

```go
// internal/api/s3compat/delete_outbox_test.go + internal/events 既有 relay 基建
func TestS3Delete_DurableAsyncAndRestartDelivery(t *testing.T) {
	// 1) 启动 server（无 relay）→ S3 DELETE（P2 或 P3）→ 断言响应 204 已返回
	//    （信号式：响应返回时 relay 尚不存在——同步 flush 在构造上不可能；
	//    镜像 fullserver_test.go:685 的判别模式，可另挂阻塞 L2 目标做 4s<5s 判别）
	// 2) 删除后（relay 未启动）：event_outbox 行存在且 status=='pending'
	//    （audit_log 行已存在——L0 不受投递影响）
	// 3) "重启"模拟：对同一 DSN 启动 events.NewEventOutboxRelay（配 AuditSink
	//    httptest 目标或 bucket notification 规则，precedent TestOutboxRelay_DeliveryLifecycle）
	//    → 轮询至 status=='delivered'；L2/通知目标收到恰 1 次 POST，载荷 == 行 payload 原样
	// 4) 期间无第二次删除；投递完成后重 claim → 无行（恰好一次，complete 后）
}
```

### AC-3 事件 schema：@1.1 载荷字段校验 + delete-marker 事件可区分

```go
// internal/events/schema_test.go（扩展）
func TestEventSchema_DeleteMarkerDistinguishable(t *testing.T) {
	// 1) 既有 TestEventSchema_Deleted11Envelope（:96）的必填字段断言保留：
	//    tenant/bucket/key/version_id/actor/request_id/etag/size/object_id 等全存在且类型正确
	// 2) 普通删除：BuildDeletedFact(goldenObject(), …) == goldenDeletedFact（字节恒等，
	//    无 delete_marker 字段——加性 omitempty 不扰动既有 golden）
	// 3) marker 删除：BuildDeleteMarkerFact（或等价）输出含 "delete_marker":true，
	//    其余字段与普通载荷一致（新 golden 常量钉死字节）
	// 4) abort 事实：payload 含 "origin":"multipart_upload"，size==0、etag==""，
	//    version_id==upload 的 version_id；schema_version=="1.1" 校验通过
}
// 组合断言（s3compat 层）：P2 与"P3 删 marker 版本"两条路径的 outbox 行 payload 均含
// delete_marker:true；P1 普通删的 payload 无该字段
```

### AC-4 组合 e2e：REST 硬删与 S3 删除同形事件、经同一投递面消费、bus drop 计数不受影响

```go
// internal/integration/fullserver_test.go（扩展，镜像 TestComposition_DeleteDeliversBothFacts :867）
func TestComposition_RestAdminDeleteAndS3DeleteSameShape(t *testing.T) {
	// 1) 同一对象：REST DELETE /v1/files/{k}?hard=1 与 S3 DELETE /s3/{b}/{k}
	//    （固定 actor 注入；request_id 各异 → 结构化比较忽略 request_id/actor 或
	//    规范化后比对）→ 两条 deleted@1.1 载荷形状一致（同 schema 同字段值）
	// 2) 两路径各产生 notify@1.1 事实 → relay（L1 协议面 = bucket notification 投递，
	//    deliverNotify :236）向 notification 目标（httptest）POST 恰 1 次/路径，
	//    records 形状一致：eventName=="s3:ObjectRemoved:Delete"、bucket/key/versionId/sequencer
	// 3) EventBus drop 计数不受影响：两路径各遗留 object_events 行恰 1 条（InsertEvent 仍调）；
	//    happy path 下 IncEventDropped 不触发（telemetry 计数器断言，metrics_test.go 先例）；
	//    既有 bus 订阅者（SSE/webhook/Indexer/AV/Replication）行为不变
	// 4) P5 abort 不产生 notify@1.1 与遗留事件（静默面维持），但 deleted@1.1 照常投递
}
```

### AC-5 既有行为不回归

- `go test ./internal/api/s3compat/ ./internal/service/ ./internal/repository/ ./internal/events/ ./internal/integration/` 全绿；`make check` 全绿。
- `Bus.Publish` 签名与吞错语义不变；`object_events`/SSE 回放路径不变；`InsertDeleteMarker`/`DeleteObjectVersion`/`DeleteUpload`（无事件变体）若保留则签名不变（其余调用方如 reconcile/UPLOAD_GC 不受影响）；relay claim/retry/complete/prune 零改动；`notifier.go:74` 的 `HasEventOutboxFact` 去重逻辑不变（P2/P5 不产生 notify 事实，不会触碰该路径）。
- 鉴权门（WIP `authorizeDelete`）与 403 语义不变——AC-1 负例 b 钉死"拒绝 → 0 行"。

---

## 5. 范围边界（明确不做）

| 不做 | 理由 |
|------|------|
| `repository.Event` 结构体/`object_events` 表加 version | 版本化已在 outbox 载荷（`schema_version:"1.1"`）；遗留流保持兼容（E9） |
| marker 创建（P2）与 abort（P5）产生 notify@1.1 事实 | AWS 语义分别是 `DeleteMarkerCreated` 与 `ObjectRemoved:Delete`，通知词汇表目前只有 `s3:ObjectRemoved:Delete`；扩词汇属独立方向。P2/P5 的 deleted@1.1 已满足 acceptance（"abort included"指 deleted@1.1 行） |
| abort 产生遗留 `s.emit`（SSE/webhook 等） | 方向文问题描述以"无事件"为缺陷，修复 = outbox 事实；给遗留流加事件会触发 webhook/SSE 对不存在对象的副作用，属行为扩张 |
| relay / claim / complete / retry / prune 改动 | 本方向事实进入既有 outbox 即自动获得 durable_async 投递；`OriginKind` 不持久化 → 无迁移（I2）、无 relay 改动 |
| L2 AuditSink（audit-sink campaign 的端口面） | 前序方向已落地（`deliverDeleted` :190）；S3 路径事实入 outbox 后自动继承 L2 投递，本方向不重复规格 |
| `?tagging` 删除、bucket 删除、`?restore`、WebDAV/REST 删除路径 | 非对象删除语义或已由前序 campaign 覆盖（REST 侧回归钉在 AC-4）；本方向锚定 s3compat 五条对象删除路径 |
| 幂等键/abort 流程重构、`CompleteIdempotencyKey` 时序改动 | 现状顺序（storage abort → 幂等完成 → upload 删除）已支持失败重试；只替换最后一步为事务方法 |
| actor 身份管线 | actor 取 `access.PrincipalFrom(ctx)`，空值合法（既有决定，deleteFacts :125-127） |

---

## 6. 实现指引（供验收后落地，非本规格交付物）

- **Repository**（建议新文件 `internal/repository/event_outbox_objects.go` + 扩展 `event_outbox.go` 的 `validateOutboxFacts`，各 ≤500 行）：`InsertDeleteMarkerWithEvent`（FR-2）/`DeleteObjectVersionWithEvent`（FR-3）/`DeleteUploadWithEvent`（FR-4）——形状镜像 `HardDeleteObjectWithEvent`（BeginTx → 业务变更 + 零行守卫 → `insertAuditEntry` → `insertOutboxFacts` → Commit）；`OutboxFact` 增 `OriginKind` 字段 + 校验放宽（upload 允许 OriginID==0）；接口同步 `repository_interface.go`。
- **Service**：`CreateDeleteMarker`/`DeleteVersion`/`AbortMultipartFor` 改用新方法；`deleteFacts` 旁新增 marker 分支（`IsDeleteMarker(obj)` → `BuildDeleteMarkerFact`）；`AbortMultipartFor` claimed 分支构建 upload 形状事实（`BuildDeletedFact` + `origin:"multipart_upload"` 或等价变体）。
- **Payload**（`internal/events/payload.go`）：`deletedFact` 增 `DeleteMarker bool \`json:"delete_marker,omitempty"\`` 与 `Origin string \`json:"origin,omitempty"\``；新增 `BuildDeleteMarkerFact`（或变体参数）；`schema_test.go` 增 golden（AC-3）。
- **测试**：AC-1/AC-2 新 `internal/api/s3compat/delete_outbox_test.go`（复用 `newAuthzServer`/`outboxCount`；`outboxCount` 需支持 originID=0 的 upload 计数或按类型计数）；AC-3 扩展 `schema_test.go`；AC-4 扩展 `internal/integration/fullserver_test.go`。
