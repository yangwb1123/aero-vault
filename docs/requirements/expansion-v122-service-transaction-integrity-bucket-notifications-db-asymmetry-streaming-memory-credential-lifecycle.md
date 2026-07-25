# 高价值扩展方向：服务层双写事务完整性、桶通知运行时缺口、DB 驱动特性不对称、流式路径内存压力、认证凭据生命周期管理

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — `cmd/server/main.go` + `internal/` 全部子包（237+ Go 源文件，50 对迁移文件），3 套 SDK（Go/Python/JS），MCP 双模式（HTTP+stdio），Web UI，`deploy/` 全套配置（Helm/Grafana/Prometheus/OTel），`HARNESS.md`，`AGENTS.md`，`ROADMAP.md`  
> **去重验证：** 对 `docs/requirements/` 下全部既有分析文档逐方向进行关键词正则 + 语义交叉验证 + 代码锚点反查  
> **日期：** 2026-07-11  
> **代码审查验证：** 对全部 5 个方向的代码锚点进行了 `grep` + 逐行阅读验证。以下标注 ✅ 已验证 / ⚠️ 已修正 
> **核心原则：** 不编写任何代码。选取**代码中存在明确锚点、有实质性产品/架构影响、且在前序分析中未被独立深度覆盖**的方向。每个方向包含：现状与代码证据 → 产品价值与典型场景 → 架构权衡与建议方案 → 边界情况。

---

## 去重验证

对 `docs/requirements/` 下全部既有分析文档逐方向进行关键词正则 + 语义交叉验证。

| 方向 | 既往覆盖情况 | 结论 |
|------|-------------|------|
| **方向一：服务层双写事务完整性缺口** — `storage.Put` 与 `repo.InsertObjectVersion`/`UpsertObject` 之间无原子性，crash 产生孤立 blob 或悬空元数据；`Delete` 路径 `emit→SoftDelete→(async chunkClean→store.Delete)` 同样存在 race | v3 方向二的 SAGA 表一行提及「批量操作缺乏事务/SAGA」但聚焦 `BatchDelete` 逐对象执行，未触及核心 CRUD 路径的双写 gap；其余文档零实质性覆盖。**正则 `dual.write\|atomic.*storage.*repo\|storage.*repo.*consistency\|storage.*metadata.*race\|orphan.*blob.*sequence\|crash.*write.*gap`** → 仅 v3 表行命中 | ✅ **全新架构方向** |
| **方向二：桶通知配置——已持久化但运行时缺口** — `notification_rules` JSON 列存在于 `buckets` 表，REST API 完整实现了 CRUD，但事件总线发布路径**完全不读取**这些规则；`Bus.Publish` 广播给所有订阅者而不是根据桶级 `notification_rules` 路由 | v65 方向三覆盖 S3 通知 XML schema（`TopicConfig`/`QueueConfig`/`LambdaConfig`）的解析——聚焦协议格式而非运行时路由；v54/v80/v87 在功能列表框中提及 `GetBucketNotifications` 端点但仅作为 feature audit 条目；**零文档分析运行时缺口**——rules 被持久化但从不用于事件路由。**正则 `notification.*route\|notification.*publish\|notification.*dispatch\|notification.*wiring\|rules.*event.*routing\|BucketNotification.*runtime`** → 0 命中 | ✅ **全新产品缺口方向** |
| **方向三：DB 驱动特性不对称与无优雅降级** — Postgres-only 功能（LISTEN/NOTIFY 事件传输、pgvector、pgFTS、`SKIP LOCKED` 在 `ClaimJob` 中、`CountJobsByStatus` 简单 COUNT 在 SQLite 上性能退化）在 SQLite 模式下无运行时检测；`sql.go` 的 `rebind($N→?)` 解决了语法差异但语义鸿沟未处理 | 前序 121 轮分析零覆盖。**正则 `sqlite.*postgres.*gap\|postgres.*sqlite.*asymmet\|feature.*asymmet\|graceful.*degrad\|driver.*gap\|driver.*feature.*matrix\|DB.*feature.*disparit`** → 0 独立分析深度覆盖 | ✅ **全新架构方向** |
| **方向四：流式路径内存压力管理缺失** — 多个热点路径使用无界 `io.Copy`/`io.ReadAll`；MCP `read_file` 的 4MB 硬截断本质是内存保护的粗糙手段；并发大对象操作（多个 500MB 文件同时 PUT/GET）可耗尽进程内存；`io.CopyN(io.Discard, rc, offset)` 用于 Range skip 但跳过不可见数据时仍全量读取 | v60 方向五覆盖「进程内内存结构无上限」聚焦 BM25、缓存、速率限制器——**静态数据结构的内存管理**，而非**流式传输路径的内存压力**；v94 方向二覆盖「准入控制与并发治理」聚焦**请求级并发上限**而非**字节级内存预算**。**正则 `io.Copy\|io.ReadAll.*memory\|streaming.*memory\|memory.*budget\|memory.*pressure.*stream\|concurrent.*large.*object\|内存.*流.*路径`** → 无独立分析对流式路径 | ✅ **全新生产加固方向** |
| **方向五：认证凭据生命周期管理缺失** — API 键持久化存在（`api_keys` 表 + `PersistKeys` flag），但无键轮换工作流、无使用率仪表盘、JWT 黑名单缺失 | v57 方向五覆盖「预签名 URL 安全绑定」——聚焦预签名约束而非凭据生命周期；v92 提及 SSE key rotation 但指加密密钥轮换；v65/v59/v7 提及 presigned URL 安全但无关凭据生命周期。**正则 `key.*rotation\|credential.*lifecycle\|API.*key.*rotation\|key.*expir.*auto\|key.*usage.*track\|key.*aging\|credential.*expire`** → 0 分析 API 键的完整生命周期管理 | ✅ **全新安全运维方向** |

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 | 验证状态 |
|---|------|------|--------|---------|---------|---------|
| **1** | **服务层双写事务完整性：从尽力一致到 SAGA 补偿** | 可靠性/架构 | **P0** | `Put` 路径 storage.Put 成功后 repo.UpsertObject/InsertObjectVersion 失败 → 存储层孤立 blob；`hardDelete` 路径 chunkClean→store.Delete→HardDeleteObject→usage 按序执行但中间步骤失败导致不一致 | `internal/service/file_crud.go:106-119`（`info, err := s.store.Put(...)` 成功 → `s.writePutObject` 失败 — **storage blob 已写但 metadata 未持久化**）；`file_crud.go:114`（`s.store.Delete(ctx, sk)` — Content-MD5 回滚，但只有这一种回滚）；`file_crud.go:119`（`writePutObject` 中 `repo.UpsertObject`/`repo.InsertObjectVersion` 失败 → **log + return error，store blob 已成孤儿**）；`file_crud.go:297-332`（`hardDeleteObject` — 五个步骤线性执行，任一失败后无法回滚已完成的步骤）；`file_crud.go:304-307`（`chunkCleaner.DeleteObjectChunks` 失败仅 warn，步骤继续——但若 store.Delete 随后失败，chunk 已清但 blob 还在） | ✅ **已验证** |
| **2** | **桶通知配置运行时缺口：已持久化但永不执行** | 产品完整/功能 | **P1** | `SetBucketNotifications` 将 rules JSON 写入 `buckets.notification_rules` 列，CRUD API 完整实现，但事件发布路径 `Bus.Publish` 完全不读取这些规则；所有事件广播给所有订阅者，桶级细粒度路由缺失 | `internal/repository/sql_buckets.go:381-415`（`GetBucketNotifications` / `SetBucketNotifications` / `DeleteBucketNotifications` — DB 层完整）；`internal/service/file_features.go:365-380`（Service 层完整，透传给 repo）；`internal/api/rest/handler.go:551-588`（REST handler 完整 CRUD）；`internal/api/s3compat/handler.go:809-833`（S3 `putBucketNotifications` 解析 XML，persist JSON）；**`internal/events/bus.go:67-75`（`Publish` 方法——broadcast 给所有 subscriber，零 bucket 规则检查）**；**`internal/events/bus.go:114`（`broadcast`——无条件发送给所有 subscriber channel）**；`internal/repository/repository.go:58-61`（`NotificationRule` 结构体——`TopicARN`/`LambdaARN` 标记 `unused, kept for compat`） | ✅ **已验证** |
| **3** | **DB 驱动特性不对称与无优雅降级** | 架构/运维 | **P1** | Postgres-only 功能在 SQLite 上无运行时检测：事件传输（LISTEN/NOTIFY）、pgvector、pgFTS、`SKIP LOCKED`（`ClaimJob`）、带偏移的 `DELETE...LIMIT`、并发 `ClaimJob` 行锁行为差异。某些功能静默降级，某些产生意外错误 | `internal/repository/sql.go:28-40`（`rebind` 函数——仅处理 `$N→?` 语法差异，不处理语义差异）；`internal/repository/jobs.go:106-115`（`ClaimJob` — Postgres 用 `FOR UPDATE SKIP LOCKED`，SQLite 用事务+无锁查询——**两套逻辑共存但无 capability 检查**）；`internal/events/postgres_transport.go:1-30`（Postgres LISTEN/NOTIFY transport——cfg.Events.Transport=="postgres" 时启用，SQLite 时配置无效但不报错）；`internal/ai/pgvector.go`（`AI_VECTOR_BACKEND=pgvector` 仅在 Postgres 可用，SQLite 时静默失败）；`internal/ai/pgfts.go`（`AI_LEXICAL_BACKEND=pgfts` 同） | ⚠️ **已验证（代码锚点修正）** |
| **4** | **流式路径内存压力管理：从无界 I/O 到有预算流控** | 可靠性/性能 | **P1** | 多个热点路径使用无界 I/O 操作；并发大对象操作可耗尽进程内存；无全局内存预算或 admission control；Range skip 通过 `io.CopyN(io.Discard, ...)` 全量读取被跳过的字节 | `internal/service/range.go:122-125`（`GetRange` — `io.CopyN(io.Discard, rc, offset)` 跳过 offset 字节，但全量读取到 discard——若 offset 很大如 1GB，1GB 数据完全读入内核再到 discard）；`internal/service/file_crud.go:106-108`（`Put` — `r io.Reader` 直传 store，无限制的 reader 可被恶意客户端利用——`r.Body` 无 `MaxBytesReader`）；`internal/service/file_crud.go:222-238`（`Get` — `s.store.Get` 返回的 `rc io.ReadCloser` 直传给调用方，无 memory-bounded wrapper）；`internal/mcp/server.go:249-250`（`toolReadFile` — `io.ReadAll(io.LimitReader(rc, 4<<20))` — 4MB 硬截断是粗糙的内存保护，大文件返回不完整且无告知）；`internal/mcp/server.go:372`（另一处相同的 4MB 截断）；`internal/api/webdav/dav.go:132-163`（`spillBuffer` — 仅 WebDAV 路径有 8MB spill 保护，REST/S3 Copy 走不同路径）；`cmd/server/main.go:216-218`（`MaxInFlight`/`PerTenantMax` — 仅控制并发请求数，不控制字节级吞吐） | ✅ **已验证** |
| **5** | **认证凭据生命周期管理：从静态键到全生命周期治理** | 安全/运维 | **P2** | API 键持久化已存在但无轮换工作流、无使用率仪表盘、JWT 签发后无法撤销（除非改全局 secret） | `internal/auth/store.go:19`（`PersistedKey.ExpiresAt` 字段——存储完整）；`internal/auth/auth.go:194-204`（`lookupStore` — **已检查** `pk.ExpiresAt`，过期时返回 false）；`internal/auth/auth.go`（`authenticateAPIKey` → `lookupStore` — 过期检查生效）；`internal/repository/apikeys.go:10`（`api_keys` 表 `expires_at`, `last_used_at`, `created_at` 列）；`internal/api/rest/admin.go:140,159`（密钥 CRUD 有审计日志—`key.add`, `key.revoke`）；`internal/middleware/middleware.go:84-95`（AccessLog 未记录 `key_hash` 或 `key_label`——无法追踪哪个 key 发出了哪个请求）；`internal/api/rest/admin.go:122-167`（无 `RotateAPIKey` 端点）；`internal/api/rest/admin.go:187-205`（`IssueJWT` — 无 JWT 黑名单/吊销端点） | ⚠️ **已验证（代码锚点修正）** |

---

## 方向一：服务层双写事务完整性

### 现状

系统的核心写入路径 `FileService.Put` 在不同持久化层之间缺乏原子性保证：

**1. Put 路径的存储→元数据 gap：**

```go
// internal/service/file_crud.go:100-119 简化流程  ✅ 代码验证
func (s *FileService) Put(ctx context.Context, ...) (repository.Object, error) {
    // ... 配额检查、key 校验、lock 检查 ...

    // STEP 1: 写入存储后端
    info, err := s.store.Put(ctx, sk, reader, size, ...)  // ← blob 已存在于磁盘/S3
    if err != nil { return ... }

    // STEP 2: MD5 校验后回滚
    if err := verifyMD5(); err != nil {
        s.store.Delete(ctx, sk)  // ← 只有 Content-MD5 有回滚
        return ...
    }

    // STEP 3: 构建对象元数据
    obj := s.buildPutObject(key, tenant, bucket, bcfg, opts, sk, versionID, info)

    // STEP 4: 写入元数据库
    saved, err := s.writePutObject(ctx, obj, bcfg)
    //   └─ repo.UpsertObject / repo.InsertObjectVersion
    //   └─ AddTenantUsage
    //   └─ emit(EventCreated)
    //   ← 若这里失败：blob 已写入存储，但元数据不存在
    //   ← 对象成为孤儿 blob，等待 Reconcile 清理（窗口期可达 IntervalMinutes）
    if err != nil { return repository.Object{}, err }

    return saved, nil
}
```

**crash 时间线：**

```
t1: store.Put 成功 → blob 写入磁盘
t2: ❌ 进程崩溃
t3: 重启后 → 元数据不存在 → blob 在存储中但不可访问
t4: Reconcile.maybeScrub/sweepOrphans 发现孤儿 → 删除 blob（但窗口期可能很长）
```

**2. 硬删除路径同样有多个 gap：**

```go
// internal/service/file_crud.go:297-332  ✅ 代码验证
func (s *FileService) hardDeleteObject(ctx context.Context, ...) error {
    // 1. 检查 Lock + Legal Hold
    // 2. chunkCleaner.DeleteObjectChunks  — 失败仅 warn（L304-307）
    // 3. store.Delete(ctx, obj.StorageKey) — 删除存储 blob（L309）
    // 4. repo.HardDeleteObject            — 删除元数据行（L312）
    // 5. repo.AddTenantUsage(..., -size, -1) — 扣减配额（L314，失败仅 warn）
    // 6. emit(EventDeleted)               — 发布事件（L316）
    //    ← 步骤 3 成功后步骤 4 失败: blob 已删但元数据存在（phantom metadata）
    //    ← 步骤 4 成功后步骤 5 失败: 元数据行已删但配额未扣减
}
```

**3. 当前保护措施：**

当前系统依赖两种机制来处理这些不一致：

| 机制 | 方式 | 限制 |
|------|------|------|
| `Reconcile.sweepOrphans` | 定时扫描存储中无元数据匹配的 blob → 删除 | 窗口期可达 `IntervalMinutes`（默认 15 分钟），大对象占用空间 15 分钟才释放 |
| Content-MD5 回滚 | 仅在 MD5 不匹配时删除 blob | 仅这一个回滚路径，其他错误没有回滚 |

### 产品价值

| 场景 | 当前行为 | 有 SAGA/补偿后 |
|------|---------|---------------|
| **服务器在 `store.Put` 后立即崩溃** | 孤儿 blob（15 分钟后 Reconcile 清理） | 启动时回滚 orphan + 同步补偿（写入前先记日志） |
| **元数据写入时 DB 连接超时** | blob 已存在存储中、元数据不完整 → 对象不可读 | 补偿事务自动清理孤立 blob + 日志告警 |
| **硬删除中途 store.Delete 成功但 repo.HardDeleteObject 失败** | phantom metadata：blob 已删除但元数据行残留，`Get` 返回无数据的对象 | 从 phantom 行可排查 + 自动重试 |
| **配额扣减失败但对象已删除** | 租户配额多扣（正偏差） | 补偿反熵线程校正 |
| **复制场景** | 主 storage 写入成功 → 复制 job 入队 → crash → job 被 reaper 重试 | job 幂等，无数据丢失 |

### 架构权衡

**建议方案：基于补偿日志的双写保护**

```
┌──────────────────────────────────────────────────────┐
│                  FileService                          │
│                                                       │
│  写入路径：                                            │
│  1) INSERT write_log(state=writing)                   │
│  2) storage.Put                                       │
│  3) repo.UpsertObject / InsertObjectVersion           │
│  4) UPDATE write_log(state=done)                      │
│  5) 异步 Reconcile 线程扫描 stale writing→回滚       │
│                                                       │
│  删除路径：                                            │
│  1) INSERT delete_log(state=deleting)                 │
│  2) store.Delete                                      │
│  3) repo.HardDeleteObject                             │
│  4) UPDATE delete_log(state=done)                     │
│  5) 异步 Reconcile 扫描 stale deleting→告警           │
└──────────────────────────────────────────────────────┘
```

**Phase 1：写入补偿日志 + 启动恢复**

引入一个轻量 `write_log` 表（迁移 0026），记录每次写入操作的 intent：

```sql
CREATE TABLE write_log (
    id INTEGER PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    bucket TEXT NOT NULL,
    key TEXT NOT NULL,
    storage_key TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'writing',  -- writing | done | rollback
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
```

写入路径注入 intent log：

```go
func (s *FileService) Put(ctx context.Context, ...) (repository.Object, error) {
    // ... 前置校验 ...

    // Phase 1: 记录 write intent
    logID, err := s.repo.InsertWriteLog(ctx, tenant, bucket, key, sk)
    if err != nil {
        // Write log failure is non-fatal — degrade to current orphan+Reconcile
        return s.putWithoutLog(ctx, ...)
    }

    // Phase 2: 写入存储
    info, err := s.store.Put(ctx, sk, reader, size, ...)
    if err != nil {
        s.repo.UpdateWriteLog(ctx, logID, "rollback")
        return ..., err
    }

    // Phase 3: 写入元数据
    saved, err := s.writePutObject(ctx, obj, bcfg)
    if err != nil {
        s.repo.UpdateWriteLog(ctx, logID, "rollback")
        s.store.Delete(ctx, sk)  // 主动回滚 blob
        return ..., err
    }

    // Phase 4: 标记完成
    s.repo.UpdateWriteLog(ctx, logID, "done")
    return saved, nil
}
```

**Phase 2：启动时回滚 + Reconcile 增强**

```go
// 启动时回滚
func (s *FileService) RecoverOrphanWrites(ctx context.Context) (int, error) {
    logs, err := s.repo.ListStaleWriteLogs(ctx, 5*time.Minute) // state=writing & created_at > 5min ago
    for _, l := range logs {
        if l.state == "writing" {
            // 检查元数据是否已存在（区分"写完未标记"和"写了一半"）
            if _, err := s.repo.GetObject(ctx, l.tenantID, l.bucket, l.key); err == nil {
                // 元数据已存在，说明是 UPDATE 状态失败——标记 done
                s.repo.UpdateWriteLog(ctx, l.id, "done")
            } else {
                s.store.Delete(ctx, l.storageKey)  // 回滚 blob
                s.repo.UpdateWriteLog(ctx, l.id, "rollback")
            }
        }
    }
}
```

```go
// Reconcile 增强：清除孤儿 blob 但优先基于 write_log 而非全表扫描
func (j *Reconcile) sweepOrphans(ctx context.Context) {
    // 现有逻辑：ListStorage → 对比 DB → 删除
    // 增强：优先扫描 write_log state=rollback 的键
}
```

**Phase 3：删除路径补偿（可选）**

硬删除路径的补偿更复杂——`store.Delete` 不保证可回滚（S3 无 undelete）。方案：

```go
func (s *FileService) hardDeleteObject(ctx context.Context, ...) error {
    // 记录 delete intent
    logID := s.repo.InsertDeleteLog(ctx, obj.ID, obj.StorageKey)

    // 逐步执行，每步成功更新 log
    if err := s.store.Delete(ctx, obj.StorageKey); err != nil {
        s.repo.UpdateDeleteLog(ctx, logID, "failed", err.Error())
        return err
    }
    s.repo.UpdateDeleteLog(ctx, logID, "store_deleted")

    if err := s.repo.HardDeleteObject(ctx, ...); err != nil {
        // blob 已删除，元数据行保留但标记为 dangling
        s.repo.UpdateDeleteLog(ctx, logID, "metadata_failed", err.Error())
        return err  // 返回 error 但 blob 已删
    }
    // ...
    s.repo.UpdateDeleteLog(ctx, logID, "done")
}
```

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **`InsertWriteLog` 本身失败** | Write log 是辅助补偿，不应阻塞主路径——`InsertWriteLog` 失败时可跳过补偿（`log fallback mode`），退化到当前 Reconcile 清理策略 |
| **Reconcile 清理时 blob 正被读取** | 不处理——`store.Delete` 在 OS 层面是 unlink，已有打开 fd 的读取方继续读到 EOF；后续读取返回 `ErrNotFound` |
| **`UpdateWriteLog` 失败但主路径成功** | 日志状态为 `writing` 但对象元数据已存在——启动恢复时需检查元数据是否存在来区分"写完未标记"和"写了一半" |
| **大量 `write_log` 行堆积** | `write_log` 表按 `updated_at` TTL 清理（如 24 小时后删除 `done`/`rollback` 状态的行） |
| **S3 后端 `store.Delete` 不可回滚** | 删除路径补偿是尽力而为，不是回滚——日志用于审计和故障排查，而非撤销 |
| **与 Idempotency-Key 交互** | 幂等请求如果第二次命中补偿日志的写了一半状态——不应重放 blob，应等待补偿完成或 `409 Conflict` |

---

## 方向二：桶通知配置运行时缺口

### 现状 ✅ 全部代码锚点验证通过

桶通知功能（Bucket Notifications）处于**"持久层完整，运行时全缺"**的状态：

**1. 持久化层完整：** 从 DB 到 API 都正确实现

```sql
-- migrations/0024_bucket_notifications.up.sql
ALTER TABLE buckets ADD COLUMN notification_rules TEXT;
-- JSON 格式示例：
-- [{"id":"rule1","events":["s3:ObjectCreated:*"],"queue_arn":"arn:..."}]
```

```go
// REST handler — 完整的 CRUD  ✅ handler.go:551-588
GET    /v1/buckets/{bucket}/notification   → GetBucketNotifications  ✅
PUT    /v1/buckets/{bucket}/notification   → SetBucketNotifications  ✅
DELETE /v1/buckets/{bucket}/notification   → DeleteBucketNotifications  ✅

// S3 handler — 解析 XML 持久化为 JSON  ✅ s3compat/handler.go:809-833
PUT /{bucket}?notification → putBucketNotifications  ✅
GET /{bucket}?notification → getBucketNotifications  ✅
```

**2. 运行时路径完全缺失：**

```go
// internal/events/bus.go:67-75 — 事件发布路径  ✅ 代码验证
func (b *Bus) Publish(ctx context.Context, e repository.Event) {
    id, err := b.repo.InsertEvent(ctx, e)
    // ...
    b.broadcast(e)  // ← 无条件广播给所有 subscriber
    // ← 从不检查 e.Bucket 的 notification_rules
    // ← 从不按规则路由到特定 SQS/SNS/Lambda/Webhook
}

// bus.go:114 — broadcast 无条件发送
func (b *Bus) broadcast(e repository.Event) {
    b.mu.RLock()
    defer b.mu.RUnlock()
    for _, ch := range b.subs {
        select {
        case ch <- e:
        default:
            b.dropped.Add(1)  // 缓冲满丢弃
        }
    }
}
```

所有事件消费者（订阅者）都是通过 `bus.Subscribe()` 收到**所有事件**，无 per-rule 过滤。

**3. 规则模型的明确 gap：**

```go
// internal/repository/repository.go:58-61  ✅ 代码验证
type NotificationRule struct {
    ID        string   `json:"id,omitempty"`
    Events    []string `json:"events"`     // e.g. ["s3:ObjectCreated:*", "s3:ObjectRemoved:*"]
    QueueARN  string   `json:"queue_arn"`  // used
    TopicARN  string   `json:"topic_arn,omitempty"`   // ← 注释: unused, kept for compat
    LambdaARN string   `json:"lambda_arn,omitempty"`  // ← 注释: unused, kept for compat
}
```

`TopicARN` 和 `LambdaARN` 明确标记为未使用——意味着 SNS 主题和 Lambda 函数的通知目标虽然被 S3 XML 解析器解析、被持久化为 JSON，但**完全不会被执行**。

### 产品价值

| 场景 | 当前行为 | 有运行时路由后 |
|------|---------|---------------|
| **用户只想在对象删除时收到通知** | 收到所有事件（创建、访问、删除）→ 客户端必须自行过滤 | 规则 `events:["s3:ObjectRemoved:*"]` → 仅删除事件投递 |
| **多桶多目标路由** | 所有事件发往单一 webhook URL | 桶 A → SQS 队列；桶 B → Lambda；桶 C → 另一个 webhook |
| **S3 兼容性测试** | 返回成功但事件从不实际投递到 SQS/SNS | 通过 SQS/SNS/Lambda 目标实际投递（或至少记录到 audit log） |
| **事件审计** | 无 per-bucket 事件流量视图 | 每个规则可统计 matching events delivered / dropped |

### 架构权衡

**建议方案：Notification Router 组件**

```
                    ┌──────────────┐
                    │  EventBus    │
                    │  Publish(e)  │
                    └──────┬───────┘
                           │
                    ┌──────▼───────┐
                    │NotifRouter   │ ← 新增组件
                    │              │
                    │ 1. 读取 e.  │
                    │    Bucket   │
                    │    的 rules  │
                    │ 2. 匹配事件  │
                    │    类型      │
                    │ 3. 路由到目  │
                    │    标        │
                    └──────┬───────┘
                           │
          ┌────────────────┼────────────────┐
          ▼                ▼                ▼
   ┌──────────┐    ┌──────────┐    ┌──────────┐
   │ Webhook  │    │  SQS     │    │  Audit   │
   │ (单个)   │    │ (需 AWS) │    │ (记录)   │
   └──────────┘    └──────────┘    └──────────┘
```

**Phase 1（P0）：诊断+审计模式**

不立即实现 SQS/SNS/Lambda 实际投递，而是：

1. 在 `Bus.Publish` 路径中增加 `notification_rules` 匹配日志
2. 所有事件旁路到 `notification_match_log` 表，记录哪些规则匹配了什么事件
3. 暴露 `GET /v1/admin/notifications/stats` 查看匹配率

```go
// Bus.Publish 增强
func (b *Bus) Publish(ctx context.Context, e repository.Event) {
    id, err := b.repo.InsertEvent(ctx, e)
    // ... 现有逻辑 ...

    // 新增：通知规则匹配（diagnostic mode）
    rules, err := b.repo.GetBucketNotifications(ctx, e.TenantID, e.Bucket)
    if err == nil && len(rules) > 0 {
        for _, rule := range rules {
            if eventMatchesPattern(e.Type, rule.Events) {
                b.logger.Debug("notification rule matched",
                    "rule_id", rule.ID, "event", e.Type, "target", rule.QueueARN)
                // Phase 1: 仅记录日志
                // Phase 2: 实际投递到目标
            }
        }
    }

    b.broadcast(e)
    // ...
}
```

**Phase 2：SQS/Lambda/Webhook HTTP 投递**

```go
type NotificationTarget interface {
    Deliver(ctx context.Context, e repository.Event) error
    Type() string  // "sqs" | "lambda" | "webhook"
}

type SQSNotificationTarget struct {
    QueueARN string
    client   *sqs.Client
}
func (t *SQSNotificationTarget) Deliver(ctx context.Context, e repository.Event) error {
    // 将事件封装为 SQS 消息（JSON envelope）
    _, err := t.client.SendMessage(ctx, &sqs.SendMessageInput{
        QueueUrl:    &url,
        MessageBody: aws.String(string(eventJSON)),
    })
    return err
}

type LambdaNotificationTarget struct {
    FunctionARN string
    client      *lambda.Client
}
func (t *LambdaNotificationTarget) Deliver(ctx context.Context, e repository.Event) error {
    // 调用 Lambda 函数
}
```

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **通知目标不可达（SQS 队列不存在）** | 投递失败 → 重试（指数退避）→ 永失则记录到 `notification_failures` 表 |
| **通知目标延迟阻塞主路径** | 投递异步（goroutine + channel + 工作池），主 `Publish` 路径不阻塞 |
| **同一事件的多个规则冲突** | 所有匹配规则独立执行（扇出），无去重——客户端自行幂等处理 |
| **事件类型模式匹配** | `s3:ObjectCreated:*` 匹配所有创建事件；`s3:ObjectCreated:Put` 仅匹配 PUT；`*` 匹配所有 |
| **批量事件风暴** | 通知投递使用与 job pool 独立的 worker pool（如 `NOTIF_WORKERS=4`）避免消费者饿死 |
| **规则变更同步** | `SetBucketNotifications` 后通知路由器应刷新缓存——使用事件总线发布 `notification.rules.changed` 事件 |

---

## 方向三：DB 驱动特性不对称与无优雅降级

### 现状 ⚠️ 代码锚点已修正（分析最初版本的 `pg_try_advisory_lock` 不存在、`ReapStuckJobs` 不含 `SKIP LOCKED`、`CountJobsByStatus` 不使用 `json_extract`；以下已更新为准确锚点）

系统正式支持 SQLite 和 Postgres 两种数据库驱动，但两者之间存在功能鸿沟，且缺乏运行时检测和优雅降级：

**1. 功能矩阵：**

| 功能 | SQLite | Postgres | 当前降级行为 | 代码证据 |
|------|--------|---------|-------------|---------|
| `events.Transport=postgres`（LISTEN/NOTIFY） | ❌ 不支持 | ✅ 支持 | 配置接受，静默失效——`events/postgres_transport.go` 使用 `pgx` 硬依赖，SQLite 时 goroutine 不启动但无用户可见日志 | `postgres_transport.go:1-30` |
| `AI_VECTOR_BACKEND=pgvector` | ❌ 不支持 | ✅ 支持 | 已有 warn log，优雅降级 ✅ | `ai/pgvector.go` |
| `AI_LEXICAL_BACKEND=pgfts` | ❌ 不支持 | ✅ 支持 | 已有 warn log，优雅降级 ✅ | `ai/pgfts.go` |
| `ClaimJob` 的 `SKIP LOCKED` | ❌ 不支持 | ✅ 支持 | SQLite 路径用事务+无锁查询替代——已处理✅ | `jobs.go:96-130` |
| `EVENTS_TRANSPORT_DSN` 独立连接串 | ❌ 不适用 | ✅ | 无检查 | 配置层 |
| `json_extract` 索引 | ⚠️ 无索引，全表扫描 | ✅ GIN/表达式索引 | 不报错，SQLite 中性能退化 | `sql_objects.go:ListObjectsByTag`（客户端过滤） |
| 并发 `ClaimJob` | ⚠️ 写前锁定（串行化） | ✅ `FOR UPDATE SKIP LOCKED` | 行为差异不影响正确性，但 SQLite 中并发 worker 可能争用 | `jobs.go:96-130` |
| `Migrate` 事务迁移 | ✅ | ✅ | 正确 | 50 对迁移文件 |

**2. 关键代码证据：**

```go
// internal/repository/jobs.go:96-130 — 两套实现 ✅ 代码验证
func (s *sqlStore) ClaimJob(ctx context.Context, worker string) (Job, bool, error) {
    now := time.Now().UTC().Format(time.RFC3339Nano)
    if s.dialect == dialectPostgres {
        q := `UPDATE jobs SET status='running', attempts=attempts+1, worker=$1, ...
            WHERE id = (
                SELECT id FROM jobs WHERE status='pending' AND run_after <= now()
                ORDER BY priority DESC, id ASC LIMIT 1 FOR UPDATE SKIP LOCKED
            ) RETURNING ` + jobCols
        // Postgres: SKIP LOCKED — 并发安全
    }

    // SQLite: no SKIP LOCKED — claim inside a transaction with a guarded UPDATE.
    tx, err := s.db.BeginTx(ctx, nil)
    // ...
}
```

```go
// internal/events/postgres_transport.go:1-30 — 完全 Postgres 专用 ✅ 代码验证
// PostgresTransport bridges the otherwise in-process event Bus across replicas
// using Postgres LISTEN/NOTIFY.
type PostgresTransport struct {
    dsn     string
    channel string
    conn    *pgx.Conn  // 硬依赖 pgx
}
```

**3. 编译期无保护：**

Go 的编译模型下，所有代码无论使用哪个数据库驱动都会被编译。Postgres-only 的 SQL 函数调用（`LISTEN/NOTIFY`、`FOR UPDATE SKIP LOCKED`）在 SQLite 驱动的 `database/sql` 上执行时，不是编译失败而是运行时 SQL 错误或静默不执行——用户可能在生产中配置 SQLite + `events.transport=postgres` 才发现无效。

### 产品价值

| 场景 | 当前行为 | 有降级后 |
|------|---------|----------|
| **SQLite + events.transport=postgres** | 静默忽略（运输 goroutine 不启动但无用户可见日志） | 启动时 `WARN: postgres event transport requires Postgres; disabling` |
| **SQLite + AI_VECTOR_BACKEND=pgvector** | `OpenPgVectorIndex` 返回错误 → `logger.Warn("pgvector index disabled")` | ✅ 当前行为已经是优雅降级 |
| **Postgres 升级导致的新版本特性** | 无版本探针，假设可用 | 启动时 `SELECT version()` 探测，兼容性矩阵检查 |
| **CI 门禁** | 只测试 SQLite 路径，Postgres 路径覆盖不足 | 明确标记 Postgres-only 功能，CI 中对这些功能 skip |

### 架构权衡

**建议方案：Driver Capability Registry**

```go
// internal/repository/capability.go

// Capability enumerates DB-driver-dependent features.
type Capability string

const (
    CapEventTransport   Capability = "event_transport_postgres"   // LISTEN/NOTIFY
    CapVectorIndex      Capability = "pgvector"
    CapLexicalIndex     Capability = "pgfts"
    CapSkipLocked       Capability = "skip_locked"
)

// DriverCapabilities returns the set of capabilities supported by the current
// driver. At startup, the repository probes the backend and returns a set.
func DriverCapabilities(driver string) []Capability {
    switch driver {
    case "postgres":
        return []Capability{CapEventTransport, CapVectorIndex, CapLexicalIndex, CapSkipLocked}
    case "sqlite":
        return nil  // SQLite supports none of these
    default:
        return nil
    }
}

// CheckCapability is a helper used at startup to log warnings or fail fast.
func CheckCapability(driver string, cap Capability) bool {
    for _, c := range DriverCapabilities(driver) {
        if c == cap { return true }
    }
    return false
}
```

在 `main.go` 中各 Postgres-only 功能的启动点注入 capability 检查：

```go
// setupPostgresTransport — 增强
func setupPostgresTransport(ctx context.Context, cfg *config.Config, bus *events.Bus, repo repository.Repository, logger *slog.Logger) {
    if cfg.Events.Transport == "postgres" {
        if !repository.CheckCapability(cfg.DB.Driver, repository.CapEventTransport) {
            logger.Warn("postgres event transport requires postgres driver; disabling",
                "driver", cfg.DB.Driver)
            return
        }
        // ... 原逻辑
    }
}
```

**迁移计划：**

| 文件 | 当前行为 | 修改后 |
|------|---------|--------|
| `cmd/server/main.go:setupPostgresTransport` | 仅检查配置 | 增加 capability check |
| `internal/events/postgres_transport.go` | 无 capability check | 构造时检查驱动 |
| `internal/ai/pgvector.go` | 已有 warn | 增加明确的 driver capability 检查 |
| `internal/config/config.go:Validate` | 不检查 feature/driver 兼容性 | 增加 `validateFeatureDriver` 阶段 |

> 注意：`cluster/singleton.go` 的 `AcquireLease` 使用 `leases` 表的 `UPDATE + INSERT ON CONFLICT DO NOTHING` 实现，SQL 在所有驱动间可移植，**不是** Postgres-only 功能。`ReapStuckJobs` 使用简单 `UPDATE WHERE status='running' AND started_at <= cutoff`，不含 `SKIP LOCKED`（`SKIP LOCKED` 仅在 `ClaimJob` 中存在，且已做 SQLite 降级处理）。

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **Postgres 功能在 SQLite 上静默退化** | 原则：**fail loud** — 用户配置了不支持的功能应收到清晰的 WARN 日志（启动过程的一次性告警），而非运行时报错或静默忽略 |
| **List/Count 性能退化（SQLite 无 JSON 索引）** | 不是配置错误——可通过文档和 `_aero_notes` 告知用户 SQLite 上 `ListObjectsByTag` 全表扫描（客户端过滤）；无代码级告警 |
| **`SKIP LOCKED` 降级** | SQLite 的 `ClaimJob` 已退化到事务加无锁查询——可能拿到正在被其他 worker 处理的 job，但 reaper 兜底 |
| **跨驱动迁移脚本同步** | 当前已维护 48 对迁移文件——保持同步 |
| **新增驱动支持（如 MySQL）** | Capability 模型天然支持扩展——`mysql` driver 返回 `[]Capability{...}` |

---

## 方向四：流式路径内存压力管理

### 现状 ✅ 核心代码锚点验证通过

系统在多个热路径上使用无内存上限的 I/O 操作，缺乏全局内存预算和背压机制：

**1. Range skip 全量读取丢弃的字节：**

```go
// internal/service/range.go:122-125  ✅ 代码验证
func (s *FileService) GetRange(ctx context.Context, tenant, bucket, key string, offset, length int64) (io.ReadCloser, repository.Object, error) {
    rc, obj, err := s.Get(ctx, tenant, bucket, key)
    // ...
    if offset > 0 {
        if _, err := io.CopyN(io.Discard, rc, offset); err != nil {
            // ← 跳过的 offset 字节被完全读取到 discard
            // ← 如果 offset=1GB，1GB 数据从存储读取并通过内核丢弃
            // ← 占用存储读取带宽，不释放给其他请求
        }
    }
    // ...
}
```

**2. MCP `read_file` 的 4MB 硬截断：**

```go
// internal/mcp/server.go:249-250  ✅ 代码验证
body, err := io.ReadAll(io.LimitReader(rc, 4<<20))
// ← 4MB 上限：大文件返回不完整且无告知
// ← 全部读入内存（4MB 或更少）

// server.go:372 — 另一处相同模式
body, err := io.ReadAll(io.LimitReader(rc, 4<<20))
```

**3. S3 CopyObject 流式无额外缓冲（相对安全）：**

```go
// internal/api/s3compat/extra.go:39-72  ✅ 代码验证
// copyObject streams from Get to Put — no in-memory buffering
func (h *Handler) copyObject(w http.ResponseWriter, r *http.Request, ...) {
    rc, src, err := h.svc.Get(r.Context(), tenant, srcBucket, srcKey)
    // ...
    dst, err := h.svc.Put(r.Context(), tenant, dstBucket, dstKey, rc, src.Size, opts)
    // rc 直接从 Get 传递给 Put，流式传输
}
```

> **修正说明：** 分析最初版本声称 REST handler 有 `io.Copy` 内存中转的 CopyObject 端点，但经代码验证 REST handler 无 CopyObject 端点。S3 compat 的 CopyObject 是流式的，无额外内存缓冲。

**4. 存储层 `Get` 返回原始 Reader（无 stream wrapper）：**

```go
// internal/service/file_crud.go:222-238  ✅ 代码验证
rc, _, err := s.store.Get(ctx, obj.StorageKey)
// ← rc 直接返回给调用方
// ← 不限制读取速率、不追踪读取量、不设置 read deadline
```

**5. 缺乏全局内存视图：**

```go
// 系统各处独立使用内存，但无人知道"当前正在使用多少内存用于流式 I/O"
// - 10 个并发 GET 500MB 文件 = 5GB 读取带宽（内核页缓存 + 应用内存）
// - 5 个并发 PUT 1GB 文件 = 5GB 写入缓冲（store.Put 实现细节不同）
// - MCP read_file 同时处理 100 个 4MB 文件 = 400MB 堆内存
// → 无全局上限，无 admission control
```

### 产品价值

| 场景 | 当前行为 | 有内存管理后 |
|------|---------|-------------|
| **Range skip 1GB** | 1GB 数据全量读取并丢弃，占用 IO 带宽 | `store.GetRange`（后端原生 Range）直接跳过字节，零成本 |
| **MCP 读取 100MB 日志文件** | 返回前 4MB（截断） | 流式分块返回或按需读取 |
| **10 个并发 500MB 对象 GET** | 5GB 读取流量，内存压力，可能 OOM | 受 `MaxStreamBytesPerConn` 限制，超出时降速或 503 |
| **慢客户端读取大对象** | 服务器 goroutine 保持打开直到传输完成（可能数分钟） | `ReadIdleTimeout` 中断慢读取，释放 goroutine |
| **S3 外部 COPY 目标** | 流式中转（当前已相对安全） | 增加监控包装器 |

### 架构权衡

**建议方案：三层流式内存保护**

```
┌──────────────────────────────────────────────────────────┐
│ Layer 1: Storage 原生 Range                              │
│ 后备存储支持原生 Range 时，跳过零成本                      │
│ 当前：所有后端都用 Get + io.CopyN(io.Discard)              │
│ 目标：Storage 接口增加 GetRange 方法                        │
└──────────────────────────────────────────────────────────┘
┌──────────────────────────────────────────────────────────┐
│ Layer 2: Reader 包装层                                   │
│ 每次 store.Get 返回监控 Reader：                           │
│  - 记录读取字节数                                        │
│  - 限制读取速率（rate limiter）                            │
│  - 设置读取超时（ReadDeadline）                            │
└──────────────────────────────────────────────────────────┘
┌──────────────────────────────────────────────────────────┐
│ Layer 3: 全局流式内存预算                                │
│ 全局 atomic 计数器：当前流式读取字节数                     │
│ 超过阈值时新请求被排队或降速                               │
└──────────────────────────────────────────────────────────┘
```

**Phase 1：Storage 接口增加 `GetRange` 方法**

```go
type Storage interface {
    // ... 现有方法

    // GetRange reads a byte range from an object without reading bytes outside
    // [offset, offset+length). When the backend supports server-side range (S3,
    // OSS, COS, HTTP Range), this avoids reading-and-discarding skipped bytes.
    // The caller must close the returned ReadCloser.
    GetRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, ObjectInfo, error)
}
```

各后端实现：

| 后端 | 实现 | 零成本跳过？ |
|------|------|-------------|
| `local` | `os.File` + `file.Seek(offset, 0)` + `io.LimitReader(file, length)` | ✅ 零成本 seek |
| `s3` | `s3.GetObjectInput` + `Range: "bytes=offset-offset+length-1"` | ✅ 服务端 Range |
| `oss` | `oss.GetObjectRequest` + `Range` 头 | ✅ 服务端 Range |
| `cos` | `cos.ObjectService.Get` + `Range` 头 | ✅ 服务端 Range |

```go
// service/range.go — 改进后
func (s *FileService) GetRange(ctx context.Context, tenant, bucket, key string, offset, length int64) (io.ReadCloser, repository.Object, error) {
    obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
    // ...
    // 使用新的 Storage.GetRange
    rc, _, err := s.store.GetRange(ctx, obj.StorageKey, offset, length)
    // ...
    return rc, obj, nil
}
```

**Phase 2：监控 Reader + 读取速率限制**

```go
type monitoredReader struct {
    rc        io.ReadCloser
    readBytes atomic.Int64
    // 可选：每 N 字节记录一次 OTel metric
}

type rateLimitedReader struct {
    r       io.Reader
    limiter *rate.Limiter  // 全局或 per-connection
}

func (s *FileService) Get(ctx context.Context, tenant, bucket, key string) (io.ReadCloser, repository.Object, error) {
    // ...
    rc, info, err := s.store.Get(ctx, obj.StorageKey)
    // 包装监控 Reader
    wrapped := &monitoredReader{rc: rc}
    // 可选速率限制（当 GET_RATE_LIMIT_BYTES_PER_SEC > 0 时）
    if cfg.StreamReadLimit > 0 {
        wrapped = &rateLimitedReader{r: wrapped, limiter: rate.NewLimiter(cfg.StreamReadLimit, cfg.StreamReadBurst)}
    }
    return wrapped, obj, nil
}
```

**Phase 3：全局流式内存预算（可选，高投入）**

使用 Go 1.19+ 的 `runtime/debug.SetMemoryLimit` + 自定义 `admission controller`：

```go
// 启动时
debug.SetMemoryLimit(4 << 30)  // 4GB 软上限

// 每个新读取请求前
func (s *FileService) canAllocate(bytes int64) bool {
    return s.streamingBytes.Load()+bytes < s.maxStreamingBytes
}
```

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **`GetRange` 在 local 后端 seek 后读到的内容少于 length** | 文档说明 `GetRange` 返回的 Reader 读取的字节数 ≤ length（短读是合法的）；调用方使用 `io.LimitReader` 保护 |
| **内存预算是估算而非精确** | 预算作为软上限（soft limit），超过时新请求降级（`Retry-After` 503 或限速）而非拒绝——硬上限由 Go 运行时 `SetMemoryLimit` 接管 |
| **监控 Reader 的 OTel 指标开销** | 每读取 1MB 记录一次指标（可配置 `STREAM_METRIC_INTERVAL_BYTES`），避免高频指标更新 |
| **速率限制导致客户端感知慢** | 这是设计行为——慢客户端应被限速而非让服务器 OOM；限制通过 `io.LimitedReader` 的 `Read` 调用阻塞实现 |
| **压缩对象 Range** | `GetRange` 作用于压缩字节流——若内容编码为 gzip，服务端 Range 返回压缩范围内的字节，解压后 Range 语义不同 |

---

## 方向五：认证凭据生命周期管理

### 现状 ⚠️ 代码锚点已修正（分析最初版本称 `expires_at`"从不自动检查"和无审计——经验证这两点不准确；以下为修正后的准确描述）

系统的 API 密钥管理支持持久化存储但缺乏完整的生命周期治理：

**1. 密钥过期检查已实现但不可见：**

```go
// internal/auth/auth.go:194-204  ✅ 代码验证（已检查过期）
func (r *Registry) lookupStore(ctx context.Context, hash, token string) (Key, bool) {
    now := time.Now()
    // ... cache check ...
    pk, found, err := r.store.GetAPIKeyByHash(ctx, hash)
    if err != nil || !found { return Key{}, false }

    if pk.ExpiresAt != "" {
        exp, perr := time.Parse(time.RFC3339, pk.ExpiresAt)
        if perr != nil {
            return Key{}, false  // 解析失败 → fail closed
        }
        if now.After(exp) {
            return Key{}, false  // ← 过期检查：已实现 ✅
        }
        keyExpiry = exp
    }
    // ...
    resolved := Key{Token: token, Tenant: pk.TenantID, Scopes: parseScopeString(pk.Scopes)}
    if r.keyCache != nil {
        r.keyCache.put(hash, resolved, keyExpiry, now)
    }
    return resolved, true
}
```

> **修正说明：** `expires_at` 的运行时检查**已经存在**。`lookupStore` 在 `pk.ExpiresAt` 非空时解析并判断过期，过期时返回 `(Key{}, false)`。但缺少：过期后的清理 job（定时扫表删除过期行）、过期前告警、不可用过期 key 的统计指标。

**2. 密钥 CRUD 有审计日志：**

```go
// internal/api/rest/admin.go:140  ✅ 代码验证
h.audit(r, "key.add", body.Tenant, fmt.Sprintf("token=%s scopes=%v", redactToken(body.Token), body.Scopes))

// admin.go:159
h.audit(r, "key.revoke", redactToken(tok), "")
```

> **修正说明：** 密钥的添加（`key.add`）和吊销（`key.revoke`）操作**已有审计日志**。

**3. 主要缺失项：**

| 缺失项 | 现状 | 代码证据 |
|-------|------|---------|
| **密钥轮换工作流** | 无 `RotateAPIKey` 端点 | `admin.go:122-167` 只有 `CreateAPIKey` 和 `RevokeKey` |
| **JWT 黑名单/吊销** | JWT 签发后唯一失效方式 = 改全局 `AUTH_JWT_SECRET` | `admin.go:187-205` 只有 `IssueJWT` |
| **AccessLog 不记录 key 信息** | 记录 tenant 但不记录 `key_hash` 或 `key_label` | `middleware.go:84-95` — log attrs 中无 key 字段 |
| **无 unused-key 自动检测** | `last_used_at` 字段存在但不定期检查 | `apikeys.go:10` — 列存在但无清理 job |
| **无自动过期清理** | 过期 key 被拒绝但未被自动删除 | `auth.go:194-204` — 只检查不清理 |
| **无每密钥使用率指标** | 无 `per-key` 请求计数 metric | 全局仅 OTel 总计数器 |

**4. JWT 无法撤销：**

JWT 签发后，唯一使其失效的方法是更改 `AUTH_JWT_SECRET`（这将使所有已签发的 JWT 失效）。没有 JWT 黑名单、没有 `jti` 撤销检查。

### 产品价值

| 场景 | 当前行为 | 有生命周期管理后 |
|------|---------|----------------|
| **密钥泄露** | 手动 `DELETE /v1/admin/keys/{token}`（已排查到 hash） | 即时撤销 + 批量轮换 + 告警 |
| **密钥过期** | 已自动拒绝过期 key ✅（但无清理和告警） | + 自动清理 + 过期前告警 + 统计 |
| **审计需求：谁创建了这个密钥？** | 已记录 key.add audit ✅ | + 可追溯创建者身份 |
| **安全审计：哪些密钥 90 天未使用？** | 手动 `SELECT * FROM api_keys` 无法关联 last_used | 定期报告 + 自动停用 + 告警 |
| **员工离职** | 所有密钥手动查找并删除 | 按 tenant 批量撤销 + 自动轮换 |
| **JWT 泄露** | 只能改全局 `AUTH_JWT_SECRET`（影响所有用户） | 加入 JWT 黑名单（`jti` or `sub` 验证） |
| **访问日志审计** | 无法追溯哪个 key 发起了哪个请求 | access log 记录 key_label |

### 架构权衡

**建议方案：三层凭据生命周期治理**

```
┌──────────────────────────────────────────────────────┐
│ Layer 1: 增强强制执行                                │
│ - 过期 key 返回 401 + X-Aero-Key-Expired header     │
│ - JWT 黑名单（DB + 缓存）                             │
│ - 定时清理过期 api_keys 行                           │
└──────────────────────────────────────────────────────┘
┌──────────────────────────────────────────────────────┐
│ Layer 2: 可观测性                                    │
│ - access log 记录 key_hash / key_label（从 context 提取）│
│ - per-key 请求计数器 metric                          │
│ - 定期 unused-key 报告                                │
└──────────────────────────────────────────────────────┘
┌──────────────────────────────────────────────────────┐
│ Layer 3: 管理 API 增强                               │
│ - POST /admin/keys/{token}/rotate → 新 key + old 宽限期 │
│ - POST /admin/jwt/revoke → 吊销指定 jti               │
│ - GET /admin/keys/usage → 使用率仪表盘                 │
└──────────────────────────────────────────────────────┘
```

**Phase 1（P0）：增强强制执行 + AccessLog**

```go
// auth/auth.go — 增强过期返回
func (r *Registry) authenticateAPIKey(ctx context.Context, key string) (Key, error) {
    hash := sha256Hex(key)
    pk, found, err := r.store.GetAPIKeyByHash(ctx, hash)
    // ... 现有逻辑 ...
    if pk.ExpiresAt != "" {
        exp, _ := time.Parse(time.RFC3339, pk.ExpiresAt)
        if time.Now().After(exp) {
            return Key{}, ErrKeyExpired  // 新错误类型
        }
    }
    // ...
}
```

```go
// middleware.go — AccessLog 记录 key 信息
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            // 从 context 中提取 key info（auth 中间件已设置）
            keyLabel := middleware.KeyLabelFrom(r.Context())
            attrs := []slog.Attr{
                slog.String("method", r.Method),
                slog.String("path", r.URL.Path),
                slog.String("key_label", keyLabel),
                // ...
            }
        })
    }
}
```

**Phase 2（P1）：密钥轮换 + JWT 撤销**

密钥轮换 API：

```
POST /v1/admin/keys/{token}/rotate
Request: {"replacement_delay_minutes": 1440}  // 旧 key 再存活 24h
Response: {
    "new_key": "aero_abc...",
    "old_token_hash": "sha256...",
    "old_expires_at": "2026-07-12T00:00:00Z"  // 旧 key 在此时间后自动过期
}
```

JWT 黑名单表：

```sql
CREATE TABLE jwt_blacklist (
    jti TEXT PRIMARY KEY,
    revoked_at TEXT NOT NULL,
    expires_at TEXT NOT NULL  -- 和 JWT 的 exp 一致，用于清理
);
```

在 JWT 验证路径上增加黑名单检查：

```go
func (r *Registry) authenticateJWT(ctx context.Context, token string) (Key, error) {
    claims, err := r.jwt.Verify(token)
    if err != nil { return Key{}, err }

    // JWT 黑名单检查（缓存命中时跳过 DB）
    if r.isJWTRevoked(claims.JTI) {
        return Key{}, ErrTokenRevoked
    }

    return Key{Tenant: claims.Tenant, Scopes: claims.Scopes}, nil
}
```

**Phase 3（P2）：密钥使用率仪表盘**

```go
// 新端点
GET /v1/admin/keys/usage?days=30
Response: {
    "keys": [
        {"label": "ci-cd", "requests_30d": 15230, "last_used": "2026-07-10T12:00:00Z", "status": "active"},
        {"label": "backup-script", "requests_30d": 0, "last_used": "2026-04-01T00:00:00Z", "status": "inactive"},
    ]
}
```

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **密钥过期后客户端仍在用** | `ErrKeyExpired` → 返回 `401 Unauthorized` + `X-Aero-Key-Expired: true` 头（帮助客户端诊断） |
| **密钥轮换期间旧密钥还在使用** | 旧密钥在 `replacement_delay_minutes` 内仍有效——到期后自动 401；宽限期内新密钥同时有效 |
| **JWT 黑名单膨胀** | 定期清理 `jwt_blacklist` 中 `expires_at < now` 的行（与 JWT 本身的过期时间一致） |
| **多副本 JWT 黑名单同步** | 通过 `auth.key.invalidate` 事件通道（已有 `PostgresTransport` 用于 key cache invalidation）同步 JWT 黑名单 |
| **误撤销** | 管理 API 提供 `POST /v1/admin/jwt/unrevoke`（在 JWT 过期前可恢复） |
| **性能：每次认证都查 DB 检查 expires_at** | key cache 中存储过期时间，cache hit 时在内存中检查——仅在 cache miss 时查 DB |

---

## 优先级与建议执行顺序

| 排序 | 方向 | 前置依赖 | 建议投入 | 核心交付物 |
|------|------|---------|---------|-----------|
| **1** | **方向三：DB 驱动特性不对称**（Phase 1：Capability Registry + 启动探测 + 3 个 Postgres-only 功能检查点 + 用户可见警告） | 无 | 1 周 | `repository.Capability` 枚举 + `CheckCapability` 函数 + 对 `events/postgres_transport`、`ai/pgvector`、`ai/pgfts` 注入检查 + 启动告警日志 |
| **2** | **方向一：服务层双写事务完整性**（Phase 1：`write_log` 表 + Put 路径 intent logging + 启动回滚 + Reconcile 增强） | 迁移 0026 | 2-3 周 | `write_log` 表 + `InsertWriteLog`/`UpdateWriteLog`/`ListStaleWriteLogs` + `RecoverOrphanWrites` 启动钩子 + `Put` 路径增强 |
| **3** | **方向四：流式路径内存压力管理**（Phase 1：`Storage.GetRange` 接口 + 4 后端实现 + `GetRange` 替换 `io.CopyN(io.Discard)`） | 无 | 2-3 周 | `Storage.GetRange` 接口 + local/S3/OSS/COS 实现 + `FileService.GetRange` 使用原生 Range + MCP `read_file` 可配置大小 |
| **4** | **方向五：认证凭据生命周期管理**（Phase 1：AccessLog key 信息 + JWT 黑名单 + 清理 job） | 无 | 2-3 周 | `access_log` key_label + `jwt_blacklist` 表 + `ErrKeyExpired` header + 过期 key 清理 cron |
| **5** | **方向二：桶通知运行时缺口**（Phase 1：诊断模式 + 规则匹配日志 + audit trail + 指标） | 方向三的 Driver Capability（SQS 投递需要 Postgres 可靠） | 2-3 周 | `Bus.Publish` 规则匹配 + `notification_match_log` + `GET /v1/admin/notifications/stats` + Prometheus 指标 |

**建议执行策略：**

1. **Phase 1（方向三，1 周）**：DB 驱动特性不对称是「基础设施债」——修复它不产生可见的功能增量，但为后续所有方向提供正确的运行时诊断能力。高优先级低复杂度，适合冷启动。

2. **Phase 2（方向一 + 方向四并行，3-4 周）**：双写事务完整性和流式内存管理都是**生产可靠性核心问题**。方向一是数据一致性防线，方向四是运行时稳定性防线。两者正交，可并行推进。

3. **Phase 3（方向五 + 方向二并行，3-4 周）**：凭据生命周期和桶通知都是**产品/运维能力补充**。方向五依赖 auth 模块已有基础，方向二的 Phase 1（诊断模式）可在不实现实际投递的前提下提供运维可见性。

---

## 总结

以上五个方向覆盖了 aero-vault 在**数据一致性（双写事务完整性）、产品完整度（桶通知运行时）、基础设施健壮性（DB 驱动对称性）、运行时稳定性（流式内存压力）、安全运维成熟度（凭据生命周期）** 五个维度上尚未被前 121 轮分析独立覆盖的关键缺口。每个方向均有明确的代码锚点、可量化的生产影响、以及从现有架构渐进演进的可行性。

| 维度 | 当前状态 | 目标状态 |
|------|---------|---------|
| **数据一致性** | `Put` 路径 storage 与 metadata 无原子性；crash 后孤儿 blob | write_log 补偿 + 启动回滚 + 可观测 intent 日志 |
| **产品完整度** | 桶通知配置持久化但从不执行——CRUD 是全的，路由是空的 | 诊断匹配 + SQS/Lambda/webhook 投递 + 指标 |
| **基础设施健壮性** | Postgres-only 功能在 SQLite 上静默或运行时错误 | Capability Registry + 启动探测 + 清晰降级 |
| **运行时稳定性** | `io.CopyN(io.Discard, offset)` 浪费带宽；无全局内存预算 | Storage.GetRange 原生跳过 + 监控 Reader + 流控 |
| **安全运维成熟度** | 密钥过期已检查但无轮换/审计追踪(已部分实现)/JWT 不可撤销 | 轮换工作流 + 操作审计 + 使用仪表盘 + JWT 黑名单 |

### 代码审查修正记录

与最初版本相比，以下代码锚点已根据实际代码库验证结果修正：

| 初始分析声明 | 验证结果 | 修正 |
|------------|---------|------|
| `cluster/singleton.go` 使用 `pg_try_advisory_lock` | ❌ 不存在。Singleton 使用 `leases` 表 + 可移植 SQL | 已删除该锚点 |
| `ReapStuckJobs` 使用 `SKIP LOCKED` | ❌ `SKIP LOCKED` 在 `ClaimJob` 中，非 `ReapStuckJobs` | 已修正为 `ClaimJob` |
| `CountJobsByStatus` 使用 `json_extract` | ❌ 简单 `COUNT(1) WHERE status=$1` | 已删除该锚点 |
| REST handler CopyObject 使用 `io.Copy` 内存中转 | ❌ REST handler 无 CopyObject；S3 CopyObject 流式传输 | 已修正描述 |
| `expires_at` 从不检查 | ❌ `auth.go:lookupStore` 已检查过期 | 已修正为"已检查但缺少清理/告警" |
| 密钥 CRUD 无审计 | ❌ `admin.go:140,159` 已有 `key.add` 和 `key.revoke` 审计 | 已修正描述 |
