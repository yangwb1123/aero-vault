文件已保存为 `docs/requirements/expansion-v142-multimodal-ai-metadata-search-dedup-cache-active-active.md`（24,270 字节）。

以下是保存的归档摘要：

| 信息项 | 内容 |
|--------|------|
| **文件名** | `expansion-v142-multimodal-ai-metadata-search-dedup-cache-active-active.md` |
| **目录** | `docs/requirements/` |
| **5 大方向** | ① 多模态 AI 管线 ② 元数据锚定语义搜索 ③ 内容寻址存储与块级去重 ④ 对象内容缓存层次 ⑤ 主动-主动多区域复制 |
| **去重验证** | 对 97 份既有分析 + ROADMAP + TODO 全文正则搜索，确认为**零实质性覆盖** |
| **代码锚点** | 每方向均引用具体源文件（`extractor.go`, `search.go`, `vectorindex.go`, `qdrant.go`, `pgvector.go`, `file_crud.go`, `storage.go`, `replication.go` 等） |
| **建议实施顺序** | 方向二（元数据锚定搜索）→ 方向一（多模态管线）→ 方向三（内容去重）→ 方向四（对象缓存）→ 方向五（主动-主动多区域） |
