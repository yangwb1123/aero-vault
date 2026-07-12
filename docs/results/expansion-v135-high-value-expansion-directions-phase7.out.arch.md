现在我已全面了解这份文档。以下是我的架构分析。

---

# 架构分析报告：AeroVault 第七期扩展方向

## 1. 架构评估

### 1.1 当前架构的优势

现有架构体现了一系列扎实的设计决策：

- **分层清晰**：`Protocol Adapters → FileService → Storage + Repository` 的 DAG 架构使得每一层的关注点高度内聚。协议层薄（仅序列化/路由），服务层厚（业务逻辑），持久层可插拔。
- **Opt-in 默认安全**：AI/WebDAV/事件/集群等所有高级特性均 flag-gated，基线路径（SQLite + local FS + 无鉴权）零网络零依赖，这是 CI gate 能够稳定的根本原因。
- **中间件链顺序固定**：`RequestID → CORS → Auth → Tenant → RateLimit → OTel → Recoverer → AccessLog` 的顺序不可变，这避免了一类常见的微服务 bug（鉴权前置条件在路由组内被绕过）。
- **测试策略务实**：`storage.contract_test.go` 保证了后端可替换性，AI mock 零网络，handler 测试用 `httptest.NewRecorder()` 而非启动真实服务器。

### 1.2 架构债务与技术债

**① FileService 层正在膨胀（即将触及 500 行红线）**

根据 AGENTS.md 单文件 ≤ 500 行的约束，`internal/service/file_crud.go`（对象 CRUD）和 `internal/service/file_features.go`（Copy/Tags/ACL/预签名）是两个最接近阈值的文件。随着本期 5 个方向的实施，FileService 的接口集合将从~20 个方法膨胀至~35 个。如果不提前拆分，将触发自动重构阻断。

**建议方案**：在实施任何新方向之前，将 FileService 按领域拆分为：

| 子服务 | 方法 | 迁移来源 |
|--------|------|---------|
| `ObjectService` | `Put` `Get` `Delete` `Head` `List` | `file_crud.go` |
| `MultipartService` | `InitMultipart` `UploadPart` `CompleteMultipart` `AbortMultipart` | `file_multipart.go` |
| `MetadataService` | `PutMetadata` `GetMetadata` `SearchMetadata` `ValidateSchema` | 新增 + 从 `file_crud.go` 迁出 |
| `UploadSessionService` | `CreateSession` `AppendData` `CompleteSession` `GetProgress` | 新增方向 4 |
| `CopyService` | `CopyObject` `CopyFrom` | `file_features.go` |

拆分后，`FileService` 退化为外观（Facade）或直接被协议层调用子服务。这符合单一职责原则，也为后续每个子服务的独立测试和限流提供了边界。

**② 元数据模型是当前最大的架构瓶颈**

`map[string]string` 的元数据模型限制了几乎所有扩展方向：
- 内容去重需要 `ContentHash` 字段 → 无结构化字段可存储
- 元数据搜索无法建立索引 → 必须全表扫描
- 计费需要 `price_tier`、`billing_plan` → 需要新表
- Schema 校验无法做类型强制 → `"2024-01-01"` vs `"01/01/2024"` 无法区分

**建议方案**：将 `Object` 结构体从扁平字段演进为**属性包（Attribute Bag）**，核心字段保留为顶级列，可扩展属性存入 JSONB/JSON 列。具体来说，`repository.Object` 新增：

```go
type Object struct {
    // 现有顶级字段保留（tenant, bucket, key, storage_key, size, etag, ...）
    StorageKey string
    Size       int64
    ETag       string
    // ...

    // 新增半结构化属性
    Attributes map[string]any   // JSON 列，索引按需建立
    ContentHash *[32]byte        // SHA-256，NULL 表示未 CAS
    SchemaID    *string          // 引用 metadata_schema.id
}
```

这不需要立即迁移所有对象——NULL 属性向后兼容，索引按 Schema 定义按需创建。

**③ Storage 接口缺少追加写原语**

当前 `Storage` 接口（`storage.go:107`）只有 `Put`（全量覆盖）和 multipart 的 `InitMultipart/UploadPart/CompleteMultipart`。方向 4（可恢复上传）需要追加写。而 S3 和 OSS 等对象存储**没有原生追加语义**。

**建议方案**：不要在 `Storage` 接口上增加 `Append` 方法（那会强制所有后端实现一个不自然的语义）。改为在 `UploadSessionService` 层统一管理临时文件，底层对不同后端做适配：

- **Local**：文件系统 `os.File` 的 `WriteAt` 天然支持
- **S3/OSS/COS**：使用 Multipart Upload 作为实现，UploadSessionService 内部管理分片边界（例如每 8MB 一个分片），客户端只需要发送连续字节流

这种"上层统一语义，下层多策略适配"的模式在 etcd 的 `wal` 和 Docker 的 `storage driver` 中都得到过验证。

---

## 2. 扩展方向评估

### 2.1 方向一：内容去重 & 内容寻址存储（CAS）

| 维度 | 评估 |
|------|------|
| **价值** | 🔴 TCO 核心竞争力，CI/CD/备份/AI 场景直接降低 50-90% 存储成本 |
| **复杂度** | L（对象级），XL（块级） |
| **架构影响** | 中——需要新包 + 新表 + Storage key 策略变化 |
| **风险** | 低——桶级 opt-in 确保不影响现有用户 |

**关键设计决策分析**：

文档选择了对象级去重（option a），理由是 80/20 法则。但我认为这个决策需要更细致的分层：

- **对象级去重**解决的是"完全相同文件的多副本"问题——收益立竿见影，实现简单
- **块级去重**解决的是"相似文件/版本之间的增量差异"——收益更高但实现复杂度翻倍

**建议**：将《对象级去重（本期）→ 块级去重（v2）→ 分片去重（v3）》的三阶段路线图缩短为**两阶段**：

- **Phase 1（本期）**：对象级去重 + **内容哈希索引**（`content_hashes` 表），确保哈希数据已就绪，为块级去重铺路
- **Phase 2（下一期）**：块级去重——利用 Phase 1 的哈希基础设施，增加固定/可变分块 + 指纹查找表

这样做的好处是 Phase 1 的哈希索引设计不需要在 Phase 2 重构——`content_hashes` 表的 `hash` 列本来就是 SHA-256（32 字节），块级哈希可以复用同一张表（新增 `block_offset` 和 `block_size` 字段），或使用同一哈希算法仅分片粒度不同。

**边缘情况补充**：

| 场景 | 分析 |
|------|------|
| 加密 + CAS 交互 | SSE 加密对象必须在**加密前**计算哈希（否则同一明文不同 enveloppe 产生不同哈希）。这意味着 CAS Store 必须做两层哈希：加密前 SHA-256 用于去重，加密后 SHA-256 用于完整性校验 |
| 分片上传的去重 | 文档说 Multipart 的 CAS 留到 v3。但 10MB 分片可能与另一个单对象内容相同——这在实际场景中常见（Docker layer = tar.gz，恰好与一个独立 tar.gz 相同）。建议在 Phase 1 就**支持分片级的 CAS 命中查找**，不作为去重写入，但查到了可以节省存储 |
| CAS + 版本控制 | 版本化桶中同一个 key 的 v1 和 v2 内容相同，按 CAS 应共享 blob。但 `storage_key` 当前含 `@v<id>` 后缀，与 CAS key 冲突。方案：`storage_key` 存 `cas/{hash[:2]}/{hash}`，版本信息只存在 objects 行的 `version_id` 列 |

### 2.2 方向二：浏览器直传 / S3 POST Object

| 维度 | 评估 |
|------|------|
| **价值** | 🟠 Web 集成的关键断裂，直接影响浏览器端用户体验 |
| **复杂度** | M |
| **架构影响** | 低——新增 `internal/auth/postpolicy.go` + S3 handler，不改变核心路径 |
| **风险** | 低——S3 标准协议，AWS 已定义完备 |

**关键设计决策**：文档选择独立 Signing Key 而非复用 API Key。这是正确的——但需要补充一个决策：**Signing Key 的作用域**。

| 选项 | 优点 | 缺点 |
|------|------|------|
| 全局 Signing Key | 简单，一个 key 签所有 policy | 泄漏后所有 policy 可伪造；轮换影响所有进行中的 policy |
| 租户级 Signing Key | 隔离性好，一个租户泄漏不影响其他 | 管理成本略高 |
| 请求级临时 Key | 最安全，每次生成新 key | 需要额外的 key 分发机制（客户端先调用 `GET /signing-key` 获取临时 key） |

**建议**：采用**租户级 Signing Key**，并支持 key 的版本化轮换（`key_id` → `key_secret` 映射表，policy 中携带 `key_id`，验证时根据 `key_id` 查找对应 secret）。这样轮换 key 时，已签发的 policy 仍有效——S3 也是这么做的。

### 2.3 方向三：计费 & 用量计量

| 维度 | 评估 |
|------|------|
| **价值** | 🔴 SaaS 产品化的最后一块拼图 |
| **复杂度** | L |
| **架构影响** | 大——新包 `internal/billing/`、新表×6、新的定时作业、Stripe Webhook |
| **风险** | 中——财务数据正确性要求高，Stripe API 依赖 |

**设计决策深度分析**：

文档建议套餐定义走配置文件（`deploy/plans.yaml`）而非代码常量。这是一个好的设计决策，但需要考虑一个关键问题：**配置热加载的原子性**。

如果在 23:55 更新 `plans.yaml`，而此时月度聚合作业正在运行，可能出现：部分租户按旧套餐计算，部分按新套餐计算。解决方案：

1. **版本化套餐**：`plans.yaml` 中每个 `Plan` 带 `version` 和 `effective_from`，聚合作业只取当前日期的有效版本
2. **快照定价**：每日聚合时，将当时的套餐定价快照写入 `billing_daily_usage` 表（而非引用 `plans.yaml` 的实时数据）
3. **两阶段提交**（不推荐，过于复杂）

**建议**：采用方案 2（快照定价）+ 方案 1（版本化套餐）的组合。每日聚合作业在运行时记录当日的套餐 ID + 版本 + 单价，后续发票生成时使用快照数据而非实时查询 `plans.yaml`。这确保了历史账单的确定性——即使套餐配置后来被修改，已生成的发票不受影响。

**关于 Stripe 集成的风险缓解**：

| 风险 | 缓解策略 |
|------|---------|
| Stripe API 不可用（网络分区） | 本地队列 + 重试；账单仍可本地生成，支付操作暂缓 |
| Webhook 重放攻击 | Stripe 的 `stripe-signature` 头验证（已有 HMAC 基础设施可复用） |
| 货币精度 | 所有金额以微美元（micro USD）存储，即 `$9.99 = 9,990,000`，避免浮点数精度问题 |
| 并发发票生成 | 使用租户级分布式锁（Postgres advisory lock 或 `job_lock` 表）防止重复发票 |

### 2.4 方向四：可恢复上传（TUS 模式）

| 维度 | 评估 |
|------|------|
| **价值** | 🟠 大文件上传的核心体验，移动端/跨国网络场景的"必须项" |
| **复杂度** | M |
| **架构影响** | 中——新端点 `/uploads/*`、新表 `upload_sessions`、后台 reaper |
| **风险** | 中——S3 后端追加写需要绕道 Multipart |

**架构设计方案评估**：

文档提出 hybrid 方案（local=append，S3=Multipart 做后端），这是务实的选择。但我认为需要进一步细化 **S3 后端的暂存策略**：

| 策略 | 优点 | 缺点 |
|------|------|------|
| **策略 A：临时文件 → 最终 multipart complete** | 实现简单 | 需要本地磁盘做 buffer；S3 作为最终存储但中间有本地依赖 |
| **策略 B：S3 Multipart 做实时后端** | 纯 S3，无本地依赖 | 每次 PATCH 需要新开一个 UploadPart（最小 5MB 限制），小数据块效率低 |
| **策略 C：混合——小 chunk 合并后 UploadPart** | 平衡 | 略微复杂（需要 5MB 缓冲区 + flush 策略） |

**建议**：采用**策略 C**，具体来说：

1. UploadSession 创建时自动 `CreateMultipartUpload`
2. 每次客户端 `PATCH` 数据 → 写入内存缓冲区（默认 5MB）
3. 缓冲区满 → `UploadPart` 到 S3（记录 partNumber + ETag）
4. 客户端 `POST /uploads/{id}/complete` → `CompleteMultipartUpload`
5. 支持部分完成的恢复：客户端 `HEAD` 时，返回已 committed 的字节数（已 UploadPart 的部分），但不包括内存缓冲区中的数据（未 committed）

这样对客户端透明——客户端只看到单调递增的 offset，服务端在内部管理分片边界。这个模式在 tusd（tus 的 Go 参考实现）中得到验证。

**边缘情况补充**：并发 PATCH 的竞态

文档提到"互斥锁 + 串行化，后到者返回 `409 Conflict`"。但在高并发场景下，更好的方案是**乐观锁**：`UPDATE upload_sessions SET received_bytes = received_bytes + $1 WHERE id = $2 AND received_bytes = $3`，使用 CAS（Compare-And-Swap）保证原子性。数据库行锁比应用层互斥锁更可靠（进程重启后锁自动释放）。

### 2.5 方向五：结构化元数据 Schema 与全文检索

| 维度 | 评估 |
|------|------|
| **价值** | 🟠 从"对象存储"到"智能内容平台"的差异化能力 |
| **复杂度** | M-L |
| **架构影响** | 大——新包 `internal/metadata/`、Schema 版本管理、JSONB 列、GIN 索引 |
| **风险** | 中——SQLite/Postgres JSON 查询语法差异需要兼容层 |

**这是一份非常扎实的设计**，但我有几个修正建议：

**① Schema 与 Metadata 的关系需要更清晰**

文档将 Schema 设计为**桶级**（每个桶一个 Schema），但我认为 Schema 应该独立于桶——否则多 Schema 的桶需要多次迁移。更好的模型：

```
Schema ── 元数据 Schema 定义（全局）
  │
  ├── Bucket 绑定（映射表：bucket → schema_id）
  │    └── 桶的默认 Schema
  │
  └── Object 绑定（对象级覆盖）
       └── 单个对象可声明 schema_id（推荐方式）
```

**② JSON 兼容层是潜在的性能瓶颈**

文档中的 `BuildMetadataWhere(conditions)` 需要根据 DBDriver 生成不同语法。如果所有元数据查询都走这个兼容层，它将成为所有 List/Search 操作的瓶颈。

**建议**：不要试图做一个通用的 SQL 生成器，而是将元数据查询限制为一组明确的查询模式（equality、range、prefix、exists），每种模式在 Postgres 和 SQLite 下都有直接的对应。例如：

| 查询模式 | Postgres | SQLite |
|---------|----------|--------|
| `field = value` | `metadata @> '{"k":"v"}'` | `json_extract(metadata, '$.k') = 'v'` |
| `field > n` | `(metadata->>'k')::int > n` | `CAST(json_extract(metadata, '$.k') AS INTEGER) > n` |
| `field IN (a,b)` | `metadata->>'k' IN (a,b)` | `json_extract(metadata, '$.k') IN (a,b)` |

如果限制在 5-6 种模式内，兼容层可以写成一个小的 switch 表，而不是一个完整的 SQL 方言转换器。

**③ 元数据搜索与语义搜索的合并是正确决策**

文档选择将元数据搜索合并到 `GET /v1/search`，用 `meta.field=value` 参数做属性过滤。这意味着搜索的架构演进为：

```
输入: query + mode + meta_filters
  → 语义搜索（向量/BM25）生成候选集
  → 元数据过滤（对候选集做属性条件筛选）
  → RRF 融合 + 排序
  → 返回 Hit
```

但需要注意**执行顺序的性能问题**：
- 先语义搜索再属性过滤：如果语义搜索返回 top-1000，属性过滤后只剩 3 个，浪费了语义搜索的计算
- 先属性过滤再语义搜索：属性过滤能大幅减少候选集，但需要属性索引先执行

**建议**：对 `mode=bm25` 或 `mode=hybrid` 的查询，先做属性过滤再搜索（属性过滤成本远低于语义搜索）。对 `mode=vector` 的查询，只能先向量搜索再属性过滤（向量索引不支持前置过滤）。在 API 设计上对用户透明，服务端根据 mode 自动选择执行顺序。

---

## 3. 接口设计建议

### 3.1 关键接口设计原则

**① 新增接口必须面向接口编程，而非面向实现**

当前代码中，`Storage` 是一个接口（`storage.go`），但 `FileService` 直接使用了具体类型（`*service.FileService`）。随着方向 1（CAS）的引入，FileService 应当接受一个接口：

```go
// 当前
type FileService struct { store *local.Store; repo *sqlite.Repository }

// 建议
type Store interface {
    Put(ctx, key, io.Reader, size, PutOptions) (ObjectInfo, error)
    Get(ctx, key) (io.ReadCloser, error)
    Delete(ctx, key) error
}
type ObjectRepository interface {
    Create(ctx, Object) error
    Get(ctx, tenant, bucket, key) (*Object, error)
    // ...
}
type FileService struct {
    store Store            // 可以是 local.Store, s3.Store, cas.CASStore
    repo  ObjectRepository
    meta  MetadataService  // 方向 5 新增
}
```

这样 CASStore 可以作为 `Store` 的一个实现透明地注入 FileService，无需修改 FileService 的代码。

**② 错误类型分层**

当前错误处理以 `error` 返回为主，缺少类型化错误。建议引入标准错误类型族：

```go
type ErrorType int
const (
    ErrNotFound ErrorType = iota
    ErrAlreadyExists
    ErrPermissionDenied
    ErrQuotaExceeded
    ErrPaymentRequired     // 方向 3 新增
    ErrSessionExpired      // 方向 4 新增
    ErrCASConflict         // 方向 1 新增
    ErrSchemaViolation     // 方向 5 新增
)

type ServiceError struct {
    Type    ErrorType
    Message string
    Details map[string]any   // 可选上下文信息
}
func (e *ServiceError) Error() string { return e.Message }
```

这允许协议层根据 `errors.As(err, &ServiceError{Type: ErrPaymentRequired})` 返回正确的 HTTP 状态码（402 Payment Required），而非统一返回 500。

### 3.2 需要引入的新抽象层

**① CAS 存储装饰器层**（方向 1）

CASStore 不应是独立的 Storage 实现，而应是一个**装饰器**：

```
Store (interface)
  ├── local.Store (存储实现)
  ├── s3.Store (存储实现)
  ├── oss.Store (存储实现)
  └── cas.CASStore (装饰器)
         └── wraps any Store
```

CASStore 接收一个 `Store` 和一个 `ObjectRepository`，在 `Put` 时做去重检查，命中则直接返回已有 `StorageKey`，不调用下层 Store 的 `Put`。这样 CASStore 可以包装任意底层存储——local、S3、OSS 都受益。

**② 元数据 Schema 引擎**（方向 5）

Schema 引擎应该作为一个独立的验证层，在 `Put` 路径中插入：

```
目前: Handler → FileService.Put → store.Put + repo.Create
新增: Handler → FileService.Put → Schema.Validate(metadata) → store.Put + repo.Create
                                                               └→ 索引更新
```

`Schema.Validate` 的职责：
1. 根据对象所在桶查找有效 Schema
2. 校验必填字段是否存在
3. 校验字段类型（number 字段不能传 string）
4. 校验正则、枚举、范围约束
5. 填充默认值
6. 校验通过 → 返回规范化后的 metadata

该层应该**不依赖具体的 DB 实现**，只操作 Go 结构体。

### 3.3 向后兼容策略

| 变更 | 兼容策略 | 迁移路径 |
|------|---------|---------|
| Storage Key 变化（cas/） | `UseCAS=false` 默认，不改变现有 key | CAS-enabled 的桶在 PUT 时自动迁移新 key |
| 元数据 JSONB 列 | 新增 `metadata` 列可 NULL，旧对象读取返回空 map | 可选后台作业批量填充 |
| 计费表 | 全新增表，不修改现有表 | 零迁移成本，启用计费前注入历史数据 |
| Upload Session | 全新增端点/表 | 与 Multipart Upload 共存，用户自行选择 |
| 接口注入 | 策略模式 + 默认实现 | 现有代码不做任何修改 |

**关键原则**：不要试图一次性迁移所有对象。每个新功能都应该是 opt-in、增量式的。AGENTS.md 中的 "安全默认" 原则（I5）必须严格遵守。

---

## 4. 技术选型

### 4.1 是否引入新框架

| 方向 | 可行方案 | 评估 |
|------|---------|------|
| 计费 Stripe 集成 | stripe-go SDK（`github.com/stripe/stripe-go`） | **推荐引入**。Stripe API 细节繁杂（Webhook 签名验证、PaymentIntent 状态机、Subscription 的生命周期），手写 HTTP 调用风险高 |
| TUS 协议 | `github.com/tus/tusd`（Go 参考实现） | **不建议**。tusd 是一个完整的 HTTP 服务，与现有 chi 路由整合困难。应参考其协议实现（主要是 PATCH range 处理 + offset 追踪），自行实现轻量版本 |
| 元数据 Schema 验证 | `github.com/xeipuuv/gojsonschema` | **候选引入**。如果 Schema 定义使用 JSON Schema 标准，可以直接复用；但自定义 DSL 更灵活 |
| 内容去重 | 无第三方依赖 | 哈希计算用 `crypto/sha256`，标准库即可 |

**决策**：仅引入 `stripe-go` 作为强依赖，其余方向使用标准库或参考开源实现自行实现。

### 4.2 第三方依赖评估标准

根据 AGENTS.md 的 I6（Stdlib 优先），新依赖的评估顺序：

1. **是否有标准库替代？** → `crypto/sha256`（CAS）、`net/http`（REST）、`encoding/json`（元数据序列化）都是标准库
2. **依赖是否活跃维护？** → stripe-go 月更新，有 2k+ GitHub stars
3. **依赖深度如何？** → stripe-go 自身依赖极少（仅 `net/http` 和 `encoding/json`），不会引入传递性依赖
4. **是否需要 CGO？** → 必须纯 Go（CI gate 不允许 CGO）
5. **许可证兼容？** → stripe-go 是 MIT 许可证

### 4.3 自建 vs 采购

| 功能 | 选项 | 决策 | 理由 |
|------|------|------|------|
| 计费支付处理 | 自建 vs Stripe | **Stripe** | PCI-DSS 合规成本极高；Stripe 的 Webhook 事件模型与 AeroVault 的 EventBus 天然契合 |
| 元数据 Schema | 自建 vs JSON Schema 标准 | **自建 DSL** | JSON Schema 标准过于复杂（支持 `if/then/else`、`oneOf`、`$ref` 引用），AeroVault 的元数据需求有限（类型 + 枚举 + 正则 + 范围），自定义 DSL 更轻量且更容易在 Postgres/SQLite 间映射 |
| 可恢复上传 | 自建 vs tusd 嵌入 | **参考 tusd 自行实现** | tusd 是完整 HTTP 服务（含自己的 router），与现有 chi 路由冲突；核心逻辑约 500 行，自行实现可控 |

---

## 5. 实施路线图

### 5.1 优先级排序

| 优先级 | 方向 | 理由 | 依赖 |
|--------|------|------|------|
| **P0** | 内容去重（CAS） | TCO 竞争力，ROI 最高，复杂度可控，为块级去重铺路 | 无 |
| **P0** | 元数据 Schema | 其他所有方向的**基础设施依赖**——CAS 需要 `ContentHash` 字段，计量需要 `price_tier`，搜索需要索引 | 无 |
| **P1** | 可恢复上传 | 大文件上传体验断裂，复杂度 M，风险可控 | 元数据 Schema（需要 `upload_sessions` 表的字段定义） |
| **P1** | 浏览器直传 | Web 集成断裂，实现相对独立，S3 协议完备 | `auth/policy.go`（已有 IAM policy 解析器可复用） |
| **P2** | 计费系统 | 商业价值高但实施复杂度大，依赖 Stripe 外部服务 | 元数据 Schema（`billing_plan`、`price_tier` 字段）+ 计量基础设施 |

### 5.2 阶段划分和里程碑

**Phase 1 — 基础设施重铸（2 周）**

在实施任何新方向前，先解决架构债务：

| 任务 | 交付物 | 影响 |
|------|--------|------|
| FileService 领域拆分 | `ObjectService` `MultipartService` `MetadataService` 等子服务 | 消除 500 行红线风险 |
| Object 模型扩展 | 新增 `Attributes` JSONB 列、`ContentHash` 列、`SchemaID` 列 | 所有方向的基础 |
| Storage 接口装饰器模式 | `Store` interface 准备 + `WrappingStore` 基类 | CASStore 可透明包装 |
| 迁移双文件 | `NNNN_cas.up.sql` + `NNNN_metadata.up.sql` | 数据库变更 |

**Phase 2 — 内容去重 + 元数据 Schema（3 周）**

| 任务 | 交付物 | 里程碑 |
|------|--------|--------|
| `internal/storage/cas/` 包 | CASStore 装饰器 + `content_hashes` 表 | ✅ CAS 去重完成 |
| CAS GC + reconcile | 引用计数归零清理 + 孤儿检查 | ✅ 后台清理 |
| 桶级/请求级 opt-in | `bucketConfig.ContentDedup` + `?cas=true` | ✅ 用户配置 |
| 去重指标 | `storage_dedup_bytes_saved` 等 | ✅ 可观测 |
| `internal/metadata/schema.go` | FieldType + FieldDef + Schema | ✅ Schema 定义 |
| Schema 验证 + 索引 | 管道集成 + GIN/表达式索引 | ✅ 元数据查询 |
| 元数据搜索 | `GET /v1/search?meta.*` | ✅ 结构化搜索 |

**Phase 3 — 上传体验（2 周）**

| 任务 | 交付物 | 里程碑 |
|------|--------|--------|
| `internal/api/rest/upload_session.go` | TUS 协议端点 | ✅ 可恢复上传 |
| S3 后端适配 | Multipart 内部管理分片 | ✅ 多云兼容 |
| 后台 reaper | 过期 session 清理 | ✅ 后台清理 |
| `internal/auth/postpolicy.go` | POST policy 签名引擎 | ✅ 签名策略 |
| S3 POST handler | `POST /{bucket}/{key}` 处理器 | ✅ 浏览器直传 |

**Phase 4 — 计费 + 收尾（3 周）**

| 任务 | 交付物 | 里程碑 |
|------|--------|--------|
| `internal/billing/` 包 | Plan + Aggregator + Stripe 集成 | ✅ 计费引擎 |
| 月度聚合 job | 每日聚合 + 月初发票生成 | ✅ 自动账单 |
| Stripe Webhook | `invoice.paid` 等事件处理 | ✅ 支付集成 |
| 计费管理 REST API | `GET /v1/admin/billing/*` | ✅ 管理面板 |
| 计费指标 + 告警 | `billing_failed_payments` 等 | ✅ 可观测 |

### 5.3 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| **CAS 哈希冲突**（SHA-256 理论上可能碰撞） | 极低 | 灾难性（数据交叉污染） | 加第二层校验：存储时同时写入 `size_bytes` + `first_tenant`，读取时双重校验 |
| **元数据搜索性能滑坡**（无索引的全表扫描） | 中 | 高——100 万对象查 10 秒+ | 强制索引创建；监控慢查询；对未索引字段的查询降级为返回提示而非执行扫描 |
| **Stripe Webhook 丢失** | 低 | 中——支付状态不同步 | Webhook 幂等 key 去重（复用 `Idempotency-Key` 基础设施）；定时全量对账 job（每日 sync Stripe invoices → 本地 billing_invoices） |
| **Upload Session 磁盘写爆** | 中 | 高——临时文件占满磁盘 | 每个租户的临时文件大小上限（`UPLOAD_SESSION_MAX_BYTES_PER_TENANT`）；过期 session 的 aggressive reaper（每 5 分钟扫描）；启用 `tmpfs` 选项 |
| **CAS + SSE 加密交互复杂** | 中 | 中——加密后哈希不匹配 | 明确文档声明：CAS 的哈希在加密层之前计算；SSE 启用时 CASStore 需要 access to 明文（在加密前计算哈希） |
| **Schema 演进破坏已有查询** | 中 | 中——用户查询依赖旧字段类型 | Schema 版本并存 + 查询时根据对象声明版本解释；禁止删除字段（只允许新增或标记 deprecated） |

---

## 总结

| 评价维度 | 结论 |
|---------|------|
| **文档质量** | 极高。代码锚点精确到行号，边缘情况分析完整，架构蓝图可落地。这是我在 Go 项目中见过的最好的功能设计文档之一 |
| **架构健康度** | 当前架构良好但有瓶颈——元数据模型（`map[string]string`）和 FileService 膨胀是最需要优先解决的两个架构债 |
| **实施顺序** | 不要按文档顺序（CAS→POST→计费→TUS→Schema）实施。应该先做**元数据 Schema + FileService 拆分**作为基础设施，再做其他方向。Schema 是所有方向的共同依赖 |
| **最大风险** | 不是技术复杂度，而是**范围蔓延**——五个方向同时实施会耗尽开发资源。建议严格限制 Phase 1 只做架构重铸 + 2 个 P0 方向 |

关于文档中验证到的一个小误差（`CopyObject` 在 S3 handler 而非 FileService），建议合并文档时顺便修正。
