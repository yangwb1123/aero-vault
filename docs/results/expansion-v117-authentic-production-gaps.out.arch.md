# 架构分析报告：AeroVault 系统深层评估

---

## 1. 架构评估

### 1.1 当前架构的优势

| 维度 | 评分 | 说明 |
|------|------|------|
| 层次化设计 | ⭐⭐⭐⭐⭐ | 六层清晰 (Protocol → Middleware → Service → Storage/Repository → Eventing → AI)，每层职责明确，无跨层依赖 |
| 抽象契约质量 | ⭐⭐⭐⭐½ | `Storage` 接口小巧（~12 方法），`Repository` 接口完整但不臃肿；`SecretProvider` 接口设计精良（`Current` + `Resolve` 双方法优雅支持轮换） |
| 多协议一致性 | ⭐⭐⭐⭐⭐ | REST/S3/WebDAV/MCP 共享同一 `FileService` 入口，对象写入后立即可见——这是所有文件存储系统的核心价值 |
| 多租户模型 | ⭐⭐⭐⭐ | 基于 Header 的租户隔离 + storage key 前缀布局 + `*` operator key 机制，简单而有效 |
| Opt-in 安全默认 | ⭐⭐⭐⭐⭐ | AI/pgvector/Qdrant/Webhook/WebDAV 全部默认关闭，`nil` embedder/llm 不阻断 CRUD 路径 |

### 1.2 结构性局限性

**问题 1：优雅关停存在实质缺口（P0 级技术债）**

关停流程在 `internal/shutdown/group.go` 定义了 6 个阶段（HTTP→Bus→Workers→Wait→OTel→DB），但有两个关键问题：

- **`runServer()` 不调用 `shutdown.Group`**：`cmd/server/main.go` 的 `runServer` 函数直接用 `srv.Shutdown()` + `bus.Close()` + `shutdownOtel()`，没有走 `Group.Shutdown()` 的相位编排。这意味着 `Group` 的相位钩子无法影响实际关停流程——**Group 注册的 goroutine 会被 cancel，但关停的时序控制实际上是缺失的**。

- **18 处 `io.Copy` 不感知 ctx**：`internal/antivirus/worker.go:113`（`io.Copy(io.Discard, rc)` 排空剩余字节）和 `internal/reconcile/scrub.go:84`（`io.Copy(h, rc)` 计算 MD5）在 ctx 取消后仍继续运行。Antivirus 扫描大文件（例如 500MB）时，`scanner.Scan` 消耗主体内容后，`io.Copy(io.Discard, rc)` 排空剩余部分可能数秒到数分钟。更关键的是 `reconcile/scrub.go` —— GC 路径可能在关停后仍持有存储层句柄。

**问题 2：写入路径的全内存加载导致 OOM 风险（P1 级技术债）**

`internal/storage/local_write.go` 中 SSE 加密路径：
```go
plain, err := io.ReadAll(reader)  // ← 整个对象加载到内存
ct, env, err := s.enc.encrypt(plain)
```
`internal/storage/local_multipart.go` 中 `mergeEncrypted` 路径：
```go
var buf bytes.Buffer            // ← 整个对象加载到内存
total, err = writePartsTo(dir, parts, &buf)
ct, env, err := s.enc.encrypt(buf.Bytes())
```

这意味着**单个 4GB 文件的上传在本地 SSE 加密时将消耗 4GB+ 内存**。即便对象体本身在正常 PUT 时是流式写入 temp 文件的，SSE 加密路径破坏了流式特性。在拥有大量并发大文件写入的生产环境中，这是一个可预期的 OOM 面。

**问题 3：SSE 密钥环冷启动后无法热加载（P2 级技术债）**

`internal/storage/secret.go` 中 `newHTTPProvider` 文档清晰写道：
```go
// The ring is fetched once; rotation is picked up on the next restart.
```

对于生产级密钥管理，这意味着：
- 密钥泄露后必须重启所有实例
- 每日密钥轮换策略需要编排滚动重启
- KMS 后端（`newDataKeyWrapper`）同样一次性初始化

对比参考：AWS KMS 的 key 缓存 TTL 可低至 1 秒，Vault 的主动续租机制也可分钟级热加载。

**问题 4：Multipart 上传的本地存储易失性（P2 级技术债）**

`internal/storage/local_multipart.go` 中分片存储在 `<root>/.multipart/<uploadID>/part-NNNNN`，完全依赖内存中的 `localUpload` 映射：
```go
s.mu.Lock()
s.uploads[uploadID] = &localUpload{key: key, dir: dir, createdAt: time.Now(), opts: opts}
s.mu.Unlock()
```

服务器崩溃后：
- 所有未完成的分片上传丢失
- `.multipart/` 下的中间文件变成孤儿，从不清理
- `List` 排除了 `.multipart` 目录，但无 GC 流程回收

这在高可用多副本部署中是严重问题——负载均衡器将 multipart 的不同 part 请求路由到不同实例时会产生"未知 upload ID"错误。

**问题 5：请求头租户注入面未统一（P2 级技术债）**

方向一的发现显示：
- **Bearer 路径**：tenant mismatch → `403`（正确行为）
- **SigV4 路径**：自动覆写 tenant → 静默修正（功能正确但行为差异）
- **匿名读路径**（`r.anonRead`）：完全无 tenant 校验
- **MCP stdio 模式**：回退到配置默认值

差异本身不是漏洞，但意味着**租户隔离的保证强度因认证方式而异**。在混用多种认证方式的部署中，审计路径可能不一致。

### 1.3 关键设计决策审慎

| 决策 | 审慎结论 | 理由 |
|------|---------|------|
| `FileService` 作为唯一入口 | ✅ 正确 | 所有协议共享同一个业务逻辑边界，验证规则、配额、事件发布不会遗漏 |
| 单 storage key 前缀布局 | ✅ 正确 | `tenant/bucket/key` 映射简单且可预测，GC 不依赖反解析 |
| 基于 Header 的租户 | ✅ 正确 | 比 URL 路径租户更灵活，中间件层即可完成，handler 零耦合 |
| SQLite 作为默认后端 | ✅ 正确 | 零配置部署 + 纯 Go 无 CGO 依赖，CI 零网络零容器 |
| SSE 加密采用 envelope 模式 | ✅ 正确 | 数据密钥由 master key 包裹存储，轮换 master key 无需重新加密对象体 |
| In-process event bus | ⚠️ 有代价 | 跨实例事件分发缺失，多副本需要 Postgres LISTEN/NOTIFY 或其他传输；当前 `WithTransport` 接口已预留 |
| `io.ReadAll` 用于 SSE 加密 | ❌ **错误** | 破坏了 Storage 接口的流式契约，引入 OOM 风险；应当使用 `cipher.StreamWriter` 流式加密 |

---

## 2. 架构扩展方向（5 个高价值方向）

### 方向 A：写入路径流式加密引擎（P0 — 立即行动）

**为什么需要：**
当前 `local_write.go` 和 `local_multipart.go` 中 SSE 加密路径的 `io.ReadAll` 调用是**已知的 OOM 来源**，且破坏了 storage 层的流式抽象。这直接影响系统可承载的最大对象大小和并发写入数。

**核心挑战：**
- Go 标准库 `crypto/aes` + `crypto/cipher` 的 AES-GCM 不支持流式加密（GCM 需要整个 plaintext 计算认证标签）
- 需要替换为 **AES-CTR + HMAC** 或 **AES-CBC + HMAC** 的 encrypt-then-MAC 模式，或使用 Chacha20-Poly1305（支持 `io.Reader` 接口）
- 与现有 envelope 格式的向后兼容：已有对象使用 AES-GCM 加密，新对象使用流式模式——需要 envelope 字段标识加密模式

**架构变更：**

```
当前: io.ReadAll(reader) → encrypt(plain) → io.Copy(tmp, bytesReader(ct))
改为: encryptStream(reader, tmp)  // 全程流式，固定内存 ~64KB
```

- `SecretProvider` 接口不变
- `s.enc.encrypt` 新增 `encryptStream(r io.Reader, w io.Writer) (envelope string, err error)` 方法
- `envelope` 格式增加 `"cipher": "AES-256-GCM" | "AES-256-CTR-HMAC"` 字段
- 读取路径维持兼容：读取时根据 envelope 中的 cipher 字段选择解密模式

**系统影响：**
- 读写路径均需修改，但接口契约不变（`Storage.Put` 签名不变）
- 已有对象无缝兼容，无需迁移
- 内存上限从 O(object_size) 降至 ~64KB

---

### 方向 B：统一的租户鉴权与审计框架（P0 — 高优先级）

**为什么需要：**
方向一的发现揭示了 Bearer/JWT/SigV4/匿名读/MCP 五种认证路径的租户处理不一致。这些差异在生产环境中可能导致：

1. 运维人员难以排查租户隔离漏洞
2. SigV4 的静默覆写可能掩盖客户端发错 tenant 头的问题
3. 监控和审计日志中缺少认证方式标签，无法区分"被覆写"和"直接指定"的 tenant

**核心挑战：**
- 五种认证路径的代码分布在 `auth_middleware.go`（~150 行）、S3 SigV4 验证器、MCP 服务器等多个文件
- 需要在不破坏向后兼容性的前提下统一行为
- SigV4 的覆写行为有实际安全价值（防止 tenant 注入），应保留但显式记录

**架构变更：**

```go
// 新增统一类型
type AuthResult struct {
    Authenticated bool
    Method       AuthMethod  // Bearer | SigV4 | JWT | Anonymous | MCP
    Tenant       string      // resolved tenant (after any override)
    OriginalTenant string    // original X-Aero-Tenant from request
    KeyID        string      // resolved key identifier (if applicable)
    Scopes       Scope
}
```

- 中间件链中 Auth 阶段产出 `AuthResult` 存入 context
- Tenant 中间件改为直接从 `AuthResult.Tenant` 读取（而非再次解析 Header）
- AccessLog 中间件记录 `auth_method`、`auth_tenant` 字段
- MCP stdio 模式的 tenant 解析合并到同一框架

**系统影响：**
- auth_middleware.go 重构，但对外接口不变
- 审计日志质量提升，支持按认证方式筛选
- 新增一个 context key，无性能影响

---

### 方向 C：Worker 路径上下文纪律化（P0 — 高优先级）

**为什么需要：**
18 处 `io.Copy` 中，`antivirus/worker.go:113` 和 `reconcile/scrub.go:84` 的后台 worker 调用是**优雅关停的头号风险**。如果 Scrub GC 正在扫描一个大型对象（如 10GB 备份文件），`io.Copy` 可能在 ctx 取消后继续运行数分钟。Antivirus 的 `io.Copy(io.Discard, rc)` 同样无上下文感知。

**核心挑战：**
- `io.Copy` 标准库不感知 `context.Context`——需要替换为 `io.CopyWithContext` 或自定义 wrapper
- Go 1.25（当前版本）仍未提供标准库的 `io.Copy(ctx, ...)` 
- 每个 `io.Copy` 调用位于不同文件、不同抽象层级，需要逐一审计

**架构变更：**

```go
// 内部工具包 internal/xio
func Copy(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
    // 包装 src 为 ctx-aware reader：每次 Read 前检查 ctx.Err()
    return io.Copy(dst, newCtxReader(ctx, src))
}
```

- 替换 18 处 `io.Copy` 为 `xio.Copy(ctx, ...)` 
- Worker 函数的 ctx 已在签名中传递（`ScanObjectByID(ctx context.Context, ...)`），无需额外修改
- 每次 Read 前做非阻塞的 ctx.Done() 检查，开销可忽略

**系统影响：**
- 机械替换，无行为变更
- 关停延迟从"未知（可能数分钟）"降至"最多一个 Read 操作的时间"
- 需要为所有 `io.Copy` 调用（包括 HTTP handler 中的 `io.Copy(w, rc)`）逐行添加 ctx

---

### 方向 D：Multipart 上传持久化与跨实例复原（P1 — 中等优先级）

**为什么需要：**
方向四的发现显示 local 后端的 multipart 上传完全在内存中，服务器崩溃后不可恢复。对生产部署而言：
- 负载均衡多副本时，不同 part 可能路由到不同实例 → UploadPart 返回"unknown upload"
- 大文件分片上传（如 5GB 文件分 50 个 100MB part）的中途崩溃意味着完全重传
- `.multipart/` 下的孤儿文件需要 GC 回收，当前不存在

**核心挑战：**
- 需要将 upload session 元数据持久化到 Repository（jobs 表或专用的 multipart_uploads 表）
- 跨实例协调：哪个实例完成 CompleteMultipart？需要轻量级 leader 选择或乐观锁
- 与 S3 兼容行为的权衡：S3 的多副本 multipart 也有类似"upload 在单个 region 内一致"的限制

**架构变更：**

```go
// 新增 repository 接口
type MultipartSession struct {
    UploadID   string
    Tenant     string
    Bucket     string
    Key        string
    CreatedAt  time.Time
    ExpiresAt  time.Time
    Parts      []PartInfo    // 已知 part 列表
    Options    PutOptions
}

// 方案 A（推荐）：UploadPart 写入 DB + 存储
func (s *LocalStorage) UploadPart(ctx context.Context, ...) {
    // 1. 持久化 part 到磁盘（已有）
    // 2. 记录 part 信息到 Repository（新）
    // 3. 更新 upload session 最后活跃时间（防止过早 GC）
}
```

- 轻量级方案：仅持久化 upload session 元数据 + part 列表到 SQLite，实际 part 文件仍在本地磁盘
- GC 定期清理过期 upload（S3 默认 7 天后自动中止）
- 跨实例：通过 DB 乐观锁实现 CompleteMultipart 的 exactly-once 语义

**系统影响：**
- Repository 新增两张表：`multipart_uploads`、`multipart_parts`
- 性能影响：每次 UploadPart 多一次 DB 写入（基于 local 的 ~1ms 写入）
- 现有 session 无缝兼容（内存映射优先于 DB 查询）

---

### 方向 E：SSE 密钥环热加载与主动续租（P2 — 低优先级）

**为什么需要：**
方向五的发现显示密钥环仅在启动时一次性加载。在需要合规密钥轮换（例如 SOC2、PCI-DSS 要求每 90 天轮换一次）的场景中，重启所有实例进行密钥轮换是不可接受的。

**核心挑战：**
- `SecretProvider.Current()` 返回的 `(id, key)` 当前是启动时确定的固定值
- 切换 primary key 时需要确保：正在写入的对象使用旧 key 还是新 key？
- KMS wrapper (`DataKeyWrapper`) 也需要热加载能力
- 需要原子切换：所有 goroutine 看到一致的 key ring 状态

**架构变更：**

```go
// SecretProvider 扩展
type SecretProvider interface {
    Current() (id string, key []byte)
    Resolve(id string) (key []byte, ok bool)
    // 新增
    Watch() <-chan struct{}  // 通知调用者 key ring 已变更
    Close() error
}
```

- **方案 A（推荐）**：定期轮询（默认为 5 分钟，可配置 `SSE_KEY_POLL_INTERVAL`）+ 原子替换 `sync.RWMutex` 保护的 `keyRingProvider`
- **方案 B**：HTTP long-polling 或 WebSocket（对密钥存储后端有实现要求）
- **方案 C**：SIGHUP 信号触发重新加载（运维友好，但需进程信号编排）

切换时序：
```
1. 新 key 加入 ring（旧对象可解密）
2. primary 指向新 key（新写入使用新 key）
3. 旧 key 保留在 ring 中直至所有引用对象被重写或过期
4. 移除旧 key
```

**系统影响：**
- `SecretProvider` 接口的契约扩展、向后兼容（新增方法有默认实现）
- KMS wrapper 路径同理
- `newHTTPProvider` 改为 goroutine + ticker 模式
- 无需重启，零中断

---

## 3. 接口设计建议

### 3.1 Storage 接口的流式化修订

当前 `Storage` 接口的 `Put` 签名：
```go
Put(ctx, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, error)
```

这个签名本身是流式的，**但 SSE 加密实现破坏了它**。建议：

1. **保持接口签名不变**（向后兼容）
2. **在加密实现层使用 `cipher.StreamWriter`** 封装，将加密与写入组合
3. 添加**契约测试断言**来确保所有存储后端的 Put 实现不将整个对象读入内存

```go
// 新增存储契约测试
func TestStoragePutStreaming(t *testing.T, s Storage) {
    // 写入一个大型随机对象，监控 goroutine stack trace
    // 验证没有 io.ReadAll 或 bytes.Buffer 调用
}
```

### 3.2 SecretProvider 的热加载扩展

```go
type SecretProvider interface {
    Current() (id string, key []byte)
    Resolve(id string) (key []byte, ok bool)
    // 新增可选接口（type-assert at runtime）
}

// 热加载能力标记接口
type WatchableSecretProvider interface {
    SecretProvider
    Watch(ctx context.Context) <-chan struct{}  // 当 key ring 变化时发送信号
    Close() error
}
```

保持 `SecretProvider` 接口最小化（`Current`+`Resolve` 两个方法），通过可选的 `WatchableSecretProvider` 扩展。这样单 key 的 `envProvider` 不需要实现 `Watch()`。

### 3.3 引入 `xio` 内部工具包

```go
// internal/xio/xio.go
package xio

import (
    "context"
    "io"
)

// Copy reads from src into dst, respecting context cancellation.
// Every Read checks ctx.Done() before proceeding.
func Copy(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
    return io.Copy(dst, readerFunc(func(p []byte) (int, error) {
        select {
        case <-ctx.Done():
            return 0, ctx.Err()
        default:
            return src.Read(p)
        }
    }))
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }
```

这个接口足够小，可以直接内联到需要的地方，但作为一个集中包可以减少重复。

### 3.4 引入 AuthResult 统一上下文

```go
// internal/auth/auth_result.go
type AuthResult struct {
    Authenticated  bool
    Method         string   // "bearer" | "sigv4" | "jwt" | "anonymous" | "mcp"
    Tenant         string   // 最终生效的 tenant
    OriginalTenant string   // 请求头中的原始值（用于审计）
    KeyID          string   // 匹配的 key ID（如有）
    Scopes         Scope
    IsOperator     bool     // tenant == "*"
}

// 从 context 中安全读取
func AuthFrom(ctx context.Context) (AuthResult, bool)
```

这个结构体可以：
- 消除 Tenant 中间件重复解析 header 的逻辑
- 为 AccessLog 提供精确的认证元数据
- 为 Admin 审计日志提供统一数据源

---

## 4. 技术选型建议

### 4.1 加密策略选择

| 方案 | 内存效率 | 兼容性 | 实现复杂度 | 推荐度 |
|------|---------|--------|-----------|-------|
| 当前 AES-GCM + io.ReadAll | ❌ O(n) | ✅ 现有 | 低 | ❌ 淘汰 |
| **AES-256-CTR + HMAC-SHA256** | ✅ O(1) | ⚠️ 新 envelope 格式 | 中 | ✅ **推荐** |
| Chacha20-Poly1305 (XChaCha) | ✅ O(1) | ⚠️ 新格式 | 中 | ✅ 同等推荐 |
| AES-GCM-SIV | ✅ O(1) | ⚠️ 新格式 | 高 | ❌ 仅特定需求 |

**推荐选择：AES-256-CTR + HMAC-SHA256**

理由：
- Go 标准库原生支持 `crypto/aes` + `crypto/cipher.NewCTR` + `crypto/hmac`
- Encrypt-then-MAC 方案成熟，认证安全性等价于 GCM
- 支持 `io.Reader`/`io.Writer` 流式接口（`cipher.StreamReader`/`StreamWriter`）
- 无需额外依赖

### 4.2 密钥管理轮询 vs 推送

| 方案 | 实时性 | 实现复杂度 | 对后端要求 | 推荐度 |
|------|-------|-----------|-----------|-------|
| 定期轮询 (5m) | ⚠️ 5min 延迟 | 低 | 无额外要求 | ✅ **推荐** |
| HTTP long-polling | ✅ 近实时 | 中 | 后端支持 Connection: keep-alive | ⚠️ 可选 |
| SIGHUP 信号 | ✅ 即时（运维触发） | 低 | 无 | ⚠️ 作为补充 |
| Vault Agent sidecar | ✅ 近实时 | 高 | 需要 Vault | ❌ 过度工程 |

对于大多数部署，**5 分钟轮询 + SIGHUP 手动刷新** 的组合足够。如需 PCI-DSS 级别的实时密钥轮换，可扩展为 HTTP long-polling。

### 4.3 Go 版本与标准库

当前文档注明 "Go 1.25"。Go 1.21+ 的 `context` 包提供了 `context.WithoutCancel` 等新增功能（Go 1.21 引入），可用于 worker 的 "drain but don't cancel abruptly" 模式。注意：
- 不引入新框架
- 不引入第三方 KV 存储或缓存
- 所有新增功能尽可能使用标准库

### 4.4 评估：是否引入分布式缓存层？

考虑为 SSE 密钥环加载引入缓存层（如 `patrickmn/go-cache` 或 `dgraph-io/badger`）：

建议：**不引入**

理由：
- 密钥环本身很小（< 1KB），每次加密操作都从内存读取，无需外部队存
- 热加载的原子更新使用 `sync.RWMutex` 已是足够高效的读写锁方案
- 不必要的依赖会违反 AGENTS.md 中 "Stdlib 优先" 原则

---

## 5. 实施路线图

### 优先级排序

| ID | 方向 | 优先级 | 风险等级 | 预估工期 | 技术债类型 |
|----|------|--------|---------|---------|-----------|
| A | 流式加密引擎 | **P0** | OOM/数据损坏 | 3-5 天 | 架构债务 |
| B | 统一鉴权框架 | **P0** | 安全/审计 | 2-3 天 | 安全债务 |
| C | 上下文纪律化 | **P0** | 优雅关停 | 2-3 天 | 可靠性债务 |
| D | Multipart 持久化 | **P1** | 数据丢失 | 5-8 天 | 数据持久性债务 |
| E | 密钥环热加载 | **P2** | 运维效率 | 3-5 天 | 运维债务 |

### 阶段划分

#### Phase 1：紧急修复（2-3 天，P0 并行推进）

| 周次 | 任务 | 交付物 | 验证方法 |
|------|------|--------|---------|
| Day 1-2 | 方向 C：xio.Copy 替换 18 处 io.Copy | `internal/xio/xio.go` + 18 处替换 | `go vet` (自定义 linter 检测 io.Copy 非测试调用) + `TestGracefulShutdown` 集成测试 |
| Day 2-3 | 方向 B：AuthResult 统一结构体 | `internal/auth/auth_result.go` + auth_middleware 重构 | 现有测试全部通过，AccessLog 输出 `auth_method` 字段 |
| Day 2-3 | 方向 A：流式加密方案设计 | AeroVault-001 设计文档 | 架构评审 |

**风险点：**
- xio.Copy 替换后某些路径可能不期望 ctx 取消——需要检查所有调用点（例如 HTTP handler 中的 `io.Copy(w, rc)` 在 response writer 写入失败时继续尝试是否有害）
- AuthResult 结构体引入后，所有现有 handler 从 context 取 tenant 的方式不变（TenantFrom 仍然工作），只需逐渐迁移到 AuthFrom

**缓解策略：**
- xio.Copy 默认仅检查 ctx.Err()，不阻断正常流程
- AuthResult 通过 `context.WithValue` 存储，与现有的 tenant context key 共存，逐步迁移

#### Phase 2：核心架构重构（1-2 周，P1 启动）

| 周次 | 任务 | 交付物 | 验证方法 |
|------|------|--------|---------|
| Week 1 | 方向 A：流式加密实现 | 新的 `encryptStream`/`decryptStream` + envelope 格式扩展 | Storage contract test (`TestPutReadStreaming`) + 已有加密对象的向后兼容测试 |
| Week 1 | 方向 A：Multipart 加密流式化 | `mergeEncrypted` 改为流式 | 大文件（>2GB）的 multipart 上传测试 |
| Week 2 | 方向 D：Multipart DB 持久化 | `multipart_uploads` + `multipart_parts` 表 + Repository 接口 | 崩溃恢复测试：模拟 crash 后剩余 upload 可继续或可通过 API 列出 |
| Week 2 | 方向 D：Multipart GC | 定期清理过期 upload（配置 TTL，默认 24h） | 过期 upload 自动清除的集成测试 |

**风险点：**
- 流式加密改变 envelope 格式后，已存在的加密对象需要在读取路径中支持两种解密模式
- Multipart 持久化后，`AbortMultipart` 需要同时清理 DB 记录和磁盘文件——部分失败时的回滚策略

**缓解策略：**
- Envelope 增加 `"enc_version": 1` 字段，读取时根据版本选择解密模式
- AbortMultipart 以 DB 删除为主，磁盘文件异步清理（记录到 orphan_cleanup 队列），最大可接受分钟级延迟

#### Phase 3：运维增强（第 3 周，P2）

| 周次 | 任务 | 交付物 | 验证方法 |
|------|------|--------|---------|
| Week 3 | 方向 E：密钥环轮询加载 | `WatchableSecretProvider` + 后台 ticker | 测试：动态添加/删除 key，验证新写入使用新 primary key，旧对象可解密 |
| Week 3 | 方向 E：SIGHUP 信号支持 | 信号监听 + `SecretProvider.Reload()` | 手动验证 |
| Week 3 | 剩余 tech debt 清理 | 代码 lint 自动化、测试覆盖率提升 | `make check` 全绿 |

**风险点：**
- 密钥环热加载的原子性：如果轮询时读取到不完整的 JSON，不能部分更新密钥环

**缓解策略：**
- 使用 `encoding/json` 的完整解析 + 校验后原子替换（`atomic.Value` 或 `sync.RWMutex`）
- 解析失败的日志应当 WARN 而非 ERROR（不影响当前运行的密钥）

### 里程碑

| 里程碑 | 时间 | 标准 |
|--------|------|------|
| M1: 安全基座 | Day 3 | 18 处 io.Copy 替换完成 + AuthResult 合并 + 审计日志增强；`make check` 全绿 |
| M2: 流式加密 | Week 1 | SSE 加密路径不再使用 `io.ReadAll`；存储契约测试通过；4GB+ 文件上传确认内存稳定 < 128MB |
| M3: Multipart 持久化 | Week 2 | 崩溃恢复测试通过；多副本路由测试通过；Multipart GC 按 TTL 清除 |
| M4: 密钥运维 | Week 3 | 密钥轮换无需重启；SIGHUP 刷新测试通过；文档更新完成 |

### 量化收益预测

| 改进 | 当前状态 | 目标状态 | 度量方式 |
|------|---------|---------|---------|
| 大文件上传内存 | O(object_size) → OOM | ~64KB 固定 | `pprof heap` + `runtime.ReadMemStats` |
| 优雅关停延迟 | 不可预测（分钟级） | < 1秒 | 集成测试关停耗时 |
| 密钥轮换影响 | 需要重启+重装饰 | 零中断热切换 | 轮换期间持续写入的 E2E 测试 |
| 审计日志质量 | 无认证方式信息 | 每条请求记录 auth_method + auth_tenant | AccessLog 行格式断言 |
| OOM 事故预期 | 高频（大文件场景） | 零 | 长时压力测试 |

---

## 总结

AeroVault 的架构基础非常扎实——清晰的层次划分、精良的接口设计、安全默认原则、以及全面的多协议支持都是其工程优势。本文识别的五个扩展方向不是对架构的否定，而是**从生产级可靠性向上提升的必经之路**：将流式加密从 O(n) 内存优化到 O(1)、将优雅关停从不完整到可预测、将密钥管理从冷启动到热加载、将 multipart 从易失性到持久化、将鉴权从五种行为到统一契约。

最优先的三个方向（A、B、C）均为 P0，因为它们直接关系到**系统能否在生产环境下可靠运行**。方向 A 防止 OOM，方向 B 确保安全审计质量，方向 C 保证运维操作的可预测性。三者可并行推进，不存在深层依赖关系。方向 D 和 E 是 P1/P2，分别解决数据持久性和运维效率问题。

建议团队按照 Phase 1（紧急修复）→ Phase 2（核心重构）→ Phase 3（运维增强）的顺序推进，每次修改后均运行 `make check` 确保基线不变。
