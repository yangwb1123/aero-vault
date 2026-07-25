# AeroVault 高价值扩展方向 v51 — 存储语义深度、数据面可靠性与跨协议一致性缺口

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部子包，约 55K `.go` + 三套 SDK + `deploy/*` + 全部 24 对迁移文件 + 全部 50 份既有的 `docs/requirements/*.md` 分析文档 + `ROADMAP.md` + `CHANGELOG.md` + `TODO.md` + `extensions*.md`）
>
> **分析视角：** 资深架构师 / 产品经理 — 在 50 期 expansion 分析（255+ 方向，~600,000+ 字分析文本）基础上，聚焦 **存储引擎数据面正确性、运行时一致性与默认后端的可靠性根基**。50 期分析已穷尽了"加什么功能"和"做什么产品"的维度，但以下方向触及的是已经实现的功能中 **执行路径上的语义精确性和默认路径的可靠性**。
>
> **去重方法：** 对 `docs/requirements/` 下全部 50 份既有分析文档（`expansion-directions.md` ~ `expansion-v50-genuine-unexplored-frontiers.md`）+ `ROADMAP.md` + `CHANGELOG.md` + `TODO.md` + `extensions*.md` 进行穷尽式关键词验证。每个方向在既有文档中 **零实质性独立架构分析**（表格一行、举例提及、单一子点均不构成实质性分析）。
>
> **分析日期：** 2026-07-10

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心矛盾 | 50 期覆盖 |
|---|------|------|--------|---------|-----------|
| **1** | **本地存储引擎崩溃安全：数据/元数据双写窗口与不完整多分片恢复** | 数据面可靠性 | **P1** — 默认存储后端的写入路径存在 crash 窗口，对象可能在重启后处于数据有元数据无或有数据元数据无的半写状态；多分片合并路径也存在双重窗口 | ❌ **零覆盖**（v15 写入日志方向侧重 repo 层数据库事务，非本地文件存储 data/meta 双写问题；v38 优雅关闭方向覆盖进程退出行为，未覆盖写入路径内的原子性）|
| **2** | **版本化桶的写-删-写一致性 & Delete Marker 语义缺失** | 数据语义/S3 合规 | **P1** — 版本化桶中 DELETE 使用软删除而非 S3 Delete Marker 语义，导致 delete→recreate 序列的事件时序与背景工作者行为不可预测；与 S3 SDK 客户端存在语义鸿沟 | ❌ **零覆盖**（v4/v16/v17 提及 delete marker 概念但仅作为路线表格列举，从未作为独立方向做架构分析）|
| **3** | **S3 多分片上传完成竞态条件 & Part 序列完整性校验** | 数据语义/S3 合规 | **P1** — `CompleteMultipartUpload` 并发调用导致未定义行为；缺少 S3 规范要求的 Part 序列连续性和最小分片大小（5 MiB）校验，产生静默数据不一致 | ❌ **零覆盖**（v44 跨协议幂等性表格一行提及 multipart idempotent 关键字但未做实质性架构分析；v48 执行层缺口聚焦 orphan GC 和并发冲突模型但未覆盖 Complete 竞态和 Part 序列校验）|
| **4** | **对象读取路径数据完整性校验（On-Read Integrity Verification）** | 数据面可靠性 | **P2** — PUT 路径计算 MD5 并存储为 ETag，reconcile scrub 周期性验证存储层完整性，但每一次 GET 直接从存储流式读取，不比对计算出 ETag 与存储 ETag。静默比特衰减/存储损坏会直接交付给客户端而无法即时发现 | ❌ **零覆盖**（v12 智能平台方向表格一行路过提及 corruption_detected 指标但非独立分析；v8 架构缺口聚焦 scrub 而非 on-read 验证）|
| **5** | **跨协议执行上下文传播不一致（Auth/S3 Precondition/Idempotency）** | 运维/一致性 | **P2** — REST、S3、WebDAV、MCP 四套协议通过不同的机制处理认证、条件请求和幂等性，但跨协议同一用户操作无法获得一致的上下文。SigV4 认证的 S3 用户与 Bearer JWT 的 REST 用户在 tenant 解析和作用域授权方面走不同路径 | ❌ **零覆盖**（v19 覆盖了四协议输出的一致性语义合约但聚焦响应格式与错误码差异，未涉及协议输入侧的执行上下文传播；v43 预签名 URL 安全策略覆盖签名约束但仅针对预签名路径）|

---

## 方向一：本地存储引擎崩溃安全：数据/元数据双写窗口与不完整多分片恢复

### 现状

本地存储后端（`internal/storage/local_write.go`、`internal/storage/local_multipart.go`）是 CI gate 的默认基线，也是单机部署的主力后端。它的写入路径遵循以下模式：

**普通 PUT 写入顺序：**

```go
// internal/storage/local_write.go:26-30
meta, err := s.writeObject(ctx, path, key, r, size, opts)  // ① 数据写入
if err != nil { return ObjectInfo{}, err }
if err := writeMeta(s.metaPath(path), meta) {              // ② 元数据写入
    _ = os.Remove(path)                                     //    失败时回滚数据
    return ObjectInfo{}, err
}
```

`writeObject` 内部：
```go
// internal/storage/local_write.go:42-73
tmp, err := os.CreateTemp(...)     // 写入临时文件
_, _ = io.Copy(tmp, reader)        // 流式写入
_ = tmp.Sync()                     // fsync（未调用！）
_ = tmp.Close()
_ = os.Rename(tmpName, path)       // 原子重命名
```

`writeMeta` 内部（`internal/storage/local_meta.go`）：
```go
tmp, err := os.CreateTemp(...)     // 写入临时文件
_, _ = tmp.Write(b)                // 写入 JSON
_ = tmp.Close()
_ = os.Rename(tmpName, path)       // 原子重命名（但无 fsync）
```

**多分片合并写入顺序**（`internal/storage/local_multipart.go:72-106`）：

```go
s.mu.Lock()
delete(s.uploads, uploadID)           // ① 从 Uploads map 中删除
s.mu.Unlock()
defer os.RemoveAll(up.dir)             // ② defer 清理 parts 目录

s.mergeParts(ctx, up, parts, dst)     // ③ 合并到目标文件（temp→rename）
meta := localMeta{...}
writeMeta(s.metaPath(dst), meta)      // ④ 写入元数据
```

### 存在的 crash 窗口

```
时间线 →                                        crash 发生在此
PUT 路径:   [data temp write] → [rename] → [meta temp write] → [meta rename]
                                              ↑                          ↑
                                             窗口 A                     窗口 B

Complete MP:  [del upload] → [mergeParts(→temp→rename)] → [writeMeta(→tmp→rename)]
                               ↑                              ↑
                              窗口 C                         窗口 D
```

| 窗口 | 崩溃时刻 | 后果 | 严重程度 |
|------|---------|------|---------|
| **A** | data rename 后、meta 开始写入前 | 数据 blob 存在但无 `.meta.json`。下次 GET 找不到元数据返回 404，但存储空间被占用 | 🟠 数据可达性丢失 + 存储泄漏 |
| **B** | meta temp 写入后、meta rename 前 | meta temp 文件残留。重启后不会自动清理，`readMeta` 不会读取它 | 🟡 小文件泄漏 |
| **C** | upload map delete 后、mergeParts 开始前 | Upload 已被标记为完成，但目标文件尚未创建。客户端得到 404，parts 目录在 defer 中被清理 — 数据永久丢失 | 🔴 **数据丢失** |
| **D** | mergeParts 完成（数据 blob 已就绪）、writeMeta 完成前 | 与窗口 A 相同的 orphan 数据 blob | 🟠 数据可达性丢失 |

### 问题规模

对于默认后端（local FS），每次 PUT、多分片完成、SSE key 重包装等写入操作都可能遭遇上述窗口。虽然概率较低（crash 发生在 rename 和下一个 rename 之间），但一旦发生：

- **窗口 C 是最严重的**：CompleteMultipartUpload 是用户通过 SDK 调用、显式等待返回的关键路径。如果进程在此处崩溃，客户端 SDK 会收到连接断开/超时，从而重试。但重试时 upload ID 已从 map 和 repo 中删除，返回 "unknown upload" 404。**客户端无法区分"上传已完成但确认丢失"和"上传从未存在过"**，可能导致业务层数据丢失。
- **窗口 A/D 的孤儿 blob**：`reconcile.DeleteOrphanBlobs` 可以清理，但 orphan 检查基于 storage key 与 repository 行比对——不检查 `.meta.json` 的存在性。一个数据 blob 有对应的 repository 行但没有 meta 文件，会被 reconcile 跳过（因为 storage key 在 repo 中存在引用），但每次 GET 都会失败。

### 建议的架构视角

| 能力 | 实现策略 |
|------|---------|
| **fsync 保证** | 在 `os.Rename` 前对 temp 文件执行 `f.Sync()`，确保数据落盘后再重命名 |
| **启动时未完成合并恢复** | 服务器启动时扫描 `.multipart/` 目录：对于每个 stale upload，检查对应的 repository 行——如果 upload 已从 repo 删除但 parts 目录仍在，则清理 parts；如果 upload 仍在 repo 中，则尝试重新合并或标记为失败 |
| **元数据完整性边界** | 将 meta JSON 嵌入数据 blob 尾部（自描述格式），或使用 file header/footer 魔法字节标记。读取时如果 meta sidecar 不存在，尝试从 blob 尾部恢复 |
| **CompleteMultipartUpload 幂等验证** | 在 `CompleteMultipart` 入口处获取仓库锁，用 `Compare-And-Swap` 语义确保只有一个完成者。后续调用返回已完成的 object metadata 而非 404 |
| **新增 telemetry** | `local_storage.crash_window_exposed` gauge（窗口未受保护的文件数）、`local_storage.orphan_blob_recovered_total`、`local_storage.missing_meta_recovered_total` |

### 边界情况

- **SSE 加密 + 双写窗口**：如果加密的 blob 已写完成但 envelope 未写入 meta 文件，加密 blob 无解密的 DEK——数据永久不可恢复。窗口 A/D 在 SSE 启用时从"可达性丢失"升级为"不可逆数据丢失"
- **大文件多分片 + 窗口 C**：对于一个 10-part、5GB 的分片上传，所有 parts 已完成合并消耗了显著的 I/O 时间。窗口 C 的持久性最差——一旦丢失无法恢复，必须要求客户端重新上传
- **并发 CompleteMultipartUpload**：两个客户端同时完成同一个 upload（SDK 重试机制），当前实现会让第一个完成、第二个收到 404。但窗口 C 意味着如果第一个在合并中崩溃，第二个也无法完成——两端都失败了

---

## 方向二：版本化桶的写-删-写一致性 & Delete Marker 语义缺失

### 现状

当前版本化桶中的 DELETE 操作流程（`internal/service/file_crud.go:Delete`）：

```go
// 在版本化桶上调用 Delete
obj, _ := s.repo.GetObject(ctx, tenant, bucket, key)
s.repo.SoftDeleteObject(ctx, tenant, bucket, key) // 设置 deleted_at 时间戳
```

版本化桶中的 PUT 操作：

```go
// 在版本化桶上调用 Put
versionID := repository.NewVersionID()
sk = storageKey(tenant, bucket, key) + "@v" + versionID
// 写入 storage
s.repo.InsertObjectVersion(ctx, obj) // 插入新版本行
```

当前不存在 S3 Delete Marker 的概念。AWS S3 版本化语义要求：

| 操作 | AWS S3 行为 | AeroVault 当前行为 |
|------|------------|-------------------|
| DELETE on versioned bucket | 创建 Delete Marker（0 字节对象版本），隐藏当前版本 | 设置当前版本的 `deleted_at = now` |
| GET on deleted versioned object | 返回 404（Delete Marker 当前版本不可见） | 返回 404（`GetObject` 检查 `deleted_at`） |
| DELETE with version ID | 永久删除指定版本 | `HardDeleteObject`（通过 `force=true` 参数） |
| ListObjectVersions | 返回所有版本 + Delete Marker，`IsLatest` 标识最新 | 返回所有版本，时序最新（`updated_at DESC`）为 `IsLatest` |

**最成问题的是写-删-写序列（Write-After-Delete）：**

```
1. PUT key=A → 创建版本 v1
2. DELETE key=A → 软删除（deleted_at 设当前时间）
3. PUT key=A → 版本桶：创建版本 v2（deleted_at=null）

事件时序（events 是异步 FIFO 总线）:
  EventCreated(v1)  →  ≈                   → Antivirus Worker 开始扫描 v1
  EventDeleted(v1)  →  ≈                   → Replication Worker 复制删除
  EventCreated(v2)  →  ≈                   → Indexer 索引 v1 的内容

问题：如果 Indexer/Antivirus 先接收到 EventCreated(v1) 但被 EventDeleted(v1) 中断前还没完成处理，
然后又接收到 EventCreated(v2)——某些 worker 可能处于不一致状态（如 Indexer 还在索引 v1 时 v2 已覆盖）
```

### 具体问题

| 场景 | 当前影响 |
|------|---------|
| **事件消费者时序** | 背景工作者（Indexer、Antivirus、Replication）按事件流顺序处理。`EventCreated(v1)` → `EventDeleted(v1)` → `EventCreated(v2)`。如果 Indexer 在处理 v1 时还没完成就收到 v2，可能在 v1 完成索引后 v1 已被软删除，索引了本不应存在的快照 |
| **S3 客户端兼容性** | 标准 S3 SDK 生成的 ListObjectVersions 期望看到明确的 Delete Marker 条目。当前实现将所有版本（包括已删除的）作为 `<Version>` 返回，`IsLatest` 基于 `updated_at` 排序。AWS SDK 的某些操作（如 `DeleteObject` 返回 `x-amz-delete-marker: true`）存在行为差异 |
| **Quota 计量混乱** | PUT→DELETE→PUT 序列：第一次 PUT 增加配额，DELETE 不减少配额（软删除不回收 quota），第二次 PUT 再次增加配额。同一个对象占用了双倍配额计量 |
| **Reconcile 时空窗口** | `SoftDeleteObject` 在版本化桶上对当前版本设置 `deleted_at`。如果 reconcile 的 retention sweep 在 v1 处理后但在 v2 创建前执行，可能清理状态为已删除的 v1，即使 v2 即将覆盖它 |

### 建议架构方向

1. **Delete Marker 独立实体**：在版本化桶中，DELETE 创建一个特殊的 Delete Marker 版本行（`is_delete_marker = true`），不触发 storage blob 写入，不触发 `object.deleted` 事件。GET 自动跳过 Delete Marker。

2. **版本化桶的事件序列优化**：对于版本化桶的 PUT→DELETE→PUT 序列，事件的增量语义应为：
   - PUT v1 → `object.created` (version=v1)
   - DELETE → `object.deleted` (version=v1) — 仅当 v1 确实被"隐藏"
   - PUT v2 → `object.created` (version=v2)
   
   软删除不应触发 `object.deleted`，因为软删除的操作在 S3 语义中等同于创建一个 Delete Marker，并非删除数据。

3. **Quota 回收策略**：版本化桶中的 DELETE（隐藏当前版本）不应释放 quota；只有 HardDelete（永久删除指定版本）才回收。这需要将 quota 计算从简单的 `COUNT(*) WHERE deleted_at IS NULL` 改为考虑版本化桶的计费模型。

4. **版本感知的 Reconcile**：清理过期版本时需要区分"软删除的当前版本"（需要保留为历史版本）和"真正应该清除的过期版本"（生命周期策略）。

---

## 方向三：S3 多分片上传完成竞态条件 & Part 序列完整性校验

### 现状

当前的多分片上传流程（`internal/service/file_multipart.go` + `internal/storage/local_multipart.go`）：

```go
// CompleteMultipartUpload — service 层
func (s *FileService) CompleteMultipart(ctx context.Context, uploadID string) (Object, error) {
    u, err := s.repo.GetUpload(ctx, uploadID)     // ① 获取上传记录
    parts, err := s.repo.ListParts(ctx, uploadID)  // ② 列出已上传分片
    storageParts, _ := buildPartList(parts)         // ③ 构建分片列表
    // ... 配额检查、锁检查 ...
    info, err := s.store.CompleteMultipart(ctx, sk, u.BackendUID, storageParts) // ④ 存储层合并
    saved, err := s.saveMultipartObject(ctx, obj, bcfg)  // ⑤ 写入仓库
    s.repo.DeleteUpload(ctx, uploadID)            // ⑥ 删除上传记录
    return saved, nil
}
```

```go
// CompleteMultipart — local 存储层
func (s *LocalStorage) CompleteMultipart(ctx context.Context, key, uploadID string, parts []MultipartPart) (ObjectInfo, error) {
    s.mu.Lock()
    delete(s.uploads, uploadID)                   // ① 删除内存中 upload 记录
    s.mu.Unlock()
    defer os.RemoveAll(up.dir)                     // ② defer 清理 parts 目录
    
    sort.Slice(parts, ...)                         // ③ 按 part number 排序
    total, envelope, err := s.mergeParts(ctx, up, parts, dst) // ④ 合并
    // ... 写入 meta ...
}
```

### 竞态条件 1：并发 CompleteMultipartUpload

两个客户端同时调用 `CompleteMultipartUpload(uploadID=X)`：

```
客户端 A:  GetUpload → ListParts → buildPartList → Store.Complete → saveObject → DeleteUpload
客户端 B:  GetUpload → ListParts → buildPartList → Store.Complete →    ← 这里发生了什么？
```

**问题：** `store.CompleteMultipart`（local 后端）在第一步就 `delete(s.uploads, uploadID)`。客户端 A 拿到锁后执行合并。客户端 B 在 `GetUpload` 时仍能从 repo 获取到 upload 记录（repo 的删除在第 ⑥ 步），但在调用 `store.CompleteMultipart` 时会得到 "unknown upload" 错误——因为 local 的 map 已被 A 删除。客户端 B 最终收到 404/失败。

更严重的是，如果客户端 A 在 ④ 和 ⑤ 之间（mergeParts 完成但 saveObject 失败），merge 已完成（数据 blob 已写），但 repo 写入失败（如唯一键冲突）。数据 blob 已成为孤儿——无法通过 GET 访问（无 repo 行），也无法通过重新上传修复（key 可能已被占用）。

### 竞态条件 2：并发 UploadPart + CompleteMultipart

AWS S3 规范要求 `CompleteMultipartUpload` 之后不得再上传 Part。但当前实现没有这部分防护：

```
UploadPart(uploadID=X, partNumber=5)           // 正在写入
CompleteMultipartUpload(uploadID=X)             // 开始合并——part-5 可能不完整
→ mergeParts 读取 part-005 文件（正在被写入）
→ 合并了不完整的 part-005 内容
```

虽然 local 存储层在 `CompleteMultipart` 入口处删除了 upload map entry，但正在进行的 `UploadPart` 可能在删除前已经获取了 `up.dir` 引用，仍能写入 part 文件。时间窗口极小但存在。

### 缺失的校验：Part 序列完整性

AWS S3 规范要求：
1. Part Numbers 必须从 1 开始连续（1, 2, 3, …, N）
2. 除最后一个 Part 外，每个 Part 必须 ≥ 5 MiB（对于非本地后端）
3. 不允许重复的 Part Number
4. Part 总数 ≤ 10,000

当前实现：

| 校验 | 状态 | 后果 |
|------|------|------|
| Part 连续性（1,2,3,...,N 无跳号） | ❌ 无校验 | 跳号的完成请求合并出不完整的对象 |
| Part 最小大小（5 MiB） | ❌ 无校验 | 违反 S3 规范，SDK 客户端可能拒绝 |
| Part Number 重复 | ⚠️ 隐式覆盖 | `RecordPart` 是 INSERT，重复的 partNumber 会报错 |
| Part 总数上限 10,000 | ❌ 无校验 | 内存耗尽风险 |

### 建议架构修复

| 问题 | 修复策略 |
|------|---------|
| 并发 Complete 竞态 | 在 repo 层使用 `SELECT ... FOR UPDATE`（Postgres）或重试乐观锁（SQLite）序列化 `CompleteMultipart`。只有第一个完成者能成功合并；后续调用返回已完成对象的 metadata（幂等） |
| Part 序列校验 | `CompleteMultipart` 前执行：`sort.Slice(parts)` → 检查 `parts[i].PartNumber == i+1` → 检查最小大小（可选，基于后端） |
| Part 文件完整性 | 合并前对每个 part 文件进行 size 检查（对比 `PartRecord.Size` 与文件实际大小） |
| UploadPart + Complete 并发防护 | 在 `CompleteMultipart` 入口设置 upload 状态为 `completing`，`UploadPart` 检查此状态并拒绝 |

---

## 方向四：对象读取路径数据完整性校验（On-Read Integrity Verification）

### 现状

当前系统的数据完整性策略：

| 检查点 | 机制 | 存在与否 |
|--------|------|---------|
| **PUT 时** | 计算 MD5，存储为 ETag；如果客户端提供 `Content-MD5`，进行匹配校验 | ✅ `internal/service/file_crud.go:md5WrapReader` |
| **存储层** | reconcile scrub 周期性验证 storage blob 的 MD5 与存储的 ETag 是否一致 | ✅ `internal/reconcile/scrub.go` |
| **GET 时** | 从 storage 流式读取数据，不计算/比对其 MD5 | ❌ **缺失** |
| **GET Range 时** | 部分内容，不校验整体 MD5 | ❌ **缺失** |
| **多分片合并对象 GET 时** | ETag 是多分片复合格式（`hex-N`），无法用简单 MD5 校验 | ❌ **缺失** |

**这意味着：** 如果存储后端（local FS、S3、OSS、COS）发生静默比特衰减、或传输过程中数据损坏，**所有后续 GET 请求都会交付损坏的数据**，且系统完全不知情。reconcile scrub 周期性扫描发现损坏之前（可能在多分钟后），用户已经读取了损坏的数据。

```go
// internal/service/file_crud.go:Get — 无任何校验
func (s *FileService) Get(ctx context.Context, tenant, bucket, key string) (io.ReadCloser, Object, error) {
    obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
    rc, _, err := s.store.Get(ctx, obj.StorageKey)  // 直接流式读取
    // 没有计算 checksum 对比 obj.ETag
    return rc, obj, nil
}
```

### 为什么这是比 scrub 更紧迫的问题

| 对比维度 | Reconcile Scrub（每 N 分钟） | On-Read Verification（每次 GET） |
|----------|------------------------------|----------------------------------|
| 检测延迟 | 平均 N/2 分钟（批量窗口） | 即时（第一次读取即检测） |
| 覆盖范围 | 周期性扫描全部对象（I/O 密集） | 只覆盖被访问的对象（热点优先） |
| 用户影响 | 管理员通过日志/告警获知 | 用户获得明确的错误响应而非损坏数据 |
| 实现复杂度 | 全量扫描（大量 I/O） | 仅附加 MD5 计算（流式，几乎无额外 I/O） |

### 实现策略

#### 普通对象的 On-Read 校验

对于非多分片对象，ETag = 内容 MD5 hex。在校验路径中：

```go
// 在 service 层的 Get 方法中增加可选校验
type GetOptions struct {
    VerifyIntegrity bool // 默认为 true（可为每请求关闭以减少开销）
}

func (s *FileService) GetWithOpts(ctx context.Context, tenant, bucket, key string, opts GetOptions) (IntegrityReadCloser, Object, error) {
    obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
    // ...
    rc, _, err := s.store.Get(ctx, obj.StorageKey)
    
    if opts.VerifyIntegrity && isSimpleETag(obj.ETag) {
        // 包装 reader，流式计算 MD5
        h := md5.New()
        verifiedRC := &integrityReader{
            reader:    io.TeeReader(rc, h),
            closer:    rc,
            onClose: func() error {
                got := hex.EncodeToString(h.Sum(nil))
                if got != obj.ETag {
                    telemetry.IncCorruptionDetected(ctx, obj.StorageKey)
                    return ErrObjectCorrupt // 或记录 + 继续（策略配置）
                }
                return nil
            },
        }
        return verifiedRC, obj, nil
    }
    // ... 返回普通 reader
}
```

#### 多分片合并对象的校验

多分片 ETag 格式为 `md5(part1_md5 + part2_md5 + ...)-N`，不能直接用流式 MD5 验证。策略：

1. **标记多分片对象**：在 metadata 中存储 `_aero_multipart_parts`（part count），或通过 ETag 格式检测
2. **惰性验证**：对于多分片对象，在读取完成后通过 `CompleteMultipartUpload` 时存储的原始 part 校验和进行验证（需要存储 part MD5 列表）
3. **降级策略**：对于无法验证的多分片对象，仅记录告警不中断读取（管理员可配置行为）

#### 配置与性能

| 配置项 | 默认值 | 说明 |
|--------|--------|------|
| `VERIFY_INTEGRITY_ON_READ` | `true` | 全局开关 |
| `VERIFY_INTEGRITY_ON_RANGE` | `false` | Range 请求跳过（range 是部分内容，无法验证全量 MD5）|
| `CORRUPTION_ACTION` | `"warn"` | `"warn"`=仅记录告警、`"reject"`=返回 409 CorruptionDetected、`"failopen"`=不检查 |

#### 遥测

- `object.corruption_detected_on_read` counter（按 tenant、bucket、storage backend 维度）
- `object.integrity_verified_total` counter
- `object.integrity_verify_duration_seconds` histogram

---

## 方向五：跨协议执行上下文传播不一致

### 现状

aero-vault 支持四套协议，每套协议的认证、授权和上下文传递路径不同：

| 协议 | 认证方式 | Tenant 来源 | 请求 ID 来源 | 幂等性支持 | 条件请求 |
|------|---------|------------|------------|-----------|---------|
| **REST /v1** | Bearer JWT / X-Api-Key | `X-Aero-Tenant` header 或 JWT 声明 | `X-Request-ID` header | ✅ Idempotency-Key | ✅ If-Match / If-None-Match |
| **S3 /s3** | SigV4（headers/chunked）| 从 SigV4 凭证映射（`accessKey:secretKey:tenant`）| 无 | ❌ 无 | ⚠️ 仅 GetObject（If-Match / If-None-Match / If-Modified-Since / If-Unmodified-Since），其他操作（CopyObject、PUT、DELETE）不支持 |
| **WebDAV** | 同上 REST middleware | `X-Aero-Tenant` header | 无 | ❌ 无 | ❌ 无 |
| **MCP HTTP** | 同上 REST middleware | `X-Aero-Tenant` header | 无 | ❌ 无 | ❌ 无 |
| **MCP stdio** | 无（默认 tenant） | 固定 `"default"` | 无 | ❌ 无 | ❌ 无 |

### 核心不一致体现

#### 1. Tenant 解析不一致

```go
// REST/S3/WebDAV/MCP HTTP：从 middleware.Tenant 获取
tenant := mw.TenantFrom(r.Context()) // X-Aero-Tenant header 或 JWT 声明

// MCP stdio：从 Server 的 tenant 字段获取
func (s *Server) tenantFor(ctx context.Context) string {
    if t := mw.TenantFrom(ctx); t != "" && t != "default" {
        return t
    }
    return s.tenant // 固定 "default"
}

// S3 SigV4：从凭证映射解析
// internal/auth/sigv4.go — 凭证格式为 "accessKey:secretKey:tenant[:scope+scope]"
// 如果凭证映射中没有 tenant，fallback 到 X-Aero-Tenant header（可能不存在）
```

**问题：** 通过 S3 SigV4 认证的请求和通过 REST JWT 认证的请求，即使来自同一个用户，可能被解析为不同的 tenant。这破坏了多租户隔离的基本假设。

#### 2. 条件请求支持不完整

AWS S3 SDK 客户端在批量操作（复制、删除、生命周期转换）中广泛使用条件请求来实现乐观并发控制。当前 S3 实现的条件请求支持：

| S3 操作 | If-Match | If-None-Match | If-Modified-Since | If-Unmodified-Since |
|---------|----------|---------------|-------------------|---------------------|
| GetObject | ✅ | ✅ | ✅ | ✅ |
| HeadObject | ✅ | ✅ | ✅ | ✅ |
| PutObject | ❌ | ❌ | ❌ | ❌ |
| DeleteObject | ❌ | ❌ | ❌ | ❌ |
| CopyObject | ❌ | ❌ | ❌ | ❌ |
| UploadPartCopy | ❌ | ❌ | ❌ | ❌ |
| CompleteMultipartUpload | ❌ | ❌ | ❌ | ❌ |

**影响：** S3 SDK 的标准批量操作工具使用条件请求来确保"写入时未被他人修改"。不实现条件请求意味着并发写入场景中存在 Last-Writer-Wins 语义，可能覆盖其他客户端的数据。

#### 3. Idempotency-Key 仅限于 REST

幂等键（Idempotency-Key header）仅在 REST `/v1` 路由上有实现（`internal/api/rest/idempotency.go`）。S3 和 WebDAV 路径完全没有幂等保护。当客户端通过 S3 SDK 重试 PUT 请求时，如果服务器已成功写入但确认响应丢失，客户端会重新发送请求：

- **S3 非版本化桶**：`UpsertObject` 是覆盖式写入，重新 PUT 没有副作用（幂等）
- **S3 版本化桶**：`InsertObjectVersion` 每次创建新版本——重试导致重复版本
- **WebDAV**：PUT 同一个文件→ 无幂等保护，可能产生重复

### 建议架构方向

#### 统一的执行上下文（Execution Context）传播

```go
// 跨协议的共享上下文结构
type ExecutionContext struct {
    Tenant       string
    UserID       string       // 认证主体（API Key ID / JWT sub / SigV4 access key）
    AuthMethod   string       // "jwt" / "apikey" / "sigv4" / "anonymous"
    RequestID    string
    Scopes       []string
    Protocol     string       // "rest" / "s3" / "webdav" / "mcp"
    ClientIP     string
    IdempotencyKey string     // 归一化幂等键
    ConditionalHeaders map[string]string // 归一化条件请求头
}
```

- 协议适配层在入口处解析协议特定的凭证，转换为统一的 `ExecutionContext`
- FileService 方法统一接收 `ExecutionContext` 而非从 context 中提取参数
- S3 的 SigV4 凭证映射应解析 tenant 并注入到 ExecutionContext，而非依赖 `X-Aero-Tenant` header 的隐式传递

#### S3 条件请求的完整实现

| 操作 | 实现路径 |
|------|---------|
| PutObject + If-Match/If-None-Match | 写入前 `Stat` 对象，检查 ETag/version 匹配 |
| DeleteObject + If-Match/If-None-Match | 删除前 `Stat` 对象，类似 PUT 的 checkLockBeforeOverwrite |
| CopyObject + 条件请求 | 读取 source 时应用条件，检查 source 是否符合条件再执行复制 |
| UploadPartCopy | 在复制 part 的 source 侧应用条件请求 |

#### 幂等性扩展至 S3/WebDAV

| 协议 | 幂等键来源 | 策略 |
|------|-----------|------|
| S3 PUT | `x-amz-idempotency-token`（或 Content-MD5 + Date 派生） | 对版本化桶的 PUT，使用 (tenant, bucket, key) + 请求体 MD5 派生幂等键 |
| WebDAV PUT | 硬编码为文件路径 + If header（如果存在） | WebDAV 本质上是版本替换，幂等性天然满足（覆盖式写入） |
| S3 CompleteMultipartUpload | upload ID 本身是天然的幂等键 | 但需要去重保护（方向三的竞态条件修复已覆盖） |

---

## 综合优先级建议

| 优先级 | 方向 | 依赖 | 建议时序 |
|--------|------|------|---------|
| **P0** | 方向一：本地存储引擎崩溃安全（fsync + 启动恢复 + CompleteMP 幂等） | 无 | **Phase 1** — 默认后端的可靠性根基，影响所有本地/单机部署实例 |
| **P0** | 方向三：多分片完成竞态 + Part 序列校验 | 无 | **Phase 1** — S3 兼容性的基本要求，影响 SDK 客户端正确性 |
| **P1** | 方向四：对象读取路径完整性校验 | 方向一（on-read 校验日志需要区分 storage vs service 层错误） | **Phase 2** — 数据面护栏，一旦出现存储损坏事件的价值极大 |
| **P1** | 方向二：版本化桶 Delete Marker 语义 | 无 | **Phase 2** — S3 语义完整性的关键差异点，影响版本化桶用户 |
| **P2** | 方向五：跨协议执行上下文 | 方向二（S3 条件请求 + 幂等扩展依赖版本化语义） | **Phase 3** — 长期架构投资，影响所有协议的整体行为一致性 |

### 实施建议

1. **方向一 + 方向三** 对 CI gate 路径（local + SQLite）有直接影响，无需新依赖，应优先修复
2. **方向四** 需要新增配置项但实现量小（~150 行），可快速落地
3. **方向二** 涉及 DB schema 变更（新增 `is_delete_marker` 列或等价标记），与方向三的幂等性修复有重叠
4. **方向五** 是架构级别的重构——建议先产出 `Execution Context` 的 ADR，逐步迁移而非一次性重写

---

## 结论

以上 5 个方向共同指向一个核心判断：AeroVault 的功能面（"提供了什么"）经过 50 轮分析已被充分覆盖，但其数据面正确性（"做的对不对"）和执行一致性（"跨协议做的是否一致"）仍然存在系统性的缺口。这些缺口不体现为"缺少某个端点或功能"，而体现为**已有功能在极端条件下的行为不可预测**。在走向生产部署之前，修复这些执行路径上的语义偏差和可靠性缺陷，优先级应高于增加任何新的功能。
