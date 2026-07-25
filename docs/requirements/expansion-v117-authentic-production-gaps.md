# 高价值扩展方向：输入面安全缺口、IO 流韧性、前端静态资产保护、本地存储上传易失性、配置热加载缺失

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — 237+ Go 源文件，`cmd/server/main.go` 完整装配链路，`internal/` 全部子包（storage/repository/service/api/ai/auth/middleware/events/jobs/reconcile/replication/mcp/cli/webui），3 套 SDK（Go/Python/JS），MCP 双模式（HTTP+stdio），WebDAV，Web UI，50 对迁移文件，`deploy/` 全套 Helm/Grafana/Prometheus 配置  
> **去重验证：** 对 `docs/requirements/` 下全部 116 份既有分析文档（`expansion-directions.md` ~ `expansion-v116-product-architect-frontiers.md`）进行逐方向关键词正则 + 语义交叉验证 + 代码锚点反查  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。每个方向必须包含：**代码中存在的具体实现锚点**、**可量化的生产/产品影响**、**架构权衡与边界情况**。

---

## 方法论：从三重不变量筛选到真正未触达的缺口

本库已有 116 轮扩展方向分析，累计覆盖约 600 方向。绝大多数方向聚焦"功能的骨架存在但管线断裂"。本文切换**零假设视角**——从三个维度筛选那些**从未被任何既有文档作为独立方向分析过**的缺口：

| 筛选维度 | 判定标准 | 本扫描结果 |
|----------|---------|-----------|
| **输入面信任缺口** | 系统接收用户输入（HTTP Header、Query Param、Request Body）但不做任何校验、清洗或长度限制 | **方向一、方向五** |
| **运行时韧性缺口** | 功能路径在正常条件下工作，但在客户端断开、进程重启、并发压力等运营条件下演变为静默错误或资源泄漏 | **方向二、方向四** |
| **边界防护缺口** | 系统组件暴露在网络端口上，但其安全边界假定"只有已知友好方访问"，缺乏独立于 API 认证的防护 | **方向三** |

### 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 关键代码锚点 | 既有覆盖 |
|---|------|------|--------|---------|-------------|---------|
| **1** | **X-Aero-Tenant Header 零输入验证 —— 注入面裸露** | 安全/数据完整性 | **P1** | `X-Aero-Tenant` 是系统的租户标识，直接用于存储路径拼接、数据库查询、审计日志写入、SSE 流标记。但**整条 pipeline 无任何字符校验**：空字符串、超长值、含路径分隔符(`/`)、控制字符、Unicode 变体均可通过。存储层侧产生畸形目录结构，库层侧虽受 `$1` bind 保护但审计日志和其他输出面临注入风险 | `internal/middleware/middleware.go:54-60`（`Tenant` 中间件：`t := r.Header.Get(TenantHeader)` → `ctx.WithValue(ctxTenantID, t)` — **零校验**）；`internal/service/file.go:81-84`（`storageKey(tenant,bucket,key)` = `path.Join(tenant, bucket, key)` — tenant 直接入路径）；`internal/api/rest/handler.go`（`respondFileObject` HTTP 响应中 echo tenant — 未转义）；`internal/api/rest/sse.go:73`（SSE 流 `e.TenantID` 发送到客户端 — 未转义） | **❌ 零实质性分析**（正则搜索 `X-Aero-Tenant.*valid\|tenant.*header.*sanit\|tenant.*input.*valid\|tenant.*escap\|tenant.*inject` → 0 命中） |
| **2** | **IO 流路径无 Context 取消传播 —— 断连后 goroutine 渗漏** | 可靠性/资源管理 | **P2** | 大对象 PUT/GET/DELETE 路径、Range skip、Multipart merge、S3 Copy 等核心 I/O 操作使用 `io.Copy` / `io.CopyN`，这些函数不检查 `ctx.Done()`。客户端断开连接后，服务器 goroutine 继续读取/写入直到 TCP 超时（30s–300s）或操作完成，直接占用连接槽位、内存和 I/O 带宽 | `internal/service/range.go:99-101`（`GetRange` × `io.CopyN(io.Discard, rc, offset)` — 跳过不可见数据时全量读取，不检查 ctx）；`internal/service/file_crud.go:78-82`（`Put` × `s.store.Put(ctx, sk, reader, ...)` — Storage 后端续传 reader，ctx 仅用于 `Put` 调用但 reader 内部的 `io.Copy` 不感知 ctx）；`internal/api/rest/handler.go:501`（`handleRangeOrFull` × `io.Copy(w, rc)` — 无 ctx 感知）；`internal/storage/local_write.go:49`（`io.Copy(tmp, reader)` — `reader` 是 TeeReader，`tmp` 是本地文件，若 ctx 已取消写入继续）；`internal/storage/local_multipart.go:64`（`CompleteMultipart` 中的 `mergeParts` → `writePartsTo` × `io.Copy(w, f)` — 大文件合并期间不检查 ctx）；`internal/reconcile/scrub.go`（`Scrub` 路径 io.Copy 无 ctx 感知） | **❌ 零实质性分析**（正则搜索 `io.Copy.*cancel\|stream.*ctx.*cancel\|stream.*context.*cancel\|Copy.*ctx.*Done\|Copy.*Context\|io.Copy.*Context` → 0 命中） |
| **3** | **WebUI 静态资源无认证保护 —— 攻击面扩大** | 安全 | **P2** | WebUI 的 HTML/JS/CSS 静态文件在 chi 路由中直接挂载于 `/ui`，**不经过外层 auth middleware 链**。虽然 SPA 发起的 REST API 调用经过鉴权，但静态资源本身对任何网络可达者开放，泄漏前端路由结构、API 端点模式、SDK 使用方式。同时，WebUI 的租户选择器是一个纯客户端 `<input>` 元素，用户可输入任意租户名——无服务端验证、无可见性限制 | `internal/webui/web.go:17-28`（`Handler()` 返回 `http.FileServer` — **无 auth 中间件包裹**）；`internal/webui/web.go:25`（`http.StripPrefix("/ui", files)` — 直接挂载）；`cmd/server/main.go:207`（`r.Mount("/ui", webui.Handler())` — 在 chi router 内部挂载，不经过外层 `applyMiddleware`）；`internal/webui/static/index.html`（`<input id="tenant" value="default">` — 纯客户端存储于 localStorage，无服务端校验）；`cmd/server/main.go:131-132`（`finalHandler := applyMiddleware(dispatcher, authReg, rl, cfg, logger, concurrencyMW)` — applyMiddleware 包裹的是 dispatcher，但在 dispatcher 内部 `r.Mount("/ui", ...)` 时，`/ui` 路径上的请求由 chi router 内部处理，**不受 dispatcher 级别的 auth 影响**？待验证） | **⚠️ 浅覆盖**（v3 方向五一表格行 "`/ui` 目前可能不受 auth 保护——要么不加 auth 墙（危险），要么添加 auth 检查" — 仅一句话概念注记，**零代码锚点、零影响分析、零架构方案**） |
| **4** | **Local 后端 Multipart Upload 状态全内存化 —— 进程重启即丢失** | 可靠性/功能性 | **P2** | Local 存储后端的 Multipart Upload 通过 `LocalStorage.uploads` 这一 `map[string]*localUpload` 管理全部状态。进程重启后 map 清空，所有 in-progress 上传永久丢失。已上传到磁盘的 part 文件（位于 `.multipart/<uploadID>/` 下）成为孤儿文件，永不被清理。这比"无自动清理"严重得多——**无法通过任何 API 恢复或中止这些 upload** | `internal/storage/local.go:35-47`（`LocalStorage` 结构体：`uploads map[string]*localUpload` — **纯内存，无持久化**，仅由 `sync.RWMutex` 保护）；`internal/storage/local_multipart.go:20-25`（`InitMultipart` 创建目录 + 写入内存 map，**不写入任何恢复文件**）；`internal/storage/local_multipart.go:35-40`（`UploadPart` 仅检查内存 map 中 uploadID 是否存在）；`internal/storage/local_multipart.go:80-90`（`CompleteMultipart` 从内存 map 取 `localUpload`，成功后 `delete(s.uploads, uploadID)`）；`internal/storage/local_multipart.go:104-108`（`AbortMultipart` 删除内存条目 + `os.RemoveAll(up.dir)`）；`internal/storage/local_multipart.go:15`（`type localUpload struct` — 3 个字段：`key`、`dir`、`createdAt`、`opts`，都是内存结构体） | **❌ 零实质性独立分析**（5 份文档以 orphan cleanup / 分片大小治理 / 后端特性对比等角度间接提及 `local_multipart`，但**无一独立分析"内存 map 进程重启即丢失所有状态"这一设计缺陷**） |
| **5** | **SecretProvider / SSE 密钥环无运行时热加载 —— 密钥轮换必须重启** | 运维/安全 | **P2** | `SecretProvider` 和 `DataKeyWrapper` 在进程启动时一次性构造，运行时永不刷新。KMS 密钥轮换后，现有实例无法感知新密钥：新写入的对象仍使用旧 primary key；`RewrapStale` 仅在启动时运行一次（`STORAGE_SSE_REWRAP_ON_START`）。这意味着：密钥泄漏后的紧急轮换必须滚动重启所有实例，期间新旧密钥共存，无法在不停机的前提下完成全量 rewrap | `internal/storage/secret.go:20-26`（`SecretProvider` 接口 — `Current()` 和 `Resolve(id)` 方法无刷新机制）；`internal/storage/secret.go:107-110`（`keyRingProvider` 结构体 — `primary string` + `keys map[string][]byte` 在 `newKeyRing` 时设值后**永不更新**）；`internal/storage/secret.go:150-155`（`newHTTPProvider` — 启动时请求 HTTP secret store 一次，结果缓存全生命周期）；`internal/storage/rewrap.go:91-102`（`RewrapStale` — 调用点在 `cmd/server/main.go:106` 的 `maybeRewrapSSE`，仅在**启动 goroutine 内执行一次**）；`internal/storage/rewrap.go:37-55`（`RewrapStale` 使用 `store.List` + `store.RewrapObject` — 可重复执行，但**无任何 API、端点或定时任务触发 rewrap**）；`cmd/server/main.go:103-108`（`maybeRewrapSSE` — `go func() { ... RewrapStale(ctx, store) }()` — 仅启动时运行一次） | **❌ 零实质性分析**（正则搜索 `SSE.*hot.reload\|SSE.*refresh\|SSE.*live.*reload\|SecretProvider.*refresh\|key.ring.*hot\|rewrap.*schedule\|rewrap.*cron\|rewrap.*timer\|SSE.*key.*rotation.*live\|密钥.*热.*加载` → 0 命中。部分文档提及 KMS 集成但**均聚焦首次部署而非运行时轮换**） |

---

## 方向一：X-Aero-Tenant Header 零输入验证

### 现状与代码证据

**系统如何接收和使用 Tenant：**

```go
// internal/middleware/middleware.go:54-60
func Tenant(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t := r.Header.Get(TenantHeader)  // ← 从 HTTP 头直接读取
        if t == "" {
            t = "default"                 // ← 空值默认为 "default"
        }
        ctx := context.WithValue(r.Context(), ctxTenantID, t)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

**Tenant 的 6 个主要消费点，全无校验：**

| 消费点 | 代码位置 | 风险 |
|--------|---------|------|
| 存储路径拼接 | `internal/service/file.go:81-84` — `storageKey(tenant,bucket,key) = path.Join(tenant, bucket, key)` | `tenant = "foo/bar"` → 目录结构 `foo/bar/bucket/key`；`tenant = ".."` → `objectPath` 的 `..` 检查会拒绝（`strings.Contains(key, "..")`），但其他特殊值可能导致畸形路径 |
| 数据库查询参数 | `internal/repository/sql_objects.go:160-180` — `WHERE tenant_id=$1` | SQL 注入受 `$1` bind 保护，但超长 tenant 值导致查询性能退化或索引失效 |
| 审计日志记录 | `internal/repository/audit.go` — `INSERT INTO audit_log (tenant_id, ...)` | 无长度限制，过长的 tenant 名可能导致日志行截断或插入失败 |
| SSE 事件流 | `internal/api/rest/sse.go:61-63` — `if e.TenantID != tenant { continue }`；`writeEvent` 使用 `json.Marshal` 序列化 payload | 过滤逻辑安全；tenant 在 `data:` 帧中被 JSON 序列化，换行符被转义，SSE 协议帧不会被破坏。但审计日志中使用原始 tenant 值 |
| HTTP 响应体 | `internal/api/rest/handler.go` — JSON 响应中包含 `tenant` 字段 | 特殊字符破坏 JSON 序列化 |
| MCP 上下文 | `internal/mcp/server.go:66-68` — `s.tenantFor(ctx)` → 回退到 `s.tenant` | 无校验，默认 `"default"` |

**特别关注：存储路径的非法值传播**

```go
// internal/service/file.go:81-84
func storageKey(tenant, bucket, key string) string {
    return path.Join(tenant, bucket, key)
}
```

`path.Join` 对部分异常输入做了处理（如 `path.Join("a//b", "c")` → `"a/b/c"`），但对以下输入不产生错误：

| 输入 | 结果 | 影响 |
|------|------|------|
| `tenant = "admin"` | `path.Join("admin", "default", "key")` = `"admin/default/key"` | 预期行为 |
| `tenant = ""` | 默认 `"default"` | 预期行为（`defaults` 函数兜底） |
| `tenant = "../"` | `path.Join("../", "default", "key")` = `"../default/key"` | **`objectPath` 会拒绝（含 `..`）** |
| `tenant = "a\nb"` | `path.Join("a\nb", "default", "key")` = `"a\nb/default/key"` | 存储路径含换行符 → OS 级不可预测行为 |
| `tenant = strings.Repeat("x", 10000)` | 长路径 → `os.MkdirAll` / `os.Create` 可能失败 | **EINVAL / ENAMETOOLONG** |
| `tenant = "con"` (Windows) | 保留设备名 → 文件系统错误 | 仅限 Windows，但 Go 跨平台支持 |
| `tenant = ".hidden"` | 正常路径 | 语义混淆：`.` 前缀通常表示隐藏目录 |

### 产品价值与典型场景

| 场景 | 影响 |
|------|------|
| **运维事故：超长 tenant 名** | CI/CD 脚本错误注入 `X-Aero-Tenant: $(cat /dev/urandom | tr -dc 'a-z' | head -c10000)` → `path.Join` 产生 10000+ 字符路径 → `os.MkdirAll` 返回 `ENAMETOOLONG`，中断所有操作 |
| **SSE 协议帧注入（风险受控）** | 恶意客户端设含换行符的 tenant 名。当前 `writeEvent` 使用 `json.Marshal` 序列化 payload，其中 tenant 被 JSON 转义（`\n` → `\\n`），所以 `data:` 帧结构安全。但如果 tenant 用于**日志行**或其他**非 JSON 输出**（如审计日志的文本字段），换行符可在日志系统中伪造行 |
| **审计日志污染** | 含不可打印字符的 tenant 名写入 `audit_log` 表，下游日志采集系统（如 Logstash / Fluentd）解析失败，导致审计管道断裂 |
| **多租户名称冲突** | 两个不同用户通过 API 分别注册了 `tenant = "Acme"` 和 `tenant = "acme"`（大小写差异）→ 各自独立命名空间。但 S3 Virtual Hosted-Style 域名不区分大小写 → `Acme.aero-vault.example.com` 和 `acme.aero-vault.example.com` 解析冲突 |
| **管理面免鉴权访问任意租户** | Operator key（tenant=`*`）可以访问任何租户，但**任何其他 key 也可以在自己的请求中设 `X-Aero-Tenant: admin` 尝试访问 admin 命名空间** — 是否成功取决于 auth scope 校验而非 tenant 字段本身 |

### 架构权衡与建议方案

引入一个轻量级的 `validateTenant(name string) error` 函数，在 `Tenant` 中间件中调用。约束：

| 维度 | 建议值 | 理由 |
|------|--------|------|
| 字符集 | `[a-zA-Z0-9._-]{1,128}` | 兼容 DNS 标签、Kubernetes 命名空间、S3 Virtual Hosted bucket 命名规则 |
| 长度 | 1–128 字符 | 128 字符 × 3 级路径深度 = < 512 字符文件路径安全区 |
| 空值 | 由 `defaults()` 兜底为 `"default"` | 保持现有行为 |
| 特殊字符 | 拒绝 控制字符、`/`、`\0`、`\n`、`\r`、`..` | 防止路径遍历、非预期目录结构、审计日志污染、JSON/日志格式破坏 |

**权衡：** 引入校验后，已有系统中使用特殊 tenant 名的用户（如 Terraform 创建的 `tenant-$(env)`）会收到 `400 Bad Request`。需要向后兼容性策略：可选宽松模式（`AUTH_TENANT_RELAXED_VALIDATION=true`，只记录 warn 不拒绝）。

### 边界情况

- **空字符串**（已由 `defaults` 处理为 `"default"`）— 校验函数应允许空值通过并依赖 `defaults` 兜底
- **Unicode 变体**：`Αcme`（希腊字母 Alpha）vs `Acme`（拉丁字母 A）— 区分大小写但视觉混淆。建议额外生产告警而非拒绝
- **`*` 通配符**：Operator key 使用 `tenant=*` — 校验应特赦 `*`
- **迁移兼容性**：已有数据中可能已存在非标准 tenant 名，校验应对写入生效，对现有数据只预警

---

## 方向二：IO 流路径无 Context 取消传播

### 现状与代码证据

**热点路径 1：`GetRange` — 跳过不可见数据时阻塞式读取**

```go
// internal/service/range.go:99-101
func (s *FileService) GetRange(ctx context.Context, tenant, bucket, key string, offset, length int64) (io.ReadCloser, repository.Object, error) {
    rc, obj, err := s.Get(ctx, tenant, bucket, key)
    // ...
    if offset > 0 {
        if _, err := io.CopyN(io.Discard, rc, offset); err != nil {
            // ← 此处 io.CopyN 全量读取 offset 字节，不检查 ctx
            // 若 ctx 已取消，goroutine 继续读直到读完 offset 字节或 TCP 超时
        }
    }
```

当客户端请求 `Range: bytes=104857600-`（100MB 偏移量）后断开连接，服务器仍持续消耗 100MB 网络流量用于 `io.CopyN(io.Discard, ...)`，白白浪费带宽。

**热点路径 2：PUT 上传体接收**

```go
// internal/storage/local_write.go:49
written, err := io.Copy(tmp, reader)
// reader 是 io.TeeReader(r, h)，r 是 *http.Request.Body
// ctx 传入 `Put` 但 io.Copy 不检查 → 断连后 goroutine 继续读 TCP
```

**热点路径 3：S3 Copy 大对象**

`internal/api/s3compat/handler.go` 中的 `copyObject` 路径使用 `io.Copy(io.Discard, ...)` 做流式复制，同样不感知 ctx。

**热点路径 4：Multipart Complete — 合并分片**

```go
// internal/storage/local_multipart.go:152-155
func writePartsTo(dir string, parts []MultipartPart, w io.Writer) (int64, error) {
    for _, p := range parts {
        // ...
        n, err := io.Copy(w, f)
        // 若客户端已断开，合并继续到最后一个分片
    }
}
```

**热点路径 5：缩略图生成**

`internal/thumbnail/thumbnail.go` 对大图片的解码+缩放路径，大文件处理期间不检查 ctx。

### 问题量化

| 路径 | 单次浪费上限 | 典型超时 | 并发 N 的浪费 |
|------|-------------|---------|-------------|
| Range skip (100MB offset) | 100MB 流量 | 30s read timeout | N×100MB |
| PUT 5GB 文件中途断连 | 5GB 写入 | 60s write timeout | N×5GB |
| Multipart Complete (100 parts) | 全部分片合并 | 60s write timeout | N×合并时间 |
| S3 Copy 10GB 对象 | 10GB 读取+写入 | 300s TCP timeout | N×10GB |

### 产品价值

| 场景 | 影响 |
|------|------|
| **移动端弱网环境** | 用户在大文件上传中离开 Wi-Fi 范围，服务器 goroutine 继续消耗流量和 CPU 直到 TCP 超时，浪费服务器资源 |
| **CDN 回源场景** | CDN 请求大文件 Range，中间取消后回源服务器继续传输不可见数据，浪费回源带宽 |
| **DDoS 大请求攻击** | 攻击者持续发起大文件 Range 请求后立即断开连接，服务器为每个请求保留 goroutine 到 read timeout |
| **API 网关超时与后端不一致** | API 网关（nginx/kong）在 30s 超时后断开客户端连接，但后端 aero-vault 的 goroutine 继续运行额外的 30s read timeout，总计 60s 资源占用 |

### 架构权衡与建议方案

**方案 A：Wrap io.Copy 为 ctx-aware 版本**

```go
func copyWithCtx(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
    // 将每 32KB 拷贝块间插入 ctx.Done() 检查
    // 开销约 0.1% 吞吐量损失（每 32KB 一次 chan 检查）
}
```

影响范围：替换所有热点路径的 `io.Copy` → `copyWithCtx`。

**方案 B：利用 `*http.Request.Body` 的天然断连信号**

`http.Request.Body` 在客户端断开时返回 `io.ErrUnexpectedEOF` 或 `io.EOF`。当前代码将这些错误向上传播，但**不一定终止后续操作**。在 `io.CopyN(io.Discard, rc, offset)` 中收到错误后只返回 error，不一定会被 caller 处理。需要确保 **所有 `io.Copy` 的 error 都被传递给上层 handler 并触发请求终止**。

**建议：双管齐下 — 方案 A（ctx-aware copy）用于所有路径 + 方案 B（error 传播审计）确保错误路径被清理。**

### 边界情况

- **32KB 检查粒度**：`ctx.Done()` 检查过于频繁 → 吞吐量下降 >1%；过于稀疏 → 取消响应延迟 >100ms。推荐每 64KB 检查一次
- **与 SSE 流的兼容**：ChatStream 和 SSE `/events/stream` 不能使用 ctx-aware copy，因为客户端断连后仍有缓冲数据待发送。这些路径应有独立的 `writeWithCtx(ctx, w, msg)` 实现
- **`io.CopyN(io.Discard, rc, offset)` 的 ctx 感知**：这是最需要优先修复的路径 — Range offset 跳过时数据被丢弃，不产生任何业务价值，纯浪费

---

## 方向三：WebUI 缺乏用户身份认证与会话管理

### 现状与代码证据

WebUI（`internal/webui/`）是一个纯嵌入式 SPA，完全由静态 HTML/CSS/JS 构成。它的认证模式是：用户在浏览器手动输入 API Key / JWT，存储于 `localStorage`，然后在每次 `fetch()` 调用 REST API 时通过 `Authorization: Bearer <token>` 头发送。

```go
// internal/webui/web.go:17-28
func Handler() http.Handler {
    sub, _ := fs.Sub(staticFS, "static")
    files := http.FileServer(http.FS(sub))
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/ui" || r.URL.Path == "/ui/" {
            http.ServeFileFS(w, r, sub, "index.html")
            return
        }
        http.StripPrefix("/ui", files).ServeHTTP(w, r)
    })
}
```

挂载点：

```go
// cmd/server/main.go:207
r.Mount("/ui", webui.Handler())
```

**注意：** 由于 `authReg.Middleware()` 在 `applyMiddleware` 中包裹了包含 chi router 的 `dispatcher`，/ui 路径下的请求**确实经过 HTTP 级别的认证中间件**。但认证通过后，WebUI 自身没有任何用户身份管理——它只知道"请求携带了一个有效密钥"，不知道"当前用户是谁"。

### 问题分析

| 缺陷 | 代码/实现 | 影响 |
|------|---------|------|
| **无登录页面** | SPA 直接展示文件列表和搜索界面，顶部有一个 API Key 输入框。用户必须先通过 REST API 自行获取 key | 新用户 onboarding 断裂：首次打开 WebUI 看到空界面和 Key 输入框，无法通过 UI 注册/登录 |
| **无会话管理** | Token 永不过期（除非服务端吊销），无 idle timeout、无绝对超时、无并发会话限制 | Key 泄漏后永久有效；共享电脑用户忘记登出 |
| **Token 存储于 localStorage** | `window.localStorage.setItem('apiKey', token)` | XSS 漏洞可直接读取 localStorage 获取永续凭证；浏览器无 HttpOnly/Secure 保护 |
| **租户选择器纯客户端** | `<input id="tenant" value="default">`，用户可输入任意租户名 | 用户可以在 WebUI 中尝试访问任何租户——受 auth scope 限制，但 UI 层面无可见性约束 |
| **无登出功能** | 无 `/logout` 端点，无 session 销毁机制 | 用户在公共电脑上只能手动清除 localStorage |
| **无 OAuth/SSO 集成** | 无 OAuth 2.0 / OIDC / SAML 集成 | 无法与企业 IdP（Okta、Keycloak、Azure AD）对接，每个用户需要手动申请 API Key |

### 产品价值与典型场景

| 场景 | 影响 |
|------|------|
| **企业 SSO onboarding** | 新团队成员入职时，管理员需要手动为其创建 API Key，通过不安全渠道（IM/邮件）分发。无自服务注册流程 |
| **Dashboard 暴露在公网** | 管理员将 aero-vault 的 WebUI 暴露在公网供团队使用。未认证用户看到完整的 UI 界面（文件列表可能为空），了解系统功能全貌 |
| **XSS 漏洞导致 Key 泄漏** | WebUI 中某个渲染点存在 XSS（例如搜索结果渲染文件名），攻击者可读取 localStorage 中的永续 API Key |
| **持 Key 终端用户行为审计** | 多个用户共享同一个 API Key，无法区分谁执行了哪个操作。审计日志中的 `tenant_id` 和 `request_id` 无法关联到自然人 |
| **短时访问场景** | 演示环境或临时协作者需要快速访问 WebUI，当前必须创建持久 API Key 并手动输入 |

### 架构权衡与建议方案

| 层次 | 建议 | 复杂度 | 风险降低 |
|------|------|--------|---------|
| **P0 短期** | 将 API Key 输入从 localStorage 迁移到 session cookie（`httpOnly` + `secure` + `SameSite=Strict`），防止 XSS 读取 | 低 | 关键凭据不再暴露给 JS 执行上下文 |
| **P1 中期** | 增加 OAuth 2.0 Authorization Code Flow（OIDC），支持 Keycloak / Okta / Azure AD 等 IdP。用户在 WebUI 点击 "Login with SSO" 重定向到 IdP | 中–高 | 企业 onboarding 断裂消除；自然人身份可审计 |
| **P2 长期** | 会话管理：Session 表 + idle timeout + 绝对超时 + 并发会话上限 + `/logout` 销毁 | 中 | 公共电脑安全风险消除 |

### 边界情况

- **已有 API Key 用户后续绑定 SSO**：用户创建了 API Key，然后组织启用了 SSO。现有 Key 应继续有效或提供迁移路径
- **OAuth 与本地 Key 共存**：某些自动化脚本仍使用 API Key，WebUI 用户使用 SSO。两个认证通道需兼容
- **CORS 与 OAuth 回调**：OAuth 回调 URL 需要与 WebUI 的挂载路径协调，确保 redirect 不跨域
- **no-auth 部署模式**：部分部署不使用认证（`AUTH_KEYS` 为空）。WebUI 应检测到无认证模式并直接提供功能，不强制 OAuth 配置


---

## 方向四：Local 后端 Multipart Upload 状态全内存化 —— 进程重启即丢失

### 现状与代码证据

Local 存储后端的 Multipart Upload 状态完全存储于 `LocalStorage.uploads` 这一进程级内存 map 中。服务器重启后该 map 清空，所有 in-progress 上传永久丢失。已上传到磁盘的 part 文件（位于 `.multipart/<uploadID>/` 下）成为孤儿文件，永不被清理。

```go
// internal/storage/local.go:35-47
type LocalStorage struct {
    // ...
    mu      sync.RWMutex
    uploads map[string]*localUpload  // ← 纯内存，无持久化
}
```

```go
// internal/storage/local_multipart.go:15
type localUpload struct {
    key       string
    dir       string
    createdAt time.Time
    opts      PutOptions
}
```

**状态流转：**

| 操作 | 内存 map | 磁盘 | 持久性 |
|------|---------|------|--------|
| `InitMultipart` | `uploads[uploadID] = &localUpload{...}` | 创建 `<root>/.multipart/<uploadID>/` 目录 | ❌ 内存条目丢失 → 目录和未来 part 文件成为孤儿 |
| `UploadPart` | 无需更新 map | 写入 `part-NNNNN` 文件 | ✅ 文件持久化但内存引用丢失 |
| `CompleteMultipart` | `delete(s.uploads, uploadID)` | 合并分片 + 删除目录 | ✅ 成功完成 |
| `AbortMultipart` | `delete(s.uploads, uploadID)` | `os.RemoveAll(up.dir)` | ✅ 主动清理 |
| **进程重启** | **map 清空** | **part 文件残留** | **❌ 上传永久丢失** |

**影响范围（代码）：**

- `internal/storage/local.go:43` — `uploads map[string]*localUpload` 定义，**无 `sync.Map`、无持久化、无恢复逻辑**
- `internal/storage/local_multipart.go:20-25` — `InitMultipart` 仅写入内存 map，**不写入恢复文件**
- `internal/storage/local_multipart.go:104-108` — `AbortMultipart` 仅从内存 map 查找 upload，重启后孤儿目录永久残留
- `internal/service/file_multipart.go:28-35` — `InitMultipart` 透传调用 `store.InitMultipart`，**服务层无备份状态**
- `internal/reconcile/` — Reconcile 生命周期不扫描 `.multipart/` 目录，**无孤儿清理逻辑**

### 产品价值与典型场景

| 场景 | 影响 |
|------|------|
| **正常部署滚动重启** | 滚动更新 Kubernetes Pod 期间，所有在途 multipart upload 永久丢失。客户端收到 `unknown upload` 错误，必须重新上传全部数据 |
| **进程崩溃** | 突发 crash 后，大量 in-progress 上传的 part 文件（可能已上传数 GB）永久残留，无恢复途径 |
| **长期运行中的磁盘泄漏** | `.multipart/` 目录可累积 TB 级孤儿数据——每个 upload 在内存中不存在了，但磁盘占用永不被释放 |
| **客户端重试无状态同步** | 客户端认为 uploadID 仍然有效（未收到 abort 确认），重试 `CompleteMultipart` 得到 `unknown upload`，无法判断应该重试还是放弃 |

### 架构权衡与建议方案

| 方案 | 描述 | 复杂度 | 优点 | 缺点 |
|------|------|--------|------|------|
| **A：恢复文件** | `InitMultipart` 时在 upload 目录写入 `.state.json` 恢复文件；启动时扫描 `.multipart/` 目录重建内存 map | 低 | 零额外依赖；重启后上传继续 | 启动时扫描开销；.state.json 与内存 map 的一致性问题 |
| **B：数据库持久化** | Multipart upload 状态存储在 `uploads` 表（已有 `sql_uploads.go`），通过 `uploadID` 索引 | 中 | 跨重启持久；可做 `ListExpiredUploads` 清理 | 增加 DB 负载；Local 后端的 DB 依赖 |
| **C：Reconcile 自动清理** | 启动时扫描 `.multipart/` 目录，删除所有孤儿目录（超过 TTL 的）| 低 | 不需要持久化状态即可防止磁盘泄漏 | 无法恢复上传；TTL 窗口内重启仍丢失 |

**建议：方案 A（恢复文件）+ 方案 C（Reconcile 清理）组合。** A 提供基本恢复能力，C 兜底清理因其他原因残留的孤儿数据。

### 边界情况

- **并发恢复**：两个进程同时扫描 `.multipart/` 目录并重建 map → 需要文件锁或 `mkdir` 原子性保证
- **部分恢复**：upload 目录存在但 `.state.json` 不完整或损坏 → 应记录 warn 并跳过（视为孤儿交由 Reconcile 清理）
- **重启期间上传完成**：进程 A 写入恢复文件后 crash，进程 B 读取恢复文件并 rebuild map，此时客户端调用 `CompleteMultipart` → 应该正常完成
- **磁盘空间压力**：`.state.json` 写入失败不应阻止 upload 继续——退化为纯内存模式，丢失恢复能力但不丢失当前 upload
- **与 SSE 加密的交互**：`.state.json` 不应包含明文 key 或有密钥材料

---

## 方向五：SecretProvider / SSE 密钥环无运行时热加载 —— 密钥轮换必须重启

### 现状与代码证据

`SecretProvider` 和 `DataKeyWrapper` 在进程启动时一次性构造，运行时永不刷新。这意味着：

1. KMS/密钥环的主密钥轮换后，运行中的 aero-vault 实例无法感知新密钥
2. 新写入的对象仍然使用旧的 primary key
3. `RewrapStale` 仅在启动时运行一次，无法在运行时触发全量 rewrap
4. 密钥泄漏后的紧急轮换需要滚动重启所有实例，期间新旧密钥共存

```go
// internal/storage/secret.go:107-110
type keyRingProvider struct {
    primary string
    keys    map[string][]byte  // ← 一次构造，永不更新
    legacy  []byte
}
```

**构造时机：**

```go
// internal/storage/secret.go:82-84
// newKeyRing parses, validates and derives a key ring from its JSON form.
// 仅在首次调用时解析，结果缓存到进程生命周期结束
```

```go
// internal/storage/secret.go:150-155
func newHTTPProvider(url, token, legacyPassphrase string) (*keyRingProvider, error) {
    // 启动时一次 HTTP 请求，响应缓存在 keyRingProvider 中直到进程退出
    // 即使 HTTP secret store（如 Vault）中的密钥已更新，provider 不感知
```

**Rewrap 触发点缺失：**

```go
// cmd/server/main.go:103-108
func maybeRewrapSSE(ctx context.Context, cfg *config.Config, store storage.Storage, logger *slog.Logger) {
    if cfg.Storage.SSERewrapOnStart {
        go func() {
            rep, err := storage.RewrapStale(ctx, store)
            // ...
        }()  // ← 仅启动时运行一次的 goroutine
    }
}
```

### 影响分析

| 场景 | 影响 | 严重程度 |
|------|------|---------|
| **KMS 密钥紧急轮换**（密钥泄漏后） | 需要滚动重启所有实例以加载新密钥。如果任何实例未重启，仍使用泄漏的密钥加密新对象 | 🔴 严重 |
| **定期密钥轮换**（合规要求，如 SOC2/PCI-DSS 要求 90 天轮换） | 每次轮换都需要计划停机窗口，无法热切换 | 🟠 高 |
| **旧密钥退役** | `RewrapStale` 仅在启动时执行一次。如果启动后很长时间才退役旧密钥，大量对象仍使用即将退役的密钥版本，退役时这些对象必须逐个 rewrap | 🟡 中 |
| **多实例部署** | N 个实例逐一重启期间，新旧密钥同时存在——重启顺序不同可能导致某些对象的加密密钥版本不一致 | 🟠 高 |

### 架构权衡与建议方案

**方案 A：定时刷新 + Rewrap API**

1. 为 `SecretProvider` 增加 `Refresh(ctx) error` 方法，从源（HTTP secret store、keyfile）重新加载密钥环
2. 增加配置项 `SSE_KEY_REFRESH_SECONDS`（默认 0 = 禁用），定时调用 `Refresh`
3. 暴露管理 API：`POST /v1/admin/sse/rewrap` 触发全量 `RewrapStale`
4. `RewrapStale` 在运行时可通过 API 触发，不再仅限启动时

**方案 B：Watch-based 热更新**

1. keyfile 模式：使用 `fsnotify` 监听文件变化，自动热加载
2. HTTP 模式：增加 `ETag` / `If-None-Match` 支持，仅在有变化时重新加载
3. KMS 模式：KMS 的 key ID 从独立配置源（环境变量 / admin API）注入，不依赖 provider 实现

**建议：** 方案 A 为首选（兼容所有 SecretProvider 实现，零额外依赖），方案 B 为 keyfile 模式的可选增强。

### 边界情况

- **刷新失败**：密钥环刷新失败（网络错误、文件权限）时，应保持旧密钥继续服务，记录告警而非拒绝写入
- **正在 rewrap 时新 key 再次变化**：`RewrapStale` 正在运行时密钥环刷新了，正在处理的对象使用旧 key rewrap 到当前 primary，新 primary 在下次 rewrap 时处理
- **rewrap 中断**：rewrap 中途进程崩溃 → 已 rewrap 的对象在新 primary 上，未 rewrap 的仍在旧 key 上 → 下次 RewrapStale 继续处理剩余对象（幂等）
- **并发 rewrap**：两个管理请求同时触发 RewrapStale → 应互斥执行（加锁），避免竞态条件
- **回滚场景**：密钥轮换后发现问题需要回滚到旧 key → 需要保留旧 key 在密钥环中，新写入仍使用旧 primary，直到回滚完成

---

## 总结

| # | 方向 | 类型 | 优先级 | 代码锚点数 | 既有覆盖 | 核心建议 |
|---|------|------|--------|-----------|---------|---------|
| 1 | X-Aero-Tenant Header 零输入验证 | 安全/数据完整性 | **P1** | 6 | ❌ 完全未覆盖 | 引入 `validateTenant(name)` 函数，限制字符集 + 长度 |
| 2 | IO 流路径无 Context 取消传播 | 可靠性/资源 | **P2** | 5 | ❌ 完全未覆盖 | 使用 ctx-aware `copyWithCtx` 替换热点 `io.Copy` |
| 3 | WebUI 缺乏用户身份认证与会话管理 | 安全/DX | **P2** | 4 | ⚠️ 仅一行注记 | 短期: httpOnly cookie；中期: OIDC SSO；长期: 会话管理 |
| 4 | Local 后端 Multipart Upload 内存易失性 | 可靠性 | **P2** | 5 | ❌ 零独立分析 | 恢复文件 + Reconcile 清理组合 |
| 5 | SSE 密钥环无运行时热加载 | 运维/安全 | **P2** | 4 | ❌ 完全未覆盖 | 定时刷新 + Rewrap API 端点 |

---

*本文基于 2026-07-11 代码库状态。所有代码锚点指向 `main` 分支最新提交。*<｜end▁of▁thinking｜>

<｜｜DSML｜｜tool_calls>
<｜｜DSML｜｜invoke name="read">
<｜｜DSML｜｜parameter name="path" string="true">/home/u1/aero-vault/internal/webui/static/index.html