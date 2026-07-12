以下是我的架构分析：

---

# 架构师分析：5 个全新扩展方向的深度评估

## 1. 架构评估

### 1.1 当前架构的优势

aero-vault 的当前架构具有几个值得肯定的设计品质：

**分层清晰且职责单一。** `Protocol Adapter → FileService → Storage + Repository` 三层模型是经过验证的经典模式。每个协议适配器（REST、S3、WebDAV、MCP）都是 **200-500 行的薄层**，不包含业务逻辑——这正是接口隔离原则的落地。`FileService` 是所有协议的唯一下游，确保跨协议行为一致性。

**事件驱动解耦。** 写入路径的 `handler → FileService → storage + repo → events → bus → subscribers` 流水线使用异步事件处理副作用（索引、复制、Webhook），使主写入路径不被阻塞。这和 Domain Events 模式完全一致，也是后续扩展的方向一/三/四的理想集成点。

**Opt-in 安全默认。** AI、复制、WebDAV、集群功能全部 flag-gated。默认 `nil embedder`/`nil llm` 不破坏 core CRUD——这在 SaaS 产品演进的「基线路径不可回归」原则中是一个成熟的设计决策。

**存储和仓库接口明确。** `storage.Storage` 仅有 ~12 个方法，`repository.Repository` 按领域拆分（`sql_objects.go`、`sql_chunks.go`、`sql_tags_acl.go` 等），避免 God 接口。4 个存储后端 + 2 个数据库后端的实现证明了接口的抽象能力。

### 1.2 局限性

在识别出 5 个空白方向后，我从架构层面看到更深层的系统局限：

**协议覆盖存在根本性缺口。** 四个现有协议（REST、S3、WebDAV、MCP）都是「API 式」接口——调用者需要先 auth、构造请求、解析响应。没有任何一种协议提供 **VFS（Virtual File System）语义**。这意味着所有 POSIX 依赖的生态工具（编译器、rsync、CI/CD runner、Kubernetes CSI、IDE）都天然不可用。这不是功能缺失，而是**架构层面的协议层抽象不完整**——`Protocol Adapter` 只覆盖了「请求-响应」范式，没有覆盖「挂载-访问」范式。

**元数据是「描述型」而非「驱动型」。** 当前 Tags 存储在 `object_tags` 表，通过 `ListObjectsByTag` 查询，但其生命周期到此为止——没有标签能触发任何自动化行为。这是一个架构层面的「数据 → 动作」断层。**数据平面的标签没有连接到控制平面的规则引擎。**

**安全检测管线的「检测完成」即结束。** `PIIDetector.Scan` 运行在索引路径中，检测到信用卡、SSN 等敏感内容后仅返回结果给调用者——不产生事件、不触发告警、不推送给安全团队。从架构角度看，这是**检测 → 响应链的断裂**。`PIIDetector` 是一个分析器，不是一个安全控件的完整实现。

**审计信任模型为 implicit trust。** audit_log 表与业务数据在同一个数据库实例中，这意味着任何拥有 DB 权限的人可以无声修改审计记录。架构层面缺乏**独立信任锚点**——没有跨系统哈希链、没有外部锚定、没有防篡改机制。

**写入路径缺乏任何缓冲/合并抽象。** 每次 `Put` 都穿透到存储后端。这在单一后端模式下 OK，但在 multi-region 复制、QoS 控制、IOPS 优化等场景中缺乏杠杆点。写入路径从 handler 到 storage 是一条直通的、无状态的链条——没有中间缓存层。

### 1.3 关键设计决策评估

| 决策 | 评价 | 分析 |
|------|------|------|
| 单一 FileService 入口 | ✅ 正确 | 确保跨协议一致性，但也成为写入路径的唯一瓶颈——写入缓冲层必须包装它 |
| 事件总线异步处理 | ✅ 正确 | 为方向三（标签自动化触发动作）和方向四（告警）提供了天然的集成点 |
| `storageKey = path.Join(tenant, bucket, key)` | ✅ 正确 | 与 FUSE 的虚拟目录模型天然一致——方向一可以直接复用它作为路径映射 |
| SQLite 默认 | ✅ 正确 | 单节点部署0配置。但注意 SQLite 的并发写入限制——方向五的写缓冲可以减少并发写入压力，间接提升 SQLite 环境下的吞吐 |
| Middleware 链固定顺序 | ✅ 正确 | RequestID→CORS→Auth→Tenant→RateLimit→OTel→Recoverer→AccessLog —— 但方向一的 FUSE 不走 HTTP，不经过这个链，需要一个独立的 auth 策略 |
| PII 检测在索引路径运行 | ⚠️ 有缺陷 | PII 检测是同步于索引流程的——索引器跳过（skip）的对象不会经过 PII 扫描。对于 PDF/图片等无文字内容对象，PII 检测覆盖不到 |

### 1.4 架构债务

1. **方向四的实际债务等级是 P1 而非 P2**——PII 检测不告警不是「遗漏的功能」，而是一个**暴露的安全漏洞**。攻击者可以通过 API 上传信用卡数据，系统会默默地索引它并使其可通过搜索检索，但安全团队永远不会收到通知。这是 `detection without response` 的架构缺陷。
2. **审计模型是隐式单点信任**——数据库管理员可以篡改 `audit_log`。对于任何面向企业的产品，这是架构信任模型中的薄弱环节。
3. **无写入缓冲抽象意味着所有 QoS/限流/背压机制都在更上层实现**——现有 ratelimiter 是基于请求数的，不是基于吞吐量的。方向五的缓冲层可以自然地成为写入 QoS 的控制点。

---

## 2. 扩展方向

### 方向 A：FUSE/POSIX 文件系统网关（P0）

**为什么需要：** 这是生态系统兼容性的天花板。没有它，整个 POSIX 生态的应用（CI/CD、rsync、Kubernetes CSI、编译器、IDE）都无法接入。与 WebDAV 不同，FUSE 提供内核级缓存（`page cache`、`dentry cache`），延迟从 ~5-50ms（WebDAV）降至 ~50μs，差距 2-3 个数量级。

**核心挑战与技术难点：**

1. **目录的虚拟化模型。** aero-vault 的实际存储是扁平的 `path.Join(tenant, bucket, key)` 结构，没有目录对象。FUSE 需要 `mkdir`、`rmdir`、`readdir`，全都必须映射到 prefix listing + 零字节标记对象的组合。这是「基于 prefix 的 filesystem」模式（goofys/s3fs）的核心设计问题。如果使用零字节标记对象（如 `.aero-dir-marker`），则存在「两个客户端同时试创建同一目录」的竞态，需要 `If-None-Match` 条件写入来保护。

2. **Inode 编号的稳定性。** FUSE 要求每个文件/目录有一个唯一的 `Inode` 号，且同一文件在不同挂载会话中应返回相同的 inode。aero-vault 中没有 inode 概念——需要从 `(tenant, bucket, key)` 三元组单向哈希出 inode（如 `fnv-1a` 或 `xxhash` 映射到 64 位）。但这存在哈希碰撞风险，虽然概率极低，但碰撞会导致两个文件共享同一个 inode。**建议方案**：将 `(tenant, bucket, key)` 的 SHA256 前缀 64-bit 作为 inode，并设计 fallback：如果 stat 碰撞，在 repo 中记录冲突映射。

3. **ReadDir 的分页和缓存。** 一个 prefix 下可能有上百万 key（`/logs/2026-07-12/`）。Linux `getdents` 系统调用有单次返回限制（约 1M entries）。需要实现分页读取 + TTL 缓存（`attr_timeout` + `entry_timeout` 控制）。对于 `tree` 之类遍历大量目录的工具，缓存策略直接影响用户体验。

4. **并发写入锁。** FUSE 支持 POSIX `flock`/`fcntl` 锁。aero-vault 当前的 `If-Match` + `If-None-Match` 可以实现乐观并发控制，但 POSIX 锁是强制锁。需要评估是否实现 FUSE 的 `Locks` 回调——如果实现，锁状态存在内存中（重启丢失），需要类似 etcd/advisory lock 的分布式锁机制。

5. **Truncate 操作。** 当前 `FileService` 不支持截断文件——`Put` 总是全覆盖写入。FUSE `Truncate(fd, offset)` 需要通过 `Put` 重建文件来模拟，对大文件效率极低。**建议**：在 Storage 接口中增加 `Truncate(key, size)` 方法——对 local 后端可以直接 `ftruncate`，对 S3 后端复制到新文件。

**预期的架构变更：**

```
内部影响：
  NEW  internal/fuse/           ← FUSE 挂载守护进程
  NEW  internal/fuse/dir.go     ← 虚拟目录管理器
  NEW  internal/fuse/inode.go   ← (tenant,bucket,key) ↔ inode 映射
  MOD  internal/service/file.go ← 新增 CreateDirectory, DeleteDirectory, Truncate, Symlink, HardLink
  MOD  cmd/server/main.go       ← 新增 `fuse` 子命令
  MOD  internal/config          ← FUSE_* 配置项

外部影响：
  NEW  aero-vault fuse mount /mnt [-o tenant=xxx,token=xxx]

边界状态：
  - 目录不存在但创建文件 → 隐式创建虚拟目录（已有 FileService.Put 支持）
  - 硬链接 → 同一 storage_key 多 metadata 行 + 引用计数
  - 符号链接 → 存储为 metadata 中的 symlink_target 字段
  - 文件重命名 → Copy + Delete（符合当前实现，但需要 FUSE 层面等待异步操作完成）
```

**对现有系统的影响：** 低破坏性。FUSE 是新增的 protocol adapter，不影响现有协议。`FileService` 接口需要扩展几个方法，但不改变现有方法的签名。最重要的问题是 FUSE 的写路径是否需要经过写入缓冲层（方向五）——我的建议是：FUSE 写应直接经过 BufferedFileService，因为文件系统层面的写入模式天然就是小对象高频写入。

### 方向 B：标签驱动自动化引擎（P1）

**为什么需要：** Tags 是对象存储的「控制平面」。没有自动化，标签只是昂贵的元数据。标签自动化和 Roadmap #9 的存储分层强依赖——只有通过标签规则将对象分类并自动过渡到不同存储类，分层才有实际意义。

**核心挑战与技术难点：**

1. **规则评估的性能。** 当有数十万对象和数百条规则时，全量扫描不可行。需要建立倒排索引（tag key → tag value → object IDs 的 bitmap），基于 tags 的规则匹配可以在 O(1) 时间内找到候选对象集。**可以采用 roaring bitmap 来压缩存储对象 ID 集合。**

2. **规则冲突解决。** 一个对象可能匹配多条规则——一条说 7 天后转 GLACIER，另一条说 14 天后删除。需要定义冲突解决策略：最严格规则优先 / 最近创建规则优先 / 显式优先级字段。建议使用显式优先级（priority int），低值优先执行。

3. **幂等执行。** 每条规则对每个对象只应执行一次。需要 `(rule_id, object_id, action_type)` 唯一索引 + 执行记录表。`reconcile` 框架的扫描周期应跳过已执行的组合。

4. **`MatchIfAbsent` 的评估成本。** 当规则监控「缺少特定标签」时，不能通过索引快速定位——需要扫描所有对象的标签。对 100M+ 对象的大型部署，这需要增量扫描（只检查自上次扫描以来新创建/修改的对象）。

**预期的架构变更：**

```
  NEW  internal/tagrule/                 ← 规则引擎
  NEW  internal/tagrule/matcher.go       ← 匹配器（支持 TagFilter 评估）
  NEW  internal/tagrule/executor.go      ← 动作执行器
  NEW  internal/repository/sql_tagrules.go
  MOD  internal/reconcile/job.go         ← 集成 TagRule 扫描
  MOD  internal/events/bus.go            ← 新增 EventTagsUpdated 事件类型
  NEW  internal/api/rest/admin_tagrules.go
```

**对现有系统的影响：** 中等。标签变更需要触发事件（当前缺失），`reconcile` 循环需要增加一个新扫描任务。其他模块完全不受影响。`TagRule` 是纯新增子系统，通过 `reconcile` 框架与现有后台任务集成。

### 方向 C：内容感知告警（调整评级：P1）

**为什么从 P2 升至 P1：** 这不是一个「运营升级」功能，而是一个**安全漏洞修复**。PII 检测管线已经存在，但它只产生日志——一个可以搜索信用卡号的系统却不通知安全团队，这构成了 "detection without response" 的安全架构缺口。攻击者可以通过 API 上传含 PII 的文件，系统将其索引为可搜索的 chunk，安全团队永远不会知道。

**核心挑战与技术难点：**

1. **告警风暴抑制。** 当数百个文件同时包含信用卡号时，不能发送 500 条告警。需要按 `Throttle`（最小间隔）+ `CooldownObjects`（每 N 对象）做速率限制，还需要 `AlertGrouping`（同一规则、同一 PII 类别的告警合并为一条）。

2. **内容去重。** 同一文件被重新上传（版本更新）不应触发重复告警。应使用 `content_hash` 作为去重键，在 `alert_history` 中记录已告警的内容指纹。

3. **PII 误报控制。** `PIIDetector` 可能产生误报（如 `credit_card` 规则匹配了产品型号编号）。告警系统应提供「抑制规则」：`AlertSuppression{ TagKey + TagValue + ExpiresAt }`，允许用户手动抑制误报。

**预期的架构变更（最小化方案）：**

```
  NEW  internal/alerting/rule.go         ← 规则结构体
  NEW  internal/alerting/matcher.go      ← PII/关键字/正则匹配器
  NEW  internal/alerting/notifier.go     ← 通知分发器
  MOD  internal/ai/indexer.go            ← 在索引完成后调用 alerting.Evaluate
  MOD  internal/reconcile/job.go         ← 集成定期扫描
  NEW  internal/api/rest/admin_alerts.go
```

**PII 快速上线子集（~2 天工程）：**

最简可行路径：在 `internal/ai/indexer.go` 的 `IndexObjectByID` 中，PII 检测后直接调用 webhook——如果 `EVENTS_PII_WEBHOOK_URL` 配置了，将 PII 检测结果 POST 出去。不需要规则引擎，不需要管理 API，只需要一个 webhook URL 配置项。这可以作为一个快速安全修复在 2 天内上线。

### 方向 D：监管链与法证完整性（P1，可选模块）

**为什么需要：** 金融/医疗/法律合规的硬性要求。但如交叉验证所述，其价值与目标客户群直接相关。对于中小团队和非受监管行业，ROI 偏低。`P1` 评级但应以 **可选模块** 实现（默认关闭）。

**核心挑战与技术难点：**

1. **哈希链性能。** 每个写入操作需要在 `custody_entries` 表中插入一行，且链式校验需要读前一条的 `entry_hash`。在单次写入路径上增加一次 DB 读写。对高频写入场景（每秒数千个对象），CoC 写入可能成为瓶颈。**解决方案**：使用批量锚定模式——只在达到批次大小（如 1000 条 entry）或时间窗口时才链接哈希。批次内的 entry 通过 Merkle 树聚合。

2. **genesis 区块的创建。** 升级前已存在的数十万对象没有 CoC 记录。需要在迁移脚本中为每个现有对象创建 genesis entry，但全量扫描耗时长。**建议**：延迟创建——第一次访问/修改时创建 genesis entry，而非升级脚本中全量扫描。

3. **外部锚定的选择。** Level 2（S3 WORM）、Level 3（RFC 3161 TSA）、Level 4（区块链）各有取舍：
   - S3 WORM：成本最低，但需要信任 S3 bucket 的 WORM 配置（配置错误则保护失效）
   - TSA：RFC 3161 标准，中低成本，时间戳可独立验证，但不能证明「谁拥有数据」
   - 区块链（如 Ethereum/Polygon）：最高独立验证度，但成本高（Gas fee）+ 延迟不可控

**建议架构决策**：从 Level 1（双表锚定）入手，Level 2（S3 WORM）作为默认增强，Level 3/4 作为插件式实现。不要在初始版本中把 TSA/区块链绑定到核心路径。

### 方向 E：写入优化层（P2）

**为什么需要：** IOPS 优化。在小对象场景下（日志、事件、IoT 传感器），IOPS 比带宽先成为瓶颈。但如交叉验证所述，客户端可以通过 SDK 批量上传部分缓解，因此 P2 合理。

**核心挑战与技术难点：**

1. **数据丢失窗口。** 缓冲在内存中的数据在进程崩溃时丢失。这是一个明确的权衡——BufferedFileService 不是持久化层。需要清晰的文档和配置项，让调用者决定是否使用缓冲。**对 WORM/对象锁 bucket 应 always 绕过缓冲。**

2. **事件时机。** 缓冲合并写入仅在 flush 时触发事件。如果缓冲中的数据在 crash 后丢失，flush 不会发生，事件也永远不会发送。这意味着因果依赖事件的消费者可能数据不一致。**建议**：事件在 buffer enqueue 时预创建（pending 状态），flush 成功后标记 confirmed。

3. **存储后端不支持批量 Put。** 当前的 `Storage` 接口只有单对象 `Put`。合并效果来自 HTTP keep-alive 重用和元数据批量提交（repo 层面支持 `BatchUpsert`）。需要评估 BatchUpsert 的性能收益是否超过实现成本。

**建议架构决策：**

- 使用装饰器模式包装 `FileService`——`NewBufferedFileService(inner FileService)`，不对 `FileService` 接口本身做任何修改
- 缓冲区使用 LRU 按 `(tenant, bucket)` 分区
- 绕过策略：size > threshold、`X-Aero-Flush: now` header、对象锁 bucket、WORM bucket
- 在 `server.Shutdown` 的 graceful period 中自动 flush

---

## 3. 接口设计建议

### 3.1 关键模块接口原则

**FUSE 网关的接口设计原则：**

- FUSE 操作应映射为 `FileService` 方法调用，不跳过任何业务逻辑（quota、versioning、event emission）。
- `ReadDir` 通过 `storage.List(prefix)` + 虚拟目录折叠实现，但应缓存 `(prefix, offset, limit) → entries` 的结果，缓存 TTL 由 `attr_timeout` 配置控制。
- Inode 映射通过 `inode.Map` 接口抽象，允许未来切换映射策略（hash-based → sequence-based）。

```go
// 推荐接口抽象
type InodeMapper interface {
    Inode(tenant, bucket, key string) (uint64, error)     // 文件
    DirInode(tenant, bucket, prefix string) (uint64, error) // 目录
    Lookup(inode uint64) (Key, error)  // 反向查找（for getattr）
    Flush() error                      // 持久化 inode 缓存（可选）
}
```

**Tag 规则引擎的接口设计原则：**

- 规则存储使用与 `reconcile` 框架相同的 repository 模式——规则 CRUD 走 repo 层，执行器走 `JobType` 注册机制。
- `TagFilter.MatchIfAbsent` 的实现依赖增量扫描——只扫描自上次评估后新创建/修改的对象，而非全量扫描。

**告警引擎的接口设计原则：**

- 告警引擎不直接依赖 `PIIDetector`——它消费 `AlertEvent` 事件。`PIIDetector` 在索引管线中包装为一个事件生产者。
- 通知通道通过 `Notifier` 接口抽象，允许未来添加 Email/Slack/PagerDuty 适配器。

### 3.2 是否需要新的抽象层

**是的，需要引入 `BufferedWriteService` 作为装饰器。** 当前 `FileService` 是同步直写。引入一个在 `FileService` 之上包裹的 `BufferedFileService`，实现相同的接口签名，可以对调用者透明。对 FUSE 网关而言，写入可以直接通过 `BufferedFileService`，对大量小文件写入有显著优化。

**不需要引入新的数据层抽象。** `storage.Storage` 和 `repository.Repository` 已经足够抽象。五个方向中，只有方向二（CoC）需要新的 repository 接口（`CustodyEntryStore`），方向三（tag automation）需要扩展已有的 `reconcile` 框架。

### 3.3 向后兼容性

所有五个方向都应遵循以下兼容模式：

1. **默认关闭。** FUSE 网关不上线，Tag 规则不配置，CoC 模块默认不激活——现有安装完全不受影响。**零迁移成本。**
2. **配置门控。** 每个方向有独立的 Enable/Disable 配置标志（`FUSE_ENABLED`、`TAGRULE_ENABLED`、`CHAIN_OF_CUSTODY_ENABLED` 等）。
3. **数据库迁移非破坏性。** 新增的表（`custody_entries`、`tag_rules`、`content_alert_rules`）都是附加（append-only）写入，不修改现有表结构。迁移文件版本号延续现有序列。
4. **`nil`-safe defaults。** 如果 CoC 模块未初始化，`FileService` 中的 `c custody.Custodian` 字段为 `nil`，`Record()` 调用用空检查跳过，不阻塞主路径。

---

## 4. 技术选型

### 4.1 FUSE 库选择

| 方案 | 优势 | 劣势 | 推荐度 |
|------|------|------|--------|
| `bazil.org/fuse` | 最成熟、社区最大、文档多、示例丰富 | GitHub 活跃度低；Go 1.18+ 兼容性需验证；Windows 支持弱 | ⭐⭐⭐ |
| `github.com/jacobsa/fuse` | 活跃维护（Google gcsfuse 使用）、支持 WinFsp、性能好、有 `memfs` 测试工具 | 学习曲线略高、Go 接口设计较「低级」 | ⭐⭐⭐⭐ |
| `github.com/hanwen/go-fuse/v2` | 活跃开发、原生 Go、性能良好、支持 macOS/Windows via WinFsp | 社区略小于 bazil | ⭐⭐⭐⭐⭐ |

**推荐：`github.com/hanwen/go-fuse/v2`**（v2）。理由：
- 活跃维护（2026 年仍有提交）
- 原生 Go，无 C 依赖
- 内建 `memfs` 用于单元测试
- 支持 macOS + Windows via WinFsp
- 接口更友好，适合映射到 `FileService` 调用

### 4.2 规则引擎 DSL

| 方案 | 优势 | 劣势 | 推荐度 |
|------|------|------|--------|
| 内置 JSON rules（如 AWS 风格） | 0 依赖、序列化简单、UI 友好 | 表达能力有限、复杂条件需要 if-then 展开 | ⭐⭐⭐⭐ |
| `expr-lang/expr` | CEL-like 语法、类型安全、Go 原生、高性能 | 新增依赖、逃逸分析复杂度增加 | ⭐⭐⭐ |
| `google/cel-go` | CNCF 项目、强类型、K8s 在用 | 1.8MB 二进制增量、学习曲线陡 | ⭐⭐ |
| Webhook + 外部决策 | 极简、灵活、不增加二进制大小 | 延迟 + 外部依赖 + 对用户不够内聚 | ⭐⭐ |

**推荐**：JSON rules（内置 DSL）——与 AWS S3 用户心智模型一致，无需新语言学习成本。所有条件表达式用 JSON 表示（`{"tag": {"key": "archive", "value": "true"}}`）。如果需要复杂布尔表达式，后续再引入 `expr-lang/expr`。

### 4.3 CoC 哈希和时间戳

- **哈希算法**：SHA-256（FIPS 140-2 合规、标准、快）
- **签名算法**：Ed25519（小签名 64 字节、生成快、非确定性友好）
- **TSA 客户端**：RFC 3161 协议——内置实现或包装 `openSSL ts`。建议初期通过 `openSSL ts` 子进程调用，后续再封装原生 Go TSA 客户端

### 4.4 告警通知通道

实现最少两个通道即可达到 P1 的 MVP 目标：
- **Webhook**（复用现有 `EventsWebhook` 基础设施）——直接可用，零新增依赖
- **日志文件**（写入 file + syslog）——适合集成到现有监控（Prometheus Alertmanager、ELK）

Slack/PagerDuty/Email 等通道可以是纯 Webhook 上的「解析映射层」——用户配置 webhook URL 指向对应服务的 webhook 入口（如 Slack Incoming Webhook），引擎不需要内置适配器。

### 4.5 自建 vs 采购决策

| 方向 | 决策 | 理由 |
|------|------|------|
| FUSE 网关 | **自建** | 市场无现成的支持多后端 + 多租户 + SSE 的 FUSE 方案。s3fs/goofys 只支持 S3。自建成本约 2-3 周 |
| CoC 哈希链 | **自建** | 核心逻辑少（哈希链 + DB 存储 + 锚定发布），不适合引入外部产品 |
| 规则引擎 | **自建** | 规则定义和执行模型高度定制化，且 AWS S3 Lifecycle 的用户认知可复用 |
| 内容告警 | **自建核心 + 可选集成** | PII 告警的触发逻辑在内部索引管线中；严重依赖内部事件格式。通知通道用 webhook 抽象即可 |
| 写入优化 | **自建** | 与 `BufferedFileService` 和 `storage.Storage` 紧密耦合，无法用外部方案替代 |

**不引入新的持久化技术栈。** 所有五个方向都可以基于现有的 SQLite/Postgres 存储（CoC 的表结构、规则的配置存储、告警历史）。不需要引入 Kafka、Redis、Cassandra 等新基础设施。

**总结：除非万不得已，零新增外部依赖。** 新增 `go.mod` 依赖应控制在：
- FUSE: `github.com/hanwen/go-fuse/v2`
- 规则 DSL: 纯 JSON（0 新增依赖）
- CoC: `golang.org/x/crypto` 已有（Ed25519）+ 原生 crypto/sha256
- 告警: Webhook 复用已有 `internal/events`，0 新增依赖
- 写缓冲: 纯标准库 + 同步原语（0 新增依赖）

---

## 5. 实施路线图

### 5.1 调整后的优先级排序

基于交叉验证的研判和我对架构债务的分析，我建议的实施顺序调整如下：

| 阶段 | 方向 | 评级 | 工期预估 | 核心目标 |
|------|------|------|----------|---------|
| **Phase 1** | FUSE 网关 | P0 | 4-6 周 | 打开 POSIX 生态大门 |
| **Phase 2a** | PII 告警（子集） | P1（紧急） | 1-2 天 | 修复安全检测→响应断裂 |
| **Phase 2b** | 标签自动化 | P1 | 3-4 周 | 为存储分层打基础 |
| **Phase 3** | 完整告警引擎 | P1 | 2-3 周 | 通用的内容感知告警框架 |
| **Phase 4** | 监管链 CoC | P1→P2 | 3-4 周 | 合规强化的可选模块 |
| **Phase 5** | 写入优化 | P2 | 2-3 周 | 性能天花板突破 |

### 5.2 阶段详细划分

#### Phase 1：FUSE 网关（P0，4-6 周）

```
Week 1-2：FUSE 骨架
  - 虚拟目录管理器（prefix → dir 映射）
  - Inode 映射器（hash-based）
  - 核心操作：Getattr, Lookup, Open, Read, ReadDir, GetXattr
  - 集成 FileService.Get/Stat/List

Week 3-4：写入路径
  - Write, Create, Mkdir, Rmdir, Unlink, Rename
  - 集成 FileService.Put/Delete
  - 写入缓冲（可选包装 BufferedFileService）
  - 测试：用 `memfs` 模拟 FUSE 操作

Week 5-6：生产级完善
  - 挂载配置（FUSE_MOUNT_POINT、FUSE_ALLOW_OTHER、FUSE_TENANT...）
  - 鉴权集成（token 作为挂载参数）
  - 并发写入锁（POSIX flock 模拟）
  - 大文件分片流式写入
  - Shell 测试：rsync、cp、mv、ls、vim
  - Helm DaemonSet 部署模板

里程碑：`rsync /data /mnt/aero-vault/` 能工作
```

#### Phase 2a：PII 告警—轻量版（P1 紧急，1-2 天）

```
Day 1：
  - 在 indexer.go 的 IndexObjectByID 中，PII 检测结果检查
  - 如果 `pii_categories_found` 非空，通过 EventBus 发送 AlertEvent
  - 新增配置项 AI_PII_ALERT_WEBHOOK_URL

Day 2：
  - 如果配置了 webhook URL，发送包含 {object_id, bucket, key, pii_types, confidence} 的 POST
  - 复用 events.WebhookSender 基础设施
  - 单元测试 + 集成测试

里程碑：信用卡号上传后 5 秒内安全团队收到告警
```

#### Phase 2b：标签自动化（P1，3-4 周）

```
Week 1：规则引擎框架
  - TagRule / TagFilter / Action 结构体定义
  - JSON 序列化/反序列化（兼容 S3 Lifecycle Configuration 语法风格）
  - Repository 层：tag_rules 表 CRUD
  - 规则校验器（TagFilter 语法验证）

Week 2：匹配器 + 执行器
  - TagFilter 评估引擎（MatchAny / MatchAll / MatchIfAbsent）
  - Action 执行器（transition/expire/tag/notify）
  - reconcile 集成：TagRuleScanner.Run()
  - 幂等执行：tag_rule_executions 表

Week 3：管理 API + SDK
  - REST API：POST/GET/PUT/DELETE /v1/admin/tag-rules
  - CLI：aero-vault cli tag-rule create/list/delete/execute
  - SDK 方法
  - 迁移文件 0026

Week 4：生产级
  - 规则冲突检测 + 优先级排序
  - 规则执行历史清理（RetentionJob）
  - 性能测试：10K 规则 × 1M 对象场景
  - 文档

里程碑：`env: production` + `retention: 30d` 标签的对象 30 天后自动删除
```

#### Phase 3：完整告警引擎（P1，2-3 周）

```
Week 1：告警框架
  - ContentAlertRule 定义（Trigger / Channel / Throttle）
  - AlertEngine 核心（规则加载、匹配、分发）
  - Notifier 接口 + Webhook + Log 适配器

Week 2：触发源集成
  - 索引路径：PIIDetector + KeywordMatcher + PatternMatcher
  - reconcile 路径：ContentChangedScanner + SkipRateHighScanner
  - 搜索路径：SearchAnomalyDetector（基于 Usage 表）

Week 3：管理 API + 抑制
  - REST API
  - AlertSuppression（手动抑制误报）
  - 告警历史 + 统计
  - 迁移文件 0027
```

#### Phase 4：监管链 CoC（P1→P2 可选，3-4 周）

```
Week 1：哈希链核心
  - CoCEntry 结构体 + 哈希链计算
  - custody_entries 表
  - FileService 集成：Put/Delete/Version 路径记录 CoC
  - Genesis 创建（首次访问/修改时自动创建）

Week 2：验证 + 锚定
  - GET /v1/lineage/{id}/proof 端点
  - Verifier（重算哈希链 + 验证连续性）
  - 双表锚定（Level 1）
  - CLI：aero-vault cli custody verify

Week 3：外部锚定
  - S3 WORM 锚定（Level 2）
  - TSA 集成（Level 3，可选）
  - CLI：aero-vault cli custody anchor --publish

Week 4：生产级
  - 批量锚定（Merkle 树聚合 → 周期性发布）
  - 大规模验证性能
  - 迁移文件 0025
```

#### Phase 5：写入优化（P2，2-3 周）

```
Week 1：缓冲核心
  - writeBuffer 结构体（(tenant, bucket) 分区队列）
  - 刷新策略：size_threshold / time_window / count_threshold
  - BufferedFileService 装饰器
  - 配置项

Week 2：集成
  - main.go 中条件选择 FileService / BufferedFileService
  - 大对象旁路 + 同步刷新（X-Aero-Flush: now）
  - 事件合并（批量 EventCreated）
  - server.Shutdown 中的 Flush

Week 3：指标 + 健壮性
  - Telemetry：buffered_writes_total, buffer_flush_duration_ms, buffer_queue_depth
  - 缓冲区满的背压行为
  - 内存限制（MaxBufferBytes）
  - 测试：崩溃 → 缓冲区丢失 → 不影响已持久化对象
```

### 5.3 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| FUSE 在生产环境中的内核不兼容性 | 中 | 高 | 在多种内核版本（5.x, 6.x）上 CI 测试；FUSE 运行在用户态，不损坏内核 |
| 标签规则 `MatchIfAbsent` 对大型部署的扫描性能 | 高 | 中 | 增量扫描（只检查新/修改对象）+ partition-aware pagination |
| PII 告警误报导致安全团队忽略真实告警 | 中 | 高 | 提供 AlertSuppression + 置信度阈值配置 + PII 正则优化（Luhn 校验已在实现中） |
| CoC 链在多副本并行写入场景下的竞态 | 低 | 高 | 哈希链的 prev_hash 依赖前一条记录——通过乐观锁（CAS + If-Match row version）保护 |
| 写缓冲崩溃丢失数据 | 中 | 中 | 明确文档为「性能优化层非持久化层」；WORM/对象锁 bucket 自动绕过 |
| FUSE + 写缓冲组合下的缓存一致性问题 | 中 | 中 | `attr_timeout=0` 绕过缓存（牺牲性能换一致性），用户可配置 |

**最重要的架构原则：不要一次性做全部。** 这 5 个方向如果能正确、干净地实现，需要 ~16-20 周的工程投入。建议以 Phase 1（FUSE）作为独立发布，Phase 2a 作为紧急安全补丁单独发布，Phase 2b-5 按依赖顺序逐步推进。每个 Phase 的交付标准是：**测试通过 + 文档更新 + 配置门控 + 零回归**。
