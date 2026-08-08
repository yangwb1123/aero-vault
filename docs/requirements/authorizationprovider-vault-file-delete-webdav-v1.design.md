# 设计：vault.file.delete 的 fail-closed 强制 —— AuthorizationProvider 端口 + admin-delete action（WebDAV 删除面，配套设计文档 v1）

> **配套规格：** `docs/requirements/authorizationprovider-vault-file-delete-webdav-v1.md` · **模块：** `internal/api/webdav` + `internal/service`（删除路径）+ `internal/access`（action 词汇） · **状态：** 设计已定稿，未实现 · **日期：** 2026-08-06
> 本文是规格的落地设计：证据复核结论（含 1 处规格事实修正、2 处设计补充）、API 变更、设计决策、兼容性约束、失败模式、迁移步骤、验收映射、最终代码形态。
> **v1.1 修订（对抗审查 Finding A 收口）：** `DeleteBucket` 级联删除纳入门禁——per-object 授权改 `deleteAction(true)`（D8）；§0.2 N6、§0.3 R4、§1、§2 D8、§3、§4 F10/F11、§5.1/§5.2/§5.3、§6/§6.1、§7.7、§8 已同步。

---

## 0. 证据复核结论（对规格逐条核验 + 本阶段新增证据）

### 0.1 逐条复核（对照工作树 HEAD `acfaaf4` 实读，非仅信引文）

规格全部 18 条证据（E1–E18）**均属实**，行号与工作树一致：

| # | 规格引用 | 复核 | 结论 |
|---|---------|------|------|
| E1 | access.go:83-104，:91 `s.authorizer == nil → return nil`（fail-open） | 实读一致（:91-93 三元分支；:95 `Authorize`；:99-101 包 `ErrForbidden`） | ✅ |
| E2 | authorizer.go:10-12 端口接口；:20-22 `!m.cfg.Enabled → Allowed:true` | 实读一致 | ✅ |
| E3 | types.go:76 `ActionDelete="object:delete"` 唯一删除 action；:84-93 `ValidAction` | 实读一致；仓库无 `"vault.file.delete"` 权限字面量（仅 @1.1 事件类型/审计动作，不同命名空间） | ✅ |
| E4 | file_delete.go:147-169 `Delete`：GetObject→:159 授权→quota 预检→hard/soft | 实读一致 | ✅ |
| E5 | file_delete.go:174-214 `DeleteVersion`：:179 永久删除同 action | 实读一致 | ✅ |
| E6 | dav.go:141-145 `RemoveAll` 恒 `Delete(..., true)`；ErrNotFound→`os.ErrNotExist` | 实读一致 | ✅ |
| E7 | dav.go:150-205 `Rename` copy-then-delete；:198-202 回滚 warn-only | 实读一致 | ✅ |
| E8 | http.go:143-172 12 环链；WebDAV 经 dispatcher 同链 | 实读一致（链表 access_log→…→request_id） | ✅ |
| E9 | auth_middleware.go:138-146 无凭据→401 或匿名；:177-186 `withAnonymousPrincipal`（:184 `PrincipalAnonymous`） | 实读一致 | ✅ |
| E10 | cmd/server/access.go:12-14 `!Enabled → nil,nil`；main.go:94/:215 `WithAuthorizer`；file.go:91/:97-98 字段+注释 | 实读一致 | ✅ |
| E11 | **x/net/webdav v0.55.0** webdav.go:242-270 `handleDelete`：Stat 非 NotExist→**405**、`RemoveAll` 任意错误→**405**、成功→204 | **模块源码实读**（GOMODCACHE 内） | ✅ |
| E12 | 同版本 file.go:614-639 `moveFiles`：`Rename` 错误→**403**、Overwrite=T 目标 `RemoveAll` 错误→403 | **模块源码实读** | ✅ |
| E13 | file_get.go `statObject` :151 `authorizeObject(ActionRead)`（Stat 带读授权） | 实读一致 | ✅ |
| E14 | `hardDeleteObject`（:18-56）次序：chunk→storage blob→`HardDeleteObjectWithEvent`（事务内 outbox @1.1+audit）→quota→emit；授权在其前 → deny 零副作用 | 实读一致 | ✅ |
| E15 | rest/handler.go:244-245 `?hard=1`；mcp/server.go:311 恒软删；s3compat/delete.go:19/:32 `DeleteVersion`/`Delete(hard=true)` | 实读一致 | ✅ |
| E16 | context.go:11 `PrincipalFrom`；authorizer.go:126-133 `isAdministrator` 含 `vault.file_admin`；:81-88 `actionMatches` 无 admin-delete 映射 | 实读一致 | ✅ |
| E17 | dav_test.go `newTestServerWithSvc`（nil provider；实际 :43-73，规格引 :53-73 漂移 10 行；`newTestServer` :29 与 `newTestServerSvc` :36 均委托之——单点装配覆盖三 helper）；:139 删除用例；:282/:315/:355/:415/:863 MOVE 族（:415 期望 **403**）；:673 <500；:823 `deleteFailStorage`；:455 `TestTenantIsolation` | 实读一致（:415 `TestMoveMissingSource` 断言 403 属实） | ✅（行号微漂移已修正） |
| E18 | s3compat/rest 测试装配行号 | grep 逐一命中（handler_test:33、versioning_test:192、sigv4_test:38、policy_test:223、managed_sse_test:32、authz_gate_test:82/217/341/437、rest acl_test:32、admin_ops_test:35、buckets_test:36/:71） | ✅ |

### 0.2 本设计阶段新增证据（N1–N6：2 处规格事实修正 + 4 处设计补充）

- **N1（规格 §4 迁移表事实错误）**：规格称 `fullserver_test.go`（经 cmd/server 生产装配，manager 已注入）"无需迁移"。**实读证伪**：`internal/integration/fullserver_test.go:75` 是 `service.NewFileService(store, repo, logger)`——**nil authorizer**，装配为包内复制（:103-125 自建 chi 路由 + `dav.Handler` + dispatcher），非 cmd/server 装配。其硬删调用点共 **6 处**：`deleteWithTenant(t, …, true)` :820/:835/:853（`TestComposition_AuditSinkL2BoundTenant`）、:924（`TestComposition_DeleteDeliversBothFacts`）、:1065（`TestComposition_MidClaimRestartRedeliversOnce`），另 **:731 为 `TestDeleteResponse_DoesNotBlockOnDelivery` 内裸 `?hard=1` 请求**（再审计补入；同 harness）——fail-closed 下全部 403 → **必须迁移**（§5.2 补入，harness 单点替身覆盖全部 6 处）。
- **N2（设计补充，系统 principal 豁免）**：`internal/antivirus/worker.go:177-181` 以 `access.WithPrincipal(ctx, Principal{Kind: PrincipalSystem, …})` 运行 `QuarantineObjectByID`（object_worker.go:50-87）；该函数对 tombstone 版本（:65）调用 **`DeleteVersion`**。若规格 FR-1a 的 fail-closed 无豁免，"AV 启用 + Access 未启用"（两个独立 opt-in，默认 AV off 但 `JOBS_WORKERS>0` 时 AV 可单独开）的生产形态下，隔离 tombstone 版本将 `ErrForbidden` → job 重试耗尽 → `failed` 终态。`access.Manager` 对 `PrincipalSystem` 本就 `trusted_system` 放行（authorizer.go:24-26）——**nil-provider 的 fail-closed 应与 Manager 的信任边界对齐**（D3）。
- **N3（FR-2b 的载荷证明）**：在途 `internal/integration/authz_parity_test.go`（`svc.WithAuthorizer(manager)` :119 + S3/REST 双门禁，X-Test-Principal 注入）步骤 1（:192）与步骤 3（:241）断言：alice 仅有 `ActionDelete allow` ACL 时 S3 DELETE → **204**（行号勘误：设计初稿引 :119 为断言行，实为装配行）。若服务层硬删换 `ActionAdminDelete` 而无超集映射，该测试在合取门禁下必失败——**FR-2b 是使在途 parity 测试保持通过的必要条件**（D2）。该文件当前 untracked，须随方向提交。
- **N4（§5.2 装配表遗漏一：webdav 双装配点）**：`dav_test.go:840-861` `newRollbackServer` 独立构造 `service.NewFileService(store, repo, nil)`（:856），`TestMoveRollbackOnDeleteFailure`（:863）经它装配——设计稿声称 ":823/:863 由 `newTestServerWithSvc` 单点覆盖"不实。flip 后 MOVE 预检（D6）直接 403，storage-失败回滚路径永不可达 → 该 helper 须同加 allow 替身。
- **N5（§5.2 装配表遗漏二：REST 硬删用例）**：`handlers_test.go:40` `setupTest`（`TestIdempotency_HardDeleteWithFreshKey` idempotency_query_test.go:89——:96 为 `?hard=1` 请求行，期望 204）与 `management_test.go:34` `newManagementRESTTest`（`TestLockObject` :132 硬删期望 **409**，授权先于锁检查 → flip 后 403）均遗漏；而设计稿所列 acl_test/admin_ops_test/buckets_test 三行实读无对象 DELETE 请求（no-op）。
- **N6（🔴 Finding A 收口：`DeleteBucket` 级联漏网 + 装配表第三处遗漏）**：对抗审查（`delete_entrypoint_auditor.md` 🔴 A）实证：`DeleteBucket`（file_bucket_settings.go:38-66）以 bucket-scope `ActionDelete`（:40）+ **per-object `ActionDelete`**（:48）授权，随后 `deleteBucketData`（file_bucket_delete.go:55-93）绕过 `svc.Delete` 直连 `store.Delete`（:78）摧毁全部对象——**nil provider 下整桶歼灭不受新门禁约束**，与 FR-1a 宗旨直接矛盾。入口：REST `bucket_handlers.go:227`、S3 `s3_bucket_handlers.go:333`（handler 前置空桶检查 :329-336，per-object 循环零迭代）、CLI `bucket-rm`（cli_admin.go:437 → REST）。**顺带修正同族第三处装配遗漏（N4/N5 同款）：** 设计稿 §5.2 称 `newTestSvc` 覆盖 usage_consistency_test——实读 `TestHardDeleteRemovesEveryVersionAndUsage`(:121) 与 `TestDeleteBucketRemovesBlobsAndUsage`(:147) 均用 **`newQuotaTestSvc`**（quota_test.go:15，独立 `NewFileService(store, repo, nil)`），须单独加 allow 替身（§5.2 新行）。

### 0.3 对规格的修正/补充（不改变验收语义）

| # | 规格原文 | 本设计裁定 | 依据 |
|---|---------|-----------|------|
| R1 | FR-1a：`authorizer == nil && action == ActionAdminDelete → ErrForbidden`（无豁免） | **增加 `PrincipalSystem` 豁免**（authorizer 非 nil 时由 Manager 的 trusted_system 路径处理；nil 时对齐该边界） | N2；I5（nil 依赖不破坏 core 路径；AV 隔离是系统路径） |
| R2 | FR-6a：MOVE 预检插在 copy（Get/填充）**之后**、`svc.Put` 之前 | **提前到 source Get 之前**（fail-fast）。可观测等价：缺失源两案皆 `ErrNotFound → moveFiles → 403`（`TestMoveMissingSource` 不变）；deny 路径免去整对象流式入 spill buffer 的浪费 | 语义等价证明 + 效率/安全 |
| R3 | §4 迁移表："fullserver_test.go 无需迁移" | **证伪**，补入迁移（harness :75 加 allow 替身） | N1 |
| R4 | §8 范围守卫："`DeleteBucket`/bucket 子资源/bucket policy —— 非对象删除，范围外" | **部分证伪**：bucket 子资源与 bucket policy 确非对象删除，但 **`DeleteBucket` 级联就是对象删除**（每对象 `store.Delete`，N6）。裁定：per-object 授权改 `deleteAction(true)`（D8）；bucket-scope `ActionDelete`（:40）与空桶删除不变 | N6/Finding A |

---

## 1. API 变更

**零配置变更、零 schema 变更、零 `go.mod` 变更（I6）、零中间件链改动（I4）、零 openapi.json 改动（无 REST 端点变化）。** 变更集中在 3 个包、8 个文件（其中 2 个是纯新增测试文件），全部为**加法**（1 个新 action 常量、1 个新导出方法、1 个 `actionMatches` 分支、4 处行为翻转点）：

| 层 | 变更 | 兼容性 |
|----|------|--------|
| `internal/access/types.go` | 新增 `ActionAdminDelete Action = "vault.file.delete"`（Action 常量块，types.go:76 旁）+ `ValidAction` 增加 case（:84-93） | 纯加法；ACL 端点可为新 action 建 allow/deny 条目；既有 action 字面量不变 |
| `internal/access/authorizer.go` | `actionMatches`（:81-88）新增超集分支：`wanted == ActionAdminDelete → granted == ActionDelete` | 纯加法；`ActionAll`/相等命中已由首行覆盖；`capabilityAllows`/`matchingEntries`/`scopeAllows` 零改动（均经 `actionMatches` 或按 write scope 走，与 `object:delete` 现状一致） |
| `internal/service/access.go` | `authorize()`（:83-104）nil 分支：`ActionAdminDelete` → `ErrForbidden`（`PrincipalSystem` 豁免），其余 action 保持 nil-allow（I5） | 行为翻转**仅限**新 action；软删/读/写/列表基线逐字节不变 |
| `internal/service/file_delete.go` | 新增私有 `deleteAction(hard bool) access.Action`（单一事实源）+ **新增导出 `AuthorizeDelete(ctx, tenant, bucket, key string, hard bool) error`**；`Delete` 授权行改调 `deleteAction(hard)`（:159）；`DeleteVersion` 授权行换 `ActionAdminDelete`（:179） | `Delete`/`DeleteVersion` 签名不变（编译期零破坏）；`AuthorizeDelete` 为纯新增方法（FR-4，非读授权） |
| `internal/service/file_bucket_settings.go` | `DeleteBucket` per-object 授权行（:48）改 `s.authorizeObject(ctx, deleteAction(true), obj)`；bucket-scope 行（:40）不动 | 签名不变；门禁仅作用于对象销毁面，bucket 管理面保持软删基线（D8，N6 收口） |
| `internal/api/webdav/dav.go` | `Handler()` 包装器（:41-51）增补 DELETE 预检；`Rename`（:150-205）增补源删除预检（置于 source Get 前） | 包装器/`davFS` 均包内实现，无外部签名；`RemoveAll` 本体不改（service 仍为权威判定） |
| 测试 | 新增 `internal/service/authz_gate_test.go`、`internal/api/webdav/authz_gate_test.go`、`internal/access` 映射用例；既有 helper/装配点加 allow 替身（§5.2） | 测试文件零改写；仅 helper 一行 |

**行为变化（默认部署 = `ACCESS_ENABLED=false` → nil provider）：**

| 端点 | 现状 | 变更后 | 依据 |
|------|------|--------|------|
| WebDAV `DELETE /webdav/{key}` | 204，硬删成功 | **403**（预检渲染；对象存活） | E6/E11/E14；AC-4 |
| WebDAV `MOVE`（源删除面） | 201/204 | **403**（预检；copy 未发生） | E7/E12；AC-5 对照 |
| REST `DELETE /v1/files/{key}?hard=1` | 204 | **403** | E15；AC-1 的 HTTP 形态 |
| S3 `DELETE /{bucket}/{key}` / `?versionId` | 204 | **403**（service 层；adapter 在途门禁亦 403，合取一致） | E15 + s3compat 规格 |
| `DeleteVersion`（含 AV 隔离 tombstone 路径） | 成功 | **403，除 `PrincipalSystem` 上下文** | N2/D3 |
| REST `DELETE /v1/buckets/{bucket}`（级联，非空桶；CLI `bucket-rm` 同源） | 204，整桶歼灭 | **403**（首个对象即拒，零 mutation；空桶 204 不变） | N6/D8 |
| S3 `DELETE /{bucket}` | 204 | **不变**（handler 前置空桶检查 :329-336，per-object 循环零迭代；409 BucketNotEmpty/404 NoSuchBucket 语义不变） | 🔒 `TestDeleteBucketRequiresEmptyBucket` |
| 软删（REST 默认、MCP、lifecycle/retention、bucket 子资源） | 不变 | **不变**（`ActionDelete` nil-allow 基线） | FR-3；I5 |

---

## 2. 设计决策

### D1 — action 字面量即 COMPOSE 权限名，无独立权限注册表

`ActionAdminDelete Action = "vault.file.delete"` 直接使用 COMPOSE 权限字符串（仓库现无该字面量，E3 核实）。不新增任何权限注册表/配置项——action 常量即权限名的唯一载体。`ValidAction` 纳入，使 ACL 端点（`/v1/acl`）可对 admin-delete 建条目；**显式 deny 是"admin 区分"的表达载体**（D2）。

### D2 — Manager 超集映射（`object:delete` ⊨ `vault.file.delete`）+ 显式 deny 区分

`actionMatches` 追加 `if wanted == ActionAdminDelete { return granted == ActionDelete }`。三点论证：

1. **回归保全**：既有 `object:delete` allow ACL 的硬删/版本删在迁移后不得失效（AC-6 与在途 `authz_parity_test.go` 步骤 3 的 204 断言——N3）。
2. **区分语义**：`vault.file.delete=deny` 条目（`matchingEntries`/`hasEffect` 现状逻辑直接命中，无新代码）→ 硬删被拒、软删放行；`ActionAll` 与相等命中由 `actionMatches` 首行覆盖，不需特判。
3. **sibling PDP**：可自行实施更严策略（端口语义 = provider 是权威，D5 不覆盖）。

`capabilityAllows`/`scopeAllows` 不改（FR-2c）：`scopeAllows` 对非读 action 走 `write` scope，与 `object:delete` 今天的行为一致（DefaultTenant 策略下 write-scoped 主体保留硬删权——比今天的 fail-open 严格、比 ACL 严格模式宽松，属规格既定范围）。

### D3 — fail-closed 的 nil 检查与 Manager 信任边界对齐（`PrincipalSystem` 豁免）

```go
if s.authorizer == nil {
    if action == access.ActionAdminDelete {
        if principal, ok := access.PrincipalFrom(ctx); ok && principal.Kind == access.PrincipalSystem {
            return nil // 对齐 Manager trusted_system（authorizer.go:24-26）
        }
        return fmt.Errorf("%w: no authorization provider configured", ErrForbidden)
    }
    return nil
}
```

- **必要性**（N2）：AV 隔离工作器（antivirus/worker.go:177-181）以 `PrincipalSystem` 调用 `QuarantineObjectByID` → tombstone 路径 `DeleteVersion`。AV 与 Access 是相互独立的 opt-in；"AV 开、Access 关"是合法生产形态，严格 fail-closed 会使其 job 进入 `failed` 终态（retry 耗尽）。
- **安全性**：`PrincipalSystem` 仅由内部可信代码注入（AV worker；适配器/auth 中间件只产生 anonymous/user/service principal，E9），外部请求无法声明该 Kind。
- **一致性**：provider 非 nil 时 Manager 对系统主体 `trusted_system` 放行；nil provider 的默认策略 = "Manager 的缺省拒绝，但保留系统信任边界"——两个形态行为同构，无漂移。

### D4 — `AuthorizeDelete`：非读、非变更的授权方法（FR-4）

```go
func deleteAction(hard bool) access.Action {
    if hard { return access.ActionAdminDelete }
    return access.ActionDelete
}

func (s *FileService) AuthorizeDelete(ctx context.Context, tenant, bucket, key string, hard bool) error {
    tenant, bucket, err := checkedObjectDefaults(tenant, bucket, key)
    if err != nil { return err }
    obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
    if errors.Is(err, repository.ErrNotFound) { return ErrNotFound }
    if err != nil { return err }
    return s.authorizeObject(ctx, deleteAction(hard), obj)
}
```

- **不做读授权**（E13：`Stat` 带 `ActionRead`，会误伤"可删不可读"主体）、**不做 quota/变更**——`Delete` 的授权前半段 + action 选择，与 `Delete` 共享 `deleteAction` 单一事实源。
- `Delete` 内联逻辑不变（GetObject→授权→quota→删除，E4/E14 次序**逐字保持**；仅授权行参数从 `ActionDelete` 改为 `deleteAction(hard)`，避免 AuthorizeDelete 二次 GetObject 的重复取行）。`DeleteVersion` 授权行换 `ActionAdminDelete`（永久删除语义）。
- 返回 `ErrForbidden`（经 `authorize` 的 `%w` 包装，`errors.Is` 可判）与 `ErrNotFound`（供预检透传与 MOVE missing-source 语义，E17 :415 不变）。

### D5 — WebDAV DELETE 的 403 渲染：`Handler()` 预检 + 纵深防御（FR-5）

x/net/webdav v0.55.0 `handleDelete` 将 `RemoveAll` 任意错误硬编码 405、Stat 非 NotFound 错误 405（E11 模块源码实读）——service 层 fail-closed 的 `ErrForbidden` 经 `davFS.RemoveAll` 回传只能渲染 405，**无法满足"unauthenticated DELETE → 403"**。因此：

1. **预检门禁**在 `Handler()` 现有包装器（GET/HEAD Content-Type 预置同层）增补：仅 `Method == DELETE`、路径截取成功、非空、非目录后缀时调用 `fsys.svc.AuthorizeDelete(r.Context(), fsys.tenant(r.Context()), service.DefaultBucket, name, true)`：
   - `errors.Is(err, service.ErrForbidden)` → `http.Error(w, "forbidden", http.StatusForbidden)`，**不进入 `dav.ServeHTTP`**（403 的唯一来源）；
   - `ErrNotFound`/其他错误 → 透传（webdav 渲染 404/405，现状不变——AC-4 的 missing 用例 `<500`、目录/空 key 语义、锁 423 均不受影响）。
2. **放行路径**进入 `dav.ServeHTTP`：其内部 Stat→RemoveAll 由 service 再次权威判定（同请求同 principal/provider，无判定漂移；纵深防御）。
3. **锁语义**（FR-5c 接受）：预检先于 `confirmLocks`（webdav.go:247-251）——"被锁且被拒"返回 403 而非 423；"被锁且允许"仍由 webdav 渲染 423（既有锁测试走 allow 路径，不受影响）。

### D6 — MOVE 源删除预检置于 source Get **之前**（R2，fail-fast）

`Rename` 在 `f.svc.Get` 之前调用 `AuthorizeDelete(ctx, tenant, DefaultBucket, src, true)`；拒绝直接 `return err` → `moveFiles` 渲染 **403**（E12）。

- **与 FR-6a 原文（copy 后、Put 前）的可观测等价证明**：缺失源时两案均得 `ErrNotFound → 403`（`TestMoveMissingSource` :415 不变）；双拒绝（读+删）时两案均得 403（状态码相同，仅先验哪个错误）；"拒绝"不产生任何 mutation（Get 为纯读）。
- **严格更优**：deny 路径免去把整对象流式灌入 spill buffer（大文件场景的 IO/磁盘浪费与延迟），且授权失败不触发任何读取。
- **回滚语义不变**：Put 成功后的源删除失败仍走 :198-202 回滚 + warn（FR-6b）；新引入的"回滚删除自身被拒"角见 §4。

### D7 — 门禁触发面（逐路径证明）

| 请求 | 预检/门禁 | 拒绝渲染 | 变更 |
|------|-----------|----------|------|
| `DELETE /webdav/{key}` | Handler 预检 → `AuthorizeDelete(hard=true)` | **403**（预检）；405（预检后 RemoveAll 拒绝，仅状态型 provider 翻转可达） | 新 |
| `DELETE /webdav/`（空/目录） | 跳过预检（非对象路径） | 404/405 现状 | 无 |
| `MOVE /webdav/a → /webdav/b` | Rename 预检（源，hard）→ webdav 内部 Stat(dst) → [Overwrite=T] `RemoveAll(dst)` → `Rename` | 403（E12 任一步）；missing src 403 | 新（源）；目标覆盖删已由 service 判定+403 渲染 |
| `GET/HEAD/PUT/PROPFIND/OPTIONS/COPY/LOCK/UNLOCK` | 无预检 | 现状 | 无（AC-4 反向锁定） |
| REST `?hard=1` / 默认软删 / MCP / S3 | 经 `svc.Delete` 自身判定（无 405 问题，不需预检） | 403（`ErrForbidden` 映射现状） | 行为翻转仅 hard 路径 |
| AV 隔离 tombstone（系统上下文） | `DeleteVersion` → `ActionAdminDelete` | 豁免（D3） | 新（豁免后行为不变） |

### D8 — `DeleteBucket` 级联纳入门禁：per-object `deleteAction(true)`（Finding A 收口）

`DeleteBucket`（file_bucket_settings.go:38-66）的 **bucket-scope `ActionDelete`（:40）不动**（bucket 记录管理门禁；空桶删除零对象，不在 file-delete 语义内），**per-object 循环（:48）改 `deleteAction(true)`**——与 `Delete`/`DeleteVersion` 共享 `deleteAction` 单一事实源（D4）。三点论证：

1. **fail-closed 一致性**：`deleteBucketData`（file_bucket_delete.go:55-93）绕过 `svc.Delete` 直连 `store.Delete`（:78）——per-object 授权全量预检（:47-53 → `deleteBucketData` :58）是歼灭面的**唯一**服务层门禁；原 `ActionDelete` 在 nil provider 下 nil-allow 放行 = 整桶歼灭，与 FR-1a 宗旨直接矛盾（N6）。
2. **D2 超集 → 零回归**：Manager 下既有 `object:delete` allow 经 `actionMatches` 超集 ⊨ `ActionAdminDelete`，allow 策略的级联删除原样放行（AC-6）；新能力：`vault.file.delete=deny` 现在阻断整桶歼灭（REST/S3/CLI 三入口同源）。注意超集**单向**：`vault.file.delete`-only 授权仍**不能**删桶（bucket-scope `ActionDelete` 判定不满足）——保守语义，文档明示。
3. **零副作用次序**：授权全量预检在 `deleteBucketData` 之前完成（:47-53 → :58）——任一对象拒绝 → 零 blob 删除、零 outbox/审计行（AC-3 的桶级变体，F10）；与 F7 同族：授权先于 `checkObjectProtection`（:51），deny/nil 下含被锁对象的桶删除返回 403 而非 `ErrLocked`（allow 路径 `ErrLocked` 不变，`TestDeleteBucketRejectsProtectedObject` 走 allow 替身，F11）。

**D3 对账（内部桶删除的 carve-out parity）：** 全仓 grep 证实 `svc.DeleteBucket` 生产调用方仅 REST `bucket_handlers.go:227` 与 S3 `s3_bucket_handlers.go:333`（CLI `bucket-rm` 复用 REST）——**当前无任何内部/系统上下文桶删除路径**（AV 隔离为对象级 `DeleteVersion`；reconcile/GC/lifecycle/retention/upload-GC 定时器路径不触 `DeleteBucket`；admin `DeleteTenant` 拒删有数据租户）。豁免位于共享 `authorize()` nil 分支（D3），未来若新增系统上下文桶删除（如租户拆除自动化）将**自动继承**该豁免——parity 由构造保证，非逐点特判。

---

## 3. 兼容性约束

**硬约束（违反即失败）：**

1. **`Delete`/`DeleteVersion` 签名不变**；`access.Authorizer` 接口不变（`access.Manager` 与 sibling PDP 均原样实现，FR-1）；无新接口类型。
2. **软删面逐字节不变**：`ActionDelete` nil-allow 基线（I5）——MCP（server.go:311）、REST 默认、lifecycle/retention（file_features.go:167）、`CreateDeleteMarker`、bucket 子资源（CORS/encryption/website/logging/notification/policy/versioning/lifecycle 等元数据操作）与 `DeleteBucket` 的 bucket 管理面（bucket-scope `ActionDelete` :40、空桶删除）。**`DeleteBucket` 的对象销毁面例外（D8）：** per-object 授权（:48）升为 `deleteAction(true)`（fail-closed）。CI 基线 `go test ./...` 全绿是硬门禁。
3. **Manager 语义增量**：既有 `object:delete` ACL 在硬删路径不失效（D2 超集）；显式 `vault.file.delete` deny 是新区分维度；`scopeAllows`/`capabilityAllows`/`isAdministrator` 零改动。
4. **中间件链不动（I4）**：401（注册表启用、无凭据）与 403（fail-closed）分层保持；匿名公读路径不变（E9）。
5. **零新依赖、零 schema（I2/I6）**：无迁移文件、无配置项、无 `go.mod` 变更；单文件 ≤ 500 行、单函数 ≤ 50 行（新增代码均满足）。
6. **`AuthorizeDelete` 不改变任何删除语义**：与 `Delete` 同源（`deleteAction`），仅暴露"判定"为可复用 API；被拒零副作用由授权前置次序保证（E4/E14）。

**行为变化清单（默认部署下，全部为"从放行变为 403"的收紧）：** WebDAV DELETE/MOVE、REST `?hard=1`、S3 DELETE/`?versionId`（服务层；adapter 在途门禁一致）、`DeleteVersion`（含 AV tombstone——系统豁免）、REST `DELETE /v1/buckets/{bucket}` 非空桶级联（CLI `bucket-rm` 同源；S3 空桶路径不变）。**部署注意事项**：启用本特性后，默认部署的硬删全部被拒，运维需配置 `ACCESS_ENABLED=true` 或注入 sibling PDP；软删与常规读写不受影响。

**已知限制（非回归，实读确认属现状）：** "可删不可读"主体经 WebDAV DELETE → 405（`handleDelete` 内部 Stat 带读授权，E11/E13）、MOVE → 403（source Get 读授权）——今天即如此，本设计不改变该路径；文档记录，不修复（修复需 fork x/net/webdav，超出范围）。**意图锁定测试（G3）：** `TestWebDavDeletableButUnreadableRenders405`——Manager `object:read` deny + `object:delete` allow → DELETE 405 + 对象存活（预检放行、Stat 拒），锁死该限制为设计行为而非回归。

---

## 4. 失败模式

| # | 场景 | 行为 | 缓解/处置 |
|---|------|------|-----------|
| F1 | 默认部署（nil provider）任何硬删 | 403（`ErrForbidden: no authorization provider configured`）；零副作用（AC-3） | 设计意图；文档 + AC-1/AC-4 锁定；日志可辨（wrap message） |
| F2 | provider 返回 error（非 deny） | 预检：非 `ErrForbidden` → 透传 → webdav 405/404（现状映射）；`Delete` 路径：`%w` 包 `ErrForbidden` 前原样返回（:99-101 现状，不改） | 与现状一致；不泄漏 provider 错误文本给客户端；记 warn 日志。**测试（G9）：** `TestWebDavDeleteProviderErrorRenders405`（`errAuthorizer` → DELETE 405 非 500 + 对象存活 + 响应无错误文本 + warn 日志） |
| F3 | 预检通过后 `RemoveAll` 被拒（有状态/竞态 provider 决策翻转） | DELETE → 405（webdav 硬编码） | 纵深防御下残余面；同请求同 ctx 同 provider，仅外部状态翻转可达；**回归锁（G1）：** `TestWebDavDeleteDecisionFlipRenders405`（有状态翻转替身：`ActionRead` 恒 allow、`ActionAdminDelete` 第 1 次 allow 第 2 次 deny）→ 405 + 对象存活 + outbox 0 行 |
| F4 | MOVE：预检过 → Put 成功 → 源删除被拒（provider 按 key 区分 deny） | 回滚删除 dst 同样被拒 → **warn + dst 残留**（:201-202 既有 warn-only 契约） | FR-6b 接受；新增日志字段 dst；不改回滚为阻断（ChunkCleaner 同款先例，AGENTS.md §2.1）。**测试（G2）：** `TestMoveRollbackDeniedLeavesOrphan`（有状态替身 + slog 捕获 → MOVE 403、GET src/dst 均 200、warn 含 dst；既有 `TestMoveRollbackOnDeleteFailure` 只覆盖 storage 失败且其装配见 N4——N4 替身布线后该测试恢复非 vacuous：Put 真实执行、注入的 storage 失败真实触发、回滚真实运行；webdav 复核建议的 `deleteFailStorage` fired 标志为可选加固，接受为不改） |
| F5 | AV 隔离 tombstone 版本 + nil provider | **放行**（`PrincipalSystem` 豁免，D3） | 若无豁免：job retry→`failed` 终态（N2）。测试三层：AC-1(e)（service 单元）；`TestQuarantineFailClosedCarveOut`（tombstone + system ctx 直接 `QuarantineObjectByID` → nil，user ctx 对照 → `ErrForbidden`）；🔒 **零改动哨兵（G14）** `TestWorkerQuarantinesOnlyInfectedVersion`（antivirus_test.go:145——完整 worker 路径，tombstone 分支恰是 flip 后唯一会 403 的既有路径） |
| F6 | tenant disabled + 硬删 | `requireActiveTenant` 先行 → `ErrForbidden`（次序不变，access.go:85-90） | 现状语义。**测试（G10）：** `tenant_status_test.go` 增用例——allow 替身 + disabled tenant 硬删 → `errors.Is(ErrForbidden) && errors.Is(ErrTenantDisabled)`（证明租户门禁先于 admin-delete 门禁；既有用例未覆盖 Delete） |
| F7 | 被锁资源 + 删除被拒 | 403 而非 423（预检先于 `confirmLocks`） | FR-5c 明示接受；allow 路径 423 不变。**测试（G8）：** `TestWebDavLockedDeniedDeleteRenders403Not423`（LOCK 后无 token DELETE：deny 替身 → 403、allow 替身 → 423 对照）。**同族边缘（webdav 复核点名，明示接受、不加测试）：** denied DELETE 携带 malformed `If` 头 / denied MOVE 携带 malformed `Destination` → **403 而非 400**——authz-first 优先序（同 F7 权衡；x/net/webdav 的 400 校验在 `confirmLocks`/`handleCopyMove` 的 Destination 校验之后才可达，deny 时客户端本就无权使用锁/目标，无新增信息泄露面；现有测试无一覆盖，接受为文档化偏差） |
| F8 | MOVE missing source | `AuthorizeDelete → ErrNotFound → 403`（moveFiles 映射） | `TestMoveMissingSource` 断言不变（AC-6） |
| F9 | 预检的 GetObject 存储/DB 错误 | 非 `ErrForbidden` → 透传 → webdav Stat 复现 → 405（现状） | 与今日 Stat 失败路径同语义。**测试（G11，低优先可选）：** `TestWebDavDeletePrecheckRepoErrorRenders405`（`objectLookupFailureRepository` 先例） |
| F10 | `DeleteBucket` 级联（非空桶）+ nil provider / deny | 首个对象即 **403**（`ErrForbidden`）；授权全量预检先于 `deleteBucketData`（:58）→ 零 blob/outbox/审计副作用；空桶（零对象）→ 204 不变 | D8 设计意图（N6 收口）。**测试：** G15 `TestDeleteBucketFailClosedWithoutProvider`（service：403 + 对象/桶存活 + 零副作用）、G16 `TestDeleteBucketUsesAdminDeleteAction`（recording：每对象 `ActionAdminDelete` + bucket-scope `ActionDelete`）、G17 `TestRestDeleteBucketFailClosedWithoutProvider`（REST e2e 403/204 对照）、🔒 S3 `TestDeleteBucketRequiresEmptyBucket`（空桶 409/204/404 序列不变） |
| F11 | `DeleteBucket` + 含被锁/WORM 对象 + deny/nil provider | **403**（授权先于 `checkObjectProtection` :51，F7 同族）而非 `ErrLocked`；allow 路径 `ErrLocked` 不变 | 既有 `TestDeleteBucketRejectsProtectedObject`（object_protection_test.go:86）装配 allow 替身（经 `newTestSvc` 单点）后原样通过（AC-6） |

---

## 5. 迁移步骤

### 5.1 实现次序（每步独立可编译；提交切分见末）

1. **`internal/access/types.go`**：常量块加 `ActionAdminDelete Action = "vault.file.delete"`；`ValidAction` case 列表追加。**锚点：** `TestActionAdminDeleteMapping`（ValidAction 断言）。
2. **`internal/access/authorizer.go`**：`actionMatches` 追加超集分支（D2）。**锚点：** `TestActionAdminDeleteMapping`（Manager 映射：`{ActionDelete,allow}` ⊨ admin、deny 区分）。
3. **`internal/service/access.go`**：`authorize()` nil 分支改造（D3，含系统豁免）。**无独立观测点**（step 4 前无人调用 `ActionAdminDelete`）——以 build + step 4 测试代验。
4. **`internal/service/file_delete.go`**：`deleteAction` + `AuthorizeDelete`；`Delete` 授权行改 `deleteAction(hard)`；`DeleteVersion` 授权行换 `ActionAdminDelete`；**`internal/service/file_bucket_settings.go`**：`DeleteBucket` per-object 授权行（:48）改 `deleteAction(true)`（D8）。**flip 点**：从本步起所有既有硬删用例同时翻红（新增翻红面：`TestDeleteBucketRejectsProtectedObject`、`TestDeleteBucketRemovesBlobsAndUsage`——经 §5.2 的 `newTestSvc`/`newQuotaTestSvc` 替身回绿）。
5. **`internal/api/webdav/dav.go`**：`Handler()` DELETE 预检（D5）；`Rename` 源预检（D6）。
6. **新测试**：`internal/service/authz_gate_test.go`（AC-1/2/3 + F5 + G0/G10 + **G15/G16**）、`internal/api/webdav/authz_gate_test.go`（AC-4/5 + G1/G2/G3/G4/G8/G9 + 替身）、`internal/access` 补 `ActionAdminDelete` 映射用例（AC-2b/D1/D2）、`internal/api/rest/authz_gate_test.go`（G13 + **G17**）+ `acl_test.go` 新字面量用例（G12）。
7. **回归装配**（§5.2，**含 N4/N5/N6 修正**）+ 全量验证：`gofmt -l`、`go build ./...`、`go vet ./...`、`go test ./internal/access/ ./internal/service/ ./internal/api/webdav/ ./internal/api/s3compat/ ./internal/api/rest/ ./internal/integration/`、`make check`。

> **原子性（硬门禁约束）：** "每步独立可提交"对 1–3 成立（纯加法、无行为翻转）；**step 4 是真正的 flip 点**（step 3 的 nil 分支此时才有调用方），step 4–7 必须**同一提交**落地（flip + 预检 + 新测试 + 全部 allow 替身布线，绿态由构造保证）。推荐切分：**Commit A = {1,2}**（含 access 映射测试）；**Commit B = {3,4,5,6,7}**。s3compat 在途方向与 `authz_parity_test.go`（untracked）先于或同于 Commit A 提交——其 :192/:241 是 Commit B 的 D2 依赖哨兵。

### 5.2 测试装配迁移（AC-6；均"helper 加一行 allow 替身"，测试文件零改写）

> 替身：各包 `_test.go` 内定义 `allowAllAuthorizer`（恒 `Decision{Allowed:true}`）——Go 测试不可跨包复用；`recordingAuthorizer`/`denyAuthorizer`/`errAuthorizer` 按 AC 用例就地定义。principal 注入用 `access.WithPrincipal`（context.go:7-10，auth 中间件同款）；HTTP 侧用包装中间件（`mw.Tenant` 先例，dav_test.go:70-72）。

| 包 | 装配点 | 迁移 |
|----|--------|------|
| `internal/service` | `service_test.go:16` `newTestSvc` → 覆盖 file_delete/object_version_delete/delete_marker 全部删除用例 + **`TestDeleteBucketRejectsProtectedObject`（object_protection_test.go:86，allow 替身下 `ErrLocked` 不变，F11）** | `WithAuthorizer(allowAllAuthorizer{})` |
| `internal/service` | **🔴 N6b：`quota_test.go:15` `newQuotaTestSvc`——独立 `NewFileService(store, repo, nil)`（设计稿原表误归 `newTestSvc` 覆盖；实读 :121/:147 均用此 helper；再审计补第三用例）** | **同加一行 allow 替身**，覆盖 `TestHardDeleteRemovesEveryVersionAndUsage`(usage_consistency_test.go:121，硬删 :136)、**`TestDeleteBucketRemovesBlobsAndUsage`(:147，F10 级联清零)**、**`TestMultipartDefaultRetentionIsPersisted`（object_retention_test.go:12，:48-50 硬删期望 `ErrLocked`——授权先于锁检查，flip 后变 `ErrForbidden`）**。注：multipart_correctness_test.go:210 为内联自建 svc（非此 helper）且无硬删路径，不需迁移 |
| `internal/api/webdav` | `dav_test.go:43-73` `newTestServerWithSvc`（`newTestServer` :29、`newTestServerSvc` :36 均委托之）→ 覆盖 :139/:282/:315/:355/:415/:455/:673 | helper 内加 `WithAuthorizer(allowAllAuthorizer{})` 一行即覆盖全部三个构造器 |
| `internal/api/webdav` | **🔴 N4：`dav_test.go:840-861` `newRollbackServer`（:856 独立构造 nil-authorizer svc）——原表遗漏；`TestMoveRollbackOnDeleteFailure` :863 经它装配** | **同加一行 allow 替身**；否则 MOVE 预检（D6）403，storage-失败回滚路径永不可达 |
| `internal/api/s3compat`（在途实现） | handler_test.go:33（svc 侧 nil authorizer；adapter 侧已 `allowAllProvider{}`）、versioning_test.go:192、sigv4_test.go:38、policy_test.go:223、managed_sse_test.go:32、authz_gate_test.go:82/217/341/437 | svc 侧 allow 替身（adapter 门禁与 service 门禁合取，FR-3b；其 allow 用例的硬删/DeleteVersion 依赖 service 放行）。handler_test.go:33 替身同时覆盖 **`TestDeleteBucketRequiresEmptyBucket`(:253)——空桶 409/204/404 序列不变（per-object 循环零迭代，D8 的 S3 面 🔒 哨兵）**，但其对象删除步骤同样依赖该替身 |
| `internal/api/rest` | acl_test.go:32、admin_ops_test.go:35、buckets_test.go:36/:71 | **实读：三者均无对象 DELETE 请求（tags_test.go:74 为 `/tags` 子资源）——原表三行为 no-op，可删**；🔴 **N5 遗漏两处**：`handlers_test.go:40` `setupTest`（`TestIdempotency_HardDeleteWithFreshKey` :89——:96 为 `?hard=1` 请求行，期望 204）与 `management_test.go:34` `newManagementRESTTest`（`TestLockObject` :132 期望 **409**，授权先于锁检查 → flip 后 403）均须加 allow 替身 |
| `internal/api/rest` | 🔴 **N6c：桶删除端点现无任何 REST e2e**（`newBucketTestServer` buckets_test.go:36 未注册 `h.DeleteBucket` 路由） | 新测试 G17：注册 `r.Delete("/v1/buckets/{bucket}", h.DeleteBucket)`（或独立 harness）+ nil-provider **403**/allow 替身 **204** 对照 + 对象/桶存活断言（`TestRestDeleteBucketFailClosedWithoutProvider`） |
| `internal/cli` | `bucket-rm`（cli_admin.go:437-456）无任何测试（cli_admin_buckets_test.go 仅覆盖 `adminBucketWebsite`） | **零迁移**（CLI 走 REST `DELETE /v1/buckets/{bucket}`，由 G17 覆盖）；可选补 CLI 级用例：403 → 退出码 1 + stderr 含状态码 |
| `internal/integration` | **`fullserver_test.go:75`（N1——规格迁移表遗漏）**：**6 处**硬删调用点（:820/:835/:853——`TestComposition_AuditSinkL2BoundTenant`；:924——`TestComposition_DeleteDeliversBothFacts`；:1065——`TestComposition_MidClaimRestartRedeliversOnce`；:731——`TestDeleteResponse_DoesNotBlockOnDelivery` 内裸 `?hard=1`） | harness 加 `WithAuthorizer(allowAllAuthorizer{})`（integration 包内定义） |
| `internal/integration` | `authz_parity_test.go:119`（在途，显式 Manager） | **零改动**——且其**步骤 1（:192）与步骤 3（:241）的 204 断言是 FR-2b 超集映射的回归哨兵**（N3 行号勘误；该文件 untracked，须随方向提交） |
| 显式装配者 | `service/access_integration_test.go:42`、`rest/enterprise_access_test.go:53` | 零改动（自行覆盖字段） |

### 5.3 上线检查单

- [ ] `make check` 全绿；`go test -race ./internal/service/ ./internal/api/webdav/`
- [ ] 默认部署冒烟：`DELETE /v1/buckets/{bucket}`（非空桶）→ 403、桶与对象存活；空桶 → 204（D8/F10）
- [ ] 默认部署冒烟：`ACCESS_ENABLED` 未设 → WebDAV DELETE 403、REST 软删 200、GET/PUT/PROPFIND 正常（I5 反向锁定）
- [ ] `ACCESS_ENABLED=true` + 既有 `object:delete` ACL → 硬删仍 204（D2）；`vault.file.delete=deny` → 403
- [ ] AV 启用（`JOBS_WORKERS>0` + `AV_PROVIDER`）且 Access 关闭 → 隔离路径不报错（D3）
- [ ] WebDAV 客户端（rclone/Finder）DELETE/MOVE 冒烟：403 时客户端收到明确拒绝而非 405 重试循环

---

## 6. 验收映射（测试 ↔ AC）

| AC | 规格验收 | 测试（文件/名称） | 关键断言 |
|----|---------|------------------|---------|
| AC-1 | `Delete` 硬删 + nil provider → `ErrForbidden`（fail-closed） | `internal/service/authz_gate_test.go` `TestDeleteFailClosedWithoutProvider`（**直接** `NewFileService(store, repo, nil)`，不用 `newTestSvc`——其已装配 allow 替身） | (a) `Delete(...,true)` → `errors.Is(ErrForbidden)`；(b) 对象仍可 `Stat`；(c) `DeleteVersion` → `ErrForbidden`；(d) `Delete(...,false)` → nil（软删基线，I5）；(e) **新增**：`PrincipalSystem` 上下文 `DeleteVersion` → nil（F5 豁免锁定） |
| AC-2 | 新 action 被端口使用且与 `object:delete` 区分 | `internal/service/authz_gate_test.go` `TestAdminDeleteActionHonoredByProvider` + `internal/access` `TestActionAdminDeleteMapping` | `recordingAuthorizer` 记录：硬删/`DeleteVersion` → `ActionAdminDelete`、软删 → `ActionDelete`、**`DeleteBucket`（非空桶）→ 每对象 `ActionAdminDelete` + bucket-scope `ActionDelete`（G16）**；Manager（SQLite repo，`testShareSecret32` 先例 authz_parity_test.go:26-29）：`{ActionDelete,allow}` → `Authorize(ActionAdminDelete)` Allowed（FR-2b）；`{ActionAdminDelete,deny}` → admin 拒、`ActionDelete` 允；`ValidAction(ActionAdminDelete)` true |
| AC-3 | provider-deny 在任何 mutation 之前中止且零事件 | `internal/service/authz_gate_test.go` `TestDeniedDeleteHasNoSideEffects` | `denyAuthorizer` 下硬删 → `ErrForbidden`；`store.ListStorageKeys` blob 仍在、`repo.GetObject` 行仍在、`event_outbox`（deleted@1.1/notify@1.1）各 0 行、`object_events` deleted 0 行、`audit_log` file.delete 0 行（E14 次序锁定）；对照 allow 替身下各恰 1 行 |
| AC-4 | WebDAV e2e：未认证 DELETE → 403，对象存活 | `internal/api/webdav/authz_gate_test.go` `TestWebDavUnauthenticatedDeleteIs403`（`newTestServerWithSvc` 同款**但不**装配 authorizer；`mw.Tenant` 包裹） | PUT 200 → DELETE **403**（预检为唯一 403 来源，E11）；GET 200（存活）；DELETE missing → `<500`（现状，:673 语义）；GET 200 反向锁定（门禁仅作用 DELETE） |
| AC-5 | 带 principal + allow provider → DELETE 204，MOVE 正常 | `internal/api/webdav/authz_gate_test.go` `TestWebDavAuthenticatedDeleteSucceeds`（`WithAuthorizer(allowAllAuthorizer{})` + `access.WithPrincipal` 注入包装，E9 同款） | DELETE 204；MOVE（Overwrite:T）204、旧路径 404、新路径 200（D6 预检放行） |
| AC-6 | 回归：既有删除测试在 mock provider 装配后原样通过 | §5.2 全表（**含 N4/N5/N6 修正**） | `go test ./internal/service/ ./internal/api/webdav/ ./internal/api/s3compat/ ./internal/api/rest/ ./internal/integration/` 全绿；🔒 `TestMoveMissingSource`(:415) 403、`TestDeleteMissingResource`(:673) <500、`TestMoveRollbackOnDeleteFailure`(:863) 回滚 warn-only、🔒 `TestWorkerQuarantinesOnlyInfectedVersion`（F5 哨兵）、🔒 S3 `TestDeleteBucketRequiresEmptyBucket`（空桶 409/204/404）、🔒 `TestDeleteBucketRejectsProtectedObject`（allow 替身下 `ErrLocked` 不变，F11）、🔒 `TestDeleteBucketRemovesBlobsAndUsage`（allow 替身下级联后 blob/usage 清零，F10） |

### 6.1 决策与失败模式的测试锚点（完整映射见 `task-1-testmap.md`）

| 项 | 测试锚点 | 状态 |
|----|---------|------|
| D1（action 字面量 + ValidAction） | `internal/access` `TestActionAdminDeleteMapping`（ValidAction）+ 🔴 G12 `acl_test.go` `TestACLAdminDeleteActionAccepted`（Upsert 新字面量 200 + Manager 生效，manager.go:204 校验） | ✅ + G12 |
| D2（超集映射） | `TestActionAdminDeleteMapping`（`{ActionDelete,allow}` ⊨ admin；deny 区分）+ 🔒 `authz_parity_test.go:192/:241`（S3 面哨兵，untracked 须随方向提交）+ 🔴 G4 `TestWebDavDeleteWithDeleteGrantOnlySucceeds`（WebDAV 面 parity） | ✅ + G4 |
| D3（系统豁免） | AC-1(e) + `TestQuarantineFailClosedCarveOut` + 🔒 `TestWorkerQuarantinesOnlyInfectedVersion` | ✅ |
| F1 | AC-1（a–d）+ AC-4 + 🔴 G0（nil-provider 零副作用断言）+ G13（REST `?hard=1` 403） | ✅ + G0/G13 |
| F2 | 🔴 G9 `TestWebDavDeleteProviderErrorRenders405` | G9 |
| F3 | 🔴 G1 `TestWebDavDeleteDecisionFlipRenders405`（有状态翻转替身） | G1 |
| F4 | 🔴 G2 `TestMoveRollbackDeniedLeavesOrphan`（warn + dst 残留） | G2 |
| F5 | AC-1(e) + `TestQuarantineFailClosedCarveOut` + 🔒 worker 哨兵 | ✅ |
| F6 | 🔴 G10 `tenant_status_test.go` 增用例（allow 替身 + disabled tenant → `ErrTenantDisabled`） | G10 |
| F7 | 🔴 G8 `TestWebDavLockedDeniedDeleteRenders403Not423`（deny 403 / allow 423 对照） | G8 |
| F8 | 🔒 `TestMoveMissingSource`（:415，零改动） | ✅ |
| F9 | 🔴 G11（可选）`TestWebDavDeletePrecheckRepoErrorRenders405` | G11 |
| 已知限制（可删不可读→405） | 🔴 G3 `TestWebDavDeletableButUnreadableRenders405`（意图锁定） | G3 |
| D8（DeleteBucket 级联入闸，N6） | 🔴 G15 `TestDeleteBucketFailClosedWithoutProvider`（nil provider → 403 + 桶/对象存活 + 零副作用）、G16 `TestDeleteBucketUsesAdminDeleteAction`（recording：每对象 `ActionAdminDelete`）、G17 `TestRestDeleteBucketFailClosedWithoutProvider`（REST e2e 403/204 对照）；🔒 S3 `TestDeleteBucketRequiresEmptyBucket`、🔒 `TestDeleteBucketRejectsProtectedObject`/`TestDeleteBucketRemovesBlobsAndUsage`（allow 替身下零改动） | 🔴 G15–G17 |

---

## 7. 实现（最终代码形态）

### 7.1 `internal/access/types.go`

```go
	ActionDelete    Action = "object:delete"
	ActionAdminDelete Action = "vault.file.delete"
```
```go
	case ActionList, ActionRead, ActionPreview, ActionDownload, ActionCreate,
		ActionWrite, ActionDelete, ActionAdminDelete, ActionRestore, ActionShare,
		ActionManageACL, ActionPublish, ActionExport, ActionAll:
```

### 7.2 `internal/access/authorizer.go`

```go
func actionMatches(granted, wanted Action) bool {
	if granted == ActionAll || granted == wanted {
		return true
	}
	// vault.file.delete is a superset requirement: an object:delete grant
	// satisfies it, so existing ACLs keep authorizing hard deletes; an
	// explicit deny on the admin action is the admin/forced-delete
	// distinction (FR-2b).
	if wanted == ActionAdminDelete {
		return granted == ActionDelete
	}
	if wanted == ActionPreview || wanted == ActionDownload {
		return granted == ActionRead
	}
	return false
}
```

### 7.3 `internal/service/access.go`（`authorize()` nil 分支，:91-93）

```go
	if s.authorizer == nil {
		// Fail-closed for the admin-delete action (COMPOSE-2026-017): a
		// missing provider must not silently allow hard deletes. System
		// principals are trusted exactly as the Manager's trusted_system
		// path trusts them — the antivirus quarantine worker runs as
		// PrincipalSystem and must not break in provider-less deployments.
		// All other actions keep the CI/MVP nil-allow baseline (I5).
		if action == access.ActionAdminDelete {
			if principal, ok := access.PrincipalFrom(ctx); ok && principal.Kind == access.PrincipalSystem {
				return nil
			}
			return fmt.Errorf("%w: no authorization provider configured", ErrForbidden)
		}
		return nil
	}
```

### 7.4 `internal/service/file_delete.go`

```go
// deleteAction selects the access action for a delete: hard deletes and
// permanent version deletes require vault.file.delete (ActionAdminDelete,
// fail-closed: nil provider denies), soft deletes stay on object:delete
// (CI baseline). Single source of truth for Delete and AuthorizeDelete.
func deleteAction(hard bool) access.Action {
	if hard {
		return access.ActionAdminDelete
	}
	return access.ActionDelete
}

// AuthorizeDelete reports whether a delete of key would be allowed, without
// performing any mutation (no quota preflight, no read authorization — Stat
// is read-authorized and would reject deletable-but-unreadable objects).
func (s *FileService) AuthorizeDelete(ctx context.Context, tenant, bucket, key string, hard bool) error {
	tenant, bucket, err := checkedObjectDefaults(tenant, bucket, key)
	if err != nil {
		return err
	}
	obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	return s.authorizeObject(ctx, deleteAction(hard), obj)
}
```

`Delete`：`:159` 授权行改为 `s.authorizeObject(ctx, deleteAction(hard), obj)`（其余逐字不动，E4 次序保持）。`DeleteVersion`：`:179` 改为 `s.authorizeObject(ctx, access.ActionAdminDelete, obj)`。

### 7.5 `internal/api/webdav/dav.go`

`Handler()` 包装器（GET/HEAD 分支之后、`dav.ServeHTTP` 之前）：

```go
		// x/net/webdav's handleDelete maps every RemoveAll error to 405, so a
		// fail-closed deny would render as 405, not 403. Pre-adjudicate
		// DELETE here so the deny renders 403 before dav.ServeHTTP is
		// reached; allow passes through and the service re-adjudicates
		// inside RemoveAll (defense in depth). Missing objects and other
		// errors fall through unchanged (webdav renders 404/405 as today).
		if r.Method == http.MethodDelete {
			if name, ok := strings.CutPrefix(r.URL.Path, prefix); ok {
				name = strings.TrimPrefix(name, "/")
				if name != "" && !strings.HasSuffix(name, "/") {
					err := fsys.svc.AuthorizeDelete(r.Context(), fsys.tenant(r.Context()), service.DefaultBucket, name, true)
					if errors.Is(err, service.ErrForbidden) {
						http.Error(w, "forbidden", http.StatusForbidden)
						return
					}
				}
			}
		}
		dav.ServeHTTP(w, r)
```

`Rename`（:150-153 处，source Get 之前）：

```go
	tenant := f.tenant(ctx)
	src := strings.TrimPrefix(oldName, "/")
	// Pre-adjudicate the source deletion before copying: fail-fast on deny
	// (no payload streaming, no mutation). moveFiles maps any Rename error
	// to 403; a missing source still surfaces ErrNotFound → 403, so
	// TestMoveMissingSource is unchanged. The service re-adjudicates the
	// real delete inside RemoveAll (defense in depth).
	if err := f.svc.AuthorizeDelete(ctx, tenant, service.DefaultBucket, src, true); err != nil {
		return err
	}
	rc, srcObj, err := f.svc.Get(ctx, tenant, service.DefaultBucket, src)
```

（后续 `src2 := strings.TrimPrefix(oldName, "/")` 可复用 `src`，删除原行；:198-202 回滚逻辑不动。）

### 7.6 测试（要点）

- 四个替身（各包内定义）：`allowAllAuthorizer`、`denyAuthorizer`、`recordingAuthorizer`、`errAuthorizer`；`internal/access` 的 Manager 映射用例复用 `testShareSecret32` 先例。
- AC-1(e)/F5：`access.WithPrincipal(ctx, Principal{SubjectID:"system:antivirus", TenantID:"default", Kind:PrincipalSystem})` + nil provider 下 `DeleteVersion` → nil。
- AC-4/AC-5 的 HTTP 装配：`mw.Tenant(webdav.Handler(...))`（dav_test.go:70-72 同款）+（AC-5）外层 principal 注入中间件。

### 7.7 `internal/service/file_bucket_settings.go`（`DeleteBucket` per-object 授权行，:47-53）

```go
	for _, obj := range objects {
		// Object-destruction face of the cascade is gated by the same
		// admin-delete action as Delete/DeleteVersion (D8): fail-closed
		// under a nil provider, object:delete superset under Manager (D2).
		if err := s.authorizeObject(ctx, deleteAction(true), obj); err != nil {
			return err
		}
		if err := s.checkObjectProtection(ctx, obj); err != nil {
			return err
		}
	}
```

（bucket-scope 行 :40 `authorizeBucket(ctx, access.ActionDelete, …)` 与 `deleteBucketData` :58 / `repo.DeleteBucket` :61 / usage :65 次序逐字不动；授权全量预检先于 `deleteBucketData` → deny 零副作用，F10。）

---

## 8. 范围守卫（与规格 §5 一致，不扩展）

| 不做 | 理由 |
|------|------|
| 软删（`ActionDelete`）fail-closed | 方向权限是 `vault.file.delete`；软删保持 CI 基线（I5）；MCP/CLI/REST 默认软删不受影响 |
| 改 auth 中间件/中间件链（I4） | 401/403 分层保持（FR-8） |
| `DeleteBucket` 级联的 **bucket 管理面**（bucket-scope `ActionDelete` :40、空桶删除） | 非对象删除；bucket 记录管理语义不变（D8） |
| **`DeleteBucket` 的对象销毁面（per-object 循环 :48）** | **纳入门禁**：`deleteAction(true)`（D8，N6/Finding A 收口）——`vault.file.delete=deny` 阻断整桶歼灭；`object:delete` allow 经 D2 超集零回归 |
| bucket 子资源（CORS/encryption/website/logging/notification/policy/versioning/lifecycle…）与 bucket policy | 非对象删除，范围外 |
| `CreateDeleteMarker` 换 action | 可逆标记；s3compat 规格亦映射 `object:delete` |
| 新增 `AuthorizationProvider` 接口类型 | 端口已存在（`access.Authorizer`）；Manager 与 sibling PDP 实现同一接口（FR-1） |
| S3 adapter 门禁 action 对齐（**Finding B，明示接受**） | 在途 s3compat adapter 以 `ActionDelete` 询问 provider（authz.go:32）；D2 落地后 `vault.file.delete`-only 授予在 S3 面过度拒绝（over-denial，安全侧非绕过）。接受不改；随 Commit B 可将 adapter 换 `ActionAdminDelete`（既有 `object:delete` ACL 经 D2 超集映射不失效） |
| Manager 对 `ActionAdminDelete` 严格模式 | 令既有 `object:delete` ACL 硬删全失效，违背回归验收（D2） |
| 修复 x/net/webdav 405 硬编码（fork/patch） | 预检渲染 403 是唯一改动点（D5）；修复引入依赖 fork |
| MOVE Overwrite=T 目标预删的 adapter 预检 | `moveFiles` 内部 `RemoveAll(dst)` 已由 service 权威判定 + 403 渲染（E12） |
| 预检扩展至 GET/PUT/PROPFIND/OPTIONS/COPY | 门禁仅作用删除（AC-4 反向锁定） |
| `AuthorizeDelete` 被 REST/S3/MCP/CLI 复用 | 其错误映射无 405 问题，经 `svc.Delete` 自身判定即可 |
| 修复"可删不可读"→ 405 限制 | 现状行为（E11/E13），非本方向引入；修复需 fork x/net/webdav |
| @1.1 事件 schema/outbox/audit 改造 | 已存在（E14）；本方向只做"被拒零行"断言 |

**proposed_vs_verified 对照（规格 §5 决策逐条落地）：** verified——fail-open 端口（E1/E10）、单 action（E3）、单 Manager 实现（E2）、405 硬编码（E11）、MOVE 403 映射（E12）、Stat 读授权（E13）、回滚双删（E7/E14）；proposed——`ActionAdminDelete = "vault.file.delete"`（D1）、复用 `access.Authorizer`（FR-1）、超集映射（D2）、DELETE/MOVE 预检（D5/D6）；**本设计新增**——系统 principal 豁免（D3/R1，N2）、fullserver_test 迁移（R3，N1）、MOVE 预检前置（R2）、`DeleteBucket` 级联纳入门禁（D8/R4，N6）。
