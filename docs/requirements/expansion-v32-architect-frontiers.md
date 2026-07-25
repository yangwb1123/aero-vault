# 高价值扩展方向分析 v32 — 架构盲区与平台级能力缺口

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go`、`internal/*` 全部 ~17,000 行 `.go` 代码、`sdk/*` 三套客户端、`deploy/*`、`docs/*`、48 对迁移文件）
> **分析日期：** 2026-07-10
> **视角：** 资深架构师 / 产品经理 — 聚焦「前 31 期分析从未实质触及或仅有表面提及的 5 个架构盲区」
> **去重方法：** 逐篇对比 `docs/requirements/` 下 **31 期既有分析（v1–v31，累计约 21,000+ 行、~165+ 个方向）** + `docs/ROADMAP.md` + `docs/adr/DECISIONS.md` + `docs/CHANGELOG.md` + `docs/TODO.md` + `docs/analysis-*.md`（8 期），每个方向在既有文档中 **零实质性架构分析**。
> **原则：** 不编写任何实现代码。每个方向附带：现状 → 代码锚点 → 缺失能力 → 为什么需要 → 架构概要 → 边界情况。

---

## 审阅：前 31 期覆盖边界（去重矩阵）

前 31 期 expansion 文档覆盖了 **约 165+ 个方向**，核心领域分布：

| 领域 | 已覆盖方向数 |
|------|------------|
| AI/RAG 管线（嵌入/搜索/Chat/Agent/Indexer/Rerank/PII/缓存/预算/模型路由） | ~22 |
| S3 兼容协议（子资源/ACL/Policy/CORS/通知/LegalHold/COPY/Batch/Multipart/SSE-C） | ~16 |
| 存储后端（Local/S3/OSS/COS/KMS/SSE/熔断器/分层/压缩/迁移/CAS/多后端） | ~19 |
| 认证授权（JWT/API Key/SigV4/OIDC/SAML/SCIM/MFA/前缀级策略/FIPS/Policy Engine） | ~16 |
| 多租户（CRUD/配额/预算/审计/日费用/物理隔离/FGA/IaC/Admin Console/Terraform） | ~16 |
| 事件/通知/Webhook（总线/传输/过滤/多通道/死信/背压/CDC/Kafka/Lambda 触发） | ~14 |
| 复制/HA/集群（CRR/SRR/单例/Federation/多活/CQRS/故障转移） | ~13 |
| Reconcile/GC/Lifecycle（孤儿/保留/Scrub/转换/版本/Multipart/非当前版本） | ~13 |
| 合规（WORM/Legal Hold/保留/访问日志/客户端加密/对象锁模式/数据驻留/Geo-Fencing） | ~12 |
| 可观测性（OTel/Metrics/Grafana/Prometheus/SLO/Tracing/pprof/告警/Debug） | ~11 |
| 工程质量（内存安全/流式加密/并发/压缩/错误模型/测试/性能/多协议一致性） | ~12 |
| Web UI / Admin Console / MCP | ~8 |
| SDK / CLI 完整性 | ~6 |
| 基础设施（TLS/ACME/配置热重载/IP ACL/Feature Flag/Helm/CDN/Data Provenance） | ~9 |
| 其他（SQL查询/预测性分层/GitOps/插件/备份/DR/批量操作/Serverless触发） | ~8 |

### 本期 5 个方向在前 31 期分析中均 **零实质性覆盖**（去重依据）

| # | 方向 | 确认方法 | 既有覆盖情况 |
|---|------|---------|------------|
| 1 | **Block-Level Deduplication & Delta Compression（块级去重与增量压缩）** | v7 覆盖了**对象级** CAS（内容寻址存储），但块级去重（对象拆分为块、版本间增量压缩/差分编码）**零分析**；`grep -rli "block.*dedup\|block.*level\|delta.*compress\|delta.*encod\|delta.*diff\|version.*diff" docs/requirements/` → **0 命中** | **差距极大** — 对象级 vs 块级是质的不同 |
| 2 | **Hierarchical Namespace with Atomic Directory Operations（层次化命名空间）** | `grep -rli "hierarchical.*namespace\|directory.*atomic\|atomic.*directory\|rename.*directory\|directory.*rename\|hierarchical.*storage\|prefix.*quota\|directory.*quota\|subtree.*quota" docs/requirements/` → **0 命中** | **完全未覆盖** |
| 3 | **Managed S3 Batch Operations / Bulk Processing Framework（批量操作框架）** | `grep -rli "batch.*operat\|batch.*job\|bulk.*operat\|s3.*batch" docs/` 仅有 matrix 表格中的引用行，**零独立架构分析** | matrix 提及但无分析 |
| 4 | **Client Certificate Authentication / mTLS（客户端证书认证）** | v11 矩阵表中有一行 "no mTLS support"，**零架构分析**；`grep -rli "mtls\|mutual.*tls\|client.*cert\|x509\|certificate.*auth" docs/requirements/` → 仅 2 矩阵引用 | **完全未分析** |
| 5 | **S3 Object Lock with Legal Hold + Complete Governance/Compliance Mode Semantics（完整对象锁实现）** | v30 覆盖了 Object Lock 模式的概念层，但 **未分析完整的 S3 API 语义实现**（Legal Hold 独立标记、Governance/Compliance 执行引擎、保留期延长/缩短规则、DefaultRetention vs 个体覆盖、BypassGovernanceRetention 权限体系） | 概念层覆盖，实现层零分析 |

---

## 本期方向总览

| # | 方向 | 类型 | 优先级 | 代码锚点 | 核心痛点 |
|---|------|------|--------|---------|---------|
| 1 | **🟠 Block-Level Deduplication & Delta Compression（块级去重与增量压缩）** | 成本/架构 | P1 — 版本化桶存储成本失控的核心原因 | `service/file_crud.go:Put`（全量写入）；`storage/storage.go:Storage`（无块级接口）；`repository/sql_objects.go`（storage_key 一对一无共享） | 版本化桶每版本全量存储 → 100 个版本 = 100 倍存储，实际差异可能 < 5% |
| 2 | **🟠 Hierarchical Namespace with Atomic Directory Operations（层次化命名空间）** | 架构/体验 | P1 — 企业数据湖与 NAS 替换场景的前提条件 | `service/file_features.go:List`（prefix 虚拟折叠）；`api/webdav/dav.go`（davDir 手动模拟目录）；`service/file_crud.go:hardDeleteObject`（无递归目录删除） | 没有原子目录操作、目录级 ACL、前缀级配额 → 无法替代企业级 NAS/数据湖 |
| 3 | **🟡 Managed S3 Batch Operations（批量操作框架）** | 平台/体验 | P2 — 大规模数据管理的运维效率瓶颈 | `service/file_features.go:BatchDelete`（顺序循环）；`service/file_features.go:BatchSetTags`（顺序循环）；`reconcile/` 有框架但不可被用户调用 | 百万级对象的批量标记/复制/恢复只能串行处理，无进度追踪、无失败报告、无作业持久化 |
| 4 | **🟠 Client Certificate Authentication / mTLS（客户端证书认证）** | 安全/生态 | P1 — IoT/Service Mesh/B2B 集成的强制性需求 | `auth/auth.go:Authenticate`（JWT/Key/SigV4/Anonymous）；`cmd/server/main.go`（无 TLS listener 配置）；`middleware/middleware.go`（无 cert 提取逻辑） | 无内建 TLS/HTTPS，完全依赖反向代理；无客户端证书认证能力 |
| 5 | **🟡 S3 Object Lock with Legal Hold & Governance/Compliance Modes（完整对象锁）** | 合规/特性 | P2 — 金融/医疗/政府合规审计的硬性要求 | `service/file_features.go:LockObject`（仅有 LockedUntil）；`service/file_crud.go:hardDeleteObject`（lock check）；`repository/repository.go:BucketConfig`（ObjectLockSeconds 单一整数） | 当前 WORM 实现无法通过 S3 Object Lock 合规审计；Legal Hold 完全缺失；Governance/Compliance 模式无区分 |

---

## 1. 🟠 Block-Level Deduplication & Delta Compression（块级去重与增量压缩）

### 现状

当前系统每个 `Put` 请求都将整个对象作为一个原子 blob 写入存储。即使版本化桶中连续的两个版本只修改了一个字节，存储层也保存了两份完整的副本。

**当前写入路径：**
```
file_crud.go:Put → store.Put(ctx, storageKey, fullReader, size, opts)
```
其中 `storageKey = path.Join(tenant, bucket, key)` 或 `storageKey + "@v" + versionID`。

**版本化桶的成本爆炸场景：**

| 场景 | 当前行为 | 存储成本 |
|------|---------|---------|
| 100MB 数据库备份，每天一次，30 天 | 30 个完整 blob，每份 100MB | 3GB（实际差异 < 1%） |
| 10GB 虚拟机镜像，10 个版本 | 10 个完整 blob，每份 10GB | 100GB（层/内核缓存差异 < 5%） |
| 1MB 配置文件，1000 次编辑 | 1000 个完整 blob | 1GB（每次编辑仅改动几行） |

**已有但未被利用的基础设施：**
- `repository/sql_objects.go` 已有 `ETag` 字段（对象级哈希）
- `reconcile/scrub.go` 已计算 `_aero_content_md5` 用于完整性验证
- `storage/local.go` 支持按 key 读写，可扩展为按 content hash 读写

### 缺失能力矩阵

| 能力 | 当前 | 目标 |
|------|------|------|
| 对象级去重（同一内容不同 key） | ❌ | ✅ v7 已分析，独立实现 |
| **块级去重（对象内分块）** | ❌ | ✅ 对象按固定/可变窗口分块，每块 SHA-256 寻址 |
| **版本间增量压缩（Delta Compression）** | ❌ | ✅ 新版本 vs 上一版本的差异编码（类似 xdelta/zstd delta） |
| **引用计数与 GC** | ❌ | ✅ 每块引用计数，GC 在引用归零后异步删除 |
| **流式写入分块** | ❌ | ✅ PUT 时边流式计算块哈希，边写入 CAS 后端 |
| **块级随机读取** | ❌ | ✅ 大文件随机访问只拉取相关块（类似 QEMU qcow2） |
| **跨租户共享控制** | ❌ | ✅ 安全策略控制是否允许跨租户块去重 |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/storage/storage.go:Storage` 接口 | `Put(ctx, key, ...)` + `Get(ctx, key)` | 无 `PutBlock`、`GetBlock`、`ResolveContentHash` 接口 |
| `internal/storage/local.go:Put` | `os.WriteFile(keypath, data)` | 无块级引用链接 |
| `internal/storage/local_write.go` | 全量写入 | 无 `sync.Pool` 块缓冲、无分块循环 |
| `internal/service/file_crud.go:Put` | 一个 reader → 一个 storage key | 无分块 → 多块引用 → 元数据记录 |
| `internal/service/file_crud.go:buildPutObject` | `Object.StorageKey = sk` | 需要 `Object.BlockRefs []BlockRef` 字段 |
| `internal/repository/repository.go:Object` | 无 `ContentHash` `BlockCount` `BlockRefs` | 需要扩展 schema 或新增 `blocks` 表 |
| `internal/repository/sql_objects.go` | `objects` 表 | 需要 `block_refs` 表（`object_id, seq, block_hash, offset, length`） |
| `internal/reconcile/scrub.go` | 对象级 MD5 校验 | 需要块级校验 + 未引用块的 GC |
| `internal/reconcile/job.go` | 孤儿 blob 清理 | CAS GC 需要引用计数而非 storage_key 存在性检查 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 目标行为 |
|-----------|------|---------|---------|
| **小文件去重阈值** | 4KB 以下的小文件分块收益为负 | 全量存储 | 可配置 `BLOCK_MIN_SIZE`（默认 4KB），小于阈值跳过 |
| **加密对象块级去重** | SSE 加密的对象，内容相同但密文不同（不同 nonce） | 无法去重 | 去重必须在明文层（客户可选）或者仅对同密钥/同 nonce 的对象去重 |
| **块引用泄漏** | PUT 写入块 A → 断电 → 元数据未提交 | 块 A 成为孤儿 | CAS GC 的 grace period 和一致性检查 |
| **增量压缩的版本边界** | v1→v2 差异 1%, v2→v3 差异 50%（批量替换） | 全量存储 v3 | 增量压缩基于全量基准 + 差异链；超过 `MAX_DELTA_CHAIN`（默认 32）自动全量 |
| **并发写入相同内容** | 两个请求同时写入相同块 | 全量写入两次，相同内容 | CAS 检测 + 引用计数原子递增（`INSERT ... ON CONFLICT DO UPDATE refcount++`） |
| **随机写入/追加写入** | 日志文件不断增长 | 不支持 | 追加写入 = 最后一块变更；需要 append API 或固定块策略 |
| **块大小异构** | 对象 A（64KB 块），对象 B（1MB 块） | 统一块大小 | 每个对象独立记录块大小；块大小一旦选定不可变更 |
| **跨后端块引用** | 部分块在 local FS，部分块在 S3 | 不可能（单后端架构） | 块级跨后端路由（v12 多后端方向 + 块级寻址的合力） |

### 架构概要

```
┌─ Block-Level CAS Layer ─────────────────────────────────────────│
│ 新增包: internal/storage/cas/blockstore.go                        │
│                                                                  │
│  blockHash = SHA-256(chunk) → BLAKE3（流式更快）                   │
│  deltaEncode(prevChunk, curChunk) → patch                        │
│                                                                  │
│  BlockStore interface:                                            │
│    PutBlock(ctx, hash, data) → ref                              │
│    GetBlock(ctx, hash) → data                                   │
│    RefBlock(ctx, hash) → refCount (原子 +1)                      │
│    UnrefBlock(ctx, hash) → refCount (原子 -1, 归零时标记 GC)      │
│                                                                  │
│  FileService 写入变更:                                             │
│    Put(obj) →                                                     │
│      1. split obj into blocks (滚动哈希窗口, buzhash/fastcdc)     │
│      2. for each block: BlockStore.PutBlock(hash, data)           │
│      3. record block_refs in repository                          │
│      4. first block = anchor, subsequent = delta chain            │
│                                                                  │
│  Repository 扩展:                                                 │
│    block_refs(object_id, seq, block_hash, offset, len, algo)      │
│    blocks(hash, size, ref_count, created_at)                      │
│                                                                  │
│  Storage 接口: 新增 StorageBlock 接口（可选，CAS store 实现）       │
```

---

## 2. 🟠 Hierarchical Namespace with Atomic Directory Operations（层次化命名空间）

### 现状

当前系统是一个**纯平的键值存储**。`/` 分隔符仅在前端显示时被"虚拟折叠"成目录结构——没有任何目录实体。

**当前目录模拟的代码证据：**

```go
// internal/api/webdav/dav.go:124-130
// WebDAV directories are virtual — no-op. Files create their own implicit dirs.
func (f *davFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
    return nil  // ← 目录创建是空操作！
}
```

```go
// internal/service/file_features.go:List
// List calls repo.ListObjects with prefix=prefix — 纯 prefix 匹配，无目录概念
func (s *FileService) List(ctx context.Context, tenant, bucket, prefix, marker string, limit int) (repository.ListPage, error) {
    return s.repo.ListObjects(ctx, tenant, bucket, prefix, marker, limit)
}
```

**当前缺失的能力对比：**

| 操作 | AWS S3（无 HNS） | AWS S3 Express One Zone（HNS） | 当前 AeroVault |
|------|-----------------|-------------------------------|---------------|
| 目录创建 | 虚拟 | 原子 mkdir | 空操作（WebDAV only） |
| 目录重命名 | 逐个文件 COPY + DELETE | 原子 rename | ❌ 遍历 COPY + DELETE |
| 目录删除 | 逐个文件 DELETE | 原子 rmdir（空目录） | ❌ 逐个 DELETE |
| 目录级 ACL | 桶 ACL + 前缀 Policy | 目录级 ACL | ❌ 无 |
| 目录级配额 | ❌ | ❌ | ❌ 仅有租户级 |
| 目录统计 | S3 Inventory | 即时 | ❌ 仅有桶级 BucketStats |
| 子树快照 | ❌ | ❌ | ❌ |

### 为什么需要

**企业级 NAS 替换**是对象存储最重要的增长市场之一。NetApp、Dell PowerScale、Qumulo 等企业 NAS 产品的核心能力就是层次化命名空间 + 原子目录操作。没有 HNS：

1. **无法迁移现有 NAS 工作负载** — `mv /projects/2025 /archive/2025` 在 NAS 上是原子操作（毫秒级），在扁平 KV 上是遍历百万对象、逐个 COPY-DELETE（分钟级）
2. **目录级权限是合规基线** — SOC2/ISO 27001 要求"最小权限原则"，只能通过目录级 ACL 实现细粒度控制
3. **HPC/科学计算场景无法满足** — Lustre/GPFS 用户期望即时目录操作和海量小文件性能
4. **跨协议一致性问题加剧** — WebDAV 用户期望"真正的"目录语义，MCP 客户端期望结构化路径

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/repository/repository.go` | 无 Directory 类型 | 需要 `Directory` 实体（可选，类似 S3 HNS 的元数据映射） |
| `internal/repository/sql_objects.go` | `ListObjects(prefix)` 纯前缀匹配 | 无 `ListDirectory(parentID)` 或 `RenameDirectory(id, newName)` |
| `internal/repository/sql_buckets.go` | `BucketConfig` 无 | 需要 `DefaultDirACL` 字段 |
| `internal/service/file_features.go:List` | prefix 参数 | 需要 `ListDir(directoryID)` 区分"列出目录"vs"前缀搜索" |
| `internal/service/file_features.go` | 无 `Rename`、`MoveDir` | 原子 renames（轻量 metadata-only 操作 vs 重写所有 child） |
| `internal/api/webdav/dav.go` | Mkdir 是空操作 | 如果启用 HNS，Mkdir 应创建目录实体 |
| `internal/api/rest/router.go` | 无 `POST /v1/directories` | 需要目录 CRUD 路由 |
| `internal/reconcile/job.go` | 孤儿 blob 检测 | 需要孤儿目录检测（元数据目录无对应路径） |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 目标行为 |
|-----------|------|---------|---------|
| **非空目录重命名** | `rename /a (1000 objects) → /b` | 遍历 1000 个 key COPY+DELETE（分钟级、非原子） | metadata-only（更新所有 child 的 prefix，或移动子树根节点） |
| **目录 ACL 继承** | 在 `/projects/confidential/` 设置 ACL，新创建的子对象 | 无 ACL 或继承桶级 ACL | 创建对象时继承最近祖先目录的 ACL |
| **软删除 + 目录** | 删除目录 `/old-project/`（100 文件） | 每个文件单独 soft-delete | 目录级原子 soft-delete（所有 child 即时不可见） |
| **跨桶目录移动** | 目录从 A 桶移动到 B 桶 | 不支持 | 需要 COPY 模式（非 metadata-only，需后处理） |
| **目录配额违规** | 用户超出目录 /upload/ 的 10GB 配额 | 仅有租户级检查 | 目录级 check + 精确报错："exceeded directory quota for /upload/ (10GB)" |
| **混合同一前缀的文件和目录** | `/docs/readme.md` 和 `/docs/` 目录 | 无歧义（所有 key 默认扁平） | 严格区分文件和目录实体（类似 POSIX 的 `isDir` 标志） |
| **空目录删除** | 删除空目录后重新创建 | 重建空目录后再 PUT 文件即可 | 空目录实体可被删除，不影响子文件（objects 仍存在，只是丢失目录结构） |
| **WebDAV 兼容性** | Mac Finder 拖拽文件夹到 WebDAV | 逐个 COPY + DELETE（慢、非原子） | 原子 rename（WebDAV MOVE 实现为 metadata-only 操作） |

### 架构概要

```
┌─ HNS 扩展（可选特征标志） ───────────────────────────────────────│
│ 核心原则: 不破坏现有扁平 KV 路径（HNS 是可选 feature flag）          │
│                                                                  │
│ 新增 `directories` 表:                                            │
│   id, tenant, bucket, parent_id, name,                          │
│   created_at, updated_at,                                        │
│   acl TEXT,  quota_bytes, quota_objects                           │
│                                                                  │
│ 新增 Repository 方法:                                             │
│   CreateDirectory(ctx, parentID, name, acl) → Directory          │
│   GetDirectoryByPath(ctx, path) → Directory                      │
│   MoveDirectory(ctx, id, newParentID) → error                    │
│   DeleteDirectory(ctx, id) → error (拒绝非空？取决于 policy)        │
│   ListDirectory(ctx, id, marker, limit) → (Directory[], Object[]) │
│                                                                  │
│ 文件与目录的关联:                                                   │
│   方案 A（弱绑定）：object.directory_id = dir.id                    │
│   方案 B（路径解析）：将 path 翻译为 directory_id（实时 Join）        │
│                                                                  │
│ 原子 Rename：                                                      │
│   MoveDirectory(id, parentID)                                   │
│   不移动任何 storage blob，只更新 directories 表 + objects.directory_id │
│   → O(N) 对象 update... 但用 UPDATE ... SET directory_id = ?     │
│      WHERE directory_id = ? 可在数据库内完成                        │
│                                                                  │
│ 特征标志: HNS_ENABLED=true                                        │
│   关闭 = 保持现有扁平 KV 行为                                      │
│   开启 = 新对象放入目录树，旧对象作为根目录直接子级                    │
```

---

## 3. 🟡 Managed S3 Batch Operations / Bulk Processing Framework（批量操作框架）

### 现状

当前所有"批量"操作本质上都是 **client-side 顺序循环**：

```go
// internal/service/file_features.go:BatchDelete
func (s *FileService) BatchDelete(ctx context.Context, tenant, bucket string, keys []string) []BatchDeleteResult {
    results := make([]BatchDeleteResult, 0, len(keys))
    for _, key := range keys {      // ← 顺序循环，无并发控制
        err := s.Delete(ctx, tenant, bucket, key, false)
        // ...
    }
    return results
}

// internal/service/file_features.go:BatchSetTags
func (s *FileService) BatchSetTags(ctx context.Context, tenant, bucket string, keys []string, tags map[string]string) []BatchTagResult {
    results := make([]BatchTagResult, 0, len(keys))
    for _, key := range keys {      // ← 顺序循环
        err := s.SetTags(ctx, tenant, bucket, key, tags)
        // ...
    }
    return results
}
```

**问题：**
- 没有进度追踪（处理了 1000/100000？调用方不知道）
- 没有作业持久化（服务重启后进度丢失）
- 没有并发控制（100 万对象 = 100 万次串行请求）
- 没有失败报告（第 5000 个失败，之前的 4999 个已执行，后续未执行）
- 没有幂等性保证（重试可能导致重复操作）
- 没有 S3 Batch Operation API 兼容性（XML 请求/响应格式）

### 缺失能力矩阵

| 能力 | AWS S3 Batch Operations | 当前 AeroVault |
|------|------------------------|---------------|
| 创建批量操作 Job | `POST /jobs`（manifest + operation + priority） | ❌ |
| Job 进度追踪 | `GET /jobs/{id}`（status, completed, failed, total） | ❌ |
| Job 结果报告 | 完成时 S3 生成 CSV 报告 | ❌ |
| Job 持久化 | 幂等，重启后恢复 | ❌ |
| 并发控制 | 内部并行 + rate-limit | ❌ |
| Manifest 格式 | S3 Inventory CSV / S3 Select / 对象列表 | ❌ |
| 支持的操作 | Copy, Tag, Restore, PutACL, Lambda invoke | ❌（仅 client-side Delete + Tag） |
| 完成通知 | SNS 通知 | ❌ |
| 优先级/调度 | Job priority 队列 + 调度 | ❌ |
| 失败自动重试 | 可配置重试次数 | ❌ |

### 为什么需要

1. **S3 兼容性核心缺口** — `PUT /?batch` 是 S3 扩展 API。主流 S3 客户端工具期望标准批量操作接口
2. **运维效率** — 1 万对象的标签更新 = 1 个 Job 请求 vs 1 万个 API 请求
3. **大规模数据治理** — 合规标签、过期标记、存储类转换在大规模下必须可管理
4. **进度可观测** — 运维人员需要知道"批量标记 1000 万个文件的进度是 47%"

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/service/file_features.go:BatchDelete` | 顺序循环，单请求 | 需要替代为 Job 提交 |
| `internal/service/file_features.go:BatchSetTags` | 同上 | 同上 |
| `internal/jobs/jobs.go` | `Registry` + `Queue`（通用 job 框架） | 可复用（新增 `BatchOperationJob` handler） |
| `internal/jobs/jobs.go:Job` | 现有 `Job` 类型（Type, Status, Payload） | 需要 `BatchManifest` 类型的 payload |
| `internal/repository/sql_buckets.go` | 无 batch_jobs 表 | 需要 batch_jobs(batch_id, status, total, completed, failed, manifest_url, ...) |
| `internal/api/rest/router.go` | 无 batch 路由 | 需要 `POST /v1/admin/batch`、`GET /v1/admin/batch/{id}` |
| `internal/api/s3compat/router.go` | 无 S3 batch 路由 | 需要 `POST /?batch` |
| `internal/api/rest/admin_jobs.go` | 作业查看 API（ListJobs, RetryJob） | 可扩展为 batch job 管理 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 目标行为 |
|-----------|------|---------|---------|
| **万亿级 Manifest** | 100 亿对象需要批量标记 | OOM（manifest 读入内存） | 流式读取 manifest（S3 Select / CSV streaming） |
| **部分失败** | 批量删除 10000 文件，其中 47 个因 lock 失败 | 当前：序列化错误列表 | 记录 failed 列表到 CSV，Job 最终状态 = "completed with errors" |
| **Job 中断重启** | 批量操作执行到 60% 时服务重启 | 当前：所有进度丢失 | 幂等 Job：从 last_marker 恢复，已处理项跳过 |
| **跨租户批量** | Admin 在多个租户上执行批量操作 | 逐租户串行 | Job manifest 包含 tenant 字段，并行处理各租户 |
| **大并发导致后端抖动** | 10 万个并发 Delete 请求打爆存储后端 | 无速率控制 | batch job 内置令牌桶 + 动态自适应节流 |
| **S3 Batch 与现有 Job 系统冲突** | JobPool 正在处理 indexer job，batch job 同时提交 | 无队列优先级 | batch 类型的工作者从同一 JobQueue 消费，但可设置优先级 |
| **Manifest 格式兼容** | 客户从 AWS S3 导出清单直接用于 Batch | 无 manifest 解析 | 支持 CSV（S3 Inventory 格式）+ JSON Lines + S3 Select |
| **超时控制** | 一个 Batch Copy Job 需要 48 小时 | 无超时（TCP 连接？） | Job 级 timeout + progress heartbeat（每 5 分钟写入 progress） |

### 架构概要

```
┌─ Batch Operations Framework ─────────────────────────────────────│
│                                                                  │
│  基于现有 jobs 系统扩展:                                           │
│                                                                  │
│  新增 BatchJob 类型:                                              │
│    type BatchOperation string // copy | tag | restore | delete   │
│    type BatchJob struct {                                         │
│        ID, Type, Status, Priority                                │
│        Manifest:        BatchManifest                            │
│        Operation:       BatchOperation + Options                 │
│        Progress:        BatchProgress                            │
│        ErrorReport:     string // CSV 下载 URL                    │
│        CompletedAt, ExpiresAt                                    │
│    }                                                             │
│                                                                  │
│  Manifest 格式:                                                   │
│    type BatchManifest struct {                                    │
│        Format   string // "csv" | "jsonl" | "s3_select"         │
│        Location string // URL/path to manifest file              │
│        ETag     string // 完整性校验                              │
│        Total    int64  // 预估行数                                │
│    }                                                             │
│                                                                  │
│  Job 执行 Loop:                                                   │
│    1. 流式读取 manifest                                           │
│    2. 内部并发管道（可配置 concurrency, default=10）               │
│    3. 每个操作更新 progress                                       │
│    4. 所有完成 → 生成 CSV 错误报告                                 │
│    5. 触发完成通知（webhook/SNS）                                  │
│                                                                  │
│  REST API:                                                        │
│    POST /v1/admin/batch — 创建 Job                                │
│    GET  /v1/admin/batch/{id} — 查看进度                           │
│    POST /v1/admin/batch/{id}/cancel — 取消 Job                   │
│    GET  /v1/admin/batch/{id}/report — 下载错误报告                │
```

---

## 4. 🟠 Client Certificate Authentication / mTLS（客户端证书认证）

### 现状

当前认证体系：**JWT / API Key / SigV4 / Anonymous**。没有任何与客户端 TLS 证书相关的代码。

```go
// internal/auth/auth.go:Authenticate
func (r *Registry) Authenticate(ctx context.Context, token string, method, rsc string) (context.Context, error) {
    // 解析 Bearer JWT
    // 或匹配 X-Api-Key (sha256)
    // 或 SigV4
    // 或 匿名公读
    // ❌ 无 TLS 证书提取与验证
}
```

```go
// cmd/server/main.go:runServer
func runServer(ctx context.Context, handler http.Handler, cfg *config.Config, ...) error {
    server := &http.Server{Addr: addr, Handler: handler}
    // ❌ 无 TLSConfig
    // ❌ 无 ClientAuth
    return server.ListenAndServe()  // ← 纯 HTTP
}
```

**注意：** 代码中没有一个 TLS listener。当前系统假设卸载 TLS 到反向代理（nginx/cloudflare）。这意味着：

1. 无法原生终止 HTTPS
2. 无法提取客户端证书
3. 无法做 mTLS 握手
4. 服务到服务通信无身份认证

### 缺失能力矩阵

| 能力 | 当前 | 目标 |
|------|------|------|
| HTTPS 原生终止 | ❌（依赖反向代理） | ✅ 可选内建 TLS |
| 客户端证书提取 | ❌ | ✅ 从 `r.TLS.PeerCertificates` 提取 |
| mTLS 握手 | ❌ | ✅ `tls.Config{ClientAuth: tls.RequireAndVerifyClientCert}` |
| CA 信任链管理 | ❌ | ✅ 可配置 CA 证书 / 中间 CA |
| 证书→租户映射 | ❌ | ✅ CN/SAN → tenant 映射规则 |
| 证书吊销检查 | ❌ | ✅ OCSP Stapling / CRL |
| 证书自动轮换 | ❌ | ✅ ACME (Let's Encrypt) 集成 |
| mTLS + 其他认证组合 | ❌ | ✅ 可选：mTLS 替代或附加于 JWT/Key |

### 为什么需要

1. **IoT 设备认证** — 物联网设备无法安全存储 API Key/JWT Secret，X.509 证书是 IoT 认证标准（Eclipse Hono/AWS IoT Core 模式）
2. **Service Mesh / Zero Trust** — Istio/Linkerd 等网格期望服务间通信使用 mTLS；aero-vault 作为数据平面必须原生支持
3. **B2B 集成** — 企业间 API 集成中，mTLS 是银行/金融/医疗的标准认证方式（STAR/Nacha 规范）
4. **Kuberenetes 原生集成** — K8s 自动签发 pod 身份证书 (kubelet CSR)，aero-vault 可直接信任
5. **行业合规** — PCI DSS 4.0、HIPAA、NIST SP 800-52 要求传输层双向认证

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `cmd/server/main.go:runServer` | `http.Server{Addr, Handler}` | 需要 `TLSConfig` 字段 |
| `cmd/server/main.go:buildRouter` | `chi.NewRouter()` | 无需改动 |
| `internal/config/config_app.go` | 无 TLS 配置 | 需要 `TLS.CertFile`, `TLS.KeyFile`, `TLS.CAFile`, `TLS.ClientAuth` |
| `internal/auth/auth.go:Authenticate` | 6 种认证方式 | 需要 "client_cert" 认证器 |
| `internal/auth/auth_middleware.go` | 从 Header 提取 token | 需要从 `r.TLS.PeerCertificates[0]` 提取 |
| `internal/auth/store.go` | 无 | 需要 Certificate→Tenant 映射存储（或从 CN 解析） |
| `internal/middleware/middleware.go` | RequestID, CORs, Auth, Tenant | 建议在 Auth 之前注入证书信息到 context |
| `internal/middleware/middleware_test.go` | 无 mTLS 测试 | 需要 `tls.Config` + `httptest` 测试 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 目标行为 |
|-----------|------|---------|---------|
| **证书过期** | 客户端使用已过期的证书连接 | N/A（无 TLS） | TLS 握手失败（Go stdlib 自动检查） |
| **CA 未知** | 证书由非信任 CA 签发 | N/A | TLS 握手失败 |
| **证书撤销** | 证书被 CA 吊销（CRL/OCSP） | N/A | OCSP 装订检查 → 拒绝 |
| **证书 SAN 匹配** | CN=`device-01`, SAN=`device-01.example.com` | N/A | 两字段均可配置为 tenant 映射源 |
| **证书+JWT 双重认证** | 要求同时提供有效证书和 JWT | N/A | Auth 链支持 AND 组合（两种认证均为 Require） |
| **证书仅用于特定路由** | `/mcp` 要求 mTLS，`/v1/files` 允许 JWT | N/A | 路由级 TLS 配置（需要 SNI 或路由级 middleware） |
| **自签名证书环境** | 开发/内网使用自签名 CA | N/A | 配置 `TLS.InsecureCA=true`（开发模式，非生产） |
| **证书链深度** | 中间 CA 签发的证书（深度 3+） | N/A | 验证完整证书链直到根 CA |
| **证书轮换热加载** | 更换 CA 证书而不重启服务 | N/A | `tls.GetConfigForClient` 回调实现热加载 |
| **K8s 自动注入** | Istio Sidecar 代理 mTLS，aero-vault 收到非 mTLS 请求 | 正常（被 envoy 透传） | 需要 envoy 透传证书到 Header（`X-Forwarded-Client-Cert`） |

### 架构概要

```
┌─ mTLS 认证扩展 ──────────────────────────────────────────────────│
│                                                                  │
│  Phase 1: 内建 HTTPS + mTLS listener                              │
│  Phase 2: 证书→租户映射                                          │
│  Phase 3: OCSP + 自动轮换                                        │
│                                                                  │
│  配置:                                                             │
│    AERO_TLS_ENABLED=true                                          │
│    AERO_TLS_CERT=/etc/certs/tls.crt                               │
│    AERO_TLS_KEY=/etc/certs/tls.key                                │
│    AERO_TLS_CA=/etc/certs/ca.crt                                  │
│    AERO_TLS_CLIENT_AUTH=require|verify|none                        │
│                                                                  │
│  Auth 扩展:                                                        │
│    func (r *Registry) authClientCert(ctx, certs []*x509.Certificate) │
│        → cert 中提取 CN/SAN → 映射为 tenant                        │
│        → context 中注入身份                                       │
│        → 可选的 JWT 附加验证                                      │
│                                                                  │
│  Middleware 变更:                                                  │
│    middleware 链新增: CertInjector                                 │
│      位置: Auth 之前（与 Auth 紧密配合）                            │
│      职责: r.TLS 不为空 → 提取 PeerCertificates → ctx 注入         │
│                                                                  │
│  Server 变更:                                                      │
│    tlsConfig := &tls.Config{                                      │
│        ClientAuth: tls.RequireAndVerifyClientCert,                │
│        ClientCAs:  caCertPool,                                    │
│        GetConfigForClient: /* 动态 SNI */                         │
│    }                                                              │
│    server.TLSConfig = tlsConfig                                   │
│    return server.ListenAndServeTLS(certFile, keyFile)             │
```

---

## 5. 🟡 S3 Object Lock with Legal Hold & Governance/Compliance Modes（完整对象锁实现）

### 现状

当前系统有一个**最小 WORM（Write Once Read Many）** 实现：

```go
// internal/repository/repository.go:BucketConfig
type BucketConfig struct {
    ObjectLockSeconds int  // 统一的默认锁定时间（秒）
}

// internal/service/file_crud.go:checkLockBeforeOverwrite
func (s *FileService) checkLockBeforeOverwrite(ctx context.Context, ...) error {
    if cur.LockedUntil != nil && cur.LockedUntil.After(time.Now()) {
        return ErrLocked  // 任何锁定都不可覆盖
    }
}
```

**当前实现与 S3 Object Lock 的差距：**

| S3 Object Lock 特性 | 当前状态 |
|---------------------|---------|
| **Governance 模式**（有特权的用户可以绕过） | ❌ 单一锁定，无模式区分 |
| **Compliance 模式**（任何人不可绕过） | ❌ 同上 |
| **Legal Hold**（独立于保留期的法律保全标志） | ❌ 完全缺失 |
| **默认保留设置**（DefaultRetention: days/years） | ✅ 部分（`ObjectLockSeconds`） |
| **个体对象保留覆盖**（PutObject 时指定 Retention） | ❌ 缺失 |
| **保留期延长**（不能缩短，只能延长） | ❌ 缺失 |
| **BypassGovernanceRetention 权限**（需 `s3:BypassGovernanceRetention`） | ❌ 缺失 |
| **GetObjectRetention** / **GetObjectLegalHold** API | ❌ 缺失 |
| **Object Lock Config API**（`PUT /{bucket}?object-lock`） | ❌ 缺失（仅 S3 子资源骨架） |
| **版本级锁定**（每个版本独立锁定状态） | ❌ 缺失 |

### 为什么需要

1. **S3 兼容性硬性缺口** — `?legal-hold`、`?retention`、`?object-lock` 三个 S3 子资源完全缺失。AWS SDK 和 S3 浏览器直接报错
2. **合规审计决定性的功能** — 金融/医疗/政府的合规审计（SEC 17a-4、FDA 21 CFR Part 11、HIPAA）要求 WORM 实现必须区分"治理者可绕过"（Governance）和"不可绕过"（Compliance）
3. **Legal Hold 是法律保全的基础** — 诉讼保全（Litigation Hold）要求独立于保留期的不可变标记，当前只能在 metadata 中模拟（无强制力）
4. **默认保留 vs 个体覆盖** — 桶级默认保留 + 上传时指定的个体保留期是 S3 Object Lock 的核心设计模式

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/repository/repository.go:BucketConfig` | `ObjectLockSeconds int` | 需要 `ObjectLockMode string`（"" | "GOVERNANCE" | "COMPLIANCE"）、`DefaultRetentionDays int`、`DefaultRetentionYears int` |
| `internal/repository/repository.go:Object` | `LockedUntil *time.Time` | 需要 `LegalHold bool`、`RetentionMode string`、`RetainUntilDate *time.Time` |
| `internal/repository/sql_objects.go:GetObject` | 无 LegalHold 字段 | 迁移新增 `legal_hold`、`retention_mode`、`retain_until_date` |
| `internal/repository/sql_objects.go:UpsertObject` | 不传递 LegalHold | 写入时携带锁定信息 |
| `internal/service/file_crud.go:Put` | 仅 `bcfg.ObjectLockSeconds` | 需要解析请求头 `x-amz-object-lock-legal-hold`、`x-amz-object-lock-mode`、`x-amz-object-lock-retain-until-date` |
| `internal/service/file_crud.go:hardDeleteObject` | 检查 `LockedUntil` | 需要检查 `LegalHold=true`（即使 LockedUntil 已过期） |
| `internal/service/file_crud.go:checkLockBeforeOverwrite` | 仅 `LockedUntil` | Governance 模式下可被 `BypassGovernanceRetention` 权限覆盖；Compliance 模式下永不可覆盖 |
| `internal/service/file_features.go:LockObject` | 仅设置时间 | 需要 `SetLegalHold`、`SetRetention`、`GetRetention`、`GetLegalHold` |
| `internal/api/s3compat/handler.go` | 无 `?legal-hold`、`?retention`、`?object-lock` 子资源处理 | 缺失三个 S3 子资源路由 |
| `internal/api/s3compat/bucketconfig.go` | `putBucketObjectLock` 骨架/缺失 | 缺少 ObjectLockConfiguration XML 解析 |
| `internal/auth/auth.go` | 无 `BypassGovernanceRetention` 权限常量 | 需要新增 Action 类型 |
| `internal/auth/policy.go` | Policy 引擎评估 | 需要新增 `s3:BypassGovernanceRetention` Action 评估 |
| `internal/repository/migrations/` | 无对象锁迁移 | 需要 `0025_object_lock`（双文件） |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 目标行为 |
|-----------|------|---------|---------|
| **Governance 模式 + 权限绕过** | 合规官需要删除一个因 Governance 模式锁定的过期对象 | 不能删除（锁定阻挡） | 拥有 `BypassGovernanceRetention` 权限的用户可以删除/覆盖 |
| **Compliance 模式 + 任何人** | 对象被 Compliance 模式锁定，即使 root 也无法删除 | N/A（当前无此模式） | 严格拒绝任何删除/覆盖，直至保留期结束 |
| **Legal Hold + 已过期保留** | 保留期已过（`LockedUntil < now`）但 Legal Hold = ON | 当前：Legal Hold 不存在，可被删除 | 即使保留期已过，Legal Hold 激活的对象不可删除 |
| **Legal Hold 解除** | 诉讼结束，Legal Hold 标记解除 | N/A | 需要 `s3:PutObjectLegalHold` 权限 + 审计记录 |
| **保留期缩短** | 合规人员试图将保留期从 7 年缩短为 1 年 | N/A | 拒绝（S3 规范：retention period can only be extended, never shortened） |
| **版本级锁定不一致** | v1: Governance/2026, v2: Compliance/2030, v3: no lock | 无版本绑定 | 每个版本独立记录锁定模式 + 时间；GET v1 vs GET v2 不同锁定行为 |
| **默认保留 + 个体覆盖** | 桶默认 30 天，但某个对象上传时指定 90 天 | 当前：仅桶级 30 天 | 个体覆盖优先于默认保留 |
| **非版本化桶+对象锁** | 用户尝试在非版本化桶上启用 Object Lock | 静默忽略或报错？ | PUT Bucket Object Lock 要求版本化已启用 → 返回 400 InvalidRequest |
| **锁定的对象 + 生命周期** | Lifecycle 规则尝试删除 Governance 锁定的对象 | 当前：检查 LockedUntil，跳过 | Governance 模式下：Bypass 权限检查；Compliance 模式下：无论如何跳过 |
| **锁定的对象 + 复制** | 复制 Governance 锁定的对象到目标桶 | 当前检查 | 目标桶也必须启用 Object Lock，否则复制失败 |
| **删除标记 + Object Lock** | 版本化桶中，锁定对象的最新版本被标记删除 | 创建删除标记 | 当前最新版本被锁定 → 删除标记仍可创建（S3 规范允许）但不可永久删除该版本 |

### 架构概要

```
┌─ S3 Object Lock 完整实现 ────────────────────────────────────────│
│                                                                  │
│  概念模型:                                                        │
│    Object Lock 是桶级开关（S3_BUCKET_OBJECT_LOCK_ENABLED=true）     │
│    启用后:                                                        │
│      1. 桶不能删除                                                │
│      2. 每个 Put 可选择指定 Retention Mode + RetainUntilDate       │
│      3. 未指定时使用 DefaultRetention                              │
│      4. Legal Hold 独立于 Retention 设置                           │
│                                                                  │
│  BucketConfig 扩展:                                               │
│    ObjectLockEnabled     bool     // 桶锁定开关                   │
│    ObjectLockMode        string   // "GOVERNANCE" | "COMPLIANCE" │
│    DefaultRetentionDays  int      // 0 = 未设置                    │
│    DefaultRetentionYears int      // 0 = 未设置                    │
│                                                                  │
│  Object 扩展:                                                     │
│    LegalHold         bool                                        │
│    RetentionMode     string   // "" | "GOVERNANCE" | "COMPLIANCE" │
│    RetainUntilDate   *time.Time                                  │
│                                                                  │
│  权限扩展:                                                        │
│    ActionBypassGovernanceRetention = "s3:BypassGovernanceRetention" │
│    ActionPutObjectLegalHold        = "s3:PutObjectLegalHold"       │
│    ActionGetObjectLegalHold        = "s3:GetObjectLegalHold"       │
│    ActionPutObjectRetention        = "s3:PutObjectRetention"       │
│    ActionGetObjectRetention        = "s3:GetObjectRetention"       │
│                                                                  │
│  S3 子资源:                                                       │
│    GET/PUT /{bucket}?object-lock → ObjectLockConfiguration XML    │
│    GET/PUT /{bucket}/{key}?legal-hold → LegalHold XML             │
│    GET/PUT /{bucket}/{key}?retention → Retention XML              │
│                                                                  │
│  删除检查逻辑:                                                    │
│    canDelete(obj):                                                 │
│      if obj.LegalHold → DENY                                      │
│      if obj.RetentionMode == "COMPLIANCE" && obj.RetainUntilDate > now → DENY │
│      if obj.RetentionMode == "GOVERNANCE" && obj.RetainUntilDate > now:       │
│        if ctx.HasPermission(BypassGovernanceRetention) → ALLOW     │
│        else → DENY                                                │
│      → ALLOW                                                      │
```

---

## 总结：优先级与实施建议

| # | 方向 | 优先级 | 影响面 | 预估工作量 | 前驱依赖 |
|---|------|--------|-------|-----------|---------|
| 1 | Block-Level Deduplication & Delta Compression | P1（成本） | `storage` / `service` / `repository` / `reconcile` | 高（3-4 周） | v7 对象级 CAS（优先实施作中间步骤） |
| 2 | Hierarchical Namespace | P1（体验） | `repository` / `service` / *aLL 协议* / `reconcile` | 高（3-4 周） | 需要架构决策（弱绑定 vs 强绑定） |
| 3 | Managed Batch Operations | P2（效率） | `jobs` / `repository` / `api/rest` / `api/s3compat` | 中（1-2 周） | 无（复用现有 jobs 系统） |
| 4 | Client Certificate Authentication / mTLS | P1（安全） | `cmd` / `auth` / `config` / `middleware` | 中（1-2 周） | 无 |
| 5 | Complete S3 Object Lock | P2（合规） | `repository` / `service` / `api/s3compat` / `auth` / `reconcile` / migration | 中（2-3 周） | 无 |

**快速取胜（1 周以内）：**
- #4 mTLS — 内建 HTTPS listener + 客户端证书提取（Phase 1）
- #3 Batch Operations — 复用 `jobs` 系统 + REST API（Phase 1）

**长期战略（需要架构决策）：**
- #1 Block-Level CAS — 需要扩展 Storage 接口、新增 blocks 表、GC 改造
- #2 HNS — 需要 Architecture Decision Record 决定：HNS 是盖在扁平 KV 上的薄层还是核心数据模型？
- #5 Object Lock — 需要完整的 migration + API handler + auth policy 集成
