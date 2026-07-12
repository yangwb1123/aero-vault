已完成对文档全部主要声明的全代码库验证。以下是逐方向的代码锚点交叉对照结果。

---

## 验证摘要

### ✅ 方向一：桶级事件通知引擎

| 声明 | 代码证据 | 准确性 |
|------|---------|--------|
| S3 `?notification` 完整解析 XML → `NotificationRule` | `handler.go:809-833` — `putBucketNotifications` 解析 Topic/Queue/Lambda 配置 | ✅ |
| 规则写入 `buckets.notification_rules` JSON 列 | `sql_buckets.go:404-410` — `SetBucketNotifications` | ✅ |
| `TopicARN`/`LambdaARN` 标记为 `unused, kept for compat` | `repository.go:63-64` — 注释精准 | ✅ |
| **事件总线完全不读取通知规则** | `bus.go:74` — `Publish` 直接 `b.broadcast(e)`，零规则检查 | ✅ |
| 所有消费者接收**所有**事件 | `main.go:630-631` — `indexer.Run(ctx, idxSub)`、`avw.Run`、`rw.Run` 均直接 `bus.Subscribe()` | ✅ |
| SSE 端点无 bucket 过滤 | `sse.go:95-115` — `liveStream` 仅过滤 `e.TenantID != tenant` | ✅ |

### ⚠️ 方向二：服务端访问日志

| 声明 | 代码证据 | 准确性 |
|------|---------|--------|
| `WriteAccessLog` 方法存在 | `repository.go:290` — 接口声明；`sql_buckets.go:370` — 实现 | ✅ |
| S3 `?logging` 端点完整 CRUD | `sql_buckets.go:334-364` — `GetBucketLogging`/`SetBucketLogging`/`DeleteBucketLogging` | ✅ |
| REST API 层完整 | `router.go` — `GET/PUT/DELETE /buckets/{bucket}/logging` | ✅ |
| 迁移文件完成 | `0023_bucket_logging.up.sql` (sqlite + postgres) | ✅ |
| **没有任何 handler/middleware 调用 `WriteAccessLog`** | `grep -rn "WriteAccessLog" internal/ --include='*.go'` — 仅定义处 2 处，调用处 0 | ✅ |
| AccessLog middleware 仅输出到 `slog` | `middleware.go:85-104` — 仅 `logger.Info("http", ...)` | ✅ |

**发现一处重要不准确：** 文档称 `WriteAccessLog` 有**"完整 SQL 实现"**，但实际代码是**空存根（no-op）**：

```go
func (s *sqlStore) WriteAccessLog(...) error {
    _ = tenant; _ = sourceBucket; _ = method; _ = key
    _ = status; _ = latencyMs; _ = userAgent
    return nil
}
```

不仅无调用者，实现本身也是空操作。这使得该路径比文档描述的**更"死"**。

### ✅ 方向三：Object Lock 完整治理模型

| 声明 | 代码证据 | 准确性 |
|------|---------|--------|
| 唯一 lockdown：`LockedUntil` 时间戳 | `repository.go:38` — `LockedUntil *time.Time` | ✅ |
| 无 `GOVERNANCE`/`COMPLIANCE` 模式 | `BucketConfig` 仅含 `ObjectLockSeconds int`，无 RetentionMode 字段 | ✅ |
| Legal Hold 存为元数据标志 | `handler.go:93-98` — `meta["_aero_legal_hold"] = "ON"` | ✅ |
| 元数据可被随意覆写 | 覆盖式 PUT 不会保留原元数据；`file_crud.go:371` 检查仅在硬删除时 | ✅ |
| 无 `PUT ?retention` 或 `PUT ?legal-hold` 端点 | `bucketconfig.go` 仅有 `get/putBucketObjectLock`（桶级） | ✅ |
| 无 `x-amz-bypass-governance-retention` 识别 | `grep -rn "bypass\|Bypass"` 无相关 Header 处理 | ✅ |

### ✅ 方向四：对象生命周期状态机

| 声明 | 代码证据 | 准确性 |
|------|---------|--------|
| 生命周期仅支持 `soft_delete`/`hard_delete` | `repository.go:48` — `ExpireAction string // "soft_delete" | "hard_delete"` | ✅ |
| 无 `Transition` 动作 | `bucketconfig.go:57-97` — `putBucketLifecycle` 仅读 `Expiration.Days`，忽略 `Transition` | ✅ |
| `storage_class` 字段存在但永不转换 | `repository.go:34` — `StorageClass string`；无任何转换 worker | ✅ |
| `?restore` 是软删除恢复 | `file_features.go:167-170` — `RestoreObject` → `repo.RestoreObject` 清除 `deleted_at` | ✅ |
| 生命周期执行器仅扫过期对象 | `reconcile/lifecycle.go:70-99` — `sweepExpired` → `ListExpired` → `SoftDelete`/`HardDelete` | ✅ |

### ✅ 方向五：桶策略条件引擎扩展

| 声明 | 代码证据 | 准确性 |
|------|---------|--------|
| 仅支持 `IpAddress`/`NotIpAddress` 条件 | `policy.go:155-171` — `matchesConditions` switch 仅两个 case，单 key `aws:SourceIp` | ✅ |
| 无 `NotPrincipal`/`NotAction`/`NotResource` | `Statement` 结构体无对应字段 | ✅ |
| REST API 层不评估桶策略 | `grep -rn "checkBucketPolicy\|\.Eval(" internal/api/rest/` — 零结果 | ✅ |
| S3 handler 调用 `checkBucketPolicy` | `handler.go:48-78` — 在 PutObject/GetObject/DeleteObject/ListBucket 入口 | ✅ |

---

## 总体评估

文档核心论点和 5 个方向的**产品价值判断**全部准确，代码锚点引用基本可靠。唯一需修正的是方向二中 `WriteAccessLog` 的实况描述——代码非"完整 SQL 实现"，而是空存根（这反而强化了"断线管线"的论证）。

该文档内容已存在于 `docs/requirements/expansion-v99-dead-code-paths-and-governance-gaps.md`（732 行，md5 一致），无需重新保存。如需生成 `.out.md` 验证摘要或提取行动项，请告知。
