# 架构分析报告：aero-vault 系统深度评估

> 基于 5 个已确认验证方向，对系统架构进行全维度分析  
> 日期：2026-07-12 | 版本：1.0

---

## 1. 架构评估

### 1.1 当前架构优势

aero-vault 的整体架构设计质量较高，值得肯定的方面包括：

| 维度 | 评价 | 依据 |
|------|------|------|
| **分层清晰度** | ⭐⭐⭐⭐⭐ | Protocol Adapters → FileService → Storage/Repository 三层边界严格，无绕过 |
| **扩展性** | ⭐⭐⭐⭐ | Storage/Repository/AI 均采用接口抽象 + factory + 配置驱动，新增后端仅需实现接口 |
| **安全默认** | ⭐⭐⭐⭐⭐ | AI/RAG/Replication/WebDAV 全部 opt-in，默认基线零外部依赖 |
| **可测试性** | ⭐⭐⭐⭐ | 接口隔离清晰，`httptest` + mock 可以独立测试各层 |
| **可观测性** | ⭐⭐⭐⭐ | OTel 15 instruments + Prometheus 面板 + 告警规则完备 |

### 1.2 关键设计决策评估

交付的 5 个问题之中，**有一些属于有意识的设计权衡，另一些则是遗漏**。需要区分对待：

| 问题 | 性质 | 评估 |
|------|------|------|
| **搜索结果摘要/高亮缺失** | 🔴 **功能遗漏** | 面向用户的搜索功能缺少标准 UX 特性。不是架构问题，但影响产品完成度 |
| **S3 `x-amz-tagging` 静默忽略** | 🟡 **协议兼容性缺陷** | 违反了 S3 兼容性契约。`service.PutOptions.Tags` 已存在于接口层，是 handler 遗漏 |
| **按桶/前缀分层配额** | 🟢 **新功能需求** | 当前架构仅支持租户级配额。需要扩展 `Quota` 模型，但不改变现有架构 |
| **多协议写入并发一致性** | 🟠 **架构债务** | 是现有设计中最重要的薄弱环节。跨协议并发写入可能导致数据竞争和状态不一致 |
| **存储后端健康管理** | 🟡 **监控缺陷** | 基于失败计数的熔断器是通用方案，但缺少延迟感知是对高可用要求的不足 |

### 1.3 架构债务识别

```
紧急度 ↑
     │
  P0 │ 🔴 多协议写入无锁/CAS                 ← 数据一致性风险
     │    （无版本控制时无条件 REPLACE）
     │
  P1 │ 🟠 健康管理缺少延迟感知                 ← 降级决策延迟
     │    （仅失败计数，50%缓慢请求不触发熔断）
     │
  P2 │ 🟡 S3 `x-amz-tagging` 遗漏            ← 兼容性缺口
     │ 🟡 搜索结果无摘要                      ← UX 缺陷
     │
  P3 │ 🟢 分层配额                           ← 新功能
     └─────────────────────────────────────→ 实现成本 →
```

**架构债务核心观察：** 该系统的本质是一个**多协议共享状态的对象存储服务**。当前架构对`状态修改`路径的并发保护几乎为零——这是最危险的债务。S3 协议在单后端系统内部通常不要求分布式一致性（因为 S3 本身是最终一致性模型），但跨协议（REST 管理操作 + S3 用户操作 + WebDAV 同步操作）并行写入同一对象，在没有乐观锁或 CAS 的情况下，会产生竞态条件。

---

## 2. 扩展方向

### 2.1 方向 A：状态变更的乐观锁/CAS 机制

**等级：** P0 · 🔴 高优先级

#### 为什么需要

当前所有写入路径（REST PUT、S3 PUT、WebDAV PUT、MCP write_file）均无条件调用 `UpsertObject`（`INSERT OR REPLACE`）。当两个协议同时写入同一 key 时，后完成的写入覆盖前一个，**即使前一个写入在业务逻辑上已提交成功**。具体风险场景：

- **跨协议覆盖：** S3 multipart upload 完成的同时 REST PUT 同一 key → 最终状态不确定
- **条件更新失效：** 客户端发送 `If-Match` ETag 条件请求 → 写入路径却从不在 DB 层校验 ETag
- **幂等密钥竞争：** 两个并发请求携带相同 `Idempotency-Key` → 竞态条件下可能执行两次

#### 核心挑战

| 挑战 | 说明 |
|------|------|
| **SQLite 不支持高并发悲观锁** | 默认后端 SQLite 写锁为 DB 级别，细粒度行锁不可用。乐观锁是唯一可行路径 |
| **版本号的原子性** | 需要在单条 SQL 中完成 `WHERE version = oldVersion` 检查和 `SET version = newVersion, content_hash = ?` 更新 |
| **条件请求与乐观锁的映射** | `If-Match` / `If-None-Match` → 应映射为 `WHERE etag = ?` 校验，需要统一到 repository 层 |
| **向后兼容** | 当前无版本号的对象库需要能平滑迁移。`version` 列可为 NULL 表示"无条件覆盖"模式 |

#### 架构变更

```
当前：
  Handler → svc.Put(ctx, key, data, opts) → repo.UpsertObject(...)  ← 无条件

变更后（可选方案）：
  
  方案 A（推荐）：数据库乐观锁
  repo.UpdateObject(ctx, id, expectedVersion, data) → ROWS_AFFECTED == 0 → 409 Conflict
  新增 Object.Version 列，每次写入递增

  方案 B：FileService 层互斥锁
  svc.muMap.Lock(keyHash) → 逐个协议排队 → 性能瓶颈，不推荐

  方案 C：文件系统级 lock file
  Put 前创建 .lock 文件，原子 rename → 复杂且难清理，不推荐
```

**接口变更建议：**

```go
// 新增（兼容旧行为）
type PutOptions struct {
    // ... 现有字段
    ExpectedVersion *int64  // nil = 无条件覆盖（向后兼容）
    ExpectedETag    *string // nil = 不校验
}

// repository 层新方法（不破坏现有 UpsertObject）
UpdateObjectConditional(ctx, obj, expectedVersion, expectedETag) (updated bool, err error)
```

#### 对现有系统的影响

- **范围：** `repository/sql_objects.go`、`service/file_crud.go`、`service/file.go`
- **版本控制兼容：** 已启用版本控制的 bucket 在 `@v<id>` 路径上已隐式具备版本隔离，乐观锁对版本化 bucket 是补充而非替代
- **测试影响：** 所有现有写入测试需要增加并发写入场景。SQLite 的串行化特性使乐观锁实现简单

---

### 2.2 方向 B：分层资源配额系统

**等级：** P2 · 🟢 中等优先级

#### 为什么需要

当前仅支持租户级配额（`MaxBytes` / `MaxObjects`）。实际多租户服务中，以下场景无法满足：

- **桶级限制：** 租户内不同桶（如 `logs/` vs `backups/`）应分配合额上限
- **前缀级限制：** 同一桶内不同前缀（`/team-a/` vs `/team-b/`）独立计算用量
- **动态配额调整：** 运营管理员需要在不重启服务的情况下调整配额
- **配额预警：** 达到 80% 阈值时触发事件/通知

#### 核心挑战

| 挑战 | 说明 |
|------|------|
| **用量统计的精度与性能平衡** | 实时统计每个 PUT/DELETE 更新 `UsedBytes` 计数 → 高并发下的写争用 |
| **层级查询收敛** | `preflightQuota` 需要检查 `bucket quota → prefix quota → tenant quota` 链，取最小有效值 |
| **存量数据同步** | 首次配置桶级配额时，需要扫描已有对象计算用量，不能在配置瞬间产生巨大 `COUNT(*)` 查询 |
| **前缀通配匹配** | 前缀不是严格目录层次（`/a/b` PUT 可能匹配 `/a/b/c` 配置的前缀配额），需定义匹配语义 |

#### 架构变更

**数据模型扩展：**

```
当前：TenantQuota
      - Tenant   string
      - MaxBytes int64
      - MaxObjects int64
      - UsedBytes int64
      - UsedObjects int64

扩展：QuotaResource
      - ID           string (PK)
      - Tenant       string
      - ResourceType enum{tenant, bucket, prefix}
      - ResourceName string  // "" for tenant, bucket name, prefix path
      - MaxBytes     int64   // 0 = unlimited
      - MaxObjects   int64   // 0 = unlimited
      - UsedBytes    int64
      - UsedObjects  int64
      - AlertPercent int     // 触发告警的百分比阈值
      - CreatedAt    time.Time
      - UpdatedAt    time.Time
```

**查询链设计：**

```go
// preflightQuota 扩展
func (s *FileService) preflightQuota(ctx, tenant, bucket, prefix, objectSize) error {
    // 1. 获取所有已定义的层级配额（tenant → bucket → prefix）
    quotas := s.repo.GetQuotaChain(ctx, tenant, bucket, prefix)
    // 2. 取路径最匹配的有效配额
    effective := resolveTightest(quotas)
    // 3. 校验
    if effective.MaxBytes > 0 && effective.UsedBytes + objectSize > effective.MaxBytes {
        return ErrQuotaExceeded
    }
    // ...类似逻辑检查对象数量
}
```

**选项分析：**

| 方案 | 优点 | 缺点 |
|------|------|------|
| **数据库行级**（推荐） | 与现有架构一致；可索引；可事件驱动缓存失效 | 每次请求需查询 1-3 行，延迟恒定 |
| **内存缓存 + TTL** | 极低延迟；减少 DB 负载 | 写路径需要缓存失效机制；TTL 内配额可能短暂宽松 |
| **Redis 实时计数** | 高吞吐量计数；原子 INCR | 引入新依赖（Redis）→ 违反 `I6`（stdlib 优先）；但可论证 |

#### 对现有系统的影响

- **无破坏性改动：** `TenantQuota` 保留不变，新增 `QuotaResource` 表。`preflightQuota` 方法扩展逻辑，旧调用方不受影响
- **迁移策略：** 首次启动时，`repo.Migrate` 创建新表。可选的 `quota_populate` one-shot 任务计算存量数据
- **性能：** 新增的链式查询应走覆盖索引（`(tenant, resource_type, resource_name)`）；高并发写入下 `UsedBytes` 更新是热点，需要 `UPDATE ... SET UsedBytes = UsedBytes + ? WHERE ...` 原子操作

---

### 2.3 方向 C：协议级遥测与多协议写入冲突检测

**等级：** P1 · 🟡 高价值

#### 为什么需要

当某个对象在短时间内被多个协议轮番写入时，运维人员完全看不到冲突信息。`events.Payload` 不含 `protocol` 字段，日志无法区分来源。这导致：

- **调试困难：** 用户报 "我的文件被覆盖了" → 无法确定是 REST API 还是 S3 客户端还是 WebDAV 同步
- **无冲突检测：** 同一对象在 100ms 内被两个协议写入 → 没有任何告警
- **无请求溯源：** 审计日志需要跨协议关联请求

#### 核心挑战

| 挑战 | 说明 |
|------|------|
| **协议信息的传递** | `http.Request` 级别的 `X-Protocol` 需要从每个 handler 传递到 `service.Put` 参数中 |
| **冲突判定阈值** | 多少毫秒内两个写入视为"冲突"？需要在可配置 + 默认合理值之间平衡 |
| **写入频率 vs 存储开销** | 写入频率高的对象会产生大量冲突事件 → 需要采样或聚合 |

#### 架构变更（轻量级）

```go
// 1. service 层 PutOptions 增加 SourceProtocol 字段
type PutOptions struct {
    // ... 现有
    SourceProtocol string // "rest", "s3", "webdav", "mcp"
}

// 2. events.Payload 扩展
type Payload struct {
    // ... 现有
    Protocol string `json:"protocol,omitempty"`
}

// 3. 可选的冲突检测 worker（在 EventBus 层订阅 object.created）
// 冲突规则：同一 key 在 T 毫秒内收到 ≥2 个不同 protocol 的写入 → emit conflict 事件
```

#### 对现有系统的影响

- **极小改动：** `PutOptions` 新增字段（零值兼容）；`events.Payload` 新增字段
- **handler 改动：** 每个 handler 在调用 `svc.Put` 前设置 `SourceProtocol: "s3"`，约 1 行代码/文件
- **可关闭：** 冲突检测 worker 默认关闭，通过配置启用

---

### 2.4 方向 D：延迟感知存储熔断器

**等级：** P1 · 🟡 中等优先级

#### 为什么需要

当前熔断器（`circuitbreaker.go`）仅统计连续失败次数。在真实 S3/OSS 场景中，**最危险的不是完全失败，而是缓慢降级**。如果一个云存储后端响应时间从 10ms 上升到 5s（P95 = 8s），熔断器不会触发，因为请求"成功"返回了内容。结果：

- 用户请求堆积在慢后端
- 连接池耗尽
- 级联故障扩散到其他依赖该后端的协议

#### 核心挑战

| 挑战 | 说明 |
|------|------|
| **延迟阈值设定** | 合理的 P95/P99 阈值取决于后端类型（本地 S3 通常 < 50ms，远端 S3 可达 200ms）→ 需要可配置 |
| **滑动窗口计算** | 需要维护一个时间窗口的延迟样本 → 内存 vs 精度权衡 |
| **快速恢复** | 熔断后如何判断后端恢复？半开状态需要发送探测请求 → 当前已有 `readyzHandler` 但未集成到熔断器 |

#### 架构变更

```go
// 方案 A（推荐）：增强现有熔断器
type CircuitBreakerConfig struct {
    FailureThreshold int           // 现有：连续失败次数
    LatencyP99      time.Duration // 新增：P99 超过此值触发熔断
    LatencyWindow   time.Duration // 滑动窗口大小（默认 60s）
}

// 方案 B（更灵活）：使用独立的 latency monitor + circuit breaker
type LatencyMonitor struct {
    window *slidingWindow
    thresholdP99 time.Duration
}
// Monitor 定期检查 → 超过阈值 → 调用 breaker.Trip()
```

**选项分析：**

| 方案 | 复杂度 | 精确度 | 与现有架构集成 |
|------|--------|--------|---------------|
| A: 增强 breaker | 低 | 中（仅 P99） | 最好，在同一结构体中 |
| B: 独立 monitor | 中 | 高（多百分位） | 需要新接口 |
| C: 采用 Go `x/net` 或 `hystrix-go` | 高 | 高 | 引入第三方依赖违反 I6 |

**建议：** 走方案 A，在 `CircuitBreakerConfig` 中增加 `LatencyThreshold` 和 `LatencyWindow`，`recordOutcome` 在请求成功时记录延迟，滑动窗口计算 P99，超过阈值则 trip。

#### 对现有系统的影响

- **`Storage.Stat` 集成：** `readyzHandler` 的探测逻辑应纳入熔断器统计，而非独立调用
- **配置兼容：** 新增配置项默认值 = 0（禁用），零值兼容现有部署
- **测试：** 需要模拟慢响应后端 + 验证熔断触发。可编写 `slowStorageAdapter` wrapper

---

### 2.5 方向 E：搜索结果片段提取与高亮

**等级：** P2 · 🟢 低优先级但高用户可见度

#### 为什么需要

当前 `Hit.Chunk` 返回完整 chunk 文本（600 字符默认窗口）。在搜索场景中，用户需要：

- **上下文片段：** 匹配关键词周围 ±50 字符的浓缩摘要
- **高亮标记：** 关键词在片段中以 `<mark>` 或 `**` 包裹
- **多片段：** 一个对象可能在多个不连续的 chunk 中匹配 → 最多返回 N 个独立片段

**这是合规的产品级搜索的最低要求。** 没有它，搜索功能只是一个"能找到文件但看不出为什么匹配"的半成品。

#### 核心挑战

| 挑战 | 说明 |
|------|------|
| **对齐到原始对象** | chunk 是按固定窗口（600 字符）分割的，片段提取需要知道 chunk 在原始对象中的字节偏移量 |
| **跨 chunk 片段** | 匹配词可能横跨两个 chunk 边界 → 需要合并相邻 chunk 的匹配结果 |
| **中文分词** | `BM25` 按英文 token 做高亮，中文需要 unigram/bigram 级别匹配 |
| **性能** | 片段提取在检索后执行，不能显著增加端到端延迟 |

#### 架构变更（最小）

**`Chunk` 结构体扩展：**

```go
type Chunk struct {
    Index        int    // 已有
    Content      string // 已有
    ObjectID     string // 已有
    // 新增
    ObjectOffset int    // 该 chunk 在原始对象中的起始字节偏移
}

type Hit struct {
    // ... 现有
    Chunk      string   // 完整 chunk（向后兼容）
    Snippet    string   // 新增：匹配关键词周围 ±50 字符的摘要
    Highlights []string // 新增：匹配到的关键词列表（用于前端渲染）
}
```

**片段提取器：**

```go
// 在 search.go 中新增 extractSnippet
func extractSnippet(chunkContent string, queryTokens []string, windowSize int) (snippet string, highlights []string)
```

#### 对现有系统的影响

- **向后兼容：** `Chunk` 保留完整文本，`Snippet` 和 `Highlights` 为新增零值忽略字段。现有客户端不受影响
- **索引重建？** 不需要——片段提取是检索时计算，不依赖索引内容。但 `ObjectOffset` 需要在 chunking 时设置
- **影响 Indexer：** `internal/ai/chunker.go` 需要在分割时计算偏移量（当前可能未计算），这是唯一的非向后兼容变化（索引后的数据不包含 offset，但可降级：offset=0 时提取片段从 chunk 起始位置对齐）

---

## 3. 接口设计建议

### 3.1 Cluster API 设计原则

基于 5 个方向的共同发现，我提出以下全局接口设计原则：

| 原则 | 说明 | 应用场景 |
|------|------|---------|
| **PoLP（最小权限）** | 接口暴露的操作恰好满足调用方需求，不多不少 | `repository` 层：条件更新应拆分为独立方法，而非在现有方法加可选参数 |
| **向后兼容优先** | 新字段零值 = 旧行为；新接口非破坏性 | 所有 5 个方向均可做到零值兼容 |
| **显式优于隐式** | 跨层传递的信息不应隐藏在 context 中 | `SourceProtocol` 应显式出现在 `PutOptions`，而非从 `ctx.Value` 提取 |
| **fail-open 安全默认** | 新功能故障不应影响核心 CRUD | 冲突检测 worker 故障 → 仅丢弃冲突告警不阻断写入 |
| **可观测性正交** | 业务逻辑层不应直接感知 instrumentation | 延迟监控应封装在中间件层，`service.Put` 不记录延迟 |

### 3.2 是否需要新的抽象层

**结论：不需要引入新的架构层级。** 现有三层架构（Adapter → Service → Repository/Storage）在可预见的演进范围内足够容纳全部 5 个方向。但有两处需要追加次级抽象：

```
当前架构                    建议调整
━━━━━━━━━━━━                 ━━━━━━━━━━
Protocol Adapters            Protocol Adapters
    │                            │
FileService                  FileService
    │                            │  ┌─ QuotaManager（新）
    ├── Storage                   ├── Storage
    └── Repository                ├── Repository  
                                  ├── EventBus
                                  └── ConcurrencyGuard（新）
```

**`QuotaManager`（方向 B 专用）：**
- **理由：** 分层配额的链式查询、缓存管理、用量更新逻辑独立于 `FileService` CRUD
- **接口：** `Check(ctx, tenant, bucket, prefix) error` + `RecordUsage(ctx, tenant, bucket, prefix, delta) error`
- **选做接口而非嵌入 FileService：** 便于单元测试和未来切换存储引擎

**`ConcurrencyGuard`（方向 A 可选）：**
- **理由：** 乐观锁校验逻辑（version increment, ETag match, CAS SQL）如果集成到 repository 层会使其过于复杂
- **接口：** `Guard(ctx, key, expectedVersion, expectedETag) (*GuardToken, error)` + `Release(token)`
- **注意：** Go 中可能过度设计。对于 SQLite 场景，单 SQL 语句的原子性足够，不需要独立 guard

### 3.3 向后兼容性清单

| 变更 | 兼容策略 | 过渡期 |
|------|---------|--------|
| `PutOptions.Tags` ← 当前已存在，补充调用方赋值 | 无兼容问题 | N/A |
| `PutOptions.SourceProtocol` 新增 | 零值 `""` → 旧行为 | 无限制 |
| `PutOptions.ExpectedVersion` 新增 | nil → 无条件覆盖（旧行为） | 无限制 |
| `Hit.Snippet` / `Hit.Highlights` 新增 | 零值 `""` / `nil` → 旧客户端忽略 | 无限制 |
| EVENTS 新增 `protocol` 字段 | 旧消费者忽略未知字段 | 无限制 |
| 熔断器配置新增延迟阈值 | 零值 `0` → 禁用延迟检测 | 无限制 |
| 分层配额新表 | 空表 → 退化到租户级配额 | 无需迁移 |

---

## 4. 技术选型

### 4.1 当前技术栈分析

| 组件 | 当前 | 新需求评估 | 结论 |
|------|------|-----------|------|
| **数据库** | SQLite(★) / Postgres | 方向 A 乐观锁：SQLite 支持但无行级锁 | ✅ 可行，通过 `WHERE version = ?` 原子 UPDATE 实现 |
| **向量检索** | BM25(★) / pgvector / Qdrant | 方向 E 片段提取：不依赖向量后端 | ✅ 无变化 |
| **事件总线** | 内存 EventBus | 方向 C 冲突检测：需要高吞吐事件订阅 | ✅ 可行，EventBus 已支持 fan-out |
| **缓存** | 无统一缓存层 | 方向 B 分层配额：需要缓存配额查询结果 | ⚠️ 可新增 `internal/cache` 简单 LRU，无需引入 Redis |

### 4.2 是否需要引入 Redis

**结论：不需要。** 理由：

1. **配额缓存：** 配额 key 数量少（租户数 × 桶数 × 前缀数），内存 LRU 足够
2. **乐观锁：** 数据库行级原子操作足够，不需要分布式锁（单进程/单 DB）
3. **冲突检测：** 滑动窗口可内存维护
4. **违反 I6：** 引入 Redis 需要论证必要性。当前场景均不充分

```go
// 足够轻量的缓存方案——在 internal/cache/lru.go 中
type Cache[K comparable, V any] struct {
    maxEntries int
    mu         sync.Mutex
    entries    map[K]*entry[K, V]
    list       *list.List
}
// 无需外部依赖，标准库 + 泛型即可实现
```

### 4.3 自建 vs 采购/依赖决策

| 需求 | 自建 | 使用第三方库 | 决策 |
|------|------|------------|------|
| **乐观锁** | 单 SQL 语句 + Go 逻辑 | 无合适库 | ✅ 自建 |
| **分段提取与高亮** | 30-50 行 Go 函数 | `bleve` 内置高亮但引入全文检索引擎 | ✅ 自建（轻量，避免依赖膨胀） |
| **延迟滑动窗口** | 环形缓冲区 + goroutine | `go-metrics` / `hystrix-go` | ✅ 自建（< 100 行，避免重型依赖） |
| **分层配额缓存** | 泛型 LRU | 无贴合需求的轻量库 | ✅ 自建（< 80 行） |

**整体基调：** 5 个方向均不需要引入新的第三方依赖。这验证了项目 `I6`（Stdlib 优先）原则的正确性——当前架构的扩展点均在 Go 标准库的能力范围内。

---

## 5. 实施路线图

### 5.1 优先级排序

```
P0 ─────────────────────────────────────────────────
  多协议写入乐观锁（方向 A）         │ 风险：数据不一致
                                    │ 时间：2-3 天
P1 ─────────────────────────────────────────────────
  延迟感知熔断器（方向 D）           │ 风险：降级决策延迟
  协议级遥测（方向 C）               │ 风险：调试成本
                                    │ 时间：各 1-2 天
P2 ─────────────────────────────────────────────────
  S3 x-amz-tagging（方向 2）        │ 风险：少（兼容性）
  搜索结果高亮（方向 1）             │ 风险：少（UX）
  分层配额（方向 3）                │ 风险：少（新功能）
                                    │ 时间：各 1-2 天
```

**排序逻辑：** P0 选方向 A 是因为数据一致性是存储系统的核心契约，**出错不可接受**。P1 选方向 D + C 是因为熔断器当前对"慢降级"完全无感——这在生产环境中比"完全断开"更致命。P2 选方向 2/1/3 按实现成本从低到高。

### 5.2 阶段划分

```
Phase 1 ── "一致性基座"（Week 1）
├── Direction A: 乐观锁
│   ├── repository: UpdateObjectConditional 方法
│   ├── service: PutOptions.ExpectedVersion
│   ├── handler: REST/S3/WebDAV/MCP 传递 version
│   └── concurrency_test: 并发写入验证
└── Direction C: 协议遥测（轻量，作为事件 payload 字段）
    └── PutOptions.SourceProtocol + events.Payload.Protocol

Phase 2 ── "可观测性增强"（Week 2）
├── Direction D: 延迟感知熔断器
│   ├── sliding window 延迟统计
│   ├── CircuitBreakerConfig.LatencyThreshold
│   ├── readyzHandler 纳入熔断器状态
│   └── integration_test: 慢后端模拟
└── Direction 2: S3 x-amz-tagging
    └── s3compat/handler.go: parse tags header

Phase 3 ── "产品体验"（Week 3-4）
├── Direction 1: 搜索高亮
│   ├── Chunk.ObjectOffset（indexer 层）
│   ├── extractSnippet + 高亮标记
│   └── Hit.Snippet / Hit.Highlights
├── Direction B: 分层配额
│   ├── QuotaResource 表 + migration
│   ├── internal/quota Manager
│   ├── preflightQuota 链式查询
│   └── quota_admin API: PUT/DELETE 配额规则
└── 集成测试 + 文档更新
```

### 5.3 风险点与缓解策略

| 风险 | 影响 | 概率 | 缓解 |
|------|------|------|------|
| **乐观锁引入 409 冲突过多** | 用户体验下降 | 中 | 重试策略：客户端收到 409 后指数退避重试；服务端不做自动重试 |
| **SQLite 并发写入锁争用** | 写入吞吐量下降 | 低 | SQLite WAL 模式 + `busy_timeout` 配置；Postgres 用户不受影响 |
| **延迟熔断器误触发** | 服务错误降级 | 中-低 | 默认延迟阈值设高（P99 > 5s）；可配置；误触发后自动恢复（半开探测） |
| **分层配额用量计数滞后** | 配额短暂超额 | 低 | 非安全临界（存储层仍会限制写入）；计数通过定时 reconcile 校正 |
| **Chunk.ObjectOffset 对存量索引不兼容** | 搜索结果无偏移信息 | 高 | 降级策略：offset = 0 时从 chunk 起始位置提取片段，而非从偏移位置 |

**最重要的缓解措施——P0 的"逐步启用"策略：**

```go
// 乐观锁默认关闭，通过配置激活
type Config struct {
    // ...
    OptimisticLocking bool // 默认 false → 无条件覆盖（旧行为）
}
```

这样 Phase 1 的发布不会破坏现有部署。已有部署可以按需开启，并在开启前确保客户端具备重试逻辑。

---

## 总结

| 维度 | 评分 | 关键结论 |
|------|------|---------|
| **架构健康度** | ⭐⭐⭐⭐ | 三层架构清晰，扩展点设计合理 |
| **主要债务** | 🔴 P0 | 多协议写入无并发保护——这是唯一需要立刻解决的架构问题 |
| **次要债务** | 🟡 P1 | 熔断器无延迟感知；协议级可观测性不足 |
| **功能缺口** | 🟢 P2-P3 | S3 兼容性、搜索 UX、分层配额——均为增量功能，无架构影响 |
| **依赖风险** | 🟢 低 | 5 个方向均无需引入新的第三方依赖 |
| **可回退性** | 🟢 高 | 所有新功能均可通过配置或零值默认关 |

**一句话对产品负责人说：** 系统架构质量良好，扩展设计合理。**请优先分配 2-3 天解决方向 A（乐观锁）**——这是唯一可能在数据层面导致问题的缺口。其余 4 个方向可在 3 周内按优先级逐步完成，期间对现有用户无任何影响。
