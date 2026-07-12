现在我对整个文档进行了全面验证。以下是准确性评估：

---

## 准确性验证摘要

### ✅ 发现准确（11/12 项关键主张）

| 主张 | 状态 | 证据 |
|--------|--------|---------|
| 不存在内容去重代码 | ✅ | `content_hash` / `ContentHash` / `CASStore` 搜索无结果 |
| `Storage.Put` 接口 | ✅ | `storage.go:107` |
| `file_crud.go:Put` 写入存储 | ✅ | `file_crud.go:71` |
| `validateMetadata` 仅校验长度 | ✅ | `file.go:123-128` — 仅检查大小和键长度 |
| `PostForm` 需要鉴权 | ✅ | `handler.go:99` — 通过中间件链 |
| 存在 `auth/policy.go`（IAM 策略）| ✅ | `policy.go:48` — `ParsePolicy` / `Eval` |
| 存在 `quota.go` 和指标 | ✅ | `quota.go:11` (`UsedBytes`/`UsedObjects`), `metrics.go:17` |
| 存在 `ai_usage_cost_test.go` | ✅ | 已验证 |
| 存在 AI 成本追踪 (`ai/cost.go`) | ✅ | 已验证 |
| Multipart 上传存在 (`file_multipart.go`) | ✅ | `InitMultipart`/`UploadPart`/`CompleteMultipart` |
| `writeMetadataHeaders` 存在 | ✅ | `handler.go:838` |
| 所有 5 个方向在现有文档中为零覆盖 | ✅ | 已验证（v1-v140 均未提及） |

### ⚠️ 一处小误差

| 文档声明 | 实际 | 影响 |
|--------|--------|--------|
| `internal/service/file_features.go:CopyObject` | `internal/api/s3compat/extra.go:copyObject`（小写，第 39 行） | 该功能确实存在，但位于 S3 兼容处理器中，而非文档中所述的 FileService 函数。行为描述（读取源对象 → 完整重写）准确。 |

### 基线验证 — 文档中引用的所有环境变量

| 变量 | 状态 | 位置 |
|--------|--------|--------|
| `AI_INDEX_ENABLED` | ✅ | `config.go:119` |
| `AI_CHAT_PROVIDER` | ✅ | `config.go:142` |
| `AI_RATE_LIMIT_RPS` | ✅ | `config.go:186` |
| `AI_AGENT_MAX_STEPS` | ✅ | `config.go:160` |
| `AI_CHUNK_WINDOW` (600) | ✅ | `config.go:161` |
| `AI_CHUNK_OVERLAP` (80) | ✅ | `config.go:162` |
| `AI_EMBED_DIM` (256) | ✅ | `config.go:124` |
| `STORAGE_BACKEND` | ✅ | `config.go:62` |
| `DB_DRIVER` | ✅ | `config.go:105` |
| `RECONCILE_INTERVAL_MINUTES` | ✅ | `config.go:190` |
| `WEBDAV_PREFIX` | ✅ | `config.go:231` |

---

该文档已在代码库中以两种版本存在：
- **`docs/requirements/expansion-v135-high-value-expansion-directions-phase7.md`**（55,713 字节）
- **`docs/requirements/expansion-v7-fresh-horizons.md`**（55,696 字节）

它们具有相同的内容。您需要我做什么？例如：
1. **将文档合并为一个规范版本**并删除重复
2. **开始实施某个方向**（内容去重是最具影响力的方向，复杂程度较低，带来最多 ROI）
3. **针对单个方向创建更详细的 TDD（技术设计文档）**
4. **验证更多代码锚点**或更新不准确之处（`CopyObject` 路径）
5. **将其集成到 AGENTS.md 路线图**中，以跟踪作为计划中的工作
