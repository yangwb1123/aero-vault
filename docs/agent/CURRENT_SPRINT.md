# CURRENT_SPRINT — 当前 Sprint 目标

> 更新于: 2026-06-16
> 下个 Sprint 计划见 `docs/ROADMAP.md`

---

## Sprint 目标: 端到端集成验证

本轮 Sprint 聚焦**全服务集成验证与测试覆盖率提升**，确保系统在真实 HTTP 请求流下的正确性。

### 范围

| 领域 | 目标 | 状态 |
|------|------|------|
| 全服务 HTTP 集成测试 | 启动完整服务器（SQLite+local+全协议），验证所有协议互操作 | ✅ 14 测试通过 |
| 协议互操作验证 | REST PUT → S3 GET → MCP list 同对象路径 | ✅ 完成 |
| 测试覆盖率提升 | 整体覆盖率 ≥70% | ✅ 70.2% |
| CI 集成测试门禁 | 全服务测试加入 CI 流程 | ✅ 完成 |

### 集成测试覆盖

新增 `internal/integration/fullserver_test.go` 覆盖：

| 测试 | 验证点 |
|------|--------|
| `Healthz` | 存活检查返回 200 |
| `Readyz` | 就绪检查（含 DB ping）返回 200 |
| `REST_CRUD` | PUT/GET/HEAD/LIST/DELETE + 404 + ETag |
| `Tags` | PUT/GET tags 完整生命周期 |
| `S3Compat` | S3 路径式 PUT/GET/HEAD |
| `SearchDisabled` | AI 未配置时返回 503 |
| `MCP` | JSON-RPC tools/list 返回工具列表 |
| `WebUI` | `/ui` 返回 HTML 内容 |
| `OpenAPI` | `/openapi.json` 返回 200 |
| `CORS` | OPTIONS 预检请求通过 |
| `ProtocolInterop` | REST 写入 → S3 读取 → MCP list 同对象 |
| `RangeRequest` | Range header → 206 Partial Content |
| `AdminEndpoints` | admin API 无 500 错误 |
| `ConcurrentCRUD` | 5 个 goroutine 并发写入和读取 |

### 禁止修改

- `internal/ai/` 中 AI 模型接口（Embedder / LLM / Reranker）
- `internal/storage/storage.go` 的 `Storage` 接口
- `internal/repository/repository.go` 的 `Repository` 接口
- SDK (`sdk/`) 代码
- 数据库迁移文件 (`internal/repository/migrations/`)

### 完成标准

- [x] 全服务集成测试通过
- [x] 协议互操作已验证（REST↔S3↔MCP 同对象）
- [x] 测试覆盖率 ≥70% ✅ 70.2%
- [x] `make check` + `go test ./internal/integration/...` 通过

---

## SPACE-P1A 增量范围：持久化基础限定切片

本增量不改上文 Sprint 目标、历史结果或其他禁止项。授权来自用户对紧邻精确确认问题（Space、成员、历史持久化及事务／租户隔离测试，暂不开 API/UI；仅新增一组四份迁移并同步 Sprint，不提交部署）的“继续”；不是整个个人／团队空间产品的授权。旧只读提案的“未批准／仅设计”标记保留为当时历史，不改写，也不因此重复请求同一精确确认。

### 阶段与允许路径

- **阶段 A（本阶段）：** 只追加本文并新增 `docs/requirements/personal-team-space-foundation-v1.spec.md`，落盘有界需求、准确拟新增 Go 文件子集及编码前 Persistence Design；不改任何 Go、测试或 SQL，不执行迁移，不声称已实现或测试通过。
- **阶段 B（后续，非本阶段操作）：** 仅在阶段 A 两文档范围机械守卫成立后，按该 spec 与本次 `allowed-paths.json` 的交集新增必要领域／具体 SQL 实现及测试文件；不能修改任何既有 Go 实现或测试，不扩宽 `repository.go`、`repository_interface.go` 或 Storage 公共接口。文件清单不足时先报告范围阻断，不静默扩张。
- 上文数据库迁移禁项的**唯一例外**为阶段 B 新增以下四文件；所有旧迁移仍禁止改动／重编，不授权其他编号或同号异名迁移：
  - `internal/repository/migrations/sqlite/0048_spaces_foundation.up.sql`
  - `internal/repository/migrations/sqlite/0048_spaces_foundation.down.sql`
  - `internal/repository/migrations/postgres/0048_spaces_foundation.up.sql`
  - `internal/repository/migrations/postgres/0048_spaces_foundation.down.sql`
- 阶段 A 读取四个目标路径均为不存在，已核对两种方言的 `0047_bucket_access_logs` up/down。当前会话只有文件读写工具，未完成全目录同号异名枚举；阶段 B 动码前机械核对 `0048_*` 占用及新增路径，发现冲突即停止报告，不覆盖、不跳号。

### 仅此切片的合同

- 仅建立未装配的 Space、当前成员、命令历史三类权威关系；完整 SubjectRef（来源、主体类型、稳定 ID）与 tenant 进入关系键／查询，个人关联由数据库唯一键裁决，同名团队可并存。
- Space ID、个人关联、承载桶绑定稳定。必要桶元数据只在未接线 SQL 创建用例中与 Space、初始人类管理员、历史严格同事务新建，拒绝认领已有桶，不复用旧 `CreateBucket` 的忽略冲突语义；测试仅用隔离临时库和 local FS。
- 成员变更先裁决同一根 revision/CAS，再检查最后一名有效人类管理员，成员／revision／历史同事务；不靠事务外计数或进程锁。失败不得留下部分初始化或虚假成功历史。
- 旧 default、其他旧桶／对象／版本／分享、owner 与 StorageKey 零回填、零搬迁；不改已有对象写入、删除、billing/audit 包装链。身份值和角色标签不等于员工资格或应用授权，不自动授予 delete/share/ACL/管理权限。
- **不装配** main/router/FileService/Manager/UI/CLI/协议；不新增 API/OpenAPI、SDK、配置、依赖、provenance、上传会话、接管或移交。P1B 全入口安全边界、P2 产品闭环及其他策略仍不在本次授权内；本基础片不产生可供用户使用的 Space。
- 不改 `AGENTS.md`、`HARNESS.md`、工程门禁／阈值／默认安全及任何其他禁项；不操作 Git 索引／分支，不提交、推送、合入或部署，不访问生产／外部写入，不用 Docker，不延长原截止 `2026-10-17T09:24:09.871261+00:00`。

### 验收状态与后续门禁

- [ ] 阶段 A 文档增量经机械范围／同源守卫确认（本会话仅落盘与回读，未执行机械检查）。
- [ ] 阶段 B 先留新功能失败测试证据，再实现领域、四迁移、SQL 事务、故障／并发／重开回归；缺实现编译失败不称为生产缺陷。
- [ ] 运行新包／SQL 定向普通与 race；随后对同一源码运行 `GOMAXPROCS=4 GOFLAGS=-p=1 make check`、`GOMAXPROCS=4 GOFLAGS=-p=1 python3 cli.py accept`、`GOMAXPROCS=4 GOFLAGS=-p=1 make test-race`（原全范围、count=1、每包 300 秒，不缩减）。
- [ ] 机械阶段执行 `make web-check` → `make build` → `make web-smoke` 确认继承 embed 未退化（不扩大前端修改范围），以及 `git diff --check`、精确范围／同源校验和独立只读验收；失败阻断下游，不放宽预算。
- 所有新增实现、测试及门禁在阶段 A 结束时均**待执行**；不提升上文历史 PASS 为本片结果。真实 PostgreSQL/Qdrant、外部身份／存储／KMS、HTTP2／代理、生产、压力／RSS及登录后浏览器 E2E 不在本片运行验证范围。仅全部机械门禁及独立验收通过才能称基础片完成，仍不构成空间产品交付或继续 P1B/P2 的许可。

### 阶段 B 本轮实际状态（2026-10-17）

上文阶段 A 的“本阶段／后续／待执行”是当时历史；本轮不再请求相同范围批准，也未把旧全产品提案纳入授权。

- [x] 动码前 `preflight` exit 0：与阶段 A 守卫快照完全一致，仅两份文档差异；已机械枚举全部 `0048_*` 及拟新增 Go 路径，均未占用。
- [x] 保留 `red-01/red-02`（exit 1，待实现的新领域／SQL 能力），再新增 spec §6 的准确 21 个 Go 文件及唯一允许的四份 0048；Space／成员／历史三表、严格新桶同事务、完整 SubjectRef、tenant、个人数据库唯一、根 CAS、最后 active user admin 和回滚均已实际实现。
- [x] 最终 `targeted-final-02`／`targeted-race-final-02` 均 exit 0，各 39 顶层新测试；两个独立 Open／连接池竞争、故障回滚、关闭重开、0047→0048 零回填、local Blob 字节及空表手工 down 已在临时库验证。PG 仅静态 DDL／合成错误／rebind，未跑实库。
- [x] 测试后 `source-freeze` exit 0：实际差异只有 21 新 Go + 4 新 SQL + 2 批准文档；原受保护文件内容／类型／模式、旧 Sprint 前缀、HEAD／分支／索引未变。没有覆盖既有脏 Go／测试／旧迁移或继承 Web 资产。
- [ ] 完整 `make check`／`python3 cli.py accept`／整仓 `make test-race`／Web 三步／`git diff --check`／最终 runner 同源范围与独立验收：留返回后的机械阶段执行，不预填 PASS。
- 本轮无 main/router/FileService/Manager/对象链/API/UI/协议接线，无真实旧桶认领、生产或外部写入。员工资格、接管／移交、分享及完整成员授权仍未批准待办；关系表与角色标签不等于可使用 Space。
- 完整实际符号、红绿灯、命令、退出码和未验证边界见 `docs/requirements/personal-team-space-foundation-v1.spec.md` §9；私有证据在本次运行 `implementations/p1a-001/local-stage-b/`，不写 runner 报告文件。

### 新24小时窗口续验（2026-10-17 · opencode-go/deepseek-v4.1-flash、thinking=max）

上文阶段 A/B 及旧失败、旧截止（`2026-10-17T09:24:09.871261+00:00`）均保留为历史；本窗口不重做 21 个 Go／四份 0048，也不扩 API/UI/P1B/P2。本窗口未改任何 Go／测试／SQL／迁移，只做同源验证恢复并追加本节。

- 诊断：同一源码（`code_sha256` 前缀 `9d0af721d5343baa`）完整 repository 包 `-race -count=1 -timeout 300s`、无 `-run/-short`、`GOMAXPROCS=4 GOFLAGS=-p=1`，exit 0，包 247.63s；`TestProbeQueryPlans` 32.66s 且排在全部 P1A internal 测试之后（P1A 占先序 198.66s 中的 40.33s）。余量约 52s，仅说明本次同预算复测通过，不确诊旧失败根因。
- 受控本地完整包级 race `local`（`local-full-race/attempt-001/result.json`）：repository 177.43s + spaces 1.006s，exit 0，39 个顶层新测试全过、无 race／死锁报告，`source_unchanged=true`，与诊断同 `code_sha256`。两次 probe 耗时 32.66s vs 6.64s、包耗时 247.63s vs 177.43s，宿主 I/O 波动大于 P1A 固定占用。
- 夹具复核：10 个 P1A 测试文件的每次 `spaceTestDB` 均有真实用途（失败注入、隔离、重开、真实 0047→0048 迁移），未发现可安全收敛的冗余空 schema 准备，故不改夹具、不新增回归。
- 完整 `make check`／`python3 cli.py accept`／整仓 `make test-race`／Web 三步／`git diff --check`／独立验收仍由后续机械阶段执行，本节不预填 PASS。

### 通知测试夹具单文件例外修复（2026-10-17 · opencode-go/deepseek-v4.1-flash、thinking=max）

上文全部历史（含 PREV 的旧 full-race exit 2、两份文档旧追加、旧“本窗口未改 Go”结论）原样保留；本窗口只执行用户在上一条精确确认后授权的最小单文件修复，不重做 21 个 P1A Go／四份 0048，不扩 API/UI/P1B/P2。

- **例外与基线：** 唯一旧测试例外 `internal/events/notifier_test.go`，动码前与窗口 `baseline/notifier_test.go` 逐字节一致（sha256 `1c9c03861e2492856f5faa156cca947944067f06812292039bba79ac2c51127f`）；另两份获批文档仅末尾追加。生产代码、全部迁移、其他旧测试、连接／PRAGMA／Open／Migrate、公共接口、依赖与配置全部冻结。
- **旧真实红灯（原样保留）：** `make test-race` exit 2；`--- FAIL: TestNotifierDelivery (5.64s)`；`notifier_test.go:149: migrate: begin 0020_bucket_policy: context deadline exceeded`；events 包 149.789s；尚未开始投递。模型汇总百分比不作为通过证明。
- **因果限度：** 旧测试在 `repository.Open`／`Migrate`／`SetBucketNotifications` 之前创建 5 秒 context，慢准备会先行耗尽投递窗口；这证明的是夹具计时边界，不是生产 Notifier 投递缺陷、死锁或 DATA RACE，也不能用一次绿色重跑确诊环境根因。
- **精确修复（本轮合同）：** 真实 `t.TempDir` 磁盘 SQLite、真实完整 Migrate、通知规则写入先在 testing.T context＋总 deadline（可取消、会关闭）内完成并照旧 Fatal；准备成功后才创建原样 5 秒投递 context。`NewNotifier`／`Run`、规则读取、HTTP POST、原事件与接收／超时断言仍在 5 秒内。投递仍 5 秒、包仍 300 秒；不换内存库／盘／sync／PRAGMA、不减迁移、不改其他测量输入。
- **回归矩阵（编码前登记）：**
  1. `TestNotifierDelivery` 原真实路径（磁盘库、完整迁移、httptest、原事件）继续通过；
  2. 新增 `TestNotifierDeliverySlowSetup`：本文件 test-only 受控准备延迟 > 5 秒，断准备实际 > 5 秒且在准备后仍获得完整 5 秒投递窗口并收到原事件；
  3. 反例：临时将同一 5 秒计时器移回准备之前，同一回归必须真实 FAIL；finally 恢复精确字节后须 PASS，保存注入前后 sha256、命令、exit 与日志。
- 新增函数只允许 `notifierTest*`／`TestNotifier*` 前缀；原文件其他既有函数逐字节保持。完整 `make check`／`python3 cli.py accept`／整仓 `make test-race`／Web 三步／`git diff --check`／独立验收在本节登记时均**待执行**，不预填 PASS；实施结果随后追加。

#### 本次修复实际结果与阻断（2026-10-17 11:20–11:45 UTC）

- **动码与恢复证明：** `internal/events/notifier_test.go` 基线 `1c9c0386…` → 最终 `da46ce38…`（仅 TestNotifierDelivery 与 `notifierTest*`／`TestNotifier*` test-only 函数；AST 窄范围检查 exit 0，gofmt 干净，256 行，函数 ≤ 50 行）；两文档仅末尾追加。反例注入 `c47d6570…` 时 `TestNotifierDeliverySlowSetup` exit 1（`notifier_test.go:248: timeout waiting for notification delivery`），finally 恢复后精确等于 `da46ce38…`（`cmp` 逐字节一致）且同回归 exit 0；004 注入与 005 恢复日志均在 `local-verification/`。生产代码 `notifier.go`、原 21 个 P1A Go、四份 0048 与全部迁移哈希与窗口基线逐字节一致。
- **定向红绿灯（GOMAXPROCS=4 GOFLAGS=-p=1、umask 022）：** `go vet ./internal/events/` exit 0；`TestNotifier` 普通 exit 0、race exit 0（原投递与新回归均 PASS）；原 39 项 P1A 普通 exit 0（39 PASS）、race exit 0（39 PASS）。
- **受控完整 local race（attempt-001，唯一一次，exit 4 未通过）：** argv 为 `go test -race -count=1 -timeout 300s -json ./internal/repository/ ./internal/spaces/ ./internal/events/`，无 run/short；`code_sha256=410cb401…`、`source_unchanged=true`、`controller_timed_out=false`、wall 581.945s。`TestNotifierDelivery` 2.54s 与 `TestNotifierDeliverySlowSetup` 7.64s 在 events 包内**通过**，spaces 1.16s 通过。**范围外冻结失败：** repository 包 300.594s 超时（`panic: test timed out after 5m0s`，未完成 `TestListExpiredNonCurrentVersionsPaginatesPastFreshCandidates`）；events 包 272.521s 失败于既有 `TestOutboxRelay_DeliversDeletedFactToL2` 两个子测试（`lease-expiry…` 8s、`receiver contract…` 43.31s，`event_outbox_relay_test.go:638`）。两文件哈希与窗口基线逐字节一致，且 PREV 同码曾通过；5 次负载探针 4 红 1 绿（绿时 IO full avg10=8.77），属主机 I/O 敏感的既有 flake／超时，不在本切片允许修复范围内。
- **阻断与宏门禁状态：** 控制器拒绝同 `code_sha256` 再次 local 尝试；`local-race.py require-local` 实测 exit 4（`same-code complete repository/spaces/events race is required`），因此返回后 runner 的 `run-gates.sh` 将在第一步阻断，`make check`／`accept`／整仓 `make test-race`／Web 三步／`git diff --check`／独立验收均**未由本窗口执行**，须由独立机械阶段实测并保留结果。未修改 local-race.py、冻结测试、包预算或任何控制器产物；旧 full-race 失败与 PREV 报告原文保留。

### SPACE-P1A-TESTBOUNDARY-5：五个旧测试的精确授权合同（2026-10-18）

用户对逐项列明五文件、原预算／租约／输入／断言不变的确认问题回复“继续”，本轮已获实施授权；旧 Proposed 与上述历史原样保留，不据此重复审批。原截止仍为 `2026-10-18T10:55:52.663808+00:00`，不续期。固定第一串行写者 `openai-codex/gpt-6.1-sol`、thinking=max；只改以下四个 repository 测试和两份文档末尾，随后冻结四文件，events 留给第二串行写者。

- `internal/repository/audit_governance_pending_idx_test.go`：Open 成功后、完整 Migrate 前注册 Close；先注册关闭观察、后注册 Close，依 t.Cleanup 的 LIFO 在真实关闭后以数据库 Ping／连接状态观察，不猜 goroutine 数量。新关闭回归附着既有两个有业务用途的数据库生命周期，不增加第三个空用途全迁移夹具；故障注入临时去掉 Close 必须实际红，finally 字节级恢复后同例实际绿。
- `internal/repository/lifecycle_test.go`：将既有 Close 注册前移至 Open 成功后、Migrate 前，保持原 Fatal 语义；只追加 Open／完整 Migrate／Close 的真实阶段耗时。
- `internal/repository/event_outbox_test.go`：原本已在 Migrate 前注册 Close，并无同样遗漏；只追加 Open／完整 Migrate／种数／业务／查询及 rows 消费／收尾 Close 计时，不改事务、审计、事实、payload、回滚、owner/token、终态或 fencing 断言。
- `internal/repository/lifecycle_pagination_test.go`：仅追加原三个用例准备、策略／种数、查询与断言的阶段耗时；fresh 先于 old、limit=1、72h 回溯、GLACIER 标记、精确 old Key／ID、真实对象与删除标记路径全部保持。
- `internal/events/event_outbox_relay_test.go`：本第一写者不修改；第二写者范围仅原三个 L2 子测试的 500 复位、仅请求 #2 压制 echo、原固定批次的被动计时／精确 fact 状态及完成记录观测，并补 always-500／never-echo／恒 echo 反例。原 deliverBatch／observer Claim 次数、50ms 租约、100ms 首 POST 超窗、5s HTTP／投递、1300／2600ms 等待与 MaxAttempts 不变；先直读状态，再保留原 Claim，空 Claim 不等于 delivered。禁止循环重投、额外批次、直接 Complete／Retry 或手改状态求绿。

**全部冻结项：** `pendingProbeNow`、整个 `pendingIdxSeed` 字面量、20 bindings／55,300 行及 ties／draining 分布、ANALYZE、索引／不全扫、原全部测试与断言均不改。库仍是独立磁盘 `t.TempDir()`、真实 Open 和全部 Migrate；不调 PRAGMA／sync／输入／预算，不缓存 schema，不新增项目文件／依赖／配置，不碰 notifier、生产、21 个 P1A Go、任何迁移、公共接口、门禁、Git 分支／索引、提交／推送／部署。新增 helper 小而领域相关；若新增测试，统一 `TestFixtureBoundary*`。所有耗时均在实际阶段结束时计算（defer 用闭包，不能提前计算 time.Since）。

**编码前验收登记（均待实测）：**

- [ ] 保存真正清理后观察的 Close 故障注入红灯、finally 精确源码恢复及绿灯；保留 diff／SHA256／command／exit／log，证据仅写本 RUN 的 `local-repository/`，不修改控制器或旧证据。
- [ ] 第一写者普通与 race 覆盖四文件受影响的全部原例及新回归；所有 Go 命令固定 `GOMAXPROCS=4 GOFLAGS=-p=1`，测试子进程 umask 022、count=1、每包 timeout=300s，不循环刷绿。
- [ ] 返回后控制器另做静态保留守卫、repository vet、索引／关闭重点定向 race 与 source checkpoint；这是补充，不代表四文件／整包通过。第二写者后仍需完整 repository／spaces／events local race，以及同源 `make check` → `python3 cli.py accept` → 原全范围 `make test-race` → `make web-check` → `make build` → `make web-smoke` → `git diff --check`、范围同源封存与独立验收。

确定的 Close 缺陷与累计 300s 根因分开：旧记录是前 17 顶层累计约 297.15s、末例仅约 3s 且停在 Migrate；修复关闭或定向绿色均不证明预算根因消除。不预填后续门禁 PASS，不声称 P1A 已验收、Space 产品已交付或 P1B／P2 获批；真实 PG／Qdrant、外部身份／存储／KMS、生产、压力／RSS与登录后业务 E2E 等仍未验证，费用未知。

#### 第一串行写者实际结果（2026-10-18 03:12–03:21 UTC）

- 两文档合同先追加、再动 Go，`local-repository/documents-before-go.json` 记录旧前缀完整且四 Go 当时未变。最终只改上述四个 repository 测试及两文档；events／notifier／生产／迁移未动。新增关闭回归是原两个 pending-index 生命周期上的清理后检查，没有新增顶层测试或第三个全迁移夹具。
- **真实红绿灯：** `05a-close-removed` exit **1**，两原例均在清理后的观察得到 `ping=<nil> open_connections=1` 而 FAIL；不是编译失败。`finally` 恢复精确源码，SHA256 `a9bde17b28c543d74b0206cb0d2f7b29fa5e34e540f0cfa522b093d61fa288f3` 与注入前逐字节相同；注入 SHA 为 `d5a525a7e8943b1d8f1b5db9a92ef06a6ce3136ae7c5e0df9baf33d32e985f06`。同一命令 `05b-close-restored` exit **0**，两例均在 Close 日志之后观察到 `ping=sql: database is closed open_connections=0`。红灯观察失败后才兜底释放库，不会掩盖遗漏。
- **定向实测：** `06-affected-normal` exit 0、5.535s；`07-affected-race` exit 0、28.810s，均 19 个原顶层用例全 PASS（四文件 18 个＋共享 openTestSQLite 的既有 malformed-JSON 用例），包含两例上的新关闭断言，无 DATA RACE。实际为 `go test [-race] -v -count=1 -timeout 300s ./internal/repository/ -run <affected-tests.json 的完整选择>`，`GOMAXPROCS=4 GOFLAGS=-p=1`、子进程 umask 022；这只是前置定向，不是整包运行。`08-vet` exit 0；`03-test-contracts`／`04-scope-after-go`／`09-local-preservation-and-phases` 均 exit 0，范围差异恰六路径；后者额外核对冻结种子前缀、outbox 原测试及 payload 构造、lifecycle 原业务例字节不变，分页差异仅计时，四 Go 与两次运行哈希一致。
- **实际阶段观测（普通／race）：** 索引夹具完整 Migrate 0.166／0.581s，原种数 0.321／9.094s，ANALYZE 0.032／1.990s，三次 plan 查询／rows／断言合计 0.250／2.213ms，Close 7.44／10.43ms。Outbox 原 20 个夹具完整 Migrate 合计 3.294／11.567s、业务合计 0.201／0.426s、Close 合计 0.117／0.154s。三个分页例准备各 0.173–0.182／0.588–0.594s，种数各 14.19–20.20／19.86–29.31ms，查询／rows 各 0.171–0.432／4.540–4.564ms；断言与 Close 另有日志。子阶段与业务／准备有包含关系，不相加冒充无重叠总耗时；不是旧失败时的 I/O 复现或 300s 根因证明。

全部本地 `.command/.exit/.log`、注入 diff／哈希和 `phase-summary.json` 位于：
`/home/u1/.abr/projects/aero-vault-5c281c8a1991a53d/runs/p1a-dispatch40-sol-flash-20261017T155929Z/implementations/five-tests-20261018T025539Z/local-repository/`。

**仍待返回后独立执行：** 控制器静态守卫／vet／重点 race／source checkpoint、第二写者 events、完整 local 与全部宏门禁及独立验收；没有生成 repository-source／accepted 或预填后续 PASS。关闭修复只证明清理缺陷及回归有辨别力，未证明累计 300s 根因消除；未提交／推送／部署，原截止不延长。

### SPACE-P1A-TESTBOUNDARY-5 第二串行写者实际结果（2026-10-18 · opencode-go/deepseek-v4.1-flash、thinking=max）

上文第一写者结果及全部历史前缀原样保留；本段只追加第二写者的 events 实测，不改四个已封存 repository 测试、notifier、生产、全部迁移、公共接口、依赖、配置或门禁。唯一 Go 改动为 `internal/events/event_outbox_relay_test.go`（test-only），两份文档仅末尾追加。原直接 `relay.deliverBatch` 调用与 observer Claim 次数保持：500=2/2、lease-expiry=2/1、receiver-contract=3/2；50ms 租约、100ms 首发 delay、5s HTTP/等待、1300/2600ms 原 sleep、原 MaxAttempts、payload/factID/退避/fencing 断言均未改。

- **500 子测试：** 仅请求 #1 返回 500，#2 恢复 2xx+echo。首轮同一 fact 精确状态为 `pending`、`attempts=1`、`event_outbox_delivered=0`；1300ms 后重试为 `delivered`、`attempts=2`、完成记录 1。空 Claim 仍是原探针，但不再作为交付证明。
- **lease-expiry：** 首轮 POST 跨过 50ms 租约后为 `inflight`、`attempts=1`、完成记录 0；第二次固定批次 re-claim 后为 `delivered`、`attempts=2`、完成记录 1。本次实测未因 50ms 持久化耗尽而 blocked。
- **receiver-contract：** 仅请求 #2 压制 echo，测试在 #3 前显式恢复的 echo 不再被 wrapper 覆盖；中间为 `pending`、`attempts=2`、完成记录 0，恢复后为 `delivered`、`attempts=3`、完成记录 1。
- **新增正反例：** `TestFixtureBoundaryOutboxRelayL2DeliveryProof` 中 always-500、never-echo 均停在 `pending` 且完成记录 0；恒 echo 为 `delivered` 且 `event_outbox_delivered=1`。新增只读 helper 用明确临时 DSN + 既有 `openRelayTestRepoAt` 直读 `event_outbox` 与 `event_outbox_delivered`，不反射公共接口、不改生产、不驱动额外批次。
- **故障注入（均只改本获批文件，finally 精确恢复）：** 旧 receiver wrapper（`count>=1` 再次压制 #3 echo）exit 1，状态 `failed attempts=3` 且未 delivered；旧 500 不复位 exit 1，状态 `pending attempts=2` 且未 delivered；把 pending/空 Claim 误当 delivered 的注入 exit 1。恢复后最终 SHA256 `c00600d47fd12a4cd4e974f962f5cb3a1a323b9ccad4383a15021b47a072c197`，三次 `cmp` 均 exit 0。
- **定向：** 原 L2 例普通 exit 0（7.03s）；原 L2 例 + 新回归 race exit 0（13.16s，无 DATA RACE）；events 整包普通 exit 0（16.91s）。`test-contracts.go` exit 0；`scope.py verify` exit 0（7 路径）；gofmt 与 `git diff --check` exit 0。
- **完整 local race：** `python3 .../local-race.py local` attempt-001（唯一一次）exit 0，argv 为 `go test -race -count=1 -timeout 300s -json ./internal/repository/ ./internal/spaces/ ./internal/events/`，无 run/short；repository 137.66s + spaces 1.007s + events 26.886s，wall 167.27s，`tests_passed=true`、`source_unchanged=true`、无 failed/unfinished，`code_sha256=6b3befb5792faab773caba274dbed85dfbf9769e6e7d252e25fbf763f4e903ca`。这是完整 repository/spaces/events race 证据，不是宏门禁或独立验收。
- **证据：** `implementations/five-tests-20261018T025539Z/local-events/` 下的 `.command/.exit/.log`、注入 diff/SHA/cmp；完整 local 在 `local-full-race/attempt-001/`。未覆盖 `make check`、`python3 cli.py accept`、完整 `make test-race`、`make web-check` → `make build` → `make web-smoke`、最终同源封存与独立验收；这些均待控制器返回后执行，本段不预填 PASS。
- 本次 50ms 夹具通过不证明生产 30s 租约无缺陷，也不证明旧 repository 累计约 297s／300s 根因已消除；外层状态/计时仍不能拆出每次 fsync／Commit。未提交、推送、部署，原截止不延长。

### SPACE-P1A-LABEL-001：观测标签命名续验（2026-10-18）

- 五测试实现后的完整三包 local race 与 make check 已真实 exit 0；随后 cli.py accept 在 I6 exit 1，原失败保留，后续门禁未执行，不把模型任务完成率当工程通过。
- 原因是既有 I6 全文件子串扫描把三处计时标签 `"assertions"` 误判为断言库导入；实际 imports 仍只有 context/testing/time。仅将 lifecycle_pagination_test.go 三标签同义改为 `"result-checks"`，其余 Go 字节、断言、测量对象、输入、依赖和检查器全部不改。
- Codex 两次网络调用失败且未写源码后，调度 Agent 在 runner 正常独占锁内完成此确定性替换；不换模型、不重做五测试。两文档只追加，原历史与备份保留，原截止 2026-10-18T10:55:52.663808+00:00 不延长。
- 已重现未改 invariants 的预期 exit 1；修改后同命令、原三分页普通/race、新源码完整 local、原七宏门禁及两指定模型独立只读复核均待执行，不预填 PASS。真实外部/生产验证与费用仍未知，不授权 API/UI/P1B/P2 或提交部署。

---

## SPACE-P1B-A-001：已批准六路径绑定探测准备片（2026-10-19）

本段只在末尾追加，上文历史前缀全部保留。用户已对准确六路径、最多四小时、保留完整门禁回复继续；这是本片许可，不重复申请，不以旧 Proposed 否认。P1A 与 P1B 设计已完成，不重做 40 方向、旧实现或旧五测试修复，也不以旧成功抵扣本片当前门禁。

### 本阶段与准确六文件

本阶段仅需求 spec＋本段，四个 Go 保持不存在。返回后须由控制器通过 docs checkpoint，下一阶段才允许新增 Go；本 Agent 不执行检查点、不写私有控制器。唯一合同见 `docs/requirements/personal-team-space-binding-probe-v1.spec.md`。

1. NEW `internal/spaces/binding.go`（后续领域值／窄只读端口）
2. NEW `internal/spaces/binding_test.go`（后续纯值正反例）
3. NEW `internal/repository/sql_spaces_binding.go`（后续一次 SELECT）
4. NEW `internal/repository/sql_spaces_binding_test.go`（后续磁盘 SQLite／错误／兼容）
5. NEW `docs/requirements/personal-team-space-binding-probe-v1.spec.md`（本阶段合同）
6. `docs/agent/CURRENT_SPRINT.md`（仅末尾追加，旧前缀不变）

无第七项目文件；所有旧 Go／测试、0048 及全部迁移、旧 spec、Repository/Storage/九方法 spaces.Store、依赖、配置、安全默认、SDK/AI/UI/协议/装配/门禁继续冻结。无 main/router/FileService/Manager/worker 接线，无授权、身份／员工政策、API、迁移或 P2；不创建生产绑定／桶／Blob，不认领旧桶、不回填，不授角色／ACL／删除权。

### 查询与错误合同（编码前登记）

- 固定新增 `BindingState`、`BindingBound/BindingUnbound`、`BindingResult`、`BindingReader.ProbeSpaceBinding`、`ValidateBindingInput/ValidateBindingResult`；SQL 具体类型独立实现，不扩旧接口。准确签名／字段／NULL 校验见 spec §4–6，后续实现遵守。
- 三态只描述绑定事实：合法 bound/unbound＋nil；fault 为完整零值＋non-nil error，零值／未知 state 不能当 unbound。结果含精确 tenant/bucket、tenant known/raw status、bound 真实非零 canonical UUID 与正 revision，不含 Allowed／成员角色。
- 严格复用现有校验并拒 bucket=`*`；无空键／坏 UTF-8／控制字符／首尾空白，无 default/trim/casefold／通配。一次 SELECT 独立观察 tenant 与精确绑定，不按 spc-前缀猜，不加缓存／锁／事务框架，生产实现只读。
- unknown tenant 也必须实际确认无绑定；unknown＋binding、partial NULL、坏字段均 fault。disabled／其他非 active 原样返回 bound/unbound 与 raw status，不在 probe 强行允许／拒绝；不授 disabled 访问权。原 GetSpaceByBucket 先查 active tenant 的 ErrTenantUnavailable 语义不变。
- DB／Scan／context／收尾／异常 sql.ErrNoRows 都不能伪 absence，保留 Is/As cause；异常 no-row 显式 ErrPersistence，不借旧分类器转 ErrNotFound。I1 每次出现独立编号／实参，SQLite 真执行＋PG 静态不等实库。

### 待执行验证与边界

- [ ] 控制器 docs checkpoint：准确两文档、完整旧前缀、四 Go 不存在；本阶段未执行机械 scope/hash 守卫。
- [ ] 测试先红后实现；严格输入／值、active/unknown/disabled、跨 tenant 同名桶／同 UUID、只读完整事实快照、错误 cause 与原 getter 兼容；真实 t.TempDir 磁盘 SQLite＋完整 Migrate，FK 不关闭。
- [ ] 新 Go 真实故障注入必须断言红而非编译错；保存 before/injected/restored SHA、diff、command/exit/log，finally 精确恢复、同命令绿；不修改旧测试或控制器。
- [ ] 新功能普通／race，独立只读 DeepSeek/max 复核，再当前源完整 repository/spaces/events local race；定向、模型报告、P1A／设计旧成功均不替代本片证据。
- [ ] 同源原七命令：make check → python3 cli.py accept → 原完整 make test-race → make web-check → make build → make web-smoke → git diff --check；范围／同源／旧文档与迁移 hash 守卫及新 Codex/max 独立验收，失败停下游。

所有 Go 子进程固定 GOMAXPROCS=4 GOFLAGS=-p=1、umask 022，本片测试 count=1／每包 300 秒；保留原 make 子目标参数和完整 cmd/server＋internal race，不缩 run/short、不改 PRAGMA/sync／连接／规模／TMPDIR，不用内存库／盘刷绿。每个新 Go ≤500 行、函数 ≤50／复杂度 ≤10；本 spec ≤400 行、本段追加 ≤80 行，不扩豁免。

固定窗口 `2026-10-19T00:50:22Z → 2026-10-19T04:50:22Z`，准备计入，重试／恢复／launch 不续期、不新开 24 小时。已登记备份不重做；失败／拒绝／超时保留现场、不自动回滚、不盲重放。不读凭据，不碰全局 Pi/provider/认证/账户池，不做 Git 索引／分支、提交推送部署、生产或外部写入。

**本阶段未编码、未测试、未故障注入、未运行任何工程门禁；上述全部待执行，不预填通过。** 真实 PG/Qdrant、外部身份／存储／KMS、HTTP2／代理、生产、压力／RSS、登录后业务 E2E 未验，费用 unknown。最终也仅未装配准备能力，不是 Space 权限完成、全 P1B 或 P2 产品交付。

### SPACE-P1B-A-001 第二串行实现写者实测（2026-10-19）

上文文档阶段历史原样保留；完整回读 documents-source.json／spec，确认 runner 已执行 docs checkpoint exit 0 后才新增四 Go。固定合同未改，两文档只追加本次结果。新文件依次 81／100／110／431 行，具体符号及完整证据索引见 binding-probe spec §10。

- 未装配 BindingReader＋成功值／error 三态；一次 SELECT 完整观察 tenant 与精确 binding，raw status 不作授权，异常 NoRows／NULL／Scan／DB／context 均不伪 absence。原九方法 Store、严格 getter 与其他既有 Go／测试／全部迁移、装配／依赖／配置／门禁未改。
- `02-new-feature-red` exit 1 为缺实现编译红；`04-targeted-normal` exit 1 为新夹具 UpdatedAt 期望过旧，仅在新夹具重读完整命令后根修正，原失败保留。最终 `07-final-normal`／`08-final-race` 均 exit 0，9 个新顶层＋4 个原 getter／tenant／snapshot 例全 PASS；两包 vet 与新文件结构／只读 contract 均 exit 0。
- 只读矩阵含 active／旧未登记 tenant＋旧桶／disabled／其他 raw 状态、同 UUID／同名桶隔离；六表完整逻辑行、版本 owner／StorageKey 与三个 local Blob 字节前后相同。真实磁盘 SQLite＋完整 Migrate，未改 FK／PRAGMA／sync／连接／输入规模／TMPDIR。
- 故障注入仅暂改新 SQL Go，把公共 DB fault 返回 unbound,nil；相同 closed_database 回归 before=0、injected=1（真实断言 FAIL）、finally restored=0。before／restored SHA=`8ac2ffba038eab387f0bc1d945e2bd73f8b2ea295804c3ea0627c3951caee744`，injected SHA=`c7ab8f2bf748d56f6c890a4d383649e0e029bbed763fc050937b5799d88e43e3`，diff／command／exit／log 与精确恢复证明保留。
- 唯一一次本 RUN `local-race.py local` exit 0；完整 repository／spaces／events `-race -count=1 -timeout 300s -json`、无 run／short：145.909／1.007／26.970s，248 顶层 PASS，无 failed／unfinished、无源码漂移；code SHA=`b792dc80e38e1a2969639cb94fe44a7121c56da129d9f08012e7705330352250`。所有 Go 子进程固定 GOMAXPROCS=4 GOFLAGS=-p=1、umask 022。
- `scope.py verify` exit 0，准确六路径、旧前缀保留。证据在本 RUN `local-implementation/` 与 `local-full-race/attempt-001/`；这不是宏门禁或 accepted，不用 P1A 旧成功抵扣。
- 返回后仍待新 DeepSeek/max 只读复核、控制器同源复核／原七宏门禁及新 Codex/max 独立验收。真实 PG 未运行（只有静态／rebind／synthetic cause），真实驱动 Close 故障未复现（仅 scanner seam）；外部／生产／业务 E2E 未验。仍只是未装配准备能力，不是 Space 权限或全 P1B 交付，无提交推送部署；截止仍为 2026-10-19T04:50:22Z。
