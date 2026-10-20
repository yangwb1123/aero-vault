# REST 多行读取条件头 v1

## 证据与范围

通过真实 API Key、共享 `server.ApplyMiddleware` 12 环、SQLite/local 与版本化 Blob，发送多个同名 HTTP 字段行，复现：

- If-Match 首行不匹配、后行匹配时返回错误 412；If-None-Match 后行命中时仍返回 200。
- If-Range 重复（即使两行相同）仍按首行产生 206/416；重复 Range 也按首行切分。日期条件重复时首行误触发 304/412。
- malformed wildcard/tag 混合或损坏后续成员仍按首行判断；空字段行遮蔽后续有效 ETag。既有单行 parser 无须改写，问题位于取 HTTP 字段值的边界。
- 派生缩略图同样出现多行 If-Match/If-None-Match 与日期问题，必须保留其二次身份/授权观察、generation 绑定及缓存门禁。

首轮 7 项内容条件回归与 2 项派生缩略图回归失败；首次 Deny 优先于条件的正向防回归已通过。日志 `/tmp/aero-repeated-read-headers-red.log`、`/tmp/aero-repeated-thumbnail-headers-red.log` 保留。

只修 `/v1/files` GET/HEAD 的 raw current/historical content 与派生缩略图读取边界，不改写入前置条件、`/share`/公开资源适配器、S3/WebDAV、鉴权/权限模型、Service/Storage/Repository/AI 接口、SDK、迁移、配置、依赖或门禁预算。保留全部当前工作区，不提交或部署。

## 契约与最小方案

- If-Match/If-None-Match 为 ETag 列表：按物理字段顺序合并所有非空字段行，再走既有实体标签语法和 strong/weak 比较；不直接按逗号切割 opaque tag。空字段行不遮蔽其他行，原始 header 不原地修改。
- malformed 合并列表不取首个合法片段：If-Match 按现有无有效匹配语义返回 412；If-None-Match 不命中，也不改用 If-Modified-Since。字段存在性与非空值分开判断；只含空值的 If-None-Match 同样不能启用日期 fallback。
- If-Match 优先于 If-Unmodified-Since，If-None-Match 优先于 If-Modified-Since。重复日期字段不构成日期列表，忽略该无效日期条件；不选择其中一行。
- If-Range 必须恰好一个非空有效字段，匹配单个 strong ETag 或精确 Last-Modified；重复/空/非法时忽略 Range，在同一已打开且授权的对象/版本流上返回完整正文。完全没有 If-Range 则保持既有 Range 行为。
- 重复 Range 字段不挑首行，忽略 Range 并返回完整选中内容。单字段多范围仍保持既有仅取第一段的兼容能力，不新增 multipart/byteranges。HEAD 继续忽略 Range，不开内容 Blob。后续共享数字解析补强见 [`range-decimal-boundaries-v1.spec.md`](range-decimal-boundaries-v1.spec.md)，不改变此处重复字段取值规则。
- 缩略图的 ETag 条件与日期判断复用相同字段读取规则；其 304 二次 Stat/授权、generation 证明、cache/admission/decode/取消/编码与脱敏行为保持不变。
- 解析不增加授权旁路：Deny/provider/租户/policy/损坏/删除错误仍先终止；条件重新观察实际打开源、pin、响应元数据及单次关闭不变。不承诺数据库/Blob 原子快照或阻塞 Read 中断。

## 验证计划

实际发送多行头而非逗号字符串模拟，覆盖当前/历史、旧精确 thumbnail Key 与显式 content、GET/HEAD、strong/weak/列表语法、空字段、日期优先级、重复 Range/If-Range、精确源与关闭次数、Deny，以及有真实 generation 证明的旧/显式派生缩略图。重跑先前内容/版本/条件/HEAD/取消/匿名公读/预签名/租户/policy 回归。

执行定向普通/race、`make check` + `python3 cli.py accept` 和前端交付顺序 `make web-check` → `make build` → `make web-smoke`。UI 冒烟仅验证登录壳与资产；外部代理/HTTP2 合并、真实存储/身份、实库、分布式及整仓 race 另行验证。

## 本轮实际验证

- 新增 16 项回归通过（15 项 HTTP、1 项 parser 边界单测），包括空 If-None-Match 的日期抑制、空 If-Range、单字段第一段 Range 兼容、IUS 先于 INM、带逗号 opaque tag 与请求头不变性；覆盖 raw 当前/历史及旧/显式派生缩略图。首轮失败证据保留。
- 实现中清理移除日期分支后未使用的 time 导入；空字段 fixture 曾把命中 INM 与过期 IUS 组合而错误期待 304，已修为各自的日期优先级，并新增独立 IUS→INM 的 412 回归，不改变正确条件顺序或跳过测试。相关日志保留。
- 新 `read_condition_headers.go` 仅提供读端多值/单值取值；`conditional.go`、`content_range.go` 与 thumbnail 的条件观察复用。未改写入前置条件、共享 ETag parser、外部协议/公开资源适配器或鉴权链。OpenAPI 与 API 文档同步。
- 使用 `GOMAXPROCS=4 GOFLAGS=-p=1`：REST/service 定向普通通过（142.133 秒、2.013 秒）；同范围定向 race 通过（237.364 秒、2.964 秒），包预算保持 300 秒。覆盖此前元数据、错误终态、条件/Range、HEAD、pin/取消、完整 Key、匿名公读、预签名、policy/租户/授权回归。
- `make check` 与 `python3 cli.py accept` 均 exit 0；完整普通测试及既有 metadata、thumbnail short、REST cache race 均执行，部分未改包正常使用缓存。未另跑整仓 `make test-race` 或真实 PostgreSQL/Qdrant。
- 按顺序 `make web-check` → `make build` → `make web-smoke` 通过：60 个前端文件/251 项测试、4 项 Node 回归，两条 UI 登录壳、9 个成功响应与 2 项 bundle/style。嵌入 JS 仍为 `index-Z_F4LqoO.js`，未新增前端功能。
- 新读端取值函数最大 10 行/复杂度 3，条件/范围文件函数最大 30 行/复杂度 9，新测试文件最大 20 行/复杂度 9；thumbnail 只替换取值并缩减日期分支，未重构既有解码流水线。文件大小、I1–I6、`git diff --check` 通过。禁止路径、配置、依赖、预算未改，未提交、推送或部署。
