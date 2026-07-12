我明白你想要我作为 **Code Implementer** 来编写代码。但是要实现代码，我需要三位一体的输入文件：

1. **架构设计文档**（`*.arch.md`）— 架构师产出的技术方案
2. **实现计划文档**（`*.impl-plan.md`）— Tech Lead 产出的任务拆解
3. **原始需求**（如 `docs/requirements/*.md`）— 要做什么功能

根据 pipeline 定义（`ai-dev/pipelines/pipeline-code-impl.yaml`），Code Implementation 阶段的输入来自前两个阶段的产出。

## 请提供以下信息

请把你要实现的功能对应的文件贴给我：
- 架构设计文档路径和内容
- 实现计划文档路径和内容
- 原始需求/功能描述

例如，如果你要运行某个 pipeline 的产出，可以提供 `docs/results/` 目录下的 `.arch.md` 和 `.impl-plan.md` 文件。或者直接告诉我：

> "请实现 XXX 功能，这是需求文档..."

一旦收到这些输入，我会严格按照以下流程完成实现：

```
需求文档 + 架构设计(.arch.md) + 实现计划(.impl-plan.md)
  → 代码实现
  → 编译验证 (go build ./...)
  → 测试验证 (go test ./...)
  → 代码质量检查 (gofmt, go vet)
  → 代码实现报告
```

**当前工作目录**: `/home/u1/aero-vault`，项目为 Go 1.25 的 aero-vault 系统。

请提供你的设计文档和需求！
