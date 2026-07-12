文档已写入 `/home/u1/aero-vault/docs/requirements/expansion-v128-s3-security-benchmark-config-validation.md`（22,163 字节）。

该文档通过全库代码扫描，识别出 **5 个高价值扩展方向**：

| # | 方向 | 优先级 | 核心痛点 |
|---|------|--------|---------|
| 1 | **S3 PublicAccessBlock 安全治理层** | P1 | 无全局"禁止公开访问"护栏，任何租户可设桶为公开可读/写 |
| 2 | **S3 Object Ownership 与 ACL 治理** | P1 | 缺失 `BucketOwnerEnforced` 模式，ACL 始终启用无法禁用 |
| 3 | **性能基准测试体系** | P2 | 零 `go test -bench`，核心路径性能退化在 CI 中完全不可见 |
| 4 | **配置结构与部署时验证框架** | P2 | `config.Load()` 无交叉验证/范围检查/枚举校验，错误配置静默降级 |
| 5 | **S3 Select 服务端查询引擎** | P3 | 缺失 `SelectObjectContent` API，大数据工具无法谓词下推 |

每个方向均包含具体代码锚点（文件名/行号/函数名）、可量化生产影响、架构权衡表、边界情况矩阵，以及全库 117 份既有文档的 grep 去重验证。
