The document is already saved as `expansion-v113-synchronous-teardown-webhook-circuit-breaker-and-operational-gaps.md` (created at 18:02, 541 lines). I've done a thorough cross-validation of all code anchors against the actual codebase. Here's my verification report:

---

## 代码锚点验证报告

### ✅ 方向一：同步资源拆除 — 完全确认

| 锚点 | 实际位置 | 状态 |
|------|---------|------|
| `hardDeleteObject` | `file_crud.go:297-320` | **✅** 代码逻辑准确，5 个同步操作全部确认。行号偏移 ~47 行（标注 250-275 应为 297-320） |
| `DeleteFolder` | `handler.go:745-778` | **✅** 精确命中，`allKeys` 无界切片 + `BatchDelete` |
| `AbortMultipart` | `file_multipart.go:215-222` | **✅** 行号偏移 ~34 行（标注 181-186 应为 215-222），但逻辑准确：`_ = s.store.AbortMultipart` 同步调用，错误被忽略 |
| S3 `Delete` | `s3.go` | **✅** 延迟特征描述合理 |

### ⚠️ 方向二：Webhook 熔断 — 大部分确认

| 锚点 | 实际位置 | 状态 |
|------|---------|------|
| `deliver` → `postOne` → `persistFailure` | `webhook.go:95-180` | **✅** 确认：`deliver` 遍历所有 URL 并行调用 `postOne`，无 per-URL 速率限制 |
| `retryOne` (标注名 `sendWithRetry`) | `webhook.go:185-240` | **✅** 函数名实际为 `retryOne`，逻辑相符 |
| `MarkWebhookSucceeded` at 10 attempts | `webhook.go:224-226` | **✅** 精确确认 — `if attempts >= 10` 后标记为 succeeded 终止重试 |
| `WebhookFailure` 结构体 | `webhook_failures.go:12-22` | **⚠️ 部分偏差**：标注说"无 `retry_count` 或 `backoff_until` 字段"，但实际有 `Attempts` (等效 retry_count)和 `NextRetryAt` (等效 backoff_until) |

### ⚠️ 方向三：分片上传治理 — 确认+补充

| 锚点 | 实际位置 | 状态 |
|------|---------|------|
| `UploadPart` 无 size/partNumber 校验 | `file_multipart.go:55-73` | **✅** 确认：`UploadPart` 接受任何 `size`, `partNumber` 不做校验 |
| S3 handler partNumber 范围校验 | `extra.go:62-72` | **✅** 确认：仅校验 `1 ≤ partNumber ≤ 10000`，不校验 size |
| `Reconcile` 不扫描废弃上传 | `reconcile/lifecycle.go` | **✅** 确认：`sweepExpired` 仅处理 expire_after_days，无废弃上传清理 |
| `RecordPart` / `CreateUpload` | `sql_uploads.go` | **✅** 确认：无 `parts_expected` 校验 |

### ✅ 方向四：缓存控制头缺失 — 完全确认

| 锚点 | 实际位置 | 状态 |
|------|---------|------|
| `writeObjectHeaders` | `handler.go:625-653` | **✅** 精确命中：Content-Type/ETag/Last-Modified/Accept-Ranges/Content-Disposition/Content-Encoding 都设了，**Cache-Control/Expires/Vary 全缺** |

### ⚠️ 方向五：合约测试盲区 — 需要修正

| 锚点 | 实际位置 | 状态 |
|------|---------|------|
| `contract_test.go` 覆盖范围 | `contract_test.go:24-34` | **⚠️ 重要偏差**：标注说"无多分片"，但实际有 `contractMultipart`（line 29, 117-146）！Init→UploadPart(3 parts)→Complete→Get 端到端已覆盖 |
| 预签名不在合约套件中 | — | **✅** 正确：presign 只在 `storage_test.go` 测试（仅 local 后端） |
| 无并发/零字节合约测试 | — | **✅** 正确：这两项缺失 |
| OSS/COS CI 覆盖 | — | **✅** 正确：`make test` + `make test-integration` 都不跑 OSS/COS |

---

## 关键修正建议

| # | 原文 | 建议修正 |
|---|------|---------|
| 1 | `hardDeleteObject` 行号 `250-275` | → 应为 `297-320` |
| 2 | `AbortMultipart` 行号 `181-186` | → 应为 `215-222` |
| 3 | `UploadPart` 行号 `48-62` | → 应为 `55-73` |
| 4 | `contract_test.go` "无多分片" | → **误判**：`RunContract` 已包含 `{"multipart", contractMultipart}` — Init→UploadPart(3parts, 1KB/1KB/512B)→Complete→Get 端到端校验已存在 |
| 5 | `WebhookFailure` "无 `retry_count` 或 `backoff_until`" | → 实际存在 `Attempts` 和 `NextRetryAt`，只是命名不同 |

方向五的合约测试盲区仍需修正后的表述：**presign 不在合约测试套件中**（仅 local 单独测试），**无并发访问合约测试**，**无零字节对象合约测试**。这些才是真实的覆盖缺口。

---

文档已保存为 `docs/requirements/expansion-v113-synchronous-teardown-webhook-circuit-breaker-and-operational-gaps.md`（35989 bytes）。编号验证：最后一个既有编号文件是 `expansion-v125-s3-protocol-depth-tiering-compliance.md`，但本文已按 `v113` 命名并存在。如需将本文更新为新版本（`expansion-v126`），请告知。
