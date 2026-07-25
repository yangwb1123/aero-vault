# AeroVault 资深架构师/产品经理视角 — 第 83 轮：协议纵深与数据完整性盲区

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 23+ 子包，三套 SDK，MCP 双模式，Web UI，48 对迁移文件，deploy/ 配置，Makefile，CI gate）  
> **去重验证：** 对 `docs/requirements/` 下全部 82 份既有分析文档逐方向进行 `grep` 正则交叉验证 + 语义比对 + 代码锚点映射。标注每个方向在 82 份文档中的命中计数字段。  
> **日期：** 2026-07-11  
> **核心原则：** 选取代码中存在具体、可量化、且在前 82 轮分析中 **零实质性架构分析** 的系统盲区。每个方向包含代码锚点、影响分析、既有覆盖证明、边界情况枚举。

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 | 既有分析覆盖 |
|---|------|------|--------|---------|---------|-------------|
| **1** | **S3 ListObjects `delimiter`/`CommonPrefixes` 参数缺失** | 协议合规/互操作 | **P1** — S3 ListObjects API 完全忽略 `?delimiter` 参数，响应用不包含 `CommonPrefixes` 节点。所有 SDK 的目录式列举（`aws s3 ls s3://bucket/dir/`、boto3 `list_objects`、AWS SDK for Go `ListObjectsInput.Delimiter`）均依赖此行为，导致文件夹浏览静默失败 | `internal/api/s3compat/handler.go:449-545`（`listObjects` / `listObjectsV2` / `listObjectsV1` — 读 `prefix`/`marker`/`max-keys` 但不读 `delimiter`）；`internal/api/s3compat/xml.go:10-38`（`listBucketResult` / `listBucketResultV1` 结构体无 `Delimiter`/`CommonPrefixes` 字段）；`internal/repository/sql_objects.go:ListObjects`（SQL 查询仅按 `key LIKE prefix%` 过滤，无按分隔符分组逻辑） | ✅ **完全去重**（`grep -rln "delimiter\|CommonPrefixes\|common.*prefix" docs/requirements/` → **0 命中**。82 份 doc 零分析此方向） |
| **2** | **SigV4 `x-amz-content-sha256` 请求正文完整性未验证** | 安全/数据完整性 | **P1** — SigV4 签名验证读取 `x-amz-content-sha256` 头部参与签名计算，但从未验证实际请求正文是否与此哈希匹配。攻击者可篡改正文（截断、替换）并保持签名通过；网络传输中静默的正文截断也可绕过检测 | `internal/auth/sigv4.go:83-118`（`Verify` 从头部读取 `payloadHash` 并计入签名——**无正文实际哈希计算和比对**）；`internal/auth/sigv4_chunk.go:48-75`（分块传输的 signature 逐块计算但 **不校验各块内容的实际哈希**）；`internal/api/s3compat/sigv4_test.go:60-90`（测试仅验证签名可验证，**不测试正文与哈希不一致**）；`internal/api/rest/handler.go:250-270`（REST `Content-MD5` 在 Put 时被校验，S3 路径无等价保护） | ✅ **完全去重**（`grep -rln "x-amz-content-sha256\|payload.*integrity\|content.*sha256.*verif\|正文.*哈希\|签名.*正文" docs/requirements/` → **0 命中**。82 份 doc 零分析此方向） |
| **3** | **ETag 响应格式：HTTP 头部带引号 vs JSON 响应裸值不一致** | API 一致性/协议合规 | **P2** — HTTP 响应头部中 ETag 按 RFC 7232 要求包装引号（`ETag: "abc123"`），但 JSON 响应体（REST `/v1/files`、`/v1/search`）中 `etag` 字段返回裸值（`"etag": "abc123"`），不带额外引号。S3 兼容客户端通常期望两种路径格式一致；用 JSON 响应中的值直接构造 `If-Match`/`If-None-Match` 请求头会因引号不匹配导致条件请求失败 | `internal/api/rest/handler.go:145-232`（HTTP header 全部 `w.Header().Set("ETag", `"`+obj.ETag+`"")` — 正确加引号）；`internal/api/rest/handler.go:679-713`（JSON 响应结构 `ObjResponse.ETag string` 序列化后为裸值 `"etag":"abc"` 而非 `"etag":"\"abc\""`）；`internal/api/rest/handler.go:898-906`（`searchHit` JSON 同样裸值）；`internal/api/s3compat/handler.go:640-647`（S3 `writeObjectHeaders` 加引号）；`internal/api/s3compat/xml.go:39-44`（S3 XML 也加引号 `ETag: '"' + o.ETag + '"'`） | ✅ **完全去重**（`grep -rln "etag.*consistency\|etag.*quote\|ETag.*format\|etag.*JSON\|etag.*header.*diff\|etag.*response.*format" docs/requirements/` → v73 表格一行路过列出 **"ETag 读取验证"** 标题行——**零架构分析、零代码锚点、零影响评估**。其余 81 份 doc 零覆盖） |
| **4** | **Bucket 命名规则与对象 Key 合法性校验缺失** | 数据完整性/平台健壮性 | **P2** — `CreateBucket` 不检查 S3 桶命名规则（3-63 字符、仅小写字母数字连字符、非 IP 格式），允许创建与已有 DNS/S3 规则冲突的桶名。对象 Key 仅做最小校验（禁空、禁 `/` 开头、禁 `..`），不检查最大长度（S3 限制 1024 字节）、不检查不可打印字符、不检查控制字符——某些存储后端（S3/OSS）会静默拒绝或截断 | `internal/service/file.go:CreateBucket`（直接调用 `repo.CreateBucket`，**零前置校验**）；`internal/repository/sql_buckets.go:CreateBucket`（INSERT 语句，SQL 层无 CHECK 约束）；`internal/service/file.go:130-136`（`validateKey` 仅拒绝空/`/`开头/`..`，无长度上限/字符集校验）；`internal/storage/s3.go:Put`（AWS SDK 可能因 key 含特殊字符返回 400/403——**未提前预防**）；`internal/storage/storage.go:15`（`ErrInvalidKey` 已定义但 **从未被 validateKey 引用**） | ✅ **完全去重**（`grep -rln "bucket.*naming\|bucket.*validation.*name\|bucket.*name.*rule\|bucket.*name.*constraint\|CreateBucket.*validat\|桶名.*校验" docs/requirements/` → **0 命中**。v82 分析 GetBucketLocation 返回硬编码空值——与本方向正交。其余 81 份 doc 零覆盖） |
| **5** | **Webhook/HTTP 客户端连接池未配置导致连接抖动** | 性能/运维健康 | **P2** — Webhook 递送、Antivirus 扫描、AI Embed/LLM/Reranker HTTP 调用、KMS SSE 调用、Event Transport Postgres LISTEN/NOTIFY 等全部使用 `http.DefaultClient` 或 `&http.Client{}` 无连接池配置。高并发下每次请求创建新 TCP 连接，导致大量 TIME_WAIT 端口耗尽、TLS 握手开销、延迟抖动。S3 存储后端具有池化配置但仅用于自身 | `internal/events/webhook.go:40`（`&http.Client{Timeout: 5 * time.Second}` — 无 `Transport`、无 `MaxIdleConns`、无 `IdleConnTimeout`）；`internal/ai/embedder.go:buildHTTPClient`（embedder 用 `http.Client{}` 零配置）；`internal/ai/llm.go:buildHTTPClient`（LLM 客户端同）；`internal/ai/rerank.go:buildHTTPClient`（Reranker 同）；`internal/antivirus/antivirus.go:NewHTTPScanner`（`&http.Client{}` 零配置）；`internal/storage/kms.go:NewKMSClient`（`&http.Client{}` 零配置）；`internal/storage/s3.go:NewHTTPClient`（仅 S3/OSS/COS backend 通过 `TimeoutConfig` 配置连接池——但也仅是 `http.Transport` 基础设置） | ✅ **完全去重**（`grep -rln "http.*client.*pool\|connection.*pool\|MaxIdleConns\|IdleConnTimeout\|DisableKeepAlives\|连接池\|TCP.*TIME_WAIT" docs/requirements/` → v14 以一行提及 S3 存储使用的 HTTP 客户端超时可配置；v65 方向四提出"嵌入器连接池"作为子观点——聚焦 AI 嵌入器单一场景、**零全系统 HTTP 连接池审查**。其余 80 份 doc 零覆盖） |

---

## 方向一：S3 ListObjects `delimiter`/`CommonPrefixes` 参数缺失

### 现状

当前 S3 兼容 API 支持 ListObjects v1 和 v2，但只做扁平列举：

```go
// internal/api/s3compat/handler.go:449-545
func (h *Handler) listObjectsV2(w http.ResponseWriter, r *http.Request, bucket string) {
    q := r.URL.Query()
    prefix := q.Get("prefix")
    // delimiter = q.Get("delimiter")   ← 不存在
    token := q.Get("continuation-token")
    // ...
    page, err := h.svc.List(ctx, tenant, bucket, prefix, token, maxKeys)
    // ...
    out := listBucketResult{
        // Delimiter:   delimiter,        ← 不存在
        // CommonPrefixes: ...            ← 不存在
    }
    for _, o := range page.Objects {
        out.Contents = append(out.Contents, listContent{...})
    }
}
```

响应 XML 缺少 `<CommonPrefixes>` 节点：

```xml
<!-- 当前: -->
<ListBucketResult>
  <Name>my-bucket</Name>
  <Contents><Key>dir-a/file1.txt</Key>...</Contents>
  <Contents><Key>dir-a/file2.txt</Key>...</Contents>
  <Contents><Key>dir-b/file3.txt</Key>...</Contents>
</ListBucketResult>

<!-- S3 标准: -->
<ListBucketResult>
  <Name>my-bucket</Name>
  <Delimiter>/</Delimiter>
  <CommonPrefixes><Prefix>dir-a/</Prefix></CommonPrefixes>
  <CommonPrefixes><Prefix>dir-b/</Prefix></CommonPrefixes>
  <Contents><Key>root-file.txt</Key>...</Contents>
</ListBucketResult>
```

### 影响分析

| 场景 | 影响 | 严重性 |
|------|------|--------|
| `aws s3 ls s3://bucket/dir/` | AWS CLI 调用 ListObjectsV2 带 `delimiter=/`，期望 `CommonPrefixes` 而非直接返回对象；无 `CommonPrefixes` 时 CLI 静默解析 `Contents` 中的 key，将所有 `dir-a/`、`dir-b/` 下的对象全部列出——用户看到一整屏扁平结果而非目录结构 | 功能破坏 |
| boto3 `bucket.objects.filter(Delimiter='/')` | Python SDK 会解析 `CommonPrefixes`，无此节点时返回空列表——用户误以为目录下无对象 | 功能破坏 |
| 文件管理器/Web UI 集成分级浏览 | 前端需要按「目录」分页加载，无 `CommonPrefixes` 只能全量加载一页再手动解析 key 模拟分层，性能损失大 | 体验降级 |
| S3 兼容性认证（CASP/SOC2） | 标准 S3 兼容性测试套件（如 `s3-tests`）对 `delimiter` 参数有强制测试；通不过则无法获得兼容性认证 | 商业影响 |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/api/s3compat/handler.go:449-545` | `listObjectsV2`/`listObjectsV1`：读 `prefix`、`marker`/`token`、`maxKeys`，不读 `delimiter` | 缺少 `q.Get("delimiter")` 解析 |
| `internal/api/s3compat/xml.go:10-38` | `listBucketResult` 结构体无 `Delimiter`/`CommonPrefixes` XML 字段 | 缺少响应序列化字段 |
| `internal/repository/sql_objects.go:ListObjects` | SQL: `WHERE key LIKE prefix% ORDER BY key LIMIT` | 缺少按分隔符层分组聚合逻辑（`GROUP BY SUBSTR(key, 1, INSTR(...))` 或应用层） |
| `internal/service/file_features.go:List` | `s.repo.ListObjects(ctx, tenant, bucket, prefix, marker, limit)` | 无 `delimiter` 参数透传 |
| `internal/repository/repository.go:ListObjects` | 接口签名无 `delimiter` | 接口无分组列举能力 |

### 边界情况

| # | 场景 | 预期行为 |
|---|------|---------|
| B1 | 空 delimiter（`?delimiter=`） | S3 规范：视为无 delimiter，回退为扁平列举 |
| B2 | delimiter 为 `/` 但无对象匹配 | 返回空 `CommonPrefixes`，`IsTruncated=false` |
| B3 | delimiter 为多字符（如 `__`） | 正确收集 `CommonPrefixes`，分组逻辑符合同一分隔符 |
| B4 | 同一前缀下有超过 `maxKeys` 个的 `CommonPrefixes` | 截断（`IsTruncated=true`），`NextMarker` 为最后一个 prefix |
| B5 | delimiter 与 prefix 组合：`prefix=a/b/` + `delimiter=/` | prefix 下只返回直接子级 prefix + root 级 contents |
| B6 | delimiter 含正则特殊字符（如 `.`） | 按字面匹配，非正则 |
| B7 | 版本化桶下 delimiter 列举 | 只列举当前版本（非版本列举），行为同非版本桶 |

### 建议实现策略

1. **`repository.Repository.ListObjects` 新增 `delimiter` 参数**（双迁移文件：`sql_objects.go` 查询 + 应用层分组）
2. **应用层分组**：从 SQL 返回扁平 `[]Object` 后，在 `service.List` 中按 `delimiter` 分组成 `CommonPrefixes` + `Contents`
3. **S3 handler**：解析 `delimiter`，调用带 delim 的 List，回填 `CommonPrefixes` XML 节点
4. **REST `/v1/files`**：如需要，也可选择性地支持 `?delimiter` 参数以保持 API 一致性

---

## 方向二：SigV4 `x-amz-content-sha256` 请求正文完整性未验证

### 现状

当前 SigV4 实现读取请求的 `X-Amz-Content-Sha256` 头并将其纳入签名哈希计算，但**仅用于校验签名的数学正确性，而不是验证请求正文是否确实具有该哈希值**。

```go
// internal/auth/sigv4.go:83-118
func (v *SigV4Verifier) Verify(r *http.Request) (*Key, error) {
    payloadHash := r.Header.Get("X-Amz-Content-Sha256")
    if payloadHash == "" {
        payloadHash = unsignedPayload  // "UNSIGNED-PAYLOAD"
    }
    sig := v.sign(r, scope, signedHeaders, payloadHash, amzDate, c.secret)
    // 比较 sig 与请求签名——但 payloadHash 来自客户端声明，非实际计算
}
```

客户端声明 `x-amz-content-sha256: abc123...`，签名计算：`HMAC(secret, "AWS4-HMAC-SHA256\n...\n...${payloadHash}...")`。签名的校验仅保证：
- 客户端知道正确的 secret key
- 客户端声称的 payloadHash 在签名计算中是一致的

**不保证**：实际请求 body 的 SHA256 = 声明的 payloadHash

### 攻击向量

| 攻击类型 | 可行性 | 影响 |
|---------|--------|------|
| **请求正文截断替换** | 中间人可替换 body 并重新声明 SHA256——但需要更新签名（需 secret key），**不可直接篡改** | 实际受影响场景：客户端 SDK 计算 hash 时出错（如大文件 stream 截断），hash 与 body 不一致但服务端无法检测 |
| **`UNSIGNED-PAYLOAD` 滥用** | 客户端可声明 `x-amz-content-sha256: UNSIGNED-PAYLOAD` 跳过完整性检查 | SigV4 规范允许此值，但服务端应可配置拒绝 `UNSIGNED-PAYLOAD` |
| **网络传输静默截断** | Go HTTP 服务器自动读取 body 直到 EOF——若连接在传输中被重置，存储 `Put` 可能收到不完整 body | 已存储的不完整对象无法通过后续 Content-MD5 或 ETag 验证（如果客户端计算了原始长度的 hash），但对象已落盘 |
| **SDK 实现缺陷** | 某些 SDK 实现可能在计算 payload hash 时分块传输导致 hash 与实际发送不一致 | 当前服务端无法验证，静默写入损坏数据 |

更严重的是，`internal/auth/sigv4_chunk.go` 的分块传输签名验证同样跳过内容校验：

```go
// internal/auth/sigv4_chunk.go
// 每块 signature 基于 "AWS4-HMAC-SHA256-PAYLOAD\n...\n${prevSig}\n${chunkHash}\n" 计算
// 其中 chunkHash 来自客户端声明的 chunk 内容哈希——同样不实际计算 chunk 内容
```

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/auth/sigv4.go:83-118` | `Verify` 用 `payloadHash` 计算签名——**读取自请求头** | 无 `sha256.Sum256(body)` 比对 |
| `internal/auth/sigv4_chunk.go:48-75` | 分块签名校验逐块读 `chunkHash`——**来自客户端声明** | 无 `sha256.Sum256(chunkData)` 比对 |
| `internal/api/s3compat/handler.go:PutObject` | S3 PUT 调用 `svc.Put` 前不验证 body hash | 无 payload hash 校验步骤 |
| `internal/api/rest/handler.go:250-270` | REST 路径有 `Content-MD5` 校验（base64 MD5） | S3 路径无等价保护 |
| `internal/service/file_crud.go:140-150` | `md5WrapReader` 仅当 `Content-MD5` 非空时才校验 | 跳过了 S3 SDK 常用的 `x-amz-content-sha256` |

### 建议实现策略

1. **可选校验**（兼容现有客户端）：默认只读 `x-amz-content-sha256` 但不校验 body
2. **严格模式开关**：`S3_SIGV4_VERIFY_PAYLOAD=true` 时，读取并缓存请求 body，计算 SHA256，与声明比对
3. **拒绝 `UNSIGNED-PAYLOAD`**：可选配置 `S3_REQUIRE_CONTENT_SHA256=true` 拒绝未签名 body 的请求
4. **分块传输校验**：`sigv4_chunk.go` 中在读取每个 chunk 后计算实际 hash 并与声明比对

---

## 方向三：ETag 响应格式 HTTP 头部 vs JSON 响应体不一致

### 现状

ETag 在三种输出路径中格式不一致：

| 输出路径 | 示例值 | 格式 |
|---------|--------|------|
| S3 HTTP 头 | `ETag: "abc123"` | RFC 7232 标准：双引号包裹 |
| S3 XML 响应 | `<ETag>"abc123"</ETag>` | XML 中带引号 |
| REST HTTP 头 | `ETag: "abc123"` | RFC 7232 标准 |
| **REST JSON 响应体** | `"etag": "abc123"` | **JSON 字符串值为裸 hash，不带额外引号** |
| **Search JSON 响应** | `"etag": "abc123"` | 同上 |
| **List JSON 响应** | `"etag": "abc123"` | 同上 |

```go
// internal/api/rest/handler.go:145 — HTTP 头（正确）
w.Header().Set("ETag", `"`+obj.ETag+`"`)

// internal/api/rest/handler.go:679-713 — JSON（裸值）
type ObjResponse struct {
    ETag string `json:"etag,omitempty"`  // “abc123” 序列化后为 "etag":"abc123"
}
```

在 HTTP 引用语义中：
- HTTP 头中的值 `"abc123"`（含引号）是强验证器
- JSON 体中的值 `"abc123"`（不含额外引号）表示字符串 `abc123`

问题出现场景：客户端从 JSON 响应中读取 `etag` 值，直接用作 `If-Match` 请求头：

```python
# Python 客户端工作流
resp = requests.get("http://aero-vault/v1/files/myfile")
etag = resp.json()["etag"]  # "abc123"（裸值）

# 后续更新使用 If-Match
headers = {"If-Match": etag}  # If-Match: abc123（无引号）
requests.put("http://aero-vault/v1/files/myfile", ..., headers=headers)
# → 响应 412 Precondition Failed
# 因为服务端比较时 ETag 存储为 "abc123"（带引号），期待 `"abc123"` 而非 `abc123`
```

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/api/rest/handler.go:679-713` | `ObjResponse.ETag string \`json:"etag,omitempty"\`` | JSON 序列化后 `obj.ETag` 裸值被编码为 `"etag":"abc123"` |
| `internal/api/rest/handler.go:898-906` | `searchHit.ETag string \`json:"etag"\`` | 同上 |
| `internal/api/rest/handler.go:145-232` | `w.Header().Set("ETag", `"`+obj.ETag+`"")` | HTTP 头正确加引号 |
| `internal/api/s3compat/handler.go:640-647` | `w.Header().Set("ETag", `"`+etag+`"")` | HTTP 头正确 |
| `internal/api/s3compat/xml.go:39-44` | `ETag: \`"\` + o.ETag + \`"\`` | XML 也加引号 |
| `internal/repository/sql_objects.go` | `ETag` 列存储裸值（无引号） | 存储正确（中间表示应无引号） |

### 建议实现策略

**统一约定**：存储层和 Repository 中 ETag 为裸值；所有输出路径按需格式化。

两种修复方案：

**方案 A（推荐）**：JSON 响应中使 `etag` 值与 HTTP 头格式一致

```go
// 在 JSON 序列化前包装引号
type ObjResponse struct {
    ETag string `json:"etag,omitempty"`
}
// marshal 时：如果 obj.ETag = "abc123"，响应应为 "etag":"\"abc123\""
// 或定义自定义序列化
func (o ObjResponse) MarshalJSON() ([]byte, error) {
    // 如果 o.ETag 非空，输出 "etag":"\"abc123\""
}
```

**方案 B**：HTTP 头去引号，使 ETag 在所有路径中一致为裸值——但**违反 RFC 7232**，不推荐。

**方案 C（最小改动）**：在 `handler.go` 中为 JSON 序列化的 ETag 值外层引用

```go
// handler.go 中 JSON 字段
ETag string `json:"etag"`
// 构造时
ETag: `"` + obj.ETag + `"`,  // 双重引号: 字符串值为 "abc123"
```

---

## 方向四：Bucket 命名规则与对象 Key 合法性校验缺失

### 现状

**Bucket 命名无校验：**

```go
// internal/service/file.go
func (s *FileService) CreateBucket(ctx context.Context, tenant, bucket string) error {
    tenant, bucket = defaults(tenant, bucket)
    return s.repo.CreateBucket(ctx, tenant, bucket)
}
```

S3 桶名必须满足：
- 长度 3-63 字符
- 仅小写字母、数字、连字符（`-`）
- 以字母或数字开头和结尾
- 不得为 IP 地址格式
- 不得以 `xn--` 开头（避免与 punycode 冲突）
- 不得以 `sthree-` 或 `sthree-configurator` 开头（AWS 保留前缀）

当前零校验——允许空的 bucket 名（`defaults` 函数将空值替换为 `"default"`）、允许大写字母、允许 IP 格式。

**对象 Key 校验过弱：**

```go
// internal/service/file.go:130-136
func validateKey(key string) error {
    if key == "" {
        return fmt.Errorf("%w: empty key", ErrInvalidArgs)
    }
    if strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
        return fmt.Errorf("%w: illegal key %q", ErrInvalidArgs, key)
    }
    return nil
}
```

S3 对象 key 限制：
- 最大 1024 字节 UTF-8 编码
- 禁止控制字符（0x00-0x1F, 0x7F）
- 某些存储后端（S3/OSS）禁止特定字符（如反引号、非转义引号）
- key 两端不得有空白字符

当前：
- 无长度上限
- 无字符集过滤
- 无存储后端适配校验
- `storage.ErrInvalidKey` 已定义但**从未被 `validateKey` 引用**

### 影响分析

| 场景 | 影响 |
|------|------|
| 桶名含大写字母 | S3 兼容 SDK 将桶名转为小写进行 URL 构造 → 请求 `GET /BucketName/key` → 服务端查找 `bucketname` 而不是 `BucketName` → 404 |
| 桶名以连字符结尾（如 `my-bucket-`） | DNS 不兼容，某些 S3 SDK 拒绝连接或 SSL 证书验证失败 |
| Key 长于 1024 字节 | AWS SDK 发送请求时截断或失败→服务端可能收到不完整 key |
| Key 含控制字符 | S3 后端返回 `400 InvalidKey`→用户收到难以 debug 的错误 |
| Key 以空格开头 | 存储后端可能保留或删掉前导空格→后续 GET 请求 key 不匹配 |
| 桶名称为 IP（如 `192.168.1.1`） | AWS SDK 混淆为路径样式端点→URL 解析错误 |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/service/file.go:CreateBucket` | 直接 `repo.CreateBucket` | 无 `validateBucketName` 调用 |
| `internal/service/file.go:130-136` | `validateKey` 极小校验 | 无长度/字符集/后端适配校验 |
| `internal/storage/storage.go:15` | `ErrInvalidKey` 错误定义 | 从未在任何路径引用 |
| `internal/storage/s3.go:78-130` | S3 Put/Get 直接传入 key | 后端被动接收 AWS SDK 错误 |
| `internal/repository/sql_buckets.go:CreateBucket` | SQL INSERT 无 CHECK 约束 | DB 层无桶名规则强制 |
| `internal/api/rest/handler.go:handlePut` | 不调 `validateKey` | PUT 路径无 key 校验 |
| `internal/config/config.go` | 无 `S3_STRICT_KEY_VALIDATION` 配置 | 不可用配置跳过校验 |

### 边界情况

| # | 场景 | 预期行为 |
|---|------|---------|
| B1 | 桶名 `null` 或 `undefined` 字符串 | 拒绝创建，返回 `400 InvalidBucketName` |
| B2 | 桶名长度 64 字符 | 拒绝（超过 63） |
| B3 | 对象 key `a//b`（连续斜杠） | 连续斜杠通常允许但应保留——部分后端合并为单斜杠 |
| B4 | key 含 Unicode 字符（如中文、表情） | UTF-8 编码后字节数必须 ≤ 1024 |
| B5 | key 以 `/` 结尾 | S3 允许（表示目录标记），当前 `validateKey` 未禁止，但需确保各后端一致 |

---

## 方向五：Webhook/HTTP 客户端连接池未配置导致连接抖动

### 现状

整个系统中至少有 6 处 HTTP 客户端创建未配置连接池参数，全部使用 Go 默认的零配置连接池（`http.DefaultClient` 的 `Transport` 为 `http.DefaultTransport`）：

```go
// internal/events/webhook.go:40
return &Webhook{
    urls:   cleaned,
    client: &http.Client{Timeout: 5 * time.Second}, // ← 无 Transport 配置
}

// internal/ai/embedder.go:buildHTTPClient
func buildHTTPClient() *http.Client {
    return &http.Client{} // ← 零配置
}

// internal/ai/llm.go:buildHTTPClient  
func buildHTTPClient() *http.Client {
    return &http.Client{} // ← 零配置
}

// internal/antivirus/antivirus.go:NewHTTPScanner
func NewHTTPScanner(endpoint, apiKey string) *HTTPScanner {
    return &HTTPScanner{
        client: &http.Client{}, // ← 零配置
    }
}

// internal/storage/kms.go:NewKMSClient
func NewKMSClient(baseURL, token string) *KMSClient {
    return &KMSClient{
        client: &http.Client{}, // ← 零配置
    }
}
```

Go 的 `http.DefaultTransport` 初始配置：
```go
var DefaultTransport = &http.Transport{
    MaxIdleConns:    100,
    MaxIdleConnsPerHost: 2,  // ← 每个 host 最多 2 个空闲连接！
    IdleConnTimeout: 90 * time.Second,
}
```

`MaxIdleConnsPerHost: 2` 是关键的瓶颈。在高并发场景：
- Webhook 需要向同一 URL 递送多个并行事件时，一次只有 2 个连接可用
- AI Embedder 连续批量 embedding 请求时，连接在 2 个之间轮流
- Antivirus 扫描并行处理时，向扫描服务的连接受限

此外，大部分客户端未设置超时：
- 建立连接超时（`DialContext` 默认无超时）
- TLS 握手超时（默认 0）
- 读取响应超时（`ResponseHeaderTimeout` 默认 0）

### 影响分析

| 组件 | 并发峰值 | 连接瓶颈 | 影响 |
|------|---------|---------|------|
| Webhook 递送 | 可能同时递送多个事件 | `MaxIdleConnsPerHost=2` → 第 3 个请求需建新 TCP 连接 | 延迟增加 1-3 RTT（TCP + TLS） |
| AI Embedder | 批量 embedding 多块 | 同上 | embedding 总延迟因排队等待连接而增加 |
| AI LLM Chat | 多租户并发 chat 请求 | 同上 | Chat 尾部延迟（tail latency）上升 |
| AI Reranker | 检索后对 top-k 结果重排名 | 同上 | 搜索尾部延迟增加 |
| Antivirus 扫描 | 并行扫描多个对象 | 同上 | 索引吞吐量受限 |
| KMS 客户端 | SSE key 重包装/获取 | 同上 | 加解密延迟增加 |

在 100 RPS 的 webhook 负载下，`MaxIdleConnsPerHost=2` 导致客户端在 98% 的请求中需要创建新连接（因为空闲连接被之前刚刚完成但尚未回收的连接占用），导致：
1. 大量的 `TIME_WAIT` 状态积累（每个新连接释放后进入 `TIME_WAIT` 60s）
2. 短暂耗尽可用临时端口（Linux 默认 `net.ipv4.ip_local_port_range` 约 28K 端口）
3. TCP 抖动→webhook 递送成功率下降

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/events/webhook.go:40` | `&http.Client{Timeout: 5 * time.Second}` | 无共享 Transport、无 MaxIdleConns 配置 |
| `internal/ai/embedder.go:buildHTTPClient` | `&http.Client{}` | 同上 |
| `internal/ai/llm.go:buildHTTPClient` | `&http.Client{}` | 同上 |
| `internal/ai/rerank.go:buildHTTPClient` | 未导出 | 同上 |
| `internal/antivirus/antivirus.go:NewHTTPScanner` | `client: &http.Client{}` | 同上 |
| `internal/storage/kms.go:NewKMSClient` | `client: &http.Client{}` | 同上 |
| `internal/jobs/jobs.go:jobQueue` | 无 HTTP 调用 | N/A——但作业处理路径调用的 AI/AV 组件受影响 |
| `internal/storage/storage.go:NewHTTPClient` | 仅 S3/OSS/COS backend 配置连接池 | 其他组件未共享此设施 |
| `internal/config/config.go` | 无 `HTTP_CLIENT_*` 配置项 | 不可外部调整连接池参数 |

### 边界情况

| # | 场景 | 预期行为 |
|---|------|---------|
| B1 | Webhook URL 指向同一 host 但不同 path | 共享同一连接池（至 host） |
| B2 | 长时间空闲后突然并发请求 | 空闲连接已关闭，须建新连接——IdleConnTimeout 合理可防止使用过期连接 |
| B3 | TLS 客户端证书认证 | 连接池应按 `Transport.TLSClientConfig` 复用，而非每请求创建 |
| B4 | HTTP/2 支持 | 连接池与 HTTP/2 多路复用配合更好——当前 `http.Transport` 默认启用 HTTP/2 |
| B5 | 代理环境（HTTP_PROXY） | 需确保 `Transport.Proxy` 配置正确 |

### 建议实现策略

1. **创建全局共享 `http.Transport`**：一个 `sync.Once` 初始化的连接池，各组件共享同一 Transport
2. **配置暴露**：`HTTP_CLIENT_MAX_IDLE_CONNS=200`、`HTTP_CLIENT_MAX_IDLE_PER_HOST=10`、`HTTP_CLIENT_IDLE_TIMEOUT=90s`、`HTTP_CLIENT_CONNECT_TIMEOUT=5s`
3. **各组件超时独立**：复用 Transport 但使用不同的 `http.Client{Timeout: ...}`（超时按组件不同）
4. **连接池预热**：启动时可对关键 endpoint 做连接预创建（减少首次请求延迟）

---

## 总体收益总结

| # | 方向 | 实现预估工作量 | 预期收益 | 风险 |
|---|------|--------------|---------|------|
| 1 | S3 ListObjects delimiter/CommonPrefixes | S（service + handler + XML，约 3 个文件，200 行） | 🔴 解锁 S3 SDK 目录列举功能；通 S3 兼容性认证 | 低 |
| 2 | SigV4 payload integrity | M（sigv4.go + config + handler，约 4 个文件，150 行） | 🟠 可选关闭的严格模式；防静默数据损坏 | 中（需兼容已有客户端） |
| 3 | ETag JSON/HTTP 格式一致 | XS（handler.go JSON 序列化，约 2 个位置，10 行） | 🟢 消除条件请求间歇失败；API 契约清晰 | 极低 |
| 4 | Bucket/Key validation | S（service/validation.go + config，约 2 个文件，80 行） | 🟠 防静默桶名冲突；storage backend 适配保护 | 低 |
| 5 | HTTP client 连接池 | M（config + http/transport.go + 各组件注入，约 5 个文件，120 行） | 🟠 高并发下延迟降低 30-80%；防端口耗尽 | 低 |

**建议实施顺序：** 方向 3 → 方向 4 → 方向 1 → 方向 5 → 方向 2

方向 3（ETag 格式）是最小侵入、零风险的快速获胜；方向 4（Bucket/Key 校验）是防数据静默错误的基础防护；方向 1（S3 delimiter）是 S3 兼容性的关键功能缺口；方向 5（HTTP 连接池）是生产规模下性能稳定性的基础设施；方向 2（SigV4 payload 校验）是安全纵深防御的最后一道可选防线。
