# 缩略图合并跟随者的访问尝试源归属 v1

## 必要性、复现与范围

槽位006留下的源码线索已独立复现：真实 SQLite/local 版本行与 Blob、API Key、共享12环中间件下，leader/follower 都先观察 A，leader 实际打开 B，follower 共享 B 的 JPEG/ETag/Version ID，但其访问尝试事件仍报告 A 的 ObjectID、ETag、size、content_type。GET/HEAD、旧/显式派生入口共4场景全部失败；leader事件、响应身份、单次打开/关闭和未向 A 缓存存入的控制断言已通过。

本轮只修已存在的合并结果向 REST 访问尝试事件传递源元数据。不是响应正文错误或权限绕过；不新增成功下载审计、事件字段/类型/可靠投递承诺、协议、权限/加密模型或跨层快照。不重做001–006，不改 FileService/Storage/Repository 接口、缓存键/淘汰/flight/admission/锁/channel、解码/流生命周期、其他协议、SDK、AI、配置、依赖、预算或前端。HTTP route/scope/响应契约不变。

## 有界验收

- 成功 coalesced follower 的既有一次 EventAccessed 使用本次共享结果实际打开源的 ObjectID、tenant/bucket/key、backend、size、ETag、Content-Type，且 RequestID 属于 follower，不能复用 leader 请求上下文或再读取当前对象来猜测。
- 源元数据按值传递，只携带现有事件需要的标量，不携带授权票据、reader、repository依赖、secret metadata、回调或可变 map。ObjectID 不加入 generation/cache identity，不替代 VersionID 或 Bound。
- resident hit 没有 opened snapshot，仍使用该请求已经授权且与缓存键匹配的 Stat；leader miss 仍只由原 FileService 路径发事件，不双发。历史 pin 使用自己的源；304/412/拒绝的 follower 不新增 access 事件。leader 已产生的打开尝试不撤销。
- 首次 Stat 的 Deny/provider/停用/状态失败优先于 lookup/flight；miss opener 仍再授权，精确源一次打开/关闭，不额外 Stat、重开、重试或 drain。源验证器、缓存政策、租户隔离、取消和已有错误优先级不变。
- 访问事件仍是 best-effort 打开/读取尝试，不代表客户端收到或保存完整正文；不承诺任意交错最新性、物理健康或阻塞 I/O 中断。不修改已有合并共享语义。

## 红灯与验证计划

证据目录 `/home/u1/.abr/projects/aero-vault-5c281c8a1991a53d/runs/implementation-24h-sol-max-20261015T070122Z/evidence/007-local/`；所有 Go 命令固定 `GOMAXPROCS=4 GOFLAGS=-p=1`、每包300秒。

- `red-http.log` / exit 1：`go test -count=1 -timeout 300s ./internal/api/rest/ -run '^TestThumbnailAccessSourceCoalescedFollowerUsesOpenedObject$' -v`。4场景仅上述事件归属失败；真实 HTTP HEAD 零正文成立。
- 最早 `red.log` / exit 1 还包含把 ResponseRecorder 当真实传输而错误期待 HEAD 零正文的 fixture 问题。改为真实 httptest HTTP server 后，在生产代码未改时重新保留 `red-http.log`，未删除有效事件/正文/身份/关闭断言；不把 recorder 行为当新增生产缺陷。
- 合并测试通过 channel 停住源读取并观察 follower wait；仅该测试 follower context 用 `context.WithoutCancel` 避免数据库取消观察者干扰握手，因此本 fixture 不证明 transport cancellation。独立取消回归另跑。
- 计划：红灯用例修复后重跑，并补 resident/miss、pin、拒绝/强条件、元数据按值传递对照；整包普通测试与受影响定向 race，scope/格式/文件/不变量检查。完整 `make check`、`python3 cli.py accept` 及 Web 三步由返回后的独立机械阶段执行，不预填结果。

## 实际结果

- 生产修改仅 `thumbnail.go`、新增 `thumbnail_source_metadata.go`、`source_identity.go`：opener 把实际源的4个归属标量填入既有按值 opened result；成功 follower 由该结果构造现有 EmitAccessed 所需对象，沿用 follower context；resident hit 仍用 Stat。保留原 Identity/ETag/Bound/CacheStoreBlocked/UpdatedAt，不把 ObjectID 混入 cache identity。没有服务事件 schema、缓存键或同步实现变更；HTTP route/scope/响应未改，故不改 OpenAPI 以冒充新协议交付。
- 最小修复后的 `green.log` exit 0，4场景全部通过；补齐6项定向回归后的 `focused.log` exit 0。6项包含：4入口组合的源归属、完整/空 opened 标量与 proof/policy 独立性、miss/resident/pin/304、共享结果强条件412不发 follower 事件、Deny/provider故障不能加入已开始的 flight、库层 metadata 按值共享/调用者修改不串扰/resident不留 opened snapshot。源精确一次打开/关闭、无额外 Stat、不向 A key 存入 B JPEG 和 follower RequestID 均断言。
- `complexity.log` exit 1 报两项新测试函数复杂度15/12；仅提取等价断言，`complexity-final.log` 和 `complexity-reviewed.log` 均 exit 0。没有增加豁免或删除真实事件/源/关闭断言。
- 首次受影响四包普通测试 `ordinary.log` exit 1：只有新增 Deny/provider 对照错误复用了“成功 Stat 后媒体拒绝”的 Vary helper，要求第一次 Stat 失败也带 derivation Vary。该失败实际先于 Vary 声明；没有据此修改生产行为。改用阶段正确的专用断言，保留全部无验证器/版本/缓存头检查，并确认未进入 derivation Vary。按原命令、原四包、原300秒预算重跑 `ordinary-reviewed.log` exit 0；没有缩小失败范围。
- 普通四包：`go test -count=1 -timeout 300s ./internal/api/rest/ ./internal/thumbnail/ ./internal/service/ ./internal/middleware/ -v`，最终分别171.300 / 19.715 / 25.946 / 0.591秒。库 corpus reproduction 有1项既有 skip（缺少指定 ProgressiveJPEG crasher），不冒称全部零 skip。
- 缓存/合并/新归属 race：`go test -race -count=1 -timeout 300s ./internal/api/rest/ -run 'ThumbnailCache|ThumbnailClose|Thumbnail.*(Cache|Revalid)|TestThumbnailContentEncoding|TestThumbnailGeneration|TestSniffThumbnailSource|TestClassifyNilThumbnailSource|TestThumbnailGeneric|TestThumbnailCoalesced|TestThumbnailConcurrentColdBurst|TestThumbnailLateCanceled|TestThumbnailAccessSource' -v`，`race-cache.log` exit 0，101.914秒；包含本轮5项REST测试及既有租户隔离、取消、关闭/合并回归。
- 打开/内容边界 race：`go test -race -count=1 -timeout 300s ./internal/api/rest/ -run '^TestThumbnailOpen(Media|Encoding)|TestThumbnailMediaTypeNormalization|TestThumbnailUnsupportedFormat|TestThumbnailErrorPathCacheHygiene|TestThumbnailPreconditions|TestThumbnailStrongPrecondition|TestThumbnailMidDecode|TestThumbnailCancelWhileParked|TestRepeated|TestObjectReadBoundary|TestLegacyThumbnailDenial|TestOpenedContent|TestContent(Stream|Close|Reader|ReadHeaders)' -v`，`race-boundaries.log` exit 0，122.387秒。两组首次执行前已固定，没有失败后缩小选择范围。
- 库全包 short race：`go test -race -short -count=1 -timeout 300s ./internal/thumbnail/ -v`，`race-thumbnail.log` exit 0，127.098秒；8项既有 skip（race fast-kernel、short 内存项及上述 corpus），不证明大内存 race 项。服务/中间件 race：`go test -race -count=1 -timeout 300s ./internal/service/ ./internal/middleware/ -run 'ThumbnailSource|ReadVerification|TestRecoverer|TestTenant' -v`，`race-service-middleware.log` exit 0，8.897 / 1.037秒。
- 最终新增6项在 race 下重复10次：`go test -race -count=10 -timeout 300s ./internal/api/rest/ ./internal/thumbnail/ -run '^TestThumbnailAccessSource|^TestCacheOpenedAccessMetadata' -v`，`focused-final.log` exit 0；含40次GET/HEAD入口矩阵。此握手不是压力/生产取消验证。
- 受影响四包 go vet、全树 gofmt、filesize、I1–I6、`git diff --check`、私有 AST 函数/文件预算及 scope 核对均 exit 0。新增文件最大268行、函数/闭包最大39行，生产两个新 helper 为14/17行；旧大 derivation 只缩短6行，没有扩大重构。完整命令与真实exit均保留在 `.command/.exit/.log` 及 `commands.jsonl`。
- 初始2788路径与006最新验收/机械快照一致；本槽起始118项脏状态均保留。最终2793路径，仅8个允许路径变化，其他既有字节/模式及 HEAD/分支/索引不变；私有备份2787个实际文件SHA已核对。旧工作、原禁止路径脏状态与001–006不归本槽新增。未改控制/计划/门禁/全局配置或其他槽报告，未启动其他agent、生产/外部写请求，未提交/推送/部署。
- 本槽未执行完整 `make check`、`python3 cli.py accept` 或 `make web-check` → `make build` → `make web-smoke`，等待返回后独立机械执行，不预填成功。无前端/嵌入资产改动。未验证整仓 `make test-race`、HTTP/2/代理、真实外部身份/存储/KMS、PostgreSQL/Qdrant、生产、阻塞I/O中断、压力/RSS或登录后浏览器E2E；不把本地确定性交错推广为任意交错最新性、可靠审计或跨层快照。
