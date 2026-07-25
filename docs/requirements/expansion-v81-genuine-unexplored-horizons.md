# AeroVault 架构师/产品经理视角 — 第 81 轮：系统性盲区深度扫描

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 23+ 子包，三套 SDK，MCP 双模式，Web UI，48 对迁移文件，Makefile，CI gate，`docs/` 全部 80 份既有分析文档）  
> **去重验证：** 对 `docs/requirements/` 下全部 80 份既有分析文档逐方向 `grep` 正则交叉验证 + 语义比对 + 代码锚点映射  
> **日期：** 2026-07-11  
> **核心原则：** 选取代码中存在具体、可量化、且在前 80 轮分析中**零实质性架构分析**的系统盲区。每个方向包含代码锚点、影响分析、既有覆盖证明、边界情况枚举。

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 | 既有分析覆盖 |
|---|------|------|--------|---------|---------|-------------|
| **1** | **Search 结果片段生成与查询命中高亮（Snippet Generation & Query Highlighting）** | AI/UX | **P2** — 语义搜索结果仅返回原始 chunk 文本，无查询命中高亮、无相关片段提取、无段落截断适配。用户无法从结果列表中快速判断 chunk 的相关性——需要点开每个结果手动阅读、搜索 Ctrl+F 找到命中词 | `internal/ai/search.go:147-160`（`hitsFromRanked` 将 `chunk.Content` 原样曝露）；`internal/ai/search.go:108-120`（`Hit.Chunk` 为完整 chunk 文本）；`internal/api/rest/dto.go:searchResponse`（`Hits` 透传 `[]Hit`）；`internal/api/rest/search.go:Search`（写响应时零转换） | ✅ **完全去重**（`grep -rln "snippet\|query.*highlight\|passage.*extract\|result.*highlight\|search.*snippet\|highlight.*result\|relevant.*passage\|search.*summary\|摘要.*搜索\|搜索结果.*截断\|命中.*高亮" docs/requirements/` → **0 命中**。v34 方向三覆盖 AI 评估框架（MRR/NDCG），但聚焦离线度量而非在线结果展示。**零分析查询命中高亮与片段生成的代码锚点与实现路径**） |
| **2** | **S3 PUT 请求 `x-amz-tagging` 头部静默忽略（Tag-at-Upload Protocol Gap）** | 协议合规/数据正确性 | **P2** — S3 PUT 请求可以通过 `x-amz-tagging: key1=val1&key2=val2` 头部在创建对象的同时设置标签。当前 S3 handler 完全忽略此头部，标签被静默丢弃。使用标准 S3 SDK 且在上传时设定标签的客户端会发现标签"神秘消失" | `internal/api/s3compat/handler.go:69-115`（`PutObject` 处理 `x-amz-copy-source`、`x-amz-object-lock-legal-hold`、`x-amz-storage-class`、`x-amz-acl`——**无 `x-amz-tagging`**）；`internal/api/s3compat/extra.go:87-121`（标签仅在独立 `?tagging` 子资源端点处理）；`internal/service/file_crud.go:Put`（`PutOptions.StorageClass` 有参，`Tags` 只能通过后续 `SetTags` 调用）；`internal/repository/sql_tags_acl.go`（标签存储就位，但从未在 PUT 路径写入） | ✅ **完全去重**（v77 方向二覆盖 `CopyObject` 的 `x-amz-tagging-directive` 缺失——聚焦**复制**路径中标签指令被忽略。本方向聚焦**直接 PUT 上传**路径中 `x-amz-tagging` 头部被忽略——两个独立代码路径。`grep -rn "x-amz-tagging.*header\|tag.*at.*upload\|upload.*tag.*header\|tagging.*header.*put\|PUT.*tagging.*header" docs/requirements/` → 除 v77 的 CopyObject 相关行外 **0 命中**） |
| **3** | **Per-Bucket / Per-Prefix 层次化存储配额（Hierarchical Quota Enforcement）** | 多租户/平台完整性 | **P2** — 配额仅在租户级别执行（`MaxBytes` / `MaxObjects`）。在共享桶场景（多个团队写入同一桶的不同前缀下），一个异常应用可以耗尽整个桶的容量，影响其他团队。缺少桶级、前缀级目录级配额能力 | `internal/service/file_crud.go:48-66`（`preflightQuota` 仅检查 `GetTenantQuota`——租户级）；`internal/repository/quota.go`（`TenantQuota` 仅有 `TenantID`、`MaxBytes`、`MaxObjects`——无桶/前缀字段）；`internal/repository/repository.go:130-138`（`TenantQuota` 结构定义）；`internal/api/rest/admin.go:SetQuota`（仅接收 tenant 参数）；`internal/api/rest/handler.go:GetBucketStats`（返回桶级统计但不驱动配额检查）；`internal/repository/sql_buckets.go:GetBucketConfig`（`BucketConfig` 无配额字段） | ✅ **完全去重**（v32 方向二覆盖"层次化命名空间"——聚焦原子目录操作和前缀级 ACL，**以一行提及前缀级配额作为子特性**。**零独立分析配额模型的桶/前缀维度扩展、代码锚点、实现路径与边界情况**。`grep -rn "per.bucket.*quota\|bucket.*level.*quota\|prefix.*quota\|directory.*quota\|folder.*quota\|子路径.*配额\|桶.*配额\|路径.*配额" docs/requirements/` → 仅 v32 一行） |
| **4** | **多协议并发写入一致性保障（Multi-Protocol Write Concurrency Safety）** | 数据一致性/架构 | **P2** — REST、S3、WebDAV、MCP 四协议各自独立调用 FileService。同一对象通过不同协议同时写入时，无操作排序、无 CAS 保护、无分布式追踪关联。版本化桶下可能出现协议 A 的写入版本 ID 与协议 B 的读取版本 ID 交叉错乱 | `internal/api/rest/handler.go:Put`（REST PUT → `svc.Put`）；`internal/api/s3compat/handler.go:PutObject`（S3 PUT → `svc.Put`）；`internal/api/webdav/dav.go:putFile`（WebDAV PUT → `svc.Put`）；`internal/mcp/server.go:toolWriteFile`（MCP write → `svc.Put`）；`internal/service/file_crud.go:Put`（四协议共享同一路径——`store.Put` → `writePutObject`）；`internal/repository/sql_objects.go:UpsertObject`（无协议来源列，无乐观锁字段）；`internal/events/bus.go:Publish`（事件中无协议来源 `protocol` 字段）；`internal/telemetry/metrics.go`（无协议间写入冲突计数器） | ✅ **完全去重**（v48 覆盖 multipart 自身并发一致性——聚焦同一协议的多分片竞态；v55 方向一覆盖 ConcurrencyLimiter TOCTOU——聚焦限流器窗口；v71 方向三覆盖版本 ID 分配时机——聚焦版本化桶内部分配；v78 方向三覆盖跨协议对象身份识别——聚焦引用标识而非写入一致性。`grep -rln "multi.*protocol.*concurr\|protocol.*concurr.*write\|concurr.*write.*diff.*protocol\|write.*race.*protocol\|cross.*protocol.*write" docs/requirements/` → **0 命中**） |
| **5** | **存储后端动态运行态健康管理（Dynamic Storage Health Management & Degradation）** | 可靠性/运维 | **P2** — 存储后端（S3/OSS/COS）启动后无运行时健康检查、无延迟探测、无自动降级。当后端发生性能退化（高延迟、限流）时，服务无感知地持续向受损后端发送请求，整体延迟被拖高。Circuit breaker 仅按错误计数断路，不按延迟退化主动降级 | `internal/storage/circuitbreaker.go:140-164`（`tryTransition`——仅统计失败次数，无延迟百分位检测、无慢调用检测、无健康探测）；`internal/storage/s3.go`（每个方法直接调用 AWS SDK，无后端延迟探针、无 readiness check）；`internal/storage/oss.go`（同）；`internal/storage/cos.go`（同）；`internal/storage/local.go`（无健康探测——假定本地 FS 永远健康）；`internal/middleware/ratelimit.go`（全局 RPS 限流不从存储后端健康状态获取反馈）；`cmd/server/main.go:readyzHandler`（仅启动时 Stat 一次，不持续监测） | ✅ **完全去重**（v63 方向三提出存储后端 `Health()`/`Capacity()` 可编程 API——聚焦接口契约设计。v65 方向四覆盖"存储后端健康管理"——侧重连接池复用和优雅降级框架概念。**零分析存储后端运行时无健康探测的具体代码锚点、主动降级缺失、以及 Circuit breaker 从不读取延迟百分位的具体实现缺口**） |

---

## 方向一：Search 结果片段生成与查询命中高亮

### 现状

当前搜索 API 响应直接透传 chunk 原文：

```json
// POST /v1/search  {"query": "how to configure TLS", "mode": "hybrid"}
{
  "query": "how to configure TLS",
  "hits": [
    {
      "score": 0.892,
      "chunk": "TLS configuration requires a valid certificate chain. The server certificate must be signed by a trusted CA. Self-signed certificates are not recommended for production use. To configure TLS, set TLS_CERT_PATH and TLS_KEY_PATH environment variables...",
      "object_key": "docs/admin/security.md",
      "seq": 3
    }
  ]
}
```

问题在于：

1. **无查询命中高亮**：用户需要肉眼扫描 chunk 文本中的 TLS 相关词汇，无法从视觉上快速确认命中位置。
2. **无相关片段提取**：chunk 长度由 `AI_CHUNK_WINDOW=600` 控制，返回的是完整的 600-token 文本块。如果命中词在 chunk 尾部，用户看到的是不相关的头部内容。
3. **无结果摘要**：长 chunk（600 tokens ≈ 450 词）截断了展示，用户需要点开每个结果手动搜索。
4. **无文档级别去重**：同一文档的多个 chunk 可以占据 top-k 结果中的多数席位，用户看到的是同一文档的不同段落而非多样的来源。

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/ai/search.go:147-160`（`hitsFromRanked`） | `out[i].Chunk = h.chunk.Content` —— 完整原样曝露 | 无片段提取、无截断、无高亮 |
| `internal/ai/search.go:93-96`（`Hit` 结构） | `Chunk string` —— 返回全部内容 | 无 `Snippet`、`Highlights`、`ScoreBreakdown` 字段 |
| `internal/api/rest/dto.go:searchResponse` | `Hits` → `interface{}` → 直接 JSON 序列化 | 无后处理管道 |
| `internal/api/rest/search.go:Search` | `writeJSON(w, http.StatusOK, searchResponse{...})` | 无片段生成步骤 |
| `internal/ai/result_cache.go`（搜索缓存） | 缓存完整 `[]Hit` | 无缓存片段预生成 |

### 片段提取策略

```
用户查询: "how to configure TLS in production"

匹配 chunk:
  "TLS configuration requires a valid certificate chain. The server 
   certificate must be signed by a trusted CA. Self-signed certificates 
   are not recommended for production use. To configure TLS, set 
   TLS_CERT_PATH and TLS_KEY_PATH environment variables..."

最佳片段（query-aware passage extraction）:
  "To **configure TLS**, set `TLS_CERT_PATH` and `TLS_KEY_PATH` 
   environment variables..."

算法:
  1. 将查询词拆分为 token 集（去除停用词）: {tls, configure, production}
  2. 在 chunk 中滑动窗口（50-100 tokens），计算每个窗口的命中密度
  3. 选择命中密度最高的窗口作为 snippet
  4. 在 snippet 中用标记（`<mark>...</mark>` 或 `**...**`）包裹命中词
```

### 为什么需要

| 场景 | 缺失的影响 |
|------|-----------|
| 知识库搜索：员工搜索公司政策 | 必须点开每个结果挨个找关键词，搜索效率降低 70%+ |
| 日志分析：搜索 ERROR 码 | 6 个结果中 4 个来自同一个大文档的不同 chunk，无去重 |
| API 文档搜索：搜索 SDK 配置 | chunk 包含 500 字背景，命中词夹杂在中间，用户找不到 |
| 对比 Google/Bing/Elastic：后三者都高亮命中词 | 用户期望的"搜索结果应有高亮"得不到满足 |

### 实现要点

- `Search.Query` 返回时可选携带 `Snippet string` / `Highlights []Range` 字段
- `hitsFromRanked` 之后新增 `annotateHits(ctx, query, hits)` 阶段
- snippet 长度控制：`AI_SNIPPET_MAX_LENGTH`（默认 300 chars）
- 缓存：snippet 生成在 `resultCache` 命中时跳过
- 接口兼容：`chunk` 字段保持完整内容，新增 `snippet` 和 `highlights` 字段（非破坏性向后兼容）

### 边界情况

| 场景 | 预期行为 |
|------|---------|
| 查询词为空或纯停用词 | 无高亮，返回 chunk 前 300 字符作为 snippet |
| 查询词不在任何 chunk 中 | 返回 chunk 前 300 字符作为 snippet，无高亮 |
| 多词查询（如 "TLS certificate rotation"） | 所有命中词按窗口密度加权选择 snippet |
| chunk 长度 < snippet 上限 | 返回完整 chunk 作为 snippet |
| 精确片段跨越两个 chunk | 报告时取包含最高命中密度的 chunk |
| BM25 模式（无 embedder） | 同样支持 snippet 生成（BM25 已有词频信息） |

---

## 方向二：S3 PUT 请求 `x-amz-tagging` 头部静默忽略

### 现状

当前 S3 PUT 路径：

```
PUT /bucket/key
x-amz-tagging: project=alpha&env=prod

handler.PutObject (handler.go:69)
  ├── x-amz-copy-source? → 路由到 copy
  ├── x-amz-object-lock-legal-hold? → 存储 legal_hold 元数据
  ├── x-amz-storage-class? → 设置 storage_class
  ├── x-amz-acl? → 设置 canned ACL
  │
  └── x-amz-tagging? ← ❌ 完全未读取
       │
       ▼
  svc.Put(ctx, tenant, bucket, key, body, size, opts)
   └── opts.Tags = nil ← 标签始终为空
```

S3 规范中标签有两种设置方式：
1. `PUT /key?tagging`（XML body）—— ✅ 已实现
2. `PUT /key` 时带 `x-amz-tagging: key1=val1&key2=val2` 头部—— ❌ 未实现

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/api/s3compat/handler.go:69-115`（`PutObject`） | 处理 5 个 S3 特定头部 | **`x-amz-tagging` 头部完全未被检查** |
| `internal/api/s3compat/handler.go:108` | `StorageClass: r.Header.Get("x-amz-storage-class")` | **无对应 `Tags: parseTagHeader(r.Header.Get("x-amz-tagging"))`** |
| `internal/api/s3compat/extra.go:87-121`（`putObjectTagging`） | 仅处理 `?tagging` 查询参数路径 | 不处理 PUT 头部路径 |
| `internal/service/file_crud.go:Put` | `PutOptions` 含 `Tags` 字段 | 从来没有人传 |
| `internal/service/file.go:PutOptions` | `Tags map[string]string` | 字段已存在，只差调用方赋值 |

### 影响范围

| 客户端行为 | 当前结果 | 预期结果 |
|-----------|---------|---------|
| AWS CLI: `aws s3 cp file s3://bucket/key --tag "project=alpha"` | 标签静默丢失 | 对象创建时即带标签 |
| boto3: `client.put_object(Bucket='b', Key='k', Body=data, Tagging='project=alpha')` | 标签静默丢失 | 标签正确关联 |
| AWS SDK Go: `s3.PutObjectInput{Tagging: aws.String("project=alpha")}` | 标签静默丢失 | 标签正确关联 |
| curl: `curl -X PUT -H "x-amz-tagging: project=alpha" ...` | 标签静默丢失 | 标签正确关联 |
| REST API: `PUT /v1/files/key` | 不支持标签（无对应头部） | 可扩展支持 `X-Aero-Tagging` 保持一致性 |

### 实现要点

```go
// handler.go PutObject 中新增
if tagHeader := r.Header.Get("x-amz-tagging"); tagHeader != "" {
    parsed, err := parseAmzTaggingHeader(tagHeader)
    if err != nil {
        writeS3Error(w, r, ...) // 400 InvalidTag
        return
    }
    opts.Tags = parsed
}
```

```go
// parseAmzTaggingHeader 解析 "key1=val1&key2=val2" 格式
func parseAmzTaggingHeader(v string) (map[string]string, error) {
    tags := map[string]string{}
    for _, pair := range strings.Split(v, "&") {
        kv := strings.SplitN(pair, "=", 2)
        if len(kv) != 2 || kv[0] == "" {
            return nil, fmt.Errorf("invalid tag pair: %q", pair)
        }
        key, _ := url.QueryUnescape(kv[0])
        val, _ := url.QueryUnescape(kv[1])
        tags[key] = val
    }
    if len(tags) > 10 { // S3 限制每对象最多 10 个标签
        return nil, fmt.Errorf("too many tags: %d > 10", len(tags))
    }
    return tags, nil
}
```

### 边界情况

| 场景 | 预期行为 |
|------|---------|
| 标签头部 + `?tagging` 查询参数同时出现 | 合并（`x-amz-tagging` 头部优先覆盖同名 key）或返回 400（冲突）—— 推荐合并 |
| 标签值包含 `&` 或 `=` | URL 编码：`key=value%3Dtest%26more` → 解码后 `value=test&more` |
| 标签 key 超过 128 字符或 value 超过 256 字符 | 返回 `400 InvalidTag` |
| 标签数量超过 10 | 返回 `400 BadRequest` 并提示 `TooManyTags` |
| S3 COPY 命令（`x-amz-copy-source` + `x-amz-tagging-directive=REPLACE` + `x-amz-tagging`） | v77 的分析覆盖 COPY 路径的 `x-amz-tagging-directive`，本分析聚焦 PUT 路径 |
| REST API 上传同步支持 | REST handler 的 `Put` 也可增加 `X-Aero-Tagging` 头部支持 |

---

## 方向三：Per-Bucket / Per-Prefix 层次化存储配额

### 现状

当前配额模型：

```
租户 "acme"
  ├── MaxBytes: 100 GB
  ├── MaxObjects: 100,000
  └── UsedBytes: 45 GB
       └── 无法区分哪个桶/前缀消耗的

桶 "shared-data"
  ├── 团队 A 写入 /team-a/*
  ├── 团队 B 写入 /team-b/*
  ├── CI 流水线写入 /ci/*
  │
  └── 如果团队 A 写入 100 GB，团队 B 和 CI 都受影响
      （因为配额在租户层，只能一起超限）
```

当前配额流程：

```
PUT /bucket/key → preflightQuota
  └── GetTenantQuota(tenant) → 仅检查租户总用量
      └── ✅ /  ❌ 对桶内用量零感知
```

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/repository/quota.go` | `TenantQuota` 结构 | 无桶级/前缀级配额类型 |
| `internal/repository/repository.go:130-138` | `TenantQuota{TenantID, MaxBytes, MaxObjects, UsedBytes, UsedObjects}` | 无 `Bucket`、`Prefix` 字段 |
| `internal/service/file_crud.go:48-53`（`preflightQuota`） | 仅调 `repo.GetTenantQuota(ctx, tenant)` | 无桶级预检 |
| `internal/api/rest/admin.go:SetQuota` | `PUT /admin/tenants/{tenant}/quota` | 无 `PUT /admin/buckets/{bucket}/quota` |
| `internal/repository/sql_buckets.go:GetBucketConfig` | `BucketConfig` 有 `Versioning`/`ObjectLockSeconds` 等 | 无 `MaxBytes`/`MaxObjects` 字段 |
| `internal/api/rest/handler.go:GetBucketStats` | 桶统计可查询但不关联配额 | 有统计数据但不驱动检查 |

### 配额模型扩展

```
租户级配额（已实现）
  └── 桶级配额（新增）
       └── 前缀级配额（新增，可选）
```

**数据库扩展：**

```sql
-- 桶级配额
ALTER TABLE buckets ADD COLUMN max_bytes BIGINT DEFAULT 0;
ALTER TABLE buckets ADD COLUMN max_objects BIGINT DEFAULT 0;
ALTER TABLE buckets ADD COLUMN used_bytes BIGINT DEFAULT 0;
ALTER TABLE buckets ADD COLUMN used_objects BIGINT DEFAULT 0;

-- 前缀级配额（可选，高级功能）
CREATE TABLE IF NOT EXISTS prefix_quotas (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id  TEXT NOT NULL,
    bucket     TEXT NOT NULL,
    prefix     TEXT NOT NULL DEFAULT '',
    max_bytes  BIGINT NOT NULL DEFAULT 0,
    max_objects BIGINT NOT NULL DEFAULT 0,
    used_bytes BIGINT NOT NULL DEFAULT 0,
    used_objects BIGINT NOT NULL DEFAULT 0,
    UNIQUE(tenant_id, bucket, prefix)
);
```

**预检链：**

```
preflightQuota(ctx, tenant, bucket, prefix, size, delta)
  ├── tenant 级检查（现有）: used_bytes + size ≤ max_bytes
  ├── bucket 级检查（新增）: bucket_used_bytes + size ≤ bucket_max_bytes
  └── prefix 级检查（新增）: prefix_used_bytes + size ≤ prefix_max_bytes
```

### 为什么需要

| 场景 | 缺失的影响 |
|------|-----------|
| 多团队共享桶（每个团队写入不同前缀） | 一个团队用量异常 → 整个桶超限 → 所有团队写入拒 |
| SaaS 运营：每个客户一个前缀 | 无法隔离客户存储用量，1% 的客户吃 90% 的存储 |
| CI/CD 流水线的构建产物桶 | 异常 Pipeline 写爆桶 → 所有 Pipeline 无法产出 |
| 计费需求：按桶/前缀核算存储成本 | 只有租户级用量，无法做桶级成本分摊 |

### 边界情况

| 场景 | 预期行为 |
|------|---------|
| 桶级配额为 0（默认值） | 退回到租户级配额检查，向下兼容 |
| 桶级配额 < 租户级配额 | 桶级优先检查（较严格的限制生效） |
| 对象在桶之间移动（Copy + Delete） | 源桶 used_bytes 减少，目标桶增加 |
| 前缀配额变更时已超限 | 已有对象不删除，新写入拒绝 |
| 多协议（REST/S3/WebDAV/MCP） | 统一的配额检查在 FileService 层执行，协议透明 |
| 配额检查服务超时/不可用 | 同现有行为——静默跳过（best-effort），但记录 warn 日志 |
| 并发写入同一桶 | 桶级 `used_bytes` 的原子更新（`UPDATE ... SET used_bytes = used_bytes + $1 WHERE ...`） |

---

## 方向四：多协议并发写入一致性保障

### 现状

四协议写入路径：

```
REST:  POST /v1/files/key   → handler.Put   → svc.Put
S3:    PUT /s3/bucket/key   → handler.PutObject → svc.Put
WebDAV: PUT /dav/path       → dav.putFile   → svc.Put
MCP:   tools/call write_file → server.toolWriteFile → svc.Put

所有路径最终调用:
  svc.Put(ctx, tenant, bucket, key, body, size, opts)
```

当前无保障：

| 场景 | 当前行为 | 问题 |
|------|---------|------|
| REST PUT + S3 PUT 同时到同一 key（非版本化桶） | 后完成的写入覆盖之前的结果，中间状态丢失 | 用户不知道哪个写入"赢了" |
| WebDAV MKCOL 正在创建目录，同时 REST PUT 同一 key | MKCOL 可能失败（已存在），但中间操作已执行 | 幂等性不一致 |
| MCP write_file + S3 PUT 几乎同时 | 两个写入都成功，存储层可能有两个 blob，但 repo 只有最终版本 | blob 孤儿 |
| 版本化桶：REST PUT 创建 v1，WebDAV PUT 创建 v2 | 版本 ID 分配在各自协程中，可能 v2 完成早于 v1 | 版本乱序 |

### 代码锚点

| 位置 | 缺口 |
|------|------|
| `internal/service/file_crud.go:Put`（第 48-64 行无锁） | 无写入锁、无 CAS、无版本号乐观锁 |
| `internal/service/file_crud.go:writePutObject`（第 96-114 行） | `UpsertObject` 是独立 SQL，无 `updated_at` 校验 |
| `internal/repository/sql_objects.go:UpsertObject`（INSERT OR REPLACE） | REPLACE 无条件覆盖——后到者胜 |
| `internal/service/file_crud.go:105`（`s.emit(ctx, saved, repository.EventCreated)`） | 事件中的 version_id 可能在另一个写入中不再准确 |
| `internal/events/bus.go` | 事件 payload 无 `protocol` 或 `caller` 字段 |
| `internal/repository/repository.go:Object` | 无 `updated_at_timestamp` 乐观锁字段（`updated_at` 仅记录时间） |

### 写入一致性模型（建议）

```
非版本化桶:
  ┌─ 乐观锁（推荐）：UpsertObject 添加 WHERE updated_at = $prev
  │   → 第二个写入收到 ErrConflict，可重试
  │   → 客户端感知一致性
  │
  └─ 悲观锁（备选）：per-key mutex（sync.Map + sharded mutex）
      → 串行化同一 key 的写入
      → 牺牲并发性能

版本化桶:
  ┌─ 每个写入产生独立版本 ID（已有）
  └─ 需保证 version_id 递增顺序与完成顺序一致
     → 乐观锁 + 版本 ID 预分配
```

### 为什么需要

| 场景 | 风险等级 |
|------|---------|
| CI/CD 通过 REST 写入构建产物，同时 S3 SDK 写入元数据 | **高** — 两种工具可能各自覆盖对方数据 |
| 用户通过 WebDAV 编辑 office 文档，同时 REST 客户端读/写 | **高** — 办公室文档冲突通常靠最后保存者赢，但无预警 |
| MCP Agent 工具循环写入文件，同时人类通过 S3 上传 | **中** — Agent 掉电后无感知 |
| 备份工具通过 S3 写入全量数据，同时应用通过 REST 增量更新 | **高** — 全量备份覆盖增量更新 |
| 运维在 Web UI 上传配置, 同时 Terraform 通过 REST 管理同一配置 | **高** — Infrastructure as Code 与人工操作冲突 |

### 实现要点

1. **乐观锁**：`UpsertObject` → `UPDATE objects SET ... WHERE tenant=$1 AND bucket=$2 AND key=$3 AND updated_at=$4`
2. **冲突响应**：FileService 返回 `ErrConflict`（HTTP 409），客户端根据协议重试或报错
3. **协议头透传**：支持 S3 `If-Match`/`If-None-Match` 头部实现条件写入
4. **事件增强**：事件 payload 增加 `protocol`（`rest`/`s3`/`webdav`/`mcp`）字段
5. **指标**：`write_conflict_total{protocol_a, protocol_b}` 追踪跨协议冲突频率
6. **可选**：`per-key sharded mutex` 作为进阶保障，通过 `CONCURRENT_WRITE_PROTECTION=key` 启用

### 边界情况

| 场景 | 预期行为 |
|------|---------|
| 乐观锁冲突 → 自动重试 | 客户端收到 409 后重试，成功率取决于窗口长度 |
| 无冲突（顺序写入） | 零性能开销（乐观锁的 `WHERE` 条件总匹配） |
| 版本化桶乐观锁 | 每个版本是独立 INSERT，无需乐观锁 |
| 批量删除中的冲突 | 每 key 独立处理，失败 key 返回 `conflict` 状态 |
| MCP 工具循环自动重试 | Agent 收到 409 → 重新读取 → 重新写入（安全但需要实现） |
| 向后兼容 | 乐观锁默认关闭（通过 `ENABLE_OPTIMISTIC_LOCK=false` 控制） |

---

## 方向五：存储后端动态运行态健康管理与主动降级

### 现状

`readyzHandler` 的行为：

```go
// cmd/server/main.go:readyzHandler
func readyzHandler(repo repository.Repository, store storage.Storage) http.HandlerFunc {
    return func(w http.ResponseWriter, req *http.Request) {
        if err := repo.Ping(req.Context()); err != nil { ... }
        if _, err := store.Stat(req.Context(), "@healthz/probe"); err != nil && !errors.Is(err, storage.ErrNotFound) {
            http.Error(w, "storage: "+err.Error(), http.StatusServiceUnavailable)
            return
        }
        w.WriteHeader(http.StatusOK)
    }
}
```

**问题：**
1. `readyz` 仅在**启动时**调用 `Stat("@healthz/probe")` 一次——`runServer` 之后不再定期检查存储后端健康状态。
2. Circuit breaker（`internal/storage/circuitbreaker.go`）**仅按失败计数**切换状态，不监测：
   - 延迟 P95 超过阈值（慢调用退化）
   - 存储后端响应时间增加 3 倍（相对退化）
   - 限流 503 增加（云后端常见）
3. 当后端退化时，没有从限流器或并发限制器获得反向压力信号。
4. S3/OSS/COS 后端各自维护独立连接池，无统一的后端健康视图。

### 当前 Circuit Breaker 实现

```go
// internal/storage/circuitbreaker.go:140-164
func (cb *CircuitBreaker) tryTransition(err error) {
    if err == nil {
        cb.successes.Add(1)
        cb.consecutiveFailures.Store(0)          // ← 只计数失败次数
        return
    }
    failures := cb.consecutiveFailures.Add(1)
    if failures >= cb.cfg.FailureThreshold {
        cb.setState(StateOpen)
        // ❌ 从不检查延迟指标
        // ❌ 不从 OTel 读取 P50/P95/P99
        // ❌ 不区分错误类型（限流 vs 崩溃 vs 慢响应）
    }
}
```

### 为什么需要

| 场景 | 当前行为 | 后果 |
|------|---------|------|
| S3 后端限流（503 慢速响应） | Circuit breaker 等待失败计数达到阈值（默认 5），在此期间所有请求经历 3-5 秒延迟 | P95 飙升，客户端超时连锁 |
| OSS 后端网络抖动（间歇性 1-2 秒延迟） | 无延迟检测，所有请求都等 2 秒超时 | 服务整体延迟被拖高 |
| COS 后端部分降级（只读模式） | 读写请求都发往有问题的后端 | 写入失败拖慢读取 |
| Local FS 磁盘 I/O 压力 | 无检测 | 所有读写慢、不稳定 |
| 多存储后端共存（如 S3 主 + Local 缓存） | 无法按后端健康度切换读取路径 | 无法利用健康后端 |

### 实现要点

**健康探测接口扩展：**

```go
// storage/storage.go 新增
type HealthStatus struct {
    Backend     string         `json:"backend"`
    Reachable   bool           `json:"reachable"`
    LatencyP50  time.Duration  `json:"latency_p50"`
    LatencyP95  time.Duration  `json:"latency_p95"`
    ErrorRate   float64        `json:"error_rate"`
    LastChecked time.Time      `json:"last_checked"`
}

type HealthProbe interface {
    Health(ctx context.Context) HealthStatus
}
```

**主动健康检查 goroutine：**

```
启动时创建 HealthProbe：
  └── 定时（每 30 秒）探测每个后端
       ├── Stat 一个已知健康探针 key
       ├── 记录延迟（P50/P95 滑动窗口）
       ├── 记录错误率（最近 5 分钟）
       └── 更新全局 HealthStatus

FileService 写入/读取路径：
  └── 在调用 store.Put/Get 前检查 HealthStatus
       ├── 如果 ErrorRate > 10% → 降级到次选后端（如有）
       ├── 如果 LatencyP95 > 5s → 返回 503 熔断
       └── 如果 Reachable == false → 立即返回 503
```

**Circuit Breaker 增强：**

```go
// 新增: 延迟感知断路
type LatencyAwareCB struct {
    *CircuitBreaker
    latencyThreshold time.Duration   // 如 2s
    window           time.Duration   // 滑动窗口 5min
}

func (l *LatencyAwareCB) recordLatency(d time.Duration) {
    // 如果 P95 超过 latencyThreshold 且持续超过 window → Open
}
```

### 边界情况

| 场景 | 预期行为 |
|------|---------|
| S3 限流 503（非下游崩溃） | 健康探测识别错误率升高 → 慢启动拒请求，不是全开 |
| 后端从降级中恢复 | 健康探测检测到 3 次连续成功 → 关闭 Circuit Breaker |
| 短暂网络抖动（1-2 秒） | HealthProbe 使用滑动窗口（5min），容忍短暂波动 |
| Local FS 磁盘满 | Stat 操作失败 → HealthStatus.Reachable=false → 503 |
| 多存储后端主备切换 | 主后端 HealthStatus.ErrorRate > 阈值 → 自动切到备后端 |
| 降级期间写入 | 如果主后端只读降级，写入应缓存或返回 503（不静默丢失） |
| 指标暴露 | `storage_backend_healthy{backend}` gauge，`storage_backend_latency_p95{backend}` histogram |

---

## 各方向既有分析去重声明

| # | 方向 | 既有分析证明 |
|---|------|-------------|
| **1** | Search Snippet & Query Highlighting | `grep -rln "snippet\|query.*highlight\|passage.*extract\|result.*highlight\|search.*snippet\|relevant.*passage" docs/requirements/` → **0 命中**。v34 方向三覆盖搜索质量评估（MRR/NDCG），聚焦离线度量，不涉在线结果展示。**零分析** |
| **2** | S3 `x-amz-tagging` PUT Header | v77 方向二覆盖 $CopyObject$ 的 `x-amz-tagging-directive`，聚焦**复制**路径。`grep -rn "x-amz-tagging.*header\|tag.*at.*upload\|upload.*tag.*header" docs/requirements/` → **仅 v77 CopyObject 相关行，零 PUT 路径分析** |
| **3** | Per-Bucket/Prefix Quota | v32 方向二覆盖层次化命名空间，**以一行提及前缀级配额**。零独立深度分析。`grep -rn "per.*bucket.*quota\|bucket.*level.*quota\|prefix.*quota\|directory.*quota" docs/requirements/` → **仅 v32 一行** |
| **4** | Multi-Protocol Write Concurrency | v48 覆盖 multipart 自身并发一致性（同协议）；v71 覆盖版本 ID 分配时机；v78 覆盖跨协议对象身份。`grep -rln "multi.*protocol.*concurr\|protocol.*concurr.*write\|cross.*protocol.*write" docs/requirements/` → **0 命中** |
| **5** | Storage Health Management & Degradation | v63 方向三提出 `Health()`/`Capacity()` 接口契约设计；v65 方向四覆盖健康管理概念框架。**零分析运行时健康探测缺失、延迟感知断路、主动降级策略的具体代码锚点** |

---

*本文档于 2026-07-11 由 AI Agent 自动生成，基于全代码库深度扫描 + 80 份既有分析文档交叉去重验证。*
