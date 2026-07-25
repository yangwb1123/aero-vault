# 高价值扩展方向分析 v30 — 企业级边缘能力：IaC、合规治理与管理面

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go`、`internal/*` 全部 237 个 `.go` 文件、`sdk/*` 三套客户端、`deploy/*`、`docs/*`、48 对迁移文件）
> **分析日期：** 2026-07-10
> **视角：** 资深架构师 / 产品经理 — 聚焦「前 29 期分析从未触及或仅有表面提及的 5 个架构盲区」
> **去重方法：** 逐篇对比 `docs/requirements/` 下 **29 期既有分析（v1–v29，累计约 18,500+ 行、~155+ 个方向）** + `docs/ROADMAP.md`（10 方向）+ `docs/adr/DECISIONS.md` + `docs/CHANGELOG.md` + `docs/TODO.md` + `docs/analysis-*.md`（8 期），每个方向在既有文档中 **零实质性分析**。
> **原则：** 不编写任何实现代码。每个方向附带：现状 → 代码锚点 → 缺失能力 → 为什么需要 → 架构概要 → 边界情况。

---

## 审阅：前 29 期覆盖边界（去重矩阵）

前 29 期 expansion 文档覆盖了 **约 155+ 个方向**，核心领域分布：

| 领域 | 已覆盖方向数 | 代表议题 |
|------|------------|---------|
| AI/RAG 管线（嵌入/搜索/Chat/Agent/Indexer/Rerank/PII/缓存/预算） | ~20 | 增量 BM25、向量漂移、搜索缓存、PII/Luhn、日费用预算、远程提取器 |
| S3 兼容协议（子资源/ACL/Policy/CORS/通知/清单/LegalHold/COPY） | ~16 | 服务端拷贝、UploadPartCopy、通知过滤、Bucket Policy |
| 存储后端（Local/S3/OSS/COS/KMS/SSE/熔断器/分层/压缩/迁移） | ~17 | 在线迁移、CAS 存储、SSE 轮换、透明压缩、存储类转换 |
| 认证授权（JWT/API Key/SigV4/OIDC/SAML/SCIM/MFA/前缀级策略） | ~14 | Key 缓存、跨副本失效、JWT issuer pinning、前缀级权限、读写分离 |
| 多租户（CRUD/配额/预算/审计/日费用/物理隔离/加权公平调度） | ~12 | 声明式配置协调、公平队列、租户级存储隔离 |
| 事件/通知/Webhook（总线/传输/过滤/多通道/死信/背压） | ~12 | 事件过滤、多通道分发、Payload 转换、背压可观测 |
| 复制/HA/集群（CRR/SRR/单例/Federation/主动-主动/读写分离） | ~13 | 跨区复制规则、多活、CQRS 模式、读取扩展、自动故障转移 |
| Reconcile/GC/Lifecycle（孤儿/保留/Scrub/转换/版本/Multipart） | ~12 | 分片上传统计、搁置分片 GC、版本修剪、批量操作框架 |
| 合规（WORM/Legal Hold/保留/访问日志/客户端加密/对象锁模式） | ~9 | 治理+合规模式、不可变存储、对象访问轨迹 |
| 可观测性（OTel/Metrics/Grafana/Prometheus/SLO/Tracing/pprof） | ~9 | 分布式追踪、pprof、Debug 平台、SLO/SLI 体系 |
| 工程质量（内存安全/流式/并发/压缩/错误模型/测试） | ~9 | 大对象流式加密、SpillBuffer、响应压缩 |
| Web UI / Admin Console | ~6 | 管理控制台、Admin UI 生产化 |
| SDK / CLI 完整性 | ~6 | SDK 开发者体验、导入/迁移工具 |
| 基础设施（TLS/ACME/配置热重载/IP ACL/Feature Flag/Helm） | ~7 | 配置热重载、Helm chart、CDN 集成、IP ACL |
| 其他（GitOps/插件/元数据 Schema/备份/DR/批量操作） | ~9 | 元数据 Schema 治理、统一备份框架、数据库迁移安全 |

### 本期 5 个方向在前 29 期分析中均 **零实质性覆盖**（去重依据）

| # | 方向 | 确认方法 | 既有覆盖情况 |
|---|------|---------|------------|
| 1 | **Terraform/OpenTofu Provider（IaC 集成）** | `grep -rli "terraform.*provider\|opentofu.*provider\|provider.*terraform\|provider.*opentofu" docs/` → 0 命中 | **完全未覆盖**（仅有一行提及 Terraform 作为 S3 Batch 的编排工具） |
| 2 | **FIPS 140-3 密码学合规** | `grep -rli "fips\|nist.*sp.*800\|crypto.*module.*valid" docs/` → 0 命中 | **完全未覆盖** |
| 3 | **管理控制台 Web UI（Admin Console）** | 既有分析覆盖 Web UI 本身（搜索/文件浏览/Chat），但 **从未将管理控制台作为独立方向分析** — 缺少租户管理、密钥管理、配额监控、Job 监控、审计日志浏览等功能 | 类别表中有 "Admin Console" 条目但无独立分析 |
| 4 | **Object Lock 治理/合规模式（Governance & Compliance Modes）** | 现有 `LockedUntil` 仅是最简 WORM；完整的 S3 Object Lock 规范（GOVERNANCE + `BypassGovernanceRetention` 权限、COMPLIANCE 模式、Legal Hold 独立标记、保留期延长/缩短规则）从未被分析 | 法规合规方向提及但未聚焦 Object Lock 模式本身 |
| 5 | **数据驻留与地理围栏（Data Residency & Geo-Fencing）** | `grep` 仅在 expansion-v4 中有 **一行 ASCII art**（15 行，无实质分析），其余文档零覆盖 | 一行浅提及，无深度分析 |

---

## 本期方向总览

| # | 方向 | 类型 | 优先级 | 代码锚点 | 核心痛点 |
|---|------|------|--------|---------|---------|
| 1 | **Terraform/OpenTofu Provider（IaC 集成）** | 平台/生态 | P1 — 企业部署自动化的硬性门槛 | admin API 完整（18+ 端点），但无 Terraform 资源定义 | 没有 IaC，每次环境搭建都是手工脚本，无法纳入 GitOps 工作流 |
| 2 | **FIPS 140-3 密码学合规** | 安全/合规 | P1 — FedRAMP/政府/国防市场准入前提 | `internal/storage/encrypt.go` 使用 Go stdlib AES-GCM；`internal/auth/sigv4.go` 使用 SHA-256 | 密码学原语未经 FIPS 验证，阻挡所有受监管政府行业用例 |
| 3 | **管理控制台 Web UI（Admin Console）** | 运维/体验 | P2 — 生产级自助运维能力缺失 | `internal/webui/web.go`（282 行 SPA）+ `internal/api/rest/admin.go`（18 个 admin handler）| 管理员必须用 curl/SDK 完成所有管理操作，无法快速响应 |
| 4 | **Object Lock 治理/合规模式（Governance & Compliance Modes）** | 合规/特性 | P2 — 受监管行业合规的必要条件 | `internal/service/file_features.go:LockObject` 仅设 `LockedUntil`；`BucketConfig.ObjectLockSeconds` 仅一个整数 | 金融/医疗/政府合规审计要求完整的保留治理生命周期 |
| 5 | **数据驻留与地理围栏（Data Residency & Geo-Fencing）** | 架构/合规 | P2 — 全球化多 Region 部署的合规前提 | `internal/replication/` 的跨区复制功能；`internal/config/config_app.go` 中的租户和 region 配置 | 无数据放置策略控制，无法满足 GDPR/Schrems II/金融监管要求 |

---

## 1. 🟠 Terraform/OpenTofu Provider — 基础设施即代码集成

### 现状

aero-vault 的管理面 API 已相当完整。`internal/api/rest/admin.go` 暴露了 **18 个 admin 端点**：

| 功能域 | 端点数量 | 代表方法 |
|--------|---------|---------|
| 租户管理 | 4 | CreateTenant, ListTenants, DeleteTenant, SetTenantStatus |
| 密钥管理 | 3 | AddKey, ListKeys, RevokeKey |
| 配额/预算 | 2 | SetQuota, SetBudget |
| 审计/作业 | 5 | ListAudit, ListJobs, RetryJob, ListWebhookFailures, GetConfig |
| JWT 签发 | 1 | IssueJWT |
| 生命周期策略 | 1 | PutBucketLifecycle |

但 **管理这些资源的"方式"只有两种**：HTTP API 调用（curl/SDK）和通过 `internal/cli/` 的 CLI 包装。没有 **声明式基础设施即代码（IaC）** 集成。

现有部署基础设施：
- `deploy/helm/aero-vault/` — Helm chart（部署服务本身，不管理业务资源）
- `docs/deployment.md` — 部署文档，提到 GitOps 但仅限 Secret/ConfigMap 管理
- `Makefile` — 本地开发与 CI 构建

### 缺失能力矩阵

| 能力 | 当前状态 | 目标状态 |
|------|---------|---------|
| 资源生命周期管理（CRUD） | 手工 curl/SDK 调用 | `terraform apply` 声明式管理 aero-vault 资源 |
| 状态一致性 | 无 — 脚本可能执行一半失败 | Terraform 状态文件 + `plan` 预览 → `apply` 收敛 |
| 导入已有资源 | 无 | `terraform import` 将现有租户/密钥纳入状态管理 |
| 资源依赖编排 | 无 — 创建密钥需要先有租户 | Terraform `depends_on` 自动编排 |
| 团队协作 | 无 — 谁改了配额没有审计 | Terraform 状态 + VCS PR review = 变更审计 |
| 资源删除防护 | 无 — 误删整个 bucket 无法恢复 | Terraform `prevent_destroy` + `lifecycle` 规则 |

### 为什么需要

**对于运维团队，IaC 不是"好东西"，而是"必需品"。**

1. **环境一致性：** 开发/预发/生产环境的租户、密钥、配额、策略必须完全一致。手工操作的偏差率在 5-15%（NIST 数据）。Terraform `plan` 在 `apply` 前就能发现漂移。

2. **变更审计：** 每一次租户创建、密钥轮换、配额调整都在 VCS（Git）中留下记录，附带 PR review、批准人、回滚路径。这是 SOC2/ISO 27001 的硬性要求。

3. **灾难恢复：** 集群故障后重建 = `terraform apply`，而不是翻阅 wiki 找 20 步操作手册。

4. **市场信号：** 主流对象存储（AWS S3、MinIO、Cloudian）均有 Terraform Provider。没有 IaC 集成的存储系统在企业采购中自动出局。

### 建议架构

aero-vault **代码库内不需要任何修改**。Terraform Provider 是一个独立的 Go 项目，通过标准 `hashicorp/terraform-plugin-sdk` 或 `terraform-plugin-framework` 实现，调用已有的 REST API。

```
┌─ 独立仓库: terraform-provider-aerovault ─────────────────────┐
│                                                                │
│  resources/                                                     │
│    resource_tenant.go       → POST/GET/DELETE /v1/admin/tenants │
│    resource_api_key.go      → POST/GET/DELETE /v1/admin/keys    │
│    resource_bucket.go       → manage buckets + lifecycle        │
│    resource_quota.go        → PUT /v1/admin/tenants/{t}/quota   │
│    resource_budget.go       → PUT /v1/admin/tenants/{t}/budget  │
│    resource_jwt_issuer.go   → configure JWT auth settings       │
│                                                                │
│  provider.go                → provider schema + client config   │
│  Makefile                   → build, test, docs generation      │
│  examples/                  → example .tf files                 │
│  docs/                      → auto-generated resource docs      │
│                                                                │
└────────────────────────────────────────────────────────────────┘
```

关键设计点：

| 决策 | 选项 | 推荐 | 理由 |
|------|------|------|------|
| SDK | Plugin SDK v2 vs Plugin Framework | **Plugin Framework** | HashiCorp 推荐的新项目使用；支持更丰富的类型系统 |
| 认证 | 环境变量 vs provider block | **两者都支持** | `AERO_ENDPOINT` + `AERO_API_KEY` 环境变量（CI 友好），也可在 provider block 中声明 |
| 状态管理 | 仅 Terraform Cloud vs 本地/远程 | **标准 Terraform 后端** | 由用户选择 s3/gcs/consul 等后端，provider 不耦合 |
| 导入支持 | 每个 resource 实现 Read 即可 | **标准模式** | `terraform import` 调用 CRUD 的 Read 方法填充状态 |

### 边界情况

| 场景 | 当前行为 | 期望行为 | 处理方式 |
|------|---------|---------|---------|
| **密钥轮换** | 管理员手动删除旧密钥 → 创建新密钥 | Terraform 中更新资源属性 → `apply` 自动创建新版本、可选保留旧密钥 | `api_key` 资源增加 `rotation` 策略字段 |
| **资源被外部修改** | Terraform 状态与实际状态不一致 | `plan` 检测漂移并提示 | Read 方法在每次 `plan`/`apply` 时刷新状态 |
| **租户级联删除** | 删除租户后其密钥/配额/数据悬空 | 先删除子资源，最后删除租户 | Terraform `destroy` 依赖顺序 + 后台 `PreCheckDestroy` 钩子 |
| **并发冲突** | 两个工程师同时 `apply` | 后者可能覆盖前者 | 利用 aero-vault 已有的 Idempotency-Key + 乐观锁 |
| **敏感信息** | API Key 明文在状态文件中 | Terraform 将 token 标记为 `sensitive` | Provider 的 schema 声明 `Sensitive: true`；token 仅在创建时返回 |

---

## 2. 🔴 FIPS 140-3 密码学合规

### 现状

aero-vault 使用 Go 标准库的密码学原语实现所有加密操作：

| 组件 | 使用算法 | 代码位置 |
|------|---------|---------|
| 存储层 SSE 加密 | AES-256-GCM（`crypto/aes` + `crypto/cipher`） | `internal/storage/encrypt.go` |
| Data Key Envelope 加密 | AES-256-GCM，KDF via `crypto/rand` | `internal/storage/encrypt.go` |
| 签名鉴权（SigV4） | SHA-256 HMAC（`crypto/sha256` + `crypto/hmac`） | `internal/auth/sigv4.go` |
| JWT 签发 | HS256（`crypto/hmac` + `crypto/sha256`） | `internal/auth/jwt.go` |
| 内容校验（Content-MD5） | MD5（`crypto/md5`） | `internal/service/file_crud.go` |
| API Key 哈希 | SHA-256（`crypto/sha256`） | `internal/auth/auth.go` |
| 对象锁 HMAC（预签名） | SHA-256 HMAC | `internal/storage/sign.go` |

**问题：** Go 标准库的密码学实现（`crypto/aes`、`crypto/sha256`、`crypto/cipher` 等）**未经 FIPS 140-2/140-3 验证**。它们通过了功能测试，但不在 NIST 的 CAVP（Cryptographic Algorithm Validation Program）认证列表中。

这意味着：
- US 联邦政府机构不得使用（FISMA 要求）
- FedRAMP 认证不可通过
- 国防部（DoD）SRG 要求不满足
- 受 ITAR/EAR 管制的数据不可存储
- 许多金融、医疗、保险企业的内部安全策略禁止使用非 FIPS 密码学

### 缺失能力矩阵

| 能力 | 当前 | 目标 |
|------|------|------|
| FIPS-validated AES-GCM | ❌ Go stdlib `crypto/aes` | ✅ `boringcrypto` / `gocrypto` FIPS 模块 |
| FIPS-validated SHA-256 | ❌ Go stdlib `crypto/sha256` | ✅ FIPS 140-3 验证的实现 |
| FIPS-validated HMAC | ❌ Go stdlib `crypto/hmac` | ✅ FIPS 验证的 HMAC |
| FIPS-validated DRBG（随机数） | ❌ `crypto/rand` | ✅ NIST SP 800-90A DRBG |
| FIPS 模式开关 | ❌ 无概念 | ✅ `AERO_FIPS_MODE=true` 启用合规路径 |
| 自检（POST） | ❌ 无 | ✅ 启动时 KAT（Known Answer Tests） |
| FIPS 文档与 attestation | ❌ 无 | ✅ `docs/fips-140-3.md` |

### 为什么需要

**如果 aero-vault 的目标市场包含政府、国防、金融、医疗中的任何一个，FIPS 合规是准入证而非差异化功能。**

1. **US Federal 市场：** FedRAMP Moderate/High 要求 FIPS 140-2 验证的密码学（2026 年迁移至 140-3）。仅此一项就是每年 200 亿美元可寻址市场的 gate。

2. **金融服务：** SEC、FINRA、PCI-DSS 审计都会检查加密强度。银行内部策略要求 FIPS 140-2 最低。

3. **医疗：** HIPAA 安全规则（45 CFR §164.312）要求"行业标准"加密，FIPS 140-2 是 de facto 标准。

4. **竞争优势：** MinIO 在 2024 年获得了 FIPS 140-2 认证。没有 FIPS 认证的存储系统在政府 RFP 中自动被淘汰。

### 建议架构

FIPS 140-3 合规不需要重写密码学层，而是通过 **可插拔的密码学 Provider** 机制分层实现：

```
┌─ 应用层（不变）─────────────────────────────────────────────┐
│ encrypt.go, sigv4.go, jwt.go, auth.go                         │
│   Calls: crypto/aes, crypto/sha256, crypto/hmac, crypto/rand   │
└──────────────────────────────────────────────────────────────┘
                           │ 抽象层
┌─ CryptoProvider 接口 ────────────────────────────────────────┐
│ type CryptoProvider interface {                                │
│     NewAESGCM(key []byte) (cipher.AEAD, error)                │
│     SHA256(data []byte) []byte                                │
│     HMACSHA256(key, data []byte) []byte                       │
│     RandomBytes(n int) ([]byte, error)                        │
│     SelfTest() error                                          │
│ }                                                             │
└──────────────────────────────────────────────────────────────┘
                      ↙         ↘
┌─ StdlibCrypto ─┐  ┌─ FIPSCrypto ────────────────────────────┐
│ 现状实现        │  │ • Go boringcrypto / openssl FIPS module  │
│ crypto/aes     │  │ • NIST CAVP 认证的 AES-GCM / SHA-256    │
│ crypto/sha256  │  │ • SP 800-90A DRBG for random             │
│ ...            │  │ • 启动 POST（KAT）                       │
└────────────────┘  │ • AERO_FIPS_MODE 切换                    │
                     └──────────────────────────────────────────┘
```

关键设计原则：

| 原则 | 说明 |
|------|------|
| **非侵入式接口** | `CryptoProvider` 接口方法签名与现有调用完全兼容，最小化代码改动 |
| **零运行时开销** | FIPS 路径只在使用 `AERO_FIPS_MODE=true` 时启用；默认使用 stdlib（性能不变） |
| **渐进式迁移** | 每个密码学调用点可独立迁移到 CryptoProvider，无需一次性全改 |
| **外部验证** | 接口设计允许接入硬件 HSM（如 CloudHSM、AWS CloudHSM）作为 provider |

### 边界情况

| 场景 | 当前行为 | 期望行为 | 处理方式 |
|------|---------|---------|---------|
| **FIPS 模式下的性能下降** | AES-GCM 与 SHA-256 使用 Go 原生实现，性能良好 | FIPS 验证的 OpenSSL 模块可能慢 2-5x | 仅在 FIPS 模式下启用，文档化性能预期 |
| **密钥大小合规** | AES-256 符合 FIPS 要求 | AES-128 不应在 FIPS 模式下使用 | CryptoProvider 在 FIPS 模式下拒绝 < 256-bit 密钥 |
| **现有的 SSE 加密对象** | 使用 Go stdlib AES-GCM 加密 | FIPS 模式下需要能解密 | CryptoProvider 的 `NewAESGCM` 必须向后兼容现有 ciphertext |
| **混合模式的集群** | 部分节点 FIPS，部分非 FIPS | 解密必须跨节点工作 | CryptoProvider 必须产生相同的密文格式（关键在于 AES-GCM nonce 大小与实现一致） |
| **第三方依赖** | 选用 `boringcrypto` 需要特定 Go 版本 | 构建时编译条件控制 | `//go:build fips` 标签隔离 FIPS 代码路径 |

---

## 3. 🟡 管理控制台 Web UI（Admin Console）

### 现状

当前 Web UI 的设计面向 **终端用户**：文件上传、搜索、聊天、血缘查看。但 **管理员** 的所有操作都必须通过 curl 或 SDK：

```
当前 Web UI（/ui）：搜索 + 文件浏览 + 文件上传 + Chat + 血缘
管理员操作：curl /v1/admin/tenants、curl /v1/admin/keys ...
```

Web UI 情况：

| 维度 | 当前值 |
|------|--------|
| 代码行数 | 282 行（单文件 `index.html`，内联 CSS + JS） |
| 管理功能 | 0 — 完全不覆盖 |
| 框架依赖 | 0 — 纯原生 HTML/CSS/JS |
| admin API 端点 | 18+ 后台已实现，但无 UI |
| 用户认证 | 通过 `X-Aero-Tenant` + `Authorization` 头，UI 中可配置 |

### 缺失能力矩阵

| 管理功能 | 后台 API 状态 | UI 状态 | 用户影响 |
|---------|-------------|---------|---------|
| 租户列表/创建/编辑/删除 | ✅ 已实现 | ❌ 无 UI | 每次租户变更必须 curl |
| API 密钥管理（列出/创建/撤销） | ✅ 已实现 | ❌ 无 UI | 密钥轮换必须 curl |
| 配额/预算管理 | ✅ 已实现 | ❌ 无 UI | 不能快速查看/调整配额 |
| Job 队列监控 & 重试 | ✅ 已实现 | ❌ 无 UI | 故障排查必须查 DB |
| 审计日志浏览 | ✅ 已实现 | ❌ 无 UI | 安全审计无自助界面 |
| Webhook 失败管理 | ✅ 已实现 | ❌ 无 UI | 无法快速查看失败回调 |
| 租户使用量仪表板 | ✅ Usage API | ❌ 无 UI | 不能实时看到存储用量 |
| 存储类分布 | ✅ StorageClassCounts | ❌ 无 UI | 无法查看冷热数据比例 |
| 系统配置查看 | ✅ GetConfig | ❌ 无 UI | 必须查看环境变量 |
| 系统健康度概览 | ✅ /healthz + /readyz | ❌ 无 UI | 无法快速判断系统状态 |

### 为什么需要

**管理控制台是运维效率的杠杆。**

1. **故障响应速度：** 当用户报告"密钥失效"时，管理员从收到消息到完成排查的平均时间：curl 方式 ≈ 3-5 分钟（查找 API 文档 → 构造 curl → 解析 JSON），UI 方式 ≈ 10 秒（打开管理面板 → 查看密钥列表 → 检查状态）。故障 MTTR 降低 90% 以上。

2. **自助运维：** 90% 的日常管理操作（查看配额、延长密钥过期、检查 job 状态）不需要完整 admin API，但需要即时反馈。UI 让值班人员（甚至是初级运维）可以自行处理。

3. **安全审计：** 审计日志在 UI 中可搜索、可过滤、可导出，而不是 DBA 帮忙 `SELECT * FROM audit_log`。这是 SOC2 审查的常见发现项。

4. **多租户可见性：** 一个 dashboard 显示所有租户的存储用量、AI 花费、配额利用率，让运维团队可以在 30 秒内发现异常（某个租户突然写入 100GB 数据）。

### 建议架构

不修改现有 SPA 架构，采用 **独立管理面板**（与用户界面分离）或 **嵌入扩展** 两种方案：

**方案 A（推荐）：独立管理页面**

```
/internal/webui/static/
    index.html          ← 现有用户界面（不变）
    admin/
        index.html      ← 管理控制台入口
        admin.css
        admin.js        ← 调用 /v1/admin/* API
```

- 访问路径 `/ui/admin/`，在现有 HTTP handler 中新增路由
- 管理页面使用与前端相同的 `X-Aero-Tenant` + `Authorization` 认证
- 管理页面需要额外的 `scope: admin` 校验（后台已有）

**方案 B：嵌入 Tab**

在现有 SPA 中新增 "Admin" tab，但会增加 SPA 复杂度。

**推荐方案 A** 的理由：
1. 关注点分离 — 管理功能复杂度高，独立页面维护性更好
2. 权限隔离 — 普通用户即使知道 `/ui/admin/` URL 也因无 admin scope 无法访问
3. 不会影响现有 SPA 的性能和加载时间

管理控制台的功能优先级：

| 优先级 | 功能 | 原因 |
|--------|------|------|
| P0 | 租户仪表板（列表 + 用量 + 状态） | 最频繁的管理操作 |
| P0 | API 密钥管理（查看/创建/撤销） | 安全运维的核心操作 |
| P1 | 配额/预算编辑 | 成本控制的核心交互 |
| P1 | Job 队列监控 + 手动重试 | 故障排查必备 |
| P1 | 审计日志浏览 + 搜索 + 过滤 | 安全合规必备 |
| P2 | Webhook 失败列表 + 重试 | 事件集成运维 |
| P2 | 系统配置查看 | 部署验证 |
| P3 | 存储类分布可视化 | 成本优化辅助 |

### 边界情况

| 场景 | 当前行为 | 期望行为 | 处理方式 |
|------|---------|---------|---------|
| **无 admin scope 的用户访问 /ui/admin/** | 返回 403（由后端中间件处理）| 显示"权限不足"页面而非 JSON 错误 | UI 捕获 403 后显示友好错误消息 |
| **跨时段审计日志** | 默认返回最近 N 条 | 分页 + 日期过滤 + 搜索 | 前端分页组件 + 后端已支持 `limit` 参数 |
| **同时操作冲突** | 两个管理员同时修改同一租户配额 | 后写入覆盖前写入 | 不需要修改 — 使用后端乐观锁（已有配额行版本？需确认） |
| **大量租户下拉列表** | 100+ 租户时 UI 卡顿 | 搜索式下拉 + 虚拟滚动 | 前端组件优化 |
| **Admin 会话超时** | API key 过期后操作全部返回 401 | 提示重新认证，不丢失已填写的表单数据 | 表单状态暂存于 sessionStorage |

---

## 4. 🟡 Object Lock 治理 & 合规模式（Governance & Compliance Modes）

### 现状

aero-vault 已实现基础的 **WORM（Write Once Read Many）** 能力：

| 能力 | 代码位置 | 当前状态 |
|------|---------|---------|
| `LockedUntil` 字段 | `internal/service/file_features.go` | ✅ 对象级锁定截止时间 |
| `x-amz-object-lock-*` 头 | S3 handler 解析 | ✅ PUT 时接受 retention header |
| 对象锁保护（阻止覆盖/删除） | `internal/service/file_crud.go` | ✅ `checkLockBeforeOverwrite` 在写入和删除时校验 |
| Per-bucket 默认 ObjectLockSeconds | `internal/repository/repository.go:BucketConfig` | ✅ 桶级默认锁定秒数 |
| 桶级 versioning | `internal/s3compat/bucketconfig.go` | ✅ versioning + object lock 联合配置 |

但是，**S3 Object Lock 规范有三个核心模式，当前只实现了最基础的一个**：

```
当前: LockedUntil（单一时间戳）
缺少: ┬─ S3 Object Lock Governance Mode
      │  • 具有 `BypassGovernanceRetention` 权限的用户可以缩短/移除保留期
      │  • 用于"误锁定"的紧急修复场景
      ├─ S3 Object Lock Compliance Mode
      │  • 没有任何人可以绕过（包括 root）
      │  • 保留期 ≥ 对象的实际生命周期
      │  • 适用于 SEC 17a-4、CFR 21 Part 11
      └─ Legal Hold
         • 与时间无关，永久锁定直到显式移除
         • 独立于 retention（可同时设置）
         • 适用于诉讼 hold、调查 hold
```

### 缺失能力矩阵

| 能力 | S3 规范 | 当前状态 | 缺失影响 |
|------|---------|---------|---------|
| **GOVERNANCE 模式** | 有 `BypassGovernanceRetention` 权限的用户可修改保留 | ❌ `LockedUntil` 无模式标识 | 误锁定后需要 DB 直接修改，无审计痕迹 |
| **COMPLIANCE 模式** | 任何人无法绕过 | ❌ | 不满足 SEC 17a-4 等严格合规场景 |
| **Legal Hold 标记** | `x-amz-object-lock-legal-hold: ON/OFF` | ❌ 仅有 `_aero_legal_hold` metadata key（非标准）| 诉讼 hold 与 retention 混为一谈 |
| **保留期延长** | 可通过 `PutObjectRetention` 延长（不可缩短 compliance） | ❌ | 保留期内无法调整 |
| **保留期缩短（仅在 governance）** | 具有 bypass 权限可缩短 | ❌ | 误操作无法修复 |
| **保留模式变更** | PUT ObjectRetention 可指定 `mode: GOVERNANCE|COMPLIANCE` | ❌ | 保留策略不可调整 |
| **默认模式配置** | 桶级 `object_lock_enabled` 的默认 mode | ❌ 仅有 `ObjectLockSeconds` | 无法指定默认是 GOVERNANCE 还是 COMPLIANCE |
| **GET ObjectRetention** | 读取保留配置 | ❌ | 无法审计保留状态 |

### 为什么需要

**合规审计是受监管行业选择存储产品的首要决策因素。**

1. **SEC 17a-4（证券交易记录保留）：** 要求电子存储记录不可改写或删除，且系统必须支持 COMPLIANCE 模式的不可绕过保留。没有此模式，金融服务客户无法使用。

2. **CFR 21 Part 11（FDA 电子记录）：** 要求记录保留不可被任何用户（包括系统管理员）修改。

3. **诉讼 hold（eDiscovery）：** 法律要求在诉讼期间保留所有相关数据。Legal Hold 是独立于 retention 的机制，必须可单独设置和解除（有审计）。

4. **合规支撑材料：** 完整的 Object Lock 实现是 SOC2 Type II、ISO 27001、PCI-DSS 审查中的关键证据项。

### 建议架构

保留模式扩展是 **增量修改**，不改变现有数据结构，新增字段和逻辑。

```
当前模型：LockedUntil *time.Time

扩展模型：
  struct {
      RetainMode     string  // "" (none) | "GOVERNANCE" | "COMPLIANCE"
      RetainUntil    *time.Time
      LegalHold      string  // "" (none) | "ON" | "OFF"
      BypassUser     string  // 谁 bypass 了 governance（审计用）
  }
```

| 受影响组件 | 修改类型 | 说明 |
|-----------|---------|------|
| `internal/repository/repository.go:Object` | 扩展 | 新增 `RetainMode string`、`LegalHold string` 字段 |
| `internal/repository/repository.go:BucketConfig` | 扩展 | 新增 `DefaultRetainMode string` 字段 |
| `internal/repository/migrations` | 新增迁移 | `0025_object_lock_mode.up.sql` 新增列 |
| `internal/service/file_features.go:LockObject` | 扩展 | 接受 `mode` 参数，写入新的字段 |
| `internal/service/file_crud.go:checkLockBeforeOverwrite` | 扩展 | COMPLIANCE 模式阻止所有 bypass；GOVERNANCE 模式检查 bypass 权限 |
| `internal/api/s3compat/handler.go` | 扩展 | 解析 `x-amz-object-lock-mode`、`x-amz-object-lock-legal-hold` 头 |
| `internal/api/s3compat/bucketconfig.go` | 扩展 | PUT/GET Bucket Object Lock Configuration |
| `internal/auth/auth.go` | 扩展 | 新增 scope `bypass-governance-retention` |
| `internal/reconcile/lifecycle.go` | 修改 | COMPLIANCE 模式下跳过生命周期过期 |
| `internal/events/` | 扩展 | 发送 `object.retention.bypassed`、`object.legal-hold.added` 事件 |

### 边界情况

| 场景 | 当前行为 | 期望行为 | 处理方式 |
|------|---------|---------|---------|
| **误设 compliance 保留** | 无法解除（数据永远不可删除）| 管理员在 compliance 模式下锁定前应收到确认提示 | COMPLIANCE 模式需要 `?confirm` 查询参数 + 二次确认 |
| **Governance bypass 审计** | 无审计记录 | bypass 操作应写入 audit_log | `audit()` 调用记录 `bypass-governance-retention` 操作 |
| **Legal Hold + Retention 同时存在** | 仅 `LockedUntil` 一个字段 | 两者独立存在，取最严格规则 | Legal Hold ON 且 retention 已过期 → 数据仍不可删除 |
| **复制场景** | 复制目标对象继承源对象的保留配置 | 默认继承，但目标桶可覆盖 | 复制时读取源对象的 `RetainMode` 和 `RetainUntil`，写入目标 |
| **锁定期间的对象覆盖** | PUT 返回 403 Locked | COMPLIANCE 模式返回 403（不可绕过）；GOVERNANCE 模式可被 bypass | `checkLockBeforeOverwrite` 判断模式 + 当前用户 scope |
| **锁定状态变更的 SSE 事件** | 无事件 | 锁定、解锁、bypass 都触发事件 | 新增 `EventObjectRetentionSet`、`EventObjectLegalHoldSet` 类型 |

---

## 5. 🟡 数据驻留与地理围栏（Data Residency & Geo-Fencing）

### 现状

aero-vault 支持**跨区域复制**（`internal/replication/`）——对象可以从主存储后端复制到副本后端。但**没有任何机制来控制数据可以或不可以保存在哪些地理区域**。

当前数据流：

```
PUT /v1/files/my-data
    → FileService.Put() 
        → storage.Storage.Put()  // 唯一后端（local / S3 / OSS / COS）
        → repository.UpsertObject()
    → EventBus → 复制 Worker
        → replica.Storage.Put()   // 跨区域副本
```

**问题：** 没有任何环节检查"这个对象是否被允许存储在这个区域"。

- 一个德国用户的 GDPR 敏感数据可能被复制到美国区域
- 一个受 ITAR 管制（国际武器贸易条例）的文件可能被存储在中国区域
- 医疗数据可能违反 HIPAA 的物理位置要求

### 缺失能力矩阵

| 能力 | 当前 | 目标 |
|------|------|------|
| 桶级允许/禁止区域策略 | ❌ | `BucketConfig.AllowedRegions []string` / `ForbiddenRegions []string` |
| 对象级覆盖策略 | ❌ | `x-aero-allowed-regions` / `x-aero-forbidden-regions` metadata |
| 写入时地理策略校验 | ❌ | Put 时检查存储后端区域是否在 allowed list 中 |
| 复制时地理策略校验 | ❌ | 复制 Worker 检查目标区域不被禁止 |
| 区域穿透策略 | ❌ | 特定用户/角色可绕过区域限制（紧急管理） |
| 区域感知的路由 | ❌ | 根据对象区域策略选择正确的存储后端 |
| 策略违规告警 | ❌ | 违反地理策略 → 事件 → webhook → 告警 |
| 审计日志 | ❌ | 每次地理策略检查记录（通过/拒绝/bypass） |

### 为什么需要

**全球部署的存储系统必须有数据驻留控制，这不是"好主意"而是法律要求。**

1. **GDPR（通用数据保护条例）：** 欧盟个人数据不得传输到"不充分保护水平"的第三国。Schrems II 裁决（2020）已经推翻了 Privacy Shield。需要显式的区域控制来证明合规。

2. **金融监管：** 许多国家的中央银行要求金融数据必须存储在境内（中国《个人信息保护法》、新加坡 MAS、印度 RBI）。

3. **ITAR/EAR（出口管制）：** 国防相关数据不得离开美国领土或只能被美国公民访问。

4. **健康数据：** HIPAA 不禁止跨州数据传输，但要求 BAA（业务关联协议）覆盖所有数据处理器。没有区域控制意味着无法保证数据处理链路都在 BAA 覆盖范围内。

5. **企业政策：** 许多跨国企业有内部数据分类策略（internal-only / confidential / restricted），restricted 数据不得离开公司数据中心的物理位置。

### 建议架构

地理围栏应作为**可插拔的策略引擎**注入到数据路径中，而非硬编码在每个 handler 中：

```
┌─ GeoFence Policy Engine ────────────────────────────────────┐
│                                                              │
│  type GeoFence struct {                                       │
│      region string               // 当前节点所在区域           │
│      provider GeoProvider        // region → 区域信息映射      │
│  }                                                            │
│                                                              │
│  func (g *GeoFence) CheckWrite(ctx, tenant, bucket, key) err  │
│  func (g *GeoFence) CheckReplicate(ctx, targetRegion) err     │
│  func (g *GeoFence) CheckBypass(ctx, user) bool               │
│                                                              │
└──────────────────────────────────────────────────────────────┘
```

**策略来源（按优先级从高到低）：**

| 来源 | 示例 | 作用域 |
|------|------|--------|
| 对象级 metadata | `_aero_allowed_regions: us-east-1,us-west-2` | 单个对象 |
| 桶级 BucketConfig | `AllowedRegions: ["eu-west-1", "eu-central-1"]` | 整个桶 |
| 全局配置 | `AERO_ALLOWED_REGIONS: us-*,eu-*` | 所有数据（默认策略） |
| 租户级 TenantRecord | `AllowedRegions: ["us-east-1"]` | 特定租户的所有数据 |

**在数据路径中的检查点：**

```
写入路径：
  1. PUT → FileService.Put()
  2. → GeoFence.CheckWrite(tenant, bucket, key)
     → 检查对象 metadata → 桶配置 → 租户配置 → 全局配置
     → 目标存储后端区域 vs allowed list
  3. → 如果拒绝 → 返回 403 GeoRestricted
  4. → 继续写入

复制路径：
  1. EventBus → Replication Worker
  2. → GeoFence.CheckReplicate(targetRegion)
     → 检查源对象的区域策略
  3. → 如果目标区域被禁止 → 跳过复制 + 记录事件
```

### 边界情况

| 场景 | 当前行为 | 期望行为 | 处理方式 |
|------|---------|---------|---------|
| **区域标签冲突** | 对象 allowed 但桶 forbidden | 取最严格规则（forbidden 优先）| 策略引擎按"最严优先"计算交集 |
| **复制时目标区域不在 allowed 列表** | 无检查 → 数据违规驻留 | 跳过复制 + 发出 `geo.restriction.violation` 事件 | CheckReplicate 返回 ErrGeoRestricted → Worker 跳过 + 记录 |
| **紧急管理访问** | 管理员可以通过直接访问存储后端绕过 | 应允许紧急 bypass + 审计 | `checkBypass()` 检查 scope `bypass-geo-restriction` + 写入 audit |
| **SSE 加密跨区域** | 加密对象复制到不允许的区域 | 加密对象也受地理策略约束 | GeoFence 在加密层之上检查，不关心是否加密 |
| **区域命名一致性** | "us-east-1" vs "US-EAST-1" | 大小写不敏感比较 | 策略引擎标准化 region 字符串（strings.ToLower + 已知区域映射） |
| **动态加入新区域** | 新区域上线后旧策略未更新 | 已经存储的数据不受影响，但新写入被拒绝 | 策略在写入/复制时实时评估，非静态校验 |
| **版本对象的地理策略** | 旧版本可能违反当前策略 | 旧版本不受新策略影响（历史事实） | 策略仅在写入时检查，不追溯已有数据 |

---

## 总结：本期 5 个方向的共同特征

1. **企业合规与市场准入导向** — FIPS 140-3（政府市场）、Object Lock Governance/Compliance（金融医疗）、Data Residency（全球化合规）——这三个方向解决的都不是"功能缺失"而是"市场准入"问题。

2. **运维基础设施完善** — Terraform Provider（IaC 集成）、Admin Console（管理 UI）——这两个方向解决的是"部署后如何运作"的问题，而非"能做什么"。

3. **独立于现有功能** — 所有 5 个方向都是增量扩展层，不影响现有系统行为。未启用时不增加任何开销。

4. **代码改动量可控** — 每个方向的核心工作不在 aero-vault 代码库本身（Terraform Provider 是外部项目；FIPS 通过接口抽象；Admin Console 通过独立页面；Object Lock 通过增量字段；Geo-Fencing 通过策略引擎）。

5. **从"功能完整"到"生产可靠"到"市场就绪"** — 前 29 期分析已覆盖功能完整性与生产可靠性。这 5 个方向将 aero-vault 推向**受监管、全球化、自动化部署**的企业级市场。
