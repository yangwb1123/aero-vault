`make check` passes successfully. Let me now provide the implementation report.

---

# 代码实现报告

## 实现概述

根据 `high-value-extensions-v21.out.arch.md` 和 `high-value-extensions-v21.out.impl-plan.md` 中的架构设计和实现计划，实现了 **Sprint 1** 中的 P0 关键任务（保留 GC 修复、Legal Hold、非当前版本生命周期、Upload GC）。

## 文件清单

### 新文件
| 文件 | 说明 |
|------|------|
| `internal/repository/sql_legal_holds.go` | Legal Hold 的存储层实现（CRUD + 查询） |
| `internal/repository/migrations/sqlite/0025_legal_holds.up.sql` | SQLite 迁移：legal_holds 表 |
| `internal/repository/migrations/sqlite/0025_legal_holds.down.sql` | SQLite 回滚 |
| `internal/repository/migrations/postgres/0025_legal_holds.up.sql` | Postgres 迁移：legal_holds 表 |
| `internal/repository/migrations/postgres/0025_legal_holds.down.sql` | Postgres 回滚 |
| `internal/repository/migrations/sqlite/0026_version_tombstone.up.sql` | SQLite 迁移：version_tombstone 列 |
| `internal/repository/migrations/sqlite/0026_version_tombstone.down.sql` | SQLite 回滚 |
| `internal/repository/migrations/postgres/0026_version_tombstone.up.sql` | Postgres 迁移：version_tombstone 列 |
| `internal/repository/migrations/postgres/0026_version_tombstone.down.sql` | Postgres 回滚 |
| `internal/repository/migrations/sqlite/0027_noncurrent_version_lifecycle.up.sql` | SQLite 迁移：noncurrent_days/count 列 |
| `internal/repository/migrations/sqlite/0027_noncurrent_version_lifecycle.down.sql` | SQLite 回滚 |
| `internal/repository/migrations/postgres/0027_noncurrent_version_lifecycle.up.sql` | Postgres 迁移：noncurrent_days/count 列 |
| `internal/repository/migrations/postgres/0027_noncurrent_version_lifecycle.down.sql` | Postgres 回滚 |
| `internal/reconcile/upload_gc.go` | Upload GC 后台清扫模块 |
| `internal/api/rest/legal_hold.go` | REST 层 Legal Hold API 处理器 |

### 修改文件
| 文件 | 修改说明 |
|------|---------|
| `internal/repository/repository.go` | 添加 `LegalHold` 类型、`Object.VersionTombstone`、Legal Hold 方法、Upload GC 方法、NoncurrentDays/Count 字段 |
| `internal/repository/sql_buckets.go` | `ListSoftDeletedBefore` 排除 version_tombstone=1；`ListExpiredNonCurrentVersions` 新方法；`GetBucketConfig` 读取新列；`SetBucketNoncurrentVersionLifecycle` 新方法 |
| `internal/repository/sql_objects.go` | `HardDeleteObject` 增加 legal hold 子查询检查；`HardDeleteObjectByID` 新方法；`InsertObjectVersion` 设置 version_tombstone=1；所有 SELECT 包含 version_tombstone 列 |
| `internal/repository/sql_helpers.go` | `scanObject` 读取 version_tombstone 列 |
| `internal/repository/sql_uploads.go` | `ListExpiredUploads`、`DeleteUploadCascade`、`ListZombieUploads` 新方法 |
| `internal/storage/storage.go` | 添加 `CleanupParts` 到 Storage 接口 |
| `internal/storage/local_multipart.go` | `CleanupParts` 本地实现 |
| `internal/storage/s3.go` | `CleanupParts` S3 实现（调用 AbortMultipartUpload） |
| `internal/storage/oss.go` | `CleanupParts` OSS 实现 |
| `internal/storage/cos.go` | `CleanupParts` COS 实现 |
| `internal/storage/circuitbreaker.go` | `CleanupParts` 透传实现 |
| `internal/storage/storage_test.go` | `nonRewrapStore` 添加 `CleanupParts` |
| `internal/service/file_features.go` | 添加 `PutLegalHold`、`GetLegalHold`、`RemoveLegalHold`、`ListLegalHolds` |
| `internal/service/file_crud.go` | `hardDeleteObject` 增加 legal_holds 表检查 |
| `internal/reconcile/lifecycle.go` | 添加 `sweepNonCurrentVersions` 方法 |
| `internal/api/rest/router.go` | 注册 legal hold 路由 |
| `internal/config/config_app.go` | 添加 `UploadGCHours`/`UploadGCEnable` 配置字段 |
| `internal/config/config.go` | 加载 Upload GC 配置 |
| `cmd/server/main.go` | 启动 Upload GC 后台任务 |

## 核心代码实现

### TASK-100: 保留 GC 版本控制修复

**修复 `ListSoftDeletedBefore`** — 现在排除 `version_tombstone = 1` 的行（由 `InsertObjectVersion` 创建的版本墓碑），防止保留 GC 误删历史版本：

```go
// sql_buckets.go
WHERE deleted_at IS NOT NULL AND deleted_at < $1 AND version_tombstone = 0
```

在 `InsertObjectVersion` 中，软删除旧行时设置 `version_tombstone = 1`：

```go
// sql_objects.go
UPDATE objects SET deleted_at=$1, version_tombstone=1 WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL
```

### TASK-101 + TASK-102: Legal Hold

**数据模型：**
```go
type LegalHold struct {
    ObjectID   int64  
    TenantID   string 
    VersionID  string // empty = applies to all versions
    HoldReason string
    CreatedBy  string
    CreatedAt  string
}
```

**Repository 方法：** `PutLegalHold`、`GetLegalHold`、`ListLegalHolds`、`RemoveLegalHold`、`ObjectHasLegalHold`、`ListObjectsOnLegalHold`

### TASK-103 + TASK-104: FileService + REST API

**FileService 方法：** `PutLegalHold`、`GetLegalHold`、`RemoveLegalHold`、`ListLegalHolds`

**REST 端点：**
- `PUT /v1/legal-hold` — 放置 legal hold（JSON body: `{"key":"...", "reason":"...", "version_id":"..."}`）
- `GET /v1/legal-hold?key=...&versionId=...` — 查询 legal hold
- `DELETE /v1/legal-hold?key=...&versionId=...` — 移除 legal hold

### TASK-105 + TASK-106: 非当前版本生命周期

**BucketConfig 新增字段：** `NoncurrentDays`、`NoncurrentCount`

**新的 Repository 方法：** `ListExpiredNonCurrentVersions` — 查询 version_tombstone=1 且过了 noncurrent_days 阈值的行（排除 legal hold）

**LifecycleJob 新增方法：** `sweepNonCurrentVersions` — 永久删除 expired 非当前版本

### TASK-020 ~ TASK-027: Upload GC

**Storage 接口新增：** `CleanupParts(ctx, key, uploadID string) error` — 所有后端实现（local/S3/OSS/COS/circuitbreaker）

**Repository 新增方法：**
- `ListExpiredUploads` — 按创建时间查询过期上传
- `DeleteUploadCascade` — 事务中级联删除 parts + upload 记录
- `ListZombieUploads` — 检测有 parts 但从未 complete 的上传

**新模块 `reconcile/upload_gc.go`：** 遵循 `LifecycleJob` 模式（interval + cluster singleton），扫描过期和僵尸上传并清理。

**配置项：** `UPLOAD_GC_TTL_HOURS`（默认 168 = 7 天）

### TASK-107: Legal Hold 覆盖删除防护

`HardDeleteObject` 和 `HardDeleteObjectByID` 现在使用子查询检查 legal_holds 表：
```sql
DELETE FROM objects WHERE id=$1
  AND id NOT IN (SELECT object_id FROM legal_holds WHERE legal_holds.object_id = $1)
```
当 legal hold 活跃时返回 `ErrLegalHoldActive`。

## 关键设计决策

1. **`version_tombstone` 列替代 `version_id` 检查**：最初尝试用 `AND (version_id IS NULL OR version_id = '')` 过滤，但 `UpsertObject` 给所有对象设置 version_id。改用显式的 `version_tombstone` 布尔列，由 `InsertObjectVersion` 在创建新版本时设置。

2. **`CleanupParts` 作为独立 Storage 方法**：不是复用 `AbortMultipart`，因为 upload GC 需要幂等清理已不存在的上传（S3 返回 404 时静默忽略）。独立方法语义更清晰。

3. **Upload GC 遵循 `LifecycleJob` 模式**：复用现有的 `cluster.Singleton` 门控、interval ticker 模式，保持代码风格一致。

## 依赖说明

无新增外部依赖。所有实现使用标准库和已有内部包。

## 验证步骤

1. `go build ./...` — 编译通过
2. `go test ./... -count=1` — 所有测试通过（~6.5 分钟）
3. `make check` — 通过（gofmt、build、vet、test、行数检查）

## 配置要求

新增环境变量：
- `UPLOAD_GC_TTL_HOURS` — 上传过期时间（小时），默认 168（7 天）。>0 时启用 Upload GC 后台任务。UPLOAD_GC_TTL_HOURS 可在 `.env` 中设置。
