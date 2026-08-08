# 设计：REST /v1 路径强制 bucket-policy Resource 约束 —— 配套设计文档 v1

> **配套规格：** `docs/requirements/bucket-policy-rest-resource-v1.md` · **模块：** `internal/auth`（调用面 `internal/api/rest`）· **状态：** 设计已定稿，未实现 · **日期：** 2026-08-07
> 本文是规格的落地设计：证据复核结论（含新增实证）、API 变更、设计决策、兼容性约束、失败模式、迁移步骤、验收映射、兄弟 pipeline 门禁发现处置、最终代码形态、门禁合规。

---

## 0. 证据复核结论（对规格逐条核验，HEAD `acfaaf4`）

规格全部代码引用（E1–E10）与仓库 HEAD `acfaaf4` 逐行复核**全部命中**，行号微调如下（HEAD 已含兄弟方向修复，行号与规格引用有 ±5 行漂移，内容一致）：

| # | 规格引用 | HEAD 复核结果 |
|---|---------|--------------|
| E1 | `policy.go:79-81` Eval 文档注释 + `:83-84` 固定 `EvalResource(action, "", sourceIP)` | ✅ `policy.go:78-82` 注释（"Resource constraints are intentionally ignored … New protocol adapters should use EvalResource"）；`:83-86` `Eval` → `return p.EvalResource(action, "", sourceIP)` |
| E2 | `policy.go:96` `matchesAction \|\| matchesResource` 过滤 | ✅ `policy.go:96`（EvalResource 循环首行） |
| E3 | `policy.go:130/132` `matchesResource` 空资源返回 true | ✅ `policy.go:130` 函数、`:132` `if resource == "" \|\| len(s.Resource) == 0 { return true }` |
| E4 | `handler.go:85` `auth.Allowed(p, action, host)` 无 ARN | ✅ `handler.go:65` `checkBucketPolicy(w, r, action string)` 无 key 参数；`:85` 唯一 `auth.Allowed(` 生产调用点 |
| E5 | 7 个调用点 | ✅ `handler.go:97`(Put)/`:141`(PostForm)/`:173`(Get)/`:203`(Head)/`:241`(Delete)/`:254`(List) + `presign.go:32`；各 handler 的 key 均已先于检查计算（PostForm 在 :135-137 已得最终 key，含 filename 回退） |
| E6 | `s3compat/policy.go:126` ARN 格式 | ✅ `policy.go:125-128` `s3ResourceARN(bucket, key)`：`"arn:aws:s3:::"+bucket`（key 空）或 `+"/"+key`（key 非空） |
| E7 | 实证复现 | ✅ **本设计重跑实证**（见 V2/V3） |
| E8 | `policy_test.go:120/:154` 锁定具体 ARN 语义 | ✅ 两测试存在且通过；`TestEvalResource_ResourceScopedAllow`（AC-1）本设计实测 **PASS**（见 V1） |
| E9 | 6 个既有 REST policy 测试全用 `default/*` | ✅ 逐一阅读 `handlers_test.go:442-677`：DenyPut/DenyGet/DenyDelete/ImplicitDeny/List/NoPolicy 全部用 `arn:aws:s3:::default/*` 通配 → 具体 ARN `default/<key>` 仍匹配；`TestBucketPolicyList` 仅 Allow GetObject（无 ListBucket）→ 修复前后均 403 |
| E10 | `Allowed`/`Eval` 无生产调用方（修复后） | ✅ 全仓 grep：生产调用方仅 `rest/handler.go:85` 一处；`policy_test.go:36-115` 等测试依赖 → 保留兼容 API |

**本设计新增独立核验（V1–V6）：**

| # | 核验 | 结论 |
|---|------|------|
| V1 | AC-1 探针（`TestEvalResource_ResourceScopedAllow`）对 HEAD 实跑 | ✅ **PASS**（`secret/key1`→Allow、`other/key1`→ImplicitDeny）——语义锁定测试，修复前后均绿 |
| V2 | AC-2 探针（REST key-scoped Allow 集成）对 HEAD 实跑 | ❌ **FAIL，绕过症状与规格完全一致**：`GET /files/other → 200, want 403`（空资源路径放行全部 key）——**回归探测器，修复后翻转绿** |
| V3 | AC-2b 探针（Deny 语义发散）对 HEAD 实跑 | ❌ **FAIL**：`Allow default/* + Deny secret/*` 下 `GET /files/other → 403, want 200`（空资源路径拒绝全部 key）——**发散探测器，修复后翻转绿** |
| V4 | 探针删除后基线 | ✅ `go build ./...`、`go vet ./...`、`go test ./internal/auth/ ./internal/api/rest/` 全绿；`gofmt -l` 无输出 |
| V5 | s3compat 门禁面（F2 证据） | ✅ `s3compat/policy.go:44-89` `authorizeS3Request` 注释明示"common policy gate for **every** S3 route"，以具体 ARN 求值；REST 仅 7 个调用点 → 两协议门禁面不对称实锤 |
| V6 | bucket-policy 求值调用面全景 | ✅ `ParsePolicy`/`AllowedResource` 生产调用方仅 4 处：`auth/policy.go`（定义）、`s3compat/policy.go`、`rest/handler.go`、`service/file_bucket_policy.go`（写路径校验）。WebDAV / MCP / `/share/{token}` 完全不评估 bucket policy（既有事实，超出本方向） |

> **探针纪律：** 两个探针测试文件（`internal/auth/zz_probe_test.go`、`internal/api/rest/zz_probe_test.go`）实跑后已删除，未留痕。

---

## 1. API 变更

**零对外 API 变更、零 schema 变更、零配置变更、零新端点。** 全部改动在 REST 适配器内部（1 个 unexported 方法签名 + 7 个调用点 + 1 个私有 helper），`policy.go` 求值语义零改动（FR-4）。

| 层 | 变更 |
|----|------|
| `rest.checkBucketPolicy`（unexported） | 签名 `(w, r, action string)` → `(w, r, key, action string)`；内部以 `auth.AllowedResource(p, action, bucketPolicyResourceARN(key), host)` 求值（替换 `auth.Allowed`） |
| 新增 `bucketPolicyResourceARN(key string) string`（包私有，`handler.go`） | `key==""` → `"arn:aws:s3:::" + service.DefaultBucket`；否则追加 `"/"+key` —— 与 s3compat `s3ResourceARN` **逐字符一致**（镜像，见 D1） |
| 7 个调用点 | 追加 key 实参（§8 表格） |
| `auth.Allowed` / `auth.Eval` | **不动**（E10：测试依赖，保留为兼容 API）；修复后 `Allowed` 生产调用方归零 |
| `auth.AllowedResource` | 已有导出函数，REST 侧复用；签名/语义不变 |
| HTTP 表面 | 路由、状态码（403 AccessDenied）、错误体格式均不变；仅判定输入从空资源变为具体 ARN |

---

## 2. 设计决策

### D1 — ARN 构造位置：REST 包内私有镜像函数（不导出、不动 s3compat）

| 候选 | 评估 |
|------|------|
| **a. rest 包内私有 `bucketPolicyResourceARN`（选定）** | 零新增导出 API；零 s3compat 改动（规格 §5 范围边界）；单文件 diff；格式由双侧单测锁定（s3compat `policy_test.go` 已锁其侧，REST 侧新增 format-lock 单测，§6 AC-5） |
| b. 导出 `auth.S3ResourceARN`，仅 REST 使用 | 新增导出 API 但 s3compat 仍保留自实现 → 仍是两份实现，还多一份 API 面 |
| c. 导出并迁移 s3compat 共用 | 最干净但触碰 s3compat，违反规格 §5"改动 s3compat 或抽取共享 ARN builder"范围边界 |

镜像处加交叉注释 `// mirrors internal/api/s3compat/policy.go s3ResourceARN`；跨包合并（候选 c）记录为后续重构方向，不入本变更。

### D2 — 参数顺序 `(w, r, key, action)`：镜像 s3compat `(w, r, bucket, key, action)`

key 在 action 之前，与 s3compat 既有签名的实参序一致，减少两适配器间认知迁移成本。`List`（bucket 级动作）传 `""`。

### D3 — bucket 级动作（List）→ bucket ARN

`key==""` → `arn:aws:s3:::default`，与 s3compat 列表路径（`s3ResourceARN(bucket, "")`）逐字符一致。**行为翻转**：`Allow s3:ListBucket Resource arn:aws:s3:::default/*` 在 /v1 从 200 → 403（/s3 今日已是 403；AWS 语义中 ListBucket 本就要求 bucket ARN，`/*` 为无效写法）——这是收敛而非回归（§3 变更 #3）。

### D4 — 错误语义保持 fail-closed 现状

policy 查找失败 / `ParsePolicy` 失败 / `p == nil` → 403（handler.go:69-78 不动）；`cfg.Policy == ""` → 放行（:76-78 不动）；`net.SplitHostPort` host 提取回退逻辑（:83-87 不动）；Deny 优先、Allow 需命中才放行 —— 全部由既有 `EvalResource` 语义继承，本设计只改变**传入的资源值**。

---

## 3. 兼容性约束

### 行为变更（4 项，均为消除缺陷的预期收紧，须写入发布说明）

| # | 场景 | 修复前（/v1） | 修复后（/v1） | 与 /s3 一致性 |
|---|------|--------------|--------------|--------------|
| 1 | key-scoped **Allow**（如 `default/secret/*`） | 全部 key 放行（**绕过**） | 仅作用域内 key 放行，作用域外 403 | 一致（本方向主修复） |
| 2 | key-scoped **Deny**（如 `default/secret/*`） | 全部 key 拒绝（语义发散） | 仅作用域内 key 拒绝；作用域外按其余 statement 判定 | 一致 |
| 3 | `Allow s3:ListBucket Resource default/*` | 200 | **403**（bucket ARN 不匹配 `/*`；/s3 今日已 403） | 一致 |
| 4 | 公开分享对象（`allowAnonymous`，Get/Head handler.go:176/206 在 policy 检查**之后**） | 作用域外分享对象 200（policy 放行后由 share 判定） | 作用域外分享对象 403（policy 优先拦截） | policy 优先于 share 的既有顺序不变，仅判定变严 |

### 保持不变的兼容面

- 无 policy 的 bucket：全操作放行（`TestBucketPolicyNoPolicyDoesNotBlock` 锁定）。
- 通配 policy（Resource 缺省 / `"*"` / `default/*`）：行为逐字节不变（E9 六个既有测试锁定）。
- 已签发预签名 URL：签发时 policy 判定收紧；执行时仍走 `Put`/`Get` handler → 同样以具体 ARN 求值（TASK.md 既有"bucket policy 可即时撤销已签发 GET 链接"语义保持）。
- `?version=` 版本读取、条件请求、Range：资源 ARN 仅含对象 key（与 s3compat 一致，versionId 不改变 ARN）。
- 错误码/状态码：全部拒绝仍为 403 `AccessDenied`。

---

## 4. 失败模式

| 模式 | 行为 | 处置 |
|------|------|------|
| ARN 格式与 s3compat 漂移 | 两协议判定不一致（安全或可用性回归） | 双侧 format-lock 单测（s3compat 已有 + 本设计 AC-5 新增）+ 镜像交叉注释 |
| key 含特殊字符（空格 `%20`、字面 `*`、`%2F` 解码斜杠） | `wildcardMatch` 值侧按字面匹配，`*` 仅 pattern 侧生效；与 s3compat 同源逻辑 | 无新风险；行为一致 |
| `RemoteAddr` 无端口 | `net.SplitHostPort` 失败 → 回退完整地址（既有逻辑 :83-87） | 不变 |
| policy 解析失败 / 查询失败 | 403 fail-closed（既有） | 不变 |
| `"Resource": []` 空数组 | `len(s.Resource)==0` → 通配（E3 既有设计） | 记录于已知分歧（§7.1），不属本方向 |
| **未门禁端点**（`POST /v1/multipart`×4、`/metadata`、`/thumbnail`、`/tags`、`/acl`、`/legal-hold`、`/batch/*`、`/folders/*`） | 不评估 bucket policy（既有 F2） | **本方向明确不扩大**（规格 §5）；记录为已知限制并指向后续方向（§7.3）。注：AC-2 仍有效——`Get` 在门禁内 |
| WebDAV / MCP / `/share/{token}` | 完全不评估 bucket policy（V6） | 既有事实，超出本方向，记录于 §7.3 |

---

## 5. 迁移步骤

**无 DB / 配置 / 数据迁移**（不触碰 I1/I2/I5：无 SQL、无 schema、无 flag、无新依赖 I6）。纯代码变更，回滚 = revert 提交（无数据副作用）。运营清单：

1. **发布说明**：列出 §3 的 4 项行为变更（尤其 #3 ListBucket `/*` 翻转与 #1/#2 收紧）。
2. **存量 policy 审计**：查询各 bucket 现存 policy（`GET /v1/buckets/{b}/policy`），凡含**非通配 Resource** 且被 /v1 客户端（Web UI/SDK REST 路径/预签名）使用的，验证访问仍符合意图——历史上这些约束在 /v1 未生效，收紧后可能 403。
3. **ListBucket 修正**：`s3:ListBucket` 的 Allow/Deny 若用 `arn:aws:s3:::default/*`，改为 bucket ARN `arn:aws:s3:::default` 或 `"*"`。
4. 无需停机；无持久化格式变化；无多副本协调（无状态变更）。

---

## 6. 验收映射

> 测试基建已核实：`setupTest`/`req`/`bodyPolicy`（handlers_test.go:19-30）可用（探针已实证）；`allowAllProvider` 使 FileService 授权层放行，AC-2 的 403 来自 `checkBucketPolicy`（policy 层），正交无干扰。

| AC | 测试 | 位置 | HEAD 实测 | 修复后 |
|----|------|------|----------|--------|
| AC-1 | `TestEvalResource_ResourceScopedAllow`（规格 §4 原文） | `internal/auth/policy_test.go` 新增 | ✅ PASS（语义锁定） | PASS |
| AC-2 | `TestBucketPolicyResourceScopedAllow`（规格 §4 原文；**先建对象再装 policy**——装后 `PUT other` 会被 403） | `internal/api/rest/handlers_test.go` 新增 | ❌ **FAIL**（`GET other → 200 want 403`） | PASS |
| AC-2b | `TestBucketPolicyResourceScopedDeny`（Allow `default/*` + Deny `secret/*`） | `handlers_test.go` 新增 | ❌ **FAIL**（`GET other → 403 want 200`） | PASS |
| AC-3 | `go test ./internal/auth/... ./internal/api/rest/...` + `go vet ./...` + `make check` | — | ✅ 基线绿（V4 实测） | 绿 |
| AC-4（新增，闭 P1-1/P1-2） | `TestBucketPolicyRejectsInvalidEffectAndPrincipal`：① 2-statement policy 坏 Effect 在 **index 1** → 400 且 body 含 `statement 1` 且未持久化；② 非法 Principal（`{"AWS":"arn:..."}`）→ 400 且未持久化 | `handlers_test.go` 新增 | 新测试，对 HEAD 即通过（写路径已 fail-closed，V8） | PASS |
| AC-5（新增） | `TestBucketPolicyResourceARNFormat`：`bucketPolicyResourceARN("") == "arn:aws:s3:::default"`；`bucketPolicyResourceARN("secret/key1") == "arn:aws:s3:::default/secret/key1"` | `handlers_test.go` 或同包测试新增 | 新测试，对 HEAD 即通过（函数随实现落地） | PASS |
| 回归 | 既有 `policy_test.go` 全部 + 6 个 REST policy 测试 | — | ✅ 全绿 | 全绿（E9 论证 + 实测） |

AC-4 的 `statement 1` 断言依据（V7 核实）：`service/file_bucket_policy.go:16-18` 将 `ParsePolicy` 错误包装为 `ErrInvalidArgs: invalid bucket policy: %v`，`ParsePolicy` 错误信息含 `statement N`（policy.go:60/68/72）；REST `classify`（handler_helpers.go:43-44）→ 400 `InvalidArgument` 且 message 透传 `err.Error()` → body 含 `statement 1`。

---

## 7. 兄弟 pipeline 门禁发现处置（逐项，含证据）

> 来源：`docs/auto/runs/bucket-policy-parsing-fails-open-on-invalid-effe-9edc16cb/artifacts/design_gate-6a76b0dd/task-1-design-gate.md`（VERDICT: FAIL）。其核心修复（Effect 校验、FR-2/FR-3、validatePrincipal/validateConditions）已随提交 `4cca6db` 合入 HEAD（V8 代码复核确认），本设计**不触碰这些代码**（FR-4）。

| 发现 | 处置 | 证据 |
|------|------|------|
| **P1-1** REST 400 测试缺失（新拒绝类） | ✅ **RESOLVED**：并入本设计 AC-4（测试侧，无生产代码变更） | AC-4 落地于 `handlers_test.go`；写路径 400 现状已核实（V7） |
| **P1-2** `statement N` 索引断言缺失 | ✅ **RESOLVED**：并入 AC-4 ①（2-statement policy 坏语句在 index 1，断言 body 含 `statement 1`） | 错误透传链核实（V7） |
| **P1-3a** `{"Service":"*"}` 过度接受未记录 | ✅ **RESOLVED**：本设计 §7.1 记录 + 实现阶段在 `docs/requirements/bucket-policy-fail-open-v1.md` 追加一行说明（docs-only） | 探针实测：`{"Service":"*"}` 解析通过且 `EvalResource` → `EffectAllow`（被当作通配接受；`UnmarshalJSON` map 分支原样保留，`validatePrincipal` 只检查取值 `"*"` 不检查键） |
| **P1-3b** 校验顺序注释缺失 | ⏭ **DISPOSITIONED**：代码复核确认 `ParsePolicy` 顺序 = Effect → conditions → principal（policy.go:58-73），与兄弟设计 §7.1 一致，**无分歧**；FR-4 禁止本方向改动 policy.go，注释属兄弟方向遗留的装饰项，不追加 | 代码复核 |
| **P1-3c** AC-2 弱断言（允许 ImplicitDeny） | ⏭ **DISPOSITIONED**：实现 default 分支返回**精确 `EffectDeny`**（policy.go:117-119），弱断言不会掩盖回归；收紧为可选，不属本方向 | 代码复核 |
| **F1** Action 字段 fail-open（typo'd/missing Action、NotAction 静默丢弃） | ⏭ **DISPOSITIONED OUT OF SCOPE**：属 policy **解析/求值层**缺陷（协议无关——/s3 同受其害，两适配器共用 `EvalResource`），FR-4 明确禁止本方向改 policy.go；归入独立方向（Action 字段解析期校验 + NotAction 拒绝）。**本方向不加重 F1**：修复后 /v1 与 /s3 判定完全一致 | 探针实测（HEAD）：`[Allow s3:DeleteObject, Deny Action:"s3:DeleteObjet"]` → `EffectAllow`；`[Allow s3:DeleteObject, Deny 无 Action]` → `EffectAllow`；`Deny NotAction` 解析通过但 NotAction 被 `UnmarshalJSON` 丢弃 |
| **F2** REST 未门禁端点（multipart/metadata/thumbnail/tags/acl/legal-hold/batch/folders） | ⏭ **DISPOSITIONED OUT OF SCOPE**：规格 §5 明确排除（"新端点门禁属独立方向"）。证据：s3compat `authorizeS3Request`（policy.go:44-89）为**全路由**统一门禁，REST 仅 7 个调用点 → 修复后 7 个端点资源语义正确，其余端点仍为独立绕过面。**已知限制显式记录**（§4），后续方向：REST 对象级端点统一接入 `checkBucketPolicy`。AC-2 有效性不受影响（Get 在门禁内） | grep 全量调用点（V5） |
| **F5** 垃圾 Principal → 通配授权 | ⏭ **DISPOSITIONED OUT OF SCOPE**：解析层缺陷（`UnmarshalJSON` 对非 string/map 类型静默置 nil → `matchesPrincipal` 对空 map 返回 true = 通配；`validatePrincipal` 只覆盖取值非 `"*"` 的字符串类）。归入与 F1 同一独立方向 | 探针实测（HEAD）：`"Principal": 5` 解析通过且 → `EffectAllow` |

### 7.1 已知分歧附录（本设计记录的既有行为）

- `{"Service":"*"}`（及任意 `{"<key>":"*"}` map）被当作通配接受——AWS 语义中 Service principal 应被拒绝；当前接受为通配**不会放宽授权**（通配即现状基线），属过度接受而非 fail-open，记录后由后续解析加固方向处理（P1-3a）。
- `"Resource": []` 空数组 = 通配（`matchesResource` :132，E3 既有设计）。
- WebDAV / MCP / `/share/{token}` 不评估 bucket policy（V6）。

---

## 8. 最终代码形态

### `internal/api/rest/handler.go`（344 → ~355 行，< 500 ✅）

```go
// checkBucketPolicy loads the bucket policy and denies the request when the
// action is not allowed for the concrete object/bucket resource. key == ""
// means a bucket-level action (resource = bucket ARN). Returns true when the
// request may proceed.
func (h *Handler) checkBucketPolicy(w http.ResponseWriter, r *http.Request, key, action string) bool {
	cfg, err := h.svc.GetBucketConfig(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket)
	if err != nil {
		h.logger.Warn("bucket policy lookup failed; denying request", "bucket", service.DefaultBucket, "err", err)
		h.writeError(w, r, service.ErrForbidden)
		return false
	}
	if cfg.Policy == "" {
		return true
	}
	p, perr := auth.ParsePolicy(cfg.Policy)
	if perr != nil || p == nil {
		h.logger.Warn("bucket policy parse failed; denying request", "bucket", service.DefaultBucket, "err", perr)
		h.writeError(w, r, service.ErrForbidden)
		return false
	}
	host, _, splitErr := net.SplitHostPort(r.RemoteAddr)
	if splitErr != nil {
		host = r.RemoteAddr
	}
	if !auth.AllowedResource(p, action, bucketPolicyResourceARN(key), host) {
		h.writeError(w, r, service.ErrForbidden)
		return false
	}
	return true
}

// bucketPolicyResourceARN mirrors internal/api/s3compat/policy.go s3ResourceARN
// byte-for-byte; the /v1 path is always service.DefaultBucket.
func bucketPolicyResourceARN(key string) string {
	resource := "arn:aws:s3:::" + service.DefaultBucket
	if key != "" {
		resource += "/" + key
	}
	return resource
}
```

### 7 个调用点（仅追加 key 实参，其余不变）

| 调用点 | 现签名调用 | 改为 |
|--------|-----------|------|
| `handler.go:97` Put | `checkBucketPolicy(w, r, "s3:PutObject")` | `checkBucketPolicy(w, r, key, "s3:PutObject")`（key 已算于 :96） |
| `handler.go:141` PostForm | 同 | `checkBucketPolicy(w, r, key, "s3:PutObject")`（key 已算于 :135-137，含 filename 回退） |
| `handler.go:173` Get | `checkBucketPolicy(w, r, "s3:GetObject")` | `checkBucketPolicy(w, r, key, "s3:GetObject")`（key 已算于 :172） |
| `handler.go:203` Head | 同 | `checkBucketPolicy(w, r, key, "s3:GetObject")`（key 已算于 :202） |
| `handler.go:241` Delete | `checkBucketPolicy(w, r, "s3:DeleteObject")` | `checkBucketPolicy(w, r, key, "s3:DeleteObject")`（key 已算于 :240） |
| `handler.go:254` List | `checkBucketPolicy(w, r, "s3:ListBucket")` | `checkBucketPolicy(w, r, "", "s3:ListBucket")`（bucket 级 → bucket ARN） |
| `presign.go:32` Presign | `checkBucketPolicy(w, r, action)` | `checkBucketPolicy(w, r, key, action)`（key 已算于 :28 `TrimSuffix(…, "/presign")`） |

### 测试新增（全部走既有基建）

- `internal/auth/policy_test.go`：AC-1 `TestEvalResource_ResourceScopedAllow`（规格 §4 原文，~30 行）。
- `internal/api/rest/handlers_test.go`：AC-2 `TestBucketPolicyResourceScopedAllow` + AC-2b `TestBucketPolicyResourceScopedDeny`（规格 §4 原文，~100 行）；AC-4 `TestBucketPolicyRejectsInvalidEffectAndPrincipal`（~35 行）；AC-5 `TestBucketPolicyResourceARNFormat`（~15 行）。
- 文件尺寸：policy_test.go 323→~355、handlers_test.go 698→~850 —— 均 `_test.go` 豁免（`engineering.yaml` filesize `ignore_patterns` 含 `_test.go`）；生产文件 handler.go ~355 < 500。

### 实现顺序

1. `handler.go`：签名 + helper + 7 调用点。
2. 新增 AC-1/AC-2/AC-2b/AC-4/AC-5 测试。
3. `gofmt -l` 无输出 → `go build ./...` → `go vet ./...` → `go test ./internal/auth/... ./internal/api/rest/...` → `make check`。
4. docs-only：`bucket-policy-fail-open-v1.md` 追加 P1-3a 一行说明（Service 过度接受）。
5. 变更说明（提交信息/发布说明）：§3 四项行为变更。

---

## 9. 门禁合规（`make check` = fmt vet vet-integration build test cli-check）

- **gofmt**：改动为局部、格式由 `gofmt -l` 验证（基线已实测无输出）。
- **build/vet**：基线实测绿；改动不新增符号依赖。
- **test**：基线 `go test ./...` 绿（V4 实测两包）；新增测试全部以既有基建编写，无网络/无 Docker（AGENTS.md §0 SQLite+local FS 基线）。
- **文件 ≤ 500 行**：生产文件 handler.go ~355、presign.go 163、policy.go **零改动**（381，维持）；测试文件豁免。
- **I1/I2**：无 SQL；**I3**：无存储 key 语义改动（ARN 仅策略判定，非存储寻址）；**I4**：中间件链零改动（handler 内判定，无新中间件）；**I5**：无新 flag；**I6**：纯 stdlib，无新依赖。
- **约定**：函数 ≤ 50 行（checkBucketPolicy ~30 行、helper 6 行）；圈复杂度不变（无新分支结构）；无 God 类型。

---

## 10. 一句话总结

在 REST 适配器层把 `checkBucketPolicy` 从空资源求值改为**具体对象/桶 ARN 求值**（7 个既有调用点传 key，`ListBucket` 传 bucket ARN，ARN 格式与 s3compat 逐字符镜像并由单测锁定），使 /v1 与 /s3 对同一 policy 判定一致——消除 key-scoped Allow 的 fail-open 绕过与 key-scoped Deny 的语义发散；`policy.go` 求值语义零改动；兄弟门禁 P1-1/P1-2 以测试并入本方向解决，P1-3/F1/F2/F5 逐项带证据显式处置。
