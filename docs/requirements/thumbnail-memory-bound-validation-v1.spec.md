# 缩略图并发内存门禁稳定化 v1

## 证据与范围

上一轮下载改动的完整门禁在 `TestGenerateSemaphoreBoundsConcurrentAllocation` 连续失败。本轮按相同 Go 1.26.8、`GOMAXPROCS=4 GOFLAGS=-p=1` 重复该单例三次亦失败（约 2.22 GB，校准上限约 1.21 GB），不是只在整包出现。

原测试在生成器持续退场/换批时调用 `runtime.GC()` + `ReadMemStats`，把 `HeapAlloc` 当作当前可达的 in-flight 堆。Go 的该指标包含未回收对象；并发 GC 中被标记/新分配的对象及 GC 后新增分配都使这两次调用不是原子活对象快照。暂停真实解码器的诊断显示：4 个 8192² 解码器最后一次源读取被阻塞时，两次 GC 后约 1,075,400,000 bytes / 4 槽；全部退出并 GC 后约 877,000 bytes / 0 槽。原指标不能证明 4 槽限制被突破，也不是服务进程 RSS 硬预算。

只修复测试的测量与清理、澄清注释；不修改解码/缩放/缓存/授权逻辑、接口、依赖、配置或门禁范围。

## 验收契约

- 保持 4 槽设计 pin、8192² 8-bit 输入、2048² 输出上限、16 次全部成功、120 s watchdog、既有 short/race 规则及 2 GiB 绝对上限；不降低并发、缩小 fixture、跳过失败测试或放大 slack。
- 三次完整单请求的 `TotalAlloc` 最大增量作为整条流水线分配预算。其至少包含 256 MiB 源帧；4 倍预算 + 原有 64 MiB slack 不得超过原有 2 GiB。此保守上界同时约束解码以后的分配，不只检查暂停点。
- 16 个请求同时启动，通过真实源 Reader 把解码器停在最后一次源读取。每批 4 个已分配大帧且不能退场/换批时进行 GC 快照，验证 4 槽被占用、可达帧下限及原有 `4 × 单请求预算 + 64 MiB` 上限；释放并等待本批完成，全部四批都必须成功。读完的旧帧可被 GC，不把其浮动垃圾当作新批的活帧。
- fixture 读前缓冲不能把整个输入预读完，快照必须真实保留 4 × 256 MiB 解码帧；假解码/缩小 fixture 必须失败。提前释放槽应由占用断言拒绝。
- Gate Reader 仅用于测试；握手而非 sleep 决定阶段。正常/失败路径均取消、释放 gate、等待 goroutine 退出，不给后续测试遗留槽或任务。
- 全流水线预算不是进程 RSS/HeapAlloc 限额，不承诺所有格式、缓存、其他请求及 GC 垃圾之和有此硬上限。

## 验证计划

重复单例及整包（含 shuffle）、Reader 单测、定向 race 和既有 REST 缩略图/历史读取回归；执行 `make check` + `python3 cli.py accept`。此前前端源码与嵌入产物保持不变；不以通过门禁代替真实存储、大文件或进程 RSS 专项验证。

## 本轮实际结果

- 单例及 3 项新 Reader/清理测试重复 3 次通过；单次全流程分配约 285,594,048 bytes，4 槽快照峰值约 1,075,423,368 bytes，动态上限约 1,209,485,056 bytes（未扩大原有 slack/绝对上限）。缩略图整包 `-count=3 -shuffle=on` 通过。
- 临时故障注入将槽在像素分配前释放，测试以 `gated decoders hold 0 slots, want 4` 拒绝；注入已撤回。最终生产差异只有边界说明注释。
- 含大帧内存测试的定向 thumbnail race 通过。相同整组 REST 缩略图/版本回归的自选 180 s 包预算首次超时，堆栈位于 8192² PNG fixture 构建；按仓库已有 300 s REST race 预算重跑同范围通过（243.355 s），未改仓库预算/范围。
- `make web-check`（235 项）→ `make build` → `make web-smoke` 通过；应用产物仍为 `index-DE9XwLIq.js`，冒烟只验证应用加载。
- `GOMAXPROCS=4 GOFLAGS=-p=1 make check` 与同配置 `python3 cli.py accept` 均 exit=0。Makefile 原有 metadata / thumbnail short / REST cache race 门禁完整运行；没有声称另跑整仓 `make test-race` 或实库集成。
- 新增函数 AST 核对 ≤39 行、复杂度 ≤10；文件/不变量/差异空白检查通过，禁止接口/SDK/迁移未改，未提交或部署。
- 日志：`/tmp/aero-thumbnail-memory-{baseline,probe,targeted,package,mutation,race,rest-race-retry,web-check,build,web-smoke,check,accept}.log`。
