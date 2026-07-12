我现在已拥有进行全面安全审查所需的全部背景信息。以下是完整报告。

---

# 安全审查报告：aero-vault

## 执行摘要

在审查的 `cmd/server/main.go` 装配入口、`internal/auth/`、`internal/mcp/`、`internal/storage/encrypt.go`、`internal/events/webhook.go`、`internal/middleware/` 以及所有 REST/S3/WebDAV 处理程序的基础上，我发现 **1 个严重漏洞、3 个高危漏洞和 5 个中等严重问题**。

---

## 详细发现

### 1. 严重 — MCP stdio 模式完全绕过认证

| 字段 | 值 |
|---|---|
| 分类 | 认证 / 授权 |
| 严重性 | **严重** |
| 标题 | MCP STDIO 传输使用静态租户且不进行身份验证 |
| 位置 | `internal/mcp/server.go:53` — `tenantFor()` 回退到 `s.tenant`，该值硬编码为 `"default"` |
| | `internal/mcp/transport.go:21-37` — `ServeStdio` 直接调用 `s.Handle()` |
| | `cmd/server/main.go:214` — `mcp.NewServer(svc, repo, search, "default", ...)` |
| 描述 | MCP stdio 模式（`aero-vault mcp`）在 `tenantFor()` 中硬编码租户为 `"default"`。没有 `Authorization` 头、没有 API 密钥检查、没有 JWT 验证。任何可以启动二进制文件或通过 stdin 管道输入的操作系统用户都可以读取、写入、删除文件以及执行搜索，而无需任何身份验证凭据。虽然 `HTTPHandler` 会通过 chi 路由器的认证中间件，但 stdio 路径完全绕过它。 |
| 攻击场景 | 在共享服务器上具有本地 shell 访问权限的攻击者运行 `echo '{"method":"tools/call","params":{"name":"write_file","arguments":{"key":"malicious.sh","content":"malware"}}}' | aero-vault mcp` — 立即获得对 vault 数据的完全未经身份验证的访问权限。 |
| 影响 | 完全破坏所有租户数据的机密性、完整性和可用性，且无需身份验证。 |
| 建议 | 在 stdio 模式下强制进行身份验证。选项：(a) 需要 `--tenant` + `--token` 标志，在启动时验证；(b) 将 stdio 传输限制为仅接受操作系统的 stdin/stdout 管道，并记录警告，表明没有进行身份验证；(c) 添加 `STDIO_REQUIRE_AUTH=true` 环境变量，在建立 MCP 会话之前要求使用 `initialize` 逻辑进行身份验证。 |
| 工作量 | S（<1 天） |

### 2. 高 — API 密钥哈希使用 SHA-256（速度过快，且无加盐）

| 字段 | 值 |
|---|---|
| 分类 | 密码学 / 数据保护 |
| 严重性 | **高** |
| 标题 | API 密钥使用原始 SHA-256 而不是慢速密码哈希进行哈希处理 |
| 位置 | `internal/auth/store.go:76-78` — `HashToken` 函数 |
| | `internal/auth/auth_middleware.go:124` — 将令牌发送到 `Lookup()` |
| 描述 | `HashToken` 使用 `sha256.Sum256([]byte(token))` 对明文 API 令牌进行哈希处理。SHA-256 是一种快速、未加盐的哈希，容易受到暴力破解和彩虹表攻击。如果通过 SQL 注入、备份泄露或日志文件泄露了持久化 `api_keys` 表，攻击者可以轻松反转这些哈希值。OWASP 建议将 Argon2id、bcrypt 或 PBKDF2 用于存储的凭据。 |
| 攻击场景 | 攻击者获得对 SQLite 数据库文件的读取权限。他们提取 `token_hash` 列，在 GPU 上运行 `hashcat -m 1400`（原始 SHA-256），并在一小时内恢复所有 API 密钥。 |
| 影响 | 完全窃取所有持久化 API 密钥，导致跨租户的未经授权访问。 |
| 建议 | 用 `golang.org/x/crypto/argon2` 替换 `HashToken`，对 API 密钥令牌进行加盐和慢速哈希处理。密钥通过管理 API 创建时，使用一个强的随机加盐值。 |
| 工作量 | M（1-2 天） |

### 3. 高 — MCP 资源读取 URI 路径遍历

| 字段 | 值 |
|---|---|
| 分类 | 输入验证 |
| 严重性 | **高** |
| 标题 | `aero-vault://` URI 方案未正确验证路径遍历 |
| 位置 | `internal/mcp/server.go:181-211` — `readResource` |
| 描述 | `readResource` 使用 `strings.SplitN(rest, "/", 3)` 将 URI 拆分为 `[tenant, bucket, key]`。由于 `key` 作为单个字符串传递给 `s.svc.Get(ctx, allowedTenant, parts[1], parts[2])`，而 `FileService.Get` 使用 `storageKey(tenant, bucket, key)`，然后执行 `path.Join`，因此包含 `..` 的精心构造的密钥可能会导致路径遍历，并可能读取存储根目录之外的文件。 |
| 攻击场景 | 调用 `resources/read`，URI 为 `aero-vault://default/default/../../../etc/passwd`。密钥 `../../../etc/passwd` 与存储密钥 `default/default/../../../etc/passwd` 连接，解析时必须经过验证。 |
| 影响 | 从文件系统读取任意文件。 |
| 建议 | 在密钥进入 `s.svc.Get()` 之前，对所有 MCP 工具中的密钥参数应用 `service.validateKey()`。MCP 应调用 `service.IsValidKey(key)` 或类似的验证方法。 |
| 工作量 | S（<1 天） |

### 4. 高 — JWT 实现使用 HS256；没有 RS256/ES256 支持，也没有密钥轮换

| 字段 | 值 |
|---|---|
| 分类 | 密码学 / 认证 |
| 严重性 | **高** |
| 标题 | JWT 使用对称 HS256，无法进行非对称签名验证 |
| 位置 | `internal/auth/jwt.go:36-48` — `JWTVerifier` 结构体 |
| | `internal/auth/jwt.go:103-105` — 强制要求 `alg == "HS256"` |
| 描述 | HS256 是一种对称算法：签名和验证使用相同的密钥。任何知道 `AUTH_JWT_SECRET` 的人都可以伪造任何租户的令牌。不存在密钥轮换机制。不支持 RS256/ES256，这使得与外部 IdP（例如 Keycloak、Okta）集成不可能以标准方式进行。 |
| 攻击场景 | 内部人员泄露了 `AUTH_JWT_SECRET`，或者攻击者通过文件泄露获得了它。他们可以为任何租户伪造具有任何作用域的 JWT 令牌，从而获得管理员访问权限。 |
| 影响 | 使用 JWT 的租户的认证完全被攻破。 |
| 建议 | 添加 RS256 支持以及可选的 JWKS 端点验证。保持 HS256 作为简单部署的选项，但记录其局限性。添加密钥轮换支持，允许重叠的验证密钥。 |
| 工作量 | L（2-3 天） |

### 5. 中 — 桶策略引擎仅验证来源 IP；忽略 ARN 主体

| 字段 | 值 |
|---|---|
| 分类 | 授权 |
| 严重性 | **中** |
| 标题 | 桶策略 `matchesPrincipal` 只接受 `"*"`，忽略所有 ARN 检查 |
| 位置 | `internal/auth/policy.go:113-123` — `matchesPrincipal` |
| 描述 | `matchesPrincipal()` 检查 `candidate == "*"`，否则总是返回 `true`。未实现实际的主体 ARN 解析（例如 `AWS: arn:aws:iam::123456789012:user/Bob`）。任何桶策略，如果意图将访问限制到特定 IAM 用户或角色，都会被忽略，从而允许所有请求访问。 |
| 攻击场景 | 管理员编写桶策略：`{"Effect":"Deny","Principal":{"AWS":"arn:aws:iam::...":"user/Bob"}}`，期望只允许特定的外部 S3 客户端。由于主体总是匹配，该策略没有效果。 |
| 影响 | 桶策略提供的保护存在缺陷，可能导致意外访问。 |
| 建议 | 实现基于 ARN 的主体匹配，或记录桶策略主体验证尚未实现且 `"*"` 是唯一受支持值的问题。 |
| 工作量 | M（2-3 天） |

### 6. 中 — Webhook HMAC 使用 `sha256=` 前缀；未使用时间安全比较

| 字段 | 值 |
|---|---|
| 分类 | 密码学 |
| 严重性 | **中** |
| 标题 | Webhook 消费者无效的 HMAC 签名格式 |
| 位置 | `internal/events/webhook.go:80-81` — HMAC 构建 |
| 描述 | HMAC 签名被格式化为 `sha256=<hex>` 用于 `X-Aero-Signature` 头。然而，这缺乏标准约定，例如 `t=...` 时间戳（以防止重放攻击）或 `v1=...`（用于密钥轮换）。此外，webhook 消费者端（在代码库外）应使用恒定时间比较，但此处无法验证。 |
| 攻击场景 | 攻击者捕获一个 webhook 有效负载并稍后重放它，因为没有时间戳绑定到签名。如果 webhook 消费者使用非恒定时间字符串比较，定时侧信道攻击是可能的。 |
| 影响 | Webhook 重放可能。 |
| 建议 | 将签名格式更改为包括时间戳：`t=1234567890,v1=<hex>`。记录 webhook 消费者应使用 `hmac.Equal` 进行验证。 |
| 工作量 | S（<1 天） |

### 7. 中 — 日志将敏感数据写入错误消息

| 字段 | 值 |
|---|---|
| 分类 | 数据保护 |
| 严重性 | **中** |
| 标题 | 内部错误细节在 HTTP 响应和日志中暴露 |
| 位置 | `internal/api/rest/admin.go:59` — `"InternalError"` 返回 `err.Error()` |
| | `internal/api/rest/admin.go:43` — 输入验证错误包含完整的解码错误 |
| | `cmd/server/main.go:63,68` — 启动错误输出到 stderr |
| 描述 | 多个错误路径返回内部错误消息，其中可能包含连接字符串、文件路径或内部状态。例如，`store.Stat(ctx, "@healthz/probe")` 在存储健康检查上的失败错误会传递到响应体中。管理 API 在 `"InternalError"` 响应中回显完整的 Go `err.Error()` 字符串，可能泄露 DSN、路径或其他敏感数据。 |
| 攻击场景 | 攻击者向 `/v1/admin/tenants/*/quota` 发送格式错误的 JSON，并收到详细的 Go 错误文本，其中包含关于内部数据结构的线索。 |
| 影响 | 信息泄露可能有助于后续攻击。 |
| 建议 | 实现一个错误分类函数，将内部错误映射到通用的、用户安全的错误消息。只有操作员端点（`/metrics`、`/debug`）应该暴露原始错误字符串。 |
| 工作量 | S（<1 天） |

### 8. 中 — SQLite WAL 模式可能使数据库暴露

| 字段 | 值 |
|---|---|
| 分类 | 数据保护 |
| 严重性 | **中** |
| 标题 | SQLite WAL/SHM 文件可能保持解锁状态并暴露数据 |
| 位置 | `internal/snapshot/snapshot.go:53-55` — 显式地包含 `-wal` 和 `-shm` 文件 |
| | `config.go:DB.DSN` — 默认 `./var/aero.db` |
| 描述 | SQLite 在 WAL 模式下运行（由 `_pragma=journal_mode=wal` 隐含）。WAL 和 SHM 文件与主 `.db` 文件并排放置，可能包含幻影数据或在服务器进程死后未刷新的数据。备份/快照代码显式地包含这些文件，确认其中包含数据。在卸载或进程终止后，这些文件可能保持解锁状态并包含敏感数据。 |
| 攻击场景 | 管理员终止该进程。WAL 文件包含未检查点的事务数据。文件系统读取器恢复已删除的数据库页面。 |
| 影响 | 数据残留。 |
| 建议 | 添加一个关闭钩子 `PRAGMA wal_checkpoint(TRUNCATE)` 来清理 WAL。在启动时设置文件系统权限（例如 `0600`）。确保存储 `./var/` 目录的 `umask` 被正确配置。 |
| 工作量 | S（<1 天） |

### 9. 低 — 可选 ETag 验证默认关闭

| 字段 | 值 |
|---|---|
| 分类 | 数据完整性 |
| 严重性 | **低** |
| 标题 | 读取路径 ETag 完整性验证是选项，默认关闭 |
| 位置 | `internal/service/file.go:42-52` — `ReadVerificationConfig` |
| | `internal/service/file_crud.go:150+` — `ETagVerifier` |
| 描述 | 虽然存在 `ETagVerifier`，但它仅在通过 `WithReadVerification` 显式启用时才被挂载。默认情况下，读取的内容根据记录的 ETag 进行零验证。存储损坏或静默数据损坏不会被检测到。 |
| 攻击场景 | 存储后端（例如 S3、本地磁盘）遭受静默数据损坏。文件已损坏但返回给用户，没有警告。或者，具有存储访问权限的恶意内部人员修改了 blob。 |
| 影响 | 静默数据损坏可能未被察觉。 |
| 建议 | 默认情况下启用 ETag 验证，可能通过一个 `STORAGE_VERIFY_ETAG=strict` 配置项来实现，该配置项为所有 GET 请求启用它。为方便起见，保留 `"opt-in"` 模式，但将其设为默认行为。 |
| 工作量 | S（<1 天） |

### 10. 低 — SSRF 在 HTTP KMS/密钥提供者中可能

| 字段 | 值 |
|---|---|
| 分类 | 威胁模型 / 输入验证 |
| 严重性 | **低** |
| 标题 | HTTP KMS 和密钥 URL 端点接受任意 URL |
| 位置 | `internal/storage/secret.go:139-147` — `newHTTPProvider` 接受任何 URL |
| | `internal/config/config.go:178-183` — `SSEKMSURL`、`SSEKeyURL` 是自由格式字符串 |
| 描述 | `STORAGE_LOCAL_SSE_KMS_URL`、`STORAGE_LOCAL_SSE_KEY_URL` 等配置值没有验证以限制为 HTTPS 或绕过主机白名单。如果攻击者可以控制配置，他们可以指向恶意服务器。这在受控部署中风险低，但值得注意。 |
| 攻击场景 | 配置错误指向 `http://evil.internal/kms` 会向攻击者服务器发送加密密钥。 |
| 影响 | KEK/DKE 泄漏。 |
| 建议 | 通过在启动时解析和验证这些 URL 并警告非 TLS 端点，添加最少的主机名/IP 验证。记录安全要求。 |
| 工作量 | S（<1 天） |

### 11. 低 — CORS `Allow-Credentials` 与通配符来源结合使用

| 字段 | 值 |
|---|---|
| 分类 | 合规性 |
| 严重性 | **低** |
| 标题 | CORS 配置在使用凭据时可能设置 `Access-Control-Allow-Origin: *` |
| 位置 | `internal/middleware/cors.go:36-39` |
| 描述 | 如果 `CORS_ALLOWED_ORIGINS=*`，并且 `AllowCreds` 被设置为 true（目前未设置，但将来可能），`Access-Control-Allow-Origin` 被设置为通配符，这违反了 Fetch 标准。浏览器会忽略 `Allow-Credentials: true` 并拒绝该请求。 |
| 攻击场景 | 只有在将来添加凭据支持时才会出现问题。 |
| 影响 | 功能问题，而非安全漏洞（目前）。 |
| 建议 | 如果 `AllowCreds` 为 true，则拒绝通配符来源，并记录原因。 |
| 工作量 | S（<1 天） |

### 12. 信息 — 无 TLS 终止

| 字段 | 值 |
|---|---|
| 分类 | 数据保护 |
| 严重性 | **信息** |
| 标题 | 服务器不强制执行 HTTPS/TLS |
| 位置 | `cmd/server/main.go:101` — `http.Server`（纯文本） |
| 描述 | 服务器在客户端和服务器之间以纯文本 HTTP 通信。所有 API 密钥、JWT 令牌和内容都以明文传输。假设是在反向代理（Nginx、Cloudflare）后面运行，但这并非强制要求。 |
| 影响 | 传输中的数据暴露于网络级攻击者。 |
| 建议 | 添加一个 `APP_TLS_CERT`/`APP_TLS_KEY` 配置对，用于内置 TLS，或记录要求在前置代理处终止 TLS。 |
| 工作量 | S（<1 天） |

---

## STRIDE 分析总结

| 类别 | 风险 | 关键发现 |
|---|---|---|
| **S**poofing（欺骗） | 高 | MCP stdio 无认证；HS256 对称密钥允许伪造 |
| **T**ampering（篡改） | 中 | ETag 验证为 opt-in；webhook 签名无时间戳绑定；桶策略主体检查不完整 |
| **R**epudiation（抵赖） | 低 | 管理操作有审计日志记录；文件操作无审计 |
| **I**nformation Disclosure（信息泄露） | 中 | 错误消息泄漏内部状态；WAL 文件可能包含残留数据 |
| **D**enial of Service（拒绝服务） | 中 | 速率限制存在，但大上传可能导致内存压力；并发限制存在，但阈值可能允许资源耗尽 |
| **E**levation of Privilege（权限提升） | 高 | MCP 绕过租户隔离；不存在存储桶策略主体验证 |

---

## 最终总结

| 指标 | 状态 |
|---|---|
| **总体安全态势** | **需要改进** |
| **严重问题** | 1 |
| **高危问题** | 3 |
| **中危问题** | 4 |
| **低危/信息性问题** | 4 |

### 前 3 个关键问题

1. **MCP stdio 无认证**（严重） — 本地 shell 访问 = 无限制的 vault 访问。必须修复。
2. **API 密钥使用原始 SHA-256 哈希**（高） — 数据库泄露 = 所有凭据泄露。迁移到 Argon2id。
3. **JWT 使用 HS256，无可扩展性/轮换**（高） — 密钥泄露 = 全面的身份伪造。添加 RS256 + JWKS。

### 前 3 个速赢项

1. **MCP 密钥验证** — 在 MCP 工具处理程序中添加 `validateKey()`。S（<1 天）。
2. **错误清理** — 创建一个 `sanitizeError(err)` 包装器，用于通用响应。S（<1 天）。
3. **关闭时 SQLite WAL 检查点** — 添加 `PRAGMA wal_checkpoint(TRUNCATE)`。S（<1 天）。

### 安全债务

- 密码哈希需要架构更改（将哈希算法从 SHA-256 迁移到 Argon2id）
- JWT 密钥轮换需要设计用于重叠密钥的 `kid` 头支持
- 桶策略需要完整的主体 ARN 解析以获得有意义的 IAM 兼容性
- 传输加密（HTTPS）留给基础设施；应记录
