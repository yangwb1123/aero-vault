Now I have a thorough understanding of the current architecture. Let me produce the full analysis.

---

# Architecture Analysis: AeroVault Production Readiness — 5-Dimensional Assessment

## 1. 架构评估

### 当前架构优势

| 维度 | 评分 | 说明 |
|------|------|------|
| **分层清晰度** | ★★★★★ | 4 层清晰分离：Protocol → Service → Storage/Repository → Eventing/AI。每层职责单一，无 god 对象穿透。 |
| **接口正交性** | ★★★★★ | `storage.Storage` 仅 15 个方法，`repository.Repository` 领域正交，不泄漏底层细节。 |
| **可测试性** | ★★★★☆ | `FileService` 依赖接口，可 mock；但 Benchmark/Fuzz/Contract 测试为零，暴露了测试生产力工具的缺失。 |
| **协议扩展性** | ★★★★★ | 四种协议（REST/S3/WebDAV/MCP）共享同一 `FileService`，新增协议无须改动业务逻辑。 |
| **安全性基线** | ★★★☆☆ | Auth 和 Tenant 隔离良好，但 TLS、安全响应头、鉴权事件日志缺失，属生产级盲区。 |
| **运维可观测性** | ★★★☆☆ | 15 个 domain metrics + OTel traces 覆盖良好，但缺少容量预测、SLO 框架和成本指标。 |
| **自愈能力** | ★★☆☆☆ | Circuit breaker 存在但纯内存化（重启丢失状态）；Scrub 仅检测不修复；无磁盘空间监控。 |

### 架构债务与技术债

| # | 类型 | 严重度 | 描述 | 位置 |
|---|------|--------|------|------|
| D1 | **可观测性债** | 中 | `RegisterStorageClassGauge` 硬编码 `"default"` tenant，多租户场景数据不准确 | `internal/telemetry/metrics.go:188-193` |
| D2 | **安全债** | 高 | `http.Server` 无 `TLSConfig`、`MaxHeaderBytes`；无 CSP/HSTS/X-Content-Type-Options 中间件 | `cmd/server/main.go` runServer() |
| D3 | **安全债** | 中 | Auth 401/403 仅 `http.Error`，不写入独立安全事件日志，无法 SIEM 集成 | `internal/auth/auth_middleware.go` |
| D4 | **韧性债** | 中 | Circuit breaker 状态纯内存，进程重启后丢失故障记录 | `internal/storage/circuitbreaker.go` |
| D5 | **韧性债** | 中 | Scrub 可以检测 corrupted 对象但无法自动恢复（仅标记 `_aero_scrub_status=corrupt`） | `internal/reconcile/scrub.go` |
| D6 | **韧性债** | 低 | Local storage backend 在 `NewLocal` 中无 `Statfs`/`Statvfs` 检查，磁盘满无告警 | `internal/storage/local.go` |
| D7 | **工程效率债** | 中 | 零 Benchmark 函数、零 Fuzz 函数、零契约测试、集成测试不在 `make check` 中 | 全局 |
| D8 | **工程效率债** | 低 | 无 `.devcontainer`、无热重载、无 `--dev` 模式，新贡献者入职摩擦大 | 根目录 |

**裁决：** 架构整体设计质量高，但生产就绪度的防护性编程（Security、Resilience、Observability）和后端工程基础设施（Benchmark、Fuzz、Contract Test）存在系统性的缺失。这不属于"架构腐烂"，而是**"生产成熟度鸿沟"**——以当前状态投入生产，将在容量事件、安全事件和回归问题上暴露风险。

---

## 2. 扩展方向

### 方向 1：可观测性成熟度平台

#### 为什么需要

当前可观测性体系有 15 个 domain metrics + OTel traces + 9 条告警规则，覆盖 HTTP 吞吐、AI 延迟、完整性检查。但生产运维所需的三个核心维度完全空白：

- **SLO 框架**：无法回答"过去 7 天的 P99 GET 延迟是否在预算内？"或"本月可用性是否达到 99.9%？"
- **成本分析**：无法回答"租户 A 的存储成本是多少？AI token 支出趋势如何？"
- **容量预测**：无法回答"以当前增长速率，磁盘 30 天后会满吗？需要提前扩容吗？"

#### 业务价值 / 技术价值

- **业务**：多租户场景下，成本追踪是计费和内部 chargeback 的前提
- **技术**：容量告警（磁盘满）从"应急"变为"计划内"，消除夜间被叫醒的运作风险
- **运维**：SLO 燃尽率为部署决策提供客观依据，避免人工判断

#### 核心挑战和技术难点

1. **指标分层**：现有 metrics 是平面结构，缺少 SLO-specific 的布尔指标（slIIndicator: 1/0）和多维度聚合（window=30d, filter=status!=5xx）
2. **成本模型**：存储成本需要（字节数 × 每 GB 成本 × 存储时长 × 复制因子），AI 成本有 per-token 和 per-request 两种模式，缺少统一的 cost registry
3. **预测算法**：容量预测需要小时级时序数据积累（至少 2 周）才能建立基线，不可在启动第一天就生效

#### 预期的架构变更

```
┌─────────────────────────────────────────────────────┐
│  SLO Engine (internal/slo/)                         │
│  ├─ SLOConfig{} → 目标值 + 窗口 + 排除过滤器        │
│  ├─ Recorder (装饰 middleware/metrics) → 布尔指标    │
│  └─ BurnRateAlert → SLO 燃尽率告警                  │
├─────────────────────────────────────────────────────┤
│  Cost Registry (internal/cost/)                     │
│  ├─ StorageCostModel{PerGB, ReplicationFactor, …}   │
│  ├─ AICostModel{PerTokenInput, PerTokenOutput, …}   │
│  └─ per-tenant accrual (日级快照 → 审计表)          │
├─────────────────────────────────────────────────────┤
│  Capacity Gauge (已有 telemetry 扩展)               │
│  ├─ Local: statfs → disk_avail_bytes / disk_total   │
│  └─ S3/OSS/COS: 可选的 API 配额查询                 │
└─────────────────────────────────────────────────────┘
```

#### 对现有系统的影响

- **增量集成**：SLO Engine 作为 middleware 装饰器，不影响现有 handler 逻辑
- **零侵入**：Cost Registry 纯计算层，不修改现有 repository 表结构
- **可选性**：所有组件 flag-gated，默认 off，符合 I5 原则
- **风险**：指标基数爆炸——per-tenant × per-endpoint SLO 可能导致 OpenMetrics 暴露规模 ×10，需添加 `-slo-metrics-filter` 白名单

---

### 方向 2：测试基础设施与质量门禁系统

#### 为什么需要

当前 `make check` 仅包含 `fmt → vet → build → test → complexity-lines`。这是单元测试级别的基本门禁。生产级系统需要：

- **性能回归检测**：没有 Benchmark，无法回答"本次 PR 让 P99 延迟变差了吗？"
- **输入边界鲁棒性**：没有 Fuzz，无法发现 panic-invariant 违反
- **API 契约一致性**：OpenAPI spec 与 handler 行为无人验证，改 handler 忘记更新 spec 不会在 CI 捕获
- **集成测试隔离**：`make check` 不含 `test-integration`，意味着 Postgres 和 Qdrant 相关代码仅在人工触发时验证

#### 业务价值 / 技术价值

- **业务**：降低生产回归概率，尤其对 S3 兼容性——S3 客户对行为严格性要求极高
- **技术**：Benchmark 基线 + CI 比对 = 性能回归在 PR 阶段即被发现，避免上线后回滚
- **工程**：Fuzz 持续运行（24h cluster fuzz）能发现代码中 90% 以上的 panic 级 bug

#### 核心挑战和技术难点

1. **Benchmark 基线管理**：Go benchmark 输出在 CI 中比对需要"参考值 + 容忍阈值"机制，不同硬件跑出的数值不可比，需固定 CI runner 或归一化
2. **契约测试框架**：`oapi-codegen` 生成的 Go types 与已有 handler 签名可能不匹配，需要定制中间层
3. **Fuzz 的持续性**：Fuzz 不在 CI 的单次执行中有价值，需要 long-running fuzz 基础设施

#### 预期的架构变更

```
internal/
├── benchmark/              # 新增：跨系统级基准测试
│   ├── bench_suite_test.go    # BenchmarkSuite: CRUD / Multipart / Search
│   └── benchdata/             # 固定 fixture 数据
├── fuzz/                   # 新增：结构化 fuzz 输入
│   └── fuzz_corpus/           # 初始种子语料库
└── contract/               # 新增：契约验证中间件
    └── contract_test.go       # 启动时验证 router → OpenAPI 覆盖
```

#### 对现有系统的影响

- **Benchmark**：纯新增文件，不修改已有代码。只需在 `Makefile` 添加 `bench` target 和 `check` 中的调用
- **Fuzz**：纯新增函数 `func FuzzXxx(f *testing.F)`，依赖标准库
- **Contract Test**：需要读取 `openapi.go` 生成的 spec 和 `router.go` 注册的路由表进行集合比较。作为 CI 步骤执行，不修改运行时
- **集成测试门禁**：需要将 `test-integration` 拆分为"可选本地验证"和"CI gate 的轻量验证"（用 testcontainers-go 替代手动 Docker 管理）

---

### 方向 3：开发者体验与生态

#### 为什么需要

当前项目有 4 种协议适配器 + AI 管线 + CLI + MCP，新贡献者入职需理解 ~50K 行 Go 源码。无 `.devcontainer` 意味着 IDE 环境配置完全靠手；无热重载意味着每次代码修改需要 `Ctrl+C → go run → 等待编译`；无 `--dev` 模式意味着调试 AI 功能需要真正连接 embedder/LLM。

#### 业务价值 / 技术价值

- **业务**（间接）：降低贡献门槛→提高 PR 频率→更快交付特性
- **技术**：`--dev` 模式启用内置 mock（`MockLLM`、`HashEmbedder` 已存在），开发者可离线测试完整 AI 链路

#### 核心挑战和技术难点

1. **.devcontainer 与 Docker Compose 的关系**：现有 `docker-compose.yml` 只含 postgres + minio + app，需要增加 `.devcontainer/devcontainer.json` 引用 compose 服务，但需避免镜像膨胀（devcontainer 内需 go + gocyclo + tools）
2. **热重载工具选型**：`air` 是最成熟的 Go 热重载方案，但需 `.air.toml` 配置，且需确保 `go vet` 在重载前运行（避免推送语法错误到正在运行的进程）
3. **`--dev` 模式的范围界定**：需要决定 dev mode 影响哪些子系统——仅限于 AI mock，还是也包括存储（内存存储）、认证（通配 allow）、事件（本地日志）？

#### 预期的架构变更

```
.devcontainer/
├── devcontainer.json      # 新增：VS Code / GitHub Codespaces
├── Dockerfile             # 新增：开发镜像（含 go+gocyclo+air+gotestsum）
└── docker-compose.yml     # 新增：覆盖服务（增加端口暴露和卷挂载）
├── .air.toml              # 新增：热重载配置
└── Makefile 扩展
    ├── dev-watch          # → air
    └── dev                # → go run -- -dev
```

#### 对现有系统的影响

- **`.devcontainer/` 不影响运行时**：纯开发环境配置，不改变任何 Go 源码
- **`--dev` 模式**：在 `cmd/server/main.go` 中添加约 30 行 flag 处理，使用已有的 `ai.MockLLM` 和 `ai.HashEmbedder`
- **`Makefile` 扩展**：不修改现有 target，仅新增
- **风险**：`--dev` 模式下需要明确文档说明"不要用于生产"，避免安全事故

---

### 方向 4：存储层自愈与韧性

#### 为什么需要

当前存储层有 circuit breaker（内存）和 scrub（检测不修复）两重保护，但缺少：

- **磁盘容量保护**：Local 后端写入前不检查可用空间，磁盘满时 Put 操作返回不可预测的错误
- **持久化故障状态**：CB 状态重启丢失，上线后故障后端会立刻触发新一轮故障风暴
- **自动修复**：Scrub 检测到 corrupt 对象后只标记，不恢复（如果有其他副本或版本）

#### 业务价值 / 技术价值

- **业务**：多租户环境中，一个租户的恶意写入撑满磁盘会导致全部租户不可用——磁盘配额保护是跨租户隔离的最后一道防线
- **技术**：自动修复（从上一版本恢复或从副本复制）将 4 小时的人工人侵恢复时间降为秒级自动化

#### 核心挑战和技术难点

1. **磁盘检查的竞态条件**：`statfs + Put` 之间有非原子窗口——写入恰好发生在磁盘满的瞬间，无法 100% 预防，需要结合写入后校验
2. **多版本自动修复的策略**：如果最新版本 corrupt 但历史版本 intact，自动替换会丢弃"修复后将历史版本 re-version"还是"原地替换"？需明确策略并幂等实现
3. **CB 持久化的写入开销**：每次状态变更写 repository（SQLite/Postgres）会增加 ~5ms 延迟，需要在"重启后快速恢复"和"运行时开销"间平衡

#### 预期的架构变更

```
internal/storage/
├── local.go 扩展
│   └── NewLocal → statfs 检查 → 注册磁盘 gauge
├── circuitbreaker.go
│   └── persistentState           # 新增：关键状态迁移写 repository
│       ├── StateStore (interface)  → SQLite/Postgres 实现
│       └── Recovery: 启动时从 state store 恢复 lastFailure

internal/reconcile/
├── scrub.go 扩展
│   └── autoRepair                # 新增：corrupt 检测后自动恢复
│       ├── version-restore: 从历史版本恢复
│       └── tag-corrupt: 无法恢复时标记为垃圾（GC 可清理）
```

#### 对现有系统的影响

- **statfs 检查**：纯新增，不影响现有行为
- **CB 持久化**：新增 `StateStore` 接口和 repo 实现，CB 逻辑核心不变
- **Scrub 修复**：需要 `FileService.CopyObject` 或 `storage.Put` 权限——需要确认 FileService 层不会对 scrub 操作施加配额检查（因为这是系统内部操作，不应计入租户配额）
- **风险**：自动修复在错误判断（false positive scrub）时可能覆盖完好数据，需要引入"human-in-the-loop"阈值——例如连续 3 次 scrub 都失败才触发自动修复

---

### 方向 5：生产安全纵深防御

#### 为什么需要

当前安全体系已有 Auth（Bearer/SigV4）、Tenant 隔离、Scope 校验，但从纵深防御角度：

- **传输层**：无 TLS，所有流量明文（包括 API key）
- **应用层**：无 `MaxHeaderBytes`（HEADER 注入攻击）、无安全响应头（CSP 防 XSS、HSTS 防 SSL Strip）
- **审计**：401/403 不被记录为安全事件，无法 SIEM 集成
- **Secrets 管理**：`SecretProvider` 接口存在，但只有 local keyfile 和 HTTP provider，没有 AWS Secrets Manager / HashiCorp Vault 原生集成

#### 业务价值 / 技术价值

- **业务**：合规要求（SOC2、HIPAA、PCI）对传输加密和安全审计有明确要求，缺失即不合规
- **技术**：`SecretProvider` 接口已存在，新增 AWS SM / Vault provider 是纯增量实现，不改变现有 envelope 加密流程

#### 核心挑战和技术难点

1. **TLS 配置复杂性**：需要支持自动 ACME（Let's Encrypt）和手动证书两种模式，且 TLS 配置应作为 `http.Server` 的额外配置而非全局 flag
2. **安全响应头中间件的选择**：是否允许客户端配置？CSP 策略过于严格可能破坏 Web UI 功能（当前 `/ui` 是 embedded SPA，内联脚本需要 CSP nonce）
3. **安全事件日志的格式**：需要与常见 SIEM（Splunk、ELK、Datadog）兼容的结构化 JSON schema

#### 预期的架构变更

```
internal/middleware/
├── tls.go                  # 新增：TLS 配置（ACME / 证书文件）
├── security_headers.go     # 新增：CSP / HSTS / X-Content-Type-Options
└── security_logger.go      # 新增：鉴权失败事件日志

internal/auth/
├── auth_middleware.go 扩展
│   └── 401/403 → telemetry.IncSecurityEvent(…)

internal/storage/
├── secret.go 扩展
│   └── awsSMProvider        # 新增：AWS Secrets Manager
│   └── vaultProvider        # 新增：HashiCorp Vault KV

cmd/server/main.go
├── http.Server 扩展
│   ├── TLSConfig
│   └── MaxHeaderBytes = 1MB
└── middleware chain 扩展
    ├── SecurityHeaders（最外层）
    └── SecurityLogger（最内层，捕获所有中间件的拒绝）
```

#### 对现有系统的影响

- **TLS**：纯新增配置，不影响现有 `runServer` 的 HTTP 路径
- **安全响应头**：新增 middleware，按 chain 顺序放在 RequestID 之前
- **Security Logger**：新增 middleware 放在 chain 最内层（handler 执行后），可以捕获 auth/rate-limit/recoverer 的所有拒绝
- **Secret Provider 扩展**：实现接口，无 breaking change
- **风险**：TLS 断连后如果 panic 处理不当可能导致非 TLS 端口暴露，需要 `--tls-disable` flag 控制

---

## 3. 接口设计建议

### 3.1 关键抽象层的接口设计原则

当前架构的接口设计质量已经很高，但针对 5 个扩展方向，以下是需要新增或调整的接口：

#### SLO Engine 接口设计原则

```go
// 原则：装饰器模式，不侵入现有 handler/middleware
type SLOConfig struct {
    Name      string        // e.g. "get_object_p99"
    Target    float64       // e.g. 500 (ms)
    Window    time.Duration // e.g. 30d
    Exclude   func(ctx context.Context, req *http.Request) bool // 可选排除
}

type SLORecorder interface {
    Record(ctx context.Context, duration time.Duration, success bool)
    // success=true 计入 numerator，false 不计（但有窗口分母）
    // 底层输出布尔指标：slo_{name}_window_good / slo_{name}_window_valid
}
```

**设计理由**：不与现有 middleware chain 耦合，SLO Recorder 作为 OTel middleware 的装饰器存在。`success` 由调用方根据 status code 决定（可能跨 middleware 边界）。

#### Cost Registry 接口设计原则

```go
// 原则：注册式，可扩展的成本模型
type CostModel interface {
    Name() string                          // "storage.local", "ai.embed.ollama"
    Cost(ctx context.Context, usage CostUsage) (micros int64, err error)
}

type CostUsage struct {
    Tenant   string
    Quantity float64 // bytes stored, tokens consumed, requests made
    Unit     string  // "byte-seconds", "tokens", "requests"
}

type CostRegistry struct {
    models map[string]CostModel
}

// 存储成本示例：bytes × perGBPerHour × hours
// AI 成本示例：tokens × perTokenCost
```

**设计理由**：`CostModel` 接口保持极简，每种成本类型独立注册。`micros`（百万分之一美元）与现有 `ai.cost_micros` 指标单位一致。

#### CB StateStore 接口设计原则

```go
// 原则：与 circuitBreaker 正交，可插拔存储
type StateStore interface {
    Load(ctx context.Context) (open bool, lastFailure time.Time, err error)
    Save(ctx context.Context, state CBState, lastFailure time.Time) error
}
```

**设计理由**：不强依赖 repository，存储实现可以是 repository 表、Redis 或内存（现有行为）。默认 `noopStore` 维持当前行为，opt-in 开启持久化。

### 3.2 是否需要新的抽象层

| 方向 | 新抽象层 | 必要性 | 理由 |
|------|---------|--------|------|
| SLO | `slo.Recorder` + `slo.Engine` | **必要** | 现有 metrics 是平面结构，无法表达 SLO 的窗口/排除逻辑 |
| 成本 | `cost.Registry` + `cost.Model` | **必要** | 当前成本指标只有 AI token 一条路径（`ai.cost_micros`），缺少统一的成本 accrual 框架 |
| 契约测试 | `contract.Validator` | **推荐** | 可避免 OpenAPI spec 与 handler 的 drift，但也可用 CI shell script 替代（开销更低） |
| 持久化 CB | `storage.StateStore` | **可选** | 可以用 `repository` 方法直接实现，不需要独立接口层 |
| Security Logger | `middleware.SecurityLogger` | **不建议** | 作为 middleware 实现即可，不需要抽象层 |

**决策建议**：SLO Engine 和 Cost Registry 建议作为独立的 `internal/slo/` 和 `internal/cost/` 包引入，因为它们有独立的领域模型和循环依赖限制。其他建议保持轻量实现。

### 3.3 向后兼容性策略

| 变更 | 兼容策略 |
|------|---------|
| 新增 middleware（SecurityHeaders、SecurityLogger） | 默认 off（flag-gated），不会改变现有请求路径 |
| SLO Engine | 纯新增，不修改现有 metrics schema |
| Cost Registry | 纯新增，不与现有 `RecordAIUsage` 冲突（可共存，下阶段再迁移） |
| CB StateStore | 添加时默认 `noopStore`，现有行为完全不变 |
| Scrub 自动修复 | 新增 `RECONCILE_SCRUB_AUTO_REPAIR` flag，默认 false |
| `--dev` 模式 | 新增 flag，默认关 |
| `.devcontainer` | 不影响运行时 |
| TLS | 新增 `--tls-cert`/`--tls-key` flag，无 TLS 时行为不变 |
| Security Logger | 新增 `SECURITY_EVENT_LOG` flag，默认关 |

**唯一可能的不兼容点**：Benchmark 测试如果放在 `internal/benchmark/` 包，需在 `Makefile` 中以 `-bench=.` 显式调用，不影响 `go test ./...`。

---

## 4. 技术选型

### 4.1 是否需要引入新技术栈

| 方向 | 建议 | 推选技术 | 理由 |
|------|------|---------|------|
| SLO Engine | **自建** | 纯 Go + OTel metrics | SLO 核心逻辑 ~200 行，不需要独立框架。Go 生态的 `sloth` 太重且与 OTel 绑定过紧 |
| Cost Registry | **自建** | 纯 Go | 业务逻辑简单（乘法+聚合），无现成 Go 库可满足 per-tenant 建模需求 |
| 契约测试 | **工具选型** | `oapi-codegen` + 自定义验证 | `oapi-codegen` 已广泛使用，与其生成 types 不如校验 spec 与实现的一致性。备选：`spectral`（Lint 级）+ `dredd`（运行时验证） |
| Fuzz | **标准库** | `testing/fz` | Go 1.25 原生支持，零依赖 |
| 热重载 | **工具选型** | `air` | 最成熟的 Go 热重载（12K+ stars），配置简单。备选：`reflex`（更轻量但功能较少） |
| .devcontainer | **模板** | 官方 devcontainers/go | 直接引用 `mcr.microsoft.com/devcontainers/go` 镜像 |
| TLS ACME | **工具选型** | `autocert`（golang.org/x/crypto/acme/autocert）| 标准库扩展，零外部依赖，自动 Let's Encrypt 证书管理 |
| Vault/AWS SM | **自建 Provider** | 官方 SDK | `SecretProvider` 接口已定义，新增 provider 只需实现两个方法 |

### 4.2 第三方依赖评估标准

```
┌──────────────────────┬───────────────────────────────────┐
│ 评估维度             │ 阈值                              │
├──────────────────────┼───────────────────────────────────┤
│ 许可证兼容性         │ 必须 Apache 2.0 / MIT / BSD      │
│ 依赖传递数           │ 新增传递依赖 ≤ 5                 │
│ 主流维护             │ GitHub stars > 500 + 90天内commits│
│ API 稳定性           │ v1.x 或承诺向后兼容              │
│ CGO 要求             │ 禁止（CI gate 需要 pure-Go）     │
│ 安全审计历史         │ 无已知 CVE 未修复                │
│ 与现有 go.mod 冲突   │ 无版本冲突（主要指 OTel、pgx）   │
└──────────────────────┴───────────────────────────────────┘
```

**当前 go.mod 依赖评估**：

```
现有依赖（主要）：
├── github.com/go-chi/chi/v5           ✅ 路由
├── github.com/google/uuid              ✅ 轻量
├── go.opentelemetry.io/otel            ✅ 可观测性标准
├── github.com/jackc/pgx/v5             ✅ Postgres 驱动
├── modernc.org/sqlite                  ✅ Pure-Go SQLite
├── github.com/aws/aws-sdk-go-v2        ✅ S3 存储后端
└── ... (阿里云 OSS/腾讯云 COS SDK)

如果新增：
├── golang.org/x/crypto/acme/autocert   ✅ 零新增 dep（已在间接依赖中）
├── github.com/air-verse/air            ⚠️ 开发工具，不在 go.mod 中
├── github.com/testcontainers-go        ⚠️ 仅测试依赖
```

**决策**：方向 1-5 的新增依赖评估均为绿色（零新增运行时依赖 或 工具链依赖不在 go.mod 中）。

### 4.3 自建 vs 采购决策矩阵

| 组件 | 自建成本 | 采购方案 | 推荐 | 理由 |
|------|---------|---------|------|------|
| SLO Engine | 2-3 天 | Datadog SLO / Grafana SLO | **自建** | 业务逻辑极简，200 行；外部方案需要额外 SaaS 成本且与本地 OTel/ Prometheus 绑定 |
| Cost Analyzer | 3-5 天 | CloudZero / Vantage | **自建** | 成本模型需要按 per-tenant 精准拆账，外部方案无法获取内部 per-object 数据 |
| Fuzz Infra | 0.5 天 | OSS-Fuzz / GitLab Fuzz | **自建**（+CI 触发） | Fuzz 是标准库能力，只需要 CI 定时触发；OSS-Fuzz 集成成本高但对个人项目过剩 |
| Secret Mgmt | 1-2 天/provider | Vault / AWS SM | **自建 Provider** | 接口已定义，实现即插即用；Vault 企业版 License 成本高 |
| Contract Test | 1-2 天 | Spectral Pro / Stoplight | **自建** | 验证逻辑简单（router vs spec 集合比较），不需要商业工具 |

---

## 5. 实施路线图

### 优先级排序（调整后）

| 优先级 | 方向 | 关键依赖 | 预估工期 | 风险系数 |
|--------|------|---------|---------|---------|
| **P0** | 方向 5 (Security) — 快速胜利 | 无 | 2-3 天 | ★☆☆☆☆ |
| **P0** | 方向 1 (Observability) — Metrics 扩展 | 无 | 3-5 天 | ★★☆☆☆ |
| **P1** | 方向 3 (DevEx) — 基础设施 | 无 | 1-2 天 | ★☆☆☆☆ |
| **P1** | 方向 2 (Testing) — Benchmark/Fuzz/Contract | 无 | 3-5 天 | ★★☆☆☆ |
| **P2** | 方向 4 (Self-Healing) — 深度韧性改造 | 方向 1 的磁盘 gauge | 5-10 天 | ★★★★☆ |

**调整理由**：方向 5（TLS + Security Headers + Security Logger）是最高 ROI/最低成本项，2-3 天补齐安全短板，建议首批执行。方向 4（自愈）改造深度最大、风险最高，需方向 1 提供磁盘容量可见性后再启动。

### 阶段划分与里程碑

#### 阶段 1：安全快速胜利（第 1 周）

```
Day 1-2:
├── TLSConfig (autocert / 证书文件) → http.Server
├── MaxHeaderBytes = 1MB
└── security_headers.go middleware (CSP / HSTS / X-Content-Type-Options)

Day 2-3:
├── security_logger.go middleware
├── auth_middleware.go: 401/403 → security event
└── OpenAPI spec 补充 securitySchemes.tls
```

**里程碑 M1**：`make check` + `curl -I https://localhost:8443` 返回安全响应头 + TLS 连接成功。安全事件日志 tail 可见。

#### 阶段 2：开发者体验 + 可观测性扩展（第 2-3 周）

```
Week 2:
├── .devcontainer / .air.toml / Makefile dev-watch
├── --dev flag + MockLLM/HashEmbedder 自动启用
└── `make dev` = 一键启动开发环境

Week 3:
├── statfs gauge → disk_avail_bytes / disk_total
├── RegisterStorageClassGauge: 修复硬编码 "default" bug
├── Cost Registry: storage + AI cost models
└── SLO Engine: Recorder + BurnRateAlert (alerts.yml 扩展现有规则)
```

**里程碑 M2**：开发者可用 `make dev` 启动完整离线开发环境。Grafana 面板新增磁盘容量和成本的三个面板。

#### 阶段 3：测试基础设施（第 4 周）

```
Week 4:
├── Benchmark: CRUD + Multipart + Search 基准函数
├── Fuzz: metadata/ 输入绑定的 fuzz 函数
├── Contract Test: router → OpenAPI spec 一致性验证
└── Makefile: check 扩展 bench → 性能基线比对
```

**里程碑 M3**：`make check` 包含 Benchmark 基线 + Fuzz 种子生成 + Contract 验证。任何 OpenAPI spec 与 handler 的不一致在 CI 被拒绝。

#### 阶段 4：存储层自愈（第 5-6 周）

```
Week 5:
├── CB StateStore (SQLite/Postgres 实现)
├── 启动时从持久化状态恢复 CB
└── Scrub 自动修复 (flag-gated, 默认 off)

Week 6:
├── 自动修复策略: 多版本恢复 / 副本复制 / 标记垃圾
├── 增加 Scrub 修复的 metrics + alerts.yml 扩展
└── human-in-the-loop 阈值 (连续 3 次失败才触发)
```

**里程碑 M4**：存储后端故障后重启自动恢复 CB 状态，不再引发故障风暴。Scrub 检测到的 corrupt 对象自动从历史版本恢复。

### 风险点与缓解策略

| 风险 | 等级 | 缓解策略 |
|------|------|---------|
| SLO 指标基数爆炸（per-tenant × per-endpoint） | 中 | 添加 `-slo-metrics-filter` 白名单，默认仅跟踪 `GET` 和 `PUT` 的高频端点 |
| Cost Registry 的 accrual 精度与 Repository 写入频率冲突 | 中 | 使用 bucket 式累加（每 5 分钟 flush 一次），而非每个请求写入 |
| 自动修复触发 false positive（Scrub 误判导致数据覆盖） | 高 | ① 连续 3 次 scrub 失败才触发修复；② 每次修复前写 WAL 记录可回滚点；③ 默认 off，仅手动启用 |
| CB 持久化的写入增加关键路径延迟 | 低 | StateStore 设计为异步写入（goroutine + buffer），同步路径仍保持在内存中 |
| `.devcontainer` 镜像体积过大导致首次 pull 慢 | 低 | 使用分阶段构建（multi-stage），dev image 共享 base layer |
| Benchmark 基线在 CI runner 不同硬件上不可比 | 中 | 使用 `benchstat` 的 delta 模式（与历史值比对，而非绝对值），或固定 CI runner 类型 |
| TLS 配置错误导致生产断连 | 中 | 添加 `--tls-disable` 紧急开关，用于 TLS 证书过期时的快速恢复；同时添加证书过期告警（alerts.yml） |
| OpenAPI 契约测试在 API 版本过渡期频繁失败 | 低 | Contract Validator 设计为 warning 模式（不阻断 CI）和 enforce 模式（阻断 CI），版本过渡期使用 warning |

---

## 总结

| 维度 | 当前状态 | 目标状态（6 周后） |
|------|---------|------------------|
| 安全纵深 | TLS 无 · 无安全头 · 无鉴权事件日志 | TLS + 安全头 + 安全日志，SOC2-ready |
| 可观测性 | 15 metrics + 9 条告警 · 无 SLO · 无成本 · 无容量 | SLO + Cost + 容量三面板就位，12+ 条告警覆盖 |
| 开发者体验 | 无 dev container · 无热重载 · 无 dev 模式 | `make dev` 一键启动，`.devcontainer` 开箱可用 |
| 测试基础设施 | 零 Benchmark · 零 Fuzz · 零 Contract · 半隔离集成测试 | 基准线 + 模糊测试 + 契约验证全绿 |
| 存储层自愈 | CB 纯内存 · Scrub 只检不修 · 无磁盘保护 | CB 持久化 + 自动修复 + 磁盘容量告警回路 |

这份路线图的编排原则是：**低成本高收益先行**（安全头/TLS 2-3 天），**工程基础设施紧随其后**（DevEx + Test infra），**深度韧性改造最后**（自愈需要前面所有的观测能力支撑）。每阶段结束都有一个可演示、可部署、可回滚的里程碑，确保投资回报可在 1-2 周内体现。
