# 方向：Bucket policy 解析失败开放 —— 拼写错误的 Deny 静默降级为 Allow

> **模块：** `internal/auth` · **来源分析：** `docs/auto/analyses/internal-auth-ae3d8e54.json` · **日期：** 2026-08-06
> **评分：** 价值 8 / 风险降低 8 / 工作量 2 / 置信度 8
> **本文所有代码引用均已对照仓库逐行验证**（行号以当前 HEAD `c16f49d` 为准）。

---

## 1. 问题陈述

`internal/auth/policy.go` 的 IAM 风格 bucket policy 解析对三类非法输入**失败开放（fail-open）**：

1. **Effect 拼写错误**：`Statement.UnmarshalJSON`（:321 `s.Effect = raw.Effect`）把 Effect 字符串**原样存储、零校验**；`ParsePolicy`（:48）只校验条件（`validateConditions`），**从不校验 Effect**。`EvalResource`（:98-103）的 switch 仅匹配精确字符串 `"Deny"`/`"Allow"`——任何变体（`"deny"`、`"ALlow"`、`"Allow "`、缺失）都**静默跳过该 statement 的意图**。当同一 action 存在另一条 Allow statement 时，本应生效的 Deny 被降级为 Allow。
2. **Deny 中的未知条件操作符**：`matchesConditions`（:228 `default: return false`）对 IpAddress/NotIpAddress 之外的任何操作符一律返回 false → statement 永不匹配。**已验证的边界修正**：`validateConditions`（:238-241）已在解析期拒绝未知操作符，且 `TestParsePolicyRejectsUnsupportedOrInvalidIPConditions`（policy_test.go:180）覆盖——因此经 `ParsePolicy` 进入的 policy 已被该层拦截；残余风险是求值层 fail-open 潜伏（任何绕过 ParsePolicy 的构造路径都会静默跳过 Deny）。
3. **非 `"*"` Principal**：`matchesPrincipal`（:178）仅当候选值恰为 `"*"` 才匹配；`{"AWS":"arn:aws:iam::123:root"}` 这类 statement **永久 inert 且解析无任何错误**。若它是 Deny，则静默失效。

后果：`PUT ?policy` 上传一个带拼写错误 Effect 的 policy 不会报错（`validatePolicyWrite` 仅依赖 `ParsePolicy` 的返回值），访问控制被**静默削弱**——AWS 在**上传时**拒绝此类 policy，本模块应在**解析时**拒绝。

### 触发场景（真实工作流）

1. 管理员编写 Deny 语句时把 `Effect` 写成 `"deny"`（大小写错误）或编辑器带入尾随空格 `"Allow "`。
2. `PUT /s3/<bucket>?policy` 上传成功（`ParsePolicy` 返回 nil error，policy 落库）。
3. 每次请求求值时，Deny statement 被 `EvalResource` 的 switch 静默跳过；同 action 的 Allow statement 生效 → **本应被拒绝的请求被放行**。
4. 无任何日志/审计信号；管理员以为受保护的对象实际公开。

---

## 2. 现状与代码证据（已逐条验证）

| # | 证据 | 验证结果 |
|---|------|---------|
| E1 | `policy.go:321` — `s.Effect = raw.Effect`：Effect 原样存储，无校验 | ✅ 与引用一致 |
| E2 | `policy.go:48-74` — `ParsePolicy` 仅对每 statement 调用 `validateConditions`（:57-60 循环），**从不校验 Effect** | ✅ 与引用一致 |
| E3 | `policy.go:98-103` — `EvalResource` 的 `switch stmt.Effect` 仅精确匹配 `"Deny"`/`"Allow"`；其他值静默 continue | ✅ 与引用一致 |
| E4 | `policy.go:213-234` — `matchesConditions`：未知操作符命中 `default: return false`（:228）→ statement 永不匹配 | ✅ 与引用一致 |
| E5 | `policy.go:172-181` — `matchesPrincipal` 仅 `candidate == "*"`（:178）匹配；非通配 principal 的 statement 永久 inert，解析期无校验 | ✅ 与引用一致 |
| E6 | `policy_test.go` 共 11 个测试（:7/:14/:21/:28/:44/:63/:76/:95/:120/:154/:180），**无非法 Effect 用例**、**无非通配 principal 用例**；条件用例仅 `TestParsePolicyRejectsUnsupportedOrInvalidIPConditions`（:180） | ✅ 与引用一致 |
| E7 | `policy.go:236-241` — `validateConditions` **已**拒绝未知条件操作符（`unsupported condition operator %q`）→ 方向文中"未知操作符 fail-open"在 ParsePolicy 入口**已被拦截**；残余为求值层纵深防御缺口（本规格据此拆分 FR-2/FR-3） | ✅ 修正性验证 |
| E8 | `s3compat/policy.go:66-84` — `validatePolicyWrite`：PUT `?policy` 上传时调用 `auth.ParsePolicy`，解析失败 → `ErrInvalidArgs`（400）拒绝上传 → **ParsePolicy 收紧后上传边界自动闭合**，无需改 adapter | ✅ 补充验证 |
| E9 | `s3compat/policy.go:110-117` 与 `rest/handler.go:75-85` — 读路径 `checkBucketPolicy` 在已存 policy 解析失败时 **403 拒绝**（fail-closed）→ 存量坏 policy 在修复后表现为全部拒绝，属安全方向（运维须知） | ✅ 补充验证 |
| E10 | `service/file_bucket_policy.go:14-19` — `SetBucketPolicy` 同样先 `ParsePolicy` 校验；非测试代码中无直接 `Statement{}` 构造（`grep "Statement{"` 非测试文件为空）→ **ParsePolicy 是唯一构造咽喉点** | ✅ 补充验证 |

### 缺陷机理

`EvalResource` 循环（`policy.go:80-110`）：`matchesAction`/`matchesResource`/`matchesPrincipal`/`matchesConditions` 全部通过后进入 Effect 分支：

- Effect 为合法 `"Deny"` → `return EffectDeny`（正确）；
- Effect 为合法 `"Allow"` → `allow = true`（正确）；
- **Effect 拼写错误 → 两个 case 均不命中 → 静默 continue** → 若兄弟 Allow 已置位 → `return EffectAllow`（**fail-open**）；
- Principal 非 `"*"` → `matchesPrincipal` 返回 false → continue → **Deny 永久 inert**；
- 未知条件操作符（绕过解析构造时）→ `matchesConditions` 返回 false → continue → **Deny 永久 inert**。

```
PUT ?policy 上传拼错 Effect 的 policy → ParsePolicy 无错 → 落库
  → EvalResource switch 不命中 → Deny 静默跳过
  → 同 action 的 Allow 胜出 → EffectAllow → 请求放行
  → 访问控制被静默削弱，无日志、无审计
```

---

## 3. 需求规格

### FR-1：解析期 Effect 校验（拒绝上传）

`ParsePolicy` 的 per-statement 校验循环（与 `validateConditions` 同处，:57-60）必须校验 `Effect ∈ {"Allow","Deny"}`：

- 约束 a：**精确字符串匹配，不做 trim/大小写归一**——`"deny"`、`"ALlow"`、`"Allow "`、`"ALLOW"`、缺失（`""`）一律拒绝（AC-1 明文要求 `"deny"` 与 `"Allow "` 被拒）。
- 约束 b：错误信息带 statement 索引，沿用现有 `statement %d: ...` 前缀（:58）。
- 约束 c：`Statement.UnmarshalJSON` 保持原样存储；校验集中在 `ParsePolicy` 单一咽喉点（E10：所有非测试构造路径均经 ParsePolicy）。

### FR-2：求值期 fail-closed（纵深防御）

`EvalResource` 的 Effect 分支不得静默跳过未知值：未知 Effect 一律按**显式 Deny** 处理（返回 `EffectDeny`），绝不 continue。理由：跳过 + 兄弟 Allow 胜出 = `EffectAllow`（fail-open）；"按 Deny 处理"是唯一能同时满足 AC-2 两语句场景的语义（见 §4 说明）。

### FR-3：Deny 的未知条件操作符 / 非通配 Principal —— 永不静默跳过

不变量：**Deny statement 不得因无法求值的条件操作符或 principal 而永久 inert**。实现二选一（AC-3 接受任一分支）：

- **分支 A（推荐，与 AWS 拒收对齐）**：解析期拒绝。未知条件操作符已被 `validateConditions` 拒绝（E7，保持即可）；新增 Principal 校验——仅接受 `"*"` 及其等价形式（`{"AWS":"*"}` / `{"AWS":["*"]}`），其余（如 `{"AWS":"arn:aws:iam::123:root"}`）拒绝。
- **分支 B**：求值期按显式 Deny 处理——`EvalResource` 对 `Effect=Deny` 且 principal 非通配（或条件含无法求值的操作符）的 statement 返回 `EffectDeny`。注：`EvalResource(action, resource, sourceIP)` 签名无请求方 principal 上下文，此处理是 fail-closed 的保守近似（会过度拒绝，但不放行）。

无论哪一分支，**Allow statement 的非通配 principal 绝不构成授权**：保持现状 inert（"inert Allow = 隐式拒绝"是安全方向，允许）；"inert Deny"是 fail-open，禁止。

### 非功能约束

- `make check` 全绿（`gofmt` / `go build` / `go vet` / `go test`，AGENTS.md §0 硬门禁）；`policy.go` 346 行，改动量小，远低于 500 行/文件上限。
- 纯 stdlib，无新 `go.mod` 依赖（I6）；无 SQL/schema/迁移改动（I1/I2 不涉及）；不触碰存储 key 校验（I3）与中间件链（I4）。
- 读路径已 fail-closed（E9）：修复后存量坏 policy 的既有请求将 403——这是安全方向的行为变化，需在变更说明中告知运维；存量扫描/修复不在本方向范围（§5）。

---

## 4. 验收标准（可测试）

> 测试基建（已验证）：`Policy`（:20-24）/`Statement`（:26-31）字段全部导出，测试可直接构造 `Policy{Statement: []Statement{...}}` 走 `EvalResource`；解析路径测试沿用 `ParsePolicy(raw)` 表驱动模式（`policy_test.go:180-197` 先例）。`PolicyEffect` 常量：`EffectAllow`/`EffectDeny`/`EffectImplicitDeny`（:14-16）。

### AC-1 解析期拒绝非法 Effect（`policy_test.go` 新增）

```go
func TestParsePolicyRejectsInvalidEffect(t *testing.T) {
	invalid := []string{
		`{"Statement":[{"Effect":"deny","Action":"s3:*"}]}`,
		`{"Statement":[{"Effect":"ALlow","Action":"s3:*"}]}`,
		`{"Statement":[{"Effect":"Allow ","Action":"s3:*"}]}`,
		`{"Statement":[{"Effect":"ALLOW","Action":"s3:*"}]}`,
		`{"Statement":[{"Action":"s3:*"}]}`, // Effect 缺失 → ""
	}
	for _, raw := range invalid {
		if _, err := ParsePolicy(raw); err == nil {
			t.Fatalf("expected parse rejection for invalid Effect: %s", raw)
		}
	}
	// 合法 Effect 不受影响（回归保护）
	for _, raw := range []string{
		`{"Statement":[{"Effect":"Allow","Action":"s3:*"}]}`,
		`{"Statement":[{"Effect":"Deny","Action":"s3:*"}]}`,
	} {
		if _, err := ParsePolicy(raw); err != nil {
			t.Fatalf("valid Effect must parse: %s: %v", raw, err)
		}
	}
}
```

### AC-2 求值期：拼错 Effect 的 Deny 绝不降级为 Allow（`policy_test.go` 新增）

> 语义说明：两语句场景 `[Allow s3:GetObject, Deny(拼错)]` 下，唯一合规实现是"未知 Effect → 显式 Deny"（返回 `EffectDeny`）；若实现为"跳过"，则 Allow 胜出得 `EffectAllow`，违反本检查。单语句场景允许 `EffectDeny` 或 `EffectImplicitDeny`，但**绝不允许 `EffectAllow`**。

```go
func TestEvalResource_TypoEffectDenyNeverAllows(t *testing.T) {
	principal := map[string]interface{}{"*": "*"}
	p := &Policy{Statement: []Statement{
		{Effect: "Allow", Principal: principal, Action: []string{"s3:GetObject"}},
		{Effect: "Deny ", Principal: principal, Action: []string{"s3:GetObject"}}, // 拼写错误（尾随空格）
	}}
	if got := p.EvalResource("s3:GetObject", "", "10.0.0.1"); got != EffectDeny {
		t.Fatalf("typo'd Deny must not downgrade to Allow: got %v, want EffectDeny", got)
	}
	// 单独一条拼错 Effect 的 Deny：同样不得产出 Allow
	alone := &Policy{Statement: []Statement{
		{Effect: "deny", Principal: principal, Action: []string{"s3:GetObject"}},
	}}
	if got := alone.EvalResource("s3:GetObject", "", "10.0.0.1"); got == EffectAllow {
		t.Fatal("single typo'd Deny must never evaluate to Allow")
	}
}
```

### AC-3 Deny 的未知条件操作符 / 非通配 Principal 永不静默跳过（`policy_test.go` 新增）

> 分支 A（解析期拒绝）与分支 B（求值期显式 Deny）均满足；下列测试对两分支分别给出可验证断言，合取语义 = "拒绝于解析 **或** 显式 Deny，绝不 Allow、绝不跳过"。

```go
// 未知条件操作符：解析期必须拒绝（E7 既有行为，转为回归测试）+ 求值层纵深防御
func TestDeny_UnknownConditionOperatorNeverSkipped(t *testing.T) {
	raw := `{"Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject"},
		{"Effect":"Deny","Principal":"*","Action":"s3:GetObject",
		 "Condition":{"BogusOp":{"aws:SourceIp":["10.0.0.0/8"]}}}]}`
	if _, err := ParsePolicy(raw); err == nil {
		t.Fatal("unknown condition operator must be rejected at parse")
	}
	// 纵深防御：绕过解析直接构造时，Deny 不得因未知操作符被跳过
	principal := map[string]interface{}{"*": "*"}
	p := &Policy{Statement: []Statement{
		{Effect: "Allow", Principal: principal, Action: []string{"s3:GetObject"}},
		{Effect: "Deny", Principal: principal, Action: []string{"s3:GetObject"},
			Condition: map[string]map[string][]string{"BogusOp": {"aws:SourceIp": {"10.0.0.0/8"}}}},
	}}
	if got := p.EvalResource("s3:GetObject", "", "10.0.0.1"); got == EffectAllow {
		t.Fatal("un-evaluable Deny condition must not be skipped (fail-open)")
	}
}

// 非通配 Principal：拒绝于解析（分支 A）或求值期显式 Deny（分支 B），绝不跳过
func TestDeny_NonWildcardPrincipalNeverSkipped(t *testing.T) {
	raw := `{"Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject"},
		{"Effect":"Deny","Principal":{"AWS":"arn:aws:iam::123:root"},"Action":"s3:GetObject"}]}`
	p, err := ParsePolicy(raw)
	if err != nil {
		return // 分支 A：解析期拒绝 —— 满足 AC-3
	}
	// 分支 B：求值期显式 Deny
	if got := p.EvalResource("s3:GetObject", "", "10.0.0.1"); got != EffectDeny {
		t.Fatalf("Deny with non-wildcard principal must be explicit Deny, got %v", got)
	}
	// 对称约束：Allow + 非通配 principal 绝不构成授权（inert Allow 允许，授权禁止）
	p2, _ := ParsePolicy(`{"Statement":[{"Effect":"Allow",
		"Principal":{"AWS":"arn:aws:iam::123:root"},"Action":"s3:GetObject"}]}`)
	if p2 != nil && p2.EvalResource("s3:GetObject", "", "10.0.0.1") == EffectAllow {
		t.Fatal("Allow with non-wildcard principal must never grant")
	}
}
```

### AC-4 既有断言不回归

- `policy_test.go` 全部 11 个既有测试（:7-197）保持全绿：`go test ./internal/auth/`。
- `make check` 全绿（`gofmt -l` 无输出 / `go build ./...` / `go vet ./...` / `go test ./...`）。
- 上游调用方行为不变：`Allowed`/`AllowedResource`/`Eval` 包装（`policy.go:73,274,284`）签名与语义不变；`s3compat` PUT `?policy` 对合法 policy 的上传/求值流程不变（E8）。

---

## 5. 范围边界（明确不做）

| 不做 | 理由 |
|------|------|
| Effect 字符串 trim/大小写归一化（`" Allow"` 不修正为 `"Allow"`） | AC-1 明文要求 `"Allow "` 被拒；与 AWS 拒收对齐 |
| 求值期引入请求方 principal 上下文（改 `EvalResource(action, resource, sourceIP)` 签名） | 超出本方向；分支 B 的"按显式 Deny"已是 fail-closed 近似 |
| 新增条件操作符（`condition.go` 已声明 `validOperators` 集合：StringEquals/Bool/Date 等）或 `NotPrincipal` | 独立方向；本方向只要求"无法求值即拒绝或按 Deny" |
| 存量坏 policy 的扫描/修复/迁移 | 读路径已 fail-closed（E9，403）；修复工具属独立方向 |
| `validatePolicyWrite`/`SetBucketPolicy` 的错误码或 HTTP 状态变更 | 现有 `ErrInvalidArgs`（400）语义已正确，ParsePolicy 收紧自动传播（E8/E10） |
| `matchesAction`/`matchesResource`/`wildcardMatch`/`StringOrArray` 行为 | 与缺陷无关 |

---

## 6. 实现指引（供验收后落地，非本规格交付物）

- `policy.go:57-60`：在 `validateConditions` 调用处新增 Effect 校验，例如 `if s.Effect != "Allow" && s.Effect != "Deny" { return fmt.Errorf("statement %d: invalid Effect %q", index, s.Effect) }`。
- `policy.go:98-103`：switch 增补 `default: return EffectDeny`（未知 Effect 按显式 Deny，FR-2）。
- 分支 A 的 Principal 校验：在 `validateConditions` 旁新增校验函数（接受 `"*"`、`{"AWS":"*"}`/`{"AWS":["*"]}`，其余拒绝），于 `ParsePolicy` 循环内调用。
- 分支 B：`EvalResource` 在 `matchesPrincipal`/`matchesConditions` 之前判断"Deny + 无法求值（principal 非通配或条件含未知操作符）"→ 直接 `return EffectDeny`。
- 新增测试按 §4 四组验收落地；运行 `go test ./internal/auth/` 与 `make check` 确认全绿。
