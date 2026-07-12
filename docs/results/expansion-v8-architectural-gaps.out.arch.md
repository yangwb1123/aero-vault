# 架构师分析报告：aero-vault 系统评估与扩展方向

---

## 1. 架构评估

### 1.1 当前架构优势

| 优势 | 依据 | 价值 |
|------|------|------|
| **协议适配器模式清晰** | 四组协议（REST/S3/WebDAV/MCP）均通过 thin handler → FileService 的单向依赖 | 新增协议只需实现 handler，不侵入业务逻辑 |
| **分层可测试性** | FileService 与 Storage/Repository 的接口分离；handler 测试可用 `httptest.NewRecorder` | 单元测试无需启动完整服务 |
| **Opt-in 安全默认** | AI/pgvector/Qdrant/events/cluster 全为 flag-gated；`nil` embedder/llm 不破坏 core CRUD | 基线路径零假设，降低 CI 复杂度 |
| **S3 Policy 置于协议层** | `checkBucketPolicy` 在 S3 handler 层执行 Deny 优先 → 验证通过 | S3 合规路径正确 |
| **Singleflight 分页设计** | ListObjects 使用 keyset pagination（`WHERE key > $4`），避免 OFFSET 性能退化 | 大数据集下性能稳定 |

### 1.2 架构局限性（技术债务）

| # | 问题 | 严重程度 | 根因分析 |
|---|------|---------|---------|
| **L1** | ⚠️ **Policy 执行不一致** — S3 执行 `checkBucketPolicy`，但 REST/WebDAV/MCP 直接绕过 | 🔴 **高** — 安全缺口 | 协议层各自实现授权职责，策略评估未下沉至统一入口 |
| **L2** | ⚠️ **Postgres 连接池未调优** — `sql.Open("pgx", dsn)` 无 `SetMaxOpenConns`/`SetMaxIdleConns`/`SetConnMaxLifetime` | 🟠 **中** — 生产稳定性 | 早期配置模型未扩展；`DBConfig` 仅含 `Driver` + `DSN` |
| **L3** | ⚠️ **ListVersions 使用 OFFSET 分页** — 分析确认第 344 行仍用 `OFFSET $5` | 🟠 **中** — 大型版本库性能 | ListObjects 已修复但 ListVersions 遗漏；版本数可能超过阈值 |
| **L4** | ℹ️ **Snapshot 限于 SQLite + local FS** — 使用 `os.File`/`filepath.Walk` 硬编码 | 🟡 **低** — 可移植性约束 | 设计时仅考虑默认栈；Postgres + S3 场景需新实现 |
| **L5** | ℹ️ **WebDAV 锁与全局锁隔离** — `xwebdav.NewMemLS()` 内存锁不被其他协议识别 | 🟡 **低** — 功能割裂 | 分布式锁管理器尚未引入，属于已知边界 |
| **L6** | ⚠️ **AI 日费用预算仅在 ChatStream 检查** — 缺少 PUT 预检或配额扣减前同步 | 🟡 **低** — 可能超限 | 计费检查点单一，缺少写入前拦截 |

### 1.3 关键设计决策合理性评估

| 决策 | 合理性 | 替代方案权衡 |
|------|--------|-------------|
| FileService 作为唯一入口 | ✅ **合理** — 但 Policy 未下沉违背此原则 | 如果继续在协议层分散检查，新增协议时易遗漏 |
| 迁移双文件（sqlite/postgres 独立） | ✅ **非常合理** — 避免 SQL 方言冲突 | 单文件 + ORM 抽象会引入 N+1 和类型映射成本 |
| Storage key = `path.Join(tenant,bucket,key)` | ✅ **合理** — 不可反解析，GC 精确匹配 | 扁平 key 空间降低 GC 复杂度，但目录模拟需客户端配合 |
| `$N` → `?` 占位符重编号（`s.rebind`） | ✅ **必要但沉重** — SQLite 仅识别 `?` | 可考虑 `pgx` native 与 `sqlite` native 分支，但增加维护成本 |
| Middleware 链顺序固定，handler 不自挂 | ✅ **严格正确** — 隔离测试无 tenant/auth 是设计行为 | 如果打破此规则，第三方扩展可能绕过鉴权 |

---

## 2. 扩展方向

基于已验证的现状评估，尤其是修正后的 Direction #1 发现，我列出以下高价值扩展方向：

### 2.1 [P0] Policy 评估下沉至 FileService — 统一授权模型

**为什么需要：**
- 当前 `checkBucketPolicy` 仅在 S3 handler 执行，REST/WebDAV/MCP 完全绕过
- 一个桶 Policy 声明 `Deny` 某主体，该主体可通过 REST API 写入——这是安全漏洞
- 所有协议共用 FileService，却走不同授权路径——违背架构分层原则

**核心挑战：**
1. **Action 映射定义**：S3 action（`s3:PutObject`）与 REST action（`file.write`）需建立可配置的映射关系，而非硬编码
2. **BucketConfig 获取时机**：当前 S3 handler 在 `checkBucketPolicy` 内调用 `GetBucketConfig`，下沉后 FileService 需自行获取——但 `Put` 路径已调用 `GetBucketConfig`，理论上可复用
3. **错误响应差异**：S3 返回 XML `<Error>` 格式，REST 返回 JSON——FileService 返回通用 error，协议层负责格式转换
4. **性能无增量**：每个请求多一次 Policy 解析 + 评估，但 `GetBucketConfig` 已在路径中——实际代价为零（如果复用结果）

**预期架构变更：**

```
当前:
  S3 handler  → checkBucketPolicy(s3:PutObject) → FileService.Put()
  REST handler ---------------------------------→ FileService.Put()  ← 绕过!

变更后:
  FileService.Put() → evaluatePolicy(ctx, bucket, action) → storage write
                        ↑
                   所有协议统一入口

  其中 evaluatePolicy:
    1. 从 ctx 提取 tenant + action（由协议层注入 action type）
    2. 取 BucketConfig（已在 FileService.Put 路径中获取）
    3. 解析 Policy → auth.Allowed
    4. Deny → 返回 PermissionDenied error
```

**对现有系统的影响：**
- **S3 handler**：删除 `checkBucketPolicy` 调用（~20 行删除）
- **REST handler**：每个路由注入 action type 到 context（~1 行/路由）
- **WebDAV/MCP**：类似注入，总量 < 20 行
- **FileService**：新增 `evaluatePolicy` 方法（~30 行）
- **向后兼容**：无 Policy 的桶行为不变；Policy 评估逻辑不变

> **选项 A：最小化下沉** — 仅将 `evaluatePolicy` 作为 FileService 方法，协议层注入 action + caller identity。优点：改动最小。缺点：协议层仍需处理 action 映射。
>
> **选项 B：Policy Middleware** — 在 Auth 后、FileService 前新增 policy middleware，从路由自动推导 action。优点：零协议层改动。缺点：引入 `GetBucketConfig` 冗余查询（除非 caching）。
>
> **建议选择 A**，原因：改动集中、无性能冗余、无缓存一致性成本。

### 2.2 [P1] Postgres 连接池调优 + 配置化

**为什么需要：**
- 当前 `sql.Open("pgx", dsn)` 使用 Go 的默认连接池配置：`MaxOpenConns=0`（无限）、`MaxIdleConns=2`、`ConnMaxLifetime=0`（无限）
- 生产环境高并发下，无限连接可能耗尽 PgBouncer/pg 连接槽；空闲连接过多浪费资源；连接永不过期可能导致负载均衡器断开
- 这是零代码成本的配置扩展，但影响生产稳定性

**核心挑战：** 极低——纯配置扩展，无逻辑变更

**预期变更：**
```go
// config/config.go
type DBConfig struct {
    Driver string `yaml:"driver"`
    DSN    string `yaml:"dsn"`

    // Postgres 连接池（新增）
    MaxOpenConns    int           `yaml:"max_open_conns"`    // 默认 25
    MaxIdleConns    int           `yaml:"max_idle_conns"`    // 默认 25
    ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"` // 默认 30m
    ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"` // 默认 5m
}
```

**影响：** 仅 `internal/repository/postgres.go` 的 Open 路径 + 配置加载。零 handler/逻辑变更。CI gate 无回归风险。

### 2.3 [P1] ListVersions 从 OFFSET 迁移至 Keyset Pagination

**为什么需要：**
- ListVersions（第 344 行）当前使用 `OFFSET $5`——每次查询扫描所有前面行，版本数达到数十万时性能线性退化
- ListObjects（第 176 行）已使用 `WHERE key > $4 ORDER BY key ASC LIMIT $5`——模式已证明有效
- 版本列举是高频率操作（版本控制 bucke 的 GET 请求）

**核心挑战：**
- **复合排序**：ListVersions 按 `(key, versionID)` 排序，而 versionID 通常由时间戳决定——keyset 需 `WHERE (key, version_id) > ($4, $5)` 形成复合游标
- **cursor 序列化**：当前 API 返回 `next_offset`（数字），需改成 `next_cursor`（带 key + versionID 的 opaque string）
- **向后兼容**：现有客户端使用 `offset` 参数——需同时支持旧参数一段时间

**技术难点：**
```sql
-- 当前（OFTSET）
SELECT ... FROM objects_versions
WHERE bucket_id = $1 AND deleted_at IS NULL
ORDER BY key ASC, version_id ASC
LIMIT $5 OFFSET $4

-- 迁移后（Keyset）
SELECT ... FROM objects_versions
WHERE bucket_id = $1 AND deleted_at IS NULL
  AND (key, version_id) > ($4, $5)   -- 复合游标
ORDER BY key ASC, version_id ASC
LIMIT $6
```

Postgres 支持复合 `(a, b) > (x, y)` 语法。SQLite 也支持 `(a, b) > (x, y)`（3.15.0+）。因此双文件迁移可行。

**影响：**
- SQL 变更：`ListVersions` 查询重写
- 迁移文件：`migrations/{sqlite,postgres}/XXXX_list_versions_keyset.{up,down}.sql`
- API 层：`next_offset` → `next_cursor`（opaque base64 编码的 `key|versionID`）
- REST/S3 响应调整
- **注意**：涉及 break 变更的分页接口，建议使用 `Accept-Version` header 或参数兼容期

### 2.4 [P1] 引入 PII/DLP 上传时预检

**为什么需要：**
- PII 检测当前仅在 AI 索引管线中执行——索引前的对象已完整存储，脱敏发生在索引层面而非存储层面
- 合规场景（PCI-DSS、HIPAA、GDPR）要求在数据**写入**时即进行检测/阻断/脱敏
- 上传时 PII 检测可触发 WORM 锁定、审计告警、或自动隔离

**核心挑战：**
1. **性能**：PII 检测（尤其是正则 + Luhn）是全量扫描——大文件（>100MB）无法在线执行
2. **流式 vs 缓冲**：当前 `PUT` 是流式写入 Storage——检测需要完整内容或分块流式检测
3. **策略可配置**：不同租户可能需要不同 DLP 规则（如 App1 禁止信用卡、App2 禁止 SSN）

**技术方案选项：**

> **选项 A：同步预检（≤ 阈值文件）** — 对 ≤ 1MB 的文件在 PUT 路径执行同步 `PiiDetector.Scan`；超大文件跳过或异步入队
> - 优点：写入前拦截高敏感数据
> - 缺点：流式场景需 buffer；大文件无覆盖
>
> **选项 B：异步后检（所有文件）** — PUT 完成后发布 `object.created` 事件 → DLP worker 异步扫描 → 发现敏感则更新 metadata 或触发隔离
> - 优点：不阻塞写入路径，适用于任意大小
> - 缺点：敏感数据写入后才被发现（有 window）
>
> **建议：A+B 组合** — 小文件同步预检 + 大文件异步后检 + 可配置阈值

**预期架构变更：**
- FileService 新增 PII 配置 + 扫描方法（复用现有 `PiiDetector`）
- EventBus 新增 `dlp.violation` 事件类型
- BucketConfig 新增 `dlp_rules` 字段（json）
- 新增 DLP Worker（可选）

### 2.5 [P2] 分布式锁管理器（跨协议协作写入）

**为什么需要：**
- WebDAV 使用独立内存锁；S3 的 `x-amz-object-lock` 依赖 WORM 字段而非分布式锁
- 当 WebDAV 锁定文件进行编辑时，REST/S3 可绕过锁直接覆盖——导致数据损坏
- 分布式锁是实现一致性的基础（多节点、多协议）

**核心挑战：**
1. **后端选择**：Postgres advisory lock（PG only）、Redis、etcd——SQLite 节点无法支持
2. **租约机制**：WebDAV 锁有 timeout，需心跳续租；网络分区时需防 split-brain
3. **锁粒度**：文件级锁 vs 路径前缀锁 vs 桶级锁——粒度过细增加开销
4. **锁定协议共享**：WebDAV LOCK/UNLOCK 需要通过 `DistributedLocker` 接口而非本地 map

**建议：**
- 定义 `DistributedLocker` 接口（Lock/Unlock/Renew/GetLock）
- 默认实现：基于 Postgres `pg_advisory_lock`（仅 Postgres 场景）
- WebDAV handler 使用此接口替代 `NewMemLS`
- SQLite + local FS 场景退化为内存锁（一致性不保证）

**优先级放低（P2）**，原因：
- WebDAV 本身为 opt-in（`WEBDAV_PREFIX`）
- 跨协议写入冲突的实际发生频率取决于部署模式
- 实现成本相对较高

---

## 3. 接口设计建议

### 3.1 FileService 接口演化原则

当前 FileService 是 go struct 而非 interface。建议为 **Policy 评估**引入接口抽象而非 struct 方法——避免 FileService 膨胀为上帝对象。

```
type PolicyEvaluator interface {
    Evaluate(ctx context.Context, tenant, bucket, action string, subject Subject) error
    // 返回 nil (Allow) 或 *PermissionDeniedError
}
```

**原则：**
1. **窄接口（RISP）** — `PolicyEvaluator` 只做一件事：评估 Policy
2. **依赖注入而非方法调用** — `FileService` 通过构造函数接收 `PolicyEvaluator`，而非自己实现
3. **error 驱动** — 返回 Go 标准 error，类型断言区分 Deny 与内部错误
4. **Context 传播** — tenant/subject/action 通过 ctx value 传递，避免方法签名膨胀

### 3.2 BucketConfig 缓存接口

当前每个请求可能多次调用 `GetBucketConfig`（S3 handler 一次，FileService `Put` 又一次）。建议新增缓存层接口：

```go
type BucketConfigCache interface {
    Get(ctx context.Context, tenant, bucket string) (*BucketConfig, error)
    Invalidate(ctx context.Context, tenant, bucket string)
}
```

- 默认实现：`CacheFirstConfig`（先查内存缓存，miss 则查 DB，TTL 30s）
- 简单实现：使用 `sync.Map` + TTL，无需 Redis
- 当 Policy 下沉后，此举消除重复查询，同时避免中间件级缓存的一致性问题

### 3.3 新增动作枚举

定义统一的 Action 类型枚举，跨越协议边界：

```go
type Action string

const (
    ActionObjectRead   Action = "object:read"    // GET/HEAD
    ActionObjectWrite  Action = "object:write"   // PUT/POST/MKCOL
    ActionObjectDelete Action = "object:delete"  // DELETE
    ActionObjectList   Action = "object:list"    // GET bucket/list
    ActionObjectLock   Action = "object:lock"    // LOCK/WORM
    // ... 可选扩展
)
```

**S3 action → 统一 action 映射**可在协议层完成（写死的 map），而非在 FileService 内做 if/else。

### 3.4 向后兼容策略

| 变更 | 兼容策略 |
|------|---------|
| Policy 下沉 | S3 handler 删除 `checkBucketPolicy` 前，先在 FileService 实现，S3 调用通过开关控制（`POLICY_IN_FILE_SERVICE=true`），并行运行双路径，观测无回归后移除旧路径 |
| ListVersions keyset 迁移 | 旧 `offset` 参数保留，内部转换为 keyset；新 `cursor` 参数优先；文档标记旧参数 deprecated |
| 分布式锁 | WebDAV 通过 `DistributedLocker` interface 替换 `NewMemLS`，性能无损；SQLite 节点使用 mem 实现不变 |
| Postgres 连接池 | 纯新增 config 字段，默认值保持当前行为（零改动） |

---

## 4. 技术选型

### 4.1 是否引入新技术栈

| 候选 | 评估 | 结论 |
|------|------|------|
| **Redis**（缓存 + 锁） | R1: 分布式锁需要 → 但 Postgres advisory lock 可替代（如果已用 PG）<br>R2: BucketConfig 缓存 → `sync.Map` + TTL 已足够<br>R3: 引入 Redis 增加运维复杂度 | **不需要** — Postgres 节点用 PG advisory lock；SQLite 节点用内存锁 |
| **etcd**（分布式协调） | 锁协调 + 租约 + 集群单例 → 但当前 PG `leases` 表 + `advisory lock` 已覆盖 | **不需要** — 过度设计 |
| **OpenFGA / OPA**（细粒度授权） | Policy 评估可完全由 OPA Rego 策略引擎实现 → 但当前 `auth.ParsePolicy` + `auth.Allowed` 已足够，OPA 引入新语言依赖 | **不建议** — 当前自建策略引擎满足需求；OPA 可作为未来扩展 |
| **pgvector**（向量搜索） | 已在 codebase 中支持（`AI_VECTOR_BACKEND=pgvector`） | ✅ **已有** |
| **Memcached**（AI 检索缓存） | 当前 `AI_SEARCH_CACHE_SIZE` 使用内存 map → 生产可扩展至 Memcached | 未来选项，当前不迫切 |

**结论**：当前阶段**无需引入新技术栈**。现有 Postgres（或 SQLite）已覆盖锁、缓存、配置等需求。

### 4.2 第三方依赖评估标准

基于 AGENTS.md 中的 **I6（Stdlib 优先）** 原则，新依赖必须满足：

| 标准 | 说明 |
|------|------|
| **必要性** | Stdlib + 现有 codebase 无法合理实现，且不引入该依赖导致代码膨胀 ≥ 200 行 |
| **稳定性** | GitHub stars ≥ 1000, 最近 release ≤ 12 个月, 无已知 CVE |
| **API 稳定性** | `go.mod` 版本 ≥ v1（对于 Go 库）；无 breaking change 历史 |
| **大小** | 不引入传递依赖树的子树（避免 `minio-go` 级别的大块头） |
| **License** | Apache-2.0 / MIT / BSD；不引入 GPL/LGPL 到主 binary |

### 4.3 自建 vs 采购决策矩阵

| 功能 | 自建成本 | 外部方案 | 推荐 |
|------|---------|---------|------|
| Policy 引擎 | 低（已有 `auth.ParsePolicy` + `auth.Allowed` 代码） | OPA（Rego），Casbin | **自建** — 已有 80% 代码 |
| PII 检测 | 中（已有 `PiiDetector` + Luhn，需扩展规则配置） | AWS Macie，Microsoft Purview | **自建** — 合规成本低；外部方案需网络调用 + 密钥管理 |
| 分布式锁 | 中（需实现 PG advisory lock + WebDAV 集成） | Redis Redlock，etcd concurrency | **自建** — PG advisory lock 零运维 |
| 审计日志分析 | 高（需要聚合查询、Dashboard） | Grafana Loki，ELK | **外部** — 审计日志已写入 `audit_log` 表，但分析平台非核心竞争力 |
| Webhook 重试 | 低（已有 `webhook_failures` 表 + durable retry） | AWS SNS，GCP PubSub | **自建** — 成熟代码 |

---

## 5. 实施路线图

### 5.1 优先级排序与阶段划分

```
Phase 1 (Sprint 1-2) │ Phase 2 (Sprint 3-4) │ Phase 3 (Sprint 5-6)
──────────────────────┼──────────────────────┼──────────────────────
P0: Policy 下沉       │ P1: ListVersions     │ P2: 分布式锁管理器
                      │   Keyset 迁移        │
P1: Postgres 连接池   │                      │ P2: Snapshot 跨后端
                      │ P1: PII 上传预检     │
                      │                      │
                      │ P1: BucketConfig     │
                      │   缓存层             │
```

### 5.2 Phase 1 详细里程碑（Sprint 1-2）

| 里程碑 | 交付物 | 验收标准 | 工作量 |
|--------|--------|---------|--------|
| M1: Policy 接口定义 | `PolicyEvaluator` 接口 + `Action` 枚举 + `Subject` 类型 | 编译通过；接口文档 | 1-2 day |
| M2: FileService 集成 | `FileService` 接收 `PolicyEvaluator`；`Put`/`Get`/`Delete`/`List` 路径调用 `evaluate` | 单元测试覆盖 4 条路径；Deny → `PermissionDenied` | 2-3 day |
| M3: 协议层 action 注入 | REST/S3/WebDAV/MCP handler 注入 action type 到 context | S3 handler 删除 `checkBucketPolicy` 后 behavior test 通过 | 1 day |
| M4: 双路径并行验证 | `POLICY_IN_FILE_SERVICE` flag；S3 handler 同时保留新旧路径 | 观测 metrics 确认双路径行为一致 | 1 day |
| M5: 清理 | 删除 S3 handler 旧路径 `checkBucketPolicy`；删除 flag | CI gate 全绿；无 behavioral regression | 0.5 day |
| M6: Postgres 连接池 | `DBConfig` 新增字段 + `postgres.go` 应用 | 配置生效；测试默认值一致 | 0.5 day |

### 5.3 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| **R1: Policy 下沉破坏现有 S3 行为** | 低 | 高 | Phase 1 使用双路径 flag 并行运行，观测回归再切换 |
| **R2: Action 映射遗漏某个 S3 action** | 中 | 中 | 写 unit test 遍历所有 S3 handler route → action map，确保全覆盖 |
| **R3: ListVersions keyset 迁移导致边缘分页错误** | 中 | 中 | 迁移前写边界测试（空结果、单页、跨页、重复 key 的多版本）；使用 `Accept-Version` header 提供 1 sprint 兼容期 |
| **R4: PII 同步检测拖慢上传 P99** | 中 | 中 | 设文件大小阈值（默认 1MB）；超大文件自动降级为异步；使用 `testing.Benchmark` 验证 |
| **R5: 连接池配置不当导致 PG 连接暴涨** | 低 | 高 | 默认值保守（25/25/30m/5m），文档建议从监控调整；加入 unit test 验证配置加载 |

### 5.4 不做清单（明确排除）

以下方向有提议但架构评估建议**本阶段排除**：

| 方向 | 排除原因 | 何时重新评估 |
|------|---------|-------------|
| OPA/Casbin 策略引擎 | 现有 `auth.ParsePolicy` + `auth.Allowed` 已满足；OPA 增加新语言依赖和部署复杂度 | 当 Policy 语法需要支持 Rego 的复杂规则（如 ABAC 跨属性评估）时 |
| Redis/Memcached 缓存层 | `sync.Map` + TTL 对单节点足够；PG 节点使用 advisory lock | 当部署为多副本集群且 `GetBucketConfig` 成为热点时 |
| gRPC 协议适配 | 当前 REST/HTTP 已覆盖所有客户端 | 当内部服务间吞吐成为瓶颈时 |
| 多区域数据分布 | CORS 级别；不是当前需求 | 当业务强制要求多区域就近读取时 |

---

## 总结

本次架构分析基于验证报告的关键修正——**Direction #1 的 Policy 应从「新增中间件」重新聚焦为「下沉至 FileService」**——提出了可执行的分阶段实施路线图。

**关键差异化建议：**

1. **不要新增中间件层**，而是将已存在的 `checkBucketPolicy` 逻辑下沉至 FileService——改动小、覆盖所有协议、无缓存一致性问题
2. **不要引入 Redis/etcd** 解决分布式锁，Postgres advisory lock 已覆盖 PG 场景；SQLite 场景的跨协议写入冲突是已知边缘，可通过文档约束
3. **不要使用 OPA/Casbin** 替代现有 Policy 引擎——自建代码已满足基础需求，OPA 增加复杂度无对应收益
4. **PII 检测采用 A+B 组合**（小文件同步 + 大文件异步），而非只做同步或只做异步——平衡安全性与吞吐

最高优先级工作：**Policy 下沉**（Phase 1）。预计 5-8 人天完成。这是唯一的安全缺口，且现有代码提供了路径捷径。
