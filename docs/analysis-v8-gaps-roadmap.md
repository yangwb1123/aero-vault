# 🏗️ AeroVault 深度评估 v8 — 用户界面与开发者工具、MCP 集成生态、CLI 脚本化、事件系统深化、生产部署成熟度

> **日期:** 2026-06-30  
> **方法:** 全局代码扫描（237 文件 / ~45K 行），第八轮  
> **视角:** 终端用户界面与开发者工具 → 协议集成深度 → 部署与运维 → 事件基础设施

---

## 0. 本轮焦点：从"核心能力"到"终端用户体验与生态黏性"

前七轮覆盖了特征缺口与性能（v1）、韧性与安全（v2）、生态系统与经济性（v3）、内部架构债与 AI 管线（v4）、存储实现与合规（v5）、平台竞争力（v6）、API 设计与代码健康（v7）。  
本轮转向**最靠近用户和集成者的表面层**——Web UI 的前端体验、CLI 的脚本化能力、MCP 的生态兼容性、事件系统的深度、以及生产部署的成熟度。这些维度决定了项目能否从"好用的引擎"变成**用户日常依赖的工具**。

---

## 1. Web UI：从功能性 SPA 到完整的前端产品

### 1.1 当前状态：282 行，功能齐全但体验基础

```go
// internal/webui/web.go — 嵌入的 SPA
// 一个文件的 HTML 页面，无 JS 框架，无外部依赖
// 标签页: search, detail, lineage, chat
// 功能: 拖拽上传、文件列表、搜索、聊天 SSE 流式渲染
```

| 维度 | 当前状态 | 用户影响 |
|----------|-------------|-------------|
| **代码行数** | 282 行（单文件）| 功能有限 |
| **框架** | 无（原生 JS + CSS）| 维护性随着增长降低 |
| **移动端** | ❌ 无响应式 | 在手机上无法使用 |
| **主题** | ✅ 深色模式 | 不错 |
| **可访问性** | ❌ 无 ARIA/语义 HTML | 屏幕阅读器无法使用 |
| **文件上传** | ✅ 拖拽上传 | 良好 |
| **文件浏览** | ⚠️ 列表 + 前缀过滤 | 无搜索/标签过滤/排序 |
| **Semantic Search** | ✅ 基本搜索 UI | 无搜索历史/建议 |
| **Chat** | ✅ SSE 流式渲染 | 无对话历史/多轮 |
| **Lineage** | ✅ 基本显示 | 无可视化/过滤 |
| **Object Detail** | ⚠️ JSON 原始显示 | 无格式化的元数据视图 |
| **管理** | ❌ 缺失 | 无租户管理/密钥管理/配额仪表板 |
| **国际化** | ❌ 无 | 仅英文 |
| **离线/缓存** | ❌ 无 | 页面刷新丢失状态 |
| **键盘快捷键** | ❌ 无 | 仅鼠标操作 |

### 1.2 缺失的关键 UI 功能

| 功能 | 描述 | 为什么重要 |
|----------|-------------|-------------|
| **管理仪表板** | 租户列表、密钥管理、配额视图、作业队列监控 | 部署后 80% 的操作是管理，不是 CRUD |
| **搜索建议** | 输入时自动补全 → 提升搜索采用率 | 用户不总是知道自己在找什么 |
| **对话历史** | persist 过去聊天会话 | RAG 聊天需要记忆上次对话的上下文 |
| **对象预览** | 文本/代码/markdown/图片的格式化预览 | JSON 原始显示不够 |
| **文件管理器视图** | 目录树、面包屑、多选、批量删除 | 标准的存储浏览体验 |
| **存储统计** | 饼图显示存储分布（按桶、按文件类型） | 用户想知道"我的空间用在哪里" |
| **通知** | SSE 连接成功/失败、上传完成、搜索完成 | 异步操作需要反馈 |
| **多语言** | i18n 支持（至少中文 + 英文）| 中国用户占比大 |

### 1.3 架构蓝图：Web UI 2.0

```
当前: 单文件 HTML → 4 个标签页
改进: Web UI 2.0 (模块化 SPA，仍无构建步骤)

┌────────────────────────────────────────────────────────────────┐
│ Architecture (架构):                                               │
│   保持 "无构建步骤" 原则，但拆分 JS 逻辑:                          │
│   ├── index.html (骨架 + CSS, 5KB)                               │
│   ├── app.js (路由 + 状态管理, 8KB)                              │
│   ├── views/search.js (搜索 UI 逻辑)                             │
│   ├── views/chat.js (聊天 SSE + 历史)                            │
│   ├── views/files.js (文件浏览 + 上传)                           │
│   ├── views/admin.js (租户/密钥/配额/仪表板)                      │
│   └── utils/api.js (HTTP 客户端 + auth 包装)                     │
├────────────────────────────────────────────────────────────────┤
│ New Features (新功能):                                              │
│   ├── Admin Dashboard:                                           │
│   │   ├── 租户列表 + 创建/删除 + 状态切换                        │
│   │   ├── 密钥管理: 列出/添加/撤销/到期显示                       │
│   │   ├── 配额视图: 使用量仪表 + 上限 + 预算                     │
│   │   ├── 作业队列: 待处理/运行中/失败 + 手动重试               │
│   │   └── 审计日志: 滚动浏览 + 过滤                               │
│   ├── File Manager:                                               │
│   │   ├── 目录树 (基于前缀)                                       │
│   │   ├── 面包屑导航                                              │
│   │   ├── 多选 + 批量删除/标签                                    │
│   │   ├── 详情面板: Content-Type, 大小, ETag, 标签, ACL          │
│   │   └── 版本浏览: 历史版本对比                                  │
│   ├── Search UX:                                                  │
│   │   ├── query-as-you-type 建议 (使用 /v1/search 前缀搜索)        │
│   │   ├── 搜索结果 → 点击 → 打开对象详情                            │
│   │   ├── 搜索历史 (localStorage)                                 │
│   │   └── 模式切换: vector/BM25/hybrid + 计分可视化              │
│   ├── Chat UX:                                                    │
│   │   ├── 对话历史 (localStorage + 可选服务器端持久化)             │
│   │   ├── 多轮上下文 (使用 prior 参数)                              │
│   │   ├── 引用点击 → 在新标签页打开对象                             │
│   │   └── "导出对话" 为 markdown                                   │
│   └── Accessibility (可访问性):                                      │
│       ├── ARIA 标签 + 角色                                         │
│       ├── 键盘导航 (Tab/Enter/Esc)                                │
│       └── 高对比度模式                                             │
└────────────────────────────────────────────────────────────────┘
```

**复用资产：** `internal/webui/web.go`（嵌入的 `embed.FS`）——只需扩展 static 目录

**边缘情况与性能考量：**
- SSE 断线重连：聊天标签页在网络中断时需要自动重连（当前浏览器 EventSource API 原生支持，但没错误处理）
- 大文件上传：当前 form 上传，UI 应添加上传进度条 + 可取消
- 缓存策略：`index.html` 应设置 `Etag`/`Cache-Control` 头，通过 `http.ServeFileFS` 自带

| 复杂度 | 用户影响 | 代码变动 | 差异化 |
|----------|-------------|-------------|------------|
| 中 | 极高（日常用户） | 中（JS 拆分 + 新视图） | ★★★★★ |

---

## 2. MCP 协议深度与生态兼容性审计

### 2.1 当前 MCP 协议实现

```go
// internal/mcp/protocol.go
// 协议版本: "2024-11-05" (最新: 2025-03-26 — 请检查)
// 能力声明:
//   resources: { subscribe: false, listChanged: false }
//   tools:     { listChanged: false }
// 暴露的工具: list_files, read_file, write_file, delete_file, search, chat
// 暴露的资源: 活动租户下的对象（限制 200 个）
```

| MCP 能力 | 状态 | Claude Desktop | Cursor | Cline |
|-----------|--------|:------:|:------:|:-----:|
| **tools/list** | ✅ 正确 | ✅ | ✅ | ✅ |
| **tools/call** | ✅ 正确 | ✅ | ✅ | ✅ |
| **resources/list** | ✅ 正确 | ✅ | ✅ | ✅ |
| **resources/read** | ✅ 正确 | ✅ | ✅ | ✅ |
| **resources/subscribe** | ❌ 声明不支持 | N/A | N/A | N/A |
| **tools/listChanged** | ❌ 声明不支持 | N/A | N/A | N/A |
| **notifications/initialized** | ❌ 未实现 | ⚠️ 可选 | ⚠️ 可选 | ⚠️ 可选 |
| **notifications/cancelled** | ❌ 未实现 | ⚠️ 可选 | ⚠️ 可选 | ⚠️ 可选 |
| **logging/setLevel** | ❌ 未实现 | ⚠️ 可选 | ⚠️ 可选 | ⚠️ 可选 |
| **资源模板** | ❌ 未实现 | ✅ 用于动态 URI | N/A | N/A |
| **分页请求** | ❌ 未实现 | ✅ 用于大列表 | N/A | N/A |
| **流式传输** | ⚠️ HTTP SSE 流 | ✅ | N/A | N/A |

### 2.2 缺失的 MCP 工具清单

```go
// 当前 6 个工具:
// list_files, read_file, write_file, delete_file, search, chat
//
// 缺失的高价值工具:
// - tag_file: 给对象添加标签
// - list_versions: 列出对象的历史版本
// - get_file_metadata: 获取 Content-Type, 大小, 标签, ACL
// - batch_delete: 批量删除多个对象
// - move_file: 重命名/移动对象
// - create_bucket: 创建桶
// - list_buckets: 列出桶
// - get_usage: 获取租户使用量/配额
// - admin_list_keys: 列出 API 密钥
```

### 2.3 MCP 集成场景深度分析

| 场景 | 当前 | 改进后 |
|----------|-------|-------------|
| **"在笔记中搜索并检索文档"** | ✅ 通过 `search` + `read_file` 工作 | 良好 |
| **"从 GitHub 复制文件到 AeroVault"** | ⚠️ 通过 `write_file` 工作，但无法设置 Content-Type | 添加 Content-Type 参数 |
| **"显示存储统计"** | ❌ 无工具暴露使用量数据 | 添加 `get_usage` |
| **"标记敏感文档"** | ❌ 无标签操作 | 添加 `tag_file` |
| **"清理旧版本"** | ❌ 无版本管理 | 添加 `list_versions` |
| **"审计密钥"** | ❌ 无管理工具（应为 admin 保留）| 添加 admin 作用域提示 |

### 2.4 架构蓝图：MCP 深度集成

```
当前: 6 个工具 + 2 个资源端点 + HTTP+stdio 传输
改进: MCP 2.0 (完全协议覆盖)

┌────────────────────────────────────────────────────────────────┐
│ Protocol Compliance (协议合规):                                    │
│   ├── 版本: 升级到 "2025-03-26"（新增采样/进度支持）              │
│   ├── 通知:                                                     │
│   │   ├── notifications/initialized (连接后就绪)                 │
│   │   └── notifications/cancelled (客户端取消请求)                │
│   ├── 能力: 灵活声明支持的子集                                    │
│   └── 资源模板:                                                   │
│       ├── `aero-vault://{tenant}/{bucket}/{key}`                  │
│       └── 客户端通过模板 URI 直接读取资源                          │
├────────────────────────────────────────────────────────────────┤
│ Extended Tools (扩展工具集 — 12 个):                                 │
│   现有: list_files, read_file, write_file, delete_file,          │
│          search, chat                                            │
│   新增:                                                          │
│   ├── tag_file      (PUT /v1/files/{key}/tags)                    │
│   ├── list_versions (GET /v1/files/{key}/versions)                │
│   ├── get_metadata  (STAT /v1/files/{key})                       │
│   ├── batch_delete  (POST /v1/batch/delete)                       │
│   ├── move_file     (MOVE / copy + delete)                        │
│   ├── create_bucket (POST /v1/buckets)                            │
│   └── get_usage     (GET /v1/usage)                               │
├────────────────────────────────────────────────────────────────┤
│ Transport Layer (传输层改进):                                        │
│   ├── HTTP 传输: 支持流式响应 (SSE) 用于长操作                    │
│   ├── stdio 传输: 改进错误处理 + 日志干扰过滤                     │
│   └── 新传输: WebSocket (用于需要双向通信的客户端)                │
└────────────────────────────────────────────────────────────────┘
```

**复用资产：** `internal/mcp/server.go`（扩展 dispatch 方法）、`internal/mcp/protocol.go`（扩展能力声明）、`internal/mcp/transport.go`（改进 HTTP/stdio）

| 复杂度 | 用户影响 | 代码变动 | 差异化 |
|----------|-------------|-------------|------------|
| 低中 | 高（AI 工具生态）| 低（几行 dispatch 扩展） | ★★★★☆ |

---

## 3. CLI 脚本化：从开发人员工具到自动化工件

### 3.1 当前 CLI 能力审计

```go
// internal/cli/ — 14 个子命令
// upload, get, ls, rm, search, tag, versions, lineage, snapshot create/restore
// admin (keys/tenants/jobs/audit)
// lsbuckets, bucket-rm, help
```

| 维度 | 当前评估 | 用户影响 |
|----------|-------------|-------------|
| **子命令数** | 14 | 覆盖面不错 |
| **JSON 输出** | ❌ 无（仅文本）| 无法与 `jq` 管道组合 |
| **shell 自动补全** | ❌ 无 | 每次使用需查 help |
| **batch 模式** | ❌ 无（每次单文件）| 无法批量上传/删除 |
| **watch 模式** | ❌ 无 | 无法 "tail -f" 一个目录 |
| **错误处理** | ⚠️ 退出码正确但错误消息不统一 | 脚本编写困难 |
| **进度指示** | ❌ 无（静默上传）| 大文件无反馈 |
| **管道支持** | ⚠️ get 输出到 stdout（可管道），upload 需文件参数 | 无法 `curl ... | aero-vault cli upload` |
| **配置文件** | ❌ 仅环境变量 | 每次需设置 AERO_ENDPOINT/AERO_API_KEY |
| **脚本化示例** | ❌ 无 | 不知道如何在 CI 中使用 |
| **通配符支持** | ❌ 无（`ls docs/*` 需手配）| 无本地 glob 展开 |

### 3.2 具体改进点（代码级）

```go
// internal/cli/cli_crud.go — cmdUpload
// 问题: 只接受本地文件路径，不接受 stdin
// 修复: 第二个参数为 "-" 时从 stdin 读取
//   cat document.md | aero-vault cli upload notes/doc.md -

// 问题: 无 JSON 输出模式
// 修复: 全局 --json 标志 → 所有命令输出 JSON（可被 jq 消费）
//   aero-vault cli --json ls | jq '.objects[].key'

// 问题: 无 shell 自动补全
// 修复: `aero-vault cli completion bash` → 输出 bash 补全脚本
```

### 3.3 架构蓝图：CLI 2.0

```
当前: 14 个命令，仅文本输出，仅文件参数
改进: CLI 2.0 (脚本优先设计)

┌────────────────────────────────────────────────────────────────┐
│ Output Modes (输出模式):                                           │
│   ├── 默认: 人类可读的彩色文本                                      │
│   ├── --json:  所有命令输出 JSON (便于 jq/CI 管道)                  │
│   ├── --quiet: 仅退出码 (最快脚本)                                  │
│   └── --format go-template: 自定义输出格式                          │
├────────────────────────────────────────────────────────────────┤
│ Input Modes (输入模式):                                               │
│   ├── upload key file       → 本地文件                              │
│   ├── upload key -          → 从 stdin 读取 (echo "hello" | cli upload) │
│   ├── upload key http://... → 从 URL 抓取并上传 (wget 模式)         │
│   └── batch upload          → 从 stdin 读取 key/file 列表            │
├────────────────────────────────────────────────────────────────┤
│ Interactive Features (交互功能):                                      │
│   ├── watch ls              → 每 2s 重新列出（类似 `watch` 命令）   │
│   ├── progress              → 大文件上传/下载显示进度条               │
│   ├── shell completion       → bash/zsh/fish 自动补全脚本生成        │
│   └── config                → 配置文件 (TOML/YAML 位于 ~/.aerovault) │
├────────────────────────────────────────────────────────────────┤
│ New Commands (新命令 ~10 个):                                        │
│   ├── bucket create/ls/rm                                          │
│   ├── presign get/put <key> <expiry>                               │
│   ├── chat      <query>                                            │
│   ├── agent     <query>                                            │
│   ├── mv        <source> <dest>                                    │
│   ├── cp        <source> <dest> (cross-bucket)                     │
│   ├── restore   <key> (恢复软删除对象)                              │
│   └── completion bash/zsh/fish                                      │
└────────────────────────────────────────────────────────────────┘
```

**复用资产：** `internal/cli/cli.go`（框架 + 注册）、`internal/cli/cli_crud.go`（扩展 upload 接受 stdin）

**边缘情况与性能考量：**
- 大文件上传到 stdout：`get` 命令直接输出到 stdout 用于管道，不能添加人类可读的干扰
- 彩色文本通过检查 `os.Stdout` 是否为终端自动禁用
- shell 补全的动态实现（不维护静态列表）

| 复杂度 | 用户影响 | 代码变动 | 差异化 |
|----------|-------------|-------------|------------|
| 低中 | 极高（自动化用户） | 中（命令扩展 + 输出模式） | ★★★★★ |

---

## 4. 事件系统深度：数据管道的基础设施

### 4.1 当前事件基础设施

```go
// internal/events/bus.go — 事件总线
// 事件持久化: 写入 events 表 (InsertEvent)
// 本地广播: 带缓冲 (64) 的 channel 订阅
// 跨实例广播: 通过 Postgres LISTEN/NOTIFY (可选的, 通过 transport)
// 事件类型: object.created, object.deleted, object.modified

// 消费者:
// - SSE (REST /v1/events/stream)
// - Webhook (HTTP POST + HMAC + 持久化重试)
// - Replication Worker (事件 → 复制作业)
// - Antivirus Worker (事件 → 扫描作业)
```

| 维度 | 当前状态 | 评估 |
|----------|-------------|:-------:|
| **事件类型** | ~3（created/deleted/modified）| 基础 |
| **事件 Schema** | 无（Free-form JSON payload）| ⚠️ 版本控制困难 |
| **Event Sourcing** | ❌ 无 | 无法重建状态 |
| **事件重放** | ❌ 无 | 新消费者错过过去的事件 |
| **事件过滤** | ❌ 仅租户过滤 | 无法按桶/键/类型过滤 |
| **事件 TTL** | ❌ 无（无限增长）| 需要保留策略 |
| **投递保证** | ⚠️ at-most-once（本地总线缓冲可能丢）| 持久化复制是幂等的 |
| **死信队列** | ❌ 无（失败的 webhook 被标记为 succeeded 后停止重试）| ⚠️ 见 webhook.go |

### 4.2 事件类型的扩展需求

```go
// 当前事件类型（repository.EventType）:
const (
    EventCreated  EventType = "object.created"
    EventDeleted  EventType = "object.deleted"
    EventModified EventType = "object.modified"
)

// 缺失的高价值事件类型:
// - object.locked       → 合规通知
// - object.restored     → 从软删除恢复
// - object.versioned    → 版本创建
// - bucket.created      → 桶生命周期通知
// - bucket.deleted      → 桶生命周期通知
// - tenant.quota_exceeded → 接近/超过限制
// - indexer.completed    → 索引完成
// - system.health_change → 健康状态变更
```

### 4.3 架构蓝图：事件系统 2.0

```
当前: 3 种事件类型 + 进程内广播 + 可选的 Postgres LISTEN/NOTIFY
改进: Event System 2.0 (可观测 + 可重放 + 可过滤)

┌────────────────────────────────────────────────────────────────┐
│ Event Schema & Registry (事件模式与注册表):                            │
│   ├── 模式注册: 每个事件类型定义 JSON Schema                        │
│   │   ├── object.created: {tenant, bucket, key, size, etag, type}│
│   │   ├── object.deleted: {tenant, bucket, key, hard_delete}     │
│   │   └── object.locked:  {tenant, bucket, key, locked_until}   │
│   ├── 验证: Publish 时验证 payload 匹配注册的模式                    │
│   └── 兼容性: 向后兼容（新 payload 字段可选）                       │
├────────────────────────────────────────────────────────────────┤
│ Event Replay (事件重放):                                              │
│   ├── POST /v1/admin/events/replay                                  │
│   │   { "since": "2026-01-01T00:00:00Z",                           │
│   │     "filter": { "types": ["object.created"] } }                 │
│   ├── 从 events 表读取 → 重新投递到 bus                              │
│   └── 用途: 新 webhook 端点需要 catch-up 模式                        │
├────────────────────────────────────────────────────────────────┤
│ Event Retention & Purging (事件保留与清理):                              │
│   ├── RECONCILE_EVENT_RETENTION_DAYS (默认 90)                     │
│   ├── 后台作业清理 events 表                                        │
│   └── 避免无限表增长（当前无清理）                                    │
├────────────────────────────────────────────────────────────────┤
│ Filtered Subscriptions (过滤订阅):                                      │
│   ├── Subscribe(filter) → 只接收匹配事件                            │
│   │   filter: { types: [...], buckets: [...], tenants: [...] }    │
│   ├── 不依赖事件的广播 + 客户端过滤                                  │
│   └── 性能: 减少无用本机处理                                         │
├────────────────────────────────────────────────────────────────┤
│ Event Metrics (事件指标):                                              │
│   ├── events_published_total{type}                                  │
│   ├── events_delivered_total{subscriber}                            │
│   ├── events_dropped_total{reason}                                  │
│   └── events_lag_seconds{consumer} (事件创建到消费的延迟)            │
└────────────────────────────────────────────────────────────────┘
```

**复用资产：** `internal/events/bus.go`（总线模式——扩展 Subscribe）、`internal/repository/repository.go`（`InsertEvent` 已存在——查询需要索引）、`internal/telemetry/metrics.go`（已有 `events_dropped_total`）

**边缘情况与性能考量：**
- 事件 TTL：90 天对合规场景太短（可能需 1-7 年）——应可配置到 3650 天
- 重放性能：重放 100 万个事件不应阻塞主总线——使用专用 goroutine + 限速
- 事件顺序：`events` 表有 `created_at` 但没有全局序列号——跨区域场景可能乱序

| 复杂度 | 用户影响 | 代码变动 | 差异化 |
|----------|-------------|-------------|------------|
| 中 | 高（可观测性/数据管道） | 中（模式 + 重放 + 清理）| ★★★★☆ |

---

## 5. 生产部署成熟度：从 Docker Compose 到生产网格

### 5.1 当前部署拓扑

| 配置 | 文件 | 评估 |
|-------------|------|--------|
| **Docker 镜像** | `Dockerfile`（distroless 镜像，~15MB）| ✅ 生产级 |
| **docker-compose** | `docker-compose.yml` + `deploy/docker-compose.demo.yml` | ⚠️ 演示可用，生产缺 postgres + 持久化配置 |
| **Helm chart** | `deploy/helm/aero-vault/`（15 个模板）| ✅ 结构合理但缺一些生产模式 |
| **GitHub CI** | `.github/workflows/ci.yml` + `docker.yml` | ⚠️ 需改善（见 v7）|
| **Prometheus** | `deploy/prometheus/`（配置 + 告警规则）| ✅ 基础覆盖 |
| **Grafana** | `deploy/grafana/`（仪表板 JSON）| ✅ 覆盖好 |
| **OpenTelemetry** | `deploy/otel-collector-config.yaml` | ✅ 存在 |

### 5.2 Helm chart 审计

```yaml
# deploy/helm/aero-vault/values.yaml — 评估
# ✅ 支持: replicaCount, image, service, ingress, persistence, secrets
# ✅ 有: HPA, PDB, ServiceAccount, PodDisruptionBudget
# ✅ 安全: runAsNonRoot, readOnlyRootFilesystem, seccomp
# ⚠️ 缺失:
#   - NetworkPolicy (默认允许所有流量——零信任不兼容)
#   - PodMonitor / ServiceMonitor (用于 Prometheus Operator)
#   - 后台任务副本（reconcile/lifecycle 应该只在一个副本运行）
#   - 普罗米修斯告警的 alertmanager 配置
#   - grafana dashboard 的 ConfigMap
#   - 迁移 Job（部署前先运行 postgres 迁移）
```

### 5.3 生产就绪检查清单缺失项

| 能力 | 当前状态 | 生产要求 |
|----------|-------------|----------------|
| **备份/恢复** | ✅ SQLite 快照工具 | Postgres 需要 pg_dump 指引 |
| **零停机部署** | ⚠️ 基础（HPA + PDB）| 无就绪探针等待 AI 管线就绪 |
| **金丝雀部署** | ❌ 缺失 | 无流量分割路由 |
| **蓝绿部署** | ❌ 缺失 | 无多版本共存 |
| **秘密轮换** | ⚠️ 环境变量 | 挂载 Secret → 需要热重载 |
| **网络策略** | ❌ 缺失 | 默认拒绝出站 Webhook |
| **Pod 抗性** | ✅ PDB | 配置 1 个副本 |
| **资源限制** | ⚠️ 有默认值 | 无 AI 管线的内存/CPU 配置建议 |
| **Sidecar 注入** | ❌ 缺失 | 无 OpenTelemetry 收集器 sidecar |

### 5.4 架构蓝图：生产部署成熟度

```
当前: Helm chart + Docker + docker-compose + Prometheus/Grafana
改进: Production Deployment Maturity

┌────────────────────────────────────────────────────────────────┐
│ Helm Chart Extensions (Helm chart 扩展):                              │
│   ├── NetworkPolicy:                                              │
│   │   ├── 入站: 仅 ingress 命名空间的 8080 端口                    │
│   │   ├── 出站: 仅 DB、Qdrant、Webhook URL、DNS                   │
│   │   └── 默认拒绝 + 白名单                                        │
│   ├── Prometheus ServiceMonitor:                                  │
│   │   └── 为 Prometheus Operator 用户自动发现 /metrics            │
│   ├── Migration Hook Job:                                         │
│   │   └── pre-install/pre-upgrade Job 运行 `aero-vault migrate`   │
│   │       （当前迁移在启动时运行——但 Job 确保幂等完成）            │
│   ├── AI Pipeline Resource Sizing:                                │
│   │   ├── resources.requests.memory: 建议值 (512Mi base + AI)    │
│   │   └── resources.limits.memory: 建议值 (1Gi base + AI)        │
│   └── Grafana ConfigMap:                                           │
│       ├── 将仪表板 JSON 放入 ConfigMap                            │
│       └── 自动挂载到 Grafana sidecar                              │
├────────────────────────────────────────────────────────────────┤
│ Migration Tooling (迁移工具):                                          │
│   ├── 专用 `aero-vault migrate` 命令                             │
│   │   ├── 独立运行迁移（不在启动时运行）                           │
│   │   ├── 输出: "已应用 N 个迁移, 0 个失败"                       │
│   │   └── --dry-run 标志: 显示将要应用的迁移                       │
│   ├── 数据迁移:                                                        │
│   │   ├── SQLite → Postgres 迁移脚本                              │
│   │   ├── local → S3 迁移脚本                                     │
│   │   └── "零停机" 迁移: 双写期间保持运行                         │
│   └── 迁移回滚:                                                       │
│       ├── `aero-vault migrate rollback` (按迁移编号)              │
│       └── 带 --force（冒险操作）                                    │
├────────────────────────────────────────────────────────────────┤
│ Zero-Downtime Practices (零停机实践):                                   │
│   ├── 就绪探针: 等待 AI 管线就绪                                     │
│   │   ├── /startupz → embedder ping + BM25 ready + migrations done │
│   │   └── /readyz   → DB + storage + AI + CB 全部正常              │
│   ├── Graceful Shutdown (见 v7 方向 2):                              │
│   │   ├── 优先排空连接                                              │
│   │   ├── 释放集群租约                                              │
│   │   └── 等待 in-flight 请求完成直至超时                            │
│   └── 滚动更新配置:                                                   │
│       ├── maxSurge: 1, maxUnavailable: 0                            │
│       └── terminationGracePeriodSeconds: 60                        │
└────────────────────────────────────────────────────────────────┘
```

**复用资产：** `deploy/helm/aero-vault/`（15 个模板——扩展它们）、`Dockerfile`（多阶段构建——可用于 migrate 命令）

| 复杂度 | 用户影响 | 代码变动 | 差异化 |
|----------|-------------|-------------|------------|
| 中 | 高（生产部署） | 最低（仅有配置/YAML 变更 + migrate 命令）| ★★★★★ |

---

## 6. 本轮扫描发现的边界情况与性能点

### 6.1 SSE 断线：SSE 客户端重新连接时的竞态条件

```go
// internal/api/rest/sse.go — SSE 处理程序
// 当前: 通过 EventSource 实现 SSE ——浏览器在断线后自动重新连接
// 问题: 在重新连接和完全新的订阅之间，丢失的事件不会重放
// 修复: 添加 Last-Event-ID 头 + 服务器端事件重放
// 影响: ??? 不重要，events 表已有持久事件
```

### 6.2 事件表无限增长（无清理机制）

```go
// internal/repository/sql_objects.go
// 问题: events 表 INSERT 从未被清理
// 影响: 长时间运行的部署→events 表数百 GB，SELECT 查询缓慢
// 修复: events 表添加 created_at 索引 + 保留作业按 RECONCILE_EVENT_RETENTION_DAYS 清理
```

### 6.3 Web UI：`localStorage` 无大小限制检查

```go
// 当前: 通过 localStorage 持久化租户选择器
// 影响: 将来持久化搜索历史/对话历史时，可能因 localStorage 配额（5MB）而静默失败
// 修复: 在持久化前检查 localStorage 可用空间 + 优雅的配额管理
```

### 6.4 MCP STDIO 传输：日志行与 JSON-RPC 行竞争

```go
// internal/mcp/transport.go — stdio 传输
// 问题: 日志语句（通过 slog 写入 stderr）可能被 MCP 客户端误解析为 JSON-RPC 响应
// 当前: 日志写入 stderr（✅ 正确——客户端应仅读取 stdout）
// 风险: 因为 Go 的日志可能被重定向，建议明确将日志始终写入 stderr
// 当前代码正确 ✅ 但值得文档化
```

### 6.5 CLI 上传：`cmdUpload` 无进度指示或取消

```go
// internal/cli/cli_crud.go — cmdUpload
// 问题: 使用 os.File 直接作为请求体——无法取消或跟踪进度
// 影响: 大文件（1GB+）请求阻塞，无反馈
// 修复: 添加 io.TeeReader（进度）+ context（取消）
```

### 6.6 多协议互操作：/v1 vs /s3 vs /webdav 的 Content-Type 不一致

```go
// REST PUT /v1/files/{key}: 从 Content-Type 头读取
// S3 PUT /s3/{bucket}/{key}: 从 x-amz-content-type 头读取
// WebDAV PUT: 从 Content-Type 头读取
// MCP write_file: 从工具参数读取
// 问题: 无规范化的 Content-Type 映射（如 text/plain → text/plain; charset=utf-8）
// 影响: 同一对象通过不同协议设置稍有不同的 Content-Type
```

### 6.7 性能：Web UI 的 SSE 事件流每客户端一个连接

```go
// /v1/events/stream: 每个连接创建一个 EventBus.Subscribe 通道（带 64 缓冲）
// 如果 1000 个 Web UI 用户同时打开 → 1000 个通道 + 每个事件 1000 次广播
// 影响: 大规模部署时的内存和 CPU 开销
// 修复: 添加事件轮询端点（GET /v1/events/poll?since=<id>）作为 SSE 的替代方案
```

### 6.8 性能：未使用的 OpenAPI JSON 嵌入在每次服务器启动加载

```go
// internal/api/rest/openapi.go
// 使用 go:embed 嵌入 ~5KB openapi.json
// 问题: 每次服务器启动加载到内存（极小问题）
// 修复: 无需修复——这是正确且高效的嵌入方式
// 注意: embed 在编译时嵌入，无运行时开销 ✅
```

---

## 7. 跨轮综合一览：8 个视角，40 个方向

| 轮次 | 核心视角 | 5 个方向 | 累计唯一发现 |
|------|-------------|----------|-------------------|
| **v1** | 特征缺口 + 性能 | 存储分层、多区域副本、FUSE、事件队列、缓存 | ~15 |
| **v2** | 韧性 + 一致性 | 断路器、Saga、多模态、自愈网格、搜索联邦 | ~12 |
| **v3** | 协议生态 + 经济性 | 可观测管线、API 网关、多云成本、合规、函数引擎 | ~18 |
| **v4** | 内部债 + AI 管线 | 数据访问优化、RAG 质量、优雅关闭、并发加固、工具链 | ~20 |
| **v5** | 存储质量 + 合规 | 存储后端加固、S3 补齐、传输安全、开发者入门、数据可移植性 | ~25 |
| **v6** | 平台竞争力 | 查询重写/多模型路由、法律封存、全球网格、函数引擎、货币化 | ~15 |
| **v7** | API + 运维 + 测试 + 供应链 | API DX、运维工具箱、测试 CI 大修、代码健康、供应链安全 | ~30 |
| **v8** | **前端 + 工具 + 部署** | **Web UI 2.0、MCP 深度集成、CLI 脚本化、事件系统深化、生产部署成熟度** | ~20 |

**跨轮累计：** 40 个扩展方向 + 100+ 独立边界情况/性能发现，覆盖全部代码库。

---

## 8. 附录：本评估系列的文件清单

| 文件 | 大小 | 聚焦领域 |
|------|------|--------------|
| `docs/analysis-v1-gaps-roadmap.md` | 9,390 B | 特征、边界情况、性能 |
| `docs/analysis-v2-gaps-roadmap.md` | 12,715 B | 韧性、一致性、安全、CB |
| `docs/analysis-v3-gaps-roadmap.md` | 17,113 B | 可观测性、经济性、多云 |
| `docs/analysis-v4-gaps-roadmap.md` | 21,491 B | 内部债、AI 管线、数据库 |
| `docs/analysis-v5-gaps-roadmap.md` | 22,945 B | 存储实现、合规、入门 |
| `docs/analysis-v6-gaps-roadmap.md` | 19,036 B | AI 2.0、企业合规、全球化、货币化 |
| `docs/analysis-v7-gaps-roadmap.md` | 29,224 B | API DX、运维、测试、代码健康、供应链 |
| **`docs/analysis-v8-gaps-roadmap.md`** | **本轮** | **Web UI、MCP、CLI、事件系统、部署** |

---

> *第八次全局扫描完成，未修改任何代码。本轮 5 个方向聚焦于最接近用户和集成者的表面层——Web UI、CLI、MCP、事件系统、部署拓扑。加上前七轮，整个代码库已从 8 个视角 40 个方向被全面审视，形成一个完整的 360° 评估套件。*
