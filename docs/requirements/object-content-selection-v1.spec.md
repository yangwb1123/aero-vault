# 对象内容无歧义读取 v1

## 问题与范围

合法 Key 可含 `tags`、`versions`、`acl`、`metadata`、`thumbnail` 后缀。当前普通 GET 按路径后缀分派，工作台下载/预览可能读取另一个对象的子资源，或返回 JSON/404 而非该 Key 的字节。JPEG 后续 `/thumbnail` 请求也可能被同名真实对象遮蔽。历史非空 pin 解决了部分后缀，但并未消除 thumbnail 的派生歧义。

本轮只修复 REST GET/HEAD 与工作台内容/缩略图读取。普通写入、删除的旧子资源分派和 SDK 迁移不在范围；不新增业务模型、REST 路径、数据库、服务、配置或依赖。

## 方案选择

- 不把“真实对象存在”改成所有旧子资源的默认优先级：这会破坏同时存在 `file` 与 `file/tags` 时对 file 的 tags 管理，并引入授权失败后的探测/回退。
- 不使用空 `version` 暗示内容读取，也不自动固定普通下载到某历史 ID：这混淆版本选择与资源选择，无法表达明确的缩略图派生。
- 增加同一路径的显式 `representation=content|thumbnail`。省略时保留旧路由；客户端选择明确表示自己读取完整 Key 的字节还是该 Key 的派生图，不再根据后缀猜测。

## 契约

- GET/HEAD `/v1/files/{key}?representation=content` 使用完整不透明 Key，直接复用 Get/Stat 及 FileService 授权。不存在、删除、损坏、Deny 或 provider 故障均不回退到去掉后缀的对象/子资源。
- `representation=thumbnail` 从完整 Key 派生 JPEG，复用既有 thumbnail 的 policy → anonymous → deadline → authorization/identity/cache/admission/decode 链路；不查找或返回相邻 `{key}/thumbnail` 对象。
- 两种表示都接受既有 `version=ID`。非空 pin 固定精确版本，错误 ID 不回退；空值保持当前对象语义。HEAD 的版本选择与 GET 一致，历史 HEAD 不打开 Blob。Range、If-Range、304/412/416 和取消边界保持既有语义。
- representation 仅接受单个非空 `content` 或 `thumbnail`；其他值、重复参数及无法解析的读取 query 返回 400，不默默选择另一个资源。它不是 Key 校验或权限旁路。
- 缺省 GET 子资源与 `/thumbnail` 的精确对象优先/派生规则保持兼容。运行时 OpenAPI 同步登记 GET/HEAD 的查询参数及响应，内容流按 binary 而非文件元数据 JSON DTO 登记。
- local FS 的同时存在父 Key 与子 Key fixture 使用版本化 Blob 避免文件/目录物理冲突，不把这些测试当作非版本化 local FS 支持该组合的证明。
- 已通过 Auth 验证、上下文绑定的预签名 GET/HEAD capability 直接读取签名完整对象路径；不根据未验证 URL 形状授权，不放宽签名参数白名单或修改签名格式。
- Web 普通/历史下载、首读预览用 content，JPEG 后续用 thumbnail；固定完整 Key/Version ID，保留 Range、凭据、AbortSignal、无 fallback、无自动重试及上下文隔离。
- 后续条件 GET 错误终态、旧精确对象 HEAD 无 Blob 与历史 HEAD 304 身份补强见 [`object-read-error-boundaries-v1.spec.md`](object-read-error-boundaries-v1.spec.md)；派生缩略图 HEAD 仍可能读取源图。

## 验证

先复现旧客户端读取后缀 Key 得到子资源 JSON，以及 JPEG 派生被同名对象遮蔽。SQLite/local + FileService fixture 验证各后缀、存在与不存在父对象、默认子资源兼容、明确派生、精确版本/HEAD/条件请求、404/410、不回退、Deny/停用/租户/provider/policy 故障及预签名完整路径/防篡改。

执行前端 `make web-check` → `make build` → `make web-smoke`；后端普通与定向 race（含旧 thumbnail 分派/缓存/身份授权）、`make check` + `python3 cli.py accept`。禁止项和既有门禁范围/阈值不变，保留当前工作区，不提交或部署。浏览器冒烟不代替登录后功能 E2E，外部存储、原生直链、大文件、RSS 和跨实例仍须专项验证。

## 交付门禁排障补充

静态 HTTP 与前端测试通过后，原浏览器冒烟多次在首次导航等待 `Page.loadEventFired` 超时；单独执行和已有 Chrome 148 对照也失败。临时 netlog 显示首个 HTTP 事务发送前出现约 20 秒停顿。页签激活/就绪检查未能稳定解决，相关尝试已撤回。

在原脚本临时副本中仅给全新、无凭据、用后删除的 Chrome profile 增加 `--password-store=basic`，原有两条 UI 路径及全部检查连续三次冷启动通过（每次 3–4 秒）。因此仅在测试浏览器隔离桌面密钥环，不修改应用 token、服务端鉴权或用户浏览器设置。

同时修复原脚本先等待导航、后等待 load promise 的未处理 rejection：联合观察两者，超时仍报失败并进入既有清理。新增原生 Node 回归纳入 `make web-test`，覆盖订阅顺序、两者均完成、导航停滞/load 失败及异步/同步发送失败；不增加重试，不忽略加载事件，不改页面预算、mount、样式、console、异常或失败请求判定。此前失败日志保留，正式交付仍以修改后原门禁完整执行为准。

## 本轮实际验证

- `make web-check`：60 个前端测试文件、251 项测试及类型/构建检查通过；额外 4 项 Node 冒烟等待回归通过。
- 依序执行 `make web-check` → `make build` → `make web-smoke` 通过。正式冒烟使用默认 Chrome 154.0.8037.97、Node v26.11.1；两条 UI 路径均为登录壳，9 个成功响应与 2 项 bundle/style。嵌入 JS 为 `index-Z_F4LqoO.js`，不表示登录后功能 E2E。
- SQLite/local 的内容选择、条件请求、历史 pin、HEAD 无 Blob、授权/租户/policy/provider/capability 及旧 thumbnail 回归通过；本轮 REST/service 定向 race（300 秒包预算）通过，分别 194.187 秒、6.535 秒。
- `GOMAXPROCS=4 GOFLAGS=-p=1 make check` 与同配置 `python3 cli.py accept` 通过。完整普通测试及既有 metadata、thumbnail short、REST thumbnail cache race 均执行，未改包部分使用正常缓存；未另跑整仓 `make test-race`、真实 PostgreSQL/Qdrant 或外部存储。
- 文件大小、I1–I6、`git diff --check` 通过；本轮新 Go 函数最大 30 行/复杂度 10，新增 TS/Node 文件函数最大 34 行/决策计数 4。禁止路径、依赖与门禁预算未改；未提交、推送或部署。
