# 方向：删除事件事务性 outbox（durable_async）——`vault.file.deleted@1.1` 审计 + `vault.file.notify@1.1` 通知（internal/api/webdav 模块验收契约）

> **模块：** `internal/api/webdav`（组合面：`internal/service` + `internal/repository` + `internal/events` + `cmd/server`）· **来源分析：** `docs/auto/analyses/internal-api-webdav-c346cab0.json`（方向 2）· **日期：** 2026-08-06
> **评分：** 价值 8 / 风险降低 8 / 工作量 7 / 置信度 8
> **验证基准：** 工作树 = HEAD `acfaaf4` + **未提交 WIP**（quarantine 批次，与 `durable-async-delete-outbox-rest-v1.md` 同基准）。本文所有引用均已对照该基准逐行验证；实测 `go build ./...` 退出码 0、`go vet` 干净、`go test ./internal/{api/webdav,events,repository,service}` 全 `ok`、`internal/integration` 四个 outbox 组合测试 `-count=1` 实测全绿（§2.3）。
>
> **本文是增量验收规格：** 方向 acceptance 要求的机制（版本化 @1.1 事件 schema、事务性 outbox、claim/lease/retry/ack relay、AuditSink 端口、L0/L1/L2 可切换）**已在 round-1/round-2 campaign 落地**（`fb74b19`、`4cca6db` 及 WIP）。本文的职责 = ①逐条验证方向文 7 条引用并修正已过时主张；②把 3 条 acceptance 映射为**可执行测试**（现状已覆盖项 + **WebDAV 模块面的真实缺口** G1/G2/G3）；③钉死范围边界。**不是绿地设计，生产代码无预期改动。**

---

## 1. 问题陈述（方向文 vs 仓库现状）

方向文写于分析快照，其问题描述的核心主张在**当前仓库已不成立**：

| 方向文主张 | 现状（已验证） |
|-----------|---------------|
| "No versioned event schema exists … nothing emits 'vault.file.deleted@1.1'" | **已过时**：`repository.OutboxEventType`（`internal/repository/event_outbox.go:14-21`）定义 `vault.file.deleted@1.1` / `vault.file.notify@1.1`；`events.BuildDeletedFact`/`BuildNotifyFact`（`internal/events/payload.go:109-160`）产出自包含 @1.1 载荷，golden 字节测试钉死（`schema_test.go`）。`repository.Event`（`repository.go:175-189`）仍是遗留四类型（created/updated/deleted/accessed）——版本化按方向文原文"加到 envelope"，落在 outbox 载荷而非 Event 结构体 |
| "File deletion writes no audit record today" | **已过时**：`file_delete.go:100-121` `deleteAuditEntry` + `:123-145` `deleteFacts`，随删除**同一事务**经 `HardDeleteObjectWithEvent`（`:46`）/`SoftDeleteObjectWithEvent`（`:86`）提交：1 行 `audit_log`（`AuditActionFileDelete`）+ 2 条 outbox 事实原子落库 |
| "the only audit path is the hardcoded Snaplink governance relay … not a replaceable AuditSink" | **前半仍成立、后半已过时**：`auditgovernance/model.go:17-28` 的 `governancePath="api/v1/events"` 硬编码面**原样保留**（legacy 租户绑定 + 脱敏机制零改动）；但新增了**常开 AuditSink 端口**（`internal/events/audit_sink.go`，`DeliverDeleted` 契约 + `ErrSinkNotBound`/`ErrSinkUnauthorized`）与配置驱动的 L2 适配器（`internal/events/audit_sink_l2.go`，Bearer + `X-Audit-Fact-Id` echo receipt，无 sibling 导入） |
| "the emit happens post-commit via Bus.Publish, not in the delete transaction" | **语义修正**：`s.emit`（`file.go:297-314`）仍是删除事务提交后的遗留本地广播（错误吞掉），但**权威持久路径已转移**——audit + 两条事实与删除同事务；`s.emit` 仅剩 SSE/indexer/AV/replication/webhook 的本地广播职责，且 `notifier.go:58-79` 以 `HasEventOutboxFact`（调用点 :74）跳过 delete 的 bus 通知路径（D2 去重，防双发） |

**仍然真实的问题残余（本规格要钉死的模块面缺口）：**
1. **WebDAV 模块面零断言**：`internal/api/webdav/` 全目录无 audit/outbox 字样（rg 实测零命中）——`TestDeleteRemovesResource`（`dav_test.go:139`）只断言 HTTP 状态码与后续 404，**不验证** WebDAV DELETE 是否产出 audit_log 行与两条 outbox 事实；L1（webhook HMAC POST）/L2（AuditSink）的 e2e 组合测试全部经由 REST 面（`internal/integration/fullserver_test.go`），WebDAV 面无等价覆盖。
2. **durable_async 时序断言只钉在 REST 面**：`TestDeleteResponse_DoesNotBlockOnDelivery`（`fullserver_test.go:685`）证明"DELETE 响应不等待 L2 投递"的是 REST 路径；WebDAV `davFS.RemoveAll → svc.Delete(hard=true)`（`dav.go:141-147`）走同一 relay，但模块面无时序测试。
3. **配置文档漂移**：`docs/configuration.md:354-358` 已记录 `EVENT_OUTBOX_*`，但 **`AUDIT_SINK_L2_*` 未落文档**（rg 零命中）——"L2 可选无需代码改动"的运维面证据不全。

---

## 2. 现状与代码证据（方向文 7 条引用逐条验证）

### 2.1 验证表

| # | 方向文引用 | 验证结果（行号以当前基准为准） |
|---|-----------|------------------------------|
| E1 | `internal/repository/repository.go:174-197` — Event schema，无版本化名称 | ✅ `Event` 结构体 :175-189（ID/TenantID/Bucket/Key/Type/ObjectID/RequestID/Payload/CreatedAt，无 version 字段）；`EventType` :188-196 仅 created/updated/deleted/accessed。**语义修正：** 版本化名称在 `OutboxEventType`（`event_outbox.go:14-21`），不在 Event 上 |
| E2 | `internal/service/file.go:297-312` — emit 内联 payload | ✅ 行号精确：`emit` :297-314，payload = `{backend, size, etag, content_type}`（map），错误吞掉（:312-313 注释 *"lifecycle events are best-effort and must never break a user request"*）。**语义修正：** 删除的权威事实由 `deleteFacts`（`file_delete.go:123-145`）在事务内构建，`emit` 仅为遗留本地广播 |
| E3 | `internal/service/file_delete.go:16-96` — 删除无审计写 | ❌ **已过时**：`hardDeleteObject` :18-73（`HardDeleteObjectWithEvent` :46）、`softDeleteObject` :76-98（`SoftDeleteObjectWithEvent` :86）、`deleteAuditEntry` :100-121、`deleteFacts` :123-145——审计行 + 2 事实与删除同事务。**保留主张：** `DeleteVersion` :174-219 无 outbox 行（E14 路径走 bus），`quarantine` 走 `SoftDeleteObjectByIDWithEvent`（WIP） |
| E4 | `internal/events/bus.go` — persist-then-broadcast; drop-on-full | ✅ `Publish` :80-104 = `repo.InsertEvent` → 非阻塞 broadcast → transport；错误仅 warn（:86-88、:101-103）；drop 计数 `Dropped()` + `TestSubscribe_BufferedAndDropsWhenFull`（`bus_test.go:172`） |
| E5 | `internal/events/notifier.go + webhook.go` — S3 风格 notify、durable retry | ✅ `s3EventName` :158-166；**新增 D2 去重**：`notifier.go:58-79` 对 `EventDeleted` 查 `HasEventOutboxFact(…, EventTypeFileNotify11)`（调用点 :74），命中则跳过 bus 通知路径（outbox 事务先于 `s.emit` 提交，无竞态）；`webhook.go` `WithSecret` :66-71 HMAC-SHA256 签名、`WithRetryStore` durable retry + DLQ；HMAC 测试 `TestWebhookDeliverWithHMAC`（`webhook_test.go:150`）。**注意：** 通知 relay 的 `postEventTo`（`notifier.go:137-153`，bus Notifier 与 outbox relay 共享）**无 HMAC**——S3 通知形状，与 webhook worker（HMAC）是两条 L1 面 |
| E6 | `internal/repository/audit_governance_claim.go + internal/billing/outbox.go` — outbox 先例 | ✅ `audit_governance_claim.go`：`ClaimAuditGovernance` :16、`CompleteAuditGovernance` :124、`RetryAuditGovernance` :135（owner+token+lease 栅栏）；`billing_outbox.go:11` `ClaimBillingUsage`（**status 形状**谓词：pending+到期 OR inflight+租约过期）。事件 outbox 的 claim 谓词直接采用 billing 形状（`event_outbox.go:251-264`），退避/抖动镜像 audit relay |
| E7 | `internal/auditgovernance/model.go:17-28` — 硬编码 L2 endpoint | ✅ `governancePath="api/v1/events"` :19（const 块 :17-27）原样存在；`auditgovernance` 机制（租户绑定、脱敏 digest、revision/draining）**零改动**。L2 新面 = `internal/events/audit_sink_l2.go`（配置驱动：`NewAuditSinkL2` 校验 HTTPS-or-loopback :39-70、强制禁重定向 H6、401/403 短路 `ErrSinkUnauthorized`、2xx+`X-Audit-Fact-Id` echo 才 complete）——核心代码无 sibling 导入 |

### 2.2 WebDAV 模块面证据（本规格锚点）

| 符号 | 位置 | 语义 |
|------|------|------|
| `davFS.RemoveAll` | `internal/api/webdav/dav.go:141-147` | WebDAV `DELETE` 唯一入口：`f.svc.Delete(ctx, f.tenant(ctx), service.DefaultBucket, name, true)`（**恒 hard=true**，default bucket）；`ErrNotFound` → `os.ErrNotExist`（HTTP 404）。→ 恒走 `hardDeleteObject` 事务路径（E3） |
| `davFS.Rename` | `dav.go:150-206` | MOVE = copy-then-delete：`svc.Put` 目标 → `svc.Delete` 源（:198）→ 失败回滚删目标（:199-204）。**可观察事实：** 每次 MOVE 对源对象产生一次删除 → 1 条 audit 行 + 2 条 outbox 事实（假删除信号）。**处理：** 属方向 3（MOVE 抑制），本规格明确不做（§5） |
| 租户/身份 | `davFS.tenant` + `mw.Tenant`（`dav_test.go:64-70` 对齐 main.go 装配） | tenant 来自上下文（`X-Aero-Tenant`，缺省 `default`）；WebDAV 测试无 principal/RequestID 中间件 → `actor`/`request_id` 为**合法空串**（`deleteFacts`/`deleteAuditEntry` 明确允许，无新身份管线） |
| 测试基建 | `dav_test.go:43-70` `newTestServerWithSvc` | 全栈 harness：SQLite + `Migrate` + local FS + `service.NewFileService` + `mw.Tenant`，**未挂 Bus/relay**——L0/outbox 断言可直接经 repo handle 做；L1/L2/时序断言需扩展 harness（§6） |
| 模块面缺口 | rg `audit|outbox|deleted@|notify@` `internal/api/webdav/` → **零命中** | 方向 acceptance 的"delete 产生 @1.1 事件 + audit 行"在模块面无任何测试钉死（G1）；L1/L2 组合测试全在 `internal/integration`（REST 面，G2） |

### 2.3 当前可执行状态（实测）

- `go build ./...` → 退出码 0；`go vet ./internal/api/webdav/ ./internal/events/ ./internal/repository/` → 无输出。
- `go test ./internal/api/webdav/ ./internal/events/ ./internal/repository/ ./internal/service/` → 全部 `ok`（缓存命中，基线无回归）。
- `go test ./internal/integration/ -run 'TestDeleteResponse_DoesNotBlockOnDelivery|TestComposition_AuditSinkL2BoundTenant|TestComposition_DeleteDeliversBothFacts|TestComposition_MidClaimRestartRedeliversOnce' -count=1` → **4/4 PASS**（9.876s；`TestDeleteResponse_DoesNotBlockOnDelivery` 0.29s、`TestComposition_DeleteDeliversBothFacts` 5.24s 含 5s no-dup 窗口）。

---

## 3. 需求规格（机制已落地；此处为模块面验收契约）

### FR-1：WebDAV `DELETE` 产出 = 1 条 audit 行 + 恰 2 条版本化 outbox 事实（与删除单事务原子），响应与投递零耦合

- `davFS.RemoveAll`（`dav.go:141-147`）对**已存在**对象恒走 `svc.Delete(hard=true)`：HTTP 响应 ∈ {204, 200}；同事务提交 `audit_log` 1 行（`Actor` 空串合法）+ `vault.file.deleted@1.1` 1 行 + `vault.file.notify@1.1` 1 行（`event_outbox`，`schema_version:"1.1"`）。
- 对**不存在**对象：HTTP 404（`os.ErrNotExist`），**零** audit 行、**零** outbox 事实（`TestFileServiceDelete_WritesAuditRow` 的 missing 子测已证 service 面；模块面同样成立——`GetObject` 未命中即返回）。
- 事务内任一失败（事实校验、约束冲突、并发双删零行）→ 整体回滚：对象行不删、audit 不落、事实不落（`TestDeleteObjectWithAudit_OneTx` 强制回滚子测已证）。
- **durable_async（不变量）：** relay 是 `cmd/server/workers.go:158-191` 启动的独立 goroutine（常开，`NewEventOutboxRelay` 在 `startEventOutboxRelay`）；删除事务只插行，不调用任何投递；WebDAV DELETE 响应与投递进度**零耦合**。

### FR-2：@1.1 版本化 schema（自包含、字节稳定）+ 插入期校验拒绝畸形载荷

- `deleted@1.1` envelope 必含（`payload.go:33-52` 字段序固定）：`schema_version:"1.1"`、`event_type:"vault.file.deleted@1.1"`、`tenant`、`bucket`、`key`、`object_id`、`version_id`、`size`、`etag`、`backend`、`request_id`、`actor`（`reason` omitempty）；**无** `records`（非 S3 通知形状）。
- `notify@1.1` 自包含（`payload.go:54-106`）：上述公共字段 + `records[0]` 完整 S3 通知块（`eventVersion:"2.1"`、`eventSource:"aws:s3"`、`eventName:"s3:ObjectRemoved:Delete"`、`s3.bucket.arn`、`s3.object.{key,size,eTag,versionId,sequencer}`）；`sequencer` 每次删除新生成（`newSequencer`，`crypto/rand` 16B hex，**非** `obj.ID`——`RestoreObject` 复用行 id，D6）；`signature` omitempty（quarantine 用）。
- **畸形载荷拒绝（插入期，事务内）：** `validateOutboxFacts`（`event_outbox.go:61-83`）要求 event_type ∈ {deleted@1.1, notify@1.1}、`OriginID>0`、tenant 非空、payload ∈ 1..1MiB、`validOutboxPayload`（:85-92）JSON 可解析且 `schema_version=="1.1"`；失败 → 整个删除事务回滚。
- `repository.Event` 结构体**不加** version 字段（方向文原文"@1.1 必须加到 envelope"；遗留 `object_events`/SSE 流不动）。

### FR-3：durable_async 投递（claim→deliver→complete；崩溃重投；exactly-once 仅在 complete 之后）

- relay（`event_outbox_relay.go`）claim 谓词 = billing 形状（pending+到期 OR inflight+租约过期，`event_outbox.go:251-264`）；`deleted@1.1` → AuditSink（`DeliverDeleted`，L2 未配置/未绑定 → complete + 记录保留，L0 权威）；`notify@1.1` → `deliverNotify`（投递时重解析规则 → `postEventTo` 字节原样 POST 每个匹配目标，目标失败重试整条事实）。
- 退避 + 抖动（`eventOutboxBackoff`：1s 基、2×、5min 封顶、[0.75,1.0) jitter），`maxAttempts` 到顶 → 终态 `failed`；401/403 → 立即终态（无刷新路径，H2）；claim-lost → warn + 计数，**不循环重试**（租约重 claim 是恢复机制）。
- **at-least-once 窗口语义（明确契约）：** complete 前崩溃/租约过期 → 重 claim 重投（接收方幂等，S3 等价）；exactly-once **仅**在 complete 之后成立（`event_outbox_delivered` 同事务写入）。
- 投递失败**不影响** L0（audit 已同事务落库）与其它已 claim 事实（per-fact goroutine）。

### FR-4：L0/L1/L2 三级适配器**均可经配置选择，无需代码改动**

- **L0（本地 audit_log）**：常开，无开关（审计是基线义务，非 opt-in）。
- **L1（协议面）**：① webhook worker（`EVENTS_WEBHOOK_URL` + `EVENTS_WEBHOOK_SECRET`，HMAC-SHA256，bus 路径，durable retry + DLQ）；② S3 通知规则（bucket notification rules → relay 投递 notify@1.1 字节原样）。二者互不依赖，现有配置选择。
- **L2（governance）**：`AUDIT_SINK_L2_ENDPOINT` + `AUDIT_SINK_L2_BINDINGS_FILE`（每租户 Bearer token）→ `NewAuditSinkL2` 注入 relay（`cmd/server/workers.go:166-176`）；空 endpoint → relay 照常 complete（降级为记录保留）。**已验证实现形状：** 复用 `event_outbox`（0041 迁移）+ AuditSink 端口，`audit_governance_outbox` 机制本身（legacy governance 面）不动——方向文 acceptance 的"L2 用既有 outbox 机制"以等价 claim/ack 机器满足，意图（durable、exactly-once、可配置）不变。
- **不变量：** 核心代码（service/events/repository）零 sibling 导入；L2 适配器仅经配置接入。

---

## 4. 验收标准（方向 3 条 acceptance 逐条保留并映射为可执行测试）

> 测试基建：service/repo 层用 `repository.Open("sqlite", "file:…")` + `Migrate`（`event_outbox_test.go` 先例）；模块面用 `dav_test.go` 的 `newTestServerWithSvc`（已有 repo handle 返回，L0 断言可直接做）+ 扩展 relay harness（镜像 `startFullServerWithRelay`，`fullserver_test.go:55-57`）。
> 标注：✅ = 已有测试覆盖（测试名 + 位置）；🟥 = 本规格新增测试（G1/G2/G3）。

### AC-1（方向 acceptance ①unit）删除产生恰 1 条 deleted@1.1 + 1 条自包含 notify@1.1；schema 校验拒绝畸形载荷

**已覆盖（✅）：**
- 版本化字段 + 字节稳定性：`TestEventSchema_GoldenJSON`（`schema_test.go:31`，golden 常量 :15-17）、`TestEventSchema_RequiredFields`（:42）、`TestEventSchema_Deleted11Envelope`（:96，断言 `schema_version/event_type/tenant/bucket/key/object_id/actor` 存在且类型正确、无 `records`）、`TestEventSchema_SequencerUniquePerCall`（:132）。
- 单事务原子 + 畸形载荷回滚：`TestDeleteObjectWithAudit_OneTx`（`event_outbox_test.go:71`，非法 event_type / 超 1MiB payload → 整体回滚零落库）、`TestDeleteObjectWithEvent_OneTx`（:136）。
- service 面 1 行 audit + 1 条 deleted@1.1：`TestFileServiceDelete_WritesAuditRow`（`file_delete_test.go:15`，hard/soft 各一、missing 零行、actor 空串合法）。

**🟥 G1（新增，模块面）：** `internal/api/webdav/dav_audit_test.go`
```go
func TestWebDAVDelete_CommitsAuditAndBothFacts(t *testing.T) {
	// harness: newTestServerWithSvc(t)（既有 :43-70，返回 repo handle）
	// 1) PUT /webdav/gone.txt → DELETE /webdav/gone.txt（204/200）
	// 2) 断言（经 repo，无 relay 参与——事务提交即可见）：
	//    a. audit_log 恰 1 行：Action 含 "delete"、Detail=="hard"、Target=="default/gone.txt"、
	//       TenantID=="default"、Actor==""（harness 无 principal，合法空串）
	//    b. event_outbox 中 vault.file.deleted@1.1 恰 1 行：payload 含
	//       "schema_version":"1.1"、"event_type":"vault.file.deleted@1.1"、"object_id":<obj.ID>
	//       （= repo.GetObject 于删除前取的 ID）、"actor":""、"request_id":""、"bucket"/"key"
	//    c. event_outbox 中 vault.file.notify@1.1 恰 1 行：payload 含
	//       "records":[{"eventName":"s3:ObjectRemoved:Delete", …自包含 size/eTag/versionId/sequencer}]
	// 3) DELETE 不存在对象 → 404；audit_log 零新增、event_outbox 零行
	// 4) 对象 GET → 404（既有 TestDeleteRemovesResource 语义保留）
}
```

### AC-2（方向 acceptance ②outbox delivery）commit 后杀 relay、重启，事件仍 exactly-once 投递（claim/ack），且不阻塞 DELETE 响应

**已覆盖（✅，组合面经 REST）：**
- 时序（响应不等待投递）：`TestDeleteResponse_DoesNotBlockOnDelivery`（`fullserver_test.go:685`，信号式：L2 目标挂起期间 DELETE 已返回；恢复后下一轮投递 → `delivered`）。
- 崩溃重投（进程重启模型）：`TestComposition_MidClaimRestartRedeliversOnce`（:1015，服务器 A commit 后关 repo → 事实保持 `pending`；服务器 B 同 DSN 重开 relay → 各自**恰 1 次** delivered；D2 见证：A 存活期零 POST）。
- 租约/claim 语义：`TestEventOutboxClaimLeaseExpiryRedelivers`（`event_outbox_test.go:259`）、`TestOutboxRelay_ClaimLostLeadsToReclaimNotDoubleSchedule`（`event_outbox_relay_test.go:229`）、`TestOutboxRelay_RetriesOn5xx`（:181）、`TestOutboxRelay_L2UnauthorizedFailsImmediately`（:609）、`TestEventOutboxRetryBackoffAndTerminalFailed`（`event_outbox_test.go:300`）。

**🟥 G2（新增，模块面时序）：** 上述机制的共享 relay 已被 REST 面证明；模块面补一条**同一形状的 WebDAV 变体**（防未来 adapter 改动引入阻塞/绕路）：
```go
func TestWebDAVDelete_ResponseDoesNotBlockOnDelivery(t *testing.T) {
	// harness 扩展：newTestServerWithRelay(t, &events.EventOutboxRelayOptions{…, AuditSink: sink})
	//   （镜像 fullserver_test.go:55-57 的 startFullServerWithRelay；L2 目标 = 可挂起 httptest，
	//    release channel + 挂起守卫 < relay HTTP timeout 默认 5s）
	// 1) PUT → DELETE /webdav/k（挂起守卫打开、release 未关闭期间）→ 断言响应已返回
	// 2) event_outbox 中 deleted@1.1 行 status ∈ {pending, inflight}；audit_log 行已存在（L0 不受影响）
	// 3) close(release) → 轮询 ≤15s → 两条事实 status=="delivered"，L2 恰 1 次 POST
	//    （X-Audit-Fact-Id echo；5s no-dup 窗口后计数器不变——镜像 TestComposition_DeleteDeliversBothFacts 断言形状）
}
```

### AC-3（方向 acceptance ③e2e）L0 写 audit 行；L1 webhook 收 HMAC POST；L2 governance relay 用既有 outbox 机制；三者均可选、无需代码改动

**L0 —— 已覆盖（✅ service 面）：** `TestFileServiceDelete_WritesAuditRow`（AC-1）；模块面由 **G1** 补上。
**L1 —— 已覆盖（✅ 组件面 + 组合面）：** HMAC 签名投递 `TestWebhookDeliverWithHMAC`（`webhook_test.go:150`，`X-Aero-Signature: sha256=…` 校验）、`TestWebhookDeliver_SendsEventIDHeaderWithHMAC`（:200）；notify@1.1 经 relay 字节原样投递 + 恰好一次：`TestComposition_DeleteDeliversBothFacts`（`fullserver_test.go:876`，D2 bus 去重 + 5s no-dup 窗口 + row↔wire 字节恒等）。
**🟥 G3（新增，模块面 L1/L2 组合）：** `internal/api/webdav/dav_audit_test.go`
```go
func TestWebDAVDelete_CompositionL1L2(t *testing.T) {
	// 1) L1 webhook：harness 挂 Bus + events.NewWebhook(url).WithSecret(secret) 订阅；
	//    PUT → DELETE /webdav/k → webhook 目标收到 1 次 HMAC POST（签名校验通过，
	//    body.type=="deleted" 遗留形状——L1 webhook 面语义）
	// 2) L1 通知规则：setDeleteRule(repo, notifyURL)（镜像 fullserver_test.go:876 的 FM-7 前置：
	//    规则必须先于 DELETE 存在）→ relay 投递 notify@1.1 字节原样（无 HMAC，S3 形状——契约内）
	// 3) L2：NewAuditSinkL2(httptest URL, {"default": token}) 注入 relay →
	//    DELETE → L2 收到恰 1 次 POST：Authorization: Bearer <token>、
	//    X-Audit-Fact-Id == 行 id、body 含 "event_type":"vault.file.deleted@1.1" + object_id
	//    （2xx+echo 才 complete——receipt 契约）
	// 4) 未绑定租户（tenant "other"）→ L2 零 POST、事实仍 complete（ErrSinkNotBound 降级）；
	//    L0 audit_log 照常（AC-1）
}
```
**可选择性（无代码改动）—— ✅ 已验证 + 🟥 文档补丁：**
- 装配证据：`cmd/server/workers.go:158-191` —— relay 常开；`cfg.AuditSinkL2.Endpoint != ""` 才构造 sink（nil → complete-only）；`EVENT_OUTBOX_*` 配置 `internal/config/config_event_outbox.go` + `docs/configuration.md:354-358`；`AUDIT_SINK_L2_*` 配置 `internal/config/config_audit_sink_l2.go`（endpoint 校验 + bindings 文件 token 卫生 H2/H4）。
- **🟥 文档漂移修复：** `docs/configuration.md` 补 `AUDIT_SINK_L2_ENDPOINT` / `AUDIT_SINK_L2_BINDINGS_FILE` 行（当前零命中，运维侧"L2 可选"证据不全）——唯一非测试改动。

### AC-4 既有行为不回归

- `go test ./internal/api/webdav/ ./internal/events/ ./internal/repository/ ./internal/service/ ./internal/integration/` 全绿；`make check` 全绿（新增文件 ≤500 行；纯 stdlib，I6）。
- WebDAV 既有语义不变：DELETE 404 映射（`RemoveAll` → `os.ErrNotExist`）、MOVE copy-then-delete 回滚（`TestMoveRollbackOnDeleteFailure` `dav_test.go:863`）、tenant 隔离（`TestTenantIsolation` :455）。
- `s.emit`/`Bus.Publish` 签名与"错误吞掉"语义不变；`object_events`/SSE 回放路径不变；`auditgovernance` 机制零改动；`DeleteVersion`/delete-marker/quarantine（E14）保持 bus 路径（D2 去重不覆盖它们——`notifier.go:71-76` 注释明确）。

---

## 5. 范围边界（明确不做）

| 不做 | 理由 |
|------|------|
| WebDAV **MOVE 假删除信号抑制**（rename 产生 deleted@1.1 + audit 行） | 方向 3（`dav.go:150-206` copy-then-delete 是既有语义，回滚测试已钉死）；抑制属 sibling 方向，不在本方向 acceptance 内 |
| share/version/RAG 引用失效、chunk 失效 | 方向 3 |
| admin 操作审计（`admin.go` 的 best-effort `RecordAudit`）outbox 化 | 方向 3；删除审计已走事务路径，admin 面是另一面 |
| `repository.Event` 加 version 字段 / `object_events` schema 改造 | 方向文原文"@1.1 必须加到 envelope"——版本化在 outbox 载荷（已落地）；遗留流兼容 |
| `auditgovernance` 机制改动（绑定表/revision/draining/脱敏形状） | 既有 governance 是租户绑定 + 脱敏面；L2 是新端口面（E7 修正点） |
| webhook 管线 / `webhook_failures` / DLQ 改造 | 既有 durable retry；L2 是独立投递面 |
| L2 绑定表的持久化/管理 API | 配置驱动（bindings 文件）已满足 acceptance；动态管理属后续方向（届时 0042 迁移双文件对，I2） |
| `notify@1.1` 通知目标加 HMAC | S3 通知形状契约内无签名（`postEventTo` 共享函数既有语义）；webhook worker 面（HMAC）不动 |
| actor 身份管线 | actor 取 `access.PrincipalFrom(ctx)`，空值合法（WebDAV harness 无 principal） |

---

## 6. 实现指引（供验收后落地，非本规格交付物）

- **测试（主要交付，2 个新文件 + harness 扩展）：**
  - `internal/api/webdav/dav_audit_test.go`（≤500 行）：G1（`TestWebDAVDelete_CommitsAuditAndBothFacts`）、G2（`TestWebDAVDelete_ResponseDoesNotBlockOnDelivery`）、G3（`TestWebDAVDelete_CompositionL1L2`）。
  - harness 扩展：`dav_test.go` 的 `newTestServerWithSvc` 旁新增 `newTestServerWithRelay(t, relayOpts)`，镜像 `internal/integration/fullserver_test.go:55-57`（`startFullServerWithRelay`：短 poll + 可选 AuditSink + 可选 Bus/Webhook 订阅）；不改既有 `newTestServer` 调用点（AC-4 回归面）。
  - 断言辅助：`assertAuditRowFor`/`outboxStatus`/`outboxPayload` 形状从 `fullserver_test.go` 复制到 webdav 测试包（跨包不共享，避免耦合 integration 包）。
- **文档（唯一非测试改动）：** `docs/configuration.md` 补 `AUDIT_SINK_L2_ENDPOINT`/`AUDIT_SINK_L2_BINDINGS_FILE` 两行（含 bindings 文件 JSON 形状与 token 卫生约束，对齐 `config_audit_sink_l2.go` 注释）。
- **生产代码：无预期改动**（方向机制已全部落地并实测；若 review 发现缺口，先回到 §2/§3 证据复核）。
- **提交前：** `gofmt -l` 无输出、`go vet ./...`、`go test ./internal/api/webdav/ -count=1`、`go test ./internal/integration/ -run 'TestComposition|TestDeleteResponse' -count=1`、`make check`。
