# 缩略图打开源的媒体类型选择 v1

## 已复现与最小范围

槽位 002 提出的线索已在当前代码独立复现，并非授权扩展。真实 SQLite/local 版本行和 Blob、API Key、共享 12 环中间件下，首次 Stat 返回旧的可派生行，FileService opener 再授权观察到新行时：

- 16 个 image/webp 声明错误生成 JPEG、返回 200；32 个 text/plain 或 bare token 声明同样错误返回 200，均读取了不应读取的源。
- 新源为 generic/空/真正解析失败的 Content-Type，带 WebP magic 时，24 个场景错误返回 400 而非既有 415。
- 反向由旧 generic 变为受支持声明，新源仍为不可解码 WebP 字节时，8 个场景错误返回 415，而非声明路径已有的 decode 400。
- 正常可解码替换对照原先通过。不据此声称权限绕过或缓存污染；identity mismatch 的现有缓存存入门禁不变。

首轮命令 `GOMAXPROCS=4 GOFLAGS=-p=1 go test -count=1 -timeout 300s ./internal/api/rest/ -run '^TestThumbnailOpenMedia' -v` exit 1，包耗时 16.880 秒。完整红灯与命令、exit 保存在本 run 私有 `evidence/003-local/red.{log,command,exit}`，不覆盖。

只修 REST 派生缩略图实际打开阶段的 Content-Type 门禁/sniff 选择。不改 metadata-only 二次 Stat、缓存命中、缓存身份/算法、解码格式/预算、admission、FileService/Storage/Repository 接口、配置、依赖、前端、SDK、原始内容或其他协议。首次 Stat 仍可提前拒绝，不因后续对象变化而重试或绕过。

## 验收契约

- 保留首次授权 Stat、编码检查、参数/条件门禁及既有顺序。需要打开时仍由 FileService 唯一再授权并选择精确当前/历史源；授权、对象状态、打开失败及非空 Content-Encoding 拒绝优先于打开源媒体分类。
- 首次 Stat 和打开源复用同一四分桶媒体规则：受支持声明 JPEG/PNG/GIF 直接解码；其他 image/* 声明返回 415；空、真正解析失败或 application/octet-stream 由 magic 决定；其他声明（包括 mime.ParseMediaType 接受的 bare token）返回 400。大小写/参数规则及现有 decode 错误分类不变。
- 打开源声明拒绝发生在任何 Read 之前；关闭该精确源一次，不 drain，cleanup Close 故障不替换主 400/415。generic 的 prefix 读取/回放、取消和拒绝清理由已有 sniff helper 负责，不重开、不重选、不增加额外 Stat。
- 拒绝结果不发成功验证器、版本/缓存声明或派生正文，不计成功/304、不存入缓存；Vary 与既有有界/安全错误分类、访问尝试事件保留。关闭在已有 admission 释放之前。
- GET/HEAD、旧后缀/显式派生、有真实 generation 证明与无能力 fallback 均覆盖。未变更的健康源、正常类型切换、历史 pin 和后续独立请求保持；不以当前不支持类型拒绝已选中的可派生历史版本。
- 测试的先旧后新观察只是确定性交错模型，不证明任意并发交错、数据库/Blob 原子快照、阻塞 Read/Close 中断或服务 RSS。

## 实际结果与边界

- 生产只提取原四分桶规则到 `thumbnailMediaType`（16 行），由首次 Stat 与新 `openThumbnailSource`（18 行）共用；后者先复用槽位 002 的授权打开/编码门禁，再检查实际打开对象的类型并决定是否调用既有 sniff helper。声明拒绝关闭一次、零 Read；不修改 Service、解码/缓存算法或并发实现。原 handler 缩减，不重构整条派生流水线。API 文档与 GET/HEAD/旧 thumbnail 的运行时 400/415 描述同步。
- 新增 10 项测试（9 项 HTTP、1 项运行时 OpenAPI）最终通过：上述 80 个错误状态场景修复，覆盖声明拒绝零 Read/精确源/一次关闭、generic 与声明决策双向切换、健康替换、大小写/参数/空/解析失败、无 generation fallback、Deny/provider 故障优先、cleanup 错误不覆盖且 admission 可复用、历史 pin、首轮拒绝终态与独立请求恢复。未声称缓存污染或权限绕过。
- 修复后首跑同一 focused 命令仍 exit 1（`green.log`，6.040 秒）：新增反向切换测试把 decoder 既有指标 `unsupported` 错写为 `invalid_argument`。400 状态已正确；核对现有 `thumbnailRejectionReason` 后仅纠正新增测试的指标期望，保留完整失败日志、原断言与生产分类。随后同命令 `focused-final.log` exit 0，8.444 秒，10 项测试通过。
- 全部 Go 命令固定 `GOMAXPROCS=4 GOFLAGS=-p=1`，每包 300 秒预算不变。普通整包 `go test -count=1 -timeout 300s ./internal/api/rest/ ./internal/service/ ./internal/middleware/ -v` exit 0，分别 120.420 / 28.960 / 0.616 秒，`ordinary.log`。
- 缓存/旧再验证 race：`go test -race -count=1 -timeout 300s ./internal/api/rest/ -run 'ThumbnailCache|ThumbnailClose|Thumbnail.*(Cache|Revalid)|TestThumbnailContentEncoding|TestThumbnailGeneration|TestSniffThumbnailSource|TestClassifyNilThumbnailSource|TestThumbnailGeneric' -v` exit 0，57.803 秒，`race-cache.log`。
- 打开/媒体/读取边界 race：`go test -race -count=1 -timeout 300s ./internal/api/rest/ -run '^TestThumbnailOpen(Media|Encoding)|TestThumbnailMediaTypeNormalization|TestThumbnailUnsupportedFormat|TestThumbnailErrorPathCacheHygiene|TestThumbnailPreconditions|TestThumbnailStrongPrecondition|TestThumbnailMidDecode|TestThumbnailCancelWhileParked|TestRepeated|TestObjectReadBoundary|TestLegacyThumbnailDenial|TestOpenedContent|TestContent(Stream|Close|Reader|ReadHeaders)' -v` exit 0，123.297 秒，`race-boundaries.log`。两组在首次执行前即按语义拆分，未因失败缩减测试或调整预算。
- Service/middleware race：`go test -race -count=1 -timeout 300s ./internal/service/ ./internal/middleware/ -run 'ThumbnailSource|ReadVerification|TestRecoverer|TestTenant' -v` exit 0，9.040 / 1.019 秒，`service-race.log`。这是定向 race，不冒充整仓 `make test-race`。
- 受影响三包 `go vet`、`python3 cli.py check-filesize`、`python3 cli.py invariants`、全树 `gofmt -l`、`git diff --check`、新媒体 helper/测试的 gocyclo 与 AST 函数行数检查均 exit 0。新生产文件 54 行、新测试 274 行；新增函数最多 20 行，复杂度不超过 10。全部执行的完整命令、真实 exit 与输出分别保存为 `.command` / `.exit` / `.log`；日志根目录为 `/home/u1/.abr/projects/aero-vault-5c281c8a1991a53d/runs/implementation-24h-sol-max-20261015T070122Z/evidence/003-local/`。
- 开始快照与槽位 002 最新验收快照逐路径一致。只变更 7 个允许路径；既有其他路径字节/模式、HEAD/分支/索引保持，禁止路径不改。范围核对保存在同目录 `scope-audit.{command,exit,log,json}`。未修改运行控制、门禁/计划/预算或其他报告，未提交、推送或部署。
- 本槽位未自行运行完整 `make check`、`python3 cli.py accept` 或 `make web-check` → `make build` → `make web-smoke`，等待返回后的独立机械执行，不预填成功。无前端/嵌入资产交付。未运行整仓 race、HTTP/2/外部代理、真实外部身份/存储、PostgreSQL/Qdrant、生产、阻塞 I/O、压力/RSS 或浏览器登录后 E2E。确定性替换模型不证明任意交错或跨层原子快照；metadata-only 二次 Stat 的媒体变化和缓存命中重新观察不在本轮保证内。
