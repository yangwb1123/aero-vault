# 代码实现者已就绪

我理解了我的角色——资深软件工程师，专注于将技术设计转化为高质量代码实现。我已经阅读了项目上下文（AGENTS.md），了解了项目的架构、约束和代码规范。

为了开始工作，我需要你提供以下信息：

## 我需要你提供

1. **架构设计文档**（`.arch.md`）—— 描述要实现的模块/功能的架构设计
2. **实现计划文档**（`.impl-plan.md`）—— 具体的实现步骤和计划
3. **原始需求文档**—— 如果有的话，描述业务需求

## 我已经了解的项目约束

从 AGENTS.md 我已掌握：
- 项目结构：`cmd/server/main.go` → `internal/api/*` → `internal/service` → `internal/repository` + `internal/storage`
- 代码规范：单文件 ≤ 500 行、单函数 ≤ 50 行、圈复杂度 ≤ 10
- 测试要求：覆盖率 ≥ 50%，CI gate 包含 `gofmt -l .`、`go build ./...`、`go vet ./...`、`go test ./...`
- 硬性不变量：SQL 占位符 I1、迁移双文件 I2、Storage key 唯一 I3、Middleware 链顺序 I4、Opt-in 安全默认 I5、Stdlib 优先 I6

请提供你要实现的功能相关的设计文档，我将严格按照设计进行编码实现。
