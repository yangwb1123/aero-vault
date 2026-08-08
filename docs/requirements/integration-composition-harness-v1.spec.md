# 方向：组合画像 harness（startFullServer）与管理端删除 e2e —— 验收规格 · 已验证现状

> **模块：** `internal/integration`（+ `internal/service` 单元腿 · `internal/api/rest` admin 路由 · `internal/access` 失效机制 · `cmd/server` 装配对照）
> **来源分析：** `docs/auto/analyses/internal-integration-7479f0a2.json`（方向 #1）· **日期：** 2026-08-06 · **HEAD：** `acfaaf4`
> **评分：** 价值 10 / 风险降低 9 / 工作量 8 / 置信度 8
> **状态声明：** 方向问题陈述**大部分已过时**——自分析快照后，admin 文件删除路由（`DELETE /v1/admin/files/{tenant}/{key}`）、share/ACL/public-asset 同事务失效、outbox 双事实（deleted@1.1 + notify@1.1）、fail-closed 授权、CLI `admin files delete` 均已合入并有测试。本文逐条核验引证（§1）、登记已验证偏差（E8/E9/E6）、把方向四条验收检查**原样保留**并映射到已存在测试与剩余缺口 G1–G5（§3/§4）。**超范围项一律不做**（§5）。
> **前置文档：** `docs/requirements/transactional-outbox-delete-events-v1.spec.md`（同特性族的 outbox/relay/payload 验收契约；本文只引用不重述其 FR-1–FR-4 机制）。

---

## 1. 证据核验（方向引证逐条对照当前 HEAD）

| # | 方向引证（分析时行号） | 当前 HEAD 位置 | 核验结论 |
|---|----------------------|----------------|---------|
| E1 | `internal/integration/fullserver_test.go` `startFullServer` 只装配 MVP：`service.NewFileService(store, repo, logger)`，无 WithAuthorizer/WithEventSink/WithChunkCleaner/auditgovernance.Runtime | `startFullServerOpts` :72-178（`startFullServer` :50 / `startFullServerWithRelay` :57 / `startFullServerWithAuthAndRelay` :65 三个构造器共用同一函数体）；`svc` 装配 :92（`WithAuthorizer(allowAllProvider{})`）、`bus`/`WithEventSink` :97/:104、`NewNotifier` + 订阅 :105-113、可选 `EventOutboxRelay` :163-172、auth 注册表变体（"opsecret:*:admin"） | ⚠️ **部分过时：** authorizer（测试替身 `allowAllProvider`）、EventSink、bus、Notifier、relay、auth 注册表**已装配**。**仍缺：** 真实 `access.Manager`、`auditgovernance.Runtime`、`WithChunkCleaner`、AI/indexer（G4） |
| E2 | `TestFullServer_REST_CRUD`、`TestFullServer_ProtocolInterop` 存在 | `fullserver_test.go:204-265`、:452-505 | ✅ 存在，行号漂移，语义一致（REST CRUD + REST→S3→MCP 互操作） |
| E3 | `internal/service/file_delete.go:80/145` `ChunkCleaner.DeleteObjectChunks` | `hardDeleteObject` :18-56（chunk 清理循环 :24-31，逐非 marker 版本）；`softDeleteObject` :76-93（:82-89）；`DeleteVersion` :174-218（:197-204） | ✅ 行号漂移（80→24-31、145→197-204），语义一致：**非致命**（失败仅 `logger.Warn`，不阻断删除，AGENTS.md §2.1③） |
| E4 | `internal/service/delete_marker.go:54` chunk 清理 | `CreateDeleteMarker` :48-52（当前对象存在且非 marker 时 `DeleteObjectChunks(current.ID)`） | ✅ 行号漂移（54→48-52），语义一致 |
| E5 | `internal/service/file.go:63-64,98`（ChunkCleaner、WithAuthorizer） | `EventSink` :63-64；`ChunkCleaner` 接口 :88-92；`FileService` 字段 `chunkCleaner` :89；`WithChunkCleaner` :147-152（nil 入参清除钩子）；`WithAuthorizer` :97-100（nil 保持 CI 基线） | ✅ 行号漂移，语义一致 |
| E6 | `internal/access/shares.go` 有 share store、**无删除路径失效（proposed）** | `Manager.CreateShare/ListShares/RevokeShare/ResolveShare/PublishAsset/...`（:23-252）确无对象删除钩子；**但**失效已实现于仓库删除事务：`deleteObjectAccessState`（`internal/repository/sql_access_cleanup.go:27-44`，含 `deleteObjectCapabilities` :12-25 → `DELETE FROM shares` + `DELETE FROM public_assets` + `DELETE FROM resource_acls`），由 `HardDeleteObjectWithEvent` 在事务内调用（`event_outbox.go:124`，软删 :168、DeleteObjectVersion :217 同路径） | ❌ **proposed 论断过时：** share 失效已实现且**同事务**（AC-1 的 "share rows removed" 已有断言，见 §3 AC-1）。Manager API 无删除钩子仍成立，但不需要新增 |
| E7 | `cmd/server/main.go:81 WrapRepository`、`:94 WithAuthorizer`、`:215 WithEventSink` | `auditgovernance.WrapRepository` :81；`WithAuthorizer(accessManager)` :94；`WithEventSink(bus)` :93；服务装配块 :90-102；第二条装配线 :191/:216（admin CLI 路径）；`buildAccessManager` 定义于 `cmd/server/access.go:11-25`（repo 须实现 `access.Store`；`access.Config{Enabled, DefaultPolicy, ShareSecret, DeleteFailOpen}`） | ✅ 行号精确（215→216 微漂）。**生产装配范式即 G4 harness 升级的对照物** |
| E8 | `internal/api/rest/router.go:187-195` admin 组仅 keys/jwt/tenants/config/webhook-failures，**无 admin 文件删除路由（verified）** | admin 组 :185-220；**`DELETE /v1/admin/files/{tenant}/{key}` 已注册**：openapi 条目 :203（AdminOnly, 204）、路由注册 :352（`r.Delete("/admin/files/{tenant}/*", adm.DeleteFile)`）；handler `internal/api/rest/admin_files_delete.go:20-45`（`requireAdmin` + tenant 必填（F13，拒绝空 tenant 归一化）+ `?hard=1` → `svc.Delete` 同路径）；CLI `admin files delete` 存在 | ❌ **"无 admin 删除路由" 论断过时：** 路由已存在且已由 `TestAC2_AdminDelete_EventTypeFilteredState` / `TestComposition_AdminFilesDeleteEndToEnd` 覆盖（`admin_files_delete_test.go`）。剩余缺口不含路由本身（§5） |
| E9 | （补充核验）outbox 表名：方向验收写 `audit_governance_outbox`（assert via AuditGovernanceStore） | 删除事实落 **`event_outbox`**（迁移对 `0041_event_outbox.{up,down}.sql`，双方言）：`HardDeleteObjectWithEvent` 事务内 `insertOutboxFacts`（`event_outbox.go:102-146`）；事件类型常量 `EventTypeFileDeleted11`/`EventTypeFileNotify11`（`repository/event_outbox.go:22-25`）。`audit_governance_outbox`（0039）是**另一条**管线：`auditgovernance.WrapRepository`（`auditgovernance/repository.go:11-51`）只捕获 `RecordAudit`/`InsertEvent` 调用并改写至 `RecordAuditWithGovernance`/`InsertEventWithGovernance`，由 `auditgovernance.Runtime`（`runtime.go:22-`，`New` :54，`Start/Close` :105/:121）relay 到 Snaplink | ⚠️ **表名更正：** 删除路径的 deleted@1.1/notify@1.1 断言目标是 `event_outbox`（harness 已有 DSN 助手 `outboxStatus`/`outboxPayload` `fullserver_test.go` + `outboxCountFor`/`outboxPayloadFor` `authz_parity_test.go:51/:69` + `deliveredCountFor`/`deliveredAt`/`deliveredTotal` `admin_files_delete_test.go:22-71`）。**且**删除事务经直写 SQL（`insertAuditEntry`/`insertOutboxFacts`），**不经过** `RecordAudit`/`InsertEvent` → governance runtime 对删除组合是**旁路**（G4 断言边界由此定，见 §3 AC-4） |

---

## 2. 已落地机制（验收对象，全部实测通过）

- **管理端删除路由：** `DELETE /v1/admin/files/{tenant}/*?hard=1` → `AdminHandler.DeleteFile`（admin_files_delete.go:20）→ `svc.Delete`（与 REST `/v1/files`、S3、WebDAV、MCP 共用同一服务路径）；admin 组要求 `requireAdmin`（scope=admin）。软删/硬删由 `?hard=1` 选择，硬删走 `hardDeleteObject`（file_delete.go:18-56）。
- **单事务原子删除：** `HardDeleteObjectWithEvent`（event_outbox.go:102-146）= legal-hold 检查 + `deleteObjectAccessState`（shares/public_assets/resource_acls 同事务清除，sql_access_cleanup.go:27-44）+ `DELETE FROM objects`（RowsAffected=0 → `ErrNotFound` 回滚，无幽灵事实/审计行）+ `insertAuditEntry`（L0 恒写）+ `insertOutboxFacts`（deleted@1.1 + notify@1.1 两条事实同事务入 `event_outbox`）。
- **Service 侧事实构建：** `deleteAuditEntry`/`deleteFacts`（file_delete.go:100-148）：actor 取 `access.PrincipalFrom(ctx)`（auth 中间件 `auth_middleware.go:183` 写入，API key 的 subject 即 key token）、request_id 取 `middleware.RequestIDFrom(ctx)`（RequestID 最外层中间件）、notify sequencer 每次删除新生成（`events/payload.go:15-22`，不用 obj.ID —— RestoreObject 复用行 id，D6）。
- **ChunkCleaner 钩子：** hard 删逐非 marker 版本（file_delete.go:24-31）、软删（:82-89）、删单版本（:197-204）、delete marker（delete_marker.go:48-52）；失败 warn 不阻断。
- **异步 relay：** `events.EventOutboxRelay` claim→deliver→complete（含 `AuditSinkL2` 回显收据、指数退避、崩溃租约恢复、`event_outbox_delivered` 保真记录）与 Notifier D2 跳过 —— 详见前置 spec FR-3/FR-4，本文不重述。
- **Harness 与既有 e2e：** `startFullServerOpts`（fullserver_test.go:72-178）含 authorizer 替身/bus/notifier/可选 relay/auth 变体；既有组合测试：`TestFullServer_REST_CRUD`/`ProtocolInterop`/`SearchDisabled`（AI off → /v1/search 503 钉死）、`TestDeleteResponse_DoesNotBlockOnDelivery`（信号判别 4s<5s，:702）、`TestComposition_DeleteDeliversBothFacts`（notify 字节原样 + exactly-once，:893）、`TestComposition_MidClaimRestartRedeliversOnce`（崩溃重投）、`TestAC2_AdminDelete_EventTypeFilteredState`（admin 路由 exactly-one pending + payload 字段，admin_files_delete_test.go:112）、`TestComposition_AdminFilesDeleteEndToEnd`（CLI 非阻塞 + relay-down 恢复，:167）、`authz_cli_failclosed_test.go`/`authz_parity_test.go`（deny 零副作用）。

---

## 3. 验收标准（方向原文四条，原样保留并测试化）

### AC-1 unit：FileService 硬删原子性（row + blob + 版本墓碑 + chunks + share）

> 方向原文：*unit: FileService hard-delete test asserts object row + blob + version tombstones + chunks deleted and (proposed) share rows removed*

**现状覆盖：** `TestAdminDelete_InvalidatesShareAndChunks`（internal/service/file_delete_test.go:184-278）已断言：share rows == 0（经 `repo.(access.Store)` cast，同事务失效）、recording ChunkCleaner 收到当前对象 id、blob 消失（`Storage().Stat` → err）、行消失（`GetObject` → `ErrNotFound`）、audit 行存在；软删腿断言 blob 保留 + deleted_at 置位 + share 仍失效。`TestAdminDelete_EmitsExactlyOneDeletedFact`（:156-178）断言两事实可见 + 恰好一条 audit 行。"share rows removed" 已**不是 proposed**（E6）。

**缺口 G1 —— 多版本硬删（版本墓碑/逐版本 blob/逐版本 chunk 现无断言）：**

- 断言 1（版本墓碑）：开启 versioning 的 bucket 上写入 2 个版本 + 1 个 delete marker；`svc.Delete(..., hard=true)` 后 `repo.ListObjectVersions(tenant, bucket, key)` 为空（`DELETE FROM objects` 按 key 全清，含 marker 行）。
- 断言 2（逐版本 blob）：每个**非 marker** 版本的 `StorageKey` 经 `svc.Storage().Stat(ctx, sk)` 返回 error（blob 级删除，storageKey 唯一性 I3）。
- 断言 3（逐版本 chunk）：recording ChunkCleaner（`mockChunkCleaner` 既有模式，file_delete_test.go:199-205）收到的 objectID **集合 == {v1.ID, v2.ID}**（marker 版本不触发 chunk 清理，hardDeleteObject :24-31 的 `IsDeleteMarker` 跳过可被证伪）。
- 断言 4：share rows == 0（跨版本路径复用同一事务，回归锚点）。
- 落点：`internal/service/file_delete_test.go`（新增测试函数，stdlib testing，I6）。

### AC-2 outbox delivery：同事务入队 + 删除先于 relay 返回

> 方向原文：*outbox delivery: same transaction enqueues one vault.file.deleted@1.1 row in audit_governance_outbox (assert via AuditGovernanceStore) and DELETE returns before any relay delivery (latency-bound assertion, durable_async)*

**表名更正（E9）：** 断言目标是 `event_outbox`，经 harness 既有 DSN 助手（`outboxStatus`/`outboxCountFor`/`outboxPayloadFor`/`deliveredCountFor`），非 `audit_governance_outbox`。

**现状覆盖：** 事务性入队（event_outbox.go:102-146）；REST-file 路由非阻塞证明 `TestDeleteResponse_DoesNotBlockOnDelivery`（fullserver_test.go:702-787：信号判别，4s 守卫 < 5s relay 超时，同步实现必挂）；CLI admin 非阻塞 + relay-down 恢复 `TestComposition_AdminFilesDeleteEndToEnd` leg1/leg2（admin_files_delete_test.go:167-360）；REST admin 路由无 relay 时 exactly-one pending `TestAC2_AdminDelete_EventTypeFilteredState`（:112-165）。

**缺口 G2 —— REST admin 路由的延迟界非阻塞证明（当前只有 CLI 版和 file 路由版）：**

- 断言 1：L2 目标挂起（httptest handler 阻塞 + `X-Audit-Fact-Id` 回显收据，复用 `TestDeleteResponse_DoesNotBlockOnDelivery` 模式）→ `DELETE /v1/admin/files/{tenant}/{key}?hard=1`（Bearer opsecret，`startFullServerWithAuthAndRelay`）在 **4s 内返回 204**（判别式：同步实现必然挂到 5s relay 超时之后；守卫严格小于超时，确定性判别）。
- 断言 2：响应时刻 `outboxStatus == pending|inflight`（delivered 不可达）+ `outboxCountFor(deleted@1.1) == 1` 且 `origin_id ==` 对象行 id；`deliveredTotal == 0`。
- 断言 3：释放目标后 ≤15s 内 `deliveredCountFor`（JOIN `event_outbox_delivered` × `event_outbox`，R2 语义）== 1（exactly-once）；5s 无重复窗口计数不变。
- 落点：`internal/integration/admin_files_delete_test.go`（文件现 383 行，若超 500 行门禁则新建 `admin_delete_nonblocking_test.go`）。

### AC-3 event schema：outbox payload 严格 JSON 校验

> 方向原文：*event schema: strict-json validator on the outbox payload (version=1.1, tenant/bucket/key/request_id/actor fields)*

**现状覆盖：** golden 字节钉（`internal/events/schema_test.go:15-51`：字段序固定、`schema_version`/必填字段存在性）+ `assertNotifyContent`（fullserver_test.go:1298-1332：schema/type/tenant/bucket/key/records/sequencer 常量钉）+ `TestAC2` 的 contains 检查（仅 schema_version/tenant/key 三个字段）。

**缺口 G3 —— integration 层严格校验器 + actor/request_id 非空（当前任何集成测试均未断言这两字段）：**

- 断言 1：新增 `strictDeletedPayload(t, payload)` 助手（`internal/integration`）：`encoding/json` `Decoder.DisallowUnknownFields` + 类型化结构体（字段集 = `events/payload.go:33-52` 的 `deletedFact`：`schema_version=="1.1"`、`event_type=="vault.file.deleted@1.1"`、tenant/bucket/key 非空、`object_id>0`、`version_id`/`size`/`etag`/`backend`、`request_id` 非空、`actor` 非空、`reason` 可选）——严格性 = 未知字段拒绝 + 类型/必填/取值三重检查（非仅 contains）。
- 断言 2：经 auth 启用 harness 的真实 admin 路径：payload 中 `actor == "opsecret"`（auth_middleware.go:183 写入 principal）、`request_id` 非空（RequestID 最外层中间件；响应错误体中的 request_id 与 outbox payload 一致为佳，但至少非空）。
- 断言 3：notify@1.1 自包含：`assertNotifyContent` 复用（records[0] 完整 S3 信封 + sequencer `^[0-9a-f]{32}$`）；`signature` 可选字段不破坏严格校验（解码为 omitempty 结构体）。
- 落点：`internal/integration` 助手 + admin 路径测试内调用。

### AC-4 composition e2e：harness 升级 + 四协议 404 + chunk 归零 + notify

> 方向原文：*composition e2e: extend startFullServer with access.Manager + auditgovernance.Runtime + events.Bus + Notifier + ChunkCleaner, then REST-admin delete -> object 404 on REST/S3/WebDAV/MCP, chunk search returns 0 hits, notify endpoint receives self-contained vault.file.notify@1.1 payload*

**现状覆盖：** events.Bus + Notifier + relay 已装配（E1）；notify 端点 exactly-once + 字节原样已有（`TestComposition_DeleteDeliversBothFacts`，但经 `/v1/files` REST 删除，非 admin 路由）；404 仅 REST 路径断言过（`TestComposition_AdminFilesDeleteEndToEnd` leg2）。**缺：** 真实 access.Manager、auditgovernance.Runtime、ChunkCleaner 装配；admin 删除后 S3/WebDAV/MCP 读路径 404；chunk 归零断言。

**缺口 G4 —— harness 装配（对照 main.go:81/94/208-212 与 cmd/server/access.go:11-25）：**

- `access.Manager`：`access.NewManager(repo, access.Config{Enabled: true, DefaultPolicy: access.DefaultTenant, ShareSecret: []byte("<32+ bytes>"), DeleteFailOpen: false})`（repo 已实现 `access.Store`；生产 DefaultPolicy 取自配置）→ `svc.WithAuthorizer(mgr)` 替换 `allowAllProvider`（对齐 main.go:94）。operator key 沿用 `startFullServerWithAuthAndRelay`（"opsecret:*:admin"）→ requireAdmin 与 manager 授权均真实。**新增构造器**（如 `startFullServerComposition(t, relayOpts)`），既有调用点不动（startFullServerOpts 的 authKeys 参数模式已示范加法式演进）。
- `ChunkCleaner`：`svc.WithChunkCleaner(recordingCleaner)`（实现 `service.ChunkCleaner`，记录 objectID 集合 + 计数）——**不启 AI**（I5，CI 基线 AI off；chunk 断言在仓库层，见 G5 断言 2）。
- `auditgovernance.Runtime`：enabled 配置 + repo 作 Store（`runtime.New` :54）+ `WrapRepository(repo, rt)` + `rt.Start/Close`（对齐 main.go:81/208-212）。**断言边界（诚实核验，E9）：** 删除事务经直写 SQL，不经过 `WrapRepository` 的 `RecordAudit`/`InsertEvent` 捕获 → governance runtime 对删除组合是**旁路**，因此装配断言 = ① runtime 存在且 tenant capture 绑定时，admin 删除仍 204、`event_outbox` 事实照常提交（删除不依赖 governance）；② 删除的 `audit_log` 行仍在本地（L0 恒写，不受 governance 影响）。

**缺口 G5 —— 组合测试体（四协议 404 + chunk 归零 + notify exactly-once）：**

- 前置：PUT 对象（opsecret，tenant=default，四协议共用默认租户）+ `setDeleteRule` 预装 notify 规则（FM-7：规则先于删除）。
- 断言 1（四协议读路径 404）：`DELETE /v1/admin/files/default/{key}?hard=1`（204）后——
  - REST：`GET /v1/files/{key}`（`X-Aero-Tenant: default`）→ 404；
  - S3：`GET /s3/default/{key}` → 404（s3compat handler 的 ErrNotFound → 404）；
  - WebDAV：`GET /webdav/{key}` → 404 —— 注意 dav.go:109-121 的目录探针语义：`ErrNotFound` 时先 `List(prefix+"/")`，前缀下无兄弟对象才返回 `os.ErrNotExist`（x/net/webdav 映射为 404）；测试 key 须选**无同前缀兄弟对象**的键（如 `k`，既有测试模式），否则会被当作目录列表返回 200；
  - MCP：`tools/call read_file`（key 同上）→ JSON-RPC 错误结果（server.go:238-251 `errResult`，非成功 content）。
- 断言 2（chunk 归零）：`repo.ListChunksForObject(ctx, obj.ID)` == 0（`chunks` 表，迁移 0004）+ recordingCleaner 收到全部非 marker 版本 id。**"chunk search returns 0 hits" 的 CI 门内可测读法**：基线 AI off（I5，`TestFullServer_SearchDisabled` 钉 /v1/search → 503），故以仓库层 chunk 归零 + cleaner 全量触发作为不变式；AI-on 搜索腿超范围（§5）。
- 断言 3（notify exactly-once + 自包含）：notify 目标收到**恰好 1 次** POST；wire body **字节等于** `outboxPayloadFor(notify@1.1)`（verbatim，投递不重派生）+ `assertNotifyContent`（自包含常量钉）；deleted@1.1 `deliveredCountFor == 1`；5s 无重复窗口（沿用 TestComposition_DeleteDeliversBothFacts 模式）。
- 落点：`internal/integration`（新测试函数；若 fullserver_test.go 逼近 500 行门禁则置 `admin_delete_composition_test.go`）。

---

## 4. 缺口汇总

| ID | 缺口 | 落点 | 关键断言 |
|----|------|------|---------|
| G1 | 多版本硬删单元断言（版本墓碑/逐版本 blob/逐版本 chunk） | `internal/service/file_delete_test.go` | `ListObjectVersions` 空；每非 marker 版本 `Storage().Stat` err；cleaner 调用集合 == {v1.ID, v2.ID}；share rows 0 |
| G2 | REST admin 路由延迟界非阻塞证明（信号判别 4s<5s） | `internal/integration`（admin 删除测试） | 204 ≤4s；响应时 pending\|inflight、count==1、deliveredTotal 0；恢复后 JOIN 计数 ==1、5s 无重复 |
| G3 | 严格 JSON 校验器 + actor/request_id 非空 | `internal/integration` 助手 + admin 测试 | `DisallowUnknownFields` + 类型化必填字段；actor=="opsecret"；request_id 非空 |
| G4 | harness 装配：真实 access.Manager + WithChunkCleaner(recording) + auditgovernance.Runtime（WrapRepository/Start/Close） | `internal/integration/fullserver_test.go`（新增构造器） | 装配后 admin 删除仍 204 + 事实照常提交 + 本地 audit 行在（governance 旁路不变量） |
| G5 | 组合测试体：四协议 404 + chunk 归零 + notify exactly-once | `internal/integration` | REST/S3 404、WebDAV 404（无同前缀兄弟对象的 key，dav.go:109-121 目录探针语义）、MCP read_file 错误；`ListChunksForObject` 0 + cleaner 全量；notify 1 次字节原样 + assertNotifyContent |

---

## 5. 超范围（不实现）

- **不改 schema/迁移/事件类型**（I2 双迁移契约；`event_outbox` 0041 与 payload 结构已由前置 spec 验收）。不加新路由（admin 删除路由已存在，E8）。
- **不改 relay/outbox/Notifier 机制**（前置 spec FR-3/FR-4 已验收；G2/G5 只断言，不实现）。
- **不把 AI 索引/embedder 接入 harness**（I5：CI 基线 AI off；chunk 断言限仓库层；`TestFullServer_SearchDisabled` 的 503 钉保持不变）。
- **不加 Postgres 腿**（`-tags=integration` 既有覆盖不动）、不加新配置项、不加新 env。
- **不改 share 表结构/Manager API**；share 失效留在仓库删除事务内（E6，现状即契约）。
- **不新增断言框架**（I6：stdlib `testing` + `encoding/json` 的 `DisallowUnknownFields` 即可实现 G3 严格校验）。

---

## 6. 验证

- **门禁：** `make check` 全绿（gofmt / go build / go vet / go test；新增/改动文件 ≤ 500 行）。
- **定向：**
  - `go test ./internal/service/ -run 'TestAdminDelete' -v`（G1，含既有 TestAdminDelete_* 回归）
  - `go test ./internal/integration/ -run 'TestComposition|TestAC2|TestDeleteResponse|TestFullServer' -v`（G2/G3/G5 + 既有组合回归）
  - `go test -race ./internal/integration/`（G2/G5 新增 goroutine 腿：relay goroutine 与阻塞 L2 的清理顺序须沿用 release→close 的 LIFO 模式）
- **回归锚点（不得变红）：** `TestFullServer_REST_CRUD`/`ProtocolInterop`/`SearchDisabled`、`TestDeleteResponse_DoesNotBlockOnDelivery`、`TestComposition_DeleteDeliversBothFacts`、`TestComposition_AdminFilesDeleteEndToEnd`、`TestAC2_AdminDelete_EventTypeFilteredState`。
