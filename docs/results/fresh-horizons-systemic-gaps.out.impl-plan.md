Tech Lead 分析文档已生成，保存至 `docs/tech-lead-analysis-fresh-horizons.md`（967 行）。

---

## 分析结论摘要

### 核心数据

| 维度 | 结果 |
|------|------|
| **任务总数** | 40 个（每个 1.5-5h，平均 ~2.8h） |
| **方向** | 5 个（Tag 引擎 9 个 / 风暴防护 7 个 / 分布式限流 9 个 / 网站托管 7 个 / 混沌工程 8 个） |
| **总预估工时** | 112.5h → 约 **28 人·天** |
| **团队** | 5 开发 + 1 QA + 1 Tech Lead |
| **工期** | **5 周开发 + 1 周缓冲 = 6 周** |

### 关键决策

1. **Quick Win（Day 1）** — `events_dropped_total` 指标单独提 PR，2 小时可上线。零风险观测改进，验证方向二的基础设施就绪。
2. **方向四（静态网站托管）提前**到与方向三并行（独立特性，无依赖阻塞）。
3. **Redis 依赖接受**（`go-redis/v9`），但必须有 LocalBackend 降级保护。Lua 脚本令牌桶为生产路径。
4. **级联检测用 TraceId 跳数**方案（已验证替代难以实现的回路检测），默认 `EVENT_CASCADE_MAX_HOPS=5`。
5. **混沌测试**的运行时注入 API 默认关闭（`CHAOS_ENABLED=false`），仅 staging 环境启用；admin scope + 二次确认头 + audit log 三保险。

### 最高风险项

| 风险 | 缓解 |
|------|------|
| Redis 引入外部依赖（R-01） | 连接失败自动降级 LocalBackend；配置 `REDIS_REQUIRED=false` |
| 级联检测误伤合法高频率事件链（R-02） | 默认 Hops=10；区分 write-back vs read-only；URL 白名单 |
| 混沌注入 API 安全（R-05） | 默认关闭；admin scope + 确认头 + 审计 + max duration=5min |

### 并行执行流水线

```
Team A: Tag 引擎 + 网站托管  (并行)
Team B: 风暴防护 + 分布式限流 (并行)
Team C: 混沌工程              (Phase 3 后启动)
```

三路并行，Phase 1-2 完全独立；Phase 3 开始交叉集成测试；Phase 4 统一收尾。
