# SPACE-P1B — Runtime deny-bound v1：完整 B 目标与 Wave-1 限定合同

## 1. 状态、依据与授权边界

- **本文是 requirements-first 合同，不是实现或验证报告。** 本阶段仅新增本文、末尾追加 `docs/agent/CURRENT_SPRINT.md`；不改 Go、不执行测试或迁移。下列新增能力、测试和工程门禁全部待执行，不预填 PASS。
- **Wave-1 仅为 scoped chunk cleanup / AI scoped sinks / durable jobs 的离线、有限运行时基础片；not a complete B / not production / no Space runtime deny-bound。** 即使后续离线门禁通过，也不能将本片称为完整 B、完整 U1 运行时准入、Space 权限完成或生产可用。
- 本片不安装 `spaces.BindingReader`、`CoverageReader` 或 `ResourceBoundary` gate；不能把四字段身份校验宣传为 Space deny-bound、检索保护、成员授权或 generation fencing。
- 本合同不扩大配置、依赖、schema、Repository/Storage/spaces.Store 或模型接口，不授权 SDK/UI/API、生产暴露、部署、Git 操作或截止延期。既有删除 provider、租户、保护、审计和 Outbox 合同不变。

精确外部证据索引（均为本任务上下文，不将历史设计提升为当前工程证据）：

| 代号 | 位置与用途 |
|------|------------|
| RUN | `/home/u1/.abr/projects/aero-vault-5c281c8a1991a53d/runs/p1b-deny-bound-impl-20261021T122830Z` |
| PLAN | RUN 下 `scheduler/plan-revised.md`：唯一 Wave-1 选择、A/B 所有权、顺序和待验矩阵；`WAVE1_STATUS: ready` 只表示调度完整 |
| CONTRACT | RUN 下 `writers/CONTRACT.md`：四字段 ref、强制 scoped capability、兼容与失败语义 |
| DOCS | RUN 下 `work/allow-docs.json`：本阶段仅两文档，Sprint 为 append；不是 35 路径同时开写许可 |
| U1 | `/home/u1/.abr/projects/aero-vault-5c281c8a1991a53d/runs/p1b-u123-targeted-readonly-sol-flash-20261019T061153Z/executors/codex.md` 的 U1/U2/U3：复用已收敛 scoped 合同，保留其历史未实施／未验收边界 |
| CANDIDATE | `/home/u1/.abr/projects/aero-vault-5c281c8a1991a53d/runs/p1b-events-only-readonly-sol-flash-20261020T001229Z/candidate-scope.json`：完整 B 候选背景，`proposal_not_permission`；不作为扩大本片许可的依据 |
| SEED | RUN 下 `writers/seed.md`，上游已验证 61 bytes、SHA256 前缀 `6f92bb626d78`；内容仅为 controller seed，不证明代码、许可或测试 |

项目边界以 `AGENTS.md`、加载链及既有 foundation/binding-probe spec 为背景；历史成功不抵扣本片检查。外部报告不覆盖的实现事实，实施前必须按当前私有源码重新核对。

## 2. 完整 B deny-bound 目标（本节除 Wave-1 身份基础外均待后续授权实施）

完整 B 的目的，是在 Space 产品授权尚未接通前，阻止 Space-bound 资源经旧文件／管理／AI／异步出口被使用；**不是赋予任何 Space 成员访问权**。准确 bound 拒绝、准确 unbound 才能继续原有授权链，fault 不能伪装成 unbound。

### 2.1 共享准入与错误政策

1. 精确 tenant/bucket 使用权威 `BindingReader`，校验完整结果和请求回显；不能按 `spc-`、桶名、owner、成员标签、context 或 `GetSpaceByBucket` 的 NotFound 猜 absence。
2. 准入独立于 access-control 开关，先于 system／disabled-authorizer／creator／publisher 快捷路径；system principal、delete opt-out 和通用 `*` 均不是 Space 豁免。
3. 已知停用租户继续拒绝。未知 tenant 只有在权威观察确实 unbound 后才可能进入原兼容政策；raw status 和 binding result 本身都不授许可，不将未知／非 active 状态补成 active。
4. 缺失、显式 nil、typed-nil capability，坏回显／不完整值，DB／Scan／JSON／context 故障均为 fault。保留 Is/As cause；fault 分类优先于其 cause 中的 NotFound/Denied，不得变为空页、空 owner、public=true、健康空结果或静默过滤。
5. 对外错误按协议优先分类并脱敏，不暴露内部错误文本；deadline 保留既有超时语义（REST 504）。只有明确普通拒绝可按既有授权规则过滤，provider fault 不得混为普通拒绝。
6. tenant/global 聚合需权威 `CoverageReader`：fresh P0 → 完整物化 D → fresh P1，再交付。有限 `ListBuckets`、空数据、旧事务快照或缓存 absence 都不是全集无绑定证明；unknown/partial/fault 不得称 complete。
7. copy/MOVE/PartCopy 同时覆盖实际源与目标；ensure-bucket 后再核验实际绑定。上述观察点不宣称 DB+Blob 跨层原子或可撤回已发送字节。

### 2.2 完整 B 的入口／出口闭包与本片缺口

| 面 | 完整目标（全部 deferred，不是本片交付） |
|----|--------------------------------------|
| FileService / Manager | 实际对象、版本、桶、ACL、分享、公开资源、匿名读取及空列表路径均准入；先于快捷授权、消费分享和副作用；保留独立 delete provider 与租户隔离 |
| REST / S3 / WebDAV / MCP | 共同入口、直接 raw-read、配置／零 limit／虚拟目录／LOCK、条件／Range／304、指定桶和 URI 均覆盖；fault 不触发 legacy/default fallback |
| replay | REST idempotency 和 multipart complete/abort 复用持久原记录、原 DTO/header/query 的精确身份，重新准入当前动作；不以重试 body／最新同 key 对象修补，不重复执行业务或重签 capability |
| aggregation / admin | account/usage/quota/audit/jobs/stats、精确 job retry 与 lineage 使用完整事实；全局 absence 不是截断列表，Missing job 不触发 RetryJob；历史引用不能按 key 替换 |
| SSE / thumbnail | SSE headers 前及 backlog/live 每事件准入，权威复读失败停止；thumbnail resident/304/follower/实际 opened identity 在成功头与 access event 前再核验，不让缓存旁路 |
| Search / Chat / Agent | P0/D/P1、当前 canonical object/chunk、cache hit、reranker 输出及模型过滤；pre-LLM、最终 answer/citations、stream callback 和累积 tool/history refs 再核验；安全 fault 不转 tool 文本续聊，保原 SSE/预算/步数与降级语义 |
| events / notifications | bus transport、remote Deliver、PG full-JSON transport、webhook 初发/retry、notifier 和 notify outbox 均有权威来源与出口准入；bound/fault 不外发，不伪 delivered；保非阻塞 broadcast、HMAC、原 retry/fencing |
| L2 deleted facts | `deleted@1.1` 是审计事实，不因 Space bound 丢弃；验证 row/payload 一致性但不做 Space 成员许可查询。notify 策略不得混入 deleted sink 或更改字节合同 |
| workers / timers | AV/replication/resync/reconcile/scrub/lifecycle/retention/upload-GC 的实际行、job tenant、保护和 outcome 覆盖；安全 fault 不计成功；孤儿 Blob 仍精确 StorageKey 集合且删除 opt-in |
| root | 在 billing/audit 包装前捕获真实窄读端口、显式注入各消费者，写入仍走原包装链；覆盖四包装组合×access on/off、HTTP/stdio/worker；prepare/preflight/start 完整排序，缺能力及不安全孤儿删除组合先失败 |

完整 B 仍不包含 P2 Space API/UI、员工身份政策、成员读写授权产品、接管／移交、旧桶认领、owner/StorageKey 回填或搬迁。不得将 Bucket 改名或角色表存在当作可使用 Space。

## 3. Wave-1 精确范围与分阶段所有权

PLAN 的 `WAVE1_A` 18 条与 `WAVE1_B` 17 条合计 **35 个不重叠路径**，其余 147 个候选全部排除。FOUNDATION 六条只是 A 的子集注释，不额外计数。A/B 在这里指实施写者，不表示完整 B 目标已覆盖。

本阶段仅表中前两文档；其他 33 个 Go 路径全部留给通过 docs checkpoint 后的实施阶段。Sprint 的 modify 计划语法在实际权限中严格为 append。

| 所有者 | 操作 | 精确路径 |
|--------|------|----------|
| A/docs | new | `docs/requirements/personal-team-space-runtime-deny-bound-v1.spec.md` |
| A/docs | append | `docs/agent/CURRENT_SPRINT.md` |
| A | new | `internal/access/chunk_ref.go` |
| A | new | `internal/access/chunk_ref_test.go` |
| A | modify | `internal/repository/sql_events.go` |
| A | new | `internal/repository/event_payload_fault_test.go` |
| A | modify | `internal/service/file.go` |
| A | new | `internal/service/chunk_cleanup.go` |
| A | modify | `internal/service/file_delete.go` |
| A | modify | `internal/service/delete_marker.go` |
| A | modify | `internal/service/file_bucket_delete.go` |
| A | modify | `internal/service/object_worker.go` |
| A | new | `internal/service/chunk_scope_fixture_test.go` |
| A | new | `internal/service/chunk_scope_test.go` |
| A | modify | `internal/reconcile/deletion.go` |
| A | new | `internal/reconcile/chunk_scope.go` |
| A | new | `internal/reconcile/chunk_scope_test.go` |
| A | modify | `cmd/server/ai.go` |
| B | modify | `internal/ai/sink.go` |
| B | modify | `internal/ai/indexer.go` |
| B | modify | `internal/ai/qdrant.go` |
| B | modify | `internal/ai/bm25.go` |
| B | new | `internal/ai/indexer_chunk_scope.go` |
| B | new | `internal/ai/indexer_chunk_jobs.go` |
| B | new | `internal/ai/qdrant_scoped.go` |
| B | new | `internal/ai/bm25_scoped.go` |
| B | new | `internal/ai/resource_fixture_test.go` |
| B | new | `internal/ai/indexer_chunk_scope_test.go` |
| B | new | `internal/ai/indexer_chunk_jobs_test.go` |
| B | new | `internal/ai/qdrant_scoped_test.go` |
| B | new | `internal/ai/bm25_scoped_test.go` |
| B | modify | `internal/ai/qdrant_test.go` |
| B | modify | `internal/ai/bm25_test.go` |
| B | modify | `internal/ai/integration_test.go` |
| B | modify | `internal/integration/qdrant_integration_test.go` |

- FOUNDATION 为两文档、`internal/access/chunk_ref.go`、`internal/access/chunk_ref_test.go`、`internal/repository/sql_events.go`、`internal/repository/event_payload_fault_test.go`。先 checkpoint，再冻结同哈希供 B 只读使用；不交叉编辑。
- B 的共享 executor API/source checkpoint 后，才给 A 只读快照完成 `cmd/server/ai.go` 最小接缝；不改 `main.go`，不宣称全局 startup-fatal 或在 EnsureCollection/warmup 前全部 fail-fast。
- 明确排除 §2 的 REST/Manager/分享/事件传输与通知/aggregation/replay/root 实现，Search/Chat/Agent、Coverage persistence、JobReader、其他 worker/timer 及 BM25 persistence；不迁移 query-side canonical／取消时点夹具。不能靠 total-Allow 假能力补齐缺口。
- 不改任何旧断言、测试名、预算、迁移、工程阈值或豁免。超出此表的必要改动须先报 `SCOPE_BLOCKER` 精确路径／原因，不扩大到目录。

## 4. 共享 API 与兼容合同（后续新增，当前未实现）

`internal/access/chunk_ref.go` 固定为纯内部值／能力，不依赖 Repository：

```go
type ChunkRef struct {
    TenantID string
    Bucket   string
    Key      string
    ObjectID int64
}

func ValidateChunkRef(ref ChunkRef) error

type ScopedChunkCleaner interface {
    DeleteScopedObjectChunks(ctx context.Context, ref ChunkRef) error
}
```

`internal/ai/sink.go` 新增能力，原 `ChunkSink` 两方法签名保持：

```go
type ScopedChunkSink interface {
    UpsertScopedObjectChunks(ctx context.Context, ref access.ChunkRef, chunks []repository.Chunk) error
    DeleteScopedObjectChunks(ctx context.Context, ref access.ChunkRef) error
}
```

- `ChunkRef` **恰有上述四字段**；无 VersionID、generation、principal、Allowed。版本身份通过实际版本行的 ObjectID 表达，不反解析 `@v`，不冒充 Restore/覆盖 generation 栅栏。
- `ValidateChunkRef` 拒绝不完整 scope 和非正 ObjectID；值必须显式、精确，原样保留，不补 default、不 trim/casefold、不按名字推归属。键业务校验仍属于 FileService+Storage，不另建第三套 key 命名政策。
- ref 只标识资源，不是授权 grant。允许来源仅为精确 Object/版本行、真正 Object NotFound 后完整一致 chunks、精确复读的持久 Event，或由其生成且已持久化的 v2 payload。客户端字段、system context、StorageKey 切段、最新同 key 行都不能补证明。
- 保留 `ChunkCleaner`、`WithChunkCleaner`、`ChunkCleaner()`、构造器和 fluent 签名。`WithChunkCleaner(nil)` 仍清除配置，是 AI-off 路径；配置了 legacy-only/typed-nil cleaner 则保留 fault，在 dispatch 报错，不调用旧 ID-only 方法。
- `WithChunkSink(nil)`、typed-nil、legacy-only 注册锁存配置 fault；`ValidateChunkSinks()` 与实际操作在受保护工作前返回，不能静默忽略或转旧方法。未注册任何 sink 不是坏注册。
- test-only scoped adapters 可在验证 ref 后向原 callback 精确转发一次、同一 ObjectID，保留旧 counters/errors.Is；生产不得用此方式恢复 ID-only sink fallback。

## 5. FileService / reconcile 精确清理

统一流程：判断真正 nil 兼容路径 → 验证配置的 scoped capability → 从实际行构造并对照预期 scope/ref → 只调用 scoped cleaner。校验失败不得发生 chunk 清理；错误交回原 warn-and-continue 调用点，**不将 cleaner fault 升级为对象删除失败**，也不新增 Space 准入。

| 真实分支 | 必须传递的来源 |
|----------|----------------|
| `file_delete.go` hard-delete versions | 每个实际枚举版本行，各自 ObjectID；不把 current ID 复用于旧版本 |
| `file_delete.go` soft delete / DeleteVersion | 分别为已加载对象／精确版本行 |
| `delete_marker.go` | 先前已加载的 current 对象，绝非新 marker；保持原 early lookup-error 行为 |
| `file_bucket_delete.go` | 每个枚举对象的 tenant/bucket/key/ID；保 marker 跳过、Blob 去重、multipart 次序 |
| `object_worker.go` quarantine | 实际加载对象／版本；保原授权、保护及事务删除事实 |
| `reconcile/deletion.go` | `cleanObjectChunks` 统一从传入真实 obj/version dispatch；key/version/soft-delete/retention/scrub 原调用继承 |

保留 WORM/legal hold 重查、Blob→DB 顺序、用量调整、配额、审计、Outbox、事件和原 warn 行为；不从 StorageKey 还原业务 scope，不修改 retention/scrub 的公共 cleaner 签名。同步清理并非全部拥有 durable 后继，失败后行消失不得宣称最终必清。

## 6. Indexer：来源、canonical rows 与真实 outcome

- 配置 fault、非法 ref、请求/job tenant 冲突在 storage/extractor/embed/tagger/本地删除/sink 前返回；system context 不消除冲突。这里是 scope/provenance 检查，不是 §2 的 Space boundary。
- ID-only 兼容入口先查精确 Object/版本，只有真正 `repository.ErrNotFound` 才可由完整一致 chunks 解析 ref。DB/context fault 保 cause，不降级为 absence；没有来源则 unresolved，零外部删除。
- 已有可信 event/v2 ref 时本地 chunks 可为空，但任何已观察 Object/chunks 必须完整且与 ref 一致；冲突先报错，不能删完再检查。能力/ref/证据验证后，才本地 chunks 删除 → scoped sinks。
- `prepareCurrentObject` 的 current NotFound、current.ID 不同、旧版本枚举三个分支均用原 obj/每个 version 的 ref，不以最新同 key 行代替旧对象。
- 正常无 sink 索引仍 **零次 `ListChunksForObject`**。有 sink 时只发送 Insert 后权威 reread 的真实 chunk ID/embedding，验证每行完整 scope、正且唯一 ID；非空 Insert 后 reread 意外为空或 mixed rows 是 fault，不解释成 explicit clear。
- 每个 sink 恰接收一次 scoped 操作，保因果错误。失败 upsert 后 Object 消失，配置 sink 的重试不得沿用 NotFound→nil 宣称 indexed；无新证明则 unresolved。
- 内部区分 indexed、skip、fault/unresolved。unsupported/empty/SSE-C 等原 skip 语义与指标保留；`ReindexStale` 只累计 indexed，遇 fault 返回 error，不吞错最终报 `n,nil`，skip 不虚增 n。

## 7. Qdrant 与 BM25 scoped 写入

### 7.1 Qdrant

1. 保留旧 Upsert/Delete 签名，但无 ref 返回明确 scope-required 错误，**零 HTTP**；SearchVectors/EnsureCollection 原行为不变。
2. Scoped Upsert 先验证 ref 和所有行，包括无 embedding 的行：tenant/bucket/ObjectID/ObjectKey 全等、chunk ID 正且唯一。非空向量检查 Dim/长度/一致性和 NaN/Inf 等 JSON 可编码性；完整请求可编码后才能做破坏操作。
3. Delete filter 精确四字段 AND：`tenant_id`、`bucket`、`object_id`、`object_key`，不能仅靠 object_id；scoped Upsert 内部只调用 scoped Delete。
4. 删除成功后才 PUT，保原 URL/method/api-key、点 ID/向量/payload、无 embedding 跳过和 `wait=true`。Delete 失败零 PUT；PUT 失败确实到达并保原 error，不宣称跨请求原子。
5. 有效 ref＋显式空 upsert 只做一次 scoped Delete、零 PUT；无 ref＋空输入仍 fault。Indexer 异常空 reread 不属于这种显式 clear。
6. 旧缺 scope points 不匹配新 filter，**保留、不回填、不扩删**。成功只代表该 ref 的 scoped 操作，不代表所有 legacy object_id points 清空，更不代表 query-side canonical/Space 保护完成。

### 7.2 BM25

- 保留独立旧纯内存算法方法；生产增量只能 scoped。不能为消除 fault 回退旧 ID-only 写。
- 同一写锁内，在修改 maps/objDocs/DF/文档与长度计数/dirty 前，验证所有 incoming rows、将替换的旧 docs 和 chunk-ID 冲突。同 objectID 混 scope、跨 ref 覆盖或任一坏行整体拒绝，状态完全不变。
- scoped replace/delete 只改精确 ref，保其他 tenant。并发读写仍一致，不先 remove 再发现错误。
- `BuildFromRepo` warmup 验证完整 object/chunk facts、每页 scope、分页前进与取消；不填空 TenantID。先完整 staging，成功后原子替换该 tenant，不在错误/取消/不完整时部分提交。
- 本片 warmup **不实现** Coverage/P0/P1；完整 B 后续才追加 §2 聚合准入。旧全 corpus 独立算法 oracle、replacement 次数、并发规模及 persistence 测试均不得同路径自证或减弱。

## 8. 持久 Event 权威复读与严格解码

- AI 仅对相关 lifecycle event 使用私有 exact-reread adapter，不新建共享 events authority 或扩 Repository；先验证非空完整 tenant、正 event ID，再调用 `ListEventsAfter(ctx, tenant, eventID-1, 1)`。
- 首行 ID 必须精确相等，type/tenant/bucket/key/ObjectID/payload 与输入完全一致，然后使用权威行；gap、伪造、矛盾、未持久事件均不得补修。nil/empty payload map 可语义相等；CreatedAt/RequestID 不作为身份比较字段。
- `scanEvents` 必须将 `unmarshalKV` error 传播给 `ListEventsAfter` 和 `NextUnconsumedEvents`，保 cause，不返回 partial-success 或把坏 JSON 当空权威 payload；正常空 payload 和 tenant/event 选择语义不变。
- bridge 与 inline 都遵守同一来源检查。enqueue 失败不消费；成功 enqueue 才 durable handoff，inline 失败亦不消费。非相关事件保原语义，不将本片扩大为 bus/transport/SSE/通知准入。

## 9. `delete_chunks` v2 与 claimed-job 执行

固定新增内部持久 JSON 合同：

```json
{"schema_version":2,"object_id":17,"tenant_id":"alpha","bucket":"docs","key":"a.txt","event_id":23}
```

- 字段必须完整、类型正确、ID 正数，ref 校验通过。只由 §8 精确事实生成；不新增 queue schema、不更改外部 `deleted/notify@1.1` payload。
- dedupe 必须对 `(delete_chunks, 2, tenant_id, bucket, key, object_id, event_id)` 使用版本化、无歧义编码，区别于旧 `delete_chunks:<id>`；不能碰到旧 dedupe 就声称 scope 已保存。Enqueue 返回成功须代表持久行拥有所需 v2 事实。
- B 固定提供 `Indexer.ValidateChunkSinks() error`、`Indexer.ExecuteChunkJob(ctx context.Context, job repository.Job) error`。后者处理两个注册 job kind，接收**完整 ClaimJob 持久行**，不只传 DecodeObjectID 的结果。
- 每次 claimed execution 校验 kind/schema/正 ID、job tenant 与 payload/ref 一致及当前已观察证据；包括 system contexts。unknown schema、坏 JSON、错 tenant、来源矛盾均零外部删除，不成功 complete。
- v2 的权威持久 payload 必须在 Object/chunks/Event 删除或 pruning 后仍可用于相同 ref 的重试；仍须拒绝现存矛盾和真实读取 fault，不能把 DB/context 错误当事件已消失。不得依赖临时 context scope 或要求无限保留 Event。
- 保持 `index_object` 原编码、DecodeObjectID 兼容与合法旧能力。v1 ID-only `delete_chunks` 每次重新从权威 Object、或 genuine NotFound 后完整一致 chunks 解析；没有证据则 unresolved，通过原有限 retry→failed，绝非 nil success。
- 本地删除后远端失败，下一次 v2 仍保 payload/ref；v1 若只剩 chunks 且上次已删，则下一次失证必须失败。已有 scope 不等于远端无残留。
- queue depth、去重持久机制、backoff、attempts、terminal/reaper、claim fencing 和 handler 注册顺序不变，不承诺 exactly-once。
- `cmd/server/ai.go` 只将两个实际 handler 的完整 claimed job 委托 executor，并在 indexer subscription/reindex start 前检查 sink 配置；保 `buildAIComponents` 签名和 main 合同，完整 B 启动排序另行实施。

## 10. 失败／重试真值表

| 情况 | 必须结果 |
|------|----------|
| true nil cleaner | AI-off 兼容，零 cleanup；不是全局 Allow |
| 配置 legacy-only/typed-nil cleaner | scoped dispatch fault、零旧方法调用；原对象删除 warn 后继续 |
| sink 坏配置／非法 ref／已观察 scope 矛盾 | 相关受保护工作前 error，零 fallback、零外部删除；不报 cleaned/indexed |
| Qdrant 任一坏行／坏向量 | 整体拒绝，零 HTTP；不能先删再校验 |
| Qdrant Delete 失败 / Delete 成功后 PUT 失败 | 分别零 PUT／确实到达 PUT 并返回原错误；不伪成功、不承诺 rollback |
| BM25 refused write / warmup fault | 原 docs/objDocs/DF/counts/dirty 不变，其他 tenant 不变 |
| v2 本地删完远端失败，源行与 Event 消失 | 关闭重开／重新 claim 后，用相同持久 ref 精确重试 |
| v1 全失证 | 零外删、unresolved、原有限重试终态；不自动升级为可信 v2 |
| 伪 Event／strict decode fault／enqueue 失败 | 不产生不安全 job/清理，不消费该事件；保 cause |
| 同步 cleaner 失败后原行消失 | 保 warn-and-continue，不保证 durable 后继，不声称所有残留终将清除 |

## 11. 验证矩阵（全部待实施、待实测）

所有旧测试、断言、真实磁盘 `t.TempDir()` SQLite、完整 Migrate、local FS、原连接／PRAGMA／sync／输入规模／时间预算保持；不换内存库/盘、不缓存 schema、不靠 skip、改名或缩范围刷绿。

| 面／未来文件 | 必须辨别的 oracle |
|--------------|--------------------|
| access chunk_ref tests | 合法精确值、不完整 scope、非正 ID；无 default/归属推断/授权字段 |
| repository event_payload_fault tests | 隔离磁盘库持久坏 JSON；两个真实 reader 都保 cause 返回错误、无 partial-success；合法空 payload 与精确 tenant/event controls |
| service chunk_scope tests | 实际 hard/soft/DeleteVersion/marker/bucket/quarantine 分支各 ref；nil/typed-nil/legacy-only、远端失败仍 warn、零旧调用、Blob→DB 与保护/事实顺序 |
| reconcile chunk_scope tests | 真实 key/version/soft-delete/retention/scrub cleanup callers；准确 ref、marker skip、保护重查、usage 次序及 warn；原 scrub/job 断言不变 |
| indexer_chunk_scope tests | exact Object→genuine NotFound/chunks 来源矩阵、DB fault、tenant/system 冲突、三个旧版本分支；canonical 空/mixed 不 clear；无 sink 零 reread；ReindexStale 真 outcome |
| indexer_chunk_jobs tests | 真实 InsertEvent/jobs/ClaimJob、关闭重开；v2 remote 失败后本地与 Event 消失仍同 ref；错 tenant/schema/伪造/gap/JSON 拒绝，enqueue 失败未消费，v1 失证原有限失败 |
| qdrant_scoped tests | stateful mock 两 tenant 同 ObjectID＋旧缺 scope points；删 A 后 B/legacy 状态与字节保留，不只检查 JSON；完整坏输入零 HTTP、explicit empty、Delete/PUT 真实错误路径 |
| bm25_scoped tests | scoped replace/delete 与跨 tenant 保留；拒绝时 maps/DF/counts/dirty 全不变；warmup 事实／分页不前进／取消／失败整体不提交；并发读写 |
| 允许调整的旧 AI fixtures | scoped fakeSink 每个原回调一次、原 ID/embedding/errors.Is、countingRepo 零 reread oracle；独立 corpus oracle、全部替换次数与并发规模不变；bridge/inline 改真实持久事件，不减原断言 |
| 单个 Qdrant integration fixture | 只迁移同一完整 ref 的写删；保 reachability skip、double provisioning、dim=3、near/far、排名及删后零 hits；本片仅离线类型检查，不运行实库 |
| root 最小接缝 | B API/source checkpoint 后运行原 cmd/server 包；检查两个 handler 实际转交完整 job，subscription/reindex 前配置检查；不以此声称完整启动 preflight |
| A 故障注入 | 仅 owned Go 临时丢弃 payload error，reader 新回归须真实断言红；保存 before/injected/restored hash、diff、command/exit/log，finally 精确恢复再绿 |
| B 故障注入 | 仅 owned Go 临时去掉 Qdrant tenant predicate，stateful 跨 tenant oracle 须真实红；同样保存证据并精确恢复，不弱化 oracle |
| 完整 B 后续 | §2 每面 bound/fault/typed-nil/disabled/tenant 隔离负例＋unbound 原政策 controls；P0/D/P1、撤销、replay、egress 和 root 包装矩阵另行验收，不借 Wave-1 绿色抵扣 |

## 12. 检查点、命令与交接门槛（待执行，不是本阶段运行记录）

1. docs checkpoint 先确认准确两文档、完整历史 Sprint 前缀、其他项目文件未变及新增 Go 仍不存在；使用 runner 提供命令并保存真实 exit/results，未通过不得编辑 Go。本阶段不创建／修改控制器脚本。
2. A FOUNDATION 实施与检查后冻结复制；B 不改这六路径。并行阶段各自仅拥有 §3 精确路径；B 发布 API/source 后再给 A 根接缝只读快照。
3. 后续 Go 文件须 gofmt、≤500 行，函数≤50 行、复杂度≤10，无新 helper 平台／依赖／豁免；保留所有红灯，自己的实现失败只能修 owned 代码，范围外必要修改先阻断。
4. 未来定向普通／race 命令如下（全部当前未运行）；Go 子进程固定 `GOMAXPROCS=4 GOFLAGS=-p=1`，umask 022、count=1、每包 300s：

```text
GOMAXPROCS=4 GOFLAGS=-p=1 go test -count=1 -timeout 300s ./internal/access/ ./internal/repository/ ./internal/service/ ./internal/reconcile/
GOMAXPROCS=4 GOFLAGS=-p=1 go test -race -count=1 -timeout 300s ./internal/access/ ./internal/repository/ ./internal/service/ ./internal/reconcile/
GOMAXPROCS=4 GOFLAGS=-p=1 go test -count=1 -timeout 300s ./internal/ai/
GOMAXPROCS=4 GOFLAGS=-p=1 go test -race -count=1 -timeout 300s ./internal/ai/
GOMAXPROCS=4 GOFLAGS=-p=1 go test -count=1 -timeout 300s ./cmd/server/
GOMAXPROCS=4 GOFLAGS=-p=1 go vet -tags=integration ./...
```

5. controller 合并仅此 35 路径后，在同源依序运行 `GOMAXPROCS=4 GOFLAGS=-p=1 make check` → `GOMAXPROCS=4 GOFLAGS=-p=1 python3 cli.py accept` → `GOMAXPROCS=4 GOFLAGS=-p=1 make test-race`，再 `make web-check` → `make build` → `make web-smoke` → `git diff --check`。这些是 controller 后续门禁，非本 docs writer 执行许可；原全范围/selector/timeout 保留，失败阻断下游，不授权改继承资产。
6. 全程守卫 exact allowlist、同源 hash、旧 tests/assertions、Sprint 完整前缀、接口、迁移、配置、依赖和资产；独立只读 review 后才讨论本基础片验收。模型报告、静态编译、历史成功及 docs checkpoint 都不替代工程门禁。
7. 真实 PostgreSQL/Qdrant、外部身份／存储／KMS、生产、代理/HTTP2、压力/RSS、登录后业务 E2E 均未验证，费用 unknown。最终报告需列精确改动、实际命令/exit/results、未运行项和交接，不写入项目报告文件。

**当前只有需求与待验合同；未实现、未测试。完整 B 与 Space runtime deny-bound 仍未交付；本片不是生产就绪证明。**
