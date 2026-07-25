# AeroVault 高价值扩展方向 — 真正未被探索的前沿

> **视角:** 资深架构师 / 产品经理
> **方法:** 全局代码扫描（Go 源码 ~45K 行，全部 23 子包，24 组 SQL 迁移）
> **去重验证:** 逐方向检查 `docs/requirements/` 下全部 60 份既有分析文档（v1–v60）及 `docs/ROADMAP.md`，确认无重复
> **日期:** 2026-07-10
> **原则:** 选取既不在 ROADMAP 主力方向列表中、也不在 60 轮分析中的**高价值空白区域**

---

## 审阅：60 轮分析已覆盖的范围

前 60 轮分析已系统地覆盖了以下领域（仅列出大类）：

| 领域 | 覆盖轮次 |
|------|---------|
| 存储分层与生命周期转换 | v13, v17, v21, v23, v28, v58 |
| S3 通知真实投递引擎 | v17, v23, v58 |
| Object Lock Governance/Compliance 模式 | v23, v58 |
| 搁置分片上传自动清理 | v21, v28, v58 |
| 多存储后端智能路由 | v12, v15 |
| 服务端拷贝 | v25, v28 |
| 优雅关闭与生产部署 | v10 |
| 配置热重载 | v16, v27 |
| 批量操作框架 | v25 |
| 事件总线背压治理 | v23, v28 |
| 合规对象锁 WORM | v23, v58 |
| Webhook 失败表无限增长 | v60 |
| SSE Chat Stream 断开盲区 | v60 |
| PerTenantConcurrencyLimiter TOCTOU | v60 |
| 进程内存结构无上限 | v60 |
| 多协议一致性模型 | v19, v59 |
| 跨副本元数据复制与灾备 | v59 |
| AI 流式处理管线 | v59 |
| 对象版本数量治理 | v15, v21 |
| 前缀级细粒度权限 | v26 |
| 存储层压缩 | v26 |
| ... 以及 40+ 其他方向 | v1–v60 |

**核心发现：** 经过 60 轮分析，函数式"有没有"的问题已基本解决。本期聚焦的 5 个方向都是**既没有被任何一轮分析覆盖、又具有明确产品/架构价值**的真实空白。

---

## 本期方向总览

| # | 方向 | 类型 | 影响 | 代码状态 | 既有分析覆盖 |
|---|------|------|------|---------|-------------|
| 1 | **SSE-C（客户提供密钥的服务端加密）** | 合规/安全 | 🛑 金融/医疗监管硬性要求 | **零实现** | ❌ 从未被任何一轮分析独立讨论 |
| 2 | **非当前版本生命周期管理与版本数治理** | 成本/合规 | 🟠 大规模生产版本爆炸的核心治理手段 | **零实现** | ❌ 仅 v15 表格一行提及"版本数治理"概念但无分析；v21 浅触但聚焦 compliance hold |
| 3 | **MCP 协议纵深（Prompts/Sampling/Roots/Completions）** | 互操作性 | 🟠 MCP 协议兼容性严重不全 | **仅实现 30% 协议表面** | ❌ 从未被任何一轮分析作为独立架构方向分析 |
| 4 | **基于标签的生命周期规则** | 平台能力 | 🟠 灵活数据管理的关键缺失 | **零实现** | ❌ 从未被任何一轮分析提及 |
| 5 | **CompleteMultipart 崩溃安全与跨后端 Copy 原子性** | 可靠性 | 🟠 写入路径的 crash safety 保障 | **零实现** | ❌ 从未被分析 |

---

## 方向一：SSE-C（客户提供密钥的服务端加密）

### 现状

当前 SSE（Server-Side Encryption）只有一种模式：服务端管理的密钥加密，通过 `STORAGE_LOCAL_SSE_KEY` 配置。

```go
// internal/storage/encrypt.go — 现有的 AES-GCM envelope 加密
// 密钥来源于：环境变量 STORAGE_LOCAL_SSE_KEY、keyfile、KMS URL
// 所有对象使用同一把服务端密钥（或同一 KMS key）
// 不支持客户每请求提供密钥
```

S3 标准定义了三种 SSE 模式：

| 模式 | 请求头 | 密钥管理 | 当前状态 |
|------|--------|---------|---------|
| SSE-S3 | `x-amz-server-side-encryption: AES256` | 服务端管理 | ✅ 已实现（STORAGE_LOCAL_SSE_KEY） |
| **SSE-C** | **`x-amz-server-side-encryption-customer-*`** | **客户在请求中提供密钥** | **❌ 零实现** |
| SSE-KMS | `x-amz-server-side-encryption: aws:kms` | AWS KMS | ⚠️ 有 KMS 集成但非 S3 API 协议层面 |

**具体缺失的请求头：**

| 请求头 | 用途 | 代码锚点 |
|--------|------|---------|
| `x-amz-server-side-encryption-customer-algorithm` | 算法标识（必须为 `AES256`） | `internal/api/s3compat/handler.go` — 无解析 |
| `x-amz-server-side-encryption-customer-key` | 客户提供的 256-bit 密钥（base64） | 同上 |
| `x-amz-server-side-encryption-customer-key-MD5` | 密钥的 MD5 校验 | 同上 |
| `x-amz-server-side-encryption-customer-*`（响应） | 响应中返回 `x-amz-server-side-encryption-customer-algorithm` | `writeObjectHeaders` — 未输出 |

### 为什么需要

**合规驱动：** 金融（PCI-DSS）、医疗（HIPAA）、政务等行业的共同要求是"客户拥有加密密钥的唯一控制权"。SSE-C 是满足这一要求的标准 S3 机制。没有 SSE-C，这些行业的生产工作负载无法迁移。

**安全模型差异：**

```
SSE-S3（当前）：服务端管理密钥
  PUT /bucket/key
  x-amz-server-side-encryption: AES256
  → 服务端使用配置的密钥加密 → 客户信任服务端

SSE-C（缺失）：客户管理密钥
  PUT /bucket/key
  x-amz-server-side-encryption-customer-algorithm: AES256
  x-amz-server-side-encryption-customer-key: base64(32-byte-key)
  x-amz-server-side-encryption-customer-key-MD5: base64(md5(key))
  → 服务端使用客户提供的密钥加密 → 密钥不在服务端持久化
```

**S3 兼容性维度：** 主流 S3 SDK（AWS CLI、boto3、aws-sdk-go）在配置 SSE-C 参数时自动设置这些请求头。当前实现静默忽略这些头，导致客户端得到 `200 OK` 但数据**未按指定密钥加密**——这是安全幻觉。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/api/s3compat/handler.go:104-108` | PutObject 解析 `x-amz-storage-class` 但不解析 `x-amz-server-side-encryption-customer-*` | 无 SSE-C 请求头解析 |
| `internal/api/s3compat/handler.go:148` | `writeObjectHeaders` 输出 storage-class 但不输出 SSE-C 响应头 | 无 SSE-C 响应头 |
| `internal/storage/encrypt.go` | AES-GCM envelope 加密，密钥来源固定 | 没有 "接受外部密钥" 的代码路径 |
| `internal/storage/storage.go:PutOptions` | PutOptions 无客户密钥字段 | 无法传递客户密钥到存储层 |
| `internal/service/file_crud.go:Put` | 透传 storage.PutOptions | 无法携带客户密钥 |
| `internal/storage/local_write.go` | localWriter.ReadSeeker 加密路径 | 仅使用服务端密钥 |

### 架构蓝图

```
协议层（handler.go）:
  PUT x-amz-server-side-encryption-customer-algorithm: AES256
      x-amz-server-side-encryption-customer-key: base64(key)
      x-amz-server-side-encryption-customer-key-MD5: base64(md5(key))
  → 解析 → 校验 MD5 → 校验算法 → 存入 context/options

服务层（file_crud.go）:
  PutOptions 扩展：CustomerKey []byte
  → 透传到 storage.Put()

存储层（local_write.go / encrypt.go）:
  当 CustomerKey != nil:
    - 使用 CustomerKey 派生 per-object DEK（不持久化密钥）
    - 加密 blob
    - 在 metadata 中标记 _aero_sse_c: "AES256"
    - 不存储 key 的任何信息
  当 CustomerKey == nil:
    - 回落现有 SSE-S3 行为

GET 路径:
  - 请求必须带相同 CustomerKey
  - 从 metadata 读取 _aero_sse_c → 使用请求中的密钥解密
  - 返回 x-amz-server-side-encryption-customer-algorithm: AES256
```

### 规模估计

| 工作项 | 涉及文件 | 估算行数 |
|--------|---------|---------|
| 请求头解析 + 校验 | `internal/api/s3compat/handler.go` | ~60 行 |
| PutOptions 扩展 | `internal/storage/storage.go` | ~10 行 |
| 服务层透传 | `internal/service/file_crud.go` | ~20 行 |
| 存储层加密/解密路径 | `internal/storage/encrypt.go`, `local_write.go`, `local_read.go` | ~120 行 |
| 响应头输出 | `internal/api/s3compat/handler.go` | ~15 行 |
| GET/HEAD 密钥校验 | `internal/service/file_crud.go` | ~30 行 |
| 测试 | `internal/api/s3compat/handler_test.go`, `internal/storage/local_test.go` | ~100 行 |
| **总计** | **~355 行 + 测试** | |

---

## 方向二：非当前版本生命周期管理与版本数治理

### 现状

当前版本管理只有创建和读取，没有任何生命周期策略：

```go
// internal/repository/repository.go:30-38 — BucketConfig
type BucketConfig struct {
    // ...
    ExpireAfterDays int    // 当前版本的过期时间
    ExpireAction    string // "soft_delete" | "hard_delete"
    // 没有 NoncurrentVersionExpiration / NoncurrentVersionCount
}

// internal/reconcile/lifecycle.go — 只处理当前版本的过期
// 扫描条件：updated_at < now - days
// 操作：soft_delete 或 hard_delete
// ⚠️ 完全不涉及对象的版本历史
```

**版本爆炸的典型场景：**

```
租户在版本化 Bucket 中每 5 分钟更新一次配置文件：
  day 1:  288 个版本
  month:  8,640 个版本
  year:   103,680 个版本
  每个版本占用 metadata 行 + blob 存储
  无任何机制清除"除了最新 N 个版本以外的所有版本"
```

**缺少的 S3 生命周期规则：**

| S3 规则 | 当前状态 | 意义 |
|---------|---------|------|
| `NoncurrentVersionExpiration` | ❌ Missing | 非当前版本在 N 天后过期删除 |
| `NoncurrentVersionCount` | ❌ Missing | 只保留最近 N 个非当前版本 |
| `ExpiredObjectDeleteMarker` | ❌ Missing | 自动清理过期删除标记 |

### 为什么需要

**成本控制：** 版本化 Bucket 中的每个版本都需要存储空间。没有非当前版本生命周期，版本化 Bucket 的存储成本随写入频率线性增长，最终超过数据本身的价值。

**S3 兼容性：** 主流 S3 客户端（特别是使用版本化 Bucket 的 CI/CD 系统、备份工具、数据库快照工具）默认依赖生命周期规则来管理版本积压。缺失该功能是迁移阻塞项。

**合规：** 某些合规策略要求"保留所有版本 X 天，但之后仅保留最近 Y 个版本"。没有版本数治理就无法满足这类要求。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/repository/repository.go:30` | `BucketConfig` 定义了 `ExpireAfterDays` + `ExpireAction` | 无非当前版本字段 |
| `internal/repository/sql_buckets.go` | `buckets` 表 SQL 列 | 无非当前版本列 |
| `internal/reconcile/lifecycle.go` | `LifecycleJob` 遍历 active 对象 | 不扫描版本表 |
| `internal/repository/sql_objects.go` | `ListObjectVersions` 返回所有版本 | 无非当前版本遍历方法 |
| `internal/service/file_features.go` | `SetBucketLifecycle` 只接受 days+action | 无非当前版本参数 |

### 架构蓝图

```go
// BucketConfig 扩展
type BucketConfig struct {
    // ... 现有字段 ...
    NoncurrentVersionExpirationDays int    // 非当前版本过期天数; 0 = 禁用
    NoncurrentVersionCount          int    // 保留的非当前版本最大数量; 0 = 不限
    ExpiredObjectDeleteMarker       bool   // 自动清理过期删除标记
}

// LifecycleJob 扩展 — 在 reconcile 周期中：
// 1. 对每个版本化 Bucket:
//    a. 如果 NoncurrentVersionCount > 0:
//       - 按 updated_at DESC 排序非当前版本
//       - 跳过前 NoncurrentVersionCount 个
//       - 删除其余版本（hard_delete：删除 blob + 删除行）
//    b. 如果 NoncurrentVersionExpirationDays > 0:
//       - 查找 updated_at < now - days 的非当前版本
//       - 硬删除
//    c. 如果 ExpiredObjectDeleteMarker:
//       - 查找删除标记（DeletedAt 非空且无 active 版本）
//       - 清理

// 新增迁移 0025:
// ALTER TABLE buckets ADD COLUMN noncurrent_version_days INTEGER NOT NULL DEFAULT 0;
// ALTER TABLE buckets ADD COLUMN noncurrent_version_count INTEGER NOT NULL DEFAULT 0;
// ALTER TABLE buckets ADD COLUMN expired_delete_marker_cleanup INTEGER NOT NULL DEFAULT 0;
```

### 规模估计

| 工作项 | 估算行数 |
|--------|---------|
| BucketConfig 扩展 + 迁移 | ~40 行 |
| 新增 repository 方法 | ~40 行 |
| reconcile.Lifecycle 扩展 | ~80 行 |
| Service 层 API 扩展 | ~30 行 |
| REST + S3 协议层 | ~50 行 |
| 测试 | ~80 行 |
| **总计** | **~320 行 + 测试** |

---

## 方向三：MCP 协议纵深（Prompts/Sampling/Roots/Completions）

### 现状

当前 MCP 服务器只实现了 MCP 协议的一个子集：

```go
// internal/mcp/server.go:76-110 — dispatch 方法完整实现
func (s *Server) dispatch(ctx context.Context, req rpcRequest) (any, *rpcError) {
    switch req.Method {
    case "initialize":
        // ✅ 返回 capabilities，但声明了不支持 prompts/sampling/roots
        Capabilities: map[string]any{
            "tools":     map[string]any{"listChanged": false},
            "resources": map[string]any{"listChanged": false, "subscribe": false},
            // ❌ 没有 prompts, sampling, roots, completions
        },
    case "tools/list":    // ✅ 已实现
    case "tools/call":    // ✅ 已实现（6 个工具）
    case "resources/list": // ✅ 已实现（基于 ListObjects）
    case "resources/read": // ✅ 已实现（基于 GetObject）
    case "ping":           // ✅ 已实现
    // ❌ 以下 MCP 方法未实现：
    // prompts/list | prompts/get
    // sampling/createMessage
    // roots/list
    // completions/complete
    }
}
```

**MCP 协议覆盖度：**

| 方法 | 当前状态 | 协议必要性 |
|------|---------|-----------|
| `initialize` | ✅ | 全部实现必须 |
| `ping` | ✅ | 全部实现必须 |
| `tools/list` | ✅ | 核心 |
| `tools/call` | ✅ (6 tools) | 核心 |
| `tools/listChanged` | ❌ 声明 false | 可选 |
| `resources/list` | ✅ | 核心 |
| `resources/read` | ✅ | 核心 |
| `resources/subscribe` | ❌ 声明 false | 可选 |
| **`prompts/list`** | **❌** | **推荐实现** |
| **`prompts/get`** | **❌** | **推荐实现** |
| **`sampling/createMessage`** | **❌** | **可选但高价值** |
| **`roots/list`** | **❌** | **推荐实现** |
| **`completions/complete`** | **❌** | **推荐实现 (UX)** |
| `resources/listChanged` | ❌ 声明 false | 可选 |
| `notifications/*` | ❌ | 可选 |

### 为什么需要

**MCP 协议兼容性：** MCP 生态正在快速成熟（Claude Desktop、VS Code 扩展、Cline、Continue.dev）。客户端在 initialize 阶段读取 capabilities，如果服务器声明不支持 prompts/sampling/roots，这些客户端会选择性地降低功能。当前 aero-vault 仅实现 4/10+ 的 MCP 方法，导致：

| 客户端场景 | 当前体验 | 潜在地 |
|-----------|---------|--------|
| Claude Desktop | 仅工具可用，无预设 prompt | 用户不能通过"需要帮助在上传的文件中搜索什么？"快速开始 |
| 文件浏览 | 只能通过 read_resource 读取 | 不能通过 roots 暴露工作区 |
| LLM 工具循环 | 只能通过 tool call 交互 | 不能通过 sampling 让 LLM 请求更多上下文 |
| CLI/TUI 体验 | 手动输入参数 | 不能通过 completions 获得自动补全 |

**具体产品价值：**

```
prompts: "搜索文件" 预设 prompt、 "分析文档" 预设 prompt
  → 用户无需编写提示词即可使用核心功能

roots: 暴露 "aero-vault://{tenant}/{bucket}/" 作为虚拟根目录
  → 客户端可直接浏览整个文件系统

sampling: 让 LLM 在响应用户前先请求检索更多文件
  → 实现多文件 RAG（当前 agent 只搜索一次）

completions: bucket name 自动补全、key path 自动补全
  → 提升 CLI 和输入体验
```

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/mcp/server.go:76-110` | dispatch 只有 case tools/resources/ping | 无 prompts/sampling/roots/completions |
| `internal/mcp/protocol.go` | 定义了 tool 和 resource 结构体 | 无 prompt/sampling_message/root/completion 结构体 |
| `internal/mcp/server.go:192` | listTools 硬编码工具列表 | 无 prompts/get 逻辑 |
| `internal/mcp/server.go:87` | capabilities 不声明 prompts/sampling/roots | 缺失协议协商 |

### 架构蓝图

```go
// 1. protocol.go 扩展
type prompt struct {
    Name        string `json:"name"`
    Description string `json:"description"`
    Arguments   []promptArgument `json:"arguments,omitempty"`
}

type samplingMessage struct {
    Role    string `json:"role"`
    Content any    `json:"content"`
}

type root struct {
    URI  string `json:"uri"`
    Name string `json:"name"`
}

// 2. server.go 新增
func (s *Server) listPrompts() listPromptsResult { ... }
func (s *Server) getPrompt(ctx, name, args) (any, *rpcError) { ... }
func (s *Server) listRoots(ctx) (any, *rpcError) { ... }
func (s *Server) complete(ctx, params) (any, *rpcError) { ... }

// 3. Sampling 集成
// 通过现有 chat.AnswerStream 实现 createMessage
// LLM 在 agent 循环中请求更多上下文 → 调用 search.Query

// 4. Roots 暴露
// 每个配置的后端暴露为一个 root:
// aero-vault://{tenant}/
// 客户端通过 resources/list 浏览
```

### 规模估计

| 工作项 | 估算行数 |
|--------|---------|
| protocol.go 结构体扩展 | ~50 行 |
| server.go: prompts 实现 | ~80 行 |
| server.go: roots 实现 | ~30 行 |
| server.go: completions 实现 | ~40 行 |
| server.go: sampling 集成 | ~60 行 |
| capabilities 声明更新 | ~10 行 |
| 测试 | ~80 行 |
| **总计** | **~350 行 + 测试** |

---

## 方向四：基于标签的生命周期规则

### 现状

当前生命周期规则只有基于时间的过期：

```go
// internal/repository/repository.go:35 — 当前生命周期模型
ExpireAfterDays int    // 统一过期天数，应用于 bucket 下所有对象
ExpireAction    string // "soft_delete" | "hard_delete"

// internal/reconcile/lifecycle.go — 当前扫描逻辑
// 无条件扫描所有对象，只检查 updated_at < now - days
// ❌ 无法按标签过滤
```

**S3 生命周期规范中缺失的过滤能力：**

```
当前：
  <Rule>
    <Expiration><Days>30</Days></Expiration>   ← 应用于所有对象
  </Rule>

S3 标准：
  <Rule>
    <Filter>
      <Tag>
        <Key>tier</Key>
        <Value>temp</Value>                     ← 仅匹配特定标签的对象
      </Tag>
    </Filter>
    <Expiration><Days>1</Days></Expiration>
  </Rule>

另一条规则：
  <Rule>
    <Filter>
      <Tag>
        <Key>retention</Key>
        <Value>archive</Value>
      </Tag>
    </Filter>
    <Expiration><Days>365</Days></Expiration>
  </Rule>
```

### 为什么需要

**灵活的数据治理：** 标签是最自然的对象分类方式。企业用户通过标签管理数据生命周期：

| 标签 | 生命周期意图 | 当前能否实现 |
|------|------------|------------|
| `tier=temp` | 1 天后自动删除 | ❌ 只能对整个 bucket 设 |
| `tier=log` | 90 天后归档 | ❌ 不支持转换 |
| `retention=7yr` | 7 年后删除 | ❌ 只能对所有对象 |
| `project=alpha` | 项目结束后批量删除 | ❌ 无法按标签选择 |

**数据成本优化：** 没有标签过滤，用户要么为整个 bucket 设置统一的过期策略（过粗），要么为每个生命周期需求创建单独的 bucket（过细，增加管理负担）。标签流策略提供中间粒度。

**S3 兼容性：** 主流 S3 客户端和基础设施工具（Terraform、CloudFormation 等）广泛使用标签生命周期规则。缺失该能力意味着这些工具的 AWS S3 配置无法直接迁移。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/repository/repository.go:44` | `LifecycleFilter` 未定义 | 无标签过滤模型 |
| `internal/repository/sql_buckets.go` | `SetBucketLifecycle` 只存 days+action | 无标签规则序列化 |
| `internal/reconcile/lifecycle.go` | `processObjects` 遍历后统一检查天数 | 无标签匹配逻辑 |
| `internal/service/file_features.go:SetBucketLifecycle` | `days, action` 参数签名 | 不支持 filter |
| `internal/api/s3compat/handler.go` | `putBucketLifecycle` 解析 S3 XML 但丢弃 filter | XML 解析已存在但被忽略 |
| `internal/api/s3compat/xml.go` | `lifecycleRule` 结构体 | 有 Filter 字段但未映射到 repository |

### 架构蓝图

```go
// 生命周期规则扩展
type LifecycleRule struct {
    ID             string
    FilterPrefix   string
    FilterTags     map[string]string  // tag key → value 匹配
    ExpireDays     int
    ExpireAction   string              // "soft_delete" | "hard_delete"
}

// 当 FilterTags 不为空时，扫描逻辑为：
// SELECT FROM objects WHERE tenant=$1 AND bucket=$2
//   AND ($prefix = '' OR key LIKE $prefix || '%')
//   AND tags MATCH filter   -- SQLite JSON 或 Postgres jsonb @>
//   AND updated_at < now - $days

// S3 XML 解析复用已有结构体：
// lifecycleRule.Filter → LifecycleRule.FilterTags + FilterPrefix
// 现有 PutBucketLifecycle handler 已解析 XML，只需映射到新模型
```

### 规模估计

| 工作项 | 估算行数 |
|--------|---------|
| LifecycleRule 模型定义 | ~20 行 |
| DB 迁移（buckets 表扩展） | ~30 行 |
| repository 方法扩展 | ~40 行 |
| reconcile.Lifecycle 标签匹配 | ~50 行 |
| S3 protocol 映射（XML ↔ 模型） | ~40 行 |
| REST API 扩展 | ~30 行 |
| 测试 | ~80 行 |
| **总计** | **~290 行 + 测试** |

---

## 方向五：CompleteMultipart 崩溃安全与跨后端 Copy 原子性

### 现状

**问题一：CompleteMultipart 崩溃安全**

当前 CompleteMultipart 的实现存在 **TOCTOU + 非原子**窗口：

```go
// internal/service/file_multipart.go:93-138 — CompleteMultipart 简化流程
func (s *FileService) CompleteMultipart(ctx context.Context, tenant, bucket, key, uploadID string) (Object, error) {
    // Step 1: 通知存储后端合并分片（网络调用）
    info, err := s.store.CompleteMultipart(ctx, storageKey, uploadID, parts)
    // ⚠️ 如果在 Step 1 之后、Step 2 之前崩溃：
    // - 存储后端中合并后的对象已存在（因为 CompleteMultipart 在 S3/OSS/COS 上
    //   是原子操作，一旦成功不可撤销）
    // - 但数据库中的 uploads 行仍然是 pending 状态
    // - 数据库中 objects 行可能不存在或过时
    // → 重启后：对象可见但 metadata 丢失？或 metadata 指向旧版本？
    
    // Step 2: 写入 metadata（数据库调用）
    obj, err := s.repo.InsertObject(...)
    // ⚠️ 如果 Step 2 失败（数据库 error 或连接断开）：
    // - 存储后端有 blob，但 DB 无记录 → 孤儿 blob
    // - 下次 Reconcile 可能通过孤儿收割清理 → 用户数据丢失
}
```

**问题二：跨后端 Copy 非原子性**

当前 copyObject 是流式复制（读取 → 写入），但 Copy 原语不存在于 `storage.Storage` 接口中：

```go
// internal/api/s3compat/extra.go:39-65 — copyObject 实现
func (h *Handler) copyObject(w http.ResponseWriter, r *http.Request, ...) {
    rc, src, err := h.svc.Get(...)     // 从存储读取
    dst, err := h.svc.Put(..., rc, ...)  // 写入同一存储
    // ❌ Get 和 Put 之间没有事务保证
    // ❌ 如果源和目标在不同后端：需要流式传输
    // ❌ 存储后端没有 Copy 原语 → 大文件需要全量读写
}
```

### 为什么需要

**CompleteMultipart 崩溃安全：** 在分片上传完成后如果服务器崩溃，产生的后果取决于崩溃时机：

| 崩溃时机 | 当前结果 | 期望结果 |
|---------|---------|---------|
| Step 1 前 | ✅ 分片保留，可重试 | ✅ 一致 |
| Step 1 后、Step 2 前 | ❌ 存储有 blob，DB 无记录 → 孤儿 → Reconcile 删除 | **自动恢复** |
| Step 2 中 | ❌ DB 状态不确定 → 部分写入 | **原子提交** |
| Step 2 后 | ✅ 完整 | ✅ 完整 |

**跨后端 Copy：** 当用户使用多后端部署（如 S3 为主、local 为复制目标）时，copyObject 需要流式传输数据。对于 GB 级对象，这意味着：
- 内存或磁盘缓冲（当前使用 unbounded ReadAll？）
- 没有进度追踪
- 没有断点续传

**Server-Side Copy 原语的价值：**

```
S3 存储后端：Copy 是单个 API 调用（不需要流经应用）
  PUT /dest-bucket/dest-key
  x-amz-copy-source: /src-bucket/src-key
  → S3 服务端在内部完成复制，不需要读取数据到应用层

当前实现：不论源和目标是否在同一后端，都流经应用：
  GET /src → 读取到内存 → PUT /dest
  → 大对象（1GB+）的内存压力
  → 不能利用 S3 服务端的原生 Copy API
```

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/service/file_multipart.go:93-138` | CompleteMultipart 非原子 | 无崩溃恢复 |
| `internal/storage/storage.go` | Storage 接口无 Copy 方法 | 无原生 Copy 原语 |
| `internal/storage/s3.go` | S3 实现 | 可用 `CopyObject` API 但未暴露 |
| `internal/api/s3compat/extra.go:39` | copyObject 用 Get+Put | 无存储后端 Copy |
| `internal/repository/sql_uploads.go` | Upload 表 | 无"合并中"标记 |

### 架构蓝图

```go
// 1. Storage 接口扩展
type Storage interface {
    // ... 现有方法 ...
    
    // Copy copies an object within the same backend without streaming
    // through the application. Returns ErrNotSupported when the backend
    // does not support server-side copy.
    Copy(ctx context.Context, srcKey, dstKey string, opts CopyOptions) (ObjectInfo, error)
}

// S3 实现：
func (s *s3Storage) Copy(ctx, srcKey, dstKey, opts) (ObjectInfo, error) {
    // 使用 S3 CopyObject API — 单 API 调用，不读数据
    // × 不需要创建副本
    // × 不需要流经应用
    // × 适用于任意大小对象
}

// Local 实现：
func (s *localStorage) Copy(ctx, srcKey, dstKey, opts) (ObjectInfo, error) {
    // 如果是同一存储根下 → os.Rename 或 io.Copy
    // 否则返回 ErrNotSupported（caller 回落流式复制）
}

// 2. CompleteMultipart 崩溃恢复
func (s *sqlStore) ListUploadsInFlight(ctx, before time.Time) ([]Upload, error) {
    // SELECT * FROM multipart_uploads
    // WHERE status = 'pending'
    // AND updated_at < $1
}

// 3. Reconcile 扩展：staleUploadRecovery
// 扫描 "pending" 但存储后端已完成的 upload
// 对每个：
//   1. 检查存储后端 upload 状态
//   2. 如果合并已完成 → 尝试恢复 metadata
//   3. 如果合并未完成但超时 → abort
```

### 规模估计

| 工作项 | 估算行数 |
|--------|---------|
| Storage.Copy 接口 + S3 实现 | ~80 行 |
| Storage.Copy Local 实现 | ~30 行 |
| 服务层 CopyWithFallback | ~40 行 |
| S3 handler 使用 Copy 原语 | ~30 行 |
| CompleteMultipart 恢复机制 | ~80 行 |
| Reconcile 恢复扫描 | ~50 行 |
| 测试 | ~100 行 |
| **总计** | **~410 行 + 测试** |

---

## 汇总：优先级矩阵

| # | 方向 | 类型 | 影响 | 商业价值 | 实现难度 | 估算规模 | 风险 |
|---|------|------|------|---------|---------|---------|------|
| 1 | SSE-C 客户密钥加密 | 合规/安全 | 🛑 高 | 金融/医疗客户入场券 | 中 | ~355 行 | 低 |
| 2 | 非当前版本生命周期 | 成本/架构 | 🟠 高 | 大规模存储必选项 | 中低 | ~320 行 | 低 |
| 3 | MCP 协议纵深 | 互操作性 | 🟠 中 | MCP 生态覆盖 | 中低 | ~350 行 | 低 |
| 4 | 标签生命周期规则 | 平台能力 | 🟠 中 | 灵活数据治理 | 低 | ~290 行 | 低 |
| 5 | CompleteMultipart 崩溃安全 | 可靠性 | 🟠 高 | 数据完整性基线 | 中 | ~410 行 | 中 |

### 实施建议

**阶段一（高价值快速见效）：** #2（非当前版本生命周期）+ #4（标签生命周期）
- 复用现有 `reconcile.Lifecycle` 框架
- 复用现有 `BucketConfig` + 迁移模式
- 直接解决生产环境中版本爆炸和灵活数据治理两大痛点

**阶段二（合规/安全能力提升）：** #1（SSE-C）
- 独立于现有加密路径，不影响现有 SSE-S3
- 满足合规客户的上线阻塞条件

**阶段三（协议完备性）：** #3（MCP 纵深）
- 增量扩展，不修改现有功能
- 随 MCP 生态发展同步跟进

**阶段四（可靠性加固）：** #5（CompleteMultipart 崩溃安全）
- 需要仔细设计恢复逻辑，避免误恢复
- 建议在压力测试覆盖后进行

---

## 附录：与 ROADMAP 的交叉验证

| ROADMAP 方向（#10） | 本期方向 | 关系 |
|--------------------|---------|------|
| #1 可扩展向量检索 | 无关 | — |
| #2 可观测性 | 无关 | — |
| #3 水平扩展 & HA | 无关 | — |
| #4 运维控制面 | 无关 | — |
| #5 数据完整性 | #5 补充但未覆盖 | 本期 #5 聚焦 CompleteMultipart 路径，ROADMAP #5 聚焦 orphan blob 和 idempotency |
| #6 生产韧性 | 无关 | — |
| #7 S3 特性对等 | #1, #2, #4 属于此范畴 | ROADMAP #7 列表（policy/cors/logging/notification/select/restore/accelerate/tags）未包含 SSE-C、非当前版本生命周期、标签生命周期 |
| #8 内容完整性 | 无关 | — |
| #9 存储分层 | #2 互补 | ROADMAP #9 聚焦 STANDARD→IA→Glacier 转换；本期 #2 聚焦版本历史生命周期 |
| #10 元数据 HA & DR | 无关 | — |

**结论：** 本期 5 个方向中，#1、#2、#3、#4 完全不在 ROADMAP 清单中；#5 与 ROADMAP #5 有互补关系但路径不同。所有方向均通过了 60 轮分析的去重验证。
