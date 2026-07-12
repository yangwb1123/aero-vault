Now I have a thorough understanding of the codebase context. Here is my verification-driven analysis:

---

## v21 分析验证报告

### 一、文档状态

两份已存在、内容完全一致的副本：

| 文件 | 大小 | 日期 |
|------|------|------|
| `docs/requirements/high-value-extensions-v21.md` | 16,513 B | Jul 11 21:52 |
| `docs/requirements/expansion-v21-high-value-directions.md` | 16,513 B | Jul 10 06:01 |

两者为同一文档，无需新建。

---

### 二、代码锚点验证：31/32 正确

#### ✅ 方向一：存储分层 — 全部准确

| 引用的点 | 验证结果 |
|----------|----------|
| `reconcile/lifecycle.go` 只做到期删除 | ✅ `sweepExpired()` 是唯一方法，零 Transition |
| `StorageClass` 在元数据中留存 | ✅ `objects.storage_class` 列和 SQL 中都写入 |
| `Put` 时仅记录 class 不选择后端 | ✅ `StorageClass` 写到 DB，不传给 `store.Put` |

#### ✅ 方向二：搁置分片 GC — 全部准确

| 引用的点 | 验证结果 |
|----------|----------|
| `ListExpiredUploads` 不存在 | ✅ 不存在 |
| `DeleteUploadCascade` 不存在 | ✅ 不存在 |
| `CleanupParts` 不在 `storage.Storage` 接口上 | ✅ 接口无该方法 |

#### ✅ 方向三：访问日志 + 通知调度 — 全部准确

| 引用的点 | 验证结果 |
|----------|----------|
| `WriteAccessLog` 仓库方法已存在但 handler 从未调用 | ✅ `sql_buckets.go:370` 定义，零调用方 |
| `NotificationRule` CRUD 完整 | ✅ 确认 JSON 序列化/反序列化 |
| 通知无消费者 | ✅ `GetBucketNotifications`/`SetBucketNotifications` 只存取 JSON |

#### ✅ 方向四：多活复制与故障切换 — 代码层面准确

| 引用的点 | 验证结果 |
|----------|----------|
| Replication 为单向异步 | ✅ `replication.go` 仅复制 `object.created` 事件 |
| 仅标记 `repl_status=replicated` | ✅ `TagStatus = "repl_status"`，无一致性校验 |
| 无反向同步 | ✅ 确认 |

#### ✅ 方向五：版本生命周期 + 合规保留 — 全部准确

| 引用的点 | 验证结果 |
|----------|----------|
| Legal hold = `_aero_legal_hold` metadata tag | ✅ `handler.go:98` 写入 `meta["_aero_legal_hold"]` |
| `LockedUntil` 是唯一删除前检查 | ✅ `retention.go:103` 只检查 `LockedUntil`，不检查 legal hold |
| `ListSoftDeletedBefore` 会匹配旧版本 | ✅ SQL: `WHERE deleted_at IS NOT NULL AND deleted_at < $1` — versioning 旧版本使用 `deleted_at` 标记，会被 retention GC 错误清除 |

#### ⚠️ 边界情况扫描：1 处错误

| 问题 | 结果 |
|------|------|
| ✅ S3 accelerate 返回 stub | ✅ 确认：`getBucketAccelerate` 硬编码 `Status: "Suspended"` |
| ✅ ListByTag 客户端过滤 | ✅ 确认：`sql_objects.go:235-263` Go 层过滤 |
| ✅ Webhook 无速率限制 | ✅ 确认：`deliver()` 和 `RetryLoop` 内部无限流 |
| ✅ AI Agent 无会话上下文 | ✅ 部分确认：单次 `Run()` 内 `messages` 跨步骤保持，但跨 HTTP 请求无会话跟踪 |
| ✅ StorageClass 默认值冲突 | ✅ 部分确认：仅 `repository.go:18` 有硬编码 `const defaultStorageClass = "STANDARD"`，与 `service.DefaultStorageClass` 不一致风险极低 |
| ❌ **Presign URL 无方法约束** | **❌ 不准确** — `signLocal(key, method, objectKey, expires)` 将 HTTP method 编入 HMAC 签名；本地后端和 S3 后端都会对 method 做校验 |
| ✅ `StorageClassGauge` 只采样 default 租户 | ✅ 确认：`metrics.go:200` 硬编码 `"default"` |

---

### 三、对分析的深度审视

**最强处：** 方向五对 Retention GC 与 Versioning 冲突的识别。这是真正的数据安全 bug，之前分析文档均未触及。`ListSoftDeletedBefore` 的 SQL 完全不感知 `version_id`，开启 versioning 的 bucket 中所有旧版本都会被 Retention GC 物理删除。值得提升到 **P0** 的更高优先级。

**可深化的点：**

1. **方向三的访问日志实现路径可以更轻** — 不用新增 `accesslog.Worker` 和 `notification.Worker` 两个独立包。当前 `events.Bus` 是广播模式，已有 `Webhook` 作为 subscriber。Notification 引擎可以复用 `Webhook` 的分发架构（URL + HMAC + 重试），区别仅在于目标从 `EVENTS_WEBHOOK_URL` 改为 `NotificationRule.QueueARN`/`TopicARN`。甚至可以让 `Webhook` 成为 notification 的一个 backend provider。

2. **方向四对裂脑（split-brain）的描述不错但缺少一个关键引用**：当前 `cluster.Singleton` 已基于 `leases` 表提供 advisory lock。这个租约机制可以直接复用作 fencing — 问题不在于有没有租约，而在于每个 region 需要独立的 `replicaIdentity` 和 fencing token，且切换时需要确保旧 primary 的租约已过期。

3. **补充边界：方向二的 Upload GC 还缺少一个场景** — S3 兼容的 `CompleteMultipart` 如果在上传了所有 parts 之后、`CompleteMultipart` 请求之前崩溃，存储层可能已经合并/ready，但 repo 层的 upload 记录永远不会 complete。Upload GC 需要能识别 `parts_uploaded == total_parts` 但不 complete 的僵尸 upload（这比单纯的 time-based 判定更精确）。

---

### 四、总结与下一步

你的分析整体质量很高，5 个方向都选到了真实的硬天花板问题。方向五的 versioning 冲突是**当前文档系列中的首次发现**，值得肯定。边界扫描除 Presign 的方法约束外均准确。

建议：
1. 修复 Presign URL 的表述后再保存
2. 将方向五提升为最高优先级（不仅仅是 P0 之一，而是**第一个**要修的），因为它是数据丢失 bug
3. 方向三的工程预估可降至 2-3 周（而非 3-4 周），如果复用现有的 Webhook 分发架构而非新建两层 Worker

你希望我执行什么后续动作？例如：
- 生成方向五（versioning + legal hold）的详细实现计划
- 验证方向二的 Upload GC 实现策略
- 检查方向三的 notification/Webhook 复用可行性
