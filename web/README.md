# Aero Vault Web

`web/` 是 Aero Vault 的 Iris UI React 前端源码。生产构建输出到
`internal/webui/static/app/`，由 Go 二进制嵌入并在 `/ui/app/` 提供。达到旧版能力齐平前，
正式入口 `/ui/` 保持不变，`/ui/legacy/` 是它的显式别名。

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
