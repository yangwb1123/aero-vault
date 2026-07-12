已保存为 `docs/requirements/expansion-v122-service-transaction-integrity-bucket-notifications-db-asymmetry-streaming-memory-credential-lifecycle.md`（1028 行）。

## 代码审查验证总结

对 5 个方向的全部代码锚点进行了 `grep` + 逐行阅读验证：

### 准确度总评

| 方向 | 核心论点 | 代码锚点准确率 | 修正项数 |
|------|---------|--------------|---------|
| **方向一：双写事务完整性** | ✅ 完全准确 | 5/5 | 0 |
| **方向二：桶通知运行时缺口** | ✅ 完全准确 | 5/5 | 0 |
| **方向三：DB 驱动不对称** | ✅ 论点成立 | 3/6 锚点需修正 | **3**（`pg_try_advisory_lock` 不存在、`ReapStuckJobs` 不含 `SKIP LOCKED`、`CountJobsByStatus` 不使用 `json_extract`） |
| **方向四：流式内存压力** | ✅ 论点成立 | 4/5 锚点准确 | **1**（REST handler 无 CopyObject 端点） |
| **方向五：凭据生命周期** | ✅ 论点成立 | 4/6 锚点需修正 | **2**（`expires_at` 已检查；密钥 CRUD 已有审计） |

### 关键修正点

1. **方向三** — `cluster/singleton.go` 使用 `leases` 表的可移植 SQL（`UPDATE + INSERT ON CONFLICT DO NOTHING`），不存在 `pg_try_advisory_lock`。`SKIP LOCKED` 在 `ClaimJob` 中（`jobs.go:106`），且 SQLite 路径已有降级处理（`jobs.go:115` 注释明确说明）。
2. **方向五** — `auth.go:lookupStore`（L194-204）已经实现了 `ExpiresAt` 的运行时检查，过期时返回 `(Key{}, false)`。密钥 CRUD 在 `admin.go:140,159` 已有 `audit("key.add")` 和 `audit("key.revoke")`。
3. **方向四** — REST handler 无 CopyObject 端点；S3 compat 的 `copyObject`（`extra.go:39-72`）是流式传输（`Get` 返回的 reader 直接传给 `Put`），非内存中转。
