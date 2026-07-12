现在我对代码库进行了全面验证，以下是评估：

---

## 审阅：AeroVault 高价值扩展方向（第二十期）

### 代码验证摘要

我对照代码库验证了每个方向的代码锚点声明。以下是结果：

| # | 方向 | 准确性 | 发现 |
|---|------|--------|------|
| 1 | 🔴 GitOps 控制器 | ✅ **准确** | 配置完全由环境变量驱动；`reconcile/` 仅处理清理/生命周期，不处理配置；admin handler 直接操作运行时状态 |
| 2 | 🔴 事件钩子 / 无服务器函数 | ✅ **准确** | `bus.Subscribe()` 返回内部通道；Job 类型硬编码；Webhook 只有简单的 URL→POST |
| 3 | 🟠 元数据 Schema 治理 | ✅ **准确** | `Object.Metadata` 是 `map[string]string`；`validateMetadata()` 仅检查大小/长度，无类型/enum/必填约束 |
| 4 | 🟠 统一 DR 框架 | ✅ **准确** | `snapshot.go` 明确声明仅支持 SQLite+本地文件系统；复制是单向的；无备份调度、无故障转移 |
| 5 | 🟠 WFQ 公平调度 | ⚠️ **代码细节有小出入** | 见下文 |

**方向 5 的小问题：**
- 文档引用 `internal/middleware/concurrency.go` — 该文件**不存在**；`ConcurrencyLimiter` 在 `internal/middleware/middleware.go` 中
- 文档显示有一个 `perTenantMax int64` 字段，但实际结构体是 `sem chan struct{}` — 一个全局信号量（按 HTTP 方法加权，无租户感知能力）
- **核心分析仍然成立**：没有加权公平队列、无预留容量、无优先级调度、无自适应速率限制

### 对分析的全局评估

**总体评分：9/10** — 一份优秀的分析，基于真实的代码结构性缺失。以下是我的详细反馈：

---

#### 方向 1（GitOps）是当之无愧的 P0

最佳论据是**代码锚点表**完美地捕捉了痛点。我只有一个架构层面的异议：

> "复用率 60%+：admin.go 的 18 个 handler 已经是'将请求体转换为 DB 操作'的逻辑"

实际上，admin handler 将 HTTP 序列化/反序列化与业务逻辑交织在一起。将它们提取为可编程的 `Executor` 层并非微不足道的重构——大约有 5-8 个 handler（租户 CRUD、Key CRUD、配额）可以干净地提取；其余的（审计、管理操作）有 HTTP 特定的假设。我估计实际复用率更接近 **40-50%**。不过，这仍然值得做。

**建议补充：** 在 Source 接口支持中添加一个 `WaitForStability()` 阶段：在触发协调前，配置源需要在 N 秒内保持稳定（防止 Git 推送中间态触发的部分应用）。

---

#### 方向 2（事件钩子）—— 正确的范围，但需要定义 MVP

文档正确地识别了差距，但我认为**MVP 应该比建议的更进一步缩小范围**：

**核心缺失**不是脚本沙箱或 Lambda 集成——而是**用户可配置的事件过滤器 + 多目标 Webhook**。当前的 Webhook 是 `url → POST all events`。一个单一的 JSON API：

```json
POST /v1/admin/hooks
{
  "name": "pii-alert",
  "events": ["object.updated"],
  "filter": "object.metadata._aero_scrub_status == 'pii_found'",
  "target": {
    "type": "webhook",
    "url": "https://alert.example/pii"
  },
  "retry": {"max_attempts": 5, "backoff": "exponential"}
}
```

就已经覆盖了 80% 的用例，无需脚本沙箱。脚本/Lambda 集成可以作为 Phase 2。

**架构评论：** 建议的 `HookRegistry` 架构是正确的。一个重要的约束：钩子执行**必须**有自己的超时上下文（独立于触发请求的上下文——当前通过 `bus.Publish` 传入的 ctx 可能来自一个即将超时的 HTTP 请求）。

---

#### 方向 3（元数据 Schema）—— 被低估的风险

文档正确地指出没有 Schema 治理，但有一个问题**分析不足**：

```
一致性挑战：S3 协议下 x-amz-meta-* 头没有类型概念
（所有值都是字符串）。
```

这在文档的边界情况表中被提及，但我认为它比"可选绕过"更深。如果你在 REST API 上强制执行 Schema 验证，但允许 S3 透传，用户会抱怨"为什么同一个文件通过 S3 SDK 上传可以，但通过 REST API 上传就不行？"

**建议：** 让 Schema 成为桶级别的可选特性，默认关闭。启用后，S3 路径也会通过验证（将字符串值按 Schema 类型解析）。这提供了跨协议的一致性。

---

#### 方向 4（DR 框架）—— 最高质量的分析

这是五个方向中**最被低估**的。RPO/RTO 对于企业采购来说是硬性要求。

文档对现有组件的分析（复制 = 单向 + 仅对象级别；快照 = SQLite 仅限本地；单例 = 仅用于协调防护）是**完全准确**的。

**一个遗漏的约束：** Postgres 流复制不是 AeroVault 可以"集成"的东西——Postgres 流复制是数据库层面的，需要 Postgres 自身管理。AeroVault 可以做的是：
1. 提供一个包装 `pg_dump` / `pg_basebackup` 的 `pg_backup` 命令
2. 使用 `LISTEN/NOTIFY`（`postgres_transport.go` 已经做了类似的事情）进行应用层元数据复制

文档的建议大致是正确的，但需要在 `backup.go` 的实现中明确**数据库类型的分支**——SQLite 的备份逻辑与 Postgres 完全不同。

---

#### 方向 5（WFQ）—— 正确的方向，但过于复杂

公平调度是必要的，但建议的设计将三个独立关注点合并了：

1. **租户权重队列**（每个租户的并发槽位比例分配）
2. **请求优先级**（方法级别的优先级）
3. **自适应速率限制**（类似 TCP 拥塞控制的延迟反馈）

我建议将它们拆分为更小、可独立部署的部分：

**Phase 1（高价值，低复杂度）：** 用**租户感知的加权信号量**替换当前的全局 `ConcurrencyLimiter`。保持简单——每个租户固定槽位分配，借用空闲槽位。修改 ~20 行代码。

**Phase 2（中价值）：** 请求优先级——一个只有 2 或 3 级（管理员、交互式、批量）的简单优先级队列，无需完整的 WFQ。

**Phase 3（低价值）：** 自适应速率——复杂性最高，价值最低（静态 RPS 配置适用于绝大多数场景）。

文档把它们合并在一个建议中，这使得实现工作看起来比实际更令人望而却步。

---

### 按代码库真实性列出的问题

| 文档中的声明 | 实际代码 | 严重程度 |
|------------|---------|---------|
| `internal/middleware/concurrency.go` 存在 | 不存在；`ConcurrencyLimiter` 在 `middleware.go` | 低 — 文件重命名 |
| ConcurrencyLimiter 有 `perTenantMax` | 没有；是 `sem chan struct{}`——全局信号量 | 低 — 描述的接口比实际更丰富 |
| `internal/api/rest/dto.go` 有"无 metadata schema 参数" | ✅ 是的，`Metadata map[string]string` 是唯一字段 | ✅ 准确 |
| `validateMetadata` 不存在 | ✅ 存在（`file.go:105`），但仅检查大小/长度 | ✅ 准确 |
| 18 个 admin handler | ✅ `admin.go:125-580` 大约有 18 个 handler | ✅ 准确 |

---

### 最终优先级评估

| P0 | P1 | P2 |
|----|----|----|
| **GitOps 控制器** — IaC 硬门槛；复用 reconcile 架构 | **DR 框架** — RPO/RTO 是企业 SLA 要求 | **元数据 Schema** — 合规驱动；侵入性最高 |
| **事件钩子（MVP：过滤 + 多目标）** — 产品差异化；复用事件总线 | **WFQ Phase 1** — 租户感知的并发（~20 行变更） | |

**总的来说**：这是一份高质量的分析，捕捉了代码库中真实的结构性缺失。方向的选择很好——它们共同将 AeroVault 从"功能丰富的单节点服务"推向"可运营的企业平台"。代码级的小问题不影响整体论证。

是否需要我就上述任何方面展开深入探讨？
