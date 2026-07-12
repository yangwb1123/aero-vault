审阅已完成。文件写入 `docs/requirements/expansion-v32-architect-frontiers.out.md`（295 行）。

---

## 审阅核心发现

逐条验证了 v32 所有 5 个方向的实际代码状态：

### ✅ 方向 1-4：分析质量高

| 方向 | 验证结论 |
|------|---------|
| **1. Block-Level Dedup** | 完全准确。Storage 接口无块级操作，`Object` 无 `BlockRefs`，零 CAS 基础设施。**补充：** 建议增加 P0 前置步骤（对象级 CAS + `_aero_content_sha256`） |
| **2. HNS** | 完全准确。WebDAV Mkdir 是 `return nil`，Rename 是 copy-then-delete，无 Directory 实体。**补充：** 建议增加 P0 目录标记对象（零 schema 变更） |
| **3. Batch Operations** | 方向正确，但有一处错误：REST **已有** `POST /v1/batch/delete` 和 `/batch/tag` 路由（`router.go:99-100`），分析声称"无 batch 路由"不准确 |
| **4. mTLS** | 完全准确。零 TLS 配置，`ListenAndServe()` 纯 HTTP，auth 包零证书代码。**补充：** 建议增加 Phase 0（反向代理 header 提取方案） |

### 🔴 方向 5（Object Lock）：多处事实错误

| 分析声称 | 实际代码 |
|---------|---------|
| "Legal Hold 完全缺失" | ❌ **已存在**：`handler.go:93-98` 解析 `x-amz-object-lock-legal-hold`→`_aero_legal_hold` metadata；`file_crud.go:301-302` hard delete 检查 |
| "`?object-lock` 完全缺失" | ❌ **已实现**：`bucketconfig.go:169-198`（get/put）+ `xml.go:231-236`（XML struct），硬编码 GOVERNANCE 模式 |
| "三个子资源 `?legal-hold`、`?retention`、`?object-lock` 完全缺失" | ❌ 混为一谈——`?object-lock` 已实现，缺的是 per-object 的 `?legal-hold` 和 `?retention` |

**修正后该方向的正确描述：** 元数据级 Legal Hold 已存在，桶级 `?object-lock` 已实现。真正缺口是：per-object 子资源（`?legal-hold`、`?retention`）、Compliance 模式、`BypassGovernanceRetention` 权限、版本级锁定。

### 快速取胜建议

1. **$P0-bug 修复：** `checkLockBeforeOverwrite` 缺少 `_aero_legal_hold` 检查——legal-held 对象在非版本化桶上可被覆盖
2. **Phase 0 mTLS：** 编写 `middleware/cert.go` 提取 `X-Forwarded-Client-Cert` header
3. **WebDAV 目录：** 实现目录标记对象（`PUT 0-byte .aero_dir`）——零 schema 迁移，解决 Mkdir 空操作
