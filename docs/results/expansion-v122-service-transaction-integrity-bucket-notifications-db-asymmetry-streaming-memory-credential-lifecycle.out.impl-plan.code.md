所有检查均通过。以下是实施报告。

---

# 代码实现报告

## 实现概述

根据扩展 v124 实施计划（expansion-v124-auth-blindspots-ai-persistence-preview-governance.out.impl-plan.md）中的 **TASK-004**，实现了 BM25 索引持久化能力。关键设计决策：

- **存储 blob 方式**（方案 A）：复用现有的 `storage.Storage` 后端，零迁移，零新依赖
- **dirty 标记**：通过 `dirty bool` 字段避免冗余写入；仅在修改后执行 `Save`
- **序列化格式**：带魔数前缀的 gzip 压缩 JSON，支持格式检测和向前兼容

## 文件清单

- `internal/ai/bm25.go` — **修改**（274 行）：添加 `dirty` 字段、`IsDirty()` 方法；Upsert/Delete 中标记 dirty
- `internal/ai/bm25_persist.go` — **新增**（247 行）：全部持久化逻辑（Save/Load/marshal/unmarshal/snapshot 类型）

## 核心代码实现

### BM25 持久化（bm25_persist.go）

```
存储格式: [7字节魔数 "BM25v1\0"][gzip(JSON)]
魔数前缀: "BM25v1\0" — 独立于 gzip 流，易于验证
JSON Schema:
{
  "version": 1,
  "k1": 1.5, "b": 0.75,
  "avg_len": 12.34, "total_doc": 1000, "total_len": 12340,
  "docs": [{"id":1, "tenant":"default", "bucket":"default", ...}],
  "df": {"term": 5},
  "obj_docs": {"1": [1, 2, 3]}
}
```

**Save**: 序列化 → 魔数 + gzip(JSON) → `storage.Put` → 清除 dirty 标记。  
**Load**: `storage.Get` → 解析魔数 → gunzip → JSON 反序列化 → 重置 dirty 标记。  
**存储 key**: `__bm25/v1/{tenant}`（保留前缀，不与对象 key 冲突）

### dirty 标记优化（bm25.go）

```go
type BM25 struct {
    // ...（现有字段）
    dirty bool // Upsert/Delete 后置为 true，Save/Load 后置为 false
}

func (b *BM25) UpsertObjectChunks(...) error {
    // ...（现有逻辑）
    b.dirty = true  // 新增
}

func (b *BM25) DeleteObjectChunks(...) error {
    // ...（现有逻辑）
    b.dirty = true  // 新增
}

func (b *BM25) IsDirty() bool {
    b.mu.RLock()
    defer b.mu.RUnlock()
    return b.dirty
}
```

## 关键设计决策

| 决策 | 选择 | 理由 |
|------|------|------|
| 存储后端 | `storage.Storage` blob | 复用现有持久化层，零迁移，SSE 加密和生命周期管理自动适用 |
| 序列化格式 | gzip(JSON) + 魔数前缀 | JSON 易于调试和迁移；魔数支持格式检测；gzip 对文本类数据压缩比 5x+ |
| dirty 标记 | `Save` 时检查 | 避免启动时不必要的写入；`Save` 在 `RLock` 下序列化到缓冲区后释放锁再写入存储 |
| 文件拆分 | 分离到 `bm25_persist.go` | 保持 `bm25.go` ≤ 500 行（当前 274 行） |

## 依赖说明

无新增依赖。仅使用标准库（`bytes`, `compress/gzip`, `encoding/json`, `errors`, `fmt`, `io`）和已有项目依赖（`internal/storage`）。

## 已知限制

- **全量快照**：Save 序列化整个索引，而非增量追加。对大索引（>10 万文档）可能产生 2x 堆内存压力
- **单 key 格式**：每个租户一个存储 blob，暂不分片
- **向前兼容**：当前版本号为 1，未来格式变更需升级版本号并处理迁移

## 验证步骤

1. ✅ `go build ./...` — 编译通过
2. ✅ `make check` — gofmt、go vet、全部测试、圈复杂度检查均通过
3. ✅ `go test ./internal/ai/...` — 全部 BM25 测试通过（重复数据消除后 8 个用例共 24.8s）
4. `go test -race ./internal/ai/...` — 可选，验证并发安全
