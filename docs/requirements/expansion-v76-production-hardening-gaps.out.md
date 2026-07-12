Excellent — I've verified all four code anchors. Let me present my findings with a critical correction on Direction 3.

---

## 验证结论

### 方向一：`ListParts` 全量加载 → ✅ **完全确认，证据完整**

| 锚点 | 状态 | 说明 |
|------|------|------|
| `repository/repository.go:306` | ✅ | `ListParts(ctx, uploadID) ([]PartRecord, error)` — 无分页参数 |
| `repository/sql_uploads.go:108-120` | ✅ | SQL 无 `LIMIT`/`OFFSET`/`> marker` |
| `s3compat/extra.go:230-250` | ✅ | Go 层遍历全量 slice 做手动分页 |
| `service/file_multipart.go:93` | ✅ | `CompleteMultipart` 同样全量加载所有 parts |

**补充发现：** `ListParts` 接口签名之后还有一行 `ListUploads` 已经展示了正确模式——它已经有 `keyMarker`/`uploadIDMarker`/`limit` 三个分页参数（第 303 行）。这进一步印证了 `ListParts` 缺少分页参数是一个可修复的设计疏漏，而不是架构决策。

---

### 方向二：`CopyObject` 忽略 `?versionId` → ✅ **完全确认**

`parseCopySource` (extra.go:48-58) 不仅剥离 `?versionId`，而且连注释都标注了 `optionally with ?versionId`，但实际代码却直接丢弃。这是典型的"注释与实现不一致"code smell。

---

### 方向三：SSE Event Stream 连接保护 → ⚠️ **核心判断有误，但衍生问题成立**

**此方向的分析存在一个关键事实错误。** 当前代码**已经实现了**方向三声称缺失的 Phase 2（channel 生命周期管理）：

```go
// events/bus.go:87-95
func (b *Bus) Subscribe() (<-chan repository.Event, func()) {
    ch := make(chan repository.Event, b.subBuffer)
    b.mu.Lock()
    b.subs = append(b.subs, ch)
    b.mu.Unlock()
    return ch, func() { b.Unsubscribe(ch) }   // ← 已返回 cancel func
}

// events/bus.go:97-107
func (b *Bus) Unsubscribe(ch chan repository.Event) { ... }  // ← 已实现

// api/rest/sse.go:67-68
sub, cancel := h.bus.Subscribe()
defer cancel()     // ← 已正确调用 cancel，goroutine 退出时清理 channel
```

所以分析中描述的"③ sub channel 永不被移除"是不成立的——`defer cancel()` 在 `liveStream` 返回时确实调用了 `Unsubscribe`，channel 被正确移除并关闭。

⚠️ **但这不意味着方向三没有价值。** 以下问题仍然成立且未解决：

| 剩余问题 | 状态 | 风险 |
|---------|------|------|
| 并发连接数上限 (`maxConns`) | ❌ **不存在** | 攻击者或 Bug 客户端可建立无限连接，耗尽 goroutine/内存 |
| 按租户连接配额 | ❌ **不存在** | 一个租户可占满所有连接预算 |
| OTel gauge 指标 (`sse_connections_active`) | ❌ **不存在** | 运维无法判断当前连接数 |
| 没有速率限制 | ❌ **不存在** | `/events/stream` 路由组没有限流（与 `/search`/`/chat` 不同） |

**建议修正描述：** 将方向三重聚焦为"SSE 连接缺乏资源上限"而非"channel 泄漏"。Phase 2 已在代码中存在（`Unsubscribe` + `defer cancel`），需要移除；Phase 1/3 仍然是有效的新增工作。

---

### 方向四：WebDAV Rename 非原子性 → ✅ **完全确认**

`dav.go:157-202` 的确使用了 copy-then-delete 模式，crash 窗口期确实存在于 Put 返回后、Delete 执行前。锁预检也的确缺失。

不过我也注意到 rollback 逻辑存在（但只是 best-effort）：
```go
// dav.go:196-200  — 回滚代码已存在
if err := f.svc.Delete(ctx, tenant, service.DefaultBucket, src2, true); err != nil {
    if delErr := f.svc.Delete(ctx, tenant, service.DefaultBucket, dst, true); delErr != nil {
        f.logger.Warn("webdav rename rollback failed", ...)
    }
    return err
}
```

---

## 修正后的方向优先级排序

建议按以下顺序处理：

| 优先级 | 方向 | 修正后的估计工作量 | 理由 |
|--------|------|-------------------|------|
| **P0** | **SSE 连接上限 + 可观测性**（方向三修正版） | ~30 行（去掉 Phase 2） | 现有代码+5 行计数器即可防资源耗尽，ROI 最高 |
| **P1** | **CopyObject versionId**（方向二） | ~40 行 | 静默数据错误，合规场景下可能是 liability |
| **P2** | **WebDAV Rename 锁预检**（方向四 Phase 1） | ~20 行 | WORM 锁跳过不需要完整重构即可显著改善 |
| **P3** | **ListParts SQL 分页**（方向一） | ~30 行 | 性能优化，影响面最小 |
| **P4** | **WebDAV Rename 幂等性 + Storage.Rename**（方向四 Phase 2/3） | ~100 行 | 需接口变更，风险中 |

需要我基于以上修正后的分析，对任一方向生成具体的实现方案吗？
