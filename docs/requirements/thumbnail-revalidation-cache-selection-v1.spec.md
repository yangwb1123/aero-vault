# 缩略图条件再观察后的缓存源选择 v1

## 线索、必要性与限定范围

前四槽位已处理二次 Stat / 实际打开源的媒体与编码门禁，不重复实施。本轮独立复现一个尚未覆盖的交点：条件再观察已经发现当前源 B、不满足 304 时，后续 200 的缓存选择仍使用初次观察 A。真实 HTTP 预热 A/B 的 JPEG 后，16 个 GET/HEAD、旧/显式入口、B 冷/热、旧 ETag/日期场景均返回 A 的 ETag/版本（8 个 GET 还返回 A 的 JPEG）；B 冷时也未打开 B。二次授权拒绝与匹配新源的正常 304 对照原先通过。是已经观察到更新后的陈旧响应，不据此宣称权限绕过、缓存写入污染或跨层快照。

仅限 REST 派生缩略图条件分支向现有缓存/生成流水线传递哪一份已授权观察。不增加重新观察次数，不改缓存键格式、算法、singleflight、admission、解码/关闭、FileService/Storage/Repository 接口、权限模型、配置、依赖、前端、SDK、原始内容或其他协议。保留全部已有工作区与禁止路径。

## 有界验收契约

- 首次拒绝、二次 FileService 授权/对象状态/编码/媒体拒绝和强前提条件仍为终态，优先于缓存。不把 Stat 当跨请求授权票据。
- 二次条件 Stat 不返回 304、继续生成 200 时，缓存身份、ETag、generation 能力、加密准入及响应元数据采用这份较新的观察，不再读取已明确过时的 A 缓存。B 可命中其既有缓存；B 未缓存则经原有 opener 再授权并生成，不回退或重试 A。
- 当前/历史 pin、GET/HEAD、旧后缀与显式派生保持；稳定匹配仍 metadata-only 304，零源打开/Read/Close、不取 decode admission。历史 pin 可独立读取 A，不能以当前 B 覆盖 pin。
- 命中时零打开/Read/Close，miss 关闭精确源一次；每个成功 200 保持原有一次访问尝试事件和一次 success，未发 304。不改缓存中历史 A 的生存期或主动清空行为。
- 没有二次条件观察的普通命中仍采用该请求已有授权 Stat，不增加全局最新性、物理健康或任意交错/跨层原子快照保证。确定性先 A 后 B 的真实版本行/Blob fixture 不等于并发压力、阻塞 I/O 中断或生产验收。

## 实际实现与验收结果

- 生产仅在 `thumbnail.go` 的既有二次条件 Stat 分支按 fresh 行独立计算 `freshBound`，不再依赖初次行的 generation 能力；不返回 304 时将 `obj/identity/sourceBound/statETag` 一次切换到 fresh 观察。后续复用原缓存、准入、再授权 opener 与响应流程。没有额外 Stat/Blob I/O、缓存算法或并发机制变更。GET/HEAD/旧 thumbnail 运行时 OpenAPI 与 `docs/api.md` 同步。
- 新增 9 项测试（8 HTTP、1 运行时 OpenAPI）最终通过。覆盖上述 16 个错误响应场景、B hot 零打开/Read/Close、B cold 精确源一次打开/关闭、success/304/access 与缓存计数、正常 304 不查缓存、二次 Stat 和 miss opener 的 Deny/provider 故障终态、历史 A 缓存保留且 pin 不查当前行。
- 最新 generation 准入丢失时，B 使用既有 fallback、无可复用验证器/版本且 no-store；初次 legacy 观察缺 marker、B 有真实 proof 时仍可命中 B。legacy 初次行由测试 seam 去 marker 建模，当前 legacy 行使用 repository 原子元数据接口去 marker；实际 local Blob/generation 未修改。加密用例仅 seed 既有 aws:kms 元数据门禁，确认最新门禁禁入已预热缓存，不是实际 KMS/外部加密验证。
- 全部 Go 命令使用 `GOMAXPROCS=4 GOFLAGS=-p=1`，每包 300 秒预算不变。`go test -count=1 -timeout 300s ./internal/api/rest/ -run '^TestThumbnailRevalidationCache' -v`：`red.log` exit 1（1.113 秒，16 场景）；最小修复后的 `green.log` exit 0（初始 3 项）；补齐 9 项的 `focused-final.log` exit 0（2.396 秒）；review 后 `focused-reviewed.log` exit 0（9 项）。红灯未覆盖，未删断言。
- `/home/u1/go/bin/gocyclo -over 10 internal/api/rest/thumbnail_revalidation_cache_test.go` 首次 exit 1（`complexity.log`，新 miss 授权测试复杂度 11）。仅提取等价断言 helper，全部断言保留；同命令 `complexity-final.log` exit 0。新文件 290 行、新函数最多 23 行、复杂度不超过 10，没有扩大既有大 handler 或增加生产函数。
- 普通整包 `go test -count=1 -timeout 300s ./internal/api/rest/ ./internal/service/ ./internal/middleware/ -v` exit 0（127.152 / 28.849 / 0.595 秒），`ordinary.log`。
- 缓存/再验证 race：`go test -race -count=1 -timeout 300s ./internal/api/rest/ -run 'ThumbnailCache|ThumbnailClose|Thumbnail.*(Cache|Revalid)|TestThumbnailContentEncoding|TestThumbnailGeneration|TestSniffThumbnailSource|TestClassifyNilThumbnailSource|TestThumbnailGeneric|TestThumbnailCoalesced|TestThumbnailConcurrentColdBurst|TestThumbnailLateCanceled' -v` exit 0（89.410 秒），`race-cache.log`，包含全部本轮 9 项、既有 coalesced 与 late-cancel 回归。
- 读取/打开边界 race：`go test -race -count=1 -timeout 300s ./internal/api/rest/ -run '^TestThumbnailOpen(Media|Encoding)|TestThumbnailMediaTypeNormalization|TestThumbnailUnsupportedFormat|TestThumbnailErrorPathCacheHygiene|TestThumbnailPreconditions|TestThumbnailStrongPrecondition|TestThumbnailMidDecode|TestThumbnailCancelWhileParked|TestRepeated|TestObjectReadBoundary|TestLegacyThumbnailDenial|TestOpenedContent|TestContent(Stream|Close|Reader|ReadHeaders)' -v` exit 0（125.763 秒），`race-boundaries.log`。两组在首次执行前按语义拆分，无失败后缩减范围或放宽预算。
- Service/middleware race：`go test -race -count=1 -timeout 300s ./internal/service/ ./internal/middleware/ -run 'ThumbnailSource|ReadVerification|TestRecoverer|TestTenant' -v` exit 0（9.273 / 1.018 秒），`service-race.log`。定向 race 不冒充整仓 `make test-race`。
- 受影响三包 `go vet`、`python3 cli.py check-filesize`、`python3 cli.py invariants`、全树 gofmt、`git diff --check`、新测试 AST 函数行数与最终复杂度检查均 exit 0。完整命令、真实 exit 与输出分别保存为 `.command` / `.exit` / `.log`，目录 `/home/u1/.abr/projects/aero-vault-5c281c8a1991a53d/runs/implementation-24h-sol-max-20261015T070122Z/evidence/005-local/`。
- 开始时 2783 个路径逐路径匹配槽位 004 最新验收快照。范围审计仅 6 个允许路径变化，其他既有路径字节/模式及 HEAD/分支/索引保持不变。未修改计划、门禁、运行控制、禁止路径、共享工具、全局配置或其他报告，未提交、推送、部署或启动其他 agent。
- 本槽位未自行运行完整 `make check`、`python3 cli.py accept` 或前端 `make web-check` → `make build` → `make web-smoke`；等待返回后独立机械执行，不预填成功。无前端/嵌入资产交付。未运行整仓 race、HTTP/2/代理、真实外部身份/存储/KMS、PostgreSQL/Qdrant、生产、阻塞 I/O、压力/RSS 或登录后浏览器 E2E。确定性交错不证明任意并发/跨层快照；无二次条件观察的命中、其他协议及源物理健康不在本轮新保证内。
