现在我掌握了完整的上下文。以下是项目中所有已完成的架构设计和实现计划文档的概览：

---

## 项目阶段状态

| 阶段 | 状态 | Commit |
|------|------|--------|
| ✅ 需求分析 | 已完成 | `9e9a216` |
| ✅ 技术设计 | 已完成 | `fb43973` (116 文件, 51,932 行) |
| ⏳ **代码实现** | **待完成 ← 当前阶段** | — |

## 可用的架构设计文档 (最新 10 对)

`docs/results/` 目录下有大量 `.arch.md` + `.impl-plan.md` 设计文档对。以下是时间最近的 10 个：

| # | 文档对 | 规模 | 主要方向 |
|---|--------|------|---------|
| 1 | `strategic-extensions` | 30KB arch + 47KB impl-plan | 统一合规框架、存储分层、AI 多模态、主动-主动复制、桶策略引擎 |
| 2 | `genuine-production-blindspots` | 24KB arch + 23KB impl-plan | 生产盲点修复 |
| 3 | `fresh-horizons-systemic-gaps` | 30KB arch + 2KB impl-plan | 系统性缺口分析 |
| 4 | `extensions` | 29KB arch + 19KB impl-plan | 扩展方向 |
| 5 | `extensions-v2` | 22KB arch + 21KB impl-plan | 扩展方向 v2 |
| 6 | **`expansion-v99-dead-code-paths-and-governance-gaps`** | 36KB arch + 31KB impl-plan | ①事件路由引擎(P0) ②访问日志 ③Object Lock ④生命周期状态机 ⑤策略条件引擎 |
| 7 | `expansion-v98-truly-high-value-directions` | 35KB arch + 33KB impl-plan | 多模态 AI、元数据搜索、去重、缓存、主动-主动复制 |
| 8 | `expansion-v97-upload-governance-cold-restore-bucket-mcp` | 28KB arch + 25KB impl-plan | 上传治理、冷存储恢复、Bucket MCP |
| 9 | `expansion-v96-genuine-architect-frontiers` | 2KB arch + 41KB impl-plan | 架构前沿 |
| 10 | `expansion-v95-engineering-architecture-blindspots` | 29KB arch + 28KB impl-plan | 工程架构盲点 |

---

**请问您希望我实现哪个设计文档？** 请提供：

1. **设计文档名称** — 例如 `expansion-v99-dead-code-paths-and-governance-gaps`
2. **实现范围** — 全部方向，或仅特定优先级方向（如方向一 P0 事件路由引擎）
3. **原始需求文档** — 如果存在 `docs/requirements/` 下的对应需求文件

例如，`expansion-v99-dead-code-paths-and-governance-gaps` 包含 5 个方向（总约 78h），其中方向一（事件路由引擎，P0，~14h）是最优先的。

一旦您指定，我会：
1. 完整阅读 `.arch.md` + `.impl-plan.md` + 需求文档
2. 实现全部代码（遵循 AGENTS.md 约束：≤500 行/文件、≤50 行/函数等）
3. 编写单元测试
4. 确保 `make check` 通过
5. 提供完整的代码实现报告
