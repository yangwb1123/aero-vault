# SPACE-P1B-A-001 — 权威绑定探测准备片 v1

## 1. 状态、授权与时间

- 用户已批准准确六路径、单次最多四小时、保留完整门禁的 P1B-A 准备片；不重复请求，不以旧设计或 approval-request 的 Proposed 历史否认本次许可。
- 本阶段仅新增本文、末尾追加 Sprint。**四个 Go 文件必须保持不存在**；返回后由控制器执行 docs checkpoint，通过后下一阶段才可新增 Go。本阶段不运行该检查点、不写私有控制器。
- 固定窗口为 `2026-10-19T00:50:22Z → 2026-10-19T04:50:22Z`，准备计入；launch、失败、重试、恢复均不续期，不新开 24 小时。
- P1A 与 P1B 设计已经完成，不重做 40 方向、旧实现或旧五测试修复。旧记录保留；其成功或模型报告不能抵扣本片当前源码的工程证据。
- 本片仅建立**未装配、只读的绑定事实查询**，不是权限允许／拒绝，不是 P1B B/C、P2 或可用 Space 产品。
- 当前状态：查询／错误／测试合同落盘；Go、测试、真实故障注入、全部当前门禁与独立验收均**待执行**，不预填通过。

## 2. 准确范围与冻结项

准确六文件以本 RUN `allowed-paths.json` 为准，无第七项目文件：

| 路径 | 本阶段／后续职责 |
|------|----------------|
| `internal/spaces/binding.go` | NEW，后续：绑定结果、严格校验、窄只读端口 |
| `internal/spaces/binding_test.go` | NEW，后续：纯值正反例，不创建数据库 |
| `internal/repository/sql_spaces_binding.go` | NEW，后续：SQL 具体类型实现，一次一致观察 |
| `internal/repository/sql_spaces_binding_test.go` | NEW，后续：真实磁盘 SQLite、错误及兼容回归 |
| `docs/requirements/personal-team-space-binding-probe-v1.spec.md` | NEW，本阶段查询／错误／测试合同 |
| `docs/agent/CURRENT_SPRINT.md` | 本阶段仅末尾追加，完整旧前缀不变 |

- 所有既有 Go／测试（含 P1A、notifier、旧五测试）、全部迁移（含四份 0048）、旧 spec／其他文档、接口、依赖、配置、安全默认、装配、协议、API、UI、SDK、AI、门禁与豁免均冻结。
- 新端口不嵌入、不扩宽九方法 `spaces.Store`、`repository.Repository` 或 `storage.Storage`；不添加要求既有 mock 实现的方法。
- 不接 main/router/FileService/Manager/worker/CLI；不在生产链捕获、调用该端口。编译断言不算装配；禁止 init 注册或全局运行副作用。
- 不创建生产 Space／桶／Blob，不认领旧桶、不迁移／回填，不授成员角色、ACL、删除权；不新增身份来源、员工资格、用户数据或权限政策。
- 不读取凭据，不写生产或外部服务，不改全局 Pi/provider/认证/账户池；不操作 Git 索引／分支，不提交、推送、合入或部署，不覆盖用户脏改动。
- RUN 已记录完整备份与逐成员 SHA／类型／模式核验；本阶段不重做、不冒称亲自复验。失败、拒绝或超时保留现场，不自动回滚、不盲重放；仅获批故障注入须 finally 精确恢复其临时字节。
- 后续每个新 Go 文件 ≤500 行、函数 ≤50 行、复杂度 ≤10，不拆出第七文件或增加豁免。本文 ≤400 行，Sprint 本片追加 ≤80 行。

## 3. 回读事实与必要性

本阶段已完整回读 `internal/spaces/{validate,errors,store,types}.go`，以及 repository 的 `sql_spaces.go`、`sql_spaces_queries.go`、`sql_spaces_tx.go`、`sql_spaces_errors.go`、`sql_spaces_fixtures_test.go`、`sql_spaces_failure_test.go`、`sql_spaces_queries_test.go`、`tenants.go`、`tenants_test.go`、`sql.go` 和四份 0048；门禁核对 `Makefile`、`checks/invariants.py`、CI。

- `GetSpaceByBucket` 先 `ValidateTenant/ValidateIdentifier`，再 `spaceRead → spaceTenant`；查绑定前已拒绝未登记或非 active tenant。其 `ErrTenantUnavailable` **不是权威无绑定证明**，不得用于本探测的 absence 分支。
- 旧 `spaceRead` 维持原事务／active tenant 语义；旧 getter、错误分类器及现有 Store 全部不改、不委托新探测替换。
- 0048 以 `(tenant_id,id)` 为根主键，`(tenant_id,backing_bucket)` 唯一；Space 外键关联 tenant 与桶。正常 FK 下未知 tenant＋绑定不可达，损坏组合用纯结果／隔离 scanner 反例，不关闭 FK。
- `ValidateIdentifier` 拒空、首尾空白、坏 UTF-8、控制字符，不 trim/casefold；`ValidateTenant` 另拒 `*`；`ValidateID` 要求 canonical 非零 UUID。沿用这些规则，不复制桶命名政策。
- `spaceSQLError` 默认保留 cause，但将 `sql.ErrNoRows` 分类为 `ErrNotFound`。新查询理论上总有一行，必须在新文件专门防止该分类把异常无行误当 absence。
- `spaceTestDB/spaceTestOpen` 已提供 `t.TempDir()` 磁盘 SQLite、真实 Open、完整 Migrate 与清理，可复用但不能修改夹具、Open、Migrate、连接或 PRAGMA。

## 4. 固定类型、方法与值合同

以下新增公开名字和签名为后续实现合同（不是本阶段 Go 实现）：

```go
type BindingState string

const (
    BindingBound   BindingState = "bound"
    BindingUnbound BindingState = "unbound"
)

type BindingResult struct {
    TenantID     string
    Bucket       string
    TenantKnown  bool
    TenantStatus string
    State        BindingState
    SpaceID      string
    Revision     int64
}

type BindingReader interface {
    ProbeSpaceBinding(ctx context.Context, tenant, bucket string) (BindingResult, error)
}

func ValidateBindingInput(tenant, bucket string) error
func ValidateBindingResult(result BindingResult) error
```

- SQL 方法固定为 `(*sqlStore).ProbeSpaceBinding(context.Context, string, string) (spaces.BindingResult, error)`；新文件只加 `var _ spaces.BindingReader = (*sqlStore)(nil)` 编译断言，不装配。
- 成功只有 `BindingBound`／`BindingUnbound` 且 error=nil；**fault 用 non-nil error 表达**，返回完整零值 `BindingResult{}`。不另造 fault 枚举／异常框架；零值 State、未知值或字符串 `fault` 都不是成功。
- `TenantID/Bucket` 必须是调用者显式精确输入，大小写敏感，不回退 default tenant/default bucket、不 trim、不 casefold、不从 context/header 推补、不接受 `*` 通配。
- `ValidateBindingInput` 先复用 `ValidateTenant(tenant)`，再 `ValidateIdentifier(bucket)`，并显式拒绝 bucket=`*`；非法输入返回可 `errors.Is(..., ErrInvalidArgument)` 的错误，查询前结束。不新增 DNS/S3 名称或 `spc-` 前缀要求。
- `ValidateBindingResult` 校验同样的请求键及完整成功形状；非法纯值可识别为 `ErrInvalidArgument`。SQL 对观测结果校验失败还必须加 `ErrPersistence` 并保留校验 cause，不能当成调用者输入失败或无绑定。
- `TenantKnown` 表示 tenants 精确行是否存在；known=false 时 `TenantStatus` 必须为空。known=true 时如实保存读到的非 NULL raw status，不默认 active、不把大小写或其他非 active 文本归并成 disabled。
- raw status 不是授权枚举；实际非 NULL 空文本也按原值保存，不能仅因其非 active 就令本探测报允许／拒绝。SQL NULL／部分缺列则是缺事实，必须 fault。
- bound 必须 known=true、`SpaceID` 通过 `ValidateID`、`Revision>0`；读取允许正 `int64` 上界，不借用成员写命令的 revision 增长限制。
- unbound 必须 `SpaceID=="" && Revision==0`；携带任一 Space 事实、unknown tenant 却 bound、缺键、坏 state、非正 revision 都无效。
- 不返回 Role、Owner、SubjectRef、成员资格、Allowed 或 legacy fallback 决策；不把桶是否存在混入本三态。

### 4.1 事实真值表（全部待测）

| tenant 观察 | 精确 Space 观察 | 探测结果 |
|-------------|----------------|----------|
| known，raw=`active` | 有合法绑定 | bound，真实 ID／revision |
| known，raw=`active` | 确认无绑定 | unbound，known=true，保留 raw |
| 未登记 | 确认无绑定，旧桶存在或桶不存在 | unbound，known=false，status 空 |
| known，raw=`disabled` 或其他非 active | 有合法绑定／确认无绑定 | 分别 bound／unbound，原样 known/status；不裁决权限 |
| 未登记 | 存在任何绑定 | fault，不返回部分事实 |
| 任意 | 查询／Scan／context／收尾错误或不完整结果 | fault，不伪 unbound |

未知 tenant 必须实际查询精确绑定；不能仅据 tenant 行缺失提前成功。disabled 绑定仍是 bound 事实，但这不赋予 disabled tenant 访问权；未来调用方政策另批。旧 getter 对未知／disabled 继续 `ErrTenantUnavailable`。

## 5. 一次 SELECT 的一致观察合同

登记的最小查询形状如下，必要参数 CAST 兼容 PG 类型推断：

```sql
SELECT t.tenant_id, t.status,
       sp.tenant_id, sp.backing_bucket, sp.id, sp.revision
FROM (SELECT CAST($1 AS TEXT) AS tenant_id,
             CAST($2 AS TEXT) AS bucket_name) AS requested
LEFT JOIN tenants AS t
       ON t.tenant_id = requested.tenant_id
LEFT JOIN spaces AS sp
       ON sp.tenant_id = requested.tenant_id
      AND sp.backing_bucket = requested.bucket_name
```

- 一次 `s.db.QueryRowContext(ctx, s.rebind(query), tenant, bucket)` 取得完整观察；派生单行是驱动表，不能由 tenants 驱动或把 spaces join 依附于 t，避免漏掉 unknown＋binding 异常。
- 无 status=`active` 过滤；不查桶前缀、不反解析 UUID、不查缓存或迁移版本来猜 absence。无绑定不等于桶存在，更不等于许可继续旧路径。
- 正常 schema 下 tenant 主键与绑定唯一键令该语句恰好一行；无绑定由该行的全 NULL Space 列表达。意外 `sql.ErrNoRows` 是故障，不是正常 absence。
- tenant 两列及 Space 四列分别用 SQL nullable 值扫描。tenant 两列全 NULL 才 known=false；tenant ID 必须与输入相等。known 行 status NULL、tenant partial NULL、Space partial NULL 都 fault。
- Space 四列全 NULL 才表示无绑定；全部有值时核对 Space tenant、bucket 与输入完全相等，再校验真实 ID、正 revision 与 known。不能用空字符串或 revision=0 代替 NULL presence 判定。
- 新文件登记扫描 seam：`scanSpaceBinding(row spaceScanner, tenant, bucket string) (spaces.BindingResult, error)`；复用现有窄 `spaceScanner`，可拆小函数，但不改旧 scanner。
- 单行 Scan 包括 database/sql 的查询与 Rows 收尾；Scan 返回的任何错误都必须转发，不能丢弃关闭错误后返回已扫描成功值。不得另写忽略 `rows.Err/Close` 的收集路径。
- 新文件登记错误 helper：`spaceBindingSQLError(ctx context.Context, op string, err error) error`，只处理本探测的特殊故障语义，其他适用分类复用旧 `spaceSQLError`，不改全局错误行为。
- 生产实现只允许 SELECT 与派生值校验；不调用旧写方法、不 Begin/Commit 新事务、不 FOR SHARE/UPDATE、不加应用锁／缓存／UoW／两阶段框架。一次语句只证明该次数据库快照一致，不承诺跨请求、撤销或 Blob 操作原子。
- I1：上式 `$1`、`$2` 各出现一次，实际参数 `[tenant,bucket]`。同值重复出现也须独立新编号／实参；复用 requested 列不是复用 `$N`。不得改 rebind。

## 6. 错误分类与因果合同

| 情形 | 必须保持的返回／因果 |
|------|---------------------|
| 请求键非法 | 零结果＋`ErrInvalidArgument`；不发 SQL、不补默认 |
| 确认无绑定 | 合法 unbound＋nil；正常成功不是 `ErrNotFound` |
| 关闭 DB、缺表、驱动／Scan／收尾故障 | 零结果＋`ErrPersistence`；保留原始 cause 的 Is/As 链 |
| 预取消／已过期 context，或执行中 context 故障 | 零结果＋`ErrPersistence`，保留 `context.Canceled/DeadlineExceeded`；驱动 cause 也不丢失 |
| 意外 `sql.ErrNoRows` | 零结果＋`ErrPersistence`，保留 `errors.Is(err, sql.ErrNoRows)`；不得分类成 spaces.ErrNotFound 或 unbound |
| 结果字段／NULL 组合／ID／revision 不一致 | 零结果＋`ErrPersistence`；保留校验 cause，不返回已扫描部分值 |
| 适用的 typed BUSY/LOCKED、PG 并发错误 | 保留既有 `ErrConcurrent` 分类及 typed cause，仍是 fault，不重试、不当 absence |

- 包装必须 `%w`／`errors.Join` 保留真实因果，不按错误消息字符串猜分类；包装时已有 context 错误也须保留。
- 特别是 `sql.ErrNoRows`：先在新 helper 显式归 persistence，再处理／保留 context，不能直接送旧分类器得到 `ErrNotFound`。
- Probe 不将任何错误转成 `ErrTenantUnavailable`、Forbidden、健康空页、legacy 许可或成功缓存；不引入授权错误类型。
- nil error 之前必须完成扫描、收尾及结果校验。任何失败返回完整零值，不返回“unbound＋error”或“bound＋error”让调用者误用。

## 7. 编码前登记的测试矩阵（全部待执行）

所有新增顶层名统一 `TestBinding*`／`TestSpaceBinding*`，仅在两个新测试文件；可拆 test-only 小函数，复用现有有用途夹具。以下名字固定登记，表内子例不得以仅无错误断言代替精确结果／cause／无变化断言。

### 7.1 `internal/spaces/binding_test.go`

| 测试名 | 必须辨别的正反例 |
|--------|----------------|
| `TestBindingInputValidation` | 合法 tenant/bucket 原样；空、`*`、首尾空白、坏 UTF-8、控制字符逐字段拒绝。大小写不同仍不同；显式 default 只是字面值，不隐式补默认；不限制合法内部字符或根据名字推绑定 |
| `TestBindingResultValidation` | active／disabled／其他 raw 状态的 bound/unbound、unknown unbound；零值／坏 state／缺键、bound 缺或非法／零 UUID、revision=0/负数、unbound 携带 ID/revision、unknown＋binding、unknown＋status 全拒；合法正 revision 上界；结果不产生成员／权限字段 |

### 7.2 `internal/repository/sql_spaces_binding_test.go`

| 测试名 | 必须辨别的真实路径／反例 |
|--------|-------------------------|
| `TestSpaceBindingFactsAndIsolation` | 通过新公共端口断言完整结果：active bound/unbound；未登记 tenant＋旧桶／无桶均实际确认 absence；disabled bound/unbound 保留 raw。alpha/beta 同 UUID、同名承载桶但不同 revision／tenant 状态不串；仅另 tenant 有绑定时本 tenant 仍 unbound；不存在的 spc-样式名不推 bound |
| `TestSpaceBindingInvalidInput` | 对公共方法逐字段跑严格输入反例，断零结果＋ErrInvalidArgument、无事实变化；精确大小写／无 trim，不把坏输入规范化成合法资源 |
| `TestSpaceBindingReadOnly` | 在有真实数据的夹具准备完成后，前后对比 tenants/buckets/spaces/space_members/space_history 的行数与完整逻辑值；覆盖已有对象／版本的 owner、StorageKey 及 local Blob 字节，不只数行。多种成功、非法输入及故障探测都无业务事实写入，不拿 SQLite 文件字节代替逻辑快照 |
| `TestSpaceBindingFaultsRetainCause` | 公共端口关闭 DB、预取消、过期 context；真实缺表／隔离 view 的坏 revision Scan。断 ErrPersistence、context Is／SQLite As、完整零值、不误 ErrNotFound/成功。特别对 unknown tenant＋不可查询 spaces 仍报错，防止只看 tenant 缺失提前 unbound |
| `TestSpaceBindingScanValidation` | 隔离 scanner 的原始 sentinel、sql.ErrNoRows、收尾错误 sentinel，断 helper Is cause／零结果；覆盖 tenant/Space partial NULL、unknown＋binding、请求键不匹配、非法 SpaceID／revision。模拟收尾仅标为 seam 覆盖，不宣称真实驱动 Close 故障已复现 |
| `TestSpaceBindingSQLBindingOccurrenceOrder` | 新 SQL 仅 SELECT、一次完整观察；首编号=1、按出现严格连续、无重复、实参顺序／数量匹配；真实 SQLite 执行、PG 原 SQL／rebind 静态。负例 `$1,$1`、`$1,$3`、`$2,$1`、`$2,$3` 必须被守卫识别，不能仅靠旧 I1 扫描范围 |
| `TestSpaceBindingStrictGetterCompatibility` | 同一夹具新 Probe 返回事实，旧 GetSpaceByBucket 未登记／disabled 仍 ErrTenantUnavailable；active bound 原 Space 不变、active absent 仍 ErrNotFound。保留旧 getter／spaceRead 源码与原测试，不重实现 P1A |

- 真实数据库一律 `t.TempDir()` 磁盘 SQLite、真实 Open、完整 Migrate；FK 保持开启。故障 view／缺表只在隔离测试库构造并清理，不是迁移修改或正常数据；不可达损坏组合优先纯值／scanner。
- 不改 PRAGMA/sync、连接、数据规模、超时、TMPDIR，不换 `:memory:` 或内存盘，不缓存 schema，不减少迁移，不加无用途全迁移夹具。纯领域及 scanner 反例不需造 DB。
- typed PG 错误如作合成校验必须明确标 synthetic；SQLite＋PG 静态／rebind 绝不称真实 PG 验证。

## 8. 红绿、复核与完整门禁序列（全部待执行）

1. 本阶段返回后先由控制器 docs checkpoint 核对两文档、准确范围、旧 Sprint 前缀及四 Go 不存在；本 Agent 不执行控制器检查点。通过后单一写者才进入四 Go。
2. 先新增本功能测试，留真正待实现红灯；缺符号编译失败只称新功能待实现，不当生产缺陷。再实现本合同，普通与 race 覆盖新端口及原严格 getter 兼容，不修改任何旧测试。
3. 至少一个稳定真实故障注入：仅暂改本片新 Go，将公共端口 DB 故障错误地返回 unbound,nil；已登记 `TestSpaceBindingFaultsRetainCause` 必须断言失败而非编译失败。finally 精确恢复原字节，同命令再绿。保存 before/injected/restored SHA、diff、command、exit、log；不删断言、skip、缩输入求绿。
4. 所有 Go 子进程固定 `GOMAXPROCS=4 GOFLAGS=-p=1`、umask 022；本片测试 `-count=1 -timeout 300s`。私有证据可 077；日志限本 RUN `local-implementation/`，不修改控制器脚本／latest／exit／旧证据。
5. 定向普通／race 仅前置证据，不能替代完整包／宏门禁。保留原 make 目标内部参数，不以改 Makefile 或环境覆盖原命令求绿；未修改的既有短模式子目标不等于完整 race。
6. 实现返回后由指定新 DeepSeek/max 会话只读复核四 Go、设计、原 getter、真实红绿／恢复，检查无授权／写副作用／装配、cause 不混 absence、一致观察及 I1；模型结论不能替代工程命令。
7. 再独立运行本 RUN `local-race.py` 的完整 repository＋spaces＋events local race：`-race -count=1 -timeout 300s -json`，无 run/short；包外 900 秒只收集、不延长包预算。最多三次且须有因果源码变化，同失败 codehash 不盲跑。
8. local 成立后，对当前同源串行执行七个原命令，红灯停止下游，保留真实 command/exit/log：
   - `GOMAXPROCS=4 GOFLAGS=-p=1 make check`
   - `GOMAXPROCS=4 GOFLAGS=-p=1 python3 cli.py accept`
   - `GOMAXPROCS=4 GOFLAGS=-p=1 make test-race`（原 `./cmd/server/ ./internal/...`、count=1、每包 300 秒、无 run/short）
   - `make web-check`
   - `GOMAXPROCS=4 GOFLAGS=-p=1 make build`
   - `make web-smoke`
   - `git diff --check`（只读检查，不操作 Git 索引／分支）
9. 控制器贯穿前后范围／同源／旧前缀／全部旧 Go 与迁移 hash 守卫；继承 Web 产物漂移也是阻断，不授权覆盖资产。最终新 Codex/max 上下文只读独立验收当前六路径及真实 local／七命令证据，不用旧成功抵扣。
10. 文档合同登记后仅末尾追加实际结果或显式勘误，不删改历史合同来刷绿。时间不足、范围漂移、必需证据缺失或红灯均保留现场停止，不延长固定截止，不自动回滚或重放。

## 9. 当前交付状态与未验证边界

- 本阶段仅完成 source read、需求合同写入与文档回读；未编码、未创建四 Go，未执行测试、故障注入、机械 scope/hash 检查或任何工程门禁。docs checkpoint 留返回后控制器执行。
- 真实 PG/Qdrant、外部身份／存储／KMS、HTTP2／代理、生产、压力／RSS、登录后业务 E2E 均未验证，费用 unknown。不运行 Docker／外部实库来冒充本片必需离线证据。
- 即使后续全部接受，也仅是未装配的绑定事实准备能力；不构成 Space 授权完成、全入口边界闭合、P1B 全部完成或 P2 产品交付，无提交／部署许可。

## 10. 第二串行实现写者实际结果（2026-10-19 01:10–01:27 UTC）

上文“本阶段未编码／待执行”保留为文档阶段历史，固定合同未删改。已完整回读本 RUN documents-source.json 与本文；runner.log 记录控制器 docs checkpoint exit 0（准确两文档），之后才新增四 Go。未自行运行其他控制器检查点。

- 新增 `internal/spaces/binding.go`（81 行）、`binding_test.go`（100 行）、`internal/repository/sql_spaces_binding.go`（110 行）、`sql_spaces_binding_test.go`（431 行）；两文档仅追加结果，无第七项目文件。SQL 只在新具体实现，一次 SELECT／两个独立参数，未装配、不扩旧接口、不写业务事实；unknown／disabled／其他 raw status 均按既有行返回绑定事实，fault 为零结果＋保因果错误，异常 NoRows 不落旧 absence 分类。
- 9 个新顶层测试覆盖 §7 登记矩阵；真实磁盘 SQLite＋完整 Migrate，复用原 helper 而不改它们；未调 FK／PRAGMA／sync／连接／数据规模／TMPDIR。只读测试比较六表完整逻辑行（含 objects 内版本、非空 `_aero_owner`、StorageKey）与三个 local Blob 的原字节；纯值／scanner 反例没有另造数据库。
- `02-new-feature-red` exit **1**：缺新增符号的待实现编译红灯，不是生产缺陷。`04-targeted-normal` exit **1**：新夹具期望仍持有成员命令前 UpdatedAt；只修新夹具，重新读取完整 post-command 根再作精确比较，未删字段或修改旧 helper。原失败日志保留。
- 最终 `07-final-normal`／`08-final-race` 均 exit **0**：9 个新顶层＋4 个原 P1A getter／tenant／snapshot 顶层全 PASS。命令均为 `go test [-race] -v -count=1 -timeout 300s ./internal/spaces/ ./internal/repository/ -run <各 .command 的完整表达式>`；repository 包分别 12.491s／6.998s。`10-vet`（两包）exit **0**；`06-final-contract` exit **0**，检查新文件长度／函数 ≤50／复杂度 ≤10、无 init、生产只读与无测试预算捷径。定向均不是宏门禁。
- **真实故障注入：** 只暂改新 `sql_spaces_binding.go`，将公共 Probe 的 DB fault 伪装为 unbound,nil。相同命令 `go test -v -count=1 -timeout 300s ./internal/repository/ -run '^TestSpaceBindingFaultsRetainCause$/closed_database$'`：`09a-before` exit **0** → `09b-injected` exit **1**（取消／过期及 closed_database 的真实结果／cause 断言失败，无编译失败）→ finally 精确恢复后 `09c-restored` exit **0**。
  - before／restored SHA256：`8ac2ffba038eab387f0bc1d945e2bd73f8b2ea295804c3ea0627c3951caee744`；injected：`c7ab8f2bf748d56f6c890a4d383649e0e029bbed763fc050937b5799d88e43e3`。
  - `09-injection-evidence.json`、`09-injection.diff`、before／injected 副本、三组 command／exit／log／SHA 均保留；恢复 diff 为空且四 Go 与最终定向 race SHA 完全相同。
- **当前源完整三包 local race：** 唯一一次 `python3 <本 RUN>/local-race.py local` exit **0**；实际 argv 为 `go test -race -count=1 -timeout 300s -json ./internal/repository/ ./internal/spaces/ ./internal/events/`，无 run／short。repository **145.909s**、spaces **1.007s**、events **26.970s**，wall **176.804s**；248 个顶层 PASS，无 failed／unfinished，`source_unchanged=true`，`code_sha256=b792dc80e38e1a2969639cb94fe44a7121c56da129d9f08012e7705330352250`。已从真实 JSONL 核对全部 9 新例与 4 原兼容例 PASS；不是完整宏门禁或 accepted。
- Go 子进程全为 `GOMAXPROCS=4 GOFLAGS=-p=1`、umask 022，测试 count=1／每包 300 秒。`scope.py verify` 已 exit **0**、准确六路径；原 Go／测试／迁移／文档前缀与 Git 所有权由该只读守卫核对，未操作索引／分支／提交部署。固定截止仍为 `2026-10-19T04:50:22Z`。

证据根：`/home/u1/.abr/projects/aero-vault-5c281c8a1991a53d/runs/p1b-binding-probe-sol-flash-20261019T005154Z/`。本地 command／exit／log／SHA／diff 在 `local-implementation/`；完整三包原日志及结果在 `local-full-race/attempt-001/{test.jsonl,result.json}`。`13-real-log-summary.json` 只是原 JSONL 核对索引，不代替原日志。

**返回后仍待：** 新 DeepSeek/max 只读复核、控制器当前同源复核及原七宏门禁、新 Codex/max 独立验收；本写者没有运行这些门禁或伪造 accepted。PG 仅静态 SQL／rebind 与 synthetic typed error，未跑实库；late Close error 仅 scanner seam 注入，未复现真实驱动 Close 故障。其他真实外部／生产／压力／业务 E2E 边界仍未验，费用 unknown；本片不是 Space 权限交付。
