# 内容读取响应头一致性 v1

## 证据与范围

在真实 SQLite/local 版本化 Blob、API Key 与共享 12 环中间件中复现：

1. 历史 GET 的 200/206 以及 If-Range 不匹配后的完整正文丢失已保存的 `Content-Disposition`、`X-Content-MD5` 和非默认 `X-Storage-Class`，但精确历史 HEAD 已返回这些字段。声明为 attachment 的 HTML 历史字节也能复现 disposition 丢失；不将此仅描述为外观差异，也不据此声称已完成浏览器漏洞利用验证。
2. 当前 HEAD 304、历史 GET/HEAD 304 缺少当前 GET 304 已携带的用户元数据、disposition、摘要或存储类别，产生不同的元数据响应。
3. gzip 历史字节/编码保持正确，但附件声明、摘要和存储类别同样遗漏；测试显式禁用 HTTP 客户端隐式 gzip 解码，避免把客户端转换误认成服务端差异。

四项初始回归失败；拒绝响应不外泄元数据的正向防回归已通过。红灯日志 `/tmp/aero-content-read-headers-red.log` 保留。

仅调整 REST 内容响应头生成及回归，不新增读取接口、selector、配置、依赖、权限模型或领域服务，不修改 Storage/Repository/AI 接口、SDK、迁移或字节存储。保留当前未提交工作区，不提交或部署。

## 契约

- 200/206/304 与 HEAD 的对象元数据头来自该路径已授权的同一份对象/版本，不重新查找当前对象。包括 ETag、Last-Modified、用户元数据、已保存的 disposition/encoding、对象摘要与非默认存储类别。
- 200/206 只在适当位置设置 Content-Type、Accept-Ranges 与响应长度；206 长度仍为选中片段。`X-Content-MD5` 描述完整选中对象的存储摘要，不是新计算的片段摘要或独立的读校验成功证明。
- 非空历史 pin 保留 `X-Version-Id`；普通当前读取不额外暴露内部版本 ID。历史字段不得取自新版本；`_aero_*` 仍不作为用户元数据头序列化，只按既有受控字段映射。
- 304 无响应体，不新增 Blob 打开；当前 metadata-only 快路径和历史 GET 原有的打开/关闭流程保持。内容 HEAD 继续仅用元数据，忽略 Range；派生缩略图 HEAD 行为不在本轮变更范围。
- 授权、租户、policy、损坏/删除及前置条件拒绝仍在内容头之前处理；错误不返回附件、摘要、类别、用户元数据或内容验证器。不改变能力签名、匿名公读、取消或错误终态。
- 存储类别只是已有声明，不证明真实冷迁移；纯元数据 HEAD 不证明 Blob 存在/可解密。此变更也不提供数据库/Blob 原子快照或阻塞 Read 中断能力。

## 验证计划

`content_read_headers_test.go` 覆盖普通/thumbnail 后缀完整 Key、旧与显式 content 路径、不同当前/历史元数据、GET/HEAD、Range/If-Range、304、gzip 历史附件、精确 StorageKey、单次关闭和拒绝不泄漏。原有版本/完整 Key/错误终态/匿名公读/租户/授权/取消回归继续执行。

运行定向普通与 race、`make check` + `python3 cli.py accept`，以及 `make web-check` → `make build` → `make web-smoke`；不修改范围、阈值、预算或跳过规则。UI 冒烟只证明登录壳与嵌入资产，不替代登录后文件读取、真实外部存储、实库或分布式验证。

## 本轮实际验证

- 初始四项元数据/附件场景均失败，首次拒绝不泄漏的正向防回归已通过，红灯日志保留。修复后新增 7 项回归通过（6 项 HTTP、1 项运行时 OpenAPI），另覆盖 412/416 不返回内容元数据；完整/片段源精确匹配选中 StorageKey 并关闭一次，HEAD 不新增打开，当前 metadata-only 304 与历史 GET 原有打开次数保持。
- `content_headers.go` 统一内容响应的身份/元数据头；`file_head.go`、`handler_helpers.go` 与 `management.go` 复用。`content_range.go` 只保留已打开流切分/取消原语，不为头字段重新查询当前对象。OpenAPI 与 `docs/api.md` 同步。
- 使用 `GOMAXPROCS=4 GOFLAGS=-p=1`：REST/service 定向普通通过（28.269 秒、0.437 秒）；同范围 race 通过（131.512 秒、4.174 秒），包预算 300 秒，覆盖上一轮拒绝终态、已打开流条件/Range、版本 pin、完整 Key、HEAD、匿名公读、policy/租户/授权与取消。
- `make check` 和 `python3 cli.py accept` 均 exit 0，完整普通测试及既有 metadata、thumbnail short、REST cache race 均执行；部分未改包正常使用缓存。未另跑整仓 `make test-race`、真实 PostgreSQL/Qdrant 或外部存储。
- 依序 `make web-check` → `make build` → `make web-smoke` 通过：60 个前端文件/251 项测试、4 项 Node 回归；两条 UI 登录壳、9 个成功响应、2 项 bundle/style。嵌入 JS 仍为 `index-Z_F4LqoO.js`，无前端功能变更。
- 新响应头文件函数最大 11 行/复杂度 2，修改的范围/HEAD/版本/注册文件最大 25 行/复杂度 6，新测试最大 22 行/复杂度 10；文件大小、I1–I6、`git diff --check` 通过。禁止路径、配置、依赖与门禁预算无改动，未提交、推送或部署。
