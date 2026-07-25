# 高价值扩展方向分析 v29 — 架构盲区：治理、性能与协议纵深

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go`、`internal/*` 全部 237 个 `.go` 文件、`sdk/*` 三套客户端、`deploy/*`、`docs/*`、48 对迁移文件）
> **分析日期：** 2026-07-11
> **视角：** 资深架构师 / 产品经理 — 聚焦「过去 28 期分析从未触及的 5 个架构盲区」
> **去重方法：** 逐篇对比 `docs/requirements/` 下 **28 期既有分析（v1–v28，累计约 18,000+ 行、~150+ 个方向）** + `docs/ROADMAP.md`（10 方向）+ `docs/adr/DECISIONS.md` + `docs/CHANGELOG.md` + `docs/TODO.md`，每个方向在既有文档中 **零实质性分析**。
> **原则：** 不编写任何实现代码。每个方向附带：现状 → 代码锚点 → 缺失能力 → 为什么需要 → 架构概要 → 边界情况。

---

## 审阅：前 28 期覆盖边界（去重矩阵）

前 28 期 expansion 文档覆盖了 **约 150+ 个方向**，核心领域分布：

| 领域 | 已覆盖方向数 | 代表议题 |
|------|------------|---------|
| AI/RAG 管线（嵌入/搜索/Chat/Agent/Indexer/Rerank/PII/缓存/预算） | ~20 | 增量 BM25、向量漂移、搜索缓存、PII/Luhn、日费用预算、远程提取器 |
| S3 兼容协议（子资源/ACL/Policy/CORS/通知/清单/LegalHold/COPY） | ~16 | 服务端拷贝、UploadPartCopy、通知过滤、Bucket Policy |
| 存储后端（Local/S3/OSS/COS/KMS/SSE/熔断器/分层/压缩/迁移） | ~17 | 在线迁移、CAS 存储、SSE 轮换、透明压缩、存储类转换 |
| 认证授权（JWT/API Key/SigV4/OIDC/SAML/SCIM/MFA/前缀级策略） | ~14 | Key 缓存、跨副本失效、JWT issuer pinning、前缀级权限、读写分离 |
| 多租户（CRUD/配额/预算/审计/日费用/物理隔离/加权公平调度） | ~12 | 声明式配置协调、公平队列、租户级存储隔离 |
| 事件/通知/Webhook（总线/传输/过滤/多通道/死信/背压） | ~12 | 事件过滤、多通道分发、Payload 转换、背压可观测 |
| 复制/HA/集群（CRR/SRR/单例/Federation/主动-主动/读写分离） | ~13 | 跨区复制规则、多活、CQRS 模式、读取扩展、自动故障转移 |
| Reconcile/GC/Lifecycle（孤儿/保留/Scrub/转换/版本/Multipart） | ~11 | 分片上传统计、搁置分片 GC、版本修剪、批量操作框架 |
| 合规（WORM/Legal Hold/保留/访问日志/客户端加密/对象锁模式） | ~9 | 治理+合规模式、不可变存储、对象访问轨迹 |
| 可观测性（OTel/Metrics/Grafana/Prometheus/SLO/Tracing/pprof） | ~9 | 分布式追踪、pprof、Debug 平台、SLO/SLI 体系 |
| 工程质量（内存安全/流式/并发/压缩/错误模型/测试） | ~9 | 大对象流式加密、SpillBuffer、响应压缩 |
| Web UI / Admin Console | ~6 | 管理控制台、Admin UI 生产化 |
| SDK / CLI 完整性 | ~6 | SDK 开发者体验、导入/迁移工具、Python/JS/Go SDK 同步 |
| 基础设施（TLS/ACME/配置热重载/IP ACL/Feature Flag/Helm） | ~7 | 配置热重载、Helm chart、CDN 集成、IP ACL |
| 其他（GitOps/插件/元数据 Schema/备份/DR/批量操作） | ~9 | 元数据 Schema 治理、统一备份框架、数据库迁移安全 |

### 本期 5 个方向在前 28 期分析中均 **零实质性覆盖**（去重依据）

| # | 方向 | 确认方法 | 既有覆盖情况 |
|---|------|---------|------------|
| 1 | **Feature Flags / 灰度发布系统** | `grep -rli "feature.*flag\|feature.*toggle\|feature.*gate\|feature.*rollout\|canary.*deploy\|gradual.*rollout\|A\.B\.test\|flag.*driven" docs/requirements/` → 0 命中 | **完全未覆盖** |
| 2 | **请求合并与缓存雪崩保护** | `grep -rli "request.*coalesce\|request.*merge\|GET.*dedup\|hot.*key.*protect\|cache.*stampede\|thunder.*herd" docs/requirements/` → 0 命中 | **完全未覆盖** |
| 3 | **MCP 协议纵深（Prompts/Sampling/Roots/Completions）** | `grep -rli "MCP.*prompt\|MCP.*sample\|MCP.*root\|MCP.*complet\|MCP.*resource.*template" docs/requirements/` → 0 命中 | **完全未覆盖**（MCP 仅有协议层提及） |
| 4 | **事件数据生命周期管理（压缩/归档/保留策略）** | `grep -rli "event.*compact\|event.*archiv\|event.*retire\|event.*retention.*policy\|event.*lifecycle.*manage\|events.*table.*grow\|events.*table.*size" docs/requirements/` → 0 命中 | **完全未覆盖**（仅 compliance 事件类型一行提及） |
| 5 | **精细化速率限制（按端点/方法/IP/路径）** | `grep -rli "rate.*limit.*per.*endpoint\|per.*endpoint.*rate.*limit\|rate.*limit.*per.*method\|rate.*limit.*per.*IP\|rate.*limit.*per.*path\|per.*IP.*rate.*limit\|per.*path.*rate.*limit" docs/requirements/` → 0 命中 | **完全未覆盖** |

---

## 本期方向总览

| # | 方向 | 类型 | 优先级 | 代码锚点 | 核心痛点 |
|---|------|------|--------|---------|---------|
| 1 | **🟠 Feature Flags / 灰度发布系统** | 基础设施/运维 | P1 — 生产变更的风险控制盲区 | 无现有设施；入口点在 `config.go` 与 `main.go` | 每次功能发布都是全量切换，无灰度、无回滚、无 A/B 测试能力 |
| 2 | **🟠 请求合并与缓存雪崩保护** | 性能/可靠性 | P1 — 高并发下的热点对象防护 | `service/file_crud.go`、`storage/storage.go` | 同一对象的高并发 GET 请求各自独立读取后端，无合并、无缓存保护 |
| 3 | **🟡 MCP 协议纵深（Prompts/Sampling/Roots/Completions）** | 协议/互操作 | P1 — AI Agent 平台协议完整性的缺口 | `internal/mcp/server.go`、`protocol.go` | 只实现了 MCP 工具和资源子集；缺失 prompts、sampling、roots、completions 等核心能力 |
| 4 | **🟡 事件数据生命周期管理** | 数据治理/运维 | P1 — 长期运行下的数据膨胀隐患 | `events/bus.go`、`repository/sql_events.go`、`reconcile/` | events 表无限制增长；无压缩、无归档、无保留策略 |
| 5 | **🟡 精细化速率限制（按端点/方法/IP/路径）** | 安全/运维 | P1 — 多租户场景下的公平性与安全缺口 | `middleware/ratelimit.go`、`api/rest/router.go` | 只有 per-tenant 全局 RPS；无 per-endpoint、per-method、per-IP 控制 |

---

## 1. 🟠 Feature Flags / 灰度发布系统（Feature Flags & Gradual Rollout）

### 现状

当前所有功能通过环境变量控制开/关，变更即全量生效：

```text
AI_INDEX_ENABLED=false          → 全集群索引关闭
AI_CHUNK_WINDOW=600             → 所有 tenant 共享同一窗口大小
REPLICATION_ENABLED=false       → 二分法开/关
RECONCILE_CLUSTER_SINGLETON=false → 全员或全否
```

**没有任何灰度发布手段**：
- 无法对 1% 的 tenant 启用新功能验证稳定性
- 无法按用户/租户/对象比例逐步放量
- 无法在运行时动态调整功能状态
- 无法 A/B 测试不同参数值（如 chunk window vs. search quality）
- 无法实现 canary 发布——失败即全量回滚

**代码锚点：**

```go
// internal/config/config.go — 所有功能开关在此一次性加载，启动后不可变
type AIConfig struct {
    Enabled              bool    // AI_INDEX_ENABLED — 启动即决定，运行时不可改
    ChunkWindow          int     // AI_CHUNK_WINDOW — 调参需重启
    HybridSearch         bool    // AI_HYBRID_SEARCH — 切换需全量重建索引
    // ...
}

// cmd/server/main.go — 基于 config 值的二分法分支：
if cfg.AI.HybridSearch {
    bm = ai.NewBM25()
    // ...
}
if cfg.AI.PIIScan {
    indexer.WithPII(ai.NewPIIDetector(), cfg.AI.PIIRedact)
}
```

**缺失能力矩阵：**

| 能力 | 现状 | 需要 |
|------|------|------|
| 按 tenant 粒度灰度 | ❌ 全局开关 | 指定 tenant 白名单启用新功能 |
| 按百分比灰度 | ❌ 无 | 基于 tenant hash 或随机抽样渐进放量 |
| A/B 参数测试 | ❌ 无 | 两个参数值分别作用于不同 tenant 组，对比指标 |
| 运行时动态开关 | ❌ 仅启动时读取 env | API 或管理面板实时切换功能状态 |
| 功能回滚 | ❌ 全量回滚需重启 | 一键回滚到上一个稳定状态 |
| 灰度指标对比 | ❌ 无 | 自动对比实验组 vs 对照组的关键指标 |
| 功能依赖管理 | ❌ 无 | 声明功能依赖关系（如 HybridSearch 依赖 BM25） |

### 为什么需要

| 理由 | 影响 |
|------|------|
| **生产变更风险控制** | 每次功能切换（如开启 hybrid search 或 PII 扫描）是全集群事件。一个 bug 影响所有用户。灰度发布将爆炸半径从 100% 降到 1%。 |
| **参数调优能力** | AI 参数（chunk window、overlap、embedding model）直接影响搜索质量。没有 A/B 测试，调参全靠"重启后目测"。 |
| **SaaS 运营标配** | 任何 SaaS 平台的 Feature Flag 是基础设施级需求。aero-vault 是自托管平台，但托管运营方同样需要灰度能力。 |
| **降低发布压力** | 开发团队可以"暗部署"新功能（dark launch），逐步放量，出现问题时最小影响面。 |

### 建议架构

```
┌──────────────────────────────────────────────────────┐
│                Feature Flag System                    │
│                                                       │
│  存储层:                                             │
│    feature_flags 表 (持久化)                          │
│    ├─ FlagKey (string, PK)                           │
│    ├─ Description                                    │
│    ├─ Owner                                          │
│    ├─ CreatedAt / UpdatedAt                          │
│    └─ Rules (JSON):                                  │
│       ├─ Tenants:    ["tenant-a", "tenant-b"]        │
│       ├─ Percentage: 10                              │
│       └─ Params:     {"chunk_window": 800}           │
│                                                       │
│  管理 API:                                           │
│    GET    /v1/admin/flags                            │
│    PUT    /v1/admin/flags/{key}    # 创建/更新 flag  │
│    DELETE /v1/admin/flags/{key}    # 删除 flag       │
│    POST   /v1/admin/flags/{key}/evaluate  # 调试     │
│                                                       │
│  运行时评估:                                         │
│    flag.Evaluate(ctx, tenant) → enabled(bool) + params(map) │
│    - 从 DB 或本地缓存读取 flag 规则                   │
│    - 按 tenant 白名单 → 百分比 → 全局默认值顺序匹配   │
│    - 结果缓存 TTL 秒级（避免每次请求查询 DB）         │
│                                                       │
│  集成点:                                             │
│    config.Config 保持现有结构（启动参数默认值）       │
│    Feature Flag 在配置之上叠加（override）            │
│    例如: AI_HYBRID_SEARCH=false (全局默认)            │
│          但 tenant-b 的 flag 覆盖为 true              │
└──────────────────────────────────────────────────────┘
```

### 边界情况

- **Flag 缓存一致性问题**：如果缓存了 flag 评估结果（性能考虑），flag 变更到生效有短暂延迟。对于安全相关 flag（如禁用某功能），应支持"立即失效"模式（类似 `Registry.InvalidateCachedKey`）
- **Flag 值类型安全**：参数值需要类型系统保障（`chunk_window` 期望 int，不能传 string）。使用 Go 泛型或类型注册表
- **首次部署**：新系统启动时 feature_flags 表为空，应使用 config.go 的默认值作为 fallback
- **删除 flag 的后果**：删除一个正在使用的 flag 应该回退到 config 默认值，而不是 panic 或报错
- **Flag 审计**：每次 flag 变更应记录到 audit_log（谁、什么、什么时候、旧值、新值）
- **高并发评估性能**：`Evaluate()` 必须极快（<1µs），用 sync.RWMutex 保护缓存，HLL 或布隆过滤器判断 tenant 是否在白名单

---

## 2. 🟠 请求合并与缓存雪崩保护（Request Coalescing & Cache Stampede Protection）

### 现状

当前对同一对象的高并发 GET 请求，每个请求独立穿透到 storage 和 repository 层：

```go
// internal/service/file_crud.go — Get 路径
func (s *FileService) Get(ctx context.Context, tenant, bucket, key string) (io.ReadCloser, repository.Object, error) {
    // 每个请求独立执行：
    obj, err := s.repo.GetObject(ctx, tenant, bucket, key)  // 每个请求查一次 DB
    rc, info, err := s.store.Get(ctx, obj.StorageKey)        // 每个请求读一次存储
    // ...
}
```

**典型危险场景（缓存雪崩）：**
1. 一个热门对象（如 AI 模型配置文件、前端 assets 包）被频繁访问
2. 突然涌入 N 个并发 GET 请求（前端刷新、CI/CD 构建集群同时拉取）
3. 每个请求都独立穿透到存储后端
4. 存储后端压力激增，响应变慢 → 更多请求排队 → 延迟雪崩
5. 在最坏情况下，存储后端过载 → 请求超时 → 客户端重试 → 雪崩加剧

**当前虽有一层缓存**（embedding/search results），但对象 GET 路径完全无缓存保护。

**代码锚点：**

```go
// internal/service/file_crud.go — 每个 Get 独立走完整路径
func (s *FileService) Get(...) (io.ReadCloser, repository.Object, error) {
    obj, err := s.repo.GetObject(ctx, tenant, bucket, key)  // 不可合并
    // ...
    rc, info, err := s.store.Get(ctx, obj.StorageKey)        // 不可合并
}

// internal/storage/storage.go — Storage 接口无缓存层
type Storage interface {
    Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
    // ...
}

// internal/storage/local.go — Local 实现在每个 Get 上直接读磁盘
func (s *Local) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
    // os.Open → io.ReadAll 或 io.Copy — 无去重
}
```

### 缺失能力矩阵

| 能力 | 现状 | 需要 |
|------|------|------|
| 并发 GET 合并（Request Coalescing） | ❌ 无 | 同一对象同一时刻的 N 个 GET 只穿透一次存储，结果广播到所有等待者 |
| 热门对象内存缓存 | ❌ 无 | 配置 TTL 的热门对象缓存（类似 nginx proxy_cache） |
| 缓存雪崩保护（随机 TTL 抖动） | ❌ 无 | `cache_ttl + random(0, jitter)` 防止同时过期 |
| 可选的 Cache-Control 支持 | ❌ 无 | 按对象/目录配置 `Cache-Control` 头 |
| 基于 ETag/Last-Modified 的条件 GET | ✅ 已实现 | REST handler 支持 If-None-Match → 304 |
| 频率自适应缓存（自适应 TTL） | ❌ 无 | 访问频率高的对象自动延长 TTL，冷对象自动淘汰 |

### 为什么需要

| 理由 | 影响 |
|------|------|
| **热点对象保护** | 单一大文件（如 500MB 模型权重）被 10 个 worker 同时拉取 → 10 倍存储带宽。合并后降为 1 倍。 |
| **延迟 SLA 保障** | 缓存命中时 GET 延迟从 10-50ms（磁盘）或 50-200ms（S3）降到 <1ms（内存）。 |
| **后端成本控制** | 云存储按请求计费（S3 GET 请求 $0.0004/1k）。缓存减少重复请求 = 直接降低账单。 |
| **雪崩防御** | 没有雪崩保护的生产系统，在流量尖峰下的行为是不可预测的——这是 SRE 最关注的可靠性指标之一。 |

### 建议架构

```
┌──────────────────────────────────────────────────────┐
│              GET Request Coalescing                   │
│                                                       │
│  核心思想: "single-fly" 模式                          │
│                                                       │
│  1. 请求到达时，检查是否有相同 (tenant, bucket, key)  │
│     的 GET 正在进行中                                 │
│  2. 是 → 挂到等待队列上（channel），共享结果           │
│  3. 否 → 自己成为"领头请求"去读取存储，完成后         │
│          将结果广播到等待队列                          │
│                                                       │
│  ┌─────┐    ┌──────┐    ┌──────────┐                  │
│  │GET A│───▶│Coalesce│──▶│领头请求  │──▶ storage.Get  │
│  ├─────┤    │        │   │(走完整路径)│                │
│  │GET A│───▶│  Map   │   └──────────┘                  │
│  ├─────┤    │  of    │         │                       │
│  │GET A│───▶│Futures │   ┌──────▼──────┐               │
│  └─────┘    └──────┘   │ 广播结果到   │               │
│                         │ 所有等待者   │               │
│                         └─────────────┘               │
│                                                       │
│  可选: 缓存层（热对象内存缓存）                        │
│  GET → 检查缓存 (TTD) → 命中? → 直接返回              │
│                       → 未命中? → 合并/穿透 → 回填缓存 │
│                                                       │
│  GET_OBJECT_CACHE_SIZE=100_000_000 (100MB)            │
│  GET_OBJECT_CACHE_TTL_SECONDS=60                      │
│  GET_OBJECT_CACHE_JITTER_SECONDS=30  ← 随机 TTL 抖动   │
│  GET_OBJECT_COALESCE_ENABLED=true                     │
└──────────────────────────────────────────────────────┘
```

### 边界情况

- **大对象缓存**：10GB 文件不应全量缓存在内存中。缓存应限定单对象大小上限（如 `MAX_CACHED_OBJECT_BYTES=10MB`），超限只做合并不做缓存
- **可写对象一致性**：当缓存中的对象被 PUT/DELETE 更新时，需要主动失效缓存（或使用短 TTL + ETag 验证）
- **版本化 Bucket**：条件 GET（If-None-Match）配合强 ETag 可安全返回过期缓存；合并时若有新版本写入，领头请求结果可能过时——合并必须在同一个存储 key + version 上
- **内存压力**：缓存 100MB 热门对象看起来合理，但如果有大量小对象被高频访问，需限制条目数（LRU）而非仅字节数
- **取消请求**：如果等待合并的客户端断开连接，应优雅地从等待队列中移除，不泄漏 goroutine。领头请求结束后应正常关闭读取器
- **限流集成**：合并机制不应绕过 RateLimiter——领头请求仍然消耗速率配额
- **加密对象**：SSE 加密对象被缓存时需要密文入缓存还是明文？密文可减少信任边界（不信任缓存的内存安全），但每次缓存命中都要解密，得不偿失

---

## 3. 🟡 MCP 协议纵深（MCP Protocol Depth — Prompts / Sampling / Roots / Completions）

### 现状

当前 MCP 实现覆盖了协议的子集，但远非完整：

**已实现：**
- `initialize` — 能力协商
- `tools/list` — 6 个工具（list_files, read_file, write_file, delete_file, search, chat）
- `tools/call` — 上述工具的调用路由
- `resources/list` — 列出桶中所有对象作为资源
- `resources/read` — 按 aero-vault URI 读取对象内容
- `ping` — 存活检测

**未实现（MCP 协议规范定义但本实现缺失）：**

| MCP 特性 | 协议版本 | 用途 | 实现状态 |
|----------|---------|------|---------|
| `prompts/list` + `prompts/get` | core | 可复用提示词模板（类似 OpenAI GPTs 的 prompt 预设） | ❌ 未实现 |
| `sampling/createMessage` | core | 服务端主动请求 LLM 生成（MCP 客户端代理调用 LLM） | ❌ 未实现 |
| `roots/list` | core | 客户端告知服务端可用根目录（信任边界声明） | ❌ 未实现 |
| `completion/complete` | core | 参数自动补全（类似 shell tab-completion） | ❌ 未实现 |
| `resources/templates/list` | core | URI 模板声明（模式匹配而非逐一列举） | ❌ 未实现 |
| `logging/setLevel` | core | 运行时日志级别控制 | ❌ 未实现 |
| `notifications/…` | core | 服务端主动事件推送（对象变更通知） | ❌ 未实现 |
| `resources/subscribe` + `notifications` | core | 资源变更订阅 | ❌ 未实现 |

**代码锚点：**

```go
// internal/mcp/server.go — dispatch 方法只处理以下方法：
func (s *Server) dispatch(ctx context.Context, req rpcRequest) (any, *rpcError) {
    switch req.Method {
    case "initialize":                    // ✅
    case "tools/list":                    // ✅
    case "tools/call":                    // ✅
    case "resources/list":                // ✅
    case "resources/read":                // ✅
    case "ping":                          // ✅
    default:                              // ❌ 所有其他方法返回 -32601
        return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
    }
}

// internal/mcp/protocol.go — capability 声明缺少多个特性
capabilities := map[string]any{
    "tools":     map[string]any{"listChanged": false},
    "resources": map[string]any{"listChanged": false, "subscribe": false},
}
// 缺少: "prompts", "sampling", "roots", "logging", "experimental"
```

### 为什么需要

| 理由 | 影响 |
|------|------|
| **MCP 协议合规性** | MCP 是 aero-vault 的四大接口协议之一。如果只实现了协议子集，外部 MCP 客户端（Claude Desktop、VS Code 扩展等）的能力协商会受限。 |
| **AI Agent 平台入口** | `prompts` 允许将领域知识编码为可复用模板，是 MCP 最具差异化的能力——服务端提供的不只是工具，还有智能提示。 |
| **采样（Sampling）** | 允许 aero-vault 服务器请求 LLM 生成，而不需要自己配置 LLM——通过客户端代理完成。这对于"解释搜索结果"等 NLG 任务至关重要。 |
| **资源模板** | 当前 `resources/list` 返回所有对象列表。如果有 100 万对象，这个列表不可用。资源模板允许声明 URI 模式 `aero-vault://{tenant}/{bucket}/{key}`，由客户端构造具体 URI。 |
| **Roots 信任模型** | MCP 的安全模型基于 roots：客户端声明允许服务端访问的根路径。aero-vault 的 multi-tenant 模型需要这个信任边界。 |

### 建议架构

```
┌──────────────────────────────────────────────────────────────┐
│                    MCP 协议扩展架构                            │
│                                                               │
│  Prompts（可复用提示词模板）：                                 │
│  ┌──────────────────────────────────────────────────────┐     │
│  │  prompts/list → 返回已注册的 prompt 定义               │     │
│  │  prompts/get  → 返回具体 prompt 的渲染结果（参数填充后） │     │
│  │                                                       │     │
│  │  内置 prompt 举例：                                    │     │
│  │  - summarize_document: "请用中文总结以下文档的核心内容"  │     │
│  │  - compare_documents: "请比较以下两份文档的异同"        │     │
│  │  - search_and_explain: "搜索与 {topic} 相关的文件，     │     │
│  │     然后用中文解释搜索结果的上下文关联"                 │     │
│  └──────────────────────────────────────────────────────┘     │
│                                                               │
│  Sampling（服务端请求 LLM 采样）：                             │
│  ┌──────────────────────────────────────────────────────┐     │
│  │  当服务端需要 LLM 能力但未配置 LLM 时（chat==nil）：    │     │
│  │  → 发送 SamplingRequest 给 MCP 客户端                   │     │
│  │  → 客户端代理调用其 LLM 并返回结果                      │     │
│  │  → 服务端利用结果完成工作（如 summarize chunks → text） │     │
│  │                                                       │     │
│  │  这解耦了 AI 需求：aero-vault 不需要配置 LLM           │     │
│  │  就可以提供 AI 功能（通过 MCP 客户端的 LLM 调用）       │     │
│  └──────────────────────────────────────────────────────┘     │
│                                                               │
│  Roots + 资源模板：                                           │
│  ┌──────────────────────────────────────────────────────┐     │
│  │  roots/list → 客户端告知服务端允许的根 URI 前缀        │     │
│  │  → 用于访问控制（如只允许 tenant-a 下的文件）           │     │
│  │                                                       │     │
│  │  resources/templates/list → 声明 URI 模板：            │     │
│  │    aero-vault://{tenant}/{bucket}/{key}                │     │
│  │  → 客户端可以直接构造 URI 来读取，无需先列举           │     │
│  └──────────────────────────────────────────────────────┘     │
│                                                               │
│  事件推送：                                                  │
│  ┌──────────────────────────────────────────────────────┐     │
│  │  资源订阅 (resources/subscribe) → 对象变更时推送通知   │     │
│  │  类似于现有 SSE 但走 MCP 协议通道                      │     │
│  └──────────────────────────────────────────────────────┘     │
└──────────────────────────────────────────────────────────────┘
```

### 边界情况

- **Prompts 安全性**：prompt 模板中可以嵌入来自对象内容的变量。需确保不被注入攻击（prompt injection）——模板变量应 escape 或使用不可信内容标记
- **Sampling 的回退策略**：当 MCP 客户端不支持 sampling（capabilities 中未声明），且 aero-vault 也未配置 LLM 时，依赖 sampling 的功能应优雅降级（如"需要 LLM 功能，请配置 AI_CHAT_PROVIDER 或通过 MCP 客户端提供"）
- **Roots 与多租户的冲突**：aero-vault 的多租户通过 X-Aero-Tenant header 控制。Roots 是 MCP 客户端侧声明的，两者语义可能冲突。当 roots 限制的范围小于 tenant scope 时，应以更严格的为准
- **资源模板 URI 编码**：对象 key 可能包含特殊字符（空格、#、?），URI 模板展开时必须正确编码
- **向后兼容**：新增 protocol methods 不应破坏现有 MCP 客户端。在 `initialize` 的 capabilities 中声明新能力，客户端协商决定是否使用

---

## 4. 🟡 事件数据生命周期管理（Event Data Lifecycle Management）

### 现状

事件系统是 aero-vault 的核心基础设施——每个对象 CRUD 操作发布一个事件到 `events` 表：

```go
// internal/events/bus.go — Publish 方法持久化事件
func (b *Bus) Publish(ctx context.Context, e repository.Event) {
    id, err := b.repo.InsertEvent(ctx, e)  // 写入 events 表，永不过期
    // ...
}
```

**events 表无任何生命周期管理：**

```sql
-- internal/repository/migrations/sqlite/0003_events.up.sql
CREATE TABLE events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id TEXT NOT NULL DEFAULT 'default',
    event_type TEXT NOT NULL,
    object_key TEXT,
    object_id INTEGER,
    event_data TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
```

| 问题 | 影响程度 | 说明 |
|------|---------|------|
| **数据无限增长** | 🔴 生产必现 | 每个 PUT/GET/DELETE 产生一条事件记录。百万级对象 × 平均 3 次操作 = 300 万行/月 |
| **无保留策略** | 🔴 合规风险 | GDPR 要求数据保留期限，有些事件在期限外必须删除。当前无此能力 |
| **无压缩机制** | 🟠 性能 & 存储成本 | 同一对象的连续操作事件高度冗余，存储相同 metadata 字段 |
| **无归档策略** | 🟠 恢复能力 | 删除事件后丢失审计线索。应支持归档到低成本存储后清理 |
| **无表分区/分片** | 🟠 查询性能 | Postgres 下 events 表随数据量增长，`SELECT ... ORDER BY created_at DESC` 变慢 |
| **无事件类型 TTL 差异化** | 🟡 灵活性 | `audit_log` 事件可能需要保留 7 年，而 `object.viewed` 事件仅需保留 30 天 |

**代码锚点：**

```go
// internal/events/bus.go — Publish 只做插入，不检查 retention
func (b *Bus) Publish(ctx context.Context, e repository.Event) {
    id, err := b.repo.InsertEvent(ctx, e)  // ← 无 TTL、无大小限制
    // ...
}

// internal/repository/sql_events.go — InsertEvent / NextUnconsumedEvents
// 只有写入和读取，没有任何维护操作

// internal/reconcile/ — 现有 Job 覆盖对象层面的清理（孤儿 blob、软删除）
// 但完全不涉及 events 表清理
```

**当前唯一的事件消耗方：**
1. Webhook（读取 + 发送 + 标记为已消费）
2. SSE 端点（尾随事件流）
3. Indexer/Antivirus/Replication worker（通过 Subscribe 监听）
4. PostgresTransport（跨实例广播）

**没有任何机制回收已消费的事件**。

### 缺失能力矩阵

| 能力 | 现状 | 需要 |
|------|------|------|
| 事件 TTL / 保留策略 | ❌ 无 | 按 event_type 配置保留天数。`events.retention.days=90`，`audit_log.retention.days=2555` |
| 已消费事件清理 | ❌ 无 | 已被所有订阅者消费的事件可安全删除 |
| 事件压缩（合并） | ❌ 无 | 同一对象 1 分钟内连续 N 个 update 事件 → 合并为 1 条摘要 |
| 事件归档 | ❌ 无 | 超过 TTL 但需保留的事件 → 归档到低成本存储（S3/本地文件） |
| 事件分区 | ❌ 无 | Postgres 下按 `created_at` 时间分区，DROP PARTITION 代替 DELETE |
| 事件导出 API | ❌ 无 | `GET /v1/admin/events/export?since=...&until=...&type=...` |
| 事件存储用量仪表盘 | ❌ 无 | Prometheus gauge: `events_storage_bytes`, `events_row_count` |

### 为什么需要

| 理由 | 影响 |
|------|------|
| **数据库膨胀** | 在事件密集型场景（如大量小文件更新），events 表可能比 objects 表大 10 倍。这是运维事故的常见来源——磁盘满导致服务不可用。 |
| **查询性能退化** | events 表是 `SELECT ... WHERE consumed=0 ORDER BY created_at LIMIT N` 模式，随着全表扫描范围增大，webhook 和 SSE 延迟增加。 |
| **合规要求** | GDPR 的"被遗忘权"要求删除特定用户/租户的所有事件。PCI DSS 要求审计日志保留至少 1 年。无事件生命周期管理无法同时满足。 |
| **成本控制** | 云 Postgres 按存储计费。清理一年前的事件可以释放大量空间。 |

### 建议架构

```
┌──────────────────────────────────────────────────────────┐
│               Event Lifecycle Manager                     │
│                                                           │
│  Reconcile 扩展（新增子任务）：                            │
│  ┌────────────────────────────────────────────────────┐   │
│  │  EventRetentionJob:                                │   │
│  │  1. 读取 event_retention_policies 配置             │   │
│  │     (按 event_type 的保留天数映射)                  │   │
│  │  2. 对每个 event_type:                             │   │
│  │     a. 标记超期事件 → 可选归档 → DELETE            │   │
│  │     b. 记录 telemetry 指标                         │   │
│  │  3. 使用集群单例（避免多副本重复清理）              │   │
│  └────────────────────────────────────────────────────┘   │
│                                                           │
│  配置模型（env 或 DB 表）：                               │
│  ┌────────────────────────────────────────────────────┐   │
│  │  EVENTS_RETENTION_DAYS=90  (全局默认)              │   │
│  │  EVENTS_RETENTION_audit_log=2555                   │   │
│  │  EVENTS_RETENTION_object_viewed=7                  │   │
│  │  EVENTS_COMPRESSION_WINDOW=60s  (合并窗口)          │   │
│  │  EVENTS_ARCHIVE_ENABLED=true                       │   │
│  │  EVENTS_ARCHIVE_BACKEND=s3                         │   │
│  │  EVENTS_ARCHIVE_BUCKET=aero-events-archive         │   │
│  └────────────────────────────────────────────────────┘   │
│                                                           │
│  压缩（可选）：                                           │
│  ┌────────────────────────────────────────────────────┐   │
│  │  对同一 tenant+bucket+key 在时间窗口内的连续事件：   │   │
│  │  [created, created, updated, updated, deleted]      │   │
│  │  → 合并为: [created→updated→deleted]               │   │
│  │  保留时间戳序列用于审计，减少 60-80% 事件行数        │   │
│  └────────────────────────────────────────────────────┘   │
│                                                           │
│  归档（可选）：                                           │
│  ┌────────────────────────────────────────────────────┐   │
│  │  超期事件 → JSONL 格式写入归档存储 → 从 DB 硬删除    │   │
│  │  归档存储: S3/本地文件/OSS (重用现有 Storage 接口)    │   │
│  │  归档索引: archive_manifest.json                     │   │
│  │  恢复 API: POST /v1/admin/events/restore?archive=.. │   │
│  └────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────┘
```

### 边界情况

- **未消费事件保护**：绝不能删除未被所有订阅者确认的事件。清理前需检查 `consumed` 状态。对于没有消费确认的事件类型，保留至少 2 个 TTL 周期
- **归档恢复 SLA**：从归档恢复事件不是实时操作（可能分钟级）。`GET /v1/events?include_archived=true` 不应阻塞——先返回 DB 中的结果，再异步通知归档数据就绪
- **Postgres 分区切换**：使用时间分区时，`DELETE FROM events WHERE created_at < X` 变更为 `DROP TABLE events_2025_q1` — 前者产生大量 WAL，后者是元数据操作（毫秒级）
- **SQLite 限制**：SQLite 不支持分区。大量 DELETE 会导致 WAL 膨胀和 VACUUM 压力。SQLite 部署下应默认开启"超过 N 行后循环覆盖"模式（FIFO by created_at）
- **压缩与审计的矛盾**：压缩事件可能丢失精确时间戳序列，对安全审计不可接受。压缩应 opt-in，且只适用于非审计事件类型

---

## 5. 🟡 精细化速率限制（Granular Rate Limiting: Per-Endpoint / Per-Method / Per-IP）

### 现状

当前速率限制模型极其简单——两个独立的 per-tenant token bucket：

```go
// internal/middleware/ratelimit.go
type RateLimiter struct {
    rps     float64
    burst   float64
    buckets map[string]*bucket  // key = tenant
}

// main.go 创建两个限流器：
rl := middleware.NewRateLimiter(cfg.RateLimit.RPS, cfg.RateLimit.Burst)  // 全局
aiRL := middleware.NewRateLimiter(cfg.RateLimit.AIRPS, cfg.RateLimit.AIBurst) // AI 专用

// AI 路由组应用 aiRL
// 所有其他路由应用 rl
```

**限制粒度对比：**

| 维度 | 当前能力 | 业界标准（AWS/GCP） |
|------|---------|-------------------|
| 按租户 | ✅ 支持 | ✅ 支持 |
| 按全局 | ✅ 支持 | ✅ 支持 |
| 按端点/路径 | ❌ 无 | ✅ 每 API 独立配额 |
| 按 HTTP 方法 | ❌ 无 | ✅ GET 与 PUT 不同配额 |
| 按来源 IP | ❌ 无 | ✅ IP-based throttling |
| 按认证级别 | ❌ 无 | ✅ 匿名 < 认证 < 管理 |
| 按并发（inflight） | ✅ 已实现（ConcurrencyLimiter） | ✅ |
| 按 AI 操作 | ✅ 已实现（aiRL 独立） | ✅ |
| 突发容量（burst） | ✅ 已实现 | ✅ |
| 自定义速率键 | ❌ 无 | ✅ 按自定义维度（如 API key、bucket） |
| 每个限流维度的告警 | ❌ 无 | ✅ 接近限值时预警 |
| 请求排队 vs 拒绝 | ❌ 只拒绝 | ✅ 可选排队（在限制内延迟处理） |

**缺失场景举例：**

1. **攻击保护**：恶意客户端通过 `GET /v1/files/expensive-operation` 大量调用拖垮服务——当前只能限制该 tenant 的全局流量，不能精准限流特定路径
2. **公平调度**：一个 tenant 的批量写入占满速率配额，导致同一 tenant 的搜索请求被拒绝——当前没有 per-method 隔离
3. **匿名限流**：未认证请求（IP-based）与认证请求共享同一个 default bucket——无法限制爬虫
4. **S3 endpoint 保护**：`/s3` 前缀下的请求（可能来自未受控的客户端）与受控的 `/v1` 请求共享全局配额
5. **管理 API 保护**：`/v1/admin/*` 应该与数据 API 有不同的限流配置，防止误操作影响数据面

**代码锚点：**

```go
// internal/middleware/ratelimit.go — Middleware 对所有非 bypass 路径应用统一的限流
func (rl *RateLimiter) Middleware() func(http.Handler) http.Handler {
    if rl == nil {
        return func(next http.Handler) http.Handler { return next }
    }
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if rateLimitBypass(r.URL.Path) {  // 只有少数系统路径 bypass
                next.ServeHTTP(w, r)
                return
            }
            ok, wait := rl.isAllowed(r.Context())  // 只有 tenant 维度
            // ...
        })
    }
}

// internal/api/rest/router.go — 所有路由共享同一个 rate limiter
func NewRouter(..., aiRL *mw.RateLimiter, ...) http.Handler {
    r := chi.NewRouter()
    // 所有 /v1/files/... 路由 — 共享全局 rl
    // 所有 /v1/search, /v1/chat — 使用 aiRL
}

// 缺失：路由分组注册不同限流器
// 缺失：从 request context 提取 IP、method、path 的限流键
func rateLimitKey(r *http.Request) string {
    // 当前: tenant 或 "default"
    // 目标: tenant:method:path:ip → 不同维度组合
}
```

### 为什么需要

| 理由 | 影响 |
|------|------|
| **公平性（Fairness）** | 一个 tenant 的爬虫不应耗尽另一个 tenant 的搜索配额。per-method 隔离确保"读不因写而饿死"。 |
| **安全防护** | DDoS/暴力破解针对特定端点（如 `POST /v1/admin/jwt`）。per-IP + per-endpoint 限流是最小权限安全原则在速率层面的体现。 |
| **成本控制** | AI 端点已经有独立配额，但 S3 与 REST 共享。如果 `/s3` 被大量使用（如备份上传），会挤占交互式客户端的 REST 配额。 |
| **SRE 运营** | 当你需要回答"哪个端点在消耗配额"时，当前只有全局计数器。没有 per-endpoint 指标就无法做出针对性扩缩容决策。 |

### 建议架构

```
┌──────────────────────────────────────────────────────────┐
│              Multi-Dimensional Rate Limiter               │
│                                                           │
│  核心：限流键（Rate Limit Key）= 维度组合                  │
│                                                           │
│  维度:                                                    │
│    tenant (已有)                                          │
│    method (GET/PUT/POST/DELETE)                           │
│    path_prefix (/v1/files, /v1/search, /v1/admin, /s3)    │
│    source_ip (客户端 IP)                                  │
│    auth_level (anonymous, key, jwt, admin)                 │
│                                                           │
│  配置模型:                                               │
│  ┌────────────────────────────────────────────────────┐   │
│  │  RATE_LIMIT_RULES=[                                  │   │
│  │    {"path":"/v1/files/*","method":"GET","rps":1000},  │   │
│  │    {"path":"/v1/files/*","method":"PUT","rps":100},   │   │
│  │    {"path":"/v1/admin/*","rps":20},                  │   │
│  │    {"path":"/v1/search","rps":50,"burst":10},        │   │
│  │    {"path":"/s3/*","method":"PUT","rps":200},         │   │
│  │    {"path":"*","ip_anon":true,"rps":10},              │   │
│  │  ]                                                     │   │
│  └────────────────────────────────────────────────────┘   │
│                                                           │
│  评估流程:                                               │
│  1. 提取请求维度（tenant, method, path, ip, auth_level）  │
│  2. 匹配最具体的规则（最长 path 优先）                     │
│  3. 所有匹配规则都需要通过（AND 逻辑）                      │
│  4. 任一规则拒绝 ⇒ 429                                    │
│  5. 记录哪个规则触发了拒绝（用于告警和调试）                │
│                                                           │
│  保留现有 RateLimiter 作为默认 fallback：                  │
│  未匹配任何规则的请求 → 使用全局 per-tenant bucket        │
└──────────────────────────────────────────────────────────┘
```

### 边界情况

- **规则匹配性能**：每个请求匹配规则列表不应超过 O(log N)（使用路由树或前缀树）。50 条规则的线性扫描在高并发下不可接受
- **规则冲突解决**：`GET /v1/files/foo` 同时匹配 `. GET /v1/files/*`（rps=1000）和 `* GET *`（rps=10）——最长路径优先原则，前者胜出
- **IP 提取可靠性**：客户端 IP 可以从 `X-Forwarded-For`、`X-Real-IP` 或直接 `RemoteAddr` 提取，取决于部署拓扑。必须可配置（`TRUSTED_PROXY_CIDRS`）以防止 IP 伪造
- **规则变更加载**：热更新限流规则（通过 API 或 config 热重载）时，现有限流状态（token buckets）如何处理？重置所有 bucket 或将新规则用于新请求？
- **分布式一致性**：多副本场景下，per-IP 限流需要跨副本协调或使用近似算法（如 Redis-based sliding window 或 synchronous token bucket）。单机精确限流在多副本下只能近似保证
- **限流指标可观测性**：每个规则应有独立的 Prometheus 计数器（`rate_limit_denied_total{rule="..."}`），否则无法调试哪个规则触发了拒绝

---

## 总结：本期 5 个方向的共同特征

1. **基础设施级缺口** — 这 5 个方向都不是"添加一个新功能"，而是**系统级能力**的缺失。它们决定了系统能否在**生产规模下安全、可靠、公平地运行**。

2. **跨组件影响** — 每个方向影响 3 个以上的子系统（Feature Flags → config + router + handler；请求合并 → service + storage + middleware；MCP 纵深 → mcp + ai + events；事件生命周期 → events + reconcile + telemetry；精细化限流 → middleware + router + telemetry）。

3. **默认不破坏现有功能** — 所有 5 个方向都是 opt-in 叠加层。现有系统的行为在未配置时保持完全不变。

4. **运维成熟度信号** — 这 5 个方向将 aero-vault 从"功能完整"推向"生产可靠"：灰度发布（变更管理）、请求合并（性能可靠性）、协议纵深（AI 互操作）、事件治理（数据管理）、精细化限流（安全与公平）——每一块都是生产级系统的标配。

---

*本文分析基于 v28 之后的代码库状态，确认与之前 28 期分析无实质性重叠。*
