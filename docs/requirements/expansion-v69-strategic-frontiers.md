# Strategic Expansion Directions

> **Perspective:** 全代码库扫描后，以资深架构师/产品经理角度分析高价值扩展方向。
> **日期:** 2026-07-10 · **版本:** v69

---

## 目录

1. [分布式一致性元数据与多活架构](#1-分布式一致性元数据与多活架构)
2. [对象级加密与密钥管理平台](#2-对象级加密与密钥管理平台)
3. [生产级事件网格与 Webhook 基础设施](#3-生产级事件网格与-webhook-基础设施)
4. [智能存储分层与自动生命周期](#4-智能存储分层与自动生命周期)
5. [资源预算与 SLO 驱动的运营体系](#5-资源预算与-slo-驱动的运营体系)

---

## 1. 分布式一致性元数据与多活架构

### 现状

当前架构基于单一的 Repository 接口实现，SQLite/Postgres 均为单写入端模型。
Replication 模块是异构被动复制（primary → replica，job queue 驱动），仅复制 blob，
**不复制元数据**。Cluster singleton 基于数据库 lease 实现，只解决"同一 task 不重复执行"，
不解决数据平面扩展。

Event bus 的跨实例传输依赖 Postgres `LISTEN/NOTIFY`（`postgres_transport.go`），
无分区、无消费组、无重放能力。Idempotency-Key 机制仅在一个实例视图内有效。

### 为什么需要

| 场景 | 当前限制 |
|------|---------|
| 多地域部署 | 元数据存在单点，跨 region RTT 不可接受 |
| 零 downtime 升级 | 无法 rolling update，因为元数据 schema + 状态耦合 |
| 跨 AZ 容灾 | blob 可跨区复制，但 metadata 仍依赖单 DB |
| 租户级数据隔离 | 所有租户共享同一 DB schema，无法独立扩缩 |
| 写入吞吐水平扩展 | 单 Postgres 写入上限 ~5k-10k TPS（受磁盘/连接限制） |

### 建议方向

**元数据分片 + 最终一致性读取（Read-Repair / LWW）**

- 引入 `RegionID` 概念，Bucket 级别的元数据归属 region
- 跨 region 写入通过 `pg_logical` / `debezium` CDC 异步复制
- 读取路径增加 `read-repair`：检测 stale 分片并触发后台修复
- 可选的 Read-After-Write 一致性：写入返回 `replication_token`，读取阻塞等待 token 到达

**具体化：**

1. **Bucket 级 sharding key** — 将 `(tenant, bucket)` 作为元数据分片键。
   Write 路径只路由到主分片；Read 路径允许从副本读（stale read 容忍窗口可配）。
2. **Idempotency-Key 跨实例正确定义** — 当前实现假设存在中心化的
   `ClaimIdempotencyKey` 调用。在分布式环境下，idempotency-key 必须基于
   写入的 `(tenant, object_key, version)` 而非 `(tenant, key)`。
   引入 `IdempotencyKeyTracker` 组件，将 fingerprint 与 storage blob 绑定。
3. **元数据 CDC 流** — 将 `repository.Event` 扩展为包含完整元数据快照，
   通过统一的 CDC 通道分发（Kafka / Postgres logical replication），
   替代当前的 `INSERT INTO events` + `LISTEN/NOTIFY` 模式。

### 前置条件

- Postgres 逻辑复制能力（已有 `postgres_transport.go` 做铺垫）
- 引入 `raft/etcd` 或 `pg_advisory_lock` 之上的 leader 选举（已有 `leases.go`）
- Bucket config 增加 `ReplicationRegion` 字段（已有 `BucketConfig` 可扩展）

---

## 2. 对象级加密与密钥管理平台

### 现状

当前加密架构：

| 能力 | 状态 | 文件 |
|------|------|------|
| 存储后端 SSE（AES-256-GCM with single key） | ✅ 已实现 | `internal/storage/encrypt.go` |
| Key 轮换重包装（rewrap stale on start） | ✅ 已实现 | `internal/storage/rewrap.go` |
| KMS 集成（HTTP KMS provider） | ✅ 已实现 | `internal/storage/kms.go` |
| SSE envelope（per-object keys wrapped by primary） | ✅ 已实现 | `internal/storage/encrypt.go` |
| SSE-C（客户提供密钥） | ❌ 未实现 | — |
| SSE-S3（服务端托管，每对象独立 key） | ❌ 未实现 | — |
| 客户端加密 | ❌ 未实现 | — |
| 密钥生命周期管理（自动轮换、吊销） | ❌ 部分 | `rewrap.go` 只有启动时单次 |
| 密钥审计（谁在何时解密了哪个对象） | ❌ 未实现 | — |

### 为什么需要

- **合规要求**：HIPAA / SOC2 / PCI-DSS 要求每个加密操作可审计，
  且客户需能控制密钥（BYOK / SSE-C）
- **多租户数据隔离**：当前所有租户共享一个 storage backend key，
  存储层无法区分租户边界
- **密钥轮换自动化**：rewrap-on-start 只在进程启动时执行一次，
  生产环境需要定时轮换 + 旧密钥安全吊销机制
- **审计需求**：解密路径没有任何日志记录谁读了什么对象

### 建议方向

**分三层密钥架构：**

```
┌────────────────────────────────────────────┐
│               KMS / HSM                    │
│  (主密钥 MK — 永不离开 KMS 边界)           │
└──────────────┬─────────────────────────────┘
               ↓ 加密/解密
┌────────────────────────────────────────────┐
│            Key Encryption Key (KEK)        │
│  每个租户 1 个 KEK，存于 KMS key ring      │
│  轮换周期 90 天，版本化                     │
└──────────────┬─────────────────────────────┘
               ↓ 加密/解密
┌────────────────────────────────────────────┐
│            Data Encryption Key (DEK)       │
│  每个对象 1 个 DEK，存于 metadata 中        │
│  envelope 格式：KEK_wrap(DEK) + IV         │
└────────────────────────────────────────────┘
```

**具体实现项：**

1. **SSE-C 协议支持** — REST `/v1/files/*` 和 S3 `/s3/*` 路径识别
   `x-amz-server-side-encryption-customer-algorithm` 头，用户每请求携带密钥。
   密钥通过 HKDF 派生后用于 AES-GCM 加密，内存中及时清零。
   → 修改 `storage.Storage.Put` / `Get` 接口，增加可选的 `customerKey` 参数
   → Local 后端在加密层实现 SSE-C（`internal/storage/encrypt.go` 增加 `EncryptC`）

2. **租户级 KEK 管理** — `TenantRecord` 增加 `kek_id` 字段，
   指向 KMS 中该租户的 Key Encryption Key。
   Admin API 增加 `POST /v1/admin/tenants/{id}/rotate-key`。
   后台 worker 自动检测过期 KEK 版本并 rewrap affected DEKs
   （类似已有的 `rewrap.go` 但定时触发、按租户限流）。

3. **解密审计日志** — `FileService.Get` / `GetRange` / `GetVersion` 路径，
   调用 `storage.Get` 之前记录 `AuditEntry{Actor, Action:"decrypt", Target: key}`。
   新增配置项 `AUDIT_DECRYPT_OPS=true` 控制是否启用。

4. **密钥吊销快速路径** — 当租户 KEK 被吊销时，立即阻止所有使用该 KEK
   版本包裹的 DEK 的解密操作。引入 `RevokedKEK` 缓存（带 TTL），
   解密前检查 DEK 的 key ID 是否在吊销列表中。

### 依赖

- 已有的 `storage/kms.go`（HTTP KMS provider）可直接扩为 KEK 操作
- 已有的 `internal/storage/encrypt.go` 的 envelope 格式需版本化（`enc_v2`）
- 已有 `AuditEntry` 表（`migrations/0016_audit_log`）

---

## 3. 生产级事件网格与 Webhook 基础设施

### 现状

| 组件 | 当前能力 | 限制 |
|------|---------|------|
| Event Bus | 内存 channel + 可选 Postgres LISTEN | 无分区、无回压信号、无死信队列 |
| Event 持久化 | `INSERT INTO events` 表，`NextUnconsumedEvents` 轮询 | 无 TTL、无消费进度持久化 |
| Webhook 投递 | HTTP POST + HMAC-SHA256 签名，`webhook_failures` 表重试 | 单 URL、无速率限制、无退避策略、无事件过滤 |
| SSE 流 | 租户过滤、Last-Event-ID 重放 | 无动态过滤器、无通配符订阅 |

### 为什么需要

- **可观察性失效**：当前在 subscriber 队列满时直接 drop 事件
  （`bus.go` 的 `default:` 分支），生产环境中静默丢失事件无法排查
- **多协议事件消费**：AWS S3 的事件模型支持同时投递 SQS、SNS、Lambda，
  当前系统只能配一个 webhook URL
- **租户维度的 webhook 差异化**：不同租户需要不同目标 URL 和不同签名密钥
- **事件风暴保护**：大量 `object.created` 事件可能在几秒内淹没 subscriber
  （例如批量导入 10 万个文件），当前没有速率限制或节流
- **事件重放需要全表扫描**：`NextUnconsumedEvents(limit)` 没有 checkpoint，
  重启后必须从旧事件中过滤，随着事件表增长线性变慢

### 建议方向

**引入事件分区 + 可插拔传输层**

1. **分区事件表** — `events` 表增加 `partition` 列（0-63）。
   写入时 `partition = hash(tenant+bucket) % 64`。每个 consumer 只轮询
   其负责的分区。SQL 增加 `WHERE partition = $1 AND id > $2 LIMIT $3`，
   比当前的全表 `ORDER BY id ASC LIMIT $5` 的轮询效率高两个数量级。

2. **事件过滤 DSL** — webhook 配置支持事件类型白名单 + 前缀过滤器：
   ```json
   {
     "url": "https://hooks.example.com/events",
     "events": ["s3:ObjectCreated:*"],
     "filter": {
       "prefix": "uploads/",
       "suffix": ".pdf"
     },
     "retry_policy": {
       "max_attempts": 5,
       "backoff_base_seconds": 2,
       "backoff_max_seconds": 300
     }
   }
   ```
   → 当前 `NotificationRule` 已有 `Events` + `FilterKey` 字段，可直接扩展
   → Webhook 模块增加 `shouldDeliver(e) bool` 过滤函数

3. **死信队列 + 事件重放 API** — 当 `webhook_failures` 超过 `max_attempts` 后，
   事件自动转入 `dead_letter` 表。Admin API 提供：
   - `GET /v1/admin/dead-letters` — 查看卡住的事件
   - `POST /v1/admin/dead-letters/{id}/replay` — 重放特定事件
   - `POST /v1/admin/dead-letters/replay-all` — 批量重放

4. **Subscriber 背压信号** — 当前 `bus.go` 在 channel 满时直接 drop。
   改为使用 **租户级有界优先级队列**：当 subscriber pending 超过阈值时，
   Bus 停止向其推送新事件，返回 `ErrSubscriberBackpressured`，
   上游（FileService）记录 metric 并降级为仅持久化（不广播）。
   → `telemetry.IncEventDropped` 升级为区分 `reason={backpressure,slow_consumer,queue_full}`

5. **租户级 webhook 配置** — `BucketConfig.NotificationRules` 扩展为
   支持多 URL、多密钥。增加 `POST /v1/buckets/{bucket}/notification` 的测试覆盖面
   （当前 handler 定义了路由但测试可能不足）。

### 与现有基础设施的兼容

- `events/bus.go` 的 `WithTransport` 接口是天然的扩展点
- `events/postgres_transport.go` 的 channel 模式可复用于分区传输
- `NotificationRule` 结构体已预留 `QueueARN`、`TopicARN`、`LambdaARN` 字段

---

## 4. 智能存储分层与自动生命周期

### 现状

| 能力 | 状态 |
|------|------|
| StorageClass 元数据记录 | ✅ `Object.StorageClass` 字段，`x-amz-storage-class` 透传 |
| StorageClass 统计 | ✅ `StorageClassCounts` repository 方法，Prometheus gauge 注册 |
| Bucket lifecycle 过期删除 | ✅ `ExpireAfterDays` + `expireAction` (soft/hard delete) |
| **存储类间自动迁移** | ❌ 未实现 |
| **归档对象恢复（RestoreObject）** | ❌ 未实现（S3 handler 有 `?restore` 路由但行为为空） |
| **基于访问频率的智能分层** | ❌ 未实现 |
| **最小存储周期计费（Minimum storage duration）** | ❌ 未实现 |

### 为什么需要

- **成本竞争力**：AWS S3 的核心竞争力之一就是 Intelligent-Tiering 自动降冷。
  如果 aero-vault 只有 "STANDARD" 一个热层，在存储量超过 50TB 的场景下
  客户成本将远高于 S3/Backblaze。
- **归档合规**：金融/医疗客户需要将数据在热层存放 30 天后自动迁至冷层，
  保留 7 年后再删除。当前只有"到期删除"一条路径。
- **恢复 SLA**：归档对象需要从冷存储恢复到热层才能读取，
  需要异步恢复通知（`POST /v1/files/{key}/restore` + SSE 事件）。

### 建议方向

**四层存储模型**

| 层 | 名称 | 存储后端 | 延迟 | 最小计费 | 典型用途 |
|----|------|---------|------|---------|---------|
| 1 | STANDARD | 主 storage backend | 即时 | 无 | 活跃数据 |
| 2 | STANDARD_IA | 主 backend + IA 标记 | 即时 | 30 天 | 低频访问 |
| 3 | GLACIER | 独立冷存储 backend | 1-12h 恢复 | 90 天 | 归档 |
| 4 | DEEP_ARCHIVE | 最廉价 backend | 12-48h 恢复 | 180 天 | 合规保留 |

**实现路径：**

1. **StorageClass 语义扩展** — `storage.Storage` 接口增加
   `TransitionObject(ctx, key, fromClass, toClass string) error` 方法。
   Local 后端实现为原地保留（metadata-only）；S3 后端通过 `CopyObject`
   修改 storage class；OSS/COS 通过各自的 tiering API。
   **新增配置**：`STORAGE_IA_ROOT` / `STORAGE_GLACIER_ENDPOINT` 等。

2. **Lifecycle 规则升级** — 当前 `BucketConfig.ExpireAfterDays` 仅支持
   "到期删除"。扩展为 rule 列表（与 S3 Lifecycle Configuration 兼容）：
   ```json
   {
     "rules": [
       {"id": "archive-30d", "enabled": true,
        "filter": {"prefix": "logs/"},
        "transitions": [
          {"days": 30, "storage_class": "STANDARD_IA"},
          {"days": 90, "storage_class": "GLACIER"}
        ],
        "expiration": {"days": 365}
       }
     ]
   }
   ```
   → 使用 `LifecycleJob`（`internal/reconcile/lifecycle.go`）作为执行引擎，
   已有 `sweepExpired` 定时循环可扩展为 `applyTransitions`。

3. **归档恢复流程** — S3 handler 已有 `?restore` 路由分派（`extra.go`:
   `h.restoreObject`），但实际未实现。
   - `POST /s3/{bucket}/{key}?restore` → 创建 `JobRestore` 后台任务
   - 任务执行：从冷存储读回 → 写回热层 → 更新 metadata
   - 完成后通过 event bus 发送 `object.restored` 事件
   - SSE 订阅者可收到恢复进度通知

4. **存储智能放置** — 可选组件：基于写入频率统计的自动分层器。
   `Reconcile` 模块增加 `TierAnalyzer`：统计每对象每小时的访问次数，
   连续 30 天 < 1 次/天的对象自动转为 STANDARD_IA。

### 涉及改动

- `service/file.go` 的 `StorageClassOrDefault` → 增加 `ValidateStorageClass`
- `storage.Storage` 接口增加 `Transition` 方法（不破坏现有后端）
- Lifecycle 迁移表：`lifecycle_rules(tenant, bucket, rules_json)`
- `RestoreObject` 在 `file_features.go` 已有桩，需填充具体逻辑

---

## 5. 资源预算与 SLO 驱动的运营体系

### 现状

| 运营能力 | 状态 | 局限 |
|---------|------|------|
| 租户配额（bytes + objects） | ✅ 已实现 | 无 `max_ingress` / `max_egress` / `max_ops_per_hour` |
| AI 日费用预算 | ✅ `AI_TENANT_DAILY_BUDGET_USD` | 仅作用于 Chat/Agent，不覆盖搜索的 embed 成本 |
| 速率限制（RPS per tenant） | ✅ token bucket | 无优先级区分（管理 API vs 用户 API 共享一个桶） |
| 并发限制（per-tenant inflight） | ✅ `PerTenantConcurrencyLimiter` | 无排队队列，直接 429 |
| Prometheus 指标 | ✅ 15 instruments | 无 SLO 错误预算、无延迟 SLO、无可用性 SLO |
| 告警规则 | ✅ 3 条 | 无多窗口燃烧速率告警、无依赖告警抑制 |
| 用量报表 / 计费系统 | ❌ 未实现 | — |

### 为什么需要

- **可预测性**：没有 `max_egress` 配额，一个租户可以刷出天价账单
  （表现为云存储出口流量费）
- **公平调度**：没有请求优先级，"管理 API 被用户 API 饿死"是真实场景
- **可审计性**：当前 `ai_usage` 表记录了 token 和 cost，但**没有记录搜索的 embed cost**
  （embedding API 调用也有成本）
- **运营 SLO**：没有量化指标，无法回答"上周搜索 P95 延迟是否达标"。
  SLO/SLI 是正式 SLA 的基础

### 建议方向

**资源预算四维框架**

```
              ┌── 容量预算 ──┐
              │ max_storage  │
              │ max_objects  │
              └──────┬───────┘
                     │
┌── 流量预算 ──┐    ┌── 性能预算 ──┐
│ max_ingress  │    │ latency SLO  │
│ max_egress   │◄──►│ throughput   │
│ max_ops/sec  │    │ availability │
└──────┬───────┘    └──────┬───────┘
       │                   │
       └──────┬───────┘
       ┌── 成本预算 ──┐
       │ daily_budget │
       │ monthly_cap  │
       └──────────────┘
```

**具体实现：**

1. **多维度租户配额** — `TenantQuota` 增加字段：
   - `MaxIngressBytes` / `MaxEgressBytes`
   - `MaxOpsPerHour`（操作频率，按 HTTP method bucket 累计）
   - `MinStorageClass`（租户级别的最低存储层限制）
   → 写入/读取路径增加 `preflightQuota` 检查（类似已有 `preflightQuota`）

2. **AI 全链路成本核算** — 当前 `Search.Query` 只记录 usage 行但不记录
   embed 调用成本。方案：
   - `Embedder` 接口增加 `CostPerToken` 方法（`HashEmbedder` 返回 0，
     `HTTPEmbedder` 从 provider 映射读取）
   - `Search.Query` 调用后通过 `telemetry.RecordAIUsage` 上报 embed cost
   - `SumAICostMicros` 聚合涵盖搜索 + chat + agent 全链路

3. **优先级队列** — `RateLimiter` 增加优先级通道：
   ```
   priority-0 (admin ops)     → 从不被限
   priority-1 (CRUD reads)    → 共享 budget 的 60%
   priority-2 (AI endpoints)  → 独立 AI 限流
   priority-3 (batch ops)     → 最低优先级，可被前二者抢占
   ```
   → 需要 `middleware/ratelimit.go` 扩展为多桶加权分配

4. **SLO 框架** — 基于已有的 Prometheus 指标，通过 `prometheus.Rules` 实现：
   ```yaml
   # search P99 latency < 2s over 5min window
   - alert: SearchSLOWarning
     expr: histogram_quantile(0.99, rate(http_request_duration_seconds_bucket{handler="search"}[5m])) > 2
     for: 5m
   # error budget burn rate > 2 (10% in 30d → 1% per 3d)
   - alert: ErrorBudgetBurnCritical
     expr: (1 - (sum(rate(http_requests_total{status=~"5.."}[1h])) / sum(rate(http_requests_total[1h])))) < 0.999
     for: 1h
   ```
   → 在 `deploy/prometheus/` 目录增加 `slo-rules.yml`

5. **用量计费报表** — Admin API 增加：
   - `GET /v1/admin/tenants/{id}/usage?from=&to=` 返回结构化用量
   - `GET /v1/admin/tenants/{id}/cost?from=&to=` 返回估算成本
   - 后端通过 `repository.SumAICostMicros` + 新增 `SumEgressBytes` 聚合

### 涉及改动

- `repository.TenantQuota` 扩展（`migrations/0025_quota_extended`）
- `middleware/ratelimit.go` 的 `Allow(tenant, priority) bool` 方法
- `Search.Query` 中 embedder cost 记录（`internal/ai/search.go` ~95-105 行）
- `deploy/prometheus/` 目录下告警规则文件
- `admin.go` 的 `Usage` handler 扩展

---

## 总结：优先级矩阵

| # | 方向 | 价值 | 复杂度 | 风险 | 建议先决条件 |
|---|------|------|--------|------|-------------|
| 1 | 分布式一致性元数据 | 🔴 架构级 | L | High | Postgres 逻辑复制 + lease 已有 |
| 2 | 对象级加密与 KMS | 🔴 合规/安全 | M | Med | KMS provider + encrypt.go 已有 |
| 3 | 事件网格增强 | 🟠 运营/可靠性 | M | Low | `bus.go` + `NotificationRule` 可扩展 |
| 4 | 智能存储分层 | 🟠 成本竞争力 | L | Med | `StorageClass` 字段 + `LifecycleJob` 已有 |
| 5 | SLO/预算运营体系 | 🟠 可观测性/计费 | M | Low | Prometheus + metrics.go 已有 |

**建议执行顺序：** 3 → 5 → 2 → 4 → 1

- **第 0 阶段（现有地基）：** 事件基础设施增强 + SLO 框架（低风险、高 ROI）
- **第 1 阶段（安全与合规）：** 对象级加密（必要的中期投资）
- **第 2 阶段（成本竞争）：** 存储分层 + 生命周期自动化
- **第 3 阶段（规模架构）：** 分布式元数据（高风险，需要前面积累的运营成熟度）
