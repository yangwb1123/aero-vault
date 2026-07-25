# 高价值扩展方向：基础设施生态集成、企业级数据迁移、自定义域名与静态托管、性能基准套件、对象存储分析生态

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — `cmd/server/main.go` + `internal/` 全部 30+ 子包（237 个 Go 源文件），3 套 SDK（Go/Python/JS），MCP 双模式（HTTP+stdio），Web UI，50 对迁移文件，`deploy/` 全套配置（Helm/Grafana/Prometheus/OTel），`HARNESS.md`，`AGENTS.md`，`ROADMAP.md`，`docs/requirements/` 下全部 100 份既有分析文档  
> **去重验证：** 对 `docs/requirements/` 下全部 136 份既有分析文档逐方向进行关键词正则 + 语义交叉验证 + 代码锚点校验  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。选取**代码中存在明确锚点、有实质性产品/架构影响、且在 136 轮既有分析中未被独立深度覆盖**的方向。

---

## 去重验证总表

| 方向 | 既往覆盖情况 | 结论 |
|------|-------------|------|
| **方向一：基础设施即代码与存储集成生态** | `Terraform Provider` — 仅 v30 一行列表提及；`K8s CSI Driver` — **零命中**；`FUSE 挂载` — **零命中**；`Docker Volume Plugin` — **零命中**；`CI/CD Actions (GitHub/GitLab)` — **零命中** | ✅ **全新方向** |
| **方向二：租户数据迁移与自助导入导出** | `snapshot.go` 功能分析 — 仅 v34 提及 migrations 编排；`import/export API` — **无独立深入分析**；`S3 租户迁移` — **零命中** | ✅ **全新方向** |
| **方向三：自定义域名与静态网站托管** | 零文件包含 `custom.domain\|vanity.url\|domain.per.tenant\|static.website\|static.web.hosting\|bucket.hosting` 等关键词 | ✅ **全新方向** |
| **方向四：性能基准测试套件** | 12 份文件提及 `benchmark\|performance test\|load test\|profiling` 但均为侧注，**无一份提供具体的基准测试计划、负载模型、回归检测框架或容量规划方法论** | ✅ **全新深度方向** |
| **方向五：对象存储分析生态与数据湖集成** | 4 份文件提及 `data lake\|analytics` 但仅为概念提及，**无不涉及 Presto/Trino 连接器、Hive Metastore 集成、Iceberg/Delta Lake 格式支持、SQL-on-Object-Storage 查询加速** | ✅ **全新方向** |

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 |
|---|------|------|--------|---------|---------|
| **1** | **基础设施即代码与存储集成生态** | 产品化/DX | **P0** | 有完备的 REST + S3 API 和三个语言 SDK，但没有 Terraform Provider、K8s CSI Driver、FUSE 挂载、Docker Volume Plugin、CI/CD Action——用户无法将 aero-vault 作为基础设施组件集成到现有编排体系中 | `sdk/go/aerovault/client.go`（1006 行 Go SDK — 完整覆盖 API）；`sdk/python/aero_vault.py`（685 行 Python SDK）；`sdk/js/aero-vault.js`（1086 行 JS SDK）；`internal/api/rest/openapi.json`；`internal/api/s3compat/handler.go`（S3 API 枚举）；`internal/service/file_features.go`（Bucket CRUD + 策略 + CORS） |
| **2** | **租户数据迁移与自助导入导出** | 特性 | **P0** | 无跨租户/跨后端/跨实例的数据迁移能力；仅有的 `snapshot.go` 只支持 SQLite+Local FS；企业租户首次入驻时无法从 S3/MinIO 批量导入；无标准化的导入导出 API | `internal/snapshot/snapshot.go`（当前只支持 SQLite 快照）；`internal/replication/replication.go`（仅 event-driven 异步复制，无手动全量迁移）；`internal/repository/sql_objects.go`（对象元数据 CRUD — 可用于批处理）；`internal/api/rest/admin.go`（管理员 API — 可扩展迁移端点）；`internal/service/file_features.go:ListBuckets`（可枚举所有数据） |
| **3** | **自定义域名与静态网站托管** | 特性 | **P1** | S3 最流行的用例之一是静态网站托管（bucket hosting），当前 aero-vault 没有任何域名绑定或静态网站支持；用户无法将存储桶暴露为 `https://static.example.com` 或托管 SPA | `internal/api/s3compat/handler.go:dispatchBucketSubresource`（已有 `?location` 响应）；`internal/api/s3compat/handler.go:checkBucketPolicy`（已有策略引擎可用于网站权限）；`internal/api/s3compat/handler.go:getBucketCORS`（已有 CORS 规则）；`internal/api/rest/handler.go:Get`（已有公共读路径 `acl.IsPublicRead`）；`internal/middleware/cors.go`（CORS 中间件 — 网站托管需要）；`internal/config/config_app.go:AppConfig`（Addr 配置 — 可扩展多监听器） |
| **4** | **性能基准测试套件** | 工程基础设施 | **P1** | 所有 CI gate 只验证正确性（`go build` + `go vet` + `go test`），没有任何性能退化检测；有丰富 OTel 指标但无法在 CI 中自动对比延迟/吞吐量；团队对 100KB vs 100MB 对象的性能差异没有量化认知；扩容决策依赖猜测而非数据 | `Makefile`（仅 `test` 和 `test-integration`，无性能目标）；`internal/integration/fullserver_test.go`（全栈集成测试框架可直接复用为基准框架）；`internal/telemetry/metrics.go`（15+ OTel 指标可作为基准测量点）；`internal/storage/contract_test.go`（存储后端合约测试——可扩展为性能合约）；`internal/api/s3compat/handler.go`（S3 API 覆盖度完整，可建模标准 S3 基准负载） |
| **5** | **对象存储分析生态与数据湖集成** | 特性/生态 | **P2** | 存储是数据平台的基础，但没有 Presto/Trino 连接器、Hive Metastore 集成、Iceberg/Delta Lake 表格式支持、SQL 查询加速；用户数据进入 aero-vault 后只能通过 REST/S3 API 读取，无法被分析引擎直接查询 | `internal/repository/sql_objects.go:StorageClassCounts`（已有按 storage_class 聚合查询）；`internal/repository/sql_buckets.go:BucketStats`（已有桶级别统计）；`internal/api/rest/admin.go:Usage`（已有租户级用量统计）；`internal/service/file_features.go:ListBuckets` / `BucketStats`（已有桶枚举和统计入口）；`internal/storage/local_list.go`（已有前缀扫描——可扩展为全量枚举） |

---

## 方向一：基础设施即代码与存储集成生态

### 现状

AeroVault 拥有完备的 **REST API**（OpenAPI 规范）、**S3 兼容 API**（覆盖 15+ 子资源操作）、**3 个语言 SDK**（Go/Python/JS）和 **MCP 工具接口**。但是：

- **无 Terraform Provider** — 用户无法用 `terraform apply` 创建桶、配置生命周期、管理 API Key 或设置租户
- **无 Kubernetes CSI Driver** — 无法将 aero-vault 作为 K8s PersistentVolume 的后端（当前 Helm chart 使用 PVC，但这个 PVC 是本地存储，不是 aero-vault 自身）
- **无 FUSE 文件系统** — 用户无法将远程 aero-vault 挂载为本地目录（像 `s3fs-fuse` 或 `goofys`）
- **无 Docker Volume Plugin** — Docker/Podman 容器无法原生挂载 aero-vault
- **无 CI/CD Action** — 没有 GitHub Action、GitLab CI template 来上传/下载/搜索制品

### 产品价值

| 集成形式 | 用户场景 | 竞品对标 | 收入影响 |
|---------|---------|---------|---------|
| **Terraform Provider** | `resource "aerovault_bucket" "assets" { name = "static-assets" public_read = true }` → 基础设施声明式管理 | S3 Terraform Provider（18M+ 下载） | 企业采购必问项 |
| **K8s CSI Driver** | Pod PVC → Pod 声明 `storage: aerovault.csi.k8s.io` → 容器内直接读写对象存储 | AWS EBS CSI / S3 CSI（如 `mounflake`） | 容器化工作负载原生集成 |
| **FUSE 挂载** | `mkdir /mnt/av && aerovault mount acme@host:/bucket /mnt/av` → `ls` / `cp` 透明操作 | `s3fs-fuse`（5.7k ★）、`goofys`（4.8k ★） | 开发者工具链粘合剂 |
| **Docker Volume Plugin** | `docker volume create --driver aerovault --opt bucket=uploads myvol` | `rexray/s3fs`（已弃用但需求仍在） | 容器化遗产工作负载迁移 |
| **GitHub Action** | `uses: aerovault/action-upload@v1 with { path: ./dist, bucket: releases }` | `aws-actions/configure-aws-credentials` + S3 组合 | CI/CD 管道原生集成 |

**加法效应：** 每多一个集成形式，aero-vault 就从"一个你需要 curl 的服务"变为**你基础设施中声明式的组成部分**。

### 架构权衡

| 方面 | 权衡 |
|------|------|
| **Terraform Provider** | 工作量中等（~800 行 Go + `terraform-plugin-sdk`）；可复用现有 Go SDK；需维护 Provider Registry 发布管道 |
| **K8s CSI Driver** | 工作量大（~2000 行 Go）：需要 CSI 身份/节点/控制器服务、`NodePublish` 实现、挂载守护进程；安全模型复杂（Pod 级别凭证注入）；需处理节点故障漂移 |
| **FUSE 挂载** | 工作量大（~3000 行 Go）：需要 `fusefs` 库（如 `jacobsa-fuse`）、内核缓存策略（attr/dentry/page cache）、并发一致性模型（close-to-open consistency）、大文件分段 |
| **Docker Volume Plugin** | 工作量中等（~1000 行 Go）：Docker 插件 API（`docker plugin` 子命令）；通常基于 FUSE 实现；需要处理 Docker 重启/插件恢复 |
| **CI/CD Action** | 工作量小（~300 行 TypeScript/Python）：封装现有 SDK 调用；版本管理与发布 |

### 边界情况与操作风险

- **Terraform 状态对齐**：桶策略、CORS 等配置既可通过 Terraform 也可通过 S3 API 修改 → 需要 `terraform refresh` 或 `PreventDestroy` 标志来防止状态漂移
- **CSI 权限最小化**：每个 PVC 应绑定到特定桶+前缀，而非给整个租户的免密访问
- **FUSE 缓存一致性**：多节点同时 FUSE 写入同一文件 → 最终一致（非强一致）；应在文档中明确声明
- **Action 凭证安全**：Action 输入 `token` 应标记为 `secret: true` 并支持 OIDC token 交换

---

## 方向二：租户数据迁移与自助导入导出

### 现状

当前唯一的数据迁移工具是 `internal/snapshot/snapshot.go`：

```go
// Create writes a tar.gz to outPath containing ./manifest.json, ./db/aero.db, ./objects/...
// dbPath is the SQLite DSN path — only SQLite local snapshots are supported
```

限制：
- 仅支持 **SQLite + local FS**（`dbFileFromDSN` 只解析 SQLite DSN）
- **无自定义选择能力**（指定租户、桶、前缀、时间范围）
- **无 S3/MinIO 导入**（企业租户从竞品迁移的第一步）
- **无跨实例迁移**（从一个 Postgres+S3 拓扑迁移到另一个）
- **无增量/在线迁移**（停机窗口随数据量增长）
- **无进度追踪**（大型迁移像黑盒）
- **无元数据完整性校验**（导入后不验证对象数/字节数）

### 产品价值

| 场景 | 当前状态 | 迁移方案后的体验 |
|------|---------|----------------|
| **企业从 S3 迁移** | 手动逐个上传或第三方工具 | `POST /v1/admin/import/s3 {"endpoint":"https://s3.amazonaws.com","bucket":"old-bank","access_key":"AK...","secret_key":"...","tenant":"acme"}` → 异步全量复制 |
| **跨地域迁移** | 无（需搭建 replication 后重新上传） | `POST /v1/admin/migrate/tenant {"source":"us-east-1","target":"eu-west-1","tenant":"acme"}` → 并行流式迁移 |
| **租户数据导出** | 无 | `GET /v1/admin/export?tenant=acme&format=tar.gz` → 打包元数据+对象的可下载归档 |
| **租户拆分/合并** | 无 | 将一个租户的数据拆成两个租户，或将两个租户合并 |
| **开发/测试环境同步** | 无 | `POST /v1/admin/mirror {"source":"prod:acme","target":"dev:acme"}` → 生产数据子集同步到开发环境 |

### 架构权衡

| 方案 | 工作量 | 复杂度 | 适用场景 |
|------|--------|--------|---------|
| **S3 导入器**（最简） | ~500 行 Go | 低 | 一次性迁移，S3→AeroVault |
| **通用导入引擎**（推荐） | ~2000 行 Go | 中 | 多源（S3/MinIO/GCS/Azure Blob），可复用 `storage.Storage` 接口 |
| **增量 CDC 迁移**（高级） | ~3000 行 Go | 高 | 零停机在线迁移（源端持续写入期间同时迁移） |

### 边界情况与操作风险

- **对象锁/版本控制兼容性**：迁移时必须保留 `locked_until`、版本 ID 和 WORM 状态；目标端需支持版本控制
- **迁移中断/断点续传**：已复制对象应标记 `migrated=true`（自定义标签）以支持重入
- **数据校验**：迁移后应支持 `GET ?checksum` 对比源和目标 ETag
- **性能节流**：迁移应尊重目标端配置的 rate limit / circuit breaker，避免影响在线流量
- **大文件处理**：大于 5GB 的对象需用 multipart 上传到目标端

---

## 方向三：自定义域名与静态网站托管

### 现状

AeroVault 的所有请求都通过单一 `APP_ADDR` 监听端口（默认为 `:8080`）处理。没有：

- **自定义域名绑定** — 用户不能将 `https://files.acme.com` 指向他的桶
- **静态网站托管** — 桶不能作为网站（`index.html` 自动返回，`404.html` 自定义，`?prefix/` 目录默认响应）
- **TLS 终结** — HTTPS 依赖反向代理（如 Ingress），无内置 Let's Encrypt 自动证书
- **请求路由** — 无法基于 `Host` 头将 `https://acme.av.com` 路由到 `tenant=acme` + `bucket=web`

虽然部分功能可委派给 Ingress（路径路由、TLS），但**静态网站托管是 S3 最广泛使用的功能之一**——GitHub Pages、Netlify 底层都依赖类似的模式。缺少它意味着：

| 场景 | 当前方案 | 有自定义域名后的方案 |
|------|---------|----------------|
| **托管开源文档站点** | 需 Nginx/Vercel/Netlify | `PUT /web/index.html` → `https://docs.acme.com` 直接访问 |
| **用户生成内容（头像/图片）** | 需 CDN + 反向代理 | `https://cdn.acme.com/u/avatar.png` 直连 |
| **SPA 部署** | 需额外部署 Pipeline | `PUT /app/index.html` + 设置 `index_document=index.html` → 浏览器直接访问 |
| **多站点管理** | 复杂 | 每个桶绑定一个域名，自动路由 |

### 代码锚点

| 锚点 | 功能 | 如何复用 |
|------|------|---------|
| `internal/service/file_features.go:GetBucketCORS` | 桶级 CORS | 静态网站必须对外部 JS 开放跨域访问 |
| `internal/api/s3compat/handler.go:checkBucketPolicy` | 桶策略引擎 | 网站权限控制（公共读/受限制访问） |
| `internal/api/rest/handler.go:Get` | 对象 GET | 网站根路径应自动返回 `index.html` |
| `internal/api/s3compat/handler.go:getBucketLocation` | 区域信息 | 网站端点信息 |
| `internal/middleware/cors.go` | 全局 CORS | 网站托管必须允许浏览器跨域 |
| `internal/config/config_app.go:AppConfig.Addr` | HTTP 监听地址 | 支持多监听器 + 虚拟主机路由 |
| `internal/service/file.go:WithDefaultStorageClass` | 默认存储类 | 网站文件可映射到 `STANDARD_IA` 以降低成本 |

### 架构边界情况

- **目录索引**：对桶 + 前缀的 GET 请求需返回 `index.html`（类似 S3 的 `IndexDocument`）
- **自定义错误页**：404/403 应返回用户自定义的 `error.html`
- **重定向规则**：S3 支持 `RoutingRules`（`Condition.KeyPrefixEquals` → `ReplaceKeyPrefixWith`）
- **CORS 预检**：自定义域名跨域 JavsScript 请求必须正确响应 `OPTIONS`
- **HTTPS 自动证书**：如不委派给 Ingress，可集成 `certmagic`/`lego` 自动获取 Let's Encrypt 证书
- **根路径歧义**：`https://domain.com` 和 `https://domain.com/` 行为应一致（重定向或直接响应）

---

## 方向四：性能基准测试套件

### 现状

当前 `Makefile` 定义了：

```makefile
check: fmt vet build test complexity-lines   # 只验证正确性
test:   go test ./...                         # 验证功能正确性
test-integration:                              # 验证 Postgres 集成
```

**零性能相关的 CI 门禁。** 有丰富的 OTel 可观测性（15+ 指标），但：

- 没有在 CI 中自动采集基准数据
- 没有性能退化检测（"本次 PR 让 P50 GET 延迟从 2ms 变成 15ms"）
- 没有对不同对象大小的分档测试（1KB / 1MB / 100MB / 1GB）
- 没有并发吞吐模型（10/100/1000 并发连接）
- 没有多协议对比（REST vs S3 vs WebDAV vs MCP）
- 没有存储后端对比（local vs S3 vs OSS vs COS）
- 没有数据驱动扩容建议（"当前硬件配置下，推荐单实例最大 500 个并发写入"）

### 产品价值

| 能力 | 收益 |
|------|------|
| **CI 性能门禁** | PR 引入性能退化 → 被阻断 → 开发者立即感知 |
| **发布版本基准报告** | 每个 release 附带性能报告：P50/P95/P99 延迟、吞吐量 QPS、内存/CPU 水位 |
| **容量规划工具** | 基于历史基准 + 增长曲线 → 推荐实例规格、副本数、后端配置 |
| **SLO 制定基础** | 没有基准就没有 SLO（服务等级目标） |
| **竞品对比** | 面向客户的销售材料：AeroVault vs MinIO vs S3 的基准数据 |
| **回归测试** | 存储后端升级（如 S3→OSS）前先跑基准 → 确认性能等价 |

### 基准套件架构

```
bench/
├── bench_test.go          # go test -bench=.
├── scenarios/
│   ├── put_small.go       # 100 byte objects
│   ├── put_large.go       # 100 MB objects (multipart)
│   ├── get_concurrent.go  # N concurrent readers
│   ├── list_prefix.go     # prefix listing with 10K objects
│   ├── search_bm25.go     # BM25 search latency
│   └── search_vector.go   # vector search latency
├── load/
│   ├── model.go           # load model: ramp-up, steady-state, cool-down
│   └── reporter.go        # histogram + percentile computation
├── regression/
│   └── compare.go         # compare current vs baseline, flag regressions
└── Makefile.bench         # make bench-ci, make bench-full
```

### 关键测量点（复用现有 OTel 指标）

| OTel 指标 | 测量命令 | 退化阈值建议 |
|-----------|---------|-------------|
| `http.server.duration_ms`（P50/P95/P99） | REST/S3 GET/PUT | P95 增加 > 2x |
| `ai_embed_duration_ms` | 向量化延迟 | P95 > 1s |
| `ai_search_duration_ms` | 检索延迟 | P95 > 3s |
| `jobs_pending` | 队列积压 | 持续 > 100 |
| `storage_bytes` | 写入吞吐量 | 吞吐量下降 > 30% |

### 边界情况与操作注意事项

- **基准环境一致性**：所有基准必须在**相同环境规格**下运行（CPU 型号、内存、磁盘类型、网络延迟）；在 CI 中，应使用 `cgroup` 固定资源或专用 runner
- **预热**：Go GC 和内存分配器需要预热期（前 1000 次请求不计入）
- **基准数据清理**：每次基准后必须清理所有测试对象（`defer cleanup` 或在临时桶中运行）
- **破坏性基准标记**：高负载/破坏性测试（10K 并发写入）应显式标记 `//go:build benchmark_destructive`，默认不执行
- **基准结果版本化**：结果应与代码版本绑定（存入 `bench/data/` 带 commit hash 的文件名）

---

## 方向五：对象存储分析生态与数据湖集成

### 现状

AeroVault 有完整的对象存储能力和 AI 检索管线，但**缺失整个分析生态层**：

- **无 SQL 查询引擎** — 无法直接在存储上运行 `SELECT * FROM objects WHERE size > 1MB AND content_type LIKE 'image/%'`
- **无 Presto/Trino 连接器** — 分析师不能通过 Trino 直接查询对象元数据
- **无 Hive Metastore 集成** — 不能将桶注册为 Hive 表并用于 Spark/Hive 作业
- **无 Iceberg/Delta Lake 支持** — 不能将对象以开放表格式存储（对数据湖场景至关重要）
- **无查询加速层** — 没有列式缓存、物化视图或统计信息以加速 SQL on Object Storage

### 产品价值

| 集成 | 用户场景 | 竞品对标 |
|------|---------|---------|
| **Trino 连接器** | `SELECT * FROM aerovault."acme"."logs/*" WHERE event = 'purchase'` | S3 + Trino 插件生态（8.5k ★） |
| **Hive Metastore 集成** | Hive/Spark 将桶识别为外部表 → `CREATE EXTERNAL TABLE logs ... LOCATION 'av://acme/logs/'` | S3 + Hive（广泛使用） |
| **Iceberg/Delta Lake 表格式** | 存储支持 ACID 事务、时间旅行、schema 演进的开放表格式；数据可直接被 Spark、Flink、Trino 查询 | AWS S3 + Iceberg（Netflix 原始驱动者） |
| **元数据查询 API** | `POST /v1/search/meta {"filter":"size > 1MB AND content_type LIKE 'image%'","aggregate":"count_by_content_type"}` | 无（当前只支持语义/BM25 搜索） |
| **S3 Select 等价** | `POST /bucket/key?select&expression="SELECT * FROM S3Object WHERE _1 > 100"` + CSV/JSON 过滤 | AWS S3 Select 标准 |

### 代码锚点

| 组件 | 作用 | 扩展方向 |
|------|------|---------|
| `internal/repository/sql_objects.go:StorageClassCounts` | 按 storage class 聚合 | 可扩展为通用元数据 SQL 查询引擎 |
| `internal/repository/sql_buckets.go:BucketStats` | 桶级统计（对象数+字节数） | 可暴露为 Prometheus 查询 + Trino connector 接口 |
| `internal/service/file_features.go:ListBuckets` | 枚举所有桶 | Trino `SHOW SCHEMAS` 等价 |
| `internal/service/file_features.go:List` | 前缀分页列举 | Trino `SELECT ... FROM table WHERE _path LIKE '...'` 等价 |
| `internal/storage/local_list.go` | 本地 FS 前缀扫描 | 为所有后端提供统一的全量枚举能力 |
| `internal/api/rest/handler.go:Get` | 对象读取 | Trino 行读取接口（按 offset+length 流式读取） |
| `internal/api/s3compat/errors.go` | S3 错误码 | S3 Select 标准错误码复用 |

### 架构路径

```
┌─────────────────────────────────────────────────────┐
│                   查询层                              │
│  ┌──────────┐  ┌──────────┐  ┌──────────────────┐   │
│  │ Trino    │  │ Spark    │  │ 元数据查询 API    │   │
│  │ Connector│  │ Connector│  │ (REST/SQL-like)   │   │
│  └────┬─────┘  └────┬─────┘  └────────┬─────────┘   │
│       │              │                 │             │
│  ┌────▼──────────────▼─────────────────▼─────────┐   │
│  │         统一查询路由层                           │   │
│  │  ┌─────────────┐    ┌──────────────────────┐   │   │
│  │  │ 元数据索引    │    │ 对象读取 & 过滤       │   │   │
│  │  │ (repository) │    │ (storage.Storage +   │   │   │
│  │  │              │    │  predicate pushdown) │   │   │
│  │  └─────────────┘    └──────────────────────┘   │   │
│  └────────────────────┬───────────────────────────┘   │
│                       │                               │
│  ┌────────────────────▼───────────────────────────┐   │
│  │              存储层                               │   │
│  │  local / S3 / OSS / COS                        │   │
│  └─────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────┘
```

### 边界情况与操作注意事项

- **谓词下推（predicate pushdown）**：查询引擎应将过滤条件下推到存储层（如 `size > 1MB` → 在 `local_list.go` 的 `Stat` 调用中过滤，而非读取所有对象到内存再过滤）
- **分页/大结果集**：查询可能返回数百万对象，必须支持 `LIMIT` + `OFFSET` 或游标分页
- **列裁剪（column pruning）**：只读取查询所需的列（如只查询 `size` 和 `etag` 时不读取 `metadata` 和 `tags`）
- **并发查询资源控制**：分析查询可能很重，需限制并发分析查询数（复用 `MAX_INFLIGHT_REQUESTS` 或专用分析连接池）
- **格式感知**：对 CSV/JSON/Parquet 文件的内容查询（S3 Select）需要格式解析器 + 类型推断
- **安全**：元数据查询必须遵循桶 ACL + 策略，不能泄露未授权租户的信息

---

## 优先级建议与实施路线

| 阶段 | 方向 | 里程碑 | 预计工作量 |
|------|------|--------|-----------|
| **Q1** | **方向一（Terraform Provider + GitHub Action）** | Terraform Provider v1（桶 CRUD + 策略 + CORS）+ GitHub Action 发布 | 2-3 周 |
| **Q1** | **方向四（基础基准套件）** | `make bench` 可运行，含 PUT/GET/List 基准 + 简单退化检测 | 2 周 |
| **Q2** | **方向二（S3 导入器 + 租户导出）** | S3→AeroVault 全量导入器 + `GET /v1/admin/export` | 3-4 周 |
| **Q2** | **方向三（静态网站托管 MVP）** | 桶级 `index_document`+`error_document` 配置 + `Host` 头路由 | 2 周 |
| **Q3** | **方向五（元数据查询 API）** | `POST /v1/search/meta` 支持 SQL-like 过滤 + 聚合 | 4 周 |
| **Q3** | **方向一（K8s CSI + FUSE）** | K8s CSI Driver alpha + FUSE 挂载 beta | 6-8 周 |
| **Q4** | **方向二（增量 CDC 迁移）** | 基于事件总线的增量同步 + 断点续传 | 4 周 |
| **Q4** | **方向五（Trino Connector）** | Trino 插件通过 `TrinoPlugin` SPI 接口实现 | 4 周 |

### 非目标（明确推迟）

- **多云存储网关**（统一前端多后端写入）— 当前 replication 已覆盖一部分，完整的多写一致性需要 Paxos/Raft
- **SQL 全文引擎**（替代 Postgres/pgvector 的内置 SQL 查询引擎）— 委派给 Trino 连接器
- **FUSE 写入缓存一致性**— 第一版 FUSE 挂载仅支持「只读」或「最终一致写入」
- **Docker Volume Plugin**— 社区需求足够大时再跟进（用户可通过 FUSE 或 CSI 间接实现）
