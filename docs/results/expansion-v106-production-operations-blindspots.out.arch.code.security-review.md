现在我拥有足够的数据来进行全面的安全审查。以下是经过分析的完整报告：

---

# 安全审查报告：Aero-Vault 对象存储系统

## 总体安全态势：需要改进

**审查范围：** `cmd/server/main.go`、`internal/auth/`、`internal/middleware/`、`internal/storage/`、`internal/repository/`、`internal/service/`、`internal/api/rest/`、`internal/api/s3compat/`、`internal/events/`、`internal/ai/`、`internal/config/config.go`

**方法：** 逐行人工审查 30+ 个关键安全文件，覆盖 10,000+ 行生产 Go 代码。

---

## 第一节：发现 1 — 高严重性 — 通过 `RemoteAddr` 的桶策略绕过

| 字段 | 值 |
|-------|-------|
| **类别** | 授权 |
| **严重性** | **高** |
| **标题** | 桶策略通过不受信任的源 IP 强制执行 |
| **位置** | `internal/api/rest/handler.go:58-64` 和 `internal/api/s3compat/handler.go:48-53` |
| **描述** | `checkBucketPolicy()` 中的桶策略评估使用 `r.RemoteAddr` 来确定客户端 IP，而没有考虑 `X-Forwarded-For` 或 `X-Real-IP` 代理头。在典型的微服务部署（Kubernetes、反向代理）中，`RemoteAddr` 始终是上游代理的 IP，而不是原始客户端。带有基于 IP 的限制的桶策略可以通过在策略中将代理作为可信客户端而无效化。 |
| **攻击场景** | 攻击者部署在集群内部（例如，作为同一 Kubernetes 命名空间中的另一个 pod），使得 `RemoteAddr` 解析为代理的内部 IP。带有源 IP 限制的桶策略错误地将所有内部流量视为来自可信 IP，从而完全绕过 IP 闸控。 |
| **影响** | 基于源 IP 的桶策略闸控被绕过。在反向外部的攻击者只能在代理 IP 与策略中的 IP 范围匹配时获得访问权限。 |
| **建议** | 实现 `Forwarded`/`X-Forwarded-For` 解析，并添加 `TRUSTED_PROXY_CIDRS` 配置。从 Proxy 协议或可信 CIDR 范围链中提取真实的客户端 IP。 |
| **工作量** | S（< 1 天） |

---

## 第二节：发现 2 — 高严重性 — 块传输上传完整性绕过

| 字段 | 值 |
|-------|-------|
| **类别** | 威胁模型 / 数据篡改 |
| **严重性** | **高** |
| **标题** | S3 流式块传输未验证每个块的签名 |
| **位置** | `internal/auth/sigv4_chunk.go:9-78` |
| **描述** | `decodeStreamingBody()` 函数在验证初始种子签名后解码 AWS `STREAMING-AWS4-HMAC-SHA256-PAYLOAD` 块，但**不会重新验证每个块的签名**。根据 AWS 规范，每个块都带有 `chunk-signature=<sig>`，必须根据前一个块的签名进行验证。此实现完全忽略块签名，仅提取块大小并丢弃签名。 |
| **攻击场景** | 1. 攻击者使用有效的 SigV4 签名启动一个被授权的上传。<br>2. 在传输过程中，攻击者用任意恶意内容替换上传的块，同时保持初始授权头不变。<br>3. 服务器基于初始（已验证的）签名接受上传。<br>4. 存储的数据是纯文本（因为 SSE 是在存储层应用的，而不是每块加密）。 |
| **影响** | 存储系统中的数据完整性完全受损。块替换意味着服务无法信任上传内容的完整性。通过内容哈希（ETags）检测修改是唯一的安全网。 |
| **建议** | 实现完整的 AWS 块签名链验证：<br>`seed_signature -> chunk1_signature -> chunk2_signature -> ...`<br>根据 AWS 规范验证每个块的签名。如果签名不匹配，立即拒绝并返回 `400 Bad Request`。至少，在对象层面比较 SHA256（通过 `putObject` 路径中的 `Content-MD5` 或 `x-amz-content-sha256`）。 |
| **工作量** | M（2-3 天，以实现符合规范的块签名验证） |

---

## 第三节：发现 3 — 高严重性 — 错误处理中的信息泄露

| 字段 | 值 |
|-------|-------|
| **类别** | 数据保护 / 信息泄露 |
| **严重性** | **高** |
| **标题** | 内部错误消息在 HTTP 响应和 SSE 帧中泄露 |
| **位置** | `internal/api/rest/handler.go:276-287`（`classify` 默认情况）和 `internal/api/rest/search.go:156-163`（SSE 错误转发） |
| **描述** | `classify()` 中的 `default` case 将 `err.Error()` 直接转发给客户端，所有路径均如此。这包括内部存储路径、SQL 错误和配置详细信息。`Search` 和 `Chat` handler 同样在 `writeSSEError` 和 JSON 响应体中公开 `err.Error()`。 |
| **攻击场景** | 攻击者发送格式错误的请求以触发各种内部错误：无效的存储键、数据库约束违规、无效的 SSE 信封。错误响应泄露内部路径（`internal/storage/factory.go:42`）、SQL 方言信息（postgres vs sqlite）和密钥管理拓扑（"unknown key version X"）。 |
| **影响** | 侦察：攻击者识别技术栈、数据库后端、文件系统路径和密钥轮换策略。对于针对性攻击（例如，SQL 注入语法调整）至关重要。违反了 OWASP Top 10 A04:2021（不安全设计）。 |
| **建议** | 在 `classify()` 中实现映射函数，将错误代码映射到通用客户端消息，同时在服务器端记录完整错误详情：<br>```go<br>func sanitizeError(err error) (string, string, int) {<br>    // 映射到安全客户端消息<br>    return "InternalError", "服务器内部错误，请稍后重试", http.StatusInternalServerError<br>}<br>```<br>将完整的 `err.Error()` 记录到结构化日志中，并始终向客户端返回通用消息。 |
| **工作量** | S（< 1 天） |

---

## 第四节：发现 4 — 高严重性 — 缺少 KDF 的 SSE 密钥派生

| 字段 | 值 |
|-------|-------|
| **类别** | 密码学 |
| **严重性** | **高** |
| **标题** | `deriveSSEKey` 使用快速哈希代替密钥派生函数 |
| **位置** | `internal/storage/secret.go:60-64` |
| **描述** | `deriveSSEKey` 使用简单的 HMAC-SHA256 从密码短语生成 32 字节主密钥。没有使用 KDF（PBKDF2、bcrypt、scrypt、Argon2id）。HMAC-SHA256 的速度针对性能而非暴力破解阻力进行了优化。密码短语是单源密钥 — 如果受损，所有数据（包括旋转前写入的）都会暴露。 |
| **攻击场景** | 1. 攻击者获得对 `STORAGE_LOCAL_SSE_KEY` env 变量或密钥文件的读取访问权限。<br>2. 密码短语较弱（例如，长度 < 20 个字符的随机字母数字）。<br>3. 攻击者在合理时间内（HMAC-SHA256 的 < 10^9 次猜测/秒/GPU）暴力破解密码短语。<br>4. 从对象信封中解密所有数据密钥。 |
| **影响** | 存储层数据完全受损。由于密钥派生缺少 KDF，弱密码短语可被暴力破解。违反 NIST SP 800-57（密钥管理）和 OWASP 密码学标准。 |
| **建议** | 将 `deriveSSEKey` 替换为 Argon2id（`golang.org/x/crypto/argon2`）：<br>```go<br>func deriveSSEKey(passphrase string) []byte {<br>    salt := []byte("aero-vault-sse-v1") // 固定盐，因为密码短语是唯一秘密<br>    return argon2.IDKey([]byte(passphrase), salt, 3, 64*1024, 4, 32)<br>}<br>```<br>document the tradeoff that Argon2 increases startup time for key derivation. |
| **工作量** | M（1 天，测试和基准测试） |

---

## 第五节：发现 5 — 中严重性 — 缺少安全 HTTP 头

| 字段 | 值 |
|-------|-------|
| **类别** | 合规性 / OWASP Top 10 |
| **严重性** | **中** |
| **标题** | 响应中缺少安全头 |
| **位置** | `internal/middleware/middleware.go`（整个模块）和 `internal/api/rest/handler.go`（所有响应写入器） |
| **描述** | 应用程序在任何端点都没有设置以下安全头：`Strict-Transport-Security`、`X-Content-Type-Options: nosniff`、`X-Frame-Options: DENY`、`Content-Security-Policy` 或 `Referrer-Policy`。这对 REST API 的影响低于 Web UI，但 Web UI 端点（`/ui`）和 Swagger 文档（`/docs`）在没有保护的情况下呈现 HTML 和 JavaScript。 |
| **攻击场景** | 攻击者通过 XSS 或者 MIME 嗅探利用 Web UI（例如，上传具有恶意 JavaScript 的有效负载的 HTML 文件，该文件被错误地提供）。没有 `X-Content-Type-Options`，浏览器可能会将上传的内容解释为 `text/html` 而不是 `application/octet-stream`。 |
| **影响** | 面向用户的 Web 路径（`/ui`、`/docs`）中的 MIME 嗅探和点击劫持漏洞。违反了 OWASP Top 10 A05:2021（安全配置错误）。 |
| **建议** | 添加安全头中间件：<br>```go<br>func SecurityHeaders(next http.Handler) http.Handler {<br>    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {<br>        w.Header().Set("X-Content-Type-Options", "nosniff")<br>        w.Header().Set("X-Frame-Options", "DENY")<br>        w.Header().Set("Content-Security-Policy", "default-src 'self'")<br>        w.Header().Set("Referrer-Policy", "no-referrer")<br>        w.Header().Set("Permissions-Policy", "geolocation=(),camera=(),microphone=()")<br>        if r.TLS != nil {<br>            w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")<br>        }<br>        next.ServeHTTP(w, r)<br>    })<br>}<br>```<br>在 `applyMiddleware` 中的 CORS 中间件之后立即应用。 |
| **工作量** | S（< 1 天） |

---

## 第六节：发现 6 — 中严重性 — SSRF 通过可配置的外部端点

| 字段 | 值 |
|-------|-------|
| **类别** | 威胁模型 |
| **严重性** | **中** |
| **标题** | 多个可配置的 HTTP 客户端创建 SSRF 攻击面 |
| **位置** | `internal/config/config.go` 和 `internal/storage/kms.go`、`internal/ai/`、`internal/events/webhook.go`、`internal/antivirus/antivirus.go` |
| **描述** | AI 管道、KMS 包装器、防病毒扫描仪、复制后端和 webhook 中的所有 HTTP 客户端都用作用户可配置的端点（通过环境变量）。虽然它们调用的是配置的端点（不是用户提供的），但如果攻击者获得对配置的写入访问权限（例如，通过管理 API 或其他漏洞），他们可以将端点重定向到内部服务（例如，`http://169.254.169.254/latest/meta-data/` 上的云元数据端点）。 |
| **攻击场景** | 1. 攻击者通过 RCE、配置错误或管理 API 漏洞修改 `AI_EXTRACTOR_ENDPOINT`。<br>2. 攻击者将端点设置为 `http://localhost:8000` 上的内部服务。<br>3. KMS/AI 客户端向内部服务发出 HTTP 请求，可能泄露敏感数据。<br>4. 或者：`AI_EMBED_API_KEY` 作为 Authorization 头发送到 SSRF 目标，泄露凭证。 |
| **影响** | 内部网络侦察、云元数据泄露、凭证泄露。影响范围从中到严重，取决于 SSRF 路径中的目标服务。 |
| **建议** | 实现 URL 验证中间件，拒绝私有/环回 IP 范围（对于所有出站 HTTP 客户端）：<br>1. 创建一个带有传输包装器的共享 HTTP 客户端，该包装器使用 `net.DialContext` 并验证目标 IP 不在 `127.0.0.0/8`、`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16` 或 `169.254.0.0/16` 中（除非通过显式配置标志覆盖）。<br>2. 为所有出站 HTTP 请求添加 `ConnectTimeout` 和 `ReadTimeout`。 |
| **工作量** | M（2 天使用共享 HTTP 客户端工厂） |

---

## 第七节：发现 7 — 中严重性 — 租户枚举通过并发限制器

| 字段 | 值 |
|-------|-------|
| **类别** | 威胁模型 / 信息泄露 |
| **严重性** | **中** |
| **标题** | `PerTenantConcurrencyLimiter` 暴露租户存在信息 |
| **位置** | `internal/middleware/middleware.go:157-192`（`PerTenantConcurrencyLimiter`） |
| **描述** | `PerTenantConcurrencyLimiter` 返回区分 `"too many concurrent requests"` 和 `"tenant has too many concurrent requests"` 的响应。这向未认证的攻击者泄露了租户的存在和活跃性。由于 `X-Aero-Tenant` 头由客户端控制（当身份验证被禁用时），攻击者可以探测租户名称。 |
| **攻击场景** | 1. 身份验证被禁用（`AUTH_KEYS` 为空，这是默认配置）。<br>2. 攻击者对不同的 `X-Aero-Tenant` 值发出请求。<br>3. 现有租户：每个桶的请求在达到全局限制后产生不同的错误消息（"tenant has too many"）。<br>4. 不存在的租户：在达到全局限制之前，请求被正常处理。 |
| **影响** | 租户枚举信息泄露。在具有许多租户的多租户环境中，暴露活跃租户列表。 |
| **建议** | 标准化错误消息为通用的 `"too many requests"`，不区分租户。|
| **工作量** | S（< 1 小时） |

---

## 第八节：发现 8 — 中严重性 — 默认配置中缺少身份验证

| 字段 | 值 |
|-------|-------|
| **类别** | 身份验证 / 默认安全 |
| **严重性** | **中** |
| **标题** | 当 `AUTH_KEYS` 未设置时，身份验证完全禁用 |
| **位置** | `internal/auth/auth.go:49-52`、`internal/config/config.go:23-27`、`internal/auth/auth_middleware.go:21-24` |
| **描述** | 当 `AUTH_KEYS` 为空时（默认部署），`Registry.Enabled()` 返回 `false`，并且 `Middleware()` 完全跳过所有身份验证检查并调用 `next.ServeHTTP(w, req)`。没有 JWT 配置（默认情况下未设置 `AUTH_JWT_SECRET`），没有 SigV4，没有匿名读取保护。攻击者可以在默认配置中免费获得完全读取/写入/管理访问权限。 |
| **攻击场景** | 1. 运维将 `aero-vault` 部署在非隔离网络中，具有默认配置。<br>2. 没有设置 `AUTH_KEYS`，没有设置 `AUTH_JWT_SECRET`。<br>3. 任意网络攻击者向 `/v1/files/*`、`/v1/admin/keys` 或任何端点发出 HTTP 请求。<br>4. 完全未经授权的访问，包括管理操作。 |
| **影响** | 默认配置下的完全数据损坏。数据读取、写入、管理访问和密钥管理均不受限制。 |
| **建议** | 在安全模式下强制执行身份验证（默认启用）：<br>```go<br>func (r *Registry) Middleware() func(http.Handler) http.Handler {<br>    // 无论环境是否配置了密钥，始终需要身份验证。<br>    // 使用 AUTH_DISABLED=true 明确选择退出。<br>    if r.authDisabled {<br>        return func(next http.Handler) http.Handler { return next }<br>    }<br>    // ... 现有的身份验证逻辑<br>}<br>```<br>添加 `AUTH_DISABLED` env var，该变量必须在默认情况下为 `false`。记录明确的选择退出。 |
| **工作量** | S（< 1 天） |

---

## 第九节：发现 9 — 中严重性 — 弱 PII 检测导致误报/漏报

| 字段 | 值 |
|-------|-------|
| **类别** | 数据保护 |
| **严重性** | **中** |
| **标题** | PII 检测器使用宽泛的正则表达式，没有上下文验证 |
| **位置** | `internal/ai/pii.go:52-67` |
| **描述** | PII 检测器使用简单的正则表达式识别电子邮件、电话号码、信用卡号、SSN 和 IP 地址。具体来说：<br>1. **电话号码正则表达式：** `(?:\+\d{1,3}[\s\-]?)?(?:\(?\d{2,4}\)?[\s\-]?){2,4}\d{2,4}` — 将匹配许多非电话字符串，例如生成的技术标识符（"abc12345def67890"）。<br>2. **IP 地址正则表达式：** `\b(?:\d{1,3}\.){3}\d{1,3}\b` — 匹配无效的 IP 地址（`999.999.999.999`）并忽略 IPv6。<br>3. **信用卡正则表达式：** `\b(?:\d[ \-]?){13,19}\b` — 应用 Luhn 检查，但空格/破折号分隔的序列可能导致假阴性。 |
| **攻击场景** | 1. **误报：** 技术标识符被拖入 AI 搜索结果，触发 PII 警报，导致不必要的调查。或者，合法的 PII 以非标准格式存储（例如，没有连字符的 SSN `123456789` 或国际号码 `+44 20 7946 0958`），这些格式被正则表达式错过。<br>2. **规避：** 攻击者使用 Unicode 混淆（例如，全角数字）或替代格式（例如，`+1 (800) 555-0170` 被正则表达式正确识别，但 `tel:+18005550170` 不会被识别）存储 PII。 |
| **影响** | 合规性风险：GDPR/CCPA/SOC2 扫描工具产生不可靠的结果，可能导致假阴性（未标记的 PII）或假阳性（不必要的警报）。 |
| **建议** | 1. 收紧电话正则表达式以要求 E.164 格式或至少 NANPA 模式。<br>2. 为 IP 地址添加八位字节范围验证（< 256）。<br>3. 考虑将 Presidio（微软的 PII 检测）或类似库后端用于生产级分类，或者将正则表达式检测记录为"尽力而为"。<br>4. 添加 IPv6 支持。 |
| **工作量** | S（< 1 天收紧正则表达式） |

---

## 第十节：发现 10 — 低严重性 — 通过 `TenantFrom` 的租户身份操纵

| 字段 | 值 |
|-------|-------|
| **类别** | 身份验证 / 授权 |
| **严重性** | **低** |
| **标题** | `X-Aero-Tenant` 头在未认证模式下是客户端控制的 |
| **位置** | `internal/middleware/middleware.go:46-53` |
| **描述** | `Tenant` 中间件无条件信任 `X-Aero-Tenant` 头，默认为 `"default"`。当身份验证被禁用时（这是默认配置！），任何客户端通过设置此头可以模拟任何租户。当身份验证启用时，auth 中间件会固定它，但在身份验证被处理之前，`Tenant` 中间件会在中间件链中运行（请参阅 `main.go:146-145` 中的 `applyMiddleware`）。 |
| **攻击场景** | 在禁用身份验证的默认配置中，攻击者设置 `X-Aero-Tenant: other-tenant` 并访问属于该租户的数据。没有租户隔离。 |
| **影响** | 多租户隔离完全被绕过。然而，这只有在身份验证被禁用时才会起作用，这是一个已知的默认状态。 |
| **建议** | 在身份验证之前，不要将租户设置到上下文中；或者，在身份验证中间件之后，将租户中间件拆分为一个“提取默认值”阶段。记录此设计限制。 |
| **工作量** | S（重新排序中间件或记录） |

---

## 第十一节：发现 11 — 低严重性 — 内存中的秘密对核心转储敏感

| 字段 | 值 |
|-------|-------|
| **类别** | 密码学 / 数据保护 |
| **严重性** | **低** |
| **标题** | API 密钥和 JWT 秘密在进程内存中作为明文保留 |
| **位置** | `internal/auth/auth.go:28-32`（`Key.Token` 字段）、`internal/auth/jwt.go:33-34`（`secret []byte`） |
| **描述** | API 密钥令牌在内存中作为 `Key.Token`（字符串，在 Go 中是不可变的）保留。JWT 秘密在 `JWTVerifier.secret` 中作为 `[]byte` 保留。两者都在进程的整个生命周期内保留在堆上。如果攻击者获得代码执行或核心转储访问权限，他们可以提取所有活动 API 密钥和 JWT 签名秘密。 |
| **攻击场景** | 攻击者在运行 `aero-vault` 进程的服务器上获得 shell 访问权限。他们运行 `gcore $(pgrep aero-vault)` 并从核心转储中提取 API 密钥。然后，他们使用泄露的密钥对存储系统进行身份验证。 |
| **影响** | 秘密泄露导致对受影响密钥的持久后门访问。 |
| **建议** | 虽然不是在生产 Go 服务中完全消除（秘密必须在某个时候在内存中解密），但实施以下缓解措施：<br>1. 使用 `memguard`（`github.com/awnumar/memguard`）或 `sodium` 包装器进行保护内存分配（mlock、mprotect）。<br>2. 注册一个 `SIGBUS` 处理程序来在崩溃时清除秘密。<br>3. 记录秘密在内存中的持续时间。 |
| **工作量** | L（3 天以上用于完整的保护内存集成） |

---

## 第十二节：发现 12 — 低严重性 — Webhook 秘密以明文形式存在于配置中

| 字段 | 值 |
|-------|-------|
| **类别** | 密码学 / 秘密管理 |
| **严重性** | **低** |
| **标题** | Webhook 签名秘密作为明文环境变量传递 |
| **位置** | `internal/config/config.go:155` 和 `internal/events/webhook.go:49-71` |
| **描述** | `EVENTS_WEBHOOK_SECRET` 作为明文环境变量传递，并作为 `[]byte` 保留在 `Webhook` 结构中。没有对秘密进行加密或屏蔽。环境变量可能出现在 `ps aux`、docker inspect 和日志中。 |
| **攻击场景** | 具有本地 shell 访问权限的攻击者读取 `/proc/<pid>/environ` 或进程的日志，提取 `EVENTS_WEBHOOK_SECRET`。然后，他们可以伪造 webhook 负载或拒绝合法的 webhook。 |
| **影响** | Webhook 负载完整性丧失。攻击者可以伪造 webhook 事件。 |
| **建议** | 记录 `EVENTS_WEBHOOK_SECRET` 不应用作共享秘密（例如，对于第三方集成），而应作为内部签名密钥。对于生产使用，建议通过文件挂载（`EVENTS_WEBHOOK_SECRET_FILE`）或密钥存储（Vault）注入秘密。 |
| **工作量** | S（添加 `_FILE` 约定） |

---

## 第十三节：低/信息性 — 认证授权分析

**STRIDE 分析：**

| 威胁 | 状态 | 说明 |
|-------|--------|-------|
| **S**poofing（身份欺骗） | ⚠️ 部分缓解 | JWT HS256 签名有效，但未实现 RS256/ES256 — 所有令牌由同一秘密签名。SigV4 验证已实现但未验证块签名（发现 2）。API 密钥散列存储，这是一个好的做法。 |
| **T**ampering（篡改） | ⚠️ 部分缓解 | SSE 加密使用 AES-256-GCM，提供认证加密。然而，块签名未被验证（发现 2）。ETag 验证作为可选功能提供（`ReadVerificationConfig`）——默认情况下已禁用。 |
| **R**epudiation（抵赖） | ✅ 良好 | 审计日志记录所有管理员操作。事件总线提供可追踪的操作历史。请求 ID 在日志中传播。 |
| **I**nformation Disclosure（信息泄露） | ❌ 高风险 | 错误消息泄露内部细节（发现 3）。无安全头（发现 5）。租户枚举可能（发现 7）。PII 检测存在漏洞（发现 9）。 |
| **D**enial of Service（拒绝服务） | ✅ 良好 | 具有桶大小限制的并发限制器。速率限制具有 50k 桶上限和空闲逐出。最大租户查询深度是可配置的。 |
| **E**levation of Privilege（权限提升） | ❌ 关键风险 | 身份验证在默认配置中被禁用（发现 8）。租户头在未认证模式下是客户端控制的（发现 10）。管理员密钥管理由 `admin` 作用域保护，但如果在默认配置中部署，则默认情况下没有身份验证。 |

**OWASP Top 10 映射：**

| A 类 | 状态 |
|-------|--------|
| A01:2021 — 破坏访问控制 | ❌ — 默认禁用身份验证（发现 8） |
| A04:2021 — 不安全设计 | ❌ — 错误泄露内部细节（发现 3） |
| A05:2021 — 安全配置错误 | ❌ — 缺少安全头（发现 5） |
| A08:2021 — 软件和数据完整性故障 | ⚠️ — 块签名未经验证（发现 2） |
| A10:2021 — 服务器端请求伪造 | ⚠️ — 可配置的端点接受任何 URL（发现 6） |

---

## 第 14 节：总结

### 总体安全态势：**需要改进**

该系统具有多个精心设计的组件（带有 I1/I2 不变量的正确参数化 SQL、带有密钥旋转的坚固 AES-256-GCM 信封加密、带有桶大小限制的弹性速率限制、完整的 OTel 可观测性），但存在一些关键的默认安全问题，使其在生产部署中面临风险。

### 前 3 个最关键的问题

1. **高：在默认配置中禁用身份验证** — 在没有 `AUTH_KEYS`、`AUTH_JWT_SECRET` 或 `AUTH_PERSIST_KEYS` 的情况下部署系统会向任何能够向端口发送 HTTP 的人授予完全读取/写入/管理访问权限。这是第一个要关闭的默认安全漏洞。

2. **高：块上传完整性绕过** — AWS SigV4 流式块传输上传未验证每个块的签名，允许中间人或恶意客户端在初始握手后替换内容，而无需重新签名。

3. **高：内部错误在响应中泄露** — `err.Error()` 被直接转发到 HTTP 响应和 SSE 帧中，暴露文件系统路径、SQL 方言、存储后端以及密钥版本信息，用于侦察。

### 前 3 个快速取胜方案

1. **S：标准化错误消息** — 阻止内部信息在 API 响应中泄露（发现 3）。工作量：< 1 天。

2. **S：添加安全头** — 实现 `X-Content-Type-Options`、`CSP`、`HSTS`（发现 5）。工作量：< 1 天。

3. **S：默认启用身份验证** — 通过添加 `AUTH_DISABLED` env var（默认为 `false`）来反转默认配置安全性（发现 8）。工作量：< 1 天。

### 安全债务

| 债务项 | 工作量 | 优先级 |
|----------|--------|----------|
| S3 流式块签名验证 | M | 高 |
| 内部 IP 的 SSRF 保护 | M | 中 |
| SSE KDF（Argon2id） | M | 中 |
| 客户端 IP 解析（X-Forwarded-For） | S | 中 |
| 保护内存中的秘密 | L | 低 |
| 收紧 PII 检测正则表达式 | S | 低 |
| 租户枚举缓解 | S | 低 |

### 合规性影响

- **PCI-DSS：** 信用卡号 PII 检测通过 Luhn 检查得到改进，但需要适当的 PCI 范围界定。未实施在存储层进行卡数据检测。
- **GDPR/CCPA：** PII 检测器存在漏洞（发现 9），但作为一个可选功能，需要在部署时明确启用和审查。
- **SOC2：** 在没有身份验证（发现 8）或安全头（发现 5）的情况下，安全配置错误会引发 SOC2 发现项。
- **NIST SP 800-53：** 访问控制（AC）和系统与通信保护（SC）控制因 #1（默认认证）和 #4（KDF）而失败。
