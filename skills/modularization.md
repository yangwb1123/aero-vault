# Skill: Modularization

## Goal

确保项目模块按业务领域组织，避免技术分层导致的耦合。

## Principles

### 1. Feature-first 组织

```
✅ 正确：
internal/
  payment/
    handler.go
    service.go
    repository.go
    dto.go

❌ 错误：
internal/
  handlers/payment.go
  services/payment.go
  repositories/payment.go
```

### 2. 禁止模式

| 反模式 | 原因 | 替代方案 |
|--------|------|---------|
| `utils/` | 无职责边界 | 按功能分散到各模块 |
| `common/` 超过 20 文件 | 包膨胀 | 拆分子包 |
| `helper/` | 无法维护 | 具名模块函数 |
| God Service | 单一类型 > 300 行 | 拆为多个 service |

### 3. 包依赖规则

- 禁止循环依赖
- 禁止 infra 包反向依赖 domain 包
- 模块间通过接口解耦
- 共享类型放在模块根目录的 `types.go` 中

## Output

重构完成后输出：

- 新模块目录结构
- 移动的文件列表
- 依赖关系变化
- 验证结果
