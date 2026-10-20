# REST 原始内容 Close 失败终态 v1

## 已复现

在真实 API Key、共享 12 环中间件、SQLite/local 与版本化 Blob 下：

- 底层源成功读出正文，但 Close 返回错误，当前/历史、旧精确 thumbnail Key/显式 content 的全文和 Range 仍正常结束 200/206（8 场景）。
- 启用既有 FileService ReadVerification，注入坏读取字节；Range 恰好消费整个对象（含先跳过前缀的 suffix）时 CopyN 不触发后续 EOF Read，而 verifier 在 Close 根据已读完整长度返回 ErrObjectCorrupt，适配器丢弃错误（8 场景）。
- 新显式请求可恢复的用例同样首次错误 200。日志 `/tmp/aero-content-close-failure-red.log` 保留；metadata-only 不受 Close 失败替换与部分校验不额外读尾部的正例已通过。

仅修 `/v1/files` 当前/历史原始内容成功复制后的关闭结果，不改 FileService/Storage/Repository 接口、verifier 算法/开关、SSE、存储流程、迁移、AI、SDK、分享/公开资源、S3/WebDAV/MCP 或缩略图派生。不改配置、依赖、预算、中间件顺序或前端功能，保留工作区且不提交/部署。

## 最小方案与取舍

- 复用现有 request-local `objectContentReader`，增加缓存 Close 结果；本请求仍单 owner、同一个 FileService 授权并打开的流。Get/GetSpecificVersion 把原来的直接 defer Close 改为 defer 包装器 Close，所有分支/异常都清理，而底层 Close 恰好调用一次。此包装器不承诺并发 Read/Close 安全。
- 全文/Range 复制字节数正确且无先前错误时，在处理结束前关闭同一源并检查结果。非取消 Close 失败走既有精确 `http.ErrAbortHandler` 终态，不追写 JSON/文本、改状态、重开/回退或自动重试。
- 已有 Read/write 错误、短读、取消仍为主要结果；失败复制时由 defer 清理，不能让随后的 Close 错误覆盖、追加或触发第二个 panic。Close 中观察到 request context 已取消也保持取消语义。
- HEAD 不打开原始 Blob；GET 的 metadata-only 304/412、416 与 prefix 错误仍保持既有协议结果，必要的 defer 清理不改写结果。不为 Close 校验读取剩余数据、提前 drain 或增加任意源 Read；未读完整的 Range 不做全文摘要结论。
- 日志只用固定 reason `source_close` 和 request ID，不包含底层错误、摘要、凭据/地址/Key。访问事件仍是既有打开/读取尝试，不新增成功下载审计或自动腐败标记。
- 已发字节不可撤回：本地小正文的中止可被客户端观察，但大正文若已全部交付 Content-Length，晚到 Close 失败不能保证客户端识别。此修复不是下载前校验、主动中断阻塞 Read/Close、任意部分 Range 完整性保证、同一数据版本快照或跨协议统一流终态。

## 验证计划

HTTP 覆盖真实 Close-only 错误、完整消费 Range 的真实 verifier 错误、当前/历史/旧与显式选择、源身份与单次打开/关闭、metadata-only 原结果、无额外 Read 与独立新请求恢复。隔离测试覆盖 Close 结果缓存、失败优先级、日志脱敏、取消/Close 中取消、无 closer 的受控内存读源兼容。

复跑既有取消/条件/Range/数字边界/错误/元数据/匿名公读/预签名/授权/租户回归；定向普通/race、`make check` + `python3 cli.py accept` 与前端交付顺序 `make web-check` → `make build` → `make web-smoke`。外部身份/存储、实库、HTTP2/代理、大正文/RSS、阻塞关闭/读取与全仓 race 另行验证；UI 冒烟仍仅登录壳与资产。

## 本轮实际验证

- 新增 10 项回归通过（5 项 HTTP、5 项隔离单测）。首轮 3 项 HTTP 失败：8 个 Close-only、8 个完整消费 Range 的真实 verifier Close 错误，以及恢复用例的首请求；红灯保留。metadata-only 不覆盖和 partial 不额外读取的控制用例原先已通过，修复后仍通过。
- HTTP 保持当前/历史、旧精确 thumbnail Key/显式 content 的源身份与一次打开/关闭；正常复制后的错误中止，失败后独立请求可恢复，HEAD/304/412/416 不被清理错误改变。真实 verifier 用例仍复用原 Service 开关与实现，不手工伪造校验 verdict。
- 隔离测试覆盖 Close 成功/失败结果缓存（重复调用仍只关底层一次）、固定 source_close 日志且无私有诊断、先前复制错误/短读优先、请求预取消和 Close 中取消、没有 io.Closer 的受控内存 reader 兼容。关闭不增加源 Read 或 drain，新增包装器不声称并发 Read/Close 安全。
- 生产只调整 REST Get/GetSpecificVersion 所持原始流包装、`content_range.go`/`content_response.go` 的同源复制结束与缓存关闭。3 处已有 helper 隔离测试仅适配明确的包装器参数；原有关闭、取消与 EOF 数据断言保留，未改其他协议或 Service 实现。API 与运行时 OpenAPI 同步。
- 使用 `GOMAXPROCS=4 GOFLAGS=-p=1`：REST/service/middleware 定向普通分别 46.142/4.056/0.008 秒；同范围定向 race 分别 115.618/4.271/1.047 秒，包预算 300 秒未改。涵盖本轮及之前原始内容、关闭/读取、取消、条件/Range/数字、元数据、匿名公读、预签名、policy/授权/租户回归。
- `make check` 与 `python3 cli.py accept` 均 exit 0；格式、vet/integration 类型检查、构建、全仓普通单测及仓库已有 metadata、thumbnail short、REST cache race 通过，部分未改包正常缓存。未另跑整仓 `make test-race` 或 PostgreSQL/Qdrant 实库。
- 前端交付依序 `make web-check` → `make build` → `make web-smoke` 通过：60 文件/251 项前端与 4 项 Node；两条登录壳、9 个成功响应、2 项资产，JS 仍 `index-Z_F4LqoO.js`。无新增前端功能、登录后下载/保存 E2E 或全部客户端晚到错误识别承诺。
- 当前/历史 handler 最大函数 44 行/复杂度 9；新完成处理最大 19 行/复杂度 6、range 文件最大 26 行/复杂度 7、新测试最大 21 行/复杂度 5。文件大小、I1–I6、`git diff --check` 通过；禁止接口/目录、配置、依赖和预算未改，未提交、推送或部署。
