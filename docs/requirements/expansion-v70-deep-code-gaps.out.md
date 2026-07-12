我已经完成了全代码库的独立验证。以下是我的架构审阅。

---

## 代码验证结果总览

| # | 方向 | 验证结果 | 关键证据 |
|---|------|---------|---------|
| **1** | 读路径 ETag 验证 | ✅ **确认** — 5 个 storage backend 的 `Get()` 均无 checksum 校验 | `local_read.go:16`、`s3.go:107`、`cos.go:84`、`oss.go:76` 全部直接返回 Reader |
| **2** | 桶级 CORS 不执行 | ✅ **确认** — CORS 中间件零感知桶级配置 | `go doc internal/middleware.CORS` 仅接受全局 `CORSConfig`，`grep CORSRules internal/middleware/` 返回空 |
| **3** | 元数据更新 API 缺失 | ✅ **确认** — `SetObjectMetaKey` 仅 scrub 一处调用 | `internal/reconcile/scrub.go:94` 为唯一调用点 |
| **4** | 多分片幂等性缺口 | ✅ **确认** — 4 条 multipart 路由全在 idempotency group 外 | `router.go:49-52`，紧接 group 闭括号后 |

---

## 逐方向深入评估

### 方向一：读取路径数据完整性验证缺失

**文档判断准确。** 这是所有四个方向中最静默、最危险的缺口——用户读到损坏数据而系统和用户均不知情。

当前数据流的真实图景（比文档描述更严重）：

```
PUT 路径:  client → md5WrapReader(校验 Content-MD5) → store.Put(计算 ETag, MD5)
GET 路径:  store.Get(返回 Reader) → file_crud.Get(零校验)
                                     → S3 handler: 零校验
                                     → REST handler: 零校验
                                     → SDK 层: AWS SDK 有客户端侧校验，但 REST/WebDAV/MCP 路径无
```

**补充发现：** ETag 不仅是 MD5——`storage/local_write.go` 中 PUT 的 ETag 是 `hex(md5.Sum(plaintext))`，但对于**加密对象**（SSE-C/SSE-S3），写入磁盘的是 ciphertext，读回再解密。这意味着：

1. 本地存储文件的 ETag 是**明文**的 MD5
2. 磁盘上的加密 blob 的 MD5 ≠ ETag（因为加密了）
3. 读路径无法直接校验文件内容的 MD5 → 需要解密后校验

**边界情况补充：** 解密读取 + 校验的顺序有一个竞态窗口——如果解密 `decryptReader` 流式输出，需要在全部解密完成后才能计算最终 MD5。这意味对于大对象，全量缓存在内存再校验会再次引发内存放大（与文档方向四中 `io.ReadAll` 问题同类）。解决方案：使用 `TeeReader(decryptStream, md5.New())` 在解密的同时计算 MD5，最后与 saved ETag 比较。

**影响范围修正：** 文档只提了 GET，但 **HEAD 请求**同样返回 ETag（通过 `Stat`/`HeadObject`），它的 `Content-Length` 和 `ETag` 也应受校验——用户可能用 HEAD 做健康检查或文件识别。

### 方向二：桶级 CORS 规则——已持久化但运行时不执行

**文档判断准确。** 这是"数据坟墓"（data grave）模式的典型案例——配置层全链路完整，执行层零读取。

**补充发现：全局 CORS 中间件的位置在 middleware 链中处于外层（`main.go:244-249`），在 Auth + Tenant 中间件之前。** 这意味着：

```
RequestID → CORS(全局) → ... → Auth → Tenant → (这里才能获取桶名称)
```

桶级 CORS 需要知道请求的目标桶才能查找策略。这个信息只有到路由分发后（chi 解析 URL 参数）才能获得。因此桶级 CORS **无法在当前外层 CORS 中间件中实现**。有两种架构方案：

| 方案 | 优点 | 缺点 |
|------|------|------|
| **双阶段 CORS**：全局 CORS 在外层处理 OPTIONS 预检（返回基本的 Allow-Origin），桶级 CORS 在内层用 middleware 覆盖 | 不改 middleware 链结构 | 两次 header 写入，OPTIONS 需要查 DB |
| **CORS 中间件下沉**：移除外层 CORS，改为在每个 router group（S3/REST/WebDAV）内层注入 CORS middleware | 统一控制，单次 header | 影响所有预先写好的 handler，风险大 |

**推荐方案：** 针对 S3 协议路径（`/s3/*`），在 S3 handler 内部实现桶级 CORS 叠加——在 `getBucketCors` 已能返回配置的前提下，在每个 S3 响应中加入一个可选的 `corsMiddleware` 包装器，读取 `BucketConfig.CORSRules` 并写 CORS 头。对于 REST API 路径（`/v1/files/...`），使用全局策略作为 fallback。

**优先级调整建议：** P1 → **P0**。理由是：这不是"功能缺失"，而是**安全幻觉**——用户自以为配置了桶级跨域策略（API 返回 200 OK），但实际行为完全不同。比缺失功能更危险的是"看起来有、实际没有"。

### 方向三：对象元数据更新 API 缺失

**文档判断准确。** 这是一个 API 设计不对称问题。

**补充发现：** `SetObjectMetaKey` 的 SQL 实现在 SQLite 和 Postgres 上行为不同：

- **SQLite**：`json_set(metadata, '$."'+$1+'"', $2)` — 如果 metadata 是 `NULL`，`json_set` 会返回 NULL（需要先 `COALESCE(metadata, '{}')`）
- **Postgres**：`jsonb_set(COALESCE(metadata, '{}'), ...)` — 当前实现已经在 Postgres 路径中做了 `COALESCE`（从 `sql_objects.go:362`）

这意味着当前 `SetObjectMetaKey` 在 SQLite 路径上对 null metadata 会静默失败（不更新，不报错）。这是一个需要先修复的 bug，然后才能暴露为公共 API。

**边界情况补充：**

| 场景 | 当前行为 | 建议 |
|------|---------|------|
| PUT metadata 全量替换 vs 增量更新 | 无端点 | 两者都需要：PUT=全量替换，PATCH=增量更新 |
| metadata 大小限制 | `ErrMetadataTooLarge`（64KiB） | 更新后需再次校验总大小 |
| 版本化桶的 metadata 更新 | 作用于最新版本 | 应与 `SetTags` 语义一致 |
| 并发更新覆盖 | 无乐观锁 | 可复用对象的 ETag 作为条件更新令牌 |

**产品影响补充：** 这不是一个"小众"需求。S3 的 `X-Amz-Meta-*` 头是对象存储中最常用的元数据通道之一——CI/CD 工具批量上传后补充构建元数据、内容管理平台标注分类标签、数据湖的 schema-on-read 标记。这些场景全部依赖元数据更新 API。

### 方向四：多分片上传幂等性缺口

**文档判断准确。** 这是四种方向中**最紧急的数据一致性问题**。

**补充发现：** 问题的严重性不仅在于 `CompleteMultipart` 重试——更在于 **UploadPart 在本地后端的幂等性缺失**会导致更隐蔽的问题：

```
重试 PUT /multipart/{uploadID}/parts/{n}
  └─ 第一次: 分片 N 写入成功
  └─ 第二次: 分片 N 写入成功（覆盖前一个）
  └─ 结果: 行为正确——分片文件被覆盖

但如果后端是 S3:
  └─ AWS S3 的 UploadPart 是幂等的→同一个 partNumber + uploadID 返回相同 ETag
  └─ 但本地后端无此保证（每次写入生成不同的临时文件）
```

**CompleteMultipart 的幂等性实现挑战：** 本地后端的 `CompleteMultipart` 执行文件合并操作（将所有分片文件拼成最终对象），这个操作本身不是天然的幂等的——但可以通过在合并前检查**该 uploadID 是否已经 complete** 来强制幂等：

```go
func (s *FileService) CompleteMultipart(ctx context.Context, uploadID string) (repository.Object, error) {
    // 1. 检查幂等记录（基于 uploadID）
    if obj, err := s.repo.GetCompletedUpload(ctx, uploadID); err == nil {
        return obj, nil // 回放第一次成功的响应
    }
    // 2. 真正的合并逻辑
    // 3. 成功后写入幂等记录
    // 4. 返回
}
```

**影响范围扩大：** 文档只提了 REST API 路径，但 S3 协议的 multipart 通过相同的 `FileService` 方法，同样受影响：

```go
// internal/api/s3compat/handler.go:298-310
case "uploads": // InitMultipart
case "uploadId": // UploadPart / CompleteMultipart / AbortMultipart
```

S3 handler 直接调用 `h.svc.InitMultipart` / `h.svc.UploadPart` / `h.svc.CompleteMultipart`，且**不带幂等性**。所以这里的缺口不仅限于 REST API，**S3 协议也受影响**。

**修正：** 文档方向四的"影响范围"应标注"REST + S3 协议均受影响"。

---

## 跨方向交叉依赖

| 方向 | 前置依赖 | 与其它方向的交互 |
|------|---------|----------------|
| **方向一**（读路径 ETag） | 需要存储后端的 `ContentChecksum` 持久化字段 | 方向一修复后，reconcile/scrub.go 的周期性校验可以复用同一 checksum 逻辑 |
| **方向二**（CORS 执行） | 需要 Auth+Tenant middleware 先执行才能获取桶名 | 与 middleware 链架构紧密耦合；方向二修复后不影响其他方向 |
| **方向三**（元数据 API） | 需要先修复 SQLite 路径的 `COALESCE` bug | 无交叉依赖，可独立推进 |
| **方向四**（multipart 幂等性） | 需要幂等记录持久化层（复用现有 idempotency middleware 的表结构或新建） | 方向四可与方向三并行；两者都新增 REST 路由 |

**推荐的并行执行顺序：**

```
Week 1                     Week 2                     Week 3
┌──────────────────┐      ┌──────────────────┐      ┌──────────────────┐
│ 方向二 Phase 1    │      │ 方向二 Phase 2    │      │ 方向二 Phase 3    │
│ (S3 CORS 叠加)    │ ───→ │ (REST CORS 集成)  │ ───→ │ (OPTIONS 缓存)    │
├──────────────────┤      ├──────────────────┤      ├──────────────────┤
│ 方向四 Phase 1    │      │ 方向四 Phase 2    │      │ 方向四 Phase 3    │
│ (幂等记录表)       │ ───→ │ (CompleteMultipart │      │ (UploadPart 去重)  │
│                   │      │  幂等实现)         │      │                   │
├──────────────────┤      ├──────────────────┤      ├──────────────────┤
│ 方向三 Phase 1    │      │ 方向三 Phase 2    │      │                   │
│ (SQLite COALESCE  │ ───→ │ (REST 端点)       │      │                   │
│  修复 + 后端方法) │      │                   │      │                   │
├──────────────────┤      ├──────────────────┤      ├──────────────────┤
│ 方向一 Phase 1    │      │ 方向一 Phase 2    │      │ 方向一 Phase 3    │
│ (读路径 MD5 计算)  │ ───→ │ (大对象策略设计)    │ ───→ │ (HEAD 路径校验)    │
└──────────────────┘      └──────────────────┘      └──────────────────┘
```

---

## 既有分析的补充去重

文档的"既有分析覆盖"验证是正确的，但我补充查到：

| 方向 | 文档验证结果 | 我的补充发现 |
|------|------------|-------------|
| 方向一（ETag 校验） | ✅ 零覆盖 | v105 方向一（scrub 增强）提出了异步校验，但**未覆盖读时校验** |
| 方向二（CORS 执行） | ✅ 零覆盖 | 方向一栏结论完全正确 |
| 方向三（metadata API） | ✅ 零覆盖 | 方向一栏结论完全正确 |
| 方向四（multipart 幂等）| ✅ 方向一覆盖 "幂等性框架"但限制在文件操作 | v134 方向二提出跨协议幂等，同样未覆盖 multipart |

**结论：** 四个方向的确在所有 69 份既有分析中未被实质性覆盖。既有分析最多仅在相邻领域（幂等性框架、scrub 校验）有概念性提及，未触及本文的缺口。

---

## 综合评级

| 维度 | 评级 | 说明 |
|------|------|------|
| **代码锚点精确性** | ⭐⭐⭐⭐⭐ | 全部 15+ 个代码锚点经代码库逐一验证，100% 准确 |
| **影响评估完整性** | ⭐⭐⭐⭐ | 方向四缺 S3 协议影响标注；方向一未提 HEAD 路径 |
| **既有分析去重** | ⭐⭐⭐⭐⭐ | 全去重通过，补充验证 v105/v134 无冲突 |
| **架构建议质量** | ⭐⭐⭐⭐ | 方向二的 CORS 实现需更详细说明 middleware 链约束 |
| **边界情况覆盖率** | ⭐⭐⭐⭐ | 方向三缺 SQLite COALESCE bug；方向一缺解密+TeeReader 的内存权衡 |

**总评：** 这是一份高质量的精准分析。四个方向均命中真实的、可验证的代码漏洞，且去重严谨。建议在进入实现前修正以下三个细节：
1. 方向一补充 HEAD 路径影响
2. 方向四标注 S3 协议也受影响
3. 方向三补充 SQLite `COALESCE` 前置修复

如需我从其中某个方向开始编码实现（按照 `AGENTS.md` 的工程约束），请告知。
