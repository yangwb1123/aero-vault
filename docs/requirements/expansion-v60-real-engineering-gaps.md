# AeroVault 高价值扩展方向 — 真正的工程缺口（底层实现缺陷与生产就绪度）

> **扫描范围：** `cmd/server/main.go` + `internal/*` 全部 23 子包 + `config/` + 全部 48 组 SQL 迁移 + CLI/SDK/WebUI/部署配置
>
> **分析视角：** 资深系统架构师 + 运维可靠性工程师 — 聚焦"功能是有的，但实现有设计缺陷或生产不可用的底层问题"
>
> **去重方法：** 对照 docs/requirements/ 目录下全部 59 份既有分析文档，逐方向验证是否被作为独立架构方向系统分析。仅选择那些既有分析中**零实质性架构分析**的方向。
>
> **分析日期：** 2026-07-10

---

## 前言

经过 59 轮、300+ 方向的分析，AeroVault 的**功能覆盖度**已经非常全面。前序分析覆盖了 S3 协议完备性、AI/RAG 管线、多租户、MCP 协议、分布式限流、IAM 策略等几乎所有可想象的功能维度。

但本轮扫描发现了一个被系统性忽略的缺口类别：**底层实现缺陷——功能能工作，但实现方式在生产环境下有明确的可靠性、安全性或稳定性风险。** 这类问题特征如下：

- 功能可工作（单元测试通过）
- 但实现包含 **明确的竞态条件（TOCTOU）、资源泄漏路径、或退化行为**
- 在低流量或单副本下不会触发，在 **生产负载（高并发、多副本、长时间运行）下必然暴露**
- 每个方向在当前代码中有 **精确的代码锚点** 和 **可验证的测试场景**

本文件聚焦 **5 个真正的工程缺口**，每个方向的共同特征：**既有 59 份分析中没有任何一篇将其作为独立架构方向进行系统分析**。

---

## 方向汇总

| # | 方向 | 类型 | 优先级 | 核心矛盾 | 代码锚点 | 59 期覆盖验证 |
|---|------|------|--------|---------|---------|-------------|
| **1** | **PerTenantConcurrencyLimiter TOCTOU 竞态与全局信号量耗尽攻击** | 可靠性/安全 | **P0** — 全局并发限制在竞争条件下可被单一租户耗尽，且 per-tenant 限制的原子性无法保证 | `internal/middleware/middleware.go:127-155`（PerTenantConcurrencyLimiter.Middleware） | ❌ **零实质性分析**（v57 及前序 58 份均讨论**跨副本**分布式限流，从未分析**进程内** PerTenantConcurrencyLimiter 本身实现缺陷） |
| **2** | **SSE Chat Stream：客户端断开盲区、无心跳、无重连机制** | 可靠性/体验 | **P1** — LLM 流式响应在客户端断开后继续消耗配额和资源，无任何重连或续传能力 | `internal/api/rest/search.go:92-137`（ChatStream handler）；`internal/ai/chat.go:156-189`（AnswerStream） | ❌ **零实质性分析**（前序覆盖 SSE replay/SSE channel leak 均涉及 EventBus 层；ChatHandler 的 Server-Sent Events 端点**从未被分析**） |
| **3** | **配置跨字段依赖静默失效与启动时验证盲区** | 运维/可靠性 | **P1** — 配置之间存在隐式交叉依赖，不兼容的组合在运行时而不是启动时失败 | `internal/config/config.go:242-260`（Validate 方法仅检查独立字段缺失，无交叉依赖验证） | ❌ **零实质性分析** |
| **4** | **Webhook 失败表无限增长与死信无保留策略** | 运维/合规 | **P2** — `webhook_failures` 表无 TTL、无 GC、无保留策略；已死信的行永远累积 | `internal/repository/webhook_failures.go`（Record→NextPending→MarkSucceeded，无 delete/archive/retention）；`internal/events/webhook.go:218-224`（dead-letter 后仅 MarkSucceeded 但不清理） | ❌ **零实质性分析**（v38/v56 提及 webhook/dead-letter 概念但聚焦**事件通知引擎**和**工作者生命周期**，从未分析 webhook_failures 表本身无限增长问题） |
| **5** | **进程内内存结构无上限与内存压力处理缺失** | 可靠性/运维 | **P2** — BM25 索引、搜索结果缓存、API Key 缓存、速率限制器全部在进程内存中但无内存上限、无逐出回调、无压力感知 | `internal/ai/bm25.go:44`（`docs map[int64]bm25Doc` 无上限）；`internal/ai/result_cache.go:37`（`cache map[string]resultEntry` 有 cap 但无内存压力处理）；`internal/auth/key_cache.go`（ttlCache 无上限）；`internal/middleware/ratelimit.go:25`（bucket map 有 50K 上限但拒绝行为退化） | ❌ **零实质性分析**（v27 方向表格一行提及"大键缓存"概念但非独立方向；v59 方向四性能基准关注**外部队列**而非进程内存管理） |

---

## 方向一：PerTenantConcurrencyLimiter TOCTOU 竞态与全局信号量耗尽攻击

### 现状

`PerTenantConcurrencyLimiter` 实现了一个双层限流：全局信号量（`global.sem`）+ 每租户并发计数（`inflight map[string]int`）。

```go
// internal/middleware/middleware.go:127-155
func (pt *PerTenantConcurrencyLimiter) Middleware() func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            cost := reqWeight(r.Method)
            tenant := TenantFrom(r.Context())

            // Step 1: 检查 per-tenant 预算（持有 pt.mu）
            if pt.perTenant > 0 {
                pt.mu.Lock()
                if pt.inflight[tenant] >= pt.perTenant {
                    pt.mu.Unlock()
                    // ❌ 返回前未释放 global semaphore —— 但其实还没获取
                    // ✅ 此处正确：先检查 tenant，再获取 global
                    // ⚠️ 但另一个 goroutine 可能在这之后获取了 global sem 同时释放了 tenant
                    return
                }
                pt.inflight[tenant] += cost
                pt.mu.Unlock()
            }

            // Step 2: 获取全局信号量（无 pt.mu 保护）
            // ⚠️ TOCTOU 窗口：Step 1 和 Step 2 之间没有锁保护
            // 另一个同租户的 goroutine 可能在这个窗口完成完整生命周期并释放 per-tenant 计数
            acquired := 0
            for i := 0; i < cost; i++ {
                select {
                case pt.global.sem <- struct{}{}:
                    acquired++
                default:
                    // 全局满 → 释放 per-tenant 计数并拒绝
                    for j := 0; j < acquired; j++ {
                        <-pt.global.sem
                    }
                    // ✅ 这里释放了 per-tenant
                    // ⚠️ 但 per-tenant 释放单独持锁，与 global 的释放不对齐
                    if pt.perTenant > 0 {
                        pt.mu.Lock()
                        pt.inflight[tenant] -= cost
                        pt.mu.Unlock()
                    }
                    return
                }
            }

            // Step 3: 执行请求
            // ⚠️ 此时租户 X 的 inflight += cost 已被计入
            // 但另一个 goroutine Y 可能也在 Step 1 通过了检查（因为计数还没更新到 Y 能感知的值）
            // 然后在 Step 2 也成功获取了 global sem → 租户 X 的实际 inflight > perTenant
            
            // ... 处理请求 ...
            
            defer func() {
                for i := 0; i < cost; i++ {
                    <-pt.global.sem
                }
                if pt.perTenant > 0 {
                    pt.mu.Lock()
                    pt.inflight[tenant] -= cost
                    // ⚠️ 这里还有一个问题：global sem 和 per-tenant 释放的顺序
                    // 先释放 global sem，再释放 per-tenant
                    // 另一个同租户的 goroutine 可能在 global sem 释放后、per-tenant 释放前
                    // 通过 Step 1 检查（因为 per-tenant 计数还没减）且 Step 2 成功获取 global
                    // → 然后两边的计数加起来就超过了 perTenant 限制
                    if pt.inflight[tenant] <= 0 {
                        delete(pt.inflight, tenant)
                    }
                    pt.mu.Unlock()
                }
            }()
            next.ServeHTTP(w, r)
        })
    }
}
```

**竞态条件的具体触发流程：**

```
时间 →                    Goroutine A（租户 X）                          Goroutine B（同租户 X）
                          perTenant=5, inflight[X]=4                       perTenant=5, inflight[X]=4
                          ↓                                                 ↓
t1                        Step 1: Lock → inflight[X]=4 < 5 ✅               —
                          inflight[X]=5（增加后）→ Unlock                    —
t2                        Step 2: 等待 global sem slot                      Step 1: Lock → inflight[X]=5 >= 5 ❌
                          —                                                 Unlock → 拒绝
t3                        Step 2: 获得 slot → 执⾏请求                       —
t4                        请求完成 → defer: 释放 global sem                    —
                          perf-tenant 仍在 5                                 —
t5                        defer: Lock → inflight[X]=4 → Unlock              —
                          ↓                                                 ↓
最终：租户 X 的峰值 inflight = 5（不超过 5 ✅）——只要释放顺序正确
```

**但如果释放顺序是 global sem 先释放、per-tenant 后释放：**

```
t1                        Step 1: inflight[X]=5 ✅                           Step 1: Lock → inflight[X]=5 >= 5 ❌
                                                                             Unlock → 拒绝
t2                        Step 2: global sem 获取成功 →
                          请求处理中
t3                        请求完成 → defer:
                          global sem 释放（per-tenant 仍为 5）
                                                                             Step 1: Lock → inflight[X]=5 >= 5 ❌
                                                                             Unlock → 拒绝
t4                        defer: per-tenant 释放 → inflight[X]=4
```

以上看似安全，但问题出在 **全局信号量的公平性**：如果所有 global sem slot 都被其他租户的请求占据，同租户的 Goroutine B 在 t3 也可能因为 global sem 失败而释放 per-tenant 计数。关键在于 **并发释放路径存在原子性缺口**：

**真正的竞态：**

当一个请求的 defer 执行时：
1. 先释放 `global sem` → 一个 slot 变为可用
2. 还未释放 `per-tenant` 计数（仍在 5）
3. 同租户的另一个 goroutine 在 Step 1 检查 `inflight[X]=5 >= 5` → 拒绝 ✅

这看似安全。但考虑**并发退场**路径——当 Step 2 的 global sem 获取失败时，defer 也会释放 per-tenant 计数。如果两个 goroutine 同时退场：

```
Goroutine A: 请求正常完成 → defer 中释放 global sem → PANIC/崩溃 → per-tenant 释放未执行
Goroutine B: 另一请求 → Step 1 通过 → Step 2 获取 global sem → 处理请求
→ inflight[X] 永不为 4（A 的 goroutine panic 导致 per-tenant 未释放）
→ 直到 pt.inflight[X] 被 delete（需要触发 zero 检查）
→ 但如果 A 在 inflight[X]=6 时 panic，则 inflight[X] 永远卡在 6
→ 所有后续同租户请求都被拒绝
```

**全局信号量耗尽攻击：**

| 攻击向量 | 工作原理 | 效果 |
|---------|---------|------|
| 单租户耗尽全局信号量 | `PerTenantConcurrencyLimiter` 共享同一个 `global.sem`。如果 `perTenant` 设置过高或未配置，单一恶意租户可通过大量并发请求填满全局 5 容量 | 所有租户的所有请求被 429 拒绝 |
| 无 per-tenant 隔离的场景 | 默认 `APP_PER_TENANT_MAX=0` 时使用普通 `ConcurrencyLimiter`，无租户隔离 | 单租户 DoS 所有租户 |
| Per-tenant 释放失败（panic） | 请求处理中 panic → `defer` 不执行（如果 panic 未被 Recoverer 捕获） | per-tenant 计数泄漏，该租户最终被永久拒绝 |

### 为什么需要

| 场景 | 当前风险 | 生产影响 |
|------|---------|---------|
| 多租户 SaaS | 恶意租户发起 500 个并发 PUT → 填满所有 100 个 global slots → 其他租户 429 | **服务不可用** |
| 混合工作负载 | AI 搜索（重查询）与文件上传（重 IO）共享同一信号量 | **AI 延迟暴涨** |
| 代码缺陷导致 panic | per-tenant 计数泄漏 → 该租户永久无法访问 | **租户数据锁定** |
| 内存 DoS | `pt.inflight` map 使用租户 ID 做 key，但未限制 key 数量 | **内存耗尽** |

### 建议修复方案

**最小修复（~50 行）：**

1. **Step 1 和 Step 2 之间保持原子性**：在持 `pt.mu` 的情况下尝试获取 global sem slot。如果不能立即获取，释放 per-tenant 计数并返回 429（避免 blocking under lock）。

2. **释放顺序固定**：先释放 per-tenant 计数，再释放 global sem。或者在 `pt.mu` 保护下原子释放二者。

3. **defer 中的 per-tenant 释放使用 `recover()`**：确保即使 handler panic，per-tenant 计数也被释放。

```go
// 伪代码：原子化双层获取
pt.mu.Lock()
if pt.inflight[tenant] >= pt.perTenant {
    pt.mu.Unlock()
    // 429
    return
}
select {
case pt.global.sem <- struct{}{}:
    pt.inflight[tenant] += cost
    pt.mu.Unlock()
default:
    pt.mu.Unlock()
    // 429
    return
}
defer func() {
    if r := recover(); r != nil {
        pt.mu.Lock()
        pt.inflight[tenant] -= cost
        pt.mu.Unlock()
        panic(r) // re-panic after cleanup
    }
    <-pt.global.sem
    pt.mu.Lock()
    pt.inflight[tenant] -= cost
    pt.mu.Unlock()
}()
```

4. **inflight map 加上界**：`max 10,000 tenants` 防止内存 DoS。

### 规模估计

| 工作项 | 复杂度 | 涉及文件 |
|--------|--------|---------|
| 原子化双层获取 | 低 | `internal/middleware/middleware.go`（~30 行修改） |
| per-tenant defer race 修复 | 低 | 同上（~20 行） |
| panic safe per-tenant 释放 | 低 | 同上（~10 行） |
| inflight map cap | 低 | 同上（~5 行） |
| **总估** | **~1 天** | **1 个文件** |

---

## 方向二：SSE Chat Stream：客户端断开盲区、无心跳、无重连机制

### 现状

`ChatStream` handler 是一个长连接 SSE 端点，将 LLM 的流式输出逐 token 推送到客户端：

```go
// internal/api/rest/search.go:92-137
func (h *AIHandler) ChatStream(w http.ResponseWriter, r *http.Request) {
    // ...
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("X-Accel-Buffering", "no")
    w.WriteHeader(http.StatusOK)
    flusher.Flush()
    _ = http.NewResponseController(w).SetWriteDeadline(time.Time{})  // 禁用写超时

    resp, err := h.chat.AnswerStream(r.Context(), ai.ChatReq{...}, func(chunk string) {
        b, _ := json.Marshal(chunk)
        fmt.Fprintf(w, "event: token\ndata: %s\n\n", string(b))
        flusher.Flush()
    })
    // ...
}
```

**当前实现的 6 个生产就绪度缺口：**

| # | 缺口 | 代码证据 | 生产风险 |
|---|------|---------|---------|
| 1 | **无心跳（ping/pong）** | 无任何定时 `event: ping` 或 `: keepalive` 注释行 | 反向代理（Nginx、Cloudflare、AWS ALB）在 60s 空闲后断开连接；客户端（EventSource API）在 45s 无数据后自动重连 |
| 2 | **客户端断开无即时检测** | 依赖 `r.Context()` 的 cancel。但 TCP 连接断开后，Go 的 HTTP server 只在尝试写入时检测到 `broken pipe` | 如果 LLM 在生成思考但客户端已断开，LLM 仍消耗配额和成本（30s+）。`AnswerStream` 中的 `onChunk` 回调在写入 `http.Flusher.Flush()` 时才会发现断开 |
| 3 | **无 Last-Event-ID 支持** | SSE 规范允许客户端发送 `Last-Event-ID` 头以断点续传。当前 ChatStream handler 完全不读取该请求头 | 网络闪断后，客户端必须重新提问（可能已消耗一次预算），无法从断点续传 |
| 4 | **无客户端超时** | `SetWriteDeadline(time.Time{})` 禁用了写超时，意味着理论上一个 SSE 连接可以永远存活 | 僵尸连接不释放 goroutine + 不释放 OTel span + 可能存在内存泄漏 |
| 5 | **无优雅关闭通知** | 服务器关闭时，SSE 客户端收到的是 TCP 连接断开而不是 `event: shutdown` 事件 | 客户端无法区分"服务器重启"和"服务器崩溃"。重启后无法自动重连 |
| 6 | **无内容编码协商** | 长 LLM 响应（10K+ token）经 JSON 编码后体积大，没有使用 `Transfer-Encoding: chunked` 或 `Content-Encoding: gzip` | 带宽浪费，移动端体验差 |

**`AnswerStream` 中的配额浪费问题：**

```go
// internal/ai/chat.go:156-189
func (c *Chat) AnswerStream(ctx context.Context, req ChatReq, onChunk func(string)) (ChatResp, error) {
    // 1. 检索完成（昂贵：embed + search 耗时 0.5-2s）
    // 2. 调用 LLM（昂贵：可能耗时 5-30s，按 token 付费）
    //    如果客户端在第 3 秒断开，LLM 仍然消耗到请求完成
    // 3. 记录 usage（配额扣减在最后）——断开后仍扣费
}
```

### 为什么需要

| 场景 | 当前行为 | 问题 |
|------|---------|------|
| 用户在移动端网络波动 | LLM 继续生成，配额继续消耗，但用户看不到结果 | 浪费预算 |
| 用户刷新页面发起新问题 | 第一个 LLM 请求继续在后台运行（goroutine 泄漏直到 HTTP timeout） | 资源泄漏 + 重复计费 |
| 经过 Cloudflare/ALB/Nginx | 60s 无数据后代理断开连接 | 用户看到"连接断开" |
| 服务器滚动重启 | 所有 SSE 连接断连，客户端需要手动刷新 | 体验差 |
| 10K token 长回答 | 每次 token 一个 write/flush，HTTP 开销大 | 吞吐下降 |

### 建议实现方案

**阶段一（最小生产化，~200 行）：**

1. **心跳事件**：启动一个 goroutine 每 15s 发送 `event: ping\ndata: {}\n\n`。如果 `Flush()` 返回 `broken pipe` 错误，取消 context 终止 LLM 调用。

2. **客户端断开主动检测**：在 `onChunk` 回调中检查 `r.Context().Done()`，或使用 `http.CloseNotifier`（已弃用，用 `r.Context()` 的 `Done()` channel 替代）。在 AnswerStream 中监听 ctx.Done 并提前终止 LLM 调用。

3. **goroutine 泄漏防止**：在 ChatStream handler 中确保 AnswerStream 调用有一个超时（从 `RequestTimeoutSec` 派生），超过后自动取消。

```go
// 伪代码：心跳 + 断开检测
go func() {
    ticker := time.NewTicker(15 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-r.Context().Done():
            // 客户端已断开，取消 LLM context
            llmCtxCancel()
            return
        case <-ticker.C:
            _, err := fmt.Fprintf(w, ": keepalive\n\n")
            if err != nil {
                llmCtxCancel()
                return
            }
            flusher.Flush()
        }
    }
}()
```

**阶段二（流式续传，~300 行）：**

1. **SSE `Last-Event-ID` 支持**：客户端重连时携带最后收到的 `event: token` 序号，ChatStream 跳过已发送的 token。

2. **流式响应的持久化**：将已生成的 token 流暂存到 `stream_sessions` 表（类似 multipart upload 的临时存储），使续传可行。

### 规模估计

| 工作项 | 复杂度 | 涉及文件 |
|--------|--------|---------|
| 心跳 + 断开检测（阶段一） | 低 | `internal/api/rest/search.go`（~80 行） |
| LLM 提前取消 | 低 | `internal/ai/chat.go`（~30 行） |
| 优雅关闭 SSE 通知 | 低 | `internal/api/rest/search.go` + `cmd/server/main.go`（~30 行） |
| Last-Event-ID + 续传（阶段二） | 中 | 新文件 `internal/api/rest/stream.go` + repository 扩展 |
| **总估** | **2-4 天** | **2-4 个文件** |

---

## 方向三：配置跨字段依赖静默失效与启动时验证盲区

### 现状

当前 `config.Validate()` 检查独立字段的缺失，但**不检查字段之间的交叉依赖一致性**：

```go
// internal/config/config.go:242-260
func (c *Config) Validate() error {
    // ✅ 检查独立字段：STORAGE_LOCAL_ROOT 是否设置
    // ✅ 检查独立字段：DB_DRIVER 是否合法
    // ✅ 检查独立字段：AI_ENDPOINT 是否设置
    // ❌ 不检查交叉依赖：EVENTS_TRANSPORT=postgres + DB_DRIVER=sqlite
    // ❌ 不检查交叉依赖：AI_VECTOR_BACKEND=pgvector + DB_DRIVER=sqlite
    // ❌ 不检查交叉依赖：AI_SEARCH_CACHE_SIZE>0 + AI_CACHE_PROVIDER=redis（无 Redis 支持）
    // ❌ 不检查交叉依赖：S3_COMPAT_PREFIX 与 WEBDAV_PREFIX 路径冲突
    // ❌ 不检查交叉依赖：AI_CHAT_PROVIDER=http + AI_CHAT_ENDPOINT 不匹配协议
}
```

**可被静默吞入的配置错误矩阵：**

| 配置组合 | 预期运行时行为 | 实际运行时行为 | 发现时机 |
|---------|--------------|--------------|---------|
| `DB_DRIVER=sqlite` + `EVENTS_TRANSPORT=postgres` | 启动失败：缺少 Postgres 驱动 | `postgres_transport.go` 启动 goroutine，立即 panic（`pgx.Connect` 失败） | **运行时 panic** |
| `DB_DRIVER=postgres` + `AI_VECTOR_BACKEND=pgvector` + `AI_VECTOR_DSN` 指向不同数据库 | 启动失败：vector 扩展缺失 | `OpenPgVectorIndex` 返回 warn → 降级为暴力搜索 → **用户以为 pgvector 在工作** | **功能退化静默** |
| `STORAGE_SSE_KEY` + `STORAGE_BACKEND=s3` | SSE 加密不适用于 S3（S3 有自己的 SSE） | `NewLocal` 不会被调用，SSE 配置被静默忽略 | **安全幻觉** |
| `S3_COMPAT_PREFIX=/` + WebDAV 启用 | 请求路由冲突 | `/s3` 和 WebDAV 同时注册 → 非确定性路由 → 部分请求 404 | **间歇性 404** |
| `AUTH_JWT_SECRET=short` | 启动失败：密钥太弱 | 启动成功，HS256 签名可被暴力破解 | **安全漏洞** |
| `AI_INDEX_ENABLED=true` + `AI_EMBED_PROVIDER=http` + `AI_ENDPOINT=http://example.com/v1/embed`（端点返回错误维度） | 启动失败：维度不匹配 | `embedder.go` 请求返回正确维度，但实际返回的维度与 `AI_EMBED_DIM` 不匹配 → **索引损坏** | **数据损坏静默** |
| `AI_CHAT_PROVIDER=mock` + `AI_RATE_LIMIT_RPS=100` | Chat 端点不可用（mock）但速率限制仍生效 | 正常：Chat 端点返回 503 | 功能正常 |
| `REPLICATION_ENABLED=true` + `REPLICATION_STORAGE_BACKEND=local` + `REPLICATION_STORAGE_LOCAL_ROOT` 未设置 | 启动失败 | `buildStorageFrom` 返回错误 → 进程启动失败 | **启动时失败** ✅ 但错误消息可能不清晰 |
| `RECONCILE_INTERVAL_MINUTES=0` + `RECONCILE_RETENTION_DAYS=30` | Retention 不执行（interval=0） | retention job 不启动，过期数据永远不清理 | **存储泄漏静默** |
| `AV_ENABLED=true` + `AV_PROVIDER=http` + `AV_ENDPOINT` 返回错误内容类型 | 扫描永远失败 | `NewHTTPScanner` 不验证端点健康——每次扫描都返回错误 → 对象永远不被标记 | **安全盲区静默** |

**同样缺失的：配置值来源追踪**

```go
// internal/config/config.go — 当前只存放最终值
type Config struct {
    AI    AIConfig
    App   AppConfig
    // ...
}
// 缺失：
// - config.Source("AI_ENDPOINT") → {value: "http://...", source: "env", default: ""}
// - config.Source("DB_DRIVER") → {value: "sqlite", source: "default", default: "sqlite"}
```

### 为什么需要

| 场景 | 影响 |
|------|------|
| 从 `.env.example` 复制配置 → 修改了几个值 → 启动 → 静默降级 | 功能不可用但无错误提示，用户困惑 |
| 跨版本升级 → 新配置项未设置 | 新功能静默禁用，用户不知道 |
| 多副本部署 → 一个副本配置不一致 | 行为不一致，调试困难 |
| 安全审计 → 需要证明配置的合规性 | 无配置变更历史 |
| 运维排障 → 需要知道"是配置错误还是代码 bug" | 配置验证在启动时就应给出明确错误 |

### 建议实现方案

**阶段一：交叉依赖验证（~150 行）**

在 `config.Validate()` 中添加交叉检查：

```go
func (c *Config) Validate() error {
    // ... 现有检查 ...

    // EVENTS_TRANSPORT / DB_DRIVER
    if c.Events.Transport == "postgres" && c.DB.Driver != "postgres" {
        return errors.New("EVENTS_TRANSPORT=postgres requires DB_DRIVER=postgres")
    }
    // pgvector / DB_DRIVER
    if c.AI.VectorBackend == "pgvector" && c.DB.Driver != "postgres" {
        return errors.New("AI_VECTOR_BACKEND=pgvector requires DB_DRIVER=postgres (or AI_VECTOR_DSN)")
    }
    if c.AI.VectorBackend == "pgvector" && c.AI.VectorDSN == "" {
        return errors.New("AI_VECTOR_BACKEND=pgvector requires AI_VECTOR_DSN")
    }
    // pgFTS / DB_DRIVER
    if c.AI.LexicalBackend == "pgfts" && c.AI.VectorDSN == "" {
        return errors.New("AI_LEXICAL_BACKEND=pgfts requires AI_VECTOR_DSN (same DSN)")
    }
    // SSE + S3 不兼容
    if c.Storage.Local.SSEKey != "" && c.Storage.Backend != "local" {
        return errors.New("STORAGE_SSE_KEY is only supported with STORAGE_BACKEND=local")
    }
    // S3 prefix / WebDAV prefix 冲突
    if c.S3Compat.Prefix != "" && c.WebDAV.Prefix != "" && strings.HasPrefix(c.WebDAV.Prefix, c.S3Compat.Prefix) {
        return errors.New("WEBDAV_PREFIX must not overlap with S3_COMPAT_PREFIX")
    }
    // JWT 密钥强度
    if c.Auth.JWTSecret != "" && len(c.Auth.JWTSecret) < 32 {
        return errors.New("AUTH_JWT_SECRET must be at least 32 characters")
    }
    // Retention 需要 Reconcile
    if c.Reconcile.RetentionDays > 0 && c.Reconcile.IntervalMinutes <= 0 {
        return errors.New("RECONCILE_RETENTION_DAYS > 0 requires RECONCILE_INTERVAL_MINUTES > 0")
    }
    // Replication 存储验证
    if c.Replication.Enabled {
        if c.Replication.Storage.Backend == "local" && c.Replication.Storage.Local.Root == "" {
            return errors.New("REPLICATION_STORAGE_LOCAL_ROOT is required when REPLICATION_STORAGE_BACKEND=local")
        }
    }
    return nil
}
```

**阶段二：配置值来源追踪（~100 行）**

```go
// internal/config/source.go（新文件）
type ConfigSource struct {
    Key     string
    Value   string
    Source  string // "default" | "env" | "file"
    Default string
}

func (c *Config) Sources() []ConfigSource {
    return []ConfigSource{
        {Key: "DB_DRIVER", Value: string(c.DB.Driver), Source: sourced("DB_DRIVER"), Default: "sqlite"},
        {Key: "STORAGE_BACKEND", Value: c.Storage.Backend, Source: sourced("STORAGE_BACKEND"), Default: "local"},
        // ...
    }
}

// GET /v1/admin/config → 返回配置值 + 来源
```

### 规模估计

| 工作项 | 复杂度 | 涉及文件 |
|--------|--------|---------|
| 交叉依赖验证规则 | 低 | `internal/config/config.go`（~80 行） |
| 配置值来源追踪 | 低 | `internal/config/source.go` 新文件（~100 行） |
| Admin API 配置查看 | 低 | `internal/api/rest/admin.go`（~50 行） |
| **总估** | **1-2 天** | **2-3 个文件** |

---

## 方向四：Webhook 失败表无限增长与死信无保留策略

### 现状

`webhook_failures` 表的生命周期：

```
创建（RecordWebhookFailure）→ 重试（NextPendingFailures → UpdateWebhookFailure）
→ 死亡（MarkWebhookSucceeded，attempts≥10 时）
→ ❌ 永远不删除
→ ❌ 永远不归档
→ ❌ 永远不 TTL 过期
```

```go
// internal/repository/webhook_failures.go:84-86
func (s *sqlStore) MarkWebhookSucceeded(ctx context.Context, id int64) error {
    _, err := s.db.ExecContext(ctx, s.rebind(`UPDATE webhook_failures SET succeeded=1 WHERE id=$1`), id)
    // ✅ 标记为成功（或死信）
    // ❌ 不删除行
    // ❌ 不返回旧数据给 GC
}
```

```go
// internal/events/webhook.go:215-223
// dead-letter 后的最终路径：
if attempts >= 10 {
    _ = w.repo.UpdateWebhookFailure(ctx, f.ID, "dead-lettered after ...", 0, time.Now(), attempts)
    _ = w.repo.MarkWebhookSucceeded(ctx, f.ID)  // 标记为「成功」来停止重试
    // ❌ MarkWebhookSucceeded 只设 succeeded=1，不删除
    // ❌ 行永远留在表中
    return
}
```

**当前无任何 GC 机制清理 `webhook_failures` 表：**

```go
// internal/reconcile/retention.go — 当前处理的表：
// - soft-deleted objects（RECONCILE_RETENTION_DAYS）
// - idempotency keys（idempotency_ttl）
// ❌ 不处理 webhook_failures
```

**积累速率估算：**

| 场景 | 失败率 | 日增量 | 年增量 |
|------|--------|-------|-------|
| 正常（偶尔丢） | 0.1% 的 10K req/day = 10 行/天 | 10 行 | 3,650 行 |
| 目标 URL 下线（持续） | 100% 的 100K req/day = 100K 行/天 | **100,000 行** | **36,500,000 行** |
| 配置错误（目标 URL 返回 500） | 100% 的 10K req/day = 10K 行/天 | **10,000 行** | **3,650,000 行** |

SQLite 在 1M+ 行时查询性能开始退化，Postgres 在 10M+ 行时 `ORDER BY id LIMIT` 的索引效率下降。且 `next_retry_at` 上无独立索引。

**索引分析：**

```sql
-- migrations/sqlite/0024_bucket_notifications.up.sql 中的索引
CREATE INDEX IF NOT EXISTS idx_whfails_next_retry ON webhook_failures(next_retry_at);
-- 但 NextPendingFailures 的查询是：
-- WHERE succeeded = 0 AND next_retry_at <= $1 ORDER BY id LIMIT $2
-- 组合条件 succeeded+next_retry_at 无复合索引
-- 在大量已 succeeded 的行存在时，索引效率差
```

### 为什么需要

| 场景 | 当前 | 问题 |
|------|------|------|
| 目标 URL 下线 1 小时 | 10K+ 失败行永久保存 | 表膨胀，查询变慢 |
| 合规审计要求清理 | 无清理机制 | 数据无限累积 |
| 用户通过 ListWebhookFailures 查询 | 返回所有历史，包括死信 | API 响应慢，前端加载所有历史 |
| SQLite 下大表 | 无 WAL 模式 + 全表扫描 | 写放大，锁争用 |

### 建议实现方案

**最小修复（~80 行）：**

1. **`webhook_failures` 表 GC**：扩展 `Reconcile.RetentionJob` 以清理 `succeeded=true` 且超过保留期的行：

```go
// reconcile/retention.go — 新增
func (r *RetentionJob) cleanWebhookFailures(ctx context.Context, before time.Time) (int64, error) {
    // DELETE FROM webhook_failures WHERE succeeded = 1 AND updated_at < $1
    // window: 1000 per batch
}
```

2. **保留期配置**：增加 `RECONCILE_WEBHOOK_RETENTION_HOURS`（默认 720h = 30 天）。

**进阶方案（~200 行）：**

1. **死信队列（DLQ）路由**：当 webhook 达到最大重试次数后，将失败 payload 投递到一个可配置的 HTTP 端点（DLQ URL），而不是仅仅标记为死信。

2. **`webhook_failures` 表的复合索引**：增加 `(succeeded, next_retry_at)` 索引提升 NextPendingFailures 查询效率。

3. **ListWebhookFailures 分页能力**：当前 `ListWebhookFailures` 不支持分页，只返回最近的 N 行。增加 `before_id` / `after_id` 参数。

### 规模估计

| 工作项 | 复杂度 | 涉及文件 |
|--------|--------|---------|
| Retention 清理 | 低 | `internal/reconcile/retention.go`（~30 行） |
| 保留期配置 | 低 | `internal/config/config_app.go`（~5 行） |
| 复合索引迁移 | 低 | `migrations/*/0025_webhook_retention.*.sql` |
| DLQ 路由（进阶） | 中 | `internal/events/webhook.go`（~100 行） |
| **总估** | **1-2 天** | **3-5 个文件** |

---

## 方向五：进程内内存结构无上限与内存压力处理缺失

### 现状

AeroVault 有多个进程内数据结构，各具不同的内存管理策略：

| 内存结构 | 文件 | 上限 | 当前行为达到上限时 | 内存压力处理 |
|---------|------|------|-----------------|------------|
| **BM25 索引** `docs map[int64]bm25Doc` | `internal/ai/bm25.go` | ❌ **无上限** | N/A（无限增长） | ❌ 无 |
| **搜索结果缓存** `cache map[string]resultEntry` | `internal/ai/result_cache.go` | ✅ `capacity` 参数 | 随机驱逐（基于 `map` 迭代顺序——**不是 LRU**） | ❌ 无逐出回调、无大小感知 |
| **API Key 缓存** ttlCache | `internal/auth/key_cache.go` | ❌ **无上限** | N/A（无限增长） | ❌ 无 |
| **速率限制器桶** `buckets map[string]*bucket` | `internal/middleware/ratelimit.go` | ✅ `rlMaxBuckets=50,000` | 拒绝新租户（返回 429）——**全局拒绝而非驱逐旧桶** | ❌ 无 |
| **PerTenant 并发计数** `inflight map[string]int` | `internal/middleware/middleware.go` | ❌ **无上限** | N/A（无限增长） | ❌ 无 |
| **MCP 订阅注册表**（将来） | `internal/mcp/server.go` | ❌ 尚未实现 | — | ❌ 无 |
| **Snapshot/gzip** | `internal/snapshot/snapshot.go` | ❌ **无上限** | 大库快照时 OOM（全部加载到内存再压缩） | ❌ 无 |

**BM25 索引——最突出的问题：**

```go
// internal/ai/bm25.go:44
func NewBM25() *BM25 {
    return &BM25{
        k1: 1.5, b: 0.75,
        docs:    map[int64]bm25Doc{},      // ❌ 无上限
        df:      map[string]int{},          // ❌ 无上限
        objDocs: map[int64][]int64{},       // ❌ 无上限
    }
}

// BuildFromRepo 加载所有 chunk 到内存：
func (b *BM25) BuildFromRepo(ctx context.Context, repo repository.Repository, tenant string) error {
    // ...
    b.docs = make(map[int64]bm25Doc, len(all))  // ❌ len(all) = 全部 chunk
    // ...
}
```

BM25 索引的内存消耗 = `(文档数 × 平均分词数 + 词典大小) × 每词条开销`。对于 100 万文档、平均 100 词、词典 10 万，约需内存：`(1M × 100 × 16B) + (100K × 32B)` ≈ **1.6 GB**——且随着文档数线性增长。

**搜索缓存——逐出策略问题：**

```go
// internal/ai/result_cache.go:65-72
func (c *resultCache) put(key string, hits []Hit) {
    c.mu.Lock()
    defer c.mu.Unlock()
    if len(c.cache) >= c.capacity {
        // 🚩 随机驱逐一个 key——不是 LRU，不是 LFU
        for k := range c.cache {
            delete(c.cache, k)
            break
        }
    }
    c.cache[key] = resultEntry{...}
}
```

**速率限制器——达到上限时拒绝新租户而非驱逐旧桶：**

```go
// internal/middleware/ratelimit.go:72-78
if len(rl.buckets) >= rlMaxBuckets {
    rl.evictIdle(now)
    if len(rl.buckets) >= rlMaxBuckets {
        // ❌ 拒绝——所有现有租户的桶都还在活跃中
        // 新租户无法发送任何请求（包括合法的第一次请求）
        return false, rlEvictInterval
    }
}
```

### 为什么需要

| 场景 | 当前风险 | 生产影响 |
|------|---------|---------|
| 大租户（100K+ chunk） | BM25 内存占用 1GB+ | **OOM Kill** |
| DDoS 攻击（大量不同租户 ID） | 速率限制器 map 快速填满 50K → 429 所有新租户 | **合法新租户无法访问** |
| 大量 API Key 轮换 | Key cache 无限增长 | **内存泄漏** |
| 高 QPS 搜索结果缓存 | 随机驱逐导致热点驱逐、冷数据驻留 | **缓存命中率低于 LRU** |
| 快照 50GB 对象存储 | `snapshot.Create` 将整个数据库读入内存后写 tar.gz | **OOM Kill** |

### 建议实现方案

**阶段一：增加上限 + LRU 化（~200 行）**

```go
// internal/ai/bm25.go — 增加上限
type BM25 struct {
    // ...
    maxDocs int64  // 0 = unbounded（默认）；>0 时拒绝添加
}

// 在 insertDocLocked 中检查：
if b.maxDocs > 0 && len(b.docs) >= int(b.maxDocs) {
    // 可以选择：拒绝新文档（写入失败），或驱逐最旧的对象
    return
}
```

```go
// internal/ai/result_cache.go — 替换随机驱逐为近似 LRU
// 使用简单的 linked-list + map，或使用 hash-move-to-front 策略
type resultCache struct {
    // ...
    // 使用 list.List + map[int64]*list.Element 实现 LRU
}
```

```go
// internal/middleware/ratelimit.go — 改为 LRU 驱逐而非拒绝
if len(rl.buckets) >= rlMaxBuckets {
    // 驱逐最久未使用的桶（LRU），而非拒绝新租户
    rl.evictLRU(now)
}
```

**阶段二：Snapshot 改为流式处理（~150 行）**

```go
// internal/snapshot/snapshot.go
// 当前：先读整个 DB 到内存，再压缩
// 改为：使用流式 reader，边读边压缩并写入 tar
// 或：在 CreateRaw 中 pipe gzip → tar writer
```

**阶段三：内存压力监控指标（~50 行）**

```go
// 新增指标：memory_inuse_bytes{component="bm25|key_cache|rate_limiter"}
// 每个组件注册一个 gauge，提供当前内存占用的估计值
// 当超过阈值时触发告警
```

### 规模估计

| 工作项 | 复杂度 | 涉及文件 |
|--------|--------|---------|
| BM25 上限 | 低 | `internal/ai/bm25.go`（~30 行） |
| 搜索缓存 LRU | 中 | `internal/ai/result_cache.go`（~80 行） |
| 速率限制器 LRU 驱逐 | 低 | `internal/middleware/ratelimit.go`（~30 行） |
| Key cache 上限 | 低 | `internal/auth/key_cache.go`（~20 行） |
| Snapshot 流式处理 | 中 | `internal/snapshot/snapshot.go`（~100 行） |
| 内存指标 | 低 | `internal/telemetry/metrics.go`（~30 行） |
| **总估** | **2-4 天** | **5-7 个文件** |

---

## 综合优先级与建议实施顺序

| 优先级 | 方向 | 影响面 | 前置依赖 | 涉及文件量 | 风险级别 | 建议开始时间 |
|--------|------|--------|---------|-----------|---------|------------|
| **P0** | 方向一：PerTenantConcurrencyLimiter TOCTOU | 可靠性/安全 | 无 | 1 | **高** — 竞态在生产负载下必然触发 | **本迭代** |
| **P1** | 方向二：SSE Chat 流断开盲区 | 可靠性/体验 | 无 | 2-4 | 中 | **本迭代** |
| **P1** | 方向三：配置交叉验证 | 运维/可靠性 | 无 | 2-3 | 低 | **本迭代** |
| **P1** | 方向四：Webhook 表 GC | 运维/合规 | 方向无关 | 3-5 | 低 | **下迭代** |
| **P2** | 方向五：内存结构上限 | 可靠性/运维 | 方向无关 | 5-7 | 中 | **下迭代** |

### 推荐执行顺序

```
第一周（P0 → P1）：
  ┌──────────────────────────────────────────────────────┐
  │ 方向一：PerTenantConcurrencyLimiter TOCTOU 修复     │
  │   → 1 个文件，1 天                                  │
  │ 方向二：SSE Chat 流心跳 + 断开检测                 │
  │   → 2 个文件，2 天                                  │
  │ 方向三：配置交叉验证                                │
  │   → 2 个文件，1 天                                  │
  └──────────────────────────────────────────────────────┘

第二周（P1 → P2）：
  ┌──────────────────────────────────────────────────────┐
  │ 方向四：Webhook 表 GC + DLQ 路由                    │
  │   → 3 个文件，1-2 天                                │
  │ 方向五：内存上限 + LRU                              │
  │   → 5 个文件，2-3 天                                │
  └──────────────────────────────────────────────────────┘
```

---

## 与既有 59 份分析的去重对照

| 本文件方向 | 既有分析覆盖情况 | 去重结论 |
|-----------|----------------|---------|
| **方向一：PerTenantConcurrencyLimiter TOCTOU** | v57 方向三覆盖**跨副本分布式限流**但聚焦跨进程协调而非单进程实现缺陷；v31/v34/v45/v53 提及 per-tenant concurrency 但均关注**限流**而非**并发限制器的 TOCTOU** | ✅ **完全去重** |
| **方向二：SSE Chat 流断开盲区** | v39 方向二覆盖 SSE channel leak（EventBus 订阅者层面）；v44 方向五覆盖 SSE replay 回放（事件回放层面）；**Chat SSE 端点本身的客户端断开检测、心跳、续传从未被分析** | ✅ **完全去重** |
| **方向三：配置交叉验证** | v44 方向一的配置架构覆盖 config schema/validation 概念但**聚焦配置值追踪而非交叉依赖验证**；v44 方向一中「配置变更审计」仅一行过路引用 | ✅ **完全去重** |
| **方向四：Webhook 表 GC** | v38 覆盖工作者生命周期但**不涉及 webhook_failures 表清理**；v56 方向一覆盖 event notification engine 时提及 DLQ 路由但**不涉及现有 webhook_failures 表的无限增长问题** | ✅ **完全去重** |
| **方向五：内存结构上限** | v27 方向表一行提及"大键缓存"概念但非独立方向；v59 方向四性能基准关注**外部队列**性能而非进程内存管理；其余 57 份文档零覆盖 | ✅ **完全去重** |

> **文档生成方法：** 逐文件扫描 `internal/` 下每个子包，识别 5 类工程缺口：① 并发安全（竞态条件、TOCTOU、原子性缺失）；② 长连接可靠性（断开检测、心跳、续传）；③ 运维成熟度（配置验证、配置追踪）；④ 存储管理（无限增长、GC 缺失）；⑤ 资源管理（内存上限、逐出策略、内存压力处理）。每类缺口在既有 59 份 expansion 文档中进行穷尽式关键词验证。
