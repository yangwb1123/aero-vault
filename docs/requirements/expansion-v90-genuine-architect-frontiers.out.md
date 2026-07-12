这是一份极其深刻、代码锚点精准的架构盲区分析。我逐方向过了一遍，有几个值得展开讨论的观察点。

---

## 整体评估

**优势：** 与其他 89 轮文档的关键区别在于——每个方向不仅是概念层面的"gap listing"，而是从 `internal/` 的精准代码行出发，推导到 S3 协议语义、产品场景、架构 tradeoff、分步实施路径、边界情况。这是目前 269 份文档中实施准备度最高的一批分析。

**潜在风险：** 方向四（补偿事务）与方向一（合规锁）之间存在隐藏的交互——Compliance 模式对象的补偿删除策略需要特殊处理；方向二（UploadPartCopy）与方向三（NoncurrentVersionExpiration）在版本化桶下也有交集。

---

## 逐方向启发式评论

### 方向一：合规锁模型 — 最成熟的"已准备实施"

`RetentionMode` 字段缺失确实是当前 WORM 实现最根本的架构缺陷。`locked_until` 作为 `*time.Time` 无法承载"不可绕过"的语义。一个值得注意的边界——当 `RetentionMode == COMPLIANCE` 时，不仅 `hardDeleteObject` 要拒绝删除，**SSE envelope 重包装路径**（`key-rewrap-on-start`）也需确保不修改 Compliance 对象的存储 metadata。当前 `encrypt.go` 的 Rewrap 逻辑扫描所有 blob 时没有检查保留模式。

### 方向二：UploadPartCopy — 协议完备性的最短木板

`copyObject` 全量读内存的实现确实是一个"等在路上的生产事故"——只要使用者拷贝一个中等文件（≥500MB），OOM 风险就出现。一个我注意到的细节：方向二的分析没有提到 `x-amz-copy-source` 的 `?versionId=` 参数解析，当前 `copySource` 解析在 `extra.go:parseCopySource` 中已经支持了 `bucket/key?versionId=xxx` 格式，这是已有的可复用基础设施。

### 方向三：版本生命周期 — 存储成本失控的第一张多米诺骨牌

这个方向最妙的地方在于 **`NoncurrentDays=0` 的默认行为**——如果用户设置了版本控制但没有设置生命周期策略，旧版本无限堆积。S3 默认行为相同，但 AWS 的策略是"用户付费所以用户自觉管理"。AeroVault 的 `maxVersions` 约束可能更适合作为**默认开启的保护措施**而非可选配置。

### 方向四：补偿事务 — 最需要"克制"的设计

方向四分析中方案 A（Reconciler 被动检测）vs 方案 B（写入时补偿）的 tradeoff 表非常清晰。我想补充一个观点：**方案 A 的 `storage.List("")` 在 Local 后端上可行，在 S3 后端上成本极高**（每 million 对象 ~$0.50 API 费用 + 耗时）。增量扫描（按 prefix/tenant/bucket 分批 + `LastModified` 过滤）不是"nice to have"而是 S3 后端的必须设计。

### 方向五：版本操作与 Diff — 产品差异化最高但实施成本也最高

版本回退（Revert）的实现其实比方向五分析的更微妙一点：`storageKey` 在版本化桶中的映射是 `storageKey(tenant, bucket, key) + "@v" + versionID`。"回退"时如果使用旧版本的 `storageKey` 来创建新版本，需要确保：
1. 旧版本的 blob 没有被 Lifecycle 删除（Compliance 锁则安全，非锁定则可能已被裁剪）
2. 回退后旧版本仍然独立存在（不会被新版本覆盖）

建议增加版本回退前的 `store.Stat(oldStorageKey)` 校验。

---

## 建议下一步

这份文档的分析深度和实施准备度已经超过 `docs/requirements/` 下的多数既有文档。你是否希望：

1. **保存为新要求文档** — 写入 `docs/requirements/expansion-v140-object-lock-compliance-and-write-path-compensation.md`？
2. **讨论优先级调整** — 是否有外部约束（如某个客户的合规要求）影响 P1/P2 排序？
3. **开始某一方向的实施** — 例如方向一的 schema 迁移 + `RetentionMode` 字段 + 硬删除路径模式检查（标注为"1-3 天"的快速闭环）？
