# AGENTS.md — Agent / 贡献者工作合约

> 加载顺序：`docs/agent/BOOTSTRAP.md` → 本文 → `docs/agent/CURRENT_SPRINT.md` → `docs/agent/TASK.md` → `HARNESS.md`。
> 本文只保留架构边界、行为契约与不变量；概述见 `README.md`，设计见 `docs/architecture.md`。
> 配置名称、默认值与激活条件查 `docs/configuration.md`、`.env.example`，并核对 `internal/config/`；不得自创环境变量或复制易漂移的默认值。

## 0. 工程约束

- **合入门禁：** `make check` + `python3 cli.py accept`；失败不得合入。实际步骤以 `Makefile`、`checks/`、`.github/workflows/ci.yml` 为准，不把旧文档的“等价步骤”当实现。
- `make check` 包含格式、vet（含 integration 类型检查）、构建、单测、定向 race 与 CLI 检查；不运行 `go mod tidy`。文件上限 **500 行**，排除项/豁免按 `engineering.yaml`；禁止放宽阈值绕过检查。
- **Review 约定：** 函数 ≤50 行、复杂度 ≤10、避免 >300 行 God 类型及 `utils/common/helper` 包；覆盖率 ≥50%（目标 80%）。告警不等于硬门禁，优先重构再扩功能。
- 后端门禁使用 SQLite + local FS + `httptest`/mock，不依赖外部网络或 Docker；真实 PostgreSQL/Qdrant 测试单独运行。
- 只改批准范围；不得改动 Sprint 禁止项。区分“存在实现 / 本轮验证通过 / 提案”，不凭 ROADMAP 或历史验收记录宣称当前已交付。

## 1. 架构与产品边界

入口 `cmd/server/main.go`，装配集中在 `cmd/server/`；Go 版本以 `go.mod` 为准。

```text
REST / S3 / WebDAV / MCP / CLI → FileService → Storage + Repository
                                           → EventBus / durable Outbox → Workers
AI：Extractor → Chunker → Embedder → Index → Search → Chat / Agent
```

- `FileService` 是对象业务唯一入口；协议层只做编解码、scope、协议条件请求，不绕过服务直连 Storage。后台领域组件通过既有端口协作，不把业务堆入 handler 或装配层。
- 默认底座 SQLite + local FS；S3/OSS/COS、Postgres 与 AI/外部索引可选。离线测试也覆盖鉴权、AI 与协议 mock，不能称“仅无鉴权、AI off 被验证”。
- 当前 AI 是**对象级 RAG**；独立知识库、文档发布与多库策略不是既有产品能力，需另定领域模型与验收，不能把 Bucket 改名就宣称知识库管理已完成。

## 2. 组件契约

### 2.1 FileService · `internal/service`

1. 统一对象/版本/Multipart/软硬删除/恢复、Tags/元数据/ACL、Range、预签名、租户与桶配额、WORM/Legal Hold、事件与 ChunkCleaner。受保护版本不得非版本化覆盖或硬删；版本化上传可创建新版本。
2. Key 校验只在 **FileService + Storage** 两层执行（禁空、`/` 前缀、`..`）；handler 不另建校验规则。校验元数据/尺寸/Content-MD5，坏摘要报 `ErrBadDigest` 并清理已写 Blob；`_aero_*` 为保留元数据，客户端不可写删，内部字段更新使用原子键级接口。
3. 删除同步清理 Chunk，失败 **warn 后继续**；硬删保持 Blob→数据库删除顺序。`_aero_scrub_status=corrupt` 时 Get/Stat 报 `ErrObjectCorrupt`；scrub 验证修复后清除标记。配额查询失败不得绕过预检。

### 2.2 协议适配器 · `internal/api/`、`internal/mcp/`

| 入口 | 必须保持的边界 |
|---|---|
| REST `/v1` | `internal/api/rest/router.go` 注册；GET/HEAD/OPTIONS 派生 read，其余 write；管理操作另做 admin/租户授权。条件请求由适配器判断；OpenAPI 注册与响应契约同步更新。 |
| S3（`S3_COMPAT_PREFIX`） | 各操作执行 bucket policy；SigV4 在 `internal/auth` 验签。Multipart 绑定 tenant/bucket/key，完成核对 part 顺序与 ETag；不得仅凭 upload ID 操作他人上传。 |
| WebDAV（`WEBDAV_PREFIX`） | 在 chi 外分发，仅 default bucket；MOVE 为 copy→delete（失败回滚），不是原子 rename；LOCK 为进程内锁。 |
| MCP HTTP / stdio | 文件工具恒有，search/chat 仅依赖就绪时暴露；resources 跨租户 URI 拒绝。write/delete 仅 default bucket，delete 为软删；HTTP 鉴权启用时需 write + `audit:event:write`（admin 蕴含）。 |

### 2.3 AI · `internal/ai`

- `AI_INDEX_ENABLED` opt-in；nil embedder/LLM/reranker 或 AI 故障不得破坏文件 CRUD。默认提取文本；PDF/Office/OCR 需兼容的远程解析服务，不宣称内置支持。
- `vector/bm25` 保持后端顺序；仅 `hybrid` 做 RRF，平分按 chunkID ASC。过滤不匹配的 EmbedModel；重建/PII tag 更新绑定具体对象，启用访问控制时结果先过 FileService 授权，缓存不能绕过授权。
- Reranker 失败：warn + 原排序；hybrid 单模态失败：健康模态 + warn + `ai_search_degraded_total`，无响应降级标记；双失败或 context 超时仍报错。跳过对象记 `IncIndexerSkip(unsupported|error|empty)`。
- Chat 返回 answer/model/citations；Agent 返回 answer/model/steps，**无 citations**。Chat SSE 仅 `token/done/error`，citations 放 done；发送头后错误用 error 帧。
- Chat 日预算需配置计价，per-tenant 覆盖优先；超限 `/chat`→402 `BudgetExceeded`，stream→SSE error。Agent 仅限步数，不检查费用预算，不宣称统一 AI 硬预算。
- AI 路由独立限流与请求超时；`AI_DEGRADED_MODE` 是全局 AI kill-switch，返回 503。

### 2.4 事件、任务与集成

- EventBus 先持久化后本地非阻塞广播，满缓冲 drop；**不是可靠投递承诺**。FileService 对象软/硬删除的数据库变更、审计及 `deleted@1.1/notify@1.1` 同事务写 Outbox；Blob、用量不因此成为跨层原子事务。
- 核心删除 Outbox Relay **默认开启**；`EVENT_OUTBOX_ENABLED=false` 只停 relay，不能停事务写事实。投递可能重复，接收方必须幂等；claim/complete/retry 保持 owner/token 栅栏。
- JobPool 持久化去重、退避重试、失败终态及 stuck-job 回收；不承诺 exactly-once。AV/复制需启用且有 workers；复制由 created 触发，已有可选 resync，删除不复制。Webhook 使用 HMAC、durable retry/DLQ。
- Reconcile/scrub/lifecycle/retention/upload-GC 是**独立定时器**，非 JobPool；可选 `leases` 单例锁。删除孤儿 Blob 必须 opt-in；保持保护检查、精确 StorageKey 对账与可重跑性。
- Billing/Audit Governance 为可选适配器，核心依赖端口，禁直连兄弟项目数据库、双向主数据写入或审计反调业务；契约见 `docs/agent-context/platform-contract.md`、`docs/snaplink-{billing,audit-governance}.md`。计费用量与事实同事务；积压降级不直接摘除核心就绪实例。

### 2.5 Auth 与 Middleware

HTTP 全局入站顺序（唯一装配 `internal/server/chain.go`，生产与测试共用）：

```text
RequestID → BucketCORS → CORS → SecureHeaders → MaxBodySize → Auth → Tenant
          → RateLimit(global) → OTel → Recoverer → Concurrency → AccessLog
```

- **Auth≺Tenant≺RateLimit；RequestID 最外、AccessLog 最内**。handler 不自挂全局链；AI/Admin 路由另套独立限流，并发按读写权重及可选 per-tenant 上限控制。
- API Key/本地 JWT/SigV4/匿名公读复用现有 auth；外部令牌验证必须用 Snaplink `interfaces/ssoclient/rs`，OIDC Authorization Code+PKCE 必须用 `remote.BrowserFlow` + `TokenClient`，Aero 只映射 Principal。
- tenant-scoped 凭据与冲突 `X-Aero-Tenant`→403；`*` 为 operator。已知停用租户、显式 Deny、撤销分享在文件/版本/分享/公开资源/AI 入口一致生效，不以 UI 隐藏替代服务端授权。
- 普通删除不继承 write、owner 或通用 `*`；管理删除走 `vault.file.delete` 独立授权。`ACCESS_DELETE_FAIL_CLOSED=true` 为默认；S3 无授权 provider 始终拒绝删除，管理删除 provider 缺失/故障亦 fail-closed；可信系统任务例外保持租户边界。
- REST 预签名 GET/PUT 回到 Aero，签名绑定方法/租户/路径/过期时间，仍过策略和对象状态检查；仅 capability signer 不启用普通鉴权。多副本共享 ≥32-byte `AUTH_PRESIGN_SECRET`，空值为进程随机。
- 公共旁路路径以 auth/rate-limit/tenant-status 实现为准（健康/UI/文档/OIDC 等）；分享/公开资源只免普通凭据，仍校验 capability、租户和资源权限，不得扩大为通用旁路。

## 3. 交付面与能力边界

- **定位入口而非复制功能清单：** REST/OpenAPI→`internal/api/rest/{router,specgen}.go`；根路由/协议分发→`cmd/server/http.go`；CLI→`internal/cli/`；SDK→`sdk/` 各 README；指标/告警→`internal/telemetry/`、`deploy/`。
- **Web UI：** `web/` 为 React/TypeScript + Iris UI，构建到 `internal/webui/static/app/` 后 Go embed；`/ui/` 为主入口、`/ui/app/` 为别名、`/ui/legacy/` 为回退。当前 React token 仅存内存，不落浏览器持久存储，不转发给其他服务。
- **缩略图：** JPEG/PNG/GIF 输入、JPEG 输出；精确对象/版本读取优先于 `/thumbnail` 子资源。缓存/304 不绕过授权、损坏检查与源身份校验；无 generation 证明不缓存、不发可复用强验证器。保持解码预算/准入、context 取消、Content-Encoding 拒绝、SSE-C/SSE-KMS 缓存禁入及客户端错误脱敏。
- **能力不得夸大：** Restore 是软删恢复，StorageClass transition 当前仅更新元数据，非 Glacier 恢复/真实冷迁移；`/lineage` 是 AI usage 列表，非关系图；archive export 是授权对象+manifest，非完整治理备份；snapshot 主要服务 SQLite+local，数据库镜像一致不等于 DB+Blob 共用恢复点。
- Local SSE/KMS 使用 `STORAGE_LOCAL_SSE_*`，唯启动重包裹为 `STORAGE_SSE_REWRAP_ON_START`；优先级 KMS > URL/keyfile SecretProvider > 单密钥。不要假设所有存储/加密/浏览器下载路径都是有界内存流式。

## 4. 硬性不变量（编号不可重排）

| # | 契约 |
|---|---|
| **I1** | `s.rebind` 按出现顺序将每个 `$N`→`?`；同值也用新占位符，禁止复用编号；时间文本用 `RFC3339Nano`，已有 `_ns` 列按 schema 存整数纳秒。 |
| **I2** | Schema 变更新增 `internal/repository/migrations/{sqlite,postgres}/NNNN_*.{up,down}.sql` 四文件；禁止改/重编已应用迁移。Migrate 只自动执行 up、跳过已应用版本，无自动 down runner。 |
| **I3** | `storageKey=path.Join(tenant,bucket,key)`；版本 Blob 追加 `@v<version_id>`，权威 ID 来自数据库。GC 按精确 `ListStorageKeys` 集合匹配，禁反解析 `@v`；Key 校验边界见 §2.1。 |
| **I4** | 全局链固定 12 环，复用 `internal/server/chain.go`；顺序与旁路边界见 §2.5，不能在 handler 重建或删环。 |
| **I5** | AI/外部索引/事件传输/AV/复制/计费/治理/Prometheus/cluster/retention/WebDAV/S3/熔断/校验 opt-in，默认不启用；nil AI 不破坏 CRUD。Web UI 与核心删除 Outbox 默认开启，不误归入“所有 events 关闭”。 |
| **I6** | Stdlib 优先；新增依赖须论证并运行 `go mod tidy`。Go 单测用 `testing`，禁 testify/gomega 等断言框架。 |

## 5. 变更与验证清单

- **改入口：** 注册 route/scope + 运行时 OpenAPI 信息 + 正反例测试；权限/删除变更覆盖 REST/S3/WebDAV/MCP/分享/AI 中相关入口，含 tenant 隔离、停用、Deny/撤销与 provider 故障。
- **改后端/任务：** Storage 过 contract suite，正确透传可选 generation 等能力；Schema 遵守 I1/I2；job 注册 handler 后入队，考虑去重/幂等、失败恢复与背压，不能以修改文档代替实现。
- **测试：** `t.TempDir()` + SQLite 迁移 + `storage.NewLocal`；AI 用 `ai.MockLLM`、`ai.NewHashEmbedder(dim)`（禁裸 HashEmbedder）。handler 用 `httptest` 并显式提供所需 Principal/Tenant；全链测试复用 `server.ApplyMiddleware`，不是调换 Auth/Tenant 包装顺序。
- **按影响追加：** 并发用 `make test-race`；实库用 `make test-integration[-qdrant]`（可选，探活失败会 skip，需核对实际运行结果）。前端交付执行 `make web-check` → `make build` → `make web-smoke`，提交对应嵌入产物；Go 门禁不替代浏览器冒烟。
- 完成时说明改动路径、实际运行检查及未验证边界；只修本任务问题，不覆盖用户改动，不把检查失败隐瞒为成功。
