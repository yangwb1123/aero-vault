# BOOTSTRAP.md — 项目全局知识注入

> Agent 启动时**必须首先加载此文件**。定义项目身份、技术栈、架构原则等不常变更的全局知识。
> 详细合约见 `AGENTS.md`，当前目标见 `CURRENT_SPRINT.md`。

---

## 项目身份

| 属性 | 值 |
|------|-----|
| 名称 | **aero-vault** |
| 定位 | AI-native file platform |
| 描述 | 同一套后端同时暴露 REST / S3 兼容 / WebDAV / MCP 四种协议，内置 RAG 流水线、多租户、可观测性 |
| 版本 | v0.4.0 |
| 入口 | `cmd/server/main.go` |

## 技术栈

| 层 | 技术 | 备注 |
|----|------|------|
| 语言 | **Go 1.25** | 标准库优先，零 CGO |
| 路由 | **chi/v5** | 仅 HTTP 路由 |
| 数据库 | **SQLite** (默认) / **Postgres** | 嵌入式 vs 生产 |
| AI 向量 | 内存暴力 / **pgvector** / **Qdrant** | opt-in |
| AI 全文 | 内存 BM25 / **pgFTS** | opt-in |
| 存储 | local / S3 / 阿里云 OSS / 腾讯云 COS | 可插拔 |
| 可观测 | **OpenTelemetry** + **Prometheus** | OTLP/HTTP |
| 认证 | API Key / JWT (HS256) / SigV4 | |

## 核心架构

```text
Protocol (REST / S3 / WebDAV / MCP)
    ↓
Middleware (RequestID → CORS → Auth → Tenant → RateLimit → OTel → Recoverer → AccessLog)
    ↓
FileService — 唯一对象 CRUD 入口
    ↓
Storage (local★/s3/oss/cos) + Repository (SQLite★/Postgres)
    ↓
EventBus → Workers (Indexer / AV / Replication / Webhook / Reconcile)
```

## 关键工程原则

1. **文件 ≤ 500 行**，函数 ≤ 50 行，圈复杂度 ≤ 10
2. **无 `utils/` `common/` `helper/` 包** — 按领域分散
3. **无 God 类型**（单类型 ≤ 300 行）
4. **所有业务逻辑必须可测试**
5. **重构优先级高于功能开发**
6. **Stdlib 优先** — 新依赖需论证
7. **Opt-in 安全默认** — AI/pgvector/Qdrant/events 均默认 off
8. **工程门禁自动化** — `python3 cli.py accept` 提交前置，CI 自动执行
9. **配置驱动** — 阈值声明于 `engineering.yaml`，非硬编码

> 完整约束见 `AGENTS.md §0 工程约束`。

## 关键约定

| 约定 | 描述 |
|------|------|
| SQL 占位符 | 使用 `$N` 经 `s.rebind` 改写；每个 bind 独立编号 |
| 迁移双文件 | 每次 schema 变更 = `migrations/{sqlite,postgres}/NNNN_*.{up,down}.sql` |
| 存储 key | `path.Join(tenant, bucket, key)` + versioned 追加 `@v<id>` |
| Middleware 顺序 | `RequestID → CORS → Auth → Tenant → RateLimit → OTel → Recoverer → AccessLog` |
| Handler 不自挂中间件 | 隔离 handler 测试无 tenant/auth — 设计行为 |
| 日志 | 统一 `slog`，JSON 格式 |
| 时间 | `time.RFC3339Nano` |
