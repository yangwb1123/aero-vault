# 已打开对象流的条件与 Range 响应 v1

## 证据与范围

SQLite/local 的版本化 Blob fixture 在第一次 Stat 返回旧元数据、随后 Get 返回当前真实行时复现：

- Range 正文为新内容，ETag/Content-Type/Last-Modified/Content-Range 总长度却来自旧 Stat。
- 旧 size 使本应 206 的请求变成 416，或本应 416 的请求在跳过前缀时变成 EOF/500。
- 旧 If-Match/If-Unmodified-Since 检查放行新正文；If-None-Match 对实际打开对象命中时仍返回 200。
- 普通 GET 忽略 If-Range，包括不匹配 ETag、weak tag、无效 token 与旧日期。

本轮仅修 REST 内容读取适配器与回归，复用已有历史范围、条件解析及 Read 前后取消检查。不改 FileService/Storage/Repository 接口、SDK、数据库、配置、依赖或对象写入；不提交或部署。

## 方案与不变量

- 保留当前条件 GET 的 Stat 预检：错误仍是当次请求终态；可在该次授权元数据观察上完成无正文的 304/412，不为它们强制打开 Blob。
- 需要正文时仍由 FileService.Get 正常取行、检查授权并打开源；拿到其返回的对象元数据后重新检查条件，之后的 200/206/416、验证器、类型、长度均使用这份元数据，而非预检结果。
- 当前与历史 Range 复用已经打开的同一流。HTTP 适配器不再以 GetRange 重新定位当前对象；条件或 If-Range 判定也不额外打开另一条流。
- If-Range 只接受匹配的单个 strong ETag 或精确 Last-Modified 日期；不匹配、weak、`*` 或非法值忽略 Range，返回所选流的完整正文，不回退其他对象/版本。后续补强多行/空值条件与重复 Range 的取值边界，见 [`repeated-read-condition-headers-v1.spec.md`](repeated-read-condition-headers-v1.spec.md)。
- 共用范围/取消原语，不改变历史非空 pin、不额外暴露当前对象的内部 Version ID；当轮保留历史与当前既有响应头差异，后续附件/元数据头一致性补强见 [`content-read-response-headers-v1.spec.md`](content-read-response-headers-v1.spec.md)。
- 原始流仍由打开它的 handler 关闭一次。Read 前后检查取消，观察到取消不交付该次结果、不继续下一次 Read；已经阻塞的底层 Read、已发送字节与打开/访问事实不可撤回。
- 前缀跳过仍可能顺序读取；存储/加密实现仍可能整对象缓冲。此修复不提供非版本化覆盖时数据库行与 Blob 的 generation 原子证明，也不承诺读取结束时的“最新”对象、CAS、物理完整性或分布式快照。

## 验证

`opened_content_response_test.go` 用原有 repository/storage 端口的测试包装器、真实版本化 Blob 和显式 Principal，在预检/打开之间给出不同元数据；记录精确 StorageKey、打开/读取/关闭次数，区分元数据检查与实际正文。

先运行红灯回归，再验证新旧条件请求、当前/历史 Range、If-Range、HEAD、完整 Key、授权/Deny/provider/租户/policy/预签名及取消。最终执行 `make check` + `python3 cli.py accept`、定向 race 与前端交付顺序；不放宽门禁或删减失败测试，原失败日志保留。浏览器冒烟仍仅验证登录壳与资源加载。

## 本轮实际验证

- 五项初始场景均复现失败，日志 `/tmp/aero-opened-content-red.log` 保留：新正文带旧验证器/类型/长度、旧条件放行正文、旧 size 的 416/500、普通 If-Range 被忽略。后续新增到 9 项 HTTP 回归，包含元数据-only 快路径、条件先于 Range、当前读取取消与打开后零 Read；各已打开源精确匹配当前 StorageKey，并恰好关闭一次。
- 历史 Range 原语转入 `content_range.go`，`content_response.go` 统一当前/历史已打开流；响应所需的 metadata、If-Range 和切分不追加当前对象查找。Service、Storage、Repository 端口和 SDK 未改。
- REST/service 定向普通测试通过（31.347 秒、0.630 秒）；同范围定向 race 通过（68.763 秒、2.375 秒），包预算 300 秒，含旧条件请求、Range、HEAD、历史 pin/取消、完整 Key、policy/Deny/provider/租户/预签名/读取授权回归。
- `GOMAXPROCS=4 GOFLAGS=-p=1 make check` 与同配置 `python3 cli.py accept` 均 exit 0。完整普通测试及既有 metadata、thumbnail short、REST thumbnail cache race 均执行；未改包部分正常使用缓存，未另跑整仓 `make test-race`、真实 PostgreSQL/Qdrant 或外部存储。
- 按顺序 `make web-check` → `make build` → `make web-smoke` 通过：60 个前端文件/251 项测试、4 项原生 Node 回归；默认 Chrome/Node 两条 UI 登录壳、9 个成功响应与 2 项 bundle/style。嵌入 JS 仍为 `index-Z_F4LqoO.js`，未新增前端功能。
- 新增/复用的范围/响应文件函数最大 25 行/复杂度 6，新测试文件最大 22 行/复杂度 6；入口文件 AST 检查符合 ≤50 行、≤10 约定。文件大小、I1–I6、`git diff --check` 通过；预算、依赖及禁止路径未改，未提交、推送或部署。
