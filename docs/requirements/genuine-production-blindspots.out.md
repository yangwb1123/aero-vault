---

# 验证报告：高价值扩展方向分析

## 总体评估

**代码锚点验证：全部 4 个方向的代码层声称已通过实际代码核查确认。**
**去重新颖性验证：2/4 方向有实质性既存覆盖，2/4 方向基本新颖。**

| # | 方向 | 代码证据 | 去重新颖性 | 判定 |
|---|------|---------|-----------|------|
| ✅ 1 | 异步管线请求追踪断裂 | ✅ 全部验证通过 | ✅ **新颖** — v50 覆盖 OTel span 传播（不同概念），RequestID 在下游 Worker 被丢弃作为独立方向未被覆盖 | **有效** |
| ✅ 2 | 元数据灾难恢复缺失 | ✅ 全部验证通过 | ✅ **新颖** — 此前仅有关键词提及和概念表行，无代码锚点驱动的实现路径分析 | **有效** |
| ❌ 3 | 数据面访问审计缺失 | ✅ 全部验证通过 | ❌ **既存覆盖** — v136 方向四（~250 行）、v67 方向二、v72 方向四均以相同深度和架构方案覆盖此方向 | **重复** |
| ❌ 4 | 写时存储类路由缺失 | ✅ 全部验证通过 | ❌ **既存覆盖** — v92 方向一（~200 行，题目"多后端存储编排引擎"）覆盖完全相同的代码断点和架构方案，且包含更细致的跨后端List分析和边界情况 | **重复** |

---

## 逐方向详细验证

### 方向一：异步管线请求追踪断裂 ✅

#### 代码级验证结果

| 声称 | 实测结果 |
|------|---------|
| `repository.Event.RequestID` 字段存在 | ✅ `internal/repository/repository.go` line 148, 176, 205 — 三个 `Event` 相关结构体均含 `RequestID string` |
| `emit` 方法填充 `RequestID` | ✅ `internal/service/file.go:239` — `RequestID: middleware.RequestIDFrom(ctx)` |
| Indexer 忽略 RequestID | ✅ `internal/ai/indexer.go` — 全文零引用 `RequestID`/`request_id`/`e.RequestID` |
| Replication Worker 忽略 RequestID | ✅ `internal/replication/replication.go` — 全文零引用 |
| Antivirus Worker 忽略 RequestID | ✅ `internal/antivirus/worker.go` — 全文零引用 |
| Webhook 是唯一消费者 | ✅ `internal/events/webhook.go:101` — `"request_id": e.RequestID` 为唯一使用处 |
| `IndexObjectByID(ctx, objectID)` 签名 | ✅ `internal/ai/indexer.go:251` — `func (ix *Indexer) IndexObjectByID(ctx context.Context, objectID int64) error` |
| `ReplicateObjectByID(ctx, objectID)` 签名 | ✅ `internal/replication/replication.go:97` — 确认 |
| `ScanObjectByID(ctx, objectID)` 签名 | ✅ `internal/antivirus/worker.go:98` — 确认 |

#### 验证检查点核查

- `object_events` 表 `request_id` 字段：✅ 存在于 `sql_events.go` schema（`INSERT INTO object_events (...) request_id ...`）
- `NextUnconsumedEvents` 返回历史事件含 `request_id`：✅ `sql_events.go:36` 返回 `Event` 结构体含 `RequestID`

#### 新颖性验证

`expansion-v50-genuine-unexplored-frontiers.md`（方向二）覆盖的是"分布式链路追踪与异步上下文传播"——聚焦 OpenTelemetry `traceparent` span 传播，与 RequestID 是不同的概念和机制。本方向首次以"Event.RequestID 被所有异步 Worker 丢弃"作为独立断裂点分析。**新颖性成立。**

---

### 方向二：元数据灾难恢复缺失 ✅

#### 代码级验证结果

| 声称 | 实测结果 |
|------|---------|
| `Object` 中 DB-only 字段（`ID`, `VersionID`, `Tags`, `LockedUntil`） | ✅ `internal/repository/repository.go:21-40` — 全部确认 |
| `storageKey` 构造规则 | ✅ `internal/service/file.go:198` — `path.Join(tenant, bucket, key)` |
| `Storage.List` 存在 | ✅ `internal/storage/storage.go:119` 接口定义；所有后端实现（`local_list.go:20`、`s3.go:158`、`cos.go:111`、`oss.go:103`） |
| 无元数据重建工具 | ✅ 未发现任何 `recover`、`rebuild`、`reconstruct` 相关 CLI 子命令或 service 方法 |
| `VersionID` 仅存于 DB | ✅ `repository.go:26` — `VersionID string`，存储 `@v<id>` 后缀无法反向解析 |
| `Tags`/`LockedUntil` 仅存于 DB | ✅ `repository.go:33` `Tags map[string]string`、`repository.go:38` `LockedUntil *time.Time` |

#### 验证检查点核查

- `Storage.List(prefix, marker, limit)` 返回所有 blob：✅ 但仅返回 key/size/ETag/LastModified，无 tag/version/ACL/lock
- `@v<id>` 后缀反向解析为 version/is_latest：❌ 后缀包含 versionID 字符串但无法判断哪个版本是"current"
- 软删除 blob vs 活跃 blob 区分：❌ 软删除对象在存储层与活跃对象完全相同

#### 新颖性验证

- `expansion-v44-systemic-architect-gaps.md` 在关键词频率表中提到"生产灾备"但只有一行概念列举
- `expansion-v51-storage-semantics-and-operational-hardening.md` 提出 `local_storage.orphan_blob_recovered_total` 遥测指标，但聚焦的是孤儿的可观测性而非元数据重建
- `expansion-v35-fresh-horizons.md` 提出"备份与灾难恢复即服务框架"为概念方向，零代码锚点

**本方向首次从 storageKey 构造规则、List 能力、Object 结构体 DB-only 字段三个维度驱动元数据重建方案（L1/L2/L3 分级），还覆盖了加密上下文、Object Lock、uploads 表不可重建等边界情况。新颖性成立。**

---

### 方向三：数据面访问审计缺失 ❌ — 既存覆盖

#### 代码级验证结果

| 声称 | 实测结果 |
|------|---------|
| `audit_log` 仅覆盖管理操作 | ✅ `RecordAudit` 仅从 `internal/api/rest/admin.go` 调用；全部调用点（`h.audit`）：quota.set、budget.set、key.add、key.revoke、tenant.create、tenant.delete、tenant.status |
| `EventAccessed` 在 Indexer 中是 no-op | ✅ `internal/ai/indexer.go:181-182` — `case repository.EventAccessed: // no-op (used only for audit)` |
| `emit` 不包含请求者身份 | ✅ `internal/service/file.go:231-246` — `RequestID` 是唯一身份标识字段 |
| 无 `RequesterFrom`/`ClientIPFrom`/`AuthMethodFrom` | ✅ 确认全局不存在 |
| GET 路径发出 `EventAccessed` | ✅ `internal/service/file_crud.go:314` — `s.emit(ctx, obj, repository.EventAccessed)` |

#### 验证检查点核查

- `GET /v1/files/some-file` 后 `object_events` 表 accessed 行含身份信息：❌ 仅含 `RequestID`，无 `RequesterID`/`RequesterIP`/`AuthMethod`
- `audit_log` 含 GET 操作记录：❌ 仅 admin 操作
- `EventAccessed` 消费者存在非 no-op 处理逻辑：❌ 唯一消费者是 indexer，且是 no-op

#### 新颖性验证 ❌

以下文档已深度覆盖此方向：

| 文档 | 覆盖度 | 核心内容 |
|------|-------|---------|
| **v136** 方向四（~250行） | **完整** | `ObjectAccessEvent` 模型（`{actor, tenant, bucket, key, action, ip, user_agent, request_id, timestamp}`）；异步批量写入器 `ObjectAuditWriter`；查询 API `GET /v1/admin/audit/objects`；自动清理 `AUDIT_OBJECT_TTL_DAYS`；注入点：`FileService.Get` 旁加 `s.recordAccess` |
| **v67** 方向二 | **完整** | 相同代码证据：`audit.go` 仅 admin 操作、`file_crud.go` 无审计调用；相同架构方案 |
| **v72** 方向四 | **完整** | 与 v136 相同内容 |
| **v85** 方向五 | **部分** | "访问治理三系统隔离"包含数据面审计作为治理组成部分 |

本方向"EventAccessed 是 no-op + 缺少请求者身份"的分析维度是 v136/v67 分析的子集，且建议方案（`RequesterID`/`RequesterIP`/`AuthMethod` Event 扩展 + 持久化消费者）与 v136 的 `ObjectAccessEvent` + `ObjectAuditWriter` 方案本质相同。

**结论：既存覆盖。本方向不符合"未被独立深度覆盖"的筛选条件。**

---

### 方向四：写时存储类路由缺失 ❌ — 既存覆盖

#### 代码级验证结果

| 声称 | 实测结果 |
|------|---------|
| `Object.StorageClass` 字段 | ✅ `internal/repository/repository.go:34` — `StorageClass string` |
| `DefaultStorageClass`/`StorageClassOrDefault` | ✅ `internal/service/file.go:19-20` `DefaultStorageClass = "STANDARD"`、`line 190-193` `StorageClassOrDefault` |
| `FileService.store` 为单后端 | ✅ `internal/service/file.go:82` — `store storage.Storage`（单一接口引用） |
| `buildStorage` 返回单后端 | ✅ `cmd/server/main.go:402-450` — `buildStorageFrom` 返回唯一实例 |
| 工厂支持多后端类型 | ✅ `internal/storage/factory.go` — local/s3/oss/cos |
| `buildPutObject` 记录 StorageClass | ✅ `internal/service/file_crud.go:232-250` — `StorageClass: StorageClassOrDefault(opts.StorageClass)` |
| `x-amz-storage-class` header 被解析 | ✅ REST handler 和 S3 compat handler 均解析此 header |

#### 验证检查点核查

- PUT `x-amz-storage-class: STANDARD_IA` 后 `storage_class` 字段：✅ `STANDARD_IA`
- 但 blob 与 STANDARD 对象在同一后端：✅ 文件系统 `/var/objects/` 下无区分
- S3 后端是否使用 S3 的 StorageClass：❌ S3 SDK 需要额外设置 `StorageClass` 参数，当前未设置

#### 新颖性验证 ❌

以下文档已深度覆盖此方向：

| 文档 | 覆盖度 | 核心内容 |
|------|-------|---------|
| **v92** 方向一（~200行） | **完整** | 相同标题语义（"多后端存储编排引擎"）；相同代码证据（`Storage` 单接口、`Object.StorageClass` 写入后永不用于路由、`NewFromConfig` switch-case）；相同写入路径扩展点（`TieredRouter` 在 FileService 和 Storage 之间插入）；相同边界情况（后端不可用降级、跨后端 List 聚合、迁移 Job） |
| **v138** 方向三 | **部分** | "策略-动作物化鸿沟"中 StorageClass 作为 4 个配置完备但执行缺失的子系统之一分析 |
| **v15**、**v42**、**v56** | **浅层** | 概念性提及 StorageClass→后端映射 |

本方向的代码证据、技术方案（`storageRouter`/`map[string]storage.Storage`）、权衡分析（读路径后端记录、List 聚合）、边界情况（GLACIER 读取延迟、后端降级、CopyObject 迁移）与 v92 v92 方向一几乎完全相同，且 v92 还额外覆盖了配置面设计（`STORAGE_BACKENDS` 复数配置）和方案 B（能力契约路由），比本方向更全面。

**结论：既存覆盖。本方向不符合"未被独立深度覆盖"的筛选条件。**

---

## 精炼后的建议

| 状态 | 方向 | 优先级 | 推理 |
|------|------|--------|------|
| ✅ **保留** | **方向一：异步管线请求追踪断裂** | P1 | 代码证据确凿，新颖性成立，修复成本低（每个 Worker 循环中 3-5 行注入 + Job 结构体扩展），影响面广（运维可观测性、SLA 衡量、审计归因） |
| ✅ **保留** | **方向二：元数据灾难恢复缺失** | P1 | 代码证据确凿，新颖性成立，影响严重（单文件部署 SQLite 损坏即全部元数据丢失），L1 方案（`recover metadata --scan` CLI）实现成本低且有渐进路径（L1→L2→L3） |
| ❌ **降级** | **方向三：数据面访问审计缺失** | P2 → 注释 | v136/v67/v72 已完整覆盖。建议引用 v136 方向四的 `ObjectAccessEvent` + `ObjectAuditWriter` 方案作为既有设计，本方向的独特贡献（EventAccessed no-op + 身份缺失）可作为 v136 方案的触发论证，而非独立方向 |
| ❌ **降级** | **方向四：写时存储类路由缺失** | P2 → 注释 | v92 方向一已完整覆盖（包括方案 A/B 分层）。建议引用 v92 的 `TieredRouter` 方案 + 本方向的 GLACIER 读取延迟和预签名 URL 跨后端处理作为 v92 方案的补充边界分析，而非独立方向 |

### 修正后的去重矩阵

将附录中的验证检查点更新为引用既有文档的成熟方案：

| 方向 | 既有覆盖 | 本方向的独特价值 |
|------|---------|-----------------|
| 异步管线追踪 | **新颖** | 唯一完整分析 |
| 元数据灾难恢复 | **新颖** | 唯一完整分析 |
| 数据面审计 | v136**✅**（方向四）、v67**✅**（方向二）、v72**✅**（方向四） | EventAccessed no-op 的具体锚点可作为补充，但非独立方向 |
| 写时存储类路由 | v92**✅**（方向一）、v138**✅**（方向三，部分） | GLACIER 读取延迟 × 预签名 URL 场景的边界分析可作为 v92 的扩展，但非独立方向 |

### 建议的下一步动作

1. **方向一实现优先级最高** — 修复成本最低（每个 Worker 增加 `ctxWithReqID := context.WithValue(ctx, requestIDKey, e.RequestID)` + Job 结构体增加 `RequestID` 字段），影响面横跨 Indexer/Replication/Antivirus/JobPool
2. **方向二可接续实现** — 先实现 L1 `recover metadata --scan` CLI（复用 `Storage.List` + `storageKey` 反推），复杂度评估低
3. **方向三建议关闭** — 引用 v136 方向四作为现成设计文档，无必要再产生独立分析
4. **方向四建议关闭** — 引用 v92 方向一作为现成设计文档，本分析的 GLACIER 边界场景注释到 v92 文档中即可
