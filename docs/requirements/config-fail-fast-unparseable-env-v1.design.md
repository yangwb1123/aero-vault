# 设计：typed env 值解析失败即启动报错（fail fast）——`(T, error)` 契约贯通

> **配套规格：** `docs/requirements/config-fail-fast-unparseable-env-v1.md`（FR-1…FR-5 / AC-1…AC-4）· **模块：** `internal/config` · **基线：** 工作树 @ HEAD `acfaaf4`（含在途未提交改动，见 §1.2）· **门禁：** `make check` 全绿 · 生产文件 ≤ 500 行 · 纯 stdlib（I6）· I1/I2/I3/I4 不涉及（无 SQL、无迁移、无中间件链、无存储 key 语义变更）

---

## 1. 证据复核（untrusted claims → 逐条独立验证）

规格 E1–E13 全部对工作树复核通过，行号零漂移：

| # | 复核结论 |
|---|---------|
| E1–E3 | `config.go:307/:319/:367` — `getEnvBool`/`getEnvFloat`/`getEnvInt` 吞 `strconv` 错误返回默认值，与规格逐字一致（:314-316 / :326-328 / :374-376） |
| E4 | `Load()` 入口 :51；`parseLogLevel` :379-392 是既有 fail-fast 先例；`TestLoad_InvalidLogLevel`（config_test.go:498）钉死该契约 |
| E5 | `config_test.go:48-49`（bool "notabool"/"yepnope"）、`:79`（int "12.5"/"abc"）、`:109`（float "x"）——fail-open 被钉为"正确"行为 |
| E6 | `main.go:39-42` `fatal:` + `os.Exit(1)`；`run()` :46 与 `runMCP()` :171 均 `config.Load()` 包装返回；mcp 分支 `main.go:29-33` 打印 `mcp: %v` 后 `os.Exit(1)` → **main.go 零改动** |
| E7 | typed 读取全包内：config.go 69（28 bool + 31 int + 10 float）、config_billing.go 7（2+5）、config_audit_governance.go 12（1+11）、config_event_outbox.go 8（1+7）、config_audit_sink_l2.go 0；全仓 `grep -rn getEnvBool|getEnvInt|getEnvFloat` **零外部调用者** |
| E8 | `loadEventOutboxConfig()`（config_event_outbox.go:25-37）当前不返回 error；调用点 config.go:227 |
| E9 | `docs/configuration.md:12-13` 记载 fail-open；:16 "Validation (fails fast on startup)" 段落为新增文本落点 |
| E10 | `strconv` 值集实证（Go 1.26）：`ParseBool("yes")` err、`Atoi("30m")` err、`ParseFloat("100USD")` err → 五个方向示例全部真实不可解析 |
| E11 | `clearEnv`（config_test.go:351+）用 `t.Setenv(k, "")` 清场 → 空串=未设置=默认的语义是全部 Load 测试的回归锁 |
| E12 | unset/empty 用默认（:41-43/:75-76/:105-106）与合法值解析（:44-47/:77-78/:107-108）用例，断言形态需从单值改双值 |
| E13 | `slog` logger 在 `Load()` 成功后才创建（main.go:50-51）→ "startup warning" 方案无 logger 可用，D1 硬证据 |

### 1.1 在途工作树差异（新证据，设计锚点以工作树为准）

| 证据 | 内容 | 设计影响 |
|------|------|---------|
| W1 | 工作树 `config.go` 较 HEAD 增加 `Access.DeleteFailClosed = getEnvBool("ACCESS_DELETE_FAIL_CLOSED", true)`（在途 access 方向）；E7 的 69 已含此字段 | 行号/计数以工作树为准，无额外影响 |
| W2 | **工作树含 outbox 方向已实现但未提交的改动**：`config_event_outbox.go` 109 行（+`Enabled`/`DeliveredRetentionHours`/`FailedRetentionHours` + `withDefaults`/`Validate` 扩展），且 `config_event_outbox_test.go:283-316` 存在 `TestEventOutboxLoad_UnparseableEnvFallsBackToDefault`——**把 `EVENT_OUTBOX_ENABLED="tru"` 回退默认 true、`EVENT_OUTBOX_DELIVERED_RETENTION_HOURS="abc"` 回退 24 钉为正确行为** | 与 FR-2 冲突：该测试必须翻转（见 §2 D5、§7 AC-3b）；"0"→disabled 子测试是合法值控制，保留 |
| W3 | 全仓 `_test.go` **零调用 `config.Load()`**（`grep -rln "config.Load()" --include="*_test.go"` 为空）；生产调用仅 main.go:46/:171 | 测试爆炸半径仅限 `internal/config` 包内 |
| W4 | `make check` = `fmt vet vet-integration build test cli-check`（Makefile:113）；`engineering.yaml` filesize `max_lines: 500` 且 `ignore_patterns` 含 `_test.go`（测试文件豁免 500 行门）；`complexity.max_cyclomatic: 10` 为 WARN 级（AGENTS §0），exempt 列表不含 `Load` | 行数预算仅约束生产文件；`Load()` 圈复杂度 WARN 属既有状态，接受 |

---

## 2. 前置尝试处置（previous attempts — 逐项给出裁决与证据）

### 2.1 本管道自身（`docs/auto/runs/fail-fast-on-unparseable-typed-env-values-instea-31391fd7/`）
`DECISIONS.md` 仅一条：requirements 阶段 PASS（2026-08-06 19:55:34）。**无 design-gate 判定**（本设计即该管道首个设计产物）。规格自身的决策点全部继承并落实：
- **D1（否决 startup warning 分支）**：采纳，证据 E13（Load 时无 logger）+ 方向标题即 "fail fast"；本设计不提供任何 warning 通道，错误唯一出口为 `Load()` error → `fatal:`/`mcp:` stderr + exit(1)。
- **FR-1c 错误文本格式**：`invalid value %q for %s: %v`，使验收可用 `strings.Contains(err.Error(), key)` 稳定锁定（`load config: %w` 外层包装不破坏 Contains，E6）。

### 2.2 兄弟管道 `add-a-dedicated-durable-async-outbox-config-sect-ef2d0976`（同一模块的相邻方向）
其 design_gate **PASS**（无 blocking findings），但交付物与本方向**直接冲突**，本设计明确裁决：
- 该方向设计文档 `docs/requirements/internal-config-durable-async-outbox.design.md` 的 **F2/F11 行**（:223-242）与已实现测试 `TestEventOutboxLoad_UnparseableEnvFallsBackToDefault`（config_event_outbox_test.go:283-316）把 "unparseable ⇒ fallback to default" 钉为契约（针对 `EVENT_OUTBOX_ENABLED`、`EVENT_OUTBOX_DELIVERED_RETENTION_HOURS`）。
- **裁决：被本方向全局性地取代。** 该测试是在"全局 typed env 吞错"旧契约下写的；本方向（规格 FR-1/FR-3）把**所有** typed env 解析失败改为启动错误，无逐变量例外（例外会复活吞错类缺陷）。该测试两个子测试翻转、一个保留（§7 AC-3b 给出最终代码）。
- **无默认值冲突**：outbox 的 "correct polarity" 论点针对的是默认值 `true`（kill-switch 默认开），本方向不改变任何默认值，仅改变解析失败路径 → 极性决策原样保留。
- 其 gate 中非阻断项 "getEnvBool consistency across ~20 call sites" 被本方向按构造解决（所有调用点统一走同一 `(T, error)` 契约）。
- 该设计文档为历史快照，不改写；强制执行点是工作树的测试翻转 + 本设计 §7。

### 2.3 兄弟方向 `cli-reject-nonnumeric-config-values-v1.design.md`（设计态、未实现）
- 模块 `internal/cli`（Sscanf CLI 参数），与本方向（`internal/config` env 解析）**零代码交集**（无共享文件/符号）。
- 其 N1 发现（`ParseFloat("NaN")` 返回 err=nil）与本方向无关——NaN/±Inf 属分析方向 2，规格 §5 已明确排除。裁决：**不动作**，在 §8 范围边界中登记。

---

## 3. 设计决策（含规格歧义消解）

| # | 决策 | 依据 |
|---|------|------|
| D1 | helper 签名 `(T, error)`；解析失败返回 `(def, error)`，错误文本 `invalid value %q for %s: %v`（点名变量 + 原始值） | FR-1（规格原文契约） |
| D2 | **first-error-wins = 逐字段赋值 + 立即早退**（每字段 2 行：inline-if 赋值 + `return nil, err`）。消解 FR-2c "末尾单次 `if err != nil`" 的歧义：若每个 loader 末尾才检查一次，`err` 被逐字段覆盖，语义变为 **last-error-wins**，直接违背 FR-2b（"首个解析错误即向上传播"，且与 parseLogLevel/loadBillingConfig 既有"遇错即返"先例一致） | FR-2b 为规范条款；错误顺序确定、单变量文本可测 |
| D3 | `Load()` 重构形态：**字符串字段留在 struct literal，typed 字段移出为逐字段赋值**，顺序严格保持当前 literal 源序（§6 给出首段规范形态）；`EventOutbox` 字段改为 `cfg.EventOutbox, err = loadEventOutboxConfig()` | FR-2c；最小 diff、gofmt 稳定、行数预算安全（§9） |
| D4 | 子 loader 同型转换：billing（7 字段）、governance（12 字段）、event_outbox（8 字段，**签名改 `(EventOutboxConfig, error)`**）；`loadAuditSinkL2Config` 已返回 error 且 typed 读取为 0 → **零改动**（仅复核） | FR-2a/E8；已核 config_audit_sink_l2.go:37 签名 |
| D5 | 测试面：3 张 helper 表加 `wantErr`（AC-3）+ 新增 2 个 Load 级测试（AC-1/AC-2）+ **翻转 outbox fail-open 钉（W2，AC-3b）** + 3 个设计级测试（§7 T-D1..T-D3） | FR-4；W2 是本规格未预见的第 4 处 fail-open 钉，必须同步处置 |
| D6 | 文档：`docs/configuration.md:12-13` 改写 + 并入 :16 Validation 段落（FR-5 文本原样采纳）；`.env.example` 无 "falls back" 表述（grep 实证），不改 | FR-5 |
| D7 | 接受 WARN：`Load()` 单函数长度/圈复杂度超过约定阈值（今日已如此：:51-:233 约 180 行）——约定仅告警不阻断（AGENTS §0），重构为逐字段赋值不新增净分支 | 门禁表（AGENTS §0） |

---

## 4. API 变更

```go
// internal/config/config.go — 包私有签名变更（无公开 API 变化）
func getEnvBool(key string, def bool) (bool, error)
func getEnvInt(key string, def int) (int, error)
func getEnvFloat(key string, def float64) (float64, error)
func getEnv(key, def string) string                    // 不变（FR-1d）
// internal/config/config_event_outbox.go
func loadEventOutboxConfig() (EventOutboxConfig, error) // 唯一跨签名涟漪（E8）
```

- **公开 API 零变化**：`Load() (*Config, error)`、`Config` 结构体、全部 env 变量名/默认值不变；`cmd/server/main.go`、`internal/cli`、其余包零改动（E6/W3 实证）。
- **行为变化**（唯一变化点）：非空但不可解析的 typed env 值 → `Load()` 返回 error（文本点名变量+值）→ 服务模式 `fatal: load config: invalid value "yes" for APP_TLS_ENABLED: strconv.ParseBool: parsing "yes": invalid syntax` + exit(1)；MCP 模式 `mcp: <同文本>` + exit(1)。
- 最终 helper 形态（三个 helper 同构）：

```go
func getEnvBool(key string, def bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil // 空串=未设置=默认（E11 回归锁，语义冻结）
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, fmt.Errorf("invalid value %q for %s: %v", v, key, err)
	}
	return b, nil
}
// getEnvInt/getEnvFloat 同构：strconv.Atoi / strconv.ParseFloat(v, 64)
```

---

## 5. 兼容性约束

| # | 约束 | 证据 |
|---|------|------|
| C1 | 未设置/空串 → `(def, nil)`，语义**冻结**（`clearEnv` 的 `t.Setenv(k,"")` 基建依赖） | E11/E12；config_test.go:351+ |
| C2 | 合法值集不变：`ParseBool` 的 `1/0/t/f/true/false/TRUE/FALSE/…`、十进制整数（含 `+7`、`-3`）、`ParseFloat` 全格式（含 `1e3`）照常解析 | E10；`docs/configuration.md:12` 清单仍有效 |
| C3 | `getEnv`（string）与 `splitCSV`/`splitMapping`/`reconcileTenants` 零改动 | FR-1d |
| C4 | `cfg.Validate()` 业务约束（backend/driver/AI 联动等）零改动，解析成功后才执行；**解析错误先于校验错误返回** | FR-2d；config.go:233-235 |
| C5 | 此前能正常启动的配置（全部值可解析）→ `Config` 输出**逐字段一致**，行为零回归 | 重构为机械等价变换 |
| C6 | CLI 子命令不受影响（`cli.Run` 不调用 `config.Load()`，grep 实证） | W3 |
| C7 | 包外测试零影响（无 `_test.go` 调用 `config.Load()`） | W3 |
| C8 | 默认值全部不变——含 outbox `EVENT_OUTBOX_ENABLED` 默认 true（kill-switch 极性决策原样保留） | §2.2 |
| C9 | 已知边界（**有意排除**，属分析方向 2）：`ParseFloat("NaN"/"+Inf"/"-Inf")` 返回 err=nil → 本方向不拦截，`AI_TENANT_DAILY_BUDGET_USD=NaN` 仍可启动 | 规格 §5；与 cli 兄弟 N1 同源 |
| C10 | 新增的失败类：**带空白/进制前缀/尾垃圾的值**（`" 42"`、`"0x10"`、`"10abc"`）此前静默回退默认，现为启动错误——属本方向意图（不可解析即失败），迁移见 §6 | E10 值集 |
| C11 | `.env` 文件 `godotenv.Load()` 的语法错误仍被忽略（`_ =`），不在本方向范围 | config.go:52 |

---

## 6. 迁移步骤（实现顺序）

1. **`internal/config/config.go`** — helper 签名+body（§4 形态）；`Load()` 重构：
   - 字符串字段留在 literal；typed 字段按当前源序逐字段赋值（规范形态）：

```go
	cfg := &Config{
		App: AppConfig{
			Addr:        getEnv("APP_ADDR", ":8080"),
			TLSCertFile: getEnv("APP_TLS_CERT_FILE", ""),
			TLSKeyFile:  getEnv("APP_TLS_KEY_FILE", ""),
		},
		// …其余字符串字段原样留在 literal…
	}
	// typed 字段逐字段赋值（顺序 = 现 literal 字段序）：
	if cfg.App.TLSEnabled, err = getEnvBool("APP_TLS_ENABLED", false); err != nil {
		return nil, err
	}
	if cfg.App.WriteTimeoutSec, err = getEnvInt("APP_WRITE_TIMEOUT", 60); err != nil {
		return nil, err
	}
	// …69 处，含包壳特例：
	var verifyMaxSize int
	if verifyMaxSize, err = getEnvInt("STORAGE_VERIFY_MAX_SIZE", 10*1024*1024); err != nil {
		return nil, err
	}
	cfg.Storage.VerifyMaxSize = int64(verifyMaxSize)
	// …
	if cfg.EventOutbox, err = loadEventOutboxConfig(); err != nil {
		return nil, err
	}
	// …
	cfg.Reconcile.UploadGCEnable = cfg.Reconcile.UploadGCHours > 0 // 后处理不变
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
```

   - 错误顺序（first-error-wins，确定性，可测）：`APP_LOG_LEVEL`(parseLogLevel) → billing(7) → governance(12) → sink_l2(0) → **App(7) → Storage(12) → Events(1) → AI(17) → Auth(4) → Access(2) → Telemetry(1) → EVENT_OUTBOX_(8) → RateLimit(6) → Reconcile(9) → Jobs(2) → Antivirus(2) → Replication(2) → WebUI(1)**（= 现 literal 求值序）。
2. **`config_billing.go`**（:44-69）/ **`config_audit_governance.go`**（:53-）— 同型转换：字符串字段留 literal，`var err error` + 逐字段 inline-if（7/12 处）。
3. **`config_event_outbox.go`**（:25-37）— 签名改 `(EventOutboxConfig, error)` + 同型转换（8 处）+ 调用点 config.go:227（步骤 1 已含）。
4. **`config_audit_sink_l2.go`** — 零改动（签名已带 error、typed 读取 0，复核即可）。
5. **测试** — §7 全部用例；`clearEnv` 与 unset/empty 用例零改动（FR-4）。
6. **`docs/configuration.md`** — :12-13 原文替换为（FR-5 文本）："…An unparseable value falls back to the default." → "**A non-empty unparseable value fails startup** with an error naming the variable (same for integer and float variables)."，并入 :16 Validation 段落。
7. **门禁** — `make check`（= `fmt vet vet-integration build test cli-check`，Makefile:113）。
8. **运维投放说明**（无代码动作）：含不可解析值的既有 `.env` 将在下次启动失败并点名变量——修复或删除该行即可；无数据迁移、无 schema 变更（I2）、无状态回滚（纯配置层行为，回退=回滚提交）。

---

## 7. 验收映射（可测试）

方向 4 条验收 → 规格 AC-1..AC-4 → 设计测试，1:1 无漂移；"or emits a startup warning" 分支按 D1 否决（证据 E13）。

| 验收 | 测试 | 位置 |
|------|------|------|
| AC-1 `APP_TLS_ENABLED=yes` → Load() 报错点名 | `TestLoad_UnparseableBoolFailsStartup` | config_test.go（新增） |
| AC-2 `AI_DEGRADED_MODE`/`RECONCILE_INTERVAL_MINUTES` 同理 | `TestLoad_UnparseableTypedEnvFailsStartup`（表驱动） | config_test.go（新增） |
| AC-3 既有三表改为新契约 | 三表加 `wantErr` | config_test.go:33/:67/:97 |
| AC-4 门禁 | `go test ./internal/config/` · `gofmt -l internal/config/` 无输出 · `go vet ./internal/config/` 无输出 · `make check` | 仓库根 |
| FR-2 贯通（billing/governance/outbox 子 loader） | AC-2 表含 `EVENT_OUTBOX_ENABLED` 行；T-D1 表含 `BILLING_ENABLED`/`AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS` 行 | 新增 |
| FR-1c 错误文本点名变量+值 | AC-1/AC-2 断言 `strings.Contains(err, key)` 与 `strings.Contains(err, val)` | 新增 |
| FR-4 空串/未设置回归锁 | 既有 unset/empty 行零改动继续通过；`clearEnv` 零改动 | 既有 |
| W2 outbox fail-open 钉取代 | `TestEventOutboxLoad_UnparseableEnvFailsStartup`（翻转 2 子测试、保留 "0" 控制） | config_event_outbox_test.go:283-316 |
| 设计级：first-error-wins 确定性 | `TestLoad_FirstParseErrorWins`（App 段先于 AI 段） | 新增 |
| 设计级：`int64` 包壳路径 | `TestLoad_VerifyMaxSizeParseError` | 新增 |

### 测试最终形态（规范性代码）

**AC-1**（config_test.go 新增）：

```go
func TestLoad_UnparseableBoolFailsStartup(t *testing.T) {
	clearEnv(t)
	t.Setenv("APP_TLS_ENABLED", "yes") // "yes" ∉ ParseBool 值集（E10）
	_, err := Load()
	if err == nil {
		t.Fatal("Load() must fail on APP_TLS_ENABLED=yes")
	}
	if !strings.Contains(err.Error(), "APP_TLS_ENABLED") {
		t.Fatalf("error must name the variable, got: %v", err)
	}
	if !strings.Contains(err.Error(), "yes") {
		t.Fatalf("error must include the offending value, got: %v", err)
	}
	// 反向锁定：合法值照常生效
	clearEnv(t)
	t.Setenv("APP_TLS_ENABLED", "true")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("valid value must load: %v", err)
	}
	if !cfg.App.TLSEnabled {
		t.Fatal("APP_TLS_ENABLED=true must yield TLSEnabled=true")
	}
}
```

**AC-2**（表驱动，含 FR-2 贯通行与 float/int/bool 三型）：

```go
func TestLoad_UnparseableTypedEnvFailsStartup(t *testing.T) {
	cases := []struct{ name, key, val string }{
		{"AI_DEGRADED_MODE bool", "AI_DEGRADED_MODE", "yes"},
		{"RECONCILE_INTERVAL_MINUTES int", "RECONCILE_INTERVAL_MINUTES", "30m"},
		{"STORAGE_VERIFY_ON_READ bool", "STORAGE_VERIFY_ON_READ", "yes"},
		{"AI_TENANT_DAILY_BUDGET_USD float", "AI_TENANT_DAILY_BUDGET_USD", "100USD"},
		{"EVENT_OUTBOX_ENABLED sub-loader bool", "EVENT_OUTBOX_ENABLED", "yes"}, // FR-2 E8
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(c.key, c.val)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() must fail on %s=%s", c.key, c.val)
			} else if !strings.Contains(err.Error(), c.key) {
				t.Fatalf("error must name %s, got: %v", c.key, err)
			}
		})
	}
}
```

**AC-3**（三表加 `wantErr`；unset/empty/合法行仅改双值接收，语义断言不变——FR-4）：

```go
// TestGetEnvBool（config_test.go:33 起）：struct 增加 wantErr bool
//   {"unparseable uses default", true, "notabool", true, true}
//   {"unparseable uses default false", true, "yepnope", false, false}
// → 改为：
//   {"unparseable returns error", true, "notabool", true, true, true},
//   {"unparseable returns error false", true, "yepnope", false, false, true},
//   其余行 wantErr=false；断言体：got, err := getEnvBool(key, c.def)
//   if err != nil { …wantErr=false 行必须 err==nil… } else if got != c.want { … }
// TestGetEnvInt（:67 起）：{"12.5",9} 与 {"abc",9} → wantErr=true
// TestGetEnvFloat（:97 起）：{"x",2.0} → wantErr=true
```

**AC-3b — outbox fail-open 钉翻转（W2，取代 config_event_outbox_test.go:285-316）**：

```go
func TestEventOutboxLoad_UnparseableEnvFailsStartup(t *testing.T) {
	t.Run("enabled parse error fails startup", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("EVENT_OUTBOX_ENABLED", "tru") // genuine parse error
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "EVENT_OUTBOX_ENABLED") {
			t.Fatalf("Load() must fail naming EVENT_OUTBOX_ENABLED, got %v", err)
		}
	})
	t.Run("parseable 0 disables", func(t *testing.T) { // 合法值控制，保留
		clearEnv(t)
		t.Setenv("EVENT_OUTBOX_ENABLED", "0")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if cfg.EventOutbox.Enabled {
			t.Fatal("EVENT_OUTBOX_ENABLED=0 must disable the relay")
		}
	})
	t.Run("non-numeric retention fails startup", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("EVENT_OUTBOX_DELIVERED_RETENTION_HOURS", "abc")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "EVENT_OUTBOX_DELIVERED_RETENTION_HOURS") {
			t.Fatalf("Load() must fail naming EVENT_OUTBOX_DELIVERED_RETENTION_HOURS, got %v", err)
		}
	})
}
```

**设计级 T-D1（子 loader 贯通）、T-D2（int64 包壳）、T-D3（first-error-wins 确定性）**：

```go
func TestLoad_SubLoaderParseErrorThreads(t *testing.T) { // T-D1: FR-2a 全 loader 贯通
	cases := []struct{ name, key, val string }{
		{"billing bool", "BILLING_ENABLED", "maybe"},
		{"governance int", "AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS", "fast"},
		{"outbox int", "EVENT_OUTBOX_BATCH_SIZE", "many"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(c.key, c.val)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), c.key) {
				t.Fatalf("Load() must fail naming %s, got %v", c.key, err)
			}
		})
	}
}

func TestLoad_VerifyMaxSizeParseError(t *testing.T) { // T-D2: int64 包壳路径
	clearEnv(t)
	t.Setenv("STORAGE_VERIFY_MAX_SIZE", "big")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "STORAGE_VERIFY_MAX_SIZE") {
		t.Fatalf("Load() must fail naming STORAGE_VERIFY_MAX_SIZE, got %v", err)
	}
}

func TestLoad_FirstParseErrorWins(t *testing.T) { // T-D3: 错误顺序确定性
	clearEnv(t)
	t.Setenv("APP_TLS_ENABLED", "yes")  // App 段（早）
	t.Setenv("AI_DEGRADED_MODE", "yes") // AI 段（晚）
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "APP_TLS_ENABLED") {
		t.Fatalf("first error in Load order must win, got: %v", err)
	}
	if strings.Contains(err.Error(), "AI_DEGRADED_MODE") {
		t.Fatalf("later error must not be reported, got: %v", err)
	}
}
```

**AC-4 门禁**（仓库根执行）：`go test ./internal/config/` · `gofmt -l internal/config/`（期望无输出）· `go vet ./internal/config/`（期望无输出）· `make check`。

---

## 8. 失败模式与处置

| 模式 | 表现 | 处置 |
|------|------|------|
| FM-1 任一 typed env 不可解析 | 进程拒绝启动：stderr `fatal: load config: invalid value %q for %s: …`（MCP：`mcp: …`），exit(1)；配置永不部分生效 | 本方向目标行为；错误点名变量+值，运维可直接修复 |
| FM-2 多个变量同时坏 | 每次启动只报**第一个**（§6 顺序确定），修复后暴露下一个 | 有意设计（first-error-wins，FR-2b）；单变量文本保证断言稳定 |
| FM-3 曾依赖 fail-open 的部署（如 `RECONCILE_INTERVAL_MINUTES=30m` 静默禁用对账） | 启动失败而非静默降级 | 意图行为；迁移=修正/删除该行（§6.8），文档已同步（FR-5） |
| FM-4 值带空白/进制前缀/尾垃圾（`" 42"`/`"0x10"`/`"10abc"`） | 从"静默默认"变为启动错误（C10） | 同 FM-3；值集收窄是方向意图 |
| FM-5 Load 时刻无 logger | 配置错误只走 stderr 明文，无结构化日志 | 接受（E13）；`fatal:` 路径已存在（E6） |
| FM-6 常驻编排（systemd/docker restart） | 坏值导致每次重启即失败（crash-loop），但每次 stderr 均点名变量 | 首启即可见；无数据面影响（config 加载先于任何存储/网络初始化） |
| FM-7 回滚 | 纯配置层行为变更，无数据/schema 状态 → 回滚提交即恢复 fail-open | 无迁移脚本（I2 不涉及） |
| FM-8 NaN/±Inf 仍放行 | `AI_TENANT_DAILY_BUDGET_USD=NaN` 照常启动（ParseFloat err=nil） | 已知边界，属分析方向 2（规格 §5 明确排除）；不得在本方向内"顺手"修复（范围纪律） |

---

## 9. 门禁与行数预算

| 文件 | 当前行数 | 变更 | 预算后 | ≤500 |
|------|---------|------|--------|------|
| config.go | 392 | 66 typed 调用点 ×（2 行赋值 − 1 行 literal）= +66；`VerifyMaxSize` 包壳 +1；`EventOutbox` 调用 +1；杂项 +2 | ≈ **462** | ✅（余量 ~38） |
| config_billing.go | 187 | +7 | ≈ 194 | ✅ |
| config_audit_governance.go | 303 | +12 | ≈ 315 | ✅ |
| config_event_outbox.go | 109 | +8（+签名） | ≈ 117 | ✅ |
| config_audit_sink_l2.go | 166 | 0 | 166 | ✅ |
| config_test.go / config_event_outbox_test.go | 513 / 336 | +新增测试 | — | 豁免（`engineering.yaml` `ignore_patterns: ["_test.go"]`，W4 实证） |

- `make check` = `fmt vet vet-integration build test cli-check`（Makefile:113）；CI 同款。
- 无新 `go.mod` 依赖（纯 stdlib `strconv`/`fmt` 已在用，I6）；无 SQL/迁移（I2）；中间件链（I4）、存储 key（I3）不触及。
- WARN 级接受项：`Load()` 单函数长度/圈复杂度超约定阈值——**今日已如此**（:51-:233），逐字段重构不新增净分支；AGENTS §0 明确 gocyclo 仅 WARN 不阻断。

---

## 10. 范围边界（明确不做）

| 不做 | 理由 |
|------|------|
| 分析方向 2：NaN/±Inf 拒绝与数值边界（budget/cost/rate limit/dim/chunk 等） | 规格 §5 明确排除；与 cli 兄弟 N1 同源，另立方向 |
| 分析方向 3：webhook/SigV4/prefix 格式校验 | 规格 §5 明确排除；属 `Validate()` 层职责 |
| 聚合全部错误一次报全 | first-error-wins 与既有先例一致（E4），单变量文本可测（D2） |
| 改 `getEnv`/string 解析、`splitCSV`/`splitMapping`/`reconcileTenants` | 无解析失败可能（FR-1d） |
| 改 `cmd/server/main.go` / `internal/cli` | fatal/mcp 退出路径已存在（E6）；cli 不加载 config（W3） |
| 改 outbox 历史设计文档（internal-config-durable-async-outbox.design.md） | 历史快照不改写；强制执行点 = 测试翻转（§2.2、§7 AC-3b） |
| `.env.example` 逐变量改写 | 无 "falls back" 表述（grep 实证），无需变更 |
| 空串语义、合法值集、`Validate()` 业务约束 | 冻结不变（FR-1a/FR-1b/FR-2d，I5 基线保护） |

**proposed_vs_verified 对照**：verified——规格 E1–E13 全数复核通过（零行号漂移）；proposed——① W2（工作树 outbox fail-open 测试钉，规格未预见，本设计新增 AC-3b 处置）；② D2 歧义消解（FR-2c "末尾单次" 落实为逐字段早退，理由：last-error-wins 违背 FR-2b）；③ 行数预算实证（§9，含 `_test.go` 豁免依据）；④ T-D1..T-D3 三个设计级测试把 FR-2a/FR-2b/包壳路径变为可执行断言。
