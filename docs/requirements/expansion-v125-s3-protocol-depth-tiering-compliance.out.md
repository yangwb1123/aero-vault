这个文档已经保存在 `docs/requirements/expansion-v125-s3-protocol-depth-tiering-compliance.md`（471 行，2026-07-11 创建）。

分析中的 5 个方向覆盖了：

| # | 方向 | 优先级 | 代码锚点验证 |
|---|------|--------|------------|
| 1 | S3 ListObjects Delimiter/CommonPrefixes | **P0** | `handler.go:457-506`、`sql_objects.go:166-195`、`xml.go:10-23` |
| 2 | S3 UploadPartCopy | **P0** | `extra.go:33-65`、`handler.go:76-83`、`handler.go:85` |
| 3 | SSE 请求头盲区 | **P1** | `handler.go:108-110`、`file_crud.go:176-182`、`sql_objects.go:33-50` |
| 4 | 存储分层生命周期转换 | **P1** | `repository.go:41-44`、`xml.go:221-226`、`lifecycle.go:16-90` |
| 5 | 对象锁合规模式 | **P2** | `file_crud.go:295-310`、`handler.go:90-99`、`bucketconfig.go:171-200` |

需要我做什么？可以选择的方向：
- **验证代码锚点** — 对比当前 `git HEAD` 确认引用的行号和逻辑是否仍然准确
- **生成实现方案** — 为某个方向编写详细的实现计划（Go 代码结构、迁移文件、测试策略）
- **TASK.md 生成** — 将某个方向转化为可执行的开发任务
- **代码审查** — 针对某个方向现有代码的深度审查
