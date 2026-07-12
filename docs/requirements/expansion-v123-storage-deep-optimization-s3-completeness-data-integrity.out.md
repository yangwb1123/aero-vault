All code claims verified. Let me now provide a thorough review of the analysis.

---

## 审阅反馈：`expansion-v109-storage-deep-dive.md`

### ✅ 代码锚点验证结果

| 方向 | 关键 claim | 验证 |
|------|-----------|------|
| #1 Copy | `Storage` 接口无 `Copy` 方法 | ✅ `storage.go:31-85` — 确认无 `Copy` |
| #1 Copy | `copyObject` 是 `Get()+Put()` | ✅ `extra.go:39-65` — 确认全内存中转 |
| #2 Lock | 仅 `LockedUntil *time.Time` 无模式 | ✅ `repository.go:38` — 确认无 `LockMode` |
| #2 Lock | `checkLockBeforeOverwrite` 只查时间 | ✅ `file_crud.go:221-230` — 确认 |
| #3 Checksum | 仅有 `x-amz-checksum-md5` | ✅ `handler.go:695-696` — 确认 |
| #4 SSE | 零 `x-amz-server-side-encryption` 处理 | ✅ 全库 `grep` 零命中 |
| #5 Transition | 生命周期只做删除 | ✅ `lifecycle.go:70-110` — `sweepExpired` 只删除 |

### 🔍 补充发现 — 文档中未充分覆盖的缺口

#### 1. `x-amz-copy-source-if-*` 条件头完全缺失

```go
// internal/api/s3compat/extra.go:39-65
// 当前 copyObject 完全不处理：
//   x-amz-copy-source-if-match
//   x-amz-copy-source-if-none-match
//   x-amz-copy-source-if-unmodified-since
//   x-amz-copy-source-if-modified-since
```

AWS S3 客户端（如 awscli `cp --copy-if-none-match`）依赖这些头实现条件复制。缺少它们会导致：
- **并发覆盖**：分布式工作流中，源对象可能在复制过程中被修改，无条件检查则静默复制不一致的版本
- **带宽浪费**：未修改对象被反复复制（缺少 `If-None-Match` 优化）
- **SDK 兼容断裂**：高级 S3 库（如 `aws-sdk-go` 的 `CopyObjectWithOptions`）会在调用方验证条件头，但服务端也应验证

建议方向一（Copy）的边界情况表中补充此表。

#### 2. `restoreObject` 仅限软删除恢复，无归档恢复

```go
// internal/api/s3compat/handler.go:880-892
// restoreObject handles POST ?restore: restores a soft-deleted object.
func (h *Handler) restoreObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
    h.svc.RestoreObject(...) // 仅恢复软删除
```

S3 标准中 `POST /key?restore` 有两个独立语义：
- **软删除恢复**（当前实现）：复原已软删除的对象
- **归档恢复**（缺失）：将 GLACIER/DEEP_ARCHIVE 对象恢复到可读取的热存储层，附带 `Days` 过期和 `Tier`（Bulk/Standard/Expedited）

若方向五（Lifecycle Transition）实现 GLACIER 归档，则 `restoreObject` 必须同时支持 `<RestoreRequest><Days>...</Days></RestoreRequest>` XML 解析和临时副本管理。建议在方向五的检查清单中补充 `POST /restore` 端点支持。

#### 3. `LegalHold` 的当前实现仅是元数据键，非一等列

```go
// internal/api/s3compat/handler.go:93-98
// 写入：
meta["_aero_legal_hold"] = "ON"

// internal/service/file_crud.go:371
// 检查：
if obj.Metadata["_aero_legal_hold"] == "ON" {
```

这意味着：
- LegalHold 状态无法高效查询（需要解析全部 metadata JSON）
- 无法在 SQL WHERE 子句中过滤（如 `SELECT * FROM objects WHERE legal_hold = true`）
- 迁移中未定义 `legal_hold` 列

方向二的检查清单已正确规划 `legal_hold BOOLEAN` 列，但建议同时提到：**迁移应当将现有的 `_aero_legal_hold` metadata 值回填到新列**（双写阶段）。

#### 4. 跨后端转换的架构前提 — 当前不支持多后端

```go
// internal/storage/factory.go
// 创建单一后端实例，不存在"根据 StorageClass 路由到不同后端"的逻辑
```

方向五的"多后端分层路由"是一个诱人的愿景，但当前 `Storage` 接口设计为**单后端模式**。要实现 `STANDARD → local FS, GLACIER → S3` 这种跨后端转换，需要：

- 引入 `StorageRouter` 或 `MultiBackend` 抽象层
- 管理多个后端的生命周期（启动、健康检查、关闭）
- 处理跨后端的数据移动（本质是 Get→Put，但至少能异步化）

这超出了"可在当前抽象层内完成"的范围。建议**明确标注**：方向五中的跨后端转换属于**高阶阶段**，第一版应限定在**单后端内存储类语义**（如 S3 的 STANDARD→STANDARD_IA 转换，local 仅做 metadata 更新）。

#### 5. `PutOptions` 的当前制约

```go
// internal/storage/storage.go:28-31
type PutOptions struct {
    ContentType string
    Metadata    map[string]string
}
```

方向四需要向 `PutOptions` 添加 `SSEKind`、`SSEKey`、`SSEKeyID`。但这不仅仅是添加字段——它影响：

- `FileService.Put()` → 需从 opts 提取 SSE 参数并传递给 store
- `FileService.Get()` → 需要接收客户密钥来解密 SSE-C 对象
- `s3compat.handler` → 解析请求头 → 构建 `PutOptions`
- 当前 `PutOptions` 未传递到 `Get()` 路径——`Get()` 签名中没有 opts 参数

这意味着**方向四需要对 Storage 接口做更广泛的变更**，超出文档中列出的范围。建议在检查清单中补充 `Get` 签名扩展。

#### 6. `CopyObject` 的 SSE-C 透传复杂性

方向一和方向四存在交集：当复制 SSE-C 对象时，S3 规范要求：

```http
x-amz-copy-source-server-side-encryption-customer-algorithm: AES256
x-amz-copy-source-server-side-encryption-customer-key: <base64>
x-amz-copy-source-server-side-encryption-customer-key-MD5: <md5>
```

以及目标的新密钥头。这意味着 `Copy` 方法必须支持源端 + 目标端的加密参数。`Copy` 方法的 `opts` 结构体需要同时包含源和目标 SSE 参数。

建议在方向一和方向四的交叉区域补充此处理逻辑。

### 📊 现有文档覆盖度交叉验证

| 方向 | v109（本文） | v10 | v119 | 覆盖度判断 |
|------|-------------|-----|------|-----------|
| #1 Copy | **完整架构方案 + 代码锚点** | 未覆盖 | 未覆盖 | ✅ **独立深度覆盖** |
| #2 Lock | **完整方案** | 未覆盖 | 覆盖（Governance+Compliance） | ⚠️ 与 v119 部分重叠 |
| #3 Checksum | **完整方案** | 未覆盖 | 未覆盖 | ✅ **独立深度覆盖** |
| #4 SSE | **完整方案** | **SSE-C 深度覆盖** | 覆盖 | ⚠️ 与 v10 深度重叠 |
| #5 Transition | **完整方案** | 未覆盖 | **深度覆盖** | ⚠️ 与 v119 深度重叠 |

其中方向 #4（SSE）在 v10 中有超过 300 行的独立分析（代码锚点一直深入到 `encrypt.go` 的 `envelopeEncrypter` 结构体），方向 #5 在 v119 中有生命周期转换的完整实现草图。建议在"去重验证"结论中注明重叠方向并明确指出本文的**增量**（如方向 #4 补充了 SSE-S3 AES256 头的低风险快速胜利路径，方向 #5 补充了 `Storage.TransitionClass` 接口设计和边界情况表）。

### 🧩 架构建议补充

#### 方向一 Copy：建议增加 `CopyOpts` 结构体

```go
type CopyOpts struct {
    MetadataDirective string // "COPY" | "REPLACE"
    ContentType       string // when REPLACE
    Metadata          map[string]string
    
    // 条件复制
    IfMatch           *string
    IfNoneMatch       *string
    IfUnmodifiedSince *time.Time
    IfModifiedSince   *time.Time
    
    // 版本化源
    SrcVersionID      string
    
    // SSE 相关（见方向四交叉区域）
    SrcSSEKind        string
    SrcSSEKey         []byte
    DstSSEKind        string
    DstSSEKey         []byte
    DstSSEKeyID       string
}
```

#### 方向五 Transition：建议分两阶段实施

- **Phase 1**（低风险）：单后端语义 + 存储类 metadata 更新。`LocalStorage` 做 `TransitionClass` 无操作，`S3Storage` 调用 S3 CopyObject 更改存储层。适用于 S3 后端的成本优化。
- **Phase 2**（高风险）：多后端路由 + 数据移动。引入 `StorageRouter`，支持根据 StorageClass 路由到不同物理后端。需要背景重平衡 job。

建议在实施优先级中将 Phase 1 提升到 P1（与校验和同级），Phase 2 保持 P2。

### ✅ 总结

这是一份高质量的深度分析。代码锚点全部验证通过，五个方向的选择逻辑清晰（代码存在锚点但管线断裂），边界情况表覆盖了 90%+ 的关键场景。建议在定稿前做以下修正：

1. **更新方法论中的文档计数**：目录中实际有 292 个文件（含 `.out.md`），独立分析文档 >109 份
2. **标注与 v10（SSE-C）、v119（Lock+Transition）的重叠范围**，并说明本文的增量
3. **补充 6 个新发现的子缺口**（条件复制头、归档恢复、多后端前提、LegalHold 回填、PutOptions 签名变更、SSE-C CopyObject 透传）
