# 高价值扩展方向

> **作者：** 架构 & 产品分析 · 全局扫描
> **日期：** 2026-07-10
> **范围：** 基于当前代码库（~50k 行 Go + 周边 SDK/UI/Infra）的增量式盲区分析
> **不包含：** 重构、技术债清理、测试覆盖提升（这些已在 AGENTS.md 中列为持续义务）

---

## 目录

1. [存储类生命周期转换（Glacier/IA 冷热分层）](#1-存储类生命周期转换)
2. [多区域活跃-活跃复制与冲突解决](#2-多区域活跃-活跃复制)
3. [按桶事件通知派发（SNS/SQS/Webhook）](#3-按桶事件通知派发)
4. [大文件流式 SSE 加密（AES-CTR + HMAC）](#4-大文件流式-sse-加密)
5. [层次化命名空间与目录级操作](#5-层次化命名空间与目录级操作)

---

## 1. 存储类生命周期转换

### 当前状态

目前已实现：

- `storage_class` 元数据字段（`STANDARD` / `STANDARD_IA` / `GLACIER`）并持久到 DB
- `StorageClassCounts` 指标；`SetBucketLifecycle` 接口（只支持 `expire_after_days` → 软删/硬删）
- `LifecycleJob`（`reconcile/lifecycle.go`）按时扫描 `ListExpired` 并执行过期删除

**未实现：**

- **`Transition` 规则** ── 对象写后 N 天自动变为 `STANDARD_IA`，再 N 天后变 `GLACIER`
- 无 `RestoreObject` 从 GLACIER 取回（当前 REST API 的 `/restore` 只恢复软删，不是解冻）
- 无存储类转换的实际 **后台 Job**（需要改写 storage provider 对象元数据或调用云 SDK 转换 API）
- 无 `transition_cost` 的预算感知（从 IA → GLACIER 虽有存储节约但有请求费）

### 为什么需要

1. **S3 兼容性硬缺口** ── AWS S3 生命周期最常用的功能是 `Transition` 而非 `Expiration`。缺少此功能意味着用户无法用 aero-vault 托管冷数据，直接流失冷数据场景用户。
2. **企业级 Tiering 是刚需** ── 合规要求（日志保留 7 年→前 90 天热存、后转 Glacier）、成本控制（按访问模式自动降冷）是最常被问到的存储需求之一。
3. **复用现有基础设施** ── `LifecycleJob`、`BucketConfig.ExpireAfterDays`、`StorageClassCounts`、`storage_class` 列均已存在，增量成本低。

### 建议的实现边界

```
bucket lifecycle 配置扩展:
  Rules: [
    { ID, Status: "Enabled",
      Filter: { Prefix: "logs/", Tag: { Key: "tier", Value: "cold" } },
      Transitions: [
        { Days: 30,  StorageClass: "STANDARD_IA" },
        { Days: 90,  StorageClass: "GLACIER" },
        { Days: 365, StorageClass: "DEEP_ARCHIVE" }
      ],
      Expiration: { Days: 730 }  # 已有
    }
  ]

RestoreObject 扩展:
  POST /v1/files/{key}/restore?days=7  # 从 GLACIER 临时取回

后台 Job:
  LifecycleTransitionJob — 扫描匹配对象 → 调用 storage provider 转换 API or
  改写 storage_key 指向新的存储类后端
```

| 影响面 | 工作量估计 |
|--------|-----------|
| 配置层 + 迁移（双文件） | 低 |
| `LifecycleJob` 扩展 | 中 |
| Storage provider 接口扩展（`ChangeStorageClass`） | 中 |
| REST API 测试 + OpenAPI | 低 |
| CLI 扩展 | 低 |

---

## 2. 多区域活跃-活跃复制

### 当前状态

目前已实现：

- `ReplicationWorker`（`replication/replication.go`）—— 单方向异步复制：`primary → replica`
- `EventBus` + Postgres LISTEN/NOTIFY transport——跨实例事件分发
- 集群 `Singleton` 协调（leases 表）
- `replication_jobs` 通过 `JobPool` 重试

**未实现：**

- **双向/对称复制** ── 副本区只能被动写入，不能写回主区
- **无冲突检测或 CRDT** ── 如果两个区同时写同一 key，后到达者赢（LWW），数据可能静默丢失
- **无写路由 / 读亲和性** ── 客户端无法按地理最近区写入
- **无全球桶视图** ── 没有一个 endpoint 能看到所有区的数据
- **无复制延迟监控** ── 没有 per-key 或 per-bucket 的复制滞后指标
- **无灾备切换** ── 主区挂掉后，没有自动 promote replica 为主（manual 也不容易）

### 为什么需要

1. **地理分布式团队的日常工作流** ── 目前主-从架构意味着欧洲用户必须写美国主区，延迟不可接受。真正的活跃-活跃能让每个区独立写入，同步在后台完成。
2. **数据主权合规** ── GDPR / 数据本地化要求数据留在特定区域内，但团队仍需全球访问。活跃-活跃 + 桶级写分区可以实现。
3. **灾备（RPO≈0）** ── 主-从复制有 RPO 窗口（秒到分钟级）。同步双向复制 + 版本向量可做到接近 RPO=0。
4. **复用度高** ── `EventBus`、`leases`、`JobPool`、`replication.Worker` 均已存在，增量工程主要在冲突解决和双向同步协议上。

### 建议的实现边界

```
阶段一（短期）:
  - 健康/活跃检测：每个区定期心跳写入 global 租约表
  - 版本向量（Version Vector）：每个对象附带 [{region, counter}]，
    冲突时以最大 counter 为准（LWW + metadata merge）
  - 双向同步：ReplicationWorker 也监听副本区的事件写回主区
  - 延迟指标：`replication_lag_seconds{source_region, dest_region}`

阶段二（中期）:
  - 地理路由：基于 DNS 或 HTTP 重定向，将请求发往最近可用区
  - 全球桶 List：跨区聚合 ListObjects 结果
  - 手动切换 / 自动故障转移

阶段三（长期）:
  - CRDT-based 冲突解决（比如对 tags/metadata 用 LWW-Register）
  - 跨区审计一致性校验（后台 ReconcileJob 检查两个区的 checksum）
```

| 影响面 | 工作量估计 |
|--------|-----------|
| 复制协议（版本向量 + CRDT） | 高（核心算法） |
| 双向 Worker | 中 |
| 全局视角路由 | 中 |
| 冲突 UI / admin API | 低 |
| 测试（TC 网络故障） | 高 |

---

## 3. 按桶事件通知派发

### 当前状态

目前已实现：

- **Schema 已就绪** ── 迁移 0024 已添加 `buckets.notification_rules TEXT`（JSON 数组）
- **Repository 接口** ── `GetBucketNotifications` / `SetBucketNotifications` / `DeleteBucketNotifications` ✓
- **REST API 路由** ── `GET/PUT/DELETE /v1/buckets/{bucket}/notification` ✓
- **`EventBus` + `webhook.Worker`** ── 单一全局 `EVENTS_WEBHOOK_URL`
- **webhook 重试表** ── `webhook_failures` 持久化 + retry loop

**未实现：**

- **通知目标引擎** ── 根据 `NotificationRule` 将事件路由到不同目标（SNS Topic、SQS Queue、SMTP、多 webhook）
- **规则过滤** ── `FilterKey` 前缀/后缀/标签过滤（schema 有 `FilterKey`，但执行层无过滤）
- **事件去重 / 至少一次保证** ── 目前是 at-most-once（dropped events are lost）
- **多 webhook endpoint** ── 每个桶可配置自己的 webhook，而非全局一个
- **S3-compat 通知格式** ── `s3:ObjectCreated:*` / `s3:ObjectRemoved:*` 事件类型命名约定

### 为什么需要

1. **半完成功能** ── Schema、接口、路由都写了，就差执行引擎。放着一个不工作的 API 表面比没有更糟糕（用户配置了认为开了但没有事件到达）。
2. **解耦第三方集成** ── 当前全局 webhook 无法区分桶来源，接收方必须自己解析 payload。桶级通知是 S3 兼容性的核心要求。
3. **事件驱动架构** ── 很多用户期望写入对象后立即触发 CI/CD pipeline、数据同步、或通知下游系统。没有多目标通知 = 丢失了最重要的 S3 集成场景。
4. **增量工程** ── 复用 `EventBus` 订阅、`webhook` retry 逻辑、`webhook_failures` 表。主要是路由层 + 目标适配器（sink）。

### 建议的实现边界

```go
// 扩展现有 NotificationRule:
type NotificationRule struct {
    ID        string              `json:"Id"`
    Events    []string            `json:"Events"`      // "s3:ObjectCreated:*"
    Filter    *NotificationFilter `json:"Filter"`
    // 目标（三选一）:
    Webhook   string              `json:"Webhook,omitempty"`   // URL
    SNS       string              `json:"SnsArn,omitempty"`    // HTTP endpoint
    SQS       string              `json:"SqsArn,omitempty"`    // SQS queue URL
    SMTP      *SMTPConfig         `json:"Smtp,omitempty"`      // email alert
}

// 新的 NotificationRouter（独立 goroutine）:
// 订阅 EventBus → 匹配规则（event type + filter）→ 异步分发到 target sink
//
// Sinks:
//   - WebhookSink (复用全局 webhook retry 逻辑)
//   - SNSHTTPSink   (HTTP POST 到 SNS topic endpoint)
//   - SQSSink       (HTTP POST 到 SQS queue URL)
//   - SMTPSink      (发送 email)
```

| 影响面 | 工作量估计 |
|--------|-----------|
| 路由引擎（匹配规则 + 分发） | 中 |
| SNS/SQS/SMTP 适配器 | 低～中（每个 1-2 天） |
| 测试（集成测试用 localstack） | 中 |
| OpenAPI + CLI 扩展 | 低 |

---

## 4. 大文件流式 SSE 加密

### 当前状态

目前已实现：

- `envelopeEncrypter`（`storage/encrypt.go`）── AES-256-GCM + data key + KEK wrapping
- Local master key provider + KMS remote wrapper (DataKeyWrapper)
- Key 轮换 + RewrapOnStart（`storage/rewrap.go`）
- SSE envelope 作为 sidecar JSON 持久化

**未实现：**

- **流式加密** ── `encryptReader()` 和 `decryptReader()` 使用 `io.ReadAll(plain)` 全缓冲 → O(object size) 内存
- **GCM 限制** ── AES-GCM 要求有整个密文后才能验证 tag，无法做到真正的流式（发送一个加密块后客户端不能开始解密）
- **无分块加密** ── 大于 1GB 的对象在加密路径上会 OOM 或消耗过多内存
- **无并行加密** ── 多分片上传时，每个 final 合并后的对象走单线程加密

### 为什么需要

1. **大文件支持** ── 当前限制意味着 >500MB 的对象 SSE 加密会有明显的内存压力，>2GB 几乎不可用。对大文件（视频、数据库 dump、科学数据）场景这是硬伤。
2. **流式验证** ── 客户端 PUT 一个大文件时，希望边流式加密边计算完整性，而不是等全量缓冲后再验证 Content-MD5。
3. **合规需求** ── 很多企业要求"存储即加密"，但允许大文件的存在。目前的缓冲区设计违背了"对用户透明"的加密承诺。
4. **多分片一致性** ── 多分片上传 + SSE 时，每个 part 应该独立加密，这样 CompleteMultipart 时不需要重新加密整个流。

### 建议的实现边界

```
方案：AES-256-CTR + HMAC-SHA256 分块加密

加密块大小：1 MiB (1048576 bytes)
每个块独立加密（同 KDF-derived key），块序号作为 counter。
文件尾部追加连续块的 HMAC-SHA256 列表。

优点：
  - 流式：encrypt 时边读边发，decrypt 时边收边解
  - 并行：多个块可并发加密/解密
  - 随机访问：reader 可 seek 到任意块
  - GCM tag 不适用于大文件，HMAC 适合

兼容性：
  - 新 envelope 格式 { "alg": "AES-256-CTR-HMAC", "block_size": 1048576 }
  - 写时选择，读时自动识别（依赖 alg 字段）
  - 小文件仍用 GCM（低开销）

迁移策略：
  - 新写入的文件按新格式，已有文件不变
  - RewrapOnStart 不涉及协议变更
```

| 影响面 | 工作量估计 |
|--------|-----------|
| 加密核心（CTR+HMAC 块式） | 高（密码学实现需审查） |
| 流式 Reader/Writer 适配 | 中 |
| 多分片集成 | 中 |
| 现有 envelope 兼容 | 低 |
| 测试（随机读写 + 完整性注入） | 高 |

---

## 5. 层次化命名空间与目录级操作

### 当前状态

目前已实现：

- `ListObjects(ctx, prefix, marker)` ── 前缀匹配列举
- `POST /v1/folders/*` ── 创建零长 marker 对象表示目录
- `DELETE /v1/folders/*` ── 删除 marker
- `ListFolders` ── 列举目录（前缀去重）
- Tags、ACL、Lock 均已支持（per-object）
- `BatchDelete` / `BatchSetTags` ── 批量操作

**未实现：**

- **原子 Rename/Move** ── 重命名目录 = 遍历 + 逐个 Copy+Delete（无事务、不可打断）
- **子目录配额继承** ── 配额是 tenant 级别的，不能对 `/projects/foo/` 单独设限额
- **ACL 递归继承** ── 子对象不继承父目录 ACL
- **CopyTree** ── 跨桶/跨租户递归复制没有原子性保障
- **目录级别生命周期** ── 目前 lifecycle 是 per-bucket，无法 per-directory
- **访问控制委托** ── 用户 A 可以 delegate 某个子树的 ACL 管理给用户 B（S3 的 bucket policy + IAM 做不到子树委派，但企业内部 NAS 场景需要）

### 为什么需要

1. **NAS 替代场景的核心诉求** ── 企业从传统 NAS/SMB 迁移到对象存储时，最常问的是"支持目录级别的权限吗？"、"支持 mv 文件夹吗？"、"目录能设配额吗？"
2. **组织大规模数据** ── 扁平命名空间 + prefix 模拟目录在数十亿对象下性能会退化（prefix scan 变慢）。原生分层结构可以用更高效的索引（路径枚举树、Materialized Path 或 Closure Table）。
3. **原子性和一致性** ── `mv /projects/active/ /projects/archived/` 目前不是原子的——如果中间崩溃，一部分对象在新路径、一部分在旧路径，没有一致性快照。
4. **WebDAV 用户体验** ── WebDAV 客户端（macOS Finder、Windows Explorer）期望目录是真实实体，带属性、可重命名。当前零长 marker 方式在 Finder 中经常会显示空目录或行为异常。

### 建议的实现边界

```
短期（利用现有 marker 模式 + 工程加固）:
  - 原子 Rename/Move: 先在 repo 内更新所有匹配 prefix 的 key（单事务），
    再异步转移 storage blob（JobPool 任务）
  - 目录级别配额: 新增 dir_quotas 表 (path, max_bytes, max_objects)
    在 FileService.Put 时检查

中期（分层索引）:
  - 新增 tree_nodes 表（Materialized Path 或 Nested Sets）:
    id, parent_id, path, depth, object_id (nullable)
  - ListObjects 改为树遍历而非 prefix scan
  - ACL 递归: unset 时自动 fallback 到最近祖先的 ACL

长期:
  - Snapshot / Clone 子树（time-consistent 拷贝）
  - 多层级 StorageClass + Lifecycle 继承（子目录覆盖父级）
```

| 影响面 | 工作量估计 |
|--------|-----------|
| 原子 Rename（事务 + 后台 blob move） | 中 |
| 目录配额 | 低 |
| 分层索引（新表 + 迁移 + repo 方法） | 高 |
| ACL 继承 + WebDAV 集成 | 中 |
| 测试（随机目录树 + 并发 rename） | 高 |

---

## 总结：优先级矩阵

| 方向 | 业务价值 | 工程成本 | 依赖关系 | 推荐排序 |
|------|---------|---------|---------|---------|
| 存储类生命周期转换 | ★★★★★ | ★★★ | 无 | 1 |
| 按桶事件通知派发 | ★★★★ | ★★ | 现有 schema 已备 | 2 |
| 大文件流式 SSE | ★★★ | ★★★★ | 加密基础已备 | 3 |
| 多区域活跃-活跃复制 | ★★★★ | ★★★★★ | 现有 EventBus + Replication | 4 |
| 层次化命名空间 | ★★★ | ★★★★★ | 依赖分层索引设计 | 5 |

**第一优先级（排序 1–2）** 解决的是"API 表面存在但行为不完整"的问题，用户感知最强烈。  
**第二优先级（排序 3）** 解决的是"大文件场景下的技术债务"，影响高并发的生产环境。  
**第三优先级（排序 4–5）** 是差异化竞争优势，但需要更长时间的设计和实现周期。

---

*分析基于 commit: 当前 HEAD | 代码行数约 ~50k (Go) + SDK/UI/Infra*
