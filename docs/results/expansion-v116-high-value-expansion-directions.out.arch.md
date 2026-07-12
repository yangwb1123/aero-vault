现在我已全面理解项目。让我基于所有上下文——`docs/requirements/` 中的两份需求文档、`docs/architecture.md`、`docs/configuration.md`、`CURRENT_SPRINT.md` 和 `AGENTS.md` 中的工程约束——来撰写架构分析。

---

# 架构分析报告：aero-vault 基础设施生态与扩展方向评估

## 1. 架构评估

### 1.1 当前架构优势

**分层清晰，接口边界严明。** 代码库验证了 `doc/architecture.md` 中描述的层次结构：

| 层级 | 实现质量 | 评价 |
|------|---------|------|
| 协议适配层 (REST/S3/WebDAV/MCP) | ✅ 厚度恰当 | 每个适配器都是薄壳，业务逻辑不在 handler 中，符合 `AGENTS.md` 2.2 禁止项 |
| 服务层 (FileService) | ✅ 职责单一 | 依赖仅 `storage.Storage` + `repository.Repository`，无协议耦合 |
| 存储抽象 (Storage) | ✅ 小而完备 | 7 个核心方法 + Multipart 接口，4 个后端实现，factory 模式构造 |
| 元数据抽象 (Repository) | ✅ 中等粒度 | SQLite/Postgres 共享 SQL core，双迁移文件机制（I2 不变量） |
| AI Pipeline | ⚠️ 完全插件化 | 以 `nil` embedder/llm/reranker 安全降级（I5 不变量），零网络可测试 |

**关键设计决策评估：**

| 决策 | 评价 |
|------|------|
| `storageKey(tenant,bucket,key)=path.Join` 单一前缀方案 | ✅ **正确的选择。** 一个 S3 bucket 或本地目录即可服务所有租户和逻辑桶，避免了多 bucket 管理的复杂度。I3 不变量强制 key 验证只在 FileService 层执行，安全 |
| Middleware 链固定顺序 + handler 不自挂链 | ✅ **工程纪律好。** 隔离的 handler 测试不需要 auth/tenant 上下文——这降低了测试门槛，但意味着集成测试必须覆盖 auth chain |
| SQL 占位符 $N → rebind → ? | ⚠️ **合理的权衡。** 跨 SQLite 和 Postgres 的代价；I1 不变量要求每个 bind 独立编号，复杂度由开发者承担，但有测试门禁 |
| 事件总线内存分发 + 可选 Postgres LISTEN/NOTIFY | ✅ **适度的方案。** 单实例足够，多实例通过 `EVENTS_TRANSPORT=postgres` 扩展 |
| AI 组件零网络 mock | ✅ **CI 友好的设计。** `MockLLM` + `HashEmbedder` 让 AI pipeline 在标准 CI gate 中可测试 |

### 1.2 架构局限性

**认知负荷偏高。** 虽然每个层本身清晰，但从需求文档中可以看出，在 136/100 轮分析后仍有大量"全新方向"未被触及。这表明：

1. **文档与代码间的发现鸿沟。** 代码中存在锚点（如 `snapshot.go` 的局限性），但缺少系统化的架构决策记录（ADR）来解释"为什么 snapshot 只支持 SQLite"、"为什么没有自定义域名支持"——新的分析者必须从代码反推。

2. **接口稳定但扩展点缺少显式 SPI。** `Storage` 和 `Repository` 是 Go 接口，但对于"第三方迁移导入器"或"Trino 连接器"这类扩展，当前未有 SPI 级别的契约规范——每个新集成都需要反向工程 repo 层。

3. **配置爆炸趋势。** 当前 50+ 环境变量，方向三（自定义域名）和方向五（分析生态）将再引入 15-20 个变量。缺乏配置分组/分层机制。

### 1.3 架构债务与技术债

| 债务类型 | 位置 | 严重程度 | 建议 |
|---------|------|---------|------|
| **`snapshot.go` 仅支持 SQLite** | `internal/snapshot/snapshot.go` | 🟡 中等 | 它是一个"功能半成品"，如果方向二开始实施，它要么被重构为通用工具，要么被废弃 |
| **BM25 in-process 无持久化** | `internal/ai/bm25.go` | 🟢 轻微 | 重启后重建索引，对于 AI 默认关闭的场景不是问题；但如果 AI 成为主卖点，需要 pgfts 或 Bleve 替代 |
| **无统一的元数据枚举能力** | `storage/local_list.go` vs s3/oss/cos | 🟡 中等 | 每个后端有自己的 list 实现，但没有"全量遍历所有对象"的标准机制——这对方向五（元数据查询 API）是阻塞依赖 |
| **Reconcile 仅支持本地存储清理** | `internal/reconcile/` | 🟢 轻微 | 对 S3/OSS/COS 后端的孤儿 blob 清理未实现 |

---

## 2. 扩展方向评估

### 2.1 方向一：基础设施即代码与存储集成生态

**🔴 P0 — 最高价值方向**

**为什么需要：** 当前 aero-vault 是一个"需要你去 curl 的服务"。缺少 Terraform Provider、K8s CSI、FUSE、CI/CD Action 意味着它无法融入现代基础设施声明式管理流程。对于企业采购，Terraform Provider 往往是必问项——不是可选项。

**核心挑战与解决方案选项：**

```
挑战 1: 多个集成形式的优先级与资源分配
├── 选项 A（推荐）：Terraform Provider + GitHub Action → Q1；K8s CSI → Q3；FUSE → Q4
│   优势：90% 用户价值在 30% 工作量中实现
│   风险：FUSE 被推迟可能影响开源社区采用
├── 选项 B：全部平推 → 同时开工
│   风险：团队分散，每个集成质量都打折扣
└── 选项 C：只做 Terraform Provider，其他委派给社区
    风险：社区可能不活跃，企业用户等待

挑战 2: Terraform 与 S3 API 的状态对齐
└── 方案：所有 Terraform 资源设置 PreventDestroy 标志 + Read 函数的 Refresh 语义
    核心难点：桶策略可同时被 Terraform 和 S3 API 修改 → 需要 State 与 Real World 的调和机制

挑战 3: CSI 的安全模型
└── 方案：每个 PVC → 绑定特定 {tenant, bucket, prefix} → 通过 Secret 注入临时凭证
    风险：CSI NodePublish 需在节点上运行 FUSE 或 http 客户端，引入内核级依赖
```

**对现有系统的影响：**
- Terraform Provider: **零影响**。外部 Go 包，只依赖 Go SDK。可放在独立仓库
- K8s CSI: **中等影响**。CSI 控制器组件需与 aero-vault 通信，可能需要暴露 Token 交换 API
- FUSE: **低影响**。纯客户端库，不修改服务端
- CI/CD Action: **零影响**。独立仓库，封装 SDK 调用

**架构变更建议：**
- 抽象一个 `TokenService` 接口（当前不存在）：用于生成临时、受限的访问凭证，供 CSI 和 FUSE 使用。当前只有 Presign URL，缺失 Pod-level 凭证注入机制
- 考虑将 Terraform Provider 放在独立 repo `terraform-provider-aerovault`，不膨胀主仓库

### 2.2 方向二：租户数据迁移与自助导入导出

**🔴 P0 — 企业入口必备能力**

**为什么需要：** 企业租户的"第一个动作"通常不是 PUT 一个对象，而是"把我的 2TB S3 数据迁移过来"。没有导入能力，企业 POC 就无法完成。`snapshot.go` 当前只支持 SQLite+local FS，对 Postgres+S3 生产部署完全不可用。

**核心挑战：**

```
挑战 1: 源端多样性
├── 选项 A（推荐）：通用导入引擎 StorageImporter 接口
│   实现：Importer 接口 { ListObjects(ctx, prefix) → []ObjectInfo; GetObject(ctx, key) → io.ReadCloser }
│   每个后端 (S3/MinIO/GCS/Azure) 实现一次
│   优势：复用 storage.Storage 的 struct 和测试
│   工作量：~2000 行
├── 选项 B：仅做 S3 导入器
│   优势：最快交付（~500 行）
│   劣势：下个用户问"能导入 GCS 吗"又要开发
└── 选项 C：CLI 工具 + 管道模式
    优势：不修改服务端，通过 CLI 管道迁移
    劣势：无进度追踪无 API，不适合 UI 集成

挑战 2: 迁移中的数据一致性
└── 增量 CDC：对 EventBus 的依赖——源端需支持事件流
    当前 replication.go 是 event-driven 的，但只针对 aero-vault 内部
    外部源的 CDC 需要轮询或源端 webhook 通知

挑战 3: 对象锁/WORM 兼容
└── 必须在迁移脚本中保留 locked_until 字段
    目标端桶需开启版本控制，否则 WORM 对象无法写入
```

**对现有系统的影响：**
- **中等影响**。需要在 Repository 层增加"批量写入"的能力（当前只有单对象 upsert）
- 需要在 Admin API 中新增导入/导出端点
- `snapshot.go` 应被重构为"通用导出器"的其中一个实现

**架构变更建议：**
- 新增 `internal/migration/` 包，包含：
  - `Importer` 接口（通用导入契约）
  - `Exporter` 接口（通用导出契约，替代当前 snapshot.go 的硬编码）
  - `S3Importer`、`LocalFSExporter` 等实现
  - 进度追踪 `ProgressTracker`（持久化到 `jobs` 表或新表 `migrations`）

### 2.3 方向三：自定义域名与静态网站托管

**🟡 P1 — 高价值但可委派**

**为什么需要：** S3 最广泛的用例之一是静态网站托管。缺少这个功能意味着"我可以用 aero-vault 做存储，但还需要一个 Nginx 做网站"。

**核心挑战：**

```
挑战 1: 多监听器 vs 虚拟主机路由
├── 选项 A（推荐）：在同一 HTTP Server 上增加 Host-based 路由
│   实现：在 chi router 顶层增加 hostMatch 中间件 → 根据 Host 头映射到 {tenant, bucket}
│   优势：不需要新端口、不需要新 Server 实例
│   劣势：域名与租户/桶的映射需要持久化存储
├── 选项 B：独立监听器端口
│   优势：安全隔离（网站端口可仅暴露 80/443，管理端口保持内网）
│   劣势：需要第二个 net/http.Server 实例，增加运维复杂度
└── 选项 C：全委派给 Ingress（推荐 MVP 方案）
    在 Ingress 层做 Host 路由 → 同一 aero-vault 后端
    优势：零代码变更
    劣势：自定义错误页、index document 等服务端功能仍需后端支持

挑战 2: HTTPS 自动证书
├── 内建 certmagic → 增加配置复杂度（ACME 邮箱、DNS challenge）
└── 委派给 Ingress + cert-manager → 运维最佳实践

挑战 3: S3 RoutingRules 支持
└── 这对迁移兼容性重要（从 AWS S3 网站迁移时，RoutingRules 需保留）
    工作量可能比核心功能更大
```

**对现有系统的影响：**
- **低-中影响**。核心在服务层，无需修改 Storage 或 Repository 接口
- 需要新增 `bucket_website` 表（`index_document`, `error_document`, `routing_rules` JSON）
- 需要在 `internal/api/s3compat/handler.go` 中新增网站端点处理

**架构变更建议：**
- MVP 阶段（Q2）：仅支持 `index_document` + `error_document` + Host-based 路由 → 2 周
- V2（Q3）：`RoutingRules` 支持
- 明确声明 TLS 不在 scope 内——委派给 Ingress/cert-manager

### 2.4 方向四：性能基准测试套件

**🟡 P1 — 工程基础设施升级**

**为什么需要：** 当前 CI gate 只验证正确性，不验证性能。对于存储系统，性能退化比功能 bug 更难发现——可能过两周才从生产监控中发现"P95 GET 从 2ms 变成了 15ms"。没有基准就没有 SLO，没有 SLO 就没有企业对 SLA 的承诺。

**核心挑战：**

```
挑战 1: 基准环境一致性
├── 选项 A：专用裸机/VM runner（推荐）
│   场景：发布前的完整基准
│   优势：结果可比
│   代价：维护成本
├── 选项 B：CI 容器内基准（推荐 CI 门禁级别）
│   场景：每个 PR 的轻量退化检测
│   优势：自动化
│   劣势：CI 环境的性能波动（CPU 竞争、磁盘 IO 变化）
└── 选项 C：两者结合（推荐完整方案）
    make bench-ci → CI 门禁（快速，退化检测）
    make bench-full → 发布前完整基准（专用环境，涵盖所有场景）

挑战 2: 退化阈值设定
└── 初始阶段："P95 增加 > 2x"作为硬门禁过于严厉
    建议：先建立 2 周的基准基线 → 基于统计分布设定动态阈值（3σ 或 MAD）
    实现：bench/regression/compare.go 需要使用滑动窗口或统计检验

挑战 3: 对象大小分档
└── 1KB / 1MB / 10MB / 100MB / 1GB 各需要独立的基准场景
    大文件（>100MB）的 multipart 上传需要独立测试路径
    这实际上是对 Storage 接口的多态性测试

挑战 4: 破坏性基准的安全隔离
└── 高危测试（如 10K 并发写入、存储容量耗尽测试）
    必须：//go:build benchmark_destructive + 隔离环境标记
    必须：自动恢复机制（清理所有测试数据后基准才算执行成功）
```

**对现有系统的影响：**
- **零影响**。基准套件是独立于主二进制之外的测试代码
- 但需要注意：`internal/integration/fullserver_test.go` 的框架复用应通过代码共享（不导出），而非复制粘贴

**架构变更建议：**
- 在项目根目录新增 `bench/` 目录，与 `internal/` 并列
- 新增 `Makefile` 目标：
  - `make bench-ci`：CI 门禁基准（默认用 SQLite + local FS，仅 small objects）
  - `make bench-full`：完整基准，需要外部依赖（Postgres, S3 兼容端点）
  - `make bench-compare`：对比当前结果与最近一次基线
- 新增 `bench/data/` 目录存储版本化的基准结果（`.json` 格式，含 commit hash + timestamp + 环境指纹）

### 2.5 方向五：对象存储分析生态与数据湖集成

**🔵 P2 — 长期差异化方向**

**为什么需要：** 这是将 aero-vault 从"对象存储"升级为"数据平台底座"的关键。但数据湖生态需要的是开放格式（Iceberg/Delta Lake）、SQL 查询引擎（Trino/Spark）和元数据目录（Hive Metastore/HCatalog），这些都不是小工程。

**核心挑战与战略选择：**

```
战略岔路口：
├── 路线 A（推荐先行）：元数据查询 API
│   实现：POST /v1/search/meta — SQL-like 查询对象元数据
│   工作量：~2-3 周
│   价值：即时可用，无需额外部署
│   代码锚点：StorageClassCounts, BucketStats 已存在
├── 路线 B：Trino 连接器
│   工作量：~4 周
│   价值：分析师可以直接用 SQL 查询
│   依赖：需要稳定的元数据枚举和对象读取 API
├── 路线 C：S3 Select 兼容
│   工作量：~6 周
│   价值：S3 兼容性完整性
│   难点：需要 CSV/JSON/Parquet 解析器 + 类型推断 + 谓词下推
├── 路线 D：Iceberg/Delta Lake 写入
│   工作量：~8-12 周
│   价值：真正的开放数据湖能力
│   风险：Go 的 Iceberg 生态不成熟，可能需自建
└── 推荐：路 A（Q3）→ 路 B（Q4）→ 路 D（明年）

核心架构挑战：
1. 谓词下推 (Predicate Pushdown)
   - 最关键的架构决策：过滤条件发生在哪一层？
   - 正确做法：在 repository 层做 SQL WHERE（针对元数据查询），在 storage 层做文件过滤（针对内容查询）
   - 错误做法：将所有对象加载到内存再过滤
   
2. 元数据与内容的混合查询
   - "查询所有 > 1MB 的 JSON 文件，且 content.name LIKE '%report%'"
   - 元数据部分（size, content.name）→ repository SQL
   - 内容部分（JSON 结构）→ 需要 schema-on-read + 文件扫描
   - 混合查询需要两层结果集的归并
   
3. 开放表格式的二象性
   - Iceberg/Delta Lake 表目录指向存储位置
   - aero-vault 需要暴露"这个 bucket 的某个前缀是一个 Iceberg 表"
   - 需要 Hive Metastore 的 integration 或自建 table catalog
```

**对现有系统的影响：**
- **元数据查询 API**：中等影响。需要在 Repository 层新增 SQL-like 查询能力（当前只有固定方法）
- **Trino 连接器**：低影响。独立 JVM 项目（Trino 插件用 Java），通过 aero-vault REST API 通信
- **S3 Select**：中-高影响。需要在 handler/FileService 层新增文件解析和过滤逻辑
- **Iceberg**：高影响。需要 Go 的 Iceberg 客户端或构建 FFI 桥

**架构变更建议：**
- 方向五应拆分实施，路线 A（元数据查询 API）作为 P2，剩余作为 P3
- 优先暴露查询能力而不是格式支持——用户更关心"能不能查到数据"而不是"数据是不是 Iceberg 格式"
- Trino 连接器可放在独立仓库 `trino-aerovault`，不膨胀主代码库

---

## 3. 接口设计建议

### 3.1 关键模块接口设计原则

基于对代码库的分析和 5 个方向的评估，有以下接口设计原则：

| 原则 | 说明 | 应用场景 |
|------|------|---------|
| **最小接口法则** | 接口越小越稳定。`Storage` 接口 7 个方法是正确范例 | 所有新接口应≤10 个方法 |
| **测试即契约** | contract test 是接口的活文档 | 新 Storage backend 必须通过 `contract_test.go` |
| **选项模式配置** | 函数选项（functional options）优于配置结构体 | `NewImporter(opts...)` 优于 `NewImporter(ImportConfig{...})` |
| **错误码优先** | 使用 sentinel errors 而非 error strings | 已有良好实践（`ErrNotFound`, `ErrBucketNotEmpty`）需延续 |

### 3.2 是否需要引入新的抽象层

| 建议新增的抽象 | 理由 | 优先级 |
|--------------|------|--------|
| **`MigrationImporter` / `MigrationExporter`** | 方向二需要统一导入导出接口，替代 `snapshot.go` 的硬编码 | P0 |
| **`TokenService`** | 方向一的 CSI/FUSE 需要临时凭证生成；当前只有 Presign URL，缺少 `CreateToken(tenant, bucket, prefix, ttl) → Token` 接口 | P1 |
| **`MetaQuery`** | 方向五需要 SQL-like 元数据查询，当前 repository 层只有固定查询方法 | P2 |
| **`WebsiteHost`** | 方向三的域名→桶映射管理接口 | P1 |

### 3.3 向后兼容性

5 个方向对向后兼容性的影响评估：

| 方向 | 兼容性风险 | 缓解措施 |
|------|-----------|---------|
| 方向一 | **低**。所有集成是独立于服务端的新组件 | 无风险 |
| 方向二 | **中**。`snapshot.go` 可能被重构 | 保留 `snapshot.go` 的当前函数签名并标记 `Deprecated`，新代码使用 `migration.Export` |
| 方向三 | **低**。新增 endpoint，不影响现有 | 无风险 |
| 方向四 | **无**。纯测试代码 | 无风险 |
| 方向五 | **中**。新增的元数据查询 API 与现有搜索 API 可能重叠 | `/v1/search/meta` 定义为新路径，`/v1/search` 保持语义/BM25 搜索不变 |

---

## 4. 技术选型

### 4.1 是否需要引入新的技术栈或框架

| 方向 | 需要引入的技术 | 评估 |
|------|--------------|------|
| **Terraform Provider** | `github.com/hashicorp/terraform-plugin-sdk/v2` | ✅ 成熟稳定，最佳实践明确 |
| **K8s CSI Driver** | `k8s.io/csi-api` + CSI spec protobuf | ⚠️ 工作量大，Go 生态成熟但需处理 CSI Identity/Node/Controller 三个 gRPC 服务 |
| **FUSE 挂载** | `github.com/jacobsa/fuse` / `bazil.org/fuse` | ⚠️ 两者都维护良好，但内核 FUSE 接口有平台差异（Linux vs macOS） |
| **Trino 连接器** | Java (Trino SPI) | ⚠️ 需要维护一个 Java 项目，当前团队是 Go 专精。可考虑外包或社区贡献 |
| **Iceberg** | Go Iceberg client (如 `github.com/apache/iceberg-go`) | ⚠️ 生态年轻，可能功能不全；替代方案：通过 REST API 集成已有 Iceberg catalog |
| **性能基准** | Go `testing.B` + `histogram` (如 `HDR Histogram`) | ✅ 无新依赖，Go 标准库 + 轻量库 |

**关于新技术的决策原则：**

```
├── 是否必须与现有系统深度集成？
│   ├── 是 → 优先 Go 生态的技术（保持统一技术栈）
│   └── 否 → 可使用最适合任务的技术
├── 团队是否有该技术的运维经验？
│   ├── 是 → 自建
│   └── 否 → 优先使用成熟 SDK/库，减少自建
└── 是否可以被现有基础设施替代？
    ├── 是 → 不引入新技术（如：HTTPS 用 Ingress，不用内建 certmagic）
    └── 否 → 谨慎引入
```

### 4.2 第三方依赖评估标准

基于 `AGENTS.md` 的 I6 不变量（stdlib 优先），以下为新依赖的引入门槛：

| 标准 | 说明 | 否决条件 |
|------|------|---------|
| **必要性** | 是否有 Go stdlib 替代方案？ | stdlib 可替代 → 不使用新依赖 |
| **维护活跃度** | 最近 6 个月内是否有提交？ | 无活跃维护 → 不使用 |
| **API 稳定性** | 是否 v1.x？ | pre-v1（0.x）→ 慎用或包装适配层 |
| **许可** | 是否为宽松许可（Apache 2.0 / MIT / BSD）？ | GPL/LGPL → 不使用 |
| **间接依赖** | 引入后会拉入多少传递依赖？ | 传递依赖 > 20 个 → 需要论证 |
| **CGO** | 是否需要 CGO？ | 需要 CGO → 与现有纯 Go 构建冲突 |

### 4.3 自建 vs 采购/集成的决策

| 功能 | 建议 | 理由 |
|------|------|------|
| **Terraform Provider** | 自建 | 直接复用 Go SDK，~800 行代码，工作量可控 |
| **Trino 连接器** | 自建或社区驱动 | 维护 Java 项目的成本 vs 价值需要权衡；可先做元数据查询 API，Trino 连接器推迟 |
| **HTTPS 自动证书** | 不内建，委派给 Ingress + cert-manager | 这是 ingress 层职责，不是存储系统职责 |
| **数据湖表格式** | 集成已有方案 | Iceberg/Delta Lake 的 Go 库正在成熟中，初期先通过 REST API 与外部目录服务集成 |
| **性能基准报告** | 自建 | 基于 Go testing 框架扩展，无新依赖 |

---

## 5. 实施路线图

### 5.1 优先级排序（综合评估）

```
优先级评估矩阵（Impact × Effort × Risk × Dependency）

P0 ─ 立即行动
├── 方向一（Terraform Provider + GitHub Action）   Impact: ★★★★★  Effort: ★★☆☆☆
├── 方向二（S3 导入器 MVP）                        Impact: ★★★★★  Effort: ★★★☆☆
└── 方向四（基础基准套件）                           Impact: ★★★★☆  Effort: ★★☆☆☆

P1 ─ 下一批
├── 方向三（静态网站托管 MVP）                       Impact: ★★★★☆  Effort: ★★★☆☆
├── 方向一（K8s CSI Driver alpha）                  Impact: ★★★☆☆  Effort: ★★★★★
└── 方向二（租户导出 API）                          Impact: ★★★★☆  Effort: ★★★☆☆

P2 ─ 长期投入
├── 方向五（元数据查询 API）                         Impact: ★★★☆☆  Effort: ★★★☆☆
├── 方向一（FUSE 挂载）                             Impact: ★★★☆☆  Effort: ★★★★★
├── 方向二（增量 CDC 迁移）                          Impact: ★★★★☆  Effort: ★★★★★
├── 方向三（RoutingRules + HTTPS 自动证书）          Impact: ★★☆☆☆  Effort: ★★★★☆
└── 方向五（Trino Connector + Iceberg 集成）         Impact: ★★★☆☆  Effort: ★★★★★
```

### 5.2 阶段划分与里程碑

```
阶段                                    ┌── Q1 2026 (当前 Sprint 已经 7月)
├── Sprint A (2 周) ─── 方向四M1
│   └── make bench-ci 可用，PUT/GET/List 小对象基准
│
├── Sprint B (2 周) ─── 方向一M1 + 方向四M2
│   ├── Terraform Provider v0.1（桶 CRUD）
│   └── GitHub Action v0.1（上传 + 下载）
│
├── Sprint C (2 周) ─── 方向二M1
│   └── S3 → AeroVault 全量导入器（500 行 MVP）
│
├── Sprint D (2 周) ─── 方向三M1
│   ├── 桶级 index_document / error_document 配置
│   └── Host 头 → {tenant, bucket} 路由
│
├── Sprint E (2 周) ─── 方向四M3 + 方向二M2
│   ├── 退化检测（bench/regression/compare.go）
│   └── GET /v1/admin/export（租户级别导出）
│
├── Q3 持续
├── Sprint F (2 周) ─── 方向一M2 + 方向五M1  
│   ├── Terraform Provider v1（策略 + CORS + lifecycle）
│   └── POST /v1/search/meta 元数据查询 API
│
├── Sprint G-H (4 周) ─── 方向一M3
│   └── K8s CSI Driver alpha（只读支持）
│
└── Q4
    ├── Sprint I (2 周) ─── 方向二M3
    │   └── 增量 CDC 迁移（基于 EventBus）
    ├── Sprint J (2 周) ─── 方向一M4
    │   └── FUSE 挂载 beta（只读 + 最终一致写入）
    └── Sprint K-L (4 周) ─── 方向五M2
        └── Trino Connector v0.1（基本 query + predicate pushdown）
```

### 5.3 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| **Terraform Provider 状态漂移**（用户同时用 Terraform 和 S3 API 修改桶配置） | 高 | 中 | ① 文档明确警告 ② `PreventDestroy` 默认启用 ③ Terraform Read 函数始终从 GetBucket* API 读取最新状态 |
| **导入迁移数据校验失败**（迁移后对象数/字节数不一致） | 中 | 高 | ① 迁移结束时自动执行校验和对比 ② 校验结果记录到 `migrations` 表 ③ 提供 `GET /v1/admin/migrations/{id}/verify` |
| **CSI Driver 节点故障漂移** | 低 | 高 | ① CSI 的 NodeStage + NodePublish 语义天然处理漂移 ② 存储层通过 storageKey 全局定位，不依赖节点本地状态 |
| **性能基准环境噪音**（CI runner 的 CPU/内存竞争导致假阳性） | 高 | 中 | ① CI 门禁使用宽松阈值（50% 退化才阻断） ② 结果存储环境指纹（CPU 型号、内存大小、Go 版本） ③ 历史基线统计建模，使用统计异常检测而非固定阈值 |
| **元数据查询 API 与现有搜索 API 语义混淆** | 中 | 中 | ① 清晰命名区分：`/v1/search` 保持语义/向量搜索，`/v1/search/meta` 做元数据过滤 ② OpenAPI 文档区分两类搜索 |
| **AI 日费用预算与方向二的导入结合**（大规模导入触发大量嵌入，烧穿预算） | 中 | 中 | ① 导入器默认独立于 AI pipeline（不触发嵌入） ② 提供 `--with-indexing` 可选标志 ③ 索引任务通过 JobPool 分批处理，受 `JOBS_WORKERS` 和 rate limit 约束 |

### 5.4 非目标（明确声明推迟）

| 功能 | 推迟原因 | 替代方案 |
|------|---------|---------|
| Docker Volume Plugin | 使用场景窄，社区需求证据不足 | 用户可通过 FUSE 或 CSI 间接实现 |
| 多云存储网关 | 多写一致性需要 Paxos/Raft，系统工程量大 | 当前 replication 覆盖主备场景 |
| 内置 SQL 查询引擎 | 委派给 Trino 连接器更优 | Trino 是行业标准，且团队不需要维护 SQL 引擎 |
| HTTPS 自动证书 | Ingress + cert-manager 是生产最佳实践 | 文档提供配置指南 |
| Iceberg/Delta Lake 表格式写入 | Go 生态不成熟，API 不稳定 | 先做元数据查询 API 和 Trino 连接器，表格式是明年方向 |

---

## 总结

aero-vault 的架构基础坚实——分层清晰、接口稳定、测试友好——但**基础设施集成生态**（方向一）和**企业数据迁移**（方向二）是两个最紧迫的扩展方向，直接影响企业用户的首次体验和采购决策。这两个方向应该在 Q1 优先实施，且对现有系统的侵入性最小。

**性能基准套件**（方向四）虽然对面向客户的价值不直接可见，但它是工程团队持续交付性能质量的基石，建议与方向一并行推进。

**自定义域名与静态网站托管**（方向三）和**对象存储分析生态**（方向五）是高价值的差异化能力，但可以在核心集成能力就绪后有序推进。

**核心架构决策建议：**
1. 新增 `TokenService` 接口 → 为 CSI/FUSE 提供临时凭证注入
2. 新增 `migration.Importer/Exporter` 接口 → 统一导入导出抽象，替代硬编码 `snapshot.go`
3. 新增 `bin/data/` 目录 → 结构化基准结果存储
4. 方向五的 Trino 连接器独立仓库 → 不膨胀主代码库
5. 所有新功能通过 Opt-in 配置守卫（延续 I5 不变量）
