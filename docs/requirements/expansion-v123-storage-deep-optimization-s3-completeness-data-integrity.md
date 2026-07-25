# 高价值扩展方向：存储层深度优化、S3 协议完备性与数据完整性增强

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — `cmd/server/main.go` + `internal/` 全部子包（237 个 Go 源文件, ~47K 行），3 套 SDK（Go/Python/JS），MCP 双模式（HTTP+stdio），Web UI，50 对迁移文件，`deploy/` 全套配置（Helm/Grafana/Prometheus/OTel），`internal/storage/` 全部 7 个后端实现  
> **去重验证：** 对 `docs/requirements/` 下全部 109 份既有分析文档进行关键词正则 + 代码锚点交叉验证  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。选取**代码中存在明确实现锚点、有实质性生产运营/产品影响、且在 109 轮分析中未被独立深度覆盖**的方向。每个方向包含：产品价值 → 现状与代码证据 → 架构权衡与建议方案 → 边界情况。

---

## 方法论：面向存储深度的代码缺口扫描

本次分析不聚焦"缺失哪些大功能"（如 TLS、WebSocket、异步 API——这些已在 v108 中覆盖），而是聚焦**已有代码中存在但管线断裂或严重低效**的高价值方向。

筛选标准：

| 条件 | 说明 |
|------|------|
| **代码中存在明确锚点** | 有接口、类型、配置字段或数据模型定义，但管线断裂或实现缺失 |
| **生产影响可量化** | 缺失去导致：性能瓶颈、数据完整性风险、合规障碍、运维盲区、成本浪费 |
| **跨 109 份分析未深度覆盖** | 前 109 份文档中无独立架构方案、无代码级分析 |
| **实现可独立推进** | 不依赖外部服务或重大架构变更，可在当前抽象层内完成 |

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码证据 |
|---|------|------|--------|---------|---------|
| **1** | **存储层零拷贝服务端复制（Server-Side Copy Offload）** | 性能/架构 | **P0** | 所有对象复制（S3 CopyObject）均通过 Go 进程内存中转：`Get()` → `io.ReadAll` → `Put()`。对于 GB 级对象及云存储后端（S3/OSS/COS）这是灾难性的资源浪费 | `internal/storage/storage.go:30-85` — `Storage` 接口**无 `Copy` 方法**；`internal/api/s3compat/extra.go:39-65` — `copyObject` 实现为 `svc.Get()` + `svc.Put()` |
| **2** | **对象锁 Governance / Compliance 双模式与 WORM 完备性** | 合规/产品 | **P1** | `LockedUntil` 仅是一个时间戳，缺少 S3 Object Lock 的 Governance（可绕过）和 Compliance（不可绕过）模式区分。无法满足金融/医疗/合规场景 | `internal/repository/repository.go:49` — `LockedUntil *time.Time`；`internal/service/file_crud.go:221-230` — `checkLockBeforeOverwrite` 仅检查时间，不检查模式 |
| **3** | **现代对象校验和算法（CRC32/CRC32C/SHA256）** | 数据完整性 | **P1** | 仅支持 `Content-MD5`。AWS S3 已标准化 CRC32/CRC32C/SHA256 作为一等校验和算法，支持 `x-amz-checksum-*` 请求头和尾部。缺少这些算法导致大数据传输的数据完整性保障不足 | `internal/api/s3compat/handler.go:695` — 仅有 `x-amz-checksum-md5`；`internal/service/file_crud.go:58-73` — `md5WrapReader` 仅支持 MD5；全库 `grep -rn "CRC32\|CRC32C\|SHA256\|x-amz-checksum-crc"` → **零命中** |
| **4** | **S3 请求级加密头（SSE-S3/SSE-KMS/SSE-C）** | 协议兼容/安全 | **P1** | 现有 SSE 是服务器全局配置，不支持 `x-amz-server-side-encryption` 请求头（AES256/aws:kms）和 `x-amz-server-side-encryption-customer-*`（客户端密钥）。标准 S3 SDK 客户端期望这些头 | `internal/api/s3compat/handler.go` — 全文件 `grep "x-amz-server-side-encryption"` → **零命中**；`internal/storage/encrypt.go` — AES-GCM 加密存在但仅通过服务器配置触发 |
| **5** | **存储类生命周期自动转换（Lifecycle Transition）** | 成本/架构 | **P2** | 生命周期规则仅支持 `soft_delete` / `hard_delete`，不支持 `STANDARD → STANDARD_IA → GLACIER → DEEP_ARCHIVE` 的存储类转换。`StorageClass` 字段在创建后永不更新 | `internal/reconcile/lifecycle.go:70-110` — `sweepExpired` 只做删除；`internal/service/file.go:149-162` — `StorageClass` 仅在 `Put` 时设置；`internal/config/config_ai.go` — 无自动分层配置 |

---

## 方向一：存储层零拷贝服务端复制

### 产品价值

| 维度 | 影响 |
|------|------|
| **大对象复制性能** | 当前 `copyObject` 读取完整对象到 Go 堆内存，然后写入目标。对于 1GB 对象，这需要 1GB+ 内存分配、两次网络传输（从存储读到内存 + 从内存写到存储），延迟 = 下载时间 + 上传时间。S3 原生支持服务端复制（单次 API 调用，数据不离开存储集群），延迟和资源消耗减少 50-90% |
| **内存压力** | 并发复制多个大对象时，Go 进程内存暴涨到 OOM 的风险。当前 `copyObject` 没有流式传输优化——`Get()` 返回 `io.ReadCloser`，但 `Put()` 直接传给后端，若后端不支持流式写入则全量缓冲 |
| **跨地域复制加速** | 当前复制（`internal/replication/replication.go`）同样是 Get→Put 模式。对于跨地域复制，若两个后端都支持服务端复制，数据可以直接在存储集群之间传输，不需要通过 aero-vault 节点中转 |
| **SDK 体验** | S3 SDK 的 `CopyObject` API 直接被翻译为 Get+Put，用户期望的近乎瞬时的复制（尤其是小对象）无法实现 |

### 现状与代码证据

**Storage 接口无 Copy 方法：**

```go
// internal/storage/storage.go:30-85
type Storage interface {
    Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, error)
    Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
    Stat(ctx context.Context, key string) (ObjectInfo, error)
    Delete(ctx context.Context, key string) error
    List(ctx context.Context, prefix, marker string, limit int) (ListResult, error)
    PresignGet(...)
    PresignPut(...)
    InitMultipart(...)
    UploadPart(...)
    CompleteMultipart(...)
    AbortMultipart(...)
    Backend() string
    // ❌ 无 Copy(ctx, srcKey, dstKey, opts) 方法
}
```

**S3 处理器的 copyObject 是内存中转型：**

```go
// internal/api/s3compat/extra.go:39-65
func (h *Handler) copyObject(w http.ResponseWriter, r *http.Request, dstBucket, dstKey, copySource string) {
    tenant := mw.TenantFrom(r.Context())
    srcBucket, srcKey, ok := parseCopySource(copySource)
    // ...
    rc, src, err := h.svc.Get(r.Context(), tenant, srcBucket, srcKey)  // ← 全量读入
    // ...
    dst, err := h.svc.Put(r.Context(), tenant, dstBucket, dstKey, rc, src.Size, opts)  // ← 全量写出
    // ...
}
```

**每个后端实现都无 Copy：** `grep -rn "func.*Copy\b" internal/storage/*.go` → 零命中。`local.go`、`s3.go`、`oss.go`、`cos.go` 都不提供 Copy 方法。

**Service 层无 Copy：** `internal/service/file_crud.go` 中 `Put()` 和 `Get()` 分别实现，无 `CopyObject()` 方法。S3 handler 直接调用 `Get` + `Put`。

**跨区复制的 Get→Put 模式：** `internal/replication/replication.go` 中的复制 worker 同样遵循 Get→Put 路径。

### 架构权衡与建议方案

#### 方案 A：在 Storage 接口上增加 `Copy(ctx, srcKey, dstKey, opts) (ObjectInfo, error)`

```
Storage 接口新增的方法
│
├── LocalStorage: 文件系统级硬链接（同一分区）或 copy_file_range syscall
├── S3Storage:    S3 CopyObject API（单次 HTTP PUT 请求）
├── OSSStorage:   OSS CopyObject API
└── COSStorage:   COS CopyObject API
```

**优势：**
- 云后端实现极为简单（单 API 调用）
- 大幅减少大对象复制的延迟和内存消耗
- 流式架构纯净

**权衡：**
- `LocalStorage` 需要区分跨分区复制（硬链接不可用时回退到流式复制）
- 需要处理 `x-amz-metadata-directive: REPLACE`（替换元数据）和 `COPY`（保留源元数据）
- 需要处理 `x-amz-copy-source-if-*` 条件头
- 版本化对象的复制需要额外处理 `versionId` 查询参数

#### 方案 B：在 Service 层增加 CopyObject + 后端回退

Service 层优先尝试存储端的 Copy，若返回 `ErrUnsupported` 则回退到当前的 Get→Put 路径。

```
FileService.CopyObject(ctx, tenant, srcBucket, srcKey, dstBucket, dstKey, opts)
  ├─ store.Copy(ctx, srcSK, dstSK, opts)  // 零拷贝路径
  └─ 回退: Get( srcSK ) → Put( dstSK )    // 兼容路径
```

**优势：**
- 向后兼容所有现有后端
- 无需一次性在所有后端实现 Copy

#### 边界情况

| 场景 | 处理 |
|------|------|
| 跨后端复制（local→s3） | 存储层无法优化，必须回退到 Get→Put |
| 相同后端不同分区（local FS跨磁盘） | `os.Rename`/`io.Copy` 回退 |
| 源对象正在被写入 | S3 CopyObject 读取写入中的对象（最终一致性），需文档化行为 |
| 复制锁定的对象 | 应复制锁定状态（`LockedUntil` 字段），目标对象继承锁定 |
| 复制标签/ACL | 默认 COPY 源标签，REPLACE 用请求头覆盖 |
| 超大对象（>5GB S3 限制） | S3 CopyObject 对 >5GB 对象需要 multipart copy upload，`s3.go` 需实现 `UploadPartCopy` |

---

## 方向二：对象锁 Governance / Compliance 双模式与 WORM 完备性

### 产品价值

| 维度 | 影响 |
|------|------|
| **合规审计** | 金融（SEC 17a-4）、医疗（HIPAA）、政府（DoD 5015.2）要求 WORM 存储区分 Governance（可绕过）和 Compliance（不可绕过）。当前实现无法通过任何合规审计 |
| **产品信任** | 客户将关键数据存储在"不可变"存储中，但当前实现不存在真正的不可变性——任何拥有写权限的用户或操作员都可以通过删除 `locked_until` 字段来解除锁定 |
| **S3 兼容性** | 真实 S3 客户端（如 Veeam、Commvault）期望 `x-amz-object-lock-mode` 取值为 `GOVERNANCE` 或 `COMPLIANCE`，当前实现忽略该头，静默降级 |

### 现状与代码证据

**数据模型只有一个时间戳：**

```go
// internal/repository/repository.go:49
type Object struct {
    // ...
    LockedUntil  *time.Time // present when Object Lock is active
    // ❌ 无 LockMode 字段
}
```

**锁定检查仅验证时间不过期：**

```go
// internal/service/file_crud.go:221-230
func (s *FileService) checkLockBeforeOverwrite(ctx context.Context, tenant, bucket, key string, versioning bool) error {
    if !versioning {
        if cur, err := s.repo.GetObject(ctx, tenant, bucket, key); err == nil {
            if cur.LockedUntil != nil && cur.LockedUntil.After(time.Now()) {
                return fmt.Errorf("%w: overwrite blocked until %s", ErrLocked, ...)
            }
        }
    }
    // ❌ 未检查锁定模式：Governance 模式应允许特权用户通过 BypassGovernanceRetention 绕过
}
```

**S3 对象锁保留模式被忽略：**

```go
// internal/api/s3compat/handler.go:92-97
// x-amz-object-lock-legal-hold: when ON, store as _aero_legal_hold.
if lh := r.Header.Get("x-amz-object-lock-legal-hold"); lh == "ON" || lh == "on" {
    // 仅处理 legal-hold，❌ 未处理 x-amz-object-lock-mode / x-amz-object-lock-retain-until-date
}
```

**对象锁的 REST API 端点仅支持单个时间：**

```go
// internal/api/rest/router.go — LockObject 端点
r.Post("/files/*", h.postKey) → strings.HasSuffix(..., "/lock")
// Handler.LockObject 只接收直到日期，不接收模式
```

**没有锁定模式风暴的迁移记录：** `internal/repository/migrations/` 中从 0001 到 0024，没有一条迁移添加 `lock_mode` 列。

### 架构权衡与建议方案

**建议的数据模型扩展：**

```go
type Object struct {
    LockedUntil  *time.Time
    LockMode     string // "" | "GOVERNANCE" | "COMPLIANCE"
    LegalHold    bool   // 独立于时间锁，永久有效直到显式移除
}
```

**Governance 模式行为：**
- 锁定期间常规用户不能覆盖或删除对象
- 拥有 `s3:BypassGovernanceRetention` 权限的用户可以通过设置 `x-amz-bypass-governance-retention: true` 头来提前解除锁定
- 管理 API 可删除 Governance 模式锁定

**Compliance 模式行为：**
- 锁定期间**没有任何用户**（包括 root 和管理员）可以覆盖或删除对象
- `locked_until` 到期前绝对不可变
- 锁定期限只能延长，不能缩短

**Legal Hold 行为：**
- 独立于时间锁，设置后永久阻止覆盖/删除，直到显式移除
- 当前已有部分实现（`_aero_legal_hold` 元数据键），但未作为一等字段

#### 边界情况

| 场景 | 处理 |
|------|------|
| Governance 模式下特权用户覆盖 | 需在 Auth 中间件/策略引擎中检查 `s3:BypassGovernanceRetention` 权限 |
| Compliance 模式锁定对象被生命周期删除 | 生命周期必须跳过 Compliance 模式锁定的对象 |
| Compliance 模式锁定对象硬删除 | 即使后台回收（Reconcile）也绝对不能删除 |
| 版本化桶中的锁定 | 每个版本独立锁定；新版本可以有不同的锁定配置 |
| 锁定模式升级（Governance→Compliance） | 允许升级，不允许降级 |
| S3 REST API `x-amz-object-lock-mode` 头 | S3 handler 必须解析并存入 Object 字段 |

---

## 方向三：现代对象校验和算法

### 产品价值

| 维度 | 影响 |
|------|------|
| **数据完整性** | MD5 碰撞已被证明在实际场景中可以实现（2017 年已存在选择前缀碰撞攻击）。CRC32C 是硬件加速的（SSE 4.2），SHA256 提供密码学级安全。多种校验和组合提供分层保障 |
| **S3 兼容性** | 2023 年起 AWS S3 支持 `x-amz-checksum-crc32`、`x-amz-checksum-crc32c`、`x-amz-checksum-sha1`、`x-amz-checksum-sha256`，并通过 `x-amz-trailer` 支持尾部校验和。现代 S3 SDK 客户端会自动计算并验证这些校验和 |
| **大对象验证** | 对于 GB 级对象，MD5 计算需要完整读取所有字节。CRC32C 硬件加速可将校验和计算速度提高 10-50 倍。尾部校验和允许在流式传输过程中逐步计算和验证 |
| **数据迁移兼容性** | 从 AWS S3 迁移到 aero-vault 时，若源对象携带 CRC32C 校验和而目标不支持，校验和链条断裂，无法端到端验证数据完整性 |

### 现状与代码证据

**唯一支持的校验和算法是 MD5：**

```go
// internal/service/file_crud.go:58-73
func md5WrapReader(r io.Reader, contentMD5 string) (io.Reader, func() error, error) {
    // 仅支持 Content-MD5 解码和验证
    expected, err := base64.StdEncoding.DecodeString(contentMD5)
    h := md5.New()
    return io.TeeReader(r, h), func() error { ... }, nil
}
```

**写入时存储校验和：**

```go
// internal/service/file_crud.go:183
storeContentMD5(&opts)
// → opts.Metadata["_aero_content_md5"] = opts.ContentMD5
```

**读取时返回：**

```go
// internal/api/s3compat/handler.go:695-696
if v, ok := meta["_aero_content_md5"]; ok && v != "" {
    w.Header().Set("x-amz-checksum-md5", v)
}
```

**S3 兼容层代码零 CRC32C/SHA256 引用：** `grep -rn "CRC32C\|CRC32\|SHA256\|sha256" internal/api/s3compat/` → 仅 `sigv4_test.go` 使用 SHA256 用于签名计算，而非校验和验证。

**配置层无校验和算法选择：** `internal/config/config.go` 无 `ChecksumAlgorithm` 或类似配置。

### 架构权衡与建议方案

**建议的校验和抽象：**

```go
type ChecksumAlgorithm int
const (
    ChecksumMD5    ChecksumAlgorithm = iota // 现有
    ChecksumCRC32                           // 新增
    ChecksumCRC32C                          // 新增（硬件加速）
    ChecksumSHA1                            // 新增
    ChecksumSHA256                          // 新增
)

// ChecksumReader wraps an io.Reader and computes the chosen checksum.
type ChecksumReader struct {
    reader  io.Reader
    algo    ChecksumAlgorithm
    hash    hash.Hash
    trailer bool  // when true, checksum is sent as HTTP trailing header
}
```

**S3 处理器需要适配：**

```
PUT /key → 读取 x-amz-checksum-{crc32,crc32c,sha1,sha256} → 包装 ChecksumReader
GET /key → 响应 x-amz-checksum-{crc32,crc32c,sha1,sha256}（如果存储了）
```

**校验和存储：** 当前方案使用 `_aero_content_md5` 元数据键。类似地，可使用 `_aero_checksum_crc32c` 等系统元数据键，或使用新的数据库列（迁移 0025）。

#### 边界情况

| 场景 | 处理 |
|------|------|
| 同时提供多个校验和头 | 全部验证，任意一个不匹配则拒绝写入 |
| 已知良好对象没有校验和 | 读取时返回旧对象，不报错；可通过后台 job 逐步补齐 |
| 尾部校验和（trailer） | Go `net/http` 不支持原生 trailer，需在客户端手动缓冲或使用 `Transfer-Encoding: chunked` 并手动读取 trailer |
| CRC32C 硬件加速不可用 | `hash/crc32` 包自动使用 IEEE 表；可软件模拟，性能稍低但不影响正确性 |
| 升级兼容 | 新对象写新校验和，旧对象保持原有 MD5，逐步迁移 |

---

## 方向四：S3 请求级加密头

### 产品价值

| 维度 | 影响 |
|------|------|
| **S3 协议兼容性** | 现代 S3 SDK 和工具（awscli、boto3、MinIO SDK）在初始化加密时默认发送 `x-amz-server-side-encryption: AES256`。当前实现忽略此头，导致许多工具无法正常工作 |
| **多租户加密隔离** | 当前 SSE 是全局配置（`STORAGE_LOCAL_SSE_KEY`）：所有对象使用同一个密钥。SSE-KMS 允许多密钥管理，SSE-C 允许每个请求由客户端提供密钥，实现租户级别的加密隔离 |
| **BYOK 场景** | 企业客户要求"自带密钥"（Bring Your Own Key）——他们在每次请求中提供加密密钥，服务器不持久化密钥。SSE-C 是实现 BYOK 的标准 S3 方式 |
| **合规** | PCI DSS、HIPAA 要求数据传输和存储加密。请求级加密头允许客户端强制执行每请求加密策略 |

### 现状与代码证据

**S3 处理器完全缺失加密头处理：**

```bash
grep -rn "x-amz-server-side-encryption" internal/api/s3compat/  →  零命中
grep -rn "x-amz-server-side-encryption-customer" internal/api/s3compat/  →  零命中
```

**现有 SSE 是全局配置，不是请求级别的：**

```go
// internal/storage/encrypt.go — AES-GCM 加密器
// 通过 NewLocal(LocalConfig{SSEKey: "..."}) 全局启用
// 所有对象统一使用同一个密钥加密
```

**本地 SSE-KMS 支持：** `internal/storage/kms.go` 和 `internal/storage/secret.go` 支持 EnvSecretProvider 和 KMS HTTP Provider，但这些都是服务器启动时配置的全局提供者，不支持 per-request 密钥。

**缺少的 S3 加密场景：**

| S3 头 | 当前状态 | 需要实现 |
|-------|---------|---------|
| `x-amz-server-side-encryption: AES256` | 忽略 | 使用服务器默认加密 |
| `x-amz-server-side-encryption: aws:kms` | 忽略 | 使用 KMS 密钥加密（需要 `x-amz-server-side-encryption-aws-kms-key-id`） |
| `x-amz-server-side-encryption-customer-algorithm: AES256` | 忽略 | 使用请求中客户提供的密钥加密 |
| `x-amz-server-side-encryption-customer-key` | 忽略 | 客户提供的 256-bit AES 密钥（base64） |
| `x-amz-server-side-encryption-customer-key-MD5` | 忽略 | 客户密钥的 MD5 校验 |

### 架构权衡与建议方案

**分层加密模型：**

```
S3 Request Headers
    │
    ├── x-amz-server-side-encryption: AES256
    │   → 使用服务器配置的默认密钥（与当前 SSEKey 相同）
    │
    ├── x-amz-server-side-encryption: aws:kms
    │   → 使用 KMS 提供者的密钥（可 per-request 指定 key-id）
    │
    └── x-amz-server-side-encryption-customer-algorithm: AES256
        → 从请求中提取密钥 → 解密时要求请求再次携带该密钥
```

**Storage 接口变更：**

```go
type PutOptions struct {
    ContentType string
    Metadata    map[string]string
    // 新增：
    SSEKind     string // "AES256" | "aws:kms" | "SSE-C" | ""
    SSEKey      []byte // SSE-C: customer-provided key (内存中，从不持久化)
    SSEKeyID    string // aws:kms: key ID
}
```

**重要设计决策：SSE-C 密钥永不持久化**

SSE-C 的核心契约是服务器**不存储**加密密钥——客户端在每次 GET 请求中必须重新提供相同的密钥。当前的 `encrypt.go` 架构（密钥在启动时已知，存储在内存中）需要扩展以支持动态密钥。

**响应头回传：** 对于使用了 SSE-C 的对象，GET/HEAD 响应必须包含 `x-amz-server-side-encryption-customer-algorithm: AES256` 头，以便客户端知道需要用客户密钥解密。

#### 边界情况

| 场景 | 处理 |
|------|------|
| PUT 时用 AES256，GET 时用 SSE-C | 不允许——加密类型在 PUT 时确定，GET 时匹配 |
| SSE-C 对象复制（CopyObject） | 目标必须使用相同的 SSE-C 设置，或显式重新加密 |
| SSE-C + multipart upload | 每个 UploadPart 必须提供相同的 SSE-C 头 |
| 未加密的存储后端收到 SSE 请求 | 本地 FS 可以加密；S3 后端可透传 SSE 头到真正的 S3 |
| SSE-C 密钥丢失 | 对象永久不可读——这是 BYOK 的内生风险，需文档化 |

---

## 方向五：存储类生命周期自动转换

### 产品价值

| 维度 | 影响 |
|------|------|
| **成本优化** | 典型对象存储工作负载中，80% 的数据在 30 天后很少被访问。如果没有自动分层，所有数据一直占用热存储成本。S3 生命周期转换（STANDARD→STANDARD_IA→GLACIER）可降低 60-80% 存储成本 |
| **产品竞争力** | 当前所有主流对象存储（AWS S3、Azure Blob、GCS）都支持存储类自动转换。缺失此功能使 aero-vault 在成本敏感的归档场景下无竞争力 |
| **多后端分层路由** | 存储类转换可以超越"对象标签"的语义——不同存储类可以实际对应不同的后端（热数据在本地 FS，冷数据在 S3，归档在 GLACIER/OSS_ARCHIVE） |

### 现状与代码证据

**生命周期仅支持过期删除：**

```go
// internal/reconcile/lifecycle.go:70-89
func (l *LifecycleJob) sweepExpired(ctx context.Context) (soft, hard int) {
    // ...
    for _, obj := range expired {
        l.handleExpiredObject(ctx, obj, bcfg.ExpireAction)
    }
}

func (l *LifecycleJob) handleExpiredObject(ctx context.Context, obj repository.Object, action string) bool {
    switch action {
    case "soft_delete":
        // 软删除
    case "hard_delete":
        // 硬删除
    }
    // ❌ 无 "transition_to_ia", "transition_to_glacier" 等动作
}
```

**StorageClass 是静态字段：**

```go
// internal/service/file_crud.go:181
StorageClass: StorageClassOrDefault(opts.StorageClass),
// 创建后永不更新——没有 UpdateStorageClass 方法
```

**BucketConfig 只有 Expire 配置：**

```go
// internal/repository/repository.go:57-68
type BucketConfig struct {
    ExpireAfterDays int    // 过期天数
    ExpireAction    string // "soft_delete" | "hard_delete"
    // ❌ 无 TransitionDays / TransitionTarget / TransitionStorageClass 字段
}
```

**迁移文件中无转换规则：** `internal/repository/migrations/` 从 0001 到 0024，没有包含 `transition` 或 `storage_class` 转换相关的迁移。

**多后端写时路由缺失：** `internal/storage/factory.go` 创建单一后端实例。不存在"根据 StorageClass 路由到不同后端"的逻辑。

### 架构权衡与建议方案

**建议的生命周期规则扩展：**

```
Bucket Lifecycle Rules
├── Expiration: expire_days + action (当前已有)
└── Transition: transition_days + target_storage_class (新增)
    ├── STANDARD → STANDARD_IA       (天数可配置)
    ├── STANDARD_IA → GLACIER         (天数可配置)
    └── GLACIER → DEEP_ARCHIVE        (天数可配置)
```

**数据模型扩展：**

```go
type TransitionRule struct {
    Days      int    // 创建后天数
    TargetClass string // "STANDARD_IA" | "GLACIER" | "DEEP_ARCHIVE"
}

type BucketConfig struct {
    // ... 现有字段
    TransitionRules []TransitionRule // 新增
}
```

**转换执行器架构：**

```
reconcile/transition.go (新文件)
    │
    ├── scan: SELECT objects WHERE storage_class != desired AND age > transition_days
    ├── transition: 对于每个后端，"过渡"的含义不同：
    │   ├── LocalStorage:     无操作，仅更新 metadata（本地 FS 无存储类概念）
    │   ├── S3Storage:        调用 S3 CopyObject 并指定 StorageClass 参数
    │   └── OSSStorage / COSStorage: 类似 S3，调用云 API 更改存储类
    └── update: repo.UpdateStorageClass(ctx, objectID, newClass)
```

**Storage 接口新增转换方法：**

```go
type Storage interface {
    // ...
    // TransitionClass moves an object to a different storage class. For
    // backends that don't support tiering (local FS), this is a no-op.
    TransitionClass(ctx context.Context, key string, newClass string) (ObjectInfo, error)
}
```

#### 边界情况

| 场景 | 处理 |
|------|------|
| 转换失败（目标后端不可用） | 记录失败，重试队列，保留原地 |
| 锁定的对象（WORM） | 可以转换存储类，但必须保留所有锁定属性 |
| 正在进行转换时读取对象 | 读取成功后端；S3 转换是原子的 |
| 版本化桶中的转换 | 每个版本独立转换；转换不改变版本语义 |
| 跨后端的存储类（local→S3） | 转换需要将数据实际移动到目标后端，涉及 Get→Put 传输 |
| 同后端内的存储类（S3 STANDARD→STANDARD_IA） | 单 API 调用（S3 CopyObject with storage class） |
| 成本估算 | 转换本身有成本（API 调用费 + 提前删除费），需在文档中说明 |

---

## 实施优先级建议

```
高影响/低风险 ──────────────────────────────────> 高影响/高风险
                    │
  方向一：Copy      │  方向二：Object Lock
  (Storage 接口     │  (Compliance 模式)
   新增方法)         │
                    │
  方向三：Checksum  │  方向四：SSE-C
  (新增算法，      │   (请求级加密头，
   不改数据模型)    │   密钥生命周期管理)
                    │
                    │  方向五：Lifecycle
                    │  Transition
                    │  (跨后端路由，
                    │   多 tier 规则引擎)
                    │
```

**建议顺序：**

1. **方向一（存储层 Copy）** → 收益最为明确，实现路径清晰。S3 后端可在 1-2 天内完成，local 后端可使用硬链接或 `copy_file_range`。可直接减少复制延迟 50-90%
2. **方向三（校验和算法）** → 低风险增量修改，不改变现有数据模型。新增 CRC32C 和 SHA256 支持，向后兼容 MD5。提升大对象传输的数据完整性
3. **方向二（Object Lock 双模式）** → 需要迁移增加 `lock_mode` 列，修改检查逻辑，在 S3 handler 中解析 `x-amz-object-lock-mode` 头。风险和收益中等
4. **方向四（SSE 请求级加密头）** → 中等风险：SSE-C 的密钥不做持久化设计是安全敏感决策，需要仔细设计。但 SSE-S3（AES256）头支持是低风险快速胜利
5. **方向五（Lifecycle Transition）** → 高风险/高收益：需要多后端路由架构、转换执行器、跨后端数据移动。建议在前四个方向完成后开启

---

## 附录：检查清单

### 方向一：存储层 Copy

- [ ] `Storage` 接口新增 `Copy(ctx, srcKey, dstKey, opts) (ObjectInfo, error)`
- [ ] `LocalStorage` 实现：`os.Link()`（同分区）→ `copy_file_range`（同 FS）→ `io.Copy`（回退）
- [ ] `S3Storage` 实现：S3 `CopyObject` API
- [ ] `OSSStorage` 实现：OSS `CopyObject` API
- [ ] `COSStorage` 实现：COS `CopyObject` API
- [ ] `FileService.CopyObject(ctx, tenant, srcBucket, srcKey, dstBucket, dstKey, opts)`
- [ ] S3 handler 的 `copyObject` 使用 `svc.CopyObject()` 替代 `Get()+Put()`
- [ ] 处理 `x-amz-metadata-directive: COPY|REPLACE`
- [ ] 处理 `x-amz-copy-source-if-*` 条件头
- [ ] 处理版本化源对象（`?versionId`）
- [ ] `contract_test.go` 添加 Copy 用例
- [ ] 更新 OpenAPI spec

### 方向二：Object Lock

- [ ] 迁移 0025：`objects` 表新增 `lock_mode TEXT` 和 `legal_hold BOOLEAN`
- [ ] `repository.Object` 新增 `LockMode string` 和 `LegalHold bool`
- [ ] `s.repo.SetObjectLock(ctx, tenant, bucket, key, until, mode)` 模式相关方法
- [ ] `s.repo.SetLegalHold(ctx, tenant, bucket, key, on bool)` 独立方法
- [ ] `checkLockBeforeOverwrite` 检查锁定模式
- [ ] S3 handler 解析 `x-amz-object-lock-mode` 和 `x-amz-object-lock-retain-until-date`
- [ ] S3 handler 解析 `x-amz-object-lock-legal-hold` 并存入 `LegalHold`
- [ ] REST handler 的 `/lock` 端点接受 mode 参数
- [ ] Auth/策略引擎支持 `s3:BypassGovernanceRetention`
- [ ] Reconcile/retention 跳过 Compliance 模式锁定的对象

### 方向三：校验和算法

- [ ] `ChecksumAlgorithm` 枚举类型
- [ ] `ChecksumReader` / `ChecksumWriter` 包装器
- [ ] S3 handler 解析 `x-amz-checksum-crc32`/`crc32c`/`sha1`/`sha256`
- [ ] 写入时存储校验和到元数据（`_aero_checksum_crc32c` 等）
- [ ] GET/HEAD 响应回传 `x-amz-checksum-*` 头
- [ ] 尾部校验和（trailer）支持
- [ ] 多校验和同时验证
- [ ] multipart upload 的校验和聚合（`x-amz-checksum-crc32c` 在 CompleteMultipartUpload 时聚合）

### 方向四：SSE 请求级加密头

- [ ] S3 handler 解析 `x-amz-server-side-encryption: AES256|aws:kms`
- [ ] S3 handler 解析 `x-amz-server-side-encryption-aws-kms-key-id`
- [ ] S3 handler 解析 `x-amz-server-side-encryption-customer-algorithm/key/key-MD5`
- [ ] SSE-C 密钥管理：每个请求中提取密钥但不持久化
- [ ] GET/HEAD 验证 SSE-C 请求中的密钥匹配
- [ ] `PutOptions` 新增 SSE 字段
- [ ] `GetOptions` 新增 SSE-C 字段（解密需要密钥）
- [ ] CopyObject 中 SSE 头透传
- [ ] multipart upload 中 SSE 头透传
- [ ] 审计日志记录加密类型变更

### 方向五：Lifecycle Transition

- [ ] `TransitionRule` 数据模型 + 迁移 0025
- [ ] `BucketConfig.TransitionRules` 字段
- [ ] 生命周期配置 API（REST + S3）支持转换规则
- [ ] `Storage.TransitionClass(ctx, key, newClass) (ObjectInfo, error)`
- [ ] `reconcile/transition.go` 扫描 + 执行转换
- [ ] `s.repo.UpdateStorageClass(ctx, objectID, newClass)`
- [ ] 跨后端转换（local→S3）的 Get→Put 回退
- [ ] 锁定对象的转换保护
- [ ] 版本化对象的转换语义
- [ ] 转换失败重试 + 死信队列
- [ ] 成本计算/预警（转换 API 调用费 + 提前删除费）
