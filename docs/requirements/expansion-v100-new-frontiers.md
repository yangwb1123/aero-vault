# 架构级扩展方向：写入路径原子性、I/O 缓冲区架构、多租户后台隔离、读取路径可靠性、API 治理层

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — 237 个 Go 源文件，`cmd/server/main.go` 完整装配链路，`internal/` 全部 30+ 子包，3 套 SDK（Go/Python/JS），MCP 双模式，Web UI，完整迁移文件对，`deploy/` 全套配置。  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。选取 **代码中存在明确实现锚点但在过往 99 轮扩展分析中未被深度覆盖** 的方向。每个方向包含：代码证据 → 产品价值/影响 → 架构方案权衡 → 边界情况。  
> **前置阅读：** `docs/ROADMAP.md`（10 大官方方向）、`docs/requirements/expansion-v99-dead-code-paths-and-governance-gaps.md`（断线管线分析）

---

## 方法论：从运行时行为到架构缺口

本库已有 99 轮扩展方向分析，累计覆盖 150+ 方向。绝大多数分析关注"缺失的功能点"——即某个 S3 端点未实现、某个 AI 组件未接入、某个配置未生效。

本期切换视角：**不关注"缺什么功能"，而是关注"已有功能在运行时是否可靠"**。三个判定标准：

| 判定标准 | 含义 | 本期方向 |
|----------|------|---------|
| **崩溃安全性** | 服务器在写路径中途崩溃，数据是否一致？ | 方向一：写入路径原子性 |
| **资源隔离** | 一个租户的突发流量是否影响其他租户？ | 方向三：多租户后台隔离 |
| **故障容错** | 后端存储短暂不可用，系统是否优雅降级？ | 方向四：读取路径可靠性 |
| **性能架构** | 大对象操作是否存在可避免的内存/CPU浪费？ | 方向二：I/O 缓冲区架构 |
| **治理完备性** | API 是否有统一的验证、审计、限流框架？ | 方向五：API 治理层 |

---

## 方向总览

| # | 方向 | 性质 | 优先级 | 核心发现 |
|---|------|------|--------|---------|
| **1** | **写入路径原子性与崩溃恢复** — 从"尽力而为"到"崩溃安全" | 数据正确性 / 可靠性 | **P0** | PUT 先写 storage 后写 repo：storage 成功但 repo 失败 → 对象孤立（仅 warn log，无补偿）。无写前日志、无两阶段提交、无重做/回滚机制 |
| **2** | **I/O 缓冲区架构与零拷贝管道** — 从"每次分配"到"池化复用" | 性能 / 资源效率 | **P1** | `local.Get` 全量读入内存 → 解密 → 返回；`local.Put` 全量接收 → 加密 → 写入；S3 COPY 全量 Get → 全量 Put；无 `sync.Pool`、无 `io.Copy` 缓冲区尺寸控制、无 sendfile 零拷贝 |
| **3** | **多租户后台工作隔离与公平调度** — 从"全局共享"到"租户感知" | QoS / 运维 | **P1** | 索引器、复制 worker、Antivirus、Lifecycle、Reconcile 全部以全局 FIFO 消费事件，无租户级优先级/限流/隔离；单租户大量创建事件可阻塞其他租户的后台处理 |
| **4** | **读取路径可靠性与缓存层次** — 从"直通存储"到"韧性读取" | 可靠性 / 延迟 | **P2** | Storage `Get`/`Stat` 错误直接冒泡为 5xx；无自动重试、无熔断降级、无读取缓存（搜索缓存存在但对象读取无缓存）；多副本环境无读取亲和性 |
| **5** | **API 治理层：请求验证、版本管理、操作审计** — 从"松散耦合"到"强制执行" | 企业治理 / 安全 | **P2** | 无统一请求 schema 验证（每个 handler 自行解析校验）；无 API 版本协商；无操作级审计（仅有 admin 操作审计，CRUD 操作无审计）；无全局请求变换管线 |

---

## 方向一：写入路径原子性与崩溃恢复

### 现状与代码证据

当前 `Put` 操作的执行顺序是 **storage 先写入，repo 后写入**：

```go
// internal/service/file_crud.go:58-73
func (s *FileService) Put(ctx context.Context, tenant, bucket, key string, r io.Reader, size int64, opts PutOptions) (repository.Object, error) {
    // ... 参数校验、quota 检查、锁定检查 ...
    
    // 1️⃣ 先写 storage
    info, err := s.store.Put(ctx, sk, reader, size, storage.PutOptions{...})
    if err != nil {
        return repository.Object{}, fmt.Errorf("storage put: %w", err)
    }
    if err := verifyMD5(); err != nil {
        s.store.Delete(ctx, sk)   // ⚠️ MD5 失败时尝试回滚 storage
        return repository.Object{}, err
    }
    
    // 2️⃣ 再写 repository
    saved, err := s.repo.UpsertObject(ctx, obj)  // 或 InsertObjectVersion
    if err != nil {
        // ⚠️ 仅日志警告，不回滚 storage
        s.logger.Error("repo write failed; storage object orphaned", ...)
        return repository.Object{}, fmt.Errorf("repo write: %w", err)
    }
    // ...
}
```

**三个崩溃安全缺口：**

| 缺口 | 场景 | 后果 |
|------|------|------|
| **Gap A** | `s.store.Put` 成功 → 服务器崩溃 → `s.repo.UpsertObject` 未执行 | 存储 blob 孤立：占用空间且永不清理（直到 reconcile 扫描到它） |
| **Gap B** | `s.repo.UpsertObject` 成功 → `s.repo.AddTenantUsage` 失败（仅 warn log） | 配额计数与实际使用不一致 |
| **Gap C** | MD5 验证失败 → `s.store.Delete` 调用但 Delete 自身可能失败 | Blob 残留已损坏数据 |

**同样的模式存在于 `file_multipart.go` 和 `file_features.go` 的多个写入路径中：**

```go
// internal/service/file_multipart.go — CompleteMultipart
func (s *FileService) CompleteMultipart(ctx context.Context, uploadID string) (repository.Object, error) {
    // ... 验证分片 ...
    
    // 先合并 storage 中的分片
    info, err := s.store.CompleteMultipart(ctx, ...) 
    if err != nil { ... }
    
    // 再写入 metadata
    obj, err := s.repo.InsertObject(ctx, ...)
    if err != nil {
        // ⚠️ 同样只日志不补偿
        s.logger.Error("repo insert after multipart complete failed", ...)
        return repository.Object{}, ...
    }
}
```

**删除路径同样存在原子性问题：**

```go
// internal/service/file_crud.go — Delete + hardDeleteObject
func (s *FileService) hardDeleteObject(...) {
    // 先清理 storage
    if err := s.store.Delete(ctx, obj.StorageKey); err != nil { ... }
    // ⚠️ 如果在此处崩溃，storage blob 已删除但 repo 行仍在
    // 再删除 repo 行
    if err := s.repo.HardDeleteObject(ctx, ...); err != nil { ... }
    // ⚠️ 如果在此处崩溃，已经反向：blob 已删但行还在
}
```

**当前唯一的补偿机制是 reconcile 扫描**（`internal/reconcile/job.go`），但它是定期（分钟级）且 opt-in 的：

```go
// reconcile/job.go — sweepOrphanRows
// 发现 storage 中不存在的 blob 的 DB 行 → soft-delete 该行
// 发现 DB 中不存在的 blob → 可选择删除（RECONCILE_DELETE_ORPHAN_BLOBS=true）
```

### 产品价值

| 维度 | 影响 |
|------|------|
| **数据完整性** | 写入过程中的服务器崩溃不应导致数据丢失或孤立的 blob。这是对象存储的最基本 SLA |
| **配额准确性** | 配额计数漂移导致租户被错误拒绝（计数过高）或账单错误（计数过低） |
| **运维成本** | 依赖 reconcile 清理孤儿是不可靠的：间隔期间存储浪费、reconcile 本身可能被禁用 |
| **恢复时间** | 崩溃后无需等待 reconcile 周期即可保证一致性 |

### 架构方案权衡

**方案 A：Write-Ahead Log (WAL) — 推荐**

引入一个 `writeJournal` 表，在 storage 写入前预写日志记录。崩溃后启动时回放：

```
1. BEGIN TRANSACTION (repo)
2. INSERT INTO write_journal (op, tenant, bucket, key, storage_key, status='pending')
3. s.store.Put(...)           // 写入 storage
4. UPDATE write_journal SET status='storage_done'
5. s.repo.UpsertObject(...)   // 写入 metadata
6. DELETE FROM write_journal WHERE ...  // 清理日志
7. COMMIT
```

崩溃恢复：
- 启动时扫描 `write_journal` 中 `status='pending'` 的行 → blob 可能不存在 → 直接删除 journal 行（安全）
- `status='storage_done'` 的行 → blob 存在但行可能丢失 → 重新尝试 repo 写入或删除 blob + journal 行

**优点：** 通用方案，覆盖所有写入路径（PUT、Multipart、Tag 更新等）  
**缺点：** 每次写入增加一次 DB 写；journal 表需要 GC（复用 idempotency GC 机制）

**方案 B：幂等写入 + 延迟清理**

为每次写入生成唯一 ID，blob 携带此 ID；崩溃后重复写入时检测到已有 blob 则跳过。reconcile 负责清理真正孤立的 blob。

**优点：** 无需额外 DB 写入  
**缺点：** 对已存在的 blob 覆盖场景复杂；需要存储层支持元数据标记

**方案 C：写入顺序反转（repo 先写）**

先写入 metadata（标记为 `pending` 状态），再写入 blob，最后标记为 `active`。读取时过滤 `pending` 行。

**优点：** 崩溃后扫描 `pending` 行即可发现未完成的写入  
**缺点：** 现有查询都需要加 `status='active'` 过滤，影响所有读取路径性能；storage 写入成功后标记更新本身又是一个可能的崩溃点

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| storage 写入成功但 journal 写入失败 | 当前行为（不动）：storage blob 存在但无记录 → reconcile 可清理 |
| storage 写入超时但实际成功 | 客户端重试导致重复 blob（versioning 开启时产生新版本，关闭时覆盖） |
| 多副本 WAL 写入 | journal 表本身是 DB 写入，DB 写入也可能失败——需确保 journal 写入使用与 metadata 写入相同的 DB 事务 |
| 大量 orphan journal 行 | 需要 GC 机制清理长时间停留在 `pending` 的行（假设写入进程已崩溃） |

---

## 方向二：I/O 缓冲区架构与零拷贝管道

### 现状与代码证据

**当前，几乎所有 I/O 路径都是"全量读入内存 → 处理 → 全量写出"：**

```go
// internal/storage/local_read.go — Get 方法
func (s *LocalStorage) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
    // ... 
    data, err := os.ReadFile(path)       // ⚠️ 全量读入内存
    if err != nil { ... }
    if s.encrypter != nil {
        data, err = s.encrypter.Decrypt(data)  // ⚠️ 解密产生另一份副本
        if err != nil { ... }
    }
    return io.NopCloser(bytes.NewReader(data)), info, nil  // ⚠️ 第三份内存副本
}
```

```go
// internal/storage/local_write.go — Put 方法  
func (s *LocalStorage) Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, error) {
    data, err := io.ReadAll(r)            // ⚠️ 全量读入内存
    if err != nil { ... }
    if s.encrypter != nil {
        data, err = s.encrypter.Encrypt(data)  // ⚠️ 加密产生第二份副本
        if err != nil { ... }
    }
    if err := os.WriteFile(path, data, 0644); err != nil { ... }  // ⚠️ 第三份
}
```

**S3 COPY 操作也是全量读取后写入：**

```go
// internal/api/s3compat/extra.go:39 — copyObject
func (h *Handler) copyObject(w http.ResponseWriter, r *http.Request, dstBucket, dstKey, copySource string) {
    // ...
    rc, src, err := h.svc.Get(r.Context(), tenant, srcBucket, srcKey)  // 读取源对象
    // ...
    dst, err := h.svc.Put(r.Context(), tenant, dstBucket, dstKey, rc, src.Size, opts)  // 写入目标
    // rc 流经 Get → Put，但 local 后端在 Get 中已全量读入内存
}
```

**新增的 gzip 自动解压缩同样产生额外副本：**

```go
// internal/service/file_crud.go:252 — Get 中的 gzip 解压缩
if obj.Metadata["_aero_content_encoding"] == "gzip" {
    gr, err := gzip.NewReader(rc)   // 包裹在流式 reader 上——✅ 这段是流式的
    // 但是底层 rc 来自 local.Get，其中 os.ReadFile 已全量读入
}
```

**加密路径是最大的内存瓶颈：** 对于 1GB 对象，local 后端需要：
1. 从文件读到 1GB `[]byte`（内核页缓存 + 用户空间 1GB）
2. 解密到另一个 1GB `[]byte`（用户空间 2GB）
3. 再从 `bytes.NewReader` 传给调用方（仍保留 2GB）

对于 10GB 对象，系统将在 OOM 前耗尽内存。

**当前没有任何 `sync.Pool` 或缓冲区池化：**

```bash
$ grep -rn "sync.Pool" internal/ --include='*.go'
# 零结果
```

**io.Copy 使用默认的 32KB 缓冲区：** 这在高速网络（10GbE+）或 NVMe 本地存储上远非最优。

### 产品价值

| 维度 | 影响 |
|------|------|
| **大对象支持** | 当前架构下 >2GB 对象即使在本地存储也接近 OOM 风险。真正的"任何大小"文件存储需要流式 I/O |
| **吞吐量** | 缓冲区池化可减少 GC 压力 50%+，减少内存分配 90%+ |
| **并发能力** | 内存占用是最大并发瓶颈——每 1GB 对象需要 2GB RAM，限制了并发请求数 |
| **成本** | 更高的内存需求意味着更大的实例规格或更多的 OOM 重启 |

### 架构方案权衡

**核心改进：Storage 层 `Get`/`Put` 全流式化**

```
当前: os.ReadFile → []byte → Decrypt → []byte → bytes.NewReader
目标: os.File → bufio.Reader → [流式解密] → io.Reader (零额外分配)
```

| 改进点 | 现状 | 目标 |
|--------|------|------|
| 加密/解密 | 全量输入 → 全量输出（`[]byte`） | 流式加密（`io.Reader` → `io.Reader`） |
| 缓冲区分配 | 每次 `make([]byte, size)` | `sync.Pool` 复用 64KB/256KB/1MB 块 |
| `io.Copy` 缓冲区 | 默认 32KB（`io.copyBuf`） | 根据后端延迟特性动态选择（local=1MB, S3=256KB） |
| 零拷贝支持 | 无 | `io.ReaderFrom`/`io.WriterTo` 检测 + `sendfile`(Linux) / `splice` |
| 对象过大保护 | 无 | 超过 512MB 的对象强制使用临时文件而非内存 |

**加密流式化的关键：** `encrypt.go` 当前使用 `envelopeEncrypter.Encrypt(data []byte)`。需要改为 `EncryptStream(r io.Reader) io.ReadCloser`，在读取时对每个 AES-CTR 块（或每个 chunk）加解密。`crypto/cipher.StreamReader`/`StreamWriter` 天然支持这种模式——当前实现绕过了这些接口。

**`s3.Get` 的流式化：** AWS SDK 的 `s3.GetObject` 返回的 Body 已经是 `io.ReadCloser`，但当前 `local.Get` 和 `s3.Get` 的行为不同——local 全量缓冲，s3 流式返回。统一为流式后，`vault.Get` 的一致性将大幅提升。

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| 流式加密中的 seek 操作 | Range 请求需要从偏移量开始解密；AES-CTR 天然支持随机访问（从指定 block 开始 XOR） |
| 部分读取后关闭 | Get 返回的 `io.ReadCloser` 调用方未读完就 Close——流式加密器需正确处理中途关闭 |
| 加密状态泄露 | 流式加密器的内部状态（IV/计数器）不能通过 panic/recover 暴露到日志 |
| sendfile 不支持加密 | 零拷贝路径无法与加密共存；检测到加密时降级为常规流式读取 |
| 临时文件溢出 | 超大对象倒灌到磁盘时需监控磁盘空间并给出清晰错误 |

---

## 方向三：多租户后台工作隔离与公平调度

### 现状与代码证据

**所有后台 worker 都以全局 FIFO 消费事件，无租户隔离：**

```go
// cmd/server/main.go — 所有 worker 共享同一个 event subscription
go avw.Run(ctx, bus.Subscribe())       // Antivirus — 全租户事件
go rw.Run(ctx, bus.Subscribe())        // Replication — 全租户事件
go wh.Run(ctx, bus.Subscribe())        // Webhook — 全租户事件
go indexer.Run(ctx, bus.Subscribe())   // Indexer — 全租户事件
```

```go
// internal/events/bus.go:168 — broadcast 是全局的
func (b *Bus) broadcast(e repository.Event) {
    b.mu.RLock()
    defer b.mu.RUnlock()
    for _, ch := range b.subs {
        select {
        case ch <- e:          // 所有 subscriber 收到所有事件
        default:
            b.dropped.Add(1)   // 或丢弃（无租户过滤）
        }
    }
}
```

**作业队列同样无租户感知：**

```go
// internal/jobs/jobs.go — Pool.runOne
func (p *Pool) runOne(ctx context.Context, worker string) (worked bool, err error) {
    job, ok, err := p.repo.ClaimJob(ctx, worker)  // ClaimJob FROM jobs ORDER BY created_at
    // ↑ 全局 FIFO：一个租户的积压 job 会阻塞所有其他租户的 job
    // ...
}
```

```sql
-- internal/repository/sql_jobs.go — ClaimJob 查询
-- (预期)：ORDER BY created_at ASC → 先入先出，无租户优先级
```

**索引器事件桥接也是全局串行的：**

```go
// internal/ai/indexer.go — Run
func (idx *Indexer) Run(ctx context.Context, sub <-chan repository.Event) {
    // 从 channel 逐个取出事件，逐一处理或入队
    // 如果某事件处理慢（大文件提取），阻塞后续所有事件
}
```

**影响场景：**

| 场景 | 后果 |
|------|------|
| 租户 A 批量上传 10,000 个文件 | 索引器忙于处理租户 A 的事件 → 租户 B 的文件等待索引 → 搜索不可用 |
| 租户 A 上传一个染毒文件 | Antivirus 扫描期间 → 其他所有待扫描事件排队 |
| 租户 A 的事件涌入导致 subscriber channel 满 | 所有租户的事件被丢弃（`bus.dropped` 递增） |
| 租户 A 的 job 持续失败重试 | 重试 backoff 虽有效但 job 行仍然在队列中，`CountJobsByStatus` 计数增高可能触发深度限制 |

### 产品价值

| 维度 | 影响 |
|------|------|
| **SLA 保障** | SaaS 场景下一个"吵闹"租户不应影响其他租户的后台处理延迟（索引、复制、扫描） |
| **可预测性** | 后台处理延迟是服务质量的重要组成部分。随机波动破坏用户信任 |
| **资源利用** | 租户隔离允许对高价值租户分配更多后台资源 |
| **运维诊断** | 无租户标签的指标无法回答"哪个租户的索引延迟高" |

### 架构方案权衡

**核心改进：为所有后台工作引入租户感知调度**

**方案 A：租户隔离事件通道 — 推荐立即实施**

```go
// 为每个租户创建独立 subscriber channel
type TenantBus struct {
    *Bus
    tenantChans map[string]chan repository.Event  // per-tenant channel
}

func (b *TenantBus) Subscribe(tenant string) <-chan repository.Event {
    // 返回特定租户的 channel
}

func (b *TenantBus) broadcast(e repository.Event) {
    // 路由事件到对应租户的 channel
    if ch, ok := b.tenantChans[e.TenantID]; ok {
        select {
        case ch <- e:
        default:
            // 仅丢弃该租户的事件
        }
    }
    // 同时保留全局 channel 用于管理/监控
}
```

**优点：** 改动局限在 `events.Bus` 包，worker 无需感知租户  
**缺点：** 租户数量多时 channel 管理开销；需要活跃租户发现机制

**方案 B：租户感知 Job 调度**

```go
// 扩展 job pool 支持 per-tenant 优先级
func (p *Pool) runOne(ctx context.Context, worker string) (bool, error) {
    // 轮询各租户的待处理 job，使用 weighted fair queuing (WFQ)
    // 高价值租户获得更高权重
    job, ok, err := p.repo.ClaimJobByTenant(ctx, worker, tenantWeights)
}
```

SQL 层面需要新增索引支持按 `tenant_id` ORDER BY：

```sql
CREATE INDEX idx_jobs_tenant_status ON jobs (tenant_id, status, created_at);
```

**优点：** 精确控制每租户的 job 处理配额  
**缺点：** 需要扩展 `ClaimJob` 语义；权重配置需管理面支持

**方案 C：Rate-Limited Worker Pools**

```go
// 每租户独立的 worker goroutine 池，带独立限流
type TenantWorker struct {
    tenant string
    limiter *rate.Limiter  // per-tenant RPS
}
func (tw *TenantWorker) Run(ctx context.Context, sub <-chan repository.Event) {
    for e := range sub {
        _ = tw.limiter.Wait(ctx)  // 租户独立限流
        tw.process(e)
    }
}
```

**优点：** 实现简单，限流参数可在运行时调整  
**缺点：** 无法在租户间动态分配闲置容量

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| 新租户注册后无 channel | 租户创建时自动创建 channel（或懒加载） |
| 租户删除后残留事件 | 清理 channel + 标记租户所有未处理事件为 `skipped` |
| 租户无事件但 channel 占资源 | 空 channel 不占 CPU，仅少量内存（带默认 buffer 的 channel） |
| 事件风暴下的回压 | 建议组合使用：租户隔离 + 每个租户独立 backpressure（如 per-tenant droppable event count） |

---

## 方向四：读取路径可靠性与缓存层次

### 现状与代码证据

**Storage 读取错误直接冒泡为 HTTP 5xx：**

```go
// internal/service/file_crud.go — Get
func (s *FileService) Get(ctx context.Context, tenant, bucket, key string) (io.ReadCloser, repository.Object, error) {
    // ...
    rc, _, err := s.store.Get(ctx, obj.StorageKey)
    if err != nil {
        if errors.Is(err, storage.ErrNotFound) {
            return nil, repository.Object{}, ErrNotFound
        }
        return nil, repository.Object{}, err  // ⚠️ 网络错误 → 500，无重试
    }
    return rc, obj, nil
}
```

**本地存储无临时故障恢复：** 如果 S3 后端（`internal/storage/s3.go:Get`）因限流返回 503，错误直接传递到 handler：

```go
// internal/storage/s3.go — Get (simplified)
func (s *S3Storage) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
    out, err := s.client.GetObject(ctx, &s3.GetObjectInput{...})
    if err != nil {
        return nil, ObjectInfo{}, err  // ⚠️ 包括限流 503、连接超时等
    }
    return out.Body, objectInfoFromS3(out), nil
}
```

**Circuit breaker**（`internal/storage/circuitbreaker.go`）仅防止级联失败，不提供重试或降级：

```go
// circuitbreaker.go
func (cb *circuitBreaker) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
    if !cb.allow() {
        return nil, ObjectInfo{}, ErrCircuitOpen  // ⚠️ 直接返回错误，不尝试备用后端
    }
    rc, info, err := cb.inner.Get(ctx, key)
    if err != nil {
        cb.failure()
    }
    return rc, info, err
}
```

**无对象读取缓存：** 搜索结果缓存（`internal/ai/result_cache.go`）存在，但对象级别的读取缓存完全缺失。频繁读取的热门对象每次都穿透到 storage。

**Stat 同样无缓存：** `HEAD` 请求和条件请求（`If-Match`/`If-None-Match`）每次都需要访问 repository 获取当前 ETag，不支持 ETag 缓存：

```go
// internal/service/file_crud.go — Stat
func (s *FileService) Stat(ctx context.Context, tenant, bucket, key string) (repository.Object, error) {
    // 每次都查 repository（DB），无内存缓存
    return s.repo.GetObject(ctx, tenant, bucket, key)
}
```

**多副本环境无读取亲和性：** 当前 replication 是单向异步的。如果将来实现多副本读取，没有任何机制将读取请求路由到最近的副本。

### 产品价值

| 维度 | 影响 |
|------|------|
| **可用性** | 临时 S3 503 导致客户端 500——即使对象在其他副本上可用。自动重试可将可用性从 99.9% 提升到 99.99% |
| **延迟** | 热门小对象的读取缓存可减少 P99 延迟 10-100 倍（微秒级 vs 毫秒级） |
| **成本** | 缓存减少对后端存储的读取请求（按量付费场景下直接节省费用） |
| **一致性** | 条件请求的 ETag 缓存减少 DB 查询；TTL 控制"脏读"窗口 |

### 架构方案权衡

**层次一：自动重试（低投入高回报）**

```go
// 在 Storage 接口外包装 retry layer
type retryStorage struct {
    inner    Storage
    maxRetries int
    baseDelay  time.Duration
    retryable  func(error) bool  // 判断是否可重试（如 503、连接超时）
}

func (r *retryStorage) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
    for attempt := 0; attempt <= r.maxRetries; attempt++ {
        rc, info, err := r.inner.Get(ctx, key)
        if err == nil || !r.retryable(err) {
            return rc, info, err
        }
        // 指数退避
        select {
        case <-ctx.Done():
            return nil, ObjectInfo{}, ctx.Err()
        case <-time.After(r.baseDelay * (1 << attempt)):
        }
    }
    // 最后一次尝试
    return r.inner.Get(ctx, key)
}
```

可与 circuit breaker 组合：`retry → circuitbreaker → real backend`

**层次二：对象元数据缓存（中等投入）**

```go
type metadataCache struct {
    mu          sync.RWMutex
    entries     map[string]*cacheEntry  // key: tenant/bucket/key
    ttl         time.Duration
    maxEntries  int
}

type cacheEntry struct {
    obj    repository.Object
    expiry time.Time
}

func (s *FileService) Stat(ctx context.Context, tenant, bucket, key string) (repository.Object, error) {
    cacheKey := storageKey(tenant, bucket, key)
    if cached := s.metaCache.get(cacheKey); cached != nil {
        return *cached, nil
    }
    obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
    if err != nil {
        return repository.Object{}, err
    }
    s.metaCache.set(cacheKey, obj)
    return obj, nil
}
```

**Invalidation 策略：**
- 写入（PUT/DELETE）时直接淘汰对应 key 的缓存
- TTL 到期自动失效（默认 5 秒平衡读写频率）
- 可选：`cache-control: no-cache` 请求头强制穿透

**层次三：对象体缓存（高投入）**

对象体缓存适合小对象（<1MB）的场景。对于大对象，缓存反而不利（缓存未命中时需回填大量数据到缓存）。

```go
// objectBodyCache: 仅在对象大小 < threshold 时缓存
const bodyCacheThreshold = 1 << 20  // 1MB

func (s *CachingStorage) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
    info, err := s.inner.Stat(ctx, key)
    if err != nil || info.Size > bodyCacheThreshold {
        return s.inner.Get(ctx, key)  // 大对象穿透
    }
    // 尝试从缓存读取
    if data, ok := s.bodyCache.get(key); ok {
        return io.NopCloser(bytes.NewReader(data)), info, nil
    }
    // 回填缓存
    rc, info, err := s.inner.Get(ctx, key)
    if err != nil { ... }
    data, _ := io.ReadAll(io.LimitReader(rc, bodyCacheThreshold))
    s.bodyCache.set(key, data)
    return io.NopCloser(bytes.NewReader(data)), info, nil
}
```

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| 缓存与后端数据不一致 | TTL 控制脏读窗口；写入时主动淘汰 |
| 缓存雪崩 | 启动时随机化 TTL（±20%）；热点 key 的并发请求只让一个回填（singleflight） |
| 大对象误缓存 | 严格按 `bodyCacheThreshold` 过滤；超过阈值直接穿透 |
| S3 限流重试耗尽 | 最后一条错误返回客户端（CIRCUIT_OPEN 或 EXHAUSTED_RETRIES） |
| 多副本场景缓存一致性 | 缓存淘汰需广播到所有副本（或用集中式缓存如 Redis） |

---

## 方向五：API 治理层 — 请求验证、版本管理、操作审计

### 现状与代码证据

**无统一请求 schema 验证：**

当前每个 handler 自行解析和校验参数：

```go
// internal/api/rest/handler.go — Put
func (h *Handler) Put(w http.ResponseWriter, r *http.Request) {
    key := keyFromPath(r)
    // 校验 key 在服务层进行，但 keyFromPath 从 chi.URLParam 直接拿
    size := r.ContentLength
    ct := r.Header.Get("Content-Type")
    meta := extractMetadataHeaders(r.Header)
    // ... 每个 handler 重复这些解析逻辑
}

// internal/api/s3compat/handler.go — PutObject
func (h *Handler) PutObject(w http.ResponseWriter, r *http.Request) {
    bucket := chi.URLParam(r, "bucket")
    key := keyFromURL(r)
    // 同样的 key 解析逻辑，不同的路径
    meta := s3PutMeta(r.Header)
    // 与 REST handler 不同的元数据提取方式
}
```

**无 API 版本协商机制：**

```go
// cmd/server/main.go — 路由挂载
r.Mount("/v1", rest.NewRouter(...))  // ↑ 硬编码 /v1，无 Accept-Version 协商
```

`/v1` 是唯一版本。如果今后需要向后不兼容的变更（例如搜索请求体格式变更），无法优雅过渡。

**操作级审计仅在 admin 路径存在：**

```go
// internal/api/rest/admin.go — 所有 admin 操作写审计日志
// 但 PUT /v1/files/*、DELETE /v1/files/*、GET /v1/files/* 等常规 CRUD 无审计
// internal/api/rest/router.go 中无 CRUD 审计 middleware
```

```bash
$ grep -rn "audit" internal/api/rest/ --include='*.go'
internal/api/rest/admin_audit_test.go  # admin 审计测试
internal/api/rest/admin.go:            # admin 操作写审计
# CRUD handler 中零 audit 调用
```

**无全局请求变换管线：**

当前 middleware 链固定为：`RequestID → CORS → Auth → Tenant → RateLimit → OTel → Recoverer → AccessLog`

不存在可配置的请求变换（header 注入、key 前缀重写、跨协议 header 适配等）。

### 产品价值

| 维度 | 影响 |
|------|------|
| **企业合规** | 缺乏 CRUD 操作审计的系统无法通过 SOC2、HIPAA 等合规审计——审计需覆盖"谁在何时访问/修改了任何对象" |
| **API 演进** | 无版本协商机制意味着任何向后不兼容的变更都必须新建 `/v2` 前缀——对于微小的 payload 变化，这是巨大的工程成本 |
| **开发者体验** | 无统一验证 = 错误信息不统一、验证逻辑分散、安全检查遗漏风险 |
| **多协议适配** | 无请求变换层意味着 S3 header → REST header 的适配逻辑分散在各 handler 中 |

### 架构方案权衡

**改进一：统一请求验证层**

引入一个声明式 schema 验证中间件（参考 OpenAPI / JSON Schema 验证模式）：

```go
type EndpointSpec struct {
    Method      string
    Path        string
    QueryParams map[string]ParamSpec
    Headers     map[string]HeaderSpec
    Body        BodySpec // JSON schema or content-type validation
    Scopes      []string
}

// 在 router.go 中集中注册
var specs = []EndpointSpec{
    {Method: "PUT", Path: "/v1/files/*", Headers: {"Content-Type": {}, "Content-MD5": {Optional: true}}, Scopes: {"write"}},
    {Method: "POST", Path: "/v1/search", Body: {JSON: true, Schema: searchSchema}, Scopes: {"read"}},
}
```

验证失败时返回统一的 `400 InvalidRequest` JSON 格式（当前各 handler 返回格式不一致）。

**改进二：API 版本协商**

```go
// 无变更：保留 /v1 作为默认版本
// 新增 Accept-Version header 支持
func APIVersionMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        version := r.Header.Get("Accept-Version")
        if version == "" {
            version = "v1"  // 默认
        }
        ctx := context.WithValue(r.Context(), apiVersionKey, version)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}

// handler 根据版本选择行为和响应格式
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
    version := r.Context().Value(apiVersionKey)
    switch version {
    case "v1":
        h.searchV1(w, r)
    case "v2":
        h.searchV2(w, r)
    }
}
```

**优点：** handler 可以共存两个版本，逐步迁移。  
**缺点：** 每个 handler 增加分支逻辑；需建立废弃版本的生命周期策略。

**改进三：CRUD 操作审计**

在 `FileService` 层或 `Service` 层注入审计回调：

```go
// FileService 新增 WithAuditor 选项
type Auditor interface {
    Record(ctx context.Context, op string, obj repository.Object, req RequestInfo)
}

func (s *FileService) WithAuditor(a Auditor) *FileService {
    s.auditor = a
    return s
}

// 在 Get/Put/Delete 的关键路径调用
func (s *FileService) Get(ctx context.Context, tenant, bucket, key string) (io.ReadCloser, repository.Object, error) {
    // ... 现有逻辑 ...
    if s.auditor != nil {
        s.auditor.Record(ctx, "GET", obj, requestInfoFrom(ctx))
    }
    return rc, obj, nil
}
```

**审计记录规则：**
- PUT/DELETE：记录请求者（tenant + key_id）、key、大小、时间、请求 ID
- GET：配置可选（可能产生大量记录）；默认仅记录敏感路径（按前缀匹配）
- GET 审计可考虑抽样（如 1:1000 采样率）
- 审计记录写入独立表（已有的 `audit_log` 表），自动轮转（保留期可配置）

**改进四：请求变换管线**

```go
// 在 middleware 链中插入
type RequestTransform func(*http.Request) *http.Request

// 示例：S3 兼容性 → REST 兼容性
// S3 的 x-amz-meta-* 转换到 REST 格式
// 租户默认值注入
// 请求 ID 注入
```

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| 审计日志增长过快 | 可配置保留期（默认 90 天）；支持 S3 归档到外部存储；支持采样率 |
| 版本协商降级 | 不支持旧的版本时返回 `400 Bad Request` + 支持版本列表 |
| 请求验证遗漏 | 验证层应保持与 OpenAPI spec 的一致性（当前已有 `openapi.json` 生成） |
| 变换管线的性能开销 | 变换操作应为纯内存操作（无 I/O）；在 middleware 链中优先级低 |

---

## 五方向关联与实施建议

```
方向一 (写原子性) ─── 基础依赖 ──→ 所有写入路径
       │
       ▼
方向二 (I/O 架构)  ─── 性能依赖 ──→ 所有读取/写入路径
       │
       ▼
方向三 (租户隔离)  ─── 公平依赖 ──→ 索引、复制、扫描、Webhook
       │
       ▼
方向四 (读可靠性)  ─── 韧性依赖 ──→ 所有读取路径
       │
       ▼
方向五 (API 治理)  ─── 治理依赖 ──→ 所有请求路径
```

### 实施阶段建议

| 阶段 | 方向 | 预估工作量 | 产出 |
|------|------|-----------|------|
| **Phase 0** (最小投入) | 方向四重试层 + 方向一 WAL 基础 | 2–3 天 | 写入崩溃安全 + 读取临时故障自动重试 |
| **Phase 1** (核心优化) | 方向二流式加密 + 缓冲区池 | 5–7 天 | 大对象无 OOM、内存占用减半 |
| **Phase 2** (公平 + 治理) | 方向三租户隔离 + 方向五 CRUD 审计 | 5–8 天 | 多租户 QoS + SOC2 合规基础 |
| **Phase 3** (缓存) | 方向四元数据缓存 + 小对象体缓存 | 3–5 天 | P99 GET 延迟降低 10× |

### 快速验证检查列表

**方向一（写原子性）：**
- [ ] 写入过程中 `kill -9` 服务器 → 重启后检查是否存在无对应 DB 行的 storage blob
- [ ] `s.repo.UpsertObject` 模拟失败 → 检查 storage blob 是否残存
- [ ] Multipart complete 过程中崩溃 → 重启后检查对象是否可用

**方向二（I/O 架构）：**
- [ ] 通过 `local` 后台上传 1GB 文件 → 监控 `GOMAXPROCS` 下 RSS 是否约等于文件大小
- [ ] 流式加密的 4KB 对象 vs 4GB 对象 → 内存分配次数差异
- [ ] `io.Copy` 缓冲区尺寸测试：32KB vs 1MB 在 NVMe 下的吞吐量差异

**方向三（租户隔离）：**
- [ ] 租户 A 批量上传 10,000 文件 → 租户 B 的一个文件索引延迟
- [ ] 租户 A 的 job 持续失败 → 检查 job 表中租户 B 的 job 是否被阻塞
- [ ] 高事件吞吐下 bus.dropped 计数 → 检查丢弃事件的租户分布

**方向四（读可靠性）：**
- [ ] 模拟 S3 后端返回 503 → 确认自动重试和指数退避
- [ ] 热门对象反复 HEAD → 确认 stat 缓存命中
- [ ] circuit breaker 打开后的降级行为

**方向五（API 治理）：**
- [ ] `PUT /v1/files/` 不带 Content-Type → 统一错误格式 vs 各 handler 自行处理
- [ ] Accept-Version: vintage-2024 → 确认版本协商路径
- [ ] 审计表中是否有 `GET secret-project/api-key.txt` 的记录
