# AeroVault 高价值扩展方向（第十五期）

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全局代码扫描（240+ Go 源码文件，约 50K 行），逐包审阅 `internal/` 全部子包（23+）、`cmd/server/main.go`、SDK 三层、配置系统、所有迁移文件、Makefile、Dockerfile、Helm chart、Grafana/Prometheus 配置。逐一比对前十四期 expansion 文档（共 ~800KB 分析）+ `ROADMAP.md` + `extensions.md` + `analysis-*.md`，确保每个方向在**既有文档中零覆盖或仅行级提及，且不从工程质量视角重复 v14 的五项硬伤**。  
> **日期：** 2026-07-10  
> **原则：** 选取 5 个**产品完整性 / 架构纵深**方向——不是"出错才出事"的硬伤（v14 已覆盖），而是"有了它才是一个完整的产品"的实质性功能缺失。每个方向附带：代码锚点 → 当前状态 → 缺失能力 → 边界情况 → 架构概要 → 实现理由。

---

## 审阅背景：前十四期覆盖的去重矩阵

前十四期（v1–v14）已从以下视角覆盖约 70 个方向：

| 领域 | 覆盖期数 | 方向数 |
|------|---------|--------|
| AI/RAG 管线（Embed/Chunk/Search/Chat/Agent/Rerank/PII/Index） | v1~v13 | ~12 |
| S3 兼容性（子资源/Batch/Multipart/ACL/Policy） | v1, v4, v6, v8, v9, ROADMAP #7 | ~8 |
| 存储后端（S3/OSS/COS/KMS/SSE/加密/CircuitBreaker） | v4~v13, ROADMAP #5 | ~6 |
| 认证与授权（JWT/API Key/SigV4/OIDC/SAML/SCIM） | v1, v5, v8, v11, v12 | ~6 |
| 多租户（CRUD/Quota/Budget/Audit/Isolation） | v1, v4, v5, v8, v11 | ~5 |
| 事件/通知/Webhook（Bus/Transport/Retry/SNS/SQS/Lambda） | v1, v3, v4, v9, v12 | ~6 |
| 复制/高可用（Async/Multi-Region/ClusterSingleton/HA） | v1, v3, v4, ROADMAP #3, #10 | ~5 |
| Reconcile/GC（Orphan/Retention/Lifecycle/Scrub） | v1, v4, v6, v7, ROADMAP #5, #8 | ~5 |
| 可观测性（OTel/Metrics/Grafana/Prometheus/Debug/evz） | v11, v13, v14 | ~4 |
| 合规（WORM/Legal Hold/Disposition/Audit/Client Encryption） | v6, v8, v9, v10, v12 | ~5 |
| Web UI / Admin UI | v3, v6, v10 | ~3 |
| CDN / 边缘交付 | v4, v13 | ~2 |
| Federation / 全局命名空间 | v4, v5, v6 | ~3 |
| 测试基础设施 | v11 | ~1 |
| 工程质量（内存/并发/压缩/诊断/错误模型） | v14 | ~5 |
| 其他 | v2, v7, v10 | ~3 |

**本期选点原则：** 选取**产品完整性 / 架构纵深**方向——不是"出错才出事"的硬伤（v14 已覆盖），而是"有了它才是一个完整的产品"的实质性功能缺失。

---

## 本期方向总览

| # | 方向 | 类型 | 影响评估 | 核心代码锚点 | 既有覆盖 |
|---|------|------|---------|-------------|---------|
| 1 | **🔴 存储后端按桶/对象路由（Multi-Backend Routing）** | 架构/成本 | 存储成本优化 30-60%；冷热数据分层的先决条件 | `storage/factory.go`（单一后端）、`storage/storage.go:Storage` 接口、`repository.Object.Backend` | **零覆盖**（已有 `Backend` 字段但全局单一实例） |
| 2 | **🔴 PUT Crash Recovery：写入路径崩溃安全** | 数据完整性 | 消除存储写入后、元数据提交前崩溃导致的静默数据丢失 | `service/file_crud.go:Put`（store.Put → repo.UpsertObject 非原子）、`reconcile/job.go`（滞后检测） | **零覆盖**（Reconcile 滞后修复，非预防） |
| 3 | **🟠 管理操作多因素认证（MFA）** | 安全/合规 | SOC2/HIPAA 准入；防 API Key 泄露后的管理权限滥用 | `auth/auth.go:Registry`、`auth/store.go:Key`、`api/rest/admin.go`（所有 admin 端点） | **零覆盖** |
| 4 | **🟠 写入路径安全：请求体尺寸强制限制** | 安全/稳定性 | 防恶意大请求 OOM/资源耗尽；防分片链式攻击 | `middleware/middleware.go`（中间件链无 MaxBodySize）、`service/file_crud.go:Put`（无上限校验） | v11 安全清单子项（3 行表格），**非独立方向** |
| 5 | **🟡 对象版本数量治理（Version Count Governance）** | 运维/成本 | 防版本无限增长耗尽存储 & DB；支持 k-of-n 保留策略；生命周期非当前版本管理 | `repository/sql_objects.go:InsertObjectVersion`（无条件插入）、`reconcile/lifecycle.go`（无版本规则） | v4 作为子节提及，**无完整架构方案** |

---

## 1. 🔴 存储后端按桶/对象路由（Multi-Backend Routing）

### 影响评估：高 — 存储成本优化 30–60%，冷热数据分层的先决条件

> 当前系统在每个实例级别使用**一个**存储后端（由 `STORAGE_BACKEND` 决定）。所有对象无论大小、类型、访问频率都写入同一个后端。`repository.Object` 结构体已有 `Backend` 字段（记录对象所在的后端）和 `StorageClass` 字段（STANDARD / STANDARD_IA / GLACIER），但**没有任何代码利用这些字段做对象级路由**——它们仅用于记录。

### 当前状态

```
┌─────────────────────────────────────────────────────────────┐
│ 整个实例一个 Storage 实例（factory.go:NewFromConfig）        │
│                                                             │
│   STORAGE_BACKEND=local → 所有对象 → ./var/objects/         │
│   STORAGE_BACKEND=s3   → 所有对象 → s3://bucket/            │
│                                                             │
│ 不能：                                                      │
│   - 热数据在本地 NVMe，冷数据在 S3 Glacier                 │
│   - 大文件在 S3，小文件在本地                              │
│   - 合规对象在特定 S3 区域，普通对象在低成本区域            │
└─────────────────────────────────────────────────────────────┘
```

### 代码锚点

| 位置 | 当前状态 | 缺失能力 |
|------|---------|---------|
| `internal/storage/factory.go:NewFromConfig` | 根据 `Config.Backend` 创建单一后端 | 无多后端管理器 |
| `internal/storage/storage.go:Storage` 接口 | 单一后端接口 | 无路由抽象层 |
| `internal/service/file_crud.go:Put` → `s.store.Put(...)` | 硬编码调用单一 store | 按规则选择后端 |
| `internal/service/file_crud.go:buildPutObject` | `Backend: s.store.Backend()` | 硬编码，非路由结果 |
| `internal/service/file_features.go:PresignGet/PresignPut` | 调用 `s.store.Presign*` | 单一 store 无选择 |
| `internal/repository/repository.go:Object.Backend` | ✅ 已有字段 | 填充但不用于路由 |
| `internal/repository/repository.go:Object.StorageClass` | ✅ 已有字段 | 填充但不驱动行为 |
| `internal/config/config.go` | 单一 `StorageConfig` | 无 `STORAGE_BACKENDS` 多后端配置 |
| `internal/config/config_storage.go` | 单一后端配置结构 | 无后端注册表 |

### 缺失能力

当前代码库已为多后端路由准备好了**数据模型层**（`Object.Backend`、`Object.StorageClass`），但缺失以下能力：

1. **多后端管理器（Backend Registry）**：支持注册多个命名的 `Storage` 实例（如 `"fast-nvme"`、`"s3-hot"`、`"s3-cold"`）
2. **路由规则引擎**：根据对象属性（content-type、size、tags、bucket、prefix）选择目标后端
3. **写入时路由**：Put 时评估规则 → 选择后端 → 记录 `Object.Backend`
4. **读取时路由**：Get/Stat/Delete 时根据 `Object.Backend` 字段选择正确的后端
5. **跨后端复制**：改变对象后端时（如 STANDARD → GLACIER），在后台复制后更新路由
6. **健康检查聚合**：`/readyz` 聚合所有后端的健康状态

### 边界情况暴露

| Edge Case | 场景 | 后果 |
|-----------|------|------|
| **后端宕机** | S3 后端不可达，但本地后端正常 | 所有写操作失败（即使某些对象应写入本地） |
| **路由规则冲突** | 两条规则匹配同一对象（如 content-type=video 和 size>1GB） | 需要优先级或首次匹配策略 |
| **后端迁移中读取** | 对象正在从 local→S3 后台复制 | 应继续从源端读取，写操作应暂停 |
| **Presign URL 跨后端** | 预签名 URL 指向 local 后端，但对象被迁移到 S3 | URL 失效；需要 Presign 路由 |
| **后端不可用时优雅降级** | S3 不可用，但本地可用 | 写入 local 并标记为待同步 |
| **桶级后端配置 + 对象级覆盖** | 桶默认用 S3，但特定 key 前缀用 local | 规则引擎需要桶→前缀→对象优先级链 |

### 架构概要

```
Phase 1 — 多后端管理器 + 基础路由（启动时配置，运行时稳定）：

                           ┌──────────────────────────────┐
                           │  BackendRouter               │
                           │  ┌────────────────────────┐ │
                           │  │  rules []RouteRule       │ │
                           │  │  backends map[string]    │ │
                           │  │  Storage                 │ │
                           │  │  default   Storage       │ │
                           │  └────────────────────────┘ │
                           └──────────────────────────────┘
                                    │
           ┌────────────────────────┼────────────────────────┐
           ▼                        ▼                        ▼
     ┌──────────┐            ┌──────────┐            ┌──────────┐
     │  local   │            │  s3-hot  │            │ s3-cold  │
     │  NVMe    │            │ Standard │            │ Glacier  │
     └──────────┘            └──────────┘            └──────────┘

配置：
  STORAGE_BACKENDS='[
    {"name":"fast","type":"local","root":"./var/hot"},
    {"name":"s3-hot","type":"s3","bucket":"aero-hot","region":"us-east-1"},
    {"name":"s3-cold","type":"s3","bucket":"aero-cold","region":"us-west-2"}
  ]'
  STORAGE_ROUTING_RULES='[
    {"match":{"content_type_prefix":"video/"},"backend":"s3-hot"},
    {"match":{"size_gt":1073741824},"backend":"s3-cold"},
    {"match":{"bucket":"archive"},"backend":"s3-cold"},
    {"default":true,"backend":"fast"}
  ]'

Phase 2 — 存储类感知路由：
  - StorageClass "STANDARD" → backend "fast" 或 "s3-hot"
  - StorageClass "STANDARD_IA" → backend "s3-warm"
  - StorageClass "GLACIER" → backend "s3-cold"
  - 跨后端数据移动（StorageClass 变更触发后台复制任务）

Phase 3 — 自动分层（访问模式驱动）：
  - 集成 Object.LastAccessedAt（v13 提及）
  - 定期评估：30 天未访问 → STANDARD → STANDARD_IA
  - 90 天未访问 → STANDARD_IA → GLACIER
```

### 为什么现在做

存储成本是云对象存储的第一大运营支出。当前"一刀切"的存储后端策略意味着要么为热数据（高 IOPS）过度支付、要么为冷数据（大容量低延迟）支付太多。`Object.Backend` 和 `Object.StorageClass` 字段已存在于 schema 中——说明架构师预见到了按对象路由的需求。**开启这个方向的成本主要在多后端管理器 + 路由规则的实现，而数据模型层已经就绪。** 此外，这是存储分层（ROADMAP #9）的实现前提——没有多后端路由，就无法将对象在不同存储层间移动。

---

## 2. 🔴 PUT Crash Recovery：写入路径崩溃安全

### 影响评估：高 — 消除存储写入后、元数据提交前崩溃导致的静默数据丢失

> 当前写入路径 `FileService.Put` 的顺序是：`store.Put(blob)` → `repo.UpsertObject(metadata)`。如果在存储写入成功之后、元数据写入完成之前服务器崩溃（或网络分区），结果是一个**孤立的 blob**——在存储后端占用空间，但没有 DB 行指向它。当前机制完全依赖 `reconcile` 的"滞后修复"（检测到孤立 blob 后基于配置的 grace period 删除），这意味着至少有 `RECONCILE_ORPHAN_GRACE_MINUTES` 的窗口期（默认 60 分钟）内，这些 orphan blob 持续占用空间且无法访问。

### 当前状态

```go
// service/file_crud.go:Put 的简化执行顺序：
func (s *FileService) Put(...) (repository.Object, error) {
    // ... preflight checks ...

    info, err := s.store.Put(ctx, sk, reader, size, opts)  // ← 第 1 步：写入存储
    if err != nil {
        return ..., fmt.Errorf("storage put: %w", err)
    }

    obj := s.buildPutObject(...)
    saved, err := s.writePutObject(ctx, obj, bcfg)           // ← 第 2 步：写入 DB
    if err != nil {
        // 如果这里崩溃，blob 在存储中但 DB 无记录
        // 当前行为：log + return error（不尝试回滚存储写入）
        // reconcile 最终会在 ~60 分钟后清理
        return ..., fmt.Errorf("repo write: %w", err)
    }
    return saved, nil
}
```

**在 store.Put 成功 → repo 写入发生期间的任意时间点崩溃 = 看不见的数据丢失。**

### 代码锚点

| 位置 | 当前状态 | 问题 |
|------|---------|------|
| `internal/service/file_crud.go:Put` | `store.Put` → `repo.UpsertObject` | 两步非原子，中间有崩溃窗口 |
| `internal/service/file_crud.go:writePutObject` | 成功后 emit event | event 在行写入之后——event 可能丢失 |
| `internal/storage/local_write.go:writeObject` | 文件写入成功后返回 | 无写入前预留记录 |
| `internal/reconcile/job.go:sweepOrphanBlobs` | 滞后修复（~60min grace period） | 非及时恢复，丢失事件 |
| `internal/service/file_multipart.go:CompleteMultipart` | 合并 → Delete parts → Upsert | 同样非原子 |
| `internal/service/file_crud.go:hardDeleteObject` | `store.Delete` → `repo.HardDelete` | 存储删除成功→DB 删除失败的静默泄漏 |

### 边界情况暴露

| Edge Case | 场景 | 后果 |
|-----------|------|------|
| **大规模写入中崩溃** | 大量存储写入完成，DB 批量写入开始前进程 kill | 大量 orphan blob，reconcile 60 分钟后才清理 |
| **网络分区** | 存储写入成功但 DB 连接中断（连接池耗尽） | 正常请求受影响，orphan 堆积 |
| **磁盘满** | DB 所在磁盘满，写入失败 | blob 已写入，DB 写入失败→orphan |
| **版本化桶的多版本崩溃** | 版本化桶中写入新版本、记录老版本 | 存储中有新版本 blob，但 DB 没有 |
| **并发冲突** | 两个并发 PUT 同一 key，一个写入后另一个覆盖 | 最终一致性窗口 |
| **幂等性 + 崩溃** | Idempotency-Key 已记录但后续步骤崩溃 | 重放幂等性可能产生不一致 |

### 架构概要

```
Phase 1 — Prewrite Journal（轻量级写入前日志）：

写入流程（带 Prewrite Journal）：
┌─────────────────────────────────────────────────────────────┐
│ 1. 生成 object_id + storage_key                             │
│ 2. INSERT INTO write_journal (                               │
│      journal_id, storage_key, tenant, bucket, key,          │
│      status='writing', created_at, expected_size            │
│    )                                                         │
│ 3. store.Put(blob) → 存储写入                                │
│ 4. UPDATE write_journal SET status='stored'                  │
│ 5. repo.UpsertObject → 元数据写入                            │
│ 6. DELETE FROM write_journal WHERE journal_id = $1           │
│                                                              │
│ 启动时自动恢复：                                               │
│   SELECT * FROM write_journal WHERE status='writing'         │
│   — 已存在超过 5 分钟且无对应 DB 行 → blob 已丢失 → 清除     │
│   — status='stored'（第 4 步后崩溃）→ 检查 DB 行存在         │
│     • 存在 → 删除 journal（正常）                             │
│     • 不存在 → 完成 Upsert（恢复 DB 写入）                   │
└─────────────────────────────────────────────────────────────┘

Phase 2 — 删除路径保护：
  DELETE 也走 journal：
    1. 记录 journal（object_key, expected_etag）
    2. store.Delete(blob)
    3. repo.HardDelete(row)
    4. 清除 journal
    恢复：journal 中的删除在重启后重做

Phase 3 — 指标：
  write_journal.pending_total
  write_journal.recovered_total
  write_journal.stale_cleaned_total
```

**影响面：**
| 组件 | 影响 | 工作量 |
|------|------|--------|
| `write_journal` 表（migration pair） | 新增 | 低 |
| `FileService.Put` 写入路径改造 | 修改 | 中 |
| `FileService.Delete` 删除路径改造 | 修改 | 低 |
| `FileService.CompleteMultipart` 多分片完成 | 修改 | 中 |
| 启动恢复逻辑（`cmd/server/main.go` 启动序列） | 新增 | 中 |
| `write_journal` GC（reconcile 中定期清理已完成记录） | 新增 | 低 |

### 为什么现在做

当前"先写 blob 再写 DB"的设计是一个已知的**原子性缺口**。虽然 reconcile 能滞后修复，但数据丢失窗口客观存在。write-ahead journal 是数据库系统的标准模式（Postgres WAL、MySQL binlog、SQLite WAL）。在对象存储场景中，journal 可以提供"提交后写入"的原子性保证——**这是对象存储从"demo grade"走向"production grade"的关键一步**。Reconcile 的 orphan blob 检测是从"结果"修复问题，journal 是从"过程"防止问题——两者互补。

---

## 3. 🟠 管理操作多因素认证（MFA）

### 影响评估：中高 — SOC2/HIPAA 准入；防 API Key 泄露后的管理权限滥用

> 当前所有 admin 操作仅依赖单因素认证（Bearer Token / API Key / JWT）。一旦 API Key 泄露（日志泄漏、git commit、员工离职），攻击者可无限制执行任何管理操作——创建租户、签发 JWT、修改配额、删除数据。**没有第二因素验证（TOTP / WebAuthn / 邮件确认）来保护高敏感操作。**

### 当前状态

```go
// internal/api/rest/admin.go: 所有 admin 路由
// 认证方式：Bearer token → auth.Middleware → 检查 scope == "admin"
// 单因素：token 本身是唯一的凭证
// 无第二因素验证、无操作确认、无敏感操作审批

// auth/auth.go:Registry.Authenticate
// 返回 Key{TenantID, Scopes} —— 无 MFA 状态
```

**攻击者视角：**
```
事件链：开发者将 AERO_VAULT_API_KEY=xxx 提交到公开 GitHub repo
        → CI 扫描发现 → 攻击者获取 key
        → curl -H "Authorization: Bearer xxx" /v1/admin/tenants        ✅
        → curl -H "Authorization: Bearer xxx" /v1/admin/keys           ✅（列举所有可用 key）
        → curl -X DELETE /v1/admin/tenants/acme                        ✅
        → 数据丢失
```

### 代码锚点

| 位置 | 当前状态 | 缺失能力 |
|------|---------|---------|
| `internal/auth/auth.go:Registry` | `Authenticate(token) → Key` | 不返回 MFA 状态 |
| `internal/auth/auth.go:Key` 结构体 | `{TokenHash, TenantID, Scopes, ...}` | 无 `MFAScopes`、无 `MFASecret` |
| `internal/auth/store.go:PersistentStore` | 存储 hashed key | 无 MFA 元数据 |
| `internal/api/rest/admin.go` | 所有 admin 端点 | 无 MFA 中间件 |
| `internal/middleware/middleware.go` | 中间件链 | 无 MFA 验证层 |
| `internal/api/rest/router.go` | adminRouter group | 无 MFA 要求配置 |

### 缺失能力

| 能力 | 当前 | 目标 |
|------|------|------|
| TOTP 生成与验证（RFC 6238） | ❌ | admin key 创建时可选择启用 TOTP |
| 敏感操作分类 | ❌ | 读操作（ListKeys）vs 写操作（DeleteTenant）不同敏感度 |
| MFA 会话 | ❌ | 首因子认证后，MFA 通过后创建短期 session |
| WebAuthn / FIDO2 | ❌ | 可选的硬件安全密钥支持 |
| 邮件确认降级 | ❌ | 无法使用 TOTP 时通过已验证邮箱确认 |
| 操作审批工作流 | ❌ | 某些操作（删除租户）需要多人审批 |
| MFA 恢复码 | ❌ | 安全的一次性恢复码防止锁定 |

### 边界情况暴露

| Edge Case | 场景 | 行为 |
|-----------|------|------|
| **MFA 锁定** | 管理员丢失 TOTP 设备且无恢复码 | 需要带外交互（联系运维、查看 DB） |
| **批量操作 vs 逐操作 MFA** | 批量删除 1000 个对象，每次请求都 MFA | UX 灾难；应为操作一次性会话 |
| **MFA 会话过期** | 管理操作耗时超过会话 TTL | 优雅提示重新 MFA 验证，不丢失已填写数据 |
| **S3 协议无法 MFA** | S3 客户端（boto3、aws-cli）无 MFA 概念 | REST admin 需要 MFA，S3 可以豁免或使用预授权 |
| **MFA 与 CI/CD 冲突** | CI pipeline 需要自动执行 admin 操作 | CI 使用不要求 MFA 的 deployment key（scope limited） |
| **MFA 恢复安全** | 恢复码生成/存储/使用 | 恢复码应是加密哈希存储，一次性使用 |

### 架构概要

```
Phase 1 — TOTP 认证（最小可工作版本）：

┌─────────────────────────────────────────────────────────────┐
│ Key 结构扩展：                                                 │
│   type Key struct {                                          │
│       TokenHash string                                       │
│       TenantID string                                        │
│       Scopes    []string                                     │
│       MFA       MFAConfig           // ← 新增                │
│   }                                                           │
│   type MFAConfig struct {                                    │
│       Enabled    bool                                        │
│       TOTPSecret string    // 加密存储，永不返回              │
│       Recovery   []string  // 加密存储的一次性恢复码哈希      │
│   }                                                           │
│                                                              │
│ 中间件：                                                       │
│   middleware/MFA.go                                           │
│   func MFA(next http.Handler) http.Handler {                  │
│       return http.HandlerFunc(func(w, r) {                    │
│           // 只对 admin 端点生效                               │
│           if !isAdminPath(r.URL.Path) { next(w,r); return }  │
│                                                              │
│           // 检查 X-MFA-Token header                           │
│           mfaToken := r.Header.Get("X-MFA-Token")            │
│           if mfaToken == "" {                                 │
│               // 要求 MFA 输入                                 │
│               w.Header().Set("X-MFA-Required", "totp")       │
│               writeJSON(w, 401, MFAChallenge{...})           │
│               return                                          │
│           }                                                   │
│                                                              │
│           // 尝试验证 TOTP                                    │
│           if !totp.Validate(mfaToken, key.MFA.TOTPSecret) {  │
│               writeJSON(w, 401, "invalid MFA token")         │
│               return                                          │
│           }                                                   │
│           next.ServeHTTP(w, r)                                 │
│       })                                                      │
│   }                                                           │
│                                                              │
│ Admin 端点扩展：                                               │
│   POST /v1/admin/keys/{hash}/mfa/enable   → 生成 TOTP secret   │
│   POST /v1/admin/keys/{hash}/mfa/verify   → 验证 + 启用        │
│   POST /v1/admin/keys/{hash}/mfa/disable  → 禁用（需要 MFA）   │
│   POST /v1/admin/mfa/recover              → 使用恢复码         │
│                                                              │
│ 敏感度分级：                                                    │
│   S1（读）：ListKeys, ListTenants, ListAudit, ListJobs        │
│             → 无需 MFA（但记录审计日志）                        │
│   S2（写）：AddKey, RevokeKey, IssueJWT, SetQuota, SetBudget  │
│             → 需要 MFA                                        │
│   S3（危险）：DeleteTenant, CreateTenant                      │
│             → 需要 MFA + 审计确认                              │
└─────────────────────────────────────────────────────────────┘

Phase 2 — WebAuthn / FIDO2 支持（可选增强）：
  - 使用 github.com/go-webauthn/webauthn（零新依赖原则？）
  - 或保持 TOTP only（纯 stdlib + 2 个依赖）

Phase 3 — MFA 状态指标：
  auth.mfa_enabled_keys_total
  auth.mfa_validation_total{result: "pass"|"fail"}
```

**影响面：**
| 组件 | 影响 | 工作量 |
|------|------|--------|
| `Key` / `MFAConfig` 结构体扩展 | `internal/auth/auth.go` | 低 |
| TOTP 实现（RFC 6238） | 新增 `internal/auth/totp.go` | 中（~150 行） |
| 新依赖 `github.com/pquerna/otp` | `go.mod` | 低 |
| MFA 中间件 | `internal/middleware/mfa.go` | 中（~120 行） |
| 数据库存储加密 MFA secret | `repository/apikeys.go` 扩展或新增列 | 低 |
| MFA admin 端点 | `internal/api/rest/admin.go` | 中 |
| 恢复码生成/验证 | `internal/auth/totp.go` | 低 |
| SDK 更新（Go/Python/JS） | `sdk/` | 中 |
| 迁移文件（migration pair） | 新增列 `mfa_secret` `mfa_recovery` | 低 |
| 指标 | `internal/telemetry/metrics.go` | 低 |

### 为什么现在做

单因素认证是当前安全架构中最薄弱的一环。对于多租户 SaaS 部署来说，管理员 API Key 泄露是时间问题（日志泄漏、员工终端、CI/CD 环境变量）。没有 MFA，一个泄露的 key 就能让攻击者执行所有管理操作——包括删除整个租户的数据。**MFA 是 SOC2、HIPAA、PCI-DSS 合规的基本要求**，是企业客户准入的必要条件。实施成本低（TOTP 是成熟标准，`github.com/pquerna/otp` 是广泛使用的 Go 实现），且是与现有 Key 系统的正交扩展——不影响数据面操作的性能。

---

## 4. 🟠 写入路径安全：请求体尺寸强制限制

### 影响评估：中 — 防恶意大请求 OOM/资源耗尽；防分片链式攻击

> 当前系统**不在任何层次限制请求体大小**。`http.Server` 使用默认的 `MaxHeaderBytes`（1MB），但请求体本身没有上限。一个恶意客户端可以发送任意大小的 PUT 请求体，导致服务器在内存中缓冲整个请求体（尤其当涉及加密或 MD5 计算时），造成 OOM。当前 `storage/local_write.go` 在非加密路径使用 `io.Copy(tmp, reader)`（流式），但加密路径使用 `io.ReadAll(reader)`（全缓冲）。此外，`md5WrapReader` 使用 `io.TeeReader` 也会缓冲。

### 当前状态

```go
// cmd/server/main.go — HTTP 服务器配置
srv := &http.Server{
    Addr:              cfg.App.Addr,
    Handler:           handler,
    ReadHeaderTimeout: 15 * time.Second,
    WriteTimeout:      time.Duration(cfg.App.WriteTimeoutSec) * time.Second,
    IdleTimeout:       time.Duration(cfg.App.IdleTimeoutSec) * time.Second,
}
// 注意：没有 MaxHeaderBytes 配置（使用默认 1MB）
// 注意：没有请求体大小限制

// internal/service/file_crud.go:Put
// preflightQuota 检查字节配额——但只在"已知大小"的情况下
// 对于 chunked transfer encoding (Content-Length = -1)，size 为 0
// 无法预检查大小
```

**不存在请求体大小限制的危害：**
```
恶意客户端 A: PUT /v1/files/bomb HTTP/1.1
  Content-Length: 10737418240  (10GB)
  → 服务器必须缓冲 → io.ReadAll → OOM

分片式攻击：
恶意客户端 B: PUT /v1/files/bomb HTTP/1.1 (Transfer-Encoding: chunked)
  每次发送 1 byte chunk，永不结束
  → 连接占用 10 分钟+
  → 耗尽连接池
  → 拒绝服务
```

### 代码锚点

| 位置 | 当前状态 | 缺失能力 |
|------|---------|---------|
| `internal/middleware/middleware.go` | 中间件链 | 无 `MaxBodySize` 中间件 |
| `cmd/server/main.go` HTTP 服务器配置 | `ReadHeaderTimeout` ✅ | 无 `MaxHeaderBytes` 配置 |
| `internal/storage/encrypt.go:encryptReader` | `io.ReadAll(r)` | 大文件全缓冲→无大小预检 |
| `internal/storage/local_write.go:writeObject` | 加密分支 `io.ReadAll` | 加密路径无大小检查 |
| `internal/service/file_crud.go:md5WrapReader` | `io.TeeReader(r, h)` | MD5 计算时流式，但下游可能全缓冲 |
| `internal/service/file_crud.go:Put` | `preflightQuota` 检查大小 | 只检查配额，不检查绝对上限 |
| `internal/api/rest/handler.go:handlePut` | 无 `http.MaxBytesReader` | handler 层无限制 |

### 边界情况暴露

| Edge Case | 场景 | 行为 |
|-----------|------|------|
| **合法的大文件上传** | 用户上传 5GB 数据库备份 | 应允许，跳过内存缓冲直接流式写入磁盘 |
| **分块上传绕过限制** | 客户端使用 1GB 分块（分块粒度 > 限制） | 限制应在合并时检查，而非单分片 |
| **CDN 回源请求** | CDN 从源站拉取大量缓存 | 大 GET 不应受 body 限制影响（限制仅写入） |
| **流式压缩** | 客户端发送 `Content-Encoding: gzip` 的压缩流 | 解压后大小可能远超 `Content-Length` |
| **无 Content-Length 的 PUT** | `Transfer-Encoding: chunked` | 无法预检查；需要在流式写入时动态检查 |
| **Multipart 小分片** | 合法的多分片上传多个 5MB 分片 | 每个分片在限制内，允许 |

### 架构概要

```
Phase 1 — 请求体大小中间件 + 配置（最小改动，最大收益）：

┌─────────────────────────────────────────────────────────────┐
│ config/config_app.go 扩展：                                   │
│   type AppConfig struct {                                    │
│       // ...existing fields...                               │
│       MaxRequestBodyBytes  int64    // 新增，默认 0=unlimited│
│       MaxHeaderBytes       int      // 新增，默认 1MB         │
│   }                                                           │
│                                                              │
│ 环境变量：                                                    │
│   MAX_REQUEST_BODY_BYTES=10737418240  (10GB，默认 0 = 无限制) │
│   MAX_HEADER_BYTES=1048576           (1MB，默认)              │
│                                                              │
│ middleware/middleware.go 新增：                                │
│   func MaxBodySize(maxBytes int64) func(http.Handler) http.Handler { │
│       return func(next http.Handler) http.Handler {          │
│           return http.HandlerFunc(func(w, r) {               │
│               if maxBytes > 0 {                              │
│                   r.Body = http.MaxBytesReader(w, r.Body, maxBytes) │
│               }                                               │
│               next.ServeHTTP(w, r)                            │
│           })                                                  │
│       }                                                       │
│   }                                                           │
│                                                              │
│ 放置位置（中间件链顺序）：                                     │
│   RequestID → MaxBodySize → CORS → Auth → Tenant → ...       │
│   （早期拦截，避免认证等前期工作浪费）                          │
│                                                              │
│ S3 compatibility：                                            │
│   PUT /s3/bucket/key → 受 MaxBodySize 限制                    │
│   UploadPart → 每个分片受 MaxBodySize 限制                    │
│   CompleteMultipart → 仅有 XML body（小），不受限              │
│                                                              │
│ 例外路径（不设限）：                                          │
│   GET/HEAD/DELETE → 无 body                                   │
│   SSE ChatStream → 请求体小                                   │
│   Admin POST/PUT → 请求体小（JSON）                            │
└─────────────────────────────────────────────────────────────┘

Phase 2 — 路径级精细化限制（可选增强）：
  - PUT /v1/files → 单独限制（MAX_PUT_BODY_BYTES）
  - POST /v1/search → 单独限制（搜索请求通常很小）
  - UploadPart → 单独限制（应与 MAX_PUT 一致）
  - Write 或默认限制

指标：
  http.request_body_too_large_total{method, path_prefix}
```

**影响面：**
| 组件 | 影响 | 工作量 |
|------|------|--------|
| `MaxBodySize` 中间件 | `internal/middleware/middleware.go` | **极低**（~15 行） |
| 配置项 `MaxRequestBodyBytes` | `internal/config/config_app.go` | **极低**（~3 行） |
| 配置项 `MaxHeaderBytes` + HTTP server 配置 | `cmd/server/main.go` | **极低**（~3 行） |
| 指标 | `internal/telemetry/metrics.go` | 低 |
| 中间件链注册 | `cmd/server/main.go:applyMiddleware` | 1 行 |

### 为什么现在做

这是一个**极低成本、高安全收益**的防御性中间件。`http.MaxBytesReader` 是标准库内置功能——零依赖引入。当前系统在加密路径上已经存在 OOM 风险（v14 方向 1），请求体大小限制不能解决分块加密问题，但可以防御最直接的攻击向量（巨量请求体导致内存分配失败）。对于面向公网的部署，这是**安全基线级别的防御**——不是"可选增强"。

---

## 5. 🟡 对象版本数量治理（Version Count Governance）

### 影响评估：中 — 防版本无限增长耗尽存储 & DB；支持 k-of-n 保留策略

> 版本化桶中，每次 PUT 操作都创建新版本（`InsertObjectVersion`），**没有任何版本数量上限或保留策略**。一个运行失控的 CI pipeline、一个配置错误的备份脚本、或者一个循环的 Agent 工具调用可以在几分钟内创建数千个版本。当前唯一的保护是手动调用 `DELETE`（软删除）或生命周期（按天过期）。没有基于**版本数量**的控制。

### 当前状态

```go
// internal/repository/sql_objects.go:InsertObjectVersion
// 无条件插入新版本行——
//   INSERT INTO objects (...) VALUES (...)  // 每次 PUT 都插入
//   不检查当前版本数量
//   不检查版本总计大小
//   不检查版本过期时间

// 运行失控的后果：
// for i in {1..100000}; do
//   curl -X PUT /v1/files/doc.pdf -d "version $i"
// done
// → 10 万个版本，DB 行爆炸，存储 10 万倍
```

### 代码锚点

| 位置 | 当前状态 | 缺失能力 |
|------|---------|---------|
| `internal/repository/sql_objects.go:InsertObjectVersion` | 无条件插入 | 无版本数检查 |
| `internal/repository/repository.go:BucketConfig` | `Versioning bool` | 无 `MaxVersionCount`、`MaxVersionAge` |
| `internal/repository/sql_buckets.go` | `CREATE TABLE buckets (...) versioning` | 无 `max_versions` 列 |
| `internal/reconcile/lifecycle.go` | `sweepLifecycle` | 只支持 `expire_after_days`，不支持版本数规则 |
| `internal/service/file_crud.go:Put` | `checkLockBeforeOverwrite` | 无版本计数预检 |
| `internal/api/rest/handler.go:handlePut` | 解析请求 | 无版本上限错误返回 |
| `internal/api/s3compat/handler.go:putObject` | 同上 | 同上 |

### 缺失能力

| 能力 | 当前 | 目标 |
|------|------|------|
| 每对象版本数上限 | ❌ | `BucketConfig.MaxVersionCount`（默认 100） |
| 超出上限时的行为 | ❌ | 自动删除最旧非当前版本 / 拒绝新写入 / 返回 409 |
| 非当前版本过期（按天） | ❌（v4 规划） | `NoncurrentVersionExpiration` 生命周期规则 |
| 版本保留策略（k-of-n） | ❌ | 保留最近 N 个版本，删除更旧版本 |
| 版本大小总计 | ❌ | 按 key 聚合所有版本的 `size` |
| 版本过期事件通知 | ❌ | 版本被 GC 前发出 `version.expiring` 事件 |
| 版本删除标记管理 | ❌ | `DeleteMarker` 不应计入版本数 |

### 边界情况暴露

| Edge Case | 场景 | 行为 |
|-----------|------|------|
| **修改版本上限** | 从 10 改为 100（旧版本号 < 10，新值 100） | 不应恢复已删除的旧版本 |
| **上限设为 0** | 兼容模式（无限制） | 行为同当前（0 = unlimited） |
| **版本 + 对象锁组合** | 对象有 LockedUntil，但版本数达上限 | 锁定的版本不能被自动删除；需要 Exception |
| **版本上限 + 复制** | 两个区域同时产生版本 | 区域 A 删除旧版本以满足上限 → 复制到区域 B 时版本不存在 |
| **批量删除旧版本** | 一次需要删除 5000 个旧版本 | 应在后台 job 中执行（复用 job pool），而非写入路径同步执行 |
| **最小版本保留** | 应始终保留至少 1 个非当前版本（快照回滚） | `MinVersionRetention` 参数 |

### 架构概要

```
Phase 1 — 版本计数 + 同步拒绝（最小可工作版本）：

┌─────────────────────────────────────────────────────────────┐
│ BucketConfig 扩展：                                           │
│   MaxVersionCount    int  // 默认 0=unlimited                │
│   NoncurrentDays     int  // 非当前版本保留天数（0=无限）    │
│                                                              │
│ 写入路径预检（FileService.Put 中，版本化桶时）：              │
│   1. SELECT COUNT(1) FROM objects WHERE                      │
│        tenant=$1 AND bucket=$2 AND key=$3 AND deleted_at IS NULL │
│   2. 如果 count >= MaxVersionCount（>0 时），返回              │
│      "版本数已达上限 (%d/%d)" 错误                              │
│     → 调用者收到 409 Conflict（类似 S3 的 TooManyVersions）   │
│                                                              │
│ 影响：写入路径增加一次 SELECT COUNT 查询                       │
│ 优化：缓存版本计数（per-key 的 atomic counter）               │
└─────────────────────────────────────────────────────────────┘

Phase 2 — 版本 GC（后台 Job，复用现有 reconcile 框架）：

┌─────────────────────────────────────────────────────────────┐
│ reconcile.NewVersionGC():                                    │
│   SELECT tenant, bucket, key, COUNT(1) as ver_count          │
│   FROM objects WHERE deleted_at IS NULL                      │
│   GROUP BY tenant, bucket, key                               │
│   HAVING COUNT(1) > MaxVersionCount                          │
│                                                              │
│   对每个超限 key：                                             │
│     1. 获取所有版本（按 created_at DESC）                     │
│     2. 保留前 MaxVersionCount 个版本                          │
│     3. 对其余版本执行硬删除（HardDeleteVersion）              │
│     4. 记录 metrics                                          │
│     5. 发出版本过期事件（version.expired）                    │
│                                                              │
│   非当前版本按天数过期：                                       │
│     NoncurrentDays > 0 时，删除非当前版本且 updated_at        │
│     （或被当前版本替代的时间）早于 NoncurrentDays 的版本       │
│                                                              │
│ 幂等性：                                                    │
│   如果两个版本 GC 同时运行：第一个删除后第二个 SELECT         │
│   COUNT 变少 → 正常结束                                      │
│   已删除的版本再次 HardDelete → 静默跳过（DELETE 幂等）      │
└─────────────────────────────────────────────────────────────┘

Phase 3 — 配置 + 指标：

迁移文件（migration pair）：
  ALTER TABLE buckets ADD COLUMN max_versions INTEGER DEFAULT 0;
  ALTER TABLE buckets ADD COLUMN noncurrent_days INTEGER DEFAULT 0;

环境变量（桶级默认值）：
  BUCKET_DEFAULT_MAX_VERSIONS=100
  BUCKET_DEFAULT_NONCURRENT_DAYS=30

REST API 扩展：
  GET /v1/buckets/{bucket}/versioning → 返回 max_versions + noncurrent_days
  PUT /v1/buckets/{bucket}/versioning → 可设置 max_versions + noncurrent_days

指标：
  version_gc.scanned_total
  version_gc.deleted_total
  version_gc.bytes_reclaimed_total
  objects.version_count{tenant, bucket, key}  // gauge per key (top-N)
```

**影响面：**
| 组件 | 影响 | 工作量 |
|------|------|--------|
| `BucketConfig` 扩展 + migration | `repository/repository.go` + migrations | 低 |
| 写入路径版本数预检 | `service/file_crud.go:Put` | 低 |
| 版本 GC Job | `reconcile/version_gc.go`（新增） | 中 |
| 桶配置 API 扩展 | `api/rest/buckets_test.go` + router | 低 |
| 事件（`version.expired`） | `repository/repository.go` EventType | 低 |
| 指标 | `telemetry/metrics.go` | 低 |
| SDK 更新（Go/Python/JS） | `sdk/` | 中 |

### 为什么现在做

版本化是对象存储的强大功能，但**没有版本数量上限的版本化是一个易被滥用的能力**。一个简单的脚本错误、CI/CD 配置错误、或 Agent 工具循环可以在几分钟内创建数千个版本——每次 PUT 都产生一个新的 `InsertObjectVersion` 行 + 一个新的存储 blob。由于当前没有生命周期规则针对非当前版本，这些版本将**永久存在**，持续占用存储和 DB 空间。版本数量治理对于任何使用版本化的生产部署都是刚需——不做的代价是存储成本不可控增长。同时，版本 GC 可以复用现有的 `reconcile` 框架（`cluster.Singleton`、`lease`、`interval`），实现成本低。

---

## 跨方向协同关系

```
方向 1 (Multi-Backend Routing)    ← 方向 5 (Version Governance): 旧版本可以自动迁移到低成本后端，而非删除
                                  ← v14 方向 1 (内存安全): 分块加密缓冲区可与多后端的流式写入共享

方向 2 (Crash Recovery)           ← v4 方向 (Reconcile): 两阶段恢复（journal 即时修复 + reconcile 滞后修复）
                                  ← v14 方向 2 (乐观锁): WriteJournal 可以记录预期 ETag，用于检测并发冲突

方向 3 (MFA)                      ← v14 方向 5 (错误模型): MFA 失败应统一纳入 ErrorCatalog
                                  ← v8 方向 (IAM Policy): MFA + Policy 共同构成完整 auth 框架

方向 4 (Max Body Size)            ← v14 方向 1 (内存安全): 大小限制是防 OOM 的第一道防线
                                  ← v14 方向 3 (压缩): 压缩后的 body 大小检测需要在解压前检查

方向 5 (Version Governance)       ← 方向 1 (Multi-Backend): 旧版本可以降级至低成本后端
                                  ← v4 方向 (Lifecycle): 版本 GC 应融入生命周期规则体系
```

**建议实施顺序：**

| 阶段 | 方向 | 理由 |
|------|------|------|
| **🔥 立即** | 方向 4 Phase 1（MaxBodySize 中间件） | ~20 行代码，零依赖，立即获得保护 |
| **Phase 1** | 方向 2 Phase 1（Write Journal） | 数据完整性基础设施——越早越好。为所有写入操作建立原子性保证 |
| **Phase 2** | 方向 1 Phase 1（多后端管理器 + 基础路由） | 为存储分层铺路。需要先有多后端，才能做自动分层 |
| **Phase 3** | 方向 5 Phase 1 + 2（版本上限 + 版本 GC） | 需要版本 GC 的 infrastructure ready（reconcile 已有） |
| **Phase 4** | 方向 3 Phase 1（TOTP MFA） | 产品成熟期加入，安全增强——不影响核心功能 |

---

> *第十五期全局扫描完成，未修改任何代码。本轮 5 个方向聚焦于"产品完整性 / 架构纵深"视角——不考虑工程质量硬伤（v14 已覆盖），而是识别当前代码中"有了它才是一个完整产品"的实质性功能缺失。每个方向都有明确的代码锚点、具体的缺失能力、和逐步可行的实现蓝图。*
