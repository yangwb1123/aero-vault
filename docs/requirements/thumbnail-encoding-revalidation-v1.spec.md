# 缩略图条件再验证的编码拒绝边界 v1

## 复现与必要性

当前代码只在派生缩略图首次授权 Stat 后检查 Content-Encoding。以真实 SQLite/local 版本化 PNG 与 gzip(PNG) 两个 Blob、真实 API Key 和共享 12 环中间件测试：第一次观察返回旧的未编码行，二次授权 Stat 返回已编码的当前行，GET/HEAD 仍返回 304，并为不可派生的源发出强 ETag、版本 ID 和可缓存声明。

新增回归首跑 exit 1：旧 `/thumbnail` 与显式 `representation=thumbnail`、GET/HEAD、If-None-Match wildcard/包含新旧标签的列表及 If-Modified-Since 共 12 场景均复现错误 304；content_encoding 拒绝计数未增加、304 计数错误增加。正常未编码再验证、二次 Deny/provider 故障优先、历史未编码 pin 不受当前编码影响的控制测试已通过。完整红灯保留在本运行私有目录 `evidence/001-local/red.log`，命令与 exit 独立记录。

仅修这一 metadata-only 条件再验证终态。不扩展解码器、配置、协议、权限或接口；不修改原始内容、打开/正文阶段、缓存算法、Storage/Repository、SDK、前端、迁移或门禁。并发 PUT 用两份真实版本行的确定性观察顺序建模，不声称已做并发压力或数据库/Blob 原子快照验证。

## 最小契约

- 二次 Stat 必须先成功完成 FileService 授权。Deny、provider 故障、租户状态或对象错误仍按既有分类终止，不先泄露编码声明。
- 对二次授权观察到的对象重新执行既有非空 Content-Encoding 拒绝，返回 415 UnsupportedMediaType；不把首次未编码观察当作后续 304 的通行证。
- 复用已有有界编码错误消息、content_encoding 拒绝计数与 Vary。不产生 304/success 计数、验证器/版本/缓存声明，不打开 Blob、不读取、解码或进入缓存。
- 仍严格使用选择的 Key/Version ID。未编码的当前源继续以新观察身份返回 304；未编码历史 pin 不因当前源变为 gzip 而被拒绝。原条件字段合并、优先级和最初的参数/授权门禁不变。
- 不解决缓存命中或源打开阶段的其他元数据变化，不把本次 metadata-only 修复推广成所有读取入口的快照、取消或关闭保证。

## 验收与实际结果

- 回归覆盖以上 12 个拒绝场景，以及未编码新身份 304、二次授权失败不泄露编码、历史 pin 对照；使用既有 generation-bound local 能力，不伪造生成证明。
- 定向普通/race 保持 `GOMAXPROCS=4 GOFLAGS=-p=1`，race 每包 300 秒；复跑既有 Content-Encoding、304 再验证、条件/Range、历史 pin、权限/租户与原始流终态回归。
- 修复后新增 6 项测试通过（5 项 HTTP、1 项运行时 OpenAPI）：12 个拒绝场景、未编码新身份 304、Deny/provider 优先、历史 pin、原始 gzip 内容不受影响；API 文档与 GET/HEAD/旧 thumbnail 的运行时 415 说明同步。生产只提取一个 12 行/复杂度 3 的再验证门禁，原大函数减少 4 行；新增测试函数最多 27 行/复杂度 9，未重构既有大流水线。
- 红灯 `go test -count=1 -timeout 300s ./internal/api/rest/ -run '^TestThumbnailEncodingRevalidation' -v` exit 1（2.971 秒）；首次修复后同命令 exit 0（8.111 秒，4 项初始测试），最终新增到 6 项后同命令 exit 0（1.599 秒）。以上均使用固定 Go 环境；完整日志为 `red.log`、`green.log`、`final-focused.log`，各有 `.command`、`.exit`。
- 普通整包 `GOMAXPROCS=4 GOFLAGS=-p=1 go test -count=1 -timeout 300s ./internal/api/rest/ ./internal/service/ ./internal/middleware/` 首次 exit 1：REST 在累计 300.158 秒时超时，当时既有 TestThumbnailHeadOversized413 仅运行 1 秒，未见断言失败；service 87.844 秒、middleware 1.735 秒通过。保留 `ordinary.log` 和堆栈。未确认此次耗时波动根因，不把它改写成成功。
- 未修改源码、范围或 300 秒预算，只追加 `-v` 重跑上述整包命令，exit 0：REST 131.964 秒、service 76.864 秒、middleware 0.861 秒；原超时位置与全部 6 项新测试均通过，日志 `ordinary-retry.log`。之后仅拆分复杂度 11 的测试断言为等价小函数，没有删除断言；最终 focused 与下述同范围 race 再通过。
- 定向 race 使用 `GOMAXPROCS=4 GOFLAGS=-p=1 go test -race -count=1 -timeout 300s ./internal/api/rest/ -run 'ThumbnailCache|ThumbnailClose|Thumbnail.*(Cache|Revalid)|TestThumbnailContentEncoding|TestUnsupportedThumbnailContentEncodingClassification|TestThumbnailGeneration|TestSniffThumbnailSource|TestClassifyNilThumbnailSource|TestThumbnailGeneric|TestThumbnailStrongPrecondition|TestRepeated|TestObjectReadBoundary|TestLegacyThumbnailDenial|TestOpenedContent|TestContent(Stream|Close|Reader|ReadHeaders)'`；两次均 exit 0（254.305 / 最终 147.050 秒），日志 `race.log`、`race-final.log`。预算和范围未缩小，未引入新的并发实现。
- 受影响三包 `go vet`、文件大小、I1–I6、全树 gofmt、`git diff --check` 与新增函数复杂度检查均 exit 0，日志同目录。Go 命令继续固定环境；完整命令见每项 `.command`。日志目录为 `/home/u1/.abr/projects/aero-vault-5c281c8a1991a53d/runs/implementation-24h-sol-max-20261015T070122Z/evidence/001-local/`。
- 未运行本槽位完整 `make check`、`python3 cli.py accept` 或前端 `make web-check` → `make build` → `make web-smoke`；它们由返回后的独立机械步骤执行，不能预填通过。未另跑整仓 race、浏览器登录后 E2E、HTTP/2/代理、真实外部身份/存储、PostgreSQL/Qdrant、压力/RSS 或阻塞 I/O；本地确定性观察也不证明任意并发交错和跨层快照。
