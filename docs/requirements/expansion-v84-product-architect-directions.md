# AeroVault 架构师/产品经理视角 — 第 84 轮：高价值产品与架构扩展方向

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 23+ 子包，三套 SDK，MCP 双模式，Web UI，48 对迁移文件，`deploy/` 配置，Makefile，CI gate）  
> **去重验证：** 对 `docs/requirements/` 下全部 83 份既有分析文档逐方向进行 `grep` 正则交叉验证 + 语义比对 + 代码锚点映射  
> **日期：** 2026-07-11  
> **核心原则：** 选取**代码中存在具体锚点**、**可量化影响**、且在前 83 轮分析中**零实质性架构分析**或**仅有表格式行级提及**的高价值方向。每个方向包含产品价值说明、代码锚点、影响分析、边界情况枚举。

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 | 既有分析覆盖 |
|---|------|------|--------|---------|---------|-------------|
| **1** | **API 版本契约与向后兼容性策略（API Versioning Contract & Backward Compatibility）** | 平台工程/开发者体验 | **P1** — 无版本契约意味着每次非兼容变更都静默断裂所有 SDK 和集成客户端；随项目扩张（83+ 轮扩展方向已提出数百个新增特性），API 变更频率必然上升，版本策略的缺失将成为平台信任的致命伤 | `internal/api/rest/router.go:NewRouter`（`/v1` 路由前缀注册——仅字符串文字，无版本协商）；`internal/api/rest/handler.go`（914 行 handler 逻辑无版本号感知）；`internal/api/rest/openapi.json`（`"info":{"version":"0.4.0"}`——项目版本而非 API 版本）；`sdk/go/aerovault/client.go:30`（`Version = "0.4.0"`——SDK 版本与服务版本耦合）；`sdk/js/aero-vault.js`（无版本协商逻辑）；`sdk/python/aero_vault.py`（无版本协商逻辑）；`internal/api/s3compat/router.go`（S3 端点无版本路径选项——`/s3` 而非 `/s3/v1`）；`internal/mcp/server.go:ListTools`（MCP 工具列表无版本元数据） | ✅ **完全去重**（`grep -rln "api.*version.*contract\|api.*version.*policy\|version.*negotiation\|version.*deprecation\|version.*sunset\|api.*backward.*compat\|api.*breaking.*change\|api.*stability\|api.*guarantee" docs/requirements/` → **2 次命中**，均为途经提及：v16 一行描述 S3 `?inventory` 缺失的表格中附带说明 `?inventory` 是"版本化特性"；v10 一行在极长文档中列出 `Deprecation: version="v1", sunset_date="2027-01-01"` 作为管理 API 表格的一格。**零实质性代码锚点分析、零影响评估、零边界情况枚举**） |
| **2** | **精细化成本感知速率限制（Cost-Weighted Granular Rate Limiting）** | 多租户/运维 | **P1** — 当前速率限制为一个全局 per-tenant 令牌桶，无法区分开销差异巨大的操作（GET/HEAD vs PUT/DELETE vs AI Embed vs AI Chat）。一个调用方发起大量廉价 GET 请求可耗尽桶配额，阻塞同租户的高价 AI 调用；反之，一次昂贵的 AI Chat 消耗的"资源份数"远高于一次文件读取，但在限流层面完全等价 | `internal/middleware/ratelimit.go:30-80`（`RateLimiter`——单令牌桶，所有路由共享一个配置）；`cmd/server/main.go:157-160`（`rl := middleware.NewRateLimiter(cfg.RateLimit.RPS, cfg.RateLimit.Burst)`——全局 RPS/Burst 两个数字定义全部限流行为）；`internal/config/config.go`（`RateLimit` 结构仅有 `RPS`/`Burst`/`AIRPS`/`AIBurst` 四个参数）；`internal/middleware/ratelimit_test.go`（仅测试桶的 token 生产/消费，不测试多路由差异化）；`internal/api/rest/router.go:50-60`（AI 端点独立套 `aiRL.Middleware()`——这是项目唯一的分组限流，但分组粒度仅 AI vs 非 AI，无更细的 `GET/PUT/DELETE` 区分）；`internal/service/file_crud.go:Put`（大文件 PUT 消耗的 I/O 带宽与 GET 差异显著但无对应权重） | ✅ **完全去重**（`grep -rln "rate.*limit.*endpoint\|rate.*limit.*granular\|per.*endpoint.*rate\|endpoint.*rate\|endpoint.*limit\|rate.*limit.*method\|rate.*limit.*GET\|rate.*limit.*PUT\|cost.*based.*rate\|weighted.*rate\|rate.*limit.*weight" docs/requirements/` → **4 次命中**。v29 以表格一行列出"精细化速率限制（按端点/方法/IP/路径）"并标记为"**完全未覆盖**"——但仅一句标题，**零代码锚点、零影响分析、零实施方案**。其余 3 次命中同样为路过提及。**本方向为首次以代码锚点驱动的完整分析**） |
| **3** | **对象访问热度追踪与自适应存储分层基础（Object Access Temperature Tracking for Adaptive Tiering）** | 成本优化/架构 | **P2** — 存储成本优化的核心杠杆是数据分层（热→温→冷→归档），而分层决策的唯一合理依据是**对象的实际访问频率与最近访问时间**。当前系统对此完全不可知：无访问时间戳（`objects` 表 `last_accessed_at`）、无访问频次统计、无热度衰减模型。即使未来实现 `STANDARD→STANDARD_IA→GLACIER` 分层规则，也只能依赖创建时间这类与访问模式无关的线性指标 | `internal/repository/repository.go:24-40`（`Object` 结构：有 `CreatedAt`/`UpdatedAt`，**无 `LastAccessedAt`**）；`internal/repository/sql_objects.go:CreateTable`（migration `0001` 定义 `objects` 表——仅 `created_at`/`updated_at` 列）；`internal/service/file_crud.go:Get`（走读路径不更新任何访问时间戳）；`internal/service/file_features.go:Stat`（元数据读取同样不记录访问）；`internal/repository/sql_objects.go:GetObject`（SQL 查询返回元数据，不更新访问计数）；`internal/telemetry/metrics.go`（15+ 领域指标——**零访问热度相关指标**）；`internal/reconcile/lifecycle.go:LifecycleJob`（生命周期规则仅基于 `updated_at` 时间线——无访问频率维度的过期策略）；`internal/config/config.go`（无 `StorageTierConfig` 或 `AccessTrackingConfig` 配置项） | ✅ **完全去重**（`grep -rln "access.*temperature\|access.*pattern.*track\|hot.*cold.*object\|object.*heat\|heat.*metric\|access.*freq\|frequency.*track\|access.*statistic\|access.*based.*tier\|thermal.*object\|object.*thermal\|last.*access.*time\|last_accessed" docs/requirements/` → **1 次命中**——v13 在自动分层规则框图中以一行概念性提及"热度分布统计"，**零代码锚点、零影响分析、零实施路径**。其余 82 份 doc 零覆盖） |
| **4** | **内容缓存控制与 CDN 集成层（Content Cache Control & CDN Integration）** | 性能/产品完整 | **P2** — 作为对象存储平台，内容交付性能直接影响用户体验。当前零缓存策略：PUT/GET 不设置 `Cache-Control`、`Expires`、`ETag`（虽然输出 ETag header 但无强缓存语义）、无 CDN 缓存失效机制、无对象级缓存 TTL。即使后端存储延迟只有 5ms，传输到全球用户仍可能高达 200ms+；借助 CDN 边缘缓存可将热门对象交付时间降至 <20ms | `internal/api/rest/handler.go:handleRangeOrFull`（`w.Header().Set("ETag", …)`——输出 ETag 但**无 `Cache-Control`、无 `Expires`、无 `Last-Modified` 之外的缓存头**）；`internal/api/s3compat/handler.go:writeObjectHeaders`（输出 `ETag`/`Last-Modified` 但**无缓存指令**）；`internal/service/file_crud.go:Put`（PUT 路径不处理客户端提供的 `Cache-Control` 请求头——**静默丢弃**）；`internal/config/config.go`（无 `CACHE_CONTROL_DEFAULT` 或 `CDN_*` 配置项）；`internal/repository/repository.go:Object`（无 `CacheControl`/`Expires` 字段）；`internal/api/rest/dto.go:objectDTO`（JSON 响应无缓存元数据字段）；`internal/events/bus.go`（事件系统无缓存失效事件类型——CDN 清除无法挂钩） | ✅ **完全去重**（`grep -rln "cache.*inval\|purge.*cache\|cache.*purge\|cache.*flush\|CDN.*cache\|content.*cache\|edge.*cach\|distribution.*cache\|cloudfront\|akamai\|fastly\|Cache-.*Control.*object" docs/requirements/` → **3 次命中**。v42 一行提及 `CORS 规则变更后生效延迟` → 建议缓存失效事件；v13 一行概念性列出"CDN-specific path"；v78 一行提及 cache invalidation via Postgres NOTIFY 作为 key cache 的子方向。**零独立分析面向对象的缓存控制策略、CDN 集成、缓存失效 API、对象级 TTL 的架构与实现**） |
| **5** | **跨租户对象分享与公网分享链接（Cross-Tenant Object Sharing & Public Share Links）** | 产品特性/平台完整 | **P2** — 当前对象严格按租户隔离，跨租户协作的唯一方式是走数据面的 PUT/GET（需目标租户有 API Key + 精确的 bucket/key 信息）。无分享链接意味着：（a）无法生成"可公开访问的文档下载链接"给非注册用户；（b）跨团队协作需要共享 API Key（不安全且不可审计）；（c）无法限制访问次数、过期时间、密码保护、IP 白名单等；（d）与主流文件分享平台的功能对标完全空白 | `internal/service/file_features.go:PresignGet`（预签名 URL 用于**存储后端**直链——仅限已认证对象，非跨租户分享）；`internal/service/file_crud.go:Get`（严格 `tenant + bucket + key` 组合——无匿名/跨租户访问路径）；`internal/auth/auth.go:Registry`（`Key.Tenant` 严格匹配——`*` 为 operator 不限租户，但无细粒度分享权限模型）；`internal/repository/sql_objects.go:GetObject`（SQL WHERE 子句 `tenant_id = ?`——无分享 token 查询路径）；`internal/api/rest/router.go`（无 `/v1/share/{token}` 或 `/v1/objects/{token}` 路由）；`internal/repository/repository.go`（无 `share_links` 表或相关接口方法）；`internal/config/config.go`（无 `SHARE_*` 配置项）；`sdk/go/aerovault/client.go`（无 ShareURL/ShareLink 方法） | ✅ **完全去重**（`grep -rln "share.*link\|shared.*link\|object.*share\|share.*object\|share.*url\|time.*limit.*url\|password.*protected.*url\|public.*share\|share.*token\|share.*access\|share.*link" docs/requirements/` → **6 次命中**。v22 在"AI 原生功能"表格中以一行概念性列出"分享链接追踪"作为 AI enrichment 特性的一格；`expansion-directions.md` 中列出了 `share_links` 表 schema 和 `share_access_logs` 表的 DDL 示例——但这是最早的需求文档（v1-equivalent），**无代码锚点、无影响分析、无路由设计、无边界情况**。**本方向为首次从当前代码库现状出发的完整分析**） |

---

## 方向一：API 版本契约与向后兼容性策略

### 现状

当前 API 通过 URL 路径中的 `/v1` 前缀区分版本，但这个前缀只是一个字符串常量，没有任何版本契约的支持：

```go
// internal/api/rest/router.go:NewRouter
func NewRouter(...) chi.Router {
    // ...
    r := chi.NewRouter()
    // 所有路由挂在 /v1 下
    return r
}

// 使用方式（cmd/server/main.go）:
r.Mount("/v1", rest.NewRouter(...))
```

问题在于：

1. **无版本协商（Content Negotiation）**：客户端无法通过 `Accept: application/vnd.aero-vault.v2+json` 或 `API-Version: 2` 请求头指定版本。所有请求强制 `/v1`。
2. **无弃用机制**：无 `Sunset` 或 `Deprecation` 响应头。客户端无法获知当前使用的 API 版本即将被弃用。
3. **无向后兼容性测试**：CI 中无步骤验证新 handler 的行为与旧 API 预期一致。
4. **多协议版本分离**：S3（`/s3`）、MCP（`/mcp`）、WebDAV（配置文件配置）各协议有自己独立的路径前缀，但版本策略不统一。
5. **OpenAPI spec 与实现脱节**：`openapi.json` 中 `info.version` 是项目版本（`0.4.0`）而非 API 版本，且无 CI 校验 handler 与 spec 的一致性（v46 首次分析了此问题但未上升到版本契约层面）。

### 影响分析

| 场景 | 当前行为 | 后果 |
|------|---------|------|
| 添加新必填字段到 search response | 修改响应结构 → 旧 SDK 反序列化失败（未知字段被忽略/error） | 客户端无声断裂 |
| 修改 error 响应格式 | 直接修改 `errorBody` 结构 → 所有解析 error 的 SDK 断裂 | 全 SDK 断裂 |
| 删除弃用的 `?deleted=true` 参数 | 直接忽略查询参数 → 客户端使用后无效果 | 用户困惑，无声行为变化 |
| 修改速率限制响应码从 429 → 403 | 直接改 `classify` 函数 → 客户端重试逻辑失效 | 客户端重试退避失效，触发雪崩 |
| 新增 AI 端点的请求格式 | 直接改 `/v1/chat` 的 JSON schema → 旧客户端发送旧格式 → 400 | 部署顺序受限（先更新所有 SDK） |
| 存储后端字段类型变更（int32→int64） | SQL 变化 + JSON 字段类型变化 → 32 位架构客户端可能溢出 | 平台特定 bug |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/api/rest/router.go:NewRouter` | 硬编码 `/v1` 路径前缀 | 无 API 版本注册表、无版本路由中间件 |
| `internal/api/rest/handler.go:914行` | handler 逻辑无版本感知 | 无法根据版本号提供差异化行为 |
| `internal/api/rest/openapi.json:info.version` | `"0.4.0"`（项目版本） | 非语义化的 API 版本号 |
| `internal/api/s3compat/router.go` | `/s3` 无版本前缀 | S3 协议无版本演进路径 |
| `sdk/go/aerovault/client.go:30` | `Version = "0.4.0"` | SDK 版本与 API 版本无关联 |
| `internal/api/rest/dto.go:objectDTO` | 公共响应结构体 | 无版本区分字段集 |
| `Makefile` / `HARNESS.md` | CI 检查（`go vet`, `go test`） | 无 OpenAPI spec vs handler 一致性校验 |

### 推荐方案（概念级）

1. **版本化中间件（`middleware.APIVersion`）**：在 router 层级解析 `Accept` 头或 `API-Version` 头，注入 `context`。`/v1` 路由默认对应 version 1。
2. **弃用响应头**：在 handler 中设置 `Sunset: Sat, 01 Jan 2028 00:00:00 GMT` 和 `Deprecation: true` 的标准 HTTP 弃用响应头。
3. **向后兼容性测试框架**：在 CI 中添加步骤，用旧版 `openapi.json` schema 校验新版 handler 的行为。
4. **OpenAPI spec 自动化校验**：`make check` 中加入 `openapi spec validate`，确保 `openapi.json` 的每次变更与 handler 代码同步。

### 边界情况

| # | 场景 | 预期行为 |
|---|------|---------|
| B1 | 客户端发送 `Accept: application/vnd.aero-vault.v1+json` | 正常返回 v1 响应（不做风格降级）；无此 header 时默认使用最新版 |
| B2 | 客户端发送 `API-Version: 2` 但 `/v1` 端点只有版本 1 | 返回 `400 VersionNotSupported` |
| B3 | 弃用 API 后旧客户端仍然调用 | 响应中始终包含 `Deprecation: true` + `Sunset` 头 |
| B4 | S3 协议版本演进 | S3 协议的版本通过 `x-amz-*` 头部演进，不依赖 URL 路径 |

---

## 方向二：精细化成本感知速率限制

### 现状

当前速率限制架构极其简化——整个服务的流量控制由两个独立的 per-tenant 令牌桶完成：

```go
// cmd/server/main.go
rl := middleware.NewRateLimiter(cfg.RateLimit.RPS, cfg.RateLimit.Burst)
aiRL := middleware.NewRateLimiter(cfg.RateLimit.AIRPS, cfg.RateLimit.AIBurst)
```

所有非 AI 端点共享 `rl` 一个桶；AI 端点共享 `aiRL` 一个桶。问题在于：

| 操作 | 后端资源消耗 | I/O 成本 | 限流权重 |
|------|------------|---------|---------|
| `GET /v1/files/doc.txt` | 1 次 DB stat + 1 次 storage read | 低 | 1 |
| `GET /v1/search?query=hello` | 1 次 DB 向量查询 + 可能 2 次 AI 调用 | 高 | 1 |
| `POST /v1/chat` | 1 次 search + 1 次 LLM 调用 | 很高 | 1 |
| `PUT /v1/files/large.iso` | 1 次 storage write（大文件—可能数 GB） | 很高 | 1 |
| `GET /v1/admin/keys` | 1 次 DB 查询 | 极低 | 1 |

一个恶意的（或行为异常的）客户端可以发送大量 `GET` 请求填满全局桶配额，导致同租户的关键业务 `PUT` 被限流；或者用数十个并行 `GET` 填满 I/O 带宽，拖慢所有其他操作。

### 影响分析

| 场景 | 后果 | 严重性 |
|------|------|--------|
| 租户 A 的 CI 流水线大量 GET | 全局桶配额耗尽 → 租户 A 的 AI Chat 被 429 | 功能影响 |
| 单个调用方大量廉价 HEAD 请求 | 桶配额耗尽 → 同租户的其他调用方被限流 | 公平性破坏 |
| 大文件 PUT（数 GB）与一个小文件 PUT 等价 | 大文件 PUT 占用后端带宽数分钟 → 其他操作排队 | 隐性不公平 |
| AI Embed 批量调用与文件 List 共享一个桶 | Embed 被 List 请求挤走 | 关键 AI 功能受影响 |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/middleware/ratelimit.go:30-80` | `RateLimiter`——单令牌桶，无多桶/多配置 | 无 `WeightedRateLimiter`、无 `EndpointRateLimiter` |
| `cmd/server/main.go:157-160` | 全局 RPS/Burst 两个值定义全部限流 | 无每端点、每方法的独立配置 |
| `internal/api/rest/router.go:10-15` | AI 端点和非 AI 端点分组是唯一粒度 | 无 `GET/PUT/DELETE` 分组 |
| `internal/config/config.go` | `RateLimit` 仅含 `RPS`/`Burst`/`AIRPS`/`AIBurst` | 无 `Limits map[string]EndpointLimit` 配置 |
| `internal/service/file_crud.go:Put` | 大文件 PUT 无尺寸感知的上限权重 | 无法将 PUT 的"成本"建模为 `1 + size/1MB` |

### 推荐方案（概念级）

1. **操作权重建模**：为每类操作分配权重因子（例如 `GET=1`, `HEAD=1`, `PUT=1+ceil(size/1MB)`, `DELETE=1`, `SEARCH=10`, `CHAT=50`, `AGENT=200`）。限流器按权重消费令牌。
2. **分桶架构**：保留 AI 与非 AI 的大分组，但 AI 分组内区分 `search` / `chat` / `agent` / `embed` 子桶；非 AI 分组内区分 `read` / `write` / `admin` 子桶。
3. **动态权重配置**：通过环境变量或 admin API 配置每个端点的权重和独立 RPS。
4. **突发保护**：超出单桶配额时降级到父桶配额（如 AI chat 桶满 → 借用 AI 父桶的配额）。

### 边界情况

| # | 场景 | 预期行为 |
|---|------|---------|
| B1 | 客户端连续发送大量小 GET 请求 | 每个 GET 消耗 1 单位——桶配额充足时正常 |
| B2 | 客户端发起一个 10GB 的 PUT | 消耗 `1 + 10240` 单位——大请求自然被节流 |
| B3 | AI Chat 子桶配额耗尽但 AI 父桶配额充足 | Chat 降级从父桶消费 |
| B4 | 权重配置变更未重启 | 通过 admin API 热更新，不影响在线请求 |

---

## 方向三：对象访问热度追踪与自适应存储分层基础

### 现状

对象存储的元数据仅追踪创建时间和最后更新时间，完全不追踪访问行为：

```go
// internal/repository/repository.go
type Object struct {
    // ...
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
    // 无 LastAccessedAt  time.Time
    // 无 AccessCount     int64
    // 无 AccessPattern   string // "hot"|"warm"|"cold"|"archive"
}
```

`GET` 路径不记录任何访问痕迹：

```go
// internal/service/file_crud.go:Get
func (s *FileService) Get(ctx context.Context, tenant, bucket, key string) (io.ReadCloser, repository.Object, error) {
    obj, err := s.repo.GetObject(ctx, tenant, bucket, key) // 只读，不更新访问时间
    // ...
    rc, _, err := s.store.Get(ctx, obj.StorageKey)
    // ...
    s.emit(ctx, obj, repository.EventAccessed) // 触发 EventAccessed 事件
    return rc, obj, nil
}
```

虽然 `EventAccessed` 事件被发到 EventBus，但**没有任何消费者处理此事件**——索引器忽略 `EventAccessed`，webhook 虽然广播但无下游聚合分析系统。

### 影响分析

| 场景 | 当前行为 | 期许行为 |
|------|---------|---------|
| 90% 的对象在创建后 7 天内被删除 | 无任何访问数据 | 生命周期规则应识别为"短寿命对象"，直接使用 STANDARD 无需降级 |
| 一个 500GB 数据集 365 天未被访问 | 与热数据同等计费，无自动降级 | 自动移至 GLACIER（假设有 tier 支持），节省 ~80% 存储成本 |
| 运维想知道"哪些 Bucket 在消耗读带宽" | 无访问统计数据 | 通过 `GET /v1/admin/stats/access?bucket=logs` 查看 |
| 存储成本报告需要"按访问频率分类" | 无此维度 | 报表显示 hot/warm/cold/archive 各占比例 |

失去了热度数据，所有分层决策只能基于：
- `created_at`：对象的年龄（与访问模式弱相关——一个 3 年前的文档可能每天被频繁读取）
- 人工标注（标签、前缀约定）：需要在写入时预知未来的访问模式

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/repository/repository.go:24-40` | `Object` 无 `LastAccessedAt`/`AccessCount` | 新增字段需要 migration `0025` |
| `internal/repository/sql_objects.go` | `objects` 表缺少 `last_accessed_at` 列 | 需要 column + index |
| `internal/service/file_crud.go:Get` | 调用 `GetObject` 后不更新访问时间 | 在 `s.emit(ctx, obj, EventAccessed)` 后添加 `s.repo.TouchAccessTime(ctx, objectID)` |
| `internal/service/file_features.go:Stat` | HEAD/Stat 同样不记录访问 | 同上 |
| `internal/service/file_crud.go:Put` | 覆盖写即更新 `updated_at`——但更新不等于访问 | |
| `internal/events/bus.go` | `EventAccessed` 类型已定义但零消费者 | 新增 `AccessTracker` 订阅者 |
| `internal/reconcile/lifecycle.go` | 生命周期规则仅 `updated_at` 维度 | 新增 `last_accessed_at > 90 days` 规则支持 |

### 推荐方案（概念级）

1. **延迟写入访问时间戳**：在 `Get`/`Stat` 路径中，以事件驱动方式异步更新 `last_accessed_at`（通过 EventBus 的 `EventAccessed` → `AccessTracker` 订阅者批量刷新），避免在请求热路径上增加同步 DB 写入。
2. **热度指标可观测化**：新增 `storage_access_age_days`（最后访问距今的天数分布）和 `storage_access_count`（30 天累积访问次数）作为 Prometheus 可观测指标。
3. **热度分类 API**：`GET /v1/admin/tier/{tenant}` 返回按热度分布的对象规模（hot/warm/cold/archive）。
4. **生命周期规则扩展**：`{ "action": "transition_to_ia", "condition": "last_accessed > 30d" }`。

### 边界情况

| # | 场景 | 预期行为 |
|---|------|---------|
| B1 | 批量扫描作业遍历整个 bucket 触发大量 GET | 应配置为不更新访问时间（可通过请求头 `X-Skip-Access-Tracking: true` 跳过） |
| B2 | 对象被 CDN 缓存长期服务——后端无 GET 请求 | 访问时间不更新是符合预期的——热度应反映"对后端存储的访问" |
| B3 | 按前缀读取（ListObjects）算不算"访问" | 不算。List 只返回元数据，不读内容。但 List 频率也值得追踪 |
| B4 | 版本化桶——读历史版本是否统计为当前版本的访问 | 历史版本的读应增加当前版本的访问计数 |

---

## 方向四：内容缓存控制与 CDN 集成层

### 现状

当前系统在 HTTP 响应中输出 `ETag` 和 `Last-Modified` 头，但既不设置缓存行为指令（`Cache-Control`），也不消费客户端请求中的缓存指令：

```go
// internal/api/rest/handler.go:handleRangeOrFull
func (h *Handler) handleRangeOrFull(w http.ResponseWriter, r *http.Request, rc io.ReadCloser, obj repository.Object) {
    w.Header().Set("Accept-Ranges", "bytes")
    w.Header().Set("ETag", `"`+obj.ETag+`"`)
    // ... 无 Cache-Control
    // ... 无 Expires
    // ... 无 Vary
}
```

S3 路径同样：

```go
// internal/api/s3compat/handler.go:writeObjectHeaders
func writeObjectHeaders(w http.ResponseWriter, contentType string, size int64, etag, lastModified, storageClass string, meta map[string]string) {
    // ... 无 Cache-Control
}
```

**PUT 路径同样不处理输入的 `Cache-Control` 请求头：**

```go
// internal/service/file_crud.go:PutOptions
type PutOptions struct {
    ContentType  string
    Metadata     map[string]string
    Tags         map[string]string
    ContentMD5   string
    StorageClass string
    // 无 CacheControl string
    // 无 Expires     string
}
```

### 影响分析

| 场景 | 当前行为 | 有缓存策略时的行为 |
|------|---------|-----------------|
| 同一图片被 1000 次 GET | 每次经过完整存储后端读取 + 网络传输 | CDN 边缘节点缓存，90%+ 命中率，延迟 <10ms |
| 对象更新后浏览器/CDN 仍然使用旧版本 | 无缓存失效机制——用户可能看到旧内容 | `Cache-Control: no-cache` 配合 ETag 条件请求确保新鲜度 |
| 静态前端资源（JS/CSS）通过 REST API 提供 | 每次请求到达后端 | 设置 `Cache-Control: public, max-age=31536000` 一年缓存 |
| 临时分享链接的有效期控制 | 无 `Cache-Control` → 浏览器可能缓存过期内容 | `Cache-Control: private, max-age=300` 5 分钟 |
| CDN 提供商需要缓存行为指令 | 返回头中无 `Cache-Control` → CDN 使用默认缓存策略（可能缓存私有内容） | 明确的 `Cache-Control` + `private`/`public` 指令 |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/api/rest/handler.go:handleRangeOrFull` | 不设置 `Cache-Control`/`Expires`/`Vary` | 需要根据对象元数据或 bucket 配置写入缓存头 |
| `internal/api/s3compat/handler.go:writeObjectHeaders` | 同上 | 同上 |
| `internal/service/file_crud.go:PutOptions` | 无 `CacheControl`/`Expires` 字段 | 接收并存储对象级缓存策略到元数据 |
| `internal/repository/repository.go:Object` | 无 `CacheControl`/`Expires` 字段 | 持久化缓存配置 |
| `internal/api/rest/dto.go:objectDTO` | JSON 响应无缓存字段 | 返回缓存状态给客户端 |
| `internal/events/bus.go` | 无缓存失效事件（`event: cache.invalidate`） | CDN purge 或 cache proxy purge 需要事件驱动 |
| `internal/config/config.go` | 无 `CACHE_*` 配置项 | 默认 TTL、每种 Content-Type 的默认策略 |

### 推荐方案（概念级）

1. **PUT 时接受 `Cache-Control` 和 `Expires`**：在 `PutOptions` 中添加缓存字段，存入对象元数据。
2. **GET 时输出存储的缓存头**：`handleRangeOrFull` 和 `writeObjectHeaders` 从元数据中读取缓存配置并输出。
3. **bucket 级默认缓存策略**：`BucketConfig` 增加 `DefaultCacheControl`/`DefaultMaxAge`，用于未单独设置缓存策略的对象。
4. **缓存失效事件**：新增 `EventCacheInvalidate` 类型，在对象更新/删除时广播，CDN 适配器可消费。
5. **CDN 适配器抽象**：`CdnPurgeProvider` 接口（CloudFront、Cloudflare、Akamai 等实现），挂接到 `EventCacheInvalidate` 事件。

### 边界情况

| # | 场景 | 预期行为 |
|---|------|---------|
| B1 | PUT 时设置 `Cache-Control: private`，CDN 不应缓存 | `private` 指令被原样输出——CDN 正确行为是不缓存 |
| B2 | 对象更新后 CDN 缓存未失效 | 通过 EventBus 广播 `cache.invalidate` 事件 → CDN adapter 发起 purge |
| B3 | 版本控制桶中不同版本的缓存策略 | 每个版本独立存储缓存策略 |
| B4 | 预签名 URL + 缓存控制 | 预签名 URL 内容应设置 `Cache-Control: private` 防止 CDN 缓存鉴权内容 |

---

## 方向五：跨租户对象分享与公网分享链接

### 现状

当前系统中对象严格按租户隔离：

```go
// internal/service/file_crud.go:Get
func (s *FileService) Get(ctx context.Context, tenant, bucket, key string) (io.ReadCloser, repository.Object, error) {
    tenant, bucket = defaults(tenant, bucket) // 默认租户
    obj, err := s.repo.GetObject(ctx, tenant, bucket, key) // WHERE tenant_id = ?
    // ...
}
```

跨租户访问的唯一方式是：
1. 调用方拥有目标租户的 API Key（不安全——API Key 是全权限，无法限定到单对象）
2. 目标对象是 `public-read` ACL（全公开——无时间限制、无访问控制）

缺少文件的"分享链接"功能——这是现代文件存储平台的基础能力。

### 影响分析

| 场景 | 当前方案 | 问题 |
|------|---------|------|
| 向客户发送一份 PDF 合同 | 复制到公共 bucket 或使用 presign URL | Presign URL 绑定到 storage backend（需 S3），不支持密码保护、访问次数限制 |
| 内部团队分享日志文件 | 共享 API Key + `sdk.Get` | API Key 权限过大，日志和敏感文件共用同一凭证 |
| 生成公开文档下载链接 | 通过反向代理暴露 | 无审计追踪、无过期、无撤销能力 |
| 向外部审计员提供访问权限 | 创建临时 API Key | 需要管理员操作，无法自助，无法限定到特定文件列表 |

当前 `PresignGet` 生成的 URL 也受限于 storage backend 支持（本地存储的 presign URL 通过签名实现，但绑定特定路径），并且默认 300 秒过期时间只能通过 `?expires` 参数延长——不适合需要长期有效或受密码保护的分享场景。

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/service/file_features.go:PresignGet` | 存储后端预签名 URL——无对象级分享 token | 需新增 `GenerateShareLink`/`ConsumeShareLink` 方法 |
| `internal/api/rest/router.go` | 无 `/v1/share/{token}` 路由 | 需新增分享链接消费路由（不经过 auth tenant 匹配） |
| `internal/service/file_crud.go:Get` | `tenant + bucket + key` 严格匹配 | 需要支持按 `share_token` 解析→映射到 `tenant + bucket + key` |
| `internal/repository/repository.go` | 无 `share_links` 表或接口 | 需要 `share_links` 表 + CRUD |
| `internal/auth/auth.go` | API Key + JWT + SigV4 三种认证 | 分享 token 是第四种认证方式——轻量级、自包含（如 HMAC 签名 URL） |
| `sdk/go/aerovault/client.go` | 无 `ShareFile`/`GetSharedFile` 方法 | SDK 需要消费端方法 |

### 推荐方案（概念级）

1. **`share_links` 表**：
   ```sql
   CREATE TABLE share_links (
       id          TEXT PRIMARY KEY,           -- 分享链接唯一 ID（UUID）
       token       TEXT NOT NULL UNIQUE,       -- 访问 token（HMAC-signed payload）
       tenant_id   TEXT NOT NULL,
       bucket      TEXT NOT NULL,
       key         TEXT NOT NULL,
       created_by  TEXT NOT NULL,              -- 创建者的 API key label
       password    TEXT,                       -- bcrypt hashed optional password
       expires_at  TIMESTAMP,
       max_access  INTEGER,                    -- 0 = unlimited
       access_count INTEGER DEFAULT 0,
       created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
   );
   ```
2. **分享链接生成 API**：`POST /v1/admin/share` — 接收 `{bucket, key, expires_in, password, max_access}` → 返回 `{token, url}`。
3. **分享链接消费路由**：`GET /v1/share/{token}` — 验证 token 签名（HMAC）→ 检查密码（如果设置了）→ 检查过期时间+访问次数→ 重定向或直接流式传输对象内容。该路由在 auth middleware **之前**注册，或使用专用的轻量级认证。
4. **审计追踪**：每次分享链接的访问记录到 `audit_log` 表（actor = `share:{token}`）。
5. **撤销**：`DELETE /v1/admin/share/{token}` 软删除分享记录。
6. **自包含 token 设计**：token 为 HMAC 签名的 JSON payload（`{share_id, tenant, bucket, key, exp, access_limit}`），服务端验证签名后可直接判断无需查库（减少 DB 压力，支持大规模分享链接场景）。

### 边界情况

| # | 场景 | 预期行为 |
|---|------|---------|
| B1 | 分享链接指向的对象在访问前已被删除 | 返回 `410 Gone` 或 `404 Not Found`（取决于过期对象是否保留分享记录） |
| B2 | 密码保护 + 多次错误密码 | 连续 5 次错误后临时锁定 15 分钟（防暴力破解） |
| B3 | 分享链接过期后对象被更新 | 即使对象已更新，过期链接仍返回 `410` |
| B4 | 创建分享链接时指定 `max_access=10`，第 10 次访问后 | 第 11 次访问返回 `410` 并记录最后一次访问 |
| B5 | 对象在分享有效期内被硬删除 | 分享链接的 Get 返回 404，同时清理分享记录（或在下次访问时惰性清理） |
| B6 | 分享链接的 origin 对象是版本化桶下的历史版本 | 分享应固定到特定版本 ID（如果创建时指定了 `?versionId`） |

---

## 总体收益总结

| # | 方向 | 实现预估工作量 | 预期收益 | 风险 |
|---|------|--------------|---------|------|
| **1** | API 版本契约与向后兼容性 | **M**（middleware + CI + 文档规范，约 3-4 个文件，200 行） | 🟢 保障平台 API 稳定性；客户端升级不再断裂；首次建立 API 工程规范 | 低（增量添加，不修改现有 handler） |
| **2** | 精细化成本感知速率限制 | **M**（ratelimit.go 重构 + 配置扩展，约 3 个文件，250 行） | 🟠 多租户公平性保障；防止一个恶意调用方耗尽配额；支持差异化定价 | 中（需迁移现有配置格式，渐进式兼容） |
| **3** | 对象访问热度追踪 | **S**（migration + 异步订阅者 + metrics，约 4 个文件，180 行） | 🟠 为自动分层奠定数据基础；存储成本优化决策数据化 | 低（非侵入式，事件驱动写入，不影响请求热路径） |
| **4** | 内容缓存控制与 CDN 集成 | **S**（PutOptions + handler 输出 + config，约 3 个文件，150 行） | 🟢 缓存命中时延迟 10-20ms（vs 后端 5-200ms）；带宽成本降低；S3 互操作性达标 | 极低（纯增量，仅添加请求头输出） |
| **5** | 跨租户分享链接 | **L**（迁移文件 + 新路由 + token 生成 + HMAC 签名，约 6 个文件，400 行） | 🔴 核心竞争力功能；对标主流文件分享平台；开箱即用的协作能力 | 中（涉及认证绕过路径——需安全审查） |

**建议实施顺序：** 方向 4 → 方向 1 → 方向 3 → 方向 2 → 方向 5

- **方向 4**（缓存控制）是最小投入、立即可见的性能收益
- **方向 1**（API 版本契约）是长期平台健康的基础设施投入，越早做越有价值
- **方向 3**（访问热度追踪）是成本优化的数据基建，尽快开始积累热度数据
- **方向 2**（精细化限流）需要一定设计但生产多租户部署必须先于方向 5
- **方向 5**（分享链接）是最大的产品特性，需要完整的 安全设计+前端配合
