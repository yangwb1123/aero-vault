## Clarification

我明白了你的上下文。你给我的角色是 **Staff Engineer 进行代码审查**，但你同时也带着"Code Implementer"的流程上下文，不确定具体要审查什么。

让我帮你理清：

---

### 你现在的状态

你的 **workflow**（来自 `pipeline-code-impl.yaml`）显示你需要先有：
1. **需求文档** → `docs/requirements/` 下
2. **架构设计** → `.arch.code.md` 文件
3. **实现计划** → `.impl-plan.code.md` 文件

然后产出代码，再让我（Staff Engineer）做审查。

### 问题：你希望我先审查什么？

我看到了大量已完成的设计文档（`.arch.md` + `.impl-plan.md` 组合），其中很多已经有 `.code.md` 后缀（表示代码已实现）。例如：

| 最近的设计 | 状态 |
|-----------|------|
| `expansion-v141-server-copy-webhook-security-tracing` | 仅有 `requirements.md`，尚未见 `.arch.md` |
| `expansion-v140-high-value-extension-directions` | 仅有 `requirements.md` |
| `expansion-v126-ai-native-pipeline` | 有 `.requirements.md` 和 `.out.md` |
| `expansion-v120-bootstrap-migration-graceful-shutdown` | 有 `.arch.md` + `.impl-plan.md`，但无 `.code.md` |
| `deep-production-gaps-v1` | 有完整 `.arch.code.md` + `.impl-plan.code.md` + review |

---

### 我建议的方案

**方案 A：** 如果你有刚写完的代码需要我 Review，告诉我文件路径或子系统名称（如 `internal/service/`、`internal/api/s3compat/`、`internal/ai/` 等），我直接开始审查。

**方案 B：** 如果你想让我先检查 **哪些设计文档已就绪但代码尚未实现**，我可以扫描 `.arch.code.md` / `.impl-plan.code.md` 与现有代码的对应关系，找出 gap。

**方案 C：** 如果你是想执行 "Code Implementer" 的下一步——先产代码再让我审查——请指定：

```
需求: docs/requirements/expansion-v141-xxx.md
架构: docs/results/expansion-v141-xxx.out.arch.md   (或 .arch.code.md)
计划: docs/results/expansion-v141-xxx.out.impl-plan.md  (或 .impl-plan.code.md)
```

---

**你倾向于哪个方向？** 或者直接告诉我你想 Review 的具体 Go package 或功能模块，我现在就可以动手。
