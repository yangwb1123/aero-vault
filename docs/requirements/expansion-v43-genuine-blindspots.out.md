您的 v43 分析我已经完整阅读，并逐一验证了代码库中的每个锚点。以下是我的技术审阅意见：

---

## ✅ 分析准确性验证结果

我深入查看了所有 5 个方向的锚定代码，确认**分析结论与代码现状高度吻合**。每个方向的"核心矛盾"均能直接从代码中找到对应证据：

### 方向一：预签名 URL 安全策略 — ✅ 准确

- `internal/storage/sign.go` 的 `signLocal` 仅对 `method + objectKey + expires` 做 HMAC，**没有任何策略条件约束**
- `internal/service/file_features.go:PresignGet/PresignPut` 只透传 `expiry`，无策略参数
- REST handler (`handler.go:Presign`) 只接受 `op` 和 `expires` 两个 query 参数
- 与 S3 SigV4 预签名的 `X-Amz-SourceIp`、`X-Amz-Content-Type` 等条件形成鲜明对比

### 方向二：外部身份联合 — ✅ 准确

- `internal/auth/auth.go:Registry` 的 `authenticateBearer` 仅在本地 `map[string]Key` + `keyCache` 中查找，**无 OIDC/SAML 流程**
- `config.go` 中 Auth 配置只有 `AUTH_KEYS/JWT_SECRET/PERSIST_KEYS`，**无任何 OIDC/SAML 配置项**
- SigV4 验证 (`sigv4.go`) 也是基于本地配置的 `map[string]credential`

### 方向三：多通道通知交付 — ✅ 准确（且这是最严重的断层）

- **确认发现：** `BucketConfig.NotificationRules` 在 3 个地方被写入/读取（S3 REST handler + FileService + sql_buckets.go），但 `internal/events/` 下**没有任何代码读取或消费这些规则**
- `main.go:startWebhook` 只从 `cfg.Events.WebhookURL` 创建全局 webhook，bucket level 规则完全被忽略
- `NotificationRule` 结构体的 `TopicARN`、`LambdaARN` 字段注释明确写着 `"unused, kept for compat"` — **S3 API 的死数据**

### 方向四：SLA/SLO 合规测量 — ✅ 准确

- `internal/telemetry/metrics.go` 有 17 个 domain 计数器/直方图，但**不存在任何 SLA 配置、滚动窗口计算器或合规指标**
- `config.go` 中无 `sla` 配置节
- Prometheus 告警规则 (deploy/prometheus) 有延迟/队列深度告警，但**无错误预算告警**

### 方向五：推送式遥测导出 — ✅ 准确

- `internal/telemetry/otel.go` 的 Setup 只在 `OTEL_EXPORTER_OTLP_ENDPOINT` 设置了**拉取式** OTLP exporter
- `internal/telemetry/prometheus.go` 只提供 `/metrics` 拉取端点
- Remote Write、StatsD、Datadog、业务指标框架**全无**

---

## 技术建议与优先级调整

基于代码验证，我建议以下实施顺序（微调整）：

### P0（当前 Sprint）: 多通道通知执行引擎

这是**唯一一个存在"死数据"的方向**——NotificationRules 已存储但永不执行，意味着 S3 API 返回成功但静默不工作，这对协议兼容性的信任打击最大。

**实施步骤建议：**
1. 新增 `internal/events/router.go` — `NotificationRouter`，读取 bucket rules + 事件过滤分发
2. 定义 `Notifier` 接口 + `HTTPNotifier`（复用现有 webhook.go）
3. 在 `cmd/server/main.go` 中替换 `startWebhook` 为 `startNotificationRouter`
4. 规则缓存（`GetBucketNotifications` 定时刷新）

### P1（当前 Sprint）: 预签名 URL 策略

改动面小（仅 `storage.Storage` 接口 + `sign.go`），但安全价值高。

**建议实施范围：**
- `PresignPolicy` 结构体 + 签名中嵌入 policy hash
- `PRESIGN_MIN/MAX_EXPIRY_SECONDS` 全局策略配置
- 审计日志记录签发事件

### P1（下一 Sprint）: OIDC 身份联合

企业采购的准入门槛，但架构影响较大（Auth 流程改造）。

**建议先做 OIDC，后做 SAML** — OIDC 协议简洁且市场占比 >70%。

### P2: SLA/SLO + 推送遥测

依赖已有可观测性基础设施，可在通知引擎和预签名 URL 之后并行实施。

---

您希望我从哪个方向开始实施？建议从 **P0 通知规则执行引擎** 入手（最大的协议兼容性断层），我可以先画出详细设计并开始编码。
