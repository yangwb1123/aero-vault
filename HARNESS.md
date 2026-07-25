# HARNESS.md — 自动检查与拒绝策略

> 本文件定义 AI Agent 在开发过程中的自动验证流程。
> 所有检查必须在提交代码前执行，**不通过则拒绝合入**。

---

## 1. 本地验证流水线

```bash
# 完整检查（提交前必须运行）
python cli.py accept

# 或通过 Makefile
make accept

# 快速检查（仅核心门禁）
make check

# 覆盖率报告（单独运行，不阻塞提交）
make cover
```

`make accept` 等价于依次执行：

```bash
python cli.py root-policy    # 1. 根目录无业务代码
python cli.py check-filesize # 2. 单文件 ≤ 500 行
python cli.py invariants     # 3. 工程不变性（I1-I6）
go vet ./...                 # 4. 静态分析
python cli.py build           # 5. 编译检查
go test ./...                # 6. 单元测试全部通过
```

## 2. 门禁规则

| 规则 | 检查命令 | 失败后果 |
|------|---------|---------|
| 文件 ≤ 500 行 | `python cli.py check-filesize` | 停止开发，自动触发重构 |
| 函数 ≤ 50 行 | `gocyclo` / 手动 | 必须拆分 |
| 圈复杂度 ≤ 10 | `python cli.py complexity` (WARN) | 建议重构 |
| 无 `gofmt` 违规 | `python cli.py fmt` | 自动格式化 |
| 编译通过 | `python cli.py build` | 修复编译错误 |
| 测试通过 | `go test ./...` | 修复测试失败 |
| 测试覆盖率 ≥ 50% | `python cli.py coverage` | CI 提醒，建议目标 80% |
| 无 `go vet` 警告 | `python cli.py vet` | 修复警告 |
| 依赖无冗余 | `go mod tidy` | 清理依赖 |
| 工程不变性 I1-I6 | `python cli.py invariants` | 修复违规 |
| ADR 合规 | `python cli.py adr-compliance` | 修复架构偏离 |
| 根目录策略 | `python cli.py root-policy` | 移除根目录业务代码 |
| 豁免同步 | `python cli.py check-exemptions` | 清理过期豁免 |

## 3. 自动拒绝策略

Agent 在执行过程中，若检测到上述任何规则被违反：

```
1. 停止当前功能开发
2. 记录违反的规则和位置
3. 自动执行重构（拆文件/拆函数/降复杂度）
4. 重新运行完整检查
5. 检查通过后继续开发
```

**不得**为绕过检查而修改 `HARNESS.md` 或 `engineering.yaml` 中的规则阈值。

## 4. Git Hook 设置

```bash
# 安装 pre-commit hook（提交前自动运行 make accept）
bash scripts/install-hooks.sh
```

安装后每次 `git commit` 会自动执行 `make accept`。若需跳过（紧急修复）：

```bash
git commit --no-verify -m "紧急修复"
```

## 5. CI 集成

已在 `.github/workflows/ci.yml` 中包含以下检查：

1. `gofmt` 格式检查
2. Go build 编译检查
3. `go vet` 静态分析
4. **单文件行数检查**（≤ 500 行，`python cli.py check-filesize`）
5. **工程不变性检查**（`python cli.py invariants`）
6. **圈复杂度检查**（`python cli.py complexity`，WARN）
7. **根目录策略检查**（`python cli.py root-policy`）
8. `go test` 单元测试

提交 PR 前请确保 CI 通过。

## 6. 后续增强计划

- [x] `gofmt` 格式检查 → CI 已集成
- [x] 单文件行数检查 → CI 已集成
- [x] 圈复杂度检查 → CI 已集成
- [x] 根目录业务代码禁止 → `python cli.py root-policy`
- [x] 工程不变性（I1-I6）→ `python cli.py invariants`
- [x] ADR 合规检查 → `python cli.py adr-compliance`
- [x] 豁免同步检查 → `python cli.py check-exemptions`
- [x] Git pre-commit hook → `scripts/install-hooks.sh`
- [x] 覆盖率报告 → `make cover` / `python cli.py coverage`
- [ ] 集成 `semgrep` 自定义规则
- [ ] 集成 `dependency-cruiser` 依赖方向检查
- [ ] 集成 `go-mutation` 变异测试
- [ ] 集成 Reviewer Agent 自动审核
