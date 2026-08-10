# 设计：对象元数据键更新原子化 —— SetObjectMetaKey/SetObjectMetaKeys/DeleteObjectMetaKey 丢失更新竞态与静默 no-op 修复

> **模块：** `internal/repository`（`sql_objects_maint.go`）· **上游：** 本管线 requirements 阶段 PASS（`artifacts/requirements-10762e10/requirements.md`）· **方向（pipeline.yaml 原文）：** "Make object metadata key updates atomic (fix lost-update race and silent no-op)"，范围强制 "Do not expand scope beyond this direction"
> **基线：** HEAD `acfaaf4`；工作区存在其它 campaign 的未提交改动（audit-governance/event-outbox 等），与本方向文件**无交集**——`sql_objects_maint.go` 在 HEAD 与工作区逐字节一致（`git diff HEAD --stat` 空）
> **本设计全部代码引用逐行复核；SQL 语义与红/绿验收在真实驱动上实证**（modernc.org/sqlite v1.50.1 → SQLite 3.53.1，§1/§2/§7）

---

## 0. 设计总览

| 项 | 决策 |
|---|---|
| 方案 | **单语句 in-DB merge + RowsAffected 存在性判定**（方向原文指定的形态："atomic single-statement merge (Postgres `metadata = metadata \|\| $1::jsonb`; SQLite json_patch or a transaction) plus a RowsAffected/return check"）。SQLite `json_patch(metadata,$1)` / `json_remove(metadata,$1)`；Postgres `metadata \|\| $1::jsonb` / `metadata - $1::text`。不选事务（B），存在性判定用 RowsAffected 而非 RETURNING——理由见 §2.2 |
| API 变化 | **零公开面**：`Repository` 接口（`repository_interface.go:41-44`）三方法签名不变；调用方（`file_features.go:305,333`、`scrub.go:95,113`）零改动。新增两个**包内私有** helper：`mergeObjectMetadata`、`jsonPath` |
| 改动文件 | `internal/repository/sql_objects_maint.go`（385 → **397 行实测**，≤500 硬门禁 ✅）· 新测试 `internal/repository/metadata_atomicity_test.go`（192 行）+ `metadata_contract_test.go`（131 行）+ `metadata_jsonpath_test.go`（75 行）· 新 PG 集成测试 `internal/integration/metadata_atomicity_postgres_test.go`（85 行，`//go:build integration`，不进零-Docker 门禁，`vet -tags=integration` 编译已验证）· `Makefile` + `.github/workflows/ci.yml`（race 门禁接线，§6/§7.4） |
| 迁移 | **无**（I2）：schema 不变，仅 DML 形态变化；无迁移文件；无新依赖（I6：`json_patch`/`json_remove` 内置于 modernc 捆绑 SQLite 3.53.1，PG `\|\|`/`-` 为 jsonb 标准算子） |
| 门禁 | `make check` 全绿实证（§7.5）；I1 占位符每语句 `$N` 各一次；不碰中间件链（I4）、存储 key 布局（I3）、opt-in 门禁（I5） |
| 行为差异 | 仅一条登记差异：**损坏 JSON 元数据从"静默覆盖"变为"报错"（fail-loud）**，仅外部 DB 篡改可达（§5 F2） |

**对 requirements 记录的两处修正（如实登记）：**
1. **行数**：requirements 估 "385→~345"，实测 **397**（385→397，净增 12 行）——估算漏掉了保留在范围内的 `ReplaceObjectMetadata`（27 行，E11 单语句 replace 无竞态、零改动）以及 helper 与文档注释的新增行。≤500 门禁仍以 103 行余量通过。
2. **红侧复现概率**：requirements/兄弟管线称红侧 6/6 必败；本机实测 **3/5 失败**（`-race -count=1`）。丢失更新复现是**时序放大型**（兄弟 QA F-3 已指出："timing-amplified"）——并发测试的绿侧是确定性的（修复后 7/7 全绿），红侧作为回归探测器有效但非 100% 必现；确定性契约（ErrNotFound 表）在两侧均通过、作为非概率性锚点。本设计如实登记，不夸大。

---

## 1. 证据复验（requirements 全部引证 + 支撑声明，逐条对照当前工作树复核；requirements 视为不可信声明）

| # | requirements 引证 | 复验结果 |
|---|---|---|
| E1 | `sql_objects_maint.go:268` `SetObjectMetaKey` = SELECT(:270)→`unmarshalKV`(:278)→Go merge(:282)→marshal(:283)→独立 UPDATE(:287-293)，两条非事务语句，无 RowsAffected 检查 | ✅ **精确**（逐行读文件确认） |
| E2 | `sql_objects_maint.go:288` 为 Postgres UPDATE 分支（`$1::jsonb`） | ✅ **精确**（`grep -n` 定位 :288） |
| E3 | `sql_objects_maint.go:297` `SetObjectMetaKeys` 同形态 + `len(meta)==0` 早退(:298-301) | ✅ **精确**（:297-329 逐行一致） |
| E4 | `sql_objects_maint.go:358` `DeleteObjectMetaKey` 同形态 + 空元数据早退(:368-369) | ✅ **精确**（:358-385 逐行一致） |
| E5 | `reconcile/scrub.go:95` 写 `_aero_scrub_status=corrupt` 标记（warn-only）；`:113` 清除路径同样受影响 | ✅ **精确**（:95 `SetObjectMetaKey(...,"corrupt")`；:113 `DeleteObjectMetaKey`） |
| E6 | `file_features.go:305` `PatchMetadata → SetObjectMetaKeys` | ✅ **精确**（:305 即 `return s.repo.SetObjectMetaKeys(...)`；同文件 :333 `DeleteMetadataKey → DeleteObjectMetaKey`） |
| E7 | 补充：`sqlite.go:26` `SetMaxOpenConns(1)` 只串行化语句、不串行化 RMW 窗口 | ✅（注释 "serialize writes to avoid SQLITE_BUSY"） |
| E8 | 补充：`postgres.go:11` 无池限制 | ✅ `openPostgres` 仅 `sql.Open`+`PingContext`，无 `SetMax*` |
| E9 | 补充：`go.mod:27` modernc.org/sqlite v1.50.1 | ✅ 精确；**实测 `sqlite_version()` = 3.53.1**，`json_patch`/`json_remove` 可用 |
| E10 | 补充：`-run 'TestSetObjectMeta\|Test.*MetaKey'` 当前零匹配 | ✅ **实测**："testing: warning: no tests to run"；本设计 7 条新测试名使过滤器非空（§7） |
| E11 | 补充：`scrub_test.go:150` 已钉 `DeleteObjectMetaKey → ErrNotFound`（行消失时 scrub warn + 保留标记） | ✅ **实测**（`internal/reconcile/scrub_test.go:150` 注释 + `:198` 断言；修复后该测试保持绿，§7.5） |
| E12 | 补充：`validateMetadata` 拒绝 `_aero_` 前缀（`file.go:177`）→ 用户不可写服务标记 | ✅ **实测**（`strings.HasPrefix(strings.ToLower(k), "_aero_")` → `ErrInvalidArgs`） |
| E13 | 补充：`objects.metadata` 列形态 | ✅ SQLite 迁移 `TEXT NOT NULL DEFAULT '{}'`；PG `JSONB NOT NULL DEFAULT '{}'::jsonb`；`jsonOrEmpty`(`sql_helpers.go:143-148`) 保证合法写入恒为合法 JSON 对象 |
| E14 | 补充：生产调用者全量清单 | ✅ 仅 4 处：`scrub.go:95,113`、`file_features.go:305,333`；测试调用者 `scrub_test.go`/`admin_files_delete_test.go`/`usage_consistency_test.go`（全部纳入回归矩阵 §7.5） |

**方向问题陈述复核：** "两调用者都读到 v0，第二个 UPDATE 静默覆盖第一个的键"——✅ 成立且**本机红证**（§7.3：`metadata len=15 want 17`，恰缺 k2/k3）。"对象并发删除时 UPDATE 影响 0 行却返回 nil（静默成功），scrub 标记可被丢弃"——✅ 成立（:283-291 `_, err = ExecContext; return err` 无 RowsAffected 检查）。"SQLite 单连接不串行化 RMW 窗口"——✅（E7）。

---

## 2. 方案选择（FR-1 → 单语句 merge + RowsAffected；附实证）

### 2.1 关键 SQL 语义实证（真实 modernc.org/sqlite v1.50.1 → SQLite 3.53.1，探针程序实跑）

| 探针 | 结果 | 含义 |
|---|---|---|
| `UPDATE t SET v=v WHERE id=1` → `RowsAffected` | **1** | **SQLite 按 WHERE 匹配行计数，即使新值等于旧值** ⇒ `0 ⟺ 行缺失`，RowsAffected 存在性判定在 SQLite 上**成立** |
| `UPDATE t SET v='z' WHERE id=99` → `RowsAffected` | 0 | 缺失行 → 0 → `ErrNotFound` ✅ |
| `json_patch('{"a":"1","b":"2"}','{"b":"3","c":"4"}')` | `{"a":"1","b":"3","c":"4"}` | 合并语义：patch 覆盖、未 patch 保留、新键加入——与 Go 侧 merge 结果一致 |
| `json_patch(…,'{"a":null}')` | `{"b":"2"}` | RFC 7396 null 删除——**但 patch 值恒为字符串**（`map[string]string` marshal），`""` 空串为 `{"a":""}`（设置空串，**非删除**，与现状 Go 行为一致） |
| `json_remove` 特殊键：空格/引号/反斜杠/换行/CR/TAB/`\u0001` 控制符/空串/`a.b$c[d]`/Unicode（经 `$."…"` 全转义路径） | 全部精确删除目标键 | `jsonPath` 转义方案正确 |
| `json_remove('{"a":"1"}','$."nope"')` | 原样返回，无错误 | 删缺失键 → nil 契约保留 |
| `json_remove('{}','$."k"')` | `{}`，无错误 | 空元数据删除 → nil 契约保留（早退移除后仍成立） |
| `json_patch('{bad',…)` / `json_remove('{bad',…)` | `SQL logic error: malformed JSON` | 损坏 JSON → **fail-loud 报错**（§5 F2 登记差异） |
| `json_patch('[1,2]',…)` / `json_remove('[1,2]',…)` | 无错误（数组基座被 patch 替换 / remove 原样返回） | 仅外部篡改可达（E13），fail-loud 只覆盖 malformed 形态 |
| `UPDATE … RETURNING id` 0 行匹配 | `sql.ErrNoRows` | RETURNING 方案也可行——但 RowsAffected 更简（§2.2） |

**RowsAffected 方言一致性（Requirements 的"方言陷阱"拒绝结论复核）：** SQLite `sqlite3_changes()` 计匹配行（实测相同值=1）；Postgres `UPDATE` 命令标签计匹配行（`UPDATE n`，n=匹配行数，与值是否变化无关——文档语义，§7.4 AC-1c 在真实 PG 上断言）。两方言一致：**0 ⟺ 行缺失 ⟺ ErrNotFound**。兄弟设计"不采用 RowsAffected"的依据与其自身探针（SQLite=1）自相矛盾；本设计采用 RowsAffected 并以此作为存在性判定的唯一机制。

### 2.2 为什么选 RowsAffected（而非 RETURNING/事务）

| 维度 | RowsAffected（本设计） | RETURNING id（兄弟方案） | 事务（方案 B） |
|---|---|---|---|
| 存在性判定 | `result.RowsAffected()==0 → ErrNotFound`，两方言一致（§2.1 实证） | `Scan(&id)` + `ErrNoRows`，同样可行 | 需 `FOR UPDATE`（PG）/ `BEGIN IMMEDIATE` 手写（SQLite） |
| 代码量 | `mergeObjectMetadata` 无 Scan 分支，更短 | 多一次 Scan | +~60 行 tx 管理 |
| 与方向一致性 | **方向原文明确 "plus a RowsAffected/return check"** | 方向允许 "return check" | 方向允许 "or a transaction" |
| 正确性 | 语句级原子，两方言一致 | 同左 | 依赖池配置/锁序，新增 `SQLITE_BUSY` 面 |
| 实证 | 本设计红/绿双证（§7.3） | 兄弟红/绿双证 | 无 |

**结论：单语句 merge + RowsAffected**——方向指定形态、两方言语义一致、语句级原子（无锁序、无事务面、PG 无池限制也不受影响）。

---

## 3. API 变化与代码设计

### 3.1 公开面

- `Repository` 接口（`repository_interface.go:41-44`）：`SetObjectMetaKey` / `SetObjectMetaKeys` / `DeleteObjectMetaKey` **签名不变**。
- 调用方零改动：`file_features.go:305,333`（PATCH/DELETE 元数据）、`scrub.go:95,113`（corrupt 标记写/清）。
- 新增**私有**：`mergeObjectMetadata`（`sqlStore` 方法）、`jsonPath`（包级函数）。不进接口。

### 3.2 替换实现（`sql_objects_maint.go` 元数据段；已实证编译+gofmt 净+全绿，全文见 §7 引用；此处为最终形态）

```go
// ── Metadata operations ─────────────────────────────────────────────────────

func (s *sqlStore) SetObjectMetaKey(ctx context.Context, tenant, bucket, key, metaKey, metaValue string) error {
	return s.mergeObjectMetadata(ctx, tenant, bucket, key, map[string]string{metaKey: metaValue})
}

func (s *sqlStore) SetObjectMetaKeys(ctx context.Context, tenant, bucket, key string, meta map[string]string) error {
	if len(meta) == 0 {
		return nil // empty patch: zero DB access (object need not exist)
	}
	return s.mergeObjectMetadata(ctx, tenant, bucket, key, meta)
}

// mergeObjectMetadata merges patch into the object's metadata in one atomic
// in-DB statement (json_patch on SQLite, || on Postgres), closing the former
// SELECT→Go-merge→UPDATE lost-update window between concurrent callers.
// Existence is decided by RowsAffected, never a pre-read: both dialects count
// rows matched by WHERE even when the new value equals the old (SQLite
// sqlite3_changes — probe-verified on modernc 3.53.1; Postgres UPDATE command
// tags count matched rows), so 0 ⟺ no live row ⟺ ErrNotFound. That also turns
// the former silent no-op on a concurrently deleted object into ErrNotFound.
func (s *sqlStore) mergeObjectMetadata(ctx context.Context, tenant, bucket, key string, patch map[string]string) error {
	tenant = defaultTenant(tenant)
	merged, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	var result sql.Result
	if s.dialect == dialectPostgres {
		result, err = s.db.ExecContext(ctx, s.rebind(`UPDATE objects SET metadata = metadata || $1::jsonb WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`),
			string(merged), tenant, bucket, key)
	} else {
		result, err = s.db.ExecContext(ctx, s.rebind(`UPDATE objects SET metadata = json_patch(metadata, $1) WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`),
			string(merged), tenant, bucket, key)
	}
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *sqlStore) ReplaceObjectMetadata(ctx context.Context, tenant, bucket, key string, meta map[string]string) error {
	// ⚠ 原样保留（E11：单语句 replace 无 RMW 竞态；其既有 RowsAffected 用法不在本方向范围——见 §8 处置）
	tenant = defaultTenant(tenant)
	updated, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	var result sql.Result
	if s.dialect == dialectPostgres {
		result, err = s.db.ExecContext(ctx, s.rebind(`UPDATE objects SET metadata=$1::jsonb WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`),
			string(updated), tenant, bucket, key)
	} else {
		result, err = s.db.ExecContext(ctx, s.rebind(`UPDATE objects SET metadata=$1 WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`),
			string(updated), tenant, bucket, key)
	}
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *sqlStore) DeleteObjectMetaKey(ctx context.Context, tenant, bucket, key, metaKey string) error {
	tenant = defaultTenant(tenant)
	var result sql.Result
	var err error
	if s.dialect == dialectPostgres {
		result, err = s.db.ExecContext(ctx, s.rebind(`UPDATE objects SET metadata = metadata - $1::text WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`),
			metaKey, tenant, bucket, key)
	} else {
		result, err = s.db.ExecContext(ctx, s.rebind(`UPDATE objects SET metadata = json_remove(metadata, $1) WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`),
			jsonPath(metaKey), tenant, bucket, key)
	}
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// jsonPath returns a SQLite JSON path addressing key as one quoted "$." segment,
// escaping per JSON string syntax (backslash, quote, control chars via \uXXXX;
// UTF-8 bytes pass through) so keys containing arbitrary characters delete
// exactly and only themselves (probe-verified: space/quote/backslash/newline/
// control/empty/dot/bracket/unicode keys).
func jsonPath(key string) string {
	var b strings.Builder
	b.Grow(len(key) + 8)
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
- **存在性判定**：仅 `RowsAffected()==0 → ErrNotFound`（§2.1/2.2 实证两方言一致）。无预读、无 Scan、无 RETURNING。
- **I1 占位符**：每条语句 `$1..$4` 各出现一次，无复用。
- **方言分支**：PG 保留 `::jsonb`（merge）并新增 **`$1::text` 显式钉死**（`metadata - $1::text`，闭锁 `jsonb - text` vs `jsonb - integer` 的驱动默认类型歧义——兄弟 gate 的 Required #5，已接受）；SQLite TEXT 直存（`json_patch`/`json_remove` 的 `$1` 参数为字符串字面量，无类型歧义）。
- **`deleted_at IS NULL`** 条件原样保留（软删对象三方法均 `ErrNotFound`，契约表钉死）。
- **`SetObjectMetaKey`** 委托 `mergeObjectMetadata`（1 键 patch），与现状单键 merge 语义一致。
- **`DeleteObjectMetaKey` 空元数据早退移除**（原 :368-369）：早退需预读 SELECT，与单语句原子性不兼容；其可观察契约（对象存在且元数据为空 → nil）由 `json_remove('{}',…)→'{}'` 单语句保留（§2.1 探针），代价是空元数据删除多一次**同值行写**（SQLite WAL 写入/PG 行写；有界放大，devops F9 处置见 §8）。若加 `WHERE metadata <> '{}'` 守卫会破坏契约（0 行 → 误报 ErrNotFound），故不加。
- **imports**：新增 `fmt`、`strings`（`errors` 保留——文件内其它函数仍用）。
- **行数**：`sql_objects_maint.go` **397 行**（385→397，实测；≤500 ✅）；`mergeObjectMetadata` 32 行、`jsonPath` 24 行、单函数 ≤50 行 ✅。

---

## 4. 兼容性约束（FR-2 逐条锁定；契约表测试钉死，§7.2）

| 现状行为 | 保留方式 | 实证 |
|---|---|---|
| 对象不存在 → `ErrNotFound`（三方法） | 单语句 WHERE 匹配 0 行 → RowsAffected 0 → `ErrNotFound` | §2.1 探针 + 契约表（7 条断言） |
| 软删对象 → `ErrNotFound`（三方法） | WHERE `deleted_at IS NULL` 原样 | 契约表 |
| `SetObjectMetaKeys` 空/nil patch → `nil` 零 DB 访问（对象不存在也 nil） | Go 侧早退原样保留（早于 tenant 归一化） | 契约表 |
| `DeleteObjectMetaKey` 元数据为空 → `nil`（对象存在） | `json_remove('{}',…)` 无错误（早退移除，见 §3.2） | §2.1 探针 + 契约表 |
| 删除缺失键 → `nil` | `json_remove` 原样返回 / PG `-` 无键 no-op | §2.1 探针 + 契约表 |
| 设置相同值 → `nil` | 幂等；RowsAffected=1（匹配行）→ nil | §2.1 探针（P1）+ 契约表 + AC-1c PG 断言 |
| 合并语义（patch 覆盖、其余保留、delete 只删目标键） | `json_patch`/`\|\|` 语义（§2.1） | 合并保真测试 |
| 任意字符键（空格/引号/反斜杠/换行/CR/TAB/控制符/Unicode/空串/路径保留字符） | patch 经 `json.Marshal`（无路径解析）；delete 经 `jsonPath` 全转义 | jsonPath 直测表 + 往返测试 |
| 空串值 = 设置 `""`，**非** RFC 7396 null 删除 | `map[string]string` marshal 恒为字符串值 | 探针 + 断言（QA F-5） |
| 接口签名 / 返回值语义 | 不变（§3.1） | 编译 + 全量回归 |
| 其余路径（`ReplaceObjectMetadata`、读路径、tags/ACL/事件） | 零改动 | 全量测试绿（§7.5） |

**登记的行为差异（唯一）：** 损坏 JSON 元数据（仅外部 DB 篡改可达，E13）——现状 `unmarshalKV` 错误被忽略 → 静默覆盖为合法 JSON；新实现 `json_patch`/`json_remove` 报 `malformed JSON` → PATCH/DELETE 500、scrub `clearCorruptFlag` warn + **标记保留**（fail-closed：对象继续锁定，与 scrub 方向意图一致）。失败操作**不销毁**篡改数据（实测：失败后元数据仍为 `{bad`）。该差异被 `TestSetObjectMetaKeyMalformedStoredJSONFailsLoud` 钉死。

---

## 5. 失败模式

| # | 模式 | 行为 | 处置 |
|---|---|---|---|
| F1 | DB 语句失败（连接断、磁盘满、`SQLITE_BUSY`） | 语句原子：无部分写；错误原样上抛（与现状 UPDATE 分支一致） | 调用方既有错误处理不变（PATCH→500；scrub→warn+保留标记） |
| F2 | **损坏 JSON 元数据**（仅外部篡改可达） | 报 `malformed JSON` → fail-loud（§4 登记差异）；scrub 侧标记保留 = fail-closed | 钉死测试；操作不销毁篡改数据 |
| F3 | 并发写（本方向要消灭的竞态） | 每次写 = 单条原子语句；任何交叠等价于某种串行顺序：**无丢失更新、无复活** | AC-1a/1b/1c 绿证（§7.3） |
| F4 | 并发硬删除（静默 no-op） | RowsAffected 0 → `ErrNotFound`，**不再静默成功** | AC-2b + 契约表 |
| F5 | `jsonPath` 转义错误 → 删错键 | 转义表直测钉死 11 种键形（§2.1 探针全过） | `TestJsonPathEscapes` |
| F6 | scrub 标记清除路径 | `clearCorruptFlag` 的 `DeleteObjectMetaKey` 错误 → warn + 标记保留（下轮重试），**永不误清除**（新路径只能删、不能复活标记；用户侧 `validateMetadata` 拒绝 `_aero_` 前缀，E12） | `TestScrub_ClearFlagFailureKeepsFlag` 回归绿（§7.5） |
| F7 | 键含 JSON 路径保留字符（`.`、`$`、`[`、`]`） | `$."…"` 引号段内字面匹配（探针）；PG `- $1::text` 按字面键名 | 探针 + 往返测试 |
| F8 | 空 patch（`len(meta)==0`） | Go 侧早退，零 DB 访问 | 契约表（含缺失对象 → nil） |

---

## 6. 迁移步骤

1. **无 schema 迁移**（I2）：DML 形态变化，迁移目录零改动；`.down.sql` 无涉及。
2. **无配置/依赖变化**（I6）：`json_patch`/`json_remove` 内置于 modernc 捆绑 SQLite 3.53.1（实证）；PG `\|\|`/`-` 自 9.5 起为 jsonb 标准算子；`$1::text` 仅 SQL 文本。
3. **部署顺序**：单步代码发布；滚动发布期间旧二进制（SELECT+UPDATE）与新二进制（单语句）对同一 schema 均正确读写——旧形态仍原子性缺失但无中间态损坏（最坏回到现状语义），安全。
4. **存量数据**：无数据改写；既有 metadata 均为合法 JSON 对象（E13），首个新代码写操作即按新语义执行。
5. **验证**：§7 验收 + `make check` 全量（§7.5）。

---

## 7. 验收映射（方向 4 条 AC 原样保留 + 测试代码 + 红/绿实证）

### 7.1 测试文件与命名（过滤器非空性 = requirements AC-3）

`internal/repository/` 下三个新测试文件（包 `repository_test` ×2 + 包 `repository` ×1）+ `internal/integration/` 一个 PG 集成测试。**`-run 'TestSetObjectMeta|Test.*MetaKey'` 过滤器实测匹配 6 条测试**（修复后全部 PASS，且 6 条中的每一条都同时被两个过滤分支之一命中；`Test.*MetaKey` 由 `TestSetObjectMetaKey…`/`TestSetObjectMetaKeys…` 命名非空覆盖）；`TestJsonPathEscapes` 为 jsonPath 直测（gate Required #3 要求），按自身名独立运行，其间接覆盖由 `TestSetObjectMetaKeysPreservesUnpatchedKeys` 的怪键往返提供。

| 文件 | 测试 | 对应 |
|---|---|---|
| `metadata_atomicity_test.go`（192 行） | `TestSetObjectMetaKeysConcurrentMerge`（AC-1a：16 键 × 25 迭代，barrier）· `TestSetObjectMetaKeyScrubMarkerSurvivesConcurrentUserWrites`（AC-1b：真实 `_aero_scrub_status` 键 vs 8 用户写者）· `TestSetObjectMetaKeyConcurrentHardDelete`（AC-2b） | 方向 AC 1/2 |
| `metadata_contract_test.go`（131 行） | `TestSetObjectMetaKeysContractTable`（FR-2 契约表：缺失/软删→ErrNotFound ×3、空 patch→nil、空元数据删→nil、删缺失键→nil、同值重设→nil）· `TestSetObjectMetaKeysPreservesUnpatchedKeys`（AC-3 合并保真 + 空串语义 + 9 种怪键往返） | 方向 AC 2 + gate Required #3 |
| `metadata_jsonpath_test.go`（75 行，包 `repository`） | `TestJsonPathEscapes`（jsonPath 转义表 11 例，gate Required #3）· `TestSetObjectMetaKeyMalformedStoredJSONFailsLoud`（fail-loud 钉死 + 不销毁） | gate Required #3 |
| `internal/integration/metadata_atomicity_postgres_test.go`（85 行，`//go:build integration`，`freshRepo` 模式，PG 不可达自动 skip） | `TestSetObjectMetaKeysPostgresConcurrentMerge`（AC-1c：16 键 × 25 迭代 + PG RowsAffected 契约断言：缺失→ErrNotFound、同值→nil、删缺失键→nil） | 方向 AC 1（postgres 方言）+ AC-1c |

种子一律 `UpsertObject`（`sql_objects.go:20`），读回一律 `GetObject`（`sql_objects.go:164`）；复用 HEAD 已提交基建 `openTestRepo`（`buckets_keys_test.go:282`）/`openTestSQLite`（`lifecycle_test.go:11`）/`freshRepo`（`internal/integration/postgres_integration_test.go:38`），零新依赖、零未提交兄弟 helper。

### 7.2 契约表（gate Required #3 / QA F-1 的解决）

`TestSetObjectMetaKeysContractTable` 逐条钉死 §4 表的确定性断言（10 个场景、7 条 ErrNotFound/nil 断言 + 软删 3 条），实现后两侧（旧/新代码）均绿——它是**非概率性**回归锚点（区别于并发测试的时序放大性质）。

### 7.3 红/绿实证（本设计亲自跑过；探针已还原，工作区恢复基线）

| 阶段 | 命令 | 结果 |
|---|---|---|
| **红（未修复 HEAD 代码）** | `metadata_atomicity_test.go` 放入后 `go test ./internal/repository/ -race -count=1 -run 'TestSetObjectMeta\|Test.*MetaKey'` ×5 | **3/5 FAIL**；签名 `metadata_atomicity_test.go:75: metadata len=15 want 17: map[k0:v k1:v k10:v ... seed:0]`——恰为丢失更新（k2/k3 缺失）；scrub 形 1/3 FAIL。**时序放大型**（兄弟 QA F-3 同述；本机 3/5，兄弟机 6/6） |
| **绿（§3.2 修复代码）** | 方向过滤器 ×多轮 + `-run 'TestJsonPath'` | **6/6（方向过滤器）+ 7/7（含 jsonPath 直测）全绿**（含 fail-loud + 契约表 + 3 并发测试），每轮 ~4-8s |
| 回归 | `go test ./internal/repository/ -count=1`（37.9s）· `./internal/reconcile/`（21.6s）· `./internal/service/`（35.7s）· `./internal/api/rest/`（38.5s） | 全绿 |
| 回归矩阵点名 | `TestScrub_ClearFlagFailureKeepsFlag`（scrub_test.go:150 契约）· `TestAdminDeleteFile*` · `TestPutOverwriteAccountsOnlyUsageDelta`/`TestRestoreAccountsQuota`（usage_consistency_test.go）· `TestReplaceObjectMetadataMissingReturnsNotFound` | 全绿 |
| 静态 | `gofmt -l internal/repository/ internal/integration/` 空 · `go vet ./internal/repository/ ./internal/reconcile/` 净 · `go vet -tags=integration ./internal/integration/` 净 · `go build ./internal/repository/` OK | 全净 |
| 行数 | `sql_objects_maint.go` **397** ≤ 500（修正 requirements 的 ~345 估算，§0） | ✅ |
| PG 集成 | `vet -tags=integration` 编译验证（本机无 PG，运行验证走 `make test-integration` / `integration-pg.yml` 的 loud-skip 契约） | 编译 ✅ |

### 7.4 竞态门禁接线（gate Required #4 的解决：Makefile + CI）

`make check` 当前 `fmt vet vet-integration build test cli-check` 跑普通 `go test ./...`，而方向 AC 3 强制 `-race`（兄弟 QA F-3 / DevOps F1 同指）。**解决**：`check` 增加一个**定向 race 步骤**（实测成本：本机 91s 墙钟，repository 90-106s）：

```makefile
test-race-meta:
	@echo "[check] data race detection (metadata atomicity packages) ..."
	go test -race -count=1 -timeout 300s ./internal/repository/ ./internal/reconcile/
	@echo "  OK (no races detected)"

.PHONY: test-race-meta

check: fmt vet vet-integration build test test-race-meta cli-check
```

- 选择**定向**（repository+reconcile，即三方法及其 scrub 调用方所在包）而非全量 `test-race`（`./internal/...`，~5 分钟）：本方向回归定义域就在这两包；全量 `test-race`/`cli.py race` 保持 opt-in。
- `-timeout 300s`：实测 repository race 106s（并行负载下），120s 余量不足。
- CI 镜像（`.github/workflows/ci.yml`，Test 步骤后）：

```yaml
      - name: Test (race — metadata atomicity packages)
        run: go test -race -count=1 -timeout 300s ./internal/repository/ ./internal/reconcile/
```

- 全量 `go test -race -count=1 ./internal/...` 本机实测 **28/28 包全绿**（repository 106s、service 86s），证明定向步骤无隐藏红。

### 7.5 门禁

`make check` 全部构成实证通过（gofmt/vet/vet-integration/build/test/race-meta/cli-check 中的 filesize+vet）；实现阶段在实施后重跑 `make check` + `python3 cli.py check`。

---

## 8. 前次尝试处置（本管线 DECISIONS.md + 兄弟管线全部 design-gate/评审 finding，逐条带证据）

### 8.1 兄弟管线 `make-metadata-read-modify-write-atomic-lost-upda-c1a3b497`（design-gate **FAIL**，4 条 blocking）

| 兄弟 gate blocking finding | 本设计处置（证据） |
|---|---|
| **① F1（principal blocking / distributed F1 High）：** `PutMetadata`/`DeleteMetadata`（`file_features.go:257-275,310-319`）在**服务层**读对象（`objectForAction`→`GetObject`）、Go 合并后整表 `ReplaceObjectMetadata`——PUT 与 scrub 标记写竞态仍丢键；兄弟设计 E11 误判 | **明确拒绝（out of scope）+ 证据**：① 本管线方向定义（pipeline.yaml）问题陈述只点名三方法，修复方案只针对三方法，验收 4 条全部为 repository 层；提示词强制 "Do not expand scope beyond this direction"；② requirements 阶段（本 run 唯一权威）已将其**显式记录为超范围**（"recorded for the design stage's disposition log"）；③ 本方向要消灭的动机场景（scrub `_aero_scrub_status` vs 用户 PATCH）在 **repository 层**被 AC-1b 用真实 `_aero_scrub_status` 键钉死——方向内缺口已闭；④ 修复 F1 需要服务层 CAS/新 repo 方法（接口+SDK 面变化），属独立立项。**登记为 follow-up**（与兄弟 §8.3 tags R1 同列）：乐观 CAS + 有界重试。兄弟 principal 的 AC-4（repo 层 scrub 标记存活）**已由 AC-1b 解决**；AC-5（服务层）随 F1 拒绝 |
| **② Required #3（FR-2 契约表 + jsonPath 转义表测试，QA F-1/F-2）：** 兄弟设计 §7 只有 3 条 AC 测试 | **已解决**：§7.2 契约表（10 场景）+ `TestJsonPathEscapes`（11 例直测）+ 空串语义断言（QA F-5） |
| **③ Required #4（race gate 在 make check，QA F-3 / DevOps F1 High）：** `check` 跑普通 `go test` | **已解决**：§7.4 Makefile + CI 定向 race 接线（实测 91s，28/28 全量 race 绿） |
| **④ Required #5（`metadata - $1::text` 钉死）：** 兄弟 §3.2 未钉类型 | **已接受并落实**：§3.2 PG 分支 `metadata - $1::text`；`$1::jsonb` 保留原样（本就在位） |

### 8.2 兄弟 adversarial reviews 全部 finding（非 blocking 亦逐条处置）

| 评审 | Finding | 处置 |
|---|---|---|
| database_architect | F1 High：PG 无池限制（连接耗尽） | 既有问题，超范围；follow-up（与 devops F5 合并） |
| | F2 Med：`objects_tagged_idx` 与 `objects_bucket_prefix_idx` 重复（SQLite-only） | 既有 schema 问题，超范围；follow-up |
| | F3 Med：SQLite 无 `busy_timeout` | 既有问题，超范围；follow-up——本设计单语句将争用窗口减半（2 语句→1） |
| | F4 Med：metadata/tags 无 JSON-object CHECK | 超范围；follow-up |
| | F5 Low：元数据写不 bump `updated_at` | **行为与现状一致**（旧代码也不 bump）；版本提升按 `updated_at` 排序的既有语义不变；超范围 |
| | F6 Low：5+ 兄弟方法用 RowsAffected | 本设计**已验证** RowsAffected 两方言一致（§2.1），不存在陷阱；其它方法超范围 |
| | F7-F9 Low | 超范围；登记 |
| distributed_engineer | F1 High：服务层 RMW | §8.1 ①（拒绝 + follow-up） |
| | F2 Med：tags 列丢失更新（兄弟 R1） | 同源不同目标，超范围；登记 follow-up：同一单语句 merge 模式（`json_patch(tags,…)`/`tags \|\| $1::jsonb`）可套用 |
| | F3 Med：PG 分支未测 + `$1` 未钉 | **已解决**：`$1::text`（§3.2）+ AC-1c 真实 PG 集成测试（§7.1，覆盖 `\|\|`、`-`、RowsAffected 契约） |
| | F4 Low：fail-loud 无管理员修复路径 | 超范围；登记 follow-up（admin 修复端点）；**严格优于现状**（旧代码静默"修复"后留下误导性 telemetry） |
| | F5 Low：字节级一致 claim 过度（键序不同） | **采纳修正**：本设计不声称字节级一致——`json_patch` 保留基座键序+按 patch 序追加，Go map marshal 为排序序；经 map 读回不可观察，无校验和 |
| | F6 Low：多进程 SQLite 无 busy_timeout | 同 database F3 |
| | F7 Low：无新时间依赖 | 无影响（本设计不写时间戳） |
| qa_lead | F-1 P1：FR-2 契约未钉 | §7.2 契约表 ✅ |
| | F-2 P1：jsonPath 无直测 | §7.1 `TestJsonPathEscapes` ✅ |
| | F-3 P1：验收模式 ≠ 门禁模式 | §7.4 Makefile/CI ✅ |
| | F-4 P1：动机场景（scrub 标记 vs 用户 PATCH）未断言 | §7.1 AC-1b（真实 `_aero_scrub_status` 键，8 用户写者 × 25 迭代）✅ |
| | F-5 P2：空串 ≠ null 删除 | 探针 + 契约测试断言（`empty` 键设为 `""` 后仍在）✅ |
| | F-6 P2：PG 分支登记债 | AC-1c 升级为真实 PG 测试（本 run requirements 明确纳入）✅ |
| | F-7 P2：DB 失败传播 | 行为不变（错误原样上抛，同现状 UPDATE 分支）；登记 |
| devops_engineer | F1 High：race 门禁缺失 | §7.4 ✅ |
| | F2-F3/F10：漏洞扫描、浮动 pin、compose floats | 既有工程债，超范围；follow-up |
| | F4：busy_timeout | 同 database F3 |
| | F5：PG 池 | 同 database F1 |
| | F6：备份 cadence/RPO | 超范围；登记（元数据成为权威 merge 状态） |
| | F7：fail-loud 路径无告警 | 超范围；登记 follow-up（可选 metric/alert） |
| | F8：文档行数不一致（378/368/349） | **采纳**：本设计给出**实测** 397（§0 修正 requirements 估算）与 imports 清单 |
| | F9 Low：空元数据删除多一次同值行写 | **接受的权衡**（§3.2）：`WHERE metadata <> '{}'` 守卫会破坏 ErrNotFound 契约；放大有界（仅空元数据删除路径） |
| principal_reviewer | 1. F1 修复（阻塞） | §8.1 ①（拒绝 + follow-up，证据齐备） |
| | 2. AC-4（repo：scrub 标记存活）+ AC-5（服务：PUT/DELETE racing） | AC-4 ✅（AC-1b）；AC-5 随 F1 拒绝（超范围） |
| | 3. FR-2 契约表 + jsonPath 表 | §7.2 ✅ |
| | 4. race gate | §7.4 ✅ |
| | 5. `::text` pin | §3.2 ✅ |
| | follow-ups（tags R1、PG 池、busy_timeout、重复索引、PG JSON CHECK、admin 修复端点、PG 集成测试"should-complete-with-launch"） | 全部登记；**PG 集成测试已随 AC-1c 完成** |

### 8.3 本管线 requirements 阶段的 disposition 复核（全部接受，无推翻）

| requirements disposition | 复核 |
|---|---|
| F1 超范围（no-expansion mandate） | ✅ 成立（pipeline.yaml 原文 + 4 条 AC 全为 repo 层）；设计 §8.1 ① 拒绝并登记 |
| `metadata - $1` 未钉 finding **接受**（FR-2 钉 `$1::text`） | ✅ §3.2 落实 |
| "RowsAffected 方言陷阱" **拒绝**（探针证据） | ✅ 独立复核成立：SQLite 同值 UPDATE RowsAffected=1（本机实测）；PG 命令标签计匹配行（AC-1c 断言） |
| 新行为差异（损坏 JSON fail-loud）文档化 | ✅ §4/§5 F2 + `TestSetObjectMetaKeyMalformedStoredJSONFailsLoud` 钉死 |
| `scrub_test.go:150` 契约保持绿 | ✅ §7.5 回归实测 |
| AC-1 含 PG 集成测试（freshRepo 模式） | ✅ AC-1c（`internal/integration/metadata_atomicity_postgres_test.go`） |
| AC-2 确定性 ErrNotFound ×3 + 并发硬删除 error ∈ {nil, ErrNotFound} | ✅ §7.1/7.2 |
| AC-3 测试名使两过滤器非空 | ✅ 实测 7 条匹配 |
| AC-4 ≤500 行（估 ~345） | ✅ 实测 397（修正估算，§0） |

### 8.4 其余兄弟管线（scrub / route-antivirus / folder-acl / sql-like-wildcard 等）

与元数据三方法无共享代码路径；其 gate 判决属各自方向的 AC 面（scrub ①②③ 为 scrub 方向自身验收项；AV `PrincipalSystem` 为授权层问题）。本设计回归矩阵点名覆盖 scrub 契约（`TestScrub_ClearFlagFailureKeepsFlag` 绿）；`allowAllAuthz` 跨 campaign 提交顺序隐患——本设计测试零依赖未提交兄弟 helper（全部基建经 `git ls-files`/工作树确认在 HEAD 或本方向自建）。

---

## 9. 范围边界（与方向 no-expansion mandate 一致，不新增）

| 不做 | 理由 |
|---|---|
| `PutMetadata`/`DeleteMetadata` 服务层 RMW（兄弟 F1） | 方向超范围（§8.1 ①）；登记 follow-up（CAS + 有界重试） |
| `ReplaceObjectMetadata` 改造 | 单语句 replace 无 RMW 竞态（E11）；其既有 RowsAffected 用法已实证两方言一致 |
| tags/ACL/事件/审计路径 | 独立方法/列；tags R1 登记 follow-up（同一 merge 模式） |
| `unmarshalKV` 错误处理全局变更 | 本方向只改三方法；fail-loud 差异已登记钉死 |
| 迁移/schema/依赖/接口变更 | I2/I6/§3.1 |
| PG 池限制 / busy_timeout / 重复索引 / JSON CHECK / admin 修复端点 / 告警 / 备份 cadence | 既有工程债，超范围；全部登记 follow-up |
| 全量 `test-race` 接入 check | 定向 race（§7.4）已满足；全量保持 opt-in |
