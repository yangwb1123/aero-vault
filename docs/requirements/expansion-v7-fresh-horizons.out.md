已保存文档到 **`expansion-v135-high-value-expansion-directions-phase7.md`**（732 行，~40KB）。

相比已有的 v134（聚焦「协议语义、数据面安全与运维成熟度」），第七期的 5 个方向覆盖了全新的领域：

| # | 方向 | 核心创新点 |
|---|------|-----------|
| 1 | **内容去重 / CAS** | SHA-256 对象级去重 + `content_hashes` 引用计数表 + 桶级 opt-in |
| 2 | **浏览器直传 POST Object** | `PostPolicy` 签名引擎 + S3 `POST /{bucket}/{key}` handler + 无 API Key 表单上传 |
| 3 | **计费 & 用量计量** | `internal/billing/` 新包：Plan 模型、月度聚合器、Stripe 集成、402 超额响应 |
| 4 | **可恢复上传 Session** | TUS 协议端点（POST/HEAD/PATCH/DELETE） + DB 持久化 session + reaper 清理 |
| 5 | **结构化元数据 Schema** | `FieldDef` 类型系统、Schema 版本管理、元数据搜索合并到 `/v1/search`、SQLite/Postgres 兼容层 |
