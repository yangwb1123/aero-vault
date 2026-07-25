# AeroVault 高价值扩展方向（第八期）

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全局代码扫描（~50K 行 Go 源码），逐一审阅 `ROADMAP.md`、八轮 `analysis-v[1-8]-gaps-roadmap.md`、七期 `expansion-directions[-v2..v7]` 及 `extensions.md`，确认每个方向在既有文档中**零覆盖**。  
> **日期：** 2026-07-10  
> **原则：** 选取 5 个**工程架构层**的增量盲区。每个方向附带具体代码锚点、当前状态缺口、边界情况暴露、架构方向和实现理由。不编写任何实现代码。

---

## 总览

| # | 方向 | 类型 | 影响 | 核心代码锚点 | 覆盖检查 |
|---|------|------|------|-------------|---------|
| 1 | **S3 Bucket Policy & IAM Evaluation Engine** | 兼容性/安全 | 🔴 S3 兼容性半成品 | `internal/auth/policy.go` / `internal/repository/repository.go:BucketConfig.Policy` | 七期全部未覆盖 |
| 2 | **Postgres 连接池调优 & Read Replica 路由** | 性能/可靠性 | 🔴 生产高并发天花板 | `internal/repository/postgres.go:openPostgres` / `internal/repository/sql_objects.go` | 全部未覆盖 |
| 3 | **生产级备份/容灾 & 云后端快照** | 运维/可靠性 | 🔴 生产部署准入盲区 | `internal/snapshot/snapshot.go` / `internal/repository/sqlite.go` | 全部未覆盖 |
| 4 | **自动内容分类 & 数据防泄漏（DLP）框架** | 差异/合规 | 🟠 从"存储"到"智能内容平台" | `internal/ai/pii.go` / `internal/ai/extractor.go` / `internal/service/file_crud.go:Put` | 全部未覆盖 |
| 5 | **跨协议并发写一致性 & 冲突检测** | 架构/可靠性 | 🟠 多协议接入的静默覆盖风险 | `internal/service/file_crud.go:Put` / `internal/api/rest/` / `internal/api/s3compat/` / `internal/api/webdav/dav.go` / `internal/mcp/server.go` | 全部未覆盖 |

---

## 1. S3 Bucket Policy & IAM Evaluation Engine

### 当前状态

代码库中**已经存在完整的 IAM-style policy 解析引擎**，但从未被请求管线调用。

**已有（但闲置）：**

| 位置 | 代码 | 状态 |
|------|------|------|
| `internal/auth/policy.go` | `ParsePolicy()` / `Policy.Eval()` / `Allowed()` | ✅ 实现完整（action 匹配、Principal、IP 条件） |
| `internal/auth/policy.go` | `s3Actions` 映射表 + 通配符 `s3:*` 支持 | ✅ 完整 |
| `internal/auth/policy.go` | IP-based 条件：`IpAddress` / `NotIpAddress` + CIDR 匹配 | ✅ 完整 |
| `internal/repository/repository.go` | `BucketConfig.Policy string` 字段 | ✅ Schema 已存储 |
| `internal/api/rest/admin.go` | `PutBucketPolicy` / `DeleteBucketPolicy` / `GetBucketPolicy` | ✅ REST API 端点 |
| `internal/api/s3compat/handler.go` | `PutBucketPolicy` / `GetBucketPolicy` / `DeleteBucketPolicy` | ✅ S3 API 端点 |
| `internal/service/file_features.go` | `SetBucketPolicy` / `GetBucketPolicy` / `DeleteBucketPolicy` | ✅ Service 层方法 |

**未实现（断路）：**

| 位置 | 应该发生 | 实际 |
|------|---------|------|
| `internal/auth/auth_middleware.go` | 每次请求装载目标桶的 `BucketConfig.Policy` 并调用 `policy.Allowed(action, sourceIP)` | 完全不检查 Policy 字段 |
| `internal/api/s3compat/handler.go:GetObject` | 检查 `s3:GetObject` 是否被 Policy 允许 | 仅检查 auth scope + ACL |
| `internal/api/rest/handler.go:Get` / `serveObjectContent` | 检查 `s3:GetObject` | 仅检查 auth scope + ACL |
| `internal/service/file_crud.go:Put` | 检查 `s3:PutObject` | 仅检查 auth scope |
| `internal/service/file_crud.go:Delete` | 检查 `s3:DeleteObject` | 仅检查 auth scope + Lock |
| `internal/service/file_features.go:ListObjects` | 检查 `s3:ListBucket` | 全局 auth scope |
| `internal/auth/auth.go:Registry.Middleware` | 在 auth 完成后触发 Policy 评估 | 无 Policy 评估步骤 |

**因此：** 用户可以 PUT 一个 Deny-all 的 bucket policy，然后仍然成功 GET/PUT——Policy 被完整地存储、返回，但**从不执行**。这是一个半完成的 S3 兼容特性，比没有更危险（用户以为启用了 IP 白名单，实际未生效）。

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **Deny-all policy** | 用户设置 `{"Statement":[{"Effect":"Deny","Action":"*","Resource":"*"}]}` | 所有操作正常通过 | 所有 S3/REST 操作返回 403 |
| **IP 白名单绕过** | 设置 `Condition:{"IpAddress":{"aws:SourceIp":["10.0.0.0/8"]}}` | 外网 IP 仍然可访问 | Only 10.x.x.x 通过 |
| **桶策略跨桶引用** | `Resource:["arn:aws:s3:::other-bucket/*"]` | policy 被存储但永远不生效 | 当前可以不实现跨桶检查，但应返回 501 而不是静默存储 |
| **Deny 覆盖 Allow** | Deny `s3:GetObject` 在前，Allow `s3:GetObject` 在后 | 无影响（都不执行） | Deny 永远优先于 Allow |
| **匿名用户 + Policy** | 未认证用户 + bucket 设置了 PublicRead + Deny-s3:GetObject 的 Policy | 匿名用户可读取 | Deny 应覆盖桶的公共读 ACL |
| **Policy 语法错误** | PUT 一个 JSON 格式错误的 Policy | 存储错误格式的字符串，GetBucketPolicy 返回无效 JSON | PUT 时验证 `ParsePolicy` 并返回 MalformedPolicy 错误 |
| **Policy 与 ACL 共存** | Bucket 有 `public-read` ACL，但 Policy Deny 匿名访问 | ACL 生效（匿名可读） | Policy 评估必须后于 ACL 检查，Deny 覆盖 Allow |

### 架构方向

```
┌─ Policy Evaluation Middleware ──────────────────────────────────│
│ 新增中间件: PolicyEnforcer                                       │
│   位置: auth middleware 之后, handler 之前                         │
│   职责:                                                          │
│     1. 从请求上下文中提取 tenant / bucket / action / sourceIP     │
│     2. 调用 repo.GetBucketConfig(tenant, bucket) 获取 Policy     │
│     3. 如果 Policy 非空 → policy.Allowed(action, sourceIP)       │
│     4. 如果 Denied → 返回 403 AccessDenied                       │
│     5. 缓存结果（短 TTL, 如 30s）避免每次请求查 DB                │
│                                                                  │
│ Action 映射:                                                      │
│   每个 REST/S3 endpoint 在 handler 级别声明其 S3 action 名称：      │
│     GET    /v1/files/*    → "s3:GetObject"                        │
│     PUT    /v1/files/*    → "s3:PutObject"                        │
│     DELETE /v1/files/*    → "s3:DeleteObject"                     │
│     GET    /v1/files/     → "s3:ListBucket"                       │
│                                                                  │
│ 缓存策略:                                                         │
│   桶策略很少变（小时/天级），缓存 30s 不会造成安全问题。             │
│   PutBucketPolicy → 显式失效缓存键                                 │
└────────────────────────────────────────────────────────────────┘

┌─ 安全注意事项 ─────────────────────────────────────────────────│
│ 1. Policy 评估在 auth scope 检查之后执行                          │
│    - scope 决定"这个用户能操作哪些 API"                             │
│    - Policy 决定"在这个桶上，允许/拒绝哪些操作"                      │
│    - 两者是 AND 关系：必须同时通过                               │
│ 2. Deny 必须覆盖所有其他授权路径                                   │
│    - 即使 API Key 有 admin scope，桶 Policy Deny 也要优先          │
│ 3. 匿名请求的 Policy 检查（AnonymousPublicRead 启用时）             │
│    - 先检查 ACL（ObjectPublicReadable）                            │
│    - 再检查 Policy（Deny 覆盖 ACL Allow）                          │
└────────────────────────────────────────────────────────────────┘
```

**为什么现在做：** 这是 S3 兼容性最显眼的半成品。桶策略的存储/CRUD API 全部就绪，唯独执行挂空。每拖延一个版本，新用户都可能被"存储了但没生效"的安全错觉困扰。

| 影响面 | 工作量估计 |
|--------|-----------|
| Policy 评估中间件（新文件） | 低（~100 行） |
| 每个 handler 声明 action 名称 | 中（20+ handlers） |
| 缓存层 | 低 |
| 测试（100+ 用例覆盖 edge cases） | 中 |
| 现有 handler 改动 | 低（每 handler +1 行 context 注入） |

---

## 2. Postgres 连接池调优 & Read Replica 路由

### 当前状态

```go
// internal/repository/postgres.go:openPostgres
db, err := sql.Open("pgx", dsn)
// ... 无任何 SetMaxOpenConns / SetMaxIdleConns / SetConnMaxLifetime
```

对比 SQLite 的显式配置：
```go
// internal/repository/sqlite.go:openSQLite
db.SetMaxOpenConns(1) // serialize writes to avoid SQLITE_BUSY
```

**问题：** Postgres 连接使用 `database/sql` 的默认池配置：
- `MaxOpenConns`: 0（无限制）→ `pgx` 默认可能创建数百个连接
- `MaxIdleConns`: 2（默认）→ 低流量时大量连接重建
- `ConnMaxLifetime`: 0（永不过期）→ 连接永远不回收，可能导致负载均衡器侧断连

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/repository/postgres.go:14` | `sql.Open("pgx", dsn)` | 无池参数配置 |
| `internal/repository/sqlite.go:16` | 正确设置 `SetMaxOpenConns(1)` | Postgres 缺少等同的关怀 |
| `internal/repository/sql.go:rebind` | SQL 占位符改写 | 无查询超时、无慢查询日志 |
| `internal/repository/sql_objects.go` | ListObjects 使用 `OFFSET` / `LIMIT` | 大数据集下 OFFSET 性能差（需要 keyset pagination） |
| `internal/config/config.go` | DB 配置节只有 `Driver` + `DSN` | 无连接池、无查询超时、无 replica DSN 字段 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **100 并发写入** | 100 个 goroutine 同时 UpsertObject | 创建 100 个连接（pgx 默认行为），pgBouncer 可能拒绝 | `MaxOpenConns=20` + 等待队列 |
| **空闲后突然流量** | 深夜后早起高峰 | `MaxIdleConns=2` → 需要重建 18 个连接，增加 P99 延迟 | `MaxIdleConns=10` 减少 TLS 握手 |
| **长期运行连接过时** | 连续运行 30 天 | `ConnMaxLifetime=0` → 连接被 AWS RDS 代理关闭，导致 `broken pipe` | `ConnMaxLifetime=30m` 定期回收 |
| **搜索查询阻塞写入** | `SearchChunks` 遍历所有 chunk | 长查询占连接，池耗尽→写入超时 | 搜索走 Read Replica + 查询超时 |
| **ListObjects 百万级** | 桶中有 1000 万个对象 | OFFSET 5000000 需要扫描 500 万行 | Keyset pagination（WHERE id > marker） |

### 架构方向

```
┌─ 配置扩展 ──────────────────────────────────────────────────────│
│ DBConfig 新增字段:                                                │
│   MaxOpenConns      int // 默认 25                               │
│   MaxIdleConns      int // 默认 10                               │
│   ConnMaxLifetime   int // 秒，默认 1800 (30min)                 │
│   ConnMaxIdleTime   int // 秒，默认 300 (5min)                   │
│   QueryTimeout      int // 秒，默认 30                           │
│   ReadReplicaDSN    string // 可选，只读查询路由到此 DSN          │
│                                                                  │
│ 环境变量:                                                         │
│   DB_MAX_OPEN_CONNS=25                                           │
│   DB_MAX_IDLE_CONNS=10                                           │
│   DB_CONN_MAX_LIFETIME_SEC=1800                                   │
│   DB_READ_REPLICA_DSN=postgres://reader:xxx@replica:5432/db      │
│   DB_QUERY_TIMEOUT_SEC=30                                        │
└────────────────────────────────────────────────────────────────┘

┌─ Read Replica 路由 ─────────────────────────────────────────────│
│ 方案: 双层 sql.DB                                                     │
│   primaryDB *sql.DB  — 所有写操作 + 强一致性读                       │
│   replicaDB *sql.DB  — 搜索、列表、统计等可容忍最终一致性的读          │
│                                                                  │
│ 仓库接口方法分类:                                                   │
│   写操作 (UpsertObject, InsertEvent, PutAPIKey ...) → primaryDB    │
│   强一致读 (GetObject, GetBucketConfig ...) → primaryDB            │
│   容忍最终一致 (SearchChunks, ListObjects, ListBuckets ...) → replicaDB │
│                                                                  │
│ 如果 replicaDB 未配置（replicaDSN=""），所有路由到 primaryDB         │
└────────────────────────────────────────────────────────────────┘

┌─ Keyset Pagination ─────────────────────────────────────────────│
│ 当前: ListObjects(prefix, marker, limit)                          │
│   marker=string (object key) → WHERE key > ? ORDER BY key         │
│                                                                  │
│ 改进: 内部实现从 OFFSET 改为 keyset pagination                     │
│   不需要改动 API 签名（marker 保持不变），只改 SQL 实现                │
│   效果: O(n) → O(log n) 对于翻页深度大的场景                        │
└────────────────────────────────────────────────────────────────┘
```

**为什么现在做：** 这是生产部署的"地雷"。默认连接池配置在 50 QPS 下可能正常工作，但在 500 QPS、多租户、混合读写负载下必然出现问题。在用户遇到"间歇性超时"bug 之前修复的成本远低于事后排查。

| 影响面 | 工作量估计 |
|--------|-----------|
| 配置扩展 | 低 |
| 连接池配置（openPostgres 内 4 行） | 极低 |
| 只读副本仓库包装器 | 中 |
| 查询超时中间件 | 低 |
| Keyset pagination 改造 | 中 |

---

## 3. 生产级备份/容灾 & 云后端快照

### 当前状态

`internal/snapshot/snapshot.go` 实现了 `Create()` 和 `Restore()`，但 **仅支持 SQLite + local FS**：

```go
// snapshot.go 的实现假设:
// - dbPath = file:./var/aero.db   (SQLite 文件路径)
// - objectsRoot = ./var/objects   (本地文件系统目录)
// - 使用 os.File, tar.gz, filepath.Walk 操作本地文件
```

对于任何生产部署（Postgres + S3/OSS/COS），**没有可用的备份工具**。

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/snapshot/snapshot.go` | SQLite+local FS 的 tar.gz 打包 | 不支持 Postgres + 云存储 |
| `internal/cli/cli.go:cmdSnapshot` | CLI `snapshot` 子命令 | 绑定到 SQLite 实现 |
| `internal/repository/repository.go` | 无备份相关方法 | 无 `ExportData` / `ImportData` 接口 |
| `internal/config/config.go` | 无备份配置 | 无备份计划、目标、保留策略 |
| `docs/deployment.md` | 部署文档 | 无 DR 章节 |
| `internal/reconcile/` | 备份可复用 reconcile 的定时+租户遍历 | 无备份 Job |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **Postgres + S3 部署** | 企业生产环境 | 无备份工具，只能手动 pg_dump + aws s3 sync | 提供坐标化的备份/恢复命令 |
| **SQLite 跨版本恢复** | 旧版本快照恢复到新版本 | tar.gz 格式兼容，但 migrations 可能已前进 | 恢复后自动运行 migrations |
| **增量备份** | 每日全量备份 500GB 对象 | 每次全量复制→带宽和存储浪费 | 支持 WAL-E / pgBackRest 风格的增量 |
| **跨区灾备** | 主 region 故障，切换到备 region | 无自动化流程 | 备份 Job 定期导出到备 region |
| **时间点恢复（PITR）** | 用户误删除后需要恢复到 1 小时前 | 只有全量快照 | Postgres WAL 归档 + 连续归档 |
| **云后端对象备份一致性** | S3 备份过程中有新写入 | tar.gz 打包 30 秒内可能有状态漂移 | 先 DB snapshot → 再一致性对象遍历 |

### 架构方向

```
┌─ 备份架构 ──────────────────────────────────────────────────────│
│ 新增: internal/backup/ 包                                         │
│                                                                  │
│ type BackupOptions struct {                                       │
│     DB        DBType    // sqlite | postgres                      │
│     Storage   string    // local | s3 | oss | cos                │
│     Target    string    // backup destination URL                 │
│     Mode      string    // full | incremental                    │
│     Retention int       // days to keep old backups               │
│ }                                                                 │
│                                                                  │
│ 备份流程（Percona XtraBackup 风格思想适配）:                        │
│   Phase 1: 预热 DB snapshot                                      │
│     - Postgres: pgBackRest / pg_dump + WAL archive               │
│     - SQLite: 现有 tar.gz 逻辑                                    │
│   Phase 2: 遍历租户 + 桶 + 对象元数据 → 写入备份清单                  │
│     - 备份清单: backup_manifest.json (对象列表 + ETag + checksum)   │
│   Phase 3: 对象备份                                               │
│     - 云后端: 使用云提供商的快照/复制 API（S3 Batch Operations）     │
│     - 本地: 现有 tar.gz 逻辑                                      │
│   Phase 4: 元数据+存储的一致性校验                                  │
│     - 对比备份清单中的 ETag 与实际对象的 Content-MD5                │
│                                                                  │
│ 恢复流程:                                                         │
│   Phase 1: 恢复 DB（pg_restore / sqlite 快照）                    │
│   Phase 2: 根据备份清单恢复对象                                     │
│   Phase 3: 运行 consistency check（恢复后 scrub）                  │
└────────────────────────────────────────────────────────────────┘

┌─ 备份 Job ──────────────────────────────────────────────────────│
│ 复用现有的 JobPool 和 reconcile 定时机制:                          │
│   - jobReg.Register(JobBackup, handler)                           │
│   - 可配置 BACKUP_SCHEDULE="0 2 * * *" (cron 每日凌晨 2 点)        │
│   - 备份 Job 记录到 jobs 表，支持重试和通知                          │
│                                                                  │
│ CLI 扩展:                                                         │
│   aero-vault cli backup create [--output s3://bucket/backups/]   │
│   aero-vault cli backup list                                     │
│   aero-vault cli backup restore <snapshot-id>                    │
│   aero-vault cli backup schedule "0 2 * * *"                    │
└────────────────────────────────────────────────────────────────┘
```

**为什么现在做：** 备份是生产部署的非功能性需求——用户不会在部署第一天问它，但会在数据丢失那天问。没有备份工具的存储系统不是生产就绪的系统。同时这个方向充分利用了现有的 `JobPool`、`Reconcile` 定时器、CLI 框架和 telemetry。

| 影响面 | 工作量估计 |
|--------|-----------|
| `internal/backup/` 包（接口 + 基础结构） | 中 |
| Postgres 备份适配器（调用 pg_dump） | 低 |
| 云存储对象备份适配器 | 中 |
| 备份清单管理 + 校验 | 中 |
| 调度集成（复用 JobPool） | 低 |
| CLI 扩展 + 测试 | 中 |

---

## 4. 自动内容分类 & 数据防泄漏（DLP）框架

### 当前状态

代码库已有 PII 检测能力，但仅限于 `internal/ai/pii.go` 的扫描和脱敏，且仅用于 AI 索引管线：

```go
// internal/ai/indexer.go
if cfg.AI.PIIScan {
    indexer.WithPII(ai.NewPIIDetector(), cfg.AI.PIIRedact)
}
```

PII 检测只在**索引阶段**（对象已被写入后）触发，且只做扫描/脱敏报告。**没有以下能力：**

- **上传时实时分类**：PUT 请求写入对象时并行分类内容类型（文档、代码、财务数据、个人数据）
- **DLP 策略引擎**：基于分类结果执行策略（阻止上传、隔离、审计告警、自动加标签/元数据）
- **自定义分类规则**：用户定义自己的内容分类正则/模式
- **内容类型统计**：按内容类型分布的报告（多少 PDF、多少含 PII 的对象）
- **基于分类的路由**：不同内容类型自动路由到不同的存储桶/存储类

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/ai/pii.go` | 信用卡 + Luhn + 邮箱检测 | 仅限 PII，无通用分类框架 |
| `internal/ai/pii.go:Scan()` | 返回 PII 报告 | 不触发策略动作（只记日志） |
| `internal/service/file_crud.go:Put` | 写入时无内容分类 | 无 Hook 用于插入分类步骤 |
| `internal/service/file.go:EventSink` | 事件发布 | 无 DLP 告警事件类型 |
| `internal/repository/repository.go:Object` | Object 有 tags/metadata | 无 auto_classification 字段 |
| `internal/repository/sql_objects.go` | 元数据存储 | 无分类索引 |
| `internal/api/rest/handler.go:Put` | PUT 对象 | 无内容分类中间件 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **信用卡号上传** | 用户上传包含明文信用卡号的 CSV | 成功写入；PII 扫描在索引阶段触发（几分钟后） | PUT 时实时检测 → 阻挡/告警/自动标记 |
| **大文件分类超时** | 用户上传 500MB 视频文件 | 无分类（仅 Content-Type 头） | 跳过超大文件的实时分类，标记为"待异步分类" |
| **误报处理** | 代码中有 `test@example.com` 被标记为 PII | 无法调节敏感度 | 分类规则支持白名单 + 精确度阈值 |
| **分类结果用于路由** | 含 PII 的对象自动存入加密桶 | 无路由机制 | DLP 策略声明 `action: reroute` |
| **加密对象分类** | SSE 加密的对象无法在服务器端读取明文 | 无分类可能 | DLP 策略应在加密前运行（或在客户端完成分类并发送分类标签） |
| **合规要求"自动标签"** | 金融合规要求所有含财务数据的对象自动加 `classification=financial` tag | 全靠用户手动 | DLP 自动打标 + 标签变更审计 |

### 架构方向

```
┌─ 内容分类器接口 ────────────────────────────────────────────────│
│ 新增: internal/classify/ 包                                      │
│                                                                  │
│ type Classifier interface {                                       │
│     Name() string                                                │
│     Classify(ctx, reader io.Reader, size int64,                   │
│              contentType string) (Classification, error)          │
│ }                                                                 │
│                                                                  │
│ type Classification struct {                                      │
│     Labels    []Label   // [{Category: "pii", Confidence: 0.95},  │
│                         //  {Category: "financial", Conf: 0.80}]  │
│     Sensitive bool      // 是否敏感内容                             │
│     PIIReport *PIIReport // 可选的 PII 详细报告                     │
│ }                                                                 │
│                                                                  │
│ 内置分类器:                                                        │
│   - PIIClassifier（复用 internal/ai/pii.go 逻辑）                  │
│   - FileTypeClassifier（基于 magic bytes 而非 Content-Type）        │
│   - RegexClassifier（用户自定义正则匹配规则）                        │
│   - SizeClassifier（按文件大小的简单规则）                           │
└────────────────────────────────────────────────────────────────┘

┌─ DLP 策略引擎 ──────────────────────────────────────────────────│
│ type DLPPolicy struct {                                          │
│     Name        string                                           │
│     Classifier  string    // 使用哪个分类器                        │
│     MatchLabels []LabelCondition                                 │
│     Action      string    // "deny" | "allow" | "quarantine" |    │
│                           // "tag" | "alert" | "reroute"          │
│     Target      string    // 目标（reroute 时指定桶）               │
│     AlertWebhook string   // 告警通知 URL                         │
│ }                                                                 │
│                                                                  │
│ 策略评估位置:                                                      │
│   1. PUT 请求的 FileService 层（Put 方法内，对象写入前或写入后）      │
│   2. 异步分类 Job（对大文件/加密对象）                               │
│   3. 索引管线（现有 PII 扫描的增强）                                │
└────────────────────────────────────────────────────────────────┘

┌─ 集成到 PUT 管线 ───────────────────────────────────────────────│
│ 在 FileService.Put 内新增步骤:                                    │
│   1. Tee reader: 同时写入 storage + 送入分类器 (只对 <= 10MB)      │
│   2. 分类器运行 → 返回 Classification                              │
│   3. DLP 策略匹配 → 执行 Action                                    │
│      - deny: 删除已写入的 blob, 返回 403                           │
│      - tag: 自动添加标签到对象的 metadata                           │
│      - alert: 发布 DLP 告警事件                                    │
│      - quarantine: 写入隔离桶而非目标桶                             │
│   4. 分类结果写入 `_aero_classification` 元数据键                   │
└────────────────────────────────────────────────────────────────┘
```

**为什么现在做：** 这是从"对象存储"进化到"智能内容平台"的差异化能力。PII 检测已有基础，向上构建通用分类+DLP 框架的增量成本低。内容分类是企业合规（金融、医疗、隐私）的准入级要求。

| 影响面 | 工作量估计 |
|--------|-----------|
| `internal/classify/` 接口 + 内置分类器 | 中 |
| DLP 策略模型 + 引擎 | 中 |
| PUT 管线集成 | 低 |
| 管理 API（策略 CRUD） | 中 |
| 测试（各种文件类型 + 策略组合） | 高 |

---

## 5. 跨协议并发写一致性 & 冲突检测

### 当前状态

一个对象可以通过 4 个协议同时写入：

```
REST   PUT /v1/files/foo     → FileService.Put
S3     PUT /s3/bucket/foo    → FileService.Put
WebDAV PUT /webdav/foo       → FileService.Put
MCP    write_file("foo")     → FileService.Put
```

当前无任何跨协议并发控制。最后写入的 wins（LWW）。问题在于：

1. **没有分布式锁**：WebDAV 客户端正在编辑文档，同时 REST 请求覆盖同一 key → 数据无声覆盖
2. **没有乐观并发控制（除非客户端显式使用 `If-Match`）**：绝大多数客户端不发送 `If-Match`
3. **WebDAV 的 `Lock-Token` 不被 REST/S3 识别**：WebDAV 支持 `LOCK`/`UNLOCK`，但其他协议完全忽略 WebDAV 锁
4. **MCP 写入无版本检查**：MCP `write_file` 工具不发送 `If-Match`

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/api/webdav/dav.go` | WebDAV LOCK/UNLOCK 实现 | 锁存储在 WebDAV 本地，不被其他协议识别 |
| `internal/service/file_crud.go:Put` | 最后写入 wins | 无强制乐观锁机制 |
| `internal/service/file.go` | 无锁服务 | 无跨协议的锁管理器 |
| `internal/api/rest/conditional.go` | `If-Match` / `If-None-Match` 支持 | 可选的（客户端决定发不发） |
| `internal/api/s3compat/conditional.go` | S3 `If-Match` / `If-None-Match` 支持 | 可选的 |
| `internal/repository/sql_objects.go:UpsertObject` | `ON CONFLICT DO UPDATE` | 无版本号检查 |
| `internal/api/webdav/dav.go` | WebDAV 的锁超时 `time` | 锁不跨节点（无 Postgres 持久化） |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **REST 覆盖 WebDAV 锁定对象** | WebDAV 客户端 LOCK 了文件，REST PUT 同一 key | REST PUT 成功（无视锁），WebDAV 客户端内容丢失 | REST PUT 应检查 WebDAV 锁并返回 423 Locked |
| **S3 并发覆盖同一对象** | 两个 S3 客户端同时 PUT same key | 后完成的覆盖先完成的（可能部分数据丢失） | 可选启用乐观锁：`x-amz-optional-lock` |
| **WebDAV 锁在集群中不可见** | 两个实例的 WebDAV 客户端分别 LOCK 同一文件 | 两个 LOCK 都成功（各实例独立） | 锁需要持久化到 DB 或使用分布式锁 |
| **MCP 工具的无条件写入** | Agent 调用 `write_file` 覆盖用户已编辑的文档 | 静默覆盖 | MCP 写入应默认 `If-Match`（除非显式 `force: true`）|
| **版本化桶中的并发** | 版本化桶中两个并发 PUT 创建两个版本 | 没问题（版本化隔离） | 没问题，但客户端需要 `X-Version-Id` 来确认哪个是"当前" |
| **长期持有锁的清理** | WebDAV 客户端崩溃，锁未释放 | 锁一直存在直到超时（到秒级超时） | 没有管理 API 来查看/清除活动锁 |
| **Lock 与 Object Lock 冲突** | 对象设置了 WORM 锁（`locked_until`），WebDAV LOCK 尝试 | WebDAV 的 LOCK 可能成功（不检查 WORM） | 所有协议的写入操作必须检查 `locked_until` |

### 架构方向

```
┌─ 分布式锁管理器 ────────────────────────────────────────────────│
│ 新增: internal/lock/ 包                                           │
│                                                                  │
│ type LockManager struct {                                        │
│     repo repository.Repository  // 锁行持久化到 DB               │
│ }                                                                 │
│                                                                  │
│ type Lock struct {                                                │
│     Tenant     string                                            │
│     Bucket     string                                            │
│     Key        string                                            │
│     Token      string                                            │
│     Owner      string  // 锁的持有者标识                          │
│     Timeout    time.Duration                                     │
│     AcquiredAt time.Time                                         │
│     ExpiresAt  time.Time                                         │
│ }                                                                 │
│                                                                  │
│ 方法:                                                             │
│   Acquire(ctx, tenant, bucket, key, owner, timeout) (*Lock, error) │
│   Release(ctx, token) error                                       │
│   Refresh(ctx, token, timeout) error                              │
│   IsLocked(ctx, tenant, bucket, key) (*Lock, bool)                │
│   ListLocks(ctx, tenant) ([]Lock, error)                          │
│                                                                  │
│ 锁行表迁移: locks.sql                                              │
│   tenant, bucket, key (UNIQUE), token, owner,                     │
│   acquired_at, expires_at                                         │
│   → Postgres SKIP LOCKED / SQLite 排他约束                        │
└────────────────────────────────────────────────────────────────┘

┌─ 集成到写入路径 ────────────────────────────────────────────────│
│ FileService.Put 新增可配置的锁检查步骤:                             │
│   if cfg.Lock.Enabled {                                          │
│       lock, ok := lockManager.IsLocked(ctx, t, b, k)              │
│       if ok && !opts.BypassLock {                                 │
│           return ErrLocked (423)                                  │
│       }                                                           │
│   }                                                               │
│                                                                  │
│ 每个协议适配器传递锁 token 的方式:                                  │
│   - WebDAV: 使用已有的 Lock-Token header                          │
│   - REST: 新增 `X-Aero-Lock-Token` header                        │
│   - S3:    可复用地使用 `x-amz-lock-token`（S3 扩展头）            │
│   - MCP:   write_file 新增可选参数 lock_token                    │
└────────────────────────────────────────────────────────────────┘

┌─ WebDAV 锁增强 ────────────────────────────────────────────────│
│ 当前 WebDAV 锁在内存中管理，不跨实例：                               │
│   - dav.go:lockManager 是 map[string]*lock (进程内)              │
│   - 改为使用分布式 LockManager（DB 持久化）                        │
│   - 锁超时清理使用 reconcile 周期任务                               │
│                                                                  │
│ 锁与 WORM 互斥:                                                   │
│   WebDAV LOCK 前先检查 Object.LockedUntil                        │
│   如果 locked_until > now → LOCK 返回 423 Locked                 │
└────────────────────────────────────────────────────────────────┘

┌─ 乐观并发控制增强 ──────────────────────────────────────────────│
│ 可选：FileService.Put 增加 `require_etag_match` 模式              │
│   当启用时，Put 需要 `ExpectedETag` 参数                           │
│   如果当前对象的 ETag 不匹配 → 返回 412 Precondition Failed        │
│   提供"安全写入"模式：客户端在 Read 时获取 ETag，写入时携带           │
│   CLI 新增 --require-etag 标志                                     │
└────────────────────────────────────────────────────────────────┘
```

**为什么现在做：** 四协议接入是该项目的核心差异化优势，但也是最大的风险来源——当多个协议同时操作同一对象时，用户期望一致的行为。WebDAV 锁被 REST 绕过是一个真实存在的用户体验 bug。在一个"数据就是资产"的产品中，静默覆盖是最让用户恐惧的事。

| 影响面 | 工作量估计 |
|--------|-----------|
| LockManager 包 + 迁移 | 中 |
| FileService 集成 + 423 错误传播 | 低 |
| WebDAV 改用分布式锁 | 中 |
| REST/S3/MCP 锁感知 | 中 |
| 管理 API（列出/清除锁） | 低 |

---

## 总结：优先级矩阵

| 方向 | 业务价值 | 工程成本 | 依赖关系 | 推荐排序 |
|------|---------|---------|---------|---------|
| S3 Bucket Policy Evaluation | ★★★★★（安全+兼容） | ★（低，复用现有代码） | 无 | **1** |
| Postgres 连接池 & Read Replica | ★★★★（性能+可靠性） | ★（极低，改配置少） | 无 | **2** |
| 生产级备份/容灾 | ★★★★（运维准入） | ★★★ | 依赖 JobPool 框架（已就绪） | **3** |
| 自动内容分类 & DLP | ★★★★（差异化+合规） | ★★★ | 依赖 PII 检测（已就绪） | **4** |
| 跨协议并发一致性 | ★★★（可靠性+体验） | ★★★★ | 依赖 LockManager 新包 | **5** |

**排序 1（Policy 评估）** 是"安全性半成品"——现有代码不执行，比没有更危险。成本最低、影响最大。  
**排序 2（连接池）** 是"迟早会出问题的配置"——在生产事故前修复，成本几乎为零。  
**排序 3（备份）** 是"生产就绪的准入条件"——没有备份的存储系统不是生产系统。  
**排序 4（分类/DLP）** 是差异化竞争优势，将存储升级为智能内容平台。  
**排序 5（跨协议锁）** 是四协议架构一致性的最后一块拼图，但实现复杂度最高。

---

*分析基于 commit: 当前 HEAD | 代码行数 ~50K (Go) + SDK/UI/Infra | 扫描工具: 全局文件遍历 + 关键路径深度代码审阅*
