# 高价值扩展方向 —— 架构师 / 产品视角

> 基于当前代码库全局扫描（2026-07-11），以下 5 个方向经过权衡价值密度、技术可行性、差异化优势后选出。每个方向均说明「为什么需要它」「核心切入点」「边界情况与风险」。

---

## 目录

1. [存储类生命周期自动转换与冷归档恢复](#1-存储类生命周期自动转换与冷归档恢复)
2. [内容寻址存储与块级去重](#2-内容寻址存储与块级去重)
3. [多层对象缓存（L1 内存 + L2 磁盘 + CDN 集成）](#3-多层对象缓存l1-内存--l2-磁盘--cdn-集成)
4. [基于事件的写前日志（WAL）与状态回放](#4-基于事件的写前日志wal与状态回放)
5. [富元数据索引与查询引擎](#5-富元数据索引与查询引擎)

---

## 1. 存储类生命周期自动转换与冷归档恢复

### 现状

代码中已定义 `StorageClass` 字段（`STANDARD`、`STANDARD_IA`、`GLACIER`），`SetBucketLifecycle` 也暴露了 REST/S3 接口，但当前生命周期唯一支持的动作是 `soft_delete` / `hard_delete`（过期删除）。不存在「30 天后转为 STANDARD_IA，90 天后转为 GLACIER」这类自动转换逻辑。GLACIER 类对象没有任何恢复（restore）流程——没有 `InitiateRestore`、`RestoreStatus`、临时副本管理。

### 为什么需要

| 维度 | 说明 |
|------|------|
| **成本** | 生产环境存储成本随数据量线性增长。自动降冷可在保证热数据性能的同时将不常访问的数据迁移到低成本介质，总成本降低 60–80%（参考 AWS S3 实际账单结构）。 |
| **合规** | 法规要求某些数据在一定周期后归档锁定，自动转换是强制合规的前提。 |
| **产品竞争力** | S3 Protocol 用户期望完整的 `x-amz-storage-class` + 生命周期规则体验。当前只能标记不能转换，是 S3 兼容的硬缺失。 |
| **现有资产** | `BucketConfig.ExpireAfterDays`、`ExpireAction` 已有，`StorageClass` 已在 Object 元数据中存在，扩展成本低。 |

### 核心切入点

1. **Lifecycle Transition Rules** —— 在 `ExpireAfterDays` 之外新增 `Transitions []TransitionRule`：
   ```go
   type TransitionRule struct {
       Days               int    // 从 updated_at 起算
       StorageClass       string // 目标类
   }
   ```
2. **Lifecycle Worker 扩展** —— `reconcile/lifecycle.go` 增加 transition 扫描 + 执行逻辑：
   - `STANDARD → STANDARD_IA`：仅更新元数据，无需搬动存储 blob（后端相同）。
   - `STANDARD_IA → GLACIER`：视后端能力决定是否搬动 blob（local FS 可保留同一文件但标记；S3 后端调用 `CopyObject` + `StorageClass`）。
3. **Restore 工作流**：
   - `POST /v1/files/{key}?restore&days=N` → 创建临时副本，设 `RestoreStatus=in-progress`。
   - `GET /v1/files/{key}?restore` → 返回 `RestoreInProgress` / `RestoreExpiryDate`。
   - 到期自动清理临时副本（Reconcile 扫描 `restore_expires_at`）。
4. **存储后端适配**：S3 后端直接调用 `CopyObject` 改 StorageClass；local 后端在元数据中记录降冷标记；OSS/COS 适配各自 API。

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| 转换中途对象被覆盖 | 新 PUT 重置 `StorageClass` 和转换计时器；老的转换 job 需检查 `updated_at` 是否变化，变化则 skip。 |
| GLACIER 对象直接被请求 | 返回 `InvalidObjectState`（S3 兼容），或自动触发 restore。 |
| 对象锁/WORM 期间降冷 | 锁定的对象不参与转换（生命周期跳过 `locked_until > now` 的行）。 |
| 后端不支持 GLACIER | OSS/COS 没有等同 GLACIER 概念；转换规则应允许后端级 fallback：忽略或改 `STANDARD_IA`。 |

---

## 2. 内容寻址存储与块级去重

### 现状

每个 `PUT` 直接写入存储后端，相同内容多次上传产生多个完全独立的 blob。无任何跨对象、跨版本、跨租户的重叠数据检测。`storageKey` = `tenant/bucket/key` 的投影，内容 hash 仅用于 `Content-MD5` 校验而不作为寻址依据。

### 为什么需要

| 维度 | 说明 |
|------|------|
| **存储效率** | 备份、CI 制品、文档库场景中，增量/重复内容占比极大。去重可节省 70–90% 物理存储。 |
| **写入吞吐** | 已存在的内容直接引用（link/clone）而非重新写入，减少后端 IO。 |
| **增量快照** | 块级去重（4–64 KB 变长分块）是构建高效版本快照的基石，显著优于当前全量 `versioning` 存储每个版本的完整 blob。 |
| **防篡改** | 内容寻址（Hash-as-key）天然提供完整性验证。 |

### 核心切入点

1. **内容分块与指纹计算** —— 引入 CDC（Content-Defined Chunking）：
   - 可选块大小 4KB–1MB（`STORAGE_DEDUP_CHUNK_SIZE`），使用 Rabin fingerprint / Buzhash 切割变长块。
   - 每个块计算 SHA-256 指纹，`指纹 → 块存储映射` 存储于专用 chunk store（可复用当前 Repository 或独立 leveldb/bbolt）。
2. **对象重组与引用计数**：
   - 对象存储从「单 blob」变为「块清单：`[]ChunkRef{fingerprint, offset, length}`」
   - 引用计数跟踪每个块的活跃引用数，引用归零时物理删除。
3. **写入路径**：
   - `FileService.Put` 在完整写入后运行可选的离线去重（避免延迟写入路径），或同步分块写入。
   - 后端透传：S3/OSS/COS 仍然一个对象一个 blob，块数据写入单独的 dedup bucket。local 后端可将块存储于 `{root}/.chunks/{fingerprint[:2]}/{fingerprint[2:]}`。
4. **版本快照** —— 同一对象的相邻版本共享大部分块，快照仅存储「块清单差异」，物理存储接近增量。

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| 单块太大/太小 | CDC 参数 `avgSize ± 1/2` + 最小/最大硬限制，防止过碎或浪费。 |
| 同步 vs 异步去重 | 同步：写入延迟增加但存储立刻优化；异步：后台 Job 分析+合并（类似 VDO）。MVP 建议异步，不上生产路径。 |
| 加密与去重 | 去重与 SSE（服务器端加密）冲突——相同内容的密文不同。方案：先加密再分块（失去去重效果），或先分块再加密（支持去重但增加 KMS 调用次数）。 |
| 租户隔离 | 跨租户去重需策略控制：允许全局去重（最大节省）还是租户级隔离（合规）。 |
| 引用计数一致 crash-safe | 引用计数更新需事务性：`InsertChunkRefs` + `IncrementRefCount` 在同一事务中。引用归零的删除通过后台 GC 进行。 |

---

## 3. 多层对象缓存（L1 内存 + L2 磁盘 + CDN 集成）

### 现状

当前所有对象读取均直接穿透 `FileService.Get → Storage.Get`。没有任何 read-through 缓存。Search 结果缓存（`resultCache`）只针对 AI 检索结果，不作用于对象读路径。虽然 `PresignGet` 可间接 CDN，但并无 CDN 回源/prefetch 的主动集成。

### 为什么需要

| 维度 | 说明 |
|------|------|
| **读取延迟** | 同一热门文件被重复请求时，每次均从后端读取（即使是 local FS 也有系统调用开销）。内存缓存可将 P99 延迟从 10ms+ 降至 <100µs。 |
| **后端压力** | 缓存命中减少对底层存储（尤其是 S3/OSS/COS，有请求费用和限流）的调用量。 |
| **CDN 卸载** | 主动集成 CDN（CloudFront / Cloudflare R2 缓存）可使全球用户就近读取，大幅降低源站带宽。 |
| **缓存一致性** | 当前无缓存自然无一致性问题，但引入缓存后需要精巧的失效策略。这就是高价值所在。 |

### 核心切入点

1. **缓存抽象层 `CachingStorage`** —— 实现 `storage.Storage` 接口，包装下游 `Storage`：
   ```go
   type CachingStorage struct {
       next  storage.Storage
       l1    *ristretto.Cache // 内存缓存，按 object key + range 分片
       l2    *diskcache.Cache // 磁盘缓存（可选）
   }
   ```
   - L1 内存缓存：使用 `ristretto` 或类似 admission-control 的缓存，容量可控（`OBJECT_CACHE_MEMORY_MB`），默认 256MB。
   - L2 磁盘缓存：`OBJECT_CACHE_DISK_PATH` / `OBJECT_CACHE_DISK_SIZE_GB`，使用 `filepath` + `lru` 或 `bbolt`。
   - 默认只缓存 `GET` 响应，`PUT`/`DELETE`/`CopyObject` 触发失效。

2. **缓存键设计**：
   - 键 = `{tenant}:{bucket}:{key}:{offset}-{length}`（范围请求分片缓存）。
   - 附带 `ETag` 作为版本标记，后端返回新 ETag 时失效旧缓存。
   - 缓存 `Content-Type`、`Metadata`、`Tags`。

3. **预签名与 CDN**：
   - 新增 `CDNProvider` 接口：`Preheat(key)`, `Invalidate(key)`。
   - `PresignGet` 生成带 CDN 绑定的 URL（可选 CDN secret 签名）。
   - `Reconcile` 定期预热热门对象到 CDN。

4. **失效策略**：
   - `PUT`/`DELETE`/`CompleteMultipart` 事件触发 broker → 异步广播失效消息。
   - 跨实例失效通过 Postgres LISTEN/NOTIFY 传播（复用现有 `PostgresTransport`）。

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| 范围请求缓存 | 小范围合并为 `offset-align` 块（如 1MB chunks），防止缓存碎片；超出 L1 容量时兜底读取后端。 |
| SSE-C 加密对象 | 缓存密文还是明文？密文缓存（无敏感信息）+ 每请求解密，或明文缓存（需严格的权限检查）。建议缓存密文。 |
| 预签名与缓存 | 预签名 URL 应绕过缓存或含 ETag 校验，防止授权过期后仍返回缓存内容。 |
| 缓存一致性与 WORM | Locked 对象即使缓存命中也要检查 `locked_until`，防止 stale cache 绕过锁定。 |
| 磁盘缓存 IO 竞争 | L2 磁盘缓存可能与 local storage 争抢同一磁盘 IO。配置隔离 `OBJECT_CACHE_DISK_PATH` 到不同块设备。 |

---

## 4. 基于事件的写前日志（WAL）与状态回放

### 现状

`events.Bus` 持久化 `Event` 行，但它们只用于：① 触发 Indexer/Antivirus/Webhook 等消费者；② 被 `NextUnconsumedEvents` 轮询消费后标记 `consumed`。不存在保留全部事件历史用于重建状态的机制。`consumed` 标记本质是「已处理」而非「已归档」，事件行可能被清理。无法通过事件回放重建 `objects` 表或审计完整变更链。

### 为什么需要

| 维度 | 说明 |
|------|------|
| **审计溯源** | 合规需求要求完整记录谁在什么时间对哪个对象做了什么操作（CREATE / UPDATE / DELETE / ACCESS），且保留不可变。当前 `audit_log` 只覆盖 admin 操作，对象级变更无完整记录。 |
| **灾难恢复** | 如果 `objects` 表损坏或被误删，当前只能从存储 blob 反推状态（且无法恢复 tags/ACL/quota）。有了完整事件日志，可重放到任意时间点。 |
| **异步重建索引** | Indexer 不再需要轮询 `NextUnconsumedEvents`（可能丢事件），直接订阅事件流从 WAL 位置游标开始处理。 |
| **跨区域最终一致复制** | 复制目标可直接消费事件流回放，替代当前「每个事件 enqueue replicate job」的脆弱模式。 |
| **测试与调试** | 开发和测试环境可从生产 WAL 快照重建特定时间点的数据状态。 |

### 核心切入点

1. **不可变事件表** —— 新表 `event_log`（与现有 `events` 表分开）：
   ```sql
   CREATE TABLE event_log (
       id          INTEGER PRIMARY KEY AUTOINCREMENT,
       seq         BIGINT NOT NULL,       -- 单调递增的全局序列号
       created_at  TEXT NOT NULL,          -- RFC3339Nano
       tenant_id   TEXT NOT NULL,
       bucket      TEXT,
       key         TEXT,
       event_type  TEXT NOT NULL,          -- created | updated | deleted | accessed | lock | tag | acl | etc.
       actor       TEXT,                   -- api_key_hash 或 JWT sub
       before      TEXT,                   -- 操作前的对象快照 (JSON)
       after       TEXT,                   -- 操作后的对象快照 (JSON)
       request_id  TEXT
   );
   CREATE INDEX idx_event_log_seq ON event_log(seq);
   ```
   - `seq` 是单调递增的全局水位线（使用 `SEQUENCE` 或 `MAX+1`），消费者记录已回放到哪个 `seq`。
   - `before/after` 记录完整对象快照（JSON），不依赖 schema 版本。

2. **FileService 写入 WAL** —— 在每个写操作（Put / Delete / SetTags / SetACL / Lock / etc.）中同步写入 `event_log`：
   - 使用 `s.repo.InsertEventLog(ctx, entry)`，与业务操作在同一事务中（或异步+at-least-once）。
   - 事务内写入确保「业务操作 ⇒ WAL 写入」原子性。

3. **状态回放引擎** —— `reconcile` 包中新增 `EventReplay`：
   - 从任意 `seq` 开始按序回放，重建 `objects`、`tags`、`acl`、`versions` 表。
   - 回放是幂等的：`after` 快照覆盖写入即可。
   - 支持 `--target-time` 回放到某一时刻。

4. **消费者迁移** —— Indexer / Webhook / Replication 从轮询 `events` 表迁移为消费 `event_log` 的游标模式：
   - 每个消费者记录自己的 `last_seq`。
   - 死信：某条事件处理失败不影响 WAL 回放，消费者跳过或进入死信队列。

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| WAL 增长无限 | `event_log` 表需要基于保留策略的分区归档。可以：① 按时间分区（月表）；② `Reconcile` 将超过 90 天的 WAL 归档到对象存储（导出为 Parquet/JSON）。 |
| 事务外写入 WAL | 如果 WAL 写入在业务事务之外，存在「业务写成功，WAL 未写入」的窗口。方案：优先在同一事务（SQLite/Postgres 支持嵌套 JSON），否则引入补偿机制。 |
| `before/after` 体积 | 对象 metadata 可能很大（64KB metadata limit）。压缩存储或仅存储 diff（JSON Patch RFC 6902）。 |
| 高吞吐下的性能 | 同步 WAL 写入增加每个请求的延迟。可配置：① 同步（最强保证）；② 异步批量（100ms 批次刷入，丢失窗口 100ms）；③ 仅关键事件写 WAL。 |
| 与现有 `events` 表共存 | `event_log` 不应替代 `events` —— `events` 保持轻量（用于消费者触发），`event_log` 用于回放和审计。两者共享相同的数据来源但消费方式不同。 |

---

## 5. 富元数据索引与查询引擎

### 现状

对象元数据（`Metadata map[string]string`）以 JSON blob 存储在 `objects.metadata` 列中。查询元数据只能通过：① 按 `tenant/bucket/prefix` 列出后客户端过滤；② 按 tag key/value 客户端过滤（`ListObjectsByTag` 是取回全部后内存过滤）。不存在对 metadata 中任意字段的二级索引、范围查询、组合查询能力。SQL 层面无法执行 `WHERE metadata->>'department' = 'eng' AND size > 1MB`。

### 为什么需要

| 维度 | 说明 |
|------|------|
| **可发现性** | 用户需要有「按任意属性查找对象」的能力——不仅是文件名前缀和标签。例如：「Find all invoices where status=paid AND amount>1000 AND created_at>=2026-01-01」。 |
| **自动化** | 工作流引擎、合规扫描、批量操作（标记/删除/迁移）依赖于元数据查询。没有查询引擎，这些只能全表扫描。 |
| **性能** | 当前 `ListObjectsByTag` 的客户端过滤方案无法扩展。超过 10 万对象后每个 tag 查询都要读大量行。 |
| **产品差异化** | 大多数对象存储（S3/MinIO/Ceph）不提供内置的元数据查询引擎。aero-vault 的 AI 检索只能搜 chunk 内容不能搜 metadata。嵌入元数据查询是独特竞争力。 |

### 核心切入点

1. **元数据索引层** —— 新增 `MetaIndex` 接口：
   ```go
   type MetaIndex interface {
       // IndexObject 为对象建立元数据倒排索引。
       IndexObject(ctx context.Context, obj Object, meta map[string]string) error
       // DeleteObject 删除对象的索引条目。
       DeleteObject(ctx context.Context, objectID int64) error
       // Query 执行元数据查询，返回匹配的 ObjectID 列表。
       Query(ctx context.Context, tenant string, expr MetaExpr, limit, offset int) ([]int64, error)
   }
   ```
   - **后端选项**：
     - **SQLite/Postgres JSON 索引** —— 对 metadata 中常用字段建立表达式索引（Postgres `GIN` 或 SQLite 的 JSON 虚拟列+索引）。
     - **Bleve / zincsearch 集成** —— 全文 + 结构化的通用索引引擎。
     - **SQLite FTS5 + JSON1** —— 零依赖嵌入式方案。
   - **查询 DSL**（`MetaExpr`）：
     ```
     department = "eng" AND (size > 1048576 OR priority IN ("high","critical")) AND NOT archived
     ```
     解析为 AST → 下推给索引引擎。

2. **自动索引 vs 显式索引规则**：
   - 自动索引：所有 metadata 字段写入全文索引（成本高，适合 <100 字段）。
   - 显式规则：用户声明「元数据索引字段」（通过 PUT bucket policy 或新 API）：
     ```
     POST /v1/buckets/{bucket}/meta-index
     {"fields": ["department", "project", "amount"]}
     ```
   - 只索引声明过的字段，降低写入开销。

3. **集成到 REST/S3 API**：
   ```
   GET /v1/search/metadata?q=department=eng&sort=-created_at&limit=20
   ```
   或扩展 S3 兼容的 `ListObjects` 请求参数加上 `metadata-filter`。

4. **与 AI 检索联动**：
   - 先执行 metadata 查询缩小候选范围，再对候选集执行语义搜索——大幅降低向量搜索的扫描量。
   - 在 Agent 中暴露 `search_metadata(query)` 工具。

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| 元数据字段爆炸 | 限制索引字段数量（最大 50 个显式字段）；每个字段值最大 1KB；超出截断或拒绝。 |
| 查询性能退化 | 无索引字段的查询回退到全表扫描 + 告警；不允许跨租户查询。 |
| 嵌套值 | 当前 `Metadata` 是 `map[string]string`，无嵌套。如需嵌套，需引入 `map[string]any`（破坏性变更）。MVP 保持 flat key-value，用户自行 `key.subkey` 约定。 |
| 与版本控制的关系 | 索引只跟踪当前活跃版本；历史版本不参与元数据查询（需要时通过版本列表单独获取）。 |
| 一致性和新鲜度 | 索引更新与业务写入异步（通过 EventBus 或 job queue），最大延迟 ~1s。如需强一致，在业务事务内同步写索引（增加写延迟）。 |

---

## 总结优先级建议

| 方向 | 投入 | 产出 | 依赖面 | 建议顺序 |
|------|------|------|--------|---------|
| 1. 存储类生命周期 | 中 | 成本优化 + S3 兼容性 | 已有 `StorageClass`、Lifecycle config 可扩展 | **P1** |
| 2. 内容寻址去重 | 大 | 存储效率 + 增量快照 | CDC 库、引用计数、GC | **P2** |
| 3. 多层缓存 | 中 | 读性能 + 带宽节省 | 缓存库 + CDN SDK | **P1** |
| 4. 事件 WAL + 回放 | 大 | 审计 + DR + 消费者简化 | `event_log` 表 + 回放引擎 | **P2** |
| 5. 元数据查询引擎 | 中 | 可发现性 + 自动化 | 索引引擎集成 | **P1** |

**说明：**
- **P1** 方向可以在 2–4 周内产出 MVP，对现有架构侵入小，用户价值立即可见。
- **P2** 方向需要更长的设计和实现周期，但一旦完成将成为该项目的核心技术壁垒。

所有方向均应遵守现有工程约束（单文件 ≤500 行、单函数 ≤50 行、圈复杂度 ≤10、先测试后提交、`make check` 全绿），并在实现前完成架构设计与 API 契约评审。
