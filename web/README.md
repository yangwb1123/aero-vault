# Aero Vault Web

`web/` 是 Aero Vault 的 Iris UI React 前端源码。生产构建输出到
`internal/webui/static/app/`，由 Go 二进制嵌入并在 `/ui/` 提供；`/ui/app/` 是兼容别名，
依赖无关的旧控制台保留在 `/ui/legacy/` 作为回退入口。

当前 Iris UI 应用已接入 Snaplink 登录、服务概览、拖拽批量上传、目录导航、对象批量标签/删除、文件与回收站、受限内容预览、
持久 SSE 对象动态与断线回放、存储桶创建/版本控制/对象锁/生命周期/ACL/Policy/CORS/SSE、对象标签/元数据/版本/Legal Hold、对象分享、公开图片、资源 ACL、部门与 aero-id 成员映射、
可移植备份、operator 任务/租户/Webhook 运维，以及 AI 搜索、SSE 流式对话、Agent 工具轨迹和对象使用血缘查询。
AI 与企业访问页面遵循后端 opt-in 开关；能力未启用时会直接展示 API 返回的降级或不可用信息。

## 文件视图

工作台提供“可访问文件 / 我的文件”切换；目录、分页和回收站沿用同一视图。
“我的文件”由后端按当前 user/service 身份的可信归属与读取权限交集筛选，不是私有空间，
也不改变上传或分享权限。匿名身份无法使用该视图，没有归属的历史文件只保留在既有可访问列表中。
该视图隐藏递归删除目录，避免将同一前缀下其他人的文件纳入操作；单对象操作仍由后端授权。
目录支持独立“加载更多目录”，跨页合并重复目录，不把第一页没有子目录误判为完整空目录；
续页失败保留已加载内容，切换视图或刷新后丢弃旧请求结果。
详细契约见 [`owned-file-view-v1.spec.md`](../docs/requirements/owned-file-view-v1.spec.md) 与
[`folder-pagination-v1.spec.md`](../docs/requirements/folder-pagination-v1.spec.md)。

## 上传队列取消

上传面板展示已收到成功响应的文件数量，并可“取消上传”以中止当前客户端请求、停止发送后续文件。
停止期间保持队列锁定；停止后移除确认成功项，保留未确认/未发送项，并刷新列表供用户核对。
取消不等于服务端回滚，已发送对象可能已保存。显式重试沿用同一 File/Key 的幂等键，但处理中记录可能返回 409，
不承诺取消后的 exactly-once；也不提供字节级进度、刷新后续传或新的后台上传任务。
切换身份/初始目录或卸载会隔离旧队列回调。详细契约见
[`upload-queue-cancel-v1.spec.md`](../docs/requirements/upload-queue-cancel-v1.spec.md)。

## 文件下载准备与取消

普通下载与历史版本下载复用同一请求隔离组件，开始时固定完整 Key/Version ID，并同步防重复点击。
两者使用 `representation=content` 明确读取完整 Key；`tags`、`metadata` 等后缀不再被猜作子资源。
工作台可“取消下载准备”；请求尚未结束时保持锁定，即使 transport 忽略 abort，取消后的迟到内容也不会交给浏览器。
切换目录、all/mine、回收站或身份，以及卸载时会隔离旧下载；普通下载不再自动刷新文件列表。
成功只提示文件已交给浏览器处理，不证明已保存到本地；交付后不能撤回，取消也不保证服务端未读取或记录访问。
依然使用 Blob，不承诺大文件端到端有界内存或刷新续传。详细契约见
[`object-download-scope-v1.spec.md`](../docs/requirements/object-download-scope-v1.spec.md)。

## 回收站批量恢复

回收站可勾选已加载文件并“恢复所选”，确认后逐项串行调用既有恢复接口。
成功项取消选择，失败项保持选中并显示原因；切换目录、视图或身份后不再发送剩余请求。
已发送请求可能仍在后端执行，切换页面不等于回滚。恢复不覆盖已有同名文件，不复活任意历史版本，
也不绕过权限、租户状态或配额。详细语义见
[`recycle-batch-restore-v1.spec.md`](../docs/requirements/recycle-batch-restore-v1.spec.md)。

## 文件内分享

活动文件行可直接打开固定目标的分享面板，对象详情也提供“对象分享”页签；回收站不提供入口。
创建、列出和撤销复用既有分享 API，读取权限不等于分享权限；能力不可用或被拒绝时显示后端错误。
可复制链接只在本次创建后保存在组件内存，关闭、切换目标/身份或刷新文件列表后清除；列表不能找回 token。
已发送请求仍可能执行，关闭面板不等于撤销；撤销本次新建链接后清除复制入口。
详细契约见 [`object-share-workbench-v1.spec.md`](../docs/requirements/object-share-workbench-v1.spec.md)。

## 对象直接权限规则

活动对象详情提供“权限规则”页签，固定完整 Key 与 object 类型，打开时读取直接 ACL，
并复用已有接口添加/更新和删除规则。此列表不含完整的上级继承或有效权限；空列表不代表公开或无访问限制。
读取文件不等于管理 ACL，拒绝规则需确认其优先级及自锁风险，删除规则也可能扩大访问权限。
多 action 沿用逐项写入语义，失败或刷新失败后可能已有变更，应重新读取确认而非自动回滚/重试。
切换对象、身份或页签会隔离旧响应，但已发送请求仍可能执行。回收站不提供该页签。
详细契约见 [`object-acl-workbench-v1.spec.md`](../docs/requirements/object-acl-workbench-v1.spec.md)。

## 历史版本预览

对象详情的“对象版本”页签可选择具体 Version ID 预览文本或 JPEG 缩略图，关闭预览及刷新列表。
内容和缩略图均固定到同一历史版本；失败不回退到最新对象，也不恢复或覆盖当前文件。
首读用 `representation=content`，JPEG 派生用同一 Key 的 `representation=thumbnail`，不读取同名相邻对象。
普通对象预览也使用这两种显式表示；旧子资源路由、上传/删除分派及 SDK 未改。
真正删除标记不提供读取入口，普通软删除版本与锁定版本仍由后端按既有授权处理。
下载同步防重入，切换对象/身份或退出页签后，迟到 Blob 不会触发浏览器下载。
取消后的 JPEG 首读不会再启动缩略图请求；未消费的迟到响应体尽力释放。
历史 Range 的前缀跳过和完整版本读取在每次 Read 前后检查取消，但不能中断已阻塞的底层 Read。
文本范围响应不等于所有存储/加密链路都具有有界内存；已交给浏览器的文件无法靠关闭页签撤回。
详细契约见 [`object-version-preview-v1.spec.md`](../docs/requirements/object-version-preview-v1.spec.md) 与
[`read-cancellation-boundaries-v1.spec.md`](../docs/requirements/read-cancellation-boundaries-v1.spec.md)；
无歧义读取见 [`object-content-selection-v1.spec.md`](../docs/requirements/object-content-selection-v1.spec.md)。

## 安装与运行

应用只消费 npm Registry 已发布的 `@iris-ui-kit/react@0.2.2` 与
`@iris-ui-kit/plugin-locale-zh@0.1.0`，不依赖相邻源码仓库或 pnpm workspace link。
`@iris-ui-kit/react@0.2.3` 的 registry 元数据仍含 `workspace:*`，独立消费端无法安装；
Iris UI 发布修复版本后再通过正常依赖升级接入。

首次运行：

```bash
cd web
pnpm install
pnpm test
pnpm build
pnpm dev
```

开发服务器代理 `/v1` 和 `/auth` 到 `http://127.0.0.1:8080`。

## 登录与安全边界

- 登录入口调用 `/auth/oidc/login`，Authorization Code + PKCE、state、回调交换继续由
  Aero Vault 后端复用 Snaplink SDK 完成。
- access token 只存在于当前 React 页面内存；不会写入 localStorage、sessionStorage 或
  runtime-config.js。
- 页面跳转到 aero-id、aero-im、audit-governance 时不转发 Aero Vault token，各服务必须
  使用自己的 Snaplink audience。
- `allowAnonymous` 只显示本地开发入口，不会改变后端鉴权结果。生产应在
  `runtime-config.js` 中将其设为 `false`。

运行配置通过 `/ui/app/runtime-config.js` 的 `window.__AERO_VAULT_WEB_CONFIG__` 注入，字段见
`public/runtime-config.js` 与 `.env.example`。

## 交付前浏览器冒烟

`pnpm build` 只保证类型与打包通过；真正会炸的是“构建成功但页面白屏”类回归
（asset hash 漂移、base path 改错、bundle 运行时异常）。因此推送前还有一道
fail-closed 门禁：

```bash
make build       # 用刚构建的 UI 重新编译二进制（embed）
make web-smoke   # 起真实二进制 → 静态 + 无头 Chrome 冒烟
```

`scripts/web-local-smoke.sh` 在临时目录里以 `env -i` 启动二进制（SQLite + local FS、
无鉴权、AI 关闭、随机端口），先校验 `/ui/`、`/ui/app/`、`/ui/legacy/`、
`/favicon.ico`、`runtime-config.js` 与 index.html 引用的每个 assets 均 2xx，再用
`scripts/web-smoke.mjs` 通过 Chrome DevTools Protocol 加载 `/ui/` 与 `/ui/app/`：
控制台零 error、零未捕获异常、零失败请求、React 已挂载进 `#root`、
`__AERO_VAULT_WEB_CONFIG__.apiBase === '/v1'`、样式已生效。

依赖 node ≥ 22（内置 `WebSocket`，不新增 npm 依赖）与 Chrome/Chromium
（`AERO_SMOKE_CHROME` 可覆盖，缺失即 fail-closed）。测试浏览器使用无凭据、用后删除的独立 profile，
以 `--password-store=basic` 避免桌面密钥环初始化阻塞首次请求；不改应用 token 或用户浏览器设置。
导航/加载事件联合等待，超时仍失败并清理；原生 Node 回归随 `make web-test` 运行。
详见 `HARNESS.md` §4.1。
