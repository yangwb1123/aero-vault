# AeroVault 高价值扩展方向分析 v39 — 系统盲区：资源泄漏、健康可见性与租户硬隔离

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 230+ `.go` 文件 + `sdk/*` 三套客户端 + `deploy/*` + `docs/*` + 48 对迁移文件 + `Makefile` + `HARNESS.md`）
> **分析日期：** 2026-07-10
> **视角：** 资深架构师 / 产品经理 — 聚焦此前 **38 期分析（累计 ~200+ 方向、29,000+ 行分析文本）从未实质性触及的系统盲区**——即那些不在功能矩阵中、却在生产运行时持续消耗资源或隐藏风险的"静默缺陷"。
> **去重方法：** 逐方向对 `docs/requirements/` 下全部 38 期既有分析 + `docs/ROADMAP.md`（含实现状态） + `docs/adr/DECISIONS.md` + `docs/CHANGELOG.md` + `docs/TODO.md` 进行完整检索验证。

---

## 背景：前 38 期覆盖了什么，遗漏了什么

前 38 期 expansion 文档覆盖了从 AI/RAG 管线（~30 方向）、S3 兼容协议（~22 方向）、存储后端（~24 方向）、认证授权（~24 方向）、多租户（~22 方向）到工程质量（~20 方向）等全方位的功能扩展建议。ROADMAP 的 10 个方向全部标记实现，TODO 清单全部完成。

**但是，所有 38 期分析存在一个共同的视角盲区：以"功能"而非"运行时行为"为分析单元。** 它们关心的是"缺什么功能"，而非"运行时系统如何退化/泄漏/失效"。本期 5 个方向全部指向后者——在生产环境中，这些缺口比缺少某个 S3 API 更致命。

---

## 本期 5 个方向的去重验证

| # | 方向 | `grep` 验证范围 | 既有覆盖判定 |
|---|------|----------------|------------|
| 1 | **SSE/Bus Subscriber 通道泄漏（Channel & Goroutine Leak）** | `grep -rli "subscriber.*remove\|subscriber.*unsubscrib\|subscriber.*cleanup\|subscription.*remove\|subscription.*leak\|subs.*remove\|subs.*clean\|subs.*nil.*not\|broadcast.*closed\|closed.*channel\|channel.*leak\|unsubscribe" docs/requirements/` → v6 有 1 项提及 subscriber life-cycle 但位于 SSE 章节，指向"SSE 需要有 Last-Event-ID"而非 subscriber 资源泄漏；v38 有 `Bus.Close` 作为 shutdown drain 的一部分讨论，但 **非运行时 subscriber 泄漏分析** | ❌ 零实质性分析 |
| 2 | **后台 Worker 健康管理与生命周期可见性** | `grep -rli "worker.*health\|worker.*monitor\|worker.*watchdog\|background.*health\|background.*monitor\|goroutine.*health\|goroutine.*watchdog\|healthz.*worker\|readyz.*worker\|subsystem.*health\|component.*health\|background.*liveness" docs/requirements/` → v14 覆盖 pprof 运行时诊断端点（pprof heap/goroutine 采样），但非后台 worker 健康状态暴露；v19 覆盖存储后端健康探测，但非业务 worker 健康 | ❌ 零实质性分析 |
| 3 | **多租户计算资源隔离（Noisy Neighbor 防护）** | `grep -rli "noisy.*neighbor\|compute.*isolat.*tenant\|memory.*isolat\|cpu.*isolat\|goroutine.*isolat\|tenant.*resource.*isolat\|tenant.*compute\|tenant.*memory\|tenant.*cpu\|soft.*isolation\|hard.*isolation" docs/requirements/` → v36 覆盖**存储 I/O QoS 与租户隔离**（Storage I/O QoS），但非计算/内存/goroutine 隔离 | ❌ 零实质性分析 |
| 4 | **可观测性数据访问控制与多租户隔离** | `grep -rli "metrics.*auth\|metrics.*access.*control\|metrics.*protect\|prometheus.*auth\|prometheus.*protect\|metrics.*unauth\|telemetry.*access.*control\|/metrics.*secur\|metrics.*leak.*tenant\|observability.*isolat\|telemetry.*isolat" docs/requirements/` → **0 命中** | ❌ 完全未覆盖 |
| 5 | **Job Queue 资源饥渴与租户级 backpressure 缺失** | `grep -rli "job.*queue.*tenant\|queue.*priority.*per\|per.*tenant.*queue\|queue.*resource.*exhaust\|queue.*starvation\|queue.*fairness\|queue.*noisy\|queue.*isolat\|job.*priority\|priority.*queue\|queue.*backpressure.*tenant\|queue.*cap.*per\|queue.*max.*per.*tenant" docs/requirements/` → v14 覆盖 `JOBS_MAX_DEPTH` 全局队列深度限制；v15 覆盖 CDC 事件队列；v17 覆盖作业延迟指标—**但均为全局视角**，无租户级队列隔离分析 | ❌ 零实质性分析 |

---

## 本期方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 |
|---|------|------|--------|---------|---------|
| 1 | **🔴 SSE/Bus Subscriber 通道泄漏** | 可靠性/性能 | **P1（严重）** — 每个 SSE 连接泄漏一个 channel + goroutine；总线广播 O(n) 遍历已断开订阅者 → 持续内存增长 + CPU 浪费 | `internal/events/bus.go:96-101`（`Subscribe` 无 `Unsubscribe`）；`internal/api/rest/sse.go:72-86`（`liveStream` 退出不清理订阅）；`cmd/server/main.go`（无 SSE 连接上限） |
| 2 | **🔴 后台 Worker 健康管理缺失** | 运维/可靠性 | **P1（严重）** — 15+ 后台 goroutine 零健康可见性；任一 worker 静默退出后系统继续响应 HTTP 但功能退化 | `cmd/server/main.go:581-654`（`buildIndexer`、`buildBackgroundWorkers` 启动 15+ goroutine）；`/healthz`、`/readyz` 仅返回 `"ok"`；`internal/ai/indexer.go:132`（`Run` 退出无信号） |
| 3 | **🟠 多租户计算资源隔离** | 架构/安全 | **P2（高）** — 一个恶意/繁忙租户可耗尽全局 goroutine 池、内存、DB 连接 → 其他租户服务降级 | `internal/service/file_crud.go`（无 per-tenant 并发限制）；`internal/middleware/middleware.go:143`（`ConcurrencyLimiter` 全局）；`internal/repository/sqlite.go:26`（`SetMaxOpenConns(1)` 共享）；`internal/jobs/jobs.go`（池共享） |
| 4 | **🟠 /metrics 端点未认证导致跨租户数据泄露** | 安全/合规 | **P2（高）** — 多租户部署中任何经过认证的用户可读取所有租户的存储用量、请求速率、AI 消耗 | `internal/auth/auth_middleware.go:38-41`（`isBypassPath` 包含 `/metrics`）；`internal/telemetry/metrics.go`（指标含 `tenant` label） |
| 5 | **🟡 Job Queue 租户级资源隔离缺失** | 架构/可靠性 | **P2（中）** — 全局 job queue 无 per-tenant 限额；一个大量上传的租户可阻塞其他租户的复制/索引/扫描作业 | `internal/jobs/jobs.go:86-90`（`WithMaxDepth` 全局）；`internal/jobs/jobs.go:140-150`（`Pool` 共享所有 workers） |

---

## 方向一：🔴 SSE/Bus Subscriber 通道泄漏（Subscriber Channel Leak）

### 现状

`internal/events/bus.go` 实现了一个 in-process 事件总线，其 `Subscribe()` 方法（line 96-101）创建一个带缓冲的 channel 并追加到 `b.subs []chan repository.Event` 切片中。**该切片从未移除已关闭或已断开的订阅者。**

`internal/api/rest/sse.go` 的 `liveStream`（line 72-86）在 SSE 客户端连接时调用 `h.bus.Subscribe()`，然后在 `select` 中等待 `r.Context().Done()` 或 channel 事件。当客户端断开连接时：

1. `r.Context().Done()` 触发，`liveStream` 函数返回
2. Subscriber channel **仍然留在 `b.subs` 中**（无 `Unsubscribe()` 方法）
3. `broadcast()`（line 114-123）每次有新事件时**仍然向该已断开的 channel 发送**——但 channel 未被关闭，select default 分支会丢弃发送（事件丢失到已断开 client）
4. **goroutine 泄漏？** 没有——SSE handler 的 goroutine 在 Context.Done 后退出。但是 channel 本身泄漏——它是 GC reachable 的（在 `b.subs` 中），所以不会被 GC 回收。只要 `Bus` 对象存活，leaked channel 就持久占用内存。

### 影响

| 指标 | 计算 | 示例 |
|------|------|------|
| 每个泄漏 channel 占用的内存 | `cap(chan repository.Event) × sizeof(Event) + chan 调度结构体` | 64 × 200 bytes + 192 bytes ≈ 13 KB |
| 每次 broadcast 的额外 CPU | O(n) 遍历泄漏的 subs | 100 个已断开的 SSE 客户端 → broadcast 慢 100×（遍历+chan send） |
| 总泄漏上限 | 无；所有历史 SSE 连接叠加 | 10,000 次连接 ≈ 130 MB 永久泄漏 + 10,000× broadcast 开销 |

### 代码锚点

```go
// bus.go:96 — Subscribe 追加但不提供移除机制
func (b *Bus) Subscribe() <-chan repository.Event {
    ch := make(chan repository.Event, b.subBuffer)
    b.mu.Lock()
    b.subs = append(b.subs, ch)  // ← 永远不删除
    b.mu.Unlock()
    return ch
}

// bus.go:114 — broadcast 遍历所有 subs，包括已断开的
func (b *Bus) broadcast(e repository.Event) {
    b.mu.RLock()
    defer b.mu.RUnlock()
    for _, ch := range b.subs {   // ← 遍历泄漏的 channel
        select {
        case ch <- e:
        default:
            b.dropped.Add(1)      // ← 事件被丢弃
        }
    }
}
```

### 缺失能力

1. **`Bus.Unsubscribe(ch)` 方法**——从 `b.subs` 中移除指定 channel 并 close 它
2. **`SSEHandler` 在客户端断开时调用 `Unsubscribe`**
3. **可配置的 SSE 连接上限**（`SSE_MAX_CONNECTIONS`，默认 100）
4. **`sse_connections_active` 仪表盘指标**——当前活跃 SSE 连接数
5. **自动清理死 subscriber 的 watchdog**——周期性检测 `b.subs` 中无法接收的 channel（非阻塞探测）

### 为什么现在需要

这是一个**静默的资源泄漏**。在 demo/开发环境中，几个 SSE 连接的重启不会暴露问题。但在生产环境中：
- Web UI（`/ui`）在搜索/detail/lineage/chat 四个页面均可能打开 SSE 流
- 自动化客户端（CI/CD、监控系统）可能频繁重连 SSE
- 经过数小时到数天的运行，泄漏的内存和 broadcast CPU 开销会累积到可观测的程度

修复成本极低（新增一个 `Unsubscribe` 方法 + 在 SSE handler 的 defer 中调用），但收益明确——消除一个在负载下必然出问题的内存泄漏点。

### 边界情况

| Edge Case | 风险 | 缓解措施 |
|-----------|------|---------|
| 并发 `Subscribe` + `Unsubscribe` | `b.subs` 切片操作竞态 | `b.mu.Lock()` 保护所有 subs 读写 |
| `Unsubscribe` 时 channel 已 close | double-close panic | 使用 `sync.Once` 或 atomic closed flag；或仅移除引用不 close |
| broadcast 期间 `Unsubscribe` | broadcast 遍历 subs 时切片被修改 | broadcast 加 RLock，Unsubscribe 加写 Lock |
| 重复 `Unsubscribe` | 第二次调用找不到 channel | 幂等：从切片移除时检查是否存在 |
| SSE 连接数无上限 | 恶意客户端可创建大量连接消耗内存 | `SSE_MAX_CONNECTIONS` 硬上限 + 超过时关闭最早连接 |

---

## 方向二：🔴 后台 Worker 健康管理与生命周期可见性

### 现状

当前 `main.go` 中直接以 `go func()` 启动了 **15+ 个后台 goroutine**：

| Goroutine | 文件 | 行号 | 退出机制 | 重启机制 | 健康信号 |
|-----------|------|------|---------|---------|---------|
| Indexer.Run | `internal/ai/indexer.go` | 132 | ctx.Done | ❌ 无 | ❌ 无 |
| Reconcile.Run | `internal/reconcile/job.go` | 78 | ctx.Done | ❌ 无 | ❌ 无 |
| Lifecycle.Run | `internal/reconcile/lifecycle.go` | 44 | ctx.Done | ❌ 无 | ❌ 无 |
| Retention.Run | `internal/reconcile/retention.go` | 56 | ctx.Done | ❌ 无 | ❌ 无 |
| Webhook.Run | `internal/events/webhook.go` | 81 | ctx.Done | ❌ 无 | ❌ 无 |
| Webhook.RetryLoop | `internal/events/webhook.go` | 169 | ctx.Done | ❌ 无 | ❌ 无 |
| Replication.Run | `internal/replication/replication.go` | 71 | ctx.Done | ❌ 无 | ❌ 无 |
| Antivirus.Run | `internal/antivirus/worker.go` | 72 | ctx.Done | ❌ 无 | ❌ 无 |
| SSE Rewrap | `cmd/server/main.go` | 292 | ctx.Done | ❌ 无 | ❌ 无 |
| BM25 Warmup | `cmd/server/main.go` | 581 | ctx.Done | ❌ 无 | ❌ 无 |
| Postgres Transport | `cmd/server/main.go` | 312 | ctx.Done | ❌ 无 | ❌ 无 |
| Key Invalidation | `cmd/server/main.go` | 654 | ctx.Done | ❌ 无 | ❌ 无 |
| Job Pool (N workers) | `internal/jobs/jobs.go` | 140 | ctx.Done | ❌ 无 | ❌ 无 |

这些 goroutine 的**共同特征**：
- 所有 goroutine 依赖 ctx.Done 来退出——但 ctx 是共享的 signal.NotifyContext，一旦 SIGTERM 所有 worker 同时收到 cancel
- **没有任何 goroutine 有重启/重试逻辑**——如果 indexer 因 panic 或未处理的 error 退出，索引功能永久失效，但 HTTP API 仍然响应（系统处于"僵尸"状态）
- **没有任何 goroutine 向 healthz/readyz 提供信号**——运营商无法区分"索引器正在处理"和"索引器已死亡"
- 除了 Job Pool 的 `execute()` 有 `recover()` 外，其他 goroutine **可能在 panic 时直接崩溃进程**（Go 默认行为）

### 影响

| 场景 | 后果 | 可观测性 |
|------|------|---------|
| Indexer panic 后永久退出 | 新上传文件永远不被索引→搜索无结果 | ❌ `/healthz` 返回 200，搜索静默返回空 |
| Webhook RetryLoop 因 OOM kill | Webhook 重试永久停止→外部系统收不到事件 | ❌ 无告警，webhook_failures 表持续增长 |
| Reconcile 因未处理 error 退出 | 孤儿 blob 积累→存储空间泄漏 | ❌ 无告警，直到磁盘满 |
| Antivirus worker 退出 | 恶意文件不再被扫描 | ❌ 无告警 |
| BM25 warmup 因 panic 退出 | 混合搜索退化为纯向量搜索（但无日志提示用户） | ❌ 仅一次 warn log |

### 缺失能力

1. **`/readyz` 端点集成**——每个后台 worker 向一个共享的 readiness registry 注册自己的健康状态：
   - Indexer: "上次成功处理事件的时间 < `INDEXER_HEALTHY_THRESHOLD_SECONDS`"
   - Reconcile: "上次完成 sweep 的时间 < `RECONCILE_INTERVAL × 3`"
   - Webhook: "重试队列深度 < 阈值"
   - Job Pool: "pending 作业数 < 阈值"
   - `/readyz` 根据所有注册 worker 的状态决定 200/503

2. **Supervisor goroutine**——每个关键 worker 在一个带有 `recover()` 的 supervisor goroutine 中启动。当子 goroutine 因 panic 退出时，supervisor 记录告警并重启（带指数退避，最多 N 次/分钟防止 crashloop）

3. **Worker 存活指标**——每个 worker 暴露：
   - `worker_up{name="indexer"}` (1=正常, 0=已退出)
   - `worker_restarts_total{name="indexer"}`
   - `worker_last_success_timestamp_seconds{name="indexer"}`
   - `worker_loop_duration_ms{name="indexer"}`

4. **Graceful degradation 策略**——当一个 worker 死亡时：
   - Indexer 死亡 → 标记系统为 DEGRADED，在 `/readyz` 返回 503，在 `/healthz` 返回 "indexer: dead"
   - Reconcile 死亡 → 不阻断 HTTP，但触发告警
   - Webhook 死亡 → 不阻断 HTTP，但标记事件处理为 DEGRADED

### 为什么现在需要

在 Kubernetes 环境中，**Pod 重启是常态**，但**部分功能退化不是通过重启就能自动修复的**。一个由 10 个微服务组成的系统有 10 个健康端点——但 AeroVault 是一个单体，其内部有 15+ 个独立的 worker，它们各自可能独立死亡。没有 worker 级别的健康可见性，运营团队只能通过"用户投诉搜索不工作"来发现索引器已死。在单体服务中构建内部健康基础设施，是合理的架构投资。

### 架构概要

```
┌─────────────────────────────────────────────────────┐
│                 /readyz handler                      │
│                                                      │
│  register("indexer")  →  lastSuccess > threshold?    │
│  register("reconcile") →  lastSuccess > threshold?   │
│  register("webhook")   →  retryDepth < threshold?    │
│  register("jobpool")   →  pendingJobs < threshold?   │
│                                                      │
│  All green → 200 OK                                  │
│  Any critical red → 503 Service Unavailable          │
│  Any non-critical red → 200 + Warning header         │
└─────────────────────────────────────────────────────┘

每个 worker 启动时：
  health.Register("indexer", &WorkerHealth{
      LastSuccess: time.Now(),
      Alive:       true,
  })
  defer func() { health.SetDead("indexer") }()

Supervisor 模式：
  go supervisor("indexer", func(ctx) error {
      return indexer.Run(ctx)  // 正常退出返回 nil
  })
  // supervisor 在 panic 或 error 时重启（带退避）
```

### 边界情况

| Edge Case | 风险 | 缓解措施 |
|-----------|------|---------|
| Worker 在启动阶段尚未就绪 | `/readyz` 过早报告异常 | 注册时容忍初始 grace period（`READY_INITIAL_GRACE=30s`） |
| Worker 在 shutdown 阶段正常退出 | `ctx.Done` 触发，worker "死亡"被标记 | shutdown 期间不检查该 worker 的健康状态 |
| Supervisor 在 crashloop 中 | 日志淹没、API 调用频率过高 | 退避策略：1s→2s→4s→8s→max 60s，重置于成功运行 > 5min |
| Worker 健康信号在 GC pause 时延迟 | 假阳性 unhealthy | P50 延迟 vs P99 延迟；允许多次连续失败再标记 dead |

---

## 方向三：🟠 多租户计算资源隔离（Noisy Neighbor 防护）

### 现状

当前多租户的隔离机制完全集中在**数据层面**（每行带有 `tenant_id` 列）和**请求速率限制**（`RATE_LIMIT_RPS` + `AI_RATE_LIMIT_RPS` 的 per-tenant token bucket）。在**计算资源**层面，所有租户共享：

| 资源 | 当前隔离 | 问题 |
|------|---------|------|
| HTTP handler goroutines | `ConcurrencyLimiter` 全局 | 一个租户的高并发上传可耗尽全局槽位 |
| 后台 job workers | `Pool.workers` 全局分配 | 租户 A 的 10 万条索引作业挤占租户 B 的复制作业 |
| DB 连接池 | SQLite: `MaxOpenConns(1)` 全局；Postgres: 无任何限制 | - |
| 嵌入/LLM API 调用 | `AI_RATE_LIMIT_RPS` per-tenant | 仅限速率，不限并发（burst 可叠加） |
| 内存 | 无限制 | 大文件并发 PUT 可耗尽内存 |
| 磁盘 IO | 无限制 | 一个租户的大量读取可影响磁盘响应 |

### 影响

| 场景 | 后果 | 现有防护 |
|------|------|---------|
| 租户 A 同时上传 100 个 1GB 文件 | 全局 goroutine 池被占满 → 租户 B 的所有请求 429 | ❌ ConcurrencyLimiter 是全局的，不区分租户 |
| 租户 A 上传百万个小文件 | Job queue 被索引作业填满 → 租户 B 的复制/扫描作业被延迟 | ❌ `JOBS_MAX_DEPTH` 是全局上限 |
| 租户 A 发起 50 个并行搜索请求 | 50 个嵌入 API 调用同时发出 → 速率限制器 burst 通过，LLM API 可能限流 → 所有租户的搜索延迟增加 | ❌ RateLimiter 仅限 per-tenant RPS，不限并发 |
| 租户 A 存储了大量文本文件 | 索引器为租户 A 索引时消耗大量内存 → 其他租户的索引延迟 | ❌ Indexer 按事件顺序处理，不区分租户 |

### 缺失能力

1. **Per-tenant ConcurrencyLimiter**——在全局 `ConcurrencyLimiter` 之上叠加 per-tenant 权重限制。租户 A 最多同时使用 N 个 handler goroutine，超过时该租户的请求排入 per-tenant 队列或返回 429。

2. **Per-tenant Job Queue Cap**——`Queue.MaxDepth` 改为 `Queue.MaxDepthPerTenant`（每个租户最多 N 个 pending job），防止单租户填满全局队列。

3. **Per-tenant DB Connection Pool**——Postgres 模式下为每个活跃租户创建独立的连接池（或使用 `SET session` 隔离），防止一个租户的慢查询阻塞其他租户的查询。

4. **Per-tenant Memory Budget**——文件 PUT 路径检查租户当前内存使用（通过 `runtime.ReadMemStats` 或维护一个 per-tenant 累计计数），超过阈值时拒绝新的上传直到内存释放。

5. **Indexer Tenant Fairness**——Indexer 在处理事件时按租户轮转（round-robin 或 weighted fair queuing），而非纯 FIFO。确保大型租户不会饿死小型租户的索引。

### 为什么现在需要

存储服务通常具有高度不均匀的租户负载分布（Zipf 分布，少数大租户产生大部分流量）。在没有计算资源隔离的情况下，**一个"吵闹的邻居"租户可以使整个集群不可用**。这不是理论风险——在 S3-compatible 存储的 SaaS 部署中，这是最常见的生产事故原因之一。

Per-tenant 速率限制是好的第一步，但在高并发场景下**速率限制只能控制 RPS，不能控制并发度**。100 个请求以均匀速率到达仍然会同时占用 100 个 goroutine。AeroVault 的目标用户（企业存储）恰好是负载分布最不均匀的场景。

### 架构概要

```
全局资源                 Per-Tenant 隔离
┌──────────┐            ┌──────────────────────┐
│DB 连接池  │            │ 租户 A: max 5 conns  │
│          │  ───→       │ 租户 B: max 5 conns  │
│max 100   │            │ 租户 C: max 20 conns │
└──────────┘            └──────────────────────┘

┌──────────┐            ┌──────────────────────┐
│Job Queue │            │ 租户 A: max 500 jobs  │
│          │  ───→      │ 租户 B: max 500 jobs  │
│max 10000 │            │ 全局: max 10000 jobs  │
└──────────┘            └──────────────────────┘

┌──────────┐            ┌──────────────────────┐
│Handler   │            │ 租户 A: max 20 inflight│
│Goroutines│  ───→      │ 租户 B: max 20 inflight│
│max 200   │            │ 全局: max 200 inflight │
└──────────┘            └──────────────────────┘
```

### 边界情况

| Edge Case | 风险 | 缓解措施 |
|-----------|------|---------|
| Per-tenant 上限设得太低 | 合法用户被限流 | 可配置 + 告警（`tenant_concurrency_throttled_total`） |
| 租户数量太多导致 per-tenant 跟踪内存开销大 | 1000 个租户 × 每个租户 3 个计数器 ≈ 微小 | 使用 sync.Map + 自动清理不活跃租户 |
| Per-tenant job cap 导致死锁 | 租户 A 的 job 想 enqueue 新 job 但已到上限 | 允许作业 enqueue 时使用 "系统" 配额而非租户配额 |
| 内存预算误判 | 压缩/加密等操作后实际内存高于预算 | 预算设软限制（warn）+ 硬限制（reject），软限制低于硬限制 20% |

---

## 方向四：🟠 `/metrics` 端点未认证导致跨租户数据泄露

### 现状

当前认证中间件（`internal/auth/auth_middleware.go`）将以下路径列为绕过认证的白名单：

```go
func isBypassPath(path string) bool {
    return path == "/healthz" || path == "/readyz" || path == "/metrics" ||
        path == "/openapi.json" || path == "/docs" ||
        strings.HasPrefix(path, "/ui")
}
```

其中，`/metrics` 暴露的 Prometheus 指标包含以下 tenant 粒度的数据：

| 指标 | 标签 | 泄露的信息 |
|------|------|-----------|
| `http_server_requests_total` | `tenant` | 每个租户的请求速率 |
| `ai_requests_total` | `tenant`, `model` | 每个租户的 AI 使用量/模型选择 |
| `ai_tokens_total` | `tenant`, `model` | 每个租户的 token 消耗 |
| `ai_cost_micros_total` | `tenant`, `model` | 每个租户的 AI 花费（美元） |
| `ai_embed_requests_total` | `tenant`, `model` | 每个租户的嵌入请求量 |
| `ai_embed_tokens_total` | `tenant`, `model` | 每个租户的嵌入 tokens |
| `storage_bytes` | `tenant` | 每个租户的存储用量 |
| `storage_objects` | `tenant` | 每个租户的对象数量 |
| `jobs_pending` | 无 tenant | 队列深度（间接反映整体负载） |

### 影响

在单租户部署中这不是问题。但在多租户 SaaS 场景中：

| 场景 | 后果 | 严重性 |
|------|------|--------|
| 租户 A 的员工访问 `/metrics` | 看到所有租户的存储用量、请求量、AI 花费 | **数据泄露**——租户 B 的用量是商业机密 |
| 自动化监控工具访问 `/metrics` | 无问题（监控通常需要全量数据） | ✅ 可接受 |
| 竞争对手注册为租户后访问 `/metrics` | 看到平台的租户数量和增长趋势 | **商业情报泄露** |
| 安全审计 | `/metrics` 未经认证暴露 PII/业务数据 | **不合规**（SOC2、ISO 27001） |

### 缺失能力

1. **分级指标暴露**——`/metrics` 端点根据认证上下文返回不同粒度的数据：
   - 未认证 → 仅聚合指标（无 tenant label），用于负载均衡器健康检查
   - 拥有 `admin` scope 的 API key → 全量指标（所有 tenant label）
   - 拥有 `read` scope 的 API key → 仅该租户的指标

2. **可选认证**——通过配置开关控制：
   - `METRICS_AUTH_REQUIRED=true`（默认 `false` 保持向后兼容）
   - `METRICS_ALLOWED_TENANTS=*` 或 `METRICS_ALLOWED_TENANTS=internal,ops`

3. **指标过滤中间件**——在 `/metrics` handler 上附加一个中间件，根据请求的 tenant 上下文过滤 Prometheus 输出。使用 Prometheus 的 `gatherer.Gather()` + 后处理过滤，或使用多 registry 方案。

### 为什么现在需要

对于任何面向企业的多租户 SaaS，**监控数据本身就是敏感信息**。一个租户的存储增长速度、AI 使用模式和 API 调用量级可以推断出他们的业务健康状况。将 `/metrics` 暴露给所有经过认证的用户（而不仅仅是运维人员）是一个安全设计缺陷。修复方案不复杂（可配置 + 认证 + 过滤），但不修复会直接阻碍 SOC2 等合规认证。

### 架构概要

```
请求 /metrics
    │
    ├─ METRICS_AUTH_REQUIRED=false (默认)
    │   └─ 返回全量指标（当前行为，向后兼容）
    │
    └─ METRICS_AUTH_REQUIRED=true
        │
        ├─ 未认证 → 返回 401 或仅聚合指标（无 tenant label）
        ├─ key scope=admin → 全量指标
        └─ key scope=read → 仅该租户的指标
```

### 边界情况

| Edge Case | 风险 | 缓解措施 |
|-----------|------|---------|
| Prometheus Operator 使用 bearer token 抓取 | token 没有 admin scope → 仅返回部分数据 | Prometheus 配置使用 admin key |
| 混合 tenant 的 aggregation 计算 | 跨租户聚合时泄露 | 仅在 admin scope 下暴露跨租户聚合 |
| 向后兼容 | 现有部署升级后 /metrics 可能不工作 | `METRICS_AUTH_REQUIRED` 默认 false |
| 匿名公读模式下 | 公读模式本身就不安全，metrics 认证可以不加 | `METRICS_AUTH_REQUIRED` 在匿名模式自动关闭 |

---

## 方向五：🟡 Job Queue 租户级资源隔离缺失

### 现状

当前 Job Queue（`internal/jobs/jobs.go`）是一个全局共享的 FIFO（近似 FCFS，依赖 `ClaimJob` 的 SKIP LOCKED）队列：

| 组件 | 当前状态 | 问题 |
|------|---------|------|
| `Queue.MaxDepth` | 全局上限（`JOBS_MAX_DEPTH`） | 一个租户填满队列后阻止所有租户入队 |
| `Pool.workers` | 全局共享（N 个 worker 竞争队列） | 无每个租户的 worker 分配 |
| `ClaimJob` | `SELECT ... WHERE status='pending' ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED` | 纯 FCFS，大租户的批量作业抢占小租户 |
| Job deduplication | `DedupeKey` 跨租户去重 | 不同租户的 key 天然隔离（key 包含 tenant），无问题 |

### 影响

| 场景 | 后果 |
|------|------|
| 租户 A 导入 10000 个文件（触发 10000 个 index job） | 租户 B 的 5 个复制作业排在 10000 个索引作业之后→B 的复制延迟从秒级到小时级 |
| 租户 A 批量删除 5000 个对象（触发 5000 个 delete-chunks job） | 租户 C 的 3 个 AV 扫描作业被延迟 |
| 租户 A 的作业由于 bug 不断失败→重试→失败 | 租户的失败作业占满 ClaimJob 循环，影响所有租户的正常作业处理 |

### 缺失能力

1. **Per-tenant 入队上限**——`Enqueue` 时检查该租户当前 pending 的 job 数是否超过 `JOBS_MAX_PENDING_PER_TENANT`（默认 1000）。超过时返回 `ErrQueueFull`（现有 `ErrQueueFull` 路径已支持 429 返回）。

2. **优先级队列**——为作业类型分配优先级等级：
   - 高优先级：`replicate`（直接影响数据安全）
   - 中优先级：`scan`（安全相关）
   - 低优先级：`index`（功能降级而非数据丢失）
   - `ClaimJob` 改为 `ORDER BY priority, created_at`，确保高优作业总是先被处理

3. **Per-tenant worker reservation**——从全局 pool 中预留一部分 worker 给每个活跃租户（例如 4 个 worker 中，最多 2 个同时处理同一租户的作业）。使用 weighted semaphore 或 per-tenant claim quota。

4. **Per-tenant 作业监控**——在 `jobs_pending` 指标中增加 `tenant` label，暴露每个租户的队列深度。在 `jobs_failed_total` / `jobs_retried_total` 中也增加 `tenant` label。

### 为什么现在需要

AeroVault 的核心使用场景是**多租户存储**。Job queue 承载了索引、复制、防病毒扫描等关键后台功能。在一个多租户系统中，**作业处理的公平性直接决定了租户体验的公平性**。没有租户级隔离，"大量写入的批处理租户"会使"少量写入的事务型租户"的后台操作延迟到不可接受的程度。

当前的 `JOBS_MAX_DEPTH` 是「全局熔断」——它防止系统过载，但不做租户间的公平调度。从 FIFO 到 weighted fair queuing 的转变，是多租户系统成熟的标志。

### 架构概要

```
当前（FIFO）:
  pending jobs: [A1, A2, A3, ..., A10000, B1, B2, B3]
                                              ↑ B 的作业被 A 的 10000 个作业阻塞

目标（Weighted Fair Queuing）:
  pending jobs: 租户 A: [A1, A2, ..., A200] (max 200 per tenant)
                租户 B: [B1, B2, B3]        (不受影响)

  ClaimJob ORDER BY priority, created_at
    → 高优先级 (replicate, scan) 优先
    → 同优先级内按租户轮转

  Per-tenant 并发上限:
    worker 在处理租户 A 的作业 ≥ PER_TENANT_JOB_CONCURRENCY 时
    → 暂不 claim 租户 A 的新作业，让给其他租户
```

### 边界情况

| Edge Case | 风险 | 缓解措施 |
|-----------|------|---------|
| Per-tenant 上限过小导致正常作业被限 | 大租户的正常索引被限 | 上限可配置 + 超过上限时记录 warn 而非 error |
| 优先级队列导致低优类型永远不被处理 | 索引作业饿死 | 低优作业也会被处理（只是优先级低），且在队列深度较低时恢复正常调度 |
| 租户级 worker 预留导致总吞吐下降 | 4 个 worker 但每个租户最多 2 个，如果有 3 个活跃租户则 2 个 worker 闲置 | 不预留固定 worker，仅设置 per-tenant 上限；一个租户不活跃时不占用配额 |
| DedupeKey 与 per-tenant 上限交互 | 去重的 job 不消耗配额？ | 去重返回已有 job ID，不占用 pending quota |

---

## 跨方向依赖关系

```
方向 1 (SSE 泄漏)     ──→ 修复独立，无依赖
方向 2 (Worker 健康)  ──→ 方向 3 (租户隔离): 健康检查需要考虑每个租户的资源隔离是否正常
                      ──→ 方向 4 (Metrics 隔离): 健康端点自身也需考虑信息泄露
方向 3 (租户隔离)     ──→ 方向 5 (Job 隔离): 租户隔离需要 job queue 层面的公平调度
                      ──→ 方向 2 (Worker 健康): per-tenant 资源使用需要在健康端点中可见
方向 4 (Metrics 隔离) ──→ 独立，但在方向 3 实施后 metrics 中 tenant label 更敏感
方向 5 (Job 隔离)     ──→ 方向 3 (租户隔离): 是资源隔离的子集
                      ──→ 方向 2 (Worker 健康): per-tenant 队列深度需要在 worker 健康中反映
```

**建议实施顺序：**

| 阶段 | 方向 | 理由 |
|------|------|------|
| 🔥 立即 | 方向 1 — SSE Subscriber Unsubscribe | 1-2 天，消除一个明确的内存泄漏 |
| 🔥 立即 | 方向 4 — `/metrics` 分级暴露（配置开关默认关闭） | 安全合规基线，配置项控制，不影响现有部署 |
| 🟠 短期 | 方向 2 — Worker 健康管理（Phase 1: 指标 + /readyz） | 大规模提升生产可观测性，边际成本低 |
| 🟠 中期 | 方向 5 — Job Queue 租户隔离（Phase 1: per-tenant pending cap） | 防止单租户填满全局队列，优先级高 |
| 🟡 长期 | 方向 3 — 多租户计算资源隔离 | 综合性架构调整，需要与现有限速/隔离机制协同 |

---

## 非目标（明确排除）

为避免方向膨胀，本期各方向的有意限制：

- **方向 1 不涉及**：多进程事件总线或分布式 SSE 方案（ROADMAP #3 已覆盖）
- **方向 2 不涉及**：各 worker 的具体功能改进（已在前 38 期覆盖）
- **方向 3 不涉及**：存储 I/O QoS（v36 独立覆盖）、网络带宽隔离（需基础设施层支持）
- **方向 4 不涉及**：审计日志中的指标访问记录（可选增值功能，非核心）
- **方向 5 不涉及**：作业级别 SLA 或超时（属 SLO 框架，未来独立方向）
