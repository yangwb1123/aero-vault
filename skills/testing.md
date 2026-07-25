# Skill: Testing

## Goal

确保所有业务逻辑可测试且覆盖核心路径。

## Standards

### 1. 测试要求

- 所有业务函数必须有对应的 `_test.go` 文件
- 测试覆盖率 ≥ 80%（核心逻辑）
- Handler 测试使用 `httptest.NewRecorder()`
- Repository 测试使用 SQLite 内存模式

### 2. 测试夹具

```go
// 标准仓库夹具
repo, _ := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "x.db"))
_ = repo.Migrate(ctx)

// 标准存储夹具
store, _ := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})

// Mock AI
ai.MockLLM{}      // 确定性 LLM
ai.HashEmbedder   // 确定性向量
```

### 3. 测试类型

| 类型 | 范围 | 环境 |
|------|------|------|
| 单元测试 | 单个函数/类型 | 无网络，无 Docker |
| 集成测试 | 跨模块 | Postgres / Qdrant Docker |
| Handler 测试 | HTTP handler | `httptest` |
| Contract 测试 | Storage backend | `storage.contract_test.go` |

### 4. 验证命令

```bash
# 单元测试
go test ./...

# 集成测试（需要 Docker）
make test-integration
make test-integration-qdrant
```
