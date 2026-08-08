# 设计：元数据 read-modify-write 原子化 —— SetObjectMetaKeys/SetObjectMetaKey/DeleteObjectMetaKey 丢失更新竞态

> **模块：** `internal/repository`（`sql_objects_maint.go`）· **上游：** `docs/requirements/metadata-read-modify-write-atomic-v1.spec.md`（本管线 requirements 阶段 PASS）· **基线：** HEAD `acfaaf4`（工作区有未提交的其它 campaign 改动，与本方向无交集——本设计改动文件 `sql_objects_maint.go` 在 HEAD 与工作区一致，`git status` 未列出）
> **本设计所有代码引用均对照当前 HEAD 逐行重新验证**（requirements 证据视为不可信声明，全部复验，见 §1）；**方案 A 的 SQL 语义与验收测试的红/绿两面均已在真实 modernc SQLite 上实证**（§2、§7）。

---

## 0. 设计总览

| 项 | 决策 |
|---|---|
| 方案 | **FR-1 选项 A：SQL 侧单语句 merge**。三方法改为单条 `UPDATE … RETURNING id`，合并/删除在 DB 内原子完成：SQLite `json_patch(metadata,$1)` / `json_remove(metadata,$1)`；Postgres `metadata \|\| $1::jsonb` / `metadata - $1`。**不选 B（事务）**，理由见 §2.2 |
| API 变化 | **零**：`Repository` 接口（`repository_interface.go:41-44`）三方法签名不变；新增**私有** `mergeObjectMetadata` 与 `jsonPath` 两个包内 helper；无新公开 API |
| 改动文件 | `internal/repository/sql_objects_maint.go`（385 → **378 行**，≤500 硬门禁 ✅，净减 7 行）+ 新测试文件 `internal/repository/metadata_atomicity_test.go`（~180 行，`_test.go` 不计入行数门禁） |
| 迁移 | **无**（I2：schema 不变，仅 DML 形态变化；无迁移文件）· 无新依赖（I6：`json_patch`/`json_remove`/`RETURNING` 内置于 modernc 捆绑 SQLite，实证 3.53.1） |
| 门禁 | `make check` 全绿（gofmt/build/vet/test 已实证，§7）；SQL 占位符遵守 I1（每条语句 `$N` 各出现一次）；不碰中间件链（I4）、存储 key 布局（I3）、opt-in 门禁（I5） |
| 语义 | 合并语义与现状 Go 侧 merge **逐字节一致**（实证，§2.1）；错误语义零回归（FR-2 表，§4）；唯一登记的行为差异：**损坏 JSON 元数据从"静默覆盖"变为"报错"**（fail-loud，§5 F2，spec §5 已登记为超范围项，本设计明确锁定） |

---

## 1. 证据复验（requirements 全部引证 + 支撑声明，逐条对照 HEAD 复核）

| # | requirements 引证 | 复验结果（HEAD `acfaaf4`） |
|---|---|---|
| E1 | `sql_objects_maint.go:268` `SetObjectMetaKey` = SELECT(:271)→unmarshal(:275)→Go merge(:278)→marshal(:279)→独立 UPDATE(:283-291)，两条非事务语句 | ✅ **精确**。:268-295 逐行一致 |
| E2 | `sql_objects_maint.go:297` `SetObjectMetaKeys` 同形态 + `len(meta)==0` 早退(:299-301) | ✅ **精确**。:297-329 逐行一致 |
| E3 | `sql_objects_maint.go:358` `DeleteObjectMetaKey` 同形态 + 空元数据早退(:367-368) | ✅ **精确**。:358-385 逐行一致 |
| E4 | `file_features.go:305` `PatchMetadata → SetObjectMetaKeys` 公共 REST 路径 | ✅ **精确**。:305 即 `return s.repo.SetObjectMetaKeys(...)`；同文件 :333 `DeleteMetadataKey → DeleteObjectMetaKey`（`DELETE …/metadata/{key}`） |
| E5 | `sqlite.go:28` `SetMaxOpenConns(1)` 只串行化语句、不串行化 RMW 窗口 | ⚠️ 行号漂移 28→26（`sqlite.go:26` 注释 "serialize writes to avoid SQLITE_BUSY"）；语义成立 |
| E6 | 补充：`scrub.go:95` 标记 / `:113` 清除 `_aero_scrub_status` | ✅ 工作树 `scrub.go:95` `SetObjectMetaKey(…,"corrupt")`、`:113` `DeleteObjectMetaKey(…)`（scrub 兄弟 campaign 的 `clearCorruptFlag` 已合入工作区未提交，:88-119）——两者都是本方向三方法的生产调用者 |
| E7 | 补充：`postgres.go:11` 无池限制 | ✅ `openPostgres` 仅 `sql.Open`+`PingContext`，无任何 `SetMax*`；竞态窗口更宽成立 |
| E8 | 补充：`modernc.org/sqlite v1.50.1` 捆绑 SQLite 3.50.x，`json_patch`/`json_remove` 可用 | ✅ go.mod:27 版本精确；**实证** `sqlite_version()` = **3.53.1**，`json_patch`/`json_remove`/`UPDATE…RETURNING` 全部可用（§2.1 探针） |
| E9 | 补充：`BeginTx`+`FOR UPDATE` 先例 | ✅ `audit_governance_claim.go:41,54`、`audit_governance_cleanup.go:35,52`、`billing_outbox.go:36` |
| E10 | 补充：无 `TestSetObjectMetaKeys*`/`TestConcurrent*` 测试（三条 `-run` 过滤器当前真空） | ✅ 复核：仓库零匹配；`openTestRepo`（`buckets_keys_test.go:282`）、`UpsertObject`（`sql_objects.go:20`）、`GetObject`（`sql_objects.go:164`）、`Object.Metadata` `map[string]string`（`repository.go:25`）均在 HEAD（`git ls-files` 确认） |
| E11 | 补充：`ReplaceObjectMetadata` :331 单语句 | ✅ :304-329 单条 UPDATE（含 `RowsAffected==0 → ErrNotFound`，Postgres 方言下的既有陷阱，见 §5 F3——本方向不动它） |
| E12 | 补充：`objects.metadata` 列形态 | ✅ **新实证**：SQLite 迁移 `0001_init.up.sql:10` `TEXT NOT NULL DEFAULT '{}'`；Postgres `:10` `JSONB NOT NULL DEFAULT '{}'::jsonb`；`jsonOrEmpty`（`sql_helpers.go:143-148`）保证合法写入恒为 JSON 对象 ⇒ 合法路径下 `metadata` 永为合法 JSON 对象，`json_patch` 不会遇到 malformed 输入（§5 F2 仅外部篡改可达） |

**方向问题陈述复核：** "两调用者都读到 v0，第二个 UPDATE 静默覆盖第一个的键"——✅ 成立且**已实证**：按 §7 探针在未修复代码上运行 AC-1/AC-2，`-race` 下 6/6 次失败，失败签名恰为丢失更新（`missing k3 in map[...]`，见 §7.3）。"SQLite 单连接不串行化 RMW 窗口"——✅ 成立（E5）。"Postgres 窗口更宽"——✅ 成立（E7）。

---

## 2. 方案选择（FR-1 二选一 → 选 A，附实证）

### 2.1 方案 A 的 SQL 语义实证（真实 modernc.org/sqlite v1.50.1 → SQLite 3.53.1）

探针程序（`sqlite_version()` + 全部关键语句在真实驱动上执行）结果：

| 探针 | 结果 |
|---|---|
| `json_patch('{"a":"1","b":"2"}','{"b":"3","c":"4"}')` | `{"a":"1","b":"3","c":"4"}` —— 与 Go 侧 merge **逐字节一致**（patch 覆盖、未 patch 保留、新键加入） |
| `json_patch` 含空格/引号/反斜杠/Unicode/点/方括号键 | 全部正确（patch 由 `json.Marshal` 构造，键无需路径转义） |
| `json_patch(…,'{"a":""}')` | `a` 被设为 `""`（**非** JSON-null 删除语义——patch 值恒为字符串，无 null 陷阱） |
| `json_remove('{…}','$."weird key"')` / `$."q\"k"` / `$."a.b$c[d]"` / `$.""` / 含 `\n` 键（`$."a\nb"` 全转义版） | 全部精确删除目标键；**键缺失时无错误**（原样返回） |
| `json_remove` 空对象 / `json_patch` 空对象 | `{}` / patch 结果，无错误 |
| `UPDATE … RETURNING id` 匹配 0 行 | `sql: no rows in result set`，`errors.Is(err, sql.ErrNoRows)==true`（真实建表后验证）⇒ **ErrNotFound 判定安全** |
| `UPDATE` 写回相同值 `RowsAffected` | SQLite = 1（印证 FR-2 方言陷阱：**不采用 RowsAffected**） |
| `json_patch('{bad json',…)` | 错误 `malformed JSON`（FR-2 登记的差异，§5 F2） |
| `json_patch('[1,2]',…')`（数组基座） | 无错误，被 patch 对象替换（合法路径不可达，见 E12） |

### 2.2 为什么选 A 而非 B（事务）

| 维度 | A（单语句 merge） | B（事务） |
|---|---|---|
| 原子性 | 单条 UPDATE = DB 语义原子，两方言一致，**Postgres 无池限制也不受影响**（无需 `FOR UPDATE`） | SQLite 依赖 `SetMaxOpenConns(1)` 的隐式串行化（`BeginTx` 独占唯一连接）——把正确性押在池配置上；Postgres 需 `SELECT…FOR UPDATE` 且两个方言事务起始方式不同（`BEGIN IMMEDIATE` 在 `database/sql` 需手写） |
| 代码量 | 三方法净减 7 行（385→378） | +~60 行（tx 管理 ×2 方言 + 提交/回滚错误路径） |
| 死锁/SQLITE_BUSY 面 | 无新锁序；单语句在 WAL 下为单写事务 | 长事务持锁，新增 `SQLITE_BUSY`/死锁路径 |
| 失败模式 | 简单：语句级原子，失败即无变更 | 需回滚路径；提交失败重试语义 |
| 损坏 JSON 行为 | 报错（fail-loud，§5 F2 登记） | 与现状一致（静默覆盖）——但该输入仅外部篡改可达（E12），且 spec §5 明确登记、不锁死任一侧 |
| 实证 | 本设计已红/绿双证（§7） | 无 |

**结论：A 胜出**——更少的代码、更少的锁面、Postgres 下同样原子、语义差异仅在不可达输入上，且 fail-loud 严格更安全（损坏元数据不再被静默销毁）。

---

## 3. API 变化与代码设计

### 3.1 公开面

- `Repository` 接口（`repository_interface.go:41-44`）：`SetObjectMetaKey` / `SetObjectMetaKeys` / `DeleteObjectMetaKey` **签名不变**，调用方（`file_features.go`、`scrub.go`）**零改动**。
- 新增两个**私有**函数（`sqlStore` 方法 + 包级 helper），不进接口。

### 3.2 替换实现（`sql_objects_maint.go`，已实证编译+全绿）

```go
func (s *sqlStore) SetObjectMetaKey(ctx context.Context, tenant, bucket, key, metaKey, metaValue string) error {
	return s.mergeObjectMetadata(ctx, tenant, bucket, key, map[string]string{metaKey: metaValue})
}

func (s *sqlStore) SetObjectMetaKeys(ctx context.Context, tenant, bucket, key string, meta map[string]string) error {
	if len(meta) == 0 {
		return nil // FR-2: 空 patch 零 DB 访问（对象不存在也返回 nil）
	}
	return s.mergeObjectMetadata(ctx, tenant, bucket, key, meta)
}

// mergeObjectMetadata atomically merges patch into the object's metadata with a
// single in-DB merge (json_patch on SQLite, || on Postgres) instead of the
// former SELECT→Go-merge→UPDATE read-modify-write, closing the lost-update
// window between concurrent callers. Row existence is decided by RETURNING id,
// never RowsAffected (dialects differ on unchanged-row counting).
func (s *sqlStore) mergeObjectMetadata(ctx context.Context, tenant, bucket, key string, patch map[string]string) error {
	tenant = defaultTenant(tenant)
	merged, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	var id int64
	if s.dialect == dialectPostgres {
		err = s.db.QueryRowContext(ctx, s.rebind(`UPDATE objects SET metadata = metadata || $1::jsonb WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL RETURNING id`),
			string(merged), tenant, bucket, key).Scan(&id)
	} else {
		err = s.db.QueryRowContext(ctx, s.rebind(`UPDATE objects SET metadata = json_patch(metadata, $1) WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL RETURNING id`),
			string(merged), tenant, bucket, key).Scan(&id)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (s *sqlStore) DeleteObjectMetaKey(ctx context.Context, tenant, bucket, key, metaKey string) error {
	tenant = defaultTenant(tenant)
	var id int64
	var err error
	if s.dialect == dialectPostgres {
		err = s.db.QueryRowContext(ctx, s.rebind(`UPDATE objects SET metadata = metadata - $1 WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL RETURNING id`),
			metaKey, tenant, bucket, key).Scan(&id)
	} else {
		err = s.db.QueryRowContext(ctx, s.rebind(`UPDATE objects SET metadata = json_remove(metadata, $1) WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL RETURNING id`),
			jsonPath(metaKey), tenant, bucket, key).Scan(&id)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// jsonPath returns a SQLite JSON path addressing key as one quoted segment,
// escaping per JSON string syntax (backslash, quote, control chars; UTF-8
// bytes pass through) so keys containing arbitrary characters delete exactly
// and only themselves.
func jsonPath(key string) string {
	var b strings.Builder
	b.WriteString(`$."`)
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case c == '\\':
			b.WriteString(`\\`)
		case c == '"':
			b.WriteString(`\"`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c < 0x20:
			fmt.Fprintf(&b, `\u%04x`, c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
```

设计要点：
- **存在性判定**：`RETURNING id` + `errors.Is(err, sql.ErrNoRows)`（现代 c 驱动实证；Postgres 原生行为一致）。**不用** `RowsAffected`（FR-2 陷阱，§5 F3）。
- **I1 占位符**：每条语句 `$1..$4` 各出现一次，无复用。
- **方言分支**：保持 `::jsonb` 强转（Postgres）与 TEXT 直存（SQLite）现状。
- **`deleted_at IS NULL`** 条件原样保留（软删对象三方法均返回 `ErrNotFound`）。
- `SetObjectMetaKey` 委托 `mergeObjectMetadata`（1 键 patch），与现状单键 merge 语义完全一致（`defaultTenant` 在 helper 内统一应用）。
- `DeleteObjectMetaKey` 的空元数据早退（原 :367-368）**移除**——它需要预读 SELECT，与原子性不兼容；其**可观察契约**（对象存在且元数据为空 → 返回 nil）由 `json_remove('{}',…)→'{}'` 单语句保留（实证），仅多一次行写（无 `updated_at` 触碰，与原 UPDATE 一致）。
- 行数：385 → **378**（≤500 ✅）；`mergeObjectMetadata` 29 行、`jsonPath` 24 行、其余两方法 ≤3 行，均 ≤50 行/函数（约定）。

---

## 4. 兼容性约束（FR-2 逐条锁定）

| 现状行为 | 保留方式 | 实证 |
|---|---|---|
| 对象不存在 → `ErrNotFound` | `RETURNING id` 无行 → `sql.ErrNoRows` → `ErrNotFound`；软删对象同（WHERE 保留 `deleted_at IS NULL`） | ✅ 探针（真实表，0 行匹配） |
| `SetObjectMetaKeys` 空 patch → `nil` 零 DB 访问 | Go 侧早退原样保留 | ✅ |
| `DeleteObjectMetaKey` 元数据为空 → `nil`（对象存在） | 单语句 `json_remove('{}',…)` → 无错误（早退移除，见 §3.2） | ✅ 探针 |
| 设置相同值 → `nil` | `json_patch` 幂等（写回相同 JSON 无错误） | ✅ 探针 |
| 接口签名 / 返回值语义 | 不变（§3.1） | ✅ |
| 合并语义（patch 覆盖、其余保留、delete 只删目标键） | `json_patch`/`||` 与 Go merge 结果逐字节一致 | ✅ 探针 |
| 任意字符键（空格/引号/反斜杠/换行/Unicode/空串） | patch 经 `json.Marshal`（无路径解析）；delete 经 `jsonPath` 全转义 | ✅ 探针（含 `\n`、`$."` 全转义版） |
| Postgres 分支 `::jsonb` / SQLite TEXT | 原样 | ✅ |
| 其余路径（`ReplaceObjectMetadata`、读路径、tags/ACL/事件） | 零改动 | ✅ 全量测试绿（§7.4） |

---

## 5. 失败模式

| # | 模式 | 行为 | 处置 |
|---|---|---|---|
| F1 | DB 语句失败（连接断、磁盘满、`SQLITE_BUSY`） | 语句原子：无部分写；错误原样上抛（与现状 UPDATE 分支一致） | 调用方既有错误处理不变（`PatchMetadata`→500；scrub→warn+保留标记） |
| F2 | **损坏 JSON 元数据**（仅外部 DB 篡改可达，E12） | 现状：`unmarshalKV` 错误被忽略 → 静默覆盖为合法 JSON。新：`json_patch`/`json_remove` 返回 `malformed JSON` 错误 → PATCH 500、scrub `clearCorruptFlag` warn + **标记保留** | **登记的行为差异**（spec §5 已列、不锁死任一侧——本设计锁定为 fail-loud）。对 scrub：标记保留 = 对象继续锁定 = **fail-closed**，与 scrub 方向意图一致；对用户：损坏数据不再被静默销毁，错误可见可审计。**不为此新增测试**（spec §5） |
| F3 | 方言 `RowsAffected` 不一致（FR-2 陷阱：SQLite 相同值计 1 行，Postgres 行为不同） | **设计上规避**：存在性判定只用 `RETURNING id`，全代码不再读取 `RowsAffected`（三方法内；`ReplaceObjectMetadata` 的既有用法不在本方向范围，E11） | 已规避 |
| F4 | 并发写（本方向要消灭的竞态） | 修复后：每次写 = 单条原子语句；任何交叠都等价于某种串行顺序，**无丢失更新、无复活** | AC-1/AC-2 红/绿双证（§7） |
| F5 | `json_patch` 空对象 / `|| '{}'`（空 patch 理论上不可达——`len(meta)==0` 早退） | 幂等无错误 | 探针 |
| F6 | key 含 JSON 路径保留字符（`.`、`$`、`[`、`]`） | `$."…"` 引号段内字面匹配（探针实证）；Postgres `- $1` 按字面键名 | 探针 |
| F7 | scrub 标记清除路径 | `clearCorruptFlag` 的 `DeleteObjectMetaKey` 错误 → warn + 标记保留（下一轮重试），**永不误清除**（新路径只能删、不能复活标记；用户侧 `validateMetadata` 拒绝 `_aero_` 前缀，`file.go:177`） | 回归矩阵含 `TestScrub_ClearFlagFailureKeepsFlag`（绿，§7.4） |

---

## 6. 迁移步骤

1. **无 schema 迁移**（I2）：DML 形态变化，迁移目录零改动；`.down.sql` 无涉及。
2. **无配置/依赖变化**（I6）：`json_patch`/`json_remove`/`RETURNING` 内置于 modernc 捆绑 SQLite 3.53.1（实证），Postgres `||`/`-` 自 9.5 起为 jsonb 标准算子。
3. **部署顺序**：单步代码发布即可；无多版本兼容问题——旧二进制（SELECT+UPDATE）与新二进制（单语句）对同一 schema 均正确读写，滚动发布期间两形态混跑安全（原子性逐步生效，无中间态）。
4. **存量数据**：无数据改写；既有 metadata 均为合法 JSON 对象（E12），首个新代码写操作即按新语义执行。
5. **验证**：§7 验收 + `make check` 全量。

---

## 7. 验收映射（三条 AC 原样保留 + 测试代码 + 红/绿实证）

测试文件：`internal/repository/metadata_atomicity_test.go`，包 `repository_test`，复用 `openTestRepo`（HEAD 已提交 ✅）；命名与 `-run` 过滤器**前缀精确匹配**（当前仓库过滤器真空——实现后必须非空）。种子一律 `UpsertObject`，读回一律 `GetObject`。

### AC-1 `go test ./internal/repository -run TestConcurrentMetadataMerge -race` 通过

```go
func TestConcurrentMetadataMerge(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	const tenant, bucket, key = "default", "default", "race-merge.txt"
	if _, err := repo.UpsertObject(ctx, repository.Object{
		TenantID: tenant, Bucket: bucket, Key: key,
		Backend: "local", StorageKey: "race-merge.txt", ETag: "e",
		Metadata: map[string]string{"seed": "0"},
	}); err != nil {
		t.Fatal(err)
	}
	const n, iters = 16, 25
	start := make(chan struct{})
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			k := fmt.Sprintf("k%d", i)
			for j := 0; j < iters; j++ {
				if err := repo.SetObjectMetaKeys(ctx, tenant, bucket, key, map[string]string{k: "v"}); err != nil {
					errs <- fmt.Errorf("set %s: %w", k, err)
					return
				}
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	obj, err := repo.GetObject(ctx, tenant, bucket, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(obj.Metadata) != n+1 {
		t.Fatalf("metadata len=%d want %d: %v", len(obj.Metadata), n+1, obj.Metadata)
	}
	for i := 0; i < n; i++ {
		if _, ok := obj.Metadata[fmt.Sprintf("k%d", i)]; !ok {
			t.Fatalf("missing k%d in %v", i, obj.Metadata)
		}
	}
}
```

### AC-2 `go test ./internal/repository -run TestConcurrentMetaDelete -race` 通过

```go
func TestConcurrentMetaDelete(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	const tenant, bucket, key = "default", "default", "race-delete.txt"
	if _, err := repo.UpsertObject(ctx, repository.Object{
		TenantID: tenant, Bucket: bucket, Key: key,
		Backend: "local", StorageKey: "race-delete.txt", ETag: "e",
		Metadata: map[string]string{"victim": "x", "seed": "0"},
	}); err != nil {
		t.Fatal(err)
	}
	const n, iters = 16, 25
	start := make(chan struct{})
	errs := make(chan error, n*2)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			<-start
			k := fmt.Sprintf("k%d", i)
			for j := 0; j < iters; j++ {
				if err := repo.SetObjectMetaKeys(ctx, tenant, bucket, key, map[string]string{k: "v"}); err != nil {
					errs <- fmt.Errorf("set %s: %w", k, err)
					return
				}
			}
		}(i)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iters; j++ {
				if err := repo.DeleteObjectMetaKey(ctx, tenant, bucket, key, "victim"); err != nil {
					errs <- fmt.Errorf("del victim: %w", err)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	obj, err := repo.GetObject(ctx, tenant, bucket, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := obj.Metadata["victim"]; ok {
		t.Fatalf("victim resurrected in %v", obj.Metadata)
	}
	for i := 0; i < n; i++ {
		if _, ok := obj.Metadata[fmt.Sprintf("k%d", i)]; !ok {
			t.Fatalf("missing k%d in %v", i, obj.Metadata)
		}
	}
}
```

### AC-3 `go test ./internal/repository -run TestSetObjectMetaKeys` 保持既有行为

```go
func TestSetObjectMetaKeysPreservesUnpatchedKeys(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	const tenant, bucket, key = "default", "default", "meta-merge.txt"
	if _, err := repo.UpsertObject(ctx, repository.Object{
		TenantID: tenant, Bucket: bucket, Key: key,
		Backend: "local", StorageKey: "meta-merge.txt", ETag: "e",
		Metadata: map[string]string{"a": "1", "b": "2", "weird key": "keep"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetObjectMetaKeys(ctx, tenant, bucket, key, map[string]string{"b": "3", "c": "4"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetObjectMetaKey(ctx, tenant, bucket, key, "a", "9"); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteObjectMetaKey(ctx, tenant, bucket, key, "c"); err != nil {
		t.Fatal(err)
	}
	obj, err := repo.GetObject(ctx, tenant, bucket, key)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"a": "9", "b": "3", "weird key": "keep"}
	if len(obj.Metadata) != len(want) {
		t.Fatalf("metadata=%v want %v", obj.Metadata, want)
	}
	for k, v := range want {
		if obj.Metadata[k] != v {
			t.Fatalf("metadata=%v want %v", obj.Metadata, want)
		}
	}
	if _, ok := obj.Metadata["c"]; ok {
		t.Fatalf("key c not deleted: %v", obj.Metadata)
	}
	// 超出 spec 最低要求的加固：任意字符键的删除路径（锁 FR-1 转义）
	for _, k := range []string{`q"k`, `bs\k`, "nl\nkey", "日本語", ""} {
		if err := repo.SetObjectMetaKey(ctx, tenant, bucket, key, k, "v"); err != nil {
			t.Fatalf("set %q: %v", k, err)
		}
		if err := repo.DeleteObjectMetaKey(ctx, tenant, bucket, key, k); err != nil {
			t.Fatalf("del %q: %v", k, err)
		}
	}
	obj, err = repo.GetObject(ctx, tenant, bucket, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`q"k`, `bs\k`, "nl\nkey", "日本語", ""} {
		if _, ok := obj.Metadata[k]; ok {
			t.Fatalf("key %q not deleted: %v", k, obj.Metadata)
		}
	}
}
```

### 红/绿实证（本设计亲自跑过）

| 阶段 | 命令 | 结果 |
|---|---|---|
| **红（未修复 HEAD 代码）** | 上述 AC-1/AC-2 以临时探针文件放入 `internal/repository`，`-race -count=1` ×6 | **6/6 FAIL**；失败签名 `missing k3 in map[k0:v k1:v … seed:0]`——恰为丢失更新（16 键中缺 1） |
| **绿（§3.2 修复代码）** | AC-1/2/3 `-race -count=1` ×3 | **3/3 全绿**（每轮 ~4s） |
| 回归 | `go test ./internal/repository/ -count=1`（33.8s）· `./internal/reconcile/`（12.7s）· `./internal/service/`（34.1s）· `./internal/api/rest/`（30.4s） | 全绿 |
| 回归矩阵点名 | `TestScrub_ClearFlagFailureKeepsFlag` · `TestAdminDeleteFile*` · `TestPutOverwriteAccountsOnlyUsageDelta`/`TestRestoreAccountsQuota`（usage_consistency_test.go:210 所在文件）· `TestReplaceObjectMetadataMissingReturnsNotFound` | 全绿 |
| 静态 | `go vet ./internal/repository/` · `go build ./...` · `gofmt -l` | 全净 |
| 行数 | `sql_objects_maint.go` 368 ≤ 500 | ✅ |

> 探针已还原（`git checkout` + 删除），工作区恢复基线；实现阶段按本设计落盘。

---

## 8. 前次尝试处置（DECISIONS.md + 兄弟管线 design-gate 判决，逐条）

### 8.1 本管线自身（`make-metadata-read-modify-write-atomic-lost-upda-c1a3b497`）

requirements 阶段 PASS，无 design-gate 前例。其规格中留给设计的**未决项**：

| 未决项 | 处置（证据） |
|---|---|
| FR-1 二选一 | **选 A**（§2，实证表） |
| 损坏 JSON 元数据行为（spec §5 登记"不锁死任一侧"） | **锁定为 fail-loud**（§5 F2）：仅外部篡改可达（E12），错误可见、scrub 侧 fail-closed，无静默数据销毁 |
| `DeleteObjectMetaKey` 空元数据早退（FR-2 表"保留"） | 早退与原子性不兼容（需预读 SELECT）；其可观察契约（→nil）由单语句保留（§3.2），探针实证 |
| RowsAffected 陷阱 | 全设计规避：`RETURNING id` + `ErrNoRows`（实证） |
| AC 命名真空 | 三条测试名前缀精确匹配（§7），实现后过滤器非空 |
| Postgres 集成测试 | spec §5 明确超范围；Postgres 分支按标准 jsonb 算子书写（`||`、`-`、`RETURNING`），正确性由方言语义自证 |
| ≤500 行 | 368 行实证 |

### 8.2 兄弟管线 `scrub-never-clears-…617a37cf`（design-gate **FAIL**，三条 blocking）

| Finding | 处置 |
|---|---|
| ① AC-4 warn 契约未断言（scrub 方向自己的 AC） | **非本方向**：那是 scrub 方向的验收项（`clearCorruptFlag` 的 warn 日志断言）。本方向不动 `scrub.go`；`DeleteObjectMetaKey` 错误路径不变，scrub 的 warn+保留标记语义不受影响。回归矩阵点名 `TestScrub_ClearFlagFailureKeepsFlag` 通过（§7.4） |
| ② still-corrupt 保持锁定未测试（scrub 的 AC-1 缺口） | **非本方向**：属 scrub 方向的验收面。本方向不改 `_aero_scrub_status` 读写守卫；且新实现**只能删、不能复活**该标记（F7），用户的 `_aero_` 写被 `validateMetadata` 拒绝（`file.go:177`）——本方向不会削弱 fail-closed 锁 |
| ③ `allowAllAuthz` 跨 campaign 提交顺序隐患（工作区未提交 helper 被 scrub 测试依赖） | **本方向零依赖**：验收测试只用 HEAD 已提交基建（`openTestRepo`/`UpsertObject`/`GetObject`，`git ls-files` 确认）+ stdlib（`sync`/`testing`/`fmt`）。提交卫生：本方向的 `sql_objects_maint.go` 改动 + `metadata_atomicity_test.go` **必须同提交**，不得依赖任何未提交的兄弟 helper |

### 8.3 兄弟管线 `route-antivirus-…27bd11cc`（design-gate **FAIL** + concurrency R1）

| Finding | 处置 |
|---|---|
| 安全评审 blocking：`PrincipalSystem` 短路被 AV 规则替换会静默丢 indexer/replication 标记 | **与本方向无交集**：授权层（`internal/access`）问题；本方向三方法无鉴权逻辑、不经 `Principal`。超范围 |
| 并发评审 R1：`UpdateObjectTagsByID`（tags 列）并发丢失更新 | **同源不同目标，明确超范围**：分析方向 1 只列元数据三方法（tags 是独立列/方法）。登记为后续候选：同一单语句 merge 模式可直接套用（SQLite `json_patch(tags,…)` / Postgres `tags \|\| $1::jsonb`），本方向落地后作为独立立项 |

### 8.4 兄弟管线其余（`folder-inherited-acls`、`fix-sql-like-wildcard` 等）

与元数据 DML 无共享代码路径；DECISIONS.md 无涉及本方向方法的判决，无需处置。

---

## 9. 范围边界（与 spec §5 一致，不新增）

| 不做 | 理由 |
|---|---|
| `ReplaceObjectMetadata`（:304） | 单语句 replace 无 RMW 竞态（E11）；其 `RowsAffected` 用法为既有方言陷阱，另行立项 |
| tags/ACL/事件/审计路径 | 独立方法/列（R1 已登记为后续） |
| `unmarshalKV` 错误处理全局变更 | 本方向只改三方法；F2 差异已登记锁定 |
| 迁移/schema/依赖/接口变更 | I2/I6/§3.1 |
| Postgres 集成测试 | spec §5 明确超范围 |
| 分析文件方向 2/3（EnqueueJob 去重、租户删除清理） | 独立立项 |
