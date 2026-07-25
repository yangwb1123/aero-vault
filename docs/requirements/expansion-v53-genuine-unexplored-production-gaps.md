# AeroVault 高价值扩展方向 v53 — 经 52 轮分析后未被触及的生产级盲区

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部子包，~55K `.go` + 三套 SDK + `deploy/*` + 全部 24 对迁移文件 + 全部 52 份既有 `docs/requirements/expansion-*.md` 分析文档 + `ROADMAP.md` + `CHANGELOG.md` + `TODO.md` + `extensions*.md` + `docs/analysis-*.md` + `AGENTS.md`）
>
> **分析视角：** 资深架构师 / 产品经理 — 在累计 **52 期 expansion 分析（260+ 方向，~650,000+ 字分析文本）** 基础上，寻找 **52 轮穷举后依然未被触及** 的真实架构盲区与生产级缺口
>
> **去重方法：** 对 `docs/requirements/` 下全部 52 份既有分析文档（v1–v52）+ `ROADMAP.md` + `CHANGELOG.md` + `TODO.md` + `extensions*.md` + `docs/analysis-*.md` 进行穷尽式关键词验证。每个方向在既有文档中 **零实质性独立架构分析**（即：不在核心方向列表中作为独立小节/独立方向出现；仅表格一行过路引用、举例提及、单一子点均不构成实质性分析）。
>
> **分析日期：** 2026-07-10

---

## 前言

经 52 期、260+ 方向的穷举分析，AeroVault 从功能维度、执行层维度、交叉架构维度、产品成熟度、生产就绪度、操作完整性与 S3 语义深度等多个视角已被反复扫描。几乎每个可想象的功能方向都被触及。

然而，在代码库的深层执行路径与默认后端的运行时行为中，依然存在一批 **从未被作为独立架构方向实质性分析** 的盲区。它们的共同特征是：

1. **不是"加一个端点"，而是"已有的基础设施缺少一个关键的安全/运营/产品层"**
2. **涉及跨组件、跨层（配置→运行→审计）的系统级能力**
3. **每个方向都对应着真实的生产故障场景或企业采购门槛**
4. **每个方向在 v1–v52 中零实质性独立架构分析**

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心矛盾 | 52 期覆盖验证 |
|---|------|------|--------|---------|-------------|
| **1** | **外部密钥存储集成：从环境变量到 Secret Store 的跃迁** | 安全/运维 | **P1** — SSE 密钥已通过 `SecretProvider` 接口优雅抽象（支持 keyfile/KMS/HTTP 三种 provider），但 **所有其他凭据**（S3/OSS/COS 密钥、AI API Key、Webhook Secret、JWT Secret、DB 密码）均为明文环境变量，无外部密钥存储集成 | ❌ **零实质性分析**（v25 在两处 SecretProvider 上下文提及"secrets manager"但仅做方向表格一行路过引用，从未作为独立架构方向展开） |
| **2** | **对象元数据搜索与结构化查询引擎** | 产品能力/可用性 | **P1** — 系统拥有丰富的元数据层（tags、custom metadata、storage_class、size、content_type、timestamps）且全部持久化到 SQL 中，但 **没有任何索引化的结构化搜索能力**——用户无法通过 tag 过滤、无法按 size 范围查询、无法组合条件、无法对 metadata 字段排序。语义搜索（RAG）与元数据搜索之间存在完全断裂 | ⚠️ v12 方向表格内一行提及"metadata 字段搜索"、v27 以一个 ~15 行子节覆盖了基础概念但**从未作为独立方向做完整架构分析**（无实施方案、无边界情况、无性能模型） |
| **3** | **存储后端自适应过载保护与反压机制** | 可靠性/性能 | **P1** — 当前限流层仅在 HTTP 中间件层（`middleware/ratelimit.go` 每租户 token-bucket），**存储后端层面完全无反压**。当 S3 延迟升高到 5s，系统不会主动降低对此后端的并发度；写入洪水不会让读取优先；存储断路器仅追踪错误率而非吞吐量。生产环境下，后端延迟抖动会直接传播为 HTTP 层雪崩 | ❌ **零实质性分析**（v36 方向二覆盖"静态 QoS"——IOPS 预留/优先级队列，但静态配置与动态自适应反压在架构上完全不同，v36 的分析不涉及本节提出的自适应问题） |
| **4** | **跨协议请求生命周期一致性与统一可观测性** | 运维/可靠性 | **P2** — REST、S3、WebDAV、MCP 四协议共享同一 `FileService`，但它们的请求生命周期完全隔离：无统一 trace ID 传播、无跨协议请求关联、无一致性的 stale-read 检测、无跨协议幂等键共享。运维人员无法回答"用户通过 S3 PUT 的对象，为什么 REST GET 返回 404？"——因为两者之间无端到端的 trace | ⚠️ v19 覆盖了四协议**响应格式与错误码**的一致性，v51 方向五覆盖了**输入侧上下文传播**（auth/precondition/idempotency）。两者均**未覆盖跨协议请求全生命周期的可观测性与一致性保障** |

---

## 方向一：外部密钥存储集成（External Secret Store Integration）

### 现状

当前系统的密钥管理状况（按安全等级分类）：

| 密钥/凭据 | 存储方式 | 轮换支持 | 审计 | 访问控制 | 安全等级 |
|-----------|---------|---------|------|---------|---------|
| SSE 加密主密钥 | ✅ `SecretProvider` 接口：keyfile / HTTP / KMS | ✅ 版本化 + `RewrapStale` | ❌ | ✅ 文件权限或 KMS IAM | 🟢 良好 |
| S3 / OSS / COS 凭据 | ❌ 明文环境变量 `.env` 或 `export` | ❌ 需重启进程 | ❌ | ❌ 可被任意进程读取 | 🔴 危险 |
| AI Embedder / LLM / Reranker API Key | ❌ 明文环境变量 | ❌ 需重启进程 | ❌ | ❌ 可被任意进程读取 | 🔴 危险 |
| Webhook HMAC Secret | ❌ 明文环境变量 `EVENTS_WEBHOOK_SECRET` | ❌ 需重启进程 | ❌ | ❌ 可被任意进程读取 | 🔴 危险 |
| JWT Signing Secret | ❌ 明文环境变量 `AUTH_JWT_SECRET` | ❌ 需重启进程 | ❌ | ❌ 可被任意进程读取 | 🔴 危险 |
| SigV4 Credentials | ❌ 明文环境变量 `AUTH_SIGV4_CREDENTIALS` | ❌ 需重启进程 | ❌ | ❌ 可被任意进程读取 | 🔴 危险 |
| DB DSN（含密码） | ❌ 明文环境变量 `DB_DSN` | ❌ 需重启进程 | ❌ | ❌ 可被任意进程读取 | 🔴 危险 |
| 预签名 URL Sign Key | ❌ 明文环境变量 `STORAGE_LOCAL_SIGN_KEY` | ❌ 需重启进程 | ❌ | ❌ 可被任意进程读取 | 🔴 危险 |

**核心矛盾：** 系统已经为 SSE 密钥构建了精致的 `SecretProvider` 抽象（`internal/storage/secret.go`），支持四种 provider（env、keyfile、HTTP、KMS）、版本化轮换、后台自动重包装。但同样的能力 **完全没有延伸到其他凭据**。所有其他凭据以明文形式暴露在进程环境中，被 `os.Getenv` 读取后常驻内存直到进程退出。

### 为什么需要

| 场景 | 当前风险 | 理想行为 |
|------|---------|---------|
| **运维审计**：谁在何时访问了 S3 凭据？ | 无从得知——凭据只是内存中的一个字符串 | 每次凭据获取都记录审计日志（谁、什么服务、何时） |
| **凭据轮换**：S3 Access Key 每 90 天必须轮换 | 需修改 .env 文件 + 重启进程 = 停机 | Secret Store 读取最新版本即可，零停机 |
| **密钥泄露**：.env 文件被误提交到 Git 仓库 | 所有凭据一次性泄露，影响面极大 | 凭据从不落入文件系统，泄露面极小 |
| **多环境隔离**：开发/测试/生产使用不同凭据 | 靠不同的 .env 文件区分，极易混淆 | Secret Store 的路径/标签天然隔离环境 |
| **K8s 部署**：Secrets 以环境变量注入 Pod | 明文存在于 Pod spec、etcd、节点文件系统 | 通过 CSI 或 Sidecar 注入内存，不留文件痕迹 |

### 当前架构中的现成切入点

系统已经在 `internal/storage/secret.go` 中定义了 `SecretProvider` 接口：

```go
type SecretProvider interface {
    Resolve(ctx context.Context, kid string) ([]byte, error)
    Primary(ctx context.Context) (string, []byte, error)
}
```

当前它仅用于 SSE 密钥。**扩展方向：**

```go
// 将 SecretProvider 提升为通用接口，所有凭据共享同一抽象
type CredentialProvider interface {
    // Resolve returns the credential value for the given path/key id.
    // Example paths:
    //   "secret/storage/s3/access_key"
    //   "secret/ai/openai/api_key"
    //   "secret/auth/jwt/signing_key"
    Resolve(ctx context.Context, path string) (string, error)
}
```

**实现策略（复用现有代码基础设施）：**

| 层次 | 现有资产 | 复用方式 |
|------|---------|---------|
| **接口定义** | `storage/secret.go` 的 `SecretProvider` | 泛化为 `Provider`，SSE 作为其一个消费者 |
| **Provider 实现** | `envProvider`（单 key） / `keyfileProvider`（JSON keyring） / `httpProvider`（Vault KV） / `kmsProvider`（远程 wrap） | 每个实现可直接复用（env 用于 dev fallback，http 用于 Vault） |
| **自动轮换** | `storage/secret.go` 的 `TickerProvider` 后台刷新 | 直接复用：所有凭据获取走 TickerProvider，轮换自动生效 |
| **审计日志** | `repository/audit.go` 的 `RecordAudit` | 每次 `Resolve` 调用记录 `credential.resolve` 事件（不含值本身） |
| **配置热加载** | `config/config.go` 的现有 `Load()` | 加载时通过 Provider 解析 `secret:` 前缀值代替直读环境变量 |

### 配置变更设计

```env
# 当前（不安全）:
STORAGE_S3_ACCESS_KEY=AKIAIOSFODNN7EXAMPLE
AI_EMBED_API_KEY=sk-abc123

# 未来（安全）:
# 统一 secret:// 协议，由 CredentialProvider 解析
STORAGE_S3_ACCESS_KEY=secret://vault/prod/storage/s3/access_key
AI_EMBED_API_KEY=secret://vault/prod/ai/openai/key

# 降级：明文时直接使用（兼容本地开发）
STORAGE_S3_ACCESS_KEY=AKIAIOSFODNN7EXAMPLE   # 无 secret: 前缀 = 明文
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **Secret Store 不可用（启动时）** | 启动失败并报错，避免以空凭据运行；提供 `--allow-insecure` flag 跳过（显式 opt-in） |
| **Secret Store 不可用（运行时）** | 保持上次获取的值 + 告警日志；不阻断运行中请求；`/readyz` 标记为不健康 |
| **凭据变更检测** | `TickerProvider` 每 N 秒轮询；当检测到新版本时，记录审计日志并更新内存中的凭据 |
| **多 Provider 链式回退** | `env → keyfile → vault → aws_secrets_manager` 链式解析，第一个成功的返回 |
| **凭据缓存与 TTL** | 缓存凭据但不超过 Secret Store 的 TTL 标记；避免每次请求都调用外部 store |
| **Secret Store 自身认证** | Secret Store 的访问凭据本身需要引导——使用环境变量（仅这一个）或 Workload Identity（K8s IRSA / GCPWI） |

### 涉及代码估算

| 组件 | 行数 | 说明 |
|------|------|------|
| `internal/credentials/provider.go` — 通用 Provider 接口 + 链式解析器 | ~120 | 抽象 `CredentialProvider`、`ChainProvider`、缓存包装 |
| `internal/credentials/env.go` — 明文 fallback | ~40 | 兼容现有配置格式 |
| `internal/credentials/vault.go` — HashiCorp Vault KV 适配器 | ~150 | HTTP client + token 刷新 + 路径解析 |
| `internal/credentials/aws.go` — AWS Secrets Manager 适配器 | ~150 | AWS SDK 或 REST API 调用 |
| `internal/credentials/k8s.go` — Kubernetes Secret 适配器 | ~100 | 通过 K8s API 读取 Secret 资源 |
| `internal/config/config.go` — 集成 CredentialProvider | ~80 | `Load()` 时解析 `secret://` 前缀 |
| `internal/telemetry/metrics.go` — 凭据指标 | ~20 | `credential_resolve_total{provider,status}` |
| 测试 | ~200 | Contract test + mock provider |
| **合计** | **~860** | |

---

## 方向二：对象元数据搜索与结构化查询引擎（Structured Metadata Search & Query Engine）

### 现状

当前系统支持以下检索能力：

| 搜索类型 | 能力 | 入口 |
|---------|------|------|
| **语义搜索** | Vector / BM25 / Hybrid (RRF) | `POST /v1/search` |
| **前缀列表** | 按 key prefix 分页列出对象 | `GET /v1/files?prefix=...` |
| **标签批量操作** | 批量读取/设置/删除标签 | `POST /v1/batch/tag`（需遍历对象列表）|
| **桶统计** | 桶级对象数和总大小 | `GET /v1/buckets/{bucket}/stats` |

**但无法做到：**

```sql
-- 用户真正需要的查询：
-- "找到所有 tag.env=prod AND tag.project=aero 的高优先级对象"
-- "列出上周创建的、大小超过 10MB 的所有 PDF 文件"
-- "查询 storage_class=GLACIER 且 size>1GB 的冷对象"
-- "按 last_modified 降序排列、分页返回 content_type=image/* 的对象"
```

这些查询在当前系统中等价于：
1. 遍历全部对象（或按 prefix 粗筛）
2. 在应用层过滤
3. 在应用层排序和分页

对于一个生产级对象存储，这等价于"不支持"[^1]。

[^1]: AWS S3 虽然没有直接等价 API，但通过 `S3 Select` + `S3 Inventory` + `Lambda` 可以构建。MinIO 通过 `mc sql` 和 `Console` 搜索界面支持。AeroVault 当前完全没有对应能力。

### 为什么需要

| 场景 | 当前矛盾 | 影响范围 |
|------|---------|---------|
| **运维排查**："查找上周被标记为 `quarantine=true` 的所有对象" | 无 API 支持→手动遍历→耗时不可接受 | 运维效率 |
| **合规审计**："导出 2026 年 Q2 所有 `content_type=application/pdf` 且 `size>10MB` 的对象清单" | 无 API → 需要写脚本遍历所有数据 | 合规能力 |
| **成本分析**："找出所有 `storage_class=GLACIER` 但 `last_accessed` 在过去 30 天内有记录的对象" | 无法按 storage_class 过滤（仅标记，不驱动行为） | 成本优化 |
| **租户自服务**："我的 API Key 下哪些文件是上周创建的图片" | 只能语义搜索或无穷尽前缀遍历 | 用户体验 |
| **删除前确认**："确认只有 3 个对象匹配 `tag.pipeline=archive` 后再执行删除" | 无法确认→盲目删除→数据丢失风险 | 安全 |

### 架构设计

```
结构化元数据查询引擎：
                             ┌─────────────────────────────┐
                             │   Query Parser & Planner     │
                             │  "tag.env=prod"              │
                             │  "size>1GB"                  │
                             │  "content_type=image/*"      │
                             │  "created_after=2026-06-01"  │
                             │  AND (... OR ...)            │
                             └──────┬──────────────────────┘
                                    │ parsed AST
                                    ▼
                    ┌───────────────────────────────┐
                    │   Query Optimizer              │
                    │   • 选择最佳索引路径            │
                    │   • 复合索引 vs 回退全表扫描     │
                    │   • 分页策略（keyset pagination）│
                    └──────┬────────────────────────┘
                           │ SQL / LINQ
                           ▼
        ┌──────────────────────────────────────────┐
        │          Repository Layer                 │
        │  • objects 表 (size, content_type, ...)   │
        │  • tags 表 (GIN index on Postgres)        │
        │  • metadata 表 (JSONB on Postgres)        │
        │  • 复合索引 (tenant, bucket, created_at)  │
        └──────────────────────────────────────────┘
```

**数据模型适配：**

```go
// 新增查询类型
type MetadataQuery struct {
    Tenant  string
    Bucket  string // optional, "" = all
    Filters []Filter
    Sort    []SortField
    Limit   int
    Offset  string // keyset pagination marker
}

type Filter struct {
    Field string // "tag.env" | "size" | "content_type" | "storage_class" | "created_at" | "metadata.key"
    Op    string // "=" | "!=" | ">" | ">=" | "<" | "<=" | "IN" | "LIKE" | "EXISTS" | "NOT_EXISTS"
    Value any
}

type SortField struct {
    Field string
    Desc  bool
}
```

**新增 SQL 支持：**

Postgres 可以利用 GIN 索引实现高效的标签查询：

```sql
-- 按标签过滤（使用 GIN jsonb index）
SELECT * FROM objects 
WHERE tenant = $1 
  AND tags @> '{"env": "prod"}'::jsonb
  AND size > $2
  AND content_type LIKE 'image/%'
  AND created_at >= $3
ORDER BY created_at DESC
LIMIT 100;

-- 对应 GIN 索引（已有 tags 列存储为 jsonb/text）
CREATE INDEX idx_objects_tags ON objects USING GIN (tags);
CREATE INDEX idx_objects_tenant_created ON objects (tenant, created_at DESC);
CREATE INDEX idx_objects_tenant_storage_class ON objects (tenant, storage_class);
```

SQLite 下回退为行过滤（适合开发/小规模）。

### 实现策略（最小可行 → 完整版）

| 阶段 | 范围 | 行数 |
|------|------|------|
| **Phase 1**：纯 SQL 过滤，简单 WHERE 组合（=, >, <, LIKE, IN）+ `ORDER BY` + keyset pagination。无 OR 组合，无嵌套 | `repository/metadata_query.go` ~200 行 + `rest/handler.go` ~80 行 | ~280 |
| **Phase 2**：复杂组合（AND/OR 嵌套、NOT EXISTS、GIN 索引 tag 查询） | `repository/sql_query_builder.go` ~150 行 + 迁移文件 | ~200 |
| **Phase 3**：聚合查询（COUNT、GROUP BY storage_class、SUM size） | ~100 行 | ~100 |
| **Phase 4**：OpenAPI + SDK 方法 + Web UI 元数据搜索标签页 | 各 ~50 行 | ~200 |

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **无索引的全表扫描** | Phase 1 接受全表扫描（性能警告）；Phase 2 提供 `EXPLAIN` 分析端点；Phase 3 自动建议缺失索引 |
| **标签值大小写** | tags 保持区分大小写；查询时提供 `tag.env:case_insensitive=Prod` 语法选项 |
| **Metadata 中包含特殊字符** | 所有值参数化绑定（`$N`），防止 SQL 注入 |
| **翻页一致性** | 使用 keyset pagination（`WHERE (created_at, id) < ($1, $2)`），避免 offset 翻页的幻读问题 |
| **与语义搜索混合** | 先元数据过滤缩小范围→再语义搜索重排序（`POST /v1/search?filter=tag.env=prod`） |
| **空值语义** | `metadata.key=NOT_EXISTS` vs `metadata.key=NULL` 区分"键不存在"和"键存在但值为空" |

---

## 方向三：存储后端自适应过载保护与反压机制（Adaptive Backpressure & Overload Control）

### 现状

当前系统的过载保护架构：

```
Client → RateLimiter(HTTP token-bucket) → ConcurrencyLimiter → Storage Backend(S3/OSS/COS/Local)
                                            ↑ 仅限制 in-flight
                                              请求数量，不分读写
                                              优先级，不对后端
                                              状态做自适应调节
```

现有组件：

| 组件 | 文件 | 保护维度 | 限制 |
|------|------|---------|------|
| `RateLimiter` | `middleware/ratelimit.go` | 每租户 HTTP RPS | 仅控制请求到达率，无法感知后端健康状态 |
| `ConcurrencyLimiter` | `middleware/middleware.go` | 全局 in-flight 请求数 | 不分读写（GET vs PUT cost 不同），无后端感知 |
| `PerTenantConcurrencyLimiter` | `middleware/middleware.go` | 每租户 in-flight | 同上 |
| `CBConfig` | `storage/circuitbreaker.go` | 存储后端错误率 | 仅跟踪错误阈值，不跟踪延迟/吞吐量，不提供降级信号 |

**缺失的闭环：**

```
存储后端延迟升高 → 应自动降低对此后端的并发度
                 → 降低 HTTP 层的速率限制（RateLimiter 接收反馈）
                 → 读请求优先于写请求
                 → 慢后端不影响其他后端
                 → 向客户端返回 503 (Service Unavailable) 而非超时
```

### 为什么需要

| 生产场景 | 当前行为 | 理想行为 |
|---------|---------|---------|
| S3 突发延迟从 50ms 升高到 5s | 所有请求排队等待 TCP 超时（30s），连接池耗尽，全链路雪崩 | 自适应降低对 S3 的并发度到 N，其余请求返回 503 + Retry-After |
| 本地磁盘写延迟飙升（IO 饱和） | 所有读写操作（包括读）受影响 | 降低写并发，读基本不受影响（读写分离的背压策略） |
| 一个后端（如 OSS）完全不可用 | 请求持续尝试直到 TCP 超时，消耗连接和 goroutine | 快速熔断 + 主动降级（如返回 "oss backend unavailable"） |
| 突发写流量灌入（JobPool + 客户端同时写） | 存储后端同时承受 Indexer、Replication、Webhook、Reconcile + 客户端请求 | 后台批量操作的 I/O 优先级降低，客户端请求优先 |
| S3 返回限流 (503 SlowDown) | 不做特殊处理，持续重试同一速率 | 检测到后端限流信号 → 指数退避降低发送速率 |

### 架构设计

```
自适应过载保护闭环：

┌─────────────────────────────────────────────────────────────────────┐
│                      Adaptive Backpressure Controller               │
│                                                                     │
│  输入信号:                                                         │
│    ├─ Backend latency p50/p95/p99 (滑动窗口 60s)                   │
│    ├─ Backend error rate (4xx/5xx/network errors)                  │
│    ├─ Backend throughput (bytes/sec, ops/sec)                      │
│    ├─ Circuit breaker state (closed/half-open/open)                │
│    ├─ Queue depth (JobPool pending)                                │
│    └─ Client request rate (per-tenant, per-backend)               │
│                                                                     │
│  输出控制:                                                         │
│    ├─ Per-backend concurrency limit (动态调整)                     │
│    ├─ Per-backend rate limit (请求速率上限)                        │
│    ├─ Read/Write priority split (读优先于写)                       │
│    ├─ Circuit breaker sensitivity (基于延迟而非仅错误率)            │
│    └─ HTTP layer backpressure (503 Service Unavailable)            │
│                                                                     │
│  控制策略:                                                         │
│    ├─ AIMD (Additive Increase, Multiplicative Decrease)            │
│    │   → 正常时逐步增加并发（尝试上限），检测到延迟跳升后立刻减半 │
│    ├─ 写优先降级：写请求并发度减为 1/4，读请求保持 3/4              │
│    └─ 队列深度感知：JobPool 深度 > 阈值时，降低后台 worker I/O     │
└─────────────────────────────────────────────────────────────────────┘
```

**当前可复用的基础设施：**

| 现有组件 | 作用 | 扩展方向 |
|---------|------|---------|
| `CBConfig` (`storage/circuitbreaker.go`) | 基于错误率的电路断路器 | 增加延迟感知模式：p95 > 阈值（2s）→ 标记为 degraded |
| `RateLimiter` (`middleware/ratelimit.go`) | HTTP 层每租户 token-bucket | 接收 Backpressure Controller 的速率调整信号 |
| `ConcurrencyLimiter` (`middleware/middleware.go`) | in-flight 请求计数 | 接收每后端并发上限动态调整 |
| OTel metrics (`telemetry/metrics.go`) | 已有 `storage_*` 指标 | 新增 `storage_backend_latency`、`storage_backpressure_actions` |
| `runtime/metrics` (Go) | Go 运行时指标（goroutine, GC, heap） | 作为系统级输入信号 |

### 关键技术点

- **AIMD 算法**：每 10s 评估一次。如果 p95 延迟 < 基线×1.5 → 并发上限 += 10%；如果 p95 > 基线×3 → 并发上限 /= 2（写）或 /= 1.25（读）
- **后端标定**：启动时对每个后端执行 3 次探针请求，建立"基线延迟"。此基线作为 AIMD 的参考点。
- **读优先队列**：`FileService` 层区分 Get（读）和 Put/Delete（写），在过载时优先处理读。通过两个独立的 weighted semaphore 实现。
- **反压传播到 HTTP 层**：当 `storage.WriteAllowed()` 返回 false（写过载），`middleware` 可以提前拒绝 PUT/DELETE 请求，避免进入存储层。
- **指标暴露**：`adaptive_backpressure_actions_total{backend,action}`（`reduce_concurrency`/`increase_concurrency`/`prioritize_reads`）+ `storage_backend_degraded{backend}` gauge。

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **首个请求（基线未建立）** | 使用保守默认值（并发=4），前 3 个请求完成后更新基线 |
| **基线漂移（正常业务增长）** | 基线每 24h 重置重标定，避免长期以来的累计误差 |
| **短暂抖动（1s 延迟尖峰）** | 滑动窗口 60s 平滑，单次尖峰不触发减半；持续 10s 以上抖动才触发 |
| **多个后端混合（local + S3）** | 每个后端独立的状态机，彼此隔离；local fs 慢不影响 S3 |
| **与现有断路器的交互** | 电弧断路器（CB）负责"开/关"决策（二值），Backpressure Controller 负责"快/慢"调节（连续值），两者互补 |

### 涉及代码估算

| 组件 | 行数 |
|------|------|
| `internal/backpressure/controller.go` — 自适应控制器闭环 | ~250 |
| `internal/backpressure/aimd.go` — AIMD 策略实现 | ~120 |
| `internal/backpressure/backend_state.go` — 每后端状态机 | ~100 |
| `internal/storage/storage.go` — Storage 接口扩展（BackendStats 方法） | ~30 |
| `internal/middleware/backpressure.go` — HTTP 层反压中间件 | ~80 |
| `internal/service/file.go` — 读/写优先分离 | ~60 |
| `internal/telemetry/metrics.go` — 新增指标 | ~40 |
| 测试 | ~200 |
| **合计** | **~880** |

---

## 方向四：跨协议请求生命周期一致性与统一可观测性（Cross-Protocol Observability & Lifecycle Consistency）

### 现状

当前四协议（REST、S3、WebDAV、MCP）的请求生命周期完全隔离：

```
REST:  RequestID → Auth → Tenant → FileService.Get → Response
S3:    SigV4 → Auth → Tenant → FileService.Get → S3 XML Response
WebDAV: WebDAV Auth → Tenant → FileService.Get → PROPFIND XML
MCP:   stdio → Auth(tenant) → FileService.Get → JSON-RPC Response

无统一 trace ID、无跨协议请求关联、
无端到端延迟分解、无一致性检测
```

具体缺失：

| 维度 | 当前状态 | 问题 |
|------|---------|------|
| **统一 Trace ID** | 每个协议有自己独立的 request ID（REST 的 `X-Request-ID`, S3 的 `X-Amz-Request-ID`, MCP 的 `id`, WebDAV 无） | 无法关联"用户通过 S3 PUT 了一个对象，然后通过 REST GET 遇到了 404"——两个请求之间无关联 key |
| **端到端延迟分解** | OTel span 仅在 HTTP middleware 创建（`telemetry/http.go:17`），异步路径零 span | 无法回答"Put 操作中存储后端耗时 vs repo 耗时 vs 事件发布耗时各占多少" |
| **一致性检测** | 无机构检测跨协议读到的数据是否一致 | S3 写入后立刻 REST 读取可能得到过期元数据（eventual consistency），但无告警 |
| **幂等键共享** | Idempotency-Key 仅在 REST 路径实现（`middleware/idempotency.go`） | 通过 S3 发起的请求无法使用幂等键，同一操作在 REST 和 S3 之间无法去重 |
| **上下文传播** | 异步 worker 使用 `context.Background()` 或独立 ctx | 异步操作（Indexer、Replication）丢失了原始请求的 trace 上下文 |

### 为什么需要

| 运维问题 | 当前困难 | 解决后 |
|---------|---------|--------|
| "用户抱怨上传后文件不可见，但我无法跟踪请求路径" | REST 和 S3 的日志独立，没有关联 key 将两者串起来 | 统一 trace ID `X-Aero-Trace-Id` 跨协议传播，日志关联 |
| "存储后端延迟高，但我不知道是哪个租户的什么操作导致的" | HTTP middleware span 记录了 latency，但不分解到 service 和 storage 层 | 每个 `FileService` 操作创建子 span，精确分解 |
| "WebDAV 用户报告文件无法更新，但 S3 可以" | 无跨协议一致性检查，只能手动复现 | 一致性探测器自动验证跨协议读写，检测到不一致即告警 |
| "后台 JobPool 中的任务失败了，但触发它的请求 trace 已经丢失" | Job 入队时不携带 trace context | Job 入队时序列化 trace context，出队时恢复 |

### 架构设计

```
跨协议统一可观测性基础设施：

┌─────────────────────────────────────────────────────────────┐
│                  Unified Trace Context                      │
│  TraceID = 16-byte random (W3C Trace Context format)       │
│  SpanID  = 8-byte random                                    │
│  Propagated via:                                            │
│    REST:  X-Aero-Trace-Id / traceparent header             │
│    S3:    X-Amz-Trace-Id (自定义) / x-amz-traceparent      │
│    WebDAV: X-Aero-Trace-Id header                          │
│    MCP:   trace_id field 在 JSON-RPC 请求中                │
│    Async: context.Context 传透 + export 到 Job payload     │
└──────────────────────────┬──────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────┐
│              Consistent Read Detector                       │
│  • 每次写入事件（object.created/deleted）记录到追踪系统     │
│  • 读取时检查：最后写入时间戳 vs 读取时的元数据时间戳       │
│  • 如果读取到的时间早于最后写入 → 记录 inconsistency event │
│  • 跨协议特别监控：S3 Put → REST Get 之间的 stale read     │
└──────────────────────────┬──────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────┐
│            Cross-Protocol Idempotency                       │
│  • Idempotency-Key 从 REST-only 提升为跨协议特性            │
│  • S3 端：x-amz-idempotency-key header                     │
│  • WebDAV 端：If-Match / If-None-Match + Idempotency-Key   │
│  • 底层：复用已存在的 idempotency_keys 表                   │
│  • 关键：S3 PutObject 和 REST PUT /v1/files/{key}          │
│    写入同一对象时共享 Idempotency-Key 空间                  │
└─────────────────────────────────────────────────────────────┘
```

### 实现策略

**Phase 1 — Trace Context 统一（~400 行）：**

| 改动 | 说明 |
|------|------|
| `internal/middleware/trace.go` | 新中间件：生成/传播 Trace ID，透传 W3C `traceparent` header，创建 root span |
| `internal/api/s3compat/handler.go` | S3 响应中添加 `X-Amz-Trace-Id` header |
| `internal/api/webdav/dav.go` | WebDAV 响应中添加 `X-Aero-Trace-Id` header |
| `internal/mcp/server.go` | MCP 请求中的 `trace_id` 字段提取并注入 context |
| `internal/jobs/jobs.go` | Job payload 扩展 `trace_context` 字段，出队时恢复 |
| `internal/events/bus.go` | Event 扩展 `trace_id` 字段，异步传播 |

**Phase 2 — 端到端延迟分解（~250 行）：**

| 改动 | 说明 |
|------|------|
| `internal/service/file.go` | 每个 FileService 方法（Get、Put、Delete、List 等）创建子 span，记录 storage 和 repo 耗时 |
| `internal/storage/storage.go` | Storage 调用创建子 span（包含 backend name、key prefix） |
| `internal/repository/sql.go` | 关键 SQL 查询创建子 span（slow query 自动标记） |
| `internal/ai/search.go` | Search/Chat/Agent 创建子 span（embed、retrieve、rerank、llm） |

**Phase 3 — 一致性检测（~200 行）：**

| 改动 | 说明 |
|------|------|
| `internal/observability/consistency.go` | 一致性探测器：订阅 object.created/deleted 事件，记录最后写入时间；提供 `CheckConsistency` API |
| `internal/middleware/consistency.go` | 可选的严格一致性中间件：等待上次写入传播到当前副本后才放行读取 |
| `internal/telemetry/metrics.go` | `consistency_stale_reads_total{protocol_source,protocol_target}` 指标 |

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **Trace ID 冲突** | 16 字节随机数冲突概率极低；不做去重检测（参考 W3C Trace Context 标准） |
| **异步 Job 的 trace 超长** | Job 入队时只序列化 TraceID + ParentSpanID（~35 字节），而非整个 span tree |
| **一致性检测误报** | 检测到 stale read 后立即重试一次；连续两次 stale 才记录告警事件 |
| **Idempotency-Key 跨租户隔离** | Idempotency 表主键为 `(tenant, key)`，不同租户的 key 空间天然隔离 |
| **S3 协议不支持自定义 header** | 部分 S3 HTTP 客户端（如 aws-sdk-go）会剥离未知 header。使用 `x-amz-meta-` 前缀或 `x-amz-trace-id` 作为 S3 标准扩展 header |

### 涉及代码估算

| 组件 | 行数 |
|------|------|
| Phase 1: Trace 中间件 + 跨协议注入 | ~400 |
| Phase 2: 端到端 span 分解 | ~250 |
| Phase 3: 一致性检测 | ~200 |
| OpenAPI + SDK 扩展 | ~100 |
| 测试 | ~300 |
| **合计** | **~1,250** |

---

## 优先级矩阵

| # | 方向 | 业务价值 | 工程成本 | 风险 | 推荐排序 |
|---|------|---------|---------|------|---------|
| **1** | 外部密钥存储集成 | ★★★★★（企业安全准入硬性门槛；SOC2/ISO 27001 前置条件） | ★★（中，复用现有 `SecretProvider` 接口，~860 行） | 低（有完整的 SSE SecretProvider 作为参考实现） | **Phase 1** |
| **2** | 对象元数据搜索与结构化查询引擎 | ★★★★（产品差异化核心；解决"我的文件在哪"的基础需求） | ★★（中，~780 行四阶段实现，独立于现有搜索） | 中（Postgres GIN 索引表现优异，SQLite 兜底） | **Phase 1**（Parallel with #1） |
| **3** | 存储后端自适应过载保护与反压机制 | ★★★★（生产可靠性核心；避免雪崩的关键护栏） | ★★★（中高，~880 行，需要设计 AIMD 调节策略） | 中高（设计不当可能导致过度调节、吞吐量震荡） | **Phase 2**（需要 #1 先让配置可运行时修改？不依赖） |
| **4** | 跨协议请求生命周期一致性与统一可观测性 | ★★★（运维成熟度提升；复杂问题排查的关键能力） | ★★★（中高，~1,250 行，涉及四协议适配 + 异步传播） | 低-中（协议 header 标准化选择可能影响部分 S3 客户端） | **Phase 3**（高成本，影响面广） |

---

## 非目标（明确不包含）

- **Kubernetes Operator / Helm Chart 强化**：涉及部署编排而非核心功能扩展，属于 devops 基础设施（已有 `deploy/`）
- **现有功能的重构/拆分**：AGENTS.md 已定义硬约束（≤500 行/文件、≤50 行/函数），已有独立检查机制
- **社区治理 / 贡献者文档 / 行为准则**：项目治理层面的非功能需求
- **性能基准测试框架**：v18 已覆盖 CLI benchmark 工具但未覆盖 CI 持续回归检测——非本期焦点
- **S3 Batch Operations API**：v49 方向五已有独立分析
- **Web UI 改进**：v46 已有独立分析

---

## 与已有 52 期分析的主要差异

| 维度 | 已有 52 期覆盖 | 本期新增 |
|------|--------------|---------|
| **密钥管理** | SSE 密钥的 SecretProvider（v13/v25） | 所有凭据的统一外部密钥存储集成（v53 direction 1） |
| **搜索能力** | 语义搜索（v1–v52 多次覆盖）；元数据搜索概念表行（v12/v27） | 完整的元数据搜索架构设计、查询 DSL、多阶段实现、边界情况（v53 direction 2） |
| **过载保护** | HTTP 层限流（v1/v45）；静态 QoS（v36） | 存储后端自适应 AIMD 反压闭环（v53 direction 3） |
| **可观测性** | OTel HTTP span（v11/v23）；域指标（v2） | 跨协议统一 trace + 一致性检测 + 端到端延迟分解（v53 direction 4） |
| **协议一致性** | 响应格式一致性（v19）；输入上下文传播（v51） | 请求全生命周期一致性 + 跨协议幂等键共享（v53 direction 4） |
