文档已保存至 `docs/requirements/expansion-v116-high-value-expansion-directions.md`（16.7 KB）。

### 保存概况

- **文件名：** `expansion-v116-high-value-expansion-directions.md`（延续 v1xx 序列，v115 之后）
- **文件位置：** `/home/u1/aero-vault/docs/requirements/`
- **去重范围：** 对 `docs/requirements/` 下全部 **136 份**既有分析文档进行了逐方向的关键词正则 + 语义交叉验证

### 五大方向的落地价值总结

| # | 方向 | 优先级 | 核心洞察 | 独特价值 |
|---|------|--------|---------|---------|
| 1 | **基础设施即代码与存储集成生态** | P0 | 代码有完整 SDK/API，但无 Terraform Provider、K8s CSI、FUSE、CI/CD Action — 用户无法声明式集成 | **从"可 curl 的服务"变为"基础设施的声明式组成部分"** |
| 2 | **租户数据迁移与自助导入导出** | P0 | `snapshot.go` 仅 SQLite+local FS；企业从 S3 迁移只能手动上传 | **企业入驻的第一个 Contact Point** |
| 3 | **自定义域名与静态网站托管** | P1 | S3 最广泛用例之一（GitHub Pages/Netlify 底层模式），代码锚点（CORS、策略引擎、`acl.IsPublicRead`）已就绪 | **覆盖 S3 生态的最后一个关键缺口** |
| 4 | **性能基准测试套件** | P1 | CI gate 零性能门禁；15+ OTel 指标无人采集为基准；扩容靠猜 | **从"功能正确"到"性能可控"的工程基础设施** |
| 5 | **对象存储分析生态与数据湖集成** | P2 | 代码有 StorageClassCounts/BucketStats 等聚合锚点，但无 Trino/Spark/Hive/Iceberg 连接 | **存储平台 → 数据平台的跃迁** |

### 实施路线（Q1-Q4）

- **Q1：** Terraform Provider v1 + GitHub Action + `make bench` 基准套件 → **2-3 周 + 2 周**
- **Q2：** S3 导入器 + 租户导出 API + 静态网站托管 MVP → **3-4 周 + 2 周**
- **Q3：** 元数据查询 API + K8s CSI Driver alpha + FUSE beta → **4 周 + 6-8 周**
- **Q4：** 增量 CDC 迁移 + Trino Connector → **4 周 + 4 周**

每个方向均附有代码锚点表、架构权衡分析和边界情况清单，可直接作为开发任务的输入。
