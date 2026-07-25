# 高价值扩展方向分析 v35 — 全新视野：数据质量、批量迁移、gRPC/GraphQL 网关、事件可观测、DR 框架

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 230+ `.go` 文件 + `sdk/*` + `deploy/*` + `docs/*` + 48 对迁移文件）
> **分析日期：** 2026-07-10
> **视角：** 资深架构师 / 产品经理 — 聚焦「此前 34 期分析（累计 ~175+ 方向、24,000+ 行分析文本）从未实质触及的 5 个全新高价值方向」
> **去重方法：** 逐领域 `grep` 验证 `docs/requirements/` 下 **34 期既有分析（v1–v34）** + `docs/ROADMAP.md`（10 方向，全部实现） + `docs/adr/DECISIONS.md` + `docs/CHANGELOG.md` + `docs/TODO.md`（全部完成）。每个方向在既有文档中 **零实质性架构分析**（表格中的一行提及或过路引用不构成实质性分析）。
> **原则：** 不编写任何实现代码。每个方向附带：现状 → 代码锚点 → 缺失能力 → 为什么需要 → 架构概要 → 边界情况。

---

## 背景：前 34 期已完成覆盖的去重矩阵

前 34 期 expansion 文档覆盖了 **约 175+ 个方向**，ROADMAP 10 个方向全部实现，TODO 清单全部完成。以下领域已深度覆盖，本期不再重复：

| 领域 | 已覆盖方向数 | 涵盖期数代表 |
|------|------------|-------------|
| AI/RAG 管线（Embed/Search/Chat/Agent/Indexer/Rerank/PII/缓存/预算/漂移/重索引/评估框架/模型路由/语义缓存） | ~28 | v1~v34 |
| S3 兼容协议（子资源/ACL/Policy/CORS/通知/LegalHold/COPY/Batch/Multipart/SSE-C/Select/ListObjectsV2/Tag-Listing/Restore/Accelerate） | ~20 | v1~v34 |
| 存储后端（Local/S3/OSS/COS/KMS/SSE/熔断器/分层/压缩/块级去重/CAS/SSE 轮换/迁移） | ~20 | v1~v34 |
| 认证授权（JWT/API Key/SigV4/OIDC/SAML/SCIM/MFA/前缀级策略/FIPS/Policy Engine/mTLS/客户端证书/临时凭证） | ~20 | v1~v34 |
| 多租户（CRUD/配额/预算/审计/日费用/物理隔离/FGA/IaC/Admin Console/Terraform/计费/自助注册/Plan Tiers/邀请/用量仪表板） | ~20 | v1~v34 |
| 事件/通知/Webhook（总线/传输/过滤/多通道/死信/背压/CDC/Kafka/Lambda 触发/Postgres NOTIFY/事件重放/Serverless Trigger） | ~18 | v1~v34 |
| 复制/HA/集群（CRR/SRR/单例/Federation/多活/CQRS/故障转移/Geo-Distributed/Conflict Resolution） | ~16 | v1~v34 |
| Reconcile/GC/Lifecycle（孤儿/保留/Scrub/版本/Multipart/Noncurrent/存储类转换/标签规则） | ~16 | v1~v34 |
| 合规（WORM/Legal Hold/保留/访问日志/客户端加密/对象锁模式/数据驻留/Geo-Fencing/SOC2/监管链/法证完整性） | ~16 | v1~v34 |
| 可观测性（OTel/Metrics/Grafana/Prometheus/SLO/Tracing/pprof/告警/Debug/Profiling/自适应背压） | ~16 | v1~v34 |
| 工程质量（内存安全/流式加密/并发/压缩/错误模型/测试/性能基准/多协议一致性/CI 门禁/代码质量） | ~16 | v1~v34 |
| 存储分层/生命周期/预测性分层/批量操作/导入迁移/备份DR/压缩/命名空间 | ~14 | v1~v34 |
| Web UI / Admin Console / MCP / CLI 完整性 | ~14 | v1~v34 |
| SDK 跨语言（Go/Python/JS）完整性 | ~9 | v1~v34 |
| 基础设施（TLS/ACME/配置热重载/IP ACL/Feature Flag/Helm/CDN/Data Provenance/熔断器/优雅关闭/自适应并发控制） | ~14 | v1~v34 |

### 本期 5 个方向的去重验证

| # | 方向 | `grep` 验证结果 | 既有覆盖判定 |
|---|------|----------------|------------|
| 1 | **数据质量与 Schema 验证管线（Data Quality & Schema Validation Pipeline）** | `grep -rli "schema.*valid\|data.*quality\|quality.*gate\|data.*profil\|data.*fresh\|quality.*check\|anomaly.*detect\|data.*govern" docs/requirements/` → **0 命中** | ❌ 完全未覆盖 |
| 2 | **批量导入/导出与数据迁移框架（Bulk Import/Export & Migration Framework）** | `grep -rli "bulk.*import\|bulk.*export\|data.*migration.*tool\|import.*tool\|export.*tool\|migrate.*data\|data.*transfer" docs/requirements/` → **0 命中** | ❌ 完全未覆盖 |
| 3 | **GraphQL / gRPC 协议网关（GraphQL / gRPC Protocol Gateway）** | `grep -rli "graphql\|grpc.*gateway\|grpc.*api\|grpc.*proxy\|GraphQL\|gql\|trpc" docs/requirements/` → **0 命中** | ❌ 完全未覆盖 |
| 4 | **事件与 Webhook 可观测仪表板（Event & Webhook Observability Dashboard）** | `grep -rli "event.*dashboard\|webhook.*monitor\|webhook.*observ\|event.*observ\|delivery.*metric\|webhook.*metric\|event.*latency\|webhook.*latency\|webhook.*dashboard" docs/requirements/` → 仅 `extensions-v2.md` 一行表格提及 `webhook_delivery_latency` 指标 | ⚠️ 仅一行表格引用，零实质性架构分析 |
| 5 | **备份与灾难恢复即服务框架（Backup & DRaaS Framework）** | `grep -rli "backup.*as.*service\|DRaaS\|disaster.*recovery.*plan\|point.*in.*time.*restore\|automated.*backup\|backup.*schedul\|dr.*plan\|disaster.*recover\|backup.*verif" docs/requirements/` → 仅 `expansion-v8.md` 一行 CLI 命令提及 | ⚠️ 仅一行 CLI 引用，零实质性架构分析 |

---

## 本期方向总览

| # | 方向 | 类型 | 优先级 | 代码锚点 | 核心痛点 |
|---|------|------|--------|---------|---------|
| 1 | **🔴 数据质量与 Schema 验证管线** | 功能/质量 | **P1** — 数据湖/AI 管线的可信度基础 | `internal/ai/` 无数据质量组件；`internal/service/file_crud.go` 无 upload 时验证钩子 | 写入的数据质量不可控，下游 AI 消费"垃圾进垃圾出"；无 schema 治理工具 |
| 2 | **🟠 批量导入/导出与数据迁移框架** | 功能/运维 | **P1** — 从"无法落地"到"可迁移" | `internal/snapshot/snapshot.go` 仅 SQLite 快照；无导入/导出 CLI 或 API | 用户有存量数据在 S3/MinIO/NAS 上无法批量迁入；迁出也只能逐个对象 GET |
| 3 | **🟠 GraphQL / gRPC 协议网关** | 生态/集成 | **P2** — 新消费群体的接入桥梁 | `internal/api/rest/` `internal/api/s3compat/` `internal/mcp/` 无 gRPC 或 GraphQL | Web/移动开发者习惯 GraphQL 接口；微服务架构偏好的 gRPC 无法使用 |
| 4 | **🟡 事件与 Webhook 可观测仪表板** | 可观测/运维 | **P2** — 事件管线的运维盲区 | `internal/events/webhook.go` `internal/events/webhook_retry_test.go` `internal/repository/webhook_failures.go` | Webhook 失败后仅自动重试，运维人员无法查看送达状态、延迟、失败原因分布、事件链路追踪 |
| 5 | **🔴 备份与灾难恢复即服务框架** | 可靠性/运维 | **P0** — 企业数据保护的最后防线 | `internal/snapshot/snapshot.go` `deploy/helm/` `internal/replication/` | 无自动备份调度、无 PITR 能力、无 DR 演练支持、无备份验证——生产数据的单点故障 |

---

## 方向一：🔴 数据质量与 Schema 验证管线（Data Quality & Schema Validation Pipeline）

### 现状

当前系统接收任意字节流并原样存储。写入时的唯一数据验证是：

```go
// internal/service/file_crud.go:Put
// 1. validateKey(key)           — 禁止空 key、/ 前缀、.. 路径遍历
// 2. validateMetadata(opts)     — metadata 大小和 key/value 长度限制
// 3. Content-MD5 (可选)         — 可选校验和匹配
// 4. preflightQuota             — 配额检查
```

系统对存储的**数据内容**不做任何质量或 Schema 层面的检查。AI 管线的 `Extractor` 在**写入之后**异步处理对象——如果对象是损坏的 CSV、XML 格式错误的日志、或者不符合预期 Schema 的 JSON，问题直到检索/查询时才暴露。

| 能力 | 当前状态 |
|------|---------|
| Upload 时 Schema 验证（JSON Schema / Avro / Parquet Schema） | ❌ 无 |
| 数据 Profile 自动统计（字段非空率/值分布/类型推断/异常值） | ❌ 无 |
| 质量门禁（Quality Gate）在写入时阻断低质量数据 | ❌ 无 |
| Schema Registry 集成（Confluent/Apache Avro/Protobuf Registry） | ❌ 无 |
| 数据时效性检查（Data Freshness / Staleness Detection） | ❌ 无 |
| 字段级 PII/敏感数据检测（超出当前全文 PII 检测） | ⚠️ 仅全文 PII（`ai/pii.go`），无结构化字段级检测 |
| 质量评分报告与报警 | ❌ 无 |

### 代码锚点

| 位置 | 当前能力 | 缺口 |
|------|---------|------|
| `internal/service/file_crud.go:Put` | 写入入口点；`Content-MD5` 可选校验 | 无 Schema 验证、无质量门禁钩子 |
| `internal/ai/extractor.go` | 对象→文本提取（PDF/HTML/CSV/JSON） | 提取后无 Schema 校验；提取失败静默跳过 |
| `internal/ai/pii.go` | 全文 PII 扫描 + Luhn 校验 | 无结构化字段级 PII 检测（如 JSON 字段路径） |
| `internal/repository/sql_objects.go` | 对象元数据 | 无 Schema ID/质量分数/Profile 字段 |
| `internal/events/webhook.go` | 事件通知 | 无法通过 webhook 发送质量告警 |
| `internal/api/rest/router.go` | REST 路由 | 无 `/v1/quality/` 端点 |

### 为什么需要

**1. AI 管线的第一道防线是数据质量，而非模型质量。**

当前系统投入了大量工程在 AI pipeline（提取→分块→嵌入→搜索→Chat→Agent）上，但在数据清洗/验证环节是空白。如果上游写入的数据是残缺的、格式错误的、Schema 不兼容的，下游 AI 的效果将不可预测——**垃圾进，垃圾出**。

| 场景 | 后果 |
|------|------|
| CSV 文件列数不一致 | 分块后语义断裂，检索结果包含不完整的记录 |
| JSON 字段类型变更（`price` 从数字变成字符串） | 下游分析断裂，AI Agent 给出错误答案 |
| 缺少必填字段（如无 `event_timestamp` 的时间序列数据） | 时间查询全部失效 |
| 重复数据（同一记录的多个版本）嵌入后污染检索排序 | 同一信息稀释准确率 |
| 敏感字段未脱敏（JSON 中的 PII 字段） | 合规风险——当前 PII 仅在文本级扫描 |

**2. Schema 治理是企业数据平台的标准能力。**

在数据湖/数据平台领域，Schema Registry + Schema Validation 是标准基础设施：

| 系统 | Schema 能力 |
|------|------------|
| Apache Kafka + Schema Registry | Avro/Protobuf/JSON Schema 注册 + 写入时验证 + 兼容性检查 |
| Delta Lake | Schema 强制 + 演进（`mergeSchema`） |
| Great Expectations | 数据质量期望 + 验证 + 文档 |
| AWS Glue / Deequ | 自动数据 profiing + 质量规则 |

aero-vault 如果不提供类似能力，在 AI + 存储的结合场景中会停留在"对象存储"而非"智能数据平台"的定位。

**3. 当前代码已有部分前置条件。**

- 已有 `internal/ai/extractor.go`（提取结构化数据）
- 已有 `internal/ai/pii.go`（PII 检测可以扩展到字段级）
- 已有 `internal/ai/indexer.go`（异步处理管道可以扩展为质量检测管道）
- 已有 OTel 指标基础设施（质量指标可以无缝接入）

### 架构概要

```
Data Quality Pipeline
======================

┌─ Schema Registry ───────────────────────────────────────────────────┐
│                                                                      │
│  POST /v1/admin/schemas {"name": "order", "version": 1,             │
│                         "schema": "{...json-schema...}"}            │
│  GET  /v1/admin/schemas/order/versions                              │
│  GET  /v1/admin/schemas/order/version/{v}                           │
│  DELETE /v1/admin/schemas/order/version/{v}                         │
│                                                                      │
│  存储: schema_registry 表 + 可选文件存储（sidecar JSON）             │
│  格式: JSON Schema / Avro (.avsc) / Protobuf (.proto)               │
│  兼容性检查: BACKWARD | FORWARD | NONE                              │
│                                                                      │
└──────────────────────────────────────────────────────────────────────┘

┌─ Schema Validation on Upload ───────────────────────────────────────┐
│                                                                      │
│  PUT /v1/files/order_2024.json                                       │
│  Request-Header: X-Aero-Schema-name=order                            │
│                  X-Aero-Schema-version=1                             │
│                                                                      │
│  FileService.Put 路径中新增:                                          │
│                                                                      │
│  1. 检查 X-Aero-Schema-* 头                                         │
│  2. 从 schema_registry 获取最新/指定版本                             │
│  3. 对对象进行 Schema 验证                                           │
│     ├─ JSON → encoding/json + JSON Schema validator                  │
│     ├─ CSV → 列数检查 + 类型推断 + 列名匹配                         │
│     ├─ Avro → avro 解码 + Schema 验证                                │
│     └─ Parquet → 读取 schema + 验证（可选）                          │
│  4. 验证结果:                                                        │
│     ├─ 通过 → 正常存储（元数据中写入 schema_name + schema_version）  │
│     ├─ 失败 → 可配置行为:                                            │
│     │   ├─ reject: 返回 422 Unprocessable Entity + 验证错误详情      │
│     │   ├─ quarantine: 写入隔离区 + 触发质量告警事件                 │
│     │   └─ warn: 写入 + 在响应头返回质量警告                         │
│     └─ skipped: 无 Schema 头时跳过验证                               │
│                                                                      │
└──────────────────────────────────────────────────────────────────────┘

┌─ Data Profiling（异步背景作业）─────────────────────────────────────┐
│                                                                      │
│  复用 internal/jobs 作业队列 + internal/reconcile 定时框架           │
│                                                                      │
│  每个对象/每个批次上传后:                                             │
│  jobReg.Register(JobProfileObject, func(ctx, job) error {           │
│     obj := loadObject(job.ObjectID)                                  │
│     profile := ProfileObject(obj)                                    │
│     └─ 结构化数据:                                                   │
│        ├─ 字段列表 + 类型推断                                        │
│        ├─ 非空率（completeness）                                     │
│        ├─ 唯一值计数（distinct count）                               │
│        ├─ 最小值/最大值/均值（数值字段）                             │
│        ├─ 异常值检测（Z-score > 3 或 IQR 法）                       │
│        ├─ 格式一致性（如日期格式是否统一）                           │
│        └─ 字段级 PII 检测（email/phone/SSN 模式匹配）               │
│     repo.SaveProfile(ctx, obj.ID, profile)                          │
│  })                                                                  │
│                                                                      │
│  Profile 结果可查看:                                                 │
│  GET /v1/admin/quality/objects/{id}/profile                         │
│  GET /v1/admin/quality/tenants/{tenant}/summary                     │
│                                                                      │
└──────────────────────────────────────────────────────────────────────┘

┌─ Quality Gates（质量门禁）───────────────────────────────────────────┐
│                                                                      │
│  POST /v1/admin/quality/gates                                        │
│  {                                                                   │
│    "id": "order_completeness",                                       │
│    "object_filter": "bucket=orders AND content_type=application/json",│
│    "rules": [                                                        │
│      {"field": "order_id",           "non_null_ratio": 1.0},         │
│      {"field": "total_amount",       "non_null_ratio": 1.0},         │
│      {"field": "email",              "pii": "email"},                │
│      {"field": "created_at",         "date_format": "RFC3339"},      │
│      {"row_count_ratio_vs_prev":     0.8}  // 行数 ≥ 上次的 80%    │
│    ],                                                                │
│    "action": "alert|webhook|reject"                                  │
│  }                                                                   │
│                                                                      │
│  质量门禁定期（或每次写入增量）评估，结果:                           │
│  ├─ quality_gate_results 表记录每次评估                              │
│  ├─ OTel 指标: quality_gate_pass{gate} / quality_gate_fail{gate}    │
│  └─ 失败事件通过 EventBus 发送 → Webhook/内部通知                    │
│                                                                      │
└──────────────────────────────────────────────────────────────────────┘

┌─ Data Freshness（数据时效性）────────────────────────────────────────┐
│                                                                      │
│  对于有预期更新周期的数据集（如每日报表、每小时日志）:                 │
│                                                                      │
│  PUT /v1/admin/quality/freshness                                     │
│  {                                                                   │
│    "id": "daily_report",                                             │
│    "prefix": "reports/daily/",                                       │
│    "expected_interval": "24h",                                       │
│    "max_staleness": "30h",                                           │
│    "timeout_action": "alert|webhook"                                 │
│  }                                                                   │
│                                                                      │
│  后台作业每小时检查: last_updated - now > max_staleness → 触发告警   │
│                                                                      │
└──────────────────────────────────────────────────────────────────────┘
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| Schema 版本不兼容（向后兼容性失败） | `compatibility=BACKWARD` 时拒绝写入；`=FORWARD` 时接受但标记为兼容性警告 |
| 对象太大无法 Profile（>1GB） | Profile 时只分析前 10,000 行（可配置 `MAX_PROFILE_ROWS`） |
| 非结构化数据（PDF/图片/视频） | Schema 验证跳过；Profile 降级为文件级元数据统计（大小/格式/分辨率） |
| Schema 在写入后被删除 | 已写入对象使用快照的 Schema 版本（`schemas` 表软删除，保留历史） |
| 重复 Schema 名称但内容不同 | 通过 `compatibility_check` 阻止注册；或使用命名空间（`org_name.schema_name`） |
| 字段级 PII 检测的误报 | 允许在质量门禁中添加 `pii_exception` 白名单路径（如 `user.password` 是哈希不是明文 PII） |
| Profile 失败但写入成功 | Profile 是异步的——Profile 失败不阻塞写入；通过指标和日志告警 |
| 质量门禁导致写入阻塞太多 | 提供 `X-Aero-Skip-Validation: true` 头绕过（审计记录绕过事件） |

### 代码影响范围

| 模块 | 影响 |
|------|------|
| **新增** `internal/quality/registry.go` | Schema Registry CRUD + 兼容性检查 |
| **新增** `internal/quality/validator.go` | JSON Schema / CSV / Avro 验证器 |
| **新增** `internal/quality/profiler.go` | 数据 Profile（字段统计/类型推断/异常值） |
| **新增** `internal/quality/gates.go` | 质量门禁定义 + 评估引擎 |
| **新增** `internal/quality/freshness.go` | 数据时效性监控 |
| **新增** `internal/quality/pii_field.go` | 字段级 PII 检测（JSONPath 驱动） |
| **新增** `internal/repository/sql_quality.go` | `schema_registry` / `object_profiles` / `quality_gates` / `quality_results` 表 CRUD |
| **新增** `internal/api/rest/admin_quality.go` | REST API：Schema 注册、Profile 查看、质量门禁管理 |
| **新增** 迁移 `0031_schema_registry.up.sql` | `schema_registry` / `object_profiles` / `quality_gates` / `quality_results` / `freshness_rules` 表 |
| **修改** `internal/service/file_crud.go:Put` | 新增 `schemaHook` 调用点（写入时验证 + 质量门禁检查） |
| **修改** `internal/service/file.go` | `PutOptions` 新增 `SchemaName`/`SchemaVersion` 字段 |
| **修改** `internal/api/rest/handler.go` | 解析 `X-Aero-Schema-*` 请求头 |
| **修改** `internal/ai/indexer.go` | AI 索引前触发质量检查（可配置 `AI_QUALITY_GATE`） |
| **新增** `internal/quality/registry_test.go` | Schema 注册 + 兼容性检查测试 |
| **新增** `internal/quality/validator_test.go` | 多种格式 Schema 验证测试 |
| **新增** SDK 方法 | `RegisterSchema`、`GetSchema`、`GetObjectProfile`、`CreateQualityGate` |

---

## 方向二：🟠 批量导入/导出与数据迁移框架（Bulk Import/Export & Migration Framework）

### 现状

当前系统对数据进出平台的支持非常薄弱：

```bash
# CLI 支持（internal/cli/）
aero-vault cli upload <file> <key>   # 单文件上传
aero-vault cli get    <key>          # 单文件下载
aero-vault cli ls     [prefix]       # 列表

# SDK 支持
for _, key := range keys {
    obj, _ := client.Get(ctx, key)   // 遍历式——没有批量操作
}
```

| 能力 | 当前状态 |
|------|---------|
| 批量导入（目录递归 + 并发 + 校验） | ❌ 无——用户需要自己写脚本遍历 + wget |
| 批量导出（按前缀/标签/版本） | ❌ 无——逐个 `GET` 下载 |
| 跨平台迁移（从 S3/MinIO/GCS/NAS 迁入） | ❌ 无——没有 `sync` 或 `import` 命令 |
| 增量同步（类似 rsync/inotify） | ❌ 无——没有变更跟踪能力 |
| 迁移验证（校验和对比 + 完整性报告） | ❌ 无——迁移后无法自动验证是否完整 |
| 大对象批量处理（>5GB 的分片传输与校验） | ❌ 无——没有断点续传或分片并行传输 |
| S3 Batch Operation API 集成 | ✅ 仅批删除（`internal/api/s3compat/extra.go`） |

### 代码锚点

| 位置 | 当前能力 | 缺口 |
|------|---------|------|
| `internal/cli/cli_crud.go` | 单文件 upload/get/ls/rm | 无批量操作、无进度显示、无重试 |
| `internal/cli/cli_snapshot.go` | SQLite 快照导出/导入 | 仅 metadata，不含对象内容 |
| `internal/snapshot/snapshot.go` | SQLite 元数据快照 | 无对象内容批量传输 |
| `internal/api/s3compat/extra.go` | S3 Batch Delete | 无 Batch Copy / Batch Restore / Batch Tag |
| `internal/replication/replication.go` | 跨后端复制（事件驱动） | 单向、一对一的被动复制，非按需批量迁移 |
| `internal/service/file_crud.go` | 单对象 Get/Put | 无 Range 批量并行下载/上传能力 |

### 为什么需要

**1. 用户迁移的"最后一公里"障碍。**

用户评估 aero-vault 时最常问的问题是："我怎么把我现在 S3/MinIO/本地 NAS 上的 10TB 数据迁进来？" 当前答案是：写脚本遍历 + $5 的 EC2 带宽——这是**平台采用的硬性障碍**。

| 竞品 | 迁移工具 |
|------|---------|
| AWS S3 | `aws s3 sync` + S3 Batch Operation + AWS DataSync + Snowball |
| MinIO | `mc mirror` + `mc cp --recursive` + 分布式批量 |
| GCS | `gsutil rsync` + `gcloud storage transfer` + Storage Transfer Service |
| Azure Blob | `azcopy` + Azure Data Factory |

aero-vault 没有迁移工具，意味着用户需要在**功能评估之后再投入额外工程成本做数据迁移**——这是大量潜在用户被过滤掉的根本原因。

**2. 批量操作是企业数据生命周期的核心需求。**

数据管理的典型场景：

| 场景 | 当前能力 | 需要的能力 |
|------|---------|---------|
| 将每日日志从生产环境移到归档存储 | 逐个对象操作 | 按前缀批量 Transition |
| 给所有图片添加标签 "project=foo" | 逐个对象 Tag | 按过滤条件批量 Tag |
| 将数据从旧 bucket 迁移到新 bucket | 逐个对象 Copy | 批量 Copy + 校验 |
| 在 Chat/Agent 中使用前将公共数据集导入 | 手动逐个上传 | 批量导入 + Schema 验证 |

**3. 当前架构已具备复用基础。**

- 已有 `internal/jobs/jobs.go` 作业队列（可以驱动批量作业）
- 已有 `internal/replication/replication.go` 跨后端复制（可以扩展为迁移引擎）
- 已有 `internal/repository/sql_objects.go` 对象查询（可以按条件批量筛选）
- 已有存储层 `storage.Storage` 接口（读写无关存储后端类型）

### 架构概要

```
Bulk Import/Export & Migration Framework
==========================================

┌─ CLI Import（命令行批量导入）────────────────────────────────────────┐
│                                                                       │
│  aero-vault import /data/dir s3://bucket/prefix \                    │
│    --concurrency 16                                                   │
│    --checksum sha256                                                  │
│    --dry-run                                                          │
│    --exclude "*.tmp"                                                  │
│    --include "*.{csv,json,parquet}"                                   │
│    --rename 's/raw-/processed-/'                                      │
│    --report import-report.json                                        │
│                                                                       │
│  工作流:                                                              │
│  1. 扫描源目录/存储（递归）+ 构建文件清单                            │
│  2. 匹配 include/exclude 过滤规则                                     │
│  3. 可选 dry-run 模式（输出将要上传的文件列表 + 总大小 + 预估时间）  │
│  4. 并发上传（goroutine pool, 可配 concurrency）                      │
│     ├─ 小文件（<16MB）：单次 Put                                      │
│     ├─ 中文件（16MB~5GB）：分片并行 multipart upload                   │
│     └─ 大文件（>5GB）：分片 + 断点续传（记录 Checkpoint 文件）       │
│  5. 上传后校验：checksum_match(sha256(local) == ETag(x-amz-checksum))│
│  6. 生成导入报告：成功数/失败数/跳过数/校验失败数/总耗时/吞吐量      │
│                                                                       │
└──────────────────────────────────────────────────────────────────────┘

┌─ CLI Export（命令行批量导出）────────────────────────────────────────┐
│                                                                       │
│  aero-vault export s3://bucket/prefix /output/dir \                  │
│    --concurrency 16                                                   │
│    --filter "size > 1048576 AND tags.project=foo"                    │
│    --versions all                                                     │
│    --checksum-verify                                                  │
│    --progress-bar                                                     │
│    --resume export-checkpoint.json                                    │
│                                                                       │
│  工作流:                                                              │
│  1. 按前缀/过滤条件查询对象列表                                       │
│  2. 生成导出清单（可选按 date/size/key 排序）                         │
│  3. 并发下载（goroutine pool）                                        │
│  4. 可选校验下载文件的 checksum                                       │
│  5. 支持断点续传（Checkpoint 文件记录已完成文件列表）                 │
│  6. 生成导出报告                                                      │
│                                                                       │
└──────────────────────────────────────────────────────────────────────┘

┌─ Cross-Platform Sync（跨平台同步迁移）────────────────────────────────┐
│                                                                       │
│  aero-vault sync s3://source-bucket/prefix s3://dest-bucket/ \       │
│    --source-endpoint https://s3.amazonaws.com \                      │
│    --source-region us-east-1                                          │
│    --source-access-key AKIA...                                        │
│    --source-secret-key ...                                            │
│                                                                       │
│  aero-vault sync /local/nas/data s3://my-bucket/archive              │
│                                                                       │
│  工作流:                                                              │
│  1. 列出源端所有对象（递归）                                           │
│  2. 列出目的端所有对象                                                 │
│  3. 计算差异（新增 + 修改 + 删除）                                     │
│     ├─ ETag/LastModified 比较（快速跳过相同对象）                     │
│     └─ 可选全量 checksum 比较（慢但更安全）                           │
│  4. 执行同步:                                                         │
│     ├─ 新增 → 并行上传                                                │
│     ├─ 修改 → 覆盖上传                                                │
│     └─ 删除 → 可选同步删除（`--delete` 标志控制）                     │
│  5. 可选增量模式（`--watch`）：监听源端变更事件 → 实时同步            │
│                                                                       │
└──────────────────────────────────────────────────────────────────────┘

┌─ REST Batch Operation API ───────────────────────────────────────────┐
│                                                                       │
│  POST /v1/batch/copy                                                  │
│  {                                                                    │
│    "source": {"bucket": "src", "prefix": "data/"},                    │
│    "destination": {"bucket": "dst"},                                  │
│    "filters": {"modified_after": "2026-01-01T00:00:00Z"},             │
│    "options": {"storage_class": "STANDARD_IA", "metadata_directive":  │
│                "COPY"}                                                │
│  } → { "job_id": "batch-copy-abc123", "estimated_objects": 15342 }   │
│                                                                       │
│  GET /v1/jobs/{id} → { status, progress, total, failed, report_url } │
│                                                                       │
│  POST /v1/batch/tag                                                   │
│  { "bucket": "my-bucket", "prefix": "images/",                       │
│    "tags": {"project": "foo", "processed": "true"} }                  │
│                                                                       │
│  POST /v1/batch/restore                                               │
│  { "prefix": "archived-2025/", "days": 7 }                           │
│                                                                       │
└──────────────────────────────────────────────────────────────────────┘

┌─ Backend: 作业驱动 ──────────────────────────────────────────────────┐
│                                                                       │
│  复用 internal/jobs 作业队列:                                         │
│                                                                       │
│  JobBatchCopy = "batch_copy"                                          │
│  payload: { batch_job_id, source_key, dest_key, options... }          │
│                                                                       │
│  JobPool 启动 N 个 worker 并行消费批处理作业                          │
│  每个 worker 完成一个子任务后更新 batch_progress 计数器               │
│  所有子任务完成后 → 标记 batch job 为 completed + 发送通知事件        │
│  子任务失败 → 重试（最多 3 次）→ 记录到 batch_failures 表            │
│                                                                       │
└──────────────────────────────────────────────────────────────────────┘
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **大文件导入中断** | checkpoint 文件记录已完成的 part ID；`--resume` 跳过已完成部分，重启失败的分片 |
| **源端文件在同步过程中被修改** | 读取 ETag + LastModified 做快照；冲突时记录到 `sync_conflicts` 表静默处理 |
| **目标端存储配额不足** | 导入前估算总大小，若超过配额则拒绝；分批导入时单批检查 |
| **跨存储后端迁移（Local → S3 → OSS）** | Sync 工具通过 `storage.Storage` 接口统一读写——无需关心后端差异 |
| **百万级对象的批量操作** | 分页查询 + 并发 worker pool；单个 batch job 拆分为最多 10,000 条子任务的子批次（参照 S3 Batch Operation 的推荐上限） |
| **迁移中网络中断** | 工具侧实现指数退避重试 + checkpoint 持久化；断网后 resume 可继续 |
| **编码/文件名冲突** | 源端 S3 允许 UTF-8 key 包含特殊字符；上传时按 URL 规范编码；冲突记录不走批量而是降级为逐个处理 |
| **导入后元数据不完整** | 迁移工具记录源端元数据（Content-Type、Tags、Metadata）并在 Put 时传递；不支持的自定义元数据记入迁移报告 |

### 代码影响范围

| 模块 | 影响 |
|------|------|
| **新增** `internal/batch/manager.go` | Batch Job 管理器：创建/进度跟踪/取消/报告生成 |
| **新增** `internal/batch/copy.go` | 批量 Copy 作业执行 |
| **新增** `internal/batch/tag.go` | 批量 Tag 作业执行 |
| **新增** `internal/batch/restore.go` | 批量 Restore 作业执行 |
| **新增** `internal/migration/importer.go` | 批量导入引擎（目录扫描 + 文件清单 + 并发上传 + 校验） |
| **新增** `internal/migration/exporter.go` | 批量导出引擎（对象列表 + 并发下载 + 断点续传） |
| **新增** `internal/migration/syncer.go` | 跨平台同步引擎（差异计算 + 双向同步 + 增量模式） |
| **新增** `internal/migration/checkpoint.go` | 断点续传 Checkpoint 文件管理 |
| **新增** `internal/api/rest/batch.go` | REST API：`POST /v1/batch/copy|tag|restore`、`GET /v1/batch/{id}` |
| **新增** `internal/repository/sql_batch.go` | `batch_jobs` / `batch_items` / `batch_failures` 表 CRUD |
| **新增** 迁移 `0032_batch_jobs.up.sql` | `batch_jobs` / `batch_items` / `batch_progress` 表 |
| **修改** `internal/cli/cli.go` | 新增 `import`、`export`、`sync` 子命令 |
| **新增** SDK 方法 | `CreateBatchCopy`、`CreateBatchTag`、`GetBatchJob`、`ImportDirectory`、`ExportObjects` |

---

## 方向三：🟠 GraphQL / gRPC 协议网关（GraphQL / gRPC Protocol Gateway）

### 现状

aero-vault 当前支持 4 种接入协议：

| 协议 | 包 | 适用场景 | 限制 |
|------|-----|---------|------|
| REST (`/v1`) | `internal/api/rest` | 通用 HTTP API | 过取/欠取问题（Over-fetching / Under-fetching） |
| S3 兼容 (`/s3`) | `internal/api/s3compat` | S3 生态工具 | 仅文件操作，无 AI/RAG/Admin 接口 |
| WebDAV | `internal/api/webdav` | 文件系统挂载 | 性能差、无 AI 能力 |
| MCP (`/mcp`) | `internal/mcp` | AI Agent 集成 | 仅 6 个工具，无复杂查询能力 |

**缺少对现代 API 协议的支持：**

GraphQL 和 gRPC 是当前 Web 和微服务生态的两个主流协议，而 aero-vault 完全没有覆盖。

| 能力 | 当前状态 | Web 应用 | 移动应用 | 微服务 |
|------|---------|---------|---------|--------|
| GraphQL（前端优先） | ❌ 无 | 🔴 前端需要手写 REST 调用、组合多个端点、处理分页 | 🔴 移动端带宽敏感，REST 过取问题突出 | — |
| gRPC（服务间优先） | ❌ 无 | — | — | 🔴 服务间调用需要 HTTP/JSON 转换，无 Protobuf Schema 代码生成 |

### 代码锚点

| 位置 | 当前能力 | 缺口 |
|------|---------|------|
| `internal/api/rest/` | REST handler（JSON over HTTP） | 无 GraphQL endpoint、无 Schema 自省 |
| `internal/api/rest/dto.go` | DTO 定义（JSON struct tags） | 无 GraphQL Schema 类型生成 |
| `go.mod` | 无 `graphql-go`、`gqlgen`、`grpc` 依赖 | — |
| `internal/service/file.go` | `FileService` 入口点 | gRPC proto 定义缺失 |
| `internal/mcp/protocol.go` | JSON-RPC 2.0 实现 | 仅 6 个方法；无 GraphQL 或 gRPC 等价物 |

### 为什么需要

**1. 前端和移动端开发者的首选协议是 GraphQL，不是 REST。**

当前 Web UI（`internal/webui/web.go`）使用 vanilla JS 直接调用 REST API。如果系统想更好地服务：

- **React/Vue 前端应用**：GraphQL 允许前端声明式获取所需字段，避免 REST 的 over-fetching
- **移动客户端**：带宽敏感场景下单次请求可获取多层级数据（如对象 + 标签 + ACL + 血缘关系）
- **低代码平台（Retool / Appsmith / Budibase）**：原生支持 GraphQL 数据源

GraphQL 的订阅（Subscription）能力尤其适合 SSE 流式 Chat、事件通知、实时血缘更新等场景——比 REST SSE 更标准化的推送机制。

**2. gRPC 是微服务间通信的事实标准。**

在 K8s 生态中，gRPC 的普及度已超过 REST：

| 场景 | REST 的痛点 | gRPC 的优势 |
|------|------------|------------|
| 服务间文件操作 | JSON 序列化/反序列化开销 | Protobuf 二进制编码，零序列化成本 |
| 流式传输（大文件上传/下载） | 需要 SSE 或 WebSocket 变通 | gRPC streaming (client/server/bidirectional) |
| 强类型契约 | OpenAPI spec → 代码生成（有损） | `.proto` → 原生类型安全的代码生成 |
| 多语言兼容 | 每个语言需要手写 HTTP client | `.proto` 自动生成所有语言客户端 |

**3. 当前架构已经为协议扩展做好了准备。**

`FileService` 已经是**协议无关**的服务层：

```go
// 任意协议 → 同一 FileService
rest.Put(ctx, ...)     → svc.Put(ctx, tenant, bucket, key, body, size, opts)
s3compat.PutObject(...) → svc.Put(ctx, tenant, bucket, key, body, size, opts)
mcp.callTool(...)       → svc.Put(ctx, tenant, bucket, key, body, size, opts)
```

添加 GraphQL / gRPC 不需要修改 `FileService`——只需要新协议适配器。

### 架构概要

```
GraphQL / gRPC Gateway
========================

┌─ GraphQL Endpoint ───────────────────────────────────────────────────┐
│                                                                       │
│  POST /graphql                                                       │
│  query {                                                             │
│    object(bucket: "default", key: "report-2024.q4.csv") {            │
│      key, size, contentType, etag, storageClass,                     │
│      metadata { key, value }                                         │
│      tags { key, value }                                             │
│      acl                                                            │
│      versions { versionId, size, updatedAt }                        │
│      lineage { sourceType, sourceDetail, createdAt }                │
│    }                                                                 │
│    search(query: "quarterly report", mode: HYBRID, k: 5) {          │
│      score, text, source { key, bucket }                            │
│    }                                                                 │
│  }                                                                   │
│                                                                       │
│  GraphQL Schema 定义类型:                                             │
│  type Object {                                                       │
│    id: ID!                                                           │
│    key: String!                                                      │
│    bucket: String!                                                   │
│    size: Int!                                                        │
│    contentType: String                                               │
│    etag: String                                                      │
│    storageClass: String                                              │
│    metadata: [MetadataPair!]                                         │
│    tags: [TagPair!]                                                  │
│    acl: String                                                       │
│    versions: [ObjectVersion!]                                        │
│    lineage: [LineageEntry!]                                          │
│  }                                                                   │
│                                                                       │
│  Subscriptions:                                                      │
│  subscription {                                                      │
│    objectEvents(bucket: "default") {                                 │
│      type, key, timestamp                                           │
│    }                                                                 │
│    chatStream(tenant: "acme", query: "...") {                       │
│      token, citations                                               │
│    }                                                                 │
│  }                                                                   │
│                                                                       │
│  推荐库: gqlgen (99designs/gqlgen) — schema-first, code-gen         │
│                                                                       │
└──────────────────────────────────────────────────────────────────────┘

┌─ gRPC Endpoint ──────────────────────────────────────────────────────┐
│                                                                       │
│  proto/aerovault/v1/service.proto                                     │
│  service AeroVault {                                                  │
│    // 文件 CRUD                                                       │
│    rpc GetObject(GetObjectRequest) returns (GetObjectResponse);       │
│    rpc PutObject(PutObjectRequest) returns (PutObjectResponse);       │
│    rpc DeleteObject(DeleteObjectRequest) returns (DeleteObjectResponse);│
│    rpc ListObjects(ListObjectsRequest) returns (ListObjectsResponse);  │
│    rpc BatchDelete(BatchDeleteRequest) returns (BatchDeleteResponse);  │
│                                                                       │
│    // AI/RAG                                                          │
│    rpc Search(SearchRequest) returns (SearchResponse);               │
│    rpc Chat(ChatRequest) returns (stream ChatResponse);  // SSE-like │
│    rpc Agent(AgentRequest) returns (AgentResponse);                  │
│                                                                       │
│    // Admin                                                           │
│    rpc CreateTenant(CreateTenantRequest) returns (Tenant);           │
│    rpc AddAPIKey(AddAPIKeyRequest) returns (APIKey);                 │
│    rpc ListAudit(ListAuditRequest) returns (ListAuditResponse);      │
│                                                                       │
│    // Batch Operations                                                │
│    rpc BatchCopy(BatchCopyRequest) returns (BatchJob);               │
│    rpc BatchTag(BatchTagRequest) returns (BatchJob);                 │
│                                                                       │
│    // Data Streams (大文件)                                           │
│    rpc UploadFile(stream FileChunk) returns (UploadResponse);        │
│    rpc DownloadFile(DownloadRequest) returns (stream FileChunk);     │
│  }                                                                   │
│                                                                       │
│  gRPC 网关（grpc-gateway）同时提供 REST ↔ gRPC 双向桥接:             │
│  - REST 请求 → grpc-gateway → gRPC 服务                              │
│  - OpenAPI 规范从 proto 自动生成                                      │
│  - 单后端服务双协议暴露                                               │
│                                                                       │
└──────────────────────────────────────────────────────────────────────┘

┌─ Protocol Coexistence ───────────────────────────────────────────────┐
│                                                                       │
│  同一端口多协议（通过主机头或路径前缀分发）:                           │
│                                                                       │
│  /v1/*      → REST (现有)                                             │
│  /s3/*      → S3 Compat (现有)                                       │
│  /mcp       → MCP (现有)                                              │
│  /graphql   → GraphQL (新增)                                          │
│  grpc://:9000 → gRPC (独立端口，或通过 grpc-web 代理)                 │
│                                                                       │
│  所有协议共享同一个:                                                    │
│  - FileService 实例（业务逻辑 100% 复用）                              │
│  - Auth Registry（鉴权逻辑）                                          │
│  - RateLimiter（限流）                                                │
│  - Tenant Resolution（租户提取）                                      │
│  - OTel Instrumentation（指标采集）                                   │
│                                                                       │
└──────────────────────────────────────────────────────────────────────┘
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **GraphQL N+1 查询** | 使用 DataLoader（`gqlgen` 内置）批量合并 DB / Service 调用 |
| **GraphQL 深度嵌套限制** | 设置 `MaxQueryDepth: 10` 防止恶意递归查询耗尽服务端资源 |
| **gRPC 大文件流式传输** | 分块大小 4MB（可配）；使用 streaming RPC + checksum 帧验证完整性 |
| **gRPC 连接管理** | Keepalive + max connection age（防止长连接累积）|
| **认证兼容性** | gRPC 使用 metadata header 传递 `X-Aero-Tenant` 和 `Authorization: Bearer`；GraphQL 通过 HTTP header 传递（与 REST 一致） |
| **GraphQL Subscription 后端压力** | 每个 subscription 占用一个 goroutine；可配置 `MAX_SUBSCRIPTIONS_PER_CLIENT`；空闲超时自动断开 |
| **跨协议一致性** | `PUT` 对象后 REST 和 gRPC 立即都能读取（共享 Service 层）；GraphQL 无额外延迟 |
| **Proto 版本迁移** | gRPC proto 用向后兼容的字段编号（1-15 保留给频繁字段）；使用 `protov1` 向后兼容包装 |

### 代码影响范围

| 模块 | 影响 |
|------|------|
| **新增** `proto/aerovault/v1/service.proto` | gRPC 服务定义（所有 CRUD + AI + Admin + Batch 操作） |
| **新增** `internal/api/grpc/server.go` | gRPC 服务器 + 实现 `AeroVaultServer` 接口 |
| **新增** `internal/api/grpc/interceptors.go` | Auth / Tenant / RateLimit / OTel gRPC 拦截器 |
| **新增** `internal/api/graphql/schema.graphql` | GraphQL Schema 定义 |
| **新增** `internal/api/graphql/resolver.go` | GraphQL Resolver（对接 FileService + Search + Chat） |
| **新增** `internal/api/graphql/dataloader.go` | DataLoader（N+1 查询合并） |
| **新增** `internal/api/graphql/subscription.go` | GraphQL Subscription（SSE 替代方案） |
| **修改** `cmd/server/main.go` | 新增 gRPC server goroutine + GraphQL 路由注册 |
| **修改** `internal/config/config_app.go` | `GRPC_ENABLED` / `GRPC_ADDR` / `GRAPHQL_ENABLED` |
| **修改** `go.mod` | `gqlgen` + `grpc` + `protobuf` 依赖 |
| **新增** 代码生成 | `make proto-gen`（proto → Go）、`make gql-gen`（schema → resolver stub）|

---

## 方向四：🟡 事件与 Webhook 可观测仪表板（Event & Webhook Observability Dashboard）

### 现状

当前事件/Webhook 系统的可靠性机制：

```go
// internal/events/webhook.go
// 成功：log + 继续
// 失败：写入 webhook_failures 表 + 自动指数退避重试

// internal/events/bus.go
// 本地订阅者 buffered channel（深度 64）
// 缓冲区满 → event dropped（telemetry.IncEventDropped）
// 但代码：Dropped() → int64 计数器（无历史，无明细）
```

运维人员对事件管线的可见性：

| 可观测维度 | 当前状态 |
|-----------|---------|
| Webhook 送达率（Delivery Rate） | ❌ 无——不知道成功/失败的比例 |
| Webhook 延迟分布（P50/P95/P99） | ❌ 无——不知道从事件产生到送达的时间 |
| Webhook 失败原因分布 | ❌ 无——无区分超时/网络/4xx/5xx |
| Webhook 重试状态可视化 | ❌ 无——不知道重试队列深度、重试间隔 |
| 事件流量时间序列 | ❌ 无——不知道事件产生速率、峰值 |
| 事件链路追踪（Event Tracing） | ❌ 无——无法追踪单个事件从 A 到 B 的全路径 |
| Failed Events 管理 UI | ❌ 无——`webhook_failures` 表有数据但无前端展示 |
| 手动事件重放 | ❌ 无——失败事件只能等自动重试，不能手动触发重放 |

### 代码锚点

| 位置 | 当前能力 | 缺口 |
|------|---------|------|
| `internal/events/webhook.go` | Webhook delivery + retry loop | 无 delivery 计量、无 latency histogram、无失败原因分类 |
| `internal/events/bus.go` | `Dropped()` atomic counter | 无 dropped 事件的上下文（类型/时间/来源） |
| `internal/repository/webhook_failures.go` | `InsertWebhookFailure` / `NextRetryDue` / `MarkSucceeded` | 无查询维度（失败原因/时间范围/目标 URL 聚合） |
| `internal/events/webhook_retry_test.go` | 重试逻辑测试 | 无重试可视化、无人工干预接口 |
| `internal/api/rest/admin_jobs.go` | `ListJobs` / `RetryJob` | 仅有 Job 的管理，无 Webhook Failure 管理 |
| `internal/telemetry/metrics.go` | 领域指标 | 无 `webhook_delivery_total{status}` / `webhook_latency_seconds` / `event_throughput` |

### 为什么需要

**1. Webhook 是平台与外部系统集成的核心——不可观测意味着不可信任。**

当客户说"我集成 aero-vault 后，有些文件变更没有收到通知"，当前的回答能力是"我查一下日志"——而日志可能在一小时前就被滚动了。

webhook 失败的表中有记录，但运维人员需要直接查数据库。企业级平台必须提供无代码/低代码的事件运维能力。

**2. 事件驱动架构越来越核心，但可观测工具严重滞后。**

aero-vault 的事件系统支撑着：

- 索引触发（`object.created` → Indexer）
- 复制触发（`object.created` → Replication Worker）
- 杀毒扫描（`object.created` → Antivirus Worker）
- Webhook（全量事件 → 外部系统）

当这一链条中的某个环节出问题时，缺乏事件追踪能力将使得排查极其困难。

**3. 对标业界标准。**

| 系统 | 事件可观测能力 |
|------|--------------|
| AWS EventBridge | Schema Registry + 事件追踪 + 重放 + 规则指标 |
| Stripe Webhooks | Dashboard（送达/失败/重试/延迟）+ Logs + Webhook Endpoint 健康 |
| GitHub Webhooks | 最近送达历史 + 重试按钮 + 响应码 + 延迟 |
| Slack Events | Event Subscriptions 日志 + 重试 + 失败原因 |

### 架构概要

```
Event & Webhook Observability Dashboard
=========================================

┌─ Webhook Delivery Metrics ───────────────────────────────────────────┐
│                                                                       │
│  OTel Counters（internal/telemetry/metrics.go 扩展）:                  │
│                                                                       │
│  webhook_delivery_total{                                             │
│    url_hash="sha256(webhook_url)[:8]",                               │
│    status="success|failed_http_4xx|failed_http_5xx|failed_timeout|   │
│             failed_network|failed_dns",                              │
│    tenant="acme"                                                      │
│  }                                                                    │
│                                                                       │
│  webhook_delivery_duration_seconds{url_hash, tenant} — histogram      │
│  webhook_retry_queue_depth{url_hash} — gauge (pending + due for retry)│
│  webhook_failures_total{url_hash, reason} — counter                  │
│                                                                       │
│  event_bus_dropped_total{reason, event_type} — counter               │
│  event_bus_publish_rate{event_type} — rate counter                   │
│  event_bus_lag_subscriber{subscriber} — gauge (channel fill level)   │
│                                                                       │
└──────────────────────────────────────────────────────────────────────┘

┌─ Webhook Endpoint Management Console ─────────────────────────────────┐
│                                                                       │
│  GET  /v1/admin/webhooks/endpoints                                   │
│  → 注册的所有 Webhook URL + 当前状态（active / failing / paused）    │
│                                                                       │
│  GET  /v1/admin/webhooks/endpoints/{url_hash}                        │
│  → 详情：配置（secret/retry policy）、24h 统计（请求数/成功率/延迟   │
│    P50/P95/P99/重试队列深度/最后送达时间/最后失败时间与原因）         │
│                                                                       │
│  POST /v1/admin/webhooks/endpoints/{url_hash}/pause                  │
│  → 暂停向该 endpoint 发送（已入队的保持等待）                         │
│  POST /v1/admin/webhooks/endpoints/{url_hash}/resume                 │
│  → 恢复发送                                                            │
│  POST /v1/admin/webhooks/endpoints/{url_hash}/test                   │
│  → 发送测试事件（`event.test` 类型）检查连通性                         │
│                                                                       │
└──────────────────────────────────────────────────────────────────────┘

┌─ Webhook Failure Management ──────────────────────────────────────────┐
│                                                                       │
│  GET /v1/admin/webhooks/failures?                                     │
│    endpoint={url_hash}&since={time}&status={pending|retrying|failed}  │
│  → 失败事件列表（每行: 事件ID/类型/Key/时间/失败原因/重试次数/       │
│    下次重试时间/HTTP 响应码）                                         │
│                                                                       │
│  POST /v1/admin/webhooks/failures/{id}/retry                         │
│  → 立即重试单条失败事件（跳过等待退避）                               │
│                                                                       │
│  POST /v1/admin/webhooks/failures/bulk-retry                         │
│  → 按条件批量重试（如：最近 1 小时、endpoint="xxx"、                   │
│     status="failed"、retry_count < 5）                                │
│                                                                       │
│  POST /v1/admin/webhooks/failures/{id}/skip                          │
│  → 永久跳过（标记为 "skipped"，不再重试）                              │
│                                                                       │
└──────────────────────────────────────────────────────────────────────┘

┌─ Event Trace（事件链路追踪）──────────────────────────────────────────┐
│                                                                       │
│  每个事件携带 TraceParent（OpenTelemetry Trace Context）:              │
│                                                                       │
│  Event {                                                              │
│    ID: 12345                                                          │
│    Type: "object.created"                                             │
│    TraceID: "abc123..."                                              │
│    SpanID: "def456..."                                               │
│    Key: "file-001"                                                    │
│    ...                                                                │
│  }                                                                    │
│                                                                       │
│  GET /v1/admin/events/trace?trace_id=abc123...                       │
│  → 返回事件经过的完整链路:                                            │
│     Bus → transport → webhook(attempt:1) → webhook(attempt:2) →      │
│     → failure → retry_queue → webhook(attempt:3) → success           │
│                                                                       │
│  链路图展示:                                                          │
│  ● Event Published (2026-07-10T10:00:00.000Z)                        │
│  ├── ● Webhook Delivery #1 (10:00:00.050Z) → 503 Service Unavailable │
│  ├── ◐ Retry Queue (backoff 30s)                                     │
│  ├── ● Webhook Delivery #2 (10:00:30.100Z) → 200 OK                  │
│  └── ✅ Delivered (10:00:30.101Z, total 30.1s)                       │
│                                                                       │
└──────────────────────────────────────────────────────────────────────┘

┌─ Grafana Dashboard（扩展现有仪表板）───────────────────────────────────┐
│                                                                       │
│  在现有 `deploy/grafana/aero-vault-ai-ops-dashboard.json` 基础上       │
│  新增 Webhook / Event 面板:                                           │
│                                                                       │
│  面板 13: Webhook Delivery Rate（24h 时间序列，按 endpoint 着色）     │
│  面板 14: Webhook Latency P50/P95/P99（柱状图）                      │
│  面板 15: Webhook Failure Reasons（饼图：timeout/4xx/5xx/network）   │
│  面板 16: Event Throughput（事件类型 × 速率， stacked area）          │
│  面板 17: Retry Queue Depth（gauge）                                  │
│  面板 18: Event Bus Backpressure（dropped rate per subscriber）       │
│                                                                       │
└──────────────────────────────────────────────────────────────────────┘
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **Webhook endpoint 长时间不可用（>24h）** | 超过 `MAX_RETRY_HOURS`（默认 72h）后自动标记为 dead → 发送事件 `webhook.endpoint_dead` → 从自动重试队列移除 |
| **手动注入大量重试请求** | 批量重试受全局 rate limit 保护；使用 `X-Retry-Scope: batch` 的独立限流 bucket |
| **事件链路大跨度时间** | 追踪信息持久化 7 天（`event_traces` 表 TTL）+ 自动清理；长链路通过 TraceID 关联多个 DB 表 |
| **Webhook 端点被暂停后产生的事件** | 暂停期间事件写入 `webhook_failures` 表（状态=`paused`），不丢弃；恢复后自动消费 |
| **高吞吐事件频发时指标采集开销** | Webhook 指标使用 rate-limited 的采样（每 N 个事件记录一次，N=10）可配置；OTel 指标本身已经低开销 |
| **控制台请求压力** | `/v1/admin/webhooks/` 端点受 admin scope + rate limit + 独立的缓存（TTL 30s）控制，不会因仪表板刷新压垮事件系统 |
| **Trace 信息泄露** | Event Trace 只在 admin scope 下可见；TraceID 不允许普通用户穿透查看 |

### 代码影响范围

| 模块 | 影响 |
|------|------|
| **新增** `internal/events/metrics.go` | Webhook 发送时的 OTel metric 记录（delivery_total/duration_seconds/failures_total） |
| **修改** `internal/events/webhook.go` | 在 `send` 和 `Run` 循环中插入指标记录点；添加 TraceID 传播 |
| **修改** `internal/events/bus.go` | 添加 `PublishWithTrace` 方法（携带 TraceID）；`Dropped()` 增加 event_type 分布 |
| **新增** `internal/events/endpoint.go` | Webhook Endpoint 管理（注册/状态/暂停/恢复/测试） |
| **新增** `internal/repository/sql_webhook_endpoints.go` | `webhook_endpoints` 表 CRUD（url/secret/状态/统计缓存） |
| **新增** 迁移 `0033_webhook_endpoints.up.sql` | `webhook_endpoints` 表 |
| **新增** `internal/api/rest/admin_webhooks.go` | REST API：Endpoint 管理 + Failure 查询/重试/跳过 |
| **新增** `internal/api/webui/event_console.html` | 事件控制台页面（Web UI 新 tab） |
| **修改** `deploy/grafana/aero-vault-ai-ops-dashboard.json` | 新增 6 个 Webhook/Event 面板 |
| **修改** `deploy/prometheus/alerts.yml` | 新增告警规则：`WebhookDeliveryFailureRateHigh`（>10% for 15m）、`WebhookRetryQueueHigh`（>1000） |

---

## 方向五：🔴 备份与灾难恢复即服务框架（Backup & DRaaS Framework）

### 现状

当前系统的数据保护能力：

```go
// internal/snapshot/snapshot.go
// Snapshots the SQLite metadata to a JSON file.
// Run: aero-vault cli snapshot <output.json>
// Restore: aero-vault cli snapshot --restore <input.json>
// Limitations:
// - SQLite only
// - Metadata only (no object content)
// - Manual invocation required

// internal/replication/replication.go
// Event-driven replication to one secondary storage backend.
// - One-way, one-target only
// - No automated failover
// - No DR promotion capability
```

| DR 能力 | 当前状态 |
|---------|---------|
| 自动备份调度 | ❌ 无——snapshot 必须手动运行 |
| Point-in-Time Recovery (PITR) | ❌ 无——snapshot 是静态文件，不是时间序列 |
| 跨区域 DR 副本 | ⚠️ 仅 Replication（单向、单目标、无自动切换） |
| DR 自动切换（Failover） | ❌ 无——需要手动改配置重启 |
| 备份验证（Restore Test） | ❌ 无——从未自动验证备份的完整性 |
| 备份保留策略 | ❌ 无——没有备份轮换/过期 |
| 增量备份 | ❌ 无——每次快照都是全量 |
| 对象内容备份（不仅仅是元数据） | ❌ 支持 `replication`（对象级），但无时间点一致性 |

### 代码锚点

| 位置 | 当前能力 | 缺口 |
|------|---------|------|
| `internal/snapshot/snapshot.go` | SQLite 元数据快照（单文件 JSON） | 仅 SQLite、仅元数据、手动、全量 |
| `internal/replication/replication.go` | 事件驱动跨后端复制 | 无批量初始同步、无故障切换、无一致性检查 |
| `internal/repository/sqlite.go` | SQLite 单文件 | 无 WAL 备份、无 PITR |
| `internal/repository/postgres.go` | Postgres 连接 | 无 WAL 归档、无流复制集成、无故障检测 |
| `deploy/postgres/pgvector-setup.sql` | Postgres 初始化 | 无 pgBackRest / PITR 配置 |
| `internal/cluster/singleton.go` | Leases 表选主 | 无健康探针 → leader 选举 DR 切换 |
| `internal/api/rest/admin.go` | Admin API | 无 DR 管理端点（backup/restore/failover） |

### 为什么需要

**1. 数据保护的"最后一层"缺失。**

当前系统有：
- ✅ 存储冗余（Replication → 自动复制对象到第二存储）
- ✅ 元数据冗余（Postgres 流复制）
- ✅ 集群单例（选主防双写）

但没有：
- ❌ **RPO 管理**：用户无法在即使 RPO 要求内恢复（没有 PITR 时间点选择）
- ❌ **DR 验证**：用户无法确认备份是否可用（没有定期恢复测试）
- ❌ **DR 流程自动化**：灾难发生时需要手动修改 DNS/重启服务——分钟级的停机变成小时级

**2. 这是企业采购的硬性门槛。**

任何企业级采购（金融/医疗/政务）都会要求：

| 要求 | 描述 | 当前能否满足 |
|------|------|------------|
| RPO (Recovery Point Objective) | 最多丢失多少时间的数据 | ❌ 无 PITR，RPO = 上次手动备份时间 |
| RTO (Recovery Time Objective) | 多久恢复服务 | ❌ 无自动化，RTO = hours/days |
| 备份保留策略 | 备份保留多久 | ❌ 无自动保留/轮换 |
| 定期恢复演练 | 每季度验证备份可恢复 | ❌ 无自动化工具，需要手动还原到测试环境 |
| 跨区域 DR | 能在另一区域恢复服务 | ⚠️ 有 replication 但无可切换 |

**3. 代码基础已有部分前置条件。**

| 已有组件 | 可复用方向 |
|---------|----------|
| `internal/replication/replication.go` | DR 复制引擎——扩展为双向/多目标/一致性组 |
| `internal/snapshot/snapshot.go` | 备份导出格式——扩展为支持对象 + metadata 的复合快照 |
| `internal/reconcile/` | 调度框架——驱动定期备份 |
| `internal/cluster/singleton.go` | 主备切换——增强为完整健康检测 + 自动晋升 |
| `internal/storage/storage.go` | 任意后端作为备份目标——Local/S3/OSS/COS |

### 架构概要

```
Backup & DRaaS Framework
==========================

┌─ Backup Scheduler（备份调度器）────────────────────────────────────────┐
│                                                                        │
│  复用 reconcile 定时框架（`RECONCILE_INTERVAL_MINUTES`）:              │
│                                                                        │
│  POST /v1/admin/backup/policies                                       │
│  {                                                                     │
│    "id": "daily-full",                                                │
│    "schedule": "0 2 * * *",  // 每天凌晨 2 点                        │
│    "type": "full|incremental",                                         │
│    "scope": "metadata+objects|metadata-only",                         │
│    "target": "s3://backup-bucket/aero-vault/",                        │
│    "retention": {"full": 30, "incremental": 7},                        │
│    "verify": true,  // 备份后自动验证                                    │
│    "notification_webhook": "https://hooks.slack.com/..."              │
│  }                                                                     │
│                                                                        │
│  执行流程:                                                              │
│  1. 暂停事件处理（确保一致性快照）                                      │
│  2. 在 DB 中标记备份开始（`backup_runs` 表）                          │
│  3. 导出元数据快照（`snapshot.go` 扩展）：                              │
│     ├─ 全部表（objects/versions/tags/acl/events/jobs/...）            │
│     └─ 精确到事务一致的时间点                                          │
│  4. 可选：导出对象内容：                                               │
│     ├─ 全量：所有 object 内容复制到备份存储                              │
│     └─ 增量：上次备份后变更的对象（基于 updated_at）                   │
│  5. 校验：恢复元数据到临时 DB + 采样验证 checksum                       │
│  6. 清理旧备份（按 retention 策略删除）                                │
│  7. 发送通知 + 报告                                                    │
│                                                                        │
│  备份清单（backup_manifest.json）:                                     │
│  {                                                                     │
│    "backup_id": "bak-20260710-020000",                                 │
│    "type": "full",                                                     │
│    "started_at": "2026-07-10T02:00:00Z",                               │
│    "finished_at": "2026-07-10T02:15:30Z",                              │
│    "metadata": "s3://.../bak-20260710-020000/metadata.json.gz",        │
│    "objects_count": 15342,                                             │
│    "objects_size": 107374182400,  // 100GB                            │
│    "checksum": "sha256:abc123...",                                     │
│    "verified": true,                                                   │
│    "retention_until": "2026-08-09T02:00:00Z"                          │
│  }                                                                     │
│                                                                        │
└──────────────────────────────────────────────────────────────────────┘

┌─ Point-in-Time Recovery（时间点恢复）──────────────────────────────────┐
│                                                                        │
│  POST /v1/admin/restore                                                │
│  {                                                                     │
│    "backup_id": "bak-20260710-020000",                                 │
│    "target_tenant": "acme-restored",  // 恢复到新租户                     │
│    "point_in_time": "2026-07-10T01:00:00Z",  // 可选时间点                │
│    "include_objects": true,                                            │
│    "prefix_filter": "critical/"  // 可选恢复部分数据                      │
│  } → { "restore_id": "res-abc123", "estimated_size": "50GB" }         │
│                                                                        │
│  工作流:                                                                │
│  1. 创建新的租户（或使用已有空租户）                                    │
│  2. 从备份存储下载元数据快照                                           │
│  3. 恢复元数据到新租户的 DB 记录                                       │
│  4. 按需恢复对象内容：                                                 │
│     ├─ 完整恢复：从备份存储复制所有对象                                  │
│     └─ 按需恢复：仅恢复 metadata，对象在 GET 时从备份存储延迟回源        │
│  5. 验证：比对恢复后的对象数与备份清单                                  │
│  6. 返回恢复完成 + 访问凭证                                              │
│                                                                        │
│  GET /v1/admin/restore/{id} → 恢复进度（objects_restored/total）       │
│                                                                        │
└──────────────────────────────────────────────────────────────────────┘

┌─ Disaster Recovery Promotion（DR 自动切换）─────────────────────────────┐
│                                                                         │
│  DR 集群部署模式:                                                        │
│                                                                         │
│  主集群（Primary）                →  复制                         →  备用集群（Standby）          │
│  ┌─────────────────────┐                    ┌─────────────────────┐    │
│  │ aero-vault instance  │  1. 对象 replication │ aero-vault instance  │    │
│  │ Postgres (primary)   │  2. DB 流复制 (pg)    │ Postgres (hot standby)│    │
│  │ Storage: Local/S3    │  3. 备份快照           │ Storage: Local/S3    │    │
│  └─────────────────────┘                    └─────────────────────┘    │
│                                                                         │
│  DR Promotion（主动/自动）:                                             │
│                                                                         │
│  POST /v1/admin/dr/promote                                             │
│  {                                                                      │
│    "instance_id": "dr-01",                                              │
│    "reason": "primary_unreachable",                                     │
│    "force": false  // true = 即使主集群疑似存活也强制切换                  │
│  }                                                                      │
│                                                                         │
│  自动切换触发条件（需要 `DR_AUTO_FAILOVER=true`）:                       │
│  ├─ /readyz 连续 3 次失败（间隔 10s）                                   │
│  ├─ DB 不可达 > 30s                                                    │
│  └─ Storage backend 不可达（不是所有都）                                │
│                                                                         │
│  切换流程:                                                              │
│  1. 停止复制（不继续从主集群接收数据）                                   │
│  2. 晋升 Postgres standby → primary                                      │
│  3. 更新 leases 表（清除主集群的 lease ownership）                       │
│  4. 启动所有后台 worker（reconcile/lifecycle/indexer）                   │
│  5. 可选：更新 DNS / Load Balancer 指向 DR 集群                        │
│  6. 记录 DR 事件到 audit_log                                             │
│  7. 发送 DR 通知                                                        │
│                                                                         │
│  POST /v1/admin/dr/demote                                              │
│  → 主集群恢复后，降级回 standby（需要重新初始化同步）                      │
│                                                                         │
└──────────────────────────────────────────────────────────────────────┘

┌─ Backup Verification（备份验证）─────────────────────────────────────────┐
│                                                                         │
│  定期自动验证（由 backup scheduler 驱动）：                              │
│                                                                         │
│  1. 恢复最新备份到临时 SQLite（内存）                                    │
│  2. 对比恢复后的记录数与备份清单:                                        │
│     ├─ objects: 15234 == 15234 ✅                                       │
│     ├─ versions: 3421 == 3421 ✅                                        │
│     └─ tags: 12345 == 12345 ✅                                          │
│  3. 随机抽样 100 个对象进行 checksum 验证：                               │
│     ├─ 从备份存储读取对象 → 计算 sha256                                  │
│     └─ 与备份清单中的 checksum 对比                                      │
│  4. 生成验证报告 → 发送通知                                              │
│  5. 清理临时 SQLite                                                      │
│                                                                         │
│  验证失败告警:                                                          │
│  backup_verify_failed_total{reason} → Prometheus Alert → Pager          │
│                                                                         │
└──────────────────────────────────────────────────────────────────────┘

┌─ DR Readiness Checks（DR 就绪性检查）────────────────────────────────────┐
│                                                                         │
│  GET /v1/admin/dr/readiness                                            │
│  → {                                                                   │
│      "last_backup": "2026-07-10T02:00:00Z",                            │
│      "last_backup_verified": true,                                      │
│      "replication_lag_seconds": 5,                                      │
│      "db_replication": {status: "streaming", lag_bytes: 12345},        │
│      "standby_instance": {host: "10.0.1.2:8080", healthy: true},       │
│      "overall": "READY",  // READY | DEGRADED | NOT_READY              │
│      "issues": ["last DR test > 90 days ago"]                          │
│    }                                                                    │
│                                                                         │
└──────────────────────────────────────────────────────────────────────┘
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **备份过程中对象被修改** | 使用事务一致快照（Postgres `REPEATABLE READ` level）；对象内容备份在快照时间点后修改的以备份时的版本为准 |
| **增量备份的依赖链** | 增量备份依赖上一个全量备份；恢复时需要"全量 + 所有增量"的组合——manifest 清单记录了依赖链 |
| **主集群故障但未完全挂掉（分区脑裂）** | `force=false` 时先检查主集群存活；如果主集群拒绝降级则 `force=true` 可能需要人工介入 |
| **跨区域 DR 的数据延迟不一致** | 复制延迟可能 >5min；Promotion 会丢失最后几分钟的数据——在 RPO 评估中记录"未复制"事件数 |
| **大规模恢复（PB 级）** | 恢复工具支持并发 restore + 按需延迟回源（GET 时从备份存储拉取，透明回填） |
| **备份存储空间不足** | 每次备份前检查目标空间的剩余容量（低于 10% 发出告警，低于 2% 暂停备份） |
| **Backup Scheduler 在集群中重复运行** | 备份调度也是集群单例（复用 `cluster.Singleton` 机制），只有一个实例执行备份 |
| **恢复时 Schema 版本不匹配** | 备份时记录 DB Schema 版本号（migration ID）；恢复前检查版本兼容性，否则提示迁移步骤 |

### 代码影响范围

| 模块 | 影响 |
|------|------|
| **新增** `internal/backup/scheduler.go` | 备份调度器（定时 + 增量/全量策略 + 保留策略） |
| **新增** `internal/backup/executor.go` | 备份执行引擎（metadata export + object copy + manifest） |
| **新增** `internal/backup/restorer.go` | 恢复引擎（metadata import + object copy + 验证） |
| **新增** `internal/backup/manifest.go` | 备份清单格式（versioned JSON manifest） |
| **新增** `internal/backup/verifier.go` | 备份验证（record count + checksum sampling） |
| **新增** `internal/dr/promoter.go` | DR 自动/手动切换（DB 晋升 + 服务启动 + DNS 更新） |
| **新增** `internal/dr/readiness.go` | DR 就绪性检查 |
| **新增** `internal/repository/sql_backup.go` | `backup_policies` / `backup_runs` / `backup_manifest_refs` / `restore_runs` 表 CRUD |
| **新增** 迁移 `0034_backup_dr.up.sql` | `backup_policies` / `backup_runs` / `restore_runs` / `dr_config` 表 |
| **新增** `internal/api/rest/admin_backup.go` | REST API：备份策略 CRUD + 手动触发 + 恢复启动 + DR 管理 |
| **修改** `internal/snapshot/snapshot.go` | 扩展支持 Postgres + 元数据+对象复合快照 |
| **修改** `internal/cluster/singleton.go` | 备份调度器注册为另一个 singletion type |
| **修改** `cmd/server/main.go` | DR 模式检测（`DR_MODE=primary|standby|auto`）|
| **修改** `internal/config/config_app.go` | `DR_ENABLED` / `DR_MODE` / `DR_AUTO_FAILOVER` / `BACKUP_ENABLED` / `BACKUP_TARGET` |
| **新增** SDK 方法 | `CreateBackupPolicy`、`TriggerBackup`、`StartRestore`、`PromoteDR`、`GetDRReadiness` |

---

## 优先级与实施建议

| # | 方向 | 优先级 | 工程成本 | 商业价值 | 建议顺序 |
|---|------|--------|---------|---------|---------|
| 1 | **备份与灾难恢复（DRaaS）** | **P0** | L（跨组件重大改造） | 🔴 企业采购硬门槛 + 数据保护的底线 | **#1 优先** — 没有 DR 就没有生产信任 |
| 2 | **批量导入/导出与迁移框架** | **P1** | M（新包 + CLI + API + 后台作业） | 🟠 平台采用的"最后一公里"障碍 | **#2** — 解决用户"怎么进来"的问题 |
| 3 | **数据质量与 Schema 验证管线** | **P1** | M（多个新包 + 作业 + API） | 🟢 AI 管道可信度的基础——区分"存储"和"智能平台" | **#3** — 建立数据治理基础 |
| 4 | **事件与 Webhook 可观测仪表板** | **P2** | M（指标 + API + Web UI 面板） | 🟡 企业运维的必备能力——事件管线的透明化 | **#4** — 与 Webhook 用户运营同步推进 |
| 5 | **GraphQL / gRPC 协议网关** | **P2** | L（新协议依赖 + 代码生成 + 长期维护） | 🟡 覆盖新消费群体 + 微服务生态 | **#5** — 平台成熟后拓广接入面 |

**建议实施序列：** `#1 → #2 → #3 → #4 → #5`

1. **备份与 DR（#1）**— 任何生产部署的底线。没有自动备份和 DR 能力，数据只有"运行中"的保护，
   没有"灾难后"的保护。这是企业采购的硬性门槛，优先级 P0。
2. **批量导入/导出与迁移框架（#2）**— 解决"怎么进来"的问题。用户评估平台后，迁移成本往往
   是决定是否采用的关键因素。没有迁移工具，平台的市场增长受限。
3. **数据质量与 Schema 验证（#3）**— 在 AI 管线完善后，数据的可信度成为下一个瓶颈。
   让平台从"存储"升级为"智能数据平台"的关键一步。
4. **事件与 Webhook 可观测仪表板（#4）**— 随着 Webhook 用户增多，运维人员需要事件管线的
   透明度。与 #2 的批量操作结合可以形成完整的数据管道可观测方案。
5. **GraphQL / gRPC 网关（#5）**— 平台成熟后的协议扩展。当前 4 种协议（REST/S3/WebDAV/MCP）
   已覆盖主流场景，新协议在用户需求明确时投入。

---

## 附录：各方向的前置依赖分析

| # | 方向 | 前置依赖 | 依赖是否已实现 |
|---|------|---------|--------------|
| 1 | 数据质量与 Schema 验证 | `internal/jobs` 作业队列（异步 Profile） | ✅ 已实现 |
|   |  | `internal/ai/extractor.go`（结构化提取） | ✅ 已实现 |
|   |  | `internal/events/webhook.go`（质量告警） | ✅ 已实现 |
|   |  | `internal/telemetry/metrics.go`（质量指标） | ✅ 已实现 |
| 2 | 批量导入/导出与迁移 | `internal/jobs` 作业队列（批量操作） | ✅ 已实现 |
|   |  | `storage.Storage` 接口（统一读写） | ✅ 已实现 |
|   |  | `internal/storage/local_multipart.go`（分片上传） | ✅ 已实现 |
|   |  | `internal/replication/replication.go`（跨后端复制） | ✅ 已实现 |
| 3 | GraphQL / gRPC 网关 | `FileService` 协议无关服务层 | ✅ 已实现 |
|   |  | `auth.Registry`（统一鉴权） | ✅ 已实现 |
|   |  | `middleware.RateLimiter`（限流复用） | ✅ 已实现 |
|   |  | `middleware.Tenant`（租户提取） | ✅ 已实现 |
| 4 | 事件与 Webhook 可观测 | `internal/events/bus.go` + `webhook.go` | ✅ 已实现 |
|   |  | `internal/repository/webhook_failures.go` | ✅ 已实现 |
|   |  | `internal/telemetry/metrics.go`（OTel 指标基础） | ✅ 已实现 |
|   |  | `deploy/grafana/*` 仪表板基础 | ✅ 已实现 |
|   |  | `internal/api/rest/admin.go`（Admin API 框架） | ✅ 已实现 |
| 5 | 备份与灾难恢复 | `internal/snapshot/snapshot.go`（快照基础） | ✅ 已实现 |
|   |  | `internal/replication/replication.go`（复制引擎） | ✅ 已实现 |
|   |  | `internal/cluster/singleton.go`（选主机制） | ✅ 已实现 |
|   |  | `internal/reconcile/`（定时调度框架） | ✅ 已实现 |
|   |  | `internal/repository/postgres.go`（流复制基础） | ✅ 已实现 |

所有 5 个方向的前置依赖均已实现。这是一个重要的观察：当前系统的基础设施已经足够成熟，
这些方向的实施不需要引入新的底层抽象，而是在现有抽象上扩展。

---

*分析结束。本文档中描述的 5 个方向经过对 `docs/requirements/` 下全部 34 期既有分析的 `grep` 验证，
确认未在任何既有文档中进行过实质性架构分析。*
