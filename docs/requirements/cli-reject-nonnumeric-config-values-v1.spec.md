# 方向：拒绝非数值/负数参数 —— 破坏性配置写入前的 CLI 校验（验收规格 · 已验证现状）

> **模块：** `internal/cli`（`cli_admin.go` · `cli_admin_buckets.go` · `cli_search.go`）
> **来源分析：** `docs/auto/analyses/internal-cli-17314662.json`（方向 1）· **日期：** 2026-08-07 · **HEAD：** `acfaaf4`
> **评分：** 价值 9 / 风险降低 9 / 工作量 2 / 置信度 9
> **状态声明：** 本文是**验收契约**而非绿地设计：逐条核验方向引证（§1）、登记服务端真实校验现状（§2）、原样保留三条验收检查并映射为可执行测试矩阵（§4）。超范围项一律不做（§5）。

---

## 1. 证据核验（方向引证逐条对照当前 HEAD）

| # | 方向引证 | 当前 HEAD 位置 | 核验结论 |
|---|---------|----------------|---------|
| E1 | `internal/cli/cli_admin.go:282` — `adminTenantQuota` 忽略 `fmt.Sscanf` 错误 | `adminTenantQuota` :278；`fmt.Sscanf(args[1], "%d", &maxBytes)` :284、`fmt.Sscanf(args[2], "%d", &maxObjects)` :285，**两处返回值均未检查** | ✅ **行号漂移**（282→284-285），语义成立：`"abc"` → n=0、变量保持 0、err 被丢弃（实证见 §2.1） |
| E2 | `internal/cli/cli_admin.go:306` — `adminTenantBudget` 忽略 `fmt.Sscanf` 错误 | `adminTenantBudget` :302；`fmt.Sscanf(args[1], "%f", &budget)` :308，返回值未检查 | ✅ **行号漂移**（306→308），语义成立：垃圾输入 → `daily_budget_usd=0` → **清除租户预算覆盖**（`admin.go:79` 注释：0 clears the override → 回落到全局 `AI_TENANT_DAILY_BUDGET_USD`） |
| E3 | `internal/cli/cli_admin_buckets.go:34` — `adminBucketLifecycle` 忽略 `fmt.Sscanf` 错误 | `adminBucketLifecycle` :30；`fmt.Sscanf(args[1], "%d", &days)` :37，返回值未检查 | ✅ **行号漂移**（34→37），语义成立：垃圾输入 → `days=0` |
| E4 | `internal/cli/cli_admin_buckets.go:143` — `adminBucketQuota` 忽略 `fmt.Sscanf` 错误 | `adminBucketQuota` :140；`fmt.Sscanf(args[1], "%d", &maxBytes)` :147、`fmt.Sscanf(args[2], "%d", &maxObjects)` :148，返回值未检查 | ✅ **行号漂移**（143→147-148），语义成立 |
| E5 | `internal/cli/cli_search.go:17` — `cmdSearch -k` 忽略 `fmt.Sscanf` 错误 | `cmdSearch` :12；`case "-k": fmt.Sscanf(args[i+1], "%d", &k)` :23，返回值未检查 | ✅ **行号漂移**（17→23），语义成立：`-k abc` → `k=0` 仍发请求 |
| E6 | `internal/service/file_crud.go:54,64` — 配额仅当 `MaxBytes/MaxObjects > 0` 才执行 | :54 `deltaBytes > 0 && bcfg.BucketMaxBytes > 0 && …`（bucket 配额）；:64 `delta > 0 && q.MaxBytes > 0 && …`（tenant 配额）；豁免门 :47 `if bcfg.BucketMaxBytes <= 0 && bcfg.BucketMaxObjects <= 0 { return nil // unlimited }` | ✅ **行号精确**。`0` 语义 = unlimited：`quota t abc xyz` 静默写 `0/0` = **移除生产租户配额**（方向核心断言成立） |
| E7 | `internal/service/bucket_notifications.go:46` — `SetBucketLifecycleFull` 原样存储 | `SetBucketLifecycleFull` :46-58：仅 `authorizeBucket` 后直写 `repo.SetBucketLifecycleFull`，**无 days 校验** | ✅ **行号精确**。服务端不校验 ⇒ CLI 是唯一防线 |
| E8 | `internal/service/file_bucket_settings.go:147` — `SetBucketQuota` 原样存储 | `SetBucketQuota` :147-153：仅 `authorizeBucket` 后直写 `repo.SetBucketQuota`，**无负值/数值校验** | ✅ **行号精确** |

**补充核验（方向未引、影响范围判定的服务端现状）：**

| 位置 | 现状 | 结论 |
|------|------|------|
| `internal/api/rest/admin.go:53-71` `SetQuota`（tenant 配额） | JSON decode 后直传 `svc.SetQuota`，无校验 | 服务端**零校验**；`file_features.go:25` → `repository/quota.go:65-78` 原样写库 |
| `internal/api/rest/admin.go:88-99` `SetBudget`（tenant 预算） | **有** `if body.DailyBudgetUSD < 0 → 400 InvalidArgument`（:90-93） | ⚠️ **方向"no negative-value check exists anywhere"表述对预算不精确**：服务端拒负值。但 CLI 垃圾输入 → 0 **绕过**该检查（0 合法 = clear override），CLI 校验对预算仍必要 |
| `internal/api/rest/admin.go:207-235` `PutBucketLifecycle` | JSON decode 后直传 `SetBucketLifecycleFull`，**无 days 校验** | 服务端零校验；负 days 直写库 |
| `internal/api/rest/admin.go:239-259` `PutBucketQuota` | JSON decode 后直传 `SetBucketQuota`，无校验 | 服务端零校验；负 quota 直写库（枚举条件 `> 0` 使负值静默失效） |

**方向问题陈述核验（当前状态）：**

| 陈述 | 核验 |
|------|------|
| "fmt.Sscanf errors are ignored … garbage input silently coerces to 0 and the command exits 0 after the server accepts it" | ✅ **成立**（E1-E5，全部五处） |
| "quota enforcement only triggers when MaxBytes/MaxObjects > 0 … silently REMOVES a production tenant's quota (0 = unlimited)" | ✅ **成立**（E6，:47/:54/:64） |
| "The server performs no validation either" | ✅ **成立**（配额/生命周期，E7/E8 + 补充核验）；⚠️ 预算例外：服务端拒负值（`admin.go:90`），CLI 仍须防非数值 → 0 |
| "no negative-value check exists anywhere" | ⚠️ **部分成立**：CLI 五处均无负值检查（✅）；服务端预算有负值检查（❌ 例外）；服务端配额/生命周期无（✅） |

### 2.1 实证：`fmt.Sscanf` 的真实接受域（本仓库 Go 实测）

| 输入 | `fmt.Sscanf(s,"%d",&i)` | 后果 |
|------|------------------------|------|
| `"abc"` | n=0, i=0, err=`expected integer`（**被忽略**） | 静默 0 → 核心 bug |
| `"10abc"` | **n=1, i=10, err=nil** | **尾部垃圾被静默接受**（Sscanf 不要求全串消费）⇒ 仅检查 err 不够，须全串解析 |
| `"-5"` | n=1, i=-5, err=nil | 负值原样放行，无任何检查 |
| `"12.5x"` | n=1, f=12.5, err=nil | 同上（`%f`） |

> 推论：实现必须用 **`strconv.ParseInt(s, 10, 64)` / `strconv.Atoi` / `strconv.ParseFloat(s, 64)`**（全串严格解析，拒绝 `10abc`/`0x10`/`12.5x`），并**显式 `< 0` 检查**；仅把 `Sscanf` 的 err 接住不足以满足"拒绝非数值"。

---

## 2. 现状：破坏面与既有防线

| 命令 | 请求 | 服务端校验 | CLI 校验 | 垃圾输入实际后果 |
|------|------|-----------|---------|-----------------|
| `admin tenants quota <id> <max_bytes> <max_objects>` | `PUT /v1/admin/tenants/{id}/quota` | ❌ 无 | ❌ 无 | `0/0` → **配额移除（unlimited）**，exit 0 |
| `admin tenants budget <id> <daily_budget_usd>` | `PUT /v1/admin/tenants/{id}/budget` | ⚠️ 仅拒负值 | ❌ 无 | `0` → **清除预算覆盖**，回落全局默认，exit 0 |
| `admin buckets quota <b> <max_bytes> <max_objects>` | `PUT /v1/admin/buckets/{b}/quota` | ❌ 无 | ❌ 无 | `0/0` → bucket 配额移除，exit 0 |
| `admin buckets lifecycle <b> <days>` | `PUT /v1/buckets/{b}/lifecycle` | ❌ 无 | ❌ 无 | `0` → 生命周期规则失效/立即过期语义，exit 0；负 days 直写库 |
| `search <q> -k <n>` | `POST /v1/search` | — | ❌ 无 | `k=0` 仍发请求 |

既有测试（`internal/cli/cli_test.go`）：`TestCmdAdminTenants_Quota_PUTsBody` :1388（精确断言 body `max_bytes=1048576, max_objects=1000`）、`TestCmdAdminTenants_Budget_PUTsBody` :1430（`daily_budget_usd=12.5`）、`TestCmdSearch_CustomKAndMode` :803（`k=5`）、`TestCmdSearch_DefaultsKAndMode` :772（`k=10`）。**bucket quota / lifecycle 无任何测试**（`cli_admin_buckets_test.go` 仅 website 两条）。测试基建：`newTestClient`（`cli_test.go:67`）、`captureStderr`/`captureStdout`（:28/:48）、零请求断言模式 `hit` 计数器（先例 `TestCmdAdminFiles_Delete_EmptyTenant_Returns2` :1338-1357）。

> ⚠️ 验收过滤器现状：`-run 'TestAdminTenantQuota|TestAdminTenantBudget|TestAdminBucketQuota|TestAdminBucketLifecycle'` 当前匹配**零个**测试（既有测试名为 `TestCmdAdminTenants_Quota_PUTsBody` 等，且 bucket 两项不存在）⇒ 过滤器真空通过。**新增测试必须以此四个前缀命名**，使过滤器非空（§4 AC-1）。

---

## 3. 需求规格（FR，范围严格限定于方向）

### FR-1：五个入口全串严格数值解析（E1-E5）

`adminTenantQuota`（max_bytes/max_objects）· `adminTenantBudget`（daily_budget_usd）· `adminBucketQuota`（max_bytes/max_objects）· `adminBucketLifecycle`（days）· `cmdSearch`（`-k`），将 `fmt.Sscanf` 替换为全串解析：

| 参数 | 解析 | 拒绝示例 |
|------|------|---------|
| max_bytes / max_objects（int64） | `strconv.ParseInt(s, 10, 64)` | `abc` · `10abc` · `0x10` · `1e3` · 溢出 |
| daily_budget_usd（float64） | `strconv.ParseFloat(s, 64)` | `abc` · `12.5x` |
| days / -k（int） | `strconv.Atoi` | `abc` · `30d` · `-1` |

解析失败 → stderr 打印用法错误（须含**参数角色名** `max_bytes`/`max_objects`/`daily_budget_usd`/`days`/`k`、实际值、期望格式），**返回退出码 2**，不发 HTTP 请求。

### FR-2：负值拒绝，零值保留（E6 语义）

解析成功后显式 `x < 0` 检查（含 `days`/`-k`/budget float）：负值 → 同 FR-1 的错误路径（退出码 2、零请求）。**零值必须继续合法**：`quota t 0 0` = 显式清除配额（unlimited，`file_crud.go:47` 契约）、`budget t 0` = 清除覆盖（`admin.go:79` 契约）、`lifecycle b 0` 保持现状语义。FR 只拒绝**非数值与负值**，不引入其他取值范围约束。

### FR-3：fail-fast 顺序

校验在**构造 body 与 `c.do` 之前**完成（对齐既有参数校验先例：`len(args)` 不足 → usage + 2 的位置）。无效输入路径不得产生任何网络副作用；有效输入路径的请求方法/路径/body 与现状字节一致（§4 AC-3 锁定）。

---

## 4. 验收标准（方向原文三条，原样保留并测试化）

### AC-1 `go test ./internal/cli/ -run 'TestAdminTenantQuota|TestAdminTenantBudget|TestAdminBucketQuota|TestAdminBucketLifecycle' passes`

> 方向原文：*go test ./internal/cli/ -run 'TestAdminTenantQuota|TestAdminTenantBudget|TestAdminBucketQuota|TestAdminBucketLifecycle' passes*

**测试化约束：** 新增测试**必须以这四个前缀命名**（当前过滤器零匹配，见 §2 警示）。矩阵（全部 `internal/cli/cli_admin_test.go` / `cli_admin_buckets_test.go` / `cli_test.go`，全部用 `newTestClient` + `hit` 计数器）：

| 测试（命名即过滤器匹配项） | 输入 | 断言 |
|------|------|------|
| `TestAdminTenantQuota_NonNumeric_Returns2` | `["acme","abc","xyz"]` | exit 2 · stderr 含 `max_bytes` · `hit==0` |
| `TestAdminTenantQuota_Negative_Returns2` | `["acme","-5","10"]`、`["acme","10","-5"]` | exit 2 · `hit==0` |
| `TestAdminTenantQuota_TrailingGarbage_Returns2` | `["acme","10abc","5"]` | exit 2（全串解析，§2.1）· `hit==0` |
| `TestAdminTenantBudget_NonNumeric_Returns2` | `["acme","abc"]` | exit 2 · stderr 含 `daily_budget_usd` · `hit==0` |
| `TestAdminTenantBudget_Negative_Returns2` | `["acme","-1.5"]` | exit 2 · `hit==0` |
| `TestAdminBucketQuota_NonNumeric_Returns2` | `["b","abc","xyz"]` | exit 2 · stderr 含 `max_bytes` · `hit==0` |
| `TestAdminBucketQuota_Negative_Returns2` | `["b","-5","10"]` | exit 2 · `hit==0` |
| `TestAdminBucketLifecycle_NonNumeric_Returns2` | `["b","abc"]` | exit 2 · stderr 含 `days` · `hit==0` |
| `TestAdminBucketLifecycle_Negative_Returns2` | `["b","-1"]` | exit 2 · `hit==0` |
| `TestCmdSearch_NonNumericK_Returns2` | `["q","-k","abc"]` | exit 2 · stderr 含 `k` · `hit==0` |
| `TestCmdSearch_NegativeK_Returns2` | `["q","-k","-3"]` | exit 2 · `hit==0` |

### AC-2 新测试：非数值/负数参数 → exit 2 + usage 错误 + 零 HTTP 请求

> 方向原文：*New tests: non-numeric or negative args (e.g. quota t abc xyz, quota t -5 10, lifecycle b -1) return exit code 2, print a usage error, and make zero HTTP requests*

与 AC-1 矩阵为同一组测试（每行同时满足两条验收）：`hit==0` 断言复用既有先例 `TestCmdAdminFiles_Delete_EmptyTenant_Returns2`（`cli_admin_test.go:599-617`）的 httptest 计数器模式；stderr 断言 `captureStderr` 捕获后 `strings.Contains` 参数角色名 + 退出码 `2`。

### AC-3 既有有效输入测试保持精确 body 断言

> 方向原文：*Existing valid-input tests still assert exact request bodies (max_bytes/max_objects/days/budget) unchanged*

| 测试 | 断言（现状，不得改动） |
|------|----------------------|
| `TestCmdAdminTenants_Quota_PUTsBody` :1388 | body `max_bytes=1048576`、`max_objects=1000`，`PUT /v1/admin/tenants/acme/quota` |
| `TestCmdAdminTenants_Budget_PUTsBody` :1430 | body `daily_budget_usd=12.5` |
| `TestCmdSearch_CustomKAndMode` :801 / `TestCmdSearch_DefaultsKAndMode` :772 | body `k=5` / `k=10`（默认值不变） |
| **新增** `TestAdminBucketQuota_Valid_Body` | body `max_bytes=1048576`、`max_objects=1000`（bucket quota 现无测试，补正控制） |
| **新增** `TestAdminBucketLifecycle_Valid_Body` | body `days=30`、`action=hard_delete`（`--action` 变体） |
| **新增** `TestAdminTenantQuota_ZeroAllowed` / `TestAdminTenantBudget_ZeroAllowed` | `0` 仍发请求：body `max_bytes=0,max_objects=0` / `daily_budget_usd=0`（FR-2 零值保留） |

---

## 5. 范围边界（明确不做）

| 不做 | 理由 |
|------|------|
| 服务端（`internal/service` / `internal/api/rest` / `internal/repository`）增加配额/生命周期数值校验 | 方向明示"the CLI is the only defense"，验收仅限 CLI 行为（exit 2 零请求）；服务端改动属另一方向 |
| `adminJobsList --limit`、`cmdAdminAudit --limit`、`adminKeysAdd` 等其他非破坏性/未引证参数 | 不在方向引证五处内，且为 GET 查询参数，非破坏性配置写入 |
| 多余参数个数校验（如 `quota t 1 2 3` 静默忽略第 4 个） | 方向只要求非数值/负数拒绝；参数个数属另一问题 |
| `k=0` 拒绝、`days` 上限、budget 上限等取值范围扩展 | 方向范围 = 非数值/负数；零值语义由服务端契约定义（FR-2） |
| usage 文案/退出码约定变更（2 = 参数错误） | 沿用既有约定（`cli_admin.go:279-283` 等先例） |

---

## 6. 基线影响

- **行为收紧（有意为之，方向核心）：** 以下输入今天被接受、实现后被拒（exit 2）：① 非数值（`abc`）；② 尾部垃圾（`10abc` — Sscanf 静默取 10，§2.1）；③ 基数前缀（`0x10` — fmt `%d` 扫描接受 0x 前缀）；④ 负数（`-5`/`-1`）。前三类当前直接产生静默 0 写入，正是本方向要消灭的破坏面。
- **行为不变：** 合法十进制输入（含 `12.50`、`0`）解析结果与 `Sscanf` 一致，既有 body 断言测试（§4 AC-3）不受影响；预算负值在 CLI 被提前拒绝（exit 2）而非服务端 400（exit 1）——属预期 fail-fast，无既有测试依赖后者。
- **测试命名契约：** 新增测试须以 `TestAdminTenantQuota`/`TestAdminTenantBudget`/`TestAdminBucketQuota`/`TestAdminBucketLifecycle` 前缀命名，否则 AC-1 过滤器真空通过（§2 警示）。
- 全仓 `go test ./...` + `make check`（gofmt/build/vet/test、单文件 ≤ 500 行）保持全绿；`internal/cli` 各文件改动量 ≤ 每处数行，无行数风险。
