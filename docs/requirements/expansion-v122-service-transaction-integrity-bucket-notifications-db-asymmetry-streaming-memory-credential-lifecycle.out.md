所有代码锚点验证通过。以下是独立验证总结报告：

---

## ✅ 独立代码锚点验证报告：expansion-v122

| 锚点 | 文件 | 行号 | 验证结果 |
|------|------|------|---------|
| `store.Put` 成功后 `writePutObject` 失败 | `internal/service/file_crud.go` | 106-119 | ✅ `store.Put` 在 L106，后续 `verifyMD5()` 回滚在 L114，但 `writePutObject` 在 L119 外调用——gap 确认 |
| `hardDeleteObject` 线性执行无回滚 | `internal/service/file_crud.go` | 373-400 | ✅ 五步执行：chunkClean(L379)→store.Delete(L382)→HardDeleteObject(L384)→usage(L387)→emit(L390)，任一失败后不可回滚 |
| `GetRange` 使用 `io.CopyN(io.Discard, offset)` | `internal/service/range.go` | 122-125 | ✅ L123 确认——跳过字节全量读到 discard |
| `Bus.Publish` 不读 `notification_rules` | `internal/events/bus.go` | 67-75 | ✅ `Publish` 调用 `broadcast(e)` L74，无 bucket 规则检查 |
| `broadcast` 无条件发给所有 subscriber | `internal/events/bus.go` | 130-142 | ✅ 遍历 `b.subs` 所有 channel，零过滤 |
| `NotificationRule.TopicARN/LambdaARN` 标记 unused | `internal/repository/repository.go` | 61-63 | ✅ 注释 `unused, kept for compat` |
| `ClaimJob` 两套实现 | `internal/repository/jobs.go` | 106-115 | ✅ Postgres: `FOR UPDATE SKIP LOCKED`；SQLite: 事务+guarded UPDATE |
| `lookupStore` 已检查 `ExpiresAt` | `internal/auth/auth.go` | 194-204 | ✅ L199-204 解析并比较 `now.After(exp)`，过期返回 false |
| MCP 4MB 硬截断 | `internal/mcp/server.go` | 249, 380 | ✅ `io.ReadAll(io.LimitReader(rc, 4<<20))` 两处确认 |
| AccessLog 无 key_label | `internal/middleware/middleware.go` | 84-95 | ✅ 仅记录 method/path/tenant/status/duration，无 key 字段 |
| 迁移 0024 `notification_rules` 列 | `migrations/*/0024_bucket_notifications.up.sql` | — | ✅ 两驱动均有 `ALTER TABLE buckets ADD COLUMN notification_rules TEXT` |
| `PersistedKey.ExpiresAt` 字段 | `internal/auth/store.go` | 19 | ✅ 结构体字段存在 |

### 修正确认

文档末尾的「代码审查修正记录」列出的 6 项修正全部准确——特别是：
- `pg_try_advisory_lock` → ❌ 不存在，Singleton 使用 `leases` 表可移植 SQL ✅
- `expires_at` 从未检查 → ❌ `auth.go:lookupStore` 已检查 ✅
- REST CopyObject 内存中转 → ❌ REST 无 CopyObject 端点；S3 CopyObject 流式传输 ✅

### 文件状态

**已保存：** `docs/requirements/expansion-v122-service-transaction-integrity-bucket-notifications-db-asymmetry-streaming-memory-credential-lifecycle.md`（58,524 字节，1028 行，Jul 11 21:15）

**待完成：** `expansion-v122-service-transaction-integrity-bucket-notifications-db-asymmetry-streaming-memory-credential-lifecycle.out.md` 尚未创建——如需生成摘要输出文件，请告知格式。
