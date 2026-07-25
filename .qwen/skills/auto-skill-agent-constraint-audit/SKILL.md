---
name: agent-constraint-audit
description: Systematic audit of a project's declared engineering constraints vs actual CI enforcement vs real code violations, based on the 5-layer Agent Engineering framework
source: auto-skill
extracted_at: '2026-06-18T08:21:01.747Z'
---

# Skill: Agent Engineering Constraint Audit

## Goal

对项目的工程约束体系进行系统性审计，找出"文档声明"与"机器执行"之间的差距，产出可操作的修复优先级列表。

## When to Use

- 接手一个由 AI Agent 长期开发的项目时
- Sprint 开始前评估技术债
- Agent 产出的代码质量开始下降时
- 建立或升级 Harness 体系前

## 5-Layer Diagnostic Framework

按 Harness > Context > Evaluation > Orchestration > Prompt 的优先级顺序诊断。

## Procedure

### Step 1: 扫描实际违规

**先量事实，再读规则。** 不要先看 AGENTS.md，先扫代码。

```bash
# 非测试文件行数（按降序，取 top 20）
find internal -name '*.go' ! -name '*_test.go' \
  -exec awk 'END{print FILENAME": "NR}' {} \; \
  | sort -t: -k2 -rn | head -20

# 测试文件行数
find internal -name '*_test.go' \
  -exec awk 'END{print FILENAME": "NR}' {} \; \
  | sort -t: -k2 -rn | head -10
```

### Step 2: 读取声明约束

读 AGENTS.md §工程约束 和 HARNESS.md，提取所有量化阈值：

| 典型约束 | 阈值 |
|---------|------|
| 单文件行数 | ≤ 500 |
| 单函数行数 | ≤ 50 |
| 圈复杂度 | ≤ 10 |
| God 类型 | ≤ 300 行 |
| 测试覆盖率 | ≥ 50% / 80% |

### Step 3: 检查 CI 实际执行

读取 `.github/workflows/ci.yml` 和 `Makefile`，对每个声明约束确认：

- ✅ CI 硬门禁（fail = 拒绝合入）
- ⚠️ CI 警告（输出但不 fail）
- ❌ CI 未检查

**常见陷阱**：
- `golines` 是格式化工具（折长行），不是行数检查器
- `gocyclo` 报圈复杂度，不报函数行数
- 声明了 `make check` 但 check target 里没有真正的行数校验

### Step 4: 产出差距矩阵

| 约束 | AGENTS.md 声明 | CI 执行 | 实际违规数 | 差距 |
|------|---------------|---------|-----------|------|
| 文件 ≤ 500 行 | ✅ | ❌ | N 个文件 | **致命** |
| 圈复杂度 ≤ 10 | ✅ | ⚠️ 警告 | — | 弱门禁 |
| gofmt | ✅ | ✅ 强制 | 0 | 已闭环 |

### Step 5: 产出修复优先级

按框架重要性排序：

| 优先级 | 行动 | 类型 |
|--------|------|------|
| P0 | CI 加硬门禁（让违规 = build fail） | Harness |
| P0 | 拆分最大违规文件 | 技术债 |
| P1 | 补 Skills（让 Agent 有技能可调用） | Context |
| P2 | 量化 acceptance 标准 | Evaluation |
| P3 | Reviewer 检查清单 | Orchestration |

## Output Format

```markdown
## 约束审计报告

### 违规事实
- file.go: NNN 行 (+XX%)

### 差距矩阵
| 约束 | 声明 | CI | 违规数 | 判定 |
...

### 修复优先级
| P | 行动 | 工作量 | 收益 |
...
```

## Key Insight

> Harness 不执行等于不存在。文档里写 ≤500 行但 CI 不 fail，Agent 会无视这个约束。
> 真正的 Harness 是**机器级的、自动的、不可绕过的**。
