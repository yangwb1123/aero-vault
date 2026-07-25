# TASK — 当前状态

> 更新于: 2026-07-24
> 当前 Sprint: `CURRENT_SPRINT.md`

---

## 已完成

### 工程 CLI 体系
- [x] `cli.py` 统一入口（19 个命令）
- [x] `checks/` 包 — 12 模块化门禁，33 pytest 测试
- [x] `engineering.yaml` — 单阈值配置源
- [x] GitHub Actions CI 集成 + Git pre-commit hook
- [x] `python3 cli.py setup` 一键开发环境

### 代码重构（12 个大文件 → 0 豁免）
- [x] `cmd/server/main.go` 893→178 行
- [x] `internal/api/rest/handler.go` 974→339 行
- [x] `internal/api/s3compat/handler.go` 890→371 行
- [x] `internal/repository/sql_objects.go` 592→214 行
- [x] `internal/repository/sql_buckets.go` 504→174 行
- [x] `internal/auth/condition.go` 657→299 行
- [x] `internal/service/file_crud.go` 534→230 行
- [x] `sdk/go/aerovault/client.go` 1006→200 行
- [x] `internal/api/webdav/dav.go` 458→240 行

### 数据完整性修复（expansion-v144）
- [x] 方向一 P1 — AI Chunk 残留（RetentionJob + softDelete + hardDelete）
- [x] 方向二 P1 — Multipart ETag 校验（客户端/服务端交叉验证）
- [x] 方向五 P2 — 并发一致性分析确认

### Bug 修复
- [x] `DeleteBucket` SQL 表名 `uploads`→`multipart_uploads`（运行时 panic）
- [x] `GetLegalHold` / `RemoveLegalHold` SQL 占位符复用（I1 违规）
- [x] `verifyMultipartETags` 长度检查违反 S3 协议

### 覆盖率提升
- [x] `internal/repository`: 47.7% → **56.4%**
- [x] `internal/service`: 49.6% → **51.0%**
- [x] `internal/api/rest`: 49.0% → **51.0%**
- [x] Legal holds 全部函数: 0% → **77-88%**
- [x] Bucket CORS/Logging/Notifications: 0% → **60-100%**
- [x] Admin keys/config: 0% → **67-100%**

### 当前状态

```
Acceptance   ✅ PASSED
覆盖率总计   ✅ 61.7%
文件行数     ✅ 0 豁免
Go 测试      ✅ 全部通过
pytest       ✅ 33/33
```
