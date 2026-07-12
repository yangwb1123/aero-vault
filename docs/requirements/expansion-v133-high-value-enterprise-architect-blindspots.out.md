## 验证报告：已读取全文，已交叉校验代码

我已完整阅读该文档，并与实际代码库进行了交叉验证。以下是关键发现。

---

### ✅ 文档核心方向概览

| # | 方向 | 锚定代码匹配 | 实际验证结论 |
|---|------|------------|-------------|
| 1 | **数据主权路由** | `buildStorage` 单实例 ✅ | **确认** — 单一后端，无租户级路由 |
| 2 | **不可变审计轨迹** | `RecordAudit` → `audit_log` 表 ✅ | **确认** — 同库存储，无哈希链，无签名 |
| 3 | **多后端数据分层** | `StorageClass` 仅元数据 ✅ | **确认** — 无 transition 动作，无分层路由 |
| 4 | **四协议统一授权** | ⚠️ **部分不准确** | **见下方详细分析** |
| 5 | **参数自调优** | 静态配置 ✅ | **确认** — 所有参数 env-only，无反馈回路 |

---

### ⚠️ 方向四的关键事实修正

文档声称：

> ❌ REST handler 不检查桶 Policy

但代码库实际状态 **与此相反**：

| 协议 | 文档声称 | 实际代码 | 验证 |
|------|---------|---------|------|
| **REST** | ❌ 不检查桶 Policy | `internal/api/rest/handler.go:46` — 有 `checkBucketPolicy`，在 PUT/GET/DELETE/LIST 均调用 | ✅ **已有，文档过时** |
| **S3** | ✅ 有桶 Policy 检查 | `internal/api/s3compat/handler.go:48` — `checkBucketPolicy` 含 `bucket` 参数 | ✅ 一致 |
| **WebDAV** | ❌ 无认证/授权 | `internal/api/webdav/` — 零 auth 引用 | ✅ 一致 |
| **MCP** | ⚠️ 无操作级授权 | `internal/mcp/server.go:373` — 仅有 tenant 校验，无 Policy | ✅ 基本一致 |

**差异细节：** REST handler 有 `checkBucketPolicy`，但签名是 `(w, r, action)` 而非 S3 的 `(w, r, bucket, action)`。REST 侧硬编码了 `service.DefaultBucket`，意味着 **多桶场景下 REST handler 的 Policy 检查使用固定桶名**——这确实是个问题，但方向与文档所述不同。

---

### 📋 代码级深度验证结果

**正确识别的架构盲区：**
- **方向一** ✅ `buildStorage` (main.go:402) 返回单实例 → 确认 `StorageRouter` 确实缺失
- **方向一** ✅ `storageKey(tenant, bucket, key)` (file_crud.go) 不区分后端 → 确认路由缺失
- **方向二** ✅ `RecordAudit` (audit.go) 直接写入同 DB → 确认无哈希链/签名/双写
- **方向二** ✅ `ListAudit` (audit.go) 无限制分页 → 确认无导出机制
- **方向三** ✅ `StorageClass` (file.go:169) 仅元数据 → 确认无后端映射
- **方向三** ✅ Lifecycle (lifecycle.go) 仅 `"soft_delete"` / `"hard_delete"` → 确认无 transition
- **方向四** ✅ WebDAV 无认证/授权 → 已确认
- **方向四** ✅ MCP 无授权 → 已确认（仅 tenant 校验）
- **方向五** ✅ 所有配置 env-only → 确认无运行时热更新

---

### 💡 建议：如果您希望推进实施

该文档已正确保存为：

```
docs/requirements/expansion-v133-high-value-enterprise-architect-blindspots.md
```

根据 `AGENTS.md` 的约束（重构优先、测试优先、`make check` 门禁），以及该文档建议的 Phase 优先级，我可以：

1. **方向四（统一授权）** — 从 REST handler 的 `checkBucketPolicy` 提取为 FileService 层的通用 `Authorizer`，消除 REST（多桶场景）和 S3/WebDAV/MCP 之间的 Policy 评估差异。这是工作量最小、影响最大的入口。

2. **方向二（不可变审计）** — 新增 `internal/audit/` 包，在 `RecordAudit` 中追加哈希链，新增 `prev_hash`/`signature` 列迁移文件。

3. **方向五（自调优最小闭环）** — 从 `internal/autotune/` 框架 + 自适应 chunk window 开始。

请告知您希望从哪个方向开始实施，或是否需要对文档进行进一步修正后再启动开发。
