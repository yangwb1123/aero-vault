两份文件几乎完全相同（仅差一个尾部换行符）。您的 v39 分析已在此仓库中存在（`expansion-v39-production-blindspots.md`，7月10日保存）。

---

## 分析确认

我已完整阅读了您的 v39 分析文档。以下是关键发现和行动准备评估：

### 已确认的代码盲区

| 方向 | 验证状态 | 实际代码风险 |
|------|---------|------------|
| **1. SSE Subscriber 泄漏** | `internal/events/bus.go` 确实无 `Unsubscribe` 方法，`b.subs` 只增不减 | ✅ **确认** — 运行时内存泄漏明确 |
| **2. Worker 健康管理缺失** | `cmd/server/main.go` 中 15+ `go func()` 无重启/健康注册 | ✅ **确认** — 僵尸 worker 检测不到 |
| **3. 多租户计算隔离** | `ConcurrencyLimiter` 全局、`jobs.Pool` 全局共享 | ✅ **确认** — Noisy neighbor 风险 |
| **4. /metrics 未认证** | `isBypassPath` 包含 `/metrics`，指标含 `tenant` label | ✅ **确认** — 信息泄露 |
| **5. Job Queue 租户隔离** | `Queue.MaxDepth` 全局、`ClaimJob` 纯 FCFS | ✅ **确认** — 租户间饥饿风险 |

### 建议下一步

您希望我从哪个方向开始实现代码变更？按您文档中的优先级排序：

1. **🔥 立即 — 方向 1 (SSE Unsubscribe)**：新增 `Bus.Unsubscribe()` + SSE handler defer 清理 + `SSE_MAX_CONNECTIONS` 上限
2. **🔥 立即 — 方向 4 (/metrics 认证)**：`METRICS_AUTH_REQUIRED` 配置开关 + 分级指标暴露
3. **🟠 短期 — 方向 2 (Worker 健康)**：`/readyz` 集成 + supervisor 模式 + worker 指标
4. **🟠 中期 — 方向 5 (Job Queue 隔离)**：Per-tenant pending cap + 优先级队列
5. **🟡 长期 — 方向 3 (计算资源隔离)**：综合架构调整

请指示实施的起点和范围。
