# AeroVault 高价值扩展方向（第十八期）

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全局代码扫描（300+ Go/HTML/JS/Python 源码文件，~75K 行），逐包审阅 `internal/` 全部子包、`cmd/server/main.go`、配置系统、SDK 三套（Go/Python/JS）、Web UI、CLI、全部 24 对迁移文件、Helm chart、Grafana/Prometheus 配置。逐一比对前十七期 expansion 文档（`expansion-directions.md` ~ `expansion-v17-production-gaps.md`，累积约 1MB+ 分析）、`ROADMAP.md`（10 方向）、`CHANGELOG.md`、`TODO.md`，确认每个方向在**既有文档中零覆盖或仅行级提及，且不属于任何已列出的方向范畴**。  
> **日期：** 2026-07-10  
> **原则：** 选取 5 个**用户采纳 / 开发者体验 / 运维就绪**方向——不是核心架构缺失（v1–v17 已覆盖 ~85 个方向），而是**"从技术项目到商业产品的最后一公里"**——开发者如何上手、非技术用户如何管理、现有客户如何迁移、定制化需求如何满足、运维人员如何容量规划。每个方向附带：代码锚点 → 当前状态 → 缺失能力 → 边界情况 → 架构概要 → 实现理由。**不编写任何实现代码。**

---

## 审阅背景：前十七期覆盖的去重矩阵

前十七期（v1–v17）已从约 17 个视角覆盖约 85 个方向。以下大类已深度覆盖，**本期不再重复**：

| 领域 | 覆盖期数 | 方向数 |
|------|---------|--------|
| AI/RAG 管线（Embed/Chunk/Search/Chat/Agent/Rerank/PII/Indexer/Cache） | v1~v13 | ~12 |
| S3 兼容性（子资源/ACL/Policy/CORS/Logging/Notification/Multipart/Batch） | v1, v4, v6, v8~v10, v16, v17, ROADMAP #7 | ~10 |
| 存储后端（S3/OSS/COS/KMS/SSE/Encryption/CircuitBreaker/Multi-Backend） | v4~v15, ROADMAP #5 | ~7 |
| 认证授权（JWT/API Key/SigV4/OIDC/SAML/SCIM/MFA/Policy Engine） | v1, v5, v8, v11~v12, v15, v16, v17 | ~8 |
| 多租户（CRUD/Quota/Budget/Audit/Isolation/Governance/日费用） | v1, v3~v5, v7~v8, v11~v12 | ~6 |
| 事件/通知/Webhook/SSE（Bus/Transport/Retry/Filter/Multi-Destination） | v1, v3~v6, v8~v9, v11~v12, v17 | ~7 |
| 复制/高可用/集群（CRR/SRR/Cluster Singleton/HA/Federation） | v1, v3~v5, v9, v17, ROADMAP #3, #10 | ~6 |
| Reconcile/GC/Lifecycle（Orphan/Retention/Scrub/Transition/Version） | v1, v4, v6~v7, v15, v17, ROADMAP #5, #8 | ~6 |
| 合规（WORM/Legal Hold/Retention/Disposition/Client Encryption/Access Log） | v2, v6, v8~v10, v12, v16, v17 | ~6 |
| 可观测性（OTel/Metrics/Grafana/Prometheus/SLO/Tracing/pprof/Debug） | v11, v13, v14 | ~4 |
| 工程质量（内存安全/并发/压缩/诊断/错误模型/Crash Recovery） | v11, v14, v15 | ~6 |
| 配置热重载/IP 访问控制/内置 TLS | v16 | ~3 |
| 测试基础设施/契约测试/Benchmark | v11, v14（行级） | ~2 |
| 边缘交付/CDN 集成 | v13 | ~1 |
| API 治理/版本兼容 | v10（行级） | ~1 |
| SDK 兼容性测试 | v11（行级） | ~1 |
| Web UI | v3, v6, v10, v11（行级） | ~1 |
| 其他（备份/迁移/优雅关闭/分享链接/Federation/Snapshot） | v2, v4, v8, v10~v11 | ~4 |

**本期选点原则：** 选取**用户采纳 / 开发者体验 / 运维就绪**方向——不是"技术上有趣"或"架构上优雅"，而是**直接决定用户是否选择、能否上手、能否规模采用的商业关键路径**。每个方向在当前代码库中都有明确的"半成品"或"骨架"证据——API 已实现但 SDK 未暴露，后端已完整但 UI 是空壳，基础设施已就绪但无迁移工具。

---

## 本期方向总览

| # | 方向 | 类型 | 影响评估 | 核心代码锚点 | 既有覆盖 |
|---|------|------|---------|-------------|---------|
| 1 | **🔴 SDK 跨语言完整性与开发者体验** | 采纳/集成 | 直接影响开发者选用决策；30+ REST 端点未暴露；SDK 间特性不一致 | `sdk/go/aerovault/client.go`（1006 行）、`sdk/python/aero_vault.py`（673 行）、`sdk/js/aero-vault.js`（1084 行） vs `internal/api/rest/router.go` 全部路由 | v11 仅测试子项提及 SDK 兼容性测试，**非独立方向** |
| 2 | **🔴 Web UI 生产级管理控制台** | 采纳/运维 | 非技术用户无法完成任何管理操作；丰富 admin API 无 UI 暴露 | `internal/webui/static/index.html`（单体 ~280 行 HTML+JS）、`internal/api/rest/admin.go`（18 个 admin handler） | v3/v6/v10 行级提及 UI 存在，**无生产化分析** |
| 3 | **🟠 导入/迁移与批量操作工具集** | 采纳/迁移 | 从 S3/MinIO/Ceph 迁移的零工具支持；snapshot 仅限 SQLite+local FS | `internal/snapshot/snapshot.go`（仅 SQLite 本地快照）、`internal/cli/`（无 import 命令）、`internal/service/file_crud.go:Put`（单对象接口） | **零覆盖** |
| 4 | **🟠 插件/扩展/钩子系统** | 架构/生态 | 定制需求必须 fork 核心代码；无业务逻辑注入点 | `internal/service/file.go:FileService`（无 hook 注册点）、`internal/events/bus.go:Subscribe`（仅事件订阅，无业务钩子） | **零覆盖** |
| 5 | **🟡 性能基准测试与容量规划框架** | 运维/可靠性 | 无任何性能基线数据；运维人员无法预测硬件需求 | `Makefile`（无 benchmark target）、`internal/`（无 benchmark 文件）、`deploy/`（无 sizing guide） | **零覆盖** |

---

## 1. 🔴 SDK 跨语言完整性与开发者体验（Developer Experience）

### 为什么需要它

当前 AeroVault 提供三种原生 SDK（Go、Python、JavaScript），但它们在功能完整性上存在**显著且分散的缺口**。大量 REST API 端点在一个或多个 SDK 中缺失，导致开发者无法通过 SDK 完成完整的工作流。

**对比：REST API vs SDK 暴露度**

```
REST API 端点                                    Go SDK    Python SDK    JS SDK
─────────────────────────────────────────────    ──────    ──────────    ──────
对象 CRUD（Get/Put/Delete/List/Head/Stat）        ✅         ✅            ✅
Tags（Get/Put/Delete）                            ✅         ✅            ✅
ACL 对象级（Get/Set）                              ✅         ❌            ❌
ACL 桶级（Get/Set）                                ✅         ✅            ✅
版本控制（ListVersions/GetVersion）                ✅         ✅            ✅
对象锁（LockObject）                               ❌         ⚠️(仅seconds) ❌
对象恢复（Restore）                               ❌         ❌            ❌
预签名 URL                                        ✅         ✅            ❌
缩略图                                            ✅         ❌            ❌
─────────────────────────────────────────────    ──────    ──────────    ──────
多分片上传（Init/Upload/Complete/Abort）           ❌         ❌            ❌
批量操作（BatchDelete/BatchTag）                   ❌         ❌            ❌
─────────────────────────────────────────────    ──────    ──────────    ──────
桶管理（List/Delete Bucket）                       ❌         ❌            ❌
桶配置（GetConfig/Versioning/Lock）                ❌         ❌            ❌
桶生命周期（Get/Put Lifecycle）                   ❌         ❌            ❌
桶策略（Get/Put Policy）                          ❌         ❌            ❌
桶 CORS（Get/Put/Delete）                          ❌         ❌            ❌
桶日志（Get/Put/Delete Logging）                   ❌         ❌            ❌
桶通知（Get/Put/Delete Notification）              ❌         ❌            ❌
桶统计（GetBucketStats）                           ❌         ❌            ❌
桶版本列表（ListBucketVersions）                   ❌         ❌            ❌
─────────────────────────────────────────────    ──────    ──────────    ──────
文件夹（List/Create/Delete Folder）                ❌         ❌            ❌
─────────────────────────────────────────────    ──────    ──────────    ──────
搜索/聊天/Agent/血缘/Linage                       ✅         ✅            ✅
─────────────────────────────────────────────    ──────    ──────────    ──────
Admin: Keys（Add/List/Revoke）                    ✅         ✅            ✅
Admin: JWT 签发                                  ✅         ✅            ✅
Admin: Tenants（CRUD/Status）                    ✅         ✅            ✅
Admin: Quota/Budget                             ✅         ✅            ✅
Admin: Audit                                    ✅         ✅            ✅
Admin: Jobs/WebhookFailures                     ✅         ✅            ✅
Admin: GetConfig                                ❌         ❌            ❌
─────────────────────────────────────────────    ──────    ──────────    ──────
事件流 SSE                                      ❌         ❌            ❌
```

**缺失了多少？**
- Go SDK：约 **25 个 REST 端点**未暴露（占可暴露的 ~40%）
- Python SDK：约 **28 个 REST 端点**未暴露
- JS SDK：需要深入分析，但根据文件名模式推测缺失更多

比功能缺失更严重的是三个 SDK 之间**不一致**：
- Go SDK 有的功能（ACL 对象级、Thumbnail），Python 没有
- Python 有的功能（lock 方法），Go 没有
- 返回类型不一致：Go 用强类型结构体，Python 用 `dict[str, Any]`，JS 用普通对象
- 错误处理模式不一致：Go 用 `error` 接口，Python 用自定义异常，JS 用 Promise rejection

### 当前状态

```go
// Go SDK — client.go 方法签名示例
func (c *Client) Upload(ctx context.Context, key string, r io.Reader, opts UploadOptions) (*Object, error)
func (c *Client) GetTags(ctx context.Context, key string) (map[string]string, error)

// ❌ 不存在：
// func (c *Client) InitMultipart(...)
// func (c *Client) BatchDelete(...)
// func (c *Client) GetBucketPolicy(...)
// func (c *Client) ListBuckets(...)
// func (c *Client) GetConfig(...)
// func (c *Client) CreateFolder(...)
```

```python
# Python SDK — 方法签名示例
def upload(self, key, data, content_type="", metadata=None, tags=None):
def get_tags(self, key):

# ❌ 不存在：
# def init_multipart(...)
# def batch_delete(...)
# def get_bucket_policy(...)
# def get_object_acl(...)
# def thumbnail(...)
```

| 代码位置 | 当前状态 | 缺失 |
|---------|---------|------|
| `sdk/go/aerovault/client.go` | 1006 行，~40 个公共方法 | 缺少 ~25 个 REST 端点对应方法 |
| `sdk/go/aerovault/types.go` | 256 行，强类型 DTO | 缺少 `BucketConfig`、`CORSRule`、`LoggingConfig`、`NotificationRule`、`Folder`、`PartRecord`、`Upload` 等类型 |
| `sdk/go/aerovault/client_test.go` | 944 行，stub-based 单元测试 | 无集成测试（无真实服务器测试） |
| `sdk/python/aero_vault.py` | 673 行，~40 个方法 | 同上，且使用弱类型 `dict` 返回 |
| `sdk/python/test_aero_vault.py` | 218 行 | 仅单元测试，测试覆盖率低 |
| `sdk/js/aero-vault.js` | 1084 行 | 无 TypeScript 类型定义（`d.ts` 可能不完整） |
| `internal/api/rest/router.go` | 全部 ~60 个 REST 路由 | SDK 仅覆盖 ~35 个 |

### 边界情况暴露

| Edge Case | 场景 | 后果 |
|-----------|------|------|
| **跨 SDK 行为不一致** | Go SDK 的 Delete 返回 error，Python SDK 返回 None，JS 返回 Promise | 开发者切换语言时需要重新学习 API 惯用法 |
| **功能盲区** | 用户需要配置桶 CORS，但所有 SDK 都不支持 | 用户只能手写 curl/使用 S3 API → 放弃或投诉 |
| **SDK 版本落后于服务器** | 服务器新增端点（如 Restore），SDK 未跟进 | 用户升级服务器后 SDK 调用失败 → 回滚 |
| **弱类型的 Python 返回** | `set_quota()` 返回 `dict[str, Any]`，键名与服务器 JSON 一致但无 IDE 提示 | 运行时 KeyError，无编译期检查 |
| **无 SDK 集成测试** | 服务器 handler 变更了 response 字段名，SDK 期望旧字段名 | 单元测试通过（mock server），但生产环境失败 |
| **错误处理不统一** | Go SDK 有 `AsError` 辅助函数，Python 有自定义异常 `AeroVaultError`，JS 仅有 HTTP 状态码 | 用户需要为不同语言写不同的错误处理逻辑 |

### 架构方向

```
┌──────────────────────────────────────────────────────────────────┐
│              SDK 完整性与 DX 提升计划                              │
│                                                                  │
│  Phase 1 — 补齐功能缺口（三 SDK 同步）：                           │
│                                                                  │
│  REST 端点 → SDK 方法映射表（新增）：                              │
│    ┌──────────────────────────┬──────────────┬──────────┐        │
│    │ REST Endpoint            │ SDK Method    │ Priority │        │
│    ├──────────────────────────┼──────────────┼──────────┤        │
│    │ POST /multipart          │ InitMultipart│ 🔴 High  │        │
│    │ PUT /multipart/{id}/parts│ UploadPart   │ 🔴 High  │        │
│    │ POST /multipart/{id}/comp│ CompleteMult │ 🔴 High  │        │
│    │ DELETE /multipart/{id}   │ AbortMultip  │ 🔴 High  │        │
│    │ POST /batch/delete       │ BatchDelete  │ 🔴 High  │        │
│    │ POST /batch/tag          │ BatchTag     │ 🔴 High  │        │
│    │ GET /buckets             │ ListBuckets  │ 🟠 Med   │        │
│    │ DELETE /buckets/{b}      │ DeleteBucket │ 🟠 Med   │        │
│    │ GET /buckets/{b}/policy  │ GetBucketPol │ 🟠 Med   │        │
│    │ PUT /buckets/{b}/policy  │ SetBucketPol │ 🟠 Med   │        │
│    │ GET /buckets/{b}/cors    │ GetBucketCORS│ 🟠 Med   │        │
│    │ PUT /buckets/{b}/cors    │ SetBucketCORS│ 🟠 Med   │        │
│    │ DEL /buckets/{b}/cors    │ DelBucketCOR │ 🟠 Med   │        │
│    │ GET /buckets/{b}/logging │ GetBucketLog │ 🟠 Med   │        │
│    │ PUT /buckets/{b}/logging │ SetBucketLog │ 🟠 Med   │        │
│    │ DEL /buckets/{b}/logging │ DelBucketLog │ 🟠 Med   │        │
│    │ GET /buckets/{b}/notif   │ GetBucketNot │ 🟠 Med   │        │
│    │ PUT /buckets/{b}/notif   │ SetBucketNot │ 🟠 Med   │        │
│    │ DEL /buckets/{b}/notif   │ DelBucketNot │ 🟠 Med   │        │
│    │ GET /buckets/{b}/config  │ GetBucketCfg │ 🟠 Med   │        │
│    │ PUT /buckets/{b}/version │ SetVersioning│ 🟠 Med   │        │
│    │ GET /folders             │ ListFolders  │ 🟡 Low   │        │
│    │ POST /folders            │ CreateFolder │ 🟡 Low   │        │
│    │ DELETE /folders/*        │ DeleteFolder │ 🟡 Low   │        │
│    │ POST /files/*/lock       │ LockObject   │ 🟡 Low   │        │
│    │ POST /files/*/restore    │ Restore      │ 🟡 Low   │        │
│    │ GET /admin/config        │ GetConfig    │ 🟡 Low   │        │
│    └──────────────────────────┴──────────────┴──────────┘        │
│                                                                  │
│  Phase 2 — 集成测试与契约测试：                                    │
│    • Go SDK 集成测试：httptest 启动完整服务 → 所有方法调用验证       │
│    • Python SDK 集成测试：同上                                     │
│    • JS SDK 集成测试：同上（Node.js）                              │
│    • 契约：OpenAPI spec → 自动生成 SDK 方法签名骨架               │
│                                                                  │
│  Phase 3 — TypeScript 类型安全（JS SDK）：                        │
│    • 完整的 .d.ts 文件，覆盖所有方法与返回类型                     │
│    • 导出 ES Module + CommonJS 双格式                             │
│                                                                  │
│  Phase 4 — SDK 发布流水线：                                       │
│    • Go: go install / GitHub Release                              │
│    • Python: PyPI 自动发布（CI tag → twine upload）               │
│    • JS: npm 自动发布（CI tag → npm publish）                     │
│                                                                  │
│  长期 — 代码生成：                                                 │
│    • 从 OpenAPI spec 生成 SDK 骨架（减少手工维护成本）              │
│    • 但保留手动优化的上传/下载流式接口（io.Reader/bytes）          │
└──────────────────────────────────────────────────────────────────┘
```

### 实现理由

**商业价值：** SDK 是开发者与平台之间的主要接口。一个不完整、不一致的 SDK 三件套给潜在用户的印象是"这个项目还没准备好被认真使用"。开发者评估一个存储平台时，首先查看的就是 SDK 的完整性和质量。30+ 端点的缺失 + 跨 SDK 不一致 = 采纳的**致命阻碍**。

**技术必要性：** 当前三个 SDK 是手工维护的，与 REST API 之间没有自动化对齐机制。每次服务器新增端点都需要三次手动实现，且容易遗漏。建立契约测试 + 部分代码生成可以大幅降低维护成本并确保一致性。

**代码复杂度：** 中。主要工作是"手工补齐 + 测试"，不是新的架构模式。每个端点约 20-40 行 SDK 代码（request 构建 + 响应解析）。全部补齐约 600-800 行每 SDK（Go 约 700 行，Python 约 500 行，JS 约 700 行），加上集成测试。

---

## 2. 🔴 Web UI 生产级管理控制台

### 为什么需要它

当前 Web UI 是一个**单体 HTML 文件（~280 行内联 HTML+JS+CSS）**，提供四个基本面板：

| 面板 | 功能 | 当前状态 |
|------|------|---------|
| Search | 语义搜索（vector/BM25/hybrid） | ✅ 基本可用 |
| Object Detail | 显示对象元数据 JSON | ⚠️ 只读，无编辑能力 |
| Lineage | 显示对象的 AI 使用记录 | ✅ 基本可用 |
| Chat | RAG 聊天界面 | ✅ 基本可用 |

**完全缺失的管理功能：**

| 管理领域 | REST API 已实现 | Web UI 是否有面板 |
|---------|----------------|-----------------|
| 租户管理（创建/查看/删除/状态） | ✅ `admin.go` | ❌ **无** |
| API Key 管理（添加/列取/撤销） | ✅ `admin.go` | ❌ **无** |
| 配额与预算管理 | ✅ `admin.go` | ❌ **无** |
| 审计日志查看 | ✅ `admin.go` | ❌ **无** |
| 后台任务监控与重试 | ✅ `admin.go` | ❌ **无** |
| Webhook 失败查看 | ✅ `admin.go` | ❌ **无** |
| 桶配置（版本控制/锁定/策略/CORS/日志/通知） | ✅ `handler.go` | ❌ **无** |
| 桶生命周期配置 | ✅ `admin.go` | ❌ **无** |
| 桶统计（容量/对象数） | ✅ `handler.go` | ❌ **无** |
| 服务配置查看 | ✅ `admin.go` | ❌ **无** |
| 健康/就绪状态 | ✅ 内建 | ❌ **无** |
| 指标仪表板 | ⚠️ Prometheus | ❌ **无** |

这意味着：虽然管理 API 完整实现了，**非技术用户（运维人员、产品经理、合规人员）每次都需要通过 curl 或开发者的帮助来完成管理操作**。对于一个宣称"企业级对象存储"的产品，这是一个严重的采纳障碍。

### 当前状态

```html
<!-- internal/webui/static/index.html — 单体文件，~280 行 -->
<main>
  <aside>
    <div class="group"><!-- upload --></div>
    <div class="group"><!-- objects list --></div>
  </aside>
  <section>
    <div class="tabs"><!-- search | detail | lineage | chat --></div>
    <!-- 四个面板 -->
  </section>
</main>
```

| 代码位置 | 当前状态 | 缺失 |
|---------|---------|------|
| `internal/webui/static/index.html` | 单体 ~280 行 HTML+JS+CSS | 无管理面板、无认证页面、无错误页面、无加载状态 |
| `internal/webui/static/` | 仅 `index.html` | 无 CSS/JS 分离、无图标、无字体 |
| `internal/webui/web.go` | `embed` 静态文件服务 | 无 API 代理、无认证中间件、无 SPA fallback |
| `internal/api/rest/admin.go` | 18 个 admin handler（完整） | UI 无一使用 |
| `internal/api/rest/handler.go` | 桶配置 handler（完整） | UI 无一使用 |

### 边界情况暴露

| Edge Case | 场景 | 后果 |
|-----------|------|------|
| **权限不足** | 普通用户打开管理页面，API 返回 403 | 显示空白/错误页面，无任何用户友好的提示 |
| **会话过期** | 管理页面长时间未操作，JWT 过期 | 所有 API 调用失败，无刷新 token 或重定向登录的流程 |
| **大租户列表** | 100+ 租户的 Admin UI | 无分页/搜索/过滤 → 页面卡死 |
| **SSE 事件流管理** | 运维人员需要实时查看事件流 | 需要通过 SSE API 自己写脚本 |
| **多语言** | 非英语用户 | 硬编码英文界面 |

### 架构方向

```
┌──────────────────────────────────────────────────────────────────┐
│              Web UI 管理控制台                                    │
│                                                                  │
│  架构选型：保持无构建工具链（当前 embed 模式）→ 多 HTML 文件      │
│  理由：零依赖、零构建步骤、与当前 embed 模式一致                   │
│                                                                  │
│  目录结构：                                                       │
│    internal/webui/static/                                         │
│      index.html         ← 重定向到 console.html（或入口页）       │
│      console.html       ← 主控制台（文件浏览+搜索+聊天）           │
│      admin.html         ← 管理面板                                │
│      style.css          ← 共享样式                                │
│      api.js             ← 共享 API 客户端（封装 fetch）            │
│                                                                  │
│  管理面板功能（admin.html）：                                      │
│    ┌─────────────────────────────────────────────────────┐       │
│    │ Tab Navigation:                                      │       │
│    │ [Tenants] [API Keys] [Buckets] [Audit] [Jobs] [Config]│       │
│    ├─────────────────────────────────────────────────────┤       │
│    │ Tenants Tab:                                        │       │
│    │   - 列表：tenant_id, display_name, status, created   │       │
│    │   - 创建：输入 tenant_id, display_name → POST        │       │
│    │   - 删除：确认对话框 → DELETE                        │       │
│    │   - 状态切换：active/disabled → PUT status            │       │
│    │   - 配额编辑：max_bytes, max_objects → PUT quota      │       │
│    │   - 预算编辑：daily_budget_usd → PUT budget           │       │
│    ├─────────────────────────────────────────────────────┤       │
│    │ API Keys Tab:                                       │       │
│    │   - 列表：token_hash, scopes, label, expires, last   │       │
│    │   - 创建：输入 token, scopes, tenant, label → POST   │       │
│    │   - 撤销：确认对话框 → DELETE                        │       │
│    │   - JWT 签发：选择 tenant+scopes+ttl → POST /jwt     │       │
│    ├─────────────────────────────────────────────────────┤       │
│    │ Buckets Tab:                                        │       │
│    │   - 按租户筛选                                       │       │
│    │   - 桶列表 + 统计（对象数/字节数）                    │       │
│    │   - 配置面板：versioning, lock, lifecycle,            │       │
│    │     policy, CORS, logging, notification              │       │
│    ├─────────────────────────────────────────────────────┤       │
│    │ Audit Tab:                                          │       │
│    │   - 审计日志列表（actor/action/target/detail/time）   │       │
│    │   - 按租户/操作筛选                                   │       │
│    ├─────────────────────────────────────────────────────┤       │
│    │ Jobs Tab:                                           │       │
│    │   - 后台任务列表（type/status/attempts/error）        │       │
│    │   - 重试失败任务                                     │       │
│    ├─────────────────────────────────────────────────────┤       │
│    │ Config Tab:                                         │       │
│    │   - 服务器配置：AI 模型/速率限制/版本等（只读）        │       │
│    └─────────────────────────────────────────────────────┘       │
│                                                                  │
│  认证集成：                                                      │
│    • 首次打开时弹出 API Key / Token 输入框                        │
│    • 存入 localStorage（同现有 tenant 输入模式）                  │
│    • 管理面板额外检查 admin scope                                 │
│                                                                  │
│  错误处理：                                                      │
│    • API 错误显示在页面顶部的 toast bar                           │
│    • 403 → "权限不足，需要 admin scope"                           │
│    • 网络错误 → "无法连接服务器" + 重试按钮                       │
│                                                                  │
│  非目标（刻意避免）：                                             │
│    • 不做前端框架（React/Vue/Svelte）— 保持零构建工具链           │
│    • 不做实时更新（WebSocket）— 使用手动刷新 + 简单轮询           │
│    • 不引入 CSS 框架 — 使用 vanilla CSS（当前风格延续）           │
└──────────────────────────────────────────────────────────────────┘
```

### 实现理由

**商业价值：** 管理控制台是产品"企业就绪"（enterprise-ready）的标志性能力。没有管理 UI，每个管理操作都需要 API 调用，意味着：① 非技术用户无法自助服务；② 运维团队需要编写脚本或使用 curl；③ 演示/POC 时只能展示 API 而不是直观的界面。在竞争对手（MinIO Console、AWS Console、Ceph Dashboard）都有完善管理 UI 的背景下，没有控制台是一个显著的竞争劣势。

**技术必要性：** 当前 Web UI 与后端 admin API 之间有一个"断裂层"——API 完整实现了但没有任何消费端。补齐这个断裂层的成本极低（vanilla JS + HTML，零依赖），但收益极大。

**代码复杂度：** 低到中。约 500-800 行 HTML+JS（单个 admin.html 文件），复用现有的 `api.js` 辅助函数。可以从当前 index.html 的模式直接扩展，不引入任何构建工具或外部依赖。

---

## 3. 🟠 导入/迁移与批量操作工具集

### 为什么需要它

AeroVault 当前没有**任何**从外部存储系统导入数据的工具。对于潜在用户来说，这意味着：

```
"我想试用 AeroVault，但我的 2TB 数据在 AWS S3 上。"
→ 没有工具能一次性迁移这些数据。
→ 用户必须自己编写脚本：ListObjectsV2 → GetObject → Put → 重复
→ 对于大容量（>100GB），这个脚本需要考虑：
  并发控制、断点续传、校验和验证、成本控制、时间预估
→ 大多数用户会选择"算了，下次再说"
```

这是客户获取（customer acquisition）漏斗中**最致命的断裂点**。

**当前已有的工具：**

| 工具 | 能力 | 限制 |
|------|------|------|
| `internal/snapshot/snapshot.go` | 打包 SQLite DB + local FS 到 tar.gz | 仅 SQLite + local FS，不能用于 S3 后端 |
| `internal/cli/cli_snapshot.go` | CLI 调用 snapshot | 同上，仅开发/小规模部署 |
| `SDK Upload` | 单对象上传 | 不能批量，不能递归目录 |

**完全缺失的迁移场景：**

| 源系统 | 目标 | 现状 |
|--------|------|------|
| AWS S3 桶 | AeroVault | ❌ 无工具 |
| MinIO / Ceph 桶 | AeroVault | ❌ 无工具 |
| 本地文件系统（递归） | AeroVault | ❌ 无工具 |
| 另一个 AeroVault 实例 | 当前实例 | ❌ 无工具 |
| NFS/SMB 挂载点 | AeroVault | ❌ 无工具 |

### 当前状态

```go
// internal/snapshot/snapshot.go — 仅限 SQLite + local FS
// Create(outPath, dbPath, objectsRoot string) error
// Restore(inPath, dbPath, objectsRoot string) error
// 不支持 S3 后端、不支持 Postgres、不支持选择迁移

// internal/cli/cli_snapshot.go — CLI 封装
// aero-vault cli snapshot create <output.tar.gz>
// aero-vault cli snapshot restore <input.tar.gz>
```

| 代码位置 | 当前状态 | 缺失 |
|---------|---------|------|
| `internal/snapshot/snapshot.go` | SQLite + local FS 快照 | 无 S3 后端/Postgres 支持 |
| `internal/cli/cli_crud.go:cmdUpload` | 单文件上传 | 无 `import` 子命令 |
| `sdk/go/aerovault/client.go:Upload` | 单对象上传 | 无批量/并发上传辅助函数 |
| `internal/repository/sql_objects.go` | 单行插入 | 无批量导入优化 |
| `internal/service/file_crud.go:Put` | 单对象写入 | 无跳过事件发布的批量模式 |
| `Makefile` | 无 import 相关 target | 无迁移脚本 |

### 边界情况暴露

| Edge Case | 场景 | 后果 |
|-----------|------|------|
| **大桶迁移（>1TB）** | 100 万对象，总大小 5TB | 单线程迁移需要数天，需要并发 + 断点续传 |
| **源端限流** | AWS S3 对 GET 请求限速（5500 req/s/prefix） | 迁移速度受限于源端，需要自适应限速 |
| **校验和验证** | 迁移后验证对象内容完整性 | 需要比对 ETag/MD5，当前无自动化验证 |
| **元数据保留** | 迁移时保留 S3 标签/元数据/ACL/版本历史 | 部分元数据可能不支持 |
| **增量同步** | 首次全量迁移后，增量同步变更的数据 | 需要追踪源端变更事件 |
| **迁移中断恢复** | 迁移 50% 后网络中断 | 需要 checkpoint + 可恢复设计 |
| **存储类映射** | S3 STANDARD → AeroVault 的哪个存储类？ | 需要可配置的映射规则 |
| **事件风暴** | 迁移 10 万对象产生 10 万事件 | 下游 webhook/indexer/antivirus 被淹没 |

### 架构方向

```
┌──────────────────────────────────────────────────────────────────┐
│              导入/迁移工具集                                       │
│                                                                  │
│  Phase 1 — 本地文件系统递归导入（最低成本，最高收益）：              │
│                                                                  │
│  CLI 新增：aero-vault import <local-dir> <prefix>                 │
│                                                                  │
│  实现：internal/cli/cli_import.go                                  │
│    walkDir: filepath.Walk → 对每个文件:                           │
│      1. 相对路径 → key                                           │
│      2. 读取文件 → multipart upload（大文件自动分片）               │
│      3. 并发 worker pool (默认 4，可配置)                          │
│      4. 进度显示 (bytes/sec, 剩余时间, 成功/失败计数)               │
│      5. 错误重试 (指数退避，最多 3 次)                             │
│      6. 迁移报告 (成功数/失败数/总字节)                             │
│                                                                  │
│  Phase 2 — S3 兼容存储导入（aero-vault import s3://...）：         │
│                                                                  │
│  CLI 新增：aero-vault import s3://source-bucket/prefix             │
│                                                                  │
│  配置：                                                            │
│    S3_SOURCE_ENDPOINT (兼容任何 S3 兼容存储)                      │
│    S3_SOURCE_REGION                                                │
│    S3_SOURCE_ACCESS_KEY_ID                                         │
│    S3_SOURCE_SECRET_ACCESS_KEY                                     │
│                                                                  │
│  实现：                                                            │
│    1. ListObjectsV2 (paginated) → 对象列表                        │
│    2. Worker pool (可配置并发数，默认 10)                          │
│    3. 每个 worker: GetObject → Put (流式传输，不缓冲到磁盘)       │
│    4. 可选的校验和验证 (Compare ETag/MD5)                          │
│    5. 可选择迁移标签/元数据                                        │
│    6. 断点续传: 定期记录 checkpoint (last key)                     │
│    7. 重试策略: 指数退避 + 最终失败记录                            │
│                                                                  │
│  Phase 3 — 实例间复制（aero-vault replicate）：                    │
│                                                                  │
│  CLI 新增：aero-vault replicate --from https://source:8080          │
│            --to https://dest:8080 --tenant acme                    │
│                                                                  │
│  用途：                                                            │
│    • 迁移到新实例                                                  │
│    • 跨区域复制（单次 bulk copy）                                  │
│    • 备份到另一个实例                                              │
│                                                                  │
│  Phase 4 — 批量操作增强：                                           │
│    • SDK 新增批量上传辅助函数 (UploadDir, UploadBucket)            │
│    • SDK 新增大文件自动分片上传 (UploadLarge)                      │
│                                                                  │
│  非目标：                                                          │
│    • 不做实时增量同步（已有 replication 机制）                     │
│    • 不做持续的双向同步（这是 federation 的范畴）                   │
│    • 不做 AWS DataSync 风格的 agent-based 传输                     │
└──────────────────────────────────────────────────────────────────┘
```

### 实现理由

**商业价值：** 迁移工具是客户获取漏斗中最关键的转化点。没有它，潜在用户的数据迁移成本≈自己写一个分布式文件复制工具——这通常超出大多数团队的意愿和能力。提供一个 `aero-vault import s3://` 命令，可以将从"评估到上线"的时间从数周缩短到数小时。

**技术必要性：** 当前 snapshot 工具仅适用于 SQLite + local FS 这一种部署模式。对于任何使用 Postgres + S3 后端的生产部署，完全没有备份/迁移工具。即使不考虑客户获取，内部运维也需要跨实例迁移能力。

**代码复杂度：** 中。Phase 1（本地文件导入）约 200-300 行 Go（使用 `filepath.Walk` + `sync.WorkerPool`）。Phase 2（S3 导入）依赖 `aws-sdk-go-v2` 或直接使用 S3 REST API（`net/http` 零依赖），约 300-400 行。Phase 3（实例间复制）复用 SDK 的 Upload，约 200 行。

---

## 4. 🟠 插件/扩展/钩子系统（Plugin / Extension / Hook System）

### 为什么需要它

当前 AeroVault 的代码架构是**完全封闭的**——所有业务逻辑都在 `internal/` 包中硬编码，没有任何方式让用户在请求生命周期中注入自定义逻辑而不修改核心代码。

**用户无法在不 fork 的情况下实现以下需求：**

| 需求 | 当前方案 | 问题 |
|------|---------|------|
| 上传时自动添加标签（如 `source:web-ui`、`checksum:xxx`） | ❌ 无钩子 | 每个客户端必须手动设置 |
| 上传后自动触发外部处理流水线（转码/压缩/病毒扫描） | ⚠️ 仅 webhook（异步） | 无法同步控制上传结果 |
| 自定义鉴权逻辑（集成公司 LDAP/OIDC） | ⚠️ 仅 JWT/API Key | 需要自己写 auth 中间件或包装 API |
| 请求/响应日志包含业务字段 | ⚠️ 仅标准 access log | 无法扩展日志格式 |
| 限制特定前缀的访问速率高于其他 | ⚠️ 仅全局+AI 限流 | 无法按前缀配置 |
| 上传时检查文件类型并拒绝非白名单类型 | ❌ 无钩子 | 只能在客户端做 |
| 下载时动态添加水印或修改响应 | ❌ 无钩子 | 只能通过反向代理 |

**市场上同类产品的对比：**

| 产品 | 扩展机制 |
|------|---------|
| AWS S3 | Lambda 触发（事件驱动）、S3 Object Lambda（请求时转换） |
| MinIO | Webhook（事件）、Bucket Notification（事件） |
| Ceph | RGW 多站点（事件）、Lua 脚本（请求时）|
| **AeroVault** | **仅 EventBus（异步事件）** |

### 当前状态

```go
// internal/service/file.go — FileService 没有钩子注册点
type FileService struct {
    store        storage.Storage
    repo         repository.Repository
    logger       *slog.Logger
    sink         EventSink        // 仅事件发布
    chunkCleaner ChunkCleaner     // BM25 专用，非通用钩子
}

// 当前仅有 2 个扩展点：
// 1. EventSink — 事件发布（异步，无返回值）
// 2. ChunkCleaner — 删除时清理索引（特定用途，非通用）

// 没有：
// - PrePutHook(ctx, obj) → (modifiedObj, error)
// - PostGetHook(ctx, obj, data) → (modifiedData, error)
// - PreDeleteHook(ctx, obj) → (allow bool, error)
// - AuthenticateHook(ctx, token) → (key, error)  [自定义认证]
```

| 代码位置 | 当前状态 | 缺失 |
|---------|---------|------|
| `internal/service/file.go:FileService` | 硬编码业务逻辑 | 无请求生命周期钩子 |
| `internal/service/file_crud.go:Put` | 无钩子调用 | 无 `PrePut` / `PostPut` 调用点 |
| `internal/service/file_crud.go:Get` | 无钩子调用 | 无 `PreGet` / `PostGet` 调用点 |
| `internal/service/file_crud.go:Delete` | 无钩子调用 | 无 `PreDelete` 调用点 |
| `internal/auth/auth.go:Registry` | 固定认证流程 | 无 `Authenticator` 接口扩展 |
| `internal/events/bus.go` | 事件发布（异步） | 无同步钩子、无请求上下文 |
| `internal/middleware/middleware.go` | 固定中间件链 | 无用户注册自定义中间件的能力 |

### 边界情况暴露

| Edge Case | 场景 | 后果 |
|-----------|------|------|
| **钩子超时** | PrePut 钩子调用外部 API 超时（10s） | 整个 PUT 请求卡住，连接池耗尽 |
| **钩子失败** | PreDelete 钩子返回错误，阻止删除 | 用户无法删除对象，需要知道是哪个钩子阻止的 |
| **钩子顺序** | 两个钩子同时修改 metadata（一个加 tag，一个改 content-type） | 后执行的钩子覆盖前者——需要明确定义执行顺序 |
| **钩子副作用** | PostGet 钩子记录访问日志到外部系统 | 钩子本身失败不应影响 GET 的正常返回 |
| **钩子热加载** | 更新钩子配置后需要重启进程 | 需要热重载支持（v16 方向 1） |
| **多租户钩子** | 租户 A 需要 PII 扫描钩子，租户 B 不需要 | 钩子需按租户配置生效 |

### 架构方向

```
┌──────────────────────────────────────────────────────────────────┐
│              Plugin / Hook System                                │
│                                                                  │
│  设计原则：                                                       │
│    1. 同步钩子默认 fail-open（钩子失败→warn log→继续执行）        │
│    2. 异步钩子（需保证顺序的）通过 EventBus 实现                   │
│    3. 按租户配置钩子（租户 A 的钩子不影响租户 B）                 │
│    4. 钩子超时默认 5s（可配置）                                   │
│    5. 钩子执行顺序由注册顺序决定                                   │
│                                                                  │
│  Phase 1 — 核心钩子接口 + 注册体系：                              │
│                                                                  │
│  internal/hook/hook.go：                                          │
│    // Hook 是所有钩子的通用接口                                    │
│    type Hook interface {                                          │
│        Name() string                                              │
│    }                                                              │
│                                                                  │
│    // PrePutHook 在对象写入存储前调用                              │
│    type PrePutHook interface {                                    │
│        Hook                                                        │
│        PrePut(ctx, obj *ObjectInfo) (*ObjectInfo, error)          │
│    }                                                              │
│                                                                  │
│    // PostPutHook 在对象写入数据库后调用                           │
│    type PostPutHook interface { ... }                             │
│                                                                  │
│    // PreGetHook 在读取对象前调用（可用于访问控制）                │
│    type PreGetHook interface { ... }                              │
│                                                                  │
│    // PostGetHook 在读取对象后调用（可用于内容修改）               │
│    type PostGetHook interface { ... }                             │
│                                                                  │
│    // PreDeleteHook 在删除前调用（可阻止删除）                     │
│    type PreDeleteHook interface { ... }                           │
│                                                                  │
│    // Registry：钩子注册表                                         │
│    type Registry struct { ... }                                    │
│    func NewRegistry() *Registry                                    │
│    func (r *Registry) Register(tenant string, h Hook)              │
│    func (r *Registry) PrePut(ctx, tenant string, obj *ObjectInfo)  │
│    func (r *Registry) PostPut(ctx, tenant string, obj *ObjectInfo) │
│    ...                                                             │
│                                                                  │
│  Phase 2 — FileService 集成：                                     │
│                                                                  │
│    // FileService 新增字段：                                       │
│    type FileService struct {                                       │
│        hooks *hook.Registry                                       │
│        ...                                                         │
│    }                                                              │
│                                                                  │
│    // Put 方法中调用钩子：                                        │
│    func (s *FileService) Put(...) {                                │
│        // PrePut 钩子                                             │
│        if s.hooks != nil {                                        │
│            obj, err = s.hooks.PrePut(ctx, tenant, obj)             │
│            if err != nil {                                         │
│                s.logger.Warn("preput hook failed", "error", err)   │
│                // fail-open: 继续执行，但记录失败                   │
│            }                                                       │
│        }                                                           │
│        // ... 正常写入流程 ...                                     │
│        // PostPut 钩子（异步）                                     │
│    }                                                               │
│                                                                  │
│  Phase 3 — 内置钩子示例：                                         │
│    • AutoTagger: 根据 content-type/size 自动添加标签               │
│    • FileTypeValidator: 拒绝特定类型的文件上传                     │
│    • MetadataEnricher: 调用外部 API 丰富元数据                     │
│    • AccessAuditor: 记录每次 GET 操作到审计日志                    │
│    • RateLimiterByPrefix: 按前缀/桶限流                           │
│                                                                  │
│  Phase 4 — 配置化（可选）：                                       │
│    // 通过环境变量或配置文件启用钩子                                │
│    HOOKS='[                                                       │
│      {"name":"auto-tagger","tenant":"*","config":{"source":"web"}},│
│      {"name":"file-validator","tenant":"compliance",              │
│       "config":{"allowed_types":["pdf","docx"]}}                  │
│    ]'                                                             │
│                                                                  │
│  非目标：                                                          │
│    • 不做 WASM/插件动态加载（Phase 1 从编译时注册开始）            │
│    • 不做远程钩子（与 EventBus + Webhook 重叠）                    │
│    • 不做钩子热加载（Phase 1 从启动时注册开始）                    │
└──────────────────────────────────────────────────────────────────┘
```

### 实现理由

**商业价值：** 扩展能力是企业级平台与"玩具项目"的分水岭。没有扩展点，每个用户的定制需求都需要维护一个 fork——这既增加了维护成本，也使用户无法获得上游更新。提供扩展点意味着 AeroVault 可以被嵌入到各种不同的业务场景中，而无需核心团队为每一个定制需求编写代码。

**技术必要性：** 当前架构中已经有扩展"种子"——`EventSink` 和 `ChunkCleaner`——但它们的设计是特定用途的、不可组合的。一个通用的钩子系统可以将这些零散的扩展点统一为一个可预测的架构模式。

**代码复杂度：** 低到中。Phase 1 钩子接口 + 注册表约 100 行 Go。FileService 集成约 100 行（每个方法增加 5-10 行钩子调用）。内置钩子示例各约 30-50 行。总体 ~300 行核心 + ~200 行示例。

---

## 5. 🟡 性能基准测试与容量规划框架

### 为什么需要它

AeroVault 当前没有任何性能基准测试（benchmark）、负载测试（load test）或容量规划指南。这意味着：

```
"我们的生产环境需要支撑每分钟 10 万次写入，100 万个对象，每个对象平均 50KB。"
→ 需要多少台机器？什么 CPU/内存/磁盘规格？
→ SQLite 还是 Postgres？local 还是 S3 存储？
→ 搜索延迟会是多少？p95/p99？
→ 没有任何数据可以回答这些问题。
→ 运维人员只能："先部署到一台机器上试试，不行再加。"
→ 这种"试错"模式在生产环境中代价极高。
```

**没有性能基线导致的后果：**

| 场景 | 问题 | 真实案例 |
|------|------|---------|
| 容量规划 | "这个配置能撑多少流量？" | 无法回答 → 过度配置浪费成本，或欠配置导致宕机 |
| 升级验证 | "新版本性能有没有回退？" | CI 不检查性能 → 性能退化一直到上线才发现 |
| 硬件选型 | "用 NVMe 还是 SSD？" | 没有 IOPS/延迟数据支持决策 |
| 配置调优 | "DB 连接池设多大？缓存多大？" | 默认配置不一定最优，但没有调优依据 |
| SLO 定义 | "我们的 p99 延迟目标是多少？" | 没有基线数据就无法定义合理目标 |

### 当前状态

```makefile
# Makefile — 没有 benchmark 相关 target
# 只有:
#   test, cover, test-integration, test-integration-qdrant
# 没有:
#   benchmark, bench, loadtest
```

```bash
# 当前测试全是功能测试 + 集成测试 — 零性能测试
$ go test ./...              # ✅ 功能正确性
$ make test-integration      # ✅ Postgres 集成
$ make test-integration-qdrant # ✅ Qdrant 集成
$ go test -bench=. ./...     # ❌ 不存在的 target，输出 "no benchmark files"
```

| 代码位置 | 当前状态 | 缺失 |
|---------|---------|------|
| `internal/` 全部子包 | 无 `*_benchmark_test.go` 文件 | 零基准测试 |
| `Makefile` | 无 `benchmark` target | 无可重复运行的基准测试套件 |
| `deploy/` | 有 docker-compose + Helm | 无性能测试配置文件 |
| `scripts/` | 脚本目录存在 | 无负载测试脚本 |
| `docs/` | 有 deployment.md、configuration.md | 无容量规划文档 |
| `README.md` | 功能特性列表 | 无性能指标或硬件建议 |

### 边界情况暴露

| Edge Case | 场景 | 后果 |
|-----------|------|------|
| **不同后端的性能差异** | local 存储 vs S3 vs OSS | 基准测试需要覆盖所有后端组合 |
| **冷启动 vs 热运行** | 刚启动时缓存为空 | 首次请求延迟远高于稳态 |
| **写入并发影响读取** | 写密集场景下读延迟 | 需要混合负载场景的基准 |
| **大文件 vs 小文件** | 4KB 对象 vs 4GB 对象 | 性能特征完全不同 |
| **AI 管线性能** | Vector 搜索 vs BM25 vs Hybrid | 不同检索模式有不同的延迟/吞吐量特性 |
| **DB 选择的影响** | SQLite 本地 vs Postgres 远程 | 数据库延迟对整体性能的贡献差异巨大 |

### 架构方向

```
┌──────────────────────────────────────────────────────────────────┐
│              性能基准测试与容量规划框架                              │
│                                                                  │
│  Phase 1 — Go 标准基准测试（Benchmark）：                          │
│                                                                  │
│  新增文件示例：internal/service/benchmark_test.go                  │
│    func BenchmarkPut1KB(b *testing.B)                              │
│    func BenchmarkPut1MB(b *testing.B)                              │
│    func BenchmarkGet1KB(b *testing.B)                              │
│    func BenchmarkGet1MB(b *testing.B)                              │
│    func BenchmarkDelete(b *testing.B)                              │
│    func BenchmarkList1000(b *testing.B)                            │
│    func BenchmarkSearchVector(b *testing.B)                        │
│    func BenchmarkSearchBM25(b *testing.B)                          │
│    func BenchmarkMultipart(b *testing.B)                           │
│    func BenchmarkBatchDelete(b *testing.B)                         │
│                                                                  │
│  每个 Benchmark 支持变体：                                         │
│    • 对象大小（1KB, 64KB, 1MB, 64MB, 1GB）                        │
│    • 并发数（1, 10, 100）                                         │
│    • 存储后端（local, s3-mock）                                   │
│    • 加密开关                                                     │
│    • 版本化开关                                                   │
│                                                                  │
│  运行方式：                                                        │
│    go test -bench=. -benchmem ./internal/service/                  │
│    go test -bench=BenchmarkPut -benchtime=10s ./...               │
│                                                                  │
│  Phase 2 — Makefile 性能测试套件：                                 │
│                                                                  │
│  Makefile 新增：                                                   │
│    bench:                                                          │
│        go test -bench=. -benchmem -benchtime=5s -count=3 \        │
│          ./internal/service/ ./internal/ai/ ./internal/storage/   │
│                                                                  │
│    bench-compare:  # 比较两次运行（如升级前后）                    │
│        go test -bench=. -benchmem -count=5 -benchtime=5s \        │
│          ./internal/service/ -o bin/bench.new                      │
│        # 与基准结果比较                                            │
│                                                                  │
│    bench-full:  # 全量基准（耗时较长）                             │
│        go test -bench=. -benchtime=3s -count=1 ./... 2>&1 \       │
│          | tee bin/bench-$(date +%Y%m%d).txt                       │
│                                                                  │
│  Phase 3 — 负载测试场景（k6/locust）：                              │
│                                                                  │
│  scripts/loadtest/：                                               │
│    get-heavy.js        # 80% GET, 20% PUT                        │
│    put-heavy.js        # 80% PUT, 20% GET                        │
│    mixed.js            # 50/50 GET/PUT                           │
│    search-heavy.js     # 大量并发搜索                              │
│    multipart.js        # 大文件分片上传                             │
│    batch.js            # 批量操作场景                              │
│    ai-intensive.js     # 持续搜索+聊天                             │
│                                                                  │
│  配合 docker-compose.demo.yml 可复现运行：                        │
│    make loadtest-get-heavy                                         │
│                                                                  │
│  Phase 4 — 容量规划文档（docs/capacity-planning.md）：              │
│                                                                  │
│  文档内容：                                                        │
│    • 硬件建议（开发/预发布/生产）                                  │
│    • 不同后端的基准数据（local/S3/OSS）                            │
│    • 推荐并发连接数                                                │
│    • 内存/CPU/磁盘的预估公式                                       │
│    • 常见瓶颈与调优建议                                            │
│    • 基线 SLO 建议                                                  │
│                                                                  │
│  示例表：                                                          │
│    ┌────────────────────┬────────────┬──────────┬────────────┐   │
│    │ 场景               │ 吞吐量     │ p99延迟  │ 硬件配置    │   │
│    ├────────────────────┼────────────┼──────────┼────────────┤   │
│    │ 小文件密集写入      │ 5000 req/s │ 200ms    │ 4CPU/16GB  │   │
│    │ 大文件上传 (100MB)  │ 100 req/s  │ 5s       │ 8CPU/32GB  │   │
│    │ 向量搜索 (100K obj) │ 200 req/s  │ 50ms     │ 4CPU/8GB   │   │
│    │ 混合负载           │ 2000 req/s │ 500ms    │ 8CPU/32GB  │   │
│    └────────────────────┴────────────┴──────────┴────────────┘   │
│                                                                  │
│  Phase 5 — CI 性能回归检测（可选）：                               │
│    • PR CI 中运行关键基准测试                                    │
│    • 比较与 main 分支的性能差异                                    │
│    • 超过 10% 退化 → 告警（不阻断，仅通知）                       │
└──────────────────────────────────────────────────────────────────┘
```

### 实现理由

**商业价值：** 性能数据是企业采购决策的核心输入。没有基准测试数据，客户无法回答"我需要多大的基础设施"这个最基本的问题。容量规划文档将"不确定性"转化为"可预测的部署模型"，显著降低客户的评估成本和风险感知。

**技术必要性：** 当前没有性能回归检测，意味着每次代码变更都有潜在的性能退化风险。一个简单的 PUT 路径优化可能无意中增加了内存分配，但因为没有基准测试，这个退化会在 CI 中静默通过，直到生产环境中才暴露。

**代码复杂度：** 低。Go 标准库 `testing.B` 原生支持基准测试，不需要任何额外的依赖。每个 benchmark 函数约 20-40 行。初期 10-15 个 benchmark 覆盖核心路径即可。k6 脚本约 30-50 行每个场景。

---

## 总结：实施优先级

| 方向 | 影响 | 复杂度 | 风险 | 为什么优先 |
|------|------|--------|------|-----------|
| 1. 🔴 SDK 跨语言完整性 | 开发者采纳 | 中 | 低（纯新增代码，不触及核心） | 开发者体验是 API 产品的门面 |
| 2. 🔴 Web UI 管理控制台 | 非技术用户采纳 | 低-中 | 低（纯 UI，不触及后端） | 管理 API 已完整，UI 是最后一块拼图 |
| 3. 🟠 导入/迁移工具集 | 客户获取 | 中 | 低（独立工具，不触及核心） | 消除从"试用"到"上线"的最大障碍 |
| 4. 🟠 插件/扩展系统 | 平台生态 | 中 | 中（修改 FileService 核心路径） | 需要周全设计 + 向后兼容 |
| 5. 🟡 基准测试与容量规划 | 运维/可靠性 | 低 | 低（纯测试代码 + 文档） | 成本最低，但为所有后续开发提供性能基线 |

**建议实施阶段：**

| Phase | 方向 | 工作量估计 | 交付价值 |
|-------|------|-----------|---------|
| **Phase 1（1-2 周）** | #5 Benchmark Phase 1-2 | ~1 人天 | 建立性能基线，CI 可观测 |
| **Phase 1（1-2 周）** | #1 SDK Phase 1（Go 优先） | ~3-5 人天 | Go SDK 覆盖所有 REST 端点 |
| **Phase 2（2-3 周）** | #2 Web UI Admin Console | ~3-5 人天 | 非技术用户可自助管理 |
| **Phase 2（2-3 周）** | #1 SDK Phase 2（Python/JS + 集成测试） | ~3-5 人天 | 三 SDK 一致 + 契约测试 |
| **Phase 3（3-4 周）** | #3 Import/Migration Tooling | ~5-8 人天 | 客户迁移路径就绪 |
| **Phase 4（4-6 周）** | #4 Plugin/Hook System | ~5-10 人天 | 平台可扩展性基础设施 |

**核心建议：** 以 **#5 基准测试**（最低成本，立即可用）和 **#1 SDK 补齐**（最高开发者影响）作为 Phase 1 快速赢取，然后集中火力在 **#2 Web UI 管理控制台**（补齐管理 API→UI 的断裂层）和 **#3 迁移工具**（打通客户获取的最后瓶颈）。**#4 插件系统**虽然架构价值最高，但需要更周全的设计，可以在前四个方向完成后进入深度设计阶段。

---

> *第十八期全局扫描完成，未修改任何代码。本轮 5 个方向聚焦于"用户采纳 / 开发者体验 / 运维就绪"视角——不是架构缺失（v1–v17 已覆盖 ~85 个方向），而是"从技术项目到商业产品的最后一公里"。每个方向瞄准一个明确的"半成品"或"空壳"证据：API 已实现但 SDK 未暴露（#1）、后端已完整但 UI 是空壳（#2）、基础设施已就绪但无迁移工具（#3）、核心逻辑硬编码但无扩展点（#4）、功能测试完善但零性能基线（#5）。*
