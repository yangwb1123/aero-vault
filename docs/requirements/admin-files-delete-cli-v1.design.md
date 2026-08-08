# 设计：`admin files delete` CLI 面 —— 跨租户管理删除入口（CLI 命令 + admin REST 路由，零服务层改动）

> **配套规格：** `docs/requirements/admin-files-delete-cli-v1.md`（FR-1…FR-3 / AC-1…AC-4）· **模块：** `internal/cli` + `internal/api/rest` · **状态：** 设计（未实现）· **基线：** HEAD `acfaaf4`（`make check` 实测全绿）
> **门禁：** `make check` 全绿 · 单文件 ≤ 500 行 · 单函数 ≤ 50 行 · 纯 stdlib（I6）· I1/I2/I3/I4 纪律 · **无新迁移**（I2）· **无服务层/仓库层/events 层生产改动**

---

## 1. 证据复核（规格全部主张独立复验；构建与测试本次实测）

**基线实测：** HEAD `acfaaf4` 上 `make check`（gofmt / build / vet / test / filesize）全绿。以下复核除规格自带的 14 条证据外，新增 6 条设计关键发现（N1–N6）。

| # | 规格引用 | 复核结论 |
|---|---------|---------|
| E1 | `cli.go` `cliHandlers` map（:70-83，`"admin"` :83 注册）+ `usage()`（:106 起，含 admin 子命令段 :124-135） | ✅ 精确。`Run` 未知命令返回 2（:99-101 区域） |
| E2 | `cli_admin.go` `cmdAdmin` 资源 switch：`keys/tenants/jobs/audit/buckets`（:46-54），**无 `files`**；default 分支「unknown admin resource」+ `adminUsage()` 返回 2（:55-59）；`adminUsage()`（:14-34）无 files 条目 | ✅ 精确。`cmdAdminAudit`（:384）→ `GET /v1/admin/audit?limit=N`，原样打印响应体 |
| E3 | `cli_crud.go` `cmdRemove`（:93-98）：`DELETE /v1/files/`+`escapeKey(key)`，无 hard 参数；`escapeKey`（:111-117）按 `/` 分段逐段 `url.PathEscape` | ✅ 精确。补充验证：REST `Handler.Delete`（`handler.go:238-252`）读 `?hard=1` → `svc.Delete(…, hard)`——服务层硬删已存在，缺口在入口 |
| E4 | `admin.go` `NewAdminHandler`（:34）、`requireAdmin`（:457）、`ListAudit`（:433）、`audit`（:410） | ✅ 精确（规格引 DeleteTenant :329，实际 :326，语义一致）。**无文件删除端点** |
| E5 | `file.go` `emit()` :297、`WithChunkCleaner` :137、`checkedObjectDefaults` :274 | ✅ 行号精确 |
| E6 | `repository.go:193` `EventDeleted EventType = "deleted"` | ✅ 行号精确 |
| E7 | `cli_admin_test.go` httptest 模式：`newTestClient`（`cli_test.go:67`）、`captureStdout`（:28）、`captureStderr`（:48），退出码 0/1/2 断言 | ✅ 精确，可镜像（`TestCmdAdminKeys_Revoke_TooFewArgs_Returns2` :64、`TestCmdAdminTenants_Delete_Success` 等） |
| E8 | 服务层删除全链路：`file_delete.go:147` `Delete(…, hard)`；`hardDeleteObject`（:18，storage 先删 blob 再元数据事务）；`softDeleteObject`（:76）；`deleteFacts`（:118 注释/:123 func，产出 deleted@1.1 + notify@1.1 两事实）；`versionsForHardDelete`（:57）；chunkCleaner 失败 warn 不阻断（:27-28/:81-82） | ✅ 补充验证。**N1：** `Delete` 还执行 `authorizeObject`（access.go:122 → `Authorize`）与 `requireActiveTenant`——admin 面删除同样受 access 层约束（见 §5 F2/F4） |
| E9 | 事务性审计 + outbox：`event_outbox.go:22` `EventTypeFileDeleted11`；迁移 **0041** `status CHECK ('pending','inflight','delivered','failed')`；**0039** `audit_governance_outbox` 无 status 列 | ✅ 精确（两迁移文件均实测）。**修正成立**：AC-2 断言须查 `event_outbox` 且按 `event_type` 过滤（一次删除两行） |
| E10 | relay：`workers.go:149-177` `startEventOutboxRelay`（claim→deliver→complete/retry，`EVENT_OUTBOX_*`）；`event_outbox_relay_test.go:144` `TestOutboxRelay_DeliveryLifecycle` | ✅ 精确 |
| E11 | envelope：`payload.go:29-49` `deletedFact`（字段序固定、**无 invalidation flags**）；`BuildDeletedFact`（:105）；golden `schema_test.go:15`（:115 断言 SchemaVersion/EventType） | ✅ 精确。**修正成立**：无 share/version/RAG flags |
| E12 | 失效机制（行为）：`sql_access_cleanup.go:12-37` `deleteObjectCapabilities`/`deleteObjectAccessState`（shares/public_assets/resource_acls 在删除事务内删） | ✅ 精确 |
| E13 | admin 路由装配：`router.go:329-352` admin 组（`adminRL` 独立限流）+ 各 handler 内 `requireAdmin`；路由表 :187-208（`AdminOnly: true` 条目） | ✅ 精确（admin 组实际延伸至 :355+，含 departments 条件路由；语义一致）。**N2：** `/openapi.json` 由 `globalSpec`（specgen.go:38-53）从路由表 init 时构建、`openapi.go:11` 直接服务——**无独立 openapi.json 文件需同步**，路由表加条目即自动生效 |
| E14 | e2e 先例：`fullserver_test.go:876` `TestComposition_DeleteDeliversBothFacts`、`:685` `TestDeleteResponse_DoesNotBlockOnDelivery`（信号式）；`authz_cli_failclosed_test.go`（真实 `cli.Run` 驱动真实服务器，:78） | ✅ 精确 |

**规格修订记录 3 条修正（§0）：** ① REST `?hard=1` 存在但 CLI 不可达、非 admin 面 ✅；② outbox 表 = `event_outbox`（0041），非 0039 ✅；③ envelope 无失效 flags，失效是行为 ✅。**全部复核成立。**

### 设计关键新发现（规格未覆盖，设计据此修正）

| # | 发现 | 设计影响 |
|---|------|---------|
| N1 | `writeError` 是 `*Handler` 的方法（`handler_helpers.go:19`），**`AdminHandler` 没有该方法**；`classify(err)` 是包级函数（handler_helpers.go:34），既有 admin handler 用 `writeJSON(w, status, errorBody{…})` 直写 | 规格 FR-2 的「失败 `h.writeError(w, r, err)`」不精确 → **D6**：admin.go 新增 4 行包装方法复用包级 `classify`，零改动既有 handler |
| N2 | `svc.Delete` → `authorizeObject` → `access.Manager.Authorize`（authorizer.go:14-31）：`PrincipalSystem` 短路放行；`tenantMatches`（:67-69）`p.TenantID=="*"` 跨租户放行；租户限定 key 仅本租户；`requireActiveTenant`（access.go:105-121）禁用租户拒绝（**无条件启用**，main.go:95）。CI 基线（authorizer nil / disabled）不受影响 | 设计**不注入 system principal**。**纵深防御有配置前提（复核修正）**：`tenantMatches` 只在 `ACCESS_CONTROL_ENABLED=true`（authorizer 非 nil）时运行；默认配置下租户限定 admin key 与 operator key 等价（C3 模型，§5 F2） |
| N3 | chi 混合 `{param}/*` 尾段通配已有先例：`s3compat/router.go:23-27` `r.Delete("/{bucket}/*", …)`；`keyFromPath`（handler.go:58-62）用 `chi.URLParam(r, "*")` + `TrimPrefix` | 路由 `r.Delete("/admin/files/{tenant}/*", …)` 形状可行，key 提取复用 `keyFromPath` |
| N4 | `cli.do`（cli.go:53-67）总是携带 `Authorization: Bearer <AERO_API_KEY>` 与 `X-Aero-Tenant`（若 env 设置）；AGENTS.md §2.5：租户限定 key 与冲突 `X-Aero-Tenant` → 403（既有中间件行为，适用所有 admin 命令） | 使用约束（§4 C3）：跨租户 admin 删除时**不设 `AERO_TENANT`**（与既有 `admin tenants delete` 用法一致）；operator key（tenant=`*`）无冲突 |
| N5 | `adminTenantDelete`（cli_admin.go:228-247）成功打印 `deleted`（字节固定），`readSuccessfulResponse`（response.go:84）2xx 读体、非 2xx 经 `renderError` 渲染到 stderr 返回 1 | FR-1 响应处理形状已确认可镜像；204 空体 `readSuccessfulResponse` 正常处理 |
| N6 | `Delete` 内 `preflightQuota(ctx, tenant, 0, 0)`（file_delete.go:159）——删除路径的配额预检为 0 增量 no-op，但依赖租户状态检查通过 | 无配额失败模式；租户禁用 → 403（F4） |

---

## 2. 设计总览

```mermaid
flowchart LR
    subgraph CLI["aero-vault cli admin files delete"]
        A["cmdAdmin switch + case files\n(cli_admin.go)"] --> B["cmdAdminFiles: action==delete\n未知 action → 2"]
        B --> C["adminFilesDelete:\n<tenant> <key> 必填 + --hard flag"]
        C --> D["DELETE /v1/admin/files/{tenant}/{key}?hard=1\nescapeKey(tenant+"/"+key) 分段转义"]
    end
    D --> R["admin 组内路由 (adminRL + requireAdmin)\nrouter.go"]
    R --> H["AdminHandler.DeleteFile\ntenant←chi param · key←keyFromPath · hard←?hard=1"]
    H --> S["FileService.Delete(tenant, default, key, hard)\n——既有全链路，零改动"]
    S --> T["单事务：元数据删 + audit_log + outbox 事实\ndeleted@1.1 + notify@1.1"]
    T --> E["EventOutboxRelay 异步 drain（宕机不阻塞）"]
    S --> I["行为失效：shares/public_assets/resource_acls\n+ ChunkCleaner（warn 不阻断）"]
```

**核心语义（三条）：**

1. **入口薄、逻辑全下沉**：CLI 命令与 REST handler 都是协议适配层（AGENTS.md §2.1/§2.2），业务语义（审计/事件/失效/保护检查/配额）全部来自既有 `svc.Delete`——admin 删除与普通 REST 删除**字节级同一路径**，无新业务逻辑（D8）。
2. **admin 面即门禁面**：路由注册在既有 admin 组内（自动获得 `adminRL` 限流），`requireAdmin` 为 handler 内检查（与既有 admin handler 同构）；access 层继续生效（租户限定 key 受限、operator key 跨租户）——**不注入 system principal**（N2）。
3. **durable_async 不变式沿用**：删除从不等待 relay；outbox 事实在删除事务内落库，relay 宕机时删除照常成功、事实停留 `pending`（AC-4 证明）。

**关键设计决策（D1–D10）：**

| # | 决策 | 理由 |
|---|------|------|
| D1 | CLI 资源 `files` 仅分发 `delete` action；未知 action → stderr + 返回 2 | 对齐 `cmdAdminKeys`/`cmdAdminTenants` 模式（E2/E7）；不扩 scope（方向签名不含 bucket） |
| D2 | `--hard` flag 解析镜像 `cmdAdminBuckets` 的 `--action` 循环（`cli_admin_buckets.go:38-41`：for i 扫描 + 消费值） | 既有 CLI flag 先例，无新解析机制 |
| D3 | 请求路径 = `"/v1/admin/files/" + escapeKey(tenant + "/" + key)`；`--hard` 存在时追加 `?hard=1`，否则无查询参数 | `escapeKey`（E3）逐段 PathEscape，key 含 `/` 安全；tenant 单段。**「上游校验」不成立（复核修正）**：仓库层无租户名校验（`CreateTenant` 仅非空，admin.go:292-296）——含 `/`/可转义字符/`*` 的租户名在本路径式路由下不可寻址或歧义（全部 fail-closed 404），属**文档化使用约束**（租户名须路径安全），非代码门禁 |
| D4 | REST 路由 `r.Delete("/admin/files/{tenant}/*", adm.DeleteFile)` 注册在既有 admin 组内（router.go:329 组） | 自动继承 `adminRL`；`{tenant}/*` 混合通配有 s3compat 先例（N3）；不改中间件链（I4） |
| D5 | `tenant := chiURLParam(r, "tenant")`；`key := keyFromPath(r)`（`chi.URLParam(r, "*")` + TrimPrefix）；`hard := r.URL.Query().Get("hard") == "1"` | 三个取值都复用既有 helper（admin.go:330、handler.go:58、handler.go:245），零新解析代码 |
| D6 | **`AdminHandler` 新增 4 行 `writeError` 包装**（复用包级 `classify` + `writeJSON` + `errorBody`），规格 FR-2 的「h.writeError」据此修正（N1） | 与 `Handler.writeError`（handler_helpers.go:19）同形状：删除路径实际映射集合为 **404 NotFound / 409 ObjectLocked / 410 ObjectCorrupt / 403 AccessDenied·TenantDisabled / 400 InvalidArgument（空租户，F13）/ 500 InternalError**——412 仅条件请求可达，不在删除路径（复核修正，见 F5） |
| D7 | bucket 恒为 `service.DefaultBucket`（file.go:18） | 与 `rm` 一致（FR-1）；方向命令签名不含 bucket，不扩 scope |
| D8 | **服务层零改动**：`FileService.Delete` 签名/实现、`deleteFacts`、outbox/relay/schema 全部不动 | 审计行（action `file.delete`，detail soft/hard）、双事实、失效行为、保护检查都是既有路径（E8/E9/E11/E12） |
| D9 | 路由表（router.go:187-208 形状）新增 `{Method: "DELETE", Path: "/v1/admin/files/{tenant}/{key}", Summary: …, Tag: "admin", AdminOnly: true, Status: 204}` | `/openapi.json` 由 `globalSpec` 从路由表自动构建（N2）——满足 AGENTS.md「REST route → 同步 openapi」且无独立文件漂移面 |
| D10 | 零新依赖、零新迁移、零中间件链改动 | I2/I4/I6；生产机制全部已落地（0041 + `deleteFacts` + relay），本方向只加入口 + 验收测试 |

---

## 3. API 变更

### 3.1 CLI —— `internal/cli/cli_admin.go`（+ `cli.go` usage 同步）

```go
// cmdAdmin switch（:44-59）新增：
case "files":
    return c.cmdAdminFiles(action, rest)

// 新增（与 cmdAdminKeys 同构；<50 行）：
func (c *Client) cmdAdminFiles(action string, args []string) int {
    switch action {
    case "delete":
        return c.adminFilesDelete(args)
    default:
        fmt.Fprintf(os.Stderr, "unknown files action: %s\n", action)
        return 2
    }
}

func (c *Client) adminFilesDelete(args []string) int {
    if len(args) < 2 {
        fmt.Fprintln(os.Stderr, "usage: admin files delete <tenant> <key> [--hard]")
        return 2
    }
    if args[0] == "" {                        // F13：空租户拒绝（exit 2），否则 defaults("") 静默指向 default
        fmt.Fprintln(os.Stderr, "usage: admin files delete <tenant> <key> [--hard]  (tenant must not be empty)")
        return 2
    }
    hard := false
    for i := 2; i < len(args); i++ {          // D2：镜像 --action 循环
        if args[i] == "--hard" {
            hard = true
        }
    }
    path := "/v1/admin/files/" + escapeKey(args[0] + "/" + args[1])  // D3
    if hard {
        path += "?hard=1"
    }
    resp, err := c.do(http.MethodDelete, path, nil, nil)   // N4：Bearer + X-Aero-Tenant 由 do 统一携带
    if err != nil {
        fmt.Fprintln(os.Stderr, err)
        return 1
    }
    defer resp.Body.Close()
    if _, ok := readSuccessfulResponse(resp); !ok {        // N5：非 2xx → renderError 到 stderr
        return 1
    }
    fmt.Println("deleted")                                 // N5：与 adminTenantDelete 字节一致
    return 0
}
```

- `adminUsage()`（:14-34）resources 段新增 `files delete <tenant> <key> [--hard]`。
- 顶层 `usage()`（cli.go:124-135 admin 段）同步加同一行（两处同文件内保持一致）。

### 3.2 REST —— `internal/api/rest/admin.go` + `router.go`

```go
// admin.go 新增（D4/D5/D6）：
func (h *AdminHandler) DeleteFile(w http.ResponseWriter, r *http.Request) {
    if !h.requireAdmin(w, r) {            // :457 模式，与既有 admin handler 一致
        return
    }
    tenant := chiURLParam(r, "tenant")    // 对齐 DeleteTenant（:330）
    if tenant == "" {                     // F13：拒绝空租户（400），否则 svc 层 defaults("") 归一为 "default"
        h.writeError(w, r, fmt.Errorf("%w: tenant is required", service.ErrInvalidArgs))
        return
    }
    key := keyFromPath(r)                 // chi "*" param + TrimPrefix（handler.go:58 复用）
    hard := r.URL.Query().Get("hard") == "1"  // 对齐 handler.go:245
    if err := h.svc.Delete(r.Context(), tenant, service.DefaultBucket, key, hard); err != nil {
        h.writeError(w, r, err)           // D6：新增 4 行包装，复用包级 classify
        return
    }
    w.WriteHeader(http.StatusNoContent)   // 204，对齐 Handler.Delete（handler.go:251）
}

func (h *AdminHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
    code, message, status := classify(err)      // 包级，handler_helpers.go:34
    writeJSON(w, status, errorBody{Error: errorPayload{
        Code: code, Message: message, RequestID: mw.RequestIDFrom(r.Context()),
    }})
}
```

- `router.go` admin 组（:329 起）新增 `r.Delete("/admin/files/{tenant}/*", adm.DeleteFile)`——**在既有组内**，自动获得 `adminRL`；与 `/v1/files/*`（:243）前缀无冲突（E13/N3）。
- 路由表（:187-208 形状）新增：
  ```go
  {Method: "DELETE", Path: "/v1/admin/files/{tenant}/{key}",
   Summary: "Delete a file in any tenant (admin)", Tag: "admin", AdminOnly: true, Status: 204},
  ```
  → `/openapi.json` 自动包含（N2）。

### 3.3 编译影响面（全量清单）

`internal/cli/cli_admin.go` · `internal/cli/cli.go` · `internal/api/rest/admin.go` · `internal/api/rest/router.go` —— **共 4 个生产文件**，全部为新增代码（switch case + 方法 + 路由行），**零签名变更、零既有调用点改动**。服务层/仓库层/events/迁移/中间件：**零变更**。

---

## 4. 兼容性约束

| # | 约束 | 处理 |
|---|------|------|
| C1 | 既有 `rm`/`cmdRemove` 语义 | **不动**（规格非功能约束）：`rm` 保持非 admin 软删；新命令是并列入口，两条路径共享同一 `svc.Delete`，无行为分叉 |
| C2 | 既有 admin 路由 | 全部不动；新路由前缀 `/admin/files/` 与既有 admin 路径（tenants/keys/jobs/audit/config/buckets/departments/webhook-failures）无重叠（E13 实测） |
| C3 | 鉴权语义（N2/N4）——**operator 等价模型（复核定稿）** | **operator key**（tenant=`*` + `admin` scope）：任意配置下跨租户删除放行（`tenantMatches`，authorizer.go:67-69）。**租户限定 admin key**：`ACCESS_CONTROL_ENABLED=true` 时仅本租户（403 `tenant_mismatch`，authorizer.go:29）；**默认配置（`ACCESS_CONTROL_ENABLED=false`，config.go:216 默认）下 `buildAccessManager` 返回 nil（cmd/server/access.go:11-15）→ `authorize` 在 `tenantMatches` 之前早退（access.go:91）→ 路径租户即唯一目标权威，租户限定 admin key 与 operator key 等价**——与全部既有 admin 路由（SetQuota/DeleteTenant/SetBudget/PutBucketQuota，router.go:333-355）同一模型，本设计不引入分叉校验。`X-Aero-Tenant` 冲突规则（auth_middleware.go:148-158）只钉**调用方身份**（tenant-scoped key 冲突 → 403，operator key 不钉；空 header 钉到 key 租户），**从不约束目标租户**（目标只来自路径）——**CLI 使用约束**：跨租户删除时勿设 `AERO_TENANT`（避免身份混淆；默认配置下非安全边界）。**无条件部分**：`requireActiveTenant`（禁用租户 403，main.go:95 + access.go:105-121）始终生效；`requireAdmin` + write-scope 门（router.go:363-378；`Key.Has` admin 蕴含全 scope，auth.go:46-51）始终生效 |
| C4 | 中间件链（I4） | 零改动；`requireAdmin` 为 handler 内检查（既有模式）；路由在 admin 组内自动获得 `adminRL` |
| C5 | OpenAPI | 纯增量：路由表加 1 条目 → `/openapi.json` 自动生成（N2）；既有 spec 路径不变；无独立文件需同步 |
| C6 | CLI 退出码/输出契约 | 0=成功（stdout `deleted`）、1=HTTP/网络错误（stderr `renderError`）、2=用法错误（stderr usage）——与 `adminTenantDelete` 完全一致（N5/E7） |
| C7 | 204 空体响应 | `readSuccessfulResponse`（response.go:84）对空体正常返回 true；CLI 打印固定 `deleted`（N5） |
| C8 | key 转义（I3） | `escapeKey`（cli_crud.go:111）逐段 PathEscape，含 `/` 的 key 路径段安全；tenant 为单段（租户名不含 `/`）；服务层 `checkedObjectDefaults` 仍做最终 key 校验——admin handler **不重复校验**（FR-3，I3） |
| C9 | 事件/审计语义一致性 | admin 删除与普通 REST 删除走同一 `svc.Delete` → 同一 `deleteAuditEntry`/`deleteFacts`/失效行为/保护检查；`actor` 取 `PrincipalFrom(ctx)`（admin 凭据下为 operator principal，未登录空串合法）——接收方无需区分删除入口 |
| C10 | schema/数据 | **无新迁移、无数据格式变化**（I2）：`event_outbox`/`audit_log`/`shares` 等表结构不动；已落库行不受影响；relay 行为不变（E10） |
| C11 | 多副本/集群 | 无共享状态新增（路由与 CLI 均无状态）；admin 端点无新增配置项；`EVENT_OUTBOX_*` 语义不变 |

---

## 5. 失败模式

| # | 故障 | 行为 | 恢复 |
|---|------|------|------|
| F1 | 非 admin 凭据（registry 启用且无 `admin` scope key / 匿名） | `requireAdmin`（admin.go:457）→ 403 `Forbidden: admin scope required`（envelope）；CLI `renderError` → stderr + 退出 1 | 运维使用 admin key；registry 未启用时基线直通（CI 路径） |
| F2 | 租户限定 admin key 删除其他租户 | `ACCESS_CONTROL_ENABLED=true` 时：`authorizeObject` → `tenantMatches` 不匹配 → 403 `AccessDenied(tenant_mismatch)`（authorizer.go:29,67-69）；**默认配置下无此约束**（authorizer nil 早退，access.go:91）——租户限定 key 与 operator key 等价（C3 模型） | 默认配置：租户边界由 admin scope 承担（运维信任模型）；需要 `tenant_mismatch` 纵深防御则启用 access 管理器。非故障 |
| F3 | 对象/租户不存在 | `svc.Delete` → `ErrNotFound` → 404 `NotFound`（classify 既有映射） | 无；幂等语义：删除不存在对象 = 明确 404 |
| F4 | 目标租户被禁用 | `requireActiveTenant` → 403 `TenantDisabled`（access.go:110-112，N1） | 先启用租户；与既有 REST 删除一致 |
| F5 | WORM/legal hold 锁定对象（`--hard`） | `versionsForHardDelete` → `checkObjectProtection` 拒绝 → `ErrLocked` → **409 `ObjectLocked`**（`classifyLock`，management.go:224-229；复核修正：412 仅条件请求可达，删除路径无） | 释放锁后重试；软删（无 `--hard`）不受影响（与 REST 语义一致） |
| F6 | 损坏对象（scrub 标记 corrupt） | Get/Stat 返回 `ErrObjectCorrupt` → 410 `ObjectCorrupt` | 既有语义（AGENTS.md 异常处理表） |
| F7 | 硬删 storage blob 失败 | `hardDeleteObject` storage-first 顺序（file_delete.go:14-17）：blob 未删 → 元数据事务不执行 → 错误返回（500 `InternalError`），**元数据与审计零残留** | 存储恢复后重试；无部分状态 |
| F8 | 删除事务内 audit INSERT 失败（磁盘满/只读） | 事务整体回滚：对象不删、outbox 不落、audit 不落 → 错误返回 | 与删除自身失败同路径；无部分状态 |
| F9 | relay 宕机 / 目标不可达 | 删除照常成功（durable_async 不变式）；outbox 事实停留 `pending` → relay 恢复后 drain → `delivered`（E10 生命周期语义） | 自动；**非阻塞由 AC-4 证明**（规格 FR-3 ③） |
| F10 | CLI 用法错误（缺参/未知 action） | stderr usage/unknown 行 + 退出 2 | 无；命令形状契约（AC-1 断言） |
| F11 | CLI 网络/HTTP 错误 | `do` 错误 → stderr + 退出 1；非 2xx → `renderError` envelope 渲染（含 `request_id`）→ 退出 1 | 重试；与既有 admin 命令一致（N5） |
| F12 | S3 bucket policy（`s3:DeleteObject`，含源 IP deny） | **admin 路由按设计绕过（文档化决策，非事故）**：REST `Handler.Delete` 在 `svc.Delete` 前执行 `checkBucketPolicy`（handler.go:238-241；lookup/解析失败 fail-closed 403，handler.go:65-90，按 **header 租户**解析）；admin `DeleteFile` **不执行**——bucket policy 是数据面协议守卫，admin 面为运维信任面（与全部既有 admin 路由一致）；access 层资源 ACL（启用时）仍生效 | 需要策略保护时配置 access 层 ACL deny（既有机制） |
| F13 | 空 tenant（`DELETE /v1/admin/files//key`、`admin files delete "" key`） | **显式拒绝（复核验证成立）**：CLI 侧 stderr usage + 退出 2（§3.1）；handler 侧 400 `InvalidArgument`（§3.2，复用 classify 对 `ErrInvalidArgs` 的既有映射，handler_helpers.go:41-42）——否则 `defaults("")`（file.go:264-269）静默归一 `"default"` 并删除默认租户对象（**非 fail-closed**；sibling `DeleteTenant("")` 为 404） | 无；用法错误 |

**无新失败面结论：** 上述全部失败模式均为既有 `svc.Delete` + `requireAdmin` + CLI 通用响应处理的既有语义——本设计**不引入任何新错误码、新哨兵、新重试机制**（F12 为沿用既有 admin 模型的行为决策；F13 的 400 复用既有 `InvalidArgument` 分类）。

---

## 6. 迁移步骤

**无数据库迁移**（I2：零新迁移文件；`event_outbox` 0041、`audit_log`、`shares` 等均已存在）。**无配置迁移**（无新配置项）。**无数据迁移**（无 schema 变化）。

| 步骤 | 动作 | 说明 |
|------|------|------|
| 1 | 实现（§3 四文件）+ 测试（§7） | `make check` 全绿；文件行数门禁（cli_admin.go 当前 454 行，新增约 40 行后余量 <10 行——`cmdAdminFiles`/`adminFilesDelete` 放新文件 `cli_admin_files.go` 更稳，见 §9 ④） |
| 2 | 合入 + 部署（单二进制） | 路由纯增量、CLI 纯增量——**零配置零门禁**，既有部署直接生效（I5：不 gate 任何 opt-in） |
| 3 | 验证 | ① `curl /openapi.json \| grep admin/files`（N2 自动生成）；② `AERO_API_KEY=<admin key> aero-vault cli admin files delete acme docs/a.txt --hard` → `deleted`、退出 0；③ `aero-vault cli admin audit list` 可见 `file.delete` 行（detail=hard）；④ `curl /metrics` 无新增计数器（无新遥测） |
| 4 | 回滚 | 仅回退代码（无 schema/配置残留）；期间已删除对象/审计/outbox 行无回滚义务（既有删除语义）；新路由消失即恢复原状 |

**升级窗口注意（文档化）：** 旧 CLI 二进制（无 `files` 资源）连新服务器 → `unknown admin resource` 退出 2（cmdAdmin default 分支，E2）——**优雅降级**，不产生半途请求；新 CLI 连旧服务器 → 404（路由不存在）→ `renderError` 退出 1。滚动升级顺序无硬性要求（双向兼容失败均为显式报错）。

---

## 7. 验收映射（AC-1…AC-4 → 可执行测试；重写版为可直接照抄的测试规格）

> **§7 重写说明（R1–R7，两轮评审裁定的落实）：**
> **R1 AC-2 迁包**：`Repository` 接口的 outbox 面只有 `HasEventOutboxFact`（bool——无法区分 1 vs 2 行）、`ClaimEventOutbox`（`EventOutboxRow` 无 Status 字段），"恰 1 行 / status=='pending' / payload 含 schema_version" 在包 service/events **不可实现**（且 `delivered_at_ns>0` 在 events 包不可读）。可执行形式只在 **repository 包**（`*sqlStore` 直查，`listEventOutbox` :587 先例）与 **integration 包**（DSN 直查，`outboxStatus` :1226 / `outboxCountFor` authz_parity_test.go:51 / `outboxPayloadFor` :69 先例）。D8「仓库层零改动」仍成立——断言全是测试内直查，**不新增 Repository 方法**。
> **R2 AC-4 join**：`event_outbox_delivered` 自身只有 `outbox_id`+`delivered_at_ns`（0041）；一次删除两事实、relay 全 drain 后该表 **2 行**——"恰 1 行"必须 **JOIN `event_outbox` 按 event_type 过滤**，否则不可 falsifiable。
> **R3 非阻塞主证明**改用 :685 **信号式**（relay 活跃 + L2 目标阻塞 + 4s hang-guard < 5s relay 超时）：同步实现无法在 L2 阻塞时返回响应。"不启动 relay → 删除成功"是 absence-based（无 relay 时同步/异步不可区分，检测延迟 ~10min），降级为 F9 relay-down leg（pending 轮询断言，:1072 形状）。
> **R4 requireAdmin 非空转**：`auth.Parse("")` → registry 禁用 → `requireAdmin` 恒真（admin.go:459-461），基于它的 e2e 对鉴权腿 vacuous。requireAdmin/operator-key 腿必须挂**启用 registry** `auth.Parse("opsecret:*:admin")`（token:tenant:scope+scope 格式，auth.go:93-131）；harness 新增 authKeys 参数（test-only，见 7.1）。
> **R5 F5 = 409 `ObjectLocked`**（`classify`→`classifyLock`，management.go:224-231；412 仅条件请求可达，删除路径无）——§5 F5 行已修订，本表测试按 409 断言。
> **R6 AC-3 的"payload 逐字节 = golden"在 admin 路径不可 falsifiable**（golden 钉死 object_id 42/version v-abc/request_id req-1，真实删除的值必然不同）——可执行形式 = builder 层既有 golden（schema_test.go:15/:115）+ integration 侧 payload 字段断言；`actor` 断言改**审计行**（`assertDeleteAuditRow` 的 actor 参数），payload actor 在接口面不可读。
> **R7 F8 注入缝**：`insertAuditEntry` 是 `*sqlStore` 内部函数（event_outbox.go:134/:171 事务内直调），接口包装 repo **不可拦截**——falsifiable 注入 = 临时 `ALTER TABLE audit_log RENAME TO audit_log_bak`（仓库层 in-package 或 handler 层经 DSN 第二连接，均实测可行），断言事务整体回滚（对象/outbox/audit 零残留）。

> 测试基建（既有先例，E7/E14 实测）：CLI 单元 = `internal/cli/cli_admin_test.go`（`newTestClient` + `captureStdout`/`captureStderr` + httptest）；REST 单元 = **新文件** `internal/api/rest/admin_files_delete_test.go`（新 handler 路径全部测试；admin_ops_test.go 既有内容不动）；仓库 outbox 断言 = `internal/repository/event_outbox_test.go`（`TestDeleteObjectWithEvent_OneTx` :136 扩展）；组合 e2e = `internal/integration/fullserver_test.go:876/:685` + `authz_cli_failclosed_test.go:78`（真实 `cli.Run`）+ `authz_parity_test.go:51/:69`（DSN helpers）。

### 7.1 测试基建变更（全部 test-only，零生产改动）

1. **harness 重构**（fullserver_test.go）：`startFullServerWithRelay` 的 body 抽为 `startFullServerOpts(t, relayOpts *events.EventOutboxRelayOptions, authKeys string)`——`authKeys != ""` 时用 `auth.Parse(authKeys)` 替换 `auth.Parse("")`（registry enabled → `requireAdmin` 真实生效，R4）；`startFullServerWithRelay(t, relayOpts)` = `startFullServerOpts(t, relayOpts, "")` 委托，**全部既有调用点零改动**。新增导出构造器 `startFullServerWithAuthAndRelay(t, relayOpts, authKeys)`。
2. **DSN helpers**（integration 包，复用）：`outboxStatus`（:1226）· `outboxCountFor`/`outboxPayloadFor`（authz_parity_test.go:51/:69，已按 origin_id+event_type 过滤）。新增 `deliveredCountFor(t, dsn, originID, eventType) int` 与 `deliveredAt(t, dsn, originID, eventType) int64`（R2）：
   ```sql
   SELECT COUNT(*) / d.delivered_at_ns FROM event_outbox_delivered d
   JOIN event_outbox o ON o.id = d.outbox_id
   WHERE o.origin_id=? AND o.event_type=?
   ```
3. **repository 包 in-package helpers**（event_outbox_test.go）：`countEventOutboxByType(t, repo Repository, originID int64, eventType OutboxEventType) int` 与 `outboxPayloadByType(...) string`——`repo.(*sqlStore)` cast 直查（`listEventOutbox` :587 先例），SQL 同形状。
4. **新 REST 测试文件** `internal/api/rest/admin_files_delete_test.go`：新 handler 路径的全部单元/表驱动测试（含 7.6 失败模式表）。

### 7.2 AC-1 单元：CLI 命令形状 + REST 路由/门禁（行为化）

| 测试（文件 / 函数） | 断言要点 |
|---|---|
| `internal/cli/cli_admin_test.go`：`TestCmdAdminFiles_Delete_Success`（镜像 `TestCmdAdminTenants_Delete_Success` :378）、`TestCmdAdminFiles_Delete_TooFewArgs_Returns2`、`TestCmdAdminFiles_Delete_EmptyTenant_Returns2`（F13）、`TestCmdAdminFiles_UnknownAction_Returns2`、`TestCmdAdminFiles_Delete_HTTPError_Returns1`、`TestCmdAdminUsage_ListsFilesDelete` | httptest 记录 method==`DELETE`、path==`/v1/admin/files/acme/docs/a.txt`（key 含 `/` 分段转义不变）；`--hard` 时 query==`hard=1`、无 flag 时 query 为空；退出码 0/1/2 与 stderr 形状（成功 stdout 固定 `deleted`；非 2xx stderr 含 `HTTP NNN:`；缺参/空 tenant/未知 action → usage + 2）；`adminUsage()` 与顶层 `usage()` 均含 `files delete <tenant> <key> [--hard]` |
| `internal/api/rest/admin_files_delete_test.go`：`TestAdminDeleteFile_RequireAdmin` | **启用 registry** `auth.Parse("user:default:read+write,opsecret:*:admin")` + `reg.Middleware()`（authz_delete_denied_test.go:64 先例）+ `NewRouter`（真实路由表）。匿名 → **401**（enabled registry 无凭据拒绝）；`Bearer user`（有 write scope、无 admin）→ **403** code `Forbidden` msg `admin scope required`（requireAdmin admin.go:457）且**对象仍在、零 outbox/audit**（`AdminHandler.svc` 是具体 `*service.FileService`，无桩可插——"handler 未调 svc"只能行为化断言）；`Bearer opsecret` → 204 |
| `TestAdminDeleteFile_RouteAndPassthrough` | 真实 repo+storage+svc：PUT `acme/docs/a.txt`（operator key + `X-Aero-Tenant: acme`）→ `DELETE /v1/admin/files/acme/docs/a.txt?hard=1` → **204 空体**。`hard` 透传以行为断言：hard → `GetObject`→`ErrNotFound` **且** `store.Stat(obj.StorageKey)` 报不存在（blob 消失）；soft（无 `?hard=1`）→ 204、行 `deleted_at` 置位、blob 仍在。**tenant 取路径参数**（D5）：带 `X-Aero-Tenant: other`（operator key，`*` 不与头冲突）仍删 acme 对象。含 `/` key：`docs/sub/a.txt` 经 `keyFromPath`（handler.go:58）解析正确。**F13**：`/v1/admin/files//k.txt`（空 tenant）→ 400 `InvalidArgument` |

### 7.3 AC-2 outbox 投递：恰一行 deleted@1.1，pending→delivered（R1 迁包后三腿）

| 腿 | 测试（文件 / 函数） | 断言要点 |
|---|---|---|
| 服务层入口语义（仅接口面可达断言） | `internal/service/file_delete_test.go`：`TestAdminDelete_EmitsExactlyOneDeletedFact` | `repo.Open("sqlite", file:…)+Migrate` + `storage.NewLocal`；`svc.Put` → `svc.Delete(ctx, "acme", DefaultBucket, "docs/a.txt", true)`。**接口面可达**：`HasEventOutboxFact(obj.ID, deleted@1.1)` 与 `(obj.ID, notify@1.1)` 均 true、`ListAudit` 恰 1 行 action==`file.delete`（`assertDeleteAuditRow`）、`GetObject`→`ErrNotFound`。**明确不在本腿断言**行数/status/payload（接口不可达，R1）——由下两腿钉 |
| 仓库层共享事务形状（sqlStore 直查） | `internal/repository/event_outbox_test.go`：`TestDeleteObjectWithEvent_OneTx`（:136）扩展 subtest + 7.1 ③ helpers | `HardDeleteObjectWithEvent(…, validDeleteFacts)` 后：`countEventOutboxByType(originID, deleted@1.1)==1`、`(notify@1.1)==1`、全表恰 **2** 行（`assertOutboxRows`——无过滤则 2 行，证明 event_type 过滤必要性）；每行 `Status=="pending"`（`outboxRowMeta`，:162 先例）；`outboxPayloadByType` 反序列化含 `schema_version=="1.1"` 且 `event_type` 与行类型一致——`deleteFacts` 产出 + `HardDeleteObjectWithEvent` 落库形状在此钉死，admin/REST 入口共用同一 `svc.Delete`（D8） |
| 集成状态机（DSN 直查 + 真实 admin 路径） | `internal/integration/fullserver_test.go`：`TestAC2_AdminDelete_EventTypeFilteredState` | `startFullServerWithAuthAndRelay(t, nil, "opsecret:*:admin")`（**无 relay**，R4）；operator key PUT `acme/docs/a.txt`；`DELETE /v1/admin/files/acme/docs/a.txt?hard=1`（operator key）→ **204**；DSN：`outboxCountFor(dsn, obj.ID, deleted@1.1)==1`、`(notify@1.1)==1`、`outboxStatus=="pending"`（两事实）、`outboxPayloadFor` 含 `"schema_version":"1.1"`、`"tenant":"acme"`、`"key":"docs/a.txt"`；`SELECT COUNT(*) FROM event_outbox_delivered`==0。**events 包不新增测试**：pending→delivered 生命周期已被 `TestOutboxRelay_DeliveryLifecycle`（:144）+ `TestOutboxRelay_DeletedFactCompletesWithoutDelivery`（:272）证明；`delivered_at_ns>0` 在 AC-4 leg 2 以 DSN 断言 |

### 7.4 AC-3 envelope 与失效行为

| 腿 | 测试（文件 / 函数） | 断言要点 |
|---|---|---|
| builder 层 golden | `internal/events/schema_test.go` 既有 golden（:15/:115）**不动** | 钉死 `schema_version=="1.1"`、`event_type=="vault.file.deleted@1.1"`、字段序、无 records/无 invalidation flags（E11）。**R6 明示**：admin 路径 payload 与 golden 逐字节一致不可端到端断言；golden 钉 builder + AC-2 集成腿钉关键字段，两者组合即 AC-3 ① 的可执行形式 |
| 失效行为 | `internal/service/file_delete_test.go`：`TestAdminDelete_InvalidatesShareAndChunks` | 预置 `shares` 行：`repo.(access.Store).CreateShare(ctx, access.Share{TenantID:"acme", Bucket:"default", Key:"docs/a.txt", …})`（`access.Store` cast 先例 authz_cli_failclosed_test.go:99）；`mockChunkCleaner`（service_test.go:303 先例）+ `svc.WithChunkCleaner`。hard 删后：`repo.(access.Store).ListShares(ctx, "acme", "default", "docs/a.txt")` **0 行**（事务内失效，E12）、`DeleteObjectChunks` 被调（版本 ID）、`GetObject`→`ErrNotFound`、`store.Stat(storageKey)` 不存在；soft（hard=false）：blob 仍在、行 `deleted_at` 置位 |
| actor | 同文件经 `assertDeleteAuditRow(t, repo, actor, "hard", "acme", obj)` | **审计行** actor == `access.WithPrincipal` 注入的 principal 值（payload actor 接口面不可读，R6）；无 principal ctx 时 actor 为空串合法（C9） |

### 7.5 AC-4 组合 e2e：真实服务器 + 真实 CLI（信号式非阻塞 + relay-down 恢复 + 启用鉴权）

`internal/integration/fullserver_test.go`：`TestComposition_AdminFilesDeleteEndToEnd`——**双 harness**，真实 `cli.Run` 驱动（authz_cli_failclosed_test.go:78 先例）。两 leg 均先 `t.Setenv("AERO_ENDPOINT", ts.URL)`、`t.Setenv("AERO_API_KEY", "opsecret")`、**`t.Setenv("AERO_TENANT", "")` 必须钉死**（`cli.do` 仅在 env 非空时发 `X-Aero-Tenant`，否则继承开发者环境；authz_cli_failclosed_test.go:105）。

**Leg 1（信号式非阻塞主证明，:685 形状）**——`startFullServerWithAuthAndRelay(t, &opts, "opsecret:*:admin")`，其中 `opts := events.EventOutboxRelayOptions{PollInterval: 50*time.Millisecond, BatchSize: 32, ClaimTTL: 30*time.Second, HTTPTimeout: 5*time.Second, MaxAttempts: 3, AuditSink: sink}`，L2 handler 阻塞在 `<-release`（sink 构造镜像 :700-719）：① operator key PUT `acme/docs/a.txt`（`Authorization: Bearer opsecret` + `X-Aero-Tenant: acme`——operator key 不被中间件盖章，须显式带头）② 后台 goroutine 跑 `cli.Run(["admin","files","delete","acme","docs/a.txt","--hard"])` ③ L2 仍阻塞时 4s hang-guard：同步实现无法返回（其 POST 会挂到 5s 超时）→ 4s 内退出码 **0** + stdout `deleted` ④ 事实 `pending` 或 `inflight`（delivered 在目标阻塞时不可达，race-free）⑤ release → 15s 轮询 `outboxStatus=="delivered"`。

**Leg 2（relay-down 恢复 + start-later wiring）**——`startFullServerWithAuthAndRelay(t, nil, "opsecret:*:admin")`（**无 relay**）：① operator PUT `acme/docs/a.txt` ② `cli.Run([..."delete","acme","docs/a.txt","--hard"])` → 退出码 0（**F9 relay-down**：relay 不在运行，删除照常成功——配合 AC-2 集成腿的 pending 断言，非阻塞的弱形式）③ GET `/v1/files/docs/a.txt`（tenant=acme）→ 404；`repo.(access.Store).ListShares` 无该对象行 ④ DSN：deleted@1.1 与 notify@1.1 均 `pending`（:1072 轮询形状）⑤ **start-later relay wiring**（harness 无此 API——relayOpts==nil 时 harness 不启动 relay（`if relayOpts != nil`，:147），测试内联构造，先例 :1117）：
```go
relay := events.NewEventOutboxRelay(h.repo,
    slog.New(slog.NewTextHandler(io.Discard, nil)), events.EventOutboxRelayOptions{
        PollInterval: 50 * time.Millisecond, BatchSize: 32,
        ClaimTTL: 30 * time.Second, HTTPTimeout: 5 * time.Second, MaxAttempts: 3,
    })
relayCtx, relayCancel := context.WithCancel(context.Background())
go relay.Run(relayCtx)
t.Cleanup(relayCancel) // LIFO：先于 harness 注册的 ts.Close/repo.Close 执行
```
⑥ 15s 轮询两事实 `delivered`；`deliveredAt(t, h.dsn, obj.ID, "vault.file.deleted@1.1") > 0` ⑦ **JOIN 断言（R2）**：`deliveredCountFor(dsn, obj.ID, deleted@1.1)==1`、`(notify@1.1)==1`，且**无过滤全表恰 2**（`SELECT COUNT(*) FROM event_outbox_delivered`）——无 join 的"恰 1 行"必失败 ⑧ `GET /v1/admin/audit`（`Bearer opsecret`）响应体含 `"action":"file.delete"`、`"tenant_id":"acme"`、`"detail":"hard"`。

### 7.6 新 handler 路径失败模式表驱动测试（F1–F8，本版新增）

`internal/api/rest/admin_files_delete_test.go`：`TestAdminDeleteFile_ErrorMapping`——表驱动（name / setup / 请求 / wantStatus / wantCode / 残留断言）。共享 env helper `newAdminDeleteEnv(t, cfg)`：真实 repo+storage+svc（可选 `failDeleteStore` 包装、`svc.WithAuthorizer(manager)`）+ `NewRouter`（真实路由表）+ `reg.Middleware()`；每行独立子测试。`denyAuthorizer`（file_delete_test.go:57-69）**不可用于 F2**（全拒，无法判别 `tenant_mismatch`）。

| 行（§5） | 环境 | 请求 | 断言（falsifiable） |
|---|---|---|---|
| **F1** 非 admin 403 | `reg=auth.Parse("user:default:read+write,opsecret:*:admin")`；PUT `acme/k.txt` | `Bearer user` `DELETE /v1/admin/files/acme/k.txt?hard=1`；对照组 `Bearer opsecret` | **403** code `Forbidden` msg `admin scope required`；对象仍在；`HasEventOutboxFact` 两类型 false；`ListAudit` 无 `file.delete` 行。匿名（无 Authorization）→ **401** |
| **F2** 租户限定 key 跨租户 403 | `reg=auth.Parse("adm:acme:admin,opsecret:*:admin")`；`access.NewManager(repo.(access.Store), access.Config{Enabled: true, DefaultPolicy: access.DefaultTenant, ShareSecret: <32 字节>})`（先例 authz_delete_denied_test.go:29-41；`DefaultTenant` 使同租户正臂走 `tenant_default` 放行，隔离 mismatch 检查）+ `svc.WithAuthorizer(manager)`；**同 key 在 `acme` 与 `other` 两租户各 PUT 一份**——`svc.Delete` 先 `GetObject` 后 `authorizeObject`（file_delete.go:152-161），目标租户无对象时是 404 而非 403，必须双租户种子 | `Bearer adm` `DELETE /v1/admin/files/other/k.txt?hard=1` | **403** code `AccessDenied`，message 含 `tenant_mismatch`（authorizer.go:29，`tenantMatches` :67-69）；**两租户副本均仍在**、零 outbox/audit。正臂：`Bearer adm` 删 `acme/k.txt` → 204；`Bearer opsecret` 删 `other/k.txt` → 204。附：`X-Aero-Tenant: other` + `adm` → 中间件 403（auth_middleware.go:64，N4，handler 前） |
| **F3** 404 | 无对象（或已删） | `DELETE /v1/admin/files/acme/missing.txt?hard=1` | **404** code `NotFound`；**重试同请求 → 仍 404 且零 outbox/audit 残留**（幂等，F3 表） |
| **F5** WORM 409 | PUT `acme/k.txt` → `svc.SetObjectRetention(ctx, "acme", DefaultBucket, "k.txt", "", "GOVERNANCE", time.Now().Add(time.Hour))`（setup 直接调 svc，无 authorizer 即放行） | hard → | **409** code `ObjectLocked`（`classifyLock` management.go:224-231；**非 412**，R5）；零 outbox/audit、行不变（`LockedUntil` 仍置位）。软删（无 `?hard=1`）→ **204**（F5 表：软删不受锁影响） |
| **F6** corrupt 410 | 直接调用 `adm.writeError(w, r, service.ErrObjectCorrupt)`（in-package 可达 D6 新增代码；`classify` handler_helpers.go:57） | — | **410** code `ObjectCorrupt`。**行为补充**（实测：corrupt 对象 `Get`→`ErrObjectCorrupt` 但 `Delete` 成功——410 是 Get/Stat 时语义，删除路径不可达）：`SetObjectMetaKey("_aero_scrub_status", "corrupt")` 对象经 admin 路径硬删 → **204**（新 handler 不得引入 410） |
| **F7** storage 500 | `failDeleteStore{storage.Storage}`（仅覆盖 `Delete` 返回 error；包装形状先例 quota_failure_test.go） | hard | **500** code `InternalError`；**零残留**：`GetObject` 仍在、`HasEventOutboxFact` false、`ListAudit` 无 `file.delete`（storage-first 顺序 file_delete.go:14-17：blob 失败 → 事务未执行） |
| **F8** audit 失败回滚 | 经 **DSN 第二连接**（`sql.Open("sqlite", dsn)`，`outboxStatus` 同款；实测与 repo 池并发安全）执行 `ALTER TABLE audit_log RENAME TO audit_log_bak` | hard | **500** code `InternalError`；`GetObject` 仍在、`HasEventOutboxFact` false；rename 回后 `ListAudit` 无 `file.delete` 行。**仓库层 seam 测试**（`internal/repository/event_outbox_test.go`：`TestHardDeleteAuditInsertFailure_RollsBack`，in-package `store.db.Exec` rename）：同一注入下 `HardDeleteObjectWithEvent` 返回错误且对象/outbox/audit 全量回滚——原子性在共享 seam 钉死（R7）；handler 的 500 呈现与 F7 同一条 `writeError` 路径 |

**回归（随 AC 一并纳入）：** `make check` 全绿；`go test ./internal/cli/ ./internal/api/rest/ ./internal/repository/ ./internal/service/ ./internal/events/ ./internal/integration/`；`git status --porcelain` 确认服务层/仓库层/迁移/中间件零改动、测试基建改动仅限 fullserver_test.go harness 重构（test-only，本设计生产面仍仅 §3 四文件）。

---

## 8. 范围边界（承接规格 §3/§5，设计不越界）

- `rm`/`cmdRemove` 行为 —— 不改（C1）。
- `FileService.Delete` 签名 / 服务层逻辑 —— 不动（D8）；admin 端点与 REST 端点共用同一路径。
- outbox/relay/schema 生产代码 —— 不动（0041 + `deleteFacts` + `EventOutboxRelay` 已落地，本方向零新增）。
- 中间件链（I4）/存储 key 不变量（I3）/迁移文件（I2）—— 零触碰。
- `AuditEntry`/`ListAudit` 渲染增强（detail 展示、分页等）—— 属分析中方向 2，不在本方向 scope（FR-3 明示）。
- bucket 参数 / `--bucket` flag —— 不加（D7；恒 `DefaultBucket`，方向签名不含 bucket）。
- 新增遥测计数器 —— 不加（无新失败面，既有 telemetry 覆盖）。
- `docs/api.md`/`docs/configuration.md` —— 无新配置项，无需 configuration 更新；如实现时触及 api.md 的 admin 路由清单，仅补 1 行（增量文档，非设计交付物）。
- CLI 测试与既有 `TestCmdAdmin_UnknownResource_Returns2`（cli_admin_test.go:33）兼容 —— `files` 成为合法资源后该测试不受影响（它测的是未知资源字符串）。

---

## 9. 实施净改动清单（实现阶段逐一核销）

1. `internal/cli/cli_admin.go`：`cmdAdmin` switch 加 `case "files"`；新增 `cmdAdminFiles` + `adminFilesDelete`（§3.1 形状，含空租户拒绝 F13）；`adminUsage()` 加 `files delete` 条目。
2. `internal/cli/cli.go`：顶层 `usage()` admin 段同步加同一行。
3. `internal/api/rest/admin.go`：新增 `DeleteFile`（含空租户 400 守卫 F13）+ 4 行 `writeError` 包装（D6/N1 修正）。
4. `internal/api/rest/router.go`：admin 组内注册 `r.Delete("/admin/files/{tenant}/*", adm.DeleteFile)`；路由表加 1 条目（D9）。
5. **行数门禁预案**：`cli_admin.go` 当前 454 行——`cmdAdminFiles`/`adminFilesDelete` 直接放**新文件** `internal/cli/cli_admin_files.go`（≤500 行门禁，新文件独立计数），cli_admin.go 仅改 2 行（switch case + adminUsage 条目），两侧均远离 500 上限；`adminUsage()` 与 switch 的相邻性无硬性要求（同包即可）。
6. 测试：§7 全部（CLI 6 个 + REST 3 函数含 7.6 失败模式表 + 服务 2 个 + 仓库 2 个 + 集成 2 个；events 包零新增——golden 与 relay 生命周期既有测试覆盖，见 §7.3）。
7. 验收核销：AC-1…AC-4 映射表逐项对应测试函数名；`make check` 全绿。

**与规格的差异记录（设计阶段修正，全部为规格文本不精确处的落实）：**
- 规格 FR-2「失败 `h.writeError(w, r, err)`」→ 实际 `AdminHandler` 无此方法，设计 D6 新增包装（N1）。
- 规格 FR-2「tenant 取 chi 参数（对齐 DeleteTenant，admin.go:329）」→ 实际 :326/:330，语义一致（E4）。
- 规格 AC-2「服务层测试 = admin 入口语义」→ 服务层无 admin 专属路径（同一 `svc.Delete`），AC-2 的服务/仓库断言即入口语义证明；admin 专属断言（requireAdmin/路由/CLI 形状）在 AC-1 REST 侧覆盖（§7 已映射）。
