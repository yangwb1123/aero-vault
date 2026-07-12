根据您提供的 **Code Implementer 角色提示词**，我已经理解了您想要我做什么。不过，要开始实现，我需要知道具体要实现的**架构设计文档**和**实现计划文档**。

您在工作目录 `docs/results/` 中有大量 `.arch.md` 和 `.impl-plan.md` 文件，例如：

- `strategic-extensions.out.arch.md` + `strategic-extensions.out.impl-plan.md`
- `genuine-production-blindspots.out.arch.md` + `genuine-production-blindspots.out.impl-plan.md`
- `extensions-v2.out.arch.md` + `extensions-v2.out.impl-plan.md`
- `expansion-v97-upload-governance-cold-restore-bucket-mcp.out.arch.md` + `...(impl-plan).md`
- 等等许多

另外还有一个名为 `fresh-horizons-systemic-gaps.out.impl-plan.md` 的较新文件（没有对应的 `.arch.md` 对）。

---

**请告诉我您要让我实现哪一个（或哪一组）设计文档？** 例如：

1. **指定文件名**：直接说出 `.arch.md` 和 `.impl-plan.md` 的路径
2. **按主题选择**：描述您想要实现的功能（如 "MCP 工具扩展"、"存储分层"、"配额管理" 等），我找对应的文档
3. **按优先级选择**：如果某个文档标明了优先级或实施顺序，也可以告诉我

一旦确认，我将严格按照：
- 架构设计文档（`*.arch.md`）的结构和接口定义
- 实现计划文档（`*.impl-plan.md`）的分步实施步骤
- 项目现有的 `AGENTS.md` 约束（文件 ≤500 行、函数 ≤50 行、圈复杂度 ≤10 等）

来编写高质量、可编译、可测试的代码。
