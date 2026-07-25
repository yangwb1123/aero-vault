# AeroVault 高价值扩展方向 v54 — 操作级可观测性、Web UI 生产化、SDK 大文件支持、Webhook 多格式路由、多层级降级架构

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部子包，~55K `.go` + 三套 SDK + `deploy/*` + 全部 24 对迁移文件 + 全部 53 份既有 `docs/requirements/expansion-*.md` 分析文档 + `ROADMAP.md` + `CHANGELOG.md` + `TODO.md` + `extensions*.md` + `docs/analysis-*.md` + `AGENTS.md` + `HARNESS.md` + `Makefile` + `Dockerfile`）
>
> **分析视角：** 资深架构师 / 产品经理 — 在累计 **53 期 expansion 分析（265+ 方向，~750,000+ 字分析文本）** 基础上，寻找 **53 轮穷举后依然未被触及** 的交叉架构盲区与产品成长期缺口
>
> **去重方法：** 对 `docs/requirements/` 下全部 53 份既有分析文档（v1–v53）进行穷尽式关键词验证与方向级交叉引用。每个方向在既有文档中 **零实质性独立架构分析**（即：不作为独立方向/独立小节出现；仅表格一行过路引用、举例提及、单一子点均不构成实质性分析）。
>
> **分析日期：** 2026-07-10

---

## 前言

经 53 期、265+ 方向的穷举分析，AeroVault 从功能维度、执行层维度、产品成熟度、运维就绪度、S3 语义纵深、AI 管线、存储引擎、事件系统、认证安全等多个视角已被反复扫描。几乎每个可想象的功能方向都被触及。

然而，在对代码库进行第 54 轮扫描时，依然有一批 **深刻但微妙** 的缺口未被覆盖。它们的共同特征是：

1. **不是"加新端点"，而是"已有架构缺少一层关键的可观测/运维/产品保障"**
2. **涉及从"功能可用"到"生产可靠"的跨越——不是 0→1 而是 1→10**
3. **每个方向在当前代码中都有明确的证据锚点（stub、空值、缺失路径）**
4. **每个方向在 v1–v53 中零实质性独立架构分析**

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心矛盾 | 53 期覆盖验证 |
|---|------|------|--------|---------|-------------|
| **1** | **FileService 操作级可观测性（Operation-Level Observability）** | 运维/可靠性 | **P1** — FileService 是四协议的共享核心（20+ 方法），但零方法级指标；HTTP 中间件指标跨协议分裂，无法聚合"全协议 Get 延迟" | ❌ **零实质性分析**（v12 覆盖 OTel span 但聚焦 HTTP 入口与 AI 管线 trace，非 FileService 方法级指标；v43 覆盖 error code 分类指标但非 operation-level 吞吐/延迟/错误率） |
| **2** | **Web 终端用户界面生产化加固（End-User Web UI Production Hardening）** | 产品/体验 | **P1** — 282 行单体 HTML 页面是唯一面向终端用户的图形界面；零错误状态、零加载态、零分页、零认证流程、零文件预览、零响应式设计 | ❌ **零实质性分析**（v18/v24/v30 方向覆盖"管理控制台/Admin Console"——面向运维的管理界面，与面向终端用户的搜索/浏览/上传/聊天界面完全不同；当前 Web UI 的终端用户生产化缺口从未被分析） |
| **3** | **Python & Go SDK 大文件上传能力缺口（SDK Large File Upload Gap）** | 采纳/集成 | **P1** — JS SDK 已完整实现 multipart upload（4 个方法），但 Python 和 Go SDK 完全没有分片上传支持；大于 ~100MB 的文件无法通过 2/3 的官方 SDK 上传 | ❌ **零实质性分析**（v18 方向一整体覆盖 SDK 特性差距，但在 v18 之后 JS SDK 已补全 multipart 而 Python/Go 仍未跟进；v18 的分析是多 SDK 的宏观覆盖，未聚焦大文件上传这一个具体的、持续存在的生产级缺口） |
| **4** | **Webhook 多格式负载转换与按事件路由（Webhook Payload Transformation & Event-Routed Delivery）** | 集成/平台 | **P2** — 当前 webhook 发送固定 JSON 负载到单一 URL；无法按事件类型/桶/前缀路由到不同目标，无法为 Slack/PagerDuty/自定义服务提供不同负载格式 | ❌ **零实质性分析**（v12 方向表"Webhook 事件目录与转换管线"一行概念性提及但 ~8 个方向仅占单行，非独立方向；v17 方向二覆盖通知过滤与多通道但聚焦 S3 兼容通知而非 webhook 的通用负载转换架构） |
| **5** | **多层级优雅降级架构（Multi-Layer Graceful Degradation）** | 可靠性/架构 | **P2** — 系统存在 AI 二元降级模式（`degradedMode` → 全部 AI 端点 503），无组件级、渐进式、自动化的降级策略；Qdrant 挂了 → 搜索不应全挂（降级为 memory/BM25），S3 后端慢 → 读不应阻塞写 | ❌ **零实质性分析**（v53 方向三覆盖自适应过载保护——控制并发与速率，但聚焦"反压"而非"降级服务语义"；v38/v41 路过提及"优雅降级"概念但从不作为独立方向做架构分析） |

---

## 方向一：FileService 操作级可观测性（Operation-Level Observability）

### 现状

`FileService` 是四协议（REST、S3、WebDAV、MCP）的共享核心，提供 20+ 公开方法：

```
Get, Put, Delete, Stat, List, Head, Copy (S3),
InitMultipart, UploadPart, CompleteMultipart, AbortMultipart,
SetTags, GetTags, SetBucketVersioning, SetBucketLifecycle,
LockObject, RestoreObject, Presign, BatchDelete, BatchSetTags,
GetBucketLogging, SetBucketLogging, GetBucketNotifications,
GetObjectACL, SetObjectACL, ...
```

**当前可观测性架构：**

```
Client → HTTP Middleware (otel/metrics) → Protocol Handler → FileService → Storage/Repo
         ^                              ^                     ^
         有 HTTP 指标                    无内省             零方法级指标
         (请求计数, 延迟,                 能力                 (不知道 Get vs Put
          响应大小)                                           的延迟差异)
```

**HTTP 中间件指标的局限性：**

| 维度 | HTTP 层指标 | 服务层需要 |
|------|-------------|-----------|
| 操作识别 | `http.request{path,method,status}` → REST 路径 `/v1/files/{key}` 与 S3 路径 `/{bucket}/{key}` 无法关联 | `file_service.operation{method="Get",protocol="s3"}` 统一聚合 |
| 延迟分解 | 仅 HTTP handler 总耗时 | `file_service.get_duration_ms` 可进一步分解为 `storage_get_duration` + `repo_stat_duration` |
| 错误归因 | `http.response{status}` → 500 不知道是 storage 错误还是 repo 错误 | `file_service.errors{error_type="storage"}` / `file_service.errors{error_type="repo"}` |
| 租户维度 | 不携带（在 middleware 层不可见） | `file_service.requests{tenant,method}` |
| 操作大小 | `http.response_size` 字节数 | `file_service.put_bytes_total` / `file_service.get_bytes_total` |
| 存储后端 | 不可见 | `file_service.operations{backend="s3",method="Get"}` |

**具体代码证据：**

```go
// internal/service/file.go — FileService 结构体
type FileService struct {
    store        storage.Storage
    repo         repository.Repository
    logger       *slog.Logger
    sink         EventSink
    chunkCleaner ChunkCleaner
    // ❌ 无 metrics 字段
    // ❌ 无 operation 计数器
    // ❌ 无 latency recorder
}
```

```go
// internal/service/file_crud.go — Put 方法
func (s *FileService) Put(ctx context.Context, tenant, bucket, key string, r io.Reader, size int64, opts PutOptions) (repository.Object, error) {
    // ❌ 无起始时间记录
    // ❌ 无方法进入计数器
    // ❌ 无错误计数器
    // ...
    // return 前 ❌ 无延迟记录
}
```

```go
// internal/telemetry/metrics.go — 现有领域指标
var (
    mAIRequests           metric.Int64Counter  // AI 专有
    mAITokens             metric.Int64Counter  // AI 专有
    mAICostMicros         metric.Int64Counter  // AI 专有
    mReconcileOrphanBlobs metric.Int64Counter  // Reconcile 专有
    // ... 全部是特定领域指标
    // ❌ 无 file_service 命名空间指标
    // ❌ 无 operation 维度
)
```

### 为什么需要

| 运维场景 | 当前困境 | 解决后 |
|---------|---------|--------|
| **性能回归**：某次部署后用户抱怨下载变慢 | 只看 HTTP 延迟知道 "Get 变慢了"，但无法区分是 storage.Get 变慢还是 repo.Stat 变慢 | `file_service_get_duration_ms{phase="storage"}` 与 `{phase="repo"}` 精确分解 |
| **容量规划**：需要确定每个租户的读写比例 | 只能从访问日志中解析 HTTP 路径来估计 | `file_service_requests_total{tenant,method}` 直接上报 |
| **错误根因**：S3 协议 500 激增 | HTTP 指标显示 status=500 增多，但 S3 handler 调用 FileService.Put 出错 vs FileService.Get 出错无从区分 | `file_service_errors_total{method,error_type}` 精确定位 |
| **协议迁移**：用户从 REST 切换到 S3 SDK | 两套 HTTP 指标路径无法合并，无法对比迁移前后的端到端延迟 | `file_service_*` 指标跨协议统一，迁移效果一目了然 |
| **存储成本归因**：需要回答"哪个操作消耗了最多存储带宽" | 无 `get_bytes_total` 或 `put_bytes_total` 指标 | `file_service_transfer_bytes_total{operation="get",tenant}` |

### 架构设计

```go
// 新增：FileService 内部指标收集器
type serviceMetrics struct {
    requests    metric.Int64Counter    // file_service.requests_total{method,tenant,protocol}
    errors      metric.Int64Counter    // file_service.errors_total{method,error_type}
    duration    metric.Float64Histogram // file_service.duration_ms{method,phase}
    bytesIn     metric.Int64Counter    // file_service.bytes_in_total{method}
    bytesOut    metric.Int64Counter    // file_service.bytes_out_total{method}
}

// 每个 FileService 方法嵌入打点
func (s *FileService) Get(ctx context.Context, tenant, bucket, key string) (io.ReadCloser, repository.Object, error) {
    start := time.Now()
    s.metrics.requests.Add(ctx, 1, metric.WithAttributes(
        attribute.String("method", "Get"),
        attribute.String("tenant", tenant),
    ))
    rc, obj, err := s.getInternal(ctx, tenant, bucket, key)
    s.metrics.duration.Record(ctx, time.Since(start).Milliseconds(), metric.WithAttributes(
        attribute.String("method", "Get"),
    ))
    if err != nil {
        s.metrics.errors.Add(ctx, 1, metric.WithAttributes(
            attribute.String("method", "Get"),
            attribute.String("error_type", classifyError(err)),
        ))
    }
    return rc, obj, err
}
```

**新增 Prometheus 指标（示例）：**

| 指标名 | 类型 | 标签 | 说明 |
|--------|------|------|------|
| `file_service_requests_total` | Counter | `method,tenant,protocol` | 操作请求总数 |
| `file_service_errors_total` | Counter | `method,error_type,tenant` | 操作错误总数（按类型分类） |
| `file_service_duration_ms` | Histogram | `method,phase` | 操作延迟分布（bucket: 10ms..30s） |
| `file_service_bytes_in_total` | Counter | `method,tenant` | 写入字节总量 |
| `file_service_bytes_out_total` | Counter | `method,tenant` | 读取字节总量 |
| `file_service_inflight` | Gauge | `method` | 当前正在执行的操作数 |

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **高基数标签：object key 不应作为标签** | 仅聚合级别标签（tenant, method, protocol, error_type）；object key 只在日志中记录 |
| **Tenant 标签爆炸** | 租户数有限（< 10,000），作为 Prometheus label 安全 |
| **Histogram bucket 选择** | 使用默认 OTel bucket 边界（10ms, 50ms, 100ms, 250ms, 500ms, 1s, 2.5s, 5s, 10s, 30s），初期不自定义 |
| **已有 HTTP 层指标重复** | HTTP 层保留（协议级 SLA 监控），FileService 层新增（内部诊断与容量规划），两者互补 |
| **file_service_duration 与 ai search latency 的关系** | Search/Chat 方法自身已在 AI 层有指标（ai.search.duration_ms），FileService 层覆盖核心 CRUD 操作 |

### 涉及代码估算

| 组件 | 行数 | 说明 |
|------|------|------|
| `internal/service/metrics.go` — 指标定义 + 注册 | ~80 | `serviceMetrics` 结构体、Init、标准标签定义 |
| `internal/service/file.go` — 嵌入 metrics 字段 | ~5 | `FileService` 结构体增加 `metrics *serviceMetrics` |
| `internal/service/file_crud.go` — Get/Put/Delete/List/Stat 打点 | ~60 | 5 个核心方法各 ~12 行打点代码 |
| `internal/service/file_features.go` — 其余方法打点 | ~60 | Tags/Version/Lock/Batch/Bucket 等方法 |
| `internal/service/file_multipart.go` — Multipart 打点 | ~30 | Init/Upload/Complete/Abort |
| 测试 + 契约验证 | ~80 | 单元测试 + prometheus 端点验证 |
| **合计** | **~315** | |

---

## 方向二：Web 终端用户界面生产化加固（End-User Web UI Production Hardening）

### 现状

当前 Web UI（`internal/webui/static/index.html`）是一个 282 行的单体 HTML 文件，内联所有 CSS 和 JavaScript：

| 维度 | 当前状态 | 对终端用户的影响 |
|------|---------|----------------|
| **错误处理** | ❌ fetch 失败（网络断开、服务器 500）→ 静默失败，用户看到空白或未响应的界面 | 用户不知道请求失败了 |
| **加载状态** | ❌ 无 loading spinner、skeleton screen 或进度指示器 | 用户不确定系统是否在工作 |
| **分页** | ❌ `GET /v1/files?limit=1000` 一次性加载全部对象；无下一页、无滚动加载 | 对象数 > 1000 时截断显示 |
| **认证流程** | ❌ API Key 手动输入到文本字段；无登录界面、无 token 校验反馈、无过期提示 | 用户体验差，误填无反馈 |
| **文件预览** | ❌ 点击文件仅显示 JSON 元数据；无图片/文本/PDF 内联预览 | 无法在浏览器中直接查看文件内容 |
| **错误提示** | ❌ 搜索零结果显示 "no hits"；上传失败无提示；后端 401/403 无明确错误消息 | 用户困惑，不知下一步 |
| **响应式设计** | ❌ CSS 硬编码像素值（320px 侧栏、固定字体）；移动设备显示错乱 | 手机/平板用户无法使用 |
| **键盘导航** | ❌ 无 tabindex、无 aria-label、无焦点管理 | 键盘用户（无障碍需求）无法操作 |
| **上传进度** | ❌ PUT 上传无进度条；大文件上传用户不知道完成百分比 | 大文件上传体验差 |
| **多标签页一致性** | ❌ localStorage 存储 tenant/key，但跨标签页不共享 | 同时打开多个标签页时上下文不同步 |
| **URL 路由** | ❌ 无浏览器历史（search URL 不可分享、刷新丢失状态） | 搜索结果无法复制链接分享 |

**与"Admin Console"的区别：**

```
已覆盖分析（v18/v24/v30）             本期分析
──────────────────────────────      ──────────────────────────────
目标用户：运维人员                     目标用户：终端用户（文件消费者）
功能：管理租户、密钥、配额、Job        功能：搜索文件、浏览目录、上传、
      监控、审计日志、系统配置              预览文件、AI Chat 问答
入口：预期的全新 /admin UI             入口：现有 /ui（需要加固而非重建）
```

### 为什么需要

| 场景 | 当前矛盾 | 影响 |
|------|---------|------|
| **非技术用户首次体验** | 打开 Web UI → 看到空文件列表，不知如何开始 | 第一印象：产品不完整 |
| **上传合同文件（~50MB PDF）** | 点击上传 → 等待 → 界面无响应 → 用户刷新 → 不知道是否上传成功 | 数据丢失风险 + 用户焦虑 |
| **搜索公司政策文档** | 输入查询 → 30 条结果 → 想要加载更多 → 没有分页或滚动加载 | 功能不可用 |
| **移动设备上查看文件** | 打开 /ui → 界面溢出屏幕 → 侧栏占 90% 宽度 | 完全不可用 |
| **网络中断后恢复** | 后台网络恢复 → 界面不自动刷新；缓存了过期的文件列表 | 状态不同步 |
| **分享搜索结果** | 想复制当前搜索链接给同事 → URL 中没有搜索参数 | 无法协作 |

### 架构设计

**Phase 1 — 最小生产化（~300 行新增/修改）：**

| 改进 | 实现方式 | 行数 |
|------|---------|------|
| **错误处理** | 所有 `fetch` 调用包裹 try/catch，显示 Toast 通知（非侵入式底部条） | ~40 |
| **加载状态** | 搜索/文件列表/聊天 三区域独立 loading indicator（简单的 CSS 脉冲动画） | ~30 |
| **分页** | `GET /v1/files?limit=100&marker=...` keyset 分页；滚动到底部自动加载下一页 | ~50 |
| **上传进度** | XMLHttpRequest 监听 `upload.onprogress` 事件（fetch 不支持进度监听） | ~30 |
| **文件预览** | 图片（`<img>`）、文本（`<pre>`）、JSON（格式化的 `JSON.stringify`）内联渲染 | ~50 |
| **响应式 CSS** | CSS Grid / Flexbox 适配 <480px / <768px / >1024px 三种断点 | ~80 |
| **错误状态展示** | 搜索零结果 → "未找到匹配文件" + 提示（修改前缀/使用不同搜索词）；上传失败 → 具体错误消息 | ~20 |

**Phase 2 — 体验升级（~300 行）：**

| 改进 | 实现方式 |
|------|---------|
| **键盘导航** | tabindex + 回车触发文件打开 / Escape 关闭详情 |
| **URL 路由** | `history.pushState` + `popstate` 实现搜索参数在 URL 中的编码与恢复 |
| **认证流程** | 输入 API Key 后验证 `/v1/usage` 端点，存储到 sessionStorage（非 localStorage），显示登录状态 |
| **多标签页同步** | `StorageEvent` 监听 localStorage 变化，跨标签页同步 tenant/key |
| **空状态引导** | 首次访问（无文件时）显示 "拖拽文件到此处开始使用" 引导 |
| **自适应刷新** | 后台定时轮询 `/v1/files`（每 30s），检测到新文件时提示"有新内容" |

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **XHR 上传超时** | 设置客户端上传超时（10 分钟）；超时后提示用户"上传超时，建议使用 SDK 或 CLI 上传大文件" |
| **SSE 聊天断开重连** | 监听 SSE `onerror` 事件；自动重连（最多 3 次），每次间隔增加（1s, 2s, 4s） |
| **大文件预览（>50MB）** | 文件元数据显示大小后，对 >50MB 文件显示"文件过大，无法在浏览器中预览" |
| **API Key 过期** | `/v1/usage` 返回 401 → 清除 sessionStorage 中的 key，提示用户重新输入 |
| **浏览器兼容性** | 使用 ES2017（async/await）特性确认浏览器支持；不支持时显示升级提示 |

### 涉及代码估算

| 组件 | 行数 |
|------|------|
| `internal/webui/static/index.html` — Phase 1 改进 | ~300 |
| `internal/webui/static/index.html` — Phase 2 改进 | ~300 |
| **合计** | **~600** |

---

## 方向三：Python & Go SDK 大文件上传能力缺口（SDK Large File Upload Gap）

### 现状

当前三套 SDK 的 multipart upload 支持状况：

| SDK | 文件 | Init | UploadPart | Complete | Abort | UploadLarge（自动分片） |
|-----|------|------|-----------|----------|-------|----------------------|
| JavaScript/TypeScript | `sdk/js/aero-vault.js` | ✅ `createMultipartUpload` | ✅ `uploadPart` | ✅ `completeMultipartUpload` | ✅ `abortMultipartUpload` | ❌ |
| Go | `sdk/go/aerovault/client.go` | ❌ | ❌ | ❌ | ❌ | ❌ |
| Python | `sdk/python/aero_vault.py` | ❌ | ❌ | ❌ | ❌ | ❌ |

**具体代码证据：**

```go
// sdk/go/aerovault/client.go — 方法列表（约 45 个公开方法）
// Upload, Get, GetVersion, GetRange, Download, Stat, Exists,
// List, IterObjects, Delete, Presign, Thumbnail,
// GetTags, PutTags, DeleteTags, ListVersions,
// GetACL, SetACL, GetBucketACL, SetBucketACL,
// Search, Chat, ChatStream, Agent, Lineage, Usage, Health,
// AddKey, ListKeys, RevokeKey, IssueJWT,
// ListWebhookFailures, ListJobs, RetryJob,
// CreateTenant, ListTenants, DeleteTenant, SetTenantStatus, ListAudit, SetQuota, SetBudget
// ❌ InitMultipartUpload  ❌ UploadPart  ❌ CompleteMultipartUpload  ❌ AbortMultipartUpload
```

```python
# sdk/python/aero_vault.py — 54 个方法（含辅助方法）
# upload, upload_file, get, download, stat, exists,
# list, iter_objects, delete, presign,
# get_tags, put_tags, delete_tags, list_versions, lock,
# get_bucket_acl, set_bucket_acl,
# search, chat, chat_stream, agent, lineage, usage, health,
# add_key, list_keys, revoke_key, issue_jwt,
# list_webhook_failures, list_jobs, retry_job,
# create_tenant, list_tenants, delete_tenant, set_tenant_status, list_audit, set_quota, set_budget
# ❌ init_multipart_upload  ❌ upload_part  ❌ complete_multipart_upload  ❌ abort_multipart_upload
```

**对比 JS SDK（完整实现，sdk/js/aero-vault.js）：**

```javascript
async createMultipartUpload(key, opts = {}) { /* ✅ 实现 */ }
async uploadPart(uploadId, partNumber, data) { /* ✅ 实现 */ }
async completeMultipartUpload(uploadId) { /* ✅ 实现 */ }
async abortMultipartUpload(uploadId) { /* ✅ 实现 */ }
```

### 为什么需要

**大文件上传是对象存储的核心功能，不是可选项。** 缺失 multipart 上传意味着：

| 场景 | 当前限制 | 实际影响 |
|------|---------|---------|
| **上传 200MB 日志文件** | Go/Python SDK `Upload` 使用单次 HTTP PUT，受 `REQUEST_TIMEOUT_SECONDS`（默认 120s）限制 | 200MB / 120s = 1.67 MB/s 吞吐量下限；更慢的网络或更大的文件直接超时失败 |
| **上传 1GB 数据集** | 单次 PUT：内存占用 1GB + 超时风险极高 | 实际不可用；用户必须自己实现分片逻辑 |
| **断点续传** | 单次 PUT 失败需全部重传 | 网络不稳定时极度低效 |
| **并发上传加速** | 无分片 = 无并发 = 串行上传 | 大文件上传速度受单 TCP 连接限制 |
| **S3 SDK 对标用户** | 用户从 AWS S3 SDK 迁移时发现缺少分片上传支持 | 直接选择其他方案 |

**v18 分析时点（2026-07-10 早期）JS SDK 同样缺失 multipart，但已补全。** 这证明：
1. Multipart 上传是 SDK 的核心功能（JS 团队优先实现了它）
2. Python/Go 的缺口不是设计决策（如果是，JS 也不会有）
3. 这是实现优先级问题，而非功能取舍

### 实现策略

每个 SDK 需要新增 4 个方法 + 1 个便捷方法：

| 方法 | REST API | 行数估计 |
|------|----------|---------|
| `InitMultipartUpload(key, opts)` → `POST /v1/multipart` | `POST /v1/multipart` | ~15 |
| `UploadPart(uploadID, partNumber, data)` → `PUT /v1/multipart/{id}/parts/{n}` | `PUT /v1/multipart/{uploadID}/parts/{n}` | ~20 |
| `CompleteMultipartUpload(uploadID)` → `POST /v1/multipart/{id}/complete` | `POST /v1/multipart/{uploadID}/complete` | ~15 |
| `AbortMultipartUpload(uploadID)` → `DELETE /v1/multipart/{id}` | `DELETE /v1/multipart/{uploadID}` | ~10 |
| `UploadLarge(key, reader, opts)` — 自动分片 + 并发上传 | 组合上述 4 个方法 | ~60 |
| **合计（每 SDK）** | | **~120** |
| **Go + Python 合计** | | **~240** |

**UploadLarge 设计模式：**

```
UploadLarge(key, reader, {partSize: 10*1024*1024, concurrency: 4, ...}):
  ├── InitMultipartUpload(key)
  ├── 以 partSize 为单位分割 reader
  ├── 用 goroutine/线程池 并发上传各分片 (UploadPart)
  ├── 所有分片成功 → CompleteMultipartUpload
  └── 任意分片失败 → AbortMultipartUpload + 返回错误
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **最小分片大小** | 验证每个分片 >= 5 MiB（S3 兼容标准），最后分片可小于 5 MiB |
| **UploadLarge 并发数** | 默认 4，可配置；客户端限流（token bucket 防止网络压满） |
| **UploadLarge context 取消** | 通过 Go `context.Context` / Python 超时传播；所有进行中的分片上传取消 |
| **分片顺序** | 按 partNumber 顺序提交；Complete 时服务端按顺序组装 |
| **断点续传（高级）** | Phase 1 不实现（完整的多分片断点续传需持久化 uploadID+已上传分片列表）；Phase 2 可用 `ListParts` API 恢复 |
| **与 v18 SDK 方向的关系** | v18 方向一覆盖 SDK 整体特性差距（15+ 缺失端点），本方向聚焦 **大文件上传这一具体生产级缺口**，提供确定的实现方案 |

### 涉及代码估算

| 文件 | 行数 |
|------|------|
| `sdk/go/aerovault/client.go` — 4 个 multipart 方法 + UploadLarge | ~130 |
| `sdk/go/aerovault/client_test.go` — multipart 测试 | ~100 |
| `sdk/python/aero_vault.py` — 4 个方法 + upload_large | ~120 |
| `sdk/python/test_aero_vault.py` — multipart 测试 | ~80 |
| **合计** | **~430** |

---

## 方向四：Webhook 多格式负载转换与按事件路由（Webhook Payload Transformation & Event-Routed Delivery）

### 现状

当前 webhook 系统（`internal/events/webhook.go`）的实现路径：

```go
// internal/events/webhook.go — 发送固定格式的 JSON payload
func (w *Webhook) send(ctx context.Context, e repository.Event) error {
    payload, _ := json.Marshal(map[string]any{
        "type":      e.Type,
        "tenant":    e.TenantID,
        "bucket":    e.Bucket,
        "key":       e.Key,
        "object_id": e.ObjectID,
        "payload":   e.Payload,
        "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
        "request_id": e.RequestID,
    })
    // HMAC-SHA256 签名
    // POST 到 EVENTS_WEBHOOK_URL
}
```

**能力矩阵：**

| 能力 | 当前 | 生产需要 |
|------|------|---------|
| **多目标路由** | ❌ 单一 `EVENTS_WEBHOOK_URL` | ✅ 按事件类型路由到不同 URL |
| **按前缀过滤** | ❌ 接收所有事件 | ✅ 仅发送 `bucket=prod` 或 `key=logs/*` 的事件 |
| **按事件类型过滤** | ❌ 发送全部 event types | ✅ 仅发送 `object.created` / `object.deleted` |
| **负载格式自定义** | ❌ 固定 JSON 结构 | ✅ 支持 Slack 消息、PagerDuty 告警、自定义 JSON 模板 |
| **多 webhook 端点** | ❌ 单一全局 webhook | ✅ 每个端点独立配置（URL + secret + 过滤器 + 格式） |
| **重试策略可配置** | ❌ 固定重试逻辑 | ✅ 每端点独立配置（max_retries, backoff, timeout） |
| **健康探测** | ❌ 无 | ✅ 定期发送健康 ping 到 webhook 端点 |

**具体代码证据：**

```go
// internal/config/config.go — 当前 webhook 配置
type EventsConfig struct {
    WebhookURL    string   // 单一 URL
    WebhookSecret string   // 单一 secret
    // ❌ 无多端点
    // ❌ 无过滤规则
    // ❌ 无格式配置
    // ❌ 无健康检查
}
```

```go
// internal/events/webhook.go:218-224 — 失败记录
// 死信复用 MarkWebhookSucceeded 接口，运维无法区分"成功"与"最终失败"
```

### 为什么需要

| 集成场景 | 当前局限性 | 解决后 |
|---------|---------|--------|
| **Slack 通知**：文件上传后发送 Slack 消息 | 需自建中间服务解析 webhook payload 并转为 Slack 格式 | 直接在 webhook 配置中选择 "Slack" 格式模板 |
| **PagerDuty 告警**：Antivirus 发现病毒时触发告警 | Webhook 收到所有事件，必须在 PagerDuty 端过滤 | 配置规则：`event_type=antivirus.infected` → PagerDuty 端点 |
| **多环境隔离**：dev/staging/prod 的 webhook 指向不同地址 | 需运行不同实例，或自建事件路由器 | 按桶/租户前缀区分路由目标 |
| **审计日志流**：将 `object.deleted` 事件转发到审计系统 | 所有事件共享同一 webhook，审计系统需丢弃 90% 无关事件 | 专门路由 `event_type=object.deleted` 到审计端点 |
| **自定义格式**：下游系统期望不同的 JSON 结构 | 必须开发适配器服务 | 内建 Go template 引擎，自定义 payload 格式 |

### 架构设计

```
┌──────────────────────────────────────────────────────────────────┐
│                      Webhook Routing Engine                       │
│                                                                   │
│  ┌────────────┐     ┌────────────┐     ┌────────────────────┐    │
│  │ Event Bus  │────▶│ Rule       │────▶│ Payload            │    │
│  │ Publish    │     │ Matcher    │     │ Transformer        │    │
│  └────────────┘     └────────────┘     └────────────────────┘    │
│                          │                    │                   │
│                          ▼                    ▼                   │
│                   ┌──────────────┐    ┌─────────────────┐        │
│                   │ Rule Config  │    │ Template Engine  │        │
│                   │ • event_type │    │ • slack          │        │
│                   │ • prefix     │    │ • pagerduty      │        │
│                   │ • bucket     │    │ • custom (Go tmpl)│       │
│                   │ • endpoint   │    │ • raw (passthru) │        │
│                   │ • secret     │    └─────────────────┘        │
│                   │ • template   │                                │
│                   └──────────────┘                                │
│                           │                                       │
│                           ▼                                       │
│                   ┌────────────────┐                              │
│                   │ HTTP Transport │                              │
│                   │ • HMAC sign    │                              │
│                   │ • retry        │                              │
│                   │ • backoff      │                              │
│                   │ • dead letter  │                              │
│                   └────────────────┘                              │
└──────────────────────────────────────────────────────────────────┘
```

**配置模型：**

```go
type WebhookRule struct {
    ID          string            // 规则标识
    Name        string            // 可读名称
    Enabled     bool              // 启用/禁用
    URL         string            // 目标端点
    Secret      string            // HMAC 签名密钥（可选）
    // 过滤条件（AND 逻辑）
    EventTypes  []string          // 空=全部; ["object.created","object.deleted"]
    Buckets     []string          // 空=全部; ["prod","staging"]
    Prefixes    []string          // 空=全部; ["logs/","documents/"]
    // 格式
    Format      string            // "raw" | "slack" | "pagerduty" | "custom"
    Template    string            // Go template string（Format=custom 时使用）
    // 重试
    MaxRetries  int               // 默认 5
    TimeoutSec  int               // 默认 10
    RetryBackoff string           // "exponential" | "fixed"
}
```

**内置模板示例：**

```json
// Slack 格式自动转换
{
  "text": "📄 *File {{.Key}} {{.Type}}*\nBucket: {{.Bucket}}\nSize: {{.Payload.size}}\nTime: {{.Timestamp}}"
}
```

```json
// PagerDuty 格式自动转换
{
  "routing_key": "{{.Secret}}",
  "event_action": "trigger",
  "payload": {
    "summary": "File {{.Key}} {{.Type}}",
    "severity": "info",
    "source": "aero-vault",
    "custom_details": {{.Payload | toJSON}}
  }
}
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **多规则匹配同一事件** | 全部匹配的规则都触发（fan-out）；同一事件投递到多个端点 |
| **规则配置变更** | 运行时 Webhook Worker 从 repository 重新加载规则（无需重启） |
| **模板渲染失败** | fallback 到 raw 格式 + 错误日志 |
| **端点不可用** | 继承现有的 durable retry + webhook_failures 表；独立的失败计数 per 端点 |
| **默认规则** | 向后兼容：无显式规则时，使用 EVENTS_WEBHOOK_URL + RAW 格式作为默认规则 |

### 涉及代码估算

| 组件 | 行数 | 说明 |
|------|------|------|
| `internal/events/webhook_rule.go` — 规则模型 + 匹配引擎 | ~120 | 规则结构体、Match 方法、规则管理器 |
| `internal/events/webhook_template.go` — 模板引擎 | ~80 | 内置模板注册（Slack/PagerDuty）+ Go template 渲染 |
| `internal/events/webhook.go` — 多端点发送 | ~60 | 从固定 URL → 规则列表；fan-out 逻辑 |
| `internal/repository/sql_webhook_rules.go` — 规则持久化 | ~80 | CRUD + 迁移文件（新增 webhook_rules 表） |
| `internal/api/rest/admin_webhook.go` — 管理 API | ~80 | CRUD 端点：POST/GET/PUT/DELETE /v1/admin/webhook-rules |
| `internal/config/config.go` — 配置扩展 | ~20 | 兼容旧配置：EVENTS_WEBHOOK_URL → 自动转换为默认规则 |
| 测试 | ~150 | 规则匹配单元测试 + 模板渲染测试 + 集成测试 |
| **合计** | **~590** | |

---

## 方向五：多层级优雅降级架构（Multi-Layer Graceful Degradation）

### 现状

当前系统对组件故障的响应：

```go
// internal/api/rest/search.go — 唯一存在的降级模式
type AIHandler struct {
    // ...
    degraded bool // when true, all AI endpoints return 503 immediately
}
```

由 `AI_DEGRADED_MODE=true` 环境变量控制。这是一个**二进制开关**：要么全部 AI 功能可用，要么全部不可用。

**故障响应对照表：**

| 故障场景 | 当前行为 | 理想行为 |
|---------|---------|---------|
| **Qdrant 不可达** | `Search.SearchVectors` 返回错误 → Chat 失败 → 用户得到 500 | 自动降级为内存暴力搜索（保留语意搜索能力，损失性能） |
| **Embedder API 超时** | `embedder.Embed` 失败 → Indexer 跳过对象 | 使用 HashEmbedder（确定性降级），搜索结果质量降低但功能可用 |
| **LLM API 限流（429）** | HTTP client 返回 429 → Chat 失败 | 返回部分结果 + 提示用户"AI 服务负载高，请稍后重试" |
| **Postgres 不可用** | 全部请求失败（DB 是强依赖） | 读请求从本地缓存响应（如 /healthz），写请求立即拒绝 |
| **S3 后端慢（延迟 5s+）** | 所有 FileService 操作排队等待 → 全链路阻塞 | 快速路径（Stat/List 从 repo 缓存）继续工作，写路径主动限流 |
| **存储熔断器打开** | `storage.ErrCircuitOpen` 传播到 handler → 500 | 降级为只读模式（允许 GET/Stat，拒绝 PUT/DELETE） |

**缺少的架构层：**

```
当前降级模型（二元）：
  组件正常 ───────────────────────── 组件故障
     ↓                                   ↓
  全部功能可用                      全部功能不可用

期望降级模型（多层级）：
  组件正常 → 性能下降 → 功能子集 → 只读模式 → 优雅关闭
     ↓         ↓           ↓          ↓          ↓
  全功能     降级算法    禁用部分    允许读     排出请求+
             精度/性能   功能模块    拒绝写     关闭连接
```

### 为什么需要

| 生产场景 | 当前风险 | 理想行为 |
|---------|---------|---------|
| **Embedder API 中断（OpenAI 故障）** | 全部搜索/聊天/Agent 失败 → 用户无法检索任何文件 | 搜索降级为 BM25（关键词匹配），Chat 返回"AI 搜索暂不可用，请使用关键词搜索" |
| **Qdrant 升级（计划内停机）** | 需停服维护 → 不可用窗口 | 自动切回内存暴力搜索 → 用户无感 |
| **S3 后端降级（区域故障）** | 全部 GET/PUT 超时 → 全服务不可用 | 本地读缓存（最近访问的热数据）继续服务读请求 |
| **数据库连接池耗尽** | 全部请求阻塞等待连接 | 区分读/写：读优先分配连接，写等待或拒绝 |
| **内存压力（OOM 风险）** | Go runtime OOM Kill → 全服务丢失 | 主动释放 BM25 索引、结果缓存等可重建结构，降低内存使用 |

### 架构设计

```
降级管理器（Degradation Manager）：

┌──────────────────────────────────────────────────────────────┐
│                    Degradation Manager                        │
│                                                               │
│  健康信号输入:                                                 │
│  ├── Component Health status (来自健康检查)                    │
│  ├── Circuit breaker state (来自 storage)                     │
│  ├── Error rate per component (来自 metrics)                  │
│  ├── Latency p95 per component (来自 metrics)                 │
│  └── Resource pressure (memory, goroutines, connpool)        │
│                                                               │
│  降级策略（每组件独立）:                                        │
│  ├── AI Pipeline: 正常 → 降级算法 → 禁用AI → 仅BM25          │
│  ├── Storage:     正常 → 限速写入 → 只读 → 本地缓存读         │
│  ├── Repository:  正常 → 读优先连接分配 → 写拒绝 → 缓存      │
│  └── Webhook:     正常 → 降速重试 → 跳过非关键事件            │
│                                                               │
│  降级状态传播:                                                 │
│  ├── metrics: aero_vault_degradation_level{component,level}   │
│  ├── /readyz: 反映降级状态（警告级别但不退出）                 │
│  ├── HTTP header: X-Aero-Degraded: "ai:algorithm,storage:ro"  │
│  └── log: 每次降级状态变化记录 WARN 日志                       │
└──────────────────────────────────────────────────────────────┘
```

**降级等级定义：**

```go
type DegradationLevel int

const (
    LevelNormal    DegradationLevel = iota // 全功能
    LevelDegraded                          // 降级算法/性能
    LevelRestricted                        // 功能子集
    LevelReadOnly                          // 只读
    LevelShutdown                          // 优雅关闭
)
```

**每个核心组件的降级路径：**

| 组件 | Normal | Degraded | Restricted | ReadOnly |
|------|--------|----------|------------|----------|
| **AI Embedder** | 远程 embedder | HashEmbedder（确定性质） | 仅 BM25 搜索 | 搜索禁用 |
| **AI LLM** | 远程 LLM | 降级为摘要/精简模型 | Chat 返回降级提示 | 全部 AI 503 |
| **Vector Index** | Qdrant/pgvector | 降级为内存暴力搜索 | 降级为 BM25 | 搜索 503 |
| **Storage Backend** | 后端正常 | 限速写入（50% 并发） | 只读模式 | 503 |
| **Repository** | 读写正常 | 读优先连接池 | 只读 | 503 |
| **Webhook** | 全部投递 | 降速重试（指数退避） | 跳过非关键事件 | 暂停投递 |

### 实现策略

**Phase 1 — 降级基础设施（~200 行）：**

| 组件 | 说明 |
|------|------|
| `internal/degrade/manager.go` | 降级管理器：组件注册、状态查询、级别变更通知 |
| `internal/degrade/levels.go` | 降级等级定义 + JSON 序列化 |
| `internal/telemetry/metrics.go` | `degradation_level{component}` gauge |

**Phase 2 — AI 管线降级（~150 行）：**

```go
// internal/ai/search.go — 降级感知搜索
func (s *Search) Query(ctx context.Context, req Query) ([]Hit, error) {
    switch s.degradeMgr.Level("ai_embedder") {
    case LevelNormal:
        return s.vectorQuery(ctx, req)   // 正规向量搜索
    case LevelDegraded:
        req.Embedder = s.hashEmbedder    // 替换为 HashEmbedder（确定性，低精度）
        return s.vectorQuery(ctx, req)
    case LevelRestricted:
        return s.bm25Query(ctx, req)     // 降级为关键词搜索
    case LevelReadOnly:
        return nil, ErrDegraded          // 搜索不可用
    }
}
```

**Phase 3 — 存储降级（~180 行）：**

```go
// internal/service/file_crud.go — 降级感知写入
func (s *FileService) Put(ctx context.Context, ...) (repository.Object, error) {
    if level := s.degradeMgr.Level("storage"); level >= LevelReadOnly {
        return repository.Object{}, ErrDegradedReadOnly
    }
    // 正常写入
}
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **降级状态频繁抖动** | 降级状态变更需稳定 N 秒（默认 30s）后才切换；防止 Oscillation |
| **逐步恢复** | 降级管理器检测到组件恢复后，逐步提升等级（ReadOnly → Restricted → Degraded → Normal），每步间隔 N 秒 |
| **手动控制** | admin API `POST /v1/admin/degrade` 允许运维手动设定/解除降级状态（覆盖自动判定） |
| **降级提示** | Chat/AI 端点返回时在响应体中加入 `"degraded": true` 和 `"degraded_reason"` 字段 |
| **审计** | 所有降级状态变更记录到 `audit_log` 表（谁、何时、什么组件、什么等级） |
| **与自适应过载控制的关系** | v53 方向三（自适应反压）解决"快/慢"问题（调整速率和并发）；本方向解决"能/不能"问题（调整提供哪些功能）。两者互补：反压先触发，持续恶化则降级接管 |

### 涉及代码估算

| 组件 | 行数 |
|------|------|
| `internal/degrade/manager.go` — 降级管理器核心 | ~120 |
| `internal/degrade/levels.go` — 等级与策略定义 | ~80 |
| `internal/ai/search.go` — 降级感知搜索 | ~60 |
| `internal/ai/chat.go` — 降级感知聊天 | ~40 |
| `internal/service/file_crud.go` — 降级感知 CRUD | ~60 |
| `internal/api/rest/admin_degrade.go` — 管理 API | ~40 |
| `internal/api/rest/search.go` — 降级提示（header + body） | ~20 |
| `internal/telemetry/metrics.go` — 降级指标 | ~20 |
| 测试 | ~160 |
| **合计** | **~600** |

---

## 优先级与实施建议

| 方向 | 影响范围 | 估算行数 | 风险 | 推荐时序 |
|------|---------|---------|------|---------|
| **#1 FileService 操作级可观测性** | 运维/诊断效率 | ~315 | 低 — 纯新增代码，不改现有逻辑 | **Phase 1** — 最立即可行，无侵入 |
| **#3 SDK 大文件上传** | 开发者体验/产品能力 | ~430 | 低 — JS SDK 已实现相同模式 | **Phase 1** — 直接对标 JS SDK |
| **#2 Web UI 生产化加固** | 终端用户体验 | ~600 | 低 — 纯前端改动，后端不变 | **Phase 2** — 与 #1/#3 可并行 |
| **#5 多层级降级架构** | 系统可靠性 | ~600 | 中 — 需多组件协调，但每个单元简单 | **Phase 2** — 需要 #1 的 metrics 作为输入 |
| **#4 Webhook 多格式路由** | 集成/平台能力 | ~590 | 中 — 新表 + 新 API + 后向兼容 | **Phase 3** — 非关键路径，可后置 |

**快速赢取：** #1（操作级指标）和 #3（SDK 大文件上传）可以立即并行启动，无需架构决策会。两者都是纯增量代码，不改变现有行为，不引入迁移成本。

**架构前提：** #5（降级架构）依赖 #1（操作级指标）提供的组件健康信号输入；建议 #1 先行，待 metrics 数据积累足够后再设计降级触发阈值。
