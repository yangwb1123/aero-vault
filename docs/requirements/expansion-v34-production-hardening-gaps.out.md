我已经完整阅读了这份 v34 分析文档。5 个方向都已仔细审视，结合当前代码库的实际情况，我有以下几点架构层面的评估与补充观察：

---

## 整体评价

这份分析的质量很高——定位准、去重验证严谨、对既有代码锚点的把握精确。但当我将其与 `internal/` 下当前的代码实际状态对照时，有几个值得注意的**落地风险点**和**优先级修正建议**：

---

## 1. Adaptive Backpressure（P0，#1 优先）— 最大价值，但需要简化

**完全认同这是 P0。** 当前静态限流（`internal/middleware/ratelimit.go` 第 45-60 行的固定 token-bucket）在生产压力下确实会成为级联失效的放大器。

**落地建议：不建议从零实现完整的控制循环。**

当前代码库已经具备 OTel 指标（`internal/telemetry/metrics.go` 已有 15 个 instruments），建议采用**轻量级 AIMD 适配器**而非独立的控制器 goroutine：

- 将 `RateLimiter` 的 `rate` 和 `burst` 改为 `atomic.Float64` / `atomic.Int64`（当前是构造时传入，永不变化）
- 在 `middleware.go` 的 `ConcurrencyLimiter` 中增加一个 `Adjust(ctx)` 方法，**由被限流的 handler 返回时被动触发调整**，而非独立的控制循环
- 避免新增 `internal/backpressure/` 包；AIMD 逻辑可以内联在 `middleware/ratelimit.go` 中（大约 +150 行），满足"单文件 ≤ 500 行"约束

**被分析文档忽略的一个重要问题：** 当前 `RateLimiter` 是**每租户独立的**（`perTenantRatelimiters` map），每个租户有独立的 `*rate.Limiter`。自适应策略需要知道"哪个租户在抖动"——这要求将租户上下文传入限流器，而当前中间件链的 `Auth→Tenant→RateLimit` 顺序刚好支持。

---

## 2. S3 Select（P1，#2）— 方向正确，但建议拆为两阶段

分析文档的架构设计很好，但有一个**架构层级问题**：

当前的 S3 handler 路径 `s3compat/handler.go` 中，所有对象读取最终走到 `s3compat.getObject` → `service.Get`（第 400-420 行）。Select 操作**不能复用这个路径**，因为：
- `service.Get` 返回完整的 `io.ReadCloser`
- Select 需要在读取过程中做 SQL 过滤 → 投影 → 格式转换

所以 `service.SelectObject` 必须是 `service` 包中的一个独立方法，接收 SQL 表达式和序列化配置，返回流式结果。

**建议将 Parquet 支持延后到 Phase 2（分析文档也这么建议，这是对的）。** CSV/JSON 的 SELECT WHERE 投影 是 80% 的用例。

**一个重要的兼容性边界：** AWS S3 Select 的 XML 请求格式非常复杂（`InputSerialization` 有 12+ 子元素）。如果要做到协议兼容，`internal/api/s3compat/xml.go` 的 XML 编解码会相当庞大。建议使用 `encoding/xml` 的 streaming decoder（`xml.Decoder`）逐元素解析，避免一次性解析大 XML。

---

## 3. RAG Evaluation（P2，#5）— 价值高但前置依赖多

分析文档的架构设计合理（Golden Dataset + 指标计算 + CI 集成），但是落地需要考虑以下现实约束：

**当前代码库的 AI 管线状态：**
- `internal/ai/` 中 `MockLLM` 和 `HashEmbedder` 是用于测试的——**生产环境中的 AI 管线质量取决于外部 LLM/Embedding 提供商**
- `internal/ai/search.go` 中的检索结果**没有 relevance label**——需要人工标注 `eval_datasets`
- `internal/ai/chat.go` 的 `Answer` 方法没有配置独立的"评估 LLM"——评估 LLM 需要使用与生产 LLM 不同的模型

**建议将评估框架的 CLI 子命令（`aero-vault eval run/dataset/check`）作为第一阶段，API 和 Web UI 延后。** CLI 的第一个版本可以：
1. 读 YAML 数据集文件（不需要 DB 表）
2. 调用当前 Search + Chat 管线
3. 输出 JSON 报告到 stdout
4. CI 中通过 `jq` 提取指标做门限检查

这样**不需要迁移文件**（0 数据库变更），数据集作为代码文件版本化管理。

---

## 4. Performance Benchmarking（P1，#3）— 当前空位最多的领域

分析文档说"零 `*_benchmark_test.go` 文件"——这是准确的。这个方向是**投入产出比最高的 P1**。

**补充意见：** 不需要新建 `benchmarks/` 目录。Go 基准测试的最佳实践是将 `*_benchmark_test.go` 放在对应的包内，与单元测试并列。这样：
- `internal/service/` 下放 `put_bench_test.go`、`get_bench_test.go`
- `internal/ai/` 下放 `search_bench_test.go`、`embed_bench_test.go`
- `internal/repository/` 下放 `crud_bench_test.go`

**CI 的 `benchstat` 比较需要考虑 Go 版本差异。** 建议在 CI benchmark 工作流中固定 Go 版本（`go-version: '1.25'`），否则编译器优化变更会导致假阳性。

---

## 5. Dynamic Tenant Provisioning（P2，#4）— 从架构上需要考虑的边界问题

分析文档的架构很完整，但有一个**被忽略的重要约束**：当前 `POST /v1/admin/tenants` 是 admin scope 下的操作（需要 `scope=admin` 在 JWT 中）。`POST /v1/signup` 如果创建租户就需要**绕过 scope 检查**——这在当前中间件链中是可行的（设置 `AllowAnonymous` 路由），但需要注意**注册 API 的限流应该比普通 API 严格**（防滥用），且与全局 RateLimiter 独立。

**另一个缺失的边界：** 自助注册后的邮箱验证（Phase 2）。目前的方案没有邮箱验证，这会导致：
- 任何人都可以创建租户
- 没有找回 API Key 的机制
- 无法识别重复注册

建议 Phase 1 先完成 `POST /v1/signup`（无需邮箱验证），但**在文档中明确标注**这是设计决策而非遗漏。

---

## 我的修正后优先级排序

```
原始顺序: #1(Backpressure) → #2(S3 Select) → #3(Benchmark) → #4(Tenant Provisioning) → #5(RAG Eval)
修正顺序: #1(Backpressure) → #3(Benchmark) → #2(S3 Select) → #5(RAG Eval) → #4(Tenant Provisioning)
```

**理由：** 基准测试（#3）的工程量较小（纯文件新增，不修改业务逻辑），却能为后续所有修改提供质量安全网。在 S3 Select 和租户开通落地之前建立性能基线，可以避免"先开发后回归"的情况。这个顺序在分析文档的"建议实施序列"中将被低估了——性能基准应该是**基础设施层**（与自适应背压同级），而非"待 AI 管线稳定后"的后续工作。

---

**总结：** 这是一份高质量的分析——5 个方向都是真实的缺口，去重验证严格，架构设计务实。以上是结合当前代码库具体实现细节的落地可行性补充。如果决定开始实施，我可以从 **#1（自适应背压）** 或 **#3（性能基准）** 入手给出具体代码变更。
