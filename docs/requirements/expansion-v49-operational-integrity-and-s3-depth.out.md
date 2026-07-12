分析完成。我对整个代码库进行了深入验证。以下是逐方向审核结果，并附有修正和改进的执行建议。

---

## 审核结果：5个方向对照代码库的准确度评估

### ✅ 方向一：Multipart Upload 过期清理 & Orphan Parts GC — **基本正确，有细微修正**

**文档准确的部分：**
- ✅ `DeleteUpload` 确实只 `DELETE FROM multipart_uploads`，未显式清理 parts
- ✅ `AbortMultipart` 中 `s.store.AbortMultipart` 的错误被 `_` 忽略（line 225），存储后端 part 可能残留
- ✅ 没有过期 upload 的 GC 后台任务
- ✅ 没有 `ListStaleUploads` 方法

**需要修正的两个点：**

**① DB 级联删除其实存在：**

```sql
-- 0001_init.up.sql (SQLite)
FOREIGN KEY (upload_id) REFERENCES multipart_uploads(upload_id) ON DELETE CASCADE

-- 0001_init.up.sql (Postgres)
upload_id TEXT NOT NULL REFERENCES multipart_uploads(upload_id) ON DELETE CASCADE,
```

所以 `DELETE FROM multipart_uploads` 会级联删除 parts 行。文档说的 "Part 行残留" 在 **DB 层面不成立**。但 SQLite 需 `PRAGMA foreign_keys = ON`（需确认是否开启）。

**② `ListUploads` 已有 `tenant_id` 和 `storage_key`（迁移 0002、0018 已添加）：**
文档建议的 `ListStaleUploads` 添加 `created_before` 过滤是正确的，但列已经完备。

**真正的缺口：**

| 缺口 | 状态 |
|------|------|
| `DELETE FROM multipart_uploads` 级联删除 parts | ✅ 已存在（有 CASCADE） |
| 存储后端 part 残留（store.AbortMultipart 失败） | ❌ 未处理 |
| 过期 stale upload GC 后台任务 | ❌ 不存在 |
| `AbortMultipart` 忽略 store 错误 | ❌ `_ = s.store.AbortMultipart(...)` |
| `CompleteMultipart` 后未清理 part 数据 | ✅ CASCADE 搞定 |
| `ListUploads` 支持 `created_before` | ❌ 不存在 |

**修正后的工程估算：** ~180 行（原估 250 行，因级联已存在节省 ~20 行，但 DB 查询需要调整）

---

### ✅ 方向二：Object Lock 模式语义 — **完全正确**

代码验证结论：

| 断言 | 代码证据 |
|------|---------|
| ❌ 无 `RetentionMode` 字段 | `Object` struct 只有 `LockedUntil *time.Time`，无 `RetentionMode` |
| ❌ 无 `GOVERNANCE`/`COMPLIANCE` 区分 | `checkLock` 仅做时间比较，所有用户等同处理 |
| ❌ 无 `?legal-hold` S3 端点 | grep 零命中 |
| ❌ 无 `?retention` S3 端点 | grep 零命中 |
| ⚠️ Legal Hold 仅靠元数据 | `_aero_legal_hold` 元数据 key，非专用字段 |
| ⚠️ `GOVERNANCE` 硬编码 | `bucketconfig.go:183` 写死 `Mode: "GOVERNANCE"` |

唯一存在的 Object Lock 相关代码：
- `bucketconfig.go` — `getBucketObjectLock` / `putBucketObjectLock`
- `file_crud.go:160` — 简单时间检查
- `handler.go:93-98` — PUT 时读 `x-amz-object-lock-legal-hold` header 写入 metadata

**当前行为与 S3 规范的差距：**

```
AWS S3 语义                  AeroVault 当前行为
────────────────────────────────────────────────────
GOVERNANCE → 管理员可绕过     ❌ 所有人均不能绕过（等同于 COMPLIANCE）
COMPLIANCE → 任何人不能绕过   ✅ 偶合正确（但语义被 GOVERNANCE 共用）
Legal Hold 独立端点           ❌ 无，仅 metadata key
保留期限可对象级覆盖          ❌ 仅桶级默认值
```

**工程估算：** ~540 行，合理。

---

### ✅ 方向三：跨协议一致性 — **部分正确，部分过度担忧**

**正确的分析：** 写入流程确实存在窗口：

```
步骤 1: s.store.Put(ctx, storageKey, reader)     // S3 HTTP 请求
步骤 2: s.repo.UpsertObject(ctx, obj)             // DB 写入
```

S3 后端下，步骤 1 成功后步骤 2 未完成时，并发读可能 404。

**但需要补充的背景：**

1. **默认配置 (local FS + SQLite) 下无此窗口** — 因为 `store.Put` 到 local FS 在 DB 事务内是同步的，不会返回成功到客户端直到磁盘写入完成。文档提到了这一点，是正确的。

2. **生产配置 (S3 + Postgres) 下此窗口真实存在** — 这是所有 "S3 as backend + external DB" 架构的固有问题，不是 AeroVault 特有的。MinIO、SeaweedFS 等系统也有同样问题。

3. **Postgres 同步复制延迟问题是独立的** — 写入后的读取如果路由到 replica，可能看到旧数据。这是 Postgres 架构问题，不是 AeroVault 代码层面的问题。

**实际工程缺口量级：**

| 建议 | 评估 |
|------|------|
| 一致性模型文档化 | ✅ 有价值，~30 行 |
| `ReadAfterWriteVerify` 可选确认 | ⚠️ 引入延迟 + 复杂性，需评估 ROI |
| 同步提交配置指导 | ✅ 文档化即可 |

**工程估算修正：** ~100 行（主要是文档 + 可选配置项 + 集成测试），非 ~195 行。

---

### ✅ 方向四：事件流交付保障 — **完全正确**

所有断言均已验证：

| 问题 | 行号 | 验证 |
|------|------|------|
| Buffer 64 静默丢弃 | `bus.go:106` | `defaultSubBuffer = 64`，丢弃时 `b.dropped.Add(1)` |
| replay 固定 200 条 | `sse.go:38` | `NextUnconsumedEvents(r.Context(), 200)` |
| 无翻页 | `sse.go:38-49` | 单次查询，无循环 |
| 无 subscriber 健康检测 | `bus.go:58-70` | subs 数组无追踪信息 |
| 无 Graceful Drain | `bus.go:120-127` | Close 直接关闭所有 ch |

**补充发现：** `NextUnconsumedEvents` 的实现（`sql_events.go:36`）查询 `WHERE consumed_at IS NULL`，这意味着：
- 消费后未调用 `MarkEventConsumed` 的事件会反复回放
- SSE 的 `replayMissed` 调用 `NextUnconsumedEvents` 但不标记消费
- 所以重连后会反复看到同一批事件

这是比文档所述更深层的问题：**SSE replay 机制没有消费追踪**，现有的 `consumed_at` 字段虽然存在但 SSE 端点从未调用 `MarkEventConsumed`。

**工程估算修正：** ~300 行（比原估多 35 行，因需修复消费追踪）

---

### ✅ 方向五：S3 批量操作 & 元数据搜索 — **基本正确**

**确认：**
- REST 有 `POST /batch/delete` 和 `POST /batch/tag`（同步）
- S3 无批量操作 API
- 无 `SearchMetadata` / 结构化元数据查询

**补充发现：** 
- `ListObjects` 已支持简单的 prefix/marker 分页
- `StorageClassCounts` 已支持按 storage_class 分组计数
- tags 存储在 `objects.tags` (JSONB/json text)
- metadata 存储在 `objects.metadata` (JSONB/json text)

这意味着**元数据搜索的存储层已就绪**，只需要 Repository 层的查询构建器和 REST/S3 端点。存储层不需要迁移。

**工程估算修正：** 
- 元数据搜索：~250 行（不需要 DB 迁移）
- 批量操作框架：~500 行（不包括 BatchJob 持久化）
- **总计：~750 行**（原估 930 行，因存储层已就绪）

---

## 综合优先级与执行建议（修正版）

```
方向一：Multipart 过期清理          ████████████████████  P1  ~180 行  立即
方向四：事件流交付保障              ████████████████      P2  ~300 行  本周
方向二：Object Lock 模式语义        ████████████          P2  ~540 行  下周
方向三：跨协议一致性 (文档化)       ██████                P2  ~100 行  穿插
方向五：元数据搜索 (前半部分)        ████████              P2  ~250 行  下周
方向五：S3 批量操作 (后半部分)       ████████              P3  ~500 行  下月
```

要求我实现哪些？如果希望我立即开始实现方向一的修复，我可以从以下切入：

1. **修复 `AbortMultipart` 的 store 错误处理**（5 行）
2. **添加 `ListStaleUploads` Repository 方法**（~20 行）
3. **创建 `StaleMultipartCleaner` Reconciler**（~120 行）
4. **在 `main.go` 中注册启动**（5 行）

需要我执行吗？
