# REST 内容流失败终态 v1

## 已复现与范围

SQLite/local、真实版本化 Blob、API Key 与共享 12 环中间件下，当前/历史与旧精确 thumbnail Key/显式 content 共 16 个场景复现：

- 全文 `io.Copy` 忽略错误：读取同时返回数据与错误、正文后读到尾部错误，仍返回可完整消费的 200。
- 启用既有 FileService ReadVerification，存储读取注入坏字节后 verifier 实际返回摘要错误，适配器仍正常结束短小对象的 200。
- Range `io.CopyN` 在读取同时返回足量数据与非 EOF 错误时会规范化为成功，得到可完整消费的 206。
- Recoverer 把 `http.ErrAbortHandler` 当普通 panic 吞掉、记 stack 并向已写内容追加 `internal server error`，阻碍 stdlib 正确中止响应。
- 补充正反例时又复现 Range 前缀阶段原始 provider 错误字符串含后端凭据/主机并直接序列化为 JSON（4 个当前/历史、旧/显式路径场景）；修复只保留现有错误 code/status，采用固定安全 message，不改全局 classify 或 opener 的错误契约。日志 `/tmp/aero-content-stream-failure-focused.log` 保留。

红灯日志 `/tmp/aero-content-stream-failure-red.log`、`/tmp/aero-content-stream-recoverer-red.log` 保留。仅修 REST 原始内容的正文复制错误终态，以及全局 Recoverer 对 stdlib 精确中止哨兵的透明传递。不改中间件链环数/顺序、不挂新全局链、不扩展派生缩略图、分享/公开资源、S3/WebDAV/MCP 或 SDK 的流契约，不改 Service/Storage/Repository/AI 接口、迁移、配置、依赖或预算。

## 最小方案与边界

- FileService 仍唯一授权/opener；正文与 Range 继续切同一打开源，原 handler 拥有并关闭源一次，失败不重开、不回退对象/版本、不自动重试。
- `objectContentReader` 观察到非 EOF 的读取错误时，不交付该次 Read 同时返回的数据，避免 CopyN 把错误规范化为成功；正常 `n>0, io.EOF` 数据仍保留。不增加预读、整文件缓冲或额外源读取。
- 全文与 Range 正文复制都处理复制错误/不足长度；实际请求 context 已取消时保持原取消行为，否则用精确 `http.ErrAbortHandler` 中止响应，绝不追写 JSON/文本错误或尝试改变已提交状态。
- Recoverer 只重抛精确 `http.ErrAbortHandler`，保持 stdlib 的中止语义，不为该哨兵记普通 panic/stack。正常响应、真实应用 panic→500 与链顺序保持。
- 警告只记录固定失败类别与 request ID，不序列化底层错误、摘要、凭据、路径或主机信息；不新增指标/配置。普通 Stat/open 阶段不变；prefix 读取失败保留现有 code/status 但使用固定 `object content read failed` message，权限与元数据不泄漏边界不变。
- **非完整校验保证：** 缓冲较小的测试正文在 handler 正常返回前中止可以让客户端观察失败，但大正文若已把全部 Content-Length 字节交付给客户端，晚到 Read/校验错误无法保证客户端还能观察。已交付字节不可回收，当轮不承诺下载前校验、close-only 错误处理、阻塞 Read 中断、部分 Range 的全文校验或数据库/Blob 原子快照；后续 close-only 终态及单次关闭见 [`content-close-failure-v1.spec.md`](content-close-failure-v1.spec.md)，不抹去本轮边界与验证记录。不扩大可选 ReadVerification 默认激活范围，不自动标记/修复对象。访问事件仍是既有打开/读取尝试事实，不新增成功/失败下载审计承诺。

## 验证计划

真实 HTTP 检查失败不得正常完成、无追加错误文本、完整选中 StorageKey、单次打开/关闭与无自动请求重试；正例覆盖正文/Range、EOF 同读返回数据、HEAD/304/412、offset skip 前错误、写入失败、取消和二次显式恢复。Recoverer 检查中止透传/无写入/无 panic stack 与真实 panic 保留。

执行定向普通/race、`make check` + `python3 cli.py accept`，以及前端交付顺序 `make web-check` → `make build` → `make web-smoke`。HTTP/2 stream reset 与真实外部代理/身份/存储、实库、规模/RSS、全部客户端/大正文晚到失败识别另行验证；UI 冒烟仅登录壳与嵌入资产。

## 本轮实际验证

- 新增 11 项测试通过（6 项 HTTP、5 项隔离单测）。首次 16 个流场景、abort 被吞/追加文本及后续 4 个 prefix 诊断泄漏场景的红灯均保留。真实 FileService verifier 的损坏测试不是仅伪造同名错误；fixture 只扰动读取字节，不改数据库标记或生产 Blob。
- HTTP 覆盖当前/历史、旧精确 thumbnail Key/显式 content、GET全文/206、同读数据+错误、尾部错误/摘要失败、正常数据+EOF、HEAD/304/412/416 零 Read、前缀错误 500/脱敏、失败后独立新请求恢复。单次打开/关闭、StorageKey 精确匹配、请求不自动重放均验证。
- 隔离单测覆盖写入失败不重试/不追加、不记录原始错误、全文不足长度 abort，prefix 保留 410/504/403/500 分类但不返回 provider 诊断；精确 abort 哨兵透传且不记 stack，字符串、非 comparable 值和 wrapped abort 的普通 panic 保持 500。
- `content_response.go` 仅增加复制完成/失败终态与 prefix 安全错误输出；`content_range.go` 继续使用同一源并保留正常 EOF 数据；`middleware.go` 只加精确 stdlib abort 透传。API 文档与运行时 OpenAPI 同步，原取消、条件/Range、授权、匿名公读、预签名和租户回归保留。
- `GOMAXPROCS=4 GOFLAGS=-p=1`：REST/service/middleware 定向普通分别 35.967/1.091/0.009 秒，同范围 race 分别 140.889/4.356/1.040 秒，300 秒包预算不变。未运行整仓 `make test-race`。
- `make check` 与 `python3 cli.py accept` 均 exit 0；fmt/vet/integration 类型检查、构建、全仓普通单测及仓库既有 metadata、thumbnail short、REST cache 定向 race 通过，部分未改包正常使用缓存。未另跑真实 PostgreSQL/Qdrant 或外部联测。
- 前端按 `make web-check` → `make build` → `make web-smoke` 通过：60 文件/251 项前端、4 项 Node；两条 UI 登录壳、9 成功响应与 2 资产。JS 仍 `index-Z_F4LqoO.js`，无新前端功能或浏览器下载完成/保存承诺。
- 新复制终态函数最大 12 行/复杂度 5，range 文件函数最大 26 行/复杂度 7，新测试最大 23 行/复杂度 9；文件大小、I1–I6 与 `git diff --check` 通过。禁止接口/目录、配置、依赖和门禁预算无差异。未提交、推送或部署，保留全部既有工作区。
