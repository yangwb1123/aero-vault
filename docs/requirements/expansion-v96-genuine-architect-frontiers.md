# 高价值扩展方向：存储分层迁移、标签驱动自动化、调度任务框架、跨协议对象身份、数据自愈框架

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — `cmd/server/main.go` + `internal/` 全部子包（237+ Go 源文件，50 对迁移文件），3 套 SDK，MCP 双模式，Web UI，`deploy/` 全套配置，`HARNESS.md`，`AGENTS.md`，ROADMAP.md，CHANGELOG.md  
> **去重验证：** 对 `docs/requirements/` 下全部 95 份既有分析文档（`expansion-directions.md` ~ `expansion-v95-engineering-architecture-blindspots.md`）逐方向进行全文关键词正则 + 代码锚点交叉验证  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。选取**代码中存在明确锚点、有实质性产品/架构影响、且在 95 轮分析中未被独立深度覆盖**的方向。每个方向包含：产品价值 → 现状与代码证据 → 架构权衡 → 边界情况。

---

## 去重验证

对 `docs/requirements/` 下全部 95 份既有分析文档逐方向进行关键词正则 + 语义交叉验证：

| 方向 | 既往覆盖情况 |
|------|-------------|
| **存储分层迁移引擎** | ⚠️ **浅层概念提及，零架构分析** — `extensions.md`（项目早期草稿）方向表格 2 行概念描述了 `Transition` 规则和 `RestoreObject`；v13 一个框图（约 20 行）展示 `target_class` 字段但无系统架构分析；v25 在高价值方向表格中 1 行指出分层转换依赖服务端 Copy——**均无代码锚点驱动的实现路径、边界情况、性能模型分析**。正则搜索 `transition.*rule\|tier.*transition\|storage.*tier.*implement\|Transition.*Days\|storage.*class.*migrat\|migrat.*storage.*class\|restore.*from.*glacier\|解冻` → 仅 `extensions.md` 和 v13 框图命中 |
| **标签驱动自动化引擎** | ✅ **零实质性覆盖** — v33 方向表第 3 行声明为**"完全未覆盖"**，但仅提供了约 50 行的概述性文字（聚焦于问题陈述和场景列表），**从未给出代码锚点驱动的实现路径、架构模型、边界情况分析**。其余 94 份文档正则搜索 `tag.*automation\|tag.*orchestrat\|tag.*propagat\|tag.*inherit\|tag.*rule\|tag.*policy` → **0 其余命中** |
| **调度任务与定时作业框架** | ✅ **零实质性覆盖** — 全量 95 份文档正则搜索 `cron.*job\|cron.*expres\|schedule.*task\|periodic.*schedul\|future.*schedul\|one.*shot.*schedul\|scheduled.*job\|timer.*registry\|schedule.*mgmt\|schedule.*job.*pool` → **0 命中**。v31 在用户自定义函数（FaaS）方向上下文用 "cron" 指代「用户自建的外部调度器」，**非系统内建调度框架**；v52 备份方向提及 "cron 风格周期调度" 但聚焦备份策略本身而非系统级的调度框架 |
| **跨协议对象身份与统一引用模型** | ✅ **零实质性覆盖** — v78 方向三声明为**"完全未覆盖"**：正则搜索 `object.*ident.*protocol\|cross.protocol.*ident\|protocol.*agnostic.*ref\|规范引用\|canonical.*ref\|URN.*object\|aero://\|统一.*标识.*协议` → **0 命中**。v59 方向一覆盖"多协议一致性模型"聚焦**读写一致性语义**；v40 方向四覆盖"四协议统一访问控制模型"聚焦**认证一致性**——两者均非对象标识或引用 |
| **数据完整性自愈框架** | ⚠️ **部分覆盖关键子集但无整体框架** — v51 方向四覆盖「on-read 完整性校验」（GET 时比对 ETag），v52 方向三覆盖「备份与 PITR」聚焦灾备恢复，v79 方向二覆盖「CompleteMultipart ETag 交叉验证」。**三者均为数据完整性（data integrity）的"检测"（detection）维度，缺少"修复"（repair/healing）维度**：无从副本自动修复、无纠删码、无位衰减（bit-rot）定期扫描、无存储后端冗余校验、无完整性积分卡。正则搜索 `self.health\|self.repair\|bit.rot\|erasure.cod\|repair.*from.*replica\|auto.*repair.*corrupt\|integrity.*heal\|repair.*object\|redundan.*check\|parity.*check` → **0 命中** |

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 |
|---|------|------|--------|---------|---------|
| **1** | **存储分层迁移引擎：从静态标签到自动分层转换** | 成本优化/架构 | **P1** | `StorageClass` 写入后永不变化；S3 生命周期 `Transition` 规则完全不支持；对象一旦写入 STANDARD 永远无法降冷或归档；缺少 `RestoreObject` 从归档层取回 | `internal/repository/repository.go:34`（`Object.StorageClass`—仅标签，无迁移逻辑）；`internal/reconcile/lifecycle.go`（`LifecycleJob.sweepExpired`—只处理 `Expiration`，**零 `Transition` 处理**）；`internal/api/s3compat/xml.go:216-230`（`lifecycleRule` 结构体——有 `Expiration`，**无 `Transition` 节点**）；`internal/service/file.go:19-20`（`DefaultStorageClass = "STANDARD"`—架构内仅此一个类）；`internal/storage/storage.go:Storage`（无 `RestoreObject` 方法）；`internal/api/s3compat/handler.go:100-101`（`?restore` 路由只处理软删除恢复，**非 GLACIER 取回**）；`internal/repository/repository.go:313-315`（`StorageClassCounts`—仅为仪表盘统计，从未用于调度） |
| **2** | **标签驱动自动化引擎：从被动元数据到主动策略执行** | 产品/运维 | **P1** | 标签是纯被动元数据；无法编写 "当对象打上 `retain=legal` 标签时自动激活 Legal Hold" 或 "标记为 `temp=true` 的对象 7 天后自动删除" 等规则；无标签继承、无标签配额、无标签审计 | `internal/repository/repository.go:36`（`Object.Tags map[string]string`—仅储存，零自动化）；`internal/repository/sql_objects.go:84-88`（`ListObjectsByTag`—仅查询，零规则引擎）；`internal/service/file_features.go:SetTags`（纯 CRUD，无副作用触发）；`internal/reconcile/lifecycle.go:LifecycleJob`（仅基于 `expire_after_days`，无 tag 条件）；`internal/auth/policy.go`（Bucket Policy 支持 `StringEquals` 条件但**无 `s3:ExistingObjectTag` 条件键**）；`internal/events/bus.go:Publish`（事件发布无 tag 谓词过滤）；`internal/repository/sql_tags_acl.go:upsertObjectTags`（纯存储，无标签校验、无标签大小/数量限制） |
| **3** | **调度任务与定时作业框架：从固定间隔的 Ticker 到声明式调度** | 基础设施/运维 | **P2** | 所有后台循环使用硬编码 `time.Ticker` + 固定间隔；无 cron 表达式、无反压感知、无单次未来调度、无任务依赖、无调度管理 API | `internal/reconcile/job.go:Run`（`time.NewTicker(l.interval)`—固定间隔轮询）；`internal/reconcile/lifecycle.go:Run`（同样 Ticker 模式）；`internal/reconcile/retention.go:Run`（同样 Ticker 模式）；`internal/events/webhook.go:RetryLoop`（`time.NewTicker(15*time.Second)`—硬编码 15s）；`internal/jobs/jobs.go:Pool`（仅支持"立即执行"或"延迟重试"——**无未来调度**）；`cmd/server/main.go:startReconcile`（传递时间为配置参数但底层仍为 Ticker）；`internal/config/config_app.go:ReconcileCfg.IntervalMinutes`（所有调度参数合并为同一个 IntervalMinutes） |
| **4** | **跨协议对象身份与统一引用模型：从协议特定标识到规范对象 URI** | 架构/产品完整 | **P2** | 同一文件在 REST 中为 `/v1/files/bucket/key`，在 S3 中为 `bucket/key`，在 WebDAV 中为 `prefix/bucket/key`，在 MCP 中为 `aero-vault://bucket/key`；无规范对象引用、无跨协议请求追踪、无统一的对象身份标识 | `internal/service/file.go:storageKey`（内部存储 key = `path.Join(tenant,bucket,key)`—非外部引用）；`internal/api/rest/router.go`（路由模式为 `/files/*`—不暴露协议无关标识）；`internal/api/s3compat/router.go`（`/{bucket}/{key}`—不同路径模式）；`internal/api/webdav/dav.go`（`davFS.OpenFile` 用 `strings.HasPrefix` 前缀匹配—全局唯一标识缺失）；`internal/mcp/protocol.go`（资源 URI 为 `aero-vault://bucket/key`—MCP 自有方案）；`internal/middleware/middleware.go:RequestID`（RequestID 存在但**不跨协议传播**——S3→REST 连续操作无可关联性）；`internal/repository/audit.go`（审计日志记录 `tenant/bucket/key` 但**无协议来源字段**） |
| **5** | **数据完整性自愈框架：从被动检测到主动修复** | 可靠性/合规 | **P1** | 系统可检测数据损坏（scrub/MD5 校验），但无法自动修复；无位衰减定期扫描；无纠删码或副本冗余；无声数据损坏影响评估（哪些对象/版本已损？何时开始？）；ETag 在 GET 路径上从不交叉验证 | `internal/reconcile/scrub.go`（`scrubObject`—验证 MD5 可检测损坏，但仅记录不修复）；`internal/service/file_crud.go:Get`（读路径不比对计算 ETag 与存储 ETag）；`internal/reconcile/job.go:maybeScrub`（被动扫描——等待 Reconcile 间隔触发，无独立的时间表）；`internal/repository/sql_objects.go`（`_aero_scrub_status` 元数据可记录"corrupt"状态但**无修复流程**）；`internal/storage/storage.go:Storage`（接口无 `WriteVerify` 或 `Checksum` 方法）；`internal/storage/local_read.go:Get`（流式读取后不计算验证 checksum）；`internal/telemetry/metrics.go:IncScrubTotal`（仅计数，无 corruption 分级指标） |

---

## 方向一：存储分层迁移引擎

### 产品价值

存储分层（storage tiering）是 S3 兼容对象存储最直接的成本优化手段。每个对象从创建起就默认存储在 STANDARD 层，但大部分数据的访问频率随生命周期递减：

| 数据生命周期阶段 | 最佳存储类 | 当前  | 有分层后 |
|-----------------|-----------|-------|---------|
| 最近 30 天（频繁访问） | STANDARD | ✅ 默认 | ✅ 默认 |
| 30-90 天（偶尔访问） | STANDARD_IA | ❌ 仍占 STANDARD 价格 | ✅ 自动降冷，存储成本降低 ~50% |
| 90-365 天（极少访问） | GLACIER | ❌ 无归档路径 | ✅ 自动归档，成本降低 ~90% |
| 1 年以上（合规保留） | DEEP_ARCHIVE | ❌ 不可能 | ✅ 最低成本长期保留 |

对于 PB 级数据，分层迁移可节省 **40-70%** 的存储费用，是直接的产品差异化优势。

### 现状

当前系统虽然完整存储了 `StorageClass` 字段并支持 S3 生命周期 `Expiration` 规则，但**完全没有分层迁移能力**：

**1. 生命周期 XML 模型缺少 `Transition` 节点**

```go
// internal/api/s3compat/xml.go 当前模型
type lifecycleRule struct {
    ID         string               `xml:"ID,omitempty"`
    Status     string               `xml:"Status"`
    Expiration *lifecycleExpiration `xml:"Expiration,omitempty"`
    // ❌ 无 Transition 节点
}
```

S3 标准要求支持多个 `Transition` 规则：
```xml
<Rule>
    <ID>tier-30</ID>
    <Status>Enabled</Status>
    <Transition>
        <Days>30</Days>
        <StorageClass>STANDARD_IA</StorageClass>
    </Transition>
    <Transition>
        <Days>90</Days>
        <StorageClass>GLACIER</StorageClass>
    </Transition>
</Rule>
```

**2. `LifecycleJob` 只处理过期，不处理迁移**

```go
// internal/reconcile/lifecycle.go
func (l *LifecycleJob) sweepExpired(ctx context.Context) (soft, hard int) {
    expired, err := l.repo.ListExpired(ctx, 200)  // ← 只查询过期对象
    // ... 只做 expiration（软删除/硬删除）
    // ❌ 无过渡逻辑
}
```

**3. `Storage` 接口无 `RestoreObject` 方法**

当对象归档到 GLACIER 后，读取前必须发出 `RestoreObject` 请求启动取回流程（通常 3-5 小时后可用）。当前系统无此概念。

**4. 存储后端无"存储类感知"能力**

`LocalStorage` 后端没有冷热数据分离概念——所有对象在同一目录树中。`S3Storage` 后端虽然支持 S3 的 `StorageClass` 参数（`PutObjectInput.StorageClass`），但写入后从不改变它。

### 架构权衡

**建议方案：分层任务引擎**

```
             ┌──────────────────────┐
             │ LifecycleJob         │ ← 已有：定时运行
             │ (reconcile/lifecycle)│
             └──────────┬───────────┘
                        │ 添加 Transition 处理
             ┌──────────▼───────────┐
             │ TransitionManager    │ ← 新增
             │ 1. 查询待迁移对象     │
             │ 2. 规则匹配          │
             │ 3. 派发迁移 Job      │
             └──────────┬───────────┘
                        │
        ┌───────────────┼───────────────┐
        ▼               ▼               ▼
 ┌────────────┐  ┌────────────┐  ┌────────────┐
 │ Local 迁移  │  │ S3 迁移     │  │ OSS/COS    │
 │ (blob copy) │  │ (class      │  │ (后端特定)  │
 └────────────┘  │  update API)│  └────────────┘
                 └────────────┘
```

**数据模型扩展：**

```sql
-- migration 0025: 在 lifecycle 中添加 Transition 支持
ALTER TABLE bucket_config ADD COLUMN lifecycle_json TEXT;  -- 扩展现有字段

-- 新增独立的 transition 表（可选方案）
CREATE TABLE transition_queue (
    id INTEGER PRIMARY KEY,
    object_id INTEGER NOT NULL REFERENCES objects(id),
    target_class TEXT NOT NULL,
    scheduled_at TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',  -- pending | in_progress | done | failed
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT
);
```

**分层策略模式：**

```go
type TransitionStrategy interface {
    // CanHandle 报告此策略是否能处理目标存储类
    CanHandle(targetClass string) bool
    // Transition 执行单对象分层迁移
    Transition(ctx context.Context, srcObj Object, targetClass string) (ObjectInfo, error)
    // Restore 从归档层取回对象（仅 GLACIER/DEEP_ARCHIVE）
    Restore(ctx context.Context, obj Object, restoreDays int) error
}
```

| 策略 | 适用场景 | 实现方式 |
|------|---------|---------|
| `LocalCopyTransition` | Local 后端内部迁移 | `os.Rename` + 软链接（同存储类无意义；不同类指不同目录） |
| `S3ClassTransition` | S3 后端内部迁移 | `s3.CopyObject` 指定 `StorageClass` 参数（零数据移动） |
| `CrossBackendTransition` | 跨后端迁移 | `source.Get` → `target.Put`（消耗带宽，需进度追踪） |

**与 Replication 的关系：**

`Replication` worker 已经实现了 backend-to-backend 的拷贝模式（Get → Put），但与分层迁移有两个根本不同：

| 维度 | Replication | Transition |
|------|------------|-----------|
| 触发器 | 实时事件驱动 | 定时规则驱动 |
| 保活源 | 保留源副本 | 迁移后删除源 blob（归档场景） |
| 目标 | 物理二级存储（另一后端） | 逻辑存储类（同一或不同后端） |
| 语义 | 副本 = 完整拷贝 | 迁移 = 数据移动 + class 更新 |

**RestoreObject 取回流程：**

GLACIER/DEEP_ARCHIVE 类的对象不可直接读取。RestoreObject 流程：

```
客户端 POST /v1/files/key/restore?days=7
  → repo.SetObjectMetadata(key, "_aero_restore_requested_at": now)
  → JobPool 调度 JobRestoreObject
  → storage.Restore(ctx, key, 7)  // S3: InitiateRestoreObject
  → 完成后更新 metadata: "_aero_restore_expires": now+7d
  → 恢复期间 GET 返回 403 (ObjectArchived)
  → 恢复完成后 GET 可读取
```

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **GLACIER 对象被请求 GET** | 返回 `403 InvalidObjectState` + 错误码 `ObjectNotInActiveTier`，告知调用方需先 Restore |
| **迁移目标对象已存在** | Transition 本质是覆盖（新 storage class = 新 blob），使用 `UpsertObject` 更新元数据行的 `storage_class` + `storage_key` |
| **迁移中源对象被修改** | 在迁移前 `SELECT ... FOR UPDATE` 锁定对象行；若 `updated_at` 已变化则跳过本次迁移 |
| **对象锁与 WORM 对象** | 锁定中的对象不应被迁移（元数据变化涉及锁状态）；WORM 对象需保留原始存储 blob |
| **版本化桶的 Transition** | 每个版本独立评估 transition 规则；`NoncurrentVersionTransition` 从非当前版本开始计时 |
| **跨后端 Transition 中断** | 源 blob 删除前失败→状态为 `pending` 等待重试；blob 已删除但 metadata 未更新→孤儿 blob + metadata 不一致 |
| **大对象 Transition（>5GB）** | 使用 multipart copy（S3）或分段 streaming（Local）避免内存 OOM；记录进度到 `transition_queue` |
| **批量迁移风暴** | 限制每轮扫描 `maxTransitions=100`；多个租户/桶规则并发时使用 JobPool 限制 worker 数 |
| **成本模型冲突** | 频繁的 STANDARD→STANDARD_IA→STANDARD 来回转换会产生不必要的数据传输费用——规则设计时应防抖动（min transition interval >= 30 天） |

---

## 方向二：标签驱动自动化引擎

### 产品价值

标签是对象存储生态中的关键杠杆——一个支付级的事件/策略元面。当前标签是纯被动的"附着物"；标签驱动自动化将其升级为主动执行引擎：

| 场景 | 当前体验 | 自动化后 |
|------|---------|---------|
| **临时文件自动清理** | 人工定期巡检或外部 cron | `tag:temp=true` → 7 天后自动删除 |
| **合规自动保留** | 逐对象设置 Legal Hold | `tag:retain=legal` → 自动激活 Lock |
| **存储分层** | 基于时间（所有对象一体） | `tag:tier=cold` → 立即降冷到 STANDARD_IA |
| **事件路由** | 所有事件广播到所有 webhook | `tag:notify=audit` → 仅路由到审计 Webhook |
| **访问控制** | 基于桶策略（静态） | `tag:classification=PII` → 限制下载权限 |
| **标签继承** | 每个对象单独打标，子路径无自动传递 | `tag:project=foo` 从前缀自动继承 |

### 现状

**1. 标签 CRUD 完整，但无副作用触发机制**

```go
// internal/service/file_features.go:SetTags
func (s *FileService) SetTags(ctx context.Context, tenant, bucket, key string, tags map[string]string) error {
    // 纯 CRUD：validate → repo.SetObjectTags → return
    // ❌ 无 TagSet 事件发布
    // ❌ 无标签规则评估
    // ❌ 无标签继承传播
}
```

**2. 标签存储在 `objects.tags` JSON 列，但无索引**

```sql
-- migrations/sqlite/0005_versioning_tagging.up.sql
ALTER TABLE objects ADD COLUMN tags TEXT;  -- TEXT 存储 JSON，无 GIN 索引
```

Postgres 中 JSONB 支持索引（`GIN`），但 SQLite 无原生 JSON 索引——`ListObjectsByTag` 的 SQL 使用 `json_extract` 无法利用索引。

**3. 事件总线无法按标签过滤**

```go
// internal/events/bus.go:Publish — 广播给所有订阅者，无过滤谓词
func (b *Bus) Publish(ctx context.Context, e repository.Event) {
    b.broadcast(e)  // 无过滤
}
```

**4. Bucket Policy 不支持 `s3:ExistingObjectTag` 条件**

```go
// internal/auth/policy.go 支持的 Condition 集合
// 支持：StringEquals, StringLike, IpAddress, NotIpAddress
// ❌ 不支持 s3:ExistingObjectTag
```

### 架构权衡

**建议方案：标签引擎三层架构**

```
                    ┌─────────────────────┐
                    │  TagRule Registry    │ ← 管理标签规则 CRUD
                    │  (内部或外部存储)     │
                    └──────────┬──────────┘
                               │
         ┌─────────────────────┼─────────────────────┐
         ▼                     ▼                     ▼
 ┌──────────────┐    ┌──────────────┐    ┌──────────────┐
 │ TagMatcher   │    │ TagTrigger   │    │ TagPropagator│
 │ (规则评估)    │    │ (动作调度)    │    │ (标签继承)    │
 └──────────────┘    └──────────────┘    └──────────────┘
```

**1. TagRule 数据模型：**

```go
type TagRule struct {
    ID       string
    Name     string
    Tenant   string   // "" = 全局规则
    // 触发条件
    Match    TagMatchCondition
    // 触发时机：on_create | on_tag_change | periodic_check
    Trigger  string
    // 执行动作
    Actions  []TagAction
    Priority int      // 冲突时优先级
}

type TagMatchCondition struct {
    // 所有条件 AND 关系
    TagKey     string            // key 匹配（= / prefix / regex）
    TagValue   string            // value 匹配
    Prefixes   []string          // 对象 key 前缀过滤
    Buckets    []string          // 桶过滤
}

type TagAction struct {
    Type string  // set_lock | schedule_delete | set_storage_class |
                 // tag_inherit | set_metadata | webhook_event | email_alert
    Config map[string]any
}
```

**2. 规则评估触发器：**

| 触发器 | 触发时机 | 实现方式 |
|--------|---------|---------|
| `on_create` | 对象创建时 | `service.Put` 路径增加 `evaluateTagRules` 调用 |
| `on_tag_change` | 标签发生变更时 | `SetTags` / `DeleteTags` 路径增加规则评估 |
| `periodic` | 定期扫描 | Reconcile 循环中检查：所有已存在的对象 + 规则引擎 |

**3. 标签传播（Tag Propagation）：**

```go
// 当对象创建在 /projects/foo/docs/report.pdf 时
// 若前缀 /projects/foo/ 上有 project=foo、team=eng 标签
// 新对象自动继承这些标签
type TagPropagationPolicy struct {
    SourcePrefix    string   // 源前缀（继承的来源）
    IncomingTags    []string // 从上游继承的标签 key 列表
    Override        bool     // 是否允许对象级标签覆盖继承值
}
```

**实现路径：**

| Phase | 内容 | 代码位置 | 影响 |
|-------|------|---------|------|
| **P1** | TagRule 数据模型 + CRUD API + 规则评估引擎（`on_create` 触发器） | `repository/tag_rules.go`（新）、`api/rest/tag_rules.go`（新） | ~400 行 |
| **P2** | `on_tag_change` 触发器 + 已有动作（set_lock, schedule_delete, set_storage_class） | `service/tag_automation.go`（新）、`reconcile/tag_eviction.go`（新） | ~300 行 |
| **P3** | 标签传播 + Bucket Policy `s3:ExistingObjectTag` + 事件路由过滤 | `service/tag_propagation.go`（新）、`auth/policy.go` 扩展 | ~250 行 |

**与 Lifecycle 的关系：**

标签驱动的生命周期可以 AND 与基于时间的生命周期规则共存。冲突处理：

```
判定逻辑：
1. 若 tag_rule 要求"删除"且 lifecycle 要求"保留"→ 默认 min(保留期限)
2. 若 tag_rule 要求"STANDARD_IA"且 lifecycle 要求"GLACIER"→ 默认更冷的类
3. 可通过 Priority 字段显式覆盖
```

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **标签规则风暴**（100 个新对象同时创建，每条规则评估成本高） | 批量规则评估（在一次 SQL 查询中获取所有匹配规则）；`on_create` 评估控制在 5ms 以内 |
| **递归标签传播** | 限制传播深度（max 3 层）；检测循环引用（A → B → C → A）并终止 |
| **标签值含有敏感信息** | 标签值不清洗；提供标签值审计日志（谁、何时、设置了什么标签值） |
| **标签数量超限** | 单对象 max 10 个标签；单租户 max 100 个标签 key；到达上限时返回 `ErrInvalidArgs` |
| **并发 SetTags 冲突** | 使用 `updated_at` 乐观锁：SET tags = ..., updated_at = now() WHERE updated_at = old |
| **标签规则依赖关系** | 规则 A 要求对象具有标签 X，规则 B 在对象获得标签 X 时触发——确保有序评估（按 Priority + CreatedAt 排序） |

---

## 方向三：调度任务与定时作业框架

### 产品价值

当前所有周期性任务通过 `time.Ticker` 固定间隔执行，运维无法：
- 在特定时间（凌晨 3 点）执行批处理
- 设置一次性未来任务（如"明早 8 点删除这个对象"）
- 在周末降低后台作业的并发度
- 查看当前有哪些定时作业在运行及其状态
- 暂停/恢复/立即触发某个作业

**典型运维场景对比：**

| 需求 | 当前方案 | 有调度框架后 |
|------|---------|------------|
| "每晚 2 点全量数据完整性扫描" | 无法实现（扫描间隔是全局 Reconcile 周期） | `cron:"0 2 * * *"` |
| "这个 100GB 对象 7 天后自动删除" | 无法实现（需外部 cron job 调用 DELETE API） | `ScheduledJob{executeAt: now+7d, action: delete}` |
| "周末降低后台 IO 优先级" | 无法实现 | `ScheduleRule{weekday: sat/sun, maxWorkers: 2}` |
| "查看当前 Reconcile 执行状态" | 无法实现（无暴露接口） | `GET /v1/admin/schedules` |

### 现状

**1. 所有后台循环使用硬编码 Ticker 模式：**

```go
// internal/reconcile/job.go — Reconcile
func (j *Reconcile) Run(ctx context.Context) {
    t := time.NewTicker(j.interval)
    defer t.Stop()
    j.maybeRun(ctx)
    for {
        select {
        case <-ctx.Done(): return
        case <-t.C: j.maybeRun(ctx)  // 固定间隔，不可变
        }
    }
}

// internal/reconcile/lifecycle.go — LifecycleJob（同模式）
// internal/reconcile/retention.go — RetentionJob（同模式）
// internal/events/webhook.go:RetryLoop — 15s 硬编码固定轮询
```

**2. JobPool 无未来调度能力：**

```go
// internal/jobs/jobs.go:Queue.Enqueue — 创建时立即执行或在失败后延迟重试
func (q *Queue) Enqueue(ctx context.Context, job repository.Job) error {
    // job.ScheduledAt 字段不存在——所有 job 加入队列后马上被 worker 领取
}
```

`repository.Job` 结构体缺乏 `ScheduledAt`（计划执行时间）字段，使得 JobPool 只能做"即时执行"或"失败后重试"两种模式。

**3. 无调度管理 API：**

```go
// internal/api/rest/admin_jobs.go — 现有管理 API
// ListJobs — 列出作业
// RetryJob — 重试失败作业
// ❌ 无 CreateScheduledJob
// ❌ 无 CancelScheduledJob
// ❌ 无 ListSchedules
// ❌ 无 PauseSchedule / ResumeSchedule
```

### 架构权衡

**建议方案：Scheduler 组件**

```
┌────────────────────────────────────────┐
│              Scheduler                  │
│  ┌──────────┐  ┌────────────────────┐  │
│  │ Cron     │  │  One-shot Queue    │  │
│  │ Registry │  │  (scheduled_jobs)  │  │
│  └────┬─────┘  └─────────┬──────────┘  │
│       │                  │             │
│       ▼                  ▼             │
│  ┌──────────────────────────────────┐  │
│  │         Tick Dispatcher          │  │
│  │   (最小调度精度 1 秒)             │  │
│  └──────────────┬───────────────────┘  │
└─────────────────┼──────────────────────┘
                  │
                  ▼ 触发 handler
          ┌──────────────┐
          │  JobPool      │
          │  (执行者)      │
          └──────────────┘
```

**1. 数据模型：**

```sql
-- migration: scheduled_jobs 表
CREATE TABLE scheduled_jobs (
    id INTEGER PRIMARY KEY,
    tenant_id TEXT NOT NULL DEFAULT 'default',
    name TEXT NOT NULL,                  -- 命名（如 "nightly-scrub"）
    description TEXT,
    cron_expr TEXT,                      -- cron 表达式（周期性）或 NULL（一次性）
    execute_at TEXT,                     -- 一次性任务的执行时间
    job_type TEXT NOT NULL,              -- 作业类型（JobPool handler 名称）
    job_payload TEXT,                    -- JSON 参数
    max_attempts INTEGER NOT NULL DEFAULT 3,
    status TEXT NOT NULL DEFAULT 'active',  -- active | paused | completed | failed | cancelled
    last_run_at TEXT,
    last_error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
```

**2. Cron 表达式支持（最小精度：分钟级）：**

```go
type CronParser interface {
    Parse(expr string) (CronSchedule, error)
}

type CronSchedule interface {
    // Next 返回 expr 之后的下一个执行时间
    Next(after time.Time) time.Time
}
```

可选用 `robfig/cron`（标准库外唯一推荐依赖）或手写一个极简的 5 字段 cron 解析器（minute hour day month weekday）。

**3. 后台架构迁移（Phase 计划）：**

| Phase | 内容 | 影响 |
|-------|------|------|
| **P1** | `scheduled_jobs` 表 + Scheduler 组件（cron + one-shot 调度）+ `Reconcile`/`Lifecycle`/`Retention` 迁移到 Scheduler | 三个阶段移入调度框架 |
| **P2** | Admin API：`POST /v1/admin/schedules`、`GET /v1/admin/schedules`、`DELETE /.../schedules/{id}`、`POST .../{id}/trigger` | 运维管理能力 |
| **P3** | 反压感知调度：当后端延迟升高时自动降低调度频率、跳过非关键作业 | 弹性运维 |

**现有作业迁移示例：**

```go
// Phase 1 后的 main.go（简化）
scheduler.RegisterCron("0 */15 * * * *", reconcileJob.RunOnce)  // 每 15 分钟
scheduler.RegisterCron("0 2 * * *",     nightlyScrub.RunOnce)   // 每晚 2 点
scheduler.RegisterCron("*/30 * * * *",  webhookRetryLoop.RunOnce)// 每 30 秒
```

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **cron 表达式无效** | 注册时验证并返回错误；不合法表达式阻止注册 |
| **已错过的调度** | Scheduler 启动时检查 `execute_at < now` 且 `status='active'` 的一次性任务——跳过（或标记为 `missed`） |
| **集群模式下重复触发** | 结合 `cluster.Singleton`：只有一个 replica 持有调度锁，避免多副本同时触发同一任务 |
| **夏令时/时区** | cron 表达式基于 UTC（默认）；记录 `timezone` 字段支持非 UTC 时区 |
| **夏令时跳变** | UTC 无跳变问题；若使用其他时区，在跳变时不会重复或丢失调度 |
| **大量 scheduled_jobs 行** | 已执行完毕的一次性任务自动清理（TTL 7 天）；周期性任务保留最近 100 次执行记录 |
| **系统时钟偏差** | 多副本间时钟误差可能导致调度窗口漂移——建议 NTP 同步 + `execute_at ± 2s` 容忍窗口 |

---

## 方向四：跨协议对象身份与统一引用模型

### 产品价值

当前四协议虽共享 `FileService`，但每个协议有自己的路径、认证、引用语义。这导致：

| 场景 | 当前问题 | 统一模型后 |
|------|---------|-----------|
| **审计追踪** | S3 上传 → REST 下载 → WebDAV 编辑：三个请求在审计日志中无法关联 | 统一 `object_id` 跨协议追踪 |
| **运维排障** | 用户报 "文件读不了"——无法确定是通过哪个协议操作的 | 每次请求记录 `via_protocol` 和规范路径 |
| **SDK 一致性** | 三套 SDK 对同一对象的引用方式不同 | 规范 URI 生成函数 |
| **MCP 资源模型** | `listFiles` 返回的 key 需手动拼接为 REST 路径 | 返回规范对象 URI |
| **事件通知** | 事件 payload 中 `key` 字段协议相关 | 附加规范 URI + 协议别名列表 |

### 现状

**1. 存储 key 内部唯一，但外部引用四分五裂：**

```go
// internal/service/file.go:storageKey — 存储层唯一 key
func storageKey(tenant, bucket, key string) string {
    return path.Join(tenant, bucket, key)  // 内部唯一，但协议层不引用此值
}
```

**2. 每条协议独立的路径模式：**

| 协议 | 对象路径模式 | 示例 |
|------|-------------|------|
| REST | `/v1/files/{bucket}/{key}` | `GET /v1/files/docs/report.pdf` |
| S3 | `/{bucket}/{key}` | `GET /s3/docs/report.pdf` |
| WebDAV | `{prefix}/{bucket}/{key}` | `PROPFIND /dav/docs/report.pdf` |
| MCP | `aero-vault://{bucket}/{key}` | `read_file("docs/report.pdf")` |
| 审计日志 | 存储为 `tenant/bucket/key` | 无协议来源字段 |
| 事件 | 存储为 `bucket/key` | 无协议来源字段 |

**3. 审计日志无协议来源：**

```go
// internal/repository/audit.go
type AuditEntry struct {
    TenantID   string
    Action     string
    Resource   string  // "tenant/bucket/key"
    // ❌ 无 Protocol 字段
    // ❌ 无 RequestID 字段
}
```

**4. 无跨协议请求追踪：**

虽然 `RequestID` 中间件为每个请求生成唯一 ID，但：
- 不跨请求传播（S3 上传的 `RequestID` 无法关联到后续 REST 下载）
- 不包含在事件 payload 中
- 不作为对象元数据的一部分

### 架构权衡

**建议方案：规范对象引用模型**

```
                  ┌─────────────────┐
                  │ Canonical Object│
                  │ Reference       │
                  │ aero://{tenant}/│
                  │   {bucket}/{key}│
                  └────────┬────────┘
                           │
          ┌────────────────┼────────────────┐
          ▼                ▼                ▼
   ┌────────────┐  ┌────────────┐  ┌────────────┐
   │ REST:      │  │ S3:        │  │ MCP:       │
   │ /v1/files/ │  │ /{bucket}/ │  │ aero-vault:│
   │ {key}      │  │ {key}      │  │ //{bucket} │
   └────────────┘  └────────────┘  └────────────┘
```

**1. 规范对象 URI：**

```go
// internal/service/object_ref.go（新文件）

// ObjectRef 是协议无关的规范对象引用。
//
// 格式: aero://{tenant}/{bucket}/{key}
// 其中 key 使用 URL 编码（/ 保留为路径分隔符，其余字符按 RFC 3986 编码）
type ObjectRef struct {
    Tenant string
    Bucket string
    Key    string
}

func (r ObjectRef) String() string {
    return fmt.Sprintf("aero://%s/%s/%s", r.Tenant, r.Bucket, url.PathEscape(r.Key))
}

func ParseObjectRef(s string) (ObjectRef, error) { ... }
func (r ObjectRef) RESTPath() string              { return fmt.Sprintf("/v1/files/%s/%s", r.Bucket, r.Key) }
func (r ObjectRef) S3Path() string                { return fmt.Sprintf("/%s/%s", r.Bucket, r.Key) }
func (r ObjectRef) MCPSession() string            { return r.Key }  // MCP 工具以 key 为参数
```

**2. 对象元数据中存储规范 ID：**

```go
// 在 repository.Object 中新增
type Object struct {
    // ... 现有字段
    CanonicalRef string  // "aero://tenant/bucket/key"（唯一标识，可用于跨协议关联）
}
```

可通过现有 `id`（自增主键）实现，但 `CanonicalRef` 提供业务可读的协议无关引用。

**3. 审计日志增强：**

```sql
-- migration: audit_log 增加协议和请求关联字段
ALTER TABLE audit_log ADD COLUMN protocol TEXT;  -- "rest" | "s3" | "webdav" | "mcp"
ALTER TABLE audit_log ADD COLUMN request_id TEXT;  -- 对应 X-Request-ID
ALTER TABLE audit_log ADD COLUMN canonical_ref TEXT;  -- aero://tenant/bucket/key
```

**4. 事件 payload 增强：**

```go
type Event struct {
    // ... 现有字段
    CanonicalRef string  // 规范对象引用
    Protocol     string  // 事件来源协议（仅首次事件，后续触发不覆盖）
    RequestID    string  // 触发事件的初始请求 ID
}
```

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **key 含非法 URL 字符** | `url.PathEscape`/`PathUnescape` 确保往返安全；`aero://` URI 中 `key` 段内的 `/` 表示路径分隔符，不转义；其他字符如空格/中文按标准百分号编码 |
| **向后兼容** | 现有协议路径继续工作；规范 URI 在新事件 payload 和审计日志中作为附加字段引入，不影响现有调用者 |
| **跨协议 RequestID 传播** | 通过特定 HTTP 头（如 `X-Aero-Request-Chain`）在客户端侧串联——非强制，但服务端为每个协议操作生成 `request_id` 并记录 |
| **WebDAV 的协议标识** | WebDAV 在 chi 外独立分发，需确保 `dispatcher` 在转发前注入协议来源标记（通过 context） |
| **MCP stdio 模式** | Stdio 模式无 HTTP 管道——规范引用作为工具参数的一部分传递；MCP 工具响应中返回规范 URI |
| **多版本对象引用** | `aero://tenant/bucket/key@v{versionID}` 区分版本（可选）；不带 `@v` 引用当前版本 |

---

## 方向五：数据完整性自愈框架

### 产品价值

作为对象存储系统，数据的持久性和完整性是核心 SLA。当前系统：

| 能力 | 当前状态 | 有自愈框架后 |
|------|---------|-------------|
| **位衰减（bit rot）检测** | 仅 Reconcile Scrub 验证 MD5（被动、低频） | 后台定期全量扫描 + 智能抽样 + 热路径 on-read 验证 |
| **修复能力** | 检测到损坏仅打标记（`_aero_scrub_status=corrupt`） | 自动从副本/Replication 目标/纠删码修复 |
| **范围评估** | 单对象级检测，无法回答"损坏了多少个对象" | 完整性积分卡（按 tenant/bucket/storage backend） |
| **SLA 可证明性** | 无数据持久性指标 | `storage.durability_ratio` / `integrity_score` 仪表盘 |
| **渐进式检测** | 全表扫描（高 IO）或等待 GET 触发 | 热数据热扫描 + 冷数据冷扫描 + 随机抽样 |

### 现状

**1. Scrub 只能检测，不会修复：**

```go
// internal/reconcile/scrub.go
func (j *Reconcile) scrubObject(ctx context.Context, obj repository.Object) error {
    // 读取对象 → 计算 MD5 → 对比存储的 _aero_content_md5
    // 不匹配 → 设置 _aero_scrub_status = corrupt（只标记，不修复）
    // ✅ 检测
    // ❌ 修复
}
```

**2. GET 路径无 on-read 校验：**

```go
// internal/service/file_crud.go:Get
func (s *FileService) Get(ctx context.Context, tenant, bucket, key string) (io.ReadCloser, repository.Object, error) {
    // ...
    rc, _, err := s.store.Get(ctx, obj.StorageKey)
    // 直接返回 rc——不计算 ETag，不与存储的 ETag 比对
    // ❌ 无 on-read 校验
}
```

**3. 无 redundancy/repair 机制：**

系统没有纠删码（erasure coding）、没有多副本冗余（multi-mirror）、没有从 replication 目标自动修复的能力。虽然 `Replication` worker 维护了副本，但没有利用它做完整性修复。

**4. 无位衰减检测能力：**

S3 后端默认提供 AWS 的完整性保护（`x-amz-sha256` 自动校验），但 Local/OSS/COS 后端无此能力。对于 Local 存储后端，磁盘 bit rot 是真实风险（尤其是 HDD + 大对象）。

### 架构权衡

**建议方案：三层完整性框架**

```
┌──────────────────────────────────────────────────────────┐
│              IntegrityManager                             │
├──────────────────────────────────────────────────────────┤
│  ┌─────────────────┐  ┌──────────────────────────────┐  │
│  │ Detection Layer  │  │ Repair Layer                 │  │
│  │                  │  │                              │  │
│  │ • On-read verify │  │ • From replica restore       │  │
│  │ • Periodic scan  │  │ • From replication restore   │  │
│  │ • Bit-rot probe  │  │ • Re-mark as healthy         │  │
│  │ • Sampling scan  │  │ • Alert on irreparable       │  │
│  └─────────────────┘  └──────────────────────────────┘  │
├──────────────────────────────────────────────────────────┤
│              Integrity Scorecard (Tenant-level)          │
└──────────────────────────────────────────────────────────┘
```

**1. 检测层（Detection Layer）：**

| 检测方法 | 触发时机 | 覆盖率 | 开销 |
|---------|---------|--------|------|
| **On-read verify** | 每次 `svc.Get` | 仅被读取的对象 | 低（额外的 checksum 计算） |
| **Full scan** | 夜间 cron（每日/每周） | 全部活跃对象 | 高（需要扫描全部 blob） |
| **Smart sampling** | 定时（每小时） | 按桶/前缀等比抽样 | 低→中 |
| **Bit-rot probe** | 定时（每日） | Local 后端对象 | 中（读取全对象） |

```go
// 在 service.Get 中增加 on-read 校验
func (s *FileService) Get(ctx context.Context, tenant, bucket, key string) (io.ReadCloser, repository.Object, error) {
    // ... 现有逻辑
    rc, _, err := s.store.Get(ctx, obj.StorageKey)
    // 新增：on-read 校验
    if s.integrity.OnReadVerify {
        rc = s.integrity.verifyReader(ctx, rc, obj)  // 包装 Reader，读取结束时计算 ETag
    }
    return rc, obj, nil
}
```

On-read 校验使用 `io.TeeReader` + `hash.Hash`，在流式传输的同时计算 checksum：

```go
func (im *IntegrityManager) verifyReader(ctx context.Context, r io.ReadCloser, obj Object) io.ReadCloser {
    h := md5.New()
    tee := io.TeeReader(r, h)
    return &verifyingReader{
        Reader: tee,
        closer: r,
        onClose: func() error {
            if obj.ETag != "" {
                got := hex.EncodeToString(h.Sum(nil))
                if got != obj.ETag {
                    im.recordCorruption(ctx, obj, got)
                    return ErrObjectCorrupt
                }
            }
            return nil
        },
    }
}
```

**2. 修复层（Repair Layer）：**

| 修复策略 | 前置条件 | 实现方式 |
|---------|---------|---------|
| **从 Replication 恢复** | 目标后端存在完整副本 | `storage.Copy(replica_key, primary_key)` |
| **从 Replica 后端恢复** | Replica 配置了 `REPLICATION_ENABLED` | 反转 replication 方向 |
| **从事件日志重建** | 事件日志包含最新 PUT 事件 | 事件回放（弱一致性，不推荐） |
| **标记不可修复** | 无可用副本 | `set _aero_scrub_status=unrepairable` + 告警 |

**3. 完整性积分卡（Integrity Scorecard）：**

```go
type IntegrityReport struct {
    Tenant             string
    TotalObjects       int64
    VerifiedHealthy    int64
    VerifiedCorrupt    int64
    VerifiedUnrepairable int64
    PendingVerification  int64
    LastFullScanAt     time.Time
    RepairSuccessCount int64
    RepairFailedCount  int64
    EstimatedDurability float64  // 9 的个数（如 99.9999%）
}
```

**4. 低开销扫描策略：**

全量扫描所有对象是最坏方案。优化策略：

| 策略 | 适用场景 | 扫描量 | 检测延迟 |
|------|---------|--------|---------|
| 全量扫描（夜间） | 首次部署 / 季度审计 | 100% | 24h |
| 等距抽样（每小时） | 持续监控 | ~1%/h | ~1h 检出率 99% |
| 热数据优先（按 LastAccessedAt） | 频繁读取的数据 | ~10%/h | 分钟级 |
| 写入时验证（Write-Verify） | 新写入对象 | 写入路径 100% | 实时 |

`Write-Verify` 模式：PUT 写入成功后立即回读并验证 checksum，确保存储后端的持久化层未静默损坏数据：

```go
// 可选的 Write-Verify 模式（P1 on-read 后的增量）
type WriteVerifier interface {
    VerifyAfterWrite(ctx context.Context, key string, expectedETag string) error
}
```

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **SSE 加密对象 on-read 校验** | 必须在解密后计算 checksum（明文），而非加密 blob | 
| **ETag 为空或不一致（multipart 对象）** | Multipart ETag 通常是 `"{partETags hash}-{partCount}"` 格式，解析后计算实际 MD5 与分片列表一致 |
| **Scrub 发现损坏但 replication 目标也已损坏** | 两个副本 checksum 不一致时不可自动修复——保留两个副本，标记 `unrepairable`，运维介入 |
| **修复过程中的 I/O 压力** | 单次修复最大并行数 = 1/4 总 worker；使用 `jobPool` 控制修复并发；监控 `storage.io_read_bytes` 自动降速 |
| **自愈触发写放大** | 修复过程需要读取副本、写入主存储，会产生写放大。权衡：`maxRepairsPerCycle=20` + 每天报告 |
| **增量修复 vs 全量重写** | 检测到单对象损坏后：从副本修复（最小代价）；副本不存在→请求客户端重新上传（next best） |
| **报告的完整性 vs 真实性** | `IntegrityReport` 基于最后成功验证的时间——如果一个对象 90 天未读，其状态为 `PendingVerification` 而非 `Healthy`，诚实反映不确定性 |

---

## 优先级与建议执行顺序

| 排序 | 方向 | 前置依赖 | 建议投入 | 核心交付物 |
|------|------|---------|---------|-----------|
| **1** | **方向一：存储分层迁移引擎**（Phase 1：`Transition` XML 解析 + 本地分层 + S3 class 更新） | `LifecycleJob` 已就绪；`StorageClass` 字段已存在 | 2-3 周 | `lifecycleRule.Transition` 结构体 + `TransitionManager` + 迁移 Job + REST取回 API |
| **2** | **方向五：数据完整性自愈框架**（Phase 1：On-read 校验 + 智能扫描 + 积分卡） | `reconcile.Scrub` 已就绪；ETag 已存储 | 2-3 周 | `IntegrityManager` + 验证 Reader + 抽样扫描 + 指标 + `GET /v1/admin/integrity` |
| **3** | **方向二：标签驱动自动化引擎**（Phase 1：TagRule 数据模型 + `on_create` 触发器 + `set_lock`/`schedule_delete` 动作） | 标签 CRUD 已完整实现；EventBus 已就绪 | 3-4 周 | `TagRule` CRUD API + 规则评估引擎 + `on_create` 触发器 + 两个初始动作 |
| **4** | **方向三：调度任务框架**（Phase 1：`scheduled_jobs` 表 + Scheduler 组件 + Reconcile/Lifecycle/Retention 迁移） | `JobPool` 已就绪；现有作业可随时接入 | 3-4 周 | `scheduled_jobs` 表 + cron 解析 + Scheduler goroutine + 管理 API |
| **5** | **方向四：跨协议对象身份**（Phase 1：规范 `ObjectRef` 类型 + 审计日志增强 + 事件 Payload 增强） | 无前置依赖 | 1-2 周 | `ObjectRef` 类型 + 审计日志 migration + 事件 payload 扩展 |

**建议执行策略：**

1. **Phase 1（方向一 + 方向五并行）**：分层迁移是直接成本优化，数据完整性是核心 SLA——两个正交维度，可同时投入。分层需要少量新功能（Transition），完整性是对现有路径的增强（wrapper）。
2. **Phase 2（方向二 + 方向三并行）**：标签自动化和调度框架都是"策略引擎"——标签是条件策略，调度是时间策略。两者共享规则评估模式。
3. **Phase 3（方向四）**：跨协议身份是"胶水层"，为所有其他方向提供统一的引用基础。可以作为单独的短期交付穿插在任何阶段。

---

## 总结

以上五个方向覆盖了 aero-vault 在**成本优化（分层迁移）、可靠性（数据自愈）、产品杠杆（标签自动化）、基础设施（调度框架）、架构一致性（跨协议身份）** 五个维度的关键缺口。每个方向均有明确代码锚点，从前 95 轮分析中未被深度覆盖，并具备从当前架构渐进演进的可行性。

| 维度 | 当前状态 | 优化后 |
|------|---------|--------|
| **存储成本** | 所有对象固定 STANDARD 层，无分层能力 | 自动 STANDARD→STANDARD_IA→GLACIER 分层，存储成本降低 40-70% |
| **数据可靠性** | 可检测损坏（Scrub）但无法修复 | On-read 校验 + 智能扫描 + 自动修复 + 完整性积分卡 |
| **产品深度** | 标签是被动元数据 | 标签是主动策略引擎：触发锁定/分层/过期/通知 |
| **运维能力** | `time.Ticker` 硬编码间隔 | 完整的 cron + one-shot 调度框架 + 管理 API |
| **协议一致性** | 4 种协议 4 套引用路径 | 规范 `ObjectRef` URI + 跨协议审计追踪 |
