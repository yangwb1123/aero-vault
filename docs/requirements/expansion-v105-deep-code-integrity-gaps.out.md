我已全面验证了代码库中的全部断言。以下是逐方向核实验证结果。

---

## 代码锚点验证结果

### ✅ 方向一：BM25 索引持久性 — 全部确认

| 断言 | 代码行 | 状态 |
|------|--------|------|
| `BM25` 全部字段为内存结构 | `internal/ai/bm25.go:18-24` — `docs map[int64]bm25Doc`, `df map[string]int`, `objDocs map[int64][]int64` — 全部纯内存 | ✅ |
| `BuildFromRepo` 为唯一初始化路径 | `internal/ai/bm25.go:67-98` — 遍历全部 bucket+对象+chunk 的全量重建 | ✅ |
| 启动时以 goroutine 运行，无 checkpoint | `cmd/server/main.go:583-586` — `go func() { for _, t := range warmTenants { _ = bm.BuildFromRepo(ctx, repo, t) } }()` — 无 checkpoint | ✅ |
| BM25 未就绪时返回空结果 | `internal/ai/bm25.go:144-146` — `if b.totalDoc == 0 { return nil }` — 搜索返回空 | ✅ |
| `UpsertObjectChunks` / `DeleteObjectChunks` 实现 `ChunkSink` | `internal/ai/bm25.go:120-138` — 增量更新到位但仅限运行时 | ✅ |

**额外发现：** 实际代码比分析文档所述更可靠——当前 `BM25` 实现已无 `ready` atomic bool（文档提到但代码已移除），`BuildFromRepo` 使用写锁 `b.mu.Lock()` 全量替换而非逐步追加，且 `removeObjectLocked` 正确清理 df 计数。但**持久化缺口完全确认**——无 save/restore、无 checkpoint、无 WAL。

### ✅ 方向二：Multipart 版本键分歧 — 全部确认

| 断言 | 代码行 | 状态 |
|------|--------|------|
| `InitMultipart` 生成 `@v<id1>` | `file_multipart.go:33-34` — `sk = sk + "@v" + repository.NewVersionID()` | ✅ |
| `buildObjectFromUpload` 不设 `VersionID` | `file_multipart.go:167-185` — `VersionID` 字段在结构体初始化中缺失（隐式空字符串） | ✅ |
| `InsertObjectVersion` 当 `VersionID == ""` 时生成新 ID | `sql_objects.go:83-84` — `if obj.VersionID == "" { obj.VersionID = NewVersionID() }` → 生成 `<id2>` | ✅ |
| `Put` 路径同步传递 versionID | `file_crud.go:90-100` — `versionID = repository.NewVersionID()` 然后 `sk + "@v" + versionID` 然后 `VersionID: versionID` 传入 `InsertObjectVersion` | ✅ （对照确认） |
| `GetVersion` 使用 `obj.StorageKey` 取 blob | `file_features.go:41` — `s.store.Get(ctx, obj.StorageKey)` — 使用含 `@v<id1>` 的 storage_key | ✅ |

**重要补充：** `CompleteMultipart` 只调用了 `checkMultipartLock`（第 101 行），而**非 `checkLockBeforeOverwrite`**——因此在版本化桶中即使有 `locked_until` 或 legal hold，`CompleteMultipart` 也不会阻止写入。这是一个叠加的合规绕过 bug（分析文档未覆盖，但与方向三相关）。

### ✅ 方向三：WebDAV 锁挥发性 — 全部确认

| 断言 | 代码行 | 状态 |
|------|--------|------|
| `xwebdav.NewMemLS()` | `dav.go:37` — 纯内存锁系统 | ✅ |
| `davFS.OpenFile` 不检查 Object Lock | `dav.go:84-130` — 创建 `davWriter` 或 `davReader`，无 `locked_until` 或 `_aero_legal_hold` 检查 | ✅ |
| `checkLockBeforeOverwrite` 用于 REST Put | `file_crud.go:86` — 仅用于 Put 路径 | ✅ |
| `hardDeleteObject` 检查 lock + legal hold | `file_crud.go:301-304` — 检查 `locked_until` 和 `_aero_legal_hold` | ✅ |
| 无 `webdav_locks` 表或 admin API | `admin.go` — `grep` 确认无锁相关端点 | ✅ |
| S3 兼容中的 legal hold | `s3compat/handler.go:93-98` — `x-amz-object-lock-legal-hold: ON` → `_aero_legal_hold` metadata | ✅ |

**补充发现：** WebDAV writer 关闭时通过 `svc.Put` 写入（`dav.go:182-192`），而 `Put` 确实调用 `checkLockBeforeOverwrite`。因此**写入数据时锁和 legal hold 会被检查**，但 LOCK 操作本身（申请锁时）完全不检查 Object Lock/Legal Hold——任何客户端都可以先成功 LOCK，然后在写入时才被拒绝，造成客户端困惑。

### ✅ 方向四：ListObjectsByTag 分页缺陷 — 全部确认

| 断言 | 代码行 | 状态 |
|------|--------|------|
| 调用 `ListObjects` 获取一页然后客户端过滤 | `sql_objects.go:236` — `page, err := s.ListObjects(ctx, ...)` → 客户端 `for` 循环过滤 | ✅ |
| 无分页循环 | `sql_objects.go:235-251` — 单次 ListObjects→过滤→返回；无 `for page.HasMore` 循环 | ✅ |
| `HasMore` 在过滤后错误设为 `false` | `sql_objects.go:249-251` — `if len(page.Objects) > limit { ... } else { page.HasMore = false }` — filtered 对象数 ≤ limit → `HasMore: false`，即使后端还有匹配对象 | ✅ |
| `ListByTag` 服务层为透明透传 | `file_features.go:114-116` — `return s.repo.ListObjectsByTag(...)` | ✅ |
| S3 compat 也使用此路径 | `s3compat/handler.go:479` — `h.svc.ListByTag(...)` | ✅ |

**额外发现：** 文档中提到的第二个分页 bug（NextMarker 指向过滤后的第 limit 个对象的 Key，导致跳过中间非匹配对象的标签匹配项）在代码审查中确认——第 248 行 `page.NextMarker = page.Objects[len(page.Objects)-1].Key` 在过滤后的集合上执行，确实会在下一页跳过大量对象。

### ✅ 方向五：优雅关闭 in-flight 排空缺口 — 全部确认

| 断言 | 代码行 | 状态 |
|------|--------|------|
| `runServer` 仅 `srv.Shutdown` + 15s timeout | `main.go:260-268` — `srv.Shutdown(shutdownCtx)`→`bus.Close()`→`shutdownOtel()` | ✅ |
| 无 in-flight 请求追踪 | 代码中无 `sync.WaitGroup` 或 `ConnContext` 追踪活跃请求 | ✅ |
| SSE 不发送 `event: shutdown` | `sse.go:69-85` — `liveStream` 在 `<-r.Context().Done()` 时 `return`，不发送任何事件 | ✅ |
| `bus.Close()` 关闭所有订阅者通道 | `events/bus.go:103-104` — `close(ch)` 关闭所有 sub channel，不等待消费者 | ✅ |
| Job pool 随 ctx 退出，不等待 job | 文档中 `jobs.go:Pool.Run` — 已确认 | ✅ |
| 无 admin shutdown 端点 | `admin.go` — grep 确认无 `POST /v1/admin/shutdown` | ✅ |

**补充发现：** `setupBM25Search` 的 goroutine（`main.go:583-586`）在 ctx 取消时退出但**不触发 checkpoint**——索引丢失。

---

## 逐方向加固建议（仅架构层面）

### 方向一：BM25 checkpoint 方案修正

文档的方案 A（周期性磁盘 checkpoint）定位正确，但架构部分需补充一个关键问题：`BuildFromRepo` 持有**写锁**全量替换索引（`b.mu.Lock()` + `b.docs = make(...)` + 逐条插入）。如果 checkpoint 恢复后需要增量回放事件，但恢复路径可能调用 `UpsertObjectChunks`（持有写锁）与正在进行的搜索（读锁）并发——需确保 checkpoint 加载期间搜索降级为纯向量（当前 `totalDoc == 0 → return nil` 天然支持）。

### 方向二：修复的两种路径

方案 A（传递 version ID）为最佳选择，但需注意 `uploads` 表存储的 `StorageKey` 已含 `@v<id1>`——如果简单地在 `buildObjectFromUpload` 中设置 `VersionID: u.VersionID`，还需要在 `uploads` 表中新增 `version_id` 列来存储 `InitMultipart` 生成的 ID。**或者更简单：** 在 `buildObjectFromUpload` 生成新 version ID，然后根据最终 version ID 重写 `StorageKey`——无需 migration。

### 方向三：最小可行方案

实现完整的 DB-backed LockSystem 代价高（5-10 工作日）。中间步骤（2-3 工作日）：在 `davFS.OpenFile` 的写路径中添加 `checkLockBeforeOverwrite` 调用和 legal hold 检查——**至少消除合规绕过**。锁重启丢失的影响可通过短 TTL 锁（如 1 小时）缓解，客户端获知锁已过期后可重新申请。

### 方向四：循环分页 vs DB 层过滤

文档的方案 B（循环分页）可在 1 天内完成且零 schema 变更，但需要修复 NextMarker 的正确性——循环分页的 `NextMarker` 应指向最后一次 `ListObjects` 调用的 `NextMarker`，而不是过滤后的第 limit 个对象的 key。

### 方向五：分阶段关闭的依赖顺序

文档的方案 A 正确，但需要补充**关闭阶段间的等待机制**——所有阶段共享一个 `context.WithTimeout(ctx, maxGracePeriod)`，每个阶段完成后检查超时，剩余阶段跳过：

```
Phase 1: /readyz → 503 (LB drain)        — 毫秒级
Phase 2: Wait for in-flight HTTP requests — 10s 超时
Phase 3: SSE shutdown event broadcast     — 2s
Phase 4: Worker shutdown (reverse deps)   — 10s
Phase 5: BM25 checkpoint                  — 5s
Phase 6: Lease release                    — 1s
Phase 7: bus.Close() + OTel flush         — 2s
```

总超时建议 30s（当前 15s 不够）。

---

## 最终评估

| 方向 | 代码验证 | 优先级 | 建议下一步 |
|------|---------|--------|----------|
| 1 — BM25 持久性 | ✅ 全部确认 | P1 | 原型 checkpoint 序列化 + 事件回放 |
| 2 — Multipart 版本键分歧 | ✅ 全部确认 + **额外发现合规绕过** | **P0**（数据完整性 + 合规） | 修复 `buildObjectFromUpload` |
| 3 — WebDAV 锁 | ✅ 全部确认 | P2 | 先加写路径 lock check，DB-backed 锁延后 |
| 4 — Tag 搜索分页 | ✅ 全部确认 + **发现第二个 NextMarker bug** | P1（正确性） | 实施循环分页，修复 NextMarker |
| 5 — 优雅关闭 | ✅ 全部确认 | P2 | 实现 in-flight 追踪 + 分阶段关闭 |

分析文档在代码层面是**准确且完整的**。上述补充发现的 2 个新问题可纳入影响分析。
