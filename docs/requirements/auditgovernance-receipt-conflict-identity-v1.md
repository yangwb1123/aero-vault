# 方向：validateReceipt 须先验证收据身份再认可 conflict:true —— 身份不匹配的收据把错误事实终态失败（静默审计事实丢失）

> **模块：** `internal/auditgovernance` · **来源分析：** `docs/auto/analyses/internal-auditgovernance-ef1a62fa.json`（方向 #1） · **日期：** 2026-08-07
> **评分：** 价值 9 / 风险降低 8 / 工作量 2 / 置信度 9
> **验证基准：** 工作树 = HEAD `acfaaf4`（2026-08-06）。本文所有引用均已对照该基准逐行验证；行号漂移已在 §2 修正表标注。
>
> **范围纪律：** 本规格只含方向所限的**一个条件重排** + 其测试钉扎。不改 wire 形状、不改 relay/存储/配置、不新增依赖、不动 `openapi.json`（无 API 表面变化）。

---

## 1. 问题陈述

`validateReceipt`（`internal/auditgovernance/http.go:196`）在验证收据身份**之前**就认可 `Conflict` 标志：`Conflict=true` 直接返回终端哨兵 `ErrReceiptConflict`（:201），`receiptMatches`（:203 调用 / :214 定义）的身份校验（event_id / tenant_id / accepted_at / status）只保护成功路径。

**后果链（全部已逐行验证）：**

1. **错误分支优先级：** `relay.go:84` 仅对 `ErrReceiptConflict` 走 `failFact`（:90 调用 / :111 定义）——终态、永不重试、保留至 retention 修剪；其余错误（含 `ErrInvalidReceipt`）走 `retryFact`（:94-96 / :124 定义，有界退避+抖动）。
2. **身份不匹配的 conflict 收据被当作"真 conflict"：** 接收端 bug、代理错路由、多租户聚合错误都可能对**非本次 POST 的事件**返回 `{conflict:true}`。此类收据今天被终态失败——`failFact` 置 failed 态（不再 re-claim、不再 re-POST，`runtime_test.go:117` 测试钉死该语义），`CleanupFailedAuditGovernance` 在 retention 窗口（默认 **7d**，`config_audit_governance.go:68` `AUDIT_GOVERNANCE_DELIVERED_RETENTION_SECONDS=604800`）后剪除该行。
3. **结果：** 一条合规审计事实**永久丢失**，而它在真实账本上的结果从未被获知。成功路径要求完整身份匹配（`receiptMatches`），conflict 是唯一未校验分支——修复 = 一个条件重排，把永久丢失场景转换为可重试的 `ErrInvalidReceipt`（有界退避重试，真相最终可被获知）。

**触发场景（真实工作流）：** 多租户网关在 tenant 路由哈希抖动时把 tenant B 的既有收据（`event_id` 属 B）误回给 tenant A 的事实 POST → A 的事实被终态失败并在 7d 后无声消失；合规审计追溯时该事实缺失且无任何告警。

---

## 2. 现状与代码证据（已逐条验证）

### 2.1 方向文证据验证表

| # | 方向文引用 | 验证结果 |
|---|-----------|---------|
| E1 | `http.go:196` — `Conflict` 在身份校验前被检查 | ✅ 精确：`if envelope.Receipt.Conflict {`（:196）→ `return ErrReceiptConflict`（:201）；`if !receiptMatches(...)`（:203）→ `return ErrInvalidReceipt`（:204）。**Bug 本体** |
| E2 | `http.go:214` — `receiptMatches` 验证 event_id/tenant_id/accepted_at 身份 | ✅ 精确：`func receiptMatches`（:214）；谓词要求 `EventID==fact.ID && TenantID==fact.TenantID && !AcceptedAt.IsZero()` + status ∈ {ledgered, indexed, archived}（:216-221）。`Duplicate` 有意缺席（contract A，:210-213 注释） |
| E3 | `relay.go:111` — `failFact` 终态失败 | ✅ 精确：`func (r *Runtime) failFact`（:111）；分发在 :84-92（`errors.Is(err, ErrReceiptConflict)` → `failFact`，注释 "terminal-with-retention"）；非 conflict 错误 → `retryFact`（:94-96，:124 定义，`boundedBackoff` :163）。**"ErrInvalidReceipt 可重试" 的代码依据** |
| E4 | `model.go:31` — 收据模型/哨兵 | ⚠️ **行号漂移（符号成立）：** :31 是哨兵 `var` 块收尾 `)`；实际符号为 `ErrReceiptConflict` 哨兵（:29）与 `Conflict bool json:"conflict"` 字段 + 契约注释（:52-56，含 "the relay fails the fact (ErrReceiptConflict), never retries it, and keeps the row until the retention prune"）。`Duplicate` 字段契约在 :47-50 |
| E5 | `http_test.go:TestReceiptConflictIsTerminalSentinel` | ✅ 存在（:262）；夹具 = **身份完全匹配**（event_id "fact-1" / tenant_id "acme" / status "ledgered" / accepted_at 非零）+ conflict:true → 断言 `ErrReceiptConflict` 且**非** `ErrInvalidReceipt`（:287-290）。**该测试夹具钉死"conflict 收据携带完整身份"的契约假设（见 D1）** |

### 2.2 新增证据（超出方向文引用，塑造了 AC）

| # | 证据 | 内容 | 影响 |
|---|------|------|-----|
| N1 | `http_test.go:204` `TestReceiptDuplicateSemanticsContract` | ① 谓词级：mismatched `event_id` 被 `receiptMatches` 拒绝（:228-232）；② 端到端：`{duplicate:true, conflict:false}` 完成如首 POST | 重排后两分支必须原样通过（AC-2）；谓词本身不动 |
| N2 | `runtime_test.go:117` `TestRuntimeConflictingReceiptIsTerminalWithRetention` | relay 级终端语义：恰 1 次 POST、不再 re-claim、`OldestPending` 为空、retention 前后 `CleanupFailedAuditGovernance` 0/1 行。⚠️ **夹具缺陷（R1，设计期修正）**：服务端硬编码 `event_id:"x"`，而 `facts.go:15` `factFromAudit` 生成 `ID: uuid.NewString()` 随机 UUID —— 重排后该收据身份必不匹配 → 被判 `ErrInvalidReceipt` 重试，`OldestPending ok==false` 断言确定性失败。**夹具须改为回显请求体 event_id（断言逐字不变）** | 提供"终态 vs 可重试"的可观察判别式；其夹具（`runtimeConfig` :39-50，backoff 1s/2s）可直接复用于重试侧测试（AC-4） |
| N3 | `relay.go:84-96` 分发 | 只有 `ErrReceiptConflict` 终态；其余全部有界退避重试 | "重排把永久丢失转换为可重试"的代码级证明；`ErrInvalidReceipt` 可重试性 = 两个哨兵的语义区分 |
| N4 | `config_audit_governance.go:68` | 默认 retention 604800s = **7d**；:245-246 校验 1h..365d | "7d 后剪除"量化成立 |

### 2.3 缺口分析

| # | 缺口 | 现状证据 | 后果 |
|---|------|---------|------|
| G1 | **conflict 是唯一未校验身份的分支** | `http.go:196-204`：成功路径过 `receiptMatches`，conflict 分支提前返回 | 身份不匹配的 `{conflict:true}` 被当作真 conflict → 终态失败 |
| G2 | **终态失败 = 不可逆丢失** | `relay.go:84-90`（仅 conflict 终态）、:111-123（`failFact` 永不 re-claim）；`CleanupFailedAuditGovernance` 7d 剪除（N4） | 错误事实永久丢失且无告警（`failFact` 仅记 `logger.Error`，无重试、无恢复路径） |
| G3 | **重排的落点无测试** | 全仓无"conflict + 身份不匹配"用例；`TestReceiptConflictIsTerminalSentinel` 只覆盖匹配身份 | 修复本身需要新测试钉扎（AC-1），且"重试而非终态"需 relay 级判别（AC-4） |

---

## 3. 需求规格

### FR-1：`validateReceipt` 先验身份、后验 conflict（单条件重排）

`internal/auditgovernance/http.go` 的 `validateReceipt` 内，将两个分支**对调**：

- **先：** `if !receiptMatches(envelope, fact) { return ErrInvalidReceipt }`（:203-205 原样上移）——event_id/tenant_id 不匹配、accepted_at 为零、或 status ∉ {ledgered, indexed, archived}，一律 `ErrInvalidReceipt`（**可重试**，与 conflict 标志无关）；
- **后：** `if envelope.Receipt.Conflict { return ErrReceiptConflict }`（:196-201 原样下移）——**身份匹配后** conflict 才终态（`ErrReceiptConflict`，relay 侧语义零变化）；
- **行为面不变：** 合法收据（身份匹配 + 合法 status + conflict:false，Duplicate 惰性）→ `nil`；`duplicate:true` 的幂等重 POST 仍按首 POST 完成（contract A）。
- **注释同步：** conflict 分支注释（:197-200）改写为明确记录**顺序契约**——"conflict 仅在收据身份（event_id/tenant_id/accepted_at/status）与所 POST 事实匹配后认可；身份不匹配的 conflict 视为无效收据（可重试），防错路由收据终态杀死错误事实"；`receiptMatches` 的 doc 注释（:209-213）补一句"该谓词同样门控 conflict 分支"。
- **不改** `receiptMatches` 谓词、哨兵、wire 形状、`model.go`、`relay.go`、repository、config。

### FR-2：契约不变量（identity-matched conflict 保持终态；duplicate 保持惰性）

- `TestReceiptConflictIsTerminalSentinel`（http_test.go:262）**逐字通过**：身份匹配 + conflict:true → `ErrReceiptConflict`（且非 `ErrInvalidReceipt`）——重排不改变"真 conflict 终态"语义。
- `TestReceiptDuplicateSemanticsContract`（http_test.go:204）**逐字通过**：Duplicate 谓词惰性、mismatched event_id 谓词级拒绝、`{duplicate:true, conflict:false}` 端到端完成。
- `TestRuntimeConflictingReceiptIsTerminalWithRetention`（runtime_test.go:117）**断言逐字通过（R1 夹具修正）**：relay 级"匹配身份 conflict → 恰 1 次 POST、终态、retention 修剪"不变。⚠️ 该测试夹具目前硬编码 `event_id:"x"`，而 `factFromAudit` 生成随机 UUID（`facts.go:15`），重排后必然被拒为 `ErrInvalidReceipt` 走重试路径、`OldestPending ok==false` 断言失败 —— 夹具必须改为**回显请求体 event_id**（复用 `TestRuntimeRelaysAtomicAdminAndFileFactsAndDrains` 的既有解码回显模式），使"匹配身份 conflict"真正满足 D1 契约假设；全部断言（恰 1 次 POST / 不再 re-claim / 非 pending / retention 0→1 行）逐字保留。
- 两个哨兵语义（`ErrReceiptConflict` = 终态-with-retention；`ErrInvalidReceipt` = 可重试）不变——`relay.go:84-96` 分发零改动。

### FR-3：新测试钉扎（重排的可执行证据）

- **单元级（http_test.go 新增）：** 202 收据 `{conflict:true}` + 身份不匹配（event_id 不匹配 / tenant_id 不匹配 / 两者皆不匹配，表驱动）→ 断言 `errors.Is(err, ErrInvalidReceipt)` 且**非** `ErrReceiptConflict`。同时保留一个对照用例：身份匹配 + conflict:true → `ErrReceiptConflict`（防重排过度）。
- **relay 级（runtime_test.go 新增，复用 :117 夹具形状）：** 服务端对**身份不匹配**事实返回 `{conflict:true, event_id:"other"}` → 断言该事实走**重试**路径而非终态：跨退避窗口发生 ≥2 次 re-POST（对照 :117 的"恰 1 次"）、事实保持可 claim/pending、`CleanupFailedAuditGovernance`（未来 cutoff）为 0 行——把方向文"永久丢失 → 可重试"的论断在状态机层面可观察化。

---

## 4. 验收标准（可测试；逐条映射方向 acceptance）

### AC-1（映射方向 acceptance ①）单元：conflict + 身份不匹配 → `ErrInvalidReceipt`

`internal/auditgovernance/http_test.go` 新增（命名建议 `TestReceiptConflictMismatchedIdentityRejected`，镜像 `TestReceiptConflictIsTerminalSentinel` 的 httptest + `publisher.Publish` 形状）：

```go
func TestReceiptConflictMismatchedIdentityRejected(t *testing.T) {
	fact := repository.AuditGovernanceFact{ID: "fact-1", TenantID: "acme", FactKind: "admin",
		Action: "tenant.status", OccurredAt: time.Now().UTC()}
	for _, tc := range []struct{ name, eventID, tenantID string }{
		{"event_id mismatch", "other-fact", "acme"},
		{"tenant_id mismatch", "fact-1", "other-tenant"},
		{"both mismatch", "other-fact", "other-tenant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// httptest：/token 返回 token；/api/v1/events 返回 202 +
			// {"receipt":{"event_id":tc.eventID,"tenant_id":tc.tenantID,
			//   "status":"ledgered","accepted_at":"2026-08-04T00:00:00Z","conflict":true}}
			// （复用 publisherConfig(server.URL) / noRedirectClient(time.Second)）
			err := publisher.Publish(context.Background(), fact)
			if !errors.Is(err, ErrInvalidReceipt) {
				t.Fatalf("mismatched conflict receipt err=%v want ErrInvalidReceipt (retryable)", err)
			}
			if errors.Is(err, ErrReceiptConflict) {
				t.Fatal("mismatched conflict classified terminal — wrong fact would be lost")
			}
		})
	}
	// 对照：身份匹配 + conflict:true 仍终态（防重排过度）——由 AC-2 的既有
	// TestReceiptConflictIsTerminalSentinel 钉死，此处不重复。
}
```

> 可重试性的代码依据（非断言框架即可证明）：`relay.go:84-96` 中唯一终态分支是 `errors.Is(err, ErrReceiptConflict)`，`ErrInvalidReceipt` 必然落入 `retryFact`（有界退避）；AC-4 在状态机层面直接观测该路径。

### AC-2（映射方向 acceptance ②）既有契约测试原样通过

```bash
go test ./internal/auditgovernance/ -run 'TestReceiptConflictIsTerminalSentinel|TestReceiptDuplicateSemanticsContract' -v
# 两个测试全部 PASS，断言不变（identity-matched conflict 保持终态；
# duplicate 语义、谓词级身份拒绝保持原样）
```

### AC-3（映射方向 acceptance ③）全量门禁

```bash
go vet ./... && go test ./internal/auditgovernance/...
# 全绿；http.go 改动后仍 ≤500 行（现状 221 行）；无新依赖（stdlib only，I6）；
# 无 API/配置/存储面变化（openapi.json、config、migrations 零改动）
```

### AC-4 relay 级：不匹配 conflict 收据走重试而非终态（复用 runtime_test.go:117 夹具）

`internal/auditgovernance/runtime_test.go` 新增（命名建议 `TestRuntimeMismatchedConflictReceiptIsRetriedNotTerminal`）：

```go
func TestRuntimeMismatchedConflictReceiptIsRetriedNotTerminal(t *testing.T) {
	// 服务端：/token 正常；/api/v1/events 恒返回 202 +
	// {"receipt":{"event_id":"other-fact","tenant_id":"acme","status":"ledgered",
	//   "accepted_at":"2026-08-04T00:00:00Z","conflict":true}}  // 身份不匹配！
	// 记录 fact "fact-1"（WrapRepository + RecordAudit，同 :117 夹具）；
	// runtimeConfig backoff = 1s/2s（:39-50）。
	// 1) 等待首 POST（≤3s）；
	// 2) 再等待 ≥2×初始退避（约 3-4s）→ posts.Load() >= 2
	//    （对照 :117 终态断言 posts==1——重试路径的判别式）；
	// 3) runtime.Close() 后 ClaimAuditGovernance(...) 仍能 claim 到该行、
	//    OldestPendingAuditGovernance ok==true（事实仍 pending，非终态）；
	// 4) CleanupFailedAuditGovernance(ctx, now+1h, 10) == 0
	//    （无终态失败行可剪——"永久丢失"未发生）。
}
```

---

## 5. 范围纪律与契约假设

- **改动面（唯二）：** `internal/auditgovernance/http.go` 的 `validateReceipt` 分支重排 + 注释；`http_test.go` / `runtime_test.go` 新增测试（含 **R1 修正**：`TestRuntimeConflictingReceiptIsTerminalWithRetention` 夹具改为回显请求体 event_id —— 设计期发现其硬编码 `event_id:"x"` 与 `factFromAudit` 的随机 UUID 永不匹配，重排后断言将确定性失败；断言集逐字不变，详见设计文 §1 R1）。**不改** `receiptMatches` 谓词、`model.go`（哨兵/字段/契约注释）、`relay.go`、repository、config、迁移、`openapi.json`。
- **D1 契约假设（实现约束，非改动项）：** conflict 收据携带**完整身份**（event_id/tenant_id/accepted_at + 合法 status）——由既有夹具 `TestReceiptConflictIsTerminalSentinel` 钉死。若未来接收端对真 conflict 省略 accepted_at，重排后会被判 `ErrInvalidReceipt`（可重试）而非终态；这是**保守方向**（宁重试不丢失），且需接收端契约确认——**不在本方向范围内**，仅记录。
- **I5 不受影响：** 纯内部校验顺序变化，无新 flag、无新依赖；CI 基线（SQLite + local FS）零接触。
- **审计事实丢失的缓解闭环：** 重排后最坏场景 = 错路由收据导致有界退避重试（事实保留在 outbox，7d 内可观测、可恢复），而非 7d 后无声剪除。
