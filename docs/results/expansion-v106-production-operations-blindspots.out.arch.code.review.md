现在我收集了所有数据。这是全面的代码质量审查。

---

# 🏗️ 代码质量审查：aero-vault

## 整体质量：需要改进

### 执行摘要

该项目在架构上很完善——关注点分离清晰（处理层、服务层、存储层、仓库层），测试套件覆盖 107 个文件（全部通过），且 CI 门控通过。然而，存在严重的**工程约束违规**和**技术债务**，如果不加以解决，将损害长期可维护性。多个文件违反了 AGENTS.md 约定的 500 行限制，`repository.Repository` 接口是一个 god 接口，并且有多个高圈复杂度的函数。整体测试覆盖率（67%）低于 80% 的目标，且一些关键组件覆盖率极低或为零。

---

## 发现项

### 1. 代码组织

| 领域 | 严重性 | 标题 | 位置 | 描述 | 当前状态 | 建议状态 | 影响 | 工作量 |
|--------|----------|-------|----------|-------------|----------------|-----------------|--------|--------|
| 组织 | **严重** | 文件大小违规（>500 行） | `internal/api/rest/handler.go` (958), `internal/api/s3compat/handler.go` (890), `cmd/server/main.go` (861), `internal/auth/condition.go` (657) | AGENTS.md 规定单文件 ≤500 行。5 个源文件超过该限制。`handler.go` 几乎是限制的两倍。 | 单体文件包含大量处理函数 | 拆分为领域特定的文件：`handler_files.go`、`handler_buckets.go`、`handler_batch.go`、`handler_folders.go` | 降低可导航性；增加合并冲突风险；阻碍并行开发 | L |
| 组织 | **严重** | God 接口 | `internal/repository/repository.go:30-394` | `Repository` 接口声明了约 60 个方法，涵盖对象、桶、块、上传、事件、作业、API 密钥、租户、审计日志等。这是一个经典的 God 接口。 | 单个接口涵盖所有持久化操作 | 拆分为更小的角色接口：`ObjectStore`、`BucketStore`、`ChunkStore`、`JobQueue`、`KeyStore`、`TenantStore`、`AuditLog`。通过组合使用。 | 使实现复杂化；难以模拟；违反接口隔离原则 | L |
| 组织 | **高** | God 结构体 | `internal/repository/sql.go` | `sqlStore` 结构体实现了 Repository 的全部约 60 个方法，分散在 15+ 个文件中。是该层的一个 God 结构体。 | 单个结构体处理所有 SQL 操作 | 拆分为按领域划分的结构体：`objectStore`、`bucketStore`、`chunkStore`、`jobStore`（均嵌入一个共享的 `*sql.DB` 句柄） | 使对新后端的测试和扩展变得复杂 | L |
| 组织 | **中** | `main.go` 职责过多 | `cmd/server/main.go` (861 行) | `main.go` 不仅负责启动，还负责：构建存储、嵌入器、LLM、重排序器、索引器、防病毒、复制、事件总线、MCP、CLI、所有 Worker、中间件链、路由注册、优雅关闭。是 God 文件。 | 全部集中在 main.go | 将构建逻辑提取到 `internal/app` 包中，包含 `App` 结构体和构建器方法。`main.go` 应只剩 50 行。 | 使启动逻辑不可测试；难以理解依赖关系图 | L |

### 2. 命名与文档

| 领域 | 严重性 | 标题 | 位置 | 描述 |
|--------|----------|-------|----------|-------------|
| 命名 | **低** | 不一致的接收器命名 | `internal/repository/sql_objects.go` | 有些方法使用 `(s *sqlStore)`，而其他部分使用 `(r *sqlStore)` 或 `(repo *sqlStore)`。没有统一的约定。 |
| 文档 | **中** | 文档覆盖率低 | 整个代码库 | 许多导出类型和方法缺少文档注释。`Repository` 接口中的方法大多没有文档。`Storage` 接口有基本文档，但实现（S3、OSS、COS）缺乏。 |
| 文档 | **低** | TODO/FIXME 痕迹 | 已审查文件 | 发现了一些 TODO，但没有关键或过期的 FIXME。建议在代码库中搜索。 |

**建议：** 对所有导出符号坚持使用 Godoc 注释。使用 `golangci-lint` 强制执行 `revive` 或 `go doc` 规则。

### 3. 错误处理

| 领域 | 严重性 | 标题 | 位置 | 描述 |
|--------|----------|-------|----------|-------------|
| 错误处理 | **高** | `flexTime` 解析过于宽松 | `internal/repository/sql_helpers.go:214-240` | `flexTime.parse` 尝试 4 种日期格式，然后回退到 `strconv.ParseInt`（unix 纳秒）。这种隐藏的格式会静默地接受输入错误。 |
| 错误处理 | **中** | 静默吞错误 | `internal/storage/local.go:85` | `NewLocal` 中的 `os.MkdirAll` 错误在 API 级别被吞掉，如果 Root 不可写，则导致存储静默失败。 |
| 错误处理 | **低** | 错误检查不一致 | `internal/storage/rewrap.go:32-65` | 有些地方使用 `if err != nil` 进行详细的错误处理，而其他函数则返回简单包装的错误。建议统一使用 `fmt.Errorf` 和 `%w`，或使用标准化的错误包装。 |

**总体评价：** 错误处理普遍较好——使用了哨兵错误（`ErrNotFound`、`ErrQuotaExceeded`），正确使用了 `errors.Is` 和 `errors.Join`。但可以更严格地确保没有路径静默地吞掉错误。

### 4. 日志记录

| 领域 | 严重性 | 标题 | 位置 | 描述 |
|--------|----------|-------|----------|-------------|
| 日志记录 | **低** | 缺少结构化字段 | `internal/service/file_crud.go` | 有些日志消息在结构化日志记录器中使用了字符串格式化，而不是关键字参数：`s.logger.Warn(fmt.Sprintf(...))` 而不是 `s.logger.Warn("msg", "key", val)`。 |
| 日志记录 | **低** | 重复日志前缀 | 整个代码库 | 没有使用 `slog.Group` 来逻辑分组相关字段。请求范围内的字段（tenant、request_id）没有被自动注入。 |
| 日志记录 | **中** | 机密日志记录 | `internal/config/config.go:152` | 配置解析会记录端点 URL，这些 URL 可能包含路径中的查询参数/令牌。应剥离机密。 |

### 5. 测试实践

| 领域 | 严重性 | 标题 | 位置 | 描述 |
|--------|----------|-------|----------|-------------|
| 测试 | **严重** | 覆盖率低于目标 | 整体 67%，许多包 <50% | AGENTS.md 要求 ≥50%。但目标应为 80%。许多关键组件的覆盖率低得危险：`internal/api/rest/handler.go` ~53%，`internal/storage/s3.go` ~10%，`internal/storage/circuitbreaker.go` ~0%。 |
| 测试 | **严重** | 零覆盖率 - 存储后端 | `internal/storage/s3.go`、`oss.go`、`cos.go`、`circuitbreaker.go` | 所有云存储后端（S3、OSS、COS）以及断路器包装器完全没有测试。这些是生产关键路径。 |
| 测试 | **高** | 零覆盖率 - 服务特性 | `internal/service/file_features.go` ~0% | `List`、`ListDeleted`、`BatchDelete`、`BatchSetTags`、`PresignGet`、`PresignPut`、`BucketPolicy`、`BucketCORS`、`BucketLogging`、`BucketNotifications`，共约 30 个方法——全部 0% 覆盖率。 |
| 测试 | **高** | 零覆盖率 - 遥测指标 | `internal/telemetry/metrics.go` | 25 个指标记录函数中有 15 个（60%）覆盖率为 0%。指标代码虽然简单，但未经测试会腐烂。 |
| 测试 | **中** | 缺乏基于属性的测试 | 整个代码库 | 没有模糊测试（`*_test.go` 中没有 `f.Fuzz`）。诸如 `ParseByteRange`、`validateMetadata`、`validateKey` 之类的函数非常适合基于属性的测试。 |
| 测试 | **中** | 测试套件中的重复设置 | 所有测试 | 每个测试文件都重复了 `setupTest` / `newTestSvc` / `req` 辅助函数。应提取到一个共享的 `testutil` 包中。 |
| 测试 | **低** | 缺少表驱动测试 | `internal/service/service_test.go` | 测试使用顺序的 `TestXxx` 函数，而不是表驱动测试。添加表驱动测试可以使边界情况更清晰。 |

### 6. 技术债务

| 领域 | 严重性 | 标题 | 位置 | 描述 |
|--------|----------|-------|----------|-------------|
| 债务 | **严重** | 硬编码默认租户 | `internal/api/rest/handler.go`、`internal/middleware/middleware.go` | `DefaultTenant = "default"` 被硬编码到每个处理函数中。多租户实现通过在中间件后设置上下文值来工作，但处理函数会回退到字符串 `"default"`。 |
| 债务 | **高** | SQL 方言复制 | `internal/repository/sql_*.go` | SQLite 和 Postgres 查询在整个 `sql_objects.go`、`sql_buckets.go`、`sql_chunks.go` 中重复，包含 `if dialect == dialectPostgres { ... } else { ... }` 块。这造成了一个巨大的重复表面。 |
| 债务 | **高** | 自定义 UUID 生成 | `internal/repository/sql_helpers.go:83-100` | `uuidLike()` 通过使用 `crypto/rand` 和手动格式化的方式重新实现了 UUID 生成。使用 `github.com/google/uuid`（已在 go.mod 中）会更安全、更可读。 |
| 债务 | **中** | 用于版本 ID 的链式格式 | `internal/repository/sql_helpers.go` | 日期格式使用 `time.RFC3339Nano`，但文档（AGENTS.md I1）指定了 `RFC3339Nano`。SQL 层有时使用 `now()` 而不是传输 Go 的格式化时间。 |
| 债务 | **中** | 自定义 LTSV 状态编写器 | `internal/middleware/middleware.go:149-172` | `statusWriter` 作为 `http.ResponseWriter` 的包装器，复制了 `_status` 和 `_bytes` 跟踪。但缺少 `Hijack` 和 `Push` 转发。SSE 和 WebSocket 可能间接依赖于此。 |
| 债务 | **低** | `strFromInt` 自定义实现 | `internal/middleware/cors.go:81-94` | 一个自定义的整数到字符串的函数，使用手动缓冲区。可以使用 `strconv.Itoa`。 |

### 7. 代码质量指标

| 指标 | 当前值 | 目标 | 状态 |
|--------|---------|--------|--------|
| 圈复杂度 | 最高 53（`compileSingleCondition`）| < 10 | ❌（6 个函数 > 10） |
| 函数长度 | `main.go:run()` 约 200 行 | < 50 行 | ❌ |
| 测试覆盖率 | 67% | > 80% | ⚠️（低于目标，但高于 AGENTS.md 的 50% 最低要求） |
| 代码重复 | 中等（SQL 方言分叉）| < 5% | ⚠️ |
| 文档覆盖率 | ~35%（估计）| > 70% | ❌ |
| 单文件大小 | 5 个文件 > 500 行 | < 500 行 | ❌ |

---

## 技术债务登记表

| 项目 | 影响 | 工作量 | 优先级 | 备注 |
|------|--------|--------|----------|-------|
| 重构 `handler.go`（拆分文件）| 高 | L | P0 | 违反 AGENTS.md 500 行规则 |
| 拆分 God 接口 `Repository` | 高 | L | P0 | 违反 AGENTS.md "禁止 God 类型" 规则 |
| 拆分 God 结构体 `sqlStore` | 高 | L | P0 | 与上述相同 |
| 减少 `main.go` 的职责 | 中 | L | P1 | God 函数，无法测试 |
| 云存储后端的测试覆盖 | 高 | M | P1 | S3、OSS、COS 为 0% 覆盖率 |
| `file_features.go` 的测试覆盖 | 高 | M | P1 | 约 30 个方法为 0% 覆盖率 |
| 统一 SQL 方言处理 | 中 | L | P1 | 跨 sql_objects、sql_buckets、sql_chunks 的重复模式 |
| 添加模糊测试 | 低 | S | P2 | 适用于解析器/验证器 |
| 提取共享测试辅助函数 | 低 | S | P2 | 跨包重复 |
| 用 `strconv.Itoa` 替换 `strFromInt` | 低 | S | P3 | CORS 中间件 |

---

## 🚨 主要发现：违反 AGENTS.md 工程约束

AGENTS.md 第 0 节定义了具体的规则。以下是违规情况：

| 规则 | 阈值 | 结果 |
|------|-------|--------|
| 单文件 ≤ 500 行 | 500 | ❌ **5 个违规文件：** `handler.go`（958）、`s3compat/handler.go`（890）、`main.go`（861）、`condition.go`（657）、`client.go`（1006） |
| 禁止 God 类型（> 300 行） | 300 | ❌ **`Repository` 接口**（394 行，60 个方法）和 **`sqlStore` 结构体**（分布在 15+ 个文件中的约 60 个方法） |
| 圈复杂度 ≤ 10 | 10 | ❌ **6 个函数违规：** `compileSingleCondition`（53）、`ConditionContext.Get`（18）、`FileService.Put`（13）、`BucketDispatch`（13）等 |
| 测试覆盖率 ≥ 50% | 50% | ⚠️ **整体 67%** 通过，但**关键包失败：** `internal/service/file_features.go`（0%）、`internal/storage/s3.go`（~12%）、`internal/storage/circuitbreaker.go`（0%）、`internal/storage/oss.go`（0%） |
| 禁止 `utils/`、`common/`、`helper/` 包 | — | ✅ 未发现此类包 |

---

## 快速取胜

1. **修复 `strFromInt` → `strconv.Itoa`**（`internal/middleware/cors.go:81`，5 分钟）
2. **用 `github.com/google/uuid` 替换 `uuidLike()`**（`internal/repository/sql_helpers.go:83`，已在 go.mod 中，10 分钟）
3. **将共享测试设置提取到 `internal/testutil` 包**（消除 10+ 个文件中的重复，1 小时）
4. **为零覆盖率的指标函数添加单元测试**（仅自动化测试，30 分钟）
5. **在指标记录函数和日志调用中添加结构化日志关键字**（30 分钟）

---

## 总结

- **整体代码质量：** 需要改进
- **关键质量问题：** 5 个文件违反 ≤500 行限制，Repository God 接口（60 个方法），6 个函数的圈复杂度 >10，云存储后端覆盖率为 0%
- **可维护性问题：** God 接口和结构体使扩展和模拟变得困难。`main.go` 试图做所有事情，阻止了启动逻辑的单元测试。SQL 方言重复意味着 Postgres 的每个功能更改都需要 SQLite 的镜像更改。
- **技术债务：** 显著。在不解决底层结构问题的情况下添加功能，会使扩展变得更加昂贵。
- **优势：** 架构是合理的（分层架构）。测试套件运行迅速且一致。错误处理模式良好（哨兵错误、`errors.Is`、适当的包装）。遥测和可观测性基础设施是一流的。`go vet` 检查通过。使用了现代 Go 实践（`slog`、嵌入式迁移）。

**建议：** 在添加新功能之前，将三个 P0 违规作为一次专门的重构冲刺来解决：拆分 `handler.go`、拆分 `Repository` 接口、减少 `main.go` 的职责。这将为接下来的增长奠定更健康的基础。
