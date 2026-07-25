# AeroVault 高价值扩展方向 v52 — 产品化深度与平台级能力缺口

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部子包，~55K `.go` + 三套 SDK + `deploy/*` + 全部 24 对迁移文件 + 已有 51 份 `docs/requirements/expansion-*.md` 分析文档 + `ROADMAP.md` + `CHANGELOG.md` + `TODO.md` + `extensions*.md` + `docs/architecture.md` + `docs/configuration.md`）
>
> **分析视角：** 资深架构师 / 产品经理 — 在 51 期 expansion 分析（255+ 方向，~600,000+ 字分析文本）基础上，寻找 **51 轮穷举后依然未被触及** 的产品化深度与平台级能力缺口
>
> **去重方法：** 对 `docs/requirements/` 下全部 51 份既有分析文档 + `ROADMAP.md` + `CHANGELOG.md` + `TODO.md` + `extensions*.md` 进行穷尽式关键词验证。每个方向在既有文档中 **零实质性独立架构分析**（表格一行、举例提及、单一子点均不构成实质性分析）。
>
> **关键词回溯确认：** 对既有 51 份文档全文检索 "存储分级/存储分层/storage class transition/tiering"、"计费/账单/billing/metering"、"备份/恢复/backup/restore/PITR"、"工作流/审批/workflow/approval/governance"、"运维面板/operator console/management UI/administrator panel"，**无一出现作为独立方向**。
>
> **分析日期：** 2026-07-10

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心矛盾 | 51 期覆盖 |
|---|------|------|--------|---------|-----------|
| **1** | **多级存储分层引擎与自动化生命周期迁移（Storage Tiering Engine）** | 产品能力/S3 兼容性 | **P1** — 当前存储类（`storage_class`）仅作元数据标注，不驱动任何行为差异；S3 生态中 `STANDARD → STANDARD_IA → GLACIER → DEEP_ARCHIVE` 的自动分层迁移完全缺失，存储成本无法随数据冷却而优化 | ❌ **零实质性分析**（v9 表格一行路过提及"lifecycle transition"概念但无任何架构分析；v49 方向一的过期 multipart 清理与 v49 方向二的 retention 模式均聚焦数据删除语义，未涉及多级分层迁移）|
| **2** | **用量计量与平台级计费底座（Usage Metering & Billing Platform）** | 平台经济/商业模式 | **P2→P1（面向 SaaS 部署）** — 已实现 AI 成本跟踪与租户配额，但缺乏存储（GB-月）、出口流量（egress/GB）、API 请求次数的计量数据模型与计费底座；无法支撑按量计费、分级定价、预付费套餐等 SaaS 商业模式 | ❌ **零实质性分析**（v47 方向一覆盖 AI 费用追踪但仅聚焦 cost control，未扩展为全维计费平台；v50 方向四覆盖"细粒度配额"但聚焦资源管理 gating 而非经济计量）|
| **3** | **对象级备份与时间点恢复（Point-in-Time Backup & Recovery）** | 数据安全/灾备 | **P1** — 当前 `snapshot` 工具仅支持 SQLite + local FS 的全量冷备，无增量备份、无桶级 PITR、无备份验证、无恢复编排；跨区域复制不等于可恢复性——没有 RPO/RTO 保证的备份策略 | ❌ **零实质性分析**（v9/v21/v50 覆盖多区域 active-active 复制但聚焦同步架构，非备份与 PITR；v38/v46 覆盖优雅关闭与灾备方向但仅提及进程级 shutdown 与文件级保护，未涉及系统级备份与恢复编排）|
| **4** | **运维面板与管理控制台（Operator Console / Admin Dashboard）** | 运维体验/产品成熟度 | **P2** — 已有 Web UI（面向终端用户的搜索/聊天/拖拽上传）与 `/metrics` 端点（面向 Prometheus），但缺少供运维人员使用的管理面板：系统健康总览、后台任务监控、事件流检视、租户用量仪表盘、配置验证与运行时编辑 | ❌ **零实质性分析**（v11/v23 覆盖 tracing 与可观测性但聚焦 metrics 与 span，非运维面板；v38 覆盖优雅关闭但为进程级分析；v50 方向二覆盖分布式链路追踪但为 instrumentation 层，非运维界面）|
| **5** | **对象生命周期工作流与审批治理引擎（Object Lifecycle Workflow & Approval Engine）** | 产品差异化/合规 | **P2** — 当前系统可存储、索引、搜索、删除对象，但缺少企业合规所需的人机交互：危险操作的预审批（批量删除/永久删除/锁释放）、基于标签的自动化规则引擎、合规保留的时间线审计、多人审批工作流 | ❌ **零实质性分析**（v7/v12/v20 路过提及"工作流"概念但从未作为独立方向展开架构分析；v49 方向二的 object lock modes 聚焦 S3 协议语义而非企业治理工作流）|

---

## 方向一：多级存储分层引擎与自动化生命周期迁移（Storage Tiering Engine）

### 现状

当前系统中 `storage_class` 字段存在于 `repository.Object`：

```go
// internal/repository/repository.go:80
StorageClass string // e.g. STANDARD, STANDARD_IA, GLACIER; "" = STANDARD
```

它由客户端在 PUT 时通过 `x-amz-storage-class` 头指定，存储为元数据标签（`internal/api/s3compat/handler.go:645`），在响应中回显。**但它不驱动任何行为差异**——GET 相同、计费相同、副本数相同、存储后端相同。

桶级生命周期目前仅支持最终过期删除（`ExpireAfterDays` + `ExpireAction`）：

```go
// internal/repository/repository.go:86
ExpireAfterDays int
ExpireAction    string // "soft_delete" | "hard_delete"
```

```go
// internal/reconcile/lifecycle.go:95-104
func (l *LifecycleJob) handleExpiredObject(ctx context.Context, obj repository.Object, action string) bool {
    if action == "hard_delete" {
        // 直接删除 blob + 行
    }
    // 软删除
}
```

**缺失的核心能力：**

| 能力 | AWS S3 | AeroVault |
|------|--------|-----------|
| 对象级存储类指定 | ✅ `STANDARD`, `STANDARD_IA`, `GLACIER`, `DEEP_ARCHIVE`, `INTELLIGENT_TIERING` | ✅（仅标记，无行为差异）|
| 基于年龄的自动降级 | ✅ `Transition` 规则（天数 → 目标存储类） | ❌ 完全缺失 |
| 基于前缀/标签的条件迁移 | ✅ 按 prefix/tag 匹配 | ❌ 完全缺失 |
| 存储类语义（副本/延迟/可用性） | ✅ 各存储类不同 SLA | ❌ 无差异 |
| 非当前版本的自动迁移 | ✅ `NoncurrentVersionTransition` | ❌ 完全缺失 |
| INTELLIGENT_TIERING（自动优化） | ✅ 基于访问频率自动调 | ❌ 完全缺失 |
| 冷存储恢复（Restore） | ✅ `x-amz-restore` + 临时副本 | ❌ 仅标记 `GLACIER`，无恢复语义 |
| 最小存储期与删除罚金 | ✅ `S3 Glacier` 90 天 min | ❌ 无 |


### 架构设计

```
存储类生命周期迁移引擎：

┌──────────────────────────────────────────────────────────────────┐
│                      Lifecycle Rules Engine                      │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │  Rule 1: prefix=logs/* + age>30d → STANDARD_IA         │    │
│  │  Rule 2: prefix=archive/* + age>90d → STANDARD_IA      │    │
│  │  Rule 3: prefix=archive/* + age>365d → GLACIER         │    │
│  │  Rule 4: current_version age>180d → STANDARD_IA         │    │
│  │  Rule 5: noncurrent_version age>30d → GLACIER           │    │
│  └─────────────────────────────────────────────────────────┘    │
│                               │                                  │
│                               ▼                                  │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │               Transition Scheduler                       │    │
│  │  • 每日扫描 bucket 的生命周期规则                        │    │
│  │  • 按 (tenant, bucket, prefix) 分片批量评估              │    │
│  │  • 每个待迁移对象入队 JobPool 任务                       │    │
│  └─────────────────────────────────────────────────────────┘    │
│                               │                                  │
│          ┌────────────────────┼────────────────────┐            │
│          ▼                    ▼                    ▼            │
│  ┌──────────────┐   ┌──────────────┐   ┌──────────────┐       │
│  │STANDARD→IA   │   │IA→GLACIER    │   │GLACIER→RESTORE│       │
│  │改元数据+无操作│   │标记为冷归档   │   │创建临时副本    │       │
│  └──────────────┘   └──────────────┘   └──────────────┘       │
└──────────────────────────────────────────────────────────────────┘
```

**存储类行为矩阵（建议）：**

| 存储类 | 后端策略 | GET 行为 | 最小存储期 | 恢复时间 | 推荐场景 |
|--------|---------|---------|-----------|---------|---------|
| `STANDARD` | 当前后端（local/S3）| 即时 | 无 | 即时 | 活跃数据 |
| `STANDARD_IA` | 当前后端（同 STANDARD）| 即时 | 30 天 | 即时 | 低频访问但需即时读取 |
| `GLACIER` | 当前后端（对象标记，数据仍存）| 需先 Restore | 90 天 | 1–5 分钟（预热）| 归档但偶尔需要访问 |
| `DEEP_ARCHIVE` | 可配置为更廉价后端 | 需先 Restore | 180 天 | 数小时 | 长期合规归档 |

**配置变更：**

```go
// 桶的生命周期规则扩展
type LifecycleRule struct {
    ID                          string
    FilterPrefix                string
    FilterTag                   map[string]string
    Transitions                 []Transition   // 分层迁移规则
    Expiration                  *Expiration    // 最终过期（已有）
    NoncurrentVersionTransitions []Transition  // 非当前版本迁移
    NoncurrentVersionExpiration  *Expiration   // 非当前版本过期
}

type Transition struct {
    Days          int    // 对象创建后 N 天触发
    StorageClass  string // 目标存储类
}

type Expiration struct {
    Days int // 过期删除
}
```

**新增组件与行数估算：**

| 组件 | 行数 | 说明 |
|------|------|------|
| `internal/reconcile/tiering.go` — 生命周期规则引擎 + Transition Scheduler | ~250 | 规则解析、对象匹配、JobPool 调度 |
| `internal/service/storage_class.go` — 存储类语义（Restore/冻结/解冻） | ~150 | RestoreObject、临时副本管理 |
| `internal/repository/sql_lifecycle.go` — 多规则存储与查询 | ~120 | 桶的 LifecycleRules JSON 存取 |
| `internal/api/rest/admin.go` — PUT/GET Lifecycle API | ~80 | 管理端点 |
| `internal/api/s3compat/handler.go` — S3 `?lifecycle` 扩展 | ~60 | XML 解析与序列化 |
| 迁移文件 — `bucket_lifecycle_rules` 表扩展 | ~40 | 4 文件 |
| 测试 | ~200 | |
| **合计** | **~900** | |

### 工程要点

- **首次迁移零成本**：STANDARD → STANDARD_IA 仅需更新元数据中的 `storage_class` 字段，无需移动数据。IA → GLACIER 同理（如存储后端相同）。仅 GLACIER → DEEP_ARCHIVE 需要跨后端移动。
- **Restore 临时副本**：GLACIER/DEEP_ARCHIVE 对象的 GET 应返回 `ErrObjectRestoreRequired` + `x-amz-restore` header。POST `?restore` 触发一个后台 JobPool 任务，完成后临时副本在 repository 中以 `expires_at` 行存在。
- **非当前版本迁移**：版本化桶中，旧版本（`deleted_at IS NOT NULL` 但 blob 仍存在）应遵循独立的 `NoncurrentVersionTransition` 规则，通常更快降级到 GLACIER。

---

## 方向二：用量计量与平台级计费底座（Usage Metering & Billing Platform）

### 现状

当前系统可计量：

| 维度 | 已有能力 | 缺失 |
|------|---------|------|
| AI 调用成本 | ✅ `ai_usage_cost` 表 + `SumAICostMicros` | — |
| 租户级存储配额 | ✅ `tenants` 表 `max_bytes`/`max_objects` + `AddTenantUsage` | ⚠️ 仅用于准入控制，无时间维度计量 |
| 桶级统计 | ✅ `BucketStats`（`ObjectCount` + `TotalSize`） | — |
| OTel metrics | ✅ `storage_bytes`/`storage_objects` gauge | ⚠️ 仅实时快照，无时序累计 |

**完全缺失的计费能力：**

| 计量项 | AWS S3 口径 | AeroVault 状态 |
|--------|------------|---------------|
| **存储用量（GB-月）** | `TimedStorage-ByteHrs` / 月 | ❌ 无时间维度，仅有实时 `storage_bytes` |
| **PUT/COPY/POST/LIST 请求数** | `Requests-Tier1` / `Requests-Tier2` | ❌ 完全缺失 |
| **GET/HEAD 请求数** | `Requests-Tier2` | ❌ 完全缺失 |
| **数据取回（Data Retrieval）** | `Retrieval-Bytes`（IA/Glacier） | ❌ 完全缺失 |
| **出口流量（Egress）** | `DataTransfer-Out-Bytes` | ❌ 完全缺失 |
| **跨区域复制流量** | `DataTransfer-Regional-Bytes` | ❌ 完全缺失 |
| **存储类间迁移** | `LifecycleTransition-*` | ❌ 方向一依赖此项 |
| **API Key 级别请求分账** | — | ❌ 完全缺失 |

### 为什么需要

| 场景 | 当前矛盾 |
|------|---------|
| **SaaS 运营**：按存储+请求数向租户收费 | 无计费数据 → 只能按固定月费收取，无法区分重度用户 |
| **成本归因**：某个 API Key 产生了大量 GET 请求导致 egress 成本飙升 | 无从判断是哪个 Key、哪个 bucket、哪个前缀 |
| **免费层控制**：免费租户每月 5GB 存储 + 10K 请求 | 无法计量请求数，只能硬限制存储 |
| **成本分析**：运维想知道"上周哪个租户产生了最多的 S3 请求" | 只有实时指标，无历史累计 |
| **节省计划**：租户选择预付年费获得折扣 | 需要精确的月度计量数据作为折扣计算基础 |

### 架构设计

```
计费数据模型：

metering_events 表（不可变日志，按时间分片）：
┌──────────────────────────────────────────────┐
│ id          BIGSERIAL PRIMARY KEY            │
│ tenant_id   TEXT NOT NULL                    │
│ bucket      TEXT NOT NULL                    │
│ api_key_id  TEXT           (可空，API Key 级分账)│
│ category    TEXT NOT NULL  'storage'/'requests'/│
│                            'egress'/'retrieval'│
│ metric      TEXT NOT NULL  'byte_hours'/'put_count'/│
│                            'get_count'/'bytes_out'/│
│ value       BIGINT NOT NULL                  │
│ recorded_at TIMESTAMPTZ NOT NULL             │
│ day         DATE NOT NULL  (分区键)           │
└──────────────────────────────────────────────┘

日汇总视图（materialized for fast billing queries）：
daily_usage_summary:
  tenant, bucket, api_key_id, day, category, metric, SUM(value)
```

**计费记录点（Event Sourcing 风格）：**

```go
// 在 FileService 的关键路径上注入 Meter 调用
// 非阻塞，失败不阻断（best-effort 计费）
type Meter struct {
    repo repository.Repository
}

func (m *Meter) Record(ctx context.Context, e MeterEvent) {
    // 异步批量写入 metering_events 表
    // 使用独立连接池避免与主业务 SQL 竞争
}

// 记录点分布
// internal/service/file_crud.go
//   Put → Meter.Record(put_count=1, byte_hours=size)
//   Get → Meter.Record(get_count=1, egress_bytes=size)
//   storage.Put → Meter.Record(byte_hours=size)
// internal/api/rest/handler.go
//   List → Meter.Record(list_count=1)
// internal/api/s3compat/handler.go
//   ListObjectsV2 → Meter.Record(list_count=1)
```

### 工程估算

| 组件 | 行数 | 说明 |
|------|------|------|
| `internal/meter/meter.go` — Meter 接口 + 批量写入器 | ~150 | 异步攒批、独立协程、超时保护 |
| `internal/meter/events.go` — MeterEvent 类型体系 | ~60 | 全计量维度的结构化类型 |
| `internal/repository/sql_metering.go` — 计费日志写入 + 汇总查询 | ~200 | 按日分区、预聚合 |
| 迁移文件 — `metering_events` + `daily_usage_summary` | ~60 | 4 文件（含分区策略）|
| `internal/service/file_crud.go` — Meter 注入点 | ~80 | PUT/GET/DELETE/LIST/Presign |
| `internal/api/rest/admin.go` — 计费查询端点 | ~80 | 日/月/年汇总、按 tenant/bucket/API Key 维度 |
| 测试 | ~150 | |
| **合计** | **~780** | |

### 设计要点

- **异步非阻塞**：Meter.Record 用 channel 缓冲 + 批量 flush，失败只 warn 不阻塞业务
- **时间维度存储**：`byte_hours` 需要在 PUT 时记录 size，并在 DELETE/soft-delete/retention 时记录终止时间点。简化方案：每小时以 gauge 采集 `storage_bytes`，用于 GB-月近似计算
- **与 Quota 分离**：计费 ≠ 准入控制。Quota 是当前资源限制；Billing 是历史经济记录
- **API Key 级分账**：`api_key_id` 从请求的认证上下文中提取（`auth.ExecutionContext`——v51 方向五的设计衍生）

---

## 方向三：对象级备份与时间点恢复（Point-in-Time Backup & Recovery）

### 现状

当前"备份"能力：

```
snapshot CLI 工具（internal/snapshot/snapshot.go）：
  - 仅支持 SQLite + local FS（tar.gz 全量打包）
  - 需要停机或文件级快照一致性
  - 无增量备份
  - 无定时自动化
  - 无恢复验证

跨区域复制（internal/replication/replication.go）：
  - 单向异步复制到另一个存储后端
  - 仅针对 object.created 事件
  - 无复制延迟监控
  - 无 RPO/RTO 保证
  - 无故障切换编排
```

**"复制 ≠ 备份"的核心区别：**

| 维度 | 复制（当前实现） | 备份（缺失能力） |
|------|---------------|----------------|
| 数据一致性 | 最终一致（异步 FIFO） | 时间点一致（snapshot isolation） |
| 恢复粒度 | 无恢复操作（只能手工重定向读取） | 按桶/按前缀/按时间点恢复 |
| 数据保护 | 不防护逻辑错误（误删/损坏也会被复制） | 保留历史快照，可回滚到错误前 |
| RPO | 秒到分钟级（取决于事件队列 backlog） | 可配置：1h/6h/24h |
| RTO | 无（需要手动切换 DNS/路由） | 有：恢复到本地/新集群的时间 |
| 验证 | 无自动验证 | 定期自动恢复测试（restore drill） |
| 版本保留 | 仅保留当前（覆盖式） | 保留 N 个快照（可配置） |

### 架构设计

```
备份系统架构：

                      ┌──────────────────┐
                      │  Backup Scheduler │
                      │  per-tenant /     │
                      │  per-bucket 策略  │
                      └────────┬─────────┘
                               │
              ┌────────────────┼────────────────┐
              ▼                ▼                ▼
     ┌──────────────┐ ┌──────────────┐ ┌──────────────┐
     │ 全量备份      │ │ 增量备份      │ │ 日志备份      │
     │ (Full)       │ │ (Incremental) │ │ (WAL/CDC)    │
     └──────┬───────┘ └──────┬───────┘ └──────┬───────┘
            │                │                │
            ▼                ▼                ▼
     ┌────────────────────────────────────────────┐
     │            Backup Storage Layer             │
     │  (目标 storage.Storage 实例，本地或远端)    │
     │  ┌──────────────────────────────────────┐  │
     │  │ full_20260710_120000.tar.gz          │  │
     │  │ incr_20260710_130000.tar.gz          │  │
     │  │ incr_20260710_140000.tar.gz          │  │
     │  │ full_20260711_120000.tar.gz          │  │
     │  └──────────────────────────────────────┘  │
     └────────────────────────────────────────────┘
```

**备份策略配置：**

```go
type BackupPolicy struct {
    Tenant           string
    Bucket           string // "" = all buckets
    FullInterval     time.Duration // 全量周期（默认 7 天）
    IncrementalInterval time.Duration // 增量周期（默认 1 小时）
    RetentionDays    int    // 快照保留天数
    Target           string // 目标 storage backend 标识
    VerifyAfterBackup bool  // 备份后自动恢复验证
    PauseOnFailure   bool   // 连续失败后暂停
}
```

**增量备份策略（基于版本化）：**

```go
// 增量备份 = 自上次 full 或 incr 备份后变更的对象列表
// 每个变更对象以独立 tar 项存储（{storage_key}.{version_id}）

type IncrementalManifest struct {
    BaseSnapshot  string    // 基快照 ID
    CreatedAt     time.Time
    Objects       []IncrementalObject
}

type IncrementalObject struct {
    Tenant     string
    Bucket     string
    Key        string
    VersionID  string
    StorageKey string
    Action     string // "created" | "deleted"
    Size       int64
}
```

**恢复时间点选择：**

```
用户指定目标时间 T →
  1. 找 T 之前最近的全量快照 F
  2. 找 F 之后、T 之前的所有增量快照 I₁, I₂, ..., Iₙ
  3. 恢复顺序：解压 F → 顺序应用 I₁ → I₂ → ... → Iₙ
  4. 产生时间点 T 的独立恢复实例
```

**恢复验证（Backup Verification / Restore Drill）：**

```go
func (b *BackupEngine) Verify(ctx context.Context, snapshotID string) error {
    // 1. 将快照恢复到临时目录 /tmp/restore-verify-{uuid}
    // 2. 用独立的 SQLite 实例验证数据库完整性（PRAGMA integrity_check）
    // 3. 抽样 N 个存储对象，计算 ETag 匹配
    // 4. 生成验证报告
    // 5. 清理临时目录
}
```

### 工程估算

| 组件 | 行数 | 说明 |
|------|------|------|
| `internal/backup/scheduler.go` — 备份调度器 + 策略管理 | ~200 | cron 风格周期调度 |
| `internal/backup/full.go` — 全量备份（清单 + 数据） | ~180 | 迭代 ListObjects，逐对象复制 |
| `internal/backup/incremental.go` — 增量备份 | ~150 | 基于事件的变更捕获 |
| `internal/backup/restore.go` — 时间点恢复 | ~200 | 快照链回放 |
| `internal/backup/verify.go` — 备份验证 | ~100 | integrity check + 抽样校验 |
| `internal/backup/manifest.go` — 清单格式 | ~60 | manifest.json 序列化 |
| `internal/repository/sql_backup.go` — 备份策略存储 | ~80 | BucketConfig 扩展 |
| `internal/api/rest/backup.go` — 管理端点 | ~80 | 查看/创建/恢复备份 |
| `internal/cli/cli_backup.go` — CLI 扩展 | ~60 | backup/restore/verify 子命令 |
| 测试 | ~200 | |
| **合计** | **~1,310** | |

### 设计约束

- **不锁业务**：备份读取操作使用 `storage.Get` 流式读取，不占用长事务
- **与 snapshot 工具共存**：`internal/snapshot` 保持为"单机一键救援"工具；`backup/` 是"企业级自动备份恢复"引擎
- **仅同步有变更的数据**：增量备份通过 `EventAccessed` 之外的 `EventCreated/Deleted` 变更追踪
- **不备份事件表**：事件是业务过程的瞬时记录，不属于用户数据；备份还原后事件从头开始

---

## 方向四：运维面板与管理控制台（Operator Console / Admin Dashboard）

### 现状

当前系统具备：

```
运维可观测性：
  ✅ /metrics (Prometheus 格式) — 15 个 OTel instrument
  ✅ /healthz — 存活检查
  ✅ /readyz — 就绪检查
  ✅ deploy/grafana/dashboard.json — 12-panel dashboard（面向 AI ops）
  ✅ deploy/prometheus/alerts.yml — 3 条告警规则

管理端点：
  ✅ /v1/admin/tenants — CRUD
  ✅ /v1/admin/keys — API Key 管理
  ✅ /v1/admin/jwt — JWT 签发
  ✅ /v1/admin/audit — 审计日志
  ✅ /v1/admin/jobs — 查看/重试后台任务

用户界面：
  ✅ /ui — Web UI（search/detail/lineage/chat + 拖拽上传）
```

**缺失的运维面板：**

| 能力 | 当前状态 | 为什么重要 |
|------|---------|-----------|
| **系统健康总览** | 只有 `/healthz` 和 `/readyz` 的文本响应 | 运维不知道"系统整体正常吗？——存储、DB、索引器、复制都在线吗？" |
| **后台任务面板** | 需要 curl API → 解析 JSON | 运维想一眼看完：当前 pending 的 job 数、失败率、卡住的 job |
| **事件流检视器** | 需要 curl `/v1/events/stream` | 运维想按时间范围搜索事件，查看最近的审计轨迹 |
| **租户用量仪表盘** | 需要 curl `/v1/usage` × 租户数 | 运维想看到所有租户的存储排行、API 调用排行 |
| **配置验证与运行时编辑** | 需要重启改环境变量 | 运维想在线修改日志级别/限流 RPS/预算，无需重启 |
| **告警历史与通知频道测试** | 仅 Prometheus 告警规则 | 运维想查看告警触发历史、测试 webhook 投递 |
| **SLO 面板** | 不存在的概念 | 运维想知道：过去 7 天 GET 请求的 99 线延迟 ≤ 500ms 吗？ |
| **存储分层可视化** | 不存在（方向一依赖） | 运维想知道多少数据在 STANDARD vs STANDARD_IA vs GLACIER |

### 架构设计

```
运维控制台（独立 SPA，/admin 路由）：

┌─────────────────────────────────────────────────────────┐
│                    Sidebar                               │
│  ┌─────────┐  ┌─────────┐  ┌─────────┐  ┌───────────┐ │
│  │ 总览     │  │ 后台任务 │  │ 事件    │  │ 租户管理  │ │
│  │ Overview │  │  Jobs   │  │ Events  │  │  Tenants  │ │
│  ├─────────┤  ├─────────┤  ├─────────┤  ├───────────┤ │
│  │ 配置     │  │ 存储    │  │ 日志    │  │ 告警      │ │
│  │ Config  │  │ Storage │  │ Audit   │  │ Alerts   │ │
│  └─────────┘  └─────────┘  └─────────┘  └───────────┘ │
└─────────────────────────────────────────────────────────┘

总览面板（最优先）：
  - DB 状态（连接数、迁移版本、wal 大小）
  - 存储状态（各后端健康、SSE 配置）
  - 索引器状态（已索引对象数、最后索引时间、skipped 对象数）
  - 后台任务池（workers 数、pending/running/failed 分布）
  - 事件总线（已发事件数、丢弃数、subscriber 数量）
  - 租户快照（租户数、总存储、配额使用率 top-5）
```

**实现策略：** 用纯 HTML/CSS/JS 实现（复用 `/ui` 的技术栈），通过已有 REST API 获取数据。增量构建，每个面板独立，不新增后端端点——前提是现有端点已暴露所需数据。

**需要新增的后端点（如现有端点不足）：**

| 端点 | 数据来源 | 行数估计 |
|------|---------|---------|
| `GET /v1/admin/system/health` | 聚合 `repo.Ping`, `store.Stat`, 索引器状态, 事件丢弃数 | ~50 |
| `GET /v1/admin/system/jobs/stats` | `repo.JobStats()` + `repo.CountJobsByStatus()` | ~30 |
| `GET /v1/admin/system/storage` | `store.Stat` 各后端健康 | ~30 |
| `GET /v1/admin/tenants/usage` | `repo.ListTenantQuotas()` + 排序 | ~40 |
| `GET /v1/admin/tenants/:t/events` | `repo.NextUnconsumedEvents()` + 过滤 | ~30 |

**前端行数估计：**

| 面板 | 技术栈 | 行数 |
|------|--------|------|
| 总览面板 | 现有 Web UI 技术栈 | ~200 |
| 后台任务面板 | 同上 | ~150 |
| 事件检视器 | 同上 | ~150 |
| 租户管理面板 | 同上（复用 admin endpoints） | ~180 |
| 配置面板 | 同上（仅显示静态配置） | ~100 |
| **合计** | | **~780** |

### 设计要点

- **现有 API 优先**：尽量不新增后端端点，用现有的 admin API + `/metrics` 数据
- **可选对 Prometheus 的依赖**：面板应能直接通过 REST API 获取数据，而非必须依赖 Prometheus（为无 Prometheus 部署兜底）
- **懒加载即用型**：只需 `WEBUI_ENABLED=true` 即可自动挂载 `/admin` 路由
- **与现有 UI 共存**：`/ui` 是面向终端用户的搜索/聊天界面；`/admin` 是面向运维的管理面板。可以用统一的 [user/admin] 选择器切换

---

## 方向五：对象生命周期工作流与审批治理引擎（Object Lifecycle Workflow & Approval Engine）

### 现状

当前系统已实现：

```
✅ 对象锁 / WORM：locked_until + legal hold
✅ 生命周期过期：ExpireAfterDays → 软/硬删除
✅ 审计日志：admin 操作记录 audit_log
✅ 多租户角色：API Key scope 体系
✅ 事件通知：webhook + SSE streaming
```

**缺失的治理能力：**

| 场景 | 当前行为 | 理想行为 |
|------|---------|---------|
| 批量删除 1000 个对象 | 立即执行，不可逆 | 提交审批请求 → 通知主管 → 审批通过 → 执行 |
| 释放 Legal Hold | 立即释放（仅检查 scope） | 需要第二个操作者确认（two-person rule） |
| 缩短对象锁保留期（GOVERNANCE 模式）| 不支持（锁不可逆） | 需要特定权限的审批才能缩短 |
| 删除一个存有重要数据的桶 | 立即删除 | 需要创建删除工单 → 审批 → 延迟执行 |
| 修改桶的 versioning 配置 | 立即生效 | 需要审批 + 影响范围评估 |
| 保留某个对象直到某日期后再自动删除 | 需设置 tag + 配置 lifecycle | 提交保留策略 → 法务审批 → 系统强制执行 |
| 对象跨桶复制审批 | 无此能力 | 提交复制请求 → 数据所有者审批 → 执行 |

### 为什么需要

1. **合规与审计**：SOX、HIPAA、GDPR 等法规要求关键操作必须经过审批、有完整的批准链
2. **防误操作**：大范围删除/覆盖是企业存储的第一大事故原因（AWS 史上多次大规模故障均源于误删）
3. **职责分离**：同一操作员不应该能同时发出删除请求和审批该请求
4. **产品差异化**：S3 本身不提供审批工作流，这是 AeroVault 相比纯 S3 兼容存储的增值能力

### 架构设计

```
工作流引擎架构：

┌────────────────────────────────────────────┐
│              Workflow Engine                │
│                                            │
│  1. Request Creation                       │
│     ┌─────────────────────────────────┐   │
│     │ type: "bulk_delete"             │   │
│     │ requester: "user_a"             │   │
│     │ target: "bucket/data/*"         │   │
│     │ reason: "cleanup old logs"      │   │
│     │ status: "pending_approval"      │   │
│     └─────────────────────────────────┘   │
│              │                             │
│  2. Approval Routing                      │
│     ┌─────────────────────────────────┐   │
│     │ approver: "admin@corp.com"      │   │
│     │ timeout: 72h (auto-escalate)    │   │
│     │ required_approvals: 1           │   │
│     └─────────────────────────────────┘   │
│              │                             │
│  3. Execution / Rejection                  │
│     ├─ Approved → 异步执行（或定时执行）    │
│     └─ Rejected → 记录原因 → 通知申请人     │
└────────────────────────────────────────────┘
```

### 工作流类型

| 工作流类型 | 触发条件 | 所需审批数 | 执行方式 |
|-----------|---------|-----------|---------|
| `bulk_delete` | 单次 DELETE 影响对象数 > 阈值（默认 100）| 1 | 异步 JobPool 执行 |
| `permanent_delete` | 硬删除（hard=true）任何对象 | 1 | 异步执行 |
| `legal_hold_release` | 释放 Legal Hold | 2（two-person rule）| 即时 |
| `lock_shorten` | 缩短 object lock retention（GOVERNANCE 模式）| 1（需特定 scope）| 即时 |
| `bucket_deletion` | DELETE /v1/admin/buckets/{name} | 2 | 异步 + 延迟（72h 冷却期）|
| `versioning_disable` | 关闭版本化桶的 versioning | 1 | 即时 |
| `cross_bucket_copy` | 跨桶复制对象 | 1（源桶所有者）| 异步 |

### 工程估算

| 组件 | 行数 | 说明 |
|------|------|------|
| `internal/workflow/types.go` — 工作流类型、状态、审批路由 | ~80 | 数据结构 + 常量 |
| `internal/workflow/engine.go` — 工作流引擎（CRUD + 状态机）| ~180 | 创建/审批/拒绝/升级/执行 |
| `internal/workflow/approver.go` — 审批人路由（基于 scope/标签/静态配置）| ~80 | 路由规则 |
| `internal/workflow/actions.go` — 各工作流类型的执行器 | ~150 | bulk_delete, lock_release 等实际逻辑 |
| `internal/repository/sql_workflow.go` — 工作流表 + 审批记录 | ~120 | workflows + approvals 表 |
| `internal/api/rest/workflow.go` — REST 端点 | ~120 | 创建/列表/审批/拒绝/查看 |
| 迁移文件 — `workflows` + `approvals` 表 | ~60 | 4 文件 |
| `internal/api/rest/admin.go` — 扩展到 bucket delete 触发工作流 | ~50 | 集成点 |
| 测试 | ~200 | |
| **合计** | **~1,040** | |

### 设计要点

- **默认关闭、opt-in**：工作流引擎仅在 `WORKFLOW_ENABLED=true` 时激活。未启用时行为不变（向后兼容）
- **阈值可配置**：`WORKFLOW_BULK_DELETE_THRESHOLD=100` 控制超过多少对象触发审批
- **Webhook 通知**：工作流状态变更（待审批/已批准/已拒绝）通过已有 EventBus 推送，webhook 接收
- **审批超时自动升级**：`WORKFLOW_APPROVAL_TIMEOUT=72h`，超时后可升级到下一个审批人
- **与审计日志集成**：每次审批操作自动写入 audit_log，形成完整的合规证据链

---

## 综合优先级建议

```
方向一：存储分层引擎    ████████████████████  P1  ~900 行  成本优化 + S3 兼容性
方向三：备份与 PITR     ████████████████████  P1  ~1,310 行  数据安全基座
方向二：计费底座        ████████████████      P2  ~780 行  SaaS 商业模式前提
方向四：运维面板        ██████████████        P2  ~780 行  运维效率
方向五：工作流审批      ████████████          P2  ~1,040 行  企业合规差异化

     Phase 1 (2周)                  Phase 2 (1月)                   Phase 3 (2月+)
  ┌─────────────────────┐    ┌─────────────────────┐      ┌──────────────────────┐
  │ 方向一：Tiering      │    │ 方向一：GLACIER      │      │ 方向三：增量备份       │
  │   · STANDARD→IA 迁移  │    │   + Restore 语义     │      │   + 自动调度          │
  │   · Lifecycle 规则引擎│    │ 方向二：计费数据模型    │      │ 方向五：全部工作流类型  │
  │   · S3 API 扩展       │    │   · metering_events  │      │                      │
  ├─────────────────────┤    ├─────────────────────┤      ├──────────────────────┤
  │ 方向四：总览面板      │    │ 方向四：全部面板      │      │ 方向二：按需报表        │
  │   · 系统健康          │    │   · 后台任务          │      │   · 租户账单导出       │
  │   · 租户快照          │    │   · 事件检视器        │      │                      │
  └─────────────────────┘    └─────────────────────┘      └──────────────────────┘
     Phase 1 (2周)                   Phase 2 (1月)              Phase 3.5 (1月)
    方向五：bulk_delete 工作流     方向五：lock_release         方向五：bucket_delete
    + legal_hold 审批（P0 安全）    + versioning 审批           + cross_bucket 审批
```

### 建议执行序列

1. **第 1–2 周（方向一核心 + 方向四 MVP）**：
   - Lifecycle 规则引擎（STANDARD→STANDARD_IA 迁移）~300 行
   - 系统健康总览面板 ~200 行（后端 2 个新端点 + 前端面板）
   - **这两个方向零外部依赖，可增量落地**

2. **第 3–4 周（方向三全量备份 + 方向五删除审批）**：
   - 全量备份调度器 + 恢复 ~380 行
   - `bulk_delete` 工作流 + `legal_hold_release` 审批 ~300 行
   - **备份带来数据安全性、审批带来防误操作——安全价值立竿见影**

3. **第 5–6 周（方向一 GLACIER 语义 + 方向二计费底座 + 方向四扩展面板）**：
   - RestoreObject 语义 + 临时副本 ~150 行
   - `metering_events` 表 + 记录点 ~400 行
   - 后台任务面板 + 事件检视器 ~300 行
   - **GLACIER 语义是 S3 兼容性的最后缺角；计费是 SaaS 化的第一推动力**

---

## 结论

这 5 个方向虽然功能面差异很大（从存储引擎到计费系统到运维面板），但它们指向同一个判断：**AeroVault 已经从"功能完整的对象存储系统"迈向了"平台级产品"的门槛，但还缺少平台经济模型、企业治理能力和运维支撑系统这三个关键支柱。**

| 方向 | 解决的问题 | 对产品定位的贡献 |
|------|-----------|----------------|
| **存储分层引擎** | 存储成本与数据热度不匹配 | 从"所有数据一个价"到"按价值付费" → 降低用户总体拥有成本 |
| **计费底座** | 无法按量计费支持 SaaS 模式 | 从"技术可用"到"商业可行" → 开启收入模型 |
| **备份与 PITR** | 误删/损坏/勒索软件导致的数据不可恢复 | 从"有复制"到"有保障" → 建立数据安全的最后防线 |
| **运维面板** | 系统运营黑盒，依赖 curl 和 grep | 从"开发友好"到"运维友好" → 降低运营成本 |
| **工作流审批** | 高危操作无防护屏障 | 从"技术控制"到"组织控制" → 满足企业合规需求 |

这 5 个方向与已有的 51 期分析不重叠、不重复，因为它们解决的不是"加不加某个 API 端点"的功能问题，而是 **"系统从技术验证原型演进为可运营、可销售、可信任的企业平台还缺什么"** 的产品化问题。
