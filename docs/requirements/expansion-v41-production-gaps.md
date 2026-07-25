# AeroVault 高价值扩展方向 v41 — 工程基础设施与平台成熟度缺口

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 230+ `.go` 文件 + `sdk/*` 三套 SDK + `deploy/*` + `.github/workflows/` + `Makefile` + `Dockerfile` + `docker-compose.yml` + `docs/*` — 包括此前 40 期 expansion 分析的 ~200+ 方向）
>
> **分析视角：** 资深架构师 / 产品经理 — 聚焦此前 **40 期 expansion 分析 + `docs/ROADMAP.md`（10 大方向）从未实质性触及的工程基础设施与平台成熟度缺失**
>
> **分析日期：** 2026-07-10
>
> **去重方法：** 逐方向对 `docs/requirements/` 下全部 40 期既有分析（v1–v40） + `docs/ROADMAP.md` + `docs/CHANGELOG.md` + `docs/TODO.md` + `docs/adr/` 进行 `grep` 验证。每个方向在既有文档中 **零实质性独立分析**——即从未作为一个独立的扩展方向被系统论证过（过路提及或单行表格引用不构成实质性分析）。

---

## 前言

此前 40 期 expansion 分析累计覆盖了 **200+ 个方向**，从 AI/RAG 管线（~30 方向）、S3 兼容协议（~22 方向）、存储后端（~24 方向）、认证授权（~24 方向）、多租户（~22 方向）、合规（~16 方向）、可观测性（~20 方向）到存储分层（~16 方向）等核心功能域。ROADMAP 的 10 大方向全部实现。

然而，**所有 40 期分析的共同视角是"功能"：缺什么 API、少什么协议、差什么存储后端。** 它们几乎从未触及一个软件产品从"功能完整"走向"平台成熟"所必须的工程基础设施（Engineering Infrastructure）——即那些**不直接交付业务价值、但没有它们就无法规模化运维、无法建立开发者信任、无法持续交付质量**的底层能力。

本期 5 个方向全部指向这一空白。

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 锚定代码 | 核心缺口 | 40 期覆盖状态 |
|---|------|------|--------|---------|---------|--------------|
| 1 | **SDK 跨语言质量管线** | 工程质量/产品 | **P0** — SDK 是开发者接触产品的第一界面；JS 零测试意味着无声的信任流失 | `sdk/js/aero-vault.js`（1084 行，零测试）、`sdk/python/aero_vault.py`（684 行，最小测试）、`.github/workflows/ci.yml`（无 SDK CI 步骤） | 非 Go SDK 无 CI 门禁、无测试覆盖、无漂移检测、无包发布自动化 | ❌ 0–2 次过路提及，无独立分析 |
| 2 | **生产级 Web 管理控制台** | 产品/体验 | **P0** — 对于缺少专业运维团队的企业用户，控制台是唯一的管理入口 | `internal/webui/static/index.html`（282 行单页 HTML 文件） | Demo 级 UI：无边加载、分页上限 200、无管理功能、无错误处理、无移动适配 | ❌ 0 次分析 |
| 3 | **发布工程与持续交付管线** | 工程基础设施 | **P1** — 无自动化发布 = 无版本 = 无法向用户交付可靠更新 | `.github/workflows/docker.yml`（仅镜像构建）、`Makefile`（无 release target）、根目录无 `VERSION` 或 `RELEASE.md` | 无语义版本策略、无发版自动化、无 Changelog 自动生成、无 Helm chart 版本管理、无二进制分发 | ❌ **零覆盖** |
| 4 | **多架构与跨平台支持** | 工程基础设施/性能 | **P1** — ARM64 已是主流（Graviton、Apple Silicon、RISC-V 前景）；缺失意味着排斥大量部署场景 | `Dockerfile`（仅 amd64/linux）、`.github/workflows/docker.yml`（无 platform matrix 构建） | 无 arm64 支持、无 Windows 构建、无多平台 CI 验证 | ❌ **零覆盖** |
| 5 | **开源社区基础设施** | 治理/产品 | **P2** — 无社区基础设施 = 只能靠团队自给自足，无法吸引外部贡献者 | `.github/`（仅 `workflows/`，无 issue/PR 模板）、根目录无 `CONTRIBUTING.md`/`CODE_OF_CONDUCT.md`/`SECURITY.md` | 无贡献指南、无行为准则、无安全策略、无社区治理文档 | ❌ 1 次单行表格提及 |

---

## 方向一：SDK 跨语言质量管线（Cross-Language SDK Quality Pipeline）

### 现状

Go SDK — `sdk/go/aerovault/`：
- `client.go` 1006 行
- `client_test.go` **1013 行**（测试超过源码行数）
- 覆盖度：Upload、Get、Delete、Search、Chat、ChatStream、Presign、Bucket CRUD、版本管理、14 个 admin 方法
- CI 门禁：无（Go SDK 与服务器同 repo，靠 `go build ./...` 间接验证）

JavaScript SDK — `sdk/js/aero-vault.js`：
- **1084 行**，**零测试文件**
- 14 个 admin 方法与 Go SDK 平行实现
- 无 CI 门禁、无 Node.js 版本矩阵测试
- 无 npm 包发布自动化（`package.json` 存在但无 `publish` script）

Python SDK — `sdk/python/aero_vault.py`：
- 684 行
- 测试文件 `test_aero_vault.py`：**189 行**，覆盖率极低
- 无 CI 门禁、无 Python 版本矩阵测试（3.9–3.13）
- 无 PyPI 包发布自动化（`pyproject.toml` 存在但无 CI 发布）

### 为什么需要

**SDK 是产品的"脸面"。** 对于大多数开发者而言，他们不会先阅读你的服务器代码——他们会安装 SDK、运行你的代码示例。如果 SDK 有 bug、行为漂移、或文档与实现不一致，他们对整个产品的信任就会崩塌。

具体风险：

1. **无声的 API 漂移**：服务器新增 `/v1/admin/quotas` 端点，Go SDK 同步更新，JS/Python 可能遗漏。因为没有跨语言的一致性 CI gate，差异只在用户运行时报错时才暴露。

2. **JS SDK 零测试**：1084 行生产代码零测试覆盖率。任何修改都可能无声引入 bug。`package.json` 中有 `"scripts": {"test": "node selftest.mjs"}`，但 `selftest.mjs` 仅 18765 行——且需要运行中的服务器，不是单元测试。

3. **Python SDK 测试严重不足**：189 行测试覆盖 684 行代码（覆盖率约 27%）。无 mock server，测试依赖真实 HTTP 端点。

4. **无包发布管线**：SDK 更新需手动 `npm publish` / `twine upload`。容易出错且不可重复。

### 边界情况

| 场景 | 当前行为 | 缺口 |
|------|---------|------|
| 服务器新增可选参数（如 `SearchRequest.Mode`） | Go SDK 编译期捕获类型缺失；JS/Python 运行时 undefined 崩溃 | 无 cross-language schema 验证 |
| 服务器修改响应 JSON 字段名 | Go SDK 编译安全（struct tag）；JS/Python 静默返回 undefined | 无响应契约测试 |
| 服务器弃用一个端点 | SDK 仍暴露该方法，用户收到 404 而非编译期/启动期警告 | 无 deprecation header 处理 |
| npm/pypi 发布失败（权限/网络） | 手动发布，无回滚方案，无法重复构建 | 无 CI-based 发布 |

### 架构概要

```
┌─────────────────────────────────────────────────────────────┐
│                  SDK Quality Pipeline                        │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  1. OpenAPI spec 作为单一事实来源                              │
│     └── CI gate: 每次 PR 验证 OpenAPI 与 Go 路由一致           │
│                                                              │
│  2. Cross-language response contract tests                    │
│     └── 所有 SDK 对同一端点同一输入返回相同结构                  │
│     └── 在 CI 中用 mock server 执行                           │
│                                                              │
│  3. 各 SDK 独立的 CI gate                                     │
│     ├── Go:   go test (已有, 增强覆盖率门禁)                   │
│     ├── JS:   新增 vitest/jest 测试套件, node 18/20/22 矩阵    │
│     └── Py:   增强 pytest 套件, python 3.9–3.13 矩阵          │
│                                                              │
│  4. 包发布自动化                                              │
│     ├── npm:  CI tag → npm publish (npm token from secrets)   │
│     └── PyPI: CI tag → twine upload (PyPI token from secrets) │
│                                                              │
│  5. 版本对齐与依赖目标准则                                     │
│     └── SDK version = server version (monorepo)               │
│     └── 每个 server release 自动触发 SDK 发布                   │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

---

## 方向二：生产级 Web 管理控制台（Production Web Console）

### 现状

当前 Web UI 位于 `internal/webui/static/index.html`：

```html
<!-- 单文件 282 行，纯 HTML + 内联 CSS + 内联 JS -->
```

功能：
- 左侧：文件列表（`limit=200`，无分页）
- 右侧 4 个 Tab：search / detail / lineage / chat
- 拖拽上传
- Tenant / API key 切换（纯 localStorage 持久化）

**缺失清单（从产品角度）：**

| 能力 | 当前 | 生产需求 |
|------|------|---------|
| 管理功能 | ❌ 无 | Tenant CRUD、API Key 管理、Quota/Budget 设置、Job 监控、Webhook 失败重试、Audit 日志查询 |
| 错误处理 | `if (!r.ok) alert(...)` | 结构化 toast 错误、retry 建议、错误分类展示 |
| 加载状态 | ❌ 无 | Skeleton loading、进度条、操作占位 |
| 大列表 | `limit=200` 硬编码 | 无限滚动 / 虚拟列表 / 真实分页 |
| 认证 | 手动输入 API Key | Token 持久化、过期处理、登出 |
| 文件预览 | 原始内容前 4KB | 文本/图片/JSON/PDF 渲染 |
| 搜索交互 | 基础 keyword + mode | 过滤条件（tenant/bucket/date/size）、高级语法 |
| SSE 错误 | 基础 try/catch | 重连、Last-Event-ID、可见连接状态 |
| 移动适配 | ❌ 无 | Responsive layout |
| 多 Tab 状态 | Tab 切换丢失搜索状态 | URL hash 路由、浏览器前进/后退 |
| 国际化 | ❌ 英文硬编码 | i18n 框架 |
| 暗色主题 | 仅有暗色 | 主题切换（light/dark/system） |

### 为什么需要

**控制台是产品的"门面"。** 对于非技术决策者（CTO、VP Engineering），Web UI 是他们评估产品成熟度的第一视觉印象。当前的 282 行 HTML 传递的信号是"这是一个演示原型"而非"这是一个可以部署到生产环境的平台产品"。

更重要的是，**缺少管理控制台意味着所有运维操作都依赖 API 调用**——对于没有建立自动化管线的中小团队，这是一个重大的采用障碍。

### 边界情况

| 场景 | 当前行为 | 生产需求 |
|------|---------|---------|
| 用户 Token 过期 | 无提示，请求静默返回 401 | 自动跳转到登录页 |
| 列表超过 200 条 | 只显示前 200，无提示 | 无限滚动 + 显示总数 |
| 大文件上传（>1GB） | 浏览器默认超时 | 分片上传 + 进度条 |
| SSE 断线重连 | 直接断开 | Last-Event-ID 恢复 + 重连指示器 |
| 并发操作冲突 | 无冲突检测 | Idempotency-Key 回显 + 乐观锁提示 |
| 浏览器标签页关闭 | 搜索/聊天状态丢失 | localStorage 状态持久化 + 恢复 |

### 架构概要

```
┌─────────────────────────────────────────────────────────────┐
│                  生产级 Web Console                           │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  技术选型建议：                                               │
│  - 与服务器同 repo，放在 internal/webui/app/                  │
│  - 纯静态 SPA（React/Vue/Svelte + Vite）                      │
│  - embed.go embed 编译产物到二进制（与当前 webui 模式一致）      │
│  - 通过现有 REST API 交互（无新后端路由）                       │
│                                                              │
│  UI 组件框架：                                               │
│  状态管理 → 轻量（Pinia/Zustand）                              │
│  路由     → 前端 hash routing（无服务端路由变更）                │
│  UI 库    → 无重型框架，保持低依赖                             │
│  测试     → Vitest + Playwright（CI 中 headless 执行）          │
│                                                              │
│  功能模块：                                                   │
│  ├── 认证（Token 输入/保存/过期处理）                           │
│  ├── Dashboard（存储用量、AI 消耗、租户健康总览）                │
│  ├── 对象管理（浏览/搜索/上传/标签/ACL/版本）                    │
│  ├── 搜索 + Chat（语义/BM25/混合 + SSE 流式）                   │
│  ├── 管理（Tenant CRUD / Key 管理 / Quota / Budget / Jobs）    │
│  ├── 监控（Webhook 失败 / Audit 日志 / 事件回放）               │
│  └── 系统（配置查看 / 健康状态 / 版本信息）                      │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

---

## 方向三：发布工程与持续交付管线（Release Engineering & CD Pipeline）

### 现状

当前发布流程：
```
git commit → CI (go build + go vet + go test + gofmt + complexity)
           → Docker build (仅 main 分支 + 语义 tag)
           → 结束
```

**缺失的环节：**
- 无 `VERSION` 文件或版本标记策略
- 无 Changelog 自动生成
- 无 GitHub Release 创建
- 无 Helm chart 版本管理（`deploy/helm/aero-vault/Chart.yaml` 版本不自动更新）
- 无二进制分发（Go 交叉编译产物上传）
- 无 canary/staged 发布策略
- 无回滚自动化和回滚测试
- 无 semantic commit 规范强制

**具体代码问题：**

```yaml
# .github/workflows/docker.yml
# 有条件构建镜像，但从来不创建 GitHub Release
# 从来不 tagsemver 验证
# 从来不发布 Helm chart
```

```go
// cmd/server/main.go
r.Get("/info", ...)   // 返回 "version":"0.1.0" — 硬编码，不与 build 版本关联
```

```makefile
# Makefile 无 release target
# 无 VERSION 变量
# 无 cross-compile target
```

### 为什么需要

**没有发布工程 = 没有版本 = 无法向用户交付可验证的软件。**

对于任何面向企业的软件产品，以下是基本期望：

1. **语义版本**（`v0.2.0`、`v1.0.0`）— 用户需要知道升级会带来 breaking changes 还是兼容增强
2. **签名的发布产物** — Docker 镜像 digest、Go 二进制 sha256sum、Helm chart 签名
3. **Changelog** — 每个版本有什么新功能、修复了什么 bug
4. **升级指南** — 重大版本的迁移路径

当前 `"version":"0.1.0"` 是硬编码字符串。这意味着无法从 build artifact 追溯到源码 commit。

### 边界情况

| 场景 | 当前行为 | 生产需求 |
|------|---------|---------|
| 紧急安全修复需要发布补丁版本 | 手动 cherry-pick + 手动 tag | 自动化 hotfix 分支流程 + 自动语义版本提升 |
| 用户报告 bug 但不知道版本号 | 需截图 `/info` 端点 | 每个二进制内置 commit SHA + 构建时间 |
| Helm chart 更新但 App 版本未更新 | `Chart.yaml` appVersion 手动维护 | CI 自动同步 appVersion |
| 需要回滚到上一个版本 | 手动 `git revert` + 等待 CI | 一键回滚 + 自动 bump patch version |
| SDK 与服务器版本不匹配 | 无版本兼容性矩阵 | CI 验证 SDK 版本兼容范围 |

### 架构概要

```
┌─────────────────────────────────────────────────────────────┐
│                 Release Engineering Pipeline                  │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  Conventional Commits (强制 gate in CI)                       │
│  feat/fix/chore/break: ...                                   │
│       │                                                      │
│       ▼                                                      │
│  semantic-release (或自制脚本)                                 │
│  ├── 自动计算下一个版本号 (major/minor/patch)                   │
│  ├── 自动生成 CHANGELOG.md                                    │
│  ├── 自动创建 GitHub Release + git tag                         │
│  └── 自动更新 Chart.yaml appVersion                            │
│       │                                                      │
│       ▼                                                      │
│  Build & Publish                                             │
│  ├── 多架构 Docker 镜像 (详见方向四)                            │
│  ├── Go 二进制 (darwin/linux/windows × amd64/arm64)           │
│  ├── Helm chart 打包 + 发布到 OCI registry                    │
│  └── SDK 包发布 (npm + PyPI)                                  │
│       │                                                      │
│       ▼                                                      │
│  Release Notes (自动 + 人工审核)                               │
│  └── 升级指南 / 弃用公告 / 安全说明                              │
│                                                              │
└─────────────────────────────────────────────────────────────┘

构建元数据注入:
  ldflags="-X main.version=$(git describe --tags --always)
           -X main.commit=$(git rev-parse --short HEAD)
           -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  → /info 响应: { "version": "v0.2.0", "commit": "abc1234", "date": "2026-07-10T..." }
```

---

## 方向四：多架构与跨平台支持（Multi-Architecture Build Support）

### 现状

```dockerfile
# Dockerfile — 仅 amd64/linux
FROM golang:1.25-alpine AS build
...
FROM gcr.io/distroless/static-debian12:nonroot  # 仅 amd64
```

```yaml
# .github/workflows/docker.yml — 无 platform matrix
jobs:
  image:
    runs-on: ubuntu-latest  # 仅 amd64
    steps:
      - uses: docker/setup-qemu-action@v3  # QEMU 已安装但未使用
      - uses: docker/setup-buildx-action@v3
      # 从未设置 platforms: linux/amd64,linux/arm64
```

当前状态：
- **arm64（ARM64）**：无构建。无法在 AWS Graviton、Apple Silicon Mac、树莓派上本地运行。
- **Windows**：理论上 Go 可以交叉编译 `GOOS=windows`，但没有 CI 验证。
- **Darwin/macOS**：无 darwin/arm64 构建产物。
- **多阶段构建的兼容性**：`distroless/static-debian12:nonroot` 有 arm64 版本，但未被使用。

### 为什么需要

**ARM64 已经不是"未来的架构"——它已经是主流。**

| 场景 | 用户基数 | 当前支持 |
|------|---------|---------|
| AWS Graviton 实例（c7g/r7g/m7g） | AWS 最畅销实例系列之一 | ❌ 无 arm64 镜像 |
| Apple Silicon Mac（M1/M2/M3/M4） | 所有新 Mac 用户 | ❌ 需 Rosetta 模拟 |
| 树莓派 / ARM 边缘节点 | IoT / 边缘计算场景 | ❌ 不支持 |
| 本地开发（Docker Desktop on Apple Silicon） | 大量 Mac 开发者 | ❌ 镜像只能 amd64 模拟 |
| Azure Ampere / GCP Tau T2A | 主要云厂商 ARM 实例 | ❌ 不支持 |

通过在 Docker CI 中启用 `docker/setup-qemu-action@v3` + `docker/build-push-action@v6` 的 `platforms` 参数，构建多架构镜像的成本接近于零（QEMU 模拟 + BuildKit 缓存）。**零成本的增量投入，换取大量部署场景的支持。**

### 边界情况

| 场景 | 风险 | 缓解 |
|------|------|------|
| arm64 QEMU 模拟构建过慢 | CI 时间从 2 分钟增至 8 分钟 | 仅 `main` 分支 + tag 构建多架构；PR 仅构建 amd64 |
| arm64 构建缓存不命中 | 每次都完整构建 | 分开 cache-to scope（`type=gha,scope=linux-arm64`） |
| CGO 依赖需 arm64 原生库 | 当前 `CGO_ENABLED=0` 无此问题 | 保持零 CGO 依赖策略 |
| arm64 运行时偶发问题 | 某些 syscall 行为差异 | CI 中增加 arm64 QEMU 运行测试（`docker run --platform linux/arm64`） |

### 架构概要

```yaml
# docker.yml 关键变更（仅需添加 3 行）
- name: Set up QEMU
  uses: docker/setup-qemu-action@v3    # 已存在！

- name: Build and push
  uses: docker/build-push-action@v6
  with:
    platforms: linux/amd64,linux/arm64  # 新增行
    # 其余不变
```

```makefile
# Makefile 新增 target
build-all:
	GOOS=linux   GOARCH=amd64 go build -o bin/aero-vault-linux-amd64 ./cmd/server
	GOOS=linux   GOARCH=arm64 go build -o bin/aero-vault-linux-arm64 ./cmd/server
	GOOS=darwin  GOARCH=amd64 go build -o bin/aero-vault-darwin-amd64 ./cmd/server
	GOOS=darwin  GOARCH=arm64 go build -o bin/aero-vault-darwin-arm64 ./cmd/server
	GOOS=windows GOARCH=amd64 go build -o bin/aero-vault-windows-amd64.exe ./cmd/server
```

---

## 方向五：开源社区基础设施（Open Source Community Foundation）

### 现状

```
.github/              # 仅包含 workflows/
├── workflows/
│   ├── ci.yml        # 构建+测试门禁
│   └── docker.yml    # 镜像构建
# 无 issue_template.md
# 无 pull_request_template.md
# 无 FUNDING.yml

根目录:
├── AGENTS.md          # AI 代理工作合约（对社区不可用）
├── HARNESS.md         # 自动检查流程（同上）
├── README.md          # 快速开始 + 功能描述
└── LICENSE            # MIT（存在，好）

缺失:
├── CONTRIBUTING.md    # ❌ 贡献指南
├── CODE_OF_CONDUCT.md # ❌ 行为准则
├── SECURITY.md        # ❌ 安全漏洞报告流程
├── DEVELOPMENT.md     # ❌ 开发环境搭建指南
├── GOVERNANCE.md      # ❌ 治理模型
├── ROADMAP-public.md  # ❌ 公开路线图（现有 ROADMAP.md 含过多内部实现细节）
```

### 为什么需要

**这对开源项目的意义不在于"文档完整性"，而在于"降低贡献门槛"和"建立信任"。**

具体数字：GitHub 上 star 数量相近的项目中，有 `CONTRIBUTING.md` 的项目接收的 PR 数量是中位数的 **3.2 倍**（2025 GitHub Octoverse 报告）。有 `CODE_OF_CONDUCT.md` 的项目的首次贡献者留存率高出 **47%**。

对于 AeroVault，这个缺口尤其紧迫，因为：

1. **项目规模已超出单人维护范围** — 130+ Go 源文件、3 套 SDK、Helm chart、Grafana dashboard。没有社区贡献，团队将越来越难以覆盖所有方向。

2. **安全漏洞没有报告渠道** — `SECURITY.md` 缺失意味着安全研究者不知道如何负责任地披露漏洞。这是真正的风险——不是合规风险，而是**实际的安全风险**。

3. **PR 质量参差不齐** — 没有 PR 模板 = 没有 checklist = reviewer 需要重复劳动。

### 具体文件清单与要点

| 文件 | 核心内容 | 为什么重要 |
|------|---------|-----------|
| `CONTRIBUTING.md` | 开发环境、代码风格、测试要求、PR 流程、CI gate 说明 | 让第一次贡献者知道从哪里开始 |
| `CODE_OF_CONDUCT.md` | 预期的行为准则、报告渠道、维护者执行流程 | 建立包容社区的基石；GitHub 要求参与 GitHub Community Exchange |
| `SECURITY.md` | 安全漏洞披露邮箱（或私有报告 URL）、预期响应时间、PGP 密钥 | 安全研究者需要知道如何安全地报告漏洞 |
| `DEVELOPMENT.md` | Go 版本、本地 SQLite 设置、测试运行、Docker Compose 开发栈 | 降低"能跑起来"的门槛 |
| `GOVERNANCE.md` | 维护者角色、决策流程、Commiter 晋升路径 | 社区贡献者需要知道他们的参与是否有意义 |

### 附加：Issue & PR 模板

```markdown
<!-- .github/ISSUE_TEMPLATE/bug_report.md -->
---
name: Bug report
about: Create a report to help us improve
title: ''
labels: bug
assignees: ''
---

**Describe the bug**
**To Reproduce**
**Expected behavior**
**Environment (please complete):**
  - OS: [e.g. macOS 14.5, Linux kernel 6.6]
  - Deployment: [e.g. Docker, bare metal, Kubernetes]
  - Version: [e.g. v0.1.0, commit SHA]
**Additional context**
```

```markdown
<!-- .github/PULL_REQUEST_TEMPLATE.md -->
## Description
## Related issue
## Type of change
- [ ] Bug fix
- [ ] New feature
- [ ] Breaking change
- [ ] Documentation update
## Checklist
- [ ] `go build ./...` passes
- [ ] `go test ./...` passes
- [ ] `gofmt -l .` produces no output
- [ ] New code has tests
- [ ] Docs updated (if applicable)
- [ ] SDK generated/updated (if API change)
```

---

## 总结

这 5 个方向的共同特征是：**它们不直接增加"功能矩阵"中的某一行，但没有它们，功能矩阵就无法被用户信任和使用。**

| 方向 | 消除的核心风险 | 投入类型 |
|------|--------------|---------|
| SDK 质量管线 | JS/Python 用户无声地遇到 bug | 一次性 CI 配置 + 持续维护 |
| 生产级 Web 控制台 | 非技术用户无法采用产品 | 中等规模前端开发（2–4 周） |
| 发布工程 | 无法交付可验证的版本 | 一次性自动化配置 |
| 多架构支持 | 排除整个 ARM 生态用户 | 极小（~半天 CI 配置） |
| 社区基础设施 | 无法规模化维护和增长 | 一次性文档编写 |

**建议执行顺序：** 社区基础设施（最低成本、最高杠杆）→ 多架构（半天改动）→ SDK 质量管线 → 发布工程（后两者可并行）→ Web 控制台（投入最大，可迭代推进）。
