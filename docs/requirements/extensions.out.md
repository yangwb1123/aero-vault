这份文档分析得非常全面，覆盖了当前代码库中几个关键的增量盲区。我有几个观察和问题想确认：

---

### 关于优先级排序的思考

**第1位（存储类生命周期转换）** 和 **第2位（按桶事件通知）** 的排序逻辑很合理——它们都有一个共同特征：**API 接口已存在，但行为不完整**。用户配置了规则但没有效果，比功能缺失更容易导致信任流失。

不过，有一个细微之处值得讨论：

**第2位（按桶事件通知）** 的实际增量成本可能比表中标注的更低。因为：

```go
// internal/repository/sql.go 已经可以读 rules
GetBucketNotifications(ctx, tenant, bucket) ([]NotificationRule, error)

// EventBus 已经派发事件
bus.Publish(ctx, eventbus.Event{Type: "object.created", ...})

// webhook.Worker 已经提供重试逻辑
```

核心缺失的是一个**轻量级的路由 goroutine**——它订阅 EventBus，按规则过滤，然后 dispatch 到不同的 sink。这部分代码可以在 **~300 行**内完成核心功能（不含每个 sink 的适配器）。相比 `Transition` 需要扩展 Storage 接口 + 建立后台 Job，通知路由的 ROI 可能更即时。

### 关于第4位（大文件流式加密）的技术选型

AES-256-CTR + HMAC-SHA256 分块方案需要特别注意几个工程细节：

1. **nonce 管理**：每个块用 `counter = block_index` 作为 CTR 的 IV 部分，但还需要一个 per-file nonce 防止不同文件相同 counter 导致密钥流复用——建议用 `envelope.KeyID + fileID` 派生 unique nonce。

2. **HMAC 验证时序**：文件末尾追加 HMAC 列表意味着如果只读前几个块，decrypt 侧的验证需要先读文件尾获取 HMAC。更好的做法是**每个块自包含 HMAC tag**（类似 GCM 的 per-block tag），这样流式读取每个块即可独立验证。

3. **现有 GCM 小文件的兼容**：建议用 `alg` 字段区分，与文档一致。

### 想确认的几个点

1. **是否计划从这些方向中选一个开始实现？** 如果是我可以现在开始做技术调研和实现计划。

2. **通知路由的短期实现**——如果打算做，我建议先只做多 Webhook endpoint（复用现有的 `webhook` 包），SNS/SQS/SMTP 可以延后。这样核心路由逻辑 1-2 天内可以完成，且 OpenAPI 已有 `GET/PUT/DELETE /v1/buckets/{bucket}/notification` 路由。

3. **存储类 Transition 的 Storage 接口扩展**——当前 Storage 接口没有 `ChangeStorageClass` 方法。对于 local storage（默认基线），Transition 本质上只是修改元数据；对于云后端（S3/OSS/COS），需要调用云 SDK 的 `CopyObject`（修改 storage class）。这个接口设计需要想清楚：是加新方法还是让 `CopyObject` 语义扩展？

你希望我从哪个方向展开？
