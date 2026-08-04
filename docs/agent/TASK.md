# TASK — 当前状态

> 更新于: 2026-08-03
> 当前 Sprint: `CURRENT_SPRINT.md`

---

## 已完成

### 企业文件平台与公网交付（source.ywbsd.site）
- [x] Snaplink/OIDC 身份接入与 Aero Vault 本地部门、个人、ACL 显式拒绝/允许解耦
- [x] 企业文件 CRUD、版本、配额、WORM/legal hold、分享、博客图片公开引用与导出备份
- [x] REST/S3/WebDAV/MCP、Python/JS/Go SDK 与 Web UI 统一走 FileService 权限边界
- [x] 已知停用租户在 REST、S3/SigV4、WebDAV、HTTP MCP、分享和公开资源路径均被封禁
- [x] REST 预签名 GET/PUT 均回到 Aero Vault；签名绑定方法、租户、对象路径和过期时间
- [x] 已签发 GET 链接可被 tenant suspension、bucket policy、ACL deny、对象删除即时撤销
- [x] `make check` 与 `python3 cli.py accept` 全量通过
- [x] 公网 E2E：GET/HEAD 200、篡改 403、停用全协议 403、恢复 200、临时数据清理 204
- [x] `aero-vault.service` 已部署并健康；`frpc.service` 未修改、未重启
- [x] Python/JS/Go SDK 与 Web UI 已补齐分享、公开图片、ACL、部门/成员的列出和回收操作
- [x] 公网 Python SDK E2E 已验证上传、分享/图片/部门/ACL 全生命周期、tar.gz 备份及自动清理

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
