# AeroVault 架构盲区与灰度缺口 — 架构师视角（第 74 轮）

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描（246 `.go` 文件，~55K 行代码，`cmd/server/`，`internal/*`，24 对迁移文件，全套 SDK/CLI/MCP 体系）  
> **去重验证：** 对 `docs/requirements/` 下全部 73 份既有分析文档（`expansion-directions.md` ~ `expansion-v73-engineering-depth-gaps.md`）进行逐方向 `grep` 正则交叉验证，确保每个方向在既有分析中 **零实质性架构覆盖**  
> **日期：** 2026-07-10  
> **核心原则：** 选取代码中存在具体、可量化的工程空洞（缺失分支、配置无效果、实现不完整）且对生产可靠性/安全性/可运维性有显著杠杆作用的方向。每个方向均以代码锚点定位，不含模糊概念。

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 | 既有分析覆盖 |
|---|------|------|--------|---------|---------|-------------|
| **1** | **存储类（Storage Class）是僵尸功能：零操作语义，纯装饰** | 数据面/运维 | **P1** — `storage_class` 字段全程持久化、API 响应、x-amz-storage-class 请求头、专用 OTel 指标；但 GLACIER 与 STANDARD 存储行为完全相同，无任何层级化操作语义。用户设置 `StorageClass: GLACIER` 期待更低成本或归档行为，得到的是 STANDARD 的行为。这是一个"元数据陷阱"——数据记录存在但全链路无分支消费 | `internal/repository/sql_objects.go:33-50`（`storage_class` 全程读写）；`internal/service/file_crud.go:181`（`StorageClassOrDefault(opts.StorageClass)` 仅字符串传递）；`internal/telemetry/metrics.go:181-190`（`storage_class_objects` gauge 存在但仅计数，无任何行为关联）；`internal/storage/local_write.go`（存储路径全程不读取 `storage_class`）；`internal/storage/s3.go/oss.go/cos.go`（云后端也忽略 `storage_class`） | ❌ **零覆盖**（73 份文档中，v13/v40/v25 等曾以"存储分层/自动分层/生命周期分层"作为新功能方向提出，但从未以"当前 storage_class 是僵尸功能、零操作语义"作为数据完整性/可信性问题分析） |
| **2** | **Circuit Breaker 不区分故障类型：`ErrNotFound` 等合法 404 会错误触发熔断** | 可靠性/数据面 | **P2** — `circuitBreaker.recordOutcome` 将所有非 nil 错误均等计数为后端故障。`ErrNotFound`（对象不存在——合法操作状态）、`ErrInvalidKey`（客户端请求错误）、`ErrBackendUnavailable`（断路器自身，会造成自激循环）均不可区分地推高 failure counter。一个批量列出不存在的 key 的客户端就能触发熔断，使健康后端被错误标记为不可用 | `internal/storage/circuitbreaker.go:182-218`（`recordOutcome` 方法：任何 `err != nil` 都增加 `b.failures++` 和 `cb.failures++`）；`internal/storage/circuitbreaker.go:86-96`（`Stats()`——计数器含所有失败类型，无法区分）；`internal/storage/storage.go:22-24`（`ErrNotFound` / `ErrAlreadyExists` / `ErrInvalidKey` 均非存储后端故障，不应触发断路器） | ❌ **零覆盖**（v55 仅覆盖断路器可观测性——无 OTel 指标暴露；v57 覆盖 AI Provider 熔断缺失；均未分析断路器自身的"故障类型不分级导致错误熔断"问题） |
| **3** | **Postgres Event Transport 无连接生命周期管理：断连后静默失效** | 可靠性/分布式 | **P2** — `PostgresTransport.Publish` 每个调用都新建一个数据库连接（无连接池）；`Run` 的 LISTEN/NOTIFY goroutine 在连接断开后直接退出，无任何重连逻辑。一旦 PG 连接中断（网络抖动、PG 重启、连接池回收），跨副本事件同步在进程重启前永久静默失效。代码注释承认"best-effort"但使用者（EventBus、Replication、Webhook fanout）依赖其正确性 | `internal/events/postgres_transport.go`（全部代码：无重连、无连接池、无健康检查、无 graceful degradation）；`internal/events/postgres_transport.go:51-58`（`Publish`：每次调用新建 `pgx.Connect`）；`internal/events/postgres_transport.go:82-83`（无 `SetMaxOpenConns` 等 pool 设置）；`internal/events/postgres_transport.go:120`（`Run` 的 LISTEN goroutine：无 for+reconnect 循环，select 返回即退出）；`cmd/server/main.go:295-300`（`setupPostgresTransport`：无重连参数传递） | ❌ **零覆盖**（v27/v31/v38/v56 均在概念图上以一行列出"Postgres LISTEN/NOTIFY"，v38 仅分析 context.Background() 传递问题，v56 分析通知引擎缺失——**均未以独立方向分析 PostgresTransport 的连接生命周期管理、重连缺失和静默失效**） |
| **4** | **Webhook 出站投递无幂等性保证：同一事件可能重复投递多次** | 数据一致性 | **P2** — Webhook 退避重试循环在部分失败与下一轮轮询之间可能导致同一事件被多次投递。事件 `webhook_failures` 表无投递 ID/DedupeKey，下游系统收到重复事件时无法可靠地检测去重。重试至上限后的 dead-letter 策略与 succeeded 状态共用同一标记——无法区分"成功"与"已死信" | `internal/events/webhook.go:211-238`（`retryOne`：重试成功则 `MarkWebhookSucceeded` 标记完成，但部分失败（POST 成功但 response 非 2xx）与完全失败均可能被下一个 15 秒轮询重新拾起）；`internal/events/webhook.go:105-115`（`postOne`：失败后 `persistFailure` 写入下次重试时间，但同步重试与异步轮询之间存在竞态窗口）；`internal/events/webhook.go:230-232`（dead-letter：`MarkWebhookSucceeded` 复用 success 标记，致死后与成功不可区分）；`internal/repository/webhook_failures.go`（无 `dedupe_key` 或 `delivery_id` 字段） | ❌ **零覆盖**（73 份文档中无任何一篇分析 webhook 出站投递的幂等性/去重/重复投递问题） |
| **5** | **健康检查端点流于表面：不验证后端真实健康状态** | 运维/可靠性 | **P2** — `/healthz` 无条件返回 200 OK，不验证任何后端状态。`/readyz` 只做基本的 `repo.Ping()` + `store.Stat("@healthz/probe")`，但 Stat 一个不存在的 key 对健康的后端也会返回 `ErrNotFound`（而非系统级错误）。存储后端可能处于"部分降级"状态（高延迟、电路断路器半开、错误率飙升）但通过检查。断路器状态、事件总线健康、索引器健康、后台队列深度等关键运维信号完全不在健康检查范围内 | `cmd/server/main.go:184-186`（`/healthz`：`{"ok":true}` 硬编码，不依赖任何后端状态）；`cmd/server/main.go:170-180`（`readyzHandler`：只做 `repo.Ping()` + `store.Stat(nonExistentKey)`——Stat 不存在的 key 并不会返回系统级错误）；`internal/storage/storage.go`（无 `Health() error` 接口方法，无法让后端报告自身健康状态）；`internal/events/bus.go`（无健康探测方法）；`internal/ai/indexer.go`（无健康探测方法）；`internal/jobs/jobs.go`（无健康探测方法） | ❌ **零覆盖**（v15 跨后端存储方向中一行"健康检查聚合"作为多后端路由治理概念出现——非当前健康检查终端的全面性分析；v47 失败场景分析中提及"网络分区"但非健康检查端点分析） |

---

## 方向一：存储类（Storage Class）是僵尸功能——零操作语义，纯装饰

### 现状

`storage_class` 字段贯穿整个代码栈：从 HTTP 请求头（`x-amz-storage-class`）到请求解析，到元数据持久化，到 API 响应和 OTel 指标——但 **没有任何代码读取这个值并做出不同的行为**。

```
        写入路径                             读取路径
客户端 ──→ x-amz-storage-class: GLACIER      客户端 ←── storage_class: "GLACIER"
         ↓                                                 ↑
         请求解析（extra.go:35-40）                 响应序列化（handler.go）
         ↓ storage_class 存入 DB                        ↑ 从 DB 读取
         FileService.Put（file_crud.go:181）           存储类指标（metrics.go:181）
         ↓ StorageClassOrDefault              
         后端存储（local_write.go / s3.go）── 完全不读取 storage_class
```

**代码证据链：**

```go
// ① 接收方——Persisted（正确）
// internal/api/s3compat/handler.go:104-108
obj, err := h.svc.Put(r.Context(), ..., service.PutOptions{
    StorageClass: r.Header.Get("x-amz-storage-class"),  // ← 正确读取
})

// ② 传递方——无失落（正确）
// internal/service/file_crud.go:64-96
func (s *FileService) Put(ctx, ..., opts PutOptions) (Object, error) {
    // ...
    // opts.StorageClass 最终存入 DB（repository/sql_objects.go:40）
}

// ③ 存储层——完全不读取
// internal/storage/local_write.go:41-60
func (s *LocalStorage) Put(ctx, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, error) {
    // opts 只有 ContentType + Metadata
    // storage_class 被忽略
}

// internal/storage/s3.go:82-87
func (s *S3Storage) Put(ctx, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, error) {
    // 同样忽略 storage_class，所有对象 S3 Standard
}

// ④ OTel 指标——只计数，无行为
// internal/telemetry/metrics.go:181-190
// RegisterStorageClassGauge 注册 `storage_class_objects` gauge
// 按 storage_class 分组计数——但仅反映"什么类别的对象有多少"
// 不驱动任何自动化行为（无自动分层、无按类计费、无生命周期策略）

// ⑤ 生命周期——完全忽略 storage_class
// internal/reconcile/lifecycle.go
// 生命周期只检查 updated_at → expire_after_days
// 不检查 storage_class，不作分层转移
```

| 场景 | 用户设了 `GLACIER` | 实际行为 | 用户期望 |
|------|-------------------|---------|---------|
| 存储介质 | 同 STANDARD 本地磁盘/S3 | 无变化 | 归档介质，更低成本 |
| 检索延迟 | 毫秒级 | 无变化 | 分钟级恢复等待 |
| 存储成本 | 同 STANDARD | 同 STANDARD | 更低（~1/5） |
| 自动过期 | 同 STANDARD 生命周期配置 | 同 STANDARD | 可能的更长保留期 |
| 最小存储周期 | 无 | 同 0 | GLACIER 通常有 90 天最小存储期 |

### 为什么需要

| 理由 | 说明 |
|------|------|
| **数据可信度** | 用户设置 `x-amz-storage-class: GLACIER` 看到的响应确认 "GLACIER"——但账单和访问行为却是 STANDARD。这是静默的数据合约违反。在合规审计中（展示存储策略），这会是一个红旗 |
| **S3 兼容性合约** | S3 兼容协议的价值在于"迁移零修改"。用户迁移到 aero-vault 后若发现 storage class 无效果（尤其是 GLACIER 归档类的成本与性能预期），会立刻失去信任 |
| **功能完备度歧义** | 代码中存在 `storage_class` OTel 指标（`storage.class_objects`）和 `STORAGE_DEFAULT_CLASS` 配置选项——给运维人员造成"此功能已运作"的错觉 |
| **技术债务** | 未来要真正实现分层存储时，当前代码中 `storage_class` 的"假装实现"路径可能误导新开发者认为分层已就位。需要先消除歧义再构建正确行为 |

### 建议方向

**短期（可信度修复）：**
- 在 PutObject/HeadObject/ListObjects 的 S3 响应中返回 `x-amz-storage-class: STANDARD`（而非存储的用户值），或清晰的文档声明"storage class is informational only"
- OTel `storage_class` gauge 添加 `effective=false` label

**长期（实际分层）：**
- 每个 `Storage` 后端实现 `StorageClassSupported(class string) bool`
- 新增 `Storage.SetStorageClass(ctx, key, class) error`
- reconcile 生命周期支持 `transition` 动作：`STANDARD → GLACIER`（30 天后）
- 分层前校验最小存储周期（如 GLACIER 90 天）

---

## 方向二：Circuit Breaker 不区分故障类型——`ErrNotFound` 等合法错误触发错误熔断

### 现状

`circuitBreaker.recordOutcome` 将所有非 nil 返回值视为后端故障：

```go
// internal/storage/circuitbreaker.go:182-218
func (cb *circuitBreaker) recordOutcome(err error) {
    // ...
    b.total++
    if err == nil {
        // success: reset counters
        return
    }
    // ↓ 任何 err != nil 都被 count 为 backend failure
    b.failures++
    cb.failures++
    // ↓ 一旦达到阈值（默认 5），电路打开
    if cb.failures >= cb.cfg.FailureThreshold {
        cb.state = CBOpen   // 所有后续请求被 ErrBackendUnavailable 拒绝
    }
}
```

而 `storage.Storage` 接口定义了多种 error sentinel，它们**不代表后端不可用**：

| Error Sentinel | 含义 | 是否应触发断路器 |
|---------------|------|----------------|
| `ErrNotFound` | 对象不存在（常见：HEAD/GET 不存在的 key） | ❌——合法客户端错误 |
| `ErrAlreadyExists` | 对象已存在（conditional write） | ❌——应用层语义 |
| `ErrInvalidKey` | 非法的 key 格式（包含 `..`、空串等） | ❌——输入校验错误 |
| 网络超时 | TCP 连接超时 | ✅——后端/网络问题 |
| I/O 错误 | 磁盘读写出错 | ✅——后端问题 |
| HTTP 5xx | 云后端返回 503 等 | ✅——后端问题 |

**真实攻击向量：** 一个恶意/异常客户端持续 HEAD 一个不存在的 key 列表（如枚举模式 `HEAD /bucket/不存在{1..10000}`），断路器将记录 5 次 `ErrNotFound` → 打开 → 所有后续存储请求（包括合法用户的 PUT/GET）返回 `ErrBackendUnavailable`，造成 DoS。

### 代码锚点

```go
// internal/storage/circuitbreaker.go:86-96
func (cb *circuitBreaker) Stats() (state CBState, failures, total int) {
    // 返回 total 和 failures——但 failures 包含了 ErrNotFound 等非后端错误
    // 运维人员无法区分"真有 5 次后端失败"vs"5 次 key 不存在"
}

// internal/storage/circuitbreaker.go:196-218
// recordOutcome 被以下方法调用：
// - Put（circuitbreaker.go:233）
// - Get（circuitbreaker.go:240）
// - Stat（circuitbreaker.go:248） ← Stat 频繁返回 ErrNotFound
// - Delete（circuitbreaker.go:255）
// - InitMultipart / UploadPart / CompleteMultipart / AbortMultipart / List
// 所有方法无差别对待所有 error
```

### 建议方向

```go
// 区分"可恢复的客户端错误"（不触发断路器）与"后端故障"（触发断路器）
func isBackendFailure(err error) bool {
    if errors.Is(err, ErrNotFound) ||
        errors.Is(err, ErrAlreadyExists) ||
        errors.Is(err, ErrInvalidKey) {
        return false
    }
    // 网络/IO/5xx 等均视为后端故障
    return true
}

func (cb *circuitBreaker) recordOutcome(err error) {
    // ...
    if err == nil {
        // success
        return
    }
    if !isBackendFailure(err) {
        // 客户端错误不计入熔断计数
        return
    }
    b.failures++
    cb.failures++
    // ...
}
```

---

## 方向三：Postgres Event Transport 无连接生命周期管理——断连后静默失效

### 现状

`PostgresTransport` 是整个系统跨副本事件同步的核心组件，但它的连接管理为零：

```go
// internal/events/postgres_transport.go:51-58
func (t *PostgresTransport) Publish(ctx context.Context, e repository.Event) error {
    payload, err := encodeEvent(e)
    // ...
    conn, err := pgx.Connect(ctx, t.dsn)  // ← 每次 Publish 新建连接！无连接池！
    // ...
    _, err = conn.Exec(ctx, "SELECT pg_notify($1, $2)", t.channel, payload)
    // ...
    conn.Close(context.Background())  // ← 用完即弃
}

// internal/events/postgres_transport.go:112-140
func (t *PostgresTransport) Run(ctx context.Context, deliver func(repository.Event)) error {
    conn, err := pgx.Connect(ctx, t.dsn)  // ← 单一连接
    // ...
    _, err = conn.Exec(ctx, "LISTEN "+t.channel)  // ← 唯一的 LISTEN 连接
    // ...
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        default:
        }
        notification, err := conn.WaitForNotification(ctx)  // ← 单点阻塞等待
        // ↓ 如果 conn 断开（网络抖动、PG 重启、空闲超时回收），
        //   WaitForNotification 返回 error，goroutine 直接退出
        //   无重连循环！
        if err != nil {
            return err
        }
        deliver(notification.Payload)
    }
}
```

**影响面：**

| 组件 | 依赖 PostgresTransport | 断连影响 |
|------|----------------------|---------|
| 多副本部署 | 跨副本事件广播 | 副本 B 上的事件对副本 A 不可见 |
| 副本间索引同步 | 事件触发索引 | 副本 A 上新增的对象，副本 B 的索引器不会知道 |
| 跨副本 Webhook | 事件触发 webhook | 副本 B 上发生的事件，副本 A 的 webhook 不会发送 |
| 跨副本 AV 扫描 | 事件触发扫描 | 同上 |

### 核心问题

| 问题 | 严重性 | 原因 |
|------|--------|------|
| 无连接池 | 中 | `Publish` 每个调用都建连 + 断连，高 QPS 下 PG 连接数飙升 |
| 无重连 | 高 | `Run` 的 LISTEN goroutine 不重连，一次断连后静默失败至进程重启 |
| 无健康检查 | 中 | 无任何指标/接口暴露 transport 连接状态 |
| 无 graceful degradation | 高 | 断连后不降级到轮询（`NextUnconsumedEvents`）或其他备选方案 |
| 无告警 | 中 | 断连无日志、无指标、无告警——运维完全不知情 |

### 建议方向

```go
// 最低修复：在 Run 中添加重连循环
func (t *PostgresTransport) Run(ctx context.Context, deliver func(repository.Event)) error {
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        default:
        }
        if err := t.listenOnce(ctx, deliver); err != nil {
            slog.Warn("postgres transport listen failed, reconnecting in 5s", "err", err)
            select {
            case <-ctx.Done():
                return ctx.Err()
            case <-time.After(5 * time.Second):
            }
        }
    }
}

// 扩展：Publish 改为重用连接池
// 使用 pgxpool 而非每次新建 pgx.Connect
```

---

## 方向四：Webhook 出站投递无幂等性保证——同一事件可能重复投递多次

### 现状

Webhook 投递通过 `RetryLoop` 每 15 秒轮询 `webhook_failures` 表中 `next_retry_at < now` 的行：

```go
// internal/events/webhook.go:166-183
func (w *Webhook) RetryLoop(ctx context.Context) {
    t := time.NewTicker(15 * time.Second)
    for {
        select {
        case <-ctx.Done():
            return
        case <-t.C:
            pending, err := w.repo.NextPendingFailures(ctx, 25)  // ← 每次取 25 条
            for _, f := range pending {
                w.retryOne(ctx, f)  // ← 尝试投递
            }
        }
    }
}
```

**竞态窗口导致重复投递：**

```
时间线:
T1: retryOne 发送 POST → 远程 200 OK（接收方已处理）
T2: 但 reply 在 HTTP 连接关闭前未被完全读取 ↓ ↓ ↓
     retryOne 检查 resp.StatusCode < 300 → true
     MarkWebhookSucceeded(f.ID) 被调用
T3: 然而，如果 T1 的 POST 成功但 response 读取超时/连接中断
     retryOne 走 else 分支 → persistFailure（写入下次重试）
T4: 15 秒后下一个轮询周期 → NextPendingFailures 再次拾取同一记录
T5: 同一事件被再次 POST 到下游

另一种场景：
T1: postOne 发送 POST → 远程返回 500
T2: persistFailure 写入 next_retry_at = now + 30s（退避后）
T3: 30 秒后 RetryLoop 拾取并重试 → POST 成功（远程恢复）
T4: 但前一次失败扣款/扣库存/创建记录已经生效
```

**更严重的是 dead-letter 与成功状态不可区分：**

```go
// internal/events/webhook.go:230-232
if attempts >= 10 {
    // 用 MarkWebhookSucceeded 标记死亡——但 succeeded 标识被复用
    // ListWebhookFailures 无法区分"已成功投递"与"投递 10 次失败后放弃"
    _ = w.repo.MarkWebhookSucceeded(ctx, f.ID)
}
```

### 缺少的保障层

| 保障 | 当前状态 | 行业基线 |
|------|---------|---------|
| 出站 Idempotency-Key | ❌ 无 | Webhook 标准建议每个事件携带唯一 `X-Aero-Delivery-ID` header |
| 去重表 | ❌ 无 | 接收方可检测重复 delivery_id |
| 至少一次语义强制 | ❌ 存在重复窗口 | 理论上至少一次，实践中可能多于一次 |
| 死信队列 | ❌ 复用 succeeded 标记 | 独立 DLQ 表或状态 |
| 投递审计 | ❌ 无 | 每次投递有 delivery_id + timestamp + 响应码 |

### 建议方向

```go
// 每个出站 POST 携带唯一 delivery ID
func (w *Webhook) postOne(ctx context.Context, eventID int64, url string, body []byte, sig string, attempt int) {
    deliveryID := uuid.NewString()  // ← 每次投递唯一 ID
    req.Header.Set("X-Aero-Delivery-Id", deliveryID)
    req.Header.Set("X-Aero-Delivery-Attempt", strconv.Itoa(attempt))
    // ...
}

// 接收方可使用 X-Aero-Delivery-Id 去重

// 死信队列独立标记
func (w *Webhook) deadLetter(ctx context.Context, f repository.WebhookFailure, lastErr string) {
    w.repo.MarkWebhookDeadLettered(ctx, f.ID, lastErr)  // ← 新状态，不与 succeeded 混淆
}
```

---

## 方向五：健康检查端点流于表面——不验证后端真实健康状态

### 现状

`/healthz` 和 `/readyz` 是 Kubernetes 等编排平台判断服务是否存活/就绪的唯一信号。当前实现极其脆弱：

```go
// cmd/server/main.go:184-186
r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
    w.WriteHeader(http.StatusOK)
    _, _ = w.Write([]byte(`{"ok":true}`))
    // 完全忽略所有后端状态：
    // - 即使存储后端已经熔断（CB open）
    // - 即使数据库无法连接
    // - 即使索引器阻塞
    // - 即使事件总线积压
    // → 仍返回 200 OK
})

// cmd/server/main.go:170-180
func readyzHandler(repo repository.Repository, store storage.Storage) http.HandlerFunc {
    return func(w http.ResponseWriter, req *http.Request) {
        if err := repo.Ping(req.Context()); err != nil {
            http.Error(w, "db: "+err.Error(), http.StatusServiceUnavailable)
            return
        }
        // 以下检查使用 Stat 一个不存在的 key
        // → 健康的后端返回 ErrNotFound（非错误）
        // → 不健康的后端也可能返回 ErrNotFound（如果后端是"返回 404"级别地失败）
        // → 无法区分"正常运行"与"部分降级"
        if _, err := store.Stat(req.Context(), "@healthz/probe"); err != nil && !errors.Is(err, storage.ErrNotFound) {
            http.Error(w, "storage: "+err.Error(), http.StatusServiceUnavailable)
            return
        }
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{"ok":true}`))
    }
}
```

**缺少的健康信号：**

| 组件 | 应检查什么 | 当前状态 |
|------|-----------|---------|
| 存储后端 | 断路器状态（CB open 时标记不健康）、延迟 p95 > 阈值 | ❌ 只做 Stat（404 无法区分后端健康与降级） |
| 数据库 | 连接池状态（可用连接数 < 最大连接数） | ❌ 只做 Ping |
| 事件总线 | subscriber 积压深度 | ❌ 无 |
| 后台队列 | pending 作业数或最旧作业等待时间 | ❌ 无 |
| 索引器 | 最后成功索引时间、积压对象数 | ❌ 无 |
| AI Provider | embedding/LLM 端点健康 | ❌ 无 |

### 为什么需要

| 场景 | 当前行为 | 应有行为 |
|------|---------|---------|
| 存储后端进入 CB Open（断路器打开） | `/healthz` 200 OK → K8s 保持流量 → 所有请求返回 503 | `/readyz` 返回 503 → K8s 停止路由流量 |
| 数据库连接池耗尽 | `/healthz` 200 OK → 新请求无法获取连接 | `/readyz` 返回 503 |
| 事件总线 subscriber 积压 10 万条 | `/healthz` 200 OK → 运维未获知 | 至少 `/readyz` 加入积压深度阈值检查 |
| RAG pipeline 中 embedder 端点不可用 | `/healthz` 200 OK → 用户搜索返回 500 | 非致命（AI 降级可接受），但健康检查应报告组件状态 |

### 建议方向

```go
// 后端实现 Health() 接口
type HealthChecker interface {
    Health(ctx context.Context) HealthStatus
}

type HealthStatus struct {
    Status    string            // "ok", "degraded", "unavailable"
    Latency   time.Duration
    Details   map[string]string
}

// Storage 接口增加 Health() 方法
type Storage interface {
    // ...
    Health(ctx context.Context) HealthStatus
}

// circuitBreaker 的 Health() 应报告断路器状态
func (cb *circuitBreaker) Health(ctx context.Context) HealthStatus {
    state, failures, total := cb.Stats()
    if state == CBOpen {
        return HealthStatus{Status: "unavailable", Details: map[string]string{
            "state": "open", "failures": strconv.Itoa(failures),
        }}
    }
    if failures > 0 {
        return HealthStatus{Status: "degraded", Details: map[string]string{
            "state": state.String(), "failures": strconv.Itoa(failures),
        }}
    }
    return HealthStatus{Status: "ok"}
}
```

---

## 关于既有分析的去重声明

上述五个方向全部经过 `docs/requirements/` 下全部 73 份既有分析文档的逐方向 `grep` 正则交叉验证：

| 方向 | 验证方式 | 结果 |
|------|---------|------|
| **方向一：Storage Class 僵尸功能** | `storage_class.*zombie\|storage_class.*noop\|zombie.*feature\|class.*no.tier\|storage_class.*meaningless` → 零命中。v13/v40/v25 以"存储分层"作为新功能提出，未分析当前的僵尸状态 | ✅ 完全去重 |
| **方向二：Circuit Breaker 故障类型分级** | `breaker.*404\|breaker.*ErrNotFound\|breaker.*type\|failure.*discriminat\|breaker.*false.*positive` → 零命中。v55 覆盖断路器可观测性，v57 覆盖 AI 熔断缺失，均未分析故障类型分级 | ✅ 完全去重 |
| **方向三：PostgresTransport 连接生命周期** | `postgres.*transport.*reconnect\|postgres.*transport.*connection\|pg_notify.*reconnect\|transport.*resilien` → 零命中。v27/v31/v38/v56 以概念提及，仅 v38 分析 context.Background()，非连接管理 | ✅ 完全去重 |
| **方向四：Webhook 出站幂等性** | `webhook.*idempot\|webhook.*dedup\|webhook.*at.most\|duplicate.*delivery\|outbound.*idempot` → 零命中 | ✅ 完全去重 |
| **方向五：健康检查深度** | `healthz.*superficial\|health.*check.*comprehens\|readiness.*depth\|health.*backend.*integrity` → 零命中。v15 以跨后端路由治理上下文提及"健康检查聚合"——非当前健康检查终端的全面性分析 | ✅ 完全去重 |
