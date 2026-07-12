感谢你提供的 Code Implementer 角色设定。我已理解自己的职责。

根据当前的上下文：

1. **pi-batch 流水线**正在并行运行多个 code-implementation 任务（共 8 个 worker）
2. **已完成的：** requirement-analysis → technical-design 阶段已提交
3. **待完成的：** 许多 `docs/results/*.arch.md` + `docs/results/*.impl-plan.md` 对尚未实现代码
4. **我收到的消息**是 Code Implementer 角色提示模板本身，但其中 `{input_content}` 占位符未被实际替换为具体的 `.arch.md` / `.impl-plan.md` 文件内容

为了推进实现，我需要你指定：

**你想让我实现哪个具体的需求？** 例如：

- `expansion-v120-bootstrap-migration-graceful-shutdown`（优雅关闭/引导迁移）
- `expansion-v12-intelligent-platform`（智能平台）
- `expansion-v56-object-streaming-and-server-side-copy`（对象流式传输与服务端拷贝）
- `expansion-v91-storage-tiering-metadata-query-events-cache`（存储分层/元数据查询/事件缓存）
- 或其他某个特定的 `.arch.md` + `.impl-plan.md` 对

请提供要使用的具体文件名或需求编号，我将：
1. 读取对应的 `.arch.md`（架构设计）和 `.impl-plan.md`（实现计划）
2. 按照设计实现完整代码
3. 确保通过 `make check`（gofmt、go build、go vet、go test）
4. 输出代码实现报告
