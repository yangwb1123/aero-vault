这是一个非常扎实的架构级分析文档。我已对照实际代码库做逐方向验证，以下是完整反馈。

---

## ✅ 代码级验证摘要

对所有 5 个方向的关键声称做了代码交叉验证：

| 方向 | 关键声称 | 验证结果 |
|------|---------|---------|
| **1. 分布式元数据** | Postgres LISTEN/NOTIFY 无分区、无重放 | ✅ **确认** — `postgres_transport.go` 无分区逻辑；`NextUnconsumedEvents` 全表 `ORDER BY id` |
| | `ClaimIdempotencyKey` 是中心化调用 | ✅ **确认** — `repository` 方法，无跨实例 token |
| | Cluster singleton 基于 lease（`leases.go`） | ✅ **确认** — `internal/cluster/singleton.go` 只有 `AcquireLease` |
| **2. 对象级加密** | SSE-C 未实现 | ✅ **确认** — `storage.go` 的 `Storage` 接口无 `customerKey` 参数；`encrypt.go` 仅 `AES-256-GCM` 信封 |
| | KMS provider 已有（`storage/kms.go`） | ✅ **确认** — `SecretProvider` 接口；`rewrap.go` 启动时单次检查 |
| | 解密未走审计日志 | ✅ **确认** — `AuditEntry` 表存在，但 `FileService.Get/GetRange` 路径未写入 `Action:"decrypt"` |
| **3. 事件网格** | `bus.go` `default:` drop 事件 | ✅ **确认** — `broadcast` 方法 `select { case ch <- e: default: dropped.Add(1) }` |
| | Webhook 只支持单 URL | ✅ **确认** — `NotificationRule` 结构体虽有 `QueueARN`/`TopicARN`/`LambdaARN` 但仅 `QueueARN` 实际使用 |
| **4. 存储分层** | `Storage.Storage` 无 `Transition` 方法 | ✅ **确认** — 接口仅 `Put/Get/Stat/Delete/List/Presign/Multipart`，无 transition |
| | `restoreObject` 是空实现 | ⚠️ **部分确认** — `restoreObject` 在 s3compat handler 返回了固定 XML（软删除恢复 stub）；不是 GLACIER 恢复 |
| | `LifecycleJob` 只有过期删除 | ✅ **确认** — `sweepExpired` 仅有 `ExpireAfterDays` 逻辑 |
| **5. SLO/预算** | `RecordAIUsage` 不记录 embed 成本 | ✅ **确认** — `search.go` 调用了 `embedder.Embed` 但不调用 `RecordAIUsage`；只有 `chat.go` 调用 |
| | `RateLimiter.Allow(tenant)` 无优先级 | ✅ **确认** — `ratelimit.go:81` 仅 `Allow(tenant string) (bool, time.Duration)` |
| | `preflightQuota` 只检查容量 | ✅ **确认** — `file_crud.go:23` 仅 `maxBytes/maxObjects` |

---

## 🔍 深度评估

### 方向 3 → 方向 5 → 方向 2 → 方向 4 → 方向 1 的执行顺序合理

但有几个交叉依赖值得注意：

**3（事件网格）← 5（SLO）：** Webhook 的 `dead_letter` 重放 API 和 `EventDropped` 指标是 SLO error budget 计算的关键输入。建议先做 3 的背压信号和 dropped 区分指标（`reason={backpressure,slow_consumer,queue_full}`），然后 5 的 SLO 框架直接消费这些指标。

**5（SLO）← 2（加密审计）：** 如果解密审计日志（Direction 2.3）先于 SLO 框架实现，解密路径的延迟指标（`file_service_get_duration_seconds`）应该立即纳入 SLO budget。建议解密审计和 decrypt latency 指标同批次完成。

**4（存储分层）← 3（事件网格）：** GLACIER 恢复完成需要触发 `object.restored` 事件，依赖事件基础设施的分区和过滤能力来完成通知。如果先做 4 后做 3，restore 通知只能用当前的单 URL webhook，不够灵活。

### 一个文档低估的工程难点

**Direction 1 的「Idempotency-Key 跨实例正确定义」** 比文档描述的更复杂：

当前 idempotency 实现是 `repository.ClaimIdempotencyKey(ctx, tenant, key, fingerprint, requestID)`，基于 DB 唯一约束。在分布式环境下：
- 时钟偏差导致 `claimed_at` 无法可靠比较
- 需要引入**混合逻辑时钟**（HLC）或 `request_id` 全序序列
- `hash(tenant + bucket + key)` 作为 routing key 确定哪个 shard 持有锁定
- 当前 idempotency key 的 GC 由 `retention.go` 的 `purgeIdempotency` 定期清理——一旦 idempotency key 跨 shard，GC 也需要跨 shard 协调

**建议先在文档中增加一个 HLC/时钟偏差的 sub-section**，否则从单实例到分布式 idempotency 的迁移会引入隐含的向量时钟需求。

---

## 💡 补充：一个被低估的第 6 方向

文档没覆盖但代码库中有明显边界点的方向：

### **6. 跨协议操作一致性治理**

现状：REST / S3 / WebDAV / MCP 四协议各自演进，缺乏统一的操作语义映射：

| 不一致点 | 涉及协议 | 问题 |
|---------|---------|------|
| WebDAV LOCK 与 S3 `x-amz-object-lock-*` | WebDAV ↔ S3 | 完全不共享锁状态，WebDAV 锁住了的文件可以通过 S3 DELETE 删掉 |
| REST ACL vs S3 ACL 格式 | REST ↔ S3 | REST 返回 JSON ACL，S3 返回 XML；写入路径走不同的 handler 但最终写入同一表 |
| Folder 语义 | S3 / WebDAV ↔ REST | S3 `PUT /bucket/key/` 创建零字节标记对象，REST 用 `PUT /v1/files/key/` 或 `Content-Type: application/x-directory` 存在歧义 |
| MCP `write_file` 权限 | MCP ↔ Auth | MCP 工具调用走内部接口绕过 S3 SigV4，但 scope 检查是否一致？当前代码中 `mcp/server.go` 的 `callTool` 未统一走 Auth middleware |

这个方向**工程量小**（主要是 mapping/glue 代码），但**产品感知强**——多协议一致性是 aero-vault 的核心差异化卖点，让用户意识到"任何协议写入、任何协议读取"是真正可靠的。

---

## 📋 下一步行动建议

按你的优先级顺序，建议开始实施方向 3（事件网格增强）的 V0 版本：

```
Phase 0-1: Event partition + dead letter table + backpressure metrics
  1. events 表增加 partition 列 + 迁移脚本
  2. postgres_transport.go 增加分区轮询消费者
  3. dead_letter 表 + Admin API replay endpoint
  4. telemetry.IncEventDropped 扩展 reason 标签
  → 预计 3-5 个 PR，不影响现有 CRUD 路径

Phase 0-2: Direction 5 的多维配额
  1. TenantQuota 扩 MaxIngressBytes / MaxEgressBytes / MaxOpsPerHour
  2. preflightQuota 扩展检查
  3. RateLimiter 增加 priority 参数
  → 预计 2-3 个 PR
```

需要我**从方向 3 开始实施**吗？如果是，我建议从 **Phase 0-1 的 `events` 表分区 + dead letter 表** 入手——这是事件网格的基础设施变更，后续所有增强都依赖它。
