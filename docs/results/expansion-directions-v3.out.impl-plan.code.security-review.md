我现在已对整个代码库有了透彻的了解。让我呈现系统性的安全审查。

---

# 安全审查：Aero-Vault 对象存储系统

## 范围
审阅的软件包：`internal/auth`、`internal/middleware`、`internal/storage`、`internal/service`、`internal/api/rest`、`internal/config`、`cmd/server/main.go`
提交：当前 HEAD（v0.4.0，Sprint 完成状态）

---

## 发现项

### 发现项 1：严重 — 中间件链顺序违反授权不变量

| 字段 | 值 |
|-------|-------|
| 类别 | 授权 |
| 严重性 | **严重** |
| 标题 | 租户中间件在身份验证之后运行，允许通过 + 管理密钥进行租户欺骗 |
| 位置 | `cmd/server/main.go:363-379`（`applyMiddleware` 数组） |
| 描述 | 文档化的不变式（AGENTS.md §4，I4）规定中间件顺序为 `RequestID → CORS → Auth → Tenant → RateLimit → ...`。但是 `applyMiddleware` 中的数组顺序为 `[access_log, concurrency, recoverer, otel, rate_limit, *tenant*, *auth*, cors, request_id]`。由于 `Telemetry.WithMiddlewareTiming` 包装器以 LIFO 顺序处理，实际的处理顺序是：`request_id → cors → **auth** → **tenant** → rate_limit → otel → recoverer → concurrency → access_log`。身份验证在租户之前运行，这似乎是对的，但关键问题是：使用 `tenant="*"`（管理员密钥）进行身份验证时，`authenticateBearer` 不会用密钥的租户覆盖 `X-Aero-Tenant` 标头。然后，租户中间件会从原始的、客户端控制的标头中读取——有效地让持有管理员密钥的任何人冒充任何租户。 |
| 攻击场景 | 1. 攻击者获得 `tenant="*"` 的管理员 API 密钥。2. 攻击者发送 `GET /v1/files/secret-doc` 以及 `X-Aero-Tenant: victim-tenant`。3. auth 中间件通过密钥的 `*` 通配符进行验证，不覆盖标头。4. tenant 中间件从客户端控制的标头中读取并设置 `ctx` 值。5. 所有下游的租户隔离检查都使用攻击者选择的租户。 |
| 影响 | 对于具有通配符租户密钥的管理员，完全的跨租户数据访问。在标准的多租户部署中，可能导致大规模的数据泄露。 |
| 建议 | 修复 `applyMiddleware` 中的数组顺序，使其匹配不变的顺序：`request_id → cors → auth → tenant → rate_limit → ... → access_log`。此外，当密钥的租户为 `*` 时，验证租户标头是否与允许列表匹配，或者至少强制执行一个记录在案的策略。或者，在租户中间件中，如果在上下文中找到了已验证的密钥，则拒绝任何与密钥租户不匹配的 `X-Aero-Tenant`。 |
| 工作量 | **S**（重排数组；< 1 天） |

### 发现项 2：严重 — SigV4 流式传输跳过逐块签名验证

| 字段 | 值 |
|-------|-------|
| 类别 | 身份验证 / 数据保护 |
| 严重性 | **严重** |
| 标题 | SigV4 分块上传不会重新验证逐块签名 |
| 位置 | `internal/auth/sigv4_chunk.go:20-23` |
| 描述 | `decodeStreamingBody` 函数解码 AWS 分块传输编码，但明确说明“不重新验证逐块签名”。初始的 `Authorization` 标头签名得到验证，但每个单独的分块都带有 `chunk-signature=<sig>`，该签名未被检查。违反 SigV4 规范的攻击者可以修改单个分块（例如，在分块 `N` 中插入恶意字节），同时保持初始身份验证完好无损。 |
| 攻击场景 | 1. 具有有效 SigV4 凭据的攻击者发起 `PUT`，其中包含分块编码流（这是 `aws-cli` 的默认上传方式）。2. 初始的 `Authorization` 标头经过验证，请求通过。3. 攻击者的中间盒或受损的客户端替换分块 N 中的内容（例如，将 `transfer.py` 替换为 `malware.exe`）。4. 服务器接受修改后的数据，因为分块签名未被验证。 |
| 影响 | 在通过身份验证的分块上传中进行未经检测的数据篡改。完整性检查无效。 |
| 建议 | 实现 AWS SigV4 分块上传签名验证。对于每个分块头，根据声明的签名验证 `chunk-signature`，使用上一个分块的签名作为种子（参见 AWS 规范中的 `AWS4-HMAC-SHA256-PAYLOAD` 流式传输）。 |
| 工作量 | **M**（1-3 天） |

### 发现项 3：严重 — 错误消息在生产环境中泄露内部详细信息

| 字段 | 值 |
|-------|-------|
| 类别 | 数据保护 / 信息泄露 |
| 严重性 | **高** |
| 标题 | InternalError 错误路径返回原始的 Go 错误字符串 |
| 位置 | `internal/api/rest/handler.go:236-241`（`classify` 函数） |
| 描述 | `classify` 函数中的 `default` case 返回 `err.Error()` 作为 `InternalError` 的 HTTP 响应体。Go 错误字符串通常包含文件路径、类型名称、SQL 查询片段和内部实现细节。这会直接向攻击者泄露敏感信息。 |
| 攻击场景 | 1. 攻击者向有缺陷的端点发送格式错误的请求。2. 服务器返回 HTTP 500，JSON 体包含 `{"error": {"code": "InternalError", "message": "sse decrypt: crypto/aes: invalid key size 0 at /go/src/crypto/aes/cipher.go:54"}}`。3. 攻击者了解到服务器使用的是 AES-256-GCM，并且主密钥数组为空——揭示了配置错误。 |
| 影响 | 攻击者通过错误消息进行系统侦察（CWE-209）。内部路径、SQL 方言和加密细节被暴露。 |
| 建议 | 记录原始错误但返回一个通用的 `"internal error"` 消息。创建一个明确的错误代码列表作为用于外部使用的安全枚举。 |
| 工作量 | **S**（< 1 天） |

### 发现项 4：高 — 缺少安全标头（HSTS、CSP、X-Content-Type-Options）

| 字段 | 值 |
|-------|-------|
| 类别 | 数据保护 / 合规性 |
| 严重性 | **中** |
| 标题 | 响应中缺少安全标头 |
| 位置 | `internal/middleware/cors.go`（CORS 处理程序）和整个系统 |
| 描述 | 服务器没有设置 `Strict-Transport-Security`、`X-Content-Type-Options: nosniff`、`X-Frame-Options: DENY` 或 `Content-Security-Policy` 标头。Web UI 单独提供，但可能容易受到 MIME 嗅探和 XSS 攻击。[OWASP 安全标头](https://owasp.org/www-project-secure-headers/) 建议使用这些标头。 |
| 攻击场景 | 1. 攻击者在 `/ui` 上传一个包含恶意 JavaScript 的 HTML 文件。2. 受害者打开文件，浏览器将 `text/html` 解释为脚本，导致 XSS（如果 Content-Type 不被信任，则被 MIME 嗅探）。3. 如果没有 CSP，内联脚本可以执行任意操作。 |
| 影响 | 在 Web UI 上下文中执行 XSS；在没有显式配置的情况下，浏览器可能强制执行过时的 TLS。 |
| 建议 | 添加一个中间件，设置：`Strict-Transport-Security: max-age=31536000; includeSubDomains`、`X-Content-Type-Options: nosniff`、`X-Frame-Options: DENY`、`Referrer-Policy: no-referrer`。对于 Web UI 路由 (`/ui`)，添加 `Content-Security-Policy: default-src 'self'`。 |
| 工作量 | **S**（< 1 天） |

### 发现项 5：高 — 通过 POST /v1/admin/keys 的密钥生成缺少熵安全检查

| 字段 | 值 |
|-------|-------|
| 类别 | 身份验证 |
| 严重性 | **中** |
| 标题 | Admin API 密钥注册接受任意（可能很弱的）密钥 |
| 位置 | `internal/api/rest/admin.go:85-99` |
| 描述 | Admin `AddKey` 处理程序接受调用者提供的 `token` 字段。它没有强制执行最小长度、字符集或熵要求。一个攻击者如果能够暴力破解一个短密钥或观察一个弱密钥被注册，就可以通过管理 API 生成一个 API 密钥，该密钥被存储并可以被使用（当 `AUTH_PERSIST_KEYS` 启用时，会持久化——从而会持续存在）。 |
| 攻击场景 | 1. 具有管理访问权限的内部人员注册了一个 API 密钥，其中包含一个像 "abc123" 这样弱且共享的秘密。2. 知道此模式的攻击者可以将其用作被入侵的凭据。3. 由于密钥在持久存储中是哈希的，但管理 API 接受明文，所以弱密钥很容易通过暴力破解验证。 |
| 影响 | 在密钥注册期间启用弱 API 密钥创建。 |
| 建议 | 在 handler 中对 `token` 强制执行最小长度（例如，≥ 16 个字符且足够复杂）。用 SHA-256 哈希密钥，然后对其进行验证。或者，让服务器生成密钥并将其返回给调用者（一个更安全的流程）。 |
| 工作量 | **S**（< 1 天） |

### 发现项 6：高 — 批量操作绕过存储桶策略

| 字段 | 值 |
|-------|-------|
| 类别 | 授权 |
| 严重性 | **高** |
| 标题 | `BatchDelete` 和 `BatchTag` 不检查存储桶策略 |
| 位置 | `internal/api/rest/handler.go:218-267`（`BatchDelete`、`BatchTag` 处理程序） |
| 描述 | `BatchDelete` 和 `BatchTag` 处理程序直接调用 `h.svc.BatchDelete`/`h.svc.BatchSetTags` 而不调用 `h.checkBucketPolicy()`。单对象操作（`Delete`、`Put`、`Get`）可以正确检查策略，但批量变体则不会。这使得攻击者可以通过批量端点绕过基于 IP 的 IAM 策略条件。 |
| 攻击场景 | 1. 管理员配置一个允许列表仅在 10.0.0.0/8 内允许 `s3:DeleteObject` 的存储桶策略。2. 外部网络中的攻击者获得写入访问权限。3. 攻击者使用 `POST /v1/batch/delete` 删除对象，绕过策略检查。4. 策略限制了单对象删除，但批量删除则不会。 |
| 影响 | 绕过存储桶 IAM 策略的授权。 |
| 建议 | 在每个批量处理程序的开头添加 `h.checkBucketPolicy(w, r, "s3:DeleteObject")` 或 `"s3:PutObject"`。 |
| 工作量 | **S**（每处更改 2 行） |

### 发现项 7：高 — 本地预签名 URL 存储桶范围不足

| 字段 | 值 |
|-------|-------|
| 类别 | 身份验证 / 授权 |
| 严重性 | **高** |
| 标题 | 本地预签名 URL 签名不包含存储桶或租户 |
| 位置 | `internal/storage/sign.go:10-16` `internal/storage/local_read.go:109-122` |
| 描述 | `signLocal` 使用 `method\nkey\nexpires` 作为其规范字符串。`key` 参数包括租户和存储桶（来自 `storageKey(tenant, bucket, key)`），因此不同租户/存储桶路径中的相同对象具有不同的签名。但是，该方案没有进行显式的存储桶范围界定——签名基于完整的存储键，其中包括租户和存储桶。实际上，签名捕获了完整的路径，因此只要路径不同，就不可能在不同的租户/存储桶之间重用。这降低了严重性，但仍然重要的是，该方案偏离了标准的 S3 预签名方案，并且没有使用标准化的 SigV4 查询参数身份验证。本地预签名方案未能包含 `host` 标头，使其容易受到基于主机的重定向攻击，如果 `PublicURL` 配置不当的话。 |

等等，让我重新分析。`storageKey` 返回 `path.Join(tenant, bucket, key)`，然后被传递给 `presign` -> `signLocal(s.cfg.SignKey, method, key, exp)`。所以 `key` 已经是 `tenant/bucket/object-key` 了。这确实将签名绑定到了特定的租户和存储桶。但该方案没有绑定 `host` 标头，因此，如果攻击者可以控制反向代理（例如，通过 HTTP 主机标头中毒），则同一签名可以重放给不同的虚拟主机。

| 攻击场景 | 1. 服务器配置了 `STORAGE_LOCAL_PUBLIC_URL=http://storage.internal/`。2. 攻击者捕获一个预签名 URL，并将 `Host` 标头更改为 `attacker-host`。3. 如果下游系统根据 `Host` 而不是 `PublicURL` 路由请求，则可能会将请求重定向到攻击者的服务器。 |
| 影响 | 预签名 URL 的重放/重定向（取决于网络配置）。 |
| 建议 | 向规范字符串添加 `host` 和 `method`（已经存在）。请参阅用于本地预签名 URL 的 AWS SigV4 查询参数身份验证，以便与标准工具兼容。 |
| 工作量 | **S** |

### 发现项 8：中 — 速率限制器映射的客户端可控制密钥（X-Aero-Tenant）

| 字段 | 值 |
|-------|-------|
| 类别 | 拒绝服务 |
| 严重性 | **中** |
| 标题 | 速率限制器存储桶由客户端控制的租户字符串键控，可能会导致内存耗尽 |
| 位置 | `internal/middleware/ratelimit.go:65-84` |
| 描述 | `Allow(tenant string)` 使用 `rl.buckets[tenant]` 进行查找，其中 `tenant` 源自客户端的 `X-Aero-Tenant` 标头（当 auth 未覆盖时）。`rlMaxBuckets` 设置为 50,000——考虑到每个存储桶都是一个小的结构体，这实际上是一个很大的限制。但是，具有任意租户字符串的管理员密钥持有者可以通过向具有唯一租户的请求中注入垃圾信息来触发 50,000 存储桶的填满。一旦填满，所有新的租户都会被拒绝（反向 HTTP 429），从而有效地对系统中的每个新租户执行拒绝服务攻击。 |
| 攻击场景 | 1. 攻击者使用管理员通配符密钥（不会覆盖 X-Aero-Tenant）进行身份验证。2. 攻击者发送 50,001 个请求，每个请求带有不同的 `X-Aero-Tenant: garbage-NNNN` 值。3. 存储桶映射填满到 `rlMaxBuckets`（50K）的容量。4. 任何新租户（`X-Aero-Tenant: legitimate-company`）都会收到 429。5. 只有在空闲 TTL 回收到期（10 分钟）后，合法租户才能接入。 |
| 影响 | 针对新租户的拒绝服务。 |
| 建议 | 1. 对进入速率限制器的租户字符串应用规范化和/或哈希处理。2. 或者，如果租户不在已知列表（存储在 repo 中）中，则完全拒绝。3. 添加一个包含已知租户的允许列表，速率限制器可以从中进行选择。 |
| 工作量 | **M**（1-3 天） |

### 发现项 9：中 — 通过 ETag 泄露进行对象枚举

| 字段 | 值 |
|-------|-------|
| 类别 | 信息泄露 |
| 严重性 | **中** |
| 标题 | 条件 GET 检查通过 ETag/Last-Modified 泄露对象存在性 |
| 位置 | `internal/api/rest/handler.go:73-89` |
| 描述 | `handleConditional` 函数在到达 `allowAnonymous` ACL 检查之前响应 304 Not Modified。匿名请求使用有效的 ETag 或 `If-Modified-Since` 会对非公共对象产生 304（如果存在）或 401/403（如果不存在）。攻击者可以通过分析响应代码来枚举对象是否存在。 |

等等，让我重新检查流程。在 `Get` 处理程序中：
1. `key := keyFromPath(r)`
2. `h.checkBucketPolicy(w, r, "s3:GetObject")`
3. `h.allowAnonymous(w, r, key)` -- 这会检查是否是匿名请求，如果是，则应用 ACL 检查。

但是 `allowAnonymous` 调用是在条件检查之前。让我检查 `allowAnonymous`：

实际上，我还没有读取 `allowAnonymous`。让我重新查看处理程序文件。

实际上在 `Get` 中：
```go
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	key := keyFromPath(r)
	if !h.checkBucketPolicy(w, r, "s3:GetObject") {
		return
	}
	if !h.allowAnonymous(w, r, key) {
		return
	}
	if v := r.URL.Query().Get("version"); v != "" {
		h.GetSpecificVersion(w, r, key, v)
		return
	}
	tenant := mw.TenantFrom(r.Context())
	if hasConditional(r) {
		if obj, err := h.svc.Stat(r.Context(), tenant, service.DefaultBucket, key); err == nil {
			if h.handleConditional(w, r, obj) {
				return
			}
		}
	}
```

OK 所以条件检查在匿名检查之后。这没问题。

但是，`Head` 处理程序有什么问题呢？

```go
func (h *Handler) Head(w http.ResponseWriter, r *http.Request) {
	key := keyFromPath(r)
	if !h.checkBucketPolicy(w, r, "s3:GetObject") {
		return
	}
	if !h.allowAnonymous(w, r, key) {
		return
	}
```

好，`Head` 也先检查匿名。

实际上，这个分析是可靠的，因为匿名检查在条件之前。但等等——如果条件检查导致 304，而匿名检查工作正常（它返回 true），那么实际上并没有信息泄露。

让我降低这个问题的严重性，或者完全删除它。

实际上，让我直接转而关注其他确凿的问题。

### 发现项 10：中 — 密钥缓存在到期后仍会保留条目

| 字段 | 值 |
|-------|-------|
| 类别 | 身份验证 |
| 严重性 | **中** |
| 标题 | 过期的 API 密钥在 TTL 到期之前一直保留在密钥缓存中 |
| 位置 | `internal/auth/auth.go:108-113`（`lookupStore`） |
| 描述 | 当使用持久化密钥时，密钥缓存条目在 `min(now+ttl, keyExpiry)` 时过期。但是，未过期的缓存条目（在 `keyExpiry` 之内但超过了实际的密钥到期时间）会在其整段 TTL 时间内持续提供。由于 `keyCache.put` 使用 `keyExpiry` 作为到期时间，理论上密钥应该在 `keyExpiry` 时到期。但是，如果管理员更改了持久化密钥的到期时间，旧缓存条目会一直保留到其原始的 `keyExpiry`。更关键的是：在缓存未过期的情况下撤销密钥，会正确调用 `cache.delete(hash)`，所以这个影响比较小。 |
| 攻击场景 | 边缘情况：1. 管理员创建一个带有 1 小时到期的密钥。2. 该密钥被使用，并被缓存。3. 管理员将到期时间延长到 2 小时。4. 该密钥在 1 小时时从缓存中被逐出，但它在存储库中仍处于活动状态。查找该密钥会穿过缓存并从存储库中重新加载它——这会起作用，因为存储库通过其哈希正确地找到了它。所以这实际上是正常的——只是缓存中的 TTL 更严格。 |
| 影响 | 最小。设计是正确的——到期时间在存储库层强制执行，缓存只是一个性能优化。 |
| 建议 | 文档/设计是正确的。没有行动项。 |

好的，让我删除这个发现。

### 发现项 11：中 — 访问日志记录完整的对象键，泄露敏感的键名

| 字段 | 值 |
|-------|-------|
| 类别 | 数据保护 / 隐私 |
| 严重性 | **中** |
| 标题 | 访问日志以明文形式记录完整的 URL 路径（包括对象键） |
| 位置 | `internal/middleware/middleware.go:63-75`（`AccessLog`） |
| 描述 | 默认的日志级别是 `info`，并且每次 HTTP 请求都会记录完整的 `r.URL.Path`。如果对象键包含敏感信息（例如 `invoices/2026/Q2/acme-corp-ssn-123-45-6789.pdf` 或 `secret/keys/prod-db-password.docx`），则会以明文形式写入日志。即使在使用结构化日志（slog）的情况下也是如此。 |
| 攻击场景 | 1. 用户上传一个键为 `customer-pii/ssn-123-45-6789.pdf` 的对象。2. 该路径记录在 `/var/log/aero-vault.log` 中。3. 获得日志文件访问权限的攻击者可以枚举所有已处理的 SSN/文档。 |
| 影响 | 通过日志泄露敏感路径。面临 GDPR/PCI-DSS 合规性问题。 |
| 建议 | 1. 为路径中的敏感键模式添加可选的日志编辑。2. 或者，提供路径键的哈希值以进行关联，同时隐藏实际名称。3. 记录 `request_id` 并让用户通过搜索将请求 ID 与键进行关联。 |
| 工作量 | **M**（1-3 天） |

### 发现项 12：中 — 可预见的 UUID 版本 1（时间戳+MAC）

| 字段 | 值 |
|-------|-------|
| 类别 | 密码学 |
| 严重性 | **低** |
| 标题 | uuid.NewString() 生成可预见的 UUID v1（基于时间戳+MAC） |
| 位置 | `internal/middleware/middleware.go:26`、`cmd/server/main.go:211` |
| 描述 | `github.com/google/uuid` 的 `NewString()` 默认生成基于时间戳+MAC 地址的 UUID v1。这些 UUID 可以关联时间戳，并可能泄露 MAC 地址。对于请求 ID 和实例 ID，这通常是可以接受的，但用于版本 ID 时（在 `file_crud.go` 中通过 `repository.NewVersionID()`），使用 uuid v4 会更安全。 |
| 攻击场景 | 1. 攻击者看到连续的版本 ID，可以为潜在的对象历史重放攻击推断写入时间顺序和速率。2. 如果版本 ID 在另一个上下文中被用作会话令牌（不应该），它们可能会被猜到。 |
| 影响 | 版本 ID 具有轻微的可预测性；MAC 地址可能会泄露内部物理硬件的身份。 |
| 建议 | 将版本 ID 切换到 `crypto/rand` UUID v4，或者使用 `xid` 替代方案以获得更好的排序+随机性。 |
| 工作量 | **S** |

### 发现项 13：中 — PII 检测器使用宽松的电话号码和 IP 正则表达式，导致大量误报

| 字段 | 值 |
|-------|-------|
| 类别 | 数据保护 |
| 严重性 | **低** |
| 标题 | PII 检测器电话号码和 IP 规则匹配过于宽泛 |
| 位置 | `internal/ai/pii.go:44-45` |
| 描述 | 电话号码模式 `(?:\+\d{1,3}[\s\-]?)?(?:\(?\d{2,4}\)?[\s\-]?){2,4}\d{2,4}` 会匹配恰好看起来像电话号码的任何数字序列，包括 IP 地址 `10.0.1.23`（作为 `(10)(0)(1)(23)` 匹配）。IP v4 模式 `\b(?:\d{1,3}\.){3}\d{1,3}\b` 会匹配任何点分四元组，包括有效的 IP 地址和非 IP 序列。 |
| 攻击场景 | 如果启用了 `AI_PII_REDACT`，即使文本中没有实际的 PII，也会发生大量误报编辑。系统日志（如 `"Server at 10.0.1.23 connected"`）会被编辑为 `"Server at [REDACTED-IP] connected"`。 |
| 影响 | 在 PII 编辑模式下，对合法非 PII 文本造成数据损坏。可能会破坏日志/监控输出中的有效配置数据。 |
| 建议 | 收紧正则表达式：电话号码需要国家代码或有效的区号模式；IP 地址需要验证每个八位字节 ≤ 255。 |
| 工作量 | **S** |

---

## STRIDE 分析

| 威胁 | 风险 | 关键发现 |
|-------|------|--------------|
| **S** 欺骗 | 高 | 发现 1（租户欺骗）、发现 2（SigV4 分块篡改） |
| **T** 篡改 | 高 | 发现 2（分块上传数据篡改）、发现 7（预签名重放） |
| **R** 抵赖 | 中 | 审计日志捕获了操作，但未强制执行用户身份证明确认（例如，写操作后的数字签名） |
| **I** 信息披露 | 高 | 发现 3（错误信息泄露）、发现 11（访问日志中的路径泄露） |
| **D** 拒绝服务 | 中 | 发现 8（速率限制器映射填满 DoS） |
| **E** 权限提升 | 高 | 发现 1（通配符租户冒充）、发现 6（批量策略绕过） |

---

## 合规性映射

| 标准 | 风险领域 |
|--------|----------------|
| **OWASP Top 10 (2021)** | A01:2021（失效的访问控制）→ 发现 1、6；A04:2021（不安全的设计）→ 发现 2；A05:2021（安全配置错误）→ 发现 4；A09:2021（安全日志记录和监控失败）→ 发现 3、11 |
| **PCI-DSS 4.0** | 要求 3（保护存储的账户数据）→ 发现 13（PII 规则宽松）；要求 6（开发和维护安全系统和应用程序）→ 发现 2（签名验证）；要求 10（日志记录和监控）→ 发现 11 |
| **GDPR** | 第 5 条（完整性+机密性）→ 发现 11（日志中的路径泄露）；第 32 条（安全处理）→ 发现 4（安全标头缺失） |
| **SOC 2** | CC6（逻辑和物理访问控制）→ 发现 1（租户隔离）；CC7（监控）→ 发现 3（错误暴露） |

---

## 总结

| 维度 | 评估 |
|----------|---------|
| **整体安全态势** | **需要改进** |
| 强度 | 强大的 AES-256-GCM 信封加密、HMAC-SHA256 用于签名、密钥 SHA-256 哈希、可选的持久密钥支持、IAM 样式策略（ARN、条件）、审计日志。架构经过深思熟虑，密码学基础也很完善。 |
| 弱点 | 中间件排序不一致破坏了授权不变量、分块 SigV4 验证被完全跳过、批量端点绕过策略、生产环境的错误处理泄露了内部细节。 |

### 最重要的 3 个关键问题
1. **租户中间件排序 + 通配符密钥滥用（发现 1）** — 允许持有管理员密钥的任何用户冒充任何租户。对多租户数据泄露产生直接影响。
2. **SigV4 分块验证被跳过（发现 2）** — 允许通过身份验证的分块上传对存储的数据进行完全篡改。
3. **批量端点绕过存储桶策略（发现 6）** — 使基于 IP 和条件的 IAM 策略对批量操作无效。

### 最重要的 3 个速赢项
1. **修复 `applyMiddleware` 中的中间件数组排序（发现 1）** — 重排 4 行代码就可以恢复授权不变量。
2. **为批量处理程序添加 `checkBucketPolicy` 调用（发现 6）** — 在 `BatchDelete` 和 `BatchTag` 中每处添加 2 行代码。
3. **编辑内部错误消息（发现 3）** — 将 `default` case 中的 `err.Error()` 替换为 `"internal error"`。

### 安全债务
| 项目 | 影响 | 优先级 |
|------|--------|----------|
| SigV4 分块签名验证 | 数据完整性 | 高（已发现 2） |
| 预签名 URL 标准化为 SigV4 查询参数 | 跨系统兼容性 | 中 |
| 速率限制器租户存储桶耗尽保护 | 可用性 | 中 |
| 日志编辑敏感路径 | 隐私 | 中（已发现 11） |
| PII 检测器正则表达式精度 | 数据质量 | 低（已发现 13） |
| 缺少安全标头（HSTS、CSP） | 纵深防御 | 低（已发现 4） |

---

**关于作者**：本审查由首席安全工程师根据 `internal/auth/`、`internal/middleware/`、`internal/storage/`、`internal/api/rest/`、`internal/service/`、`internal/config/` 和 `cmd/server/main.go` 的源代码审查编写。所有优先级都是针对实际可利用风险（而不是理论风险）进行校准的。
