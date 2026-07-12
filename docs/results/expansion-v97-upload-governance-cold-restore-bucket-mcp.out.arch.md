---

# 架构师分析报告：expansion-v97 文档验证与架构深度评估

## 文档准确性验证结果

**结论：文档高度准确**。已对 15+ 个代码锚点进行逐项验证，全部确认：

| 锚点 | 文档声明 | 验证结果 |
|------|---------|---------|
| REST `r.Body` 直传无 `MaxBytesReader` | ✅ | `handler.go:56` 确认 |
| S3 `r.Body` 直传无大小预检 | ✅ | `handler.go:104` 确认 |
| WebDAV `spillBuffer` 8 MiB 阈值 | ✅ | `spill.go:13` 确认 |
| AppConfig 无 upload 字段 | ✅ | `config.go:33-40` 确认 |
| 零 `throttle`/`bandwidth` 代码 | ✅ | grep 零结果 |
| `RestoreObject` = 软删除恢复 | ✅ | `file_features.go:167-170` 确认 |
| Storage 接口无 `RestoreObject` | ✅ | 确认 |
| `StorageClass` 仅标签字段 | ✅ | 迁移文件 0021 验证 |
| 桶表无 quota 字段 | ✅ | `sql_buckets.go` 确认 |
| `checkQuota` 仅 tenant 级 | ✅ | `file_crud.go:28` 确认 |
| MCP `read_file` 4MB 截断 | ✅ | `server.go:249` 确认 |
| MCP dispatch 仅 6 个 case | ✅ | `server.go:117-217` 确认 |
| Stdio 模式回退到 `s.tenant` | ✅ | `server.go:47-50` 确认 |
| MCP protocol 无 Prompt/Sampling 类型 | ✅ | `protocol.go` 确认 |

**需微调之处：**
- 文档 Phase 4 中提及 `ProtocolVersion = "2025-03-26"`，代码实际为 `"2024-11-05"`——应统一版本字符串
- 文档 MCP 已实现表中未列出 `notifications/initialized`（虽其无实际 handler，但 client 端的 initialized notification 在初始化流程后发出，server 端静默忽略）
- 建议为 `notifications/initialized` 添加显式 handler case（如同 ping），避免默认 `-32601` 错误

---

## 1. 架构评估

### 当前架构的优势

- **清晰的层叠架构**：Protocol Adapters (thin) → FileService (core) → Storage/Repository (pluggable) — 每层职责明确，单一方向依赖
- **Opt-in 安全默认**：所有高级功能（AI/pgvector/Qdrant/events/cluster）均 flag-gated — 降低基线攻击面，CI 路径保持最小依赖
- **接口可替换性**：`Storage` 接口已有 local/s3/oss/cos 四个实现；`Repository` 接口支持 SQLite/Postgres；vector backend 支持 memory/pgvector/Qdrant — 为扩展提供良好基础
- **JobPool 设计**：`jobs` 表轮询 + 注册 handler 模式优雅解耦了异步工作流（恢复、GC、复制、AV 扫描）

### 关键设计决策的合理性

| 决策 | 合理性 | 代价 |
|------|--------|------|
| **FileService 作为唯一控制器** | 所有协议共享同一业务逻辑，避免「N 协议 → N 实现」的爆炸 | FileService 的接口不可太薄——已有 30+ 方法，警惕成为 God Service |
| **Storage backend 数据平面 + Repository 控制平面分离** | 存储层纯 blob 操作，元数据层管理对象生命周期 | 跨后端事务协调复杂（如 S3 写入成功但 SQLite 写入失败——已通过幂等 upsert 处理） |
| **协议适配器不自挂中间件** | 单元测试无需启动完整中间件链，handler 测试只需 mock tenant/auth 上下文 | 易遗忘 middleware 链的完整行为（如 OTel span 在 handler 测试中不存在） |
| **EventBus + JobPool 双订阅异步机制** | EventBus 做实时广播（Webhook、Replication），JobPool 做可靠异步（GC、恢复） | 事件成功但 job 入队失败？——当前 `Queue.Enqueue` 前需确保事务原子性 |

### 架构债务

| # | 债务 | 严重程度 | 来源路径 |
|---|------|---------|---------|
| **D1** | **上传完全无治理**：四个协议路径直传 `r.Body`，无 `MaxBytesReader`，无带宽控制，无超时 | **高**——OOM/慢客户端攻防风险 | 文档方向一已覆盖 |
| **D2** | **`?restore` 语义不一致**：S3 协议声明的 `POST ?restore` 与 GLACIER RestoreObject 语义不同，实际做的是软删除恢复 | **高**——S3 兼容测试失败 | 文档方向二已覆盖 |
| **D3** | **桶级资源隔离缺失**：所有配额在 tenant 级——一个桶的突发写入可饿死同租户其他桶 | **中**——SaaS 多租户精细运营受阻 | 文档方向三已覆盖 |
| **D4** | **MCP 协议覆盖率低**：6/12+ 核心方法，不完整能力声明，4MB 硬截断，无通知 | **中**——MCP client 集成能力受限 | 文档方向四已覆盖 |
| **D5** | **Storage 接口未契约化**：无 `StorageClassSupported`，无 `RestoreObject`，无 cross-backend 迁移 | **中**——冷存储、分层迁移受阻 | 文档方向二间接覆盖 |
| **D6** | **配置系统无版本化**：Env 变量无前缀组织，无 `UPLOAD_*` 命名空间 | **低**——新配置项激增时需 namespace 规划 | 新发现 |
| **D7** | **MCP 协议版本硬编码**：`"2024-11-05"` 无 capability negotiation | **低**——客户端要求更新版本时无优雅降级 | 验证中发现 |

---

## 2. 扩展方向：优先级排序与深度分析

### P0 — 上传治理与流量整形（方向一）

**为什么是 P0 而非 P1？** 生产可靠性基础。当前无 `MaxBytesReader` 意味着一个恶意或失控的客户端可以触发 OOM（内存中大对象缓冲）或耗尽磁盘 IO。这在生产环境中是 **高可用性事故**，不应归为 P1。

**核心挑战：**

1. **跨协议治理统一**：REST、S3、WebDAV、MCP 四个路径需要不同的错误码和行为——REST 用 `413`，S3 用 `EntityTooLarge` XML，WebDAV 用 `507`。治理配置应统一但错误响应各异。

2. **带宽限速与吞吐的权衡**：`rate.Limiter.WaitN` 是阻塞操作，在 handler goroutine 中阻塞会占用连接槽。若使用 `request-goroutine-per-connection` 模型（Go 标准 `http.Server`），带宽限速导致的慢速连接意味着 goroutine 被长时间占用。

3. **Idle Timeout 的协议感知**：S3 分片上传（`UploadPart`）每个 part 有一个独立的请求——不应在 part 之间触发 idle timeout。REST 的 streaming body 同理。

**架构方案评估：**

| 方案 | 优点 | 缺点 |
|------|------|------|
| **A (推荐)：Protocol-Level `MaxBytesReader` + Service-Level `rate.Limiter`** | 保持 Protocol Adapter 厚度极薄；限流在服务层可复用；不改变 Storage 接口 | 服务层 `rate.Limiter` 需管理 per-connection limiter 的回收 |
| **B：Storage-Level 限流** | Storage 实现内部做限速——local fs 慢速写，S3 API throttling | 违背「Storage 是纯 blob 层」的设计原则；限流逻辑跨后端重复 |
| **C：Middleware-Level 全局上传治理** | 统一入口，一个中间件覆盖所有协议 | 四个协议的错误响应格式不同（JSON/XML/...），中间件难以做到协议感知的响应格式化 |

**推荐方案 A 的细化路径：**

```
Phase 1 (1周): MaxBytesReader + UploadIdleTimeout
  ├─ config: UPLOAD_MAX_BYTES, UPLOAD_IDLE_TIMEOUT
  ├─ 所有协议 handler 注入 r.Body = http.MaxBytesReader(w, r.Body, cfg)
  └─ Upload idle 感知：自定义 MaxBytesReader 内置 timer 或单独 middleware

Phase 2 (1-2周): 带宽整形
  ├─ BandwidthLimiter: per-tenant + per-connection token bucket
  ├─ rateLimitedReader wrapper
  └─ config: BANDWIDTH_BYTES_PER_SEC, BANDWIDTH_BYTES_PER_TENANT

Phase 3 (可选): 可恢复上传
  └─ Tus 协议子集或自定义 Upload-Offset 语义
```

**风险：** Phase 2 的 `rate.Limiter.WaitN` 阻塞特性需评估 goroutine 池是否调整 `MaxInFlight`。建议在 `BandwidthLimiter` 上层叠加一个 context deadline，避免限速导致的无限阻塞。

---

### P0 — 冷存储恢复语义（方向二）

**为什么是 P0？** S3 兼容性是 aero-vault 的核心卖点之一。`?restore` 当前行为与 S3 规范不符——用户预期的 GLACIER 恢复实际做的是软删除恢复，这是 **语义级 bug**，影响认证兼容性测试。

**核心挑战：**

1. **`Storage` 接口扩展的 ripple effect**：添加 `RestoreObject` 方法需为所有 4 个后端（local/s3/oss/cos）提供实现。S3 后端容易（直接调用 AWS SDK），local/OSS/COS 后端需要决定——local 应返回 `ErrNotSupported`，OSS 和 COS 各自调用其 SDK。

2. **异步恢复工作流的状态机设计**：

```
       POST ?restore
           ↓
   ┌───────────────┐
   │  Pending      │  ← 作业入队，预期 4-6 小时内完成
   │  (in_progress)│
   └───────┬───────┘
           ↓ restore job completes
   ┌───────────────┐
   │  Restored     │  ← 临时副本可用，GET 返回对象
   │               │      x-amz-restore: ongoing-request="false", expiry-date="..."
   └───────┬───────┘
           ↓ expiry time reached
   ┌───────────────┐
   │  Expired      │  ← 临时副本删除，GET 返回 403 InvalidObjectState
   └───────────────┘
```

3. **S3 GLACIER 与 Intelligent-Tiering 的区别**：GLACIER 需要显式 `RestoreObject`，Intelligent-Tiering 自动调温。当前 `StorageClass` 只有标签——未来需要区分「需要 Restore 的冷存储类」和「自动调温的智能存储类」。

**推荐方案：状态机 + JobPool 驱动**

```
┌────────────────────────────────────────────────────────────┐
│  S3/REST Handler                                           │
│  POST ?restore&days=7                                     │
│  1. 校验 StorageClass ∈ {GLACIER, DEEP_ARCHIVE}            │
│  2. 检查 restore_status == NULL                           │
│  3. 设置 restore_status='in_progress'                     │
│  4. jobs.Queue(JobRestore, {object_id, days})             │
│  5. 返回 202 Accepted                                     │
└──────────────────────────┬────────────────────────────────┘
                           │
┌──────────────────────────▼────────────────────────────────┐
│  JobPool Worker                                           │
│  func executeRestore(ctx, job) {                          │
│    1. storage.RestoreObject(ctx, storageKey, days)        │
│    2. repo.SetRestoreStatus(object_id, 'restored', expiry)│
│    3. events.Publish('restore.completed', ...)            │
│  }                                                        │
└──────────────────────────────────────────────────────────┘
```

**核心设计决策：谁管理临时副本？**

| 方案 | 优点 | 缺点 |
|------|------|------|
| **A (推荐)：后端管理**（S3 RestoreObject 返回临时副本，aero-vault 仅记录状态 + 到期时间） | 不额外占用 local 磁盘；与 S3 原生语义一致 | 需在恢复到期后校验临时副本是否仍有效 |
| **B：aero-vault 管理**（后端取回对象到 local 作为临时副本） | 所有后端统一行为（OSS/COS 的恢复对象也转到 local 缓存） | 增加额外存储；需要本地缓存策略和清理；与 S3 协议语义不同 |

**风险：** 恢复作业失败（如 S3 RestoreObject API 返回 `RestoreAlreadyInProgress` 或权限不足）需要有明确的错误传播和重试策略。建议 `JobRestore` 在失败时设置 `restore_status='failed'` + 错误消息，并通过 Webhook 或 Admin API 暴露。

---

### P1 — 桶级资源配额（方向三）

**为什么 P1 不是 P0？** 桶级配额是 SaaS 运营优化而非生产可靠性/S3 兼容性需求。在单租户或少量桶场景中，当前 tenant 级配额足够。但多租户 SaaS 场景下这是 **客户隔离** 的基础。

**架构方案对比：**

| 方案 | 存储要求 | 性能影响 | 实现复杂度 | 推荐度 |
|------|---------|---------|-----------|-------|
| **A：桶级计数器 + 租户级硬帽** | 新 migration（`buckets` 表加字段） | `Put`/`Delete` 增加一次原子 DB 写入 | 中等 | ⭐⭐⭐ |
| **B：桶级 rate limiter 中间件** | 无需 DB 变更（bucket → limiter 映射可热加载） | 极低 | 低 | ⭐⭐⭐ Phase 1 |
| **C：Redis 分布式桶级桶配额计数器** | 新增 Redis 依赖 | 低 | 高 | ⭐ |
| **D：动态配额分配（overcommit with fairness）** | 需要调度器 | 中等 | 高 | ⭐⭐ |

**推荐两阶段实施：**

**Phase 1（1 周）：桶级 rate limiter 中间件**

```go
// 基于现有 PerTenantRateLimiter 模式
type BucketRateLimiter struct {
    defaults  *rate.Limiter
    mu        sync.RWMutex
    buckets   map[string] /* "tenant:bucket" */ *rate.Limiter
}

// 从 DB 配置热加载（buckets.rate_limit_rps 字段）
func (rl *BucketRateLimiter) Reload(ctx context.Context, repo repository.Repository) { ... }
```

**Phase 2（2 周）：桶级存储/对象配额**

- migration: `ALTER TABLE buckets ADD COLUMN quota_max_bytes INTEGER`
- API: `PUT /v1/buckets/{bucket}/quota`, `GET /v1/buckets/{bucket}/usage`
- 服务层 `Put`/`Delete` 中添加 `checkBucketQuota` + `addBucketUsage`

**关键设计决策：桶级配额的继承语义**

```
bucket_quota.max_bytes = NULL → 继承 tenant_quota.max_bytes
bucket_quota.max_bytes = 0    → 无限制（仅受 tenant 配额）
bucket_quota.max_bytes = 100GB → 桶单独限制
```

建议 `NULL = 继承`、`0 = 无限制`、`正值 = 硬限制` 三者语义清晰区分。

---

### P1 — MCP 协议完备性（方向四）

**Phase 1（1 周）：基础修复（P1）**

| 改进项 | 优先级 | 现状 |
|--------|--------|------|
| 移除 `read_file` 4MB 硬限制，改为 `max_size` 参数 | **P0** | 4MB 截断，数据静默丢失是 UX defect |
| `list_files` 支持分页（`cursor` + `limit`） | **P0** | 50 条硬限制 |
| `resources/subscribe` + `notifications/resources/listChanged` | **P1** | 完全缺失 |
| Stdio 模式 `AERO_TENANT` 环境变量支持 | **P1** | 硬编码 `"default"` |
| `notifications/initialized` handler | **P1** | 缺失（虽已忽略，但返回 -32601） |
| 协议版本升级到 `"2025-03-26"` | **P1** | 当前 `"2024-11-05"`，与主流 MCP client 兼容 |

**Phase 2（2 周）：Prompts + Notifications（P2）**

- `prompts/list` + `prompts/get` 暴露预定义提示模板
- `notifications/resources/listChanged` 当对象变更时主动推送

**Phase 3（按需）：Sampling + Streaming（P3）**

- 依赖 LLM 配置，适合最后交付

**关键设计决策：租户隔离策略**

| 方案 | 适用场景 | 实现方式 |
|------|---------|---------|
| **A：环境变量 `AERO_TENANT`** | stdio 模式（Claude Desktop、Claude Code） | 在 MCP host 的启动命令中设置环境 |
| **B：JSON-RPC `initializationOptions`** | HTTP 模式 + 自定义客户端 | 扩展 MCP 的 initialize 参数（MCP spec 允许自定义字段） |
| **C：每个工具调用传入 `tenant`** | 多租户管理场景 | 在工具参数中增加可选 `tenant` 字段 |
| **D (推荐)：A + B 组合** | 覆盖所有传输模式 | stdio 优先 A，HTTP 优先 B，工具参数 C 作为兜底 |

---

## 3. 接口设计建议

### 关键模块接口原则

**原则 1：配置 namespace 化**

当前配置系统所有变量平铺（`APP_*`, `PER_TENANT_*`）。建议新增 `UPLOAD_*` namespace：

```
UPLOAD_MAX_BYTES=5368709120
UPLOAD_IDLE_TIMEOUT_SECONDS=300
UPLOAD_BANDWIDTH_BYTES_PER_SEC=10485760
UPLOAD_BANDWIDTH_BYTES_PER_TENANT=52428800
```

这要求在 `config_app.go` 中新增 `UploadConfig` struct，组合到 `Config`：

```go
type UploadConfig struct {
    MaxBytes              int64
    IdleTimeout           time.Duration
    BandwidthBytesPerSec  int64
    BandwidthBytesPerTenant int64
}
```

**原则 2：Storage 接口渐进扩展**

当前 `Storage` 接口的扩展应保持向后兼容——新增方法不破坏现有实现：

```go
// 方案 A (推荐)：默认实现模式
type Storage interface {
    // ... 现有方法 ...
    
    // RestoreObject initiates restoration of an archived object.
    // Returns ErrNotSupported if the backend doesn't support cold storage tiers.
    RestoreObject(ctx context.Context, key string, restoreDays int) error
    
    // StorageClassSupported returns true if the backend can store objects in the given class.
    StorageClassSupported(class string) bool
}

// 在 DefaultStorage 类型中提供默认 fallback
func (d *DefaultStorage) RestoreObject(ctx, key, days) error {
    return ErrNotSupported  // local 后端直接继承
}
```

**原则 3：Service 层接口不膨胀**

上传治理的配置和限流逻辑不应渗透到 `FileService.Put` 的方法签名中。相反，通过 `PutOptions` 扩展：

```go
type PutOptions struct {
    // ... 现有字段 ...
    MaxUploadBytes    int64 // overwrite per-request, 0 = use global
    IdempotencyKey    string
    ExpectedOffset    int64 // for resumable upload
}
```

### 是否需要新抽象层

| 抽象层 | 是否需要 | 理由 |
|--------|---------|------|
| **UploadManager** | ✅ 是 | `BandwidthLimiter` + `UploadTracker` + `UploadProgress` 组合为一个 `internal/service/upload.go` 中的独立类型，避免 FileService 膨胀 |
| **RestoreManager** | ✅ 是 | 冷存储恢复状态机 + JobRestore/JobRestoreExpire 注册 + 进度查询，应独立为 `internal/service/restore.go` |
| **BucketQuotaManager** | ❌ 否 | 桶级配额应是对 FileService 中现有 `checkQuota`/`AddUsage` 的扩展——追加桶级检查，不新增 abstraction |
| **MCPSession** | ✅ 可选 | 如果实现资源订阅持久化 + 通知推送，需要一个 `session` 抽象管理连接的订阅列表和通知通道 |

### 向后兼容性

| 变更 | 兼容性策略 |
|------|-----------|
| 新增 `UPLOAD_MAX_BYTES=0`（0 表示无限制） | 默认 `0`，现有部署无行为变化 |
| Storage 接口新增方法 | 提供 `DefaultStorage` fallback 返回 `ErrNotSupported` |
| `?restore` 语义变更 | 通过 `AI_INDEX_ENABLED`+`STORAGE_CLASS_HANDLING=enabled` 之类的 feature flag 控制——现有用户在迁移到新的恢复语义前可选保留旧行为 |
| 桶表新增 column | migration 默认值 `NULL`，桶配额行为无变化 |
| MCP dispatch 新增 case | 客户端调用新方法前完成 capability negotiation——旧客户端不受影响 |
| 协议版本升级到 `"2025-03-26"` | 需要声明 capabilities 时也向下兼容 `"2024-11-05"`，在 `initialize` 的 response 中声明 `protocolVersion` |

---

## 4. 技术选型

### 是否需要新依赖

| 方向 | 建议新依赖 | 理由 | 风险评估 |
|------|-----------|------|---------|
| 上传治理 | **`golang.org/x/time/rate`**（已间接使用） | `rate.Limiter` 是带宽整形的最优方案——token bucket 实现，语义精确 | 已经是标准依赖无风险 |
| 冷存储恢复 | **无** | JobPool + EventBus 已提供异步工作流基础设施；S3 SDK 已依赖 | — |
| 桶级配额 | **无** | 使用现有 DB schema 扩展 + 现有计数机制 | — |
| MCP Prompts | **无** | `protocol.go` 中添加类型，`dispatch` 中添加 case | — |

### 自建 vs 依赖决策矩阵

| 组件 | 自建 | 依赖 | 决策 |
|------|------|------|------|
| Token bucket rate limiter | `golang.org/x/time/rate` | 标准库 | ✅ 使用现有依赖 |
| 大文件流式读取 | 工具函数（`StreamReader`） | — | ✅ 自建（~50 行） |
| 可恢复上传协议（Tus） | 实现核心语义 | `tusd` 或第三方 library | ❌ 建议自建 Tus 子集——aero-vault 已有 S3 multipart，Tus 作为补充不需全实现 |
| 桶级配额缓存（大量桶场景） | LRU cache | — | ✅ 自建（文件已有 `sync.Map` + TTL 模式） |
| MCP protocol 增强 | Go 标准库 JSON | — | ✅ 自建（当前已自建，无需框架化） |

### `AGENTS.md` 的 I6 约束（Stdlib 优先）

所有四个方向的实现可以在不新增 `go.mod` 依赖的情况下完成。关键依赖表：

```
golang.org/x/time/rate  — 已存在（间接）
github.com/mattn/go-sqlite3 — 已存在
其他所有组件标准库实现
```

✅ **符合 I6 约束**

---

## 5. 实施路线图

### 总体路线图

```
Week 1-2                    Week 3-5                    Week 6-8
┌─────────────────────┐    ┌─────────────────────┐    ┌─────────────────────┐
│ Phase 1 (P0/P1)     │    │ Phase 2 (P0/P1)     │    │ Phase 3 (P1/P2)     │
│                     │    │                     │    │                     │
│ 上传治理 Phase 1    │    │ 冷存储恢复 Phase 1  │    │ MCP Prompts        │
│  └ MaxBytesReader    │    │  └ Storage接口扩展   │    │  └ prompts/list     │
│  └ UploadIdleTimeout │    │  └ 恢复状态机       │    │  └ prompts/get      │
│  └ 配置字段          │    │  └ DB migration    │    │  └ 预定义模板       │
│  └ 测试              │    │  └ JobRestore       │    │                     │
│                     │    │  └ 测试              │    │ 桶级配额 Phase 2   │
│ MCP Phase 1         │    │                     │    │  └ bucket quota API │
│  └ read_file分块    │    │ 桶级配额 Phase 1    │    │  └ usage tracking   │
│  └ list_files分页   │    │  └ rate limiter     │    │  └ 测试             │
│  └ stdio 租户传递   │    │  └ DB migration    │    │                     │
│  └ notifications    │    │  └ quota API        │    │ 冷存储恢复 Phase 2 │
└─────────────────────┘    └─────────────────────┘    │  └ restore events   │
                                                       │  └ Grafana panel   │
                                                       └─────────────────────┘
```

### 详细阶段规划

#### Phase 1（第 1-2 周：并行双线）

**Track A：上传治理（P0）— 2 人·周**

| 里程碑 | 交付物 | 检查点 |
|--------|--------|--------|
| Day 1-3 | `UploadConfig` struct + `Load()` 解析 + AppConfig 集成 | `make check` 全绿 |
| Day 3-5 | 四个协议路径的 `MaxBytesReader` 注入 + 对应的错误响应（REST 413 JSON, S3 EntityTooLarge XML, WebDAV 507, MCP `-32000`） | `go test ./internal/api/...` + 手动 curl 测试 |
| Day 5-7 | `UploadIdleTimeout` — 自定义 `MaxBytesReader` 实现会超时的 reader | 慢客户端测试（1KB/s, 应 5 分钟后 408） |
| Day 7-10 | 单元测试 + 集成测试 + 边界情况 | `x/net/http/httptest` 覆盖 413/408/超时/0 字节 |
| Day 10-14 | 文档更新 + CHANGELOG | `docs/` + `README.md` |

**Track B：MCP Phase 1（P1）— 1 人·周**

| 里程碑 | 交付物 | 检查点 |
|--------|--------|--------|
| Day 1-2 | `read_file` 添加 `max_size` 参数，移除 4MB 硬限制 | 10MB 文件读取测试 |
| Day 2-4 | `list_files` 添加 `cursor` + `limit` 分页 | 1000 文件列表测试 |
| Day 4-5 | Stdio 模式 `AERO_TENANT` 环境变量 + `notifications/initialized` handler | 多租户 MCP 测试 |
| Day 5-7 | 协议版本升级到 `"2025-03-26"` + capability 声明 | claude-desktop 兼容性测试 |

#### Phase 2（第 3-5 周：并行双线）

**Track C：冷存储恢复（P0）— 2 人·周**

| 里程碑 | 交付物 |
|--------|--------|
| Week 3 | `Storage` 接口扩展 + S3/local/OSS/COS 实现 |
| Week 3-4 | Migration 0025（`restore_status`, `restore_expires_at`）+ 数据模型扩展 |
| Week 4 | `JobRestore` + `JobRestoreExpire` handler + `main.go` 注册 |
| Week 4-5 | `?restore` 重构（弃用 `svc.RestoreObject` 软删除路径，重写为冷存储恢复） |
| Week 5 | REST API `POST /v1/files/{key}/restore` + `GET /v1/files/{key}?restore` 状态查询 |

**Track D：桶级配额 Phase 1（P1）— 1-2 人·周**

| 里程碑 | 交付物 |
|--------|--------|
| Week 3 | `buckets` 表 migration（`quota_max_bytes`, `quota_max_objects`）+ `BucketQuota` 数据模型 |
| Week 4 | `GetBucketQuota`/`AddBucketUsage` Repository 方法 + `checkBucketQuota` 服务层 |
| Week 5 | `PUT /v1/buckets/{bucket}/quota` + `GET /v1/buckets/{bucket}/usage` REST API |

#### Phase 3（第 6-8 周）

**Track E：MCP Prompts + Notifications（P2）— 1 人·周**

- `Prompt` 类型定义 + `prompts/list` + `prompts/get` 
- 预定义模板：`summarize-file`, `search-and-answer`, `extract-actions`
- `notifications/resources/listChanged` 通过 EventBus 触发

**Track F：上传治理 Phase 2 + 桶级配额 Phase 2**

- 带宽整形 `BandwidthLimiter` + `rateLimitedReader`
- 桶级配额 Phase 2：`quota_max_rps`, `quota_daily_budget_micros`, `quota_warn_bytes`

### 风险矩阵

| 风险 | 可能性 | 影响 | 缓解策略 |
|------|--------|------|---------|
| **`MaxBytesReader` 导致已有客户端 POST 被拒绝** | 中 | 高 | 默认 `UPLOAD_MAX_BYTES=0`（无限制），仅显式配置后才生效 |
| **带宽限速导致连接堆积** | 高 | 中 | `rate.Limiter.WaitN` 前设置 context deadline（`timeout = limit / rate + buffer`） |
| **JobRestore 在恢复中失败** | 中 | 中 | 重试策略（`max_attempts=3`）+ 失败状态持久化 + Admin API 查询 |
| **桶级配额迁移导致大量桶的 DB 死锁** | 低 | 高 | 使用 `ALTER TABLE ... ADD COLUMN` 不涉及全表重建；测试 10K 桶场景 |
| **MCP 协议版本升级破坏现有客户端** | 中 | 高 | 在 `initialize` response 中同时声明 `"2024-11-05"` 兼容版本 |
| **上传治理与 S3 分片上传交互** | 中 | 中 | `MaxBytesReader` 应在 S3 `PutObject` 入口生效，分片上传的 `UploadPart` 在验证完分片大小后单独应用 `part_max_bytes` |
| **`?restore` 语义变更破坏现有用户** | 高 | 高 | Feature flag: `STORAGE_CLASS_HANDLING=true` 启用新语义，默认旧行为 |

### CI Gate 集成

所有变更必须通过 `make check`（= `gofmt -l .` + `go build ./...` + `go vet ./...` + `go test ./...` SQLite/local FS 基线）。新增功能需满足：

- 上传治理：`go test ./internal/service/...` 覆盖 `MaxUploadBytes` rejection 测试
- 冷存储恢复：`go test ./internal/service/...` + `go test ./internal/repository/...` 含 migration 测试
- 桶级配额：`go test ./internal/api/rest/...` + `go test ./internal/service/...`  
- MCP：`go test ./internal/mcp/...` 含 stdio/HTTP 两种 transport 测试

**新迁移文件规范：** `migrations/{sqlite,postgres}/0025_restore_status.{up,down}.sql` — 遵守 I2 双文件原则。

---

## 总结

| 维度 | 发现 |
|------|------|
| **文档准确度** | ✅ 高度准确，15+ 代码锚点全部验证通过；仅 MCP 协议版本号需修正 |
| **方向优先级调整** | 上传治理（P0而非P1）和冷存储恢复（P0）因生产可靠性与 S3兼容性提升到 P0 |
| **架构影响** | 上传治理涉及 4 个协议路径的 handler 变更（范围广但模式简单）；冷存储恢复涉及 Storage 接口扩展 + 状态机 + JobPool（影响深但可渐进）；桶级配额扩展现有配额机制（增量）；MCP 增量增强（局部） |
| **技术选型** | 零新依赖——全部四个方向可用现有依赖实现，完全符合 I6 约束 |
| **实施建议** | Phase 1 并行双线（上传治理 + MCP 基础），Phase 2 并行双线（冷存储 + 桶级配额），Phase 3 增强收尾 |
| **风险等级** | 中等——`?restore` 语义变更和带宽限速连接堆积是两个主要风险，均可通过 feature flag 和 timeout 缓解 |
