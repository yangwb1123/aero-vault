# AeroVault 高价值扩展方向（第十一期）

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全局代码扫描（~50K 行 Go 源码），逐行审阅 `internal/` 全部 23 个子包、`cmd/server/main.go`、全部 10 期 expansion 文档（`expansion-directions.md` ~ `expansion-v10-undiscovered-horizons.md`）、`ROADMAP.md`、`CHANGELOG.md`、`HARNESS.md`、`Makefile`、`Dockerfile`、`docker-compose.yml`、`deploy/` 全部清单、`go.mod` 与 `sdk/` 全部代码。  
> 选取 5 个**既有 10 期文档与 ROADMAP 均未系统讨论**的工程方向。  
> **日期：** 2026-07-10  
> **原则：** 不编写任何实现代码。每个方向附带：代码锚点 → 当前状态 → 缺口分析 → 边界情况暴露 → 架构蓝图 → 实现理由。

---

## 审阅摘要：前 10 期已覆盖的范围（验证去重）

| 覆盖类别 | 对应期数 | 状态 |
|---------|---------|------|
| AI 管线（检索/Embedding/Chat/Agent/PII/Cache/Indexer） | v1~v10, ROADMAP #1~#2 | 9×+，深度覆盖 |
| S3 兼容性（Policy/CORS/Logging/Notification/SSE-C） | v8, v9, v10, ROADMAP #7 | 4× |
| 存储后端（OSS/COS/KMS/SSE/Encryption/Decoupling） | v4~v10, ROADMAP #5 | 7×+ |
| 多租户/配额/预算/计费 | v3, v4, v7, ROADMAP #2, #4 | 4× |
| 事件系统（Webhook/Postgres Transport/Bus/SSE 韧性） | v6, v8, v9 | 3× |
| 身份联邦/SSO/OIDC/SAML/SCIM | v5 | 1× |
| 合规（WORM/Legal Hold/生命周期治理/Disposition） | v6, v9 | 2× |
| 跨区域复制/Active-Active/冲突检测 | v9 | 1× |
| CDC 流/可回放变更日志 | v9 | 1× |
| WASM 函数/事件触发计算 | v9 | 1× |
| 内容去重/CAS（内容寻址存储） | v7 | 1× |
| 结构化元数据 Schema | v7 | 1× |
| 备份/快照/容灾 | v8 | 1× |
| 客户端加密/SSE-C/零信任 | v10 | 1× |
| 对象级访问审计 | v10 | 1× |
| 跨存储后端数据迁移 | v10 | 1× |
| API 版本治理/SDK 兼容性 | v10 | 1× |
| 优雅关闭/生产级部署韧性 | v10 | 1× |
| 内容分类/DLP 框架 | v8 | 1× |
| 跨协议并发一致性/分布式锁 | v8 | 1× |
| 冷存储/Deep Archive/Restore | v5 | 1× |
| 批量操作/文件夹管理 | v3 | 1× |
| 浏览器直传/Resumable Upload | v7 | 1× |
| Web UI / CLI / MCP | v8 | 1× |
| Postgres 连接池/Read Replica | v8 | 1× |

**本期选点原则：** 选取上述矩阵中**零覆盖**的方向，且满足：① 决定系统是否具备"生产就绪"（Production Readiness）属性；② 与现有架构无缝增量集成；③ 有明确的边界情况和 edge cases 暴露；④ 存在具体代码锚点。

---

## 本期方向总览

| # | 方向 | 类型 | 影响 | 核心代码锚点 | 既有覆盖 |
|---|------|------|------|-------------|---------|
| 1 | **可观测性成熟度平台：SLO 框架、成本分析、容量预测** | 运维/平台 | 🔴 缺少仪表盘可操作性和业务可观测性 | `internal/telemetry/metrics.go:79-81`, `internal/telemetry/prometheus.go`, `deploy/prometheus/prometheus.yml`, `deploy/grafana/` | **零覆盖** |
| 2 | **测试基础设施与质量门禁系统** | QA/工程 | 🔴 集成测试不可靠、无性能基线、无契约测试 | `Makefile:test-integration`, `Makefile:check`, `internal/integration/` | **零覆盖** |
| 3 | **开发者体验与本地开发生态** | 工程/效率 | 🟠 新贡献者入职门槛高、开发反馈周期长 | `cmd/server/main.go`, `Dockerfile`, `docker-compose.yml`, `go.mod` | **零覆盖** |
| 4 | **存储层自愈与运维韧性** | 运维/可靠性 | 🟠 磁盘满/后端故障后恢复时间不可控 | `internal/storage/local.go`, `internal/storage/circuitbreaker.go`, `internal/reconcile/scrub.go` | **零覆盖** |
| 5 | **生产安全纵深防御：传输安全、Secret 集成、输入加固** | 安全/合规 | 🟠 无 TLS/mTLS、无 Secret 管理、无输入安全边界 | `cmd/server/main.go`, `internal/config/config.go`, `internal/api/rest/handler.go` | **零覆盖** |

---

## 1. 可观测性成熟度平台：SLO 框架、成本分析、容量预测

### 当前状态

**基础 OTel + Prometheus 指标已就绪，但停留在"原始数据"层面，缺乏从指标到决策的完整链条。**

```go
// internal/telemetry/metrics.go
// 当前 15 个 domain 指标：ai.requests, ai.tokens, ai.cost_micros, 
// ai.embed_requests, ai.embed_tokens, ai.search.duration_ms, 
// ai.embed.duration_ms, reconcile.orphan_blobs, events.dropped, 
// indexer.skip_total, jobs.*, scrub.*, webhook.retries_total
//
// 以及 3 个 gauge：jobs.pending, storage.class_objects, storage.bytes/objects
```

**已做（但不够）：**

| 维度 | 当前实现 | 状态 |
|------|---------|------|
| AI 指标 | Token 计数、延迟、成本微元 | ✅ 基础 |
| 存储指标 | 字节/对象计数、存储类分布 | ✅ 但 `storage.class_objects` 仅采样 `"default"` tenant |
| 作业指标 | 完成/失败/重试计数 | ✅ 基础 |
| HTTP 指标 | 请求数、延迟 | ✅ `telemetry/http.go` |
| 事件指标 | dropped 计数 | ✅ 基础 |

**未做（缺口）：**

| 维度 | 缺口 | 严重性 |
|------|------|--------|
| **SLO 框架** | 无服务等级目标定义、无错误预算消耗跟踪、无燃尽率告警 | 🔴 无法回答"系统是否健康" |
| **成本分析** | 无存储成本（$/GB/tenant/class）、无 AI API 成本趋势、无 per-operator 成本归因 | 🔴 SaaS 运营不可见 |
| **容量预测** | 无存储增长趋势、无索引/向量库增长趋势、无 DB 连接/磁盘空间预 | 🟠 生产容量事件被动应对 |
| **仪表盘自动化** | Grafana 2 个 JSON 仪表盘需手动导入，无版本化、无 provisioning | 🟠 部署变更后仪表盘不同步 |
| **告警规则** | 仅有 1 个 `alerts.yml`（未读其内容），无运行时自动告警配置 | 🟠 指标无告警=未观测 |
| **Per-tenant 可观测性** | `storage.class_objects` 只采样 default tenant | 🔴 多租户环境不可用 |
| **依赖健康** | 无 storage backend 延迟/吞吐/错误的 P99 指标 | 🟠 后端退化无感 |
| **业务指标** | 无活跃租户数、无活跃 API Key 数、无对象增长/删除率 | 🟠 业务增长无法追踪 |

**现有 Grafana 仪表盘的文件状态：**

```
deploy/grafana/
├── aero-vault-ai-ops-dashboard.json    # AI 运维仪表盘
└── aero-vault-dashboard.json           # 通用仪表盘
```

两个 JSON 文件存在，但无 `provisioning/dashboards/` 目录结构，需用户手动导入。**非 infrastructure-as-code**。

**Prometheus 告警规则的存在性：**

```
deploy/prometheus/
├── prometheus.yml
└── alerts.yml   # 告警规则文件
```

但 `alerts.yml` 当前内容未知（需读取确认具体规则）。即便存在，Prometheus 规则需要通过 ConfigMap 挂载到 Prometheus 实例——没有 Kubernetes 部署的自动化集成。

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/telemetry/metrics.go:79-81` | `mAIEmbedRequests` / `mAIEmbedTokens` 定义 | 无 `storage.bytes_per_class_per_tenant` 、无 `storage.cost_per_tenant` |
| `internal/telemetry/metrics.go` | `RegisterStorageClassGauge` 内 `fn(ctx, "default")` | **硬编码 `"default"` tenant** — 多租户部署中只采样一个租户 |
| `internal/telemetry/http.go` | HTTP 请求延迟/计数器 | 无 per-endpoint latency（当前是全局 `http.server.duration_ms`） |
| `internal/telemetry/prometheus.go` | Prometheus exporter 注册 | 无自定义聚合/直方图桶配置 |
| `deploy/grafana/` | 2 个 JSON 仪表盘 | 无 provisioning + 无 dashboard-as-code 管线 |
| `deploy/prometheus/prometheus.yml` | scrape 配置 | `storage.class_objects` 的 `labeldrop` 正则可能丢失重要标签 |
| `deploy/prometheus/alerts.yml` | 告警规则 | 未读内容，但至少缺少 capacity-related 告警 |
| `internal/telemetry/otel.go` | OTel SDK setup | 无 span 采样策略配置、无 tail-based sampling |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **多租户存储报表** | 50 个租户，各不同用量 | `storage.class_objects` 只采样 "default"——其余 49 个租户无数据 | 采样所有活跃租户，按 tenant label 区分 |
| **SLO 消耗 90%** | 一个月内错误预算消耗 90%，即将违反 SLO | 无告警 | 燃尽率 > 90% → P2 告警；> 100% → P1 |
| **存储成本爆炸** | 某租户一夜写入 10TB，存储成本飙升 | 无成本指标，账单周期结束才察觉 | `storage.cost_usd_per_tenant` gauge + 日费用告警 |
| **磁盘空间耗尽前** | local backend 磁盘剩余 5% | 无告警 | 磁盘使用率 > 85% → P3 告警；> 95% → P1 |
| **P99 检索延迟退化** | 检索 P99 从 200ms 涨到 2s | 无告警（只有原始直方图，无 SLO） | P99 > 1s → P2 告警 |
| **Grafana 仪表盘漂移** | 部署升级后指标名变更，仪表盘全 show No Data | 无检测，用户上报问题 | 仪表盘作为 code 管理 + CI 验证指标存在 |
| **零成本租户检测** | 一个租户 AI 费用为零（配置错误，无 indexer 运行） | 无通知 | 日费用为零的活跃租户 → 通知运维检查 |

### 架构蓝图

```
┌─ 可观测性框架扩容 ───────────────────────────────────────────│
│ 1. SLO 定义层（配置驱动）                                       │
│    type SLO struct {                                            │
│        Name        string  // "storage_read_latency_p99"        │
│        Target      float64 // 99.9%                            │
│        Window      string  // "30d"                            │
│        Measurement string  // "http.server.duration_ms"        │
│        Filter      map[string]string // {endpoint: "/v1/files/*"}│
│        BurnRateAlerts []BurnRateAlert                          │
│    }                                                            │
│    配置来源：环境变量 / YAML / 管理 API                          │
│    → 启动时注册到 Prometheus Recording Rules                     │
│                                                                  │
│ 2. 成本分析指标                                                  │
│    新增 metrics:                                                 │
│      storage.bytes_per_class{tenant, class}      // by storage class │
│      storage.cost_usd{tenant, class}             // estimated cost    │
│      ai.cost_usd{tenant, model}                  // $ 而非 micros     │
│      ai.cost_usd_trend{tenant, period}           // 7d/30d 趋势      │
│                                                                  │
│    成本计算来源：                                                │
│      配置中声明：STORAGE_COST_PER_GB={STANDARD:0.023, IA:0.0125} │
│      → gauge 自动计算 storage_bytes * cost_per_gb / 1073741824   │
│                                                                  │
│ 3. 容量预测（Grafana + Prometheus）                               │
│    使用 Prometheus 的 predict_linear() 函数 · 30d 斜率预测        │
│      predict_linear(storage_bytes{tenant="x"}[30d], 86400*90)    │
│    → 预测 90 天后存储用量                                        │
│    → 超过阈值(85% capacity) → 告警                               │
│                                                                  │
│ 4. Dashboard as Code (Grafana Provisioning)                      │
│    deploy/grafana/provisioning/dashboards/                       │
│    ├── aero-vault-overview.json      // 全局总览                  │
│    ├── aero-vault-tenant.json        // 租户级（支持 template）   │
│    ├── aero-vault-ai-cost.json       // AI 费用                  │
│    ├── aero-vault-storage.json       // 存储趋势                  │
│    └── aero-vault-slo.json           // SLO 燃尽率               │
│                                                                  │
│    Kubernetes: ConfigMap 挂载到 grafana-dashboards-config         │
│    make dashboards-export → 从 Grafana API 导出 JSON（反同步）    │
│    make dashboards-validate → 验证 JSON 中使用的指标在代码中存在   │
│                                                                  │
│ 5. 修复多租户 gauge                                              │
│    RegisterStorageClassGauge 当前仅采样 "default" tenant：        │
│      func(ctx context.Context, o metric.Int64Observer) error {   │
│          for cls, count := range fn(ctx, "default") {            │
│              o.Observe(count, ...)                               │
│          }                                                        │
│      }                                                            │
│    改为采样所有活跃租户（通过 repo.ListTenants 获取租户列表）：     │
│      for _, tenant := range tenants {                            │
│          for cls, count := range fn(ctx, tenant) {               │
│              o.Observe(count, attr("tenant", tenant)...)         │
│          }                                                        │
│      }                                                            │
└────────────────────────────────────────────────────────────────┘

**为什么现在做：** 可观测性是生产运维的基石——没有 SLO 就无法量化服务质量，没有成本分析就无法驱动优化决策，没有容量预测就只能被动应对磁盘满事件。当前 15 个 domain 指标提供了"有什么东西"的数据，但缺少"这是否正常"的判断框架和"趋势如何"的预测能力。在 SaaS 产品上线前，至少需要：SLO 燃尽仪表盘、每租户成本视图、存储增长趋势。

| 影响面 | 工作量估计 |
|--------|-----------|
| SLO 配置模型 + Prometheus recording rules | 低 |
| 成本指标 + 配置驱动 | 中 |
| 多租户 gauge 修复（1 行改动 + 租户遍历） | 低 |
| Grafana provisioning（JSON as code） | 低 |
| 容量预测告警 | 低 |
| **修复 `RegisterStorageClassGauge` 的硬编码 default** | **1 行 + 租户查询** |

---

## 2. 测试基础设施与质量门禁系统

### 当前状态

**测试数量多（237 个 Go 文件），但关键质量基础设施缺失。**

```makefile
# Makefile
test:
	go test ./...    # 只跑单元测试，零网络、零 Docker

test-integration:   # 需要手动运行，不在 CI gate 中
    # 启动 Postgres with pgvector → 运行 tagged integration tests → 清理

test-integration-qdrant:  # 需要手动运行
    # 启动 Qdrant → 运行 tagged tests → 清理

check: fmt vet build test complexity-lines   # CI gate = 单元测试 + lint
```

**好消息：**

| 维度 | 状态 |
|------|------|
| 单元测试数 | 充分（覆盖大部分逻辑） |
| 集成测试框架 | 存在（`internal/integration/`） |
| Storage contract tests | 存在（`internal/storage/contract_test.go`） |
| 内存泄漏/竞态测试 | 少量 |

**缺口（QA 基础设施缺失）：**

| 维度 | 缺口 | 严重性 |
|------|------|--------|
| **无基准测试（Benchmark）** | 没有任何 `func Benchmark*` 文件——无法检测性能退化 | 🔴 性能回归无感知 |
| **无 Fuzz 测试** | 零 `func Fuzz*` 文件——输入验证漏洞静默存在 | 🔴 安全/稳定性风险 |
| **集成测试不在 CI 守门员中** | `test-integration` 和 `test-integration-qdrant` 需要 Docker 且非 `make check` 的一部分 | 🟠 Postgres/pgvector/Qdrant 路径的回归只能在本地发现 |
| **无契约测试** | 无 OpenAPI spec vs handler 行为的一致性验证 | 🟠 API 变更后 SDK 与 handler 偏差 |
| **无负载测试工具** | 无任何形式的压力/负载测试脚本 | 🟠 上线前无吞吐量基线 |
| **无混沌测试** | 无存储后端故障/网络分区/DB 断连场景 | 🟠 故障恢复路径未验证 |
| **代码注释诊断** | `cmd/server/main.go` 多处注释 `"unverified in CI"` | 🟠 核心功能路径未经 CI 验证 |

**具体不安全的代码注释（直接引述）：**

```
// cmd/server/main.go
// logger.Info("pgvector vector index enabled (requires Postgres + vector ext; unverified in CI)")
// logger.Info("qdrant vector index enabled (external store; unverified in CI)")
// logger.Info("pgfts lexical index enabled (requires Postgres; unverified in CI)")
// logger.Info("postgres event transport enabled (requires Postgres; unverified in CI)")
```

——这四个功能涉及 AI 检索管道和数据事件传输的核心路径，均**未经 CI 门禁保护**。

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `Makefile:test` | `go test ./...` | 无 `-bench` 或 `-fuzz` 目标 |
| `Makefile:check` | `fmt vet build test complexity-lines` | 无 benchmark comparison、无 fuzz |
| `internal/integration/` | 3 个集成测试文件（postgres, qdrant, fullserver） | 无 CI 自动运行 |
| `internal/storage/contract_test.go` | Storage 后端合同测试 | 无基准版本（benchmark contract） |
| `internal/ai/search.go` | 检索管线 | 无性能基线测试 |
| `internal/api/rest/openapi.go` | OpenAPI 规范生成 | 无 spec vs handler 一致性验证 |
| `internal/api/s3compat/` | S3 handler | 无 AWS SDK 兼容性测试套件 |
| `.github/workflows/` | CI workflow（待读） | 假设仅运行 `make check` |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **Embedder 改实现后性能退化** | hash embedder 改为 HTTP embedder，P50 从 0.1ms 涨到 100ms | 单元测试通过 | Benchmark 对比：性能退化 > 20% → CI 阻断 |
| **ListObjects 被改为 OFFSET pagination** | 百万级对象的分页性能从 O(log n) 退化到 O(n) | 单元测试通过（功能正确） | Benchmark 检测线性增长 pattern → CI 告警 |
| **Postgres 迁移没有 SQLite 等价测试** | 新 migration 只在 Postgres 上测试，SQLite 路径断裂 | CI 中 SQLite 测试通过（无这个 migration） | 所有 migration 必须在两个数据库上都通过 |
| **OpenAPI 与实际 handler 字段不匹配** | 新增 handler 返回字段但未更新 OpenAPI spec | CI 通过 | OpenAPI diff check + SDK 再生验证 |
| **OSSP/COS 后端在 CI 中从不测试** | 阿里云 OSS 或腾讯云 COS 的行为只在生产才验证 | CI gate 只跑 local FS | contract test 必须 mock 云端行为（或使用 MinIO 统一测试） |
| **Fuzz 发现 key 注入漏洞** | key = `../../../etc/passwd` 通过 handler 验证 | 单元测试可能遗漏边界 | Fuzz test 持续运行，覆盖所有用户输入路径 |

### 架构蓝图

```
┌─ Benchmark 基线 ──────────────────────────────────────────────│
│ 新增文件:                                                       │
│   internal/ai/benchmark_test.go    // Search (+pgvector,+qdrant) │
│   internal/service/benchmark_test.go // Put/Get/List             │
│   internal/storage/benchmark_test.go  // local/S3/OSS/COS       │
│   internal/api/rest/benchmark_test.go  // HTTP handler 吞吐      │
│                                                                  │
│ 基准测试框架:                                                    │
│   - 使用 testing.B 标准库                                        │
│   - 每个 Benchmark 输出：ops/sec, bytes/sec, allocs/op           │
│   - 存储后端测试：1KB, 1MB, 10MB 对象                            │
│   - 检索测试：100/1K/10K chunk 数量级                            │
│   - 结果写入 benchstat 格式                                       │
│                                                                  │
│ CI 集成:                                                         │
│   make bench → 运行全部基准测试 → 输出结果                          │
│   make bench-compare → 对比上一个 commit 的基准数据                 │
│     退化 > 20% → CI 失败（允许通过 BENCH_THRESHOLD 覆盖）          │
│     新基准 → 写入 .benchmarks/baseline-{commit}.txt               │
│   GitHub Actions: 定时（每日）运行完整基准测试并保存结果              │
│                                                                  │
│ 存储: bin/benchmarks/                                            │
│   git 管理基线文件                                                │
│   CI 中 `go test -bench=. -benchmem -count=5 ./... > /tmp/bench`  │
│   `benchstat .benchmarks/baseline.txt /tmp/bench` → 退化检测       │
└────────────────────────────────────────────────────────────────┘

┌─ Fuzz 测试 ───────────────────────────────────────────────────│
│ 新增文件:                                                       │
│   internal/service/fuzz_test.go      // validateKey, validateMetadata │
│   internal/api/rest/fuzz_test.go     // keyFromPath, extractMetadata │
│   internal/api/s3compat/fuzz_test.go // bucket/key parsing           │
│   internal/storage/fuzz_test.go      // localMeta marshal/unmarshal   │
│                                                                  │
│ Fuzz 目标:                                                       │
│   FuzzValidateKey:    0x00 bytes, path separators, long paths    │
│   FuzzKeyFromPath:    URL encoding, double encoding, null bytes  │
│   FuzzMetadataKeys:   Duplicate keys, unicode, control chars     │
│   FuzzPolicyParse:    Malformed JSON policy, injection attempts  │
│                                                                  │
│ CI 集成:                                                         │
│   make fuzz → go test -fuzz=Fuzz -fuzztime=30s ./...              │
│   每次 CI 运行 30s fuzz（每晚在 GitHub Actions 上运行 5 分钟）     │
│   发现的 crash → 自动转为 unit test case                          │
└────────────────────────────────────────────────────────────────┘

┌─ 集成测试 CI 化 ───────────────────────────────────────────────│
│ 当前: test-integration/test-integration-qdrant 需手动运行         │
│ 目标: CI 中自动运行（但不会阻塞 PR merge）                        │
│                                                                  │
│ GitHub Actions 策略:                                              │
│   - 常规 PR: `make check`（单元测试 + lint + build）              │
│   - 定时（每日 UTC 00:00）:                                       │
│       * `make test-integration`（Postgres + pgvector）            │
│       * `make test-integration-qdrant`（Qdrant）                 │
│       * 结果通知到 Slack/邮件                                     │
│   - 手动触发: workflow_dispatch + 参数选择测试套件                 │
│                                                                  │
│ 基础设施即代码:                                                   │
│   .github/workflows/test-integration.yml                         │
│   .github/workflows/test-qdrant.yml                              │
│   .github/workflows/benchmark.yml                                │
│   .github/workflows/fuzz.yml                                     │
└────────────────────────────────────────────────────────────────┘

┌─ 契约测试框架 ────────────────────────────────────────────────│
│ API 规范 vs 实现的自动化验证:                                      │
│   1. 启动测试服务器（httptest）                                    │
│   2. 加载 OpenAPI spec（通过内部 openapi.go）                      │
│   3. 遍历 spec 中的每个 endpoint：                                │
│      - 验证 spec 声明的路径在 router 中确实存在                    │
│      - 验证 spec 声明的 response schema 与 handler 实际返回匹配    │
│      - 验证 spec 声明的 status code 与实际匹配                    │
│      - 验证 spec 声明的 request body schema 被 handler 正确解析    │
│   4. 差异超过阈值 → CI 失败                                       │
│                                                                  │
│ SDK 兼容性测试:                                                   │
│   启动正式服务器 → Go/Python/JS SDK 各运行一次功能测试              │
│   SDK 测试失败 → 标记对应 endpoint 为 "broken" → 阻断 PR          │
└────────────────────────────────────────────────────────────────┘

**为什么现在做：** 这是工程生产力的地基问题。没有性能基线的代码库每次重构/优化都是盲人摸象。没有 fuzz 测试的输入验证代码迟早出安全漏洞。集成测试不在 CI 中意味着"核心功能路径未经门禁保护"。在代码量到达 100K 行之前建立测试基础设施的工程债远低于之后。

| 影响面 | 工作量估计 |
|--------|-----------|
| Benchmark 基线（配置 4 个文件） | 中 |
| CI benchmark 对比 pipeline | 中 |
| Fuzz 测试（4 个输入点） | 低 |
| 集成测试 CI 化（GitHub Actions YAML） | 低 |
| 契约测试框架 | 中 |
| 修复 4 个 "unverified in CI" 注释的行为 | 零（框架到位后自然解决） |

---

## 3. 开发者体验与本地开发生态

### 当前状态

**现有开发流程完全手动，反馈周期长。**

```bash
# 当前开发循环：
go run ./cmd/server        # 编译+启动（~3-5s）
# 修改代码
Ctrl+C → go run ./cmd/server  # 再次等待
```

**当前开发体验问题清单：**

| 问题 | 当前状态 | 影响 |
|------|---------|------|
| **无热重载** | 每次修改代码需要手动停启 `go run` | 修改→验证循环：~10-15s/次 |
| **无 Dev Container** | 无 `.devcontainer/` 配置 | 新开发者需要手动安装 Go 1.25、配置环境 |
| **Docker Compose 缺失服务** | 只有 Postgres + MinIO | 无 Qdrant、无 pgvector、无 otel-collector |
| **无 Mock 服务** | 测试中依赖真实 HTTP 调用 | AI 测试需要真实 embedder endpoint |
| **无本地 Dashboard** | `docker-compose up` 不提供 Grafana/Prometheus | 开发中不可见指标 |
| **SDK 开发无 Mock Server** | SDK 测试需要真实服务器运行 | Python/JS SDK 开发依赖已部署的实例 |
| **无 Pre-commit Hook 自动安装** | `make check` 需手动运行 | CI 失败频率高 |
| **OpenAPI 无 playground** | `/docs` 有 Swagger UI 但无交互式 Playground | 开发/测试 API 需要 curl |
| **日志缺乏开发友好格式** | 仅 JSON 格式 | 终端阅读困难 |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `Dockerfile` | 多阶段构建，正确 | 无开发专用 Docker Compose profile |
| `docker-compose.yml` | Postgres + MinIO + app | 无 Qdrant / pgvector / Grafana / Prometheus |
| `go.mod` | 仅生产依赖 | 无 `tools.go` 管理开发工具的 Go 版本 |
| `cmd/server/main.go:952` | `run()` 全量启动 | 无 dev-mode 标志（轻量启动、mock AI、实时日志） |
| `Makefile` | build/test/check | 无 dev-watch、无 compose-dev、无 mock-server |
| `internal/api/rest/openapi.go` | OpenAPI spec 生成 | 无 mock server 生成器 |
| `.github/workflows/` | CI pipeline | 无 Pre-commit Hook 安装步骤 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **新贡献者第一天** | clone → go run → 缺少 `.env` | 启动失败或无 AI 功能 | `make dev-setup` → 复制 .env.example + 下载工具 |
| **编辑代码后快速验证** | 修改了一个 handler，想立即看到效果 | `Ctrl+C → go run`（~5s 编译） | `make dev-watch` → 文件变更自动重启（~1s） |
| **本地测试 AI 管道** | 需要验证 embedder + search 全链路 | 需要配置真实的 embedder endpoint | `make dev-mock-ai` → 启动内置 mock embedder + mock LLM |
| **SDK 开发需要 API Server** | Python SDK 开发者需要测试新 API | 需要启动完整服务器 + 配置鉴权 | `make dev-api-mock` → 启动 OpenAPI mock server |
| **跨团队 debug 网络问题** | 需要查看 OTel trace 定位慢请求 | 本地无 OTel collector | `make compose-dev` → 启动完整 observability stack |
| **多人开发冲突 env** | 两个人用同一台机器的不同端口 | 端口冲突 | `make dev PORT=9090` 覆盖端口 |

### 架构蓝图

```
┌─ 热重载开发服务器 ──────────────────────────────────────────│
│ 方式：使用 air（github.com/air-verse/air）                     │
│ 无需 go.mod 依赖——air 是独立二进制                                │
│                                                                  │
│ .air.toml                                                        │
│   [build]                                                        │
│     cmd = "go build -o ./tmp/aero-vault ./cmd/server"            │
│     bin = "./tmp/aero-vault"                                     │
│     include_ext = ["go", "env"]                                  │
│     exclude_dir = ["var", "bin", "tmp", ".git"]                  │
│                                                                  │
│   [log]                                                          │
│     main_only = true                                             │
│                                                                  │
| make dev → air                                                  |
|   文件变更 → < 1s 重新编译 + 重启                                 |
└────────────────────────────────────────────────────────────────┘

┌─ Dev Container ────────────────────────────────────────────────│
│ .devcontainer/devcontainer.json                                  │
│   {                                                              │
│       "name": "aero-vault",                                      │
│       "build": { "dockerfile": "Dockerfile" },                   │
│       "features": {                                              │
│           "ghcr.io/devcontainers/features/go:1": {               │
│               "version": "1.25"                                  │
│           }                                                      │
│       },                                                         │
│       "extensions": [                                            │
│           "golang.go",                                           │
│           "ms-azuretools.vscode-docker",                         │
│           "github.copilot"                                       │
│       ],                                                         │
│       "postCreateCommand": "make dev-setup",                     │
│       "forwardPorts": [8080, 9090, 3000]                         │
│   }                                                              │
│                                                                  │
│ make dev-setup:                                                  │
│   cp -n .env.example .env                                        │
│   go install github.com/air-verse/air@latest                     │
│   go install github.com/fzipp/gocyclo/cmd/gocyclo@latest         │
│   pre-commit install (optional)                                  │
└────────────────────────────────────────────────────────────────┘

┌─ 全面 Docker Compose ────────────────────────────────────────│
│ docker-compose.yml (扩展):                                       │
│   services:                                                      │
│     postgres:  (已有)                                            │
│     minio:     (已有)                                            │
│     qdrant:    (新增)                                            │
│       image: qdrant/qdrant                                       │
│       ports: ["6333:6333"]                                       │
│     otel-collector: (新增)                                       │
│       image: otel/opentelemetry-collector-contrib:latest         │
│       config: deploy/otel-collector-config.yaml                  │
│       ports: ["4318:4318", "8889:8889"]                          │
│     grafana: (新增)                                              │
│       image: grafana/grafana:latest                              │
│       volumes: ["./deploy/grafana/provisioning:/etc/grafana/provisioning"] │
│       ports: ["3000:3000"]                                       │
│     prometheus: (新增)                                           │
│       image: prom/prometheus                                     │
│       command: ["--config.file=/etc/prometheus/prometheus.yml"]  │
│       volumes: ["./deploy/prometheus:/etc/prometheus"]           │
│       ports: ["9090:9090"]                                       │
│                                                                  │
│ 使用:                                                             │
│   make dev-full → docker compose -f docker-compose.yml -f        │
│                   docker-compose.dev.yml up -d                   │
│   → 本地开发环境含 7 个服务                                      │
└────────────────────────────────────────────────────────────────┘

┌─ 开发模式服务器 ─────────────────────────────────────────────│
│ cmd/server/main.go 新增 --dev 标志:                              │
│   aero-vault --dev                                               │
│   效果:                                                          │
│     - 日志格式：text（非 JSON，终端可读）                        │
│     - 默认启用 *mock* embedder 和 LLM（零网络需求）              │
│     - 默认启用 AnonymousPublicRead（无鉴权开发）                 │
│     - 注册 /debug/pprof/ 端点                                    │
│     - 启动时打印路由表（所有 endpoint 一览）                     │
│     - 默认写入 ./var/dev/ 隔离数据目录（不影响生产目录）         │
│                                                                  │
│ Make 辅助:                                                       │
│   make dev         → aero-vault --dev                            │
│   make dev-watch   → air -- --dev                                │
└────────────────────────────────────────────────────────────────┘

┌─ API Mock Server ─────────────────────────────────────────────│
│ 基于 OpenAPI spec 生成 mock server：                              │
│   go run ./cmd/mock-server &                                     │
│   或: 使用 oapi-codegen 生成 server stubs                        │
│                                                                  │
│ 用途:                                                             │
│   - SDK 开发（Python/JS/Go 开发者不需要启动完整后端）            │
│   - 前端开发（WebUI 开发者可 mock API 行为）                     │
│   - 集成测试（确定性响应，零网络依赖）                           │
│                                                                  │
│ 内置 mock 模式:                                                   │
│   REST handler 注册 `/_mock/` route group:                       │
│     GET /_mock/reset         → 重置 mock state                   │
│     POST /_mock/expect       → 注册期望                          │
│     GET /_mock/unmatched     → 列出未匹配的请求                  │
└────────────────────────────────────────────────────────────────┘

**为什么现在做：** 开发者体验决定了团队的生产力和新贡献者的入职速度。当前开发循环（手动停启+编译）在一天内重复 100+ 次，每次 ~5s = 每天浪费 ~8 分钟在"等编译"上。更重要的是——没有 Dev Container + 完整 Docker Compose，新开发者需要手动搭建 7 个服务的开发环境，这是一天甚至更长的入职成本。

| 影响面 | 工作量估计 |
|--------|-----------|
| `.air.toml` + `make dev-watch` | 低（~10 行） |
| `.devcontainer/devcontainer.json` | 低（20 行 JSON） |
| Docker Compose 扩展（Qdrant/Grafana/Prometheus/OTel） | 低（30 行 YAML） |
| `--dev` 模式（cmd/server/main.go 修改） | 中（~50 行） |
| `make dev-setup` | 低（~5 行） |
| API Mock Server | 中 |

---

## 4. 存储层自愈与运维韧性

### 当前状态

**存储层有基本的错误处理（circuit breaker、quota check），但故障后的自动恢复路径几乎不存在。**

```go
// internal/storage/local.go:NewLocal
// 创建本地存储后端——接受 Root 路径，但从不检查磁盘空间
// 如果磁盘写满 → Put 返回 os error → handler 返回 500
// 没有：自动降级、空间告警、恢复后自动重试

// internal/storage/circuitbreaker.go
// 有 "open → half-open → closed" 状态机
// 但：没有持久化状态（重启后丢失）、没有联动健康检查端点、
//     没有自动恢复触发、没有度量和告警

// internal/reconcile/scrub.go
// Scrub 检查对象完整性（ETag 匹配）
// 但：不触发自动修复（只记录日志）、不检查 storage backend 自身健康
```

**当前存储层韧性全景：**

| 韧性场景 | 当前行为 | 严重性 |
|---------|---------|--------|
| **local 磁盘满** | Put 返回 `os error` → 500 | 🟠 无优雅降级，无空间告警 |
| **S3 后端不可达** | 网络错误传播到 handler | 🟠 circuit breaker 重试，但无自动 failover |
| **数据静默损坏** | Scrub 检测到 ETag 不匹配但仅记录 | 🟠 需要手动恢复 |
| **存储后端慢但未死** | 请求变慢但 circuit breaker 不触发（不到 failure threshold） | 🟠 P99 退化无感 |
| **SSE 密钥轮换后旧对象不可读** | Rewrap 在启动时运行，但中途失败的 envelope 无法读取 | 🟠 需手动 rewrap |
| **孤儿 blob 堆积** | reconcile GC 扫描但频率不可预测（interval_minutes） | 🟠 存储浪费 |
| **metadata vs storage 不一致** | Object 行存在但 storage blob 已被误删 | 🟠 返回 404？500？不确定 |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/storage/local.go:NewLocal` | 创建目录 | 无磁盘空间检查 |
| `internal/storage/local_write.go` | `os.WriteFile` | 无 ENOSPC 优雅处理 |
| `internal/storage/circuitbreaker.go` | CB 状态机（内存中） | 无持久化、无健康检查联动、无恢复自愈 |
| `internal/reconcile/scrub.go` | ETag 校验 | 不触发自动修复 |
| `internal/storage/factory.go:NewFromConfig` | 单后端选型 | 无"主+备"后端配置 |
| `internal/middleware/middleware.go:readyzHandler` | 健康检查 | 不感知 circuit breaker 状态 |
| `internal/service/file_crud.go:serveObjectContent` | 流式响应 | 读中断无恢复路径 |
| `internal/reconcile/job.go` | 孤儿清理 | 无 metadata-storage 一致性修复 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **磁盘空间 95% 满** | local 后端剩余 5% 空间 | 写操作随机失败（`disk full`） | 主动进入 **DegradedMode**：拒绝写入，允许读取，弹出告警 |
| **S3 后端故障恢复** | S3 宕机 30 分钟后恢复 | circuit breaker 在内存中状态在重启后丢失 | 重启后重新评估后端健康 + 重建 CB 状态 |
| **对象损坏自动修复** | Scrub 发现 object X 的 ETag 不匹配 | 仅记录日志 + 标记 `_aero_corrupt` | 如果对象有副本（replication 或多版本），自动从副本修复 |
| **健康检查感知降级** | storage backend 不可用（CB open） | `/readyz` 仍然返回 200（只检查 repo.Ping + Stat 一次） | `/readyz` 报告 `"storage": "degraded"` / `"storage": "unavailable"` |
| **孤儿 blob 累积风暴** | 大量写入后进程崩溃→orphan blobs 大量产生 | reconcile 每小时扫描一次，但可能跟不上 | reconcile 滞后量 > 阈值 → 加速扫描频率 |
| **SSE rewrap 失败恢复** | 某个 envelope 重包装时 KMS 超时 | 记录 warn，跳过该对象 | 持久化失败队列 → 下一轮重试（指数退避） |
| **metadata 存在但 blob 被误删** | 管理员误删了 storage 目录下的 blob | GET 返回 500 | 检测 blob 缺失 → 标记对象为 `CORRUPT` → 触发自动恢复 |

### 架构蓝图

```
┌─ 磁盘空间监控与自动降级 ────────────────────────────────────│
│ 新增: internal/storage/monitor.go                               │
│                                                                  │
│ type DiskMonitor struct {                                        │
│     root        string    // storage root                        │
│     warnPercent int       // 85%                                 │
│     critPercent int       // 95%                                 │
│     mu          sync.Mutex                                       │
│     degraded    bool      // 是否处于降级模式                    │
│ }                                                                 │
│                                                                  │
│ func (m *DiskMonitor) Check() (DiskStatus, error)                │
│   // 使用 syscall.Statfs 获取磁盘信息（跨平台）                   │
│   // 返回: {Total, Used, Avail, UsedPercent, Degraded}           │
│                                                                  │
│ func (m *DiskMonitor) Run(ctx, interval)                         │
│   // 定期（60s）检查磁盘状态                                      │
│   // used% > critPercent → m.degraded = true                     │
│   // used% < warnPercent 持续 5 分钟 → m.degraded = false        │
│                                                                  │
│ 集成:                                                             │
│   FileService.Put 检查:                                          │
│     if monitor.IsDegraded() → return ErrDegraded                 │
│   /readyz 检查:                                                  │
│     if monitor.IsDegraded() → storage: "degraded" (200 + warn)  │
│   metrics:                                                       │
│     storage.disk_usage_percent{path}                              │
│     storage.degraded_mode{reason}  (0/1 gauge)                   │
└────────────────────────────────────────────────────────────────┘

┌─ Circuit Breaker 持久化 + 健康联动 ───────────────────────────│
│ 当前: CB 状态在内存中（重启丢失）                                 │
│ 改进: 可选持久化到 metadata key（对象: `_aero_cb_state`）         │
│   或: 启动时重新评估后端健康（主动探测 3 次）                      │
│                                                                  │
│ CB 状态与健康检查联动:                                            │
│   /readyz → 检查所有 active storage backend 的 CB 状态            │
│     if any backend is open → resp.storage = "unavailable"        │
│     if any backend is half-open → resp.storage = "degraded"      │
│     else → "ok"                                                  │
│                                                                  │
│ 后端健康指标:                                                     │
│   storage.backend_up{backend}           // 0/1 gauge             │
│   storage.backend_latency_ms{backend}   // P50/P95/P99 hist      │
│   storage.backend_errors_total{backend, error_type}              │
└────────────────────────────────────────────────────────────────┘

┌─ 自动数据修复框架 ───────────────────────────────────────────│
│ 当 Scrub 检测到数据不一致时（未来流程）:                           │
│   1. 标记对象为 `_aero_inconsistent` + 记录不一致类型              │
│      (size, etag, missing blob, encrypted mismatch)             │
│   2. 检查是否有其他副本（versioning 或 replication）：             │
│      - 有副本 → 从副本恢复（CopyObject from valid version）       │
│      - 无副本 → 保留标记，记录告警                                │
│   3. 恢复成功 → 解除不一致标记 + 记录恢复日志 + 指标              │
│   4. 恢复失败 → 升级告警到 P1                                    │
│                                                                  │
│ 新增指标:                                                         │
│   storage.auto_repair_total{status}  // "success" | "failed"    │
│   storage.inconsistent_objects{tenant} // gauge                  │
└────────────────────────────────────────────────────────────────┘

**为什么现在做：** 存储层韧性是运维疲劳度的决定因素。没有磁盘监控的生产存储系统=迟早被动应对 P1 事件。没有自动修复框架的 Scrub=检测到问题但需要人工介入的"半诊断"。在代码层面，这些新增组件与现有 circuit breaker、Scrub、reconcile 框架紧密配合，增量成本低。

| 影响面 | 工作量估计 |
|--------|-----------|
| `DiskMonitor` 包（磁盘检查 + 降级） | 低 |
| `/readyz` 联动 CB 状态 | 低 |
| 自动修复框架集成到 Scrub | 中 |
| 存储后端健康指标 | 低 |
| metrics 告警规则补充 | 低 |

---

## 5. 生产安全纵深防御：传输安全、Secret 集成、输入加固

### 当前状态

**当前安全能力主要集中在"鉴权"层面（JWT、API Key、SigV4），传输安全、Secret 管理和输入安全方面有显著缺口。**

```go
// cmd/server/main.go
srv := &http.Server{
    Addr:              cfg.App.Addr,            // :8080 — 纯 HTTP
    Handler:           handler,
    ReadHeaderTimeout: 15 * time.Second,
    WriteTimeout:      ...,
    IdleTimeout:       ...,
    // 没有 TLSConfig — 没有 HTTPS 支持
    // 没有 MaxHeaderBytes — 没有请求头大小限制
    // 没有 ConnContext — 没有连接级上下文
}
```

**当前安全状态全景：**

| 安全维度 | 当前实现 | 状态 |
|---------|---------|------|
| 鉴权（Auth） | JWT / API Key / SigV4 / AnonymousPublicRead | ✅ 完整 |
| 鉴权（Bucket Policy） | Policy 存储+解析+评估（v8 方向 #1） | ⚠️ 待评估执行 |
| 传输安全（TLS） | 无 HTTPS 支持 | ❌ |
| 传输安全（mTLS） | 无 | ❌ |
| Secret 管理 | 环境变量硬编码 Key | ❌ |
| 请求大小限制 | 无 `MaxHeaderBytes`、无 body size middleware | ❌ |
| 安全响应头 | 无 CSP / HSTS / X-Content-Type-Options | ❌ |
| IP 级限流 | 仅有 per-tenant token-bucket | ❌ |
| 安全事件日志 | 鉴权失败不单独记录 | ❌ |
| 输入验证 | key/path 验证在 FileService 层 | ✅ |
| 审计（admin） | 管理操作已记录 | ✅ |
| 审计（对象访问） | 未实现（v10 方向 #2） | ❌ |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `cmd/server/main.go:803-808` | `http.Server` 无 `TLSConfig` | 不支持 HTTPS |
| `cmd/server/main.go` | 启动逻辑 | 无 `--tls-cert` / `--tls-key` 标志 |
| `internal/config/config.go` | 配置结构 | 无 `TLS`、`SecurityHeaders`、`SecretProvider` 字段 |
| `internal/middleware/middleware.go` | 中间件链 | 无 `SecurityHeaders`、无 `MaxBodySize`、无 `RequestLogger（安全事件）` |
| `internal/auth/auth_middleware.go` | Auth 中间件 | 失败时不记录安全事件 |
| `internal/api/rest/handler.go` | Handler | `r.Body` 无尺寸限制包装 |
| `internal/storage/secret.go` | `SecretProvider` 接口 | 仅 Keyfile/KMS，无 Vault/AWS Secrets Manager |
| `internal/config/config_storage.go` | SSE 密钥配置 | 明文 env var 传输密钥 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **明文 HTTP 传输** | 客户端通过公网传输 API Key + 对象内容 | 明文传输（无 HTTPS） | 默认启用 TLS，可选强制 HTTP→HTTPS 重定向 |
| **Secret 泄露到日志** | 启动时打印 `SSEKey` 到日志 | 日志显示密钥值 | 配置值在日志中自动 masking |
| **Key 存储在 git 中** | 开发者误提交 .env 到 git | API Key 泄露 | Secret Provider 集成（Vault/AWS SM）消除静态配置 |
| **超长请求头攻击** | 恶意请求发送 100MB header | 无限制，内存耗尽 | `MaxHeaderBytes=1MB` + 超长请求体 413 |
| **CSP 缺失 XSS** | WebUI 被注入恶意脚本 | 无 CSP 保护 | 所有 /ui 响应附加 `Content-Security-Policy` |
| **鉴权失败无追踪** | 攻击者尝试 1000 次密码暴力破解 | 日志中 1000 条 401，但无安全事件 | 鉴权失败速率告警 + 自动 IP 封禁（可选） |
| **首字节时间攻击** | 用户名字典攻击（响应时间差异） | 存在时间侧信道 | 鉴权路径固定时间比较（已用 sha256 hash） |
| **HSTS 缺失** | 用户通过书签访问 HTTP 版本 | 无自动升级到 HTTPS | `Strict-Transport-Security` header |

### 架构蓝图

```
┌─ TLS/HTTPS 支持 ──────────────────────────────────────────────│
│ cmd/server/main.go:                                              │
│   if cfg.TLS.Enabled {                                           │
│       srv.TLSConfig = &tls.Config{                                │
│           MinVersion: tls.VersionTLS12,                           │
│           CurvePreferences: []tls.CurveID{                        │
│               tls.CurveP256, tls.X25519,                          │
│           },                                                      │
│           PreferServerCipherSuites: true,                         │
│           CipherSuites: []uint16{                                 │
│               tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,       │
│               tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,         │
│           },                                                      │
│       }                                                           │
│       srv.ListenAndServeTLS(cfg.TLS.CertFile, cfg.TLS.KeyFile)   │
│   } else {                                                        │
│       srv.ListenAndServe()                                        │
│   }                                                               │
│                                                                  │
│ 配置:                                                             │
│   TLS_ENABLED=false                                               │
│   TLS_CERT_FILE=""                                                │
│   TLS_KEY_FILE=""                                                 │
│   TLS_MIN_VERSION="1.2"                                           │
│                                                                  │
│ 可选: 自动 Let's Encrypt（使用 autocert）                          │
│   TLS_AUTO=true                                                   │
│   TLS_DOMAIN=aero.example.com                                     │
└────────────────────────────────────────────────────────────────┘

┌─ Secret Provider 集成 ────────────────────────────────────────│
│ 当前: `SecretProvider` 接口存在（internal/storage/secret.go）     │
│      实现: LocalKeyProvider（key file）/ RemoteKeyProvider（HTTP）│
│ 缺口: 企业级 Secret Store 集成                                    │
│                                                                  │
│ 新增实现:                                                         │
│   type VaultProvider struct {                                     │
│       addr   string  // HashiCorp Vault address                   │
│       token  string  // Vault token                               │
│       path   string  // secret/ data/aero-vault/keys              │
│   }                                                                │
│   Resolve(ctx, kid) → []byte  // 从 Vault KV 读取当前 key         │
│                                                                  │
│   type AWSSecretsManagerProvider struct {                         │
│       region    string                                            │
│       secretID  string  // arn:aws:secretsmanager:...:secret:...  │
│   }                                                                │
│   Resolve(ctx, kid) → []byte                                      │
│                                                                  │
│ 配置:                                                             │
│   SECRET_PROVIDER=""     // "vault" | "aws-sm" | "keyfile" | ""  │
│   SECRET_VAULT_ADDR=""   // https://vault.example.com:8200         │
│   SECRET_VAULT_TOKEN=""  // 或 VAULT_TOKEN env                    │
│   SECRET_VAULT_PATH=""   // secret/data/aero-vault                │
│                                                                  │
│ 日志安全:                                                         │
│   所有 config 值的 logging 自动 masking：                          │
│     如果 key 包含 "SECRET"、"KEY"、"TOKEN"、"PASSWORD" →           │
│       只输出前 4 字符 + "****"                                     │
└────────────────────────────────────────────────────────────────┘

┌─ 安全中间件层 ────────────────────────────────────────────────│
│ 新增中间件（在 middleware/middleware.go 或新文件）:                │
│                                                                  │
│ 1. SecurityHeadersMiddleware                                      │
│    func SecurityHeaders(next http.Handler) http.Handler {         │
│        return http.HandlerFunc(func(w http.ResponseWriter, r) {   │
│            w.Header().Set("X-Content-Type-Options", "nosniff")    │
│            w.Header().Set("X-Frame-Options", "DENY")             │
│            w.Header().Set("X-XSS-Protection", "0")               │
│            w.Header().Set("Referrer-Policy", "strict-origin")    │
│            if isUI(r) {                                          │
│                w.Header().Set("Content-Security-Policy",          │
│                    "default-src 'self'; script-src 'self'")      │
│            }                                                      │
│            if cfg.TLS.Enabled {                                   │
│                w.Header().Set("Strict-Transport-Security",        │
│                    "max-age=31536000; includeSubDomains")         │
│            }                                                      │
│            next.ServeHTTP(w, r)                                   │
│        })                                                         │
│    }                                                              │
│                                                                  │
│ 2. MaxBodySizeMiddleware                                          │
│    bodySize := cfg.App.MaxBodySize  // 0 = unlimited              │
│    r.Body = http.MaxBytesReader(w, r.Body, bodySize)              │
│    // 超限 → 413 Request Entity Too Large                        │
│                                                                  │
│ 3. SecurityEventLogger (替代 AccessLog 的 Auth-failure 增强)      │
│    Auth 失败时单独记录结构化事件：                                  │
│      {                                                            │
│        "time": "...",                                             │
│        "type": "auth_failure",                                     │
│        "reason": "invalid_signature|expired_token|no_key",        │
│        "remote_ip": "10.0.0.1",                                   │
│        "method": "PUT",                                           │
│        "path": "/s3/bucket/key",                                  │
│        "user_agent": "aws-sdk-go/..."                             │
│      }                                                            │
│    安全事件发送到独立的事件通道（`security_events_total` metric）    │
└────────────────────────────────────────────────────────────────┘

┌─ 补全中间件链顺序 ───────────────────────────────────────────│
│ 当前链顺序:                                                      │
│   RequestID → CORS → Auth → Tenant → RateLimit → OTel →         │
│   Recoverer → AccessLog                                          │
│                                                                  │
│ 新增中间件插入位置（I4 约束：顺序不可变 → 扩展而非插入）：        │
│   RequestID → SecurityHeaders → MaxBodySize → CORS →             │
│   Auth → Tenant → RateLimit → OTel → Recoverer → SecurityLogger  │
│   → AccessLog                                                    │
│                                                                  │
│ 注意: 不能破坏现有中间件链顺序（I4 规则禁止重排），只能两端补充。   │
│ SecurityHeaders 放在最前（尽早设置响应头）。                        │
│ SecurityLogger 放在最后（有完整请求上下文的最终拦截点）。            │
└────────────────────────────────────────────────────────────────┘

**为什么现在做：** 安全是一个"木桶效应"领域——最薄弱的一环决定了整体安全性。当前 AeroVault 在鉴权层面做得很好，但传输安全、Secret 管理和输入加固三块基本空白。对于任何面向公网部署的实例，缺少 HTTPS 和 Security Headers 是不可接受的。对于合规部署（金融、医疗），缺少 Secret Provider 集成意味着密钥以明文环境变量存储，这违反了几乎所有审计标准。

| 影响面 | 工作量估计 |
|--------|-----------|
| TLS 支持（`cmd/server/main.go` 修改） | 低（~30 行） |
| Secret Provider 实现（Vault/ AWS SM） | 中 |
| Security Headers 中间件 | 极低（~20 行） |
| MaxBodySize 中间件 | 极低（~5 行） |
| 安全事件日志 + metric | 低 |
| 配置值日志 masking | 低 |
| Auth-failure 增强日志 | 低 |

---

## 总结：优先级矩阵（跨 10 期文档的最终缺口）

| 方向 | 业务/工程价值 | 工程成本 | 依赖关系 | 推荐排序 |
|------|-------------|---------|---------|---------|
| **可观测性成熟度平台** | ★★★★★（运维决策、SaaS 运营的先决条件） | ★★（指标已就绪，缺框架+仪表盘） | 现有 telemetry/metrics | **1** |
| **测试基础设施与质量门禁** | ★★★★★（长期工程效率的决定因素） | ★★★ | CI pipeline（已存在） | **2** |
| **开发者体验与生态** | ★★★★（新贡献者入职 + 开发速度） | ★★（增量添加, 零架构变更） | 无 | **3** |
| **存储层自愈与韧性** | ★★★★（生产事故响应时间从"小时"到"秒"） | ★★★ | 现有 reconcile/scrub/CB | **4** |
| **生产安全纵深防御** | ★★★★（公网部署准入条件） | ★★（TLS/Headers 低成本, Secret 集成中成本） | 无 | **5** |

**说明：** 这 5 个方向共同覆盖了 AeroVault 从"功能丰富"到"生产就绪"的最后鸿沟。前 10 期 expansion 文档已经覆盖了全部功能特性增长方向（AI、S3 兼容、多租户、合规、复 制、加密、审计等）。本期聚焦的是——**当一个产品拥有所有功能后，还需要什么才能真正上线运行、被运维、被安全审计？**

---

*分析基于 commit 当前 HEAD | 代码行数 ~50K (Go) + SDK/UI/Infra | 审阅范围：全部 internal/ 子包（23 个）、全部 10 期 expansion 文档、ROADMAP、CHANGELOG、HARNESS、Makefile、Dockerfile、deploy/、go.mod、sdk/*
