# 缩略图空实体标签与通配符区分 v1

## 必要性与限定范围

槽位009最初发现的线索已独立HTTP复现：REST 缩略图以 `etagListMatches(inm, "")` 识别 If-None-Match 通配符；共享弱比较允许空 opaque 实体标签匹配空比较值，导致 `""`、`W/""` 或含它们的非匹配列表被误判为 `*`。真实SQLite/local、API Key、共享12环中间件下，120个当前/历史、GET/HEAD、旧/显式、冷/热及无generation证明场景全部错误返回304，而不是200。故障是条件匹配错误，不是权限绕过或生产可利用性证明；红灯保留。

仅限 REST 派生缩略图的通配符识别，当前/精确历史、GET/HEAD、旧/显式入口共用一处最小判定。保留共享 ETag 解析器、原始内容、写入前置条件、授权、缓存/flight/admission/取消/解码/关闭及事件模型；不扩展其他协议、SDK、配置、依赖、预算、产品能力或001–008范围。复用已验收夹具，不将其优化重复计为新增。

## 有界验收

- 空 opaque 标签 `""`、弱标签 `W/""` 与包含它们的列表不是通配符；没有实际匹配派生 ETag 时正常生成/命中200，不错误认证304。If-None-Match 存在仍抑制日期 fallback。
- 只有合并字段去除HTTP OWS后恰为 `*` 才启用既有通配符语义。真正匹配的强/弱标签列表、多行列表、合法 `*` 与 metadata-only304保持；混合 wildcard/tag 的非法列表不挑合法片段。
- 无 generation 证明仍无可复用 ETag/版本且 no-store；非匹配空标签不得因此成为存在性匹配，真正 `*` 仍保留既有授权二次Stat304，不新增源打开。
- 预检授权/对象状态与强前提条件优先。miss仍由FileService再授权并关闭同一精确源一次；resident命中不打开。200保留一次访问尝试及success，非匹配请求不增加304；不新增成功下载审计或任意交错最新性保证。

## 红灯与验证计划

以真实SQLite迁移副本、local版本Blob/generation、API Key、共享12环中间件及HTTP/1验证；仅复用已有fixture，不改旧断言。先保留未修生产代码下的红灯，再最小修改判定。覆盖冷/热、当前/历史、GET/HEAD、旧/显式入口，检查JPEG/源身份、一次Close、响应元数据与计数；追加真正wildcard/匹配列表、无proof、拒绝/强条件对照及运行时OpenAPI。

Go命令固定 `GOMAXPROCS=4 GOFLAGS=-p=1`，每包300秒；本槽自行执行有辨别力的受影响普通/race，完整六项门禁与完整 `make test-race` 等待返回后的机械阶段，必须同源后独立验收。未执行项不预填成功；HTTP/2、代理、真实外部身份/存储/KMS、实库、生产、压力/RSS、阻塞I/O及登录后浏览器E2E不在本槽证明范围。

## 实际结果

证据目录：`/home/u1/.abr/projects/aero-vault-5c281c8a1991a53d/runs/implementation-24h-sol-max-20261015T070122Z/evidence/009-local/`。每项检查有独立 `.command/.exit/.log`，后续验证另记 `commands.jsonl`；测试/构建子进程umask022、私有日志077。

- `red.log` exit 1：`go test -count=1 -timeout 300s ./internal/api/rest/ -run '^TestThumbnailEmptyTagNonmatchingUsesSelectedSource$' -v`，120场景均记录错误304；不修改旧生产逻辑就能复现。最小修复后完全相同命令的 `green.log` exit 0（1.002秒）。
- 生产行为仅 `thumbnail.go`：用 `strings.Trim(inm, " \t") == "*"` 识别真正通配符，新增标准库strings导入与说明。共享实体标签解析器、其他生产逻辑、原断言和008取消检查不变；不新增Stat、打开、重试或同步机制。
- 新增7项顶层回归：120场景的JPEG/元数据/精确StorageKey及Close/缓存/事件/成功计数；合法wildcard/OWS/空字段+wildcard与真正匹配的强弱/多行列表保持metadata-only304；quoted星号和非法混合列表不当wildcard；强前提412与Deny/provider故障优先于热缓存；原始当前/历史、旧精确thumbnail Key/显式content的空标签条件保持200，HEAD不开源；运行时OpenAPI说明同步。无proof的非匹配请求仍no-store、无ETag/版本，真正wildcard保留原304。
- `red-spec.log` exit 1明确显示三个既有读取operation缺少空标签说明（旧缩略图operation未单列304）；只补 `file_read_spec.go`、`router.go` 的响应说明与 `docs/api.md`，不改route/scope。补齐后 `go test -count=1 -timeout 300s ./internal/api/rest/ -run '^TestThumbnailEmptyTag' -v`：`focused.log` exit 0，7项全部通过，1.602秒。
- 所有Go命令固定 `GOMAXPROCS=4 GOFLAGS=-p=1`。普通整包 `go test -count=1 -timeout 300s ./internal/api/rest/ ./internal/service/ ./internal/middleware/ -v`：`ordinary.log` exit 0，分别72.213 / 26.316 / 0.590秒。
- 完整受影响REST包 `go test -race -count=1 -timeout 300s ./internal/api/rest/ -v`：`race-rest.log` exit 0，212.167秒，无-short/-run；不把它冒充整仓make test-race。新增7项 `go test -race -count=3 -timeout 300s ./internal/api/rest/ -run '^TestThumbnailEmptyTag' -v`：`race-focused-repeat.log` exit 0，21次顶层测试通过，44.262秒。三组验证期间所有Go源码SHA保持，未改变包预算。
- 三包go vet、全树gofmt、filesize、I1–I6、git diff --check、新文件函数行数与gocyclo检查均exit 0。新文件132/190行，最大函数28行，复杂度≤10；原长thumbnailDerive只改局部布尔判定，不宣称整体重构达标。
- 本槽完整 `make check`、`python3 cli.py accept`、`make web-check` → `make build` → `make web-smoke`、机械阶段的git diff --check与整仓 `GOMAXPROCS=4 GOFLAGS=-p=1 make test-race` 仍待runner实际执行并同源独立验收，没有预填PASS。本槽无Web/嵌入资产变更；008及既有fixture优化不是本槽新交付。
- 本地确定性HTTP/1结果不证明任意交错最新性、metadata/Blob原子快照或成功下载审计。未验证HTTP/2/代理、真实外部身份/存储/KMS、PostgreSQL/Qdrant、生产、压力/RSS、阻塞I/O中断或登录后浏览器E2E。保留原工作区与历史FAIL/超时/中断证据，不提交、推送、部署或写外部系统。
