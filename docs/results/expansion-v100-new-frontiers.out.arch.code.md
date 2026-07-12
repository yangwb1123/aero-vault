现在我理解了完整的流程。根据 `pipeline-code-impl.yaml`，我作为 **Code Implementer** 需要接收以下输入：

1. **需求文档**（`docs/requirements/` 下的某个文件）
2. **架构设计文档**（`docs/results/{input_stem}.arch.md`）
3. **实现计划文档**（`docs/results/{input_stem}.impl-plan.md`）

然后产生代码实现。

---

**不过，你还没有告诉我具体要实现什么任务。** 请提供以下信息之一：

### 方案 A：指定需求和已有设计文档

请告诉我：
1. 哪个需求文档需要实现？(例如 `docs/requirements/expansion-v141-...md`)
2. 对应的 `.arch.md` 和 `.impl-plan.md` 文件路径是什么？

### 方案 B：直接提供需求和设计

如果你已经有明确的实现目标，可以直接告诉我需求描述。

### 方案 C：让我从已有设计中选择

我可以检查最近创建的 `.arch.md` + `.impl-plan.md` 组合，看看哪些设计已经完成但尚未实现代码。

---

你觉得哪种方式方便？或者想让我先看看当前有哪些已完成的设计文档等待实现？
