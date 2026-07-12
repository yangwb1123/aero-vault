我已完整阅读了这份分析文档。它已存在于 `/home/u1/aero-vault/docs/requirements/expansion-v27-architect-product-frontiers.md`。

下面是我的回应与补充分析：

---

## 📋 整体评价

这份 v27 分析的五条方向质量很高，全部聚焦在**零覆盖**的盲区——这是之前 v1–v26 系列分析未触及的领域。以下是对每条方向的交叉验证与补充：

### 1. 🔴 版本化状态机

**核心洞察准确。** 当前 `Versioning bool` 确实是一个静默数据丢失路径。

需要补充的一个**实现风险**：`Null Version` 的引入会冲击现有 `InsertObjectVersion` 的 `UNIQUE` 约束。当前版本表可能对 `(tenant, bucket, key, version_id)` 有唯一约束，而 `version_id="null"` 在同一桶内允许多个 key 有各自 Null Version——但同一个 key 的多个 PUT 在 Suspended 期间应互相覆盖（单行 upsert）。这意味着：

- `objects` 表需要一个新的 `(tenant, bucket, key, is_null_version)` 部分唯一索引
- 或者保留一个单独的 `null_versions` 列在 `objects` 表中

建议在 `objects` 表增加 `current_null_version_id TEXT` 字段，指向 `object_versions` 表中 `version_id='null'` 的那一行，这样 Suspended 状态下找它只需一次索引查找。

### 2. 🔴 预签名 URL 约束

**这是五个方向中 ROI 最高的。** 300 行代码 + 零 schema 变更 = 消除一个明确的 CVE 级安全缺口。

值得扩展的一个设计点：`MaxUses` 的并发计数在 Local 后端没有 Redis 的支持下，可以使用 SQLite 的 `UPDATE counters SET uses = uses + 1 WHERE ... RETURNING uses` 原子递增。S3 后端可以用 AWS SDK 原生的 `PresignPut` 条件参数（`x-amz-content-sha256` 和 `Content-Type` 已在 AWS SDK 签名范围内），这意味着：

- Local 后端需要手写签名约束
- S3 后端只需传参给 AWS SDK——工作量低于 Local 端

### 3. 🔴 凭据审计轨迹

**与 v26 的 `AuthInfo` 分析产生共振。** v26 已指出 `AuthInfo` 缺少凭据标识，此处是具体的 Phase 1/Phase 2 方案。

一个被忽视的细节：`auth_middleware.go` 目前将 `AuthInfo` 注入 context 的 key 是 `mw.CtxKeyTenant`（只取了 tenant）。实际上整个 `AuthInfo` 结构体应该存入 context，然后在 `AccessLog` 中间件中提取。这意味着：

```go
// 在 auth_middleware.go 中
ctx = context.WithValue(ctx, mw.CtxKeyAuthInfo, authInfo)
// 而不是当前的: ctx = context.WithValue(ctx, mw.CtxKeyTenant, authInfo.Tenant)
```

这需要同时修改所有 handler 中 `mw.TenantFrom(ctx)` 的调用点——约 30+ 处。建议在 `middleware/context.go` 中保留 `TenantFrom` 的便捷方法（从 `AuthInfo` 中提取），同时新增 `AuthInfoFrom`。

### 4. 🟠 标签生命周期

**正确识别了缺失。** 但这里有一个被低估的实现复杂度：规则引擎的 N+1 问题。

当前 `sweepExpired` 是单条 SQL 全表扫描。升级到 N 条规则后，如果每个规则各自执行一个 JOIN `object_tags` 的查询，对于一个 50 规则的桶，每个 reconcile 周期执行 50 次 JOIN 查询——在 1000 万对象的规模下，这是不可行的。

建议的工程方案：**规则分批评估**——将规则的模式预先计算为一条 UNION SQL，由数据库一次性返回所有匹配的对象 ID 及其对应的规则 ID 列表：

```sql
SELECT o.id, array_agg(r.rule_id) as matched_rules
FROM objects o
LEFT JOIN object_tags t ON ...
CROSS JOIN bucket_rules r
WHERE ... -- 所有规则的条件 UNION
GROUP BY o.id
```

### 5. 🟠 跨桶复制

**分析扎实。** 补充一个重要的边界情况：**跨后端 Copy 的动静判断**。

如果源和目标在同后端（如同为 S3 或同为 Local），调用 `Copy` 原语。但如果是 Local→S3（跨后端），回退到 GET+PUT 是正确的。然而，存在一个灰色地带：**同一个 S3 后端的不同 bucket 是否在同一 region**？AWS S3 的 `CopyObject` 只能在同一 region 内跨 bucket 使用（跨 region 需要 `Replication` 或 `CopyObject` 配合 `x-amz-source-region`）。当前的 `S3Storage` 没有 region 信息暴露给 Service 层做判断。

建议在 `storage.Storage` 接口上增加一个可选方法：

```go
type StorageInfo struct {
    Backend string // "local" | "s3" | "oss" | "cos"
    Region  string // for cloud backends
}
```

这样 Service 层可以判断跨后端复制的可行性。

---

## 优先级排序的再审视

| 顺序 | 方向 | 我的评估 |
|------|------|---------|
| **P0** | #2 预签名 URL 约束 | 第零优先级——这是已上线的安全缺口，不是功能缺口 |
| **P1** | #3 凭据审计 | 与 v26 的 AuthInfo 扩展形成复合依赖，会影响 P2 的跨租户授权 |
| **P1** | #1 版本化状态机 | 数据完整性风险，但与 #2/#3 无代码冲突，可并行开发 |
| **P2** | #5 跨桶复制 | 依赖于 #3（跨租户授权需要凭据追踪）；Phase 1（存储层 Copy 原语）可先行 |
| **P2** | #4 标签生命周期 | 依赖于 #1（版本生命周期规则）；Phase 1（Filter 解析）可先行 |

**建议实际执行顺序：** #2 → #3(Phase 1) → #1 → #5(Phase 1) → #3(Phase 2) → #4(Phase 1) → #5(Phase 2-3) → #4(Full)

### 关于既有分析覆盖

文档中每个方向标注"零覆盖 — 无独立深度分析"，我验证确认这五个方向**确实**在之前的 v1–v26 系列中未被独立分析过。Null Version 语义、预签名约束绑定、凭据级审计、标签生命周期过滤器、跨桶 Copy 原语——这五条都是全新的盲区发现，与 README.md 中的五条方向（存储类生命周期、内容寻址去重、多层缓存、WAL 回放、元数据查询引擎）也无重叠。这是一份高质量的补充分析。
