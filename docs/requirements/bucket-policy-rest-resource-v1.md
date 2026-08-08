# 方向：REST /v1 路径强制 bucket-policy Resource 约束（当前 fail-open）

> **模块：** `internal/auth`（+ `internal/api/rest` 调用面）· **来源分析：** `docs/auto/analyses/internal-auth-ae3d8e54.json`（方向 #1）· **日期：** 2026-08-07
> **评分：** 价值 9 / 风险降低 9 / 工作量 4 / 置信度 9
> **本文所有代码引用均已对照仓库逐行验证**（行号以当前 HEAD `acfaaf4` 为准）；缺陷机理另经临时测试**实证复现**（见 E7，验证后已删除）。

---

## 1. 问题陈述

`internal/auth/policy.go` 的 bucket-policy 求值在**资源维度 fail-open**：

1. `Statement.matchesResource`（:130-138）在**被求值资源为空串时无条件返回 true**（:132 `if resource == "" || len(s.Resource) == 0 { return true }`）。而 `Eval`（:83-84）固定以 `resource=""` 委托 `EvalResource`——因此经 `auth.Allowed`/`Eval` 求值的调用方，policy 中**每一条 statement 的 `Resource` 约束都被静默忽略**。
2. REST 适配器的 `checkBucketPolicy`（`internal/api/rest/handler.go:85`）调用 `auth.Allowed(p, action, host)`，**不传任何资源 ARN**；而 S3 网关（`internal/api/s3compat/policy.go:126`）通过 `AllowedResource(policy, action, s3ResourceARN(bucket, key), ip)` 传具体 ARN。
3. `Eval` 的文档注释（:79-81）明示这是"为没有具体 S3 资源的调用方而保留的兼容行为"，并指示**新协议适配器应使用 `EvalResource`**——REST 适配器恰恰没有遵守。

**实证后果**（E7 复现）：policy `Allow s3:GetObject Resource ["arn:aws:s3:::default/secret/*"]` 在 `/s3` 上正确限定 `secret/*`，但在 `/v1` 上对**任意 key** 的 GET 均返回 `EffectAllow`——**资源级授权绕过（fail-open）**。反向：同 policy 改为 `Deny` 限定 `secret/*` 时，`/v1` 上**全部 key** 被拒（fail-closed，但与 policy 语义发散——`/s3` 上仅 `secret/*` 被拒）。

### 触发场景（真实工作流）

1. 管理员为公开分享配置 policy：`Allow s3:GetObject, Resource arn:aws:s3:::default/secret/*`（仅 `secret/` 前缀公开）。
2. `/s3` 网关按预期只放行 `secret/*`；**`/v1` REST（Web UI、预签名、SDK 的 REST 路径）放行同一 bucket 的全部 key** —— 包括本应受保护的 `other/*`。
3. 无任何日志/审计信号；管理员以为资源约束已生效。

---

## 2. 现状与代码证据（已逐条验证）

| # | 证据 | 验证结果 |
|---|------|---------|
| E1 | `policy.go:79-81` — `Eval` 文档注释明示"Resource constraints are intentionally ignored…New protocol adapters should use **EvalResource**"；`policy.go:83-84` — `Eval` 固定 `return p.EvalResource(action, "", sourceIP)` | ✅ 与引用一致 |
| E2 | `policy.go:96` — `EvalResource` 循环按 `!stmt.matchesAction(canonAction) \|\| !stmt.matchesResource(resource)` 过滤 statement | ✅ 与引用一致 |
| E3 | `policy.go:130-138` — `matchesResource`：`resource == ""`（:132）→ 返回 true，**任何 Resource 模式都匹配**；`len(s.Resource)==0`（缺省）→ 通配是既有设计 | ✅ 与引用一致 |
| E4 | `internal/api/rest/handler.go:65-89` — `checkBucketPolicy(w, r, action)` 签名**无 key 参数**；:85 `if !auth.Allowed(p, action, host)` —— 空资源求值 | ✅ 与引用一致 |
| E5 | REST 全部 7 个 policy 调用点均只传 action：`handler.go:97`（Put）、`:141`（PostForm）、`:173`（Get）、`:203`（Head）、`:241`（Delete）、`:254`（List）、`presign.go:32`（Presign）；各 handler 的 key 均已在手（`keyFromPath(r)`；Presign 在 `presign.go:28` 已 `TrimSuffix(…, "/presign")`） | ✅ 补充验证 |
| E6 | `internal/api/s3compat/policy.go:125-126` — `resource := s3ResourceARN(bucket, key)` → `auth.AllowedResource(...)`；:141-145 — ARN 格式 `"arn:aws:s3:::" + bucket`（key 空）或 `+ "/" + key`（key 非空） | ✅ 与引用一致 |
| E7 | **实证复现**（临时测试，验证后已删除）：`Allow s3:GetObject Resource ["arn:aws:s3:::default/secret/*"]` 下——`EvalResource("s3:GetObject", "arn:aws:s3:::default/secret/key1", ip)` → `EffectAllow`；`…/other/key2` → `EffectImplicitDeny`；`Eval("s3:GetObject", ip)`（空资源，即当前 /v1 路径）→ **`EffectAllow`（全部 key 放行）**。加 `Deny` 限定 `secret/*` 后：空资源求值 → `EffectDeny`（全部拒绝）；具体 ARN `…/other/key2` → `EffectAllow`（语义发散实锤） | ✅ 实证 |
| E8 | `internal/auth/policy_test.go:120`（`TestPolicy_ResourceMatching`）与 `:154`（`TestPolicy_BucketResourceAndExplicitDeny`）已锁定 **EvalResource + 具体 ARN** 的正确语义——AC-1 是其聚焦变体（采用方向指定的 `default/secret/*` 场景） | ✅ 补充验证 |
| E9 | REST 既有 6 个 policy 测试（`handlers_test.go:440-690`）全部使用通配 Resource `arn:aws:s3:::default/*` → 具体 ARN `arn:aws:s3:::default/<key>` 仍匹配，**无回归**；`TestBucketPolicyList`（:611）的 ListBucket 用 Allow `default/*` → bucket ARN `arn:aws:s3:::default` 不匹配 `default/*` → 修复前后均为 403 | ✅ 回归分析 |
| E10 | `grep` 全仓：`auth.Allowed(`/`.Eval(` 的生产调用方**仅** `rest/handler.go:85` 一处；修复后无生产调用方，但 `policy_test.go:36-115` 等既有测试依赖 `Allowed`/`Eval` → **保留为兼容 API，不删不改** | ✅ 补充验证 |

### 缺陷机理

```
/v1 GET /files/<key>
  → checkBucketPolicy(w, r, "s3:GetObject")          // handler.go:65,173
  → auth.Allowed(p, action, host)                    // handler.go:85 —— 无资源 ARN
  → Eval → EvalResource(action, "", host)            // policy.go:84
  → matchesResource("") == true                      // policy.go:132 —— 空资源匹配一切
  → 每条 statement 的 Resource 约束全部失效
  → key-scoped Allow 放行全部 key（fail-open 绕过）/ key-scoped Deny 拒绝全部 key（语义发散）
对比：/s3 网关同场景走 AllowedResource(policy, action, "arn:aws:s3:::default/<key>", ip) → 正确限定
```

修复面在 **REST 适配器层**（按 E1 文档注释的既有指引改用 `EvalResource`/`AllowedResource`），**不改动** `policy.go` 求值语义。

---

## 3. 需求规格

### FR-1：REST `checkBucketPolicy` 以具体资源 ARN 求值

`internal/api/rest/handler.go:65` 的 `checkBucketPolicy` 必须携带目标 key 并以 `auth.AllowedResource` 求值：

- 约束 a：签名扩展为 `checkBucketPolicy(w http.ResponseWriter, r *http.Request, key, action string) bool`；`key == ""` 表示 bucket 级动作。
- 约束 b：资源 ARN 构造与 s3compat `s3ResourceARN`（E6）**格式逐字符一致**：`key == ""` → `"arn:aws:s3:::" + service.DefaultBucket`；否则 `"arn:aws:s3:::" + service.DefaultBucket + "/" + key`。
- 约束 c：求值调用改为 `auth.AllowedResource(p, action, resource, host)`（`auth.Allowed` 不再被 REST 使用）。
- 约束 d：既有错误处理不变——policy 查找/解析失败仍 403 fail-closed（:69-78）；`net.SplitHostPort` 的 host 提取逻辑不变（:83-87）。

### FR-2：全部 7 个调用点传入 key

| 调用点 | action | 传入 key |
|--------|--------|---------|
| `handler.go:97` Put | `s3:PutObject` | `keyFromPath(r)` |
| `handler.go:141` PostForm | `s3:PutObject` | `key`（`r.FormValue("key")`，已计算） |
| `handler.go:173` Get | `s3:GetObject` | `keyFromPath(r)` |
| `handler.go:203` Head | `s3:GetObject` | `keyFromPath(r)` |
| `handler.go:241` Delete | `s3:DeleteObject` | `keyFromPath(r)` |
| `handler.go:254` List | `s3:ListBucket` | `""`（bucket 级 → bucket ARN） |
| `presign.go:32` Presign | `presignPolicyAction(op)` | `strings.TrimSuffix(keyFromPath(r), "/presign")`（`presign.go:28` 已计算） |

### FR-3：行为契约——REST 与 /s3 对同一 policy 判定一致

- key-scoped **Allow**（如 `arn:aws:s3:::default/secret/*`）：仅匹配 key 放行，不匹配 → **403**（修复前：全部放行——本方向消除的绕过）。
- key-scoped **Deny**：仅匹配 key 拒绝；不匹配 key 按其余 statement 正常判定（修复前：全部拒绝——本方向消除的语义发散）。
- 通配 policy（Resource 缺省、`"*"` 或 `arn:aws:s3:::default/*`）行为**不变**：既有 6 个 REST policy 测试（E9）与 S3 网关语义均无回归。

### FR-4：非功能约束

- `policy.go` 求值语义**零改动**：`matchesResource` 的 `resource == ""` 分支、`Eval`、`Allowed` 保持现状（E1 兼容契约 + E10 既有测试依赖）；修复面仅在 REST 适配器。
- `make check` 全绿（`gofmt -l` 无输出 / `go build ./...` / `go vet ./...` / `go test ./...`，AGENTS.md §0 硬门禁）；`handler.go`（~346 行）与 `presign.go` 改动量小，远低于 500 行/文件上限。
- 纯 stdlib，无新 `go.mod` 依赖（I6）；无 SQL/schema/迁移改动（I1/I2 不涉及）；不触碰存储 key 校验（I3）与中间件链（I4）。
- 安全方向变更须知：修复后，REST 上 key-scoped Allow 的 policy 将从"放行全部"收紧为"仅放行匹配 key"——这是消除授权绕过，属预期行为变化，需在变更说明中告知。

---

## 4. 验收标准（可测试）

> 测试基建（已验证）：`Policy`/`Statement` 字段全部导出（policy.go:22-31），测试可直接构造或走 `ParsePolicy`；`PolicyEffect` 常量 `EffectAllow`/`EffectDeny`/`EffectImplicitDeny`（:13-16）；REST 集成测试沿用 `setupTest`/`req`/`bodyPolicy` 基建与 `PUT /buckets/default/policy` 安装/清除流程（`handlers_test.go:19-21,440-460` 先例）。

### AC-1 单元测试：EvalResource 尊重 Resource 约束（`internal/auth/policy_test.go` 新增）

```go
func TestEvalResource_ResourceScopedAllow(t *testing.T) {
	p, err := ParsePolicy(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":"*","Action":"s3:GetObject",
		 "Resource":["arn:aws:s3:::default/secret/*"]}]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	tests := []struct {
		resource string
		want     PolicyEffect
	}{
		{"arn:aws:s3:::default/secret/key1", EffectAllow},        // 命中 Allow 作用域
		{"arn:aws:s3:::default/other/key1", EffectImplicitDeny},  // 作用域外：隐式拒绝
	}
	for _, tt := range tests {
		if got := p.EvalResource("s3:GetObject", tt.resource, "10.0.0.1"); got != tt.want {
			t.Errorf("EvalResource(%q) = %v, want %v", tt.resource, got, tt.want)
		}
	}
}
```

### AC-2 集成测试：REST 按 key 作用域强制 policy（`internal/api/rest/handlers_test.go` 新增）

> 测试机制说明：**必须先在安装 policy 之前创建两个对象**——policy 生效后 `PUT /v1/files/other` 本身会被 403（修复后的正确行为），无法再用作测试数据准备。policy 检查先于对象查找（`handler.go:173-174`），故 `other` 的 403 断言不依赖对象存在性，但 `secret/key1` 的 200 断言要求对象已存在。

```go
func TestBucketPolicyResourceScopedAllow(t *testing.T) {
	_, _, ts := setupTest(t)

	// 1) 先建对象（policy 安装后 PUT other 会被拒，故提前创建）。
	for _, k := range []string{"secret/key1", "other"} {
		resp, body := req(t, "PUT", ts.URL+"/files/"+k, []byte("data"), nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("setup PUT %s: status=%d want 201, body=%s", k, resp.StatusCode, body)
		}
	}

	// 2) 安装 key-scoped Allow policy：仅 secret/* 可读。
	policyURL := ts.URL + "/buckets/default/policy"
	scopedPolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*",
		"Action":"s3:GetObject","Resource":["arn:aws:s3:::default/secret/*"]}]}`
	resp, body := req(t, "PUT", policyURL, bodyPolicy(scopedPolicy), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set policy: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	// 3) 作用域外 key：403（修复前 fail-open 返回 200 —— 本测试锁定修复）。
	resp, body = req(t, "GET", ts.URL+"/files/other", nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET other after scoped Allow: status=%d want 403, body=%s", resp.StatusCode, body)
	}

	// 4) 作用域内 key：200。
	resp, body = req(t, "GET", ts.URL+"/files/secret/key1", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET secret/key1 after scoped Allow: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	// 5) 清除 policy。
	resp, _ = req(t, "PUT", policyURL, bodyPolicy(""), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear policy: status=%d want 200", resp.StatusCode)
	}
}
```

**AC-2b（可选，覆盖问题陈述第二半句——Deny 语义发散）：** 同一测试框架下将 policy 换为 `Allow s3:GetObject Resource "*"` + `Deny s3:GetObject Resource ["arn:aws:s3:::default/secret/*"]`，断言 `GET /files/other` → 200（修复前：403）而 `GET /files/secret/key1` → 403（两路径均应与 /s3 一致）。

### AC-3 门禁

- `go test ./internal/auth/... ./internal/api/rest/...` 全绿（含既有 `policy_test.go` 11 个测试与 `handlers_test.go` 6 个 policy 测试，E8/E9 无回归）。
- `go vet ./...` 无输出；`make check` 全绿。

---

## 5. 范围边界（明确不做）

| 不做 | 理由 |
|------|------|
| 修改 `matchesResource` 的 `resource == ""` 分支、`Eval`、`Allowed` 语义 | E1 文档化兼容契约 + E10：`Allowed`/`Eval` 无生产调用方但被既有测试依赖；修复在适配器层（EvalResource 已正确） |
| 删除/重命名 `Allowed`/`Eval` | 既有测试（`policy_test.go:36-115`）依赖；保留为兼容 API |
| 为 REST 新增 policy 检查端点（`/tags`、`/acl`、`/legal-hold`、`/restore`、`/batch/*`、`/thumbnail`、`/metadata` 等） | 本方向只修复**既有 7 个调用点**的资源维度；新端点门禁属独立方向 |
| 改动 s3compat 或抽取共享 ARN builder | s3compat 格式已正确（E6）；REST 侧按同格式内联构造即可，跨包重构超出本方向（可在实现时选择抽到 `internal/auth` 导出函数，非必需） |
| `internal/auth/arn.go` 的 `arn:aero:*` 体系 | 独立遗留子系统（`MatchARN`/`ResourceARNFromParts` 无生产调用方），未接入 policy 匹配，与本缺陷无关 |
| 存量 policy 审计/迁移、日志告警增强 | 独立方向；本方向只保证**同一 policy 两协议判定一致** |
| `EvalResource` 签名或条件/principal 语义 | 与缺陷无关（条件/principal 维度已在 `bucket-policy-fail-open-v1` 方向修复并验证） |

---

## 6. 实现指引（供验收后落地，非本规格交付物）

- `handler.go:65`：`checkBucketPolicy` 增加 `key string` 参数；在 `auth.Allowed` 调用处改为：
  ```go
  resource := "arn:aws:s3:::" + service.DefaultBucket
  if key != "" {
      resource += "/" + key
  }
  if !auth.AllowedResource(p, action, resource, host) {
      h.writeError(w, r, service.ErrForbidden)
      return false
  }
  ```
- 按 FR-2 表更新 7 个调用点（List 传 `""`；Presign 复用 `presign.go:28` 已计算的 key）。
- 新增测试按 §4 AC-1/AC-2 落地；运行 `go test ./internal/auth/... ./internal/api/rest/...` 与 `make check` 确认全绿。
