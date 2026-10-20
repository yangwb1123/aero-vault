# REST 测试空 schema 复用 v1

## 必要性与最小范围

这是008完整 race 验证被阻断后的测试私有初始化优化，不是生产缺陷修复或产品能力。原完整 `make test-race` exit 2：REST 累计300.030秒超时，当时编码单测仅运行1秒；另一个 config 权限fixture失败已由运行侧077/022对照定位，不能修改 config 源码、测试或断言。原失败与008验收 FAIL 均保留。

恢复目录 `resumes/003/diagnostics/` 的代表性 race profile：`Migrate/applyMigration` 累计4.50秒，占8.14秒CPU的55.28%。本次修改前在 `resumes/003/local/` 重新采集同一个完整编码测试：8.23秒CPU中 `Migrate` 4.06秒（49.33%），`applyMigration` 4.03秒，`setupTestWith` 4.09秒；包耗时13.992秒，exit 0。新增原路径基准 `Fresh`（真实文件 Open/Migrate/Close，race、10次、重复3轮）为688.791/706.471/1159.885毫秒每次，exit 0。它们只是代表性证据，不外推整仓比例或设置性能硬阈值。

更小方案比较：不改初始化会保留已测重复DDL开销；仅整理调用/连接不消除该开销；调高300秒预算、缩减场景、关闭持久化、跳过真实Migrate均不允许。选用只影响 `setupTestWith` 的空schema字节复用，移除该处接线即可回退原逐库迁移；其他直接建库的旧fixture、Repository迁移/故障测试完全不动。

仅允许修改 `internal/api/rest/handlers_test.go` 的 `setupTestWith` 函数、新增 `internal/api/rest/rest_fixture_schema_*_test.go` 及本文件。原imports、注释、其他函数和既有断言逐字保留；生产代码、接口、SQL迁移、config、依赖、SDK、AI、门禁、控制文件与预算均不变。无新环境变量、包、TestMain/init或测试跳过。

## 初始化与错误契约

- 只有首次实际fixture调用时才在该用例 `t.TempDir` 内创建临时样本，执行当前真实 `repository.Open` 和完整 `Migrate`，全部工作仍在实际测试及原300秒包预算内。
- `Migrate` 成败均关闭样本连接，处理关闭错误；确认 WAL/SHM 不存在才读取主文件。任何遗留sidecar、读取、清理错误均失败，不复制不完整主库。迁移、打开、关闭失败不发布字节，不降级或跳过失败。
- 进程内 `sync.Once` 仅发布不可变空schema字节或初始化错误，不共享文件、业务数据、可写DB、连接或事务；不读外部/预生成缓存。首个用例目录删除后仍可创建新副本。
- 每次以独占创建的独立文件写入、Sync、关闭后，才执行原来的真实 `Open` 和 `Migrate` 版本检查。复制失败返回错误并清理本次创建的部分文件；不覆盖已有路径。真实SQLite WAL/foreign_keys及synchronous配置不改变，不使用内存库或关闭fsync。
- 每个用例保留独立 `t.TempDir`、连接、Local FS、FileService、handler与HTTP server。服务端清理先于数据库关闭，初始化后续失败也关闭该用例连接。
- 样本不接受业务写入；用例写入、DDL、事务、关闭与重开不能影响其他副本或后续副本。初始化错误在该缓存实例内保持失败，不用静默重试掩盖故障。

## 有界验收

新增回归应证明：冷构建后关闭与sidecar处理；副本与从零真实迁移的完整schema、版本集合及初始数据一致；真实Migrate幂等及故障仍生效；目录/文件/连接/事务独立；同Key不同内容的真实Local FS/FileService读写与重开隔离；样本字节不变；首次并发调用仅发布同一完整schema，各副本可独立写入且关闭互不影响；真实初始化/复制错误失败而非fallback。新文件≤500行、函数≤50行、复杂度≤10。

所有Go命令固定 `GOMAXPROCS=4 GOFLAGS=-p=1`，测试/构建子进程umask022，日志由私有0700目录及外层077保护。实际命令、exit与完整日志写恢复目录的 `local/*.command/.exit/.log` 和 `commands.jsonl`；profile和测试binary也仅写该私有目录。保留原测试全部场景、尺寸、次数及300秒预算，不加-short。

## 验证状态与边界

证据根目录：`/home/u1/.abr/projects/aero-vault-5c281c8a1991a53d/runs/implementation-24h-sol-max-20261015T070122Z/resumes/003/local/`。每个下列日志均有同名 `.command/.exit`，并记入 `commands.jsonl`；没有覆盖旧失败或旧采样。Go源码最终快照见 `tested-source.json`，Go源码集合摘要为 `d8be67b2b6c48bfafc01dddd0e2d0c41dc5d80c7cf352ee35511f6606663c4cb`。

### 接线红绿灯与回归

- `red-setup-wiring.log` exit 1：添加新私有初始化及回归、但尚未改变 `setupTestWith` 时，`go test -count=1 -timeout 300s -run '^TestRESTFixtureSchemaSetupKeepsHTTPAndLocalFSIndependent$' -v ./internal/api/rest/` 明确失败于“未懒初始化空schema”。这是优化接线红灯，不是新生产缺陷或性能阈值。
- 接入后 `green-fixture-ordinary.log` exit 0；初版复杂度检查 exit 1 的日志保留，已拆分测试断言/清理函数，没有删断言或放宽阈值。最终 `final-ast.log`、`final-cyclo.log` exit 0：新文件最大196行、函数/闭包最大38行、复杂度≤10。
- 最终完整 REST 普通测试：`go test -count=1 -timeout 300s -v ./internal/api/rest/`，`final-source-rest-ordinary.log` exit 0，454个顶层执行、0 skip，245.553秒。此前同范围 `final-rest-ordinary.log` 也 exit 0，135.649秒；整包墙钟波动明显，不能承诺300秒race必然通过。
- 最终新回归乱序重复race：`go test -race -count=3 -timeout 300s -shuffle=on -run '^TestRESTFixtureSchema' -v ./internal/api/rest/`，`final-source-fixture-race.log` exit 0，36个顶层执行，22.681秒，0 skip，无DATA RACE报告。
- 最终008/编码/初始化定向race：`go test -race -count=1 -timeout 300s -run '^TestRESTFixtureSchema|^TestThumbnailCompletedFollower|^TestThumbnailOpenEncoding' -v ./internal/api/rest/`，`final-source-related-race.log` exit 0，29个顶层执行，26.554秒，0 skip，无DATA RACE报告。这不是整仓race。
- 覆盖：冷样本目录完整清理；全量sqlite_schema、当前迁移版本集合与所有初始表数据对照从零真实迁移；integrity_check/foreign_key_check及journal/synchronous对照；Migrate幂等且保留applied_at；移除0047的表/版本后真实重迁移；真实迁移冲突不记录成功版本且关闭连接；两个同时存活的独立写事务分别回滚/提交并重开验证；同Key不同Local FS内容、目录删除后新副本及样本哈希不变；八路同时首次初始化的同一完整镜像哈希、独立文件/目录/连接和各自唯一桶写入；真实setup的HTTP隔离与关闭互不影响。
- 错误回归覆盖取消、初始路径不存在且错误不被静默重试、真实迁移失败、样本Close错误和读取错误、关闭后模拟残留空/非空WAL与SHM时整个capture拒绝发布，以及复制到已有文件/目录/缺失父目录时拒绝覆盖。真实磁盘满、内核Sync/Close失败和RemoveAll失败没有做系统级故障注入；实现检查并返回这些错误，不能将此称为已验证全部OS故障。

### 同一代表性测试与基准实测

代表性测试保持全部8种fixture组合及32次GET/HEAD请求，名称为 `TestThumbnailOpenEncodingRejectsChangedSourceBeforeRead`，未改尺寸、场景、次数或断言。

| 指标 | 修改前 | 修改后首次采样 | 最终Go源码采样 |
|---|---:|---:|---:|
| race profile包耗时 | 13.992s | 9.369s | 3.664s |
| CPU采样总量 | 8.23s | 3.77s | 2.22s |
| Migrate累计CPU | 4.06s / 49.33% | 0.49s / 13.00% | 0.36s / 16.22% |
| setupTestWith累计CPU | 4.09s | 0.61s | 0.42s |
| 普通测试包耗时 | 13.052s | 3.584s | 0.912s |

上述命令均 exit 0，日志分别是 `before-representative-profile.log`、`after-representative-profile.log`、`final-source-representative-profile.log` 及对应普通测试日志。profile使用同一 `-race -count=1 -timeout 300s -run '^TestThumbnailOpenEncodingRejectsChangedSourceBeforeRead$'`，显式私有 `-cpuprofile/-outputdir/-o`；CPU明细见 `before-profile-top.log`、`after-profile-top.log`、`final-source-profile-top.log`。冷构建仍在采样内，没有预热全局缓存。

最终基准命令：`go test -race -count=3 -timeout 300s -run '^$' -bench '^BenchmarkRESTFixtureSchema(Fresh|Cold|Amortized)$' -benchtime=10x -benchmem ./internal/api/rest/`，`final-source-fixture-benchmark.log` exit 0。三轮毫秒/次：Fresh 638.790/637.606/656.609；Cold 672.160/680.663/707.176；Amortized 84.107/82.923/84.164。Fresh保留原真实Open/Migrate/Close路径；Cold计入每次从零构建、关闭、复制和副本Migrate；Amortized在10个副本内计入首次冷构建，没有StopTimer/ResetTimer或计时外预热。前后其他轮次（包括较慢轮次）全部保留；这些是数据库初始化局部基准，不是整仓耗时或性能保证。

### 范围与待执行门禁

`final-source-vet.log`、`final-gofmt-check.log`（空输出）、`final-filesize.log`、`final-invariants.log`、`final-diff-check.log` 均 exit 0。启动2797条Git路径到2804条，仅批准的8路径变化；`handlers_test.go` 的imports、注释、其他函数与既有断言字节不变，其他既有源码字节/模式及HEAD/branch/索引内容保留。未修改生产源码、config、迁移、依赖、资产或控制器；原截止、预算及门禁不变。

附加运行态核对曾在 `tested-scope.log` exit 1 观测根 `RUNNING.md` 的瞬时摘要变化，未写改/恢复它；稍后只读复核已与启动摘要相同。原失败日志保留。源码范围检查及launch/预算/控制文件核对见 `tested-source-scope.log` exit 0，不把运行态文档变化伪装成源码改动或删去该次失败。

整仓 `make test-race` 及原六步机械门禁（`make check`、`python3 cli.py accept`、`make web-check` → `make build` → `make web-smoke`、`git diff --check`）在返回后由控制器真实执行；本轮没有运行这些完整流水线或Web构建，不预填PASS，旧008 FAIL不能被定向诊断替代。任何机械FAIL仍阻断继续。

样本是进程内测试初始化素材，不是产品DB+Blob备份、在线数据库复制或原子快照能力。不证明整仓race通过、跨平台/压力/RSS、磁盘故障、PostgreSQL/Qdrant、生产、外部服务或跨进程缓存能力。未提交、推送、合入、部署，未启动其他agent或并行工作树写者；下一步仅等待同源快照的真实完整机械门禁及只读验收。

## 恢复004增量：剩余公共fixture接线

### 实证与最小方案

003之后原六步机械门禁真实exit 0，但真正完整 `make test-race` exit 2：REST累计300.114秒超时，`TestThumbnailVersionPinTrimmedKeySSECParity` 自身仅0秒，栈在尚未复用schema的 `newAuthRESTTestWithRepo → Migrate → applyMigration`。其余包（含正常umask022的config）通过；本次恢复 `failed-full-race-003/` 的原日志及结果不改写，008仍未解除阻断。

沿用原截止 `2026-10-16T07:07:31.573090+00:00`，不新增24小时。更小方案是不接线，但会保留已观测的重复DDL；已有seed足够，无需第二缓存、服务、包、预热或新性能框架。仅新增 `restFixtureSchemaDir(t, ctx, name)`：在实际测试内部 `t.Helper`、独立 `t.TempDir`、向既有 `sharedRESTFixtureSchema.seed` 原样传ctx及目标路径，再返回dir；首次真实迁移与每次Sync/关闭仍计入原300秒预算。

唯一旧代码增量是下列10个公共构造函数各替换一行 `dir := t.TempDir()`；原函数行数、imports、注释、全部真实Open/Migrate、鉴权、Local FS、FileService/handler/HTTP装配、错误处理及清理逐字保留，所有旧测试和断言保持。`setupTestWith`、003的helper/12项回归/基准、inline Test数据库及迁移故障路径不改。

| 文件（`internal/api/rest/`） | 公共fixture | ctx | 固定文件名 |
|---|---|---|---|
| acl_test.go | newAuthRESTTest | context.Background() | acl.db |
| conditional_test.go | newRESTTest | context.Background() | c.db |
| thumbnail_cache_test.go | newThumbnailCacheREST | context.Background() | c.db |
| thumbnail_coalesce_test.go | newThumbnailCoalesceREST | context.Background() | c.db |
| thumbnail_identity_test.go | newThumbnailIdentityREST | context.Background() | thumb.db |
| thumbnail_test.go | newRESTTestWithRepo | context.Background() | c.db |
| thumbnail_test.go | newAuthRESTTestWithRepo | context.Background() | acl.db |
| thumbnail_test.go | newThumbnailAccessHarness | ctx | access.db |
| thumbnail_test.go | newThumbnailFailingGetServer | context.Background() | fg.db |
| thumbnail_test.go | newThumbnail304Harness | context.Background() | c.db |

新增仅 `rest_fixture_schema_adapters*_test.go`：钉住10处精确接线、5种固定文件名的独立目录/文件、真实写入/关闭/重开与后续副本隔离，以及3种认证公共fixture的无凭据401/私有读取403、公读正例、同Key不同内容与ACL隔离。原有schema完整性、真实迁移、事务、并发与失败证明沿用并继续执行，不重新设计。

本次实际命令、exit、日志仅写 `resumes/004/local/`，正常测试子进程umask022，私有日志077/0700。以下为本次实际结果；完整原六步与整仓 `make test-race`、独立008验收仍交回控制器实际执行，不预填成功。

### 实际增量验证：整包race仍失败，008继续阻断

本次证据根目录：`/home/u1/.abr/projects/aero-vault-5c281c8a1991a53d/runs/implementation-24h-sol-max-20261015T070122Z/resumes/004/local/`。以下日志均有同名 `.command/.exit/.json`，完整命令与真实退出码汇总于 `commands.jsonl`，结果计数/日志摘要见 `results.json`，Go源码快照见 `tested-source.json`。本次没有采集新profile或在项目根输出测试binary。

**先记录旧成本，再单行接线：** 原认证测试 `TestThumbnailVersionPinTrimmedKeySSECParity` 的全部测试、请求与断言不改，前后均运行：

```bash
GOMAXPROCS=4 GOFLAGS=-p=1 go test -count=3 -timeout 300s -run '^TestThumbnailVersionPinTrimmedKeySSECParity$' -v ./internal/api/rest/
GOMAXPROCS=4 GOFLAGS=-p=1 go test -race -count=3 -timeout 300s -run '^TestThumbnailVersionPinTrimmedKeySSECParity$' -v ./internal/api/rest/
```

| 包耗时（3次完整用例） | 接线前 | 接线后 | exit及日志 |
|---|---:|---:|---|
| 普通 | 5.457s | 0.495s | 均0；before-auth-ordinary.log / after-auth-ordinary.log |
| race | 3.702s | 1.964s | 均0；before-auth-race.log / after-auth-race.log |

接线后普通三次用例0.36/0.06/0.07秒，race为0.81/0.07/0.07秒；首个用例计入冷构建，无计时外预热。只说明局部接线收益，不能作为整包race成功或性能硬阈值。沿用上文003已验证的Fresh/Cold/Amortized基准和完整schema隔离证据，本次未另建基准框架或重复预热。

**红绿灯与隔离/鉴权：**

- `red-common-constructor-wiring.log` exit 1：`go test -count=1 -timeout 300s -run '^TestRESTFixtureSchemaAdaptersCommonConstructorWiring$' -v ./internal/api/rest/` 在接线前10个公共构造函数全部失败于缺少指定调用；不是生产缺陷或耗时阈值。
- `green-fixture-ordinary.log` exit 0：`go test -count=1 -timeout 300s -run '^TestRESTFixtureSchema' -v ./internal/api/rest/`，16个顶层回归全部通过，4.372秒，无skip。
- `fixture-race.log` exit 0：`go test -race -count=3 -timeout 300s -shuffle=on -run '^TestRESTFixtureSchema' -v ./internal/api/rest/`，003原12项与新增4项共48次顶层执行全部通过，26.152秒，无skip或DATA RACE报告。
- 新增回归核对10处ctx/文件名精确接线；5种文件名均为仅含闭合schema副本的独立目录、独立0600文件；真实Open/Migrate后同Key的两份Local FS内容不同，关闭后WAL/SHM消失、重开保持本份数据，后续新副本无业务数据、样本摘要不变；3种真实认证fixture无凭据写入401、私有读取403，认证读取各自内容，A公读不改变B私有ACL，关闭A不影响B，新fixture不继承对象。
- 003原完整schema/初始数据、真实Migrate幂等/补迁移、独立事务、八路首次并发及迁移/取消/Close/读取/sidecar/O_EXCL失败回归均原样继续执行；既有seed的Sync/关闭/残留WAL拒绝契约不变。

**完整REST普通及race均真实运行，不缩范围、不加-short/-run、不改变300秒预算：**

```bash
GOMAXPROCS=4 GOFLAGS=-p=1 go test -count=1 -timeout 300s -v ./internal/api/rest/
GOMAXPROCS=4 GOFLAGS=-p=1 go test -race -count=1 -timeout 300s -v ./internal/api/rest/
```

- 普通：`full-rest-ordinary.log` **exit 0**，77.197秒，458个顶层测试全部PASS，0 skip。原auth、公读、SSE-C、Deny/provider、精确版本、缓存、取消与条件测试未改且全部执行。
- race：`full-rest-race.log` **exit 1**，**300.186秒包超时**；418个顶层测试已开始、417个PASS，其余41项未完成race（含正在超时的用例），0 skip，日志未见DATA RACE报告。超时时 `TestThumbnailDeadlineWhileParkedIs504` 已运行35秒，栈为原有 `pngBytes(8192,8192) → png.Encode → compress/flate`，位置 `thumbnail_test.go:3414`；不是本次认证fixture迁移栈。不能把前417项、代表性测试或定向race外推成整包/整仓通过。
- 本次完整REST race只运行一次。失败原样保留，不通过调整预算、图片尺寸、测试场景或断言来变绿，也不反复无改动重跑。本次白名单内接线没有消除008整包race阻断；原 `failed-full-race-003/` exit 2及新失败同时保留。

**静态与范围核对：** `adapters-ast.log`、`adapters-cyclo.log`、`rest-vet.log`、`gofmt-check.log`、`filesize.log`、`invariants.log`、`diff-check.log`、`tested-scope.log` 均exit 0。新增文件81/102/58行，目录函数8行，最大函数33行，复杂度≤10。2804条基线路径到2807条，仅6个旧测试文件各指定单行（合计10行）、3个新adapter测试文件及本需求的追加段落变化；原文件行数/其他字节/模式、003全部文件、HEAD/分支/索引以及受保护控制文件和原失败证据核对不变。Go源码集合摘要为 `ce9603d14a3fda7da8c26fed654bdb1c415d3ce4fbdfccfb60709da2180f74be`。

### 未执行门禁及边界

本恢复未运行新的完整 `make check`、`python3 cli.py accept`、`make web-check` → `make build` → `make web-smoke` 或整仓 `make test-race`（`cmd/server + internal`），这些仍待控制器真实执行并绑定同源快照；机械阶段 `git diff --check` 也待重跑，本地该项已exit 0。003旧六步exit 0不代表004已通过，008独立重验仍待执行，不预填PASS或推进后续槽位。

当前结论是**限定接线及本地隔离/鉴权回归完成，但整包REST race门禁失败、008阻断未解除**。后段原有测试的race仍未完成；不证明整仓race、真实OS Sync/Close/磁盘故障注入、跨平台/压力/RSS、PostgreSQL/Qdrant、外部服务或生产行为。原大PNG构造与其他inline数据库均不在本次允许变更内，未继续修改。未改生产Go、config、SQL迁移、接口、SDK、Go依赖、Makefile、预算、控制器或其他报告；未提交、推送、合入、部署或外部写入，未启动其他agent或并行写者，原截止不延长。

## 恢复005增量：其余17个公共空库fixture最小接线

### 范围与保持项

004完整普通REST为458项、77.197秒、exit 0，完整REST race仍300.186秒超时（418项开始、417项通过，栈在原8192² PNG编码）；随后父级门禁被中断、真实exit 130。两者均保留，不继承PASS。005只继续复用已有 `restFixtureSchemaDir`，没有修改PNG构造、压缩、bytes/像素、8192²、4槽、时限或任何旧场景，也没有增加图片缓存、预热或第二schema机制。原截止仍为2026-10-16 07:07:31 UTC。

下表均在 `internal/api/rest/`，各仅将该函数唯一 `t.TempDir()` 表达式换成 `restFixtureSchemaDir(t, 原ctx, 原数据库名)`；其中3处仍嵌在原 `filepath.Join` 内，不挪语句、不加变量、不改imports。原函数行数、模式和其他全部字节保持，真实Open/Migrate、manager/Principal、tenant、故障注入、同步/关闭、Local FS/HTTP/清理及所有旧断言不动。

| 原文件 | 构造函数 | ctx | 数据库名 |
|---|---|---|---|
| admin_audit_test.go | newAdminAuditTest | context.Background() | audit.db |
| admin_files_delete_test.go | newAdminDeleteEnv | ctx | admin-delete.db |
| admin_files_delete_test.go | envRepo | ctx | mgr.db |
| admin_keys_test.go | testAdminHandler | context.Background() | admin.db |
| admin_scope_test.go | newAdminScopeTestServer | ctx | scope.db |
| admin_tenants_test.go | newAdminTenantTest | context.Background() | tenants.db |
| buckets_test.go | newBucketTestServer | ctx | b.db |
| buckets_test.go | newBucketQuotaAuthTestServer | ctx | quota-auth.db |
| enterprise_access_test.go | newEnterpriseRESTTest | ctx | enterprise.db |
| idempotency_test.go | idemTestRepo | context.Background() | idem.db |
| management_test.go | newManagementRESTTest | ctx | x.db |
| thumbnail_close_test.go | newCloseErrorThumbnailREST | ctx | close-errors.db |
| thumbnail_close_test.go | newCompletePayloadErrorREST | ctx | thumbnail.db |
| thumbnail_content_encoding_test.go | newContentEncodingHarness | context.Background() | encoding.db |
| thumbnail_error_redaction_test.go | newPrefixErrorThumbnailREST | ctx | prefix-error.db |
| thumbnail_sniff_test.go | newNilThumbnailSourceREST | ctx | nil-source.db |
| thumbnail_sniff_test.go | newDeadlineSniffREST | ctx | deadline-sniff.db |

新增仅 `rest_fixture_schema_remaining_{wiring,admin,enterprise}_test.go`，复用已有工具，无新builder/cache。4项顶层回归钉住17处ctx、文件名及真实Open/Migrate；实际admin-delete factory的目录/文件/连接、DDL/版本补迁移、Local FS、删除/审计/outbox及后续副本隔离；匿名401、非admin403、operator正例；scope factory的key/tenant权限隔离；enterprise factory同Key不同正文、无凭据401、租户冲突403、单份租户停用和关闭不影响其他副本。003/004全部helper、16项回归、fresh/cold参考、并发证明、AI和inline Test原样保留。

### 实际验证与保留的修正过程

所有证据仅在 `/home/u1/.abr/projects/aero-vault-5c281c8a1991a53d/runs/implementation-24h-sol-max-20261015T070122Z/resumes/005/local/`。每次命令有 `.command/.exit/.log/.json`，汇总 `commands.jsonl`；测试子进程umask022，日志0600、外层077、目录0700，TMPDIR/GOTMPDIR也位于该local目录。所有Go命令固定 `GOMAXPROCS=4 GOFLAGS=-p=1`。

- `red-remaining-constructor-wiring.log` exit 1：接线前17个子用例全部明确缺少指定调用；不是生产缺陷或性能阈值。
- 首次 `green-fixture-ordinary.log` exit 1：新增enterprise补证错误复用了另一factory的匿名GET 403预期；当前enterprise registry实际返回401。只修正新增补证为该factory的真实401，并保留租户冲突403及认证正例；旧代码和断言未动。`green-fixture-ordinary-corrected.log` exit 0，20项顶层全通过，3.126秒。
- `remaining-cyclo.log` exit 1：新增scope补证复杂度12；仅将其两个quota断言原样提取为私有断言函数，之后 `remaining-cyclo-corrected.log` exit 0。没有放宽阈值或删除断言。`fixture-ast-corrected.log` exit 0，新文件133/82/58行，最大函数41行，复杂度≤10。
- 提取前完整普通/race均真实exit 0（57.116/183.356秒包耗时），连同当时源码快照完整移存 `pre-review/`。最终Go源码发生上述新增断言提取后，重新完整验证如下；不是无改动反复重跑已知失败，未覆盖前次结果或失败日志。
- 最终 `final-fixture-race.log` exit 0：`go test -race -count=3 -timeout 300s -shuffle=on -run '^TestRESTFixtureSchema' -v ./internal/api/rest/`，003/004原16项加005新增4项，共60次顶层执行全部通过，28.030秒，0 skip，无DATA RACE报告。原迁移、并发、事务、取消、Close/sidecar/O_EXCL失败证明全部继续执行。

同一组原Admin/Delete/Scope/Enterprise/Close测试（7项顶层、原范围/断言/300秒预算），前后命令均为：

```bash
GOMAXPROCS=4 GOFLAGS=-p=1 go test -race -count=1 -timeout 300s -run '^TestAdminDeleteFile_|^TestEnterpriseImageSharePublishAndExport$|^TestTenantAdminIsConfinedToItsTenant$|^TestThumbnailCloseErrorRESTClassificationAndHeaders$|^TestThumbnailCompletePayloadSourceErrorREST$' -v ./internal/api/rest/
```

`before-representative-race.log` exit 0，15.708秒；最终源码 `final-after-representative-race.log` exit 0，6.766秒。首次冷构建仍在实际测试内。中间 `after-representative-race.log` 的3.286秒也保留；环境耗时波动，局部收益不作为完整门禁或性能保证。

### 最终同源完整REST：普通与race均真实PASS

```bash
GOMAXPROCS=4 GOFLAGS=-p=1 go test -count=1 -timeout 300s -v ./internal/api/rest/
GOMAXPROCS=4 GOFLAGS=-p=1 go test -race -count=1 -timeout 300s -v ./internal/api/rest/
```

| 最终检查 | 真实exit | 包耗时 / 命令墙钟 | 完整结果 | 日志 |
|---|---:|---|---|---|
| 完整普通REST | 0 | 57.259s / 60.296s | 462项顶层全部PASS，0 skip | full-rest-ordinary.log |
| 完整race REST | 0 | 183.882s / 184.978s | 462项顶层全部PASS，0 skip，无DATA RACE报告 | full-rest-race.log |

两者均无-short/-run/-skip、未改300秒。旧超时用例 `TestThumbnailDeadlineWhileParkedIs504` 在最终race中28.34秒PASS，原PNG全部输入/编码和场景仍在计时内，未作PNG诊断或优化。

Go源码逐文件SHA位于 `local/tested-source.json` 的 `go_sources`（811个文件），其排序紧凑JSON映射的SHA-256为 `2daf2e18a7c2e60a66ce0fc4a37682d38569328753ab1c8bc7ef482e45a80503`。完整测试前后按此映射核对，不能将提取前的旧快照当最终源码证据。

`final-rest-vet.log`、`final-gofmt-check.log`（空输出）、`final-filesize.log`、`final-invariants.log` 均exit 0；精确范围核对只允许13个旧文件的17个表达式、3个新测试文件和本需求追加段落，其余源码/模式、旧PNG、全部原断言、003/004 helper/回归/参考、HEAD/分支/索引及控制文件与历史失败保持。

### 仍待控制器执行

本地完整REST通过仅满足前置条件，不等于008已验收。原六项完整门禁（`make check`、`python3 cli.py accept`、`make web-check` → `make build` → `make web-smoke`、机械 `git diff --check`）、真正完整 `make test-race`（cmd/server+internal、count1/300秒、无short/run）、同源核对及独立accept008均待控制器真实执行；全部PASS前不推进009。旧004超时、旧整仓race失败、父级exit130中断及本次两个中间失败均保留。未验证整仓race、Web交付、真实OS故障、跨平台/压力/RSS、PostgreSQL/Qdrant、外部服务或生产；未提交、推送、部署、启动其他agent或新方向。

