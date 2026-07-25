# Checks Registry

> 所有工程门禁统一由 `engineering.yaml` 配置阈值，由 `cli.py` + `checks/` 模块执行。
> 本地：`python3 cli.py <command>`；CI：`.github/workflows/ci.yml` 自动运行。

---

## 门禁列表

| 命令 | 模块 | 阈值 | 类型 | 说明 |
|------|------|------|------|------|
| `check-filesize` | `checks/filesize.py` | ≤ 500 行 | 🔴 硬门禁 | 单文件行数上限 |
| `complexity` | `checks/complexity.py` | ≤ 10 | 🟡 WARN | 圈复杂度（需 `gocyclo`） |
| `architecture` | `checks/architecture.py` | 分层规则 | 🟡 WARN | 包依赖方向 |
| `root-policy` | `checks/root_policy.py` | 白名单 | 🔴 硬门禁 | 根目录无业务代码 |
| `invariants` | `checks/invariants.py` | I1-I6 | 🔴 硬门禁 | 工程不变性（SQL/迁移/存储key/opt-in/testify） |
| `adr-compliance` | `checks/adr_compliance.py` | ADR-001~005 | 🟡 WARN | 架构决策合规 |
| `check-exemptions` | `checks/exemptions.py` | 无过期豁免 | 🟡 WARN | 豁免同步检查 |
| `coverage` | `checks/coverage.py` | ≥ 50% | 📊 信息 | 测试覆盖率 |
| `build` | `checks/build.py` | 编译通过 | 🔴 硬门禁 | 构建所有 binary |
| `accept` | `checks/acceptance.py` | 全项通过 | 🔴 硬门禁 | 完整验收流水线 |
| `health-report` | `checks/health_report.py` | — | 📊 信息 | 综合健康报告 |

---

## 运行方式

```bash
# 单个检查
python3 cli.py check-filesize
python3 cli.py invariants

# 分组
python3 cli.py check          # filesize + vet
python3 cli.py harness        # filesize + complexity + architecture
python3 cli.py accept         # 完整验收（HARNESS.md）

# 通过 python3 -m checks
python3 -m checks filesize
python3 -m checks all

# 通过 Makefile
make check                    # fmt + vet + build + test + cli-check
make accept                   # cli-accept = full acceptance suite
```

---

## CI 集成

GitHub Actions 在每次 push/PR 自动运行：

```
gofmt → root-policy → check-filesize → invariants
→ adr-compliance → complexity → check-exemptions
→ go build → go vet → go test → coverage
```

所有门禁均在 `.github/workflows/ci.yml` 中声明。
