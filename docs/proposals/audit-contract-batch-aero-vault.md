实施提案已产出：`docs/proposals/audit-contract-batch-aero-vault.md`（259 行，未修改任何代码）。

## 核心结论摘要

**基线发现（工作树）**：上一轮 campaign 已未提交完成 B3-1 的仓库层（0042 迁移 `failed_at_ns` + `FailAuditGovernance` + claim/lag 死行排除）和 conflict 回执终态、B3-5 测试侧。本轮实施对象是：B3-1 的 422/409/无效回执分类扩展、B3-2（`Ready()` maxLag 翻转仍在）、B3-3（facts.go 仍 uuid）、B3-4（包内 0 telemetry）、B3-6（`Validate()` 空 bindings 缺口）。

**逐项设计要点**：
- **B3-1**：`isPermanentDeliveryError` 分类函数替换 `deliverFact` 单哨兵（conflict/无效回执/409/422 → 终态；401/403/5xx 保持瞬态，cap 300s 已满足）
- **B3-2**：删 `Ready()` 翻转、新增 `BacklogAge`、run() 循环 maxLag×0.5（450s）降级告警、store 查询内部超时降级非 503
- **B3-3**：⚠️ **清单路径不准确**——`origin_id` 在 facts.go 构造点不可知（repository 层 `RETURNING` 才分配），确定性 ID 公式必须单点定义于 repository 写层三方法
- **B3-4**：5 个 `audit_governance.relay_*` 指标 + observable gauge + alerts.yml 三条 H2 规则
- **B3-5**：核验成立（`RequiredScope` + scope 拒绝测试已在），纯验证项
- **B3-6**：`Validate()` 补空 bindings fail-closed；⚠️ 清单动作串 `file.delete.admin` 在本仓不存在（实际 `file.delete`/`file.deleted`）

**未验证标注（10 项）**：0039→0042 迁移路径差异、facts.go 构造点不可行、动作串 mismatch、time_bucket 粒度、指标/告警命名、日志节流、fake Store 方案、部分索引、跨仓 drill、工作树变更归属。

**门禁**：G4 中 T-3/T-4/D1 单元映射可独立先行；完整 G4 需 G1/G2（B1-1+B4-1+B1-7）与 B4-2（B3-5 矩阵配给）跨仓就绪。
