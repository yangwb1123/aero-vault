代码级验证完成。**文档中所有核心论断均与代码库一致。** 以下是验证摘要：

| 论断 | 验证结果 | 代码证据 |
|------|---------|---------|
| 仅一处 span 创建 | ✅ 确认 | `internal/telemetry/http.go:23` — 唯一定义的 span |
| `context.Background()` 在生产代码中使用 | ✅ 确认 | `internal/ai/indexer.go:313,316`、`internal/events/bus.go:139` |
| storageKey 为简单位置寻址 | ✅ 确认 | `internal/service/file.go:167` — `path.Join(tenant, bucket, key)` |
| 配额仅 tenant 维度 | ✅ 确认 | `internal/repository/repository.go:318-320` — `GetTenantQuota(tenant, maxBytes, maxObjects)` |
| 复制为单向 primary→replica | ✅ 确认 | 包注释"copies objects to a secondary storage backend"；Worker 仅含 primary+replica |
| 缩略图仅标准库 image 缩放 | ✅ 确认 | `internal/thumbnail/thumbnail.go` — 142 行，无 WebP/AVIF |

---

这份文档质量很高，已完整保存。作为接下来的动作，我有几个具体的跟进建议：

### 1. 方向二（Tracing）的快速启动最有把握

文档估算 ~290 行，且**零外部依赖**（OTel SDK 已引入）。这是 5 个方向中 ROI 最高的切入点。我可以立即开始实现：
- 为 `FileService.Get/Put/Delete` 注入 span
- 为 `Storage.Get/Put` 和 `Repository.GetObject` 注入 span
- 将 indexer/replication 的 `context.Background()` 改为透传 context

### 2. 方向一（CAS）需要验证一个关键假设

文档建议 `STORAGE_CAS_ENABLED` flag 控制，但 CAS 与现有 SSE 加密的交互需要专项设计——SSE 加密后相同内容的密文不同，这一条需要在前置设计中明确。是否需要在 CAS 分支中：先解密→计算 hash→再去重？

### 3. 方向五 Phase 1（Magic Byte 检测）目前没有安全屏障

这是一个立即可做的安全加固。当前系统对上传文件不做任何 Content-Type 验证——任何 `PUT /v1/files/photo.jpg` 都可以写入一个 ELF 可执行文件。用纯标准库的 `http.DetectContentType` + magic table 即可实现第一道防线。

---

**你希望我从哪个方向开始切入？** 我可以：
- **选项 A**：立即开始方向二（Tracing）的实现 — 创建分支、注入 span、传递 trace context
- **选项 B**：先做方向五 Phase 1（Magic Byte 检测）— ~80 行的快速安全收益
- **选项 C**：对方向一（CAS）做更深的架构预研——特别是 SSE 加密的冲突设计
- **选项 D**：先更新 `ROADMAP.md` 和 `CURRENT_SPRINT.md`，将这 5 个方向纳入规划
