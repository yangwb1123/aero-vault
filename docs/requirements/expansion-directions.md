# AeroVault 高价值扩展方向（第二期）

> **视角:** 资深架构师 / 产品经理  
> **方法:** 全局代码扫描（Go 源码 ~45K 行），第八轮评估后的补充分析  
> **日期:** 2026-07-10  
> **原则:** 选取 **ROADMAP + 八轮 analysis-v[1-8] + 第一期 expansion-directions 均未覆盖**的方向。每个方向附带具体代码位置、架构蓝图和实现理由。

---

## 总览

| # | 方向 | 类型 | 影响 | 当前状态 | 覆盖情况 |
|---|------|------|------|---------|---------|
| 1 | **批量异步操作引擎（S3 Batch Ops 模式）** | 平台能力 | 🟠 企业运维必备 | 零实现 | 仅一期#4 函数引擎有重叠，但方向不同 |
| 2 | **合规对象锁（GOVERNANCE/COMPLIANCE 模式 + 保留期限绕过）** | 合规/安全 | 🛑 金融监管硬性要求 | `locked_until` 字段存在但无模式区分 | 首期#3 法律封存涵盖面不同，锁模式未覆盖 |
| 3 | **服务端对象分析查询（S3 Select 模式）** | 差异化 | 🟠 从存储进化为数据平台 | 零实现 | 全系列未覆盖 |
| 4 | **存储后端多镜像同步写入（RAID-1 式持久化）** | 架构/可靠性 | 🟠 最高持久性保障 | 零实现 | 仅 v2 浅提 RAID 恢复，涵盖面不同 |
| 5 | **协作分享链接（受控预签名 URL + 安全策略）** | 用户体验/协作 | 🟠 企业协作高频需求 | 仅基础 `PresignGet/Put` | 全系列未覆盖 |

---

## 1. 批量异步操作引擎

### 为什么需要它

当前所有对象操作都是**逐对象同步**的：用户需要遍历每个 key，逐一调用 PUT/DELETE/Tag/ACL。对于存储了数百万对象的企业租户，这是不可接受的运维负担。

S3 Batch Operations（批处理操作）是 AWS S3 的重要企业特性。它的核心模式是：

1. 指定一个过滤器（前缀、标签、大小、创建时间等的组合）或提供一个 manifest（key 列表）
2. 指定一个操作类型（复制、标签、ACL、恢复、删除）
3. 创建一个后台 job → 系统自动遍历匹配对象并执行操作
4. 提供进度追踪、完成通知、失败报告

当前代码库缺少：
- **无 manifest/清单管理**（`internal/repository/repository.go` 无 manifest 表）
- **无过滤遍历引擎**（`ListObjects` 分页存在但无组合过滤）
- **无批量作业的进度追踪 UI**（Web UI 无管理面板）
- **无批量操作的审计和完成报告**

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/jobs/jobs.go` | 成熟的 job 队列 + registry + pool + dedup | 无批量作业类型 |
| `internal/service/file_crud.go` | 单对象 CRUD 方法 | 无批量变体 |
| `internal/service/file_features.go:BatchDelete/BatchSetTags` | 批量删除/标签已存在 | 仅限 key 列表，无过滤，无异步 |
| `internal/repository/repository.go:ListObjects` | 支持按 prefix + marker 分页 | 无组合过滤（+tags +size +date）|
| `internal/repository/sql_objects.go` | SQL 查询仅支持 prefix 过滤 | 需要 `WHERE` 扩展 |
| `internal/webui/web.go` | 现有 Web UI | 无批量作业管理界面 |
| `internal/api/rest/admin_jobs.go` | 作业列表/重试 API 存在 | 无批量作业创建 API |

### 架构蓝图

```
┌─ Batch Job Model ──────────────────────────────────────────────┐
│ type BatchJob struct {                                         │
│     ID            string                                       │
│     TenantID     string                                        │
│     Operation    string   // "copy" | "tag" | "delete" |       │
│                           // "restore" | "acl" | "transition"  │
│     Filter       Filter                                        │
│     Manifest     Manifest   // inline key list OR S3-style CSV  │
│     Params       map[string]any  // per-operation parameters   │
│     Status       string   // "pending" | "running" |           │
│                           // "complete" | "failed"             │
│     Total        int64    // matched objects at creation       │
│     Completed    int64                                         │
│     Failed       int64                                         │
│     CreatedAt    time.Time                                     │
│     CompletedAt  *time.Time                                    │
│     NotifyURL    string  // completion webhook                 │
│ }                                                              │
│                                                                 │
│ type Filter struct {                                           │
│     Prefix       string                                        │
│     TagKey       string                                        │
│     TagValue     string                                        │
│     MinSize      int64    // bytes                             │
│     MaxSize      int64                                         │
│     CreatedAfter  string  // RFC3339                           │
│     CreatedBefore string                                       │
│     StorageClass  string                                       │
│ }                                                              │
└────────────────────────────────────────────────────────────────┘

┌─ API Surface ──────────────────────────────────────────────────┐
│ POST   /v1/admin/batch-jobs    → 创建批量作业（返回 job ID）     │
│ GET    /v1/admin/batch-jobs    → 列出批量作业 + 进度             │
│ GET    /v1/admin/batch-jobs/{id} → 作业详情+统计                 │
│ GET    /v1/admin/batch-jobs/{id}/failures → 失败项列表           │
│ DELETE /v1/admin/batch-jobs/{id} → 取消正在运行的作业            │
└────────────────────────────────────────────────────────────────┘

┌─ Execution Engine ─────────────────────────────────────────────│
│ 创建 → `job_queue.Enqueue(BatchJob)` → 专用 worker:             │
│   1. 应用 Filter 遍历匹配对象（分批 1000 个）                    │
│   2. 对每个匹配对象 enqueue 子 job（复用现有 JobDeleteChunks）   │
│   3. 更新进度: batch_jobs.completed/failed/total                 │
│   4. 完成后发送通知（POST NotifyURL）                            │
│   5. 生成失败报告 JSON                                          │
│                                                                 │
│ 子 job 类型（注册到 jobs.Registry）:                              │
│   batch_copy_object    → 单对象复制                              │
│   batch_tag_object     → 单对象打标签（复用 service.SetTags）     │
│   batch_delete_object  → 单对象软删除（复用 service.Delete）     │
│   batch_restore_object → 单对象恢复（复用 service.RestoreObject）│
│   batch_acl_object     → 单对象 ACL 更新                         │
│   batch_transition     → 单对象存储类流转（配合方向#2 第二期）   │
└────────────────────────────────────────────────────────────────┘

┌─ Manifest Support ─────────────────────────────────────────────│
│ 两种模式:                                                       │
│ 1. Filter-based: 系统遍历匹配的对象（适合大规模操作）             │
│ 2. Explicit CSV: 用户上传 manifest CSV（格式: key, versionId）  │
│    POST /v1/admin/batch-jobs/manifest → 接受 CSV 上传           │
│    验证每行对应的对象存在 → 创建 ManifestBatchJob                 │
└────────────────────────────────────────────────────────────────┘

┌─ Integration with Existing Components ────────────────────────│
│ 迁移表: batch_jobs (存储作业元数据)                               │
│ 迁移表: batch_job_failures (存储失败项 + 错误原因)               │
│ Web UI: Admin Dashboard → "Batch Operations" tab                 │
│ 复用: jobs.Pool, jobs.Registry, service.BatchDelete/BatchSetTags│
└────────────────────────────────────────────────────────────────┘
```

**边界情况：**
- 过滤条件变更风险：创建 job 时评估一次匹配数量，执行时可能漂移（新增/删除对象）。**策略**：使用快照（对匹配对象 ID 快照到 `batch_job_items` 表）
- 大规模清单（10M+ 对象）：不能 loaded in memory，应流式处理
- 批量操作**不**是原子事务：部分成功时报告失败项，不整体回滚
- 取消中的作业：添加 "cancelling" 状态 → 停止新子 job 入队，已完成的不回滚
- S3 Batch Ops 兼容的 CSV manifest 格式：`Bucket,Key,VersionId`（一行一个对象）

**复杂度:** L（复用 job 队列） · **用户影响:** ★★★★☆（企业运维） · **代码变更:** ~1200 行新代码 + ~400 行修改

---

## 2. 合规对象锁（GOVERNANCE/COMPLIANCE 模式 + 保留期限绕过）

### 为什么需要它

当前对象锁实现（`service/file_crud.go:295-301`）使用单一的 `locked_until` 时间戳和 `_aero_legal_hold` 元数据标记进行阻止删除检查。**这在企业合规场景中是不够的：**

- **没有锁模式区分**：S3 Object Lock 定义了两种模式——GOVERNANCE（管理员可在保留期内绕过）和 COMPLIANCE（任何人均不可绕过，包括 root）。不同监管场景需要不同的模式。
- **没有保留期限绕过 API**：GOVERNANCE 模式下，具有 `bypass-governance-retention` 权限的管理员应能通过 `x-amz-bypass-governance-retention: true` 头在保留到期前删除对象。
- **没有默认桶级保留策略**：桶配置 (`BucketConfig`) 支持 `ObjectLockSeconds` 但仅作为默认值，不能强制所有 PUT 都应用锁。
- **没有保留期限延长 API**：不能延长已存在对象的保留期限。

这对金融（SEC 17a-4）、医疗（HIPAA）、电子证据（FRCP）监管场景是准入级需求。没有完整的对象锁实现，面向金融/保险行业的采购会直接否决。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/service/file_crud.go:295-301` | `checkLockBeforeOverwrite` + 法律保留检查 | 无模式区分，无绕过路径 |
| `internal/service/file_crud.go:hardDeleteObject` | 检查 `LockedUntil` + `_aero_legal_hold` | 无 governance 绕过 |
| `internal/repository/repository.go:32` BucketConfig.ObjectLockSeconds | 桶级默认锁秒数 | 无法强制，无模式字段 |
| `internal/repository/sql_objects.go` | `locked_until` 列 | 无 `lock_mode` 列 |
| `internal/api/s3compat/bucketconfig.go` | 支持 `?lock` 子资源 | 仅返回/设置 seconds，缺失 mode |
| `internal/api/rest/router.go` | `/lock` 路由存在 | 无按模式锁管理路径 |

### 架构蓝图

```
┌─ Lock Mode Model ──────────────────────────────────────────────│
│ type LockMode string                                           │
│ const (                                                        │
│     LockModeGovernance LockMode = "GOVERNANCE"                 │
│     LockModeCompliance LockMode = "COMPLIANCE"                 │
│ )                                                              │
│                                                                 │
│ Object 扩展字段:                                                │
│   LockMode   LockMode   // 当前锁模式                            │
│   RetainUntil *time.Time // 保留到期时间（原 LockedUntil）       │
│   LegalHold  bool       // 独立于锁的法律封存标记                │
│                                                                 │
│ BucketConfig 扩展字段:                                           │
│   DefaultLockMode   LockMode  // 默认锁模式                     │
│   LockEnabled       bool      // 是否启用桶级锁（一旦启用不能禁用）│
│   LockRetentionDays int       // 默认保留天数                    │
└────────────────────────────────────────────────────────────────┘

┌─ Lock State Machine ───────────────────────────────────────────│
│                                                                 │
│  ┌─────────────┐                                               │
│  │ 未锁定对象   │ ←──── 新 PUT 或 PUT 无锁头                    │
│  └──────┬──────┘                                               │
│         │ PUT with x-amz-object-lock-mode + x-amz-object-lock-  │
│         │ retain-until-date                                     │
│         ▼                                                       │
│  ┌─────────────────────┐                                       │
│  │ GOVERNANCE 锁定      │ ←──── x-amz-object-lock-mode: GOVERNANCE│
│  ├─────────────────────┤                                       │
│  │ 可绕过:                │                                       │
│  │ 需要 bypass + admin 权限 │ → 硬删除成功                       │
│  │ 自动到期: RetainUntil 过期 │ → 自动解锁                       │
│  └─────────────────────┘                                       │
│                                                                 │
│  ┌─────────────────────┐                                       │
│  │ COMPLIANCE 锁定      │ ← x-amz-object-lock-mode: COMPLIANCE │
│  ├─────────────────────┤                                       │
│  │ 不可绕过:              │                                       │
│  │ 无任何 bypass 路径     │ → 硬删除永远阻塞                     │
│  │ 自动到期: RetainUntil 过期 │ → 自动解锁                       │
│  └─────────────────────┘                                       │
└────────────────────────────────────────────────────────────────┘

┌─ API 扩展 ─────────────────────────────────────────────────────│
│ PUT /v1/files/{key}/lock                                       │
│   { "mode": "GOVERNANCE", "retain_until": "2027-07-10T00:00:00Z" }         │
│   (x-amz-bypass-governance-retention: true 头可选)               │
│                                                                 │
│ GET /v1/files/{key}/lock → { mode, retain_until, legal_hold }   │
│                                                                 │
│ S3-compat 路由扩展:                                              │
│   PUT /s3/{bucket}/{key}?retention                              │
│     x-amz-object-lock-mode: GOVERNANCE|COMPLIANCE                │
│     x-amz-object-lock-retain-until-date: ...                    │
│     x-amz-bypass-governance-retention: true                      │
│                                                                 │
│   PUT /s3/{bucket}/{key}?legal-hold                              │
│     x-amz-object-lock-legal-hold: ON|OFF                         │
│                                                                 │
│   PUT /s3/{bucket}?object-lock                                   │
│     ObjectLockConfiguration → {Enabled, Rule (DefaultRetention)} │
└────────────────────────────────────────────────────────────────┘

┌─ 迁移需求 ─────────────────────────────────────────────────────│
│ SQLite + Postgres 迁移 0025:                                     │
│   ALTER TABLE objects ADD COLUMN lock_mode TEXT DEFAULT '';      │
│   ALTER TABLE objects ADD COLUMN legal_hold INTEGER DEFAULT 0;   │
│   ALTER TABLE buckets ADD COLUMN lock_enabled INTEGER DEFAULT 0; │
│   ALTER TABLE buckets ADD COLUMN default_lock_mode TEXT DEFAULT '';│
│   ALTER TABLE buckets ADD COLUMN lock_retention_days INTEGER DEFAULT 0;│
└────────────────────────────────────────────────────────────────┘

┌─ 与第一期方向#3（法律封存）的关系 ─────────────────────────────│
│ 法律封存 (Legal Hold) 和 对象锁 (Object Lock) 是互补的独立机制：  │
│ - 法律封存: 管理员手动触发，无到期时间，覆盖诉讼保全场景           │
│ - 对象锁: 通过 PUT API 自动触发，有到期时间，覆盖监管保留场景      │
│ - 任一激活时，对象都不可删除                                      │
│ - 本方向补齐了对象锁的模式区分和绕过能力                           │
└────────────────────────────────────────────────────────────────┘
```

**边界情况：**
- 启用桶级锁后，default 保留期限不能缩短（只能延长或不变）——S3 硬性要求
- COMPLIANCE 模式的对象即使**桶被删除**也不应被删除——需在 `DeleteBucket` 中检查
- 保留期限**延长**不受限（任何锁模式下都可以延长），但缩短受限制
- GOVERNANCE 模式的 bypass 操作必须记录不可变的审计日志（复用 `audit_log` 表）
- 生命周期策略不得删除锁定的对象（`lifecycle.go` 需加锁检查）

**复杂度:** M · **用户影响:** ★★★★★（合规准入） · **代码变更:** ~800 行新代码 + ~300 行修改

---

## 3. 服务端对象分析查询（S3 Select 模式）

### 为什么需要它

几乎所有对象存储服务最终都要面对同一个用户问题：**"如何从这些文件中提取我需要的数据？"** 当前方案是：用户 GET 整个对象、下载到本地、解析、过滤。这在以下场景中效率极低：

- 一个 2GB JSON 文件，用户只需要 `WHERE status="active"` 的 10KB 数据
- 一个 500MB CSV 日志文件，用户需要按时间范围过滤列
- 一个 Parquet 格式的分析文件，需要按维度聚合

S3 Select 允许用户发送 SQL 查询直接作用于存储内容，服务端执行过滤和投影后只返回结果。这同时节省**网络传输量、客户端 CPU、和查询延迟**。

当前代码库：
- **没有 SQL 解析/执行能力**（Go 标准库有 `encoding/csv`、`encoding/json`、但无 SQL 引擎）
- **没有流式分析模式**（所有对象读取都是全量 GET）
- **没有 Parquet/ORC 等列式格式支持**

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/service/range.go` | Range 请求支持字节范围 | 不支持行范围/SQL 过滤 |
| `internal/storage/storage.go:Get` | 全量对象读取 | 无谓词下推 |
| `internal/api/s3compat/router.go` | S3 路由注册 | 无 `?select` 路由 |
| `internal/ai/extractor.go` | 已有文件类型检测逻辑 | 可复用为 SQL 引擎的输入源 |
| `internal/ai/chunker.go` | 文本分块 | 无 SQL 行过滤 |

### 架构蓝图

```
┌─ SQL Engine ───────────────────────────────────────────────────│
│  Go 实现轻量 SQL 子集，零外部依赖:                                │
│                                                                 │
│  支持的语法:                                                     │
│    SELECT [column1, column2, ...]                               │
│    FROM S3Object                                               │
│    [WHERE condition]                                           │
│    [LIMIT count]                                               │
│    [OFFSET count]                                              │
│  不支持: JOIN, GROUP BY, ORDER BY（v1 简化）                     │
│                                                                 │
│  condition 支持:                                                │
│    =, !=, <, >, <=, >=                                         │
│    AND, OR, NOT                                                │
│    IN, BETWEEN, LIKE                                            │
│    IS NULL, IS NOT NULL                                         │
│    CAST (类型转换)                                               │
│                                                                 │
│  支持的输入格式:                                                 │
│    CSV   → encoding/csv 解析 → 列投影 + 行过滤                    │
│    JSON  → encoding/json 流式解析 → 字段投影 + 条件过滤           │
│    JSON Lines (每行一个 JSON 对象) → 流式过滤                     │
│  未来扩展:                                                       │
│    Parquet → github.com/parquet-go/parquet-go                    │
│    Apache Arrow → github.com/apache/arrow/go                     │
└────────────────────────────────────────────────────────────────┘

┌─ Architecture ─────────────────────────────────────────────────│
│ HTTP 请求: POST /s3/{bucket}/{key}?select                      │
│   Content-Type: text/csv                                       │
│   Body: {"Expression": "SELECT name, age FROM S3Object          │
│           WHERE age > 30", "ExpressionType": "SQL",             │
│          "InputSerialization": {"CSV": {"FileHeaderInfo":       │
│            "NONE"}},                                            │
│          "OutputSerialization": {"CSV": {}}}                    │
│                                                                 │
│ 处理流程:                                                       │
│   1. 解析 SelectRequest (XML/JSON)                              │
│   2. 编译 SQL → AST → 列投影器 + 行过滤器                        │
│   3. 流式读取对象（`storage.Get` → `io.Reader`）                │
│   4. 按格式解析输入（CSV/JSON 行）                               │
│   5. 应用投影（仅选择所需列）                                     │
│   6. 应用条件过滤（WHERE 子句）                                   │
│   7. 序列化输出（CSV/JSON）                                      │
│   8. 流式写入响应                                               │
│                                                                 │
│ 响应格式:                                                       │
│   200 OK                                                       │
│   Content-Type: text/csv                                        │
│   body: 过滤后的行                                              │
│   x-amz-select-results-total-bytes: 12345                       │
└────────────────────────────────────────────────────────────────┘

┌─ 集成路径 ─────────────────────────────────────────────────────│
│ go.mod: 添加 `gopkg.in/alecthomas/kingpin.v2` 或使用纯手工递    │
│ 归下降解析器（~200 行即可 cover 所需子集）                       │
│                                                                 │
│ 新包: `internal/select/`                                        │
│   parser.go      → SQL 词法/语法解析 (recursive descent)         │
│   executor.go    → 查询执行引擎                                  │
│   csv_reader.go  → CSV 行解析 + 列投影                           │
│   json_reader.go → JSON 流式解析 + 字段投影                      │
│   format.go      → 输入/输出序列化                              │
│                                                                 │
│ S3 兼容路由:                                                    │
│   internal/api/s3compat/router.go → 添加 select 路由             │
│   解析 `?select` 参数 → 委托到 select 包 → 流式返回              │
│                                                                 │
│ REST API 扩展:                                                  │
│   POST /v1/select/{bucket}/{key}                                │
│     { "query": "SELECT ...", "format": "csv" }                  │
└────────────────────────────────────────────────────────────────┘
```

**边界情况：**
- 大对象（>5GB）的 SELECT 必须使用流式处理，不得 buffer 全部内容到内存
- 嵌套 JSON：支持点号路径（`SELECT user.address.city`）
- 空结果集：正常返回 200 + 空 body + 适当的 `x-amz-select-results-total-bytes: 0`
- SQL 注入风险：查询来自用户输入，SQL 引擎只读不写，无注入风险
- 格式错误：输入 CSV 格式错误时失败优雅，返回错误行号
- 进度指标：添加 `select_scanned_bytes_total` + `select_returned_bytes_total` + `select_latency_ms`

**复杂度:** L-M · **用户影响:** ★★★★☆（数据分析场景） · **代码变更:** ~1500 行新代码 + ~200 行修改

---

## 4. 存储后端多镜像同步写入（RAID-1 式持久化）

### 为什么需要它

当前存储层设计允许**一个活跃后端**：`FileService` 通过 `storage.Storage` 接口操作单个后端（local / S3 / OSS / COS）。复制工人（`replication.Worker`）提供**异步、最终一致**的副本，但在以下场景中不够：

- **主存储故障**：主后端不可用时，系统需要立即切换到副本——当前复制是异步的，切换有数据丢失窗口
- **数据持久性**：单后端的持久性受限于该后端的设计（如 local FS -> 单盘故障即丢失数据）
- **写后读一致性**：在异步复制下，写入主存储但未完成复制时读副本会看到旧数据

解决方案：**同步多镜像**——每次 PUT 操作同时写入多个存储后端，所有镜像确认后才返回成功。这类似 RAID-1（镜像）模式。当前代码结构非常适合这个方向，因为：

- `storage.Storage` 接口已经抽象化（`contract_test.go` 提供确定性验证）
- 工厂模式 (`storage/factory.go`) 已支持创建多种后端
- `storage/circuitbreaker.go` 展示了包装模式的正确实现方式

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/storage/storage.go` | 清晰的 `Storage` 接口 | 无多后端的组合模式 |
| `internal/storage/factory.go` | 工厂创建单后端 | 无多后端配置路径 |
| `internal/storage/circuitbreaker.go` | 已有装饰器模式先例 | 可作为镜像包装器的模式参考 |
| `internal/replication/replication.go` | 异步复制工人 | 同步镜像完全不同的契约 |
| `internal/config/config.go` | 单 `Storage` 配置 | 需要 `StorageMirror` 配置节 |
| `storage/contract_test.go` | 存储后端合约测试 | 镜像组合模式也需要通过合约 |

### 架构蓝图

```
┌─ Mirror Storage Wrapper ───────────────────────────────────────│
│ type MirrorStorage struct {                                    │
│     primaries []Storage     // 必须全部成功的镜像                 │
│     readMirror Storage      // 读取目标（可以是任一 primary）     │
│     writeQuorum  int        // 成功需要的写入数，默认 len(all)    │
│     readStrategy ReadStrategy // 读取策略：round-robin /         │
│                               // latency-optimal / specific     │
│ }                                                              │
│                                                                 │
│ Write 语义:                                                     │
│   Put(ctx, key, r, size, opts):                                │
│     对所有 primaries 并行写入                                    │
│     等待 writeQuorum 个成功返回                                  │
│     如果 writeQuorum 未达到 → 返回 error + 需要清理（issue）     │
│     对已完成的后端无需回滚（幂等性保证）                          │
│                                                                 │
│ Read 语义:                                                      │
│   Get(ctx, key):                                               │
│     根据 readStrategy 选择后端读取                                │
│     失败时 fallback 到下一个镜像                                  │
│     所有镜像都失败 → 返回 ErrNotFound                            │
│                                                                 │
│ Consistency 保证:                                               │
│   同步写入: 所有镜像确认后 PUT 才返回                             │
│   写后读一致性: 读任一镜像都看到最新写入                           │
└────────────────────────────────────────────────────────────────┘

┌─ 配置模型 ─────────────────────────────────────────────────────│
│ STORAGE_MIRROR_ENABLED=true                                    │
│ STORAGE_MIRROR_WRITE_QUORUM=2  # 默认 len(backends)            │
│ STORAGE_MIRROR_READ_STRATEGY=round-robin                       │
│                                                                 │
│ # 第一个后端（主配置）                                          │
│ STORAGE_BACKEND=local                                          │
│ STORAGE_LOCAL_ROOT=./var/objects                                │
│                                                                 │
│ # 第二个后端（镜像配置）                                        │
│ STORAGE_MIRROR_1_BACKEND=s3                                    │
│ STORAGE_MIRROR_1_S3_ENDPOINT=https://s3.amazonaws.com          │
│ STORAGE_MIRROR_1_S3_BUCKET=aero-backup                         │
│ STORAGE_MIRROR_1_S3_REGION=us-east-1                           │
│ STORAGE_MIRROR_1_S3_ACCESS_KEY=...                             │
│ STORAGE_MIRROR_1_S3_SECRET_KEY=...                             │
│                                                                 │
│ # 第三个后端（可选）                                            │
│ STORAGE_MIRROR_2_BACKEND=local                                 │
│ STORAGE_MIRROR_2_LOCAL_ROOT=./var/objects-mirror               │
└────────────────────────────────────────────────────────────────┘

┌─ 实现路径 ─────────────────────────────────────────────────────│
│ 新类型: storage/mirror.go → MirrorStorage                       │
│   实现 Storage 接口                                            │
│   组合模式（持有多 Storage 实例）                                │
│   并发写入（goroutine per backend）                             │
│   读策略（round-robin / latency-probe）                         │
│   WriteQuorum: 可配置的写入成功数                                 │
│                                                                 │
│ Factory 扩展:                                                   │
│   storage/factory.go → NewFromConfig 检测 mirror 配置            │
│   构建所有后端实例 → 包裹到 MirrorStorage                         │
│                                                                 │
│ Config 扩展:                                                    │
│   config/config.go → MirrorConfig 节                             │
│   config/config_storage.go → 新增镜像配置项                      │
│                                                                 │
│ 合约测试:                                                       │
│   storage/contract_test.go → 添加 mirror 包裹测试                │
│   验证写后读一致性                                               │
│   验证后端故障下的故障转移                                         │
│   验证 writeQuorum 约束                                          │
└────────────────────────────────────────────────────────────────┘

┌─ 与现有复制工人的关系 ─────────────────────────────────────────│
│ 同步多镜像 (MirrorStorage): 跨后端强一致写入                      │
│ 异步复制工人 (ReplicationWorker): 跨区域最终一致复制               │
│ 两者互补:                                                        │
│   同一区域内 → 镜像（低延迟，强一致）                              │
│   跨区域间 → 复制（高延迟，最终一致）                              │
│   更高级: 区域内镜像 + 区域间复制 = 最高持久性                     │
└────────────────────────────────────────────────────────────────┘

┌─ Metrics ──────────────────────────────────────────────────────│
│ mirror_write_total{backend, status}      # 按后端写入计数       │
│ mirror_write_quorum_failures_total       # 未达 quorum 的次数   │
│ mirror_read_fallback_total{from,to}      # 读故障转移计数        │
│ mirror_lag_bytes{backend}               # 后端间差异字节数      │
│ mirror_health{backend}                  # 1=健康, 0=故障       │
└────────────────────────────────────────────────────────────────┘
```

**边界情况：**
- 写入 quorum 未达到时的处理：已写入的后端数据需要保留（幂等性保证），人工介入修复
- 后端对称恢复：一个后端离线后恢复，需 reconcile 追赶丢失的写入（复用 `internal/reconcile/job.go`）
- 部分成功：写入 A 成功、B 失败 → 返回 error，但 A 的数据保留（不自动回滚）
- 分片上传：UploadPart 也需要写入所有镜像——需要改造 `internal/storage/storage.go:UploadPart`
- 删除操作：Delete 必须删除所有镜像，一个失败 → log warning 但不返回 error（尽力而为）
- 跨镜像 ETag/Checksum 不一致：应在写入时确保完全一致（相同 body + 相同 ETag 算法）

**复杂度:** M · **用户影响:** ★★★★☆（可靠性） · **代码变更:** ~900 行新代码 + ~300 行修改

---

## 5. 协作分享链接（受控预签名 URL + 安全策略）

### 为什么需要它

当前预签名 URL 功能（`PresignGet` / `PresignPut`）提供了基础的能力——生成一个有时效的 URL。但企业协作场景需要**更丰富的分享控制**：

- **分享下载链接**：用户希望生成一个链接发给同事，对方无需认证即可下载文件
- **密码保护**：链接需要密码才能访问
- **下载次数限制**：链接只能使用 N 次
- **分享预览**：不是下载整个文件，而是预览前 N KB
- **分享管理**：查看已创建的分享、撤回分享、查看分享访问日志
- **目录分享**：分享整个目录的访问权限

AWS S3 没有原生分享链接功能（需要自建），但这是用户**最常问到**的功能。对于 aero-vault 这样的独立存储服务，内置分享能力是显著的产品差异化优势。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/service/file_features.go:PresignGet/PresignPut` | 基础预签名 URL | 无额外安全控制 |
| `internal/storage/local.go:PresignGet` | HMAC 签名 URL | 无密码/次数/IP 限制 |
| `internal/storage/sign.go` | URL 签名/验签 | 签名 payload 无法携带额外元数据 |
| `internal/api/rest/router.go` | `/presign` 路由 | 无 `/shares` 路由 |
| `internal/auth/auth.go` | 认证/授权 | 无分享 token 认证 |

### 架构蓝图

```
┌─ Share Link Model ─────────────────────────────────────────────│
│ type ShareLink struct {                                        │
│     ID           string   // 随机短 ID，如 "abc123xyz"         │
│     TenantID    string                                        │
│     Bucket      string                                        │
│     Key         string   // 单文件 或 前缀（目录分享）           │
│     IsPrefix    bool     // true = 分享整个前缀                 │
│     PasswordHash string  // bcrypt hash，nil = 无密码          │
│     MaxDownloads int      // 0 = 无限制                        │
│     DownloadCount int64                                       │
│     ExpiresAt    *time.Time                                   │
│     CreatedAt    time.Time                                    │
│     CreatedBy    string  // 创建者 user/API key label          │
│     LastAccessed *time.Time                                   │
│     IsActive     bool    // false = 已撤销                     │
│ }                                                              │
└────────────────────────────────────────────────────────────────┘

┌─ API Surface ──────────────────────────────────────────────────│
│ POST   /v1/shares                                               │
│   { "bucket": "default",                                       │
│     "key": "reports/q3-2026.pdf",                              │
│     "password": "secure123",          // optional              │
│     "max_downloads": 50,              // optional              │
│     "expires_in_hours": 72 }          // optional              │
│   → { "id": "abc123", "url": "https://.../share/abc123" }      │
│   Response 201 Created                                          │
│                                                                 │
│ GET    /share/{id}        → 下载页面（HTML）或直接 302 跳转      │
│         ?password=secure123                                     │
│   → 验证有效 → 提供文件（或返回 HTML 表单让用户输入密码）         │
│                                                                 │
│ GET    /share/{id}/preview → 预览前 4KB 内容                    │
│   （供社交分享时的卡片/预览用）                                   │
│                                                                 │
│ GET    /v1/shares          → 列出活跃分享（需 auth）             │
│ DELETE /v1/shares/{id}     → 撤销分享链接                       │
│ GET    /v1/shares/{id}/log → 访问日志（谁/何时/IP）              │
└────────────────────────────────────────────────────────────────┘

┌─ Security Model ───────────────────────────────────────────────│
│ ┌─ 创建分享 ───────────────────────────────────────────┐        │
│ │ 需要 write scope（或 dedicated "share" scope）         │        │
│ │ 创建者信息记录在 share 行，用于审计                      │        │
│ │ 密码 bcrypt 哈希后存储，永远不存明文                      │        │
│ └────────────────────────────────────────────────────┘        │
│                                                                 │
│ ┌─ 访问分享 ───────────────────────────────────────────┐        │
│ │ 无需认证（这就是"分享"的意义）                          │        │
│ │ 但需要密码（如果设置了）                               │        │
│ │ 密码通过 query param 或 POST body 传入                │        │
│ │ 服务端 bcrypt.Compare 验证                            │        │
│ │ 验证通过后发 SET-Cookie（会话缓存密码验证结果）           │        │
│ └────────────────────────────────────────────────────┘        │
│                                                                 │
│ ┌─ 速率限制 ───────────────────────────────────────────┐        │
│ │ 每个分享链接的访问速率限制（10 req/s）                   │        │
│ │ 防止暴力破解密码                                        │        │
│ │ IP 级别速率限制                                         │        │
│ └────────────────────────────────────────────────────┘        │
│                                                                 │
│ ┌─ 审计 ───────────────────────────────────────────────┐        │
│ │ 每次分享访问记录到 share_access_logs 表                │        │
│ │ 字段: share_id, ip, user_agent, timestamp, success    │        │
│ │ 管理员可查看访问日志                                    │        │
│ └────────────────────────────────────────────────────┘        │
└────────────────────────────────────────────────────────────────┘

┌─ 迁移需求 ─────────────────────────────────────────────────────│
│ 迁移 0026 (双驱动):                                             │
│   CREATE TABLE share_links (                                   │
│     id TEXT PRIMARY KEY,                                       │
│     tenant_id TEXT NOT NULL,                                   │
│     bucket TEXT NOT NULL,                                      │
│     key TEXT NOT NULL,                                         │
│     is_prefix INTEGER DEFAULT 0,                               │
│     password_hash TEXT,                                        │
│     max_downloads INTEGER DEFAULT 0,                           │
│     download_count INTEGER DEFAULT 0,                          │
│     expires_at TEXT,                                           │
│     created_at TEXT NOT NULL,                                  │
│     created_by TEXT NOT NULL,                                  │
│     last_accessed TEXT,                                        │
│     is_active INTEGER DEFAULT 1                                │
│   );                                                           │
│   CREATE TABLE share_access_logs (                             │
│     id INTEGER PRIMARY KEY AUTOINCREMENT,                      │
│     share_id TEXT NOT NULL REFERENCES share_links(id),         │
│     ip TEXT NOT NULL,                                          │
│     user_agent TEXT,                                           │
│     accessed_at TEXT NOT NULL,                                 │
│     success INTEGER DEFAULT 1                                  │
│   );                                                           │
└────────────────────────────────────────────────────────────────┘

┌─ 与现有 Presign 的关系 ───────────────────────────────────────│
│ 分享链接 构建在 预签名 URL 之上:                                  │
│   分享链接 = 短 ID + 数据库记录 + 安全层                          │
│   底层: 实际文件访问 = 标准 `PresignGet`（复用 HMAC 签名）        │
│   额外: 访问时先验证分享链接的有效性（密码/次数/过期）              │
│                                                                 │
│ 关系: 预签名 URL = 开发者工具（程序化使用）                        │
│       分享链接 = 用户功能（业务协作场景）                          │
└────────────────────────────────────────────────────────────────┘
```

**边界情况：**
- 分享链接被滥用：密码保护 + IP 级速率限制 + 最大下载次数 + 过期时间
- 密码重置：不能重置密码，只能撤销链接创建新链接（密码不可逆）
- 对象在分享期间被删除：分享链接访问时返回 410 Gone，而不是报错
- 对象在分享期间被更新（新版本）：分享指向最新版本还是固定版本？**策略**：分享固定到创建时的 storage_key + versionID，不受后续更新影响
- 目录分享：最大深度 N 级 + 分页，防止列出上百万个文件
- CLI/自动化场景：`aero-vault cli share create reports/q3.pdf --password --max-downloads 10`

**复杂度:** M · **用户影响:** ★★★★★（日常协作） · **代码变更:** ~1000 行新代码 + ~200 行修改

---

## 附录：被排除但值得关注的较小改进

| 问题 | 位置 | 说明 | 建议 |
|------|------|------|------|
| **密码绕过策略缺失** | `internal/service/file_crud.go:hardDeleteObject` | `_aero_legal_hold == "ON"` 检查无权限绕过路径 | 需要 `x-amz-bypass-governance-retention` 等价机制（#2 核心内容）|
| **桶删除不检查对象存在** | `internal/service/file_features.go:DeleteBucket` | `repo.DeleteBucket` 直接删除行，不检查是否有对象 | 应在删除前检查 `BucketStats` 并拒绝非空桶 |
| **内容类型规范化缺失** | `internal/service/file_crud.go:buildPutObject` | Content-Type 原样存储 | 添加 magic byte 嗅探 + 标准化映射（`text/plain`→ `text/plain; charset=utf-8`）|
| **事件表无限增长** | `internal/repository/sql_events.go` | `events` 表 INSERT 无清理机制 | 添加 `RECONCILE_EVENT_RETENTION_DAYS` + 后台清理 |
| **缺失跨区域事件的排序保证** | `internal/events/postgres_transport.go` | `pg_notify` 不保证跨区域的事件顺序 | 添加 Lamport 时钟或全局序列计数器 |
| **CLI 无 JSON 输出模式** | `internal/cli/cli_crud.go` | 仅文本输出，无法与 `jq` 管道组合 | 添加 `--json` 全局标志 |
| **SSE 连接无事件重放** | `internal/api/rest/sse.go` | 客户端断线后丢失中间事件 | 支持 `Last-Event-ID` + 事件重放 |
| **WebDAV 文件锁定（LOCK/UNLOCK）缺失** | `internal/api/webdav/dav.go` | 不支持 WebDAV LOCK 方法 | 一些客户端（MS Office）需要 LOCK 才能编辑 |
| **预签名的速率限制缺失** | `internal/middleware/ratelimit.go` | 预签名 URL 绕过 rate limiter（签名在中间件之前）| 在 Presign middleware 中添加预签名专用的 rate limit |
| **多协议 Content-Type 不一致** | 跨 REST/S3/WebDAV/MCP | 同一对象通过不同协议设置略有不同的 Content-Type | 添加规范化的 Content-Type 映射层 |

---

> **总结：** 以上 5 个方向均未被前期八轮评估或第一期 expansion-directions 覆盖。它们覆盖了企业运维（批量操作）、合规安全（对象锁模式）、数据处理（S3 Select）、架构可靠性（多镜像）、协作体验（分享链接）五个关键维度。建议实施顺序：#2（合规锁，准入级）→ #5（分享链接，快速见效）→ #4（多镜像，架构加固）→ #1（批量操作，运维提效）→ #3（S3 Select，差异化）。#2 和 #5 可以并行开发。
