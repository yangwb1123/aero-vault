# 对象读取错误与 HEAD 边界 v1

## 证据与范围

复查无歧义读取后，在 SQLite/local + FileService + httptest 中复现三项问题：

1. 当前对象 GET 带条件或 Range 时，第一次 Stat 的显式拒绝/provider 故障被忽略；测试 provider 第二次允许后，响应为 200、返回完整文件字节（两次授权、一次 Blob 打开）。旧路径与 `representation=content` 均受影响。
2. 无 selector、真实 Key 以 `/thumbnail` 结尾时，HEAD 通过 GET 分支打开 Blob；当前与历史版本均能复现。
3. 显式 content 的历史 HEAD 304 缺失 `X-Version-Id`；旧 thumbnail 精确对象 HEAD 304 虽有 ID，却仍打开 Blob。

本轮只补 REST 读适配器及回归，不新增功能、路径、配置、依赖、权限模型、存储/仓储接口或迁移。保留此前未提交工作区，不提交或部署。

## 契约

- 条件/Range 当前 GET 的 Stat 错误是当次请求终态，使用既有错误分类；拒绝/故障后不再 Get、不打开 Blob、不自动重试、不返回 200/206/304。
- 后续补强旧 thumbnail 精确对象分派：非匿名首次拒绝同样终止；匿名公读 capability 注入机会单独保留，见 [`legacy-thumbnail-read-denial-v1.spec.md`](legacy-thumbnail-read-denial-v1.spec.md)。
- 成功 Stat 不是可复用授权凭据。后续实际读取仍由 FileService 检查授权；第二次拒绝仍阻止打开 Blob。后续 HTTP Range 改为切分已打开流，不再重新定位当前对象，见 [`opened-content-response-v1.spec.md`](opened-content-response-v1.spec.md)。
- 旧 `/thumbnail` 分派命中精确对象时，HEAD 复用完整 Key 的元数据路径；固定版本、policy、anonymous 与授权/损坏检查保留。HEAD 忽略 Range，不读正文。
- 非精确对象的缩略图派生 HEAD 仍走既有身份/cache/admission/decode 链路，可能打开源图，不承诺所有 HEAD 都是纯元数据操作。纯元数据 HEAD 也不证明 Blob 物理存在、可解密或下一次 GET 能读取成功。
- 历史内容 HEAD 304 保持授权版本的 `X-Version-Id`、ETag、Last-Modified 与 Content-Encoding，不为这些头打开 Blob。
- 此修复不提供当前对象 Stat→Get 的原子快照/覆盖 CAS、不改变历史 pin、取消时点、GET 子资源或 PUT/DELETE 分派，也不扩大 SDK 或外部存储能力。

## 验证

`object_read_boundary_test.go` 使用可恢复/二次拒绝的 provider、atomic Blob 计数与真实 HTTP HEAD body 抑制，覆盖旧/显式路径、If-Match/If-None-Match/Range、首次 Deny/故障、二次拒绝、精确当前/历史 HEAD 和历史 304。

修复前 12 个首次失败子用例均返回文件字节，当前/历史旧 HEAD 各打开一次 Blob，显式历史 HEAD 304 缺少 ID；第二次拒绝的正向防回归用例已通过。失败日志保留。

修复后运行定向普通与 race、完整 `make check` + `python3 cli.py accept`，以及前端交付顺序 `make web-check` → `make build` → `make web-smoke`。不改既有预算、阈值或跳过门禁；浏览器冒烟只证明登录壳/资源加载，真实外部存储、并发覆盖、整仓 race 与阻塞 Read 中断不据此宣称通过。

## 本轮实际验证

- 新增 7 项 HTTP 回归（含 12 个首次拒绝/故障子用例）；补充正常 Stat 后 Get/GetRange 二次拒绝、HEAD 忽略 Range、gzip 历史 304 响应头与恢复后的显式新请求。均通过；首次失败日志仍保留。
- REST/service 定向普通测试通过（16.024 秒、0.459 秒）；同范围定向 race 通过（52.844 秒、2.338 秒），包预算 300 秒，覆盖本轮边界及原条件请求、版本 pin、thumbnail HEAD/精确对象、policy/租户/读取授权/取消回归。
- 使用 `GOMAXPROCS=4 GOFLAGS=-p=1` 执行 `make check`、`python3 cli.py accept`，均 exit 0。完整普通测试与既有 metadata、thumbnail short、REST cache race 均执行；未改包部分正常使用缓存，未另跑整仓 `make test-race`。
- 依序 `make web-check` → `make build` → `make web-smoke` 通过：60 个前端文件/251 项测试，额外 4 项 Node 回归；默认 Chrome/Node 验证两条登录壳路径、9 个成功响应及 2 项 bundle/style。嵌入产物仍为 `index-Z_F4LqoO.js`，没有新的前端源码功能。
- 本轮涉及 Go 文件的函数最大 44 行、复杂度 10；文件大小、I1–I6、`git diff --check` 通过。禁止路径、配置、依赖、门禁预算未改；未提交、推送、部署或外部实库联测。
