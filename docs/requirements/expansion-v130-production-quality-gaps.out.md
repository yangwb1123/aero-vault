文档已完整阅读。这是一份极其高质量的系统性分析——定位精准、锚点具体、架构概要清晰、边界情况考虑周全。以下是我作为架构同行的**补充审查意见**：

---

## 全局印象

v38 的独特价值在于它识别了一个所有功能分析都回避的真相：**aero-vault 的功能密度已经远超其生产质量基线**。190+ 方向覆盖了你能想到的每一个 feature corner，但 Context 断裂、连接池缺失、排空能力为零、错误模型扁平这些基础问题——才是真正决定系统能否在 3AM 扛住 PagerDuty 告警的要素。

一句赞：去重验证方法论（逐方向 `grep` + 区分"过路提及" vs "实质性分析"）做得非常扎实，这在长序列分析中极度重要。

---

## 对每个方向的实质性补充

### 方向一：Context Propagation

**一个未提及的深坑：`otelhttp` handler 的自动注入与手动 propagator 之间的不一致。**

当前代码中可能使用了 `otelhttp.NewHandler` 来自动从 HTTP 请求提取 trace parent。如果某些 handler（如 WebDAV 的 goroutine）跳过这个 middleware 直接处理，即便传递了 `ctx`，trace context 也是空的。建议在诊断步骤中加入：*验证每个协议入口点是否经过了同一个 `otelhttp` middleware（或手动调用了 `propagators.Extract`）*。

**另一个实践建议：** 与其全面禁止 `context.Background()`（会打破很多合法的 standalone purge/test 路径），不如定义一个 lint rule 为：`func Background() context.Context` 在生产代码中调用时**必须附带注释 `//lint:allow-background` 并说明理由**。这样迁移路径更平滑。

### 方向二：HTTP Connection Pooling

**补充一个关键隐患：`secret.go` 的 `newHTTPProvider` 不仅是池缺失，它可能是 goroutine-safe 的并发泄漏源头。**

如果 `newHTTPProvider` 被多个请求同时调用，每个都新建 TCP 连接 → 写入 KMS → 读响应 → 丢弃 `*http.Client`（transport 的 idle conn 还在后台存活），GC 回收前这些连接始终保持 ESTABLISHED。在高并发下，这可能导致 `too many open files` 的硬失败，**且重启前不恢复**（fd 泄漏到内核极限）。

**建议架构调整：** 与其全局共享 `http.Transport`（存在后端交叉影响），更适合用 **`transport` pool per destination group**：

```go
// 按 destination group 分片
transportPool := map[string]*http.Transport{
    "ai":      {MaxIdleConnsPerHost: 4, IdleConnTimeout: 90*s},
    "storage": {MaxIdleConnsPerHost: 10, IdleConnTimeout: 30*s},
    "kms":     {MaxIdleConnsPerHost: 2, IdleConnTimeout: 15*s},
}
```

这样 LLM 慢响应不会饿死 S3 请求池，各自独立。

### 方向三：Graceful Shutdown

**提到一个容易被忽略的竞态：shutdown 过程中的 `EventBus.Publish` 和 `Bus.Close` 之间缺少 `sync.RWMutex`。**

如果 "停止新请求" 和 "排空" 之间有一个微小的窗口——`Publish` 检查了 `closed` flag 为 false 后刚准备写 channel，而此时 `Close` 开始了——会导致 `send on closed channel` panic。需要在 Bus 中用 `sync.RWMutex` 保护 `Publish` 路径（`RLock`）和 `Close/Drain` 路径（`Lock`）。

**另一个补充：** Job queue 的 `running → pending` 重置逻辑需要处理"作业已执行完成但结果尚未持久化"的情况。建议在 `StoreResult` 中先更新 job status 再返回——如果进程在 `StoreResult` 之前崩溃，作业会处于 `running` 状态，重启后重置为 `pending` 会被重做（at-least-once 语义）。如果这是可接受的，文档应明确说明。

### 方向四：Structured Error Domain

**补充一个设计中容易出现的陷阱：`AeroError.Retryable` 不是静态属性，而是依赖上下文。**

一个错误在以下情况中可能 retryable 或 not：
- 面向 S3 客户端时，`NoSuchUpload` 是永久错误（上传已不存在）
- 面向内部 repl worker 时，`NoSuchUpload` 可能是时序问题（可重试一次后放弃）

建议 `AeroError.Retryable` 改为方法而非字段：
```go
func (e *AeroError) Retryable(ctx context.Context) bool {
    // 可结合当前上下文中的 consumer 类型动态判断
}
```

或者分层设计：**`Classify(ctx, err, protocol)`** 接受 `protocol` 参数，不同协议对同一错误给出不同 retryable 判定。

**另一个设计建议：** `Tenant/Bucket/Key` 字段可以考虑通过一个 `WithContext(ctx)` 方法从 context 中自动提取（使用 `TenantFromCtx` 等已有的 helper），这样领域代码只需 `errors.NewAeroError(...)` 而无需手动传这些元数据。

### 方向五：Testing Infrastructure

**补充一个关键缺失：变异测试（Mutation Testing）的考虑。**

如果用 go-mutator 或类似工具做简单的变异测试（mutate `==` 到 `!=`、删除一行、翻转 bool），可以快速发现当前 61.1% 覆盖率中有多少是"有效覆盖"（测试确实在断言行为） vs "死覆盖"（测试运行了但不验证结果）。这在 AGENTS.md 的"重构优先级高于功能开发"原则下尤为重要——高覆盖率但低质量的测试集会在重构时产生大量的 false positive 报警。

**对于 fuzz 的补充建议：** 不要只 `go test -fuzz`，还应该在 CI 中加入**崩溃语料库（crash corpus）的回归检测**。每次 fuzz 发现的崩溃应序列化到 `testdata/fuzz/crashers/` 并 commit 到仓库，CI 自动重放。否则 fuzz 发现的 bug 修复后可能再次引入。

---

## 两个未提及但相关的交叉风险

### 风险 A：方向① + 方向③ 结合时的 shutdown ordering deadlock

如果 drain 阶段等待 bus subscriber 排空，而 subscriber 正在等待某个 in-flight context 的 cancel 信号才能完成——但 cancel 信号来自某个已经被 shutdown 的 HTTP handler——就会形成依赖环。建议 shutdown order 中强制：`HTTP server shutdown` > `cancel signal ctx` > `bus drain` > `worker drain`，确保 cancel 信号在 drain 之前发出。

### 风险 B：方向⑤ + 方向④ 结合时的测试侵入性

`AeroError` 的 `Tenant`、`Bucket`、`Key` 字段意味着测试中需要构造大量上下文对象。如果设计不好，会导致测试从 `assert.Equal(t, want, got)` 退化为 `assert.Equal(t, want.Code, got.Code)`（只测 error code 不测 message）。建议 `AeroError` 实现 `testutils.Matcher` 接口（或等效的 testify `assert` helper），让测试可以按字段部分匹配：

```go
assertError(t, err, &AeroError{Code: "NotFound", Retryable: false})
// 忽略 Tenant/Bucket/Key 等难以构造的字段
```

---

## 总结

| 维度 | 评价 |
|------|------|
| **方向选择** | 🟢 精确命中了功能覆盖盲区，"非功能属性的生产质量缺口"这个切入点非常锐利 |
| **证据链** | 🟢 每个方向都有明确的代码锚点（行号 + 代码内容），可复现性强 |
| **架构概要** | 🟢 目标架构图清晰，从"当前"到"目标"的演进路径具体可执行 |
| **边界情况** | 🟡 优质（方向一四五的边界表很好），部分方向（二、四）的 edge case 深度还可以加大 |
| **依赖关系** | 🟢 优先级排序有说服力，①→③的依赖链分析正确 |
| **去重验证** | 🟢 严格、透明，可信度高 |

**总体判断：** 这是 aero-vault 分析系列中从"功能扩张"转向"质量夯实"的标志性一期。5 个方向如果全部实施，系统将从"功能丰富但生产脆弱"进化为"功能丰富且生产可靠"。方向①和②是最佳 ROI 切入点——它们的修复成本不高，但对故障恢复时间（MTTR）和延迟稳定性的影响是立竿见影的。

---

*需要我针对某个方向展开更深入的技术设计草案（比如方向一的 context 传播 lint 规则、方向二的 transport pool 分片策略、方向四的 `Classify` 统一接口）吗？*
