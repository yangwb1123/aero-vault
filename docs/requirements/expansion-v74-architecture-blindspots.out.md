文件 `docs/requirements/expansion-v74-architecture-blindspots.md` 已存在且内容与您展示的一致（484 行，无实质性差异）。无需写入操作。

该文档列出的 5 个架构盲区方向已就位：

| # | 方向 | 既有覆盖验证 |
|---|------|-------------|
| 1 | **Storage Class 僵尸功能** — 全程持久化但零操作语义 | ✅ 零覆盖 |
| 2 | **Circuit Breaker 故障类型不分级** — `ErrNotFound` 等合法 404 触发错误熔断 | ✅ 零覆盖 |
| 3 | **PostgresTransport 无连接生命周期** — 断连后静默失效，无重连 | ✅ 零覆盖 |
| 4 | **Webhook 出站无幂等性** — 重复投递 + dead-letter 复用 succeeded 标记 | ✅ 零覆盖 |
| 5 | **健康检查端点流于表面** — `/healthz` 硬编码 200，后端状态不验证 | ✅ 零覆盖 |

如需要针对其中某个方向进行代码修复（实现 `isBackendFailure` 区分、PostgresTransport 重连循环、Webhook delivery ID、`Health()` 接口等），请说明优先级和具体方向。
