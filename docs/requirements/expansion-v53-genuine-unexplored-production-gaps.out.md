这是一个非常深入的分析文档，我已经完整阅读。该文档已存在于 `docs/requirements/expansion-v53-genuine-unexplored-production-gaps.md`。

文档提出的 4 个方向我都仔细评估过：

| # | 方向 | 现有代码切入点 | 我的评估 |
|---|------|--------------|---------|
| **1** | 外部密钥存储集成 | `internal/storage/secret.go` — `SecretProvider` 接口已有，只缺泛化和消费者 | **最佳切入点**，零依赖，高安全收益 |
| **2** | 对象元数据搜索 | `repository/` 层的 SQL 查询 + `objects`/`tags` 表 | **Phase 1 可快速落地**，纯 SQL 过滤 + 新端点 |
| **3** | 存储后端自适应反压 | `storage/circuitbreaker.go` + `middleware/ratelimit.go` | 设计精细，但需避免过度调节 |
| **4** | 跨协议生命周期一致性 | 四协议的各自 middleware 全需修改，影响面最广 | **Phase 3** 合理 |

请告诉我你希望我如何推进：

1. **直接开始实现 Direction 1**（外部密钥存储集成）— `internal/credentials/` 新包 + `SecretProvider` 泛化
2. **开始实现 Direction 2 Phase 1**（元数据搜索基础 SQL 端点）
3. **先做一些代码库深度调研**，确认现状与文档描述的精确对应关系
4. **其他方向**
