文件已保存至 `docs/requirements/expansion-v29-architecture-blindspots-governance-performance-protocol.md`。

**确认：**
- 文件名：`expansion-v29-architecture-blindspots-governance-performance-protocol.md`
- 路径：`/home/u1/aero-vault/docs/requirements/`
- 大小：47 KB / 692 行
- 日期：2026-07-11
- 格式：延续 v28 的分析文档模板

**文档结构：**
| 部分 | 内容 |
|------|------|
| 去重矩阵 | 前 28 期 ~150+ 方向覆盖表 + 本期 5 方向 0 命中确认 |
| 方向总览 | 5 个方向优先级/代码锚点/核心痛点 |
| 方向 1 🟠 | Feature Flags / 灰度发布系统 — config.go 全量开关问题 |
| 方向 2 🟠 | 请求合并与缓存雪崩保护 — file_crud.go GET 无去重问题 |
| 方向 3 🟡 | MCP 协议纵深（Prompts/Sampling/Roots/Completions）— server.go 仅实现子集 |
| 方向 4 🟡 | 事件数据生命周期管理 — events 表无限增长问题 |
| 方向 5 🟡 | 精细化速率限制（per-endpoint/method/IP）— ratelimit.go 仅 per-tenant |
| 总结 | 4 个共同特征：基础设施级、跨组件、opt-in、运维成熟度 |

每个方向均包含：现状 → 代码锚点 → 缺失能力矩阵 → 为什么需要 → 建议架构 → 边界情况，未编写任何实现代码，符合分析文档的既定原则。
