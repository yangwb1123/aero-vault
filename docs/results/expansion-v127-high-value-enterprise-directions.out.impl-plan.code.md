All 24 test packages pass. Here's the implementation report:

---

# 代码实现报告

## 实现概述

根据 `expansion-v141-server-copy-webhook-security-tracing.out.arch.md` 和 `.out.impl-plan.md` 的规划，实现了两个独立且无前置依赖的基础性任务：

1. **D1-01**: `Storage.Copy` 接口定义 + Local 后端实现
2. **D2-03**: Webhook 交付可观测性指标

遵循最小化变更和向后兼容原则，所有变更编译通过且现有测试全部通过。

## 文件清单

### 新建文件
- `internal/storage/local_copy.go` — Local 后端 `CanCopy()` / `Copy()` 实现

### 修改文件
- `internal/storage/storage.go` — 新增 `ErrUnsupported` error sentinel、`CopyOptions` 结构体、`CanCopy()` / `Copy()` 接口方法
- `internal/storage/s3.go` — S3 后端 `CanCopy()`/`Copy()` 桩方法（Copy 暂返回 `ErrUnsupported`，D1-03 实现）
- `internal/storage/oss.go` — OSS 后端 `CanCopy()`/`Copy()` 桩方法
- `internal/storage/cos.go` — COS 后端 `CanCopy()`/`Copy()` 桩方法
- `internal/telemetry/metrics.go` — 新增 4 个 Webhook 指标 + 5 个导出函数
- `internal/events/webhook.go` — `postOne`/`retryOne` 中嵌入指标调用
- `internal/storage/storage_test.go` — 为 `nonRewrapStore` mock 补齐 `CanCopy`/`Copy` 方法

## 核心代码实现

### D1-01: Storage.Copy 接口 + Local 实现

#### Storage 接口变更

```go
// 新增 error sentinel
var ErrUnsupported = errors.New("operation not supported by this backend")

// CopyOptions controls how Copy transfers an object between keys.
type CopyOptions struct {
    MetadataDirective string            // "COPY" (default) or "REPLACE"
    Metadata          map[string]string // used when directive is "REPLACE"
    ContentType       string            // overrides when directive is "REPLACE"
}

// 新增 Storage 接口方法
CanCopy() bool
Copy(ctx context.Context, srcKey, dstKey string, opts CopyOptions) (ObjectInfo, error)
```

#### Local 后端实现 (`local_copy.go`)

```go
// CanCopy returns true — local backend supports server-side copy.
func (s *LocalStorage) CanCopy() bool { return true }

// Copy duplicates the object using streaming I/O with a 32 KiB buffer,
// preserving SSE envelopes. Supports "COPY" and "REPLACE" metadata directives.
func (s *LocalStorage) Copy(ctx context.Context, srcKey, dstKey string, opts CopyOptions) (ObjectInfo, error) {
    // Uses OTel tracing (child span: "LocalStorage.Copy")
    // 1. Resolve srcPath, read metadata
    // 2. Resolve dstPath, create parent directory
    // 3. Stream src → temp file via io.Copy (no large allocations)
    // 4. Atomic rename temp → dst
    // 5. Write metadata (COPY source or REPLACE with overrides)
    // Returns dst ObjectInfo
}
```

**关键设计决策：**
- 使用 `io.Copy`（默认 32KB 缓冲区）而非 `io.ReadAll`，避免大对象 OOM
- 写入使用 temp file + `os.Rename` 原子模式，与 `Put` 一致
- SSE envelope 直接保留，加密 blob 按原样复制，无需解密再加密
- OTel 子 span `LocalStorage.Copy` 与现有 `LocalStorage.Get`/`Put` 的追踪模式一致
- S3/OSS/COS 后端 `CanCopy()=true` 但 `Copy()` 暂返回 `ErrUnsupported`，待 D1-03 实现

### D2-03: Webhook 交付可观测性指标

#### 新增指标 (4 个)

| 指标名 | 类型 | 标签 | 说明 |
|--------|------|------|------|
| `webhook.delivery_total` | Counter | `url`, `status_code` | 每次递送尝试 |
| `webhook.delivery_latency_ms` | Histogram | `url` | 递送延迟分布 |
| `webhook.dead_letter_total` | Counter | `url` | 死信事件计数 |
| `webhook.retry_queue_depth` | Gauge | `url` | 待重试队列深度（ObservableGauge） |

#### 导出函数

```go
func RecordWebhookDelivery(ctx context.Context, url string, statusCode int)
func RecordWebhookDeliveryLatency(ctx context.Context, url string, ms float64)
func IncWebhookDeadLetter(ctx context.Context, url string)
func RegisterWebhookQueueDepthGauge(fn func(context.Context) map[string]int64)
```

#### Webhook 集成

- `postOne()`: 每次 HTTP 调用记录 `delivery_total` + `delivery_latency_ms`（含错误路径）
- `retryOne()`: 当 `attempts >= 10` 时调用 `IncWebhookDeadLetter` 记录死信事件
- `IncWebhookRetry` 保持原有行为不变

## 依赖说明

无新增外部依赖。

## 已知限制

- S3/OSS/COS 的 `Copy` 方法尚未实现真正的 server-side copy（标识为 `TODO` 待 D1-03 完成）
- `webhook.retry_queue_depth` ObservableGauge 需要调用方在启动时注册回调函数（用法：`telemetry.RegisterWebhookQueueDepthGauge(func(ctx) map[string]int64 { ... })`）
- Local 后端的 `Copy` 在同一进程内是原子的，但发生并发写入时（同一 destination key 同时被 Copy 和 Put 操作），最后一个 `os.Rename` 会覆盖之前的文件——这是文件系统的自然行为，与现有 `Put` 一致

## 验证步骤

```bash
# 1. 编译
go build ./...

# 2. 全量测试
go test ./... -count=1 -short

# 3. 验证新接口编译
go vet ./internal/storage/...

# 4. 检查 gofmt
gofmt -l internal/storage/storage.go internal/storage/local_copy.go internal/events/webhook.go
```

## 配置要求

无新增配置项。
