# 读取取消边界复查 v1

## 范围

复查尚未提交的工作台 ACL、上传、版本读取及下载改动。仅补强本轮读链路中两个取消边界，不新增服务、接口、配置、后台任务或大文件能力。

1. 精确历史版本已打开流的 Range 前缀跳过使用 `io.CopyN(io.Discard, ...)`。若源流忽略 context，客户端取消不会停止后续 Read；响应体尚未开始，不能依靠 ResponseWriter 写失败停止前缀读取。
2. JPEG 预览首读成功后释放响应体，再发送缩略图请求。仅透传 AbortSignal 无法阻止忽略 abort 的 transport 在旧上下文发出第二个请求。

## 契约

- 历史版本内容继续由 FileService 打开/授权精确 ID，只包装其已打开流；不调用定位当前对象的 GetRange，不增加 storage/repository 接口或 Key 校验。
- 在历史 Range 跳过、响应体和完整历史读取的每次 Read 前后检查 request context。已开始的底层 Read 可以完成；检查时已观察到取消，则不交付该次 Read 结果、不再发起下一次 Read。已经返回/发送的字节不可回收，原始流仍由 handler 恰好关闭一次。
- 304/412/416、If-Range、租户/ACL/provider 故障及删除标记语义保持不变。开始发送响应后取消不能改写已发送状态或追写 JSON；不证明服务端未打开或审计。
- 预览开始前、成功响应转入 Blob 读取前、JPEG 首读体释放后和 Blob 完成后检查 AbortSignal。取消后的未消费响应体尽力释放，不启动第二次 JPEG 请求或回退最新；旧缩略图已经发出的请求不等于可撤回。
- 保持完整 Key/Version ID、凭据、Range 与默认桶语义，不改 ACL、上传或下载状态模型。
- 后续将同一已打开流/Read 取消原语复用于普通内容响应，见 [`opened-content-response-v1.spec.md`](opened-content-response-v1.spec.md)；本节下方记录仍是历史版本/前端取消补强当轮证据。
- 不承诺中断一个已阻塞的底层 Read，也不解决 Get 返回前整文件解密/远程实现取消、大文件资源预算或普通读取的子资源歧义。
- 后续非取消型 Read/write 错误的终态、CopyN 对同时返回数据/错误的处理及 stdlib abort 透传，见 [`content-stream-failure-v1.spec.md`](content-stream-failure-v1.spec.md)。保留本轮取消时不追写正文、不新增底层关闭或读取次数的边界。后续缓存 Close 结果与正文成功后的 Close 失败处理见 [`content-close-failure-v1.spec.md`](content-close-failure-v1.spec.md)。

## 验收

先复现：忽略 context 的读流在取消后继续读取 Range 前缀、忽略 abort 的 transport 继续发 JPEG 第二个请求。

Go 使用 SQLite/local fixture + FileService + `httptest`/mock，覆盖前缀跳过、Range 体、完整历史读取、打开后取消、精确版本 StorageKey 与关闭次数；重跑原有授权/版本/缩略图读取回归。前端覆盖预取消、迟到 JPEG/文本/缩略图响应、首读体释放时取消及 Blob 读取中取消，同时保留现有正常与拒绝测试。

执行 `make web-check` → `make build` → `make web-smoke`、定向 race、`make check` + `python3 cli.py accept`。不修改 Sprint 禁止项或门禁阈值，保留此前工作区改动；不提交或部署。

## 本轮验证记录

- 环境 Go 1.26.8、Node v26.11.1；Go 检查使用 `GOMAXPROCS=4 GOFLAGS=-p=1`，未改仓库预算或测试范围。
- 修复前四项 HTTP 回归全部失败：取消后的前缀仍读取 65,544 字节、Range 体读取 8,192 字节、完整旧版读取 90,112 字节，打开后取消也继续读取。六项初始前端取消回归全部失败；保留 `/tmp/aero-read-cancellation-{http,web}-red.log`。
- 修复后四项 HTTP 回归及七项前端取消回归通过。模拟源每次最多 1,024 字节，Read 中取消只完成一次底层 Read、该次结果不交付；打开后取消零 Read，各路径原始流都关闭一次。这是受控 fixture 证据，不是所有后端的字节上限。
- 原有历史 Range、条件请求、错误 ID/删除标记、授权与版本化缩略图回归通过；REST/service 定向 race 以 300 秒包预算通过，包含本轮取消、历史预览、读取授权、版本化缩略图、对象 ACL、幂等、Deny 与停用租户范围。未运行整仓 `make test-race`。
- `make web-check` 通过（59 文件、242 项测试）→ `make build` → `make web-smoke` 通过。产物为 `internal/webui/static/app/assets/index-yVsCGWOY.js`；Chrome 冒烟仅验证 `/ui/`、`/ui/app/` 应用加载及登录壳，不是登录后取消下载 E2E。
- `make check` 与 `python3 cli.py accept` 都通过。make check 的完整单测及既有 metadata、thumbnail short、REST thumbnail cache race 均执行；部分未改包正常使用 Go 测试缓存。
- 本轮修改的 management.go、版本流适配和新增取消测试文件中，函数最大 28 行、复杂度 6；本轮两个前端文件的函数（含测试 describe 回调）最大 43 行、决策计数复杂度 7。filesize、I1–I6、`git diff --check` 通过；Sprint 禁止接口、SDK 和迁移无差异。
- 日志前缀 `/tmp/aero-read-cancellation-`，包括 web-targeted、http-targeted、web-check、build、web-smoke、race、check、accept。未验证真实外部存储/身份、阻塞 Read 的主动中断、整文件解密、大文件/RSS、多实例或灾备。保留既有未提交改动，未提交、推送或部署。
