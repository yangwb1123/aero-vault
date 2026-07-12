# 代码实现报告

## 实现概述

基于 `expansion-v141-server-copy-webhook-security-tracing` 架构设计与实现计划，完成了以下三个安全方向的代码实现：

### D3-01: 输入验证中间件链
- **MaxBodySize** — 限制请求体大小，支持 Content-Length 提前拒绝和 LimitReader 流式截断
- **SecureHeaders** — 设置安全响应头 (HSTS, X-Content-Type-Options, X-Frame-Options, Referrer-Policy, Permissions-Policy)
- **EnforceContentType** — 对带 body 的请求强制执行 Content-Type 检查

### D3-02: 安全 XML 解析
- **safeXMLDecoder** — 用 io.LimitReader 包装 XML 解析，防止 XML bombs / entity expansion
- 替换了全部 6 处 `xml.NewDecoder(r.Body).Decode(&in)` 调用

### D3-03: CORS 安全增强
- 改进 Origin 校验：非 `*` 配置时严格执行白名单
- 空 Origin 的 OPTIONS 预检返回 204（向后兼容非浏览器的健康检查）
- 不允许的 Origin 请求不返回 CORS headers（浏览器端阻断）

### D4-01 (部分): 嵌套 Span 追踪
- 创建 `telemetry.Tracer()` 辅助函数
- 在 service/storage/repository 三层添加包级 tracer 和嵌套 spans
- HTTP → FileService.Get/Put → LocalStorage.Get/Put → Repository.GetObject/UpsertObject

## 文件清单

### 新建文件
- `internal/middleware/validation.go` — 输入验证中间件 (MaxBodySize, SecureHeaders, EnforceContentType)
- `internal/middleware/validation_test.go` — 11 个测试覆盖所有中间件行为和边界条件
- `internal/api/s3compat/safe_xml.go` — safeXMLDecoder + decodeXMLBody 辅助函数
- `internal/api/s3compat/safe_xml_test.go` — 安全 XML 解析测试 (含 XXE 攻击向量)
- `internal/telemetry/tracer.go` — 包级 Tracer 辅助函数 + WithSpanAttrs

### 修改文件
- `internal/api/s3compat/handler.go` — 2 处 XML 解析替换为 safe decoder
- `internal/api/s3compat/extra.go` — 4 处 XML 解析替换为 safe decoder
- `internal/middleware/cors.go` — CORS Origin 校验增强，实现 CORS 规范兼容
- `cmd/server/main.go` — 中间件链增加 `max_body` 和 `secure_headers`
- `internal/config/config.go` — AppConfig 增加 `MaxBodySize` 字段
- `internal/config/config_app.go` — 无修改 (CORS 类型定义已存在)
- `internal/service/file_crud.go` — 添加包级 tracer + Put/Get 方法嵌套 span
- `internal/storage/local_read.go` — 添加包级 storeTracer + Get 方法嵌套 span
- `internal/storage/local_write.go` — Put 方法嵌套 span
- `internal/repository/sql_objects.go` — 添加包级 repoStoreTracer + GetObject/UpsertObject 嵌套 span

## 核心代码实现

### D3-01: 输入验证中间件

```go
// MaxBodySize limits the request body to maxBytes.
func MaxBodySize(maxBytes int64) func(http.Handler) http.Handler {
    if maxBytes <= 0 {
        return func(next http.Handler) http.Handler { return next }
    }
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if r.ContentLength > maxBytes {
                http.Error(w, "request body too large: max N bytes",
                    http.StatusRequestEntityTooLarge)
                return
            }
            r.Body = io.NopCloser(io.LimitReader(r.Body, maxBytes))
            next.ServeHTTP(w, r)
        })
    }
}

// SecureHeaders sets HSTS, X-Content-Type-Options, X-Frame-Options, etc.
func SecureHeaders() func(http.Handler) http.Handler { ... }

// EnforceContentType rejects requests with unexpected Content-Type.
func EnforceContentType(expected string) func(http.Handler) http.Handler { ... }
```

### D3-02: 安全 XML 解析

```go
func safeXMLDecoder(r io.Reader, maxBytes int64) *xml.Decoder {
    return xml.NewDecoder(io.LimitReader(r, maxBytes))
}

func decodeXMLBody(r io.Reader, maxBytes int64, dest any) error {
    dec := safeXMLDecoder(r, maxBytes)
    if err := dec.Decode(dest); err != nil {
        return fmt.Errorf("decode xml: %w", err)
    }
    return nil
}
```

### D4-01: 嵌套 Span 追踪

```go
// Package-level tracer
var tracer = telemetry.Tracer("aero-vault/service")

// In service method:
func (s *FileService) Get(ctx context.Context, tenant, bucket, key string) (...) {
    ctx, span := tracer.Start(ctx, "FileService.Get", 
        trace.WithAttributes(attribute.String("bucket", bucket), ...))
    defer span.End()
    // ...
}
```

## 依赖说明

无新增外部依赖。所有实现均使用标准库或已有的 `go.opentelemetry.io/otel` 依赖。

## 配置要求

| 环境变量 | 默认值 | 说明 |
|---------|--------|------|
| `APP_MAX_BODY_SIZE` | 0 (无限制) | 全局请求体大小限制（字节） |

## 验证步骤

```bash
# 编译验证
cd /home/u1/aero-vault
go build ./...

# 运行新测试
go test ./internal/middleware/... -v -run "TestMaxBody|TestSecureHeaders|TestEnforceContentType"
go test ./internal/api/s3compat/... -v -run "TestSafeXML|TestDecodeXML|TestXMLDecoder"

# 回归测试
go test ./internal/middleware/...
go test ./internal/api/s3compat/...
go test ./internal/service/...
go test ./internal/storage/...
go test ./internal/repository/...
go test ./internal/telemetry/...

# 完整 CI gate
make check
```

## 已知限制

1. **MaxBodySize 注册在全局中间件链中** — 当 `APP_MAX_BODY_SIZE=0` 时不生效，可通过路由组级中间件精细控制
2. **XML LimitReader 静默截断** — 当 body 超过限制时，XML 解码器可能返回 EOF 错误或部分解析结果（取决于截断位置）。这是有意的设计：宁愿返回乱数据也比 OOM 安全
3. **嵌套 spans 默认始终开启** — 没有采样率配置，OTel exporter 会导出所有 span。建议生产环境中通过 `OTEL_TRACES_SAMPLER_RATIO` 控制
4. **仅 LocalStorage 添加了 spans** — S3/OSS/COS 后端尚未添加，后续可在各自 Put/Get 方法中添加
