# 方向：`admin files delete` CLI 面 —— 面向运维的跨租户管理删除入口（`internal/cli` + REST admin 路由）

> **模块：** `internal/cli`（+ 最小 REST 路由面 `internal/api/rest/admin.go`）· **来源分析：** `docs/auto/analyses/internal-cli-17314662.json`（方向 1/3）· **日期：** 2026-08-06
> **评分：** 价值 9 / 风险降低 8 / 工作量 6 / 置信度 8
> **本文所有代码引用均已对照仓库逐行验证**（HEAD `acfaaf4`；`make check` 基线：SQLite + local FS + AI off）。
>
> **修订记录（验证后回写）：**
> - **方向文声称「`rm` 是唯一删除路径且永不触硬删」不精确**：REST `DELETE /v1/files/{key}?hard=1` 已支持硬删（`handler.go:238-252`），但 CLI 的 `rm` 不发该参数，且两路径均非 admin 面、无显式 tenant 参数。本方向的缺口 = **admin 面 + 显式 tenant + `--hard` 可达**，而非「服务层无硬删」。
> - **验收中的「audit outbox 表 + status pending→delivered」指向 `event_outbox`（迁移 0041），不是 `audit_governance_outbox`（0039）**：0039 无 status 列（仅 `delivered_at_ns`）；0041 才有 `status CHECK ('pending','inflight','delivered','failed')`。且一次删除经 `deleteFacts` 写入**两条**事实（`deleted@1.1` + `notify@1.1`），「恰好一行」必须以 `event_type='vault.file.deleted@1.1'` 过滤。
> - **验收中的「envelope 携带 share+version+RAG-reference invalidation flags」不存在于 `deleted@1.1` schema**：envelope 字段为 `schema_version/event_type/tenant/bucket/key/object_id/version_id/size/etag/backend/request_id/actor/reason(omitempty)`（`payload.go:29-49`）。实际失效机制是**行为而非 flag**：share/public_assets/resource_acls 行在删除事务内删除（`sql_access_cleanup.go`）、RAG 引用经 `ChunkCleaner.DeleteObjectChunks` 同步钩子失效（`file_delete.go`）。验收按行为断言保留，不改 schema（I5/向后兼容）。
> - **验收的 4 项检查全部保留**，落为 AC-1..AC-4（§4）；其中 outbox/relay/schema 生产机制**已全部落地**（0041 + `deleteFacts` + `EventOutboxRelay`），本方向不新增 outbox 生产代码，仅新增 CLI/REST 入口 + 验收测试。

---

## 1. 问题陈述

COMPOSE-2026-017 要求「管理员文件删除」为一等能力，但运维侧的 admin 客户端 CLI 没有 admin 文件资源：

- `cmdAdmin`（`internal/cli/cli_admin.go`）的资源 switch 只分发 `keys / tenants / jobs / audit / buckets`——**无 `files`**；`adminUsage()` 无对应条目。
- CLI 唯一删除命令 `rm <key>`（`cli_crud.go` `cmdRemove` → `DELETE /v1/files/{key}`）是**非 admin 软删**：不显式携带 tenant（租户来自 `X-Aero-Tenant` 头，缺省 `default`）、无 `--hard` 参数，且不构成 operator-facing 的管理入口。
- REST `AdminHandler`（`internal/api/rest/admin.go`，`NewAdminHandler` 持有 `svc *service.FileService`）**无任何文件删除端点**——admin 路由组（`router.go:329-352`）只有 tenants/keys/jobs/audit/config/buckets/departments。

服务层能力**已齐备**（这是本方向低工作量的根因）：`FileService.Delete(ctx, tenant, bucket, key, hard bool)`（`internal/service/file_delete.go:147`）一条路径覆盖软删/硬删 + 删除事务内原子写 `audit_log` 行（`AuditActionFileDelete="file.delete"`，`audit.go:13`）+ 版本化 outbox 事实（`deleteFacts` → `vault.file.deleted@1.1` + `vault.file.notify@1.1`）+ share/ACL/public-asset 失效 + `ChunkCleaner` RAG 引用清理 + WORM/保留锁检查 + 配额扣减。缺的只是**入口**：CLI 命令 + 一条 admin REST 路由，二者都薄（协议适配层，业务逻辑全在 FileService，符合 AGENTS.md §2.1/§2.2）。

### 触发场景（真实工作流）

1. 运维收到合规删除令（对象含 PII / 违规内容），需要**跨租户、带审计**地删除 `tenant=acme, key=docs/a.txt`。
2. 今日只能 `AERO_TENANT=acme aero-vault cli rm docs/a.txt`——软删、无显式租户参数（凭 env 头）、无 hard 选项、无 admin 权限门禁；或手工 `curl -X DELETE /v1/files/...?hard=1`（非 admin 面）。
3. 期望：`aero-vault cli admin files delete acme docs/a.txt --hard` 一条命令完成：元数据 +（可选）对象状态删除、审计行落 `audit_log`、`vault.file.deleted@1.1` 进 outbox、share/RAG 引用失效；relay 宕机也不阻塞删除（durable_async 已保证）。

---

## 2. 现状与代码证据（已逐条验证）

| # | 证据 | 验证结果 |
|---|------|---------|
| E1 | `internal/cli/cli.go` — `cliHandlers` map（init 内注册 `"admin" → c.cmdAdmin` 等 14 个命令）；`usage()` 列出全部命令含 `admin …` 各子命令；`Run` 未知命令返回 2 | ✅ 与引用一致（map 与 usage 均存在） |
| E2 | `internal/cli/cli_admin.go` — `cmdAdmin` 资源 switch：`keys/tenants/jobs/audit/buckets`，**无 `files`**；default 分支「unknown admin resource」+ `adminUsage()` 返回 2；`adminUsage()` 无 files 条目 | ✅ 与引用一致；`cmdAdminAudit` → `GET /v1/admin/audit?limit=N`，打印响应体原文（无解析） |
| E3 | `internal/cli/cli_crud.go` — `cmdRemove`：`DELETE /v1/files/`+`escapeKey(key)`，无 hard 参数（`?hard=1` 不可达）；`escapeKey` 按 `/` 分段逐段 `url.PathEscape` | ✅ 与引用一致；补充验证：REST `Handler.Delete`（`handler.go:238-252`）读 `?hard=1` → `svc.Delete(…, hard)`，即服务层硬删已存在，缺口在 CLI/admin 面 |
| E4 | `internal/api/rest/admin.go` — `NewAdminHandler(svc, repo, reg)`（:34）；全部 handler 顶部 `requireAdmin`（:457）；`audit()`（:410）best-effort `RecordAudit`；`ListAudit`（:433）→ `repo.ListAudit` 读 `audit_log`。**无文件删除端点** | ✅ 与引用一致（`AdminHandler` 持 `svc`，可直接调 `svc.Delete`） |
| E5 | `internal/service/file.go` — `emit()` 在 **:297**（精确）；`WithChunkCleaner` 在 **:137**（精确）；`checkedObjectDefaults`（:274）补 tenant/bucket 缺省 + key 校验 | ✅ 行号精确 |
| E6 | `internal/repository/repository.go:193` — `EventDeleted EventType = "deleted"`（精确；:191-194 为 created/updated/deleted/accessed） | ✅ 行号精确 |
| E7 | `internal/cli/cli_admin_test.go` — httptest 模式：`newTestClient(t, ts)` + `captureStdout/captureStderr`，断言 method/path、退出码 0/1/2（如 `TestCmdAdminKeys_Revoke_Success`、`TestCmdAdminTenants_Delete_Success`）；`cli_test.go` 提供 `newTestClient`/`captureStdout`（:26）/`captureStderr`（:47）/`newTestClient`（:65） | ✅ 与引用一致，可镜像 |
| E8 | 服务层删除全链路（**本方向的生产依赖，已落地**）：`file_delete.go:147` `Delete(ctx, tenant, bucket, key, hard)`；`:18-55` `hardDeleteObject`（storage 先删 blob → `HardDeleteObjectWithEvent` 单事务删行+审计+outbox → 配额 → emit）；`:76-101` `softDeleteObject`；`:105-124` `deleteFacts` 构建 `deleted@1.1`+`notify@1.1`；`:26-33`/`:83-91` `chunkCleaner.DeleteObjectChunks`（失败 warn 不阻断，I3/§2.1 语义）；`versionsForHardDelete`（:57）含 `checkObjectProtection`（WORM/legal hold） | ✅ 补充验证 |
| E9 | 事务性审计 + outbox：`event_outbox.go:102` `HardDeleteObjectWithEvent` / `:147` `SoftDeleteObjectWithEvent`（单事务 = 对象行 + `audit_log` 行 + outbox 事实）；`:22` `EventTypeFileDeleted11 = "vault.file.deleted@1.1"`；迁移 `0041_event_outbox.up.sql`：`event_outbox` 表 `status CHECK ('pending','inflight','delivered','failed')` + `event_outbox_delivered` 保真表；`audit_governance_outbox`（`0039_audit_governance_outbox.up.sql`）**无 status 列** | ✅ 补充验证（**修正**：验收的 pending→delivered 断言须查 `event_outbox`，且按 `event_type` 过滤——一次删除两行事实） |
| E10 | relay：`cmd/server/workers.go:149-177` `startEventOutboxRelay`（claim→deliver→complete/retry，`EVENT_OUTBOX_*` 配置）；`internal/events/event_outbox_relay_test.go:144` `TestOutboxRelay_DeliveryLifecycle`（pending→delivered 生命周期断言先例） | ✅ 补充验证 |
| E11 | envelope schema：`internal/events/payload.go:29-49` `deletedFact`（字段序固定、字节稳定）；`BuildDeletedFact`（:105）在**删除时刻**构建、自包含；golden 测试 `schema_test.go:15`（`goldenDeletedFact`，:115 断言 `SchemaVersion=="1.1" && EventType=="vault.file.deleted@1.1"`） | ✅ 补充验证；envelope **无** invalidation flags（修正见头部） |
| E12 | 失效机制（行为）：`repository/sql_access_cleanup.go:12-37` `deleteObjectCapabilities`/`deleteObjectAccessState`（`DELETE FROM shares / public_assets / resource_acls`，在删除事务内执行——`sql_objects_maint.go:36/68/91/134/171/186` 调用）；share 解析在删除后自然 404（`ResolveShare` 按 token 查 `shares` 行） | ✅ 补充验证 |
| E13 | admin 路由装配：`router.go:329-352` admin 组（`adminRL` 独立限流 + 各 handler 内 `requireAdmin`）；路由表 `router.go:187-208`（`AdminOnly: true` 条目，`specgen.go` 据此生成 `/openapi.json`——AGENTS.md 要求 REST route 同步 spec 表） | ✅ 补充验证 |
| E14 | e2e 先例：`internal/integration/fullserver_test.go:876` `TestComposition_DeleteDeliversBothFacts`、`:685` `TestDeleteResponse_DoesNotBlockOnDelivery`（信号式，非墙钟）；`internal/integration/authz_cli_failclosed_test.go` —— **真实 `cli.Run` 驱动真实服务器**的 AC-4 组合测试先例（bearer principal 中间件 + `AERO_*` env） | ✅ 补充验证 |

### 缺口机理

```
运维想删 tenant=acme 的 docs/a.txt（硬删 + 审计 + 事件）
  cmdAdmin switch: keys|tenants|jobs|audit|buckets  ← 无 files → "unknown admin resource" 退出 2
  rm docs/a.txt: DELETE /v1/files/docs/a.txt        ← 软删、tenant 靠 env 头、无 --hard、无 admin 门禁
  AdminHandler: 无文件端点                            ← curl 无路
  ✗ 服务层 Delete(tenant,bucket,key,hard) 已就绪，只缺入口
```

---

## 3. 需求规格

### FR-1：CLI `admin files delete <tenant> <key> [--hard]`（`internal/cli`）

- `cmdAdmin` 资源 switch 新增 `case "files": return c.cmdAdminFiles(action, rest)`（`cli_admin.go`，与既有资源同构）；`cmdAdminFiles` 仅分发 `delete`，未知 action → stderr + 返回 2（对齐 `cmdAdminKeys` 模式）。
- `admin files delete` 参数：`<tenant> <key>` 必填（缺参 → usage 行 + 返回 2，对齐 `adminTenantDelete`）；`--hard` 可选 flag（解析模式对齐 `cmdAdminBuckets` 的 `--action` 循环，`cli_admin_buckets.go:38-41`）。
- 请求形状：`DELETE /v1/admin/files/<tenant>/<key>?hard=1`（无 `--hard` 时无查询参数）；路径分段转义复用 `escapeKey(tenant + "/" + key)`（`cli_crud.go:77-82`，key 含 `/` 时逐段 `PathEscape`）；**bucket 恒为 `service.DefaultBucket`**（与 `rm` 一致；方向命令签名不含 bucket，不扩 scope）。
- 响应处理：复用 `readSuccessfulResponse`（`response.go`，与 `adminTenantDelete` 一致：2xx 读体、非 2xx 经 `renderError` 渲染 REST envelope 到 stderr 并返回 1）；成功打印 `deleted`（对齐 `adminTenantDelete` 输出，字节固定）。
- `adminUsage()` 新增条目 `files delete <tenant> <key> [--hard]`；顶层 `usage()`（`cli.go`）的 admin 子命令段落同步补一行（两处都在 `internal/cli`，保持一致）。

### FR-2：REST admin 文件删除端点（`internal/api/rest/admin.go` + `router.go`）

- `AdminHandler` 新增 `DeleteFile(w, r)`：`requireAdmin`（:457 模式）→ tenant 取 chi 参数 `chiURLParam(r, "tenant")`（对齐 `DeleteTenant`，admin.go:329）、key 从路径尾段提取（对齐 `handler.go:58` `keyFromPath`，key 可含 `/`）→ `hard := r.URL.Query().Get("hard") == "1"`（对齐 `handler.go:245`）→ `svc.Delete(r.Context(), tenant, service.DefaultBucket, key, hard)` → 成功 `204 No Content`，失败 `h.writeError(w, r, err)`（哨兵 `ErrNotFound`/`ErrObjectCorrupt`/WORM 拒绝等既有映射不变）。
- `router.go` admin 组（:329-352）注册 `r.Delete("/admin/files/{tenant}/*", adm.DeleteFile)`（尾段通配形状对齐 `r.Delete("/files/*", h.deleteKey)`，router.go:243）——**在既有 admin 组内**，自动获得 `adminRL`；**不改中间件链**（I4，`requireAdmin` 为 handler 内检查，与既有 admin handler 一致）。
- 路由表（`router.go:187-208` 形状）新增 `{Method: "DELETE", Path: "/v1/admin/files/{tenant}/{key}", Summary: "Delete a file in any tenant (admin)", Tag: "admin", AdminOnly: true, Status: 204}` → `/openapi.json` 自动生成（`specgen.go`），满足 AGENTS.md「REST route → 同步 openapi」。
- **服务层零改动**：`FileService.Delete` 已含审计行（`deleteAuditEntry`，action `file.delete`，detail 区分 soft/hard）、outbox 事实（`deleteFacts`）、share/ACL 失效（事务内）、RAG chunk 清理、保留锁检查、配额——admin 端点与 REST `Handler.Delete` 共用同一条 `svc.Delete`，无新业务逻辑。

### FR-3：不变量与边界（沿用既有语义，不新增机制）

- 审计：删除事务内原子写 `audit_log`（detail=soft/hard）→ `admin audit list`（`cmdAdminAudit` → `GET /v1/admin/audit` → `ListAudit`）**今日即可见**，无需改 `AuditEntry`/`ListAudit`（审计渲染增强属分析中方向 2，不在本方向 scope）。
- 事件：admin 删除与普通删除走同一 `deleteFacts` → 每次删除 `event_outbox` 恰 2 行（`deleted@1.1` + `notify@1.1`），payload 在删除时刻构建、字节稳定；`actor` 取 `access.PrincipalFrom(ctx)`（admin 凭据下为 operator principal，未登录为空串——合法，不新增身份管线）。
- durable_async：删除**从不等待** relay——outbox 写入在删除事务内，relay 独立轮询（`workers.go:149-177`）；relay 宕机时删除照常成功，事实停留 `pending`，恢复后投递（E10 已有生命周期测试）。
- 失效语义：share/public_assets/resource_acls 行随删除事务删除（E12）；RAG 引用经 `ChunkCleaner.DeleteObjectChunks` 同步清理，**失败 warn 不阻断**（§2.1 契约）；`deleted@1.1` envelope 无 invalidation flags（头部修订记录）。
- key 校验在 FileService 层（`checkedObjectDefaults` → `validateKey`）——admin 端点不重复校验（I3）。

### 非功能约束

- `make check` 全绿（gofmt / build / vet / test，AGENTS.md §0）；新增/修改文件 ≤ 500 行；单函数 ≤ 50 行。
- 纯 stdlib，无新 `go.mod` 依赖（I6）；不触碰中间件链（I4）、存储 key 不变量（I3）、迁移文件（I2，**无新迁移**）。
- 基线路径（SQLite + local FS + AI off）必须全绿；不 gate 任何 opt-in 功能（I5）。
- 不修改 `rm`/`cmdRemove`、`AdminHandler` 既有端点、`FileService.Delete` 签名、outbox/relay/schema 生产代码。

---

## 4. 验收标准（可测试）

> 测试基建（已验证）：CLI 单元测试镜像 `internal/cli/cli_admin_test.go`（`newTestClient`+`httptest`）；服务/仓库层 outbox 断言镜像 `internal/repository/event_outbox_test.go:136`（`TestDeleteObjectWithEvent_OneTx`）与 `internal/events/event_outbox_relay_test.go:144`（`TestOutboxRelay_DeliveryLifecycle`）；组合 e2e 镜像 `internal/integration/authz_cli_failclosed_test.go`（真实 `cli.Run` + 真实服务器）与 `fullserver_test.go:876/685`。

### AC-1 单元：CLI 命令形状（httptest 断言请求）

```go
// internal/cli/cli_admin_test.go（镜像 TestCmdAdminTenants_Delete_Success）
func TestCmdAdminFiles_Delete_Success(t *testing.T) {
	// httptest server 记录 method/path/query：
	// cmdAdminFiles("delete", []string{"acme", "docs/a.txt", "--hard"})
	//   → 断言 method=="DELETE"、path=="/v1/admin/files/acme/docs/a.txt"、
	//     query=="hard=1"（无 --hard 时 query 为空）、退出码 0
	// key 含斜杠：[]string{"acme", "docs/sub/a.txt"} → path 逐段转义不变
}

func TestCmdAdminFiles_Delete_TooFewArgs_Returns2(t *testing.T) {
	// cmdAdminFiles("delete", nil) / ["acme"] → 退出码 2 + stderr 含 usage
	// cmdAdmin("files", "frob") → 未知 action → 2
}

func TestCmdAdminFiles_Delete_HTTPError_Returns1(t *testing.T) {
	// 服务器返回 404/409 → 退出码 1，stderr 含 renderError 形状（"HTTP 404 …"）
}

func TestCmdAdminUsage_ListsFilesDelete(t *testing.T) {
	// captureStderr 下调用 adminUsage()，断言输出含 "files delete <tenant> <key>"
	// 顶层 usage() 同断言（cli.go）
}
```

### AC-2 outbox 投递：admin 删除 → 恰一行 `vault.file.deleted@1.1`，pending→delivered

```go
// internal/service/file_delete_test.go（镜像既有 file_delete_test.go 形状）
func TestAdminDelete_EmitsExactlyOneDeletedFact(t *testing.T) {
	// repo.Open("sqlite", file:…)+Migrate；store := storage.NewLocal(TempDir)
	// Put → svc.Delete(ctx, "acme", service.DefaultBucket, "docs/a.txt", true)
	// 断言 event_outbox 中 event_type='vault.file.deleted@1.1' 恰 1 行
	//   （notify@1.1 恰 1 行；status=='pending'、origin_id==obj.ID、
	//     payload 含 schema_version=="1.1"）；audit_log 恰 1 行 action=="file.delete"
}

// internal/events/event_outbox_relay_test.go（镜像 TestOutboxRelay_DeliveryLifecycle:144）
func TestOutboxRelay_AdminDeletePendingToDelivered(t *testing.T) {
	// 种子 = 上述删除产生的行 → 跑 relay（PollInterval 短）→
	// 断言 status 由 pending 转 delivered、delivered_at_ns>0；relay 宕机
	// （不启动 relay）时 status 保持 pending，业务删除仍成功（非阻塞证明，见 AC-4）
}
```

### AC-3 事件 schema：envelope 与 golden 一致（actor/tenant/key/version_id + 失效行为）

```go
// internal/events/schema_test.go（golden 已存在，schema_test.go:15/:115 —— 本项为回归钉死）
// 断言 admin 路径（带 principal 的 ctx）产出的 payload 与 goldenDeletedFact 逐字节一致：
//   schema_version=="1.1"、event_type=="vault.file.deleted@1.1"、
//   tenant/key/version_id/actor 字段齐备；envelope 无 records/无 invalidation flags

// internal/service/file_delete_test.go
func TestAdminDelete_InvalidatesShareAndChunks(t *testing.T) {
	// 预置 shares 行 + 记录型 ChunkCleaner：
	// svc.Delete(…, hard=true) 后 → shares 表 0 行（事务内失效）、
	// chunkCleaner.DeleteObjectChunks 被调（version.ID，硬删含全部版本——
	// versionsForHardDelete）；GetObject → ErrNotFound（元数据删除）；
	// 硬删后 storage blob 不存在；软删（hard=false）则 blob 仍在、行 deleted_at 置位
}
```

### AC-4 组合 e2e：真实服务器 + 真实 CLI；relay 宕机不阻塞

```go
// internal/integration/fullserver_test.go（镜像 TestComposition_DeleteDeliversBothFacts:876、
// TestDeleteResponse_DoesNotBlockOnDelivery:685、authz_cli_failclosed_test.go 的 cli.Run 驱动）
func TestComposition_AdminFilesDeleteEndToEnd(t *testing.T) {
	// 1) 全服务器（httptest + SQLite + local FS，admin 凭据）；PUT 上传 acme/docs/a.txt
	// 2) 不启动 outbox relay（或 relay 指向死目标）→ cli.Run(["admin","files","delete",
	//    "acme","docs/a.txt","--hard"]) 退出码 0 —— 删除完成（durable_async 非阻塞证明）
	// 3) GET /v1/files/docs/a.txt（tenant=acme）→ 404；shares 表无该对象行
	// 4) event_outbox 中 deleted@1.1 行 status=='pending'（relay 未 drain）
	// 5) 启动 relay 并 drain → status=='delivered'；event_outbox_delivered 恰 1 行
	// 6) 以 admin audit list 形状断言：GET /v1/admin/audit 响应含
	//    {"action":"file.delete","tenant_id":"acme","detail":"hard"} 行（审计可见）
}
```
