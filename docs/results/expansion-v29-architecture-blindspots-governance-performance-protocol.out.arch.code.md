全量测试通过，`make check` 通过。

---

# 代码实现报告

## 实现概述

基于 `docs/results/deep-production-gaps-v1.out.arch.md` 和 `docs/results/deep-production-gaps-v1.out.impl-plan.md`，实现了四个方向的生产盲区修复。所有变更保持向后兼容，现有测试全部通过。

## 文件清单

### 方向 1：读路径数据完整性验证

| 文件 | 变更 |
|------|------|
| `internal/config/config_storage.go` | 新增 `VerifyOnRead`, `VerifyMaxSize`, `VerifySample` 配置字段 |
| `internal/config/config.go` | 新增配置解析，env vars: `STORAGE_VERIFY_ON_READ`, `STORAGE_VERIFY_MAX_SIZE`, `STORAGE_VERIFY_SAMPLE` |
| `cmd/server/main.go` | 连接 `WithReadVerification(cfg)` |
| `internal/service/file_crud.go` | 新增 `ETagVerifierConfig`、`NewSamplingETagVerifier`、`isMultipartETag`；优化 `Get()` 优先使用 `_aero_content_md5` 校验；连接 `IncETagVerifyMismatch` 指标 |
| `internal/service/file_multipart.go` | `InitMultipart` 中调用 `storeContentMD5(&opts)` 持久化 Content-MD5 |

### 方向 2：桶级 CORS

| 文件 | 变更 |
|------|------|
| `internal/middleware/cors_bucket.go` | **新文件**：`BucketCORSProvider` 接口、`bucketCORSProvider` TTL 缓存实现、`BucketCORS()` 中间件、`bucketFromPath()` 解析 |
| `cmd/server/main.go` | 创建 `corsProvider`、传入 `applyMiddleware` 和 `buildRouter`、中间件链新增 `cors_bucket` |
| `internal/api/rest/handler.go` | Handler 新增 `corsProvider`、`WithCORSProvider()` 方法、`PutBucketCORS`/`DeleteBucketCORS` 添加缓存失效 |
| `internal/api/rest/router.go` | `NewRouter` 支持可选 opt 参数 |

### 方向 3：Metadata API

| 文件 | 变更 |
|------|------|
| `internal/repository/repository.go` | 接口新增 `SetObjectMetaKeys`, `ReplaceObjectMetadata`, `DeleteObjectMetaKey` |
| `internal/repository/sql_objects.go` | 三个方法的 SQL 实现 |
| `internal/service/file_features.go` | 新增 `PutMetadata`, `PatchMetadata`, `DeleteMetadata`, `DeleteMetadataKey` |
| `internal/api/rest/handler_metadata.go` | **新文件**：`PutMetadata`, `PatchMetadata`, `DeleteMetadata` HTTP handler |
| `internal/api/rest/router.go` | `putKey` 添加 `/metadata` 路由；`deleteKey` 添加 `/metadata`；新增 `patchKey` 分发器 + PATCH 路由 |

### 方向 4：多分片上传幂等性

| 文件 | 变更 |
|------|------|
| `internal/service/file_multipart.go` | `UploadPart` 幂等性（去重+缓存）；`CompleteMultipart` 幂等性（重放结果）；`AbortMultipart` 幂等性（已终止→204）；新增 `multipartCompleteKey/PartKey/AbortKey` 辅助函数 |

## 核心设计决策

| 决策 | 选项 | 选择 | 理由 |
|------|------|------|------|
| 读路径验证 | Service 层 TEE wrapper | 方案 B | 最小入侵，不与 Storage 接口耦合，与现有 `md5WrapReader` 对称 |
| 大文件采样 | 全量/抽样 | 抽样 | 默认 10% 采样，降低 CPU 开销，最小 4KiB |
| Multipart ETag | 跳过/校验 | 跳过含 `-` 的 ETag | S3 多分片 ETag 非内容 MD5，无法校验 |
| 桶级 CORS | 双阶段 | 全局 CORS + 桶级覆盖 | 保持 OPTIONS 预检快路径，不破坏现有顺序 |
| 桶 CORS 缓存 | TTL 内存 | 60s TTL + 写入失效 | 避免每次请求查库，配置量小无需 Redis |
| Metadata API | 三种操作 | PUT/PATCH/DELETE | 对标 Tags 完整 CRUD，PATCH 增量合并，PUT 全量替换 |
| Multipart 幂等性 | 新表/复用 idempotency_keys | 复用 idempotency_keys | 无 schema 变更，key 前缀防冲突，TTL 清理自动生效 |

## 验证步骤

```bash
# 编译验证
go build ./...

# 全量测试
go test ./... -count=1 -timeout 120s

# CI 门禁
make check

# 手动验证
# 读路径验证：
STORAGE_VERIFY_ON_READ=true STORAGE_VERIFY_MAX_SIZE=10485760 go run ./cmd/server &
curl -X PUT -d "hello" http://localhost:8080/v1/files/test.txt
curl http://localhost:8080/v1/files/test.txt  # 正常读取

# 元数据 API：
curl -X PUT -d '{"color":"blue"}' http://localhost:8080/v1/files/test.txt/metadata
curl -X PATCH -d '{"shape":"round"}' http://localhost:8080/v1/files/test.txt/metadata
curl -X DELETE http://localhost:8080/v1/files/test.txt/metadata?key=color
curl -i http://localhost:8080/v1/files/test.txt  # 查看 X-Meta-* 头

# 桶级 CORS：
curl -X PUT -d '[{"AllowedOrigins":["https://app.example.com"],"AllowedMethods":["GET"]}]' \
  http://localhost:8080/v1/buckets/default/cors
curl -H "Origin: https://app.example.com" -H "Access-Control-Request-Method: GET" \
  -X OPTIONS http://localhost:8080/v1/files/test.txt -v  # 检查 Access-Control-Allow-Origin
```

## 配置说明

| 环境变量 | 默认值 | 说明 |
|----------|--------|------|
| `STORAGE_VERIFY_ON_READ` | `false` | 启用读路径 ETag 校验 |
| `STORAGE_VERIFY_MAX_SIZE` | `10485760` (10MB) | 全量校验阈值，超过则采样 |
| `STORAGE_VERIFY_SAMPLE` | `true` | 大文件启用采样校验 |
