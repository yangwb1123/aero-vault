# 方向：typed env 值解析失败即启动报错（fail fast），不再静默回退默认值

> **模块：** `internal/config` · **来源分析：** `docs/auto/analyses/internal-config-a932ee1e.json`（方向 1） · **日期：** 2026-08-06
> **评分：** 价值 9 / 风险降低 8 / 工作量 4 / 置信度 9
> **本文所有代码引用均已对照仓库逐条验证**（行号以当前工作树为准，HEAD `acfaaf4`；方向引用的行号与当前树一致，无漂移）。
>
> **与既有规格/在途实现的关系：** 无冲突——本方向只改 `internal/config` 包内契约；`cmd/server/main.go` **零改动**（fail-fast 退出路径已存在，见 E6）。分析文件中的方向 2（NaN/±Inf 与数值边界校验）与方向 3（webhook/SigV4/前缀格式校验）**明确不在本规格范围**（§5）。

---

## 1. 问题陈述

`getEnvBool`/`getEnvInt`/`getEnvFloat` 吞掉 `strconv` 解析错误并返回默认值，**无 error、无 warning、无日志**（E1-E3）。解析失败发生在 `Load()` 之内（E4），而 `Load()` 本身是错误返回式入口、main 已有 `fatal:` 退出路径（E6）——fail-fast 的管道已就位，唯独三个 helper 把错误咽了。后果：**安全/可用性旋钮上的一个笔误，静默关闭该旋钮**：

| 笔误 | 实际效果 | 静默后果 |
|------|---------|---------|
| `APP_TLS_ENABLED=yes` | `strconv.ParseBool` 失败 → `false` | **TLS 关闭**，明文端口上线 |
| `AI_DEGRADED_MODE=yes` | 同上 → `false` | **AI kill-switch 关闭**，降级模式失效 |
| `STORAGE_VERIFY_ON_READ=yes` | 同上 → `false` | **on-read 完整性校验关闭** |
| `RECONCILE_INTERVAL_MINUTES=30m` | `strconv.Atoi` 失败 → `0` | **对账/GC 静默禁用** |
| `AI_TENANT_DAILY_BUDGET_USD=100USD` | `strconv.ParseFloat` 失败 → `0` | **AI 日费用上限静默无限** |

（方向原文列出的 `AI_PER_TENANT_BUDGETS` 等其余 ~90 个 typed 读取点同理，见 E7。）

### 触发场景（真实工作流）

1. 运维在 `.env` 里写 `APP_TLS_ENABLED=yes`（`yes` 不在 `strconv.ParseBool` 的合法值集 `1/t/T/TRUE/true/True/0/f/F/FALSE/false/False` 内，E10）→ 进程以 `TLSEnabled=false` 正常启动，`/healthz` 全绿，TLS 证书从未加载。
2. 同型笔误作用于 `AI_DEGRADED_MODE`（全局 AI kill-switch）→ 事故期间运维以为已降级，实际 AI 端点照常计费。
3. 现有测试把 fail-open 钉死为"正确"行为（E5：`config_test.go:48/:79/:109` `'unparseable uses default'`）——任何修复若不同步改这三处测试，会直接红。
4. `docs/configuration.md:12-13` 明文记载 "An unparseable value falls back to the default"（E9）——文档把缺陷写成契约，运维无从知晓这是 bug。

---

## 2. 现状与代码证据（已逐条验证）

| # | 证据 | 验证结果 |
|---|------|---------|
| E1 | `config.go:307-317` — `getEnvBool`：:314 `b, err := strconv.ParseBool(v)` → :315 `if err != nil { return def }`（方向引用 :307） | ✅ 与引用一致，无行号漂移 |
| E2 | `config.go:319-329` — `getEnvFloat`：:326 `f, err := strconv.ParseFloat(v, 64)` → :327 `if err != nil { return def }`（方向引用 :319） | ✅ 与引用一致 |
| E3 | `config.go:367-377` — `getEnvInt`：:374 `n, err := strconv.Atoi(v)` → :375 `if err != nil { return def }`（方向引用 :367） | ✅ 与引用一致 |
| E4 | `config.go:51` — `func Load() (*Config, error)`（方向引用 :51）；同文件已有 fail-fast 先例：`parseLogLevel`（config.go:379 起）非法值 → `Load()` 返回 error，测试 `TestLoad_InvalidLogLevel`（config_test.go:498）钉死该契约 | ✅ 与引用一致 |
| E5 | `config_test.go:48-49`（bool `'unparseable uses default'` ×2）、`:79`（int `"12.5"`）、`:109`（float `"x"`）——把 fail-open 钉为正确行为（方向引用 :48/:79/:109） | ✅ 与引用一致 |
| E6 | `main.go:39-42` `main()`：`fmt.Fprintf(os.Stderr, "fatal: %v\n", err); os.Exit(1)`；:46 `run()` 与 :171 `runMCP()` 均 `cfg, err := config.Load(); if err != nil { return fmt.Errorf("load config: %w", err) }` → **Load 报错即退出，fail-fast 启动路径已存在，main.go 无需任何改动** | ✅ 补充验证 |
| E7 | typed helper 调用面（**全部包内**，无外部调用者）：`config.go` 69 处（含方向五个旋钮）、`config_billing.go` 7 处、`config_audit_governance.go` 12 处、`config_event_outbox.go` 8 处（`grep -rn` 全仓确认） | ✅ 补充验证（工作量 4 的来源） |
| E8 | `config.go:227` `EventOutbox: loadEventOutboxConfig()` —— 该 loader（config_event_outbox.go:25-37）**当前不返回 error**；错误贯通须将其签名改为 `(EventOutboxConfig, error)`（唯一跨签名涟漪，见 FR-2） | ✅ 补充验证 |
| E9 | `docs/configuration.md:12-13`："**Booleans** accept Go `strconv.ParseBool` values … An unparseable value falls back to the default."——文档记载 fail-open 契约，须同步更新（FR-5）；同文件 :16 "Validation (fails fast on startup)" 段落为新增行为的自然落点 | ✅ 补充验证 |
| E10 | `strconv` 合法值集核验：`ParseBool` 仅接受 `1/t/T/TRUE/true/True/0/f/F/FALSE/false/False`——`"yes"` 必失败；`Atoi("30m")` 必失败；`ParseFloat("100USD")` 必失败 → 方向五个示例全部为**真实不可解析值**，验收测试可稳定复现 | ✅ 补充验证 |
| E11 | 空串语义：三个 helper 对 set-but-empty 一律 `return def`（:310-312/:322-324/:370-372）；测试基建 `clearEnv`（config_test.go:351 起）用 `t.Setenv(k, "")` 清场——**空串=未设置=默认，不得报错**，否则全部现有 Load 测试崩（FR-4 回归锁） | ✅ 补充验证 |
| E12 | 既有"unset/empty 用默认"用例（config_test.go:41-43/:75-76/:105-106）与"合法值解析"用例（:44-47/:77-78/:107-108）——新契约下语义不变，仅断言形态从单值变双值 | ✅ 补充验证 |
| E13 | `main.go:50-51`：`logger := slog.New(...)` 在 `config.Load()` **成功之后**才创建——"启动 warning"方案在 Load 时刻根本没有 logger 可用，warning 无从输出（决策记录 D1 的硬证据） | ✅ 补充验证 |

### 缺陷机理

```
Load()（config.go:51）
  ├─ parseLogLevel → 非法值已 fail-fast（E4）          ← 唯一会报错的入口
  ├─ loadBillingConfig / loadAuditGovernanceConfig / loadAuditSinkL2Config
  │    └─ getEnvBool/getEnvInt → 解析失败 → return def（E1-E3）  ← 静默
  ├─ 大 struct literal（:59-232，69 处 typed 读取）
  │    └─ APP_TLS_ENABLED=yes → TLSEnabled=false       ← 静默，无日志
  └─ cfg.Validate() → 只查业务约束（backend/driver 等），不查解析
main.go:46 → run() → 启动成功，TLS 关闭/对账禁用/预算无限（E6 未触发）
```

---

## 3. 需求规格

### FR-1：helper 契约——解析失败返回 error，且 error 点名变量与非法值

`internal/config/config.go` 三个 helper 签名改为：

```go
func getEnvBool(key string, def bool) (bool, error)
func getEnvInt(key string, def int) (int, error)
func getEnvFloat(key string, def float64) (float64, error)
```

- **约束 a（未设置/空串，语义不变）：** `!ok || v == ""` → `(def, nil)`。空串=未设置=默认（E11，`clearEnv` 基建依赖此语义）。
- **约束 b（解析成功）：** 合法值 → `(parsed, nil)`；`strconv` 合法值集不变（E10，`docs/configuration.md:12` 的 `1/0/t/f/…` 清单仍有效）。
- **约束 c（解析失败）：** 非空但不可解析 → `(def, error)`，错误文本**必须包含环境变量名与原始值**，统一格式：

  ```go
  return def, fmt.Errorf("invalid value %q for %s: %v", v, key, err)
  // 例：invalid value "yes" for APP_TLS_ENABLED: strconv.ParseBool: parsing "yes": invalid syntax
  ```

  该格式使验收断言可用 `strings.Contains(err.Error(), "APP_TLS_ENABLED")` 稳定锁定（外层 `run()` 的 `load config: %w` 包装不破坏 Contains，E6）。
- **约束 d：** `getEnv`（string）不改——字符串型变量无解析步骤，无失败可能。

### FR-2：错误贯通全部 loader，first-error-wins，Load() 返回

- **约束 a：** 四个 loader 逐一贯通：`loadBillingConfig`（config_billing.go:44-69）、`loadAuditGovernanceConfig`（config_audit_governance.go:53 起）、`loadAuditSinkL2Config`（config_audit_sink_l2.go:37 起）、**`loadEventOutboxConfig` 签名改为 `(EventOutboxConfig, error)`**（config_event_outbox.go:25-37 + 调用点 config.go:227，E8）——唯一跨签名涟漪。
- **约束 b（first-error-wins）：** 任一 loader 返回首个解析错误即向上传播，`Load()` 原样返回（与 `parseLogLevel`、`loadBillingConfig` 现有"遇错即返"先例一致，E4）。错误顺序确定（`Load()` 内既有调用顺序：`parseLogLevel` → billing → auditGovernance → auditSinkL2 → struct literal 字段序），可测。
- **约束 c：** 每个 loader 内保持"逐字段赋值 + 末尾单次 `if err != nil`"结构；`int64(getEnvInt("STORAGE_VERIFY_MAX_SIZE", …))`（config.go:93）等包壳调用改为先取 `(int, error)` 再转换。`cfg.Reconcile.UploadGCEnable = cfg.Reconcile.UploadGCHours > 0`（config.go:292）后处理逻辑不变。
- **约束 d：** `cfg.Validate()`（config.go:233-235）仍保留——解析错误先于校验错误返回，两者职责分离，校验逻辑零改动。

### FR-3：启动 fail-fast 语义（main.go 零改动）

- `Load()` 返回解析错误 → 既有路径生效：`run()`/`runMCP()` 包装后返回（E6），`main()` 打印 `fatal: <err>`/`mcp: <err>` 并 `os.Exit(1)`。**不新增 warning 通道**（D1：warning-only 违背方向标题 "fail fast"，且 Load 时刻无 logger 可用，E13）。
- 行为目标（对方向问题陈述的逐条闭合）：`APP_TLS_ENABLED=yes` 不再以 TLS-off 启动；`AI_DEGRADED_MODE=yes` 不再以 kill-switch-off 启动；`STORAGE_VERIFY_ON_READ=yes` 不再以校验-off 启动；`RECONCILE_INTERVAL_MINUTES=30m` 不再以对账-off 启动；`AI_TENANT_DAILY_BUDGET_USD=100USD` 不再以预算无限启动——全部变为**启动失败 + 点名变量的错误**。

### FR-4：空串/未设置语义冻结（回归锁）

set-but-empty 与 unset 一律 `(def, nil)`（FR-1a）。现有测试基建 `clearEnv`（E11）与全部 `'unset/empty uses default'` 用例（E12）**断言不变、不得因本方向修改**——只更新三个 `'unparseable uses default'` 用例（E5）与断言形态。

### FR-5：文档契约同步

`docs/configuration.md:12-13` 的 "An unparseable value falls back to the default" 改为失败契约，并入既有 "Validation (fails fast on startup)" 段落（E9）：

> **Booleans** accept Go `strconv.ParseBool` values: `true`/`false`, `1`/`0`, `t`/`f`, etc. **A non-empty unparseable value fails startup** with an error naming the variable (same for integer and float variables).

### 非功能约束

- `make check` 全绿（gofmt/build/vet/test，AGENTS.md §0）；改动限于 `internal/config`（config.go、config_billing.go、config_audit_governance.go、config_audit_sink_l2.go、config_event_outbox.go、config_test.go）+ `docs/configuration.md`。
- `config.go` 当前 392 行，逐字段赋值改造保持行数基本不变（FR-2c 结构不增行），单文件 ≤ 500 行约束安全。
- 纯 stdlib，无新 `go.mod` 依赖（I6）；无 SQL/schema/迁移（I2）；不触碰中间件链（I4）与存储层（I3）。

---

## 4. 验收标准（可测试）

> 方向提供的 4 条验收全部保留并落为可执行测试；测试沿用包内既有基建：`clearEnv(t)`（E11）清场 + `t.Setenv`。错误文本断言统一用 `strings.Contains`（对 `load config: %w` 包装鲁棒，E6）。

### AC-1 Load() 对 `APP_TLS_ENABLED=yes` 返回点名错误（unit）

> 对应方向验收 1："New test: `t.Setenv("APP_TLS_ENABLED", "yes")`; `Load()` returns an error naming `APP_TLS_ENABLED` … instead of silently returning TLSEnabled=false"。**"or emits a startup warning" 分支被否决**（D1）——验收的测试形式取 error 契约。

```go
func TestLoad_UnparseableBoolFailsStartup(t *testing.T) {
	clearEnv(t)
	t.Setenv("APP_TLS_ENABLED", "yes")
	_, err := Load()
	if err == nil {
		t.Fatal("Load() must fail on APP_TLS_ENABLED=yes (strconv.ParseBool rejects \"yes\")")
	}
	if !strings.Contains(err.Error(), "APP_TLS_ENABLED") {
		t.Fatalf("error must name the variable, got: %v", err)
	}
	if !strings.Contains(err.Error(), "yes") {
		t.Fatalf("error must include the offending value, got: %v", err)
	}
	// 反向锁定：合法值照常生效（E10 值集）
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

### AC-2 `AI_DEGRADED_MODE=yes` 与 `RECONCILE_INTERVAL_MINUTES=30m` 同样报错（unit，表驱动）

> 对应方向验收 2。表内另含问题陈述点名的 `STORAGE_VERIFY_ON_READ=yes`、`AI_TENANT_DAILY_BUDGET_USD=100USD`（覆盖 float 路径；bool/int/float 三型各有 Load 级用例）与 `EVENT_OUTBOX_ENABLED=yes`（锁定 FR-2 经 `loadEventOutboxConfig` 新签名的贯通，E8）——均为同一契约的表行，不扩范围。

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

### AC-3 既有三个 helper 表测试改为断言新错误契约（unit）

> 对应方向验收 3："Existing TestGetEnvBool/TestGetEnvInt/TestGetEnvFloat 'unparseable uses default' cases are updated to assert the new error/warning contract"。表结构增加 `wantErr` 字段；**unset/empty/合法值用例仅改断言形态（双值接收），语义断言不变**（FR-4/E12）。

```go
// TestGetEnvBool（config_test.go:33 起）：
//   {"unparseable uses default", true, "notabool", true, true}
//   {"unparseable uses default false", true, "yepnope", false, false}
// → 改为 wantErr=true 行，断言 (def, err) 且 err 文本含 "AERO_TEST_BOOL"：
//   {"unparseable returns error", true, "notabool", true, true, true},
//   {"unparseable returns error false", true, "yepnope", false, false, true},
//   其余行 wantErr=false：got, err := getEnvBool(key, c.def)；err 必须为 nil。
// TestGetEnvInt（config_test.go:67 起）：{"12.5",9} 与 {"abc",9} → wantErr=true。
// TestGetEnvFloat（config_test.go:97 起）：{"x",2.0} → wantErr=true。
```

### AC-4 门禁

> 对应方向验收 4。命令在仓库根目录执行：

```bash
go test ./internal/config/
gofmt -l internal/config/   # 期望无输出（make check 同款）
go vet ./internal/config/   # 期望无输出
```

---

## 5. 范围边界（明确不做）与决策记录

| 不做 / 决策 | 理由 |
|------|------|
| **D1：否决 "startup warning" 分支**（方向验收 1 的括号内选项） | 方向标题即 "Fail fast … instead of silently falling back"；warning-only 进程继续以错误配置存活（fail-open 依旧）。硬证据：`slog` logger 在 `Load()` 成功后才创建（E13，main.go:50-51），Load 时刻无 logger 可 warn；且 "warning" 不可作为确定性测试契约。error + `fatal:` 退出（E6）是唯一可测、真正 fail-fast 的形式 |
| **聚合全部错误一次报全** | first-error-wins 与既有先例一致（parseLogLevel、loadBillingConfig 遇错即返，E4）；错误顺序确定（FR-2b），单变量验收断言稳定 |
| **在 `Validate()` 增加重解析 pass** | 解析发生在 helper，重解析会复制解析逻辑并漏掉 loader 内变量（E7）；改 helper 签名是单一事实源 |
| 改 `getEnv`（string） | 无解析步骤，无失败可能（FR-1d） |
| 改 `cmd/server/main.go` | `fatal:` 退出路径已存在（E6），零改动 |
| 方向 2：NaN/±Inf 拒绝与数值边界（AI budget/cost、rate limit、embed dim、chunk window、STORAGE_* 超时） | 属分析文件独立方向；`ParseFloat("NaN")` 不报错，本方向只管**不可解析**值。README/AGENTS 未要求合并 |
| 方向 3：webhook URL/secret、SigV4 凭据、前缀格式校验 | 属分析文件独立方向；格式校验是 `Validate()` 层职责，与本方向正交 |
| `.env.example` 逐变量改写 | 无"回退默认值"表述（grep 确认），无需变更 |
| 空串语义、合法值集、`Validate()` 业务约束 | 冻结不变（FR-1a/FR-1b/FR-2d，I5 基线保护） |

**proposed_vs_verified 对照：** verified——三 helper 吞错（E1-E3，行号与方向引用零漂移）、`Load()` 错误入口（E4）、测试钉死 fail-open（E5）、文档记载 fail-open（E9）；proposed——helper 返回 `(T, error)` + 点名错误文本（FR-1c，格式为新增决策）、`loadEventOutboxConfig` 签名涟漪（FR-2a，E8 驱动）、warning 分支否决（D1，E13 驱动）、first-error-wins（FR-2b）。

---

## 6. 实现指引（供验收后落地，非本规格交付物）

1. **`internal/config/config.go`**：三个 helper 改签名（FR-1，config.go:307/:319/:367）；`Load()` 大 literal（:59-232）改为逐字段赋值 + 末尾 `if err != nil { return nil, err }`（FR-2c；`VerifyMaxSize` 的 `int64(...)` 包壳先取 `(int, error)`）。
2. **`internal/config/config_billing.go`**（:44-69）、**`config_audit_governance.go`**（:53 起）、**`config_audit_sink_l2.go`**（:37 起）：同型改造；**`config_event_outbox.go`**（:25-37）签名改 `(EventOutboxConfig, error)` + 调用点 config.go:227（FR-2a）。
3. **`internal/config/config_test.go`**：三张表测试加 `wantErr` 字段（AC-3，:33/:67/:97）；新增 `TestLoad_UnparseableBoolFailsStartup`（AC-1）与 `TestLoad_UnparseableTypedEnvFailsStartup`（AC-2）；`clearEnv` 与 unset/empty 用例零改动（FR-4）。
4. **`docs/configuration.md`**：:12-13 改失败契约文本（FR-5）。
5. 验证：`go test ./internal/config/`、`gofmt -l internal/config/`、`go vet ./internal/config/`、`make check`（AC-4）。
