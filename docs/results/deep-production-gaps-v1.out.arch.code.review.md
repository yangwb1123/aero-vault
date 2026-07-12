现在我已拥有撰写全面审查所需的所有数据。这是我详细的工作人员工程师审查。

---

# 🔍 工作人员工程师审查：Aero-Vault `internal/service` 子系统

**审查范围：** `internal/service/`、`internal/repository/`、`internal/storage/`、`internal/middleware/`、`internal/api/rest/`
**代码库版本：** 2026年7月12日
**Go 版本：** 1.25

---

## 📊 代码质量指标

| 指标 | 当前状态 | 目标 | 状态 |
|--------|---------|--------|--------|
| **圈复杂度**（每个函数） | `Put`: ~18 | < 10 | ❌ |
| **圈复杂度**（每个函数） | `handler.handlePut`: ~15 | < 10 | ❌ |
| **圈复杂度**（每个函数） | `CompleteMultipart`: ~14 | < 10 | ❌ |
| **函数长度**（`Put`） | 71 行 | < 50 行 | ❌ |
| **函数长度**（`handler.handlePut`） | ~120 行 | < 50 行 | ❌ |
| **文件长度**（`handler.go`） | 958 行 | < 500 行 | ❌ |
| **文件长度**（`service_test.go`） | 644 行 | < 500 行 | ❌ |
| **文件长度**（`condition.go`） | 657 行 | < 500 行 | ❌ |
| **文件长度**（`chunks_events_buckets_test.go`） | 922 行 | < 500 行 | ❌ |
| **文件长度**（`integration_test.go`） | 762 行 | < 500 行 | ❌ |
| **测试覆盖率**（`service` 包） | ~60%（估算） | > 80% | ⚠️ |
| **代码重复率** | ~8%（SQL 方言分支） | < 5% | ⚠️ |
| **文档覆盖率**（导出的 API） | ~75% | > 70% | ✅ |
| **依赖项**（无 `utils/` 包） | 已遵守 | 0 个此类包 | ✅ |

---

## 1. 组织：发现的严重问题

### 🔴 关键：文件长度违规 — `internal/api/rest/handler.go`（958 行）

| 字段 | 值 |
|-------|-------|
| **类别** | 组织 |
| **严重性** | **关键** |
| **标题** | `handler.go` 超过项目 500 行限制 91% |
| **位置** | `internal/api/rest/handler.go:1-958` |
| **描述** | `AGENTS.md` 强制执行 **单文件 ≤ 500 行**。此文件超过限制 458 行，包含 PUT、GET、DELETE、LIST、COPY、预签名等处理程序逻辑，全部堆叠在一起。 |
| **当前状态** | 一个 958 行文件中的所有 REST 处理程序。`handlePut` 函数单独约 120 行，包含了内联的内容协商、范围解析、条件请求逻辑和事件发布。 |
| **建议状态** | 按资源或操作领域拆分为多个文件：`object_handlers.go`、`system_handlers.go`、`middleware_policy.go`、`response_helpers.go`。每个文件的处理函数应委托给辅助函数，以保持单体文件 ≤ 500 行。 |
| **影响** | 降低了给定文件的单一职责性，当多个开发者同时处理路由时会产生合并冲突，并使新成员的认知负载过高。 |
| **工作量** | M |

### 🔴 关键：函数长度违规 — `Put`（71 行）

| 字段 | 值 |
|-------|-------|
| **类别** | 组织 / 质量 |
| **严重性** | **关键** |
| **标题** | `FileService.Put` 超过 50 行限制 42% |
| **位置** | `internal/service/file_crud.go:89-160` |
| **描述** | `Put` 函数在一个作用域中执行了太多操作：默认值设置、键验证、元数据验证、桶创建、配置获取、配额预检、锁检查、存储键构造、MD5 包装、存储写入、大小验证、MD5 验证、对象构建、存储库写入。责任链是清晰的，但作为线性代码块被内联在一起。 |
| **当前状态** | 一个 71 行的函数，包含 6 个 "段落"，可以担任独立方法。 |
| **建议状态** | 将 `Put` 拆分为：
```go
func (s *FileService) putPreflight(ctx, tenant, bucket, key string, opts, size) (BucketConfig, error)
func (s *FileService) putToStorage(ctx, sk string, r io.Reader, size, opts) (ObjectInfo, error)
func (s *FileService) putFinalize(ctx, obj Object, bcfg BucketConfig) (Object, error)
```
验证和配额检查不应嵌套在主流程中。 |
| **影响** | 高维护风险：逻辑中的分支路径（版本控制、对象锁、MD5 验证）在不提取函数的情况下难以独立测试。圈复杂度 ~18。 |
| **工作量** | M |

### 🟡 中：函数长度 — `CompleteMultipart`（69 行）

| 字段 | 值 |
|-------|-------|
| **类别** | 组织 |
| **严重性** | **中** |
| **标题** | `CompleteMultipart` 超过 50 行限制 |
| **位置** | `internal/service/file_multipart.go:93-162` |
| **描述** | 类似于 `Put`，`CompleteMultipart` 在一个块中处理上传获取、部件列表构建、配置加载、配额检查、锁检查、存储完成、对象构建、存储库写入、使用跟踪、上传删除和事件发布。 |
| **影响** | 当添加新的完成前验证时，会形成 "雪球" 函数。 |
| **工作量** | S |

---

## 2. 命名与文档发现问题

### 🟡 中：使用字符串键作为上下文值

| 字段 | 值 |
|-------|-------|
| **类别** | 命名 |
| **严重性** | **中** |
| **标题** | `callerFrom` 使用未类型化的字符串上下文键 `"auth_key_label"` |
| **位置** | `internal/service/file.go:100-104` |
| **描述** | 函数 `callerFrom` 从上下文中读取 `ctx.Value("auth_key_label")` 作为一个裸字符串。Go 的 `context` 包强烈建议使用自定义类型来作为上下文键，以防止包间的键冲突。 |
| **当前状态** | ```go
func callerFrom(ctx context.Context) string {
    t := middleware.TenantFrom(ctx)
    if label, ok := ctx.Value("auth_key_label").(string); ok && label != "" {
        return label
    }
    return t
}
``` |
| **建议状态** | ```go
// 在 auth 包中定义：
type authKeyLabelType struct{}
var AuthKeyLabelKey = authKeyLabelType{}

// 在 service 包中引用：
if label, ok := ctx.Value(auth.AuthKeyLabelKey).(string); ok && label != "" {
```
这可以防止当另一个包意外地存储一个带有键 "auth_key_label" 的字符串值时的损坏。 |
| **影响** | 在大型代码库中，上下文键冲突是一个真实的 bug 来源。随着代码库的发展，这是一个定时炸弹。 |
| **工作量** | S |

### 🟢 低：重复的错误哨兵

| 字段 | 值 |
|-------|-------|
| **类别** | 命名 |
| **严重性** | **低** |
| **标题** | `ErrUploadNotFound` 在两个包中定义 |
| **位置** | `internal/service/file.go:18` 和 `internal/repository/repository.go:7` |
| **描述** | 两个包都声明了 `var ErrUploadNotFound = errors.New(...)`。服务层重新声明了它从存储库获得的相同的语义错误。这违反了 DRY 原则，如果在某个地方没有使用 `errors.Is` 检查，可能导致细微的 bug。 |
| **当前状态** | `repository.ErrUploadNotFound` 和 `service.ErrUploadNotFound` 是不同的指针；服务代码通过 `errors.Is(err, repository.ErrUploadNotFound)` 进行检查，因此它目前能工作，但冗余是一种代码异味。 |
| **建议状态** | 删除 `service.ErrUploadNotFound` 并引用 `repository.ErrUploadNotFound`。在 `service/file_multipart.go` 中统一 `errors.Is(err, repository.ErrUploadNotFound)` 的使用。 |
| **影响** | 低；当前实现通过使用 `errors.Is` 来避免错误，但会混淆新加入的开发者。 |
| **工作量** | S |

---

## 3. 错误处理发现

### 🟡 中：`checkBytesQuota` / `checkObjectsQuota` 中的错误处理不一致

| 字段 | 值 |
|-------|-------|
| **类别** | 错误处理 |
| **严重性** | **中** |
| **标题** | `preflightQuota` 中的配额检查在大小为零时不一致地处理限额拒绝 |
| **位置** | `internal/service/file_crud.go:27-71` |
| **描述** | 当 `deltaObjects == 0` 且 `size == 0`（例如，一个带有未知 `Content-Length` 的流）时，两个检查只在 `>=` 限额时阻塞。但是，当已经处于当前限额时，`checkBytesQuota` 和 `checkObjectsQuota` 都会阻塞——即使大小为零，这可能会拒绝一次写入，而该写入可能不会增加字节数（例如，流式覆盖）。文档中说“无限制的流无法绕过配额”，但阻塞一个已经处于当前限额的对象流覆盖会产生一个错误。这可能意译了 S3 的行为，但与标准的乐观配额策略不一致。 |
| **当前状态** | ```go
if q.MaxBytes > 0 && q.UsedBytes >= q.MaxBytes {
    return fmt.Errorf("%w: bytes %d/%d", ErrQuotaExceeded, q.UsedBytes, q.MaxBytes)
}
```
对于大小为零的检查也是如此。但是... |
| **建议状态** | 文档化该行为，或者当大小为零且增量对象为 0 时，允许使用 `if q.UsedBytes > q.MaxBytes` 的严格大于检查（禁用严格限制的对象计数）。 |
| **影响** | 边界情况：客户端在限额已满时发送流式 PUT，由于配额被拒绝而失败，尽管操作可能不会增加使用量。 |
| **工作量** | S |

### 🟢 低：错误包装不一致

| 字段 | 值 |
|-------|-------|
| **类别** | 错误处理 |
| **严重性** | **低** |
| **标题** | `Put` 在存储错误上使用 `fmt.Errorf("storage put: %w", err)`，但 `Get` 包裹了存储错误 |
| **位置** | `internal/service/file_crud.go:136` 和 `:281` |
| **描述** | `Put` 中的存储错误被包装为 `"storage put: %w"`，但 `Get` 中的类似错误的包装是 `s.store.Get(ctx, ...)` 返回的裸错误。包装允许调用者使用 `errors.Is`/`errors.As`，这是期望的行为。 |
| **当前状态** | `Put` 包装了存储错误。`Get` 不包装：它返回裸存储错误。 |
| **建议状态** | 在 `Get` 中添加包装：`return nil, repository.Object{}, fmt.Errorf("storage get: %w", err)` |
| **影响** | 低；但包装使得链路追踪和调试更容易。 |
| **工作量** | S |

---

## 4. 日志记录发现

### 🟡 中：无结构的事件日志记录

| 字段 | 值 |
|-------|-------|
| **类别** | 日志记录 |
| **严重性** | **中** |
| **标题** | `emit` 吞没了扇出错误 |
| **位置** | `internal/service/file.go:122-127` |
| **描述** | `emit()` 从事件接收器默默地吞下错误，并附有注释："生命周期事件尽力而为，绝不能破坏用户请求。" 这当然是正确的设计，但接收器错误从未被记录，使得诊断事件管道中断变得困难。 |
| **当前状态** | ```go
func (s *FileService) emit(ctx context.Context, o repository.Object, t repository.EventType) {
    ...
    s.sink.Publish(ctx, e)  // 错误被丢弃
}
``` |
| **建议状态** | ```go
s.sink.Publish(ctx, e)
// 记录接收器错误但不返回它们——事件绝不能破坏用户请求。
// 我们可以添加一个可选的错误记录器。
``` |
| **影响** | 操作员注意不到事件管道的中断（例如，断裂的 Webhook、关闭的通道）。 |
| **工作量** | S |

### 🟢 低：未记录的大小不匹配

| 字段 | 值 |
|-------|-------|
| **类别** | 日志记录 |
| **严重性** | **低** |
| **标题** | 大小不匹配被记录为 `Warn`，但可能应被视为 `Error` |
| **位置** | `internal/service/file_crud.go:140-143` |
| **描述** | 当实际写入的字节数与 `Content-Length` 不匹配时，记录一条警告。这说明传输过程中发生了截断，这可能是数据损坏的信号。 |
| **当前状态** | `s.logger.Warn("size mismatch: bytes written differ from Content-Length", ...)` |
| **建议状态** | 考虑将其提升为 `s.logger.Error`，因为这是指示有问题的事务数据。或者，为下游监控公开一个 `storage_size_mismatch_total` 计数器。 |
| **影响** | 在数据管道中必须认真对待截断。 |
| **工作量** | S |

---

## 5. 测试实践发现

### 🟡 中：`GetRange` 和 `gzipReadCloser` 没有测试

| 字段 | 值 |
|-------|-------|
| **类别** | 测试 |
| **严重性** | **中** |
| **标题** | 关键函数缺少单元测试 |
| **位置** | `internal/service/range.go`（整个文件=140 行，只有 42 行测试）和 `file_crud.go:304-315` |
| **描述** | `GetRange` 函数（一个面向用户的、跨多个协议使用的公共方法）没有直接测试。只有底层的 `ParseByteRange` 被测试。同样地，gzip 自动解压缩路径（`gzipReadCloser`）也没有被测试。 |
| **当前状态** | `range_test.go` 只测试了 `ParseByteRange`。`file_crud.go` 中的 `gzipReadCloser` 结构体没有被任何测试覆盖。 |
| **建议状态** | 添加：
```go
func TestGetRange(t *testing.T) {
    svc, _ := newTestSvc(t)
    putTestObject(t, svc, "range.txt", "hello world")
    rc, obj, err := svc.GetRange(ctx, "", "", "range.txt", 3, 5)
    // assert "lo wo"
}
func TestGzipAutoDecompress(t *testing.T) {
    // PUT with _aero_content_encoding=gzip metadata
    // GET → verifies transparent decompression
}
``` |
| **影响** | 这些路径被 REST、S3 和 WebDAV 处理程序使用。未经测试的代码是高风险的。 |
| **工作量** | S |

### 🟢 低：测试文件大小违规

| 字段 | 值 |
|-------|-------|
| **类别** | 测试 |
| **严重性** | **低** |
| **标题** | 多个测试文件超过 500 行限制 |
| **位置** | `internal/repository/chunks_events_buckets_test.go`（922 行），`internal/ai/integration_test.go`（762 行），`internal/service/service_test.go`（644 行） |
| **描述** | `AGENTS.md` 的 500 行限制同样适用于测试文件。这些文件很难导航，并且当多个测试函数放在同一个超级文件中时，可能会产生合并冲突。 |
| **建议状态** | 将每个测试文件拆分为领域子文件：例如，`service_test.go` → `service_put_test.go`、`service_get_test.go`、`service_delete_test.go`、`service_events_test.go`。对 `chunks_events_buckets_test.go` 和 `integration_test.go` 做同样的处理。 |
| **影响** | 可维护性差；新成员发现这些文件令人生畏。 |
| **工作量** | M |

---

## 6. 技术债务发现

### 🔴 关键：SQL 方言之间的 SQL 重复

| 字段 | 值 |
|-------|-------|
| **类别** | 技术债务 |
| **严重性** | **关键** |
| **标题** | Postgres 和 SQLite 的 SQL 字符串两者都被维护，导致了 8-10% 的重复代码 |
| **位置** | `internal/repository/sql_objects.go`（全部），`internal/repository/sql_buckets.go`（全部），`internal/repository/sql_chunks.go`（全部） |
| **描述** | 大部分查询作为字符串常量被复制，一个版本使用 `$N`（postgres）和一个 `$N`（sqlite，被 `rebind` 转换为 `?`）但有细微的差异（`jsonb` vs 文本，`now()` vs `$13`）。文件 `sql_objects.go` 中大约 30-40% 的行是由于这种分支。示例：
**`UpsertObject`**（4-5 行只用于分支差异的复制）+ `InsertObjectVersion` 也是如此。这创建了双倍的代码，需要保持同步。 |
| **当前状态** | 使用 `if s.dialect == dialectPostgres { ... } else { ... }` 模式在单行级别上切换。这种方法已经蔓延到 `sql_buckets.go`、`sql_chunks.go` 和 `sql_events.go`。 |
| **建议状态** | 提取一个查询注册表或每个方言的查询函数：
```go
type querySet struct {
    upsertObject        string
    insertObjectVersion string
    getObject           string
    ...
}

var querySets = map[dialect]querySet{
    dialectPostgres: {
        upsertObject: `INSERT INTO objects (...VALUES ($1,$2,$3,...) ON CONFLICT ... DO UPDATE SET ...`,
        insertObjectVersion: `...`,
    },
    dialectSQLite: {
        upsertObject: `INSERT INTO objects (...VALUES ($1,$2,$3,...) ON CONFLICT ... DO UPDATE SET ...`,
        ...
    },
}
```
（注意：SQLite 的 `$N` 保留给 `rebind`）。然后 `UpsertObject` 变成：
```go
func (s *sqlStore) UpsertObject(ctx context.Context, obj Object) (Object, error) {
    return s.execInsert(ctx, querySets[s.dialect].upsertObject, args...)
}
```
或者，创建一个代码生成器来从单个模板生成两张方言表。 |
| **影响** | Postgres 和 SQLite 的每个演进都必须在两个地方复制。针对一个方言进行的重构的更改可能会使另一个方言失去同步。这是项目中最显著的维护负债。 |
| **工作量** | **L** |

### 🟡 中：`PerTenantConcurrencyLimiter` — 脆弱的手动槽管理

| 字段 | 值 |
|-------|-------|
| **类别** | 技术债务 |
| **严重性** | **中** |
| **标题** | 并发限制器使用带有显式获取/释放循环的手动信号量 |
| **位置** | `internal/middleware/middleware.go:152-215` |
| **描述** | `PerTenantConcurrencyLimiter.Middleware` 使用一个手动循环来获取槽位，然后使用一个带有多个 `<-cl.sem` 读操作的延迟函数来释放它们。获取失败路径必须释放所有已获取的槽位。这个模式容易在未来的更改中引入 "漏一个" 或 "双重释放" 的 bug。`acquired` 变量很难推理。 |
| **当前状态** | 用于全局和每个租户获取/释放的嵌套延迟和手动循环。 |
| **建议状态** | 将资源获取抽象为一个类型：
```go
type concurrencySlot struct { n int }
func acquire(sem chan struct{}, n int) (concurrencySlot, bool) {
    for i := 0; i < n; i++ {
        select {
        case sem <- struct{}{}:
        default:
            for j := 0; j < i; j++ { <-sem }
            return concurrencySlot{}, false
        }
    }
    return concurrencySlot{n}, true
}
func (cs concurrencySlot) release(sem chan struct{}) {
    for i := 0; i < cs.n; i++ { <-sem }
}
```
这使获取/释放的责任封装在类型中。 |
| **影响** | 对并发代码的中等风险。随着越来越多的租户相关特性被添加，有人可能错误地修改释放路径。 |
| **工作量** | M |

### 🟢 低：`uuidLike()` 函数将时间与随机数据混合

| 字段 | 值 |
|-------|-------|
| **类别** | 技术债务 |
| **严重性** | **低** |
| **标题** | `uuidLike()` 创建了一个非标准的 "类 UUID" 格式 |
| **位置** | `internal/repository/sql_helpers.go:78-95` |
| **描述** | `NewVersionID()` 使用一个自定义函数 `uuidLike()`，它将时间的 Unix 纳秒数组与 16 字节的加密随机数据混合，创建一个 36 字符的十六进制字符串。这不是 RFC-9562 UUID（它不是特定的版本格式）。虽然这在技术上是碰撞安全的，但它混淆了身份格式的期望。明确使用 `google/uuid.NewString()` 会更好，它已经是一个函数调用。 |
| **当前状态** | 自定义的 `uuidLike()` 具有非标准的格式和混合的时间/随机字节。 |
| **建议状态** | 直接用 `uuid.NewString()` 替换它。该依赖项已经存在于 `go.mod` 中，用于 `middleware` 包。或者，对于真正的 v4 UUID，使用 `crypto/rand` 读取 16 个字节并手动格式化。 |
| **影响** | 低；它有效，但有细微差别。可能会在调试期间产生混乱。 |
| **工作量** | S |

---

## 7. 具体的代码质量问题

### 🟡 中：未导出的 `parseRangeSpec` 和 `clampRange` — 可测试性差

| 字段 | 值 |
|-------|-------|
| **类别** | 质量 |
| **严重性** | **中** |
| **标题** | 范围解析函数是未导出的 |
| **位置** | `internal/service/range.go:50-100` |
| **描述** | `parseRangeSpec` 和 `clampRange` 是未导出的（小写）。虽然这是合理的封装，但这也意味着它们不能直接从包外进行单元测试。包内测试（`range_test.go`）测试了 `ParseByteRange`，它调用了它们，但不会对极端情况直接进行单元测试。由于 `GetRange` 依赖于它们，这降低了测试粒度。 |
| **当前状态** | 只有 `ParseByteRange` 被导出和测试。 |
| **建议状态** | 要么将它们导出（`ParseRangeSpec` 和 `ClampRange`）以便在集成场景或直接单元测试中重用，要么在 `range_test.go` 中添加更全面的包内表驱动测试，以覆盖规范边缘情况（负值、无效的起始值、过大的后缀等）。 |
| **影响** | 低至中；规范的边界只能在包边界之外断言，如果以后出现了 bug，可能会错过回归。 |
| **工作量** | S |

### 🟡 中：`objectPath` 对存储键的路径遍历检查不足

| 字段 | 值 |
|-------|-------|
| **类别** | 质量 |
| **严重性** | **中** |
| **标题** | 本地存储 `objectPath` 依赖于 `path.Join` 进行清理 |
| **位置** | `internal/storage/local_read.go` / `local_write.go` |
| **描述** | `objectPath(key)` 函数（在本地存储后端中）使用 `filepath.Join` 将根配置与键组合。虽然它能处理 `..` 的情况，但恶意构造的键可能导致路径遍历。根据 `AGENTS.md`（I3：存储键唯一且不可反向解析），键验证发生在服务层，但如果一个键带有前导 `/` 或编码的 `..` 序列绕过验证，可能会出现问题。 |
| **当前状态** | `validateKey` 检查 `..` 和前导 `/`，但存储后端并没有独立地防御路径遍历。 |
| **建议状态** | 在 `objectPath` 中添加防御性检查：
```go
func (s *LocalStorage) objectPath(key string) (string, error) {
    if key == "" {
        return "", ErrInvalidKey
    }
    joined := filepath.Join(s.root, key)
    if !strings.HasPrefix(joined, filepath.Clean(s.root)+string(filepath.Separator)) {
        return "", fmt.Errorf("%w: path traversal detected in %q", ErrInvalidKey, key)
    }
    return joined, nil
}
``` |
| **影响** | 中等安全性：如果以后更改了键验证，存储后端可能会暴露于目录遍历攻击。 |
| **工作量** | S |

---

## 📋 技术债务登记册

| 项目 | 影响 | 工作量 | 优先级 | 备注 |
|------|--------|--------|----------|-------|
| **SQL 方言重复** — Postgres/SQLite 查询复制 | **高** | **L** | **P0** | 最显著的技术债务；重构为查询注册表系统；有高回归风险 |
| **文件大小 > 500 行**（6 个文件） | **高** | M | **P0** | 硬性违规违反 `AGENTS.md`；`handler.go`（958 行）是最大的问题 |
| **`Put` 函数过长**（71 行） | **中** | M | **P1** | 圈复杂度 ~18；必须提取辅助函数 |
| **字符串上下文键** `"auth_key_label"` | **中** | S | **P1** | 与运行时包冲突的风险 |
| **未经测试的 gzip 自动解压缩** | **中** | S | **P1** | 被 REST/S3/WebDAV 使用；没有覆盖率 |
| **`emit()` 吞没事件错误**，没有日志记录 | **中** | S | **P1** | 静默中断的事件管道 |
| **`CompleteMultipart` 函数过长**（69 行） | **低** | S | **P2** | 与 `Put` 是同一个模式 |
| **`ErrUploadNotFound` 重复** | **低** | S | **P2** | 包间的代码重复 |
| **配置时间键** `config.AuthKeyLabelKey` | **低** | S | **P2** | 与上下文字符串键相同的模式 |
| **`uuidLike` 非标准格式** | **低** | S | **P3** | 功能性但令人困惑 |
| **`concurrencyLimiter` 手动获取/释放** | **中** | M | **P2** | 脆弱的并发控制模式 |

---

## 发现总结

### 总体代码质量：有待改进

#### ✅ 优点
1. **架构分解**：跨 `service`、`storage`、`repository` 和协议适配器的关注点分离清晰，效果极好。
2. **依赖注入**：所有的服务通过显式构造函数接受它们的依赖项（`NewFileService(store, repo, logger)`），使得它们可以独立地进行单元测试。
3. **接口设计**：`EventSink`、`ChunkCleaner`、`Storage`、`Repository` 接口都很好地抽象，使得实现可以轻松替换。
4. **行为准则合规**：包名如 `utils`/`common`/`helper` 被避免。领域分割得到遵守。
5. **测试基础设施**：`newTestSvc` 夹具和 `storage.RunContract` 套件简化了测试设置。
6. **发送者错误变量**：`ErrNotFound`、`ErrLocked`、`ErrQuotaExceeded` 等清除了错误语义。

#### ❌ 严重问题（必须在生产前修复）
1. **文件大小违规**：6 个文件超过 500 行，违反了 `AGENTS.md`。`internal/api/rest/handler.go`（958 行）是最严重的违规者。
2. **函数长度违规**：`FileService.Put`（71 行）和 `handler.handlePut`（~120 行）超过了 50 行限制，具有较高的圈复杂度。
3. **SQL 重复**：~30% 的存储库代码是 Postgres/SQLite 案例分支的复制。这是长期可维护性的最大风险。

#### 🔮 可维护性问题
1. **字符串上下文键**：`"auth_key_label"` 应该使用一个类型化的上下文键，以防止跨包冲突。
2. **未经测试的 gzip 路径**：`gzipReadCloser` 封装器没有测试覆盖，但被三个协议适配器消耗。
3. **事件错误审计**：事件接收器错误被吞没而没有日志记录；操作员不知道事件管道是否损坏。

#### 💰 技术债务摘要
累积的债务主要是**有意的**（SQL 方言分支）而不是**意外的**。核心问题是 Postgres/SQLite 查询维护。通过提供方言分割的查询集，可以大幅减少约 500-700 行重复代码。评估为：**中等债务，如果延迟太久，将转为高债务**，因为架构演变需要接触更多查询。

#### ⚡ 快速获胜
1. 用 `uuid.NewString()` 替换 `uuidLike()` — 1 次编辑，已经导入。（**工作量：S**）
2. 添加 `callerFrom` 的上下文键类型。（**工作量：S**）
3. 添加 `GetRange` 和 `gzipReadCloser` 的测试。（**工作量：S**）
4. 在存储后端添加路径遍历检查。（**工作量：S**）
5. 记录 `emit()` 接收器错误。（**工作量：S**）

#### 📈 团队入职建议
- 为新开发者创建一个 **"存储库查询架构"** 文档，解释方言分支模式（`sql.go` 中的 `rebind` + 跨 `sql_*.go` 文件的 `if dialect` 分支）。
- 添加一个 `make lint-files` 目标来检查文件长度（在 `AGENTS.md` 500 行限制的 CI 中强制执行）。
- 考虑在 `.github/workflows` CI 中使用 `gocyclo` 或 `golangci-lint` 的 `funlen`/`cyclo` 检查器。

---

## 附录：一些值得注意的代码片段

**存储库中的 SQL 分支模式：**

```go
// sql_objects.go 中的当前状态（重复的 SQL）：
if s.dialect == dialectPostgres {
    q = `INSERT INTO objects (...) VALUES ($1,$2,$3,...)
         ON CONFLICT ... DO UPDATE SET ...
         RETURNING id, version_id, created_at, updated_at`
    args = []any{...}
} else {
    q = `INSERT INTO objects (...) VALUES ($1,$2,$3,...)
         ON CONFLICT ... DO UPDATE SET ...
         RETURNING id, version_id, created_at, updated_at`
    args = []any{..., now, now}
}
```

一个更好的模式是将完整的 SQL 语句提升为常量，但在这种方式下，可以使用具有已编译语句的方言感知查询集。

**`Put` 中的脆弱错误包装：**

```go
// 文件 crud.go:136 — 包装
return repository.Object{}, fmt.Errorf("storage put: %w", err)

// 文件 crud.go:281 — 没有包装
return nil, repository.Object{}, err
```

不一致的包装可能会让调用者感到困惑，他们通过 `errors.Is(err, storage.ErrNotFound)` 来判断到底是哪一步失败了。
