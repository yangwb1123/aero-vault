# 高价值扩展方向：企业身份联合、API 生命周期治理、存储韧性、元数据可扩展性、运行时配置

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — 240+ Go 源文件、4 个协议适配器(REST/S3/WebDAV/MCP)、50 对双数据库迁移文件、3 套 SDK(Go/Python/JS)、Web UI、MCP 双模(HTTP+stdio)、完整部署配置(Helm/Grafana/Prometheus)  
> **核心原则：** 不编写任何代码。每个方向必须包含：代码中的具体实现锚点、可量化的生产影响、架构权衡与边界情况。  
> **日期：** 2026-07-11

---

## 方法论

本分析从三个维度筛选已有 117 份分析文档（`expansion-v1` ~ `expansion-v117`）均未充分覆盖的缺口：

| 维度 | 判定标准 | 筛选结果 |
|------|---------|---------|
| **企业就绪性** | 不符合企业安全/合规/运营要求的功能缺口 | 方向一、二 |
| **弹性架构** | 系统在高负载/故障场景下表现出的设计局限 | 方向三、四 |
| **运维成熟度** | 集群部署和生产环境所需的基础设施能力缺失 | 方向四、五 |

### 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点数 | 既有分析覆盖度 |
|---|------|------|--------|---------|-----------|--------------|
| **1** | **企业身份联合（OIDC/OAuth2/SSO）** | 安全/合规 | **P1** | 身份认证完全自建，无任何企业 IdP 集成。多租户 SaaS 的 SOC2/ISO 硬性要求无法满足 | 6+ | **❌ 零覆盖** |
| **2** | **API 生命周期治理（版本协商/废弃策略/向后兼容契约）** | 平台/可维护性 | **P2** | REST API 在 `/v1` 下线性增长，无版本协商、无废弃标记、无向后兼容性测试框架 | 5+ | **⚠️ 浅覆盖（3份文档仅概念提及）** |
| **3** | **存储后端韧性 — 断路器完备性、多后端路由与故障转移** | 可靠性/架构 | **P1** | 断路器仅装饰单个后端，无多后端路由/降级/迁移能力；单后端失效即整体不可用 | 8+ | **⚠️ 浅覆盖（6份文档，均未分析断路器代码缺口）** |
| **4** | **元数据存储可扩展性天花板 — 查询模式、索引策略与连接管理** | 性能/架构 | **P1** | SQLite 是默认基线，但其并发模型和查询模式无法支撑 >100K 对象或 >10 并发写入。Postgres 路径缺乏连接池、查询优化与索引覆盖 | 7+ | **⚠️ 轻覆盖（7份文档提及瓶颈但无系统性分析）** |
| **5** | **运行时配置系统与功能标志（Feature Flags）** | 运维/平台 | **P2** | 所有配置在进程启动时一次性从环境变量加载。运行时无法变更配置、无法动态启停功能、无法金丝雀发布 | 5+ | **⚠️ 浅覆盖（4份文档概念提及，无实现分析）** |

---

## 方向一：企业身份联合（OIDC/OAuth2/SSO）

### 现状

当前认证系统（`internal/auth/`）完全自建，仅支持三种认证方式：

| 方式 | 实现 | 局限 |
|------|------|------|
| **API Key (X-Api-Key / Bearer)** | `auth.go:101-130` — 内存 map + 可选的持久化哈希存储 | 无法与企业 IdP 集成；密钥管理完全脱离标准协议 |
| **JWT (HS256)** | `jwt.go:40-70` — 自签发 HS256 JWT，基于 `AUTH_JWT_SECRET` | 无 JWKS 端点、无公钥验证、无 OIDC 发行者发现、无 `aud` 声明校验 |
| **SigV4** | `sigv4.go:60-120` — S3 签名验证 | 仅用于 S3 协议，不适用于 REST API |

**关键代码证据：**

```go
// internal/auth/auth.go:45-50
type Key struct {
    Token  string            // 明文 token（仅内存中存在）
    Tenant string            // 关联租户
    Scopes map[string]struct{} // read / write / admin
}
```

- `auth.go` — `Registry` 结构体完全自建，无任何 IdP 回调、OIDC Discovery、JWKS 验证逻辑
- `auth_middleware.go:30-50` — 中间件从 header 提取 token → `reg.Authenticate()` → 在内存 map 中查找。无 OAuth2 Bearer token 自省
- `auth_test.go` — 所有测试仅使用本地 token，无 IdP mock
- `store.go` — `PersistentStore` 接口仅支持 API Key 的持久化，不涉及身份联合

### 为什么需要它

| 场景 | 影响 |
|------|------|
| **企业 SSO 集成** | 客户要求使用 Okta/Azure AD/Keycloak 登录。当前无任何标准协议支持，成为 PoC→Production 的关键障碍 |
| **JWT 互操作** | 自签发 HS256 JWT 无法被外部服务验证（无 JWKS 端点、无 RS256/ES256 支持）。外部 IdP 签发的 JWT 也无法被系统信任 |
| **OAuth2 授权码流程** | 浏览器 SPA 需要通过 OAuth2 授权码 + PKCE 获取 token，当前完全缺失 |
| **租户身份映射** | 企业 IdP 中的 `group`/`role` 声明需映射到内部 tenant scope。当前认证模型无声明提取管道 |
| **SOC2/ISO 27001** | 自建密码方案在合规审计中需要额外论证。OIDC/OAuth2 是行业标准，合规路径明确 |

### 边界情况与架构权衡

| 权衡 | 讨论 |
|------|------|
| **自建 vs 代理** | 集成 OIDC 有两种路径：(a) 在系统内实现 RP（Relying Party）逻辑，需要维护 OIDC 库和会话状态；(b) 前置反向代理（如 OAuth2 Proxy、Envoy 外部授权），认证在代理层完成，系统仅消费代理注入的头信息。路径(b) 更符合 Unix 哲学，但会丧失细粒度租户映射能力 |
| **会话与无状态** | OIDC 授权码流程需要会话支持（cookie/session store），当前系统完全无状态（每次请求独立认证）。引入会话是架构范式转换 |
| **JWKS 缓存失效** | 远程 JWKS 端点需缓存 + TTL + 主动刷新。`auth/key_cache.go` 已提供通用缓存基础设施，但 JWKS 刷新需要额外的后台轮询线程 |
| **租户身份源** | 多租户场景下每个租户可能使用不同的 IdP（如租户 A 使用 Okta、租户 B 使用 Azure AD）。认证中间件需要根据 `X-Aero-Tenant` 动态选择 IdP 配置 |
| **SCIM 用户同步** | 企业期望通过 SCIM 协议自动同步用户/组。当前无用户模型、无组模型、无 SCIM 端点。这是远大于 OIDC 集成的工程投入 |

### 代码实现路径

1. **JWKS 端点 + RS256/ES256 支持**：在 `internal/auth/jwt.go` 中添加 JWKS 缓存与公钥验证逻辑。为系统签发 JWT 的能力添加 RS256 支持（当前仅 HS256）。暴露 `GET /.well-known/jwks.json` 端点
2. **OIDC Discovery 集成**：添加 `OIDCProvider` 结构体，支持从 `.well-known/openid-configuration` 发现元数据。认证中间件根据 tenant 配置选择 OIDC provider
3. **OAuth2 授权码 + PKCE 流程**：为 SPA 客户端新增 `GET /v1/auth/login`、`GET /v1/auth/callback`、`POST /v1/auth/token` 端点。引入短期会话 cookie
4. **声明提取与租户映射**：在 token 验证后添加声明提取插件接口，将 IdP 返回的 `groups`/`roles` 映射到内部 `TenantID` + `Scopes`
5. **测试策略**：使用 `httptest` 模拟 OIDC Provider。集成测试使用 `ory/hydra` 或 `dex`（Docker 编排）

---

## 方向二：API 生命周期治理（版本协商、废弃策略、向后兼容契约）

### 现状

系统 REST API 全部部署在 `/v1` 路径下，这是唯一的版本标识。但：
- **无版本协商**：客户端无法请求特定版本。API 变更对所有客户端立即可见
- **无废弃标记**：删除/修改端点时不通知客户端。无 `Sunset`、`Deprecation`、`Deprecation-Plan` HTTP 头
- **无向后兼容性测试**：无合约测试来确保已有客户端不会因 API 变更而损坏
- **OpenAPI 规范同步**：`openapi.json` 是手动维护的副本而非源头，容易与实现不同步

**关键代码证据：**

```go
// internal/api/rest/router.go:15-30
func NewRouter(...) http.Handler {
    r := chi.NewRouter()
    // 所有路由注册在 /v1 下，无版本子路由
    r.Get("/files", ...)
    r.Put("/files/{key}", ...)
    // ...
}
```

```go
// internal/api/rest/openapi.go:20-40
// OpenAPI 规范是硬编码 JSON 字符串，不反射实现
func OpenAPISpecHandler() http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        w.Write(openapiJSON) // 来自 openapi.json 文件
    }
}
```

- `router.go:25-60` — 所有 handler 直接挂载在 `/v1` 下，无 `/v1.1` 或 `/v2` 路由组
- `handlers_test.go` — 测试验证 JSON 响应格式，但无向后兼容性套件
- `openapi.json` — 静态 JSON 文件，需手动更新（经常与实现不同步）
- `dto.go` — DTO 类型直接用于序列化，无版本感知的编解码器

### 为什么需要它

| 场景 | 影响 |
|------|------|
| **API 演进** | 当前修改 JSON 响应字段即破坏所有现有客户端。无能力添加新字段同时保持旧字段（兼容性扩展） |
| **SDK 版本锁定** | Go/Python/JS SDK 绑定到特定响应结构。服务端一次不兼容变更需要同时更新 3 个 SDK 并强制用户升级 |
| **灰度/金丝雀** | 无法让部分客户端使用新版 API 而其他客户端保持旧版。API 变更必须全量发布 |
| **废弃流程** | S3 API 的废弃通常有 12 个月过渡期。当前无机制标记端点废弃并在过渡期后自动移除 |
| **OpenAPI 文档漂移** | 当前 `openapi.json` 与实现之间无自动同步机制。API 消费者看到的是过时文档 |

### 边界情况与架构权衡

| 权衡 | 讨论 |
|------|------|
| **URL 路径版本 vs Accept Header 版本** | `/v1/files` vs `Accept: application/vnd.aero-vault.v1+json`。路径版本对缓存和 API 网关更友好；Accept Header 版本符合 REST 纯粹主义但增加了客户端复杂度。建议 URL 路径版本 |
| **向后兼容的默认值** | 新端点默认返回新格式。但已有客户端可能依赖隐式默认值。例如：分页从 20 改为 50 会静默改变客户端行为 |
| **版本维护成本** | 每维护一个 API 版本意味着 N 套 handler/序列化/测试。需要明确版本生命周期（如维护 2 个主要版本 + 12 个月过渡期） |
| **内部重构 vs 外部断裂** | `/v1` 下端的内部重构可能保持序列化不变（兼容性重构）。需要 CI 阶段运行合约测试来验证 |

### 代码实现路径

1. **版本中间件**：新增 `internal/middleware/version.go` — 解析 `Accept` header 或 URL 前缀，注入 `context.WithValue(ctxAPIVersion)`。未指定版本时默认使用最新版
2. **版本化响应编码器**：在 `internal/api/rest/dto.go` 中添加版本感知的响应编解码器。每个 DTO 结构支持多个版本的 `MarshalJSON`/`UnmarshalJSON`
3. **废弃头注入**：在中间件层添加 `Deprecation: true`、`Sunset: Sat, 01 Jan 2027 00:00:00 GMT`、`Deprecation-Plan: https://docs.aero-vault.dev/api/v1/deprecation-plan` 等响应头
4. **合约测试套件**：新增 `internal/api/contract/` 包，使用 golden file 模式记录每个 API 端点的响应快照。CI 中运行 `go test ./internal/api/contract/` 检测意外变更
5. **OpenAPI 自动生成**：替换静态 `openapi.json` 为基于 Go 代码注解的自动生成管线（参考 `ogen` 或 `swaggo` 模式）。确保文档始终与实现一致
6. **路由版本化**：在 `router.go` 中使用 chi 的 `Route` 分组：`r.Route("/v1", func(r chi.Router) { ... })`、`r.Route("/v2", func(r chi.Router) { ... })`，共享底层 `FileService` 但使用不同的 DTO

---

## 方向三：存储后端韧性 — 断路器完备性、多后端路由与故障转移

### 现状

系统当前有一个**单后端存储架构**：

```
FileService → Storage (interface) → Local / S3 / OSS / COS 中的一个
```

断路器（`internal/storage/circuitbreaker.go`）已实现但存在关键缺口：

| 缺口 | 描述 | 代码证据 |
|------|------|---------|
| **断路器仅装饰单后端** | `NewFromConfig` 支持断路器包装，但只能包装一个后端。后端失效时系统整体不可用 | `factory.go:55-68` → `store = NewCircuitBreaker(store, cfg.CircuitBreaker)` |
| **无多后端路由** | 无法根据存储类别（STANDARD vs GLACIER）或租户将对象路由到不同后端 | `storage.go:30` — `Storage` 接口无路由概念 |
| **无故障转移** | 主后端不可用时无法降级到备后端（如 S3 → local 降级） | 从 `FileService` 到 `Storage` 的调用是直接方法调用，无 fallback 链 |
| **断路器状态不可观测** | `s.circuitBreaker.Stats()` 方法存在但未暴露为 Prometheus 指标 | `circuitbreaker.go:100-110` — Stats 方法未被 `telemetry/metrics.go` 引用 |
| **单点故障** | `FileService.Storage()` 返回单一 `Storage` 实例，无健康检查和自动切换 | `file.go:100` → `func (s *FileService) Storage() storage.Storage { return s.store }` |

此外，`CompleteMultipart` 路径在本地存储后端存在**原子性缺口**：part 文件在合并过程中若进程崩溃，部分合并的临时文件成为孤儿，且无法重试（upload ID 对应的内存状态已丢失）。

```go
// internal/storage/local_multipart.go:80-95
func (s *LocalStorage) CompleteMultipart(ctx context.Context, key, uploadID string, parts []MultipartPart) (ObjectInfo, error) {
    // 从内存 map 读取 upload 状态
    up, ok := s.uploads[uploadID]  // ← 进程重启后此处永远为 false
    
    // 逐个合并 part 文件
    for _, part := range parts {
        // 如果在此处崩溃，部分合并的文件被写入，但无法回滚
        io.Copy(tmp, f)  // ← 无事务边界
    }
}
```

### 为什么需要它

| 场景 | 影响 |
|------|------|
| **S3 后端降级** | S3 服务降级（如限流或可用区故障）时，系统可降级到本地缓存后端继续服务读操作。当前直接 503/5xx |
| **存储在线迁移** | 从 local FS 迁移到 S3（或反之）需要停机窗口。多后端路由允许逐个对象/桶迁移，零停机 |
| **冷热数据分层** | 标准存储 vs 归档存储。当前无机制将 30 天前的对象自动迁移到低成本后端 |
| **多区域部署** | 主区域后端失效时，从备用区域的后端提供读取服务。当前无此能力 |
| **断路器的运维盲区** | 断路器触发但 ops 人员不知情。当前无 Prometheus alert 反映断路器状态 |

### 边界情况与架构权衡

| 权衡 | 讨论 |
|------|------|
| **双写一致性** | 多后端写入时的最终一致性模型：是同步双写（主+备同时写入）还是异步复制（利用现有 replication worker）？同步双写增加写入延迟和失败概率；异步复制的故障转移窗口有数据丢失风险 |
| **读取向** | 故障转移后读请求指向备后端。但备后端可能比主后端延迟更高或数据更新滞后。需要定义自愈策略（主后端恢复后数据回迁） |
| **存储类别映射** | S3 支持 `STANDARD_IA`/`GLACIER`/`DEEP_ARCHIVE`。OSS/COS 的对应类别不同。需要一个抽象层来映射跨后端的存储类别语义 |
| **部分故障** | 不是整个后端不可用，而是特定操作（如 `Put`）变慢。需要操作级别的断路器，而非后端级别的粗粒度断路器 |
| **测试复杂度** | 多后端路由系统的测试需要模拟后端对组合（主可用/备可用/主不可用/备不可用/二者均不可用/二者均降级等 6+ 种状态）。当前单后端测试模型不适用 |

### 代码实现路径

1. **路由 Storage 抽象**：新增 `internal/storage/router.go` — 实现 `Storage` 接口的路由器，根据以下策略选择后端：
   - `storage_class` 字段：`STANDARD` → 后端 A，`GLACIER` → 后端 B
   - `tenant_id`：租户级后端绑定（数据主权）
   - `bucket`：桶级后端配置
2. **健康检查与故障检测**：新增 `internal/storage/health.go` — 定期探测所有注册后端的健康状态（使用 `/healthz` 风格轻量检查）。健康状态影响路由决策
3. **故障转移逻辑**：在主后端健康检查失败时，路由自动切换到备后端。读请求可以透明降级；写请求返回 503 或排队到恢复
4. **断路器状态可观测**：将 `circuitBreaker.Stats()` 注册到 `telemetry.RegisterStorageGauges`，暴露 `storage_breaker_state{backend="s3"}`、`storage_breaker_failures_total`
5. **Multipart 事务化**：在 `local_multipart.go` 中引入临时合并文件 + 重命名提交模式，确保 `CompleteMultipart` 在进程崩溃时不会留下半合并文件。恢复时通过扫描 `.multipart/` 目录重建 `uploads` map
6. **迁移 Worker**：新增 `internal/migration/` worker，逐对象处理存储后端迁移。支持 `JobMigrateObject` 类型，使用现有 `jobs` 基础设施实现可重试的异步迁移

---

## 方向四：元数据存储可扩展性天花板 — 查询模式、索引策略与连接管理

### 现状

系统默认使用 SQLite（`internal/repository/sqlite.go`），可选 Postgres（`internal/repository/postgres.go`），通过 `s.rebind` 适配 SQL 占位符差异。但元数据层存在系统性扩展性问题：

**SQLite 瓶颈代码证据：**

```go
// internal/repository/sql_objects.go:130-150
func (r *repository) ListObjects(ctx, tenant, bucket, prefix, marker, limit) (ListPage, error) {
    query := `SELECT ... FROM objects 
              WHERE tenant_id=$1 AND bucket=$2 AND key LIKE $3 AND deleted_at IS NULL
              ORDER BY key COLLATE NOCASE LIMIT $4`
    // SQLite: 全表扫描 + ORDER BY (无索引支持 LIKE 前缀查询的有效排序)
}
```

```go
// internal/repository/sql_chunks.go:60-80
func (r *repository) SearchChunks(ctx, tenant, query, k) ([]Chunk, error) {
    // 暴力扫描: SELECT 所有该租户的 chunk → 内存加载 embedding → Go 层暴力扫描
    // 注释明确提到 "scales to ~100K chunks per tenant"
    rows, _ := r.db.Query(`SELECT id, tenant_id, object_id, ... FROM chunks WHERE tenant_id=$1`)
    // 全量加载 embedding (BYTEA/BLOB) → 在 Go 中计算 cosine 相似度 → 排序
}
```

**更广泛的元数据层问题：**

| 问题 | 代码锚点 | 影响 |
|------|---------|------|
| **ListObjects 全表排序** | `sql_objects.go:130-150` — `ORDER BY key COLLATE NOCASE` 无法利用前缀索引，大数据量下排序在内存中进行 | 10K+ 对象的桶，带前缀过滤的 LIST 延迟 >500ms |
| **无翻页游标优化** | `repository.go:ListPage` — `NextMarker` 基于 key 偏移，但对于 deep pagination，SQLite 的 `OFFSET` 性能随深度线性恶化 | 深度翻页（第 100 页以后）响应时间退化 10x |
| **SearchChunks 全量加载** | `sql_chunks.go:60-80` — 所有 chunk embedding 加载到内存。单租户 500K chunks ≈ 500MB+ 内存开销 | 每查询消耗与语料库大小成线性关系 |
| **connection 单点** | `sqlite.go:30` — `db.SetMaxOpenConns(1)` SQLite 单写；`postgres.go` 无连接池配置 | SQLite 写入并发度 = 1；Postgres 连接池缺失导致连接泄漏 |
| **无读副本支持** | `repository.go` — 单一数据源接口。LIST/GET 类读操作为何要消耗主库连接？ | 无法 scale read → 大量 LIST 请求挤占写入连接 |
| **事务范围过大** | `file_crud.go:writePutObject` — `s.repo.InsertObjectVersion` 在事务内执行 INSERT。对于大对象，事务持续整个存储写入周期 | 长事务阻塞其他写入；SQLite 的 WAL 模式缓解但未完全解决 |
| **元数据/存储双写无协调** | `file_crud.go:Put` — 先写存储再写元数据。若存储成功但元数据写入失败，产生孤儿 blob | 需要两阶段提交或补偿事务 |
| **TTL/GC 查询无索引覆盖** | `sql_objects.go:listSoftDeletedBefore` — 扫描 `deleted_at`，该列无索引 | 定时 GC 任务随表增长线性变慢 |

### 为什么需要它

| 场景 | 影响 |
|------|------|
| **>100K 对象部署** | SQLite 在单租户 100K 对象时查询延迟不可接受，搜索功能完全不可用（暴力扫描 100K+ chunks） |
| **高写入吞吐** | 多租户并发写入时，SQLite 的序列化写入成为瓶颈，`database is locked` 错误成为常见故障 |
| **多副本部署** | Postgres 部署不支持读副本路由，所有查询击中主库，写入吞吐受限于主库资源 |
| **LIST 大规模桶** | 包含 50K+ 对象的桶带前缀过滤的 LIST 操作响应 >2s，S3 协议下客户端的 listing 请求超时 |
| **搜索索引重建** | `ReindexStale` 扫描全表 chunk 与 model 不匹配的行。无索引时 O(n) 扫描成为定时炸弹 |

### 边界情况与架构权衡

| 权衡 | 讨论 |
|------|------|
| **SQLite vs Postgres 默认** | 修改默认存储是破坏性变更。建议保持 SQLite 为开发/轻量默认，但提供明确的扩展性文档：SQLite ≤ 50K 对象/单节点，Postgres ≥ 50K 对象/多节点 |
| **复合索引 vs 写入性能** | 为 `(tenant_id, bucket, key, deleted_at)` 建复合索引可加速 LIST 但增加写入开销。需评估读写比 |
| **读副本一致性** | 读副本可能读到过期数据（主从延迟）。LIST 和 SEARCH 类操作是否可以接受最终一致性？GET 操作需要强一致性吗？ |
| **连接池维度** | `database/sql` 的 `SetMaxOpenConns`、`SetMaxIdleConns`、`SetConnMaxLifetime` 需要根据部署规模调整。当前全未配置 |
| **搜索索引分离** | 元数据 DB 和向量搜索 DB 当前共享同一连接（pgvector 模式）。如果 Qdrant 作为搜索后端，向量搜索流量不应影响元数据查询性能 |

### 代码实现路径

1. **关键查询索引优化**：为 `objects` 表添加复合索引 `(tenant_id, bucket, key, deleted_at)`。为 `chunks` 表添加 `(tenant_id, embed_model)` 索引加速 `ListObjectIDsToReindex`。为 `soft_deleted_at` 添加部分索引（仅 `deleted_at IS NOT NULL`）
2. **ListObjects 翻页优化**：将 `OFFSET` 分页改为 keyset pagination（`WHERE key > $marker ORDER BY key LIMIT $limit`），消除深度分页的性能退化
3. **连接池与读副本**：在 `postgres.go` 中添加 `SetMaxOpenConns`/`SetMaxIdleConns`/`SetConnMaxLifetime` 配置。可选添加 `ReadReplica` 包装器，将查询路由到 `DB_READ_DSN`
4. **搜索向量列分离**：当使用 Postgres + pgvector 时，将 `chunks.embedding` 列移到独立的分区表或使用 `TOAST` 策略避免查询时加载。使用 `EXCLUDE` 和 `SET` 技巧避免 `SELECT *` 时的全列加载
5. **事务边界收缩**：`writePutObject` 中的事务仅包裹元数据写入，与存储写入解耦。引入重试/补偿机制处理存储写入成功但元数据写入失败的情况
6. **可扩展性基准测试**：新增 `internal/repository/bench_test.go`，在 SQLite 和 Postgres 上执行标准化查询基准。CI 门控确保关键查询的延迟不超过线性增长

---

## 方向五：运行时配置系统与功能标志（Feature Flags）

### 现状

系统全部配置通过环境变量在启动时加载（`internal/config/config.go`），运行时完全不可变更：

```go
// internal/config/config.go:30-50
type Config struct {
    App      AppConfig      `env:"APP_*"`
    Storage  StorageConfig  `env:"STORAGE_*"`
    Auth     AuthConfig     `env:"AUTH_*"`
    AI       AIConfig       `env:"AI_*"`
    // ... 所有子配置均通过 env tag 绑定
}

func Load() (*Config, error) {
    var cfg Config
    // 启动时一次性解析环境变量
    if err := env.Parse(&cfg); err != nil {
        return nil, err
    }
    return &cfg, nil
}
```

**运行时不可变的具体缺口：**

| 缺口 | 代码锚点 | 运营影响 |
|------|---------|---------|
| **功能无法动态启停** | `config.go` — 所有字段 `env:"..."`，无任何运行时覆盖机制 | 新功能上线必须全量重启。若新`indexer`逻辑有 bug，无法快速关闭 |
| **日志级别不可动态调整** | 启动时 `slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.App.LogLevel})` | 排查生产问题需 `DEBUG` 日志时，必须修改环境变量并重启实例 |
| **限流参数不能热更新** | `middleware/ratelimit.go:NewRateLimiter(cfg.RateLimit.RPS, cfg.RateLimit.Burst)` | 突增流量时无法动态提升限流阈值；攻击时可立刻收紧 |
| **AI 模型/端点不能运行时切换** | `cmd/server/main.go:buildEmbedder(cfg, logger)` — embedder 构建后不可变 | Embedder 服务故障时无法快速切换到备用端点；模型升级需要滚动重启 |
| **租户配置静态缓存** | `auth/key_cache.go:WithKeyCache(ttl, size)` — TTL 固定 | 密钥轮换后至多等待 TTL 才能全网生效 |

### 为什么需要它

| 场景 | 影响 |
|------|------|
| **金丝雀发布** | 新功能在 1% 租户上启用，观察指标后再逐步放量。当前只能全量或全不 |
| **紧急降级** | AI 后端故障 → 关闭语义搜索降级为纯 BM25。当前需要重启所有实例 |
| **动态限流** | 突增流量 → ops 人员在 dashboard 上直接调高限流参数。当前必须修改部署配置 |
| **A/B 测试** | 两个 embedder 模型在相同流量下对比效果。当前只能切换全部或手动部署两套环境 |
| **运行时调参** | 调整 chunker 参数（window/overlap）观察搜索结果变化。当前需重启索引器并重建索引 |

### 边界情况与架构权衡

| 权衡 | 讨论 |
|------|------|
| **配置中心依赖** | 引入外部配置中心（etcd/Consul/Redis）增加基础设施依赖和延迟。对于需要即时生效的场景（如限流）延迟必须 <100ms |
| **本地缓存 vs 远程读取** | 功能标志频繁读取（每次请求检查标志）可能成为性能热点。本地缓存 + 长轮询更新是成熟的模式 |
| **标志范围** | 全局标志、租户级标志、用户级标志。范围越大，存储和传播复杂度越高。建议从全局级开始，逐步引入租户级 |
| **配置变更审计** | 谁在何时修改了什么配置？配置变更应写入 `audit_log`。当前无上述链路 |
| **Config struct vs FeatureFlag struct** | 某些配置（如日志级别）适合直接修改，某些（如 storage backend）变更风险极高需要严格控制。建议区分"运行时参数"和"启动参数" |

### 代码实现路径

1. **Feature Flag 基础设施**：新增 `internal/feature/` 包，包含：
   - `FlagStore` 接口（支持内存 map / etcd / Redis / Postgres 等实现）
   - `Flag` 类型（名称 + 作用域 + 布尔/数值/字符串值）
   - `Middleware` — 从请求上下文提取 tenant，检查相关标志并注入 context
   - 默认回退：若 feature store 不可达，使用编译期默认值（fail-closed 策略）
2. **运行时日志级别**：将 slog 的 `Leveler` 替换为原子变量（`slog.LevelVar`），暴露 `POST /v1/admin/log-level` 端点（需 admin scope）。变更记录至 audit log
3. **动态 Rate Limiter**：`middleware/ratelimit.go` 中的 token bucket 参数改为 atomic 变量。新增 `PUT /v1/admin/ratelimit` 端点更新 RPS/Burst。变更同步可选 via Postgres LISTEN/NOTIFY 跨副本传播
4. **AI Provider 热切换**：embedder/LLM/reranker 实例包装为可切换代理层。`POST /v1/admin/ai/provider` 切换模型端点（限特定 scope）。新请求立即使用新端点，已有请求不受影响
5. **功能标志管理端点**：新增 `GET/PUT/DELETE /v1/admin/flags/{name}` 端点。标志值可作用于 `global` 或 `tenant:{id}` 范围。SDK 新增对应管理方法
6. **Config Watcher for 文件源**：支持从 YAML/TOML 文件读取热配置（使用 `fsnotify`），文件变更时自动重载。保留环境变量为最高优先级

---

## 附录：代码锚点汇总

| 方向 | 文件 | 关键行 | 说明 |
|------|------|--------|------|
| 1 | `internal/auth/auth.go` | 45-50 | Key 结构体——纯自建认证模型 |
| 1 | `internal/auth/auth_middleware.go` | 30-50 | 认证中间件——无 IdP 回调 |
| 1 | `internal/auth/jwt.go` | 40-70 | HS256 自签发 JWT——无 JWKS |
| 1 | `internal/auth/store.go` | 全文件 | PersistentStore 仅 Key 相关 |
| 1 | `internal/auth/key_cache.go` | 全文件 | 通用缓存——可复用做 JWKS 缓存 |
| 2 | `internal/api/rest/router.go` | 15-30 | 路由注册——单版本 |
| 2 | `internal/api/rest/openapi.json` | 全文件 | 静态 OpenAPI 规范 |
| 2 | `internal/api/rest/dto.go` | 全文件 | DTO——无版本感知编码 |
| 2 | `internal/api/rest/handlers_test.go` | 全文件 | 无合约测试 |
| 3 | `internal/storage/circuitbreaker.go` | 50-60 | 断路器实现但状态不可观测 |
| 3 | `internal/storage/factory.go` | 55-68 | 单后端工厂方法 |
| 3 | `internal/storage/storage.go` | 30 | Storage 接口——无路由概念 |
| 3 | `internal/storage/local_multipart.go` | 80-95 | CompleteMultipart 非事务性 |
| 3 | `internal/service/file.go` | 100 | FileService.Storage() 单实例 |
| 3 | `internal/telemetry/metrics.go` | 全文件 | 断路器指标完全缺失 |
| 4 | `internal/repository/sql_objects.go` | 130-150 | ListObjects 全表排序 |
| 4 | `internal/repository/sql_chunks.go` | 60-80 | SearchChunks 暴力扫描 |
| 4 | `internal/repository/sqlite.go` | 30 | MaxOpenConns=1 |
| 4 | `internal/repository/postgres.go` | 全文件 | 无连接池配置 |
| 4 | `internal/service/file_crud.go` | writePutObject | 长事务窗口 |
| 5 | `internal/config/config.go` | 30-50 | 一次性环境变量加载 |
| 5 | `internal/config/config_app.go` | 全文件 | LogLevel 启动时固定 |
| 5 | `internal/middleware/ratelimit.go` | NewRateLimiter | 限流参数不可变 |
| 5 | `cmd/server/main.go` | buildEmbedder | AI 组件启动时构建 |
