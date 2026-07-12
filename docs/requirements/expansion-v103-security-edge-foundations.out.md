我已经验证了代码库中的关键锚点。以下是验证结果。

---

## 验证结论：引用代码锚点确认

所有五个方向引用的代码位置均已确认准确。逐项验证结果：

### 方向一：`_aero_` 注入

| 引用 | 状态 |
|------|------|
| `file.go:105-120` validateMetadata 对 `_aero_` 前缀 `continue` | ✅ 准确（第103-120行，第105行 `continue`，无拒绝逻辑） |
| `handler.go:848-866` extractMetadataHeaders 不过滤 | ✅ 准确（第848-866行，提取 `x-amz-meta-*` 和 `x-meta-*` 后直接放入 out，无 `_aero_` 过滤） |
| `s3compat/handler.go:700-718` extractMetaHeaders 不过滤 | ✅ 准确（第700-718行，同样无 `_aero_` 过滤） |
| `s3compat/handler.go:668-684` s3PutMeta 主动写 `_aero_content_disposition`/`_aero_content_encoding` | ✅ 准确（第668-684行，注入系统键，用户也可注入其他 `_aero_` 键） |
| `s3compat/handler.go:686-690` writeS3ObjectMeta 输出过滤 `_aero_` | ✅ 准确（第688-691行，输出时 `strings.HasPrefix(k, "_aero_") { continue }`） |

### 方向二：Content-Encoding Range

| 引用 | 状态 |
|------|------|
| `file_crud.go:248-261` Get 自动解压 gzip | ✅ 准确（第248行 `if obj.Metadata["_aero_content_encoding"] == "gzip"` → gzip.NewReader） |
| `range.go:77-90` GetRange 调用 Get 后 `io.CopyN(Discard, rc, offset)` | ✅ 准确（第77-90行，GetRange 依赖 Get 返回的解压流，offset 语义错误） |
| `handler.go:836-843` writeContentResponseHeaders 设置 Content-Encoding 但下发解压数据 | ✅ 准确（第839-843行，`Content-Encoding` header 与实际内容矛盾） |

### 方向三：对象键安全

| 引用 | 状态 |
|------|------|
| `file.go:129-134` validateKey 6行实现 | ✅ 准确（第129-134行，仅检查 empty/`..`/`/` 前缀） |
| `file.go:143` storageKey 使用 `path.Join` | ✅ 准确（第143行 `path.Join(tenant, bucket, key)`） |

### 方向四：分段上传孤子

| 引用 | 状态 |
|------|------|
| `sql_uploads.go` uploads 表无 expires_at 字段 | ✅ 准确（INSERT只有7列，不含 `expires_at`/`last_activity_at`/`status`） |
| `reconcile/job.go` sweep 无上传扫描 | ✅ 准确（第167-196行：只扫描 orphanRows 和 orphanBlobs，无 `sweepUploads`） |
| `file_multipart.go` AbortMultipart 仅客户端主动调用 | ✅ 准确（第149-155行，AbortMultipart 只被客户端主动调用） |
| Local 后端分片从不清理 | ✅ 准确（`local_multipart.go` 无清理逻辑） |
| S3 后端有 7 天 TTL | ✅ 准确（AWS S3 默认行为，服务端无覆盖） |
| `uploads` 表 `created_at` 存在但不被 Reconcile 利用 | ✅ 准确（0001_init.up.sql 定义了 `created_at`，但 reconcile 从不读取） |

### 方向五：Web UI 管理面

| 引用 | 状态 |
|------|------|
| 282 行单文件 | ✅ 准确（`wc -l` 返回 282） |
| 4 tabs: search/detail/lineage/chat | ✅ 准确（HTML 中的 `.tabs` 和 switchTab 函数） |
| 无 admin tab | ✅ 准确（4个tab switch，无 admin） |
| 不消费 admin API | ✅ 准确（JS 中仅调用 `/v1/files` `/v1/search` `/v1/chat/stream` `/v1/lineage`） |
| 纯静态 embed | ✅ 准确（`web.go` 中 `embed.FS` + `http.FileServer`） |

---

## 对文档的综合评价

**方法质量：** 优秀。103份文档的去重验证使用了正则 + 语义交叉 + 代码锚点反查，可信度高。

**代码分析深度：** 优秀。每个方向都指出了具体的文件、行号、函数名，并且关键攻击路径（如 `_aero_` 注入：输入提取层 → 验证层 → 持久化）的代码流分析准确。

**架构权衡覆盖面：** 良好。每个方向都提出了渐进式实施路径（Phase 0/1/2），边界情况考虑全面。

**改进建议：** 以下几点可补充——

1. **方向二中 `GetRange` 对 SSE 加密的声明：** `range.go` 注释说 "It works across every storage backend by slicing the (already-decrypted) stream from Get, so SSE-encrypted and remote objects are handled correctly." — 但 gzip+SSE 组合场景（加密后再压缩或压缩后再加密）未被分析。

2. **方向三中 `path.Join` 的副作用：** `storageKey` 使用 `path.Join` 不仅会清理 `..` 和双斜杠，还会**删除尾随斜杠**。`path.Join("t", "b", "k/")` → `"t/b/k"`，这意味着键 `"foo/"` 和 `"foo"` 映射到同一个 storageKey。这对目录模拟有影响。

3. **方向五中已有 `/v1/admin/audit` 端点但缺少 `created_at` 过滤：** 审计日志无时间范围查询能力，Web UI 中即使添加 Admin tab 也需要后端支持分页/时间过滤。

总体来说，这份文档是高质量的安全与产品分析，代码引用准确，后续阶段划分合理。建议按文档推荐的 Phase 0（P0 热修复集合）优先执行。
