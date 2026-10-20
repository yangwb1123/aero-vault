# SPACE-P1A：个人／团队空间持久化基础 v1

## 0. 状态、授权与阶段边界

**状态：阶段 B 已实际新增领域／窄 Store、具体 SQL 事务、四份迁移和测试；最终定向普通／race 均 exit 0。完整机械门禁及独立验收待执行；未接线，非空间产品交付。** §2–§8 保留阶段 A 的编码前设计和验收合同；“候选／拟／待执行”不冒充本轮运行记录，实际符号、红绿灯和边界见 §9。

用户对紧邻的精确确认问题回答“继续”，仅批准 Space、成员、历史的持久化及事务／租户隔离测试、四份新迁移和 Sprint 限定增量，暂不开 API/UI、不提交部署。阶段 A 当时只修改 `docs/agent/CURRENT_SPRINT.md` 与本文；其机械守卫已通过，阶段 B 动码前又用 `preflight` 核对当前源码与 `scope-source.json` 完全一致、仅两文档差异、新增路径及全部 `0048_*` 未占用（exit 0）。本文件不是继续 P1B/P2、开放 API 或全空间设计的许可。

旧运行目录中的 `design.md`、`design-review.md`、`implementation-scope-p1a.proposed.md` 的“未批准／只读／Proposed”保留为历史，不改写；不据此反复请求同一精确确认，也不把其宽范围设计纳入本片。`preflight=READY` 只是预检证据。原截止仍为 `2026-10-17T09:24:09.871261+00:00`，不重置窗口。

## 1. 必要性与最小范围

现有 `mine` 只是可信 owner 与当前读取权限的交集；桶是文件承载，不是 Space。需要独立关系保证个人关联唯一、成员命令并发一致和历史原子性，不能把这些事实写进对象 metadata、JSON 文件或进程 map。

本片仅拟新增：

1. `internal/spaces` 的领域值、验证、错误及窄 Store 端口；不依赖 repository、HTTP、UI 或具体身份 SDK。
2. 现有具体 `*sqlStore` 在新文件中实现该端口，复用当前连接和事务能力；不复写／扩宽 Repository、Storage、access.Store 或其他公共接口。
3. `spaces`、`space_members`、`space_history` 三表及必要索引、四份 0048 迁移、隔离离线测试。必要承载桶元数据与创建同事务，但不写任何 Blob。

不做：生产装配、REST/S3/WebDAV/MCP/CLI/SDK/UI/OpenAPI、完整应用授权、ACL 写入、对象／版本 provenance、multipart 绑定、接管／移交／冻结／归档流程、外部人员目录同步、计费或治理投递。角色只有关系标签，不自动获得读取、delete/share/ACL/管理等应用权限；不新建权限目录或审计外发协议。

即使基础片以后验收通过，也不能创建对外可用空间。P1B 仍需另定范围处理 nil/disabled/system、owner/admin、原桶、分享／公开 capability、预签名、multipart 重放、SSE、AI、管理快捷路径等安全边界；P2 产品闭环不在此许可内。无接线不是旧协议天然安全的证明，不能在实际工作库预先创建承载桶。

## 2. 本阶段实际查阅的底座锚点

以下“源码确认”不等于本轮运行验证；不从旧报告推导当前 PASS。

| 路径／符号 | 确认的事实及设计影响 |
|---|---|
| `docs/agent/BOOTSTRAP.md` → `AGENTS.md` → `docs/agent/CURRENT_SPRINT.md` → `docs/agent/TASK.md` → `HARNESS.md` | 已依次完整读取；保留旧目标及禁项，仅追加本片例外。 |
| 本次运行的 `TASK.md`、`allowed-paths.json`、`setup.json` | 已首先完整读取；限定切片、只新增 Go、四迁移、阶段 A 仅两文档；基线 HEAD／脏状态／备份信息是上游记录，未在本会话重算。 |
| 旧 `design.md` §0–4.6、`design-review.md`、`implementation-scope-p1a.proposed.md` | 读取本片必要提案与评审，不采用其未批准的 API、provenance、接管或完整产品范围。 |
| `internal/repository/sql.go::sqlStore/rebind/Migrate/applyMigration/listMigrationFiles` | embed 两方言 SQL；只自动执行未应用 up，逐迁移事务；SQLite 按出现顺序替换每个 `$N`，编号不能复用。 |
| `internal/repository/sql_instrument.go::newSQLStore/timedDB.BeginTx`；`sqlite.go::openSQLite`；`postgres.go::openPostgres` | 已有 `*sql.DB`／`*sql.Tx`；SQLite 当前单连接、WAL、foreign_keys=ON；PG 使用 pgx。不能为新测试改 Open、连接参数、全局重试或 Migrate。 |
| `internal/repository/repository.go`、`repository_interface.go::Repository/Open` | 已有 Object、BucketConfig、TenantRecord 及对象仓储契约；新窄能力只由具体 SQL 类型实现，不嵌入或复制这些接口。 |
| `internal/repository/sql_buckets.go::CreateBucket/DeleteBucket` | CreateBucket 忽略冲突，不能认领旧桶；DeleteBucket 会先删除关联元数据，FK 不等于安全的 Blob 删除前置门。 |
| `internal/repository/tenants.go::GetTenant/UpsertTenant/DeleteTenant` | 物理表是 `tenants`，status 使用 active/disabled；旧隐式租户兼容存在。本片不改旧行为，新命令显式要求已登记 active 租户。 |
| `internal/repository/sql_objects.go::InsertObjectVersion`、`sql_helpers.go::defaultTenant/NewVersionID/uuidLike` | 既有版本事务和 StorageKey 不改；新端口不套用空 tenant→default，也不把 uuidLike 当标准 UUID 生成器。 |
| 两方言 `0001_init.up.sql`、`0002_multitenant.up.sql`、`0005_versioning_tagging.up.sql`、`0015_tenants.up.sql`、`0037_enterprise_access.up.sql` | 当前 buckets 主键 `(tenant_id,name)`、tenants 主键 tenant_id 已能支撑新 FK；旧部门成员缺完整来源维度，不能替代本片成员键。旧桶 created_at 的 SQLite TEXT／PG TIMESTAMPTZ 类型不改。 |
| 两方言 `0047_bucket_access_logs.{up,down}.sql`；四个指定 0048 路径 | 0047 四文件实际存在且已读；四个 0048 目标读取均 ENOENT。没有创建／应用任何 SQL。 |
| `internal/access/{types.go,store.go,authorizer.go}`、`sql_access_directory.go`、`sql_access.go` | 有窄端口实现先例，但 Principal 不含完整来源，授权有独立回退；本片不改 ACL／部门关系或授权器。 |
| `internal/auth/principal.go::PrincipalForKey` | 缺 SubjectID 可生成 apikey 派生 ID 且默认 user，不能据 user 标签证明员工资格。 |
| `internal/service/access.go`、`file_listing.go::ListWithOptions`；`docs/requirements/owned-file-view-v1.spec.md` | owner/mine 不是 Space，不回填 owner、不替换 mine、不改变旧文件授权。 |
| `cmd/server/main.go::run/runMCP` | 装配及 billing/audit 包装保持；不新增 spaces 捕获、Manager 或启动副作用。 |
| `internal/repository/{sql_access_acl_test.go,tenants_test.go,buckets_keys_test.go}`；`internal/storage/local.go::NewLocal` | 已有 testing、Open/Migrate、t.TempDir、窄端口类型断言及 local FS 测试方式可沿用。 |
| `go.mod`；`docs/agent-context/platform-contract.md` | 已有 google/uuid、pgx、modernc SQLite；无新依赖。身份／组织主数据不在 Vault 复制，不跨项目数据库写入。 |
| `Makefile`、`checks/{acceptance.py,invariants.py,filesize.py}`、`.github/workflows/ci.yml` | 后续使用实际门禁，不采用 HARNESS 旧“等价步骤”；现 I1 扫描并不全面覆盖新 SQL，须另加有辨别力测试与审查。 |

为定位实际文件名，还读取了上游 `snapshot-origin.json` 与其指向的 `evidence/010/validated-source.json` 的相关路径片段：历史清单两方言止于 0047。这不是当前全目录／Git／哈希校验。会话只有 read/edit/write，目录读取返回 EISDIR；**同号异名 `0048_*` 的当前枚举仍由阶段 B 前机械守卫完成**。如占用即停止报告，不覆盖、不改旧迁移、不跳号。

## 3. 领域与窄端口候选

### 3.1 值与边界

- `SubjectRef = {Source, Kind, ID}`：Source 为明确的身份命名空间／验证来源，Kind 首片仅 user/service，ID 为来源内稳定 ID；三者均必填，区分大小写、不拼接裸 ID、不按姓名／邮箱合并。输入不得为空白、非法 UTF-8 或含控制字符，不静默裁剪／改写标识。远程 ID 按不透明值保存，不另建人员／组织主档或跨库 FK。
- SubjectRef 仅是**调用方已解析身份的值引用**；Source 非空、Kind=user 均不证明员工在职或可信资格。本片不从当前 Principal 自动补造 Source、不增加认证路径、不接受它作为对外准入证明。
- `Space`：tenant、UUID ID、kind=personal/team、显示名、稳定承载绑定、可空原始个人关联、正整数 revision、创建／更新操作者与时间。不增加生命周期命令；本片不需要 frozen/archived、责任移交或外部部门模型。
- `Member`：完整 SubjectRef、viewer/editor/admin 标签、active/revoked 状态、最后变更根 revision、操作者及时间。只在本 Space 内有效，不是 Snaplink 全局角色或外部组织成员关系。
- 所有操作显式给非空 tenant；拒绝空白、控制字符及 `*`，不继承 operator 全租户查询，不调用 defaultTenant。已登记 active 才能读写本窄能力；未知／disabled 返回明确租户不可用错误，不自动 UpsertTenant。
- Space／命令／历史 ID 使用已有 google/uuid 的标准随机 UUID 能力，生成错误必须处理，拒绝零 UUID／非规范 ID；SubjectRef.ID 不强改为 Vault UUID。显示名为 1–200 个有效 Unicode 字符且非纯空白，不作团队唯一键；这是新值校验，不是新配置项。
- revision 使用 int64，创建后为 1；成员命令要求 `1 <= expected_revision < MaxInt64`，非法值在任何写入前拒绝，不允许溢出。

### 3.2 拟提供的内部能力（不是公共 API）

`spaces.Store` 仅含以下语义，不嵌入 Repository、不复写文件 CRUD：

| 候选方法 | 明确输入／结果 |
|---|---|
| `CreateSpace` | tenant、调用方预生成 Space UUID、kind、name、actor 完整 SubjectRef、command UUID；actor 必须为 user 并成为唯一初始 active admin。personal 原关联固定为该 actor；team 原关联为空。返回提交后的 Space。 |
| `GetSpace` / `GetSpaceByBucket` | tenant＋Space ID／承载桶；不提供无 tenant 反查。 |
| `GetPersonalSpace` | tenant＋完整 user SubjectRef；只查原始个人关联，不从成员角色、owner 或名字猜测。service 查询参数明确报无效值。 |
| `GetSpaceMember` / `ListSpaceMembers` | tenant＋Space ID；单成员再带完整 SubjectRef；列表按完整三元组排序，游标包含三元组，分页 limit 为 1–1000。 |
| `ListSpacesForSubject` | tenant＋完整 SubjectRef、Space ID 游标及 limit；只返回 active 成员关系关联的 Space，**不是已授权文件可见性**。使用完整身份索引，不授予内容访问。 |
| `ListSpaceHistory` | tenant＋Space ID、after_revision（0 表示从头）、limit；按 after_revision ASC；负游标非法。 |
| `ChangeSpaceMember` | tenant＋Space ID＋expected_revision＋actor＋command UUID＋目标完整 SubjectRef＋add/change_role/revoke/reactivate 之一；返回提交后 revision。此为内部持久命令，不做应用权限签发。 |

除 ListSpacesForSubject 的“无关联”和已存在 Space 的有效空页外，未知 Space／成员不得伪装为空成功。查询 tenant 状态、根存在性与结果使用同一读事务；SQLite 使用其读快照，PG 读取租户行时 `FOR SHARE` 并保持到提交，避免多次查询间混入状态变更。关闭 rows、检查 Scan/rows.Err/Commit 错误，读事务内不回调 s.db 的外部查询。

## 4. Persistence Design（编码前，候选 DDL 合同）

只新增三表；SQLite 整数列用 INTEGER，PG 用 BIGINT，枚举／标识用 TEXT。新 `*_at_ns` 为 Unix 纳秒 int64，不写 RFC3339 字符串；旧表时间遵守其既有类型，文本写 UTC RFC3339Nano。SQLite 对所有 revision／`*_ns` 列增加 `typeof(列)='integer'` CHECK，可空 revoked_at_ns 则为 NULL 或 integer，防止类型亲和性接受时间文本／REAL；PG 由 BIGINT 类型约束。以下每组展开为实体列，不用 JSON／CSV 存当前关系；首片历史也采用显式列，不要求 JSON。

### 4.1 `spaces`

| 候选字段 | 非空／约束 |
|---|---|
| `tenant_id`, `id` | NOT NULL，复合 PK `(tenant_id,id)`；id 为规范 UUID，无按裸 ID 查询。 |
| `kind`, `name` | NOT NULL；kind CHECK personal/team；name 非空，领域施加长度／字符校验。 |
| `backing_bucket` | NOT NULL、非空；UNIQUE `(tenant_id,backing_bucket)`。 |
| `personal_source`, `personal_kind`, `personal_subject_id` | personal 行三列全部非 NULL、非空且 `personal_kind='user'`；team 行三列全部 NULL。用显式 IS NULL/IS NOT NULL 的条件 CHECK，不能让 NULL 的 UNKNOWN 绕过检查。 |
| `revision` | NOT NULL、CHECK revision >= 1；只有成员命令可增长。 |
| `created_by_source/kind/id`, `updated_by_source/kind/id` | 各三列 NOT NULL、非空；kind CHECK user/service。创建后 created_by 不再写。 |
| `created_at_ns`, `updated_at_ns` | NOT NULL 整数纳秒、CHECK > 0；更新时写真实命令时间，不把时间当并发序号。 |

数据库个人唯一索引 **`spaces_personal_subject_uq`**：`UNIQUE (tenant_id,personal_source,personal_kind,personal_subject_id) WHERE kind='personal'`。不能只在进程／事务外查重；不能遗漏 Source 或 Kind；无删除／换绑命令，因此成员撤销不会释放原始个人关联。同名 personal/team 均不按 name 合并，团队可有多个同名 Space。

新增 FK：`tenant_id -> tenants(tenant_id)`，`(tenant_id,backing_bucket) -> buckets(tenant_id,name)`；均 RESTRICT 删除／更新，绝不级联删旧文件或桶。不需为此修改旧表／旧唯一键。FK 仅维护引用完整性，不替代未来原桶删除前置授权。

ID、kind、personal 三元组、backing_bucket 在本端口中不可变，SQL 更新白名单只有 revision、updated_by 三元组及 updated_at_ns；不提供 rename、换桶或释放个人关联命令。Space UUID 同时用于派生 `spc-<规范UUID>` 的承载桶名，不允许调用方传任意旧桶绑定。

### 4.2 `space_members`

| 候选字段 | 非空／约束 |
|---|---|
| `tenant_id`, `space_id`, `subject_source`, `subject_kind`, `subject_id` | 全 NOT NULL，复合 PK 为这五列；身份非空、kind CHECK user/service。 |
| `role`, `state` | NOT NULL；CHECK viewer/editor/admin 与 active/revoked。 |
| `changed_revision` | NOT NULL、CHECK >= 1；记录本次实际提交的根 revision。 |
| `changed_by_source/kind/id` | 完整操作者三列 NOT NULL、非空；kind CHECK user/service。 |
| `joined_at_ns`, `updated_at_ns`, `revoked_at_ns` | 前两列 NOT NULL、CHECK > 0；revoked_at_ns 可空；active 必须 NULL，revoked 必须非 NULL 且 > 0。再加入保留首次 joined_at，更新其余变更事实。 |

FK `(tenant_id,space_id) -> spaces(tenant_id,id)`，删除／更新 RESTRICT；无远程 Subject FK。撤销保留当前 revoked 行，不物理删除身份关系；重新加入只能走显式 reactivate。

索引：
- `space_members_subject_idx (tenant_id,subject_source,subject_kind,subject_id,state,space_id)`：ListSpacesForSubject。
- `space_members_admin_idx (tenant_id,space_id,state,role,subject_kind)`：同根最后有效人类管理员检查。
- 单成员／成员分页使用已有复合 PK，不重复另建等价索引。

“有效人类管理员”仅指本地关系 `state=active AND role=admin AND subject_kind=user`，Source/ID 完整；service admin 不计数，revoked user 不计数。这不是在职／真人的外部证明。

### 4.3 `space_history`

| 候选字段 | 非空／约束 |
|---|---|
| `tenant_id`, `id`, `space_id`, `command_id` | NOT NULL；PK `(tenant_id,id)`；历史 id 和 command_id 为 UUID。 |
| `command_type` | NOT NULL；CHECK create/member.add/member.change_role/member.revoke/member.reactivate。 |
| `actor_source/kind/id`, `target_source/kind/id` | 两套完整 SubjectRef，NOT NULL、非空、kind CHECK user/service；create 的 target 为初始管理员。 |
| `before_revision`, `after_revision` | NOT NULL；CHECK before_revision >= 0 且 after_revision = before_revision + 1 且 after_revision > 0；另 CHECK create 固定 0→1，其他命令 before_revision >= 1。 |
| `before_role`, `after_role`, `before_state`, `after_state` | 后态两列 NOT NULL；前态两列仅 create/add 时为 NULL，其余命令均非 NULL，以按 command_type 的 CHECK 约束；非 NULL 限相同 role/state 枚举，记录真实前后态。 |
| `reason`, `request_id`, `occurred_at_ns` | 前两列 NOT NULL，允许空字符串；时间 NOT NULL、CHECK > 0。不保存令牌、文件内容或人员主档。 |

FK `(tenant_id,space_id) -> spaces(tenant_id,id)`，RESTRICT；UNIQUE `(tenant_id,command_id)` 防同租户命令 ID 重复提交；UNIQUE `(tenant_id,space_id,after_revision)` 约束每个根版本至多一行历史，命令事务再保证每个成功版本必有一行事实；该唯一索引直接支撑历史 keyset 查询，无需重复时间索引。

历史只允许内部事务追加；不提供独立 append/update/delete 端口，不把它当外部 AuditSink／Outbox 的交付承诺。根与成员的跨表一致性由命令事务保证，CHECK/FK 本身不能证明最后管理员仍存在。以后如用 JSON，只可作非权威历史快照，不能替代这些当前关系键／角色。

### 4.4 创建、个人唯一与严格承载事务

- 创建候选 Space UUID／command UUID 先验证；承载名由 ID 派生，不从实际工作数据选桶。只允许新建空桶元数据，无 ACL grant、版本策略决策、对象或 Blob 副作用，旧桶配置默认值不复制到新代码。
- SQLite：BeginTx 后先严格 `INSERT INTO buckets`（不忽略冲突）取得写事务，再在该事务读取 tenant 并要求已登记 active；错误全回滚。这样不先读旧快照再升级写锁；当前单连接只在同一 Open 内串行，不能当跨 Open／进程并发证明。
- PG：BeginTx 后先读 tenant `FOR SHARE` 检查 active 并持锁，再严格 INSERT 桶；不使用 CreateBucket，不用 ON CONFLICT DO NOTHING。桶 created_at 依现存方言类型绑定，SQLite 写 RFC3339Nano 文本，PG 写时间值。
- 同一 `*sql.Tx` 依次插入 Space(revision=1)、初始 active user admin(changed_revision=1)、create 历史(0→1)，然后 Commit；不调用会另开连接／事务的既有仓储方法。任何步骤失败回滚该事务，桶也不得残留。
- 并发 personal 创建由 `spaces_personal_subject_uq` 最终裁决。约定赢家成功，唯一冲突方回滚整个事务并返回 ErrConflict，可在赢家提交后用 tenant＋完整 SubjectRef 的 GetPersonalSpace 恢复同一持久关联；不覆盖赢家显示名，不补第二成员／历史，不在 PG 已失败事务中继续读。
- SQLITE_BUSY/LOCKED 或 PG 事务竞争可返回可恢复 ErrConcurrent；调用方在 context 有效时用新事务显式重试，不在仓储隐藏无限重试，也不改既有重试预算。测试须等已知赢家提交后核对唯一关系与无孤儿；不能把所有错误都容忍为成功。
- 同名 team 用不同 ID／command ID 可各自成功；重复 ID、承载桶或 command ID 是冲突，不按名称合并。任何已存在桶（即使空桶、同租户、同名派生桶）都绝不被认领。

### 4.5 成员 CAS、最后管理员与历史事务

1. Validate tenant／ID／actor／target／动作／角色／command ID／expected_revision。BeginTx；**先** `UPDATE spaces SET revision=revision+1, updated_by_source=$1, updated_by_kind=$2, updated_by_id=$3, updated_at_ns=$4 WHERE tenant_id=$5 AND id=$6 AND revision=$7`，按出现顺序绑定七个实参。SQLite 的首次写／PG 根行更新锁将所有该 Space 的成员命令序列化。
2. 读取 RowsAffected 并传播其错误；无论 0 或 1 行，均先在该事务检查 active tenant（PG 对 tenant 读 FOR SHARE），未知／disabled 返回 ErrTenantUnavailable 并回滚；其他异常行数视为持久化错误。
3. tenant 有效但 0 行时，再按 tenant＋ID 区分未知根（ErrNotFound）和旧 revision（ErrConflict）；1 行才继续读取目标当前行及有效 user admin 数。不得只读 revision 后无条件更新，不得转调 s.db、事务外计数或依赖进程锁。
4. add 只接受未出现的身份；已有 active→ErrMemberExists，已有 revoked→ErrMemberRevoked。change_role/revoke 要求 active，reactivate 要求 revoked；未知→ErrNotFound；重复撤销→ErrMemberRevoked；相同 role 的无变化请求→ErrConflict。所有失败均回滚预增根 revision。
5. 若将 active user admin 降级或撤销，且当前有效 user admin 数为 1，返回 ErrLastAdmin 并回滚。service 或已撤销管理员不能充当替代。首片不提供绕过／接管／批量移除路径。
6. 更新唯一目标成员行，将 changed_revision 写为新根 revision；追加同事务历史（完整 actor/target、真实前后态及旧／新 revision），最后 Commit 才返回成功。独立历史写失败、重复 command ID、更新行数异常均回滚成员和根。

两个管理员以同一旧 revision 并发互撤／降级时至多一笔提交；另一笔是 stale CAS 或可恢复数据库竞争。它以最新 revision 重试时必须被 ErrLastAdmin 拒绝，而非两人都退出。独立两个 Open 的测试必须验证此结果，不能只测单连接 goroutine 串行。

本合同不授予 actor 成员管理权限：actor 值仅记事实，调用方还没有安全入口。关系存储存在绝不等于全入口应用授权完成；不能将此端口挂到旧 Manager／FileService 作为许可来源。

### 4.6 错误、取消与恢复

拟用 `errors.Is/As` 可辨别领域错误：ErrInvalidArgument、ErrNotFound、ErrTenantUnavailable、ErrConflict、ErrMemberExists、ErrMemberRevoked、ErrLastAdmin、ErrConcurrent、ErrPersistence。名称为待实现合同，不声称已有符号。

- 本地值错误先拒绝；已知唯一键冲突／stale CAS 归 ErrConflict，成员语义按上文返回。跨租户查询按给定 tenant 的不存在／不可用处理，不泄露另一租户事实。
- PG 使用现有 pgx 的类型化 PgError/SQLSTATE（23505 且核对相关约束；40001/40P01 等事务竞争）；SQLite 使用现有 modernc 错误代码区分 UNIQUE/PK 与 BUSY/LOCKED，不用错误字符串猜测。FK/CHECK/未知 DB 错误不得一律当“个人已存在”。
- 其他 SQL／Scan／rows.Err／RowsAffected 错误包装 ErrPersistence 并保留 cause；context.Canceled/DeadlineExceeded 必须可被 errors.Is 识别，不伪装空列表、ErrNotFound 或成功。
- 不更改 Open/Migrate、连接池／PRAGMA／超时、已有仓储方法或重试策略来消除失败；所有回滚/关闭沿现有 defer 方式，不新增依赖或生产故障注入开关。
- Commit 返回错误不得报成功；若提交结果不确定，保留原始错误，用原 tenant＋Space ID／个人关联及历史 command ID 查证，不声称一定未提交，也不盲重放。本片不承诺 HTTP 幂等、通用命令重放或 exactly-once。

## 5. 迁移与旧数据零回填

阶段 A 已先在 Sprint 末尾记录唯一迁移例外，当时没有 SQL 改动。阶段 B 实际只新增：

- `internal/repository/migrations/sqlite/0048_spaces_foundation.up.sql`
- `internal/repository/migrations/sqlite/0048_spaces_foundation.down.sql`
- `internal/repository/migrations/postgres/0048_spaces_foundation.up.sql`
- `internal/repository/migrations/postgres/0048_spaces_foundation.down.sql`

动码前机械枚举两方言所有 `0048_*`、核对新增路径未被占用；冲突停止，不改名跳号。up 只建三空表／约束／索引，不 ALTER/UPDATE/INSERT/DELETE 任何既有业务表，不自动纳管 default 或其他旧桶，不从 owner 推导员工／个人空间。四文件随既有 embed/Migrate 机制生效，无启动用例、无初始化样本数据。

down 仅按 history→members→spaces 的依赖顺序移除本片新表／索引，不删 buckets、tenants、旧对象或 Blob。只在隔离临时库的空基础表验证，不在工作库运行；不存在自动 down runner。将来有空间数据后不能靠 down 或无安全边界的旧二进制回退，必须保留事实并另审前滚恢复方案。

零回填包括旧 tenant 隐式兼容、bucket 配置、Key、StorageKey、version_id、历史无 owner 对象、分享 URL 全部原样；没有数据搬移、owner 补写、版本重编号，也不把旧数据称为默认私有。新 FK 仅影响新关联行，不授权在原桶删除／租户管理处新增接线。

## 6. 准确拟新增 Go 文件子集

以下 **21 个**路径是阶段 A 声明、逐项属于本次 `allowed-paths.json` 的准确子集，阶段 B 已全部实际新增（11 个实现／端口文件、10 个测试文件）；没有通配符扩容许可。仍未选用 `internal/spaces/store_test.go`、`internal/repository/sql_spaces_test.go`，没有额外 Go 文件或既有 Go／测试修改；没有扩大预算或接口。

| 精确路径 | 职责 |
|---|---|
| `internal/spaces/types.go` | SubjectRef、Space、Member、History、命令及分页值。 |
| `internal/spaces/store.go` | §3.2 窄端口；无应用装配／授权逻辑。 |
| `internal/spaces/errors.go` | §4.6 可辨别领域错误。 |
| `internal/spaces/validate.go` | 显式身份／tenant／UUID／revision／动作校验。 |
| `internal/spaces/types_test.go` | 完整身份值及枚举／命令结构的区分。 |
| `internal/spaces/validate_test.go` | 正反例、非法值、revision 边界。 |
| `internal/repository/sql_spaces.go` | 具体 SQL 类型对新窄端口的编译断言、行扫描及字段定义。 |
| `internal/repository/sql_spaces_commands.go` | 创建命令、严格新桶、初始事实的事务编排。 |
| `internal/repository/sql_spaces_queries.go` | tenant 限定根／个人／承载／主体关系查询。 |
| `internal/repository/sql_spaces_members.go` | 单成员／分页及成员命令、最后管理员裁决。 |
| `internal/repository/sql_spaces_history.go` | 事务内历史追加和 revision 分页，无独立外部写入口。 |
| `internal/repository/sql_spaces_tx.go` | 本片读写事务、tenant 检查、根 CAS；不改全局事务策略。 |
| `internal/repository/sql_spaces_errors.go` | 现有驱动类型化错误映射、cause 保留。 |
| `internal/repository/sql_spaces_fixtures_test.go` | 临时 SQLite/local、租户、SubjectRef、并发屏障及断言设施。 |
| `internal/repository/sql_spaces_create_test.go` | 两类创建、严格承载、同名团队及个人唯一。 |
| `internal/repository/sql_spaces_members_test.go` | 成员状态转换、旧 revision、最后管理员。 |
| `internal/repository/sql_spaces_history_test.go` | 真实前后态、命令／revision 唯一、历史分页。 |
| `internal/repository/sql_spaces_queries_test.go` | tenant/source/kind 隔离、未知根、主体查询和分页。 |
| `internal/repository/sql_spaces_concurrency_test.go` | 多 Open 的个人竞争、两管理员互撤／降级。 |
| `internal/repository/sql_spaces_migration_test.go` | 四迁移、SQLite 约束／空表 down、旧数据／重开兼容。 |
| `internal/repository/sql_spaces_failure_test.go` | 各步骤回滚、DB/context 错误和驱动分类。 |

依赖方向仅 repository→spaces，spaces 用 stdlib 值／context 等及已存在 UUID 能力；不加模块／配置、不运行 go mod tidy。文件 ≤500 行、函数 ≤50 行、复杂度 ≤10，避免 God 类型；不调整 engineering.yaml、豁免、AGENTS 或门禁。不得为便于 mock 改旧接口。所有新测试用 testing，无 testify/gomega。

## 7. 有辨别力的待执行测试矩阵

**以下为阶段 A 的验收矩阵；阶段 B 的实际映射见 §9。** 已先补失败测试保存红灯，再实现与补绿。缺新增领域／实现造成的编译失败仅为待实现功能，不伪称发现生产回归。每项必须有正对照，不能只断言“没报错”；PG 静态／合成检查不等于实库验证。

| 编号 | 场景与必要断言 |
|---|---|
| V1 | tenant 空／`*`、缺 Source/Kind/ID、非法 UTF-8／UUID、未知枚举、空名／超长名、expected_revision=0/负数/MaxInt64、负历史游标：明确值错误，数据库及 revision／历史不变；合法边界成功。 |
| I1 | 两 tenant 中同裸 ID／同名；同 tenant 中不同 Source／user 与 service 同裸 ID；根、个人、单成员、主体空间、成员／历史分页均不串。personal 的 service 输入明确拒绝，team 中 user/service 关系可分别存在。 |
| P1 | 同租户同完整 user ref 用不同 UUID／command 并发创建：恰一根、一桶、一初始成员、一历史；败者仅是可恢复冲突／竞争并可读回赢家；不同来源／tenant 可各自唯一，同名 team 可并存。直接 SQL 反例证明 UNIQUE 由数据库裁决。 |
| A1 | 在临时库对 bucket→Space→初始 member→history 各后续步骤以测试专用 SQLite trigger RAISE(ABORT) 注入失败：四类行全回滚、重试可成功。预置同租户空／非空派生桶后创建必须冲突，旧桶／对象字节及配置不变、无关联。无生产注入开关。 |
| M1 | add/change_role/revoke/reactivate 正路径；未知、重复、已撤销、相同 role 与旧 revision 反例不抬 revision／不写历史。不同来源／kind 的目标不可互相覆盖，撤销不改个人原关联。 |
| C1 | 两管理员以同 revision 并发互撤、互降级、混合撤销／降级：至多一笔提交；败者刷新 revision 后 ErrLastAdmin。service admin／revoked user 不充数，始终保留有效 user admin；失败无部分成员／历史。 |
| H1 | create 0→1 与每次成功 +1；actor/target 来源／kind 完整、前后态真实、command ID 和 after_revision 唯一；历史插入失败／重复 command 导致根与成员一起回滚，分页无重复／遗漏。 |
| T1 | 未登记、disabled 租户读写拒绝，不自动建租户；空／`*` 不回落 default。另租户不存在不能读回同 ID；已存在 Space 的合法空页与未知 Space 可区分。 |
| E1 | 取消／到期 context、关闭 DB、Scan/rows 错误及类型化驱动错误保持可辨别，不伪装空／未找到／成功。SQLite 真实 UNIQUE/FK/CHECK/BUSY 类与 PG 合成 PgError 分类分开记录；合成错误测试不等于实库。 |
| R1 | 同一文件库 Close→重新 Open→Migrate，根／成员／历史仍在且 0048 应用记录不重复；多次 Migrate 跳过已应用 up，不能假定自动 down。 |
| U1 | 从临时 0047 库种旧 default/其他桶、无 owner 对象／版本与 local Blob，再应用 0048：新表仍空，旧 ID/version/StorageKey/owner/内容／配置不变；后续既有 CRUD 与原测试保持。四迁移存在，SQLite 真实 PK/个人唯一/复合 tenant FK/CHECK 可辨别，空表 down 保留旧表／旧数据；PG 仅静态对称审查。 |
| N1 | 新包和具体 SQL 窄能力可编译断言；最终范围／导入／装配审查确认没有 main/router/FileService/Manager/UI/CLI/协议调用点、公共接口改动或新增依赖；不从“没有 UI”推论协议已封堵。 |

并发必须用至少两个通过既有 Open 打开的独立连接池指向同一个 `t.TempDir()` 文件库，以 channel／屏障协调，无 sleep 猜时序；不改 SetMaxOpenConns／PRAGMA 来制造通过。单 Open 的 goroutine 用例只是补充。所有故障对象／桶／trigger 都在测试临时库，兼容测试使用 `storage.NewLocal(storage.LocalConfig{Root: ...})`，不触实际工作库或外部存储。

SQL 绑定再加新语句的 I1 正反例：同一个 tenant 或 SubjectRef 多次出现仍是新 `$N` 和新实参；SQLite 实际执行及绑定顺序断言，不以当前 invariants 的局部字符串扫描替代。`*_ns` 要测整数纳秒往返，不以时间文本冒充。

## 8. 后续执行、门禁与完成定义

阶段 B 顺序：先核对阶段 A 两文档机械范围／同源、0048 全目录占用、21 个新路径未被占用；保存红灯；实现三表、领域与 SQL；跑定向普通／race；由后续机械阶段对同一最终源码执行完整门禁。超范围、门禁失败、同源失败、剩余时间不足均保留现场并报告，不无变化重跑或缩小失败范围。

阶段 A 的执行计划如下（**不是全部已执行清单**）：定向命令的实际参数、退出码和日志见 §9；完整门禁／Web／diff 仍由返回后的机械阶段执行，不预填结果。

```sh
GOMAXPROCS=4 GOFLAGS=-p=1 go test -count=1 ./internal/spaces/ ./internal/repository/ -run 'TestSpace|TestSubjectRef'
GOMAXPROCS=4 GOFLAGS=-p=1 go test -race -count=1 -timeout 300s ./internal/spaces/ ./internal/repository/ -run 'TestSpace|TestSubjectRef'
GOMAXPROCS=4 GOFLAGS=-p=1 make check
GOMAXPROCS=4 GOFLAGS=-p=1 python3 cli.py accept
GOMAXPROCS=4 GOFLAGS=-p=1 make test-race
make web-check
make build
make web-smoke
git diff --check
```

新增测试统一使用 TestSpace*/TestSubjectRef* 名称以免定向漏跑；`-run` 只作前置定向，不替代完整门禁。完整 test-race 保持 Makefile 的 `./cmd/server/ ./internal/...`、count=1、每包 300 秒，不 short／不 run。Web 三步仅确认继承 embed 未退化，不授权修改前端；产物漂移即阻断报告，不覆盖基线资产。另由 runner 做精确允许路径、旧迁移字节、原用户脏状态、Git 索引与最终同源校验、独立只读验收，所有实际命令需记退出码／日志，不预填 PASS。

本片不运行真实 PostgreSQL/Qdrant、Docker／数据库服务、外部身份／存储／KMS、HTTP2／代理、生产、压力／RSS或登录后浏览器 E2E；SQLite 与 PG 静态对称／错误类型测试不等于 PG 实库通过。基线历史 PASS、备份校验及上游 READY 不提升为本片新结果。

**阶段 A 当时仅完成两文档落盘与人工回读，未编码／测试；阶段 B 的实际结果见下文，仍未部署。** 只有后续当前源码全部机械门禁为 0、同源／范围成立且独立只读验收 PASS，才能称 SPACE-P1A 持久化基础片完成；仍不构成个人／团队空间产品交付，也不授权 P1B/P2。最终阶段报告直接返回正文由 runner 保存，不写 runner 的 scope-report.md 或项目根日志。

## 9. 阶段 B 实际实施结果（2026-10-17）

### 9.1 源码与事务事实

- §6 的 21 个新 Go 文件及 §5 四份新 `0048_spaces_foundation` 已存在；仅两份获准文档同步状态。无既有 Go／测试／旧迁移修改，无依赖、配置、接口预算或工程豁免调整。
- `spaces.SubjectRef/Space/Member/History/CreateCommand/MemberCommand`、三个 keyset Page、`spaces.Store`（9 方法）、9 类领域错误及 `Validate*` 已实现。完整身份大小写敏感，不静默裁剪；身份／角色不是员工资格或应用授权。
- `var _ spaces.Store = (*sqlStore)(nil)` 只在新增 `sql_spaces.go` 中作编译断言；既有 Repository／Storage 未扩宽。`CreateSpace` 严格 INSERT 新 `spc-<UUID>` 桶元数据，然后根、初始 active user admin、create 历史同事务提交；已有空／非空桶均 ErrConflict，绝不认领。
- `spaceCAS` 首语句裁决根 revision；`spaceMemberState/spaceMemberAfter/spaceProtectAdmin` 在同一 `*sql.Tx` 中转换成员并检查最后 active user admin，`spaceAppendHistory` 与根／成员共同提交。失败回滚，不抬 revision、不留虚假历史，无进程锁、事务外计数或隐藏重试。
- `GetSpace/GetSpaceByBucket/GetPersonalSpace/GetSpaceMember/ListSpaceMembers/ListSpacesForSubject/ListSpaceHistory` 全部显式 tenant。`spaceRead` 共用 tenant 状态与结果快照；PG 分支为 repeatable-read + tenant `FOR SHARE`（不是只读事务选项）。SQL 重复 tenant 仍用新占位符／实参；新时间字段为整数纳秒，旧桶文本为 UTC RFC3339Nano。
- `spaceSQLError/classifySpaceSQLite/classifySpacePostgres` 按现有驱动类型／代码分类并保留 cause；已知唯一键／CAS 冲突、成员语义、事务竞争、DB/context 错误不伪装成功。Commit 错误不返回成功；写 Commit 结果不确定仍需查证，不承诺一定回滚或 exactly-once。

### 9.2 实际测试映射

| 矩阵 | 本轮实际证据（普通和 race 均通过） |
|---|---|
| V1 | `TestSpaceValidate*`、`TestSpaceInvalidCommandsLeaveFactsUnchanged`、`TestSpaceRevisionMaxBoundary`：非法 tenant／完整身份／UUID／枚举／revision／分页拒绝，失败事实不变；MaxInt64-1 可增长到 MaxInt64，后续写入拒绝溢出。 |
| I1 / T1 | `TestSubjectRefCompleteIdentity`、`TestSpaceQueries*`、`TestSpaceTenantUnavailableAcrossPort`、`TestSpaceReadKeepsSQLiteTenantSnapshot`：同 UUID／命令／裸 ID 的跨 tenant、Source、Kind 与 ID 分隔、三种分页、缺根 vs 空页；未知／停用租户所有 9 方法拒绝，SQLite 状态读取快照真实验证。 |
| P1 | `TestSpaceConcurrentPersonalAcrossOpen`：两个独立 Open／连接池、channel 屏障，恰一成功，败者仅 Conflict／Concurrent，可从两实例恢复同一持久关联；根／桶／初始成员／历史各一行。直接 SQL 反例和 `PRAGMA index_info` 证明数据库个人唯一键含完整字段。 |
| A1 | `TestSpaceCreateAtomicFacts`、`TestSpaceCreateRollbackEachStep`、`TestSpaceCreateNeverClaimsExistingBucket`：bucket/root/member/history 各处真实 trigger ABORT 全回滚；删除注入后原命令可成功；预存桶配额配置、对象完整元数据及 local Blob 字节不变。 |
| M1 / C1 | `TestSpaceMember*`、`TestSpaceConcurrentAdminsAcrossOpen`、`TestSpaceBusyIsRecoverableAcrossOpen`：成员状态正反例、旧 CAS、撤销不释放个人绑定、JoinedAt 保留与 revoked_ns 往返；独立 Open 互撤／互降级／混合竞争恰一提交，败者最新 revision 重试被 LastAdmin 拒绝；service admin／revoked user 不充数；持有真实写事务时另一连接返回类型化 BUSY。 |
| H1 | `TestSpaceHistory*`、`TestSpaceMemberRollbackEachStep`：完整 actor/target、真实前后态、每次 +1、历史 UUID 与 revision 分页；重复 command／历史失败使根及成员回滚；RAISE(IGNORE) 的 0 行写入也不当成功。 |
| E1 | `TestSpaceContextErrorsRetainCauseAcrossPort`、`TestSpaceClosedDBIsNotEmptySuccess`、`TestSpacePublicQueryScanFailure`、`TestSpaceScanRowsAndAffectedErrors`、`TestSpaceSQLTypedErrorClassification`：全部方法取消／过期、关闭 DB、公共 Scan 故障、真实 rows.Err、RowsAffected 单元错误、读 Commit 失败；保留 cause。PG 仅合成 PgError。 |
| R1 / U1 | `TestSpaceReopenAndMigrateSkip0048`、`TestSpaceMigrationLegacyZeroBackfillAndEmptyDown`、`TestSpaceSQLite*`：Close/Open/Migrate 跳过已应用 0048；真实 0047 临时库种 default／其他桶、无 owner 的版本／对象／local Blob／分享后迁移，新表空、旧值与字节不变；手工空表 down 不删旧事实，旧 CRUD 仍可用；真实 UNIQUE/PK/FK/CHECK 和整数亲和性反例。 |
| I1 / N1 | `TestSpaceSQLBindingOccurrenceOrder`：新 SQL 字面量连续占位符扫描及重复编号／跳号／逆序反例、SQLite 真实绑定、PG rebind 静态正对照。精确清单和导入审查没有装配调用点。 |

共 **39 个顶层新测试**通过（含子测试），不是全仓测试。新包定向覆盖率 **93.2% (69/74 statements)**；新增 SQL 文件按 profile 逐语句加权为 **90.6% (289/319)**。仓储整包的定向覆盖率为 **17.7%**，因为旧测试未运行；不把定向覆盖率冒充全仓覆盖率门禁。

### 9.3 命令、红绿灯与私有证据

日志目录（不在项目根）：
`/home/u1/.abr/projects/aero-vault-5c281c8a1991a53d/runs/personal-team-spaces-24h-solmax-20261016T091859Z/implementations/p1a-001/local-stage-b/`

每个下列 label 都保存同名 `.command/.exit/.log`；包装 `run.sh` 的父进程 umask 077，证据目录 0700，三类文件 0600；测试子进程显式 umask 022。旧失败日志保留，没有覆盖或删掉。

| label | exit | 实际含义 |
|---|---|---|
| `preflight` | 0 | 与阶段 A 守卫源码一致；只有两文档差异；0048 和拟新增 Go 均未占用。 |
| `red-01` | 1 | 新测试先于领域类型：undefined SubjectRef／Validate* 等，属待实现新功能。 |
| `red-02` | 1 | 领域测试已绿，SQL 窄能力尚未实现，CreateSpace／ChangeSpaceMember 缺失。 |
| `green-01` | 0 | 最小领域、DDL 和 SQL 事务实现后首批测试转绿。 |
| `targeted-02` | 1 | PK 反例同时撞个人唯一索引；隔离 PK 反例，未放宽实现约束。 |
| `targeted-03` | 1 | SQLite ON DELETE RESTRICT 实际返回内部 TRIGGER(1811)，不是插入 FK 的 FOREIGNKEY(787)；改为分别精确断言两个代码，均保留原始 cause。 |
| `targeted-04` | 0 | 上述反例修正后扩展测试通过。 |
| `audit-01` / `audit-02` | 1 / 1 | 自用新文件函数长度／估算复杂度审查发现超标；只拆分新文件内部函数，没有加预算／文件。 |
| `audit-03` / `audit-final` | 0 / 0 | 21 新 Go 文件 ≤500 行、函数 ≤50 行、估算圈复杂度 ≤10；这是附加审查，不替代正式门禁。 |
| `targeted-final` / `targeted-race-final` | 0 / 0 | 全部新定向普通／race 通过；随后只加强既有桶字节不变的新增测试。 |
| `targeted-final-02` / `targeted-race-final-02` | 0 / 0 | 最终同一 Go／SQL 源码的普通／race，各 39 顶层测试通过，仓储耗时分别 22.369s／30.829s。 |
| `source-freeze` | 0 | 测试后冻结源码；精确 27 路径差异（21 Go + 4 SQL + 2 文档），原受保护路径内容／类型／模式、旧 Sprint 前缀、HEAD／分支／索引均保持。 |
| `source-verify` | 0 | 文档同步后以 `inspect.py verify-01` 复核同一白名单与全部原文件，Go／测试／SQL 与测试后冻结快照逐项一致；私有日志权限亦保持。此本地复核不代替后续 runner 门禁／同源守卫。 |

实际最终测试命令如下（`.command` 保留完整 argv）：

```sh
env GOMAXPROCS=4 GOFLAGS=-p=1 go test -v -count=1 -timeout 300s -coverprofile=/home/u1/.abr/projects/aero-vault-5c281c8a1991a53d/runs/personal-team-spaces-24h-solmax-20261016T091859Z/implementations/p1a-001/local-stage-b/targeted-final-02.cover ./internal/spaces/ ./internal/repository/ -run 'TestSpace|TestSubjectRef'
env GOMAXPROCS=4 GOFLAGS=-p=1 go test -race -v -count=1 -timeout 300s ./internal/spaces/ ./internal/repository/ -run 'TestSpace|TestSubjectRef'
```

最初红灯用相同包／run 选择、`-count=1 -timeout 300s`，无 `-v/-coverprofile/-race`；普通中间验证加 `-v`。自用 audit 运行 `env GOMAXPROCS=4 GOFLAGS=-p=1 go run <上述证据目录>/audit.go`。这些本地定向选择没有缩小后续完整门禁要求。

### 9.4 未验证、未接线与待办

- **待机械阶段**：完整 `make check`、`python3 cli.py accept`、整仓 `make test-race`、`make web-check` → `make build` → `make web-smoke`、`git diff --check`、最终 runner 同源／范围守卫及独立只读验收；本轮均未执行，不预填 PASS。
- **未运行真实 PG**：只静态比较双 DDL 的结构对称与 BIGINT/INTEGER 差异、合成 PgError 分类、PG rebind。PG 事务锁／迁移／并发、真实写 Commit 不确定故障、跨 OS 进程或多机竞争未实跑；SQLite 两独立持久连接池的竞争已运行，不用进程锁替代数据库保护。
- 没有 main/router/FileService/Manager/对象写入链、协议／UI／API／CLI／SDK 接线；角色不授予 delete/share/ACL 等能力，不创建对外可用空间。只有迁移空表随既有 embed/Migrate 生效，没有启动创建／认领用例。
- 未触实际工作库／真实旧桶／Blob，无旧数据回填、owner 补写、分享重发；SQLite + local 都在 `t.TempDir()`。外部身份、PG/Qdrant、存储/KMS、生产、网络协议端到端、压力/RSS/登录后浏览器 E2E 未验证。
- 员工资格、成员管理应用授权、接管／移交、分享策略、全入口封堵及完整空间产品仍是未批准待办；本片没有新增这些产品策略。
- 阶段内偏差仅为新增测试中两处 SQLite 约束反例校正及新函数内部拆分；没有越白名单、修改旧测试、调连接／PRAGMA／重试、放宽阈值或接口来过测试。基础片尚待完整机械门禁和独立验收，不能称已最终验收完成。

### 9.5 新24小时窗口续验（2026-10-17，opencode-go/deepseek-v4.1-flash、thinking=max）

§9.1–§9.4 为旧窗口历史事实，保留不变；本窗口未改任何 Go／测试／SQL／迁移，只做同源验证恢复并追加本节。

- 完整 repository race 诊断：exit 0，247.63s；`TestProbeQueryPlans` 32.66s 位于全部 P1A internal 测试之后（P1A 计 40.33s），余量约 52s，属脆弱余量；只证明本次同预算复测通过，不证明或推翻旧失败根因。
- 受控本地 repository+spaces 完整 race `local`：exit 0，177.43s+1.006s，39 顶层测试全过、无 race 报告、`source_unchanged=true`、同 `code_sha256` `9d0af721…`；两次 probe 耗时 32.66s vs 6.64s，宿主 I/O 波动大于 P1A 固定占用，环境仍是主要嫌疑但未确诊。
- 夹具审查：10 个 P1A 测试文件中每次 `spaceTestDB` 都有真实用途（失败注入、隔离、重开、真实 0047→0048 迁移），不存在可安全删除的冗余空 schema 准备，因此不改夹具、不新增回归；后续机械门禁仍须独立重跑。
- 本窗口新增证据在 `runs/p1a-24h-dsflash-max-20261017T021405Z/diagnostic/`、`local-full-race/attempt-001/` 与本窗口私有 `local-verification/`；不写 runner 报告文件。

### 9.6 通知测试夹具单文件例外修复（2026-10-17 · opencode-go/deepseek-v4.1-flash、thinking=max）

§9.1–§9.5 为旧窗口事实，原样保留；本节是用户在上一条精确确认后授权的新 24 小时窗口，只处理 `internal/events/notifier_test.go` 准备／投递计时边界及两份文档末尾追加，不重做 21 个 P1A Go／四份 0048，不扩 API/UI/P1B/P2。

**动码前基线：** 目标测试与窗口 `baseline/notifier_test.go` 逐字节一致（sha256 `1c9c0386…`）。旧红灯原文：`--- FAIL: TestNotifierDelivery (5.64s)`、`notifier_test.go:149: migrate: begin 0020_bucket_policy: context deadline exceeded`、`FAIL github.com/aero-vault/aero-vault/internal/events 149.789s`、整仓 `make test-race` exit 2。旧失败发生在投递开始之前，不能推断生产 Notifier 投递、死锁或 DATA RACE 缺陷。

**精确设计（编码前合同）：**

1. 准备阶段（`t.TempDir()` 真实磁盘 SQLite、`repository.Open`、真实完整 `repo.Migrate`、`SetBucketNotifications`）使用由 `testing.T` context 与包总 deadline 派生的 context；失败照旧 `t.Fatalf`，不缩减迁移、不换内存库／TMPDIR、不改连接／PRAGMA／sync，不用 `context.Background()` 掩盖挂死。
2. 上述准备成功后，才创建原样 `context.WithTimeout(context.Background(), 5*time.Second)`；`NewNotifier`／`Run`、规则读取、HTTP POST、原事件（ID=1、default/default/test-file.txt、EventCreated）与接收／超时断言全部留在该 5 秒内，保留 `cancel`、repo／httptest 关闭。
3. 整包预算仍 300 秒，不增加任何阈值或 skip/short/run 过滤；不改生产代码、其他旧测试与测量输入。
4. 新增函数只允许 `notifierTest*`／`TestNotifier*` 前缀；原文件其他既有函数逐字节不变。

**有辨别力测试矩阵（编码前登记）：**

| 编号 | 场景与必要断言 |
|---|---|
| N-1 | `TestNotifierDelivery`：真实磁盘库＋完整迁移＋httptest 接收原事件；证明正常路径不回归。 |
| N-2 | `TestNotifierDeliverySlowSetup`（新增）：test-only 受控准备延迟 > 5 秒，准备实际耗时 > 5 秒；随后仍收到通知，证明准备未提前消耗投递窗口。 |
| N-3 | 反例：临时把同一 5 秒计时器移回准备之前，N-2 必须真实 FAIL（`timeout waiting for notification delivery`）；恢复精确字节后 N-2 必须 PASS；保留注入前后 sha256、命令／exit／日志。 |

**门禁状态：** 本节登记时完整门禁与独立验收均**待执行**，不预填 PASS；本地定向与受控完整 race 的实际命令、exit、日志随后追加。

**实施结果（2026-10-17 11:20–11:45 UTC）：**

| label | exit | 实际含义 |
|---|---|---|
| `01-vet` | 0 | `go vet ./internal/events/`。 |
| `02-notifier-normal` | 0 | `TestNotifier` 普通：原投递 3.26s、新回归 10.54s 均 PASS。 |
| `03-notifier-race` | 0 | `TestNotifier` race：原投递 13.22s、新回归 10.79s 均 PASS，无 DATA RACE。 |
| `04-counterexample-injected` | **1（预期红）** | 临时把同一 5 秒计时器移回准备之前（注入 sha `c47d6570…`）：`notifier_test.go:248: timeout waiting for notification delivery`，证明回归有辨别力。 |
| `05-counterexample-restored` | 0 | finally 恢复 `da46ce38…` 后同回归 PASS；`cmp` 逐字节一致。 |
| `06-p1a39-normal` / `07-p1a39-race` | 0 / 0 | 原 39 项 P1A 普通与 race 均 39 PASS、0 FAIL。 |
| `08-events-full-race` | 1 | events 全包 race 在既有 `TestOutboxRelay_DeliversDeletedFactToL2` 两个子测试失败（非 notifier）。 |
| `09` / `10` | 1 / 1 | 同一 relay 子测试孤立 race `-count=3` 3/3 红、普通 1/1 红；两文件与窗口基线逐字节一致。 |
| `11-local-race`（attempt-001，唯一一次，exit 4） | 4 | `code_sha256=410cb401…`、`source_unchanged=true`；notifier 两测试与 spaces 通过；repository 包 300.594s 超时（未完成 `TestListExpiredNonCurrentVersionsPaginatesPastFreshCandidates`）、events 包 272.521s 因既有 relay 子测试失败。 |
| `12-require-local` | 4 | `same-code complete repository/spaces/events race is required`；唯一一次尝试失败后控制器拒绝同码重跑。 |

5 次 relay 负载探针 4 红 1 绿（绿时 IO full avg10=8.77，红时 24–52），指向主机 I/O 敏感的既有 frozen 用例／脆弱 300s 预算，而非本测试修复；不能据此断言旧 full-race 根因已确诊。新测试使用真实磁盘 SQLite＋完整 Migrate，准备受 testing.T context＋包 deadline 约束，投递仍原样 5 秒。**未由本窗口执行：** `make check`、`cli.py accept`、整仓 `make test-race`、Web 三步、`git diff --check`、独立只读验收；返回后 runner 机械阶段将在 `require-local` 阻断，实际结果以那个阶段的现场为准。未修改 local-race.py、冻结测试、包预算或控制器产物；旧失败与旧报告原文保留。

### 9.7 SPACE-P1A-TESTBOUNDARY-5：本次五文件实施合同与验收（2026-10-18）

本节在修改 Go 前追加。用户已对精确五个旧测试文件、原预算／租约／输入／断言不变的问题回复“继续”；这就是本轮授权，不再以旧 Proposed 历史要求同一批准。旧文档整个前缀保留。原截止 `2026-10-18T10:55:52.663808+00:00` 不延长，单写者串行，第一写者固定 `openai-codex/gpt-6.1-sol`、thinking=max。

**第一写者的准确文件与设计：**

| 文件 | 仅此改动／保持项 |
|---|---|
| `internal/repository/audit_governance_pending_idx_test.go` | Open 成功后、完整 Migrate 前立刻登记 Close；关闭观察先登记、Close 后登记，LIFO 保证观察发生于真实清理之后。以未取消 context 下的数据库 Ping／连接状态判断，不用 goroutine 数猜测。复用原索引计划和 failed-one-attempt 两个真实 DB 生命周期做关闭回归，不另造第三个无意义全迁移夹具。记录 Open、完整 Migrate、原种数、ANALYZE、查询／rows 消费和关闭收尾。 |
| `internal/repository/lifecycle_test.go` | 仅将既有 Close 注册前移到 Open 成功后、Migrate 前，保留原迁移失败 Fatal；追加 Open／完整 Migrate／Close 实际计时。 |
| `internal/repository/event_outbox_test.go` | 原有 Migrate 前 Close 不变，不称同样泄漏；仅追加准备、种数、业务、查询／rows 消费与 Close 阶段计时。原对象／审计／事实同事务、回滚、payload、owner/token、终态、fencing 与所有旧断言不变。 |
| `internal/repository/lifecycle_pagination_test.go` | 仅原三个分页用例的准备、策略／种数、查询和断言阶段计时；fresh→old、limit=1、72h、GLACIER、精确 old Key／ID 与真实删除标记路径不变。 |

两份获批文档仅末尾追加。第二串行写者才可改 `internal/events/event_outbox_relay_test.go`：仅原三个获批 L2 子测试的 500 复位、请求 #2 无 echo 后请求 #3 恢复、被动阶段计时与同一精确 fact 的只读状态／完成记录证明；先 SELECT 再保留原 observer Claim（Claim 是写操作）。原固定 deliverBatch／Claim 次数与全部旧反例保持，补 always-500、never-echo、恒 echo 辨别力检查，不用空 Claim 冒充 delivered。本第一写者不改 events 或 notifier，返回后四个 repository 文件冻结。

**不可改变的实验条件：** `pendingProbeNow` 与整个 `pendingIdxSeed` 字面量、20 bindings、50,000 delivered／5,000 failed／300 pending、ties／draining 分布、ANALYZE、索引／不全扫与 exactly-one-attempt 断言全部保持。所有库独立磁盘 `t.TempDir()`、真实 Open／全部 Migrate；不改 PRAGMA／sync／schema 缓存／输入／事务／扫描 SQL。50ms 租约、100ms 首 POST 超窗、5s HTTP／投递、1300／2600ms 等待、原 MaxAttempts 和 300s 包预算均冻结；禁止循环重投、额外批次、直接 Complete／Retry 或手改状态替代 relay 求绿。生产、21 个 P1A Go、全部迁移（含 0048）、公共接口、依赖／配置／门禁与其他项目文件零修改，不新增项目文件，不提交／推送／部署或改 Git 分支／索引。

计时在阶段真实结束时取 time.Since，defer 必须闭包到结束时取值；新增 helper 小而领域相关，不造框架，新增测试统一 `TestFixtureBoundary*`。关闭回归可直接附着既有两例生命周期，不为测试名称新增空用途全迁移。

**编码前验收矩阵（本节登记时全部待执行）：**

| 编号 | 判据及证据 |
|---|---|
| TB-R1 | 原两个 pending-index DB 用例完成清理后，原连接确已关闭；临时仅在获批文件去掉 Close 时同一观察实际红。任何异常 finally 恢复精确字节，再跑同例必须绿；保存注入 diff、前／注入／恢复 SHA256、command／exit／log。 |
| TB-R2 | 普通与 race 覆盖四文件所有受影响原测试和新回归；Open／完整 Migrate／种数与 ANALYZE／查询和 rows 消费／收尾 Close 均有真实阶段日志。Go 固定 `GOMAXPROCS=4 GOFLAGS=-p=1`，子进程 umask 022、count=1、每包 300s，证据写本 RUN `local-repository/`。 |
| TB-S | 只读运行本 RUN `test-contracts.go` 与 `scope.py verify`，保留原测试／子测试、冻结种子、预算与范围；不改控制器，不自行创建 repository-source 或 accepted 记录。 |
| TB-E | 第二写者保留原固定批次与租约／等待，真实 relay + delivered 状态及 event_outbox_delivered 记录证明交付；500／echo 反例应红，finally 恢复应绿。50ms 若确被持久化耗尽则如实 blocked，不放宽条件。 |
| TB-G | 返回后控制器独立静态／repository vet／索引与清理定向 race／source checkpoint；第二写者后完整 repository／spaces／events local race，再对同源执行 make check、cli.py accept、原全范围 make test-race、Web 三步、diff／范围／同源守卫及独立验收。定向补充不等于整包／全仓成功，不能预填 PASS。 |

修 Close 只解决已确认清理缺陷，不等于确诊或消除累计 300s：旧包前 17 顶层累计约 297.15s，末例仅约 3s，活动栈在 Migrate 建 schema_migrations，并非扫描查询跑 300s；外层阶段计时也不能拆出每个 fsync／Commit。旧 40 方向恢复汇总与新审查是分析，不是本轮运行通过。本基础片仍未接线、未独立验收，不授权 P1B／P2 或 Space 产品交付。真实 PG／Qdrant、外部身份／存储／KMS、HTTP2／代理、生产、压力／RSS及登录后业务 E2E 仍未验证，费用 unknown。

#### 9.7.1 第一写者实际证据（2026-10-18，Codex／max）

只修改 §9.7 的四个 repository 测试及两文档追加；events／notifier、生产、全部迁移、原 P1A 实现／接口／门禁保持冻结。`documents-before-go.json` 证明合同先于 Go，旧前缀完整。关闭回归直接放在原两个 pending-index 用例的 LIFO 清理之后：Close→未取消 context 的 Ping 与连接数观察→TempDir 删除，没有新增顶层测试或空用途全迁移夹具。

证据根目录 **E**：
`/home/u1/.abr/projects/aero-vault-5c281c8a1991a53d/runs/p1a-dispatch40-sol-flash-20261017T155929Z/implementations/five-tests-20261018T025539Z/local-repository/`。
下表每个 label 的真实 `.command/.exit/.log` 均在 E，完整定向选择和四 Go SHA 在 `affected-tests.json`；没有自行写控制器 checkpoint／验收记录。

| label | exit | 结果与限度 |
|---|---|---|
| `05a-close-removed` | **1（预期红）** | 临时仅移除获批 pending-index 文件的已登记 Close；原索引计划和 failed-one-attempt 两例均因清理后 `ping=<nil> open_connections=1` 而 FAIL。原业务检查仍运行，没有靠编译错误变红。失败观察后才兜底释放连接。 |
| `05b-close-restored` | **0** | finally 恢复精确字节，同命令两例均 PASS；日志顺序 Close 在前，观察 `sql: database is closed`＋0 连接在后。 |
| `06-affected-normal` | **0** | 19 个原顶层例全 PASS，包 5.535s；四文件全部 18 例及共享 lifecycle 夹具的既有 malformed-JSON 例，包含两例上的新清理断言。 |
| `07-affected-race` | **0** | 同一选择 19 PASS，包 28.810s，无 DATA RACE；是受影响原例定向，不是完整 repository 包。 |
| `08-vet` | **0** | `go vet ./internal/repository/`，不抵扣返回后控制器独立 vet。 |
| `03-test-contracts`／`04-scope-after-go`／`09-local-preservation-and-phases` | **0／0／0** | 保留静态合同、恰六路径差异和本地补充字节／阶段检查；不替代独立审查或完整 runtime。 |

红／绿真实命令相同：
`go test -v -count=1 -timeout 300s ./internal/repository/ -run '^(TestAuditGovernancePendingIndexesServeClaimAndLagPlans|TestAuditGovernanceFailedFactReadsBackOneAttempt)$'`。
普通／race 为 `go test [-race] -v -count=1 -timeout 300s ./internal/repository/ -run <E/affected-tests.json 中 run>`；所有 Go 固定 `GOMAXPROCS=4 GOFLAGS=-p=1`、测试子进程 umask 022。没有新增顶层测试（`TestFixtureBoundary*` 预留选择不增加夹具），新关闭回归由原两例直接覆盖。

**恢复证明：** 注入前与 finally 恢复后 SHA256 均为 `a9bde17b28c543d74b0206cb0d2f7b29fa5e34e540f0cfa522b093d61fa288f3`，逐字节等于 `close-before.go`；注入为 `d5a525a7e8943b1d8f1b5db9a92ef06a6ce3136ae7c5e0df9baf33d32e985f06`。`close-injection.diff` 只移除主 Close 调用，`close-counterexample-hashes.json` 保留红 1／恢复绿 0 与 exact-bytes=true。四 Go 在两次定向运行及事后检查哈希一致。

**真实阶段样本（普通／race；详见 phase-summary.json）：**

| 原阶段 | 普通 | race |
|---|---|---|
| 索引例 Open／全部 Migrate | 11.09ms／165.73ms | 26.38ms／580.59ms |
| 原 55,300 行种数／ANALYZE | 321.13ms／32.30ms | 9.094s／1.990s |
| 三次计划查询＋rows＋断言合计 | 0.250ms | 2.213ms |
| 索引例真实 Close | 7.44ms | 10.43ms |
| 原 Outbox 20 夹具 Migrate 合计 | 3.294s | 11.567s |
| 原 Outbox 业务／Close 合计 | 0.201s／0.117s | 0.426s／0.154s |
| 三个分页例各自准备范围 | 0.173–0.182s | 0.588–0.594s |
| 三个分页例各自策略／种数范围 | 14.19–20.20ms | 19.86–29.31ms |
| 三个分页例各自查询／rows 范围 | 0.171–0.432ms | 4.540–4.564ms |
| 三个分页例各自断言／Close 范围 | 0.14–0.21µs／5.63–5.77ms | 0.22–0.45µs／6.93–8.09ms |

计时在真实阶段结束时取值；业务／准备包含部分子阶段，不能重复相加。冻结的时钟与整个 seed 前缀逐字节相同；outbox 原测试／payload 构造及 lifecycle 原业务例逐字节相同，分页除计时外逐字节相同；原索引与扫描、事务、分页断言全部保留。

**仍未知／待验：** 此轮没有动态注入 Migrate 失败；前移注册由源码与静态范围复核确认，关闭辨别力红绿针对 pending-index 主 Close 遗漏。关闭修复不是旧 297s 累计／300s 超时根因证明，阶段样本不能还原旧宿主 I/O 或每次 fsync。返回后仍需控制器独立静态／vet／重点 race／source checkpoint、第二写者 events、完整 local、全部宏门禁与独立验收；不预填 PASS，不声称所有验证完成、P1A 已验收或产品已交付。未提交／推送／部署，截止不延长，外部／生产验证与费用仍未知。

### 9.8 SPACE-P1A-TESTBOUNDARY-5 第二串行写者实际结果（2026-10-18 · opencode-go/deepseek-v4.1-flash、thinking=max）

§9.1–§9.7.1 及全部旧前缀原样保留；本段只追加第二写者对 `internal/events/event_outbox_relay_test.go` 的 test-only 实施结果和证据，不修改四个已封存 repository 测试、notifier、生产、全部迁移、接口、依赖、配置或门禁。两份文档仅末尾追加。

**实施必要性：** 原三段 L2 case 分别存在 500 首次设置后不复位、receiver wrapper 在 `count>=1` 时再次覆盖阶段 3 的 `echo=true`、以及只凭空 Claim 收尾的问题。本轮只修复这些确定夹具/判据问题，并保留原固定批次、租约、等待、attempt、payload/factID 与 fencing 反例：500=2 次 `deliverBatch`/2 次 observer Claim，lease-expiry=2/1，receiver-contract=3/2。

| 原 case | 实际状态/计时（最终通过样本） |
|---|---|
| 500 首发 500 | `pending attempts=1 delivered_at_ns=0 delivered_rows=0`；首轮 batch 约 10.66ms |
| 500 重试恢复 2xx+echo | `delivered attempts=2 delivered_at_ns>0 delivered_rows=1`；重试 batch 约 11.78ms |
| lease-expiry 首 POST | `inflight attempts=1 delivered_rows=0`；首轮 batch 约 107.17ms（100ms delay 跨 50ms 租约） |
| lease-expiry re-claim | `delivered attempts=2 delivered_rows=1`；固定第二批约 10.69ms |
| receiver #2 无 echo | `pending attempts=2 delivered_rows=0`；backoff 约 1.92s |
| receiver #3 恢复 echo | `delivered attempts=3 delivered_rows=1`；固定第三批约 11.01ms |

- 只读观测：新增 `readRelayTestFactState`，用明确临时 DSN + 既有 `openRelayTestRepoAt` 直读同一 `origin_id` 的 `event_outbox`（status/attempts/available_at_ns/delivered_at_ns）及 `event_outbox_delivered` 行数，并记录读取耗时；先读状态再执行原 observer Claim。空 Claim 不再等于 delivered。
- 新回归：`TestFixtureBoundaryOutboxRelayL2DeliveryProof` 三个真实子测试——always-500 与 never-echo 均为 `pending`、完成记录 0；恒 echo 为 `delivered`、完成记录 1，证明成功判据需要持久状态与完成记录同时成立。
- 故障注入与恢复：旧 receiver wrapper（`count>=1`）使阶段 3 仍无 echo，执行 exit 1，状态 `failed attempts=3`；旧 500 不复位使重试仍 500，exit 1，状态 `pending attempts=2`；把 pending/空 Claim 误当 delivered 的注入 exit 1。三次注入后均从最终源码副本恢复，最终 SHA256 `c00600d47fd12a4cd4e974f962f5cb3a1a323b9ccad4383a15021b47a072c197`，`cmp` exit 0。注入 diff、前后 SHA 与红日志在 `local-events/11-*`、`12-*`、`13-*`。
- 定向命令/exit/log：原 L2 普通 `01-l2-original-normal` exit 0（7.03s）；新回归普通 `02-fixture-boundary-normal` exit 0（0.59s）；原 L2+新回归 race `03-targeted-race` exit 0（13.16s，无 DATA RACE）；events 整包普通 `04-events-full-normal` exit 0（16.91s）。均为 `GOMAXPROCS=4 GOFLAGS=-p=1`、umask 022、count=1、timeout=300s，日志在 `local-events/`。
- 完整 local race：`20-local-race` exit 0（唯一 attempt-001），实际 argv `go test -race -count=1 -timeout 300s -json ./internal/repository/ ./internal/spaces/ ./internal/events/`，无 run/short；repository 137.66s、spaces 1.007s、events 26.886s，wall 167.27s；`tests_passed=true`、`controller_timed_out=false`、`source_unchanged=true`、`failed_tests=[]`、`unfinished_tests=[]`，`code_sha256=6b3befb5792faab773caba274dbed85dfbf9769e6e7d252e25fbf763f4e903ca`。结果在 `local-full-race/attempt-001/result.json`，由控制器复核；本角色不写 accepted/latest 之外的控制器裁决。
- 静态/范围：本 RUN `test-contracts.go` exit 0（原测试/子测试、固定批次数、租约/等待/attempt、无新 skip/short 均保留），`scope.py verify` exit 0（7 路径），gofmt 与 `git diff --check` exit 0。
- **剩余边界：** 50ms 夹具本次未 blocked 不等于生产默认 30s 租约无缺陷；旧 repository 累计约 297.15s／300s 超时根因仍未被本段确诊或消除；外层计时不能拆分每份迁移 Commit／fsync。真实 PG/Qdrant、外部身份/存储/KMS、HTTP2/代理、生产、压力/RSS 与登录后 E2E 仍未验证，费用 unknown。
- **后续待执行：** `make check`、`python3 cli.py accept`、完整 `make test-race`、`make web-check` → `make build` → `make web-smoke`、最终 `git diff --check`/同源封存及独立只读验收均须由控制器在返回后执行，本段不预填 PASS。未提交、推送或部署，原截止不延长。

### 9.9 SPACE-P1A-LABEL-001：观测标签命名续验（2026-10-18）

- 五测试实现后完整三包 local race 与 make check 已真实 exit 0，后续 cli.py accept 因 I6 exit 1；旧失败/成功与未执行的下游保持历史，不晋升为当前源码验收。
- 唯一必要变更：lifecycle_pagination_test.go 三处计时字符串 `"assertions"` → `"result-checks"`；既有 I6 粗扫描误判标签，实际 imports 仍为 context/testing/time。除三字符串外 Go 字节、全部原断言/测量/输入、迁移、生产、依赖和检查器冻结，不改变或隐藏被测对象。
- Codex 两次网络调用失败、仅 read 未写后，调度 Agent 持 runner 原生正常独占锁做确定性替换；两旧文档仅末尾追加。未换模型、未重做五测试/40方向，旧备份与失败保留，截止仍为 2026-10-18T10:55:52.663808+00:00。
- 未改 invariants 已实际预期红 exit 1；修改后的同命令绿、原三分页普通/race、当前源码完整 local/原七宏门禁与两固定模型只读复核均待实测。任何缺失/红灯不得接受；仍非 Space 产品，不授权 API/UI/P1B/P2/提交部署，外部/生产验证及费用未知。
