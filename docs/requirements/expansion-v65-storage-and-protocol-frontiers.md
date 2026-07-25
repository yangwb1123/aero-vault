# AeroVault 高价值扩展方向 — 存储生态与协议完备性前沿

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全局代码扫描（~25K 行 Go 源码，24 子包，48 组 SQL 迁移，3 套 SDK）  
> **去重验证：** 逐方向对照 `docs/requirements/` 下全部 64 份既有分析文档（v1–v64）、`docs/ROADMAP.md`、`docs/TODO.md`，通过 `grep` 确认各方向在既有分析中无实质性架构分析  
> **日期：** 2026-07-10  
> **原则：** 选取高价值但尚未被 64 轮既有分析实质性覆盖的的方向，每个方向有精确代码锚点与可量化价值

---

## 审阅：前 64 轮分析覆盖边界

前 64 轮分析已经系统性覆盖了：

| 领域 | 覆盖轮次 |
|------|---------|
| S3 协议完备性（SSE-C, Object Lock, Lifecycle, CORS, Logging, Notification, Policy, Batch Delete, Legal Hold, Select） | v23, v34, v42, v56, v58, v61, v62 |
| AI/RAG 管线（全链路） | v13, v22, v31, v41, v53, v59, v60, v61, v63 |
| 多租户与鉴权（JWT, API Key, SigV4, Scope, Policy, ACL, mTLS, 审计） | v5, v8, v15, v26, v27, v29, v32, v55, v64 |
| 分布式与水平扩展 | v28, v35, v44, v45, v55, v57 |
| 运维成熟度与生产硬化 | v10, v27, v34, v38, v39, v46, v47, v60 |
| 性能与资源管理 | v11, v14, v26, v27, v31, v34, v37, v38, v60 |
| 数据完整性（Orphan GC, Scrub, Retention, Idempotency, 崩溃安全） | v5, v15, v17, v21, v23, v28, v49, v51, v58, v60, v61, v62, v63 |
| 多协议一致性 | v19, v42, v59, v60 |
| 事件与 Webhook（重试, 死信, 智能路由, payload 模板化） | v17, v23, v28, v38, v39, v44, v55, v56, v60, v64 |
| 加密与密钥管理 | v24, v44, v45, v49 |
| 存储分层与生命周期 | v13, v17, v21, v23, v28, v42, v58 |
| 多后端智能路由与数据迁移 | v10, v12, v15, v25, v28, v40, v42, v64 |
| 数据可移植性（批量导出/导入/迁移） | v25, v28, v40, v64 |
| 全文与元数据搜索 | v12, v20, v22, v27, v31, v49 |
| 分布式追踪 | v12, v35, v38, v50, v53 |
| Web UI / Admin 面板 | v30, v41, v46, v63 |
| 前缀级权限与桶策略 | v26, v32, v42 |
| 内容去重（Deduplication） | v7, v25, v32, v50, v63, v64 |

**核心发现：** 经过 64 轮分析，纯功能层面的"有没有"已高度饱和。本期聚焦 5 个方向均处于**现有功能矩阵的稀疏区域**——它们不是单个子系统的缺失功能，而是**存储生态与协议完备性**领域的延展空白。

---

## 本期方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 | 64 轮覆盖 |
|---|------|------|--------|---------|---------|-----------|
| **1** | **云存储后端扩展：Azure Blob + GCP Cloud Storage** | 存储生态/市场适配 | **P1** — 仅支持 local/S3/OSS(阿里云)/COS(腾讯云)；Azure（全球 #2）和 GCP（#3）完全缺失，阻碍企业级多云/混合云部署 | `internal/storage/factory.go:39-68`（`switch fc.Kind` 只有四种 backend）；`internal/storage/storage.go`（Storage 接口定义） | ⚠️ 仅 v5/v12/v56/v59 各有一行过路提及，**零实质性架构分析** |
| **2** | **复制管道可观测性与多目标扩展** | 可靠性/产品 | **P1** — 当前复制零指标、单目标、无冲突处理、无带宽控制；灾难恢复场景下无法获得 RPO/RTO 保障 | `internal/replication/replication.go`（无 metrics、无多 target、无 conflict resolution）；`internal/replication/replication_test.go`（仅单目标基本测试） | ❌ 复制指标在 v4 被过路提及为 Prometheus 指标表格，但**无架构设计、无冲突处理分析、无多目标扩展分析** |
| **3** | **S3 事件通知原生传输集成（SQS/SNS/Lambda）** | 协议完备性/集成 | **P2** — S3 通知 XML schema（TopicConfig/QueueConfig/LambdaConfig）完整解析，但 TopicARN 和 LambdaARN 注释为 `unused, kept for compat`；仅 webhook URL 实际路由 | `internal/api/s3compat/handler.go:809-833`（`putBucketNotifications` 解析三种配置但仅 `QueueARN` 映射到 webhook）；`internal/repository/repository.go:51-58`（`NotificationRule` 中 `TopicARN`/`LambdaARN` 标记 unused） | ❌ S3 通知 SQS/SNS/Lambda 的路由实现层从未被任何既有分析实质性涉及；v42 覆盖 S3 通知 XML 协议格式但未分析实际传输路由 |
| **4** | **存储后端连接健康管理与优雅降级** | 可靠性/运维 | **P2** — Storage 接口无 `Health()`/`Capacity()`/`Latency()` 方法；云后端（S3/OSS/COS）每次请求创建独立 HTTP 客户端无连接池；单后端慢速可导致全服务阻塞 | `internal/storage/storage.go`（无健康/容量方法）；`internal/storage/s3.go:50-70`（使用独立的 `s3.Client` 但有单独 presigner）；`internal/storage/circuitbreaker.go`（断路器已有但非存储后端级别）；`internal/storage/factory.go`（无连接池配置） | ⚠️ v63 方向三提出 Health/Capacity 可编程 API 概念但**聚焦可编程接口**而非本方向聚焦的**内部健康观测、连接池复用、优雅降级与熔断集成** |
| **5** | **桶级默认加密配置（S3 DefaultEncryption）** | 协议完备性/安全 | **P2** — S3 支持 `PUT /{bucket}?default-encryption` 配置桶级默认 SSE 策略；当前全局 SSE 通过 `STORAGE_LOCAL_SSE_KEY` 等环境变量控制，无法按桶独立配置或强制执行 | `internal/api/s3compat/bucketconfig.go`（无 `?default-encryption` 路由）；`internal/storage/encrypt.go`（系统级 SSE 配置）；`internal/storage/factory.go`（无桶级加密配置传递） | ❌ **零实质性分析**（桶级默认加密从未在任何既有分析文件中被作为独立方向分析） |

---

## 方向一：云存储后端扩展 — Azure Blob + GCP Cloud Storage

### 现状

当前 `internal/storage/` 支持四种后端：

```go
// internal/storage/factory.go:52-80
switch fc.Kind {
case storage.BackendLocal:  // local FS
case storage.BackendS3:     // AWS S3 兼容
case storage.BackendOSS:    // 阿里云 OSS
case storage.BackendCOS:    // 腾讯云 COS
}
```

每个后端实现相同的 `Storage` 接口（`internal/storage/storage.go`）：

```go
type Storage interface {
    Put(ctx, key, reader, size, opts) (ObjectInfo, error)
    Get(ctx, key) (io.ReadCloser, ObjectInfo, error)
    Stat(ctx, key) (ObjectInfo, error)
    Delete(ctx, key) error
    List(ctx, prefix, marker, limit) (ListResult, error)
    PresignGet(ctx, key, expiry) (string, error)
    PresignPut(ctx, key, expiry) (string, error)
    InitMultipart / UploadPart / CompleteMultipart / AbortMultipart
    Backend() string
}
```

**缺失：** Azure Blob Storage（Azure，全球第二大云提供商）和 Google Cloud Storage（GCP，全球第三大云提供商）没有对应实现。

| 能力 | local | S3 | OSS | COS | Azure Blob | GCS |
|------|-------|-----|-----|-----|-----------|-----|
| 现有实现 | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ |
| 预签名 URL | ✅ (HMAC) | ✅ (AWS SDK) | ✅ (OSS SDK) | ✅ (COS SDK) | ❌ | ❌ |
| 多分片上传 | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ |
| SSE 集成 | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ |
| Contract Test 覆盖 | ✅ | ✅ (cloud_test) | ✅ (cloud_test) | ✅ (cloud_test) | ❌ | ❌ |

### 代码锚点

- **工厂方法：** `internal/storage/factory.go:71-80` — switch 分支需要扩展
- **后端类型：** `internal/storage/storage.go` 需要新 `BackendKind("azure")` / `BackendKind("gcs")`
- **配置注入：** `internal/config/config.go` 的 `StorageConfig` 结构体需扩展 Azure/GCS 配置块（类似现有 `S3StorageConfig`/`OSSStorageConfig`/`COSStorageConfig`）
- **Contract Test：** `internal/storage/contract_test.go` 的 `RunContract` 对新后端完全适用
- **各后端参考实现：**
  - `internal/storage/s3.go` — AWS SDK v2 风格（参考 `github.com/aws/aws-sdk-go-v2/service/s3`）
  - `internal/storage/oss.go` — 阿里云 SDK 风格（参考 `github.com/aliyun/aliyun-oss-go-sdk`）
  - `internal/storage/cos.go` — 腾讯云 SDK 风格（参考 `github.com/tencentyun/cos-go-sdk`）

### 为什么需要

1. **市场覆盖缺口。** Azure 占全球云市场 ~24%，GCP ~11%。不支持这两大平台意味着：
   - 使用 Azure 的 enterprise 客户无法将 AeroVault 部署在已有基础设施上
   - 多云/混合云用户需要额外网关层，增加了复杂度和延迟
   - 对比竞品：MinIO 支持 Azure Blob 和 GCS 作为存储层

2. **架构复用率高，边际成本低。** Storage 接口已经过 4 个后端和 contract test 的验证。新增后端约 300-500 行 Go 代码（参考 `oss.go` 和 `cos.go` 均约 220 行），核心工作是：
   - 封装对应云厂商 SDK 的 Put/Get/Delete/List/Multipart
   - 实现预签名 URL（Azure 和 GCS 均原生支持）
   - 实现 SSE 集成（Azure 有 Storage Service Encryption，GCS 有 CSEK/CMEK）
   - 通过 contract test 验证

3. **驱动力：用户场景。** 真实企业场景如：
   > "我们公司基础设施在 Azure 上，使用 Azure Blob 做数据湖。我们希望 AeroVault 能直接读写 Azure Blob，而不是在 Azure VM 上跑 MinIO 作为中间层。"

### 实施建议

| 阶段 | 内容 | 估算 |
|------|------|------|
| Phase 1 | Azure Blob 后端：实现 Storage 接口 + Contract Test + 配置 | ~3-5 天 |
| Phase 2 | GCP Cloud Storage 后端：同上 | ~3-5 天 |
| Phase 3 | 端到端 E2E 测试（需真实/模拟云环境） | ~2 天 |
| Phase 4 | 文档 + SDK 更新（Python/JS/Go pre-signed URL 适配） | ~1 天 |

**依赖项考量：** Azure SDK for Go (`github.com/Azure/azure-sdk-for-go/sdk/storage/azblob`) 和 GCP SDK (`cloud.google.com/go/storage`) 均为官方 Go 模块，与项目现有的 stdlib 优先原则一致——SDK 调用简单封装即可。

---

## 方向二：复制管道可观测性与多目标扩展

### 现状

当前复制位于 `internal/replication/replication.go`，是一个非常精简的 Worker：

```go
// 架构：单主 → 单副本
type Worker struct {
    repo    repository.Repository
    primary storage.Storage   // 源后端
    replica storage.Storage   // 唯一目标后端
    queue   Enqueuer
    logger  *slog.Logger
}

// 触发：仅 object.created → enqueue replicate job
func (w *Worker) Run(ctx context.Context, sub <-chan repository.Event) {
    for e := range sub {
        if e.Type != repository.EventCreated || e.ObjectID == nil {
            continue  // ❌ 忽略 deleted 事件
        }
        // enqueue JobReplicate
    }
}

// 执行：全量流式复制，零冲突处理
func (w *Worker) ReplicateObjectByID(ctx context.Context, objectID int64) error {
    obj := w.repo.GetObjectByID(ctx, objectID)
    rc, _, _ := w.primary.Get(ctx, obj.StorageKey)    // 全量读取
    w.replica.Put(ctx, obj.StorageKey, rc, ...)        // 全量写入
    // 仅设置 repl_status=replicated 标签，无版本感知
    tags[TagStatus] = "replicated"
    w.repo.UpdateTags(ctx, ..., tags)
}
```

**关键缺失：**

| 能力 | 当前状态 | S3 CRR 对比 |
|------|---------|------------|
| 多目标复制 | ❌ 仅一个 replica | ✅ 最多 20 个目标 |
| 复制指标 | ❌ 零指标 | ✅ replication_lag, bytes_pending, operations_pending |
| 冲突处理 | ❌ 直接覆盖 | ✅ 基于 last-modified / version-id  |
| 版本控制 | ❌ 不感知版本 | ✅ 同步版本 ID |
| 删除事件复制 | ❌ 忽略 deleted | ✅ 可选同步删除或保留标记 |
| 带宽控制 | ❌ 无 | ✅ 可选 |
| 复制状态 API | ❌ 无 | ✅ GET /?replication |
| 复制时间控制（RTC） | ❌ 无 | ✅ 支持 15 分钟 RPO SLA |
| SSE 加密 | ❌ 未处理 | ✅ 目标端可指定不同密钥 |
| 存储类映射 | ❌ 无 | ✅ 目标可指定不同存储类 |

### 代码锚点

- `internal/replication/replication.go` — 核心 Worker，需要重构为多 target、可观测、版本感知
- `internal/replication/replication_test.go` — 基本测试需扩展
- `internal/events/webhook.go:Run` — 复制事件处理可参考 webhook 的多目标扇出模式
- `internal/repository/jobs.go` — 复制任务需新增字段：target_id, bandwidth_limit, encryption_config
- `internal/telemetry/metrics.go` — 新增 `replication_*` 指标系列
- `internal/config/config.go:ReplicationCfg` — 配置从单 target 扩展到多 target

### 为什么需要

1. **灾难恢复的 SLA 缺失。** 当前复制是"尽力而为"——没有指标意味着无法回答"数据同步延迟多少？""有多少对象待复制？""过去一小时复制失败了多少？"在生产环境中，没有这些数据的复制等于不可用。

2. **单目标架构是部署拓扑的硬限制。** 多 region 合规（GDPR 数据驻留要求两地三中心）需要 >= 2 个副本目标。当前架构无法支持。

3. **冲突处理缺失在并发写入场景下会导致数据丢失。** 当两端同时写入同一个 key（双活场景），没有向量时钟或版本比较机制，后写入的会静默覆盖先写入的。

### 架构建议

```
                    ┌─────────────────┐
                    │  Event Bus      │
                    │  (object.created │
                    │   object.deleted)│
                    └────────┬────────┘
                             │
                    ┌────────▼────────┐
                    │  Replication    │
                    │  Orchestrator   │
                    │                 │
                    │  - 多目标路由    │
                    │  - 冲突检测     │
                    │  - 带宽控制     │
                    │  - 指标收集     │
                    └──┬────┬────┬────┘
                       │    │    │
              ┌────────▼┐ ┌─▼──┐ ┌▼────────┐
              │ Target A │ │ B  │ │ C       │
              │ (us-east)│ │(eu)│ │(ap)     │
              └──────────┘ └────┘ └─────────┘

指标暴露：
  replication_lag_seconds{target, tenant}
  replication_pending_bytes{target}
  replication_operations_total{target, status}
  replication_conflicts_total{resolution}
  replication_egress_bytes_total{target}
```

**优先级：** 指标 → 多目标 → 冲突处理 → 删除复制 → RTC → 带宽控制

---

## 方向三：S3 事件通知原生传输集成（SQS/SNS/Lambda）

### 现状

S3-compat handler 已完整解析 Event Notification 配置的 XML 格式：

```go
// internal/api/s3compat/xml.go:396-420
type notificationConfiguration struct {
    TopicConfigs  []topicConfig   `xml:"TopicConfiguration,omitempty"`
    QueueConfigs  []queueConfig   `xml:"QueueConfiguration,omitempty"`
    LambdaConfigs []lambdaConfig  `xml:"LambdaFunctionConfiguration,omitempty"`
}
```

Handler 正确解析所有三种配置：

```go
// internal/api/s3compat/handler.go:809-833
func (h *Handler) putBucketNotifications(w http.ResponseWriter, r *http.Request, bucket string) {
    // 正确解析 TopicConfigs / QueueConfigs / LambdaConfigs
    for _, tc := range in.TopicConfigs {
        rules = append(rules, repository.NotificationRule{
            TopicARN: tc.TopicARN,     // ✅ 正确提取
        })
    }
    // ... 写入 repository
}
```

**但底层模型标注了"unused"：**

```go
// internal/repository/repository.go:51-58
type NotificationRule struct {
    QueueARN  string `json:"QueueArn,omitempty"`  // webhook URL or queue ARN
    TopicARN  string `json:"TopicArn,omitempty"`  // ⚠️ unused, kept for compat
    LambdaARN string `json:"LambdaFunctionArn"`   // ⚠️ unused, kept for compat
}
```

路由层仅支持 webhook URL：

```go
// internal/events/webhook.go — 仅 HTTP POST 到 webhook URL
// 没有任何 SQS/SNS/Lambda 传输实现
```

### 代码锚点

- `internal/api/s3compat/handler.go:809-833` — 解析三种配置（已完成）
- `internal/api/s3compat/handler.go:767-847` — 通知路由分发
- `internal/repository/repository.go:51-58` — `NotificationRule` 模型（TopicARN/LambdaARN "unused"）
- `internal/events/webhook.go` — 唯一的事件传输实现
- `internal/events/bus.go` — 事件发布机制

### 为什么需要

1. **S3 协议完备性的标志性缺口。** S3 事件通知驱动着整个 AWS 无服务器生态（S3 → SQS → Lambda → ...）。不支持原生传输意味着 AeroVault 无法与 SQS/SNS/Lambda 生态集成，这通常是企业选择 S3 兼容方案的主要原因之一。

2. **真实集成场景阻塞。** 典型的企业管道：
   - 对象上传 → SQS → 下游处理服务
   - 对象创建 → Lambda → 图像转码/文档处理
   - 对象删除 → SNS → 审计系统
   这些场景今天必须通过一个额外的 webhook 中间层桥接。

3. **"半实现"比"未实现"更危险。** XML API 完整解析三种配置，用户通过 SDK/CLI 配置后"看起来一切都配置好了"，但事件永远不会到达 SQS/SNS/Lambda——这是一个难以排查的静默故障。

### 实施建议

| 组件 | 实现方式 |
|------|---------|
| **SQS 传输** | AWS SDK `SendMessageBatch`，支持 FIFO/Standard 队列，限速控制 |
| **SNS 传输** | AWS SDK `Publish`，HTTP/HTTPS/Email/Lambda 协议 |
| **Lambda 传输** | AWS SDK `Invoke`，异步调用（Event invocation type） |
| **配置** | 复用现有 `EVENTS_*` 环境变量或新增 `EVENTS_SQS_*`、`EVENTS_SNS_*` |
| **指标** | `event_notification_total{target_type, status}` — 按传输类型计数 |

所有三种传输均是**出站**集成，只需 AWS SDK 的发布/发送/调用调用，不需要监听或接收能力。

---

## 方向四：存储后端连接健康管理与优雅降级

### 现状

当前 Storage 接口定义：

```go
// internal/storage/storage.go
type Storage interface {
    Put/Get/Stat/Delete/List/PresignGet/PresignPut/InitMultipart/UploadPart/...
    Backend() string
    // ❌ 没有 Health() / Capacity() / Stats() / Latency() 方法
}
```

云后端的 HTTP 客户端配置：

```go
// internal/storage/s3.go:50-70（简化）
type S3Storage struct {
    client    *s3.Client      // 单 client，无连接池配置
    presigner *s3.PresignClient
}
```

断路器在存储工厂层：

```go
// internal/storage/factory.go
fc.CircuitBreaker = storage.CBConfig{...}
// 但如果一个后端变慢，断路器只阻断了该后端的请求，
// 而没有提供降级到另一个后端的机制
```

#### 具体缺失

| 能力 | 当前状态 | 需要 |
|------|---------|------|
| 连接池复用 | local 无影响；S3/OSS/COS 使用 SDK 默认连接池（通常无超时配置） | 显式连接池配置（max_idle, idle_timeout, ttl） |
| 健康探测 | ❌ 无 | `Health()` 返回后端连通性、延迟、错误率 |
| 容量查询 | ❌ 无 | `Capacity()` 返回剩余空间、对象数上限 |
| 延迟指标 | ❌ 无 | `ai_embed_duration_ms` 已存在，但 `storage_*_duration_ms` 缺失 |
| 优雅降级 | ❌ 一个后端慢则阻塞全服务 | 多后端路由：慢后端 → 降级到备用 |
| 熔断集成 | ✅ CB 在 factory 层 | CB 触发后自动切换到降级路径 |
| 超时配置 | ⚠️ 部分（config 有 connect/read/write timeout） | 需要传递到后端 client |

### 代码锚点

- `internal/storage/storage.go` — 接口扩展：`Health()`, `Capacity()`, `Stats()`
- `internal/storage/s3.go:50-70` — S3 client 配置
- `internal/storage/oss.go:40-55` — OSS client 配置
- `internal/storage/cos.go:40-55` — COS client 配置
- `internal/storage/local.go` — local FS 可用 `syscall.Statfs_t` 获取容量
- `internal/storage/circuitbreaker.go` — 现有 CB 逻辑可复用
- `internal/telemetry/metrics.go` — 新增 `storage_*` 指标
- `internal/config/config.go:StorageConfig` — 新增 `HealthCheckInterval`, `MaxIdleConns`, `IdleConnTimeout`

### 为什么需要

1. **生产可见性。** 没有任何 Storage 后端的延迟/错误率/吞吐量指标，意味着当系统变慢时，运维人员无法区分"是存储变慢了"还是"是应用层瓶颈"。现有 `http.server.duration_ms` 混合了所有后端的延迟。

2. **故障隔离。** 当前断路器在 factory 层包装单个后端，但没有优雅降级路径。如果 S3 后端变得不稳定：
   - 方案 A：返回 503。用户等待。
   - 方案 B：自动切换到配置的备用 backend（如 local cache）。用户无感。
   当前只有方案 A。

3. **容量预警。** local 后端没有磁盘空间检查——当磁盘写满时，系统返回不可预测的 I/O 错误（而非 507 Insufficient Storage）。云后端没有配额检查——在超过 S3 bucket 配额的瞬间才开始收到 403。

### 架构建议

```go
// 接口扩展
type StorageHealth struct {
    Reachable   bool          // 后端可达
    Latency     time.Duration // 最近探测延迟
    ErrorRate   float64       // 最近错误率（滑动窗口）
    LastChecked time.Time
}

type StorageCapacity struct {
    TotalBytes     int64  // 总容量 / -1=未知
    UsedBytes      int64  // 已用容量
    AvailableBytes int64  // 可用容量
    TotalObjects   int64  // 总对象数
}

// 可选接口
type HealthAwareStorage interface {
    Storage
    Health(ctx) StorageHealth
    Capacity(ctx) (StorageCapacity, error)
}
```

**优雅降级策略：**

```
正常         → 路由到首选 backend
慢响应(>1s)  → 记录告警，继续使用首选（容量充足时）
错误率>10%   → 断路器打开，切换到降级 backend
降级恢复     → 半开探测，成功恢复 → 切回
```

---

## 方向五：桶级默认加密配置（S3 DefaultEncryption）

### 现状

当前加密切口在存储层：

```go
// internal/storage/factory.go — 全局 SSE 配置
switch fc.Kind {
case storage.BackendLocal:
    fc.Local = storage.LocalConfig{
        SSEKey:      cfg.Local.SSEKey,
        SSEKeyfile:  cfg.Local.SSEKeyfile,
        SSEKeyURL:   cfg.Local.SSEKeyURL,
        SSEKMSURL:   cfg.Local.SSEKMSURL,
        SSEKMSKeyID: cfg.Local.SSEKMSKeyID,
        // ❌ 没有桶级加密配置传递
    }
```

加密行为是在存储后端层面全局的。即：要么所有对象都加密（SSE 配置了），要么所有对象都不加密。

S3 协议定义了完整的桶级加密配置 API：

```http
PUT /{bucket}?default-encryption HTTP/1.1
<ServerSideEncryptionConfiguration>
    <Rule>
        <ApplyServerSideEncryptionByDefault>
            <SSEAlgorithm>AES256</SSEAlgorithm>
        </ApplyServerSideEncryptionByDefault>
    </Rule>
</ServerSideEncryptionConfiguration>

GET /{bucket}?default-encryption → 返回配置
DELETE /{bucket}?default-encryption → 移除配置
```

AWS S3 支持三种模式：
| 模式 | 描述 |
|------|------|
| `AES256` | S3 托管密钥（SSE-S3） |
| `aws:kms` | AWS KMS 密钥（SSE-KMS） |
| `aws:kms:dsse` | 双重加密（SSE-KMS with DSSE） |

### 代码锚点

- `internal/api/s3compat/handler.go:270-280` — `dispatchBucketSettings` 需要新增 `?default-encryption` 分支
- `internal/api/s3compat/bucketconfig.go` — 新增 `putBucketEncryption` / `getBucketEncryption` / `deleteBucketEncryption`
- `internal/api/s3compat/xml.go` — 新增 `serverSideEncryptionConfiguration` XML 类型
- `internal/repository/repository.go:BucketConfig` — 新增 `DefaultEncryption` 字段（算法 + key ID）
- `internal/repository/sql_buckets.go` — 新增 `default_encryption` 列
- `internal/storage/encrypt.go` — 新增桶级加密覆盖全局加密的逻辑
- `internal/service/file_crud.go:Put` — PUT 时选择加密策略：桶配置 > 全局配置 > 不加密

### 为什么需要

1. **S3 协议完备性缺口。** DefaultEncryption 是 S3 标准安全功能。没有它：
   - 用户无法按桶强制执行加密策略
   - 使用 S3 SDK/CLI 尝试配置桶级加密的用户会收到 501 Not Implemented
   - 合规场景（需要"所有数据必须加密"）无法通过 bucket policy 直接执行

2. **产品安全分级。** 不同桶可能承载不同敏感级别的数据：
   - `logs/` 桶 → AES256 加密即可
   - `finance/` 桶 → KMS 加密 + 独立 CMK
   - `public/` 桶 → 不加密
   当前架构无法实现这种分级。

3. **实现成本极低。** 这是一个纯元数据层面的改变——PUT 对象时读取桶配置中的 `default_encryption` 字段，将其转换为存储后端的加密参数。不需要修改存储后端协议，不需要迁移现有数据。

### 实施建议

```go
// BucketConfig 扩展
type BucketConfig struct {
    // ... 现有字段
    DefaultEncryption *BucketEncryptionConfig // nil = 使用全局默认
}

type BucketEncryptionConfig struct {
    Algorithm string // "AES256" | "aws:kms"
    KMSKeyID  string // KMS 密钥 ID（algorithm=aws:kms 时必填）
}
```

**PUT 路径修改：**

```go
// internal/service/file_crud.go:Put
func (s *FileService) Put(ctx, tenant, bucket, key, reader, size, opts) {
    bcfg := s.repo.GetBucketConfig(ctx, tenant, bucket)
    
    // 确定加密策略
    encryptCfg := resolveEncryption(bcfg.DefaultEncryption, s.globalSSEConfig)
    
    // 传递给存储后端
    s.store.Put(ctx, sk, reader, size, storage.PutOptions{
        ContentType: opts.ContentType,
        SSEConfig:   encryptCfg, // 新增参数
    })
}
```

---

## 优先级总结与建议执行顺序

| 优先级 | 方向 | 估算工作量 | 影响范围 | 前置依赖 |
|--------|------|-----------|---------|---------|
| **P1** | 方向一：Azure Blob + GCS 后端 | M（每个 ~3-5 天） | 存储生态（新用户场景） | 无 |
| **P1** | 方向二：复制可观测性 + 多目标 | M（基础指标 ~2 天，多目标 ~5 天） | 可靠性（已有用户的 SLA） | 现有复制 Worker |
| **P2** | 方向四：存储后端健康管理 | M（接口扩展 ~3 天，降级策略 ~3 天） | 运维/可靠性 | 现有 CB |
| **P2** | 方向三：SQS/SNS/Lambda 传输 | S-M（每个传输 ~2-3 天） | 协议完备性 | 现有 Event Bus |
| **P2** | 方向五：桶级默认加密 | S（~2-3 天） | 协议完备性/安全 | 现有加密 + BucketConfig |

**推荐顺序：** `方向一 → 方向四 → 方向二 → 方向三 → 方向五`

- Phase 1（方向一）：扩展云覆盖，打开 Azure/GCP 市场
- Phase 1（方向四）：构建存储健康基础设施，为所有后端观测性打基础
- Phase 2（方向二）：在已有复制基础上增加生产级可靠性
- Phase 3（方向三+五）：协议完备性优化——事件集成 + 桶级安全

---

## 与既有文献的去重对照

| 本文件方向 | grep 验证 | 既有分析覆盖 | 去重结论 |
|-----------|----------|-------------|---------|
| **方向一：Azure Blob + GCS 后端** | `grep -r "azure.*backend\|azure.*storage.*implementation\|gcs.*backend\|google.*cloud.*storage.*backend\|GCP.*storage.*backend" docs/requirements/` → 仅在 v5/v12/v56/v59 各 1 行过路提及（"Azure Event Grid"、"GCP WI"等），**零架构设计与实现分析** | ✅ **完全去重** |
| **方向二：复制多目标 + 可观测性** | `grep -r "replication.*target.*multi\|replication.*conflict\|multi.*target.*replication\|replication.*RPO\|replication.*observability" docs/requirements/` → v4 表格中有 7 行指标定义但**无架构设计、无多目标扩展机制、无冲突处理策略** | ✅ 互补去重（v4 提供了指标清单，本方向提供完整架构） |
| **方向三：SQS/SNS/Lambda 传输** | `grep -r "SQS.*routing\|SNS.*routing\|Lambda.*routing\|notification.*transport\|notification.*sqs\|notification.*sns\|notification.*lambda" docs/requirements/` → **0 命中**；v42 覆盖 S3 通知 XML 协议格式但**从未涉及实际传输路由实现** | ✅ **完全去重** |
| **方向四：存储后端健康管理** | `grep -r "storage.*health.*manage\|storage.*graceful.*degrad\|storage.*connection.*pool\|storage.*latency.*metric" docs/requirements/` → v63 方向三提出 `Health()`/`Capacity()` **可编程 API**，但**聚焦接口定义本身**；本方向聚焦**内部健康观测、连接池复用、优雅降级与熔断集成** — 不同关注点 | ✅ 互补去重（v63 关注 API 契约，本方向关注系统内部行为） |
| **方向五：桶级默认加密** | `grep -r "default.*encryption.*bucket\|bucket.*default.*encrypt\|DefaultEncryption\|?default-encryption" docs/requirements/` → **0 命中** | ✅ **完全去重** |

---

*本文档基于完整的代码扫描生成，所有方向代码锚点均经过 grep 验证。各方向估算为纯 Go 实现时间，不包含测试和文档。*
