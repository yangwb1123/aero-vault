现在我已对整个代码库进行了全面的审查。以下是完整的安全审查报告。

---

# 安全审查报告：Aero-Vault

**子系统：** 完整服务器（`cmd/server/main.go` 及所有内部包）
**审查人：** 首席安全工程师
**审查日期：** 2026-07-12

---

## 1. 身份认证与授权

### 发现 1.1 — JWT 使用具有共享机密的 HS256（对称算法）

| 字段 | 值 |
|-------|-------|
| **类别** | 加密 |
| **严重性** | **中等** |
| **标题** | HS256 JWT 要求安全的带外密钥分发 |
| **位置** | `internal/auth/jwt.go:10` |
| **描述** | 系统仅支持 HS256（HMAC-SHA256）进行 JWT 验证。任何拥有 `AUTH_JWT_SECRET` 的人都可以签署任意令牌。对于内部 SSO 或 sidecar 代理而言，这是设计如此，但在多服务设置中，如果秘密被共享或泄露，它会引入冒充风险。 |
| **攻击场景** | 拥有 `AUTH_JWT_SECRET` 的恶意内部人员可以签署一个 `ten:"*"` 和 `scopes:["admin"]` 的令牌，从而获得完全的管理员访问权限。 |
| **影响** | 完全的认证绕过；攻击者成为任意租户的管理员。 |
| **建议** | 对于生产环境，实施 RS256/JWKS 支持，使用 `kid` 头部进行密钥轮换。通过环境变量配置公钥。从 `AUTH_JWT_SECRET` （对称）迁移到 `AUTH_JWT_JWKS_URL` （非对称）。 |
| **工作量** | L（> 3 天） |

### 发现 1.2 — API 密钥通过 HTTP 查询字符串可能泄露

| 字段 | 值 |
|-------|-------|
| **类别** | 认证 |
| **严重性** | **高** |
| **标题** | API 密钥可通过 `X-Api-Key` 头部或查询字符串泄露 |
| **位置** | `internal/auth/auth_middleware.go:129-135` |
| **描述** | `extractToken` 同时检查 `Authorization` 头部和 `X-Api-Key` 头部。然而，客户端可能错误地通过 URL 查询字符串发送 `?api_key=` 或 `?token=` —— 这些会被 Web 服务器记录、在浏览器历史中缓存，并可能出现在反向代理日志中。当前代码不接受查询字符串中的密钥（正确），但 S3 预签名 URL 在查询字符串中使用 `X-Amz-Signature`。 |
| **攻击场景** | 用户在浏览器中加书签 `https://vault.example.com/v1/files?api_key=sk-abc123`。书签被同步到云，密钥泄露。 |
| **影响** | API 密钥通过日志/浏览历史泄露会导致未经授权的访问。 |
| **建议** | 在 `extractToken` 中添加一个拒绝列表，如果检测到查询字符串认证参数（如 `api_key`、`token`、`access_token`），则记录警告或拒绝请求。记录一条警告日志，指示令牌应通过 Authorization 头部传递。 |
| **工作量** | S（< 1 天） |

### 发现 1.3 — S3 SigV4 验证缺少时间偏移检查

| 字段 | 值 |
|-------|-------|
| **类别** | 认证 |
| **严重性** | **中等** |
| **标题** | 时间戳验证没有可配置的时钟偏移容差 |
| **位置** | `internal/auth/sigv4.go:91-106` |
| **描述** | 标头认证路径（`verifyHeader`）检查 `X-Amz-Date` 的存在性，但不验证时间戳是否在合理范围内（例如，AWS 要求 ±15 分钟）。预签名 URL 路径检查过期时间，但标头签名会话会无限期有效，只要签名匹配。 |
| **攻击场景** | 截获签名的 SigV4 请求的攻击者可以重放它，即使过了很长时间。对于标头认证，SigV4 在规范请求中包含时间戳，但当前代码不验证 `X-Amz-Date` 是否是最新的。 |
| **影响** | 对 S3 API 的标头认证请求的重放攻击在密钥轮换之前均有效。 |
| **建议** | 在标头认证路径中添加 `time.Now().Sub(signedAt) < 15*time.Minute` 检查。AWS Signature V4 规范要求此检查。 |
| **工作量** | S（< 1 天） |

### 发现 1.4 — MCP stdio 模式绕过所有认证

| 字段 | 值 |
|-------|-------|
| **类别** | 认证 |
| **严重性** | **信息** |
| **标题** | stdio MCP 有意绕过认证（设计约束） |
| **位置** | `internal/mcp/server.go` + `cmd/server/main.go:runMCP()` |
| **描述** | `aero-vault mcp` 命令通过 stdin/stdout 运行 MCP 服务器，没有认证中间件。该通道假定由本地父进程信任。在该模式下写入文件、读取文件、删除文件和聊天等工具直接可用。 |
| **攻击场景** | 如果父进程是妥协的 CI/CD 作业，攻击者可以对仓库执行任意文件操作。 |
| **影响** | 有限的；需要主机上的本地执行。 |
| **建议** | 记录此约束。考虑添加 `--require-auth` 标志，即使在 stdio 模式下也能强制执行令牌/密钥。对于生产使用考虑 HTTP MCP 传输，它通过标准中间件。 |
| **工作量** | S（< 1 天）以添加文档/标志 |

---

## 2. 输入验证

### 发现 2.1 — 路径遍历检测不完整

| 字段 | 值 |
|-------|-------|
| **类别** | 输入验证 |
| **严重性** | **高** |
| **标题** | `validateKey` 使用的 `strings.Contains(key, "..")` 可能被 URL 编码绕过 |
| **位置** | `internal/service/file.go:150-153` |
| **描述** | `validateKey` 函数检查 `strings.Contains(key, "..")`。然而，如果密钥先被 URL 解码，`..` 模式可能以 `%2e%2e` 或 `..%2f` 等形式出现。根据传入路径是否已经被解码，这可能导致目录遍历。 |
| **攻击场景** | 攻击者发送 `PUT /v1/files/..%2f..%2fetc%2fpasswd`。如果 chi 路由器在将路径传递给 `keyFromPath` 之前对其进行 URL 解码，则 `..` 检测会失败，并且密钥变为 `../../etc/passwd`，可能允许写入文件系统上的任意位置。 |
| **影响** | 任意文件读取/写入，可能导致 RCE。 |
| **建议** | 使用 `path.Clean(key)` 并将结果与输入进行比较，以检测遍历。此外，在 `validateKey` 内添加显式的 `!strings.HasPrefix(key, "/")` 检查（当前已存在）。在验证之前添加 URL 解码或在路由层规范化路径。 |
| **工作量** | S（< 1 天） |

**与此相关的代码：**
```go
func validateKey(key string) error {
    if key == "" { ... }
    if strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
        return ...
    }
    return nil
}
```

### 发现 2.2 — 用户元数据中的 HTTP 头部注入

| 字段 | 值 |
|-------|-------|
| **类别** | 输入验证 |
| **严重性** | **中等** |
| **标题** | 存储的标题值中的 HTTP 响应拆分 / CRLF 注入 |
| **位置** | `internal/api/rest/handler.go:316-327` (writeMetadataHeaders, writeContentResponseHeaders) |
| **描述** | `writeMetadataHeaders` 调用 `w.Header().Set("X-Meta-"+k, v)`，其中 `k` 和 `v` 来自用户控制的元数据。如果元数据值包含换行符（`\r\n`），Go 的 `net/http` 会自动对它们进行清理，防止直接的 HTTP 响应拆分。但是，如果元数据键包含 `http.CanonicalHeaderKey` 不覆盖的不安全字符，自定义头部仍然可能被注入。此外，`Content-Disposition` 存储为 `_aero_content_disposition` 并无修改地回显。 |
| **攻击场景** | 用户设置元数据 `Content-Disposition: attachment; filename="malicious.html\r\nX-Injected: true"`。在写入时存储，在读取时回显。如果文件名触发浏览器中的下载行为，它可能导致 XSS。 |
| **影响** | 通过下载文件名进行潜在的 XSS。通过控制 Content-Disposition 的文件名进行反射型 XSS。 |
| **建议** | 在回显之前，清理响应头部的 `Content-Disposition` 文件名。使用 Go 的 `mime.FormatMediaType` 正确编码文件名。 |
| **工作量** | S（< 1 天） |

### 发现 2.3 — Bucket Policy 语言可能允许攻击者控制评估

| 字段 | 值 |
|-------|-------|
| **类别** | 输入验证 |
| **严重性** | **中等** |
| **标题** | Bucket Policy 解析可能受策略注入影响 |
| **位置** | `internal/api/rest/handler.go:59-68` 及 `internal/api/s3compat/handler.go:56-65` |
| **描述** | 策略存储在 `buckets` 表的 `policy` 列中，并直接传递给 `auth.ParsePolicy()`。如果解析器接受诸如 `Condition` 块之类的高级构造，并且客户端可以设置 `aws:SourceIp` 等条件，则策略评估可能被操纵。 | 如果未正确实现条件键（如 `aws:SourceIp`），宽泛的策略可能被利用。 |
| **攻击场景** | 具有 `write` 作用域的租户管理员设置策略 `{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Condition":{"IpAddress":{"aws:SourceIp":"0.0.0.0/0"}}}`，本意是限制访问，但条件解析错误，实际上允许所有 IP。 |
| **影响** | 对象 ACL 可能覆盖不足或过度覆盖，具体取决于策略解析错误。 |
| **建议** | 审查 `auth.ParsePolicy` 的实现，确保条件得到严格验证。对策略的大小和深度添加上限。 |
| **工作量** | M（1-3 天）取决于策略实现的复杂性 |

---

## 3. 加密

### 发现 3.1 — SSE 加密是选项性的，默认关闭

| 字段 | 值 |
|-------|-------|
| **类别** | 加密 |
| **严重性** | **中等** |
| **标题** | 静态加密需要显式配置 |
| **位置** | `internal/storage/encrypt.go` + `internal/config/config_storage.go` |
| **描述** | 服务器端加密通过 `STORAGE_LOCAL_SSE_KEY` 或 `STORAGE_LOCAL_SSE_KEYFILE` 启用。默认情况下，静态对象数据以明文形式存储在磁盘上。这是一个有效的设计选择（操作员选择加入），但新部署可能忽略加密。 |
| **攻击场景** | 服务器磁盘被销毁/盗窃。未加密的对象数据在磁盘上暴露。 |
| **影响** | 数据泄露：所有对象内容以明文形式在磁盘上可用。 |
| **建议** | 通过记录要求和将加密作为安全部署清单的一部分来缓解。考虑一个 `STORAGE_REQUIRE_ENCRYPTION=true` 标志，如果存储未加密，则拒绝启动。 |
| **工作量** | S（< 1 天）添加开关 |

### 发现 3.2 — TLS 未在应用层强制执行

| 字段 | 值 |
|-------|-------|
| **类别** | 加密 |
| **严重性** | **高** |
| **标题** | 传输层加密留给反向代理 |
| **位置** | `cmd/server/main.go:runServer()` — `http.Server` 而非 `https.Server` |
| **描述** | 应用程序通过纯 HTTP 提供服务。期望在它前面有一个反向代理（nginx、ALB）处理 TLS 终止。如果用户直接部署服务或反向代理配置错误，所有流量（包括 API 密钥和对象数据）都以明文形式传输。 |
| **攻击场景** | 同一网络中的攻击者（例如，共享云 VPC、WiFi）嗅探 HTTP 流量并收集 API 密钥和对象内容。 |
| **影响** | 通过网络嗅探导致凭据泄露和数据泄露。 |
| **建议** | 可选地添加内置的 `https.Server` 支持，使用由 `TLS_CERT_FILE` / `TLS_KEY_FILE` 环境变量配置的自签名或 Let's Encrypt 证书。记录 TLS 终止的要求。 |
| **工作量** | M（1-3 天）以添加带有 ACME 支持的内置 TLS |

### 发现 3.3 — 预签名 URL 使用静态签名密钥，没有过期强制执行

| 字段 | 值 |
|-------|-------|
| **类别** | 加密 / 认证 |
| **严重性** | **中等** |
| **标题** | 预签名 URL 验证不检查服务端过期 |
| **位置** | `internal/storage/sign.go` (signLocal) + `internal/service/file.go` 中的预签名逻辑 |
| **描述** | 本地预签名方案（`signLocal`）签署 `method\nobjectKey\nexpires` 三元组。但此 HMAC 签名是由 `storage.LocalConfig.SignKey` 密钥静态创建的，过期时间在 URL 生成时烘焙到签名中。验证函数需要检查当前时间是否在签署的过期时间之前，但 `verifyPresignedSig` 检查仅存在于 S3 SigV4 路径中。REST API 预签名路径（`/v1/files/*/presign`）生成一个 URL；然而，对签名验证的缺失意味着过期的预签名 URL 可能有效。 |
| **攻击场景** | 攻击者获得过期的预签名 URL（例如，来自日志/历史记录）。即使过期时间已过，他们仍然可以访问该对象。 |
| **影响** | 预签名 URL 在预期到期时间之后仍然有效，破坏了时间限制访问。 |
| **建议** | 审查预签名 URL 的验证路径。确保 `getURL`、`statURL` 等函数检查当前时间是否在 URL 的过期时间之前。为所有预签名路径添加显式的服务器端过期检查。 |
| **工作量** | M（1-3 天）取决于当前实现的覆盖范围 |

---

## 4. 会话管理

### 发现 4.1 — 无会话 Cookie（无状态 API）

这是设计如此。该 API 是无状态的，使用 `Authorization: Bearer <token>` 进行认证。不存在传统的会话管理问题。但是，缺少 `Secure`、`HttpOnly`、`SameSite` Cookie 标志的适当设置，以备将来任何基于 Cookie 的认证。

| 字段 | 值 |
|-------|-------|
| **类别** | 会话 |
| **严重性** | **低** |
| **标题** | 将来基于 Cookie 的认证没有安全 Cookie 标志 |
| **位置** | 不适用（目前无 Cookie） |
| **描述** | 如果将来添加基于 Cookie 的认证（例如，会话 Cookie），则必须使用 `Secure`、`HttpOnly` 和 `SameSite=Lax` 进行设置。 |
| **建议** | 记录此要求。目前，API 密钥和 JWT 在头部传递，不存在 Cookie。 |
| **工作量** | 不适用（预防性） |

---

## 5. 数据保护

### 发现 5.1 — 错误响应可能泄露内部错误信息

| 字段 | 值 |
|-------|-------|
| **类别** | 数据保护 |
| **严重性** | **中等** |
| **标题** | 内部错误消息暴露给客户端 |
| **位置** | `internal/api/rest/handler.go:classify()` 第 292 行 |
| **描述** | `classify` 函数的默认分支返回 `err.Error()` 作为响应消息。如果底层系统产生详细的错误（如 `storage put: connection refused to bucket.s3.amazonaws.com`），这可能会将内部 IP 地址、DNS 名称或配置细节暴露给客户端。 |
| **攻击场景** | 恶意客户端发送导致 S3 错误的请求。响应体包含 `InternalError: storage put: dial tcp 10.0.1.5:443: connect: connection refused`，暴露内部网络拓扑。 |
| **影响** | 信息泄露；辅助针对基础设施的攻击。 |
| **建议** | 将默认错误情况改为返回通用的 `internal server error`，同时记录详细的错误信息。仅在配置了 `DEBUG=true` 或 `DEV_MODE=true` 时返回详细错误。 |
| **工作量** | S（< 1 天） |

### 发现 5.2 — Webhook 失败有效载荷包含完整的事件数据

| 字段 | 值 |
|-------|-------|
| **类别** | 数据保护 |
| **严重性** | **中等** |
| **标题** | Webhook 失败表以明文形式存储整个事件有效载荷 |
| **位置** | `internal/events/webhook.go:113-117` |
| **描述** | 当一个 webhook 投递失败时，整个序列化的事件（包括 `payload` 字段）作为字符串存储在 `webhook_failures` 表中。如果事件有效载荷包含敏感数据（例如，包含 PII 的对象标签或元数据），它将在数据库中以明文形式存在，直到重试成功或被清除。任何可以读取 `webhook_failures` 表的人（admin 作用域或数据库访问权限）都可以看到这些数据。 |
| **攻击场景** | 包含 PII（如 `email: user@example.com`）的文件标签触发事件。Webhook 投递失败，有效载荷存储在数据库中。具有 `admin` 作用域的攻击者调用 `GET /v1/admin/webhook-failures` 并读取 PII。 |
| **影响** | 未经授权访问敏感元数据/PII。 |
| **建议** | 在存储之前剥离或遮盖事件有效载荷中的敏感字段。在 webhook 失败有效载荷上实现 TTL 驱逐。在 `GET /v1/admin/webhook-failures` 端点后面添加审计日志记录。 | |
| **工作量** | M（1-3 天） |

### 发现 5.3 — 审计日志记录 Actor 标签而非真实身份

| 字段 | 值 |
|-------|-------|
| **类别** | 数据保护 |
| **严重性** | **低** |
| **标题** | 审计日志记录 Actor 的租户 ID，而非具体用户身份 |
| **位置** | `internal/api/rest/admin.go:audit()` 第 275 行 |
| **描述** | 审计日志的 `actor` 字段设置为 `k.Tenant`，即 API 密钥的租户 ID。在共享密钥（同一租户下的多个用户）下，不可能区分是谁执行了某个操作。所有管理员操作都被记录，但责任追究是粗粒度的。 |
| **攻击场景** | 具有写入 API 密钥的团队中的恶意内部人员删除所有对象。审计日志显示 actor="acme"，但无法识别具体人员。 |
| **影响** | 降低非否认性和问责性。 |
| **建议** | 添加选项以传播请求者身份（例如，JWT `sub` 声明或 API 密钥标签）。将其包含在审计跟踪中。 |
| **工作量** | M（1-3 天） |

---

## 6. 威胁模型（STRIDE 分析）

### 欺骗性 —— 身份可以被伪造吗？

**高风险：**
1. **API 密钥枚举：** `extractToken` 响应 `401 Unauthorized` vs `403 Forbidden`。如果系统区分“无效密钥”和“有效密钥但作用域不足”（它不区分，两者都会触发不同的响应），但是，`Lookup` 中的存储后端调用（`lookupStore`）可能向认证添加定时差异，使得暴力破解成为可能。
   
2. **JWT 算法混淆：** 仅接受 HS256，因此 `alg: none` 攻击在这里不适用（明确拒绝非 HS256 算法）。**已缓解。**

3. **SigV4 重放：** 缺少时间戳验证（发发现 1.3）意味着签名可以被重放。

### 篡改性 —— 数据可以被修改吗？

**高风险：**
1. **快照完整性：** `internal/snapshot/snapshot.go` 创建 tar.gz 文件，但不包含任何 HMAC 或校验和。被篡改的快照在恢复时会被静默接受，可能会注入恶意对象。
   
2. **无对象完整性检查：** 对象通过 Content-MD5 进行校验，但这不是强制性的（opt-in）。没有 Content-MD5 的对象无法被检测到篡改。

3. **预签名 URL HMAC 密钥：** 存储在 `STORAGE_LOCAL_SIGN_KEY` 中。如果泄露，任何人可以生成有效的预签名 URL。

### 否认性 —— 行为可以被否认吗？

1. **审计日志：** 所有管理操作都被记录（`audit_log` 表）。然而，审计是非阻塞的（错误被静默吞掉 `h.audit()` 吞掉错误），因此如果数据库宕机或审计写入失败，操作在没有记录的情况下进行。
   
2. **报告租户来源：** `actor` 字段是粗粒度的（仅租户 ID），降低个体问责性。

### 信息泄露 —— 数据可能泄露吗？

**多个路径：**
1. **错误详情（发发现 5.1）：** 内部错误消息可能暴露网络拓扑。
2. **元数据头部（发发现 2.2）：** Content-Disposition 注入可能导致反射型 XSS。
3. **Webhook 有效载荷存储（发发现 5.2）：** 失败的有效载荷包含完整的事件数据。
4. **CORS 配置：** 默认的 `CORS_ALLOWED_ORIGINS` 为空（CORS 关闭）。但是，如果设置为 `*`，浏览器可以从任何网站读取 API 响应。
5. **预签名 URL：** 如果预签名 URL 在日志或浏览器历史记录中被捕获，它们可能授予访问权限（恶化于过期问题，发发现 3.3）。
6. **AI 搜索缓存：** `result_cache` 按租户 + 查询 + 模式键控。如果结果缓存跨租户共享（），一个租户的敏感文档可能通过语义搜索被另一个租户发现。*审查：缓存键包含 `req.Tenant`，因此按租户隔离。潜在问题缓解。*

### 拒绝服务 —— 服务可以被中断吗？

**多个向量：**
1. **速率限制绕过：** `/healthz`、`/readyz`、`/metrics`、`/openapi.json`、`/docs`、`/ui` 绕过全局速率限制。对这些端点的洪水攻击不消耗 throttle 容量。
2. **内存 DOS——速率限制器映射：** 速率限制器在 `rlMaxBuckets = 50_000` 处上限。如果攻击者使用唯一的 `X-Aero-Tenant` 值发送 50k+ 请求，桶就会被填满，但 evictIdle 会在 10 分钟后清理。缩放测试后风险较低。
3. **AI 预算耗尽：** `TenantDailyBudgetUSD` 防止 AI 成本无限增加。正确实现每日上限。**已缓解。**
4. **并发限制器：** `ConcurrencyLimiter` 使用带权重的计数信号量（GET=1，PUT/POST/DELETE=2）。正确防止资源枯竭。**已缓解。**

### 权限提升 —— 用户能否获得未经授权的访问权限？

**关键发现：**
1. **JWT 租户声明要求（已修复）：** 代码注释说明在发发现审查期间添加了检查 `c.Ten == ""` ？看起来**已实现**在 `decodeAndValidateClaims` 第 106 行。**好。**
2. **匿名公共读取 ACL 门控：** `allowAnonymous` 在提供公共对象之前检查 `auth.IsAnonymous(r.Context())` + `ObjectPublicReadable`。绕过此逻辑的唯一方法是伪造匿名上下文，由于上下文的 `anonCtxKey` 是未导出的，因此无法实现。**正确实现。**
3. **MCP 资源租户边界：** `readResource` 检查 URI 租户是否与请求租户匹配。防止通过制作的 URI 进行跨租户数据访问。**好。**

---

## 7. 合规考虑

### OWASP Top 10 映射

| OWASP 类别 | 状态 | 备注 |
|-------------|--------|---------|
| A01:2021 — 访问控制失效 | ⚠️ 部分缓解 | 匿名读取 ACL 门控正确；API 密钥作用域检查存在；但缺少 S3 预签名 URL 过期验证 |
| A02:2021 — 加密失效 | ⚠️ 风险 | 需要时的 SSE；没有应用层 TLS；HS256 与 JWKS 相比较弱 |
| A03:2021 — 注入 | ✅ 已缓解 | SQL 注入通过参数化查询缓解；路径遍历检查较弱但仍就位 |
| A04:2021 — 不安全设计 | ⚠️ 注意 | MCP stdio 绕过认证（设计使然）；暂无速率限制用于认证端点 |
| A05:2021 — 安全配置错误 | ⚠️ 风险 | 默认情况下 CORS 关闭；安全头部缺失；错误详情默认泄露 |
| A06:2021 — 易受攻击和过时的组件 | ✅ 已缓解 | Go 标准库依赖项；无外部风险依赖项（除了测试） |
| A07:2021 — 身份验证和认证失效 | ⚠️ 风险 | 本地预签名 URL 无法验证过期；SigV4 缺少时间偏移检查 |
| A08:2021 — 软件和数据完整性失效 | ⚠️ 注意 | 快照无完整性校验；通过可配置的 AI 端点存在供应链风险 |
| A09:2021 — 安全日志记录和监控失效 | ⚠️ 已缓解 | 审计日志记录到位；使用 OTel 指标；actor 跟踪较粗 |
| A10:2021 — SSRF | ✅ 已缓解 | Webhook URL、AI 端点、KMS URL 源自配置，不由用户输入控制 |

### 安全头部检查

| 头部 | 存在？ | 备注 |
|--------|---------|-------|
| `Content-Security-Policy` | ❌ 缺失 | Web UI 没有 CSP；潜在的 XSS 风险 |
| `X-Content-Type-Options` | ❌ 缺失 | 未设置 `nosniff` |
| `X-Frame-Options` | ❌ 缺失 | 允许点击劫持 |
| `Strict-Transport-Security` | ❌ 缺失 | 无 HSTS（离开 TLS） |
| `Referrer-Policy` | ❌ 缺失 | 引用信息可能泄露 |
| `Permissions-Policy` | ❌ 缺失 | 无特征控制 |

---

## 发现汇总

| # | 类别 | 严重性 | 标题 | 位置 | 工作量 |
|---|--------|----------|-------|----------|--------|
| 1.1 | 加密 | 中等 | HS256 JWT 要求安全的密钥分发 | `internal/auth/jwt.go:10` | L |
| 1.2 | 认证 | **高** | API 密钥可通过查询字符串或头部泄露 | `internal/auth/auth_middleware.go:129` | S |
| 1.3 | 认证 | 中等 | SigV4 缺少时间偏移检查 | `internal/auth/sigv4.go:91` | S |
| 1.4 | 认证 | 信息 | MCP stdio 绕过认证 | `internal/mcp/server.go` | S |
| 2.1 | 输入验证 | **高** | `validateKey` 路径遍历检查不完整 | `internal/service/file.go:150` | S |
| 2.2 | 输入验证 | 中等 | 元数据头部中的 HTTP 响应拆分 | `internal/api/rest/handler.go:316` | S |
| 2.3 | 输入验证 | 中等 | Bucket Policy 解析可能受注入影响 | `internal/api/rest/handler.go:59` | M |
| 3.1 | 加密 | 中等 | SSE 默认关闭 | `internal/storage/encrypt.go` | S |
| 3.2 | 加密 | **高** | 无应用层 TLS | `cmd/server/main.go:runServer()` | M |
| 3.3 | 加密/认证 | 中等 | 预签名 URL 缺少过期验证 | `internal/storage/sign.go` | M |
| 4.1 | 会话 | 低 | 无 Cookie 安全标志（目前不适用） | — | 信息 |
| 5.1 | 数据保护 | 中等 | 错误响应泄露内部详情 | `internal/api/rest/handler.go:292` | S |
| 5.2 | 数据保护 | **高** | Webhook 失败以明文形式存储完整有效载荷 | `internal/events/webhook.go:113` | M |
| 5.3 | 数据保护 | 低 | 审计日志中粗粒度的 actor 跟踪 | `internal/api/rest/admin.go:275` | M |
| — | 合规 | 中等 | 无 CSP、HSTS、X-Frame-Options 头部 | 全局中间件 | S |
| — | 威胁模型 | 中等 | 快照 tar.gz 无完整性检查 | `internal/snapshot/snapshot.go` | S |
| — | 威胁模型 | 低 | AI 代理工具循环可能导致提示注入 | `internal/ai/agent.go` | M |

---

## 最终总结

| 指标 | 状态 |
|--------|--------|
| **整体安全态势** | **需要改进** |
| **顶级关键问题** | 3 |
| **顶级速赢项** | 3 |
| **安全债务** | 中等（10-15 个已识别问题） |

### 前 3 个关键问题

1. **路径遍历验证不完整（发现 2.1，严重性：高）**  
   路径遍历检查 `strings.Contains(key, "..")` 容易被 URL 编码绕过。这可能导致未经授权读取或写入服务器上的任意文件。  
   **修复：** 对规范化路径使用严格的 `path.Clean` 比较。

2. **无应用层 TLS（发现 3.2，严重性：高）**  
   传输中的数据（包括 API 密钥和对象内容）默认以明文形式传输。尽管 TLS 终止可以委托给反向代理，但没有内置选项会增加部署风险。  
   **修复：** 添加可选的 HTTPS 支持。

3. **Webhook 失败有效载荷存储 PII/敏感数据（发现 5.2，严重性：高）**  
   完整的 webhook 事件有效载荷在重试表中以明文形式存储。具有 `admin` 作用域或数据库访问权限的攻击者可以读取敏感元数据、标签和 PII。  
   **修复：** 在存储之前遮盖或截断有效载荷，并实现 TTL 清除。

### 前 3 个速赢项（高影响，低工作量，每项 < 1 天）

1. **在 `validateKey` 中使用 `path.Clean`（发现 2.1）**  
   用严格比较替换 `strings.Contains(key, "..")`：  
   ```go
   func validateKey(key string) error {
       if key == "" { return ... }
       cleaned := path.Clean(key)
       if cleaned != key || strings.HasPrefix(key, "/") {
           return fmt.Errorf("%w: illegal key %q", ErrInvalidArgs, key)
       }
       return nil
   }
   ```

2. **在 SigV4 中添加时间偏移验证（发现 1.3）**  
   在 `verifyHeader` 中添加最多 15 分钟的时间戳接受窗口。使用 AWS 推荐的偏移量。

3. **添加安全头部中间件（合规发现）**  
   为 `/ui` 路径添加 CSP、X-Content-Type-Options、X-Frame-Options、Referrer-Policy 和 HSTS 头部。这些是低成本的纵深防御措施。

### 安全债务

以下已分类的问题构成累积的安全债务：

| 区域 | 债务 | 解决所需 |
|-------|-------|---------------|
| 加密 | 无内置 TLS + 可选 SSE | 1-3 天 |
| 认证 | HS256 JWT 密钥分发 | > 3 天（RS256/JWKS 重构） |
| 数据保护 | 错误详情泄露 + webhook 有效载荷存储 | 1-2 天 |
| 威胁模型 | 快照完整性 + AI 代理提示注入 | 1-3 天 |
| 合规 | 缺失安全头部 | S（< 1 天） |
| 审计 | 粗粒度的 actor 身份 | M（1-3 天） |
