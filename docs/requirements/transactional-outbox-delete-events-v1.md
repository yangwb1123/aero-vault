# 方向：删除事件事务性 outbox —— `vault.file.deleted@1.1` / `vault.file.notify@1.1` 与删除原子写入，异步 relay

> **模块：** `cmd/server`（装配）· **来源分析：** `docs/auto/analyses/cmd-server-7a3bfea7.json` · **日期：** 2026-08-06
> **评分：** 价值 10 / 风险降低 9 / 工作量 7 / 置信度 9
> **本文所有代码引用均已对照仓库逐行验证**（行号以当前 HEAD `fb74b19` 为准）。
>
> **修订记录（2026-08-06，验证后设计回写）：** §FR-3/§FR-4/§AC-2 按验证后设计 D1–D7 修订（权威记录见 run `DECISIONS.md` 的 requirements-amend 条目）：`deleted@1.1` 不做本地回放（D3）；dedupe 键 = outbox 行 id，废除 `UNIQUE(event_type, origin_id)`（D1/D4）；claim 谓词 status 形状（D5）；payload 双方言 TEXT + emit 侧 sequencer（D6）；deliver→complete 窗口显式 at-least-once（D7）。

---

## 1. 问题陈述

`FileService.Delete` 的删除事件**与删除本身不原子**。`hardDeleteObject`/`softDeleteObject` 先提交元数据删除（`repo.HardDeleteObject`/`SoftDeleteObject` 各自开启事务），随后 `addTenantUsage` 再开事务扣配额，最后 `s.emit()` → `Bus.Publish` → `repo.InsertEvent` 在**第三个事务**里持久化事件（`internal/service/file_delete.go:16-71,74-94`、`internal/events/bus.go:84`）。任意一次提交与下一次提交之间进程崩溃，`deleted` 事件即永久丢失——违反"事务性 outbox"要求：**业务行与事件行必须在同一事务内提交**。

同时，事件载荷存在**结构性缺口**：持久化的 `Event.Payload` 是 `map[string]string`（`internal/repository/repository.go:175-189`），无 `schema_version`，且 `emit()` 只写入 `backend/size/etag/content_type`（`internal/service/file.go:297-320`）——**`version_id` 与 `actor` 根本不进事件**；`internal/events/notifier.go` 的 `buildS3Event`（:231-258）在投递时才拼 S3 形状载荷，且只带 `key` + `sequencer`，`size/etag/versionId` 直接丢弃。没有自包含、带版本的 `notify@1.1` 状态可投递。

仓库内已有两处可泛化的先例：(a) `RecordAuditWithGovernance`/`InsertEventWithGovernance` 已实现"业务行 + outbox 事实同事务"（`internal/repository/audit_governance_write.go:14-89`）；(b) billing 运行时展示 `claim → retry(backoff+jitter) → complete` 的异步 relay 形态（`internal/billing/outbox.go`）。但两者均绑定租户/脱敏形状，非通用版本化事件 outbox。`Bus.Publish` 已满足"不阻塞业务流"（错误只记日志、不向上传播，bus.go:85-88），**只缺原子性 + schema**。

### 触发场景（真实工作流）

1. `DELETE /v1/files/doc.pdf` → `hardDeleteObject` 提交 `DELETE FROM objects`（事务 1 提交）。
2. 进程在 `addTenantUsage`（事务 2）或 `InsertEvent`（事务 3）提交前崩溃。
3. 重启后：对象已删除、配额已扣（或未扣），但 `deleted` 事件从未落库 → 通知目标（SNS/SQS/Lambda/HTTP）、SSE 回放、下游消费者**永远不知道这次删除**。

---

## 2. 现状与代码证据（已逐条验证）

| # | 证据 | 验证结果 |
|---|------|---------|
| E1 | `file_delete.go:16-71` — `hardDeleteObject`：`store.Delete` → `repo.HardDeleteObject`（**事务 A 提交**）→ `addTenantUsage`（**事务 B**）→ `s.emit`（:51，**事务 C**）；`:74-94` `softDeleteObject` 同形状（emit 在 :90） | ✅ 与引用一致（方向文引 15-53/61-81，行号轻微漂移，语义一致） |
| E2 | `bus.go:84` — `id, err := repo.InsertEvent(ctx, e)` 在业务事务**已提交之后**执行；:85-88 错误仅 `logger.Warn`，从不返回 → "不阻塞业务流"已满足 | ✅ 行号精确 |
| E3 | `sql_objects_maint.go:74-98` — `HardDeleteObject` 自带事务（legal-hold 检查 + `deleteObjectAccessState` + `DELETE FROM objects` + commit）；:108-126 `SoftDeleteObject` 自带事务（UPDATE `deleted_at` + access state + commit） | ✅ 补充验证：删除事务已含保护检查与访问状态清理，outbox 写入应并入**同一事务** |
| E4 | `audit_governance_write.go:14-43` — `RecordAuditWithGovernance`：`BeginTx`(:17) → 插 `audit_log` → 插 `audit_governance_outbox` → `Commit`；`:45-89` `InsertEventWithGovernance`：`BeginTx`(:49) → 插 `object_events` → 插 outbox → `Commit` | ✅ 与引用一致（方向文引 :16/:58，实际 :17/:49，轻微漂移）——"业务行 + outbox 事实同事务"先例成立 |
| E5 | `billing/outbox.go` — `runOutbox`/`deliverBatch`/`deliverFact`/`retryFact`：claim→deliver→complete；`billingBackoff`（指数 2×、上限 5min、jitter ±25%） | ✅ 与引用一致 |
| E6 | `repository.go:175-189` — `Event{ID, TenantID, Bucket, Key, Type, ObjectID, RequestID, Payload map[string]string, CreatedAt}`，**无 schema_version**；:190-197 `EventType` 仅 `created|updated|deleted|accessed` | ✅ 与引用一致（方向文引 180-194，实际 175-189） |
| E7 | `notifier.go:231-258` — `buildS3Event`：投递期拼 S3 载荷；`s3Object` 仅 `Key`+`Sequencer`，**`Size/ETag/VersionId` 从不下发**——比"投递期重派生"更糟：版本状态在投递时**被丢弃** | ✅ 修正性验证 |
| E8 | `file.go:297-320` — `emit()`：payload 仅 `{backend, size, etag, content_type}`，无 `version_id`/`actor`/`schema_version`；sink 错误吞掉（:314-319） | ✅ 与引用一致 |
| E9 | `repository.go:17-36` — `Object` 含 `VersionID`/`Backend`/`Size`/`ETag`/`ContentType` → deleted@1.1 所需字段在 emit 时刻**全部可得**；`access.PrincipalFrom(ctx)`（`access/context.go:11`）提供 actor；`middleware.RequestIDFrom`（`middleware/middleware.go:40`）提供 request_id（emit 已用） | ✅ 补充验证 |
| E10 | `0039_audit_governance_outbox.up.sql` — outbox 表含 `attempts/available_at_ns/claim_owner/claim_token/lease_expires_at_ns/last_error/delivered_at_ns` + `UNIQUE(origin_kind, origin_id)`；`0040_audit_governance_control.up.sql:20-27` — `audit_governance_delivered_origins` 以 `(origin_kind, origin_id)` 为主键 | ⚠️ 修正性验证（D4）：`INSERT … WHERE NOT EXISTS` 在 **enqueue 侧**（audit_governance_write.go:133-138），墓碑在 **cleanup** 写（audit_governance_cleanup.go:29-46，prune 后 gap-scan 跳过已投递 origin）——`delivered_origins` **不是投递去重**；事件 outbox 无 gap-scan，该模式**不复制** |
| E11 | `repository_interface.go:97` — `InsertEvent` 在 Repository 接口上；:30-34 四个删除方法 | ✅ 与引用一致 |
| E12 | `cmd/server/main.go:213-214` — `bus := events.NewWithBuffer(repo, …)` + `WithEventSink(bus)`；`cmd/server/workers.go:140-145` — `startNotificationWorker`：Notifier 订阅 bus 投递通知 | ✅ 补充验证（relay 装配点） |
| E13 | `handler.go:241-253` — REST `DELETE /v1/files/*key`（`?hard=1`）→ `h.svc.Delete` | ✅ 补充验证 |
| E14 | 其他 `EventDeleted` 生产者：`file_delete.go:161`（DeleteVersion）、`delete_marker.go:58`、`object_worker.go:73`（隔离/保留清除）——均为独立路径 | ✅ 补充验证（**明确不在本方向范围**，§5） |

### 缺陷机理

```
DELETE /v1/files/k → HardDeleteObject 事务提交（对象行已删）
  → 崩溃点①：addTenantUsage 事务未提交 → 配额未扣、事件未发
  → 崩溃点②：InsertEvent 事务未提交 → deleted 事件丢失 ← 🔴
重启后：对象没了，事件永远没有；通知/SSE 回放/下游消费者不知情
```

---

## 3. 需求规格

### FR-1：删除行 + outbox 事实单事务原子写入

`FileService.Delete`（硬删与软删）必须把**元数据删除与版本化事件事实在同一事务内提交**：

- 新增 Repository 方法（`internal/repository`，保持既有方法签名不变——E3 中 `HardDeleteObject`/`SoftDeleteObject` 继续服务 reconcile/quarantine 等其它调用方）：
  - `HardDeleteObjectWithEvent(ctx, tenant, bucket, key string, facts []OutboxFact) error` —— 单事务 = 现有 `HardDeleteObject` 全部逻辑（legal-hold 检查 + access state 清理 + `DELETE FROM objects`）+ 插入全部 outbox 事实；
  - `SoftDeleteObjectWithEvent(ctx, tenant, bucket, key string, facts []OutboxFact) error` —— 单事务 = `SoftDeleteObject` 逻辑 + outbox 事实；
  - `OutboxFact{EventType string; OriginID int64; TenantID string; Payload []byte}` —— payload 为自包含 JSON（FR-2）。
- 事务内任一失败（含事实校验失败、约束冲突）→ 整体回滚：**对象行不删、outbox 事实不落**（AC-1 的强制回滚断言）。
- 事实插入位于受影响行校验（`SoftDeleteObject` 的 `n==0` → `ErrNotFound`，sql_objects_maint.go:29-31）**之后、同一事务内**——并发双删竞态不产生幽灵事实（GAP-4 守卫；`HardDeleteObject` 现状无 RowsAffected 检查，WithEvent 变体应补齐，优于现状）。
- **有意的语义变更（显式化）：** WithEvent 事务提交后 `addTenantUsage` 失败 → API 返回错误，但 outbox 事实已提交、通知**仍会投递**（今日语义：usage 失败会连带抑制事件）。接受此变更，列入 relay F-模式表。
- `FileService.Delete` 在调用删除方法**之前**构建 facts（payload 所需字段全部来自已加载的 `obj`，E9），删除提交后照旧 `s.emit()`（本地广播 + 遗留 `object_events` 最佳努力写入，E2 语义不变）——**`EventSink`/`Bus.Publish` 接口零改动**。

### FR-2：版本化、自包含事件 schema（`vault.file.deleted@1.1` / `vault.file.notify@1.1`）

一次删除写入**两条事实**（同一事务，FR-1），payload 在删除时刻构建、**自包含**，携带显式 `schema_version` 字段：

- **`vault.file.deleted@1.1`** —— 生命周期事实：
  ```json
  {
    "schema_version": "1.1",
    "event_type": "vault.file.deleted@1.1",
    "tenant": "default", "bucket": "default", "key": "docs/a.txt",
    "version_id": "v-abc", "size": 42, "etag": "etag-1",
    "backend": "local", "request_id": "req-1", "actor": "alice"
  }
  ```
- **`vault.file.notify@1.1`** —— S3 通知形状（自包含，投递期**不再重派生**）：
  ```json
  {
    "schema_version": "1.1",
    "event_type": "vault.file.notify@1.1",
    "tenant": "default", "bucket": "default", "key": "docs/a.txt",
    "version_id": "v-abc", "size": 42, "etag": "etag-1",
    "backend": "local", "request_id": "req-1", "actor": "alice",
    "records": [{ "eventVersion": "2.1", "eventSource": "aws:s3",
      "eventName": "s3:ObjectRemoved:Delete",
      "s3": { "s3SchemaVersion": "1.0",
        "bucket": { "name": "default", "arn": "arn:aws:s3:::default" },
        "object": { "key": "docs/a.txt", "size": 42, "eTag": "etag-1",
          "versionId": "v-abc", "sequencer": "8f3a2c9e4b1d7f06a5c0e2d94b7a1f3e" } } }]
  }
  ```
- 必填字段（AC-3 golden 钉死）：`tenant, bucket, key, version_id, size, etag, backend, request_id, actor` + `schema_version`。`version_id` 取 `obj.VersionID`（E9）；`actor` 取 `access.PrincipalFrom(ctx)` 的 principal（未登录/无上下文时允许空串，**不新增鉴权管线**）；`request_id` 沿用 `middleware.RequestIDFrom(ctx)`。
- **sequencer（D6）：** emit 时刻由 `crypto/rand` 生成 16 字节 hex（webhook token 先例；测试注入固定值）。**不可取 `obj.ID`**——`RestoreObject` 原地 UPDATE 复用行 id（sql_objects_maint.go:247-273），restore→re-delete 会产生重复 sequencer；也不做"INSERT RETURNING id → 重建 payload → UPDATE"两阶段（把 JSON/schema 知识逼进 repository 层）。
- `internal/events/notifier.go` 投递时**消费 notify@1.1 载荷本身**（含 `records`），不再调用 `buildS3Event` 重拼（E7 修复）；`buildS3Event` 退役或仅作载荷生成器（在 emit 时刻调用一次）。

### FR-3：异步 relay —— claim → deliver → complete，带重试；complete 后恰好一次、deliver→complete 窗口 at-least-once（D7）

新增 outbox relay（形态对齐 E5 billing 运行时与 E10 审计 outbox）：

- **claim**：`ClaimEventOutbox(ctx, owner, limit, ttl)` —— status 形状谓词（D5）：仅取 `(status='pending' AND available_at_ns<=now) OR (status='inflight' AND lease_expires_at_ns<=now)` 的事实（billing_outbox.go:79-81 形状；崩溃中继的租约过期可被重新 claim，AC-2(3) 的"崩溃重投"断言）。claim 用 owner+token+lease 守卫（audit 形状，audit_governance_claim.go:102-132）；**无** enqueue 侧 `WHERE NOT EXISTS`（E10 先例误读纠正，见 E10 行/D4）。claim 可返回少于 `limit` 的行（短批次容忍，billing `deliverBatch` 先例）；排序 `ORDER BY (available_at_ns, created_at_ns, id)` 为 **best-effort，无跨事实排序保证**（重试把 `available_at_ns` 推到未来 → 旧事实可能晚于新事实；一次删除的两条事实是独立行）。
- **deliver**：按 `event_type` 分发——`vault.file.notify@1.1` → 按既有 bucket 通知规则 POST 到目标（复用 `Notifier` 的规则匹配/目标解析；**载荷原样携带，不再重派生**，D6/E7 修复）；`vault.file.deleted@1.1` → **不重放本地订阅者**（D3）：webhook/indexer/AV/replication/SSE 已同步收到 bus 广播（workers.go:35/53/74/141、ai.go:126、rest/sse.go），relay 重放 = 双重投递 → deleted@1.1 仅 complete + telemetry 计数，作为持久化生命周期记录（前向兼容）。**显式边界（GAP-2）：** commit→`s.emit` 窗口内 webhook/indexer/SSE 的本地投递缺口**不在本设计覆盖范围**（其持久性 = `object_events`+bus；`webhook_failures` 仅记已收事件的投递失败）。
- **complete**：`CompleteEventOutbox(ctx, id, owner)` —— 同事务写 `status='delivered'` + 插 `event_outbox_delivered`（**键 = outbox 行 id**，D4，保真保留，功能惰性——claim 谓词已按 status 排除）→ **恰好一次（仅 complete 后成立）**。**deliver→complete 窗口为显式 at-least-once（D7）：** POST 已发出而 complete 前崩溃/租约过期 → 重 claim 重复投递（S3 等价语义，接收方需幂等）。
- **retry**：5xx / 传输错误 → `RetryEventOutbox(ctx, id, owner, lastErr, next)`，指数退避 + jitter（复用 `billingBackoff` 形状：2×、上限 5min、±25%）；`attempts` 递增；**终态判定（`attempts >= MaxAttempts` → `status='failed'`）在 `RetryEventOutbox` 内**（仅 5 个 repo 方法）+ telemetry 计数；**claim-lost**（complete/retry 时 owner/token/lease 守卫失败）→ warn + telemetry、**无循环内重试**——租约重 claim 是恢复机制（D7）。
- 单事实投递失败**不影响**其它已 claim 事实（per-fact goroutine，E5 `deliverBatch` 形状）。

### FR-4：`cmd/server` 装配 + 迁移对

- 新增迁移对 `0041_event_outbox.{up,down}.sql`（sqlite + postgres 双份，I2）：
  - **`event_outbox`**：`id`（sqlite `AUTOINCREMENT` / pg `BIGSERIAL`；**去重主键 = 行 id**，D1——`RestoreObject` 原地 UPDATE 复用行 id，`UNIQUE(event_type, origin_id)` 会让 restore→re-delete 500，`ON CONFLICT DO NOTHING` 则会吞掉第二次删除的事实，均不可取）+ `tenant_id, origin_id`（参考列，无 FK）+ `event_type` + `payload TEXT`（**双方言 TEXT**，pg 不用 `jsonb`——jsonb 规范化键序/空白会破坏 AC-3/AC-2 的字节精确断言，D6）+ `status`（`pending|inflight|delivered|failed`，D5）+ `attempts/available_at_ns/claim_owner/claim_token/lease_expires_at_ns/last_error`（512 字节上限）`/created_at_ns`。索引：due 索引 `(status, available_at_ns, lease_expires_at_ns, created_at_ns)`（billing 先例；OR 谓词 pg 上 seq-scan，outbox 规模可接受，注明即可）+ `(tenant_id, created_at_ns)`（两先例均有）。`_ns` 列 = INTEGER UnixNano——I1 的 outbox 惯例（RFC3339Nano 文本只用于领域行），显式声明以预阻误读。
  - **`event_outbox_delivered`**：键 = `event_outbox_id`（outbox 行 id，D4），complete 事务内插入，功能惰性（保真保留，供 AC-2(4) 断言）。
  - `.down.sql` **先删 `delivered` 再删 `outbox`**。
- `cmd/server/workers.go` 新增 `startEventOutboxRelay(ctx, …)`（紧邻 `startNotificationWorker`，E12）：轮询 `ClaimEventOutbox` → 分发 → complete/retry；配置项（默认值镜像 billing/audit 先例，config_billing.go:51-55、config_audit_governance.go:234；键名落 `docs/configuration.md`）：`EVENT_OUTBOX_POLL_INTERVAL_MILLIS`（默认 1000）/ `EVENT_OUTBOX_BATCH_SIZE`（默认 32）/ `EVENT_OUTBOX_CLAIM_TTL_SECONDS`（默认 30）/ `EVENT_OUTBOX_HTTP_TIMEOUT_SECONDS`（默认 5）/ `EVENT_OUTBOX_MAX_ATTEMPTS`。**校验：`CLAIM_TTL_SECONDS > 2×HTTP_TIMEOUT_SECONDS`**（audit 先例；billing 仅要求 `>`，取更严格者，D7 阻断项）——否则慢目标 + 租约过期会在**无崩溃**时产生并发重复 POST。单 fact 顺序投递 N 目标的最坏在飞时间 = N×timeout，须满足文档化约束 `N×timeout < TTL`（或改每目标并行投递）。relay **始终启动**（核心删除原子性不 gate；无通知规则时静默无操作）。
- Notifier **保持 bus 订阅**（D2）：created/updated 与 E14 删除路径（`DeleteVersion` file_delete.go:161、delete-marker delete_marker.go:58、隔离/保留清除 object_worker.go:73，均非 outbox 化）依赖 bus 投递；对 `EventDeleted` **仅当** `EXISTS (SELECT 1 FROM event_outbox WHERE origin_id=e.ObjectID AND event_type='vault.file.notify@1.1')`（任意 status）时跳过——WithEvent 事务先于 `s.emit` 提交 → 行可见，且与 relay 进度无关（无竞态）；E14 行无 outbox 行 → 旧路径保留。bus 广播保留给 SSE/Indexer/webhook 等既有本地订阅者（E2 语义不变）。

### 非功能约束

- `make check` 全绿（`gofmt` / `go build` / `go vet` / `go test`，AGENTS.md §0）；新增文件 ≤ 500 行；新 SQL 遵守 I1（每个 `$N` 按出现顺序独立占位）与 I2（双文件迁移对、`.down.sql` 永不自动执行）。
- 纯 stdlib，无新 `go.mod` 依赖（I6）；不触碰中间件链（I4）与存储 key 校验（I3）；`EventSink` 接口与 `Bus.Publish` 签名不变（E2 的"不阻塞"保证原样保留）。
- 基线路径（SQLite + local FS + AI off）必须全绿；Postgres 方言差异仅限 `BIGSERIAL` 自增类型（`payload` **双方言 TEXT**，D6），逻辑同一（E10 先例）。

---

## 4. 验收标准（可测试）

> 测试基建（已验证）：`repository.Open("sqlite", "file:…")` + `repo.Migrate` 是既有模式（`chunks_events_buckets_test.go`、`billing_test.go`、`audit_governance_test.go`）；服务测试用 `service.NewFileService(store, repo, …)` + `httptest`；`billing_test.go:86-125` 已有 claim/complete/redelivery 断言先例可镜像。

### AC-1 单事务原子性 + `FileService.Delete` 恰好一条 deleted@1.1 事实

```go
// internal/repository/event_outbox_test.go
func TestDeleteObjectWithEvent_OneTx(t *testing.T) {
	// 1) 有效路径：对象行删除与 outbox 事实同事务提交
	//    HardDeleteObjectWithEvent(…, facts{deleted@1.1, notify@1.1}) → nil
	//    断言：GetObject → ErrNotFound；event_outbox 恰 2 行
	//    （event_type 各 1，origin_id=obj.ID，payload 字段自包含）；
	//    event_outbox_delivered 为空
	// 2) 强制回滚：传入非法事实（event_type 不在允许集 → 校验错误）
	//    → 方法返回 error；GetObject 仍存在（行未删）；event_outbox 为 0 行
	// 3) SoftDeleteObjectWithEvent 同形状：deleted_at 置位 + 恰 2 行 outbox
}

// internal/service/file_delete_test.go
func TestFileServiceDelete_EmitsExactlyOneDeletedFact(t *testing.T) {
	// 硬删与软删各一：Put → Delete(hard) / Delete(soft)
	// 断言：event_outbox 中 vault.file.deleted@1.1 恰 1 行；
	// payload 解析后 = {schema_version:"1.1", tenant, bucket, key,
	//   version_id, size, etag, backend, request_id, actor}
	// （actor 经 access 上下文注入；无上下文时允许 ""）
	// 且 notify@1.1 恰 1 行，records[0].s3.object.versionId == obj.VersionID
}
```

### AC-2 relay：claim→deliver→complete、5xx 退避重试、崩溃中继重投、complete 后去重（status）

```go
// internal/events/event_outbox_relay_test.go（或承载 relay 的包）
func TestOutboxRelay_DeliveryLifecycle(t *testing.T) {
	// 1) 成功路径：claim(owner-A) → deliver(httptest 目标收到恰 1 次 POST，
	//    载荷 == notify@1.1 原样) → complete → status=='delivered'；
	//    event_outbox_delivered（键 = outbox 行 id）恰 1 行
	// 2) 5xx 重试：目标返回 500 → RetryEventOutbox 已调度：attempts==1、
	//    available_at_ns > now、退避 ∈ [1s, 5min] 且抖动 ±25%（billingBackoff 界）
	// 3) 崩溃重投：claim(owner-A, ttl 短) 后不 complete；模拟租约过期 →
	//    claim(owner-B) 重新取到同一事实并投递成功
	//    （若 POST 已发出则重复投递——属预期 at-least-once，D7）
	// 4) 恰好一次（断言目标 = status 列，权威）：complete 后再 claim 同 origin
	//    → 无行返回；status=='delivered'。显式语义：deliver→complete 窗口为
	//    at-least-once（AC-2(3) 已验证重复 POST 路径），本断言仅钉死 complete 后的
	//    恰好一次；delivered 表（键 = outbox 行 id）为保真保留，不作断言依赖
}
```

### AC-3 golden JSON：钉死 deleted@1.1 与 notify@1.1 schema

```go
// internal/events/schema_test.go
func TestEventSchema_GoldenJSON(t *testing.T) {
	// 用生产构建函数（FR-2 的 payload builder）以固定输入
	// （tenant/bucket/key/version_id/size/etag/backend/request_id/actor）
	// 生成两条事实；与 golden 常量/文件逐字节比较（规范 marshal）。
	// 断言：显式 schema_version=="1.1"；deleted@1.1 无 records 字段；
	// notify@1.1 的 records[0] 含 eventName=="s3:ObjectRemoved:Delete"、
	// s3.object.{key,size,eTag,versionId,sequencer} 全部自包含。
}
```

### AC-4 组合 e2e：REST 删除 → 本地订阅者 + relay 双观察；崩溃恢复后投递完成

```go
// cmd/server/server_e2e_test.go（httptest 全服务器 + SQLite + local FS，
// 镜像 internal/integration/fullserver_test.go 基建）
func TestComposition_DeleteOutbox_E2E(t *testing.T) {
	// 1) 无崩溃：PUT → DELETE /v1/files/k → 本地订阅者收到 EventDeleted(key 匹配)；
	//    relay 向 httptest 通知目标 POST 了 notify@1.1（载荷字段匹配 AC-3 golden）
	// 2) 崩溃窗口：以 relay 未启动的服务器执行 DELETE → event_outbox 有 2 行事实、
	//    目标零 POST；重启 relay（同 DB）→ 目标收到 POST、status=='delivered'；
	//    complete 后重复 claim 不再投递（恰好一次——deliver→complete 窗口为
	//    at-least-once，接受重复 POST，D7）
}
```

### AC-5 既有行为不回归

- `go test ./internal/repository/ ./internal/events/ ./internal/service/ ./cmd/server/` 全绿；`make check` 全绿。
- `Bus.Publish` 签名与"错误吞掉、不阻塞"语义不变；`object_events` 写入路径（SSE 回放、`NextUnconsumedEvents`）不变；既有 40 对迁移文件不动（只新增 0041 对）。

---

## 5. 范围边界（明确不做）

| 不做 | 理由 |
|------|------|
| `DeleteVersion`（file_delete.go:161）、delete marker（delete_marker.go:58）、隔离/保留清除（object_worker.go:73、reconcile）路径的 outbox 化 | 本方向锚定 `FileService.Delete`（AC-1/AC-4 明文）；其余路径是同一 WithEvent 模式的机械扩展，属后续方向 |
| `Event.Payload` 从 `map[string]string` 迁移为结构化类型 / `Event` 结构体改造 | outbox 事实已携带自包含 JSON（FR-2）；遗留 `object_events` 流保持兼容（E6/E11） |
| `object_events` 与 `event_outbox` 双份持久化的合并/统一 | 本方向只补"原子性 + schema"；遗留流是既有行为（SSE 回放依赖它），合并属独立重构 |
| Webhook 投递管线改造 | 已有 durable retry + DLQ（`webhook_failures` 表）；本方向只覆盖通知 relay 与 deleted 回放 |
| 通知规则引擎（`GetBucketNotifications`/目标解析/ARN→HTTP）改动 | 复用现有规则匹配（E7 保留）；仅替换载荷来源与投递时序 |
| actor 传播管线（鉴权中间件之外的身份注入） | actor 取 `access.PrincipalFrom(ctx)`，空值合法；新增身份管线超出本方向 |
| Postgres 方言下 `FOR UPDATE SKIP LOCKED` 级别的 claim 并发优化 | 对齐 E10 既有 claim 实现即可，无需新并发特性 |

---

## 6. 实现指引（供验收后落地，非本规格交付物）

- **迁移**：`internal/repository/migrations/{sqlite,postgres}/0041_event_outbox.{up,down}.sql`——列与索引对齐 §FR-4。
- **Repository**（新文件 `internal/repository/event_outbox.go`，≤500 行）：`OutboxFact` 类型 + 校验（`event_type ∈ {vault.file.deleted@1.1, vault.file.notify@1.1}`、`origin_id>0`、payload 可解析且含 `schema_version`）；`HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent` 把 E3 现有事务体扩展为"删除逻辑 + `INSERT INTO event_outbox`"（事实插入在受影响行校验之后、同事务内，GAP-4；**无** `WHERE NOT EXISTS`，D4）；`ClaimEventOutbox`/`CompleteEventOutbox`/`RetryEventOutbox` 镜像 `audit_governance_claim.go`（SQLite tx 版 + Postgres 版），claim 谓词取 billing status 形状（D5）。
- **Schema 构建**（`internal/events/payload.go` 或 `internal/service` 内）：`BuildDeletedFact(obj, requestID, actor) []byte` / `BuildNotifyFact(obj, actor, requestID, sequencer) []byte`（sequencer = emit 时刻 `crypto/rand` 16 字节 hex，webhook token 先例，测试注入固定值；**不可取 `obj.ID`**——restore 复用行 id，D6；内部一次性调用 `buildS3Event` 的等价物生成 records）。
- **Service**（`internal/service/file_delete.go`）：`hardDeleteObject`/`softDeleteObject` 在调用 repo 前构建 facts，改调 `…WithEvent`；`s.emit` 保留（Notifier 侧条件跳过，D2）。
- **Relay**（`internal/events/event_outbox_relay.go` 或 `internal/notify`）：轮询 claim → 按 event_type 分发（notify→POST 目标；deleted→complete + telemetry，**不重放**，D3）→ complete/retry；退避复用 `billingBackoff` 或平移至共享 helper；claim-lost → warn + telemetry、无循环内重试（D7）。
- **装配**（`cmd/server/workers.go` + `internal/config`）：`startEventOutboxRelay` + 配置键（默认值/校验见 FR-4）；Notifier 保持 bus 订阅 + 条件跳过（D2），`startNotificationWorker` 不变。
- 验收测试按 §4 落地；`go test ./...` 与 `make check` 确认全绿。
