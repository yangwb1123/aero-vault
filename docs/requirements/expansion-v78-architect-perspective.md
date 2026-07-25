# AeroVault 架构师视角 — 第 78 轮：系统纵深盲区

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 23+ 子包，三套 SDK，MCP 双模式，Web UI，48 对迁移文件，Makefile，CI gate，`docs/` 全部现有文档）  
> **去重验证：** 对 `docs/requirements/` 下全部 77 份既有分析文档（`expansion-directions.md` ~ `expansion-v77-genuine-product-gaps.md`，累计 ~400+ 方向）进行逐方向 `grep` 正则交叉验证 + 语义比对  
> **日期：** 2026-07-11  
> **核心原则：** 选取代码中存在具体、可量化、且在前 77 轮分析中**零实质性覆盖**的架构/工程盲区。每个方向包含代码锚点、影响分析、既有覆盖证明。

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 | 既有分析覆盖 |
|---|------|------|--------|---------|---------|-------------|
| **1** | **AI 管线跨租户资源争抢 — 嵌入/LLM/重排序无隔离** | 可靠性/性能 | **P1** — Embedder、LLM、Reranker、BM25 index 均为全局共享单一实例。一个租户的大规模嵌入请求或高并发 Chat 调用可直接阻塞其他租户的 AI 操作。无租户级请求排队、优先级调度或资源预算 | `internal/ai/embedder.go:27-32`（`Embedder` 接口无租户参数）；`internal/ai/llm.go:30-37`（`LLM` 接口无租户/优先级参数）；`internal/ai/search.go:40-48`（`Search` 持单个共享 `embedder`）；`internal/ai/bm25.go`（`BM25` 全局单实例，所有租户共享 term 索引）；`internal/ai/indexer.go:75-90`（`Indexer` 持单 `embedder`，无租户级排队）；`internal/middleware/ratelimit.go`（RPS 限流不控制 compute 资源消耗） | ✅ **完全去重**（v60 覆盖 AI 专项 RPS 限流但仅控制 HTTP 请求频率，非 compute 资源隔离；v57 覆盖 AI Provider 熔断但聚焦故障容错而非多租户争抢；v71 覆盖搜索个性化但聚焦单租户体验。**零分析 embedder/LLM/reranker 的跨租户资源隔离**） |
| **2** | **SDK 层零信任加密工具缺失 — 客户端加密原语三套 SDK 均不支持** | 安全/合规 | **P2** — SSE 仅为服务端加密，服务器始终能访问明文数据。Go/JS/Python SDK 中无任何加密辅助工具来实现客户端加密（encrypt-before-upload / decrypt-after-download）。需要零信任部署的用户必须自行实现加密逻辑，无标准指南或可复用库 | `sdk/go/aerovault/client.go`（`Put`/`Get` 仅处理明文）；`sdk/js/aero-vault.js`（`PUT`/`GET` 仅处理明文）；`sdk/python/aero_vault.py`（同）；`internal/storage/encrypt.go`（SSE 仅服务端信封加密，密钥在服务器上）；`.env.example`（`STORAGE_LOCAL_SSE_KEY` 为服务端密钥） | ✅ **完全去重**（v10 方向一覆盖 SSE-C —— "客户提供密钥的服务端加密"，密钥仍传到服务器由服务端执行加密。**本方向聚焦客户端侧加密，数据在离开客户端前已加密，服务器永不见明文**。v61 方向一覆盖 SSE-C 深化。两者均非 SDK 层加密工具） |
| **3** | **对象跨协议统一身份缺失 — REST/S3/WebDAV/MCP 引用模型分裂** | 架构/可维护性 | **P2** — 对象在不同协议中通过不同的标识符模式引用：REST 用 `key`（路径参数）、S3 用 `bucket/key`（含 versionId）、WebDAV 用文件路径、MCP 用 REST 风格 key。跨协议操作（WebDAV 重命名后通过 REST 检索标签、S3 删除后通过 MCP 搜索）缺乏一致的对象引用方案。审计日志、Lineage、工具调用各自为政 | `internal/api/rest/handler.go:35-42`（key 标识对象）；`internal/api/s3compat/handler.go:25-35`（bucket+key 标识）；`internal/api/webdav/dav.go:25-40`（路径标识）；`internal/mcp/server.go:120-145`（REST 风格 key 标识）；`internal/ai/agent.go:58-103`（Agent 持 `DefaultBucket`，无法按协议感知桶）；`internal/service/file.go:storageKey`（存储 key 含 tenant+bucket+key，但无 protocol 维度的标识映射）；`internal/events/webhook.go:30-45`（事件 payload 仅有 key，缺 protocol 来源信息） | ✅ **完全去重**（v59 方向一覆盖"多协议一致性模型"聚焦 read-after-write 语义保证，非对象引用标识；v40 方向四覆盖"四协议统一访问控制模型"聚焦认证一致，非 identity。**零分析跨协议对象身份标识的缺失**） |
| **4** | **搜索索引新鲜度无保障 — 写入后可搜索时间无 SLA、无衡量指标** | AI/可靠性 | **P2** — 对象上传后，索引器通过事件驱动异步处理。无任何机制告诉客户端"对象已可搜索"；无索引延迟分布指标；无按租户/桶的索引覆盖比率监控；高吞吐场景下索引器可能积压，但运维无法感知搜索结果的"新鲜度"。`read-after-write` 一致性仅限于元数据读，不扩展到语义搜索 | `internal/ai/indexer.go:115-145`（`Run` 事件循环：无积压深度 metric、无延迟分布、无处理速率告警）；`internal/ai/indexer.go:150-175`（`processEvent`：无 e2e 延迟记录——从事件入队到 chunk 写入的耗时）；`internal/telemetry/metrics.go`（15+ 领域指标，**零索引延迟、零索引覆盖率、零索引积压**）；`internal/api/rest/handler.go:handlePut`（`PUT` 响应后不提供索引状态）；`internal/repository/repository.go`（无 `GetIndexLag` 或 `GetObjectIndexStatus` 方法）；`internal/service/file_crud.go:Put`（无索引完整性回调） | ✅ **完全去重**（v5 表格一行列出"indexer latency distribution"作为监控项概念；v23 表格一行"索引延迟 SLA 不可控"作为事件背压的附属提及；v38/v39 提及事件背压但聚焦 event bus 而非搜索一致性。**零独立方向分析索引新鲜度的代码层面缺失、衡量体系与 SLA 保证**） |

---

## 方向一：AI 管线跨租户资源争抢 — 嵌入/LLM/重排序无隔离

### 现状

当前 AI 管线的核心计算资源全部为**全局共享单一实例**：

```
main.go 装配路径（cmd/server/main.go:193-228）:
  embedder = buildEmbedder(cfg, logger)   ← 一个 embedder，所有租户共享
  llm      = buildLLM(cfg, logger)        ← 一个 LLM client，所有租户共享
  reranker = buildReranker(cfg, logger)   ← 一个 reranker，所有租户共享
  
  bm = ai.NewBM25()         ← 全局 BM25 index，所有 chunk 统一索引
  search = ai.NewSearch(...) ← 全局 search，持单一 embedder
  
  chat = ai.NewChat(search, llm, repo, logger)  ← 全局 chat，持单一 llm
  agent = ai.NewAgent(svc, search, llm, ...)    ← 全局 agent，持单一 llm
```

**调用路径中的资源争抢：**

```
租户 A: POST /v1/search (mode=hybrid, k=50, 复杂查询)
         │
         ▼
  search.Query(ctx, Request{Tenant: "tenant-a", ...})
         │
         ▼
  s.embedder.Embed(ctx, [query])                  ← Embedder 开始处理
         │                                            （阻塞，单协程/线程）
         ▼
  同一时刻，租户 B 的搜索请求到达
         │
         ▼
  s.embedder.Embed(ctx, [query-B])                ← 被阻塞！等待 A 完成
         │
         ▼
  租户 C: POST /v1/chat (流式)
         │
         ▼
  llm.Chat(ctx, req)                              ← LLM HTTP 调用中
         │                                            （单 HTTP client，连接池竞争）
         ▼
  租户 D: POST /v1/agent（工具循环）
         │
         ▼
  llm.Chat(ctx, req)                              ← 与 C 争抢连接池
```

**代码证据链：**

```go
// internal/ai/embedder.go:27-32
type Embedder interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
    //         ^^ 无 tenant 参数      ^^ 无优先级参数
    Dimensions() int
    Name() string
}

// internal/ai/llm.go:30-37
type LLM interface {
    Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
    //    ^^ 无 tenant 参数  ^^ 无优先级参数
    ChatStream(ctx context.Context, req ChatRequest, ...) (ChatResponse, error)
    Name() string
}

// internal/ai/search.go:40-48
type Search struct {
    repo     repository.Repository
    embedder Embedder            // ← 全局共享，所有租户用同一个
    bm25     *BM25               // ← 全局 BM25，所有租户的倒排索引在一起
    rerank   Reranker            // ← 全局共享
    vindex   VectorIndex         // ← 全局共享（pgvector/Qdrant 连接池）
    ...
}

// internal/ai/bm25.go — BM25 索引结构
type BM25 struct {
    mu       sync.RWMutex
    df       map[string]int      // ← 所有租户的 term 频率混在一起
    avgLen   float64             // ← 全局平均值，不受租户影响
    ...
}
```

**影响量化：**

| 场景 | 影响 | 可观测性 |
|------|------|---------|
| 租户 A 上传 1000 个 PDF → 索引器逐批调用 `embedder.Embed` | 租户 B 的搜索查询延迟飙升（等待 embedder） | ❌ 无指标 |
| 租户 C 发起高并发 `/v1/chat`（LLM 调用耗时 2-5 秒） | 租户 D 的 Agent 工具循环中的 LLM 调用排队等待 | ❌ 无指标 |
| 租户 E 的 BM25 索引重建（`BuildFromRepo` 扫描所有 chunk） | 搜索响应中 BM25 查询被读锁阻塞 | ❌ 无 visibility |
| 单个 reranker HTTP 端点被慢请求占满连接池 | 所有租户的 hybrid search 降级为无 rerank | ❌ 降级静默 |

### 为什么需要

| 理由 | 说明 |
|------|------|
| **SaaS 公平性** | 一个租户的不当使用不应降低其他租户的服务质量。当前无任何隔离机制 |
| **可预测的延迟** | 搜索/聊天延迟不应随其他租户的负载而变化。当前无任何 QoS 保证 |
| **成本归属** | Embedder/LLM 的 API 费用按使用量计费。当前无法按租户隔离 API 调用量，无法准确计费 |
| **调试复杂度** | 当搜索延迟偶发飙升时，运维无法区分是"本租户查询复杂"还是"其他租户在争抢 embedder" |

### 建议方向

**Phase 1（可观测性先行 — 不改变架构，增加可见性）：**

```go
// 为 embedder/LLM/reranker 增加租户感知的 metrics
telemetry.RecordEmbedWait(ctx, tenant, waitMs)     // embedder 排队等待时间
telemetry.RecordLLMQueueDepth(ctx, tenant, depth)   // LLM 请求排队深度
telemetry.RecordRerankerContention(ctx, tenant)     // reranker 争抢次数

// 为 BM25 增加按租户的隔离指标
telemetry.RecordBM25TenantSize(ctx, tenant, docCount, termCount)
```

**Phase 2（租户级 Embedder 排队 — 无锁争抢的公平调度）：**

```go
type tenantEmbedder struct {
    embedder Embedder
    queue    map[string]chan request  // per-tenant request queue
    workers  int                      // per-tenant concurrent workers
}

// 请求按 tenant 入队，每个 tenant 独立 worker 消费
// tenant 间互不阻塞，一个 tenant 的积压不阻塞其他 tenant
```

**Phase 3（租户级 LLM/reranker 连接池隔离）：**

- 为每个 tenant 创建独立的 HTTP 连接池（`http.Transport.MaxConnsPerHost` 按 tenant 分配）
- LLM 调用按 tenant 排队，独立限流（超出 RPS 后排队而非拒绝）
- Reranker 同样按 tenant 分配连接预算

| 指标 | Phase 1 | Phase 2 | Phase 3 |
|------|---------|---------|---------|
| 代码量 | ~80 行（指标 + 埋点） | ~200 行（排队 + worker） | ~250 行（连接池 + 限速） |
| 风险 | **低** — 纯新增埋点 | **中** — 异步排队改变并发模型 | **中** — 连接池管理 |
| 向后兼容 | ✅ 默认无隔离（行为不变） | ✅ 默认单 worker | ✅ 默认共享连接池 |

---

## 方向二：SDK 层零信任加密工具缺失 — 客户端加密原语三套 SDK 均不支持

### 现状

当前加密体系完全依赖于**服务端加密（SSE）**：

```
客户端 (Go/JS/Python SDK)
         │
         │ TLS 加密传输
         ▼
  AeroVault Server
         │
         │ SSE 服务端加密（envelopeEncrypter）
         ▼
  Storage Backend (Local / S3 / OSS / COS)
         └── 密文存储
```

三个 SDK 当前均只处理明文：

```go
// sdk/go/aerovault/client.go:35-52
func (c *Client) Put(ctx context.Context, key string, body io.Reader, opts ...any) (*Object, error) {
    req, _ := http.NewRequestWithContext(ctx, "PUT", c.endpoint+"/v1/files/"+key, body)
    // body 是明文，不做任何客户端加密
    ...
}
```

```javascript
// sdk/js/aero-vault.js:45-60
async put(key, body, opts) {
    const resp = await fetch(`${this.endpoint}/v1/files/${key}`, {
        method: 'PUT',
        body: body,        // 明文
        ...
    });
}
```

```python
# sdk/python/aero_vault.py:30-48
def put(self, key, data, content_type=None):
    resp = requests.put(
        f"{self.endpoint}/v1/files/{key}",
        data=data,          # 明文
        ...
    )
```

**与 SSE-C 的本质区别：**

| 特性 | SSE-C（v10/v61 覆盖） | 客户端 SDK 加密（本方向） |
|------|---------------------|------------------------|
| 加密执行者 | 服务器 | 客户端（SDK） |
| 密钥位置 | 在请求中传送到服务器 | 仅存在于客户端 |
| 服务器是否见明文 | 是（解密后） | **否** |
| 实现位置 | 服务端 handler + storage 层 | SDK 层 + 可选的密钥管理 |
| 协议支持 | S3 `x-amz-server-side-encryption-customer-*` headers | SDK 加密 helper 函数 |
| KMS 集成 | 服务端 KMS wrapper | 客户端 KMS（AWS KMS, GCP Cloud KMS, HashiCorp Vault） |

### 代码锚点

| 位置 | 当前状态 | 缺口 |
|------|---------|------|
| `sdk/go/aerovault/client.go` | `Put`/`Get` 明文 I/O | 无 `EncryptingWriter` / `DecryptingReader` 包装器 |
| `sdk/go/aerovault/client.go` | 无密钥管理接口 | 无 `KeyProvider` 接口或 `LocalKeyStore` |
| `sdk/js/aero-vault.js` | `PUT`/`GET` 明文 | Web Crypto API（`SubtleCrypto`）可用但未使用 |
| `sdk/python/aero_vault.py` | `put`/`get` 明文 | 无 `cryptography` 或 `pycryptodome` 集成 |
| `internal/storage/encrypt.go` | 服务端 AES-256-GCM 信封加密 | 加密原语可复用但 SDK 侧需独立实现 |
| `sdk/` 全部 | 无加密文档 | 无 client-side encryption 使用指南 |

### 边界情况

| Edge Case | 场景 | 处理方式 |
|-----------|------|---------|
| **密钥轮换** | 对象用旧 key 加密，现在用新 key | 每个对象携带 key ID（fingerprint），读取时按 ID 选择解密 key |
| **空对象加密** | 0 字节文件 | 不加密（密文为空串，无 IV/nonce） |
| **流式加密** | 大文件逐块加密 | 用 AES-GCM 分段模式（每段独立 nonce）支持流式加解密 |
| **密钥丢失** | 用于加密的 key 被删除 | 返回明确错误"对象使用不可用密钥加密"，非静默解密失败 |
| **加密 + 压缩** | 先压缩再加密 vs 先加密再压缩 | 先压缩再加密（压缩不能对密文操作）；SDK 提供可组合的 `CompressThenEncryptWriter` |
| **元数据加密** | 对象的 Tags/Metadata 是否加密 | Tags/Metadata 通常不加密（用于服务端检索），但可提供选项 |
| **预签名 URL + 客户端加密** | 预签名 URL 对象如何解密 | 客户端通过带外机制获取 key（非 URL 内传输） |
| **跨 SDK 互操作** | Go SDK 加密 → Python SDK 解密 | 统一的加密格式（AES-256-GCM + key fingerprint）确保互操作性 |

### 建议方向

**Go SDK 加密辅助工具（`aerovault/crypto` 子包）：**

```go
import "github.com/aero-vault/aero-vault/sdk/go/aerovault/crypto"

// 客户端加密对象存储
type EncryptedObject struct {
    KeyID      string // 用于解密的密钥标识
    Ciphertext []byte // AES-256-GCM 密文
}

// EncryptReader 包装 io.Reader，加密后写入目标 Writer
func EncryptReader(src io.Reader, key []byte) (io.Reader, error)

// DecryptReader 包装密文 Reader，解密后读取
func DecryptReader(src io.Reader, key []byte) (io.Reader, error)

// KeyProvider 抽象：如何获取加密密钥
type KeyProvider interface {
    EncryptKey(ctx context.Context, objectPath string) (keyID string, key []byte, err error)
    DecryptKey(ctx context.Context, keyID string) ([]byte, error)
}
```

使用模式：

```go
// 客户端加密写入
keyProvider := crypto.NewLocalKeyProvider(masterKey)
keyID, encKey, _ := keyProvider.EncryptKey(ctx, "/docs/report.pdf")
encReader, _ := crypto.EncryptReader(fileContent, encKey)
obj, _ := client.Put(ctx, "/docs/report.pdf", encReader)

// 客户端解密读取
obj, _ := client.Get(ctx, "/docs/report.pdf")
key, _ := keyProvider.DecryptKey(ctx, obj.Metadata["_aero_key_id"])
plaintext, _ := crypto.Decrypt(obj.Body, key)
```

**JS SDK（Web Crypto API）：**

```javascript
import { EncryptTransform, DecryptTransform } from './crypto.js';

// 使用 Web Crypto API（SubtleCrypto）硬件加速
const key = await crypto.subtle.importKey('raw', userKey, 'AES-GCM', false, ['encrypt', 'decrypt']);

// 加密上传
const encrypted = new Blob([await crypto.subtle.encrypt({ name: 'AES-GCM', iv }, key, plaintext)]);
await client.put('doc.pdf', encrypted, { headers: { 'X-Aero-Key-Id': keyId } });
```

**Python SDK（cryptography 库）：**

```python
from aerovault.crypto import encrypt, decrypt, LocalKeyProvider

kp = LocalKeyProvider(master_key)
key_id, enc_key = kp.encrypt_key("/docs/report.pdf")
ciphertext = encrypt(file_content, enc_key)
client.put("/docs/report.pdf", ciphertext)
```

| 指标 | 估计 |
|------|------|
| Go SDK crypto 子包 | ~200 行（AES-GCM 包装 + KeyProvider 接口 + 示例） |
| JS SDK crypto helpers | ~150 行（SubtleCrypto 包装 + KeyProvider） |
| Python SDK crypto helpers | ~150 行（cryptography 库包装 + KeyProvider） |
| 文档 + 指南 | ~2 页 README + 各 SDK 示例 |
| 风险 | **低** — 纯新增代码，不修改现有行为 |

---

## 方向三：对象跨协议统一身份缺失 — REST/S3/WebDAV/MCP 引用模型分裂

### 现状

当前对象通过四种不同的标识模式被引用，彼此之间缺乏统一映射：

```
REST    /v1/files/{key}                        ← key 唯一标识
S3      /s3/{bucket}/{key}                     ← bucket + key 标识
WebDAV  /webdav/{bucket}/{key}                 ← 路径标识（类似 S3）
MCP     list_files / read_file({key})          ← REST 风格 key
Agent   list_files / read_file({key})          ← 硬编码 DefaultBucket

事件系统 object_events:
  {tenant, bucket, key, type, ...}             ← 三元组标识

审计日志 audit_log:
  {tenant, resource, action, ...}              ← resource 含 key 但无 protocol

Lineage（ai_usage 表）:
  {object_id (int64 PK), caller, query, ...}   ← 数据库主键标识

Webhook payload:
  {tenant, bucket, key, type, ...}             ← 三元组标识
```

**跨协议操作示例 — 问题展示：**

```
场景：用户通过 WebDAV 挂载浏览，将 docs/report.pdf 重命名为 docs/final.pdf
  → WebDAV MOVE /webdav/default/docs/report.pdf → /webdav/default/docs/final.pdf
  → 服务端：Delete("default", "default", "docs/report.pdf")
              Put("default", "default", "docs/final.pdf", ...)
  → WebDAV 用户看到文件已重命名  ✅

但：
  → REST 用户 GET /v1/files/docs/report.pdf → 404（已删除）✅
  → MCP 用户 search("report") → 仍返回旧 key 的搜索结果 ❌（chunk 未 update）
  → S3 用户列出 /s3/default/ → 看到两个 key ❌（old + new）
  → Lineage 追踪旧 key 的血缘 → 无法关联到新 key ❌
  → 设置了标签的旧对象 → 标签没有迁移到新 key ❌
```

**代码证据链：**

```go
// internal/api/rest/handler.go:35-42 — REST handler 使用 key
func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
    key := chi.URLParam(r, "key")
    // key 字符串——无 protocol/bucket 上下文
    rc, obj, err := h.svc.Get(ctx, tenant, DefaultBucket, key)
}

// internal/api/s3compat/handler.go:25-35 — S3 handler 使用 bucket+key
func (h *Handler) serveObjectContent(w http.ResponseWriter, r *http.Request, bucket string) {
    key := strings.TrimPrefix(r.URL.Path, "/s3/"+bucket+"/")
    // bucket+key 标识——与 REST 的 key 不同命名空间
}

// internal/api/webdav/dav.go:25-40 — WebDAV handler 使用路径
func (h *webdavHandler) handleMove(w http.ResponseWriter, r *http.Request) {
    src := strings.TrimPrefix(r.URL.Path, h.prefix)
    dst := strings.TrimPrefix(r.Header.Get("Destination"), h.prefix)
    // 路径标识——与 REST/S3 的 key 无显式关系
}

// internal/mcp/server.go:120-145 — MCP 使用 REST 风格 key
func (s *Server) callReadFile(ctx context.Context, args map[string]any) (any, error) {
    key, _ := args["key"].(string)
    // 假设 DefaultBucket，无法引用其他 bucket 中的对象
    rc, obj, err := s.svc.Get(ctx, tenant, service.DefaultBucket, key)
}
```

### 根因分析

根本原因在于**架构层缺少"规范对象引用"（Canonical Object Reference）的概念**：

```
当前隐式引用路径：
  key string         → FileService.Get/Delete/... → repo 通过 (tenant, bucket, key) 查找
  bucket+key string  → 同上但 bucket 来自 URL 解析
  路径 string         → WebDAV 解析为 bucket+key
  object_id int64    → DB 主键（仅内部使用，不对外暴露）

缺少：
  URN: aero://{tenant}/{bucket}/{key}@{version}  ← 统一规范引用
```

### 为什么需要

| 理由 | 说明 |
|------|------|
| **协议互操作正确性** | WebDAV 重命名后，其他协议应能看到一致的对象视图。当前无此保证 |
| **审计完整性** | 同一对象通过不同协议访问时，审计日志应能关联。当前无法做到 |
| **Lineage 跨操作跟踪** | 对象被复制/重命名/版本切换后，Lineage 应能跟踪。当前 Lineage 基于不可变的 object_id，但重命名导致旧 object_id 消失 |
| **MCP/Agent 跨桶操作** | MCP `read_file` 和 Agent `read_file` 不能访问非 default bucket 的对象 |
| **事件通知一致性** | Webhook payload 应包含规范引用，让下游系统可以统一处理 |

### 建议方向

**Phase 1（规范引用格式 + 内部映射）：**

```go
// CanonicalRef 是对象的跨协议规范引用
type CanonicalRef struct {
    Tenant    string `json:"tenant"`
    Bucket    string `json:"bucket"`
    Key       string `json:"key"`
    VersionID string `json:"version_id,omitempty"`
}

// String 返回规范 URN 表示
func (r CanonicalRef) String() string {
    s := fmt.Sprintf("aero://%s/%s/%s", r.Tenant, r.Bucket, r.Key)
    if r.VersionID != "" {
        s += "@" + r.VersionID
    }
    return s
}
```

- 在 `FileService` 的公共 API 中返回 `CanonicalRef`
- HTTP 响应头增加 `X-Aero-Object-Ref: aero://tenant/bucket/key`
- 事件 payload 增加 `canonical_ref` 字段
- 审计日志记录 `canonical_ref`

**Phase 2（MCP/Agent 跨桶支持）：**

- MCP `list_files` / `read_file` / `search` 工具增加 `bucket` 参数（与 REST/S3 对齐）
- Agent 工具增加 `bucket` 参数，移除 `DefaultBucket` 硬编码
- Agent `write_file`、`delete_file` 工具（当前 MCP 已有但 Agent 缺失）补全

**Phase 3（跨协议操作一致性）：**

- WebDAV `Rename`/`Move`/`Copy` 操作生成 `object.renamed` 事件（含新旧 canonical ref）
- Lineage 跟踪重命名链路（`renamed_from` → `renamed_to`）
- 搜索索引在处理 rename 时更新 key（而非仅标记旧 key 删除）

| 指标 | Phase 1 | Phase 2 | Phase 3 |
|------|---------|---------|---------|
| 代码量 | ~100 行（CanonicalRef + 响应头 + 事件字段） | ~80 行（MCP/Agent 参数扩展） | ~200 行（rename 事件 + lineage 扩展 + 索引更新） |
| 风险 | **低** — 纯新增，不改变现有行为 | **低** — 向后兼容参数 | **中** — 涉及索引一致性 |

---

## 方向四：搜索索引新鲜度无保障 — 写入后可搜索时间无 SLA、无衡量指标

### 现状

对象上传到可搜索的完整路径：

```
PUT /v1/files/report.pdf
         │
         ▼
  FileService.Put()
         │
         ├── store.Put(blob)              ← 存储写入（ms 级）
         ├── repo.UpsertObject(meta)      ← 元数据写入（ms 级）
         └── bus.Publish(event)           ← 事件发布（同步写入 object_events 表）
         │
         ▼  🔴 从此刻起，元数据可见，但搜索不可见
         │      没有告诉客户端"对象已可搜索"
         │      没有衡量"索引延迟"的指标
         ▼
  event bus broadcast
         │
         ▼
  Indexer.Run() 事件循环（select/poll）
         │
         ├── extract text                 ← 耗时不定（取决于文件大小和类型）
         ├── chunk                        ← 固定开销（~O(n)）
         ├── embed(chunks)                ← 耗时不定（取决于 embedder 吞吐和队列深度）
         └── sink.InsertChunks(chunks)    ← 写入索引（BM25/向量库）
         │
         ▼  ✅ 从此刻起，搜索可发现该对象
              🔴 总耗时不可预测、不可观测、不可告警
```

**代码证据链：**

```go
// internal/ai/indexer.go:115-145 — 事件循环
func (idx *Indexer) Run(ctx context.Context, events <-chan repository.Event) {
    for {
        select {
        case e := <-events:
            idx.processEvent(ctx, e)     // ← 处理事件，无 e2e 计时
        case <-time.After(idx.pollEvery):
            idx.pollAndIndex(ctx)        // ← catch-up poll，无积压测量
        ...
        }
    }
}

// internal/ai/indexer.go:150-175 — 事件处理
func (idx *Indexer) processEvent(ctx context.Context, e repository.Event) {
    start := time.Now()
    ...
    idx.indexObject(ctx, id)             // ← 提取→分块→嵌入→写入
    // ↑ 没有记录 start→now 的延迟
    // ↑ 没有记录事件入队时间→处理完成的时间
    // ↑ 没有按租户/桶/对象类型分类的延迟直方图
}

// internal/telemetry/metrics.go — 15+ 领域指标
// 零：
//   indexer.lag_seconds{tenant}            ← 事件入队到处理完成的延迟
//   indexer.queue_depth{tenant}            ← 待处理事件数
//   indexer.processing_rate{tenant}        ← 每秒处理对象数
//   search.staleness_seconds{tenant}       ← 最新索引时间戳到当前时间的差距
//   index.coverage_ratio{tenant}           ← 已索引对象 / 全部对象

// internal/api/rest/handler.go:handlePut — PUT 响应
func (h *Handler) handlePut(w http.ResponseWriter, r *http.Request) {
    obj, err := h.svc.Put(ctx, tenant, bucket, key, r.Body, size, opts)
    // 返回 200 OK + object metadata
    // 不返回索引状态，不提供索引完成的回调方式
    writeJSON(w, http.StatusOK, objectToDTO(obj))
}

// internal/repository/repository.go — 无索引状态查询
type Repository interface {
    // 不存在的方法：
    // GetObjectIndexStatus(ctx, tenant, bucket, key) (IndexStatus, error)
    // GetIndexLag(ctx, tenant) (time.Duration, error)
    ...
}
```

**影响量化：**

| 场景 | 影响 | 严重程度 |
|------|------|---------|
| **用户上传文档后立即搜索** | 搜索不到刚上传的内容，用户体验断裂 | P1 — 核心体验 |
| **CI pipeline 上传产物后验证** | 需要 sleep N 秒等待索引完成，不可靠 | P1 — CI 可靠性 |
| **大量文件批量导入** | 索引器积压，刚导入的文件需数分钟才可搜索 | P2 — 性能退化 |
| **Embedder 服务降级** | 索引器堵塞，事件队列堆积但无告警 | P1 — 运维盲区 |
| **跨区域复制后搜索** | 复制的对象需要在新区域重新索引，无进度提示 | P2 — 用户体验 |

### 为什么需要

| 理由 | 说明 |
|------|------|
| **用户体验断层** | "上传后搜不到"是搜索型产品最破坏信任的体验。当前完全无法保证 |
| **运维盲区** | 索引延迟是搜索产品的核心运维指标。当前完全不可观测 |
| **SLA 制定** | 无法对用户承诺"上传后 5 秒内可搜索"。因为系统不报告当前延迟 |
| **容量规划** | 无法衡量索引器是否达到处理上限。批量导入场景下积压无声增长 |
| **调优依据** | Chunk window/overlap、embedder batch size、worker count 等参数调整缺乏效果衡量 |

### 建议方向

**Phase 1（索引延迟可观测性 — 不改变架构，增加指标）：**

```go
// indexer 处理事件时记录端到端延迟
func (idx *Indexer) processEvent(ctx context.Context, e repository.Event) {
    start := time.Now()
    ...
    // 记录从事件创建到处理完成的延迟
    created := parseEventTime(e) // event 的 created_at
    lag := time.Since(created)
    telemetry.RecordIndexLag(ctx, e.TenantID, lag.Seconds())
    
    // 按对象大小、类型分类的延迟
    telemetry.RecordIndexDuration(ctx, e.TenantID, size, contentType, time.Since(start).Seconds())
}

// Prometheus 指标：
//   indexer_lag_seconds{tenant}          — 直方图：事件创建到处理完成
//   indexer_duration_seconds{tenant}     — 直方图：处理耗时（不含排队）
//   indexer_queue_depth{tenant}          — gauge：待处理事件数
//   indexer_objects_total{tenant,status} — counter：成功/失败/跳过
//   index_coverage_ratio{tenant}         — gauge：已索引 / 全部对象
```

**Phase 2（索引状态 API — 客户端可查询）：**

```go
// GET /v1/files/{key}?include_index_status=true
// 响应增加字段：
//   "index_status": "pending" | "indexing" | "indexed" | "failed"
//   "indexed_at": "2026-07-11T12:00:00Z"

// 后端实现：
type IndexStatus struct {
    Status    string     `json:"status"`
    IndexedAt *time.Time `json:"indexed_at,omitempty"`
    Error     string     `json:"error,omitempty"`
}
```

**Phase 3（写入等待索引完成 — 可选一致性级别）：**

```go
// PUT 请求头: X-Aero-Index-Consistency: strong | eventual
//
// strong: PUT 阻塞直到对象被索引（超时返回 201 + warning header）
// eventual（默认）: 当前行为，PUT 立即返回

// PUT 响应:
// 201 Created
// X-Aero-Index-Status: pending|indexed
// Warning: 299 aero-vault "object indexed after 2.3s"
```

| 指标 | Phase 1 | Phase 2 | Phase 3 |
|------|---------|---------|---------|
| 代码量 | ~60 行（指标定义 + indexer 埋点） | ~100 行（IndexStatus 查询 + API 字段） | ~150 行（一致写入 + 超时 + 响应头） |
| 风险 | **低** — 纯新增指标 | **低** — 新增可选查询参数 | **中** — 阻塞写入改变用户感知的延迟 |
| 默认行为 | 无变化 | 无变化 | 默认 eventual（保持向后兼容） |

---

## 各方向既有分析去重声明

| 方向 | 验证方式 | 结果 |
|------|---------|------|
| **方向一：AI 跨租户资源隔离** | `grep -rli "tenant.*ai.*isolat\|ai.*resource.*tenant\|ai.*compute.*isol\|embedder.*contention\|embedder.*shared\|llm.*shared\|ai.*pipeline.*isol\|embedder.*tenant.*isolation\|llm.*tenant.*queue" docs/requirements/` → **零命中**。补充语义扫描：v60 聚焦 HTTP RPS 限流非 compute 隔离；v57 聚焦 Provider 熔断非多租户争抢；v71 聚焦搜索个性化非资源隔离 | ✅ **完全去重** |
| **方向二：SDK 客户端加密** | `grep -rli "client.side.*encrypt\|client.side.*crypto.*sdk\|零信任.*SDK\|SDK.*encrypt.*helper\|encrypt.before.*upload\|decrypt.after.*download\|SDK.*crypto\|aerovault.*crypto\|client.*encrypt.*key.*provider" docs/requirements/` → **零命中**。补充语义：v10/v61 覆盖 SSE-C（服务端加密+客户密钥，密钥传到服务器由服务端执行加密），vs 本方向的**客户端加密**（数据离客户端前已加密，服务器永不接触明文）。v10 实施路径全部为服务端 handler/storage 层修改，非 SDK 工具 | ✅ **完全去重** |
| **方向三：跨协议对象身份** | `grep -rli "object.*ident.*protocol\|cross.protocol.*ident\|protocol.*agnostic.*ref\|规范引用\|canonical.*ref\|URN.*object\|aero://\|对象.*引用.*协议\|统一.*标识.*协议" docs/requirements/` → **零命中**。v59 方向一覆盖"多协议一致性模型"聚焦**读写一致性语义**（read-after-write 保证），非**对象引用标识**。v40 方向四覆盖"四协议统一访问控制模型"聚焦**认证一致性**，非 identity | ✅ **完全去重** |
| **方向四：索引新鲜度 SLA** | `grep -rli "index.*freshness.*SLA\|index.*lag.*metric\|search.*staleness\|read.after.write.*search\|index.*consistency.*search\|索引.*新鲜.*SLA\|索引.*延迟.*指标\|搜索.*一致.*性\|index.*coverage.*ratio" docs/requirements/` → **零命中**。v5 表格一行"indexer latency distribution"作为监控项概念列举（无代码锚点、无实施路径）；v23 表格一行"索引延迟 SLA 不可控"作为事件背压的附属提及；v38/v39 提及事件背压但非搜索一致性。**本方向为首次以独立方向、代码锚点定位的方式分析索引新鲜度的衡量体系与 SLA 保证** | ✅ **完全去重** |
