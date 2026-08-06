# 设计：Bucket policy 解析失败开放修复 —— 配套设计文档 v1

> **配套规格：** `docs/requirements/bucket-policy-fail-open-v1.md` · **模块：** `internal/auth` · **状态：** 设计已定稿，未实现 · **日期：** 2026-08-06
> 本文是规格的落地设计：证据复核结论、API 变更、设计决策（含逐类行为保持性证明）、兼容性约束、失败模式、迁移步骤、验收映射、最终代码形态。

---

## 0. 证据复核结论（对规格逐条核验）

规格全部代码引用（E1–E10）与仓库 HEAD `c16f49d` 逐行一致，**无需修正**；行号复核：`ParsePolicy`=:48、Effect switch=:98、`matchesPrincipal`=:172/178、`matchesConditions`=:213/228、`validateConditions`=:236、`s.Effect = raw.Effect`=:321，全部命中。

规格已折叠的两处修正（本设计复核认可）：
- **C1（E7）**：未知条件操作符已在解析期被 `validateConditions` 拒绝（`unsupported condition operator %q`），且 `TestParsePolicyRejectsUnsupportedOrInvalidIPConditions`（policy_test.go:180）覆盖 → 规格据此拆分 FR-2（求值期 fail-closed）与 FR-3（永不静默跳过）。
- **C2（E8/E10）**：`ParsePolicy` 是唯一构造咽喉点（全仓库非测试代码无 `Statement{` 直构），且 `s3compat/policy.go:66-84` 的 `validatePolicyWrite` 与 `service/file_bucket_policy.go:14-19` 的 `SetBucketPolicy` 均以 `ParsePolicy` 返回值为上传闸门 → 解析收紧自动闭合 `PUT ?policy` / REST PUT policy，零 adapter 改动。

本设计阶段新增的独立核验（V1–V6）：

| # | 核验 | 结论 |
|---|------|------|
| V1 | **AC 探针可判别性（实跑复验）**：按规格 §4 编译 AC-1..AC-4 为临时测试，对 HEAD 运行 | 4 个探针全部按预测失败：AC-1 `expected parse rejection for invalid Effect: {"Statement":[{"Effect":"deny",...}]}`；AC-2 `typo'd Deny must not downgrade to Allow: got 0, want EffectDeny`（got 0=`EffectAllow`，即 fail-open 实锤）；AC-3a `un-evaluable Deny condition must not be skipped (fail-open)`；AC-3b `Deny with non-wildcard principal must be explicit Deny, got 0`。`-skip 'TestProbe_AC'` 后 `internal/auth` 全包 green → 修复后这些探针是唯一翻转点 |
| V2 | 全仓库（含 `_test.go`）`grep "Statement{"` 零命中 | 无任何测试直构 `Statement` → FR-2 求值分支改动**零回归面**；`EvalResource` 行为只被 `Allowed`/`AllowedResource` 消费（s3compat/policy.go:117、rest/handler.go:85） |
| V3 | REST 错误映射：`classify`（rest/handler_helpers.go:19-60）`ErrInvalidArgs → 400 "InvalidArgument"`；s3compat `writeS3Error(service.ErrInvalidArgs)` 同样 400 | 解析拒绝自动以 400 呈现，**无状态码/错误码变更** |
| V4 | 存量坏 policy 的补救通道存在：S3 `DELETE ?policy` / REST `DELETE /v1/buckets/{b}/policy` → `SetBucketPolicy(ctx, tenant, bucket, "")`，`policy != ""` 守卫跳过解析 | 清理无需合法替代文档（§5 迁移步骤依赖此通道） |
| V5 | `matchesPrincipal` 对 `len(s.Principal)==0` 返回 true → **缺省 Principal = 通配，是活语句**（非 inert） | 不在缺陷类内；设计保持该语义（D4） |
| V6 | s3compat 已有 `TestInvalidBucketPolicyRejectedAndStoredInvalidPolicyFailsClosed`（policy_test.go:43-64）证明写拒绝 400 + 存量坏 policy 读 403 | 新增校验的 adapter 层测试沿用同一 harness（§6） |

---

## 1. API 变更

**零签名变更、零 schema 变更、零配置变更。** 全部改动在 `internal/auth/policy.go` 内部（+ 测试文件）。

| 层 | 旧行为 | 新行为 |
|----|--------|--------|
| `ParsePolicy` | 仅校验条件；Effect/Principal 任意值放行 | **FR-1**：`Effect ∉ {"Allow","Deny"}` → 错误（带 statement 索引）；**FR-3 分支 A**：非通配 Principal → 错误（带 statement 索引） |
| `EvalResource` | switch 精确匹配，未知 Effect **静默 continue**（fail-open） | **FR-2**：未知 Effect → `return EffectDeny` |
| `EvalResource` | Deny + 非通配 Principal / 不可求值条件 → `matchesPrincipal`/`matchesConditions` false → **静默跳过**（fail-open） | **FR-3 求值层**：`EffectDeny` 且（principal 非通配 或 条件不可求值）→ `return EffectDeny` |
| `Eval`/`Allowed`/`AllowedResource` | — | 签名与语义不变（包装 `EvalResource`） |
| HTTP：S3 `PUT ?policy` / REST `PUT /v1/buckets/{b}/policy` | 非法 Effect/Principal 上传成功 200/204（缺陷） | **400**（`ErrInvalidArgs`，既有错误路径新触发点，V3） |
| HTTP：读路径（s3compat/rest） | 存量坏 policy 已 403 fail-closed（E9，现状） | 不变 |

---

## 2. 设计决策

### D1 — FR-2 语义：未知 Effect 按**显式 Deny**（`return EffectDeny`），而非跳过、亦非仅隐式拒绝

候选语义对比（两语句场景 `[Allow s3:GetObject, Deny(拼错)]`）：

| 候选 | 结果 | 是否满足 AC-2 |
|------|------|--------------|
| 静默跳过（现状） | 兄弟 Allow 胜出 → `EffectAllow` | ✗（fail-open） |
| 视为不匹配、整体回退隐式拒绝 | 语义含混：无法区分"无匹配"与"有坏语句"；单语句场景得 `EffectImplicitDeny` | AC-2 单语句断言恰好允许 `EffectDeny`/`EffectImplicitDeny`，但两语句场景必须非 Allow——隐式拒绝需要额外簿记"遇到过坏语句"，复杂度更高 |
| **未知 Effect → `return EffectDeny`** | 两语句 → `EffectDeny`；单语句 → `EffectDeny`（AC-2 允许） | ✓ 唯一同时满足两语句与单语句断言的直白语义 |

### D2 — 求值循环重构：Effect switch 前移，Deny 分支先做"可求值性"守卫

将 `matchesPrincipal`/`matchesConditions` 从循环级 continue 移入 switch 分支，逐输入类证明行为保持性（**证明：除三个 fail-open 类外，所有输入类行为逐字节不变**）：

| 输入类 | 旧行为 | 新行为 | 判定 |
|--------|--------|--------|------|
| Allow + 通配 principal + 可求值条件，条件命中 | allow=true | allow=true | 不变 |
| Allow + 通配 principal + 可求值条件，条件未命中 | 跳过 | 跳过 | 不变 |
| Allow + 非通配 principal | 跳过（`matchesPrincipal` false） | 守卫失败 → 不授权 | 不变（对称约束：inert Allow 允许，授权禁止） |
| Allow + 不可求值条件（BogusOp） | `matchesConditions` default false → 跳过 | 守卫后 `matchesConditions` false → 不授权 | 不变 |
| Deny + 通配 principal + 可求值条件，条件命中（IP 在范围内） | `EffectDeny` | `EffectDeny` | 不变 |
| Deny + 通配 principal + 可求值条件，条件未命中（IP 不在范围内） | 跳过（**正确语义**：Deny 只作用于该范围） | `matchesConditions` false → 跳过 | 不变（关键：不得把"条件未命中"误判为"不可求值"） |
| **Deny + 非通配 principal** | 跳过（fail-open） | `EffectDeny` | **修复** |
| **Deny + 不可求值条件**（含畸形 CIDR 直构） | 跳过（fail-open） | `EffectDeny` | **修复** |
| **未知 Effect** | 跳过（fail-open） | `EffectDeny` | **修复** |

改动后形态（§7）。注意 `matchesAction`/`matchesResource` 仍先于 switch——resource 不匹配的 Deny 依旧不否认（`TestPolicy_BucketResourceAndExplicitDeny` 回归保护，已核对该测试只含合法 Effect/通配 principal）。

### D3 — FR-3 取分支 A（解析期拒绝非通配 Principal）+ 无条件求值层守卫

- **分支 A**：`validatePrincipal` 仅接受缺省（=通配，D4）、`"*"`（字符串，被 `UnmarshalJSON` 归一为 `{"*":"*"}`）、`{"AWS":"*"}`、`{"AWS":["*"]}`；其余（`{"AWS":"arn:..."}`、`{"Service":...}`、多值数组含非 `*`）一律拒绝。
- **同时保留求值层守卫**（Deny + 非通配 → `EffectDeny`）：AC-3b 在分支 A 下 `ParsePolicy` 早退 return，其求值断言成为死代码——故 §6 增补直构测试 `TestEvalResource_DenyNonWildcardPrincipalFailsClosed` 使求值层守卫始终被测试。

**选 A 而非 B 的理由：** ① A 使"never-skipped 不变量"在唯一咽喉点（E10）**结构性成立**，求值守卫降为纵深防御而非主机制；② 写入期 400 + statement 索引是**响亮失败**（fail-loud-at-write，与方向主旨一致），B 的"存量 Deny 静默翻转"无信号；③ 与 AWS 拒收姿态对齐——本模块无法求值 principal（`EvalResource` 无请求方 principal 上下文，规格 §5 已排除改签名），不能兑现的语义就应拒绝而非假装。B 保留为文档化备选（§4 失败模式亦覆盖其差异）。

### D4 — 缺省 Principal 保持通配语义

`matchesPrincipal` 对空 principal 返回 true（V5），该语句是**活的**（对所有人生效）——不属于缺陷类（缺陷类是"inert 且无信号"）。分支 A 的 `validatePrincipal` 对空 map 无操作。**不**引入"缺省 Principal 拒绝"（那会是行为破坏且超出方向）。

### D5 — 不做 trim/大小写归一，错误信息带 statement 索引

`"Allow "`、`"deny"`、`"ALLOW"` 一律拒绝（AC-1 明文要求；AWS 同样拒收）。错误格式沿用 `statement %d: ...` 前缀（policy.go:58 先例）：`statement %d: invalid Effect %q (must be "Allow" or "Deny")` 与 `statement %d: unsupported Principal %q: only "*" is supported`。校验顺序：先 `validateConditions`（既有），再 Effect，再 Principal——无测试依赖错误优先级。

### D6 — 求值期"可求值性"复用 `validateConditions`，不新增平行实现

`EvalResource` 的 Deny 分支以 `stmt.validateConditions() != nil` 判定不可求值（含畸形 CIDR → `net.ParseCIDR` 失败）。成本：每次 Deny 求值多一轮 CIDR 解析——`matchesConditions`/`ipInAnyCIDR` 本就逐值解析，量级相同且 policy 极小、每次请求重新 `ParsePolicy`（无缓存），可忽略。单一 allow-list 来源避免双份定义漂移（对照 `condition.go` 的 `validOperators`——那是独立的前瞻性算子集合，本方向不触碰）。

---

## 3. 兼容性约束

1. **所有今日被兑现（honored）的 policy 行为逐字节不变**：凡 Effect ∈ {Allow,Deny}、principal 缺省或通配、条件为受支持算子且经 `ParsePolicy` 进入的语句，D2 已证明求值结果与现状完全一致（含 IP 范围命中的 Deny、resource 不匹配的 Deny、NotIpAddress 等全部既有测试场景）。
2. **新写入拒绝只命中"从未被兑现"的语句**：非通配 principal 的 Allow 今日即 inert（不授权），非通配 principal 的 Deny 今日即失效（fail-open）——没有任何*可工作的* policy 会被新解析拒绝。工作流破坏面为零。
3. **读路径行为变化仅针对坏 policy**：存量坏 policy 在部署后从"静默弱化"翻转为 403（E9，fail-closed 方向），属安全方向行为变化，需运维知悉（§5）。
4. **HTTP 面不变**：400（写入拒绝）与 403（存量坏 policy 读）均为既有错误码/既有错误路径；`Allowed`/`AllowedResource`/`Eval` 签名与语义不变；SDK/CLI/Web UI 无需改动。
5. **门禁面**：纯 stdlib（I6，无 `go.mod` 变更）；无 SQL/schema（I2 不涉及）；不触碰 key 校验（I3）与中间件链（I4）；`policy.go` 346→约 375 行、`policy_test.go` 191→约 320 行，均 < 500 行硬门禁。

---

## 4. 失败模式

| 场景 | 行为 | 缓解 |
|------|------|------|
| 管理员上传含非通配 principal 的 AWS 风格 policy（如照抄 AWS 文档的 `{"AWS":"arn:..."}` Allow 语句） | **400 拒绝**——今日该语句本为 inert（不授权），被拒的是"不兑现的意图"而非工作流 | 错误信息带 statement 索引与 `only "*" is supported` 指引；文档注明对象级隔离走 ACL（`/acl`）/ `Resource` 匹配（`TestPolicy_ResourceMatching` 已支持），principal 级隔离超出本模块能力 |
| 存量 policy 含非法 Effect/非通配 principal，未做迁移 | 部署后该 bucket 所有请求 **403** + 每请求一条 warn 日志（`checkBucketPolicy` "bucket policy parse failed"，既有行为） | §5 迁移清单：部署前扫描、部署前清理/重传；403 是安全方向且可观测（有日志、有 403 状态码），优于静默放行 |
| 求值层守卫过度拒绝（直构 Deny + 非通配 principal，本意"仅拒该 ARN"） | `EffectDeny`（全拒） | 生产不可达：所有写入经 `ParsePolicy`（E10），分支 A 已拒绝此类 policy；仅测试直构可达，且"拒绝"是 fail-closed 保守近似（规格 §3 分支 B 注释同款理由） |
| 实现误把"条件未命中"当"不可求值" | Deny + `IpAddress` 不含请求 IP 时错误否认 | D2 设计已区分：可求值性只查**算子/键/值形态**（`validateConditions`），命中与否由 `matchesConditions` 判定——实现评审须对照 D2 表第 6 行（`TestPolicy_DenyOverridesAllow`/`TestPolicy_NotIpAddress` 回归保护） |
| 条件算子扩展（未来 `condition.go` 算子接入求值） | 若只扩展 `matchesConditions` 而不同步 `validateConditions`，新算子 Deny 会在解析期被拒（fail-closed）而非静默 | 这是安全方向；扩展算子属于独立方向（规格 §5），届时须同步两处 allow-list（D6 单一来源原则） |
| 回滚 | 旧二进制恢复宽松解析：坏 policy 回到 fail-open | 仅应急回滚；正向部署优先（§5） |

---

## 5. 迁移步骤

无数据迁移（I2 不涉及）、无配置迁移、无 API 版本化。操作清单：

1. **部署前盘点（只读，可选但推荐）**：`sqlite3 var/aero.db "SELECT tenant, bucket FROM bucket_configs WHERE policy != ''"`；对每个 bucket `GET /v1/buckets/{b}/policy`（或 S3 `GET ?policy`），用新解析器（部署前可用临时 `go test` 或 `go run` 探针）判定是否会被拒绝。
2. **对受影响 bucket 择一处理（部署前完成，避免 403 窗口）**：
   - (a) 重传修正后的 policy（Effect 精确 `Allow`/`Deny`，principal 仅通配/缺省）；或
   - (b) 清空 policy：S3 `DELETE ?policy` / REST `DELETE /v1/buckets/{b}/policy`（V4：`SetBucketPolicy("")` 绕过解析闸门，无需合法替代文档）。清空后权限回落到 ACL 基线——注意这本身可能放宽访问，须按 bucket 的实际 ACL 评估。
3. **部署二进制**。`make check` 全绿（gofmt/build/vet/test）；无 schema 变更故无需迁移执行。
4. **部署后验证**：抽查通配 policy bucket 的允许/拒绝行为与部署前一致（`TestBucketPolicyProtectsBucketSubresources` 等 adapter 测试全绿即证明）；观察日志中新增的 "bucket policy parse failed" 告警。
5. **回滚**：旧二进制对存量 policy 宽松解析，读路径无写操作、存储未变 → 回滚无损；但坏 policy 的 fail-open 随之回归，故回滚仅限紧急情况，回滚后应立即执行步骤 2。

---

## 6. 验收映射（测试 ↔ AC）

探针判定性已在设计阶段实跑复验（V1）：四组 AC 在 HEAD 上**恰好按预测原因失败**、修复后是唯一翻转点、非探针套件保持 green——因此"测试变绿"即"缺陷修复"的判别力成立。

| AC（规格 §4） | 测试（`internal/auth/policy_test.go` 新增） | 判别断言 | 修复后预期 |
|---|---|---|---|
| AC-1 解析期拒绝非法 Effect | `TestParsePolicyRejectsInvalidEffect` | 5 种非法（`deny`/`ALlow`/`Allow `(尾随空格)/`ALLOW`/缺失）+ 2 种合法回归 | 非法 → err；合法 → 无 err |
| AC-2 拼错 Effect 的 Deny 绝不降级 Allow | `TestEvalResource_TypoEffectDenyNeverAllows` | 两语句 `[Allow, "Deny "]` → `EffectDeny`（禁止 `EffectAllow`）；单语句 `"deny"` → 非 Allow | 两断言通过 |
| AC-3a 未知条件操作符永不静默跳过 | `TestDeny_UnknownConditionOperatorNeverSkipped` | 解析期拒绝（E7 回归化）+ 直构 BogusOp Deny → 非 Allow（求值层） | 通过 |
| AC-3b 非通配 Principal 永不静默跳过 | `TestDeny_NonWildcardPrincipalNeverSkipped` | 解析拒绝**或**求值 `EffectDeny`（分支 A 下走解析拒绝早退）+ 对称守卫：Allow + 非通配绝不授权 | 通过 |
| 分支 A 解析层（新增，非规格强制） | `TestParsePolicyRejectsNonWildcardPrincipal` | 表驱动：3 非法（arn / 多值数组含 arn / Service）+ 4 合法（`"*"` / `{"AWS":"*"}` / `{"AWS":["*"]}` / 缺省） | 通过 |
| 求值层守卫常测（分支 A 下 AC-3b 求值断言成死代码，补直构测试） | `TestEvalResource_DenyNonWildcardPrincipalFailsClosed` | 直构 `[Allow*, Deny{arn}]` → `EffectDeny`；直构 `[Allow{arn}]` → 非 Allow | 通过 |
| AC-4 既有断言不回归 | 既有 11 个 policy 测试 + `go test ./...` + `make check` | 全绿 | 通过 |
| adapter 自动闭合（E8 端到端证明，可选） | `internal/api/s3compat/policy_test.go` 新增 `TestPutBucketPolicyRejectsInvalidEffect`（沿用 `newPolicyTestServer` harness，先例 `TestInvalidBucketPolicyRejectedAndStoredInvalidPolicyFailsClosed`） | `PUT ?policy` 带 `"Effect":"deny"` → **400** 且不落库（随后 GET 200）；直插 `SetBucketPolicy` 存量坏 policy → GET **403** | 通过 |

**文件尺寸预算（硬门禁 < 500 行/文件）**：`policy.go` 346 → ≈375（+validatePrincipal ~10、ParsePolicy 循环 +6、EvalResource 重构净 +12）；`policy_test.go` 191 → ≈320。均余量充足。

---

## 7. 实现（最终代码形态）

### 7.1 `internal/auth/policy.go`

**ParsePolicy 校验循环**（替换 :57-60 循环体）：

```go
	for index := range p.Statement {
		stmt := &p.Statement[index]
		if err := stmt.validateConditions(); err != nil {
			return nil, fmt.Errorf("statement %d: %w", index, err)
		}
		if stmt.Effect != "Allow" && stmt.Effect != "Deny" {
			return nil, fmt.Errorf("statement %d: invalid Effect %q (must be \"Allow\" or \"Deny\")", index, stmt.Effect)
		}
		if err := stmt.validatePrincipal(); err != nil {
			return nil, fmt.Errorf("statement %d: %w", index, err)
		}
	}
```

**新增 `validatePrincipal`**（放在 `validateConditions` 旁；依赖既有 `principalValues` :185-201）：

```go
// validatePrincipal rejects any Principal value other than "*" or its
// equivalent forms ({"AWS":"*"}, {"AWS":["*"]}). An absent Principal is
// preserved as wildcard (matchesPrincipal returns true for empty maps).
func (s *Statement) validatePrincipal() error {
	for _, v := range s.Principal {
		for _, candidate := range principalValues(v) {
			if candidate != "*" {
				return fmt.Errorf("unsupported Principal %q: only \"*\" is supported", candidate)
			}
		}
	}
	return nil
}
```

**`EvalResource` 求值循环重构**（替换 :80-110 循环体；D2 证明对合法输入逐类行为保持）：

```go
	allow := false
	for _, stmt := range p.Statement {
		if !stmt.matchesAction(canonAction) || !stmt.matchesResource(resource) {
			continue
		}
		switch stmt.Effect {
		case "Deny":
			// FR-3: a Deny that cannot be evaluated must deny, never be skipped.
			if !stmt.matchesPrincipal() || stmt.validateConditions() != nil {
				return EffectDeny
			}
			if stmt.matchesConditions(sourceIP) {
				return EffectDeny
			}
		case "Allow":
			if stmt.matchesPrincipal() && stmt.matchesConditions(sourceIP) {
				allow = true
			}
		default:
			// FR-2: unknown Effect fails closed, never silently skipped.
			return EffectDeny
		}
	}
```

> 注意第 6 输入类（Deny + IP 范围未命中）走 `matchesConditions(sourceIP)==false` → 正常跳过，**不得**落入"不可求值"分支——`validateConditions` 只查算子/键/值形态，不查命中。

### 7.2 `internal/auth/policy_test.go` 新增测试

按 §6 表落地：AC-1..AC-4 四组测试原文取自规格 §4（已实跑验证判定性），另加分支 A 表驱动与直构守卫测试（§6 第 5、6 行）。全部沿用既有表驱动风格（policy_test.go:180-197 先例）与 `Policy`/`Statement` 直构（字段全导出，规格 §4 已确认）。

### 7.3 `internal/api/s3compat/policy_test.go`（可选 adapter 证明）

```go
func TestPutBucketPolicyRejectsInvalidEffect(t *testing.T) {
	s := newTestServer(t)
	base := s.URL + "/bucket"
	resp, _ := do(t, http.MethodPut, base+"/init.txt", []byte("init"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("init status = %d", resp.StatusCode)
	}
	resp, _ = do(t, http.MethodPut, base+"/?policy",
		[]byte(`{"Statement":[{"Effect":"deny","Action":"s3:*"}]}`), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid Effect write status = %d, want 400", resp.StatusCode)
	}
	resp, _ = do(t, http.MethodGet, base+"/init.txt", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rejected policy must not persist, GET status = %d", resp.StatusCode)
	}
}
```

### 7.4 验证序列

```
gofmt -l internal/auth/ internal/api/s3compat/   # 无输出
go build ./...
go vet ./...
go test ./internal/auth/ ./internal/api/s3compat/   # 新增 + 既有全绿
make check
```

---

## 8. 范围守卫（与规格 §5 一致，不扩展）

不做：Effect trim/归一化；`EvalResource` 签名变更 / 请求方 principal 上下文；新条件算子接入（`condition.go` 集合保持独立）；存量坏 policy 扫描工具化（迁移为操作清单，§5）；错误码/HTTP 状态变更；`matchesAction`/`matchesResource`/`wildcardMatch`/`StringOrArray` 行为。分支 B（求值期唯一防线、解析放行）为文档化备选，未采用（D3）。
