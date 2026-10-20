# 缩略图已完成合并结果的 REST 取消终态 v1

## 必要性与最小范围

槽位007指出其合并事件归属fixture显式使用 `context.WithoutCancel`，不证明 follower 的真实传输取消。本槽先独立复现：库层 `waitFlightResult` 明确优先交付已完成结果（已有正例固定该语义），REST 收到成功共享结果后未再次检查请求context。真实客户端已取消时仍尝试写200/JPEG、增加follower访问事件及成功计数（4场景）；真实deadline已过时仍返回200/JPEG（4场景），或被打开源的If-Match再判断改为412（1场景）。这是服务端终态缺陷，不是已断开客户端实际接收了JPEG的证明，也不是权限绕过。

仅限 REST 派生缩略图成功结果到协议响应之间的context终态。保持库层完成结果优先、flight/锁/channel/缓存/准入/解码/关闭实现不变；不改 FileService、权限/事件模型、原始内容、其他协议、SDK、前端、配置、依赖、预算或控制文件。不重复001–007。

## 有界验收

- 实际取消已发生且生成调用成功返回时，REST在强条件再判断、follower事件、成功头/正文/成功计数之前再次检查请求context。客户端取消沿用静默终态；实际deadline沿用504/固定安全message及timeout计数。
- 生成调用本身返回失败时，仍先处理原错误，不用后续context覆盖opener/decode/Close等既有错误优先级。
- 已由leader发出的打开尝试不撤销；取消follower不新增事件，不取消leader、不清理其合法缓存、不重开/重试/drain源。未取消follower继续使用实际打开源及自己的RequestID。
- FileService预检授权与miss再授权、精确Key/版本、generation/proof、租户隔离、一次Close、resident命中/304与Deny/provider拒绝优先级保持。不是成功下载审计、可靠事件、任意时点取消原子性、已发字节撤回或阻塞I/O中断保证。

## 复现与验证计划

- 真实SQLite/local版本Blob、API Key、共享12环中间件与HTTP/1客户端；复用007的A预检/B打开场景，不重复其事件归属实现。
- 新测试的context保留原始Done/Err/Value与真实取消传播，仅在库层 `waitFlightResult` 调用Done求值时以channel暂停当前goroutine（使用runtime帧区分SQL取消观察者）。先确认已加入flight，再取消客户端/等真实deadline、完成leader，最后恢复follower，使完成与取消同时就绪。这个调度缝不改生产代码、不关闭/替换取消信号，也不靠概率重试制造红灯；函数名耦合仅限测试。
- 保留红灯日志，最小修复后重跑GET/HEAD、旧/显式入口、静默取消与deadline；增加未取消和源失败优先等对照。
- 所有Go命令固定 `GOMAXPROCS=4 GOFLAGS=-p=1`，普通整包与定向race每包300秒，不缩减失败范围。完整 `make check`、`python3 cli.py accept` 及Web三步等待返回后独立机械阶段，当前不预填结果。

## 实际结果

证据根目录 `/home/u1/.abr/projects/aero-vault-5c281c8a1991a53d/runs/implementation-24h-sol-max-20261015T070122Z/evidence/008-local/`。完整命令、真实exit及输出存于 `.command/.exit/.log` 和 `commands.jsonl`，没有覆盖先前证据。

- `red-cancel.log` exit 1：`go test -count=1 -timeout 300s ./internal/api/rest/ -run '^TestThumbnailCompletedFollowerClientCancelDoesNotRespondOrEmit$' -v`，4个GET/HEAD、旧/显式入口都尝试写200及609字节、发第二个访问事件、增成功计数。客户端返回实际context.Canceled，服务端原始Done也已关闭；leader响应、源身份、一次打开/关闭和Stat次数对照成立。
- `red-deadline.log` exit 1：同包同预算 `-run '^TestThumbnailCompletedFollowerDeadline'`，4场景错误200，1场景错误412；真实context.DeadlineExceeded已观察。最小修复后同一批测试 `green.log` exit 0，随后控制用例 `focused.log` exit 0。
- 生产终态修复只有 `thumbnail.go` 的成功后context检查，位于原生成失败处理之后、打开源强条件再判断及事件之前，复用既有writeThumbnailGenerateError；不改变库层完成优先或错误优先级。`file_read_spec.go`、`router.go` 仅为既有三个读取operation补充504说明，未改路由/scope；运行时OpenAPI专用回归先以 `red-spec.log` exit 1显示缺失，再 `green-spec.log` exit 0。API文档同步区分静默取消、deadline和已生成失败。
- 最终8项新增顶层回归：真实传输取消4入口矩阵、deadline4入口矩阵、deadline优先于尚未执行的打开源条件、未取消follower保留B源及自身RequestID、pending取消不等leader/不关闭leader、当前/精确pin取消不删除leader合法缓存且后续resident零打开、Close来源失败在取消/deadline后仍保持原500/脱敏/计数优先，以及运行时OpenAPI。只对成功生成结果新增context终态；既有source错误的响应不被本槽统一静默化。
- 普通整包 `go test -count=1 -timeout 300s ./internal/api/rest/ ./internal/thumbnail/ ./internal/service/ ./internal/middleware/ -v`：`ordinary.log` exit 0；补齐运行时说明后按原四包原命令重跑 `ordinary-final.log` exit 0，各包143.704 / 19.731 / 84.548 / 0.589秒。库corpus reproduction有1项既有skip。
- 缓存/合并/新终态race：`race-cache.log` exit 0，126.582秒，含全部新测试与既有007事件、Deny/provider、租户缓存隔离、resident/304、关闭/取消回归。打开源/内容/条件/媒体/编码边界race：`race-boundaries.log` exit 0，128.857秒。完整选择表达式见对应 `.command`，没有失败后缩减范围。
- 库全包 `go test -race -short -count=1 -timeout 300s ./internal/thumbnail/ -v`：`race-thumbnail.log` exit 0，127.220秒；保留完成结果/错误优先正例，8项既有short/race/corpus skip，不冒称大内存race已验证。服务/中间件ThumbnailSource、ReadVerification、Recoverer、Tenant定向race：`race-service-middleware.log` exit 0，8.852 / 1.019秒。
- 所有新增8项在race下重复5次：`go test -race -count=5 -timeout 300s ./internal/api/rest/ -run '^TestThumbnailCompletedFollower' -v`，`race-focused-repeat.log` exit 0，40次顶层通过，包含40次GET/HEAD取消/deadline入口组合。所有Go检查仍为GOMAXPROCS=4、GOFLAGS=-p=1，无预算/依赖/配置变更。
- 受影响四包go vet、全树gofmt、filesize、I1–I6、git diff --check、新测试AST函数/文件预算及gocyclo检查均exit 0；新文件最大200行、函数/闭包最大36行、复杂度≤10。原长derivation/init仅插入局部guard/响应说明，未宣称整体重构为≤50行。
- 起始2793路径与007已验收机械快照及last-accepted逐路径一致，起始123项脏状态已私有完整备份/SHA验证。最终仅本槽8个允许路径变化；其他既有字节/模式、HEAD/main/索引保留，001–007及原113项恢复基线不归本槽新增。没有删除源/测试、改控制文件/计划/门禁、启动其他agent、生产/外部写请求、提交/推送/部署。
- 本槽只完成本地验证。完整 `make check`、`python3 cli.py accept` 及 `make web-check` → `make build` → `make web-smoke` 尚未由本槽执行，等待返回后独立机械阶段；没有Web/嵌入资产变更。未验证整仓make test-race、HTTP/2/代理、真实外部身份/存储/KMS、PostgreSQL/Qdrant、生产、阻塞I/O中断、压力/RSS或登录后浏览器E2E。确定性帧定位只控制测试调度，不证明任意交错取消原子性，也不撤回已开始事件或交付字节。
