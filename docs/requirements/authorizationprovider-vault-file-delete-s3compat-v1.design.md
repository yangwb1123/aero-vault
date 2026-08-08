# 设计：vault.file.delete 的 fail-closed 强制 —— s3compat 边界 AuthorizationProvider 端口（配套设计文档 v1）

> **配套规格：** `docs/requirements/authorizationprovider-vault-file-delete-s3compat-v1.md` · **模块：** `internal/api/s3compat`（+ `cmd/server/http.go` 一行装配） · **状态：** 设计已定稿，未实现 · **日期：** 2026-08-06
> 本文是规格的落地设计：证据复核结论（含 HEAD 实跑探针）、API 变更、设计决策（含门禁触发面逐路径证明）、兼容性约束、失败模式、迁移步骤、验收映射、最终代码形态。

---

## 0. 证据复核结论（对规格逐条核验）

规格全部 16 条代码引用（E1–E16）与仓库 HEAD `acfaaf4` 逐行一致，**无需修正**。本设计阶段新增独立核验 V1–V4（探针实跑）与 R1–R2（规格精确化，不改变语义）。

### 0.1 逐条复核（对照工作树行号）

| # | 规格引用 | 复核 | 结论 |
|---|---------|------|------|
| E1 | policy.go:43 `authorizeS3Request` 不咨询 `access.Authorizer` | 实际 :43-80：expected-bucket-owner → `validatePolicyWrite` → action 计算 → `checkBucketPolicy` → copy-source；无 provider 概念 | ✅ |
| E2 | policy.go:107-109 allow-on-empty | 实际 :108-109 `if cfg.Policy == "" { return true }` | ✅（:107-109→:108-109，行差 1，无实质） |
| E3 | policy.go:39 批量 `?delete` bucket 级规则 | 实际 :39 `{query: "delete", putAction: "s3:DeleteObject", deleteAction: "s3:DeleteObject"}` | ✅ |
| E4 | extra.go:430 deleteObjects 循环无 per-key 授权 | 实际 :432-456，`deleteS3Object` 调用在 :437，仅 bucket 级门禁后直接执行 | ✅ |
| E5 | errors.go:118 ErrForbidden→AccessDenied；:61→403 | 实际 :118 映射；:61 `"AccessDenied": 403`；:84 message `"Access denied."` | ✅ |
| E6 | access.go:91-93 nil authorizer 提前放行；file.go:88-90 注释 | 实际 access.go:91-93 `if s.authorizer == nil { return nil }`；file.go:95-97 "A nil authorizer preserves the CI/MVP baseline" | ✅ |
| E7 | Authorize error 不包 ErrForbidden → 500 | 实际 access.go:97-100 `fmt.Errorf("authorization decision: %w", err)`；**探针 V3 实跑确认** `s3ErrorCode` 落 `InternalError` | ✅ |
| E8 | delete_marker.go:32 ActionDelete | 实际 :34（现版本 `authorizeObject(ActionDelete)`）/ :37（`authorizePath(ActionDelete)`） | ✅ |
| E9 | access/authorizer.go:10 接口；:25-27 missing_principal；:131 vault.file_admin | 实际 :10-12 接口；:24-26 `SubjectID=="" → denied("missing_principal")`；:129-137 `isAdministrator` 含 `vault.file_admin` | ✅ |
| E10 | types.go:76 `ActionDelete="object:delete"` | 实际 :76；仓库无 `"vault.file.delete"` 字面量 | ✅ |
| E11 | access.go(server):12-14 nil manager；main.go:215 WithAuthorizer | 实际 `cmd/server/access.go:12-14`（`!cfg.Access.Enabled → nil,nil`）；main.go:215 `.WithAuthorizer(accessManager)` | ✅ |
| E12 | http.go:120 NewRouter 唯一装配点 | 实际 :120 `r.Mount(cfg.S3Compat.Prefix, s3compat.NewRouter(svc, logger))`；`accessManager` 在同函数 :83 签名内可见 | ✅ |
| E13 | event_outbox.go:22 @1.1；:76-83 schema 校验 | 实际 :22 `EventTypeFileDeleted11`；:76-83 `validOutboxPayload` 要求 `schema_version=="1.1"` | ✅ |
| E14 | file_delete.go:131-134 事务内写 outbox | 实际 :111-127 `deleteFacts`（deleted@1.1 + notify@1.1）；:46/:86 经 `HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent`；repo event_outbox.go:232 `INSERT INTO event_outbox`（事务内） | ✅ |
| E15 | rest/handler.go:245 REST admin delete | 实际 :245 `h.svc.Delete(...)`；rest `classify`（handler_helpers.go:47-49）`ErrForbidden → 403 AccessDenied` | ✅ |
| E16 | 测试基建 `NewRouter(svc, nil)` | 实际 5 处单测：handler_test.go:34、versioning_test.go:205、policy_test.go:224、managed_sse_test.go:32、sigv4_test.go:49；**+1 处集成**：fullserver_test.go:115（规格未列，本设计补入迁移面，见 §5.2） | ✅ |

### 0.2 探针实跑（V1–V4，HEAD 上临时测试，已删除）

按规格 §4 编译四个探针，证明缺陷类在 HEAD 真实存在、修复后是唯一翻转点：

| 探针 | HEAD 实测 | 修复后预期 |
|------|-----------|-----------|
| **V1（=AC-1 前身）** 无 policy、无 provider、无 principal 上下文：`DELETE /b/k.txt` | **204**（对象被删） | 403 `AccessDenied`（provider 未设置=默认拒绝） |
| **V2（=AC-3 前身）** `POST /b?delete`（2 keys，无 policy/provider） | **200**，两 key 均入 `Deleted` 列表 | 200 外壳 + 每 key `Errors:[AccessDenied]`，key 未被删 |
| **V3（=AC-2 前身）** service 侧 provider-error 形状（access.go:97-100 包装后 error） | `s3ErrorCode(...)` = **`InternalError`**（500） | 403 `AccessDenied`（adapter 门禁先于 service，错误不再到达 s3ErrorCode） |
| **V4（=AC-4/AC-5 对照）** 放行删除后查 `HasEventOutboxFact(originID, EventTypeFileDeleted11)` | **true**（outbox 事实存在） | 不变（放行路径仍写 @1.1）；被拒路径必须 false |

### 0.3 规格精确化（R1–R2，不改变规格语义）

- **R1（FR-2 约束 a 的"先于任何 service 调用"精确化）**：`checkBucketPolicy` 自身的 `GetBucketConfig`（只读，policy.go:101）**必然先于** provider 门禁执行——这是 IAM 门禁的既有前置，不可消除。规格不变量"被拒请求零副作用"的实质是：门禁先于**删除路径的任何 service 调用**（`deleteS3Object` 内的 `GetObjectRetention`/`GetBucketConfig`/`Delete`/`DeleteVersion`/`CreateDeleteMarker`）与任何写路径。AC-4/AC-5 断言的是持久副作用（outbox/audit/events/blob），只读 policy 查询不影响该不变量。实现按此精确放置（§7.2）。
- **R2（provider error 的 message 归一）**：规格未断言 XML message 文本。设计决定：单删走 `writeS3Error(w, r, service.ErrForbidden)`（message 自动取 `s3CodeMessage["AccessDenied"]` = `"Access denied."`，errors.go:84）；批删 per-key 用同一 `"Access denied."` 常量。**不把 provider 返回的 error 文本或 decision.Reason 泄漏给客户端**（防内部信息泄漏，AWS 同款姿态）；provider error 记 `Warn` 日志（含 tenant/bucket/key/err），deny 记 `Debug` 日志（含 reason）。

---

## 1. API 变更

**零配置变更、零 schema 变更、零 `go.mod` 变更（I6）、`errors.go`/`service`/`access`/`repository` 零改动。** 唯一签名变更在两个构造器，且编译期强制所有调用点显式表态（§3-4）。

| 层 | 旧 | 新 |
|----|----|----|
| **新端口类型** `s3compat.AuthorizationProvider` | — | `Authorize(ctx, access.Principal, access.Action, access.Resource) (access.Decision, error)` —— 与 `access.Authorizer`（authorizer.go:10-12）**同构**；`*access.Manager.Authorize`（authorizer.go:15-17）结构化满足，**零包装器** |
| `NewHandler(svc *service.FileService, logger *slog.Logger)` | 2 参 | `NewHandler(svc, logger, authz AuthorizationProvider)`（handler.go:25） |
| `NewRouter(svc *service.FileService, logger *slog.Logger)` | 2 参 | `NewRouter(svc, logger, authz AuthorizationProvider)`（router.go:14） |
| `Handler` struct（handler.go:20-22） | `{svc, logger}` | + `authz AuthorizationProvider` |
| `cmd/server/http.go:120` | `NewRouter(svc, logger)` | `NewRouter(svc, logger, accessManager)`（nil 安全：nil = 未设置 = 默认拒绝） |
| **行为：S3 对象删除**（单删 / `?versionId` / delete-marker / 批量 `?delete`） | 无 provider 门禁；provider error → 500 `InternalError`（V3） | provider 未设置、拒绝、错误**一律** → 403 `AccessDenied`；批删逐 key `AccessDenied`（200 XML 外壳不变）；被拒请求零 outbox/audit/event/blob 副作用 |
| **行为：非删除操作**（GET/PUT/HEAD/list/multipart/`?tagging`/`?uploadId`/bucket 级） | — | **逐字节不变**（D2 触发面证明） |
| `errors.go` 映射 | — | 零改动（`ErrForbidden → AccessDenied → 403` 已存在，E5） |

---

## 2. 设计决策

### D1 — 端口放 **adapter 边界**，构造器显式参数（非 variadic）

- **位置**：adapter 边界（门禁先于任何 service 调用），而非 service 边界。理由即规格头部记载：service 边界端口被 `s.authorizer == nil` 提前返回（access.go:91-93）击败，旧 campaign（`docs/auto/runs/authorizationprovider-port-enforcing-vault-file--59697301`）在 design_gate 被拒。adapter 门禁与 service 装配解耦：即使 `ACCESS_CONTROL_ENABLED=false`（`buildAccessManager` 返回 nil，access.go(server):12-14）且 `WithAuthorizer(nil)`（main.go:215），S3 删除仍 fail-closed。
- **显式参数而非 variadic/option**：`NewRouter(svc, logger, authz)`。变参或 `With*` 风格会让 6 个既有调用点静默编译通过、运行时才暴露默认拒绝——正是旧 campaign 的失败模式（静默绕过）。显式参数把"此调用点的 provider 是什么"变成编译期义务：生产装配点传 `accessManager`，测试按用例显式传 stub 或 nil。**nil 的语义固定为"未设置=默认拒绝"**（AC-7），在构造器注释与 `authorizeDelete` 中双重声明。
- **同构复用**：端口形状与 `access.Authorizer` 逐字段一致（已核验 `*access.Manager.Authorize` 签名精确匹配，authorizer.go:15-17），`accessManager` 直接作为 provider 传入——**S3 与 REST 共享同一决策源与 store**（FR-6），无包装器、无第二次实现、无漂移可能。

### D2 — 门禁触发面（逐路径证明）

**单删门禁**位于 `authorizeS3Request` 内 `checkBucketPolicy` 通过之后、return true 之前，条件为 **`key != "" && action == "s3:DeleteObject"`**（action 取自 `objectPolicyAction`，policy.go:172-183）。逐路径判定：

| 请求 | `keyFromURL` | `objectPolicyAction`/`bucketPolicyAction` | 门禁 |
|------|-------------|------------------------------------------|------|
| `DELETE /{b}/{k}` | `k` | `s3:DeleteObject` | **触发** → `svc.Delete`（硬删） |
| `DELETE /{b}/{k}?versionId=v` | `k` | `s3:DeleteObject`（versionId 不在查询判定表） | **触发** → `svc.DeleteVersion` |
| `DELETE /{b}/{k}`（版本化桶） | `k` | `s3:DeleteObject` | **触发** → `svc.CreateDeleteMarker` |
| `DELETE /{b}/{k}?tagging` | `k` | `s3:DeleteObjectTagging` | 不触发（范围外） |
| `DELETE /{b}/{k}?uploadId=u` | `k` | `s3:AbortMultipartUpload` | 不触发（范围外） |
| `DELETE /{b}`（bucket 删除） | `""` | `s3:DeleteBucket` | 不触发（key==""） |
| `PUT/POST /{b}?delete`（批删） | `""` | `s3:DeleteObject`（:39 规则） | 不在 adapter 级触发（key==""）；**per-key 门禁在 `deleteObjects` 循环内**（D4） |
| `GET/HEAD/PUT/POST /{b}/{k}` 及全部子资源 | — | 非 `s3:DeleteObject` | 不触发 |

> 边界：`DELETE /{b}/`（尾斜杠）——chi 双路由均可匹配（`/{bucket}/` 与 `/{bucket}/*`），两条路径下 key 均为 `""` → 门禁不触发，行为与现状一致（service 层空 key 校验照旧，I3）。`POST /{b}/*`（multipart complete）action= `s3:PutObject`（objectPolicyAction default 分支）→ 不触发。

**批删 per-key 门禁**：`deleteObjects`（extra.go:432-456）循环内、`deleteS3Object`（:437）**之前**，对每个 `o.Key` 咨询 provider。

### D3 — deny 与 error 单一呈现（403 单一化）

- 单删：`authorizeDelete` 返回 false（无论 nil provider / deny / error）→ `writeS3Error(w, r, service.ErrForbidden)` → `AccessDenied` / `"Access denied."` / **403**，直接 return，不进入 `DeleteObject` 主体（handler.go:264-288 的 `?tagging`/`?uploadId` 分支与 `deleteS3Object` 均不执行）。
- 批删：循环内 false → `out.Errors = append(out.Errors, deleteErrItem{Key: o.Key, VersionID: o.VersionID, Code: "AccessDenied", Message: "Access denied."})`（xml.go:182-187 既有结构），`continue` 处理下一 key；200 XML 外壳不变（AWS 兼容）。
- **禁止**把 provider error 传播为 500：这是 E7/V3 缺陷的修正点，且修正发生在 adapter（error 永不进入 `s3ErrorCode`），`errors.go` 零改动。

### D4 — 批删语义：per-key 拒绝 = 跳过 + 报错，AND 合取

- 被拒 key：**不调用 `deleteS3Object`**（绝不被删除），XML `Errors` 报 `AccessDenied`；允许的 key 正常删除（与现有 per-key 错误循环语义一致，extra.go:443-455 的 `ErrNotFound` 先例）。
- 双层合取：既有 bucket 级 `s3:DeleteObject` policy 检查（E3）为第一层，per-key provider 门禁为第二层，**AND-composition**。provider 对某 key error 时按该 key deny（fail-closed per-key），不中断整批、不影响其他 key。
- provider 门禁与 service 门禁（`authorizeObject`）是**第三个** AND 层：provider 放行但 service 拒绝（两门禁间 ACL 变更等竞态）→ service 返回 `ErrForbidden` → 既有路径 403。任何一层 deny 都不得被其余层覆盖。

### D5 — 零副作用不变量（结构性成立）

门禁位置（`authorizeS3Request` 内 + `deleteObjects` 循环内，均先于 `deleteS3Object`）保证被拒请求**不进入删除事务**。outbox 行（deleted@1.1 / notify@1.1）、`object_events`（EventDeleted 广播）、`audit_log`（file.delete）与 storage blob 变更全部发生在 service 删除事务内（E14，file_delete.go:46/:86 + repo event_outbox.go:232 同事务）→ 被拒请求零持久化副作用。AC-4/AC-5 以 SQL/API 断言锁定该不变量，防止未来把门禁移到 service 调用之后（回归保护）。

### D6 — 装配与 parity

- 生产装配唯一改点：http.go:120 传 `accessManager`（buildRouter :83 参数已在作用域内）。`ACCESS_CONTROL_ENABLED=false` → nil → S3 删除默认拒绝（**刻意反转** service 侧 "nil authorizer preserves CI/MVP baseline"）；`ACCESS_CONTROL_ENABLED=true` → 同一 `*access.Manager` 既作 service `WithAuthorizer`（main.go:215）又作 S3 provider。
- `Manager.Authorize` 每请求实时读 store（authorizer.go:32 `ListApplicableACL`，无会话级缓存）→ mid-session 撤销即时生效：S3 与 REST（rest/handler.go:245 → `svc.Delete` → service 门禁 → `ErrForbidden` → classify 403）**同时**翻转（AC-6）。
- service 侧 nil-authorizer 基线（access.go:91-93）**保持不动**：REST/CLI/MCP 在 access 未启用时的行为不变；S3 的 fail-closed 由 adapter 门禁独立保证。

### D7 — 默认拒绝不扩散到非删除操作

门禁条件是 `action == "s3:DeleteObject"`（D2 表），GET/PUT/HEAD/list 等一律不经过 `authorizeDelete`。AC-7 以回归断言锁定"仅删除路径受门禁"（I5 基线保护）。批删的 bucket 级 `POST/PUT ?delete` 请求本身（XML 解析、200 外壳）不因默认拒绝而改变形状。

---

## 3. 兼容性约束

1. **生产默认行为翻转（本方向最重要的兼容性影响，刻意为之）**：`ACCESS_CONTROL_ENABLED=false`（默认配置：config.go:216 `Enabled` 默认 false；`ACCESS_DEFAULT_POLICY` 默认 `deny`，config.go:217）的部署中，**所有 S3 对象删除从"放行"翻转为 403**。这是 AC-7 明文要求（fail-closed 默认）；非删除操作不受影响。依赖 S3 删除的存量部署必须执行 §5.3 运维迁移。
2. **所有非删除操作逐字节不变**：GET/PUT/HEAD/list/multipart/`?tagging`/`?uploadId`/bucket 级删除/全部 bucket 子资源——D2 表逐路径证明，AC-7 回归锁定。
3. **服务层不变**：`internal/service`、`internal/access`、`internal/events`、`internal/repository`、`internal/api/rest` 零改动；REST/CLI/MCP/WebDAV 行为与今日一致（含 access 未启用时 REST 删除照常放行的基线）。
4. **IAM bucket-policy 门禁语义不变**：`checkBucketPolicy` 的 allow-on-empty（policy.go:108-109）保持；provider 门禁是独立第二层 AND 门禁——空 policy 不再等于"删除放行"（规格 §5 范围边界明示）。
5. **错误面不变**：无新 S3 错误码、无 HTTP 状态新增（403 已存在）；批删 XML 外壳与结构不变（`Deleted`/`Error` 元素、200 状态）；`errors.go`/rest `classify` 零改动。
6. **测试基建迁移面（编译期强制）**：6 个 `NewRouter` 调用点必须显式传参——含对象删除断言的用例注入 allow-all stub（§5.2 清单），否则编译失败（fail-loud，优于运行时静默）。
7. **工程约束**：新增代码限于 `internal/api/s3compat`（`authz.go` 新文件 ≈55 行 + 两处门禁插入）+ `cmd/server/http.go` 一行；单文件 < 500 行硬门禁余量充足；纯 stdlib（I6）；无 SQL/schema（I2）；不触碰 key 校验（I3）与中间件链（I4）。

---

## 4. 失败模式

| 场景 | 行为 | 缓解 |
|------|------|------|
| provider 未设置（生产默认 `ACCESS_CONTROL_ENABLED=false`） | **全部 S3 对象删除 403**（fail-closed，AC-7） | §5.3 运维迁移（启用 access 模块或接受 403）；403 有明确错误码可观测 |
| provider store 故障（`ListApplicableACL` 错误，authorizer.go:32） | `Manager.Authorize` 返回 error → **403**（fail-closed；修正今日 500，V3） | Warn 日志（tenant/bucket/key/err，R2）；数据安全方向（拒绝而非误删）；既有 /healthz/readyz 覆盖 store 健康 |
| access 模块启用但 principal 无 ACL 且 `DefaultPolicy=deny` | 403 `default_deny`（authorizer.go:69-70） | 授权流程：`PutACL` 授予 `ActionDelete` 或 `vault.file_admin` 角色（E9）；Debug 日志含 reason |
| mid-session 撤销（ACL 删除 / 显式 deny） | 下一请求即 403（无缓存，authorizer.go 每请求读 store） | 预期行为；S3 与 REST 同步翻转（AC-6） |
| 批删部分拒绝 | 被拒 key 保持存在，XML `Errors:[AccessDenied]`，其余 key 正常删（200 外壳） | AWS 兼容语义；客户端必须检查 `Errors` 而非只看 200（文档注明） |
| 双门禁竞态（provider 放行、service 拒绝——两门禁间 ACL 变更） | service 返回 `ErrForbidden` → 403（既有路径） | AND 合取（D4）；任何一层 deny 不被其他层覆盖 |
| 双门禁竞态·store-error（L2 adapter 门禁放行后、L3 service 门禁遇 ACL store 故障——同一请求内两次 `Authorize` 之间 store 不可用） | service 侧 `Authorize` error 未包 `ErrForbidden`（access.go:97-100）→ **500**；删除不发生、零持久副作用（fail-safe，但打破“403 非 500”的绝对表述——该表述的精确范围是**门禁自身**的拒绝/错误，L3 service 门禁的 store 故障不在其内） | 非 S3 既有路径同态（今日 REST 亦 500），本方向不新增暴露面；窗口极窄（同请求内两查）；store 健康由 /healthz/readyz 覆盖；provider error 文本仍不泄漏（R2） |
| 门禁误触发于非删除路径（实现回归） | 读路径被 403 破坏 | D2 精确表 + AC-7 回归断言（GET/PUT/HEAD 全绿）+ 代码评审对照 D2 表 |
| provider panic | Go panic → `Recoverer` 中间件兜底 500（中间件链既有行为，I4） | 不在本方向范围；provider 是既有 `access.Manager`，无新 panic 面 |
| 回滚（env + 二进制**一起**，§5.4） | 门禁消失：S3 删除恢复放行（fail-open 回归） | 仅应急；正向部署优先；**仅回滚二进制不恢复放行**——option (a) 部署下旧二进制 service 门禁继续 403（§5.4），必须 `ACCESS_CONTROL_ENABLED=false` 一并回滚 |

---

## 5. 迁移步骤

### 5.1 代码迁移（单一提交）

1. 新增 `internal/api/s3compat/authz.go`：`AuthorizationProvider` 接口 + `Handler.authz` 字段 + `authorizeDelete` helper + 测试用 allow-all stub（§7.1）。
2. `policy.go` `authorizeS3Request` 末尾插入单删门禁（§7.2）；`extra.go` `deleteObjects` 循环插入 per-key 门禁（§7.3）。
3. `handler.go:25` / `router.go:14` 构造器签名加 `authz AuthorizationProvider` 参数并透传。
4. `cmd/server/http.go:120` 传 `accessManager`。
5. `make check` 全绿 + §6 新测试全绿。

### 5.2 测试迁移（编译期强制，6 个 `NewRouter` 调用点）

| 调用点 | 含对象删除断言？ | 迁移动作 |
|--------|----------------|---------|
| `handler_test.go:34`（`newTestServer` 共享 harness） | 是（`TestBatchDelete` :229-251；`TestDeleteBucketRequiresEmptyBucket` :261-270 的 :265 对象删除） | **harness 统一注入 allow-all stub**（单点迁移，覆盖多数用例）；默认拒绝行为由新测试 `authz_gate_test.go` 单独构造（不传 provider 的 server） |
| `versioning_test.go:205` | 是（:242 `?versionId` 删版本、:257 delete-marker、:280 删 marker） | 传 allow-all stub；断言不变（204 / `x-amz-delete-marker` / 版本保留语义） |
| `fullserver_test.go:115`（集成，规格 E16 未列） | 是（:263-271 DELETE：请求 :265，断言 204 于 :271） | 传 allow-all stub（或复用 harness 已有 `accessManager` 若装配了 access——当前 harness 未装，用 stub 最小改动） |
| `policy_test.go:224` | 否（policy 读写用例） | 传 nil（=默认拒绝，不影响非删除断言） |
| `managed_sse_test.go:32` | 否（SSE 加解密 PUT/GET） | 传 nil |
| `sigv4_test.go:49` | 否（SigV4 验签 PUT/GET/HEAD） | 传 nil |
| `handler_test.go:122`（`?tagging` 删除）、`:218`（`?uploadId` abort）、`:727/:928-930`（bucket 子资源删除） | 否（D2：非 `s3:DeleteObject`） | 无需单独处理（随 harness 获得 allow-all，行为不变） |

### 5.3 运维迁移（生产部署）

1. **盘点**：`ACCESS_CONTROL_ENABLED` 当前取值（config.go:216）。**注意变量名**：`getEnv` 对未知变量静默忽略（config.go:299-302），写错为 `ACCESS_ENABLED` 会无日志、无报错地失效。若未启用且生产使用 S3 删除 → 部署本版本后删除将 403（§3-1）。
2. **需要 S3 删除的部署**（二选一）：
   - (a) 启用 access 模块：`ACCESS_CONTROL_ENABLED=true` + **`ACCESS_SHARE_SECRET`（≥32 字节**——缺失时 `NewManager` 硬失败（manager.go:44-45）→ 进程启动即报错退出（main.go:61/190），fail-loud 而非静默）；`ACCESS_DEFAULT_POLICY` 默认 `deny`；`tenant` 可收窄 enable 窗口（见步骤 5）；并按 ACL 模型为删除主体授予 `ActionDelete`（`object:delete`，types.go:76）或 `vault.file_admin` 角色（E9）；或
   - (b) 接受删除被拒：保持默认，把 S3 删除流量迁移到 **REST/CLI（服务层行为不变，§3-3）或 WebDAV**——WebDAV DELETE 直连 `svc.Delete`（dav.go:143，不经 S3 门禁），MOVE 亦走 service（dav.go:198 copy-then-delete），是现成的替代删除面（`WEBDAV_PREFIX` opt-in，默认关）；或改造调用方。
3. **文档同步（随本版本合入，勿滞后到部署后）**：`docs/api.md:625`（DeleteObject 行）与 `:630`（DeleteObjects 行，现仅写 "supports quiet mode" 未警告 200 可携带 per-key `Errors`）补一句：**200 外壳 ≠ 全部成功，客户端必须检查 `Errors` 元素**（批删 per-key 拒绝 = 200 + `Errors:[AccessDenied]`，extra.go:430-457）；`README.md:155`（S3 协议行 "batch delete"）同步。仅看状态码的 curl/脚本是批删静默失败的唯一暴露面（AWS SDK/boto3 逐 key 报错）。
4. **不需要 S3 删除的部署**：无操作（默认拒绝即预期安全姿态）。
5. **部署顺序（option (a)）——先后次序即安全与否**：
   - **第一步（在旧二进制上完成）**：设 `ACCESS_CONTROL_ENABLED=true`（可先 `ACCESS_DEFAULT_POLICY=tenant` 收窄）+ 为删除主体预授权（`PutACL` 授 `ActionDelete` / `vault.file_admin`）。旧二进制的 service 门禁（`WithAuthorizer` 非 nil，main.go:215）即刻接管：S3 删除经 service 侧 fail-closed，与新二进制的 adapter 门禁共用同一 `*access.Manager` 每请求实时决策——切换无决策断点。
   - **enable 窗口 = 全量 fail-closed（必须预警）**：授权路由（`PUT /v1/access/acl` 等）仅在 `h.access != nil` 时注册（rest/router.go:301），**无法先授权后启用**——翻 env 到授完 ACL 之间，非 admin principal 的 REST/CLI/WebDAV/S3 写、读、删**全部 403**（service 门禁覆盖三操作：access.go:162/170 写、file_get.go:51 读；WebDAV 呈现 405，x/net/webdav 既有行为）。fail-closed、零数据丢失；`ACCESS_DEFAULT_POLICY=tenant` 可收窄窗口（write/read-scoped principal 继续工作），窗口内排期须知悉。
   - **第二步（窗口关闭后）**：换新二进制——adapter 门禁接管同一 Manager，行为连续。
   - 代码+测试先合入（`make check` 全绿），二进制随后；**无数据迁移**（I2 不涉及）、无配置格式变更、无 API 版本化。

### 5.4 部署后验证

- §6 全部新测试 + 既有 s3compat 套件全绿（`go test ./internal/api/s3compat/ ./internal/integration/` + `make check`）。
- 抽查（手工/集成）：未启用 access 时 `DELETE /s3/b/k` → 403 `AccessDenied`；启用 + 授权后 → 204 且 `event_outbox` 出现 `vault.file.deleted@1.1` 行；撤销后 → 403。
- **回滚（必须 env + 二进制一起）**：
  - **正确回滚**：`ACCESS_CONTROL_ENABLED=false` + 旧二进制。旧二进制无 adapter 门禁；service 侧 `WithAuthorizer(nil)`（main.go:215）→ nil-authorizer 基线放行（access.go:91-93）→ S3 删除恢复旧语义（放行）；无数据损坏风险（回滚方向是放宽）。
  - **仅回滚二进制（option (a) 部署）＝无效回滚**：env 仍为 `ACCESS_CONTROL_ENABLED=true` → 旧二进制 service 门禁继续拒绝 S3 删除（file_delete.go:159/179、delete_marker.go:34/37）→ **403 依旧**；且门禁层换成 service 侧，REST/CLI/WebDAV 的写（access.go:162/170）与读（file_get.go:51）也同时被 gate（WebDAV 呈现 405）。症状与 adapter 门禁 403 不可区分——事故处置会被误导，S3 删除“看起来回滚了”实则没有。
  - 回滚后重新升级按 §5.3 正向步骤执行。

---

## 6. 验收映射（测试 ↔ AC）

探针判定性已在 HEAD 实跑复验（V1–V4）：缺陷类按预测存在，修复后是唯一翻转点。新测试落 `internal/api/s3compat/authz_gate_test.go`（AC-1..AC-5、AC-7）+ `internal/integration/`（AC-6）。四个 stub provider：`allowAllProvider` / `denyAllProvider` / `errProvider` / 真实 `access.Manager`（SQLite repo + `DefaultPolicy=deny`，构造 `NewManager(store, access.Config{Enabled: true, DefaultPolicy: access.DefaultDeny, ShareSecret: []byte("0123456789abcdef0123456789abcdef")})`，manager.go:31-48）。**`ShareSecret` ≥32 bytes 硬校验在 manager.go:44-45（`len(cfg.ShareSecret) < 32` 即返回 error）**——字面量取仓库既有测试常量 32 字节（access_test.go:32 同款；`"test-share-secret-32-bytes-min"` 仅 30 字节，会触发该校验）。AC-1/AC-4/AC-6 三处真实 Manager 构造均用此完整形式。

| AC（规格 §4） | 测试 | 判别断言 | HEAD 现状 | 修复后预期 |
|---|---|---|---|---|
| AC-1 provider wired：无 principal 或 provider-deny → 403（无 bucket policy） | `TestDeleteDeniedWithoutBucketPolicy`：表驱动 3 路径（plain / `?versionId` / delete-marker，先 PUT 建对象/版本）+ `TestDeleteDeniedWhenNoPrincipal`（真实 Manager——构造含 `ShareSecret`，见 §6 桩清单——ctx 不注入 principal → `missing_principal`，authorizer.go:26-27） | 全部 403 + XML `Code=AccessDenied`（errors.go:61/118） | V1：plain 204；无 principal 时 service 门禁 no-op（nil authorizer） | 3 路径全 403；`missing_principal` → 403 |
| AC-2 provider error → 403 非 500 | `TestDeleteProviderErrorIs403Not500`：`errProvider{err}` 单删 + 批删（2 keys） | 单删 403 `AccessDenied`；批删 200 + 每 key `Error.Code=AccessDenied`；**绝无 500** | V3：单删 error 形状落 `InternalError` 500 | 全 403 呈现 |
| AC-3 批删 per-key AccessDenied，被拒 key 不删除 | `TestBatchDeletePerKeyDenial`：`denyAllProvider` + key 判定 stub（对 `denied.txt` 拒、`allowed.txt` 放） | 200 + 1×`Deleted(allowed.txt)` + 1×`Error(denied.txt, AccessDenied)`；被拒 key 后续 GET 200、允许 key GET 404；全拒场景 0×Deleted + 2×Error | V2：两 key 全删 | per-key 拒绝 + 不删被拒 key |
| AC-4 被拒请求零 outbox 行 | `TestDeniedDeleteWritesNoOutboxRows`：SQLite repo + Migrate + 真实 Manager（构造含 `ShareSecret`，§6 桩清单）；单删被拒 + 批删 2 keys 全拒；repo 查询 `HasEventOutboxFact(originID, EventTypeFileDeleted11)` + `EventTypeFileNotify11` + `object_events` + `audit_log` | 被拒路径：全部 0 行；对照（allow stub 同 key 删除）：deleted@1.1 恰 1、notify@1.1 恰 1 | V4：放行路径确写 @1.1（对照成立）；被拒路径不可达（无门禁） | 被拒 0 行、放行 1+1 行 |
| AC-5 拒绝时无 @1.1 事件 | `TestDeniedDeleteEmitsNoEvent`：订阅 EventBus（events/bus_test.go 先例）+ `HasEventOutboxFact` | 订阅通道 0 事件；`HasEventOutboxFact == false` | 同上 | 通过 |
| AC-6 组合 e2e：mid-session 撤销 → S3 与 REST 均 403；恢复 → 204 + 有效 outbox | `TestCompositionRevokeRestoreParity`（`internal/integration`，fullserver_test.go harness 先例）：repo(SQLite) + `access.NewManager(store, access.Config{Enabled: true, DefaultPolicy: access.DefaultDeny, ShareSecret: []byte("0123456789abcdef0123456789abcdef")})`（ShareSecret ≥32B 硬校验，manager.go:44-45）→ `svc.WithAuthorizer(manager)` + `NewRouter(svc, logger, manager)`；principal P（`Kind=PrincipalUser`，**无 scopes/roles**——非 admin 才能让撤销生效，`isAdministrator` 短路会 allow，authorizer.go:53/:126——经 `access.WithPrincipal` 注入，access/context.go:7-10，auth_middleware.go:183 同款；授权 `PutACL`（manager.go:119，`ACLEntry{TenantID, Bucket, Key, ResourceKind, PrincipalType: PrincipalTypeUser, PrincipalID, Action: ActionDelete, Effect: EffectAllow}`，access_test.go:59 先例），撤销用 `DeleteACL(tenant, entry.ID)`，ID 取 PutACL 返回值（manager.go:231）） | ① `manager.PutACL(P, ActionDelete)` → S3 DELETE 204 + deleted@1.1 恰 1 行且 payload 含 `schema_version=1.1`/tenant/bucket/key/version_id/etag/size/request_id/actor（E13/E14）；② `DeleteACL` 撤销 → 不换 ctx：S3 403 **且** REST admin delete（rest/handler.go:245）403（parity）；outbox 计数不变；③ 重新授予 → S3 204 + 新 @1.1 行 | 无门禁：①、②S3 均 204 | ①③ 204 + outbox；② 双 403 + 零新增 outbox |
| AC-7 provider 未设置 → 默认拒绝（仅删除路径）+ 回归 | `TestDeleteDeniedWhenProviderUnset`：`NewRouter(svc, nil)` 单删/`?versionId`/delete-marker → **403**；批删 → **200 外壳 + 每 key `Error(AccessDenied)`**（D3：per-key 门禁，外壳不变，非整批 403）；回归：同 server GET 200 / PUT 200 / HEAD 200 / `?tagging` 删除 / `?uploadId` abort 不受影响；**bucket 级负向断言**：先 PUT `/{b}` + PUT `/{b}/obj` 使桶非空 → `DELETE /{b}` → **409 `BucketNotEmpty`**（服务层非空检查被到达 = 门禁未在 bucket 级误触发；门禁若误触发会 403 而非 409）；`DELETE /{b}?lifecycle` → **204**（bucket 子资源删除不在触发面：action=`s3:PutLifecycleConfiguration`，policy.go:27；先例 handler_test.go:712-737） | 删除路径全拒绝；非删除与 bucket 级全照常 | V1：删除 204 | 删除 403；非删除与 bucket 级逐字节不变 |

**文件尺寸预算（硬门禁 < 500 行/文件）**：`authz.go` ≈55；`policy.go` 237→≈252；`extra.go` 470→≈485（余量 15，若超限可将批删门禁拆入 `authz.go` 独立 helper——已按此设计，实际净增 ≈10）；`handler_test.go` 等既有文件仅签名行变更；`authz_gate_test.go` ≈290（含 AC-7 bucket 级负向断言 ≈+15 行）。均余量充足。

---

## 7. 实现（最终代码形态）

### 7.1 `internal/api/s3compat/authz.go`（新文件）

```go
package s3compat

import (
	"context"
	"log/slog"

	"github.com/aero-vault/aero-vault/internal/access"
)

// AuthorizationProvider is the s3compat-boundary port for fail-closed
// enforcement of vault.file.delete (access.ActionDelete) on S3 object
// deletion. The shape mirrors access.Authorizer so *access.Manager satisfies
// it structurally — same decision source as the FileService gate (REST
// parity), zero wrapper.
//
// A nil provider means "not configured" and MUST deny (fail-closed) — the
// deliberate inversion of the service-side nil-authorizer baseline
// (service/access.go:91-93), scoped to deletion only (AC-7).
type AuthorizationProvider interface {
	Authorize(ctx context.Context, principal access.Principal,
		action access.Action, resource access.Resource) (access.Decision, error)
}

// authorizeDelete enforces the single/per-key delete gate. Any of
// provider-unset, provider-error, or non-allow decision denies.
// R2: provider errors are logged, never surfaced to the client.
func (h *Handler) authorizeDelete(ctx context.Context, tenant, bucket, key string) bool {
	if h.authz == nil {
		return false
	}
	principal, _ := access.PrincipalFrom(ctx)
	decision, err := h.authz.Authorize(ctx, principal, access.ActionDelete,
		access.Resource{TenantID: tenant, Bucket: bucket, Key: key})
	if err != nil {
		h.logger.Warn("s3 delete authorization provider error; denying",
			"tenant", tenant, "bucket", bucket, "key", key, "err", err)
		return false
	}
	if !decision.Allowed {
		h.logger.Debug("s3 delete denied by provider",
			"tenant", tenant, "bucket", bucket, "key", key, "reason", decision.Reason)
		return false
	}
	return true
}

// allowAllProvider is the test double for the pre-existing test suite
// (AC-7 migration, §5.2): preserves today's allow semantics where a test
// asserts deletion succeeds.
type allowAllProvider struct{}

func (allowAllProvider) Authorize(context.Context, access.Principal,
	access.Action, access.Resource) (access.Decision, error) {
	return access.Decision{Allowed: true, Reason: "test_allow_all"}, nil
}
```

### 7.2 `internal/api/s3compat/policy.go` — 单删门禁（`authorizeS3Request` 末尾，copy-source 检查之后、:67 `return true` 之前）

```go
	if !h.checkBucketPolicy(w, r, bucket, key, action) {
		return false
	}
	srcBucket, srcKey, _, ok := parseCopySource(r.Header.Get("x-amz-copy-source"))
	if r.Header.Get("x-amz-copy-source") != "" && ok {
		if !h.checkBucketPolicy(w, r, srcBucket, srcKey, "s3:GetObject") {
			return false
		}
	}
	// FR-2: fail-closed vault.file.delete gate — object-level DELETE only.
	// Runs before any delete-path service call; R1: the bucket-policy
	// GetBucketConfig calls above are read-only and precede by design.
	if key != "" && action == "s3:DeleteObject" {
		if !h.authorizeDelete(r.Context(), mw.TenantFrom(r.Context()), bucket, key) {
			writeS3Error(w, r, service.ErrForbidden)
			return false
		}
	}
	return true
```

> `action` 与 `key` 均在函数内已计算（:50-56），`mw`/`service` 已 import（policy.go:9-15）。D2 表其余路径（GET/PUT/HEAD/子资源/`?tagging`/`?uploadId`/bucket 级）因 action 或 key 条件不满足而天然跳过。

### 7.3 `internal/api/s3compat/extra.go` — 批删 per-key 门禁（`deleteObjects` 循环内，:438-439 之间、`deleteS3Object` 之前）

```go
	for _, o := range in.Objects {
		if !h.authorizeDelete(r.Context(), tenant, bucket, o.Key) {
			out.Errors = append(out.Errors, deleteErrItem{
				Key: o.Key, VersionID: o.VersionID,
				Code: "AccessDenied", Message: "Access denied.",
			})
			continue
		}
		versionID, deleteMarker, err := h.deleteS3Object(
			r.Context(), tenant, bucket, o.Key, o.VersionID,
		)
```

> message 与单删 `writeS3Error(service.ErrForbidden)` 的 `s3CodeMessage["AccessDenied"]`（errors.go:84）逐字一致。XML 外壳与既有 `ErrNotFound` 分支（:443-455）共享同一响应结构。

### 7.4 装配

- `handler.go:25`：`func NewHandler(svc *service.FileService, logger *slog.Logger, authz AuthorizationProvider) *Handler`，`return &Handler{svc: svc, logger: logger, authz: authz}`；`router.go:14`：`func NewRouter(svc *service.FileService, logger *slog.Logger, authz AuthorizationProvider) chi.Router`，`h := NewHandler(svc, logger, authz)`。
- `cmd/server/http.go:120`：`r.Mount(cfg.S3Compat.Prefix, s3compat.NewRouter(svc, logger, accessManager))`。

### 7.5 验证序列

```
gofmt -l internal/api/s3compat/ cmd/server/        # 无输出
go build ./...
go vet ./...
go test ./internal/api/s3compat/ ./internal/integration/   # 新增 + 迁移后既有全绿
make check
```

---

## 8. 范围守卫（与规格 §5 一致，不扩展）

**强制边界 = per-surface（合规口径明示）**：本设计仅在 s3compat 边界对对象删除 fail-closed。`ACCESS_CONTROL_ENABLED=false`（默认）时，WebDAV DELETE/MOVE（dav.go:143 直连 `svc.Delete`）、REST 删除（rest/handler.go:245）、MCP `delete_file`（mcp/server.go:311）仍走 service 层 nil-authorizer 基线（access.go:91-93）放行——既有行为（§3-3），非静默绕过，但**系统级**删除 fail-closed 的合规要求必须 `ACCESS_CONTROL_ENABLED=true`（service 门禁覆盖全部表面；S3 与 REST 同决策源，AC-6）。S3 adapter 门禁是独立于该启用的第一道防线。

不做：service 侧 nil-authorizer 基线改动（access.go:91-93 / file.go:95-97）；`DeleteObjectTagging`（`?tagging`）/`AbortMultipartUpload`（`?uploadId`）/bucket 删除（`s3:DeleteBucket`）/bucket 子资源删除的门禁（D2 天然排除，零代码处理）；`checkBucketPolicy` allow-on-empty 语义变更（IAM 门禁保持现状，provider 为独立 AND 层）；新增 `"vault.file.delete"` 字面量权限/注册表（域内映射已存在：`ActionDelete="object:delete"` types.go:76 + `vault.file_admin` authorizer.go:129-137）；@1.1 事件 schema/outbox 新建或改造；S3 错误码 / `errors.go` 映射变更；provider 门禁扩展到非删除操作；SigV4/auth 中间件与中间件链（I4）改动；`access.Manager` 行为（含 `DefaultPolicy` 语义、`access_control_disabled` 早退 authorizer.go:19-21——注：该早退只在 Manager 自身 `cfg.Enabled=false` 时触发，而 `buildAccessManager` 在该配置下返回 nil，S3 侧根本不会拿到 disabled Manager，故不构成绕过）。
