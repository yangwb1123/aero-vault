# 🏗️ AeroVault 深度评估 v7 — API 与开发者体验、运维成熟度、测试基础架构、代码健康、供应链安全

> **日期:** 2026-06-30  
> **方法:** 全局代码扫描（237 文件 / ~45K 行），第七轮  
> **视角:** 开发者体验 → 运维可操作性 → 测试质量 → 代码健康 → 供应链 → 边界情况/性能

---

## 0. 本轮焦点：从"有什么功能"到"用起来怎么样"

前六轮覆盖了特征缺口（v1）、韧性（v2）、生态系统与经济性（v3）、内部架构与AI管线质量（v4）、存储实现与标准合规（v5）、平台竞争力与AI管线2.0（v6）。  
本轮转向**体验与维护性维度**——API调用时的开发者感觉、生产中的运维体验、测试基础设施完善度、代码库长期可维护性和供应链安全。这些维度决定了一个项目能否从"好的代码"进化为"好的产品"。

---

## 1. API 设计质量与开发者体验

### 1.1 REST API 一致性审计

| 维度 | 评价 | 具体发现 |
|----------|---------|-------------|
| **URL 模式** | ⚠️ 不一致 | `GET /v1/files/{key}` 用通配符 (`*`)，`GET /v1/lineage/objects/{id}` 用路径参数。没有统一的 URL 参数模式 |
| **HTTP 方法** | ✅ 正确 | GET/HEAD/POST/PUT/DELETE 使用正确 |
| **状态码** | ✅ 良好 | `200`/`201`/`202`/`400`/`403`/`404`/`409`/`412`/`416`/`429`/`500`/`507` |
| **响应信封** | ⚠️ 不一致 | 成功: 直接 JSON 对象。错误: `{"error":{"code":"...","message":"..."}}`。分页: `{"objects":[],"next_marker":"...","has_more":bool}`。没有统一的 `{"data":...,"meta":{...}}` 信封 |
| **错误消息** | ⚠️ 参差不齐 | 有些包含内部细节（`svc.Put: ensure bucket: ...`），有些清晰 |
| **分页** | ⚠️ 基础 | 基于标记的分页，但响应中缺少总大小/总页数。搜索未分页 |
| **幂等性** | ✅ 良好 | Stripe 风格 `Idempotency-Key` + 可选的 body-hash 指纹 |
| **速率限制** | ⚠️ 缺少标准头 | `429` 返回 `Retry-After`，但缺少 `RateLimit-Limit`/`Remaining`/`Reset` |
| **HATEOAS/可发现性** | ❌ 缺失 | API 响应不包含相关链接（如 `GET /v1/files/{key}` 不提供版本/标签/编辑链接） |
| **`Accept` 头** | ⚠️ 不一致 | REST 仅返回 JSON；S3 仅返回 XML；无内容协商 |
| **CORS** | ✅ 全局中间件 | 全局 CORS，缺少桶级覆盖 |

### 1.2 错误消息质量分析

通过扫描 `writeError` 和 `writeS3Error` 调用，以下是真实错误消息的抽样：

```go
// 优秀的错误消息:
// "object not found"                          → NotFound
// "quota exceeded: bytes 5000/1000"           → QuotaExceeded
// "range not satisfiable"                     → InvalidRange

// 需要改进的错误消息:
// "svc.Put: ensure bucket: create bucket..."  → 泄露内部实现细节
// "get object: ..."                            → 包装不一致，有时用户看到的是内部错误
// "no handler registered for job type ..."     → 内部细节泄露给作业 API 消费者
```

**发现：** `internal/api/rest/handler.go` 中的 `writeError` 函数返回 `err.Error()` 给客户端，没有过滤内部上下文。在 `FileService` 层使用的 `fmt.Errorf("ensure bucket: %w", err)` 等包装错误中，可能泄露实现细节。

### 1.3 SDK 质量与一致性

| SDK | 方法覆盖率 | 错误处理 | 自动重试 | 类型安全 | 测试 |
|-----|:---------:|:---------:|:-------:|:--------:|:----:|
| **Go** | ✅ ~30 方法 | ✅ 结构化 `Error` | ❌ 无 | ✅ 强类型 | ✅ 有 |
| **Python** | ⚠️ 部分 | ❌ 未审计 | ❌ 无 | ❌ 未审计 | ❌ 无 |
| **JavaScript** | ⚠️ 部分 | ❌ 未审计 | ❌ 无 | ❌ 未审计 | ❌ 无 |

```go
// sdk/go/aerovault/client.go — Go SDK
// 模式: 统一错误类型 + 未包装的原始 HTTP 响应
// 缺失: 自动重试 + 退避 + 超时传播

// 具体改进点:
// - client.go: 方法 Upload() 使用 io.ReadAll → 大文件内存溢出风险
// - client.go: 无 `WithRetry(maxAttempts, backoff)` 选项
// - client.go: ChatStream 支持较好（SSE 扫描器）但缺少错误恢复
```

### 1.4 OpenAPI 规范质量

```go
// internal/api/rest/openapi.json — 嵌入在 openapi.go 中
// 评价: ✅ 包含 ~50+ 端点 + 类型 + 安全模式
// 缺失:
// - x-status-code 默认值（200 与 201 的混用）
// - 错误响应的 Examples（返回 `{"error":{...}}` 的一致性）
// - 桶管理端点的文档（logging, notifications, cors DELETE — 见 v5）
// - SDK 生成的 x-sdk-type 提示
```

---

## 2. 运维成熟度与 SRE 准备度

### 2.1 健康检查深度

```go
// cmd/server/main.go — readyzHandler
// 当前状态:
//   - /healthz: 仅返回 {"ok":true} — 极其基础，无依赖检查
//   - /readyz: 检查 repo.Ping + storage.Stat (非法键，查找 ErrNotFound)
//
// 发现:
//   ⚠️ 无 AI 管线健康检查（如果嵌入器/LLM 宕机，search/chat 静默失败）
//   ⚠️ /readyz 不检查事件总线/作业池状态
//   ⚠️ 无 /livez 存活探针（仅健康状态）
//   ⚠️ 无 /startupz 启动探针（在索引构建期间使用）
```

**建议的健康检查矩阵：**

| 端点 | 检查项 | 当前状态 | 建议 |
|----------|-------|-------------|----------|
| `/healthz` | 进程存活 | ✅ 总是 OK | 保留 |
| `/livez` | Goroutine 健康 / 无死锁 | ❌ 缺失 | 添加 goroutine 概要 + 上次健康时间戳 |
| `/readyz` | DB + 存储 + 最少依赖 | ⚠️ 部分 | 添加嵌入器 ping、CB 状态、事件总线状态 |
| `/startupz` | 启动完成 / 迁移完成 / 初始索引完成 | ❌ 缺失 | 添加启动就绪标志 |
| `/debug/pprof` | Go pprof 端点 | ❌ 缺失 | 启用标准 pprof 路由 |

### 2.2 优雅关闭

```go
// cmd/server/main.go — runServer 中的优雅关闭路径
func runServer(ctx context.Context, handler http.Handler, cfg *config.Config, logger ...) error {
    srv := &http.Server{...}
    go func() {
        <-ctx.Done()
        shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
        defer cancel()
        srv.Shutdown(shutdownCtx)  // ✅ 服务器关闭
        bus.Close()                // ✅ 事件总线关闭
        shutdownOtel()             // ✅ OTel 关闭
    }()
    return srv.ListenAndServe()
}

// 已覆盖: ✅ HTTP 服务器 Shutdown, ✅ EventBus Close, ✅ OTel Shutdown
// 未覆盖: ❌ 作业池排空（运行中的作业被终止）
//        ❌ 存储后端关闭（S3/OSS/COS 客户端无优雅关闭）
//        ❌ 索引器停止（正在进行的嵌入被中断）
//        ❌ 机群租约释放（持有租约的副本退出 → 其他副本需等待到期）
```

### 2.3 配置管理

| 维度 | 评估 | 发现 |
|----------|--------|--------|
| **验证** | ⚠️ 基础 | `config.Load()` 解析 env 但**不验证**跨字段约束（如 `AI_VECTOR_BACKEND=qdrant` 需要 `AI_VECTOR_URL`） |
| **文档** | ✅ 完整 | `docs/configuration.md` 覆盖了 80+ 变量 |
| **默认值** | ✅ 合理 | 零成本、零依赖的默认值（local + sqlite + 无 AI） |
| **敏感信息** | ⚠️ 混合 | Secret 从 env 读取（无 vault/集成）——helm chart 通过 `secret.yaml` 支持 Secret |
| **重载** | ❌ 缺失 | 所有配置在启动时读取。无运行时重载（无 `SIGHUP` 处理） |
| **类型安全** | ⚠️ 混合 | `getEnvInt` / `getEnvBool` 包装但**无 schema 验证** |

```go
// internal/config/config.go — 配置加载
// 发现: 配置结构体无验证方法。
// Load() 返回 *Config 但有 nil 值风险（例如，未设置 AI.Endpoint 可能导致 nil 指针）
// 改进: Config.Validate() error 方法检查跨字段约束
```

### 2.4 日志记录质量

| 维度 | 评估 | 发现 |
|----------|--------|--------|
| **结构化 JSON** | ✅ 是 | `slog.NewJSONHandler` |
| **请求 ID** | ✅ 是 | `RequestID` 中间件 + 上下文传播 |
| **追踪 ID** | ❌ 缺失 | 无 OpenTelemetry 追踪 ID 传播到日志 |
| **登录级别** | ✅ 是 | `APP_LOG_LEVEL` 可配置：debug/info/warn/error |
| **登录速率限制** | ❌ 缺失 | 高错误率时，大量错误日志可能淹没 |
| **敏感数据清理** | ⚠️ 部分 | API key 从未记录，但请求体可能包含 PII |
| **登录路由/API 端点** | ✅ 是 | 访问日志中间件 + `METHOD`/`PATH`/`STATUS`/`DURATION` |
| **采样** | ❌ 缺失 | 高流量时，每请求日志的开销显著 |

---

## 3. 测试基础设施质量

### 3.1 测试覆盖统计

| 指标 | 值 |
|--------|-------|
| **测试文件** | 104（共 237 源，44%）|
| **覆盖率** | ~55%（行）|
| **单元测试** | 大多数测试文件 |
| **集成测试** | 3（`*_integration_test.go`）均**不**在 `make check` 中运行 |
| **模糊测试** | 0 |
| **基准测试** | 0 |
| **属性测试** | 0 |
| **race 检测** | `make check` 中**无**（CI 运行 `go test ./...` 无 `-race`）|

### 3.2 测试模式分析

```go
// ✅ 优秀模式:
// - ai/ 使用 MockLLM / HashEmbedder（零网络，确定性）
// - storage/contract_test.go 参数化合约测试
// - 模拟 HTTP 服务器的 httptest.NewServer（在 ai/, s3compat/ 中）

// ⚠️ 需要改进的模式:
// - internal/repository/sql_objects_test.go — 在 TestMain 中为每个包创建新的 SQLite DB
//   跨测试文件无法并行化（通过文件系统锁序列化）
// - internal/api/rest/handlers_test.go — 一些测试在 TestMain 中共享 DB 状态
// - internal/service/file_test.go — 缺少大文件分块测试

// ❌ 缺失的模式:
// - 端到端多协议测试（PUT via REST → GET via S3 → 预期相同对象）
// - 模糊测试（模糊 AI 嵌入器输入、分块器配置、PII 检测器输入）
// - 基准测试（启动时间、索引性能、搜索延迟）
// - 并发/竞态测试（`go test -race` 在 CI 中未启用）
```

### 3.3 各包测试强度

| 包 | 测试文件 | 质量 | 覆盖率 | 备注 |
|-------|:--------:|:-------:|:--------:|:------|
| `internal/ai` | 16 | ✅ 优秀 | >70% | 有模拟 + 集成 + 漂移测试 |
| `internal/api/rest` | 12 | ✅ 良好 | >60% | 需要更多流式/S3 交叉验证测试 |
| `internal/api/s3compat` | 3 | ⚠️ 基础 | ~40% | 缺少许多端点测试 |
| `internal/api/webdav` | 1 | ⚠️ 基础 | ~30% | PROPFIND + spillBuffer 已覆盖 |
| `internal/auth` | 1 | ⚠️ 基础 | ~40% | JWT + SigV4 + 存储路径未充分测试 |
| `internal/cli` | 1 | ❌ 很差 | ~10% | 1440 行测试，声明了 7 个已知 BUG |
| `internal/config` | 0 | ❌ 没有 | 0% | **最关键的包完全没有测试** |
| `internal/events` | 1 | ⚠️ 基础 | ~30% | 缺少 webhook + 重试 + 传输测试 |
| `internal/jobs` | 2 | ✅ 良好 | >70% | 深度上限 + 池逻辑 |
| `internal/mcp` | 0 | ❌ 没有 | 0% | 无 MCP 工具测试 |
| `internal/middleware` | 1 | ⚠️ 基础 | ~50% | 限速器已测试，但缺少 CORS/otel/recoverer |
| `internal/reconcile` | 2 | ⚠️ 基础 | ~40% | 孤儿 blob + 保留已测试 |
| `internal/repository` | 8 | ✅ 良好 | >60% | SQL 查询 + 迁移 + chunk |
| `internal/storage` | 8 | ✅ 良好 | >70% | 合约 + 本地 + 加密 + CB |
| `internal/service` | 9 | ✅ 良好 | >65% | CRUD + 功能 + 多部分 |

### 3.4 测试性能与并行化

```go
// 所有测试使用 `go test ./...` 运行——无按包隔离
// SQLite 测试使用 `file:` DSN，默认启用共享缓存
// 大型测试文件（cli_test.go: 1440 行，storage_test.go: 1120 行）拖慢 CI
```

| 问题 | 影响 | 严重性 |
|-------|--------|:----------:|
| 无 `-race` 在 CI 中 | 竞态条件未被检测 | 🔴 高 |
| `config` 包无测试 | 配置错误导致生产故障 | 🔴 高 |
| `mcp` 包无测试 | MCP 行为无验证 | 🟡 中 |
| `cli_test.go` 1440 行 + 7 个 BUG | 高维护负担，降低信号/噪声比 | 🟡 中 |
| `TestMain` 包级 SQLite | 阻止并行包级测试 | 🟡 中 |
| 无模糊测试 | AI/存储边界情况未被探索 | 🟢 低 |

### 3.5 CI 质量

```yaml
# .github/workflows/ci.yml — 当前 CI 管道
# 步骤: gofmt → build → vet → 圈复杂度检查 → test → coverage report
#
# ✅ 良好: gocyclo 门禁, gofmt 门禁, 文件行数门禁 (500)
# ⚠️ 缺失:
#   - 无 `go test -race`（无竞态检测）
#   - 无 `go vet ./...` 阴影/原子性检查
#   - 无静态分析（staticcheck, govulncheck）
#   - 无集成测试（`make test-integration` 需要 Docker）
#   - 无缓存测试（重复运行不缓存）
#   - 无构建标签测试（无 `integration` 标签执行）
```

---

## 4. 代码健康与可维护性趋势

### 4.1 包耦合分析

| 包 | 导入 | 被导入 | 扇出风险 |
|-------|---------|----------|:----------:|
| `internal/config` | `os`, `strconv`, godotenv | **所有其他包** | 🔴 核心配置——变更影响一切 |
| `internal/repository` | `sql`, time, uuid | `service`, `ai`, `events`, `reconcile`, `antivirus`, `replication`, `jobs`, `api/rest` | 🔴 高耦合——数据层访问 |
| `internal/service` | `repository`, `storage` | `api/rest`, `api/s3compat`, `api/webdav`, `mcp` | 🔴 主中枢 |
| `internal/ai` | `repository`, `storage` | `api/rest`, `mcp` | 🟡 中 |
| `internal/storage` | `os`, aws/aliyun/tencent SDK | `service`, `replication`, `factory` | 🟡 中 |

**发现：** 循环依赖不存在 ✅。但 `repository` 包因其 19 个导入文件和多种职责面临**通用陷阱**。

### 4.2 接口健康状况

| 接口 | 方法 | 实现 | 使用一致性 |
|-----------|:----------:|:--------:|:--------------:|
| `storage.Storage` | 12 | 4 (local/s3/oss/cos) | ✅ 所有后端实现完整接口 |
| `repository.Repository` | ~30 | 2 (SQLite/Postgres) | ⚠️ 大型——需要为测试 stub 化 |
| `ai.Embedder` | 2 | 3 (HTTP/Hash/Caching) | ✅ 简洁，专注 |
| `ai.LLM` | 2 | 2 (HTTP/Mock) | ✅ 简洁，专注 |
| `ai.Reranker` | 2 | 2 (HTTP/Heuristic) | ✅ 简洁，专注 |
| `ai.Extractor` | 1 | 2 (Default/Remote) | ✅ 简洁，专注 |
| `ai.VectorIndex` | 1 | 3 (repo/pgvector/Qdrant) | ✅ 简洁，专注 |
| `ai.LexicalIndex` | 1 | 2 (pgFTS) | ✅ 简洁，专注 |
| `ai.ChunkSink` | 3 | 4 (repo/BM25/pgvector/Qdrant) | ⚠️ 组合增长 |
| `auth.PersistentStore` | ~6 | 1 (repo) | ✅ 单一实现目前够用 |
| `storage.SecretProvider` | 3 | 3 (env/keyfile/url) | ✅ 优秀抽象 |

**发现：** 接口普遍设计良好——单一职责、方法少、明确命名。`repository.Repository` 以 ~30 个方法突出最重。

### 4.3 命名与组织一致性

| 模式 | 评估 | 发现 |
|---------|--------|--------|
| **包命名** | ✅ 一致 | `service/`, `storage/`, `repository/`, `ai/`, `auth/` |
| **文件命名** | ⚠️ 有些不一致 | `file_crud.go`（CRUD 操作）与 `file_features.go`（特征管理）与 `file_multipart.go`（多部分）——还不错 |
| **变量命名** | ✅ 良好 | 描述性名称。简写避免（`svc` 用于 `service`，`repo` 用于 `repository`） |
| **注释** | ⚠️ 高效但缺失 | 公共类型 ✅ / 公共函数 ✅ 但许多内部函数缺少文档 |
| **错误变量** | ⚠️ 不一致 | `service.ErrInvalidArgs`（✅ 可导出），`repository.ErrNotFound`（✅ 可导出），但一些包使用未导出的 `errors.New` |
| **测试函数** | ✅ 良好 | `TestXxx` 模式，一些 `TestXxx_Yyy` |

### 4.4 死代码与未使用的抽象

| 位置 | 代码 | 状态 |
|----------|------|--------|
| `internal/ai/pii.go` | `MapPII` 函数 | ⚠️ 定义但无处调用 |
| `internal/ai/llm.go` | `SSEScanner` 结构体 | ✅ 由聊天流在内部使用 |
| `internal/api/webdav/spill.go` | `spillBuffer` | ✅ 大对象上传/下载使用 |
| `internal/snapshot/snapshot.go` | 整个包 | ⚠️ 仅从 `cli/cli.go` 调用——帮助/文档路径可能被忽略 |

### 4.5 已知技术债务（按文件）

| 文件 | 行数 | 债务 |
|----------|:------:|----------|
| `internal/cli/cli_test.go` | 1440 | **超过 500 行限制**。声明了 7 个 BUG（"flaky: ..."、"BUG: ..."）上次扫描至今未修复 |
| `internal/api/rest/handler.go` | 565 | **超过 500 行限制**。1 个处理函数，多个职责（PUT、POST 表单、列出等） |
| `internal/storage/storage_test.go` | 1120 | **超过 500 行限制**。合约测试套件合理但过长 |
| `internal/service/file_crud.go` | 352 | ✅ 在限制内，但功能密集 |
| `internal/api/rest/admin.go` | 390+ | ✅ 在限制内，但处理越来越多的端点 |

---

## 5. 供应链安全与依赖管理

### 5.1 依赖审计

| 类别 | 计数 | 风险 |
|----------|:------:|:----:|
| **Go 标准库依赖** | ~12（net/http、encoding/json、crypto 等）| 🟢 零风险 |
| **外部 Go 模块** | ~25 | 🟡 中等（详细见下文）|
| **云 SDK** | 3（aws-sdk-go-v2、aliyun-oss-go-sdk、cos-go-sdk-v5）| 🟡 各自有不同发布节奏 |
| **HTTP 路由** | 1（chi/v5）| 🟢 广泛使用，成熟 |
| **配置** | 1（godotenv）| 🟢 小，稳定 |
| **UUID** | 1（google/uuid）| 🟢 标准 |
| **前端（Web UI）** | 0（纯 HTML+JS）| 🟢 零 JS 依赖 |

```go
// go.mod — 关键依赖:
// github.com/go-chi/chi/v5         v5.1.0     // HTTP 路由器
// github.com/google/uuid             v1.6.0     // UUID
// github.com/joho/godotenv           v1.5.1     // .env 加载
// github.com/aws/aws-sdk-go-v2       v1.30.x    // S3 SDK
// github.com/aliyun/aliyun-oss-go-sdk v3.0.x    // OSS SDK
// github.com/tencentyun/cos-go-sdk-v5 v0.7.x    // COS SDK
```

### 5.2 供应风险

| 风险 | 评估 | 缓解 |
|--------|--------|----------|
| **依赖版本固定** | ⚠️ 不完全 | `go.sum` 锁定，但无 `vendor/` 目录 |
| **CVE 扫描** | ❌ 缺失 | CI 中无 `govulncheck` 或 `trivy` |
| **许可证合规** | ❌ 缺失 | 无明确许可证清单 |
| **SBOM** | ❌ 缺失 | 无自动导出的 SBOM（`syft` + `spdx`） |
| **镜像签名** | ❌ 缺失 | Docker 镜像未签名 |
| **SLSA 合规** | ❌ 缺失 | 构建链无 SLSA 证明 |

### 5.3 license 兼容性

所有外部依赖使用宽松许可证（MIT、Apache-2.0、BSD-3-Clause）。  
- `chi` → MIT  
- `aws-sdk-go-v2` → Apache-2.0  
- `aliyun-oss-go-sdk` → MIT  
- `cos-go-sdk-v5` → Apache-2.0  
- `godotenv` → MIT  
- `uuid` → BSD-3-Clause  

**发现：** 所有依赖与专有部署兼容 ✅。但无自动许可证验证。

---

## 6. 边界情况与性能增强点（本轮新视角）

### 6.1 错误恢复：对象锁和封存之间的竞态条件

```go
// internal/service/file_crud.go
// 删除路径检查 locked_until，但 legal hold（当前未实现）可能需要不同的检查点
// ⚠️ 当前竞态条件: 检查锁 → 删除
// 如果锁设置在检查和删除之间，删除会悄无声息地通过
// 修复: 加租户+桶+键上的咨询锁，或使用 `UPDATE ... WHERE locked_until IS NULL` 原子条件
```

### 6.2 并发边界：`EventBus.Subscribe` 和 `Close` 之间的竞态条件

```go
// internal/events/bus.go
// Subscribe: 获取写锁 + append 到 subs
// Close: 获取写锁 + 关闭所有 subs
// 竞态: Subscribe 和 Close 并发 → Subscribe 可以写入即将关闭的切片
// 级别: 低可能性但可能存在
// 修复: Close 使用原子标志，Subscribe 在设置后拒绝
```

### 6.3 配置默认值陷阱：无 AI 但 AI 功能暴露

```go
// internal/mcp/server.go
// if s.chat != nil { 暴露 chat 工具 }
// 问题: 当 AI 未配置 (AI_INDEX_ENABLED=false) 时，MCP 正确隐藏 AI 工具 ✅
// 但: POST /v1/search 返回 500（内部错误）而非清晰的 404/400
// 修复: AI 禁用时所有 AI 端点返回 `503 AI Disabled`
```

### 6.4 性能：`BM25.Search` 在每个查询上分配

```go
// internal/ai/bm25.go
// Search 每次调用分配切片 + 映射
// 修复: 对象池 sync.Pool 用于临时搜索缓冲区
// 影响: 高 QPS 搜索的中等 GC 压力
```

### 6.5 性能：高基数标签的标签过滤

```go
// internal/repository/sql_objects.go:ListObjectsByTag
// 当前: WHERE EXISTS (SELECT 1 FROM tags WHERE ...)
// 改进: JOIN + 复合索引 (tenant_id, bucket, tag_key, tag_value)
// 影响: 数十万带标签对象的搜索延迟从 O(n) 降至 O(log n)
```

### 6.6 边界：带有对象锁的桶删除

```go
// internal/service/file_features.go:DeleteBucket
// 当前: 调用 repo.DeleteBucket —— 直接删除元数据
// ⚠️ 不检查桶中是否有锁定对象
// 影响: 可在桶有锁定对象时硬删除桶
// 修复: DeleteBucket 前先 Select count(*) WHERE locked_until > now()
```

### 6.7 边界：零字节对象和分块器

```go
// internal/ai/indexer.go
// 零字节对象触发 IndexObject
// 分块器处理空字符串 → 零块 → skip
// 当前: Indexer 对零字节对象返回 nil（正确跳过）
// 但: 跳过时 `telemetry.IncIndexerSkip(ctx, "empty")` 计数器递增 ✅
```

### 6.8 边界：非 UTF-8 元数据值

```go
// internal/service/file_crud.go:validateMetadata
// 当前: 检查键名，不检查值编码
// ⚠️ 非 UTF-8 字节可能进入 Go 字符串
// 修复: 添加值编码验证
```

---

## 7. 🚀 5 个高价值扩展方向

---

### 🥇 方向 1：API 开发者体验（DX）升级 — 统一信封、错误规范、分页标准、链接

**为什么需要它：** 一个功能完善但 API 不一致的产品会**降低每位开发者信心**。API 一致性是开发者衡量代码质量的**最先接触点**。`/v1/files/{key}` 的通配符匹配与 `/v1/lineage/objects/{id}` 的路径参数传递不一致，错误可能泄露内部细节，缺少统一信封，使自动化消费变得困难。修复 API DX 是从"可用的 API"到"开发者喜爱的 API"的成本最低、影响最大的方向。

**架构蓝图：**

```
当前:
├── 成功 → 裸 JSON（不同端点不同形状）
├── 错误 → {"error":{"code":"...","message":"..."}}
└── 分页 → {"objects":[],"next_marker":"...","has_more":bool}

改进: API DX Standard (跨 REST + 通知后端)

┌─────────────────────────────────────────────────────────────┐
│ Unified Envelope (统一信封) — 可选，协商:                          │
│   Accept: application/vnd.aero-vault.v1+json                   │
│   → {"data": ..., "meta": {"request_id": "...", "ts": "..."}}   │
│   Accept: application/json (向后兼容，裸体)                       │
├─────────────────────────────────────────────────────────────┤
│ Error Specification (错误规范):                                     │
│   {"error": {                                                   │
│     "code": "NotFound",   // 机器可读 — 客户可编程处理           │
│     "message": "object 'foo.txt' not found in bucket 'default'",│
│     "request_id": "abc123",                                     │
│     "details": { "tenant": "acme", "bucket": "default" },       │
│     "docs": "/docs/errors#NotFound"                             │
│   }}                                                            │
├─────────────────────────────────────────────────────────────┤
│ Pagination Standard (分页标准):                                       │
│   请求: ?cursor=<base64>&limit=50                               │
│   响应: {"data":[], "next_cursor":"...", "total": 1234}         │
│   HTTP 头: X-Total-Count: 1234                                  │
├─────────────────────────────────────────────────────────────┤
│ Rate Limit Headers (速率限制头):                                       │
│   RateLimit-Limit: 100                                          │
│   RateLimit-Remaining: 42                                       │
│   RateLimit-Reset: 1688160000                                   │
│   Retry-After: 3 (429 时)                                         │
├─────────────────────────────────────────────────────────────┤
│ SDK Generation Pipeline (SDK 生成管线):                               │
│   工具: oapi-codegen / openapi-generator                        │
│   输入: 更新的 openapi.json (API DX 升级)                        │
│   输出: Go / Python / JS 客户端 (100% 覆盖)                    │
│   CI: OpenAPI 变更 → 自动生成 SDK PR                            │
└─────────────────────────────────────────────────────────────┘
```

**边缘情况与性能考量：**
- 向后兼容：统一信封在 `Accept` 头后协商——默认保持裸体
- 分页光标：无 `LIMIT/OFFSET` 偏移（大表性能差）——使用基于光标的分页（WHERE id > $cursor ORDER BY id）
- 错误详情：敏感信息（堆栈）仅在 debug 模式下管理端点暴露

| 复杂度 | 用户影响 | 代码变动 | 差异化 |
|----------|-------------|-------------|------------|
| 低中 | 极高（信心）| 最小（适配器层） | ★★★★★ |

---

### 🥇 方向 2：运维工具箱 — 深度健康检查、优雅关闭贯穿、运行时配置重载

**为什么需要它：** AeroVault 的生产运维能力**未经过充分考验**。健康检查太基础（跳过 AI 状态），优雅关闭未覆盖作业池和索引器，配置不能运行时重载需要重启。对于 Kubernetes 部署（有 Liveness/Readiness/Startup 探针），这直接转化为滚动更新期间的部署失败风险。

**架构蓝图：**

```
当前: /healthz (always 200) + /readyz (DB + storage ping) + 部分优雅关闭
改进: SRE Toolbox (cmd/server + internal/sre)

┌──────────────────────────────────────────────────────────────┐
│ Deep Health Checks (深度健康检查):                                  │
│   端点矩阵:                                                     │
│   ├── /healthz   → 总是 200 (存活)                             │
│   ├── /livez     → Goroutine 健康 + 上次健康时间戳 + 无死锁    │
│   ├── /readyz    → DB ping + storage stat + embedder ping      │
│   │                 + LLM ping + CB 状态 + 事件总线状态         │
│   ├── /startupz  → 迁移完成 + 初始索引完成 + BM25 就绪          │
│   └── /debug/pprof → Go pprof 端点                              │
│                                                                   │
│   响应格式: {"ok":true/false, "checks": [{"name":"db",          │
│              "ok":true, "latency_ms":2, "error":""}]}           │
├──────────────────────────────────────────────────────────────┤
│ Graceful Shutdown (优雅关闭贯穿):                                    │
│   ├── HTTP 服务器: 现有 Shutdown(ctx) (15s 超时)               │
│   ├── 作业池: Pool.Shutdown(ctx) 排空正在运行的作业              │
│   │   ├── 发送终止信号 → 当前作业完成 → 无新作业               │
│   │   └── 硬超时后强制取消 (`context.WithTimeout`)                │
│   ├── 索引器: Indexer.Stop() 完成当前嵌入后退出                  │
│   ├── 存储客户端: S3/OSS/COS 关闭 HTTP 连接池                   │
│   └── 机群租约: releaseLease() 以便其他副本可立即接管           │
├──────────────────────────────────────────────────────────────┤
│ Runtime Configuration Reload (运行时配置重载):                       │
│   ├── SIGHUP 处理程序 → 重新加载配置 · 应用增量                  │
│   ├── 哪些可重载: 日志级别、速率限制、预算、                    │
│   │                AI 降级模式、CORS 来源                       │
│   ├── 哪些不可重载: 存储后端、数据库 DSN、AI 提供商端点        │
│   └── /-/reload (admin-only HTTP POST) 替代方案                  │
└──────────────────────────────────────────────────────────────┘
```

**边缘情况与性能考量：**
- 关闭超时：总关闭超时应小于 Kubernetes `terminationGracePeriodSeconds`（默认 30s）
- 配置重载：不可重载的变更应返回 `400 Bad Request` + 消息 "需要重启"
- 健康检查缓存：深度 ping 应缓存结果（5s TTL）以避免 Dogpile

| 复杂度 | 用户影响 | 代码变动 | 差异化 |
|----------|-------------|-------------|------------|
| 中 | 极高（生产可靠性） | 中（新包 internal/sre/）| ★★★★★ |

---

### 🥇 方向 3：测试基础设施大修 — CI 竞态检测、模糊测试、并行化、包覆盖

**为什么需要它：** CI 缺少 `-race`（竞态检测）、全部集成测试、模糊测试，以及关键包（`config`、`mcp`、`s3compat` 部分）的测试。`cli_test.go` 在 1440 行中声明 7 个 BUG。配置加载完全没有测试——配置错误是运维中最常见的故障模式。`AGENTS.md` 要求 50% 覆盖率，但 CI 仅检查而不强制执行。

**架构蓝图：**

```
当前: gofmt → build → vet → gocyclo → test (无 race) → coverage report
改进: CI 2.0 (分布式 + 并行化 + 模糊化)

┌──────────────────────────────────────────────────────────────┐
│ CI Pipeline Changes (CI 管道变更):                                    │
│   ┌── Unit tests (单元测试):                                          │
│   │   ├── go test -race -count=3 -shuffle=on ./...                │
│   │   ├── 超时: 10 分钟                                             │
│   │   └── 若有竞争或波动则失败                                        │
│   ├── Static Analysis (静态分析):                                      │
│   │   ├── govulncheck (漏洞)                                       │
│   │   ├── staticcheck (lint)                                       │
│   │   └── gosec (安全扫描)                                        │
│   ├── Fuzz Tests (模糊测试):                                           │
│   │   ├── go test -fuzz=FuzzPII -fuzztime=30s ./internal/ai       │
│   │   ├── go test -fuzz=FuzzChunker -fuzztime=30s ./internal/ai   │
│   │   └── go test -fuzz=FuzzKeyValidation -fuzztime=30s ./internal/service │
│   ├── Integration Tests (集成测试):                                     │
│   │   ├── Docker Compose 启动 Postgres + Qdrant                      │
│   │   ├── go test -tags=integration -count=1 ./internal/integration │
│   │   └── 超时: 5 分钟                                             │
│   └── Coverage Gate (覆盖率门禁):                                          │
│       ├── 每个包 >= 50%（AGENTS.md 要求）                             │
│       ├── `config` 包需要至少基本加载测试                             │
│       └── 新包默认除外（需明确降级）                                   │
├──────────────────────────────────────────────────────────────┤
│ Package-Level Test Fixes (包级测试修复):                                 │
│   ├── config: 添加 TestLoad / TestValidate                          │
│   │   ├── 测试 80+ 环境变量默认值                                    │
│   │   ├── 测试跨字段约束 (AI_VECTOR_BACKEND=qdrant → AI_VECTOR_URL)   │
│   │   └── 测试 `.env` 加载 + 覆盖                                    │
│   ├── mcp: 添加 TestTools / TestCallTool                              │
│   │   ├── 对每个工具：输入 → 预期输出                                 │
│   │   └── 对 chat 工具：mock search + mock llm                        │
│   ├── cli_test.go: 重构                                               │
│   │   ├── 将 1440 行拆分为 5-6 个文件                                 │
│   │   ├── 修复 7 个已知 BUG                                           │
│   │   └── 使用 httptest.NewServer 模拟服务器                          │
│   └── 并行化:                                                          │
│       ├── 删除共享 TestMain 数据库                                     │
│       ├── 每个测试函数使用 t.TempDir() 创建隔离的 SQLite              │
│       └── go test -parallel=4                                          │
└──────────────────────────────────────────────────────────────┘
```

| 复杂度 | 用户影响 | 代码变动 | 差异化 |
|----------|-------------|-------------|------------|
| 低中 | 极高（长期质量） | 中（测试 + CI）| ★★★★★ |

---

### 🥇 方向 4：代码健康与静态分析 — 静态检查、Govulncheck、死代码检测、文档门禁

**为什么需要它：** 236 个文件，~45K 行，7 个已知 BUG，2 个文件超过 500 行限制，`MapPII` 等死代码——代码健康趋势**值得关注但不紧急**。静态分析捕获模式级问题（泄露的 goroutine、不安全指针、竞态条件），但当前 CI 仅运行 `go vet`（基础子集）。`gosec` 等安全 Linter 可捕获 SSRF 和路径遍历风险。

**架构蓝图：**

```
当前: gofmt + go build + go vet (基础) + gocyclo + 行数门禁
改进: Code Health Toolchain (CI 中的自动化)

┌──────────────────────────────────────────────────────────────┐
│ Static Analysis Pipeline (静态分析管线):                               │
│   ├── staticcheck:                                                │
│   │   ├── 捕获：未使用变量、死代码、不正确的锁、未处理错误        │
│   │   ├── 级别：CI 严格模式前使用 warn-only 运行 1 周            │
│   │   └── 通过后：CI 门禁                                            │
│   ├── govulncheck:                                               │
│   │   ├── 扫描 Go 标准库 + 模块的已知 CVE                        │
│   │   └── 门禁：发现任何漏洞则失败                                    │
│   ├── gosec:                                                      │
│   │   ├── 捕获：硬编码凭证、SQL 注入模式、SSRF 风险、路径遍历      │
│   │   ├── `G107`（SSRF）→ 标记 events/webhook.go URL 构建         │
│   │   └── `G304`（路径遍历）→ 验证 storage/local.go 处理器         │
│   ├── ineffassign:                                                │
│   │   └── 捕获已分配但未使用的变量                                    │
│   └── unconvert:                                                   │
│       └── 捕获不必要的类型转换                                        │
├──────────────────────────────────────────────────────────────┤
│ Tech Debt Repayment (技术债务偿还):                                      │
│   ├── 500 行文件：                                                  │
│   │   ├── handler.go (565) → 拆分为 handler_upload.go,              │
│   │   │   handler_download.go handler_admin.go                │
│   │   ├── cli_test.go (1440) → 拆分为 5 个文件（按命令）         │
│   │   └── storage_test.go (1120) → 拆分为 3 个文件（合同+本地+CB） │
│   ├── 7 个已知 BUG (cli_test.go):                                   │
│   │   ├── 审查 + 修复：每个 BUG 一个提交                             │
│   │   └── 跟踪：docs/BUGS.md（当前无）                              │
│   └── 死代码：                                                       │
│       ├── MapPII: 移除或使用（当前未调用）                           │
│       └── SSEScanner 内部类型：保留（由 ChatStream 使用）            │
├──────────────────────────────────────────────────────────────┤
│ Documentation Health (文档健康):                                          │
│   ├── 公共 API 的 godoc 门禁：                                    │
│   │   └── 每个导出类型/函数必须有注释                                 │
│   ├── docs/BUGS.md：文档化已知 BUG + 变通方法                      │
│   └── docs/DEPLOYMENT_CHECKLIST.md：生产就绪清单                    │
└──────────────────────────────────────────────────────────────┘
```

| 复杂度 | 用户影响 | 代码变动 | 差异化 |
|----------|-------------|-------------|------------|
| 低 | 高（长期可维护性） | 最小（CI + 拆分） | ★★★★☆ |

---

### 🥇 方向 5：供应链安全 — SBOM、漏洞扫描、vendor 化、镜像签名

**为什么需要它：** 随着 AeroVault 部署到受监管环境中，供应链安全成为必备条件。当前，依赖通过 `go.mod` + `go.sum` 被引用但无 `vendor/` 目录（离线构建风险），无 CVE 扫描（易受已知漏洞攻击），无 SBOM（阻碍安全审查）。这可能是企业部署购买过程中**第一个被问的问题**。

**架构蓝图：**

```
当前: go.mod + go.sum (锁定) + 无 vendor + 无扫描 + 无签名
改进: Supply Chain Security (CI + 部署)

┌──────────────────────────────────────────────────────────────┐
│ Dependency Auditing (依赖审计):                                       │
│   ├── 每次 CI 运行: `govulncheck ./...`                         │
│   │   ├── Go 标准库 CVE 扫描                                    │
│   │   ├── 模块 CVE 扫描                                         │
│   │   └── 失败门禁：发现高/关键漏洞则失败                          │
│   ├── 每周计划: `trivy fs .` (Docker + Go 二进制分析)            │
│   │   ├── 图像 CVE + 操作系统包                                  │
│   │   └── 输出到 GitHub Issues / Slack                          │
│   └── 依赖通知: Dependabot + Renovate 版本更新 PR                 │
├──────────────────────────────────────────────────────────────┤
│ Supply Chain Artifacts (供应链产物):                                    │
│   ├── 提案: `go mod vendor` (vendor 目录)                       │
│   │   ├── 理由: 可重现构建 + 离线构建 + 依赖审查                 │
│   │   ├── 权衡: repo 大小增加 (~10MB)，但安全团队要求            │
│   │   └── 替代方案: `go mod download` 缓存（需网络）             │
│   ├── SBOM 生成:                                                  │
│   │   ├── 工具: `syft` (Go 二进制扫描) 或 `go-makefile`          │
│   │   ├── 格式: SPDX 2.3 + CycloneDX 1.5                        │
│   │   ├── CI: `syft packages /app/aero-vault -o spdx-json=build/sbom.spdx.json` │
│   │   └── 归档: 作为发布产物                                       │
│   └── 容器镜像签名:                                                │
│       ├── 工具: `cosign` (Sigstore)                              │
│       ├── 密钥: GitHub OIDC (无密钥管理)                           │
│       └── CI: `cosign sign --key env://COSIGN_KEY ...`           │
├──────────────────────────────────────────────────────────────┤
│ License Compliance (许可证合规):                                         │
│   ├── `go-licenses` 或 `licenseclassifier`                       │
│   ├── 生成: THIRD_PARTY_NOTICES.md                                │
│   └── 门禁：拒绝 COPYLEFT / UNKNOWN 许可证                        │
└──────────────────────────────────────────────────────────────┘
```

| 复杂度 | 用户影响 | 代码变动 | 差异化 |
|----------|-------------|-------------|------------|
| 低中 | 高（企业准入） | 最小（CI 集成 + 文档）| ★★★★☆ |

---

## 8. 跨轮综合优先级：所有方向对比三十项

| 阶段 | v1 | v2 | v3 | v4 | v5 | v6 | **v7** |
|-------|-----|-----|-----|-----|-----|-----|--------|
| **P0（立即）** | — | 断路器 | — | 竞态检测 CI | S3 补齐 | 查询重写 | **API DX：统一信封 + 错误规范** |
| **P0** | — | — | — | 数据访问优化 | 存储错误映射 | 函数引擎 | **测试 CI：启用 -race + 模糊测试** |
| **P1（本季度）** | 存储分层 | 自愈网格 | 可观测管线 | 优雅关闭 | 传输安全 | 法律封存 | **运维：深度健康检查 + 关闭贯穿** |
| **P1** | — | — | — | RAG 质量 | 开发者体验 | SLA 仪表板 | **代码健康：静态分析 + 修复 BUG** |
| **P2（下季度）** | FUSE | 搜索联邦 | 合规套件 | 开发者工具链 | 数据可移植性 | 全球网格 | **供应链：SBOM + vendor + 签名** |

---

## 9. 附录：代码库关键元数据（第七轮后）

| 度量 | 值 |
|--------|-------|
| **Go 源文件** | 237（+7 个新分析 md 文件）|
| **总代码行** | ~45,389 |
| **内部包** | 23 |
| **外部依赖** | ~25 Go 模块 |
| **数据库迁移** | 24 对（48 SQL 文件）|
| **环境变量** | 80+ |
| **接口定义** | 14 |
| **存储后端** | 4 |
| **测试覆盖率** | ~55%（行）|
| **CI 门禁** | gofmt / build / vet / gocyclo / 行数 / coverage |
| **CI 缺失** | `-race` / fuzz / 集成 / staticcheck / govulncheck |
| **已知 BUG** | 7（测试注释中声明，本轮确认仍存在）|
| **文档文件** | 14 md + 嵌入的 openapi.json |
| **分析报告** | 7（v1-v7，总 ~140KB）|

---

> *第七次全局扫描完成，未修改任何代码。本轮从开发者体验、运维成熟度、测试基础设施、代码健康、供应链安全五个全新视角审视了代码库。累计七份分析组成一个 360° 评估套件，覆盖 30+ 方向，适合用作团队规划路线图和优先级排序的参考。*
