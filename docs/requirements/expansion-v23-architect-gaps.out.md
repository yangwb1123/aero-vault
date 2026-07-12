注释明确说：「lifecycle events must not break user requests」。这是有意识的设计取舍，但你提出的背压治理问题仍然成立——尤其是 Webhook 没有 catch-up 机制、Indexer 仅靠轮询补偿、没有重放 API 这三个子问题。

---

### 边缘问题验证：4/7 准确，1 处有误，2 处需补充

| # | 断言 | 验证 | 判定 |
|---|------|------|------|
| 1 | `ListBucketVersions` REST handler 只返回最新版本 | ✅ `handler.go:889` 调用 `svc.List()`，S3 路径反而正确（`bucketconfig.go:245` 做了逐 key 的 `ListObjectVersions`） | 准确 |
| 2 | BM25 全在内存，百万级不可控 | ✅ `BuildFromRepo` 扫描全量 chunk | 准确 |
| 3 | `StorageClassCounts` 硬编码 `default` | ✅ `metrics.go:187` — `fn(ctx, "default")` | 准确 |
| 4 | Replication 无压缩 | ✅ 未发现 gzip 中间件 | 准确 |
| 5 | RateLimiter 无 Retry-After header | ❌ **实际代码实现了**：`ratelimit.go:126-127` 的 `writeRateLimitHeaders` 设置 `Retry-After`，`http.Error` 前已调用 | **有误** |
| 6 | `preflightQuota` size=0 不对称 | ✅ `file_crud.go:39-46` — size>0 用 `+size > MaxBytes`，size==0 用 `>= MaxBytes` | 准确 |
| 7 | 无 OTel trace 关联日志 | ✅ 全代码库未发现 trace ID 注入日志上下文 | 准确 |

RateLimiter 那个我帮你修正——`Retry-After` 是存在的，但缺乏的是**速率限制的 OTel metrics**（被拒绝的 token 桶水位、等待时间分布等）。你可以把边缘问题 #5 调整为「限流失效的可观测性不足——无 token bucket 水位/拒绝率指标」。

---

## 超越你分析的 3 个额外洞察

既然做完了验证，我再贡献 3 个你分析中没有涉及的、但同样是生产化天花板的问题：

### 洞察 1：Webhook 的系统可用性依赖关系（未覆盖）

`Webhook.Run` 订阅事件总线后**只有实时流处理，没有冷启动追赶**。这与 Indexer（`drainBacklog`）的容错机制形成鲜明对比。更严重的是：webhook 的目的地是一个**全局配置**（`EVENTS_WEBHOOK_URL`），如果这个 URL 不可用，所有事件的投递都堆积到 `webhook_failures` 表（指数退避最多 10 次后放弃）。**没有死信队列、没有备选端点、没有运维告警触发点。** 这是事件驱动架构的单点故障。

### 洞察 2：多协议间的事件一致性没有保证

当一个对象通过 S3 `PutObject` 创建，又通过 REST `POST /files` 上传，再通过 WebDAV `PUT` 写入——三个协议路径都触发 `EventBus.Publish`。但是如果：
- 其中一个路径的事件持久化成功，但 `broadcast` 丢弃（方向五的问题）
- 另一个路径的事件 `InsertEvent` 失败（日志 warn 后默默返回）

那么同一个对象的创建会在不同的订阅者视角中呈现出"事件不完整"的状态。**缺少事件溯源（event sourcing）所需的事件事务边界。**

### 洞察 3：`listObjects` 的 SQL 查询在 `LIKE + > + IS NULL` 复合条件下索引选择不可预测

你提到了这个问题，但值得展开。当前 SQL 模式：
```sql
WHERE tenant_id = ? AND bucket = ? AND deleted_at IS NULL
  AND key >= ? AND key LIKE ?
ORDER BY key ASC LIMIT ?
```
在 Postgres 上，规划器可能在 `(tenant_id, bucket, deleted_at, key)` 的索引上走 index scan，但 `LIKE` 的 `%` 前缀会导致它 fallback 到 filter。当数据量达到百万级，实际表现会是**每个请求扫描数万行**。**建议补充为：加上 `AND key < exclusive_end` 的区间终止条件**，把 `LIKE` 降级为可选的二次过滤，让索引能走 range scan。

---

## 优先级微调建议

你的排序整体合理。但我建议微调两处：

| 方向 | 你的优先级 | 微调建议 | 理由 |
|------|-----------|---------|------|
| 方向二（事件通知） | **P0** | 维持 P0，但原因从「用户配置了规则不生效是功能欺骗」调整为 **「S3 兼容性断裂，迁移 blocker」** | 当前 `bucket_notifications` 确实只存不用，但这对 REST 客户的影响远小于 S3 客户——而 S3 是 aero-vault 的核心协议。`PutBucketNotification` 是 S3 SDK 最常调用的 bucket 配置 API 之一 |
| 方向四（列表性能） | **P1** | 提升为 **P1 头部/准 P0** | 理由：`ListObjectsByTag` 的 O(N) 实现一旦被 S3 客户调用（比如用 `s3api list-objects --query "Contents[?contains(...,'tag')]"`），在 10 万对象时就已不可用。这不是渐进性问题——在演示/POC 中就会暴露 |

---

## 最终评价

这是一份**高质量的架构缺口分析**。5 个方向的代码锚点精确，必要性论证扎实，影响范围评估务实。4 个方向完全准确，1 个方向（方向五）的部分改进建议可以整合到你已有的分析框架中（方向五的改进建议 1-4 合理，可以保留）。

边缘问题中除 Retry-After 那条外，其他 6 个也都站得住脚。我补充的 3 个洞察可以视为你的分析框架的自然延伸而非独立方向。

如果你打算基于此进入实现阶段，建议**每个方向独立出一个 Design Doc**，按照 `AGENTS.md` 的扩展入口规范（`main.go` 装配 → `router.go` 注册 → `config.go` 配置 → migration 对 → `*_test.go`），依次实现方向一、方向二 → 方向四 → 方向三 → 方向五。
