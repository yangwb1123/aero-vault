# 设计：CLI 破坏性配置写入前拒绝非数值/负数参数（全串解析 + 负值门禁 · 零服务端改动）

> **配套规格：** `docs/requirements/cli-reject-nonnumeric-config-values-v1.spec.md`（FR-1…FR-3 / AC-1…AC-3）· **模块：** `internal/cli` · **状态：** 设计（未实现）· **基线：** HEAD `acfaaf4`（`make check` 实测全绿）
> **门禁：** `make check` 全绿 · 生产文件 ≤ 500 行 · 单函数 ≤ 50 行 · 纯 stdlib（I6）· I1/I2/I3/I4 不涉及（无 SQL、无迁移、无中间件链、无存储 key 语义变更）

---

## 1. 证据复核（规格全部主张独立复验；Sscanf 接受域本次实证）

**基线实测：** HEAD `acfaaf4` 上 `python cli.py check-filesize` PASS；规格 E1–E8 全部逐条复核，行号精确到当前 HEAD。

| # | 规格引用 | 复核结论 |
|---|---------|---------|
| E1 | `cli_admin.go:284-285` — `adminTenantQuota` 两处 `fmt.Sscanf` 返回值未检查 | ✅ 精确。函数 :278，`Sscanf(args[1],"%d",&maxBytes)` :284、`:285` |
| E2 | `cli_admin.go:308` — `adminTenantBudget` `fmt.Sscanf(args[1],"%f",&budget)` 未检查 | ✅ 精确。函数 :302 |
| E3 | `cli_admin_buckets.go:37` — `adminBucketLifecycle` `Sscanf` 未检查 | ✅ 精确。函数 :30 |
| E4 | `cli_admin_buckets.go:147-148` — `adminBucketQuota` 两处 `Sscanf` 未检查 | ✅ 精确。函数 :140 |
| E5 | `cli_search.go:23` — `cmdSearch -k` `Sscanf` 未检查 | ✅ 精确。函数 :12 |
| E6 | `file_crud.go` 配额仅 `> 0` 生效 + unlimited 门 | ✅ 语义精确。unlimited 门 `if bcfg.BucketMaxBytes <= 0 && bcfg.BucketMaxObjects <= 0` :46（`return nil // unlimited` :47）；bucket 条件 :53（规格写 :54，为 return 行，off-by-one 属行漂移）；tenant `q.MaxBytes > 0` :63（规格写 :64，同理） |
| E7 | `bucket_notifications.go:46-58` `SetBucketLifecycleFull` 原样直写，无 days 校验 | ✅ 精确（仅 `authorizeBucket`） |
| E8 | `file_bucket_settings.go:147-153` `SetBucketQuota` 原样直写，无校验 | ✅ 精确（仅 `authorizeBucket`） |

**补充核验（服务端现状，规格 §2 表格复核）：**

| 位置 | 现状 | 复核结论 |
|------|------|---------|
| `api/rest/admin.go:56-71` `SetQuota`（tenant 配额） | JSON decode 后直传 `svc.SetQuota`，无校验 | ✅ 零校验 |
| `api/rest/admin.go:88-93` `SetBudget` | `if body.DailyBudgetUSD < 0 → 400 InvalidArgument` | ✅ 存在负值检查（规格 §1 修正成立：方向"no negative-value check anywhere"对预算不精确） |
| `api/rest/admin.go:207-235` `PutBucketLifecycle` | 直传 `SetBucketLifecycleFull`，无 days 校验 | ✅ 零校验 |
| `api/rest/admin.go:239-259` `PutBucketQuota` | 直传 `SetBucketQuota`，无校验 | ✅ 零校验 |
| `api/rest/search.go:65-66` | `if req.K == 0 { req.K = 10 }` | ✅ 服务端 k=0 → 默认 10；负 k 无校验（超范围，见 §8） |

### 1.1 Sscanf 接受域实证（本仓库 Go 1.26.5 实测，`/tmp/sscanfcheck`）

| 输入 | `Sscanf(s,"%d")` | `Sscanf(s,"%f")` | 设计后（ParseInt/ParseFloat + 非负检查） |
|------|-----------------|-----------------|----------------------------------------|
| `"abc"` | n=0, i=0, err（被忽略）→ **静默 0** | n=0, f=0, err → 静默 0 | 拒绝（exit 2） |
| `"10abc"` | n=1, i=10, **err=nil** | n=1, f=12.5, **err=nil** | 拒绝（全串解析，规格 §2.1 实证成立） |
| `"0x10"` | n=1, **i=0**, err=nil | — | 拒绝（比规格示例更糟：今天静默写 0 = 配额移除） |
| `"1e3"` | n=1, **i=1**, err=nil（部分扫描 "1"） | n=1, f=1000, err=nil | 拒绝 |
| `"-5"` | n=1, i=-5, err=nil | f=-1.5 | 拒绝（负值） |
| `"12.5x"` | — | n=1, f=12.5, err=nil | 拒绝 |
| `" 42"` | n=1, i=42（前导空白接受） | — | 拒绝（严格数字串；引号包裹才可能传入，收窄属 FR-1 意图） |
| `"+7"` | n=1, i=7 | — | **接受**（合法十进制，ParseInt 同接受） |
| `"NaN"` / `"Inf"` / `"-Inf"` | — | n=1, f=NaN/±Inf, **err=nil** | **拒绝**（须显式 `math.IsNaN/IsInf`，见 N1） |
| `"12.50"` / `"0"` / `"-0"` | — | 12.5 / 0 / -0 | 接受（-0.0 < 0 为 false，语义 = 0） |

> 结论与规格 §2.1 一致并强化：仅接住 Sscanf 的 err 不够（尾垃圾/部分扫描/基数前缀/NaN 均 err=nil 或产生部分值）；**必须全串解析 + 显式非负检查 + NaN/Inf 拒绝**。

### 1.2 设计关键新发现（规格未覆盖，设计据此补强）

| # | 发现 | 设计影响 |
|---|------|---------|
| N1 | `strconv.ParseFloat("NaN"/"Inf"/"-Inf")` 返回 **err=nil**；`NaN < 0` 为 false ⇒ 规格 FR-2 的 `x < 0` 检查漏掉 NaN/Inf。今天 `budget t NaN` → `json.Marshal` 失败被忽略 → **空 body** → 服务端 400（exit 1）——非静默写但属垃圾输入未被 CLI 拒绝 | **D4**：budget 校验显式 `math.IsNaN(v) \|\| math.IsInf(v, 0)` → 拒绝；新增测试 `TestAdminTenantBudget_NaN_Returns2`（FR-1"拒绝非数值"意图内，非范围扩张） |
| N2 | `Sscanf("0x10","%d")` → **i=0, err=nil**：今天 `quota t 0x10 0x10` 静默写 0/0 = 配额移除，比"0x10 被接受为 16"更危险 | 全串解析后拒绝；规格示例表已列 `0x10`，设计补记实际语义 |
| N3 | `adminUsage()` 被 `TestCmdAdminUsage_ListsFilesDelete`（cli_admin_test.go:694）pin（子串断言）；`usage()`/`adminUsage()` 文案本次**零改动**（无条目增删） | 无 usage 文案变更（规格 §5 亦未要求） |
| N4 | 搜索 `-k` 尾参潜藏 bug（`cli_test.go:1698-1704` 注释：`["q","-k"]` 时 `-k` 被静默忽略）——非本方向 | 显式出范围（§8），不引入参数个数校验 |
| N5 | 生产文件行数预算：`cli_admin.go` 457 行（+8×2 ≈ 473 ≤ 500 ✓）· `cli_admin_buckets.go` 182（+16 ≈ 198 ✓）· `cli_search.go` 110（+6 ≈ 116 ✓）；`_test.go` 豁免 500 行门（`engineering.yaml` `filesize.ignore_patterns` 含 `_test.go`，`cli.py check-filesize` 实测 PASS） | helper 放**新文件** `cli_validate.go`（~55 行），避免 cli_admin.go 逼近上限 |

---

## 2. 设计总览

```mermaid
flowchart LR
    subgraph CLI["internal/cli（唯一变更面）"]
        A["admin tenants quota/budget · admin buckets quota/lifecycle · search -k\n（cli_admin.go / cli_admin_buckets.go / cli_search.go）"]
        A --> B["校验门禁：cli_validate.go\nrequireNonNegInt64 / requireNonNegFloat / requireNonNegInt\n全串解析 → 非负检查 →（budget）NaN/Inf 拒绝"]
        B -->|失败| C["stderr: invalid <role> \"<值>\": expected …\n+ usage 行 · return 2 · 零 HTTP 请求"]
        B -->|通过| D["既有 body 构造 + c.do —— 字节不变"]
    end
    D --> S["REST 服务端（零改动）\nSetQuota/PutBucketQuota/SetBudget/PutBucketLifecycle/search"]
```

**核心语义（三条）：**

1. **校验即门禁，fail-fast**：五个入口在 `len(args)` 检查之后、body 构造与 `c.do` 之前完成全串解析 + 非负检查（FR-3）。无效输入路径**结构性保证**零网络副作用（校验先于任何 `c.do`）。
2. **接受域严格化、合法域不动**：`strconv.ParseInt(s,10,64)` / `strconv.ParseFloat(s,64)` / `strconv.Atoi` 替换 `Sscanf`（拒绝 `10abc`/`0x10`/`1e3`/`12.5x`/`NaN`/`Inf`/溢出/空白填充，§1.1）；合法十进制串（`1048576`/`12.50`/`+7`/`0`）解析值域与 Sscanf **逐值一致**，既有 body 断言不受影响（AC-3）。
3. **零值语义保留（FR-2）**：`0`/`-0` 继续合法——quota `0/0` = unlimited（`file_crud.go:46-47` 契约）、budget `0` = 清除覆盖（`admin.go:79` 注释）、lifecycle `0`/`-k 0` 语义不变。只拒绝**非数值与负值**，不引入其他取值范围约束。

**关键设计决策（D1–D8）：**

| # | 决策 | 理由 |
|---|------|------|
| D1 | **新文件 `internal/cli/cli_validate.go`**，三个包私有 helper：`requireNonNegInt64(name, s string) (int64, error)` · `requireNonNegFloat(name, s string) (float64, error)` · `requireNonNegInt(name, s string) (int, error)` | 五处共用同一校验语义（含错误消息契约），避免复制；新文件 ~55 行，`cli_admin.go` 保持 ≤500（N5）；不建 `utils/` 类包（AGENTS.md 禁），属 cli 领域内私有 helper |
| D2 | 每个命令在 `len(args)` 检查后插 3-5 行守卫：解析 → 失败打印 `err` + 既有 usage 行 → `return 2` | 对齐 `unknown admin resource` 先例（消息 + usage + 2，`cli_admin.go:55-59`）与既有 too-few-args 路径（`cli_admin.go:279-283`）；`adminBucketLifecycle` 的守卫在 `--action` flag 循环**之前**（更早 fail-fast） |
| D3 | **错误消息契约（FR-1 三要素：角色名 + 实际值 + 期望格式）**：int64/int → `invalid <role> "<值>": expected a non-negative integer`；float → `…: expected a non-negative number`。角色名固定为 `max_bytes` / `max_objects` / `daily_budget_usd` / `days` / `k` | 消息即验收断言面（stderr 含角色名）；单行、静态、无原始响应体回显（对齐 fail-closed 兄弟方向的 F-A 约束） |
| D4 | budget 校验链：`ParseFloat` → `math.IsNaN(v) \|\| math.IsInf(v, 0)` → `v < 0` | N1：ParseFloat 对 `NaN`/`Inf` 返回 err=nil，仅 `< 0` 检查漏网；显式拒绝 + 专测 |
| D5 | `cmdSearch -k` 走 `requireNonNegInt("k", args[i+1])`；`-k 0` 保持合法（服务端 k=0 → 默认 10，search.go:65-66） | FR-1 五入口全覆盖；零值保留（FR-2） |
| D6 | 测试按命令归属分布：tenant quota/budget → `cli_admin_test.go`；bucket quota/lifecycle → `cli_admin_buckets_test.go`；search → `cli_test.go`；**命名强制前缀** `TestAdminTenantQuota*` / `TestAdminTenantBudget*` / `TestAdminBucketQuota*` / `TestAdminBucketLifecycle*`（search 保持 `TestCmdSearch*`） | AC-1 过滤器当前零匹配（规格 §2 警示），前缀契约使过滤器非空；`_test.go` 豁免 500 行门 |
| D7 | **零服务端/REST/仓库/迁移改动**；`usage()`/`adminUsage()` 文案零改动（N3） | 方向明示"CLI is the only defense"，验收仅限 CLI；I2/I4/I6 |
| D8 | 全部新测试复用既有基建：`newTestClient`（cli_test.go:67）、`captureStderr`/`captureStdout`（:28/:48）、`hit` 计数器零请求断言（先例 `TestCmdAdminFiles_Delete_EmptyTenant_Returns2` cli_admin_test.go:599-617）；纯 stdlib（testing/httptest），无网络依赖、无并行 | 与既有 117 个测试同构；确定性、可重跑 |

---

## 3. API 变更

### 3.1 对外行为变更（唯一变更面 = CLI 参数校验；REST 面零变更）

| 命令 | 参数 | 现状 | 变更后 |
|------|------|------|--------|
| `admin tenants quota <id> <max_bytes> <max_objects>` | max_bytes/max_objects (int64) | `"abc"`/`"10abc"`/`"0x10"`/`"1e3"`/`"-5"` 静默写入（垃圾→0 = 配额移除，exit 0） | 非数值/负值 → stderr 错误 + usage + **exit 2**，零请求 |
| `admin tenants budget <id> <daily_budget_usd>` | daily_budget_usd (float64) | 垃圾→0 清除覆盖（exit 0）；`NaN`/`Inf` → 空 body → 服务端 400（exit 1）；负值 → 服务端 400（exit 1） | 非数值/NaN/Inf/负值 → **exit 2**，零请求（fail-fast，服务端 400 路径不再可达于 CLI） |
| `admin buckets quota <b> <max_bytes> <max_objects>` | 同 tenant quota | 同左 | 同左（exit 2） |
| `admin buckets lifecycle <b> <days>` | days (int) | `"abc"`→0、`"-1"` 直写库（exit 0） | 非数值/负值 → **exit 2**，零请求 |
| `search <q> -k <n>` | k (int) | `-k abc` → k=0 仍发请求（服务端回默认 10） | 非数值/负值 → **exit 2**，零请求 |

### 3.2 新增内部 API（`internal/cli/cli_validate.go`，包私有）

```go
// 全串严格解析 + 非负门禁。错误消息含角色名/实际值/期望格式（FR-1 三要素）。
func requireNonNegInt64(name, s string) (int64, error) // strconv.ParseInt(s, 10, 64) + v < 0
func requireNonNegFloat(name, s string) (float64, error) // strconv.ParseFloat(s, 64) + IsNaN/IsInf + v < 0
func requireNonNegInt(name, s string) (int, error)       // strconv.Atoi + v < 0
```

调用方形状（每命令 4 行，`adminTenantQuota` 例）：

```go
maxBytes, err := requireNonNegInt64("max_bytes", args[1])
if err != nil {
    fmt.Fprintln(os.Stderr, err)
    fmt.Fprintln(os.Stderr, "usage: admin tenants quota <id> <max_bytes> <max_objects>")
    return 2
}
```

### 3.3 显式无变更清单

REST 路由/handler（`api/rest/admin.go` `SetQuota`/`SetBudget`/`PutBucketLifecycle`/`PutBucketQuota`）· `service`（`SetBucketQuota`/`SetBucketLifecycleFull`/配额预检）· `repository` · 迁移（I2）· 中间件链（I4）· `go.mod`（I6）· `usage()`/`adminUsage()` 文案 · 退出码语义（0/1/2）· CLI 命令签名与请求字节（AC-3 锁定）。

---

## 4. 兼容性约束

| # | 约束 | 依据 |
|---|------|------|
| C1 | **零值全程合法**：`quota t 0 0`、`budget t 0`、`lifecycle b 0`、`search q -k 0` 照旧发请求；`-0`/`-0.0` 解析为 0 亦合法 | FR-2；服务端契约 `file_crud.go:46-47`（0 = unlimited）、`admin.go:79`（0 = clear override）、`search.go:65-66`（k=0 → 10） |
| C2 | **合法输入请求字节不变**：ParseInt/ParseFloat 与 Sscanf 在合法十进制域逐值一致（`12.50`→12.5、`+7`、`1048576`）→ `json.Marshal(map[string]any{…})` 输出与现状字节相同 | AC-3 三组既有精确 body 断言原样通过 |
| C3 | **无 wire 变更、双向二进制兼容**：新 CLI + 旧服务端 ✓（校验纯客户端）；旧 CLI + 新服务端 ✓（服务端零改动） | 3.3 无变更清单 |
| C4 | **退出码契约不变**：0=成功 / 1=HTTP/传输 / 2=参数错误。唯一变化：无效值从"静默 0 成功"改为 exit 2；budget 负值从服务端 400（exit 1）改为 CLI exit 2——**无既有测试/脚本依赖旧路径**（核验：cli 测试中无负值/垃圾输入用例） | 既有退出码先例 `cli_admin.go:279-283` 等 |
| C5 | **usage 文案不变**（`usage()`/`adminUsage()`），`TestCmdAdminUsage_ListsFilesDelete`（cli_admin_test.go:694）继续通过 | N3 |
| C6 | **有意收紧清单**（今天接受、实现后被拒，exit 2）：`abc` · `10abc` · `0x10` · `1e3` · `12.5x` · ` 42` · `NaN`/`Inf` · 溢出 · 负值 | 方向核心意图；§1.1 实证 |

---

## 5. 失败模式

| # | 模式 | 行为 | 测试 |
|---|------|------|------|
| F1 | 非数值/尾垃圾/基数前缀/科学计数/溢出/空白填充（int64/int 全串解析失败） | exit 2 · 零请求 · stderr 含角色名+值+期望格式 | `TestAdminTenantQuota_NonNumeric/TrailingGarbage_Returns2` 等 7 行 |
| F2 | 负值（`-5`/`-1.5`/`-1`/`-3`） | 同上（`< 0` 显式检查） | `_Negative_Returns2` 4 行 |
| F3 | budget `NaN`/`Inf`/`-Inf`（ParseFloat err=nil 漏网） | 同上；改善现状（今天：marshal 失败→空 body→服务端 400 exit 1） | `TestAdminTenantBudget_NaN_Returns2` |
| F4 | AC-1 过滤器真空通过（命名不守约 → `-run` 匹配 0 个测试） | 强制前缀契约（D6）；矩阵 14 个测试命中过滤器，真空不可能 | 验收阶段重跑过滤器命令 |
| F5 | 合法输入 body 漂移（回归） | 请求方法/路径/body 被精确断言锁死 | AC-3：3 既有 + 2 新增 valid + 2 zero |
| F6 | 测试基建脆弱（网络/并行/时序） | 全部 httptest 本地服务器 + capture 管道，无并行、无真实网络 | 与既有 117 测试同构（D8） |
| F7 | 出范围残留（有意不处理，防 scope 膨胀） | `-k` 尾参静默忽略（cli_test.go:1698 已文档化）· 多余参数个数 · `k=0`/days 上限/预算上限 · jobs/audit `--limit` · 服务端校验 | 规格 §5 边界表原样承接（§8） |

---

## 6. 迁移步骤

| # | 步骤 | 说明 |
|---|------|------|
| M1 | **零数据/零 schema/零配置迁移** | 无迁移文件（I2）；无新依赖（I6）；无环境变量 |
| M2 | 随主二进制发布（CLI 与 server 同二进制 `cmd/server/main.go`） | 校验纯客户端：新 CLI + 旧 server 兼容（C3），发布顺序自由 |
| M3 | 行为收紧对运维的影响 = 唯一"迁移面"：自动化脚本若曾传垃圾/负值参数，将从"静默成功（破坏配置）"变为 exit 2 | 属方向核心意图（破坏面消灭）；无回滚需求——回滚 = 换回旧二进制，零状态残留 |
| M4 | 验证：`make check`（gofmt/build/vet/test/filesize）· `go test ./internal/cli/ -run 'TestAdminTenantQuota\|TestAdminTenantBudget\|TestAdminBucketQuota\|TestAdminBucketLifecycle'` · 全仓 `go test ./...` | §7 验收映射 |

---

## 7. 验收映射（AC-1…AC-3 → 可执行测试）

### 7.1 AC-1：`go test ./internal/cli/ -run 'TestAdminTenantQuota|TestAdminTenantBudget|TestAdminBucketQuota|TestAdminBucketLifecycle'` passes

新增 **14 个测试命中该过滤器**（现为 0 个，规格 §2 警示）。矩阵（全部复用 `newTestClient` + `hit` 计数器 + `captureStderr`/`captureStdout`；拒绝行断言：`code==2` · `hit==0` · stderr 含角色名与 `usage:`）：

| 测试（前缀即过滤器匹配项） | 输入 | 文件 |
|------|------|------|
| `TestAdminTenantQuota_NonNumeric_Returns2` | `["acme","abc","xyz"]` | cli_admin_test.go |
| `TestAdminTenantQuota_Negative_Returns2` | `["acme","-5","10"]`、`["acme","10","-5"]`（表驱动两行） | 同上 |
| `TestAdminTenantQuota_TrailingGarbage_Returns2` | `["acme","10abc","5"]`（全串解析，§1.1） | 同上 |
| `TestAdminTenantQuota_ZeroAllowed` | `["acme","0","0"]` → 发请求，body `max_bytes=0,max_objects=0` | 同上 |
| `TestAdminTenantBudget_NonNumeric_Returns2` | `["acme","abc"]` | 同上 |
| `TestAdminTenantBudget_Negative_Returns2` | `["acme","-1.5"]` | 同上 |
| `TestAdminTenantBudget_NaN_Returns2` | `["acme","NaN"]`（N1/D4 新增行） | 同上 |
| `TestAdminTenantBudget_ZeroAllowed` | `["acme","0"]` → 发请求，body `daily_budget_usd=0` | 同上 |
| `TestAdminBucketQuota_NonNumeric_Returns2` | `["b","abc","xyz"]` | cli_admin_buckets_test.go |
| `TestAdminBucketQuota_Negative_Returns2` | `["b","-5","10"]` | 同上 |
| `TestAdminBucketQuota_Valid_Body` | `["b","1048576","1000"]` → `PUT /v1/admin/buckets/b/quota`，body `max_bytes=1048576,max_objects=1000` | 同上 |
| `TestAdminBucketLifecycle_NonNumeric_Returns2` | `["b","abc"]` | 同上 |
| `TestAdminBucketLifecycle_Negative_Returns2` | `["b","-1"]` | 同上 |
| `TestAdminBucketLifecycle_Valid_Body` | `["b","30","--action","hard_delete"]` → `PUT /v1/buckets/b/lifecycle`，body `days=30,action=hard_delete` | 同上 |

### 7.2 AC-2：非数值/负数 → exit 2 + usage 错误 + 零 HTTP 请求

与 7.1 拒绝行为同一组测试（每行同时满足 AC-1 与 AC-2）：`hit==0` 复用 `TestCmdAdminFiles_Delete_EmptyTenant_Returns2`（cli_admin_test.go:599-617）的计数器模式；stderr 断言 `strings.Contains(out, "<角色名>")`（D3 消息契约）+ `strings.Contains(out, "usage:")`。search 两行（不在 AC-1 过滤器内，属 FR-1 覆盖）：`TestCmdSearch_NonNumericK_Returns2`（`["q","-k","abc"]`）· `TestCmdSearch_NegativeK_Returns2`（`["q","-k","-3"]`），放 cli_test.go，断言同构。

### 7.3 AC-3：既有有效输入测试保持精确 body 断言（原样不动）

| 测试 | 断言（现状，**零改动**） | 位置 |
|------|------------------------|------|
| `TestCmdAdminTenants_Quota_PUTsBody` | body `max_bytes=1048576`、`max_objects=1000`，`PUT /v1/admin/tenants/acme/quota` | cli_test.go:1388 |
| `TestCmdAdminTenants_Budget_PUTsBody` | body `daily_budget_usd=12.5`，`PUT /v1/admin/tenants/acme/budget` | cli_test.go:1430 |
| `TestCmdSearch_CustomKAndMode` / `TestCmdSearch_DefaultsKAndMode` | body `k=5` / `k=10`（默认不变） | cli_test.go:801/:772 |
| **新增** `TestAdminBucketQuota_Valid_Body` / `TestAdminBucketLifecycle_Valid_Body`（7.1） | 补正控制：bucket quota/lifecycle 现无任何测试 | cli_admin_buckets_test.go |
| **新增** `TestAdminTenantQuota_ZeroAllowed` / `TestAdminTenantBudget_ZeroAllowed`（7.1） | `0` 仍发请求（FR-2 零值保留） | cli_admin_test.go |

**验收执行命令（实现后由 acceptance 阶段原样运行）：**

```bash
go test ./internal/cli/ -run 'TestAdminTenantQuota|TestAdminTenantBudget|TestAdminBucketQuota|TestAdminBucketLifecycle'
go test ./internal/cli/
go test ./...        # + gofmt -l / go build / go vet（make check 四项）
```

---

## 8. 范围边界（承接规格 §5，设计不越界、不收缩）

| 不做 | 理由 |
|------|------|
| 服务端（service/rest/repository）数值校验 | 方向明示"CLI is the only defense"；验收仅限 CLI（exit 2 零请求） |
| `-k` 尾参静默忽略、多余参数个数校验 | `cli_test.go:1698-1704` 已文档化；规格 §5 明示不做 |
| `k=0` 拒绝、days 上限、预算上限等取值范围 | FR-2 零值语义由服务端契约定义 |
| `adminJobsList --limit` / `cmdAdminAudit --limit` / `adminKeysAdd` 等未引证参数 | 非破坏性配置写入；不在方向五处引证内 |
| usage 文案/退出码约定变更 | 沿用既有约定（C4/C5） |

---

## 9. 实施净改动清单（实现阶段逐一核销）

**生产代码（3 改 1 增，全部 ≤ 500 行/≤ 50 行）：**

| 文件 | 改动 |
|------|------|
| `internal/cli/cli_validate.go`（新，~55 行） | `requireNonNegInt64` / `requireNonNegFloat`（含 NaN/Inf）/ `requireNonNegInt` |
| `internal/cli/cli_admin.go`（457→~473） | `adminTenantQuota` 双参守卫（:284-285 替换）+ `adminTenantBudget` 守卫（:308 替换） |
| `internal/cli/cli_admin_buckets.go`（182→~198） | `adminBucketLifecycle` 守卫（:37，flag 循环前）+ `adminBucketQuota` 双参守卫（:147-148） |
| `internal/cli/cli_search.go`（110→~116） | `cmdSearch -k` 守卫（:23） |

**测试（16 新增，全部 stdlib + httptest + 既有基建）：** §7 矩阵 16 行（14 过滤器命中 + 2 search）；既有 117 测试零改动。

**门禁验证：** `make check`（gofmt / build / vet / test / filesize）· AC-1 过滤器命令 · `python cli.py check-filesize`。

---

## 10. 先前尝试处置（docs/auto/runs/ 复核：本方向无前次设计，逐条 disposition 相关运行）

| 来源 | 发现 | 处置 |
|------|------|------|
| 本运行 `DECISIONS.md`（requirements PASS） | ① 服务端**有** budget 负值检查（方向表述不精确）；② 仅查 Sscanf err 不够（须全串解析）；③ AC-1 过滤器现零匹配，新测试须用四前缀；④ AC-3 需补 bucket quota/lifecycle valid-body 测试 | 全部吸收为设计事实：§1 补充核验表 · §1.1/§2 全串解析（D3）· D6 前缀契约 · 7.3 两新增 valid 测试。**无未处置项** |
| 兄弟运行 `add-admin-files-delete-cli-surface-…f5da52a4`（design_gate PASS；implement 因 900s 超时失败——基础设施失败，非设计缺陷） | test_plan_reviewer：验收断言必须可照字面实现（其 AC-4 e2e 曾不可实现）；断言用行为化而非字节 golden；CLI 退出码 2 + 零请求模式获 authz_contract_reviewer 验证 | 本设计全部验收均为**单元级 httptest、可照字面运行**（§7 每条给出输入/断言/文件）；拒绝行为复用已验证的 exit-2 零请求模式（D8）；无 e2e/信号式断言。**无未处置项** |
| 兄弟运行 `fail-closed-permission-…f876a87f`（design_gate PASS；implement 因 provider 失败——非设计缺陷） | cli_ux_reviewer F-A：stderr 不得回显原始 HTTP/HTML dump（≤512B 单行渲染约束）；testing_reviewer：断言须确定性、与顺序无关、无网络 | 本设计错误消息为**静态单行**（D3），从不回显响应体；测试全本地 httptest、无并行（D8）。**无未处置项** |
| 兄弟运行 `authorizationprovider-…`（cli 面） | `renderError`/`readSuccessfulResponse` 三档渲染契约 | 本设计不触碰响应渲染路径（3.3 无变更清单）；拒绝路径不经过 `renderError`（校验先于请求，无响应可渲染） |

**结论：** 规格 FR-1…FR-3 与 AC-1…AC-3 全部映射为可执行测试；服务端/迁移/中间件零改动；唯一行为收紧 = 垃圾/负值参数 exit 2 零请求（方向核心）；零值语义、合法输入字节、退出码契约、usage 文案全部不变。
