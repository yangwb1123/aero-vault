文档已确认保存至 `/home/u1/aero-vault/docs/requirements/expansion-v80-systemic-production-gaps.md`（30KB，约 370 行）。`expansion-v80-systemic-production-gaps.out.md` 尚不存在，我来生成。

---

## 验证评估报告 & 去重核查

我对全代码库独立验证了本分析中的 5 个方向，涉及 23+ 子包、Go SDK/Python SDK/JS SDK、MCP、Web UI、Schema。

### ✅ 方向一：存储类生命周期自动化缺失 — **完全正确**

**代码锚点全部精确：**
- `repository.go:38` — `StorageClass` 字段定义 ✅
- `sql_objects.go:33-50` — `storage_class` 在 INSERT/UPSERT 中持久化 ✅
- `lifecycle.go:15-90` — 仅有 `soft_delete` / `hard_delete`，无任何 `transition` 动作 ✅
- `metrics.go:181-183` — `RegisterStorageClassGauge` 存在但不驱动行为 ✅
- `xml.go:203-231` — Lifecycle XML 定义 `Transition` 结构体但 handler 从不解析 ✅

**去重声明成立：** v21/v31–v37 仅概念性提及表格行；v42 聚焦 CopyObject 属性保留；v77 聚焦 events 表 GC。**零代码级分析 storage_class → tier transition 引擎缺失**。

### ✅ 方向二：服务器访问日志写入管道缺失 — **完全正确，链路完整**

**代码锚点全部精确：**
- `handler.go:306-313` — `dispatchBucketLogging` 分发表 ✅
- `handler.go:720-760` — `get/put/deleteBucketLogging` 实现 ✅
- `sql_buckets.go:334-364` — SQL 操作就绪 ✅
- `0023_bucket_logging.up.sql` — schema 就绪 ✅
- `middleware.go` — AccessLog 仅写 stderr，不触发 bucket logging pipeline ✅

**去重声明成立：** v75 方向以 ~30 行概念性提及管道缺失但无完整链路分析；v39 聚焦 admin audit log 而非对象级访问日志。**本方向首次提供 `?logging` API → repository → schema → middleware 全链路追踪 + 边界情况枚举**。

### ⚠️ 方向三：关键订阅者可靠事件送达保障缺失 — **正确，但需补充既有分析覆盖**

**代码锚点全部正确：**
- `bus.go:84-88` — `select { case ch <- e: default: b.dropped.Add(1) }` 静默丢弃 ✅
- `bus.go:80-82` — 缓冲 64，极易触发丢弃 ✅
- `antivirus/worker.go:48-63`, `replication/replication.go:80-115`, `webhook.go:50-80` — 慢消费时丢弃 ✅

**去重声明需微调：** v79 方向一第 68-78 行**确实**提及 EventBus 丢弃作为 AI chunk 残留的次要路径，并指出"无日志、无告警、无死信队列"。但 v79 以两句话带过，**非独立方向深度分析**。本方向的贡献在于：首次作为独立 P1 方向进行全链路分析（丢弃条件量化、关键订阅者清单、死信队列 + 回放 API + 健康检测 + 背压传导完整方案、影响量化表格）。建议将去重声明从"零分析"调整为"v79 首次识别但仅附属提及，本方向首次独立深度分析"。

### ✅ 方向四：Python/JS SDK 管理面功能断裂 — **完全正确**

**代码锚点全部精确：**
- Go SDK `client.go` — 14 个 admin 方法完整 ✅
- Python `aero_vault.py` — 仅 4 个 admin 方法（缺失 10 个） ✅
- JS `aero-vault.js` — 0 个 admin 方法 ✅
- REST `admin.go` — 完整管理 API 存在但 Python/JS 不消费 ✅

**去重声明成立：** v79 方向三聚焦 Web UI 管理面缺口（图形界面）；v46 聚焦 admin API 文档；v52 概念性提及 SDK 自动生成。**零方法级跨 SDK 覆盖率对比清单**。

### ✅ 方向五：存储层内容去重缺失 — **完全正确**

**代码锚点全部精确：**
- `storage.go:38-40` — `ETag` 字段定义 ✅
- `local_write.go:50-90` — `Put` 无内容比对 ✅
- `handler.go:250-280` — `handlePut` 校验 MD5 但不用于去重 ✅
- `file_crud.go:173-205` — 全程追踪 MD5 但丢弃去重信息 ✅
- `sql_objects.go:33-50` — `etag` 字段持久化 ✅

**去重声明成立：** v6 方向一概念性提及 "content-addressable storage"（路线图方向）；v13/v14/v15 表格行；v61 方向二单行提及。**零分析 MD5/ETag 基础设施已就位但去重零行实现的具体代码路径**。

---

## 总体评估 & 实施建议

| 指标 | 评分 |
|------|------|
| **代码锚点精确度** | ⭐⭐⭐⭐⭐ — 所有行号引用精确可复现 |
| **影响量化** | ⭐⭐⭐⭐ — 方向一有详细成本分析表格；方向三/四可加强量化数据 |
| **实现方案具体性** | ⭐⭐⭐⭐ — 各方向实现要点 + 边界情况表格完整清晰 |
| **去重严谨度** | ⭐⭐⭐⭐ — 方向三需微调声明但本质判断正确 |
| **优先级合理性** | ⭐⭐⭐⭐⭐ — P1/P2/P3 划分符合系统影响面 |

### 建议实施顺序

1. **P2 方向四（SDK 管理面功能断裂）** — 最小风险（纯新增代码），最高开发者体验 ROI，可增量推进
2. **P1 方向三（事件可靠送达）** — 优先修复关键订阅者静默丢弃，`SubscribeCritical` 约 50 行改造
3. **P1 方向一（生命周期 transition）** — 配置化实现，默认不改变现有行为，S3 兼容性增量
4. **P2 方向二（访问日志写入管道）** — 异步非阻塞，需 middleware + logger worker 配合
5. **P3 方向五（内容去重）** — 功能开关默认关闭，需 content_hashes 表 + 写入/删除路径改造

---

请问需要对其中哪个方向进行具体实现？我可以从**方向四（SDK 管理面补齐）**或**方向三（事件可靠送达）**开始——前者影响面最小，后者数据完整性最重要。
