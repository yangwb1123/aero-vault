# 缩略图条件再验证的媒体声明门禁 v1

## 线索与限定范围

槽位 003 已修复实际打开源的媒体选择，本轮不重复该工作。本轮在当前代码独立复现：`revalidateThumbnailSource` 的二次 FileService 授权 Stat 只查编码、未查 Content-Type，旧可派生声明因此为新不可派生声明错误签发 304。真实 SQLite/local 版本行与 Blob、API Key、共享 12 环中间件下，60 个场景均错误返回 304（20 个 image/webp 应为 415；40 个 text/plain/bare token 应为 400），还错误增加 304 计数。正常声明与二次授权 Deny/provider 故障对照原先通过。不是已证明的权限绕过或缓存污染。

仅处理 REST 派生缩略图的二次条件 Stat 媒体声明门禁。复用既有四分桶规则和 400/415 分类，不新增格式、接口、依赖、配置或额外 I/O，不改缓存身份/算法、admission、解码预算、打开源/原始内容、前端、SDK、分享/公开资源或其他协议。保留全部既有工作区和禁止路径。

## 验收契约

- 首次 Stat 的拒绝仍为终态；进入条件再观察时，FileService 授权、租户/对象状态错误先终止，非空 Content-Encoding 仍优先于媒体门禁。
- 二次授权观察的 Content-Type 复用 `thumbnailMediaType`：JPEG/PNG/GIF 声明继续；其他 image/* 返回 415；其他非 generic 非图片声明（包括合法 bare token）返回 400。大小写/参数规范化保持。
- 空、真正解析失败或 application/octet-stream 的 generic 声明在 metadata-only 再验证阶段仍不读 magic；不为检查物理健康打开 Blob、获取 decode admission、读取/关闭、解码或写派生缓存。metadata-only 304 不承诺新源字节可解码。
- 声明拒绝不产生 304/success、验证器、版本、缓存声明或派生正文；保留 Vary、已有错误 code/status 与拒绝计数。条件不因旧声明获得通行证；不回退、重试或重选源。
- 当前/历史 pin、GET/HEAD、旧后缀/显式派生、真实 local generation 与无能力 fallback 的既有行为保持。已选中的受支持历史版本不受当前声明影响；独立新请求可正常恢复。
- 确定性先旧后新 repository 观察仅建模一种替换交错，不证明任意并发交错、跨层原子快照、阻塞 I/O 中断、服务 RSS 或缓存命中重新观察。

## 实际结果

- 生产仅在既有 `revalidateThumbnailSource` 授权、编码门禁后增加 4 行：调用共享 `thumbnailMediaType(fresh.ContentType)`，失败沿用 `writeThumbnailError` 分类/计数并终止。修改后的 helper 16 行；没有额外 Stat、Blob 打开、源读取/关闭、重试、admission 或缓存算法变更。GET/HEAD/旧 thumbnail 运行时 OpenAPI 与 `docs/api.md` 同步。
- 新增 10 项测试（9 项 HTTP、1 项运行时 OpenAPI）通过：上述 60 个拒绝场景；supported 大小写/参数；generic 空/真正解析失败/octet-stream 在真实不可解码 WebP 字节上仍 metadata-only；generation/fallback；Deny/provider 与编码优先；历史 pin 不查当前行；独立请求恢复；首轮拒绝终态；admission 已占满仍不等待。拒绝没有成功验证器/版本/缓存声明、成功/304 计数或缓存存入，保留 Vary；源零打开/Read/Close、没有访问事件。generic 304 明确不证明物理健康，不新增该承诺。
- 全部 Go 命令固定 `GOMAXPROCS=4 GOFLAGS=-p=1`，每包预算 300 秒未变。`go test -count=1 -timeout 300s ./internal/api/rest/ -run '^TestThumbnailMediaRevalidation' -v`：`red.log` exit 1（2.975 秒，60 个错误状态）；4 行修复后 `green.log` exit 0（2.567 秒，3 项初始测试）；补齐 10 项后 `focused-final.log` exit 0（5.828 秒）；review 后最终 `focused-reviewed.log` exit 0（5.714 秒）。红灯未覆盖，没有删除断言。
- review 时 `/home/u1/go/bin/gocyclo -over 10 internal/api/rest/thumbnail_media_revalidation_test.go` 首次 exit 1（`complexity.log`，一个新断言 helper 复杂度 11）。只将该 helper 的互斥 bound/unbound 分支改为等价早返回，保留全部断言，最终同命令 exit 0（`complexity-final.log`）；未改生产规则或预算。新测试文件 313 行、函数最多 29 行、复杂度不超过 10；改动的生产 helper 复杂度 4。
- 普通整包 `go test -count=1 -timeout 300s ./internal/api/rest/ ./internal/service/ ./internal/middleware/ -v` exit 0（122.475 / 26.699 / 0.592 秒），`ordinary.log`。
- 缓存与再验证 race：`go test -race -count=1 -timeout 300s ./internal/api/rest/ -run 'ThumbnailCache|ThumbnailClose|Thumbnail.*(Cache|Revalid)|TestThumbnailContentEncoding|TestThumbnailGeneration|TestSniffThumbnailSource|TestClassifyNilThumbnailSource|TestThumbnailGeneric' -v` exit 0（83.374 秒），`race-cache.log`，含本轮全部 10 项。
- 打开/读取边界 race：`go test -race -count=1 -timeout 300s ./internal/api/rest/ -run '^TestThumbnailOpen(Media|Encoding)|TestThumbnailMediaTypeNormalization|TestThumbnailUnsupportedFormat|TestThumbnailErrorPathCacheHygiene|TestThumbnailPreconditions|TestThumbnailStrongPrecondition|TestThumbnailMidDecode|TestThumbnailCancelWhileParked|TestRepeated|TestObjectReadBoundary|TestLegacyThumbnailDenial|TestOpenedContent|TestContent(Stream|Close|Reader|ReadHeaders)' -v` exit 0（167.701 秒），`race-boundaries.log`。两组在首次执行前按语义拆分；没有失败后缩减范围或延长预算，没有并发实现变更。
- Service/middleware race：`go test -race -count=1 -timeout 300s ./internal/service/ ./internal/middleware/ -run 'ThumbnailSource|ReadVerification|TestRecoverer|TestTenant' -v` exit 0（10.706 / 1.018 秒），`service-race.log`。仅定向 race，不冒充整仓 `make test-race`。
- 受影响三包 `go vet`、`python3 cli.py check-filesize`、`python3 cli.py invariants`、全树 gofmt、`git diff --check`、生产 helper/新测试的 gocyclo 与 AST 函数行数检查均 exit 0。完整命令、输出和真实 exit 分别保存在同名前缀 `.command` / `.log` / `.exit`，目录为 `/home/u1/.abr/projects/aero-vault-5c281c8a1991a53d/runs/implementation-24h-sol-max-20261015T070122Z/evidence/004-local/`。
- 开始时 2781 个路径逐路径匹配槽位 003 最新验收快照；范围审计仅 6 个允许路径变化，其他初始路径字节/模式、HEAD/分支/索引保持不变。未修改门禁、运行控制、计划、禁止路径或其他报告，未提交、推送、部署或启动其他 agent。
- 本槽位未自行运行完整 `make check`、`python3 cli.py accept` 或前端 `make web-check` → `make build` → `make web-smoke`，等待返回后的独立机械执行，不预填成功。无前端/嵌入资产交付。未运行整仓 race、HTTP/2/外部代理、外部身份/存储、实库 PostgreSQL/Qdrant、生产、阻塞 I/O、压力/RSS 或登录后浏览器 E2E。确定性交错不证明任意并发/跨层快照；缓存命中的重新观察、源物理健康与其他协议仍不在本轮保证内。
