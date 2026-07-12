您好！我已准备好将技术设计转化为代码实现。

不过我注意到您提供了 **Code Implementer 角色提示词**，但没有指定具体的架构设计文档（.arch.md）和实现计划（.impl-plan.md）来完成哪些任务的实现。

在 `docs/results/` 目录下有多组可用的文档，例如：

| 文档对 | 方向 |
|--------|------|
| `high-value-extensions-v21` | 5 个方向 + 33 个任务 — 存储分层、分片上传 GC、访问日志、多活复制、版本生命周期/合规保留 |
| `expansion-v33-fuse-custody-tag-alert-writebuffer` | FUSE、托管标签、告警、写缓冲区 |
| `expansion-v141-server-copy-webhook-security-tracing` | 服务端复制、Webhook、安全、链路追踪 |
| `expansion-v142-multimodal-ai-metadata-search-dedup-cache-active-active` | 多模态 AI、元数据搜索、去重、缓存、主主 |
| `expansion-v29-architecture-blindspots-governance-performance-protocol` | 架构盲点、治理、性能、协议 |

**请告诉我您希望实现哪个架构设计/实现计划中的哪些任务？** 例如：
- `high-value-extensions-v21` 的 **TASK-100**（P0 关键修复：阻止 Retention GC 清除旧版本）
- 或者某个完整的方向（如方向五：版本生命周期 + 合规保留）
- 或者其他文档对中的特定任务

我将严格按照您指定的 .arch.md 和 .impl-plan.md 来编写高质量、可编译、可测试的代码。
