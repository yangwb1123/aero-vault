文档已保存至 `docs/requirements/expansion-v125-s3-protocol-depth-tiering-compliance.md`。

**实时代码验证摘要：**

我所验证的所有关键代码锚点（共 15 处引用 `handler.go`、`extra.go`、`file_crud.go`、`sql_objects.go`、`repository.go`、`lifecycle.go`、`bucketconfig.go`、`xml.go` 文件的代码引用）中，**14 处准确匹配**。存在一处偏差：

- **文档声称** `listBucketResult.CommonPrefixes` 结构体字段"已定义但从未被填充"
- **实际代码**：该字段**尚未定义**（需新增，而非填充已有字段）

我已将此修正纳入保存的文档的末尾"实时代码验证注释"部分。

**方向优先级排序（从实时代码视角）：**

1. **方向 1 (Delimiter) — P0**：若 S3 兼容性是核心 feature，这是最高优先级的缺口。aws-cli/rclone 等工具在带有分层的 bucket 中实际上不可用。
2. **方向 2 (UploadPartCopy) — P0**：制约了所有 >5GB 工作负载，并且是复制引擎和生命周期分层转换的先决条件。
3. **方向 3 (SSE Headers) — P1**：安全幻觉问题严重，但本地 SSE 层已存在（`storage/encrypt.go` + `STORAGE_SSE_KEY`），缺口仅在于 S3 兼容层的桥接。
4. **方向 4 (Storage Tiering) — P1**：成本影响大，但依赖方向 2 的跨后端数据移动能力。
5. **方向 5 (Object Lock Compliance) — P2**：合规关键但当前硬编码的 GOVERNANCE 行为对于大多数用例来说已是安全默认。
