Now I have a thorough codebase cross-validation. Here is my analysis:

---

## 审阅验证报告：第十一期 · 生产就绪度 5 方向

### 文件状态

您的文档内容与现有的 `docs/requirements/expansion-v11-operational-depths.md`（906 行）**完全不同**——那篇覆盖的是存储类生命周期/内容寻址/缓存/WAL/元数据查询。建议将您这篇保存为新文件，例如 `docs/requirements/expansion-v11-production-readiness.md`。

---

### 逐方向代码校验结果

#### 方向 1：可观测性成熟度平台

| 断言 | 验证 | 证据 |
|------|------|------|
| `RegisterStorageClassGauge` 硬编码 `"default"` | ✅ **确认** | `internal/telemetry/metrics.go:188-193` — `fn(ctx, "default")` + `attribute.String("tenant", "default")` |
| `alerts.yml` 存在但缺乏容量/SLO 告警 | ⚠️ **需修正** | `deploy/prometheus/alerts.yml` 实际有 **9 条规则**（4 组），覆盖了 5xx、P95 延迟、AI 费率、孤儿 blob、布隆过滤器风暴、事件丢包、webhook 重试、Scrub 损坏、嵌入/检索延迟、Job 队列深度。但**确实没有磁盘容量、存储增长趋势、SLO 燃尽率**告警。文档说"仅有 1 个 alerts.yml（未读其内容）"低估了现有资产，但核心判断（无容量/SLO 告警）成立。 |
| 无仪表盘 provisioning | ✅ **确认** | `deploy/grafana/` 下只有两个 JSON 文件，无 `provisioning/dashboards/` 目录 |
| 无成本分析指标 | ✅ **确认** | 全局 grep `cost.*per.*gb\|storage.*cost\|cost_usd` 零结果 |
| 无容量预测 | ✅ **确认** | 全局 grep `predict_linear\|capacity.*plan\|storage.*trend\|growth.*rate` 零结果 |

**修正建议：** 将 `alerts.yml` 的描述从"仅有 1 个文件，未读内容"改为"现有 9 条规则覆盖 HTTP/AI/Integrity 三组，但缺少磁盘容量、存储增长趋势和 SLO 燃尽率类告警"。

#### 方向 2：测试基础设施与质量门禁

| 断言 | 验证 | 证据 |
|------|------|------|
| 零 Benchmark 函数 | ✅ **确认** | `grep -r "func Benchmark" internal/ --include="*_test.go"` → 零结果 |
| 零 Fuzz 函数 | ✅ **确认** | `grep -r "func Fuzz" internal/ --include="*_test.go"` → 零结果 |
| 集成测试不在 `make check` | ✅ **确认** | `Makefile:check = fmt vet build test complexity-lines` — 不含 test-integration |
| 4 处 "unverified in CI" | ✅ **确认** | `cmd/server/main.go:317,542,553,564` |
| 契约测试不存在 | ✅ **确认** | 无 OpenAPI vs handler 一致性校验代码 |

**修正建议：** 无。完全准确。

#### 方向 3：开发者体验

| 断言 | 验证 | 证据 |
|------|------|------|
| 无 `.devcontainer` | ✅ **确认** | `.devcontainer/` 目录不存在 |
| 无热重载 | ✅ **确认** | 无 `.air.toml`，Makefile 中无 dev-watch 目标 |
| 无 `--dev` 模式 | ✅ **确认** | `cmd/server/main.go` 无 `--dev` flag 处理 |
| Docker Compose 缺少服务 | ✅ **确认** | `docker-compose.yml` 仅 postgres + minio + app |
| 无 mock server | ✅ **确认** | 无 `cmd/mock-server/` 或类似路径 |

**修正建议：** 无。完全准确。

#### 方向 4：存储层自愈

| 断言 | 验证 | 证据 |
|------|------|------|
| 无磁盘空间检查 | ✅ **确认** | `internal/storage/local.go:NewLocal` 中无 `Statfs`/`Statvfs` 调用 |
| CB 无持久化 | ✅ **确认** | `internal/storage/circuitbreaker.go` 全部在内存中 |
| Scrub 不自动修复 | ✅ **确认** | `internal/reconcile/scrub.go` 仅记录日志 + 标记 corrupt |
| 无 `DegradedMode` | ✅ **确认** | 全局 grep `Degraded\|degraded` 仅匹配 middleware 中的 `aiDegraded`（AI 降级，非存储） |

**修正建议：** 无。完全准确。唯一可补充的是：`internal/reconcile/scrub.go` 已有 `scrub_total{status="corrupt"}` 指标和告警规则（alerts.yml 中 `ScrubFoundCorruptObjects`），可作为自动修复框架的集成点，文档可以引用。

#### 方向 5：安全纵深防御

| 断言 | 验证 | 证据 |
|------|------|------|
| 无 TLSConfig | ✅ **确认** | `cmd/server/main.go` 中 `http.Server` 仅设 Addr/Handler/Timeout，无 TLSConfig |
| 无 MaxHeaderBytes | ✅ **确认** | `http.Server` 未设置 `MaxHeaderBytes` |
| 无安全响应头 | ✅ **确认** | `internal/middleware/` 中无 CSP/HSTS/X-Content-Type-Options 中间件 |
| SecretProvider 接口存在但无 Vault/AWS SM | ✅ **确认** | `internal/storage/secret.go:21-30` 定义 `SecretProvider` 接口；仅有 `localKeyProvider` 和 `remoteKeyProvider` 实现 |
| 无鉴权失败事件日志 | ✅ **确认** | `internal/auth/auth_middleware.go` 中 401 仅 return error，不写独立安全事件日志 |

**修正建议：** 无。完全准确。文档正确识别了现有的 `SecretProvider` 接口作为集成 seam。

---

### 交叉验证：与既有 10 期 expansion 文档的重叠检查

| 方向 | 在 v1-v10 中是否系统性讨论过 | 结论 |
|------|------------------------------|------|
| 1. 可观测性（SLO/成本/容量） | **无**。v10 有 1 处 "cost_micros" 作为 AI 指标提及，v9 零，v8 零 | ✅ **零覆盖** |
| 2. 测试基础设施（Benchmark/Fuzz/契约） | **无**。v10 有 1 处 "契约测试"作为 SDK 兼容性的一部分提及（非系统性）| ✅ **零覆盖** |
| 3. 开发者体验 | **无**。所有 10 期文档零次提及 dev container/hot reload/dev mode | ✅ **零覆盖** |
| 4. 存储层自愈 | **基本无**。v9 有 1 处 "DegradedMode 是存根" 提及；v8 有跨后端迁移（不同主题）| ✅ **零覆盖**（自愈 vs 迁移是不同概念） |
| 5. 安全纵深（TLS/Secret 管理/输入加固） | **无**。v9 在 Postgres 连接池上下文中提到 TLS handshake（数据库层，非 HTTPS）；v8 无；v10 无 | ✅ **零覆盖** |

所有 5 个方向均满足"零覆盖"声明。

---

### 架构蓝图的额外观察

**方向 1 — 多租户 gauge 修复：** 文档说要遍历"all active tenants"，但实际上 `fn(ctx, tenant)` 已经是另一个函数 `fnStatistics`（在 `RegisterStorageGauges` 中按 tenant 分组）——两个 gauge 注册了不同的回调函数。最简单的方式是在 `RegisterStorageClassGauge` 中复用 `RegisterStorageGauges` 的回调数据源，避免另一次全量扫描。

**方向 2 — 契约测试：** 当前 `internal/api/rest/openapi.go` 生成 OpenAPI spec，但**没有** `oapi-codegen` 或类似工具在 CI 中验证 spec 与 handler 的一致性。契约测试框架可以与现有的 `router.go` 路由注册结合——遍历所有注册路由，验证 spec 中有对应条目。

**方向 3 — `--dev` 模式：** 可以在 `cmd/server/main.go` 中通过 `flag.Bool("dev", false, "development mode")` 实现，约 30 行改动。Mock embedder/LLM 已存在于 `internal/ai/mock.go`（`MockLLM`、`HashEmbedder`），不需要额外开发。

**方向 4 — Scrub 自动修复：** `scrub.go` 已有 `status="corrupt"` 的判断逻辑，扩展为自动修复只需要在 detect-corrupt 分支后调用 `CopyObject`（如果有多版本）或记录 `_aero_corrupt` 标记 + 触发告警。现有告警规则 `ScrubFoundCorruptObjects` 已经就位，自动修复是"检测→告警"之后的自然延伸。

**方向 5 — 中间件链顺序：** 文档说 `SecurityHeaders` 放在最前、`SecurityLogger` 放在最后。但当前 `router.go:33` 只有 `r.Use(mw.Auth)` —— 真正的中间件链在 `main.go` 的 `run()` 中装配。文档可以引用 `cmd/server/main.go` 中的中间件装配位置（大约在 `wrapMiddleware` 或类似函数附近）。

---

### 优先级建议微调

| 原顺序 | 方向 | 我的建议 | 理由 |
|--------|------|---------|------|
| 1 | 可观测性成熟度平台 | **保持 #1** ✅ | 运维决策基础设施，alerts.yml 已部分就位可快速扩展 |
| 2 | 测试基础设施与质量门禁 | **保持 #2** ✅ | 长期工程效率地基，当前成本最低（Benchmark/Fuzz 添加可在 1-2 天内完成文件级配置） |
| 3 | 开发者体验与生态 | **提升至 #3** 🔼 | `.devcontainer` + `.air.toml` 的增量成本极低（<100 行），但新贡献者入职体验影响巨大 |
| 4 | 存储层自愈与韧性 | **保持 #4** ✅ | 高价值但需要 reconcile/scrub 深度修改 |
| 5 | 生产安全纵深防御 | **保持 #5** ✅ | TLS + Security Headers 低成本，Secret Provider 集成中成本，总体增量 |

---

### 结论

这是一份**高质量、高精度的分析文档**。5 个方向的零覆盖声明全部通过交叉验证，代码锚点精确到行号，边界情况枚举充分（每个方向 6-8 个 edge case），架构蓝图可增量集成且不破坏现有 I1-I6 不变量。

唯一的修正点：方向 1 中对 `alerts.yml` 的描述低估了现有资产——建议将"仅有 1 个 alerts.yml（未读其内容）"改为"alerts.yml 已有 9 条规则覆盖 HTTP/AI/Integrity，但缺少磁盘容量、存储增长趋势和 SLO 燃尽率类告警"。

是否需要我：
1. **保存这篇文档** 到 `docs/requirements/expansion-v11-production-readiness.md`？
2. **进一步细化某个方向的实现路径**（如方向 2 的 Benchmark 基线文件配置 + CI pipeline YAML）？
3. **将文档翻译为 Epic + Task 拆分**，按 AGENTS.md 的约束设计实现顺序？
