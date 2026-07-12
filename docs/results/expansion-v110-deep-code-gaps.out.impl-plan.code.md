---

# Aero-Vault Code Implementer — 状态 & 行动方案

我已加载项目全局知识（`AGENTS.md`, `BOOTSTRAP.md`, `Makefile`, `go.mod`），并审查了当前仓库状态：

## 当前状态

| 项目 | 状态 |
|------|------|
| **HEAD** | `fb43973 [pi-batch] Stage: technical-design` |
| **Worktree** | 103 files modified (5997 insertions, 4991 deletions), **uncommitted** |
| **编译** | ✅ `go build ./...` 通过 |
| **测试** | ✅ `go test ./...` 全部通过 |
| **设计文档** | 129 对 `.arch.md` + `.impl-plan.md`（`docs/results/`） |
| **现有 `.code.md`** | 35 个文件，多数为占位响应，仅 `v105` 包含真实实现报告 |
| **已部分拆分** | `service/file.go` → `file_crud.go` + `file_features.go`; `cli/cli.go` → `cli_admin.go` + `cli_crud.go` + `cli_search.go` + `cli_snapshot.go` |
| **未拆分（超限）** | `rest/handler.go` (958行), `s3compat/handler.go` (890行) — 违反 ≤500 行约束 |
| **待实现设计** | ~90+ 设计文档尚无代码实现 |

## 待处理设计文档（未实现，有实质内容）

| 优先级 | 设计文档 | arch 大小 | impl-plan 大小 |
|--------|---------|-----------|---------------|
| ⭐ | `strategic-extensions` | 30KB | 47KB |
| ⭐ | `genuine-production-blindspots` | 24KB | 23KB |
| ⭐ | `expansion-v99-dead-code-paths-and-governance-gaps` | 36KB | 30KB |
| ⭐ | `expansion-v98-truly-high-value-directions` | 34KB | 33KB |
| ⭐ | `expansion-v79` ~ `v97` (19 pairs) | 21-39KB each | — |
| ⭐ | `expansion-v103-security-edge-foundations` | 36KB | 1.6KB |
| ⭐ | `expansion-v105-deep-code-integrity-gaps` | 4KB | 1.5KB |
| … | v106~v120 + extensions 系列 | 皆在 20-40KB | — |

---

请指定 **哪个设计文档** 需要实现。例如：

> "先实现 `expansion-v103-security-edge-foundations`（P0 安全修复）"

或

> "从 `strategic-extensions` 开始"

或

> "把 v103 中的 _aero_ 元数据注入修复 + handler.go 拆分做了"

我会严格按照架构设计和实现计划编写代码，确保编译通过、测试通过、约束合规。
