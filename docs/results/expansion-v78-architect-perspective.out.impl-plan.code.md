我已收到 **Code Implementer** 角色提示词。我已完整阅读项目上下文（`AGENTS.md`、`CURRENT_SPRINT.md`、`TASK.md`）。

根据 `pi-batch` 的 `pipeline-code-impl.yaml` 定义，代码实现阶段需要从 `technical-design` 阶段的输出（即 `.arch.md` + `.impl-plan.md` 文件对）读取具体的设计文档。

我注意到在 `docs/results/` 目录下有约 **110+ 对已完成的架构设计文档和实现计划**，包括：

| 设计文档 | 大小 |
|---------|------|
| `expansion-v113-synchronous-teardown-webhook-circuit-breaker-and-operational-gaps` | 架构 521 行 + 实现计划 475 行 |
| `expansion-v119-strategic-extension-directions` | 架构 468 行 + 实现计划 360 行 |
| `expansion-v118-architect-frontiers` | 架构 475 行 + 实现计划 502 行 |
| `expansion-v118-security-benchmark-config-gaps` | 架构 630 行 + 实现计划 408 行 |
| `expansion-v117-authentic-production-gaps` | 架构 527 行 + 实现计划 415 行 |
| `expansion-v114-s3-protocol-completeness-and-operational-gaps` | 架构 544 行 + 实现计划 322 行 |
| `expansion-v105-deep-code-integrity-gaps` | 架构 381 行 + 实现计划 386 行（已有部分代码实现） |
| ... 以及更多 | |

**请指定您希望我实现的具体设计文档。** 您可以选择：

1. **指定文档名称：** 如 `expansion-v113-synchronous-teardown-webhook-circuit-breaker-and-operational-gaps`
2. **按功能描述：** 如"实现服务端 COPY/MOVE 数据移动"（对应 `expansion-v141`）
3. **按优先级：** 如"从 v119 开始逐个实现"
4. **自定义需求：** 直接描述您想要实现的功能
