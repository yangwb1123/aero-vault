# 高价值扩展方向：生产运维盲区 — 配置治理、资源公平调度、存储分析、优雅降级、在线迁移

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — `cmd/server/main.go` + `internal/` 全部子包（237+ Go 源文件，~47K 行），3 套 SDK（Go/Python/JS），MCP 双模式（HTTP+stdio），Web UI，50 对迁移文件，`deploy/` 全套配置（Helm/Grafana/Prometheus/OTel），`HARNESS.md`，`AGENTS.md`  
> **去重验证：** 对 `docs/requirements/` 下全部 105 份既有分析文档（`expansion-directions.md` ~ `expansion-v105-deep-code-integrity-gaps.md` + `genuine-production-blindspots.md`）逐方向进行全文关键词正则 + 语义交叉验证 + 代码锚点反查  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。选取**代码中存在明确锚点、有实质性生产运营影响、且在 105 轮既有分析中未被独立深度覆盖**的方向。每个方向包含：产品价值 → 现状与代码证据 → 架构权衡与建议方案 → 边界情况。

---

## 去重验证总表

| 方向 | 既往覆盖情况 | 结论 |
|------|-------------|------|
| **方向一：声明式配置与 IaC 验证框架** | v15 在表格中一行提及"config file"概念；v27 方向三的 Go 类型级 `Validate()` 方法分析聚焦**运行时字段校验的代码实现**而非配置管理基础设施（无文件格式、无 diff、无版本化、无 validate 子命令、无 schema 导出）；v42/v60/v65/v87/v90 零实质性覆盖。**正则搜索 `config.*file\|config.*schema\|config.*validate.*cmd\|config.*diff\|validate.*subcomm\|yaml.*config\|config.*IaC\|config.*version\|config.*format\|config.*declarative`** → 仅 v15 表格行命中，且无代码锚点 | ✅ **全新方向** |
| **方向二：多租户资源治理与公平调度** | v104 方向四覆盖「流式路径内存压力管理」聚焦**单个请求的字节级内存消耗**；v94 方向二覆盖「准入控制与并发治理」聚焦**请求级并发上限**；v59/v89 覆盖「per-tenant unfair rate limiting」聚焦**速率限制不均匀**。但**零文档**覆盖：跨协议资源池协调（REST/S3/WebDAV/MCP 共享同一进程资源）、per-bucket 配额与隔离、租户级公平调度（noisy neighbor 保护）、存储 I/O 优先级、全局资源预算。**正则搜索 `cross-protocol.*rate\|per-bucket.*quota\|bucket.*quota.*isolat\|fair.*schedule.*tenant\|noisy.*neighbor.*protect\|resource.*budget.*tenant\|storage.*io.*priorit\|global.*resource.*pool`** → 0 独立深度覆盖 | ✅ **全新方向** |
| **方向三：存储用量分析与成本归因面板** | v4 在容器化方向表格一行提及「metrics 仪表盘」但聚焦 Grafana 操作面板；v89 在运营治理方向提及「usage analytics」但聚焦**租户级用量统计的 API 端点**而非可视化和成本归因。**零文档**系统分析：存储增长趋势可视化、top-N 访问对象热力图、冷数据识别、每租户/每桶成本归因、访问模式分析、存储效率（去重比、压缩比）指标。**正则搜索 `storage.*growth\|cost.*attribution\|top.*accessed\|access.*pattern\|cold.*data.*report\|storage.*efficien\|dedup.*ratio.*dashboard\|compression.*ratio\|usage.*trend\|analytics.*dashboard`** → 0 独立深度覆盖 | ✅ **全新方向** |
| **方向四：优雅降级与故障模式透明化** | v10 方向六覆盖「优雅降级与熔断」但聚焦**BM25 不可用时的搜索降级**；v25 表格行覆盖「AI 降级模式」但仅 `AI_DEGRADED_MODE` 单一开关；v71 方向一覆盖「熔断器」但聚焦**存储后端的 circuit breaker**；v104 方向一覆盖「服务层双写事务完整性」聚焦**数据一致性**。**零文档**系统分析：全局健康端点缺乏子系统粒度、无降级状态枚举与通知、无运维面板显示当前降级状态、无 "degraded since" 时间戳或影响范围报告、无实验性功能渐进式发布框架。**正则搜索 `health.*subsystem\|degraded.*since\|degradation.*panel\|partia.*outage\|feature.*flag.*rollout\|gradual.*rollout\|health.*status.*page\|健康.*子系统\|降级.*状态`** → 0 独立深度覆盖 | ✅ **全新方向** |
| **方向五：在线对象迁移与存储重平衡** | v25 方向四覆盖「对象搬迁与存储后端重平衡」但仅 6 行概念描述——无代码锚点、无架构方案、无边界情况；v94 方向一覆盖「分层存储迁移」聚焦**冷热分层（STANDARD→GLACIER）** 而非通用的后端间迁移；v87/v32/v77/v11/v90/v72/v5/v10 均为浅层提及。**正则搜索 `live.*migrat.*object\|storage.*rebalanc\|object.*rebalanc\|move.*between.*backend\|blob.*migrat\|storage.*node.*rebalanc\|data.*relocat.*storage\|rebalanc.*blob`** → 仅 v25 方向四 6 行概念提及，无任何代码锚点或架构方案 | ✅ **全新深度方向** |

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 |
|---|------|------|--------|---------|---------|
| **1** | **声明式配置与 IaC 验证框架：从失控的 env var 扩散到可版本化、可验证、可审计的配置管理** | 运维/工程基础设施 | **P1** | 100+ 环境变量零散管理，无配置文件格式，无 schema 导出，无 `validate` 子命令，无 diff/versioning；`config.Load()` 直接从 env 读取，错误仅在启动时发现；重构后遗留的 env var 无声不被检测 | `internal/config/config.go:Load()`（全部从 `getEnv`/`getEnvInt`/`getEnvBool` 读取——~150 行纯 env var 绑定，零配置文件路径）；`internal/config/config.go:Validate()`（仅 5 个基本检查——storage backend 存在性、DB driver 类型、embed 端点非空、timeout 非负、rate limit 对称——无全面语义验证）；`internal/config/config_app.go`（11 个子结构体分散配置，**无单一可导出的 schema**）；`cmd/server/main.go:run()`（`cfg, err := config.Load()` — 错误即 `os.Exit`，无降级或交互式修复）；`internal/config` 目录（无 `schema.go`、`diff.go`、`validate.go`、`file.go`、`upgrade.go` 文件） |
| **2** | **多租户资源治理与公平调度：从尽力而为的并发到有保障的服务质量** | 生产可靠性/SaaS 运营 | **P1** | 所有资源限制在请求级（RPS、并发数），无跨协议协调。一个租户通过 REST 和 S3 同时发送请求可绕过 PerTenantMax 限制。无 per-bucket 配额、无存储 I/O 优先级、无公平调度、无突发保护。一个 noisy neighbor 租户可以降低所有其他租户的体验。 | `internal/middleware/ratelimit.go:PerTenantRateLimiter`（**仅按 tenant 建 token bucket**，不同协议路径（REST/S3/WebDAV/MCP）各自拥有独立的 middleware 实例——**不共享令牌桶**）；`internal/config/config.go:AppConfig.PerTenantMax`（`PER_TENANT_CONCURRENCY_MAX`——限制**同一 tenant 的并发请求数**，但不同协议的请求从不同入口进入，此限制被绕过）；`internal/service/file_crud.go:Put`（`checkQuota` 仅检查 `tenant.MaxBytes`/`MaxObjects`——**无 bucket 级配额**）；`internal/service/quota_test.go`（仅测试 tenant 级配额，无 bucket 配额测试）；`internal/repository/repository.go:GetTenantQuota` 和 `SetTenantQuota`（仅 tenant 级，`buckets` 表无 `max_bytes`/`max_objects` 字段）；`internal/api/rest/admin.go:SetQuota`（`PUT /v1/admin/tenants/{tenant}/quota`——只有 tenant 端点）；`internal/repository/sql_objects.go:AddTenantUsage`（写入 `tenant_usage` 表——仅累加 tenant 级用量）；`internal/storage/circuitbreaker.go`（存储后端熔断器——但**熔断时不区分 tenant**，一个租户的 S3 超时触发熔断 = 所有租户的 S3 请求被拒绝）；`internal/jobs/jobs.go:Pool`（Job 池不区分租户——一个租户的 1000 个复制 job 可排空全 pool） |
| **3** | **存储用量分析与成本归因面板：从黑箱存储到可观测、可归因、可优化的成本模型** | 运维/成本优化 | **P1** | 无存储增长趋势可视化、无 top-N 访问对象识别、无冷数据报告、无每租户/每桶成本归因、无存储效率指标（如去重比、压缩比）。运维人员无法回答"谁用了多少存储？为什么增长？哪些数据可以归档或删除？" | `internal/telemetry/metrics.go`（15+ OTel instruments——覆盖请求延迟、AI 成本、job 队列等，但**无存储增长趋势、无用量分布、无访问频次**）；`internal/repository/sql_objects.go:StorageClassCounts`（按 storage_class 聚合——**单一统计查询，无趋势、无热力图**）；`internal/repository/sql_buckets.go:BucketStats`（`SELECT COUNT(*), COALESCE(SUM(size),0) ...`——**单点快照，无历史曲线**）；`internal/service/file_features.go:GetBucketStats`（返回 `{Count, TotalBytes}`——仅当前快照）；`internal/api/rest/admin.go:ListTenantQuotas`（返回 `{TenantID, MaxBytes, MaxObjects, CurrentBytes, CurrentObjects}`——**仅有总量，无明细**）；`internal/reconcile/job.go:maybeScrub`（Scrub 遍历对象——可以顺便统计，但**无访问频次记录**）；`internal/repository/repository.go:Event`（事件结构有 `Type, ObjectID, Key` 等——可用来统计对象访问频次，**但当前无消费端**）；`docs/deploy/grafana/`（现有 Grafana dashboard **12 个 panel**——均聚焦**操作延迟和 AI 指标**，无存储用量面板）；`internal/storage/local_list.go:List`（`filepath.Walk`——遍历所有 blob，**可用于统计但无持久化**） |
| **4** | **优雅降级与故障模式透明化：从二元健康到渐进式降级感知** | 可靠性/运维 | **P2** | `/healthz` 与 `/readyz` 返回二元结果（200/503），不区分"完全正常"与"部分降级"。AI 后端不可用时 `/search` 返回空结果（非 503）。存储后端熔断时不通知客户端请求被拒的原因。运维人员无法从单一端点获得全局健康状态 | `internal/api/rest/router.go`（无 `/debug/health` 或 `/status` 端点——`/healthz` 和 `/readyz` 由标准 chi 中间件处理，不检查子系统）；`internal/middleware/middleware.go`（无健康检查 handler——**当前健康端点为 `main.go` 中 `chi.Healthz()` 的默认实现**）；`cmd/server/main.go:240-245`（`r.Get("/healthz", chi.Healthz())` — chi 内置实现，**ping DB 后返回 200**，不检查 storage/AI/events 等子系统）；`internal/telemetry/prometheus.go`（Prometheus `/metrics` 暴露 15+ 计数器——**但无 `aero_up{component="..."}` 或 `aero_degraded{reason="..."}` 这类直接的健康指标**）；`internal/storage/circuitbreaker.go:State()`（熔断器有 `Open/HalfOpen/Closed` 状态——**被存储层内部使用，不暴露给健康端点**）；`internal/ai/search.go:Search`（`if s.embedder == nil` 时返回 `[]Hit{}`——**静默空结果而非 503**）；`internal/config/config.go:AIConfig.DegradedMode`（`AI_DEGRADED_MODE` 单一开关——**只控制 AI 端点是否返回错误，不提供子系统级降级**）；`internal/reconcile/job.go:Run`（Reconcile 失败仅 `slog.Warn`——**运维无法知道 GC/scrub 是否正在运行**）；`internal/ai/indexer.go:Run`（Indexer 失败仅 warn——**运维无法知道索引是否健康**） |
| **5** | **在线对象迁移与存储重平衡：从静态绑定到可移植的存储后端** | 架构/运维 | **P2** | 对象在创建时绑定到一个存储后端，之后无法移动到另一个后端。无法：从 local FS 迁移到 S3、从一个 S3 bucket 迁移到另一个、在存储节点之间重平衡对象、为特定对象切换 storage class 对应的后端。每次后端变更需要全量导出再导入 | `internal/storage/storage.go:Storage` 接口（**无 `Move(ctx, srcKey, dstKey) error` 或 `CopyTo(ctx, Storage, key) error` 方法**）；`internal/storage/local.go`（`LocalStorage`——`os.Rename` 可用于同磁盘移动，但**接口不暴露**）；`internal/storage/s3.go`（`S3Storage`——S3 `CopyObject` API 可用于服务端复制，但**接口不暴露**）；`internal/service/file_crud.go:Put`（写入路径仅 `s.store.Put(ctx, sk, ...)`——**始终将对象写入默认后端**，无后端选择逻辑）；`internal/service/file_features.go:170-191`（`PresignGet`/`PresignPut` 生成当前后端的签名 URL——**不感知其他后端的存在**）；`internal/service/file_crud.go:Get`（返回 `s.store.Get(ctx, obj.StorageKey)`——**硬绑定到存储时的后端**）；`internal/repository/repository.go:Object`（`StorageKey` 字段隐式编码了后端标识——`path.Join(tenant, bucket, key)`——**无后端选择器字段如 `StorageBackend` 或 `StorageEndpoint`**）；`internal/replication/replication.go:ReplicateObjectByID`（唯一的多后端操作——但**仅用于跨区域复制，不支持重平衡**）；`internal/reconcile/job.go:sweepOrphans`（遍历 storage 后端的 blob 列表——**可用于枚举但无迁移逻辑**）；`internal/storage/factory.go:NewStorageFrom`（单次创建后**无法更换后端**）；`internal/config/config.go:StorageConfig`（单一 `Backend` 字段——**不支持多后端共存**） |

---

## 方向一：声明式配置与 IaC 验证框架

### 产品价值

| 运营场景 | 当前体验 | 期望体验 |
|---------|---------|---------|
| **新环境部署** | 从 `.env.example` 手动复制粘贴 100+ 变量；拼写错误在启动时暴露 | `aero-vault config validate staging.yaml` 在 CI 中通过 |
| **配置审计** | 无法回答"当前运行的配置是什么？与基准配置的差异是什么？" | `aero-vault config diff --from baseline.yaml --live` |
| **版本回滚** | 回滚二进制但不回滚 env vars → 新旧混合，无声行为变化 | 配置版本化 + `aero-vault config apply v1.2.3.yaml` |
| **配置重构** | 删除一个废弃的 env var，没人知道是否还有其他代码用 | schema 导出标记为 `deprecated`，运行时 warn |
| **Changelog 影响** | 升级文档写"新增 XXX_CONFIG"，用户手动合并 | 配置迁移工具 `aero-vault config migrate old.yaml > new.yaml` |

### 现状

当前配置系统完全是 env var 驱动，没有任何配置文件格式的支持：

**`internal/config/config.go` 的全部逻辑是 env var → struct 绑定：**

```go
func Load() (*Config, error) {
    _ = godotenv.Load()
    // ~150 行 getEnv/getEnvInt/getEnvBool 调用
    cfg := &Config{
        App: AppConfig{
            Addr:    getEnv("APP_ADDR", ":8080"),
            LogLevel: logLevel,
            // ... 50+ 字段
        },
        // 10 个子结构体，每个 ~5-20 个字段
    }
    if err := cfg.Validate(); err != nil {  // 仅 5 个基本检查
        return nil, err
    }
    return cfg, nil
}
```

**`Validate()` 仅覆盖最基本的边界情况：**

```go
func (c *Config) Validate() error {
    if err := c.validateStorage(); err != nil { return err }
    // 仅检查: DB driver 为 "postgres"|"sqlite"
    //        DB_DSN 非空
    //        AI_EMBED_ENDPOINT 非空（当 AI 启用 + provider=http）
    //        timeout 非负
    //        rate limit 对称且非负
    // 不检查: 端口冲突、AI 配置完整性、事件配置一致性
    //         存储凭证是否有效、Schema 版本是否兼容
    return nil
}
```

**没有配置文件格式支持的关键缺失：**

| 能力 | 实现行数 | 原因 |
|------|---------|------|
| YAML/JSON/Toml 配置文件解析 | 0 | Godotenv 仅加载 `.env` |
| `config validate` 子命令 | 0 | CLI 无此子命令 |
| Schema 导出（JSON Schema / OpenAPI 风格） | 0 | 无结构化 schema 定义 |
| 配置 diff | 0 | 无配置比较功能 |
| 配置版本化 | 0 | Config 结构无 Version 字段 |
| 废弃字段检测 | 0 | 无 Deprecated 注解 |
| 输出当前运行时配置 | 0 | 无 `/debug/config` 端点 |

### 架构权衡与建议方案

**推荐方案：Env Var + YAML 双通道，配置文件优先**

```
优先级（从高到低）：
1. 命令行 flag（如 --config-file, --log-level=debug）
2. 配置文件（config.yaml，由 --config-file 指定）
3. 环境变量（与当前行为一致，作为 overlay）
4. 内建默认值（与当前行为一致）
```

**配置结构建议：**

```yaml
# aero-vault 配置文件示例
version: "1.0"
meta:
  environment: "production"
  cluster: "us-east-1"

app:
  addr: ":8080"
  log_level: "info"
  write_timeout: 60s
  max_inflight: 100

storage:
  backend: "s3"
  s3:
    endpoint: "https://s3.us-east-1.amazonaws.com"
    region: "us-east-1"
    bucket: "aero-data"
  default_class: "STANDARD"
  circuit_breaker:
    failure_threshold: 5
    recovery_timeout: 30s
    half_open_max: 3

ai:
  enabled: true
  embed:
    provider: "http"
    endpoint: "https://api.openai.com/v1/embeddings"
    model: "text-embedding-3-small"
    dimension: 256
  chat:
    provider: "http"
    endpoint: "https://api.openai.com/v1/chat/completions"
    model: "gpt-4o-mini"
  search_cache:
    size: 1000
    ttl: 30s
```

**Schema 生成（关键基础设施）：**

```go
// internal/config/schema.go — 新文件
type Field struct {
    Key         string   `json:"key"`
    Type        string   `json:"type"`
    Default     string   `json:"default,omitempty"`
    Description string   `json:"description,omitempty"`
    Deprecated  bool     `json:"deprecated,omitempty"`
    EnvVar      string   `json:"env_var,omitempty"`
    Required    bool     `json:"required,omitempty"`
    Validate    string   `json:"validate,omitempty"` // regex or check name
}

// ExportSchema returns the complete configuration schema as JSON.
func ExportSchema() []Field { ... }
```

**CLI 子命令扩展（`internal/cli/cli.go`）：**

```go
// aero-vault config validate <file>         # 验证配置文件语法和语义
// aero-vault config schema                  # 导出 JSON Schema
// aero-vault config diff <a> <b>            # 比较两个配置文件
// aero-vault config show                    # 显示当前运行时配置
// aero-vault config migrate <old> > <new>   # 升级到新版本
```

**`cmd/server/main.go` 扩展：**

```go
func main() {
    if len(os.Args) > 1 {
        switch os.Args[1] {
        case "config":
            os.Exit(configCLI(os.Args[2:]))
        case "mcp": ...
        case "cli": ...
        }
    }
    if err := run(); err != nil { ... }
}
```

### 边界情况

| 场景 | 处理 |
|------|------|
| 配置文件和 env var 冲突 | 配置文件优先，env var 覆盖——行为可预测 |
| 未知配置字段 | warn（非 fatal）——向后兼容性 |
| 废弃字段 | 加载时 `slog.Warn("config: STORAGE_LEGACY_FIELD is deprecated, use STORAGE_NEW_FIELD instead")` |
| 敏感值（密码、密钥） | 支持 `file://` 引用（`sse_key: "file:///run/secrets/sse_key"`）和 env var 引用（`token: "${AUTH_TOKEN}"`） |
| 配置文件不存在 | 退回到纯 env var 模式（向后兼容） |
| 运行时变更 | 配置在启动时加载——不支持热重载（可由外部挂载 ConfigMap + SIGUSR1 重载实现） |

---

## 方向二：多租户资源治理与公平调度

### 产品价值

| 运营场景 | 当前行为 | 期望行为 |
|---------|---------|---------|
| 租户 A 突发 10000 个并发上传 | 所有租户共享 `MaxInFlight` 连接池，租户 B/C/D 的请求被饿死 | 租户 A 受限于 `PerTenantConcurrency=100`，其它租户不受影响 |
| 租户 A 同时通过 REST + S3 + WebDAV 涌入 | 三个协议各自有独立的并发限制 → 实际并发 = 3× 配置值 | 全局协调，租户级总并发上限 |
| 租户 A 的存储用量激增 | 仅 tenant 级 `max_bytes` 限制，租户 A 的一个桶占满全部配额 → 同一租户下其他桶无法写入 | 提供 bucket 级 `max_bytes`/`max_objects` 配额覆盖 tenant 级设置 |
| 存储后端熔断 | `storage/circuitbreaker.go` 熔断时不区分租户——一个租户的 S3 超时导致所有租户的 S3 请求被拒绝 | 按租户或按后端分离熔断（至少提供 per-tenant 降级路径） |
| 后台 Job 排队 | 租户 A 的 500 个复制 job 排满 pool→租户 B 的索引 job 等待 | 按租户/按 job 类型的加权公平队列 |

### 现状

**跨协议并发控制缺失：**

`PerTenantMax` 定义在 `internal/config/config.go`：

```go
type AppConfig struct {
    // ...
    MaxInFlight  int // MAX_INFLIGHT_REQUESTS; 0 = unlimited
    PerTenantMax int // PER_TENANT_CONCURRENCY_MAX; 0 = unlimited
}
```

但在 `cmd/server/main.go` 中，这些限制仅用于 REST 路由组：

```go
r.Group(func(r chi.Router) {
    r.Use(mw.MaxInFlight(cfg.App.MaxInFlight))
    r.Use(mw.PerTenantMax(cfg.App.PerTenantMax))
    // REST routes ...
})
```

S3 handler、WebDAV handler、MCP HTTP handler 在**不同的路由组/不同的 mux** 上注册，不使用这些中间件。

**速率限制 per-protocol 而非 global：**

```go
// internal/middleware/ratelimit.go
type PerTenantRateLimiter struct {
    mu     sync.Mutex
    limit  float64
    burst  int
    tenants map[string]*rate.Limiter
}
```

REST handler 创建一个 `PerTenantRateLimiter`，S3 handler 创建另一个——两个独立的令牌桶。一个租户可以通过 REST 消耗其 RPS 限制，同时通过 S3 使用另一个完整的 RPS 限制。

**无 bucket 级配额：**

```go
// internal/repository/repository.go — 仅 tenant 级配额
type TenantRecord struct {
    ID              string
    MaxBytes        int64   // tenant 级上限
    MaxObjects      int64   // tenant 级上限
    CurrentBytes    int64
    CurrentObjects  int64
    DailyBudgetUSD  float64
    // 无 per-bucket 配额字段
}
```

**Job 池无租户隔离：**

```go
// internal/jobs/jobs.go:Pool
// 所有 job 类型（复制、索引、防病毒）共享同一个 worker pool
// 不区分租户——一个租户的大量 job 可以排空整个池
```

**存储后端熔断器无租户意识：**

```go
// internal/storage/circuitbreaker.go
// 熔断器按 backend 实例维护——所有租户共享同一个熔断器
// 租户 A 的 S3 超时 → 熔断器打开 → 租户 B/C/D 的 S3 请求失败
```

### 架构权衡与建议方案

**1. 全局资源池（推荐）：**

引入一个 `ResourceGovernor`，作为所有协议路径共用的中心资源管理器：

```go
type ResourceGovernor struct {
    // 全局并发限制
    maxGlobalInFlight int64
    inFlight          atomic.Int64
    
    // 按租户并发限制
    maxPerTenant int64
    perTenant    sync.Map // tenantID → atomic.Int64
    
    // 按租户令牌桶（统一 rate limiter，所有协议共享）
    rateLimiters *PerTenantRateLimiter
    
    // 存储 I/O 权重
    priorities map[string]int // tenantID → weight
}
```

**中间件集成点（所有协议共享）：**

```go
// 在中间件链的开头（Auth 之后，业务逻辑之前）
r.Use(mw.GlobalResourceGovernor(resourceGov))
```

这个中间件：
- 在所有协议（REST/S3/WebDAV/MCP）中注册
- 在每个请求进入时 `Acquire()` 一个槽位，结束时 `Release()`
- 按租户维护子计数器
- 借用 token bucket ——所有协议共享同一个桶

**2. 分桶配额（中等）：**

在 `buckets` 表添加 `max_bytes` 和 `max_objects` 列，在 `FileService.Put` 路径增加 `checkBucketQuota` 检查（与现有 `checkQuota` 并列）：

```go
// internal/service/file_crud.go:Put
func (s *FileService) Put(ctx, tenant, bucket, key string, ...) {
    // ... key 校验、lock 检查 ...
    if err := s.checkQuota(ctx, tenant, objSize); err != nil {
        return ..., err  // tenant 级配额
    }
    if err := s.checkBucketQuota(ctx, tenant, bucket, objSize); err != nil {
        return ..., err  // bucket 级配额（新增）
    }
    // ... storage.Put ...
}
```

**3. Job 池公平调度（中等）：**

引入多级队列（每个租户一个 FIFO），用一个加权 round-robin 调度器从各租户的队列中取 job：

```go
type FairPool struct {
    workerCount int
    queues      map[string]chan repository.Job  // tenantID → job channel
    scheduler   func() (tenantID string)         // weighted round-robin
    reg         *Registry
}
```

### 边界情况

| 场景 | 处理 |
|------|------|
| tenant 级 quota 和 bucket 级 quota 冲突 | bucket 级覆盖 tenant 级（更细粒度优先） |
| 跨协议 rate limiter 同步 | 所有协议通过同一个 `RateLimiter` 实例——使用 sync.Map + atomic 实现无锁读 |
| 熔断器按租户 vs 按后端 | 优先按后端（保护后端），但在响应中携带 `X-Aero-Rate-Cause: circuit-breaker(backend=s3, state=open)` |
| 新租户接入 | 自动继承默认配额（可由 `TenantRecord.DefaultQuota` 配置） |
| 公平调度中的优先级反转 | 高优先级 job（用户交互）不应被低优先级 job（批量复制）阻塞——实现优先级队列 |

---

## 方向三：存储用量分析与成本归因面板

### 产品价值

| 运营场景 | 当前体验 | 期望体验 |
|---------|---------|---------|
| **成本归因** | 无法回答"上个月哪个租户/桶用了多少存储？出站带宽费用是多少？" | 月度成本报表自动生成，按租户、桶、storage class 拆分 |
| **容量规划** | 无法回答"存储增长率是多少？何时会达到上限？" | 趋势图表 + 预测线 + 配额警报 |
| **冷数据识别** | 无法回答"哪些对象在过去 90 天未被访问？可以归档到低成本存储吗？" | 冷数据报表 + 一键归档建议 |
| **访问热点分析** | 无法回答"哪些对象被最频繁地读取？可以缓存到 CDN 吗？" | Top-N 访问热力图 + 缓存建议 |
| **存储效率** | 无法回答"存储去重率是多少？压缩率是多少？" | 效率仪表盘 + 优化建议 |

### 现状

**现有指标全部面向操作延迟和 AI 成本：**

```
# 现有 OTel 指标（internal/telemetry/metrics.go）
ai_requests_total           # AI 请求计数
ai_tokens_total             # token 用量
ai_cost_micros_total        # AI 成本
ai_embed_duration_ms        # 嵌入延迟
ai_search_duration_ms       # 搜索延迟
jobs_pending                # job 队列深度
storage_bytes               # 当前存储用量
storage_objects             # 当前对象数
events_dropped_total        # 事件丢失
idempotency_replays_total   # 幂等重放
indexer_skip_total          # 索引跳过
reconcile_orphan_blobs      # 孤儿 blob 数
reconcile_orphan_deleted    # 已删除孤儿 blob
http.server.requests        # HTTP 请求计数（已有）
http.server.duration_ms     # HTTP 请求延迟（已有）
```

**缺失的存储分析数据：**

| 数据维度 | 来源 | 当前状态 |
|---------|------|---------|
| 存储增长趋势（按天/周/月） | `tenant_usage` 表的历史数据 | 有 `AddTenantUsage` 但**无聚合查询**和**无趋势可视化** |
| Top-N 访问对象 | `object_events`（EventAccessed 类型） | `EventAccessed` 事件存在但**无消费端统计** |
| 冷数据（90 天未访问） | 对象 `updated_at` + `EventAccessed` | `updated_at` 在元数据中，但**无冷数据查询 API** |
| 每桶成本 | 桶对象数 × 每 GB 单价 | **无成本模型**、无单价配置 |
| 出站带宽 | HTTP 响应大小累计 | `http.server.duration_ms` 不计 body 大小 |
| 去重率（如启用） | content_hash 引用计数 | **去重尚未实现** |

### 架构权衡与建议方案

**推荐：构建 `internal/analytics` 包 + `POST /v1/analytics/*` 端点**

```
internal/analytics/
├── analytics.go          # Analytics 引擎，聚合查询
├── growth.go             # 存储增长趋势
├── top_access.go         # 热门对象统计
├── cold_data.go          # 冷数据识别
├── cost.go               # 成本归因模型
├── efficiency.go         # 去重/压缩效率
└── analytics_test.go
```

**核心数据流：**

```
                 ┌──────────────────────┐
                 │   object_events 表    │
                 │  (EventAccessed 等)   │
                 └──────────┬───────────┘
                            │ 定时聚合
                            ▼
                 ┌──────────────────────┐
                 │   daily_analytics     │  ← 新表：每日聚合快照
                 │   (tenant, bucket,    │
                 │    date, total_bytes, │
                 │    total_objects,     │
                 │    total_gets,        │
                 │    total_puts,        │
                 │    egress_bytes)      │
                 └──────────┬───────────┘
                            │ 查询 API
                            ▼
                 ┌──────────────────────┐
                 │  GET /v1/analytics/   │
                 │  growth?tenant=&     │
                 │  bucket=&period=30d  │
                 └──────────────────────┘
```

**关键查询 API：**

```go
// GET /v1/analytics/growth?tenant=acme&bucket=data&period=30d
// → {"daily_usage": [...], "growth_rate_pct": 3.2, "estimated_full_date": "2027-01-15"}

// GET /v1/analytics/top-access?tenant=acme&bucket=data&limit=20&period=7d
// → {"objects": [{"key": "hot-file.pdf", "gets": 5432, "bytes": 1048576, ...}]}

// GET /v1/analytics/cold-data?tenant=acme&bucket=data&since=90d&limit=100
// → {"objects": [{"key": "archive-2024.tar.gz", "last_accessed": "2024-06-15", "size": 5368709120, ...}]}

// GET /v1/analytics/cost?tenant=acme&period=monthly
// → {"total_cost": 123.45, "breakdown": {"STANDARD": 98.00, "STANDARD_IA": 25.45}}
```

**Grafana Dashboard 扩展：** 现有仪表盘（12 panels）增加 4 个新 panel：

| Panel | 类型 | 数据来源 |
|-------|------|---------|
| 存储增长趋势 | 面积图（按租户分色） | `/v1/analytics/growth` |
| Top-10 热门对象 | 表格（key + get 次数 + 带宽） | `/v1/analytics/top-access` |
| 冷数据报表 | 表格（key + 最后访问 + 大小 + 建议动作） | `/v1/analytics/cold-data` |
| 月度成本归因 | 饼图 + 柱状图 | `/v1/analytics/cost` |

**降低成本的数据采集：**

当前已有 `object_events` 表和 `EventAccessed` 事件。关键缺口是**没有消费端来聚合这些事件**。新增一个轻量 `AnalyticsCollector`：

```go
type AnalyticsCollector struct {
    repo repository.Repository
    // 每日聚合缓存
    mu          sync.Mutex
    dailyAccess map[string]map[string]*accessStats // tenant → bucket → stats
}
```

在上传/下载路径中增加调用：

```go
// internal/service/file_crud.go:Get
func (s *FileService) Get(ctx, tenant, bucket, key) {
    // ... 现有逻辑 ...
    s.analytics.RecordAccess(ctx, tenant, bucket, key, obj.Size)
    return rc, obj, nil
}
```

### 边界情况

| 场景 | 处理 |
|------|------|
| 分析数据延迟 | 每日聚合 `T+1`（离线风格），实时查询回退到当前表扫描 |
| 大量冷数据查询 | 限制 `limit=1000`，分页返回；提供 `since` 时间范围 |
| 成本模型自定义 | 通过 `STORAGE_COST_PER_GB_STANDARD`、`STORAGE_COST_PER_GB_GLACIER` 等 env var 配置单价 |
| 历史数据保留 | `daily_analytics` 表保留 13 个月（满足年度对比），由 retention job 清理 |
| Top-N 准确性 | 内存中 sliding window + 定期 flush 到 DB；允许少量计数丢失（不阻塞请求路径） |

---

## 方向四：优雅降级与故障模式透明化

### 产品价值

| 运营场景 | 当前行为 | 期望行为 |
|---------|---------|---------|
| **AI 后端不可用** | `/search` 返回 `[]`（空结果），`/chat` 返回 `500`；原因无差异 | `/healthz` 返回 `200` + `{"degraded": ["ai"], "reason": "embedder unreachable"}` |
| **存储后端熔断** | `/healthz` 返回 `200`，但实际 `PUT`/`GET` 返回 503 或超时 | `/healthz` 检测熔断器状态，返回 `503` + `{"circuit_breaker": {"backend": "s3", "state": "open", "since": "2026-07-11T10:00:00Z"}}` |
| **DB 复制延迟** | Postgres 复制延迟 > 30s，读取可能返回过期数据 | `/readyz` 返回 `503` + `{"replication_lag_seconds": 45}` |
| **BM25 索引重建中** | 搜索返回空结果（BM25 未就绪），用户误以为无数据 | 搜索返回 `X-Aero-Search-Mode: vector-only` header；`/healthz` 报告 `{"bm25": "building", "eta_seconds": 120}` |
| **事件总线缓冲溢出** | `events_dropped_total` 递增，运维不感知 | `/healthz` 报告 `{"events_dropped_total": 42}` |

### 现状

**健康端点过于简单：**

```go
// cmd/server/main.go:240-245
r.Get("/healthz", chi.Healthz())        // chi 默认实现：ping DB 后返回 200
r.Get("/readyz", chi.Healthz())         // 同样，仅 ping DB
```

chi 的 `Healthz()` 实现大致为：

```go
func Healthz() http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if _, err := w.Write([]byte("OK")); err != nil {
            // ...
        }
    }
}
```

**没有子系统状态可观察性：**

| 子系统 | 当前状态可观察性 | 故障时行为 |
|--------|----------------|-----------|
| **存储后端** | `circuitbreaker.go:State()` 有熔断状态——**不暴露到 HTTP** | 熔断器打开后 `store.Put/Get` 返回 `ErrBackendUnavailable`，handler 返回 503 |
| **AI Embedder** | `search.go` 检查 `s.embedder != nil`——**不暴露状态** | Embedder 报错 → `search.go` 返回空 `[]Hit{}`，**无 503** |
| **AI LLM** | `chat.go` 同样 nil 检查——**不暴露状态** | LLM 报错 → Chat 返回 `500` |
| **BM25 索引** | `bm25.go:ready` atomic bool——**不暴露** | 未就绪时返回空结果 |
| **事件总线** | `Bus.Dropped()` 可查询——**不暴露** | 缓冲溢出静默丢事件 |
| **索引器** | Indexer.Run 中的错误仅 `slog.Warn`——**不暴露** | 索引中断无告警 |
| **Job 池** | `CountJobsByStatus` 可查询——**不暴露到健康端点** | 队列积压无信号 |
| **Reconcile** | Reconcile 最后运行时间——**不记录** | GC/Scrub 静默停止 |

**缺少降级模式定义：**

```go
// 当前：一个布尔开关
type AIConfig struct {
    DegradedMode bool  // 控制 AI 端点是否返回错误
    // ...
}
```

需要的是细粒度的降级状态枚举：

```go
type DegradationState struct {
    Component   string    `json:"component"`
    Status      string    `json:"status"`      // "healthy" | "degraded" | "down"
    Since       time.Time `json:"since,omitempty"`
    Reason      string    `json:"reason,omitempty"`
    RecoveredAt time.Time `json:"recovered_at,omitempty"`
}

type HealthReport struct {
    Status      string             `json:"status"`       // "healthy" | "degraded" | "down"
    Uptime      string             `json:"uptime"`
    Version     string             `json:"version"`
    Components  []DegradationState `json:"components"`
    DegradedSince time.Time        `json:"degraded_since,omitempty"`
}
```

### 架构权衡与建议方案

**1. 健康端点增强（最小可行）：**

```go
// GET /healthz → 200 + {"status":"healthy","components":[...]}
// GET /healthz → 200 + {"status":"degraded","components":[{"component":"ai_embedder","status":"down"}],"degraded_since":"..."}
// GET /readyz → 503 + {"status":"down","components":[{"component":"storage_s3","status":"down","since":"...","reason":"circuit_breaker_open"}]}

type HealthChecker struct {
    components []ComponentChecker
}

type ComponentChecker interface {
    Name() string
    Check(ctx context.Context) ComponentStatus
}

type ComponentStatus struct {
    Healthy bool
    Reason  string
    Since   time.Time
}
```

**注册组件：**

```go
health.Register("storage_s3", &StorageHealth{store: s3store})
health.Register("ai_embedder", &AIHealth{embedder: embedder})
health.Register("ai_bm25", &BM25Health{bm25: bm25})
health.Register("events_bus", &BusHealth{bus: bus})
health.Register("job_pool", &JobPoolHealth{pool: pool})
health.Register("reconcile", &ReconcileHealth{reconciler: reconciler})
health.Register("indexer", &IndexerHealth{indexer: indexer})
```

**2. 降级模式响应（关键体验）：**

当部分子系统降级时，相关 API 应返回明确的降级信号而非静默空结果：

```go
// 搜索降级示例
func (h *AIHandler) Search(w http.ResponseWriter, r *http.Request) {
    // ...
    hits, err := h.search.Query(ctx, req)
    if err != nil {
        if errors.Is(err, ai.ErrEmbedderUnavailable) {
            w.Header().Set("X-Aero-Degraded", "embedder")
            w.Header().Set("X-Aero-Degraded-Since", degradedSince)
            // 返回空结果 + 降级 header，非 503
        }
    }
    writeJSON(w, http.StatusOK, searchResponse{Hits: hits, Degraded: err != nil})
}
```

**3. 运维面板端点：**

```go
// GET /v1/admin/health → 完整健康报告（验证 Auth/admin scope）
// GET /v1/admin/degradation → 当前降级列表（从 HealthChecker 获取）
// POST /v1/admin/degradation/reset → 手动重置降级状态（运维用）
```

**4. Feature Flag 渐进式发布（可选扩展）：**

```go
type FeatureFlag struct {
    Name      string    `json:"name"`
    Enabled   bool      `json:"enabled"`
    Owner     string    `json:"owner"`
    CreatedAt time.Time `json:"created_at"`
}
```

用于：新索引器版本逐步灰度、新提取器渐进启用、实验性 search 算法 A/B 测试。

### 边界情况

| 场景 | 处理 |
|------|------|
| 组件短暂不可用（瞬时故障） | 不立即标记为 degraded——使用 `failureCount + sliding window`（如连续 3 次失败，10s 窗口） |
| 组件恢复 | 自动恢复为 healthy——`RecoveredAt` 记录恢复时间 |
| `/healthz` 被频繁调用 | 组件健康检查结果缓存（5-10s TTL），避免 health check 自身造成负载 |
| 健康检查中的超时 | 每个组件检查有独立的超时（如 2s），超时视为 degraded |
| 安全：健康端点不应暴露内部细节 | `/healthz` 和 `/readyz` 公开；`/v1/admin/health` 需要 admin scope |

---

## 方向五：在线对象迁移与存储重平衡

### 产品价值

| 运营场景 | 当前行为 | 期望行为 |
|---------|---------|---------|
| **更换存储后端** | 从 local FS 迁移到 S3：停机 → 全量导出 → 配置变更 → 全量导入 → 验证 → 恢复 | `POST /v1/admin/migration --from local --to s3 --bucket data` 在线迁移 |
| **存储容量扩展** | 添加新存储节点后，旧节点上的对象无法自动重平衡 | 重平衡 job 自动将对象均匀分布到所有节点 |
| **存储节点退役** | 节点下线前需要手动逐对象复制 + 删除 | `POST /v1/admin/rebalance --drain-storage-node old-node` |
| **Storage Class 映射变更** | "所有 STANDARD 对象都存储在 S3 Standard，现在想换到另一个 S3 bucket" | 生命周期规则 `Transition` + 对象迁移执行 |
| **地域搬迁** | 从 S3 US 迁移到 S3 EU：需要跨区域复制 + 验证 + 切换 | `POST /v1/admin/migration --from s3://us-bucket --to s3://eu-bucket` |

### 现状

**Storage 接口无跨后端操作能力：**

```go
// internal/storage/storage.go
type Storage interface {
    Put(ctx, key, r, size, opts) (ObjectInfo, error)
    Get(ctx, key) (io.ReadCloser, ObjectInfo, error)
    Stat(ctx, key) (ObjectInfo, error)
    Delete(ctx, key) error
    List(ctx, prefix, marker, limit) (ListResult, error)
    PresignGet(ctx, key, expiry) (string, error)
    PresignPut(ctx, key, expiry) (string, error)
    // ❌ 无 Move(ctx, srcKey, dstKey) error
    // ❌ 无 CopyTo(ctx, dst Storage, key) error
    // ❌ 无 BackendName() string
}
```

**对象元数据无后端标识：**

```go
// internal/repository/repository.go
type Object struct {
    // ...
    StorageKey  string         // 隐式绑定到单个后端
    StorageClass string        // 归类标签，不影响存储位置
    // ❌ 无 StorageBackend string  — 指示对象存储在哪个后端
    // ❌ 无 StorageEndpoint string — 指示对象存储的具体端点
    // ❌ 无 StorageBucket  string  — 指示对象存储的远程 bucket
}
```

**单后端实例化：**

```go
// cmd/server/main.go:buildStorageFrom
// 只创建单个 Storage 实例
store, err := storage.NewStorageFrom(ctx, &cfg.Storage, cfg.Storage.DefaultClass)
// 无多后端映射表
// 无 StorageClass → Storage 映射
```

**目前唯一的"迁移"路径：**

- **复制 worker** (`internal/replication/replication.go`)：事件驱动，仅处理 `EventCreated`
- **snapshot** (`internal/snapshot/snapshot.go`)：仅 SQLite+local FS，停机时间窗口
- **应用层 copy**：用户先 `GET` 对象再 `PUT` 到新位置—双重网络 I/O，无法服务端操作

### 架构权衡与建议方案

**1. Storage 接口扩展（基础）：**

```go
// 新增方法
type Storage interface {
    // ... 现有方法 ...

    // CopyTo copies an object from this storage to another storage backend
    // without routing data through the caller. Returns the new ObjectInfo.
    // When the destination supports server-side copy, this is a metadata
    // operation; otherwise it falls back to streaming.
    CopyTo(ctx context.Context, dst Storage, key string, opts PutOptions) (ObjectInfo, error)

    // Move moves an object from this storage to another. Equivalent to
    // CopyTo + Delete, but may be atomic on backends that support it.
    MoveTo(ctx context.Context, dst Storage, key string, opts PutOptions) (ObjectInfo, error)

    // SupportsServerSideCopy returns true when this backend can copy
    // objects natively (e.g. S3 CopyObject).
    SupportsServerSideCopy() bool
}
```

**S3 实现优势：** S3 的 `CopyObject` 是服务端操作——数据不经过 aero-vault 服务器。对于同 region 的 S3 bucket 间迁移，这是 O(1) 元数据操作，不消耗本地带宽。

**Local 实现：** 通过 `os.Rename`（同文件系统）或 `io.Copy`（跨文件系统）。

**2. 多后端映射表（架构核心）：**

```go
// internal/storage/registry.go — 新文件
type StorageRegistry struct {
    backends map[string]Storage  // 名称 → 后端实例
    classes  map[string]string   // StorageClass → 后端名称
}

func NewStorageRegistry() *StorageRegistry { ... }
func (r *StorageRegistry) Register(name string, backend Storage) { ... }
func (r *StorageRegistry) MapClass(class string, backendName string) { ... }
func (r *StorageRegistry) GetForClass(class string) Storage { ... }
func (r *StorageRegistry) Get(name string) Storage { ... }
func (r *StorageRegistry) List() map[string]string { ... }
```

**配置示例：**

```yaml
storage:
  backends:
    hot:
      backend: "local"
      root: "/data/ssd"
    warm:
      backend: "s3"
      endpoint: "https://s3.us-east-1.amazonaws.com"
      bucket: "aero-warm"
    cold:
      backend: "s3"
      endpoint: "https://s3.us-east-1.amazonaws.com"
      bucket: "aero-cold-glacier"
      storage_class: "GLACIER"
  
  # StorageClass → 后端映射
  class_mapping:
    STANDARD: "hot"
    STANDARD_IA: "warm"
    GLACIER: "cold"
```

**3. 迁移 Job（执行引擎）：**

```go
// internal/reconcile/migration.go — 新文件

const JobTypeMigrateObject = "migrate_object"

type MigrationPlan struct {
    SourceBackend string
    TargetBackend string
    Bucket       string
    KeyPrefix    string      // 可选：只迁移匹配前缀的对象
    StorageClass string      // 可选：只迁移特定 StorageClass 的对象
    DryRun       bool        // 仅计算影响，不执行
}

// POST /v1/admin/migration — 创建迁移计划
// POST /v1/admin/migration/{id}/execute — 执行迁移
// GET /v1/admin/migration/{id}/status — 查看进度
```

**迁移流程：**

```
1. 创建迁移计划（统计影响的对象数、总大小）
2. 确认执行 → 为每个对象创建 MigrateObject job
3. Job 执行：
   a. 从源后端读取 StorageKey
   b. 如果目标后端支持服务端复制 → 调用 CopyTo（零网络 I/O）
   c. 否则 → stream 读取→写入
   d. 验证 checksum（ETag 比较）
   e. 更新对象元数据的 StorageKey + StorageBackend
   f. 从源后端删除（可选，可配置保留策略）
4. 迁移完成后通知（事件总线 + webhook）
```

**4. 迁移粒度与一致性：**

| 粒度 | 适用场景 | 影响 |
|------|---------|------|
| **单对象** | 手动触发、hotfix | 立即生效，单个对象 |
| **前缀匹配** | 目录级迁移（`logs/archive/` → GLACIER） | 局部迁移 |
| **桶级** | 整桶迁移（`data` → S3 US → S3 EU） | 大规模迁移 |
| **StorageClass 级** | 所有 `STANDARD_IA` → 新节点 | 按存储类迁移 |
| **全量** | 节点退役、灾备切换 | 全量迁移 |

### 边界情况

| 场景 | 处理 |
|------|------|
| 迁移过程中对象被更新 | 迁移 job 处理前检查对象 `updated_at`——若迁移期间有新的 `Put`，跳过旧版本，新版本独立迁移或标记冲突 |
| 迁移失败（源后端故障） | 重试 N 次后标记 `migration_failed`；保留源 blob；提供重试 API |
| 对象正在被读取 | 迁移期间不阻塞 GET——旧后端持续服务直到迁移确认 |
| 迁移中删除 | 迁移 job 完成后删除旧 blob——通过 `DeleteAfterMigration` 配置（默认保留 7 天作为回滚窗口） |
| 服务端复制不可用 | 降级为流式复制（`Get` → `Put`） |
| 并发迁移 | 使用 Job dedupe key 防止同一对象被多次迁移；迁移计划提供 `max_concurrent` 限速 |
| 迁移已完成后的读一致性 | 更新元数据后，后续 GET 走新后端；正在进行的 GET 继续使用旧后端（通过 `StorageKey` 快照） |

---

## 总结与优先级建议

| # | 方向 | 预估工作量 | 影响范围 | 短期收益 | 长期战略价值 | 建议顺序 |
|---|------|-----------|---------|---------|-------------|---------|
| **1** | **声明式配置与 IaC 验证** | M (2-3 周) | 运维/工程基础设施 | 立即提升配置可管理性、减少部署错误 | IaC 基础、合规审计、多环境管理 | **① 最高投资回报** |
| **2** | **多租户资源治理与公平调度** | L (3-5 周) | 生产可靠性/SaaS | 消除 noisy neighbor、提升多租户稳定性 | 企业 SaaS 必备能力 | **②** |
| **3** | **存储用量分析与成本归因** | M (2-4 周) | 运维/成本 | 存储成本可观测、冷数据可识别 | 运营成熟度核心能力 | **③** |
| **4** | **优雅降级与故障透明化** | M (2-3 周) | 可靠性/运维 | 故障快速定位、降级可控 | 生产 SLA 基础 | **④ 与方向 2 协同** |
| **5** | **在线对象迁移与重平衡** | L (4-8 周) | 架构/运维 | 后端切换零停机 | 企业级存储平台必备 | **⑤ 依赖方向 1 的配置框架** |

### 依赖关系

```
方向 1（配置框架）→ 方向 5（迁移配置需要灵活的存储后端定义）
                        ↓
方向 2（资源治理）+ 方向 4（健康降级）→ 协同：熔断器事件可触发健康状态变更
                        ↓
              方向 3（分析面板）→ 可独立推进，不依赖其他方向
```

**建议分批：**
- **首批（2 周）：** 方向 1（YAML 配置 + `validate` 子命令 + schema 导出）+ 方向 3（`daily_analytics` 表 + `/v1/analytics/growth` 端点）
- **次批（2 周）：** 方向 2（跨协议 `ResourceGovernor` + bucket 级配额）+ 方向 4（`HealthChecker` + 组件注册 + 降级 header）
- **末批（4 周）：** 方向 5（`StorageRegistry` + 迁移 job + `MoveTo/CopyTo` 接口 + 管理 API）
