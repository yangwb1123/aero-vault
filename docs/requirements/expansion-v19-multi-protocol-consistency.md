# AeroVault 高价值扩展方向（第十九期）

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全局代码扫描（300+ 源码文件），逐包审阅 `internal/` 全部子包、`cmd/server/main.go`、全部 SDK、Web UI、CLI、迁移文件、部署配置。逐一比对前十八期 expansion 文档（`expansion-directions.md ~ expansion-v18-adoption-gaps.md`，累积约 1.3MB 分析）、`ROADMAP.md`（10 方向）、`extensions.md`，确认每个方向在**既有文档中零覆盖**。
> **日期：** 2026-07-10  
> **原则：** 选取 5 个**架构纵深 / 平台基础设施 / 企业安全**方向——不是功能点的堆叠（前 18 期已覆盖约 90 个方向），而是**当前架构中影响生产就绪度的根本性缺失**。每个方向附带：代码锚点、当前状态、缺失能力、边界情况、架构概要、实现理由。**不编写任何实现代码。**

---

## 审阅背景：前十八期覆盖的去重矩阵

前十八期（v1–v18）已从 19 个视角覆盖约 90 个方向。以下大类已深度覆盖，**本期不再重复**：

| 领域 | 覆盖期数 | 方向数 |
|------|---------|--------|
| AI/RAG 管线（Embed/Search/Chat/Agent/Rerank/PII/Indexer/Cache/Lineage） | v1~v13, ROADMAP #1~#2 | ~12 |
| S3 兼容性（子资源/Batch/Multipart/ACL/Policy/CORS/Logging/Notification/Inventory/LegalHold） | v1, v4, v6, v8~v10, v16, v17, ROADMAP #7 | ~12 |
| 存储后端（S3/OSS/COS/KMS/SSE/Encryption/CircuitBreaker/Multi-Backend Routing） | v4~v15, ROADMAP #5 | ~8 |
| 认证授权（JWT/API Key/SigV4/OIDC/SAML/SCIM/MFA/Policy Engine/Key Isolation） | v1, v5, v8, v11, v12, v15, v16, v17 | ~8 |
| 多租户（CRUD/Quota/Budget/Audit/Governance/日费用/隔离） | v1, v3~v5, v7, v8, v11, v12 | ~7 |
| 事件/通知/Webhook/SSE/Bus/Transport/Filter/Multi-Destination | v1, v3~v6, v8, v9, v11, v12, v17 | ~8 |
| 复制/高可用/集群（CRR/SRR/HA/Active-Active/Federation/Cluster Singleton） | v1, v3~v5, v9, v17, ROADMAP #3, #10 | ~7 |
| 存储分层/生命周期转换/冷热数据（Glacier/IA/Transition/NoncurrentVersion/AbortMPU） | v1, v3, v5, v15, v17, ROADMAP #9 | ~6 |
| Reconcile/GC/Lifecycle/Orphan/Retention/Scrub/Version Governance | v1, v4, v6, v7, v15, ROADMAP #5, #8 | ~6 |
| 合规（WORM/Legal Hold/Retention/Client Encryption/Access Log Runtime/MFA Delete） | v2, v6, v8~v10, v12, v16, v17 | ~6 |
| 可观测性（OTel/Metrics/Grafana/Prometheus/SLO/Tracing/pprof/Debug） | v11, v13, v14 | ~4 |
| 工程质量（内存安全/并发/压缩/诊断/错误模型/Crash Recovery/PUT Crash Safety） | v11, v14, v15 | ~6 |
| Web UI / Admin Console 生产化 | v3, v6, v10, v11, v18 | ~5 |
| SDK 跨语言完整性 | v11, v18 | ~2 |
| 基础设施（配置热重载/IP ACL/内置 TLS/ACME/Feature Flag） | v16 | ~4 |
| 导入/迁移/批量操作工具 | v18 | ~1 |
| 插件/扩展/钩子系统 | v18 | ~1 |
| 性能基准与容量规划 | v18 | ~1 |
| CDN / 边缘交付 | v4, v13 | ~2 |
| 其他（API 治理/备份/优雅关闭/分享链接/Federation/Snapshot） | v2, v4, v8, v10, v11 | ~5 |

---

## 本期方向总览

| # | 方向 | 类型 | 影响评估 | 核心代码锚点 | 既有覆盖 |
|---|------|------|---------|-------------|---------|
| 1 | **🔴 四协议一致性语义与跨协议集成测试** | 架构/产品 | 多协议是核心差异化，但无一致性合约 — 行为差异静默影响用户 | `internal/service/file.go`（共享 FileService）+ `internal/webdav/dav.go` + `internal/api/s3compat/handler.go` + `internal/mcp/server.go` — 无一致性文档或契约 | **零覆盖** |
| 2 | **🔴 结构化元数据查询引擎（Faceted Search）** | 功能/平台 | 企业合规、数据治理、运营流程的基础设施；当前仅 RAG 文本搜索 | `internal/repository/sql_objects.go`（ListObjects 仅 prefix+marker）、`internal/repository/repository.go:SearchChunks`（仅 chunk 向量搜索） | **零覆盖** |
| 3 | **🟠 租户级加密密钥隔离与分层密钥体系** | 安全/架构 | 多租户 SaaS 安全基线；当前单全局密钥意味着单点泄露暴露所有租户 | `internal/storage/encrypt.go`（`envelopeEncrypter` 单实例）、`internal/storage/secret.go`（`SecretProvider` 无 tenant 路由） | **零覆盖** |
| 4 | **🟠 客户端上传会话韧性（Resumable Upload & Session Recovery）** | 可靠性/UX | 大文件上传在网络故障后无法恢复；客户端对进行中上传无可见性 | `internal/service/file_multipart.go`（无 `ListMultipartUploads`/`UploadPartCopy`）、`internal/reconcile/lifecycle.go`（无超时分片清理） | **零覆盖** |
| 5 | **🟡 存储后端运行状况监控与自动故障切换** | 运维/可靠性 | 单存储后端故障导致全局写入中断；Circuit Breaker 仅防级联，不触发切换 | `internal/storage/circuitbreaker.go`（单实例状态机）、`cmd/server/main.go:buildStorageFrom`（单一 storage.Storage 实例） | **零覆盖** |

---

## 1. 🔴 四协议一致性语义与跨协议集成测试

### 为什么需要它

aero-vault 的核心差异化在于**四套协议共享一个后端**。这是它相较于 MinIO（纯 S3）、Nextcloud（纯 WebDAV）、或是单一 REST 文件服务的根本优势。但这个优势没有伴随一个明确的一致性合约——即，**用户通过不同协议访问同一对象时，应期望什么行为？**

当前代码库中，FileService 是共享核心，但四个协议适配器对 FileService 的调用方式、参数传递、错误处理存在细微差异，而这些差异从未被系统化地文档化或测试过。

### 当前状态

通过代码扫描发现的跨协议不一致场景：

```go
// REST handler — 使用 DefaultBucket，版本 ID 在 query param ?version=
internal/api/rest/handler.go
  Put() → svc.Put(ctx, tenant, "default", key, ...)       // 硬编码 DefaultBucket
  Get() → 仅 ?version= 支持版本，无标准 Version-Id header

// S3 handler — 路径解析 bucket，版本 ID 在 x-amz-version-id header
internal/api/s3compat/handler.go
  putObject() → svc.Put(ctx, tenant, bucket, key, ...)    // 从路径解出 bucket
  getObject() → 响应 x-amz-version-id，请求 x-amz-version-id 作为版本选择

// WebDAV handler — 无 tenant 感知，无版本概念
internal/webdav/dav.go
  FileSystem.Open() → tenant 固定从 Header 取，fallback "default"
  PROPFIND 无版本元数据返回

// MCP server — "default" bucket，tenant 固定或从 header 取
internal/mcp/server.go
  listFiles() → svc.List(ctx, tenant, "default", ...)      // 硬编码 DefaultBucket
  readResource() → URI aero-vault://{tenant}/{bucket}/{key}
```

**已发现的协议间行为差异：**

| 场景 | REST | S3 | WebDAV | MCP | 差异影响 |
|------|------|-----|--------|-----|---------|
| Bucket 默认值 | `default` 硬编码 | 从路径解析 | `default` 硬编码 | `default` 硬编码 + URI 可指定 | S3 创建的对象在 `custom-bucket` 下，WebDAV 看不到 |
| 版本控制感知 | `?version=` querystring | `x-amz-version-id` header + `?versionId=` query | **无版本概念** | 无版本概念 | 版本化桶中，WebDAV/MCP 始终访问最新版本，用户无法察觉 |
| 对象锁/WORM | `/lock` 子路径 | `x-amz-object-lock-*` headers | **无锁概念** | 无锁概念 | WebDAV 无法感知锁定状态，可写入锁定对象 |
| 错误编码 | JSON `{"error":{"code":"..."}}` | XML `<Error><Code>...</Code></Error>` | HTTP 状态码 + 文本 | JSON-RPC `{"error":{"code":...,"message":...}}` | 客户端统一错误处理复杂化 |
| 认证方式 | Bearer JWT / X-Api-Key | SigV4 / X-Api-Key | X-Api-Key（若配置）| Bearer JWT（通过 HTTP） | 同一客户端需维护多种认证凭证 |
| 存储类表达 | `x-amz-storage-class` header | `x-amz-storage-class` header | **无存储类概念** | 无存储类概念 | WebDAV/MCP 写入对象始终为 STANDARD |
| ACL 表达 | JSON body | `x-amz-acl` / `x-amz-grant-*` headers | **无 ACL 概念** | 无 ACL 概念 | WebDAV 写入绕过 ACL 检查 |
| ETag 格式 | 带引号 `"abc"` 返回，不带引号请求 | 带引号 `"abc"` | 无 ETag 可见 | 无 ETag 可见 | 条件请求在 WebDAV/MCP 不可用 |
| Bucket 操作 | `/v1/buckets/{bucket}` | `/{bucket}` S3 风格 | 无 | 无 | WebDAV/MCP 不能管理桶 |

### 边界情况

| Edge Case | 场景 | 后果 |
|-----------|------|------|
| **S3 写入 → WebDAV 读取** | 使用版本控制 PUT 对象，WebDAV GET | WebDAV 获取最新版本，但用户不知道存在旧版本 |
| **REST 锁定 → S3 修改** | REST `/lock` 锁定对象后 S3 PUT 覆盖 | S3 handler 通过 FileService.checkLockBeforeOverwrite 防护 — 但 WebDAV 无此防护 |
| **WebDAV 写入 → S3 读取 ACL** | WebDAV 创建无 ACL 元数据的对象，S3 GET /?acl | 返回空 ACL（undefined），S3 客户端可能解析失败 |
| **MCP 工具写入 → REST 列表** | MCP write_file 用非默认 bucket，REST GET /v1/files 列 default bucket | 对象看起来"丢失" |
| **并发协议访问** | 同一对象同时被 REST PUT 和 WebDAV PROPFIND 访问 | 无读写一致性保证（当前的存储 + 元数据不是快照隔离的） |

### 建议的方向

1. **编写一致性合约文档**：明确定义每个协议的行为契约，包括版本语义、锁语义、错误映射、认证映射。这份文档应成为 cross-protocol 集成测试的规范。

2. **建立 Cross-Protocol Integration Test Suite**：在 `internal/integration/` 下增加跨协议场景的端到端测试。例如：
   - S3 PUT → REST GET → 验证数据一致性
   - REST PUT with lock → S3 PUT → 验证 412/423
   - WebDAV PROPFIND → REST GET /tags → 验证元数据透视
   - MCP write → S3 HEAD → 验证 ETag 一致

3. **共享协议语义层**：在 FileService 之上抽象一个 `ProtocolSemantic` 接口，将桶默认值、版本策略、锁行为等协议差异从各适配器提升到共享层。

4. **WebDAV 版本感知**：扩展 WebDAV 适配器以理解版本（例如通过 `?versionId=` X-AMZ 属性或在 PROPFIND 中暴露版本元数据）。

### 为什么这个方向优先级最高

因为**一致性是多协议产品的生命线**。用户选择 aero-vault 正是因为四协议互通。如果行为在不同协议之间存在细微差异，用户在迁移到生产后才会发现——那时修复成本极高。这是一项"先做对、再做全"的基础工程。

---

## 2. 🔴 结构化元数据查询引擎（Faceted Search）

### 为什么需要它

当前 aero-vault 的搜索能力完全围绕**文本 chunk 的向量/BM25 检索**，服务于 RAG Chat/Agent。但企业文件平台的日常使用中，**结构化元数据查询**的频率远高于语义搜索：

- "找到所有 2025 年 Q3 的发票 PDF，金额 > ¥10,000，状态 = '已审核'"
- "谁在什么时间修改了 `/contracts/` 目录下的文件？"
- "列出所有超过 30 天未访问的 .mp4 文件，大小 > 500MB"
- "统计每个部门的文件存储用量，按文件类型分组"

这些查询不需要 RAG，不需要向量嵌入，但它们是企业数据治理、合规审计、容量规划、运维巡检的**日常操作**——而当前系统完全无法支撑。

### 当前状态

```go
// internal/repository/sql_objects.go — 当前所有查询接口
func (r *sqlRepository) ListObjects(ctx, tenant, bucket, prefix, marker string, limit int) (ListPage, error) {
    // 仅支持 prefix + marker 分页，WHERE tenant=? AND bucket=? AND key LIKE prefix...
    // 无：日期范围、大小范围、标签过滤、内容类型过滤、元数据 kv 过滤
}

func (r *sqlRepository) ListObjectsByTag(ctx, tenant, bucket, prefix, marker string, limit int, tagKey, tagValue string) (ListPage, error) {
    // 有限：仅单标签 key（+ 可选 value），且过滤是 client-side（SELECT 全部后 Go 过滤）
}

// REST handler — 仅 `/v1/files?prefix=&marker=&limit=`
func (h *Handler) List(w, r) {
    // 无 filters 参数
}

// Web UI — 搜索面板仅用于语义搜索
// internal/webui/static/index.html — 仅 "semantic search" tab
```

**缺失的部分：**

| 能力 | 当前状态 | 需要什么 |
|------|---------|---------|
| 按大小过滤 | ❌ 完全缺失 | `GET /v1/files?min_size=1048576&max_size=1073741824` |
| 按日期过滤 | ❌ 完全缺失 | `GET /v1/files?before=2026-01-01&after=2025-01-01` |
| 按内容类型过滤 | ❌ 完全缺失 | `GET /v1/files?content_type=application/pdf` |
| 按标签组合过滤 | ⚠️ 仅单标签+client-side | `GET /v1/files?tag:env=prod&tag:dept=finance` |
| 按自定义元数据过滤 | ❌ 完全缺失 | `GET /v1/files?meta:project=alpha` |
| 按存储类过滤 | ❌ 完全缺失 | `GET /v1/files?storage_class=GLACIER` |
| 按删除状态过滤 | ⚠️ 仅 `?deleted=true` 独立端点 | 统一纳入 filter 参数 |
| SQL 排序支持 | ❌ 固定 `ORDER BY key` | `?sort=size&order=desc` |
| 聚合查询 | ❌ 完全缺失 | `GET /v1/files/stats?group_by=content_type` |
| 跨桶查询 | ❌ 固定单桶 | `GET /v1/files?bucket=*` |

### 边界情况

| Edge Case | 场景 | 后果 |
|-----------|------|------|
| **大量 tags** | 单个对象有 50 个 tag，filter 组合呈指数级 | SQL 查询爆炸，需要索引策略 |
| **元数据大小** | 用户元数据总上限 64KB，单 key 最长 256B | 模糊查询慢，需 `LIKE` 索引 |
| **分页+排序** | 10M 对象的第 5000 页按日期降序 | 传统 `OFFSET` 分页性能灾难，需 keyset pagination |
| **跨桶聚合** | 含 1000 桶的租户查询所有桶的总使用量 | 分布式聚合 vs 实时扫描的权衡 |
| **并发写入+查询** | 查询运行中对象被 PUT/DELETE | 需要快照隔离或最终一致性声明 |

### 建议的方向

1. **扩展 `GET /v1/files` 查询参数**：增加 `min_size`/`max_size`、`before`/`after`（updated_at 范围）、`content_type`、`storage_class`、`tag:` + `meta:` 前缀的参数体系。

2. **从 client-side 过滤改为 server-side SQL 过滤**：将 `ListObjectsByTag` 的 Go 层循环过滤下推到 SQL WHERE 子句。对 `tags` 和 `metadata` JSON 字段使用 SQLite JSON1 扩展或 Postgres JSONB 路径查询。

3. **增加聚合端点**：`GET /v1/files/stats?group_by=content_type,storage_class` 返回分组统计，支撑 Web UI 的使用量仪表板。

4. **引入对象级变更日志**：通过审计事件记录每个对象的关键变更（创建/修改/删除/ACL变更），使 "谁在何时做了什么" 可查询。

5. **Web UI 增加结构化查询面板**：在语义搜索之外，增加一个"元数据过滤"面板，支持日期选择器、大小滑块、标签组合选择。

### 为什么需要它

这是**从"AI 文件平台"到"企业文件管理平台"的缺失环节**。RAG 搜索找到"内容相似的文档"，但企业日常运营需要的是"找到符合特定条件的文档"。当前用户只能逐个协议地遍历前缀列表，或导出全量数据后用外部工具查询——这在文件数超过 1 万时完全不现实。

---

## 3. 🟠 租户级加密密钥隔离与分层密钥体系

### 为什么需要它

aero-vault 的 SSE（服务端加密）实现功能完整：支持单密钥、版本化密钥环、HTTP KMS 集成、密钥轮换重包装。但**所有租户共用同一套加密密钥**。

在单租户部署中，这不是问题。但多租户 SaaS 场景下，这意味着：
- 一个租户的加密密钥泄露 → 所有租户的数据全量解密
- 无法为合规需求（如 GDPR `right to explanation`、金融监管 `data segregation`）提供租户级加密证明
- 密钥轮换必须影响所有租户，不能按租户独立操作

### 当前状态

```go
// internal/storage/encrypt.go — 单实例 envelopeEncrypter
type LocalStorage struct {
    enc *envelopeEncrypter // nil 时无加密；单实例，所有对象共享同一加密配置
}

// internal/storage/secret.go — SecretProvider 无租户路由
type SecretProvider interface {
    PrimaryKey(ctx context.Context) ([]byte, error)
    KeyVersion(ctx context.Context, version int) ([]byte, error)
    AllVersions(ctx context.Context) ([]KeyVersion, error)
}
// 所有方法均无 tenant 参数

// internal/storage/rewrap.go — RewrapStale 扫描所有对象，用当前 primary key 重新包装
// 无租户感知，扫描全局

// cmd/server/main.go
store, _ := buildStorage(ctx, cfg) // 单一 storage.Storage 实例
// storage 实例要么有 SSE（全局密钥），要么没有
```

**密钥隔离的缺失层级：**

| 层级 | 当前状态 | 建议 |
|------|---------|------|
| **1. 系统级密钥** | ✅ 支持（SSEKey / SSEKeyfile / SSEKeyURL / KMS） | 保留为默认 |
| **2. 桶级密钥** | ❌ 完全缺失 | 每个桶可指定独立密钥 ID |
| **3. 租户级密钥** | ❌ 完全缺失 | 每个租户的存储密钥信封用租户专属 KEK 包装 |
| **4. 客户端密钥** | ❌ 完全缺失（S3 无 CSE 支持） | S3 CSE-C / CSE-KMS 模式 |

### 架构挑战

**这里的核心困难在于**：当前存储 key 的格式是 `path.Join(tenant, bucket, key)`，用一个全局 Key Envelope 加密后存在磁盘上。如果改为租户级加密，那么：

1. **Key Envelope 必须携带租户标识**：在 envelope 元数据中记录 `tenant_id`，使 Rewrap 组件能查找正确的租户密钥。
2. **读路径必须解析租户**：`LocalStorage.Get()` 读取对象时需要知道使用哪个密钥来解密 envelope。可以从存储 key 的前缀（`tenant/`）推断，但这要求解密引擎感知存储 key 结构——当前纯字节流的抽象层不支持。
3. **密钥缓存分层**：当前 `SecretProvider` 返回全局 primary key。租户级需要 `map[tenantID]KEK` 的 LRU 缓存 + 租户密钥的独立轮换状态机。
4. **KMS 调用成本**：每次写入/读取都需要用租户 KEK 来 wrap/unwrap DEK。如果 KEK 在远程 KMS 中，延迟会增加。需要 batched key caching。
5. **混合场景**：已加密对象用旧密钥，新对象用新密钥。系统必须同时支持多个活跃租户密钥，且 Rewrap 必须能区分。

### 边界情况

| Edge Case | 场景 | 后果 |
|-----------|------|------|
| **密钥轮换中的读取** | 租户密钥轮换期间，旧 envelope 尚未 Rewrap | 必须保留旧密钥直到 Rewrap 完成，否则数据永久丢失 |
| **租户删除后数据** | 租户被删除但数据仍存留（合规保留期） | 租户密钥被销毁后，保留数据不可读 — 需要"保留期密钥保留"策略 |
| **桶级与租户级密钥冲突** | 桶指定了独立密钥，同时租户级密钥也存在 | 优先级规则：桶级 > 租户级 > 系统级 |
| **跨租户复制** | 复制 worker 将租户 A 的数据复制到租户 B 的后端 | 数据需用租户 B 的密钥重新加密 |
| **共享桶（多租户读）** | 一个桶通过 ACL 授权多个租户读取 | 该桶的密钥必须对所有授权租户可访问 |

### 建议的方向

1. **扩展 `SecretProvider` 接口**：增加 `TenantKey(ctx, tenantID) ([]byte, error)` 和 `BuckeKey(ctx, tenantID, bucket) ([]byte, error)`。

2. **引入 `KeyResolver` 组件**：在加密层之前增加一个解析层，根据存储 key 或上下文确定用哪一组密钥。解析策略：`bucket级 > tenant级 > 系统级（默认）`。

3. **在 Envelope 元数据中记录密钥标识**：当前 envelope 只包含 `key_version`。增加 `tenant_id`、`key_id` 字段，使 Rewrap 能定位正确的封装密钥。

4. **管理 API**：`PUT /v1/admin/tenants/{tenant}/encryption-key` 用于设置/轮换租户密钥。`POST /v1/admin/tenants/{tenant}/rewrap` 触发单租户 Rewrap。

5. **Web UI 增加密钥管理面板**：显示每个租户的加密状态、上次轮换时间、未 Rewrap 的对象数。

### 为什么需要它

**多租户 SaaS 的安全基线**。在 SOC2 / ISO 27001 / GDPR 审计中，"租户数据是否使用独立密钥加密"是一个标准问题。没有这个能力，企业在合规敏感场景中无法采用 aero-vault。

---

## 4. 🟠 客户端上传会话韧性（Resumable Upload & Session Recovery）

### 为什么需要它

当前的分片上传（Multipart Upload）实现是完整的——可以 Init、UploadPart、Complete、Abort。但它在以下场景中存在重大可用性缺口：

1. **客户端网络中断**：上传了 95% 的分片后断连，恢复后必须重新开始整个上传。没有 S3 的 `ListParts` 机制（已有）的客户端恢复流程。
2. **大文件无断点续传**：单 PUT 上传 5GB 文件，中途断连 → 全部重传。没有 TUS 或 S3 的 `PUT with Content-Range` 断点续传支持。
3. **进行中上传无可见性**：客户端无法列出自己正在进行中的上传会话，也无法获取已完成分片的进度。
4. **服务端上传垃圾累积**：InitMultipart 之后从未 Complete 或 Abort 的上传在服务器端残留分片数据。当前 `reconcile/lifecycle.go` **没有清理超时分片上传的逻辑**。

### 当前状态

```go
// internal/service/file_multipart.go
func (s *FileService) InitMultipart(ctx, tenant, bucket, key string, opts PutOptions) (repository.Upload, error) {
    // ✓ 创建上传会话
}

func (s *FileService) UploadPart(ctx, uploadID string, partNumber int32, r io.Reader, size int64) (PartRecord, error) {
    // ✓ 上传单分片
}

func (s *FileService) CompleteMultipart(ctx, uploadID string) (repository.Object, error) {
    // ✓ 合并分片
}

func (s *FileService) AbortMultipart(ctx, uploadID string) error {
    // ✓ 放弃会话——但客户端必须知道 uploadID 才能调用
}

// 缺失的方法：
// ❌ ListMultipartUploads — 客户端无法列出自己的上传
// ❌ UploadPartCopy — 服务端拷贝作为分片（S3 标准功能）
// ❌ ResumeMultipart — 检查上传状态并返回待上传分片列表
// ❌ GC: 超时未完成的分片上传清理（>7天未活动）

// REST handler — 路由存在但缺少 ListUploads
// internal/api/rest/router.go
r.Post("/multipart", h.InitMultipart)          // ✓ Init
r.Put("/multipart/{uploadID}/parts/{n}", ...)  // ✓ UploadPart
r.Post("/multipart/{uploadID}/complete", ...)   // ✓ Complete
r.Delete("/multipart/{uploadID}", ...)          // ✓ Abort
// ❌ GET /multipart — 无 ListUploads
// ❌ PUT /multipart/{uploadID}/parts/{n}?copy=... — 无 UploadPartCopy
```

### 边界情况

| Edge Case | 场景 | 后果 |
|-----------|------|------|
| **上传超时** | InitMultipart 后 7 天未 Complete | 分片数据永久占用存储（无 GC） |
| **部分分片上传后断连** | 上传 50/100 个分片后客户端崩溃 | 用户不知 uploadID，无法 Abort，也无法恢复 |
| **并发完成** | 两个 CompleteMultipart 同时调用 | 加锁或幂等性保障？当前无锁 |
| **分片大小不一致** | 最后一个分片 < 5MB（S3 规则） | CompleteMultipart 返回错误，分片残留 |
| **存储后端迁移** | 上传进行中后端从 local 切换到 S3 | 分片分属不同后端，Complete 失败 |
| **上传期间对象版本变更** | InitMultipart 时版本关闭，Complete 时版本开启 | StorageKey 不一致 — 当前 CompleteMultipart 未重建 version-aware key |

### 建议的方向

1. **增加 `GET /v1/multipart` 端点**：列出进行中的上传，支持 `?prefix=&max-uploads=&key-marker=&upload-id-marker=` 参数（S3 ListMultipartUploads 兼容）。

2. **实现 S3 `UploadPartCopy`**：允许从已有对象拷贝作为分片，减少大文件复制时的网络传输。

3. **在 Web UI 中添加上传管理面板**：显示进行中的上传、每上传的已上传分片数/预期总大小、允许用户取消或恢复上传。

4. **实现上传超时 GC**：在 `reconcile/lifecycle.go` 中增加 `AbortIncompleteMultipartUpload` 规则。超过 N 天（S3 默认 7 天）未活动的上传自动 Abort。

5. **增加客户端恢复指南**：在 SDK 中增加辅助方法，传入 uploadID 后自动查询 `ListParts`，返回待上传分片编号列表，使客户端能实现"断点续传"。

6. **分片上传版本一致性**：CompleteMultipart 时检查当前桶版本配置与 Init 时是否一致，如果不一致则重建 StorageKey。

### 为什么需要它

**大文件上传是文件平台的日常操作**，而网络故障是常态。在一个部署了 aero-vault 的团队中，每周可能发生数十次上传中断。没有恢复机制，用户要么承受重复上传的带宽浪费，要么转向 rsync/scp——绕过了平台本身。同时，缺少 GC 意味着"被遗忘的上传"会不可见地消耗存储资源。

---

## 5. 🟡 存储后端运行状况监控与自动故障切换

### 为什么需要它

当前 aero-vault 的存储架构是**单一后端实例**：启动时根据配置选择一个后端（local/S3/OSS/COS），之后所有读写都经过这个后端。Circuit Breaker 可以防止级联故障（后端挂了 → 快速失败而非挂起），但**不能把流量切换到另一个后端**。

这意味着：
- S3 后端不可用 → 整个平台不可写（所有 PUT 429/503）
- 存储维护窗口 → 需要完全停服
- 无法实现"优先写入本地，异步复制到云端"的混合架构
- 无法在运行时在不中断服务的情况下迁移数据到新存储后端

### 当前状态

```go
// cmd/server/main.go — 单一存储实例
func buildStorageFrom(ctx, cfg) (storage.Storage, error) {
    // 返回 1 个 storage.Storage 实例
    // switch fc.Kind { case BackendLocal: return newLocalStorage(...)
    //                  case BackendS3:    return newS3Storage(...) }
}

// internal/service/file.go — 单一存储引用
type FileService struct {
    store storage.Storage // 一个实例
    repo  repository.Repository
}

// service 的所有方法都直接调用 s.store.Put/Get/Delete...

// internal/storage/circuitbreaker.go — 单实例状态机
type circuitBreaker struct {
    state int32 // closed / open / half-open
    // 失败次数、恢复超时等
    // 但都是针对单个后端实例
}
```

**缺失的架构组件：**

| 组件 | 当前 | 需要 |
|------|------|------|
| 后端注册表 | ❌ 无 | `map[backendName]storage.Storage` 支持多后端实例 |
| 健康探测 | ❌ 无 | 定期向后端发送 `HeadBucket` / `Stat(@health)` |
| 路由策略 | ❌ 固定单路由 | `primary → secondary` 故障切换 / `weighted` 负载均衡 |
| 降级模式 | ⚠️ Circuit Breaker 仅 fail-fast | CB 打开后自动切换到健康后端 |
| 静默恢复检测 | ❌ 无 | 定期探测已恢复的后端，自动切回 |
| 运维 API | ❌ 无 | `GET /admin/storage/health` / `POST /admin/storage/failover` |

### 架构挑战

1. **数据一致性**：如果主后端写入成功但元数据写入失败，故障切换到备用后端后，备用端没有该数据。需要 Reconcile 组件最终补齐。

2. **写后读一致性**：写入主后端后立即故障切换，新请求发送到备用后端 → 读不到刚写入的数据。需要"读取本地、写入全局"的策略或用写入时复制（Write-Through）。

3. **存储 Key 兼容性**：不同后端的 key 格式或限制可能不同（local FS 无限制，S3 有 1024 字符限制，某些云有特殊字符限制）。统一存储 Key 可能在某个后端上非法。

4. **List 操作的语义**：主后端有 1000 个对象，备用后端有 800 个（异步复制滞后）。`List` 应该返回哪组结果？Union？Intersection？主后端优先？

5. **Multipart 跨后端**：在主后端 InitMultipart，故障切换后在备用后端 UploadPart → 不一致。需要 session 级别的后端绑定。

### 建议的方向

1. **引入 `StorageRegistry`**：持有 `map[string]storage.Storage`，支持按名称添加/移除后端实例。FileService 通过 registry 访问，而非直接持有单实例。

2. **实现 `HealthCheckProbe`**：每个后端启动一个 goroutine 定期探测健康状态（`HeadBucket` 或 `Stat(@health/probe)`）。状态汇总到 registry 的路由表。

3. **实现路由策略**：
   - **Primary-Failover**（初始阶段）：请求发往 primary；CB 打开 → 自动切换到 failover；定期探测 primary，恢复后切回。
   - **Weighted**（进阶）：按权重分配读写到多个后端，用于存储迁移或灰度上线新后端。

4. **增加运维端点和 CLI**：
   - `GET /v1/admin/storage/backends` — 列出所有注册后端及其健康状态
   - `POST /v1/admin/storage/failover` — 手动触发主备切换
   - `POST /v1/admin/storage/backends` — 运行时注册新后端（配合数据迁移）
   - `aero-vault storage migrate --from local --to s3` — 全量数据迁移工具

5. **结合 Replication Worker**：将现有异步复制重构为"写主 → 异步复制到从"的模式。故障切换时，从端晋升为主端，原主端恢复后降为从端。

### 为什么需要它

**单存储后端是生产环境中最大的单点故障。** 即便应用层可以水平扩展（多副本），存储后端的故障仍然意味着整个平台的不可用。Circuit Breaker 只是让系统"优雅地失败"，而自动故障切换让系统"继续工作"——这是从可用性 99.9% 到 99.99% 的跨越。

---

## 实现优先级建议

| # | 方向 | 预估工作量 | 风险 | 影响面 | 建议顺序 |
|---|------|-----------|------|--------|---------|
| 1 | 四协议一致性语义 | M（2–3 周）| 低 | 架构/产品 | **第 1 批** |
| 2 | 结构化元数据查询引擎 | L（4–6 周）| 中 | 功能/平台 | **第 2 批** |
| 3 | 租户级加密密钥隔离 | L（4–6 周）| 高（数据安全） | 安全/合规 | **第 2 批**（与 2 并行） |
| 4 | 客户端上传会话韧性 | M（2–3 周）| 低 | 可靠性/UX | **第 1 批** |
| 5 | 存储后端自动故障切换 | XL（6–10 周）| 高（架构变更） | 运维/可靠性 | **第 3 批** |

**说明：**
- **第 1 批**：高影响、低风险、可快速见效的工程改进。协议一致性提升用户体验，上传韧性降低日常摩擦。
- **第 2 批**：高影响但需要较深设计的工作。元数据查询和密钥隔离在架构上是正交的，可以并行推进。
- **第 3 批**：影响最大但工程量也最大的方向。存储多后端需要较长时间的架构演进和充分测试，适合在平台稳定后投入。

---

*此文档基于对 aero-vault 代码库（v0.1.0）的全局扫描完成。所有代码引用均在 `/home/u1/aero-vault/internal/` 下可查。*
