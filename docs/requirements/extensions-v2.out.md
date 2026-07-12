这个分析非常出色。我已通过直接阅读源代码验证了你的主张——让我分享我的发现和补充。

## 验证总结

我追踪了每一个关键断言，下面是结果：

### 方向一：S3 事件通知 ✅ 大部分正确

**已确认的缺口：** `internal/events/bus.go` 中的 `Publish` 方法在持久化事件并广播给本地订阅者后结束——没有分配逻辑。然而：

- `internal/events/bus.go` 中的 `Bus.Publish` 已经是正确的集成钩子：它在第 60 行持久化事件，然后在第 63 行 `broadcast(e)`。
- 通知规则通过 REST (`/v1/buckets/{bucket}/notification` — handler.go:551) 和 S3 兼容层 (s3compat/handler.go:767) 进行正确的 CRUD 操作。
- 底层数据模型 (`repository.BucketConfig.NotificationRules`) 已经支持 `QueueArn`、`TopicArn` 和 `LambdaFunctionArn`。

**一个缺失的微妙点：** SQS 目标需要 SigV4 签名，这不能复用目前的 HTTP 客户端。一个实用的 first step 是使通知分发器仅支持 HTTP 端点（与 webhook 相同的机制，但按事件类型 + 前缀过滤），然后再处理 SQS/SNS。

### 方向二：存储类分层 ✅ 准确

**已确认：** `BucketConfig`（第 45-52 行）仅包含 `ExpireAfterDays` + `ExpireAction`——没有 `Transitions []TransitionRule`。`internal/reconcile/lifecycle.go` 中的 `LifecycleJob` 仅扫描过期条目。

**一个额外的观察：** 当前的 `Storage` 接口没有 `Copy` 或 `Move` 操作——只有 `Get/Put/Delete/Exists/PresignGet/PresignPut`。这意味着跨后端分层（例如 LOCAL → S3）需要扩展接口。S3 内部的分层（`STANDARD` → `STANDARD_IA`）可以使用特定的后端 `CopyObject` 调用来完成，但对等后端分层需要更多基础设施。

另外：`RestoreObject` 目前处理的是**软删除恢复**（清除 `deleted_at`），而不是 Glacier 恢复。这是两个需要不同端点的不同机制。

### 方向三：完整访问控制 ✅ 非常准确，有一个细微修正

**已确认：**
- `checkBucketPolicy` 仅在 `internal/api/s3compat/handler.go` 中调用——REST API（handler.go:114-161）中的 `Get`、`Put` 和 `Delete` 从未检查策略。✅
- 策略操作映射仅包含 4 个条目（`s3Actions` 映射）。✅
- 没有 `ObjectLockMode`（governance/compliance）——只有缺少模式的 `ObjectLockSeconds` 字段。✅
- 法律保留只是 `metadata["_aero_legal_hold"] = "ON"`（file_crud.go:371）——没有专用列。✅
- 没有 `legal-hold` REST 端点——在路由中没有（router.go）。✅

**一处修正（略有不同）：** 你提到"匿名读只检查 `IsAnonymous` 但没有调用 ACL 验证"。实际上，`allowAnonymous`（acl.go:19）确实调用了 `h.svc.ObjectPublicReadable()`，后者同时检查对象 ACL 和存储桶 ACL（acl.go:63-72）。所以匿名读取确实有 ACL 限制。但问题是：
- **已验证的读取**在 REST 中不检查 ACL——任何已验证的租户都可以读取任何对象。
- `Put`、`Delete` 或 `List` 在 REST 中不检查 ACL（即使是 `public-read-write` 存储桶）。
- 策略检查在 REST 中完全不存在。

### 方向四：多模态 AI Pipeline ✅ 准确

**已确认：** `Extractor`（extractor.go）只处理 `text/*`、`application/json` 和 `application/xml`。`Chunker`（chunker.go）是一个纯粹的滑动窗口（窗口=600，重叠=80）。`Embedder`（embedder.go）只接受 `[]string`。`Agent`（agent.go）只有三个工具：`list_files`、`read_file`、`search`。

**一个工程补充：** 代码的 AST 感知分块（阶段 2）可以使用 `go/ast` 和 `go/parser` 处理 Go 文件，因为它们在标准库中——在添加 `tree-sitter` 绑定作为依赖之前，这是一个实用的起点。

---

## 跨方向见解

### 推荐的实施顺序：**③ → ① → ④ → ②**

我同意这个排序，但我会将方向三分解为可逐步交付的子部分：

| 阶段 | 工作 | 估计影响 |
|------|------|---------|
| **③a** | 在 REST API 的 `Get`/`Put`/`Delete`/`List` 路径中强制执行策略 | ~1 天——将 `checkBucketPolicy` 添加到 REST handler.go |
| **③b** | 法律保留作为专用列 + API 端点 | ~1-2 天——新模式迁移，PUT/DELETE `/v1/files/{key}/legal-hold` |
| **③c** | Governance/Compliance 模式 | ~3-5 天——模式变更 + `BucketConfig.ObjectLockMode` + 写路径中的逻辑分支 |
| **①** | S3 通知分发器 | ~1 周——`internal/events/dispatcher.go` + HTTP 适配器 + 集成测试 |
| **④a** | 多模态嵌入（CLIP/SigLIP） | ~1 周——新的嵌入提供者 + 向量索引兼容性 |
| **④b** | 扩展 Agent 工具 | ~1-2 周——`search_images`、结构化提取 |
| **②** | 存储类分层 | ~2 周——新模式 + 生命周期扩展 + 恢复端点 + 跨后端支持 |

### 跨方向约束

一件需要注意的事情：方向一和方向二都不是完全正交的。

- 方向一（通知）如果适当连接，可以触发方向二（生命周期事件上的对象分层）。
- 方向三（访问控制）为方向四中的多租户 Agent 工具提供安全保障。如果 Agent 可以在没有适当授权的情况下跨租户读取文件，这是一个安全漏洞。

### 缺失的模式细节

在一个快速的架构扫描中，有一个领域你的分析没有深入覆盖，它影响了所有四个方向：

**存储接口限制：** `internal/storage/storage.go` 中的 `Storage` 接口：
```go
type Storage interface {
    Get(ctx, key) -> (io.ReadCloser, ObjectInfo, error)
    Put(ctx, key, reader, size) -> error
    Delete(ctx, key) -> error
    Exists(ctx, key) -> (bool, error)
    PresignGet(ctx, key, expiry) -> (string, error)
    PresignPut(ctx, key, expiry) -> (string, error)
}
```
没有 `Copy`、`Move`、`Restore` 或 `MultipartUpload` 作为接口方法（multipart 是在服务层版本化中实现的）。任何跨后端分层（方向二）或涉及跨后端复制的事件通知（方向一）都需要扩展此接口。

---

## 下一步

这份分析扎实且可操作。如果你想要，我可以帮你起草以下任何一项的实施计划：

1. **方向三a**（REST 策略执行）——最小的工作量，最高的安全回报
2. **方向一**（通知分发器）——最大的 S3 兼容性影响
3. 每个方向的**新 schema 迁移**（方向二、三c、四a）

如果你将这份分析提交到 PR 或 RFC 中，我也很乐意审查草案的措辞。
