# Skill: Refactor Large File

## Goal

将超过 500 行的 Go 源文件拆分为职责单一的小文件。

## Trigger

当以下情况发生时自动调用本技能：

- 新建文件超过 500 行
- 现有文件超过 500 行且需要修改

## Steps

### 1. 分析职责

阅读文件，识别其承担的职责：

```
Transport / Handler
Controller / Service
Repository / Storage
DTO / Types
Utility / Helper
Config / Constants
```

### 2. 规划拆分

为每个独立职责创建新文件：

```
file.go
├── file_handler.go      — HTTP handler / transport
├── file_service.go      — 业务逻辑
├── file_repository.go   — 数据访问
├── file_types.go        — DTO / types
└── file_test.go         — 测试
```

### 3. 拆分规则

- 每个新文件 ≤ 500 行
- 每个函数 ≤ 50 行
- 同一 package 内拆分，不改变包名
- receiver 方法跟随其类型定义
- 全局函数按职责归入对应文件

### 4. 移动与更新

- 创建新文件，复制对应代码
- 删除原文件中已移出的代码
- 确保 import 完整（Go 工具链会自动检测）
- 更新测试文件中的引用路径

### 5. 验证

```bash
go build ./...
go vet ./...
gofmt -l .
gocyclo -over 10 ./internal/...
go test ./...
```

### 6. 检查清单

- [ ] 编译通过
- [ ] 所有测试通过
- [ ] 行数检查通过（各文件 ≤ 500 行）
- [ ] 圈复杂度通过（≤ 10）
- [ ] 无 import cycle
- [ ] 原文件已删除或显著缩小
