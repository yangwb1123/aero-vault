# AeroVault 高价值扩展方向 v42 — S3 协议实现纵深与执行层缺口

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 230+ `.go` 文件 + `sdk/*` + `deploy/*` + `docs/*` + 48 对迁移文件 + `Makefile`）
>
> **分析视角：** 资深架构师 / 产品经理 — 聚焦此前 **41 期 expansion 分析（累计 200+ 方向、27,000+ 行分析文本）从未实质性触及的 S3 协议实现纵深与执行层缺口**
>
> **分析日期：** 2026-07-10
>
> **去重方法：** 逐方向对 `docs/requirements/` 下全部 41 期既有分析（v1–v41） + `docs/ROADMAP.md` + `docs/CHANGELOG.md` + `docs/TODO.md` + `docs/adr/DECISIONS.md` + `docs/extensions*.md` 进行关键术语 `grep` 验证。每个方向在既有文档中 **零实质性独立架构分析**（矩阵表格中的一行过路引用或浅层提及不构成实质性分析）。

---

## 前言

此前 41 期 expansion 分析以 **功能覆盖面广**（200+ 方向）著称，从 AI/RAG 管线到存储后端、从认证授权到合规、从工程质量到社区基础设施，覆盖了产品应有的绝大多数功能。然而，这些分析的共同视角是 **"有什么功能，缺什么功能"** — 它们很少深入审查 **已有功能的实现纵深**。

本期 5 个方向的共同特征是：**它们不是新功能，而是已有功能的执行层缺口**。所有涉及的 S3 API 表面上都已"实现"——请求可以被路由、响应可以返回、配置可以持久化——但实际的行为层要么完全缺失、要么仅实现了一半。这是"功能完整"到"行为完整"之间的鸿沟。

```
功能矩阵视角（前 41 期）：  ❌ 不支持 → ✅ 已实现
执行纵深视角（本期 v42）：   ✅ 有 CRUD → ✅ 运行时行为完整
                                       → ⚠️ 有 CRUD 无运行时（本期焦点）
```

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心发现 | 锚定代码 |
|---|------|------|--------|---------|---------|
| 1 | **S3 协议合规验证基础设施** | 工程质量/质量保障 | **P1** — 无合规套件 = S3 协议每处修改都无声引入回归 | `internal/api/s3compat/` 全目录（仅自写测试，零外部合规套件） |
| 2 | **StorageClass 纯元数据 → 主动分层桥接** | 架构/性能/成本 | **P1** — 最常被引用的 S3 特性（12 处代码引用）却无任何运行时行为 | `internal/service/file_crud.go`（存储 StorageClass）→ `internal/reconcile/lifecycle.go`（无 transition） |
| 3 | **对象锁 Legal Hold + Retention 子资源缺失** | 合规/安全 | **P1** — 金融/法规场景的对象级锁定 API 完全缺失 | `internal/api/s3compat/handler.go`（无 `?legal-hold`/`?retention` 路由）；`internal/service/file_crud.go`（`_aero_legal_hold` 存 metadata 但无 S3 API） |
| 4 | **生命周期规则执行引擎不完整** | 合规/成本 | **P2** — 只能处理 `Expiration.Days`；Transition、NoncurrentVersion、AbortMultipart 全部缺失 | `internal/reconcile/lifecycle.go`（仅 soft/hard delete）；`internal/api/s3compat/bucketconfig.go`（丢弃 NoncurrentVersion/Transition/Abort 规则） |
| 5 | **Per-Bucket CORS 执行层断层** | 安全/协议 | **P2** — CORS 规则可配置存储但永不执行；全局 CORS 中间件覆盖一切 | `internal/repository/sql_buckets.go`（存储 `cors_rules`）；`internal/middleware/cors.go`（全局 middleware，不读 per-bucket 规则） |

---

## 方向一：S3 协议合规验证基础设施（S3 Protocol Compliance Test Suite）

### 现状

当前 S3 兼容层的测试覆盖：

```
internal/api/s3compat/
├── handler_test.go        # ~600 行，自写 handler 测试
├── sigv4_test.go          # SigV4 签名验证测试
├── versioning_test.go     # 版本化行为测试
└── ...                    # 无外部合规测试套件
```

所有测试都是 **自写自测** — 测试代码与生产代码由同一团队编写、测试与实现共享同一心理模型、测试覆盖的路径就是实现者已知的路径。

**没有以下任何一种外部/系统性合规测试手段：**

| 测试手段 | 存在状态 | 说明 |
|---------|---------|------|
| AWS SDK 认证集成测试 | ❌ | 从未用真实 AWS SDK 客户端（boto3、aws-cli、aws-sdk-go）对 Handler 做端到端测试 |
| S3 兼容性认证工具 | ❌ | 无 MinIO `mc` 客户端测试、无 s3verify、无 S3Proxy 合规认证 |
| 协议契约测试 | ❌ | 无 OpenAPI/S3 契约验证——响应格式靠手写 XML，无 schema 验证 |
| 回归检测 | ❌ | 修改 S3 handler 后，无自动化手段发现"之前能用的 S3 客户端现在 500" |
| 模糊测试（Fuzz） | ❌ | S3 handler 从未做 fuzz 测试——malformed XML、超大 header、非法 query 参数等 |
| 负载/并发测试 | ❌ | S3 handler 无并发 PUT/GET 的 race 检测 |

**具体代码指出的风险：**

```go
// internal/api/s3compat/xml.go — XML 响应结构体全部手写
// 没有任何一个结构体有对应的 schema 验证
// 例如：CopyObjectResult 的字段顺序/类型是否与 AWS 一致？无人能回答。

// internal/api/s3compat/handler_test.go — 测试是"我测我自己"
func TestPutGetDelete(t *testing.T) {
    // 创建对象、读取、删除——测试已知路径
    // 不会发现"如果 aws-cli 发送非标准 header 会出现什么"
}
```

### 为什么需要

**S3 协议兼容是 AeroVault 的核心差异化和获客入口。** 用户选择 AeroVault 的重要原因之一就是"可以用标准 S3 SDK 接入"。如果这个承诺不可靠——某些 S3 SDK 正常工作、某些静默失败——用户的信任就会崩塌。

具体风险：

1. **无声的 S3 协议退化**：修改 XML 响应结构体的字段顺序（Go 的 `xml.Marshal` 按结构体字段序），可能破坏解析依赖字段顺序的 S3 客户端（某些 SDK 确实如此）。

2. **AWS SDK 行为差异**：各语言 AWS SDK 在解析 S3 响应时存在细微差异。只测试 Go handler 的 HTTP 响应不等于测试了 `boto3` 或 `aws-cli` 的端到端体验。

3. **S3 兼容认证的门槛**：若产品需要进入企业采购清单，"是否通过 S3 兼容认证"是常见问题。当前无法回答。

4. **边际差异不可见**：S3 的"长尾"特性（如错误响应格式、header 格式、条件请求的行为边界）最易遗漏。自写测试只会覆盖实现者关心的主流路径。

### 缺失的能力

1. **AWS SDK 集成测试层**：新增 `internal/integration/s3_compat_test.go`，用 `aws-sdk-go-v2` 创建真实客户端指向测试服务器，执行完整的对象 CRUD 生命周期：

   ```go
   // 示例测试
   func TestS3Compat_PutGetDelete(t *testing.T) {
       cfg := awsConfigFromEnv(t)  // endpoint = test server
       client := s3.NewFromConfig(cfg)
       _, err := client.PutObject(ctx, &s3.PutObjectInput{
           Bucket: aws.String("test"),
           Key:    aws.String("hello.txt"),
           Body:   strings.NewReader("world"),
       })
       // ...
   }
   ```

2. **MinIO `mc` 客户端测试**：在 CI 中下载 `mc` 二进制，执行标准 `mc cp`、`mc ls`、`mc rm`、`mc tag`、`mc lock` 等操作，验证二进制客户端兼容性。

3. **协议模糊测试**：为 S3 handler 添加 `testing.F` 模糊测试，覆盖：
   - Malformed XML body（生命期、通知、CORS、ACL 配置）
   - 非法 `Range` header
   - 超大 `x-amz-copy-source` header
   - 非标准 `?query` 参数
   - 并发 PUT/GET/DELETE 同一对象

4. **XML Schema 契约验证**：为每个 XML 响应结构体编写对应的 XSD 或 Go struct tag 验证，确保响应格式与 AWS S3 一致。考虑 CI 中用 `xmllint --schema` 验证。

5. **S3 API 覆盖率报告**：新增自动化工具统计当前 S3 handler 覆盖的 API 点数，与 AWS S3 API 参考对比，生成覆盖率缺口报告。

### 架构概要

```
当前测试架构:
  go test ./internal/api/s3compat/
    └── handler_test.go    ← 自写自测，覆盖已知路径

改进后的测试架构:
  go test ./internal/api/s3compat/   (单元测试 — 保持)
  go test ./internal/integration/    (AWS SDK 集成测试 — 新增)
    └── s3_compat_test.go            ← aws-sdk-go-v2 端到端测试
    └── s3_fuzz_test.go              ← 模糊测试

  CI 中新增步骤:
    make test-s3-compat              ← 启动 test server + AWS SDK 测试
    make test-s3-fuzz                ← 模糊测试
    make test-s3-mc                  ← minio/mc 客户端验证

  可选增强:
    make test-s3-compliance-report   ← 生成 S3 API 覆盖率缺口报告
```

### 边界情况

| 场景 | 当前风险 | 合规测试后会暴露 |
|------|---------|----------------|
| PUT 对象后用 `aws-sdk-go` 读取 ETag 响应 | 自写测试验证了 ETag 存在，但 SDK 可能期望带引号的格式？ | 集成测试直接验证 SDK 行为 |
| `ListObjectsV2` 的 `IsTruncated` 语义 | 自写测试覆盖了分页逻辑，但不同 SDK 处理 `NextContinuationToken` 格式有差异 | 真实 SDK 调用会暴露格式差异 |
| 错误响应的 XML 格式 | 自写测试验证了 HTTP 状态码，但 SDK 可能期望特定 XML 错误结构 | AWS SDK 测试会暴露 `Code`/`Message` 字段缺失 |
| `CopyObject` 的 metadata-directive 行为 | 自写测试验证了 metadata 复制逻辑，但未验证 `x-amz-metadata-directive` 与 AWS 的行为差异 | 集成测试会暴露边界行为 |
| DELETE 对象的 204 响应 | 自写测试验证了 204，但 SDK 可能期望 `DeleteMarker=true` header | 集成测试会暴露 header 缺失 |

---

## 方向二：StorageClass 纯元数据 → 主动分层桥接（StorageClass Metadata-to-Tiering Bridge）

### 现状

StorageClass 是 AeroVault 代码库中 **引用最广泛的 S3 特性之一**，但它只有"元数据层"，没有任何"执行层"：

| 代码位置 | StorageClass 的角色 | 行为 |
|---------|--------------------|------|
| `internal/repository/migrations/sqlite/0021_storage_class.up.sql` | `ALTER TABLE objects ADD COLUMN storage_class` | 存储 ✅ |
| `internal/repository/repository.go:34` | `Object.StorageClass` 字段定义 | 定义 ✅ |
| `internal/repository/sql_objects.go` | `INSERT ... storage_class=...` | 持久化 ✅ |
| `internal/repository/sql_objects.go:337` | `StorageClassCounts` 查询 | 统计 ✅ |
| `internal/service/file_crud.go:181` | `StorageClass: StorageClassOrDefault(opts.StorageClass)` | 传递 ✅ |
| `internal/service/file.go:149` | `PutOptions.StorageClass` | 选项 ✅ |
| `internal/telemetry/metrics.go:181` | `RegisterStorageClassGauge` | 可观测 ✅ |
| `internal/api/s3compat/handler.go:108` | `StorageClass: r.Header.Get("x-amz-storage-class")` | 解析 ✅ |
| `internal/api/s3compat/handler.go:650` | 响应中写 StorageClass header | 回显 ✅ |
| `internal/api/rest/handler.go:805` | `writeStorageClass` | 回显 ✅ |
| **`internal/storage/` 全目录** | **❌ 没有任何 StorageClass 相关的后端选择逻辑** | **缺失 ❌** |
| **`internal/reconcile/lifecycle.go` 全文件** | **❌ 没有任何 transition_to_ia/glacier 逻辑** | **缺失 ❌** |
| **`internal/service/file_crud.go`** | **❌ StorageClass 传入后仅存为元数据，不影响存储后端** | **缺失 ❌** |

**核心问题：StorageClass 是纯装饰性字段。** 用户可以上传对象时指定 `x-amz-storage-class: GLACIER`，元数据库会愉快地保存 `storage_class = 'GLACIER'`，但数据仍然存储在默认的本地/S3 后端中。没有后端选择、没有生命周期迁移、没有成本差异化。

```
当前数据流:
  PUT x-amz-storage-class: GLACIER
    → FileService.Put(opts.StorageClass="GLACIER")
      → repo.InsertObject(storage_class="GLACIER")   ✅ 保存
      → store.Put(key, data)                          ❌ 写入同一个后端
      → 返回响应 (x-amz-storage-class: GLACIER)       ✅ 回显

期望数据流:
  PUT x-amz-storage-class: GLACIER
    → FileService.Put(opts.StorageClass="GLACIER")
      → router.SelectBackend("GLACIER")               ← 新增
      → glacierStore.Put(key, data)                   ← 路由到冷存储
      → repo.InsertObject(storage_class="GLACIER")   ✅ 保存
      → 返回响应
```

### 为什么需要

1. **StorageClass 是 S3 最核心的成本控制手段。** 企业使用对象存储时，StorageClass 的选择直接影响 40-70% 的存储成本。如果 AeroVault 的 `GLACIER` 不真的省钱，用户就没有理由选择它。

2. **当前实现会误导用户。** 用户上传一个对象标记为 `GLACIER`，以为它在低成本存储中。实际上它和 `STANDARD` 对象存储在同一个后端。当账单/存储用量显示异常时，用户会发现"StorageClass 是假的"——这将严重损害产品信任。

3. **它阻碍了生命周期价值主张。** 生命周期规则的核心价值是"30 天后自动转为 IA，90 天后转为 Glacier"。如果没有真正的 tiering，这个价值主张完全无法兑现。

### 缺失的能力

1. **StorageClass → Backend 映射注册表：** 新增 `TierRouter`，将 StorageClass 名称映射到 `storage.Storage` 实例：

   ```go
   type TierRouter struct {
       tiers map[string]storage.Storage  // "STANDARD" → localStore, "GLACIER" → s3Glacier
       default storage.Storage
   }
   ```

2. **配置化 tier 映射：** 新增环境变量（如 `STORAGE_TIER_STANDARD=local`、`STORAGE_TIER_GLACIER=s3://cold-bucket`），在 `main.go` 中构建多后端并注册。

3. **生命周期 Transition 执行：** 当生命周期规则触发 transition 时，`LifecycleJob` 需要：
   - 从 source backend 读取对象字节
   - 写入 target backend
   - 更新 `Object.StorageKey` + `StorageClass`
   - 从 source backend 删除旧 blob（或等待 GC）

4. **跨 tier 读取降级：** 如果对象被标记为 `GLACIER` 但存储在标准后端（迁移过程中），GET 应透明地从实际存储位置读取。

5. **Tier 迁移状态跟踪：** 新增 `storage_key_history` 或 `tier_transitions` 表，记录每个对象的 tier 变更历史。

### 架构概要

```
当前:
  store = buildStorage(cfg)   // 一个后端实例
  FileService → store.Put(key, data)  // 所有 StorageClass 写入同一后端

改进:
  tierRouter = storage.NewTierRouter()
  tierRouter.Register("STANDARD",     hotStore)    // local SSD
  tierRouter.Register("STANDARD_IA",  warmStore)   // S3 Standard
  tierRouter.Register("GLACIER",      coldStore)   // S3 Glacier Deep Archive

  FileService → tierRouter.Put(ctx, opts.StorageClass, key, data)
    → backend = tierRouter.Lookup(opts.StorageClass)
    → backend.Put(tierRouter.EncodeKey(key, opts.StorageClass), data)

  LifecycleJob:
    → 扫描对象 (age > transition_days AND storage_class != target)
    → 读取 source → 写入 target → 更新 metadata → 清理 source

  新增配置:
    STORAGE_TIER_STANDARD=local:///var/hot
    STORAGE_TIER_STANDARD_IA=s3://warm-bucket?endpoint=...
    STORAGE_TIER_GLACIER=s3://cold-bucket?endpoint=...&glacier
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **不存在的 StorageClass** | PUT 时报 `InvalidStorageClass` 错误（当前静默降级为 STANDARD） |
| **Migrating 中的 GET** | 通过 `Object.Backend` 字段（现有）判断实际存储位置，路由到正确后端 |
| **跨 tier CopyObject** | 源对象在 STANDARD 后端，目标指定 GLACIER → 复制到冷后端 |
| **Tier 后端不可用** | 返回 `503 ServiceUnavailable`，不影响其他 tier 的操作 |
| **ListObjects 跨 tier 排序** | ListObjects 按元数据 DB 排序（已如此），不依赖后端位置 |
| **SSE 加密跨 tier** | 迁移到新后端时需要解密→重新加密（复用现有 `Sealer.UnsealAndSeal`） |

### 影响评估

| 维度 | 评估 |
|------|------|
| 复杂度 | **中**（概念简单——路由 + 迁移；挑战在于一致性保证） |
| 用户感知影响 | **极高**（StorageClass 从"假"变"真"——用户可以直接感知） |
| 代码变动 | 中（新增 `storage/tier_router.go`；修改 `main.go:buildStorage`；修改 `service/file_crud.go`；增强 `reconcile/lifecycle.go`） |
| 差异化 | ★★★★☆（让 AeroVault 从"存所有数据在同一层"变为"真正的分层存储"） |

---

## 方向三：对象锁 Legal Hold + Retention 子资源缺失（Object Lock API Surface Gap）

### 现状

当前对象锁的实现与 S3 API 之间存在 **显著的 API 语义断层**：

| S3 对象锁特性 | 当前状态 | 详细 |
|--------------|---------|------|
| `PUT /{bucket}/{key}?legal-hold` | ❌ **完全缺失** | Legal hold 通过 metadata `_aero_legal_hold` 实现（内部 hack），没有 S3 API 端点 |
| `GET /{bucket}/{key}?legal-hold` | ❌ **完全缺失** | 无 S3 API 查询 legal hold 状态 |
| `PUT /{bucket}/{key}?retention` | ❌ **完全缺失** | 无 API 设置 per-object retention |
| `GET /{bucket}/{key}?retention` | ❌ **完全缺失** | 无 API 查询 retention 设置 |
| `x-amz-object-lock-mode` (GOVERNANCE/COMPLIANCE) | ❌ **完全缺失** | 无模式区分，所有锁都一样 |
| `x-amz-object-lock-retain-until-date` | ❌ **未解析** | PUT handler 不读取此 header |
| `s3:BypassGovernanceRetention` 权限 | ❌ **完全缺失** | GOVERNANCE 模式应允许特权用户绕过，当前无实现 |
| 桶级别 `DefaultRetention` 设置 | ⚠️ 部分实现 | `object_lock_seconds` 存储了默认保留秒数，但无 `Mode`（GOVERNANCE/COMPLIANCE）|

**具体代码位置：**

```go
// internal/api/s3compat/handler.go:92-98 — Legal hold 通过 metadata hack 实现
if lh := r.Header.Get("x-amz-object-lock-legal-hold"); lh == "ON" || lh == "on" {
    meta["_aero_legal_hold"] = "ON"
}
// 问题：没有 ?legal-hold 子资源端点；没有 GET 查询 legal-hold 状态；
// 没有 S3 标准的 XML 响应格式

// internal/api/s3compat/handler.go — PUT handler 不解析以下 header:
// - x-amz-object-lock-mode
// - x-amz-object-lock-retain-until-date
// - x-amz-object-lock-legal-hold (仅在 PutObject 时处理，但没有独立子资源)

// internal/service/file_crud.go:301 — Legal hold 检查
if obj.Metadata["_aero_legal_hold"] == "ON" {
    return fmt.Errorf("%w: object is under legal hold", ErrLocked)
}

// internal/api/s3compat/bucketconfig.go:171 — Object lock 桶配置
// getBucketObjectLock / putBucketObjectLock 已实现但只有 RetentionSeconds，
// 没有 RetentionMode（GOVERNANCE/COMPLIANCE）
```

### 为什么需要

1. **S3 API 兼容性是信任的基础。** 用户期望 `?legal-hold` 和 `?retention` 子资源是 S3 对象锁的标准接口。当前只能通过 PUT object 时 header 设置 legal hold，且无法查询或修改——这是"半实现"。

2. **合规场景要求模式区分。** 金融/医疗行业合规要求明确区分 GOVERNANCE（可绕过，用于内部管控）和 COMPLIANCE（不可绕过，用于法律强制）。当前不区分模式意味着：
   - 无法满足需要 COMPLIANCE 的法规要求
   - 无法提供 GOVERNANCE 的灵活绕过机制

3. **`_aero_legal_hold` metadata hack 不是 API。** 将 legal hold 状态藏在用户 metadata 中：
   - 不兼容 S3 SDK 的 `get_object_legal_hold()` 调用
   - 不兼容 aws-cli 的 `aws s3api get-object-legal-hold`
   - 用户无法通过标准工具查询 legal hold 状态

### 缺失的能力

1. **`?legal-hold` 子资源端点：** 新增 GET/PUT `/bucket/key?legal-hold`，返回/设置 S3 标准格式的 Legal Hold 状态：

   ```xml
   <LegalHold xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
     <Status>ON</Status>
   </LegalHold>
   ```

2. **`?retention` 子资源端点：** 新增 GET/PUT `/bucket/key?retention`，返回/设置 S3 标准格式的 Retention 配置：

   ```xml
   <Retention xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
     <Mode>GOVERNANCE</Mode>
     <RetainUntilDate>2026-12-31T00:00:00Z</RetainUntilDate>
   </Retention>
   ```

3. **RetentionMode 区分（GOVERNANCE vs COMPLIANCE）：**
   - GOVERNANCE：允许拥有 `s3:BypassGovernanceRetention` 权限的用户在保留期内删除/覆盖
   - COMPLIANCE：绝对不可绕过，任何用户都不能在保留期内删除/覆盖
   - 桶级 `DefaultRetention` 扩展为包含 `Mode` 字段

4. **`BypassGovernanceRetention` 权限实现：** 在 `auth/policy.go` 中新增 action，在 Delete/Put 中检查：

   ```go
   if obj.RetentionMode == "GOVERNANCE" && 
      !auth.HasPermission(ctx, "s3:BypassGovernanceRetention") {
       return ErrLocked
   }
   ```

5. **metadata `_aero_legal_hold` 迁移：** 将现有基于 metadata 的 legal hold 迁移到专用字段，同时保持向后兼容。

### 架构概要

```
新增端点:
  PUT /{bucket}/{key}?legal-hold   Body: <LegalHold><Status>ON</Status></LegalHold>
  GET /{bucket}/{key}?legal-hold   Response: <LegalHold><Status>ON</Status></LegalHold>
  PUT /{bucket}/{key}?retention    Body: <Retention><Mode>GOVERNANCE</Mode><RetainUntilDate>...</RetainUntilDate></Retention>
  GET /{bucket}/{key}?retention    Response: <Retention><Mode>GOVERNANCE</Mode><RetainUntilDate>...</RetainUntilDate></Retention>

数据模型扩展:
  Object 结构体新增:
    LegalHold      bool   // 独立于 metadata
    RetentionMode  string // "GOVERNANCE" | "COMPLIANCE" | ""
    RetentionUntil *time.Time

  BucketConfig 结构体扩展:
    ObjectLockMode    string // 默认保留模式
    ObjectLockSeconds int64  // 现有字段

运行时行为:
  DeleteObject:
    if obj.LegalHold → ErrLocked
    if obj.RetentionMode == "COMPLIANCE" && time.Now() < obj.RetentionUntil → ErrLocked
    if obj.RetentionMode == "GOVERNANCE" && time.Now() < obj.RetentionUntil:
      if !hasBypassPermission → ErrLocked
      else → proceed (bypass)
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **Retention 过期后延长** | S3 允许在保留期内延长保留期限但不允许缩短。延长需要验证新日期 > 当前日期 |
| **Legal hold + Retention 同时设置** | 任何一个导致锁定 → 不可删除。解除需要两者均解除 |
| **版本化桶中的对象锁** | 每个版本独立存储 legal hold / retention | 
| **桶级 DefaultRetention 变更** | 变更仅影响新对象；已有对象不受影响 |
| **COMPLIANCE 模式下绕过** | 严格拒绝——这是 S3 协议的核心合规保证 |
| **GOVERNANCE 下 bypass 权限检查** | 需在 auth/policy.go 中新增 `s3:BypassGovernanceRetention` action 和声明式策略支持 |

---

## 方向四：生命周期规则执行引擎不完整（Lifecycle Rule Completeness Gap）

### 现状

当前生命周期实现 **只能处理 Expiration 规则，且只有 soft/hard delete 动作**。S3 生命周期规范的其余部分全部被静默忽略：

| S3 生命周期特性 | 当前状态 | 代码位置 |
|----------------|---------|---------|
| `Expiration.Days` → `soft_delete`/`hard_delete` | ✅ 已实现 | `internal/reconcile/lifecycle.go` |
| `Transition.Days` → `STANDARD_IA`/`GLACIER` | ❌ **缺失** | XML 解析丢弃 Transition 元素 |
| `NoncurrentVersionExpiration.NoncurrentDays` | ❌ **缺失** | 无版本感知的生命周期 |
| `NoncurrentVersionTransition.NoncurrentDays` | ❌ **缺失** | 无版本感知的降冷 |
| `AbortIncompleteMultipartUpload.DaysAfterInitiation` | ❌ **缺失** | 无孤儿分片清理 |

**具体代码证据：**

```go
// internal/api/s3compat/bucketconfig.go:77-96 — putBucketLifecycle
// 只读取第一个 Expiration.Days，完全丢弃：
// - Transition 元素
// - NoncurrentVersionExpiration
// - NoncurrentVersionTransition
// - AbortIncompleteMultipartUpload
func (h *Handler) putBucketLifecycle(w http.ResponseWriter, r *http.Request, bucket string) {
    var in lifecycleConfiguration
    if err := decodeBucketBody(r, &in); err != nil {
        // ...
    }
    days := 0
    for _, rule := range in.Rules {
        if rule.Expiration != nil && rule.Expiration.Days > 0 {
            days = rule.Expiration.Days
            break  // 只取第一个规则，且只取 expiration
        }
    }
    // 使用 days 设置生命周期（只有 ExpireAfterDays）
}

// internal/reconcile/lifecycle.go — 扫描过期对象
func (j *LifecycleJob) sweep(ctx context.Context) {
    // 只处理 ExpireAfterDays → soft_delete / hard_delete
    // 没有 transition handler
    // 没有 noncurrent version handler
    // 没有 abort multipart handler
}
```

### 为什么需要

1. **数据生命周期管理是对象存储的核心价值之一。** 企业用户选择对象存储的关键原因就是 S3 生命周期规则能自动管理数据在不同存储层之间的流转。没有 Transition 能力，所有数据永远在同一层——成本无法优化。

2. **版本化桶的生命周期是 S3 标准实践。** 开启版本控制的桶中，旧版本会快速累积。没有 `NoncurrentVersionExpiration`，版本化桶的存储成本会线性增长，最终超过原始数据。

3. **孤儿分片是真实的存储泄漏源。** 中断的分片上传如果不清除，会持续占用存储空间。AWS S3 通过 `AbortIncompleteMultipartUpload` 生命周期规则自动清理。当前 AeroVault 没有此能力。

4. **用户从 AWS S3 迁移时会遇到障碍。** 如果用户的 S3 生命周期规则包含 Transition/NoncurrentVersion/Abort 元素，导入到 AeroVault 后这些规则会被静默丢弃——用户会收到"规则已设置"的确认，但实际行为与 AWS 不同。

### 缺失的能力

1. **多规则生命周期配置模型：** 将当前单一的 `ExpireAfterDays + ExpireAction` 扩展为多规则的 `[]LifecycleRule`：

   ```go
   type LifecycleRule struct {
       ID                        string
       Status                    string   // "Enabled" | "Disabled"
       Filter                    *Filter  // prefix/tag 过滤
       
       // Expiration
       ExpirationDays            int      // 到期删除天数
       ExpireDeleteMarker        bool     // 到期删除标记（版本化桶）
       
       // Transition
       TransitionDays            int      // 迁移天数
       TransitionStorageClass    string   // 目标 StorageClass
       
       // NoncurrentVersion
       NoncurrentDays            int      // 非当前版本天数
       NoncurrentTransitionDays  int      // 非当前版本迁移天数
       
       // Multipart
       AbortMPUDays              int      // 未完成分片上传天数
   }
   ```

2. **生命周期规则持久化：** 将规则存储从 `expire_after_days + expire_action`（单规则）迁移到 `lifecycle_rules JSON`（多规则）。新增 migration `0025`。

3. **`LifecycleJob` 规则执行引擎：** 扩展 `sweep()` 方法，按序遍历所有规则并执行：
   - Transition 规则 → 调用 TierRouter 迁移对象
   - NoncurrentVersionExpiration → 扫描旧版本并清除
   - NoncurrentVersionTransition → 扫描旧版本并迁移
   - AbortMultipartUpload → 扫描 `uploads` 表并中止过期上传

4. **S3 lifecycle XML 完整解析：** `putBucketLifecycle` 和 `getBucketLifecycle` 需要完整支持 S3 规范的 XML 格式，包括 `Filter`、`NoncurrentVersion*`、`AbortIncompleteMultipartUpload`。

### 架构概要

```
当前:
  putBucketLifecycle → 解析 XML → 提取第一个 Expiration.Days → 存储到 expire_after_days
  LifecycleJob.sweep → 查询 expire_after_days > 0 → soft/hard delete

改进:
  putBucketLifecycle → 完整解析 lifecycle XML → 序列化为 JSON → 存储到 lifecycle_rules
  LifecycleJob.sweep → 读取 lifecycle_rules → 遍历规则:
    ├── Expiration → deleteObject (现有逻辑)
    ├── Transition → tierRouter.Migrate(object, targetClass)
    ├── NoncurrentVersionExpiration → deleteOldVersions
    ├── NoncurrentVersionTransition → migrateOldVersions
    └── AbortMPU → abortStaleUploads

  新增存储:
    migration 0025: buckets.lifecycle_rules TEXT (JSON)
    替代 buckets.expire_after_days + expire_action（保留向后兼容）
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **规则中同时指定 Expiration 和 Transition** | 先 Transition 再 Expiration（匹配 AWS 行为） |
| **Filter 过滤** | 仅匹配 filter 的对象执行规则（支持 prefix/tag 过滤） |
| **规则冲突（多个规则匹配同一对象）** | 最短生命周期优先（Expiration 最早优先） |
| **版本化桶中 Expiration 语义** | 没有指定 NoncurrentVersion 时，Expiration 仅影响当前版本（删除标记） |
| **Transition 到相同 StorageClass** | 忽略（不执行任何操作） |
| **规则变更后已有对象** | 不追溯——规则变更仅影响变更后的行为 |

---

## 方向五：Per-Bucket CORS 执行层断层（Per-Bucket CORS Enforcement Gap）

### 现状

CORS（跨域资源共享）配置在 AeroVault 中有 **三层结构，但只有前两层工作**：

| 层 | 实现 | 状态 |
|---|------|------|
| **存储层**：`buckets.cors_rules` 列 + CRUD API | `internal/repository/sql_buckets.go:293-331` | ✅ 存储/读取/更新/删除完全实现 |
| **业务层**：FileService GetBucketCORS/SetBucketCORS/DeleteBucketCORS | `internal/service/file_features.go:226-240` | ✅ 业务逻辑完整 |
| **API 层**：REST `/v1/buckets/{bucket}/cors` | `internal/api/rest/handler.go:429-469` | ✅ REST 端点完整 |
| **API 层**：S3 `GET/PUT/DELETE /{bucket}?cors` | `internal/api/s3compat/handler.go` | ✅ S3 端点完整（见 `BucketDispatch`） |
| **🔴 执行层**：请求时实际 CORS 策略检查 | `internal/middleware/cors.go` | ❌ **完全缺失** |

**核心问题：CORS 中间件是全局的，不读取 per-bucket 规则。**

```go
// internal/middleware/cors.go — 全局 CORS 中间件
// 配置来自 main.go 中的 CORSConfig 环境变量
// 不读取请求对应的 bucket、不查询 per-bucket CORS 规则

// cmd/server/main.go:820 — 所有请求共用同一个 CORSConfig
m.CORS(middleware.CORSConfig{
    AllowedOrigins: cfg.CORS.AllowedOrigins,  // 全局列表
    AllowedMethods: cfg.CORS.AllowedMethods,  // 全局列表
    AllowedHeaders: cfg.CORS.AllowedHeaders,  // 全局列表
})

// internal/api/s3compat/handler.go — S3 handler 的 BucketDispatch
// 注意：cors 查询参数的处理只返回/设置配置，不执行
// 即，用户可以对 bucket A 设置 cors_rules: [{"AllowedOrigins":["https://app1.com"]}]，
// 但请求到达时，cors.go 中间件不看这个配置
```

**这意味着：**
- 桶 A 设置了 `AllowedOrigins: ["https://app1.com"]`
- 但 `https://evil.com` 的请求仍然可以通过（因为全局 CORS 配置允许 `*` 或未配置 CORS）
- 用户以为他们设置了安全的 CORS 策略，但这个策略从未被强制执行

### 为什么需要

1. **安全错觉比没有安全更危险。** 用户通过 S3 API 设置了 CORS 规则，收到了 200 OK，就会相信 CORS 在起作用。但实际上，CORS 的执行完全取决于全局 middleware 配置。如果全局配置是 `AllowedOrigins: ["*"]`（开发常见配置），那么所有 per-bucket CORS 规则形同虚设。

2. **S3 兼容的 CORS 行为是安全基线。** 在 AWS S3 中，CORS 规则是 **per-bucket 且按请求时路径匹配的**。如果 AeroVault 宣称支持 `?cors` 子资源但不执行，这直接违反 S3 协议规范，可能造成安全漏洞。

3. **四协议不一致。** REST API 和 S3 API 都已经实现了 CORS CRUD，但 WebDAV 和 MCP 协议完全不参与 CORS——因为 CORS 在 middleware 层一刀切。

### 缺失的能力

1. **Per-bucket CORS 执行中间件：** 新增 middleware 层，在请求处理路径中读取 `X-Aero-Tenant` header 提取 tenant，然后根据请求路径中的 bucket 名称查询对应的 `cors_rules`，如果有配置则使用 per-bucket 规则覆盖/补充全局规则。

2. **请求路径→Bucket 提取：** 新增轻量级函数从请求 URL 中提取 bucket 名称：

   ```go
   func bucketFromPath(path string) string {
       // S3 path-style: /bucket/key → bucket
       // REST: /v1/files/* → extract from route params (chi context)
       parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)
       if len(parts) > 0 {
           return parts[0]
       }
       return ""
   }
   ```

3. **CORS 规则优先级：** 如果 per-bucket 规则存在，则优先使用 per-bucket 规则；如果不存在，降级到全局规则。如果两者都未配置，不设置 CORS header（当前行为）。

4. **预检请求（OPTIONS）的 per-bucket 路由：** OPTIONS 请求也需要知道目标 bucket，以便应用正确的 CORS 规则。当前 OPTIONS 由 `CORS` middleware 在路由前拦截，需要改造为能访问 bucket context 的形式。

5. **CORS 规则缓存：** 如 `KeyCache` 的 pattern，对 per-bucket CORS 规则做 TTL 缓存，避免每次请求都查询数据库。

### 架构概要

```
当前:
  请求 → RequestID → CORS(全局) → Auth → ... → Handler
                    ╰── 使用 cfg.CORS.AllowedOrigins（环境变量）

改进:
  请求 → RequestID → CORS(全局 fallback) → Auth → ... → Handler
                    ╰── 提取 tenant + bucket → 查询 per-bucket CORS
                        → 如果有 per-bucket 规则：使用它
                        → 如果没有：使用全局规则

  新增组件:
    internal/middleware/cors_bucket.go
    ── 中间件在 Auth+Tenant 之后（需要 tenant context），REST/S3 路由之前
    ── 从请求路径提取 bucket
    ── 查询 repo.GetBucketCORS 获取 per-bucket 规则
    ── 缓存规则（TTL，避免每次请求查 DB）
    ── 如果规则存在，覆盖全局 CORS 响应头

  中间件链顺序更新:
    RequestID → CORS(全局) → Auth → Tenant → PerBucketCORS → ...
                                              ╰── 新增位置
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **OPTIONS 预检请求** | OPTIONS 在路由匹配之前，需要从 URL 中提取 bucket 名称之后应用 per-bucket 规则（当前 OPTIONS 在 CORS middleware 中直接返回 204） |
| **WebDAV/MCP 路径** | WebDAV 有自己的前缀路由，MCP 在 `/mcp` 路径。这些路径没有 bucket 概念，应继续使用全局 CORS 规则 |
| **不存在的 bucket** | 如果请求路径中的 bucket 不存在，降级到全局规则（不拒绝请求） |
| **CORS 规则变更后生效延迟** | 缓存 TTL 期间规则变更未生效。解决方案：短 TTL（30s）+ 缓存失效事件（类似 key cache invalidation） |
| **跨域请求的 x-amz-* header** | S3 自定义 header 需要在 `AllowedHeaders` 中显式声明。per-bucket 规则应覆盖这些 |

---

## 总结

这 5 个方向的共同特征是：**它们不是"新功能"，而是"已有功能的执行层补全"。** 所有涉及的 S3 API 都已经有表面的 CRUD 实现，但实际的运行时行为要么完全缺失、要么仅实现了一半。

| 方向 | 当前状态 | 目标状态 | 核心价值 | 工作量估计 |
|------|---------|---------|---------|-----------|
| **S3 合规测试套件** | 自写测试，零外部合规验证 | AWS SDK 集成测试 + 模糊测试 + 覆盖率报告 | S3 协议兼容性的可验证保障 | 1-2 周 |
| **StorageClass 主动分层** | 纯装饰性元数据 | 真正的多后端路由 + 生命周期迁移 | StorageClass 从"假的"变"真的"；成本差异化 | 3-4 周 |
| **Legal Hold + Retention API** | metadata hack + 无子资源 | S3 标准 `?legal-hold` 和 `?retention` 端点 | 合规场景的 API 完整性 | 1-2 周 |
| **生命周期规则完整性** | 仅 Expiration.Days + delete | 完整规则引擎（Transition/Noncurrent/Abort） | 数据生命周期管理从"哑巴"变"智能" | 3-4 周 |
| **Per-Bucket CORS 执行** | 配置存储不执行 | 请求时 per-bucket CORS 策略检查 | 安全"错觉"变真正的隔离 | 1 周 |

### 建议执行顺序

```
Phase 1（最低成本、最高安全收益）:
  ├── Per-Bucket CORS 执行  — 1 周 — 安全缺口修复
  └── Legal Hold + Retention API  — 1-2 周 — API 兼容性修复

Phase 2（质量基础设施）:
  └── S3 合规测试套件  — 1-2 周 — 为后续变更建立安全网

Phase 3（核心差异化、投入最大）:
  ├── StorageClass 主动分层  — 3-4 周 — 需要 TierRouter 抽象
  └── 生命周期规则完整性  — 3-4 周 — 依赖 StorageClass 分层
```

### 与前 41 期分析的去重关系

| 方向 | 有没有被前 41 期覆盖？ | 说明 |
|------|----------------------|------|
| **S3 合规测试套件** | ❌ **零覆盖** | 前 41 期无任何方向讨论 S3 handler 的合规验证基础设施 |
| **StorageClass 纯元数据→主动分层** | ⚠️ v40 方向三覆盖了"多后端分层"解决方案 | v40 聚焦于"如何实现多后端分层"，但未分析 **StorageClass 作为纯装饰性字段的现状** 这一独立缺口 |
| **Legal Hold + Retention API** | ⚠️ v25/v32 单行提及 | 仅作为列表达中的一行过路引用，**无独立架构分析** |
| **生命周期规则完整性** | ⚠️ v17 覆盖了 Transition/Noncurrent/Abort 需求 | v17 提出了需求方向，但 **未分析当前 `putBucketLifecycle` 的 XML 解析静默丢弃和 `lifecycle.go` 的执行引擎缺失** |
| **Per-Bucket CORS 执行** | ❌ **零覆盖** | 前 41 期无任何方向讨论 cors_rules 的存储→执行断层 |
