现在我掌握了所有必要的代码信息。让我基于代码证据编写架构分析。

---

# 架构评估与技术设计分析

## 1. 架构评估

### 优势

当前架构呈现清晰的**分层设计**（config → storage → repo → service → workers → middleware → router），在各个维度上表现出深思熟虑的权衡：

| 维度 | 优势 |
|------|------|
| **协议适配隔离** | REST、S3、WebDAV、MCP 四种协议各自独立路径，互不耦合，修改其一不影响其余 |
| **持久化抽象** | `Repository` 接口包含 80+ 方法，覆盖完整对象生命周期，SQLite/Postgres 通过 `s.rebind` 统一占位符机制 |
| **事件驱动解耦** | `EventBus` + 订阅模式使 AV/Replication/Webhook 异步独立运行，不阻塞主请求路径 |
| **Opt-in 安全默认** | AI/向量/事件/复制/集群全部 flag-gated，基线路径（SQLite + local FS + 无鉴权）零外部依赖 |
| **存储层可替换** | `Storage` 接口 + 工厂模式，local/S3/OSS/COS 统一抽象，通过 `storage/contract_test.go` 契约测试保障一致性 |

### 关键设计决策评估

| 决策 | 评价 | 风险 |
|------|------|------|
| Auth 作为 middleware，而非协议层强制接口 | 简化 handler 编写，但出现了 MCP/WebDAV 盲区 | **P0 安全漏洞**（已确认） |
| BM25 纯内存 + 启动时全量重建 | 简单实现，零外部依赖 | 冷启动混合搜索退化、进程重启丢失索引 |
| `succeeded bool` 作为死信终结状态 | 避免无限重试和表膨胀 | 数据契约违约，监控遗漏 |
| Web UI 作为单文件 vanilla JS SPA | 零构建步骤，动态部署 | 缺乏内容类型感知渲染，预览能力严重受限 |
| 租户信息通过 Header/JWT 隐式传递 | 最小化 handler 参数改动 | MCP stdio 和 WebDAV 无法获取 HTTP 上下文，退化为 `"default"` |
| 迁移双文件（sqlite + postgres） | 完美适配两种后端 | 需额外维护并行 DDL |

### 架构债务与技术债

按影响面和修复成本排序：

1. **认证中间件注册时序缺陷**（P0）— `mcp` 和 `webdav` 注册在 `applyMiddleware()` **之后**或通过独立分发器，导致它们无认证、无租户、无请求ID追踪。根本原因是 `buildRouter` 内部将 MCP/WebDAV 视为"基础设施端点"而非"业务端点"。

2. **BM25 持久性缺口**（P1）— 持久化能力只存在于向量索引端（pgvector/Qdrant/pgFTS），BM25 没有任何 Save/Load。对称性破坏意味着混合搜索的冷启动行为不一致且不可观测。

3. **Webhook 失败状态机设计缺陷**（P1）— 单布尔字段 `succeeded` 承载三态（待重试 / 已送达 / 永久死信），缺少 `updated_at` 时间戳和 `status` 枚举。`MarkWebhookSucceeded` 的重用语义混淆。

4. **内容预览管线全面缺失**（P2）— 缩略图端点存在（`GET /files/*/thumbnail`），但 Web UI 完全不消费它。Agent 工具中 `read_file` 的 4MB 截断（`io.LimitReader(rc, 4<<20)`）与 Web UI 的 4KB 显示不一致。没有任何内容类型路由。

5. **租户管理全量 admin 权限**（P2）— 只有 `GET /usage` 是自助路由，其余全部通过 admin scope。缺少 `/me/` 系列端点意味着租户无法自行管理 API key、用量告警、通知配置。

---

## 2. 扩展方向

### 方向 A：统一认证门面（Authentication Gateway Facade）

**业务价值**：消除 P0 安全漏洞；为所有协议提供一致的认证、租户提取、审计追踪。

**核心挑战**：
- MCP stdio 模式没有任何 HTTP 上下文，需要另一种认证方式（如环境变量 `MCP_API_KEY` 或 stdin 首行令牌）
- WebDAV 使用了 `golang.org/x/net/webdav.Handler`，其内部 `ServeHTTP` 模式使得在其之上叠加认证需要包装器
- `buildDispatcher` 当前的函数式分发路径（`davH != nil && path matches prefix → dispatch`）使得 WebDAV 完全脱离 chi 中间件链

**预期架构变更**：

```
当前:
  applyMiddleware(r, ...) → 包装后的 chi router
  └─ r 内部注册了 REST + S3
  └─ r 外部附加了 MCP (r.Method)
  └─ buildDispatcher 在 chi 外分发 WebDAV

建议:
  authGate := auth.NewGateway(authReg)  // 统一认证门面
  // 所有协议适配器通过 authGate.Wrap(handler, opts) 注册
  authGate.Wrap(restRouter, opts{Routes: "/v1", Scopes: [...]})
  authGate.Wrap(s3Router, opts{Routes: "/s3", SigV4: true})
  authGate.Wrap(mcpHandler, opts{Routes: "/mcp", APIKey: true})
  authGate.Wrap(davHandler, opts{Routes: cfg.WebDAV.Prefix, HeaderTenant: true})
```

**对现有系统的影响**：
- `buildRouter` 和 `applyMiddleware` 需要合并为一个清晰的 assembly 函数
- MCP `tenantFor` 回退逻辑可保留，但需要从请求上下文中提取已认证的租户
- 对现有 REST handler 的测试无影响（它们通过 `httptest` 测试，不从中间件获取 auth）
- S3 的 SigV4 验证可以保留在本身 `s3compat` 内部，但租户/请求 ID 仍然从上下文中提取

---

### 方向 B：统一索引持久化层（Unified Index Persistence Layer）

**业务价值**：消除 BM25 重启丢失问题；使混合搜索在冷启动后立即可用；为未来索引后端（Meilisearch、Tantivy）留出统一接入点。

**核心挑战**：
- BM25 状态是不可序列化为简单 JSON 的复杂数据结构（`bm25Doc` 含 token 频率 map）
- 写入存储 blob（`{tenant}/__bm25_state`）需要一个**原子快照**机制：序列化期间不能有并发写入
- 跨版本兼容性：序列化格式需要版本前缀，以便向前/向后兼容
- `BuildFromRepo` 和 `UpsertObjectChunks` 之间的竞态已在验证报告中提及，需要通过测试确认边界情况

**预期架构变更**：

```go
// 新增持久化接口
type PersistentIndex interface {
    Save(ctx context.Context, store storage.Storage, tenant string) error
    Load(ctx context.Context, store storage.Storage, tenant string) (bool, error) // bool: found
}

// BM25 实现 PersistentIndex
func (b *BM25) Save(ctx context.Context, store storage.Storage, tenant string) error {
    b.mu.RLock()
    defer b.mu.RUnlock()
    // 写入 storage blob: {tenant}/__index/bm25/v1/state
    return store.Write(ctx, tenant+"__index/bm25/v1/state", marshalBM25State(b))
}

// main.go 启动时
if bm != nil {
    found, err := bm.Load(ctx, store, tenant)
    if !found {
        go bm.BuildFromRepo(ctx, repo, tenant) // fallback to full rebuild
    }
}
```

**选项权衡**：

| 方案 | 优点 | 缺点 |
|------|------|------|
| **A: 存储 blob** | 复用现有 Storage 后端，无迁移 | 快照大小随索引增长；反序列化有内存压力 |
| **B: 数据库表** | 增量持久化，粒度细 | 需要新迁移（违反 I2 双文件规则）；Schema 复杂 |
| **C: LMDB/bbolt 嵌入式** | 进程内持久化，ACID | 引入新依赖；文件描述符管理 |
| **推荐：A** | 最简单，与 opt-in 原则一致 | 大型索引需分片 |

---

### 方向 C：内容感知渲染管线（Content-Aware Rendering Pipeline）

**业务价值**：Web UI 从调试工具变为可用产品；Agent 文件读取根据内容类型采用不同策略；缩略图端点被消费。

**核心挑战**：
- 不同媒体类型需要不同处理路径：图片（缩略图）、视频（首帧）、PDF（文本提取）、Markdown（渲染）
- 4MB 截断限制对 PDF/文本文件足够，但对视频/音频无意义
- 缩略图端点（`GET /files/*/thumbnail`）预设只支持 JPEG/PNG 输出，输入格式检测逻辑需要扩展
- Web UI 是 vanilla JS，没有自动路由或动态导入能力，需要手动处理每种类型

**预期架构变更**：

```
当前:
  showDetail(obj) → fetch('/v1/files/{key}') → body.slice(0, 4096)

建议:
  showDetail(obj) {
    const ct = obj.content_type;
    if (ct.startsWith('image/'))
      renderImage(obj.key);        // <img src="/v1/files/{key}/thumbnail?w=800">
    else if (ct.startsWith('text/') || ct === 'application/json')
      renderText(obj.key);         // fetch full text, render with syntax highlighting
    else if (ct === 'application/pdf')
      renderPDF(obj.key);          // <embed src="/v1/files/{key}#view=FitH" type="application/pdf">
    else
      renderFallback(obj.key);     // metadata + download link (current behavior)
  }
```

**选项权衡**：

| 方案 | 优点 | 缺点 |
|------|------|------|
| **A: 纯前端** | 零后端改动，快速迭代 | 需要浏览器能力检测；大文件不友好 |
| **B: 后端代理渲染** | 可做服务端转码（PDF→HTML） | 增加延迟；需要新的依赖（pdftohtml 等） |
| **推荐：A+B 渐进** | 先做前端类型路由（不引入后端依赖），再逐步添加后端渲染 | Web UI 文件量会增加（需要拆分 JS） |

---

### 方向 D：Webhook 死信状态机正规化（Dead-Letter State Machine Formalization）

**业务价值**：消除监控盲区；为 Webhook 失败提供可观测性和运营可见性；支持手动重试和死信队列检查。

**核心挑战**：
- 现有迁移文件已应用到生产环境，添加新迁移需要产生新的 `NNNN_*` 文件
- `NextPendingFailures` 的 SQL 查询依赖 `(succeeded, next_retry_at)` 索引，schema 变更需要重新索引
- 后端适配：SQLite 的 `INTEGER` 状态枚举和 Postgres 的 `INT` 需要保持一致
- `MarkWebhookSucceeded` 调用方需要审查（`retryOne` 和 `postOne` 的不同路径）

**预期架构变更**：

```sql
-- 新迁移: 0009_webhook_dead_letter.up.sql
ALTER TABLE webhook_failures ADD COLUMN status TEXT NOT NULL DEFAULT 'pending';
-- status 枚举: pending | delivered | dead_letter
-- ALTER TABLE webhook_failures ADD COLUMN updated_at TEXT;
UPDATE webhook_failures SET status = 'delivered' WHERE succeeded = 1;
UPDATE webhook_failures SET status = 'dead_letter' WHERE succeeded = 1 AND attempts >= 10;
-- 移除 old column (SQLite 不支持 DROP COLUMN，需重建表简化路径)
```

**接口变更**：

```go
// 新增的状态感知方法
type WebhookFailure struct {
    ID          int64
    EventID     int64
    URL         string
    Payload     string
    Attempts    int
    LastError   string
    LastStatus  int
    Status      string // "pending" | "delivered" | "dead_letter"
    NextRetryAt time.Time
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

---

### 方向 E：租户自助服务域（Tenant Self-Service Domain）

**业务价值**：减少运营负担；使租户可以自我管理 API keys、用量限额、通知偏好；Web UI 自适应为管理控制台。

**核心挑战**：
- 当前 scope 校验是二元的（`admin` 或非 admin）；需要引入更细粒度的 scope（`self-service:keys`、`self-service:usage`、`self-service:notifications`）
- `auth.Registry` 的 scope 验证需要在 REST router 中扩展，不能仅靠路由挂载位置隐式判定
- Web UI 需要新增配置面板（tab），与现有 search/detail/lineage/chat 并列
- 租户标识必须从中间件上下文传入，不能依赖路由段的 `{tenant}` 参数（防止跨租户越权）

**预期架构变更**：

```go
// router.go 新增自助路由组
r.Group(func(r chi.Router) {
    r.Use(mw.RequireScope("self-service")) // 任何非 admin 但已认证的 key 都满足
    r.Get("/me/usage", adm.MyUsage)
    r.Get("/me/keys", adm.MyKeys)
    r.Post("/me/keys", adm.MyAddKey)
    r.Delete("/me/keys/{id}", adm.MyRevokeKey)
    r.Get("/me/budget", adm.MyBudget)
    r.Put("/me/notifications", adm.MyNotifications)
})
```

**与 Web UI 的关系**：
- 新增第五个 tab（设置），展示租户上下文中的用量、key 列表、预算信息
- 无需 admin scope，使用当前请求中已认证的 tenant 标识

---

## 3. 接口设计建议

### 3.1 接口设计原则

| 原则 | 说明 | 示例 |
|------|------|------|
| **面向契约，非实现** | 接口定义调用方需要的行为，而非被调方的能力 | `Repository` 接口 80+ 方法，但建议按领域拆分 |
| **最小暴露** | 接口只暴露调用方需要的，非被调方所能提供的全部 | `ChunkSink` 接口仅 2 方法，恰好满足索引器的需要 |
| **一致性错误处理** | 所有接口使用相同错误语义层次 | `ErrNotFound`、`ErrDuplicate`、`ErrUploadNotFound` 应当被所有实现返回 |

### 3.2 当前接口分解评估

`Repository` 接口当前有约 80+ 方法，覆盖对象 CRUD、bucket 配置、multipart upload、chunks、events、jobs、API keys、tenants、audit 等多个领域。这种**大型接口**在 AGENTS.md 中被禁止（God 类型），Repository 接口本身虽然行数未超 300，但其方法数量导致了以下问题：

- 每次需要新操作时，必须同时在 SQLite 和 Postgres 实现中添加方法
- 测试 mock 实现时必须实现全部 80+ 方法
- 接口变更波及面大

**建议的领域拆分**：

```
Repository (facade - 可选保留)
├── ObjectRepository    → CRUD + versions + tags + ACL
├── BucketRepository    → config + lifecycle + CORS + logging
├── ChunkRepository     → chunks + search + embedding
├── JobRepository       → job queue + webhook failures
├── AuthRepository      → API keys + tenants
└── AuditRepository     → usage + audit log
```

### 3.3 新抽象层需求

**认证门面**（见方向 A）：提供一个包装器，将所有协议的认证/租户提取集中化。不再是每个协议各自处理。

```go
type AuthGateway struct {
    reg   *auth.Registry
    store repository.AuthRepository // 可选
}

func (g *AuthGateway) Wrap(handler http.Handler, opts WrapOptions) http.Handler
// opts 指定: 接受的认证方式 (APIKey | JWT | SigV4 | AnonymousRead)、
//            scopes 要求、是否从 header 提取 tenant
```

**索引持久化**（见方向 B）：与 `ChunkSink` 平行但不同的 seam，专注于索引快照。

```go
type IndexSnapshot interface {
    Save(ctx context.Context, store storage.Writer, id string) error
    Load(ctx context.Context, store storage.Reader, id string) (found bool, err error)
}
```

**内容类型路由**（见方向 C）：Agent 和 Web UI 的通用内容渲染接口。

```go
type ContentRenderer interface {
    Render(ctx context.Context, rc io.Reader, obj *repository.Object, w io.Writer) error
}
```

### 3.4 向后兼容性

| 变更类型 | 兼容策略 |
|----------|---------|
| 新增接口 | 新接口作为可选依赖注入，不影响现有代码（`WithXXX` 方法） |
| 扩展接口方法 | 仅在该方法被链路调用时才需要实现，且通过类型断言检查（`if rs, ok := repo.(ChunkSink); ok { ... }`） |
| 改动迁移 schema | 严格使用新增迁移文件，不编辑已有文件。双文件必须同步 |
| 移除废弃字段 | 至少保留一个 minor 版本周期的废弃兼容层 |

---

## 4. 技术选型

### 4.1 当前技术栈评估

| 层 | 当前选型 | 评估 | 是否需重选 |
|----|----------|------|-----------|
| 应用框架 | `chi/v5` | 轻量、主流、与 `net/http` 兼容 | 否 |
| 对象存储 | local / S3 / OSS / COS | 覆盖主流云厂商 | 否 |
| 关系数据库 | SQLite / Postgres | 覆盖两极部署场景 | 否 |
| 向量后端 | 内存暴力 / pgvector / Qdrant | 灵活分级 | 否 |
| LLM 适配 | HTTP provider | 兼容 OpenAI API 格式 | 否 |
| WebDAV | `golang.org/x/net/webdav` | 成熟标准库 | 否 |
| 全文检索 | 内存 BM25 / pgFTS | BM25 缺少持久化 | 需要改进，非替换 |
| 前端 | Vanilla JS | 零依赖，但扩展困难 | 建议逐步引入轻量框架 |

### 4.2 建议新增的技术栈

| 候选 | 用途 | 理由 | 风险 |
|------|------|------|------|
| `alpine.js` 或 `lit-html` | Web UI 组件化 | 比 Vanilla JS 更易维护；体积 < 10KB；无构建步骤 | 增加前端复杂性；不引入外部依赖中最低的一种 |
| `gojay` 或 `sonic` | JSON 序列化优化 | 适用于大对象的 BM25 状态快照序列化 | 现有 `encoding/json` 已足够；仅在 Profile 显示瓶颈时引入 |
| **无**新依赖用于 BM25 持久化 | 复用存储 blob | 零新增依赖；最小的改动面 | 反序列化慢于专用格式 |

### 4.3 第三方依赖评估标准

```
准入条件（全部满足）:
1. 使用 AGENTS.md 中的标准测试（testing 包、httptest、零 Docker）
2. 不增加 CI gate 的外部依赖
3. 许可证兼容（MIT/Apache 2.0/BSD）
4. 提供 Go 1.25 兼容构建

否决条件（任一满足）:
1. 需要在启动时下载远程模型或二进制
2. 无法通过 `go mod tidy` 干净拉取
3. CGo 依赖（除非在 opt-in 路径中）
```

### 4.4 自建 vs 采购决策矩阵

| 能力 | 自建依据 | 采购/集成前提 |
|------|----------|-------------|
| BM25 持久化 | 复用存储 blob，2-3 天开发量 | — |
| 内容预览 | 缩略图已有，仅需 Web UI 集成 | 如果需要 OCR/PDF 转换，可以集成 `tesseract`/`pdftohtml` 作为 opt-in |
| 死信队列可视化 | REST 端点已有，仅需 schema 调整 | — |
| 自助 API 管理 | 完全在 auth + REST 层内实现 | — |
| 文档预览（Office） | 复杂度高，建议集成 Collabra/LibreOffice | 容器化部署时作为 sidecar |

---

## 5. 实施路线图

### 5.1 优先级矩阵

| 方向 | 影响面 | 风险 | 开发量 | 优先级 |
|------|--------|------|--------|--------|
| **A: 统一认证门面** | P0 安全漏洞 | 中间件包装器变更，可能影响请求链路 | 中等（5-7 文件） | **P0** |
| **D: 死信状态机** | 监控盲区 + 数据契约违约 | 迁移需要仔细回滚 | 低（5-6 文件） | **P1** |
| **B: BM25 持久化** | 混合搜索冷启动退化 | 序列化竞态需确认 | 中低（3-4 文件） | **P1** |
| **C: 内容预览** | Web UI 可用性受限 | 纯前端，影响范围受控 | 高（10+ 文件） | **P2** |
| **E: 自助 API** | 运营负担、缺少产品化能力 | scope 扩展需谨慎 | 高（8-12 文件） | **P2** |

### 5.2 阶段划分

#### 🚩 阶段 1：安全与可观测性修复（2-3 周）

| Sprint | 内容 | 交付物 |
|--------|------|--------|
| 1.1 | **认证门面 v1**：将 MCP 移到中间件链内；修复 WebDAV 通过 buildDispatcher 前的包装 | 所有协议端点经过 auth 中间件；MCP stdio 保留无 auth（文档声明） |
| 1.2 | **死信状态机迁移**：新增 migration 0009；`status` 枚举 + `updated_at`；废弃 `succeeded` 兼容层 | `ListWebhookFailures` 返回状态枚举；监控可以按 `status=dead_letter` 告警 |
| 1.3 | **回归测试**：为所有协议添加 auth bypass 测试（`internal/api/auth_bypass_test.go`） | CI gate 包含协议适配器的 auth 渗透测试 |

**风险点**：
- WebDAV 的 `x/net/webdav.Handler` 内部 ServeHTTP 模式与 chi 中间件不兼容，可能需要进一步封装
- 缓解策略：在 `buildDispatcher` 中对 WebDAV 路径先应用 auth + tenant 中间件，再分发给 `davH`

#### 🚩 阶段 2：索引持久性与数据完整性（2-3 周）

| Sprint | 内容 | 交付物 |
|--------|------|--------|
| 2.1 | **BM25 Save/Load 实现**：序列化格式设计（protobuf 或 JSON 流）、存储 blob 路径约定、版本前缀 | `PersistentIndex` 接口实现；启动时 Load 回退到 BuildFromRepo |
| 2.2 | **竞态测试**：`BuildFromRepo` 与 `UpsertObjectChunks` 并发写入测试 | 测试确认现有的 `sync.RWMutex` 足以防止数据丢失；若不足，增加 `drain` 机制 |
| 2.3 | **混合搜索冷启动行为文档化**：当 BM25 不可用时，Search.Query 的行为模式 | 降级到的向量搜索模式有明确日志记录；客户端可选等待 BM25 就绪 |

**风险点**：
- `BuildFromRepo` 在分页循环中遍历所有存储桶/对象，可能在大索引上耗时数分钟，期间 Save 可能拿到不一致快照
- 缓解策略：Save 仅在 BuildFromRepo 完成后触发，或实现增量 checkpoint

#### 🚩 阶段 3：Web UI 与自助服务（3-4 周）

| Sprint | 内容 | 交付物 |
|--------|------|--------|
| 3.1 | **内容预览 v1**：Web UI 按 content-type 切换显示（图片用 img 标签 + 缩略图，文本用 pre，PDF 用 embed） | 无需新端点；缩略图端点被消费 |
| 3.2 | **自助 API 组**：新增 `/me/` 路由组 + scope 检查；Web UI 添加配置 tab | 租户可在 Web UI 中管理 API keys、查看用量、设置预算告警 |
| 3.3 | **MCP 读文件增强**：超过 4MB 的文件支持 Range 请求（已存在服务端能力） | Agent 大文件读取不再截断 |

**风险点**：
- Web UI 从 Vanilla JS 重构为组件化（alpine/lit）会导致代码膨胀和回归
- 缓解策略：保持增量演进，先添加类型分派逻辑，再分批引入框架

### 5.3 长期演进方向（v2.0+）

```
┌──────────────────────────────────────────────────┐
│ v2.0 能力                                          │
├──────────────────────────────────────────────────┤
│ • 认证门面 v2：引入 OAuth2/OIDC 第三方身份提供商   │
│ • 索引 v2：BM25 → Meilisearch/Tantivy 适配器      │
│ • Web UI：组件化 + 暗模式 + 移动端适配             │
│ • 死信可视化：Webhook 失败 Dashboard panel         │
└──────────────────────────────────────────────────┘
```

### 5.4 风险矩阵总览

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| MCP 修复后破坏 Claude Desktop 集成 | 中 | 高 | 保留 `runMCP` stdio 路径不变；仅 HTTP 端点加 auth |
| WebDAV 客户端缓存旧的 401 后无法连接 | 低 | 中 | 文档标注为 breaking change |
| BM25 序列化格式变化导致回滚失败 | 中 | 高 | 序列化格式含版本前缀；旧格式可读 |
| `Repository` 接口新增方法导致 all mock implementations 未更新 | 低 | 高 | 在 AGENTS.md I2 中添加显式的 mock 更新检查 |
| 自助 scope 实现引入权限提升漏洞 | 低 | P0 | 代码审查 + 渗透测试；scope 校验在中间件层执行，不在 handler 中 |

---

## 总结

当前架构在分层和抽象方面表现出色，但在**安全边界一致性**和**数据语义严谨性**方面存在可修复的缺陷。所有五项发现均已通过代码验证，且相互独立，可以在不同开发周期中并行或串行处理。

**核心建议**：先修复认证盲区（P0），因为这不仅是一个安全漏洞，还暴露了一个结构性缺陷——协议适配器各自的注册模式不一致。死信状态机和 BM25 持久性修复成本低，可在同一 Sprint 内解决。内容预览和自助 API 影响面更大，应作为独立的产品化里程碑来规划。
