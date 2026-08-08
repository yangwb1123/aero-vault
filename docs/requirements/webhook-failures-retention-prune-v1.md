# 方向：webhook_failures 表无界增长 — 为 succeeded / dead-lettered 行增加保留期清理（prune）

> **模块：** `internal/events`（+ `internal/repository`） · **来源分析：** `docs/auto/analyses/internal-events-495038b5.json`（方向 2） · **日期：** 2026-08-07
> **评分：** 价值 7 / 风险降低 6 / 工作量 3 / 置信度 10
> **本文所有代码引用均已对照仓库逐条验证**（行号以当前工作树为准；方向 6 条引用中 5 条零漂移，1 条行号漂移但符号与模式成立，见 E6）。
>
> **与既有规格/在途实现的关系：** 无冲突——不触碰 schema/迁移（I2），不新增配置项，`cmd/server` 零改动（RetryLoop 已在 workers.go:79 启动）。镜像对象是兄弟 outbox relay 的既有 prune 模式（`event_outbox_relay.go:379-391` + `PruneEventOutbox`，E4/E7）。分析文件中的方向 1（Notifier 持久化）与方向 3（pg_notify 8000 字节限制）**明确不在本规格范围**（§5）。

---

## 1. 问题陈述

`webhook_failures` 表只增不减：

1. 每次投递失败插入**一行永久记录**——`RecordWebhookFailure` 是纯 INSERT，无 `(event_id, url)` upsert（E1），`NextRetryAt` 退避到最长 1h（webhook.go:157-161）。
2. `MarkWebhookSucceeded`（E2）/ `MarkWebhookDeadLettered`（E3）**只置位标志**，不删行、不记时间戳。
3. **全仓没有任何 DELETE**：表名 `webhook_failures` 仅出现在 `internal/events/webhook.go` 与 `internal/repository/webhook_failures.go`（E17）；repository 接口 Webhooks 段（repository_interface.go:133-139）只有 6 个方法，无 prune（E5）；reconcile/retention 任务不触碰该表；无保留期配置。
4. `RetryLoop`（webhook.go:174）每 15s 轮询 `NextPendingFailures(ctx, 25)` 重新投递，**从不清理**（E8）。

后果：一旦配置 `EVENTS_WEBHOOK_URL` 且端点不稳定，succeeded 与 dead-lettered 行**永久累积**——表膨胀拖慢 `NextPendingFailures` 的 `ORDER BY id` 扫描（webhook_failures.go:62-68），admin 列表 API `ListWebhookFailures`（admin.go:263-268，`ORDER BY id DESC`）返回越来越多死行。

**兄弟模块已有解法（本方向要镜像的模式）：** outbox relay 定义保留期常量（delivered 24h / failed 7d，E7a）、在自身循环内每 60 轮 prune 一次（E7b）、仓库层 `PruneEventOutbox` 事务删除并返回删除数（E7c）——webhook 路径照搬即可。

### 触发场景（真实工作流）

1. `EVENTS_WEBHOOK_URL` 指向偶发 5xx/超时的端点 → 每次失败插一行；重试 10 次后 `handleRetryFailure`（webhook.go:231-249）置 `dead_lettered`（:238 `attempts >= 10`）——dead-lettered 行此后**永不再被读取**，成为纯死数据。
2. 端点恢复 → 重试成功，`MarkWebhookSucceeded` 置位——succeeded 行同样永久保留，同样无人再读（`NextPendingFailures` 的过滤谓词 `succeeded = 0 AND dead_lettered = 0` 已排除它们，E12）。
3. 长跑实例 + 高事件量：每事件每失败一次一行；数月后数十万死行，admin 的 `/v1/admin/webhook-failures`（LIMIT 50 但 `ORDER BY id DESC`）与 `NextPendingFailures` 扫描退化。
4. 与 outbox relay 同机运行形成反差：relay 的表按 24h/7d 自动收敛，webhook 表却无限增长——运维无任何旋钮可调。

---

## 2. 现状与代码证据（已逐条验证）

| # | 证据 | 验证结果 |
|---|------|---------|
| E1 | `webhook_failures.go:24` — `RecordWebhookFailure`：纯 INSERT（`INSERT INTO webhook_failures (event_id, url, payload, …)`），无 `(event_id, url)` upsert，`created_at` 由 DB 默认值写入 | ✅ 与方向引用一致，零漂移（:24-42） |
| E2 | `webhook_failures.go:91` — `MarkWebhookSucceeded`：仅 `UPDATE … SET succeeded=1/true WHERE id=$1` | ✅ 与引用一致（:91-99） |
| E3 | `webhook_failures.go:102` — `MarkWebhookDeadLettered`：仅置 `dead_lettered` + 更新错误信息/退避时间，不删行 | ✅ 与引用一致（:102-115） |
| E4 | `event_outbox_relay.go:239` — **方向引用为 "prune — the pattern to mirror"** | ⚠️ **行号漂移**：:239 在 `failImmediately` 内（`telemetry.IncEventOutboxFailed` 附近）；实际 `prune()` 在 **:379-391**，周期调用在 **:138-139**（`rounds%eventOutboxPruneEveryRounds == 0`），常量在 **:67-69**。符号与模式全部存在，仅行号偏移约 140 行 |
| E5 | `repository_interface.go:134` — Webhooks 段（:133-139）列 6 方法（RecordWebhookFailure / NextPendingFailures / MarkWebhookSucceeded / MarkWebhookDeadLettered / UpdateWebhookFailure / ListWebhookFailures），**无 prune**；兄弟方法 `PruneEventOutbox` 在 :109 | ✅ 与引用一致 |
| E6 | `webhook.go:174` — `RetryLoop`：15s ticker（:178），只调 `NextPendingFailures(ctx, 25)` 后逐行 `retryOne`，无 prune；`w.repo == nil` 早退（:176-177） | ✅ 与引用一致（:174-190） |
| E7 | **镜像模式（补充验证）**：a) 常量 `eventOutboxDeliveredRetain = 24h`（relay:68）、`eventOutboxFailedRetain = 7×24h`（:69）、`eventOutboxPruneEveryRounds = 60`（:67）；b) `Run` 循环内 `rounds++; if rounds%60 == 0 { r.prune() }`（:138-139）；c) `prune()` 调 `repo.PruneEventOutbox(ctx, now.Add(-deliveredRetain), now.Add(-failedRetain))`，错误 warn log 后返回，成功删除计数走 `IncEventOutboxPruned`（:379-391）；d) 仓库实现 `event_outbox.go:393-423`：**零值时间 → error**（:394-396）、事务内按状态分条件 DELETE、`RowsAffected` 求和返回 | ✅ 全部存在 |
| E8 | 装配点：`cmd/server/workers.go:79` — `go wh.RetryLoop(ctx)`（`startWebhook`，仅当 `EVENTS_WEBHOOK_URL` 非空且 `WithRetryStore(repo)`）；reconcile 包不触碰 events 表（grep 确认） | ✅ 补充验证——prune 的唯一自然落点是 RetryLoop 内部 |
| E9 | **Schema（补充验证）**：sqlite 0008 迁移 `created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))`（**毫秒精度**）；postgres 0008 `created_at TIMESTAMPTZ NOT NULL DEFAULT now()`；`succeeded`/`dead_lettered` 双方言均有（0008 + 0036，后者加 `webhook_failures_retryable_idx (succeeded, dead_lettered, next_retry_at)`） | ✅ 补充验证——无需任何迁移，两列已存在 |
| E10 | **created_at 比较安全性（补充验证）**：sqlite TEXT 字典序比较中，毫秒精度行的 `Z`（0x5A）> 任意数字（0x30-0x39）→ 与 RFC3339Nano 截止值比较时，边界行只会**偏向保留**（多留一个周期），绝不误删；RFC3339Nano 格式与既有代码惯例一致（RecordWebhookFailure/NextPendingFailures 均 `UTC().Format(time.RFC3339Nano)`，I1） | ✅ 补充验证——安全方向，无正确性问题 |
| E11 | **双方言先例**：`NextPendingFailures` 按 dialect 分支（sqlite `succeeded = 0` / postgres `succeeded = false`，:60-69）——prune SQL 须同型分支；`$1`/`$2` 经 `rebind` 改写（I1） | ✅ 补充验证 |
| E12 | **安全论证**：`NextPendingFailures` 谓词 `succeeded = 0 AND dead_lettered = 0`（:62-68）→ 被 prune 的行本就永远不被重试队列再读；`MarkWebhookSucceeded`/`MarkWebhookDeadLettered` 后 `next_retry_at` 已无意义，故边界列必须用 **created_at**（succeeded 行无成功时间戳可依） | ✅ 补充验证——prune 只删"已终态且无人再读"的行 |
| E13 | **测试基建**：`webhook_failures_test.go`（package `repository_test`，`openWebhookRepo` + `recordWebhook` 助手）；outbox 侧已有确定性回拨模式 `backdateEventOutboxAt`（events 测试 :90-101，raw `sql.Open` UPDATE 时间列）与 `TestOutboxRelay_PruneUsesConfiguredRetention`（:729 起，**直调 `relay.prune()`** 而非等待循环） | ✅ 补充验证——AC-1/AC-2 的测试形态照此设计 |
| E14 | **可测性约束**：RetryLoop ticker 硬编码 15s（:178）——验收 2 要求"RetryLoop 周期 prune"，测试等 15s 不可接受；outbox relay 的轮询间隔是 options 字段，而 Webhook 无 options。解法：新增**未导出字段 `retryInterval`**（默认 15s，生产行为不变），白盒测试（`internal/events` 测试均为 package `events`）注入毫秒级间隔 | ✅ 补充验证——决定见 D2 |
| E15 | **Telemetry**：`IncEventOutboxPruned`（metrics.go:149-150）是 outbox prune 的计数；webhook 已有 4 个计数器（`webhook.retries_total` / `delivery_total` / `delivery_latency_ms` / `dead_letter_total`，metrics.go:36-39, :78-81）但无 pruned 计数器 | ✅ 补充验证——是否新增见 D3 |
| E16 | **索引现状**：`webhook_failures_pending_idx (succeeded, next_retry_at)`（0008）+ `webhook_failures_retryable_idx (succeeded, dead_lettered, next_retry_at)`（0036）；prune 谓词 `(succeeded, created_at)` 可用前导列，created_at 未索引 → 每 15min 一次的保留 job 做部分扫描可接受，**无需索引变更** | ✅ 补充验证 |
| E17 | "grep 只有 webhook.go 与 webhook_failures.go 引用它"：全仓（internal/ + cmd/，非测试）表名仅命中这两文件；admin.go:263-268 引用的是 `ListWebhookFailures` **方法**而非表名 | ✅ 与方向断言一致 |
| E18 | **行数口径**：`PruneEventOutbox` 返回 `(int64, error)`（interface:109）——镜像签名须同型，幂等性/计数可断言 | ✅ 补充验证 |

### 缺陷机理

```
RecordWebhookFailure（webhook_failures.go:24，纯 INSERT）  ──┐
  每次投递失败插一行（退避 30s·2^n ≤ 1h + jitter）            │
                                                              ▼
RetryLoop（webhook.go:174，15s ticker）                    webhook_failures 表
  ├─ NextPendingFailures(25)：ORDER BY id 扫描               （只增不减）
  ├─ retryOne 成功 → MarkWebhookSucceeded（置位，不删）        ├─ succeeded=1 行：死数据，永久保留
  └─ 10 次失败 → MarkWebhookDeadLettered（置位，不删）          └─ dead_lettered=1 行：死数据，永久保留
无任何 DELETE 路径（E17）                                      ▼
                                          ListWebhookFailures（admin.go:263）与
                                          NextPendingFailures 扫描随表膨胀退化

兄弟对照：EventOutboxRelay.prune()（relay:379-391，每 60 轮）
  → PruneEventOutbox（event_outbox.go:393-423）→ delivered 24h / failed 7d 自动收敛  ← 本方向镜像
```

---

## 3. 需求规格

### FR-1：仓库方法 `PruneWebhookFailures(ctx, succeededBefore, deadLetteredBefore) (int64, error)`

`internal/repository`（接口 `repository_interface.go` Webhooks 段 :133-139 + 实现 `webhook_failures.go` 末尾），签名镜像 `PruneEventOutbox`（E7d/E18）：

```go
// 接口（Webhooks 段末尾）
PruneWebhookFailures(ctx context.Context, succeededBefore, deadLetteredBefore time.Time) (int64, error)
```

- **约束 a（删除语义）：** 单语句删除，**双方言分支**（镜像 E11）：

  ```sql
  -- sqlite
  DELETE FROM webhook_failures
  WHERE (succeeded = 1     AND created_at < $1)
     OR (dead_lettered = 1 AND created_at < $2)
  -- postgres
  DELETE FROM webhook_failures
  WHERE (succeeded = true  AND created_at < $1)
     OR (dead_lettered = true AND created_at < $2)
  ```

  `$1`/`$2` 经 `s.rebind` 改写（I1，数字按文本出现顺序处理，禁止复用占位符）；截止值 `succeededBefore.UTC().Format(time.RFC3339Nano)`（I1 时间惯例，E10）。
- **约束 b（边界列）：** 只比较 **`created_at`**，不比较 `next_retry_at`（succeeded/dead-lettered 行的 next_retry_at 是失效的退避时间，且无成功时间戳可依，E12）。succeeded 行仅按 `succeededBefore` 删除，dead-lettered 行仅按 `deadLetteredBefore` 删除——两窗口独立。
- **约束 c（零值保护）：** `succeededBefore.IsZero() || deadLetteredBefore.IsZero()` → 返回 error（镜像 `PruneEventOutbox` 的 :394-396 "prune times are required"）。**防止调用方误传零值把全表当旧行清空。**
- **约束 d（返回删除行数）：** `RowsAffected()` 求和返回 `(int64, error)`（镜像 event_outbox.go:407-421）；**幂等**——重复调用返回 0。
- **约束 e（保留 pending）：** 谓词只匹配 `succeeded=1` / `dead_lettered=1`，双 0 的 pending 行**永不删除**（重试队列完整性，与 `NextPendingFailures` 谓词互补，E12）。
- **约束 f（不引入迁移）：** 零 schema 变更（两列双方言均已存在，E9），I2 不受影响。

### FR-2：保留期常量 + RetryLoop 周期 prune（镜像 outbox relay）

`internal/events/webhook.go`：

- **约束 a（常量）：** 镜像 E7a——

  ```go
  webhookDeliveredRetain = 24 * time.Hour     // succeeded 行保留期（镜像 eventOutboxDeliveredRetain）
  webhookFailedRetain    = 7 * 24 * time.Hour // dead-lettered 行保留期（镜像 eventOutboxFailedRetain）
  webhookPruneEveryTicks = 60                 // 每 60 个 15s tick = 15min 一次（镜像 eventOutboxPruneEveryRounds=60）
  ```

  镜像对象语义一一对应：outbox `delivered` ↔ webhook `succeeded`；outbox `failed`（终态）↔ webhook `dead_lettered`（终态）。
- **约束 b（周期触发）：** `RetryLoop`（:174-190）增加 tick 计数器：每 `webhookPruneEveryTicks` 个 tick 调用一次 `w.prune()`（镜像 relay:138-139 的 `rounds%…==0` 模式）；`w.repo == nil` 早退不变（:176-177），prune 随同受保护——**未配置 retry store 时零行为变化**（I5 基线保护）。
- **约束 c（prune 方法）：** `func (w *Webhook) prune()`——`now := time.Now().UTC()`；调 `w.repo.PruneWebhookFailures(ctx, now.Add(-webhookDeliveredRetain), now.Add(-webhookFailedRetain))`；错误 `w.logger.Warn("webhook prune failed", "err", err)` 后**返回不中断循环**（镜像 relay:384-388）；零行删除静默成功（无日志、无计数）。
- **约束 d（有效保留期语义）：** 行删除时间 ∈ `[horizon, horizon + 15min]`（一个 prune 间隔的抖动）——与 `docs/configuration.md:361` 对 outbox 的既有表述同型，无需文档变更（无新配置）。
- **约束 e（可测性）：** `Webhook` 增加**未导出字段 `retryInterval time.Duration`**（默认 15s，`RetryLoop` 的 ticker 改读该字段；生产行为不变）。`internal/events` 测试为 package `events` 白盒（E14），可注入毫秒级间隔驱动 RetryLoop 真实循环完成 AC-2。
- **约束 f（不新增配置项）：** 保留期走常量（与 outbox 的 `EVENT_OUTBOX_*_RETENTION_HOURS` 不同，webhook 不新增 env）——方向验收未要求，常量是回退路径的自然形态（镜像 relay:98-102 的 `opts.X <= 0 → 默认` 逻辑），后续如需可配置化无需 schema/契约变更（D1）。

### FR-3：安全不变量

| 规则 | 依据 |
|------|------|
| 只删终态行（succeeded/dead-lettered），pending 永不删 | E12（与 `NextPendingFailures` 谓词互补，不丢 at-least-once 投递） |
| DELETE-style，无墓碑、无软删标志 | 镜像 outbox "DELETE-style, no tombstones"（relay:375-377）；表内无其他消费者依赖旧行（E17） |
| 零值时间拒绝（error）而非全表清空 | 镜像 event_outbox.go:394-396（E7d） |
| 失败仅 warn log，不中断 RetryLoop | 镜像 relay:384-388；与 webhook 既有错误处理风格一致 |
| 无迁移、无索引变更、无 admin API 变更、无 main.go/workers.go 变更 | E9/E16/E17/E8（`ListWebhookFailures` 列表自动变短） |
| 不新增 telemetry 计数器（默认） | D3；既有 4 个 webhook 计数器不动（E15） |

---

## 4. 验收标准（方向验收 1-3 全部保留并测试化）

### AC-1 — `PruneWebhookFailures` 单元测试（对应方向验收 1）

`internal/repository/webhook_failures_test.go`（package `repository_test`），复用 `openWebhookRepo`/`recordWebhook`（:24-45）+ **新增回拨助手** `backdateWebhookCreatedAt(t, dsn, id, when)`——镜像 `backdateEventOutboxAt`（E13，raw `sql.Open` 打开同一 dsn，`UPDATE webhook_failures SET created_at=$1 WHERE id=$2`，**写入 `when.UTC().Format(time.RFC3339Nano)`**，与生产格式同族可精确比较，E10）。测试用显式 dsn 建仓（`"file:"+filepath.Join(t.TempDir(),"wh.db")`）以便助手连接。

测试 `TestPruneWebhookFailures` 断言序列（全部可确定复现）：

1. **succeeded 超期删除：** seed 一行 → `MarkWebhookSucceeded` → 回拨 created_at 至 `now-3h` → `PruneWebhookFailures(ctx, now.Add(-1*time.Hour), now.Add(-7*24*time.Hour))` → 返回 `n==1`，`ListWebhookFailures` 中该行消失。
2. **succeeded 未超期保留：** seed 一行 → `MarkWebhookSucceeded` → created_at = `now-30min` → 同上调用 → 返回 `n==0`，行仍在。
3. **dead-lettered 超期删除 / pending 保留：** seed 两行 → 一行 `MarkWebhookDeadLettered` 且回拨 8 天、一行保持双 0（pending）→ 同上调用 → `n==1`，仅 dead-lettered 行消失，**pending 行仍在**。
4. **幂等：** 对同一仓库再次调用同一截止值 → `n==0`，无 error。
5. **全删路径：** `PruneWebhookFailures(ctx, time.Now(), time.Now())` → 清空全部终态行、保留 pending。
6. **零值保护：** `PruneWebhookFailures(ctx, time.Time{}, time.Now())` → **error 非 nil**，且无行被删。

（注：回拨助手落在 `repository_test` 包内，与既有 `recordWebhook` 风格一致；不得依赖 15s ticker。）

### AC-2 — RetryLoop 周期 prune + 零行 no-op（对应方向验收 2）

`internal/events`（package `events` 白盒，E14）。**选 RetryLoop 分支**（非 reconcile hook，理由见 D4）：

1. **周期触发 + 常量钉死**（`TestWebhook_RetryLoopPrunes`）：`wh := NewWebhook("http://127.0.0.1:1", logger).WithRetryStore(repo); wh.retryInterval = 10*time.Millisecond`（ticker 由 FR-2e 字段注入，默认 15s 生产不变）。seed 两行 succeeded：created_at 分别回拨 **25h（>24h 默认保留期）** 与 **23h（<24h）**。`go wh.RetryLoop(ctx)`，轮询等待至多 3s（= 60 ticks × 10ms + 裕量，`webhookPruneEveryTicks=60`）：断言 25h 行被删、23h 行保留、`NextPendingFailures` 无异常——**同一测试同时钉死周期触发与 24h 默认常量**（镜像 `TestOutboxRelay_PruneUsesConfiguredRetention` 的"horizon 之外删/之内留"判定法，E13）。
2. **零行 prune 是 no-op**（`TestWebhook_Prune_Noop`）：空表直接调 `wh.prune()`（或跑一轮短 ticker 循环）→ 无 error、无 panic、表仍空——幂等 + 空操作路径。
3. 补充：`TestWebhook_Prune_RepoNilNoop`——`NewWebhook` 未 `WithRetryStore` 时 `RetryLoop` 早退（:176-177）行为不变（回归锁，I5）。

### AC-3 — 门禁（对应方向验收 3）

```bash
go test ./internal/repository -run Webhook      # 含新 TestPruneWebhookFailures
go test ./internal/events -run 'TestWebhook_RetryLoopPrunes|TestWebhook_Prune'
gofmt -l internal/repository internal/events     # 期望无输出（make check 同款）
go vet ./internal/repository ./internal/events   # 期望无输出
```

---

## 5. 范围边界（明确不做）与决策记录

| 不做 / 决策 | 理由 |
|------|------|
| **D1：不新增 env 配置**（如 `EVENTS_WEBHOOK_DELIVERED_RETENTION_HOURS`） | 方向验收未要求；outbox 的 env 是既有设施（docs/configuration.md:361-362），webhook 用常量 + 15min 周期即可收敛。常量即未来配置化的回退路径（镜像 relay:98-102），后续加配置无需 schema/契约变更 |
| **D2：新增未导出字段 `retryInterval`（默认 15s）** | AC-2 要求"断言 RetryLoop 周期 prune"，而 ticker 硬编码 15s（:178）不可等待。字段是测试注入的最小侵入（无公开 API 变更，I6）；outbox relay 的 PollInterval 是既有 options 字段，其测试直调 `prune()` 绕过了循环——本方向验收明确要循环级断言，故字段注入优于"只测方法" |
| **D3：不新增 telemetry 计数器**（默认否决 `IncWebhookPruned`） | 方向验收未要求；outbox 的 `IncEventOutboxPruned`（E15）是既有设施的镜像参照而非本方向交付物。如需可在后续 PR 加（metrics.go:36-39 追加一个 Int64Counter），不阻塞本方向 |
| **D4：选 RetryLoop 周期 prune，否决 reconcile hook**（方向验收 2 的 "or" 分支） | reconcile 包从不触碰 events 表（E17 的 grep 结果）；RetryLoop 已有 15s 循环 + repo 依赖已注入（workers.go:79），在自身循环内 prune 是**零新装配**的镜像（outbox relay 同样在自身 Run 循环内 prune，E7b） |
| **D5：行号漂移处理** | 方向引用 `event_outbox_relay.go:239`（prune）实际在 :379，周期调用 :139，常量 :67-69——符号、语义、模式全部存在，仅行号偏移；本规格以 :379-391 为准（E4），不影响方向结论 |
| 不做：schema 迁移 / 索引变更 | 两列双方言已存在（E9）；现有索引前导列 `succeeded` 可部分利用，15min 一次的保留 job 扫描可接受（E16）；I2 零触碰 |
| 不做：admin API / `ListWebhookFailures` 变更 | 不需要——prune 后列表自然变短（E17） |
| 不做：webhook 重试策略本身 | 10 次上限、指数退避、DLQ 标志（webhook.go:214-222）均不动，只加清理 |
| 不做：方向 1（Notifier 持久化/at-least-once）与方向 3（pg_notify 8000 字节） | 属分析文件独立方向，与 retention 正交；AGENTS/README 未要求合并 |
| 不做：`docs/configuration.md` 变更 | 无新配置、无行为契约变化（有效保留期语义与 outbox 表述同型，FR-2d）；`CHANGELOG`/`ROADMAP` 等分析文档非契约 |

**proposed_vs_verified 对照：** verified——insert-only 无 upsert（E1）、双 Mark 仅置位（E2/E3）、接口无 prune（E5）、RetryLoop 只轮询（E6）、表名仅两文件引用（E17）、outbox 镜像模式全套存在（E7，行号修正见 E4/D5）；proposed——`PruneWebhookFailures` 双方言 SQL + 零值保护 + RowsAffected 计数（FR-1，签名镜像 E7d/E18）、常量 24h/7d + 每 60 tick（FR-2a/b，镜像 E7a/b）、`retryInterval` 测试字段（FR-2e，E14 驱动）、无配置/无 telemetry/无 reconcile hook 三个范围决策（D1/D3/D4）。

---

## 6. 实现指引（供验收后落地，非本规格交付物）

1. **`internal/repository/repository_interface.go`**：Webhooks 段（:133-139）末尾加 `PruneWebhookFailures(ctx context.Context, succeededBefore, deadLetteredBefore time.Time) (int64, error)`。
2. **`internal/repository/webhook_failures.go`**：文件末尾实现——双方言分支 DELETE（FR-1a，dialect 分支风格照抄 :60-69 的 `NextPendingFailures`）、零值 error（FR-1c）、`RowsAffected` 求和（FR-1d）、RFC3339Nano 截止值（FR-1a）。
3. **`internal/events/webhook.go`**：三个常量（FR-2a，置于文件顶部常量区，紧邻 `jitter` 上方或 RetryLoop 上方）；`Webhook` struct 加 `retryInterval time.Duration` 字段并在 `NewWebhook` 默认 15s（FR-2e）；`RetryLoop` 加 tick 计数器 + 周期调用（FR-2b，模式照抄 relay:138-139）；新增 `prune()` 方法（FR-2c，模式照抄 relay:379-391 的 warn-log 结构）。
4. **测试**：`internal/repository/webhook_failures_test.go` 加 `backdateWebhookCreatedAt` 助手 + `TestPruneWebhookFailures`（AC-1）；`internal/events/` 加 `TestWebhook_RetryLoopPrunes` / `TestWebhook_Prune_Noop` / `TestWebhook_Prune_RepoNilNoop`（AC-2）。回拨助手镜像 `backdateEventOutboxAt`（events 测试 :90-101）的 raw-sqlite 形态。
5. **验证**：AC-3 四条命令 + `make check`。
