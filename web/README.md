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
（`AERO_SMOKE_CHROME` 可覆盖，缺失即 fail-closed）。详见 `HARNESS.md` §4.1。
