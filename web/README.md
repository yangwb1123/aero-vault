# Aero Vault Web

`web/` 是 Aero Vault 的 Iris UI React 前端源码。生产构建输出到
`internal/webui/static/app/`，由 Go 二进制嵌入并在 `/ui/` 提供；`/ui/app/` 是兼容别名，
依赖无关的旧控制台保留在 `/ui/legacy/` 作为回退入口。

当前 Iris UI 应用已接入 Snaplink 登录、服务概览、拖拽批量上传、目录导航、对象批量标签/删除、文件与回收站、受限内容预览、
存储桶创建/版本控制/对象锁/生命周期、对象标签/元数据/版本/Legal Hold、对象分享、公开图片、资源 ACL、部门与 aero-id 成员映射、
可移植备份、operator 任务/租户/Webhook 运维，以及 AI 搜索、SSE 流式对话和对象使用血缘查询。
AI 与企业访问页面遵循后端 opt-in 开关；能力未启用时会直接展示 API 返回的降级或不可用信息。

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
