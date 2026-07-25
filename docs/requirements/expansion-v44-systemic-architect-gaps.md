# AeroVault 高价值扩展方向 v44 — 系统性架构与工程质量缺口

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 `.go` 文件 + SDK × 3 + `deploy/*` + 48 对迁移文件 + `Makefile` + `docs/*`）
>
> **分析视角：** 资深架构师 / 产品经理 — 聚焦此前 **43 期 expansion 分析（累计 200+ 方向，~400,000+ 字分析文本）+ `docs/ROADMAP.md`（10 大方向）+ `docs/CHANGELOG.md` + `docs/TODO.md`** 中从未实质性触及的系统性架构缺口
>
> **分析日期：** 2026-07-10
>
> **去重验证：** 对 `docs/requirements/` 下全部 44 份既有分析文档 + `ROADMAP.md` + `CHANGELOG.md` + `TODO.md` + `adr/DECISIONS.md` + `extensions*.md` 进行关键术语穷尽式 `grep` 验证。每个方向在既有文档中 **零实质性独立架构分析**（表格中的一行过路引用、举例提及、或单一子点提及均不构成实质性分析）。

---

## 前言

此前 43 期 expansion 分析累计覆盖了 200+ 方向，横跨 AI/RAG 管线、S3 协议实现纵深、存储后端、认证授权、多租户、合规、可观测性、工程基础设施、社区治理等几乎所有维度。最新两期（v42 聚焦 S3 执行层断层 5 方向，v43 聚焦安全策略/SSO/通知/SLA/遥测 5 方向）覆盖了大量此前遗漏的执行层缺口。

然而，经过对代码库的最后一遍穷举扫描，以下 **5 个系统性缺口** 依然未被任何一期分析触达。它们的共同特征：**不涉及"新功能"的添加，而是已有能力之间的"连接层"缺失**——AeroVault 的功能矩阵已经非常完整，但功能之间的协同、可运维性保障、格式演进策略、跨协议一致性等方面存在架构层面的断裂。

```
功能维度（前 43 期）：    ❌ 不支持 → ✅ 已实现
执行层维度（v42）：       ✅ 有 CRUD → ✅ 运行时行为完整
系统性维度（本期 v44）：   ✅ 各功能独立完整 → ⚠️ 功能间连接/格式演进/运维保障缺失
```

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心矛盾 | 锚定代码 | 43 期覆盖 |
|---|------|------|--------|---------|---------|-----------|
| 1 | **配置架构：Schema、校验与运行时审计** | 运维/可靠性 | **P1** — 80+ 环境变量零 schema 校验；配置笔误静默生效，无变更审计跟踪 | `internal/config/config.go`（无 `Validate()`，无 `ConfigDump()`，无变更审计） | ❌ **零覆盖** |
| 2 | **跨协议请求幂等性与一致性保障** | 可靠性/数据完整性 | **P1** — Idempotency-Key 仅在 REST /v1 工作；S3/WebDAV/MCP 间无请求去重，用户混合协议时可能创建重复对象 | `internal/api/rest/idempotency.go`（仅 REST）；`internal/api/s3compat/handler.go`（无 Idempotency）；`internal/api/webdav/dav.go`（无 Idempotency）；`internal/mcp/server.go`（无 Idempotency） | ❌ **零覆盖** |
| 3 | **生产架构的备份/灾备一体化方案** | 可靠性/灾备 | **P2** — Snapshot 仅支持 SQLite；Postgres+S3 推荐生产配置无统一备份工具，文档建议退化为外部工具拼凑 | `internal/snapshot/snapshot.go`（仅 SQLite）；`docs/deployment.md`（未提供 Postgres+S3 的 DR 方案） | ⚠️ v8/v20 方向表各 1 行浅层草图，**无实质性架构分析** |
| 4 | **存储加密信封格式版本化与演进策略** | 安全/工程持续 | **P2** — `sseEnvelope` 无版本字段，`Alg` 硬编码 "AES-256-GCM"；格式变更时无迁移路径，新旧版本 JSON 互操作无保障 | `internal/storage/encrypt.go:24-33`（`sseEnvelope` 无 `Version` 字段）；`const sseAlg = "AES-256-GCM"`（硬编码）；`parseEnvelope()`（无格式版本检查） | ⚠️ v24「SSE envelope 轮换」方向提及 envelope 但聚焦 key rotation，**非信封格式版本化** |
| 5 | **事件流交付保障：SSE Replay 完备性与 Webhook 死信审计** | 可靠性/运维 | **P2** — SSE `replayMissed` 仅回放 200 条事件且无分页；Webhook 死信记录与成功记录混淆（Dead-letter 标记为 Succeeded），运维人员无法区分 | `internal/api/rest/sse.go:28-43`（`replayMissed` 限 200 条无分页）；`internal/events/webhook.go:218-224`（死信复用 `MarkWebhookSucceeded`）；`internal/repository/webhook_failures.go`（无死信状态字段） | ⚠️ v39 方向一覆盖 SSE 订阅泄漏（channel 泄漏）但 **非 replay 完备性与死信审计** |

---

## 方向一：配置架构：Schema、校验与运行时审计

### 现状

当前配置系统（`internal/config/config.go`）通过 `getEnv()` / `getEnvInt()` / `getEnvBool()` 从环境变量加载配置，存在以下问题：

```go
// internal/config/config.go — 典型模式
cfg := &Config{
    Storage: StorageConfig{
        Backend: strings.ToLower(getEnv("STORAGE_BACKEND", "local")),
        CBFailureThreshold: getEnvInt("STORAGE_CB_FAILURE_THRESHOLD", 0),
        // ...
    },
    AI: AIConfig{
        EmbedProvider: getEnv("AI_EMBED_PROVIDER", ""),
        ChatProvider:  getEnv("AI_CHAT_PROVIDER", ""),
        // ...
    },
}
```

**问题 1：无结构化 Schema**

| 能力 | 当前状态 | 行业最佳实践 |
|------|---------|-------------|
| 配置字段类型声明 | ❌ 隐式（从 Go 类型推导） | ✅ JSON Schema / 结构化文档 |
| 取值范围约束 | ❌ 无（负值 RPS、空字符串 Provider、无效 Backend 均静默接受） | ✅ `Minimum: 1, Maximum: 100000` |
| 字段间依赖关系 | ❌ 无（`AI_VECTOR_BACKEND=pgvector` 但 `AI_VECTOR_DSN` 未设置 → 静默降级为内存搜索） | ✅ `requiredIf` |
| 必填字段校验 | ❌ 无（`STORAGE_S3_BUCKET` 在 `STORAGE_BACKEND=s3` 时未设 → 启动后才知道） | ✅ `required` |

**问题 2：无 `Validate()` 方法**

```go
// 当前：启动时直接使用配置，无校验步骤
cfg, err := config.Load()     // Load 不返回校验错误
// ...

// 期望：显式校验
if err := cfg.Validate(); err != nil {
    return fmt.Errorf("config validation: %w", err)
}
```

典型的可检测的错误类型：

| 错误配置 | 当前行为 | 应该行为 |
|---------|---------|---------|
| `STORAGE_BACKEND=s2`（拼写错误） | factory 返回 `"unsupported backend: s2"`；启动失败 | `Validate()` 时提前检查合法值枚举 |
| `RATE_LIMIT_RPS=-1`（负数） | 作为 `0` 使用（限流关闭），日志无提示 | `Validate()` 拒绝负值 |
| `AI_EMBED_PROVIDER=openai` 但 API key 未设 | embedder 构建时静默返回 `nil` → 无 Embedding → 所有搜索退化 | 启动时验证 Provider + Key 组合 |
| `AI_VECTOR_BACKEND=pgvector` 但 `AI_VECTOR_DSN` 为空 | emit warn log → 回退内存暴力搜索 | `Validate()` 要求 `AI_VECTOR_DSN` 当后端为 pgvector |
| `DB_DRIVER=postgres` 但 `DB_DSN` 为空 | `sql.Open("pgx", "")` → 启动失败，但错误信息模糊 | `Validate()` 检查必填字段 |

**问题 3：无运行时配置审计**

| 能力 | 当前状态 | 需要 |
|------|---------|------|
| 生效配置导出 | ❌ 无 `GET /v1/admin/config` 端点 | 运维人员可以验证当前运行配置 |
| 配置变更审计 | ❌ 无记录 | 环境变量变更无审计跟踪 |
| 配置与默认值对比 | ❌ 无 | 区分"显式设置"与"使用默认值" |
| 敏感配置掩码 | ❌ Token/Key 明文记录 | 导出时对 `*_KEY` `*_SECRET` `*_TOKEN` 掩码 |

**具体的代码证据：**

```go
// internal/config/config.go — Config 结构体定义
type Config struct {
    App         AppConfig
    Storage     StorageConfig
    DB          DBConfig
    // ... 10+ 子结构体，共 80+ 字段
}
// 没有 Validate() 方法
// 没有 MarshalJSON() / Dump() 方法
// 没有 sensitive bool tag 标记敏感字段
```

```go
// internal/config/config_storage.go — 各后端配置互相独立
type S3Config struct {
    Bucket string // 需要 Validate(): STORAGE_BACKEND=s3 时必填
    Region string // 需要 Validate(): STORAGE_BACKEND=s3 时必填
    // ...
}
// 但 Validate() 不存在
```

### 缺失的能力

1. **`Config.Validate()` 方法**：对每个字段做类型/范围/依赖校验，返回 `[]ValidationError`（聚合所有错误而非第一个）：

   ```go
   func (c *Config) Validate() []ValidationError {
       var errs []ValidationError
       // 枚举值校验
       if !validBackends[c.Storage.Backend] {
           errs = append(errs, ValidationError{"STORAGE_BACKEND", "must be one of: local, s3, oss, cos"})
       }
       // 依赖校验
       if c.Storage.Backend == "s3" && c.Storage.S3.Bucket == "" {
           errs = append(errs, ValidationError{"STORAGE_S3_BUCKET", "required when STORAGE_BACKEND=s3"})
       }
       // 范围校验
       if c.RateLimit.RPS < 0 {
           errs = append(errs, ValidationError{"RATE_LIMIT_RPS", "must be >= 0"})
       }
       // AI 依赖校验
       if c.AI.VectorBackend == "pgvector" && c.AI.VectorDSN == "" {
           errs = append(errs, ValidationError{"AI_VECTOR_DSN", "required when AI_VECTOR_BACKEND=pgvector"})
       }
       return errs
   }
   ```

2. **配置导出端点**：`GET /v1/admin/config` 返回当前所有配置（敏感字段掩码为 `****`）：

   ```json
   {
     "app": {"addr": ":8080", "log_level": "info"},
     "storage": {"backend": "s3", "s3_bucket": "my-bucket", "s3_secret_key": "****"},
     "ai": {"embed_provider": "openai", "embed_api_key": "****"}
   }
   ```

3. **配置变更审计**：admin 端点的`POST /v1/admin/config/reload`（如果将来支持热加载）需记录到 `audit_log`。环境变量启动值在 `GET /v1/admin/config` 返回的 `started_at` 快照中体现。

4. **配置值来源追踪**：每个配置项记录其来源（`default` / `env` / `config_file`），方便调试。

### 为什么需要

1. **配置错误是生产事故的首要原因。** 在所有运维问题中，配置错误（typo、错误值、遗漏必填项）占比超过 30%。当前系统在配置错误时要么静默降级（无 Embedder）、要么在启动后很久才报错（S3 后端初始化失败），缺乏预防性校验。

2. **运维人员需要确认"当前服务在用哪个配置"。** 没有 `GET /admin/config`，运维人员只能通过 SSH 登录服务器查看环境变量——这在容器化/K8s 环境中极不方便，且无法区分"显式设置" vs "默认值"。

3. **80+ 配置项已到需要 Schema 管理的规模。** 随着配置项增长（已超 80），无 Schema 的配置系统成为维护负担——新开发者无法从代码直观了解"有哪些配置可选、合法值是什么、哪些互斥"。

### 架构概要

```
当前:
  Load() → *Config     // 无校验，无导出，无审计

改进:
  Load() → *Config
  cfg.Validate()       // 新增：聚合校验，启动时调用
  GET /admin/config    // 新增：运行时配置导出（敏感掩码）
  audit_log            // 配置变更（热加载时）记录审计
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **Validate 返回多个错误** | 聚合返回所有错误，而非第一个（让用户一次修复所有问题） |
| **敏感字段掩码** | 基于字段命名约定（`*_KEY`、`*_SECRET`、`*_TOKEN`），导出 `GET /admin/config` 时自动掩码 |
| **空字符串 vs 未设置** | 区分 `env` 来源和 `default` 来源，方便调试 |
| **热加载** | 当前无热加载需求，保留为未来扩展点 |

---

## 方向二：跨协议请求幂等性与一致性保障

### 现状

当前幂等性实现（Idempotency-Key, `internal/api/rest/idempotency.go`）是 Stripe 风格、覆盖 REST /v1 协议的 PUT/POST/DELETE。但 S3、WebDAV、MCP 协议完全不在保护范围内：

```go
// internal/api/rest/idempotency.go — 仅 REST /v1 使用
func idempotency(repo repository.Repository, logger *slog.Logger, hashBody bool) func(http.Handler) http.Handler {
    // 通过 chi 中间件注册，仅作用域为 REST 路由
}

// internal/api/s3compat/handler.go — S3 handler 无 Idempotency-Key 处理
// 即使客户端发送 Idempotency-Key header，在 S3 入口处也会丢失
```

**跨协议的幂等性鸿沟：**

| 操作 | REST /v1 | S3 Compat | WebDAV | MCP |
|------|---------|-----------|--------|-----|
| PUT 对象 | ✅ Idempotency-Key 保护 | ❌ 无幂等 | ❌ 无幂等 | ❌ 无幂等 |
| DELETE 对象 | ✅ Idempotency-Key 保护 | ❌ 无幂等 | ❌ 无幂等 | ❌ 无幂等 |
| Multipart Upload | ✅ Idempotency-Key 保护（Complete） | ❌ 无幂等 | N/A | N/A |
| 条件请求（If-Match/N-Match） | ❌ 无 Idempotency 保护 | ⚠️ 条件请求自身提供弱保护 | ❌ 无 | ❌ 无 |

**具体的代码证据：**

```go
// internal/api/rest/router.go — Idempotency middleware 注册
r.With(mw.Auth, mw.Tenant, idempotency(h.repo, logger, cfg.App.IdempotencyHashBody)).
    // ... REST 写操作路由
```

```go
// internal/api/s3compat/router.go — S3 路由
func Router(h *Handler, logger *slog.Logger) http.Handler {
    r := chi.NewRouter()
    // ... 无 Idempotency middleware
    r.Put("/{bucket}/{key}", h.handlePutObject)
    r.Delete("/{bucket}/{key}", h.handleDeleteObject)
    // ...
}
```

**这意味着：**

1. **用户混合使用 REST 和 S3 SDK 时可能重复创建对象。** 例如 CI 脚本使用 `aws s3 cp`（S3 协议）上传文件，网络错误后重试 → 同一文件产生两个版本。而使用 `curl -X PUT /v1/files/key -H 'Idempotency-Key: xxx'`（REST 协议）则安全。

2. **WebDAV 挂载后拖拽上传无法去重。** 用户通过 macOS Finder 挂载 WebDAV 后拖拽上传大文件，连接中断后 Finder 自动重试 → 文件重复。

3. **多协议共享同一个 FileService，但幂等性检查在协议层（middleware）而非业务层（service）实现。** 同样的 `svc.Put(ctx, ...)` 调用，从 REST 进入时有幂等保护，从 S3 进入时无保护。

### 为什么需要

1. **多协议是 AeroVault 的核心差异化，但也是数据重复的温床。** 用户可能同时使用 aws-cli（S3）、curl（REST）、文件管理器（WebDAV）、Claude Desktop（MCP）操作同一批对象。任意一个协议的重试机制都可能导致重复。

2. **网络重试不可靠性随着协议数量增加。** 在 Kubernetes 环境中，Ingress/Nginx/Service Mesh 层可能自动重试 5xx 响应。如果所有协议都提供幂等性，网络重试+协议重试形成乘法效应（3 层 × 4 协议 = 最多 12 次重复写入）。

3. **S3 生态的工具普遍期望幂等性。** 很多 S3 客户端工具（aws-cli、rclone、s3cmd）的重试机制假定 PUT 对象是幂等的（AWS S3 的 PUT 确实是幂等的）。当前 S3 handler 不满足这个期望。

4. **实现成本低。** Idempotency-Key 基础设施（持久化表 + TTL GC + body hash）已经完整实现。只需将 middleware 从 REST 路由组推广到所有协议入口，并在 service 层提供幂等性检查钩子。

### 缺失的能力

1. **协议无关的幂等性中间件**：将 Idempotency-Key 的处理从 REST-specific middleware 提升为跨协议的通用中间件：

   ```go
   // 新的跨协议 idempotency 中间件
   func Idempotency(repo repository.Repository, logger *slog.Logger, hashBody bool) func(http.Handler) http.Handler {
       // 读取 Idempotency-Key header（所有协议共享）
       // 支持 X-Idempotency-Key / Idempotency-Key
       // 跨协议共享同一键空间：(tenant, idempotency_key)
   }
   ```

2. **S3 handler 集成 Idempotency**：在 S3 路由组注册幂等中间件，处理 PUT/DELETE/POST（CompleteMultipartUpload）请求。

3. **WebDAV handler 集成跨请求幂等**：WebDAV 的 PUT（上传文件）场景最危险——连接中断后客户端自动重试可能创建重复。基于 `(tenant, key, content-length, content-md5)` 做轻量级去重。

4. **MCP `write_file` 工具幂等**：MCP 工具的 `write_file` 目前是"写时即写"，如果 AI Agent 在工具调用超时后重试，同一文件可能被写入两次。

5. **Service 层幂等钩子**：如果协议层中间件不可行（如 MCP 通过 JSON-RPC 调用），在 `FileService.Put()` 中提供可选的幂等检查：

   ```go
   func (s *FileService) Put(ctx context.Context, tenant, bucket, key string, ...) (*repository.Object, error) {
       // 可选的幂等检查（当 idempotencyKey 非空时）
       if idempotencyKey != "" {
           // 存在已完成 → 返回原结果
           // 存在进行中 → 409
       }
       // ...
   }
   ```

### 架构概要与边界情况

```
当前:
  REST 请求 → Idempotency middleware → service.Put()
  S3 请求  → (无)                    → service.Put()
  WebDAV   → (无)                    → service.Put()

改进:
  REST 请求 → Idempotency middleware → service.Put()
  S3 请求  → Idempotency middleware → service.Put()
  WebDAV   → Idempotency middleware → service.Put()
  MCP      → service.Put(idempotencyKey="auto-<hash>")  ← 服务层钩子
```

| 场景 | 处理方式 |
|------|---------|
| **REST 与 S3 共享同一 Idempotency-Key** | 键空间按 `(tenant, key)` 隔离，S3 的 `Idempotency-Key` header 与 REST 使用同一存储表，跨协议共享幂等保证 |
| **WebDAV 无 Idempotency-Key header** | 自动基于 `(tenant, bucket, key, Content-MD5)` 生成隐式幂等键 |
| **MCP 重试** | 自动基于 `(tenant, bucket, key, content_hash)` 生成隐式键 |
| **S3 Conditional Request + Idempotency** | 条件请求在幂等之后处理——先检查重复，再检查条件 |

---

## 方向三：生产架构的备份/灾备一体化方案

### 现状

当前备份工具 `internal/snapshot/snapshot.go` 仅支持 SQLite + local FS。对于推荐的 Postgres + S3 生产配置，项目文档只能建议退化为外部工具：

```go
// internal/snapshot/snapshot.go:29
return errors.New("snapshot: cannot derive sqlite file from DSN; only sqlite local snapshots are supported")
```

```go
// internal/snapshot/snapshot.go — 包注释
// Package snapshot packs the database + object storage into a single tar.gz
// for backup/restore. It is intended for SQLite + local-FS development
// instances and small production deployments. For large Postgres+S3 stacks,
// fall back to pg_dump + s3 lifecycle copies.
```

**备份覆盖矩阵：**

| 部署模式 | DB 备份 | 对象存储备份 | 一致性保障 | 跨区域灾备 |
|---------|---------|------------|-----------|-----------|
| SQLite + local FS | ✅ tar.gz 打包 | ✅ tar.gz 打包 | ⚠️ 无快照一致性（30s 打包窗口内可能有写入） | ❌ 无 |
| SQLite + S3 | ❌ 不支持 | ❌ 不支持 | ❌ | ❌ |
| Postgres + local FS | ❌ 无工具 | ❌ 无工具 | ❌ | ❌ |
| Postgres + S3 | ❌ 建议 pg_dump | ❌ 建议 s3 lifecycle | ❌ 无协调 | ❌ 无 |

**Postgres 连接池配置（`internal/repository/postgres.go`）：**

```go
// repository/postgres.go:17-18
func openPostgres(dsn string) (*sql.DB, error) {
    db, err := sql.Open("pgx", dsn)
    // 未调用:
    //   db.SetMaxOpenConns(...)
    //   db.SetMaxIdleConns(...)
    //   db.SetConnMaxLifetime(...)
    //   db.SetConnMaxIdleTime(...)
    return db, nil
}
```

### 为什么需要

1. **Postgres+S3 是官方推荐的"生产配置"但没有备份工具。** 这意味着投入生产的用户需要自行拼凑 pg_dump（元数据）和 aws s3 sync（对象）——这两个工具无法保证一致的时间点（pg_dump 过程中 S3 可能有新写入）。

2. **元数据数据库是单点故障。** 对象 blob 可能由多个存储后端（本地 + S3 + 远程 Replication）持久化，但 Repository 元数据只有一个。失去 DB = 失去对象索引、租户配置、版本历史、AI 索引——即使对象数据完好。

3. **缺乏灾备架构意味着无法满足合规 RPO/RTO 要求。** 金融/医疗等受监管行业要求明确的恢复点目标（RPO）和恢复时间目标（RTO）。当前无工具可满足。

4. **Postgres 连接池未配置最佳参数。** `MaxOpenConns=0`（无限）在流量突发时可能导致 `too many clients` Postgres 错误。`MaxIdleConns=2`（默认）可能导致频繁建连。

### 缺失的能力

1. **`internal/backup/` 包**——统一备份框架，支持多种后端组合：

   ```go
   type BackupOptions struct {
       DSN         string // 数据库 DSN（SQLite 或 Postgres）
       StorageType string // local, s3
       Target      string // 备份目标 URL
       Mode        string // full | incremental
   }
   
   func Create(opts BackupOptions) error {
       // Phase 1: DB 快照
       //   - SQLite: 现有 tar.gz 逻辑
       //   - Postgres: pg_dump 或 pgBackRest
       // Phase 2: 元数据一致性遍历
       //   - 遍历所有对象，生成 backup_manifest.json
       // Phase 3: 对象备份
       //   - 本地: tar.gz 打包
       //   - S3: 通过 CopyObject 或 Batch Operations
       // Phase 4: 一致性校验
       //   - 对比 manifest 中的 ETag 与存储对象的哈希
   }
   ```

2. **Postgres 连接池配置：**

   ```go
   func openPostgres(dsn string) (*sql.DB, error) {
       db, err := sql.Open("pgx", dsn)
       if err != nil {
           return nil, err
       }
       db.SetMaxOpenConns(25)         // 根据内核文件描述符限制
       db.SetMaxIdleConns(10)
       db.SetConnMaxLifetime(30 * time.Minute)
       db.SetConnMaxIdleTime(5 * time.Minute)
       return db, nil
   }
   ```

3. **跨区域元数据灾备架构：**

   - Read replica 感知：`DB_READ_DSN` 配置一个或多个只读副本 DSN
   - Primary 故障检测 + 自动提升（failover awareness）
   - WAL 归档配置检查：启动时检查 `archive_mode` 和 `archive_command` 是否已设置

4. **时间点恢复 API：** `POST /v1/admin/restore?timestamp=...` 对于支持 WAL 的 Postgres 部署，允许恢复到任意时间点。

### 架构概要

```
当前:
  internal/snapshot/snapshot.go — 仅 SQLite + local FS

改进:
  internal/backup/
    ├── backup.go        — BackupOptions, Create/Restore
    ├── db_sqlite.go     — SQLite snapshot driver (迁移现有逻辑)
    ├── db_postgres.go   — pg_dump WAL-aware driver
    ├── storage_local.go — local FS archive driver
    ├── storage_s3.go    — S3 batch operations driver
    └── manifest.go      — backup_manifest.json schema + validation

  CLI:
    aero-vault backup create <target> [--db <dsn>] [--storage <type>]
    aero-vault backup restore <source> [--db <dsn>] [--storage <type>]

  Reconcile job（可选）:
    BackupJob — 定时备份作业，支持 backup_interval_minutes 配置
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **备份过程中有新写入** | DB 快照（pg_dump --data-only）先获取一致性时间点 → 基于该时间点遍历对象 | 
| **跨区域灾备切换** | 切换 DNS 到备 region + 启动备 DB + 验证备用存储可达性 → 手动或半自动 |
| **备份目标不可达** | 非致命错误：记录告警 + 重试（复用 job 重试机制） |
| **增量备份** | 先实现全量备份，增量备份作为未来增强（基于 WAL 归档） |
| **Postgres 连接池耗尽** | `MaxOpenConns` + 健康检查 + 备用连接池 |

---

## 方向四：存储加密信封格式版本化与演进策略

### 现状

当前 SSE（服务端加密）信封格式定义如下：

```go
// internal/storage/encrypt.go:24-33
const sseAlg = "AES-256-GCM"

type sseEnvelope struct {
    Alg  string `json:"alg"`
    Wrap string `json:"wrap,omitempty"` // "" = local; "kms" = remote
    Kid  string `json:"kid,omitempty"`
    Kek  string `json:"kek"` // base64, wrapped data key
    IV   string `json:"iv"`  // base64, 96-bit object nonce
}
```

**信封格式没有任何版本字段。**

```go
// internal/storage/encrypt.go — 解析信封
func parseEnvelope(s string) (*sseEnvelope, error) {
    var env sseEnvelope
    if err := json.Unmarshal([]byte(s), &env); err != nil {
        return nil, fmt.Errorf("sse: parse envelope: %w", err)
    }
    if env.Alg != sseAlg {
        return nil, fmt.Errorf("sse: unsupported envelope alg %q", env.Alg)
    }
    // 无版本检查
    return &env, nil
}
```

**潜在风险：**

| 场景 | 问题 |
|------|------|
| **新增 `compression` 字段** | 旧代码解析 JSON 时忽略未知字段（`json.Unmarshal` 默认行为），但如果新代码写入了压缩后的数据，旧代码仍能解析信封（Alg 不变）但无法正确解密（数据体被压缩了但旧代码未解压） |
| **切换到 AES-256-GCM-SIV** | `Alg` 字段可以承载新算法名。但 `sseAlg` 常量是硬编码的——旧代码读到 `Alg=AES-256-GCM-SIV` 时会直接拒绝。但运行时无法热切换，因为旧对象仍使用旧算法 |
| **数据 key 派生方式变更** | 当前是 `rand.Read → 32 bytes data key`。如果将来需要派生 key（比如基于对象路径+主 key 用 HKDF），信封格式需要标识派生方式 |
| **跨主版本兼容** | 如果信封重构（如使用 Protocol Buffers 替代 JSON，或添加额外认证数据 AAD），没有任何版本号可供旧代码优雅降级或报出清晰的升级提示 |

**当前信封格式的演进能力矩阵：**

| 维度 | 能力 | 风险等级 |
|------|------|---------|
| 算法变更 | `Alg` 字段可承载新值（但旧代码拒绝 $未知值） | 🟡 中——可扩展但需协调升级 |
| 字段新增 | JSON 天然向前兼容（`json.Unmarshal` 忽略未知字段） | ✅ 低——向后兼容 |
| 字段含义变更 | 无版本号 → 无法表达"同一个 Alg 的新实现" | 🔴 高——无法区分 |
| 数据格式变更 | 无版本号 → 新旧代码对同一信封的理解不一致 | 🔴 高——可能导致解密失败 |
| 跨主版本兼容 | 无版本号 → 无法声明兼容性策略 | 🔴 高——升级风险不可控 |

### 为什么需要

1. **加密数据无法回滚。** 如果加密信封格式升级后发现有 bug，降级意味着所有在升级期间写入的对象都无法被旧版本解密。"版本号"是最低成本的保险机制。

2. **当前格式的演进路径为零。** 虽然 JSON 的未知字段忽略提供了基本的向后兼容，但没有任何机制表达"这是 v1 格式"——未来任何格式变更都是在没有坐标的情况下修改。

3. **开源项目需要清晰的兼容性契约。** 如果其他开发者 fork 或贡献 SSE 功能，他们需要知道：信封格式是否稳定？添加字段是否需要升级版本？不同版本的兼容策略是什么？

4. **这不是紧急问题，但拖延会增加迁移成本。** 当前只有 ~200 行加密代码，信封改动代价低。等到有 100 万+ 对象使用老格式时再想加版本号就晚了。

### 缺失的能力

1. **信封格式版本号：**

   ```go
   type sseEnvelope struct {
       Version int    `json:"ver"`       // ← 新增：格式版本，当前 1
       Alg     string `json:"alg"`
       Wrap    string `json:"wrap,omitempty"`
       Kid     string `json:"kid,omitempty"`
       Kek     string `json:"kek"`
       IV      string `json:"iv"`
   }
   ```

2. **版本化解析器：**

   ```go
   func parseEnvelope(s string) (*sseEnvelope, error) {
       var env sseEnvelope
       if err := json.Unmarshal([]byte(s), &env); err != nil {
           return nil, fmt.Errorf("sse: parse envelope: %w", err)
       }
       // 旧格式（无 version 字段）= v1
       if env.Version == 0 {
           env.Version = 1
       }
       // 未来 v2 检查
       if env.Version < 1 || env.Version > currentEnvelopeVersion {
           return nil, fmt.Errorf("sse: unsupported envelope version %d", env.Version)
       }
       // ...
   }
   ```

3. **兼容性策略文档（在代码注释中）：**

   ```
   // Envelope version history:
   //   v1 (ver:0 or ver:1) — AES-256-GCM, JSON sidecar
   //     Fields: alg, wrap, kid, kek, iv
   //     Backward compatible: yes
   //     Forward compatible: new fields appear as unknown to old code
   //
   //   v2 (planned) — AES-256-GCM-SIV support + additional authenticated data
   //     Added: aad field (base64 encoded AAD)
   //     Migration: rewrap on read, or batch rewrite
   ```

4. **主版本兼容性守卫：**

   ```go
   // 在 decrypt 和 rewrap 路径中
   if env.Version > currentEnvelopeVersion {
       return nil, fmt.Errorf("sse: envelope version %d > current %d; "+
           "please upgrade aero-vault to decrypt this object", env.Version, currentEnvelopeVersion)
   }
   ```

### 改造影响

| 维度 | 评估 |
|------|------|
| **向后兼容** | `Version:0`（旧格式未设置）自动识别为 v1，旧信封无需迁移 |
| **代码变动量** | 极小：新增常量 + 结构体字段 + parse 检查 + 所有 write 路径写入 Version:1 |
| **存储影响** | 每个信封 JSON 增加 ~10 字节（`"ver":1,`），约 0.5% 开销 |
| **用户感知** | 完全透明 |

---

## 方向五：事件流交付保障——SSE Replay 完备性与 Webhook 死信审计

### 现状

事件流系统有两个交付保障缺口：

**缺口 A：SSE Replay 不完备**

```go
// internal/api/rest/sse.go:28-43
func (h *SSEHandler) replayMissed(w http.ResponseWriter, flusher http.Flusher, r *http.Request, tenant string, lastID int64) {
    if lastID <= 0 {
        return
    }
    backlog, err := h.repo.NextUnconsumedEvents(r.Context(), 200)  // ← 硬编码 200 条上限
    if err != nil {
        return  // ← 静默返回，客户端不感知 replay 失败
    }
    for _, e := range backlog {
        if e.ID <= lastID || e.TenantID != tenant {
            continue
        }
        if !writeEvent(w, flusher, e) {
            return
        }
    }
}
```

| 问题 | 代码证据 | 影响 |
|------|---------|------|
| **上限 200 条，无分页** | `NextUnconsumedEvents(r.Context(), 200)` | 客户端断开较久（如网络抖动 1 分钟），产生的超过 200 条事件在重连后静默丢失 |
| **DB 查询错误静默忽略** | `if err != nil { return }` | 事件日志 DB 故障时客户端不感知 replay 失败 |
| **不校验事件是否存在** | 仅按 ID 过滤 | 如果事件被 GC 清理（`IdempotencyGC` 或 `RetentionJob`），replay 丢失的事件对客户端完全透明 |
| **不分页效率** | 一次最多查 200 条，如果客户端需要更多，需多次重连递增 Last-Event-ID | 设计缺陷 |

**缺口 B：Webhook 死信与成功记录混淆**

```go
// internal/events/webhook.go:218-224
// give up after 10 attempts: record the final failure detail, then retire the
// row so it is no longer re-selected by NextPendingFailures. The schema only
// has a binary `succeeded` flag (no dedicated dead-letter state), so we reuse
// MarkWebhookSucceeded as the terminal transition — this intentionally
// conflates "permanently dead" with "succeeded" to stop perpetual retries and
// unbounded table growth.
if attempts >= 10 {
    _ = w.repo.UpdateWebhookFailure(ctx, f.ID, "dead-lettered after ...", 0, time.Now(), attempts)
    _ = w.repo.MarkWebhookSucceeded(ctx, f.ID)    // ← 死信标记为成功
    return
}
```

| 问题 | 代码证据 | 影响 |
|------|---------|------|
| **死信标记为 succeeded** | `MarkWebhookSucceeded` | 运维人员查询 `webhook_failures` 表时无法区分"成功投递"和"永久失败" |
| **无死信告警** | 无 `dead_lettered` 指标或无 `succeeded=false AND attempts>=10` 查询 | 死信事件无声消失，无人知晓 |
| **无重驱能力** | 死信后无法重新投递 | 即使目标恢复，已死信的事件无法重新投递 |

### 为什么需要

1. **SSE 是 Web UI 核心交互通道。** `/ui` 页面（search/detail/lineage/chat）依赖 SSE 流接收实时事件。如果用户切换标签页后回来，replay 只能回放最多 200 条事件——这意味着高频场景（如大批量上传时的 `object.created` 事件洪峰）下，用户会无声地错过事件。

2. **Webhook 死信混淆是一个隐蔽的数据丢失通道。** 事件经过重试 10 次后，被标记为 "succeeded" 从重试队列中移除。运维人员查询 `ListWebhookFailures` 看到的是 "succeeded"，不会去检查这批事件。目标系统在 10 次重试期间可能暂时不可用，恢复后也无法收到这些事件。

3. **事件基础设施信任是事件驱动架构的基石。** 如果用户不能信任 SSE 能收到所有事件、不信任 webhook 能可靠投递，事件系统的价值就大打折扣。当前的设计在这两个信任维度上都有缺口。

### 缺失的能力

**SSE 部分：**

1. **分页式事件 replay：**

   ```go
   func (h *SSEHandler) replayMissed(w http.ResponseWriter, flusher http.Flusher, r *http.Request, tenant string, lastID int64) {
       if lastID <= 0 {
           return
       }
       pageSize := 200
       for {
           backlog, err := h.repo.NextUnconsumedEvents(r.Context(), pageSize, lastID+1) // ← 分页游标
           if err != nil {
               writeError(w, flusher, "replay_failed", err.Error()) // ← 向客户端报告错误
               return
           }
           if len(backlog) == 0 {
               break
           }
           for _, e := range backlog {
               if e.TenantID != tenant {
                   continue
               }
               if !writeEvent(w, flusher, e) {
                   return
               }
               lastID = e.ID // 追踪已发送的最大 ID
           }
           if len(backlog) < pageSize {
               break // 队列已赶齐
           }
       }
   }
   ```

2. **`sse_replay_lag{tenant}` 指标**——追踪 SSE 客户端 replay 延滞。

3. **SSE 事件日志 GC 告知机制**——如果事件被 RetentionJob GC 清理，SSE handler 在 replay 时向客户端发送 `event: gap` 通知，表明事件流中有缺口。

**Webhook 部分：**

4. **`webhook_failures` 表新增 `status` 字段**（而非仅二进制 `succeeded` flag），支持以下状态：

   ```
   pending    → 等待重试
   succeeded  → 成功投递
   dead       → 超过最大重试次数，永久失败
   ```

5. **`dead_lettered_total` 指标**——Prometheus counter，当 webhook 死信时 +1。

6. **`dead_letter_redrive` 管理 API**——`POST /v1/admin/webhook/dead-letter/redrive` 将状态为 `dead` 的记录重新放入重试队列。

7. **死信告警规则**——Prometheus alert：`rate(webhook_dead_lettered_total[5m]) > 0`。

### 架构概要

```
当前:
  SSE replay: 硬编码200条，静默失败
  Webhook dead-letter: 复用 MarkWebhookSucceeded，与成功混淆

改进:
  SSE replay: 分页replay直到赶齐
              replay错误→客户端感知
              event: gap 通知
              sse_replay_lag 指标
              
  Webhook dead-letter: 独立 dead 状态
                       dead_lettered_total 指标
                       admin redrive API
                       Prometheus 告警

  webhook_failures 表迁移（0025）:
    ALTER TABLE webhook_failures ADD COLUMN status TEXT NOT NULL DEFAULT 'pending';
    -- succeeded=1 → status='succeeded'
    -- succeeded=0 AND attempts>=10 → status='dead'
    -- succeeded=0 AND attempts<10 → status='pending'
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **事件日志 GC 已清理** | SSE replay 检测到 ID 间隙 → 发送 `event: gap` 通知，不阻塞后续 replay |
| **死信后目标恢复** | 运维通过 `/admin/webhook/dead-letter/redrive` 批量重新投递，或手动设置单条 |
| **SSE 客户端落后太多（>10000 条事件）** | 限制最大 replay 量（`SSE_MAX_REPLAY=5000`），超出时发送 `event: gap` + 跳过早期事件 |
| **Webhook 多次死信同一事件** | 防止死信-redrive-死信循环：redrive 后 attempts 递增，超过最大尝试后再次死信 |

---

## 综合优先级与建议实施顺序

| 优先级 | 方向 | 影响面 | 前置依赖 | 涉及文件量 | 建议开始时间 |
|--------|------|--------|---------|-----------|------------|
| **P1** | 配置架构：Schema、校验与运行时审计 | 运维/可靠性——直接影响生产启动安全 | 无 | `internal/config/*`（~600 行） | **当前 Sprint** |
| **P1** | 跨协议请求幂等性 | 可靠性/数据完整性——多协议用户核心信任 | 现有 Idempotency-Key 基础设施已完整 | `internal/api/s3compat/` + `internal/api/webdav/` + `internal/mcp/` + `internal/service/` | **当前 Sprint** |
| **P2** | 生产架构备份/灾备 | 可靠性/运维——中长线工程投入 | `internal/snapshot/` 已有 SQLite 基础 | `internal/backup/`（新增 ~400 行）+ `internal/repository/postgres.go`（~10 行） | **下一 Sprint** |
| **P2** | SSE Replay + Webhook 死信审计 | 可靠性/事件基础设施信任 | 无 | `internal/api/rest/sse.go` + `internal/events/webhook.go` + 迁移 0025 | **下一 Sprint** |
| **P3** | 加密信封格式版本化 | 安全/工程持续——远期兼容性 | 当前格式无需迁移（backfill 为 v1） | `internal/storage/encrypt.go`（~20 行） | **在下一次信封格式变更前** |

### 与现有分析的关系

| 本方向 | 与既有覆盖的最大重叠 | 差异化价值 |
|--------|---------------------|-----------|
| 配置 Schema/校验/审计 | ❌ 零覆盖 | 从无到有建立配置治理体系 |
| 跨协议幂等性 | ❌ 零覆盖 | 首次提出多协议间的数据一致性保障 |
| 生产架构备份/灾备 | ⚠️ v8/v20 方向表各 1 行提及 | 首次给出完整架构设计和实现路径 |
| SSE Replay + 死信审计 | ⚠️ v39 覆盖 SSE channel 泄漏 | 聚焦交付保证而非资源泄漏 |
| 加密信封版本化 | ⚠️ v24 覆盖 envelope key rotation | 聚焦格式演进而非密钥轮换 |

---

## 附录：去重验证方法

| # | 方向 | 验证关键词 | 覆盖文档数 | 最高覆盖深度 |
|---|------|-----------|-----------|------------|
| 1 | 配置架构 | `config.*schemas\|config.*validat\|config.*audit\|config.*dump\|env.*var.*valid\|configuration.*schema` | **0** | ❌ 零覆盖 |
| 2 | 跨协议幂等性 | `s3.*idempotent\|multipart.*idempotent\|webdav.*idempotent\|mcp.*idempotent\|cross.*protocol.*idempotent\|rest.*idempotent.*s3` | **0** | ❌ 零覆盖 |
| 3 | 生产灾备 | `backup.*postgres\|postgres.*backup\|postgres.*dr\|disaster.*recovery.*arch\|backup.*strategy\|backup.*story\|pitr.*api` | 3（v8/v20/v35） | ⚠️ 方向表各 1 行浅层草图，无架构分析 |
| 4 | 信封版本化 | `envelope.*version\|sse.*version.*format\|encrypt.*format\|encrypt.*migration\|format.*migration\|sseEnvelope.*version` | 2（v24/v43） | ⚠️ v24 聚焦 key rotation 技术细节；v43 方向一预签名 URL 安全中一行举例提及 envelope 格式，**非信封版本化分析** |
| 5 | SSE Replay 完备性 | `sse.*replay.*limit\|sse.*pagination\|event.*replay.*page\|replay.*missed\|NextUnconsumed.*limit\|sse.*backlog` | 1（v39） | ⚠️ v39 方向一 SSE channel 泄漏分析中 2 行提到 replay 边界但非独立分析；死信审计零覆盖 |

> **验证范围：** `docs/requirements/` 下全部 44 份分析文档 + `docs/ROADMAP.md` + `docs/CHANGELOG.md` + `docs/TODO.md` + `docs/adr/DECISIONS.md` + `docs/extensions*.md` + `docs/analysis-gaps-roadmap.md`
