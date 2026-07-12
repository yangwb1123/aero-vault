已生成 `expansion-v88-product-architect-gaps.out.md` — 对五个方向的逐行代码验证、增量架构设计、边界修正和实施路线图。

**验证核心结论：**

| 方向 | 验证 | 关键发现 |
|------|------|---------|
| 1. Agent 沙箱 | ✅ **完全准确** | `dispatchTool` 裸调用、零审计、零配额 — 与代码完全一致 |
| 2. 后端能力契约 | ✅ **完全准确** | `Backend() string` 唯一标识、`uploadStorageKey` 已经按 backend 名称做了隐式分支 |
| 3. 分片上传治理 | ✅ **完全准确** | `UploadPart` 无 size 校验、`uploads map` 无 TTL、`InitMultipart` 无并发上限 |
| 4. 补偿事务 | ⚠️ **需要修正** | Reconcile **已存在** `sweepOrphanBlobs` 但分析声称"零覆盖"；建议仍实施及时的补偿事务而非等待周期性 sweep |
| 5. 模型漂移 | ✅ **完全准确** | `searchVector` 过滤正确、`ReindexStale` 无进度/无自动调度、零指标 |

**关键补充：**
- 方向二：新增了 `Capabilities` 在构造时检测而非运行时查询的原则
- 方向三：发现了 `local.uploads` 内存 map 与 `repository.Upload` 持久表之间的同步鸿沟 — TTL 需要作用于两层
- 方向四：补充了 `CompleteMultipart` 和 versioned 软删除两个被遗漏的补偿路径；标注了并发覆盖竞争条件的严重性
- 方向五：提供了水印续传的完整伪代码实现；简化了 shadow-mode 提案（不需要双重索引）
