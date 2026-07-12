All verifications complete. Here is my comprehensive validation report:

---

## ✅ 验证结论：全部 5 个方向均为真实工程缺口

### 方向一：内容感知增量重索引 — ✅ **Confirmado**

| 验证点 | 代码位置 | 验证结果 |
|--------|---------|---------|
| `IndexObjectByID` 无 skip-if-unchanged | `internal/ai/indexer.go:176-218` | ✅ 函数全量读取→提取→分块→嵌入→写入，无任何跳过逻辑 |
| Extract 总是从零开始 | `internal/ai/extractor.go:44-64` | ✅ `DefaultExtractor.Extract()` 读取 `io.Reader` 并复制到 `strings.Builder`，无缓存 |
| Chunk 总是分块 | `internal/ai/chunker.go:14-38` | ✅ `Chunk()` 每次从字符串拆分，无复用机制 |
| ETag 字段存在但未用于索引跳过 | `internal/repository/repository.go:21-40` | ✅ `Object.ETag` 在 struct 中，但 `IndexObjectByID` 不使用 |
| `last_indexed_etag` 或类似字段 | 全库 grep | ✅ **不存在** — 无字段、无迁移、无列 |
| `ReindexStale` 仅检测模型漂移 | `internal/ai/indexer.go:221-238` | ✅ 仅比较 `embed_model`，不比较内容变更 |
| 最新迁移版本 | `0024` | ✅ 可添加 `0025` 迁移 |

**补充发现：** `IndexObjectByID` 对于每次事件（包括 `object.created` 带版本覆盖、`metadata/tags` 更新触发的重索引、`AI_REINDEX_STALE_ON_START`）均执行全量操作，无增量跳过。`RemoteExtractor`（`extractor_remote.go`）将整个对象发送到外部服务，无内容变更时额外浪费网络带宽。

---

### 方向二：非版本化桶的并发写入安全 — ✅ **Confirmado**

| 验证点 | 代码位置 | 验证结果 |
|--------|---------|---------|
| `Put` 无乐观锁 | `internal/service/file_crud.go:74-118` | ✅ 顺序为 `store.Put` → `writePutObject`，无任何锁定或 CAS |
| `UpsertObject` 无条件覆盖 | `internal/repository/sql_objects.go:15-48` | ✅ `ON CONFLICT ... DO UPDATE` 无条件 — 无 `WHERE updated_at = $x` |
| S3 `PutObject` 不检查 If-Match | `internal/api/s3compat/handler.go:69-110` | ✅ S3 handler 中 `PutObject` **无条件写入** — 不读取 If-Match/If-None-Match 请求头 |
| REST `Put` 已有写条件检查 | `internal/api/rest/handler.go:44-48` + `conditional.go:65-85` | ✅ REST handler 有 `checkWritePreconditions` — **协议不一致** |
| S3 `GetObject` 有条件检查 | `internal/api/s3compat/conditional.go:34-86` | ✅ GET/HEAD 有条件支持，但 PUT 没有 |

**补充发现：** 这三处形成**多协议一致性缺口** — REST 有 If-Match 写条件 → 通过 `checkWritePreconditions`；S3 只有读条件 → 通过 `evalS3GetPreconditions`；WebDAV 和 MCP 的 Put 路径无条件检查。`store.Put` 成功但 `writePutObject` 失败时的 **孤儿 blob** 问题真实存在（文件已记录"storage object orphaned"日志但未清理）。

---

### 方向三：存储后端健康与容量 API — ✅ **Confirmado**

| 验证点 | 代码位置 | 验证结果 |
|--------|---------|---------|
| Storage 接口无 Health/Capacity | `internal/storage/storage.go:58-104` | ✅ 接口仅定义对象操作方法，无 `Health()` 或 `Capacity()` |
| `readyzHandler` 仅 Stat 探测 | `cmd/server/main.go:168-181` | ✅ 仅 `store.Stat("@healthz/probe")` — 检查后端是否存活，无容量信息 |
| LocalStorage 无磁盘空间检查 | `internal/storage/local.go:83-97` | ✅ `NewLocal` 仅创建目录，无 `Statfs_t` 调用 |
| 507 InsufficientStorage | 全库 grep | ✅ 仅 REST handler 的配额超限返回 507，存储层不做容量预检 |

**补充发现：** 引用 v55 方向三的表述准确 — v55 聚焦 `storage_health` 和 `storage_capacity_bytes` **Prometheus 指标**的添加，而非可编程 API。与本期方向三「暴露 REST 可查询的健康/容量端点」互补无重叠。

---

### 方向四：桶策略 JSON 解析缓存 — ✅ **Confirmado**

| 验证点 | 代码位置 | 验证结果 |
|--------|---------|---------|
| `checkBucketPolicy` 每请求解析 | `internal/api/s3compat/handler.go:48-60` | ✅ `GetBucketConfig`（DB 查询）+ `auth.ParsePolicy`（JSON 解析）每次调用 |
| `ParsePolicy` 全量 `json.Unmarshal` | `internal/auth/policy.go:44-62` | ✅ `json.Unmarshal` → 遍历 Statement 构建条件映射 — 无缓存 |
| 无任何策略缓存机制 | 全库 grep | ✅ 零命中 |
| `key_cache.go` 已存在类似模式 | `internal/auth/key_cache.go` | ✅ **有意思的发现** — auth 包已有 TTL 边界的 API key 缓存基础设施，可复用模式 |

**补充发现：** 引用 v57 方向四的表述准确 — 仅表格一行提及"策略评估缓存（TTL 30s）"作为 IAM 策略深度的子项概念，**零架构分析、零代码锚点**。本期的缓存设计（TTL 方案 + 代码锚点 `checkBucketPolicy` → `ParsePolicy` → `Eval`）是全新分析。

---

### 方向五：Web UI Admin 面板 — ✅ **Confirmado**

| 验证点 | 代码位置 | 验证结果 |
|--------|---------|---------|
| 当前 UI 无 Admin 标签页 | `internal/webui/static/index.html` | ✅ 282 行 SPA — 4 个标签页（search/detail/lineage/chat），无 admin |
| Admin API 完整 | `internal/api/rest/admin.go` (411 行) | ✅ 12+ Admin 端点：租户、Key、Job、审计、Webhook 失败 |
| Admin 路由注册 | `internal/api/rest/router.go:108-122` | ✅ 全部注册，但 UI 未消费 |
| 渐进式 Admin 标签页 vs 全新构建 | 去重验证 | ✅ v46 聚焦现有 UI 质量提升（错误处理、加载态、XSS）；v30 聚焦全新独立 Admin Console 构建；本期聚焦**渐进式在现有 SPA 侧边栏新增 Admin 标签页** |

**补充发现：** admin API 的认证需要 `Authorization: Bearer <operator-key>` — 但当前 UI 的 `headers()` 函数仅支持 `X-Aero-Tenant` 和可选的 `Bearer` token。Admin 标签页需要扩展认证 UI（要求用户额外输入操作员 API Key）。

---

## 去重验证矩阵

| # | 方向 | grep 命令匹配 | 最相关既有分析 | 去重结论 |
|---|------|-------------|--------------|---------|
| 1 | 增量重索引 | `incremental.*index\|index.*skip\|skip.*reindex\|etag.*skip\|content.*hash.*index` → **0 命中** | v7 CAS 去重（块级，非索引跳过） | ✅ **完全去重** |
| 2 | 并发写入安全 | `concurrent.*put\|put.*concurrent\|cas.*put\|compare.*swap.*object\|conditional.*write.*s3\|if-match.*put` → **0 命中** | v55 配额 TOCTOU（不同竞态窗口） | ✅ **完全去重** |
| 3 | 健康/容量 API | `storage.*health.*api\|capacity.*api\|Health.*Storage.*method` → **0 命中** | v55 指标收集（互补非重叠） | ✅ **互补关系** |
| 4 | 策略解析缓存 | `policy.*cache\|ParsePolicy.*cache\|bucket.*policy.*cache` → **0 命中** | v57 表格一行概念提及（零分析） | ✅ **完全去重** |
| 5 | Web UI Admin 面板 | `admin.*web.*ui\|web.*admin.*panel\|admin.*dashboard.*web` → **0 命中** | v46 UI 硬化 / v30 全新 Console（非渐进式） | ✅ **完全去重** |

---

## 综合优先级调整建议

基于代码深度验证，建议微调优先级矩阵：

| # | 方向 | 推荐优先级 | 原因 |
|---|------|-----------|------|
| **1** | 增量重索引 | **P1** | 代码验证：每次事件都触发全量重索引，大对象场景下成本浪费可量化（Embed API 按 token 付费），且已有 ETag 可作为轻量检测手段 |
| **2** | 并发写入安全 | **P1** | 代码验证：发现了**更强的多协议不一致证据** — REST 已有写条件检查但 S3/WebDAV/MCP 没有，且孤儿 blob 风险真实存在 |
| **3** | 健康/容量 API | **P2** | 代码验证：确认无接口方法、无实现、无 REST 暴露；但与 v55 互补，v55 实现指标后可复用部分逻辑 |
| **4** | 策略解析缓存 | **P2 → P1 降级为 P2** | 代码验证：发现 `key_cache.go` 已有缓存模式可以复用，实现成本更低（实际约 60 行）；P2 级别合适 |
| **5** | Web UI Admin 面板 | **P2** | 代码验证：admin API 完整（411 行），UI 仅需增量约 200-300 行 HTML+JS；无构建步骤依赖 |

**关键额外发现：** `internal/auth/key_cache.go` 已存在 TTL 边界缓存模式，可用于 Direction 4 的策略缓存实现，将估算行数从 ~100 行降低至 ~60 行。

---

## 文档准确性修正

文档中有少量行号偏移（由于代码在分析后可能有微小修改），但不影响结论有效性：

| 文档引用 | 实际位置 | 偏差 | 结论影响 |
|---------|---------|------|---------|
| `indexer.go:251-320` | `indexer.go:176-218` | 函数缩短/重构 | ✅ 无影响 — `IndexObjectByID` 全量处理逻辑未变 |
| `file_crud.go:41-88` | `file_crud.go:74-118` | +33 行偏移 | ✅ 无影响 — Put 函数无条件覆盖逻辑未变 |
| `policy.go:82-86` | `policy.go:82-86` | ✅ 精确 | — |
| S3 handler `checkBucketPolicy` 行号 | `handler.go:48-60` | 代码稳定 | ✅ |
| `webui/static/index.html` 282 行 | 282 行 | ✅ 精确 | — |
