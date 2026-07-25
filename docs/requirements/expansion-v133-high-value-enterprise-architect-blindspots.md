# AeroVault 高价值扩展方向 — 核心架构盲区与企业级纵深缺口

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 230+ `.go` 文件 + 48 对迁移文件 + `sdk/*` 三套客户端 + `deploy/*` + `docs/*` + `Makefile` + `HARNESS.md` + `.env.example`）
>
> **分析视角：** 资深架构师 / 产品经理 — 聚焦此前 **39 期 expansion 分析（累计 200+ 方向、~350,000+ 字分析文本）+ `docs/ROADMAP.md`（10 大方向）+ `docs/adr/` + `docs/CHANGELOG.md` + `docs/TODO.md`** 中从未实质性触及的企业级架构盲区。
>
> **分析日期：** 2026-07-10
>
> **去重方法：** 逐方向对 `docs/requirements/` 下全部 39 期既有分析 + `docs/ROADMAP.md` + `docs/CHANGELOG.md` + `docs/TODO.md` + `docs/analysis-v8-gaps-roadmap.md` 进行完整关键词检索验证，确保每个方向在既有文档中 **零实质性覆盖**。

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 锚定代码 |
|---|------|------|--------|---------|---------|
| 1 | **数据主权与租户级存储后端路由** | 合规/架构 | **P0** — 多租户 SaaS 全球部署的合规基座；GDPR/数据本地化法案的硬性要求 | `internal/storage/factory.go`（单一 `storage.Storage` 实例）+ `internal/service/file_crud.go`（无后端选择逻辑） |
| 2 | **不可变审计轨迹（Immutable Audit Trail）** | 合规/安全 | **P0** — SOC2/HIPAA/FedRAMP 审计的必备条件；当前审计日志与业务 DB 同库，可被篡改 | `internal/repository/audit.go`（写入 `audit_log` 表）+ `internal/api/rest/admin.go`（无导出/签名机制） |
| 3 | **多存储后端数据分层与自动迁移** | 架构/成本 | **P1** — 单一后端是单点瓶颈；无冷热分层意味着成本与性能无法兼顾 | `internal/service/file_crud.go:Put`（`StorageClass` 仅元数据，不映射后端）+ `internal/reconcile/lifecycle.go`（无 transition 动作） |
| 4 | **四协议统一访问控制模型** | 安全/产品 | **P1** — 核心差异化（四协议）因认证模型碎片化而成为安全隐患 | `internal/auth/auth.go`（JWT/API Key）+ `internal/auth/sigv4.go`（S3 专用）+ `internal/api/webdav/dav.go`（无专用认证） |
| 5 | **可观测性驱动的参数自调优** | 运维/性能 | **P2** — 15+ OTel 指标 + 80+ 配置参数，但所有参数静态配置，无反馈闭环 | `internal/telemetry/metrics.go`（丰富指标）+ `internal/config/config.go`（全是静态 env）+ `internal/ai/chunker.go`（固定 window/overlap） |

---

## 方向一：数据主权与租户级存储后端路由（Data Sovereignty & Tenant-Level Storage Routing）

### 现状

当前架构中，**所有租户共享同一个存储后端**：

```go
// cmd/server/main.go:buildStorageFrom — 启动时创建单一 storage.Storage 实例
store, err := buildStorage(ctx, cfg)  // 一个 backend，一个实例

// internal/service/file_crud.go:Put — 存储 key 仅由 tenant+bucket+key 拼接
sk := storageKey(tenant, bucket, key)
// 没有后端选择逻辑——所有数据都写入启动时确定的那个后端
```

存储后端配置是全局的：

```yaml
# .env.example
STORAGE_BACKEND=local           # 全局：所有租户都用 local
# 或
STORAGE_BACKEND=s3              # 全局：所有租户都用 S3
```

没有租户级别的后端映射。**这意味着：**

| 场景 | 当前行为 | 合规要求 |
|------|---------|---------|
| 欧盟租户数据必须留在欧盟 S3 | ❌ 所有租户数据在同一后端 | ✅ 数据必须路由到指定地域的存储 |
| 金融租户要求本地加密 + 本地存储 | ❌ 无法独立选择后端 | ✅ 独立存储策略 |
| 免费层租户用低成本冷存储 | ❌ 无法按租户配置存储后端 | ✅ 差异化 SLA |
| 中国租户数据必须存中国云 | ❌ 全球统一后端 | ✅ 属地化存储 |

### 为什么需要

1. **法律合规是准入条件：** GDPR（Article 44–49 数据跨境传输限制）、中国《数据安全法》第 31 条（重要数据境内存储）、CCPA、LGPD——任何服务全球多租户的 SaaS 产品都必须支持数据属地化。这是 **P0（不做则无法进入企业市场）**。

2. **成本与服务分级：** 不同租户需要不同 SLA 和成本结构。免费/开发租户可以用低成本后端（本地 HDD 或 S3 Glacier），生产租户用高性能本地 NVMe 或 Premium SSD 后端。

3. **故障隔离：** 一个后端故障不应影响所有租户。多后端部署可限制爆炸半径。

### 缺失的能力

1. **租户-后端映射存储：** 新增 `tenant_storage_routing` 或扩展 `tenants` 表，存储 `backend_kind` + `backend_config_override`（可覆盖全局配置）。

2. **带后端选择的 Storage 抽象层：** 当前 `storage.Storage` 是单一接口。需要一个 `RoutingStorage` 包装器或 `StorageRouter`，根据 tenant + bucket + storage class 选择后端实例：

   ```
   调用链路:
   FileService.Put → StorageRouter.Put(tenant, bucket, key, ...)
     → 路由规则匹配 → backendA.Put(key, ...) 或 backendB.Put(key, ...)
   ```

3. **租户级迁移能力：** 将租户 A 的数据从后端 X 在线迁移到后端 Y（异构后端之间），不停机。

4. **跨后端复制一致性：** 当租户配置了"主 EU 后端 + 备份 US 后端"时，复制规则应当感知数据主权约束——US 备份不得包含 EU 租户的原始数据（仅可存加密后的元数据或摘要）。

### 边界情况与注意事项

| 场景 | 处理方式 |
|------|---------|
| **后端故障后降级** | 当租户的主后端不可用时，是拒绝写入还是降级到全局后备后端？建议：拒绝写入（数据主权必须遵守），仅 GET 可降级到只读副本 |
| **后端间 ListObjects** | 跨后端列出对象需要合并结果——增加延迟。建议：metadata DB 保持全局视图（已如此），存储后端按租户路由 |
| **迁移中的数据一致性** | 在线迁移期间，对象可能部分在旧后端、部分在新后端。需要双写阶段 + 迁移标记 |
| **删除的后端配置** | 删除租户的后端映射前，必须先迁移或确认数据已删除 |
| **存储类与后端冲突** | 若租户路由到后端 A，但请求指定 `x-amz-storage-class: GLACIER`，而后端 A 不支持 Glacier——拒绝还是降级？建议：返回 `InvalidStorageClass` |

### 架构概要

```
当前:
  main.go → buildStorage → 1× storage.Storage (全局)
  FileService → store.Put(key)  // 同一后端

改进:
  main.go → buildStorageMap → map[string]storage.Storage (按后端 ID 索引)
  StorageRouter {
    Rules: []StorageRule{  // 有序，首匹配
      {Tenant: "eu-acme", Backend: "eu-s3"},
      {Tenant: "cn-acme", Backend: "cn-oss"},
      {Tenant: "*",       Backend: "default-local"},  // 兜底
    }
  }
  FileService → router.Put(ctx, tenant, bucket, key, ...)
    → rules match → backend.Put(key, ...)
```

### 影响评估

| 维度 | 评估 |
|------|------|
| 复杂度 | **高**（需要 RoutingStorage 抽象 + 配置模型 + 迁移工具 + 重做 storage key 管理） |
| 用户影响 | **极高**（合规准入×成本优化×故障隔离） |
| 代码变动 | 大（`internal/storage/` 新增 router + 配置模型；`internal/config/` 新增多后端配置；`cmd/server/main.go` 重写 buildStorage；数据面迁移工具） |
| 差异化 | ★★★★★（让 AeroVault 从单集群存储进化为全球分布式合规存储平台） |
| 工作量估计 | 4-6 周（核心路由：2 周；租户配置 API + 迁移工具：2-3 周；测试 + 文档：1 周） |

---

## 方向二：不可变审计轨迹（Immutable Audit Trail）

### 现状

当前审计日志实现：

```go
// internal/repository/audit.go — 审计写入
func (r *sqlRepo) InsertAuditEntry(ctx context.Context, e AuditEntry) error {
    // INSERT INTO audit_log (tenant_id, ...) VALUES (?, ?, ...)
    // 与业务数据在同一 SQLite/Postgres 数据库
}

// internal/api/rest/admin.go — 审计查询
func (h *AdminHandler) ListAudit(w http.ResponseWriter, r *http.Request) {
    entries, err := h.repo.ListAuditEntries(ctx, tenant, ...)
    // 直接查询 audit_log 表
}
```

**关键问题：**

| 问题 | 严重性 |
|------|--------|
| **同库存储：** 审计日志与业务数据在同一数据库中。有 admin 权限的用户可以 `UPDATE audit_log ...` 或 `DELETE FROM audit_log ...` 来掩盖操作 | **🔴 关键** — SOC2 合规要求审计日志不可篡改 |
| **无加密签名：** 每条审计记录没有哈希链或数字签名，无法检测事后篡改 | **🔴 关键** — 审计的可信度无法独立验证 |
| **无独立导出：** 无法将审计日志实时导出到外部 SIEM（Splunk、ELK、Datadog）或外部存储（S3、Glacier） | **🟠 高** — 企业需要集中化审计管理 |
| **无写后读验证：** 写入审计日志后不回读验证持久化成功 | **🟡 中** — 静默写入失败可能导致审计缺失 |
| **无保留期限：** 审计日志无限增长，缺乏自动归档/轮换策略 | **🟡 中** — 存储成本 + 查询性能下降 |

### 为什么需要

1. **合规准入门槛：** SOC2（CC6.1/CC6.2 要求审计日志防篡改）、HIPAA（§164.312(b) 要求审计控制）、PCI DSS（§10.2/10.3 要求审计轨迹 + 防篡改）、FedRAMP（AU-5/6 要求审计记录保护）——**没有不可变审计日志，这些合规框架全部无法通过。**

2. **企业客户信任：** 企业客户的多租户 SaaS 需要向审计员证明操作可追溯且不可否认。当前的"同库可改"模型无法提供这种保证。

3. **事故取证能力：** 当发生安全事件时，审计日志是最重要的取证来源。如果攻击者已经获得 admin 访问权限，他们可以在清理痕迹前修改/删除审计日志。

### 缺失的能力

1. **哈希链审计（Hash-Chained Audit）：** 每条审计记录包含前一条记录的 SHA-256 哈希，形成不可篡改的链。验证工具可以遍历链并检测任何断裂。

   ```
   AuditEntry {
     ID:          1234
     TenantID:    "acme"
     Action:      "object.delete"
     Timestamp:   2026-07-10T12:00:00Z
     Actor:       "user@acme.com"
     Details:     {...}
     PrevHash:    "a1b2c3..."  // 前一条记录的哈希
     MyHash:      "d4e5f6..."  // sha256(PrevHash + ID + Action + ... + salt)
     Signature:   "..."         // 可选的 HMAC-SHA256 签名（server secret）
   }
   ```

2. **双写审计（Dual-Write）：** 每条审计记录同时写入主 DB 和独立的外部审计存储（仅追加的文件、S3 Append-only 桶、或专用的审计即服务）——确保即使主 DB 被篡改，外部副本可验证。

3. **实时审计流：** 新增 `GET /v1/events/audit-stream`（SSE）或通过 EventBus 发布 `audit.*` 事件，让 SIEM 系统实时消费。

4. **审计导出/归档：** 新增 `POST /v1/admin/audit/export` API，支持按时间范围 + 租户 + 操作类型过滤，导出为 JSON Lines 或 CSV 格式，可直接写入 S3/本地文件。

5. **审计保留与轮换：** 新增 `AUDIT_RETENTION_DAYS` 配置，配合 `reconcile` 定期归档或清理过期的审计记录。但注意：不可变审计意味着"归档"而非"删除"——旧记录应导出到长期存储后从主 DB 移除。

### 边界情况与注意事项

| 场景 | 处理方式 |
|------|---------|
| **审计写入延迟** | 双写可能导致主请求延迟增加。建议：审计写入异步进行（通过 EventBus），但需保证"至少一次"语义 |
| **审计存储故障** | 外部审计存储不可用时，不可阻塞主请求。方案：本地 buffer + 重试；buffer 满时降级为仅本地写 + 告警 |
| **时间同步** | 审计日志的时间戳依赖服务器时钟。NTP 偏移可导致审计顺序混乱。建议：使用 monotonic clock + NTP 监控告警 |
| **哈希链的分叉** | 如果同时写入两条审计记录（并发），哈希链可能分叉。解决方案：使用数据库序列或分布式 ID 生成器确保全局有序 |
| **跨区域审计** | 多区域部署时，审计日志应汇聚到中心化审计存储。需考虑跨区域延迟和网络故障 |
| **审计日志压缩** | 长时间运行的审计日志可能非常大。支持按天分区 + gzip 压缩存储 |

### 架构概要

```
当前:
  Admin操作 → InsertAuditEntry → audit_log 表（与业务同库、可篡改）

改进:
  Admin操作 → AuditService.Record(entry)
    ├─→ 本地 DB: audit_log 表（哈希链 + 签名）
    ├─→ EventBus: 发布 audit.event（→ SIEM 消费者）
    └─→ 外部 Audit Sink: S3 append-only / syslog / HTTP API

  AuditService.Verify(chain) → 遍历链验证哈希 → 返回完整性报告

  新组件:
    internal/audit/        — 审计服务（哈希链 + 签名 + 双写）
    internal/audit/sink.go — 外部审计存储接口（S3/HTTP/file）
    internal/audit/verify.go — 审计完整性验证工具
```

### 影响评估

| 维度 | 评估 |
|------|------|
| 复杂度 | **中-高**（哈希链模型 + 双写架构 + 外部 sink 接口） |
| 用户影响 | **极高**（SOC2/HIPAA 合规准入；企业信任） |
| 代码变动 | 中（新增 `internal/audit/` 包；修改 `internal/repository/audit.go`；修改 admin handler 审计查询；新增配置项；迁移文件增加 `prev_hash`/`signature` 列） |
| 差异化 | ★★★★★（让 AeroVault 成为合规就绪的存储平台，而非需要外部审计工具的裸存储） |
| 工作量估计 | 3-4 周（核心哈希链 + 签名：1.5 周；双写 + sink：1 周；验证工具 + 测试：1 周） |

---

## 方向三：多存储后端数据分层与自动迁移（Multi-Backend Data Tiering & Auto-Migration）

### 现状

当前存储类支持情况：

```go
// internal/service/file_crud.go — 存储类仅做元数据记录
type PutOptions struct {
    StorageClass string  // "STANDARD" | "STANDARD_IA" | "GLACIER"
}

// internal/service/file.go — StorageClass 仅作为业务字段，无后端映射
var DefaultStorageClass = "STANDARD"

// internal/reconcile/lifecycle.go — 生命周期动作有限
const (
    ActionSoftDelete = "soft_delete"  // ❌ 没有 "transition_to_ia" 或 "transition_to_glacier"
    ActionHardDelete = "hard_delete"
)
```

**关键缺失：**

| 能力 | 现状 | 影响 |
|------|------|------|
| **存储类→后端映射** | `STANDARD` / `STANDARD_IA` / `GLACIER` 仅元数据，共享同一后端 | 无法通过存储类实现真正的成本优化 |
| **自动迁移** | 生命周期只有 `soft_delete` / `hard_delete`，没有 `transition_to_ia` / `transition_to_glacier` | 数据无法自动降冷，存储成本失控 |
| **多后端共存** | 启动时只能配置一个后端 | 无法同时使用"本地 SSD（热数据）+ S3（温数据）+ S3 Glacier（冷数据）"分层架构 |
| **迁移操作** | 无工具将对象从后端 A 迁移到后端 B | 手工迁移需要写脚本，且无进度追踪和一致性校验 |

### 为什么需要

1. **存储成本是企业存储的第一考量：** 企业存储支出中，生命周期管理带来的成本优化通常是 40-70%。不支持自动降冷意味着 AeroVault 在总拥有成本（TCO）上与 MinIO / 公有云 S3 存在数量级差距。

2. **区别于"仅存"的竞争力：** 大量竞品（特别是开源方案）都支持基本的对象 CRUD。**多后端分层存储 + 自动迁移**是 AeroVault 区别于 MinIO（单后端）和公有云 S3（不可跨云）的差异化能力。

3. **与现有存储类元数据的断层：** 既然已经跟踪 `StorageClass`，却不根据它执行任何操作——这是"存了不用"的架构半成品。补齐这一环让整个 feature matrix 从"功能完整"变为"有商业价值"。

### 缺失的能力

1. **后端抽象层支持多实例：** 当前 `storage.NewFromConfig` 返回单实例。需要 `storage.NewMultiBackend` 或 `storage.NewRouter` 注册多个后端。

2. **存储类→后端映射配置：**
   ```yaml
   # 新增配置格式
   STORAGE_TIERS=STANDARD:local,STANDARD_IA:s3,GLACIER:s3-glacier
   STORAGE_TIER_LOCAL_BACKEND=local
   STORAGE_TIER_LOCAL_ROOT=./var/hot
   STORAGE_TIER_S3_ENDPOINT=https://s3.amazonaws.com
   STORAGE_TIER_S3_BUCKET=aero-warm
   STORAGE_TIER_S3_GLACIER_ENDPOINT=https://s3.amazonaws.com
   STORAGE_TIER_S3_GLACIER_BUCKET=aero-cold
   ```

3. **生命周期 Transition 动作：** 扩展 `BucketConfig.ExpireAction` 支持 `transition_to_ia`、`transition_to_glacier`。在 `LifecycleJob.sweep()` 中执行实际数据迁移。

4. **迁移作业框架：** 基于已有 `jobs` 表实现异步迁移作业。每个对象的迁移是一个独立 job，支持失败重试和进度查询。

5. **读取时回退（Read-Through Fallback）：** 正在迁移或已迁移的对象，GET 请求应透明地从目标后端读取。

### 边界情况与注意事项

| 场景 | 处理方式 |
|------|---------|
| **迁移中的部分成功** | 对象迁移到新后端后，应在 metadata 中标记 `storage_key` 更新。旧后端的 blob 在确认迁移完成后才 GC。需要双写或"迁移标记"机制 |
| **正在迁移的对象被修改** | PUT 正在迁移的对象应中止迁移 job，更新新后端的 blob，或回退到原后端。需要在迁移 job 中检查对象版本/更新时间 |
| **不同后端的延迟差异** | S3 Glacier RESTR 需要 3-5 小时才能读取。API 应返回 `RestoreInProgress` 状态，而非直接失败 |
| **跨后端 multipart 上传** | 大文件上传的分片应全部在同一个后端完成。路由规则应在 InitMultipart 时确定后端 |
| **后端之间 storage key 格式差异** | local 是文件路径，S3 是对象键名，OSS/COS 也有差异。`storageKey` 生成方式可能需要与后端绑定 |
| **存储类变更的事件通知** | 转换完成时应发布 `object.storage_class.transitioned` 事件，触发 webhook 通知 |
| **带 SSE 加密的数据迁移** | 迁移到新后端时需要解密→重新加密。密钥体系需要支持跨后端 envelope 重新包装 |

### 架构概要

```
当前:
  store = buildStorage(cfg)        // 一个后端实例
  FileService.store.Put(key, data) // 写入同一后端

改进:
  tierMap = {
    "STANDARD":    backend_local,
    "STANDARD_IA": backend_s3,
    "GLACIER":     backend_s3_cold,
  }

  FileService.Put(key, data, opts.StorageClass)
    → backend = tierMap[opts.StorageClass]
    → backend.Put(key, data)

  LifecycleJob.sweep():
    → 扫描对象 "age > transition_days AND StorageClass != target"
    → 生成 TransitionJob: sourceBackend.Get → targetBackend.Put → 更新 Object.StorageKey

  Reconcile:
    → 扫描 "storage_key 不存在于 backend" 的孤儿 → GC
```

### 影响评估

| 维度 | 评估 |
|------|------|
| 复杂度 | **高**（多后端路由 + 数据迁移 + 一致性保证 + 跨后端 GC） |
| 用户影响 | **极高**（成本优化 × 架构灵活性 × 差异化竞争力） |
| 代码变动 | 大（`internal/storage/` 新增 router/tier；`internal/config/` 新增多后端配置；`internal/reconcile/lifecycle.go` 新增 transition；`internal/repository/sql_objects.go` 迁移 state 字段；迁移文件） |
| 差异化 | ★★★★★（多后端"冷热温"分层是竞品无法在单集群内实现的能力） |
| 工作量估计 | 6-8 周（路由 + 配置：2 周；迁移引擎：2-3 周；生命周期 transition + GC：1-2 周；测试：1 周） |

---

## 方向四：四协议统一访问控制模型（Unified Access Control Across 4 Protocols）

### 现状

四个协议的认证/授权现状：

| 协议 | 认证机制 | 授权模型 | 当前状态 |
|------|---------|---------|---------|
| **REST `/v1/`** | Bearer JWT / X-Api-Key | Scope-based（read/write/admin），桶 ACL，桶 Policy | ✅ 完整实现 |
| **S3 `/s3/`** | SigV4 / 降级到 header auth | 桶 Policy（IAM 风格语句）+ 桶 ACL | ✅ 已实现，但桶 Policy 只在 S3 handler 中检查，REST handler 不执行 |
| **WebDAV** | 无专用认证（依赖上层 chi 中间件链）| 无专用授权 | ❌ 认证靠外部中间件；授权完全缺失 |
| **MCP** | stdio 模式无认证；HTTP 模式依赖 chi 中间件 | 无操作级授权 | ⚠️ stdio 被假定为本地可信；HTTP 模式使用固定 tenant |

**关键问题：**

```go
// internal/api/s3compat/handler.go — S3 handler 自行检查桶 Policy
func (h *Handler) checkBucketPolicy(w http.ResponseWriter, r *http.Request, bucket, action string) bool {
    // 这是 S3 特有的——REST handler 不调用这个函数
}

// internal/api/rest/router.go — REST 路由没有桶 Policy 检查
r.Get("/buckets/{bucket}/policy", h.GetBucketPolicy)   // ✅ 可以读取
r.Get("/files/*", h.getKey)                             // ❌ 不检查桶 Policy

// internal/mcp/server.go — MCP 完全不执行授权
func (s *Server) callTool(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
    // 没有检查调用者是否有权限执行该操作
}
```

**不一致的后果：**

| 场景 | 实际行为 |
|------|---------|
| 桶 Policy 禁止 `s3:GetObject` | S3 GET → 403 ✅；REST GET → 200 ❌（绕过） |
| 桶 Policy 设置 `Principal` 限制 | S3 检查 → 通过/拒绝 ✅；REST 不检查 → 永远允许 ❌ |
| MCP `read_file` 读取受 Policy 限制的桶 | 不执行 Policy 检查 → 可越权读取 ❌ |
| WebDAV 访问私有桶 | 依赖外层中间件的认证 → 但无桶级授权检查 ❌ |

### 为什么需要

1. **安全一致性的底线：** 四个协议共享同一个 `FileService`，也必须共享同一个授权边界。如果桶 Policy 说"只有 IP 192.168.1.0/24 可以读"，那么无论通过 REST、S3、WebDAV 还是 MCP 访问，都应统一执行该规则。

2. **核心差异化的潜在致命伤：** 四协议覆盖是 AeroVault 区别于所有竞品的最大特色。但如果这个特色伴随"安全漏洞"（协议 A 有授权而协议 B 无），它将从优势变为 liability。

3. **企业信任的前提：** 安全团队评估时，第一个检查点就是"访问控制是否在所有入口一致"。不一致直接导致安全评估不通过。

### 缺失的能力

1. **统一的授权评估引擎（Policy Decision Point, PDP）：** 将 `auth.Policy` 评估从 `internal/api/s3compat/handler.go` 提升到 `internal/service/file.go` 的 `FileService` 层——在每次操作前统一执行。

   ```go
   // 当前：FileService 不感知策略
   func (s *FileService) Get(ctx, tenant, bucket, key) (io.ReadCloser, Object, error)

   // 改进：FileService 在执行前调用授权引擎
   func (s *FileService) Get(ctx, tenant, bucket, key, action, callerInfo) (io.ReadCloser, Object, error) {
       if err := s.authz.Check(ctx, tenant, bucket, key, action, callerInfo); err != nil {
           return nil, Object{}, ErrForbidden
       }
       // ... 执行原有逻辑
   }
   ```

2. **调用者信息传递：** 当前 `context.Context` 携带 tenant 和 request ID，但不携带"调用者身份"（user ARN/IP/role），也不携带"接入协议"（REST/S3/WebDAV/MCP）。需要统一上下文协议。

3. **WebDAV 专用认证适配器：** WebDAV 没有原生 Bearer token 传递机制。需要一个认证适配器（支持 `Authorization: Bearer` header 或 `?auth_token=X` query 参数）。

4. **MCP 请求级授权：** MCP 的 `callTool` 必须为每个工具调用检查权限。工具清单应当反映当前调用者可见的桶（如 `list_files` 只列出有读权限的桶）。

5. **跨协议审计的一致性：** 审计日志应当记录"通过哪个协议执行的此操作"，以便在跨协议访问异常时追溯。

### 边界情况与注意事项

| 场景 | 处理方式 |
|------|---------|
| **WebDAV 无 Bearer token 通道** | WebDAV 客户端通常只支持 Basic/Digest 认证。建议实现 `?auth_token` URL 参数（限时签名），或在 WebDAV 前缀路由上支持 Bearer token header |
| **MCP stdio 模式** | stdio 模式由本地进程启动，可以假定为可信（类似 localhost）。可配置开关 `MCP_STDIO_SKIP_AUTH=true` |
| **预签名 URL 绕过 Policy** | 预签名 URL 应携带签名时生效的 Policy 快照。不允许通过预签名 URL 绕过后新增的 Policy 限制 |
| **S3 条件键（Condition Keys）的跨协议兼容** | S3 条件键如 `s3:x-amz-server-side-encryption` 仅在 S3 协议中有意义。跨协议评估时，不支持的键应默认通过，而非拒绝 |
| **性能开销** | 为每个请求增加 Policy 评估会带来延迟（特别是 Policy JSON 解析）。建议：缓存已解析的 Policy 语句，按桶+更新时间作 key |
| **匿名公共读** | `AUTH_ANONYMOUS_PUBLIC_READ=true` 时，未认证请求应通过统一的评估引擎，而非在 REST/S3 handler 中硬编码放行 |

### 架构概要

```
当前:
  REST:  chi middleware 检查 scope → FileService → 无 Policy 检查
  S3:    SigV4 验证 → S3 handler 检查 Policy → FileService
  WebDAV: 无认证 → FileService
  MCP:   固定 tenant → FileService

改进:
  统一授权引擎（Policy Decision Point）内置于 FileService：

  FileService {
    store  storage.Storage
    repo   repository.Repository
    authz  Authorizer  // 新增：统一的 Policy 评估器
  }

  每个 FileService 方法在执行业务逻辑前调用：
    s.authz.Check(ctx, "s3:GetObject", AccessRequest{
      Tenant:  tenant,
      Bucket:  bucket,
      Key:     key,
      Caller:  callerARN,     // user/<key_id> 或 anonymous
      IP:      remoteIP,
      Protocol: protocol,     // "rest" | "s3" | "webdav" | "mcp"
    })

  各协议适配器的职责仅限：
    1. 提取认证凭据 → 解析出 caller identity
    2. 将请求参数映射到标准 AccessRequest
    3. 调用 FileService 方法（统一评估）
```

### 影响评估

| 维度 | 评估 |
|------|------|
| 复杂度 | **中**（概念简单，但涉及四个协议适配器的修改 + FileService 新增职责） |
| 用户影响 | **极高**（安全基线——不一致的授权等于没有授权） |
| 代码变动 | 中（`internal/service/file.go` 新增 `Authorizer` 接口；`internal/auth/policy.go` 提升为通用评估器；四个协议适配器的调用侧修改；迁移文件无需变动） |
| 差异化 | ★★★★☆（让"四协议"从功能特色变成有安全保障的企业级能力） |
| 工作量估计 | 2-3 周（引擎提取 + 接口设计：1 周；各协议适配：0.5 周 × 3；测试 + 契约文档：0.5 周） |

---

## 方向五：可观测性驱动的参数自调优（Observability-Driven Auto-Tuning）

### 现状

系统拥有丰富的可观测性基础设施：

```go
// internal/telemetry/metrics.go — 15+ OTel instruments
// embedded search latency, chunk index duration, queue depth, storage bytes, etc.

// internal/config/config.go — 80+ 配置参数（全是静态环境变量）
type AIConfig struct {
    ChunkWindow   int  // AI_CHUNK_WINDOW=600   — 固定值
    ChunkOverlap  int  // AI_CHUNK_OVERLAP=80   — 固定值
    EmbedCacheSize int // AI_EMBED_CACHE_SIZE=0  — 启动后不可变
    // ... 其余所有参数都是静态的
}

// internal/ai/chunker.go — 参数写死在初始化
type Chunker struct {
    Window  int  // 创建后不可变
    Overlap int  // 创建后不可变
}
```

**关键缺失：**

| 能力 | 当前状态 | 问题 |
|------|---------|------|
| **动态参数调整** | 所有参数在启动时加载，运行时不可变 | 无法在流量模式变化时自适应调整 |
| **指标→参数反馈闭环** | 丰富的指标存在，但没有消费它们的控制循环 | metrics "read-only"，不触发任何动作 |
| **自适应 Chunk Window** | `AI_CHUNK_WINDOW=600` 对所有文件类型统一 | PDF 和日志文件的理想 chunk 策略完全不同 |
| **自适应 Worker 池** | `JOBS_WORKERS=4` 固定 | 高峰期排队，低谷期资源浪费 |
| **自适应缓存 TTL** | `AI_SEARCH_CACHE_TTL_SECONDS=30` | 低命中率时缓存浪费内存；高命中率时 TTL 太短 |
| **自适应限流阈值** | `RATE_LIMIT_RPS=100` | 无法根据实际后端延迟自动调整 |

### 为什么需要

1. **运维成本是隐性竞争力：** 80+ 个配置参数意味着新用户需要数天才能调优到生产水平。自动化调优将"数天的专家调优"降为"启动即优化"——这是开源项目从"可用"到"易用"的飞跃。

2. **负载模式随时间变化：** 白天搜索密集（需要大缓存 + 大 worker 池），晚上索引密集（需要大 chunk window + 小缓存）。静态配置只能取"折中值"，无法达到最优。

3. **不同租户的不同特征：** 大租户有海量小文件（需要大 BM25 池 + 小 chunk window），小租户有几份大 PDF（需要大 chunk window + 小 worker 池）。静态配置无法按租户差异化。

### 缺失的能力

1. **参数动态更新框架（Dynamic Config Store）：** 所有可调参数从"环境变量"迁移到"可运行时更新的配置中心"。第一阶段：支持 `SIGHUP` 信号重载配置（最小改动）。第二阶段：支持通过 admin API 实时更新。

2. **自适应 Chunk Window 控制器：** 监控 `indexer_chunk_duration_ms`、`indexer_chunk_size_bytes`、`search_result_score` 指标，自动调整 chunk window/overlap 以在索引速度和检索质量之间取得最佳平衡。

3. **自适应 Worker 池：** 监控 `job_queue_depth`、`job_latency_p99`，自动调整 `JOBS_WORKERS` 数量。后台 worker 数在高峰期增加，在低谷期减少（同时支持下限和上限配置）。

4. **自适应缓存策略：** 监控 `search_cache_hit_ratio`（当前已存在），自动调整缓存大小和 TTL。命中率高时增加 TTL，低时减小 TTL 或缩小缓存以释放内存。

5. **自适应限流：** 监控后端存储的 `storage_latency_p99` 和 `storage_error_rate`（当前已存在），当后端延迟升高时自动降低 `RATE_LIMIT_RPS`，延迟恢复后回升。

### 边界情况与注意事项

| 场景 | 处理方式 |
|------|---------|
| **调优震荡** | 反馈控制回路可能产生震荡（过度调整→过度回调）。需要：PID 控制器风格或带死区的增量调整 |
| **参数耦合** | 增加 chunk window 可能同时增大内存使用和降低缓存命中率。需注意参数间的相互影响 |
| **安全变更** | 配置变更应该可审计、可回滚。每次自动调整记录到 `audit_log`；支持手动覆盖 |
| **启动阶段不调优** | 系统刚启动时指标为空或噪声大。需要前 N 分钟（可配置）的"观测期"，期间不触发自动调整 |
| **租户级差异** | 调优参数应支持 per-tenant 覆盖。大租户可能需要独立的 worker 池和缓存空间 |
| **保守 vs 激进** | 不同运营者调优风格不同。提供 `AUTO_TUNE_MODE=conservative|balanced|aggressive` 配置 |
| **可观测性的可观测性** | 自动调优系统本身需要可观测性：当前值、目标值、调整原因、调整历史。新增 `auto_tune_adjustments_total` 指标 |

### 架构概要

```
当前:
  env → config.Load() → 静态配置 → 创建组件（参数固定）

改进:
  + 新增包: internal/autotune/

  autotune.Controller {
    Watchers: []TunableWatcher{
      ChunkWindowWatcher{
        Metric:  indexer_chunk_duration_ms,
        Target:  "p95 < 500ms",
        Min:     200,
        Max:     2000,
        Current: 600,          // 初始值来自 AI_CHUNK_WINDOW
      },
      WorkerPoolWatcher{
        Metric:  job_queue_depth,
        Target:  "avg < 10",
        Min:     1,
        Max:     32,
        Current: 4,            // 初始值来自 JOBS_WORKERS
      },
      // ...
    }
    Loop:  每隔 T 秒（60s）:
      1. 读取当前指标
      2. 评估是否需要调整（偏差 > 阈值）
      3. 计算调整量（增量式，避免震荡）
      4. 应用调整（更新组件参数）
      5. 记录调整日志（audit entry + metric）
  }

  组件改为支持安全地热更新参数：
    Chunker.Update(window, overlap) → 原子替换
    WorkerPool.Resize(n) → 增减 goroutine
    Cache.Resize(maxEntries) → LRU 驱逐
```

### 影响评估

| 维度 | 评估 |
|------|------|
| 复杂度 | **中**（控制器模式本身简单；挑战在于每个组件的热更新实现 + 防震荡逻辑） |
| 用户影响 | **高**（降低调优门槛 × 提高资源利用率 × 改善自适应能力） |
| 代码变动 | 中-大（新增 `internal/autotune/` 包；每个可调组件需要实现 `Tunable` 接口的热更新方法；配置兼容性） |
| 差异化 | ★★★★☆（让 AeroVault 从"需要专家调优"变为"启动即优化"——这是开源存储项目的显著差异化） |
| 工作量估计 | 4-6 周（控制器框架：1.5 周；Chunk/Worker/Cache 适配：每周 1 个；限流适配：0.5 周；测试 + 观测性：1 周） |

---

## 综合建议：实施优先级与路线图

### 优先级矩阵

```
                   影响（合规/安全/成本）
                   低 ←──────────────→ 高
                    ┌──────────────────────────┐
   实施 高          │                          │
   难度            │  方向五（自调优）          │ 方向一（数据主权）★★★
    ↑              │                          │ 方向三（分层存储）★★★
   中              │                          │ 方向二（不可变审计）★★
                    │                          │
   低              │                          │ 方向四（统一授权）★★
                    └──────────────────────────┘
```

### 建议实施顺序

| 阶段 | 方向 | 原因 |
|------|------|------|
| **Phase 1（当前 Sprint）** | 方向四：统一授权模型 | 安全基线——修复跨协议授权缺口，这是四个协议共享后端的底线要求。工作量最小（2-3 周），影响最大（安全） |
| **Phase 2（下个 Sprint）** | 方向二：不可变审计轨迹 | 合规准入——SOC2/HIPAA 的必要条件。影响企业销售周期。与 Phase 1 无冲突可并行 |
| **Phase 3（1-2 月）** | 方向一：数据主权路由 | 架构基础——需要多后端路由能力。是 Phase 4（分层存储）的前置条件 |
| **Phase 4（2-3 月）** | 方向三：分层自动迁移 | 建立在 Phase 3 的多后端之上。成本优化的最终形态 |
| **持续进行** | 方向五：自调优 | 可独立推进，与 Phases 1-4 无依赖。建议从"最小闭环"（自适应 chunk window）开始逐步扩展 |
