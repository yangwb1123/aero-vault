# 🏗️ 架构分析报告：expansion-v122

## 基于代码锚点验证的深入技术评审

---

## 1. 架构评估

### 1.1 核心优势

| 维度 | 评价 |
|------|------|
| **分层清晰** | Protocol Adapters → FileService → Storage/Repository 三层隔离明确，各层职责边界定义在 `AGENTS.md` 中严格约束 |
| **可测试性** | Storage 层有 `contract_test.go` 契约测试，Repository 有 Migration 自举 + SQLite 默认基线，handler 测试可脱中间件链 |
| **Opt-in 安全** | AI/pgvector/events 等均为 flag-gated，默认 off → 基线路径零依赖 |
| **DB 双驱动** | SQLite + Postgres 并存，Migration 文件成对，`s.rebind` 桥接占位符差异 |

### 1.2 关键设计决策评估

#### ✅ 合理决策

1. **FileService 作为唯一业务控制器** — 所有协议层必须经过 FileService，禁止直连 Storage/Repository，保证了业务逻辑的一致性
2. **Storage key 设计与 GC 匹配机制** — `path.Join(tenant, bucket, key)` 不可反解析，GC 精确匹配 key，避免误删
3. **Middleware 链固化顺序** — RequestID→CORS→Auth→Tenant→RateLimit→OTel→Recoverer→AccessLog 不可变，Handler 不自挂链

#### ⚠️ 存在争议的决策

1. **存储层与仓库层无事务** — `store.Put` 成功 + `writePutObject` 失败 = 僵尸 blob（已确认 gap）。这是最严重的设计决策问题，将在 1.3 单独展开。
2. **`Bus.Publish` 无条件广播** — `notification_rules` 字段存在但在 Bus 中完全不读，所有 subscriber 接收所有事件。这是未完成的抽象——字段已加但消费侧未实现过滤。
3. **持久化 key 的 `ExpiresAt` 先期未检查** — 虽已在 `lookupStore` 修复，但说明字段添加与安全检查之间存在时序断裂。

### 1.3 技术债务与架构债务清单

| 类别 | 债务项 | 严重程度 | 根因分析 |
|------|--------|---------|---------|
| **架构债务** | 存储/仓库缺乏分布式事务协调 | 🔴 P0 | 两阶段提交(2PC)或 Saga 模式缺失，`store.Put` 成功但 `writePutObject` 失败留下孤儿 blob |
| **架构债务** | `hardDeleteObject` 五步线性无回滚 | 🔴 P0 | 任一中间步骤失败后，部分清理已完成但后续未执行，状态不一致 |
| **设计未完成** | `Bus.Publish` 忽略 `notification_rules` | 🟡 P1 | 字段已迁移(bucket schema 0024)但业务逻辑未对接，存在字段定义与消费的匹配缺口 |
| **设计未完成** | `NotificationRule.TopicARN/LambdaARN` 标记 unused | 🟡 P1 | 字段保留但无消费方，未来需清理或补充实现 |
| **实现债务** | `GetRange` 使用 `io.CopyN(io.Discard, offset)` 跳过读取 | 🟡 P2 | 大文件大 offset 场景浪费 I/O，对 object storage 场景伤害更大（走网络读满整个文件到跳过） |
| **实现债务** | MCP 4MB 硬截断 | 🟢 P3 | 硬编码 magic number 而非可配置，大文件场景不可控 |
| **观测债务** | AccessLog 未记录 `key_label` | 🟢 P3 | 审计/计费/租户分析场景缺少细粒度身份标识 |
| **兼容性债务** | DB 驱动分支 (`ClaimJob` 两套实现) | 🟢 P3 | 当前可接受，但如果加第三个驱动(MySQL/CockroachDB)则需要统一抽象 |
| **实现债务** | `io.ReadAll + LimitReader` 在 MCP 两处硬截断 | 🟢 P3 | 一致性问题——应统一为一个工具方法 + 配置化上限 |

#### 🔴 P0 深入分析：存储/仓库事务性断裂

```
PUT /v1/files/foo

  ① store.Put(ctx, key, body)     → 写入存储成功
  ② verifyMD5()                   → 校验通过  
  ③ writePutObject(ctx, obj)      → 仓库写入失败（如磁盘满/SQL约束）

结果: blob 已存在于存储层，但元数据未持久化。
      → 此 blob 成为永久的孤儿（无 GC 可发现，因为 GC 基于仓库记录）
```

**影响面：**
- S3 multipart 场景同样受影响（分片上传完成后合并 blob 也有类似 gap）
- 副本写入同样是先存储后仓库
- 无幂等性保障：重试可能导致重复 blob（虽然不是覆盖）

**修复难度：** 高。因为 Storage 和 Repository 是两个独立后端，无法用本地事务。需要引入补偿机制。

---

## 2. 扩展方向

### 🔹 方向一：引入 Saga/补偿事务模式解决存储-仓库不一致

| 维度 | 描述 |
|------|------|
| **业务价值** | 消除僵尸 blob，提升数据完整性基线的可靠性。直接影响用户数据的持久性和一致性承诺 |
| **技术价值** | 建立分布式系统下的可靠事务模式，未来可用于跨区复制、多存储后端等场景 |
| **核心挑战** | Storage 后端(S3/OSS/COS)不支持本地事务；补偿动作(store.Delete)本身也可能失败；补偿需幂等 |
| **预期架构变更** | 引入 `Compensation` 类型管理回滚栈；FileService 内部使用 `defer` 注册补偿动作；在 `hardDeleteObject` 中应用相同模式 |
| **现有影响** | 影响 FileService 核心 CRUD 路径；需为所有 `store.Put` → `repo.Write` 的双步操作绑定补偿 |

**设计原则：**
```
// 伪设计
func (s *FileService) putWithSaga(ctx, obj, body) {
    compensations := saga.NewCompensationStack()
    defer compensations.ExecuteIfNotCommitted()

    key := s.store.Put(ctx, body)  // 补偿: store.Delete(ctx, key)
    compensations.Push(func(){ s.store.Delete(ctx, key) })

    md5 := verifyMD5(...)
    version := s.repo.WriteObject(ctx, obj)  // 补偿: repo.HardDeleteVersion(ctx, version)
    compensations.Push(func(){ s.repo.HardDeleteVersion(ctx, version) })

    compensations.Commit() // 清除所有补偿
}
```

**选项权衡：**

| 方案 | 优势 | 劣势 |
|------|------|------|
| A. 进程内补偿栈 | 实现简单，不依赖外部组件 | 进程崩溃后补偿丢失；不支持跨节点 |
| B. 补偿写 jobs 表持久化 | 跨进程/跨节点可靠；重试机制复用 JobPool | 增加写放大；补偿延迟 |
| C. 引入 Outbox 模式 | 先写仓库事件，后异步同步到存储 | 一致性与性能的折中；读自己的写可能不一致 |

**推荐：** P0 阶段采用方案 A（进程内补偿栈）覆盖 CRUD 主路径，同时开辟方案 B（持久化补偿 job）作为可选升级路径。

---

### 🔹 方向二：Bucket Notification Rule 过滤引擎

| 维度 | 描述 |
|------|------|
| **业务价值** | Webhook/Notification 按 bucket 过滤，降低下游接收方负载；支持细粒度事件路由(Topic vs Lambda) |
| **技术价值** | 完成已设计的 `notification_rules` 字段到运行时逻辑的闭环 |
| **核心挑战** | 规则过滤性能（是否在 `Publish` 热路径做过滤）；规则缓存一致性；规则变更后事件流切换的无缝性 |
| **预期架构变更** | `EventBus` 增加 `SubscribeWithFilter` 或 subscriber 侧实现过滤；引入 `NotificationMatcher` 接口；`Bus.Publish` 在 broadcast 前查 bucket rules |
| **现有影响** | 影响 `internal/events/bus.go` 广播逻辑；需注意性能开销（事件量大的场景） |

**设计选项：**

| 方案 | 优势 | 劣势 |
|------|------|------|
| A. Publish 侧过滤 | 减少无效 subscriber 调用 | Publish 路径延迟增加；需缓存 bucket rules |
| B. Subscriber 侧过滤 | Publish 路径无性能损失 | subscriber 接收后丢弃，浪费资源 |
| C. 基于事件的 topic 路由 | 灵活可扩展 | 引入 topic 抽象层，改动较大 |

**推荐：** 方案 A（Publish 侧过滤）+ 规则缓存（TTL 5s）。`Publish` 在 `broadcast` 前加载 bucket 的 `notification_rules`，如果规则列表为空则跳过。充分利用已存在的 `notification_rules` 列。

---

### 🔹 方向三：Streaming 零拷贝改造（Range + 预签名 + 缩略图）

| 维度 | 描述 |
|------|------|
| **业务价值** | 大文件 Range 请求性能提升；减少内存/IO 浪费；预签名 URL 的 Range 场景覆盖率提升 |
| **技术价值** | 消除 `io.CopyN(io.Discard, offset)` 这个 O(n) 的浪费；为多后端提供一致的 Seek 语义 |
| **核心挑战** | 不同 Storage 后端对 `Seek` 支持不同（local FS 支持 SEEK_SET，S3 需 Range header 实现）；统一抽象难度大 |
| **预期架构变更** | `Storage.Reader` 接口增加 `Seek(offset) Reader` 方法（或直接在 `Get` 参数中增加 offset）；local 和 S3 后端各自优化；Range handler 不再手动 discard |
| **现有影响** | 影响 `storage.Storage` 接口定义；所有 Storage backend 需适配 |

**设计建议：**
```go
// 当前
type Storage interface {
    Get(ctx, key) (io.ReadCloser, error)
}

// 提议
type Storage interface {
    Get(ctx, key) (io.ReadSeekCloser, error)
    // 或
    GetRange(ctx, key, offset, length int64) (io.ReadCloser, error)
}
```

S3 后端可用 `GetObjectInput.Range` 传递 `bytes=offset-`；Local 用 `file.Seek`。WebDAV 和 REST 的 Range handler 统一使用此接口。

---

### 🔹 方向四：Credential Lifecycle 管理体系化

| 维度 | 描述 |
|------|------|
| **业务价值** | 支持 key 自动过期、轮换、吊销通知；满足安全合规要求 |
| **技术价值** | 完成 `ExpiresAt` 检查闭环；补齐 key 生命周期管理 |
| **核心挑战** | 过期 key 的优雅降级；预过期通知机制；撤销列表的分布式复制 |
| **预期架构变更** | `Auth.LookupKey` 增加 expires 检查（✅ 已完成）；增加 `Auth.RotateKey` 接口；增加 `Auth.RevokeKey` + 撤销列表缓存；Webhook 通知 key 过期事件 |
| **现有影响** | 低，仅影响 auth 包内部 |

**扩展层次：**

```
P0: ExpiresAt 检查（✅ 已实现）
P1: 自动过期扫描（JobPool 定时任务清理过期 key + 通知）
P2: Key 轮换 API（POST /v1/admin/keys/{id}/rotate → 新 key + old key 软过期）
P3: 撤销列表广播（跨节点 Redis/PubSub 同步吊销状态）
```

---

### 🔹 方向五：Telemetry & Observability 增强（修复 AccessLog + 索引计量 + 事件追踪）

| 维度 | 描述 |
|------|------|
| **业务价值** | 生产运维可观测性关键需求：按用户审计(billing)、按事件追踪(debug)、按索引状态监控 |
| **技术价值** | 完成 OTEL 仪表板和告警规则的闭环；补齐观测数据缺口 |
| **核心挑战** | AccessLog 添加 key_label 需要 Auth 上下文透传；事件追踪需要 trace ID 跨组件传播 |
| **预期架构变更** | AccessLog middleware 读取 `tenant` + `key_label`（从 context）；事件 payload 追加 traceparent；Indexer 跳过计量已实现(3 个 reason) |
| **现有影响** | 低，仅 middleware 日志字段和事件结构体变更 |

---

## 3. 接口设计建议

### 3.1 关键模块接口设计原则

| 原则 | 说明 |
|------|------|
| **I1: Compensatable** | 所有两步骤操作必须返回可补偿的句柄。FileService CRUD 方法返回 `error` + 隐含补偿栈 |
| **I2: Backend-agnostic** | Storage 接口不应暴露后端特性（如 Seek 支持度差异）。可选能力用 `interface{ CanSeek() bool }` 类型断言探测 |
| **I3: Event schema stability** | 事件 payload 一旦发布不可破坏向后兼容。新增字段用 `optional` 标记 |
| **I4: Config over magic** | 所有硬编码常量（MCP 4MB）必须迁移到配置项 + 合理默认值 |

### 3.2 是否需要新的抽象层

**需要引入——两个新抽象层：**

1. **`saga.Compensation`** （进程内事务补偿）
   - 位置：`internal/service/saga.go` 或独立 `internal/saga/` 包
   - 职责：管理补偿栈的 push/commit/rollback
   - 原则：补偿动作必须是幂等的；补偿栈提交后清空

2. **`events.Filter`** （事件过滤引擎）
   - 位置：`internal/events/filter.go`
   - 职责：将 Event 与 bucket 的 `notification_rules` 匹配
   - 原则：不阻塞 Publish 主路径（缓存命中）；规则变更实时性要求 TTL 秒级

**不需要引入的抽象：**
- 不需要为 DB 差异引入新的 ORM。当前 `sql.go` 的 `rebind` 策略够用，且新增驱动(MySQL/CockroachDB)概率低
- 不需要为 Streaming 引入独立的 Reader 抽象层。在 Storage 接口内增加 `GetRange` 更简洁

### 3.3 向后兼容性策略

| 变更类型 | 策略 |
|----------|------|
| Storage 接口新增方法 | 用可选接口断言：`if rs, ok := reader.(io.ReadSeeker); ok { ... }` |
| Event 结构体新增字段 | 用 `omitempty`；消费者应忽略未知字段 |
| 配置项新增 | 默认值与原有硬编码一致 |
| 新中间件/路由注册 | 通过配置开关，默认关闭 |
| 删除废弃字段(`TopicARN`/`LambdaARN`) | 标记 `Deprecated: ...`，保留一个 release 周期后再移除 |

---

## 4. 技术选型

### 4.1 是否需要引入新的技术栈

| 技术 | 评估结果 | 理由 |
|------|---------|------|
| Redis/KeyDB | ❌ 暂不需要 | 规则缓存和撤销列表可以用进程内缓存(TTL)，除非多节点场景出现再做 |
| Kafka/Pulsar | ❌ 暂不需要 | 事件量级仍在进程内 Bus 可处理范围；未来跨区复制超出后可评估 |
| **Outbox Table (SQLite/Tx)** | ✅ **推荐引入** | 轻量级、基于现有 SQLite/Postgres、零新依赖。用于可靠事件发布 |
| **一致性哈希/租约锁定** | ✅ **已部分实现** | `leases` 表已用于 cluster singleton；保持不引入 etcd/consul |

### 4.2 第三方依赖评估标准

按照 AGENTS.md I6 规则（Stdlib 优先），新依赖需论证。评估矩阵：

| 维度 | 标准 | 禁止条件 |
|------|------|---------|
| 功能必要性 | 标准库无法 20 行内实现 | strict |
| 许可证 | MIT/Apache 2.0/BSD | GPL/AGPL/SSPL |
| Go 版本兼容 | 支持 Go 1.25 | 低于 Go 1.24 |
| 依赖树 | 传递依赖 < 10 个 | > 20 个传递依赖 |
| API 稳定性 | Go 1 兼容承诺 | beta/v0 API |

### 4.3 自建 vs 采购决策

| 场景 | 自建 | 外部依赖 | 结论 |
|------|------|---------|------|
| 事务补偿 | ✅ 80 行以内实现 | 无成熟 Go 库 | 自建 |
| 事件过滤规则匹配 | ✅ 50 行以内 | 表达式引擎（Expr/CEL）可选 | 当前自建，未来规则复杂化可引入 CEL |
| 分布式追踪 | ❌ | OpenTelemetry ✅ 已集成 | 维持现状 |
| MCP 配置化限流 | ✅ 几行配置 | — | 自建 |

---

## 5. 实施路线图

### 5.1 优先级排序

| 优先级 | 方向 | 技术债务编号 | 预估工作量 |
|--------|------|-------------|-----------|
| **P0** | 事务补偿模式（方向一） | 🔴 I1/I2 | 3-5 天 |
| **P1** | Bucket Notification 过滤（方向二） | 🟡 设计未完成 | 2-3 天 |
| **P1** | ExpiresAt 长期管理 + 自动清理（方向四 P1） | 🟡 设计未完成 | 1-2 天 |
| **P2** | Streaming 零拷贝（方向三） | 🟡 实现债务 | 3-5 天 |
| **P2** | AccessLog 添加 key_label（方向五） | 🟢 观测债务 | 0.5 天 |
| **P2** | MCP 4MB 配置化（方向五附属） | 🟢 实现债务 | 0.5 天 |
| **P3** | NotificationRule 废弃字段清理 | 🟢 兼容性债务 | 0.5 天 |
| **P3** | DB 驱动统一抽象（ClaimJob） | 🟢 兼容性债务 | 2-3 天 |

### 5.2 阶段划分

```
Phase 1 (此 Sprint, 5天)
├── P0: 事务补偿 → file_crud.go saga 包装
│   ├── putObject → saga.Compensation
│   ├── hardDeleteObject → saga.Compensation
│   └── Move/Copy operation 同模式
├── P0: GetRange 跳过优化（可合并实现）
│   └── Storage 接口增加 GetRange(key, offset, length) (io.ReadCloser, error)
└── P0: 测试覆盖 → 补偿路径 + GetRange 场景

Phase 2 (下 Sprint, 3天)
├── P1: Bus.Publish × notification_rules 对接
│   ├── BucketNotificationRules 缓存(5s TTL)
│   ├── Publish 路径过滤
│   └── Webhook subscriber 确认
├── P1: ExpiresAt 自动清理定时任务
│   ├── JobPool handler: delete_expired_keys
│   └── 通知 key 过期事件

Phase 3 (未来 Sprint, 5天)
├── P2: Streaming 全面零拷贝（所有后端适配）
├── P2: AccessLog key_label + 审计增强
├── P2: MCP 限流配置化
└── P3: 废弃字段 + DB 分支清理
```

### 5.3 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| **补偿动作本身失败**（store.Delete 在补偿时也失败） | 中 | 高 | 补偿重试+最终手工兜底；写日志+告警；hardDeleteObject 定时重扫孤儿 |
| **BucketRules 缓存与 DB 不一致** | 低 | 中 | 5s TTL 接受短窗口不一致；支持 `?reload` 参数强制刷新 |
| **GetRange 接口变更影响 S3/OSS 后端实现** | 高 | 中 | 先给 local 后端实现，S3 用 Range header 优化，OSS/COS 留空走回退(discard)路径 |
| **Phase 1 修改量超文件 500 行限制** | 中 | 中 | 先拆分 `file_crud.go`（当前若超 500 行则需按 AGENTS.md 规则先重构再改） |
| **测试覆盖因事务补偿引入复杂度** | 中 | 中 | 单元测试用 mock storage 验证补偿栈交互；集成测试用真实 SQLite+local FS 验证完整循环 |

---

## 总结

### 本次验证发现的核心问题

```
严重等级                   问题                                 影响域
──────────────────────────────────────────────────────────────────────
P0-架构   存储/仓库无事务补偿 → 僵尸 blob                   数据完整性
P0-架构   hardDelete 无回滚 → 部分清理不一致                数据完整性  
P1-设计   事件广播忽略 notification_rules                   功能未闭环
P1-设计   TopicARN/LambdaARN 废弃未清理                      技术债
P2-实现   GetRange 丢弃跳过                                 性能浪费
P2-实现   MCP 4MB 硬编码                                    配置化缺失
P3-观测   AccessLog 缺 key_label                             可观测性
P3-兼容   ClaimJob 两套实现                                 DB 可移植性
```

### 架构健康度评分

| 维度 | 评分(1-5) | 说明 |
|------|----------|------|
| 模块化 | ⭐⭐⭐⭐½ | 分层清晰，边界严格 |
| 可测试性 | ⭐⭐⭐⭐ | 契约测试+handler 隔离 |
| 数据完整性 | ⭐⭐⭐ | 事务补偿缺失降分 |
| 可观测性 | ⭐⭐⭐½ | OTel 已集成但 AccessLog 有缺口 |
| 扩展性 | ⭐⭐⭐⭐ | Opt-in 设计+DB 双驱动 |
| 技术债 | ⭐⭐⭐ | 6 项已确认债务需偿还 |

**结论：** 整体架构设计良好，分层和约束清晰。核心风险集中在**分布式事务缺失**导致的**数据完整性 gap**，这是 P0 级架构债务，建议在此 Sprint 内优先偿还。其余问题（notification_rules 未使用、Streaming 浪费、观测缺口）属于演进性债务，可在后续 Sprint 有序消除。
