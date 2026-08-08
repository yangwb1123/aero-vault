# 设计：webhook_failures 保留期清理（prune）—— 配套设计文档 v1

> **配套规格：** `docs/requirements/webhook-failures-retention-prune-v1.md`（FR-1..FR-3, AC-1..AC-3）· **模块：** `internal/events` + `internal/repository` · **状态：** 设计（未实现）· **基线：** HEAD `acfaaf4`
> **门禁要求：** `make check` 全绿（fmt/vet/vet-integration/build/test/cli-check）· 生产文件 ≤ 500 行 · 纯 stdlib（I6）· I1（占位符/时间格式）/I2（零迁移）纪律 · 无新 `go.mod` 依赖。

---

## 0. 证据复核（对要求规格全部引用逐条重查，HEAD `acfaaf4`）

要求规格的 E1–E18 在本设计中**全部再次核对**，结论：

| # | 引用 | 复核结果 |
|---|------|---------|
| E1 | `webhook_failures.go:24` `RecordWebhookFailure` 纯 INSERT | ✅ 零漂移（:24-48，双方言同 SQL，`created_at` 走 DB 默认） |
| E2 | `:91` `MarkWebhookSucceeded` 仅置位 | ✅ 零漂移（:91-99，`SET succeeded=1/true`） |
| E3 | `:102` `MarkWebhookDeadLettered` 仅置位 | ✅ 零漂移（:102-115，同时改写 last_error/next_retry_at/attempts，**不删行**） |
| E4 | relay prune 行号漂移（规格引用 :239，实际 :379） | ✅ 已修正：`prune()` 在 **:379-391**，周期调用 :138-139，常量 :67-69（grep 复核） |
| E5 | `repository_interface.go:134` Webhooks 段无 prune | ✅ 零漂移（:133-139 恰 6 方法；`PruneEventOutbox` 在 :109） |
| E6 | `webhook.go:174` RetryLoop 只轮询 | ✅ 零漂移（:174-190；ticker 硬编码 15s 于 :178；repo-nil 早退 :176-177） |
| E7 | outbox 镜像模式（常量/周期/方法/仓库实现） | ✅ 全部存在：relay :67-69 常量、:138-139 `rounds%60`、:379-391 warn-log+计数；`event_outbox.go:393-423` 含零值保护 :394-396、RowsAffected 求和 |
| E8 | 装配点 `workers.go:79` `go wh.RetryLoop(ctx)` | ✅ 零漂移；reconcile 包不触碰 events 表（grep 复核） |
| E9 | schema 双方言已具备三列（0008/0036） | ✅ sqlite 0008 `succeeded`+`created_at`（毫秒 `%f` 精度）、0036 `dead_lettered`；postgres 同形（BOOLEAN）；**零迁移成立** |
| E10 | created_at 字典序比较安全方向 | ✅ 复核：sqlite 行值毫秒精度 `…Z`，Go 截止值 RFC3339Nano；`Z`(0x5A)>数字(0x30-0x39) → 边界行偏向保留，绝不误删 |
| E11 | 双方言分支先例 | ✅ `NextPendingFailures` :57（pg `= false`）/ :63（sqlite `= 0`） |
| E12 | 只删终态行安全论证 | ✅ `NextPendingFailures` 谓词 `succeeded=0 AND dead_lettered=0`（:62-68）→ 被删行永不被重试队列再读 |
| E13 | 测试基建（回拨助手/直调 prune 先例） | ✅ `backdateEventOutboxAt`（relay_test:85-101，raw `sql.Open("sqlite", dsn)`，modernc 驱动）、`TestOutboxRelay_PruneUsesConfiguredRetention`（:729）、`openRelayTestRepoAt`（:71，显式 dsn 可复用） |
| E14 | ticker 硬编码 → 需 `retryInterval` 注入字段 | ✅ `Webhook` struct 现仅 urls/secret/client/logger/repo 五字段，无 options；测试为白盒 package `events`（webhook_test.go:7 `package events`） |
| E15 | telemetry 现状 | ✅ metrics.go:36-39 声明 4 计数器、:78-81 注册，无 pruned；`IncEventOutboxPruned` :149-150 为 outbox 参照 |
| E16 | 索引现状 | ✅ 0008 `webhook_failures_pending_idx (succeeded, next_retry_at)`；0036 `webhook_failures_retryable_idx (succeeded, dead_lettered, next_retry_at)`；created_at 无索引 |
| E17 | 表名仅两文件引用 | ✅ grep 复核：仅 `webhook.go:172`（注释）+ `webhook_failures.go`（:27/:30/:57/:63/:97/:110/:119/:132）；admin.go 引用的是方法 `ListWebhookFailures` 而非表名 |
| E18 | `PruneEventOutbox` 返回 `(int64, error)` | ✅ interface:109、impl:393 |

**复核新增发现（要求规格未列，驱动本设计 4 处修正）：**

| # | 发现 | 证据 | 处置 |
|---|------|------|------|
| F1 | **仓库接口的全部测试 fake 均内嵌 nil `repository.Repository`** | `recordFailureRepo`（webhook_test.go:38）、`terminalRepo`（webhook_retry_test.go:29）、`stubReadyRepo`（cmd/server/http_test.go:30）、ai 侧 `bm25_test.go:15`/`sink_test.go:38` 均 `repository.Repository` 内嵌 | **接口加方法零编译破坏**；但任何 fake 上误触新方法会 panic（设计意图）——既有测试不触发（见 §3） |
| F2 | `time.NewTicker` 对 `<=0` 会 panic；`webhook_retry_test.go:20` 有 `&Webhook{client:…, logger:…}` 直接字面量先例 | Go 标准行为 + 测试文件 | RetryLoop 内加 `interval<=0 → 15s` 防御（Dv3） |
| F3 | `prune()` 若被直调而 `repo==nil` 会 nil 解引用 panic | `persistFailure` :150 有 `if w.repo == nil { return }` 先例 | `prune()` 内加 repo-nil 早退（Dv4），AC-2 的 RepoNil 测试才有意义 |
| F4 | `PruneEventOutbox` 用事务是因**双表删除**（event_outbox_delivered + event_outbox）；webhook 是**单表单语句** | event_outbox.go:399-423 vs 本设计 SQL | `PruneWebhookFailures` **不需要事务**（单语句原子性），简化镜像（Dv2） |

---

## 1. 设计概览与改动清单

一次 15 分钟的周期（60 ticks × 15s）把 `webhook_failures` 的终态行按 `created_at` 保留期收敛：succeeded 24h / dead-lettered 7d——**完全镜像** outbox relay 的既有模式（E7），落点选 RetryLoop 内部（D4，`cmd/server` 零改动）。

| 文件 | 改动 | 行数增量（当前 → 预计） |
|------|------|------------------------|
| `internal/repository/repository_interface.go` | Webhooks 段末尾 +1 方法声明 | 201 → 203 |
| `internal/repository/webhook_failures.go` | 末尾 +`PruneWebhookFailures` 实现（+`errors` import） | 156 → ~195 |
| `internal/events/webhook.go` | +3 常量、+1 struct 字段、`NewWebhook` 默认值、`RetryLoop` 注入间隔 + tick 计数 + prune 调用、+`prune()` 方法 | 281 → ~335 |
| `internal/events/webhook_prune_test.go` | **新文件**：AC-2 三测试 | ~130 |
| `internal/repository/webhook_failures_test.go` | +回拨助手 + `TestPruneWebhookFailures`（AC-1） | ~90 |

生产文件均 < 500 行门禁（见 §7）。无迁移文件、无配置项、无 telemetry 变更、无 admin API 变更、`cmd/server` 零改动。

---

## 2. API 变更

### 2.1 `repository.Repository` 接口（`repository_interface.go` Webhooks 段 :139 后）

```go
// PruneWebhookFailures removes succeeded rows older than succeededBefore and
// dead-lettered rows older than deadLetteredBefore. DELETE-style, no
// tombstones. Pending rows (succeeded=0 AND dead_lettered=0) are never
// matched. Returns the number of removed rows. Zero-valued cutoffs are
// rejected (would otherwise delete the whole table).
PruneWebhookFailures(ctx context.Context, succeededBefore, deadLetteredBefore time.Time) (int64, error)
```

### 2.2 `sqlStore.PruneWebhookFailures`（`webhook_failures.go` 末尾，镜像 `PruneEventOutbox` :393-423）

```go
func (s *sqlStore) PruneWebhookFailures(ctx context.Context, succeededBefore, deadLetteredBefore time.Time) (int64, error) {
	if succeededBefore.IsZero() || deadLetteredBefore.IsZero() {
		return 0, errors.New("webhook failure prune times are required")
	}
	succeededCutoff := succeededBefore.UTC().Format(time.RFC3339Nano)   // I1 时间惯例
	deadLetteredCutoff := deadLetteredBefore.UTC().Format(time.RFC3339Nano)
	var q string
	if s.dialect == dialectPostgres {
		q = `DELETE FROM webhook_failures
WHERE (succeeded = true AND created_at < $1)
   OR (dead_lettered = true AND created_at < $2)`
	} else {
		q = `DELETE FROM webhook_failures
WHERE (succeeded = 1 AND created_at < $1)
   OR (dead_lettered = 1 AND created_at < $2)`
	}
	res, err := s.db.ExecContext(ctx, s.rebind(q), succeededCutoff, deadLetteredCutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
```

要点（对照 FR-1 约束 a–f）：
- **双方言分支**：`= 1` / `= true`，镜像 `NextPendingFailures` :57/:63 风格（E11）。
- **I1 纪律**：`$1`/`$2` 各出现一次、按文本顺序经 `rebind` 改写为 `?`；时间一律 `UTC().Format(time.RFC3339Nano)`（与 `RecordWebhookFailure`/`NextPendingFailures` 同族，E10 安全论证成立）。
- **零值保护**（约束 c）：镜像 event_outbox.go:394-396，防调用方误传零值清空全表。
- **单语句原子**（F4/Dv2）：无需事务——`PruneEventOutbox` 的事务是为双表一致性，此处单表 DELETE 天然原子。
- **幂等**（约束 d）：重复调用 `RowsAffected=0` 返回 `(0, nil)`。
- **pending 永不删**（约束 e）：谓词只匹配 `succeeded=1` / `dead_lettered=1`。
- **零迁移**（约束 f）：列双方言已存在（E9），I2 不触碰。

### 2.3 `internal/events/webhook.go`

**常量**（文件顶部，镜像 relay :67-69）：

```go
const (
	webhookPruneEveryTicks = 60  // 60 × 15s tick ≈ 15min（镜像 eventOutboxPruneEveryRounds=60）
	webhookDeliveredRetain = 24 * time.Hour     // succeeded 行保留期（镜像 eventOutboxDeliveredRetain）
	webhookFailedRetain    = 7 * 24 * time.Hour // dead-lettered 行保留期（镜像 eventOutboxFailedRetain）
)
```

**struct + 构造器**：

```go
type Webhook struct {
	urls          []string
	secret        []byte
	client        *http.Client
	logger        *slog.Logger
	repo          repository.Repository
	retryInterval time.Duration // 未导出；默认 15s，测试注入毫秒级（FR-2e）
}
// NewWebhook 内新增：
return &Webhook{urls: cleaned, client: &http.Client{Timeout: 5 * time.Second},
	logger: logger, retryInterval: 15 * time.Second}
```

**RetryLoop**（:174-190 改造，镜像 relay :138-139 的 tick 计数模式）：

```go
func (w *Webhook) RetryLoop(ctx context.Context) {
	if w.repo == nil {
		return
	}
	interval := w.retryInterval
	if interval <= 0 {
		interval = 15 * time.Second // 防御：struct 字面量遗漏字段时防 ticker panic（F2/Dv3）
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	ticks := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pending, err := w.repo.NextPendingFailures(ctx, 25)
			if err != nil {
				w.logger.Warn("retry pending fetch", "err", err)
				continue
			}
			for _, f := range pending {
				w.retryOne(ctx, f)
			}
			ticks++
			if ticks%webhookPruneEveryTicks == 0 {
				w.prune(ctx)
			}
		}
	}
}
```

**prune 方法**（镜像 relay :379-391 的 warn-log 结构；`removed` 计数返回给 AC-1 断言与未来 telemetry，本设计按 D3 不新增计数器故调用侧忽略）：

```go
// prune removes terminal webhook failure rows past their retention horizons:
// succeeded rows older than 24h, dead-lettered rows older than 7d. DELETE-
// style, no tombstones — NextPendingFailures never re-reads terminal rows.
// Failures are warn-logged and never interrupt the retry loop.
func (w *Webhook) prune(ctx context.Context) {
	if w.repo == nil {
		return // F3/Dv4：直调安全（镜像 persistFailure :150 的 repo-nil 守卫）
	}
	now := time.Now().UTC()
	_, err := w.repo.PruneWebhookFailures(ctx,
		now.Add(-webhookDeliveredRetain), now.Add(-webhookFailedRetain))
	if err != nil {
		w.logger.Warn("webhook prune failed", "err", err)
		return
	}
	// 零行删除静默成功（无日志、无计数）；removed 计数经返回签名保留（AC-1 断言用）
}
```

ctx 说明（Dv1）：prune 复用 **RetryLoop 的 ctx**（取消传播、无新 goroutine）；relay 用 `context.WithTimeout(context.Background(), …)` 是因为其 prune 在 deliverBatch 之后独立计时，webhook 的 repo 操作是本地 SQLite/PG 单语句、无此需要——两选一均正确，取更简者。

---

## 3. 兼容性约束

| # | 约束 | 证据/论证 |
|---|------|---------|
| C1 | **接口增长零编译破坏**：仓库内唯一真实实现是 `*sqlStore`；全部测试 fake 内嵌 nil `repository.Repository`（F1）——新增方法不破坏任何编译单元 | webhook_test.go:38、webhook_retry_test.go:29、http_test.go:30、bm25_test.go:15、sink_test.go:38 |
| C2 | **零 schema 变更（I2）**：两列双方言已存在（E9）；不新增迁移文件、不触碰 `.up/.down.sql` | 0008/0036 双方言复核 |
| C3 | **零配置变更**：保留期为编译期常量（D1）；`docs/configuration.md` 不动；无 fleet-wide 配置分歧可能（对比 outbox 的 env 配置存在 fleet-min 语义——兄弟 gate ef2d0976 的发现，对本设计**不适用**，见 §8） | FR-2a/f |
| C4 | **`cmd/server` 零改动**：RetryLoop 已在 workers.go:79 启动；装配不变 | E8 |
| C5 | **无公开 API 变化**：`NewWebhook`/`WithRetryStore` 签名不变；`retryInterval` 未导出；`Repository` 接口只增不改 | §2 |
| C6 | **at-least-once 投递不变量保持**：pending 行（`succeeded=0 AND dead_lettered=0`）永不匹配删除谓词；prune 与 `NextPendingFailures` 谓词互补（E12） | FR-3 |
| C7 | **未配置 retry store 时零行为变化（I5）**：`RetryLoop` repo-nil 早退不变；`prune()` 自带 repo-nil 守卫 | E6、F3 |
| C8 | **admin API 不变**：`ListWebhookFailures`（admin.go:263-268）签名/语义不变，列表随 prune 自然变短 | E17 |
| C9 | **既有测试零改动**：`TestWebhookRetryLoop_NoRepoDoesNotPanic`（webhook_test.go:491-497）走 repo-nil 早退；`recordFailureRepo`/`terminalRepo` 驱动的方法集不含新方法；`&Webhook{…}` 字面量（webhook_retry_test.go:20）因 `retryInterval` 未导出字段且非编译期必填而不受影响 | 逐文件核对 |
| C10 | **多副本语义**：每副本各跑一个 RetryLoop，各自 prune；DELETE 幂等，并发执行至多一方 RowsAffected>0，无锁/租约需求（区别于 reconcile 单例） | §4 F-3 |

---

## 4. 失败模式（F-模式表）

| # | 模式 | 行为 | 缓解/处置 |
|---|------|------|----------|
| F-1 | DB 错误（连接断开/锁）时 prune | warn log 后返回，**不中断 RetryLoop**；下一轮（15min 后）重试 | 镜像 relay:384-388；与 `retry pending fetch` 错误处理同风格 |
| F-2 | 零值截止时间被传入 | repo 层返回 error → warn log；**绝不清空全表** | 零值保护（§2.2）；测试 AC-1 第 6 步钉死 |
| F-3 | 多副本并发 prune | 幂等 DELETE；至多一方计数>0；无数据竞争（单语句） | 无需租约（区别于 reconcile 单例锁）；C10 |
| F-4 | `retryInterval<=0`（struct 字面量遗漏） | `time.NewTicker` 会 panic | RetryLoop 防御回退 15s（Dv3）；生产路径恒为 15s（NewWebhook 默认） |
| F-5 | `prune()` 被直调且 repo==nil | nil 解引用 panic | 方法内 repo-nil 早退（Dv4）；AC-2 回归锁 |
| F-6 | 时钟回拨 | 截止值偏小 → 行多留一个周期（**安全方向**） | 保留期 best-effort（与 relay 同语义）；E10 |
| F-7 | 时钟前跳 | 终态行可能提前一个周期被删（最多 ~15min） | 仅影响终态死行，无投递语义损失；与 relay 同语义 |
| F-8 | ctx 取消时 prune 执行中 | DELETE 中止，下轮重试 | 无泄漏（无新 goroutine/连接） |
| F-9 | 表规模大、无 created_at 索引 | prune 每 15min 一次部分/全表扫描 | **接受**（E16）：表仅按失败量增长（每次失败一行，≤25 并发重试）；`succeeded` 是 `webhook_failures_retryable_idx` 前导列，sqlite 对 OR 谓词的 index-merge 不保证 → 最坏全扫一次/15min；不做索引变更（I2）。兄弟 gate R1 同型问题按此处置（§8） |
| F-10 | 行在 `NextPendingFailures` 读取与 prune 之间被标记终态 | 该行可能晚一个周期才被删 | 无正确性问题（终态行本就该删）；无竞态窗口风险（单 goroutine 顺序执行） |

---

## 5. 迁移步骤

**DB 迁移：零。** 无新迁移文件（I2）；`succeeded`/`dead_lettered`/`created_at` 双方言已存在（E9）。`repo.Migrate` 启动行为不变。

**部署滚动：**
1. 构建新二进制（`make check` 全绿后）。
2. 滚动重启副本；`startWebhook`（workers.go:72-79）装配不变。
3. **首个 prune 发生在启动后首个 `60 × 15s = 15min` tick**——历史累积的终态死行在第一次 prune 即被收敛（无需回填脚本、无一次性 SQL）。
4. 观测：`/v1/admin/webhook-failures` 列表自然缩短；无新配置项需同步；多副本无需配置对齐（C3）。

**回滚：** 旧二进制无 prune 行为，仅停止清理（表重新累积），无数据/契约破坏；无需迁移回退。

---

## 6. 验收映射（AC-1..AC-3 → 测试 × 断言 × 命令）

### AC-1 → `internal/repository/webhook_failures_test.go`

新增助手（镜像 `backdateEventOutboxAt` relay_test:85-101 的 raw-sqlite 形态；写入 `when.UTC().Format(time.RFC3339Nano)` 与生产格式同族，E10）：

```go
func backdateWebhookCreatedAt(t *testing.T, dsn string, id int64, when time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn) // modernc 驱动已在测试包导入
	...
	_, err = db.ExecContext(ctx, `UPDATE webhook_failures SET created_at=$1 WHERE id=$2`,
		when.UTC().Format(time.RFC3339Nano), id)
}
```

`TestPruneWebhookFailures`（显式 dsn `"file:"+filepath.Join(t.TempDir(),"wf.db")`，照 `TestNextPendingFailures` 先例自建 repo 以便助手连接；复用 `recordWebhook` :25-44）：

| 步骤 | 动作 | 断言 |
|------|------|------|
| 1 | seed → `MarkWebhookSucceeded` → 回拨 3h → `PruneWebhookFailures(ctx, now-1h, now-7d)` | `n==1`；`ListWebhookFailures` 无此行 |
| 2 | seed → succeeded → 回拨 30min → 同调用 | `n==0`；行仍在 |
| 3 | seed 两行：一行 dead-lettered 回拨 8d、一行 pending | `n==1`；仅 dead-lettered 行消失，**pending 行仍在** |
| 4 | 对同一 repo 重复同截止值调用 | `n==0`，无 error（幂等） |
| 5 | `PruneWebhookFailures(ctx, time.Now(), time.Now())` | 全部终态行清空、pending 保留（全删路径） |
| 6 | `PruneWebhookFailures(ctx, time.Time{}, time.Now())` | **error 非 nil**，且无行被删（零值保护） |

### AC-2 → 新文件 `internal/events/webhook_prune_test.go`（package `events` 白盒）

| 测试 | 动作 | 断言 |
|------|------|------|
| `TestWebhook_RetryLoopPrunes` | `dsn := "file:"+…`；`openRelayTestRepoAt(t, dsn)` 复用（同包，:71）；seed 两行 succeeded，`backdateWebhookCreatedAt` 回拨 **25h / 23h**（钉死 24h 默认常量）；`wh := NewWebhook("http://127.0.0.1:1", logger).WithRetryStore(repo); wh.retryInterval = 10*time.Millisecond`；`go wh.RetryLoop(ctx)`；轮询 `ListWebhookFailures` 至多 3s（≥5 个 prune 周期：60×10ms=600ms） | 25h 行消失、23h 行保留、无 panic（succeeded 行不会被 `NextPendingFailures` 拾取 → 无 HTTP 调用） |
| `TestWebhook_Prune_Noop` | 空 repo 直调 `wh.prune(ctx)` | 无 error、无 panic、表仍空（零行 no-op） |
| `TestWebhook_Prune_RepoNilNoop` | `NewWebhook` 未 `WithRetryStore` 直调 `wh.prune(ctx)` | 无 panic（F3/Dv4 回归锁）；`RetryLoop` 早退不变（既有 `TestWebhookRetryLoop_NoRepoDoesNotPanic` 续保） |

### AC-3 → 门禁命令

```bash
go test ./internal/repository -run Webhook          # 含 TestPruneWebhookFailures
go test ./internal/events -run 'TestWebhook_RetryLoopPrunes|TestWebhook_Prune'
gofmt -l internal/repository internal/events        # 期望空输出
go vet ./internal/repository ./internal/events      # 期望空输出
make check                                          # 全量门禁
```

---

## 7. 门禁合规（`make check`）

| 门禁 | 状态 | 说明 |
|------|------|------|
| `gofmt -l` 无输出 | ✅ | 新增代码按标准格式；提交前跑 `gofmt -w` |
| `go build ./...` | ✅ | 接口新增方法仅 `*sqlStore` 实现；fake 全部内嵌接口（F1） |
| `go vet ./...`（含 `-tags=integration`） | ✅ | 无新包/依赖；`errors` import 补齐 |
| `go test ./...` | ✅ | 新测试独立文件；既有测试零改动（C9）；SQLite+local FS 零网络（AC-2 用 `http://127.0.0.1:1` 且 succeeded 行不被重试拾取 → 无出站请求） |
| **单文件 ≤ 500 行（生产代码）** | ✅ | webhook.go 281→~335；webhook_failures.go 156→~195；其余文件 <20 行增量；测试文件不受行数门禁（Makefile:161-165 仅 `-not -name '*_test.go'`） |
| 圈复杂度 ≤ 10（WARN 级） | ✅ | `prune()` 线性；RetryLoop 仅 +tick 计数分支 |
| I1（占位符/时间格式） | ✅ | `$1`/`$2` 独立出现、`rebind` 改写；RFC3339Nano |
| I2（零迁移） | ✅ | 无迁移文件 |
| I5（opt-in 默认） | ✅ | 未配置 retry store → 零行为变化（C7） |
| I6（stdlib） | ✅ | 无新依赖；测试仅用 `testing` |

---

## 8. 先前尝试与 gate 发现处置（逐条，供 gate 复查）

### 8.1 本管线 `DECISIONS.md`（仅 1 条：requirements PASS，无未决发现）

- requirements 交付：`docs/requirements/webhook-failures-retention-prune-v1.md` 已落盘（本设计 §0 全量复核，E1-E18 成立，F1-F4 为复核新增）。
- 规格 §5 决策 D1-D5 本设计**全部采纳**：D1 无 env 配置（C3）、D2 `retryInterval` 注入字段（§2.3）、D3 无 telemetry 计数器（§2.3 prune 注释）、D4 选 RetryLoop 否决 reconcile hook（§1）、D5 行号漂移修正（E4）。
- **无遗留 finding。** 规格 AC-1..AC-3 原样保留并映射为 §6 可执行测试。

### 8.2 兄弟 run `transactional-outbox-for-deletion-events-write-v-b0546a6a`（DECISIONS.md D3 GAP-2）

- 其 D3 提到 `webhook_failures` 仅作**范围边界陈述**（"commit→emit 窗口内 webhook 本地投递缺口不在该设计覆盖范围；webhook_failures 仅记已收事件的投递失败"）。
- **处置：不适用（N/A）**——该陈述是对方方向的边界声明，与本方向（表膨胀清理）正交，不构成对本设计的约束或未决问题。其 design gate 逐条对账（GAP-1/2/3/4）均落于对方方向。

### 8.3 兄弟 gate `make-rag-chunk-invalidation-durable-async-and-tr-b29a7c7a` 的 R1（prune 缺索引，Open）

- R1 原话："prune lacks `delivered_at_ns` index, 10× at 100k, zero-migration blocks — Open"（针对 RAG chunk invalidation 表）。
- **同类问题处置（本设计 F-9/E16）**：webhook prune 谓词 `(succeeded, created_at)`；`succeeded` 是 `webhook_failures_retryable_idx` 前导列（0036），sqlite 对 OR 谓词的 index-merge 不保证 → 最坏情况每 15min 一次表扫描。接受理由（证据）：① 表增长速率受失败投递量约束（每次失败一行，重试并发 ≤25，dead-lettered 后停止写入）；② 15min 一次的保留 job 扫描在 SQLite 本地文件上为亚秒级（万行量级）；③ 加索引需新迁移文件，违反 I2 零迁移目标且收益不匹配风险（0036 迁移已应用，回填索引需评估）。**结论：明确接受，非未决项。**

### 8.4 兄弟 gate `add-a-dedicated-durable-async-outbox-config-sect-ef2d0976`（outbox 保留期配置 + fleet-min 语义）

- 该 gate 确认了 outbox 的 env 配置化保留期 + "fleet-wide 最小保留期胜出"语义。
- **处置：不适用（N/A）**——本方向按 D1 明确**不配置化**（C3），保留期为编译期常量 → 全 fleet 恒同，fleet-min 分歧问题在本设计**不可能发生**，无需配置对齐文档（区别于 docs/configuration.md:361-362 对 outbox 的多副本警告）。

### 8.5 其余兄弟 gate（guard-or-error、authorizationprovider 等）

- grep 复核：均不涉及 `webhook_failures`/webhook prune（`add-a-fail-closed-guard-variant` 的 "mid-prune" 指 lease guard 的 sweep 竞态，与本方向无关）。
- **处置：N/A。**

---

## 9. 设计偏差记录（相对要求规格，均证据驱动）

| # | 规格原文 | 本设计 | 证据 |
|---|---------|--------|------|
| Dv1 | FR-2c 未指明 prune 的 ctx 来源（relay 镜像暗示 fresh ctx） | prune 复用 **RetryLoop 的 ctx**（取消传播，无新 goroutine/超时上下文） | relay 用 fresh ctx 因其在 deliverBatch 后独立计时；webhook 的 prune 是本地单语句 repo 调用，两选一均正确，取简者 |
| Dv2 | FR-1 镜像 `PruneEventOutbox` 的事务形态 | **无事务**——单表单语句 DELETE 原子 | `PruneEventOutbox` 事务是为双表（event_outbox_delivered + event_outbox）一致性（F4）；本 SQL 单语句无中间状态 |
| Dv3 | FR-2e 仅规定 `NewWebhook` 默认 15s | RetryLoop 内追加 `interval<=0 → 15s` 防御 | `time.NewTicker` 对非正时长 panic；`&Webhook{…}` 字面量先例（webhook_retry_test.go:20）（F2） |
| Dv4 | FR-2c 依赖 RetryLoop 的 repo-nil 早退 | `prune()` 方法内自带 `if w.repo == nil { return }` | AC-2 的 `TestWebhook_Prune_RepoNilNoop` 直调 prune（F3）；镜像 `persistFailure` :150 同款守卫 |
| Dv5 | FR-2c 不指定计数去向 | `removed` 经返回签名保留、调用侧忽略（`_`），零日志零计数 | D3（无 telemetry）；AC-1 第 1/4/5 步依赖仓库层计数断言 |

**proposed_vs_verified 对照（设计层）：** verified——接口无 prune（E5）、RetryLoop 只轮询（E6）、镜像模式全套（E7，行号按 :379 修正）、schema 双方言已具备（E9）、fake 全内嵌接口（F1）、回拨助手/直调先例可复用（E13）；proposed——`PruneWebhookFailures` 单语句双方言 DELETE + 零值保护 + RowsAffected（§2.2）、常量 24h/7d/60ticks + RetryLoop tick 计数（§2.3）、`retryInterval` 注入 + 防御回退（§2.3/Dv3）、prune 内 repo-nil 守卫（Dv4）、零配置/零迁移/零 telemetry/零 admin 变更（§3/§5）、AC-1..AC-3 全测试化映射（§6）。

---

## 10. 实现指引（供 implement 阶段，非本设计交付物）

1. `internal/repository/repository_interface.go` :139 后插入 §2.1 方法声明。
2. `internal/repository/webhook_failures.go` 末尾插入 §2.2 实现（import 增 `errors`）。
3. `internal/events/webhook.go`：顶部常量组；struct 加 `retryInterval`；`NewWebhook` 设默认 15s；`RetryLoop` 按 §2.3 改造；新增 `prune()`。
4. `internal/repository/webhook_failures_test.go`：`backdateWebhookCreatedAt` 助手（镜像 relay_test:85-101）+ `TestPruneWebhookFailures`（§6 AC-1 六步）。
5. 新文件 `internal/events/webhook_prune_test.go`：AC-2 三测试（复用 `openRelayTestRepoAt`）。
6. 验证：§6 AC-3 命令 + `make check`；`gofmt -w` 后复查 `gofmt -l` 空输出。
