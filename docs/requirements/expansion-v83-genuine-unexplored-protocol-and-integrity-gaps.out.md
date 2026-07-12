## 代码锚点验证报告

我已对全文档 5 个方向的所有代码锚点进行了逐行验证。以下是验证结论：

---

### ✅ 方向一：S3 ListObjects `delimiter`/`CommonPrefixes`

| 锚点 | 验证结果 | 备注 |
|------|---------|------|
| `handler.go:449-545` | ✅ 准确 | `listObjectsV2` 读 `prefix`/`token`/`maxKeys`，不读 `delimiter` |
| `xml.go:10-38` | ✅ 准确 | 两个结构体均无 `Delimiter`/`CommonPrefixes` |
| `sql_objects.go:ListObjects` | ✅ 准确 | SQL 仅 `WHERE key LIKE prefix%`，无分组逻辑 |

**去重验证**：grep 在 v1–v82 范围内确实为 **0 命中**（v112/v125 是后续文档）。

---

### ✅ 方向二：SigV4 `x-amz-content-sha256` 正文完整性

| 锚点 | 验证结果 | 备注 |
|------|---------|------|
| `sigv4.go:83-118` | ✅ 准确 | L86-87 注释明确："so the body need not be read to verify the signature" |
| `sigv4_chunk.go:48-75` | ✅ 准确 | L55 注释："Per-chunk signatures are not re-verified" |
| `sigv4_test.go` | ✅ 准确 | 测试仅验证签名可验证，无 payload hash 比对测试 |
| `file_crud.go:md5WrapReader` | ✅ 准确 | 仅校验 `Content-MD5`（base64 MD5），非 `x-amz-content-sha256` |

**去重验证**：v1–v82 中的 `x-amz-content-sha256` 提及均关于内容寻址/预签名条件，非 SigV4 payload 完整性。✅ 实质零覆盖。

---

### ✅ 方向三：ETag JSON vs HTTP 格式

| 锚点 | 验证结果 | 备注 |
|------|---------|------|
| HTTP 头 L146 | ✅ 准确 | `w.Header().Set("ETag", `"`+obj.ETag+`"")` — 有引号 |
| JSON `folderItem.ETag` L704 | ✅ 准确 | `ETag: obj.ETag` — 裸值 |
| JSON `versionEntry.ETag` L906 | ✅ 准确 | `ETag: obj.ETag` — 裸值 |
| S3 XML `ETag: '"'+o.ETag+'"'` | ✅ 准确 | XML 有引号 |

**去重验证**：v73 的 "ETag 读取验证" 提及的是不同的概念（存储完整性校验），非 JSON/HTTP 格式一致性。✅ 实质零覆盖。

---

### ⚠️ 方向四：Bucket 命名 / Key 校验（有微小偏差）

| 锚点 | 验证结果 | 备注 |
|------|---------|------|
| `FileService.CreateBucket` L235 | ✅ 准确 | 直接 `defaults()` + `repo.CreateBucket()`，零校验 |
| `sql_buckets.go:CreateBucket` | ✅ 准确 | `INSERT OR IGNORE / ON CONFLICT DO NOTHING`，无 CHECK |
| `validateKey` 实际在 L152-160 | ⚠️ 偏差 | 文档写 L130-136，实际偏移至 L152-160 |
| `storage.ErrInvalidKey` 未被引用 | ✅ 准确 | 从未在 `validateKey` 或任何路径引用 |

**去重验证**：grep 结果仅 v83 自身。✅ 实质零覆盖。

---

### ⚠️ 方向五：HTTP 连接池（有微小偏差）

| 锚点 | 验证结果 | 备注 |
|------|---------|------|
| `webhook.go:59` | ⚠️ 偏差 | 文档写 L40，实际 L59 |
| `embedder.go:97-106` | ✅ 准确 | `&http.Client{Timeout: 30 * time.Second}` — 无 Transport |
| `llm.go:69-80` | ✅ 准确 | `&http.Client{Timeout: 90 * time.Second}` — 无 Transport |
| `rerank.go:33-44` | ✅ 准确 | 同上 |
| `antivirus.go:80` | ✅ 准确 | 同上 |
| `kms.go:38` | ✅ 准确 | 同上 |
| `storage.go:NewHTTPClient` | ✅ 准确 | 仅 S3/OSS/COS 使用；其余 9 处客户端零 Transport 配置 |

**注意**：`buildHTTPClient` 函数名在当前代码中不存在，但所有客户端均以 `&http.Client{Timeout: X}` 内联创建，零 Transport 配置。核心问题（`MaxIdleConnsPerHost=2` 瓶颈）确认存在。

---

### 总体评估

| 方向 | 代码锚点准确度 | 去重验证 | 优先级 | 实施预估 | 建议顺序 |
|-----|--------------|---------|--------|---------|---------|
| 1 — delimiters | ✅ 准确 | ✅ 零前例 | P1 | 3 文件 ~200 行 | 第 3  |
| 2 — SigV4 payload | ✅ 准确 | ✅ 零前例 | P1 | 4 文件 ~150 行 | 第 5  |
| 3 — ETag 格式 | ✅ 准确 | ✅ 实质零覆盖 | P2 | 2 位置 ~10 行 | **第 1** |
| 4 — Bucket/Key 校验 | ⚠️ 行号偏移 | ✅ 零前例 | P2 | 2 文件 ~80 行 | **第 2** |
| 5 — HTTP 连接池 | ⚠️ 函数名/行号偏差 | ✅ 全系统零审查 | P2 | 5 文件 ~120 行 | 第 4  |

**文档整体结论**：五个方向均为真实的架构盲区，代码锚点准确，影响分析透彻。存在 3 处微小偏差（行号偏移、函数名变更），不影响实质结论。

---

请问您希望从哪个方向开始实施？根据报告中建议的顺序（方向 3 → 方向 4 → 方向 1 → 方向 5 → 方向 2），我可以立即开始编码。
