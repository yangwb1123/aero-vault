现在我已拥有完整的数据。以下是全面的去重验证结果。

---

## 去重验证报告：`expansion-v116-product-architect-frontiers.md`

**前提：** 此文件已存在于 `docs/requirements/` 路径下（`expansion-v116-product-architect-frontiers.md`，2026-07-11 18:17）。没有相应的 `.out.md` 评审文件，因此该文件尚未经过审查。以下是对 115 份既有分析文档进行的逐方向验证。

---

### 方向 1：Multi-Range HTTP 请求支持（RFC 7233 §4.1）

| 文件 | 匹配结果 | 判定 |
|------|---------|------|
| `expansion-v103-security-edge-foundations.md` | 命中 1 处，但内容是关于 **Content-Encoding: gzip 导致的单 Range 字节偏移损坏**。这是一个完全不同的错误——gzip 解压缩会破坏 Range 偏移计算，而不是多段 Range 支持。| ❌ **不同问题** |
| 其他 114 份文件 | 零次命中 `multipart/byteranges`、`ParseMultiRange`、`bytes=.*,.*-`（逗号分隔的多段 Range）| — |
| **结论** | **✅ 全新方向** — 115 份既有分析均未覆盖 multipart/byteranges 支持。v103 的 Range 分析解决的是不同问题（gzip 解压缩偏移损坏）。|

---

### 方向 2：Server-Timing 逐请求耗时剖断面

| 文件 | 匹配结果 | 判定 |
|------|---------|------|
| `expansion-v80-systemic-production-gaps.md` | 原始 grep 返回 1 个匹配，但详细检查后该匹配属于其他上下文中的通用“timing”一词。未出现 `Server-Timing` 或 `server_timing`。| ❌ **误报** |
| 其他 114 份文件 | 零次命中 `Server-Timing`、`耗时剖面`、`server_timing` | — |
| **结论** | **✅ 全新方向** — 未在 115 份既有分析中被覆盖。|

---

### 方向 3：数据完整性校验强制策略

| 文件 | 匹配结果 | 判定 |
|------|---------|------|
| `expansion-v114-s3-protocol-completeness-and-operational-gaps.md` | **核心覆盖（~120 行）** — 方向 2 "S3 Flexible Checksum API" 深入分析了 CRC32/CRC32C/SHA1/SHA256 算法支持、Storage 接口契约缺口、Repository 持久化缺失、读取路径校验和验证。| ⚠️ **显著重叠** |
| `expansion-v57-production-architecture-gaps.md` | 1 行提及 `"force_md5": true` 用于预签名 URL 场景 | ⚠️ 已被触及 |
| `expansion-v100-new-frontiers.md` | 在 API 治理背景下简要提及 MD5 验证 | ⚠️ 已被触及 |
| `expansion-v15-cross-cutting-gaps.md` | 表中 3 行提及 MD5（非独立方向） | ⚠️ 已被触及 |
| **结论** | **⚠️ 部分新颖** — 具体的 **`STORAGE_CHECKSUM_POLICY`（none | prefer | required）三档策略配置**和**通用 `checksumWrapReader`** 概念确实是新的。但底层的 S3 Flexible Checksum API 分析（v114）以及 MD5 的零散引用覆盖了绝大部分技术细节。**新颖增量约为总方向的 25-30%。** |

---

### 方向 4：桶清单定时生成管线

| 文件 | 匹配结果 | 判定 |
|------|---------|------|
| `expansion-v16-foundations.md` | **~200 行深入分析**，配有架构 ASCII 图、边界情况（>1 亿对象、文件写入中断、并发一致性、存储成本、加密桶）、DB schema 草案、S3 XML 配置格式、JobPool 调度细节以及实现估算（~500 行）。| ❌ **已有深度覆盖** |
| `expansion-v108-production-hardening-and-api-completeness.md` | `?inventory` 被识别为 S3 bucket 子资源缺口，并指出 `dispatchBucketSubresource` 中缺少对应 case。| ⚠️ 已被触及 |
| `expansion-v17-production-gaps.md`、`expansion-v27-operational-maturity-gaps.md`、`expansion-v32-architect-frontiers.md`、`expansion-v85-cross-cutting-platform-gaps.md` | 零散提及 Inventory | ⚠️ 已被触及 |
| **结论** | **❌ 已有覆盖** — v16 提供了非常详尽的架构分析。v116 添加了特定的代码锚点（`internal/reconcile/job.go`、`internal/snapshot/snapshot.go`）和边界情况（增量清单、分布式锁），但核心方向在 v16 中已被充分涵盖。**新颖增量约为 10-15%。** |

---

### 方向 5：事件订阅者背压与缓冲区溢出保护

| 文件 | 匹配结果 | 判定 |
|------|---------|------|
| `expansion-v102-genuine-code-level-gaps.md` | **深度覆盖** — 方向 2 "SSE 事件流的韧性架构" 包含完全相同的代码锚点（`bus.go:30` `defaultSubBuffer=64`、`broadcast` 的 `default` 分支、`events_dropped_total`），加上持久化游标和指数退避重连。| ❌ **已有深度覆盖** |
| `expansion-v121-replication-integrity-sse-resilience-cross-protocol-cli-event-timing.md` | 与 v102 内容几乎相同，同样覆盖背压和事件丢失。| ❌ **已有覆盖** |
| `expansion-v108-production-hardening-and-api-completeness.md` | 将 `events_dropped_total` 识别为运维指标 | ⚠️ 已被触及 |
| `expansion-v106-production-operations-blindspots.md`、`expansion-v113-*`、`expansion-v119-*` 等 | 多个文件在不同背景下提及事件丢失 | ⚠️ 已被触及 |
| **结论** | **❌ 已有覆盖** — v102 和 v121 都提供了对同一问题的深入分析。v116 的方向 5 将缓解措施结构化为三个阶段（可见性 → 背压 → 持久化队列），这是一个增量贡献，但核心缺口分析并非新颖。**新颖增量约为总计的 20%。** |

---

### 总结表

| # | 方向 | 覆盖情况 | 新颖性评估 | 最相关的既有分析 |
|---|------|---------|-----------|----------------|
| **1** | Multi-Range HTTP 请求支持 | **✅ 全新** | 100% 新颖 | v103 覆盖的是不同的 Range 问题（gzip 解压缩） |
| **2** | Server-Timing 剖断面 | **✅ 全新** | 100% 新颖 | 没有既有分析 |
| **3** | 数据完整性校验强制策略 | **⚠️ 部分新颖** | ~25% 增量 | v114 覆盖 Flexible Checksum API；策略框架是新增的 |
| **4** | 桶清单生成管线 | **❌ 已有覆盖** | ~10% 增量 | v16 提供完整架构分析 |
| **5** | 事件订阅者背压保护 | **❌ 已有覆盖** | ~20% 增量 | v102/v121 覆盖相同的漏洞 |

**整体而言：** 在 5 个方向中，有 **2 个是全新的**（方向 1 和方向 2），**1 个是部分新颖的**（方向 3：策略框架增加了新内容），**2 个在既有分析中已有显著覆盖**（方向 4 在 v16 中，方向 5 在 v102/v121 中）。

此外，该文件与其后续版本 `expansion-v116-high-value-expansion-directions.md`（涵盖基础设施生态集成、数据迁移、自定义域名、性能基准测试和分析生态——一组完全不重叠的方向）共存。
