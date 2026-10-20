# 缩略图打开源的编码拒绝边界 v1

## 复现与最小范围

槽位 001 只修复 metadata-only 条件再验证的二次 Stat。本轮在当前代码新增真实 HTTP 回归，确认首次 Stat 未编码、FileService.OpenThumbnailSource 再授权观察为已编码版本时：16 个 gzip 场景错误返回 400，16 个 identity 声明场景错误生成 JPEG 并返回 200（generation-bound 路径还发出强验证器/版本/缓存声明），另有 4 个 cleanup Close 故障场景错误返回 500。均已发生不应有的源 Read；不是权限绕过，也没有据此证明缓存污染。首轮 exit 1，红灯 `evidence/002-local/red.log` 保留；二次授权失败、未编码替换与历史未编码 pin 对照原本通过。

只处理 REST 派生缩略图这一打开结果的编码门禁。保持 FileService 唯一授权与打开入口，不添加先验授权票据、额外 Stat/重开/重试，不改 Storage/Repository、接口、配置、依赖、前端、SDK、其他协议、原始内容或缓存算法。不新增解码格式，不做整个缩略图流水线重构。

## 验收契约

- 首次 Stat 的编码门禁与 metadata-only 再验证不变；实际进入 opener 后，FileService 授权/对象状态/打开失败仍先按既有分类终止。
- 成功打开结果带非空 `_aero_content_encoding` 时，复用有界 UnsupportedMediaType/415 消息与 content_encoding 计数，在 magic sniff / decode 之前拒绝，不发出验证器/版本/缓存声明或派生正文。
- 此阶段已打开的同一源必须关闭恰好一次且不读取、不 drain；cleanup Close 错误不能替换主 415。关闭先于现有 admission 释放；不承诺中断阻塞 Close。
- 不向派生缓存存入拒绝结果，不计生成成功或 304。打开尝试已由 FileService 产生的访问事件不撤回，不称其为成功下载事实。
- 保留 generation-bound 与无 generation 能力的既有路径、GET/HEAD 和旧/显式派生选择；未编码新源与历史未编码 pin 正常生成，pin 不受当前已编码行影响。
- 测试使用 SQLite/local 真实版本行和 Blob、真实 API Key、共享 12 环中间件。确定性先旧后新 repository 观察只建模一种替换交错，不证明跨层原子快照或任意并发交错。缓存命中没有 opener，不能据此承诺其重新观察当前编码。

## 实际结果

- 生产只把 REST opener 调用替换为 `openUnencodedThumbnailSource`，新增 11 行/复杂度 3 helper；仍先调用 FileService.OpenThumbnailSource，成功后才检查打开对象的编码。非空时复用既有有界错误，关闭同一源后把主错误交给原 OpenError 分类/计数。没有额外 Stat、Read、重开或重试，也未改已有缓存、admission 和解码实现。
- 新增 9 项测试通过（8 项 HTTP、1 项运行时 OpenAPI）：32 个替换场景、4 个 Close 错误场景、授权 Deny/provider 故障优先、Blob 缺失优先、空格与过长 legacy 声明、有界消息、历史 pin、正常未编码替换和独立新请求恢复。真实 local generation 证明与无能力 fallback 均覆盖。拒绝源零 Read、一次打开/关闭、精确 StorageKey、无成功/304/缓存存入得到验证；已有访问尝试事件保留。Close 故障后的 admission 可重新获取。GET/HEAD 与旧/显式派生选择都覆盖；没有新增并发实现。
- `GOMAXPROCS=4 GOFLAGS=-p=1 go test -count=1 -timeout 300s ./internal/api/rest/ -run "^TestThumbnailOpenEncoding" -v`：`red.log` exit 1（3.447 秒）；最小修复后 `green.log` exit 0（3.403 秒、5 项初始测试）；最终 `final-focused.log` exit 0（4.741 秒、9 项测试）。红灯没有被覆盖。首次 review 发现两个新测试函数复杂度 11，随后仅提取等价断言 helper；最终新增测试函数最多 30 行、复杂度不超过 10，原断言保留。
- 普通整包 `GOMAXPROCS=4 GOFLAGS=-p=1 go test -count=1 -timeout 300s ./internal/api/rest/ ./internal/service/ ./internal/middleware/ -v` exit 0：REST 237.243 秒、service 59.746 秒、middleware 3.288 秒，日志 `ordinary.log`。本槽位没有普通测试超时；上一槽位历史超时不作为本轮通过或失败证据。
- 定向 race `GOMAXPROCS=4 GOFLAGS=-p=1 go test -race -count=1 -timeout 300s ./internal/api/rest/ -run "ThumbnailCache|ThumbnailClose|Thumbnail.*(Cache|Revalid)|TestThumbnailOpenEncoding|TestThumbnailContentEncoding|TestUnsupportedThumbnailContentEncodingClassification|TestThumbnailGeneration|TestSniffThumbnailSource|TestClassifyNilThumbnailSource|TestThumbnailGeneric|TestThumbnailStrongPrecondition|TestThumbnailPreconditions|TestThumbnailMidDecode|TestThumbnailCancelWhileParked|TestRepeated|TestObjectReadBoundary|TestLegacyThumbnailDenial|TestOpenedContent|TestContent(Stream|Close|Reader|ReadHeaders)" -v` exit 0，281.973 秒，日志 `race.log`。保持 300 秒每包预算，没有缩减失败范围或修改门禁。
- 追加 `GOMAXPROCS=4 GOFLAGS=-p=1 go test -race -count=1 -timeout 300s ./internal/service/ ./internal/middleware/ -run "ThumbnailSource|ReadVerification|TestRecoverer|TestTenant" -v` exit 0：14.565 / 1.019 秒，日志 `service-race.log`。覆盖 generation proof/fallback、原始编码校验、租户停用与原 abort 语义；不冒充整仓 race。
- 受影响三包 `go vet`、文件大小、I1–I6、全树 gofmt、新 helper/测试复杂度与 AST 函数行数检查均 exit 0；完整命令、真实 exit 与日志保存为同名前缀 `.command` / `.exit` / `.log`。日志根目录：`/home/u1/.abr/projects/aero-vault-5c281c8a1991a53d/runs/implementation-24h-sol-max-20261015T070122Z/evidence/002-local/`。API 文档与运行时 OpenAPI 的 GET/HEAD/旧 thumbnail 415 说明同步。
- 本槽位未自行执行完整 `make check`、`python3 cli.py accept` 或 `make web-check` → `make build` → `make web-smoke`；等待返回后的独立机械命令，不预填成功。没有前端或嵌入资产交付。未运行整仓 race、HTTP/2/代理、外部身份/存储、PostgreSQL/Qdrant、生产、阻塞 I/O、压力/RSS、登录后浏览器 E2E；本地确定性观察不证明任意并发交错或跨层快照。
