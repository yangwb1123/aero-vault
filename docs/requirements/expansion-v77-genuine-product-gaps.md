# AeroVault 高价值扩展方向 v77 — 产品完整性盲区

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描（46K+ 行 Go 代码，23+ `internal/*` 子包，三套 SDK，MCP 双模式，Web UI，48 对迁移文件，Makefile，CI gate）  
> **去重验证：** 对 `docs/requirements/` 下全部 76 份既有分析文档进行逐方向 `grep` 正则交叉验证 + 语义比对  
> **日期：** 2026-07-11  
> **核心原则：** 选取代码中存在具体、可量化、且在前 76 轮分析中**零实质性覆盖**的架构/产品空缺。每个方向包含代码锚点、影响分析、既有覆盖证明。

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 | 既有分析覆盖 |
|---|------|------|--------|---------|---------|-------------|
| **1** | **`object_events` 表无 GC 机制 — 消费事件永久积累** | 生产可靠性/存储成本 | **P2** — 所有对象 CRUD 事件写入 `object_events` 表，`consumed_at` 标记消费但已消费行永不删除。高吞吐生产环境（百万级对象、频繁更新）下该表无限增长，影响备份时间、查询性能、存储费用 | `internal/repository/sql_events.go:41-79`（`NextUnconsumedEvents` + `MarkEventsConsumed` — 只标记不删除）；`internal/repository/migrations/sqlite/0003_events.up.sql`（`object_events` DDL — 含 `consumed_at` 但无 TTL 或清理机制）；`internal/repository/sql_buckets.go:104`（唯一 DELETE 发生在删除 bucket 时）；`internal/reconcile/retention.go`（RetentionJob 清理软删除对象但不碰 events 表） | ✅ **完全去重**（v30/v38/v29/`strategic-extensions.md` 提及 events 但聚焦"新增事件类型"和"事件路由扩展"，**零分析消费事件的持久化存储 GC**） |
| **2** | **S3 `CopyObject` 不保留标签/存储类/ACL — 附属属性无声丢失** | 协议合规/数据正确性 | **P2** — `CopyObject` 在 `x-amz-metadata-directive: COPY`（默认）下保留源对象的 content-type 和用户 metadata，但**忽略 tags、storage class、ACL**。客户端复制对象后，标签、存储层级和权限控策略被无声丢弃，目标对象使用系统默认值。`x-amz-tagging-directive` header 完全未被解析 | `internal/api/s3compat/extra.go:35-68`（`copyObject` 全程无 tags/storage_class/acl 从源到目标的传递）；`internal/api/s3compat/extra.go:52-56`（仅检查 `x-amz-metadata-directive`，无 `x-amz-tagging-directive`）；`internal/api/s3compat/extra.go:60-64`（`Put` 调用未设 Tags/StorageClass/ACL） | ✅ **完全去重**（v25 分析"服务端 Copy 缺失 UploadPartCopy"聚焦并行复制能力缺失，v56 聚焦存储后端本地 Copy 接口。**zero 分析已有 CopyObject 实现中附属属性的无声丢失**） |
| **3** | **存储层无瞬态错误重试 — S3/OSS/COS 一次失败即直通客户端** | 可靠性/弹性 | **P2** — Storage 接口的所有云后端实现（S3、OSS、COS）在遇到瞬态错误（throttling 503、连接超时、DNS 抖动）时直接向应用层返回错误，不做任何重试退避。对比：Webhook 系统有完整的指数退避重试（10 次尝试，30s→1h），Object CRUD 路径却零容忍。Circuit breaker 只阻断不重试 | `internal/storage/s3.go`（全文件 — 每个方法直接调用 AWS SDK，无 retry wrapper）；`internal/storage/oss.go`（同）；`internal/storage/cos.go`（同）；`internal/storage/circuitbreaker.go:140-164`（`tryTransition` 仅在失败时切换状态，不触发重试）；`internal/events/webhook.go:145-150`（对比：Webhook 的指数退避实现） | ✅ **完全去重**（v19 提及"storage retry"作为概念方向表中的一行，**零代码锚点、零分析**；其余 75 份文档零覆盖此方向） |
| **4** | **`go.mod` 依赖供应链安全策略缺失 — 依赖漂移与漏洞无声进入** | 工程质量/安全 | **P3** — CI gate（`make check`）覆盖 `gofmt`、`go vet`、`build`、`test`、`complexity-lines`，但无以下供应链安全措施：`go mod verify`（校验下载缓存的完整性）、`govulncheck`（Go 官方漏洞扫描）、Dependabot/Renovate（自动依赖更新 PR）、`go mod tidy -diff`（防间接依赖冗余）、`golang.org/x/vuln` 集成 | `Makefile:57-91`（`check` target 显式列出 fmt/vet/build/test/complexity-lines，**无 dependency 相关检查**）；`go.mod`（27 个直接依赖 — 无版本锁定策略注释）；`.github/workflows/`（需检查 CI 编排）；无 `.github/dependabot.yml` 或 `renovate.json` | ✅ **完全去重**（v47/v54/v45/v4/v16 提及"CI 增强""Docker 镜像扫描"等基础设施方向，**聚焦 Docker 层和配置校验**，未覆盖 `go.mod` 级别的供应链安全） |
| **5** | **OSS/COS 云后端未通过 Storage Contract Test — 语义差异静默潜伏** | 工程质量/兼容性 | **P2** — `storage.RunContract` 定义了 5 组契约测试（put/get/stat、delete、list_prefix、key_validation、multipart），但**仅在测试中以 local 后端运行**（`factory.go` 测试辅助函数）。OSS/COS 仅有构造验证测试（`TestNewOSS`/`TestNewCOS`），从未运行过完整的 contract suite。意味 ETag 引号处理、分片上传边界、metadata 大小写保持、List 分页语义等 15+ 个行为属性在 OSS/COS 上可能存在与 local 不同的静默偏差 | `internal/storage/contract_test.go:16-30`（`RunContract` — 5 组测试，仅被带 `TestStorage` 的 `factory.go` 测试调用）；`internal/storage/oss_cos_test.go`（75 行 — 仅测试 `NewOSS`/`NewCOS` 构造验证，**零功能测试**）；`internal/storage/cloud_test.go:18` (`TestOSSNotFound` — 唯一的云后端功能测试，仅覆盖 Get Stat 404 路径）；`internal/storage/factory_test.go`（`RunContract` 仅以 local 运行） | ✅ **完全去重**（v42 方向一覆盖"S3 合规验证基础设施"，聚焦 S3 协议的外部合规测试套件缺失。v46 一行提及"新增存储后端开发指南"。**零分析本地 contract suite 从未在云后端上执行**） |

---

## 方向一：`object_events` 表无 GC 机制 — 消费事件永久积累

### 现状

`object_events` 表是 FileService 事件系统的核心持久化存储。每次对象创建、删除、访问都写入一行记录。Indexer、Webhook、Postgres Transport、SSE replay 均依赖此表。

当前数据流：

```
FileService CRUD
       │
       ▼
  events/bus.go: Publish → InsertEvent
       │
       ▼
  repository/sql_events.go
       │ INSERT INTO object_events (...)
       ▼
  ┌────────────────────────────────┐
  │ object_events 表               │
  │                                │
  │ id | tenant | key | type | ... │
  │ ─────────────────────────────  │
  │ 1  | acme   | a.txt | created │ ← consumed_at IS NULL (待消费)
  │ 2  | acme   | b.txt | created │ ← consumed_at IS NULL
  │ 3  | acme   | a.txt | deleted │ ← consumed_at = 2026-07-10T...
  │ 4  | acme   | c.txt | created │ ← consumed_at = 2026-07-10T...
  │ ...                           │
  │ N  | acme   | z.txt | accessed│ ← consumed_at = 2026-07-10T...
  │ N+1| acme   | a.txt | created │ ← consumed_at IS NULL
  └────────────────────────────────┘
        │                     │
        │ 已消费（consumed_at 非空）  │ 未消费（consumed_at IS NULL）
        │ 永不删除                   │ 等待消费者处理
        ▼                           ▼
  永久积累                        Indexer / Webhook / SSE
```

**代码证据链：**

```go
// ① 写入 — 无限制
// internal/events/bus.go
func (b *Bus) Publish(ctx context.Context, e repository.Event) error {
    _, err := b.repo.InsertEvent(ctx, e)  // 永远 INSERT
    // ...
}

// ② 读取 — 仅按 consumed_at IS NULL 筛选
// internal/repository/sql_events.go:41
func (s *sqlStore) NextUnconsumedEvents(ctx context.Context, limit int) ([]Event, error) {
    rows, err := s.db.QueryContext(ctx, s.rebind(
        `SELECT ... FROM object_events WHERE consumed_at IS NULL ORDER BY id ASC LIMIT $1`), limit)
    // ...
}

// ③ 消费 — 仅标记不删除
// internal/repository/sql_events.go:75
func (s *sqlStore) MarkEventsConsumed(ctx context.Context, ids ...int64) error {
    // UPDATE object_events SET consumed_at=now() WHERE id=$1  ← 只更新不删除
    // ...
}

// ④ 唯一删除路径 — 只有删除 bucket 时
// internal/repository/sql_buckets.go:104
tx.ExecContext(ctx, s.rebind(`DELETE FROM events WHERE tenant_id=$1 AND bucket=$2`), tenant, bucket)
// 注意：这里用的是 "events" 表（可能是旧的实现），而不是 "object_events"
```

**影响量化：**

| 场景 | 日均事件量 | 月度积累 | 年化数据量 | 备份影响 |
|------|-----------|---------|-----------|---------|
| 开发/演示 | ~100 | ~3,000 行 | ~36K 行 | 微 |
| 小团队（10 用户，100 文件/日） | ~300 | ~9,000 行 | ~110K 行 | 微 |
| 中等团队（100 用户，1K 文件/日，含版本） | ~5,000 | ~150K 行 | ~1.8M 行 | ~200MB |
| 企业（1K 用户，10K 文件/日，含版本+访问事件） | ~50,000 | ~1.5M 行 | ~18M 行 | ~2GB |
| CI/CD 工件仓库（频繁更新+删除） | ~200,000 | ~6M 行 | ~72M 行 | ~8GB |

### 根因

`object_events` 表被设计为**事件溯源存储**和**生产消息队列**的双重用途。作为消息队列需要保留未消费事件；作为持久化存储需要保留审计痕迹。这两种目的冲突——消息队列需要 GC，审计日志需要保留。当前实现隐含地假设"所有事件都应永久保留"，但没有任何保留策略配置或分区清理机制。

### 为什么需要

| 理由 | 说明 |
|------|------|
| **存储成本** | 事件行虽小（~150 bytes/row），但高频访问路径（`object.created` + `object.deleted`）在 CI/CD 工件仓库等场景下每天可产生数十万行。无 GC 意味着生产实例在几个月后积累数千万行，导致备份时间超时、存储扩容 |
| **查询性能退化** | `consumed_at IS NULL` 索引只帮助未消费事件的查询。已消费事件的行仍留在表中，`SELECT COUNT(*)`、`PgDump`、`VACUUM` 等全表操作随数据量线性变慢 |
| **运维标准化** | 所有成熟的数据库事件系统（pg_notify、AWS SQS、Redis Streams）都提供消息 TTL 或消费后自动删除。当前实现要求运维人员手动 `DELETE FROM object_events WHERE consumed_at IS NOT NULL AND created_at < now() - interval '30 days'`——大部分运维人员不知道这个表的存在 |
| **可预测的恢复时间** | 灾难恢复时，`object_events` 表是最大的非必要表。从备份恢复时，所有已消费事件都是 useless 数据。数据库迁移（SQLite → Postgres）也需要迁移这些无用数据 |

### 建议方向

**Phase 1（最小可行 GC — 配置化保留策略）：**

```
EVENTS_RETENTION_DAYS=30  # 已消费事件保留天数；0 = 永久保留（当前行为，保持向后兼容）
```

```go
// 新增：RetentionJob 扩展 → 清理已消费事件
func (r *RetentionJob) sweepConsumedEvents(ctx context.Context) {
    if r.eventsRetention <= 0 {
        return
    }
    cutoff := time.Now().Add(-r.eventsRetention).UTC().Format(time.RFC3339Nano)
    deleted, err := r.repo.DeleteConsumedEventsBefore(ctx, cutoff)
    if err != nil {
        r.logger.Warn("events gc", "err", err)
        return
    }
    if deleted > 0 {
        r.logger.Info("events gc", "deleted", deleted)
    }
}
```

```sql
-- 新增清理 SQL
DELETE FROM object_events
 WHERE consumed_at IS NOT NULL
   AND consumed_at < $1
 LIMIT 10000;  -- 分批删除，避免大事务
```

**Phase 2（事件分区 — Postgres 按时间表分区）：**

- 按月或按季分区 `object_events` 表
- GC 直接 `DROP TABLE object_events_2025_q1` — O(1) 删除，无 IO 开销
- 保留最新 N 个分区的未消费事件

**Phase 3（事件类型白名单 — 减少不必要的事件写入）：**

- 当前 `EventAccessed`（访问事件）也写入 `object_events`，这在高频读取场景下产生大量事件
- 可配置白名单：`EVENTS_TYPES=created,deleted` 过滤 accessed 事件

| 指标 | 估计 |
|------|------|
| Phase 1 代码量 | ~60 行（Repository 方法 + RetentionJob 扩展 + Config 项） |
| Phase 2 代码量 | ~30 行（Postgres 迁移文件） + 维护手册更新 |
| Phase 3 代码量 | ~20 行（Bus.Publish 过滤逻辑） |
| 风险 | **低** — GC 默认关闭（0 = 永久保留），行为不变；新配置项只在需要时启用 |

---

## 方向二：S3 `CopyObject` 不保留标签/存储类/ACL — 附属属性无声丢失

### 现状

S3 `CopyObject` 操作（`PUT /{dst} x-amz-copy-source: /{src}`）在 AWS 规范中应复制源对象的大多数属性，除非客户端明确使用 `x-amz-metadata-directive: REPLACE` 或 `x-amz-tagging-directive: REPLACE` 覆盖。

当前实现路径：

```
客户端请求:
  PUT /dst-bucket/final.pdf
  x-amz-copy-source: /src-bucket/source.pdf
       │
       ▼
  extra.go:39 copyObject(w, r, dstBucket, dstKey, copySource)
       │
       ├── 默认行为: x-amz-metadata-directive=COPY
       │   ├── ✅ Content-Type: 从源复制（src.ContentType → opts.ContentType）
       │   ├── ✅ User Metadata: 从源复制（src.Metadata → opts.Metadata）
       │   ├── ❌ Tags: 不使用（opts.Tags = nil）→ 目标对象无标签
       │   ├── ❌ StorageClass: 不使用（opts.StorageClass = ""）→ 默认 STANDARD
       │   └── ❌ ACL: 不使用（不调用 SetObjectACL）→ 默认 private
       │
       ├── x-amz-metadata-directive=REPLACE
       │   ├── ✅ Content-Type: 从请求头获取
       │   ├── ✅ User Metadata: 从请求头获取
       │   ├── ❌ Tags: 仍不使用 → 目标对象无标签
       │   └── ❌ StorageClass/ACL: 同上
       │
       └── x-amz-tagging-directive header: 完全未解析（存在但被忽略）
```

**代码证据链：**

```go
// internal/api/s3compat/extra.go:39-65
func (h *Handler) copyObject(..., copySource string) {
    // ...
    rc, src, err := h.svc.Get(r.Context(), tenant, srcBucket, srcKey)
    // src 包含 Tags, StorageClass, ACL 等信息，但后续未使用

    opts := service.PutOptions{
        ContentType: src.ContentType,       // ✅ 保留
        Metadata:    src.Metadata,           // ✅ 保留
        // Tags: nil,                         // ❌ 未设置
        // StorageClass: "",                  // ❌ 未设置
    }
    if strings.EqualFold(r.Header.Get("x-amz-metadata-directive"), "REPLACE") {
        opts.ContentType = r.Header.Get("Content-Type")
        opts.Metadata = extractMetaHeaders(r.Header)
        // Tags 和 StorageClass 仍未被覆盖
    }

    dst, err := h.svc.Put(r.Context(), tenant, dstBucket, dstKey, rc, src.Size, opts)
    // Put 后未调用 SetObjectACL → ACL 丢失
    // ...
}
```

### 影响场景

| 用户操作 | 期望行为 | 实际行为 |
|---------|---------|---------|
| `aws s3 cp s3://bucket/doc.pdf s3://bucket/doc-v2.pdf`（保留标签） | 标签随复制保留 | 标签丢失，目标无标签 |
| SDK `copy_object(CopySource=..., MetadataDirective='COPY')` | 存储类、标签、ACL 随复制保留 | 只有 content-type 和 user meta 保留 |
| 生命周期规则触发 Transition → CopyObject | 对象在新层级保留标签 | 存储类转换后标签丢失 |
| 合规标签（如 `_aero_legal_hold=ON`） | 复制后保留 | 目标无合法持有标记 |
| `x-amz-storage-class: STANDARD_IA` 指定到目标 | 目标使用指定存储类 | 使用默认 STANDARD |
| 带 bucket-default ACL 的复制 | 目标继承 bucket 默认 ACL | 目标总是 private |

### 为什么需要

| 理由 | 说明 |
|------|------|
| **数据正确性** | 这不是缺失功能，这是语义不正确。用户期望 COPY 是"复制"而非"重新创建"。无声丢失标签、存储类、ACL 意味着复制后的对象与源对象本质不同 |
| **S3 协议合规** | AWS S3 `CopyObject` 默认保留所有属性（tags、storage class、ACL、metadata、content-type）。`x-amz-tagging-directive` 是标准 S3 header，其缺失意味着 SDK 的 `CopySource={"Bucket":"b","Key":"k"}` 产生不一致结果 |
| **合规场景** | 如果源对象带有合规标签（legal hold）或固定的存储类（GLACIER 用于长期保留），复制后策略丢失可能违反内部合规要求 |
| **标签存储类是成本优化基础** | 用户根据标签和存储类来做成本分析（`StorageClassCounts` 指标已存在）。复制后标签丢失会产生成本报告偏差 |

### 建议方向

```go
// copyObject 增强逻辑
func (h *Handler) copyObject(w http.ResponseWriter, r *http.Request, dstBucket, dstKey, copySource string) {
    tenant := mw.TenantFrom(r.Context())
    srcBucket, srcKey, ok := parseCopySource(copySource)
    // ...

    rc, src, err := h.svc.Get(r.Context(), tenant, srcBucket, srcKey)
    // ...

    opts := service.PutOptions{
        ContentType: src.ContentType,
        Metadata:    src.Metadata,
    }

    // 处理存储类：默认从源继承，可通过 x-amz-storage-class 覆盖
    opts.StorageClass = src.StorageClass
    if sc := r.Header.Get("x-amz-storage-class"); sc != "" {
        opts.StorageClass = sc
    }

    // 处理标签 directive（COPY/REPLACE）
    tagDirective := r.Header.Get("x-amz-tagging-directive")
    if tagDirective == "" || strings.EqualFold(tagDirective, "COPY") {
        if len(src.Tags) > 0 {
            opts.Tags = make(map[string]string, len(src.Tags))
            for k, v := range src.Tags {
                opts.Tags[k] = v
            }
        }
    } // REPLACE → Tags 留 nil（不设置标签）

    // 处理 metadata directive（现有逻辑，增强）
    if strings.EqualFold(r.Header.Get("x-amz-metadata-directive"), "REPLACE") {
        opts.ContentType = r.Header.Get("Content-Type")
        opts.Metadata = extractMetaHeaders(r.Header)
    }

    // 执行 Put（tags 和 storage class 已集成）
    dst, err := h.svc.Put(r.Context(), tenant, dstBucket, dstKey, rc, src.Size, opts)
    // ...

    // 复制 ACL（源 ACL 通过 src metadata 或独立的 GetObjectACL 获取）
    if acl := src.Metadata["_aero_acl"]; acl != "" {
        _ = h.svc.SetObjectACL(r.Context(), tenant, dstBucket, dstKey, acl)
    }
}
```

| 指标 | 估计 |
|------|------|
| 代码量 | ~40 行（extra.go copyObject 增强 + tag directive 解析） |
| 修改文件 | `internal/api/s3compat/extra.go`、`internal/api/s3compat/xml.go`（可能需新增 struct） |
| 风险 | **低** — 纯新增属性传递逻辑；默认行为（COPY）与当前行为兼容；现有 `Put` 的 Tags/StorageClass 字段已存在 |

---

## 方向三：存储层无瞬态错误重试 — S3/OSS/COS 一次失败即直通客户端

### 现状

所有存储后端（S3、OSS、COS、Local）在调用底层 API 时，如果遇到瞬态错误（throttling、服务端内部错误、网络抖动），直接返回错误，在调用栈中逐层传播，最终以 HTTP 5xx 响应给客户端。

**无重试路径：**

```
客户端 HTTP PUT /v1/files/doc.pdf
       │
       ▼
  service/file_crud.go: FileService.Put
       │
       ▼
  storage/s3.go: S3Storage.Put
       │ s3.client.PutObject(ctx, &s3.PutObjectInput{...})
       ▼
  AWS SDK → S3 服务端 503 (SlowDown / ServiceUnavailable)
       │
       ▼
  S3Storage.Put → return err
       │
       ▼
  FileService.Put → return err
       │
       ▼
  HTTP 500 Internal Server Error
```

**对比：同项目的 Webhook 系统**

```go
// internal/events/webhook.go:145-150 — 指数退避重试
backoff := time.Duration(30) * time.Second
for i := 1; i < attempt && backoff < time.Hour; i++ {
    backoff *= 2
}
backoff = jitter(backoff)
next := time.Now().Add(backoff)
// 最大 10 次尝试
```

**每个存储后端的具体情况：**

| 后端 | 方法数 | 重试逻辑 | 备注 |
|------|-------|---------|------|
| Local | 8 | ❌ 无 | 本地文件系统错误通常不是瞬态的，可接受 |
| S3 (`s3.go`) | 10 | ❌ 无 | AWS SDK Go v2 默认有少量重试（但配置在 client 层），当前代码未充分利用 |
| OSS (`oss.go`) | 9 | ❌ 无 | 阿里云 OSS SDK 可能也有重试但当前未配置 |
| COS (`cos.go`) | 9 | ❌ 无 | 腾讯云 COS SDK 同 |
| Circuit Breaker (`circuitbreaker.go`) | 所有方法 | ❌ 仅阻断不重试 | 断路器只快速失败，不重建请求 |

### 影响场景

| 场景 | 概率 | 影响 |
|------|------|------|
| S3 服务端 throttling（`SlowDown` 响应） | 高 — 高并发上传时常见 | 整个 PUT 失败，客户端需重试整个请求（包括大文件的上传字节） |
| 云 API 临时 503（部署滚动更新期间） | 中 — 云服务商每月有 SLA 保证外的短时 503 | 中断文件 CRUD，用户看到 5xx 错误 |
| 网络连接超时（跨 region 复制） | 中 — 复制 worker 与远 region S3 通信 | 复制失败，进入 JobPool 重试（还行），但首次延迟几十秒 |
| DNS 解析临时失败 | 低 | 文件无法读取/写入，用户重试即可 |
| IAM 凭证轮转窗口（新旧凭证同时有效） | 低 — 但轮换期间可能偶发 | 认证失败而不是限流，可能不需要重试 |

### 为什么需要

| 理由 | 说明 |
|------|------|
| **用户体验** | 用户的一次文件上传操作（尤其大文件），因为 S3 服务端一个瞬时 throttle 而完整失败，需要用户侧完整重试。存储层内部 2-3 次退避重试（总延迟 < 5s）可大幅降低用户可见的失败率 |
| **长路径操作保护** | `CompleteMultipartUpload` 在客户端已上传完所有分片后调用，是一个"最终步骤"。此时失败意味着客户端必须重试整个分片上传流程。重试保护此关键路径的效益远高于重试成本 |
| **复制/备份可靠性** | 跨区复制 worker 和 lifecycle GC worker 都依赖稳定的存储操作。瞬态失败触发 JobPool 重试（分钟级延迟）而不是存储层内秒级重试，增加了端到端延迟 |
| **现有基础设施可复用** | `storage.CircuitBreaker` 已经是装饰器模式，可以轻松在其内或在其外层添加 `RetryMiddleware`。无需修改 Storage 接口 |

### 建议方向

**Phase 1（可配置重试装饰器 — 零接口变更）：**

```go
// 新增 storage/retry.go — RetryMiddleware 包装现有 Storage

type RetryConfig struct {
    MaxAttempts     int           // 最大尝试次数（含首次），默认 3
    BaseBackoff     time.Duration // 初始退避，默认 100ms
    MaxBackoff      time.Duration // 最大退避，默认 5s
    Jitter          time.Duration // 抖动范围，默认 30%
    RetryableErrors []string      // 重试的 error 关键词：["Throttling", "SlowDown", "ServiceUnavailable", "RequestTimeout"]
}

type retryStorage struct {
    Storage
    cfg RetryConfig
}
```

- `retryStorage` 实现 `Storage` 接口，委托给内嵌 `Storage`，在遇到重试类错误时退避重试
- 配置项：`STORAGE_RETRY_ATTEMPTS=3` / `STORAGE_RETRY_BASE_MS=100`
- 非幂等方法（`Put`）需要判断实际是否已写入（通过 `Stat` 验证 ETag）

**Phase 2（重试检测与可观测性）：**

- OTel counter `storage_retry_attempts_total{backend, method}` 和 `storage_retry_success_total{backend, method}`
- 告警规则：`storage_retry_attempts_total` 速率 > 10/s → 后端可能退化

| 指标 | 估计 |
|------|------|
| Phase 1 代码量 | ~80 行（retryStorage + 重试逻辑 + factory 集成） |
| Phase 2 代码量 | ~20 行（指标注册 + 配置项） |
| 风险 | **低-中** — 非幂等方法（Put）需仔细处理：写入成功后 Stat 验证 vs. 幂等重试（影响评估，建议 Write 不重试，Read-only 方法重试） |

---

## 方向四：`go.mod` 依赖供应链安全策略缺失 — 依赖漂移与漏洞无声进入

### 现状

项目的 CI gate (`make check`) 覆盖了代码质量检查，但完全没有依赖安全方面的验证。

**当前 `make check` 执行链：**

```
$ make check
  [check] gofmt ...          ← 代码格式
  [check] go vet ...         ← 静态分析
  [check] build ...          ← 编译
  [check] test ...           ← 单元测试
  [check] 圈复杂度 ...        ← 复杂度检查
  [check] 单文件行数 ...     ← 文件大小检查
```

**缺失的依赖安全层：**

| 措施 | 状态 | 说明 |
|------|------|------|
| `go mod verify` | ❌ 缺失 | 验证 `go.sum` 中的哈希与下载的 module 内容一致，防止篡改 |
| `govulncheck` | ❌ 缺失 | Go 官方漏洞检测工具，扫描已知 CVE |
| Dependabot / Renovate | ❌ 缺失 | 自动检测过时依赖并创建更新 PR |
| `go mod tidy -diff` / `go mod tidy --check` | ❌ 缺失 | 确保 `go.mod` 和 `go.sum` 干净 |
| 依赖许可证检查 | ❌ 缺失 | 检测不合规的依赖许可证（GPL/AGPL） |
| SBOM 生成 | ❌ 缺失 | 软件物料清单，用于漏洞追溯 |

**依赖概况（`go.mod`）：**

```
require (
    github.com/go-chi/chi/v5         // Web 框架
    github.com/google/uuid            // UUID 生成
    github.com/mattn/go-sqlite3       // SQLite 驱动
    github.com/lib/pq                 // Postgres 驱动
    golang.org/x/net                  // WebDAV
    // ... ~27 个直接依赖
)
```

这些依赖通过 `go.sum` 进行了哈希锁定，但 `go.sum` 只防篡改不防漏洞。一个依赖的新 patch 版本引入了安全漏洞后，`go build ./...` 仍然静默成功。

### 为什么需要

| 理由 | 说明 |
|------|------|
| **安全合规基线** | 企业采购时，"如何管理第三方依赖安全"是常见安全问卷问题。当前没有答案 |
| **已知漏洞预警** | Go 团队每月发布已知漏洞数据库。无 `govulncheck` 意味着团队被动发现漏洞（通常是安全公告发布很久后） |
| **供应链攻击防范** | `go.sum` 防止依赖被篡改，但 `go mod verify` 是主动验证手段。当前 CI 不执行此检查 |
| **依赖膨胀控制** | 无 `go mod tidy` 检查 = 残余的间接依赖可能长时间留在 `go.sum` 中 |
| **许可证合规** | 引入 GPL 依赖可能影响项目的商业分发能力。当前无检测机制 |

### 建议方向

**Phase 1（CI 集成 — 零工具安装成本）：**

在 `Makefile` 的 `check` target 中添加：

```makefile
check: fmt vet build test complexity-lines deps-check  # 新增 deps-check

.PHONY: deps-check
deps-check:
	@echo "[check] go mod verify ..."
	go mod verify
	@echo "[check] go mod tidy diff ..."
	@go mod tidy
	@if [ -n "$$(git diff --name-only go.mod go.sum)" ]; then \
		echo "  FAIL: go.mod or go.sum changed after 'go mod tidy'"; \
		exit 1; \
	fi
	@echo "  OK"
```

**Phase 2（govulncheck 集成 — Go 官方漏洞检测）：**

```makefile
.PHONY: vulncheck
vulncheck:
	@which govulncheck >/dev/null 2>&1 || \
	  go install golang.org/x/vuln/cmd/govulncheck@latest
	govulncheck ./...
```

**Phase 3（自动化依赖更新治理）：**

- 添加 `.github/dependabot.yml`：

```yaml
version: 2
updates:
  - package-ecosystem: "gomod"
    directory: "/"
    schedule:
      interval: "weekly"
    open-pull-requests-limit: 5
```

| 指标 | 估计 |
|------|------|
| Phase 1 代码量 | ~15 行（Makefile 变更） |
| Phase 2 代码量 | ~5 行（Makefile target）+ CI pipeline 配置 |
| Phase 3 代码量 | ~15 行（dependabot.yml） |
| 风险 | **低** — 纯新增检查，不修改任何生产代码 |

---

## 方向五：OSS/COS 云后端未通过 Storage Contract Test — 语义差异静默潜伏

### 现状

`storage.RunContract` 是存储后端的官方契约测试套件，涵盖 5 组 15+ 行为规范：

```go
// internal/storage/contract_test.go:16-30
func RunContract(t *testing.T, f Factory) {
    cases := []struct {
        name string
        fn   func(*testing.T, Storage)
    }{
        {"put_get_stat",        contractPutGetStat},
        {"delete",              contractDelete},
        {"list_prefix",         contractListPrefix},
        {"key_validation",      contractKeyValidation},
        {"multipart",           contractMultipart},
    }
    // ...
}
```

但当前，这套测试**仅在** local 后端上运行：

```go
// internal/storage/factory_test.go — 隐式地用 local
func TestStorage(t *testing.T) {
    RunContract(t, func(t *testing.T) Storage {
        s, _ := NewLocal(LocalConfig{Root: t.TempDir()})
        return s
    })
}
```

而 OSS 和 COS 后端的测试只有构造验证：

```go
// internal/storage/oss_cos_test.go — 75 行，零功能测试
func TestNewOSS(t *testing.T) {
    s, err := NewOSS(OSSConfig{...})
    // 只验证构造不报错，不测试 Put/Get/Delete/List/Multipart
}
func TestNewCOS(t *testing.T) {
    s, err := NewCOS(COSConfig{...})
    // 同上
}
func TestOSSNotFound(t *testing.T) {
    // 唯一的功能测试：验证 Get/Stat 对不存在的 key 返回 ErrNotFound
}
```

**具体的未验证行为差异风险：**

| 行为 | Local 行为 | OSS/COS 可能差异 | 风险 |
|------|-----------|-----------------|------|
| ETag 引号 | 存储时不带引号，返回时统一加 `"` | SDK 可能返回不同格式 | S3 handler 的 ETag 响应格式不正确 |
| Multipart ETag 计算 | `MD5(part1+part2+...)-N` | OSS/COS 返回值可能不同 | multipart 对象的 ETag 不符合 S3 预期 |
| Metadata key 大小写保持 | 按原文存储 | 云后端可能小写化 | 用户自定义 metadata 丢失大小写 |
| List 分页行为 | NextMarker 精确控制 | 翻页边界可能不同 | ListObjectsV2 ContinuationToken 不兼容 |
| Content-Type 默认值 | 保留 `""` | 可能默认 `application/octet-stream` | object 的 Content-Type 变化 |
| 空对象 (0 字节) | 正常处理 | 可能被认为是"目录" | 空文件上传后无法读取 |
| Delete 幂等性 | 已删除 key 再次 Delete 返回 nil | 可能返回 NotFound | Delete 路径报错 |
| Multipart 分片上限 (10000) | 严格检查 | SDK 可能限制不同 | 大文件分片上传失败 |

### 根因

OSS/COS 后端无法在普通 CI 中运行 Contract Test，因为它们需要：
1. 真实的云服务凭证（AccessKey/SecretKey）
2. 真实的云存储 bucket（有命名唯一性约束）
3. 网络访问云服务端点

这并非设计疏忽，而是测试基础设施约束。但**契约测试从不在真实后端上运行**意味着云后端的正确性没有被验证——它们只在"开发者手动测试时"才被触及。

### 为什么需要

| 理由 | 说明 |
|------|------|
| **云后端是收费特性** | 用户选择 OSS/COS 后端往往是为了生产用途。如果这些后端有语义偏差，付费用户最先遇到 |
| **版本回归防护** | 修改 `storage.Storage` 接口或 contract test 时，OSS/COS 可能无声地不再满足新契约——无人知晓直到生产事故 |
| **SDK 版本升级验证** | OSS/COS SDK 更新时，需要验证新 SDK 版本是否仍满足 contract |
| **新后端质量基线** | 未来添加 GCS、Azure Blob、MinIO 等后端时，contract test 应作为必须通过的准入门槛 |

### 建议方向

**Phase 1（Contract Test 可脚本化运行 — 本地开发 + CI 手动触发）：**

```bash
# 通过环境变量注入凭证运行 OSS/COS contract test
STORAGE_BACKEND=oss \
OSS_ENDPOINT=https://oss-cn-hangzhou.aliyuncs.com \
OSS_BUCKET=aero-contract-test \
OSS_ACCESS_KEY=*** \
OSS_SECRET_KEY=*** \
  go test -run TestStorageContract ./internal/storage/
```

- 为 OSS/COS 添加 `TestStorageContract_OSS` 和 `TestStorageContract_COS` 构建约束测试
- 测试使用 `os.Getenv("STORAGE_BACKEND")` 来判断是否运行
- 在 `make test-integration` 中添加可选步骤（需要配置的才跑）

**Phase 2（minio 模拟 — CI 可运行的 S3 兼容 contract 测试）：**

- 使用 MinIO Docker 容器模拟 S3 兼容存储
- S3 后端运行 contract test 指向 MinIO
- 这也能同时验证 S3 后端的实现（s3.go 目前也缺 contract test 覆盖）

```makefile
test-contract-s3:
	docker run -d --name aero-minio -p 9000:9000 \
	  -e MINIO_ROOT_USER=minio -e MINIO_ROOT_PASSWORD=minio123 \
	  minio/minio server /data
	# 等待后执行
	AERO_S3_CONTRACT=1 \
	AERO_S3_ENDPOINT=http://localhost:9000 \
	AERO_S3_BUCKET=aero-contract \
	AERO_S3_ACCESS_KEY=minio \
	AERO_S3_SECRET_KEY=minio123 \
	  go test -run TestStorageContract_S3 ./internal/storage/
```

| 指标 | 估计 |
|------|------|
| Phase 1 代码量 | ~40 行（OSS/COS contract test 包装 + Makefile target） |
| Phase 2 代码量 | ~20 行（MinIO contract target + Docker 编排） |
| 风险 | **低** — 纯新增测试；contract test 本身已有 5 组稳定测试 |

---

## 各方向既有分析去重声明

| 方向 | 验证方式 | 结果 |
|------|---------|------|
| **方向一：Events table GC** | `grep -rl "events.*table.*gc\|events.*cleanup\|events.*purge\|object_events.*delete\|events.*retention.*days\|consumed.*events.*cleanup" docs/requirements/` → v30/v38/v29 命中，阅读命中处：全部聚焦"新增事件类型"和"事件路由扩展"，**零消费事件持久化 GC 分析** | ✅ **完全去重** |
| **方向二：CopyObject 附属属性** | `grep -rl "x-amz-tagging-directive\|tagging.*directive.*copy\|copy.*tags.*loss\|copy.*storage.*class\|CopyObject.*ACL\|copy.*attribute.*loss\|附属属性" docs/requirements/` → 零命中。补充 `grep -rl "CopyObject.*tag"` → 零命中 | ✅ **完全去重** |
| **方向三：Storage 重试** | `grep -rl "storage.*retry\|retry.*storage\|transient.*storage\|storage.*transient\|storage.*backoff\|s3.*retry\|oss.*retry\|cos.*retry" docs/requirements/` → v19 方向表一行概念"storage retry"，**零代码锚点、零分析** | ✅ **完全去重** |
| **方向四：依赖供应链** | `grep -rl "go.mod.*security\|dependency.*security\|supply.*chain\|golang.*vuln\|govulncheck\|Dependabot\|Renovate\|SBOM\|go.sum.*check\|go mod verify" docs/requirements/` → 零命中 | ✅ **完全去重** |
| **方向五：OSS/COS Contract Test** | `grep -rl "OSS.*contract\|COS.*contract\|oss.*contract\|cos.*contract\|cloud.*contract.*test\|contract.*cloud\|backend.*contract.*test\|contract.*OSS\|contract.*COS" docs/requirements/` → v46 一行提及"新增存储后端开发指南"，**零功能分析** | ✅ **完全去重** |
