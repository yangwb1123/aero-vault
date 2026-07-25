# 高价值扩展方向分析 v25 — 架构盲区与平台深度

> **分析范围：** 全代码库扫描（cmd/server + internal/* + deploy/* + sdk/*，237 个 `.go` 文件，48 对迁移文件）
> **分析日期：** 2026-07-10
> **视角：** 资深架构师 / 产品经理
> **去重方法：** 逐篇对比 `docs/requirements/` 下 24 期既有分析 + `docs/ROADMAP.md`（10 方向）+ `docs/TODO.md` + `CHANGELOG.md`，确认每个方向在既有文档中**零覆盖或仅有行级提及**。
> **原则：** 不编写任何实现代码。

---

## 审阅：既有分析的覆盖边界

前 24 期 expansion 文档（v1–v24）累计覆盖约 **130+ 个方向**，核心领域包括：

| 领域 | 已覆盖方向数 | 代表性议题 |
|------|------------|-----------|
| 对象存储 CRUD / 多协议适配 | ~15 | S3 子资源完整、WebDAV、MCP 工具集 |
| AI 管线 | ~18 | 增量 BM25、向量漂移、搜索缓存、PII |
| 存储后端 | ~14 | 多后端、SSE 轮换/重包装、电路熔断器、存储分层 |
| 多租户 | ~10 | 租户管理、预算强制、审计日志 |
| 认证授权 | ~12 | 持久化 Key、跨副本失效、JWT issuer、桶策略 |
| 事件/通知 | ~11 | 多副本事件桥、通知规则 CRUD、webhook 重试 |
| 复制/HA/集群 | ~10 | 集群单例、跨区复制、pgvector 集成 |
| Reconcile / GC / Lifecycle | ~9 | 孤儿 blob、软删除保留、Scrub、分片上传统计 |
| 合规（WORM/Legal Hold/Retention） | ~7 | 合规锁、版本生命周期、Legal Hold |
| 可观测性 | ~8 | OTel 仪表、Grafana 面板、Prometheus 告警 |
| SDK | ~5 | 跨语言 API 方法覆盖、开发者体验 |
| Web UI / 管理控制台 | ~5 | UI 生产化、Admin Console |
| 工程质量 | ~8 | 大对象流式、内存安全、并发控制 |
| 基础设施 | ~6 | TLS/ACME、CDN 集成、Helm chart |
| API 工程 | ~4 | 错误协议统一、版本化策略 |
| 导入/迁移 | ~3 | 从 S3 导入 |

**本期 5 个方向在前 24 期分析中均无实质性覆盖**，且不与 ROADMAP.md 中的 10 个方向重叠。

---

## 本期方向总览

| # | 方向 | 类型 | 严重度 | 既有覆盖 |
|---|------|------|--------|---------|
| 1 | **服务端拷贝协议与存储层 Copy 原语** | 性能/架构 | 🔴 大型对象拷贝浪费 2 倍带宽 + 内存 | **零覆盖** |
| 2 | **存储后端在线迁移（Live Backend Migration）** | 运维/架构 | 🔴 唯一变更后端的方式是停机重搭 | **零覆盖** |
| 3 | **对象级访问审计轨迹（Object Access Trail）** | 合规/安全 | 🟠 方法已定义但 handler 从未调用 | v21 方向三提及后半段但**未聚焦** |
| 4 | **不可变存储 & 内容寻址模式（Immutable / Content-Addressable）** | 特性/合规 | 🟠 审计日志、区块链、数据湖场景必须 | **零覆盖** |
| 5 | **批量操作框架（Bulk Operations Framework）** | 运维/平台 | 🟠 百万级对象运维行为缺少编排层 | **零覆盖** |

---

## 1. 🔴 服务端拷贝协议与存储层 Copy 原语

### 现状

当前 `storage.Storage` 接口没有任何 `Copy` 方法。S3 兼容协议中的 `CopyObject`（`internal/api/s3compat/extra.go:39`）实现方式为：

```go
func (h *Handler) copyObject(w http.ResponseWriter, r *http.Request, ...) {
    rc, src, err := h.svc.Get(ctx, tenant, srcBucket, srcKey) // ← 读取源
    defer rc.Close()
    // ...
    dst, err := h.svc.Put(ctx, tenant, dstBucket, dstKey, rc, src.Size, opts) // ← 写入目标
}
```

这意味着：
- **每个 CopyObject 请求都经过「服务端内存」中转**。即使源和目标位于同一个 S3 bucket，数据也会从 S3 下载到 aero-vault 服务器的内存，再重新上传到 S3。
- **无法使用云后端的服务端拷贝 API**。AWS S3 的 `PUT Object - Copy` 是服务端操作，不传输数据。阿里云 OSS、腾讯云 COS 也有相同的 `CopyObject` API。当前实现使这些后端的成本优势（零数据传输费、零服务器带宽）完全丧失。
- **不存在 `UploadPartCopy`**。S3 的 `UploadPartCopy` 允许分片上传的每个 Part 从现有对象拷贝，无需客户端下载再上传。当前完全缺失，使用 S3 multipart API 的用户无法在 aero-vault 上做跨对象的分片拷贝。
- **跨 bucket 拷贝会在同一个存储后端内部产生不必要的数据传输**。对于 local FS 后端，这是两次磁盘读取 + 一次写入；对于 S3 后端，这是跨 region 数据传输费用（如果源和目标在不同 bucket/region）。

### 为什么需要

| 理由 | 影响 |
|------|------|
| **性能** | 拷贝 1 GB 对象：当前实现需要服务器分配 1 GB 缓冲区（或 spill to disk），传输 2 GB 出向 + 2 GB 入向带宽。服务端拷贝只需发送一个 HTTP 请求。 |
| **成本** | 云后端数据传出有费用（AWS S3 出向 $0.09/GB）。对于大数据集拷贝（如版本迁移、桶间复制），费用可观。 |
| **S3 兼容性断裂** | AWS SDK 和 S3 工具（aws s3 cp, rclone）在拷贝大对象时会自动使用 `UploadPartCopy` 进行并发拷贝。没有这个能力，这些工具对大文件表现极差或退化到单线程。 |
| **分层转换的基础** | 存储分层（STANDARD → GLACIER）的转换操作本身就是一种 Copy（设置新 tier + 删除旧 blob），没有服务端 Copy 就无法高效实现。 |

### 建议的接口变更

```go
type Storage interface {
    // 现有方法……
    
    // Copy 在存储后端内部复制一个对象，返回新对象信息。
    // 当源和目标在同一后端时，实现应使用服务端拷贝。
    // 当后端不支持服务端拷贝时，回退到 Get + Put。
    Copy(ctx context.Context, srcKey, dstKey string, opts PutOptions) (ObjectInfo, error)
}
```

| 影响的层 | 范围 |
|---------|------|
| `storage.Storage` 接口 | 新增 `Copy` 方法签名 |
| `storage.LocalStorage` | 实现 = CopyFile + metadata clone |
| `storage.S3Storage` | 使用 `PutObjectInput.CopySource` 实现服务端拷贝 |
| `storage.OSSStorage` | 使用阿里云 `CopyObject` API |
| `storage.COSStorage` | 使用腾讯云 `CopyObject` API |
| `service/file_crud.go` | 新增 `CopyObject` 方法，优先使用存储层 Copy，回退到现有 Get+Put |
| `internal/api/s3compat/extra.go` | `copyObject` 改为调用 `svc.CopyObject` |
| `internal/api/s3compat/handler.go` | 新增 `UploadPartCopy` 路由（`?partNumber&uploadId&copySource`） |

### 边界情况

- **同 key 拷贝（就地覆盖）**：需要先读源、写目标到临时 key、再交换——或由调用方确保 key 不同。
- **跨后端拷贝**：如果源在 local FS、目标在 S3，Copy 必须回退到 Get+Put 模式。
- **版本化 bucket**：拷贝到已存在的 key 在新版本化 bucket 中应创建新版本。
- **SSE 加密对象**：服务端拷贝需要确保目标 key 使用正确的 SSE key（可能同源、也可能换新 key）。
- **大对象（>5GB）**：AWS S3 服务端拷贝有 5GB 上限；超过的需要使用 `UploadPartCopy`。本地实现可以放宽这个限制。

---

## 2. 🔴 存储后端在线迁移（Live Backend Migration）

### 现状

aero-vault 支持四种存储后端：`local`、`s3`、`oss`、`cos`。但是：

- **存储后端由启动配置决定**，运行时不可变。
- **没有任何迁移工具**可以将一个后端的数据迁移到另一个后端。
- 已有的 `replication.Worker` 用于跨区异步复制，但：
  - 它是**持续运行**的，不是「一次性迁移」。
  - 它**写两个后端**（primary + replica），而不是「把一个后端的存量数据搬到另一个」。
  - 它不处理 metadata 层的存储 key 重写（storage key 包含后端信息）。
- 如果运维团队想从 local FS 迁移到 S3（或从 S3 迁移到 OSS），**唯一的路径是**：
  1. 启动一个新的空实例指向目标后端
  2. 手动遍历所有对象，逐个 Get → Put
  3. 切 DNS 流量
  4. 停机窗口内完成

### 为什么需要

| 理由 | 影响 |
|------|------|
| **运维现实** | 几乎每个生产部署都会经历至少一次存储后端变更：POC 阶段用 local FS → 上线后切到 S3 → 成本优化后切到 OSS。 |
| **供应商锁定反制** | 如果用户想从 AWS S3 迁移到阿里云 OSS，当前没有任何可行路径——存量数据无法迁移。 |
| **已有复制的架构铺垫** | `replication.Worker` 已经实现了对象级别的异步拷贝框架。在其基础上增加"模式切换"（持续复制 vs. 一次性同步）、"迁移完成检测"、"读流量切换"是增量工程。 |
| **服务连续性** | 停机迁移在大规模场景下不可接受。需要在线迁移——先双写 + 回填存量 → 验证 → 切读 → 关旧后端。 |

### 建议的架构

```
Phase 1: 配置目标后端 → 启动双写（所有写入同时写两个后端）
Phase 2: 后台回填存量对象（遍历所有 storage key，单向拷贝到目标后端）
Phase 3: 验证（对比源/目标的 etag + size，随机 spot-check 内容）
Phase 4: 切换读指针（所有读请求优先从目标后端读取；失败时降级到源）
Phase 5: 清理源后端（确认无误后释放源后端资源）
```

| 影响的层 | 范围 |
|---------|------|
| `internal/storage/factory.go` | 新增支持「双后端模式」（Primary + MigratingTo） |
| `internal/storage/migration.go` | 新增 `MigrationManager` 实体，封装 5 阶段状态机 |
| `internal/repository/sql_objects.go` | 新增 `ListAllObjectIDs` 用于回填游标；新增 `SetBackend` 更新对象的后端记录 |
| `internal/repository/sql_migrations.go` | migration 表新增 `backend_migration` 记录迁移进度 |
| `internal/api/rest/admin.go` | 新增 `POST /admin/storage/migrate`（启动迁移）、`GET /admin/storage/migrate/status`（查看进度）、`POST /admin/storage/migrate/commit`（切换读）、`POST /admin/storage/migrate/rollback`（回滚） |
| `internal/config` | 新增 `STORAGE_MIGRATION_TARGET`（目标后端配置别名） |
| `internal/jobs/` | 新增 job type `backend_migrate`，可 enqueue 成千上万个对象回填任务 |

### 边界情况

- **双写期间的一致性**：如果两个后端写入都成功但结果不同（如 etag 不同），以源为准。
- **回填期间的增量更新**：回填一个对象后，该对象又被 PUT 更新了——回填任务需要检测 `updated_at` 并在覆盖前重新拷贝。
- **存储 key 的改变**：不同后端可能使用不同的 key 格式（local 用文件路径，S3 用 object key）。`storageKey(tenant, bucket, key)` 的计算方式不应绑定到后端。
- **迁移期间的后端故障**：如果目标后端在 Phase 2 期间不可用，应暂停迁移、保持双写、告警、而不是丢失数据。
- **SSE 密钥一致性**：如果源后端和目标后端的 SSE 密钥不同，迁移过程中需要对数据重新加密。

---

## 3. 🟠 对象级访问审计轨迹（Object Access Trail）

### 现状

代码库中存在完整的「桶日志配置」骨架：

- `repository.BucketConfig.LoggingTarget` / `LoggingPrefix`（`repository.go:52`）
- `repository.Repository.WriteAccessLog(ctx, tenant, sourceBucket, method, key, status, latencyMs, userAgent)`（`repository.go:274`）
- `service.GetBucketLogging` / `SetBucketLogging` / `DeleteBucketLogging`（`file_features.go`）
- `rest.Handler` 中 GetBucketLogging / PUT / DELETE 路由（`handler.go:503–541`）
- S3 XML schema 中的 ServerAccessLogging 类型已定义（`s3compat/xml.go:372`）
- 迁移 `0023_bucket_logging` 已为 logging 配置准备好存储

**但是：`WriteAccessLog` 从未被任何 handler、middleware 或业务逻辑调用。** 这意味着：

- 即使运维打开了桶的 logging 配置，**没有任何访问日志被写入**。
- middleware 层的 `AccessLog`（`middleware/middleware.go:85`）只输出到 slog——**不是持久的、可审计的日志记录**。
- 对于合规审计（SOC2、HIPAA、PCI DSS），「对象级访问记录」是必选项，当前系统完全缺失。

### 为什么需要

| 理由 | 影响 |
|------|------|
| **合规审计红线** | SOC2、PCI DSS、HIPAA、FedRAMP 都要求记录谁在什么时间访问了什么数据。没有这个能力，金融/医疗/政府客户无法通过合规审查。 |
| **安全取证** | 数据泄露后，第一个问题永远是「哪些对象被谁访问了？」。没有访问日志，安全团队无法做 forensic analysis。 |
| **使用分析 / 成本归因** | 访问日志可以分析哪些对象是热的（大量 GET）、哪些是冷的（从未被访问），从而指导生命周期策略。这是存储分层的前提条件。 |
| **实现成本低** | `WriteAccessLog` 接口已存在，logging 配置 CRUD 已就位。缺失的是一个 **middleware** 或 **handler 链中的日志点**来调用它。 |

### 建议的集成方式

高层级设计：

```
Request → AccessLogMiddleware → [记录到 slog]  ← 已有的
                                 ↓
                         BucketLoggingFilter
                                 ↓
                    ┌────────────┴────────────┐
                    │ 匹配请求的 sourceBucket   │
                    │ 检查 BucketConfig 中      │
                    │ LoggingTarget 是否为空     │
                    └────────────┬────────────┘
                                 ↓ (有配置)
                    WriteAccessLog(ctx, …) → 写入目标桶
```

`BucketLoggingFilter` 逻辑：

1. 从请求中提取 `tenant`、`bucket`、`method`、`key`、`status`、`latency`、`user-agent`
2. 查询 `BucketConfig.LoggingTarget` ——如果为空则跳过
3. 构造日志条目（S3 格式或自定义 JSON）
4. 调用 `repo.WriteAccessLog` 写入目标桶

| 影响的层 | 范围 |
|---------|------|
| `internal/middleware/middleware.go` | 新增 `AccessLogWriter(repo)` middleware，在请求完成后调用 `WriteAccessLog` |
| `internal/repository/sql_buckets.go` | `WriteAccessLog` 需要实际写入日志对象到目标桶（或日志表）。当前实现只写一条 slog——需要改造 |
| `internal/service/file_features.go` | 可选：暴露 `WriteAccessLog` 方法供 middleware 调用 |
| `internal/api/rest/handler.go` + `s3compat/handler.go` + `webdav/dav.go` | 所有协议入口需要确认统一经过 logging middleware |
| Migration | 新增 `access_logs` 表（或日志对象命名约定 + 轮转策略） |

### 边界情况

- **日志对象自身产生的递归**：写日志到目标桶时又会产生事件和访问记录。必须通过 `x-aero-logging: true` 标记或 `sourceBucket != targetBucket` 检查来跳过。
- **高吞吐下的写入压力**：每请求一次 DB 写可能成为瓶颈。需要设计批量异步写入（每 N 条或每 T 毫秒 flush 一次）。
- **日志格式标准化**：S3 有标准的日志格式（`<bucket_owner> <bucket> <time> <remote_ip> <requestor> <request_id> <operation> <key> ...`）。如果要做 S3 兼容，必须对齐这个格式。
- **日志轮转与生命周期**：日志对象本身也占存储空间。需要设计轮转策略（按小时/天分桶）和在 logging 桶上的生命周期配置。

---

## 4. 🟠 不可变存储 & 内容寻址模式（Immutable / Content-Addressable Storage）

### 现状

当前 aero-vault 使用的是**纯键值覆盖模型**：
- PUT `my-key` = 写入（覆盖已有值或新建）
- DELETE `my-key` = 删除（软删除或硬删除）
- 一个 key 最多只有一个「当前版本」（加上 N 个版本化历史版本）

不存在以下模式：
- **WORM（Write Once Read Many）桶**：写入后的对象永远不能被修改或删除（即使没有开启 versioning）。
- **内容寻址存储**：对象以内容哈希（SHA256）为 key，相同的文件自动去重。
- **Append-Only 模式**：日志、审计、事件溯源场景需要只追加不删除的存储语义。
- **不可变版本链**：写入后产生不可擦除的版本记录（即使 versioning 为 off）。

### 为什么需要

| 理由 | 影响 |
|------|------|
| **合规刚性需求** | SEC Rule 17a-4 要求电子存储记录不可擦除、不可重写。金融行业的邮件归档、交易记录必须使用 WORM 存储。当前只有 object lock（有限期的保留），没有真正的 WORM 桶。 |
| **数据湖完整性** | 数据湖 / 数据管道中，原始数据应该不可变——一旦摄入就不该在源头被修改。当前模型允许后续 PUT 覆盖原始数据，破坏了数据血统。 |
| **内容去重存储** | 很多用户上传大量重复文件（如 CI 构件、容器镜像层）。内容寻址 + 去重可以节约 50–80% 的存储空间。当前每个 PUT 都写独立 blob，没有去重。 |
| **与现有功能的互补** | 内容寻址 WORM 桶可以与 versioning 共存：versioning 内每个版本不可变，但仍允许创建新版本；WORM 桶则是完全不允修改。 |

### 建议的模式分类

| 模式 | 桶级别配置 | 行为 |
|------|-----------|------|
| **标准模式**（当前） | 默认 | PUT 覆盖，DELETE 删除 |
| **不可变模式** | `x-aero-bucket-immutable: true` | PUT 永远创建新版本（即使 versioning off）；DELETE 返回 403；旧版本永不被 GC |
| **内容寻址模式** | `x-aero-bucket-content-addressed: true` | key 被忽略（或作为元数据），实际存储 key = `sha256(content)`；相同内容只存一份 |
| **Append-Only 模式** | `x-aero-bucket-append-only: true` | PUT 只能创建新对象；DELETE 返回 403；PUT 到已存在 key 返回 409 |

| 影响的层 | 范围 |
|---------|------|
| `internal/repository/repository.go` | `BucketConfig` 新增 `Immutable` / `ContentAddressed` / `AppendOnly` flag |
| `internal/service/file_crud.go` | `Put` 方法根据桶模式执行不同语义：不可变模式强制创建新版本；内容寻址模式计算 SHA256 并去重 |
| `internal/service/file_features.go` | 新增 `SetBucketImmutable` / `SetBucketContentAddressed` |
| `internal/storage/local_write.go` | 内容寻址模式下，存储 key 重写为 `sha256(content)`；引用计数管理 |
| `internal/reconcile/` | 不可变桶的 GC 必须跳过所有对象（即使软删除标记） |
| `internal/repository/sql_buckets.go` | 新增 migration `0025_bucket_immutability` |
| `internal/api/s3compat/bucketconfig.go` | S3 的 `ObjectLockConfiguration` 扩展为支持 `RetentionMode=COMPLIANCE`（当前只有 bucket-level lock seconds） |
| `internal/auth/policy.go` | 新增 `s3:BypassGovernanceRetention` / `s3:PutObjectRetention` 等 IAM action |

### 边界情况

- **内容寻址 + 版本化**：如果版本化桶做了内容寻址，同一 key 的多个版本如果内容相同应指向同一个 blob（引用计数）。
- **不可变桶的迁移**：将标准桶改为不可变桶时，已有的对象应该如何处理？是否追溯锁定？
- **内容寻址 + 大文件**：1 GB 文件的内容寻址需要计算全文件 SHA256——必须在写入完成（或分片完全上传）后才能确定 key。
- **性能影响**：内容寻址在写入路径上增加了 SHA256 计算和去重查找。需要基准测试保证性能可接受。
- **与 AI 管线的交互**：内容寻址桶中的同名对象可能存在多次，AI 索引需要能处理「多版本引用同一 blob」的场景。

---

## 5. 🟠 批量操作框架（Bulk Operations Framework）

### 现状

当前大规模运维操作没有统一的编排层：

| 操作 | 当前实现 | 问题 |
|------|---------|------|
| 批量删除 | `BatchDelete`（`service/file_features.go`），循环调用 `Delete` | 同步、单线程、无进度、不可取消 |
| 批量打标签 | `BatchSetTags`，同上 | 同上 |
| SSE key 轮换 | `RewrapStale`（`storage/rewrap.go`），单次启动遍历 | 一次性、不可暂停/续传、无细粒度进度 |
| Reindex | `ReindexStale`（`ai/reindex.go`），`LIMIT 1000` 批量 | 小批量、同步、不可取消 |
| 生命周期过期 | `LifecycleJob`（`reconcile/lifecycle.go`），单 Ticker 循环 | 不可手动触发、不可按前缀或标签过滤 |
| 存储后端迁移 | 不存在 | — |
| 批量变更存储类 | 不存在 | — |
| 批量更新元数据 | 不存在 | — |

**缺少的共同能力：**
- 统一的 `BulkJob` 抽象：scope（全部 / 前缀 / 标签 / SQL 条件）、速率限制、进度跟踪、取消、续传。
- 操作记录：谁在什么时候做了什么批量操作、结果如何。
- 对租户/API 的保护：大规模批量操作不应影响正常读写延迟。

### 为什么需要

| 理由 | 影响 |
|------|------|
| **运维现实** | 真实世界的运维场景几乎都是批量操作：「给所有带有 `project=foo` tag 的对象加一个 `archived=true` 标签」、「把 /logs/2024/ 路径下所有对象降冷到 STANDARD_IA」、「重加密所有使用旧 SSE key 的对象」。 |
| **用户体验** | 单个 API 调用处理 100 万个对象，用户希望得到的是 `{"job_id": "...", "status": "running"}`，而不是 5 分钟后超时或 503。 |
| **已有基础设施** | Job pool、事件总线、进度指标（telemetry）都已就位。批量操作天然适配 job 模式：一个「调度 job」拆成 N 个「工作 job」并行执行。 |
| **与存储迁移、SSE 轮换、Reindex 的复用** | 这三个功能本质都是批量操作。如果先建立统一的 BulkJob 框架，它们都会简化实现并获得进度跟踪、暂停/续传等能力。 |

### 建议的架构

```go
// 统一批量操作接口
type BulkOperation struct {
    ID         string            // uuid
    Type       string            // "copy" | "tag" | "reindex" | "rewrap" | "migrate" | "class-change"
    Scope      BulkScope         // 操作范围
    Throttle   int               // 每秒最大操作数（0 = 不限制）
    Status     string            // "pending" | "running" | "paused" | "completed" | "failed" | "cancelled"
    Progress   BulkProgress      // processed, total, failed, errors
    CreatedAt  time.Time
    CompletedAt *time.Time
    CreatedBy  string            // 操作发起人（audit）
}

type BulkScope struct {
    Tenant      string
    Bucket      string
    Prefix      string            // key 前缀过滤
    Tags        map[string]string // 标签过滤
    Query       string            // SQL WHERE 条件（管理员使用）
    StorageClass string           // 存储类过滤
}

// 进度跟踪
type BulkProgress struct {
    Total       int64
    Processed   int64
    Succeeded   int64
    Failed      int64
    Errors      []BulkError       // 最近 N 条错误
}
```

| 影响的层 | 范围 |
|---------|------|
| `internal/jobs/` | 新增 `BulkManager`：创建批量 job、拆分子任务、跟踪进度、支持 pause/resume/cancel |
| `internal/repository/sql_bulk.go` | 新增 `bulk_operations` 表 + `bulk_sub_tasks` 表，持久化进度以便续传 |
| `internal/repository/repository.go` | 新增 `BulkOperation` 相关 CRUD 接口 |
| `internal/service/file_features.go` | 所有现有批量方法（`BatchDelete`、`BatchSetTags`）增加 BulkJob 路径；新增 `BulkChangeStorageClass`、`BulkUpdateMetadata`、`BulkCopyByPrefix` |
| `internal/api/rest/admin.go` | 新增 `POST /admin/bulk`（创建）、`GET /admin/bulk/:id`（查询）、`POST /admin/bulk/:id/pause`、`POST /admin/bulk/:id/resume`、`POST /admin/bulk/:id/cancel` |
| `internal/telemetry/metrics.go` | 新增 `bulk_operations_total`、`bulk_operations_duration_seconds`、`bulk_items_processed_total` 指标 |

### 边界情况

- **部分失败的处理**：批量操作中某些子任务失败时，整体状态应为 `completed_with_errors`，可通过 `GET /admin/bulk/:id/errors` 查看失败的项。
- **幂等性**：批量标记操作应当是幂等的——如果因为网络问题重试了一个子任务，不应产生副作用。
- **操作对租户的影响**：批量操作应遵循租户的 rate limit，不因后台批量任务影响前台用户请求。建议批量操作使用独立的 worker pool（`JOBS_WORKERS` 的子集）。
- **取消语义**：`cancel` 应阻止新子任务启动，但已运行的子任务应允许完成（或强制终止）。`pause` 则是优雅暂停——等待运行中的子任务完成后暂停调度。
- **超大范围（数百万对象）**：`Total` 计数可以通过 `COUNT(*)` 预计算，但精确进度需要游标式枚举。建议使用 `estimated_total` + 实际 `processed`。

---

## 优先级排序与依赖关系

```
Phase 1（短期，1–2 周）
├── Server-Side Copy & UploadPartCopy
│   └── 依赖：storage.Storage 接口变更 → Service 层 → S3 handler
│   └── 收益：CopyObject 性能提升 10–100x（云后端）
│   └── 风险：低（向后兼容，回退到 Get+Put）
│
└── Object Access Trail
    └── 依赖：middleware 层新增日志写入点
    └── 收益：合规审计达标
    └── 风险：低（与现有逻辑正交）

Phase 2（中期，2–4 周）
├── Bulk Operations Framework
│   └── 依赖：jobs 层 + repository 新表
│   └── 收益：所有批量操作获得统一编排
│   └── 风险：中（新抽象层需要与既有批量方法兼容）
│
└── Immutable / Content-Addressable Storage
    └── 依赖：BucketConfig 扩展 + Service 层分支逻辑
    └── 收益：获取合规 + 高价值场景准入
    └── 风险：中（去重引入引用计数复杂性）

Phase 3（长期，4–6 周）
└── Storage Backend Live Migration
    └── 依赖：Bulk Operations Framework（Phase 2）+ 双写架构
    └── 收益：唯一解决后端变更问题
    └── 风险：高（状态机复杂性 + 数据一致性保障）
```

---

*本文档不包含任何实现代码。分析基于 `b6a7c4d` (HEAD) 的全代码库静态扫描。*
