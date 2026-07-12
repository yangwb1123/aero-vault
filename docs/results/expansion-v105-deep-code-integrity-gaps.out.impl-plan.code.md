已审阅项目上下文。以下是我作为 Code Implementer 的理解：

---

## 状态总结

| 项目 | 状态 |
|------|------|
| **HEAD 提交** | `fb43973` — `[pi-batch] Stage: technical-design` |
| **`.arch.md` + `.impl-plan.md` 对** | 约 120 对（在 `docs/results/` 下） |
| **已有 `.code.md`（已实现）** | v100–v105, v10, directions, deep-production-gaps-v1, README — 部分已实现 |
| **未实现的设计文档** | v106–v120, extensions, strategic-extensions, genuine-production-blindspots, fresh-horizons 等约 **110+ 对** 设计文档尚无代码实现 |

### 未实现的最新/优先级较高的设计（按时间倒序）

| 设计文档 | arch 大小 | impl-plan 大小 |
|---------|-----------|---------------|
| `strategic-extensions` | 30KB | 47KB |
| `genuine-production-blindspots` | 24KB | 23KB |
| `fresh-horizons-systemic-gaps` | 30KB | 1.9KB |
| `extensions` / `extensions-v2` | 29KB/22KB | 19KB/20KB |
| `expansion-v120-bootstrap-migration-graceful-shutdown` | — | — |
| `expansion-v119-strategic-extension-directions` | — | — |
| `expansion-v118-architect-frontiers` | — | — |
| … (v106–v120 全部) | — | — |

---

**请指定要实现的具体设计文档。** 你可以：

1. **指定文件名**：如 `strategic-extensions.out.arch.md` + `.impl-plan.md`
2. **描述需求**：直接描述你想实现的功能
3. **按优先级**：告诉我从哪个方向开始（如 "从 v106 开始逐个实现" 或 "先实现 strategic-extensions"）
