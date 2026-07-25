# AeroVault 深度架构盲区与扩展方向（第 90 轮）

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — `cmd/server/main.go` + `internal/` 全部 24+ 子包、三套 SDK、MCP 双模式、Web UI、~50 对迁移文件、`deploy/` 全套配置、`HARNESS.md`、`AGENTS.md`  
> **去重验证：** 对 `docs/requirements/` 下全部 89 份既有分析文档逐方向进行关键词正则 + 语义交叉验证  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。选取**代码中存在明确锚点、有实质性产品/架构影响、且在 89 轮分析中未被深度独立覆盖**的方向。每个方向包含：现象与代码证据 → 产品价值 → 架构权衡 → 边界情况。

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 | 既有分析覆盖 |
|---|------|------|--------|---------|---------|-------------|
| **1** | **对象合规锁模型深度：Governance/Compliance 模式区分与独立 Legal Hold** | 合规/协议完备 | **P1** — `locked_until` 单一时间戳无法满足 SEC 17a-4、FINRA、GDPR 等法规对保留模式（Governance 可绕过 vs Compliance 不可绕过）和 Legal Hold（独立于保留期的永久标记）的区分要求 | `internal/repository/repository.go:Object.LockedUntil`（仅 `*time.Time`，无 `RetentionMode` 字段）；`internal/service/file_crud.go:checkLockBeforeOverwrite`（仅检查时间，不检查模式）；`internal/repository/sql_tags_acl.go:SetLockedUntil`（只写时间）；`internal/api/s3compat/handler.go:dispatchObjectLock`（处理 `?retention` 和 `?legal-hold` 但未区分模式）；`internal/storage/encrypt.go`（SSE envelope 无保留模式元数据） | v66 方向三以概念方向列出但描述为"零实质性架构分析"；v16 方向五标题含"锁模式治理"但聚焦 S3 子资源 API 端点完整性，非保留模式语义与权限模型 |
| **2** | **S3 UploadPartCopy 与大对象服务端拷贝能力** | 协议完备/性能 | **P1** — `CopyObject` 将源对象全部读入内存再 PUT，>5GB 对象直接失败；无 `UploadPartCopy` 意味着 SDK/工具对大对象拷贝退化到单线程全量下载再上传，浪费网络带宽与 I/O | `internal/api/s3compat/extra.go:39-66`（`copyObject` → `svc.Get` 全量读内存 → `svc.Put` 全量写）；`internal/api/s3compat/extra.go`（无 `UploadPartCopy` 路由或 handler）；`internal/storage/storage.go:MultipartInit/UploadPart/CompleteMultipart`（multipart 基础能力存在但无 copy-from-existing 语义）；`internal/service/file_multipart.go`（无服务端分片拷贝逻辑） | v19 方向表一行列出 UploadPartCopy 缺失；v25 方向五以 gap 表格列出 UploadPartCopy 但聚焦 SDK 兼容性断裂，**无代码锚点驱动的实施架构分析**；v26/v27 仅表格路过提及 |
| **3** | **版本化对象生命周期深度治理：非当前版本过期、分片上传自动中止与版本数量上限** | 成本优化/存储效率 | **P2** — 版本化桶的旧版本无限堆积；中断的分片上传永久残留；生命周期引擎仅支持当前版本过期。三个 S3 标准生命周期规则类型均缺失，导致启用版本化后的存储成本无上限增长 | `internal/reconcile/lifecycle.go:handleExpiredObject`（仅处理当前版本过期——`soft_delete`/`hard_delete`，无 `NoncurrentVersionExpiration`、无 `AbortIncompleteMultipartUpload`）；`internal/repository/repository.go:BucketConfig`（仅有 `ExpireAfterDays` + `ExpireAction`，无 `NoncurrentDays`/`MaxVersions`/`AbortMPUDays`）；`internal/api/s3compat/bucketconfig.go:putBucketLifecycle`（解析 S3 lifecycle XML 时丢弃 `Transition`、`NoncurrentVersionExpiration`、`AbortIncompleteMultipartUpload` 元素）；`internal/storage/local_multipart.go`（InitMultipart 创建目录无 TTL） | v15 方向表一行列出"非当前版本过期"目标；v17 方向表三行列出三个缺失规则类型（NoncurrentVersionExpiration / NoncurrentVersionTransition / AbortIncompleteMultipartUpload）并附有高层级概念设计（~1.5 页），但**无基于当前代码引擎（reconcile/lifecycle.go）的完整实施路径分析、无版本数量上限约束设计、无与存储后端整合的数据移动策略** |
| **4** | **写入路径分布式补偿事务与一致性治理（Write Path Distributed Compensating Transaction）** | 数据一致性/韧性 | **P1** — `store.Put` → `repo.UpsertObject`（和 `store.Delete` → `repo.HardDeleteObject`）之间无事务边界，部分故障导致孤儿 blob 或幽灵元数据。现有 Reconciler 仅清除版本保留与软删除，不处理写入路径故障残留。需要从被动检测升级为主动预防 | `internal/service/file_crud.go:Put`（`store.Put` 成功 → `writePutObject` 失败 → 孤儿子 blob）；`internal/service/file_crud.go:hardDeleteObject`（`store.Delete` 成功 → `repo.HardDeleteObject` 失败 → 幽灵元数据）；`internal/service/file_multipart.go:CompleteMultipart`（`store.CompleteMultipart` 成功 → `saveMultipartObject` 失败 → 孤儿）；`internal/reconcile/job.go:scanAll`（仅扫描过期/软删除，无写入路径孤儿检测）；`internal/repository/sql_objects.go`（无 `ListOrphanStorageKeys` 或 `ListActiveObjects` 方法）；`internal/storage/local.go:List`（可列出所有存储 key，但 repository 侧无交叉验证逻辑） | v86 方向一完整分析了四种不一致状态（诊断阶段）但未给出补偿架构设计；v88 方向四以概念级设计了补偿事务模式（~2 页高层级设计）但**缺少与现有代码引擎（reconcile + storage.List + repository）的整合实施路径、缺少存储后端无关的通用孤儿检测算法、缺少对 POST 多步操作（multipart complete）的补偿策略** |
| **5** | **对象版本操作与历史演进管理（Object Version Operations: Revert, Diff & Timeline）** | 产品特性/数据管理 | **P2** — 版本化存储了每个对象的完整历史，但无版本回退（revert）、版本间变更对比（diff）、可视化时间线等用户交互能力。审计场景需要查看"谁在何时修改了什么"，当前 lineage 只追踪 AI 消费不追踪对象变更。Restore API 被重用于软删除恢复而非版本回退 | `internal/service/file_features.go:ListVersions`（返回版本列表但无 revert 语义）；`internal/service/file_features.go:GetVersion`（可读取旧版本但需应用层手动 PUT 实现"回退"）；`internal/service/file_features.go:RestoreObject`（仅清除 `deleted_at` 标志——非版本回退）；`internal/api/rest/handler.go:Restore`（`POST /v1/files/{key}/restore`——软删除恢复；无 `POST /v1/files/{key}/versions/{v}/revert`）；`internal/webui/static/index.html`（Web UI 无版本历史展示或操作）；`internal/repository/sql_objects.go:ListObjectVersions`（SQL 查询返回全部版本，无分页、无变更摘要）；`internal/api/rest/dto.go`（无 version diff/timeline 响应类型） | v16 方向表一行列出"版本回退"概念；v31 方向表一行列出 `Restore` 的 `source_detail.version_id` 字段概念；v41 仅路过提及"回滚到上一个版本"概念（聚焦配置回滚而非对象版本）。**版本历史可视化管理、版本间 diff、单版本 revert——从未被作为独立的产品方向分析** |

---

## 方向一：对象合规锁模型深度 — Governance/Compliance 模式区分与独立 Legal Hold

### 现状

当前对象锁的实现极为简单：一个 `*time.Time` 类型的 `LockedUntil` 字段，加一个 `_aero_legal_hold` 元数据键，在硬删除路径中做一次性检查。

```go
// internal/repository/repository.go:21-38 (Object struct)
type Object struct {
    // ...
    LockedUntil *time.Time  // present when Object Lock is active
    // 无 RetentionMode string
    // 无 LegalHold bool
}
```

```go
// internal/service/file_crud.go:290-300 (hardDeleteObject 中的锁检查)
if obj.LockedUntil != nil && obj.LockedUntil.After(time.Now()) {
    return fmt.Errorf("%w: object is under retention lock", ErrLocked)
}
if obj.Metadata["_aero_legal_hold"] == "ON" {
    return fmt.Errorf("%w: object is under legal hold", ErrLocked)
}
```

S3 Object Lock 定义了两种截然不同的保留模式：

| 模式 | 特点 | 当前是否支持 |
|------|------|-------------|
| **Governance** | 有 `s3:BypassGovernanceRetention` 权限的用户可覆盖 | ❌ — `locked_until` 一刀切，无人可绕过 |
| **Compliance** | **没有任何用户**（包括 root）可以缩短保留期或删除 | ❌ — 同上，无法实现不可逆保留 |
| **Legal Hold** | 独立于保留期的 on/off 标记，可随时设置/移除（有权限时） | ⚠️ — 通过 `_aero_legal_hold` 元数据键部分模拟，但无独立 API、无权限校验 |

S3 的实现逻辑：

```
PUT /{bucket}/{key}?retention
    <Retention>
        <Mode>GOVERNANCE|COMPLIANCE</Mode>
        <RetainUntilDate>2026-12-31T23:59:59Z</RetainUntilDate>
    </Retention>

PUT /{bucket}/{key}?legal-hold
    <LegalHold>
        <Status>ON|OFF</Status>
    </LegalHold>
```

当前代码中 `dispatchObjectLock` (`internal/api/s3compat/handler.go:374`) 可以路由到 `?retention` 和 `?legal-hold` 参数，但底层只写入了 `locked_until` 时间，丢弃了 `Mode` 信息，且 `SetLockedUntil` 接口不接收模式参数。

### 产品价值

| 用户画像 | 场景 | 当前能力 | 就绪后 |
|---------|------|---------|--------|
| 合规官 | 需要满足 SEC 17a-4 对电子记录不可改写的要求 | ❌ locked_until 可由任何有 Delete 权限的用户绕过（修改 metadata `_aero_legal_hold`） | Compliance 模式一旦设置，包括系统管理员在内的任何人都无法删除或缩短保留期 |
| 法务团队 | 诉讼中发现义务：对相关文档设置 Legal Hold，禁止任何删除 | ✅ `_aero_legal_hold` 元数据可实现 | 专门的 `PUT ?legal-hold` API + 审计日志记录谁何时设置/清除 Legal Hold |
| 数据管理员 | 需要对某个保留中的文档进行"合规的"覆盖操作（Governance bypass） | ❌ 当前锁不可绕过，只能等到期 | Governance 模式 + `s3:BypassGovernanceRetention` 权限 + 审计日志记录 bypass 操作 |
| 企业客户 | 审计需要证明"哪些对象受 Compliance 模式保护及其保留期" | ⚠️ 可通过 `ListObjectVersions`+`locked_until` 字段推断 | `GET ?retention` 返回完整保留信息（Mode + RetainUntilDate），可被合规审计工具扫描 |

### 架构权衡

| 方案 | 复杂度 | 影响范围 | 建议 |
|------|--------|---------|------|
| **`Object` 增加 `RetentionMode` 字段**（`""` | `"GOVERNANCE"` | `"COMPLIANCE"`），`LockedUntil` 改为 `RetentionUntil`；迁移增加列 | 低 | `repository.Object` + migration 0025 | ✅ **第一步** — schema 基础，不阻塞后续 |
| **Legal Hold 独立字段**：`LegalHold bool`，替代 `_aero_legal_hold` metadata hack | 低 | `repository.Object` + migration | ✅ **第一步** |
| **硬删除路径按模式决策**：Compliance 模式 → 任何人不可删；Governance 模式 → 检查 `s3:BypassGovernanceRetention` IAM action | 中 | `file_crud.go:hardDeleteObject` + `auth/policy.go` | ✅ **第二步** |
| **覆盖/修改保留期权限校验**：Governance 模式下缩短保留期需 bypass 权限；Compliance 模式禁止任何修改 | 中 | `SetLockedUntil` + `PutObjectRetention` handler | ✅ **第二步** |
| **Legal Hold API + 审计**：独立端点，设置/清除记录到 `audit_log` | 中 | `s3compat` + `rest` + audit | ✅ **第二步** |
| **通过 SSE envelope 保存合规元数据**：Compliance 模式的 retention 信息同时加密存储在 sidecar 中，防止 DB 被篡改后合规保证失效 | 高 | `storage/encrypt.go` + sidecar schema | ❌ **第三步** — 超出当前阶段需求 |

### 边界情况

| 场景 | 影响 | 处理策略 |
|------|------|---------|
| Compliance 模式对象所在存储后端故障，需要迁移到新后端 | 数据移动需要读取旧数据，Compliance 模式禁止任何形式的删除或覆盖 | 迁移操作应被视为系统管理操作而非数据删除——原对象标记为"compliant-moved"，新副本继承相同的 Compliance 属性 |
| Governance 模式对象设置后，管理员通过 `UpdateTags` 修改 `_aero_legal_hold` metadata | 绕过 Legal Hold 合规保护 | Legal Hold 须使用独立字段（非 metadata），修改需专门权限和审计记录 |
| 用户 A 设置 Governance 保留，用户 B 用 `s3:BypassGovernanceRetention` 绕过并删除 | 合规审计需要追溯谁 bypass 了保留 | 每次 bypass 写入 `audit_log`：`action=bypass_governance_retention, target=object_key, actor=user_b` |
| 桶默认 Object Lock 设置为 1 天，用户 PUT 时指定 30 天 Compliance 模式 | 需要决定桶默认值 vs 对象级覆盖的优先级 | S3 语义：对象级指定优先于桶默认值；桶默认值仅适用于 PUT 时未指定的情况 |
| 版本化桶中，新版本覆盖旧版本，旧版本保留 compliance 保护 | 旧版本 Blob 不可删除但新版本可见 | Compliance 模式作用于版本级别（非对象级别）：每个版本独立的 `RetentionMode` + `RetentionUntil` |

---

## 方向二：S3 UploadPartCopy 与大对象服务端拷贝能力

### 现状

当前的 `CopyObject` 实现：

```go
// internal/api/s3compat/extra.go:39-66
func (h *Handler) copyObject(w http.ResponseWriter, r *http.Request, dstBucket, dstKey, copySource string) {
    // ...
    rc, src, err := h.svc.Get(r.Context(), tenant, srcBucket, srcKey)  // ← 全量读入内存
    // ...
    dst, err := h.svc.Put(r.Context(), tenant, dstBucket, dstKey, rc, src.Size, opts)  // ← 全量写出
    // ...
}
```

这个实现有三个根本问题：

| 问题 | 触发条件 | 后果 |
|------|---------|------|
| **全量内存缓冲** | 任何大小的对象拷贝 | 500MB 对象需 500MB+ 内存；并发拷贝 → OOM |
| **>5GB 对象失败** | AWS SDK 使用 `CopyObject` 时自动回退到 `UploadPartCopy` | S3 标准要求 >5GB 必须用 multipart copy，当前直接失败 |
| **无条件请求支持缺失** | `x-amz-copy-source-if-match` / `x-amz-copy-source-if-none-match` / `x-amz-copy-source-if-modified-since` | S3 CopyObject 支持条件拷贝，当前完全不实现 |

AWS S3 的 UploadPartCopy 工作流：

```
1. POST /{bucket}/{key}?uploads               → uploadId
2. PUT /{bucket}/{key}?partNumber=1&uploadId={id}&x-amz-copy-source=/src/bucket/key
   → 从源对象拷贝字节范围 [0, 5MB) 作为 part 1
3. PUT /{bucket}/{key}?partNumber=2&uploadId={id}&x-amz-copy-source=/src/bucket/key
   &x-amz-copy-source-range=bytes=5MB-10MB   → part 2
4. POST /{bucket}/{key}?uploadId={id}         → complete
```

### 产品价值

| 用户画像 | 场景 | 当前体验 | 就绪后 |
|---------|------|---------|--------|
| 数据工程师 | 使用 `aws s3 cp s3://bucket/source s3://bucket/dest`（500MB 文件） | 内存暴涨 + 单线程全量传输 | 服务端 multipart copy — 零客户端网络传输、并发分片、<= 256MB/chunk |
| DevOps 团队 | CI/CD 管道中跨桶拷贝构建产物（6GB Docker 镜像） | HTTP 413 / OOM kill | 自动切换到 `UploadPartCopy`，5MB-5GB 每片，16 路并发 |
| SDK 用户 | 使用 AWS Go SDK `CopyObject` 输入 >5GB 对象 | 收到 `EntityTooLarge` 错误 | `CopyObject` 对 >5GB 对象自动内部转换为 `UploadPartCopy` |
| 平台工程师 | 需要拷贝对象时保留加密状态（SSE-C/SSE-KMS） | 全量下载再上传需要解密→重新加密 | 服务端拷贝保持加密端到端 |

### 架构权衡

| 方案 | 复杂度 | 影响范围 | 建议 |
|------|--------|---------|------|
| **`Storage` 接口增加 `CopyPart` 方法**：从源 key 的字节范围直接写入 multipart part（无需经 FileService 中转） | 中 | `storage/storage.go` + `local.go` + `s3.go` + `oss.go` + `cos.go` | ✅ **可复用存储后端的原生 copy 能力**：S3 后端可用 `CopyObject`/`UploadPartCopy` API，Local 用 `io.CopyN(os.File)` |
| **S3 handler：UploadPartCopy 路由**：`PUT /{bucket}/{key}?partNumber=N&uploadId=U&x-amz-copy-source=...` | 低 | `s3compat/extra.go` 新增 handler + router | ✅ **协议层面最直接的入口** |
| **`FileService.CopyByRange`**：封装存储层 `CopyPart`，处理租户/桶/key 校验 | 中低 | `service/file.go` + `service/file_crud.go` | ✅ **统一协议接入点** |
| **条件拷贝支持**：`If-Match`/`If-None-Match`/`If-Modified-Since` → `304/412` | 中 | `s3compat/extra.go:copyObject` + `service/file_crud.go` | ✅ **S3 协议完整性** |
| **CopyObject >5GB 自动降级**：`CopyObject` 检测到源 >5GB 时自动发起 multipart + UploadPartCopy 作为内部实现 | 高 | `service/file_multipart.go` 新增 `CopyObjectMultipart` | ❌ **第三步** — 超出初始需求，可留给 SDK 层处理 |

### 边界情况

| 场景 | 影响 | 处理策略 |
|------|------|---------|
| UploadPartCopy 指定 `x-amz-copy-source-range` 超出源对象范围 | S3 返回 `InvalidRange` (416) | 校验 `offset+length <= src.Size` |
| 源对象启用了 SSE-C（自定义加密密钥），目标段需要指定不同的 SSE-C key | 加密 key 不同 → 数据需解密再加密 | S3 要求源和目标 SSE-C key 一致或目标显式传入——复制到不指定 SSE 的目标时解密重新加密 |
| 并发 UploadPartCopy 覆盖同一个 part number | 最后写入的 part 生效，之前的被覆盖（S3 语义明确） | 不额外加锁——使用 S3 原生语义，与 `UploadPart` 行为一致 |
| 源对象在 copy 进行中被覆盖或删除 | 部分分片使用旧数据、部分使用新数据，最终对象损坏 | 读取源对象时获取其 `version_id`（或 `ETag`），校验每个 `CopyPart` 的源一致性（S3 不保证，但可以校验） |
| 目标桶启用了版本控制 | 每个 multipart complete 创建一个新版本 | 复用现有版本的 `storageKey` 逻辑——`storageKey + @v<versionID>` |

---

## 方向三：版本化对象生命周期深度治理

### 现状

当前 `LifecycleJob` 的实现非常简洁——只做一件事：

```go
// internal/reconcile/lifecycle.go:sweepExpired
func (l *LifecycleJob) sweepExpired(ctx context.Context) (soft, hard int) {
    expired, err := l.repo.ListExpired(ctx, 200)  // 仅查询 expired 对象
    // ...
    for _, obj := range expired {
        action := obj.Metadata["__expire_action"]
        if action == "hard_delete" { store.Delete(...); repo.HardDeleteObject(...)
        } else { repo.SoftDeleteObject(...) }
    }
}
```

S3 生命周期引擎定义了三种规则类型，AeroVault 只实现了第一种的一个子集：

| 规则类型 | S3 XML 元素 | AeroVault 状态 | 影响 |
|---------|------------|---------------|------|
| **Expiration** | `<Expiration><Days>N</Days></Expiration>` | ✅ 部分实现 — 仅当前版本过期；仅 `soft_delete`/`hard_delete` | 无法降冷（见 v87/v3 的分层存储方向） |
| **NoncurrentVersionExpiration** | `<NoncurrentVersionExpiration><NoncurrentDays>N</NoncurrentDays></NoncurrentVersionExpiration>` | ❌ **完全缺失** | 版本化桶旧版本无限堆积，存储成本无上限 |
| **AbortIncompleteMultipartUpload** | `<AbortIncompleteMultipartUpload><DaysAfterInitiation>N</DaysAfterInitiation></AbortIncompleteMultipartUpload>` | ❌ **完全缺失** | 中断的分片上传残留，存储泄漏 |

此外，缺少**版本数量上限**约束：

```go
// internal/repository/repository.go:40-50 (BucketConfig)
type BucketConfig struct {
    // ...
    Versioning        bool
    ObjectLockSeconds int
    ExpireAfterDays   int
    ExpireAction      string
    // 无 MaxVersions           int  // 每个 key 最多保留 N 个版本
    // 无 NoncurrentDays        int  // 非当前版本保留天数
    // 无 AbortMPUDays          int  // 分片上传自动中止天数
    // 无 TransitionRules       []TransitionRule
    // 无 NoncurrentVersionTransition
}
```

### 产品价值

| 用户画像 | 场景 | 当前能力 | 就绪后 |
|---------|------|---------|--------|
| 平台运维 | 版本化桶启用后，每个文件每次编辑都产生一个新版本，存储量每周翻倍 | ❌ 无自动清理机制，需手动维护 | 设置 `NoncurrentDays=30`：30 天前的旧版本自动清除 |
| 开发者 | 上传大文件时分片上传进程被中断，残留的分片目录永远不被清理 | ❌ 残留 `.multipart/` 目录需手动删除 | 设置 `AbortMPUDays=7`：初始化超过 7 天未 complete 的上传自动 abort |
| SaaS 运营 | 用户上传了 10000 个版本的文件，存储成本是该文件的 10000 倍 | ❌ 版本无限增长 | 设置 `MaxVersions=100`：超过 100 个版本时自动裁剪最旧的 |
| 合规审计 | 法规要求某些文档（如合同、审计日志）的主版本保留 7 年，旧版本保留 1 年 | ❌ 只有统一的 `ExpireAfterDays` | `NoncurrentVersionExpiration` + `Expiration` 组合规则精细控制 |

### 架构权衡

| 方案 | 复杂度 | 影响范围 | 建议 |
|------|--------|---------|------|
| **BucketConfig 增加治理字段**：`NoncurrentDays int`、`AbortMPUDays int`、`MaxVersions int`；迁移 0025 | 低 | `repository/repository.go` + `migrations/*/0025_*.sql` | ✅ **第一步** |
| **S3 lifecycle XML 解析增加规则类型**：`putBucketLifecycle` 解析 `NoncurrentVersionExpiration`/`AbortIncompleteMultipartUpload` | 中 | `s3compat/bucketconfig.go` + `xml.go` | ✅ **第二步** |
| **NoncurrentVersion 扫描**：`LifecycleJob` 新增 `sweepNoncurrent` 查询 `version_id IS NOT NULL AND created_at < cutoff` | 中 | `reconcile/lifecycle.go` + `repository/sql_objects.go` | ✅ **第二步** — 每个对象只保留最新的 N 个版本按 `updated_at DESC` |
| **AbortIncompleteMultipartUpload 扫描**：`LifecycleJob` 新增 `sweepAbandonedUploads` 查询超过 TTL 的 uploads | 中 | `reconcile/lifecycle.go` + `repository/sql_uploads.go` + `storage.AbortMultipart` | ✅ **第二步** |
| **MaxVersions 写入路径强制**：每次 `Put`（版本化桶）成功后，检查版本数并异步删除最旧版本 | 中高 | `service/file_crud.go:Put`（`InsertObjectVersion` 后）→ 异步 job | ❌ **第三步** — 可先通过 LifecycleJob 周期性扫描实现 |

### 边界情况

| 场景 | 影响 | 处理策略 |
|------|------|---------|
| `MaxVersions=5`，但 5 个版本中有 3 个处于 Compliance 锁状态 | 可删除的版本只有 2 个，无法裁剪到 5 以下 | 先删除非锁定版本；如果全部锁定，跳过该 key 并记录告警。`locked_until` 不超时不可删除 |
| `NoncurrentDays=30` 的版本在 29 天时被用户 GET（读取） | 访问时间应延长版本保留期？ | S3 语义：NoncurrentDays 从版本变为非当前版本的时间计算，不因读取重置 |
| `AbortMPUDays=7` 但分片上传处于 `UploadPart` 进行中（仍在活跃上传） | 正在上传的分片不应被 abort | 检查 `updated_at` 而非 `created_at`：超过 7 天**没有任何 part 上传活动**的上方才 abort |
| 用户通过 S3 API 设置了 `NoncurrentDays` 但系统之前没有该字段 | 从 `0` 开始，不会误删除旧版本 | 默认值 `0` = 不启用（与当前行为一致） |
| 并发冲突：一个版本刚被 LifecycleJob 标记为待删除，又被用户 GET 请求访问 | GET 422 / 修复 | 标记-清除两阶段：先标记 `deleted_at = now()`（软删除），下次扫描再硬删除；GET 检查 `deleted_at` 不为空则返回 404 |

---

## 方向四：写入路径分布式补偿事务与一致性治理

### 现状

当前所有"两步写入"操作（storage + metadata）都没有事务保护：

```
Put 路径:
  1. store.Put(key, data)  →  ✅ 成功 / ❌ 失败
  2. repo.UpsertObject()   →  ✅ 成功 / ❌ 失败 ← ① 失败时 {key} 是孤儿 blob

Delete 路径:
  1. store.Delete(key)     →  ✅ 成功 / ❌ 失败
  2. repo.HardDeleteObject() → ✅ 成功 / ❌ 失败 ← ② 失败时 metadata 是幽灵行

CompleteMultipart 路径:
  1. store.CompleteMultipart() → ✅ 成功 / ❌ 失败
  2. repo.saveMultipartObject() → ✅ 成功 / ❌ 失败 ← ③ 失败时合并后 blob 是孤儿
  3. (之前的旧版本如果 versioning 开启) chunk cleaner 清理 ← ④ 失败时 chunk 索引残留
```

v86 和 v88 已经深入诊断了这四种不一致状态，但未给出与现有代码引擎整合的完整实施架构。

### 产品价值

| 用户画像 | 场景 | 当前风险 | 就绪后 |
|---------|------|---------|--------|
| 平台 SRE | 存储后端写满/DB 事务超时导致 `store.Put` 成功但 `UpsertObject` 失败 | 孤儿 blob 永久占据存储空间，无人能读取或删除 | 自动检测 + 补偿删除（新 Reconciler 模式），或写入时两阶段提交 |
| 合规审计 | 数据面 Delete 成功但元数据面 HardDelete 失败（DB 故障） | "已删除"的对象仍出现在列表中（幽灵行），但读取出错 | 幽灵行被自动识别并清除 |
| 数据工程师 | 100 个对象的批量 delete：50 个成功、50 个 DB 故障 | 50 个孤儿 blob，50 个幽灵行 | 批量操作出错时自动回滚已成功的操作（compensating delete/insert） |
| 运维 | 磁盘故障后从快照恢复，"已删除"的 blob 重新出现 | Reconcile 不清理这些历史孤儿 | 定期扫描检测 `storage.List` 与 `repo.ListActiveObjects` 的不一致 |

### 架构权衡

**方案 A：被动检测 + 补偿（Reconciler 模式）**

| 步骤 | 操作 | 复杂度 | 影响 |
|------|------|--------|------|
| 1 | `storage.List("")` 枚举所有存储 key | 低 — `List` 已实现 | 列出所有后端 blob |
| 2 | `repo.ListActiveObjects()` / `repo.ListStorageKeys()` 枚举已知 key | 中 — 需新增 Repository 方法 | 获取"应该存在的 blob 列表" |
| 3 | 集合差：`StorageKeys - KnownKeys = Orphans` | 低 | 发现孤儿 blob |
| 4 | 集合差：`KnownKeys - StorageKeys = Ghosts` | 低 | 发现幽灵元数据 |
| 5 | 补偿：孤儿 blob 调用 `store.Delete()`，幽灵行调用 `repo.HardDeleteObject()` | 低 | 自动清除不一致 |

**方案 B：主动预防（写入时补偿）**

| 步骤 | 操作 | 复杂度 | 影响 |
|------|------|--------|------|
| 1 | `Put` 路径：先生成 `storageKey`，先写入 `repo.UpsertObject`（设置 `storage_key`） | 高 — 需反转写入顺序 | 如果 metadata 写入失败，不写 storage；如果 storage 写入失败，留下"准幽灵行"（有 metadata 但 blob 不存在） |
| 2 | 补偿：storage 写失败 → `repo.HardDeleteObject`（回滚 metadata） | 中 | 类似"compensating transaction"模式 |
| 3 | 补偿引擎：为每个两步操作注册逆操作 | 中高 | 通用架构 |

| 维度 | 方案 A (Reconciler) | 方案 B (Write-time Compensating) |
|------|-------------------|----------------------------------|
| 数据丢失窗口 | 直到下次 Reconciler 运行（分钟~小时级） | 毫秒级 |
| 实现复杂度 | 低（新增 1-2 个 method + 1 个 reconciler 步骤） | 中高（反转写入顺序 + 补偿引擎） |
| 存储后端需求 | 需要 `List("")` 能力 | 不依赖 List |
| 是否影响热路径 | 否（异步） | 是（每个写操作增加补偿注册） |
| 建议 | ✅ **第一步** | ❌ **第二步**（方案 A 上线后再评估是否需要） |

### 边界情况

| 场景 | 影响 | 处理策略 |
|------|------|---------|
| `storage.List("")` 在 S3 后端上扫描数百万对象 | List 操作耗时久、成本高（每 1000 个对象一次 API 调用） | 增量扫描：每次 Reconciler 只扫描最近 N 小时变更的 key（利用 storage 的 `LastModified` 排序）；分桶+前缀分批进行 |
| Local 后端 `storage.List("")` 遍历整个目录树 | 大目录下 `readdir` 性能差 | 使用 `filepath.Walk` 或 `os.ReadDir` 分页；对已知对象目录结构可做前缀优化（按 tenant/bucket 分批） |
| 并发写入造成假阳性：Reconciler 扫描时 `store.Put` 刚完成但 `repo.UpsertObject` 还没执行 | 刚 PUT 的对象被识别为孤儿并删除 | 孤儿 blob 检测增加 grace period（如孤儿 blob 创建时间 > 5 分钟才认为是真正的孤儿）；覆盖 `PutOptions` 写 `_aero_created_at` 侧车标记 |
| 存储后端不支持 `List("")` 的 prefix-less 扫描（某些 S3 兼容后端可能限制） | 无法获取全量存储 key | `List` 方法返回 `NotImplemented` 错误时，Reconciler 跳过主动检测，退化到仅清理写入路径明确标记的失败记录（需要写路径增加失败记录表） |
| 补偿删除后，原 PUT 请求的客户端收到 201 但对象实际被删除 | 客户端以为写入成功但对象不可访问 | 这是方案 A 的固有 tradeoff：写入路径无补偿时数据已丢失；补偿只是把"不可见的丢失"变得"可见的丢失"（GET 返回 404），后者更易于诊断 |

---

## 方向五：对象版本操作与历史演进管理

### 现状

系统自版本化功能上线以来一直累积完整版本历史，但**没有提供任何面向用户的操作能力**：

| 能力 | 当前状态 | 代码证据 |
|------|---------|---------|
| 查看版本列表 | ✅ REST `GET /v1/files/{key}/versions` + S3 `GET ?versions` | `handler.go:ListVersions`、`s3compat/handler.go:listObjectVersions` |
| 读取特定版本 | ✅ `GET /v1/files/{key}?versionId=xxx` | `handler.go:getKey` 解析 `versionId` query |
| **版本回退（Revert）** | ❌ 需手动 GET 旧版本内容 → PUT 到相同 key | 两步操作、非原子、无审计 |
| **版本间 Diff** | ❌ 无任何版本间比较能力 | 元数据 diff、内容 diff 均不存在 |
| **版本时间线可视化** | ❌ Web UI 无版本历史面板 | `webui/static/index.html` 无版本 tab |
| **版本锁定（阻止被覆盖）** | ❌ `locked_until` 阻止硬删除但不阻止覆盖写入 | `Put` 路径的 `checkLockBeforeOverwrite` 仅检查 WORM |
| **版本删除** | ❌ 无删除特定版本的 API | S3 `DELETE ?versionId` 未实现 |

当前 `Restore` API 的语义冲突：

```go
// internal/api/rest/handler.go:618
func (h *Handler) Restore(w http.ResponseWriter, r *http.Request) {
    // ... 调用 svc.RestoreObject() 清除 deleted_at 标志
    // 这其实是"取消软删除"，而不是"版本回退"
}
```

而 S3 的 `POST ?restore` 用于从 GLACIER/DEEP_ARCHIVE 恢复归档对象，AeroVault 却将其重用于软删除恢复，导致协议语义冲突——当未来实现 GLACIER 存储类时，需要另一个端点来实现 S3 的归档恢复。

### 产品价值

| 用户画像 | 场景 | 当前体验 | 就绪后 |
|---------|------|---------|--------|
| 内容创作者 | 误修改了文档想回到昨天的版本 | 手动 GET 旧版本 ID → 读取 → 重新 PUT（丢失元数据/标签/ACL 等） | 一键 `POST /v1/files/{key}/versions/{v}/revert` — 原子化回退，保留当前版本的元数据或回退时一并恢复 |
| 合规管理员 | 法律调查需要查看某文件的版本变更历史 | 只能看到时间戳和版本 ID 列表，看不出内容变化 | `GET /v1/files/{key}/versions/{v1}/diff?v2={v2}` 返回元数据变化 + 内容变化摘要 |
| 知识工作者 | 通过 Web UI 浏览文件时想查看早期版本 | Web UI 没有版本 tab，完全无法操作 | Web UI 版本时间线面板：选择版本 → 预览内容 → 回退 |
| DevOps 团队 | 部署配置文件被错误覆盖，想快速回退到上个版本 | 需要通过 SDK/AWS CLI 手动操作 | `aero-vault cli revert <key> --version <v>` |
| 审计人员 | 需要证明"2026-07-10 日的某些文件没有被修改" | 只能遍历 `list-object-versions` 查看所有版本的 created_at | 版本锁定 `POST /v1/files/{key}/versions/{v}/lock` 禁止该版本被 lifecycle 裁剪 |

### 架构权衡

| 方案 | 复杂度 | 影响范围 | 建议 |
|------|--------|---------|------|
| **`version_revert` Repository 方法**：原子操作——读取旧版本的 `StorageKey` 和元数据 → 创建新版本（用旧版本的 content 但新版本的时间戳和 ETag） | 中 | `repository/sql_objects.go` + `service/file_features.go` | ✅ **核心能力** |
| **REST 端点**：`POST /v1/files/{key}/versions/{v}/revert` — 可选 `?preserve-metadata=true` 保留当前版本的元数据或回退旧版本的元数据 | 低 | `rest/router.go` + `rest/handler.go` | ✅ **用户入口** |
| **S3 兼容 `COPY` 到自身**：`PUT /{bucket}/{key}?versionId={v}&x-amz-copy-source=/bucket/key?versionId={v}` — S3 无标准 API，可用 CopyObject 到同 key 实现 | 中低 | `s3compat/extra.go:copyObject` 增加 versionId 处理 | ✅ **协议入口** |
| **版本间 Diff API**：`GET /v1/files/{key}/versions/{v1}/diff?since={v2}` 返回元数据变化 + 基于 chunk 的内容变化摘要 | 中高 | 新 `rest/diff.go` + `service/file_features.go` + 存储层比较 | ❌ **第二步** — 需要内容比较策略（逐行 diff / chunk diff / 二进制比较） |
| **Web UI 版本面板**：版本时间线展示 + 版本预览 + 一键回退 | 中 | `webui/static/index.html` | ✅ **与 REST API 一同发布** |

版本回退的内部流程：

```mermaid
flowchart LR
    A[POST /v1/files/k/revert?versionId=v] --> B[GetObjectVersion(tenant, bucket, key, v)]
    B --> C{Source 版本是否存在?}
    C -->|No| D[404]
    C -->|Yes| E[读取旧版本的 StorageKey 和元数据]
    E --> F{是否 preserve-metadata?}
    F -->|Yes| G[保留当前版本的 content_type/metadata/tags/acl]
    F -->|No| H[使用旧版本的 content_type/metadata/tags/acl]
    G --> I[创建新版本: InsertObjectVersion 使用旧 blobs StorageKey + 新版本 ID 和新/旧元数据]
    H --> I
    I --> J[返回新版本信息 + 201 Created]
```

### 边界情况

| 场景 | 影响 | 处理策略 |
|------|------|---------|
| 回退到一个已被 Lifecycle 软删除的版本 | 软删除版本存在但 `deleted_at` 不为空 | 允许回退，同时清除源版本的 `deleted_at` 标志（取消其软删除状态） |
| 版本 A（当前版本）→ 版本 B（回退目标），版本 B 的 content 与版本 A 完全一致 | 不必要的版本创建，存储浪费 | 比较两个版本的 ETag：若完全一致则返回 200 OK（无操作），不创建新版本 |
| 回退时存储后端（S3/OSS）不支持 SE 读取版本 Blob | 回退需要校验源存储 key 可读 | 回退前先 `store.Stat(sourceStorageKey)` 验证存在性；不存在则返回 409 Conflict |
| 并发回退：两个请求同时回退到不同版本 | 最终结果是后完成的请求覆盖先完成的 | 复用现有版本化的并发模型——每个写操作创建一个新版本，回退只是"用旧内容创建新版本"，本身不涉及覆盖或冲突 |
| 版本锁定 vs 回退：某版本处于 Compliance 锁中 | Compliance 版本的内容不应该被"复制出来"创建新版本（违反不可绕过的合规保证） | 回退时检查源版本的 `RetentionMode`：Compliance 模式版本的 content 不可复制到新版本（除非新版本也带相同的 Compliance 设置） |

---

## 优先级排序与实施建议

| 优先级 | 方向 | 快速闭环（1-3 天） | 中期（1-2 周） | 长期（1-3 月） |
|--------|------|-------------------|---------------|---------------|
| **P1** | 方向一：合规锁模型 | `RetentionMode` + `LegalHold` 字段 + migration + 硬删除路径模式检查 | Governance bypass 权限校验 + SetLockedUntil 模式感知 | Compliance 模式不可逆保证 + sidecar 合规元数据背书 |
| **P1** | 方向二：UploadPartCopy | `Storage.CopyPart` 接口（local 实现） + `UploadPartCopy` handler | Service 层 `CopyByRange` + 条件拷贝头支持 | CopyObject >5GB 自动 multipart 降级 |
| **P1** | 方向四：补偿事务 | Reconciler 新增 `sweepOrphans`（storage.List - repo.ListStorageKeys 交叉验证） | 操作级补偿注册表 + grace period 防假阳性 | 写入前 metadata 优先模式 + 自动回滚 |
| **P2** | 方向三：版本生命周期 | `NoncurrentDays`/`MaxVersions`/`AbortMPUDays` schema + S3 lifecycle XML 解析扩展 | LifecycleJob 新增 `sweepNoncurrent` + `sweepAbandonedUploads` | MaxVersions 写入路径强制裁剪 |
| **P2** | 方向五：版本操作 | `version_revert` Repository 方法 + REST 端点 + CLI 命令 | Web UI 版本面板 + S3 Copy-to-self 兼容 | 版本间 diff API（content-level + metadata-level） |

---

> **文档生成方法：** 逐文件扫描 `cmd/server/main.go` + `internal/` 全部 24+ 子包（~231 个 Go 源文件），识别 5 类有明确代码锚点、高产品/架构价值、且在 89 轮既有 expansion 文档中未被深度独立覆盖的扩展方向。每方向均包含：现状代码精确引用 → 产品价值量化 → 架构权衡分析（含分步实施路径）→ 边界情况枚举。
