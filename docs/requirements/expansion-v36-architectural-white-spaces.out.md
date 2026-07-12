我已完成全面的代码库验证。以下是我的分析报告：

---

## 🔍 代码锚点验证报告

我已对照实际源码验证文档中的每个关键代码锚点声明和去重声明。整体文档质量很高，但发现以下需要修正的问题：

---

### 一、去重声明问题（需修正）

#### ❌ 方向 2：「0 命中」声明不准确

文档声称 `grep` 结果为零命中，但实际 `expansion-v106-production-operations-blindspots.md` 对「多租户资源治理与公平调度」（包括 noisy neighbor 保护、存储 I/O 优先级）有 **实质性架构分析**——它作为方向二有完整的设计：现状→代码锚点→解决方案→边界情况，约 900+ 行分析。

```bash
# 实际验证结果（我在您文档之外找到的）：
docs/requirements/expansion-v106-production-operations-blindspots.md
  → 方向二：多租户资源治理与公平调度
  → 包含 "noisy neighbor"、"存储 I/O 优先级"、"跨协议资源池协调"
  → P1 优先级，完整分析（虽然聚焦请求级而非 IOPS 级）
```

**建议修正：** 在去重矩阵中承认 v106 的覆盖面，然后差异化说明：「v106 覆盖请求级公平调度和跨协议协调，但本方向聚焦 **存储 IOPS/吞吐带宽级** 的物理资源隔离——这是本质不同的资源维度。」

#### ⚠️ 方向 1：与 v104/v122 的边界需明确

`expansion-v104` 和 `expansion-v122` 对「服务层双写事务完整性」（`storage.Put` ↔ `repo.UpsertObject` 原子性）有实质性架构分析，含 `write_log` 表设计、SAGA 补偿模式、启动时回滚等。您现有文档的评价：「批量操作缺乏事务/SAGA」但聚焦 `BatchDelete` 逐对象执行，未触及核心 CRUD 路径的双写 gap——这不够准确。v104/v122 确实只覆盖**单对象**的双写保护，但涉及的事务基础设施（补偿日志、reconcile 回滚）可复用于跨对象场景。

**建议修正：** 在方向一的「为什么需要」中引用 v104/v122 的基础设施作为起点，然后提出本方向的核心增量——「从单对象双写保护 → 跨对象 begin/commit/rollback API」。

---

### 二、代码锚点小误差

| # | 文档声明 | 实际位置 | 严重程度 |
|---|---------|---------|---------|
| 1 | `internal/middleware/concurrency.go` 文件存在 | `ConcurrencyLimiter` 在 `internal/middleware/middleware.go:111`，**无独立 `concurrency.go`** | 🟡 轻微（文件路径偏差不影响分析实质性） |
| 2 | `internal/storage/local.go:Get` / `Put` | 实际在 `local_read.go:16` 和 `local_write.go:13`（`local.go` 仅有 `NewLocal`/`Backend`/`objectPath`） | 🟡 轻微（Go 包内同一包，文件拆分是内部实现细节） |
| 3 | 文档说「`internal/repository/sql.go` 无显式事务管理 API」 | ✅ 正确——`Repository` 接口无 `BeginTransaction`/`WithTransaction`，但 `sql_buckets.go`/`sql_objects.go` 等内部使用 `s.db.BeginTx(ctx, nil)` | ✅ 准确 |
| 4 | 文档说「leases 表仅用于 singleton，不暴露通用锁 API」 | ✅ 正确——`AcquireLease` 在 `leases.go` 存在，但接口狭窄（仅 singleton key），无通用的 `Lock(name, holder, ttl, mode)` | ✅ 准确，且方向四的复用基础好 |

---

### 三、代码验证确认的缺口

| 方向 | 验证结论 |
|------|---------|
| **方向 1** | ✅ 确认：`BatchDelete`/`BatchSetTags` 在 `handler.go:629/651` 和 `file_features.go:129/153` 均无事务边界。`Repository` 接口无事务传播 API。**缺口实质性存在** |
| **方向 2** | ✅ 确认：`Storage` 接口（`storage.go:105`）无 QoS 维度。`RateLimiter`（`ratelimit.go`）仅 HTTP RPS 限流。`ConcurrencyLimiter`（`middleware.go:111`）仅并发数限流。整个代码库无 `qos`/`IOPS`/`throughput` 相关概念。**缺口实质性存在**（但去重声明需修正） |
| **方向 3** | ✅ 确认：Router（`router.go:72-88`）所有 bucket 配置基于 imperative PUT/GET/DELETE。`reconcile/` 目录（`job.go`/`lifecycle.go`/`retention.go`/`scrub.go`）无 config 协调器。**缺口实质性存在** |
| **方向 4** | ✅ 确认：`leases.go` 仅有 `AcquireLease(name, holder, ttl)` 单一模式。`Repository` 接口（`repository.go:231`）无通用锁方法。`cluster/singleton.go` 仅 singleton 用途。依赖验证：`go.mod` 无 `redis`/`etcd`/`zookeeper`/`consul`。**缺口实质性存在**，且**基础好**（^扩展现有 `leases` 表即可） |
| **方向 5** | ✅ 确认：`events/bus.go:67` `Publish` 全量广播、无规则引擎。`ai/indexer.go`/`antivirus/`/`replication/` 各自直接订阅。**缺口实质性存在** |

---

### 四、建议润色的边界情况

| 位置 | 建议 |
|------|------|
| 方向一「存储层不支持回滚」 | S3 blade 可通过 versioning 模拟回滚（删除最新版本=还原），建议提及此方案作为 Intent Log 的可选替代 |
| 方向二「QoS 对 local FS 的效果」 | 建议补充：Go `rate.Limiter` 做带宽控制有效但对磁盘 IOPS 的精确控制需 `os.File` 级的 Fadvise/DirectIO，local FS 场景可考虑 iotop/cgroups v2 IO 控制器作为 OS 辅助方案 |
| 方向三「Secret 值引用」 | 建议明确引用机制（环境变量 / Vault ref / K8s Secret）而不仅是「不硬编码」 |
| 方向四「SQLite vs Postgres」 | 建议明确说明：SQLite 单进程场景分布式锁退化为进程内 mutex——仍需暴露统一 API 以保持接口一致 |
| 方向五「策略循环检测」 | 建议用 `policies[].name` + `policy_stack` context 做运行时检测（类似 HTTP `X-Forwarded-For`），比固定 `max_policy_depth` 更安全 |

---

### 五、补充建议：跨方向依赖图

文档已有 Mermaid「跨方向协同效应」图，建议补充**反依赖**——哪些方向**不应同时实现以避免重复工作**：

```
❌ 不要方向 1（事务 Coordinator）的同时独立实现方向 4（分布式锁）
   → 方向 4 的 fencing token 是方向 1 的乐观并发控制核心机制
   → 先做方向 4（锁原语）→ 再做方向 1（事务）→ 事务复用锁保证并发安全

❌ 不要方向 3（声明式配置）和方向 5（策略引擎）同时设计
   → 方向 5 的策略定义（YAML）语法可复用于方向 3 的 manifest
   → 优先统一配置/策略的声明式 YAML schema
```

---

### 总结

| 项目 | 结果 |
|------|------|
| 整体分析质量 | ⭐⭐⭐⭐⭐ 高质量，5 个方向确实填补了实质性架构空白 |
| 代码锚点准确率 | ~95%（2 处文件路径小偏差，不影响分析有效性） |
| 去重声明准确率 | ~80%（方向 2 的「0 命中」需修正；方向 1 与 v104/v122 边界需明确） |
| 架构设计合理性 | 全部可行，且巧妙利用了现有基础设施（leases 表、Postgres/SQLite 事务、event bus）|
| 最重要改进建议 | ① 修正方向 2 的去重声明；② 明确方向 1 与 v104/v122 的关系边界 |

现有文件已在 `docs/requirements/expansion-v36-architectural-white-spaces.md`。是否需要我写入更新后的版本？
