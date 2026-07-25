# 高价值扩展方向分析 v28 — 多协议一致性与系统隐式债务

> **分析范围：** 全代码库扫描（`cmd/server/`、`internal/*` 共 237+ 个 `.go` 文件、`sdk/*`、`deploy/*`）
> **分析日期：** 2026-07-10
> **视角：** 资深架构师 / 产品经理 — 聚焦「多协议共存下的隐式架构债务」
> **原则：** 不编写任何实现代码。每个方向附带：现状 → 代码锚点 → 缺失能力 → 原理分析 → 边界情况。

---

## 本期方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 涉及组件 |
|---|------|------|--------|---------|---------|
| 1 | **Server-Side Copy（服务端拷贝）** | 功能缺失 | P0 — 所有协议均依赖 | 无拷贝操作=对象复制需客户端中转，大对象场景不可接受 | `service/`, `storage/`, `rest/`, `s3compat/` |
| 2 | **WebDAV Basic Auth 不兼容** | 互操作性 | P0 — 主流 WebDAV 客户端无法连接 | macOS Finder / Windows Explorer / Cyberduck 全部使用 Basic Auth，当前只支持 Bearer | `auth/auth_middleware.go`, `api/webdav/` |
| 3 | **Multipart Upload 遗弃与搁置生命周期** | 存储泄漏 | P1 — 长期运维债务 | 已初始化的分片上传无回收机制，云端存储成本持续增加 | `service/file_multipart.go`, `reconcile/job.go` |
| 4 | **事件总线背压不可观测与公平性缺失** | 可靠性 | P1 — 高负载下静默丢事件 | 订阅者掉队时事件静默丢弃，无公平调度，无慢消费者隔离 | `events/bus.go`, `telemetry/metrics.go` |
| 5 | **跨协议条件写覆盖不全** | 数据安全 | P1 — 并发写入数据竞争 | 只有 REST 实现 If-Match/If-None-Match；S3/WebDAV 条件写缺失 | `service/file_crud.go`, `s3compat/`, `webdav/` |

---

## 1. Server-Side Copy（服务端拷贝）

### 现状

当前系统**没有任何服务端拷贝操作**。对象从一个键复制到另一个键，都必须通过客户端执行完整的下载-上传序列：

```text
GET /v1/files/{key}     → 完整数据经网络到客户端内存
PUT /v1/files/{newKey}  → 再完整上传回去
```

**代码锚点：**

```
internal/api/rest/router.go          — REST 路由：无 /copy 或 /files/*/copy 端点
internal/api/s3compat/handler.go     — S3 handler 未处理 x-amz-copy-source（COPY 动词）
internal/api/s3compat/router.go      — S3 路由：未注册 COPY/UploadPartCopy
internal/api/webdav/dav.go           — WebDAV handler：未实现 COPY/MOVE（Rename 已实现）
internal/service/file_crud.go        — FileService：仅 Put/Get/Delete，无 Copy 方法
internal/storage/storage.go          — Storage 接口：无 Copy 方法
internal/storage/{local,s3,oss,cos}.go — 各后端本地均无 Copy 实现
```

### 缺失能力矩阵

| 能力 | 现状 | 需要 |
|------|------|------|
| REST `/v1/files/{key}/copy` | 不存在 | 同租户内跨 key 拷贝 |
| S3 `PUT` + `x-amz-copy-source` | 未处理 | S3 COPY 接口，支持源版本 ID |
| S3 `UploadPartCopy` | 未实现 | 分片上传时从已有对象拷贝部分 |
| WebDAV `COPY` 方法 | 未实现 `davFS` | 虽 `x/net/webdav` 本身支持，但当前 `OpenFile` 只有只读/写入模式 |
| 元数据指令 | 不存在 | `x-amz-metadata-directive: COPY/REPLACE` |
| 条件拷贝 | 不存在 | `x-amz-copy-source-if-match/-if-none-match/-if-unmodified-since` |
| 存储类指定 | 不存在 | `x-amz-storage-class` 覆盖目标存储类 |
| 跨后端拷贝 | 不存在 | Local→S3、S3→OSS 的存储层透明拷贝 |

### 原理分析

**为什么没有 Copy？** 架构历史上 `FileService` 是围绕对象 CRUD 设计的（Put/Get/Delete），Copy 被认为是"便捷操作"而非核心原语。但在 S3/WebDAV 生态中，COPY 是高频操作：

- **S3 生态**：awscli `cp`/`mv`、s3cmd、rclone 均通过 COPY 动词操作。无 COPY = 这些工具退化为下载+上传，带宽翻倍、延迟激增
- **生命周期**：S3 生命周期通过 COPY 实现存储类转换（STANDARD→STANDARD_IA→GLACIER）。当前只能过期删除，无法降冷
- **跨区域复制**：CRR 核心机制是 COPY 到目标区域
- **元数据更新**：S3 无 PUT 原地覆盖元数据的 API，只能通过 COPY 到自身（`x-amz-metadata-directive: REPLACE`）实现
- **WebDAV**：`COPY`/`MOVE` 是 RFC 4918 核心方法，当前 `davFS.Rename` 使用客户端 Get+Put 模拟，大文件场景不可用

**架构入侵度：**

```text
Storage.Copy(ctx, srcKey, dstKey, opts) → ObjectInfo (新接口)
  ├─ Local 实现: os.Rename 或 io.Copy（同后端）
  ├─ S3 实现:  S3 CopyObject API（跨 region 时用 CopyObject 或 multipart copy）
  ├─ OSS 实现: OSS CopyObject API
  ├─ COS 实现: COS CopyObject API
  └─ 跨后端:   Get(src) → Put(dst) 流式桥接（存储层负责，不经过 handler）

FileService.Copy(ctx, t, bucket, src, dst, opts) → Object
  ├─ 检查源存在、锁、LegalHold
  ├─ 预检目标配额
  ├─ 委托 storage.Copy（同后端）或 storage.Get+Put（跨后端）
  ├─ 处理元数据指令 (COPY|REPLACE)
  ├─ 写入 repo (UpsertObject / InsertObjectVersion)
  ├─ 更新标签、存储类
  └─ 发布事件

REST: POST /v1/files/{key}/copy  {source_key, ...}
S3:  PUT /{bucket}/{key} + x-amz-copy-source
WebDAV: COPY /{prefix}/{key}  Destination: /{prefix}/{newKey}
```

**边界情况：**
- **源=目标**（原地元数据/存储类更新）→ 跳过物理拷贝，直接更新元数据
- **大对象 >5GB** → S3 要求使用 multipart copy（UploadPartCopy）。当前连单次 COPY 都无，缺口更大
- **SSE-C/SSE-KMS 加密对象** → COPY 必须带上加密上下文，否则后端拒绝
- **版本化 Bucket** → 拷贝到已存在 key 时，行为取决于 Versioning：应创建新版本而非覆盖
- **跨租户拷贝** → 需要授权模型扩展（当前不允许跨租户操作）
- **原子性** → Copy + Delete 组合用于 Move。如果 Delete 失败，应回滚 Put（类似 `davFS.Rename` 的 copy-then-delete + rollback）

### 为什么需要

| 理由 | 影响 |
|------|------|
| **S3 兼容性断裂** | S3 COPY 是 AWS S3 核心 API；缺失意味着 awscli/s3cmd/rclone 无法正常工作 |
| **带宽和延迟** | 大对象经客户端中转，延迟增加 10-100 倍，浪费服务器带宽和内存 |
| **存储类转换** | 无 COPY → 无法实现 STANDARD→IA/Glacier 的自动降冷，存储成本不可优化 |
| **WebDAV 兼容** | COPY/MOVE 是 WebDAV 核心方法，当前 Rename 的 get+put+delete 在大文件上不可用 |

---

## 2. WebDAV Basic Auth 不兼容（Standard WebDAV Clients Cannot Authenticate）

### 现状

在深入分析中间件链后，可以确认：**WebDAV 请求经过了完整的中间件链**（Auth、Tenant、RateLimit、OTel 等），但它与标准 WebDAV 客户端的认证机制存在根本不兼容。

**中间件链执行顺序**（`applyMiddleware` 在 `buildRouter` 外层包装）：

```go
// cmd/server/main.go line ~230
finalHandler := applyMiddleware(dispatcher, authReg, rl, cfg, logger, concurrencyMW)

// applyMiddleware 包裹顺序 → 执行顺序（外到内）：
// AccessLog → Concurrency → Recoverer → OTel → RateLimit → Tenant → Auth → CORS → RequestID
//                                                                   ↑
//                                                        dispatcher 在此内部拦截 WebDAV 路径
```

WebDAV 请求完整路径：
1. ✅ `RequestID` — 注入 trace ID
2. ✅ `CORS` — 设置跨域头
3. ✅ **`Auth`（`authReg.Middleware()`）** — 解析 `Authorization` 头，提取 Bearer/ApiKey token
4. ✅ `Tenant` — 从 X-Aero-Tenant 头提取租户
5. ✅ `RateLimit` — 全局速率限制
6. ✅ `OTel` — HTTP 指标
7. ✅ `Recoverer` — panic 恢复
8. ✅ `ConcurrencyLimiter` — 并发控制
9. ✅ `AccessLog` — 请求日志
10. → `dispatcher` 拦截 `/webdav` 前缀 → `davH.ServeHTTP`

**关键问题：** `authReg.Middleware()` 中的 `extractToken` 只支持三种格式：

```go
// internal/auth/auth_middleware.go
func extractToken(r *http.Request) string {
    if h := r.Header.Get("Authorization"); h != "" {
        for _, prefix := range []string{"Bearer ", "ApiKey ", "bearer ", "apikey "} {
            // ...
        }
    }
    if h := r.Header.Get("X-Api-Key"); h != "" {
        return h
    }
    return ""
}
```

主流 WebDAV 客户端全部使用 **HTTP Basic Auth**：`Authorization: Basic base64(user:pass)`，该格式被 `extractToken` 忽略。结果：
- 认证开启时：所有 WebDAV 请求返回 `401 Unauthorized`，客户端无法连接
- 认证关闭时：WebDAV 可工作但无身份鉴别

**`isBypassPath` 不包含 WebDAV 前缀**，所以 WebDAV 请求未被豁免认证检查——这意味着 WebDAV 在 auth enabled 时对标准客户端**完全不可用**。

**有效但有限的变通方式：** `X-Api-Key` 头（一些 WebDAV 客户端支持自定义头），或使用 URL 查询参数中嵌入 token（非标准，有日志泄漏风险）。

### 代码锚点

```
internal/auth/auth_middleware.go:extractToken — 只识别 Bearer/ApiKey，不支持 Basic
internal/auth/auth_middleware.go:isBypassPath   — 不包含 WebDAV 前缀（正确行为）
internal/auth/auth_middleware.go:checkScope     — 明确处理 "PROPFIND"/"PROPPATCH" 为读操作（正确）
internal/api/webdav/dav.go:davFS.tenant         — 从 ctx 提取 Tenant（正确，由 Tenant middleware 注入）
cmd/server/main.go:buildDispatcher              — WebDAV 在 dispatcher 内部分发
```

### 缺失能力矩阵

| 能力 | 现状 | 需要 |
|------|------|------|
| Basic Auth 支持 | 不支持 | 支持 `Authorization: Basic base64(username:password)`，映射到 API Key 或 JWT |
| Digest Auth 支持 | 不支持 | macOS Finder 在某些网络配置下使用 Digest |
| 匿名 WebDAV 读 | 无 | 当 `ANONYMOUS_PUBLIC_READ` 启用的只读场景 |
| WebDAV 资源级 ACL | 无 | 以目录为粒度的访问控制 |
| 租户/用户映射 | 无 | 每个 Basic Auth 用户映射到特定 Tenant |

### 原理分析

**认证流程应有的修正路径：**

```text
Auth middleware 收到 Authorization: Basic base64(user:pass)
  → 解析出 user:pass
  → user 作为 API Key / JWT 在 Registry 中查找
    → 方案 A（推荐）：在 AUTH_KEYS 或持久化 key 中增加 WebDAV 专用条目，
      user=token, pass=token（客户端将 token 作为密码输入）
    → 方案 B：user@tenant 模式，解析租户信息
    → 方案 C：独立的 WebDAV 用户/密码存储（类似 htpasswd）
  → 通过 → 注入 Tenant 上下文 → 进入 WebDAV handler
  → 失败 → 401 + WWW-Authenticate: Basic realm="aero-vault"
```

**注意：** `checkScope` 已正确处理 PROPFIND/PROPPATCH 为读操作，Mkdir 等为写操作。所以 auth 层就位后，scope 校验自动生效。

### 边界情况

- **macOS Finder 的 WebDAV 实现有已知 bug**：它会在 PROPFIND 前发送不带认证头的 OPTIONS 请求（用于能力嗅探），随后发送带认证的请求。Auth middleware 在不带认证的 OPTIONS 上必须返回 401 让客户端弹出密码框，或者通过 CORS 预检路径
- **密码中包含 `:`**：Basic Auth 解码需按第一个 `:` 分割，密码本身可包含 `:`
- **空密码**：一些 WebDAV 客户端发送空密码，需妥善处理
- **连接池/Keep-Alive**：Authenticated 和 Unauthenticated 请求可能复用同一连接，需确保中间件不缓存认证状态

### 为什么需要

| 理由 | 影响 |
|------|------|
| **用户可达性断裂** | WebDAV 是 aero-vault 的四大协议之一，但 macOS/Windows 原生客户端无法连接（且没有除 curl 外的合理 fallback） |
| **产品体验崩塌** | 用户配置 WebDAV 后看到的不是文件列表而是"验证失败"，第一印象灾难 |
| **生态工具链断裂** | Cyberduck、rclone webdav、davfs2 等全部依赖 Basic Auth |
| **安全风险** | 为了绕过认证，运维人员可能关闭全局认证，反而降低整体安全性 |

---

## 3. Multipart Upload 遗弃与搁置生命周期（Orphaned Multipart Upload Lifecycle）

### 现状

当前 multipart upload 的生命周期管理只有三个操作：
1. `InitMultipart` → 创建上传会话，在 repo 和 storage backend 中记录
2. `UploadPart` / `CompleteMultipart` → 正常完成路径
3. `AbortMultipart` → 客户端主动取消

**没有任何机制处理客户端启动上传后崩溃/断连的搁置上传。** 这些"遗弃"的分片上传会：

- 在存储后端（S3/OSS/COS）保留已上传的分片数据，持续产生存储费用
- 在 repository 的 `uploads` 表中留下僵尸记录
- 占用租户的 in-progress upload 计数（虽然当前计数未实现，但未来可能有限制）

**代码锚点：**

```
internal/service/file_multipart.go:InitMultipart   — 创建上传会话
internal/service/file_multipart.go:AbortMultipart   — 需客户端主动发起
internal/repository/sql_uploads.go:ListUploads      — 可以列出所有 in-progress 上传
internal/reconcile/job.go                           — 现有 reconcile sweeper，未覆盖 multipart
internal/repository/repository.go:Repository 接口   — 无清理 multipart 的方法
```

`reconcile.Job` 目前覆盖：
- 孤儿行（DB 有、存储无）✅
- 孤儿 blob（存储有、DB 无）✅
- 生命周期过期删除 ✅
- PII Scrub ✅
- **搁置 multipart 清理** ❌ — 完全未覆盖

### 缺失能力

| 能力 | 现状 | 需要 |
|------|------|------|
| 搁置分片 TTL | 无 | 自动清理超过 N 天未 complete 的分片上传 |
| Reconcile 集成 | 无 | `reconcile.Job` 增加 multipart 清理扫描 |
| S3 生命周期规则 | 无 | S3 兼容的 `AbortIncompleteMultipartUpload` 规则解析 + 执行 |
| 过期通知/告警 | 无 | 在大量分片被清理时通知管理员 |
| 存量清理 | 无 | 初始部署后遗留的旧分片清理 |
| 租户级配额关联 | 无 | 将 in-progress 分片纳入租户存储用量计算 |

### 原理分析

**S3 规范** 支持通过生命周期规则自动清理搁置分片：

```xml
<AbortIncompleteMultipartUpload>
    <DaysAfterInitiation>7</DaysAfterInitiation>
</AbortIncompleteMultipartUpload>
```

当前 `BucketConfig` 已有 `ExpireAfterDays`/`ExpireAction`，但仅针对已完成的**对象**。multipart 分片是独立的存储实体，需要独立的规则。

**实现路径：**

```text
新增 reconcile 子任务（每 sweep 周期执行）：
1. 列举所有 in-progress upload（repo.ListUploads）
2. 对每个 upload，检查 LastModified > TTL（默认 7 天）
3. 超期 → repo.GetUpload + storage.AbortMultipart + repo.DeleteUpload
4. 记录 telemetry 指标（reconcile.orphan_multipart_deleted_total）

或更灵活的方式：
5. 支持 S3 生命周期规则解析（AbortIncompleteMultipartUpload）
6. 不同 bucket 可设置不同 TTL
```

**边界情况：**
- **正在进行的分片上传**：如果客户端很慢但在持续上传分片，TTL 应从最后上传分片的时间重新计算——这需要跟踪每个 upload 的最新活动时间
- **大量搁置分片**：对于历史遗留的大量搁置上传，首次清理可能产生大量后端 API 调用。需分页、限速
- **分片独占锁**：如果 storage backend 不支持并发 abort（某些 s3 实现），需串行化
- **SQLite 并发**：reconcile 在 SQLite 上运行，大量删除可能导致 WAL 文件膨胀
- **版本化 Bucket**：分片上传过程中如果 bucket 开启了 versioning，storage_key 已包含版本后缀，清理不受影响

### 为什么需要

| 理由 | 影响 |
|------|------|
| **存储成本泄漏** | 搁置分片在云存储（S3/OSS/COS）上持续产生费用，且难以发现 |
| **运维盲区** | 管理员无任何手段发现/清理遗弃分片，唯一的办法是手动调用 S3 ListMultipartUploads |
| **S3 兼容性** | AWS S3 推荐通过生命周期规则管理搁置分片，这是最佳实践 |
| **配额失真** | 当前租户配额不包含 in-progress 分片的字节数，实际用量被低估 |

---

## 4. 事件总线背压不可观测与公平性缺失（Event Bus Backpressure & Fairness）

### 现状

事件总线 `events.Bus` 使用非阻塞的 fan-out 广播模式：

```go
// internal/events/bus.go
func (b *Bus) broadcast(e repository.Event) {
    b.mu.RLock()
    defer b.mu.RUnlock()
    for _, ch := range b.subs {
        select {
        case ch <- e:
        default:
            // subscriber backpressure: drop, the DB has it
            b.dropped.Add(1)
            telemetry.IncEventDropped(context.Background())
        }
    }
}
```

每个订阅者有一个固定深度的缓冲 channel（默认 64，可通过 `EVENTS_SUB_BUFFER_SIZE` 配置）。当订阅者处理速度跟不上事件产生速度时：

1. channel 满 → `select default` 分支 → **事件被静默丢弃**
2. 计数器 `b.dropped` 递增（可通过 `/metrics` 观测）
3. 但没有任何按订阅者的粒度指标——不知道**哪个**订阅者在丢事件
4. 一个慢订阅者不会影响其他订阅者（各自独立的 channel）——这是好的
5. 但没有公平队列：所有订阅者共享同一优先级，无法区分重要消费者（如 webhook）和不重要消费者（如 indexer）

**订阅者列表（按照 `main.go` 中的注册顺序）：**

| 订阅者 | 缓冲深度 | 重要性 | 消费速率特征 |
|--------|---------|--------|------------|
| Indexer | 64 | 高（丢失后可选重建） | 可能慢（需提取+分块+嵌入+写入） |
| Antivirus worker | 64 | 安全相关，丢失有合规风险 | 受扫描引擎延迟影响 |
| Replication worker | 64 | 高（丢失导致副本不一致） | 受目标存储延迟影响 |
| Webhook | 64 | 视配置而定 | 受目标 URL 延迟/可用性影响 |
| SSE stream（每个浏览器连接） | 64 | 低（浏览器的实时感） | 受客户端网络影响 |

**问题总结：**
- 所有订阅者共享相同缓冲深度，无法按重要性独立配置
- 丢失事件时无法区分是哪个订阅者掉队（`dropped` 是全局计数器）
- 没有背压信号传递给生产者（`FileService` 发布完就继续，不知道事件丢了）
- 没有慢消费者隔离机制（一个慢消费者不会影响其他消费者，这已经是当前架构的优点）
- 没有事件重放 API 供掉队的消费者批量追赶

### 代码锚点

```
internal/events/bus.go:55-75              — broadcast 实现，select default 丢弃
internal/events/bus.go:Dropped()           — 全局计数器，无订阅者标签
internal/events/bus.go:Subscribe()         — 固定 channel 深度
internal/events/bus.go:NewWithBuffer()     — 配置项只支持全局 subBuffer
cmd/server/main.go:initInfrastructure     — Bus 创建，子系统订阅
internal/telemetry/metrics.go:IncEventDropped — 只有全局事件丢弃计数
```

### 缺失能力矩阵

| 能力 | 现状 | 需要 |
|------|------|------|
| 按订阅者粒度的丢弃计数 | 无 | `events.dropped_total{subscriber="indexer"}` |
| 独立缓冲配置 | 固定 64 | `EVENTS_SUB_BUFFER_INDEXER=128`, `EVENTS_SUB_BUFFER_WEBHOOK=256` |
| 背压信号 | 无 | 高丢弃率时触发告警或降级 |
| 慢消费者告警 | 无 | channel 占用 >80% 持续 N 秒时告警 |
| 事件重放 API | 无 | 掉队消费者可通过 API `/v1/admin/events/replay?since=...` 追赶 |
| 订阅者优先级 | 无 | 高优订阅者使用更大的 channel + 专属 goroutine |
| 指标完善 | 全局计数 | + subscriber 维度的 emit/dropped/queue_depth gauge |

### 原理分析

事件丢失的实际情况：

```text
高写入负载（PUT 1000 obj/sec）→ 事件总线广播
  ├─ Indexer (缓冲64): 处理速度 50/sec → 每 2ms 满 → 每秒约丢 950 个事件
  ├─ Webhook (缓冲64): 处理速度 200/sec → 每秒约丢 800 个事件
  ├─ SSE (缓冲64): 空闲 → 不丢
  └─ 所有事件已写入 events 表 → 不丢失持久化
```

虽然事件的持久化副本在 `events` 表中安全（DB 写入在 `Publish` 时已做），但**订阅者丢失事件意味着异步处理链条延迟无限增长**：

- Indexer 靠事件驱动，丢失事件后需依赖 `drainBacklog` 轮询 `NextUnconsumedEvents` 来捡回。这引入了额外的 DB 查询延迟（默认 5 秒间隔）
- Antivirus/Replication/Webhook 没有轮询 fallback——丢失事件意味着处理永远延迟，直到下次重启或手动触发

**改进方向：**

```text
短期：增加订阅者级指标 + 独立缓冲深度
中期：增加 backpressure API（通知发布者降低写入速率）
长期：引入事件驱动的消费确认机制（类似 Kafka consumer group）
```

### 边界情况

- **大量 SSE 客户端（每个浏览器一个连接）**：每个连接是一个订阅者，缓冲深度再大也有限。1000 个 SSE 客户端意味着 1000 个 channel 和 1000 次广播。需要订阅者去重或 SSE 聚合层
- **订阅者 goroutine panic**：如果订阅者 panic，事件丢失且无重试
- **Bus.Close 时的竞态**：`broadcast` 中 `b.mu.RLock()` 不能防止 channel 被 `Close()` 关闭后的写入 panic（需要 `subs` 的读拷贝或 sync.Map）

### 为什么需要

| 理由 | 影响 |
|------|------|
| **事件驱动丢失不可接受** | Antivirus/Replication 依赖事件驱动，丢失=安全合规风险+副本不一致 |
| **运维盲区** | 管理员无法知道"我的异步管道是否健康"，只能看到全局一个丢弃数 |
| **吞吐瓶颈** | 当前 64 缓冲深度在中等负载下（100+ ops/sec）即溢出，对大文件场景（处理慢）更严重 |
| **无降级策略** | 系统高负载时只能丢事件，没有 graceful degradation |

---

## 5. 跨协议条件写覆盖不全（Conditional Writes Are Protocol-Fragmented）

### 现状

`FileService.Put` 在持久化前检查对象锁（`checkLockBeforeOverwrite`），但没有原子性的乐观并发控制。跨协议的条件写支持完全不一致：

**三个协议的规则矩阵：**

| 条件 | REST `/v1` | S3 `/s3` | WebDAV |
|------|-----------|---------|--------|
| `If-Match`（ETag 匹配才写） | ✅ 在 `handler.go:Put` 实现 | ❌ S3 规范要求但未实现 | ❌ WebDAV lock 系统不覆盖数据写入 |
| `If-None-Match`（ETag 不匹配才写） | ✅ 通过 `If-None-Match: *` 实现 create-only | ❌ S3 规范要求但未实现 | ❌ |
| `If-Modified-Since` / `If-Unmodified-Since` | ❌ 未实现 | ❌ S3 规范要求但未实现 | ❌ |
| `x-amz-object-lock-*` | 依托 LockedUntil 全协议保护 | 已实现 | ❌ davWriter.Close 直接 Put，无锁检查 |
| Legal Hold | 元数据检查 | 暂未暴露 | ❌ |

**具体代码锚点：**

```
internal/api/rest/handler.go:Put      — 前置检查 If-Match/If-None-Match（调用 Stat + checkWritePreconditions）
internal/api/s3compat/handler.go      — S3 PUT 没有条件检查（无 If-Match/If-Modified-Since）
internal/api/webdav/dav.go:davWriter.Close — 直接 svc.Put，无任何前置条件检查
internal/service/file_crud.go:Put    — 只有 checkLockBeforeOverwrite，无 ETag/时间条件
```

### 原理分析

条件写是分布式对象存储的**核心数据安全机制**。没有它：

- **丢失更新（Lost Update）**：
  ```
  Client A GET → ETag:"abc" → PUT(If-Match:"abc")  // REST 安全
  Client B GET → ETag:"abc" → PUT(If-Match:"abc")  // S3 无此检查 → 静默覆盖 A 的写入
  ```

- **Create-Only 违反**：
  ```
  Client A PUT /file (不存在) → 创建成功          // 期望
  Client B PUT /file (不存在, If-None-Match:*) → 创建失败  // 只有 REST 保护
  Client C PUT /file (无条件) → 静默覆盖 A        // S3/WebDAV 无保护
  ```

- **并发编辑冲突**：
  两个用户同时编辑同一文件后保存，后保存者静默覆盖前者。WebDAV 的锁系统（`MemLS`）提供一定的协作锁，但 x/net/webdav 的 `LockSystem` 默认是 advisory lock，不强制，且 WebDAV 写入路径（`davWriter`）不检查锁。

### 缺失能力矩阵

| 能力 | REST | S3 | WebDAV |
|------|------|----|--------|
| If-Match | ✅ handler | ❌ | ❌ |
| If-None-Match (*) | ✅ handler | ❌ | ❌ |
| If-Modified-Since | ❌ | ❌ | ❌ |
| If-Unmodified-Since | ❌ | ❌ | ❌ |
| x-amz-copy-source-if-match | N/A | ❌ (COPY 本身未实现) | N/A |
| WebDAV Lock 强制 | N/A | N/A | ❌ (advisory only) |
| 锁 + 条件写原子性 | ❌ | ❌ | ❌ |

### 建议实现位置

条件检查不应分散在各个 handler 中，而应统一到 **`FileService.Put`** 作为可选参数：

```go
type PutOptions struct {
    ContentType  string
    Metadata     map[string]string
    Tags         map[string]string
    ContentMD5   string
    StorageClass string
    // 新增条件参数（可选，nil 表示不检查）
    IfMatch           *string  // ETag 必须匹配；* = must exist
    IfNoneMatch       *string  // ETag 必须不匹配；* = must not exist
    IfUnmodifiedSince *time.Time
    IfModifiedSince   *time.Time
}
```

这样 REST、S3、WebDAV handler 只需要传入对应的条件，所有检查逻辑集中在 `service.Put` 方法内部。同时，`checkLockBeforeOverwrite` 也应并入这个检查框架。

### 边界情况

- **原子性检查**：条件检查和写入之间有时间窗口。对于本地存储 + SQLite（单事务）可以做到原子，但对于 S3（分两步），需要在应用层做 read-check-write 序列，无法保证原子性
- **空 ETag**：新上传的对象如何确定 ETag？REST PUT 返回 ETag，但 WebDAV 写入路径 `davWriter.Close` 不返回 ETag。WebDAV 场景下条件写需要对象存在状态（存在/不存在），而非 ETag 精确匹配
- **版本化 Bucket**：条件写应作用于当前最新版本，不检查历史版本
- **软删除对象**：软删除对象被视为不存在，条件写应正常工作

### 为什么需要

| 理由 | 影响 |
|------|------|
| **并发数据丢失** | 多协议多客户端并发写入时，后写者静默覆盖前者，无任何保护 |
| **S3 规范不达标** | If-Match/If-None-Match 是 S3 PUT 规范要求的基本能力 |
| **创建-更新混淆** | 无 create-only 语义，PUT 永远是 upsert，无法实现"不存在时才创建" |
| **一致性模型不统一** | 三种协议对同一数据提供不同的一致性保证，用户难以理解预期行为 |

---

## 附录：v28 与前 27 期去重矩阵

| 方向 | v1-27 覆盖状态 | 本期的独特增量 |
|------|---------------|---------------|
| Server-Side Copy | 零覆盖 | 首次系统分析 COPY 在 3 个协议中的完整缺失 + 架构方案 |
| WebDAV Basic Auth | 零覆盖（v23 提及 WebDAV 认证但未分析 Basic Auth 不兼容） | 首次从 Auth middleware 源码推导认证断裂原因 |
| Multipart Abandonment | 零覆盖 | 首次提出 multipart 生命周期管理作为独立方向 |
| Event Bus Backpressure | 零覆盖（v14/v15 提及事件总线改进但不同角度） | 首次从广播公平性+订阅者粒度指标+背压信号维度分析 |
| Conditional Writes | 零覆盖 | 首次系统比较三协议条件写覆盖情况 + 提出统一切入点 |
