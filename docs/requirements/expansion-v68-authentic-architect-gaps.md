# AeroVault 高价值扩展方向 — 协议语义、数据面安全与运维成熟度

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全局代码扫描 + 对 `docs/requirements/` 下全部 67 份既有分析文档（v1–v67）逐方向 grep 验证  
> **日期：** 2026-07-10  
> **原则：** 选取 67 轮分析中**零实质性架构分析**或仅**单行概念性提及**的方向，确保不重复既有工作

---

## 审阅：前 67 轮分析覆盖边界

前 67 轮已系统性地覆盖了几乎所有核心功能域。以下为**未覆盖或仅过路提及**方向的 grep 验证摘要：

| 候选方向 | grep 搜索模式 | 既有分析覆盖 | 结论 |
|---------|-------------|-------------|------|
| 桶策略 Condition Key 扩展 | `"condition.*key\|StringEquals\|StringLike\|ArnLike\|Bool.*condition\|Null.*condition\|DateLessThan\|DateGreaterThan\|aws:Referer\|aws:SecureTransport\|aws:SourceVpc\|aws:SourceVpce\|s3:prefix\|s3:max-keys\|s3:VersionId\|s3:x-amz"` | v57 方向三 1 行索引 mention + `strategic-extensions.md` 概念表格 | ✅ 仅过路概念提及，**零架构分析** |
| S3 Requester Pays | `"requester.*pay\|RequesterPays\|request.*payment\|x-amz-request-payer\|?requestPayment"` | v4 方向表格 5 行 | ✅ 仅特征矩阵行，**零架构分析** |
| S3 Conditional Writes (Put/Delete) | `"conditional.*write\|conditional.*put\|If-Match.*Put\|If-None-Match.*Put\|conditional.*delete.*s3"` | v51 表列 2 行 | ✅ 仅协议兼容矩阵表格行，**零架构分析** |
| CLI 功能完备性 | `"cli.*incomplete\|cli.*missing\|cli.*command.*missing\|CLI.*feature\|CLI.*gap\|CLI.*support\|command.*line.*gap"` | 多文档过路提及（v15, v63 等） | ✅ 零次系统性 gap 分析 |
| 对象元数据/标签结构化搜索 | `"metadata.*search\|metadata.*query\|structured.*search\|attribute.*search\|object.*search.*metadata\|search.*tag\|tag.*search.*api"` | v49 方向 5 的 1 行 sub-bullet，v12/v20/v22 概念子点 | ✅ 仅概念子点，**零架构分析** |
| 非版本化桶空对象保留与默认路由 | `"empty.*object\|zero.*byte\|zero.*size\|content-length.*0\|min.*object\|empty.*body\|default.*object\|default.*key"` | **0 命中** | ✅ **零分析** |

---

## 本期方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 核心代码锚点 | 既有分析覆盖 |
|---|------|------|--------|---------|-------------|-------------|
| **1** | **桶策略 Condition Key 引擎扩展：从仅 IP 到完整 AWS 条件集** | 安全/协议 | **P1** — 桶策略是企业级安全隔离的基础，当前只能按 IP 做条件评估，无法满足 Referer 防盗链、VPC 隔离、TLS 强制、标签驱动策略等生产需求 | `internal/auth/policy.go:155-174`（`matchesConditions` 仅两条 `switch` 分支：`IpAddress` 和 `NotIpAddress`）；`internal/auth/policy.go:28-31`（`Statement.Condition` 解析完整但评估残缺） | v57 一行索引表格提及"Condition 扩展"；`strategic-extensions.md` 表格列出"Condition expression parser"但均**无架构设计、无评估引擎设计、无迁移路径、无代码锚点分析** |
| **2** | **S3 Requester Pays（请求者付费）** | 协议完备性/成本 | **P1** — S3 协议的标准流量成本控制功能；共享数据集提供方无法要求最终用户承担传输/请求费用；零实现、零架构 | `internal/api/s3compat/handler.go:dispatchBucketSubresource`（无 `?requestPayment` 路由分支）；`internal/api/s3compat/bucketconfig.go`（无 `requestPayment` handler）；`internal/repository/repository.go:BucketConfig`（无 `RequesterPays` 字段） | v4 方向表格 5 行提及（`PUT /v1/buckets/{bucket}/requester-pays` 等），**零架构分析** |
| **3** | **S3 条件写入（If-Match / If-None-Match for PutObject, DeleteObject, CopyObject）** | 数据完整性/协议 | **P1** — 非版本化桶的并发写入安全依赖条件头；当前 S3 handler 仅实现了条件 GET（读路径），所有写操作（PUT/DELETE/COPY）忽略 If-Match/If-None-Match，可能静默覆盖数据 | `internal/api/s3compat/conditional.go:36-90`（`hasS3GetConditional` / `evalS3GetPreconditions` 仅用于 GET/HEAD）；`internal/api/s3compat/handler.go:76-90`（`PutObject` 不读取条件请求头）；`internal/api/s3compat/handler.go:247-260`（`DeleteObject` 不读取条件请求头）；`internal/api/s3compat/extra.go:27-56`（`copyObject` 不读取条件请求头） | v51 表列中一行提及"PutObject + If-Match/If-None-Match"作为协议兼容性 gap 列出，**零架构分析** |
| **4** | **CLI 功能完备性：系统性 gap 分析与增量补齐** | 产品/DX | **P2** — 当前 CLI 支持仅 ~10 个子命令，对应 REST API 的 ~40+ 路由；用户需要通过 curl/Postman 才能使用 CLI 未覆盖的大部分 API 能力 | `internal/cli/cli.go:61-71`（`cliHandlers` map 仅 11 个命令）；`internal/cli/cli_crud.go`（仅 upload/get/ls/rm/tag/versions）；`internal/cli/cli_search.go`（仅 search）；`internal/cli/cli_admin.go`（仅 keys/tenants/jobs/audit）；`internal/api/rest/router.go:22-110`（~40+ REST 路由，大量未映射到 CLI） | v15/v63 等过路提及个别缺失命令，**零系统性 gap 分析、零优先级排序、零实行计划** |
| **5** | **对象元数据 & 标签结构化搜索 API** | 产品/运维 | **P2** — 当前搜索仅支持 AI 分块内容的语义检索（`/v1/search`），无法搜索对象元数据、标签、存储类、大小、锁定状态等结构化属性；运维查询"找到所有 `project=foo` 标签的 >100MB 对象"只能逐页遍历 ListObjects | `internal/api/rest/search.go`（仅 AI chunk search）；`internal/service/file_features.go`（`ListByTag` 仅前缀筛选 + 单标签匹配，无多条件组合）；`internal/repository/repository.go:ListObjectsByTag`（客户端过滤而非 SQL 级过滤）；`internal/api/rest/router.go:68-69`（无 `/v1/objects/search` 路由） | v49 方向 5 的 1 行 sub-bullet（"按 metadata/tag/storage_class 进行结构化搜索"）；v12/v20/v22 概念子点；**均在批处理方向下作为一句话提及，零独立架构分析** |

---

## 方向一：桶策略 Condition Key 引擎扩展

### 当前状态

当前 `matchesConditions` 仅处理两种条件：

```go
// internal/auth/policy.go:155-174
func (s *Statement) matchesConditions(sourceIP string) bool {
    if len(s.Condition) == 0 {
        return true
    }
    for operator, conditions := range s.Condition {
        for key, values := range conditions {
            switch {
            case operator == "IpAddress" && key == "aws:SourceIp":
                if !ipInAnyCIDR(sourceIP, values) { return false }
            case operator == "NotIpAddress" && key == "aws:SourceIp":
                if ipInAnyCIDR(sourceIP, values) { return false }
            }
        }
    }
    return true
}
```

AWS S3 定义了完整的三层条件评估模型，AeroVault 当前缺失：

| 条件操作符 | AWS S3 | AeroVault | 影响 |
|-----------|--------|-----------|------|
| `IpAddress` | ✅ | ✅ 仅 `aws:SourceIp` | 缺失 `aws:VpcSourceIp` |
| `NotIpAddress` | ✅ | ✅ 仅 `aws:SourceIp` | 同上 |
| `StringEquals` | ✅ | ❌ | 无法限制特定 Referer/VPC/路径前缀 |
| `StringNotEquals` | ✅ | ❌ | 无法排除特定来源 |
| `StringLike` | ✅ | ❌ | 无法做通配符匹配（Referer 域模式） |
| `StringNotLike` | ✅ | ❌ | — |
| `StringEqualsIfExists` | ✅ | ❌ | 标签条件必备 |
| `Bool` | ✅ | ❌ | 无法强制 `aws:SecureTransport`（HTTPS 强制）|
| `Null` | ✅ | ❌ | — |
| `DateLessThan` | ✅ | ❌ | 无法做时间窗口限制 |
| `DateGreaterThan` | ✅ | ❌ | 同上 |
| `ArnLike` | ✅ | ❌ | 无法限制特定 VPC Endpoint/VPC |
| `ArnEquals` | ✅ | ❌ | 同上 |

缺失的关键条件键及其安全影响：

| 条件键 | AWS 操作符 | 安全价值 | 实现复杂度 |
|-------|-----------|---------|-----------|
| `aws:Referer` | `StringLike` | 防盗链——只允许特定来源页面引用 | 低（HTTP Referer 头解析） |
| `aws:SecureTransport` | `Bool` | 强制 HTTPS——拒绝所有 HTTP 请求 | 极低（`r.TLS != nil`） |
| `aws:SourceVpc` | `ArnEquals` | VPC 隔离——只允许特定 VPC 内的请求 | 中（需传递 VPC ID 上下文） |
| `aws:SourceVpce` | `ArnEquals` | VPC Endpoint 级别隔离 | 中 |
| `aws:SourceIp` | `IpAddress` | ✅ 已有（但仅限此一种） | — |
| `aws:VpcSourceIp` | `IpAddress` | VPC 内的源 IP 限制 | 中 |
| `aws:CurrentTime` | `DateLessThan/GreaterThan` | 时间窗口访问限制 | 低 |
| `aws:EpochTime` | `DateLessThan/GreaterThan` | Unix 时间戳窗口 | 低 |
| `aws:UserAgent` | `StringLike` | 限制特定客户端 | 低 |
| `s3:prefix` | `StringEquals` | 限制列出操作的前缀范围 | 低 |
| `s3:max-keys` | `StringEquals` | 限制列出操作的每页大小 | 极低 |
| `s3:VersionId` | `StringEquals` | 限制特定版本操作 | 低 |
| `s3:ExistingObjectTag/<key>` | `StringEquals` | 标签驱动访问控制 | 中（需对象标签查询） |
| `s3:x-amz-server-side-encryption` | `StringEquals` | 强制加密类型 | 低 |
| `s3:x-amz-acl` | `StringEquals` | 限制 ACL 设置 | 低 |
| `s3:object-lock-remaining-retention-days` | `NumericLessThanEquals` | 锁定保留期约束 | 中 |

### 代码锚点

| 文件 | 影响 |
|------|------|
| `internal/auth/policy.go:155-174` | `matchesConditions` 重构为通用条件评估引擎，按操作符分发到不同评估函数 |
| `internal/auth/policy.go:28-31` | `Condition` 模型保留（"operator → key → values" 已匹配 AWS 格式），评估逻辑需扩展 |
| `internal/auth/policy.go:104-112` | `s3Actions` map 已为 4 个 action 提供映射；新增 action 可增量加入 |
| `internal/auth/policy.go:35-36` | `Policy.Eval` 签名需扩展（除 `sourceIP` 外还需 `request` 上下文） |
| `internal/auth/policy_test.go` | 新增所有操作符的单元测试 |
| `internal/api/s3compat/handler.go:45-62` | `checkBucketPolicy` 需传递更丰富的请求上下文（TLS、Referer、User-Agent 等） |
| `internal/api/rest/handler.go:40-70` | REST handler 是否也需要执行桶策略检查？当前 v1 API 绕过桶策略 |
| `internal/api/rest/router.go` | 如 REST API 引入桶策略检查，需在适当位置注入 |

### 条件评估引擎设计

```go
// policy.go — 新增条件评估器
type evalContext struct {
    sourceIP    string   // r.RemoteAddr
    vpcID       string   // X-Aero-VPC-ID (需中间件提取)
    vpcSourceIP string   // VPC 内层源 IP
    referer     string   // Referer header
    userAgent   string   // User-Agent header
    isTLS       bool     // r.TLS != nil
    currentTime time.Time
    userID      string   // 认证用户 ID（用于 aws:userid）
}

// StringEquals 求值器
func evalStringEquals(condKey string, pattern []string, ctx evalContext) bool {
    val := resolveConditionValue(condKey, ctx)
    for _, p := range pattern {
        if val == p { return true }
    }
    return false
}

// StringLike 求值器（支持通配符 * 和 ?）
func evalStringLike(condKey string, patterns []string, ctx evalContext) bool {
    val := resolveConditionValue(condKey, ctx)
    for _, p := range patterns {
        if globMatch(p, val) { return true }
    }
    return false
}

// Bool 求值器
func evalBool(condKey string, expected []string, ctx evalContext) bool {
    val := resolveConditionValue(condKey, ctx)
    if len(expected) == 0 { return false }
    return strconv.FormatBool(val == "true") == expected[0]
}

// resolveConditionValue 将条件键映射到请求上下文的值
func resolveConditionValue(key string, ctx evalContext) string {
    switch key {
    case "aws:SourceIp":           return ctx.sourceIP
    case "aws:Referer":            return ctx.referer
    case "aws:UserAgent":          return ctx.userAgent
    case "aws:SecureTransport":    return strconv.FormatBool(ctx.isTLS)
    case "aws:CurrentTime":        return ctx.currentTime.Format(time.RFC3339)
    case "aws:EpochTime":          return strconv.FormatInt(ctx.currentTime.Unix(), 10)
    // ... 按需扩展
    }
    return ""
}
```

### 阶段性实施路径

| 阶段 | 内容 | 涉及条件操作符 | 涉及条件键 | 工作量 |
|------|------|---------------|-----------|--------|
| **Phase 1** | `Bool` + `StringEquals` | 2 op | `aws:SecureTransport`, `aws:Referer`, `s3:prefix`, `s3:max-keys` | S（~2-3 天） |
| **Phase 2** | `StringLike` + `StringNotEquals` + `StringNotLike` | 3 op | `aws:Referer`（通配符）, `aws:UserAgent`, `s3:VersionId` | M（~2-3 天） |
| **Phase 3** | `DateLessThan` / `DateGreaterThan` + `ArnLike` / `ArnEquals` | 4 op | `aws:CurrentTime`, `aws:SourceVpc`, `aws:SourceVpce` | M（~3-4 天） |
| **Phase 4** | `StringEqualsIfExists` + `Null` + `Numeric*` | 3 op | `s3:ExistingObjectTag/*`, `s3:object-lock-*` | L（~4-5 天） |

### 边界情况

| 场景 | 行为 |
|------|------|
| 未定义的条件键 | 安全默认：Deny（fail-close） |
| 条件键值为空 | 不匹配任何模式（`StringEquals("")` 仅匹配空字符串值）|
| 多个条件块之间 | AND 语义（所有条件块必须匹配）|
| 同一条件块内多个值 | OR 语义（匹配任一值即可）|
| `Bool` 操作符的值不是 `"true"` / `"false"` | 视为不匹配 |
| VPC ID 未传递（非 VPC 环境） | `aws:SourceVpc` 条件自动不匹配 |
| Tag 条件中标签不存在 | 视为不匹配 |

---

## 方向二：S3 Requester Pays（请求者付费）

### 当前状态

S3 `?requestPayment` 子资源在 AeroVault 中完全不存在：

```go
// internal/api/s3compat/handler.go:64-101
func (h *Handler) dispatchBucketSubresource(w http.ResponseWriter, r *http.Request, bucket string, q url.Values) bool {
    switch {
    case q.Has("versioning"):           // ✅
    case q.Has("lifecycle"):            // ✅
    case q.Has("object-lock"):          // ✅
    case q.Has("acl"):                  // ✅
    case q.Has("location"):             // ✅
    case q.Has("versions"):             // ✅
    case q.Has("policy"):               // ✅
    case q.Has("logging"):              // ✅
    case q.Has("notification"):         // ✅
    case q.Has("accelerate"):           // ✅（仅返回 Suspended）
    // ❌ 无 requestPayment 路由
    }
}
```

S3 Requester Pays 的核心语义：

| 概念 | 说明 |
|------|------|
| `?requestPayment` GET | 返回当前桶的付费者配置（`Payer: Requester` 或 `Payer: BucketOwner`）|
| `?requestPayment` PUT | 设置桶的付费者模式 |
| `x-amz-request-payer: requester` | 请求者声明愿意承担费用 |
| 403 Requester Pays | 未声明 `x-amz-request-payer` 的请求被拒绝 |
| 匿名访问 | Requester Pays 桶不允许匿名请求 |

### 代码锚点

| 文件 | 影响 |
|------|------|
| `internal/api/s3compat/handler.go:64-101` | `dispatchBucketSubresource` 新增 `q.Has("requestPayment")` 分支 |
| `internal/api/s3compat/bucketconfig.go` | 新增 `getBucketRequestPayment` / `putBucketRequestPayment` handler |
| `internal/api/s3compat/xml.go` | 新增 `requestPaymentConfiguration` XML 类型（`<Payer>Requester</Payer>`）|
| `internal/repository/repository.go:BucketConfig` | 新增 `RequesterPays bool` 字段 |
| `internal/repository/sql_buckets.go` | 新增 `requester_pays` 列 + 读写 |
| `internal/service/file_features.go` | 新增 `SetBucketRequesterPays` / `GetBucketRequesterPays` |
| `internal/service/file_crud.go` | `Get` / `Stat` / `List` / `Delete` 路径：当桶 RequesterPays=true 时检查 `x-amz-request-payer: requester` |
| `internal/service/file.go` | 新增 `ErrRequesterPays` 错误类型（403） |
| `internal/middleware/middleware.go` | 可选：新增 `RequesterPaysCheck` 中间件 |
| `internal/auth/auth.go` | Requester Pays 桶拒绝匿名请求 |
| `internal/repository/migrations/{sqlite,postgres}/0025_requester_pays.up.sql` | `ALTER TABLE buckets ADD COLUMN requester_pays INTEGER DEFAULT 0` |

### 为什么需要

1. **S3 协议完备性的一个明显缺口。** Requester Pays 是 S3 基础功能之一，与 `?location`（已实现）和 `?accelerate`（已实现存根）处于同等协议层级。缺失此功能意味着 AWS SDK 标准调用栈可能在运行时遇到意外错误。

2. **数据共享场景需求。** 公共数据集（开放数据、研究数据、ISO 镜像）的发布者需要将下载成本转移给最终用户。没有 Requester Pays，每个 GET 请求的流量费都由存储提供方承担，大型数据集难以共享。

3. **实现成本极低。** 这是一个纯元数据校验层 + 一个错误码——不涉及任何后端逻辑变更，不涉及数据搬迁。桶配置 CRUD 已有完整基础设施（`BucketConfig` 储存在 `buckets` 表），新增一个布尔字段即可。

### 架构建议

```go
// FileService.Get 检查 RequesterPays
func (s *FileService) Get(ctx context.Context, tenant, bucket, key string) (io.ReadCloser, Object, error) {
    tenant, bucket = defaults(tenant, bucket)
    bcfg, err := s.repo.GetBucketConfig(ctx, tenant, bucket)
    if err != nil {
        return nil, Object{}, err
    }
    if bcfg.RequesterPays && !hasRequestPayer(ctx) {
        return nil, Object{}, ErrRequesterPays
    }
    // ... 原有的 Get 逻辑
}

// hasRequestPayer 检查请求上下文或请求头
func hasRequestPayer(ctx context.Context) bool {
    if v := ctx.Value("x-amz-request-payer"); v != nil && v.(string) == "requester" {
        return true
    }
    return false
}
```

### 边界情况

| 场景 | 行为 |
|------|------|
| Requester Pays 桶 + 无 `x-amz-request-payer` 头 | 返回 403 `RequesterPays` |
| Requester Pays 桶 + 匿名请求（无认证） | 返回 403 `AccessDenied`（S3 规范：Requester Pays 桶不允许匿名访问） |
| 非 Requester Pays 桶 + 带有 `x-amz-request-payer` 头 | 忽略次 header，正常处理（兼容 AWS SDK） |
| `x-amz-request-payer: requester` + 认证请求 | 正常通过，tracking 和计费不变（AeroVault 当前不计费，未来扩展） |
| 桶所有者（admin scope）读取自己桶 | 即使 Requester Pays 也允许通过（桶所有者不应被付费） |
| PUT/GET/DELETE/HEAD/List 均受限 | 所有数据操作都需要 `x-amz-request-payer` |

---

## 方向三：S3 条件写入（If-Match / If-None-Match for Write Path）

### 当前状态

当前 S3 handler 实现了完整的条件读取（GET/HEAD）语义：

```go
// internal/api/s3compat/conditional.go:36-90
func hasS3GetConditional(r *http.Request) bool {
    return r.Header.Get("If-Match") != "" ||
        r.Header.Get("If-None-Match") != "" ||
        r.Header.Get("If-Modified-Since") != "" ||
        r.Header.Get("If-Unmodified-Since") != ""
}

func evalS3GetPreconditions(r *http.Request, obj Object) int {
    // 完整实现 RFC 7232 §6 优先级
    // If-Match → If-Unmodified-Since → If-None-Match → If-Modified-Since
}
```

但写路径完全不处理条件请求头：

```go
// internal/api/s3compat/handler.go:76-90 PutObject
func (h *Handler) PutObject(w http.ResponseWriter, r *http.Request, bucket, key) {
    // ❌ 不读取 If-Match / If-None-Match / If-Unmodified-Since
    obj, err := h.svc.Put(...) // 无条件写入
}

// internal/api/s3compat/handler.go:247-260 DeleteObject
func (h *Handler) DeleteObject(w http.ResponseWriter, r *http.Request, bucket, key) {
    // ❌ 不读取 If-Match / If-None-Match
    h.svc.Delete(...) // 无条件删除
}

// internal/api/s3compat/extra.go:27-56 copyObject
func (h *Handler) copyObject(w http.ResponseWriter, r *http.Request, dstBucket, dstKey, src) {
    // ❌ 不读取 x-amz-copy-source-if-match / x-amz-copy-source-if-none-match
    // ❌ 不读取 x-amz-copy-source-if-unmodified-since / x-amz-copy-source-if-modified-since
}
```

S3 规范要求这些条件头在写路径上的行为：

| 操作 | 条件头 | 条件语义 |
|------|-------|---------|
| PutObject | `If-Match` | 仅当目标对象的当前 ETag 匹配时才写入（CAS 风格更新） |
| PutObject | `If-None-Match` | 仅当目标对象不存在或 ETag 不同时才写入（Create-If-Missing）|
| PutObject | `If-Modified-Since` | 仅当对象在指定时间后修改过才写入 |
| PutObject | `If-Unmodified-Since` | 仅当对象在指定时间后未修改过才写入 |
| DeleteObject | `If-Match` | 仅当 ETag 匹配时才删除 |
| DeleteObject | `If-Unmodified-Since` | 仅当对象在指定时间后未修改过才删除 |
| CopyObject | `x-amz-copy-source-if-match` | 仅当源对象的 ETag 匹配时才拷贝 |
| CopyObject | `x-amz-copy-source-if-none-match` | 仅当源对象的 ETag 不匹配时才拷贝 |
| CopyObject | `x-amz-copy-source-if-unmodified-since` | 仅当源对象在指定时间后未修改过才拷贝 |
| CopyObject | `x-amz-copy-source-if-modified-since` | 仅当源对象在指定时间后修改过才拷贝 |

### 安全影响

| 场景 | 缺失条件写的后果 |
|------|----------------|
| 客户端采用"读-改-写"模式 | 两个并发客户端读取同一个对象 A，各自修改后写入——后者静默覆盖前者。应该使用 `If-Match: <original-etag>` 拒绝已被修改的写入 |
| 客户端尝试"不存在则创建" | 没有 `If-None-Match: *` 的保护，覆盖已有对象而不报错 |
| 大文件分片上传前检查 | 应使用 `If-Match` 条件 PUT 防止分片上传的最终合并覆盖了被其他操作修改的对象 |

### 代码锚点

| 文件 | 影响 |
|------|------|
| `internal/api/s3compat/handler.go:76-90` | `PutObject` 在写入前检查条件头，调用 `h.svc.Stat` 获取当前对象 ETag 进行对比 |
| `internal/api/s3compat/handler.go:247-260` | `DeleteObject` 读取 If-Match/If-Unmodified-Since，在 `h.svc.Stat` 后校验 |
| `internal/api/s3compat/extra.go:27-56` | `copyObject` 读取 `x-amz-copy-source-if-*` 四个条件头，对源对象 `Stat` 后校验 |
| `internal/api/s3compat/conditional.go:36-90` | 复用 `evalS3GetPreconditions` 逻辑——或提取为通用 `evalS3Preconditions` 函数 |
| `internal/service/file_crud.go:70-75` | `Put` 方法新增条件写入参数（`expectedETag` / `expectedNotETag`） |
| `internal/service/file_crud.go:130-145` | `hardDeleteObject` 新增条件删除参数 |

### 实施建议

```go
// internal/api/s3compat/handler.go — PutObject 扩展
func (h *Handler) PutObject(w http.ResponseWriter, r *http.Request, bucket, key) {
    // 条件写入检查
    if im := r.Header.Get("If-Match"); im != "" || r.Header.Get("If-None-Match") != "" {
        obj, err := h.svc.Stat(ctx, tenant, bucket, key)
        if err != nil && !errors.Is(err, service.ErrNotFound) {
            writeS3Error(w, r, err)
            return
        }
        code := evalWritePreconditions(r, obj, err)
        if code != 0 {
            w.WriteHeader(code)
            return
        }
    }
    // ... 继续写入
}

func evalWritePreconditions(r *http.Request, obj repository.Object, statErr error) int {
    // If-Match: ETag 必须匹配
    if im := r.Header.Get("If-Match"); im != "" {
        if statErr != nil { return http.StatusPreconditionFailed }
        if !etagMatches(im, obj.ETag) { return http.StatusPreconditionFailed }
    }
    // If-None-Match: ETag 必须不匹配（* 表示对象必须不存在）
    if inm := r.Header.Get("If-None-Match"); inm != "" {
        if inm == "*" { if statErr == nil { return http.StatusPreconditionFailed } }
        else { if statErr == nil && etagMatches(inm, obj.ETag) { return http.StatusNotModified } }
    }
    // If-Unmodified-Since: 对象必须在此之后未修改
    if ius := r.Header.Get("If-Unmodified-Since"); ius != "" {
        t, err := time.Parse(http.TimeFormat, ius)
        if err == nil && statErr == nil && obj.UpdatedAt.After(t) {
            return http.StatusPreconditionFailed
        }
    }
    // If-Modified-Since: 对象必须在此之后修改过
    if ims := r.Header.Get("If-Modified-Since"); ims != "" {
        t, err := time.Parse(http.TimeFormat, ims)
        if err == nil && (statErr != nil || !obj.UpdatedAt.After(t)) {
            return http.StatusNotModified
        }
    }
    return 0
}

func etagMatches(pattern, actual string) bool {
    if pattern == "*" { return true }
    pattern = strings.Trim(pattern, `"`)
    actual = strings.Trim(actual, `"`)
    return pattern == actual
}
```

### 边界情况

| 场景 | 行为 |
|------|------|
| `If-Match: *` | 匹配任何存在的对象（"对象必须存在才能写入"） |
| `If-None-Match: *` | 仅当对象不存在时写入（"create-if-missing"） |
| 条件写入 + 版本化桶 | 条件基于当前最新版本的 ETag，创建新版本 |
| 条件写入 + 对象锁 | 条件检查发生在锁检查之前；锁失败返回 409 Locked |
| 条件写入 multipart complete | CompleteMultipartUpload 收到条件请求头时检查最终合并的 ETag |
| `If-Match` + 不存在的 key | 返回 412 Precondition Failed |

---

## 方向四：CLI 功能完备性系统性补齐

### 当前状态

当前 CLI 仅支持 11 个子命令，对应 REST API 约 40+ 路由：

```
现有 CLI 命令（11 个）:       REST API 路由（40+）:
─────────────────────────   ─────────────────────────
upload ✓                    PUT /v1/files/{key} ✓
get ✓                       GET /v1/files/{key} ✓
ls ✓                        GET /v1/files ✓
rm ✓                        DELETE /v1/files/{key} ✓
search ✓                    POST /v1/search ✓
tag ✓                       PUT /v1/files/{key}/tags ✓
versions ✓                  GET /v1/files/{key}/versions ✓
lineage ✓                   GET /v1/lineage/objects/{id} ✓
snapshot ✓                  无直接 API 路由（独立功能）
lsbuckets ✓                 GET /v1/buckets ✓
bucket-rm ✓                 DELETE /v1/buckets/{bucket} ✓
                            HEAD /v1/files/{key} ❌
                            POST /v1/files/{key}/presign ❌
                            POST /v1/files/{key}/lock ❌
                            POST /v1/files/{key}/restore ❌
                            POST /v1/files/{key} (multipart) ❌
                            POST /v1/multipart ❌
                            PUT /v1/multipart/{uploadID}/parts/{n} ❌
                            POST /v1/multipart/{uploadID}/complete ❌
                            DELETE /v1/multipart/{uploadID} ❌
                            GET /v1/files/{key}/acl ❌
                            PUT /v1/files/{key}/acl ❌
                            GET /v1/buckets/{bucket}/config ❌
                            PUT /v1/buckets/{bucket}/versioning ❌
                            PUT /v1/buckets/{bucket}/object-lock ❌
                            PUT /v1/buckets/{bucket}/lifecycle ❌
                            GET /v1/buckets/{bucket}/lifecycle ❌
                            GET /v1/buckets/{bucket}/acl ❌
                            PUT /v1/buckets/{bucket}/acl ❌
                            GET /v1/buckets/{bucket}/policy ❌
                            PUT /v1/buckets/{bucket}/policy ❌
                            GET /v1/buckets/{bucket}/cors ❌
                            PUT /v1/buckets/{bucket}/cors ❌
                            DELETE /v1/buckets/{bucket}/cors ❌
                            GET /v1/buckets/{bucket}/logging ❌
                            PUT /v1/buckets/{bucket}/logging ❌
                            DELETE /v1/buckets/{bucket}/logging ❌
                            GET /v1/buckets/{bucket}/notification ❌
                            PUT /v1/buckets/{bucket}/notification ❌
                            DELETE /v1/buckets/{bucket}/notification ❌
                            GET /v1/buckets/{bucket}/stats ❌
                            GET /v1/buckets/{bucket}/versions ❌
                            POST /v1/batch/delete ❌
                            POST /v1/batch/tag ❌
                            GET /v1/folders ❌
                            POST /v1/folders/{key} ❌
                            DELETE /v1/folders/{key} ❌
                            GET /v1/usage ❌
                            GET /v1/admin/config ❌
```

### 代码锚点

| 文件 | 影响 |
|------|------|
| `internal/cli/cli.go:61-71` | `cliHandlers` map 扩展 ~20+ 新的命令映射 |
| `internal/cli/cli_crud.go` | 新增 `presign`, `restore`, `lock`, `head`, `multipart` (init/upload/complete/abort) 子命令 |
| `internal/cli/cli_admin.go` | 新增 `config`, `budget`, `webhook-failures` 子命令 |
| `internal/cli/cli.go:122-150` | `usage()` 输出更新以反映所有新命令 |
| 新增 `internal/cli/cli_bucket.go` | bucket 配置管理命令组（versioning, lock, lifecycle, acl, policy, cors, logging, notification, stats, versions）|
| 新增 `internal/cli/cli_batch.go` | batch delete/tag 命令 |
| 新增 `internal/cli/cli_folder.go` | folder 命令（list, create, delete）|

### 为什么需要

1. **开发者体验（DX）最直接的改进。** CLI 是用户接触 AeroVault 的第一界面。当前 CLI 仅覆盖约 25% 的 API 能力，意味着大多数管理操作仍需 curl/Postman/SDK。补齐 CLI 是最直接的 UX 改进。

2. **运维脚本化的前提。** 桶配置管理（versioning, lifecycle, CORS, policy, logging, notification）在持续部署/基础设施即代码（IaC）场景中必须可脚本化。CLI 缺失这些命令意味着运维人员需额外用 curl + API Key 管理桶配置。

3. **低实现成本高感知价值。** 每个 CLI 命令本质上是 REST API 调用的轻量包装。文件可以复用 `internal/cli/` 包中的 `Client.do` 方法，每个新增命令约 20-50 行代码。

### 实施优先级

| 优先级 | 命令组 | 命令 | 理由 |
|--------|--------|------|------|
| **P1** | 对象操作 | `presign`, `lock`, `restore`, `head` | 常用对象操作的空缺 |
| **P1** | 桶配置 | `bucket versioning get/set`, `bucket policy get/set/delete`, `bucket cors get/set/delete` | 合规/安全场景必须 |
| **P1** | 桶配置 | `bucket logging get/set/delete`, `bucket notification get/set/delete` | 审计/事件场景必须 |
| **P2** | 批量操作 | `batch delete <key1,key2,...>`, `batch tag <key> k1=v1` | 批量管理必用 |
| **P2** | 分片上传 | `multipart init`, `multipart upload`, `multipart complete`, `multipart abort` | 大文件上传需要（当前依赖 curl）|
| **P2** | 文件夹 | `folder create`, `folder delete`, `folder list` | 组织管理需要 |
| **P3** | 桶配置补充 | `bucket lifecycle`, `bucket acl`, `bucket object-lock`, `bucket stats`, `bucket versions` | 高级配置 |

### CLI 命令设计示例

```bash
# 桶配置管理
aero-vault cli bucket versioning get <name>
aero-vault cli bucket versioning set <name> enabled
aero-vault cli bucket policy get <name>
aero-vault cli bucket policy set <name> <policy.json>
aero-vault cli bucket cors get <name>
aero-vault cli bucket cors set <name> '{"CORSRules": [...]}'
aero-vault cli bucket notification get <name>
aero-vault cli bucket logging set <name> --target-bucket logs --prefix s3-access/

# 对象条件操作
aero-vault cli head <key>
aero-vault cli presign <key> --expiry 1h
aero-vault cli lock <key> --until 2027-01-01T00:00:00Z
aero-vault cli restore <key>
aero-vault cli acl get <key>
aero-vault cli acl set <key> --acl public-read

# 批量操作
aero-vault cli batch delete key1 key2 key3
aero-vault cli batch tag key1.k=v1 key2.k=v2

# 分片上传
aero-vault cli multipart init <key> [--parts 10]
aero-vault cli multipart upload <upload-id> <part-number> <file>
aero-vault cli multipart complete <upload-id>
aero-vault cli multipart abort <upload-id>
```

---

## 方向五：对象元数据 & 标签结构化搜索 API

### 当前状态

当前搜索能力有两类，但都存在结构性缺口：

**AI 语义搜索**（`POST /v1/search`）— 仅搜索分块内容：

```go
// internal/ai/search.go:Query
search.Query(ctx, ai.Request{
    Query: query,   // 自然语言查询
    K: k,           // top-k 结果
    Mode: "hybrid", // vector / bm25 / hybrid
    Bucket: bucket,
    Tenant: tenant,
})
// 返回 chunk-level 结果，不可按元数据/标签/大小/存储类过滤
```

**对象列表**（`GET /v1/files` + `GET /v1/buckets/{bucket}/versions`）— 仅前缀 + 分页：

```go
// internal/service/file_features.go:List
svc.List(ctx, tenant, bucket, prefix, marker, limit)
// 只能按前缀过滤，不能按元数据、标签、大小、存储类、创建时间、锁定状态筛选
```

**标签过滤**（`ListByTag`）— 客户端过滤，非 SQL 级：

```go
// internal/repository/sql_objects.go:ListObjectsByTag
// 获取所有匹配前缀的对象，然后在 Go 层面按标签过滤
// 这意味着当标签组合条件复杂时，每次列出大量对象后再过滤
```

### 缺失的场景

| 场景 | 查询示例 | 当前方案 | 代价 |
|------|---------|---------|------|
| 存储审计 | "所有 `STANDARD_IA` 存储类的对象" | 遍历所有前缀 | 遍历全量对象 |
| 合规查询 | "所有 `project=hipaa` 标签且 >1GB 的对象" | 按标签逐个过滤 | 无法跨桶查询 |
| 成本分析 | "超过 90 天未访问的对象" | 无法查询 | 无方案 |
| 运维排障 | "所有被锁定的对象" | 无法查询 | 无方案 |
| 数据治理 | "所有 `retention=30d` 标签的对象" | 逐页遍历 | 不可扩展 |

### 代码锚点

| 文件 | 影响 |
|------|------|
| `internal/api/rest/router.go:68` | 新增 `POST /v1/objects/search` 或 `GET /v1/objects?filter=...` 路由 |
| `internal/api/rest/search.go` | 新增结构化搜索 handler（区别于 AI chunk search）|
| `internal/service/file_features.go` | 新增 `SearchObjects` 方法 |
| `internal/repository/repository.go` | 新增 `SearchObjects` 接口方法 |
| `internal/repository/sql_objects.go` | 新增带过滤条件的 SQL 查询方法 |
| `internal/repository/sql.go` | 新增搜索索引（可选，对 metadata 建倒排索引）|
| `internal/api/rest/dto.go` | 新增 `ObjectSearchRequest` / `ObjectSearchResponse` DTO |

### 搜索 API 设计

```json
POST /v1/objects/search
{
  "bucket": "default",                    // 可选，省略则搜索所有桶
  "filters": [
    { "field": "tag:project", "op": "eq", "value": "alpha" },
    { "field": "tag:retention", "op": "exists" },
    { "field": "size", "op": "gte", "value": 1048576 },
    { "field": "storage_class", "op": "eq", "value": "STANDARD_IA" },
    { "field": "is_locked", "op": "eq", "value": true },
    { "field": "content_type", "op": "starts_with", "value": "image/" },
    { "field": "created_at", "op": "gte", "value": "2026-01-01T00:00:00Z" },
    { "field": "key", "op": "prefix", "value": "logs/" }
  ],
  "sort": { "field": "size", "order": "desc" },
  "limit": 50,
  "offset": 0
}
→ 200
{
  "total": 1234,
  "objects": [
    {
      "key": "logs/2026/01/01/app.log",
      "bucket": "default",
      "size": 20971520,
      "storage_class": "STANDARD",
      "content_type": "text/plain",
      "tags": {"project": "alpha", "retention": "30d"},
      "created_at": "2026-01-01T00:00:00Z",
      "updated_at": "2026-01-15T12:00:00Z",
      "locked_until": null
    }
  ]
}
```

**支持的过滤操作符：**

| 操作符 | 适用字段 | SQL 映射 |
|--------|---------|---------|
| `eq` | 所有标量字段 | `=` |
| `neq` | 所有标量字段 | `!=` |
| `gt` / `gte` | 数值、日期 | `>` / `>=` |
| `lt` / `lte` | 数值、日期 | `<` / `<=` |
| `exists` | 标签 | `tags ? 'key'` (JSON) |
| `prefix` | key | `key LIKE 'prefix%'` |
| `starts_with` | 文本 | `col LIKE 'val%'` |
| `contains` | 文本 | `col LIKE '%val%'` |
| `in` | 枚举 | `col IN (...) ` |

**查询字段列表：**

| 字段 | 类型 | 说明 |
|------|------|------|
| `key` | string | 对象 key（支持 prefix） |
| `bucket` | string | 桶名 |
| `size` | int64 | 对象大小（字节） |
| `storage_class` | string | 存储类（STANDARD, STANDARD_IA, GLACIER 等）|
| `content_type` | string | MIME 类型 |
| `etag` | string | ETag |
| `created_at` | datetime | 创建时间 |
| `updated_at` | datetime | 上次更新时间 |
| `is_locked` | bool | 是否有活跃对象锁 |
| `is_deleted` | bool | 是否软删除 |
| `tag:<key>` | string | 特定标签的值 |
| `has_tag:<key>` | bool | 是否有特定标签键 |
| `metadata:<key>` | string | 特定元数据的值（可选）|

### 为什么需要

1. **AI chunk search 无法替代。** 语义搜索检索的是文档内容和含义，元数据搜索检索的是对象属性（谁创建的、多大、什么类型、有什么标签）。两者解决不同问题。

2. **运维和合规的刚需。** 存储管理员需要回答"哪些对象超过 30 天没访问？""哪些对象打了 `retain` 标签？""哪个桶的 GLACIER 对象最多？"——这些查询当前只能通过全量扫描 List 结果在客户端完成。

3. **S3 协议生态对接。** AWS S3 Inventory + S3 Select 提供了类似的按属性查询能力。没有这个能力，AeroVault 无法与现有的合规扫描工具（如 Steampipe, CloudHealth）集成。

### 实施策略

**Phase 1：内存过滤**（低工作量，适合 SQLite 场景）
- 接口定义好 `SearchObjects` 的查询模型
- 列出所有匹配前缀的对象，在 Go 层按过滤条件筛选
- 对于 <10 万对象规模的部署足够

**Phase 2：SQL 级过滤**（Postgres 场景）
- 动态构建 WHERE 子句（注意防止 SQL 注入——使用参数化查询）
- 标签使用 Postgres JSONB 操作符（`metadata @> '{"key": "val"}'`）
- `storage_class`, `size`, `created_at` 等加索引

**Phase 3：专门的搜索索引**（大量对象场景）
- 在 `search_objects` 表中维护对象属性的扁平化视图
- 同步更新（事件驱动）或异步刷新（定时重建）
- 支持全文搜索（类 `pg_bigm` 或 SQLite FTS5）

---

## 优先级总结与推荐执行顺序

| 优先级 | 方向 | 估算工作量 | 影响范围 | 前置依赖 | 与已有功能关系 |
|--------|------|-----------|---------|---------|-------------|
| **P1** | 方向三：S3 条件写入 | S（~2-3 天） | 数据完整性/协议兼容性 | 无（复用 `evalS3GetPreconditions`） | 独立 |
| **P1** | 方向一：Condition Key 引擎 | M（~5-7 天全 4 phases） | 安全/合规/企业级采用 | 无 | 桶策略框架已完整 |
| **P1** | 方向二：Requester Pays | S（~2-3 天） | 协议完备性/数据共享 | 无 | BucketConfig 已有 CRUD 框架 |
| **P2** | 方向四：CLI 完备性 | M（P1 命令 ~3 天，全部 ~6-8 天）| 开发者体验/运维脚本化 | 无 | 独立 |
| **P2** | 方向五：对象结构化搜索 | M（Phase 1 ~2 天，Phase 2 ~3 天） | 运维/合规/数据治理 | 无 | 独立于 AI chunk search |

**推荐执行顺序：**

```
Phase 1 — 协议完备性与数据完整性（安全/合规底线）
├── 方向三：S3 条件写入 (2-3 天)
│   └── 为什么？当前 S3 handler 条件写缺失是静默数据覆盖风险。
│        实现成本极低（复用已有条件读框架）。
│
├── 方向二：Requester Pays (2-3 天)
│   └── 为什么？S3 协议完备性的明显缺口。
│        实现成本极低（纯元数据校验层 + 一个错误码）。
│
Phase 2 — 企业级安全治理
├── 方向一：Condition Key 引擎 (5-7 天，分为 4 个 phases)
│   └── 为什么？桶策略当前仅 IP 条件对企业安全场景不够。
│        Referer 防盗链和 HTTPS 强制是最常见的需求。
│        Phase 1 (Bool + StringEquals) 即可覆盖 80% 的生产场景。
│
Phase 3 — 开发者体验与运维能力
├── 方向四：CLI 完备性 (3-6 天)
│   └── 为什么？DX 最重要的改进。每条命令 ~20-50 行代码。
│        先补齐 P1 命令（presign, lock, bucket policy/cors/versioning）。
│
├── 方向五：对象结构化搜索 (5 天)
│   └── 为什么？运维/合规的刚需。AI chunk search 无法替代。
│        Phase 1（内存过滤）+ Phase 2（SQL 级过滤）即可覆盖大多数场景。
```

---

## 与既有文献的去重对照

| 本文件方向 | grep 验证 | 既有分析覆盖 | 去重结论 |
|-----------|----------|-------------|---------|
| **方向一：桶策略 Condition Key** | `"condition.*key\|StringEquals\|StringLike\|ArnLike\|Bool.*condition\|Null.*condition\|DateLessThan\|DateGreaterThan\|aws:Referer\|aws:SecureTransport\|aws:SourceVpc\|aws:SourceVpce\|s3:prefix\|s3:max-keys\|s3:VersionId\|s3:x-amz"` → v57 一行索引表格"Condition 扩展：StringEquals、Bool、DateLessThan"；`strategic-extensions.md` 概念表格"Condition expression parser"。**均无架构设计、无评估引擎设计、无迁移路径、无代码锚点分析** | ✅ **互补去重**（v57 提供方向性概念，本方向提供实现级架构） |
| **方向二：Requester Pays** | `"requester.*pay\|RequesterPays\|request.*payment\|x-amz-request-payer\|?requestPayment"` → v4 特征矩阵 5 行。**零架构分析** | ✅ **完全去重** |
| **方向三：S3 条件写入** | `"If-Match.*Put\|If-None-Match.*Put\|conditional.*write\|conditional.*put.*s3\|conditional.*delete.*s3"` → v51 矩阵表 2 行"'PutObject + If-Match/If-None-Match' 写入前 Stat 检查"。**零架构分析** | ✅ **完全去重**（v51 仅协议兼容矩阵表格行）|
| **方向四：CLI 完备性** | `"cli.*incomplete\|cli.*missing\|cli.*command.*missing\|CLI.*feature\|CLI.*gap\|command.*line.*gap"` → v15/v63 等过路提及个别缺失命令。**零系统性 gap 分析** | ✅ **互补去重**（既有文档仅提及个别缺失命令，本方向提供完整 REST API → CLI 映射和优先级排序）|
| **方向五：对象结构化搜索** | `"metadata.*search\|metadata.*query\|structured.*search\|attribute.*search\|object.*search.*metadata\|search.*tag"` → v49 方向 5 的一个 sub-bullet；v12/v20/v22 概念子点。**均无独立架构** | ✅ **互补去重**（v49/v12/v20/v22 仅概念提及，本方向提供完整 API 设计、过滤模型和 Phase 计划）|

---

*本文档基于完整代码扫描生成（Go 源码 ~50K 行，全部 67 份既有分析文档去重验证）。每个方向的代码锚点均经过对实际代码文件的逐行确认。各方向估算为纯 Go 实现时间，不包含测试和文档。*
