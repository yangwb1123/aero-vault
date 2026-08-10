# 方向：MaxBodySize 静默截断超大 chunked 上传，以 200 OK 存储损坏对象

> **模块：** `internal/middleware`（+ `internal/service` 写路径联动、`internal/api/s3compat`/`internal/api/rest` 错误映射） · **来源分析：** `docs/auto/analyses/internal-middleware-697499e2.json`（方向 1/3） · **日期：** 2026-08-07
> **评分：** 价值 9 / 风险降低 8 / 工作量 4 / 置信度 9
> **本文所有代码引用均已对照仓库逐条验证**（行号以当前工作树为准；方向引用的两处行号漂移已在证据表中修正）。
>
> **范围纪律：** 本规格**只**做方向验收覆盖的四件事：① 中间件在截断点返回可辨识错误（非干净 EOF）的哨兵；② 适配器（S3 + REST 写路径）将哨兵映射为 413；③ 中间件单元测试（chunked 超限 → 非 EOF 错误、恰好 maxBytes 无 off-by-one）；④ S3 chunked PUT 集成检查（4xx + 不留对象/blob）。**不改** MaxBodySize 的已知 Content-Length 早拒语义、不改 `APP_MAX_BODY_SIZE` 默认值、不修 WebDAV/MCP 读体路径、不引入断言框架（I6）。

---

## 1. 问题陈述

`MaxBodySize`（`internal/middleware/validation.go`）在 `Content-Length` 已知且超限时早拒 413（:23-27），否则用 `io.LimitReader` 包裹请求体（:31）。`io.LimitReader` 的语义是**在达到上限处返回干净的 `io.EOF`**——它无法区分"身体恰好到这里结束"和"身体更长但被截断"。

当客户端使用 **chunked Transfer-Encoding**（`ContentLength == -1`，S3 网关挂在 12 环链内，SDK 流式上传/未知长度上传均走此路径）时：

1. `r.ContentLength > maxBytes` 早拒**永不触发**（-1 > maxBytes 为假）；
2. 包裹后的 body 在第 `maxBytes` 字节后干净 EOF；
3. S3 适配器把 `r.Body` 与 `r.ContentLength`（=-1）直接传入 `FileService.Put`（handler.go:105）；
4. `materializeUnknownSize`（usage.go:106-134）走 spool 分支，`io.Copy` 读到 EOF，**把截断后的字节数当作真实对象大小**返回；
5. 随后 `validateStoredSize`（file_crud.go:173/:191）比较的是 `expected(截断数) == read(截断数) == stored(截断数)`——**恒等，守卫形同虚设**。`ErrSizeMismatch` 的定义本身就是 "actual bytes differ from Content-Length"（file.go:38），只对已知长度有效；
6. 服务端存储截断 blob、记截断大小入配额、返回 **200 + ETag**。

**后果：静默数据损坏。** 客户端上传超过 `APP_MAX_BODY_SIZE` 的 chunked 对象，收到 200/ETag 认为成功，实际落库的是被截断的损坏对象；配额按截断字节数计费（少计）。修复前，任何设置了 `APP_MAX_BODY_SIZE` 的部署都暴露于此缺陷（默认 0 = 不限，缺陷默认潜伏，I5 opt-in 语义）。

### 触发场景（真实回归）

1. 运维设置 `APP_MAX_BODY_SIZE=10MiB` 防大对象攻击 → S3 SDK 以 `Transfer-Encoding: chunked` 上传 12MiB 对象 → 服务器静默存 10MiB 截断对象并回 200/ETag；客户端下次 GET 得到损坏数据，无任何错误痕迹。
2. curl `-H 'Transfer-Encoding: chunked'` 或任意未知长度 HTTP 客户端上传超限对象 → 同上；`/v1/files` REST PUT 同样受影响（rest 适配器同走 `svc.Put`）。
3. 对照：已知 Content-Length 的客户端（`bytes.Reader` 类）→ 命中 :23-27 早拒 413，**路径正确**——这正是现有测试全部绿色、缺陷未被发现的原因（E11）。

---

## 2. 现状与代码证据（已逐条验证）

| # | 证据 | 验证结果 |
|---|------|---------|
| E1 | `internal/middleware/validation.go:29-31` — 注释 "silently stops at maxBytes" + :31 `r.Body = io.NopCloser(io.LimitReader(r.Body, maxBytes))`（方向引用 :29/:31，精确） | ✅ 与引用一致；`io.LimitReader` 在 cap 处返回干净 EOF，**截断与正常结束不可区分** |
| E2 | `internal/middleware/validation.go:23-27` — 早拒条件 `r.ContentLength > maxBytes`；`ContentLength == -1`（chunked）永不命中 | ✅ 补充验证（早拒只覆盖已知长度；chunked 走包裹路径） |
| E3 | `internal/service/usage.go:108` — `materializeUnknownSize`（:106）内 `if size >= 0` 分支；:122 `written, err := io.Copy(tmp, reader)`——未知长度时 io.Copy 到 EOF，**截断字节数即返回的 size**；:126-128 err → cleanup + `%w` 包裹 | ✅ 与引用一致（:108/:122 精确）；spool 失败路径已 `%w` 包裹，哨兵可经 `errors.Is` 穿透 |
| E4 | `internal/api/s3compat/handler.go:99` — 方向引用 :99；实际 `h.svc.Put(r.Context(), mw.TenantFrom(r.Context()), bucket, key, r.Body, r.ContentLength, opts)` 在 **:105**（PutOptions 起于 :92）；成功路径 :106-110 回 `ETag` + **200** | ✅ 语义一致（行号漂移 +6）；S3 适配器把 body 与 `ContentLength`（chunked 时 = -1）原样交给 FileService |
| E5 | `cmd/server/http.go:126` — **已漂移**：MaxBodySize 环现位于 `internal/server/chain.go:74` `{RingMaxBody, middleware.MaxBodySize(int64(cfg.App.MaxBodySize))}`；S3 网关挂载 `cmd/server/http.go:127` `r.Mount(cfg.S3Compat.Prefix, s3compat.NewRouter(...))`；链装配 `cmd/server/main.go:166` `finalHandler := server.ApplyMiddleware(dispatcher, ...)` | ✅ 语义一致（原 applyMiddleware 已被 internal/server 抽取）；**S3 网关位于 12 环链内，max_body 环包裹 S3 PUT** |
| E6 | `internal/service/file_crud.go:108/:153-154/:161/:173/:191` — Put 流：`materializeUnknownSize(r, size)` → `countingReader` → `store.Put(ctx, sk, r, size, ...)` → `validateStoredSize(ctx, sk, size, sizeReader.total, info.Size)` | ✅ 补充验证；spool 后 `size == read == stored`（皆截断数），**守卫恒通过**，损坏对象落库 |
| E7 | `internal/service/file.go:38` — `ErrSizeMismatch = errors.New("size mismatch: actual bytes differ from Content-Length")` | ✅ 补充验证；守卫按构造只对已知 Content-Length 有效（E6 机理的根） |
| E8 | 全仓 grep `ErrBodyTooLarge` / `MaxBytesError`（internal/ 非测试代码）**零命中** | ✅ 补充验证；当前无任何可辨识的"超限截断"错误，需新引入 |
| E9 | 适配器错误映射：`internal/api/s3compat/errors.go:110` `{service.ErrSizeMismatch, "IncompleteBody"}`（400）、:122-130 `s3ErrorCode` 未知错误 → `"InternalError"` → 500；`internal/api/rest/handler_helpers.go:49-51` `ErrSizeMismatch` → 400 `"SizeMismatch"`，default → 500 | ✅ 补充验证；**新哨兵必须显式映射到两个适配器，否则以 500 泄漏** |
| E10 | `internal/config/config.go:48/:83` — `MaxBodySize int // APP_MAX_BODY_SIZE; max request body bytes (0 = unlimited)`，默认 0 → validation.go:17-19 透传 | ✅ 补充验证；缺陷仅在显式配置时激活（I5） |
| E11 | 既有测试：`TestMaxBodySize_ExceedsLimit`（validation_test.go:42-52，`bytes.Reader` 已知长度）、`TestMaxBodySize_UnderLimit`（:26-40）、`TestMaxBodySize_ExceedsViaContentLength`（:54-65，只覆盖早拒路径）——**无任何 `ContentLength == -1` 用例**；`httptest.NewRequest` 自动按 reader 设长度，须显式改 `req.ContentLength = -1` 才能模拟 chunked | ✅ 补充验证（测试盲区即缺陷未被发现的原因） |
| E12 | 集成 harness：`internal/integration/fullserver_test.go:147` 无条件挂 `/s3`（`s3compat.NewRouter(svc, logger, nil)`）、:166 `server.ApplyMiddleware(dispatcher, repo, authReg, rl, cfg, ...)`（12 环链生效）、:84-90 `startFullServerWithConfig(t, relayOpts, authKeys, cfg)` 已存在；既有 `TestFullServer_MaxBodySize413`（middleware_chain_test.go:22-58）只覆盖已知长度路径 | ✅ 补充验证（AC-4 装具齐全，零新增装配） |
| E13 | import 图：`internal/service` 已 import `internal/middleware`（file.go:12、access.go:10、file_delete.go:10）→ **middleware 不得 import service（成环）**；哨兵放 middleware 包，`rest`/`s3compat` 均已 import middleware（别名 `mw`）可用 `errors.Is` 映射；service 侧零改动（E3 的 `%w` 已穿透） | ✅ 补充验证（FR-1 落点与依赖方向约束） |

### 缺陷机理

```
chunked PUT (ContentLength == -1)
  → max_body 环：早拒不触发（E2），io.LimitReader 包裹（E1）
  → S3 PutObject: svc.Put(r.Body, -1)（E4）
  → materializeUnknownSize: io.Copy 到 EOF，size := 截断字节数（E3）
  → store.Put 落截断 blob → validateStoredSize(截断==截断==截断) 恒过（E6/E7）
  → 200 + ETag（E4）—— 客户端认为成功，对象已损坏
根因：io.LimitReader 的干净 EOF 把"超限截断"伪装成"正常结束"，且错误在
      中间件层就信息全失；下游所有守卫都基于长度恒等，无法自证。
```

---

## 3. 需求规格

### FR-1：MaxBodySize 在截断点返回可辨识哨兵错误（`ErrBodyTooLarge`）

`internal/middleware/validation.go` 中：

1. 新增包级哨兵：

```go
// ErrBodyTooLarge is returned by the wrapped request body reader when a
// chunked/unknown-length body exceeds maxBytes. Unlike a clean EOF it is
// distinguishable from a body that ends exactly at the limit; adapters map
// it to 413 (RequestEntityTooLarge).
var ErrBodyTooLarge = errors.New("request body exceeds maximum allowed size")
```

2. **替换** :31 的 `io.LimitReader` 为同语义的限长读取器（下述 `limitErrReader`，或行为等价实现）——不再干净 EOF 收场，而是在**恰好越过 maxBytes 的那个字节**上返回 `ErrBodyTooLarge`：

- **约束 a（无 off-by-one）：** body 长度 `< maxBytes` → 行为与现状逐字节一致（数据透传，末尾 `io.EOF`）；body 长度 `== maxBytes` → 完整读出 maxBytes 字节，末尾**干净 `io.EOF`，无错误**（必须"偷读"第 maxBytes+1 个字节来区分：底层有字节 → 返回 `ErrBodyTooLarge`；底层 EOF → 返回 EOF）。
- **约束 b（已知长度早拒不变）：** :23-27 的 `r.ContentLength > maxBytes` → 413 + `Connection: close` 分支原样保留（已知长度请求不进入包裹路径；包裹路径只可能被 chunked/未知长度请求触达）。
- **约束 c（`maxBytes <= 0` 透传不变）：** :17-19 零成本透传分支不动。
- **约束 d（依赖方向，E13）：** 哨兵定义在 middleware 包；middleware **不** import service（防环）。service 无需改动——`materializeUnknownSize` 已 `%w` 包裹 spool 错误（E3），`errors.Is(err, middleware.ErrBodyTooLarge)` 全程可穿透。

参考实现形态（行为契约，允许等价实现）：

```go
type limitErrReader struct {
    r     io.Reader
    limit int64
    n     int64
}

func (l *limitErrReader) Read(p []byte) (int, error) {
    if l.n >= l.limit {
        var one [1]byte
        n, err := l.r.Read(one[:])
        if n > 0 {
            return 0, ErrBodyTooLarge // 偷读到第 maxBytes+1 个字节：截断，丢弃
        }
        return 0, err // 底层 EOF：body 恰好 maxBytes，干净结束
    }
    if int64(len(p)) > l.limit-l.n {
        p = p[:l.limit-l.n]
    }
    n, err := l.r.Read(p)
    l.n += int64(n)
    return n, err
}
```

### FR-2：写路径适配器将哨兵映射为 413（S3 + REST）

- **S3（`internal/api/s3compat/errors.go`）：** `errToS3Code` 表（:104-120 形态）新增 `{middleware.ErrBodyTooLarge, "EntityTooLarge"}`（AWS 语义命名）；`s3CodeStatus`（:45-66）新增 `"EntityTooLarge": http.StatusRequestEntityTooLarge`（413）；`s3CodeMessage` 补对应文案。效果：`writeS3Error`（:13-25）→ `classify`（:139-147）→ 413。
- **REST（`internal/api/rest/handler_helpers.go`）：** `classify`（:29-58）新增 `case errors.Is(err, middleware.ErrBodyTooLarge): return "BodyTooLarge", err.Error(), http.StatusRequestEntityTooLarge`。
- **约束 a：** 两个映射缺一不可——E9 证明未映射的哨兵以 500（`InternalError`）泄漏；REST `/v1/files` PUT 同走 `svc.Put`，chunked 超限同样触发哨兵。
- **约束 b：** 413 与既有已知长度早拒（validation.go:26）状态码一致，客户端可统一处理。

### FR-3：中间件单元测试（`internal/middleware/validation_test.go`）

新增三个测试（仅 `testing`，I6）：

1. **chunked 超限 → 非 EOF 错误：** `MaxBodySize(5)` + body "too long body"（12 字节）+ `req.ContentLength = -1`（模拟 chunked）→ 下游 handler `io.ReadAll(r.Body)` 得到错误：`err != nil`、`errors.Is(err, ErrBodyTooLarge)`、**`!errors.Is(err, io.EOF)`**。
2. **恰好 maxBytes 成功（off-by-one 阴性）：** `MaxBodySize(1024)` + body 恰 1024 字节 + `req.ContentLength = -1` → handler 读出完整 1024 字节、`err == nil`（或 io.EOF），响应 200。
3. **超限 1 字节即报错（off-by-one 阳性）：** `MaxBodySize(1024)` + body 1025 字节 + `req.ContentLength = -1` → 第一次读出 1024 字节无错，**下一次 Read 返回 `ErrBodyTooLarge`**（错误落在"第 maxBytes 字节之后"）。

（可选控制组：chunked 且 body 小于 maxBytes → 透传无错，锁定不回归。）

### FR-4：集成检查 —— S3 chunked PUT 超限 → 4xx 且不留对象/blob

`internal/integration/middleware_chain_test.go`（或 fullserver_test.go）新增：

1. `startFullServerWithConfig(t, &events.EventOutboxRelayOptions{}, "", &config.Config{App: config.AppConfig{MaxBodySize: 1024}})`（装具已具备，E12）。
2. 构造真实 chunked 请求：`http.NewRequest(PUT, ts.URL+"/s3/default/chunked-oversize.bin", io.NopCloser(strings.NewReader(<4096 字节>)))` + `req.ContentLength = -1` + `req.TransferEncoding = []string{"chunked"}` → `http.DefaultClient.Do`。
3. 断言：**status ∈ [400, 499]**（目标 413）。
4. **无对象残留：** `GET /s3/default/chunked-oversize.bin` → **404**（S3 `NoSuchKey`）；且经 harness 暴露的 `h.repo.GetObject(ctx, "default", "default", "chunked-oversize.bin")` → `repository.ErrNotFound`（file_crud.go 落库唯一入口 `writePutObject` 未执行；spool 临时文件已由 cleanup 删除，E3）。
5. 控制组：同 chunked 构造 body 512 字节 → 2xx；`GET` 回读 body 逐字节一致（证明 413 来自大小门而非其他环节，且正常 chunked 上传不受影响）。

---

## 4. 验收标准（可测试）

> 方向提供的 4 条验收全部保留，逐条映射为可执行断言。

### AC-1 chunked 超限请求令下游观察到非 EOF 错误（单元）

- `internal/middleware/validation_test.go` 新增测试：`MaxBodySize(5)` 包裹 handler，请求 `ContentLength == -1`、body 12 字节 → handler 内 `io.ReadAll(r.Body)` 返回错误且 **`errors.Is(err, ErrBodyTooLarge)` 为真、`errors.Is(err, io.EOF)` 为假**。
- 可证伪性：若实现退回 `io.LimitReader`（干净 EOF 收场），本测试的 `err != nil`/`errors.Is` 断言立即失败——旧行为下 `io.ReadAll` 返回 `(5, nil)`。

### AC-2 恰好 maxBytes 无 off-by-one（单元）

- 同一测试文件新增：`MaxBodySize(1024)` + `ContentLength == -1` + body 恰 1024 字节 → handler 完整读出 1024 字节且无 `ErrBodyTooLarge`；同配置 body 1025 字节 → 前 1024 字节读取无错、第 1025 字节的读取返回 `ErrBodyTooLarge`。
- 可证伪性：若限长实现是"读到 limit 即报错"（不偷读），恰好 maxBytes 的用例失败；若继续用 `io.LimitReader`，1025 用例失败（AC-1 同）。

### AC-3 既有测试全绿（回归门禁）

- `go test ./internal/middleware/` 通过，**包括**既有 `TestMaxBodySize_ExceedsLimit`（validation_test.go:42）与 `TestMaxBodySize_UnderLimit`（:26）——已知长度早拒与透传语义零回归（FR-1 约束 b/c）。
- 门禁命令：`go build ./...`、`go vet ./...`、`go test ./internal/middleware/ ./internal/integration/`。

### AC-4 S3 chunked PUT 超限 → 4xx 且无残留（集成）

- 构造：`startFullServerWithConfig(t, &events.EventOutboxRelayOptions{}, "", &config.Config{App: config.AppConfig{MaxBodySize: 1024}})`；`PUT /s3/default/chunked-oversize.bin`，body 4096 字节、`ContentLength = -1` + `Transfer-Encoding: chunked` → **status ∈ [400, 499]**（目标 413）。
- 残留断言：`GET /s3/default/chunked-oversize.bin` → **404**；`h.repo.GetObject(ctx, "default", "default", "chunked-oversize.bin")` → `repository.ErrNotFound`。
- 控制组：同构造 body 512 字节（chunked）→ **2xx**，GET 回读 body 与上传逐字节一致。
- 可证伪性：**当前缺陷代码上本测试必然失败**（200 + 截断对象落库）——这是"静默损坏"的最终反证；修复后转绿。

---

## 5. 范围边界（明确不做）与决策记录

| 明确不做 | 理由 |
|---------|------|
| 改动已知 Content-Length 的 413 早拒路径 / `Connection: close` 头 | 行为正确且有既有测试锁定（FR-1 约束 b、AC-3） |
| 改 `APP_MAX_BODY_SIZE` 默认值（0 = 不限） | I5 opt-in 语义；缺陷只在显式配置时激活，默认保持 |
| WebDAV / MCP 读体路径的超限错误处理 | 方向验收限 S3 写路径；这些 handler 从"静默截断"变为"读到错误"，严格更安全，但不在本方向验收内 |
| 修复配额少计问题（截断字节数入账） | 随 FR-1 截断不再落库，配额问题自然消除；无需独立改动 |
| 为 service 层增加新守卫 / 引入 `http.MaxBytesError` | 哨兵放 middleware（E13 防环）；service 零改动（E3 `%w` 已穿透） |
| 引入断言框架 / 改动 harness 链装配 | I6；E12 装具已备，零新增装配 |

**决策记录：**

- **D1 哨兵放 `internal/middleware` 而非 `http.MaxBytesError` 或 service：** 方向验收接受 `ErrBodyTooLarge` / `*http.MaxBytesError` 任一形态；选 middleware 自有哨兵因为：① service 已 import middleware（E13），反向依赖成环，middleware 定义哨兵则依赖方向不变；② `rest`/`s3compat` 均已 import middleware，`errors.Is` 映射零新依赖；③ 哨兵可 grep、可文档化，且不把 `net/http` 错误类型渗入 service 层。
- **D2 偷读一个字节区分"恰好 maxBytes"与"超限"：** `io.LimitReader` 无法区分（E1），AC-2 的 no-off-by-one 要求决定必须读第 maxBytes+1 个字节来裁决；行为与 `http.MaxBytesReader` 一致，但不需要其 ResponseWriter 耦合（连接关闭由 net/http 在 handler 返回错误后处理）。
- **D3 S3 错误码命名 `EntityTooLarge`、状态 413：** AWS S3 对超限对象用 400 `EntityTooLarge`，但本服务器既有已知长度超限路径已统一回 413（validation.go:26），保持一致（FR-2 约束 b）；验收只要求 4xx，413 同时满足两条语义线。
- **D4 service 层零改动：** spool 失败路径 `fmt.Errorf("spool upload: %w", err)`（usage.go:126）已保留哨兵链；`cleanup()` 删除临时文件（:117-120）保证"不留残留"由既有代码承担，AC-4 只验证结果。
- **D5 REST 映射一并纳入 FR-2：** 方向验收只测 S3，但 REST `/v1/files` PUT 同走 `svc.Put`，若不映射则 REST chunked 超限回 500（E9），与 S3 行为割裂；映射是同一哨兵的同一改动面，不构成范围扩张。

---

## 6. 实现指引（供验收后落地，非本规格交付物）

1. **中间件：** `validation.go` 增加 `ErrBodyTooLarge` 哨兵 + `limitErrReader`（FR-1 形态），:31 改包 `io.NopCloser(&limitErrReader{r: r.Body, limit: maxBytes})`；:23-27 早拒与 :17-19 透传不动。
2. **适配器映射：** `internal/api/s3compat/errors.go` 三表各加一行（FR-2）；`internal/api/rest/handler_helpers.go` classify 加一 case。
3. **单元测试：** `validation_test.go` 三个新测试（FR-3），用 `req.ContentLength = -1` + 显式 `req.TransferEncoding = []string{"chunked"}` 模拟 chunked（E11：`httptest.NewRequest` 不会自动产生 -1）。
4. **集成测试：** `middleware_chain_test.go` 新增 `TestFullServer_MaxBodySizeChunkedS3Put413`（FR-4），复用 `startFullServerWithConfig`；"无残留"断言同时用 HTTP GET 404 与 `h.repo.GetObject`（双保险）。
5. **门禁：** `make check` 全绿（含 `go test ./internal/middleware/ ./internal/integration/`）；单文件 ≤ 500 行约束下新增代码量约 40 行，无拆分需求。

**验收证据（落地后应可复现）：** ① 在修复前运行 AC-4 测试：S3 chunked 超限 PUT 返回 200 且对象落库（截断 1024 字节）——缺陷复现；② 修复后：413 + GET 404 + `ErrNotFound`，AC-1/2 单元测试绿；③ 将 :31 改回 `io.LimitReader`：AC-1/AC-2(1025 用例)/AC-4 全部失败——三处验收共同钉死"不得静默 EOF 收场"这一行为契约。
