All key code anchors verified. Here is my assessment.

---

## 验证结果：5 方向全部确认，3 处文档修正、1 项严重程度升级

### 核心发现矩阵

| # | 方向 | 代码锚点验证 | 文档准确度 | 关键修正 |
|---|------|------------|-----------|---------|
| 1 | Tenant Header 零输入验证 | ✅ 全部6/6锚点确认 | **准确** | 追加 1 个消费点：S3 SigV4 认证中自动覆写 `X-Aero-Tenant` 头 |
| 2 | IO 流 Context 取消 | ✅ 全部5/5锚点确认 | **准确** | 追加范围：搜索到 **18 处** `io.Copy` 非测试调用，远超文档所列 5 处 |
| 3 | WebUI 认证 | ✅ 全部4/4锚点确认 | ⚠️ **需修正** | `/ui` 是 **显式绕过 auth**（`isBypassPath` + `strings.HasPrefix(path, "/ui")`），非"假设只有友好方访问"——严重程度升级为 **P1** |
| 4 | Multipart 内存易失性 | ✅ 全部5/5锚点确认 | **准确** | — |
| 5 | SSE 密钥环热加载缺失 | ✅ 全部4/4锚点确认 | **准确** | — |

---

### 修正 1（严重）：方向三 — `/ui` 不是"未受 auth 保护"，而是**显式 bypass**

文档原文状态为"待验证"，实际代码证据确凿：

```go
// internal/auth/auth_middleware.go:37-40
func isBypassPath(path string) bool {
    return path == "/healthz" || path == "/readyz" || path == "/metrics" ||
        path == "/openapi.json" || path == "/docs" ||
        strings.HasPrefix(path, "/ui")   // ← 显式绕过所有 auth
}
```

这意味着：
- `/ui` 不仅仅是没有经过 auth —— 它是**被刻意排除在认证之外**的
- `/ui/index.html`、`/ui/app.js`、`/ui/style.css` **所有**静态资源对任何网络可达者完全开放
- 与 `/healthz`、`/metrics` 同等级别的公开端点，但后两者不暴露前端路由结构/API 端点模式/SDK 使用方式
- 攻击者直接 `curl http://host/ui/` 即可获得完整 SPA，分析所有前端逻辑

**建议将方向三优先级从 P2 升级为 P1。**

---

### 修正 2：方向二 — IO Copy 影响面远超文档所列

文档列举 5 处热点。全库扫描发现 **18 处 `io.Copy` / `io.CopyN` 非测试调用**：

```
internal/storage/local_write.go:60          # PUT 上传体写入
internal/storage/local_multipart.go:46      # UploadPart 分片写入
internal/storage/local_multipart.go:182     # CompleteMultipart 合并
internal/ai/extractor.go:55                 # 文档提取
internal/ai/extractor_remote.go:75          # 远程文档提取
internal/antivirus/worker.go:113            # AV 扫描
internal/reconcile/scrub.go:84              # Scrub/GC 路径
internal/api/s3compat/handler.go:152        # S3 GET
internal/api/s3compat/handler.go:197        # S3 GET (range)
internal/api/s3compat/handler.go:211        # S3 GET
internal/api/webdav/spill.go:155            # WebDAV
internal/api/rest/handler.go:187,211        # REST GET
internal/api/rest/management.go:113         # 管理 API
internal/api/rest/idempotency.go:257        # 幂等性 body 缓冲
internal/snapshot/snapshot.go:168,181       # 快照
internal/service/range.go:122               # Range skip（文档已列）
```

其中**最关键的新发现**是 `internal/antivirus/worker.go:113` 和 `internal/reconcile/scrub.go:84`——后台 worker 路径的 `io.Copy` 不感知 ctx 意味着**关闭信号发出后，后台 goroutine 仍可继续运行数十分钟**，阻碍优雅关停。

---

### 修正 3：方向一 — 追加 S3 SigV4 路径的 Tenant 自动覆写

文档未覆盖的一个 Tenant 消费点：

```go
// internal/auth/auth_middleware.go:71
if k.Tenant != "*" {
    req.Header.Set("X-Aero-Tenant", k.Tenant)  // 自动覆写 Header
}
```

S3 SigV4 认证通过后，系统用 key 绑定的 tenant **覆写请求头中的 tenant 值**。这意味着：

| 场景 | 效果 |
|------|------|
| 用户发送 `X-Aero-Tenant: admin` + S3 SigV4 key (tenant=`acme`) | 中间件**覆写**为 `acme` → user 的注入尝试被静默覆盖 |
| 用户发送 `X-Aero-Tenant: admin` + Bearer key (tenant=`acme`) | **拒绝**：tenant mismatch → `403 Forbidden` |

SigV4 路径有**隐式保护**（覆写机制自动修正恶意 tenant），Bearer 路径**拒绝而非静默修正**。这个差异意味着：方向一的注入面在 SigV4 路径上被部分缓解，但 Bearer 路径和无认证路径（匿名读）仍然裸露。

---

### 追加观察：方向三的深入风险

除了 `isBypassPath` 显式绕过外，还有一个隐含风险：

```go
// internal/auth/auth_middleware.go:63
if r.anonRead && isObjectReadPath(req.Method, req.URL.Path) {
```

匿名读模式（`r.anonRead`）下的对象读取绕过所有 auth。如果 `/ui` 下的静态资源**恰好**匹配 `isObjectReadPath` 的判断逻辑，会产生双重绕过。建议在实现 WebUI 认证前，先移除 `isBypassPath` 中 `/ui` 的例外，改为在 `webui.Handler()` 内部处理匿名访问的逻辑。

---

### 总体评估

| 维度 | 评分 | 说明 |
|------|------|------|
| 代码锚点准确性 | ⭐⭐⭐⭐⭐ | 全部代码引用准确，无一处引用不存在或偏移的代码行 |
| 影响分析深度 | ⭐⭐⭐⭐ | 方向二量化了浪费上限，方向一覆盖了 6 个消费点和具体异常值 |
| 既有覆盖去重 | ⭐⭐⭐⭐⭐ | 5 方向中 3 个完全零覆盖，1 个仅一行注记，1 个浅覆盖 |
| 架构方案完整性 | ⭐⭐⭐⭐ | 方向二缺"SSE 流需独立实现"的代码引用，方向三缺 `isBypassPath` 的具体代码定位 |

**总评：** 这是一份高质量、经过深度代码扫描且去重验证的扩展方向分析。最需要修正的是方向三的严重程度（从 P2 升级为 P1）和方向二的影响面扩大（18 处而非 5 处）。方向四和五的文档与代码一致，无重大偏差。
