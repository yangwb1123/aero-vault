现在我理解了完整上下文。我以 **Code Implementer** 的身份准备就绪，可以将架构设计和实现计划转化为实际的 Go 代码。

目前已完成的流水线阶段：
1. ✅ **需求分析** — 已提交
2. ✅ **技术设计** — 已提交（在 `docs/results/*.out.arch.md` + `*.out.impl-plan.md` 中）
3. ⏳ **代码实现** — 待完成

但 `docs/design` 目录下有 **大量** 设计文档（超过 120 对 `.arch.md` + `.impl-plan.md` 文件）。为了开始工作，请告知我需要实现**哪些**设计文档。

以下是一些可供选择的选项：

---

**选项 A：最高优先级 — `expansion-v99-dead-code-paths-and-governance-gaps`**

包含 5 个高价值方向（估算工作量：78h）：
1. **桶级事件通知引擎** — NotificationRouter（5 个任务）
2. **服务端访问日志** — WriteAccessLog（4 个任务）
3. **Object Lock 完整治理模型** — retention_mode / legal_hold（5 个任务）
4. **对象生命周期状态机** — 存储类转换 + Restore（7 个任务）
5. **桶策略条件引擎扩展** — IAM 条件（5 个任务）

**选项 B：按领域分批实现**

例如仅实现方向一（事件通知引擎，14h）或方向三（Object Lock，16h）。

**选项 C：其他设计文档**

`docs/results/` 中还有像 `expansion-v97-upload-governance-cold-restore-bucket-mcp`、`expansion-v98-truly-high-value-directions` 等系列文档。

---

请提供：
1. **需要实现的具体设计文档**（一个或多个）
2. **实现范围**（全部方向或特定方向）
3. **优先级排序**（如果需要的话）
