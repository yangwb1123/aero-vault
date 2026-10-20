# 缩略图条件再观察的强前提回归 v1

## 必要性与最小范围

槽位010仅补已有读取契约的可辨别回归，不报告新的生产缺陷。现有测试分别固定首次条件检查、实际打开源及合并结果的条件检查；二次授权 Stat 从 A 观察到 B 后，强前提必须先于304及缓存继续路径生效，这个交点尚需独立固定。复用005已验收的真实SQLite/local、版本Blob/generation、HTTP预热A/B缓存、API Key及共享12环fixture，不重复归功既有修复或schema素材优化。

仅新增一份REST测试与本文，不修改生产代码、旧断言、运行时OpenAPI、路由、权限/事件模型、缓存/flight/admission实现、其他协议、SDK、配置、依赖、预算或门禁。截止保持2026-10-16T07:07:31.573090Z；剩余不足一小时不启动新方向。

## 有界验收

- 首次Stat的A满足强If-Match，但二次授权Stat的B不满足时，返回412；即使INM在B匹配、日期可返回304，或不匹配后本可命中B缓存，均不能返回304/200或进入cache/opener。
- If-Match列表中的弱B不能让二次观察通过强比较。合法包含强A/B的多物理字段列表或通配符仍允许正常metadata-only304；成功If-Match保持抑制If-Unmodified-Since。
- 精确历史pin A保持自己的304，不查当前B，不触碰已预热缓存。412不泄漏验证器/版本，不增success/304/access；304仅增原304计数。两种终态均不打开/读取/关闭源，不查缓存，不清除A/B条目。
- 使用现有确定性先A后B读取缝，不引入真实并发写者或新的同步机制；不证明任意交错最新性、Blob健康、原子快照、阻塞I/O中断或下载成功审计。

## 证据方案与实际状态

私有证据目录：`/home/u1/.abr/projects/aero-vault-5c281c8a1991a53d/runs/implementation-24h-sol-max-20261015T070122Z/evidence/010-local/`。

使用Go overlay只在私有副本移除二次Stat后的强前提检查，先运行既有相关测试确认该定点变异未被这些测试检出，再以新回归取得红灯。该红灯是有意变异，不是当前生产缺陷或网络故障；工作树生产源码从不改动。随后无overlay运行新增及受影响普通/race验证。Go命令固定`GOMAXPROCS=4 GOFLAGS=-p=1`、每包300秒；测试子进程umask022，证据077。

### 本槽实际结果

- `mutant-existing-controls.log` exit 0：私有overlay仅删除`thumbnail.go`二次Stat后现有强前提检查，12项既有相关测试仍通过（预检、opener、coalesced及005全部9项）。这是选定相关集合的回归缺口证据，不宣称整仓既有测试都无法检出。
- 最终`red-mutant-final.log` exit 1：`go test -overlay=<私有mutant-overlay.json> -count=1 -timeout 300s ./internal/api/rest/ -run '^TestThumbnailRevalidationPreconditionChangedStrongTagIsTerminal$' -v`，32子场景全部失败：24个错误304、4个热缓存错误200、4个冷缓存虽最终412却额外授权/open/Read/Close并触碰缓存/访问事件。冷场景每次重新失效B缓存，避免前一变异请求使后续场景变热。首次`red-mutant.log`也保留，未覆盖。
- 去掉overlay、其余参数不变的`green.log` exit 0（0.409秒）；`-run '^TestThumbnailRevalidationPrecondition'`的`focused.log` exit 0（0.540秒）。新增3项顶层测试：32场景终态矩阵、8次合法强列表/通配符304、4次历史pin A的独立304。所有请求检查两次实际FileService授权；当前观察为两次目标行Stat，pin不查当前行。
- 普通完整REST包`go test -count=1 -timeout 300s ./internal/api/rest/ -v`：`ordinary-rest.log` exit 0，472项顶层测试通过，56.592秒。
- 完整受影响REST包`go test -race -count=1 -timeout 300s ./internal/api/rest/ -v`：`race-rest.log` exit 0，472项顶层测试通过，195.882秒，无`-short/-run`。新测试`go test -race -count=3 -timeout 300s ./internal/api/rest/ -run '^TestThumbnailRevalidationPrecondition' -v`：`race-focused-repeat.log` exit 0，9次顶层通过，9.800秒。包race不是整仓race。
- REST `go vet`、全树gofmt检查、filesize、I1–I6、gocyclo及AST函数/闭包行数检查均exit 0；新测试125行，最大函数/闭包28行，复杂度≤10。`evidence-verification.log` exit 0：所有814份Go源码与普通/race/repeat快照一致，生产`thumbnail.go`保持009字节；命令/日志SHA与umask校验通过。完整命令、exit及日志保存在独立`.command/.exit/.log`和`commands.jsonl`。
- 新增只有测试与本文；生产代码、旧断言、schema素材/公共fixture、运行时API和其他协议不变，不重复实现或归功001–009。起始2813条Git路径匹配009普通门禁与完整race同源快照；162项起始脏状态已私有备份并核验归档内逐文件SHA/模式。

完整`make check`、`python3 cli.py accept`、`make web-check`→`make build`→`make web-smoke`、机械`git diff --check`与完整`GOMAXPROCS=4 GOFLAGS=-p=1 make test-race`待返回后同源执行并独立验收，不预填成功；无Web/嵌入资产交付。HTTP/2、代理、真实外部身份/存储/KMS、PostgreSQL/Qdrant、生产、压力/RSS、阻塞I/O中断、登录后浏览器E2E均未验证。不提交、推送、部署或写外部系统；不再启动下一方向。
