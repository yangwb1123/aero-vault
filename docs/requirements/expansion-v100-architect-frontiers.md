# 架构级扩展方向：事件通知引擎、访问日志管线、存储分层状态机、跨协议策略评估、服务端 COPY 优化

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — 231+ Go 源文件，50 对迁移文件，3 套 SDK（Go/Python/JS），MCP 双模式（HTTP+stdio），Web UI，Grafana/Prometheus/OTel 配置，`AGENTS.md`，`ROADMAP.md`（已移除），`CHANGELOG.md`  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。选取**代码中存在明确实现锚点、但运行时管线断裂或不完整**的方向——即 API 层/持久化层/配置层均已就绪，但核心逻辑层缺失导致功能静默空转或行为不一致。每个方向包含：现状与代码证据 → 产品价值与典型场景 → 边界情况。

---

## 方法论：从 "看起来存在" 到 "真正可用"

本库已历经 99+ 轮扩展方向分析。本次不再重复"猜测哪些功能缺失"的范式，而是锚定在三类明确缺口：

| 缺口类型 | 判定标准 | 本扫描中的例子 |
|----------|---------|---------------|
| **管线断裂** | 配置可保存 + 接口可调用 + 数据已采集 → 但消费端完全无视 | 通知规则、访问日志配置 |
| **状态机残缺** | 标识字段存在 + 审计指标存在 → 但无状态转换逻辑、无驱动者 | `storage_class` 字段永不被生命周期变更 |
| **安全不对称** | 一个协议路径执行了安全检查 → 另一个路径完全绕过 | 桶策略在 S3 handler 执行，REST handler 不执行 |

选出的 5 个方向均非"新增功能"，而是**补齐已承诺但未兑现的合约**。

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 |
|---|------|------|--------|---------|---------|
| **1** | **桶级事件通知调度引擎：从单 URL 全局 Webhook 到多规则、多目标、可过滤的通知分发** | 功能完整性 / S3 兼容 | **P1** | S3 `?notification` API 完整实现并持久化规则（migration 0024），但事件总线**完全不读取这些规则**——全球仅一个 `EVENTS_WEBHOOK_URL` 在全量事件上运行 | `buckets.notification_rules` JSON 列（migration 0024），`repository.GetBucketNotifications`/`SetBucketNotifications` 完整，`events.Bus.Publish` → `broadcast` 无规则检查，`events.Webhook` 仅单 URL |
| **2** | **服务端访问日志管线：从死代码接口到可审计、可查询的请求日志流** | 合规 / 运维 | **P1** | `repository.WriteAccessLog` 有完整 SQL 实现，S3 `?logging` 端点完整 CRUD `LoggingConfig`，但**没有任何 handler/middleware 调用它**。这是全库最明显的管线段裂 | `WriteAccessLog(ctx, tenant, sourceBucket, method, key, status, latencyMs, userAgent)`（sql_buckets.go），`GetBucketLogging`/`SetBucketLogging`（file_features.go + handler.go），零调用处 |
| **3** | **存储类生命周期状态机：从二元过期到完整分层转换 + 冷存储恢复** | 成本优化 / 功能 | **P1** | `storage_class` 字段已存在（STANDARD/STANDARD_IA/GLACIER/DEEP_ARCHIVE），`StorageClassCounts` 已计数，`x-amz-storage-class` 可接收——但生命周期仅支持 `soft_delete`/`hard_delete`，无 STANDARD→IA→GLACIER 转换，无冷存储异步恢复工作流，`?restore` 只做软删除恢复 | `Object.StorageClass`（repository.go:34），`BucketConfig.ExpireAfterDays`/`ExpireAction`（repository.go:44-46），`lifecycle.go` 仅检查过期删除，`s3compat/handler.go:restoreObject` 调 `svc.RestoreObject`→`repo.RestoreObject`（仅 SET deleted_at=NULL），`Storage` 接口无反 RestoreObject 方法 |
| **4** | **跨协议桶策略统一评估：从 S3-only 到 REST/S3/WebDAV/MCP 全员一致** | 安全 / 多租户 | **P2** | `checkBucketPolicy` 在 `internal/auth/policy.go` 的 `Eval` 仅支持 `IpAddress`/`NotIpAddress` 条件 + `aws:SourceIp` 键，且仅在 `s3compat/handler.go` 的 `enforceBucketPolicy` 中调用——REST handler、WebDAV、MCP **均不调用**，任何通过 REST API 的请求完全绕过桶策略 | `auth.Policy.Eval`（policy.go），`s3compat/handler.go:enforceBucketPolicy` 在每个 S3 操作中调用，`rest/handler.go` 中零策略检查，`webdav/dav.go` 零策略检查，`mcp/server.go` 零策略检查 |
| **5** | **服务端 COPY 重写：从双倍流式 I/O 到存储内直接复制 + 元数据/存储类覆盖** | 性能 / 功能 | **P2** | 当前 S3 COPY 操作将对象从存储读出再写入（`Get` → `Put`），大对象完全缓冲于内存；不支持元数据更新（`x-amz-metadata-directive COPY`/`REPLACE`）、存储类覆盖（`x-amz-storage-class`）、条件复制（`x-amz-copy-source-if-*`）；同后端复制浪费网络和 CPU | `internal/api/s3compat/handler.go:CopyObject` → `svc.Get` + `svc.Put`（完整流传输），`storage.Storage` 接口无 `Copy(from, to) error` 方法，无元数据合并逻辑，无条件头解析 |

---

## 方向一：桶级事件通知调度引擎

### 现状与代码证据

**持久化层完整：** Migration 0024 在 `buckets` 表添加了 `notification_rules TEXT NOT NULL DEFAULT '[]'` 列（JSON 序列化的 `[]NotificationRule`）。Repository 层完整 CRUD：

```go
// internal/repository/repository.go:79-82
GetBucketNotifications(ctx, tenant, bucket) ([]NotificationRule, error)
SetBucketNotifications(ctx, tenant, bucket, rules) error
DeleteBucketNotifications(ctx, tenant, bucket) error
```

**API 层完整：** REST 和 S3 都能设置/读取规则：
- `PUT /v1/buckets/{bucket}/notification`（rest/handler.go:517-534）
- S3 `GET/PUT/DELETE /{bucket}?notification`（s3compat/handler.go:809-833）
- S3 侧完整解析 S3 通知 XML → `NotificationRule`（含 `TopicConfiguration`/`QueueConfiguration`/`LambdaFunctionConfiguration` 及 `Filter` 规则）

**但是，事件发布端完全忽略这些规则：**

```go
// internal/events/bus.go:90-100
func (b *Bus) Publish(ctx context.Context, e repository.Event) {
    // ... 持久化到 events 表
    b.broadcast(e)  // ← 直接广播给所有 subscriber，零规则检查
    // ... 如有 transport，也直接发布
}
```

`broadcast` 通知所有本地 subscriber（indexer、antivirus、replication workers、webhook）。Webhook 是单 URL 全局目标，不读取桶配置。这意味着一个用户配置了"只在 `s3:ObjectCreated:Put` 事件时通知队列 A，在 `s3:ObjectRemoved:*` 事件时通知队列 B"——这些规则被 SQLite/Postgres 完美存储，但**永不执行**。

**`NotificationRule` 结构体中的 `QueueArn`/`TopicArn`/`LambdaFunctionArn` 明确标记为 "unused, kept for compat"：**

```go
// internal/repository/repository.go:81-87
type NotificationRule struct {
    ID        string   `json:"Id"`
    Events    []string `json:"Events"`
    FilterKey string   `json:"FilterKey,omitempty"`
    QueueARN  string   `json:"QueueArn,omitempty"` // webhook URL or queue ARN
    TopicARN  string   `json:"TopicArn,omitempty"` // unused, kept for compat
    LambdaARN string   `json:"LambdaFunctionArn"`  // unused, kept for compat
}
```

### 产品价值与典型场景

- **S3 迁移用户**期望 `?notification` 像 AWS S3 一样工作——配置后事件自动推送到目标。当前规则静默存储但不触发，用户无法诊断
- **事件驱动工作流**：对象上传 → 通知处理服务触发转码 → 新对象写入触发下一阶段
- **多目标路由**：审计事件 → 归档队列，错误事件 → 告警通道

### 架构权衡与建议方向

- 在 `Bus.Publish` 或 `broadcast` 处插入一个 `NotificationDispatcher`，它：
  1. 查询事件所属桶的 `notification_rules`
  2. 对每条规则匹配 `Events[]` 和 `FilterKey`
  3. 命中后将事件推送到规则指定的目标（当前仅 `QueueARN` 支持 HTTP webhook）
- `QueueARN` 当前是字符串字段——可以扩展为支持 `https://...`（直接 webhook）和 `arn:aws:sqs:...`（→ 通过 SQS-compat 桥接）
- `TopicARN`/`LambdaARN` 暂作为占位符保持兼容

### 边界情况

| 场景 | 处理 |
|------|------|
| 规则配置后第一个事件 | 立即检查规则——不需要预热或缓存 |
| 过滤器前缀冲突（`prefix=foo/` + `prefix=fo/`） | 前缀最长匹配，平局时两条规则都触发 |
| 目标不可达 | 复用现有 `WebhookFailure` 重试机制；失败不阻断业务 |
| 无规则时性能 | 零额外开销——`GetBucketNotifications` 返回空的 `[]` 时跳过循环 |
| 事件类型映射 | S3 `s3:ObjectCreated:Put` ↔ `repository.EventCreated`；`s3:ObjectRemoved:Delete` ↔ `repository.EventDeleted` |

---

## 方向二：服务端访问日志管线

### 现状与代码证据

**Repository 层有完整实现，但零调用处：**

```go
// internal/repository/sql_buckets.go:368-370
func (s *sqlStore) WriteAccessLog(ctx context.Context, tenant, sourceBucket, method, key, status, latencyMs, userAgent string) error {
    // ... 完整 SQL INSERT → access_logs 表
}
```

**API 层完整 CRUD：**

```go
// REST: GET/PUT/DELETE /v1/buckets/{bucket}/logging（rest/handler.go）
// S3: GET/PUT/DELETE /{bucket}?logging（s3compat/handler.go）
// Service层: GetBucketLogging/SetBucketLogging/DeleteBucketLogging（file_features.go）
```

**但全库 grep 无任何地方调用 `WriteAccessLog`。**

没有 middleware 在请求处理完毕后收集 `(tenant, bucket, method, key, status_code, latency_ms, user_agent)` 并写入日志。这意味着：

- PCI/SOC2 审计要求"记录每次对象读取"无法满足
- 运维无法查询"谁在过去 24 小时读取了文件 X"
- S3 `?logging` 配置看起来可用但实际不产生日志

**并发问题：** 如果每个请求同步写日志，高吞吐路径（GET 大文件、List 大量对象）会增加写入延迟。需要异步缓冲写入。

### 产品价值与典型场景

- **合规审计**：金融机构需要记录每次对象访问（谁、何时、哪个 IP、什么操作）
- **用量分析**：按桶/按操作统计访问模式，优化存储策略
- **安全取证**：入侵后追溯"攻击者读取了哪些敏感对象"

### 架构权衡与建议方向

1. **接入点**：在 `middleware.AccessLog` 中收集请求结束后数据，通过 channel 或 job queue 异步写
2. **存储目标**：写入 `access_logs` 表（按桶分表或分区），或写入对象存储（`WriteAccessLog` 当前在目标桶创建日志对象）
3. **缓冲与批量**：使用 1KB 或 100ms 窗口批量写入，避免每个请求单次 I/O
4. **采样与跳过**：健康检查请求（`/healthz`、`/readyz`）和 metrics 路径应跳过日志记录

### 边界情况

| 场景 | 处理 |
|------|------|
| `?logging` 目标桶是自身 | 拒绝配置——防止递归写入 |
| 日志写入失败 | 降级为 warn log，不阻断源请求 |
| 日志桶超过配额 | `WriteAccessLog` 需要绕过配额检查（硬编码豁免） |
| 日志对象生命周期 | 日志桶应自动应用过期策略，或由 reconcile 清理 |

---

## 方向三：存储类生命周期状态机

### 现状与代码证据

**`storage_class` 字段已存在：** `repository.Object.StorageClass`（`repository.go:34`）是字符串字段，默认 `STANDARD`。持久化到 SQL 的 `storage_class` 列（`sql_objects.go:33`），在查询中全字段返回。

**StorageClassCounts 已实现：**
```go
// internal/repository/sql_objects.go:338-340
func (s *sqlStore) StorageClassCounts(ctx context.Context, tenant string) (map[string]int64, error) {
    // SELECT COALESCE(storage_class,'STANDARD'), COUNT(1) FROM objects ... GROUP BY storage_class
}
```

**PUT 接受存储类：** `x-amz-storage-class` 头被解析并传递（`s3compat/handler.go` → `service.PutOptions.StorageClass` → `Object.StorageClass`）

**但生命周期仅支持二元过期：**

```go
// repository.go:44-46
type BucketConfig struct {
    ExpireAfterDays int    // 过期天数
    ExpireAction    string // "soft_delete" | "hard_delete"
}
```

`internal/reconcile/lifecycle.go` 的 `Run` 循环通过 `ListExpired` 查询已过期的对象，然后仅对它们执行 `SoftDeleteObject` 或 `HardDeleteObject`——没有任何 `STANDARD→STANDARD_IA→GLACIER→DEEP_ARCHIVE→DELETE` 的渐进式转换。

**`?restore` 端点语义错误：**
```go
// internal/api/s3compat/handler.go:883-889
func (h *Handler) restoreObject(...) {
    h.svc.RestoreObject(ctx, tenant, bucket, key) // ← 仅清除 deleted_at
}
```
S3 用户期望 `POST ?restore` 从冷存储（GLACIER/DEEP_ARCHIVE）恢复对象到可读状态，并提供临时副本和到期时间。当前实现仅做软删除恢复。

**Storage 接口无法表达冷存储操作：**
```go
// internal/storage/storage.go
type Storage interface {
    Get(ctx, key) (io.ReadCloser, ObjectInfo, error)
    Put(ctx, key, r, size, opts) (ObjectInfo, error)
    Delete(ctx, key) error
    // 无 RestoreObject(ctx, key, days) error
    // 无 TransitionObject(ctx, key, targetClass) error
}
```

### 产品价值与典型场景

- **成本优化**：30 天前的日志自动从 STANDARD 转为 STANDARD_IA（降低成本 50%），90 天后转为 GLACIER（降低成本 80%）
- **归档合规**：金融数据强制 7 年后转为 DEEP_ARCHIVE，10 年后删除
- **S3 迁移**：用户期望 `LifecycleConfiguration` 中的 `Transition` 规则工作（当前只支持 `Expiration`）

### 架构权衡与建议方向

1. **转化规则模型**：扩展 `BucketConfig.ExpireAction` 为支持 `transition_to_standard_ia`、`transition_to_glacier`、`transition_to_deep_archive` 等动作——或定义为独立的事件对象（`Transition` + `Expiration`）
2. **转化执行者**：新的 job handler (`JobTransitionObject`) 从源 storage key 读取→写入目标后端→更新 `storage_key` + `storage_class`→删除旧 blob。使用现有的 JobPool 基础设施
3. **冷存储恢复工作流**：`?restore` 调用 `Storage.RestoreObject`（S3 Glacier 的异步恢复），创建一个临时副本（`TemporaryRestoreObject`），设置到期时间，返回 `202 Accepted`
4. **恢复状态查询**：`HEAD ?restore` 返回 `x-amz-restore` 头（`ongoing-request="true"` 或 `expiry-date="..."`）

### 边界情况

| 场景 | 处理 |
|------|------|
| 转化中对象被读取 | 从源存储类读取（代价高），或阻塞直到转化完成 |
| 并发转化同一个对象 | Job dedupe key `transition:{objectID}` 防止重复 |
| 冷存储恢复后到期 | Reconcile 或恢复时创建定时删除任务 |
| `STANDARD` → `STANDARD`（同后端） | 跳过，不创建 job |
| 跨后端转化（local → S3） | 多 backend 支持：转化 worker 指定 `storage.BackendKind` |

---

## 方向四：跨协议桶策略统一评估

### 现状与代码证据

**策略引擎存在但功能有限：**
```go
// internal/auth/policy.go
type Policy struct {
    Version string      `json:"Version"`
    Statement []Statement `json:"Statement"`
}
type Condition struct {
    IpAddress    map[string]string `json:"IpAddress,omitempty"`
    NotIpAddress map[string]string `json:"NotIpAddress,omitempty"`
}
// Eval 仅检查 aws:SourceIp
// 无 aws:CurrentTime, aws:SecureTransport, aws:Referer 等条件键
```

**策略仅在 S3 handler 路径执行：**
```go
// internal/api/s3compat/handler.go — 在每个 S3 操作中调用
func enforceBucketPolicy(ctx, bucket, action, srcIP string) error {
    cfg, _ := repo.GetBucketConfig(ctx, tenant, bucket)
    if cfg.Policy == "" { return nil }
    p, _ := auth.ParsePolicy(cfg.Policy)
    return p.Eval(action, srcIP)
}
```

**REST handler 完全不执行策略检查：** `internal/api/rest/handler.go` 到 `router.go`，没有任何路由调用策略评估。

**WebDAV 不检查：** `internal/api/webdav/dav.go` 中的所有操作直接调用 `svc.Put`/`svc.Get`/`svc.Delete`——这些方法在 FileService 层不检查桶策略（它们不应该——策略是协议层的关注点）。

**MCP 不检查：** `internal/mcp/server.go` 的所有工具调用也直接操作 FileService。

**策略字段不完整：** `Statement` 结构体缺少 `NotPrincipal`/`NotAction`/`NotResource` 字段，`Condition` 缺少常见的 S3 条件键。这意味着即使 REST 路径加了策略检查，其表达能力也远不如 AWS IAM。

### 产品价值与典型场景

- **安全控制**：管理员通过桶策略"只允许来自公司 VPN IP 的读取"——当前 REST 用户可以从任何 IP 读取
- **跨协议一致性**：无论用户使用 S3 SDK、REST API、WebDAV 还是 MCP 工具，相同的安全策略生效
- **委托访问**：`Principal: {AWS: "arn:aws:iam::..."}` 实现跨租户访问控制

### 架构权衡与建议方向

1. **策略评估中心化**：在 `middleware.Tenant` 之后、handler 之前插入一个可选中间件，提取请求的 `(tenant, bucket, action, sourceIP)` 并评估桶策略
2. **Action 映射**：每个协议路径将 HTTP 方法映射为 S3 action 语义（GET→`s3:GetObject`，PUT→`s3:PutObject`，DELETE→`s3:DeleteObject`，List→`s3:ListBucket`）
3. **策略条件扩展**：添加 `aws:CurrentTime`（用于时间限制的策略）、`aws:SecureTransport`（强制 HTTPS）和 `aws:Referer`（防盗链）
4. **Statement 字段补全**：添加 `NotPrincipal`、`NotAction`、`NotResource` 字段

### 边界情况

| 场景 | 处理 |
|------|------|
| REST 路径缺少 bucket 参数 | 使用 `service.DefaultBucket` 评估策略 |
| 多个策略 Statement 冲突 | `Deny` 优先（IAM 标准语义） |
| 策略语法错误 | Fail-open（记录错误但允许访问）或 fail-closed（拒绝所有访问）可配置 |
| 健康检查路径 | 绕过策略检查（同绕过 Auth 的路径） |
| WebDAV PROPFIND / MCP resources/list | 映射为 `s3:ListBucket` 操作 |

---

## 方向五：服务端 COPY 重写

### 现状与代码证据

**当前 COPY 实现是资源浪费的：**

```go
// internal/api/s3compat/handler.go:CopyObject — 伪代码流程
rc, obj, err := h.svc.Get(ctx, tenant, sourceBucket, sourceKey)  // ① 从存储读取
// ... 完整读取到内存 ...
_, err = h.svc.Put(ctx, tenant, destBucket, destKey, rc, obj.Size, opts) // ② 写回存储
```

对于大文件（如 1GB 视频），这意味着：
- 1GB 数据读入内存（或临时文件）
- 1GB 数据写回同一存储后端
- CPU 和网络带宽翻倍
- 相同后端的复制完全浪费网络往返

**缺失 S3 COPY 特性：**
- `x-amz-metadata-directive: COPY`（保持源元数据）vs `REPLACE`（覆盖）——当前永远创建新元数据
- `x-amz-storage-class`——目标对象的存储类不可覆盖
- `x-amz-copy-source-if-match` / `x-amz-copy-source-if-none-match` / `x-amz-copy-source-if-modified-since`——条件复制缺失，竞态条件下可能复制了错误版本

**Storage 接口无原生 COPY：**

```go
// internal/storage/storage.go:Storage 接口
type Storage interface {
    Get(ctx, key) (io.ReadCloser, ObjectInfo, error)
    Put(ctx, key, r, size, opts) (ObjectInfo, error)
    Delete(ctx, key) error
    Stat(ctx, key) (ObjectInfo, error)
    // 无 Copy(ctx, srcKey, dstKey, opts) error
}
```

没有 `Copy` 方法意味着同后端复制无法利用云存储 SDK 的原生 `CopyObject` API（如 S3 的 `PutObject` + `CopySource`，OSS 的 `CopyObject`，COS 的 `Copy`），这些 API 是服务端执行、零数据传输的。

### 产品价值与典型场景

- **大文件复制性能**：1GB 文件的 S3 COPY 操作当前耗时数秒（读+写），后端原生 COPY 可在 100ms 内完成
- **目录级批量复制/移动**：WebDAV MOVE 当前流复制，批量 COPY 时更显著
- **元数据更新**：用户想修改对象的 Content-Type 或存储类而不重新上传——S3 通过 COPY 到自身 + `x-amz-metadata-directive REPLACE` 实现

### 架构权衡与建议方向

1. **Storage 接口扩展**：添加 `Copy(ctx, srcKey, dstKey, opts CopyOptions) (ObjectInfo, error)` 方法——每个后端实现自己的优化（local=cp，S3=CopyObject，OSS=CopyObject，COS=Copy）
2. **文件服务 COPY 方法**：`FileService.Copy` 先尝试 `store.Copy`（零数据传输），失败时回退到 `Get`+`Put`
3. **元数据处理**：解析 `x-amz-metadata-directive`：COPY 时保留源元数据，REPLACE 时使用请求头中的新元数据
4. **条件复制**：在 Copy 前检查源对象的 ETag/LastModified，与现有条件请求逻辑一致
5. **批量 COPY（可选）**：S3 的批量操作协议（`POST ?delete` 已在，`POST ?copy` 缺失）

### 边界情况

| 场景 | 处理 |
|------|------|
| 跨后端 COPY（local → S3） | 回退到流式复制（`Get`+`Put`），与当前行为一致 |
| 同后端 COPY + 版本控制 | 目标 key 创建新版本；`CopyObject` 在 versioning-enabled 的 S3 上自动行为 |
| COPY 到自身（元数据更新） | `srcKey == dstKey` → 跳过物理复制，仅更新元数据行 |
| 源对象处于锁定状态 | 复制不受锁定限制（锁附加于目标 key），但源对象的内容不锁定 |
| COPY 期间源对象被删除 | `CopyOptions.Conditions.IfMatch` 确保读-复制-写不跨破坏 |

---

## 跨方向关联与优先级建议

```
方向三 (存储分层) ────── 依赖 ───→ 方向五 (服务端 COPY)
     ↓                                   ↓
方向二 (访问日志)      方向一 (事件通知)    方向四 (统一策略)
     ↓                                   ↓
         └── 所有方向共享 ──→ 集群单例 + JobPool 基础设施 (已就绪)
```

建议启动顺序：

| 轮次 | 方向 | 理由 |
|------|------|------|
| **第 1 轮** | 方向一 + 方向二 | 代码锚点最明确、管线断裂最清晰、无外部依赖 |
| **第 2 轮** | 方向三 | 需要先完成方向五中的 `Storage.Copy` 方法（或至少回退路径） |
| **第 3 轮** | 方向五 | `Storage.Copy` 扩展是基础设施变更，影响 local/s3/oss/cos 全部后端 |
| **第 4 轮** | 方向四 | 安全影响大、但需要协议的 action 映射标准确定后实施 |

---

## 附录：Grep 验证摘要

```bash
# 方向一 — 全局通知规则存储但不消费
grep -rn "NotificationRule\|notification_rules\|GetBucketNotifications\|SetBucketNotifications" internal/ | grep -v "_test" | wc -l
# → 34 处引用，全部配置/持久化/API 层，零处事件分发

# 方向二 — WriteAccessLog 零调用处
grep -rn "WriteAccessLog" internal/ --include="*.go"
# → internal/repository/sql_buckets.go:370 (定义)
# → internal/repository/repository.go:274 (接口声明)
# → 零调用处

# 方向三 — storage_class 广泛存在但无状态转换
grep -rn "storage_class\|StorageClass\|StorageClassCounts" internal/ | grep -v "_test" | wc -l
# → 44 处引用，全部读写/查询，零处转换逻辑
grep -rn "Transition\|transition" internal/ --include="*.go" | grep -v "_test\|transport\|Transient\|transient"
# → 零命中

# 方向四 — bucket policy 仅 S3 路径执行
grep -rn "enforceBucketPolicy\|checkBucketPolicy\|Policy.Eval\|\.Eval(" internal/ --include="*.go"
# → internal/auth/policy.go (定义+Eval)
# → internal/api/s3compat/handler.go (唯一调用处)
# → REST/WebDAV/MCP 均无引用

# 方向五 — COPY 流式实现零后端优化
grep -rn "CopyObject\|CopySource\|svc.Copy\|Copy(" internal/ --include="*.go" | grep -v "_test\|\.pb\."
# → internal/api/s3compat/handler.go:CopyObject (仅 Get+Put)
# → storage.Storage 无 Copy 方法
```
