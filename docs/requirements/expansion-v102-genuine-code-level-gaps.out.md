文档已成功保存。

**文件：** `/home/u1/aero-vault/docs/requirements/expansion-v121-replication-integrity-sse-resilience-cross-protocol-cli-event-timing.md`
**大小：** 22,023 字节

该文档涵盖了 5 个经过严格去重验证的高价值扩展方向：

| 方向 | 类型 | 优先级 | 核心代码锚点 |
|------|------|--------|------------|
| **1. 复制完整性**（全生命周期复制） | 数据一致性 | **P0** | `replication.go:78` — `EventCreated` 单事件过滤；`replication.go:85` — dedupe key 阻挡 update 复制 |
| **2. SSE 流韧性**（持久游标 + SDK 退避） | 可靠性 | **P1** | `bus.go:30` — 硬编码 64 深缓冲；`sse.go:44` — replayMissed 用全局 unconsumed 而非客户端游标 |
| **3. 跨协议语义一致性**（DELETE/RENAME/条件写） | 产品一致性 | **P1** | `handler.go:258` — S3 硬删除 vs `handler.go:310` — REST 软删除；`dav.go:251` — WebDAV Rename 在其他协议缺失 |
| **4. CLI 工程成熟度**（HTTP 状态码 Bug + `--json`） | DX | **P2** | `cli_test.go:1419-1430` — 明确记录的 6 个 BUG 注释 |
| **5. 事件驱动时序缺口**（创建后快速删除 → ObjectID 悬空） | 数据一致性 | **P2** | `replication.go:107` / `worker.go:41` / `indexer.go` — `GetObjectByID` 返回 `ErrNotFound` 被等同重试 |
