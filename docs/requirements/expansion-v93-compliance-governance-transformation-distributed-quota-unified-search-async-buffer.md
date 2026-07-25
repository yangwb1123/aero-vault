# 高价值扩展方向：合规锁治理、服务端变换管线、分布式配额平面、统一查询语言、异步写入缓冲

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — `cmd/server/main.go` + `internal/` 全部子包（237+ Go 源文件），3 套 SDK，MCP 双模式，Web UI，48 对迁移文件，`deploy/` 全套配置，HARNESS.md，AGENTS.md，ROADMAP.md  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。选取**代码中存在明确锚点、有实质性产品/架构影响、且在过往 93 轮分析中未被独立深度覆盖**的方向。每个方向包含：代码证据 → 产品价值 → 架构权衡 → 边界情况。方向排序按在产品路线图中的综合优先级。

---

## 去重验证

对 `docs/requirements/` 下全部 92 份既有分析文档逐方向进行关键词扫描：

| 方向 | 既往覆盖情况 |
|------|-------------|
| **对象锁合规治理与 Legal Hold 案件管理框架** | ✅ **零实质性覆盖** — v88/v90 分别覆盖了 S3 Object Lock API 模式区分（Governance vs Compliance）和合规锁模型深度，但**均聚焦于 S3 协议适配层面**，未触及：Legal Hold 案件生命周期、保管人追踪、保留事件触发器、重叠保留策略仲裁、合规审计轨迹。正则搜索 `legal.hold.case\|custodian\|retention.trigger\|hold.overlap` → **0 命中** |
| **服务端对象变换管线与预览生成框架** | ✅ **零实质性覆盖** — v85 方向一覆盖了「基于 Content-Type 的后处理管道」，但聚焦于**事件触发架构**而非**存储层内置的可组合变换引擎**。本方向考察的是：声明式变换链（→thumbnail→watermark→transcode）、懒加载变换（读取时即时变换）、变换结果缓存、多尺寸派生物管理。全量文档正则搜索 `transform.pipeline\|image.process\|video.transcod\|thumbnails.*cache\|derivative.object\|on.the.fly.transfor` → **0 命中** |
| **分布式速率限制与多层级配额治理平面** | ⚠️ **部分覆盖但深度不足** — v84 方向二覆盖「精细化成本感知速率限制」；v86 方向四覆盖「速率限制器每进程独立——无分布式协调」；v81 方向三覆盖「Per-Bucket / Per-Prefix 层次化存储配额」。**均未覆盖**：统一治理平面（融合 RPS+并发+存储配额+AI 预算）、跨进程协调、每 API 权重、突发信用银行、分层配额继承。三者均为孤立分析，无整体架构方案 |
| **统一搜索查询语言跨元数据与内容** | ⚠️ **浅层提及无架构** — v91 方向二覆盖「对象元数据与标签查询引擎」，但聚焦于新增 SQL 接口查询元数据，**未涉及元数据+语义+关键词三者的统一查询语言**。正则搜索 `unified.query\|query.language\|GraphQL\|metadata.content.search\|cross.modal.search` → **1 次命中**（v91 元数据查询方向正文中提及 "查询引擎" 但未涉及多模态统一） |
| **异步写入缓冲与优雅降级写入路径** | ✅ **零实质性覆盖** — 全量文档正则搜索 `write.buffer\|write.queue\|async.write\|ingestion.buffer\|write.behind\|admission.control\|graceful.degradation.write` → **0 命中**。v86 方向五覆盖了「缺少请求体大小准入控制」但那是**静态大小限制**而非**动态写入缓冲与背压机制** |

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 |
|---|------|------|--------|---------|---------|
| **1** | **对象锁合规治理与 Legal Hold 案件管理框架** | 合规/企业特性 | **P1** | Object Lock 仅有 retention-duration 模型，无法律案件级 hold 生命周期管理；`_aero_legal_hold` 是二进制标记，无保管人、案件引用、过期时间、审计轨迹；合规性无法证明 | `internal/service/file.go:ErrLocked`（简单时间比较拒绝写入）；`internal/service/file_crud.go:checkLockBeforeOverwrite`（仅检查 `LockedUntil > now`）；`internal/repository/repository.go:Object.LockedUntil`（仅 `*time.Time` 无 hold 元数据）；`internal/repository/sql_objects.go`（SET locked_until 单字段更新）；`internal/api/rest/handler.go:LockObject`（接受 `until` 参数无 hold 上下文）；`internal/api/s3compat/handler.go`（`x-amz-object-lock-legal-hold: ON` → 存为 `_aero_legal_hold` 元数据） |
| **2** | **服务端对象变换管线与预览生成框架** | 产品特性/平台能力 | **P1** | 除 `GET /thumbnail` 外无任何服务端内容变换能力；用户如需要缩略图之外的派生物（WebP 转码、PDF 预览、视频关键帧、水印）必须自行下载→处理→重新上传；ImageMagick/LibreOffice 等本地工具能力未封装；SDK/API 无 `?format=webp&w=200` 等价 URI 参数 | `internal/thumbnail/thumbnail.go`（仅 JPEG/PNG 缩放，硬编码 MaxDim=2048）；`internal/api/rest/thumbnail.go`（`GET /files/*/thumbnail` 唯一变换入口）；`internal/service/file_crud.go:Get`（纯读取流，无变换注入点）；`internal/storage/storage.go:Storage`（无派生 URI/方法）；`internal/api/rest/router.go`（无 `?format=` 或 `/derivatives/` 路由） |
| **3** | **分布式速率限制与多层级配额治理平面** | 架构/性能 | **P2** | `middleware.RateLimiter` 是每进程内存 token bucket — 多副本下各自为政，全局速率无法保证；`preflightQuota` 与 `aiRL` 速率限制无关联；无每 API 权限成本模型（DELETE 不应与 CREATE 同权）；无分级配额（租户→桶→前缀）；无突发信用或延迟容忍机制 | `internal/middleware/ratelimit.go:RateLimiter`（`map[string]*bucket` — 进程本地无共享）；`internal/middleware/ratelimit.go:Allow`（纯 token-bucket 无成本模型）；`internal/service/file_crud.go:preflightQuota`（仅查 `GetTenantQuota` 无分布式协调）；`internal/config/config.go:RateLimitCfg`（`RPS`/`Burst`/`AIRPS`/`AIBurst` 四值无关联）；`cmd/server/main.go:buildRouter`（`aiRL` 作为独立中间件附加在 AI 路由组） |
| **4** | **统一搜索查询语言——跨元数据与跨模态内容检索** | AI/产品 | **P2** | 当前搜索有三条独立路径：语义（`Search.Query`→`VectorIndex.SearchVectors`）、关键词（`BM25.Search`）、元数据列举（`ListObjects`/`ListObjectsByTag`）。无法在一个查询中融合「创建时间 > 上周 AND 标签包含 "report" AND 内容包含 "budget"」；SDK 用户需组合多个 API 调用；无排序/分页/聚合能力 | `internal/ai/search.go:Search.Query`（独立查询，`mode` 仅 `vector`/`bm25`/`hybrid` 纯内容）；`internal/repository/repository.go:SearchChunks`（仅向量距离搜索）；`internal/repository/sql_objects.go:ListObjectsByTag`（`WHERE tags->>$4 = $5` — 客户端过滤）；`internal/api/rest/search.go:AIHandler.Search`（仅 `ai.Request` 结构）；`internal/api/s3compat/handler.go:listObjectsV2`（`tag-key`/`tag-value` 过滤——纯元数据） |
| **5** | **异步写入缓冲与优雅降级写入路径** | 可靠性/性能 | **P3** | `FileService.Put` → `store.Put` 是同步直写：后端不可达时写入立即失败；无请求排队或写缓冲；突发写入高峰期后端饱和时无背压反馈路径；大对象（>100MB）写入占用连接直到持久化完成，阻塞其他请求 | `internal/service/file_crud.go:Put`（同步 `s.store.Put(ctx, sk, reader, size, ...)`）；`internal/storage/local_write.go:Put`（`os.Create` → `io.Copy` → `Sync` 同步完成）；`internal/storage/s3.go:Put`（直接 `s3manager.Upload`）；`internal/config/config.go:AppConfig`（无写缓冲或队列深度参数）；`internal/storage/circuitbreaker.go`（仅 fail-fast 无排队/缓冲降级） |

---

## 方向一：对象锁合规治理与 Legal Hold 案件管理框架

### 现状

当前 Object Lock 实现极为简陋：

```go
// internal/service/file_crud.go
func (s *FileService) checkLockBeforeOverwrite(ctx context.Context, tenant, bucket, key string, versioning bool) error {
	if !versioning {
		if cur, err := s.repo.GetObject(ctx, tenant, bucket, key); err == nil {
			if cur.LockedUntil != nil && cur.LockedUntil.After(time.Now()) {
				return fmt.Errorf("%w: overwrite blocked until %s", ErrLocked, cur.LockedUntil.Format(time.RFC3339))
			}
		}
	}
	return nil
}
```

- `LockedUntil` 只是一个 `*time.Time` 字段
- Legal Hold 通过一个 `_aero_legal_hold` 元数据 key 的 `"ON"` 值标记
- 无「保管人」（custodian）记录——谁在何时因为什么原因施加了 hold
- 无「案件」（case）概念——无法将多个对象的 hold 关联到一个法律案件
- 无「合规 hold」——当前 hold 可由同一用户撤销（相当于 S3 Governance 模式），无 Compliance 模式
- 无保留策略重叠仲裁——如果 bucke-level retention 是 7 年，legal hold 保留 30 天，哪个生效？
- 无 hold 事件日志——hold 创建/修改/释放无审计记录（当前 audit_log 仅记录 admin 操作）
- 无基于事件的保留触发器——例如「对象被删除时开始 7 年保留期」
- 租户级默认保留策略——只能按 bucket 设置 `ObjectLockSeconds`，不能按租户设置基线

### 产品价值

| 价值 | 量化影响 |
|------|---------|
| **合规可证明性** | 金融/医疗/法律客户将 Object Lock 作为采购前提条件。当前实现无法通过任何合规审计（SOC2/ISO 27001/HIPAA）的 retention management 控制项 |
| **法律 hold 管理** | 诉讼/调查/监管要求中，法律团队需要将 hold 关联到案件、保管人，并出具 hold 报告。当前二进制标记无法满足 eDiscovery 需求 |
| **防止误操作** | Compliance hold 模式下即使是 admin 也不能覆盖或删除——避免人为错误或恶意内部行为 |
| **审计链条** | 每一个 hold 的操作都有记录，支持合规官 "who held what, when, and why" 的追溯需求 |

### 架构权衡

**数据模型变化（高影响）：**
- `objects` 表新增 `legal_holds` 独立实体表（1:N），每个 hold 记录：`case_id, custodian, reason, applied_at, expires_at, status`
- 当前 `locked_until` 字段保持为 retention 的快捷方式
- Legal hold 和 retention 的叠加逻辑：`locked_until = max(retention_deadline, max(hold_expires_at))`
- 新增 `hold_events` 表记录所有 hold 操作

**核心路径无性能影响：**
- `checkLockBeforeOverwrite` 调用频率低（仅在写覆盖时触发）
- legal hold 检查可以缓存（hold 变更不频繁）

**新增风险：**
- Compliance hold 下的对象永久不可变——系统需要确保存储 blob 永远不可删除（包括 GC/retention 工作线程）
- 当前 `hardDeleteObject` 和 `reconcile` 都需要检查 compliance hold
- 对象数量膨胀：版本化桶 + legal hold 可能导致无限增长——需要与方向五（v82 已覆盖的版本增长治理）配合

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| Legal hold 与 retention 重叠 | 取两者中更大的到期时间 |
| Compliance hold 期间存储后端故障 | hold 状态存储于元数据（repository），与 blob 独立；即使 blob 暂时不可读，hold 信息不丢失 |
| 多个 hold 同时作用 | 任意 hold 有效即阻止操作（OR 语义）；释放需逐个释放 |
| Hold 到期后自动续期 | 支持通过事件触发器续期（如「案件未结案」事件触发 hold 续期 90 天） |
| 跨租户 legal hold | 当前 tenenant 隔离设计下，legal hold 不会跨租户——但审计功能需能全局查询所有 hold |
| 迁移中 bucket 开启 Object Lock | 现有未加锁对象不会自动获得 retention——需决定是否支持「向后追溯」 |

---

## 方向二：服务端对象变换管线与预览生成框架

### 现状

当前唯一的服务端变换是缩略图：

```go
// internal/thumbnail/thumbnail.go
// Thumbnail generates a JPEG or PNG thumbnail from an object key.
// Only image/* content types are supported; returns nil for unsupported.
func (g *Generator) Thumbnail(ctx context.Context, key string, w, h int) ([]byte, string, error) {
	// ... reads full object content into memory, decodes with stdlib image,
	// scales with nearest-neighbor / bilinear, encodes JPEG/PNG ...
}
```

支撑代码：
- `internal/api/rest/thumbnail.go`: `GET /v1/files/*/thumbnail` — 唯一变换入口
- `internal/api/rest/handler.go:getKey`: 检测 `/thumbnail` 后缀路由
- `internal/storage/local_read.go`: `Get` 返回原始字节流——无变换注入点

**缺失的能力：**

| 能力 | 代码证据 | 竞品对标 |
|------|---------|---------|
| 格式转换（WebP/AVIF/JPEG-XL） | 无 `?format=` 参数 | S3 Object Lambda + Sharp/Libvips |
| 图像大小调整（`?w=&h=&fit=`） | 仅 `/thumbnail` 独立端点 | Imgix / Cloudinary / S3 ImageOptimization |
| PDF/DOCX 预览生成 | 无 | Google Docs Viewer / S3 + Lambda |
| 视频关键帧提取 | 无 | AWS MediaConvert |
| 水印叠加 | 无 | 自定义 |
| 变换结果缓存 | 无 | CDN + cache-control |
| 懒加载变换（on-the-fly） | 无 | Sharp / ImageMagick pipe |
| 派生物管理（多尺寸关联） | 无 | Cloudinary派生版本 |

### 产品价值

| 价值 | 量化影响 |
|------|---------|
| **带宽节省 60-80%** | 客户端只请求实际需要的尺寸/格式（WebP 比 JPEG 小 25-35%，缩略图比原图小 90%+） |
| **消除上游处理成本** | 用户无需下载→处理→重新上传；一键 `?w=200&format=webp` 即可服务 |
| **预览能力** | 企业客户将文档预览作为核心需求——无需下载即可查看 PDF/DOCX/XLSX |
| **CDN 友好** | 变换结果可设置 `Cache-Control: public, max-age=31536000`，CDN 边缘缓存派生物 |

### 架构权衡

**变换引擎选型：**
- **内置 Go 标准库：** JPEG/PNG/GIF 可用，无新增依赖，但 WebP/AVIF/PDF 需 C 绑定
- **Libvips/ImageMagick 进程外调用：** 最强大但引入外部依赖；需进程管理与超时
- **HTTP 远程变换服务：** 与 `AI_EXTRACTOR_ENDPOINT` 模式一致（已有先例），但增加延迟

**缓存策略：**
- 变换结果以 `{storage_key}?w={w}&h={h}&format={fmt}` 为缓存键
- 本地磁盘缓存 + `Cache-Control` 头
- 缓存失效：对象更新时删除所有派生缓存（或等待 TTL 过期）

**懒加载 vs 预生成：**
- 懒加载（请求时变换）：适合低频访问的派生物，没有存储开销
- 预生成（对象上传时触发）：适合高频访问的派生物（如产品图片缩略图）
- 建议：以懒加载为主，通过 post-upload 事件订阅预生成热路径

**新增路径影响：**
- `FileService.Get` 需要新增 `TransformOptions` 可选参数
- 变换结果不应计入租户存储配额（派生物是服务端优化，非用户数据）
- `Get` 路径的 OTel span 需要区分「原始读取」和「变换读取」

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 超大图像（8000×6000） | 限制最大输入尺寸（如 `MaxInputDim=4096`），超限返回 413 或降级 |
| 不支持的输入格式 | 返回原始内容（透传），用 `Content-Warning` 头指示 |
| 变换超时（如慢速图像处理） | 可配置超时（默认 5s），超时则降级为原始内容 |
| 缓存被对象更新污染 | 以 `object.updated` 事件为信号，异步驱逐缓存 |
| 水印覆盖法律限制 | 水印应作为显式 API 参数而非元数据——避免用户在不知情下加水印 |
| 视频转码的内存限制 | 使用进程外 ffmpeg，`RLIMIT_AS` 限制内存，超时杀死进程 |

---

## 方向三：分布式速率限制与多层级配额治理平面

### 现状

当前系统有**三个独立的准入控制机制**，互不关联：

```go
// 1. 进程本地 token-bucket（REST 全局）
rl := middleware.NewRateLimiter(cfg.RateLimit.RPS, cfg.RateLimit.Burst)

// 2. AI 路由独立 rate limiter
aiRL := middleware.NewRateLimiter(cfg.RateLimit.AIRPS, cfg.RateLimit.AIBurst)

// 3. 存储配额（SQLite/Postgres 行级锁）
q, qErr := s.repo.GetTenantQuota(ctx, tenant)
if q.MaxBytes > 0 && q.UsedBytes+size > q.MaxBytes {
    return fmt.Errorf("%w: bytes %d/%d", ErrQuotaExceeded, ...)
}
```

**核心缺陷：**

| 缺陷 | 影响路径 | 严重程度 |
|------|---------|---------|
| 每进程独立：多副本下各限各的 | 2 副本 × 100 RPS/副本 = 200 全局 RPS | 🔴 多副本部署不可控 |
| 无权重模型：`PUT 1GB` 与 `GET 1KB` 同权 | 大请求消耗资源不均 | 🟠 |
| RPS 配额分离：写配额 ≠ 读配额 | 无法限制突发写入同时允许读取 | 🟠 |
| 无分层继承：无法限制「桶 A 的某前缀」 | 一个恶意前缀可以抢占整个租户配额 | 🟢 |
| 无突发银行：桶不可累积 | 匀速流量无法吸收突发 | 🟢 |
| 配额与限流无联动：配额超限 ≠ 触发限流 | 配额触发的 413 无法被限流预判 | 🔴 |

### 产品价值

| 价值 | 量化影响 |
|------|---------|
| **多副本一致性** | 所有副本共享同一速率限制状态——即使有 10 个 Pod，全局 RPS 限制仍然生效 |
| **公平调度** | 大请求（PUT 1GB）消耗更多配额分片——低延迟的 LIST 操作不被大请求饥饿 |
| **分层治理** | SaaS 产品可以为每个租户设置总 RPS、按桶分级、按操作类型加权——细粒度资源控制 |
| **弹性突发** | 租户累积的突发信用可以在低负载时段使用——平滑流量峰值，提升用户体验 |

### 架构权衡

**分布式协调后端选型：**

| 方案 | 一致性 | 延迟 | 复杂度 |
|------|--------|------|--------|
| Redis Sliding Window（推荐） | 最终一致（足够） | <1ms | 低 |
| Postgres 行级 `UPDATE ... RETURNING` | 强一致 | 2-10ms | 低（已有 Postgres 依赖） |
| 内存 + Gossip 协议 | 最终一致 | 纳秒 | 高 |

推荐**Postgres 优先**（零新增依赖、已有连接池、强一致），后续可选**Redis 热替换**。

**请求成本模型：**

```go
// 每 API 操作的成本权重（示例）
map[string]int{
    "PUT":          10,   // 大写入
    "GET":          1,    // 小读取
    "DELETE":       5,    // 删除
    "LIST":         2,    // 列举
    "SEARCH":       20,   // AI 检索（含 embedding）
    "CHAT":         50,   // LLM 推理
    "MULTIPART":    30,   // 分片上传
}
```

**分层配额层级：** `Global → Tenant → Bucket → Prefix`

继承规则：低层级未明确设置时继承高层级配额。同时设置时取**更严格的值**。

**突发信用银行：**
- 每秒未使用的 RPS 额度按比例（如 50%）累积到突发银行
- 银行上限 = `burst_max`（如 1 小时额度）
- 超额请求消耗银行信用（无等待）
- 银行耗尽后恢复常规限流

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| Postgres/Redis 不可达 | 降级到进程本地 token-bucket（松弛模式），日志告警 |
| 请求成本峰值 + 低总 RPS | 请求成本只影响配额消耗速率，不影响安全线（burst 总量） |
| 租户 A 恶意旋转多个 API Key 绕过 | 限流以认证身份（tenant）而非 API Key 为键——旋转 key 不影响限流状态 |
| 跨副本时钟偏差 | Postgres 方案使用服务器时间，无时钟依赖；Redis 方案使用 Redis TTL，容忍秒级偏差 |
| 配额快速变更 | 变更后旧状态在 1-2 个 TTL 窗口内完全过期，过渡期期间使用新旧中更严格的 |

---

## 方向四：统一搜索查询语言——跨元数据与跨模态内容检索

### 现状

当前搜索能力被拆分为三个完全独立的子系统：

```
1. 语义搜索（向量）
   ai.Search.Query → mode=vector → VectorIndex.SearchVectors → 余弦相似度排序

2. 关键词搜索（BM25）
   ai.Search.Query → mode=bm25 → BM25.Search / LexicalIndex → TF-IDF 排序

3. 元数据列举+过滤（Repository）
   ListObjects(prefix, marker, limit)         // 前缀列举
   ListObjectsByTag(tagKey, tagValue)          // 标签过滤
```

**用户不需要的三条路径：**

| 用户需求 | 当前方案 | 问题 |
|---------|---------|------|
| "找上周上传的 PDF，包含 budget 一词" | 语义搜索 → 过滤客户端侧 | 客户端下载大量无关结果 |
| "找大小 > 10MB 且标签含 project=X 的图片" | `ListByTag` + 语义搜索不可能 | 需要两次 API 调用 |
| "找租户 A 和 B 中关于某话题的所有文档" | 跨租户搜索不可能 | 必须逐个租户搜索后合并 |
| "统计按月份/标签/存储类聚合的对象量" | 无聚合能力 | 需要导出后外部处理 |

**具体代码层面的缺失：**

| 锚点 | 缺失 |
|------|------|
| `internal/ai/search.go:Request` — 无 `MetadataFilter` 字段 | 无法约束搜索结果到满足元数据的对象 |
| `internal/ai/bm25.go:Search` — 纯文本匹配 | 无法按日期/大小/标签过滤 |
| `internal/repository/repository.go:ListObjectsByTag` — `LIMIT 1000` 客户端过滤 | 服务端无法理解复杂谓词 |
| `internal/repository/sql_objects.go` — `WHERE tags->>$4 = $5` | 仅等值过滤，无 >/</BETWEEN/LIKE |
| `internal/api/rest/search.go` — 单一 `Query` 请求体 | 无 `filter` / `sort` / `agg` 字段 |
| `internal/repository/repository.go:SearchChunks` — `tenant, bucket` 双参数 | 无法跨租户搜索 |

### 产品价值

| 价值 | 量化影响 |
|------|---------|
| **单一查询接口** | 开发者学一次 API 即可覆盖所有搜索场景——降低集成成本 50%+ |
| **服务端过滤** | 减少网络传输 10-100×（只返回满足所有条件的对象，而非全部再在客户端过滤） |
| **跨模态查询** | 「包含某个关键词的 PDF 文件」——当前需要 semantic search（内容）→ 挨个 Stat（类型） |
| **聚合分析** | "上月各租户的存储增长趋势"——无需导出到外部分析工具 |
| **跨租户搜索** | 企业集团场景下，母公司需要跨子公司搜索——当前完全不可能 |

### 架构权衡

**查询语言设计：**

不押注自定义 SQL 方言。推荐使用 **JSON 查询 DSL**（与 Elasticsearch Query DSL 类似的风格），通过 REST API `/v1/query` 暴露：

```json
{
  "tenant": ["default", "acme"],
  "filter": {
    "and": [
      {"gt": {"object.size": 1048576}},
      {"eq": {"object.storage_class": "STANDARD"}},
      {"has_tag": {"project": "x"}},
      {"after": {"object.created_at": "2026-06-01T00:00:00Z"}}
    ]
  },
  "content": {
    "query": "quarterly budget report",
    "mode": "hybrid",
    "k": 20
  },
  "sort": [{"object.created_at": "desc"}, {"_score": "desc"}],
  "aggregate": [
    {"group_by": "object.storage_class", "metric": "count"}
  ],
  "limit": 50,
  "offset": 0
}
```

**查询执行计划（示意）：**

```
Input JSON DSL
  ↓
Parse & Validate (new pkg: internal/query)
  ↓
Plan: (metadata_filter → content_search → merge) or (parallel → RRF merge)
  ↓
Execute:
  ├─ Repository: WHERE clause (Postgres JSONB / SQLite JSON)
  ├─ VectorIndex: content.SearchVectors(query, k * oversample)
  ├─ LexicalIndex: BM25 / pgFTS
  └─ Aggregator: SQL GROUP BY / count
  ↓
Merge + Score + Paginate → Response
```

**索引层改造：**
- 当前 `SearchChunks` 接口需要扩展为接受 `metadataFilter` 参数
- Postgres 路径：向量搜索可以在 `WHERE tenant=$1 AND bucket=$2 AND metadata @> $3` 之后再 ANN
- BM25 路径：pgFTS 天然支持 WHERE 子句约束
- SQLite 路径：暴力搜索仍然可用但效率低——建议 SQLite 用户使用元数据预过滤

**执行模式：**
- **Filter → Content**：先元数据过滤缩小范围，再在结果集上内容搜索（适合高选择性过滤）
- **Content → Filter**：先语义搜索 top-K，再在结果上过滤（适合低选择性过滤）
- **Parallel → RRF**：并行执行两端，RRF 融合（适合无法预先判断的通用场景）

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 跨租户查询的鉴权 | 调用方必须是 operator/管理角色。普通租户不能跨租户查询 |
| 超长查询（100+ 过滤条件） | 解析阶段限制 `max_clauses=64`，超限返回 400 |
| 无索引字段过滤（如 `metadata.X` 无 GIN 索引） | 扫描执行前检查可用索引，对无索引字段降级为顺序扫描 + warning |
| 深度分页（offset=100000） | 限制 max_offset=10000，推荐 `search_after` 游标分页 |
| 搜索超时 | 与 AI 路由组的 `aiTimeout` 保持一致，支持配置独立查询超时 |
| 过时向量索引 | 搜索结果中的 chunk 指向已删除对象——需要 filter 阶段排除 `deleted_at IS NOT NULL` |

---

## 方向五：异步写入缓冲与优雅降级写入路径

### 现状

当前所有写操作都是同步的：

```go
// internal/service/file_crud.go:Put
func (s *FileService) Put(...) (repository.Object, error) {
    // ... preflight checks ...
    info, err := s.store.Put(ctx, sk, reader, size, storage.PutOptions{...})  // 同步阻塞
    if err != nil {
        return repository.Object{}, fmt.Errorf("storage put: %w", err)
    }
    // ... metadata write ...
}
```

| 代码路径 | 阻塞点 | 失败后果 |
|---------|--------|---------|
| `local_write.go:Put` | `os.Create` → `io.Copy` → `file.Sync` | 磁盘满 → 写入失败 → 对象不存在 |
| `s3.go:Put` | `s3manager.Upload`（HTTP PUT 到 S3） | 网络故障 → 写入失败（可能部分上传） |
| `circuitbreaker.go:Put` | 断路器打开 → `ErrBackendUnavailable` | 立即拒绝，无缓冲 |

**核心问题：**

1. **无写缓冲**：后端不可达时写入 100% 失败，即使是瞬态故障
2. **大对象阻塞**：`PUT 1GB` 占用连接线程数秒到分钟
3. **无背压反馈**：后端吞吐饱和时，客户端接收不到带宽感知的建议重试延迟
4. **无请求排期**：无法设置 `?run_after=`（延迟写入）或优先级（紧急写入插队）

### 产品价值

| 价值 | 量化影响 |
|------|---------|
| **瞬态容错** | S3 后端 5 秒不可用时，写入缓冲到本地盘——用户无感知 |
| **削峰填谷** | 写入突发速率 2× 后端容量时，缓冲吸收峰值——后端持续满负载运行 |
| **大对象不阻塞** | 1GB 文件写入入队即响应 202——后台异步持久化 |
| **优先级调度** | 小文件/高优先级请求可以插在大文件请求之前 |
| **优雅降级** | 后端完全不可达时仍接受写入（提供写入保证：at-least-once 或 best-effort） |

### 架构权衡

**架构决策树：**

```
请求到达 → 是否需要异步？
  ├─ 否：(默认) 同步直写，如同当前，最低延迟
  └─ 是：(显式选择或自动降级)
       ├─ 写入本地缓冲：低延迟，需本地盘，副本数 1
       └─ 写入 Postgres：依赖 DB 写入吞吐瓶颈

缓冲实现选项：
1. 本地 WAL（SQLite）：已有 sqlite 依赖，零新增组件，重启不丢
2. 独立 queue 表：已有 jobs 表基础设施，复用 retry/backoff
3. 纯内存 channel：最低延迟，重启丢失
```

推荐**分层策略**：

| 层 | 实现 | 持久性 | 延迟 |
|----|------|--------|------|
| **L1** 内存写入缓冲区 | `chan writeRequest` + 按优先级排序 | 🚫 重启丢失 | <1μs |
| **L2** WAL 持久缓冲区 | SQLite 本地 `ingestion_queue` 表 | ✅ 重启保留 | <1ms |
| **L3** 后端持久化 | `store.Put` 到 S3/Local/OSS | ✅ 最终持久 | 取决于后端 |

写入路径流程：

```
Client → Accept (202) → L1 Memory Buffer → [优先级排序] → L2 WAL (SQLite) → [flush worker] → L3 Backend (S3) → Metadata Write → Event Publish
                                                                                         ↘ 失败 → RetryQueue → backoff → L3
```

**API 变化：**

```http
PUT /v1/files/important.doc
X-Write-Mode: sync                # 默认：同步写，等待持久化返回
# or
X-Write-Mode: async               # 异步写：返回 202 Accepted + Job ID
# or
X-Write-Mode: async-low-priority  # 低优先级异步写

# 响应（async 模式）：
HTTP/1.1 202 Accepted
Location: /v1/jobs/write-abc123
Content-Type: application/json
{"job_id": "write-abc123", "status": "pending", "estimated_latency_ms": 1500}
```

**缓冲容量管理：**

| 策略 | 实现 | 阈值 |
|------|------|------|
| 拒绝 | L2 WAL 行数/大小达到容量上限时返回 503 | `WRITE_BUF_MAX_ROWS=50000` / `WRITE_BUF_MAX_BYTES=1GB` |
| 降级 | 缓冲满时新写入自动降级为同步模式 | 超过容量 80% 触发 |
| 告警 | 缓冲深度持续高于阈值 → OTel 告警 | `write_buffer.depth > 1000 for 5min` |

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 写入缓冲后节点崩溃 | SQLite WAL 重启后扫描未完成的写入。若有重复写入风险，用 idempotency-key 去重 |
| 缓冲中对象在持久化前被读取 | 从缓冲中提供（L1 内存 + L2 WAL 联合搜索），保证 read-after-write 最终一致性 |
| 缓冲写入最终持久化失败 | 重试耗尽后触发 webhook 通知 + 对象标记为 `_aero_write_failed` 元数据 |
| 超大对象（>1GB）缓冲 | 缓冲仅存储元数据引用（`{tenant, bucket, key, size, sha256}`），不缓冲实际内容。内容直接写入后端临时区 |
| 缓冲顺序保证 | 同一 `(tenant, bucket, key)` 的写入按 FIFO 顺序持久化（FIFO per key，全局无序） |
| WAL 磁盘满 | 监控 SQLite WAL 所在磁盘，提前触发降级信号 |

---

## 优先级与建议执行顺序

| 排序 | 方向 | 前置依赖 | 建议投入 | 独立可交付 |
|------|------|---------|---------|-----------|
| **1** | 方向二：服务端变换管线 | 无（可从 thumbnail 模块增量扩展） | 4-6 周 | ✅ `/v1/files/*/transform` + WebP 转码 |
| **2** | 方向一：对象锁合规治理 | `objects` 表新增 `legal_holds` 表（迁移 v0025） | 6-8 周 | ✅ Legal Hold API + 审计日志 |
| **3** | 方向三：分布式配额平面 | Postgres 连接池（已有） | 8-10 周 | ✅ 分布式限流 + 层级配额 + 统一治理 API |
| **4** | 方向四：统一搜索查询语言 | 方向三无直接依赖，但需要 VectorIndex + LexicalIndex 就绪（均已实现） | 8-12 周 | ✅ `/v1/query` DSL + 跨模态搜索 |
| **5** | 方向五：异步写入缓冲 | `jobs` 表基础设施（已有） | 10-14 周 | ✅ `X-Write-Mode: async` + SQLite WAL |

**建议执行策略：**

1. **Phase 1（方向二）**：以 `GET /v1/files/*/transform?w=200&format=webp` 为 MVP，复现 thumbnail 的代码模式但推广为通用变换管线。3 周内可交付
2. **Phase 2（方向一）**：Legal Hold 是合同/合规硬需求。先推 `legal_holds` 表 + API，保留策略重叠逻辑简化版（max wins）
3. **Phase 3（方向三 + 方向五）**：分布式配额与异步缓冲共享底层数据结构和协调机制（Postgres advisory lock / Redis）。可并行开发，共享治理层
4. **Phase 4（方向四）**：统一查询是最复杂也是差异化最大的方向。需要前三个阶段积累的元数据治理 + 内容索引 + 限流能力作为基础设施

---

## 总结

以上五个方向覆盖了当前 aero-vault 在**合规治理、内容平台化、多副本运营、搜索体验、写入韧性**五个维度的关键缺口。它们与既有 92 轮分析无实质重叠，同时在代码库中有明确锚点，具备从当前架构渐进演进的可行性。

| 维度 | 当前状态 | 目标状态 |
|------|---------|---------|
| 合规 | 二进制 legal hold 标记 + retention TTL | Legal Hold 案件管理 + Compliance 模式 + 审计链条 |
| 内容平台 | 仅 JPEG/PNG 缩略图 | 通用变换管线 + 格式转换 + 预览生成 + 懒加载缓存 |
| 运营 | 单进程限流 + 独立配额 | 分布式分层配额 + 权重模型 + 突发银行 |
| 搜索 | 三条孤立路径 | 统一 DSL + 元数据+内容跨模态 + 聚合 + 跨租户 |
| 韧性 | 同步直写 + 失败即返回 | 异步缓冲 + 优雅降级 + 优先级调度 + 读-写一致性 |

