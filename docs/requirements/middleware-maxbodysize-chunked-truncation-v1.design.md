# Design：MaxBodySize 静默截断超大 chunked 上传 → 可辨识哨兵 + 413 映射

> **模块：** `internal/middleware`（哨兵 + 限长读取器）· `internal/api/s3compat` / `internal/api/rest`（错误映射）· `internal/integration`（回归） · **规格：** `docs/requirements/middleware-maxbodysize-chunked-truncation-v1.md`（FR-1..FR-4、D1..D5 全部采纳） · **日期：** 2026-08-07
> **交付物：** 本设计文档（repo `*-v1.design.md` 惯例）；副本位于 pipeline artifact 路径 `docs/auto/runs/maxbodysize-silently-truncates-oversized-chunked-c0acfed6/artifacts/design-a77de8a6/task-1-design.md`。

---

## 1. 证据复核（全部重新对着工作树测量，非采信规格原文）

| 锚点 | 测量结果（今日工作树） | 判定 |
|------|----------------------|------|
| `validation.go:29-31` | :31 `r.Body = io.NopCloser(io.LimitReader(r.Body, maxBytes))`；:17-19 `maxBytes <= 0` 透传；:23-27 已知 CL 早拒 413 + `Connection: close` | ✅ 精确 |
| `usage.go:106-134` | `materializeUnknownSize`：`size >= 0` 直通；否则 spool + `io.Copy(tmp, reader)`（:122），`written` 即 size；错误 `fmt.Errorf("spool upload: %w", err)`（:126）→ 哨兵可 `errors.Is` 穿透；`cleanup()` 删临时文件 | ✅ 精确 |
| `usage.go:61-84` | `validateStoredSize`：仅当 `read == expected && stored == expected` 通过；chunked 下三者皆截断数 → 恒过 | ✅ 精确（守卫失守机理） |
| `file_crud.go:79-195` | `Put`：:108 `materializeUnknownSize` → countingReader → `store.Put` → :173/:191 `validateStoredSize`；错误路径不触 `writePutObject` | ✅ 精确 |
| `file_multipart.go:136` | `UploadPartFor` 同走 `materializeUnknownSize` → 分片上传同样触发哨兵 | ✅ 补充验证（D6） |
| `s3compat/handler.go:35-113` | `PutObject`：:105 `svc.Put(..., r.Body, r.ContentLength, opts)`；:106-110 200 + ETag；:54 chunked 分片 PUT → `uploadPart`（extra.go）→ `svc.UploadPartFor` + `writeS3Error` | ✅ 语义一致（规格引 :99，实测 :105，漂移 +6） |
| `s3compat/errors.go` | `errToS3Code` 表（:100-120，含 `{ErrSizeMismatch, "IncompleteBody"}`）；`s3CodeStatus`（:45-66，无 `EntityTooLarge`）；`s3CodeMessage`（:68-93）；`s3ErrorCode` 未知 → `"InternalError"`（:122-130）→ 500；`classify`（:139-147）；`writeS3Error`（:13-25） | ✅ 精确；**未映射哨兵 → 500** |
| `rest/handler_helpers.go` | `writeError`（:19-24）→ `classify`（:26+，先 `classifyLock` 再 switch）；`ErrSizeMismatch` → 400 `"SizeMismatch"`（:49-51）；default → 500 `"InternalError"`（:66） | ✅ 精确；REST PUT（handler.go:119 `size := r.ContentLength`）同走 `svc.Put` |
| `internal/server/chain.go:74` | `{RingMaxBody, middleware.MaxBodySize(int64(cfg.App.MaxBodySize))}` 在 12 环链内；`chain_test.go:66` `TestBuildChain_12RingsInOrder` 钉死 12 环 | ✅ 精确；S3 网关（`cmd/server/http.go:127` `r.Mount(cfg.S3Compat.Prefix, ...)`）在链内，`main.go:166` 装配 | 
| 全仓 grep `ErrBodyTooLarge` / `MaxBytesError` | **零命中**（非测试代码） | ✅ 需新引入 |
| import 图 | `internal/service` 已 import `middleware`（file.go:12 / access.go:10 / file_delete.go:10）；`middleware` 仅 import stdlib（叶包）；`rest`（别名 `mw`）与 `s3compat`（别名 `mw`）均已 import middleware | ✅ 哨兵必须放 middleware，service 零改动 |
| harness | `internal/integration/fullserver_test.go:84` `startFullServerWithConfig(t, relayOpts, authKeys, cfg)`；:43-47 `fullServerHarness{ts, repo, dsn}` 暴露 repo；/s3 无条件挂载；`server.ApplyMiddleware` 装配 | ✅ AC-4 零新增装配 |
| 既有测试 | `validation_test.go:26/:42/:54` 全部走已知 CL（`bytes.Reader` → `httptest` 自动设 CL）→ 只覆盖早拒路径；`middleware_chain_test.go:22-58` `TestFullServer_MaxBodySize413` 亦为已知 CL | ✅ 盲区确认 |
| 配置/文档 | `config.go:48/:83` `MaxBodySize int // APP_MAX_BODY_SIZE` 默认 0；`docs/configuration.md:33`、`.env.example:5` | ✅ 缺陷仅 opt-in 激活（I5） |
| OpenAPI | `openapi.go`/`specgen.go` 不枚举错误码（无 `SizeMismatch`/`BadDigest` 命中） | ✅ 新错误码零 spec 漂移 |

---

## 2. 先前尝试处置账（design-gate 将复检）

| 运行 | 门禁结论 | 处置 |
|------|---------|------|
| **本 run**（requirements PASS） | 无设计阶段发现 | D1–D5 全部采纳（§3）；一处**加固偏差 D7** 显式记录（对规格参考实现的 `(0,nil)` 空读防御，行为等价） |
| **sibling `close-the-i4-gap`**（design-gate PASS，implement FAIL 后代码已落地） | 无阻断发现；其交付物即本设计基质：`internal/server` BuildChain/ApplyMiddleware 已在树（chain.go:58/:92），MaxBodySize 环即 chain.go:74；其 `TestFullServer_MaxBodySize413` 只覆盖已知 CL | 采纳教训：**链装配零改动**（C6）；本方向只扩展其 413 测试未覆盖的 chunked 盲区；其 implement FAIL 系验证脚本问题，非设计缺陷，无遗留 |
| **sibling `fix-silent-32-mib-truncation`**（同族"静默截断"，acceptance PASS） | 其 F1（存量已截断对象无追溯修复）等 9 项 | **F1 处置为 N/A（不可行 + 明确不做）：** 本缺陷落库的截断对象**内部自洽**（size==blob、无 MD5 失配、无损坏标记），与合法同尺寸对象在静止态不可区分，任何 scrub/扫描无法检出；规格 §5 明确不做追溯。其 F2/F3/F5b/F6（HTTPScanner 远程截断、drain 门控等）与本方向主题无关，不携带。采纳其方法学：fail-closed + 红绿测试钉死（§5 可证伪性） |
| **sibling `chatstream-*`、`fix-chunk-boundary-*`**（requirements FAIL，agent exited 1） | 无产出 | 无发现可处置 |
| **其余 sibling**（billing-outbox / governance / authz-port / versioned-delete 等） | 与 MaxBodySize/chunked 主题不相交 | 不携带任何发现；其"无幻影引用"教训已内化于 §1（全部行号今日实测） |

---

## 3. 设计核心

### 3.1 API 变更（全部，无其他）

| 位置 | 变更 | 形态 |
|------|------|------|
| `internal/middleware/validation.go`（97 → ≈135 行） | 新增包级哨兵 + 限长读取器 + :31 换包 | `var ErrBodyTooLarge = errors.New("request body exceeds maximum allowed size")`；未导出 `limitErrReader`；`MaxBodySize` 签名、早拒、透传**不变** |
| `internal/api/s3compat/errors.go`（147 → ≈156 行） | 三表各加一行 | `errToS3Code` += `{mw.ErrBodyTooLarge, "EntityTooLarge"}`；`s3CodeStatus["EntityTooLarge"] = http.StatusRequestEntityTooLarge`；`s3CodeMessage["EntityTooLarge"] = "Your proposed upload exceeds the maximum allowed object size."` |
| `internal/api/rest/handler_helpers.go`（205 → ≈212 行） | classify switch 加一 case | `case errors.Is(err, mw.ErrBodyTooLarge): return "BodyTooLarge", err.Error(), http.StatusRequestEntityTooLarge` |
| `internal/integration/middleware_chain_test.go`（165 → ≈245 行） | 新增集成测试 | `TestFullServer_MaxBodySizeChunkedS3Put413`（§5 AC-4 + REST 控制，D6b） |

**不变面（零改动）：** `MaxBodySize` 签名与语义（C2/C3）；chain 装配与环序（C6）；`service` 全包（D4，`materializeUnknownSize` 的 `%w` 已穿透哨兵，`cleanup()` 已负责删 spool）；config 默认值（I5）；`go.mod`（零新依赖，I6）；DB schema（无迁移）；OpenAPI（不枚举错误码）。

### 3.2 哨兵与读取器（FR-1；规格参考实现 + D7 加固）

```go
// ErrBodyTooLarge is returned by the wrapped request-body reader when a
// chunked/unknown-length body exceeds maxBytes. Unlike a clean EOF it is
// distinguishable from a body that ends exactly at the limit; adapters map
// it to 413 (RequestEntityTooLarge).
var ErrBodyTooLarge = errors.New("request body exceeds maximum allowed size")

type limitErrReader struct {
    r     io.Reader
    limit int64
    n     int64
}

func (l *limitErrReader) Read(p []byte) (int, error) {
    if len(p) == 0 { // D7a: io.Reader 契约（(0,nil)，不消费）
        return 0, nil
    }
    if l.n >= l.limit {
        // 偷读第 limit+1 个字节裁决：有字节 → 截断哨兵（丢弃该字节）；
        // 底层 EOF → 恰好 maxBytes，干净结束。
        var one [1]byte
        for { // D7b: 防御底层 (0,nil) 空读（io.Reader 允许但不鼓励）
            n, err := l.r.Read(one[:])
            if n > 0 {
                return 0, ErrBodyTooLarge
            }
            if err != nil {
                return 0, err
            }
        }
    }
    if int64(len(p)) > l.limit-l.n {
        p = p[:l.limit-l.n]
    }
    n, err := l.r.Read(p)
    l.n += int64(n)
    return n, err
}
```

- :31 替换为 `r.Body = io.NopCloser(&limitErrReader{r: r.Body, limit: maxBytes})`。
- **行为契约（逐字节）：** body `< maxBytes` → 与 `io.LimitReader` 逐字节一致；`== maxBytes` → 完整读出、末尾干净 `io.EOF`（**必须**偷读第 maxBytes+1 字节区分，D2）；`> maxBytes` → 第 maxBytes 字节后下一次 Read 返回 `ErrBodyTooLarge`（**绝不**伪装为 EOF）。
- 传输层真实错误（客户端中止等）原样透传，**永不**误标为 `ErrBodyTooLarge`（哨兵仅在确实读到第 limit+1 个字节时返回）。
- 错误已返回后再 Read → 再次进入偷读分支（返回 `ErrBodyTooLarge` 或底层 EOF），无害。

### 3.3 错误传播链（端到端，零 service 改动）

```
chunked PUT (CL == -1)
→ max_body 环：limitErrReader 包裹（早拒不触发）
→ s3compat PutObject :105 svc.Put(r.Body, -1)
→ materializeUnknownSize：io.Copy → 读到 ErrBodyTooLarge
→ "spool upload: %w" 包裹 → file_crud.go:110 直接返回（未触 writePutObject）
→ cleanup() 删 spool 临时文件
→ s3compat writeS3Error → classify → errToS3Code 命中 → 413 EntityTooLarge
REST /v1/files PUT：同一哨兵 → rest writeError → classify → 413 BodyTooLarge
```

**D6（分片上传自动覆盖）：** REST `UploadPart`（handler.go:312-322）与 S3 `uploadPart`（extra.go）同走 `svc.UploadPartFor` → `materializeUnknownSize`（file_multipart.go:136），哨兵在**适配器共享的 classify 表**映射 → 全部写路径（对象 PUT + 分片 PUT）自动覆盖，零额外代码。S3 `uploadPartCopy` 读 `x-amz-copy-source` 而非请求体，不受影响。

---

## 4. 兼容性约束

| # | 约束 | 依据 |
|---|------|------|
| C1 | 默认部署（`APP_MAX_BODY_SIZE=0`）零行为变化（透传分支不动） | validation.go:17-19；I5 |
| C2 | 已知 CL 早拒路径（:23-27，413 + `Connection: close`）原样保留 | 既有测试 `TestMaxBodySize_ExceedsLimit`/`ExceedsViaContentLength` 锁定；AC-3 |
| C3 | `≤ maxBytes` 的 body（任意 CL）读取结果逐字节一致、干净 EOF 收场 | D2 偷读裁决；AC-2 |
| C4 | `> maxBytes` 且 CL 未知：**行为变更 200+截断落库 → 413+无残留**。仅影响显式设置上限的 opt-in 部署；属缺陷修复，需在发版说明标注 | 规格 §1/§3；AC-4 可证伪性 |
| C5 | 大对象迁移路径：S3 multipart（分片各自 ≤ 上限）仍可用；文档建议客户端超限时改用 multipart 或调高 `APP_MAX_BODY_SIZE` | — |
| C6 | 链装配零改动：不增删环、不动环序 → `TestBuildChain_12RingsInOrder` 保持绿 | I4；chain.go 只读 |
| C7 | 错误身份经 `errors.Is` 判定：任何未来 `%w` 包裹不破坏映射 | 两适配器均用 `errors.Is` |
| C8 | `middleware` 保持叶包（stdlib-only），import 图不变（无环） | 已验证 |

---

## 5. 失败模式

| # | 模式 | 处置 |
|---|------|------|
| F1 | 哨兵只映射一个适配器 → 另一侧 500 泄漏 | FR-2 两映射同 commit；集成测试同文件覆盖 S3 + REST（D6b） |
| F2 | 底层 `(0, nil)` 空读 → 偷读分支死循环 | D7b：循环直至 `n > 0` 或 `err != nil` |
| F3 | `len(p) == 0` 违反 io.Reader 契约 | D7a：立即 `(0, nil)` |
| F4 | 传输中止（客户端断开）：错误原样透传，映射为既有 500 | 与修复前一致（pre-existing），哨兵不吞真实错误 |
| F5 | HTTP/2 未知长度流（无 CL，非 chunked）：同样走包裹路径，同样被拦截 | 设计天然覆盖，无需额外测试 |
| F6 | WebDAV/MCP 读体：从"静默截断"变为"读到错误"（严格更安全，x/net/webdav 报 500） | 规格 §5 明确不做；残余记录，不阻断 |
| F7 | 413 后连接处理：未读完的 chunked body → net/http 自动关连接（与早拒路径的 `Connection: close` 行为对等） | 无需代码；文档提示客户端可能同时见连接关闭 |
| F8 | 修复前已落库的截断对象无法追溯 | **处置：N/A**（§2 sibling F1）——对象内部自洽（size==blob、无标记），静止态不可区分；规格明确不做 |
| F9 | 修复前配额少计不可追溯 | 同 F8；修复后截断不再落库，配额问题自然消除（规格 §5） |

---

## 6. 迁移步骤

1. **无 DB 迁移、无配置迁移、无 `go.mod` 变更**——纯代码修复（§3.1）。
2. 实现中间件变更（哨兵 + `limitErrReader` + :31 换包）。
3. 实现两适配器映射（s3compat 三表 / rest classify）。
4. 新增单元测试（§5 验收 AC-1/AC-2）与集成测试（AC-4 + REST 控制）。
5. 门禁：`make check` 全绿（gofmt/build/vet/test，含 `./internal/middleware/` 与 `./internal/integration/`）；可选 `go test -race ./internal/middleware/ ./internal/integration/`。
6. 文档触点：`docs/configuration.md:33` 措辞追加一句"unknown-length (chunked) bodies over the limit are rejected with `413`, never truncated"；`.env.example` 注释同步（可选，非门禁）。
7. 运维发版说明：部署了 `APP_MAX_BODY_SIZE` 的站点升级后，超限 chunked 上传由"静默损坏"变"显式 413"；客户端改用 multipart 或调高上限（C5）。

---

## 7. 可测试验收映射（AC → 测试 → 断言 → 可证伪性）

| AC | 测试（文件） | 断言 | 可证伪性 |
|----|-------------|------|---------|
| **AC-1** chunked 超限 → 非 EOF 错误 | `validation_test.go`：`TestMaxBodySize_ChunkedOversizeReturnsErrBodyTooLarge`（`MaxBodySize(5)`，`req.ContentLength = -1`，12B body，handler 内 `io.ReadAll(r.Body)`） | `err != nil` ∧ `errors.Is(err, middleware.ErrBodyTooLarge)` ∧ `!errors.Is(err, io.EOF)`；响应 413 | 退回 `io.LimitReader` → `ReadAll` 返回 `(5, nil)`，断言即红 |
| **AC-2** 恰好 maxBytes 无 off-by-one | `validation_test.go`：`TestMaxBodySize_ChunkedExactLimitNoOffByOne`（子用例 a：`MaxBodySize(1024)` + 1024B + CL=-1 → 完整读出、无哨兵；子用例 b：1025B → 前 1024B 无错、下一次 Read → `ErrBodyTooLarge`） | a：读出 == 1024B ∧ err ∈ {nil, io.EOF}；b：首个错误即 `ErrBodyTooLarge` | 读到 limit 即报错（不偷读）→ a 红；`io.LimitReader` → b 红 |
| **AC-3** 既有测试全绿 | `go test ./internal/middleware/`（既有 5 测试 + 新增）；`go build ./...`、`go vet ./...` | 全绿 | 改动早拒/透传 → `TestMaxBodySize_ExceedsLimit`/`UnderLimit` 红 |
| **AC-4** S3 chunked PUT 超限 → 4xx 且无残留 | `middleware_chain_test.go`：`TestFullServer_MaxBodySizeChunkedS3Put413`（`startFullServerWithConfig(t, &events.EventOutboxRelayOptions{}, "", &config.Config{App: config.AppConfig{MaxBodySize: 1024}})`；`PUT /s3/default/chunked-oversize.bin`，4096B，`ContentLength = -1` + `TransferEncoding = ["chunked"]`） | ① status == 413（断言区间 [400,499]）；② `GET /s3/default/chunked-oversize.bin` → 404；③ `h.repo.GetObject(ctx, "default", "default", "chunked-oversize.bin")` → `repository.ErrNotFound`；④ 控制组：512B chunked → 2xx，GET 回读逐字节一致 | **今日缺陷代码上必然失败**（200 + 截断对象落库）——静默损坏的最终反证；修复后转绿 |
| **FR-2（D6b）** REST 映射钉死 | 同测试文件追加：`PUT /v1/files/chunked-oversize.txt`（同 chunked 构造） | status == 413 ∧ body 含 `"BodyTooLarge"` | 只映射 S3 → REST 红；两映射同 commit 保证 |

**门禁合规（硬门禁逐项）：** `gofmt -l` 无输出（编辑后格式化）；`go build ./...` / `go vet ./...`；单文件 ≤ 500 行——非测试文件最大增量 `validation.go` 97 → ≈135 行（测试文件按 `Makefile:162` 豁免，且均远低于上限）；I6 stdlib-only；I4 链序不变；I5 默认值不变。

**验收证据（落地后复现步骤）：** ① 修复前运行 AC-4 → 200 + 截断 1024B 对象落库（缺陷复现）；② 修复后 → 413 + GET 404 + `ErrNotFound`，AC-1/AC-2 绿；③ 将 :31 改回 `io.LimitReader` → AC-1/AC-2(b)/AC-4 三处齐红——三路验收共同钉死"不得以干净 EOF 静默收场"契约。
