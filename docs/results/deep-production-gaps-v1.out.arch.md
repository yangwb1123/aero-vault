Now I have a thorough understanding of the codebase architecture, the middleware chain, the storage interface, and the gap analysis document. Let me compose the architectural analysis.

---

# Architectural Analysis: AeroVault Production Gap Remediation

## 1. 架构评估 (Architecture Assessment)

### 1.1 当前架构的核心优势

AeroVault 的架构在以下方面表现出色，构成了本分析的基础：

| 优势 | 代码证据 | 架构价值 |
|------|---------|---------|
| **单入口服务层** | 所有协议适配器 (REST/S3/WebDAV/MCP) 均调用 `FileService` | 一致性规则执行、避免协议层绕过 |
| **Storage 接口最小化** | `storage.go:76-113` 仅 13 个方法，后端可替换 | 低切换成本、contract test 可复用 |
| **租户第一设计** | `storageKey(tenant, bucket, key)` 三级前缀 + `X-Aero-Tenant` header | 多租户天然隔离，存储层无感知 |
| **事件驱动异步处理** | `EventBus` + `JobPool` 解耦写路径与索引/扫描/复制 | 写路径延迟不随后台负载增长 |
| **Opt-in 安全默认** | AI/pgvector/Qdrant/events 全部 flag-gated | 最小攻击面，基线路径零网络依赖 |
| **幂等 Key 基础设施** | `idempotency.go` Stripe 风格中间件已存在 | 为扩展覆盖范围提供可复用模式 |

### 1.2 架构局限性（结构性而非增量修复）

以下局限性无法通过单点修复解决，需要架构层面的演进：

**L1 — CORS 中间件位置与桶级上下文不可兼得**

当前 middleware 链顺序固定：
```
RequestID → CORS → Auth → Tenant → RateLimit → OTel → Recoverer → AccessLog
```

`CORS` 在 `Auth` 和 `Tenant` 之前运行，因此它无法：
- 从 request context 读取 `X-Aero-Tenant`
- 调用 `repo.GetBucketCORS()`（需要 tenant + bucket）
- 实现桶级 CORS 覆盖

这本质上是 **中间件拓扑顺序与数据依赖的冲突**。修复方向二（桶级 CORS）必然触及中间件链结构。

**L2 — 读路径没有契约性数据完整性保证**

`Storage.Get` 接口返回 `(io.ReadCloser, ObjectInfo, error)`，但没有契约要求实现者在返回前验证内容与 `ObjectInfo.ETag` 匹配。Scrub 模块（`reconcile/scrub.go`）是异步后台校验，存在检测窗口。这是 **接口契约缺口**：写路径有完整性保障（`md5WrapReader`），读路径没有对等方法。

**L3 — 公共 API 表面与内部能力不对称**

内部能力矩阵：
```
Repository 能力:  SetObjectMetaKey ✅   UpdateTags ✅   PutObjectACL ✅
公共 REST 端点:  /tags ✅  /acl ✅  /metadata ❌
```

`SetObjectMetaKey` 的 SQL 实现（`json_set`/`jsonb_set` 单个 key）在 `sql_objects.go:360-380` 已完整存在，唯一调用者是 `internal/reconcile/scrub.go:94`。这是 **API 债务** —— 内部能力已就绪，但未面向用户暴露。

**L4 — Idempotency 覆盖范围的架构边界不清晰**

当前 idempotency group 包含：
- `POST /files` / `PUT /files/*` / `DELETE /files/*`

不包含：
- `POST /multipart` (InitMultipart) — 合理，每次 Init 创建独立 upload session
- `PUT /multipart/{uploadID}/parts/{n}` (UploadPart) — 需要去重
- `POST /multipart/{uploadID}/complete` (CompleteMultipart) — **最需要幂等性**
- `DELETE /multipart/{uploadID}` (AbortMultipart) — 安全但缺乏一致覆盖

架构问题不在于"哪些没覆盖"，而在于**覆盖范围的标准未定义**。哪些操作应当获得幂等保护的判断标准是什么？是"产生副作用"（所有写操作）、"非原子写"（分阶段操作）、还是"用户感知的数据变更"（对象版本）？

### 1.3 技术债务评估

| 债务类型 | 位置 | 严重度 | 修复前提 |
|---------|------|--------|---------|
| CORS 配置"数据坟墓" | bucket cors CRUD 完整但运行时被忽略 | **高** — 用户产生虚假安全感 | 中间件链重构 |
| SetObjectMetaKey 未暴露 | repository 接口存在，仅 scrub 使用 | **中** — 功能缺口 | 新增路由 + 服务方法 |
| 多分片无幂等保护 | router.go idempotency group 外 | **高** — 数据一致性风险 | 复用现有 `IdempotencyHandler` |
| Scrub 与读路径冗余 | 独立实现完整性校验逻辑 | **低** — 可整合但需演进 | 统一的 checksum 验证层 |

---

## 2. 扩展方向 (Extension Directions)

### 方向 A：读路径数据完整性保护层  
**（修复缺口一，P1）**

#### 为什么需要

**业务价值**：AeroVault 定位为"安全的知识保险库"。如果用户不能确信读出的文件与当初存入的一致，整个产品的信任基础不牢固。SOC2/PCI-DSS/HIPAA 合规要求存储和传输完整性。

**技术价值**：当前架构对数据完整性是"写时验、读时盲"。修复后形成全路径闭环：写时计算 ETag/checksum → 持久化 → 读时验证 → 腐败时触发修复/告警。

#### 核心挑战

1. **性能权衡**：GB 级文件全量 MD5 计算的延迟不可接受。分层方案：
   - 小文件（≤16 MB）：全量 MD5 验证，延迟 ~数十毫秒
   - 大文件：按固定分片（如 8 MB）抽样验证，或使用快速哈希（xxHash）做完整性检查 + MD5 做最终验证
   - S3 multipart 上传的 ETag 是 `{md5_of_part_md5s}-{part_count}` 格式，非内容哈希，需独立存储 checksum

2. **后端适配差异**：
   - local FS：服务端计算 MD5，完全可控
   - S3：AWS 返回的 ETag 不保证是 MD5（multipart、SSE-C 时不同），需在 Put 时额外存储 `_aero_content_md5`
   - OSS/COS：各有自己的 ETag 规则

3. **降级策略**：验证失败时的行为选择：
   - 严格模式：返回 502/500（用户感知失败，触发重试）
   - 告警模式：记录 audit、标记 `_aero_scrub_status=corrupt`、返回数据（用户感知慢但可用）
   - 修复模式：尝试从 replica 读取（如配置了 replication）

#### 预期架构变更

```
当前:
  Storage.Get → 返回 Reader → 直接给客户端

方案 A（新增 ChecksumVerifier 接口）:
  type ChecksumVerifier interface {
      Verify(ctx, key, expectedMD5 string) error
  }
  Storage.Get → TEE Reader（计算 MD5）→ 与 Object.ETag 比较 → 通过返回 / 失败走降级

方案 B（Service 层包装）:
  FileService.Get → store.Get → md5VerifyReader → 客户端
  （纯 Service 层实现，不修改 Storage 接口）

方案 C（Storage 层可选实现）:
  Storage 接口增加可选方法 GetWithVerification()
  实现者决定是否支持
```

**推荐方案 B** — 最小入侵，Service 层包装 TEE reader，不影响 Storage 接口契约，与现有 `md5WrapReader` 对称。

#### 对现有系统的影响

- Storage 接口不变（向后兼容）
- 新增可选的验证策略配置（`STORAGE_VERIFY_READ=true`）
- Scrub 模块可复用同一验证逻辑（消除重复）
- 性能影响通过分层策略控制

---

### 方向 B：中间件链可组合化与桶级策略路由  
**（修复缺口二，P0-P1）**

#### 为什么需要

**业务价值**：桶级 CORS 是 S3 协议的标准能力。当前系统允许用户配置但运行时不执行，构成"功能幻觉"。多租户场景下，不同客户需要独立的跨域策略。

**技术价值**：当前中间件链的拓扑固定（CORS → Auth → Tenant），使任何"需要认证后上下文"的中间件逻辑无法插入合适位置。重构后可支持更丰富的桶级策略（如桶级 RateLimit、桶级 IP 白名单）。

#### 核心挑战

1. **中间件顺序重构**：CORS 需要提前处理 OPTIONS 预检（无需 tenant 上下文），但桶级 CORS 需要 tenant + bucket 上下文。解决方案：
   - **双阶段 CORS**：第一阶段（预检快路径）—— 全局 CORS 处理 OPTIONS，仅返回基础允许头。第二阶段（主请求覆盖）—— Tenant 中间件后，读取桶级 CORS 并覆盖响应头
   - **CORS 后移**：将 CORS 拆分为两段：在 RequestID 后立即处理 OPTIONS 预检（仅返回简单头），桶级 CORS 覆盖后移至 Tenant 之后

2. **缓存设计**：每个请求（不仅仅是 OPTIONS）都需要写 CORS 头。每次从 DB 读取桶级 CORS 不可接受。需要：
   - 桶级配置内存缓存（LRU + TTL）
   - 缓存失效：桶 CORS 更新时清除缓存（通过 bus 或直接）
   - 默认值：桶未配置时继承全局配置

3. **预检缓存**：OPTIONS 响应中的 `Access-Control-Max-Age` 应从桶级 CORS 规则取 `MaxAgeSeconds`，而非全局固定值。

#### 预期架构变更

```
当前:
  RequestID → CORS(全局) → Auth → Tenant → handler

方案 A（双阶段 CORS — 推荐）:
  RequestID → CORS_OPTIONS_PREFLIGHT(仅处理 OPTIONS+简单头) 
           → Auth → Tenant 
           → CORS_BUCKET_AWARE(读取桶级配置，覆盖响应头) 
           → handler

方案 B（CORS 后移 + 预检分离）:
  (顶级路由) RequestID → OPTIONS_HANDLER(单独处理预检)
  (子路由)   Auth → Tenant → CORS(桶级感知) → handler
```

**推荐方案 A**，因为它：
- 保持 OPTIONS 预检的快路径（无 DB 调用）
- 对主请求的 CORS 头写操作影响可忽略（缓存命中时无 DB 调用）
- 不与现有路由结构冲突

#### 对现有系统的影响

- `CORSConfig` 结构体需要扩展支持桶级覆盖模式
- 中间件链顺序变更，需验证所有现有 handler 的行为不受影响
- `BucketConfig.CORSRules` 数据字段由"数据坟墓"变为活跃配置
- S3 兼容性显著提升

---

### 方向 C：对象元数据统一管理 API  
**（修复缺口三，P1-P2）**

#### 为什么需要

**业务价值**：Tags 有完整 CRUD 端点，Metadata 没有。用户在上传后发现 metadata 拼写错误或需要补充构建信息（CI/CD pipeline 场景），只能重新上传整个对象。这是直接的用户体验断层。

**技术价值**：内部 `SetObjectMetaKey` 已完整实现（`repository/sql_objects.go:360-380`），基于 `json_set`/`jsonb_set` 支持单 key 增量更新。暴露为公共 API 的边际成本极低。

#### 核心挑战

1. **API 风格选择**：
   - **PATCH /files/{key}/metadata**：增量更新（只发变化的 key），与 `SetObjectMetaKey` 语义匹配
   - **PUT /files/{key}/metadata**：全量替换，幂等性强
   - **DELETE /files/{key}/metadata?key=someKey**：删除特定 key
   - 应同时支持三种，对标 AWS S3 没有直接对应的操作，但参照 REST 惯例

2. **大小限制校验**：更新后 metadata 总大小不能超过 64 KiB（`ErrMetadataTooLarge`）。增量更新需要：
   - 读取现有 metadata
   - 合并新 key/value
   - 计算合并后大小
   - 验证不超过限制
   - 写回

3. **并发安全**：并发 PATCH 同一对象的 metadata 可能丢失更新。可选方案：
   - 乐观锁：使用 ETag 或 `object_version` 做条件更新
   - 行级锁：事务内 SELECT ... FOR UPDATE
   - **推荐**：使用 SQL 的 `json_set`，在单个原子 UPDATE 中完成，数据库行锁天然保护

4. **版本控制兼容**：对于版本控制启用的 bucket，metadata 更新应作用于当前版本（与 `SetTags` 语义一致）。

#### 预期架构变更

```
新增 REST 路由:
  PATCH  /v1/files/{key}/metadata    → 增量更新特定 key
  PUT    /v1/files/{key}/metadata    → 全量替换 metadata
  DELETE /v1/files/{key}/metadata    → 删除特定 key 或清空

新增 FileService 方法:
  svc.PatchMetadata(ctx, tenant, bucket, key, metadata map[string]string) 
  svc.PutMetadata(ctx, tenant, bucket, key, metadata map[string]string)
  svc.DeleteMetadataKey(ctx, tenant, bucket, key, metaKey string)

Repository 层增强:
  BatchSetObjectMetaKey(ctx, tenant, bucket, key, metadata map[string]string)
  DeleteObjectMetaKey(ctx, tenant, bucket, key, metaKey string)
  ReplaceObjectMetadata(ctx, tenant, bucket, key, metadata map[string]string)
```

#### 对现有系统的影响

- 零存储层变更（纯元数据操作）
- 与 Tags 的操作模式对称，学习成本低
- 新增端点的 scope 和 audit log 应与其他写操作一致
- 不影响现有 PUT/GET/DELETE 路径

---

### 方向 D：多分片上传幂等性覆盖与操作原子性保证  
**（修复缺口四，P0）**

#### 为什么需要

**业务价值**：大型文件（GB 级）上传可能耗时数十分钟，客户端依赖重试机制。`CompleteMultipartUpload` 重试在非版本化 bucket 上可产生重复对象或覆盖正确版本。这是数据可靠性最严重的问题。

**技术价值**：Idempotency-Key 基础设施已存在且运行稳定。复用现有机制覆盖 multipart 是低风险、高回报的架构改进。

#### 核心挑战

1. **Key 选择**：
   - `CompleteMultipart`：`(tenant, uploadID)` — uploadID 天然唯一标识上传会话
   - `UploadPart`：`(tenant, uploadID, partNumber)` — 需要区分同一 upload 内不同分片
   - `AbortMultipart`：`(tenant, uploadID)` — 已 abort 的 upload 再次 abort 应返回 204
   - `InitMultipart`：不需要幂等（同一 `(bucket, key)` 并发 Init 是合法 S3 行为）

2. **CompleteMultipart 的特殊性**：
   - 成功后需持久化幂等记录（当前 `idempotency.go` 支持）
   - 重放应返回第一次成功时的 ETag 和对象信息
   - 需考虑：合并后对象存储 key 不可变（uploadID 是唯一的），重放不应重新合并

3. **UploadPart 的去重语义**：
   - AWS S3 保证同一 `(uploadID, partNumber)` 返回相同 ETag（当内容相同时）
   - 但如果客户端重试时发送了不同的内容，幂等 key 会拒绝更新 — 这是否符合预期？
   - **设计决策**：应拒绝（409 Conflict）— 同 key 不同 fingerprint

4. **与 S3 后端的交互**：
   - local backend：服务端合并，幂等性在服务端完全可控
   - S3 backend：`CompleteMultipartUpload` 是 AWS API 调用，AWS 不保证幂等。需要本地记录 `(uploadID, etag)` 对，重放时返回缓存的 ETag

#### 预期架构变更

```
router.go 变更:
  将 multipart 路由移入 idempotency group 或单独包裹:

  // 方案 A：加入现有 group
  r.Group(func(r chi.Router) {
      r.Use(idempotency(repo, logger, idemHashBody))
      r.Post("/files", h.PostForm)
      // ...
      r.Post("/multipart/{uploadID}/complete", h.CompleteMultipart)
      r.Put("/multipart/{uploadID}/parts/{n}", h.UploadPart)
      r.Delete("/multipart/{uploadID}", h.AbortMultipart)
  })

  // 方案 B：单独的 multipart 幂等 group（推荐）
  r.Group(func(r chi.Router) {
      r.Use(multipartIdempotency(repo, logger))
      r.Post("/multipart/{uploadID}/complete", ...)
      r.Put("/multipart/{uploadID}/parts/{n}", ...)
      r.Delete("/multipart/{uploadID}", ...)
  })

CompleteMultipart 实现变更:
  // 非幂等路径 → 
  1. 检查 uploadID 的幂等记录（已存在→返回缓存结果）
  2. 执行合并
  3. 写入对象行
  4. 保存幂等记录
  5. 返回结果
```

**推荐方案 B**，因为：
- 复用 `IdempotencyHandler` 核心逻辑
- 使用 `(tenant, uploadID)` 作为 key，不依赖 `Idempotency-Key` 请求头（S3 多分片上传不走标准 REST header）
- 可无缝支持 local 和 S3 backend

#### 对现有系统的影响

- `IdempotencyHandler` 需要提取为可复用组件（目前是 `idempotency()` 闭包，硬编码了 `extractIdempotencyKey` 逻辑）
- 不影响现有 GET/PUT/DELETE 路径
- S3 兼容性提升（客户端重试 CompleteMultipart 安全）
- 需考虑幂等记录的过期清理（TBD — 与现有 idempotency 清理策略一致）

---

### 方向 E：中间件链可组合架构（架构演进，超脱于单一缺口）  
**（P2 — 中远期架构演进）**

#### 为什么需要

以上缺口二的修复（桶级 CORS）暴露了一个更根本的问题：当前固定顺序的中间件链不支持"路由感知的优先级可配置性"。随着产品发展，更多桶级策略需要加入：桶级 RateLimit、桶级 IP 白名单、桶级 AES 密钥覆盖。如果每个策略都需要重新排列中间件链，系统将不可维护。

#### 核心挑战

1. **Pipeline 与可组合性的平衡**：Go 的 `http.Handler` 包装模式天然是线性 pipeline。要实现可组合的中间件链，可以引入：
   - **方案 A**：中间件注册表 + 条件执行（`if route.HasBucketConfig` then resolve after Tenant）
   - **方案 B**：内部子路由器 + 层次化中间件（顶层全局 → 路由组级 → handler 级）
   - **方案 C**：策略引擎（每个请求评估匹配的 bucket policy 列表，动态应用）

2. **性能考虑**：每个请求额外的 bucket policy 解析不能引入明显延迟。缓存和预编译策略是关键。

3. **向后兼容**：任何变更不得改变现有未配置特性的请求路径。

#### 对现有系统的影响

- 重大重构，需要详尽测试
- 与方向 B 的关系：方向 B 是方向 E 的子集和第一步
- 建议方向 E 不作为独立项目，而是作为方向 B 实现过程中的架构原则

---

## 3. 接口设计建议 (Interface Design Suggestions)

### 3.1 Storage 层的 Integrity Extension

**选项 A：可选接口（推荐）**

```go
// ChecksumVerifier is an optional extension to Storage.
// If a backend implements it, the service layer may call Verify
// to confirm content integrity on the read path.
type ChecksumVerifier interface {
    // Verify reads the content and computes the checksum using algorithm,
    // then compares it to expectedChecksum (hex-encoded).
    Verify(ctx context.Context, key, algorithm, expectedChecksum string) error
}
```

- 优：不破坏现有 Storage 接口，实现者选择加入
- 优：支持多算法（MD5、xxHash、CRC32C）
- 劣：服务层需要 type-assert 或接口探测

**选项 B：增强 Storage.Get 的 ObjectInfo**

```go
type ObjectInfo struct {
    // ... existing fields ...
    ContentChecksum string  // hex-encoded checksum stored at write time
    ChecksumAlgo    string  // "md5", "sha256", "crc32c"
}
```

- 优：接口不变，信息更丰富
- 劣：所有后端和调用点都需要适配

**选项 C：Service 层透明包装（推荐与 A 结合）**

FileService 内部实现 `verifiedGet` 方法，对 `store.Get` 返回的 reader 做 TEE 计算，比对后返回。与 `md5WrapReader` 对称。

### 3.2 CORS 中间件的桶级感知

**选项 A：CORSConfig 扩展为可分支**

```go
type CORSConfig struct {
    // Global defaults (from env)
    AllowedOrigins []string
    // ...
    
    // Optional bucket-level override provider
    BucketProvider BucketCORSProvider
}

type BucketCORSProvider interface {
    GetCORSRules(ctx context.Context, tenant, bucket string) ([]CORSRule, error)
}
```

- 优：中间件只需感知 `BucketProvider` 接口，不依赖 repository 具体实现
- 劣：每次请求增加接口调用（尽管可用缓存）

### 3.3 向后兼容性策略

| 变更 | 兼容策略 |
|------|---------|
| CORS 中间件重构 | 默认行为不变（桶未配置时回退全局策略）|
| 读路径验证 | 默认关闭（`STORAGE_VERIFY_READ=false`），开启后仅影响 GET/HEAD |
| Metadata API | 纯新增路由，不影响现有 PUT/GET DELETE |
| Multipart 幂等 | 内部变更，API 契约不变（新增幂等行为是超集）|

核心原则：**所有变更均 opt-in、向后兼容、默认行为不变。**

---

## 4. 技术选型 (Technology Selection)

### 4.1 无需引入新依赖

四个缺口均可通过现有技术栈解决：

| 方向 | 所需技术 | 来源 | 理由 |
|------|---------|------|------|
| 读路径验证 | `crypto/md5` + `io.TeeReader` | Go stdlib | 与现有的 `md5WrapReader` 对称 |
| 桶级 CORS | Redis 或无（内存 LRU） | Go stdlib `container/list` + `sync.RWMutex` | 桶级配置量小（一般 ≤ 1000 桶），DB 缓存已足够 |
| Metadata API | `json_set` SQL | 现有 SQLite/Postgres 能力 | `sql_objects.go:360-380` 已验证 |
| Multipart 幂等 | 复用 `IdempotencyHandler` | 内部已有 | `idempotency.go` 核心逻辑 |

### 4.2 可选增强技术评估

| 技术 | 场景 | 评估 | 自建 vs 引入 |
|------|------|------|-------------|
| xxHash（Go 实现 `cespare/xxhash`）| 大文件快速校验和 | 已广泛使用，纯 Go，无 CGO，Apache 2.0 | 引入，单依赖，低风险 |
| Ryzen 或 ARM CRC32 硬件指令 | 存储后端 checksum | Go `hash/crc32` 已支持硬件加速 | 使用 stdlib，无需新依赖 |
| 内存 KV 缓存（ristretto/hashicorp LRU）| 桶级 CORS 缓存 | 若 stdlib LRU 不够用 | **暂不需要**，当前配置量下 stdlib 足够 |

### 4.3 自建 vs 采购决策框架

这四个方向均为平台内部能力，无采购替代方案。但引入第三方验证工具链的选择：

```
是否需要端到端文件完整性审计？
├── 是 → 考虑: t9tio/gofake (checkpoint-based verifier)
├── 是 → 但更倾向于轻量 → 自建: 扩展 scub.go 并加入读路径验证
├── 否 → 自建 scub 扩展即可
```

结论：**全部自建**，因为：
- 所有方向的实现难度与现有代码耦合度低
- 无开箱即用的替代品能满足 AeroVault 的多协议、多后端架构
- 自建可完全控制性能特征和降级策略
- 额外依赖的论证成本高且收益不确定

---

## 5. 实施路线图 (Implementation Roadmap)

### 5.1 优先级排序

```
P0 — 数据一致性风险，可能产生静默数据损坏
P1 — 功能严重缺陷或合规风险
P2 — 用户体验提升或架构演进
```

| 优先级 | 方向 | 为什么 |
|--------|------|--------|
| **P0** | 方向 D：多分片上传幂等性覆盖 | 数据损坏风险最高。CompleteMultipart 重试可产生重复版本或覆盖正确版本，直接影响用户数据的正确性 |
| **P0** | 方向 A（第一阶段）：读路径基本验证 | 静默数据腐败可被用户感知且无法追溯。**第一阶段**仅对小文件做全量验证，大文件做抽样，配合告警 |
| **P1** | 方向 B：桶级 CORS 运行时生效 | 消除"功能幻觉"，S3 协议兼容性关键缺陷。安全审计虚检风险 |
| **P1** | 方向 C：Metadata API | 用户体验断裂，但无数据风险。可与 P0/P1 并行开发 |
| **P2** | 方向 E：中间件链可组合架构 | 超脱于单一缺口，支撑长期可扩展性。但非紧急 |
| **P2** | 方向 A（第二阶段）：分层验证策略 | 大文件分片验证、可配置降级策略、与 Scrub 对齐 |

### 5.2 阶段划分与里程碑

#### 阶段一：静默腐败防治 (P0, 预计 1-2 周)

**目标**：消除数据一致性最高风险。

| 工作项 | 产出 | 依赖 |
|-------|------|------|
| 1.1 multipart 路由移入/新建幂等 group | `router.go` 变更 | 无 |
| 1.2 提取 `IdempotencyHandler` 为可复用组件 | `idempotency.go` 重构 | 1.1 |
| 1.3 multipart 幂等 key 生成器（`(tenant, uploadID)`） | 新增 `multipart_idem.go` | 1.2 |
| 1.4 读路径 MD5 验证（service 层 TEE wrapper，默认关闭） | `file_crud.go` 扩展 | 无 |
| 1.5 验证失败告警 + `_aero_scrub_status=corrupt` 标记 | 降级路径 | 1.4 |

**里程碑 M1**：`make check` 全绿 + multipart 重试幂等验证通过 + 读路径验证可选启用。

#### 阶段二：桶级策略生效 (P1, 预计 2-3 周)

**目标**：桶级 CORS 从"数据坟墓"变为运行时活跃配置。

| 工作项 | 产出 | 依赖 |
|-------|------|------|
| 2.1 `BucketCORSProvider` 接口定义 + 缓存实现 | `internal/middleware/cors.go` | 无 |
| 2.2 双阶段 CORS 中间件（预检快路径 + 桶级覆盖） | `cors.go` 重构 | 2.1 |
| 2.3 中间件链顺序变更验证 | `main.go` + 集成测试 | 2.2 |
| 2.4 桶级 CORS 缓存失效（更新时清除） | 事件订阅 | 2.1 |

**里程碑 M2**：桶级 CORS 配置通过 REST/S3 写入后，OPTIONS 请求和跨域响应头反映桶级配置。全局策略作为兜底默认值。

#### 阶段三：元数据管理完整化 (P1, 预计 1-2 周，可与阶段二并行)

**目标**：Metadata 与 Tags 在 API 上对称。

| 工作项 | 产出 | 依赖 |
|-------|------|------|
| 3.1 `BatchSetObjectMetaKey` / `ReplaceObjectMetadata` Repo 方法 | `sql_objects.go` | 无 |
| 3.2 `FileService.PatchMetadata` / `PutMetadata` / `DeleteMetadataKey` | `file_features.go` | 3.1 |
| 3.3 REST 路由注册 + handler | `router.go` + handler | 3.2 |
| 3.4 大小限制校验（增量合并后校验） | `validateMetadata()` | 3.2 |

**里程碑 M3**：`PATCH /v1/files/{key}/metadata` / `PUT /v1/files/{key}/metadata` / `DELETE /v1/files/{key}/metadata?key=xxx` 全功能可用。

#### 阶段四：架构演进 (P2, 预计 3-4 周，中远期)

**目标**：中间件链可组合化，支持未来的桶级策略扩展。

| 工作项 | 产出 | 依赖 |
|-------|------|------|
| 4.1 现有中间件解耦为可注册组件 | `middleware/` 重构 | 阶段二经验 |
| 4.2 桶级策略评估引擎 | `internal/bucket/policy.go` | 4.1 |
| 4.3 读路径分层验证（大文件抽样 + 可选算法） | `checksum_verify.go` | 1.4 |
| 4.4 Scrub 与读路径验证逻辑统一 | `reconcile/scrub.go` | 4.3 |

**里程碑 M4**：新的中间件注册架构 ready，桶级策略可插拔。

### 5.3 风险点和缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| **R1** 中间件链顺序变更导致鉴权绕过 | 低 | **灾难性** | 全覆盖集成测试（`middleware_test.go`）+ 每个顺序变更加 `TestMiddlewareChainOrder` |
| **R2** 读路径 MD5 验证导致高延迟（大文件场景） | 中 | 高 | 分阶段部署：小文件全量→大文件抽样→可配置阈值→默认关闭 |
| **R3** multipart 幂等性与 S3 后端行为不一致 | 中 | 中 | 单独测试 S3 后端的 CompleteMultipart 幂等重放行为；幂等记录持久化在 repo 而非依赖后端 |
| **R4** 桶级 CORS 缓存与 DB 不一致 | 低 | 中 | TTL 抖动 + 写路径直接清除缓存 + degrade gracefully（回退全局策略）|
| **R5** Metadata 并发 PATCH 导致数据丢失 | 低 | 中 | 使用 `json_set` 原子更新 + 行级锁；文档建议客户端使用乐观锁或单一权威写入者 |
| **R6** Idempotency 记录膨胀（multipart 场景） | 中 | 低 | 设置 TTL（默认 24h）+ 定期清理 + 与现有 idempotency 清理机制对齐 |

### 5.4 验证与测试策略

每个阶段的 CI 验收标准：

```
# 阶段一验收
▶ go test ./internal/api/rest/ -run TestMultipartIdempotency
▶ go test ./internal/service/ -run TestReadPathVerification
▶ make check   # 全绿

# 阶段二验收
▶ go test ./internal/middleware/ -run TestCORSWitchBucketAware
▶ 手动验证: PUT /v1/buckets/b1/cors → OPTIONS /v1/files/b1/key → 响应头匹配桶级CORS
▶ go test ./internal/api/s3compat/ -run TestBucketCORS

# 阶段三验收
▶ go test ./internal/api/rest/ -run TestMetadataEndpoints
▶ go test ./internal/service/ -run TestMetadataPatch

# 阶段四验收
▶ go test ./internal/middleware/ -run TestMiddlewareRegistration
▶ benchmark: 读路径验证 vs 无验证 (小文件/大文件)
```

---

## 6. 关键设计决策总结

| 决策 | 选项 | 推荐 | 理由 |
|------|------|------|------|
| 读路径验证位置 | Service / Storage / Optional Interface | **Service 层 TEE wrapper** | 最小入侵，不影响 Storage 接口，与现有 `md5WrapReader` 对称 |
| 桶级 CORS 覆盖模式 | 双阶段 / CORS 后移 / 策略引擎 | **双阶段 CORS** | 保持 OPTIONS 预检快路径，主请求桶级覆盖 |
| Multipart 幂等组 | 加入现有 group / 独立 group | **独立 multipart 幂等组** | 使用 `(tenant, uploadID)` key，不依赖 Idempotency-Key 请求头，S3 兼容 |
| Metadata API 风格 | PATCH 增量 / PUT 全量 | **两者都支持** | PATCH 对应 `SetObjectMetaKey` 语义，PUT 对应全量替换，对标 REST 惯例 |
| 中间件链重构范围 | 最小化（仅 CORS）/ 全链路可组合 | **最小化优先** | 阶段二实现最小变更，阶段四再考虑全链路可组合。渐进式演进 |

---

## 总结

四个缺口本质上是同一个根本问题的不同表现：**系统的接口契约与运行时行为之间存在未覆盖的路径**。

- 缺口一（读路径验证）：Storage 接口有 `ETag` 字段但无读时验证契约
- 缺口二（CORS 持久化不执行）：Repository 接口有 `CORSRules` 但中间件不读取
- 缺口三（Metadata API 缺失）：Repository 接口有 `SetObjectMetaKey` 但无对应公共 API
- 缺口四（Multipart 无幂等）：Idempotency 中间件有实现但未覆盖 multipath

这四个缺口的修复路径高度独立，无强耦合，可以并行或串行实施。优先级由**对用户数据的风险等级**决定：幂等性缺口和读路径验证是 P0（数据损坏风险），桶级 CORS 和 Metadata API 是 P1（功能完整性问题而非数据风险）。
