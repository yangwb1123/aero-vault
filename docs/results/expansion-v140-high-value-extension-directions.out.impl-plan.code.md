我已全面理解 Code Implementer 角色职责。当前阶段是 **pi-batch 流水线的代码实现（code-implementation）阶段**，上一步的 technical-design 阶段已生成多个扩展方向的 `.arch.md` 和 `.impl-plan.md` 文档。

我看到以下已就绪的可实现扩展方向（按创建时间排序）：

| 扩展方向 | 架构文档 | 实现计划 |
|---------|---------|---------|
| **v141: 服务端 COPY/MOVE + Webhook + 安全架构 + 分布式追踪** | `.arch.md` ✅ | `.impl-plan.md` ✅ — 含详细任务分解（7+5+6+4=22个子任务） |
| **v142: 多模态 AI + 元数据搜索 + 去重缓存 + 主备** | `.arch.md` ✅ | `.impl-plan.md` ✅ |
| **v33: FUSE/POSIX + 标签告警 + 写入缓冲 + 保管链** | `.arch.md` ✅ | `.impl-plan.md` ✅ |
| **v29: 架构盲点 - 治理/性能/协议** | `.arch.md` ✅ | `.impl-plan.md` ✅ |
| **high-value-extensions-v21** | `.arch.md` ✅ | `.impl-plan.md` ✅ |
| 以及其他 20+ 个扩展方向 | ✅ | ✅ |

请告诉我：
1. **要实现的扩展方向** — 哪个（或哪几个）方向的 `.arch.md` 和 `.impl-plan.md` 作为输入？
2. **实现范围** — 是否需要实现 impl-plan 中的全部子任务，还是优先某个子集？

一旦确认，我将严格按照架构设计和实现计划编写可编译、可运行的代码，并输出完整的**代码实现报告**。
