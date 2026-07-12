已为您的分析文档 `fresh-horizons-systemic-gaps.md`（364 行）完成了完整的代码交叉验证响应，保存为 `fresh-horizons-systemic-gaps.out.md`（384 行）。

### 响应内容概要

对 5 个方向逐一进行了代码级验证与补充：

| 方向 | 验证结论 | 关键补充 |
|------|---------|---------|
| **方向一：Tag 治理引擎** | ✅ 全部断层线代码锚点确认 | 新增 Tag 变更时序竞态分析（Snapshot vs. ReadCommitted 模式）；风险分析包括治理引擎反馈循环的防护需求 |
| **方向二：风暴防护** | ✅ 确认 `broadcast()` `sendOrDrop` 零指标 + `deliver()` 无限制 goroutine 扇出 | 拆为三层（观测→控制→检测）；提出 TraceId 跳数检测方案替代难以实现的回路检测 |
| **方向三：分布式限流** | ✅ 确认 `tokenBucket` 纯内存实现 + lazy-init 导致新租户不受限 | 补充 RPS=0/-1 语义、加权请求成本、新租户预热期、三种后端的工程评估表 |
| **方向四：静态网站托管** | ✅ 确认 `s3compat/handler.go` 无 `?website` 路由 | 指出 `GET /s3/{bucket}/` 与 `GET /s3/{bucket}` 的 S3 语义歧义；估算 5.5 天工程量 |
| **方向五：混沌工程** | ✅ 确认 6 个韧性模式的可验证性 | 补充 SSE 加密轮换、Idempotency E2E、租户隔离等 5 个缺失的混沌实验目标；设计 `FaultInjector` 接口与安全模型 |

**优先级调整建议：** 将方向二的最简单子集（`events_dropped_total` 计数器）提前到"2 小时内可完成"的零风险观测改进；方向三的 local 增强提到 Phase 1；方向四（独立特性）可提前到与方向三并行。
