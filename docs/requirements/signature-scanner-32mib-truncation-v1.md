# 方向：修复 SignatureScanner 静默 32 MiB 截断——>32 MiB 文件尾部从未被扫描却报 `clean`（永久假阴性）

> **模块：** `internal/antivirus` · **来源分析：** `docs/auto/analyses/internal-antivirus-4eff1e6c.json`（第 1 条 direction）· **日期：** 2026-08-07
> **评分：** 价值 9 / 风险降低 9 / 工作量 3 / 置信度 10
> **验证基准：** 工作树 = HEAD `acfaaf4` + **未提交 WIP**（多轮 campaign 已落地：antivirus worker 路由方向 `route-antivirus-worker-mutations-tag-write-quara-27bd11cc`、access fail-closed、outbox relay 等）。本文全部引用已对照该基准逐行验证；行号以工作树为准。

---

## 1. 问题陈述（已验证成立）

`SignatureScanner.Scan` 用 `io.LimitReader(r, 32<<20)` + `io.ReadAll` 读取待扫描流：

1. **无截断信号：** 当对象 > 32 MiB 时，`io.ReadAll` 返回恰好 32 MiB 且 `err == nil`——调用方无法区分"对象恰好 32 MiB"与"对象被截断"。
2. **静默假阴性：** worker 将 `Result{Clean: true}` 持久化为 `av_status=clean`（`worker.go:174-175`）并跳过 quarantine（`:190-194`）。放在前 32 MiB 之后的 payload（如 EICAR）**永远**漏检——AV 最糟失效模式（假阴性），且无任何错误信号触发 job 重试。
3. **可达：** `APP_MAX_BODY_SIZE` 默认 `0 = unlimited`（`internal/config/config.go:48,83`；`MaxBodySize(0)` 禁用有测试钉死，`middleware/validation_test.go:14-25`）→ >32 MiB 上传是受支持路径，缺陷可被真实触发。
4. **附带浪费：** `worker.go:164` 无条件 `_, _ = io.Copy(io.Discard, rc)` 在扫描后把整个对象从存储读完——`SignatureScanner` 只匹配了头部，剩余读取纯属浪费（S3 等按读计费后端为每次扫描付全对象读费）。

**本方向要求：** `SignatureScanner.Scan` 不得在部分读取后返回 `Clean:true`——要么全流扫描（流式匹配），要么返回显式"未完整扫描"信号（错误或非 clean 结果）；worker 不得把部分扫描持久化为 `clean`；`HTTPScanner` 路径的 drain 保留（HTTP 连接复用所需），`SignatureScanner` 路径消除全量 drain。

---

## 2. 现状与代码证据（已逐条验证）

### 2.1 工作树状态修正

- `internal/antivirus/antivirus.go` **无 WIP**（`git status` 干净）：截断缺陷在 HEAD 与工作树同样存在。
- `internal/antivirus/worker.go` **有 WIP**（sibling 方向已落地）：新增 `SystemActor`（= `access.SystemActorAntivirus`）、`maxSignatureBytes`（4 KiB）、`WithObjectController`、tenant 一致性 guard、`QuarantineObjectByID(ctx, objectID, signature)`（签名携带威胁名）、controller 调用钉 `PrincipalSystem`。**本规格与之正交**：只动 Scan 的读取语义与 drain 条件，不触碰上述 WIP 表面。
- 测试基建（工作树，`antivirus_test.go`）：`setupSvc`（:66-79，sqlite + local FS + `service.NewFileService(store, repo, nil)`）；`NewWorker(repo, svc.Storage(), NewSignatureScanner(nil), nil, true, logger).WithObjectController(svc)` 模式；`TestWorkerQuarantinesInfected`（quarantine 后 `GetObject→ErrNotFound` + `quota.UsedBytes/UsedObjects==0`）、`TestWorkerNoQuarantineKeepsButTags`、`TestHTTPScanner`（httptest）。

### 2.2 方向文证据验证表

| # | 方向文引用 | 验证结果 |
|---|-----------|---------|
| E1 | `internal/antivirus/antivirus.go:74-90` | ⚠️ **行号偏移，实质成立**。`SignatureScanner.Scan` 实际在 `:55-67`：`:58` `const max = 32 << 20`、`:59` `data, err := io.ReadAll(io.LimitReader(r, max))`、`:62-65` `bytes.Contains` 循环、`:66` `return Result{Clean: true}, nil`。引用的 `:74-90` 落在 `HTTPScanner` 区域，但缺陷代码（LimitReader + 无截断信号 + 部分读取即 Clean）逐行属实 |
| E2 | `internal/antivirus/worker.go:96-101` | ⚠️ **行号偏移，实质成立**。无条件 drain `_, _ = io.Copy(io.Discard, rc) // drain remainder` 实际在 **`:164`**（紧接 `w.scanner.Scan(ctx, rc)` :160 及错误检查之后）；其后 `:174-175` 写 `av_status=clean`、`:190-194` quarantine。引用的 `:96-101` 为 `Run`/`ScanObjectByID` 前段，非 drain 位置 |
| E3 | `internal/config/config.go:48` | ✅ **精确**。`:48` `MaxBodySize int // APP_MAX_BODY_SIZE; max request body bytes (0 = unlimited)`；`:83` `MaxBodySize: getEnvInt("APP_MAX_BODY_SIZE", 0)`；`internal/middleware/validation.go:16` `MaxBodySize(0)` 恒放行 + `validation_test.go:14-25` `TestMaxBodySize_DisabledWhenZero` 钉死 → >32 MiB 上传可达成立 |

**符号验证（全部存在）：** `SignatureScanner`（antivirus.go:55）· `NewSignatureScanner`（:34）· `Result{Clean, Signature}`（:13-17）· `Scanner` 接口（:20-23）· `EICAR` 常量（:31）· `HTTPScanner`（:93，60s 超时）· `TagStatus`/`TagSignature` = `av_status`/`av_signature`（worker.go:26-27）· `ScanObjectByID`（worker.go:135）· `Scanner.Scan` **全仓唯一调用点** = `worker.go:160`（grep 证实，无其他消费者）。

### 2.3 缺口分析

| # | 缺口 | 现状证据 | 后果 |
|---|------|---------|------|
| G1 | **无截断信号** | `io.ReadAll(io.LimitReader(r, 32<<20))`（antivirus.go:59）：输入超限时返回恰 32 MiB + nil error | 调用方无法区分"完整"与"截断"，尾部 payload 结构性漏检 |
| G2 | **部分扫描被持久化为 clean** | `res.Clean` → `tags[TagStatus]="clean"`（worker.go:174-175），无任何"未扫描"状态可写 | 永久假阴性且无 job 重试、无告警 |
| G3 | **SignatureScanner 路径全量 drain** | `_, _ = io.Copy(io.Discard, rc)`（worker.go:164）对**所有** scanner 无条件执行 | 本地扫描每次付全对象读 I/O（S3 按读计费）；drain 对 `SignatureScanner` 无任何收益（`HTTPScanner` 需要：POST body 消费 + 连接复用） |

---

## 3. 需求规格

### FR-1：`SignatureScanner.Scan` 不得静默截断（核心）

- 实现二选一（方向 acceptance 允许任一）：
  - **(a) 流式匹配器**：扫描**整个**流，任意偏移命中签名即返回 `{Clean:false, Signature}`——>32 MiB 尾部 EICAR 必须检出；
  - **(b) 显式截断信号**：保持有界读取，但流未耗尽时必须返回非 `Clean:true` 的结果（如非 nil 错误，或新增显式字段/哨兵，由设计定），调用方据此判定"未完整扫描"。
- **禁止：** 任何路径下部分读取后返回 `Result{Clean: true}`。
- **内存约束：** 不得引入整对象缓冲（现 32 MiB 为内存上界；流式方案须滑动窗口或等价有界内存）。
- **接口不变：** `Scanner` 接口签名（`Scan(ctx, r) (Result, error)` / `Name()`）、`HTTPScanner` 协议与 60s 超时、`EICAR` 常量均不动。
- **≤32 MiB 对象行为零回归：** 既有 `TestSignatureScannerEICAR`/`TestSignatureScannerExtra` 语义不变。

### FR-2：worker 不得将部分扫描持久化为 `clean`

- `ScanObjectByID` 对截断/未完整扫描必须：要么让 `Scan` 的错误传播（job 进入重试/失败终态，**不写任何 tag**），要么写入显式非 clean 结果（设计定）。
- `av_status=clean` 仅在扫描器**确认全对象已扫描**时写入（`worker.go:174-175` 的条件收窄）。
- 不改变 `av_status=infected` + `av_signature` 既有写入路径（WIP 的 `maxSignatureBytes` 截断保持）。

### FR-3：尾部感染 = infected + quarantine（既有语义延续）

- 流式匹配器在偏移 > 32 MiB 命中 EICAR 时，走与头部感染完全相同的路径：`tags[TagStatus]="infected"`、`tags[TagSignature]="EICAR-Test-File"`（worker.go:178-179），`w.quarantine==true` 时 `QuarantineObjectByID(controllerCtx, objectID, signature)`（:190-194，WIP 签名）。
- 显式截断信号设计下 quarantine 不触发（扫描未完成即失败返回），由 job 重试机制处理——**不得**在未扫描的情况下 quarantine。

### FR-4：drain 仅限 `HTTPScanner` 路径

- `worker.go:164` 的无条件 `io.Copy(io.Discard, rc)` 改为按 scanner 路径条件化：
  - **`SignatureScanner` 路径：** 不得在扫描后再全量读取对象（存储 I/O = scanner 自身消费量，无额外读取）；
  - **`HTTPScanner` 路径：** drain 保留（消费剩余 body 以完成服务端请求/复用连接）。
- 机制（scanner 类型分支或 scanner 回报"是否已消费全流"）由设计定；不得破坏 `HTTPScanner` 现有测试。

### 约束（AGENTS.md 硬门禁）

- `gofmt -l` 无输出 · `go build ./...` · `go vet ./...` · `go test ./...` 全绿；改动文件 ≤ 500 行；单函数 ≤ 50 行。
- 测试仅用 stdlib `testing`（I6，禁断言框架）；**零新增 go.mod 依赖**。
- 不新增配置键（无扫描上限/截断策略配置）；不触碰 `internal/config` 与本模块以外文件。
- `internal/antivirus` 内改动不破坏 WIP 测试（`TestScanObjectByIDQuarantineWritesAuditAndOutbox`、`TestScanObjectByIDBoundsOversizedSignature`、`TestScanObjectByIDRejectsTenantMismatch` 等必须照常通过）。

---

## 4. 验收标准（可测试；逐条映射方向 acceptance）

> 测试基建（已验证）：`setupSvc`（antivirus_test.go:66-79）提供 sqlite + local FS + FileService；`NewWorker(repo, svc.Storage(), NewSignatureScanner(nil), nil, <quarantine>, slog…).WithObjectController(svc)` 为既有装配模式；`TestWorkerQuarantinesInfected`（:102）提供 quarantine 断言先例（`GetObject→ErrNotFound` + `quota.UsedBytes/UsedObjects==0`）。`svc.Put` 无尺寸上限（`APP_MAX_BODY_SIZE` 仅作用于 HTTP middleware，`internal/config/config.go:48`），>32 MiB 对象可直接写入 local FS。

### AC-1 单元：`SignatureScanner.Scan` 对 >32 MiB 尾部 EICAR 永不返回 `Clean:true`

`internal/antivirus/antivirus_test.go` 新增（今日必红——当前实现返回 `{Clean:true}`，证明是回归测试）：

```go
func TestSignatureScannerTailBeyond32MiB(t *testing.T) {
	s := NewSignatureScanner(nil)
	// 32 MiB 零填充 + EICAR 尾部；io.MultiReader 避免物化 33 MiB 缓冲
	r := io.MultiReader(bytes.NewReader(make([]byte, 32<<20)), strings.NewReader(EICAR))
	res, err := s.Scan(context.Background(), r)
	// 失败条件：部分读取后报 clean —— 结构性假阴性
	if err == nil && res.Clean {
		t.Fatalf("silent truncation: >32MiB tail payload reported clean: %+v", res)
	}
	// 通过条件（二选一）：
	//  (a) 流式匹配: err==nil && !res.Clean && res.Signature=="EICAR-Test-File"
	//  (b) 显式截断: err != nil（错误串应含 "truncat"/"unscan" 语义，可断言）
}
```

正对照：既有 `TestSignatureScannerEICAR`（:24）与 `TestSignatureScannerExtra`（:38）保持绿——≤32 MiB 路径行为不变。

### AC-2 集成：`ScanObjectByID` 对 >32 MiB 尾部 EICAR 对象**永不写 `av_status=clean`**

`internal/antivirus/antivirus_test.go` 新增（沿用 `setupSvc` 模式；对象 = 32 MiB 零 + EICAR 尾，`size = 32<<20 + len(EICAR)`）：

```go
func TestScanObjectByIDTailEICARNeverClean(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	body := io.MultiReader(bytes.NewReader(make([]byte, 32<<20)), strings.NewReader(EICAR))
	obj, err := svc.Put(ctx, "default", "default", "tail-eicar.bin", body, int64(32<<20+len(EICAR)), service.PutOptions{})
	// ...
	// 分支 1: quarantine=true —— 尾部感染 → 软删（照 TestWorkerQuarantinesInfected 断言：
	//   repo.GetObject → ErrNotFound；quota.UsedBytes==0 && quota.UsedObjects==0）
	w := NewWorker(repo, svc.Storage(), NewSignatureScanner(nil), nil, true, logger).WithObjectController(svc)
	// 分支 2: quarantine=false —— 对象保留，但 tags[TagStatus] != "clean"
	//   （流式匹配: == "infected" 且 av_signature=="EICAR-Test-File"；
	//    显式截断: ScanObjectByID 返回非 nil error 且对象无 av_status tag —— 两者均非 clean）
}
```

### AC-3 套件 + drain 范围

- `go test ./internal/antivirus` 通过（含全部既有 + WIP 测试，如 `TestWorkerQuarantinesInfected`、`TestScanObjectByIDQuarantineWritesAuditAndOutbox`、`TestHTTPScanner`）。
- **drain 断言测试**（新增，字节计数 reader 包住 storage reader 或等价手段）：
  - `SignatureScanner` 路径：`ScanObjectByID` 返回后，从存储读取的**总字节 ≤ scanner 自身消费量**（流式匹配 = 全对象但扫描即消费，drain 贡献 0 字节；有界+截断信号 = ≤ 32 MiB 上界）——证明无事后全量 drain（G3）；
  - `HTTPScanner` 路径：剩余流仍被消费（既有 httptest 模式 + drain 保留断言）——证明 FR-4 未破坏 HTTP 连接语义。

---

## 5. 范围外（明确不扩）

- `HTTPScanner` 协议、超时、鉴权头；quarantine 策略/审计/outbox（WIP 表面）不动。
- `APP_MAX_BODY_SIZE` / 上传限制 / middleware 不动（可达性已确认，非本方向修复对象）。
- JobPool 重试/背压/`ErrQueueFull` 语义不动。
- 分析 JSON 中第 2 条 direction（AV worker 与 indexer/replication 的 tags lost-update 竞态）**属另一方向**，本规格不涉及。
- 不新增配置键、不新增 `go.mod` 依赖、不改 `internal/config`。
