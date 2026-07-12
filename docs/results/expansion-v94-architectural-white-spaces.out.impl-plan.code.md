基于对项目代码库的分析，我看到：

1. **上一轮 `pi-batch` pipeline** 完成了 requirement-analysis 和 technical-design 两个阶段，生成了 `.arch.md` 和 `.impl-plan.md` 文件
2. **code-implementation 阶段尚未执行** — 49 个 `.arch.code.md` 文件都只是空壳（内容为"请提供设计文档"）
3. **AGENTS.md** §4 要求：所有变更必须通过 `make check`（gofmt、go build、go vet、go test）

---

## 为了实现代码，请确认以下信息

请告诉我你想实现的具体功能。例如：

> 请实现 `expansion-v118-architect-frontiers` 的代码，对应文件是：
> - `docs/results/expansion-v118-architect-frontiers.out.arch.md`
> - `docs/results/expansion-v118-architect-frontiers.out.impl-plan.md`

或者直接指定某个需求方向：

| # | 方向 | 文件前缀 | 规模 |
|---|------|---------|------|
| 1 | Graceful Shutdown 异步排空 | `expansion-v120-bootstrap-migration-graceful-shutdown` | 中 |
| 2 | 数据库连接韧性体系 | (同上) | 中 |
| 3 | CORS 与安全头硬化 | (同上) | 小 |
| 4 | 其他已有完整 arch+impl-plan 的方向 | `expansion-v118-architect-frontiers` 等 | 中 |

一旦你指定了要实现的功能，我会：
1. 读取对应的 `.arch.md` + `.impl-plan.md` + 原始需求 → 理解设计
2. 实现所有代码（遵循 Go 1.25、chi/v5、工程约束）
3. 运行 `make check`（gofmt、build、vet、test）确保全绿
4. 输出完整的**代码实现报告**
