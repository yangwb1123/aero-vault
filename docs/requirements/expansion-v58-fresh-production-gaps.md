# 高价值扩展方向：生产级缺口分析

> 基于当前代码库（HEAD `cae49d9`）的全局扫描，聚焦于协议层已实现但执行层缺失的功能断层。
> 每个方向标注了对应的代码位置与现有基础设施的复用点。

---

## 方向一：S3 事件通知真实投递引擎（Notification Rule Delivery）

**现状定位：** 配置管道完整，投递为零。

| 层级 | 文件 | 状态 |
|------|------|------|
| S3 XML 解析 | `internal/api/s3compat/xml.go` 第 395 行 `notificationConfiguration` | ✅ 已实现 |
| S3 路由分发 | `internal/api/s3compat/handler.go` 第 767–810 行 | ✅ 已实现 |
| REST API | `internal/api/rest/handler.go` 第 551–582 行 | ✅ 已实现 |
| Service 层 | `internal/service/file_features.go` 第 261–273 行 | ✅ 已实现 |
| Repository 层 | `internal/repository/sql_buckets.go` 第 381–417 行 | ✅ 已实现 |
| Schema 迁移 | `migrations/{sqlite,postgres}/0024_bucket_notifications.up.sql` | ✅ 已应用 |
| **真实投递 → SQS/SNS/Lambda** | **不存在** | ❌ **完全缺失** |

**为什么需要：** 用户在 S3 兼容 API 上配置了通知规则（`PUT ?notification`），底层 `notification_rules` 列也确实写入了 JSON，但没有任何代码读取该配置并实际发送事件到队列、主题或函数。这是一个静默失败的假配置——客户端得到 `200 OK`，但永远不会收到任何事件。对于面向事件驱动架构（EDA）的工作负载，这是直接迁移阻止项。

**可复用的基础设施：**
- 事件总线 `internal/events/bus.go` 已有 `Subscribe()` 通道和 `Webhook` 投递器
- `internal/events/webhook.go` 具备 HMAC 签名 + 持久化重试
- 工作池 `internal/jobs` 提供异步调度能力
- 通知规则中的 `QueueARN` 可直接映射为 `events.Webhook` 的 URL

**建议实现方案：** 新增 `NotificationDispatcher` 组件，订阅总线事件，根据事件的 `(tenant, bucket, key)` 匹配所有已配置的通知规则的 `FilterKey` 和 `Events`，匹配成功则通过 webhook 基础设施投递。`QueueARN` 支持 `https://` 和 `arn:aws:sqs:`/`arn:aws:sns:` 格式即可覆盖 80% 场景。

---

## 方向二：存储类生命周期转换 + 冰川恢复（Storage Class Transition & Glacier Restore）

**现状定位：** 存储类字段全链路贯通，但生命周期只做删除，不做转换；`POST ?restore` 实现的只是软删除恢复而非归档恢复。

| 组件 | 位置 | 状态 |
|------|------|------|
| StorageClass 模型 | `internal/repository/repository.go:34` `Object.StorageClass` | ✅ 已持久化 |
| 写入管道 | `internal/service/file_crud.go:buildPutObject` | ✅ 已接收 `x-amz-storage-class` |
| S3 查询参数 | `internal/api/s3compat/handler.go:writeObjectHeaders` | ✅ 已输出 `x-amz-storage-class` |
| 分桶计数 | `internal/repository/sql_objects.go:337` `StorageClassCounts` | ✅ 已有 SQL |
| 度量暴露 | `internal/telemetry/metrics.go:181` `RegisterStorageClassGauge` | ✅ 已接入 Prometheus |
| **生命周期转换规则** | `internal/repository/repository.go:30` `ExpireAction` 只有 `soft_delete`/`hard_delete` | ❌ **无 transition 类型** |
| **执行引擎** | `internal/reconcile/lifecycle.go` 只扫描过期 → 删除 | ❌ **不做转换** |
| **POST ?restore** | `internal/api/s3compat/handler.go:880` → `service.RestoreObject` → `repo.RestoreObject`（清除 deleted_at） | ❌ **实际上不是归档恢复** |

**为什么需要：** 存储类字段 (`STANDARD`/`STANDARD_IA`/`GLACIER`) 已经全栈贯通，但其价值在于自动转换——对象创建时是 `STANDARD`，30 天后自动转为 `STANDARD_IA`，90 天后转为 `GLACIER`。没有转换逻辑，`storage_class` 就是一列无法驱动任何行为的数据。同时，`POST ?restore` 当前调用的是软删除恢复，对 `GLACIER` 类对象的正确语义应该是：启动异步取回 → 对象临时变为可读 → 到期后还原为归档状态。

**可复用的基础设施：**
- `internal/reconcile/lifecycle.go` 已经是周期扫描框架，只需扩展动作类型
- `BucketConfig.ExpireAfterDays` 模式可扩展为 `TransitionAfterDays + TargetClass` 数组
- `internal/jobs` 工作池可执行异步恢复任务
- `storage.Storage` 接口可添加 `Restore(key, days)` 方法（S3 原生支持）

**建议实现方案：** 扩展 `BucketConfig` 生命周期规则以支持 `Transition{AfterDays, TargetClass}` 序列；`reconcile.Lifecycle` 扫描到符合条件的对象时调用 `storage.Copy()` 到目标后端 + 更新 `StorageKey` + `StorageClass`；`POST ?restore` 在 `GLACIER` 类对象上触发 `storage.Restore()`（S3 暂存副本）并设置 `restore_expires_at`。

---

## 方向三：过期分片上传自动清理（Stale Multipart Upload Lifecycle）

**现状定位：** `multipart_uploads` 表只有创建和手动中止，没有任何自动过期机制。

| 组件 | 位置 | 状态 |
|------|------|------|
| 分片上传表 | `migrations/sqlite/0001_init.up.sql` `multipart_uploads` | ✅ 已持久化 |
| 创建 API | `internal/api/s3compat/extra.go:212` `createMultipartUpload` | ✅ 已实现 |
| 列出 API | `internal/api/s3compat/extra.go:272` `listMultipartUploads` | ✅ 已实现 |
| 手动中止 | `internal/api/s3compat/extra.go:222` `abortMultipartUpload` | ✅ 已实现 |
| 计数 | `internal/repository/sql_uploads.go:48` `ListUploads` | ✅ 已有 |
| **自动过期/清理** | **不存在** | ❌ **完全缺失** |

**为什么需要：** S3 的默认行为是在分片上传初始化 7 天后自动中止未完成的上传。当前实现中，如果客户端在 `InitMultipart` 后崩溃、网络中断或主动放弃，所有已上传的分片数据会永久占用存储空间（在多分片上传中，部分数据可能已经写入存储后端）。在内存中或使用 S3 后端时，这些孤儿分片会持续产生费用。对于大规模生产环境，缺少自动清理意味着静默存储泄漏。

**可复用的基础设施：**
- `internal/reconcile/retention.go` `RetentionJob` 已经是按周期间隔执行清理的框架
- `repository.ListUploads` 可带 `before` 时间戳查询
- `service.AbortMultipart` 已有完整的中止逻辑（清理存储后端 + 删除数据库行）
- 集群单例 `cluster.Singleton` 可确保跨副本只执行一次

**建议实现方案：** 在 `RetentionJob` 中新增 `staleUploadTTL`（默认 7 天），每次执行时查询 `created_at < NOW() - TTL` 的所有分片上传，逐一调用 `AbortMultipart`。暴露配置项（如 `RECONCILE_STALE_UPLOAD_HOURS`），支持用户自定义窗口。

---

## 方向四：S3 分片上传最小分片大小强制（Multipart Min Part Size Enforcement）

**现状定位：** `UploadPart` 对 `r.ContentLength` 不加任何验证。

```go
// internal/api/s3compat/extra.go:161
func (h *Handler) uploadPart(w http.ResponseWriter, r *http.Request, uploadID string, partNumber int) {
    if partNumber < 1 || partNumber > 10000 {   // 校验了编号范围
        writeS3Error(w, r, ...)
        return
    }
    part, err := h.svc.UploadPart(r.Context(), uploadID, int32(partNumber), r.Body, r.ContentLength)
    // 没有校验 size 下限
}
```

```go
// internal/service/file_multipart.go:UploadPart 直接透传
func (s *FileService) UploadPart(ctx context.Context, uploadID string, partNumber int32, r io.Reader, size int64) (repository.Part, error) {
    // 没有 size >= 5MB 检查
}
```

**为什么需要：** AWS S3 要求除最后一片外，每片分片 ≥ 5 MiB（`UploadPart` 返回 `EntityTooSmall`）。当前无此检查意味着：
1. 客户端将大对象拆分为极多小分片（例如 1 GB 拆成 1000 个 1 MB 分片）——**会导致存储后端的 API 调用次数爆炸**，S3/OSS/COS 均按调用次数计费。
2. 上报 `CompleteMultipart` 时所有小分片会合并成大对——小分片本身是存储和计费的浪费。
3. 与主流 S3 SDK（AWS CLI、boto3）的交互会因行为不一致而产生混淆：SDK 默认 8 MiB 分片，若用户显式指定更小值，期望收到 `EntityTooSmall`。

**建议实现方案：** 在 `file_multipart.go:UploadPart` 中新增 `partsCount` 上下文感知检查——非最后一片（通过 `repo.PartCount(uploadID) + 1 < expectedParts` 判断，或更简单的惰性方案：`CompleteMultipart` 时统一校验每片大小）。但 AWS 在 `UploadPart` 阶段就拒绝，所以推荐在 `UploadPart` 中立即拒绝。最小分片大小设为 5 MiB（`5 * 1024 * 1024`），通过 `MIN_PART_SIZE` 常量暴露。

---

## 方向五：S3 对象锁治理模式与合规模式分离（Object Lock Governance vs Compliance Mode）

**现状定位：** Object Lock 配置只存储 `seconds`，`Mode` 在写入 Repository 时被丢弃。

```go
// internal/api/s3compat/bucketconfig.go:putBucketObjectLock
func (h *Handler) putBucketObjectLock(w http.ResponseWriter, r *http.Request, bucket string) {
    var in objectLockConfiguration
    if err := decodeBucketBody(r, &in); err != nil { ... }
    seconds := 0
    if in.Rule != nil {
        seconds = in.Rule.DefaultRetention.Days * 86400     // ← 只取了 Days，Mode 被丢弃
    }
    if err := h.svc.SetBucketObjectLock(r.Context(), mw.TenantFrom(r.Context()), bucket, seconds); err != nil { ... }
}

// GET ?object-lock 始终返回 GOVERNANCE
func (h *Handler) getBucketObjectLock(...) {
    out.Rule = &objectLockRule{DefaultRetention: objectLockRetention{
        Mode: "GOVERNANCE",     // ← 硬编码，忽略了实际存储的模式
        Days: days,
    }}
}
```

```go
// internal/service/file_features.go:SetBucketObjectLock → repository.SetBucketObjectLock
// 只接受 durationSeconds，模式信息丢失：
func (s *FileService) SetBucketObjectLock(ctx context.Context, tenant, bucket string, durationSeconds int) error {
    return s.repo.SetBucketObjectLock(ctx, tenant, bucket, durationSeconds)
}

// internal/repository/sql_buckets.go:SetBucketObjectLock
func (s *sqlStore) SetBucketObjectLock(ctx context.Context, tenant, bucket string, seconds int) error {
    _, err := s.db.ExecContext(ctx, s.rebind(`UPDATE buckets SET object_lock_seconds=$1 WHERE tenant_id=$2 AND name=$3`), seconds, tenant, bucket)
    // object_lock_mode 根本没有列
}
```

**为什么需要：** Governance 模式和 Compliance 模式有根本的安全差异：

| 特性 | Governance | Compliance |
|------|-----------|------------|
| 谁可以绕过 | 拥有 `s3:BypassGovernanceRetention` 权限的用户 | **任何人都不能** |
| 锁定后对象 | 可被特权用户删除/覆盖 | 不可删除/不可覆盖/不可修改 |
| 合规审计要求 | 常见 | SOC2/PCI 硬要求 |

当前实现将 `GOVERNANCE` 硬编码在 GET 响应中，且 `putBucketObjectLock` 丢弃了用户指定的模式。对于一个声称具备 Object Lock 能力的系统，这会产生安全幻觉——用户配置了 Compliance Mode，认为数据不可变，但实际上底层 `LockedUntil` 检查仅是一个简单的 `time.After()` 比较，没有任何特权绕过路径，也没有真正的 Compliance 语义。

**可复用的基础设施：**
- Schema 迁移模式已存在（如 `0024_bucket_notifications` 的 `ALTER TABLE buckets ADD COLUMN`）
- `repository.Object.LockedUntil` 已有，只需添加 `LockMode string` 字段
- `service.hardDeleteObject` 已有 `LockedUntil` 检查，可扩展为检查 `LockMode`

**建议实现方案：**
1. 新增迁移 `0025`：`ALTER TABLE buckets ADD COLUMN object_lock_mode TEXT NOT NULL DEFAULT 'GOVERNANCE'`
2. 扩展 `BucketConfig` 和 `SetBucketObjectLock` 签名以接受 `mode string`
3. `putBucketObjectLock` 从 XML 解析 `Mode` 并存入库
4. `getBucketObjectLock` 从库读取真实模式
5. `hardDeleteObject` 在 `LockMode == "COMPLIANCE"` 且 `LockedUntil > now()` 时无条件拒绝（即使调用方有 "bypass" 签名）

---

## 汇总

| # | 方向 | 当前状态 | 影响范围 | 估算规模 | 风险 |
|---|------|---------|---------|---------|------|
| 1 | 通知规则真实投递引擎 | 配置全链路完成，投递为零 | EDA 用户迁移阻塞项 | ~300 行 + 测试 | 低 |
| 2 | 存储类生命周期转换 + 冰川恢复 | StorageClass 全栈贯通，生命周期只有删除 | 成本优化、归档场景 | ~500 行 + 测试 | 中 |
| 3 | 过期分片上传自动清理 | 只有手动中止 | 存储泄漏风险 | ~80 行 + 测试 | 低 |
| 4 | 分片上传最小大小强制 | 无校验 | 计费爆炸、API 滥用 | ~30 行 + 测试 | 低 |
| 5 | Object Lock Governance/Compliance 模式分离 | Mode 被静默丢弃 | 安全幻觉、合规硬要求 | ~150 行 + 迁移 + 测试 | 低–中 |

以上 5 个方向有一个共同特征：**协议层感知的语义已经在代码中存在（XML 结构体、路由、Service 签名），但执行层（Repository 存储、Worker 执行、校验逻辑）未落地**。每个方向利用了现有基础设施（事件总线、Job 池、Reconcile 框架、集群单例），扩展成本可控。
