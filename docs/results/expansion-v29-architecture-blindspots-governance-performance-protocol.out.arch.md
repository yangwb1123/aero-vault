现在我对整个代码库有了深入的理解。让我基于此给出架构分析。

---

# 架构分析报告：aero-vault 5 方向扩展评估

## 1. 架构评估

### 1.1 当前架构优势

aero-vault 的架构设计展示出成熟的分层模式，优势突出：

| 维度 | 当前状态 | 评价 |
|------|----------|------|
| **层间解耦** | `Protocol Adapters → FileService → Storage/Repository` | 严格的单向依赖，Handler 不绕过 FileService 直连 Storage，符合分层隔离原则 |
| **依赖反转** | `FileService` 依赖 `storage.Storage` / `repository.Repository` 接口 | 允许轻松切换后端实现（local↔s3↔oss↔cos），且通过 contract_test.go 进行契约测试 |
| **事件驱动** | Bus + 订阅者模式，事件首先持久化再播送 | 事件不丢失，订阅者压降不影响写入路径 |
| **Opt-in 安全默认** | AI、pgvector、Qdrant、复制、WebDAV 均 flag-gated，默认 off | 基线路径零网络零依赖，CI 门禁高效 |
| **协议平等** | REST / S3 / WebDAV / MCP 共享同一 `FileService` | 多协议无优先级倾斜，写入后立即可通过其他协议读取 |

### 1.2 关键架构瓶颈

经过深度代码审查，我发现以下结构性瓶颈：

**A. 缺少 Feature Flag 基础设施（技术债等级：高）**

当前所有 opt-in 功能通过 `getEnvBool` 在启动时静态确定。这意味着：
- 无法灰度发布：新功能必须全量上线或全量关闭
- 无法 %-based rollout：比如新索引器算法无法逐步放量
- 无法运行时动态切换：需要重启进程
- 代码中大量 `if cfg.AI.Enabled` 散落在各构建函数中，缺乏统一治理

**B. Rate Limiter 实现过于简化（技术债等级：中高）**

当前 `internal/middleware/ratelimit.go` 的 `RateLimiter`：
- 全局只有一个维度（tenant），没有 per-endpoint、per-user、per-IP
- 使用 `sync.Mutex` 保护全 map — 在高并发下（1000+ RPS）会成为全局锁竞争热点
- 没有分布式一致性 — 多实例部署时每个实例独立计数，实际限流效果 = RPS × N
- 没有分级拒绝 — 429 是唯一的响应，无法降级（如返回过期缓存）而非拒绝

**C. 事件表无生命周期管理（技术债等级：高）**

`repository.InsertEvent` 将事件持久化到 `events` 表，但：
- 没有自动清理机制（无 TTL、无分区、无归档）
- SQLite 下单表无限增长，最终导致写性能退化（B-tree 膨胀）
- Postgres 下没有做时间范围分区
- 虽然 `webhook_failures` 表有重试，但 `events` 表本身缺乏 retention policy

根据代码中的 Event 结构（`internal/repository/repository.go`），`Event` 包含 `TenantID, Bucket, Key, Type, ObjectID, RequestID, Payload` 等字段。随着时间推移（特别是高吞吐场景），这张表会成为显著 ops 负担。

**D. FileService 存在隐式耦合增长**

`FileService` 的 `Put` 方法（`internal/service/file.go`）承担了：quota 检查 → storage.Put → repository 写入 → 版本控制 → 事件发布 → ChunkCleaner 同步钩子。这条链正在变长，但没有做调用链分离或装饰器模式。若继续叠加功能（如请求合并、同步校验、备份触发器），单方法圈复杂度会逼近 AGENTS.md 的 10 阈值。

### 1.3 关键设计决策评估

| 决策 | 评估 | 建议 |
|------|------|------|
| 存储 key = `path.Join(tenant, bucket, key)` | ✅ 正确。prefix 隔离简洁有效 | 坚持，但需加强 GC 的 key 校验不误删跨 tenant 数据 |
| Middleware 链固定不可变 | ✅ 正确。handler 不自挂链保证了测试独立性 | 保持现状 |
| 事件先持久化再广播 | ✅ 正确。保证订阅者重启后不丢失事件 | 保持现状 |
| RateLimiter 使用 `sync.Mutex` | ❌ 性能瓶颈。高并发下锁争用 | 需改为分段锁或 atomic token bucket |
| SQL 占位符按位置编号 | ⚠️ 正确但脆弱。`s.rebind` 实现可靠，但约束了编写自由 | 保持，考虑引入 named parameter 适配层 |
| 索引器跳过仅记录 metric | ✅ 非致命设计符合降级原则 | 但缺少跳过原因的告警阈值配置 |

---

## 2. 扩展方向分析

基于当前架构的瓶颈和文档分析的 5 个方向，我重新评估优先级并补充两个高价值方向：

### 方向 A（P0）：Feature Flag 基础设施

**为什么需要：**
- 这是其他所有安全扩展的**前提条件**。没有 Feature Flag，无法灰度发布粒度限流、MCP 新功能、请求合并等变更
- 约 30% 的启动配置选项 (`getEnvBool`) 本质上是功能开关，应集中管理

**核心挑战：**
1. **Go 静态语言特性**：没有动态类加载，flag 变更后需要进程内重新初始化组件
2. **状态迁移**：flag 从 off→on 时，需要重建（或补充）已存在数据的状态（如新索引器开启后需 reindex 已有对象）
3. **一致性问题**：多实例部署时，flag 值需要在一定延迟内收敛

**预期的架构变更：**
- 新增 `internal/feature` 包，定义 `Flag` 类型和 `FlagRegistry`
- Flag 支持静态配置（环境变量/配置文件）和动态切换（运行时 API）
- %-based 和 tenant-based 路由支持
- 为已存在的 `getEnvBool` 开关建立迁移路径

**选项 A1 — 轻量级内存 registry：**
- 启动时从 env/config 加载，提供 HTTP endpoint 动态更新
- 缺点：重启丢失，多实例需逐台操作

**选项 A2 — repo-backed registry：**
- 存入 `feature_flags` 表，支持 `POST /v1/admin/feature-flags` 管理
- 实例通过轮询或 pub/sub 通知更新
- 优点：持久化、集群一致
- 建议：采用 A2 作为主要方案，A1 作为启动 fallback

### 方向 B（P0）：粒度限流系统

**为什么需要：**
- 当前单 tenant 全局 token bucket 无法满足多租户生产级需求
- 缺少 per-endpoint、per-user、per-IP 分级限流
- 分布式部署时限流失效（每实例独立计数）

**核心挑战：**
1. **分布式一致性 vs 性能**：集中式（Redis）限流器会引入网络延迟和可用性依赖
2. **多维限流组合**：tenant + endpoint + user 的组合策略需要固定评估顺序
3. **成本感知限流**：AI endpoints 调用涉及外部 LLM 费用，限流策略需区分请求类型

**预期的架构变更：**
- 将 `middleware.RateLimiter` 重构为插件化架构
- 引入 `RateLimitKey` 提取器接口（从 request context 提取维度）
- 支持 `local`（内存 token bucket）和 `redis`（滑动窗口）两种后端
- 分层限流：global → tenant → endpoint → user
- 新增限流响应头标准：`X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`

**选项 B1 — 内存多 bucket：**
- 保留当前 token bucket 基础，改为分段锁（striped lock）减少竞争
- 支持多维 key 组合
- 简单但无分布式能力

**选项 B2 — Redis 滑动窗口：**
- 支持跨实例限流一致性
- 引入 Redis 依赖
- 建议：B1 为默认（opt-in），B2 为可选。不强制所有部署引入 Redis

### 方向 C（P1）：分层缓存与请求合并

**为什么需要：**
- 缓存加密边界（ciphertext vs plainttext）是影响整个缓存架构的根本设计决策
- 请求合并（request coalescing）可以显著降低热点对象的并发读放效应
- 当前没有缓存层，所有 GET 直接落到 storage backend

**核心挑战：**
1. **加密边界决定缓存位置**：若缓存 ciphertext，则缓存层不需要加密密钥，但每请求需解密；若缓存 plainttext，则缓存层需信任或携带密钥
2. **合并粒度**：按 ETag 合并 vs 按 key+range 合并
3. **缓存失效**：storage backend 外部更新的场景（如 S3 多端写同一 bucket）

**预期的架构变更：**
- 新增 `internal/cache` 包，定义 `Cache` 接口（Get/Set/Delete）
- 在 `FileService` 和 `storage.Storage` 之间插入缓存层 — *不是* 在 protocol adapter 层
- 缓存支持 `local`（memory LRU）和 `redis` 两种后端
- 请求合并仅对幂等 GET/HEAD 请求生效

**ADR 建议：** 加密边界应作为独立 ADR 文档，在实现前明确决策。我建议：**缓存 ciphertext** — 因为缓存层不应持有密钥，且 ciphertext 缓存不会引入额外的信任域。

### 方向 D（P1）：事件生命周期管理

**为什么需要：**
- 文档中已明确指出 `events` 表的增长从未被分析文档覆盖 — 这是唯一有时间维度的方向
- 在高吞吐生产环境中，`events` 表可能以每月数百万行增长
- 当前只增不删，最终导致写性能退化、存储成本上升

**核心挑战：**
1. **SQLite 下无分区能力**：单表无法时间分区，只能通过 `DELETE WHERE created_at < ?` 清理，这会导致 WAL 文件膨胀和 VACUUM 需求
2. **业务依赖事件历史**：某些消费者（如 replication、审计日志）依赖事件时间线，清理策略需与其协调
3. **Postgres 分区迁移**：对已有大表做分区需要零停机迁移策略

**预期的架构变更：**
- 定义 `EventRetentionPolicy` — 按事件类型、租户配置不同保留期
- SQLite 场景：实现 FIFO 语义（保留最近 N 行），通过 `DELETE ... LIMIT` 渐进清理
- Postgres 场景：实现时间范围分区（`PARTITION BY RANGE (created_at)`），自动创建新分区
- 清理任务作为 `Reconcile` 的一部分，或独立 `EventRetention` worker

### 方向 E（P2）：MCP 深度演进与资源模板化

**为什么需要：**
- 当前 MCP 实现使用 `tools/list` 提供工具调用。对 100K+ 对象存储，`resources/list` 不可用
- `resources/templates/list` 是 MCP 协议标准化的资源模板机制，适合对象存储场景
- 开发者体验提升：AI IDE 集成时能直接通过 MCP 浏览和搜索文件

**核心挑战：**
1. **URI 模板设计与扩展**：`aero-vault://{tenant}/{bucket}/{key}` 模板化基础上，还需支持分页、搜索、版本等参数
2. **与 REST API 一致性**：MCP 不应暴露 REST 中不可用的功能，反之亦然
3. **鉴权模型**：MCP stdio 模式下鉴权跳过的设计需要重新审视

**建议：** 这个方向虽然价值高，但不是 P0 — 因为当前 MCP 的 `tools/list` 已满足基础集成需求，且用户群尚小。建议放到 P2 作为差异化竞争力。

### 方向 F（P1，新补充）：启动时的优雅降级与依赖健康检查

**为什么需要：**
- 当前启动流程是严格线性的：`storage → repo → service → workers → middleware → router`
- 若 storage 后端不可用（如 S3 bucket 临时故障），整个进程启动失败
- 生产环境中希望核心 API 在依赖退化时仍能提供有限服务（如只读模式）

**核心挑战：**
1. 定义"核心可用"的边界：metadata-only 操作（list、search）vs 数据操作（GET/PUT）
2. 健康检查分层：`/healthz`（存活）vs `/readyz`（就绪）vs `/livez`（依赖状态）
3. 从部分退化状态恢复到完全服务的自动化流程

---

## 3. 接口设计建议

### 3.1 Feature Flag 接口

```go
// internal/feature/flag.go

// FlagState 定义单个开关的状态
type FlagState int
const (
    FlagOff FlagState = iota
    FlagOn
    FlagPercentage  // 按百分比灰度
    FlagByTenant    // 按租户列表
)

// Flag 是不可变的运行时视图
type Flag struct {
    Name    string
    Enabled bool        // 当前上下文是否启用
}

// Registry 是 flag 的集中管理入口
type Registry interface {
    // IsEnabled 检查给定 flag 在当前请求上下文中是否启用
    IsEnabled(ctx context.Context, name string) bool
    
    // Register 注册一个 flag 及其配置来源
    Register(feature Feature, source Source)
    
    // List 返回所有已知 flag 的状态（用于管理 API）
    List(ctx context.Context) []FlagInfo
}
```

**设计原则：**
- `Flag` 是值对象，不可变
- `IsEnabled` 接收 `context.Context` 以便提取 tenant/user/requestID 做灰度决策
- 配置来源（`Source`）可以是环境变量、配置文件、DB 行、HTTP API
- 代码中使用 `if feature.IsEnabled(ctx, "new_indexer")` 而非 `if cfg.AI.NewIndexerEnabled`

### 3.2 粒度限流接口

```go
// internal/middleware/ratelimit.go (rewritten)

// KeyExtractor 从请求中提取限流维度
type KeyExtractor interface {
    Extract(r *http.Request) string
}

// Limiter 是限流器的核心接口
type Limiter interface {
    // Allow 返回是否允许请求通过以及等待时间
    Allow(ctx context.Context, key string) (ok bool, wait time.Duration, remaining int)
}

// RateLimitRule 定义一条限流规则
type RateLimitRule struct {
    Name      string
    Priority  int              // 评估优先级（数字小优先）
    Extractor KeyExtractor     // 从哪提取 key
    Limiter   Limiter          // 使用什么限流算法
    Action    RateLimitAction  // 超限时的动作：Reject | Degrade | Throttle
}
```

**向后兼容策略：**
- 当仅设置 `RATE_LIMIT_RPS`（旧配置）时，自动创建一条兼容规则：`tenant → token-bucket`
- 当设置 `RATE_LIMIT_RULES`（新配置）时，覆盖默认规则
- `RateLimiter.Middleware()` 行为保持不变

### 3.3 缓存层接口

```go
// internal/cache/cache.go

type Cache interface {
    Get(ctx context.Context, key string) (value []byte, hit bool, err error)
    Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
    Delete(ctx context.Context, key string) error
    
    // Merge 合并多个并发 Set（请求合并场景）
    Merge(ctx context.Context, key string, fn func() ([]byte, error), ttl time.Duration) ([]byte, error)
}
```

**引入位置：** 缓存层插入在 `FileService` 和 `storage.Storage` 之间 — 不是作为 middleware，也不是在 handler 层。

```
FileService.Get → cache.Get(miss) → storage.Get → cache.Set → response
```

理由：
- 所有协议共享同一缓存（保障协议一致性）
- 版本控制、条件请求的缓存键可以集中在 service 层计算
- 避免 middleware 层多次加解密

### 3.4 抽象层建议

| 需要新增 | 说明 |
|----------|------|
| `internal/feature` | Feature Flag 注册表 |
| `internal/cache` | 缓存接口 + local/redis 实现 |
| `internal/limiter` | 限流算法实现（token-bucket、滑动窗口、GCRA） |
| 不需要新增 | `utils/` — 按 AGENTS.md 禁止 |
| 不需要新增 | `common/` — 按 AGENTS.md 禁止 |

---

## 4. 技术选型

### 4.1 需要引入的技术栈

| 组件 | 推荐 | 备选 | 评估 |
|------|------|------|------|
| **Feature Flag** | 自建轻量级（`internal/feature`） | LaunchDarkly SDK / Unleash | 自建：简单可控，无外部依赖；LD：企业级但需网络+费用。建议自建 P0 |
| **缓存** | 自建 memory LRU (`hashicorp/golang-lru`) | Redis / 自建 `sync.Map` | memory 实现零网络延迟；可选 Redis 用于多实例共享。`golang-lru` 稳定成熟 |
| **分布式限流存储** | Redis + Redis Sliding Window | 自建 Raft 集群 | Redis 是事实标准，生态成熟；Go Redis 客户端质量高 |
| **Postgres 事件分区** | 原生 pg_partman 或手动 `CREATE PARTITION` | TimescaleDB hypertable | 不是新依赖 — 已有 Postgres 支持。只需 migration 脚本 |
| **指标细化** | 现有 OTel + Prometheus 体系 | — | 不需要新依赖。只需增加更多 instrument |

### 4.2 第三方依赖评估标准

| 标准 | 门槛 | 说明 |
|------|------|------|
| 活跃维护 | ✅ 最近 6 个月有提交 | 安全修复及时 |
| Go 模块支持 | ✅ 必须是 `go mod` 可拉取 | 不使用 glide/dep |
| 许可证兼容 | ✅ MIT / Apache 2.0 / BSD | GPL/LGPL 需法务审核 |
| 大小 | ≤ 50K 行代码（含 vendor） | 过大依赖增加构建时间和 CVE 面 |
| 零 CGO | ✅ 必须 | 不可要求 C 编译器 |
| 社区 | GitHub Stars ≥ 500 | 越低越需自行评估替代方案 |

### 4.3 自建 vs 采购决策

| 方向 | 推荐 | 理由 |
|------|------|------|
| Feature Flag | **自建** | 项目范围有限（一个 Go binary），不需要多语言 SDK、不需要 A/B 测试平台。自建 < 500 行 |
| 分布式限流 | **自建** | 限流逻辑已存在，只需添加 Redis 后端，不需采购商业 API 网关 |
| 缓存层 | **自建** | 接口定义清晰（Get/Set/Delete/Merge），实现简单 |
| 事件生命周期 | **自建** | 本质是 SQL 管理，不需外部服务 |
| API 文档 | **现有** | 已用 OpenAPI + Swagger UI，不需要商业方案 |

---

## 5. 实施路线图

### 5.1 优先级矩阵

```
                 高业务价值
                    │
                    │
        方向 C     │     方向 A
        (缓存)     │   (Feature Flag)
                    │
    ────────────────┼──────────────── 低实施成本
                    │
        方向 E     │     方向 B+D+F
        (MCP演进)  │   (限流+事件+健康)
                    │
                    │
                    ▼
                 低业务价值
```

- **P0** — 方向 A (Feature Flag) + 方向 B (粒度限流)
- **P1** — 方向 C (缓存) + 方向 D (事件生命周期) + 方向 F (依赖健康)
- **P2** — 方向 E (MCP 深度演进)

### 5.2 阶段划分

**Phase 1 — Foundation (2-3 sprints)**

| Sprint | 交付物 |
|--------|--------|
| 1 | `internal/feature` 包设计实现 + 迁移 `getEnvBool` 开关 | 
| 2 | `GetEnvBool` → `feature.IsEnabled` 全量替换 + `POST /v1/admin/feature-flags` 管理 API |
| 3 | RateLimiter 重构：分段锁 + `KeyExtractor` 接口 + 分层限流（global → tenant → endpoint） |

**Phase 2 — Resilience (2-3 sprints)**

| Sprint | 交付物 |
|--------|--------|
| 4 | `internal/cache` 接口 + local LRU 实现 + 在 FileService.Get 中插入缓存层 |
| 5 | 事件生命周期：SQLite FIFO 清理 + Postgres 时间分区 migration |
| 6 | 启动优雅降级：分层健康检查 + 退化模式自动化 |

**Phase 3 — Scale (2 sprints)**

| Sprint | 交付物 |
|--------|--------|
| 7 | 分布式限流：Redis 滑动窗口实现 + `EVALUATE` 脚本 |
| 8 | 请求合并：`Cache.Merge` 实现 + 热点检测启发式 |

**Phase 4 — Differentiation (2-3 sprints)**

| Sprint | 交付物 |
|--------|--------|
| 9 | MCP `resources/templates/list` 实现 + URI 模板设计 |
| 10 | ADR 文档更新 + OpenAPI 扩展 + SDK 更新 |

### 5.3 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| Feature Flag 动态切换导致组件状态不一致 | 中 | 高 | 所有 flag 变更仅对**新请求**生效，不重启已运行 goroutine；状态依赖组件（如 indexer）注册 lifecycle hook |
| RateLimiter 重构破坏现有行为 | 低 | 高 | 保留旧 `RateLimiter` 作为默认实现；新实现通过 Feature Flag 灰度 |
| 缓存加密边界决策影响整个缓存架构 | 中 | 高 | **要求**在 Phase 1 启动前完成 ADR 文档，明确选择 ciphertext 缓存方案 |
| 事件分区 migration 在已有大表上失败 | 低 | 中 | 对 >100M 行的 events 表，建议先做数据归档再分区，而非原地 ALTER |
| 依赖健康分层导致启动逻辑复杂化 | 中 | 中 | 退化状态下通过 `/healthz` 告知退化模式，而非静默降级 |
| MCP URI 模板与未来协议扩展冲突 | 低 | 中 | URI 模板预留 `aero-vault://{tenant}/{bucket}/{key}?{params}` 参数空间 |

### 5.4 跨方向依赖关系

```
Feature Flag ─────────────────────────────────┐
    │                                          │
    ├── 为粒度限流提供灰度能力                  │
    ├── 为缓存层提供% rollout                  │
    └── 为MCP新功能提供canary                  │
                                               ▼
粒度限流 ─────────────── 缓存层 ──────── 请求合并
                                               │
事件生命周期 ───── 无依赖，可独立推进            │
                                               │
MCP演进 ── 依赖 Feature Flag + 缓存就绪         │
                                               │
依赖健康检查 ── 独立，但建议在 Phase 1 后推进   │
```

---

## 总结

上述分析的五个方向中，**Feature Flag（方向 1）** 是架构底座，没有它其他方向无法安全上线。**粒度限流（方向 5）** 和 **事件生命周期（方向 4）** 是生产级多租户平台必须解决的现实问题。**请求合并和缓存加密边界（文档中提到的跨方向问题）** 需要在 Phase 1 启动前完成 ADR 文档，因为它直接影响整个缓存架构的设计。

我建议的执行顺序：

1. **立即启动**：Feature Flag 基础设施（P0）
2. **并行推进**：ADR 文档（缓存加密边界）← 在 Phase 1 完成
3. **Phase 1 中期**：粒度限流重构（P0）
4. **Phase 1 后期**：事件生命周期管理（P1）
5. **Phase 2**：缓存层 + 请求合并（P1）
6. **Phase 3**：MCP 深度演进（P2）

这个路线图确保了架构依赖性被正确处理，同时给予高业务价值的方向最高优先级。
