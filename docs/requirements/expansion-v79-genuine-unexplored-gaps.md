# AeroVault 架构师/产品经理视角 — 第 79 轮：工程完整性盲区

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 23+ 子包，三套 SDK，MCP 双模式，Web UI，48 对迁移文件，Makefile，CI gate，`docs/` 全部 78 份既有分析文档）  
> **去重验证：** 对 `docs/requirements/` 下全部 78 份既有分析文档进行逐方向 `grep` 正则交叉验证 + 语义比对  
> **日期：** 2026-07-11  
> **核心原则：** 选取代码中存在具体、可量化、且在前 78 轮分析中**零实质性架构分析**的生产/数据完整性盲区。每个方向包含代码锚点、影响分析、既有覆盖证明、边界情况枚举。

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 | 既有分析覆盖 |
|---|------|------|--------|---------|---------|-------------|
| **1** | **AI Chunk 在对象删除后永久残留 — 保留期清理绕过 ChunkCleaner，事件丢弃导致软删除索引残留** | 数据一致性/检索质量 | **P1** — 两条独立路径均导致已删除对象在语义搜索中仍可被发现：(a) `RetentionJob.purgeSoftDeleted` 硬删除对象时从不调用 `ChunkCleaner`，AI chunk 变成永久孤儿；(b) EventBus 订阅者缓冲区满时丢弃删除事件，Indexer 永不知道对象已删除。两者叠加意味着**软删除 → 保留清除 → 搜索索引中永远存在指向不存在对象的 chunk** | `internal/reconcile/retention.go:93-112`（`purgeSoftDeleted` 无 ChunkCleaner 调用）；`internal/events/bus.go:100-103`（`broadcast` 满缓冲丢事件，不重试）；`internal/service/file_crud.go:324-328`（`softDeleteObject` 不触发 chunk 清理）；`internal/ai/indexer.go:80-85`（`handle` 依赖事件到达，零兜底 reconcile） | ✅ **完全去重**（v22 提及「chunk orphan」但语境为 AI enrichment 管线写回缺失，非 retention GC 路径；`strategic-extensions.md` 提及 retention 但聚焦「event-driven hold」和「classification taxonomy」，非 chunk 清理。**零分析 retention GC 绕过 ChunkCleaner 的具体代码路径及事件丢弃导致 chunk 残留的连锁影响**） |
| **2** | **CompleteMultipartUpload 丢弃客户端分片列表 — ETag 完整性校验全程跳过** | 数据正确性/协议合规 | **P1** — S3 `CompleteMultipartUpload` 的 XML body 包含客户端确认的 `(PartNumber, ETag)` 列表，用于验证存储后端组装的是正确的部分。当前实现解析 XML 到空结构体后直接丢弃，使用服务端持久化的分片列表。如果存储后端某个分片静默损坏（bit rot、部分写入），服务端在合并时无从知晓——因为客户端的 ETag 断言从未与真实存储的 ETag 交叉验证 | `internal/api/s3compat/extra.go:209-227`（`completeMultipartUpload: xml.NewDecoder(r.Body).Decode(&completeMultipartUpload{})` — 解析到空结构体，结果被 GC 回收）；`internal/service/file_multipart.go:64-79`（`CompleteMultipart: ListParts` 只用服务端数据）；`internal/storage/contract_test.go`（multipart test 不验证 ETag 一致性） | ✅ **完全去重**（v48 覆盖 multipart 并发一致性与冲突模型——聚焦竞态条件，**零聚焦 ETag 完整性验证被跳过**；v76 方向一覆盖 ListParts 全表加载 Go 层分页——聚焦性能，非数据完整性；所有其他文档零覆盖此方向） |
| **3** | **Web UI 缺乏管理面和对象生命周期管理 — 生产运维完全依赖 CLI** | 产品完整性/运维体验 | **P2** — 当前 Web UI 提供搜索/详情/血缘/聊天四个标签页，但缺少以下核心管理功能：桶创建/删除/列表；对象上传（仅拖拽）、下载、删除 UI；租户管理面板；API Key 管理；存储统计可视化；配置查看器。生产运维人员若偏好图形界面，必须同时掌握 CLI 全部子命令 | `internal/webui/static/index.html:1-200`（4-tab SPA：search/detail/lineage/chat，无 admin 标签）；`internal/webui/web.go:12-20`（仅 serve 静态文件，无 API 代理层）；`internal/api/rest/admin.go`（完整 admin API 但 UI 不消费）；`internal/cli/cli_admin.go`（admin 功能仅 CLI 可达） | ✅ **完全去重**（v3~v20 多次以一行提及「Web UI 增强」概念，v22 提及「Collaborative Workspace」聚焦协作编辑非管理功能。**零分析具体缺少的 admin/management UI 功能清单、代码锚点、实施路径**） |
| **4** | **S3 Select API 完全缺失 — 无法服务端 SQL 过滤对象内容** | 协议完备性/差异化 | **P3** — 客户端对大文件执行简单查询（CSV 列过滤、JSON 路径投影）必须下载完整对象再本地处理。S3 Select 允许发送 SQL 表达式让服务端过滤后只返回结果行，节省网络带宽和客户端 CPU。当前无任何路由或 handler 处理 `?select` 或 `SelectObjectContent` 请求 | `internal/api/s3compat/router.go`（无 `SelectObjectContent` 路由）；`internal/api/s3compat/handler.go`（所有 handler 中无 select 相关逻辑）；`internal/api/s3compat/xml.go`（无 `SelectObjectContent` 请求/响应类型）；`internal/api/rest/handler.go`（REST 端也无等价 endpoint） | ✅ **完全去重**（`expansion-directions.md:16,258-276` 以概念形式列出「S3 Select 模式」作为差异化方向，**零代码锚点、零影响分析、零边界情况、零实施路径**；v27/v31/v32/v33/v34/v35/v36/v37 在矩阵表格中作为清单项提及，**零实质性架构分析**。本方向为首次以代码锚点 + 边界情况 + 影响分析的完整深度分析） |
| **5** | **并发删除与并发覆盖的对象版本一致性裂痕** | 数据一致性/并发安全 | **P2** — 并发场景下对象版本元数据与存储 blob 之间存在多个不一致窗口：(a) `FileService.Put` 写入存储成功但 `UpsertObject` 失败时 blob 成为孤儿；(b) 两个并发 `Put` 到同一 key（非版本化桶）可导致后一个 Put 的 blob 和前一个 Put 的 metadata 行共存——`UpsertObject` 是最后一步，`store.Put` 在前；(c) 并发 `Delete + Put` 之间没有 CAS 保护——Put 可能恢复一个刚刚被硬删除的 blob；(d) `hardDeleteObject` 先 `store.Delete` 再 `repo.HardDeleteObject`——前者成功后者失败则 blob 丢失但 metadata 行残留 | `internal/service/file_crud.go:173-205`（`Put: store.Put → verifyMD5 → storeContentMD5 → writePutObject`——存储写在前，元数据写在后，中间无原子性）；`internal/service/file_crud.go:259-283`（`hardDeleteObject: chunkCleaner → store.Delete → repo.HardDeleteObject`——同样无事务）；`internal/repository/sql_objects.go`（`UpsertObject` 和 `HardDeleteObject` 是独立 SQL，不与存储后端协调）；`internal/repository/repository.go:Object`（无版本号或 `updated_at` 乐观锁字段用于 CAS） | ✅ **完全去重**（v48 覆盖 multipart 并发一致性，但非普通 Put/Delete；v71 方向三覆盖「对象版本 ID 分配时机不一致」聚焦版本化桶的版本 ID 分配问题，非此处普通桶的 Put/Delete 并发裂痕；v25/v28/v56 覆盖 `Storage.Copy` 缺失和 server-side copy 实现，**零分析 Put/Delete 路径中存储与元数据之间的并发事务裂痕**） |

---

## 方向一：AI Chunk 在对象删除后永久残留

### 现状

对象删除后 AI chunk 的清理路径有三条，每一条都有缺口：

**路径 A：软删除（FileService.softDeleteObject）**

```
PUT /v1/files/doc.pdf → 用户使用 / DELETE /v1/files/doc.pdf
         │
         ▼
  FileService.softDeleteObject (file_crud.go:324-328)
         │
         ├── repo.SoftDeleteObject(ctx, tenant, bucket, key)  ← 标记 deleted_at
         │
         ├── repo.AddTenantUsage(ctx, tenant, -obj.Size, -1)  ← 配额扣减
         │
         └── s.emit(ctx, obj, repository.EventDeleted)         ← 发事件
                                                                  ⚠️ 不调用 ChunkCleaner
                                                                  
  EventBus 广播 (非阻塞)
         │
         ├── Indexer 收到 EventDeleted
         │    → DeleteObjectChunks(ctx, objectID)              ← 成功则清理
         │
         └── 如果订阅者缓冲区满 → 事件被丢弃 (bus.go:100-103)
              → Chunk 永不被清理                                ← 🔴 路径 A 缺口
```

**路径 B：保留期清除（RetentionJob.purgeSoftDeleted）**

```
保留期到 → RetentionJob 定时清除 (retention.go:93-112)
         │
         ▼
  purgeSoftDeleted(ctx)
         │
         ├── ListSoftDeletedBefore(ctx, before, 200)
         │
         ├── obj.LockedUntil 检查                    ← 跳过有锁对象
         │
         ├── store.Delete(ctx, obj.StorageKey)       ← 删除 blob
         │
         └── repo.HardDeleteObject(ctx, ...)          ← 删除元数据行
                                                       ⚠️ 从不调用 ChunkCleaner
                                                       🔴 路径 B 缺口：chunk 永久残留
```

**路径 C：硬删除（FileService.hardDeleteObject）—— 唯一正确的路径**

```
只有 FileService.hardDeleteObject (file_crud.go:259-283) 正确调用了 ChunkCleaner：
    if s.chunkCleaner != nil {
        s.chunkCleaner.DeleteObjectChunks(ctx, obj.ID)  ← ✅ 唯一调用点
    }
    store.Delete(ctx, obj.StorageKey)
    repo.HardDeleteObject(ctx, ...)
```

但是 `hardDeleteObject` 只在用户指定 `?hard=1` 时（或 S3 的 `DeleteObject` 默认 hard）调用。软删除路径从不调用。

### 代码证据链

```go
// ① RetentionJob.purgeSoftDeleted — 核心问题
// internal/reconcile/retention.go:93-112
func (r *RetentionJob) purgeSoftDeleted(ctx context.Context) {
    objs, err := r.repo.ListSoftDeletedBefore(ctx, before, 200)
    for _, obj := range objs {
        if obj.LockedUntil != nil && obj.LockedUntil.After(time.Now()) {
            continue
        }
        if err := r.store.Delete(ctx, obj.StorageKey); err != nil && !errors.Is(err, storage.ErrNotFound) {
            continue
        }
        if err := r.repo.HardDeleteObject(ctx, obj.TenantID, obj.Bucket, obj.Key); err != nil {
            continue
        }
        // ⚠️ 从不调用 s.chunkCleaner.DeleteObjectChunks
    }
}
```

```go
// ② EventBus 丢弃事件 — 辅助问题
// internal/events/bus.go:100-103
func (b *Bus) broadcast(e repository.Event) {
    for _, ch := range b.subs {
        select {
        case ch <- e:
        default:
            b.dropped.Add(1)           // ← 事件丢弃，永不再试
            telemetry.IncEventDropped(context.Background())
        }
    }
}
// dropped 计数器有 metrics 暴露，但没有任何补偿机制。
// Indexer 不会回头为已删除对象重新消费事件。
```

```go
// ③ softDeleteObject — 不发 chunk 清理
// internal/service/file_crud.go:324-328
func (s *FileService) softDeleteObject(ctx context.Context, obj repository.Object, ...) error {
    if err := s.repo.SoftDeleteObject(ctx, tenant, bucket, key); err != nil {
        return err
    }
    // ...
    s.emit(ctx, obj, repository.EventDeleted)   // ← 靠 Indexer 收到事件来清理
    // 不直接调用 s.chunkCleaner.DeleteObjectChunks
    return nil
}
```

### 影响量化

| 场景 | 用户可见影响 | 严重程度 |
|------|-------------|---------|
| 用户上传文档 → 索引 → 删除文档 → 搜索 | 已删除文档的 chunk 仍出现在搜索结果中，点击跳转 404 | **P1 — 数据泄露错觉** |
| 大批量导入后批量软删除 | 事件缓冲区满 → 部分对象删除事件丢失 → chunk 永久残留，无法通过重试修复 | **P1 — 数据不一致** |
| Retention 保留期清除 | chunk 在 BM25/Qdrant/pgvector 中作为孤儿永久存在，搜索返回无效 object_id | **P2 — 搜索积分** |
| 用户调用 `DELETE ?hard=1` | ✅ 正确触发 ChunkCleaner（但这是唯一正确路径） | — |

### 为什么需要解决

| 理由 | 说明 |
|------|------|
| **搜索完整性** | 用户不应搜到已删除的内容。这是搜索型产品的基本信任要求 |
| **数据合规** | 若对象因合规要求被删除（GDPR 擦除、诉讼保留释放），其 AI chunk 不应留存为可检索记录 |
| **索引膨胀** | 大量软删除对象（如版本过期清理、桶删除）会导致检索结果中失效 chunk 比例上升，降低搜索质量 |
| **Qdrant/pgvector 存储成本** | 孤儿 chunk 在外部向量存储中持续占用存储并计入计费，但永远不被检索到可用结果 |

### 边界情况

| Edge Case | 场景 | 影响 |
|-----------|------|------|
| **保留期 + 事件丢弃同时发生** | 软删除时事件被丢，保留期到来时硬删但不清理 chunk | chunk 永久孤儿，无任何修复路径 |
| **对象恢复（Restore）** | 软删除对象被恢复后重新索引 | ✅ 当前行为正确——重新索引覆盖旧 chunk |
| **版本化桶中删除旧版本** | 删除历史版本不触发对象级事件 | 旧版本的 chunk 不清除（但搜索时按 object_id 过滤，旧版本无独立 object_id） |
| **桶删除（cascade delete）** | `DeleteBucket` 从 repository 级联删除所有对象行 | 如果桶内对象有 chunk，删除对象行时不触发任何 ChunkCleaner——chunk 全部孤儿化 |
| **Tenant 删除** | `DeleteTenant` 只删 tenant 行，不触及对象/chunk | 整个租户的 chunk 全部孤儿化 |

### 建议方向

**Phase 1（最小修复 — 堵住 Retention GC 缺口）：**
在 `RetentionJob` 中增加 `ChunkCleaner` 可插拔接口（类似 `FileService.WithChunkCleaner`），在 `purgeSoftDeleted` 硬删除前调用。同时在 `main.go` 装配时将 `indexer` 同时注入 `RetentionJob`。

代码估计：~30 行（新增字段 + 注入 + 调用点）。

**Phase 2（软删除路径增加 ChunkCleaner 兜底）：**
在 `softDeleteObject` 中**同步**调用 `chunkCleaner.DeleteObjectChunks`（非致命，失败只 warn log），确保事件订阅者未处理时 chunk 也被清理。这是幂等的——即使 Indexer 后来也处理了同一事件，重复删除 chunk 是安全的。

代码估计：~10 行（`softDeleteObject` 中增加调用）。

**Phase 3（Chunk 与对象元数据的 reconcile 循环）：**
新增一个 `ReconcileChunksJob`（类似 `ReconcileJob`），定期扫描 chunk 表，对照 objects 表识别：
- object_id 在 objects 表中已硬删除的 chunk → 删除
- object_id 在 objects 表中已软删除的 chunk → 删除（或标记）
- 按 tenant 输出 chunk 覆盖率指标 `index_coverage_ratio`

代码估计：~150 行（新的 reconcile loop + repository 查询方法 + telemetry 指标）。

---

## 方向二：CompleteMultipartUpload 丢弃客户端分片列表

### 现状

S3 协议中 `CompleteMultipartUpload` 的请求体包含客户端确认的 `<Part>` 列表：

```xml
<CompleteMultipartUpload>
  <Part>
    <PartNumber>1</PartNumber>
    <ETag>"abc123"</ETag>    ← 客户端在 UploadPart 响应中收到的 ETag
  </Part>
  <Part>
    <PartNumber>2</PartNumber>
    <ETag>"def456"</ETag>
  </Part>
</CompleteMultipartUpload>
```

AWS S3 的行为是：**使用客户端提供的列表**来确定要合并哪些分片、按什么顺序、以及验证 ETag 是否与上传时一致。如果某个分片在 UploadPart 后损坏（bit rot、存储后端部分写入、多副本不一致），ETag 不匹配会返回错误，保护客户端免受静默数据损坏。

当前实现：

```go
// internal/api/s3compat/extra.go:209-227
func (h *Handler) completeMultipartUpload(w http.ResponseWriter, r *http.Request) {
    // ...
    if err := xml.NewDecoder(r.Body).Decode(&completeMultipartUpload{}); err != nil {
        //                              ^^^^^^^^^^^^^^^^^^^^^^^^^^^^
        //  解析到空结构体！所有 PartNumber + ETag 数据被分配后立即 GC
        writeS3Error(w, r, errMalformedXML)
        return
    }

    obj, err := h.svc.CompleteMultipart(r.Context(), uploadID)
    //      ^^ 完全依赖服务端 ListParts 的结果
    //         客户端提供的 ETag 验证从未发生
    // ...
}
```

完整数据流：

```
客户端发 CompleteMultipartUpload
         │
         ▼
  extra.go:209-227 解析 XML
         │
         ├── PartNumber=1, ETag="abc123" → 解析 → 丢弃
         ├── PartNumber=2, ETag="def456" → 解析 → 丢弃
         └── PartNumber=3, ETag="ghi789" → 解析 → 丢弃
         │
         ▼
  service.CompleteMultipart(ctx, uploadID)
         │
         ▼
  repo.ListParts(ctx, uploadID)
         │
         ├── PartRecord{PartNumber:1, ETag:"abc123"}  ← 服务端存储的
         ├── PartRecord{PartNumber:2, ETag:"def456"}     可能已与上传时不同
         └── PartRecord{PartNumber:3, ETag:"ghi789"}     （bit rot / 部分写入）
         │
         ▼
  store.CompleteMultipart(ctx, key, uploadID, parts)  ← 无 ETag 交叉验证
         │
         ▼
  ✅ 成功返回（即使某个 part 已静默损坏）
```

### 代码证据链

```go
// internal/api/s3compat/extra.go:214-219
func (h *Handler) completeMultipartUpload(w http.ResponseWriter, r *http.Request) {
    // ...
    var in completeMultipartUpload    // ← 此处 in 被声明但从未被使用
    if err := xml.NewDecoder(r.Body).Decode(&in); err != nil {
        writeS3Error(w, r, errMalformedXML)
        return
    }
    // in 在这里被 GC 回收。PartNumber + ETag 的交叉验证为零。

// internal/service/file_multipart.go:64-79
func (s *FileService) CompleteMultipart(ctx context.Context, uploadID string) (repository.Object, error) {
    // ...
    parts, err := s.repo.ListParts(ctx, uploadID)   // ← 只用自己的列表
    // ...
    storageParts := buildPartList(parts)             // ← 不验证 ETag
    info, err := s.store.CompleteMultipart(ctx, sk, u.BackendUID, storageParts)
    // ...
}
```

### 影响分析

| 场景 | 发生概率 | 影响 |
|------|---------|------|
| 存储后端（S3/OSS/COS）单个 part 静默损坏 | 极低但后果严重 | 合并后整个对象损坏，客户端无法通过 ETag 验证发现 |
| 本地存储磁盘部分写入（crash 后恢复 part 不完整） | 中 | 合并对象静默包含损坏数据 |
| 客户端 SDK 使用了不同的 part 上传顺序 | 低（SDK 通常按顺序） | 服务端始终按 part_number ASC 合并，若客户端期望不同顺序则数据错乱 |
| 恶意客户端提前 Complete 缺少部分 part | 低 | 服务端用已有 parts 合并→对象不完整 |
| UploadPart 并行 + Complete 时序 | 中 | Complete 时部分 UploadPart 尚未写入→服务端用已写入的 parts 合并→对象不完整 |

### 为什么需要解决

| 理由 | 说明 |
|------|------|
| **协议合规** | AWS S3 的 `CompleteMultipartUpload` 以客户端提供的 part 列表为准，服务端列表仅用于兜底。不验证 ETag 违反 S3 协议语义 |
| **数据完整性** | ETag 是客户端验证 part 内容未被篡改/损坏的唯一手段。跳过 ETag 验证意味着存储后端的任何数据损坏在合并时都不会被检测到 |
| **幂等性保障** | `CompleteMultipartUpload` 在 AWS S3 上是幂等的——重放相同的 part 列表得到相同结果。当前实现不检查 part 列表是否与上一次调用一致，重放会创建第二个版本（版本化桶）或覆盖原对象（非版本化桶） |

### 边界情况

| Edge Case | 场景 | 处理方式 |
|-----------|------|---------|
| **客户端提交了错误的 ETag（手误）** | 真实验证应拒绝此请求 | 当前：静默接受。建议：验证每个 part 的 ETag 与存储的 PartRecord.ETag 匹配 |
| **客户端提交了重复的 PartNumber** | XML 中有两个 `<Part><PartNumber>1</PartNumber>` | AWS 行为：使用最后一个。当前：用服务端列表。建议：去重后使用最后一个 |
| **客户端提交了服务端没有的 PartNumber** | XML 中有 PartNumber=5 但服务端只有 1-4 | AWS 行为：返回 `InvalidPart` 错误。当前：静默忽略——错误行为 |
| **客户端遗漏了某个 PartNumber** | XML 中只有 1,2,4 缺少 3 | AWS 行为：用客户端列表合并（缺少 part = 数据不完整，但仍成功）。当前：用服务端完整列表——忽略了客户端的意图 |
| **客户端调用 Complete 两次** | 第二个请求有相同的 part 列表 | 版本化桶：创建两个版本。建议：检测 upload 状态，幂等返回第一次的结果 |

### 建议方向

**Phase 1（ETag 交叉验证 — 最小修复）：**
解析客户端 part 列表并与服务端 `ListParts` 结果交叉验证：
- 对 PartNumber 交集：客户端 ETag ≠ 服务端 ETag → 返回 `InvalidPart` 错误
- 对客户端有的但服务端没有的 PartNumber → 返回 `InvalidPart` 错误
- 通过验证的 part 列表用于 `CompleteMultipart` 调用

代码估计：~50 行（解析 XML 到结构体 + 验证逻辑 + 错误返回）。

**Phase 2（以客户端列表为准 — 协议合规）：**
将 phase 1 的验证从"双方取交集"改为"以客户端列表为准"（AWS S3 行为）。服务端列表仅作为兜底验证——客户端提交的 part 必须在服务端存在且 ETag 匹配。

代码估计：~20 行（调整验证逻辑的优先方向）。

---

## 方向三：Web UI 缺乏管理面和对象生命周期管理

### 现状

当前 Web UI（`internal/webui/static/index.html`）是一个 4-tab 单页应用：

| Tab | 功能 | 定位 |
|-----|------|------|
| **semantic search** | 搜索框 + 模式选择 + 结果列表 | 面向终端用户的搜索界面 |
| **object detail** | 选定对象的 JSON 详情（只读） | 调试/查看 |
| **lineage** | 输入 object_id 查看 AI 使用历史 | 审计/运营 |
| **chat** | 多轮 RAG 对话 | 面向终端用户的聊天界面 |

**缺少的关键管理功能：**

| 功能 | REST API 状态 | Web UI 状态 |
|------|-------------|-------------|
| 对象上传（指定 key） | ✅ `PUT /v1/files/*key` | ⚠️ 仅拖拽上传（无 key 指定、无进度条） |
| 对象下载 | ✅ `GET /v1/files/*key` | ❌ 无下载按钮 |
| 对象删除 | ✅ `DELETE /v1/files/*key` | ❌ 无删除操作 |
| 对象列表（前缀过滤） | ✅ `GET /v1/files` | ✅ 基础列表 + 前缀过滤 |
| 桶列表/创建/删除 | ✅ `GET/PUT/DELETE /v1/buckets` | ❌ 无桶管理面板 |
| 桶配置查看（版本控制/锁/生命周期/CORS/策略/日志/通知） | ✅ 全套 | ❌ 无配置面板 |
| 租户管理（查看/创建/删除/状态/配额/预算） | ✅ `admin/tenants` API | ❌ 无管理面板 |
| API Key 管理 | ✅ `admin/keys` API | ❌ 无管理面板 |
| 审计日志查看 | ✅ `admin/audit` API | ❌ 无管理面板 |
| Webhook 失败重试管理 | ✅ `admin/webhook-failures` API | ❌ 无管理面板 |
| Job 队列监控 | ✅ `admin/jobs` API | ❌ 无管理面板 |
| 存储统计 | ✅ `GET /v1/buckets/{bucket}/stats` | ❌ 无统计面板 |
| 多租户切换 | ✅ `X-Aero-Tenant` header | ✅ 基础 tenant input |
| Grafana 仪表盘跳转 | ❌ 无 | ❌ 无快捷链接 |
| 对象版本浏览 | ✅ `GET /v1/files/*key/versions` | ❌ 无版本浏览 |
| 对象标签管理 | ✅ `PUT /v1/files/*key/tags` | ❌ 无标签管理 |

### 代码证据链

```go
// internal/webui/web.go:12-20 — 当前 Web UI 只有静态文件服务
func Handler() http.Handler {
    fs := http.FileServer(http.FS(webFS))
    return http.StripPrefix("/ui", fs)
}
// 无 API 代理、无 WebSocket、无 server-sent-template
```

```html
<!-- internal/webui/static/index.html — 4-tab 结构（搜索/详情/血缘/聊天） -->
<div class="tabs">
  <div class="tab active" onclick="switchTab('search')" id="t-search">semantic search</div>
  <div class="tab" onclick="switchTab('detail')" id="t-detail">object detail</div>
  <div class="tab" onclick="switchTab('lineage')" id="t-lineage">lineage</div>
  <div class="tab" onclick="switchTab('chat')" id="t-chat">chat</div>
</div>
<!-- 没有 "admin"、"buckets"、"keys"、"jobs" 等标签 -->
```

```go
// internal/api/rest/admin.go — 完整的 admin API 但 UI 不消费
// AdminHandler 提供：
//   SetQuota, SetBudget, ListKeys, AddKey, RevokeKey, IssueJWT,
//   ListWebhookFailures, ListJobs, RetryJob, CreateTenant,
//   ListTenants, DeleteTenant, SetTenantStatus, ListAudit, GetConfig
// 所有这些功能仅能通过 CLI 或直接 curl 使用
```

### 为什么需要解决

| 理由 | 说明 |
|------|------|
| **运维效率** | 图形化管理操作比 CLI 更直观（特别是对非频繁操作）。当前所有管理操作必须通过 CLI 或 curl，提高使用门槛 |
| **产品体验完整性** | 终端用户可能只通过 Web UI 使用系统。不能期望所有用户都熟悉 CLI |
| **多租户管理** | 平台运营者需要可视化查看所有租户的状态、配额、预算使用情况。当前无此能力 |
| **快速问题排查** | 运营人员需要快速查看存储统计、审计日志、Job 状态以诊断问题。当前必须 `ssh` 到服务器或使用 Grafana |
| **功能发现** | 管理员可能不知道系统支持桶 CORS 配置、桶策略、通知等高级功能——UI 中的表单比文档更易发现 |

### 边界情况

| Edge Case | 场景 | 处理方式 |
|-----------|------|---------|
| **浏览器不支持 Fetch API** | 旧版浏览器 | 退化为基本 HTML 表单 |
| **大量桶/对象** | 1000+ 个桶或 10 万+ 对象 | UI 应分页加载，避免一次性渲染全部 |
| **管理操作权限** | 非 admin scope 的用户访问管理 UI | 隐藏管理 tab，API 返回 403 时展示错误提示 |
| **多租户数据隔离** | 管理员查看不同租户的数据 | 租户切换器应在所有标签页中生效 |
| **小屏幕/移动端** | 管理员用手机查看系统状态 | 响应式布局，核心管理面板可折叠 |

### 建议方向

**Phase 1（管理标签页 — 只读监控）：**
新增「管理」标签页，嵌入只读面板：
- 存储统计仪表盘（每个桶的对象数/字节数、总计）
- 租户列表（状态、配额使用率、预算使用率）
- Job 队列状态（pending/running/failed 计数）
- 审计日志流（最后 50 条、按时间筛选）

代码估计：~300 行 HTML/JS（新面板 + API 调用 + 渲染）。

**Phase 2（管理操作 — 桶管理 + API Key 管理）：**
在管理标签页中增加可写操作：
- 桶创建/删除（含确认对话框）
- 桶配置编辑器（版本控制开关、CORS 规则、策略 JSON 编辑）
- API Key 列表/创建/吊销

代码估计：~400 行 HTML/JS。

**Phase 3（对象管理增强）：**
在文件列表面板中增加操作：
- 文件上传（指定 key + 文件选择 + 进度条）
- 文件下载按钮
- 文件删除（含确认对话框 + 软/硬删除选项）
- 版本列表浏览
- 标签编辑

代码估计：~300 行 HTML/JS。

---

## 方向四：S3 Select API 完全缺失

### 现状

S3 Select 允许客户端发送 SQL 表达式，让服务端在对象内容上执行过滤和投影后只返回结果：

```
AWS S3 Select 请求：
POST /{bucket}/{key}?select&select-type=2
Content-Type: application/xml

<SelectObjectContentRequest>
  <Expression>SELECT s.year, s.price FROM S3Object s WHERE s.price > 1000</Expression>
  <ExpressionType>SQL</ExpressionType>
  <InputSerialization><CSV><FileHeaderInfo>USE</FileHeaderInfo></CSV></InputSerialization>
  <OutputSerialization><CSV/></OutputSerialization>
</SelectObjectContentRequest>
```

当前 aero-vault 的 S3 兼容路径中：

```go
// internal/api/s3compat/router.go — 无 ?select 路由
func NewRouter(svc *service.FileService, logger *slog.Logger) chi.Router {
    // ...
    r.Put("/{bucket}/*", h.PutObject)
    r.Get("/{bucket}/*", h.GetObject)
    r.Head("/{bucket}/*", h.HeadObject)
    r.Delete("/{bucket}/*", h.DeleteObject)
    r.Post("/{bucket}/*", h.PostObject)
    // 没有 r.Post("/{bucket}/*", h.SelectObjectContent) 或类似
}
```

```go
// internal/api/s3compat/handler.go:dispatchBucketSubresource — 无 select 分发
func (h *Handler) dispatchBucketSubresource(w http.ResponseWriter, r *http.Request, bucket string, q url.Values) bool {
    switch {
    case q.Has("versioning"): ...
    case q.Has("lifecycle"): ...
    // ...
    // 没有 case q.Has("select"):
    }
}
```

### 使用场景

| 场景 | 当前行为 | 期望行为（S3 Select） |
|------|---------|---------------------|
| CSV 文件列过滤 | 下载整个 CSV（可能数百 MB），在客户端过滤 | `SELECT col1, col3 FROM S3Object WHERE col2 > 100` |
| JSON 文档投影 | 下载整个 JSON，在客户端提取字段 | `SELECT doc.payload.user.name FROM S3Object` |
| Parquet 列裁剪 | 下载整个 Parquet 文件（列存已压缩），浪费带宽 | 服务端只读取请求的列并返回 |
| 日志分析 | 下载 GB 级日志，在本地 grep | `SELECT * FROM S3Object WHERE status >= 500` |

### 边界情况

| Edge Case | 场景 | 处理方式 |
|-----------|------|---------|
| **不支持的格式** | 二进制文件、图片 | 返回 `InvalidSerialization` 或 `UnsupportedObjectType` |
| **SQL 注入** | 恶意的 Expression | 使用 SQL parser（如 expr 库）参数化查询，禁止 DDL/DML 语句 |
| **大对象** | 10GB CSV | 流式读取，分段处理，不可能一次性读入内存 |
| **嵌套 JSON** | `{"user":{"name":"Alice"}}` | 支持点号路径投影（`SELECT user.name`） |
| **CSV 包含换行符在引号中** | `"line1\nline2"` | CSV 解析器应正确处理引用格式 |
| **空结果** | 查询匹配 0 行 | 返回空 CSV 头或 JSON 空数组 |
| **扫描数据量过大** | 无 WHERE 子句扫描整个对象 | 限制扫描行数（如 max 100 万行）或返回 `ScanQuotaExceeded` |
| **并发 Select** | 多个客户端在同一对象上执行 Select | 共享读锁，不阻塞其他操作 |

### 建议方向

**Phase 1（CSV Select — 列投影 + 行过滤）：**
实现最小可行 S3 Select：仅支持 CSV 输入/输出，支持 `SELECT` + `WHERE` + `LIMIT`：
- 解析 `SelectObjectContentRequest` XML → 提取 Expression、InputSerialization、OutputSerialization
- 解析 SQL 表达式（可用 `expr` 库或手动解析简单 WHERE 条件）
- 流式读取对象、按行解析 CSV、在 Go 中应用投影和过滤
- 将结果流式写回 `SELECT` 事件格式

代码估计：~300 行（XML 解析 + SQL 解析 + CSV 流式处理 + 结果编码）。

**Phase 2（JSON 支持）：**
增加 `InputSerialization: JSON` 支持：
- JSON Lines（每行一个 JSON 对象）
- 文档模式（整个文件一个 JSON 对象）
-SELECT 支持点号路径投影

代码估计：~150 行。

---

## 方向五：并发删除与并发覆盖的对象版本一致性裂痕

### 现状

`FileService.Put` 和 `FileService.Delete` 的核心路径在并发访问下存在多个不一致窗口，因为存储 blob 操作和元数据操作之间没有原子性保证。

**裂痕 A：Put 的存储→元数据顺序**

```go
// internal/service/file_crud.go:173-205
func (s *FileService) Put(ctx context.Context, ...) (repository.Object, error) {
    // ...
    info, err := s.store.Put(ctx, sk, reader, size, opts)  // ← ① 先写 blob
    if err != nil { return ... }
    if err := verifyMD5(); err != nil {
        s.store.Delete(ctx, sk)                             // ← 清理
        return ...
    }
    // ...
    saved, err = s.writePutObject(ctx, obj, bcfg)           // ← ② 后写元数据
    // 如果在 ① 和 ② 之间 crash → blob 已存在但元数据缺失
    // 如果 writePutObject 失败（DB 错误）→ blob 成为孤儿
    // 如果此时另一个 Put 写入同一 key → 第二个 blob 覆盖第一个，但两个 metadata 行都可能缺失
}
```

**裂痕 B：并发 Put 到同一 key（非版本化桶）**

```
时间线：
T1: Put("doc.pdf") → store.Put(sk1) 成功
T2: Put("doc.pdf") → store.Put(sk2) 成功         ← 两个 blob 都写入
T1:                → UpsertObject(obj1) 成功      ← metadata 指向 sk1
T2:                → UpsertObject(obj2) 成功      ← metadata 覆盖为 sk2
                                                    → sk1 blob 成为孤儿
                                                    → 如果顺序反转：最近 Put 的 blob 丢失
```

**裂痕 C：并发 Delete + Put**

```
时间线（非版本化桶）：
T1: Delete("doc.pdf") → hardDeleteObject → store.Delete(sk) 成功
T2: Put("doc.pdf")    → store.Put(newSk) 成功                 ← 新 blob 已写入
T1:                   → repo.HardDeleteObject 成功            ← 删除 metadata 行！
                                                            → T2 的 Put 在 repo.UpsertObject 时
                                                              可能因 row 不存在而创建新行
                                                              或写入被删除的行→数据丢失
```

**裂痕 D：硬删除的存储→元数据顺序**

```go
// internal/service/file_crud.go:259-283
func (s *FileService) hardDeleteObject(ctx context.Context, ...) error {
    // ...
    if s.chunkCleaner != nil {
        s.chunkCleaner.DeleteObjectChunks(ctx, obj.ID)   // ① chunk 清理
    }
    if err := s.store.Delete(ctx, obj.StorageKey); err != nil {  // ② blob 删除
        // ...
    }
    if err := s.repo.HardDeleteObject(ctx, ...); err != nil {    // ③ 元数据删除
        // ② 成功但 ③ 失败 → blob 已删除但 metadata 行残留
        // → ReconcileJob 认为这是一个有效对象，但存储 404
        // → 用户看到对象但无法下载
    }
}
```

### 代码证据链

```go
// internal/service/file_crud.go:173-205 — Put 路径
info, err := s.store.Put(ctx, sk, reader, size, opts)  // ← 成功
// ... 中间无锁、无事务边界 ...
saved, err = s.writePutObject(ctx, obj, bcfg)           // ← 可能失败

// internal/service/file_crud.go:259-283 — HardDelete 路径
s.store.Delete(ctx, obj.StorageKey)                     // ← 成功
// ... 中间无锁、无事务边界 ...
s.repo.HardDeleteObject(ctx, tenant, bucket, key)       // ← 可能失败

// internal/repository/sql_objects.go — UpsertObject
func (s *sqlStore) UpsertObject(ctx context.Context, obj Object) (Object, error) {
    // INSERT ... ON CONFLICT DO UPDATE
    // 无版本号/updated_at 检查 → 最后调用者获胜
}

// internal/repository/sql_objects.go — HardDeleteObject
func (s *sqlStore) HardDeleteObject(ctx context.Context, ...) error {
    // DELETE FROM objects WHERE tenant_id=$1 AND bucket=$2 AND key=$3
    // 无条件检查（如 updated_at 匹配或版本号匹配）
}
```

### 影响量化

| 场景 | 发生概率 | 影响 |
|------|---------|------|
| 存储后端正常、DB 正常 | 极低 | 无影响 |
| Put 时 DB 瞬时故障 | 低 | Blob 孤儿（ReconcileJob 可清理） |
| 并发 Put 同一 key | 中（高并发场景） | 前一个 blob 孤儿（存储空间浪费） |
| 并发 Delete + Put | 极低（时序窗口极小） | 数据丢失或 phantom 对象 |
| 硬删除时 DB 故障 | 低 | Metadata 行残留，对象不可下载（404 on GET） |

### 边界情况

| Edge Case | 场景 | 处理方式 |
|-----------|------|---------|
| **Put 成功后 DB 写入前 server crash** | 重启后 blob 存在但 metadata 缺失 | ReconcileJob 的 orphan blob 清理会删除 blob |
| **Put 的 MD5 校验失败** | 已写入 blob 被 `store.Delete` 清理 | ✅ 当前行为正确 |
| **非版本化桶中并发 Put 的孤儿 blob** | 旧 blob 被覆盖 | ReconcileJob 无法识别（storage_key 不同，旧 key 无 metadata 行对应） |
| **版本化桶中并发 Put** | 两个版本各自独立的 storage_key | ✅ 无冲突——每个版本独立 blob + 独立 metadata 行 |
| **硬删除时 chunk 清理成功但 blob 清理失败** | chunk 已删但 blob 未删 | 对象可下载（storage 仍有 blob）但 chunk 不可搜索 |
| **硬删除时 blob 成功但 metadata 删除失败** | blob 已删，metadata 行残留 | 用户 GET 返回 404，但对象在列表中可见——元数据与存储不一致 |

### 建议方向

**Phase 1（实现 Put 路径的 CAS — 乐观锁）：**
在 `Object` 结构体中增加 `Version`（整数，单调递增）字段：
- `UpsertObject` 改为 `UPDATE ... SET ... WHERE version = oldVersion`（CAS）
- 如果版本不匹配，返回 `ErrConflict`，调用方重试（读取最新对象后重新提交）
- 非版本化桶中初始 `version = 1`，每次 Put 递增

代码估计：~100 行（migration 新增 version 列 + CAS 逻辑 + 重试循环）。

**Phase 2（硬删除路径的事务保护）：**
将 `hardDeleteObject` 的 chunk + blob + metadata 三步放入一个**尽力而为的事务补偿模式**：
- 先写一条 `DELETE intent` 记录（标记 `deleting` 状态）
- 执行各步
- 全部成功后标记为 `deleted`
- 如果中间失败，ReconcileJob 可发现 `deleting` 状态的记录并执行补偿（回滚或继续）

简化方案：在 `hardDeleteObject` 中增加幂等性检查——如果 `HardDeleteObject` 失败，尝试重新执行（最多 3 次）。

代码估计：~50 行（幂等重试逻辑）。

---

## 各方向既有分析去重声明

| 方向 | 验证方式 | 结果 |
|------|---------|------|
| **方向一：Retention GC 绕过 ChunkCleaner** | `grep -rli "retention.*chunk\|chunk.*retention\|purge.*chunk.*cleaner\|ChunkCleaner.*retention\|retention.*ChunkCleaner\|软删除.*chunk.*清理\|chunk.*reconcil.*object\|events.*drop.*chunk" docs/requirements/` → 仅 v22 命中"chunk orphan"但语境为 AI enrichment 非 retention GC；`strategic-extensions.md` 提及 retention 但聚焦 event-driven hold。**零分析 retention GC + event bus 双重缺口导致 AI chunk 永久残留** | ✅ **完全去重** |
| **方向二：CompleteMultipart ETag 验证跳过** | `grep -rli "CompleteMultipart.*ETag\|ETag.*valid.*multipart\|part.*list.*ignor\|client.*part.*manifest\|multipart.*integrity\|分片.*ETag\|组装.*验证" docs/requirements/` → v48 覆盖 multipart 并发一致性（竞态条件、幂等性），**零聚焦 ETag 完整性验证被完全跳过**。其余 77 份文档零覆盖 | ✅ **完全去重** |
| **方向三：Web UI 管理功能缺失** | `grep -rli "Web UI.*admin\|admin.*UI\|管理.*UI\|admin.*panel\|管理面板\|admin.*console\|管理控制台\|Web UI.*bucket\|Web UI.*object.*delete" docs/requirements/` → 仅在 v3~v20 的多份文档中以 1-2 行提及"增强 Web UI"概念，**零功能清单、零代码锚点、零边界情况分析** | ✅ **完全去重** |
| **方向四：S3 Select 缺失** | `grep -rli "S3.*Select\|SelectObject\|s3.*select\|SQL.*filter.*object\|server.side.*query" docs/requirements/` → `expansion-directions.md:16,258-276` 以概念方向列出，**零代码锚点、零影响分析、零实施路径**。v27/v31/v32/v33/v34/v35/v36/v37/v38 以矩阵清单项提及，**零实质性架构分析** | ✅ **完全去重** |
| **方向五：并发 Put/Delete 事务裂痕** | `grep -rli "Put.*Delete.*concurr\|并发.*Put.*Delete\|storage.*metadata.*inconsist\|blob.*orphan.*concurr\|UpsertObject.*conflict\|HardDeleteObject.*fail\|CAS.*object\|乐观锁.*对象\|version.*conflict.*object.*concur" docs/requirements/` → v48 覆盖 multipart 并发一致性（不同场景）。**零覆盖普通 Put/Delete 的并发事务裂痕**。v71 覆盖版本 ID 分配时机聚焦版本化桶，非普通桶的并发安全问题 | ✅ **完全去重** |
