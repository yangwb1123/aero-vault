# 设计：`validateReceipt` 先验身份、后验 conflict —— 身份不匹配的 conflict 收据可重试而非终态丢失

> **配套规格：** `docs/requirements/auditgovernance-receipt-conflict-identity-v1.md`（FR-1…FR-3 / AC-1…AC-4）· **模块：** `internal/auditgovernance`（`http.go` + 两个测试文件）· **状态：** 设计（未实现）· **基线：** HEAD `acfaaf4` + 工作树未提交批量（Contract A 已落树：`ErrReceiptConflict`/`failFact`/`TestReceiptConflictIsTerminalSentinel` 等）
> **门禁：** `make check` 全绿（gofmt/build/vet/test）· 单文件 ≤ 500 行 · 纯 stdlib（I6）· 无新 `go.mod` 依赖 · 无 wire/config/DB schema/`openapi.json` 变更

---

## 1. 证据复核（独立复验，逐行对照工作树）

| # | 规格主张 | 复核结论 |
|---|---------|---------|
| E1 | `http.go:196` conflict 先于身份校验（bug 本体） | ✅ 精确：`if envelope.Receipt.Conflict {`（:196）→ `ErrReceiptConflict`（:201）；`if !receiptMatches(...)`（:203）→ `ErrInvalidReceipt`（:204）；conflict 分支注释 :197-200 |
| E2 | `http.go:214` `receiptMatches` 谓词 | ✅ 精确：`EventID==fact.ID && TenantID==fact.TenantID && !AcceptedAt.IsZero()` + status ∈ {ledgered,indexed,archived}（:216-221）；`Duplicate` 有意缺席（:209-213 注释） |
| E3 | `relay.go:84-96` 仅 `ErrReceiptConflict` 终态 | ✅ 精确：`deliverFact` :84-92 `errors.Is(err, ErrReceiptConflict)` → `failFact`（:111 定义）；其余 → `retryFact`（:124 定义，`boundedBackoff` :163）。`ErrInvalidReceipt` 可重试的代码依据成立 |
| E4 | `model.go` 哨兵 + `Conflict` 字段契约 | ✅ 符号成立（规格已修正行号漂移）：`ErrReceiptConflict` :29；`Conflict` 字段 + "never retries it … retention prune" 契约注释 :52-56 |
| E5 | `http_test.go:262` `TestReceiptConflictIsTerminalSentinel` | ✅ 夹具 `fact.ID="fact-1"` 与收据 `event_id:"fact-1"` **显式一致** → 重排后仍 `ErrReceiptConflict`（身份先过、conflict 后判）——该测试**逐字通过**（AC-2 成立） |
| N1 | `TestReceiptDuplicateSemanticsContract`（http_test.go:204） | ✅ 谓词级（`fact-1`/`acme` 匹配）+ 端到端 `{duplicate:true, conflict:false}`；重排后两分支原样通过 |
| N2 | `TestRuntimeConflictingReceiptIsTerminalWithRetention`（runtime_test.go:117） | ⚠️ **规格 FR-2 "逐字通过" 主张不成立 —— 见 R1** |
| N3 | `runtimeConfig`（runtime_test.go:39-50）backoff 1s/2s、poll 10ms | ✅ 精确；revision=1、claimTTL=3s、retention=3600s |
| N4 | `config_audit_governance.go` 默认值 | ✅ `INITIAL_BACKOFF_SECONDS=1`（:64）、`MAX_BACKOFF_SECONDS=300`（:65）、`DELIVERED_RETENTION_SECONDS=604800`（7d，:68） |

### R1 ⚠️ 规格缺陷（设计期修正，必须落进实现）

**`TestRuntimeConflictingReceiptIsTerminalWithRetention`（runtime_test.go:117）的夹具在重排后必然失败，规格 FR-2 "逐字通过" 不成立。**

证据链（逐行验证）：

1. `facts.go:15` `factFromAudit` 生成 `ID: uuid.NewString()` —— 随机 UUID；
2. runtime_test.go:132 终端测试的服务端**硬编码** `event_id:"x"` 回包；
3. `receiptMatches`（http.go:216）要求 `receipt.EventID == fact.ID` —— `"x"` 与随机 UUID 永不相等；
4. 重排后该收据 → `ErrInvalidReceipt` → `retryFact`（有界退避）→ 行保持 pending；
5. 终端测试 :141-144 `posts==1` 在 500ms 窗内恰好仍成立（首退避 ≥750ms，抖动 ±25%），但 :150-153 `OldestPendingAuditGovernance ok==false` 断言**确定性失败**（行未 failed，仍 pending）。

**修正（R1，测试夹具改动，断言逐字不变）：** 服务端改为**回显请求体中的 event_id**（复用 `TestRuntimeRelaysAtomicAdminAndFileFactsAndDrains` :62-69 的既有模式：`jsonNewDecoder(r).Decode(&body)` + `fmt.Fprintf` 回显），使"身份匹配的 conflict"真正满足 Contract A 的 D1 假设（conflict 收据携带完整身份）。测试名、全部断言（恰 1 次 POST / 不再 re-claim / 非 pending / retention 0→1 行）**逐字保留**——该测试钉的语义（身份匹配 conflict → 终态-with-retention）在重排后不变，只是夹具从"碰巧不匹配"修正为"真正匹配"。

> 规格 §2.2 N2 行与 §3 FR-2 相应更新（见 §8 规格同步清单）。

---

## 2. 变更设计（唯一代码改动：`validateReceipt` 单条件重排）

### 2.1 `internal/auditgovernance/http.go` — `validateReceipt` 最终形态

现状（:196-206）：

```go
	if envelope.Receipt.Conflict {
		// Contract A: conflict is terminal — the receiver will never ledger
		// this event, so retrying cannot succeed. Distinct sentinel so the
		// relay fails the fact with retention instead of bounded-backoff
		// retrying it forever.
		return ErrReceiptConflict
	}
	if !receiptMatches(envelope, fact) {
		return ErrInvalidReceipt
	}
	return nil
```

改后（身份先、conflict 后；分支体与哨兵零改动）：

```go
	if !receiptMatches(envelope, fact) {
		// Identity first, conflict second (ordering contract): a conflict
		// receipt that does not correspond to the posted fact — receiver bug,
		// misrouted proxy response, multi-tenant aggregation error — is an
		// invalid (retryable) receipt, never a terminal conflict. Honoring
		// conflict before identity would terminally fail the wrong fact
		// (silent audit-fact loss). Pinned by
		// TestReceiptConflictMismatchedIdentityRejected.
		return ErrInvalidReceipt
	}
	if envelope.Receipt.Conflict {
		// Contract A: conflict is terminal — the receiver will never ledger
		// this event, so retrying cannot succeed. Distinct sentinel so the
		// relay fails the fact with retention instead of bounded-backoff
		// retrying it forever. Honored only after the receipt identity
		// matches the posted fact (ordering contract above).
		return ErrReceiptConflict
	}
	return nil
```

### 2.2 `receiptMatches` doc 注释（:209-213）补一句

在既有 "Duplicate is intentionally absent from this predicate (contract A): …" 段落后追加：

```go
// This predicate also gates the conflict branch: {conflict:true} is honored
// only after identity matches (validateReceipt ordering contract); a
// conflict receipt with mismatched identity is an invalid, retryable receipt.
```

**谓词体、哨兵、`model.go`、`relay.go`、repository、config 一律不动。**

### 2.3 行为面（重排前后对照）

| 收据形态（202 + JSON） | 现状（conflict 先） | 改后（身份先） | 变化 |
|---|---|---|---|
| 身份匹配 + conflict:true | `ErrReceiptConflict`（终态） | `ErrReceiptConflict`（终态） | 无 |
| 身份不匹配 + conflict:true | `ErrReceiptConflict`（**终态杀错事实**） | `ErrInvalidReceipt`（**有界退避重试**） | **修复点** |
| 身份匹配 + conflict:false | `nil`（完成） | `nil`（完成） | 无 |
| `duplicate:true` 幂等重 POST | `nil`（完成） | `nil`（完成） | 无 |
| 非 202 / 非 JSON / 超限 / 畸形 | `ErrInvalidReceipt`/`httpStatusError` | 同 | 无 |

---

## 3. API 变更

**外部：零。** `validateReceipt`/`receiptMatches` 均为包内未导出函数；无 wire 形状变化（请求 `governanceEvent`、收据 JSON 字段不变）；无 HTTP/CLI/OpenAPI/配置/迁移变化。哨兵集合（`ErrInvalidReceipt`/`ErrReceiptConflict`/…）不变。全仓 grep 证实 `ErrReceiptConflict`/`ErrInvalidReceipt` 仅 `internal/auditgovernance` 内部消费（无跨包依赖）。

**内部：** `http.go` 约 +4 行注释（221 → ~225）；`http_test.go` 297 → ~347（新增 AC-1 测试）；`runtime_test.go` 400 → ~468（R1 夹具 + 新增 AC-4 测试）。均 < 500 行硬门禁（runtime_test.go 预留 ~30 行余量，实现时保持紧凑）。

---

## 4. 兼容性约束

- **合规接收端零感知：** 真 conflict 收据携带完整身份（D1，被 `TestReceiptConflictIsTerminalSentinel` 夹具钉死）→ 行为逐位不变；幂等重 POST（`duplicate:true, conflict:false`）不变；错误分支（非 202/非 JSON/超限）不变。
- **唯一行为差异 = 修复角落：** 身份不匹配的 `{conflict:true}` 从终态失败变为可重试。旧行为是缺陷（静默丢失），新行为是保守方向（宁重试不丢）。
- **D1 契约假设（已记录，非本方向改动项）：** 若未来接收端对真 conflict 省略 `accepted_at`，重排后判 `ErrInvalidReceipt`（可重试）而非终态 —— 保守方向，需接收端契约确认；AC-1 表新增 `accepted_at missing` 行钉住该行为，使保守偏置可观测。
- **跨版本混跑安全：** 新旧 relay 行为差异仅限上述角落，无跨版本 DB/线协议交互，滚动升级/回滚均安全（见 §6）。

---

## 5. 失败模式

| # | 模式 | 改后行为 | 可观测性 | 处置 |
|---|------|---------|---------|------|
| F1 | **修复目标**：接收端 bug / 代理错路由 / 多租户聚合错误返回不匹配 conflict | 有界退避重试（1s→300s 默认，抖动 ±25%，`boundedBackoff`），事实保留在 outbox，真相最终可获知 | `audit_governance_outbox.last_error='audit governance receipt is invalid'`，attempts 递增；`/readyz` 在 backlog > maxLag 时报警 | 修复点本身 |
| F2 | **新引入（D1 面）**：真 conflict 但缺 `accepted_at`/空 tenant_id 的收据 | 有界退避重试（非终态） | 同 F1 | 接受的保守偏置；接收端契约修复；AC-1 第 4 行钉住 |
| F3 | **回归**：身份匹配 conflict 不再终态 | 不可能 —— 单元级（`TestReceiptConflictIsTerminalSentinel`）+ relay 级（R1 修正后的 `TestRuntimeConflictingReceiptIsTerminalWithRetention`）双钉 | 两个既有测试 | AC-2 |
| F4 | **回归**：duplicate 惰性被破坏 | 不可能 —— `TestReceiptDuplicateSemanticsContract` 钉住 | 既有测试 | AC-2 |
| F5 | 修复前已 failed 的行不复活 | gap 扫描 `ListAuditGovernanceGaps` 的 SQL 以 `o.id IS NULL AND d.origin_id IS NULL` 判定缺口 —— **任何 outbox 行（含 failed）都抑制缺口**（`audit_governance_write.go:218-223/:247-252` 实测）；failed 行保留至 7d retention 修剪 | `CleanupFailedAuditGovernance` 7d 剪除 | 不回填、不复活（与 events outbox"无自动补投"同哲学）；运维可手工捞库 —— 记录，非本方向范围 |
| F6 | 持续作恶接收端 → 重试风暴 | 有界退避封顶 300s；治理 relay **无 attempts 上限**（`RetryAuditGovernance` 无 max-attempts 分支，与 events outbox 不同）→ 永不静默剪除、永不终态 | `last_error` 恒定 + attempts 增长 | 有界、可观测；接收端修复是唯一正解 |

---

## 6. 迁移步骤

**零迁移。** 无 schema/wire/config 变更；部署 = 纯代码替换（单函数重排 + 测试）。滚动升级无顺序要求（relay 侧纯客户端行为）。回滚 = revert 该函数重排即可，无数据面残留。

部署后运维验证（可选 SQL）：

```sql
-- 修复生效证据：身份不匹配 conflict 的 retry 行（改前会以 failed_at_ns 终态出现）
SELECT id, attempts, last_error FROM audit_governance_outbox
WHERE delivered_at_ns=0 AND failed_at_ns=0 AND last_error='audit governance receipt is invalid';
-- 历史已 failed 行（改前产生）仍在 retention 窗内，7d 后由 CleanupFailedAuditGovernance 剪除
SELECT count(*) FROM audit_governance_outbox WHERE failed_at_ns>0;
```

---

## 7. 验收映射（AC → 测试 → 断言锚点 → 门禁）

| AC | 测试（文件） | 断言锚点 | 门禁 |
|----|------------|---------|------|
| AC-1 | `TestReceiptConflictMismatchedIdentityRejected`（http_test.go 新增，表驱动 4 行） | `errors.Is(err, ErrInvalidReceipt)` ∧ `!errors.Is(err, ErrReceiptConflict)`；行：event_id 不匹配 / tenant_id 不匹配 / 双不匹配 / accepted_at 缺失（钉 D1）。对照用例不重复 —— 由 AC-2 既有测试钉"匹配身份 conflict 仍终态" | `go test ./internal/auditgovernance/ -run TestReceiptConflictMismatchedIdentityRejected -v` |
| AC-2 | 既有 `TestReceiptConflictIsTerminalSentinel` + `TestReceiptDuplicateSemanticsContract`（http_test.go，断言零改动）；R1 修正后的 `TestRuntimeConflictingReceiptIsTerminalWithRetention`（断言零改动，夹具回显 event_id） | 终态哨兵 / duplicate 惰性 / relay 级"恰 1 次 POST + 不再 re-claim + retention 0→1 行" | `go test ./internal/auditgovernance/ -run 'TestReceiptConflictIsTerminalSentinel\|TestReceiptDuplicateSemanticsContract\|TestRuntimeConflictingReceiptIsTerminalWithRetention' -v` |
| AC-3 | 全量门禁 | `gofmt -l` 空 · `go build ./...` · `go vet ./...` · `go test ./...`（`make check`）；文件 ≤500 行（§3 预算） | `make check` |
| AC-4 | `TestRuntimeMismatchedConflictReceiptIsRetriedNotTerminal`（runtime_test.go 新增） | ① 等待 ≤4s `posts.Load() >= 2`（对照终态测试的 `==1`）—— 重试路径判别式；② `Close()` 后 `OldestPendingAuditGovernance ok==true`（行仍 pending，created_at 基，不受 available_at 未来值影响 —— `audit_governance_claim.go:188-199` 实测）；③ 轮询 claim（deadline 5s，observer/revision=1/`time.Minute`）最终可取回（retry 写 `available_at=now+backoff`，`RetryAuditGovernance` :137-150 实测）；④ `CleanupFailedAuditGovernance(ctx, now+1h, 10)==0`（无终态行） | `go test ./internal/auditgovernance/ -run TestRuntimeMismatchedConflictReceiptIsRetriedNotTerminal -v` |

### AC-1 测试代码（最终形态）

```go
// Reorder contract: identity is verified before the conflict flag. A
// {conflict:true} receipt that does not match the posted fact (receiver bug,
// misrouted proxy, multi-tenant aggregation error) must surface as the
// retryable ErrInvalidReceipt — never the terminal ErrReceiptConflict, which
// would permanently fail the wrong fact (silent audit-fact loss).
func TestReceiptConflictMismatchedIdentityRejected(t *testing.T) {
	fact := repository.AuditGovernanceFact{ID: "fact-1", TenantID: "acme", FactKind: "admin",
		Action: "tenant.status", OccurredAt: time.Now().UTC()}
	for _, tc := range []struct{ name, body string }{
		{"event_id mismatch", `{"receipt":{"event_id":"other-fact","tenant_id":"acme","status":"ledgered","accepted_at":"2026-08-04T00:00:00Z","conflict":true}}`},
		{"tenant_id mismatch", `{"receipt":{"event_id":"fact-1","tenant_id":"other-tenant","status":"ledgered","accepted_at":"2026-08-04T00:00:00Z","conflict":true}}`},
		{"both mismatch", `{"receipt":{"event_id":"other-fact","tenant_id":"other-tenant","status":"ledgered","accepted_at":"2026-08-04T00:00:00Z","conflict":true}}`},
		{"accepted_at missing", `{"receipt":{"event_id":"fact-1","tenant_id":"acme","status":"ledgered","conflict":true}}`}, // D1: conservative retry-not-lose
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"access_token":"token","token_type":"Bearer","expires_in":60}`))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, transport := noRedirectClient(time.Second)
			defer transport.CloseIdleConnections()
			publisher, err := newPublisher(publisherConfig(server.URL), client)
			if err != nil {
				t.Fatal(err)
			}
			err = publisher.Publish(context.Background(), fact)
			if !errors.Is(err, ErrInvalidReceipt) {
				t.Fatalf("mismatched conflict receipt err=%v want ErrInvalidReceipt (retryable)", err)
			}
			if errors.Is(err, ErrReceiptConflict) {
				t.Fatal("mismatched conflict classified terminal — wrong fact would be lost")
			}
		})
	}
}
```

### AC-4 测试代码（最终形态，复用 `runtimeConfig` 与既有 DB 夹具形状）

```go
func TestRuntimeMismatchedConflictReceiptIsRetriedNotTerminal(t *testing.T) {
	// Discriminator vs TestRuntimeConflictingReceiptIsTerminalWithRetention
	// (exactly 1 POST, no re-claim): a conflict:true receipt with mismatched
	// identity must be retried with bounded backoff, never terminally failed.
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"token","token_type":"Bearer","expires_in":60}`))
			return
		}
		posts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		// Identity deliberately mismatched: event_id is not the posted fact's.
		_, _ = w.Write([]byte(`{"receipt":{"event_id":"other-fact","tenant_id":"acme","status":"ledgered","accepted_at":"2026-08-04T00:00:00Z","conflict":true}}`))
	}))
	defer server.Close()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "mismatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := repo.(Store)
	runtime, err := New(runtimeConfig(server.URL), store,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	wrapped := WrapRepository(repo, runtime)
	if err := wrapped.RecordAudit(ctx, repository.AuditEntry{TenantID: "acme", Action: "tenant.status"}); err != nil {
		t.Fatalf("record audit: %v", err)
	}
	runtime.Start(ctx)
	// Retry path: second POST lands after the first bounded-backoff window
	// (initial 1s, max 2s in this harness).
	deadline := time.Now().Add(4 * time.Second)
	for posts.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	runtime.Close()
	if got := posts.Load(); got < 2 {
		t.Fatalf("mismatched-conflict fact re-POSTed %d times, want >=2 (retried, not terminal)", got)
	}
	// Never failed: the row stays pending (created_at-based) even while
	// available_at sits in the future after the last retry.
	if _, ok, err := store.OldestPendingAuditGovernance(ctx); err != nil || !ok {
		t.Fatalf("mismatched-conflict fact not pending ok=%v err=%v", ok, err)
	}
	// And it remains reclaimable once the last backoff window passes.
	deadline = time.Now().Add(5 * time.Second)
	for {
		again, err := store.ClaimAuditGovernance(ctx, "observer", "observer-token", 1, 10, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if len(again) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("mismatched-conflict fact never reclaimable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Zero terminal-failed rows: nothing for the retention prune to remove.
	if n, err := store.CleanupFailedAuditGovernance(ctx, time.Now().Add(time.Hour), 10); err != nil || n != 0 {
		t.Fatalf("terminal-failed row appeared: n=%d err=%v", n, err)
	}
}
```

### AC-2 R1 夹具修正（runtime_test.go:117 测试内，断言零改动）

```go
	// （替换 :131-134 的硬编码回包）
	var body struct {
		EventID string `json:"event_id"`
	}
	_ = jsonNewDecoder(r).Decode(&body)
	posts.Add(1)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	// Identity must match the posted fact (ordering contract): echo the
	// request's event_id so conflict is honored after receiptMatches.
	_, _ = fmt.Fprintf(w, `{"receipt":{"event_id":%q,"tenant_id":"acme","status":"ledgered","accepted_at":"2026-08-04T00:00:00Z","conflict":true}}`, body.EventID)
```

---

## 8. 范围纪律与规格同步

**改动面（唯三文件）：** `http.go`（重排 + 注释，~225 行）· `http_test.go`（AC-1 新增，~347 行）· `runtime_test.go`（R1 夹具 + AC-4 新增，~468 行）。**不改：** `receiptMatches` 谓词体、`model.go`、`relay.go`、`repository/`、config、迁移、`openapi.json`、CLI、SDK。

**规格同步清单（`docs/requirements/auditgovernance-receipt-conflict-identity-v1.md` 需随本设计更新两处）：**
1. §2.2 N2 行：注明夹具需回显 event_id（R1），断言集不变；
2. §3 FR-2 第三条："逐字通过" → "断言逐字通过；夹具按 R1 回显 event_id"（其余 FR 不变）。

**D1 契约假设**（沿用规格，不新增）：conflict 收据携带完整身份；省略 `accepted_at` 的 conflict 收据在重排后判可重试 —— 保守方向，AC-1 第 4 行使其可观测。

---

## 9. 先前尝试与兄弟 run 裁决处置（design-gate 复检项）

| # | 来源 | 裁决/发现 | 本设计处置（证据） |
|---|------|----------|-------------------|
| S1 | `docs/auto/runs/replace-the-hardcoded-audit-governance-block-wit-cd58c0a7` design_gate | **FAIL**：B1 派生双配置未 fail-fast / B3 装配门控不查 `L2Variant()` / B4 FR-4 新表面含 sibling 名 / B2 错误文本未逐字钉 | **DISPOSED-无关**：B1/B3 属 auditsink 配置与装配面 —— 本设计零 config/装配改动（§3 变更面 grep 证实无 `config`/`cmd/server` 触碰）；B4 无新文件，注释仅改既有 `internal/auditgovernance` 内部文本且沿用 "the receiver" 措辞、无品牌名新增；B2 部分适用已被吸收 —— 本设计可观测契约是**哨兵身份**（`errors.Is`），模块既有测试已用此模式（`TestReceiptConflictIsTerminalSentinel` :287-290），且**不引入任何新错误字符串**（哨兵文本在 model.go 原样保留） |
| S2 | `docs/auto/runs/reuse-the-audit-governance-transactional-outbox--8327eed2` design_gate | **PASS**（4 阻塞项已解决）；implement 阶段超时（基建，非设计缺陷） | **DISPOSED-无关**：该方向是 events outbox（另一模块）。其 D5 "回显 receipt 校验" 以 `validateReceipt` 为先例 —— 本重排收紧该先例（身份先于 conflict），未来 L2 适配器继承修复，无冲突 |
| S3 | `docs/auto/runs/audit-sink-deleted-11-at-least-once-contract-review`（adversarial review） | **FAIL**（outbox 投递契约文档：去重键/顺序/2xx commit point） | **DISPOSED-无关**：对象是 `audit-sink-deleted-11-v1.design.md`（events outbox 投递契约），非本模块；其 G2 确认治理 relay 有 gap-scan 先例 —— 与本设计 F5 的实测结论（failed 行抑制缺口）一致，无冲突 |
| S4 | 本 run requirements 阶段 | **PASS**；遗留 = 规格 FR-2 "逐字通过" 主张 | **RESOLVED-设计期修正**：R1（§1）给出完整证据链（`uuid.NewString()` vs 硬编码 `event_id:"x"` → `OldestPending` 断言必然失败），夹具修正 + 规格同步（§8），断言集逐字保留 |

---

## 10. 净变更清单

| 文件 | 变更 | 行数预算 |
|------|------|---------|
| `internal/auditgovernance/http.go` | `validateReceipt` 分支重排（身份先、conflict 后）+ 两处注释更新 | 221 → ~225 |
| `internal/auditgovernance/http_test.go` | 新增 `TestReceiptConflictMismatchedIdentityRejected`（4 行表驱动） | 297 → ~347 |
| `internal/auditgovernance/runtime_test.go` | R1 夹具回显 event_id；新增 `TestRuntimeMismatchedConflictReceiptIsRetriedNotTerminal` | 400 → ~468 |
| `docs/requirements/auditgovernance-receipt-conflict-identity-v1.md` | §2.2 N2 / §3 FR-2 两处同步（R1） | — |

`make check`（gofmt/build/vet/test）全绿为合入门禁；无新依赖（I6）；无 wire/config/schema/OpenAPI 面变化（I5 不触及）。
