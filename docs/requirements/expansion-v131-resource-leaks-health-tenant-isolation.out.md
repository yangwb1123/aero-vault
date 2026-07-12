文件已保存至 `docs/requirements/high-value-extensions-v39.md`（21,116 bytes）。

概要：
- **5 个方向**，全部聚焦运行时行为盲区而非功能缺口
- **P1 严重**：SSE Subscriber 通道泄漏（`bus.go` 无 `Unsubscribe`）+ 15+ 后台 Worker 零健康可见性
- **P2 高**：`/metrics` 端点未认证泄滞留户数据 + 多租户计算资源隔离缺失
- **P2 中**：Job Queue 全局 FIFO 无租户级公平调度
- 每个方向含现状分析、影响量化、代码锚点、缺失能力清单、边界条件表
- 建议实施顺序：方向 1 → 方向 4（立即）→ 方向 2（短期）→ 方向 5（中期）→ 方向 3（长期）
