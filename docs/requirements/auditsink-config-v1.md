# 方向：泛型 `AuditSinkConfig`（adapter kind L0/L1/L2 + 泛型 endpoint/credentials）取代硬编码 `AUDIT_GOVERNANCE_*` 块，Snaplink 契约降级为 legacy L2 mapping

> **模块：** `internal/config`（组合面：`cmd/server/workers.go` 装配 + `internal/events/audit_sink.go` 端口）· **来源分析：** `docs/auto/analyses/internal-config-a932ee1e.json` · **日期：** 2026-08-06
> **评分：** 价值 9 / 风险降低 8 / 工作量 6 / 置信度 9
> **验证基准：** 工作树 = HEAD `acfaaf4` + **未提交 WIP**（audit-batch round-2 落地代码：`config_audit_sink_l2.go`、`internal/events/audit_sink.go`、event outbox 等，见 §2.1）。本文所有引用均已对照该基准逐行验证。
>
> **本文是增量规格：** 分析文所验证的硬编码 Snaplink 契约现状**仍然成立**，但 WIP 已在其旁新增了第二个平行 L2 表面（`AuditSinkL2Config`）与 `AuditSink` 端口——本方向的缺口因此更精确地表述为：**config 模块有两个平行 L2 表面、无 kind 选择器、无 L0/L1 表面**。FR 是缺口增量（§2.3 G1–G5），不是绿地设计。

---

## 1. 问题陈述

组合面（COMPOSE 契约）要求 AuditSink 端口带**可替换适配器**（L0 本地 / L1 协议 / L2 governance），且核心配置**不硬编码 sibling 项目名**。config 模块今天烘焙了恰好一个 sink——Snaplink L2 线上契约：

1. `internal/config/config_audit_governance.go` 定义 `AuditGovernanceConfig` + `loadAuditGovernanceConfig`（`AUDIT_GOVERNANCE_ENABLED/BASE_URL/TOKEN_URL/HMAC_KEY/BINDINGS_FILE` + 11 个运行参数），`resolveAuditGovernanceSecrets` 强制每 binding 的 `client_secret_env` 以 **`AUDIT_GOVERNANCE_CLIENT_SECRET_` 前缀**命名且 env 必须可解析——即 legacy 契约把"一个 OAuth client-credentials + HMAC 治理端点"焊死在 Config 里。
2. `.env.example:185/188` 与 `docs/configuration.md:264` **显式写出 sibling 项目名**（"Snaplink Audit Governance durable relay" / "Snaplink OAuth token endpoint"）。
3. **没有 adapter-kind 选择器、没有 L0/L1 配置表面**（全仓无 `AuditSinkConfig` 符号）：L0（本地 `audit_log`）与 L1（bus/SSE/webhook 协议面）在配置中不可表达、不可断言——L0 只是隐式常开，L1 只是隐式旁路。组合面无法从配置表述"本次装配用的是哪个适配器"。
4. **WIP 新增的第二个平行 L2 表面**（`config_audit_sink_l2.go`，`AUDIT_SINK_L2_ENDPOINT/BINDINGS_FILE`，静态 bearer）缓解了"无 L2 泛型表面"，但**加剧**了拓扑混乱：两个 L2 表面（legacy OAuth+HMAC vs 泛型 bearer）并存、互不隶属、无选择语义，`cmd/server/workers.go` 只按 `Endpoint != ""` 隐式开关。

**本方向要求：** 引入**一个**泛型 `AuditSinkConfig`——`Kind ∈ {L0, L1, L2}` 选择器 + 泛型 endpoint/credentials——Snaplink 线上契约**原样保留为 legacy L2 mapping**（env 键/默认值/校验规则零变化，`config_audit_governance_test.go` 套件必须照常通过），使组合装配仅由配置选择适配器。

### 触发场景（真实工作流）

1. 运维在 `.env` 里只设 `AUDIT_SINK_KIND=L0` → 装配断言"本地审计"，无需任何治理凭据即可启动（CI 基线形状）。
2. 合规部门接入外部治理系统 → 只改配置（`AUDIT_SINK_KIND=L2` + endpoint/bindings），**不改一行代码**即切换投递目标。
3. 存量部署沿用 `AUDIT_GOVERNANCE_*`（Snaplink OAuth 契约）→ 自动派生 `Kind=L2`（legacy mapping），行为与今天完全一致。

---

## 2. 现状与代码证据（已逐条验证）

### 2.1 工作树状态修正（分析之后的关键事实）

分析文（针对更早基线）声称"config 模块烘焙唯一 sink：Snaplink L2 线上契约"——**前半仍成立**（`AuditGovernanceConfig` 原样在列）；但 WIP（audit-batch round-2，未提交）已新增：

| WIP 文件/符号 | 内容 | 验证 |
|---|---|---|
| `internal/config/config_audit_sink_l2.go` | `AuditSinkL2Config{Endpoint, BindingsFile, Bindings}`（`AUDIT_SINK_L2_ENDPOINT`/`AUDIT_SINK_L2_BINDINGS_FILE`）；bindings = `{"bindings":[{"tenant","token"\|"token_env"}]}` 静态 bearer；文件纪律 **mode 0600**（比 governance 的 0o022 更严）、≤1 MiB、DisallowUnknownFields、trailing-JSON 拒绝；token 卫生（≥16 字符、无首尾空白、`AUDIT_SINK_L2_TOKEN_` env 前缀）；空 Endpoint = L2 禁用恒合法 | ✅ 读全文 |
| `Config.AuditSinkL2` | `config.go:34` 字段、`config.go:66` `loadAuditSinkL2Config()`、`config.go:227` 装配；`config_validate.go:45` `c.AuditSinkL2.Validate()` | ✅ |
| `internal/events/audit_sink.go` | **`AuditSink` 端口**：`DeliverDeleted(ctx, tenant, factID, payload)`；`ErrSinkNotBound`（未绑定租户 ⇒ complete 记录保留）、`ErrSinkUnauthorized`（401/403 ⇒ 立即终态） | ✅ |
| `cmd/server/workers.go:166-175` | 装配：`cfg.AuditSinkL2.Endpoint != ""` ⇒ `events.NewAuditSinkL2(...)` 注入 `EventOutboxRelayOptions.AuditSink`（**隐式 kind 开关**） | ✅ |
| `internal/events/audit_sink_l2.go` | L2 适配器：端点校验、redirect 禁用、401/403 终态、2xx + `X-Audit-Fact-Id` 回显为 commit point | ✅（引用 sibling 规格） |
| `internal/config/config_event_outbox.go` | `EventOutboxConfig`（`EVENT_OUTBOX_*`：poll/batch/claim TTL/http timeout/max attempts） | ✅ |
| 测试 | `config_audit_governance_test.go`（5 个）、`config_audit_sink_l2_test.go`（2 个）、`config_test.go`（`TestLoad_*`/`TestValidate_*` 模式） | ✅ |

**推论：** 方向文"config 模块烘焙唯一 sink"**已过时（WIP 加了泛型 L2 bearer 表面）**；"**无 adapter-kind 选择器、无 L0/L1 表面**"**仍成立**（`AuditSinkConfig` 符号全仓不存在；kind 选择在 config 模块不可表达）。本规格的 FR 聚焦：**kind 选择器 + 单表面统一 + legacy mapping + 严格校验**。

### 2.2 方向文证据验证表

| # | 方向文引用 | 验证结果 |
|---|-----------|---------|
| E1 | `config_audit_governance.go` — `AuditGovernanceConfig`、`loadAuditGovernanceConfig`、`resolveAuditGovernanceSecrets`、`validateAuditGovernanceURL` | ✅ 全部存在（`loadAuditGovernanceConfig` :53、`readAuditGovernanceBindings` :86、`resolveAuditGovernanceSecrets` :140：`HasPrefix(client_secret_env, "AUDIT_GOVERNANCE_CLIENT_SECRET_")` + `os.LookupEnv` 必解析；`validateAuditGovernanceURL` :195：绝对 URL 无凭据/query/fragment，HTTPS 或 loopback HTTP） |
| E2 | `.env.example:186-204` — `AUDIT_GOVERNANCE_*` 块 | ✅ 块确在 :186-204（18 个键）。**行号微漂移：** 分析文引 "187-188 命名 sibling"——实际 :185 块头注释 "Snaplink Audit Governance durable relay"、:188 注释 "Snaplink OAuth token endpoint"；主张（显式命名 sibling）成立 |
| E3 | `config_billing.go` — bindings-file 模式 | ✅ `readBillingBindings`（≤1 MiB、DisallowUnknownFields、EOF 拒绝）、`resolveBillingSecrets`（env 间接）、`validateBillingURL`/`validateBillingBindings` 边界——复用先例 |
| E4 | `config_validate.go` — `validateCommercialIntegrations` | ✅ :38 调用、:51-61 定义（billing + governance 各自 Validate）、:63-81 `validateCommercialCredentialSeparation`（HMAC ≠ billing 凭据、client_id/secret_env/secret 三互异）。**新增：** :45 `c.AuditSinkL2.Validate()` |
| E5 | `internal/auditgovernance/relay.go` — L2 消费侧 | ✅ `Runtime.deliverBatch`(:59)/`deliverFact`(:80)/`failFact`(:111)/`retryFact`(:124)/`boundedBackoff`(:163, 2× 指数 + sha256(id) ±25% jitter)/`classifyRelayError`(:181)——legacy 投递面（独立 outbox 表 + 脱敏形状），**本方向不改** |

### 2.3 缺口分析（本方向 acceptance vs 现状）

| # | 缺口 | 现状证据 | 后果 |
|---|------|---------|------|
| G1 | **无 kind 选择器** | 全仓 grep 无 `AuditSinkConfig`；`Config` 平铺两个平行字段 `AuditGovernance` + `AuditSinkL2`（config.go:33-34） | 组合面无法表达"所选适配器"；两个 L2 表面无统一语义 |
| G2 | **L0/L1 无配置表面** | L0 = 隐式（`insertAuditEntry` 删除事务内，sibling 规格已落地）；L1 = 隐式（`EventsConfig` 只有 webhook/transport，config_app.go） | AC"L1 无凭据自举"无法从配置断言 |
| G3 | **legacy 块仍烘焙 sibling 名** | `.env.example:185/188`、`docs/configuration.md:264` 显式写 "Snaplink"；`deploy/snaplink-audit-governance-bindings.example.json` | 违反"不硬编码 sibling 项目名"（文档/注释层） |
| G4 | **无"未知 kind 拒绝 / L1/L2 需 endpoint / L0 禁凭据 / 双选择冲突"校验** | `config_validate.go` 只做逐类型 Validate，无选择层校验 | 配置错误静默落为隐式行为（如 L2 endpoint 拼错 + kind 缺省 = 静默 L0） |
| G5 | **`.env.example` 缺 `AUDIT_SINK_L2_*`** | grep 证实 .env.example 无任何 `AUDIT_SINK` 键（仅 docs/configuration.md:359-360） | 新表面无样例文档，运维不可发现 |
| — | （记录）`AuditSinkL2Config.Validate()` 对空 Endpoint 恒合法（=禁用） | `config_audit_sink_l2_test.go:31-33` `TestAuditSinkL2Config_EndpointScheme` 断言 `AuditSinkL2Config{}.Validate()==nil` | **实现约束：** "L2 需 endpoint"必须在**选择器层**（`AuditSinkConfig.Validate`）强制，不得改 `AuditSinkL2Config` 自身规则，否则既有测试红 |

---

## 3. 需求规格

### FR-1：泛型 `AuditSinkConfig`（kind 选择器 + 泛型 endpoint/credentials + legacy mapping）

新增 `internal/config/config_audit_sink.go`（新文件，≤500 行），**单一**选择语义取代两个平铺字段：

- **`AuditSinkConfig`**（`Config.AuditSink` 取代 `Config.AuditGovernance` + `Config.AuditSinkL2`，或等价合并——要求：单一选择、单一 `Validate` 入口）：
  - `Kind string` ← **`AUDIT_SINK_KIND`**（大小写不敏感；空 = 派生，见下），取值 `L0`/`L1`/`L2`；
  - L2 泛型表面：`Endpoint`/`BindingsFile`/`Bindings`（**沿用既有 `AuditSinkL2Config` 形状与 `AUDIT_SINK_L2_*` 键**——它们已是泛型、无 sibling 命名；不重命名，避免 WIP churn）；
  - legacy mapping：`Legacy AuditGovernanceConfig`（Snaplink 契约整体保留）。
- **kind 派生（优先级，防歧义）：**
  1. `AUDIT_SINK_KIND` 显式非空 ⇒ 以之为准（值必须 ∈ {L0,L1,L2}，否则启动失败）；
  2. 否则 `AUDIT_GOVERNANCE_ENABLED=true` ⇒ `Kind=L2`（**legacy 选择机制**，存量部署零迁移）；
  3. 否则 `AUDIT_SINK_L2_ENDPOINT` 或 `AUDIT_SINK_L2_BINDINGS_FILE` 非缺省 ⇒ `Kind=L2`（WIP 表面向后兼容——sibling 规格已文档化"设 endpoint 即启用"）；
  4. 否则 ⇒ `Kind=L0`（**默认**；CI 基线 = SQLite+local FS+无凭据，I5 不受影响）。
- **冲突即失败（fail-fast，镜像本模块 DisallowUnknownFields/bindings 严格纪律）：**
  - 显式 kind ∈ {L0, L1} 且 legacy enabled 或 L2 键非缺省 ⇒ 启动失败（dead-config 拒绝）；
  - `Kind=L2` 且 legacy enabled **且** `AUDIT_SINK_L2_ENDPOINT` 也设置 ⇒ 启动失败（双重凭据源歧义）。合法组合：L2 + legacy 单独驱动（OAuth+HMAC 变体）、L2 + `AUDIT_SINK_L2_*` 单独驱动（bearer 变体）。
- **语义：** L0 = 本地 `audit_log`（删除事务内写入，常开，无 endpoint/凭据）；L1 = 协议面（既有 bus/SSE/webhook，`EventsConfig` 驱动，**无需任何 governance 凭据**，无新增投递机器）；L2 = governance 端点投递（bearer 或 legacy OAuth 变体）。
- **兼容性（不变量）：** `AuditGovernanceConfig`/`loadAuditGovernanceConfig`/`readAuditGovernanceBindings`/`resolveAuditGovernanceSecrets`/`validateAuditGovernanceURL` 及其全部 env 键、默认值、校验规则、bindings JSON 形状（`tenant_id/client_id/client_secret_env/state` + `revision`）、HMAC 规则（32..4096、与 OAuth/billing 互异）**零改动**——`config_audit_governance_test.go` 5 个测试直构该类型并调 `Validate()`/`readAuditGovernanceBindings`，必须照常通过。`AuditSinkL2Config` 自身规则（含"空 Endpoint 合法"）也**不改**（见 G5 记录）；"L2 需 endpoint"只在选择器层强制。

### FR-2：严格校验（`AuditSinkConfig.Validate()`，经 `Config.Validate()` 单入口）

- **未知 kind 拒绝：** `AUDIT_SINK_KIND` 非空且 ∉ {L0,L1,L2}（任意非空非法值，如 `l3`、`foo`）⇒ `Load()` 报错；空串 = 派生路径（FR-1），不在拒绝集。
- **L2 需 endpoint：** `Kind=L2` 时须解析出**唯一** endpoint——legacy 变体：既有规则（enabled ⇒ `BASE_URL`/`TOKEN_URL` 必填且过 `validateAuditGovernanceURL`）；bearer 变体：`AUDIT_SINK_L2_ENDPOINT` 非空且过同一 URL 策略（HTTPS 或 loopback HTTP）；两者皆空 ⇒ 报错。bindings 纪律沿用各自既有规则（0600/≤1 MiB/严格 JSON；revisioned/非组写/`CLIENT_SECRET_` 前缀），不新增。
- **L0/L1 禁治理凭据：** `Kind=L0|L1` 时 legacy enabled 或任一 L2 键非缺省 ⇒ 报错（dead-config，FR-1 冲突规则的校验化）。
- **L1 无凭据自举：** `Kind=L1` **不要求**新 endpoint——协议面（bus/SSE/webhook）由既有 `EventsConfig` 驱动；direction acceptance 的 "L1/L2 require endpoint" 在本系统的映射为：**L2 必须配置治理端点；L1 的端点 = 恒在的协议面，因此 L1 的唯一硬要求是不携带治理凭据**（解释性映射，见 §4 AC-1）。
- **凭据互异扩展：** `validateCommercialIntegrations` 签名从 `(Billing, AuditGovernance)` 调整为组合表面（如 `(Billing, AuditSink)`，legacy 字段透传既有规则）：L2 泛型 token、legacy HMAC、billing 凭据三者互异规则在允许组合内**继续成立**（审计行为零变化）。

### FR-3：组合契约——kind 唯一决定 adapter 选择，L0↔L2 配置切换零代码改动

- `cmd/server/workers.go` 的 sink 注入仅由 `cfg.AuditSink.Kind`（+L2 字段）派生，**替换**现 `Endpoint != ""` 隐式开关：
  - L0 ⇒ 不注入 sink（既有行为：relay claim→complete，L0 `audit_log` 权威）；
  - L1 ⇒ 注入协议面分发钩子（既有 bus/webhook 管线；投递细节归 events 面既有机制，本规格只要求选择语义与装配钩子）；
  - L2 ⇒ 注入 `events.NewAuditSinkL2`（bearer 变体，既有）或 legacy OAuth 变体对应适配器（= `internal/auditgovernance` 既有 runtime，**不改**）。
- **零代码改动（不变量）：** relay 的 claim→deliver→complete 状态机、退避/租约、载荷处理对三种 kind **完全相同**——只有装配点读 `Kind`。L0 与 L2 配置之间的切换 = 纯 `.env` 差异（AC-2 钉死）。
- 启动日志输出所选 kind（`audit sink kind=L2 endpoint=…` / `kind=L0`），空绑定数照旧只记计数不记 token（既有纪律）。

### FR-4：品牌中立（无 sibling 项目名硬编码）

- `internal/config` 新增泛型表面（类型名、字段注释、错误串、日志）**不得出现 sibling 项目名**；`.env.example` 与 `docs/configuration.md` 的 `AUDIT_GOVERNANCE_*` 块注释去品牌化为 "legacy L2 governance adapter (OAuth client-credentials + HMAC)"——**env 键名本身不变**（向后兼容）。
- legacy mapping 的文档定位：Snaplink 线上契约 = **一个** legacy L2 mapping，不是通用表面；泛型表面以 `AUDIT_SINK_KIND` 为入口。
- **不动** `config_auth_validate.go:27` 等 auth 面的 Snaplink SDK 引用（AGENTS.md §2.5：外部令牌经 sibling SDK 验证属既有契约，不在本方向）。

### FR-5：文档同步（与实现同 commit）

- `.env.example`：增 `AUDIT_SINK_KIND`（注释含 L0/L1/L2 语义与派生规则）及缺失的 `AUDIT_SINK_L2_*` 条目（G5）；legacy 块注释去品牌化。
- `docs/configuration.md`：审计 sink 段重写为 kind 表（L0/L1/L2 语义、冲突规则、默认 L0）+ legacy mapping 小节（标注"存量键，新部署用 `AUDIT_SINK_KIND`"）。

---

## 4. 验收标准（可测试；逐条映射方向 acceptance）

> 测试基建（已验证）：`internal/config/config_test.go` 的 `t.Setenv` + `Load()` 模式（`TestLoad_OverridesAndLowercasing` :454、`TestValidate_OK` :194）；`config_audit_governance_test.go` 直构 `AuditGovernanceConfig` 模式（`validAuditGovernanceConfig()` :24）；relay 测试 `internal/events/event_outbox_relay_test.go`（`TestOutboxRelay_DeliversDeletedFactToL2` :432，httptest 目标 + 注入 sink）与集成 harness `internal/integration/fullserver_test.go`（`startFullServerWithRelay` :55、`TestDeleteResponse_DoesNotBlockOnDelivery` :700、`assertAuditRowFor` :1334）先例。

### AC-1 单元：kind 解析与严格校验（`internal/config/config_audit_sink_test.go` 新增）

```go
// 解析与派生
func TestAuditSinkKind_ParseAndDerive(t *testing.T) {
	// 1) AUDIT_SINK_KIND=l0|L0|l1|L2 均解析为对应 Kind（大小写不敏感）；
	//    AUDIT_SINK_KIND 空 + 全缺省 ⇒ Kind=="L0"（默认）
	// 2) 仅设 AUDIT_GOVERNANCE_ENABLED=true（+ 完整必填 legacy 集）⇒ Kind=="L2"
	//    （legacy 派生；字段逐一映射：AUDIT_GOVERNANCE_BASE_URL→L2 endpoint 语义、
	//    TokenURL/HMACKey/BindingsFile 原样进入 Legacy）
	// 3) 仅设 AUDIT_SINK_L2_ENDPOINT ⇒ Kind=="L2"（WIP 表面向后兼容）
	// 4) AUDIT_SINK_KIND=L2 + legacy enabled + AUDIT_SINK_L2_ENDPOINT 同设 ⇒ Load() 报错（双重凭据源）
	// 5) AUDIT_SINK_KIND=L0 + AUDIT_GOVERNANCE_ENABLED=true ⇒ Load() 报错（dead-config）
}

func TestAuditSinkKind_StrictValidation(t *testing.T) {
	// 1) AUDIT_SINK_KIND=foo|L3（任意非空非法值）⇒ Load() 报错（未知 kind 拒绝）；
	//    空串 = 派生路径（FR-1），不在拒绝集（ParseAndDerive 已覆盖）
	// 2) AUDIT_SINK_KIND=L2 + 无任何 endpoint（legacy 未 enabled、AUDIT_SINK_L2_ENDPOINT 空）⇒ 报错
	// 3) AUDIT_SINK_KIND=L2 + AUDIT_SINK_L2_ENDPOINT=http://audit.example（非 loopback HTTP）⇒ 报错
	//    （复用 validateAuditGovernanceURL 策略）
	// 4) AUDIT_SINK_KIND=L1 + 零治理凭据（无 AUDIT_GOVERNANCE_*、无 AUDIT_SINK_L2_*）⇒ Load() 成功
	//    （L1 无凭据自举；L1 + legacy enabled ⇒ 报错）
	// 5) AUDIT_SINK_KIND=L0 ⇒ Load() 成功且 Config.AuditSink.Kind=="L0"（CI 基线形状）
}

// legacy 契约零回归（不新增，只跑既有套件）
// go test ./internal/config/ -run 'TestAuditGovernance|TestCommercialMachineCredentials|TestAuditSinkL2|TestLoad_|TestValidate_' 全绿
// 其中 TestAuditSinkL2Config_EndpointScheme 对 AuditSinkL2Config{}.Validate()==nil 的既有断言必须保持成立
// （"L2 需 endpoint"在 AuditSinkConfig 选择器层强制，见 §2.3 G5 记录）
```

### AC-2 outbox delivery：kind 选择路由，L0↔L2 配置切换零代码改动（`internal/events/event_outbox_relay_test.go` 扩展，镜像 `TestOutboxRelay_DeliversDeletedFactToL2`）

```go
func TestOutboxRelay_SinkSelectedByConfigKind(t *testing.T) {
	// 同一 relay 构造路径（EventOutboxRelayOptions.AuditSink 注入点），仅配置不同：
	// 1) Kind=L0（或 AuditSink 为 nil）：claim 一条 vault.file.deleted@1.1 事实
	//    ⇒ L2 目标（httptest 计数）零 POST；事实被 complete（status=='delivered'）；
	//    L0 audit_log 行已由删除事务写入（复用既有断言 helper）
	// 2) Kind=L2（同一事实、同一 relay 代码路径）：⇒ L2 目标恰 1 次 POST
	//    （Authorization: Bearer <binding token>）；complete
	// 3) 断言两分支除装配参数外共享同一 deliverFact 状态机——以"测试仅通过
	//    opts.AuditSink 的 nil/非 nil 与 config 结构体差异切换行为"为可执行证据
	//    （配置选择 = 唯一差异源；grep 级断言：deliverFact 内无 kind 分支）
}
```

### AC-3 event schema：payload 到达 sink 字节恒等（JSON golden）

```go
func TestOutboxRelay_L2PayloadByteIdentical(t *testing.T) {
	// 1) 写入 event_outbox 的 payload 字节（schema_test.go golden 钉死 @1.1 envelope：
	//    schema_version/event_type/tenant/bucket/key/object_id/actor/version_id/...）
	// 2) relay claim → L2 POST 的请求体 == event_outbox.payload 原样字节
	//    （verbatim：不 enrich、不重排、不脱敏；sink 侧 httptest 记录 body 逐字节比对）
	// 3) Kind 切换（L0 vs L2）不改变 payload 来源——字节恒等与 kind 无关（单一 payload 生产者）
}
```

### AC-4 组合 e2e：delete 经三种 kind 配置端到端（`internal/integration` harness 扩展，镜像 `TestDeleteResponse_DoesNotBlockOnDelivery`）

```go
func TestComposition_AuditSinkKindEndToEnd(t *testing.T) {
	// 同一服务器装配函数，仅 config 结构体（或环境）不同；PUT → DELETE 事实流：
	// 1) Kind=L2（bearer 变体）：删除 ⇒ 治理端点（httptest sink）收到 deleted@1.1 POST；
	//    audit_log 行存在（L0 常开）
	// 2) Kind=L0：删除 ⇒ 本地 store（audit_log 行断言）；治理端点零 POST
	// 3) Kind=L1：零治理凭据（AUDIT_GOVERNANCE_* 全缺省、无 AUDIT_SINK_L2_*）⇒ 启动成功、
	//    删除 2xx、audit_log 行存在（协议面既有行为不回归）
	// 4) 三种配置下删除响应均不等待投递（既有 TestDeleteResponse_DoesNotBlockOnDelivery 语义复用）
}
```

### AC-5 回归

- `make check` 全绿；`go test ./internal/config/ ./internal/events/ ./cmd/server/ ./internal/integration/` 全绿。
- `config_audit_governance_test.go`（legacy 套件）与 `config_audit_sink_l2_test.go` **零改动**通过——legacy 契约与 WIP 泛型 L2 表面均不回归。
- `internal/auditgovernance`（legacy runtime：租约/重试/脱敏/revision）零改动；`events.AuditSink` 端口签名零改动。

---

## 5. 范围边界（明确不做）

| 不做 | 理由 |
|------|------|
| `events.AuditSink` 端口接口改动（`DeliverDeleted` 签名） | 已落地（sibling 规格 pin 的契约）；本方向只改配置选择层 |
| relay 状态机/退避/租约/载荷处理改动 | sibling 方向（event outbox）所有物；FR-3 只要求"装配点读 Kind" |
| `AUDIT_SINK_L2_*` 键重命名为 `AUDIT_SINK_*` | 已是泛型、无 sibling 命名的表面；重命名是 churn，不在方向内 |
| `internal/auditgovernance` runtime 改动（legacy L2 消费侧） | legacy mapping 原样保留（E5）；其投递机制独立于本方向 |
| auth 面 Snaplink SDK 引用（`config_auth_validate.go`、`config_oidc_test.go`） | AGENTS.md §2.5 既有契约；非审计 sink 表面 |
| 删除 outbox 本身 / @1.1 schema / L0 审计行写入 | sibling 方向已落地；本规格只引用其行为做 AC |
| L1 的具体投递路由设计（deleted@1.1 经协议面的细节） | 属 events 面；本规格只要求 config 能表达 L1 选择 + 无凭据自举 + 装配钩子 |
| legacy bindings 文件/部署示例的去品牌化改名 | 文件路径与键名是存量契约，改名破坏部署；只改注释文案 |

---

## 6. 实现指引（供验收后落地，非本规格交付物）

- **新文件** `internal/config/config_audit_sink.go`（≤500 行）：`AuditSinkConfig{Kind, Endpoint, BindingsFile, Bindings, Legacy}`（或等价合并形状）+ `loadAuditSinkConfig()`（FR-1 派生优先级）+ `AuditSinkConfig.Validate()`（FR-2）。**复用** `loadAuditGovernanceConfig`/`loadAuditSinkL2Config` 两个既有 loader 作为子表面装载，不复制其解析逻辑。
- **`config.go`**：`Load()` 中以 `loadAuditSinkConfig()` 取代两个现有 loader 调用；`Config` 结构体以 `AuditSink AuditSinkConfig` 取代两个平铺字段（或保留字段、合并为选择视图——实现侧自选，测试以 `Config.AuditSink.Kind` 断言为准）。
- **`config_validate.go`**：`Validate()` 中 `c.AuditSink.Validate()` 取代 `c.AuditSinkL2.Validate()`；`validateCommercialIntegrations` 签名更新为组合表面（legacy 规则透传）。
- **`cmd/server/workers.go:166-175`**：`Endpoint != ""` 隐式开关改为按 `cfg.AuditSink.Kind` 三路派生（L0/L1/L2），启动日志输出 kind。
- **文档**：`.env.example` + `docs/configuration.md` 按 FR-5 同步（同 commit）。
- **测试**：AC-1 新增 `config_audit_sink_test.go`；AC-2/AC-3 扩展 `event_outbox_relay_test.go`；AC-4 扩展 `internal/integration`；AC-5 全量回归（**先跑既有套件确认基线，再跑新增项**）。
