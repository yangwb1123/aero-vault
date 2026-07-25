# AeroVault 架构师/产品经理视角 — 第 82 轮：产品架构深度扫描

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库扫描（`cmd/server/main.go` + `internal/*` 全部 23+ 子包，三套 SDK，MCP，Web UI，48 对迁移文件，`docs/requirements/` 下全部 81 份既有分析文档）  
> **去重验证：** 对 `docs/requirements/` 下全部 81 份既有分析文档逐方向进行正则交叉验证 + 语义比对 + 代码锚点映射  
> **日期：** 2026-07-11  
> **核心原则：** 选取代码中存在具体、可量化、且在前 81 轮分析中 **零实质性架构分析** 或 **仅有行级提及** 的系统盲区

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 | 既有分析覆盖 |
|---|------|------|--------|---------|---------|-------------|
| **1** | **版本化桶的对象版本无限制增长（Version Bloat in Versioned Buckets）** | 存储成本/运维 | **P1** — 启用版本控制的桶中对象版本无限积累，无 `max_versions` 上限、无自动裁剪过期非当前版本的机制。单次误操作（如循环 CI 写入相同 key）可产生数十万个版本，存储成本线性膨胀且 List 性能随版本数退化。S3 标准生命周期规则 `NoncurrentVersionExpiration` 完全缺失 | `internal/repository/repository.go:31-55`（`BucketConfig` 无 `MaxVersions`/`NoncurrentDays` 字段）；`internal/reconcile/lifecycle.go:15-90`（`LifecycleJob.sweepExpired` 仅对当前版本按 `updated_at` 过期处理，**无视历史版本**）；`internal/repository/sql_objects.go:InsertObjectVersion`（每次生成新 `VersionID`，无旧版本检查）；`internal/service/file_features.go:ListVersions`（返回全部版本，无分页上限）；`internal/repository/sql_buckets.go:GetBucketConfig`（`SELECT … versioning, …` 无 `max_versions` 列） | ✅ **完全去重**（v15 在"方向一：存储可观测性"的 feature matrix 中以 3 行提及 `max_versions` 作为一项配置变体，**零代码锚点、零影响分析、零实现路径**。其余 80 份 doc 零提及版本爆炸或非当前版本自动裁剪。v80 方向一覆盖 `storage_class` 生命周期转换（STANDARD→GLACIER），与本方向正交） |
| **2** | **Chat/Agent RAG 单请求上下文窗口溢出（RAG Context Window Overflow）** | AI 可靠性/用户体验 | **P2** — `Chat.buildChatPrompt` 将所有 K 个检索块无差别拼入 LLM 上下文。K=20、`AI_CHUNK_WINDOW=600` 时，上下文可能超过 12K tokens。若 LLM 上下文窗口为 4K/8K/16K，超出部分被 LLM 静默截断（早期的 token 丢失），或被 API 拒绝（400 context_length_exceeded）。用户无法感知哪些 chunk 被裁掉，也无法控制上下文预算。Agent 的同题加 4KB 文件读取更严重 | `internal/ai/chat.go:66-82`（`buildChatPrompt`——无 token 计数、无截断、无 token budget 检查，直接 `fmt.Fprintf` 拼接全部 chunks）；`internal/ai/chat.go:53-55`（LLM context window size 全无知：Chat 没有 `MaxContextTokens` 配置项，也不从 LLM response 中读取 token 消耗）；`internal/ai/agent.go:66-75`（agent 的 tool call 消息历史无限追加，不受 token 预算约束）；`internal/ai/search.go:Query`（检索返回全部 K 个 chunk，无 relevance threshold 控制）；`internal/config/config_ai.go`（无 `AI_MAX_CONTEXT_TOKENS` 或 `AI_CHAT_CONTEXT_LIMIT` 配置项） | ✅ **完全去重**（v71 方向一覆盖"会话级 MemoryManager"聚焦多轮对话的历史压缩与摘要，**非单次 RAG 请求的上下文窗口溢出**——两个独立问题。v22 在"可编程转换管线"一节以一行提及"大文件转换超时 → LLM context window 溢出"，**零代码锚点、零影响分析**。其余 79 份 doc 零分析此问题） |
| **3** | **本地 Local 存储无文件系统级租户隔离（Local Storage Filesystem-Level Tenant Isolation Gap）** | 多租户可靠性/安全 | **P1** — 租户级别配额（`MaxBytes`/`MaxObjects`）是服务层软限制：`preflightQuota` 在 PUT 路径上基于 DB 中的 `used_bytes` 做比较检查。DB 值与真实磁盘用量之间不存在原子写入契约。一个恶性租户可以通过（a）并发写入绕过配额检查窗口、（b）直接通过 Presign URL 写入绕过服务层、（c）填充磁盘占满全部分区导致其它租户无法写入——三种方式耗尽资源。本地 FS backend (`LocalStorage`) 不做任何 per-tenant 磁盘空间隔离 | `internal/service/file_crud.go:48-66`（`preflightQuota`——服务层 TOCTOU：检查与写入非原子）；`internal/storage/local_write.go`（`LocalStorage.Put` 直接 `os.Create`，无 per-tenant 目录容量限制）；`internal/storage/storage.go`（`Storage` 接口无磁盘容量/压力接口）；`internal/repository/quota.go`（`AddTenantUsage` 写入 SQL 不校验磁盘剩余空间）；`internal/service/file_features.go:PresignPut`（预签名 URL 跳过 `preflightQuota` 检查）；`internal/config/config_storage.go`（LocalConfig 无 `PerTenantDiskLimit` 或 `MaxDiskUsagePercent` 参数） | ✅ **完全去重**（v11 方向二覆盖"磁盘容量监控与 DegradedMode"本——聚焦**全局**磁盘满的降级策略，**非租户级隔离**。`grep -rn "per.tenant.*disk\|tenant.*isolat.*disk\|fs.*quota\|file.*system.*quota\|disk.*quota.*tenant" docs/requirements/` → **0 命中**） |
| **4** | **AI 重索引批量操作无进度可观测性（Reindex Progress Visibility & Control Gap）** | 运维/平台完整 | **P2** — `ReindexStale` 在后台以 goroutine 方式运行，无任何对外可见的进度界面。操作者无法知道（a）当前有多少对象待重索引、（b）已处理多少 / 总计多少、（c）重索引速度与预期完成时间、（d）是否有大量失败需手动干预。也不支持暂停/恢复/取消操作。对于百万级对象仓库，嵌入模型升级后的全库重索引可能持续数天，完全不可观测 | `internal/ai/indexer.go:220-240`（`ReindexStale`——单次 `for range` 循环，无进度回调、无状态更新）；`internal/ai/indexer.go:50-65`（`Indexer` 结构无 `progress`/`cancel`/`pause` 字段）；`internal/repository/repository.go`（无 `reindex_progress` 表或 `ReindexStatus` 接口方法）；`internal/api/rest/router.go`（无 `GET /v1/admin/reindex/progress` 或 `POST /v1/admin/reindex/cancel` 路由）；`cmd/server/main.go:650-658`（`startReindexOnStartup`——`go func()` 后台启动，结果仅一行 log）；`internal/ai/reindex_test.go`（仅测试"能处理几行"，不测暂停/恢复/进度查询） | ✅ **完全去重**（v63 方向一覆盖"内容感知增量重索引"——聚焦**通过 Content-Hash 跳过未变更对象的增量**而非**进度可观测性**。`grep -rn "reindex.*progress\|reindex.*status\|reindex.*pause\|reindex.*resume\|reindex.*cancel\|reindex.*track\|reindex.*eta\|重索引.*进度\|重索引.*状态\|reindex.*dashboard" docs/requirements/` → **0 命中**） |
| **5** | **S3 兼容 `GetBucketLocation` 返回硬编码空值（S3 Bucket Location Hardcoded to us-east-1）** | 协议合规/多区域 | **P3** — `GET /{bucket}?location` 总是返回空的 `locationConstraint`（S3 客户端将其解释为 `us-east-1`），与实际部署区域无关。这破坏了（a）AWS SDK 的区域感知端点路由（某些 SDK 根据 location 自动选择 endpoint）、（b）客户端的多区域延迟路由策略、（c）S3 Transfer Acceleration 的前提条件（需 location 非 us-east-1）、（d）`aws s3api get-bucket-location` 的可靠性。配置中也无可配置的 `S3_REGION` 参数用于覆盖 | `internal/api/s3compat/bucketconfig.go:153-167`（`getBucketLocation` 返回 `locationConstraint{}`——硬编码空值）；`internal/api/s3compat/xml.go:342-347`（`locationConstraint` 结构体无 `Location` 字段，XML 序列化后为空标签）；`internal/api/s3compat/handler_test.go:603-620`（`TestBucketLocation`——仅测试返回 200，**不测试 Location 值**）；`internal/config/config.go`（无 `S3_REGION` 或 `S3_BUCKET_LOCATION` 配置项）；`internal/api/s3compat/sigv4_test.go:67`（SigV4 测试硬编码 `us-east-1`——全局依赖此常量） | ✅ **完全去重**（`grep -rn "getBucketLocation\|bucket.*location.*fix\|location.*hardcode\|location.*us-east\|bucket.*location.*constraint\|GetBucketLocation.*hardcoded\|bucket.*location.*return" docs/requirements/` → **0 命中**。81 份 doc 无任何一篇分析此问题） |

---

## 方向一：版本化桶的对象版本无限制增长

### 现状

当桶启用版本控制时，每次覆盖写入创建新版本：

```go
// internal/service/file_crud.go:176-184
if bcfg.Versioning {
    versionID = repository.NewVersionID()
    sk = sk + "@v" + versionID
}
```

每行携带完整的元数据（`objects` 表行）和物理 blob（存储后端对象）。**没有任何机制限制版本总数**或按龄期自动清理旧版本。

### 影响分析

| 场景 | 版本数 | 存储成本（相对于单版本） |
|------|--------|------------------------|
| 正常 CI 每部署写 1 次 × 365 天 | 365 | 365× |
| 循环写配置文件的 bug 每小时写 60 次 × 一天 | 1,440 | 1,440× |
| 监控系统将同一指标文件每分钟覆写一次 × 30 天 | 43,200 | 43,200× |

`internal/service/file_features.go:ListVersions` 返回 **无分页** 的全部版本——数千个版本行全量加载到内存。

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/repository/repository.go:BucketConfig` | `Versioning bool`，无 `MaxVersions`/`NoncurrentDays` | 无版本保留上限配置 |
| `internal/repository/sql_objects.go:InsertObjectVersion` | 每次插入新版本行，无前版本淘汰 | 无 `DELETE FROM objects … WHERE version_id NOT IN (latest N)` |
| `internal/reconcile/lifecycle.go:LifecycleJob` | `sweepExpired` 仅对当前版本按 `updated_at` 过期 | 无 `sweepNoncurrentVersions` |
| `internal/api/s3compat/bucketconfig.go:putBucketVersioning` | 仅开关 `Enabled`，不接受 `MaxVersions` | S3 `PUT /{bucket}?versioning` 的 XML body 无 parsing |
| `internal/service/file_features.go:SetBucketVersioning` | 签名 `(enabled bool)` 限 | S3 `VersioningConfiguration` 包含 `Status` + 可选的多版本保留策略 |

### 推荐方案

1. **扩展 `BucketConfig`**：增加 `MaxVersions int`（0 = 无限制，默认 0）+ `NoncurrentDays int`（保留非当前版本的天数）
2. **扩展 `PUT /{bucket}?versioning`**：解析 S3 `VersioningConfiguration` XML 中的 `Status` + 可选生命周期参数
3. **新增 `LifecycleJob.sweepNoncurrentVersions`**：每轮 reconciliation 后，对每个版本化桶：
   - 若 `MaxVersions > 0`，对每个 key 保留最新 N 个版本，先按时间裁剪
   - 若 `NoncurrentDays > 0`，删除早于该时间的非当前版本版本
4. **存储 cleanup**：版本行删除后异步删除对应的 `@v<id>` blob（延迟可配置）
5. **可观测性**：`version_pruned_total{reason∈{max_versions,noncurrent_days}}` counter

### 边界情况

| 边界 | 行为 | 
|------|------|
| `MaxVersions=0`（默认） | 向后兼容：无版本裁剪，行为保持不变 |
| `MaxVersions=1` | 自动保留仅最新版本（等效于非版本化但保留版本行） |
| `NoncurrentDays=0` | 仅用 `MaxVersions` 裁剪，不基于时间 |
| 版本锁（`LockedUntil`） | 裁剪时跳过 `locked_until > now` 的非当前版本 |
| `MaxVersions` 与已有版本数冲突 | 立即触发一次性裁剪（可能耗时），记录 `version_pruned_initial` 指标 |
| 并发写入时的版本计数窗口 | 先 DELETE 旧版本再 INSERT 新版本，或使用（`MaxVersions` threshold 触发 + 后台惰性清理） |
| 删除最后一个非当前版本 | 不能删除当前版本（active version），当前版本须等到被新版本替代后才可裁剪 |

---

## 方向二：Chat/Agent RAG 单请求上下文窗口溢出

### 现状

```go
// internal/ai/chat.go:66-82
func (c *Chat) buildChatPrompt(ctx context.Context, req ChatReq) ([]ChatMessage, []Hit, error) {
    // ...
    var ctxBlock strings.Builder
    ctxBlock.WriteString("Knowledge base context:\n\n")
    for i, h := range hits {
        fmt.Fprintf(&ctxBlock, "[#%d] %s/%s (score %.3f)\n%s\n\n", i+1, h.Bucket, h.ObjectKey, h.Score, h.Chunk)
    }
    // ...
}
```

所有 K 个检索块 **无差别拼接**，完全不感知 LLM 的上下文窗口限制。

### 影响分析

| K | AI_CHUNK_WINDOW | 估算 token 数（1 token ≈ 0.75 英文词） | 4K 窗口 | 8K 窗口 | 16K 窗口 | 32K 窗口 |
|---|----------------|----------------------------------------|---------|---------|----------|----------|
| 5 | 600 (≈450 词) | ~3,000 | ✅ | ✅ | ✅ | ✅ |
| 10 | 600 | ~6,000 | ❌ 溢出 | ✅ | ✅ | ✅ |
| 20 | 600 | ~12,000 | ❌ 溢出 | ❌ 溢出 | ✅ | ✅ |
| 50 | 600 | ~30,000 | ❌ 溢出 | ❌ 溢出 | ❌ 溢出 | ✅ |

加上系统提示词 + 用户问题 + 引用模板开销（~500 tokens），真实可用预算进一步缩小。

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/ai/chat.go:66-82`（`buildChatPrompt`） | `fmt.Fprintf` 拼接所有 chunks | 无 token 计数、无预算检查、无截断 |
| `internal/ai/chat.go:53-55`（`ChatReq`） | 无 `MaxContextTokens` 字段 | 调用方无法控制上下文预算 |
| `internal/ai/chat.go:53-55`（`Chat` 结构） | 无 `maxContextTokens` 配置 | LLM context window size 完全未知 |
| `internal/ai/agent.go:58-103`（`Run`） | 消息历史无限追加 | 4KB 文件读取 + K 个 chunks 可轻松溢出 32K |
| `internal/ai/search.go:108-120`（`Request.K`） | `K <= 100` 时仅上限检查 | 无 relevance threshold（`min_score`），低分 chunk 浪费预算 |
| `internal/config/config_ai.go` | 无 `AI_MAX_CONTEXT_TOKENS` | 管理员无法配置上下文预算 |

### 推荐方案

1. **Token 预算预检**：`Chat.buildChatPrompt` 统计系统提示 + chunk 上下文 + 用户问题的 token 数，比较 LLM 的 `MaxContextTokens`。超限时按 chunk 相关性得分降序裁减，直到 fit
2. **配置化**：新增 `AI_MAX_CONTEXT_TOKENS`（默认 4096）和 `AI_CONTEXT_RESERVE_TOKENS`（为回答保留的 token 数，默认 500）
3. **Chunk 级 relevance 过滤**：在 `Search` 层增 `MinScore` 参数（默认 0.0，可大于 0 做硬过滤）；低相关性 chunk 不入上下文
4. **Agent 上下文窗口管理**：`Agent.Run` 预检消息历史的 token 总数，达到阈值时自动触发消息压缩（summarize 较旧的 tool call 结果）
5. **可观测性**：`ai_context_truncated_total{reason∈{budget,relevance}}` counter + chat response 中增加 `context_used_tokens` / `chunks_requested` / `chunks_used` 元数据

### 边界情况

| 边界 | 行为 |
|------|------|
| OVER 预算但用户要求最大 K | 按相关性降序裁剪，保留最相关 chunk，记录 `chunks_requested` vs `chunks_used` |
| `AI_MAX_CONTEXT_TOKENS = 0`（默认） | 向后兼容：不设限制，行为不变 |
| Agent 调用 `read_file` 读入大文件（>10K tokens） | 主动截断为前 N tokens + 追加摘要标签；或拒绝读取并提示文件过大 |
| Agent 多轮对话累积 | 每轮计算累积 token 数，临近 `MaxContextTokens` × 70% 时触发历史压缩 |
| 流式响应（`AnswerStream`） | token 预算计算时间点：LLM 调用前预检，SSE headers 发送后不可回退 |
| LLM 返回 `context_length_exceeded` | 捕获后自动重试（降低 K），附加 `x-aero-retry: true` header |

---

## 方向三：本地 Local 存储无文件系统级租户隔离

### 现状

租户级别配额是纯服务层软限制：

```go
// internal/service/file_crud.go:48-66
func (s *FileService) preflightQuota(ctx context.Context, tenant string, size int64, deltaObjects int) error {
    q, qErr := s.repo.GetTenantQuota(ctx, tenant)
    if qErr != nil {
        return nil // ← 配额服务不可用时静默跳过
    }
    if q.MaxBytes > 0 && q.UsedBytes+size > q.MaxBytes {
        return ErrQuotaExceeded
    }
    // ...
}
```

存在以下安全缺口：

1. **配额不可用时跳过**（`qErr != nil`）：DB 故障时所有写入放行
2. **TOCTOU 窗口**：`GetTenantQuota` 与 `AddTenantUsage` 不是同一事务，并发写入可超限
3. **Presign URL 绕过**：`PresignPut` 直接将 URL 交给 `store.PresignPut`，绕过 `preflightQuota`
4. **无磁盘空间感知**：`AddTenantUsage` 成功但磁盘已满时，`store.Put` 失败，配额已递增不可回滚
5. **无 per-tenant 磁盘隔离**：所有 tenant 写入同一 `./var/objects/<tenant>/` 树，一个填充可阻塞所有人

### 影响分析

| 攻击/事故模式 | 后果 | 恢复难度 |
|---------------|------|---------|
| 恶意租户循环写入大文件 | 磁盘 100% → 所有租户 500 Internal Server Error | 高（需人工清理） |
| 配额 DB 不可用 | 所有写入放行写入无限制 | 中（重启 DB） |
| CI 脚本失控 | 数十 GB 日志填满磁盘 | 中（定位 + 清理） |
| 合法租户突发写入超过配额 | 覆盖超限部分写入成功但记录拒绝 | 低（手动删除） |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/service/file_crud.go:48-66`（`preflightQuota`） | `qErr != nil` 跳 + TOCTOU | 无原子配额+写入事务、无备查约束 |
| `internal/storage/local_write.go`（`LocalStorage.Put`） | `os.Create` 无容量检查 | 无文件系统级 per-tenant 配额 |
| `internal/storage/storage.go`（`Storage` 接口） | 无磁盘容量/压力 API | 全局准入无法判断后端资源 |
| `internal/service/file_features.go:PresignPut` | 直接 `store.PresignPut` | 跳过 `preflightQuota` |
| `internal/repository/quota.go:AddTenantUsage` | 独立 UPDATE，无磁盘感知 | DB 成功 ≠ 磁盘有空间 |
| `internal/config/config_storage.go` | 无 `PerTenantDiskLimit` | 管理员无配置位置 |

### 推荐方案

1. **磁盘预留（Disk Reservation）**：写入前通过 `statfs` 获取文件系统剩余空间，低于阈值（`STORAGE_DISK_RESERVE_BYTES`）拒绝写入——全局保护
2. **Per-Tenant 目录配额**：`LocalStorage.Put` 前检查 `<root>/<tenant>/` 目录实际用量，可选 `STORAGE_PER_TENANT_DISK_LIMIT` 硬限制（更准确但 I/O 开销高）
3. **配额+写入原子化**：将 `preflightQuota` + `store.Put` + `AddTenantUsage` 纳入同一个重试循环：先保留配额，成功后提交，失败回滚配额
4. **Presign URL 配额检查**：`PresignPut` 调用 `preflightQuota`（至少检查当前 tenant 是否已超限）并记录预留
5. **量化指标**：`disk_bytes_reserved{tenant}` gauge + `disk_write_rejected{reason∈{disk_full,tenant_over_limit}}` counter

### 边界情况

| 边界 | 行为 |
|------|------|
| 多租户共享同一 `LocalStorage` 根目录 | 所有方案均适用，per-tenant 目录隔离 |
| 磁盘故障（硬件/卸载） | `statfs` 返回错误 → 回退到仅服务层配额 → `warn` 日志触发告警 |
| 小文件大量写入 | 磁盘 inode 耗尽而非字节耗尽——需额外 `statfs` 检查 `f_ffree` |
| Presign URL 已在客户端分发后配额降低 | 不可能追溯——建议 Presign URL 附加短 TTL（默认 15 分钟） |
| 并发写入导致配额检查窗口重叠 | 数据库 `SELECT … FOR UPDATE` 或 optimistic lock on `used_bytes` |

---

## 方向四：AI 重索引批量操作无进度可观测性

### 现状

```go
// internal/ai/indexer.go:220-240
func (ix *Indexer) ReindexStale(ctx context.Context, tenant string, limit int) (int, error) {
    ids, err := ix.repo.ListObjectIDsToReindex(ctx, tenant, ix.embedder.Name(), limit)
    // ...
    for _, id := range ids {
        if err := ix.IndexObjectByID(ctx, id); err != nil {
            ix.logger.Warn("reindex stale object", "object_id", id, "err", err)
            continue
        }
        n++
    }
    // ...
    ix.logger.Info("reindexed stale objects", "count", n, "model", ix.embedder.Name())
    return n, nil
}
```

整个重索引过程：
- **无进度报告**：完成后才一行 log（`"reindexed stale objects"`），运行中零可见性
- **无暂停/恢复**：`context.Context` 取消只能终止，不能暂停
- **无进度查询 API**：管理员无法通过 REST/CLI 查询重索引状态
- **无分页/批量控制**：`limit` 是硬编码的 1000，不支持从外部控制
- **无统计聚合**：失败数、跳过数、处理速率、ETA 均不可知

### 影响分析

| 嵌入模型变更场景 | 全库对象数 | 重索引时间估计（假设 5 obj/s） | 管理员体验 |
|------------------|-----------|-------------------------------|-----------|
| 模型升级（dim 256→384） | 10 万 | ~5.5 小时 | 零反馈，只能看 log |
| 模型切换（hash→HTTP） | 100 万 | ~55 小时 | 完全不可控 |
| PII 扫描开启 | 50 万 | ~28 小时 | 不知进度，无法计划 |
| CI 发布后自动触发 | 1 万 | ~33 分钟 | 不知是否完成 |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/ai/indexer.go:220-240`（`ReindexStale`） | `for range` + `continue` + 最终 log | 无进度回调、无状态更新 |
| `internal/ai/indexer.go:50-65`（`Indexer` 结构） | 无 `progress`/`cancel`/`pause` 字段 | 运行时无法控制 |
| `internal/repository/repository.go` | 无 `reindex_progress` 表或状态接口 | 无法持久化进度 |
| `internal/api/rest/router.go` | 无 `reindex` 相关路由 | 管理员无 API 入口 |
| `cmd/server/main.go:650-658`（`startReindexOnStartup`） | `go func()` 后台启动 | 结果仅一行 log |
| `internal/config/config_ai.go`（`AI_REINDEX_STALE_ON_START`） | 布尔开关 + 硬编码 `limit=1000` | 无速率、批次参数 |

### 推荐方案

1. **Progress record**：新增 `reindex_progress` 表（或复用 `runtime_config` 表），记录 `{tenant, model, total, processed, failed, started_at, status}`，每处理完一批更新
2. **进度查询 API**：
   - `GET /v1/admin/reindex/progress?tenant=default` → 当前进度
   - `POST /v1/admin/reindex/pause` → 设置 `cancel` flag
   - `POST /v1/admin/reindex/resume` → 清除 flag，继续处理
   - `POST /v1/admin/reindex/trigger` → 手动触发重索引（可选指定 model）
3. **批量速率控制**：`AI_REINDEX_BATCH_SIZE`（默认 100）+ `AI_REINDEX_SLEEP_MS`（默认 100ms，避免存储 IO 冲击）
4. **可观测性**：
   - `reindex_total_objects` gauge（总量）
   - `reindex_processed_objects` gauge（已处理）
   - `reindex_failed_objects` counter
   - `reindex_duration_seconds` histogram
   - Prometheus alert：`reindex_processed_objects / reindex_total_objects < 1` after stale duration

### 边界情况

| 边界 | 行为 |
|------|------|
| 重索引过程中嵌入模型再次变更 | `progress.model` 与当前模型不匹配时自动取消并重新开始 |
| 服务重启后恢复进度 | 读取 `reindex_progress` 表的 `last_processed_id` / `last_key` 断点续传 |
| 全库 0 对象全部落在 `skip` 条件 | 记录 `processed = total`，状态置为 `completed_skip_all` |
| 大量失败（>50% error rate） | 自动暂停，触发 `reindex_high_error_rate` 告警，记录最后失败的 10 个 object_id |
| 租户级重索引 | `Limit` + tenant 参数 = 每个租户独立跟踪进度 |

---

## 方向五：S3 兼容 `GetBucketLocation` 返回硬编码空值

### 现状

```go
// internal/api/s3compat/bucketconfig.go:153-167
func (h *Handler) getBucketLocation(w http.ResponseWriter, r *http.Request, bucket string) {
    // Empty constraint = us-east-1, the standard single-region response.
    writeXML(w, http.StatusOK, locationConstraint{Xmlns: s3Namespace})
}
```

响应体：

```xml
<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/"/>
```

AWS S3 客户端将空的 `<LocationConstraint/>` 解释为 `us-east-1`，但实际部署可能在 `eu-west-1`、`ap-northeast-1` 甚至本地私有数据中心。

### 影响分析

| 调用方 | 依赖 location 的行为 | 问题 |
|--------|---------------------|------|
| `aws s3api get-bucket-location` | CLI 工具显示区域 | 总是 us-east-1，混淆运维 |
| AWS SDK（v2）自动 endpoint 选择 | 部分 SDK 根 location 调整 endpoint | 请求被路由到错误区域 |
| `s3cmd` / `rclone` | 多区域场景下尝试端点自动发现 | 行为不可预测 |
| S3 Transfer Acceleration | AWS 要求 bucket location 非 us-east-1 | 无法通过 S3TA 兼容性测试 |
| 跨区域复制（CRR） | 源/目标区域必须匹配 | 配置校验失败 |
| latency-based routing（CloudFront） | 基于区域做 DNS 路由 | 路由到错误的边缘站点 |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/api/s3compat/bucketconfig.go:153-167` | `getBucketLocation` 返回 `locationConstraint{}` | 硬编码空值 |
| `internal/api/s3compat/xml.go:342-347` | `locationConstraint` 无 `Location` 字段 | XML 序列化后为空标签 |
| `internal/api/s3compat/handler_test.go:603-620` | `TestBucketLocation` 仅测 200，不测值 | 测试盲区 |
| `internal/config/config.go` | 无 `S3_REGION` 配置项 | 管理员无法配置 |
| `internal/config/config_storage.go` | 无 `Location` 字段 | 存储后端无区域属性 |

### 推荐方案

1. **配置化**：新增环境变量 `S3_BUCKET_LOCATION`（默认 `""`），为空时沿用现行为；非空时写入 XML 响应体
2. **存储后端区域属性**：`storage.Storage` 接口增加 `Region() string` 方法（local 返回空，S3 返回真实 bucket region）
3. **响应体修复**（最小变更）：

```go
func (h *Handler) getBucketLocation(...) {
    writeXML(w, http.StatusOK, locationConstraint{
        Xmlns:    s3Namespace,
        Location: cfg.S3Compat.Location, // from config
    })
}
```

4. **可观测性**：`s3_bucket_location` gauge（值为 1，label=location）暴露当前配置

### 边界情况

| 边界 | 行为 |
|------|------|
| `S3_BUCKET_LOCATION=""`（默认） | 向后兼容：返回空 constraint，客户端解释为 us-east-1 |
| S3 后端真实 region 提取 | `s3.go` 在 `NewS3Storage` 时通过 `GetBucketLocation` API 获取真实 region |
| 同一实例多个 bucket 不同 location | 当前无法表达——v2 可扩展为 per-bucket location |
| 配置为 `us-east-1` | 明确的 us-east-1 返回，与隐式的空值对客户端行为略有不同（XML tag 非空） |
| 中国区域（`cn-north-1`） | 标准 `LocationConstraint` 不涉及分域，直接透传即可 |

---

## 跨方向关联矩阵

| 方向 | 与其它方向的关联 | 依赖关系 |
|------|----------------|---------|
| 方向一（Version Bloat） | v80 方向一（Storage Class Transition）——生命周期规则的两种语义；v79 方向一（Chunk Retention）——重索引与版本裁剪共享 reconciliation cadence | 独立实现 |
| 方向二（Context Window） | v71 方向一（Session Memory）——不同抽象层级的上下文管理；v34 方向三（AI 评估框架）——MRR/NDCG 可验证裁剪策略质量 | 依赖方向一？的 `MinScore` relevance threshold |
| 方向三（FS Quota） | v11 方向二（DegradedMode）——全局磁盘满 vs 租户级隔离；v80 方向四（SDK 管理面）——仅 CLI 可配 quota | 配额检查路径与 `preflightQuota` 共享 |
| 方向四（Reindex Progress） | v63 方向一（增量重索引）——Content-Hash 跳过 + 进度追踪互补 | 建议先实现 v63 增量跳过，再叠加进度 |
| 方向五（Bucket Location） | 独立 | 零依赖，最小侵入 |

---

## 优先级建议

| 方向 | 实施工作量估计 | 风险回报比 | 建议排期 |
|------|--------------|-----------|---------|
| **1. Version Bloat** | ~3-5 天（schema + reconcile worker + S3 API 扩展 + test） | ⭐⭐⭐⭐⭐ 直接成本优化 | **Sprint N** |
| **2. Context Window** | ~4-6 天（tokenizer 集成 + 预算逻辑 + chunk 裁剪 + Agent 扩展） | ⭐⭐⭐⭐ 用户体验提升 + 运行时可靠性 | **Sprint N+1** |
| **3. FS Quota** | ~5-8 天（disk stat + per-tenant 目录 + 原子配额 + Presign 改造） | ⭐⭐⭐⭐⭐ 多租户生产安全的基石 | **Sprint N** |
| **4. Reindex Progress** | ~3-5 天（progress 表 + API + 暂停/恢复 + 前端 Dashboard 扩展） | ⭐⭐⭐ 运维体验提升 | **Sprint N+2** |
| **5. Bucket Location** | ~0.5 天（配置 + 序列化 + 测试） | ⭐⭐ 协议合规边际改进 | **Sprint N+3** |
