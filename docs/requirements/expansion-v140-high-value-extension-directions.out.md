现在我已完成全面的代码级验证。以下是验证摘要：

---

## 代码验证结果

### ✅ 方向一：存储生命周期分层引擎 — 全部准确

| 引用点 | 验证结果 |
|--------|----------|
| `reconcile/lifecycle.go` 仅处理 `ExpireAfterDays` + soft/hard delete | ✅ 代码确认：`sweepExpired()` 是唯一 sweep 方法，零 Transition |
| `0021_storage_class.up.sql` — storage_class 列存在 | ✅ 已确认 migration 存在 |
| `bucketconfig.go` 解析 lifecycle XML 时只读 `Expiration.Days` | ✅ 代码确认：`putBucketLifecycle` 中 `rule.Expiration.Days` 是唯一读取的字段，`Transition`/`NoncurrentVersionTransition`/`NoncurrentVersionExpiration` 全部被静默忽略 |
| `BucketConfig` 无 TransitionDays/NoncurrentVersionTransition 等字段 | ✅ 代码确认：只有 `ExpireAfterDays` + `ExpireAction` |

### ⚠️ 方向二：元数据/标签查询引擎 — 有一处值得注意的细节

你的分析指出 "没有任何查询接口能按标签或元数据筛选对象"，这在 **REST API (`GET /v1/files`)** 层面确实成立——`handler.go:List` 只接受 `prefix/delimiter/limit/marker`。

但代码库中存在 **S3 兼容路径**的按 tag 查询：
```
internal/api/s3compat/handler.go:471-479
  → ?tag-key=X&tag-value=Y → h.svc.ListByTag()
    → FileService.ListByTag()
      → repo.ListObjectsByTag()  [客户端内存过滤]
```

该方法使用**客户端过滤**（先执行无 tag 条件的 `ListObjects` SQL，再在 Go 层按 tag 筛选），而非 SQL 级过滤。你的分析提到 "仅 `WHERE bucket=? AND key LIKE ?`，无 `tags` 或 `metadata` 过滤"——对 SQL 查询本身是正确的，但建议在 "代码锚点" 中补充提及 `ListObjectsByTag` 的存在及其内存过滤的局限性。

元数据（`metadata`）查询则**完全没有实现**——无论是 REST、S3 还是 MCP 路径。

### ✅ 方向三：事件驱动工作流触发器 — 全部准确

| 引用点 | 验证结果 |
|--------|----------|
| `events/bus.go` 纯广播模式 | ✅ 确认：所有 subscriber 接收全部事件，无过滤 |
| `events/webhook.go` 单一 URL | ✅ 确认 |
| `NotificationRule` CRUD 已实现但无执行引擎 | ✅ 确认：`GetBucketNotifications`/`SetBucketNotifications`/`DeleteBucketNotifications` 只是存取 JSON 字符串 |
| v139 对 SQS/SNS/Lambda ARN 投递已有深度分析 | ✅ 验证：v139 方向一确实覆盖了 ARN 执行引擎 |
| 差异化：HTTP endpoint + 前缀/后缀过滤 + 规则级限流 | ✅ 确认与 v139 的 ARN 路线是不同的产品化方向 |

### ✅ 方向四：读路径缓存 — 全部准确

所有引用点均已验证——`storage.go` 的 `Storage` 接口无缓存装饰器、`local_read.go` 每次 `os.Open`、`factory.go` 无 wrap-with-cache 逻辑。

### ✅ 方向五：PITR/一致快照 — 全部准确

`snapshot.go:23` 的注释明确声明 "only sqlite local snapshots are supported"，`dbFileFromDSN` 只解析 `file:` 前缀的 SQLite DSN——对 Postgres DSN 返回空字符串。全部确认。

---

## 去重验证

| 方向 | 已有分析文件 |
|------|-------------|
| 这篇文档 (v91) | `expansion-v91-storage-tiering-metadata-query-events-cache.md`（36KB, Jul 11 15:46） |
| 后续副本 (v140) | `expansion-v140-high-value-extension-directions.md`（36KB, Jul 11 23:54） |

你的文档已以 v91 和 v140 两个版本存在。v140 删除了 "90 份"/"81 份" 等具体数目引用，与你本次的版本一致。方向三的 "既有分析覆盖" 更新（引用 v139 的 S3 Event Notifications 分析）在两个版本中均已体现。

**主要区别**：你的版本在方向二的描述中写了 "AWS 提供了 SELECT-like 的 `s3:ObjectTagging` + `s3:GetObjectTagging` 组合"（不含 `select-object-content`），而 v140 也是这个版本。v91 原始版包含了 `select-object-content` 引用。

---

你希望我做什么？例如：
1. 将你的更新保存为最新版本（`expansion-v145-...md`）
2. 对方向二补充 `ListObjectsByTag` 的内存过滤细节
3. 检查其他尚未分析的方向
