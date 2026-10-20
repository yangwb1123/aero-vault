# 缩略图实际打开源的缓存存入准入 v1

## 必要性与最小范围

槽位 005 留下的线索已独立复现：REST 在 opener 前检查加密缓存准入，实际打开结果只向缓存层报告 identity、ETag 和 generation proof；相同身份/ETag 的较新 aws:kms 元数据未参与存入决策。4 个 GET/HEAD、旧/显式入口场景在首次 miss 后各留下 1 项、609 字节 resident JPEG，重复请求错误命中而不打开源。真实 HTTP 红灯已保留；AES256、预检已知 KMS、Deny/provider 故障、SSE-C 拒绝和历史 pin 对照原先通过。不重复 001–005 的编码、媒体或条件再观察修复。

范围仅限已有缩略图 miss 的 resident JPEG 缓存存入准入，复用实际打开源的既有 SSE-C/SSE-KMS/non-content-MD5 规则。不同版本本来已被 identity 比较挡住，不能把该已有保证算作新增。测试以真实 SQLite/local Blob、generation、API Key 和共享 12 环为底座，用 repository seam 建模同一身份下首次观察缺少、实际打开观察含加密标记；只 seed 元数据门禁，不冒称真实 KMS 加密或生产可利用路径。

不扩展权限/加密模型，不改 FileService/Storage/Repository 接口、缓存键/淘汰/flight/admission/解码机制、配置、依赖、前端、SDK、原始内容或其他协议。保留已有基线及禁止路径。

## 有界验收

- 初次已知禁入的源仍绕过缓存；需要 opener 时仍由 FileService 再授权、校验对象状态并打开精确源，失败优先且不重试/回退。
- 已打开源的加密元数据独立否决 resident 缓存存入，即使 identity、ETag、真实 generation proof 均匹配预检；不可用否定 generation proof 来冒充缓存政策。
- 缓存禁入不是读取拒绝。可解码的 aws:kms 元数据源仍返回原有 JPEG/HEAD、同源验证器/版本和访问尝试事件，源关闭一次。AES256/普通健康源继续允许存入及后续命中；历史 pin 独立于当前行。
- 现有 flight 仅在预检允许缓存时启动；本轮不改变已开始 flight 的请求内结果共享、取消或元数据传递，只确保完成后不留 resident 条目。不能宣称没有临时内存驻留、任意交错最新性、物理完整性或原子 DB/Blob 快照。
- 新判定字段零值保持现有 opener 行为；与 identity/ETag/proof 原门禁同时生效。关闭/读取/取消错误继续阻止成功存入，不 drain、不重开。

## 实际结果

- REST 只在既有成功 opener 回调报告实际打开源的缓存禁入结果，并复用既有 bounded bypass reason 计数。`thumbnail.OpenedSource.CacheStoreBlocked` 独立携带 resident 存入否决，零值不改变其他 opener；`storeCached` 同时保留 identity/ETag/proof 比较。不以否定 Bound 伪装缓存政策，不改变安全验证器或 HTTP cache-control，不修改缓存 key/schema、flight 或同步机制。
- 新增 9 项测试通过：6 项真实 HTTP、1 项运行时 OpenAPI、2 项缓存单测。覆盖 KMS 同身份/真实 MD5/proof 的存入否决及后续独立请求仍打开、AES256 零值允许存入/命中、预检 KMS 零 lookup、Deny/provider 故障优先、SSE-C 仍在物理打开前拒绝、历史 pin 独立、源精确且一次关闭、成功/304/access 计数、admission 可复用。确定性 leader/follower 验证 flag/proof 完整传递、原有请求内共享保持、flight 清除且无 resident 条目，后续请求重新打开。没有真实 KMS 或生产可利用性验证。
- 全部 Go 命令固定 `GOMAXPROCS=4 GOFLAGS=-p=1`，每包 300 秒不变。`go test -count=1 -timeout 300s ./internal/api/rest/ -run '^TestThumbnailOpenCacheAdmission' -v`：`red.log` exit 1（4.931 秒，4 场景错误存入/后续命中，另有未同步 OpenAPI 描述失败）；最小修复和文档同步后 `green.log` exit 0（7 项）。`go test -count=1 -timeout 300s ./internal/api/rest/ ./internal/thumbnail/ -run '^TestThumbnailOpenCacheAdmission|^TestCacheOpenedStorePolicy' -v`：`focused-final.log` exit 0，最终 9 项全部通过。红灯未覆盖，未删断言。
- 普通整包 `go test -count=1 -timeout 300s ./internal/api/rest/ ./internal/thumbnail/ ./internal/service/ ./internal/middleware/ -v`：`ordinary.log` exit 0，分别 153.651 / 19.620 / 29.327 / 0.767 秒。已有 corpus reproduction 因缺少 FuzzProgressiveJPEG pinned crasher 标为 skip（Exif 子项实际通过），不称全部零 skip。
- REST 缓存 race：`go test -race -count=1 -timeout 300s ./internal/api/rest/ -run 'ThumbnailCache|ThumbnailClose|Thumbnail.*(Cache|Revalid)|TestThumbnailContentEncoding|TestThumbnailGeneration|TestSniffThumbnailSource|TestClassifyNilThumbnailSource|TestThumbnailGeneric|TestThumbnailCoalesced|TestThumbnailConcurrentColdBurst|TestThumbnailLateCanceled' -v`：`race-cache.log` exit 0，96.323 秒，含本轮 7 项 REST 及既有租户隔离、合并/取消回归。
- REST 打开/读取边界 race：`go test -race -count=1 -timeout 300s ./internal/api/rest/ -run '^TestThumbnailOpen(Media|Encoding)|TestThumbnailMediaTypeNormalization|TestThumbnailUnsupportedFormat|TestThumbnailErrorPathCacheHygiene|TestThumbnailPreconditions|TestThumbnailStrongPrecondition|TestThumbnailMidDecode|TestThumbnailCancelWhileParked|TestRepeated|TestObjectReadBoundary|TestLegacyThumbnailDenial|TestOpenedContent|TestContent(Stream|Close|Reader|ReadHeaders)' -v`：`race-boundaries.log` exit 0，119.502 秒。两组在首次执行前按语义拆分，无失败后缩减范围。
- 库全包 short race：`go test -race -short -count=1 -timeout 300s ./internal/thumbnail/ -v`：`race-thumbnail.log` exit 0，127.205 秒；包含新增两项、原 identity/proof/ETag、flight、Close/Read/取消测试。有 8 项既有 skip（race fast-kernel/short 内存项及上述 corpus），不冒称大内存项均在 race 下执行。
- Service/middleware race：`go test -race -count=1 -timeout 300s ./internal/service/ ./internal/middleware/ -run 'ThumbnailSource|ReadVerification|TestRecoverer|TestTenant' -v`：`race-service-middleware.log` exit 0，8.865 / 1.019 秒。未运行整仓 `make test-race`，无并发实现或锁/channel 修改。
- 受影响四包 `go vet`、`python3 cli.py check-filesize`、`python3 cli.py invariants`、全树 gofmt、`git diff --check` 与范围核对均 exit 0。新测试 168 / 103 行，新函数最多 36 行，新增生产 helper 3 行、opener closure 19 行。没有放宽文件、函数、复杂度或测试预算。
- 全文件 gocyclo 检查 `complexity.log` exit 1，只报未改动的 `generateContextWithOpenerCachedResult` 复杂度 14。启动 SHA 对应原文件的 `baseline-complexity.log` exit 1，原有该 14 及 `storeCached` 11；后者通过本轮小谓词提取已降至不超过 10。私有 AST/复杂度核对初次因错误假设基线只有一项告警而 exit 1（`review-bounds.log`），修正私有核对器以核对完整原始输出后 `review-bounds-final.log` exit 0：新测试/helper/存入函数均不超过 10，且除 `storeCached` 外 cache_entry.go 所有函数逐字节未改。不修正、隐藏或豁免原 14 告警，不因此扩展为流水线重构。
- 完整命令、真实 exit 与输出均保存为同名 `.command` / `.exit` / `.log`，另有 `commands.jsonl`，目录 `/home/u1/.abr/projects/aero-vault-5c281c8a1991a53d/runs/implementation-24h-sol-max-20261015T070122Z/evidence/006-local/`。初始 2785 路径与槽位 005 最新验收/机械快照逐路径一致；最终仅本槽位 9 个允许路径变化（2788 路径），其他初始字节/模式、HEAD/分支/索引保持。已有 113 项脏状态及 001–005 改动保留，不归作本轮新增。
- 本槽位未自行执行完整 `make check`、`python3 cli.py accept` 或 `make web-check` → `make build` → `make web-smoke`，等待返回后独立机械执行，不预填成功。无前端/嵌入资产交付。未验证整仓 race、HTTP/2/代理、真实外部身份/存储/KMS、PostgreSQL/Qdrant、生产、阻塞 I/O 中断、压力/RSS 或登录后浏览器 E2E；确定性观察不证明任意并发最新性、无临时内存驻留或跨层快照。未修改计划/门禁/控制/禁止路径/共享工具/其他报告，未提交、推送或部署。
