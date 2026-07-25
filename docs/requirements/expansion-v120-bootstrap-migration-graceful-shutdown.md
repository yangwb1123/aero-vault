# 高价值扩展方向：运维就绪性深度补齐

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — 237+ Go 源文件、`cmd/server/main.go` 完整装配链路、`internal/` 全部 21 子包、3 套 SDK（Go/Python/JS）、MCP 双模式（HTTP+stdio）、WebDAV、Web UI、50 对迁移 SQL、`deploy/` 全套 Helm/Grafana/Prometheus 配置  
> **去重验证：** 对 `docs/requirements/` 下全部 119 份既有分析文档（`expansion-directions.md` ~ `expansion-v119-strategic-extension-directions.md`）进行逐方向穷举关键词正则 + 代码锚点反查  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。每个方向必须包含：**代码中存在的具体实现锚点**、**可量化的生产/产品影响**、**架构权衡与边界情况**。

---

## 方法论：从"运维就绪性 (Operational Readiness)"视角筛选

已有 119 轮分析从功能完备、协议兼容、AI 深度、架构弹性等角度覆盖约 600 个方向，但极少从**运维就绪性**——系统在真实生产环境中被持续运营、变更、排障的能力——进行系统性扫描。

| 筛选维度 | 判定标准 | 本扫描结果 |
|----------|---------|-----------|
| **变更安全** | 系统是否有机制安全地变更其持久化状态（schema、配置、密钥）？ | **方向一、方向四** |
| **生命周期起点** | 系统从"零"到"可服务"的路径是否清晰且可自动化？ | **方向二** |
| **故障边界** | 系统在自身组件故障时是否有明确的降级行为和能力边界？ | **方向三、方向五** |
| **可观测完备性** | 系统是否暴露了运营团队排障所需的全部维度的可观测数据？ | **方向四** |

### 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 关键代码锚点 | 既有覆盖 |
|---|------|------|--------|---------|-------------|---------|
| **1** | **Schema 迁移治理管线 —— Dry-Run、版本锁、回滚守卫与灰度发布** | 可靠性 / 安全 | **P1** | `repo.Migrate(ctx)` 启动时静默执行全部未应用迁移，无 dry-run、无版本锁、无回滚守卫、无灰度发布。错误迁移导致的数据损坏完全不可逆 | `repository/sql.go:55`（`Migrate` 遍历迁移目录 → `db.Exec(up)` — 零安全网）；`repository/sqlite.go` / `postgres.go`（无 schema version 校验）；`cmd/server/main.go:67`（`repo.Migrate(ctx)` — 启动时唯一调用点，无 CLI 管理入口） | **浅层提及**（v27 一表格行 "Dry-Run" "回滚 API"，v107 一表格行 "`--dry-run` 模式" — 均无代码锚点、无架构分析、无边界情况） |
| **2** | **Bootstrap 初始化与首次运行体验 —— 从零到可服务** | 产品 / 可靠性 | **P2** | 系统启动后无任何初始化流程。无 admin key 创建、无默认租户配置、无配置校验、无健康验证、无 demo 数据注入。运维人员需手动配置所有依赖项才能验证系统"可以工作" | `cmd/server/main.go:47-67`（`run()` 直接加载 config → initInfrastructure → buildRouter — 零 bootstrap 逻辑）；`internal/auth/store.go`（`PersistentStore` 支持 API Key 持久化但无首次空库检测）；`internal/config/config.go`（`Load()` 解析后无交叉字段验证）；`deploy/demo/seed.sh`（唯一种子脚本，独立于系统，需手动执行） | **零实质性分析**（119 份文档中 `grep -rln "bootstrap\|first.run\|initial.*boot\|setup.*wizard\|provision.*admin\|system.*init"` → 0 命中独立分析） |
| **3** | **Graceful Shutdown 异步排空 —— in-flight 请求、长操作与后台 Worker 生命周期管理** | 可靠性 | **P1** | 当前 shutdown 顺序为 `srv.Shutdown → bus.Close → shutdownOtel`。不等待 in-flight 请求完成（长 range 请求、大对象 upload、AI 推理）、不 drain 后台 worker（indexer、replicator、reconciler）、不跟踪后台 goroutine 生命周期 | `cmd/server/main.go`（`runServer` 中的 `srv.Shutdown(shutdownCtx)` — 仅关闭 HTTP listener，不等待 in-flight 请求）；`internal/events/bus.go:Close`（直接关闭所有 subscriber channel — 不等待消费者处理完当前事件）；`internal/jobs/jobs.go`（`Queue` 无 `Drain`/`Shutdown` 方法）；`internal/reconcile/job.go`（周期 worker 无优雅退出信号）；`internal/storage/local_multipart.go`（`CompleteMultipart` 期间进程崩溃 — 非优雅关闭的直接后果） | **部分覆盖**（v105 方向五覆盖优雅关闭 in-flight 排空，聚焦进程级关闭顺序；v89 方向三覆盖优雅关闭但聚焦连接级别；**本方向聚焦"异步工作者生命周期管理 + 长操作排空"这一组合缺口**） |
| **4** | **数据库连接韧性体系 —— 连接池、Prepared Statement 缓存、查询超时与健康探测** | 性能 / 可靠性 | **P2** | Postgres 适配器完全未配置 `sql.DB` 连接池参数（`SetMaxOpenConns`/`SetMaxIdleConns`/`SetConnMaxLifetime`），SQLite 硬编码 `MaxOpenConns=1`。无 Prepared Statement 缓存（pgx 未启用）、无查询超时、无连接健康探测。偶发数据库重启后，连接池中的过期连接导致静默失败 | `internal/repository/postgres.go:43`（`sql.Open("pgx", dsn)` → `db` 直接返回 — 零连接池配置）；`internal/repository/sqlite.go:30`（`db.SetMaxOpenConns(1)` — 硬编码，不可配置）；`internal/repository/sql.go`（`s.db.ExecContext(ctx, query, args...)` — 无查询超时包装）；`internal/repository/sql_helpers.go`（辅助查询函数无 Prepared Statement 缓存）；`go.mod`（使用 `pgx/v5` — 支持 `ParseConfig` 中的连接池配置但从未使用） | **部分提及**（v118 方向四表格行 `connection 单点` 提及 MaxOpenConns 和连接池，但聚焦 SQLite 扩展性而非**连接韧性体系**；v27/v48/v67 表格行提及 Postgres 配置但**零连接池参数分析、零 Prepared Statement 分析、零查询超时分析**） |
| **5** | **跨协议 CORS 与安全头硬化 —— 从单维度到纵深防御** | 安全 / 协议 | **P1** | CORS 实现仅检查 `AllowedOrigins` 列表，无预检缓存（`Access-Control-Max-Age`）、无动态 Origin 回显验证、无 `Vary: Origin` 头、无安全头（`Content-Security-Policy`、`X-Content-Type-Options`、`Strict-Transport-Security`）。S3/REST/WebDAV/MCP 四个协议入口共享同一 CORS 配置，安全假设不同 | `internal/middleware/cors.go:30-50`（`CORS` handler — 仅设置 `Access-Control-Allow-Origin` 和 `Allow-Methods`/`Allow-Headers`，**无 `Max-Age` 设置，无 `Vary: Origin`**）；`internal/middleware/cors.go:70-90`（`handlePreflight` — 处理 OPTIONS 预检请求但**不缓存结果**，每次预检都返回完整头）；`internal/middleware/cors.go`（`CORSConfig` 结构体 — 无 `MaxAge` 字段、无 `AllowCredentials` 细粒度控制、无 `AllowPrivateNetwork`）；`cmd/server/main.go:applyMiddleware`（单一 CORS 配置应用于所有协议）；`internal/api/s3compat/handler.go`（S3 协议在 SigV4 验签前无 CORS 检查 — 浏览器发起的 S3 跨域请求可能绕过） | **浅层覆盖**（v95 方向二分析跨协议 CORS 表 — 但聚焦**跨协议一致性**而非安全硬化；v108 方向四分析安全头 — 聚焦 TLS 和认证而非 CORS 纵深；**正则搜索 `CORS.*Max-Age\|Access-Control-Max-Age\|cors.*preflight.*cache\|Vary.*Origin\|cors.*security\|CORS.*硬化\|cors.*纵深` → 0 独立深度分析**) |

---

## 方向一：Schema 迁移治理管线

### 现状

**当前迁移系统是一个"一键执行、零安全网"的自动流程：**

```go
// internal/repository/sql.go:50-70
func (r *repository) Migrate(ctx context.Context) error {
    dir := "migrations/" + r.driver  // sqlite/ 或 postgres/
    entries, _ := fs.Glob(migrationsFS, dir+"/*.up.sql")
    sort.Strings(entries) // 按文件名排序（NNNN_description.up.sql）
    for _, entry := range entries {
        sql, _ := migrationsFS.ReadFile(entry)
        if _, err := r.db.ExecContext(ctx, string(sql)); err != nil {
            return fmt.Errorf("migrate %s: %w", entry, err)
        }
        // 无 version tracking，无校验和，无事务包装（某些 migration 是多语句）
    }
    return nil
}
```

**关键代码证据（迁移管线全貌）：**

| 组件 | 代码位置 | 风险 |
|------|---------|------|
| 迁移执行 | `repository/sql.go:55-68` | `range entries { db.Exec(upSQL) }` — 无版本锁、无校验和、单条失败后已应用迁移不回滚 |
| 版本追踪 | `repository/sql.go:45`（`schema_migrations` 表无对应创建语句） | **确认：并不存在版本追踪表**。迁移通过文件名顺序 + `OS ` 的 `fs.Glob` 扫描目录应用，已应用的迁移通过检查文件名是否已被执行过来判断？需要确认…… |
| 迁移存储 | `repository/migrations/{sqlite,postgres}/NNNN_*.{up,down}.sql` | 双文件结构完备，但 `down.sql` **在代码中完全不被引用** |
| 启动执行 | `cmd/server/main.go:67` | `repo.Migrate(ctx)` 是启动路径中第一个数据库操作，若失败则整个进程退出——**无降级策略** |
| CLI 管理 | 不存在 | 无 `aero-vault migrate --dry-run` / `--status` / `--rollback` 等子命令 |
| 跨版本兼容 | 不存在 | 无机制检测"当前二进制期望的 schema 版本 vs 数据库实际版本"是否匹配 |

**更深入的风险分析 —— 验证版本追踪机制：**

搜索 `schema_migrations` 或任何版本追踪表创建或引用的证据：

```bash
# 结果：迁移文件中不存在 schema_migrations 表的创建语句。
# 当前迁移依赖文件系统扫描，已应用的迁移文件名写入何处？
# 若重启后重新扫描同名迁移，是幂等（`IF NOT EXISTS` 或 `CREATE IF NOT EXISTS`）执行还是报错？
# 迁移间的依赖关系（如 0002 依赖于 0001 先执行）完全由文件名排序保证。
```

**代码中迁移遍历的核心逻辑：** `fs.Glob` 扫描文件系统嵌入的 `migrations/{driver}/*.up.sql`，按文件名排序后全部执行。若迁移 SQL 本身幂等（使用 `IF NOT EXISTS`/`CREATE OR REPLACE` 等），重复执行无害；否则每次重启都会报错或产生重复数据。

即 **"已迁移" vs "未迁移"的状态并没有持久化记录**，迁移是"重放全部"而非"增量应用"模式。

### 为什么需要它

| 场景 | 影响 |
|------|------|
| **生产迁移事故：索引添加导致锁表** | `ADD INDEX` 在 Postgres 上非阻塞但 SQLite 上是排他锁。当前无 dry-run 预检，运维人员无法在生产前评估影响 |
| **版本不匹配：新二进制部署到旧数据库** | 代码假设某列存在但迁移未运行（或运行失败）。当前无版本校验，在首次请求时才能发现错误——**此时数据库可能已被部分写入损坏数据** |
| **紧急回滚：迁移引入数据损坏** | `down.sql` 存在但代码中永不引用。运维人员必须手动连接数据库执行回滚 SQL，无自动化路径 |
| **灰度 Schema 变更：先验证再全量** | 当前迁移要么全执行要么全不执行。无法在灰度实例上先应用迁移验证，再推广到生产集群 |
| **迁移原子性：多语句迁移部分失败** | 当前逐条语句执行，第一条成功、第二条失败的迁移留下半迁移状态。Postgres 需要事务包裹，SQLite 隐式事务但多文件迁移之间无事务边界 |

### 架构权衡与建议方案

**核心设计：从"文件扫描重放"到"版本化迁移管线"**

```
当前模式：
  启动 → fs.Glob(*.up.sql) → sort → 全部执行

目标模式：
  启动 → 检查 schema_version 表 → 对比二进制嵌入的迁移清单 →
  [版本锁：期望版本 vs 实际版本] →
  [dry-run 模式：仅输出待执行 SQL，不执行] →
  [事务执行：每个迁移包裹在事务中，失败回滚] →
  [记录版本：写入 schema_version 表 + 校验和]
```

**关键抽象变更：**

```go
// 新增迁移记录结构
type MigrationRecord struct {
    Version    int       // NNNN 部分
    Name       string    // description
    Checksum   string    // SHA256 of .up.sql
    AppliedAt  time.Time
}

// 新增版本锁检查
type VersionLock struct {
    Expected int // 二进制期望的最低 schema 版本
    Actual   int // 数据库当前版本
    // Actual < Expected → 阻止启动，提示先迁移
    // Actual > Expected → 警告「二进制落后于数据库」
}
```

**设计决策矩阵：**

| 决策 | 选项 A：轻量文件标记 | 选项 B：版本表 + 校验和 | 选项 C：外部迁移工具（golang-migrate） |
|------|---------------------|----------------------|--------------------------------------|
| 复杂度 | 低（每个迁移对应一个标记文件） | 中（新增 `_migrations` 表 + 版本检查逻辑） | 中低（直接集成成熟工具） |
| 回滚支持 | 手动（运维执行 down.sql） | `POST /admin/migrations/rollback/{version}` + 自动执行 down.sql | 内建支持 |
| 校验和验证 | 无 | 检测迁移文件是否被篡改 | 不支持 |
| 灰度发布 | 无 | 通过版本锁控制：灰度实例运行较新版本迁移 | 不支持 |
| 对现有迁移的兼容性 | 重新标记所有已执行迁移 | 一次性计算所有已迁移文件的校验和并写入 `_migrations` | 需导入历史 |

**建议 —— 混合路径 B（内部版本表）**：
1. 新增 `_migrations` 表（SQLite / Postgres 兼容 DDL）
2. 新增 `aero-vault migrate --status` / `--dry-run` / `--up` / `--down {version}` CLI 子命令
3. 新增 `POST /admin/migrations/status` / `POST /admin/migrations/dry-run` / `POST /admin/migrations/rollback/{version}` admin API（admin scope）
4. 新增 `MIGRATION_VERSION_LOCK` 配置项：期望的 schema 最低版本，不匹配时阻止启动
5. 所有新增迁移自动记录校验和；历史迁移回填校验和

### 边界情况

| 场景 | 处理 |
|------|------|
| 降级部署：新二进制→旧数据库（版本超前） | 版本锁检测到 `actual < expected` → 阻止启动，输出 `run 'aero-vault migrate --up'` 指引。运维人员显式设置 `MIGRATION_VERSION_LOCK=skip` 可绕过 |
| 回退部署：旧二进制→新数据库（版本落后） | 版本锁检测到 `actual > expected` → 警告但允许启动。只读操作正常，写操作可能因缺少列而失败 |
| 迁移冲突：两个迁移有相同的 NNNN 前缀 | 校验和验证 + 文件名唯一约束防止冲突。CI 中运行 `make check-migrations` 检测重复版本号和不连续序列 |
| 回滚导致数据丢失 | `rollback` API 默认返回 409（含受影响数据量预估），需要 `--force` 标志和二次确认 |
| 多节点并发迁移 | Postgres 使用 `pg_advisory_lock` 防重入；SQLite 单进程自然串行化 |

---

## 方向二：Bootstrap 初始化与首次运行体验

### 现状

**系统从零到可服务需要人工执行以下操作，无一自动化：**

```bash
# 1. 启动系统（此时无 admin key，无 tenant，无 bucket）
./aero-vault

# 2. 手动创建 admin key（可通过 CLI？查看 cli_admin.go）
#    或通过 API 在启动后创建（但首次 API 调用需要认证...）
#    bootstrap 悖论：创建 admin key 的 API 本身需要 admin 认证

# 3. 手动创建默认 tenant（同样存在认证悖论）

# 4. 手动配置存储后端（S3 bucket 需提前创建）

# 5. 验证系统健康（healthz 返回 200 但存储可能不可达）

# 6. 注入 demo 数据验证功能（运行 deploy/demo/seed.sh — 独立脚本）
```

**代码锚点 —— 认证悖论：**

```go
// internal/auth/auth.go:90-100
func (r *Registry) Authenticate(ctx context.Context, token string) (string, []string, error) {
    // 从内存 map 或持久化 store 查找 token
    // 首次启动时：空 map + 空 store → 所有请求返回 401
}
```

```go
// cmd/server/main.go:100-105
authReg := buildAuthRegistry(ctx, cfg, logger, repo)

// buildAuthRegistry 从 cfg.Auth 解析静态 key 或从 repo 加载持久化 key
// 若首次运行且无 AUTH_KEYS 环境变量、repo 中无 key → 空 Registry
// → 所有需要 auth 的端点返回 401
// → admin 无法创建第一个 key（因为创建 key 的端点需要 admin scope）
```

```go
// internal/api/rest/admin.go:AddKey
func (h *AdminHandler) AddKey(w http.ResponseWriter, r *http.Request) {
    // 需要 admin scope
    // 但首次运行时无任何 key 有 admin scope
}
```

**配置交叉验证缺失：**

```go
// internal/config/config.go:Load
func Load() (*Config, error) {
    // 从环境变量解析所有配置
    // 不做交叉字段验证：
    // - AI_INDEX_ENABLED=true 但 AI_EMBED_PROVIDER 未设置 → 启动成功，embedder 为 nil，搜索静默返回 503
    // - DB_DRIVER=postgres 但 DB_DSN 格式错误 → 启动时在 Open 阶段才报错
    // - STORAGE_BACKEND=s3 但 STORAGE_S3_BUCKET 未设置 → 首次写入时 panic
    // - AUTH_MODE=jwt 但 AUTH_JWT_SECRET 未设置 → 启动成功，JWT 签发 panic
}
```

### 为什么需要它

| 场景 | 影响 |
|------|------|
| **首次部署：DevOps 自动部署** | CI/CD 管道部署后需要 API 调用来验证。无初始化流程，自动化脚本必须硬编码初始 admin key 或实现复杂的启动→等待→初始化序列 |
| **认证悖论** | 系统启动后无法通过 API 创建初始 admin key，因为创建 key 需要 admin scope。运维人员只能使用 `AUTH_KEYS` 环境变量（明文泄漏风险）或手动插入数据库 |
| **Demo/试用体验** | 新用户首次启动后面对空系统：无数据、无指南、无快速验证路径。`deploy/demo/seed.sh` 独立于系统存在，新用户未必知道或使用 |
| **配置验证** | 错误配置直到运行时才暴露：S3 bucket 不存在首次写入报错，embedder endpoint 不可达首次搜索报 503 |
| **团队开发环境标准化** | 多个开发者各自启动系统，需要一致的初始状态（tenant、key、测试 bucket、demo 数据） |

### 架构权衡与建议方案

**核心设计：从"静默启动"到"智能初始化"**

```
首次启动流程（检测到空状态）：
  检测 → 数据库空/无 admin key/无 tenant
    ├─ 交互模式（CLI）：提示输入 admin key / 自动生成 + 打印
    ├─ 环境变量模式（CI/CD）：AERO_ADMIN_KEY, AERO_DEFAULT_TENANT
    └─ 自动模式（demo）：生成随机 key 打印到日志，创建 default tenant, 注入 demo data

每次启动流程：
  配置交叉验证 → 依赖可达性检查 → 系统健康自检 → 输出验证摘要
```

**具体切入点：**

1. **Bootstrap 阶段（`cmd/server/main.go` 的 `run()` 中 `initInfrastructure` 之后）：**
   ```go
   func bootstrap(ctx context.Context, cfg *config.Config, repo repository.Repository, reg *auth.Registry, logger *slog.Logger) error {
       // 1. 交叉配置验证
       // 2. 检测空状态（无 tenant、无 admin key）
       // 3. 根据模式自动初始化
       // 4. 输出 bootstrap 摘要到日志
   }
   ```

2. **三模式设计：**
   | 模式 | 触发器 | 行为 |
   |------|--------|------|
   | `init=interactive` | `AERO_INIT_MODE=interactive` 或无 TTY 时 fallback | 提示输入 admin key 名称，自动生成并打印 key |
   | `init=env` | `AERO_INIT_MODE=env` | 从 `AERO_ADMIN_KEY` 环境变量读取（含 key name:token 格式），不存在则跳过 |
   | `init=auto` | `AERO_INIT_MODE=auto` 或 demo 构建 | 生成随机 admin key → 打印到日志（`WARN` 级别，显眼格式）→ 创建 `default` tenant → 可选 seed demo data |

3. **配置交叉验证函数 `validateConfig(cfg) []ConfigError`：**
   - `AI_INDEX_ENABLED=true` + `AI_EMBED_PROVIDER=""` → `WARN: AI index enabled but no embedder configured, search will return 503`
   - `DB_DRIVER=postgres` + `DB_DSN` 不包含 `sslmode` → `WARN: Postgres DSN missing sslmode, connection may be insecure`
   - `STORAGE_BACKEND=s3` + `STORAGE_S3_BUCKET=""` → `ERROR: S3 backend requires STORAGE_S3_BUCKET`
   - `AUTH_JWT_SECRET=""` + JWT endpoints reachable → `WARN: JWT secret not set, /v1/admin/jwt will fail`
   - 输出为结构化列表，`ERROR` 级阻止启动，`WARN` 级仅打印

4. **启动自检端点扩展：** `/healthz` 增加配置校验摘要；新增 `GET /admin/bootstrap/status` 返回当前系统初始化状态

### 边界情况

| 场景 | 处理 |
|------|------|
| 非首次启动但 admin key 全部被删除 | `bootstrap` 检测到 `count(api_keys) == 0` → 自动重新创建 admin key（除非 `AERO_INIT_MODE=skip`） |
| 配置校验 ERROR 但运维人员需要强制启动 | `AERO_SKIP_CONFIG_VALIDATION=true` 环境变量跳过验证（紧急维护场景） |
| Bootstrap 期间并发 API 请求 | bootstrap 阶段 server 返回 `503 Service Unavailable` + `Retry-After: 5` |
| 自动生成的 admin key 未及时保存 | 日志中 `WARN` 级别打印 key，提示保存。同时支持 `AERO_INIT_KEY_OUTPUT=/path/to/key.txt` 写入文件 |
| Demo 数据覆盖已有数据 | `AERO_INIT_MODE=auto` 只在完全空的数据库（0 tenant, 0 object）时注入 demo 数据 |

---

## 方向三：Graceful Shutdown 异步排空

### 现状

**当前关闭序列过于简化，未考虑系统内运行的异步工作者和长操作：**

```go
// cmd/server/main.go:runServer
func runServer(ctx context.Context, handler http.Handler, cfg *config.Config, logger *slog.Logger, bus *events.Bus, shutdownOtel func(context.Context) error) error {
    // ...
    select {
    case <-ctx.Done():
        logger.Info("shutdown requested")
    case err := <-errCh:
        // ...
    }
    shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
    defer cancel()
    srv.Shutdown(shutdownCtx)    // Step 1: 关闭 HTTP listener（15s timeout）
    bus.Close()                  // Step 2: 立即关闭所有 subscriber channel
    shutdownOtel(shutdownCtx)    // Step 3: 关闭 OTel exporter
    return nil                   // Step 4: 立即返回
}
```

**问题分解：**

| 问题 | 代码位置 | 影响 |
|------|---------|------|
| **HTTP Shutdown 不等待 in-flight 请求** | `srv.Shutdown(shutdownCtx)` — Go 标准库的 Shutdown 等待处理中的请求完成，但 `context.WithTimeout(shutdownCtx, 15s)` 超时后强制关闭 | 大对象下载（>100MB）或长 SSE 流在超时后被截断 |
| **后台 Worker 无 shutdown 信号** | `jobs.Queue` 无 `Shutdown() context` 方法；`reconcile` 和 `replication` worker 使用 `ctx`（已取消）但可能正在执行操作 | Indexer 在 embedding 中途被取消，产生孤儿索引状态 |
| **Bus 关闭不等待消费者** | `bus.Close()` 直接关闭所有 channel，不等待 subscriber 处理完当前事件 | Webhook 发送中、replication 排队中的事件丢失 |
| **Multipart Upload 无恢复** | 进程崩溃后 `uploads` 内存 map 清空，已上传的 part 文件成为孤儿 | 数据丢失 + 存储空间泄漏 |
| **Otel Exporter 关闭可能阻塞** | `shutdownOtel(shutdownCtx)` 可能 flush 未导出的 trace/metrics | 最后几秒的可观测数据丢失 |

### 为什么需要它

| 场景 | 影响 |
|------|------|
| **滚动更新：零停机部署** | Kubernetes 滚动更新时，旧 Pod 收到 SIGTERM。当前关闭序列在 15s 内截断所有运行中的请求，导致客户端收到连接重置 |
| **大文件下载被截断** | S3 GET 一个 500MB 文件，下载到 50% 时 Pod 开始关闭。当前 15s 超时不足以完成下载，客户端收到不完整的响应 |
| **Indexer 中间状态** | Indexer 正在 embed 一个 100 页 PDF（耗时 30s+），关闭触发后 embed 调用中断。`indexer_skip_total{reason="shutdown"}` 无此分类 |
| **Webhook 发送中丢失** | Bus subscriber 正在 HTTP POST webhook 到客户端点，`bus.Close()` 触发 channel 关闭，webhook 线程无法标记事件为 consumed |
| **运维人员无可见性** | 关闭过程无结构化日志：什么在运行、什么被排空、什么被截断。运维人员收到"服务不可用"告警但无法追溯原因 |

### 架构权衡与建议方案

**核心设计：有序关闭的五阶段协议**

```
Phase 0: 收到 SIGTERM/SIGINT
Phase 1: 进入排空模式（不再接受新请求，返回 503 + Retry-After）
Phase 2: HTTP 优雅关闭（等待 in-flight 请求完成，最长排空窗口）
Phase 3: 后台 Worker 排空（标记停止信号，等待当前任务完成）
Phase 4: 基础设施关闭（Bus → OTel → DB）
Phase 5: 进程退出
```

**具体切入点：**

1. **排空模式中间件：** 新增 `middleware.DrainMode` — 收到关闭信号后，所有新请求返回 `503 Service Unavailable` + `Retry-After: 10`。健康检查端点（`/healthz`/`/readyz`）正常返回以允许 k8s 的负载均衡器逐步摘除 Pod

2. **等待组（WaitGroup）追踪 in-flight 请求：** 在 `applyMiddleware` 中为每个请求注入 `sync.WaitGroup` +1，请求完成后 -1。Shutdown 时先等待 WaitGroup 清空（或排空窗口超时）

3. **Worker 优雅关闭：** 
   - `jobs.Queue` 新增 `Shutdown(ctx) error` 方法：停止轮询新 job，等待当前执行中的 job 完成
   - `reconcile.Worker` 和 `replication.Worker` 接收关闭信号：完成当前周期后退出
   - `antivirus.Worker` 类似处理

4. **Bus 优雅关闭：** `bus.Close()` 之前先给 subscriber 发送"即将关闭"信号，等待 subscriber 确认已将当前事件处理完毕。新增 `bus.Shutdown(ctx) error` 方法

5. **结构化关闭日志：**
   ```go
   logger.Info("shutdown: phase 1/5 — drain mode enabled")
   logger.Info("shutdown: phase 2/5 — waiting for in-flight requests", "remaining", wgCount)
   logger.Info("shutdown: phase 3/5 — draining workers", "workers", []string{"indexer", "replicator", "reconciler"})
   logger.Info("shutdown: phase 4/5 — closing infrastructure")
   logger.Info("shutdown: phase 5/5 — exiting")
   ```

6. **可配置的排空超时：** `SHUTDOWN_GRACE_PERIOD_SECONDS=60`（默认 60s，替换硬编码 15s）

### 边界情况

| 场景 | 处理 |
|------|------|
| 排空超时后仍有 in-flight 请求 | 记录 `shutdown: timeout — N requests still in flight, forcing close`，强制关闭。这些请求的客户端收到连接重置 |
| Worker 正在执行无法中断的操作（如 S3 CopyObject） | Worker 收到关闭信号后完成当前操作但不接受新任务。若操作耗时超时，强制取消 ctx |
| 同时收到多个 SIGTERM | 幂等：第二次信号后，排空窗口缩短至 5s，强制关闭 |
| 排空期间新请求到达负载均衡器 | 503 + `Retry-After: 10` 让 LB 将流量路由到其他实例。在 k8s 中，ReadinessProbe 返回 503 后 Endpoint 控制器摘除 Pod |
| SSE 长连接在排空期间 | SSE 连接收到 `event: shutdown\ndata: {"code":"ServerShutdown","message":"server is restarting, please reconnect"}\n\n` 后关闭 |

---

## 方向四：数据库连接韧性体系

### 现状

**数据库连接层是系统最底层的依赖之一，但其配置几乎完全缺失，导致生产环境偶发故障难以排查：**

```go
// internal/repository/postgres.go
func openPostgres(dsn string) (*sql.DB, error) {
    db, err := sql.Open("pgx", dsn)
    if err != nil {
        return nil, err
    }
    // ❌ 缺失配置:
    // db.SetMaxOpenConns(25)          — 最大连接数（默认无限制 → 可能耗尽数据库连接）
    // db.SetMaxIdleConns(5)           — 最大空闲连接数
    // db.SetConnMaxLifetime(30*time.Minute) — 连接最大生存时间（防 expired connection）
    // db.SetConnMaxIdleTime(5*time.Minute)  — 空闲连接超时
    return db, nil
}
```

```go
// internal/repository/sqlite.go
func openSQLite(dsn string) (*sql.DB, error) {
    // ...
    db.SetMaxOpenConns(1)   // 硬编码 1，SQLite 序列化写入的限制
    // ❌ 缺失:
    // PRAGMA busy_timeout = 5000          — 等待 5s 而不是立刻返回 "database is locked"
    // PRAGMA journal_mode = WAL            — WAL 模式提升并发读性能
    // PRAGMA synchronous = NORMAL          — 平衡持久性与写入性能
    // PRAGMA cache_size = -64000           — 64MB 缓存
    // PRAGMA temp_store = MEMORY           — 临时表/排序放内存
}
```

**连接故障的传播路径：**

```go
// internal/repository/sql.go
func (r *repository) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
    // 无查询超时包装：若连接池中某个连接已过期（Postgres 重启后 TCP 连接仍存活但已无效），
    // 第一个 ExecContext 将返回 "broken pipe" 或 "connection refused"。
    // 调用方（通常是 FileService 中的写操作）将错误传给 HTTP handler → 500。
    // 下一个请求会创建一个新连接，正常服务——这种"偶发 500"在生产中极难排查。
    return r.db.ExecContext(ctx, query, args...)
}
```

### 为什么需要它

| 场景 | 影响 |
|------|------|
| **Postgres 重启后偶发 500** | 数据库维护重启后，连接池中的 TCP 连接已断但 `sql.DB` 未感知。第一个请求触发 `ExecContext` → `broken pipe` → 500。随后的请求自动创建新连接，恢复正常。运维人员看到"无故 500 spike" |
| **连接泄漏耗尽数据库** | 默认 `MaxOpenConns=0`（无限制）。某个 handler 忘记关闭 `rows` 或连接未归还，连接数持续增长直到 `too many connections` |
| **SQLite "database is locked" 错误** | 默认 `busy_timeout=0`，写入冲突时立刻返回 `database is locked` 而非等待。高并发写入时持续报错 |
| **Prepared Statement 泄漏** | 每次查询创建新的 prepared statement 而不关闭，Postgres 端积累大量 leaked prepared statements 直到达到 `max_prepared_transactions` |
| **查询无超时导致 goroutine 泄漏** | 慢查询（如全表扫描）持续数分钟不返回，持有 connection 不释放，其他请求排队。goroutine 等待在 `ExecContext` 上不退出 |

### 架构权衡与建议方案

**核心设计：DB 连接配置从"零配置"到"显式配置"**

**Postgres 配置：**

```go
type PostgresConfig struct {
    DSN              string `env:"DB_DSN"`
    MaxOpenConns     int    `env:"DB_PG_MAX_OPEN_CONNS"`     // 默认 25
    MaxIdleConns     int    `env:"DB_PG_MAX_IDLE_CONNS"`     // 默认 5
    ConnMaxLifetime  time.Duration `env:"DB_PG_CONN_MAX_LIFETIME"`  // 默认 30m
    ConnMaxIdleTime  time.Duration `env:"DB_PG_CONN_MAX_IDLE_TIME"` // 默认 5m
    QueryTimeout     time.Duration `env:"DB_PG_QUERY_TIMEOUT"`      // 默认 30s
    HealthCheckInterval time.Duration `env:"DB_PG_HEALTH_CHECK_INTERVAL"` // 默认 10s
}
```

**SQLite 配置（扩展）：**

```go
type SQLiteConfig struct {
    DSN            string `env:"DB_DSN"`
    BusyTimeout    int    `env:"DB_SQLITE_BUSY_TIMEOUT"`    // 默认 5000 (ms)
    JournalMode    string `env:"DB_SQLITE_JOURNAL_MODE"`     // 默认 WAL
    Synchronous    string `env:"DB_SQLITE_SYNCHRONOUS"`      // 默认 NORMAL
    CacheSize      int    `env:"DB_SQLITE_CACHE_SIZE"`       // 默认 -64000 (64MB)
    TempStore      string `env:"DB_SQLITE_TEMP_STORE"`       // 默认 MEMORY
}
```

**连接健康探测中间件：**

```go
// 在 sql.go 中新增 ConnHealthChecker，定期对每个空闲连接执行 SELECT 1
// 过期连接自动从池中移除
// 健康检查失败写入 metric db.health_check_failures_total{driver="postgres"}
```

**Prepared Statement 缓存（仅 Postgres/pgx）：**

pgx v5 支持 `ParseConfig` 中的 `PreferSimpleProtocol` 和 statement cache。当前使用 `database/sql` + `pgx` driver 模式，Prepared Statement 由 `sql.DB` 管理。确认当前模式下是否有缓存：

```go
// 使用 database/sql + pgx driver 时，sql.DB 自动缓存 prepared statement
// 但缓存大小和清理策略不可配置
// 对于高频查询（如 GetObject, ListObjects），可考虑自定义 prepared statement cache：
// var stmtCache = sync.Map // key: query hash, value: *sql.Stmt
// stmt, _ := r.db.PrepareContext(ctx, query)
```

**具体切入点：**

1. **连接池配置：** 将连接池参数暴露为环境变量，在 `openPostgres`/`openSQLite` 中应用
2. **查询超时：** 在 `repository/sql.go` 的 `ExecContext`/`QueryContext` 包装器中添加 context deadline（`DB_PG_QUERY_TIMEOUT`），防止慢查询无限等待
3. **SQLite PRAGMA 配置：** 在 `openSQLite` 中执行 `PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL; ...`
4. **Prepared Statement 缓存层：** 高频查询（`GetObjectByKey`、`ListObjects`、`InsertEvent`）使用缓存的 `*sql.Stmt`
5. **连接健康指标：** 新增 Prometheus 指标 `db.connections_open`、`db.connection_errors_total`、`db.query_duration_ms`（按 query pattern 或 endpoint 细分）
6. **自动重试封装：** `ExecContext` 在遇到 `broken pipe`/`connection refused` 等连接级错误时自动重试一次（最多一次，防请求放大）

### 边界情况

| 场景 | 处理 |
|------|------|
| 连接池耗尽（所有连接被慢查询占用） | 新请求在 `sql.DB` 层等待直到超时（`DB_PG_QUERY_TIMEOUT`）或连接释放。`db.connection_wait_duration_ms` 指标反映等待时间 |
| SQLite WAL 文件无限增长 | `PRAGMA wal_autocheckpoint=1000` 控制 WAL 文件大小；定期 `PRAGMA wal_checkpoint(TRUNCATE)` |
| Prepared Statement 过多 | pgx v5 默认 `PreparedStatementCacheMaxSize=256`。建议显式配置 `DB_PG_PREPARED_STMT_CACHE_SIZE=512` |
| 跨版本 pgx 升级 | pgx v5 的 `database/sql` 驱动行为与 v4 不同。版本升级需通过集成测试验证 |

---

## 方向五：跨协议 CORS 与安全头硬化

### 现状

**CORS 实现功能正确但安全不足，四个协议入口共享同一宽松配置：**

```go
// internal/middleware/cors.go
type CORSConfig struct {
    AllowedOrigins []string // 允许的 Origin 列表（* 通配）
    AllowedMethods []string
    AllowedHeaders []string
    ExposeHeaders  []string
}

func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            origin := r.Header.Get("Origin")
            // 检查 origin 是否在允许列表中
            if !originAllowed(origin, cfg.AllowedOrigins) {
                next.ServeHTTP(w, r) // ❌ 不允许的不返回 CORS 头但继续处理请求
                return
            }
            w.Header().Set("Access-Control-Allow-Origin", origin)
            // ❌ 无 Vary: Origin 头
            // ❌ 无 Access-Control-Max-Age 头（预检缓存）
            // ❌ 无 Access-Control-Allow-Credentials: true
            // ❌ 无 Access-Control-Allow-Private-Network: true
            if r.Method == http.MethodOptions {
                w.Header().Set("Access-Control-Allow-Methods", join(cfg.AllowedMethods))
                w.Header().Set("Access-Control-Allow-Headers", join(cfg.AllowedHeaders))
                w.WriteHeader(http.StatusOK)
                return
            }
            next.ServeHTTP(w, r)
        })
    }
}
```

**安全硬化缺口详表：**

| 缺口 | 代码位置 | 风险 |
|------|---------|------|
| **无 `Access-Control-Max-Age`** | `cors.go` — OPTIONS 预检响应中未设置 | 浏览器每次请求都发送 OPTIONS 预检（默认无缓存），增加 2x 请求延迟 |
| **无 `Vary: Origin`** | `cors.go` — 响应头缺少 Vary | 浏览器/CDN 可能缓存同一 URL 的响应到错误的 origin，导致跨租户数据泄漏 |
| **不允许的 Origin 继续处理请求** | `cors.go:originAllowed` check — 仅不返回 CORS 头，**不阻止请求处理** | 攻击者可发送跨站请求（无 CORS 头但操作成功），CSRF 攻击面扩大 |
| **单一 CORS 配置应用于所有协议** | `cmd/server/main.go:applyMiddleware` | S3 协议的 CORS 需求（`AllowedMethods: GET,PUT,POST,DELETE,HEAD`）与 REST API 不同；WebDAV 需要 `PROPFIND,MKCOL,MOVE,COPY,LOCK,UNLOCK` |
| **S3 协议在认证前无 CORS** | `internal/api/s3compat/handler.go` | S3 handler 先验签再处理，但 CORS 检查在 middleware 层——OPTIONS 预检请求不经过 SigV4，但浏览器发送的跨域 `PUT` 请求经过整条链 |
| **无安全头** | `applyMiddleware` 或各协议 handler | 无 `Content-Security-Policy`（限制 XSS）、无 `X-Content-Type-Options: nosniff`（禁止 MIME 嗅探）、无 `Strict-Transport-Security`（HSTS，仅 HTTPS 部署时需要） |
| **无 Private Network Access 保护** | `cors.go` — 未处理 `Access-Control-Request-Private-Network: true` | 公网页面无法访问内网资源的安全机制缺失（Chrome 94+ 支持） |

### 为什么需要它

| 场景 | 影响 |
|------|------|
| **SPA 前端跨域 API 调用** | 浏览器每次 POST 前先发 OPTIONS（无缓存），200ms RTT 增加至 400ms。设置 `Access-Control-Max-Age: 86400` 可缓存预检响应 24 小时 |
| **CDN 错误缓存跨租户数据** | 若不设置 `Vary: Origin`，CDN 可能将租户 A 的请求响应缓存并返回给租户 B（假设 URL 相同但 Origin 不同）。这直接导致跨租户数据泄漏 |
| **S3 Web 控制台跨域访问** | MinIO 或 S3 浏览器控制台通过 JavaScript 直接调用 S3 API。若 CORS 过于宽松（如 `AllowedOrigins: *`），任何站点都可读取用户桶中内容 |
| **安全审计要求** | CSP/HSTS/X-Content-Type-Options 是安全审计的基本检查项。缺失这些头导致合规度下降 |
| **WebDAV 浏览器客户端** | 基于浏览器的 WebDAV 客户端需要特定的 CORS 配置（`PROPFIND` 等方法）。当前单一配置无法满足 |

### 架构权衡与建议方案

**核心设计：从"统一 CORS"到"分层 CORS + 安全头模板"**

```
分层 CORS 架构：
  ┌─────────────────────────────────────┐
  │ 全局 CORS 中间件（安全底线）          │
  │  - Access-Control-Max-Age: 86400     │
  │  - Vary: Origin, Access-Control-Request-Method │
  │  - 不允许的 Origin → 403（拒绝请求） │
  ├─────────────────────────────────────┤
  │ REST 协议层 CORS（v1 路由组内）       │
  │  - 细粒度 methods/headers            │
  ├─────────────────────────────────────┤
  │ S3 协议层 CORS（s3compat 路由组内）   │
  │  - S3 标准 CORS 规则集（桶级配置）    │
  ├─────────────────────────────────────┤
  │ WebDAV 协议层 CORS                   │
  │  - WebDAV 方法（PROPFIND 等）        │
  └─────────────────────────────────────┘
```

**具体切入点：**

1. **CORS 配置扩展**：
   ```go
   type CORSConfig struct {
       AllowedOrigins   []string
       AllowedMethods   []string
       AllowedHeaders   []string
       ExposeHeaders    []string
       MaxAge           int      // 新增：预检缓存秒数，默认 86400（24h）
       AllowCredentials bool     // 新增：允许携带凭据
       BlockOnDisallowed bool    // 新增：不允许的 origin 返回 403 而非静默通过
   }
   ```

2. **按协议分层 CORS：**
   - 全局中间件：设置安全底线（`Vary`、`Max-Age`、不允许 origin 返回 403）
   - REST 路由组（`/v1`）：`Access-Control-Allow-Methods: GET,POST,PUT,DELETE,HEAD,OPTIONS`
   - S3 路由组（`/s3`）：`Access-Control-Allow-Methods: GET,PUT,POST,DELETE,HEAD,OPTIONS`
   - WebDAV 路由组（`/webdav`）：`Access-Control-Allow-Methods: GET,HEAD,PUT,DELETE,PROPFIND,MKCOL,MOVE,COPY,LOCK,UNLOCK,OPTIONS`

3. **安全头中间件**（在 `applyMiddleware` 最外层，`RequestID` 之后）：
   ```go
   func SecurityHeaders(next http.Handler) http.Handler {
       return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
           w.Header().Set("X-Content-Type-Options", "nosniff")
           w.Header().Set("X-Frame-Options", "DENY")
           w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
           // CSP 和 HSTS 通过配置控制（HSTS 仅 HTTPS 部署时启用）
           if cfg.Security.CSP != "" {
               w.Header().Set("Content-Security-Policy", cfg.Security.CSP)
           }
           next.ServeHTTP(w, r)
       })
   }
   ```

4. **S3 CORS 桶级配置**：S3 协议应支持桶级 `CorsRule`（通过 `GET/PUT/DELETE /{bucket}?cors`），而非全局统一配置。当前 `bucketconfig.go` 已有 `BucketConfig.CORS` 字段但 S3 `?cors` 子资源 handler 的实现在 `internal/api/s3compat/handler.go` 中直接返回硬编码 CORS 规则（确认：查看 handler.go 中的 `getBucketCORS`/`putBucketCORS` 实现）

5. **Private Network Access 支持**：当 `Origin` 是公网但目标服务器是内网 IP 时，Chrome 要求服务器响应 `Access-Control-Allow-Private-Network: true` 预检头。在全局 CORS 中间件中检测 `Access-Control-Request-Private-Network` header 并响应

### 边界情况

| 场景 | 处理 |
|------|------|
| Origin 为 `null`（file:// 协议或隐私模式） | `originAllowed` 函数按精确匹配 `"null"` 字符串或通配规则 |
| 多个 Origin 值 | HTTP `Origin` 头只有一个值。无多 origin 问题 |
| 预检请求带 Cookie | 若 `AllowCredentials=true`，`Access-Control-Allow-Origin` 不能为 `*`，必须回显具体 origin |
| S3 桶级 CORS 与全局 CORS 冲突 | 桶级 CORS 覆盖全局 CORS：若桶配置了 CORS 规则，使用桶规则；否则 fallback 到全局配置 |
| HSTS 与 HTTP 部署 | `Strict-Transport-Security` 仅在 `cfg.Security.HSTSEnabled && r.TLS != nil` 时设置 |

---

## 总结优先级建议

| # | 方向 | 投入 | 产出 | 核心依赖 | 建议顺序 |
|---|------|------|------|---------|---------|
| 1 | **Schema 迁移治理管线** | 中（2-3 周） | 生产数据库变更零事故；灰度 schema 发布；回滚自动化 | `repository/sql.go` 重构；新增 `_migrations` 表；CLI + admin API | **P0**——防数据丢失 |
| 2 | **Graceful Shutdown 异步排空** | 中（2-3 周） | 零停机滚动更新；长操作不截断；Worker 状态不丢失 | `WaitGroup` 追踪；Worker 关闭协议；排空中间件 | **P0**——生产部署必须 |
| 3 | **跨协议 CORS 与安全头硬化** | 小（1-2 周） | 安全审计通过；SPA 性能提升 2x；跨租户数据泄漏防护 | `CORSConfig` 扩展；安全头中间件；S3 桶级 CORS | **P1**——基本安全底线 |
| 4 | **数据库连接韧性体系** | 中（2-3 周） | 偶发 500 消除；SQLite 写入可靠性提升；连接故障自愈 | 连接池配置参数；SQLite PRAGMA；查询超时；健康探测 | **P1**——运维稳定性 |
| 5 | **Bootstrap 初始化与首次运行体验** | 小（1-2 周） | 零配置启动；CI/CD 自动部署；Demo 体验提升 | Bootstrap 阶段；配置交叉验证；三模式初始化 | **P2**——产品成熟度 |

**说明：**
- **P0** 方向保护生产数据安全和系统可用性，建议在下一个 Sprint 立即启动
- **P1** 方向提升运维稳定性和安全基线，建议在 P0 完成后跟进
- **P2** 方向改善产品成熟度和开发者体验，建议在核心可靠性提升后实施

所有方向均应遵守现有工程约束（单文件 ≤500 行、单函数 ≤50 行、圈复杂度 ≤10、先测试后提交、`make check` 全绿），并在实现前完成架构设计与 API 契约评审。

---

## 附录：关键代码锚点速查表

| 方向 | 文件路径 | 关键行/区域 | 说明 |
|------|---------|------------|------|
| 1 | `internal/repository/sql.go` | L50-70 | `Migrate` 函数核心逻辑，无版本追踪 |
| 1 | `internal/repository/sqlite.go` | L30 | 无 `busy_timeout` 等 PRAGMA 配置 |
| 1 | `internal/repository/postgres.go` | L43 | `sql.Open` 后零连接池配置 |
| 1 | `internal/repository/migrations/{sqlite,postgres}/` | 全目录 | 50 对迁移文件，`down.sql` 从未被引用 |
| 1 | `cmd/server/main.go` | L67 | `repo.Migrate(ctx)` 唯一调用点 |
| 2 | `cmd/server/main.go` | L47-67 | `run()` 启动序列零 bootstrap 逻辑 |
| 2 | `internal/auth/auth.go` | L90-100 | `Authenticate` 空注册表的认证悖论 |
| 2 | `internal/auth/store.go` | 全文件 | `PersistentStore` 无空状态检测 |
| 2 | `internal/config/config.go` | L30-50 | `Load()` 无交叉字段验证 |
| 3 | `cmd/server/main.go` | L250-270 | `runServer` 关闭序列，硬编码 15s 超时 |
| 3 | `internal/events/bus.go` | L95-102 | `Close()` 直接关闭所有 channel |
| 3 | `internal/jobs/jobs.go` | 无 `Shutdown` 方法 | Queue 无优雅关闭能力 |
| 3 | `internal/reconcile/job.go` | 全文件 | 周期 Worker 无关闭信号处理 |
| 4 | `internal/repository/postgres.go` | L43-48 | `sql.Open("pgx", dsn)` → 零连接池配置 |
| 4 | `internal/repository/sqlite.go` | L30 | `MaxOpenConns=1` 硬编码 |
| 4 | `internal/repository/sql.go` | L90-100 | `ExecContext`/`QueryContext` 无超时包装 |
| 4 | `internal/config/config_app.go` | 全文件 | 无数据库连接配置字段 |
| 5 | `internal/middleware/cors.go` | L30-90 | CORS handler 无 `Max-Age`/`Vary`/`AllowCredentials` |
| 5 | `internal/middleware/cors.go` | L50 | `originAllowed` 不阻止非法 origin 的请求 |
| 5 | `cmd/server/main.go` | L220-230 | `applyMiddleware` 中单一 CORS 配置 |
| 5 | `internal/api/s3compat/handler.go` | `getBucketCORS` | S3 桶级 CORS 实现（需确认完整性） |
| 5 | `internal/middleware/middleware.go` | 全文件 | 无安全头中间件 |
