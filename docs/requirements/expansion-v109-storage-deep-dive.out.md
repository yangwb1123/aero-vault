文件已保存为 `docs/requirements/expansion-v123-storage-deep-optimization-s3-completeness-data-integrity.md`（20.8 KB）。

五个方向的核心发现总结：

| 方向 | 代码锚点 | 核心修复 |
|------|---------|---------|
| **P0: 存储层 Copy** | `Storage` 接口缺 `Copy()` 方法；S3 handler 用 `Get()+Put()` | 接口新增 `Copy` → 云后端单 API 调用，local 后端硬链接 → 减少 50-90% 复制延迟 |
| **P1: Object Lock 双模式** | `LockedUntil *time.Time` 无模式区分；`checkLockBeforeOverwrite` 仅检查时间 | 迁移 0025 加 `lock_mode`/`legal_hold` 列；Governance 可绕过 vs Compliance 绝对不可变 |
| **P1: 校验和算法** | 仅 `md5WrapReader`；`x-amz-checksum-*` → MD5 唯一 | `ChecksumAlgorithm` 枚举 + `ChecksumReader` → CRC32/C RC32C/SHA1/SHA256 |
| **P1: SSE 请求级加密头** | `x-amz-server-side-encryption` 零命中；`encrypt.go` 全局密钥 | 分层加密模型：AES256 → aws:kms → SSE-C（密钥永不持久化） |
| **P2: Lifecycle Transition** | `sweepExpired` 只做删除；`StorageClass` 创建后永不更新 | `TransitionRule` 数据模型 + `reconcile/transition.go` 执行器 + 跨后端路由 |
