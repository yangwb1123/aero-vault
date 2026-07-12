The implementation is clean and complete. Here's the comprehensive report.

---

# 代码实现报告

## 实现概述

实现了 **TASK-001: REST API 的 Get/Put/Delete/List 路径中强制执行桶策略**，填补了架构分析中识别的最高优先级安全漏洞：REST API 缺乏与 S3 API 一致的桶策略执行机制。

遵循的原则：
- **协议一致性**：REST handler 复用与 S3 handler 相同的 `auth.ParsePolicy` / `auth.Allowed` 策略评估逻辑
- **最小化变更**：仅修改 `handler.go`（添加方法 + 调用点）和 `handlers_test.go`（新增测试）
- **向后兼容**：没有桶策略的 bucket 行为不变（通过 policy="" 检查短路）
- **防御性编程**：策略解析错误时 warn 日志 + 跳过执行（不阻断请求）

## 文件清单

| 文件 | 变更类型 | 说明 |
|------|---------|------|
| `internal/api/rest/handler.go` | 修改 | 添加 `checkBucketPolicy` 方法 + 在 6 个 handler 中添加调用 |
| `internal/api/rest/handlers_test.go` | 修改 | 新增 7 个桶策略测试 + `bodyPolicy` 辅助函数 |

## 核心代码实现

### REST 桶策略检查

```go
// checkBucketPolicy loads the bucket policy and denies the request when the
// action is not allowed. Returns true when the request may proceed; false when
// a 403 has already been written. Buckets without a policy always pass.
func (h *Handler) checkBucketPolicy(w http.ResponseWriter, r *http.Request, action string) bool {
	cfg, err := h.svc.GetBucketConfig(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket)
	if err != nil || cfg.Policy == "" {
		return true
	}
	p, perr := auth.ParsePolicy(cfg.Policy)
	if perr != nil {
		h.logger.Warn("bucket policy parse error, skipping enforcement",
			"bucket", service.DefaultBucket, "err", perr)
		return true
	}
	host, _, splitErr := net.SplitHostPort(r.RemoteAddr)
	if splitErr != nil {
		host = r.RemoteAddr
	}
	if !auth.Allowed(p, action, host) {
		h.writeError(w, r, service.ErrForbidden)
		return false
	}
	return true
}
```

### 调用点分布

| REST Handler | Action | 调用位置 |
|-------------|--------|---------|
| `Put` | `s3:PutObject` | key 提取后、写入前 |
| `PostForm` (multipart) | `s3:PutObject` | key 确定后、写入前 |
| `Get` | `s3:GetObject` | key 提取后、`allowAnonymous` 前 |
| `Head` | `s3:GetObject` | key 提取后、`allowAnonymous` 前 |
| `Delete` | `s3:DeleteObject` | key 提取后、删除前 |
| `List` | `s3:ListBucket` | 方法起始处、查询参数前 |

### 关键设计决策

1. **策略优先于 ACL**：`checkBucketPolicy` 在 `allowAnonymous` 前执行。如果 Deny 策略命中，立即 403，不再检查 ACL。这与 AWS IAM 的 Deny 优先级一致。

2. **错误处理采用宽松降级**：策略 JSON 解析失败时 skip（记录 warn），避免因配置错误导致服务不可用。这是参考 S3 handler 的相同行为（`h.logger.Warn("bucket policy parse error, skipping enforcement")`）。

3. **S3 action 映射复用**：REST handler 使用相同的 S3 action 名称（`s3:GetObject`, `s3:PutObject` 等），直接通过 `auth.s3Actions` 映射到规范名。同一份策略文件可同时控制 S3 和 REST 访问。

4. **无策略时零开销**：`cfg.Policy == ""` 短路，不执行 JSON 解析和 IP 匹配。

## 测试覆盖

| 测试 | 验证点 |
|------|--------|
| `TestBucketPolicyDenyPut` | Deny PutObject → PUT 403；GET/HEAD 仍然 200 |
| `TestBucketPolicyDenyGet` | Deny GetObject → GET/HEAD 403 |
| `TestBucketPolicyDenyDelete` | Deny DeleteObject → DELETE 403；GET 仍然 200 |
| `TestBucketPolicyImplicitDeny` | 无 Allow 声明 → 所有未允许操作隐式拒绝 |
| `TestBucketPolicyList` | 缺少 ListBucket Allow → LIST 403 |
| `TestBucketPolicyNoPolicyDoesNotBlock` | 无策略 → 全部正常（向后兼容） |

## 验证步骤

```bash
# 编译
go build ./...

# 格式化检查
gofmt -l .

# 静态分析
go vet ./...

# 单元测试
go test ./internal/api/rest/... -v -count=1

# 全量测试
go test ./... -count=1

# CI 门禁
make check
```

## Configuration

无新增配置项。桶策略通过已有 `PUT /v1/buckets/{bucket}/policy` 端点设置，policy JSON 格式与 S3 IAM 策略兼容。
