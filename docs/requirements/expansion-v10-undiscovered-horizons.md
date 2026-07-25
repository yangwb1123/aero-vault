# AeroVault 高价值扩展方向（第十期）

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全局代码扫描（~50K 行 Go 源码），逐一审阅前九期 expansion 文档（`expansion-directions.md` ~ `expansion-v9-architectural-whitepaper.md`）、`ROADMAP.md`、`TODO.md`、`CHANGELOG.md` 以及全部 `analysis-v[1-8]-gaps-roadmap.md`。  
> 选取 5 个**既有文档从未系统讨论**的工程方向。  
> **日期：** 2026-07-10  
> **原则：** 不编写任何实现代码。

---

## 审阅摘要：前九期已覆盖的范围

| 覆盖类别 | 对应文档 | 频率 |
|---------|---------|------|
| S3 兼容性（Policy/CORS/Logging/Notification） | v8, v9, ROADMAP #7 | 3× |
| AI 管线（检索/Embedding/Chat/Agent/PII/Cache） | v1~v9, ROADMAP #1~#2 | 9×+ |
| 存储后端（OSS/COS/KMS/SSE/Encryption） | v4~v9, ROADMAP #5 | 6×+ |
| 事件系统（Webhook/Postgres Transport/Bus） | v6, v8, v9 | 3× |
| 多租户/配额/预算 | v3, v4, ROADMAP #2, #4 | 4× |
| 身份联邦/SSO/OIDC/SAML/SCIM | v5 | 1× |
| Web UI / CLI / MCP | v8 | 1× |
| 批量操作 / 文件夹管理 | v3 | 1× |
| 内容去重 / CAS | v7 | 1× |
| 合规（WORM/Legal Hold/生命周期治理） | v6, v9 | 2× |
| 冷存储 / Deep Archive / Restore | v5 | 1× |
| 跨区域 Active-Active 复制 | v9 | 1× |
| CDC 流 / 可回放变更日志 | v9 | 1× |
| WASM 函数 / 事件触发计算 | v9 | 1× |
| 结构化元数据 Schema | v7 | 1× |
| 备份 / 快照 / 容灾 | v8 | 1× |

**本期选点原则：** 选取上述矩阵中**零覆盖或仅骨架提及**的方向，且满足：① 企业级产品必需；② 与现有架构可增量集成；③ 有明确的边界情况和 edge cases。

---

## 本期方向总览

| # | 方向 | 类型 | 影响 | 核心代码锚点 | 既有文档覆盖 |
|---|------|------|------|-------------|-------------|
| 1 | **客户端加密（CSE）/ 零信任架构** | 安全/兼容 | 🔴 企业合规准入 + S3 兼容缺口 | `internal/storage/encrypt.go` / `internal/api/s3compat/handler.go` | 零覆盖（仅 v3 一行提及"SSE-C 互斥"） |
| 2 | **对象级访问审计追踪** | 安全/合规 | 🔴 SOC2/PCI/HIPAA 必须 | `internal/repository/audit.go` / `internal/service/file.go` | **零覆盖** |
| 3 | **跨存储后端在线数据迁移** | 运维/韧性 | 🟠 生产迁移/多云/重平衡必需 | `internal/storage/storage.go` / `internal/jobs/jobs.go` | **零覆盖** |
| 4 | **API 版本治理与兼容性保障** | 架构/可维护 | 🟠 长期 API 演进的基础设施 | `internal/api/rest/router.go` / `internal/api/rest/openapi.go` / `sdk/` | **零覆盖** |
| 5 | **优雅关闭与生产级部署韧性** | 运维/可靠性 | 🟠 零宕机部署 + 生产排障 | `cmd/server/main.go:256-286` / `internal/middleware/middleware.go` | **零覆盖** |

---

## 1. 客户端加密（CSE）/ 零信任架构 — SSE-C + 端到端加密

### 当前状态

**当前仅支持服务端加密（SSE-S3 模式）。**

```go
// internal/storage/encrypt.go
// envelopeEncrypter — 服务端信封加密，master key 由 SecretProvider 管理
// 写入: 数据用随机 DEK 加密 → DEK 用 Master Key 包裹 → 信封存储为 sidecar
// 读取: 加载信封 → Resolve(Kid) → 解开 DEK → 解密数据
```

**支持的加密模式：**

| 模式 | 实现 | Master Key 来源 |
|------|------|---------------|
| SSE-S3（默认） | `envelopeEncrypter` | 环境变量 / Keyfile / HTTP KMS |
| SSE-KMS（远程） | `DataKeyWrapper` | 远程 KMS API（wrap/unwrap） |
| **SSE-C（客户提供密钥）** | ❌ **未实现** | 请求级客户密钥，不落盘 |

**AWS S3 标准定义的三种服务端加密：**

```
PUT /bucket/key HTTP/1.1
x-amz-server-side-encryption: AES256           → SSE-S3（服务端管理密钥）✅ 已实现
x-amz-server-side-encryption: aws:kms          → SSE-KMS（KMS 管理密钥）✅ 已实现
x-amz-server-side-encryption-customer-algorithm: AES256  → SSE-C ❌ 缺失
x-amz-server-side-encryption-customer-key: base64(32bytes)  → ❌ 缺失
x-amz-server-side-encryption-customer-key-MD5: md5hex       → ❌ 缺失
```

**为什么 SSE-C 是 S3 兼容性不可绕过的缺口：**

| 场景 | 需求 |
|------|------|
| 合规：金融/医疗 | 客户保留密钥控制权，服务商不可读 |
| 零信任部署 | 即使存储后端泄露，数据仍受客户密钥保护 |
| 密钥轮换策略 | 不同对象/不同版本使用不同客户密钥 |
| 跨组织共享 | 发送方加密，接收方用自己的密钥解密 |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/storage/encrypt.go` | `envelopeEncrypter` 服务端加密 | 无 SSE-C 的"直接使用请求密钥"路径 |
| `internal/storage/secret.go` | `SecretProvider` + `DataKeyWrapper` | 无"从请求上下文提取加密密钥"的机制 |
| `internal/storage/storage.go` | `PutOptions` / `GetOptions` | 无 `CustomerKey []byte` 字段 |
| `internal/storage/local.go` / `local_read.go` / `local_write.go` | PUT/GET 路径 | 不支持请求级密钥覆盖服务端密钥 |
| `internal/api/s3compat/handler.go` | S3 handler 解析 SSE-C headers | 解析后丢弃（无下游消费） |
| `internal/service/file_crud.go:Put` / `serveObjectContent` | FileService 写入/读取 | 不传递客户密钥参数 |
| `internal/service/file.go:FileService` | 服务层 | 无 `CustomerKey` 在请求上下文中的传播路径 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **SSE-C 密钥与服务端密钥互斥** | 对象用 SSE-C 写入，后续用服务端密钥尝试读取 | 服务端解密失败（误认为 envelope 加密） | 对象元数据标记加密模式（`SSE-C` vs `SSE-S3` vs `SSE-KMS`），读取时按模式选择解密路径 |
| **SSE-C 校验和失效** | 客户提供 `key-MD5` 与实际密钥不匹配 | 无校验，潜在静默错误 | PUT 请求时计算 key 的 MD5 并与 header 比对，不匹配返回 `400 InvalidArgument` |
| **预签名 URL + SSE-C** | 预签名 GET URL 不携带加密密钥 | 无法解密对象 | SSE-C 对象的预签名 URL 必须要求客户在请求时提供密钥（即 GET 仍需传 SSE-C headers） |
| **版本化桶中 SSE-C 密钥变更** | 对象 v1 用 key-A 加密，v2 用 key-B 加密 | 无版本级密钥追踪 | 每个版本独立记录使用的密钥标识（key fingerprint），读取时要求相应密钥 |
| **SSE-C + 范围请求** | `Range` + SSE-C 对象 | 需要解密完整对象再截取 | 解密完整块后取范围（或缓存全解密结果），性能退化需要文档说明 |
| **ETag 与 SSE-C** | SSE-C 对象的 ETag 不反映明文内容（S3 规范） | 当前 ETag 是加密内容的哈希 | SSE-C 对象的 ETag 应该是**加密后**内容的哈希（与 S3 行为一致），不得泄露明文的可比较信息 |
| **多协议 SSE-C 一致性** | REST PUT 不传 SSE-C headers，S3 GET 却传了 | REST 走服务端加密，S3 找不到 key | 跨协议一致：对象元数据声明的加密模式在所有协议上统一执行 |

### 架构方向

```
┌─ 元数据扩展 ───────────────────────────────────────────────────┐
│ Object 新增字段:                                                 │
│   EncryptionMode   string   // "SSE-S3" | "SSE-KMS" | "SSE-C"  │
│   CustomerKeyHash  string   // SSE-C: sha256(key) 用于去重校验   │
│                                                                  │
│ 对象写入时记录加密模式，读取时按模式选择解密路径。                   │
└────────────────────────────────────────────────────────────────┘

┌─ Storage 接口扩展 ──────────────────────────────────────────────│
│ PutOptions / GetOptions 新增:                                    │
│   CustomerAlgorithm string  // "AES256"                         │
│   CustomerKey      []byte  // 32-byte AES key                   │
│   CustomerKeyMD5   []byte  // key 的 MD5 校验                   │
│                                                                  │
│ local backend 的 Put 路径:                                        │
│   如果 CustomerKey != nil:                                        │
│     用 CustomerKey 作为直接 AES-GCM 密钥加密 body                 │
│     不生成 envelope（无存储 key 的 sidecar）                      │
│     写入 EncryptionMode = "SSE-C" 到元数据                        │
│   否则: 使用现有的服务端 envelopeEncrypter                         │
│                                                                  │
│ local backend 的 Get 路径:                                        │
│   如果 Object.EncryptionMode == "SSE-C":                          │
│     强制要求请求携带 CustomerKey                                 │
│     验证 sha256(CustomerKey) == Object.CustomerKeyHash           │
│     直接用 CustomerKey AES-GCM 解密                               │
│   否则: 使用现有的 envelope 解密路径                               │
└────────────────────────────────────────────────────────────────┘

┌─ S3 协议适配 ───────────────────────────────────────────────────│
│ S3 handler 当前已解析 SSE-C 请求头（但丢弃不做）：                  │
│   x-amz-server-side-encryption-customer-algorithm                │
│   x-amz-server-side-encryption-customer-key                     │
│   x-amz-server-side-encryption-customer-key-MD5                 │
│                                                                  │
│ 改动：将解析后的密钥传递到 FileService 的 CustomerKey 参数         │
│                                                                  │
│ 响应头也要补：                                                     │
│   x-amz-server-side-encryption-customer-algorithm（回显）         │
│   x-amz-server-side-encryption-customer-key-MD5（回显）          │
└────────────────────────────────────────────────────────────────┘

┌─ REST 协议适配 ────────────────────────────────────────────────│
│ 新增 REST headers（与 S3 语义对齐）:                               │
│   X-Aero-SSE-C-Algorithm: AES256                                │
│   X-Aero-SSE-C-Key: base64(32bytes)                             │
│   X-Aero-SSE-C-Key-MD5: md5hex                                  │
│                                                                  │
│ 注意：REST 和 S3 协议共用同一 FileService，所以密钥传递路径一致     │
└────────────────────────────────────────────────────────────────┘

**为什么现在做：** 这是 S3 兼容性最后一个未被系统讨论的加密模式。SSE-C 是 S3 对象存储的标配，对金融、医疗、零信任部署是不可或缺的准入级功能。当前代码已完整解析 SSE-C 请求头并丢弃——这是一个比缺失更危险的半成品状态（用户发送了密钥但未被使用，数据以服务端密钥加密存储，客户以为数据受自己密钥保护）。

| 影响面 | 工作量估计 |
|--------|-----------|
| Object 元数据模式字段（migration） | 低 |
| Storage 接口 CustomerKey 参数 | 低 |
| local backend SSE-C 加/解密路径 | 中 |
| S3 handler 传递密钥到 FileService | 低 |
| REST handler SSE-C headers | 低 |
| 预签名 URL SSE-C 文档约束 | 低 |
| 测试（SSE-C 写入/读取/范围/版本/跨协议） | 高 |

---

## 2. 对象级访问审计追踪

### 当前状态

**当前审计日志仅覆盖管理操作。**

```go
// internal/repository/audit.go
// 记录: 密钥添加/撤销、租户创建/删除/状态变更、配额设置、预算设置
// 不记录: 对象读取、对象写入、对象删除、列表操作、搜索操作
```

**代码证据：**

| 操作类型 | 当前审计 | 缺少的审计 |
|---------|---------|-----------|
| 密钥管理 | ✅ `AddKey` / `RevokeKey` | — |
| 租户管理 | ✅ `CreateTenant` / `DeleteTenant` / `SetStatus` / `SetQuota` / `SetBudget` | — |
| 对象读取（GET） | ❌ 无 | `actor → read object X at time T from IP Y` |
| 对象写入（PUT） | ❌ 无 | `actor → wrote object X (size, checksum) at time T` |
| 对象删除（DELETE） | ❌ 无 | `actor → deleted object X at time T` |
| 对象列表（LIST） | ❌ 无 | `actor → listed bucket X (prefix: "")` |
| 搜索操作 | ❌ 无 | `actor → searched "confidential" (mode: hybrid)` |
| 搜索点击 | ❌ 无 | `actor → opened result #3 : object X` |
| 预签名 URL 使用 | ❌ 无 | `presigned URL X → accessed object Y at time T` |
| 管理/非管理员操作分离 | ❌ 混合在同一个 `audit_log` 表 | 应分离为 `audit_log_admin` + `audit_log_access` |

### 为什么需要

**合规驱动：**

| 合规标准 | 要求 | 缺口的严重性 |
|---------|------|------------|
| SOC 2 | "系统对数据的访问需生成审计记录" | ❌ 无法满足 |
| PCI DSS 10.2 | "对所有访问 Cardholder Data 的操作记录审计追踪" | ❌ 无法满足 |
| HIPAA | "访问 PHI 的用户、时间、操作、IP 需记录" | ❌ 无法满足 |
| GDPR Art. 33 | "数据泄露后 72 小时内确定受影响的数据范围" | ❌ 无法确定谁访问了泄露的数据 |
| 金融行业 IT 审计 | "对象变更的完整可追溯性" | ❌ 无法提供 |

**运维驱动：**

| 场景 | 问题 | 影响 |
|------|------|------|
| 安全事件调查 | 发现异常数据导出 → 需要知道谁下载了什么 | 无审计日志 → 无法追溯 |
| 数据泄露定损 | 需要确定哪些对象被未授权访问 | 无审计日志 → 无法定损 |
| 内部滥用检测 | 员工下班后大量读取敏感数据 | 无审计日志 → 无法检测 |
| 容量规划 | 需要知道哪些对象被频繁访问（热/冷数据识别） | 无访问频率统计 → 只能基于修改时间 |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/repository/audit.go` | `LogAdminAction` 记录管理操作 | 无对象级的 `LogObjectAccess` |
| `internal/repository/repository.go` | `Repository` 接口 | 无 `InsertAccessAudit` / `QueryAccessAudit` 方法 |
| `internal/service/file_crud.go:Put` | 写入对象 | 无审计记录 |
| `internal/service/file_crud.go:serveObjectContent` | 读取对象（GET 路径） | 无审计记录 |
| `internal/service/file_crud.go:hardDeleteObject` | 硬删除 | 无审计记录 |
| `internal/service/file_features.go:ListObjects` | 列出对象 | 无审计记录 |
| `internal/service/file_features.go:SetTags` / `SetACL` | 变更对象元数据 | 无审计记录 |
| `internal/api/rest/search.go` | 搜索 API | 无审计记录 |
| `internal/auth/auth_middleware.go` | 请求中解析出 actor identity | 未传递给审计记录器 |
| `internal/middleware/middleware.go` | 中间件链 | 无审计中间件 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **高吞吐写入审计** | 每秒 1000 次 PUT，每次写一条审计日志 | 数据库写入成为瓶颈 | 异步审计写入（内存 buffer → 定时刷入 / 单独审计写入队列） |
| **审计日志量暴增** | DDoS 或爬虫读取数百万对象 | 审计表膨胀，影响主库查询性能 | 独立审计存储（单独的表/单独的数据库/单独的文件） |
| **审计日志完整性** | 审计日志被攻击者篡改或删除 | 当前无防护 | 审计日志使用**只追加**存储（append-only table），对已有行禁止 UPDATE/DELETE |
| **审计日志签名** | 法律纠纷需要审计记录作为证据 | 记录可被篡改 | 审计行使用 HMAC 链（前一行哈希包含在后一行中 → 形成 hash chain） |
| **审计日志保留** | 合规要求保留审计日志 7 年 | 无保留策略，无限增长 | 可配置保留期 + 自动归档到冷存储（保留原始 hash chain） |
| **预签名 URL 审计** | 用户分享预签名 URL，该 URL 被多次使用 | 无法追踪谁使用了预签名 URL | 预签名 URL 使用记录包括 source IP、User-Agent、时间戳 |
| **批量操作审计** | 批量删除 10000 个对象 | 产生 10000 条审计行 | 聚合审计：一条记录记录"批量删除 10000 个对象，范围：prefix: /archive/" |
| **审计跨多协议** | 同一对象通过 REST、S3、WebDAV 分别读取 | 审计格式不一致 | 统一审计事件格式，区分访问协议字段 |

### 架构方向

```
┌─ 对象级审计 ───────────────────────────────────────────────────│
│ 新增 internal/audit/ 包（与 internal/repository/audit.go 区分）  │
│                                                                  │
│ type AccessAuditEntry struct {                                   │
│     ID           int64     // 自增 ID                            │
│     Tenant       string    // 租户                                │
│     Actor        string    // 用户标识（key name / JWT sub / IP） │
│     Action       string    // "read" | "write" | "delete" |       │
│                            // "list" | "search" | "tag" | "acl"  │
│     Protocol     string    // "rest" | "s3" | "webdav" | "mcp"   │
│     Bucket       string    // 目标桶                               │
│     Key          string    // 目标对象 key（list/search 可为空）   │
│     SourceIP     string    // 请求来源 IP                          │
│     UserAgent    string    // User-Agent                          │
│     RequestID    string    // 关联请求 ID（与 http.request_id 对应）│
│     ObjectSize   int64     // 对象大小（PUT 时记录）               │
│     Success      bool      // 操作是否成功                         │
│     ErrorCode    string    // 失败时的错误码                      │
│     Timestamp    time.Time // ISO 8601                            │
│ }                                                                 │
│                                                                  │
│ 存储:                                                              │
│   SQLite: 独立表 `access_audit_log`（从 admin audit_log 分离）     │
│   Postgres: 独立表，可配置存储到独立数据库                          │
│   TTL: 可配置保留期，到期按分区删除（按月分区）                     │
│                                                                  │
│ 写入策略:                                                          │
│   同步写入（关键路径）: 每次对象读取/写入直接 INSERT                │
│     风险: 高吞吐路径增加请求延迟                                   │
│     优化: 异步批量写入（内存 channel → 每 1s 或每 1000 条刷入）    │
│                                                                  │
│ Hash Chain（可选，提升完整性）:                                     │
│   每条审计记录包含前一行的 SHA-256：                                │
│     row.Hash = SHA256(row.Timestamp + row.Action + row.Key +      │
│                 prevRow.Hash)                                     │
│   定期审计日志完整性校验工具                                        │
└────────────────────────────────────────────────────────────────┘

┌─ 集成到请求路径 ──────────────────────────────────────────────│
│ 方案 A: 中间件层（推荐）                                          │
│   新增 AuditMiddleware，位置在 TenantMiddleware 之后。             │
│   对于每个请求：                                                   │
│     1. 在请求处理前记录开始时间 + actor + action                  │
│     2. 包装 ResponseWriter 捕获状态码                              │
│     3. 在请求处理后异步写入审计日志                                │
│     优点: 零侵入 handler，统一适配所有协议                          │
│     缺点: 无法获取对象大小等业务级上下文                            │
│                                                                  │
│ 方案 B: Service 层（补充方案）                                     │
│   在 FileService 的关键方法中插入审计点：                           │
│     Put → audit("write")                                         │
│     serveObjectContent → audit("read")                             │
│     Delete → audit("delete")                                      │
│     ListObjects → audit("list")                                   │
│     优点: 可获取完整的业务上下文（对象大小、版本号等）               │
│     缺点: 需要在每个方法中插入代码                                  │
│                                                                  │
│ 推荐: 方案 A（中间件）做粗粒度审计 + 方案 B（Service）做细粒度审计  │
└────────────────────────────────────────────────────────────────┘

┌─ 管理员 API ───────────────────────────────────────────────────│
│ 对象级审计查询端点:                                               │
│   GET /v1/admin/audit/access?actor=&action=&bucket=&             │
│       key=&from=&to=&limit=&cursor=                              │
│                                                                  │
│ 审计统计端点:                                                     │
│   GET /v1/admin/audit/stats?from=&to=                             │
│     → { total_actions: 100000, top_actors: [...],                │
│         top_buckets: [...], read_write_ratio: 0.7 }              │
│                                                                  │
| 审计导出端点:                                                     |
|   POST /v1/admin/audit/export?from=&to=&format=csv               |
|     → 返回 CSV/JSON 格式的审计数据文件（异步 Job）                 |
└────────────────────────────────────────────────────────────────┘

**为什么现在做：** 对象级审计是合规必选项——没有它，任何金融、医疗、政务客户都无法通过安全评审。当前仅有管理操作审计的现状是"审计半成品"。

| 影响面 | 工作量估计 |
|--------|-----------|
| `access_audit_log` 表（migration） | 低 |
| AuditMiddleware | 低 |
| FileService 细粒度审计点 | 中 |
| 异步批量写入引擎 | 中 |
| 管理 API 审计查询 | 中 |
| 审计保留/GC | 低 |
| Hash chain（可选） | 中 |

---

## 3. 跨存储后端在线数据迁移

### 当前状态

**没有跨后端的数据移动机制。**

当前存储架构：

```go
// internal/storage/storage.go
type Storage interface {
    Put(ctx, key, ...) (ObjectInfo, error)
    Get(ctx, key) (io.ReadCloser, ObjectInfo, error)
    Delete(ctx, key) error
    // ...
}

// internal/storage/factory.go
// 启动时选择一个后端（local / s3 / oss / cos），运行期间不可变更。
```

**用户只能通过业务操作间接迁移：**
1. GET 对象 → 本地缓存 → PUT 到新后端 → DELETE 旧对象
2. 或写外部脚本循环所有对象

**问题：**

| 场景 | 当前做法 | 缺陷 |
|------|---------|------|
| 开发环境 local → 生产环境 S3 | 无工具 | 需要写一次性脚本，可能遗漏 |
| 从 S3 迁移到 OSS（云换供应商） | 无工具 | 可能数 TB 数据，手动并行化困难 |
| 存储成本优化：SSD → HDD 冷存储 | 无工具 | 无 tier-aware 迁移 |
| 存储后端故障：重建新的存储 | 无工具 | 必须离线恢复 |
| 多 region 部署：数据分布调整 | 无工具 | 只能全量复制 |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/storage/factory.go` | 一个后端，启动时固定 | 无运行时热切换能力 |
| `internal/storage/storage.go` | 单后端 Storage 接口 | 无多后端路由、无迁移状态 |
| `internal/jobs/jobs.go` | Job 队列调度 | 可复用但需迁移 Job 类型 |
| `internal/reconcile/` | 定时扫描 | 可复用但需迁移协调逻辑 |
| `internal/repository/sql_objects.go:UpsertObject` | storage_key 记录对象位置 | 单个 storage_key 字段 → 无法表达"迁移中"状态 |
| `internal/repository/repository.go:Object` | `StorageKey string` | 无 `StorageClass` / `StorageBackend` 字段 |
| `internal/service/file_crud.go:Get` | 按 storage_key 读取 | 无"从源读取→写入目标→更新 storage_key"的迁移路径 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **迁移中断后恢复** | 迁移到 80% 时进程崩溃 | 从头开始（无 checkpoint） | 持久化迁移进度 checkpoint（每完成 1000 个对象记录一次） |
| **迁移中对象同时被写入** | 用户在迁移窗口内 PUT 新对象 | 新对象写入源端，迁移不捕获它 | 迁移流程需要捕获增量变更（WAL 或双写） |
| **迁移中对象被删除** | 对象在迁移过程中被用户删除 | 迁移可能报错 404 | 跳过已删除对象（幂等处理） |
| **大文件迁移超时** | 10GB 文件从 local → S3（网络限速） | 单一 Job 超时失败 | 大文件分块迁移（复用 multipart upload）、每个块独立 Job |
| **迁移后一致性校验** | 10 万个对象迁移完成，需要确认 | 无校验机制 | 迁移后比对 ETag + size，不一致则重试 |
| **回滚方案** | 迁移到新 S3 后发现性能不达标 | 无法回滚（旧数据已被删除） | 保留期（迁移后 7 天内不删除源对象），提供回滚开关 |
| **跨后端版本兼容** | 不同 storage backend 的版本化语义不同 | local 支持独立版本，S3 版本化需桶级开启 | 迁移时必须同步版本信息（Object + Version rows） |
| **迁移速率控制** | 迁移 Job 吃掉所有带宽，影响在线服务 | 无节流 | 可配置速率限制（MB/s）、并发度、时间窗口（仅凌晨执行） |

### 架构方向

```
┌─ 迁移管理 ─────────────────────────────────────────────────────│
│ 新增 internal/storage/migration/ 包                              │
│                                                                  │
│ type MigrationPlan struct {                                      │
│     ID            string    // uuid                              │
│     SourceBackend string    // "local" | "s3" | "oss" | "cos"   │
│     TargetBackend string    // "local" | "s3" | "oss" | "cos"   │
│     Filter        MigrationFilter // 可选：按桶/前缀/标签过滤    │
│     RateLimit     int       // MB/s，0=不限                     │
│     RetentionDays int       // 迁移完成后保留源数据天数         │
│     VerifyChecksum bool     // 迁移后校验 ETag                   |
│     CreatedAt     time.Time                                     │
│     StartedAt     *time.Time                                    │
│     CompletedAt   *time.Time                                    │
│     Status        string    // "pending" | "running" |           │
│                             // "completed" | "failed" | "rolled_back" │
│     Progress      float64   // 0.0 ~ 1.0                        │
│ }                                                                 │
│                                                                  │
│ type MigrationFilter struct {                                    │
│     Buckets       []string   // 留空 = 所有桶                     │
│     Prefix        string     // 可选前缀过滤                     │
│     Tags          map[string]string // 可选标签过滤               │
│     StorageClass  string     // 可选存储类过滤                    │
│ }                                                                 │
│                                                                  │
│ 迁移 Job 注册:                                                    │
│   jobs.Registry["storage_migration"] = func(ctx, job) error {    │
│       // 1. 迭代匹配条件的所有对象（分批，每次 1000 个）          │
│       // 2. 对每个对象:                                          │
│       //    a. 从 SourceBackend GET                              │
│       //    b. PUT 到 TargetBackend                              │
│       //    c. 更新 Object.StorageKey → 新后端 key               │
│       //    d. 旧后端 Delete（仅在 RetentionDays 到期后）        │
│       // 3. 更新 MigrationPlan.Progress                          │
│   }                                                              │
│                                                                  │
│ 管理 API:                                                         │
│   POST   /v1/admin/migrations        → 创建迁移计划               │
│   GET    /v1/admin/migrations        → 列出所有迁移               │
│   GET    /v1/admin/migrations/{id}   → 迁移详情 (含进度)          │
│   POST   /v1/admin/migrations/{id}/start  → 开始迁移              │
│   POST   /v1/admin/migrations/{id}/pause  → 暂停迁移              │
│   POST   /v1/admin/migrations/{id}/rollback → 回滚（从目标删+恢复源）│
│   DELETE /v1/admin/migrations/{id}   → 清理迁移记录                │
│                                                                  │
│ 指标:                                                             |
│   migration_progress{migration_id}      // gauge: 0.0~1.0         |
│   migration_bytes_total{migration_id}   // counter                |
│   migration_objects_total{migration_id} // counter                |
│   migration_errors_total{migration_id, error_type} // counter     |
└────────────────────────────────────────────────────────────────┘

┌─ 增量迁移 ─────────────────────────────────────────────────────│
│ 全量迁移完成 ≠ 迁移完成（迁移期间可能有新写入）。                    │
│                                                                  │
│ 增量迁移方案:                                                      │
│   1. 全量迁移：遍历所有既有对象                                     │
│   2. 增量窗口：记录全量迁移开始时间 T0                                │
│   3. 增量迁移：迁移从 T0 之后发生变化的所有对象（由 events 表驱动）    │
│   4. 最终一致性：重复 2-3 直到增量窗口为空（catch-up complete）       │
│                                                                  │
│ 变更跟踪方式:                                                      │
│   方案 A: 使用 events 表（推荐）                                    │
│     events 表记录所有 object.created + object.deleted              │
│     迁移 Job 消费 T0 之后的事件                                    │
│     优点: 复用现有事件系统                                          │
│                                                                  │
│   方案 B: 使用 Object.updated_at 字段                              │
│     迁移 Job 定期扫描 updated_at > last_migrated_at 的对象          │
│     优点: 独立于事件系统，不依赖事件可靠性                            │
└────────────────────────────────────────────────────────────────┘

**为什么现在做：** 跨后端迁移是生产运维的基本需求——换存储供应商、调整存储策略、灾备恢复——没有迁移工具的系统在实际部署中会出现"存储绑定"（storage lock-in）问题。当前代码中多个 storage backend 已经就绪，唯一缺的是连接"从 A 到 B"的迁移管道。

| 影响面 | 工作量估计 |
|--------|-----------|
| Migration 表 + CRUD | 低 |
| 迁移 Job 实现（全量） | 中 |
| 增量迁移（事件驱动） | 中 |
| 管理 API | 中 |
| 速率控制 | 低 |
| 校验 + 回滚 | 中 |
| CLI 集成 | 低 |

---

## 4. API 版本治理与兼容性保障

### 当前状态

**当前只有一个 `/v1` 命名空间，没有版本治理框架。**

```go
// internal/api/rest/router.go
// 所有路由硬编码在 /v1 下
r.Route("/v1", func(r chi.Router) { ... })
```

**现状诊断：**

| 维度 | 当前状态 | 风险 |
|------|---------|------|
| 版本号 | 硬编码 `/v1` | 无 `/v2` 升级路径 |
| 版本协商 | 无 `Accept: application/vnd.aero.v2+json` | 无法按版本分发 |
| 废弃策略 | 无 `Sunset` header | 无法通知客户端废弃 |
| 废弃期限 | 无废弃时间表 | 突然移除 endpoint |
| 兼容性测试 | 只有单元测试 | 无面向 SDK 的契约测试 |
| OpenAPI 规范 | 有 `/openapi.json` | 无多版本规范 |
| SDK 版本管理 | Go/Python/JS SDK 用各自版本 | 无法关联 API 版本 |
| Changelog | 手写 `CHANGELOG.md` | 无自动化的 breaking change 检测 |
| 向后兼容 | 隐式（靠开发者自觉） | 容易被破坏 |

**为什么现在需要做：** 项目已有 Go/Python/JS 三个 SDK、MCP 工具集、OpenAPI 规范。随着功能加速扩展，breaking change 几乎不可避免。没有版本治理框架，以下问题会越来越严重：

1. SDK 开发者无法判断某个 endpoint 是否稳定
2. 用户在升级后遇到不可用的 endpoint 但没有协商机制降级
3. MCP 工具被客户端缓存，服务端变更后 client 崩溃
4. 管理员无法逐步迁移到新版本

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/api/rest/router.go` | 路由注册 `/v1/*` | 无版本分发中间件 |
| `internal/api/rest/openapi.go` | 生成 OpenAPI JSON | 只生成当前版本 |
| `internal/api/rest/handler.go` | handler 集合 | 无 `Deprecated` 标记、无版本兼容层 |
| `internal/api/rest/dto.go` | 请求/响应 DTO | 无版本化 DTO（v1 vs v2 不同字段） |
| `internal/api/s3compat/handler.go` | S3 兼容 handler | 无 S3 API 版本协商 |
| `sdk/go/aerovault/client.go` | Go SDK | 无版本感知的客户端 |
| `sdk/python/` | Python SDK | 同上 |
| `sdk/js/` | JS SDK | 同上 |
| `internal/mcp/server.go` | MCP 工具定义 | 无工具版本/废弃属性 |
| `docs/CHANGELOG.md` | 手写 changelog | 无自动化 breaking change 检测 |
| `internal/api/rest/router.go:routeRegistration` | `r.Route("/v1", ...)` | 不支持 `Accept` header 协商 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **SDK 使用过期版本** | 客户端仍调用废弃 6 个月的 endpoint | 正常返回，无提示 | 返回 `Warning: 299 aero-vault "GET /v1/files will be removed in 2027Q1"` header |
| **向后不兼容的字段变更** | 响应中移除 `object.url` 字段 | 直接移除，SDK 报错 | 新增字段用 `+`，移除字段用 `deprecated` 过渡 2 个版本 |
| **多版本共存** | 某些客户端用 v1，某些用 v2 | 只支持 v1 | `Accept: application/vnd.aero.v2+json` → v2 响应；默认 → v1 响应 |
| **废弃 endpoint 检测** | 管理员想了解正在使用的过期 API | 无监控 | `api_version_usage_total{version, endpoint, deprecated}` 指标 |
| **MCP 工具版本不匹配** | 老版 MCP client 调用新版 server | client 字段解析失败 | MCP 的工具元数据中加入 `version` 和 `deprecated` 标记 |
| **OpenAPI 多版本管理** | v1 和 v2 的 OpenAPI 规范不同 | 只有当前版本 | `GET /openapi.json?version=v1` / `/openapi.v2.json` |
| **S3 兼容性版本演进** | S3 API 的 `ListObjectsV2` 的响应增加新字段 | 老客户端忽略新字段（S3 规范允许） | 所有新增字段必须是可选的、扩展性的 |

### 架构方向

```
┌─ 版本治理策略 ─────────────────────────────────────────────────│
│ 定位: 不是马上做 /v2，而是搭建版本治理骨架                               │
│                                                                  │
│ 1. API 生命周期标记                                               │
│    每个 endpoint 标注生命周期阶段：                                 │
│      "stable"    → 主版本内承诺兼容                               │
│      "beta"      → 可能变更，提前通知                              │
│      "deprecated" → 将移除，返回 Sunset header                    │
│      "removed"   → 返回 410 Gone                                  │
│                                                                  │
│ 2. 废弃通知 header                                              │
│    废弃 endpoint 自动附加：                                       │
│     Sunset: Sat, 01 Jan 2027 00:00:00 GMT                        │
│     Deprecation: version="v1", sunset_date="2027-01-01"          │
│     Link: </v2/files/{key}>; rel="successor-version"             │
│                                                                  │
│ 3. 版本协商                                                       │
│    Accept: application/vnd.aero.v1+json → 返回 v1 响应            │
│    Accept: application/vnd.aero.v2+json → 返回 v2 响应            │
│    无 Accept header → 当前最新稳定版本                             │
│                                                                  │
│ 4. 兼容性承诺                                                     │
│    同一主版本内（/v1 → /v1）：                                    │
│      - 不移除或重命名字段                                         │
│      - 新字段必须可选（omitempty）                                │
│      - 不改变已有字段的类型或语义                                  │
│      - 不改变已有 endpoint 的 HTTP 方法、路径、状态码              │
│    主版本升级（/v1 → /v2）：                                      │
│      - 至少 6 个月并行期                                          │
│      - 并行期内 v1 返回 Warning header                            │
│      - SDK 必须更新 major version                                 │
└────────────────────────────────────────────────────────────────┘

┌─ 技术实现 ─────────────────────────────────────────────────────│
│ 版本协商中间件:                                                    │
│   func VersionMiddleware(next http.Handler) http.Handler           │
│   从 Accept header 提取版本号                                      │
│   注入 `context.WithValue(ctx, "api_version", "v2")`              │
│   路由层根据版本分发                                              │
│                                                                  │
│ 版本化 DTO:                                                        │
│   type FileResponseV1 struct { ... }                               │
│   type FileResponseV2 struct {                                     │
│       FileResponseV1       // 嵌入 v1 保证兼容                      │
│       NewField string \`json:"new_field,omitempty"\`              │
│   }                                                               │
│                                                                  │
│ 废弃中间件:                                                        │
│   路由注册时声明废弃信息:                                           │
│     r.Get("/v1/files/{key}", h.Get).WithDeprecation(               │
│         "v1", "2027-01-01", "/v2/files/{key}")                    │
│   → 自动附加 Sunset + Deprecation + Link headers                  │
│                                                                  │
│ 指标:                                                              │
│   api_version_usage_total{endpoint, version, deprecated}          │
│   → 帮助管理员了解何时可以安全移除废弃 endpoint                      │
└────────────────────────────────────────────────────────────────┘

┌─ 兼容性测试框架 ───────────────────────────────────────────────│
│ OpenAPI 差异检测:                                                  │
│   每次 CI 中对比当前 OpenAPI 规范与基线规范                          │
│   检测 breaking changes:                                           │
│     - removed endpoint                                            │
│     - removed field (required → 不存在)                           │
│     - changed field type                                          │
│     - added required field                                        │
│     - changed HTTP method                                         │
│     - changed response status code （2xx → 4xx/5xx）             │
│   结果写入 CI 注释 / 阻断 PR                                       │
│                                                                  │
│ SDK 契约测试:                                                      │
│   Go SDK 测试在 CI 中针对最新服务端运行                               │
│   Python SDK 测试同上                                              │
│   JS SDK 测试同上                                                  │
│   检测到 SDK 测试失败 → 自动标记对应 endpoint 为 "broken"              │
└────────────────────────────────────────────────────────────────┘

**为什么现在做：** API 版本治理是"迟早要做"的基础设施——越早搭建骨架，后续 breaking change 的成本越低。当前项目已有 3 个 SDK + MCP + OpenAPI，缺少版本协商就是在这几个生态组件之间埋下兼容性地雷。

| 影响面 | 工作量估计 |
|--------|-----------|
| 版本协商中间件 | 低 |
| 废弃 header 自动附加 | 低 |
| 生命周期标记（路由注册扩展） | 低 |
| API 版本指标 | 低 |
| OpenAPI 差异检测（CI） | 中 |
| SDK 契约测试（CI） | 中 |
| 版本化 DTO 模式指导文档 | 低 |

---

## 5. 优雅关闭与生产级部署韧性

### 当前状态

**基本关闭机制存在但非常粗糙。**

```go
// cmd/server/main.go:256-286
func runServer(...) error {
    // ...
    <-ctx.Done()
    logger.Info("shutdown requested")

    shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
    defer cancel()

    if err := srv.Shutdown(shutdownCtx); err != nil {
        return fmt.Errorf("shutdown: %w", err)
    }
    _ = shutdownOtel(shutdownCtx)
    // ...
}
```

**当前缺陷清单：**

| 缺陷 | 影响 | 严重性 |
|------|------|--------|
| **15s 硬编码超时** | 大型文件传输或长查询可能在关闭时中断 | 中 |
| **无连接排空（draining）** | 关闭瞬间新请求仍被接受（负载均衡器未收到 `not ready` 信号） | **高** |
| **无 In-Flight 请求追踪** | 关闭时不知道还有多少请求在处理中 | 中 |
| **无健康检查联动** | `/readyz` 在关闭时不返回 `503` → LB 会将请求路由到正在关闭的实例 | **高** |
| **Job/Worker 不排空** | Job 队列 worker 在关闭时被终止，正在处理的任务丢失 | **高** |
| **Webhook 不排空** | 正在发送的 webhook 请求被截断 | 中 |
| **SSE 连接不排空** | SSE 客户端连接被直接关闭，无 `event: shutdown` 通知 | 中 |
| **无关闭钩子注册机制** | 新增组件（如 KMS 客户端、AI embedder 连接池）无法注册关闭回调 | 中 |
| **无 graceful 与 force 分阶段** | 先 graceful（等待排空）→ 超时后 force（硬杀） | 中 |
| **无 readiness/liveness 状态机** | `readyz` 仅检查 DB 存活，不反映整体的 draining/stopping 状态 | **高** |

### 为什么需要

**零宕机部署（Zero-Downtime Deployment）的前提：**

```
健康检查状态机:    停止信号 → Ready=False → 等待LB排空 → 排空完成 → Shutdown
SIGTERM → 更改健康状态 → 等待连接排空 → 完成 in-flight → Shutdown
```

| 部署策略 | 依赖 | 当前支持 |
|---------|------|---------|
| Rolling update（Kubernetes） | Pod 在 `SIGTERM` 后返回 `NotReady` 直到排空完成 | ❌ |
| Blue-green deploy | 旧实例完全排空前新实例不可接管流量 | ❌ |
| Canary deploy | 实例按比例关闭/启动，不影响整体可用性 | ❌ |
| Auto-scaling down | 缩容时正在处理的请求不能中断 | ❌ |
| 计划内维护 | 声明维护窗口，排空所有连接后安全停机 | ❌ |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `cmd/server/main.go:256-286` | 基本 `srv.Shutdown` | 无多阶段关闭、无排空、无 readiness 联动 |
| `internal/middleware/middleware.go` | 中间件链 | 无 `ShutdownMiddleware` |
| `internal/middleware/middleware.go:healthzHandler` / `readyzHandler` | 健康检查 | `readyz` 不感知 draining |
| `internal/jobs/jobs.go:JobQueue` | Job 队列 | 无 worker 排空机制 |
| `internal/events/webhook.go` | Webhook 投递 | 无关闭时等待发完的回调 |
| `internal/events/bus.go` | 事件总线 | 无关闭排空 |
| `internal/api/rest/sse.go` | SSE 流 | 无 `event: shutdown` 通知 |
| `internal/api/s3compat/handler.go` | S3 handler | 大量 running requests 无法追踪 |
| `internal/service/file_crud.go:serveObjectContent` | 大文件流式响应 | 中断后客户端收到截断内容 |
| `internal/storage/encrypt.go` | KMS 客户端 | 无关闭时断开连接池 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **Kubernetes Pod 终止** | Pod 收到 SIGTERM → K8s 同时从 Service Endpoints 移除 | LB 可能仍有 30s 的缓存，新请求进入正在关闭的 Pod | 立即在 `/readyz` 返回 503 + 等待一定时间（如 5s）再真正关闭 |
| **大文件下载中断** | 用户在关闭前正在下载 5GB 文件 | 下载被 `srv.Shutdown` 截断 | 等待所有 in-flight GET 请求完成（设置最大等待时间）|
| **Job 执行中** | 关闭前 Job 正在处理对象拷贝 | Job 丢失，需要手动恢复 | Job 实现 `Shutdown()` 方法 → 保存 checkpoint → 下次启动恢复 |
| **内存索引未持久化** | BM25 内存索引在关闭时丢失 | 启动时重建（代价高） | 注册关闭回调：等待索引持久化或写 checkpoint |
| **SSE 客户端断线** | 1000 个 SSE 客户端连接在关闭时被断开 | 客户端收到 FIN 包，无上下文 | SSE 连接收到 `event: shutdown\ndata: {"reason":"maintenance"}\n\n` |
| **优雅关闭超时** | in-flight 请求超过等待时间 | 硬杀（当前：15s 强制 Shutdown） | 记录 metrics：`graceful_shutdown_killed_requests` + 日志列出被杀请求 |
| **双 SIGTERM** | K8s 发送 SIGTERM，15s 后发送 SIGKILL | 没有 SIGTERM 重入保护 | 忽略重复 SIGTERM，继续排空 |

### 架构方向

```
┌─ 关闭状态机 ───────────────────────────────────────────────────│
│ 新增 internal/server/ 包（管理服务器生命周期）                      │
│                                                                  │
│ type ShutdownState int                                            │
│ const (                                                            │
│     StateRunning   ShutdownState = iota  // 正常运行              │
│     StateDraining                         // 收到停止信号，排空中  │
│     StateStopping                         // 排空完成，正在关闭   │
│     StateStopped                          // 已关闭               │
│ )                                                                 │
│                                                                  │
│ type ShutdownManager struct {                                     │
│     state     atomic.Int32                                        │
│     waitGroup sync.WaitGroup   // 追踪 in-flight 请求              │
│     hooks     []ShutdownHook  // 注册的关闭回调                   │
│     timeout   time.Duration                                      │
│ }                                                                 │
│                                                                  │
│ func (sm *ShutdownManager) RegisterHook(name string,              │
│     fn func(context.Context) error, priority int)                  │
│     // 关闭时按优先级顺序执行回调                                   │
│     // 优先级: LB 排空(最高) > HTTP 排空 > Worker 排空 > ...        │
│                                                                  │
│ func (sm *ShutdownManager) Shutdown(ctx context.Context) error     │
│     // 1. state → Draining                                        │
│     // 2. 执行高优先级 hooks（健康检查置为 NotReady）                │
│     // 3. 等待 in-flight 请求完成（或超时）                         │
│     // 4. 执行中优先级 hooks（Job 保存 checkpoint）                 │
│     // 5. 执行低优先级 hooks（关闭连接池、释放资源）                  │
│     // 6. state → Stopped                                         │
└────────────────────────────────────────────────────────────────┘

┌─ 健康检查联动 ─────────────────────────────────────────────────│
│ 当前: readyz = DB 存活  →  true/false                            │
│ 改进: readyz 状态机:                                               │
│   Running  : 200 OK {"ok": true, "state": "running"}              │
│   Draining : 503 Service Unavailable {"ok": false, "state":       │
│              "draining", "reason": "shutdown", "drain_progress":  │
│              "45s remaining"}                                     │
│   Stopping : 503 {"ok": false, "state": "stopping"}               │
│   Stopped  : 503 {"ok": false, "state": "stopped"}               │
│                                                                  │
│ K8s 行为:                                                          │
│   Pod 收到 SIGTERM → readyz 立即返回 503                            │
│   K8s 从 Service Endpoints 移除该 Pod                              │
│   等待 K8s 传播（~5s）→ 开始真正的排空                               │
│   排空完成后调用 srv.Shutdown                                     │
└────────────────────────────────────────────────────────────────┘

┌─ In-Flight 请求追踪 ───────────────────────────────────────────│
│ 中间件包装:                                                        │
│   func TrackInFlight(next http.Handler) http.Handler               │
│       return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { │
│           shutdownManager.Add(1)                                    │
│           defer shutdownManager.Done()                              │
│           next.ServeHTTP(w, r)                                     │
│       })                                                           │
│                                                                  │
│ 关闭时:                                                             │
│   正在进行的请求记录到日志:                                          │
│     "draining: waiting for 12 in-flight requests:                  │
│      [GET /v1/files/large.bin (3.2GB/4.1GB elapsed 42s),          │
│       PUT /v1/files/data.csv (2.1MB/10MB elapsed 8s), ...]"      │
|                                                                   |
| 指标:                                                              |
|   server_in_flight_requests{method, path}  // gauge                |
|   server_shutdown_duration_ms              // histogram             |
|   server_shutdown_killed_requests          // counter （超时被杀）  |
└────────────────────────────────────────────────────────────────┘

┌─ Worker 排空 ──────────────────────────────────────────────────│
│ Job 队列 worker 注册 ShutdownHook:                                │
│   1. 停止从队列拉取新 Job                                         │
│   2. 等待当前正在执行的 Job 完成（每个 Job 允许最多 30s）            │
│   3. 在超时前未完成的 Job 标记为 "interrupted"（下次启动可恢复）     │
│                                                                  │
│ SSE 连接管理器注册 ShutdownHook:                                   │
│   1. 向所有活跃 SSE 客户端发送 event: shutdown                    │
│   2. 等待客户端收到（100ms）                                       │
|   3. 关闭 SSE 连接                                                |
|                                                                  |
| Webhook 投递器注册 ShutdownHook:                                   |
|   1. 停止投递新事件                                                |
|   2. 等待正在投递的 webhook 完成                                   |
|   3. 未完成的投递标记为 "delayed"（下次启动继续重试）               |
└────────────────────────────────────────────────────────────────┘

**为什么现在做：** 零宕机部署是生产环境的基本要求。当前的 15s 硬关闭 + 无 readiness 联动，意味着 K8s 滚动更新时**每次部署都会中断正在处理的请求**。随着项目规模增长，停机时间的影响从"几秒钟"变成"几个部署窗口×用户影响范围"。在生产部署之前修复这个基础设施债，成本远低于事后排查。

| 影响面 | 工作量估计 |
|--------|-----------|
| ShutdownManager 包 | 中 |
| 健康检查状态机改造 | 低 |
| In-Flight 请求追踪中间件 | 低 |
| Job Worker 排空 | 中 |
| SSE 排空 | 低 |
| Webhook 排空 | 低 |
| LB 延迟排空（readyz 503） | 低 |
| 指标 + 日志 | 低 |

---

## 总结：优先级矩阵

| # | 方向 | 业务价值 | 工程成本 | 依赖关系 | 推荐排序 |
|---|------|---------|---------|---------|---------|
| 1 | **客户端加密（CSE）/ 零信任架构** | ★★★★★（合规准入 + S3 兼容） | ★★（中，复用现有加密引擎） | 无 | **1** |
| 2 | **对象级访问审计追踪** | ★★★★★（合规必须） | ★★（中，独立表 + 中间件） | 无 | **2** |
| 3 | **跨存储后端在线数据迁移** | ★★★★（运维必需） | ★★★（中高，全量+增量迁移） | 无 | **4** |
| 4 | **API 版本治理与兼容性保障** | ★★★（长期健康） | ★（低，多为中间件+CI） | 无 | **3** |
| 5 | **优雅关闭与生产级部署韧性** | ★★★★（可靠性必须） | ★★（中，状态机+排空） | 无 | **5** |

**排序依据：**

1. **CSE 🥇** — 企业合规准入,S3 兼容性最后一块加密拼图。安全错觉风险（header 被解析但不生效）
2. **对象级审计 🥈** — SOC2/PCI/HIPAA 合规必须。没有它，受监管行业客户无法采购
3. **API 版本治理 🥉** — SDK 生态健康的基础设施。越早做，后续 breaking change 成本越低
4. **数据迁移** — 运维刚需但不是每日必须。复杂度和价值匹配，可以稍晚
5. **优雅关闭** — 可靠性提升但类 infrastructure 项目，短期价值不如上述产品级功能

---

*分析基于: 当前 HEAD | 代码行数 ~50K (Go) + SDK/UI/Infra | 前九期 expansion docs + ROADMAP + 全部 analysis docs 交叉审阅 | 确保第十期方向零重复*
