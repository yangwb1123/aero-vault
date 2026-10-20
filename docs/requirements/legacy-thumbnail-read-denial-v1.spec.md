# 旧 thumbnail 精确对象读取拒绝终态 v1

## 证据与范围

在 SQLite/local 版本化 Blob + 真实 API Key + `server.ApplyMiddleware` 12 环 + REST router 中复现：

- `/v1/files/{key}/thumbnail` 的完整 Key 实际存在时，初次精确对象 Stat/StatVersion 的 `ErrForbidden` 无条件转到 Get/HEAD，再次授权。
- 受控 provider 首次显式拒绝、第二次允许：当前/历史普通 GET 返回 200 文件字节，Range 返回 206，validator 返回 304；HEAD 返回 200/304 元数据。12 个子场景全部未保持首次拒绝终态，打开 Blob 的历史 304 亦能复现。
- 这不是实际读取绕过 FileService；问题是同一身份/请求中的首次拒绝被适配器吞掉。无 selector 的旧分派独有，显式 content/thumbnail 不经该精确对象判别。

仅改 REST 旧读取适配器的错误分派、回归和文档。不新增接口、功能、配置、依赖、权限模型或存储/仓储能力，保留既有工作区，未授权提交或部署。

## 契约

- 非匿名请求精确对象判别得到 Forbidden 时，该次请求立即按既有错误分类结束：不 Get/HEAD 重查、不返回内容验证器、不打开 Blob、不派生父 Key。
- 保留现有匿名公读机会：匿名请求的判别发生在 canned-public capability 注入前，可进入完整 Key 的 bucket policy + `allowAnonymous`，随后仍由 FileService 授权。该机会不扩为已认证请求的拒绝重试。
- 成功的精确对象判别仍不能替代后续 GET/HEAD 的实际授权；二次拒绝保持拒绝。provider 故障、损坏、SSE-C 等错误不变为对象缺失或父 Key 派生。
- 私有完整 Key 不因父 Key 公读而变为可读。普通/历史完整 Key、pin、条件/Range、HEAD 元数据-only 与旧真实 miss/key-length artifact 兼容不变。
- provider 恢复后，显式发送的独立新请求可正常读取；不自动重试同一请求。

## 验证计划

`legacy_thumbnail_denial_test.go` 使用真实认证全链、read authorization/Blob atomic 计数，覆盖首次拒绝 12 个场景、provider 故障、成功判别后的拒绝、匿名当前/历史公读、HEAD 无 Blob、私有完整 Key/公读父 Key 隔离及显式新请求恢复。使用真实 Manager 验证匿名公读，不以 provider 永远允许替代 capability 链路。

修复前日志 `/tmp/aero-legacy-thumbnail-denial-red.log` 保留；修复后运行定向普通/race 与旧 thumbnail/条件/完整 Key/租户/policy/pin/取消回归，以及 `make check`、`python3 cli.py accept` 和 `make web-check` → `make build` → `make web-smoke`。不改范围、阈值、超时或既有跳过规则，不把 UI 登录壳冒烟称为登录后读取 E2E。真实外部存储、分布式撤权、数据库/Blob 原子快照与阻塞 Read 中断不据此宣称通过。

## 本轮实际验证

- 新增 8 项 HTTP 回归通过，含 12 个首次 Deny 子用例、当前/历史 GET/HEAD/validator/Range、provider 故障、成功判别后再授权、真实 Manager 匿名公读和 Everyone 显式 Deny、私有完整 Key/公读父 Key 隔离、停用租户在读取判别前被阻止以及独立新请求恢复。初始首次拒绝和恢复测试失败日志保留。
- REST/service 定向普通测试通过（57.878 秒、0.607 秒）；同范围定向 race 通过（117.329 秒、2.543 秒），包预算 300 秒。包含上一轮已打开流响应、原条件/Range、精确对象/HEAD、版本 pin、完整 Key、预签名、policy/租户/授权与取消回归。
- `GOMAXPROCS=4 GOFLAGS=-p=1 make check` 和同配置 `python3 cli.py accept` 都 exit 0。完整普通测试及既有 metadata、thumbnail short、REST cache race 均执行；未改包部分正常使用缓存，未运行整仓 `make test-race` 或真实 PostgreSQL/Qdrant。
- 依序 `make web-check` → `make build` → `make web-smoke` 通过：60 个前端文件/251 项测试、4 项 Node 回归，两条 UI 登录壳、9 个成功响应及 2 项 bundle/style。嵌入 JS 仍为 `index-Z_F4LqoO.js`，无新增前端功能。
- 改动分派文件函数最大 27 行/复杂度 9，新测试文件最大 18 行/复杂度 8；文件大小、I1–I6、`git diff --check` 通过。禁止路径、依赖、配置和门禁预算未改；未提交、推送或部署。
