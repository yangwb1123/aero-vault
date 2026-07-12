所有 25 个测试包全部通过，零失败。以下是实现总结：

---

## 实施总结：第 73 轮工程纵深缺口修复

已实施所有 **5 个方向**，均经过 `go build ./...`、`go vet ./...`、`gofmt -l .`（零输出）和 `go test ./...`（25/25 通过）验证。

### 更改为 9 个文件

| # | 方向 | 文件 | 更改内容 |
|---|------|------|---------|
| **1** | **Content-Length 字节验证** | `internal/service/file.go` | 新增 `ErrSizeMismatch` 错误 |
| | | `internal/service/file_crud.go` | 导入 telemetry；`Put()` 中新增 `info.Size != size` 验证 → 记录警告并递增指标 |
| | | `internal/telemetry/metrics.go` | 新增 `storage.size_mismatch_total` 计数器 + `IncStorageSizeMismatch()` |
| **2** | **版本列表 N+1 + 深层分页** | `internal/repository/repository.go` | 新增 `VersionListOpts`、`VersionListPage`、`ListObjectVersionsWithOpts` 接口方法 |
| | | `internal/repository/sql_objects.go` | 实现 `ListObjectVersionsWithOpts`——支持基于偏移量的 `Limit`/`VersionIDMarker` 分页 |
| | | `internal/api/s3compat/xml.go` | 为 S3 协议兼容新增 `VersionIdMarker`、`NextVersionIdMarker`、`DeleteMarkers` XML 字段 |
| | | `internal/api/s3compat/bucketconfig.go` | 解析 `version-id-marker` 查询参数；使用分页的 `ListObjectVersionsWithOpts`；在 `NextVersionIdMarker` 中链接至深层分页 |
| **3** | **中间件可观测性** | `internal/telemetry/http.go` | 新增 `WithMiddlewareTiming()`——为每个中间件层包裹 Prometheus 直方图 |
| | | `internal/telemetry/metrics.go` | 新增 `middleware.duration_ms{middleware}` 直方图 + `RecordMiddlewareLatency()` |
| | | `cmd/server/main.go` | 重构 `applyMiddleware()`——为每个中间件赋予名称，并通过 `WithMiddlewareTiming` 包裹 |
| **4** | **ETag 读取验证** | `internal/service/file_crud.go` | 新增 `ETagVerifier` 结构体（`Read` + `Close`）+ `NewETagVerifier` 工厂函数；修改 `Get()` 方法，在配置启用时使用可选验证包裹阅读器 |
| | | `internal/service/file.go` | 新增 `ReadVerificationConfig` 结构体 + `WithReadVerification()` 流畅设置方法 |
| | | `internal/telemetry/metrics.go` | 新增 `etag.verify_mismatch_total` 计数器 + `IncETagVerifyMismatch()` |
| **5** | **预签名 URL 审计（第一阶段）** | `internal/service/file_features.go` | `PresignGet()` 和 `PresignPut()` 现在记录调用者身份、租户、存储桶、键和过期时间；递增 `presign.generated_total` |
| | | `internal/service/file.go` | 新增 `callerFrom(ctx)` 辅助函数（从上下文中提取 API 密钥标签或租户） |
| | | `internal/telemetry/metrics.go` | 新增 `presign.generated_total`、`presign.consumed_total` 计数器 + 相应的 `Inc*`函数 |

### 关键设计决策

| 决策 | 理由 |
|------|------|
| **方向一：`size > 0` 时仅发出警告，不硬拒绝** | 保持与未知长度（分块）请求的向后兼容性；指标允许渐进式收紧 |
| **方向二：基于偏移量的分页，每个键保留 N+1 模式** | 单 SQL 查询替代需要重构数据模型（第二阶段）；分页解决了 S3 `version-id-marker` 协议合规性问题 |
| **方向三：Phase 1 Prometheus 直方图，而非 OTel span** | 更简单的 API，零额外依赖；未来可添加 OTel span 以进行分布式追踪 |
| **方向四：默认禁用，通过 `WithReadVerification()` 启用** | 避免对未选择加入的租户产生不必要的 CPU/内存开销 |
| **方向五：Phase 1 记录 + 指标，Phase 2 迁移 + 可撤销性** | 无状态变更，零模式破坏；使用结构化 `slog` 日志自动发送至日志基础设施 |
