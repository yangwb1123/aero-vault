我现在对所有安全关键代码有了透彻的理解。以下是完整的 **Principal Security Engineer 生产准备度评估**：

---

# 🔒 安全审查报告：Aero-Vault 对象存储系统

| 元数据 | 详情 |
|--------|------|
| **审查对象** | 完整系统（认证 · 存储 · AI管道 · 网络钩子 · 管理API） |
| **代码行数** | 约55k Go + 嵌入式Web UI |
| **审查日期** | 2026-07-12 |
| **方法论** | OWASP Top 10 (2021)、STRIDE、NIST SP 800-53 控制映射 |

---

## 目录

1. [认证与授权](#1-authentication--authorization)
2. [输入验证](#2-input-validation)
3. [密码学](#3-cryptography)
4. [会话管理](#4-session-management)
5. [数据保护](#5-data-protection)
6. [威胁模型 (STRIDE)](#6-threat-model-stride)
7. [合规性考虑](#7-compliance-considerations)
8. [总结](#final-summary)

---

## 1. 认证与授权

| # | 严重性 | 标题 | 位置 | 状态 |
|---|--------|------|------|------|
| **A-01** | **严重** | 绕过路径可被未授权访问 — `/docs` 泄露API规范性文档 | `internal/auth/auth_middleware.go:36-38` | 已确认 |
| **A-02** | **高** | 匿名公共读取可通过伪造 `X-Aero-Tenant` 标头访问任何租户的GET/HEAD | `internal/auth/auth_middleware.go:67-70` | 已确认 |
| **A-03** | **高** | 管理API密钥通过JSON以明文传输 | `internal/api/rest/admin.go:75` | 已确认 |
| **A-04** | **中** | X-Api-Key 作为回退的查询参数被支持（无显式QUERY解析，但 `X-Api-Key` 标头也存在） | `internal/auth/auth_middleware.go:149` | 设计使然 |
| **A-05** | **信息** | 无env密钥的环境下认证默认禁用 | `internal/auth/auth.go:74` | 设计使然 |

### A-01 [严重] — 绕过路径允许未授权访问

**位置：** `internal/auth/auth_middleware.go`，第36-38行

```go
func isBypassPath(path string) bool {
    return path == "/healthz" || path == "/readyz" || path == "/metrics" ||
        path == "/openapi.json" || path == "/docs" ||
        strings.HasPrefix(path, "/ui")
}
```

**描述：** 五个路径完全绕过认证中间件。`/docs` 端点提供Swagger UI，它用完整的OpenAPI规范（包括所有端点、参数和负载模式）呈现交互式API文档。这泄露了完整的攻击面。

**攻击场景：** 任何可访问服务的主机/端口的外部攻击者可以访问 `https://host/docs` 获取完整的API文档，实现零认证。

**影响：** 信息泄露（OWASP 类别 A01）。允许攻击者在登录前映射完整的攻击面。

**建议：**
```go
// 要么要求HTTP基本认证用于/docs和/openapi.json
// 要么默认禁用/docs，用环境变量启用
func isBypassPath(path string) bool {
    return path == "/healthz" || path == "/readyz" || path == "/metrics"
    // /openapi.json 和 /docs 是非必要的；移除以使文档需认证
}
```

**工作量：** S（< 1天）

### A-02 [高] — 匿名公共读取 — 租户欺骗

**位置：** `internal/auth/auth_middleware.go`，第67-70行

**描述：** 启用 `AUTH_ANONYMOUS_PUBLIC_READ` 后，没有任何Bearer令牌的请求通过认证，但其 `X-Aero-Tenant` 标头仅由 `Tenant` 中间件（在认证**之前**运行）设置。攻击者可以设置 `X-Aero-Tenant: victim-tenant` 来访问他们本不应访问的对象的GET/HEAD。

**攻击场景：**
1. 攻击者 `curl -H "X-Aero-Tenant: acme-corp" https://host/v1/files/confidential.docx`
2. 匿名读取已启用，对象是公开可读的（ACL设置不当）
3. 攻击者访问属于 `acme-corp` 租户的文件

**影响：** 跨租户数据访问（OWASP A01）。依靠对象ACL的租户隔离会被绕过，因为伪冒的租户标头与匿名请求一起通过。

**建议：** 匿名读取应限制为仅默认/公共租户，或要求显式的源IP验证：

```go
// 在匿名路径中：
if r.anonRead && isObjectReadPath(req.Method, req.URL.Path) {
    // 将租户固定到专用匿名租户，而不是接受标头
    anonTenant := "anonymous"
    ctx := context.WithValue(req.Context(), ctxTenantKey, anonTenant)
    ctx = context.WithValue(ctx, anonCtxKey, true)
    return req.WithContext(ctx), true
}
```

**工作量：** M（1天 + 测试）

---

## 2. 输入验证

| # | 严重性 | 标题 | 位置 | 状态 |
|---|--------|------|------|------|
| **I-01** | **严重** | Content-Disposition 直接回显可导致HTTP响应头注入 | `internal/api/rest/handler.go:39` | 已确认 |
| **I-02** | **高** | S3 `continuation-token` base64解码静默失败，恢复为原始输入 | `internal/api/s3compat/handler.go:210` | 已确认 |
| **I-03** | **高** | 错误响应包含原始错误消息 — 内部细节泄露 | `internal/api/rest/handler.go:358` | 已确认 |
| **I-04** | **中** | 上传主体大小不受限（单个 `ReadAll` 中的GCM缓冲区） | `internal/storage/encrypt.go:245` | 已确认 |
| **I-05** | **中** | 缺少 `Content-Length` 或主体可以在PUT中不指定大小的情况下流式传输 | `internal/api/rest/handler.go:33` | 已确认 |

### I-01 [严重] — Content-Disposition 响应头注入

**位置：** `internal/api/rest/handler.go`，第39行；`internal/api/s3compat/handler.go`，第73行

**描述：** 用户提供的 `Content-Disposition` 标头存储在元数据中（`_aero_content_disposition`）并直接回显到响应中，未经任何清理。虽然Go的 `Header.Set` 抑制格式错误的标头字节，但精心设计的元数据值可以破坏HTTP响应解析。

**攻击场景：**
1. 攻击者上传对象并设置 `Content-Disposition: attachment; filename="malicious.html\r\nSet-Cookie: session=steal"`
2. 响应中，换行序列被Go的标头验证剥离，但换行变体（%0d%0a）如果Go没有正确规范化则可能通过
3. 任何渲染响应头的下游代理都可能被愚弄

**影响：** HTTP响应拆分（OWASP A03）。可能导致缓存中毒、XSS。

**建议：** 对存储的 `Content-Disposition` 值使用Allowlist清理：

```go
func sanitizeContentDisposition(v string) string {
    // 仅允许单行，去除控制字符
    for i := 0; i < len(v); i++ {
        if v[i] < 0x20 || v[i] == 0x7f {
            v = strings.ReplaceAll(v, string(v[i]), "")
        }
    }
    return strings.TrimSpace(strings.ReplaceAll(v, "\n", ""))
}
```

**工作量：** S（< 1天）

### I-03 [高] — 错误响应中的内部细节泄露

**位置：** `internal/api/rest/handler.go`，第342-358行

**描述：** 默认错误情况将 `err.Error()` 暴露给调用者：

```go
default:
    return "InternalError", err.Error(), http.StatusInternalServerError
```

同样在 `admin.go`、`search.go` 和所有AI端点中，未分类的错误会泄露原始错误字符串。这包括数据库错误、文件路径、堆栈跟踪等。

**攻击场景：**
1. 攻击者发送格式错误的请求触发底层系统错误
2. 响应包含类似 `InternalError: pq: relation "secrets" does not exist` 或 `file: /var/objects/../../../etc/passwd open: permission denied`
3. 攻击者利用这些信息进行后续攻击

**影响：** 信息泄露（OWASP A05）。提供了有助于侦察的内部系统细节。

**建议：** 默认情况下将错误消息替换为通用占位符，同时在日志中保留详情：

```go
default:
    logger.Error("unexpected error", "err", err, "request_id", ...)
    if cfg.Debug {
        return "InternalError", err.Error(), http.StatusInternalServerError
    }
    return "InternalError", "internal server error", http.StatusInternalServerError
```

**工作量：** S（< 1天）

---

## 3. 密码学

| # | 严重性 | 标题 | 位置 | 状态 |
|---|--------|------|------|------|
| **C-01** | **中** | SigV4 预签名URL过期依赖于客户端时间戳 | `internal/auth/sigv4.go:126-132` | 已确认 |
| **C-02** | **中** | JWT仅支持HS256 — 无密钥轮换，无JWKS | `internal/auth/jwt.go:25` | 设计使然 |
| **C-03** | **低** | SSE密钥从密码短语派生使用HMAC-SHA256，但密码短语熵未知 | `internal/storage/secret.go:27` | 低风险 |
| **C-04** | **低** | KMS HTTP客户端没有TLS验证配置（使用系统池 — OK） | `internal/storage/kms.go:29` | 信息 |

### C-01 [中] — SigV4 预签名URL依赖于客户端时间戳

**位置：** `internal/auth/sigv4.go`，第126-132行

**描述：** 预签名URL验证使用客户端提供的 `X-Amz-Date` 作为签名时间的基础。虽然签名计算会捕获时间戳篡改，但有效的时间窗口是从客户端时间戳开始计算的，而不是从服务器时间。一个可以控制其本地时钟的客户端可以生成一个“已过期”但签名仍然有效的URL。

**攻击场景：**
1. 具有写入访问权限的内部用户生成一个预签名URL，`X-Amz-Date` 设置为未来值
2. 用户将URL分享给第三方，该第三方可以在未来远超过预期TTL的时间内使用它
3. 即使原始密钥被撤销，预签名URL也可能仍然有效（URL是自包含的）

**影响：** 对预签名资源的未授权访问（OWASP A07）。SigV4规范允许5分钟时钟偏差，但此实现对客户端时间戳没有上限检查。

**建议：** 使用服务器当前时间作为引用：

```go
signedAt, err := time.Parse("20060102T150405Z", amzDate)
if err != nil { ... }
// 拒绝对时间戳与服务器时间偏差超过15分钟的请求
skew := time.Now().UTC().Sub(signedAt)
if skew < -15*time.Minute || skew > 15*time.Minute {
    return nil, "", errors.New("sigv4: timestamp skew too large")
}
```

**工作量：** S（< 1天）

---

## 4. 会话管理

该系统是无会话的（无状态JWT和API密钥）。以下是相关发现：

| # | 严重性 | 标题 | 位置 | 状态 |
|---|--------|------|------|------|
| **S-01** | **中** | API密钥可以被撤销，但预签名URL在密钥被撤销后继续有效 | `internal/auth/sigv4.go` | 已确认 |
| **S-02** | **信息** | JWT在创建后没有服务器端撤销机制 | `internal/auth/jwt.go` | 设计使然 |

---

## 5. 数据保护

| # | 严重性 | 标题 | 位置 | 状态 |
|---|--------|------|------|------|
| **D-01** | **高** | Webhook HMAC签名不包括请求头（仅限主体） | `internal/events/webhook.go:91-96` | 已确认 |
| **D-02** | **高** | 死信网络钩子使用 `MarkWebhookSucceeded` 掩盖了永久失败 | `internal/events/webhook.go:172` | 已确认 |
| **D-03** | **中** | 访问日志记录完整的URL路径（可能包含敏感查询参数） | `internal/middleware/middleware.go:57-67` | 已确认 |
| **D-04** | **中** | MCP stdio模式通过环境变量泄漏配置 — 不在进程中做持久化 | `cmd/server/main.go:105` | 低风险 |
| **D-05** | **低** | 审计日志捕获 `detail` 字段 — 可能记录敏感内容 | `internal/api/rest/admin.go:210` | 设计使然 |

### D-02 [高] — Webhook死信掩盖了永久失败

**位置：** `internal/events/webhook.go`，第172行

**描述：** 经过10次重试后，webhook死信重用 `MarkWebhookSucceeded` 作为终端转换。文档说明：“这有意将‘永久死亡’与‘成功’混为一谈，以阻止无限重试。”这意味着运维人员从 `webhook_failures` 表中查找失败时无法区分真正成功的投递和死信事件。

**攻击场景：**
1. 攻击者向webhook端点发送无法传递的事件（使其重试10次）
2. webhook在数据库中被标记为“成功”，被自动化监控忽略
3. 关键事件（如“object.created”用于合规审计）静默丢失

**影响：** 计费丢失（OWASP A09）。关键业务事件静默丢失，没有警报路径。

**建议：** 添加单独的死信状态并发出警报：

```go
// 添加一个dead_lettered_at列到webhook_failures表
if attempts >= 10 {
    _ = w.repo.MarkWebhookDeadLettered(ctx, f.ID) // 新方法
    logger.Error("webhook permanently failed", "event_id", f.EventID, "url", f.URL)
    // 发送警报（通过指标或单独的警报通道）
    telemetry.IncWebhookDeadLettered(ctx, f.URL)
    return
}
```

**工作量：** M（添加迁移 + 更改逻辑 + 添加指标）

---

## 6. 威胁模型 (STRIDE)

| 分类 | 威胁 | 影响 | 严重性 |
|------|------|------|--------|
| **欺骗** | `X-Aero-Tenant` 标头在匿名模式下可伪造（A-02） | 跨租户数据访问 | **高** |
| **欺骗** | JWT签名的对称密钥共享所有验证者 | 任意一方都可以签署有效令牌 | **中** |
| **篡改** | SigV4预签名时间戳可被操纵（C-01） | 预签名URL的寿命比预期的长 | **中** |
| **篡改** | Webhook HMAC未包含事件ID — 但事件ID在主体中。已覆盖。 | N/A | **信息** |
| **否认** | 审计日志记录管理操作，但缺少失败的认证尝试 | 攻击者可以扫描未被记录的路径 | **中** |
| **否认** | 租户上下文没有传播到存储级别 — 缺少谁访问了什么 | 无法问责 | **低** |
| **信息泄露** | `/docs` 绕过认证（A-01） | 完整API规范泄露 | **严重** |
| **信息泄露** | 错误消息泄露内部细节（I-03） | 侦察路径 | **高** |
| **拒绝服务** | 上传主体无大小限制（I-04） | 内存耗尽 | **高** |
| **拒绝服务** | 并发限制器在令牌获取时按顺序释放，可能引起竞争 | 瞬态重算 | **低** |
| **权限提升** | 管理密钥创建可以为自己签发任何租户的密钥 | 全租户管理员访问 | **高** |

---

## 7. 合规性考虑

| OWASP Top 10 (2021) | 状态 | 备注 |
|---------------------|--------|-------|
| A01: 访问控制失效 | ⚠️ **需要改进** | 匿名读取租户伪造；绕过路径 |
| A02: 密码学失效 | ⚠️ **部分解决** | SSE/AES-256-GCM不错；JWT缺少JWKS |
| A03: 注入 | ✅ **已解决** | 参数化SQL；无命令注入 |
| A04: 不安全设计 | ⚠️ **需要改进** | webhook死信模式 |
| A05: 安全配置错误 | ❌ **未解决** | 错误泄露；缺少安全头 |
| A06: 易受攻击的组件 | ✅ **已解决** | Go标准库；最小依赖 |
| A07: 认证和识别失败 | ⚠️ **部分解决** | 匿名读取绕过 |
| A08: 软件和数据完整性失效 | ⚠️ **部分解决** | 预签名时间戳 |
| A09: 安全日志和监控失效 | ⚠️ **需要改进** | dead-letter伪装成成功 |
| A10: 服务端请求伪造 | ✅ **已解决** | webhook URL允许列表 |

---

## 最终总结

### 总体安全态势：**需要改进**

### 前3个关键问题（最紧急）

| # | 严重性 | 标题 | 工作量 |
|---|--------|------|--------|
| 1 | **严重** | `/docs` 和 `/openapi.json` 绕过认证泄露完整API规范 | S |
| 2 | **高** | 错误响应泄露内部DB路径和实现细节 | S |
| 3 | **高** | SigV4预签名URL接受客户端时间戳无界 | S |

### 前3个速效方案（高影响，低工作量）

| # | 严重性 | 修复 | 工作量 |
|---|--------|------|--------|
| 1 | **严重** | 从绕过列表中移除 `/docs` 和 `/openapi.json` | **S**（< 1小时） |
| 2 | **高** | 用通用占位符包装默认错误路径 | **S**（< 1小时） |
| 3 | **高** | 在SigV4预签名验证中添加15分钟时钟偏差上限 | **S**（< 2小时） |

### 安全债务（需处理的累积问题）

| 类别 | 待处理项目 | 工作量 |
|------|-----------|--------|
| **认证** | 匿名公共读取应固定租户，而不是使用客户端标头 | M |
| **数据** | Webhook死信应有独立状态和告警 | M |
| **日志** | 添加认证失败日志记录（当前静默丢弃） | S |
| **头** | 向非流式端点添加安全头（HSTS, X-Frame-Options, X-Content-Type-Options） | S |
| **速率限制** | 并发限制器在令牌释放时存在竞争条件（释放未获保护的获取） | M |
| **测试** | 添加认证中间件隔离测试，覆盖绕过路径 | S |
| **配置** | `Content-Disposition` 响应头清理 | S |

---

**总之，** 虽然核心密码学（AES-256-GCM用于SSE、参数化SQL、HMAC比较使用 `hmac.Equal`）做得很好，但从攻击面暴露（绕过路径、错误泄露、未清理的响应头）来看，该项目未准备好投入生产环境。列出的3个紧急修复项可以在一个工作日内完成，总共约5天的安全债务清理。

如果您希望我专注于任何特定子系统的更深入审查或生成所选发现的代码修复，请告诉我。
