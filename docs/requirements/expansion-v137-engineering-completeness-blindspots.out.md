已完成。文件保存为 `docs/requirements/expansion-v144-engineering-integrity-blindspots.md`（680 行，约 27KB）。

**文档覆盖的 5 个方向：**

| # | 方向 | 优先级 | 核心问题 |
|---|------|--------|---------|
| 1 | AI Chunk 在对象删除后永久残留 | **P1** | Retention GC 跳过 ChunkCleaner + EventBus 丢事件 → 搜索索引中永久存在已删除对象的 chunk |
| 2 | CompleteMultipartUpload 丢弃客户端分片列表 | **P1** | 客户端 ETag 验证被完全跳过，存储后端静默数据损坏无法被检测 |
| 3 | Web UI 缺乏管理面和对象生命周期管理 | **P2** | 15+ 项管理功能（桶管理、租户、API Key、审计等）仅 CLI/curl 可达 |
| 4 | S3 Select API 完全缺失 | **P3** | 无服务端 SQL 过滤能力，大文件查询必须整对象下载 |
| 5 | 并发删除与并发覆盖的对象版本一致性裂痕 | **P2** | Put/Delete 路径中存储 blob 与 metadata 操作无原子性，存在 4 种不一致窗口 |

每个方向均包含：代码锚点、影响分析、边界情况枚举、分阶段修复建议、以及去重验证声明。
