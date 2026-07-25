# 高价值扩展方向分析 v33 — 全新架构盲区：文件系统网关、审计链、标签自动化、内容告警、写入优化

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 ~17,000 行 `.go` 代码 + `sdk/*` + `deploy/*` + `docs/*` + 48 对迁移文件）
> **分析日期：** 2026-07-10
> **视角：** 资深架构师 / 产品经理 — 聚焦「此前 32 期分析（累计 ~150+ 方向、21,000+ 行分析文本 + `ROADMAP.md` + `CHANGELOG.md` + `TODO.md`）从未实质触及的 5 个全新高价值方向」
> **去重方法：** 逐领域 `grep` 验证 `docs/requirements/` 下 **32 期既有分析**（v1–v32）+ `docs/ROADMAP.md` + `docs/adr/DECISIONS.md`。每个方向在既有文档中 **零实质性架构分析**。
> **原则：** 不编写任何实现代码。每个方向附带：现状 → 代码锚点 → 缺失能力 → 为什么需要 → 架构概要 → 边界情况。

---

## 审阅：前 32 期覆盖边界（去重矩阵）

前 32 期 expansion 文档覆盖了 **约 160+ 个方向**，核心领域分布：

| 领域 | 已覆盖方向数 |
|------|------------|
| AI/RAG 管线（嵌入/搜索/Chat/Agent/Indexer/Rerank/PII/缓存/预算/模型路由/语义缓存） | ~24 |
| S3 兼容协议（子资源/ACL/Policy/CORS/通知/LegalHold/COPY/Batch/Multipart/SSE-C/Select） | ~18 |
| 存储后端（Local/S3/OSS/COS/KMS/SSE/熔断器/分层/压缩/块级去重/CAS/多后端/压缩/迁移） | ~20 |
| 认证授权（JWT/API Key/SigV4/OIDC/SAML/SCIM/MFA/前缀级策略/FIPS/Policy Engine/mTLS） | ~18 |
| 多租户（CRUD/配额/预算/审计/日费用/物理隔离/FGA/IaC/Admin Console/Terraform/计费） | ~18 |
| 事件/通知/Webhook（总线/传输/过滤/多通道/死信/背压/CDC/Kafka/Lambda 触发/事件重放） | ~15 |
| 复制/HA/集群（CRR/SRR/单例/Federation/多活/CQRS/故障转移/Geo-Distributed） | ~14 |
| Reconcile/GC/Lifecycle（孤儿/保留/Scrub/转换/版本/Multipart/非当前版本/上传GC） | ~14 |
| 合规（WORM/Legal Hold/保留/访问日志/客户端加密/对象锁模式/数据驻留/Geo-Fencing/SOC2） | ~14 |
| 可观测性（OTel/Metrics/Grafana/Prometheus/SLO/Tracing/pprof/告警/Debug/Profiling） | ~12 |
| 工程质量（内存安全/流式加密/并发/压缩/错误模型/测试/性能/多协议一致性/代码质量） | ~13 |
| Web UI / Admin Console / MCP 工具完整性 | ~9 |
| SDK / CLI 完整性 | ~7 |
| 基础设施（TLS/ACME/配置热重载/IP ACL/Feature Flag/Helm/CDN/Data Provenance/熔断器） | ~10 |
| 其他（SQL查询/预测性分层/GitOps/插件/备份/DR/批量操作/Serverless触发/命名空间） | ~10 |

### 本期 5 个方向在前 32 期分析中 **均零实质性覆盖**（去重证据）

| # | 方向 | `grep -rli` 命中数 | 既有覆盖情况 |
|---|------|-------------------|------------|
| 1 | **FUSE/POSIX 文件系统网关** | `fuse\|nfs\|smb\|cifs\|s3fs\|mount.*point\|filesystem.*gate` → **0 命中** | **完全未覆盖** |
| 2 | **监管链 / 法证完整性（Chain of Custody）** | `chain.*custody\|forensic\|tamper.*evident\|hash.*chain\|merkle\|digital.*sign\|object.*provenance` → 仅 2 处浅层引用 | **零实质性架构分析** |
| 3 | **标签驱动自动化引擎（Tag-Driven Automation）** | `tag.*automation\|tag.*orchestrat\|tag.*propagat\|tag.*inherit\|tag.*rule\|tag.*policy` → **0 命中** | **完全未覆盖** |
| 4 | **内容感知索引监控与告警（Content-Aware Index Alerting）** | `search.*alert\|search.*webhook\|search.*notif\|index.*watch\|content.*alert\|content.*watch` → **0 命中** | **完全未覆盖** |
| 5 | **写入优化层（Write Buffering & Coalescing）** | `write.*buffer\|write.*coalesc\|buffered.*io\|small.*write\|write.*throughput\|async.*write` → 仅 2 处浅层引用 | **零实质性架构分析** |

---

## 本期方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 |
|---|------|------|--------|---------|
| 1 | **🔴 FUSE/POSIX 文件系统网关** | 生态/集成 | **P0 — 企业落地硬要求** | 现有应用无法通过文件系统接口接入；WebDAV 性能不足且无内核支持 |
| 2 | **🔴 监管链与法证完整性（Chain of Custody）** | 合规/安全 | **P1 — 金融/医疗/法律合规强制性** | 没有任何加密审计链来证明数据未被篡改；审计日志可被静默修改 |
| 3 | **🟡 标签驱动自动化引擎** | 运维/成本 | **P1 — 大规模数据管理的效率瓶颈** | 标签仅用于描述，没有基于标签的生命周期/复制/告警自动化规则 |
| 4 | **🟠 内容感知索引监控与告警** | 安全/可观测 | **P2 — 数据安全与运营盲区** | 上传的内容变更后无法触发安全告警；敏感内容在索引后无通知机制 |
| 5 | **🟠 写入优化层（Write Buffering & Coalescing）** | 性能/成本 | **P2 — 小对象写入性能天花板** | 每次 `Put` 直写存储后端；小对象高频写入导致 IOPS 爆炸和存储碎片 |

---

## 方向一：🔴 FUSE/POSIX 文件系统网关

### 现状

当前 aero-vault 支持的应用层协议：
- REST `/v1`（JSON API）
- S3 兼容 `/s3`（XML + SigV4）
- WebDAV `WEBDAV_PREFIX`（XML 属性）
- MCP `/mcp`（JSON-RPC 2.0）

**唯一的文件系统级接口是 WebDAV**（`internal/api/webdav/dav.go`，458 行）。WebDAV 的问题：
1. **非标准实现**：许多操作系统级文件系统客户端（Windows 资源管理器、macOS Finder、Linux GVFS）对 WebDAV 的支持有 bug 且性能差。
2. **无内核态缓存**：WebDAV 每次 `readdir`/`stat`/`read` 都走 HTTP 请求/RTT，延迟比 FUSE/NFS 高 1-3 个数量级。
3. **无文件锁**：WebDAV 类 2（LOCK/UNLOCK）未实现，不支持并发写入保护。
4. **无 POSIX 语义**：无法 `rename` 跨目录原子操作（当前 `Rename` 走 copy+delete），无 `chmod`/`chown`，无 hard link/symlink，无 truncate。

```go
// internal/api/webdav/dav.go — 当前 WebDAV 缺失清单
// - LOCK / UNLOCK (WebDAV Class 2)
// - PROPPATCH (部分属性)
// - MOVE 跨存储后端
// - COPY 跨存储后端
// - 大文件流式（spillBuffer 已修复, 见 spill.go）
```

### 为什么需要

**1. 企业应用兼容性是最大的用户获取杠杆。**

企业工作负载（CI/CD 流水线、数据管道、IDE 集成、备份工具、旧式文件服务器迁移）**期望一个可挂载的文件系统**，而不是一个 HTTP API。提供 `/s3` S3 兼容接口已经覆盖了 AWS SDK 生态，但对以下场景完全无效：

| 场景 | 当前状态 | 有文件系统网关后的状态 |
|------|---------|-------------------|
| `rsync` 备份到 aero-vault | 不可用 — `rsync` 期望文件系统 | `mount -t fuse aero-vault /mnt && rsync /data /mnt` |
| IDE 直接打开远程文件 | WebDAV 支持差，延迟高 | FUSE 挂载后像本地文件一样操作 |
| Docker/容器挂载持久卷 | NFS 才是 CSI 的标准协议 | NFS export + Kubernetes CSI driver |
| 日志采集器（Fluentd/Filebeat） | 只能通过 S3 SDK 写，增加复杂度 | 配置 `path` 直接写入挂载目录 |
| 旧版 NAS 替换（Isilon/NetApp） | 完全不兼容 | 提供 SMB/CIFS 共享给 Windows 客户端 |

**2. FUSE 是实现成本最低、效果最好的文件系统协议。**

- FUSE（Filesystem in Userspace）在 Linux 上生态成熟（`libfuse`/`fuse2`）—— Go 有 `bazil.org/fuse` 和 `jacobsa-fuse` 两个成熟绑定库。
- FUSE 可提供接近原生的 POSIX 语义：`open`/`read`/`write`/`readdir`/`getattr`/`setattr`/`rename`/`unlink`/`mkdir`/`rmdir`/`symlink`/`link`。
- 延迟：FUSE（~50μs per op）远优于 WebDAV（~5-50ms per op），但仍需关注内核页面缓存的配合。

**3. NFS v3/v4 和 SMB 是第二大需求。**

- NFS 是 Linux/Unix 生态的标准网络文件系统协议，也是 Kubernetes CSI（Container Storage Interface）的首选协议。
- SMB/CIFS 是 Windows 生态的标准协议。
- 从 FUSE 实现转向内核态 NFS 导出（通过 `nfsd`/`ganesha`）或 SMB 导出（通过 `samba`）是成熟的扩展路径。

### 架构概要

```
aero-vault process
┌────────────────────────────────────────────┐
│  internal/gateway/                          │
│  ┌────────────────────────────────────┐     │
│  │ FUSE daemon (userspace)            │     │
│  │   fuse.Service:                    │     │
│  │     Getattr → svc.Stat             │     │
│  │     Lookup  → svc.Stat             │     │
│  │     Open    → svc.Get              │     │
│  │     Read    → svc.Get (range)      │     │
│  │     Write   → svc.Put (buffered)   │     │
│  │     Create  → svc.Put              │     │
│  │     Mkdir   → svc.Put (zero-byte   │     │
│  │                dir marker object)  │     │
│  │     Rmdir   → svc.Delete           │     │
│  │     Rename  → svc.Copy + Delete    │     │
│  │     ReadDir → svc.List             │     │
│  │     Unlink  → svc.Delete           │     │
│  └────────────────────────────────────┘     │
│                                             │
│  ┌────────────────────────────────────┐     │
│  │ NFS export (v3/v4 via ganesha/     │     │
│  │ built-in NFSv4 server in future)   │     │
│  │  (Phase 2)                          │     │
│  └────────────────────────────────────┘     │
│                                             │
│  ┌────────────────────────────────────┐     │
│  │ SMB/CIFS share (via samba/         │     │
│  │ built-in SMB server)               │     │
│  │  (Phase 2)                          │     │
│  └────────────────────────────────────┘     │
│                                             │
│  FileService (existing)                     │
│    ├─ storage.Storage                       │
│    └─ repository.Repository                 │
└────────────────────────────────────────┘
```

**关键设计决策：**

- **目录是虚拟的**——aero-vault 没有真正的目录对象。FUSE `Mkdir` 创建一个零字节标记对象（例如 `dir/.aero-dir` 或利用 prefix 隐式存在），`ReadDir` 通过 `List(prefix)` 聚合前缀折叠层实现。**这与 S3 的 prefix 模型一致，也是 FUSE-over-S3 类系统（如 s3fs、goofys）的标准做法。**
- **写入缓存**：FUSE `Write` 到的数据先在用户态缓冲区累积，flush 时合并写出——可大幅减少小对象写入次数（见方向五）。
- **目录列表缓存**：`ReadDir` 的结果应以 TTL 缓存，防止每次 `ls` 都穿透到 `ListObjects`（大规模 bucket 下开销极大）。
- **并发一致性**：FUSE 内核缓存可能导致多个挂载点之间的可见性问题。应利用内核的 `attr_timeout`+`entry_timeout` 参数控制缓存时长，或使用 `libfuse` 的 `notify_inval_entry`/`notify_inval_inode` 推送失效。

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| 目录不存在但创建文件 | 自动创建虚拟目录（`FileService.Put` 已隐式支持） |
| 大文件写入 | FUSE 层面流式写入（分片），不 buffer 全部在内存 |
| 并发同一文件写入 | FUSE 层依赖 kernel VFS 的锁 + aero-vault 的 `If-Match` 条件写 |
| 目录下有千万元素 | `ReadDir` 需要分页（系统 `getdents` 限制）或异步流式返回 |
| 符号链接 | FUSE `Symlink` → 存储为 metadata 中的 `_aero_symlink_target` |
| 文件锁（POSIX flock / BSD lock） | FUSE `Locks` 回调 → 在 repo 层实现持有者检查（内存中，重启丢失） |
| 硬链接 | FUSE `Link` → 同一 `storage_key` 的多个 metadata 行（引用计数） |
| 截断文件 | FUSE `Truncate` → 需要存储层支持 `Truncate` 操作（当前有 `Put` 全覆盖重写） |
| Windows 客户端 | SMB/CIFS 是第一需要；FUSE 在 Windows 上可通过 WinFsp + 映射得到 |

### 代码影响范围

| 模块 | 影响 |
|------|------|
| **新增** `internal/gateway/` | FUSE 挂载守护进程（`gateway fuse [mountpoint]`） |
| **新增** `internal/gateway/nfs/` | NFS 导出（Phase 2） |
| **新增** `internal/gateway/smb/` | SMB 共享（Phase 2） |
| **新增** `internal/gateway/dir/` | 虚拟目录管理器（创建/删除/列表与 Flat namespace 的映射） |
| **修改** `cmd/server/main.go` | `gateway` 子命令注册 |
| **修改** `internal/service/file.go` | 可能需要 `CreateDirectory` / `DeleteDirectory` / `Symlink` / `HardLink` 方法 |
| **修改** `internal/config` | `FUSE_MOUNT_POINT` / `FUSE_ALLOW_OTHER` 等配置项 |
| **新增** `deploy/helm/templates/fuse-daemonset.yaml` | FUSE DaemonSet 或 sidecar 模式部署 |
| **测试** | `gateway_test.go` — 用 `fuse` 测试库 + 内存 filesystem 来验证语义 |

### 为什么不直接使用现有 FUSE-over-S3 方案（s3fs / goofys）

s3fs 和 goofys 是为 AWS S3 设计的，它们假设了一个特定的 S3 后端。而 aero-vault 的 `FileService` 抽象了多后端（local/S3/OSS/COS），FUSE 层直接调用 `FileService` 接口可以获得：
- 统一的租户隔离（`X-Aero-Tenant` → 挂载参数 `-o tenant=xxx`）
- 统一的鉴权（JWT/API Key → 挂载参数 `-o token=xxx`）
- SSE 加密/解密（挂载时透明处理）
- 事件发布（写入触发 Webhook/副本）
- 版本化 bucket 的透明访问（可配置--查看最新版本或所有版本）

这是通用 S3 FUSE 客户端 **完全做不到** 的。

---

## 方向二：🔴 监管链与法证完整性（Chain of Custody / Forensic Integrity）

### 现状

当前系统的审计与完整性机制：
- **`reconcile/scrub.go`**：实现了周期性 MD5 校验，发现损坏对象标记为 `_aero_scrub_status=corrupt`。
- **`audit_log` 表**（migration `0016`）：记录 admin/security 操作（actor + action + target + detail）。
- **`Usage` 记录**：记录谁搜索了哪些 chunk/对象。
- **`Event` 持久化**：记录对象生命周期（`created`/`accessed`/`deleted`）。

**严重缺失：**

1. **审计日志可被静默篡改**：`audit_log` 表存储在 SQLite/Postgres 中，任何拥有数据库写权限的人（或攻击者）可以直接 `UPDATE`/`DELETE` audit 行。**没有任何手段检测审计日志的篡改**。

2. **没有数字签名**：对象本身不携带数字签名，无法证明对象由特定用户/系统在特定时刻写入，也无法在对象流转到外部系统后验证其真实性。

3. **没有操作链**：一个对象从 `created → accessed (N次) → replicated → deleted` 的完整操作链没有加密链接，无法验证操作的连续性和完整性。

4. **没有完整性证明**：无法向第三方审计员提供「文件 X 自 N 天前写入后未被篡改」的密码学证明。

### 为什么需要

**1. 金融/医疗/法律合规的硬要求。**

| 法规 | 要求 | 当前满足度 |
|------|------|-----------|
| **SOX** (Sarbanes-Oxley) | 电子记录必须保留，且任何修改/删除必须可审计。 | ⚠️ 部分 — 有 `audit_log`，但可被静默删除 |
| **HIPAA** | 访问审计链必须不可篡改。 | ❌ — 无防篡改审计 |
| **GDPR Art. 5(1)(f)** | 完整性和保密性。 | ❌ — 无加密完整性验证 |
| **FDA 21 CFR Part 11** | 电子记录必须有签名和时间戳。 | ❌ — 无数字签名 |
| **eDiscovery** (FRCP) | 电子发现须可验证证据链。 | ❌ — 无监管链证明 |
| **PCI DSS v4** | 审计跟踪必须防止篡改。 | ❌ — 无篡改检测 |

**2. 法律场景：证据能力（Admissibility）。**

在涉及电子证据的诉讼中，对方律师的第一步就是质疑数据完整性。**如果无法证明数据从采集到呈堂之间未被篡改，数据可能被法庭排除。** 监管链（Chain of Custody）文档是证明数据可采信的关键。

**3. 代码复用成本极低。**

现有基础设施：
- `_aero_content_md5` 已经在 `Put` 时存储（`storeContentMD5`）。
- `scrub.go` 已经有周期的 MD5 校验。
- `Event` 已经有完整的对象生命周期记录。
- `audit_log` 表已经存在。
- `reconcile/job.go` 有周期性的后台任务框架。

**只需要增加：哈希链（hash chain）、时间戳权威认证、和数字签名层。**

### 架构概要

```
Chain of Custody 架构
========================

┌─────────────────────────────────────────────────────────────────┐
│ 1. 写入时: 构建证据记录                                           │
│                                                                  │
│  svc.Put → 计算对象内容 MD5 + SHA256                              │
│          → 获取当前时间戳（或外部 TSA 时间戳令牌）                  │
│          → 构建 CoCEntry: {                                       │
│              prev_hash: last_entry.hash,   ← 哈希链             │
│              object_id, operation,                               │
│              content_hash, metadata_hash,                        │
│              timestamp (from TSA or monotonic clock),            │
│              actor (tenant + key ID),                            │
│              signature (optional: private key sign)              │
│            }                                                     │
│          → 计算当前 entry 的 hash                                 │
│          → 追加到 chain (anchor in DB + optional external log)   │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│ 2. 审计链存储                                                    │
│                                                                  │
│  主存储: custody_entries 表（数据库, 业务查询用）                  │
│    列: id, prev_hash, entry_hash, object_id, operation,         │
│         content_hash, actor, timestamp, signature               │
│                                                                  │
│  验证锚点: 定期（每小时/每天）将链的当前根哈希发布到:               │
│    - 另一个数据库（防跨库篡改）                                    │
│    - 日志文件（WORM 存储）                                        │
│    - 外部时间戳权威（RFC 3161 TSA）                               │
│    - 区块链/分布式账本（Phase 2）                                 │
│    - 仅追加的 S3 bucket（用 Object Lock 锁定）                    │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│ 3. 验证 API                                                     │
│                                                                  │
│  GET /v1/lineage/{id}  ← 已有（扩展为包含 CoC 链）               │
│  GET /v1/lineage/{id}/proof                                      │
│    → 返回对象的完整 CoC 链                                       │
│    → 返回链的签名根哈希                                          │
│    → 外部验证者可以:                                              │
│      1. 从哈希链重新计算每个 entry 的 hash                        │
│      2. 验证 prev_hash 链接连续性                                 │
│      3. 验证内容 hash == 当前对象 hash                            │
│      4. 验证签名（如果有）                                        │
│      5. 与锚定在外部系统中的根哈希对比                             │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│ 4. 验证 root anchor 管理                                         │
│                                                                  │
│  cli custody anchor --publish [--tsa URL]                        │
│    → 计算当前链根哈希                                             │
│    → 发往外部锚定（日志 / TSA / 区块链）                           │
│                                                                  │
│  cli custody verify --object-id 42 --anchor <hash>               │
│    → 从 DB 读取链                                                │
│    → 重建哈希链                                                  │
│    → 验证连续性                                                  │
│    → 验证内容 hash == 对象当前 MD5                                │
│    → 输出通过/失败报告                                            │
└─────────────────────────────────────────────────────────────────┘
```

**信任模型：**
- **Level 1（内部锚定）**：根哈希写回到第二个数据库表（不同持久化文件），攻击者需要同时篡改两个表。**检测篡改的能力 > 防篡改的能力。**
- **Level 2（外部锚定）**：每 N 小时自动将根哈希发布到外部仅追加的日志（或 S3 WORM bucket）。外部日志在写入后不可修改。
- **Level 3（时间戳权威）**：使用 RFC 3161 TSA 为每个 entry 或每批根哈希签署时间戳，证明数据在特定时间之前就已存在。
- **Level 4（区块链）**：将根哈希写入公共/联盟区块链，提供最大程度的独立验证。

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| 重建哈希链时发现断裂 | 标记从断点到现在的所有操作为 `unverified`，告警通知 |
| 旧系统升级：已有对象没有 CoC 链 | 为所有现有对象创建初始 CoC entry（`genesis`），记录 `content_hash` 和 `genesis` 标记 |
| 对象被合规删除 | 删除前创建最终的 `deleted` CoC entry，哈希链终止；对象内容可删除但审计链保留 |
| 对象在未挂载 CoC 的情况下更新 | 写入新的 CoC entry 并链接到上一个 entry（即使上一个 entry 是"缺失"节点） |
| TSA 不可用 | 降级为使用服务器本地时钟 + 记录 `timestamp_source: local`，告警但不阻断写入 |
| 大规模验证 | `GET /v1/lineage/proof` 需要分页支持（一个对象可能有百万级操作链？不，每个对象单独链） |

### 代码影响范围

| 模块 | 影响 |
|------|------|
| **新增** `internal/custody/` | 监管链管理包：`Chain`（哈希链计算/验证）、`Entry`（证据记录）、`Anchor`（根锚定发布者接口） |
| **新增** `internal/custody/store.go` | 存储：`custody_entries` 表的 CRUD、按 `object_id` 查询、按时间范围查询 |
| **新增** `internal/custody/verifier.go` | 验证器：重建哈希链、对比外部锚定哈希、生成验证报告 |
| **新增** `internal/custody/anchor/` | 锚定实现：`DBAnchor`（双表锚定）、`S3Anchor`（WORM 锚定）、`TSAAnchor`（RFC 3161 时间戳） |
| **修改** `internal/service/file_crud.go` | 在 `Put`/`Delete`/`hardDeleteObject`/`emit` 中调用 `custody.Record(ctx, entry)` |
| **修改** `internal/service/file_features.go` | `GetVersion`、`Delete` 等也需记录 CoC |
| **修改** `internal/reconcile/scrub.go` | 在 scrub 发现损坏时创建 CoC 证据记录 |
| **新增** `internal/api/rest/custody.go` | REST endpoints：`GET /v1/lineage/{id}/proof`、`GET /v1/admin/custody/verify/{id}` |
| **新增** migration `0025_custody.up.sql` | `custody_entries` 表 + `custody_anchors` 表 |
| **修改** `internal/cli/` | `aero-vault cli custody anchor --publish`、`aero-vault cli custody verify --object-id ...` |

---

## 方向三：🟡 标签驱动自动化引擎（Tag-Driven Automation Engine）

### 现状

当前系统对标签（Tags）的利用：
- **存储**：`repository.UpdateTags(ctx, tenant, bucket, key, tags)` — 标签持久化在 `object_tags` 表。
- **查询**：`ListObjectsByTag(ctx, tenant, bucket, prefix, tagKey, tagValue)` — 按标签过滤对象列表。
- **S3 协议**：`PUT /{bucket}/{key}?tagging`、`GET /{bucket}/{key}?tagging`、`DELETE /{bucket}/{key}?tagging` 已实现。
- **REST 协议**：`PUT /v1/files/{key}/tags` 已实现。
- **批量设置**：`BatchSetTags` 已实现。
- **S3 Lifecycle** 不支持基于标签的过期策略（只支持统一的 `ExpireAfterDays`）。

**严重缺失：**
- **没有规则引擎**来定义「当对象带有标签 `archive=true` 时，在 7 天后转为 GLACIER」或「当对象带有标签 `expire=30d` 时，在 30 天后删除」。
- **没有标签传播**：当存储桶级别设置标签时，不会向下传播到现有对象。
- **没有标签自动添加**：不能根据对象内容/大小/来源自动添加标签。
- **没有标签约束策略**：不能强制要求所有上传都必须包含某些标签（如 `environment`、`department`）。
- **没有标签变更事件**：标签修改不会触发事件（`Bus.Publish` 不包含 `tags` 变更）。

### 为什么需要

**1. 标签是对象存储生命周期管理中最重要的控制平面。**

在 AWS S3 中，标签是以下操作的输入：
- 生命周期规则（`Tag` filter → transition/expire）
- 存储类分析（按标签分组建议降冷）
- 批量操作（按标签筛选数百万对象）
- 跨区域复制（只复制带特定标签的对象）
- S3 Access Points 策略（按标签限制访问）
- 成本分配（按标签拆分账单）

aero-vault 的标签目前**只具备描述功能，没有驱动能力**。

**2. 数据规模增长后无法人工管理。**

当用户有 100 万对象时，不可能查看每个对象的标签来决定生命周期。标签自动化是 S3 用户最常用的运维手段之一——没有它，用户必须自己写脚本轮询 `ListObjects` 并逐个调用 API。

**3. 工程成本低，ROI 极高。**

现有的基础设施已经提供：
- 标签存储和索引
- `reconcile/lifecycle.go` 的周期扫描框架
- `reconcile/job.go` 的分布式 job 执行器
- `JobType` 注册机制
- 集群单例（`cluster.Singleton`）

新增一个标签规则引擎的边际成本非常低——核心是一个规则定义结构体 + 一个扫描循环 + 规则匹配执行器。

### 架构概要

```
Tag-Driven Automation Engine
================================

标签生命周期规则（TagRule）:

```go
type TagRule struct {
    ID         string
    TenantID   string
    Bucket     string      // "" 表示所有 bucket
    TagFilter  TagFilter   // 标签匹配条件
    Actions    []Action    // 匹配后执行的动作列表
    Schedule   string      // cron 表达式，默认每小时
    Enabled    bool
    CreatedAt  time.Time
}

type TagFilter struct {
    Key         string   // 标签键
    Value       string   // "" 表示任意值
    MatchIfAbsent bool   // 匹配缺少该标签的对象（用于强制打标签策略）
}

type Action struct {
    Type string // "transition" | "expire" | "replicate" | "notify" | "tag" | "delete"

    // transition: 目标存储类
    TargetClass      string // "STANDARD_IA" | "GLACIER" | ...

    // expire: 到期后操作
    ExpireAction     string // "soft_delete" | "hard_delete"

    // tag: 自动添加/修改标签
    SetTags          map[string]string
    RemoveTags       []string

    // notify: 发送 webhook
    WebhookURL       string

    // replicate: 跨区域复制
    ReplicationTarget string

    // 所有动作都支持
    AfterDays        int    // 匹配后延迟 N 天执行
    AfterDate        string // 在指定日期后执行
}
```

**执行流程：**

```
reconcile tick (每60分钟)
  │
  ├─ 1. 加载所有活跃的 TagRule
  │
  ├─ 2. 对每条规则:
  │     ├─ 扫描匹配的对象（ListObjectsByTag + TagFilter 评估）
  │     ├─ 对每个匹配对象:
  │     │     ├─ 检查是否已执行（幂等 key = ruleID + objectID + actionType）
  │     │     ├─ 如果满足 AfterDays / AfterDate 条件:
  │     │     │     ├─ 执行 Action
  │     │     │     ├─ 记录执行历史（tag_rule_executions 表）
  │     │     │     └─ 如果 Action.type == "notify" → 发送 Webhook
  │     │     └─ 如果不满足条件 → 跳过
  │     └─ 续页扫描所有匹配对象
  │
  └─ 3. 清理过期的执行历史
```

**规则管理 API：**

```http
POST   /v1/admin/tag-rules                    # 创建规则
GET    /v1/admin/tag-rules                    # 列出规则
GET    /v1/admin/tag-rules/{id}               # 获取规则详情
PUT    /v1/admin/tag-rules/{id}               # 更新规则
DELETE /v1/admin/tag-rules/{id}               # 删除规则
POST   /v1/admin/tag-rules/{id}/execute       # 立即执行规则（手动触发）
```

**扩展：标签约束策略（Tag Compliance Policy）：**

```yaml
# 示例：强制所有上传必须有 environment 和 department 标签
tag_compliance:
  - tenant: acme-corp
    bucket: ""                  # 所有 bucket
    required_tags:
      - environment             # 值必须在 [dev, staging, prod] 中
      - department              # 值必须在 [eng, finance, hr, legal] 中
    forbidden_tags:
      - confidential            # 禁止使用此标签
    action_on_violation: "reject"  # reject | warn | auto-tag-with-default
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| 规则匹配到大量对象（百万级） | 基于 `ListObjectsByTag` 分页扫描，合并 `reconcile` 框架的分批处理 |
| 多个规则匹配同一对象 | 按规则权重/优先级顺序执行；`AfterDays` 从最早匹配的规则开始计算 |
| 规则修改后已匹配对象的状态 | 重新评估——如果对象已执行 `expire`，不能"撤销"；过渡类状态变更可以叠加上去 |
| 标签在规则评估后被修改 | `TagRule` 每次扫描时实时匹配——标签变了就重新评估 |
| 规则执行失败 | 通过 job queue 重试（复用现有`JobType`注册机制），最多 3 次 |
| 执行历史膨胀 | `tag_rule_executions` 保留 N 天（默认 90），过期的由 `RetentionJob` 清理 |
| 对象锁阻止规则执行 | 规则标记为 `failed`，记录原因，跳过该对象 |

### 代码影响范围

| 模块 | 影响 |
|------|------|
| **新增** `internal/tagrule/rule.go` | `TagRule` / `TagFilter` / `Action` 结构体定义、规则评估引擎 |
| **新增** `internal/tagrule/scanner.go` | 按标签扫描对象 + 规则匹配 + 动作调度 |
| **新增** `internal/tagrule/executor.go` | 动作执行器（transition/expire/tag/notify）|
| **新增** `internal/repository/sql_tagrules.go` | `tag_rules` / `tag_rule_executions` 表的 CRUD |
| **修改** `internal/reconcile/job.go` | 在 `Run` 循环中集成 `TagRule` 扫描 |
| **修改** `internal/service/file_features.go` | `SetTags` 时可选触发事件（`EventTagsUpdated`）|
| **修改** `internal/events/bus.go` | 新增 `EventType`：`EventTagsUpdated` |
| **修改** `internal/config` | `TAGRULE_SCAN_INTERVAL` / `TAGRULE_EXECUTION_RETENTION_DAYS` |
| **新增** migration `0026_tag_rules.up.sql` | `tag_rules` 表 + `tag_rule_executions` 表 |
| **添加** `internal/api/rest/admin_tagrules.go` | REST API（CRUD + 手动执行）|
| **添加** SDK 方法 | Admin SDK：`CreateTagRule` / `ListTagRules` / `DeleteTagRule` / `ExecuteTagRule` |

---

## 方向四：🟠 内容感知索引监控与告警（Content-Aware Index Monitoring & Alerting）

### 现状

当前系统已经拥有完整的 AI 索引管道：
- `Extractor` — 从对象提取文本
- `Chunker` — 文本分块
- `Embedder` — 向量化
- `Indexer` — 写入索引（BM25 + 向量存储）
- `Search` — 语义/BM25/混合检索
- `PIIDetector` — PII 检测与脱敏

**严重缺失：**
1. **没有内容变更告警**：当对象被索引后，如果以后同一对象被更新/删除，没有任何通知告诉用户「某文件的内容已变更」。
2. **没有敏感内容发现告警**：`PIIDetector` 在索引时运行，但检测结果没有触发任何外部通知机制。当系统发现包含信用卡号/SSN 的文件时，无法实时通知安全团队。
3. **没有内容搜索监控**：无法设置「当索引中包含关键字 'confidential' 的对象数量超过阈值时告警」或「当新索引的对象包含 'internal-only' 标签时通知管理员」。
4. **没有搜索活动异常检测**：无法监控「特定术语的搜索频率突然增加 N 倍」——这可能是数据泄露的前兆（攻击者确认找到敏感数据）。
5. **没有索引健康度监控**：`indexer_skip_total{reason}` 已记录计数器，但没有基于跳过大比例对象触发告警的阈值规则。

### 为什么需要

**1. 数据安全需要实时发现，而非事后追溯。**

安全事件的时间窗口通常只有几分钟。如果 PII 检测只在日志里写一条记录（当前行为），安全团队可能在数天甚至数周后才注意到。**内容感知告警将检索管道从「查询工具」转变为「主动安全层」。**

**2. 合规场景下的必选项。**

| 法规 | 要求 | 当前状态 |
|------|------|---------|
| GDPR | 发现个人数据泄露后 72 小时内通知监管机构 | ❌ — PII 检测后无告警 |
| PCI DSS | 持卡人数据存储位置必须持续监控 | ❌ — 无法发现新的信用卡数据 |
| HIPAA | PHI 访问必须实时监控和告警 | ❌ — 无监控规则引擎 |
| SOX | 财务相关文档变更必须立即通知 | ❌ — 无内容变更告警 |

**3. 运营效率提升。**

- 当新文档中包含 `confidential`、`proprietary`、`internal-only` 等标记时自动通知安全团队
- 当特定项目目录下的文档频繁更新时通知项目经理
- 当索引器跳过率超过 10% 时提醒运维团队检查提取管道

**4. 工程复用度高。**

所有构建块已经存在：
- `PIIDetector.Scan()` — 检测敏感内容
- `Indexer` — 索引时挂钩
- `Bus` — 事件通知
- `Webhook` — 外部通知
- `reconcile/` — 后台扫描框架
- Prometheus 告警规则 — 已有告警基础设施

### 架构概要

```
Content-Aware Monitoring & Alerting
====================================

告警规则（ContentAlertRule）:

```go
type ContentAlertRule struct {
    ID           string
    TenantID     string
    Name         string
    Description  string
    Enabled      bool

    // 触发条件（OR 关系 — 满足任一即触发）
    Triggers     []AlertTrigger

    // 目标
    Channels     []AlertChannel

    // 限频（防止告警风暴）
    Throttle         time.Duration // 同一规则最小间隔，默认 5 分钟
    CooldownObjects int           // 每 N 个对象触发一次（大场景下）
}

type AlertTrigger struct {
    Type string // "pii_found" | "keyword_match" | "tag_match" |
                // "content_changed" | "pattern_match" |
                // "skip_rate_high" | "search_anomaly"

    // pii_found
    PIICategories []string // "credit_card" | "ssn" | "email" | "phone" | "all"

    // keyword_match
    Keywords      []string

    // tag_match
    TagKey        string
    TagValue      string   // "" = any value

    // pattern_match
    RegexPattern  string

    // content_changed
    ObjectPrefix  string   // 监控特定前缀下的对象变更

    // skip_rate_high
    SkipRatePct   float64  // 阈值百分比，默认 10

    // search_anomaly
    SearchTerm    string
    Threshold     int      // 超过 N 次/小时 触发
}

type AlertChannel struct {
    Type    string // "webhook" | "email" | "slack" | "pagerduty" | "log"
    Webhook string // URL (for webhook/slack type)
    Secret  string // HMAC secret
}
```

**触发路径：**

```
路径 A：索引时同步触发
─────────────────────
Indexer.Run (internal/ai/indexer.go)
  │
  ├─ 1. 文本提取 → Chunk → Embed
  │
  ├─ 2. PII 检测（PIIDetector.Scan）
  │     │
  │     ├─ 如果 PII 发现 → ContentAlertRule 匹配
  │     └─ 如果匹配 → 发送通知（非阻断）
  │
  ├─ 3. 关键字/正则匹配
  │     │
  │     └─ 如果匹配 → 通知
  │
  └─ 4. 跳过计数（IncIndexerSkip）
        │
        └─ 如果跳过率 > 阈值 → 通知


路径 B：周期性扫描（reconcile 框架）
─────────────────────────────────
ContentAlertScanner.Run(ctx)
  │
  ├─ 1. 加载所有活跃的 ContentAlertRule
  │
  ├─ 2. 对每条规则:
  │     ├─ 检查自上次扫描以来的新事件/新对象
  │     ├─ matchTriggers(rule, events)
  │     └─ 如果有匹配 → dispatchNotifications
  │
  └─ 3. 统计搜索频率异常
        ├─ 从 Usage 表读取搜索频率
        └─ 与历史基线对比 → 偏离 N 倍时告警


路径 C：搜索时触发（冷启动/搜索活动监控）
─────────────────────────────────
Search.Query (internal/ai/search.go)
  │
  ├─ 1. 记录搜索活动到 ai_usage
  │
  └─ 2. 异步检查搜索异常规则
        └─ 如果某搜索词频率 > 阈值 → 通知
```

**管理 API：**

```http
POST   /v1/admin/content-alerts                    # 创建告警规则
GET    /v1/admin/content-alerts                    # 列出规则
PUT    /v1/admin/content-alerts/{id}               # 更新规则
DELETE /v1/admin/content-alerts/{id}               # 删除规则
GET    /v1/admin/content-alerts/{id}/history       # 查看触发历史
GET    /v1/admin/content-alerts/stats              # 告警统计
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| 索引期间大量匹配导致风暴 | `Throttle` 最小间隔 + `CooldownObjects` 每 N 对象触发一次 |
| PII 检测在高敏感环境 | PII 匹配后告警到多个通道（Webhook + Slack + PagerDuty） |
| 关键字误报率太高 | 支持正则 + 排除列表 + AI 辅助（用 LLM 验证匹配上下文） |
| 搜索频率基线不可靠 | 搜索异常检测需要在生产运行 ~7 天积累数据后才能激活 |
| 对象删除后又重建 | 如果内容相同，抑制重复告警（用内容 hash 去重） |
| 告警通道不可用 | 写入 `alert_failures` 表，重试机制类似于 `WebhookFailure` |

### 代码影响范围

| 模块 | 影响 |
|------|------|
| **新增** `internal/alerting/rule.go` | `ContentAlertRule` / `AlertTrigger` / `AlertChannel` 结构体 + 规则引擎 |
| **新增** `internal/alerting/matcher.go` | 触发条件匹配器（PII/关键字/正则/标签/跳过率/搜索异常）|
| **新增** `internal/alerting/notifier.go` | 通知分发器（Webhook/Slack/PagerDuty 适配器）|
| **新增** `internal/repository/sql_alerts.go` | `content_alert_rules` / `alert_history` 表的 CRUD |
| **修改** `internal/ai/indexer.go` | 在 `IndexObjectByID` 中插入内容事件（调用 `alerting.Evaluate`）|
| **修改** `internal/ai/search.go` | `Query` 中记录搜索频率（如果 `search_anomaly` 规则激活）|
| **修改** `internal/reconcile/job.go` | 集成 `ContentAlertScanner` 作为定期扫描任务 |
| **新增** migration `0027_alerts.up.sql` | `content_alert_rules` / `alert_history` 表 |
| **添加** `internal/api/rest/admin_alerts.go` | REST API |
| **添加** SDK 方法 | `CreateAlertRule` / `ListAlertRules` / ... |

---

## 方向五：🟠 写入优化层（Write Buffering & Coalescing）

### 现状

当前写入路径：

```go
// internal/service/file_crud.go
func (s *FileService) Put(ctx context.Context, tenant, bucket, key string,
    r io.Reader, size int64, opts PutOptions) (repository.Object, error) {
    // ...
    info, err := s.store.Put(ctx, sk, reader, size, storage.PutOptions{...})
    // ...
}
```

**每次 `Put` 调用都是同步直写后端存储。** 即使对象只有几百字节，也会立即：
1. 打开与存储后端的连接（或本地磁盘文件）
2. 写入整个对象数据
3. 等待存储确认
4. 写入仓库元数据行
5. 更新租户配额
6. 发布事件

**典型生产场景中的问题：**

| 场景 | 问题 |
|------|------|
| 日志采集器每秒写数百个小日志（几百字节） | 每次写入都产生完整的 API 调用和磁盘 IOPS → 严重浪费 |
| CI/CD 管道产生大量构建产物（<1MB） | 大量小文件导致的元数据行争用和存储碎片 |
| 实时事件捕获（IoT 传感器数据） | 高频小对象 → EventBus 被淹没 → 后端子系统的背压 |
| 客户端网络延迟高（RTT > 100ms） | 每次 Put 都要等至少一个 RTT → 吞吐量极低 |

### 为什么需要

**1. 小对象场景下的性能瓶颈是 IOPS 而非带宽。**

本地 SSD 的顺序写入带宽可达 1GB/s+，但随机小文件写入的 IOPS 上限通常在 5K-20K/s。如果每次 `Put` 都产生一次小文件写入，在日志/事件/传感器数据场景中，**IOPS 先于带宽成为瓶颈**。

| 对象大小 | 一次 Put 的 IOPS 成本 | 每秒最大 Put 数（假设 10K IOPS） |
|---------|---------------------|-------------------------------|
| 256 字节 | 1 次后端写入 | ~10,000 |
| 1 MiB | 1 次后端写入 | ~10,000 |
| 256 字节（使用写合并）| 每 10ms 合并一次，批量写入 | ~1,000,000+ |

**2. 跨网络后端（S3/OSS/COS）的延迟放大效应更严重。**

如果后端是 S3：
- 每次 `Put` HTTP 请求 2 RTT（TCP + TLS + 上传）
- 小对象（<128KB）的传输时间相对于 RTT 几乎可以忽略不计
- **合并 N 个小对象为一次批量 Put 可以将延迟降低 N 倍**

**3. 写合并是存储系统的标准优化。**

- RocksDB 的 WAL 写合并
- Linux 内核的 `pdflush`（脏页合并写回）
- Kafka 的 `linger.ms` 批处理
- S3 的 AWS SDK 支持批量 `PutObjects`

**4. 现有架构可以低成本集成。**

当前 `service.Put` 是同步方法。写合并层可以在 `FileService` 之上作为一个透明的缓冲层（类似于 `net/http` 的 `Transport` 之上的连接池）：调用者仍然调用 `Put`，但数据先进入缓冲区，由后台协程批量刷新。

### 架构概要

```
Write Optimization Layer (BufferedWrite Service)
================================================

非侵入式设计：在 FileService 之上包裹一层

┌─────────────────────────────────────────────────────────────────┐
│ 调用者 (REST/S3/WebDAV/MCP handler)                              │
│                                                                  │
│  svc.Put(ctx, tenant, bucket, key, reader, size, opts)           │
│    │                                                             │
│    ▼                                                             │
│  BufferedFileService (透明代理)                                   │
│  ┌─────────────────────────────────────────────────────────┐     │
│  │  PendingWrites 缓冲区                                    │     │
│  │  ┌──────────────────────────────────────────────────┐   │     │
│  │  │ 队列 1: tenant-A / bucket-1                      │   │     │
│  │  │   [key=logs/001, data=[...], size=256]           │   │     │
│  │  │   [key=logs/002, data=[...], size=512]           │   │     │
│  │  │   [key=logs/003, data=[...], size=128]           │   │     │
│  │  └──────────────────────────────────────────────────┘   │     │
│  │                                                         │     │
│  │  刷新策略:                                               │     │
│  │  ■ size_threshold: 达到 N bytes 自动刷新                  │     │
│  │  ■ time_window: 超过 M ms 自动刷新（linger）              │     │
│  │  ■ count_threshold: 积累 K 个对象自动刷新                  │     │
│  │  ■ explicit_flush: 调用 Flush() 手动刷新                  │     │
│  │  ■ sync_flush: 如果对象锁/X-Aero-Flush 头 = now           │     │
│  │                                                         │     │
│  │  刷新协程:                                               │     │
│  │    └─ 从缓冲区批量取出 → 逐次（或批量）写入 storage         │     │
│  └─────────────────────────────────────────────────────────┘     │
│    │                                                             │
│    ▼                                                             │
│  FileService (原有, 同步直写)                                     │
│    ├─ storage.Storage.Put()                                      │
│    ├─ repo.UpsertObject()                                        │
│    ├─ event.Publish()                                            │
│    └─ ...                                                        │
└─────────────────────────────────────────────────────────────────┘
```

**缓冲配置：**

```go
type BufferedWriteConfig struct {
    // 缓冲区大小限制（字节）。达到此大小后强制刷新。默认 1 MiB。
    SizeThreshold int

    // 时间窗口（毫秒）。第一次写入后等待此时间后刷新。默认 100ms。
    LingerMs int

    // 对象数量限制。达到此数量后强制刷新。默认 100。
    CountThreshold int

    // 最大缓冲区容量（字节）。超过此值后写入开始阻塞。默认 64 MiB。
    MaxBufferBytes int64

    // 并行刷新协程数。默认 2。
    FlushWorkers int

    // 暂停的后端（后端故障时自动暂停该后端方向的缓冲写入）
    BackoffBackends []string
}
```

**关键设计决策：**

1. **对象键分区（Object Key Sharding）**：缓冲区按 `(tenant, bucket)` 分区，确保同一租户的同一 bucket 的操作顺序性。

2. **刷新原子性**：缓冲的写入在刷新时**逐对象写入**（不是批量 API——因为 Go 的 storage.Storage 接口不支持多对象 Put）。合并效果体现在：
   - 相同 storage key 前缀的对象在刷新时重排以减少后端 seek
   - 小对象的 HTTP 请求合并（通过 HTTP keep-alive + pipelining）
   - 同一租户的多个写入在同一个事务批次中提交元数据（需要 repo 支持批量 Upsert）

3. **显式屏障**：客户端可以通过 `X-Aero-Flush: now` 请求头或 `Flush()` API 强制刷新缓冲区，确保在该 API 返回时数据已持久化。

4. **崩溃恢复**：缓冲数据如果在内存中崩溃会丢失。这是可接受的——因为 `BufferedFileService` 是**性能优化**而非可靠性层。写回保证由底层 `storage.Storage` 的 `Put` 提供。需要更强保证的客户端可以直接调用原生的 `FileService.Put`（绕过缓冲层）。

5. **事件合并**：缓冲写入刷出时，只产生一次 `EventCreated`（或合并为批量事件），而不是 N 次独立事件——这对 Webhook 消费者更友好。

**API 设计：**

```go
// BufferedFileService 包装 FileService，调用同样的接口方法
type BufferedFileService struct {
    inner  *FileService
    buf    *writeBuffer
    config BufferedWriteConfig
}

// 同 FileService.Put 签名
func (b *BufferedFileService) Put(ctx context.Context, tenant, bucket, key string,
    r io.Reader, size int64, opts PutOptions) (repository.Object, error) {

    // 如果对象超过 SizeThreshold 或是显式同步写入，绕开缓冲直接写
    if size > int64(b.config.SizeThreshold) || isSync(ctx) {
        return b.inner.Put(ctx, tenant, bucket, key, r, size, opts)
    }

    // 小对象：读入缓冲区
    data, err := io.ReadAll(r)
    if err != nil {
        return repository.Object{}, err
    }

    // 等待对象 ready 的 channel
    result := b.buf.Enqueue(tenant, bucket, key, data, opts)
    select {
    case res := <-result:
        return res.obj, res.err
    case <-ctx.Done():
        return repository.Object{}, ctx.Err()
    }
}

// Flush 强制刷新所有缓冲区（用于 shutdown 或显式同步点）
func (b *BufferedFileService) Flush(ctx context.Context) error { ... }
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| 对象 size 超过配置阈值（默认 1MB） | 直接透传到 FileService.Put，不经过缓冲 |
| 缓冲读取失败（`io.ReadAll` OOM） | 大对象已在 SizeThreshold 检查时旁路；小对象风险低 |
| 服务器崩溃导致缓冲数据丢失 | 缓冲是性能优化层，不保证持久化；需要持久性保证的调用者应使用 `Flush()` 同步点 |
| 同一键被多次写入 | 后一次覆盖前一次（在缓冲区中直接替换），减少重复写入 |
| 缓冲区满 | 阻塞调用者直到有空间或上下文取消 |
| Shutdown 时未刷完 | `server.Shutdown` 前调用 `BufferedFileService.Flush(ctx)` |
| 指标 | 新增 Telemetry：`buffered_writes_total`、`buffered_bytes_total`、`buffer_flush_duration_ms`、`buffer_queue_depth` |

### 代码影响范围

| 模块 | 影响 |
|------|------|
| **新增** `internal/writebuf/buffer.go` | `writeBuffer` 结构体：分片队列 + 后台刷新协程 |
| **新增** `internal/writebuf/flusher.go` | 刷新器：从缓冲区取出数据 → 调用 `FileService.Put` |
| **修改** `internal/service/file.go` | 新增 `BufferedFileService` 封装 |
| **修改** `cmd/server/main.go` | 根据配置决定使用 `FileService` 还是 `BufferedFileService` |
| **修改** `internal/config` | `WRITE_BUFFER_SIZE_THRESHOLD` / `WRITE_BUFFER_LINGER_MS` / `WRITE_BUFFER_COUNT_THRESHOLD` / `WRITE_BUFFER_MAX_BYTES` |
| **新增** `internal/api/rest/flush.go` | `POST /v1/flush`（强制刷新） |
| **修改** `internal/telemetry/metrics.go` | 新增缓冲区指标 |

---

## 总结：5 个方向的优先级与建议实施顺序

| # | 方向 | 优先级 | 为什么是这个优先级 |
|---|------|--------|-----------------|
| 1 | **FUSE 文件系统网关** | **P0** — 企业落地硬性要求 | 没有文件系统接口，大量企业工作负载无法迁移到 aero-vault。这是市场覆盖面的瓶颈 |
| 2 | **监管链法证完整性** | **P1** — 合规审计强制 | 没有密码学审计链，无法通过 SOC2/HIPAA/PCI 的严格审计。是合规场景的阻断性缺失 |
| 3 | **标签驱动自动化引擎** | **P1** — 运维效率瓶颈 | 标签只存不用。当用户对象数 >10 万时，没有标签自动化就无法管理生命周期。ROADMAP #9 存储分层的前提条件 |
| 4 | **内容感知告警监控** | **P2** — 安全运营升级 | PII 检测已实现但不告警，大大降低了安全价值。安全团队无法实时发现敏感数据泄露 |
| 5 | **写入优化层** | **P2** — 性能天花板 | 小对象高频写入是生产场景的常见瓶颈。但可通过应用层优化（合并上传）部分缓解，不作为优先 |

**建议实施顺序：** `#1 → #3 → #2 → #5 → #4`
1. 先做文件系统网关（最大市场缺口）
2. 再做标签自动化（为存储分层打基础，与 ROADMAP #9 互补）
3. 监管链法证完整性（合规要求，需要仔细设计哈希链算法）
4. 写入优化层（性能优化，工程成本相对低）
5. 内容感知告警（安全运营，依赖于索引管线的稳定）

---

> **去重声明：** 以上 5 个方向均经过对 `docs/requirements/` 下 32 期既有分析文档（v1–v32，累计 ~160+ 方向、21,000+ 行分析文本）+ `docs/ROADMAP.md` + `docs/CHANGELOG.md` + `docs/TODO.md` 的逐领域 `grep` 验证，确认为 **零实质性架构分析覆盖** 的新方向。
