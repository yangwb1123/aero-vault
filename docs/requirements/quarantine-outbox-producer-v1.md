# 方向：隔离路径成为 `vault.file.deleted@1.1` 事务性 outbox 的试点生产者 + `vault.file.notify@1.1` 自包含载荷

> **模块：** `internal/antivirus`（+ `internal/service`、`internal/repository`、`cmd/server` 的最小触点）· **来源分析：** `docs/auto/analyses/internal-antivirus-4eff1e6c.json`（方向 1）· **日期：** 2026-08-06
> **评分：** 价值 9 / 风险降低 8 / 工作量 7 / 置信度 8
> **本文所有代码引用均已对照仓库逐行验证**（行号以当前 HEAD 为准）。方向文中的行号引用与仓库一致，无漂移；唯一需要修正的是方向问题陈述中的一句现状描述（见 §2.1 修正注）。

---

## 1. 问题陈述（已验证）

`QuarantineObjectByID`（`internal/service/object_worker.go:38-77`）是代码库中**唯一的系统发起删除**，但今天它只做软删 + `s.emit(EventDeleted)`：

1. **无审计**：不写 `audit_log`。`RecordAudit` 的直接调用点只有 admin API（`internal/api/rest/admin.go:421`）——隔离删除发生后，任何审计查询都看不到这次文件删除。
2. **无事务性 outbox 事实**：不写 `event_outbox`。`SoftDeleteObjectByID` 自带事务提交后，`addTenantUsage` 与 `s.emit`（→ `Bus.Publish` → `InsertEvent`）各是独立事务；`Publish` 对持久化错误只记日志、从不传播（`internal/events/bus.go:76-80`）——事件与删除不原子，且 `object_events` 行**没有版本化 schema**（无 `schema_version`/`version_id`/`actor`/原因），无法满足 COMPOSE-2026-017 的 `vault.file.deleted@1.1`（durable_async 事务性 outbox，不阻塞业务流）与 `vault.file.notify@1.1`（自包含载荷）。

**修正注（相对方向文）：** 方向文称 "No audit record exists for file deletion at all" —— 该表述在**当前 HEAD 已部分过时**：REST/admin 删除路径（`FileService.Delete`）已实现事务性 outbox 全套（迁移 `0041_event_outbox`、`HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent`、`deleteAuditEntry`/`deleteFacts`、`EventOutboxRelay`、Notifier D2 去重）。**已验证的当前缺口精确收敛为：隔离路径（`QuarantineObjectByID`）是唯一尚未接入 outbox/审计的删除生产者**——"第一个系统删除类型将无审计/无通知地上线"的核心论断依然成立，且本轮全部改动都落在该路径上。

---

## 2. 现状与代码证据（逐条验证）

| # | 证据（方向文引用） | 验证结果（HEAD） |
|---|------|------|
| E1 | `internal/service/object_worker.go:38-77` — `QuarantineObjectByID`：GetObjectByID（:39）→ `authorizeObject(ActionDelete)`（:46）→ tombstone 分支 `DeleteVersion`（:51-52）→ `DeletedAt` 幂等 no-op（:54-55）→ `preflightQuota`（:57-59）→ chunkCleaner（:60-64）→ **`repo.SoftDeleteObjectByID`（:65，自带事务）** → `addTenantUsage`（:69-71，独立事务）→ `s.emit(EventDeleted)`（:72） | ✅ 行号精确（函数体 :38-74）。**无 `audit_log` 写入、无 outbox 事实**；删除与事件跨三个事务 |
| E2 | `internal/events/bus.go:76-80` — `Publish`：先 `InsertEvent` 持久化再本地广播；错误仅 `logger.Warn`，"Errors are logged but never propagated" | ✅ 行号精确（`Publish` 为 :76-95；:81-86 吞错）——durable 但**与删除非事务性** |
| E3 | `internal/repository/audit.go:10-21` — `insertAuditEntry`（事务内插 `audit_log`）；`RecordAudit`（:33-46，独立调用）；"only admin.go:421 calls it" | ✅ `RecordAudit` 直接调用点全仓仅 `admin.go:421`（另有 `auditgovernance/repository.go:22-27` 装饰器委托，非独立调用点） |
| E4 | `internal/api/rest/admin.go:421` — `_ = h.repo.RecordAudit(...)`（`auditForTenant` :414-426） | ✅ 行号精确 |
| E5 | `internal/repository/billing_outbox.go:12-45` — `ClaimBillingUsage`：claim 式 durable outbox（status/attempts/claim_owner/claim_until_ns） | ✅ 行号精确。**注：** 事件 outbox 已按此模板实现（`event_outbox.go` + 迁移 `0041_event_outbox.{up,down}.sql`，sqlite+postgres 双份），本方向**复用现成模板，不新建表** |
| E6 | `internal/auditgovernance/relay.go:22-48` — L2 治理 relay 的 gap 对账先例 | ✅ 行号精确（`Runtime.reconcile` :22-48）。**注：** 事件 outbox relay 已存在（`internal/events/event_outbox_relay.go`，`cmd/server/workers.go:63` 恒启动），本方向**不新增 relay** |
| E7 | `internal/antivirus/worker.go:120-157` — `ScanObjectByID`：取对象（:122-125）→ 取字节扫描（:126-135）→ 写 tags `av_status`/`av_signature`（:135-143）→ infected 且 quarantine=true → `QuarantineObjectByID`（:146-152） | ✅ 行号精确（`ScanObjectByID` 实为 :119-157）。**关键细节：** tag 写入在隔离调用**之前**提交，`res.Signature` 在 worker 手中 |
| E8 | `internal/antivirus/antivirus.go:15-20` — `Scanner` 端口（`SignatureScanner`/`HTTPScanner` 两个适配器） | ✅ 行号精确 |

### 2.1 补充验证（方向文未引、但决定实现形状的事实）

| # | 事实 | 位置 |
|---|------|------|
| S1 | `SoftDeleteObjectWithEvent`/`HardDeleteObjectWithEvent` 已存在，**按 (tenant,bucket,key) 键控**；单事务 = 删除 + `insertAuditEntry` + `insertOutboxFacts`（含 `validateOutboxFacts` 校验，失败整体回滚）；零行删除 → `ErrNotFound` 回滚，不留幽灵事实 | `internal/repository/event_outbox.go:96-176` |
| S2 | **没有按 object ID 键控的 WithEvent 变体**——隔离路径按 ID 操作（精确版本），必须新增 `SoftDeleteObjectByIDWithEvent`；其事务体照抄 `SoftDeleteObjectByID`（`WHERE id=? AND deleted_at IS NULL` 守卫 + `deleteObjectAccessState`，`internal/repository/sql_objects_maint.go:42-63`） | `repository_interface.go:31-32` |
| S3 | `access.SystemContext` 的 `SubjectID` 是 `"aero-vault-system"`（`internal/access/context.go:16-25`），**不是** `"system:antivirus"`；`PrincipalSystem` 在 `authorizeObject` 处直接放行（`internal/access/authorizer.go:23`）→ 换 principal 不改变鉴权行为 | `cmd/server/workers.go:33`（AV job 现跑在 SystemContext 下） |
| S4 | 现有 `deletedFact`/`notifyFact` 载荷**无 reason/signature 字段**；`internal/events/schema_test.go` 以字节精确 golden 钉死字段序 | `internal/events/payload.go:26-140` |
| S5 | Notifier D2 去重已就位：`EventDeleted` 广播时若该 origin 已存在 notify@1.1 outbox 行（任意 status），跳过 bus 投递——隔离路径写入 outbox 事实后**自动免双投**，且注释明示 E14 路径（隔离/DeleteVersion/delete-marker）无 outbox 行时保留 bus 路径 | `internal/events/notifier.go:70-81` |
| S6 | relay 恒启动（`cmd/server/workers.go:63,158-177`）；`deliverDeleted`（sink==nil → complete）、`deliverNotify`（无规则 → complete，不联网）；claim→deliver→complete 全链路与 telemetry 计数已存在 | `internal/events/event_outbox_relay.go:111-178,182-214,232-305` |
| S7 | `GetObjectByID` 不过滤 `deleted_at`（软删行仍可读）；`QuarantineObjectByID` 的 `DeletedAt != nil → return nil` 幂等守卫保持；queue 去重键 `virus_scan:<object_id>` 已存在；**无 `UNIQUE(event_type, origin_id)`**（D1：`RestoreObject` 原地 UPDATE 复用行 id，恢复→再删会撞唯一键） | `internal/repository/sql_objects.go:188-199`；`internal/antivirus/worker.go:97-100`；`0041_event_outbox.up.sql` |
| S8 | AV 测试基座已就绪：`setupSvc` 用真实 sqlite repo + 真实 `FileService`（`internal/antivirus/antivirus_test.go:50-73`），EICAR 全链路可离线复现；`jobs.Pool.Run`/`Queue.Enqueue`/registry 可直接组 composition | `internal/jobs/jobs.go:70-230` |

---

## 3. 需求规格

> 范围边界：本方向只让**隔离软删分支**成为 outbox 生产者。tombstone 分支（`DeleteVersion`）、REST 删除路径（已实现）、AuthorizationProvider 端口（fail_closed）、删除竞态终态跳过**均不在本轮范围**（§6）。

### FR-1：新增 `SoftDeleteObjectByIDWithEvent`（按 ID 的事务性删除 + 审计 + outbox 事实）

`internal/repository` 新增方法（接口 `repository_interface.go` + `sqlStore` 实现，sqlite/postgres 同文件、走 `rebind`，遵守 I1）：

```
SoftDeleteObjectByIDWithEvent(ctx context.Context, id int64, entry AuditEntry, facts []OutboxFact) error
```

- 单事务 = 现有 `SoftDeleteObjectByID` 全部逻辑（`SELECT tenant_id,bucket,key WHERE id=$1 AND deleted_at IS NULL` → `UPDATE objects SET deleted_at WHERE id=$1 AND deleted_at IS NULL`，零行 → `ErrNotFound` → `deleteObjectAccessState`）+ `insertAuditEntry` + `insertOutboxFacts`（复用 `event_outbox.go:180` 的现有插入函数）。
- `validateOutboxFacts`（`event_outbox.go:53-77`）在事务内执行；任何失败（含约束冲突、载荷超 1 MiB、`schema_version`≠1.1）→ **整体回滚**：对象行不软删、审计行不落、outbox 事实不落（AC-1 强制回滚断言）。
- 零行删除（并发双删/已删）→ `ErrNotFound`，同事务回滚，**不产生幽灵事实**（对齐 `SoftDeleteObjectWithEvent` 的 GAP-4 语义，`event_outbox.go:143-176`）。
- **不新增迁移**：`event_outbox` 表已存在（`0041_event_outbox.{up,down}.sql`，sqlite+postgres 双份），`origin_id` 无 FK、接受任意 objects.id（S7）。现有 `SoftDeleteObjectByID` 保留不动（reconcile 等其它调用方继续使用，对齐 `transactional-outbox-delete-events-v1.md` §FR-1 的既有决策）。

### FR-2：`QuarantineObjectByID` 在删除事务内产生两条事实（deleted@1.1 + notify@1.1）

`internal/service/object_worker.go` 的 `QuarantineObjectByID` 重构（tombstone 分支、`DeletedAt` 幂等守卫、`preflightQuota`、chunkCleaner 均不变）：

1. **接口变更（最小触点）：** `internal/antivirus/worker.go:32` 的 `ObjectController.QuarantineObjectByID` 增加 `signature string` 形参（worker.go:149 传 `res.Signature`；FileService 侧同步改签名）。**拒绝备选方案：** 从 `obj.Tags["av_signature"]` 反读签名——service 不得耦合 antivirus 的 tag 键约定，且 tag 写失败时扫描本就中止，显式传参更可测。
2. **审计条目**（镜像 `file_delete.go:100-117` 的 `deleteAuditEntry` 形状）：`Action=repository.AuditActionFileDelete`（`audit.go:12-13`）、`Actor` = 上下文 principal 的 `SubjectID`（FR-4 保证为 `system:antivirus`）、`Target=bucket/key`、`TenantID`、`Detail` 携带原因（`"av_infected"`，可测）。
3. **两条事实**（镜像 `file_delete.go:123-150` 的 `deleteFacts` 形状）：
   - `vault.file.deleted@1.1`：`events.BuildDeletedFact(obj, actor, requestID, tenant)` 扩展出 reason（FR-3），值为 `"av_infected"`——**service 侧常量**（隔离路径仅 AV 调用，E7 已验证唯一调用点；常量即本轮"原因词表"的试点条目）。
   - `vault.file.notify@1.1`：`events.BuildNotifyFact(obj, actor, requestID, tenant, sequencer)` 扩展出 signature（FR-3），值为 FR-2.1 传入的 `res.Signature`。
   - `request_id` 沿用 `middleware.RequestIDFrom(ctx)`；sequencer 沿用 `newSequencer()`（`payload.go:31-42`；测试注入固定值）；actor 允许空串的既有约定不变（FR-2 约束：不新增鉴权管线）。
4. **调用顺序：** 构建 entry+facts **之后**调用 `repo.SoftDeleteObjectByIDWithEvent(ctx, obj.ID, entry, facts)`（删除+审计+事实单事务提交）；提交后照旧 `addTenantUsage`（`UsageQuarantine` 扣减）与 `s.emit(EventDeleted)`。**有意的语义继承**（对齐已接受的 REST 路径语义，见 `transactional-outbox-delete-events-v1.md` §FR-1）：usage 失败不抑制已提交的事实；`s.emit` 仍是本地广播（indexer/webhook/SSE），Notifier 因 D2 去重（S5）自动跳过 bus 投递、由 relay 从自包含载荷投递——**无双投**。

### FR-3：载荷 schema 追加可选字段（REST 路径字节不变）

`internal/events/payload.go`：

- `deletedFact` 追加 `Reason string \`json:"reason,omitempty"\``（位于 `actor` 之后）；`notifyFact` 追加 `Signature string \`json:"signature,omitempty"\``（位于 `records` 之后）。
- `omitempty` 保证 REST 删除路径（空值）的既有 golden 字节**逐字节不变**（`internal/events/schema_test.go` 的 `goldenDeletedFact`/`goldenNotifyFact` 无需改动——这是硬性回归约束）。
- `schema_version` **保持 `"1.1"`**：追加可选字段是 additive，不升版本（`validateOutboxPayload` 只校验 `schema_version=="1.1"`，`event_outbox.go:79-86`，不受影响）。
- 构建函数签名相应扩展（`BuildDeletedFact`/`BuildNotifyFact` 增加 reason/signature 形参，REST 路径传空串；具体形态——新参 vs variadic——留给设计，载荷字节是契约）。

### FR-4：`system:antivirus` 系统主体

- `internal/antivirus/worker.go` 导出常量 `SystemActor = "system:antivirus"`。
- `Worker.ScanObjectByID` 对 `ObjectController` 的两个调用（`SetObjectTagsByID` + `QuarantineObjectByID`）统一在 `access.WithPrincipal(ctx, access.Principal{SubjectID: SystemActor, TenantID: obj.TenantID, Kind: access.PrincipalSystem})` 上下文中执行——保证**无论调用方上下文如何**，隔离路径产出的 actor 恒为 `system:antivirus`（单位测试可直接用 `context.Background()` 断言）。
- `PrincipalSystem` 在 `authorizeObject` 直接放行（S3）→ **鉴权行为零变化**；`cmd/server/workers.go:33` 可保留 `SystemContext` 或换用同常量，二选一（装配细节，不改变语义）。

### FR-5：durable_async 交付（不阻塞业务流）

- **零新增投递代码**：隔离路径写入的 outbox 行由已恒启动的 `EventOutboxRelay`（S6）按现有 claim→deliver→complete 周期排空；`deleted@1.1` 无 L2 sink 时 complete（L0 `audit_log` 已在删除事务内落库，`audit_log` 恒权威），`notify@1.1` 按 bucket 通知规则从**存储载荷原样**投递（规则缺失 → complete，不联网）。
- **硬性约束：** 病毒扫描 job 的完成路径上**不得有任何**对 relay 进度、outbox 状态或通知目标可达性的依赖/等待——outbox 写入随删除事务提交即结束业务流（AC-2 的"dispatcher 停止时 job 延迟不受影响"断言）。

---

## 4. 验收标准（保留方向文检查项，逐一可测化）

### AC-1（Unit · 同事务原子性 + 回滚）

**位置：** `internal/antivirus/antivirus_test.go`（`setupSvc` 基座，S8）+ `internal/repository` 事务回滚测试。

1. **正向断言：** `ScanObjectByID(ctx, obj.ID)`（EICAR 对象、quarantine=true）成功后：
   - `event_outbox` 中 `origin_id=obj.ID` 恰有 **2 行 pending**：一行 `event_type=vault.file.deleted@1.1`，其 `payload` JSON 含 `"actor":"system:antivirus"` 与 `"reason":"av_infected"`；一行 `event_type=vault.file.notify@1.1`，其 `payload` 含 `"signature":"EICAR-Test-File"`；
   - `audit_log` 恰有 1 行 `action=file.delete`、`actor=system:antivirus`、`detail` 含 `av_infected`；
   - 对象行 `deleted_at` 非空、配额归零（既有断言保留）。
2. **强制回滚断言（同事务）：** repository 级调用 `SoftDeleteObjectByIDWithEvent` 并注入**一个校验必败的事实**（如 `schema_version` 为 `"2.0"` 的 payload，触发 `validateOutboxFacts`）→ 返回错误，且：`objects.deleted_at` 仍为 NULL、`event_outbox` 该 origin 计数为 **0**（无孤儿行）、`audit_log` 计数为 **0**。
3. **并发双删守卫断言：** 对已软删的 id 再次调用 → `ErrNotFound`，且不新增任何 outbox/audit 行。

### AC-2（Unit · durable_async：dispatcher 停摆不影响业务 job）

**位置：** `internal/antivirus/antivirus_test.go`（composition 形态：Worker + 真实 `jobs.Queue`/`Registry` + sqlite repo）。

1. **relay 未启动**（测试不建 relay）：`ScanObjectByID` 返回 nil，job 进入 done，`attempts==1`；`event_outbox` 该 origin 2 行**保持 pending**（可稍后投递——durable）。
2. **relay 排空不触碰业务 job：** 随后调用 `relay.deliverBatch()`（或短时 `Run`，无通知规则 → complete，零网络）→ 2 行进入终态（`delivered`/`failed`），**jobs 表该 job 的 attempts 不变、无新 virus_scan 行**（排空路径只操作 `event_outbox`，绝不重入业务 handler）；`audit_log` 行数与 AC-1 一致（L0 权威，不因 relay 增删）。
3. **断言"写 outbox 不使扫描失败"：** 删除事务成功而 relay 永不启动的场景下，`jobs` 表无 `failed` 行、无重试风暴。

### AC-3（Event schema · golden-JSON 字节钉死）

**位置：** `internal/antivirus/antivirus_test.go` 新增 golden 测试（固定输入：`repository.Object{ID:42, VersionID:"v-abc", ...}` 或 `setupSvc` 真实对象；固定 `request_id`/sequencer——sequencer 经 `payload.go:31-42` 的注入点固定）。

- 隔离路径的 `deleted@1.1` 与 `notify@1.1` 载荷逐字节等于各自 golden 常量（含 `"reason":"av_infected"`、`"signature":"EICAR-Test-File"`、`"actor":"system:antivirus"`、`"schema_version":"1.1"`）。
- **回归约束：** `internal/events/schema_test.go` 的既有 golden（REST 路径）**原样通过、零改动**——证明追加字段对既有路径字节不可见。

### AC-4（Composition e2e · EICAR → job → 隔离 → jobs/audit_log/outbox 轮询 + 幂等）

**位置：** `internal/antivirus`（真实 repo + `FileService` + `SignatureScanner` + `jobs.Queue`/`Pool` + `EventOutboxRelay`；sqlite+local FS，零网络——无通知规则时 relay complete 不联网，CI 基线可跑）。

1. 上传 EICAR 文件（`svc.Put`）→ 经 bridge（`Run` 消费 bus 的 `created` 事件）或直接 `Enqueue`（`DedupeKey=virus_scan:<object_id>`）入队 → `Pool` 执行。
2. 轮询 `jobs` 表：job 终态 done、`attempts==1`；轮询 `audit_log`：**恰好 1 条** `file.delete`、`actor=system:antivirus`；轮询 `event_outbox`：**恰好 1 条** `vault.file.deleted@1.1`（payload 含 `av_infected`）+ **恰好 1 条** `vault.file.notify@1.1`（payload 含 `av_signature` 值 `EICAR-Test-File`）。
3. **重复投递幂等：** 模拟 job pool 重试——对同一 job 再次执行 handler（第二次 `GetObjectByID` 读到 `deleted_at` → no-op）→ outbox/audit 行数**不变**（零重复）。
   - **幂等键 = `object_id+version_id`**，机制 = ① 队列去重键 `virus_scan:<object_id>`（防双入队）② 删除事务的 `WHERE deleted_at IS NULL` 守卫 + service 层 `DeletedAt` no-op（防双执行）。**明确不使用** `UNIQUE(event_type, origin_id)`（S7/D1：`RestoreObject` 复用行 id，恢复→再删会误吞第二次删除的事实）。
4. relay 排空后：notify@1.1 行按规则投递或 complete；deleted@1.1 行 complete——均不产生重复审计。

---

## 5. 实现触点清单（最小改动面）

| 文件 | 改动 |
|------|------|
| `internal/repository/repository_interface.go` + `internal/repository/event_outbox.go` | 新增 `SoftDeleteObjectByIDWithEvent`（FR-1） |
| `internal/service/object_worker.go` | `QuarantineObjectByID` 重构：签名 + entry/facts 构建 + WithEvent 调用（FR-2） |
| `internal/antivirus/worker.go` | `ObjectController.QuarantineObjectByID` 增参（:32, :149）；`SystemActor` 常量 + controller 调用上下文 principal（FR-2.1/FR-4） |
| `internal/events/payload.go` | `deletedFact.reason,omitempty` / `notifyFact.signature,omitempty` + 构建函数扩展（FR-3） |
| `internal/events/schema_test.go` | **零改动**（回归约束，AC-3） |
| `internal/antivirus/antivirus_test.go` | AC-1/AC-3/AC-4 测试 |
| `internal/repository/event_outbox_test.go`（或同包测试） | AC-1 回滚断言 |
| 迁移 | **无**（`0041_event_outbox` 已存在，I2 不新增） |
| `go.mod` | **无新增依赖**（I6） |

---

## 6. 明确不在本方向范围（防蔓延）

- **AuthorizationProvider 端口 / fail_closed / 显式系统主体端口化**（来源分析方向 2）：本方向只保证 actor 字符串为 `system:antivirus`，不引入可替换鉴权端口。
- **删除竞态终态跳过 / EventDeleted 失效扫描**（来源分析方向 3）：本方向不改变 `ErrNotFound → 重试` 语义。
- **tombstone 分支**（`object_worker.go:51-52` 的 `DeleteVersion`）：独立删除生产者（既有 `transactional-outbox-delete-events-v1.md` §5 E14 明确排除），本轮不动。
- **REST 删除路径**：已实现，仅受 FR-3 追加字段的字节不变约束。
- **L2 AuditSink 适配器**（跨项目治理对接）：relay 端口已存在（`events/audit_sink.go`），本轮只产出行，不实现新适配器。
- **outbox 表结构/去重键变更**：`UNIQUE(event_type, origin_id)` 明确不加（S7/D1）。

## 7. 不变量与测试模式映射

- **I1**：新 SQL 走 `s.rebind` 占位符（照抄 `sql_objects_maint.go:42-63` 形状）；时间戳沿用 `RFC3339Nano`（领域行）与 `_ns`（outbox 行，`0041` 既有约定）。
- **I2**：无新迁移。
- **I4**：middleware 链零改动；`system:antivirus` principal 只在 job handler 上下文注入，不触碰 HTTP 链。
- **I5**：隔离出 outbox 行不改变任何 opt-in flag；relay 恒启动语义不变（S6）。
- **I6**：仅 stdlib + 既有依赖。
- 测试模式：`repository.Open("sqlite", "file:...")` + `Migrate` + `storage.NewLocal`（对齐 AGENTS.md 测试模式与 `setupSvc` 基座）；断言仅 `testing`，无框架。
