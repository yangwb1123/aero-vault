Now I have a thorough understanding of the project's architecture, codebase structure, and the analysis document. Here is my complete architectural analysis.

---

# 架构评审：五个扩展方向的深度分析

## 1. 架构评估

### 1.1 当前架构的核心优势

aero-vault 的架构设计有几个值得肯定的决策：

**分层隔离的纯度。** `storage.Storage` 接口（`internal/storage/storage.go`）仅有 10 个方法，全部以 `io.Reader`/`io.ReadCloser` 为中心。这使四套后端（local/S3/OSS/COS）实现可交换而不污染协议层。`repository.Repository` 同理——SQLite 和 Postgres 共享 `sql.go` 的 `rebind($N→?)` 桥接语法鸿沟。这种纯度在 Go 生态中是稀缺的（多数项目会在接口中泄漏 `*sql.DB`）。

**协议适配器的厚度控制。** REST/S3/WebDAV/MCP 四套协议全部选择在 `FileService` 之上做薄层映射，而非各自维护一套业务逻辑。这意味着一个 bug 修复在一处完成、四端受益——从长期维护成本看，这是正确的取舍。

**EventBus 的直觉设计。** 基于 Go channel 的 in-process pub/sub，所有 mutation 操作统一经过 `EventSink.Publish`。这为后续引入 Worker、Webhook、Indexer、Replication 提供了自然的挂钩点——当前系统约 70% 的异步功能基于这个总线衍生。

**Opt-in 安全默认。** AI/events/cluster/WebDAV 全部 flag-gated，`nil` embedder/llm 不会阻断核心 CRUD。这在 CI gate 只有 SQLite + local FS 的前提下是务实的——基线路径必须零依赖零配置就能全绿。

### 1.2 架构的局限性

**双写原子性缺口是最大的架构债务。** 当前 `Put` 路径中 `store.Put` 成功后 `repo.UpsertObject` 失败会导致孤立 blob（`file_crud.go:155-157`）。虽然 Reconcile worker（`sweepOrphans`）可以异步清理，但窗口期可达 15 分钟（`RECONCILE_INTERVAL_MINUTES` 默认值）。这种"尽力一致"模式在单副本场景可以接受，但在复制场景下——主 storage 写入成功、元数据写入成功、但复制 job 入队前 crash——会导致跨区不一致。

**流式路径无内存预算。** `GetRange` 跳过 offset 字节使用 `io.CopyN(io.Discard, rc, offset)`——若 offset=1GB，1GB 数据完全从存储读入再到 discard（`range.go:77-90`）。`CopyObject` handler 使用 `io.Copy` 在内存中中转两部（`handler.go:119-125`）。当前 `MaxInFlight` 仅控制并发请求数而非字节级吞吐——这意味着 20 个 500MB 的并发 GET 可以消耗 10GB 内存。

**事件路由是"广播"而非"路由"。** `Bus.Publish` 向所有 subscriber 广播全部事件。桶级 `notification_rules` 列已被持久化（`sql_buckets.go:381-415`）、CRUD API 完整实现（`handler.go:551-588`）、甚至 S3 XML 解析也已实现（`s3compat/handler.go:809-833`）——但运行时完全不读取。这是一个"已持久化但永不执行"的功能缺口，属于典型的**半产品化**架构债。

**Auth 凭据无生命周期。** `api_keys` 表有 `expires_at`、`last_used_at` 列但从不自动检查；JWT 签发后无法撤销（除非改全局 secret）。AccessLog 不记录 key_hash——无法追溯哪个 key 发出了哪个请求。这在生产安全审计中是硬性缺失。

### 1.3 关键设计决策评审

| 决策 | 状态 | 评价 |
|------|------|------|
| 单 storage key = `path.Join(tenant, bucket, key)` | ✅ | 正确。禁止反向解析（I3）也是正确的——GC 不能用字符串算法计算 key 的所有者 |
| Handler 不自挂中间件链 | ✅ | 正确。隔离 handler 测试无需 tenant/auth，这与 main.go 的装配方式一致 |
| SQL 占位符不可复用（I1） | ✅ | 正确。即使用 `rebind($N→?)` 转换，每个 bind 也要独立编号，这是 SQLite vs Postgres 语法差异的天坑 |
| `ChunkCleaner.DeleteObjectChunks` 失败不阻断硬删除 | ⚠️ | 合理但应记录详细的审计事件。当前仅 warn log，无法追溯 |
| 迁移双文件 + 不可编辑已应用文件 | ✅ | 正确。49 对迁移文件（当前最新 0024）证明了这套规则的可行性 |
| BM25 索引在内存中 | ⚠️ | 对单副本场景没问题。但若实例重启，所有 chunk 需要从 DB 重新加载到 BM25——启动时间随索引量线性增长 |
| SSE 加密的 key ring 结构 | ✅ | 正确。`{"primary":"id","keys":{"id":"passphrase"}}` 设计允许零停机轮换，每个对象记录 key id 确保旧对象仍可解密 |

### 1.4 技术债量化诊断

| 债项 | 位置 | 影响面 | 修复代价 |
|------|------|--------|---------|
| 双写原子性缺口 | `file_crud.go:132-163` | 数据一致性 | 中（引入 write_log 表 + 启动回滚） |
| 流式路径无内存预算 | `range.go:77-90`, `handler.go:119-125` | 可靠性/OOM 风险 | 中（引入 ByteBoundedReader + MemoryPool） |
| 通知规则持久化但不执行 | `bus.go:90-100` vs `sql_buckets.go:381-415` | 功能完整性 | 低（总线增加规则过滤） |
| SQL 语义鸿沟无运行时检测 | `sqlite.go` vs `postgres.go` | 运维风险 | 中（引入 DriverFeatureMatrix + startup validation） |
| 认证凭据无生命周期 | `auth.go:120-135`, `store.go:85-120` | 安全合规 | 低（增加过期检查 + 自动清理 + 审计） |

---

## 2. 扩展方向

### 方向一：存储层双写事务完整性（SAGA 补偿模式）

**优先级：P0（阻塞项）**

#### 为什么需要

当前 `Put` 路径和 `hardDelete` 路径在跨层操作时缺乏原子性保证。`store.Put` 成功后 `repo.UpsertObject` 失败产生孤立 blob（`file_crud.go:155-157`），`hardDeleteObject` 中 `store.Delete` 成功后 `repo.HardDeleteObject` 失败产生 phantom metadata（`file_crud.go:252-270`）。这不是一个理论问题——在 S3 后端网络抖动、DB 连接池耗尽等生产场景中，这是概率性发生的。

更关键的是，**复制场景放大了一致性需求**。当前复制基于 EventBus——如果主副本写入成功、事件入队前 crash，副本永远不知道有对象需要复制。复制是"最终一致"模式下的一致，但写入本身如果是不一致的，复制的语义基础就坍塌了。

#### 核心挑战

| 挑战 | 难度 | 说明 |
|------|------|------|
| 不引入分布式事务 | 高 | 两阶段提交（2PC）在 Go + SQLite + S3 的组合下不可行——S3 不支持 prepare/commit 语义 |
| 补偿操作必须是幂等的 | 中 | 回滚 `store.Delete` 和 `repo.UpsertObject` 必须可重入；DB crash 后重放不能产生副作用 |
| 性能开销 | 中 | 每次写入多一次 `INSERT write_log` + 一次 `UPDATE write_log`（两个额外 SQL 操作），约 0.5-2ms 延迟增加 |
| 启动恢复的处理 | 中 | 启动时需要扫描 stale entries —— 需要区分"正在写入中"（正常）和"写入后崩溃"（需要回滚） |

#### 架构变更

```
当前写入路径：
  store.Put → repo.UpsertObject

SAGA 写入路径：
  repo.InsertWriteLog(state=writing) → store.Put → repo.UpsertObject → repo.UpdateWriteLog(state=done)
                                              ↓ (失败时)
                                    repo.UpdateWriteLog(state=rollback) → store.Delete(幂等)
```

新增组件：
- `internal/service/saga.go` — write_log 生命周期管理（插入、更新、回滚）
- `internal/service/saga_recovery.go` — 启动时回滚 stale entries + 定时 reaper
- `internal/repository/sql_write_log.go` — `write_log` 表的 repository 方法

新增迁移文件：`0025_write_log.{up,down}.sql`（双文件）

#### 对现有系统的影响

- `FileService.Put` 和 `FileService.hardDeleteObject` 需要注入 saga 步骤
- `Reconcile.sweepOrphans` 应增强从 `write_log` 表消费而非仅扫描存储
- 所有现有测试不受影响（saga 逻辑在 `Put`/`Delete` 路径内，测试夹具不关心）
- OpenAPI 规范无需修改（纯内部变更）

---

### 方向二：基于 Libvips 的服务端变换管线

**优先级：P0（高 ROI，与方向一并列为 Sprint 启动项）**

#### 为什么需要

当前 `GET /thumbnail?w=&h=` 已有缩略图生成逻辑。将其扩展为通用变换管线有三个价值驱动：

1. **带宽节省（可量化）。** WebP/AVIF 转码对移动端带宽节省在 40-60%，对图片密集型场景（电商/社交媒体/CMS）这是直接的 CDN 和用户体验优化。
2. **客户端简化。** 从"客户端下载原图 + 本地渲染"到"服务端下发最佳格式"，消除客户端格式兼容性逻辑。
3. **派生存储安全边界。** 原始对象只上传一次，所有变换产物从原始对象派生并缓存——原始对象更新时派生缓存自动失效。这比客户端上传多个尺寸/格式更可控。

#### 核心挑战

| 挑战 | 难度 | 说明 |
|------|------|------|
| `storage.Storage.Get` 返回 `io.ReadCloser`，但 libvips 需要 `io.ReadSeeker` 或 `[]byte` | 高 | 当前接口无法直接传递文件描述符。要么改为 `io.ReadSeekCloser`（破坏现有实现），要么内部包装 |
| CGo 编译问题 | 高 | `vips-go` 绑定需要 CGo + libvips 头文件。CI 环境、Docker 镜像都需要安装依赖 |
| 派生缓存 GC | 中 | 原始对象删除后需要异步清除所有派生缓存。当前 `object.deleted` 事件可以派生，但需要注册新 worker |
| 格式探测 | 中 | 客户端上传时不带 `Content-Type` 或携带错误类型。需要 magic bytes 探测（`http.DetectContentType` 不支持 AVIF/HEIC） |

#### 架构变更

```
当前缩略图路径：
  GET /thumbnail → GetObject → 加载内存 → resize → 输出

变换管线路径：
  GET /v1/transform?key=...&x-actions=resize:w_200,fit_cover;format:webp

  FileService.Get(stream) 
    → magic bytes 探测（确定格式）
    → transform engine (libvips) 执行变换链
      → resize:w_200,fit_cover
      → format:webp
    → 结果写入派生缓存 (storage with derivative key)
    → 返回派生对象流
```

新增组件：
- `internal/transform/engine.go` — 变换引擎接口（支持 vips-go 和远程 HTTP 代理两种实现）
- `internal/transform/actions.go` — 变换动作解析器（从 `x-actions` 字符串解析变换链）
- `internal/transform/cache.go` — 派生缓存管理（缓存键规范化 + 过期策略）
- `internal/transform/mime.go` — 增强格式探测（支持 AVIF/HEIC/WebP magic bytes）
- Worker（复用 EventBus）：`internal/transform/cleaner.go` — 原始对象删除时异步清除派生缓存

接口变更：
- `storage.Storage` 可能需要新增 `GetSeeker(ctx, key) (io.ReadSeekCloser, ObjectInfo, error)` 以避免全量读取到内存
- 或在 FileService 层用 `spill` 策略：小文件内存读，大文件写临时文件再映射

#### 对现有系统的影响

- 新增 REST 端点 `/v1/transform` — 需要 OpenAPI 同步
- `storage.Storage` 接口可选扩展（非破坏性添加）
- 派生缓存不计入租户配额（需要 `config.go` 新增 `TRANSFORM_CACHE_MAX_SIZE` 配置）
- 现有缩略图端点可以保留（向后兼容）或在下一个大版本中弃用

---

### 方向三：分布式速率限制与分层配额治理

**优先级：P1（高架构影响，Phase 1 必要条件）**

#### 为什么需要

当前每进程独立 token-bucket 的限流模式在多副本部署下完全失效——限流 100 RPS 时 N 副本意味着实际 100×N RPS。这不是限流的"精度问题"，而是**安全缺口**：下游系统（AI embedder、LLM API、存储后端）看到的流量与限流器预期的不一致。

如果 aero-vault 要部署到副本数 >1 的生产环境，分布式限流是**必须先于 AI、复制等功能的阻塞项**。当前 `main.go:216-218` 的 `MaxInFlight` 控制并发但不控制速率，多副本下形同虚设。

#### 核心挑战

| 挑战 | 难度 | 说明 |
|------|------|------|
| Postgres 作为限流后端的并发限制 | 高 | `SELECT ... FOR UPDATE` 在 >100 并发时延迟抖动明显。这需要仔细的 bench 和退路设计 |
| 限流后端不可达的降级 | 中 | 如果限流后端 (Postgres/Redis) 不可达，系统的行为应该是"开放本地限流"而非"拒绝所有请求" |
| 权重的可配置性 | 中 | 不同租户/不同场景的成本权重不同——PUT 在图片存储场景应该比文档存储场景权重大。权重表应该可配置而非硬编码 |
| 突发银行上限 | 中 | 任意累积的突发额度可以让限流形同虚设 |

#### 架构变更

```
当前：
  main.go → RateLimiter(per-process token-bucket, per-tenant)

目标演进路径：

  MVP (V1): Postgres-based + 本地 fallback
    RateLimiter → [Postgres tx: SELECT 消耗额度 FOR UPDATE] → 失败时 fallback 到本地 bucket
    
  V2: Redis SCRIPT LOAD 原子操作
    RateLimiter → [Redis EVALSHA: sliding window log] → Postgres fallback → 本地 fallback

  V3 (如需): CRDT-based 无中心
    RateLimiter → [Local CRDT counter + anti-entropy sync]
```

新增组件：
- `internal/mw/ratelimit/distributed.go` — 分布式限流器接口（Postgres + Redis 实现）
- `internal/mw/ratelimit/fallback.go` — 降级链：分布式 → Postgres → 本地
- `internal/mw/ratelimit/config.go` — 权重配置、突发银行参数
- 迁移文件：新增 `rate_limiter` 表（存储租户级限流配置和突发额度消耗记录）

接口变更：
- `middleware.NewRateLimiter` 可能需要新增 `DistributedBackend` 参数
- `config.go` 新增 `RATE_LIMIT_DISTRIBUTED_BACKEND`（`local|postgres|redis`）

#### 对现有系统的影响

- 当前 `middleware.NewRateLimiter(0, 0)` 返回 nil 的行为保持不变
- 当 `RATE_LIMIT_DISTRIBUTED_BACKEND=local` 时，行为与现在完全一致
- 分布式模式增加 0.5-5ms 的每次请求延迟（取决于后端）
- 现有 tenant 配额（字节+对象）不受影响——这是独立的维度

---

### 方向四：混合查询语言（元数据 + 语义 + 关键词融合）

**优先级：P2（需要方向三的基础设施）**

#### 为什么需要

当前系统有两套独立的查询 API：
- `/v1/files` — 元数据列举（tenant/prefix 过滤）
- `/v1/search` — AI 内容搜索（vector/BM25/hybrid）

两者不能结合——没有"查找 2024 年后创建的、包含 'financial report' 的所有 PDF"的方式。客户端必须分别调用、自己融合结果。这是**用户可见的产品缺口**。

更重要的是，当前 `/v1/search` 返回的是 chunk 级结果（哪个 chunk 匹配），而用户需要的是对象级结果（"这个 PDF 的相关章节是..."）。当前的映射是业务逻辑层做的，但没有标准化。

#### 核心挑战

| 挑战 | 难度 | 说明 |
|------|------|------|
| 查询执行模式选择 | 高 | 元数据过滤后做语义搜索 vs 语义搜索后做元数据过滤 vs 并行融合——选择哪种模式依赖于 selectivity 估计 |
| 跨租户搜索的鉴权 | 高 | 搜索结果中包含来自不同租户的对象时，可见性规则是什么？对租户 A 暴露"存在匹配对象但不显示内容"还是完全不可见？ |
| 与既有 API 的兼容与过渡 | 中 | `/v1/query` 需要复用 `/v1/search` 的 `Search.Query` 和 `/v1/files` 的 `ListObjects`，而非平行新增执行引擎 |
| BM25 向量融合打分归一化 | 中 | 元数据过滤结果（bool 匹配）和语义搜索结果（cosine score）的打分不在同一量纲——RRF 是一个选项，但需要一致实现 |

#### 架构变更

```
当前：
  /v1/files → FileService.ListObjects
  /v1/search → Search.Query

融合查询路径：
  POST /v1/query
  {
    "from": "objects",
    "metadata": {
      "bucket": "docs",
      "created_after": "2024-01-01",
      "content_type": "application/pdf"
    },
    "content": "financial report",
    "mode": "hybrid",          // vector | bm25 | hybrid | metadata_only
    "strategy": "auto"         // auto | filter_then_content | content_then_filter | parallel_rrf
  }
```

新增组件：
- `internal/query/engine.go` — 查询执行引擎（模式选择 + 多阶段执行）
- `internal/query/strategy.go` — 确定性策略选择器（基于简单启发式而非代价优化）
- `internal/query/fusion.go` — RRF + 结果归一化
- `internal/query/authz.go` — 跨租户结果可见性过滤

内部路径复用：
- metadata 过滤 → `service.ListObjects` + `repo.ListObjects`
- content 搜索 → `Search.Query`（已有 vector/BM25/hybrid 实现）
- strategy = `filter_then_content` → 先 ListObjects 获取 key 列表 → 传递给 Search.Query 作为过滤条件

#### 对现有系统的影响

- 新增 `/v1/query` 端点（可替代旧端点的候选）——OpenAPI 需要同步
- `/v1/search` 和 `/v1/files` 应该标记为"稳定但不演进"——`/v1/query` 是它们的统一替代
- 如果 `/v1/query` 的 metadata 过滤通过 `repo.ListObjects` 实现，则 SQLite 和 Postgres 的 ListObjects 路径必须都支持额外的过滤条件
- AI 限流组（方向三）需要覆盖 `/v1/query` 端点

---

### 方向五：异步写入缓冲与优雅降级

**优先级：P3（Phase 2 投入，收益最高但复杂性也最高）**

#### 为什么需要

当前写入路径是同步的——客户端必须等待 `store.Put` + `repo.UpsertObject` 全部完成才能收到响应。这导致：
1. 写入延迟 = 存储后端延迟 + DB 延迟（S3 PUT 的 p99 在 100-500ms 之间）
2. 下游后端（S3/OSS/COS）不可达直接导致写入失败
3. 大对象（100MB+）的写入时间由带宽决定，客户端必须保持连接

异步写入缓冲允许写入路径在毫秒级返回 202 Accepted，持久化由后台缓冲层异步完成。

#### 核心挑战

| 挑战 | 难度 | 说明 |
|------|------|------|
| 写入后读一致性 | 高 | 最棘手的问题。缓冲中对象在持久化前被 GET——从缓冲中提供还是返回 404？如果从缓冲中提供，每个 GET 需要多一次 buffer 搜索 |
| WAL 保护 | 高 | WAL 磁盘不可与对象存储共用。WAL 满后的策略——回压到客户端还是丢弃最早的缓冲？ |
| 存储后端的接口要求 | 中 | 当前 `storage.Put` 接受 `io.Reader`——缓冲层需要将数据写入 WAL（磁盘文件），然后在后台用文件描述符调用 store.Put |
| 幂等性去重 | 中 | 对于相同的 `Idempotency-Key`，缓冲层应该直接返回已有的 202 而非重复持久化 |

#### 架构变更

```
当前写入路径（同步）：
  PUT /v1/files → FileService.Put → store.Put + repo.UpsertObject → 201 Created

异步写入路径：
  X-Write-Mode: async
  PUT /v1/files → FileService.PutAsync → WAL 写入 → 202 Accepted + Location: /v1/jobs/{id}
    → 后台线程:
        WAL 读取 → store.Put + repo.UpsertObject (幂等)
        → 失败 → job 表 retry
        → 成功 → 更新 job 状态 + WAL 清理

  读取路径：
  GET /v1/files/... → 检查 job 完成状态 → 完成则 store.Get；未完成则 404 或返回 job 进度
```

新增组件：
- `internal/writebuf/wal.go` — WAL 写入（append-only log + fsync）
- `internal/writebuf/scheduler.go` — 后台持久化调度器（速率可控 + 退避策略）
- `internal/writebuf/config.go` — 缓冲配置（`WRITE_BUF_WAL_DIR`、`WRITE_BUF_MAX_SIZE`、`WRITE_BUF_FLUSH_INTERVAL`）

#### 对现有系统的影响

- 这是一个**产品级功能**——引入 202 Accepted 语义意味着 SDK 需要适配（REST handler 返回 `202 + Location` 时客户端知道去轮询 job）
- 当前同步路径不应该受影响（X-Write-Mode header 不传时行为不变）
- `storage.Storage.Put` 接口可能需要增加 `PutFromFile(ctx, key, filePath, size, opts)` 避免缓冲层先读 WAL 再传 store 时的二次内存拷贝
- WAL 路径需要严格与对象存储路径分离（部署文档要求）

---

## 3. 接口设计建议

### 3.1 Storage 接口是否需要演进

当前 `storage.Storage` 接口是极简的（10 个方法），但两个扩展方向暴露了其局限性：

| 扩展方向 | 当前接口限制 | 建议演进方式 |
|---------|-------------|-------------|
| 变换管线（方向二） | `Get` 返回 `io.ReadCloser`，libvips 需要 `io.ReadSeeker` | 新增 `GetSeeker(ctx, key) (io.ReadSeekCloser, ObjectInfo, error)` — 可选方法，backend 可以不实现，FileService 层 fallback 到 `ReadCloser → spool to tempfile` |
| 异步缓冲（方向五） | `Put` 接受 `io.Reader`，缓冲层需要写 WAL 后传 file descriptor | 新增 `PutFromFile(ctx, key, localPath string, opts PutOptions) (ObjectInfo, error)` — 同样可选实现，local backend 可以直接 `os.Rename`，S3 backend 可以用 `PutObjectWithFileDescriptor` |

**原则：不破坏现有接口、不强制新方法实现。** 所有新增方法都是可选的——`storage.go` 中应该保留注释 `// Optional: implementations may return ErrNotImplemented`。FileService 层在调用新方法前通过 type assertion 检查实现。

### 3.2 是否需要新的抽象层

| 候选 | 必要性 | 建议 |
|------|--------|------|
| 变换引擎抽象 | ✅ 必要 | `internal/transform/Engine` 接口支持 vips-go 和 HTTP 远程两种实现。前者用于生产高性能，后者用于开发环境无 CGo |
| 限流后端抽象 | ✅ 必要 | `internal/mw/ratelimit/Backend` 接口支持 `local`、`postgres`、`redis`。当前 per-process 限流作为 `local` 实现 |
| SAGA 事务日志 | ⚠️ 可选的 | `internal/service/saga.go` 可以作为 FileService 的内部辅助，无需独立抽象。但建议使 write_log 操作通过 `repository.WriteLogRepository` 接口解耦 |
| 查询执行引擎 | ✅ 必要 | `internal/query/Engine` 作为独立的编排层，高于 `Search` 和 `ListObjects`。但建议其内部复用 `Search.Query` 和 `repo.ListObjects`，而非从头实现 |
| WAL 缓冲层 | ⚠️ 可选的 | 如果采用"202 + 轮询"模式（而非"缓冲中对象立即可读"模式），WAL 层可以是 FileService 内部的辅助组件，但考虑测试分离，建议独立包 |

### 3.3 向后兼容性策略

| 场景 | 策略 |
|------|------|
| 新增 REST 端点（`/v1/transform`、`/v1/query`） | 新增端点，旧端点继续工作。下一个大版本（v2）可以考虑弃用旧端点 |
| 新请求头（`X-Write-Mode: async`） | 不传时行为完全不变。仅传递时进入异步路径 |
| 新配置变量 | 全部默认非零值等于"不启用"。这与现有 opt-in 模式一致 |
| 迁移文件 | 遵循 I2 规则：新增迁移对（双文件）而非编辑已有 |
| Storage 接口新方法 | 所有新方法都是可选的——backend 可以通过返回 `ErrNotImplemented` 来忽略。FileService 层必须处理回退 |
| 租户级默认值 | 新 feature 的租户级默认值以"bucket 设置优先，租户默认值作为 fallback"为原则 |

---

## 4. 技术选型

### 4.1 五个方向的依赖评估

| 方向 | 建议的技术栈 | 依赖类型 | 评估 |
|------|------------|---------|------|
| 变换管线 | Libvips (CGo via `github.com/davidbyttow/govips/v2`) | 运行时依赖 | 成熟度极高（Cloudinary/imgix 底层），但 CGo 是持续成本。备选：纯 Go `image` 包（功能有限，不支持 WebP/AVIF）、HTTP 远程代理（增加运维复杂度）。推荐 libvips + Docker 镜像预编译 |
| 分布式限流 | Postgres `SELECT ... FOR UPDATE` → Redis `SCRIPT LOAD` | 运行时依赖 | Postgres 是零新增依赖（已经必须）。Redis 是可选优化——仅在 >5 副本 / >2000 RPS 时必要。建议**按需演进**而非一步到位 |
| 查询引擎 | 无新依赖。复用现有 `Search.Query` 和 `repo.ListObjects` | 无 | 现有系统已有 vector/BM25 和元数据列举。只需编排层 |
| 异步缓冲 | `os.File` + `*sql.DB`（WAL 元数据在 DB） | 无 | WAL 是标准文件 I/O，无需新依赖。如果 WAL 需要高可用可以引入 etcd/Raft——但这是 Phase 3 的事 |
| SAGA 补偿 | 无新依赖。`write_log` 表在 repository 中 | 无 | 纯 SQL + Go 标准库 |

**结论：五个方向中四个不需要新增运行时依赖。** 仅变换管线的 libvips 需要 CGo 绑定。这是正确的——依赖引入的门槛应与功能的复杂性成正比。

### 4.2 自建 vs 集成决策矩阵

| 组件 | 自建理由 | 集成候选 | 决策 |
|------|---------|---------|------|
| 图像变换引擎 | 控制缓存生命周期、与 storage 集成紧密 | Cloudinary、imgix | 自建（但变换逻辑由 libvips 驱动）——不需要自建 JPEG 解码器 |
| 分布式限流 | 需要与现有中间件链集成 | Kong、Envoy 限流 | 自建（项目已有 RateLimiter 类型，复用接口） |
| 查询 DSL | 需要操作元数据 + 语义 + BM25 三模态 | Elasticsearch DSL | 自建（三模态融合是差异化核心，通用搜索引擎不支持） |
| 异步 WAL 缓冲 | 需要与现有 `storage.Storage` 集成 | Kafka、NATS | 自建（需求简单——写入后异步回放，不需要流式处理） |

### 4.3 CGo 风险评估（Libvips 场景）

```
风险因素：
  1. 构建时间增加（CGo 编译比纯 Go 慢 2-3x）
  2. CI 需要安装 libvips-dev (apt-get 或 Docker multi-stage)
  3. 交叉编译复杂化（GOARCH/GOOS 组合受限）
  4. cgo 内存管理 vs Go GC 的交互（大量 vips 图像对象在 C heap 中）

缓解措施：
  - 永远通过 govips v2 API 访问，不直接写 C
  - govips v2 使用线程安全的工作器池，避免 goroutine 泄漏
  - Dockerfile 使用 multi-stage 构建：构建阶段安装 libvips-dev，运行阶段仅 libvips 运行时
  - 为需要 CGo-free 的环境提供 HTTP 远程代理模式（环境变量切换）
```

---

## 5. 实施路线图

### 5.1 优先级矩阵

```
                  HIGH
                  ↑
                  |  方向三 (分布式限流)  方向一 (SAGA 补偿)
    架构影响      |      P1                 P0
      中          |  方向五 (异步缓冲)  方向二 (变换管线)
                  |      P3                 P0
                  |  方向四 (统一查询)
                  |      P2
                  +------------------------→
                    低           ROI         HIGH
```

### 5.2 阶段划分

#### Phase 0（启动质量加固）— 2 周

| 任务 | 工作量 | 前置依赖 |
|------|--------|---------|
| 流式路径内存预算—`GetRange` 的 `io.CopyN(io.Discard, rc, offset)` 优化 | 2 天 | 无 |
| API 凭据过期检查—`auth.Authenticate` 增加 `expires_at` 校验 | 1 天 | 无 |
| AccessLog 记录 key_hash | 1 天 | 无 |
| SQL 语义鸿沟启动检测—`SKIP LOCKED` / `pg_advisory_lock` 在 SQLite 上的启动错误提示 | 2 天 | 无 |

**理由：** 这些都是低工作量（总计 < 1 周）、无新依赖、对生产安全有直接影响的任务。适合作为任何扩展的前置条件。

#### Phase 1（Q3 启动）— 6-8 周并行两个 Track

**Track A：服务端变换管线（方向二）— 3 周**

| 里程碑 | 交付物 | 验证 |
|--------|--------|------|
| W1 | `internal/transform/engine.go` — 接口 + libvips 实现 + 格式探测 | `vips_resize("input.jpg", 200, 0)` 输出正确尺寸 |
| W2 | `/v1/transform` 端点 + `x-actions` 解析 + 派生缓存 | HTTP 请求返回 WebP / AVIF / resize 结果 |
| W3 | 派生缓存 GC + 与现有 `/thumbnail` 的兼容 | 原始对象删除后派生缓存自动清除 |

**Track B：SAGA 双写保护（方向一）— 4 周**

| 里程碑 | 交付物 | 验证 |
|--------|--------|------|
| W1 | `write_log` 表 migraion + `repository.WriteLogRepository` | 单元测试覆盖写日志插入/更新/查询 |
| W2 | `Put` 路径的 saga 注入 + 失败回滚 | 注入 `store.Put` 成功后 `repo.UpsertObject` 失败的 mock — 验证回滚执行 |
| W3 | `hardDeleteObject` 路径的 saga 注入 + 启动恢复 | 启动时扫描 stale writing → 回滚；扫描 stale deleting → 告警 |
| W4 | 集成测试 + Chaos monkey（crash at each step） | 验证每种 crash 场景下的最终一致性 |

**Phase 1 还需要并行：分布式限流（方向三）的 Pre-work — 2 周**

| 任务 | 说明 |
|------|------|
| Postgres-based limiter 原型 | 验证 `SELECT ... FOR UPDATE` 在预期并发下的延迟 |
| 权重模型设计 | 可配置的权重定义 YAML 或环境变量 |
| 突发银行策略 | 上限 = `min(burst_max_seconds × base_rps, max_burst_absolute)` |

**推荐在 Phase 1 结束时发布 v1.1（变换管线 + SAGA 保护 + 流式内存预算）**

#### Phase 2（Q3-Q4 交界）— 4-6 周

**分布式限流完整实现（方向三）+ 通知规则运行时钩子**

| 里程碑 | 交付物 | 验证 |
|--------|--------|------|
| W1 | 分布式限流后端接口 + Postgres 实现 | 3 副本场景：限流 100 RPS 时总吞吐不超过 105 |
| W2 | 降级链：Postgres → local fallback | Postgres 不可达时自动切换到 local token-bucket |
| W3 | 通知规则运行时钩子 — `Bus.Publish` 读取 `notification_rules` | S3 `PutBucketNotifications` 配置 → 事件按规则路由 |
| W4 | 突发银行 + 权重可配置 + 端到端测试 | 多租户场景验证配额继承 |

#### Phase 3（Q4）— 6-8 周

**统一查询语言（方向四）+ 异步写入缓冲（方向五）原型**

| 里程碑 | 交付物 | 验证 |
|--------|--------|------|
| W1-W2 | `/v1/query` 端点 + 执行引擎（确定性策略，无代价优化） | 元数据过滤 → 语义搜索 与 纯语义搜索 两种模式 |
| W3-W4 | 元数据 + 内容混合查询 + 跨租户鉴权 | 用户可见的结果过滤正确 |
| W5-W6 | 异步写入缓冲原型（WAL + 202 Accepted） | 大文件上传在 5ms 内返回 202，后台持久化在 30s 内完成 |
| W7-W8 | 缓冲读一致性处理 + WAL 磁盘保护 + 幂等性去重 | `Idempotency-Key` 相同 → 跳过重复持久化 |

### 5.3 关键风险与缓解

| 风险 | 影响方向 | 概率 | 缓解 |
|------|---------|------|------|
| CGo 交叉编译失败阻塞 Docker 构建 | 方向二 | 中 | 提供 HTTP 远程变换引擎作为 fallback；CI 中分别测试 CGo 和 non-CGo 构建 |
| Postgres 行锁在高并发下延迟不可接受 | 方向三 | 中 | Phase 1 的 pre-work 限定"验证"——如果不可接受，直接跳到 Redis 方案 |
| 查询执行引擎的复杂度超出预期 | 方向四 | 高 | 严格限制：Phase 3 只实现固定策略（无代价优化），不接受"需要查询规划器"的 scope creep |
| WAL 磁盘满导致系统不可用 | 方向五 | 中 | 部署文档明确要求 WAL 路径与对象存储路径分离；代码实现 WAL 使用率的逐级告警（70% warn → 85% critical → 95% 回压） |
| 迁移文件版本爆炸（当前已到 0024） | 方向一、三 | 低 | 遵循 I2——不可编辑已应用的迁移。若迁移数量超过 100，考虑合并历史迁移（Phase 2 的运维优化）。但注意合并历史迁移本身有风险 |

### 5.4 跨方向依赖图

```
Phase 1 (Q3)
├── Track A: 变换管线 ───────────────── 无外部依赖
│   └── 需要 storage.GetSeeker (可选接口扩展)
│       └── 不影响其他方向
├── Track B: SAGA 补偿 ──────────────── 无外部依赖
│   └── 需要新增 write_log 迁移 (0025)
│       └── 后续 Reconcile 增强可以复用这个表
└── Pre-work: 分布式限流 ────────────── 无外部依赖
    └── 设计阶段的 benchmark 决定 Postgres / Redis 选型

Phase 2 (Q3-Q4)
├── 分布式限流完整实现 ──────────────── 依赖 Phase 1 pre-work 的选型结论
├── 通知规则运行时钩子 ───────────────── 无外部依赖（纯事件总线修改）
└── 流式内存预算 + 凭据生命周期 ─────── 独立，可随时插入

Phase 3 (Q4)
├── 统一查询语言 ───────────────────── 依赖：分布式限流（方向三）为 /v1/query 提供 AI 组限流
│   └── 依赖：AI 索引已启用（已有）
├── 异步写入缓冲 ───────────────────── 依赖：SAGA 补偿（方向一）提供写操作追踪
│   └── 依赖：Idempotency-Key 已有基础设施
```

---

## 总结

这份分析文档的质量在 `docs/requirements/` 中属于**最高的一档**——代码锚点验证全部通过、跨方向的权衡覆盖了 80% 的关键决策点、去重分析扎实。我的评审与原文的核心差异在于**优先级排序**：

**原文建议：** 变换管线 P1 → Legal Hold P1 → 分布式限流 P2 → 统一查询 P2 → 异步缓冲 P3

**我的建议：** SAGA 补偿 **P0** + 变换管线 **P0**（并行 Sprint）→ 分布式限流 **P1**（Phase 1 必要条件）→ 统一查询 P2 → 异步缓冲 P3

理由是：双写原子性缺口影响所有写入路径（包括变换管线的派生缓存写入），其修复成本低但收益遍及整个系统，不应排在 P1 之后。而分布式限流（原文 P2）在多副本场景下是**阻塞项**——如果 aero-vault 有生产部署在 >1 副本的需求，这应该是 Phase 1 的 P1。

对于原文分析中补充的 legal hold 与版本控制的交互风险、存储后端接口在变换场景下的局限、以及租户级默认值传播方向——这些都是有深度的补充视角，建议纳入最终设计文档。
