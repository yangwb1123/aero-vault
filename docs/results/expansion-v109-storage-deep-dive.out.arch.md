以下是对 `expansion-v123-storage-deep-optimization-s3-completeness-data-integrity.md` 的架构层面分析。

---

# 架构分析报告：存储层深度优化、S3 协议完备性、数据完整性增强

## 1. 架构评估

### 1.1 当前架构的优势

该项目的架构具有几个经过深思熟虑的工程决策：

**清晰的分层职责分离。**
`Protocol Adapters → FileService → Storage/Repository` 的分层模型是正确的。协议适配器（REST/S3/WebDAV/MCP）共享同一个 `FileService` 控制器，确保所有协议路径下的一致行为。`Storage` 接口的抽象（7 个后端实现：local/S3/OSS/COS，加上 `CircuitBreaker` 装饰器）提供了良好的多态性，而 `Repository` 接口则处理元数据持久化（SQLite/Postgres）。

**事件驱动解耦。**
`EventBus → Worker` 模式（AV 扫描、复制、Webhook、Reconcile/GC）正确地将同步请求路径与异步副作用分离。`EventSink` 接口被设计为可插入的，`noopSink` 的回退确保事件失败不会破坏用户请求。

**Opt-in 安全默认值。**
AI、pgvector、Qdrant、事件、集群模式、WebDAV 都是通过标记控制的，默认关闭。`nil` 嵌入器/LLM/重排序器不会破坏核心 CRUD 路径——这对于基线 CI 门的稳定性至关重要。

**一致的迁移系统。**
双文件迁移模式（每个变更 `{sqlite,postgres}/NNNN_*.{up,down}.sql`）是行业标准的正确选择。SQL 占位符不能重用规则（I1）虽然在自动检查中有些繁琐，但在 SQLite↔Postgres 之间的差异中防止了一类微妙的错误。

**多协议入口点共享单一服务层。**
这是一个重要的架构胜利。S3 适配器、REST API 和 MCP 工具都调用 `FileService`。这意味着锁定检查、配额、版本控制和事件发布都能一致地工作，无论入口点如何。

### 1.2 关键架构局限（架构债务）

通过对代码库的分析，我在文档确定的 5 个缺口之上，识别出以下更多的架构级别债务：

**瓶颈 #1：`Storage` 接口缺乏针对连接操作的语义。**
`Copy` 不仅仅是 `Get+Put`——它是一个具有完全不同语义的原语操作。同样，`TransitionClass` 也不是 `Get(Copy(Put))`。当前的接口将它们归约为数据传输操作，失去了服务端优化。这个问题源于接口设计的 **语义不完整性**：`Storage` 接口是围绕"内容寻址的 blob 读取器/写入器"建模的，而不是"分布式对象存储"。区别至关重要：对象存储具有复制、转换、标签传播、ACL 继承、锁定传播和校验和传递——当前的接口抽象没有捕捉到这些。

**瓶颈 #2：`PutOptions` 被过度重用。**
所有操作都使用单一的 `PutOptions` 结构体，但 SSE 配置、锁定、标签、校验和算法选项应该在操作级别作用域，而不是全局的。这会产生耦合：向 `PutOptions` 添加一个字段会影响所有调用者。

**瓶颈 #3：元数据和数据存储之间的隐形耦合。**
在 `LocalStorage` 中，SSE 信封存储为侧车 JSON 文件。对于 S3 后端，元数据被映射到 S3 用户元数据。这种隐形耦合使得抽象中出现漏洞：元数据不再有统一的表示。

**瓶颈 #4：异步操作缺少可观测性相关性。**
当用户执行 `PUT` 触发事件时，该事件会产生一个复制 job，该 job 由 `JobPool` 执行，然后调用 `primary.Get() + replica.Put()`。如果出现故障，用户收到的 HTTP 200 响应是成功的，但复制 job 可能会失败。没有跟踪 ID 连接 HTTP 请求→事件→job→重试。这在生产中是运维盲区。

**瓶颈 #5：存储后端的单一实例（无多后端路由）。**
`storage/factory.go` 创建单一的 `Storage` 实例。`StorageClass` 只是一个标签——它不会路由到不同的后端。对于真正的多层级（热/冷/归档）或混合云场景，对象需要根据策略路由到不同的后端，而目前的架构不支持这一点。

### 1.3 关键设计决策评估

| 决策 | 评估 |
|------|--------|
| 协议适配器直接调用 `FileService`，而非 RPC/内部消息 | ✅ 正确。进程内调用的开销可忽略不计；RPC 会增加延迟和复杂性 |
| 每个协议适配器有独立的 HTTP 路由设置（S3 用 chi，WebDAV 用独立逻辑，MCP 用 JSON-RPC） | ✅ 正确。适配器应作为隔离的网关运行 |
| SSE 作为 `Storage` 层的一个透明包装器实现，而非 `FileService` | ⚠️ 合理。加密越靠近数据越好，但 SSE-C 请求级密钥迫使在 `PutOptions`/`GetOptions` 中有所意识——打破了透明性 |
| `FileService.EventSink` 将错误吞掉 | ✅ 正确。用户请求不应因事件失败而失败，但需要监控来检测盲点 |
| 锁检查只在 `FileService` 层完成 | ✅ 正确。适配器不应该处理业务规则 |
| 版本化对象使用追加式版本 ID 后缀（`@v<id>`） | ✅ 正确。避免原地更新，简化 GC |
| `LockedUntil` 是一个时间指针 | ❌ 错误。缺少模式（治理/合规/LegalHold）作为一等字段是合规债务 |

---

## 2. 高价值扩展方向

除了文档中已识别的 5 个方向之外，我在此添加架构视角，并引入 2 个额外的战略方向。

### 方向 A（文件方向 1 — P0）：存储层零拷贝复制

**我的评估：** P0 正确。这是目前最昂贵的技术债务。

**架构影响：**
- `Storage` 接口获得一个 `Copy()` 方法，具有 `CopyOptions` 结构体（独立于 `PutOptions`）
- `ErrCopyUnsupported` 哨兵错误，供不支持服务端复制的后端使用
- `FileService.CopyObject()` 作为入口点，包含回退逻辑（优先 `Storage.Copy()`，兜底 Get+Put）
- 复制 workder 也要使用它（`replication.go` 中的 Get+Put 模式同样需要优化）

**关键设计决策：条件头的归属。**
`x-amz-copy-source-if-*` 条件头（`If-Match`、`If-None-Match`、`If-Modified-Since`、`If-Unmodified-Since`）应该在哪个层评估？

- **选项 A：** 在 `Storage.Copy()` 中评估 → 适用于 S3 后端（原生支持），但 local 后端需要额外的 stat 调用
- **选项 B：** 在 `FileService` 层评估 → 统一所有后端的行​​为，但增加了额外的 Get 操作
- **我的建议：** 选项 B。`FileService` 已经拥有关于 Repo 中元数据的权威视图。在 `Storage` 层评估条件头会在后端中重复 `FileService` 逻辑。对于 S3，条件头可以透传给 S3 API，但 local 后端不应强制匹配 S3 的语义。

### 方向 B（文件方向 2 — P1）：对象锁双模式

**我的评估：** 正确优先级。但缺少 LegalHold 作为一等字段是一个安全/合规问题。

**架构影响：**
- 迁移 0025 添加 `lock_mode TEXT`、`legal_hold BOOLEAN` 列
- `BucketConfig.ObjectLockSeconds` → 还应支持 `ObjectLockMode`（存储级默认值：治理/合规）
- `checkLockBeforeOverwrite` → 根据模式分支
- Auth 策略需要解析 `s3:BypassGovernanceRetention` → 意味着策略引擎必须理解锁模式
- `Reconcile/retention` 和 `reconcile/lifecycle` 必须跳过合规锁定的对象

**设计决策：锁定模式演进。**
- 升级路径应允许 Governance→Compliance 升级，但不允许降级
- LegalHold 独立于时间锁运行：即使 `locked_until` 到期，如果 LegalHold 为 true，对象仍然被锁定
- 目前 `_aero_legal_hold` 存储在元数据中，但不应是元数据——它应是一个顶级对象字段

### 方向 C（文件方向 3 — P1）：校验和算法

**我的评估：** P1 正确。CRC32C 和 SHA256 支持对于现代客户端兼容性和数据完整性至关重要。

**架构影响：**
- `ChecksumAlgorithm` 枚举 → 作为一等字段在 `PutOptions`、`GetOptions` 和 `Object` 中
- `ChecksumReader`/`ChecksumWriter` → 可组合的 io.Reader 包装器，支持同时计算多个校验和
- 多校验和验证：如果客户端发送 `x-amz-checksum-crc32c` 和 `x-amz-checksum-sha256`，两者都必须验证

**关键设计决策：存储。**
- **选项 A：** 系统元数据键（`_aero_checksum_crc32c`、`_aero_checksum_sha256`）→ 利用现有元数据机制，但不支持索引
- **选项 B：** 新的数据库列（`checksum_crc32c TEXT`、`checksum_sha256 TEXT`）→ 支持查询，但需要迁移
- **我的建议：** 选项 A 用于快速赢利，如果查询变得必要再迁移到选项 B。校验和主要用于端到端完整性，而不是检索。

**边界情况：尾标（trailer）。**
Go 的 `net/http` 不支持服务器端的原生 HTTP 尾标。文档正确地指出了这一点。解决方案：手动分块编码或缓冲直到完整性验证完成。后者的 IMO 对于非流式客户端来说更简单且足够。

### 方向 D（文件方向 4 — P1）：请求级 SSE 头

**我的评估：** 正确的优先级，但 SSE-C 需要仔细的安全架构审查。

**架构影响：**
- SSE-S3（AES256）：低风险。复用现有密钥，仅需添加头解析
- SSE-KMS：中风险。需要现有的 KMS 提供者接口支持 per-request key-id
- SSE-C：高风险。密钥在请求中提供，且**绝不能持久化**。当前架构（启动时密封，信封存储在 metadata.json 中）不适用于 SSE-C，因为密钥只在 ram 中存在，仅限请求期间

**SSE-C 设计决策——密钥生命周期：**

```
PUT 请求 → [SSE-C 密钥] → 加密数据 → 丢弃密钥
                                      
GET 请求 → [SSE-C 密钥] → 解密数据 → 丢弃密钥
            ↑ 客户端在每次请求中重新提供密钥
```

这对于当前的 `encrypt.go` 架构来说是一个根本性的变化，在该架构中，`envelopeEncrypter` 在启动时拥有密钥提供者的引用。对于 SSE-C，加密/解密**不能**使用 `envelopeEncrypter`——它必须使用直接从请求头派生的临时密钥。

**我的建议：** 分阶段实现 SSE 头：
1. 先添加 SSE-S3（AES256）头解析——这是低风险、高 S3 兼容性
2. 然后添加 SSE-KMS——重用现有 KMS 抽象
3. 最后是 SSE-C——需要全新的"临时加密"路径

### 方向 E（文件方向 5 — P2）：生命周期转换

**我的评估：** P2 正确。虽然高价值，但它依赖于其他方向的数量（特别是多后端路由）。

**架构影响：**
- `Storage.TransitionClass(ctx, key, newClass)` 方法
- `TransitionRule` 数据模型 + 迁移
- `reconcile/transition.go`：新的 workder
- 跨后端转换：local→S3 需要 Get→Put 传输（回归到方向 A 的回退路径）
- 成本估算：转换有 API 成本 + 提前删除费用；系统应在执行转换前提供成本预警

**关键设计决策：转换 vs 复制+删除。**
- S3 后端：`CopyObject` 配合 `StorageClass` 参数（单原子调用）
- Local 后端：无操作（本地文件系统没有存储类概念）
- 跨后端：Get+Put+Delete（昂贵的，但罕见的边缘情况）

### 方向 F（新增）：多后端路由引擎

我在文件未覆盖到的情况下，将此作为最高价值架构方向识别。

**为什么需要：**
目前，`storage/factory.go` 创建单一的 `Storage` 实例。这意味着：
- 所有对象都驻留在同一个后端
- `StorageClass` 只是一个标签，不是路由决策
- 无法支持热/冷/归档层级
- 混合云（本地 + S3 + 其他供应商）需要一个路由层

**架构向量：**

```
Storage Router (新抽象层)
    │
    ├── 路由规则（基于 StorageClass / bucket / prefix / tag）
    │   ├── STANDARD → LocalStorage
    │   ├── STANDARD_IA → S3Storage（以成本优化）
    │   └── GLACIER → S3Storage-Glacier
    │
    ├── 向后端分发读取/写入
    └── 转换 executor（用于在层级间移动）
```

**这如何影响其他方向：**
- 方向 E（生命周期转换）成为此路由引擎的自然扩展
- 方向 A（复制）通过路由引擎的 `Copy(ctx, srcKey, dstKey)` 得到增强，该引擎可将目标映射到正确的后端
- 这是架构深度影响最大的方向，但实现复杂度也最高

### 方向 G（新增）：可观测性相关性

**为什么需要：**
缺乏在异步边界之间的跟踪 ID 连接。当前架构中，每个子系统（HTTP、事件总线、job 队列、索引）独立工作，没有共享 trace ID。

**架构向量：**
- OTel 传播已存在（`OTEL_*` 配置）但需要在事件/队列边界上完成上下文传播
- `repository.Event` 结构体有 `RequestID` 字段——这是正确的种子，但 job 执行从未记录它
- 实现开销较低：在 `Event.Payload` 中传递 `traceparent`，在 job handler 中提取

---

## 3. 接口设计建议

### 3.1 `Storage` 接口演进原则

**建议的签名变更：**

```go
type Storage interface {
    // 现有方法（保留不变）
    Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, error)
    Get(ctx context.Context, key string, opts ...GetOption) (io.ReadCloser, ObjectInfo, error)
    // ... 其他现有方法
    
    // 新增
    Copy(ctx context.Context, srcKey, dstKey string, opts CopyOptions) (ObjectInfo, error)
    
    // 新增（用于方向 E）
    TransitionClass(ctx context.Context, key string, newClass string) (ObjectInfo, error)
}
```

**设计原则：**
1. **`CopyOptions` ≠ `PutOptions`**。复制有其自己的关注点（元数据指令、源条件、版本 ID）。使用独立类型防止关注点耦合。
2. **`GetOption` 作为 functional option**。`Get(ctx, key, WithSSECKey(key), WithRange(offset, length))`。这允许向后兼容的扩展。
3. **`ErrNotSupported` 哨兵**。后端通过返回 `ErrNotSupported` 来指示不支持。`FileService` 应优雅处理，回退到规范路径。

### 3.2 校验和抽象

```go
type ChecksumAlgorithm string

const (
    ChecksumMD5    ChecksumAlgorithm = "MD5"
    ChecksumCRC32  ChecksumAlgorithm = "CRC32"
    ChecksumCRC32C ChecksumAlgorithm = "CRC32C"
    ChecksumSHA1   ChecksumAlgorithm = "SHA1"
    ChecksumSHA256 ChecksumAlgorithm = "SHA256"
)

type ChecksumReader struct {
    reader    io.Reader
    algo      ChecksumAlgorithm
    hash      hash.Hash
    verifyOnEOF bool
    expected    []byte // optional: verify at EOF
}
```

**为什么作为结构体而非接口：** 校验和计算是一个横切关注点。结构体作为 io.Reader 的包装器，可以被链接和组合。多个校验和封装在一个 `MultiChecksumReader` 中。

### 3.3 SSE 分层模型

```
S3 请求层（头解析）
    │
    v
加密选择器（基于 SSE 种类选择加密器）
    ├── SSE-S3:  现有 envelopeEncrypter，全局密钥
    ├── SSE-KMS: 重用现有 DataKeyWrapper，但 per-request key-id
    └── SSE-C:   新的 ephemeralEncrypter（密钥来自请求，不持久化）
```

**建议的新接口：**

```go
type Encrypter interface {
    Encrypt(ctx context.Context, plaintext []byte) (ciphertext []byte, envelope string, err error)
    Decrypt(ctx context.Context, ciphertext []byte, envelope string) (plaintext []byte, err error)
}

type EncrypterFactory interface {
    EncrypterFor(ctx context.Context, opts PutOptions) (Encrypter, error)
}
```

这允许 SSE 实现可插拔，且不将密钥生命周期耦合到 `Storage` 实现。

### 3.4 向后兼容性策略

1. **`Copy()` 是协议扩展。** 旧后端（缺少 Copy 实现）回退到 Get+Put。无需中断变更。
2. **校验和算法是增加性的。** 新的校验和写入新元数据。旧对象缺少新校验和，返回时不报错。后台工作定时补齐。
3. **锁模式是增加性的。** 迁移使新列为 NULLable。旧对象用 ""（空字符串），解释为"锁定但未指定"旧行为。
4. **SSE-C 是全新的。** 现有对象保持使用当前加密方案。SSE-C 仅适用于选择加入的请求。
5. **生命周期转换是增加性的。** 旧配置保持不变。新配置字段在新 UI/API 中可见。

---

## 4. 技术选型

### 4.1 Go 标准库覆盖评估

所有 5 个方向都可以仅使用 Go 标准库实现，这符合项目的 `stdlib 优先`（I6）原则。

| 需求 | 标准库支持 | 备注 |
|---------|-----------------|-------|
| 复制（零拷贝） | `os.Link()`、`os.Rename()`、`io.Copy()` | Local 后端：`os.Link()` 用于硬链接（同分区），`copy_file_range` 可通过 `os` 包访问 |
| CRC32/CRC32C | `hash/crc32` | IEEE 和 Castagnoli 多项式 |
| SHA256 | `crypto/sha256` | 标准实现 |
| AES-256-GCM | `crypto/aes` + `crypto/cipher` | 已使用 |
| KMS 集成 | 需要 HTTP 客户端 | 已通过 `kms.go` 实现 |
| 校验和尾标 | 无原生支持 | 需要手动分块编码 |
| 后端路由 | 需要自定义实现 | 无标准库编排 |

**评估：** 零外部依赖。这是正确的。

### 4.2 第三方依赖评估

我同意文档的隐含立场：**目前不需要新的依赖**。在以下条件下应考虑新的依赖：

1. **KMS 集成。** 如果添加 HashiCorp Vault 集成，应使用官方的 `github.com/hashicorp/vault/api`——而不是从头实现。但这不是方向四（请求级 SSE）的先决条件，该方向可以使用现有的 `KMS HTTP Provider` 模式。
2. **成本估算。** 如果生命周期转换需要成本估算，可能需要一个查询 AWS S3 定价的库。但这应该是可选/外部的。
3. **OpenTelemetry 传播。** 现有的 OTel 集成已覆盖。

### 4.3 自建 vs 采购决策

| 场景 | 建议 | 理由 |
|----------|-----------|--------|
| 加密 | 自建 | 使用 Go 标准库；密钥管理已实现 |
| KMS | 集成（采购）| 通用 KMS（Vault、AWS KMS）提供审计、轮换、HSM 支持 |
| 校验和 | 自建 | 标准库完整覆盖 |
| 备份/DR | 自建 | 现有复制机制可以使用；加强即可 |

---

## 5. 实施路线图

### 5.1 优先级排序矩阵

```
        高影响                 影响               中影响
   ┌─────────────────┬─────────────────┬─────────────────┐
高  │                  │                  │                  │
风险 │  方向四：SSE-C    │  方向二：锁定模式  │  方向 G：可观测性  │
   │  (密钥生命周期,   │  (合规驱动,       │  (trace 传播)    │
   │  安全审计)        │  中等迁移工作)    │                  │
   │                  │                  │                  │
中  │  方向一：Copy    │  方向三：校验和    │  方向 F：路由引擎  │
风险 │  (P0, 清晰路径,  │  (增量, 无迁移,  │  (架构先决条件    │
   │  立即收益)        │  向后兼容)        │  用于方向五)      │
   │                  │                  │                  │
低  │  方向四：SSE-S3   │  方向 E：生命周期   │                  │
风险 │  (头解析, 快速赢利)│  转换             │                  │
   │                  │  (需要路由引擎)   │                  │
   └─────────────────┴─────────────────┴─────────────────┘
```

### 5.2 建议的实施顺序

#### 阶段 1：快速赢利 + 基础（2-3 周）

| 项目 | 周数 | 关键交付物 |
|------|------|--------|
| **方向一：Copy** | 1-2 | `Storage.Copy()` 接口 + 所有后端实现 + `FileService.CopyObject()` + S3 handler 更改为使用 Copy + 复制 workder 更改为使用 Copy + `contract_test.go` 覆盖 + OpenAPI 更新 |
| **方向四 — SSE-S3 头** | 0.5 | S3 handler 解析 `x-amz-server-side-encryption: AES256` → 进行中传递。无加密变更，仅协议兼容性 |
| **方向三 — CRC32C + SHA256** | 1-1.5 | `ChecksumAlgorithm` 枚举 + `ChecksumReader`/`ChecksumWriter` + S3 handler 解析头 + 元数据存储 + GET/HEAD 响应头 |

**里程碑 1：** S3 兼容性显著提升。现代 S3 SDK 客户端可以连接并通过校验和验证。复制性能提升 50-90%。

#### 阶段 2：合规与安全（3-4 周）

| 项目 | 周数 | 关键交付物 |
|------|------|--------|
| **方向二：锁模式** | 1.5-2 | 迁移 + 模型变更 + 治理/合规检查逻辑 + S3 handler `x-amz-object-lock-mode` 解析 + LegalHold 一等字段 + 策略引擎 `BypassGovernanceRetention` + Reconcile 合规跳过 |
| **方向四 — SSE-KMS** | 1-1.5 | per-request KMS key-id + `PutOptions.SSEKeyID` + 检索时的 KMS 决议 + CopyObject SSE 透传 |

**里程碑 2：** 金融/医疗/政府合规场景（SEC 17a-4、HIPAA、DoD 5015.2）成为可能。

#### 阶段 3：SSE-C 与深度安全（3-4 周）

| 项目 | 周数 | 关键交付物 |
|------|------|--------|
| **方向四 — SSE-C** | 2-3 | `EphemeralEncrypter` + S3 handler SSE-C 头解析（algo、key、key-MD5）+ GET 验证密钥匹配 + CopyObject SSE-C 透传 + Multipart SSE-C 一致性 + 安全审计：密钥在请求后从内存中清除 |
| **方向 G：可观测性** | 1 | 在 `Event.Payload` 中传播 traceparent + job handler 提取 + dashboard 面板用于复制/job 延迟 |

**里程碑 3：** BYOK（自带密钥）场景可用。完整的异步跟踪。

#### 阶段 4：成本优化（4-6 周）

| 项目 | 周数 | 关键交付物 |
|------|------|--------|
| **方向 F：路由引擎** | 2-3 | `StorageRouter` 接口 + 路由规则配置（StorageClass/bucket/prefix/tag）+ `FileService` 更改以使用 Router + 跨后端复制回退 |
| **方向 E：生命周期转换** | 2-3 | `TransitionRule` 模型 + 迁移 + `Storage.TransitionClass()` + `reconcile/transition.go` + S3 生命周期配置 API + 跨后端转换回退 + 成本预警 |

**里程碑 4：** 多层级存储可操作。热/冷/归档按策略自动转换。

### 5.3 风险与缓解

| 风险 | 可能性 | 影响 | 缓解 |
|------|------|--------|--------|
| **SSE-C 密钥安全漏洞** | 低 | 极高 | 在合并前进行安全设计审查；单元测试验证密钥在请求后归零；在测试中使用 `runtime.SetFinalizer` 验证清理 |
| **跨后端 Copy 回退竞争条件** | 中 | 中 | 在 Get→Put 回退中使用幂等性密钥；事务性的源/目标更新 |
| **迁移 0025 锁定模式 + 校验和冲突** | 中 | 中 | 将迁移拆分为两个：0025（锁定模式）、0026（校验和）。双迁移在生产中经过实践检验，但仍需仔细排序 |
| **合规锁定的对象被生命周期转换操作** | 低 | 极高 | 转换前显式的合规模式检查。单元测试必须覆盖所有锁定模式 + 生命周期动作组合 |
| **Go `net/http` 尾标限制** | 已知 | 中 | 只在客户端支持尾标；服务端在完整性验证前对对象进行缓冲并拒绝写入。对于 >100MB 的对象，尾标作为流式替代方案；目前对大多数用例来说缓冲是足够的 |
| **路由引擎引入的状态空间爆炸** | 中 | 高 | 限制为确定性、声明性的路由规则。禁止动态路由。使用表单（bucket → backendClass）而非通用规则引擎 |

### 5.4 非功能性要求

| 要求 | 目标 | 测量 |
|-----------|-------|-------|
| Copy 延迟（1GB 对象，S3→S3） | ≤当前延迟的 20% | 基准测试：当前 Get+Put 延迟 |
| 校验和吞吐量 CRC32C vs MD5 | ≥2x | Go 基准测试 |
| SSE-C 请求级加密开销 | ≤单个 AES-GCM 操作开销 | `crypto/cipher` 基准测试 |
| 生命周期转换延迟（S3 内部） | ≤S3 CopyObject 延迟 | S3 API 延迟测量 |
| 迁移 0025 回滚 | 从 `lock_mode` 列 100% 恢复 | 迁移测试：应用 + 回滚 + 验证 |

---

## 总结表

| 方向 | 我的优先级 | 架构影响 | 主要风险 | 关键设计决策 |
|----------|---------|--------------|-----------|--------------|
| 方向 A：Copy | **P0** | 中等。接口新增 + 回退逻辑 | 跨后端边界情况 | 条件头归属：Service vs Storage 层 |
| 方向 B：锁模式 | **P1** | 中等。迁移 + 检查分支 + 策略 | 合规回退 | Governance→Compliance 升级策略 |
| 方向 C：校验和 | **P1** | 低。增量，无迁移 | 尾标在 Go 中 | 存储：元数据 vs DB 列 |
| 方向 D：SSE-S3/KMS | **P1** | 低-中。头解析 + put options | 错误配置 | 传播现有密钥 |
| 方向 D：SSE-C | **P1/P2** | 高。新的临时加密路径 | 密钥安全 | 密钥必须永不持久化；使用 `EphemeralEncrypter` |
| 方向 E：生命周期转换 | **P2** | 高。路由引擎 + 转换 workder | 跨后端复杂性 | 原子性：转换失败 → 保留 + 重试 |
| 方向 F：路由引擎 | **P2（新）** | 很高。新的架构层 | 状态空间 | 确定性规则，非通用引擎 |
| 方向 G：可观测性 | **P1（新）** | 低。trace 传播 | 无风险 | Job handler 中的 traceparent 提取 |

文档的五个方向是合理的，且优先级排序正确。两个新方向（F：路由引擎 和 G：可观测性相关性）解决了架构盲点，这些盲点在数据增加到 PB 级或租户数量增长到数百个时，将成为限制性约束。
