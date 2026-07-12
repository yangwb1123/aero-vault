# S3 协议完备性与运营治理盲区：架构分析报告

> **分析师角色：** 资深架构师  
> **分析依据：** [expansion-v114-s3-protocol-completeness-and-operational-gaps.md](./expansion-v114-s3-protocol-completeness-and-operational-gaps.md) + 实时代码库验证结果  
> **核心更正：** 方向四（Multi-Object Delete）为**假阳性**——代码已完整实现，真实缺口降级为增量增强  
> **分析边界：** 纯架构层面，不涉及具体代码实现

---

## 一、架构评估

### 1.1 当前架构的优势

AeroVault 的架构在系统设计层面有几个值得肯定的决策：

| 优势 | 评估 |
|------|------|
| **Protocol Adapters 薄层模式** | REST / S3 / WebDAV / MCP 作为等价的薄适配器共享同一 `FileService`，保证了跨协议的语义一致性。这是一个正确的架构选择。方向四的 Multi-Object Delete 之所以能快速实现，正是因为 REST API 的 `BatchDelete` 存在且 service 层已完整——验证了薄 adapter 模式的收益 |
| **Storage 接口简约** | 7 个核心方法 + 4 个 multipart 方法 + 2 个 presign 方法。接口的大小刚刚好（~13 方法），不多不少。这为新后端接入提供了低门槛 |
| **Opt-in 安全默认** | AI/pgvector/Qdrant/events/cluster 全部 flag-gated，默认 off。这有效防止了能力膨胀导致的基线回归 |
| **Repository 接口规格清晰** | 方法集覆盖了对象/桶/上传/审计/事件/作业/幂等性等领域界限分明。`WriteAccessLog` 作为 no-op 存在于接口中虽然功能上断裂，但接口签名设计是正确的——问题在执行层 |

### 1.2 关键设计决策评估

针对报告中 5 个方向涉及的架构决策，逐项评估：

#### 决策 1：S3 路由仅支持 Path-Style（`/s3/{bucket}/{key}`）

| 评估维度 | 结论 |
|----------|------|
| **当时上下文** | 项目初期，为最小可行实现选择一个固定的 `/s3` 前缀是对的。Path-Style 路由实现简单，不需要配置域名解析 |
| **当前负担** | 严重。所有现代 S3 SDK 默认使用 Virtual Hosted-Style。这制造了用户 onboarding 的第一道摩擦——必须阅读文档并设置 `force_path_style = true` |
| **架构债务等级** | **高**。不是代码复杂度债务，而是**协议兼容性债务**——一个缺失的入口路由模式阻塞了通道层所有流量的自然到达 |
| **修复窗口** | 已超过最佳修复窗口。每多一个大版本，依赖 Path-Style 的客户端越多，切换到双模式的断裂风险越大 |

#### 决策 2：Checksum 仅支持 MD5（`Content-MD5`）

| 评估维度 | 结论 |
|----------|------|
| **当时上下文** | MD5 是最低合规基线（RFC 1864）。项目早期只关注 crud 正确性，不关注端到端数据完整性 |
| **当前负担** | 中等偏高。aws-sdk-go-v2 v1.18.0+ 默认启用 `RequestChecksumCalculation.WHEN_REQUIRED`。虽然 SDK 会在服务端不支持时降级，但会输出警告日志。企业 RFP 中 "end-to-end checksum" 是必选项 |
| **架构债务等级** | **中**。影响范围集中在 service 层和 s3compat handler，核心架构（Storage 接口 + Repository Object 模型）需要一次性修改 |

#### 决策 3：WriteAccessLog 作为 no-op 保留在接口中

| 评估维度 | 结论 |
|----------|------|
| **当时上下文** | 接口设计时预留了审计能力。功能齐全的配置 CRUD 与空操作的消费端说明这是"框架先建，逻辑缓上"的迭代节奏 |
| **当前负担** | 低。接口和配置层都正确，只需在 repository 实现层添加真正的日志写入逻辑。不涉及架构变更 |
| **架构债务等级** | **低**（但风险高——合规审计场景下是致命的运营缺口） |

#### 决策 4：废弃上传无治理

| 评估维度 | 结论 |
|----------|------|
| **当时上下文** | Reconcile 框架最初聚焦于生命周期和保留策略。Multipart 清理被推迟 |
| **当前负担** | 中。Storage 接口没有扫描方法，Local 后端的 multipart 信息仅存于内存 map（重启即丢失）。这是资源泄漏 |
| **架构债务等级** | **中高**。Storage 接口的局限性是核心障碍——现有方法集无法让 Reconcile 层与存储后端就"活跃上传"达成共识 |

### 1.3 架构债务全景

| 债务类型 | 方向 | 债务级别 | 修复难度 | 影响面 |
|----------|------|---------|---------|--------|
| 协议兼容性债务 | 1 - Virtual Hosted-Style | 高 | 小（~250 行） | 全量 S3 SDK 用户 onboarding |
| 协议语义债务 | 2 - Flexible Checksum | 中 | 大（400-600 行 + 迁移） | 企业采购、SDK 兼容 |
| 功能债务（空骨架） | 3 - Server Access Log | 低 | 中（300-400 行） | 合规审计 |
| 协议语义增益 | 4 - Multi-Object Delete 增强 | 极低 | 极小（~50 行） | 版本化桶批量删除 |
| 资源治理债务 | 5 - Abandoned Multipart | 中高 | 中（200-300 行 + 接口变更） | 存储成本 |

**架构债务总计评估：** 中等。修复集中在协议适配层（s3compat）和存储抽象层（Storage 接口），核心业务逻辑（FileService）基本不受波及。

---

## 二、扩展方向

基于报告中的 5 个方向，结合代码验证的真实状况，我重新组织了优先级和扩展方向。

### 方向 A：【P0】Virtual Hosted-Style S3 路由 —— 用户入口兼容性

**为什么需要：**
- 这本质上是**入口路由模式缺失**，而非功能缺失。AWS CLI v2、boto3、aws-sdk-go-v2、aws-sdk-js-v3 全部默认使用 Virtual Hosted-Style。当前状态意味着每个新用户都需要额外的配置步骤。这是 onboarding 的第一道门槛。
- 从技术角度看，Host 头携带了完整的路由信息（`bucket.s3.example.com` → bucket = "bucket"），当前完全不利用是信息浪费。

**核心挑战：**
1. **多租户与虚拟主机冲突。** 当前 tenant 通过 `X-Aero-Tenant` 头传递。虚拟主机模式下，tenant 信息可能在 Host 头中（`bucket.tenant.aero-vault.example.com`），也可能在 Header 中。需要一个清晰的提取优先级规则。
2. **域名模式需配置化。** `aero-vault.example.com` 不是硬编码的——用户可能部署在不同域名下。需要支持配置多个虚拟主机域名后缀。
3. **双模式共存策略。** Path-Style 不能废弃（已有客户端依赖），Virtual Hosted-Style 要作为第一等公民添加。路由层需要一个**请求模式检测器**来决定使用哪种解析路径。

**预期的架构变更：**

```
                   ┌──────────────────────────────────┐
                   │          Inbound Request          │
                   │  Host: bucket.aero.example.com   │
                   │  ─ or ─                          │
                   │  Host: aero.example.com          │
                   │  Path: /s3/bucket/key            │
                   └──────────┬───────────────────────┘
                              │
                              ▼
              ┌───────────────────────────────┐
              │    s3VirtualHostResolver      │  ← 新中间件
              │    (或 dispatcher 内逻辑)       │
              │                                │
              │  1. 检查 Host 是否匹配已知域名    │
              │  2. 如果匹配 → 从 Host 提取 bucket│
              │     → 注入 X-Aero-Virtual-Bucket │
              │  3. 如果不匹配 → 原 path-style    │
              │  4. 回退策略                     │
              └──────────┬────────────────────┘
                         │
                         ▼
              ┌───────────────────────────────┐
              │      s3compat router          │
              │  (改动极小：优先读取 Virtual-    │
              │   Bucket 头，fallback 到 URL)   │
              └───────────────────────────────┘
```

**对现有系统的影响：**
- **极小。** s3compat handler 内部只需在 bucket 提取点增加一个 Header 检查分支（`r.Header.Get("X-Aero-Virtual-Bucket")` 优先于 `chi.URLParam(r, "bucket")`）
- 全局中间件链无影响（新中间件仅在 s3compat mount 点内注册）
- 现有 Path-Style 客户端完全不受影响

**推荐方案选项：**

| 方案 | 优势 | 劣势 |
|------|------|------|
| **A-1：中间件转换** — 在 s3compat SubRouter 前置一个中间件，将 Virtual Host → Header 注入，handler 零改动 | 改动最小，handler 不变 | 引入了一个"暗转换«dark translation»"，调试时可能困惑 |
| **A-2：路由层双模式** — NewRouter 内部在注册 path-style 路由的同时，通过 `r.Route` 或 `r.Group` 注册虚拟主机路由 | 路由声明式可见 | `chi.Router` 的 Host 匹配能力有限；需要外部反向代理配合 |
| **A-3：仅文档要求反向代理** — 要求用户在 Nginx/Envoy 层将 Virtual Host 转为 Path-Style | 代码零改动 | 对用户不友好；CORS、签名验证等场景可能断裂 |

**推荐：A-1（中间件转换）。** 理由：与 AGENTS.md 中 "Middleware 链顺序固定" 的约束兼容（s3compat 内部子链不违反全局链顺序），且后向兼容代价为零。

---

### 方向 B：【P1】端到端数据完整性（Flexible Checksum + 读取路径验证）

**为什么需要：**
- 当前的数据完整性模型是"TLS in, MD5 out"。TLS 终止于反向代理后，内部链路无独立校验。磁盘静默损坏不可检测。
- AWS S3 自 2023 年起默认使用 CRC32，SDK 层越来越倾向于服务端校验。
- RFP 必选项：金融/医疗行业 RFP 中"end-to-end data integrity verification"是标准条款。

**核心挑战：**
1. **算法选择策略。** 做不到全部支持（性能开销大），只支持一个又不够灵活。需要确定"默认 + 按需"的策略。
2. **存储放大 vs 计算放大。** 持久化所有校验和（CRC32 + CRC32C + SHA1 + SHA256）意味着每对象额外存储 4×8-32 字节，放大不明显（可接受），但写入路径需要计算 4 种哈希，CPU 开销显著。
3. **读取时算法回退。** 用户 GET 时请求 `x-amz-checksum-crc32`，但存储时只持久化了 CRC32C。需要实时重算——这引入了首次读取延迟。
4. **Multipart 聚合校验和。** S3 规范要求 multipart 校验和为各段相等算法的异或组合（或特定算法组合）。需要在 `CompleteMultipart` 时做校验和合并。
5. **迁移兼容。** 已有对象没有校验和记录。读取时不能报错——校验和应逐步附加到新对象，旧对象读取时优雅降级。

**预期的架构变更：**

```
Storage 接口扩充:
  PutOptions 增加 ChecksumAlgorithms []string
  ObjectInfo 增加 Checksums map[string]string    ← 算法→校验和值的映射
  Put/CompleteMultipart 返回 ObjectInfo 包含 checksums

Repository Object 模型扩充:
  新增字段: ChecksumCRC32 / ChecksumCRC32C / ChecksumSHA1 / ChecksumSHA256
  需要新的 migration 双文件 (sqlite + postgres)

Service 层:
  md5WrapReader 重构为 checksumWrapReader(ctx, r, algorithms []string)
    → 返回 io.Reader + 校验和验证函数 + 计算出的校验和值数组

S3Compat Handler:
  PutObject: 读取 x-amz-checksum-* 头 → 传入 service → 验证
  GetObject: 读取 ObjectInfo.Checksums → 写入 x-amz-checksum-* 响应头
  writeS3ObjectMeta: 新增 x-amz-checksum-* 输出分支
```

**对现有系统的影响：**
- **中高。** Storage 接口、Repository Object 模型、Service 层写入路径三个核心抽象层均有变更
- 但所有变更都是**增量添加**（新增字段、新增可选参数），不修改已有方法的签名或行为
- 已有对象的读取路径完全不受影响：无校验和字段时，响应头不输出 `x-amz-checksum-*`

**推荐算法策略：** 默认计算 CRC32C（ISA-L 硬件加速，AWS 新默认），客户端通过 `x-amz-checksum-crc32` 等头指定时追加计算。读取时优先返回已持久化的算法，缺失算法触发实时流式重算 + 缓存策略。

---

### 方向 C：【P1】Server Access Log 交付管线 —— 合规审计

**为什么需要：**
- 配置层完备但消费端空转——这是最危险的一种架构断裂：监控面板显示"日志已配置"但日志从未流出。SOC2 审计员只需一次随机抽查即可发现。
- `BucketConfig.LoggingTarget` 和 `LoggingPrefix` 已经有完整 CRUD，只差 `WriteAccessLog` 的实现体。

**核心挑战：**
1. **写入频次与性能的权衡。** 每请求同步写一个日志对象 → 写入放大 2×，延迟跳跃。必须异步缓冲处理。
2. **日志循环爆炸。** 如果日志目标 bucket 就是源 bucket，访问日志写入自身会产生新的写入事件 → 无限递归。必须禁止同 bucket 日志。
3. **日志格式标准化 vs 扩展性。** 严格遵循 S3 Server Access Log 格式有利于工具链解析，但字段集可能不匹配 AeroVault 的租户/协议扩展。需要确定"S3 规范子集 + 自定义扩展字段"的平衡。
4. **宕机丢失容忍。** 异步 buffer 在宕机时丢失缓冲区内日志。这是可以接受的吗？S3 真实行为也是 best-effort，但合规审计可能要求"无丢失"。

**预期架构变更：**

```
新增组件:
  internal/accesslog/:
    format.go      — 日志行格式 (S3 规范子集 + 扩展字段)
    writer.go      — 异步 buffer + 定时 flush goroutine
    bucket_check.go— 循环写入检测

Repository:
  WriteAccessLog 实现体: 写入 format.Writer 的 channel

Middleware:
  AccessLog 中间件增强: 提取对象级信息（key、操作、状态、延迟）
                       → 调用 repo.WriteAccessLog

可选的存储分离:
  如果日志写入到对象存储 → 复用 WriteObject 路径（注意不能触发自身日志）
  如果日志写入到外部系统 → 新增 LogSink 接口（可选）
```

**对现有系统的影响：**
- **小。** 不修改已有接口签名，不修改路由，不修改数据库 schema（除非决定存储日志到 DB 而非对象存储）
- AccessLog 中间件的增强是增量添加对象级字段

**推荐策略：** 异步缓冲 + 批量写入到日志目标 bucket（对象存储），每 60 秒或 10000 条 flush。日志 bucket 自身不做访问日志（配置层阻断）。宕机容忍度：最多丢失 ≤60 秒的日志。

---

### 方向 D：【P1】Multi-Object Delete 增量增强 —— 协议完善

**为什么需要：**
- 这不是新方向——核心功能已实现。回归报告中原本将其列为方向四，但代码验证发现是假阳性。
- 真正的价值在三个增量增强点（基于代码审查）：版本化桶中的精确删除、1000 限制校验、不存在的 key 静默成功。

**核心挑战：**
1. **`deleteRequestObject` 缺少 `VersionId` 字段。** 当前实现对所有 key 执行 `h.svc.Delete(..., true)`（`true` = 软删除）。在版本化桶中，这删除的是最新版本，而不是指定版本。需要 XML 解析增加 `<VersionId>` 字段支持。
2. **1000 对象上限校验。** S3 规范要求 `len(Objects) > 1000` → `MalformedXML`。当前实现无此校验，超长请求可能引发服务端压力。
3. **`h.svc.Delete` 循环 vs `h.svc.BatchDelete`。** 当前使用 `Delete` 循环（对不存在的 key 返回 `ErrNotFound`，被当前代码静默处理为 `Deleted`），而 `BatchDelete` 也存在但未被 S3 层使用。实质上不影响功能正确性，但可优化。

**预期架构变更：**
- `xml.go`：`deleteRequestObject` 增加 `VersionID string` 字段（`xml:"VersionId"`）
- `extra.go` 的 `deleteObjects`：增加 `len(in.Objects) > 1000 → MalformedXML` 检查；版本化桶中传递 VersionID 到 Delete 调用
- 对不存在的 key：`errors.Is(err, service.ErrNotFound)` 已在当前实现中正确处理

**对现有系统的影响：**
- **接近于零。** 三处修改都是增量增强，不修改已存在的功能路径

---

### 方向 E：【P2】废弃分段上传生命周期治理 —— 成本优化

**为什么需要：**
- 高吞吐场景下，废弃分段上传的存储成本不可忽略（报告估算：每日 10000 InitMultipart，5% 废弃率 → 1.5TB/月无效存储）。
- 当前无任何监控指标覆盖废弃上传量（运营盲区）。
- S3 后端上的废弃分片按标准存储计费，Local 后端占用磁盘不可见。

**核心挑战：**
1. **Storage 接口无上传列表扫描方法。** 这是核心架构障碍。当前 `Storage` 接口假设 "谁创建谁清理"，没有第三方扫描机制。新增方法需要对 Local/S3/OSS/COS 四个后端全部实现。
2. **Local 后端 multipart 信息仅存于内存。** 服务器重启后，Local 后端的 multipart 会话丢失，但 DB 中的 upload 记录成为孤儿。这产生了"DB 记录存在但存储会话不存在"的不一致状态。
3. **并发安全。** Reconcile 清理与用户主动 CompleteMultipart 的竞态条件——如果 Reconcile 先执行了 AbortMultipart，用户的 CompleteMultipart 会失败（返回 `NoSuchUpload`）。
4. **超时粒度策略。** 大文件上传可能需要 7 天（视频转码链路上传），小文件只需 1 小时。是否需要 per-bucket upload TTL 还是全局统一配置？

**预期架构变更：**

```
Storage 接口扩充:
  新增: ListActiveMultipartUploads(ctx, prefix string) ([]MultipartUploadInfo, error)

Repository 接口扩充:
  新增: ListUploadsOlderThan(ctx, tenant string, before time.Time) ([]Upload, error)

Reconcile 框架:
  RetentionJob 增加 uploadTTL 配置
  新增方法: CleanupAbandonedUploads(ctx, ttl)

监控:
  新增指标: abandoned_upload_total, abandoned_upload_cleaned_total

Local 后端:
  local_multipart.go 增加持久化层（至少是重启可恢复的）
  或在 Repository 中重建：读取 DB 中的 upload 记录 → 重建 multipart 上下文
```

**对现有系统的影响：**
- **中。** Storage 接口变动影响全部 4 个后端。但新增方法是可选的（backend 可以返回 `nil, ErrNotSupported`，Reconcile 降级为纯 DB 侧清理）
- Repository 的 `ListUploadsOlderThan` 可以直接复用已有的 `created_at` 字段，无需 migration

**推荐策略（最小侵入方案）：**
1. 不修改 Storage 接口
2. Reconcile 层读取 DB 中 `created_at < now - TTL` 的 upload 记录
3. 逐条调用 `svc.AbortMultipart`（通过 service 层，而非直接操作 storage）
4. S3 后端额外通过 SDK 的 `ListMultipartUploads` 做交叉验证
5. Local 后端依赖于 `AbortMultipart` 的幂等性——如果存储会话已丢失（重启后），DB 记录删除即可

这种方式的损失：S3 后端上可能存在"DB 记录已清理但 S3 上传会话仍残留"的不一致窗口。通过监控指标 `abandoned_upload_total` 可发现。

---

## 三、接口设计建议

### 3.1 核心原则

| 原则 | 适用场景 |
|------|---------|
| **增量添加，不修改已有签名** | 所有 5 个方向都遵循：Storage 接口增方法 → 已有后端返回 `ErrNotSupported`；Object 模型增字段 → 零值为旧对象 |
| **配置驱动，flag-gated** | 参考已有 Opt-in 模式。新功能默认 off。Virtual Hosted-Style 需要配置域名列表后才能激活 |
| **Protocol → Service 层语义透明** | Service 层应保持协议无关。Checksum 的参数传递不应携带 S3 协议特有的 header 名称 |
| **空接口优雅降级** | Backend 不支持某功能（如 `ListActiveMultipartUploads`）时，调用方应优雅降级，不阻断主流程 |

### 3.2 是否需要新的抽象层

| 方向 | 是否需要新抽象层 | 理由 |
|------|-----------------|------|
| Virtual Hosted-Style | **否** | 一个中间件足以，不需要新接口 |
| Flexible Checksum | **否** | `PutOptions` / `ObjectInfo` 增字段即可，现有接口不新增 |
| Server Access Log | **可选** | 如果日志写入外部系统（ELK/Loki），可抽象 `LogSink` 接口；否则复用对象存储写入路径 |
| Multi-Object Delete 增强 | **否** | 纯 handler 层适配 |
| Abandoned Multipart | **可选** | Storage 接口是否需要新增扫描方法是关键决策——新增则影响所有后端，不新增则降级为 DB-only 清理 |

**判断：不引入新抽象层。** 5 个方向当前都可以在现有架构层（Middleware / Storage / Repository / Reconcile）内部解决。引入新抽象层的需求不存在。

### 3.3 向后兼容性保证

| 方向 | 兼容策略 |
|------|---------|
| Virtual Hosted-Style | Path-Style 完全保留。Host 头解析仅在配置了域名后缀时激活。未配置时等同于当前行为 |
| Flexible Checksum | 无校验和的老对象读取时响应头不含 `x-amz-checksum-*`。客户端不应假设校验和总是存在 |
| Server Access Log | 已启用 logging 配置的桶在部署后自动开始产生日志。不影响已有请求处理路径 |
| Multi-Object Delete | 已有 `deleteObjects` 行为完全不变。增强点全部是错误处理路径 |
| Abandoned Multipart | 清理任务默认 off（需要配置 `UPLOAD_TTL`）。用户主动 Complete 与 Reconcile 清理的竞态通过 `DeleteUpload` 的幂等性处理 |

---

## 四、技术选型

### 4.1 是否需要引入新依赖

| 方向 | 新依赖 | 评估 |
|------|--------|------|
| Virtual Hosted-Style | **无** | 纯标准库 `net/http.Request.Host` + 字符串匹配 |
| Flexible Checksum | **可选：`hash/crc32`（stdlib）** + **可选：`github.com/google/crc32c`** | CRC32C（Castagnoli）需要硬件加速 lib。stdlib 有 `hash/crc32` 表驱动实现，但不支持硬件加速。`google/crc32c` 使用 Intel SSE 4.2 指令（~10 倍加速）。**建议：** 先用 stdlib 实现，后续性能优化时引入硬件加速库 |
| Server Access Log | **无** | 纯标准库 |
| Multi-Object Delete 增强 | **无** | 纯标准库 |
| Abandoned Multipart | **无** | 纯标准库 |

**总体评估：无需引入新的第三方依赖。** 所有 5 个方向的实现可以用标准库完成。CRC32C 硬件加速可作为 P2 优化。

### 4.2 Storage 接口新增方法决策矩阵

关于方向 E 中 Storage 接口是否需要新增 `ListActiveMultipartUploads` 方法的决策：

```
                ┌────────────────────────────────────┐
                │   是否新增 Storage 接口扫描方法？    │
                └────────────────┬───────────────────┘
                                 │
              ┌──────────────────┼──────────────────┐
              ▼                  ▼                  ▼
        方案 1: 新增        方案 2: 不新增       方案 3: 可选接口
        ──────────        ──────────           ──────────
        接口变动明确        Zero-touch 后端      ListActiveMultipartUploads
        所有后端需实现       仅 DB 记录判断      作为可选方法，backend
        S3后端可精准         S3后端可能出现       返回 nil 表示不支持
        清理                孤儿残留              
                                                
        代价: 4后端实现       代价: 精度损失      平衡方案
        收益: 精准清理       收益: 零接口变动
```

**推荐：方案 3（可选接口）。** 定义 `MultipartLister` 接口，与 `Storage` 接口分离：

```go
// 独立可选接口
type MultipartLister interface {
    ListActiveMultipartUploads(ctx, prefix string) ([]MultipartInfo, error)
}
```

S3 后端实现此接口（调用 `ListMultipartUploads`），Local/COS/OSS 可以不实现。检测模式：

```go
if lister, ok := store.(MultipartLister); ok {
    uploads, err := lister.ListActiveMultipartUploads(...)
}
```

这种方式与 Go 的标准接口分离模式一致（如 `io.Writer`/`io.ReaderFrom`），零侵入已有接口定义。

### 4.3 自建 vs 采购/集成的决策

此报告涉及的 5 个方向均属于**自建范畴**：

| 方向 | 为什么必须自建 |
|------|--------------|
| Virtual Hosted-Style | 路由模式选择是产品核心行为，不可外部依赖 |
| Flexible Checksum | 校验和是存储系统的核心数据面功能，必须内建 |
| Server Access Log | 日志格式和交付路径需要与业务模型对齐（tenant/bucket/object 层面） |
| Multi-Object Delete 增强 | 已经自建完成，仅做增量增强 |
| Abandoned Multipart | 资源治理是存储系统的运维基本功，外部无法替代 |

**无采购/集成必要性。** 这些功能都是 S3 兼容存储系统的内建职责。

---

## 五、实施路线图

### 5.1 优先级修正

基于代码验证的更正，重新排定优先级：

```
P0 (本轮 Sprint)     P1 (下轮 Sprint)       P2 (后续)
─────────────────────────────────────────────────────────
方向 1: Virtual      方向 3: Server         方向 5: 废弃
Hosted-Style         Access Log             Multipart 清理
                     ─────────────           ─────────────
方向 4: Multi-      方向 2: Flexible        成本优化
Object Delete 增强   Checksum
───────────────      ─────────────
高用户感知           企业合规 + 采购门槛
```

**优先级调整说明：**
- **方向 4 从 P1 降级**（从"全新实现"降为"增量增强"），但因用户感知强烈（`aws s3 rm --recursive` 已可用但版本化删除不精确），仍安排在 P0 并列实现
- **方向 2 从 P1 保持**，但逻辑优先级在方向 3 之后——因为合规缺口比性能增强对采购决策的影响更大
- **方向 5 从 P2 保持**，确认其架构影响评估准确

### 5.2 阶段划分

#### 阶段 1（1-2 周）：入口兼容性 + 协议完善

| 工作项 | 涉及文件 |
|--------|---------|
| 虚拟主机中间件开发 + 配置项 | 新文件 + `config.go` + s3compat router |
| s3compat handler Host 头读取分支 | `handler.go` bucket 提取点 |
| Multi-Object Delete 增量增强（versioned delete + 1000 限制） | `xml.go` extra.go |
| 测试：Host 头解析 + `aws s3 rm --recursive` e2e | handler_test.go |
| 文档更新：Virtual Hosted-Style 使用说明 | README / docs |

**交付物：** 客户端无需 `force_path_style` 配置即可连接；版本化桶批量删除精确。

#### 阶段 2（2-3 周）：合规审计 + 数据完整性基线

| 工作项 | 涉及文件 |
|--------|---------|
| Storage 接口 PutOptions 增加 ChecksumAlgorithms | `storage.go` |
| Service 层 `md5WrapReader` → `checksumWrapReader` 重构 | `file_crud.go` |
| Repository Object 增加 Checksum 字段 + migration | `repository.go` + migration 双文件 |
| S3 写入/读取路径 checksum header 处理 | `handler.go` `writeS3ObjectMeta` |
| Server Access Log 异步 writer 实现 | 新文件 `internal/accesslog/writer.go` |
| AccessLog 中间件增强（对象级字段） | `middleware.go` |
| 日志格式定义 + S3 规范子集 | `internal/accesslog/format.go` |
| 测试：checksum 算法套件 + 日志格式 + buffer flush | 对应 _test.go |

**交付物：** PUT/GET 路径端到端 CRC32C 校验；访问日志持续写入。
**里程碑：** 满足 SOC2 审计要求的基础设施就绪。

#### 阶段 3（3-4 周）：存储成本治理 + 可选接口

| 工作项 | 涉及文件 |
|--------|---------|
| Storage 可选接口 `MultipartLister` 定义 | `storage.go` |
| S3 后端实现 `MultipartLister` | `s3.go` |
| Repository `ListUploadsOlderThan` + 实现 | `repository.go` + `sql_uploads.go` |
| Reconcile `CleanupAbandonedUploads` | `retention.go` |
| Local 后端 multipart 持久化（可选） | `local_multipart.go` |
| 监控指标注册 + Prometheus 告警 | `telemetry/` |

**交付物：** 废弃分片自动清理；废弃上传量可监控。
**里程碑：** 存储成本泄漏可量化和控制。

### 5.3 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| **方向 1：Host 头解析与 CORS 预检冲突** | 中 | 中 | 虚拟主机中间件在 CORS preflight（`OPTIONS *`）时不解析 bucket。CORS 配置路由优先于 Host 解析 |
| **方向 2：checksum 引入的性能回归** | 中 | 中 | 使用流式计算，不缓冲全量数据再进行哈希。CRC32C 优先选择硬件加速 lib。写入路径 100% 流式 |
| **方向 2：Migration 时已有对象无 checksum** | 高 | 低 | 读取时若 checksum 字段为空，跳过校验和验证（静默降级）。新增对象自动填充。兼容旧对象的渐进式迁移 |
| **方向 3：日志写入自身桶导致循环** | 中 | 高 | `WriteAccessLog` 中做目标桶检测：如果 `targetBucket == sourceBucket`，跳过写入并记录告警日志。S3 侧也做配置验证 |
| **方向 3：Buffer 宕机导致日志丢失** | 高 | 中 | 在文档和配置注释中声明为 best-effort。如果合规要求日志零丢失，提供同步模式配置（同步模式有性能代价） |
| **方向 5：Cleanup 与 CompleteMultipart 竞态** | 中 | 中 | `AbortMultipart` 在 upload 已 complete 时返回 `ErrUploadNotFound`，Cleanup 任务忽略此错误。竞态窗口极小（Cleanup 的 DB 查询到实际 Abort 之间的延迟） |
| **方向 5：Local 后端重启后上传上下文丢失** | 高 | 中 | Reconcile 清理的入口是 DB 记录，不是 Storage 会话。DB 记录是重启安全的。Local 后端重启后无法 list 活跃上传，但 DB 记录仍然存在，允许通过 `svc.AbortMultipart` 清理（Local 的 AbortMultipart 对不存在的会话是幂等的） |

### 5.4 与现有 Sprint 的协调

根据 AGENTS.md 中的优先级映射：

| 方向 | AGENTS.md 优先级映射 | 备注 |
|------|---------------------|------|
| 1 - Virtual Hosted-Style | **Bug**（用户入口断裂） | 应优先于功能开发 |
| 2 - Flexible Checksum | **Feature** | P1 功能，需遵循"测试覆盖率 ≥ 50%" |
| 3 - Server Access Log | **Bug**（已存储的配置不工作） | 数据完整性丢失，实际是生产缺陷 |
| 4 - Multi-Object Delete 增强 | **Improvement** | 非阻塞，可并行处理 |
| 5 - Abandoned Multipart | **Feature** | P2 成本优化 |

`make check` 约束对方向 2 影响最大——Storage 接口变动后，所有后端的 contract test 需要补充。

---

## 六、最终建议摘要

```
┌─────────────────────────────────────────────────────────────────┐
│                    架构师建议摘要                                  │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  【必须立即修复（P0 - 高影响 + 低成本）】                        │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │ 方向 1: Virtual Hosted-Style S3 路由                    │    │
│  │ 影响: 全量 S3 SDK user onboarding 摩擦                 │    │
│  │ 成本: ~250 行, 1 中间件, 0 依赖变更                    │    │
│  │ 推荐方案: 中间件转换 (A-1)                              │    │
│  └─────────────────────────────────────────────────────────┘    │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │ 方向 4: Multi-Object Delete 增量增强 (Versioned + 1000) │    │
│  │ 影响: 版本化桶批量删除精确度 + 协议完备性                │    │
│  │ 成本: ~50 行, 3 处文件修改                              │    │
│  │ 状态: 核心功能已实现, 仅增量增强                        │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                                                 │
│  【必须做（P1 - 合规/企业采购门槛）】                            │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │ 方向 3: Server Access Log 交付管线                      │    │
│  │ 影响: SOC2/PCI-DSS 合规审计能力                         │    │
│  │ 成本: ~350 行, 跨 3 层                                  │    │
│  │ 关键风险: 循环日志写入禁止, 宕机丢失容忍度声明            │    │
│  └─────────────────────────────────────────────────────────┘    │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │ 方向 2: Flexible Checksum (CRC32C 基线 + 按需扩展)       │    │
│  │ 影响: 端到端数据完整性 + SDK 兼容 + RFP 必选项           │    │
│  │ 成本: ~500 行 + migration, 3 个核心接口层变动            │    │
│  │ 推荐算法策略: 默认 CRC32C + 客户端指定追加                │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                                                 │
│  【值得做（P2 - 成本优化 + 运营提升）】                          │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │ 方向 5: 废弃分段上传生命周期治理                          │    │
│  │ 影响: 高吞吐场景下 TB 级存储成本泄漏                      │    │
│  │ 成本: ~250 行 + 可选 MultipartLister 接口                │    │
│  │ 推荐方案: DB 优先 + Storage 可选接口交叉验证               │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                                                 │
│  【架构债务总结】                                               │
│  总计: 中低                                                    │
│  核心问题: 入口路由模式缺失 (方向1) + 数据面完整性契约缺失 (方向2) │
│  无 God 类型 / 圈复杂度 / 文件行数 / 禁止包违反                 │
│  所有修复均与 AGENTS.md 约束兼容                               │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```
