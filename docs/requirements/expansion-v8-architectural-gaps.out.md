感谢您提供这份详尽的分析。我已经逐一验证了与代码库相关的所有关键代码锚点和声明。以下是已验证的评估：

---

## 验证结果

### ✅ Direction #2, #3, #4, #5 — 准确

| # | 声明 | 状态 |
|---|-------|--------|
| **2** | Postgres 连接池未调优 — `sql.Open("pgx", dsn)`，无 `SetMaxOpenConns`/`SetMaxIdleConns`/`SetConnMaxLifetime` | ✅ **确认** — `internal/repository/postgres.go:14` 仅调用了 `sql.Open` |
| **2** | `DBConfig` 缺少池配置字段 | ✅ **确认** — `internal/config/config.go:65-66` 只有 `Driver` + `DSN` |
| **2** | ListObjects 使用 keyset 分页（`WHERE key > ?`），无需 OFFSET 改造 | ⚠️ **轻微不准确** — ListObjects **已**使用 keyset 分页（`WHERE key > $4 ORDER BY key ASC LIMIT $5`，第 176 行）。但 ListVersions（第 344 行）仍使用 `OFFSET $5` — 分析应将此范围缩小到版本列举。 |
| **3** | Snapshot 仅支持 SQLite + local FS | ✅ **确认** — 第 20 行的注释写明："仅支持 SQLite 本地快照"；使用 `os.File`、`filepath.Walk` |
| **4** | PII 检测仅存在 AI 索引管线中，未在上传时触发 | ✅ **确认** — `pii.go` 中的检测器仅被 `internal/ai/indexer.go` 引用 |
| **4** | 无 DLP 策略引擎 | ✅ **确认** — 无 `classify/` 包，无 `DLPPolicy` 模型 |
| **4** | PUT 时无内容分类 | ✅ **确认** — `file_crud.go:Put` 第 81-200 行不含分类步骤 |
| **5** | WebDAV 使用内存锁 (`xwebdav.NewMemLS()`) | ✅ **确认** — `internal/api/webdav/dav.go:37` |
| **5** | REST/S3/MCP 不识别 WebDAV 锁 | ✅ **确认** — 未引用 WebDAV 的锁管理器 |
| **5** | 无分布式锁管理器 | ✅ **确认** — 不存在 `internal/lock/` 包 |

### 🔴 Direction #1 — 有重大不准确

分析文档声明 **"Policy 被完整地存储、返回，但从不执行"** 和 **"S3 handler 仅检查 auth scope + ACL"**。这**不正确**。

**实际状态：S3 handler DO 执行 Policy 检查**。

```go
// internal/api/s3compat/handler.go:45-62
func (h *Handler) checkBucketPolicy(w http.ResponseWriter, r *http.Request, bucket, action string) bool {
    cfg, err := h.svc.GetBucketConfig(r.Context(), ..., bucket)
    // ... 解析并执行 Policy ...
    if !auth.Allowed(p, action, host) {
        writeS3Error(w, r, service.ErrForbidden)
        return false
    }
    return true
}
```

该函数在 S3 handler 的以下位置被调用：
- **PutObject**（第 72 行，`"s3:PutObject"`）
- **GetObject**（第 125 行，`"s3:GetObject"`）
- **HeadObject**（第 217 行，`"s3:GetObject"`）
- **DeleteObject**（第 247 行，`"s3:DeleteObject"`）
- **BucketDispatch**（第 291 行，`"s3:ListBucket"`）

完整流程为：`S3 handler → checkBucketPolicy → auth.ParsePolicy → auth.Allowed → Authz，否则返回 403`。

**文档中不正确的断言：**

| 锚点 | 文档称 | 实际行为 |
|------|-----------|----------|
| `internal/api/s3compat/handler.go:GetObject` | "仅检查 auth scope + ACL" | 调用 `checkBucketPolicy(w, r, bucket, "s3:GetObject")` |
| `internal/api/s3compat/handler.go:PutObject` | "仅检查 auth scope + ACL" | 调用 `checkBucketPolicy(w, r, bucket, "s3:PutObject")` |
| "Policy 被完整存储，但从不执行" | 从未执行 | S3 handler 确实执行 |

**文档「即使 API Key 有 admin scope，桶 Policy Deny 也要优先」的澄清：** 这**已经**是 S3 handler 的工作方式——`checkBucketPolicy` 在 auth 中间件之后运行，Deny 会覆盖 Allow。

---

## 已修正的 Direction #1 评估

方向仍然有效且有价值，但其范围需要重新界定：

### 真正缺失的部分

| 路径 | 是否执行 Policy | 风险 |
|------|----------------|------|
| **S3 handler** (`/s3/*`) | ✅ 是 — `checkBucketPolicy` | 无 |
| **REST handler** (`/v1/files/*`) | ❌ 否 — 无 Policy 检查 | 🔴 高 — 通过 REST 绕过 S3 Policy |
| **WebDAV** | ❌ 否 — 无 Policy 检查 | 🔴 中 — 通过 WebDAV 绕过 |
| **MCP** | ❌ 否 — 无 Policy 检查 | 🟠 中 — 通过 MCP 绕过 |
| **FileService 层** (`Put`/`Get`/`Delete`) | ❌ 否 — 无 Policy 检查 | 🔴 高 — 所有路径的后端入口 |

### 建议的架构方向（已修正）

不是新增 middleware，而是**将 `checkBucketPolicy` 逻辑从 S3 handler 下沉到 FileService 层**：

```
当前: S3 handler → checkBucketPolicy() → FileService.Put()
        REST handler ---------------------→ FileService.Put()  ← 绕过策略!

提议: FileService.Put() → checkBucketPolicy() → 存储写入
          ↑                    ↑
       所有协议             单一策略执行点
```

这确保了 **一个桶不能通过 REST 绕过 S3 Policy 写入**。同时消除了文档中建议的缓存层（在 FileService 层面，每个请求已从 S3 handler 的 `GetBucketConfig` 中持有 `BucketConfig`，该配置在 `Put` 中已被调用）。

### 修正后的工作量估算

| 影响面 | 工作量 | 变化原因 |
|--------|--------|--------------|
| 将 `checkBucketPolicy` 移入 FileService | 低（~30 行） | 逻辑已存在于「仓库 - 策略」代码路径中；只需将其从 S3 handler 移至 service 层 |
| 从 S3 handler 中移除重复检查 | 低（~20 行删除） | 一旦放入 FileService，移除 S3 handler 前面的包装调用 |
| 向 REST/WebDAV/MCP 暴露 action 注入 | 低（每个 handler 1 行上下文） | 无需全部 20+ 个 handler |
| 移除中间件/缓存层 | 无 | 不再需要，因为 `GetBucketConfig` 已在 FileService 路径中 |

**不再需要**：中间件（~100 行）、缓存层、20 个 handler 改动。

---

## 总结

**Direction #1** 仍然有价值且是最高优先级，但其范围应重新聚焦为：*将策略评估从 S3 handler 下沉至 FileService 层*（使所有协议路径统一强制执行），而非*新增中间件*（因为 S3 handler 已能执行）。

其余四个方向的分析与审计结果完全一致。总体而言，这份质量很高——这是唯一需要更正的事实性错误。
