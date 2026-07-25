# AeroVault 资深架构师/产品经理视角 — 第 88 轮：产品纵深与架构盲区

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 24+ 子包，三套 SDK，MCP 双模式，Web UI，48 对迁移文件，`deploy/` 配置，`HARNESS.md`，`AGENTS.md`，全部已有 expansion docs 交叉验证）  
> **去重验证：** 对 `docs/requirements/` 下全部 87 份既有分析文档逐方向进行正则 + 语义交叉验证，确保每个方向在前述分析中**零实质性独立架构分析**  
> **日期：** 2026-07-11  
> **核心原则：** 选取代码中存在具体锚点、有明确产品/架构价值、跨 87 轮分析仍未被触及或仅一行路过提及的纵深盲区。每个方向包含产品价值、架构权衡、代码锚点与边界情况。

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 | 既有分析覆盖 |
|---|------|------|--------|---------|---------|-------------|
| **1** | **AI Agent 工具执行安全沙箱与治理（AI Agent Tool Execution Sandbox & Governance）** | 安全/治理 | **P1** — Agent 在 LLM 驱动下可执行任意工具调用（`read_file`、`search`、`list_files`），但无沙箱隔离、无单次调用预算、无操作审计、无访问范围收缩。Agent 继承租户的全部权限，LLM prompt 注入可导致未授权数据访问 | `internal/ai/agent.go:68-103`（工具描述硬编码在 system prompt）；`internal/ai/agent.go:115-150`（`dispatchTool` 无权限校验、无审计、无大小限制）；`internal/ai/agent.go:155-170`（`callReadFile` 无速率限制、无敏感内容过滤）；`internal/ai/agent.go:180-200`（`callSearch` 返回全部 chunks 无输出截断）；`internal/ai/agent.go:90-104`（`agentSystemPrompt` 包含 `read_file`、`search`、`list_files` 三个工具——无作用域约束） | ✅ **零实质性架构分析**（v21 方向表一行提到「Agent 无会话上下文」——聚焦跨 API 调用的会话状态，**非安全沙箱**；v61 分析 MCP prompts/sampling/roots 协议完备度——聚焦 MCP 协议本身，非 Agent 治理；v75 覆盖 Agent↔MCP 工具代码重复——聚焦工程重构，**非安全治理**） |
| **2** | **存储后端能力契约与组合式多后端路由（Storage Backend Capabilities Contract & Multi-Backend Composition）** | 架构/组合性 | **P2** — `Storage` 接口仅暴露 `Backend() string`，整个系统据此假定所有后端行为一致。实际上不同后端的能力差异巨大（Local 需要 SignKey 才能 presign；S3 原生支持 multipart 但 OSS/COS 有不同语义；本地 FS 无存储类概念），系统无法根据后端能力调整行为、无法优雅降级、无法混用多后端 | `internal/storage/storage.go:120`（`Backend() string` 是唯一的能力标识方法）；`internal/storage/s3.go:60-90`（S3 后端实现依赖原生 SDK，部分特性 Local 无等价）；`internal/storage/local.go:25-40`（`SignKey` 为空时 PresignGet 返回 "not implemented"——错误降级）；`internal/storage/factory.go:30-55`（`NewFromConfig` 创建单一后端，无多后端路由）；`internal/service/file_crud.go:173-180`（`Put` 无后端仲裁逻辑——所有后端同质处理） | ✅ **零实质性独立架构分析**（v87 方向四在 "StorageClass 分层转换"中以一行概念提及 `BackendCapabilities()` 方法——**仅一行概念、零架构设计**；v46 一行提及 "新增存储后端开发指南"——聚焦 DX 文档；v77 方向五覆盖 OSS/COS 未运行 contract 测试——聚焦工程质量，**非能力契约模型**；其余 docs 零覆盖） |
| **3** | **分片上传生命周期治理与孤儿预防（Multipart Upload Lifecycle Governance & Orphan Prevention）** | 可靠性/存储效率 | **P1** — 分片上传在初始化后无超时回收、无最大分片数限制、无最小分片大小校验、无单 key 并发上传数上限。S3 标准要求除最后一片外每片 ≥5MB，AeroVault 不检查。AbortMultipart 或 CompleteMultipart 中断后的残留 `.multipart/` 目录无 GC 回收 | `internal/storage/local_multipart.go:20-30`（`InitMultipart` 创建目录——无 TTL/清理）；`internal/storage/local_multipart.go:35-50`（`UploadPart` 不检查 part size ≥5MB 约束）；`internal/storage/local.go:40-45`（`uploads map[string]*localUpload` 无过期淘汰）；`internal/service/file_multipart.go:40-60`（`InitMultipart` 无每 key 并发上传上限）；`internal/service/file_multipart.go:140-150`（`CompleteMultipart` 验空 part 列表但不验 part 数上限）；`internal/service/file_crud.go:Put`（无 `size == 0` 拒绝——零长度文件可用作哈希计算 DoS） | ✅ **零实质性独立架构分析**（v79 方向二覆盖 CompleteMultipart ETag 交叉验证——聚焦数据完整性，**非生命周期治理**；v48 覆盖 multipart 并发一致性——聚焦竞态条件；v76 方向一覆盖 ListParts 全表加载——聚焦性能；v77 方向三覆盖 server-side copy 缺失——聚焦协议完备度。**分片上传的生命周期、约束校验、孤儿回收——从未被作为独立架构方向分析**） |
| **4** | **写入路径补偿事务（Write Path Compensating Transaction for Storage-Metadata Consistency）** | 数据一致性/韧性 | **P1** — `store.Put` 与 `repo.UpsertObject` 之间无事务边界：前者成功后者失败 → 永久孤儿 blob；`store.Delete` 与 `repo.HardDeleteObject` 同理 → 幽灵元数据行。当前 Reconcile 仅处理版本保留与软删除清除，不扫描写入路径业务孤儿 | `internal/service/file_crud.go:173-205`（`Put` 路径：`store.Put` 在前 → `writePutObject` 在后）；`internal/service/file_crud.go:285-316`（`hardDeleteObject` 路径：`store.Delete` → `repo.HardDeleteObject`）；`internal/service/file_multipart.go:90-110`（`CompleteMultipart`：`store.CompleteMultipart` → `saveMultipartObject`）；`internal/reconcile/job.go`（仅处理 `ListExpired`/`ListSoftDeletedBefore`——零写入路径孤儿检测）；`internal/repository/repository.go`（`ListActiveObjects` 与 `Storage.List` 之间无交叉验证） | ✅ **部分覆盖但零方案架构**（v86 方向一完整分析了四种不一致状态——orphan blob / ghost metadata ——**但仅诊断未给出补偿架构设计**；v55 方向一覆盖 EventBus 持久化与孤儿 blob 策略——聚焦事件系统；v48 方向二覆盖 multipart 孤儿——焦点范围窄。**写入路径的补偿事务模式（compensating transaction / DIU pattern）——从未被设计**） |
| **5** | **向量嵌入模型漂移自动检测与渐进式修复（Auto-Detection & Progressive Healing for Embedding Model Drift）** | AI 运维/检索质量 | **P2** — Embedder 更换后现有 chunks 仍使用旧模型，Search 正确跳过不匹配模型的 chunks（`drift_test.go` 验证），但检索覆盖率骤降。当前修复手段只有全量 `ReindexStale`（boot-time one-shot，无进度、无监控、无策略）。无自动漂移检测、无计划性重索引调度、无灰度切换、无回滚安全网 | `internal/ai/drift_test.go`（验证 `EmbedModel` 过滤——正确被动行为）；`internal/ai/indexer.go:120-140`（`ReindexStale`——全量扫描 `ListObjectIDsToReindex`，零策略控制）；`internal/ai/embedder.go:70-80`（`Name()`——是模型身份唯一标识）；`internal/ai/search.go:82-95`（`searchVector` 过滤 `embedModel != queryModel` 的 chunk——被动防御）；`internal/ai/caching_embedder.go`（缓存嵌入但不感知模型变换）；`internal/config/config_ai.go`（`AI_REINDEX_STALE_ON_START`——仅启动时单次触发）；`internal/telemetry/metrics.go`（零模型漂移指标） | ✅ **零实质性架构分析**（v30 方向四一行概念性提及「Dynamic Embedding Warm-Swap」——零代码锚点与设计；v71 方向三一行概念性提及「Scheduled Reindex Strategy」——零锚点；v82 方向二覆盖 RAG context window overflow——**检索质量的不同维度**；v69 方向一覆盖 embedding model versioning ——聚焦 chunk 元数据字段，**非探测与愈合策略**） |

---

## 方向一：AI Agent 工具执行安全沙箱与治理

### 现状

Agent 的核心工具循环 `dispatchTool` 完全信任 LLM 输出的工具调用指令：

```go
// internal/ai/agent.go:115-150（简写）
func (a *Agent) dispatchTool(ctx context.Context, tenant, name string, args map[string]any) string {
    switch name {
    case "list_files":
        return a.callListFiles(ctx, tenant, args)     // ← 可列出全桶
    case "read_file":
        return a.callReadFile(ctx, tenant, args)      // ← 可读取任意文件
    case "search":
        return a.callSearch(ctx, tenant, args)         // ← 可搜索全量索引
    default:
        return "error: unknown tool " + name
    }
}
```

Agent 继承调用者的完整租户作用域，没有任何收缩或隔离：

| 保护维度 | 当前状态 | 风险 |
|---------|---------|------|
| 访问范围收缩 | Agent 继承调用者租户全部权限 | LLM prompt 注入可访问租户下所有桶/文件 |
| 工具调用预算 | 仅全局 `Agent.MaxSteps`（4 步）限制 | 单次工具调用可返回 MB 级数据，单步可发起 N 次 `search` 调用 |
| 输出大小限制 | `callReadFile` 有 4KB 截断；`callSearch` 无截断 | search 结果可能包含大量 chunks，Token 消耗失控 |
| 速率限制 | 无工具级速率限制 | 单次 Agent 调用可在 1 秒内发几十次 `search`/`list_files` |
| 操作审计 | 工具调用不记录到 `audit_log` | 无法追溯"哪个 Agent 读了哪些文件" |
| 敏感内容过滤 | 无 | `read_file` 直接返回文件内容，敏感数据可被 LLM 输出 |
| 工具调用超时 | 仅全局 `RequestTimeout` | 单次 `search` 超时可终止整个 Agent 循环 |
| 并发安全 | `dispatchTool` 无锁 | 并发 Agent 请求共享无保护的 `Agent` 结构体 |

### 产品价值

| 用户画像 | 场景 | 当前瓶颈 | 治理后 |
|---------|------|---------|--------|
| 企业安全团队 | 部署 Agent 给内部员工使用，担心 LLM 被诱导读取敏感文档 | Agent 可读取/搜索租户下所有文件 | Agent 作用域可缩小到 "只读 bucket-A，不读 bucket-B（合规）" |
| 运维团队 | 发现 Agent 在 1 分钟内调用了 200 次 `search`，消耗大量嵌入配额 | 无限制，只能事后发现 | 单次 Agent 调用限制 `search ≤ 10 次`，`read_file ≤ 5 次` |
| 合规团队 | 需要审计"用户在 14:30 通过 Agent 看到了哪些文件" | Agent 工具调用零审计 | `audit_log` 记录 `agent:{id} tool:read_file args:{key} result_size:4096` |
| 产品经理 | Agent 返回了某模型的版权保护内容，公司面临法律风险 | 无法在工具层拦截 | `read_file` 返回前千检 `_aero_legal_hold` 或 content-classification 标签 |

### 架构权衡

| 方案 | 复杂度 | 维护成本 | 侵入性 | 建议 |
|------|--------|---------|--------|------|
| Agent 工具调用中增加 `context.WithValue` 注入作用域 | 低 | 低 | 低 | ✅ 第一步：工具调用时校验作用域 |
| 新增 `AgentConfig` 控制每工具配额 | 低 | 低 | 低 | ✅ 第一步：`Agent.MaxReadsPerRun=10`、`Agent.MaxSearchesPerRun=10` |
| 工具调用写 `audit_log` | 中 | 低 | 低 | ✅ 第二步：复用已有的审计日志基础设施 |
| 工具输出后加敏感内容过滤器 | 中 | 中 | 低 | ✅ 第二步：复用已有 PII Detector |
| 为 Agent 引入独立沙箱 tenant | 高 | 高 | 中 | ❌ 第三步：超出当前阶段需求 |

### 边界情况

| 场景 | 影响 | 处理策略 |
|------|------|---------|
| Agent 在 one-step 中返回多个 tool_calls（OpenAI 并行调用） | 一次 Agent 响应可触发 N 个 `search` | 按 step 维度累计配额，而非按 `dispatchTool` 单次 |
| 用户通过 prompt 注入让 Agent 修改 system prompt | Agent 的工具定义和服务器的工具实现之间存在差距 | 工具列表应当在服务端**硬编码**而非从 LLM 输出中提取 |
| Agent 并发调用——同一租户多请求 | `Agent` 结构体是无状态纯函数，但配额计数器需要同步 | 配额状态关联到 `(tenant, sessionID)` 而非 `Agent` 实例 |
| Agent 读取的文件包含二进制的非文本内容 | `callReadFile` 的 4KB 截断可截断 UTF-8 字符边界 | 截断应在 rune 边界而非 byte 边界，或标记 `truncated:true` |
| Agent 使用 `search` 返回的数据之外的 LLM 训练数据外泄 | 非工具层问题 | 需要 LLM response 的 post-processing filter（AI ops 方向） |
| 多租户环境下一个 Agent 会话泄漏到另一个 | `dispatchTool` 无 `sessionID` 隔离 | 引入 `AgentSession{ID, Tenant, Budget, Tools}` 上下文 |

---

## 方向二：存储后端能力契约与组合式多后端路由

### 现状

```go
// internal/storage/storage.go:120
type Storage interface {
    Put(...) (ObjectInfo, error)
    Get(...) (io.ReadCloser, ObjectInfo, error)
    Stat(...) (ObjectInfo, error)
    Delete(...) error
    List(...) (ListResult, error)
    PresignGet(...) (string, error)
    PresignPut(...) (string, error)
    InitMultipart(...) (MultipartInit, error)
    UploadPart(...) (MultipartPart, error)
    CompleteMultipart(...) (ObjectInfo, error)
    AbortMultipart(...) error
    Backend() string   // ← 唯一的能力标识
}
```

`Backend()` 返回的字符串（`"local"`、`"s3"`、`"oss"`、`"cos"`）是系统了解后端行为的唯一途径，这种字符串匹配导致大量隐式假设：

```go
// storage/local.go:signURL — presign 依赖 SignKey 配置
func (s *LocalStorage) PresignGet(...) {
    if s.cfg.SignKey == "" {
        return "", fmt.Errorf("not implemented")  // ← 局部降级
    }
    // ...
}
```

当前无法回答的关键问题：

| 问题 | 当前答案 | 正确行为 |
|------|---------|---------|
| 这个后端支持 presign 吗？ | 调用才知道（`"not implemented"`） | 通过 `Capabilities()` 提前告知 |
| 这个后端支持哪些 storage class？ | 不知道—全部假定为 `STANDARD` | 后端声明支持的 classes |
| 这个后端支持 multipart 吗？ | 假定全部支持 | 非全部后端都需要（如 K8s CSI volume） |
| 最大对象大小是多少？ | 受存储系统隐式限制 | 后端声明限制，FileService 提前校验 |
| 支持哪些 checksum 算法？ | 假定 MD5（ETag） | 后端声明支持的算法（CRC32C、SHA256、MD5） |
| 支持 versioning 吗？ | 全部假定支持 | local FS 支持，部分云后端不支持 |

### 产品价值

| 场景 | 价值 |
|------|------|
| **组合多后端**：热数据 → local NVMe；温数据 → S3 Standard；冷数据 → S3 Glacier | 存储成本优化 60%+；当前零组合能力 |
| **优雅降级**：S3 后端不支持某个特性时自动退回到基础路径 | 避免运行时 panic 或 "not implemented" 500 错误 |
| **统一存储类路由**：设置 `x-amz-storage-class: GLACIER` → 自动路由到冷后端 | StorageClass 字段从"被动元数据"变为"主动路由策略" |
| **迁移期双写**：后端 A→B 迁移时双写到两个后端 | 零停机后端替换 |

### 架构方案（概念级）

```go
type Capability string

const (
    CapPresign      Capability = "presign"
    CapMultipart    Capability = "multipart"
    CapVersioning   Capability = "versioning"
    CapEncryption   Capability = "encryption"
    CapChecksumMD5  Capability = "checksum:md5"
    CapChecksumCRC32C Capability = "checksum:crc32c"
    CapStorageClassSTANDARD  Capability = "storage-class:STANDARD"
    CapStorageClassIA        Capability = "storage-class:STANDARD_IA"
)

type Capabilities struct {
    Features      []Capability           // 支持的特性集
    MaxObjectSize int64                   // 最大对象大小(0=无限制)
    MaxParts      int                     // multipart 最大分片数
    MinPartSize   int64                   // multipart 最小分片大小
    StorageClasses []string               // 支持的存储类列表
}

// Storage 接口新增：
type Storage interface {
    // ... 现有方法 ...
    Capabilities() Capabilities  // ← 新增：返回后端能力描述
}
```

组合式多后端路由架构：

```
FileService.Put(ctx, tenant, bucket, key, ...)
    │
    ├─ StorageClassRouter（根据 storage_class + 对象特征选择后端）
    │   ├─ hotBackend (Capabilities: STANDARD, multipart, presign)
    │   ├─ warmBackend (Capabilities: STANDARD_IA, multipart, presign)
    │   └─ coldBackend (Capabilities: GLACIER, no multipart, no presign)
    │
    ├─ 校验后端能力满足操作需求
    ├─ store.Put(ctx, key, ...)  ← 目标后端
    └─ repo.UpsertObject(obj)    ← 记录 backend + storage_class
```

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 路由的后端不支持 `PresignGet` | 降级为走 `Get` + 自身 HTTP 代理（或在 FileService 层实现 presign） |
| 迁移期间对象存在于多个后端 | `StorageKey` 需要支持 "multi-homed" 引用 |
| 后端临时不可用（circuit breaker open） | 自动降级到冷后端（只读），或返回 503 |
| 新后端加入后无需重新 index | capabilities 是运行时查询而非注册时绑定 |
| 存储类中途变更 | 需要后台 transition worker 移动数据（v87 方向四） |

---

## 方向三：分片上传生命周期治理与孤儿预防

### 现状

分片上传（`InitMultipart` → `UploadPart` → `CompleteMultipart`/`AbortMultipart`）有三个主要的治理缺失：

**缺失 1：无超时回收**

```go
// internal/storage/local_multipart.go:20-30
func (s *LocalStorage) InitMultipart(ctx context.Context, key string, opts PutOptions) (MultipartInit, error) {
    uploadID := uuid.NewString()
    dir := filepath.Join(s.cfg.Root, ".multipart", uploadID)
    os.MkdirAll(dir, 0o755)
    s.mu.Lock()
    s.uploads[uploadID] = &localUpload{key: key, dir: dir, createdAt: time.Now(), opts: opts}
    // ← createdAt 存在，但永不检查、永不淘汰
    s.mu.Unlock()
}
```

- 客户端 `InitMultipart` 后断开连接 → 上传目录永久留在 `.multipart/` 下
- `uploads map` 无限增长 → 内存泄漏 + 磁盘空间泄漏
- 无类似 S3 的 `AbortIncompleteMultipartUpload` 生命周期规则
- 分片上传记录存在于 `repository.Upload` 表，但无 `expires_at` 或 `last_used_at` 字段

**缺失 2：无 S3 约束校验**

S3 协议标准要求：
- 除最后一片外，每片 ≥ 5MB（AeroVault 不检查）
- 最大分片数 10000（AeroVault 不检查）
- 单 key 的并发上传有合理上限（AeroVault 不检查）

```go
// internal/storage/local_multipart.go:35-50
func (s *LocalStorage) UploadPart(ctx context.Context, key, uploadID string, partNumber int32, r io.Reader, size int64) (MultipartPart, error) {
    // ← 无 size ≥ 5MB 检查（最后一片除外）
    // ← 无 partNumber ≤ 10000 检查
    f, err := os.Create(path)
}
```

**缺失 3：无并发上传保护**

```go
// internal/service/file_multipart.go:40-60
func (s *FileService) InitMultipart(ctx context.Context, tenant, bucket, key string, opts PutOptions) (repository.Upload, error) {
    // ← 无 "同一 key 最多 N 个活跃上传" 检查
    // ← 无 "租户最多 N 个活跃上传" 检查（复用 quota 机制）
}
```

### 产品价值

| 用户行为 | 当前后果 | 治理后 |
|---------|---------|--------|
| 客户端 init 后崩溃/断网 | 永久孤儿 `.multipart/` 目录；`uploads` 表残行永不清理 | 后台 GC 定时扫描 `created_at + 24h` |
| SDK 错误发送大量 2MB 分片 | 服务端静默接受，最终合并成无效对象 或 磁盘碎片 | 返回 `400 EntityTooSmall`（最后一片除外） |
| 恶意客户端对同一 key 发起 1000 次 `InitMultipart` | 磁盘/DB 被 1000 个上传 session 撑满 | 单 key 上限 + 租户上限 |
| 上传完成后 `AbortMultipart` 未调用 | `.multipart/` 临时文件占据大量磁盘 | TTL 过期自动 abort |

### 治理措施（概念级）

| 措施 | 实现路径 | 配置参数 |
|------|---------|---------|
| 上传 session TTL | `upload.expires_at` 列 + 周期性 `ScanExpired` | `MULTIPART_UPLOAD_TTL_HOURS=24` |
| 最小分片大小检查 | `UploadPart` 中校验 `size >= minPartSize`（最后一片除外） | `MULTIPART_MIN_PART_SIZE=5242880`（5MB） |
| 分片数上限检查 | `UploadPart` 中校验 `partNumber <= maxParts` | `MULTIPART_MAX_PARTS=10000` |
| 单 key 并发上传上限 | `InitMultipart` 中按 key 计数活跃 | `MULTIPART_CONCURRENT_PER_KEY=5` |
| 租户级上传上限 | 复用 `TenantQuota.MaxObjects` 或新增 `MaxInflightUploads` | `QUOTA_MAX_INFLIGHT_UPLOADS=100` |
| 孤儿上传 GC | Reconcile sweep 中加入 `ListExpiredUploads` + `AbortMultipart` | `RECONCILE_INTERVAL_MINUTES` 复用 |

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 客户端在最后一秒上传了最后一片，同时 TTL 到期 | `CompleteMultipart` 应在事务中检查 `expires_at`，若过期返回 `400 ExpiredUpload` |
| TTL 到期后部分分片已上传，部分未上传 | `AbortMultipart` 清理全部已上传分片 |
| 云后端（S3/OSS/COS）自己的上传过期策略 vs AeroVault 的策略 | 上传 TTL 以 AeroVault `expires_at` 为准（应用层），云后端策略作为补充 |
| 最小分片大小在 versioned bucket 中可否绕过？ | 同上约束——版本控制不影响分片大小要求 |
| 上传 session 跨越进程重启 | `Upload` 表持久化在 repository 中，重启后 GC 可以正常处理 |

---

## 方向四：写入路径补偿事务

### 现状

当前写入路径的**时序依赖**导致了不可恢复的不一致状态：

**Put 路径（file_crud.go:173-205）：**

```
store.Put(sk, reader, size, opts)         // 第 1 步：存储写入
    │                                        （消耗时间、网络、磁盘）
    ▼
repo.UpsertObject(ctx, obj)               // 第 2 步：元数据写入
    │
    ▼
repo.AddTenantUsage(ctx, +size, +1)       // 第 3 步：配额更新
    │
    ▼
s.emit(ctx, saved, EventCreated)          // 第 4 步：事件发布（最佳努力）
```

**硬删除路径（file_crud.go:285-316）：**

```
chunkCleaner.DeleteObjectChunks(...)       // 第 1 步：AI 索引清理（最佳努力）
    │
    ▼
store.Delete(ctx, obj.StorageKey)          // 第 2 步：存储删除
    │
    ▼
repo.HardDeleteObject(ctx, ...)            // 第 3 步：元数据删除
    │
    ▼
repo.AddTenantUsage(ctx, -obj.Size, -1)    // 第 4 步：配额更新
```

每两个步骤之间发生故障（进程崩溃、网络中断、DB 拒绝连接、磁盘满），结果就是孤儿 blob 或幽灵元数据。

### 故障情境矩阵

| 路径 | 步骤 | 故障点 | 后果 | 当前处理 |
|------|------|--------|------|---------|
| Put | store.Put 成功 / UpsertObject 失败 | 第 1→2 步间隙 | **孤儿 blob**：存储有内容，repo 无记录，永不访问 | ❌ 无 GC |
| Put | UpsertObject 成功 / AddTenantUsage 失败 | 第 2→3 步间隙 | **配额不准确**：对象存在但配额未增加 | ✅ warn log（最佳努力） |
| Delete | store.Delete 成功 / HardDeleteObject 失败 | 第 2→3 步间隙 | **幽灵元数据**：repo 记录指向不存在的 blob，List 继续显示 | ❌ 无修复 |
| Delete | HardDeleteObject 成功 / AddTenantUsage 失败 | 第 3→4 步间隙 | **配额不准确**：对象已删除但配额未减少 | ✅ warn log（最佳努力） |
| Multipart | CompleteMultipart 成功 / InsertObjectVersion 失败 | 合并→写入间隙 | **孤儿合并 blob**：存储有完整对象，repo 无记录 | ❌ 无 GC |

### 补偿事务模式（概念级）

```
Put 路径中的补偿事务：

Step 1: store.Put(sk, ...)
    if err → return err（用户可见）

Step 2: repo.UpsertObject(...)
    if err → COMPENSATE: store.Delete(sk)  // ← 回滚 blob
    if compensate err → log + emit "孤儿" 事件

Step 3: repo.AddTenantUsage(...)
    if err → warn log（配额不准的风险最低）
```

```go
// 概念代码（不落地）
func (s *FileService) PutWithCompensation(ctx context.Context, ...) (repository.Object, error) {
    info, err := s.store.Put(ctx, sk, reader, size, opts)
    if err != nil {
        return repository.Object{}, err
    }
    
    obj := s.buildPutObject(...)
    saved, err := s.repo.UpsertObject(ctx, obj)
    if err != nil {
        // 补偿：删除已写入的 blob
        if compErr := s.store.Delete(ctx, sk); compErr != nil {
            s.logger.Error("compensating delete failed: blob orphaned",
                "storage_key", sk, "original_err", err, "comp_err", compErr)
            // 仍然向用户返回原始错误
        }
        return repository.Object{}, fmt.Errorf("repo write: %w", err)
    }
    // ... 继续 AddTenantUsage, emit
    return saved, nil
}
```

### 实施原则

| 原则 | 说明 |
|------|------|
| **仅补偿关键路径** | `store.Put → repo.UpsertObject` 和 `store.Delete → repo.HardDeleteObject` 是数据完整性关键路径；`AddTenantUsage` 等辅助操作仅 warn log |
| **补偿必须幂等** | `store.Delete(sk)` 对已删除的 key 返回 nil（storage 接口保证） |
| **补偿失败不掩盖原始错误** | 用户始终看到原始 `repo.UpsertObject` 错误；补偿失败计入指标 |
| **补偿记录 Prometheus 指标** | `storage_write_path_compensations_total{reason, success}` |
| **Reconcile 作为兜底** | 即使补偿失败，Reconcile orphan-blob sweep 也应能回收 |

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 补偿 `store.Delete` 也失败（后端不可用） | 记录为 orphan blob，计入 `storage_write_path_compensations_total{success=false}`；Reconcile 兜底 |
| 两个并发 Put 到同一 key，第一个成功/第二个失败并在补偿时删除了第一个的 blob | 🔴 严重——需要 object-level 乐观锁防止此情形 |
| versioned bucket 中的补偿 | 补偿删除版本化 blob 不会影响其他版本（单独 blob） |
| store.Put 部分写入（写入 500MB 后失败） | 补偿 delete 清理部分写入的文件；local 后端使用 tmp + rename 确保原子性 |
| 集群环境下补偿操作在另一个副本执行 | 补偿是幂等的 `Delete`，无副作用 |

---

## 方向五：向量嵌入模型漂移自动检测与渐进式修复

### 现状

AeroVault 已经实现了嵌入模型变更时的**被动防御**：

```go
// internal/ai/search.go:82-95
func (s *Search) searchVector(ctx context.Context, req Request) ([]ranked, error) {
    queryModel := s.embedder.Name()
    for _, h := range hits {
        if queryModel != "" && h.Chunk.EmbedModel != "" && h.Chunk.EmbedModel != queryModel {
            continue  // ← 正确跳过不匹配的 chunk
        }
        vecHits = append(vecHits, ranked{...})
    }
}
```

以及**启动时一次性修复**：

```go
// internal/ai/indexer.go:120-140
func (ix *Indexer) ReindexStale(ctx context.Context, tenant string, limit int) (int, error) {
    ids, _ := ix.repo.ListObjectIDsToReindex(ctx, tenant, ix.embedder.Name(), limit)
    // ← AI_REINDEX_STALE_ON_START=true 时启动时调用一次
    // ← limit 硬编码为 1000（cmd/server/main.go:242）
}
```

但这些措施存在以下缺口：

| 缺口 | 描述 | 影响 |
|------|------|------|
| **无自动漂移检测** | 无法知道"现有 chunks 中 %p 使用了旧模型"——没有定时扫描、没有指标 | 运维人员在换模型后对检索覆盖率下降一无所知 |
| **无可调度重索引策略** | `ReindexStale` 仅在启动时单次调用，无定期执行、无增量执行 | 大型 corpus（百万级 chunks）无法在单次启动窗口中完成 |
| **无灰度切换** | 新模型替换旧模型是原子操作，无法 A/B 对比新旧模型的检索质量 | 模型降级风险：新模型可能更差但无法回退 |
| **无回滚安全网** | 切换到旧模型 ID 后无法重新使用旧 embedding（已覆盖） | 必须全量 reindex 才能回滚 |
| **无重索引进度监控** | `ReindexStale` 返回 count 但不写进度表 | 百万级 corpus 的重索引中进程崩溃 → 从头开始 |

### 产品价值

| 用户画像 | 场景 | 当前困境 | 治理后 |
|---------|------|---------|--------|
| ML 团队 | 每月更新 embedding 模型以获得更高的检索准确率 | 更新后检索覆盖率突然下降 40%（旧 chunks 被跳过），需要停机全量 reindex | 新模型 warm-up 期间自动 reindex，检索质量平滑过渡 |
| SRE 团队 | 发现新模型 embedding 延迟突然升高（p95 从 200ms → 2s） | 要么忍受慢速，要么回滚 env 重启（停机） | 一键回滚到上一模型 ID，系统自动重新激活旧 embedding |
| 产品经理 | 用户反馈"以前能搜到的文档现在搜不到了" | 排查困难：是模型变了？文档删了？索引满了？ | `embedding_coverage_ratio{model}` 仪表盘一目了然 |
| QA 团队 | 上线新模型前需要对比新旧模型的 top-5 检索结果 | 无法在生产环境做 A/B 对比 | Shadow-mode embedding：新旧模型同时运行以对比质量 |

### 架构方案（概念级）

**Phase A：可观测性**

```go
// telemetry/metrics.go 新增
embedding_coverage_ratio{model}       // gauge: 当前模型 chunk 占比
embedding_stale_chunks_total{model}   // gauge: 使用旧模型的 chunk 数
reindex_progress{tenant, model}       // gauge: 0.0~1.0
reindex_duration_seconds             // histogram
```

**Phase B：可调度重索引**

```
reindex_stale_job (定期扫描器)
    │
    ├─ repo.CountStaleChunks(tenant, activeModelName)
    │   └─ 如果 > threshold → 触发渐进式 reindex
    │
    ├─ repo.ListObjectIDsToReindex(tenant, activeModelName, batchSize)
    │   └─ 每批处理完成后更新 reindex_progress
    │
    └─ repo.RecordReindexWatermark(tenant, model, lastObjectID)
        └─ 进程重启后可续传
```

**Phase C：Shadow-mode 对比**

```go
// 影子模式下，两个 embedder 同时运行：
//   - activeEmbedder: 用于搜索（写 chunks 时也用它）
//   - shadowEmbedder: 仅用于搜索时额外计算（不写索引）
//
// search 返回两组结果：
//   {hits: [...], model: "v1"}
//   {hits: [...], model: "v2-shadow"}
// 管理员通过 dashboard 对比 NDCG/MRR 后决定切换
```

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 重索引过程中有新文档写入 | 新文档使用当前 active embedder 写入，与重索引并行无冲突 |
| 重索引过程中模型再切换 | 重索引以"目标模型"为准，切换后已重索引的 chunk 不变，新的重索引使用新目标模型 |
| 进程在重索引中途崩溃 | `reindex_watermark` 表记录最后一个已处理的 `objectID`，重启续传 |
| 旧模型 Embedding 在一部分 chunk 上比新模型更好 | 这是模型选择问题而非架构问题——shadow-mode 的 A/B 对比能暴露此情况 |
| 重索引期间检索质量下降（新旧模型混合） | Search 始终过滤不匹配的 chunks，覆盖率下降是暂时的，完成后恢复 |

---

## 总结与优先级建议

| # | 方向 | 类型 | 用户可见影响 | 实施量级 | 依赖关系 | 建议优先级 |
|---|------|------|------------|---------|---------|-----------|
| 1 | Agent 工具执行沙箱与治理 | 安全/治理 | 高（企业准入阻挡项） | 小～中 | 无 | **P1 — 本迭代** |
| 2 | 存储后端能力契约与多后端路由 | 架构/组合性 | 高（成本优化 60%+） | 大 | 依赖 v87 StorageClass 分层 | **P2 — 下迭代** |
| 3 | 分片上传生命周期治理与孤儿预防 | 可靠性/存储效率 | 中（防磁盘泄漏） | 小 | 复用 Reconcile 框架 | **P1 — 本迭代** |
| 4 | 写入路径补偿事务 | 数据一致性 | 高（防孤儿/幽灵） | 中 | 无 | **P1 — 本迭代** |
| 5 | 向量模型漂移自动检测与修复 | AI 运维/检索质量 | 中（检索覆盖率保障） | 中 | 无 | **P2 — 下迭代** |

**建议迭代顺序：**

1. **立即（本迭代）：** 方向一（Agent 安全基座）+ 方向三（分片上传治理）+ 方向四（补偿事务）
   - 三者均为 P1，影响数据安全/完整性/存储效率，且无外部依赖
2. **下迭代：** 方向二（能力契约）+ 方向五（模型漂移治理）
   - 方向二需要 StorageClass 分层的基础设施（v87 方向四）作为前提
   - 方向五重索引调度器可独立交付，shadow-mode 可后续扩展

---

*本分析基于代码库状态 `2026-07-11`，`internal/` 全部 30+ 子包，三套 SDK，MCP 双模式，WebUI，48 对迁移文件。*
