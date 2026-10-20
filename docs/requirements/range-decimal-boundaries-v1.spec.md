# Byte Range 十进制边界 v1

## 复现与最小范围

读取边界复查中，已有共享 `service.ParseByteRange` 用 `strconv.ParseInt` 解析 HTTP 十进制字段：

- `bytes=+1-3`、`bytes=1-+3`、`bytes=-+3` 被当有效 Range；`bytes=0--1`/`0--2` 的负结束值混入内部开放区间哨兵，错误返回 206。
- 超过 int64 的合法十进制结束位置/后缀长度被当语法错误并返回全文 200；超大起点未得到 416。空对象上的同类合法 Range 也错误返回 200。
- 首轮 4 项 parser 与 4 项原始内容 HTTP 测试失败，日志 `/tmp/aero-range-decimal-red.log` 保留。条件/HEAD 优先级、溢出后非法字符与首段/空白兼容正例已通过。

只改这一纯解析器，继续由原有适配器决定状态/条件/授权；已确认调用方为 REST 原始内容、REST 分享/公开资源与 S3 当前/历史读取，因此追加这些入口回归，不复制解析规则。不改端口/接口、SDK、迁移、AI、存储/数据库流程、配置、依赖、门禁或其他协议功能，不提交或部署。原工作区完整保留。

## 取舍与契约

- 数字只含 ASCII 0–9，拒绝数字自身的正负号、非 ASCII 数字和尾部垃圾；`-` 仅为 Range 分隔符或后缀语法。空白裁剪与单字段只取首个区间保持现有兼容，不新增 multipart/byteranges、后续段语法整体验证或新的 Range header 规范化。
- 使用 O(n) 扫描、O(1) 额外空间的饱和十进制累加，不引入 arbitrary precision。字段达到 int64 最大值后仍扫描剩余字符验证语法，不提前接受错误后缀；前导零不因文本长度被误判为溢出。
- 对所有可表示对象尺寸，超大起点必越界→416；超大结束值裁到 EOF；超大后缀覆盖整对象→206；空对象上的有效 Range→416。饱和值不与负数哨兵冲突，最大尺寸算术不溢出。对象 Size 数据类型不变，不声称支持超 int64 的对象。
- 保留原有未知单位/非法语法忽略 Range→完整响应，反向区间既有 416、零后缀既有忽略、单字段首段、重复字段/If-Range 的读端规则及 HEAD/304/412、Deny/provider/policy/租户优先级。不新增错误状态或声称拒绝读取授权之外的内容；此前返回全文本身不是权限绕过。
- 原始内容仍只切同一已打开授权流，精确 pin、metadata、单次关闭、取消与非 EOF 错误终态不变。S3/公开资源仅继承数字解析修复，不借此宣称已具备原始 REST 的同源、取消或流失败契约。

## 验证计划

parser 覆盖符号、溢出/长数字、前导零、溢出后垃圾、int64 最大尺寸与空对象；REST 原始内容覆盖当前/历史、旧精确 thumbnail Key/显式 content、字节/头/精确 StorageKey、206/416、HEAD/条件优先；共享调用方覆盖 S3 当前/历史及分享/公开图片，包含既有授权/撤销/停用回归。补空文件正常 GET/HEAD 与截断流既有终态的正反例，不扩大物理健康或完整校验承诺。

执行定向普通/race、`make check` + `python3 cli.py accept`，以及 `make web-check` → `make build` → `make web-smoke`。真实外部代理/HTTP2/身份/存储、实库、全仓 race、规模/RSS 与分布式专项不由这些本地测试证明。

## 本轮实际验证

- 新增 17 项回归通过：5 项 parser、5 项 raw HTTP + 1 项截断 span 单测、3 项公开入口 HTTP、3 项 S3 HTTP。首轮 4 项 parser/4 项 raw HTTP 失败证据保留，不把完整授权正文的错误 200 归为授权绕过。空文件无 Range 的 GET/HEAD 正常 200/零长度，合法超大 Range 返回 416；原截断流终态保持。
- raw HTTP 验证当前/历史、旧精确 thumbnail Key/显式 content 的字节、响应元数据、精确 StorageKey 与单次打开/关闭；超大起点 416 零 Read，HEAD/304/412 仍优先。分享/公开图片及 S3 当前/历史继承同一数字解析；超大起点 416 不消费一次性分享次数，后续正常读取仍按原规则消费。未改这些适配器的生产流程或权限模型。
- 生产行为变更仅 `internal/service/range.go`：三个数字入口共用 `parseRangeDecimal`，无第二套 parser、额外 I/O、预读或 arbitrary precision。API 文档和运行时 OpenAPI 同步，前轮多行字段取值、同源读取、取消与流错误边界回归保留。
- 使用 `GOMAXPROCS=4 GOFLAGS=-p=1`。service/REST/S3/middleware 定向普通分别 1.126/40.419/2.745/0.009 秒；同范围定向 race 分别 4.274/123.789/9.887/1.055 秒，300 秒包预算未改。包含 SigV4、SSE-C Range、bucket policy、分享/公开资源撤销/删除/停用、历史与原始内容读取回归。未另跑整仓 `make test-race` 或真实实库联测。
- `make check` 与 `python3 cli.py accept` 均 exit 0：fmt/vet/integration 类型检查、构建、全仓普通测试及已有 metadata、thumbnail short、REST cache race 通过；部分未改包正常缓存。未用旧验收记录代替本轮执行。
- 前端交付依序通过 `make web-check` → `make build` → `make web-smoke`：60 文件/251 项前端与 4 项 Node；两条 UI 登录壳、9 成功响应、2 嵌入资产。JS 仍 `index-Z_F4LqoO.js`，无新 UI 功能，冒烟不证明登录后下载或外部 E2E。
- parser 文件最大函数 31 行/复杂度 9，新测试最大 23 行/复杂度 9；文件大小、I1–I6、`git diff --check` 通过。禁止接口/目录、配置、依赖、阈值与预算均无差异。未提交、推送或部署。
