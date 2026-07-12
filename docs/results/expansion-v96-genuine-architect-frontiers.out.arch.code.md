已完成分析。我将等待用户提供具体的架构设计文档（.arch.md）和实现计划文档（.impl-plan.md），然后开始实现代码。

---

### 当前发现

你的仓库中有大量设计文档对（.arch.md + .impl-plan.md），但既有：

1. **TASK.md** 和 **CURRENT_SPRINT.md** 来自 6 月中旬，内容已经过时
2. 最近的两条提交完成了 pi-batch 流水线的 `requirement-analysis` 和 `technical-design` 阶段
3. `cmd/server/main.go` 已膨胀至 **861 行**（超出 AGENTS.md 规定的 500 行限制），这是架构文档 `expansion-v45-systemic-cross-cutting-gaps` 中重点分析的问题

**请告诉我以下信息：**

1. **你想要我实现哪个设计文档？** 例如：
   - `docs/results/expansion-v45-systemic-cross-cutting-gaps.out.arch.md` — 聚焦 main.go 重构/ShutdownGroup 集成/goroutine 管理
   - 或其他扩展方向的文档（`expansion-v121`, `expansion-v138` 等）
2. **实现范围：** 是整个实现计划，还是其中的部分任务（如 Phase 1/P0 任务）
3. 或者你有**全新的需求**要实现？

一旦你指定了具体文档，我会：
- 详细阅读架构设计和实现计划
- 逐文件、逐函数地实现代码
- 确保编译通过、测试通过
- 保持文件 ≤ 500 行、函数 ≤ 50 行的约束
