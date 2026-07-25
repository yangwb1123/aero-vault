# AeroVault 高价值扩展方向 — 真实产品与架构缺口

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全局代码扫描（Go 源码，24 子包，48 组 SQL 迁移，3 套 SDK，Web UI）  
> **去重验证：** 逐方向对照 `docs/requirements/` 下全部 63 份既有分析文档、`docs/ROADMAP.md`、`docs/TODO.md`，使用 `grep` 确认无实质性分析覆盖  
> **日期：** 2026-07-10  
> **原则：** 选取 63 轮分析中**零实质性分析**的高价值空白区域，每个方向在代码中有精确锚点，且聚焦**产品能力与架构完整性**而非纯工程问题

---

## 审阅：前 63 轮分析的覆盖边界

前 63 轮分析已系统性地覆盖了（仅列大类）：

| 领域 | 覆盖轮次 |
|------|---------|
| S3 协议完备性（SSE-C, Object Lock, Lifecycle, CORS, Logging, Notification, Policy, Batch Delete, Legal Hold） | v23, v42, v56, v58, v61, v62 |
| AI/RAG 管线（Embedder, Search, Chat, Agent, Reranker, PII, Cost, Budget, Cache, 增量索引） | v13, v22, v31, v41, v53, v59, v60, v61, v63 |
| 多租户与鉴权（JWT, API Key, SigV4, Scope, Policy, ACL, mTLS, 租户 CRUD, 审计） | v5, v8, v15, v26, v27, v29, v32, v55 |
| 分布式与水平扩展（Cluster Singleton, Postgres Transport, DR, 限流协调） | v28, v35, v44, v45, v55, v57 |
| 运维成熟度（配置验证、优雅关闭、指标、告警、健康检查、Release Engineering） | v10, v27, v34, v38, v39, v46, v47, v60 |
| 性能与资源管理（内存上限, LRU, 连接池, 限流, 零拷贝, HTTP/2） | v11, v14, v26, v27, v31, v34, v37, v38, v60 |
| 数据完整性（Orphan GC, Scrub, Retention, Idempotency, 崩溃安全, 并发写入, 断路器测试） | v5, v15, v17, v21, v23, v28, v49, v51, v58, v60, v61, v62, v63 |
| 多协议一致性（REST/S3/WebDAV/MCP） | v19, v42, v59, v60 |
| Webhook 与事件（死信, 重试, SSE, 背压, 订阅者健康） | v17, v23, v28, v38, v39, v44, v55, v56, v60 |
| 加密（SSE envelope 版本化, 密钥轮换, KMS） | v24, v44, v45, v49 |
| 存储分层与生命周期 | v13, v17, v21, v23, v28, v58 |
| 多后端智能路由、数据迁移 | v10, v12, v15, v25, v28, v40, v42 |
| 元数据搜索与查询语言 | v12, v20, v22, v27, v31, v49 |
| 存储 QoS、限流精细化 | v29, v36, v53 |
| HTTP/2、零拷贝、I/O 优化 | v37 |
| Web UI 生产硬化、Admin 面板 | v30, v46, v63 |
| 分布式追踪 | v53 |
| 过程内事件总线订阅者健康管理 | v55 |
| 定期数据巡检与内容完整性 | ROADMAP #8 |
| 层次化命名空间与原子目录操作 | v32 |

**核心发现：** 经过 63 轮分析，纯技术层面的"有没有"问题已高度饱和。本期聚焦的 4 个方向都是**既没有被任何一轮分析实质性覆盖、又具有明确产品与架构价值**的真实空白——它们处于现有功能矩阵的**交叉地带**，而非单一子系统的缺失。

---

## 本期方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 | 63 轮覆盖 |
|---|------|------|--------|---------|---------|-----------|
| **1** | **事件通知智能路由：单火线 → 多目标、可过滤、可转换** | 产品/架构 | **P1** — 所有事件发送到单一 URL，无法按类型/前缀/内容过滤，无法多路路由；webhook 消费者被迫自行过滤和分发 | `internal/events/webhook.go:48`（`NewWebhook` 接受一个 `urls` 逗号分隔字符串）；`internal/events/webhook.go:76`（`Run` 对所有事件全部发送到所有 URL） | ⚠️ ROADMAP #7 的 `?notification` 子资源覆盖 S3 通知协议格式，但聚焦**S3 API 兼容性**而非**内部 webhook 系统的过滤/路由/转换能力**；v23/v56 覆盖 S3 通知但不涉及 webhook 内部实现 |
| **2** | **API 密钥细粒度授权：全局作用域 → 路径/桶/操作级权限** | 安全/产品 | **P1** — 当前作用域仅 `read` / `write` / `admin` 三档，无法限制到特定桶或路径前缀；多租户场景下无法创建"只读访问桶 A"的凭证 | `internal/auth/auth.go:20-23`（`ScopeRead`, `ScopeWrite`, `ScopeAdmin` 三个常量）；`internal/auth/auth.go:43-50`（`Key` 结构体只有 `Tenant` 和 `Scopes map[Scope]bool`） | ❌ **零实质性分析**（v26/v32 覆盖前缀级权限但聚焦**桶策略**（bucket policy JSON），与**API 密钥细粒度授权**是不同控制面；v29 覆盖 mTLS 等但非此方向） |
| **3** | **存储后端数据可移植性：导出/导入/迁移/备份（Bulk Data Portability）** | 产品/运维 | **P2** — 无法将对象数据从系统批量导出（如迁移到其他存储服务、创建异地冷备、或为离线审计做准备）；metadata snapshot 存在但仅含元数据，无对象内容；无"一键迁移到 S3"能力 | `internal/snapshot/snapshot.go`（仅 metadata 快照）；`internal/service/file_crud.go:Get`（每次一个对象，无批量导出）；`internal/storage/storage.go`（Storage 接口无 `Export`/`Import`/`Walk` 方法） | ⚠️ v25/v28/v40 覆盖**存储层数据迁移**（跨后端迁移对象数据）但聚焦**内部存储后端间的转换**（如 local → S3）而非**面向用户的批量导出/导入/备份能力**；v10 方向表一行提及"bulk import/export"零架构分析 |
| **4** | **Webhook 有效载荷模板化与可配置签名策略** | 产品/安全 | **P2** — Webhook payload 固定为原始 Event 序列化，无字段选择、无格式转换、无自定义头；HMAC 签名仅 SHA256 无算法选择；下游系统被迫解析 AeroVault 内部 Event 结构 | `internal/events/webhook.go:93-110`（`deliver` 方法硬编码 JSON 序列化 Event 结构体，无任何转换/过滤/模板逻辑）；`internal/events/webhook.go:62-65`（`WithSecret` 仅支持 HMAC-SHA256，无签名算法协商） | ❌ **零实质性分析**（v17/v28/v56 覆盖 webhook 重试/死信/交付保障，但从未讨论 payload 内容定制；v23 覆盖 S3 通知 XML 格式但非 webhook payload） |

---

## 方向一：事件通知智能路由 — 单火线 → 多目标、可过滤、可转换

### 现状

当前 webhook 的实现是一个简单的一对多扇出：

```go
// internal/events/webhook.go:48
func NewWebhook(urls string, logger *slog.Logger) *Webhook {
    parts := strings.Split(urls, ",")
    // 所有 URL 同等对待
}

// internal/events/webhook.go:76
func (w *Webhook) Run(ctx context.Context, sub <-chan repository.Event) {
    for {
        select {
        case e, ok := <-sub:
            // 对每个事件、每个 URL 都执行 deliver
            for _, url := range w.urls {
                w.deliver(ctx, e, url)  // ← 所有事件发送到所有 URL
            }
        }
    }
}

// internal/events/webhook.go:93
func (w *Webhook) deliver(ctx context.Context, e repository.Event) {
    body, _ := json.Marshal(e)  // ← 完整 Event 结构体，无法裁剪
    sig := hmacSHA256(body, w.secret)
    // POST body + HMAC header 到目标 URL
}
```

**关键缺失：没有任何过滤、路由、或转换能力。**

| 能力 | 当前 | 期望 |
|------|------|------|
| 按事件类型过滤 | ❌ 所有事件全部发送 | ✅ 只发送 `object.created` |
| 按桶过滤 | ❌ 无法指定桶 | ✅ 只发送桶 `uploads/` 的事件 |
| 按前缀过滤 | ❌ 无法指定 key 前缀 | ✅ 只发送 `uploads/*` 的事件 |
| 多路由 | ❌ 所有 URL 接收所有事件 | ✅ URL A 收 created, URL B 收 deleted |
| 并发控制 | ❌ 串行发送（一个慢 URL 阻塞其他） | ✅ 每 URL 独立 goroutine |
| 背压管理 | ❌ 无 | ✅ 每 URL 独立缓冲区 |

### 为什么这是问题

**下游系统过度耦合：** 当前模型强制每个 webhook 消费者实现自己的过滤逻辑。如果有 5 个下游系统（审计日志、通知服务、数据仓库、CDN 刷新、监控），它们每个都接收**所有事件**，即使只关心其中一种类型。这增加了下游系统的复杂性、带宽消耗和处理成本。

**事件数据泄露：** 无法按目标 URL 过滤事件意味着：即使某个 URL 只应当接收 `object.created` 事件（如 CDN 刷新通知），它也会收到包含 `object.deleted` 事件 payload 的 POST 请求。后者可能包含不应暴露给该系统的信息。

**背压级联：** 当前实现串行遍历所有 URL——如果一个 URL 的端点无响应（等待 5s 超时），其后所有 URL 的交付都会被延迟。一个慢速消费者可以阻塞所有其他消费者的实时性。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/events/webhook.go:48-58` | `NewWebhook` 接受逗号分隔 URL 字符串 | 无 URL 与规则的关联配置 |
| `internal/events/webhook.go:76-91` | `Run` 循环：对所有事件遍历所有 URL | 无过滤判断、无独立 goroutine |
| `internal/events/webhook.go:93-110` | `deliver` 硬编码 Event JSON 序列化 | 无 payload 转换/裁剪能力 |
| `internal/events/webhook.go:117-138` | `postOne` 执行 HTTP POST | 无自定义头、无签名算法选择 |
| `cmd/server/main.go:284-290` | 主程序创建 Webhook 实例 | 无路由配置 |
| `internal/repository/repository.go` | `NotificationRule` 结构体 | 定义了 S3 通知的过滤/事件类型，但未映射到 webhook |

### 架构蓝图

```go
// 路由规则：每个目标 URL 有自己的过滤条件和配置
type WebhookRoute struct {
    URL             string   // 目标 URL
    Events          []string // 过滤：事件类型列表，空 = 全部
    Buckets         []string // 过滤：桶列表，空 = 全部
    Prefix          string   // 过滤：key 前缀，空 = 全部
    IncludeFields   []string // 转换：payload 包含字段，空 = 全部
    ExcludeFields   []string // 转换：payload 排除字段
    Headers         map[string]string // 自定义 HTTP 头
    SigningAlgorithm string  // 签名算法："sha256" | "none" ，默认 "sha256"
    Timeout         int      // 请求超时秒数，0 = 全局默认
}

// 配置方式 A: 环境变量
// EVENTS_WEBHOOK_ROUTES='[
//   {"url":"https://hooks.example.com/created","events":["object.created"],"prefix":"uploads/"},
//   {"url":"https://hooks.example.com/audit","events":["object.created","object.deleted"]},
//   {"url":"https://hooks.example.com/cdn","events":["object.created"],"buckets":["public"]}
// ]'

// 配置方式 B: 扩展 BucketConfig 的 NotificationRules 以支持 webhook
// 当前 S3 NotificationRule 结构体已有 Events + FilterKey + QueueARN
// QueueARN 可映射为 webhook URL
```

**规模估计：** ~250 行（路由规则模型 + 解析 + 调度引擎 + 并发控制 + 测试）

---

## 方向二：API 密钥细粒度授权 — 全局作用域 → 路径/桶/操作级权限

### 现状

当前 API 密钥的作用域模型极其简单——只有三个全局作用域：

```go
// internal/auth/auth.go:20-23
const (
    ScopeRead  Scope = "read"
    ScopeWrite Scope = "write"
    ScopeAdmin Scope = "admin"
)

// internal/auth/auth.go:43-50
type Key struct {
    Token  string
    Tenant string
    Scopes map[Scope]bool  // 仅有全局读/写/管理员
    // ❌ 没有桶限制
    // ❌ 没有路径/前缀限制
    // ❌ 没有 IP 白名单
    // ❌ 没有时间窗口
    // ❌ 没有速率限制覆盖
}
```

**授权检查路径（REST handler 示例）：**

```go
// internal/api/rest/handler.go — 授权检查
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
    key := auth.KeyFromContext(r.Context())
    if !key.Has(auth.ScopeWrite) {
        writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
        return
    }
    // ✅ 有 write 作用域 => 可以写任何桶、任何 key、任何大小
    // ❌ 无法限制"只能写桶 acme-public"
    // ❌ 无法限制"只能写前缀 uploads/"
}
```

**生产场景的典型需求：**

| 场景 | 当前能力 | 期望 |
|------|---------|------|
| 只读第三方集成 | `scope=read` → 可列出/读取所有对象 | `scope=read+bucket:public` → 仅可读取桶 `public` |
| CDN 回源凭证 | `scope=read` → 可读取所有租户数据 | `scope=read+prefix:cdn-assets/` |
| 有限写入的 CI/CD token | `scope=write` → 可覆盖任何 key | `scope=write+bucket:deploy+bucket:staging` |
| 临时运维 key | `scope=admin` → 完全管理权限 | `scope=admin+ttl:24h` |
| 内部审计只读 | `scope=read` → 可读任何对象 | `scope=read+exclude-bucket:private` |

### 为什么这是问题

**最小权限原则（Principle of Least Privilege）的缺失是最常见的安全缺陷之一。** 当前模型是"全有或全无"的二分法——一个 API key 要么对一个租户有完全读写权限，要么没有。这导致：

- **密钥共享泛滥：** 开发者和 CI/CD 系统共享同一个拥有完全权限的 key，因为无法创建仅用于特定用途的受限 key。
- **横向移动风险：** 攻击者获取一个 key 后即可访问该租户的全部数据，无法隔离爆炸半径。
- **合规缺口：** SOC2 / ISO 27001 / PCI-DSS 都要求访问控制遵循最小权限原则。当前的三档权限模型无法通过合规审计。
- **产品壁垒：** 企业客户期望为不同团队、不同应用创建不同权限的凭证。这是 SaaS 存储产品的基线能力。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/auth/auth.go:20-23` | 三个 `Scope` 常量 | 仅全局作用域，无资源路径限定 |
| `internal/auth/auth.go:43-50` | `Key` 结构体 | 无 `Buckets []string`、`Prefixes []string`、`IPWhitelist`、`TTL` 等字段 |
| `internal/auth/auth.go:88-133` | `Authorize` 方法 | 仅检查全局 `ScopeRead`/`ScopeWrite`，无资源级检查 |
| `internal/auth/store.go:16` | `PersistentKey` 结构体 | `Scopes string` 字段存储 `read+write`，无资源限定 |
| `internal/api/rest/handler.go` | 各 handler 的鉴权逻辑 | 分散在 handler 内，无统一授权引擎 |
| `internal/api/s3compat/handler.go:93-99` | `checkBucketPolicy` S3 策略评估 | 桶策略存在但仅用于 S3 协议，REST API 不经过策略评估 |

### 架构蓝图

```go
// 扩展 Scope 为资源级
type ResourceScope struct {
    Action  string   // "read" | "write" | "admin" | "delete"
    Buckets []string // 空 = 全部；通配符 "public-*" 支持
    Prefix  string   // 空 = 全部
    Exclude map[string][]string // 排除规则
}

type Key struct {
    Token    string
    Tenant   string
    Scopes   []ResourceScope  // 有序：第一条匹配即生效
    IPWhitelist []string      // 可选
    ExpiresAt   *time.Time    // 可选：到期后自动拒绝
    RateLimitOverride *float64 // 可选：覆盖默认 RPS
}

// 统一授权引擎
func Authorize(key Key, action, bucket, keyPath string) error {
    // 1. 检查过期
    if key.ExpiresAt != nil && time.Now().After(*key.ExpiresAt) {
        return ErrKeyExpired
    }
    // 2. 检查 IP 白名单（需从 context 获取客户端 IP）
    // 3. 遍历 Scopes，第一条匹配胜出
    for _, s := range key.Scopes {
        if s.Action != action { continue }
        if !matchBuckets(s.Buckets, bucket) { continue }
        if !strings.HasPrefix(keyPath, s.Prefix) { continue }
        return nil // ✅ 授权通过
    }
    return ErrForbidden
}
```

**迁移方案（无破坏性）：** 保持 `ScopeRead`/`ScopeWrite`/`ScopeAdmin` 后向兼容——当 Key 使用传统作用域时，自动展开为 `ResourceScope{Action: "read", Buckets: ["*"], Prefix: ""}`。仅当 Key 存储了 `resource_scopes` 字段时才启用细粒度授权。

**规模估计：** ~350 行（模型定义 + 序列化/反序列化 + 授权引擎 + 迁移扩展 + 测试）

---

## 方向三：存储后端数据可移植性 — 导出/导入/迁移/备份（Bulk Data Portability）

### 现状

系统当前的"数据导出"能力极其有限：

```go
// 方式 1: 元数据快照（仅 metadata）
// internal/snapshot/snapshot.go
func (s *Snapshot) Export(ctx context.Context, w io.Writer) error {
    // 导出所有对象的元数据行（ID, TenantID, Bucket, Key, VersionID, Size, ETag ...）
    // ❌ 不包含对象内容
    // ❌ 无法选择性导出（必须全部或全不）
}

// 方式 2: 逐对象 GET（无批量）
// internal/service/file_crud.go:Get
func (s *FileService) Get(ctx context.Context, tenant, bucket, key string) (io.ReadCloser, repository.Object, error) {
    // 一次一个对象
    // ❌ 无批量迭代器
    // ❌ 无并行下载
}

// 方式 3: 跨区域复制（仅事件驱动）
// internal/replication/replication.go
// 仅适用于部署间的持续同步，不适合一次性导出或备份
```

**当前可用的数据导出选项：**

| 方法 | 包含内容 | 适用场景 | 缺点 |
|------|---------|---------|------|
| `GET /v1/files/*` | ✅ 对象内容 | 单个对象 | 无法批量 |
| `GET /v1/buckets/{bucket}/stats` | ❌ 仅统计 | 容量监控 | 无数据 |
| `aero-vault cli ... get` | ✅ 对象内容 | 单次下载 | 无批量、串行 |
| `snapshot.Export` | ❌ 仅元数据 | 元数据备份 | 无对象内容 |
| 存储后端直接访问 | ✅ 原始 blob | 应急恢复 | 无元数据关联、无 key 结构 |

**缺失的能力：**

```
# 理想的一键导出
aero-vault admin export --tenant acme --output acme-backup.tar.gz
  → 生成包含元数据 + 对象内容的可移植归档

# 理想的跨后端迁移
aero-vault admin migrate --from local --to s3 --bucket production
  → 将本地存储的 production bucket 完整迁移到 S3
  → 零停机：迁移期间旧数据仍可读取
  → 完成后自动切换存储后端指针

# 理想的选择性导出
aero-vault admin export --tenant acme --bucket uploads --prefix "2026/" --output uploads-2026.tar
  → 按条件筛选要导出的对象
```

### 为什么这是问题

**供应商锁定风险：** 当前系统没有标准化的数据导出机制。如果用户需要从 AeroVault 迁移到其他存储系统（或反之），唯一的选择是通过 REST API 逐对象下载——对于存储了数百万个对象的生产系统，这可能需要数天甚至数周的时间。

**备份与灾备缺口：** 元数据快照 + 存储后端直接访问是离线的、分立的备份方式。没有原子的、元数据与内容一致的备份点。当需要从灾难中恢复时，运维人员需要手动关联元数据和 blob——操作复杂且易错。

**离线审计与合规：** 很多合规场景需要将数据导出到外部系统进行审计分析（如将对象数据导入 SIEM、数据湖、或第三方归档服务）。当前无法实现此需求。

**数据迁移导致停机：** 如果用户需要从 local FS 切换到 S3（或从一个 S3 区域切换到另一个），当前必须停服迁移——因为没有热迁移能力。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/snapshot/snapshot.go` | 仅元数据快照 | 无对象内容导出 |
| `internal/service/file_crud.go:Get` | 逐对象 GET | 无批量迭代、无流式导出 |
| `internal/service/file_features.go:List` | 分页列出对象 | 分页仅用于 UI，非导出用途 |
| `internal/repository/sql_objects.go:ListObjects` | `LIMIT/OFFSET` 游标 | 无流式游标（keyset pagination）|
| `internal/storage/storage.go` | Storage 接口 | 无 `Walk` / `Export` / `Import` 方法 |
| `internal/storage/local.go` | local 后端 | 可直接读取文件系统但不通过 API |
| `internal/storage/s3.go` | S3 后端 | 可通过 S3 API 批量导出但不通过 AeroVault |
| `cmd/server/main.go` | 启动序列 | 无迁移工具入口 |

### 架构蓝图

**Phase 1：对象级导出迭代器（~200 行）**

```go
// 新增 Service 方法
func (s *FileService) Walk(ctx context.Context, tenant, bucket, prefix string, fn func(Object, io.ReadCloser) error) error {
    // 使用 keyset pagination 高效遍历所有对象
    // 对每个对象：Get 内容 → 调用 fn → 继续下一个
    // 支持并行度控制（--concurrency 参数）
    // 支持错误处理策略（stop-on-error / skip-and-log）
}

// 导出格式：tar.gz 或 tar.zst
// 每个对象在归档中的路径: {tenant}/{bucket}/{key}
// 元数据文件: _metadata.jsonl（每行一个 JSON Object）
```

**Phase 2：原子数据迁移（~300 行）**

```go
// 迁移状态机
type MigrationJob struct {
    Source      storage.Storage
    Target      storage.Storage
    Filter      MigrationFilter  // tenant/bucket/prefix
    Strategy    string           // "copy-then-switch" | "mirror-then-cutover"
    ObjectCount int64
    DoneCount   int64
    Status      string           // "running" | "verifying" | "cutover" | "completed"
}

// 迁移流程（copy-then-switch）：
// 1. 枚举源后端的所有对象
// 2. 并行 copy 到目标后端（使用 Storage.Copy 原语或流式传输）
// 3. 校验：每个对象比较 ETag/checksum
// 4. 切换：原子更新 storage_key → 指向新后端（短停机窗口或零停机）
// 5. 清理：删除源后端的旧 blob（可选，保留期后删除）

// 零停机策略（mirror-then-cutover）：
// - 新增一个 "写入双向、读取优先" 的 wrapper Storage
// - 写操作同时写入源和目标
// - 读操作优先从目标读取
// - 确认目标完全同步后，切换 Storage 指针
```

**Phase 3：导入能力（~150 行）**

```go
// 从外部来源导入数据
// POST /v1/admin/import
// 支持格式：tar.gz（AeroVault 导出格式）、S3 batch 清单、CSV 文件列表
// 导入验证：checksum 校验、重复检测、冲突策略
```

**规模估计：** ~650 行 + 测试（分三个阶段实施）

---

## 方向四：Webhook 有效载荷模板化与可配置签名策略

### 现状

Webhook 事件 payload 是**固定的、不可修改的**Event 结构体序列化：

```go
// internal/events/webhook.go:93-110
func (w *Webhook) deliver(ctx context.Context, e repository.Event) {
    body, err := json.Marshal(e)  // ← 整个 Event 结构体，无字段选择
    if err != nil {
        return
    }

    sig := hmacSHA256(body, w.secret)  // ← 仅 SHA256，无算法选择
    req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
    req.Header.Set("X-Aero-Signature-256", "sha256="+sig)
    req.Header.Set("Content-Type", "application/json")
    // ← 无自定义头、无载荷格式选项
}
```

**当前 Event 序列化输出：**

```json
{
  "id": 12345,
  "tenant_id": "acme",
  "bucket": "uploads",
  "key": "2026-07-10/report.pdf",
  "type": "object.created",
  "object_id": 67890,
  "request_id": "abc-123-def",
  "payload": {
    "backend": "local",
    "size": "1048576",
    "etag": "\"abc123\"",
    "content_type": "application/pdf"
  },
  "created_at": "2026-07-10T12:00:00Z"
}
```

**下游系统常见需求：**

| 需求 | 当前能否满足 | 期望 |
|------|------------|------|
| 只接收 `key` 和 `type` 字段 | ❌ 固定全量 | ✅ `include_fields: ["key", "type", "event_time"]` |
| 排除 `tenant_id`（已知上下文） | ❌ 无法排除 | ✅ `exclude_fields: ["tenant_id", "request_id"]` |
| 使用 CloudEvents 格式 | ❌ AeroVault 自定义格式 | ✅ `format: "cloudevents"`（`specversion: "1.0"`）|
| 自定义 Authorization 头 | ❌ 仅 HMAC 签名 | ✅ `headers: {"Authorization": "Bearer ztkn-..."}` |
| 使用 Ed25519 而非 HMAC-SHA256 | ❌ 仅 HMAC-SHA256 | ✅ `signing_algorithm: "ed25519"` |
| payload 中包含桶的访问 URL | ❌ 不包含 | ✅ `template: "Object {{.key}} was created at {{.url}}"` |

### 为什么这是问题

**集成成本高：** 每个 webhook 消费者必须解析 AeroVault 的 Event 结构体，理解其内部字段含义。如果消费者本身是通用 webhook 接收器（如 Zapier、Zapier、Slack Webhook、PagerDuty），它们无法直接使用 AeroVault 的事件 payload——需要中间转换服务。

**过度暴露内部细节：** Event 结构体暴露了 `object_id`、`request_id`、`payload.backend` 等内部实现细节。这些信息对下游系统不仅无用，还可能被攻击者利用进行系统 fingerprinting。

**缺乏互操作性：** CloudEvents 已成为云事件的行业标准（CNCF 孵化项目、Azure Event Grid、Google Cloud Events、AWS EventBridge 均支持）。不支持 CloudEvents 意味着 AeroVault 无法与 CloudEvents 原生工具链集成。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/events/webhook.go:93-110` | `deliver` 固定 JSON 序列化 Event | 无格式选择、无字段过滤、无模板 |
| `internal/events/webhook.go:62-65` | `WithSecret` 仅 HMAC-SHA256 | 无法选择签名算法或提供自定义头 |
| `internal/events/webhook.go:117-138` | `postOne` 硬编码 headers | 无法添加自定义 HTTP 头 |
| `internal/repository/repository.go:Event` | Event 结构体定义 | 内部结构体作为 webhook payload 暴露 |
| `internal/events/webhook_test.go` | 测试固定 payload 格式 | 无模板/格式测试 |

### 架构蓝图

```go
// payload 模板配置
type WebhookTemplate struct {
    IncludeFilter  []string  // 只包含这些 top-level 字段
    ExcludeFilter  []string  // 排除这些 top-level 字段
    Format         string    // "json" (默认) | "cloudevents"
    CustomHeaders  map[string]string
    Signing        SigningConfig
}

type SigningConfig struct {
    Algorithm  string  // "hmac-sha256" (默认) | "hmac-sha512" | "ed25519" | "none"
    KeyID      string  // 用于多密钥轮换
}

// CloudEvents 格式映射
type CloudEvent struct {
    SpecVersion    string      `json:"specversion"`     // "1.0"
    ID             string      `json:"id"`              // event.ID
    Source         string      `json:"source"`          // "/aero-vault/{tenant}/{bucket}"
    Type           string      `json:"type"`            // "io.aero-vault.object.created"
    Subject        string      `json:"subject"`         // event.Key
    Time           string      `json:"time"`            // event.CreatedAt
    Data           interface{} `json:"data"`            // 过滤后的 payload
}
```

**规模估计：** ~200 行（模板引擎 + 格式渲染 + 签名扩展 + 配置解析 + 测试）

---

## 综合优先级矩阵

| # | 方向 | 影响面 | 商业价值 | 实现难度 | 估算规模 | 前置依赖 | 与现有功能的关系 |
|---|------|--------|---------|---------|---------|---------|----------------|
| **1** | 事件通知智能路由 | 产品/架构 | 🔴 高（多下游系统集成刚需；当前模型阻塞企业采用） | 中低 | ~250 行 | 无 | 扩展现有 `Webhook`/`Bus`，不改造现有接口 |
| **2** | API 密钥细粒度授权 | 安全/产品 | 🔴 高（最小权限原则是企业合规基线） | 中 | ~350 行 | `auth` 包重构 | 前后兼容：传统 scope 自动展开为全权限资源 scope |
| **3** | 存储后端数据可移植性 | 产品/运维 | 🟠 中（供应商锁定缓解；备份与迁移刚需） | 中高 | ~650 行 | `Storage` 接口扩展 | 新增功能，不改造现有路径 |
| **4** | Webhook 有效载荷模板化 | 产品/安全 | 🟡 中低（提升集成体验；降低对接成本） | 低 | ~200 行 | 无 | 扩展现有 `Webhook`，后向兼容 |

### 商业价值 vs 实现成本

```
商业价值高
   │
   │  方向二 (API 细粒度授权)      方向一 (事件智能路由)
   │      ●                             ●
   │
   │                          方向三 (数据可移植性)
   │                              ●
   │
   │                  方向四 (payload 模板)
   │                      ●
   │
   └───────────────────────────────────────────→ 实现难度
       低                         高
```

### 推荐实施顺序

```
Sprint 1（产品体验速赢）：
  ┌─────────────────────────────────────────────┐
  │ #1 事件通知智能路由（~250 行）              │
  │   └─ 按事件类型/桶/前缀过滤 + 多 URL 独立   │
  │       goroutine + 背压保护                  │
  │                                             │
  │ #4 Webhook payload 模板化（~200 行）        │
  │   └─ CloudEvents 格式 + 字段过滤 + 自定义头 │
  └─────────────────────────────────────────────┘

Sprint 2（安全加固）：
  ┌─────────────────────────────────────────────┐
  │ #2 API 密钥细粒度授权（~350 行）            │
  │   └─ ResourceScope 模型 + 统一授权引擎      │
  │   └─ 前后兼容：传统 scope 自动展开           │
  └─────────────────────────────────────────────┘

Sprint 3（数据可移植性）：
  ┌─────────────────────────────────────────────┐
  │ #3 Phase 1: Walk 迭代器 + tar 导出（~200 行）│
  │ #3 Phase 2: 跨后端迁移（~300 行）           │
  │ #3 Phase 3: 导入能力（~150 行）             │
  └─────────────────────────────────────────────┘
```

---

## 与既有 63 份分析的去重对照

| 本文件方向 | grep 验证命令 | 既有分析覆盖情况 | 去重结论 |
|-----------|-------------|----------------|---------|
| **方向一：事件通知智能路由** | `grep -r "webhook.*filter\|webhook.*route\|event.*rout\|event.*filter\|webhook.*select\|webhook.*subscription\|webhook.*topic\|webhook.*pattern\|notif.*rout\|notif.*filter" docs/requirements/` → **0 实质性分析命中**。ROADMAP #7 的 `?notification` 子资源覆盖 S3 通知协议格式（XML `TopicConfiguration`），但聚焦**S3 API 兼容性**——`TopicConfiguration` 的 `Event` 和 `Filter` 字段用于 S3 协议 XML 序列化/反序列化，而非内部 webhook 实现。v23/v56 覆盖 S3 通知但不涉及 webhook 内部路由。 | ✅ **完全去重**（ROADMAP #7 和 v23/v56 聚焦 S3 协议层，方向一聚焦 webhook 内部实现层） |
| **方向二：API 密钥细粒度授权** | `grep -r "api.*key.*scope.*resource\|api.*key.*bucket\|api.*key.*prefix\|key.*permission\|key.*authorization.*level\|key.*granular.*scope\|key.*resource.*scope\|fine.*grained.*auth\|scoped.*key\|key.*bucket\|key.*prefix" docs/requirements/` → **0 命中**。v26/v32 覆盖**桶策略（bucket policy JSON）**——通过 JSON 策略文档实现前缀级权限；但策略评估仅在 S3 协议 `checkBucketPolicy` 路径中执行，**不影响 REST API 和 API 密钥的作用域模型**。v29 覆盖 mTLS 和 SNI。 | ✅ **完全去重**（桶策略与 API 密钥授权是不同的控制面；桶策略是桶级、声明式的、S3 专用的；方向二是密钥级、程序化的、系统全局的） |
| **方向三：存储后端数据可移植性** | `grep -r "data.*export\|data.*import\|bulk.*export\|bulk.*import\|storage.*backup\|data.*portab\|vendor.*lock\|migrate.*data\|data.*migrat\|export.*objects\|import.*objects\|object.*export\|object.*import\|tar.*export\|archive.*export" docs/requirements/` → **0 实质性命中**。v25/v28/v40 覆盖**存储层数据迁移**（跨后端迁移对象数据）但聚焦**后端间的内部转换**（如 local → S3 迁移脚本/worker），**而非面向用户的批量导出/导入/备份产品能力**。v10 方向表一行提及"bulk import/export"概念但**零架构分析**。 | ✅ **互补去重**（v25/v28/v40 聚焦内部存储后端迁移的基础设施，方向三聚焦为用户提供标准化的数据可移植性产品能力——导出归档、导入、热迁移） |
| **方向四：Webhook payload 模板化** | `grep -r "payload.*template\|webhook.*template\|webhook.*format\|cloudevents.*support\|event.*format\|webhook.*transform\|payload.*convert\|webhook.*custom.*header\|webhook.*sign.*algo\|event.*schema\|payload.*schema" docs/requirements/` → **0 命中**。v17/v28/v56 覆盖 webhook 重试/死信/交付保障但**从未讨论 payload 内容定制**。v23 覆盖 S3 通知 XML 格式但那是 S3 协议级别的通知格式，**非 webhook payload**。 | ✅ **完全去重**（payload 内容定制是 webhook 系统中从未被触及的维度） |

---

## 附录：为什么这些方向是"交叉地带"缺口

前 63 轮分析覆盖了每个子系统内部的功能完备性。本期 4 个方向均处于**子系统之间的交叉地带**——它们不是单个子系统缺失的功能，而是多个子系统相互作用时需要的能力：

```
方向一（事件智能路由）：
  Bus ──→ Webhook ──→ HTTP POST ──→ 下游系统
  │                    │
  │                    └── 无过滤/路由/转换
  │
  └── 有事件类型、桶、前缀维度

方向二（细粒度授权）：
  API Key ──→ Auth Middleware ──→ REST Handler ──→ FileService
     │                              │
     └── 仅有全局 scope             │
                      └── 无统一授权引擎

方向三（数据可移植性）：
  List ──→ Get ──→ tar/zip ──→ 导出文件
   │                │            │
   │  仅分页        │  逐对象     └── 无格式
   └── 无游标        └── 无并行

方向四（payload 模板化）：
  Event Bus ──→ Webhook ──→ JSON ──→ 下游系统
                └── 固定 payload ──── 无 CloudEvents
```

这些交叉地带在 "每个子系统做得好不好" 的分析中被系统性忽略——它们不是系统的"零件缺失"，而是"零件间的连接器缺失"。
