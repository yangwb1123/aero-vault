# Architecture Decision Records

---

## ADR-001

**日期:** 2026-06-13

**决策:** MCP server 新增 `write_file` / `delete_file` / `chat` 工具，并通过 `WithChat` 方法延迟注入 Chat 依赖

**原因:** MCP 协议的工具列表是动态的（`tools/list` 响应由运行时状态决定）。`chat` 工具只在 AI 已启用时才有意义。通过 `WithChat(*ai.Chat) *Server` 方法注入，而不是在构造函数中强制要求，保持了零值安全性：未配置 AI 时服务器正常工作，`tools/list` 不暴露 `chat` 工具。

**影响:** `Server` 结构体增加一个 `chat` 字段；`callTool` 分支仅当字段非 nil 时执行。Stdio MCP 路径（`runMCP`）需要独立构造 Chat 实例，因此需要调用 `buildLLM`。

**替代方案:** 在 `NewServer` 构造函数中接受 `*ai.Chat`（允许 nil）。拒绝是因为这会使测试构造更繁琐，且语义不清晰——`nil` 作为有意义的"AI 未启用"状态不如显式 opt-in 方法清晰。

---

## ADR-002

**日期:** 2026-06-13

**决策:** AI 端点速率限制使用独立的 `*middleware.RateLimiter` 实例，通过 `NewRouter` 参数传入

**原因:** 复用现有的 `RateLimiter` 类型（token bucket per tenant），但不共享状态——AI 操作（embedding、LLM 调用）的成本远高于存储 I/O，需要独立的 RPS 配额。将其作为 `NewRouter` 的最后一个参数传入，而不是在中间件层全局应用，保证了路由层的精确控制（只作用于 5 个 AI 路由）。`nil` 表示"不限制"，与现有 `NewRateLimiter(0,0)` 返回 nil 的行为一致。

**影响:** `NewRouter` 签名新增 `aiRL *mw.RateLimiter` 参数（最后一个）；`main.go` 新增一行构建 `aiRL`。所有现有测试不受影响（测试直接调用 handler，不经过 router）。

**替代方案:** 在 AI handler 层内部做限速（在 `AIHandler` 持有一个 limiter）。拒绝是因为这会让限速逻辑散落在业务逻辑中，而中间件层是正确的抽象位置。

---

## ADR-003

**日期:** 2026-06-13

**决策:** PII 检测器的信用卡规则增加 Luhn 校验，而非缩小正则表达式

**原因:** 缩小正则（例如仅匹配 `4[0-9]{15}` 的 Visa 前缀）会增加维护负担且覆盖不全。Luhn 算法是信用卡行业标准校验，误报率极低（随机 16 位数字通过 Luhn 的概率约 1/10）。实现为后置过滤（`ReplaceAllStringFunc` + luhn check）比修改正则侵入性更小，也更易测试。

**影响:** `pii.go` 增加两个辅助函数（`digitsOnly`、`luhn`）；`Scan` 和 `Redact` 对 credit_card 类型增加过滤步骤；其他规则不受影响。新增 `pii_test.go` 覆盖 Luhn 单元测试和集成场景。

**替代方案:** 依赖外部 PII 服务（Presidio）。作为内置轻量级实现，保持零依赖是正确的 trade-off；更完整的覆盖由 `RemoteExtractor` + 外部分类器提供。

---

## ADR-004

**日期:** 2026-06-13

**决策:** ChatStream 错误帧格式定为 `event: error\ndata: {"code":"…","message":"…"}\n\n`

**原因:** SSE 规范允许自定义事件类型（`event:` 字段）。客户端可以通过 `addEventListener("error", handler)` 明确监听，与正常的 `token`/`citations` 事件分离。JSON 格式的 `data` 字段（而非纯文本）让客户端可以结构化解析错误码，实现自动重试（BudgetExceeded → 提示用户）与内部错误区分。`encoding/json` 序列化防止 message 中的引号破坏 SSE 帧。

**影响:** 仅修改 `internal/api/rest/search.go`；新增 `writeSSEError` helper；现有 token/citations 帧不变。

**替代方案:** 在 SSE 流末尾发送 `data: {"error":"…"}`（无 event 类型）。拒绝是因为客户端无法用 EventSource API 区分 token 数据和错误数据。

---

## ADR-005

**日期:** 2026-06-13

**决策:** `AI_CHUNK_WINDOW` / `AI_CHUNK_OVERLAP` / `AI_AGENT_MAX_STEPS` 通过 config 注入，而非 `NewChunker()` / `NewAgent()` 新参数

**原因:** 项目的 config 层（`internal/config/config.go`）是所有环境变量的唯一入口，所有可调参数都应该通过它暴露。在构造函数中增加参数（breaking change）不如在 config 结构体中增加字段 + 在 main.go 中注入（non-breaking，零值=旧默认值）。`Chunker` 的字段（`Window`、`Overlap`）已经是公开的，直接赋值即可。`Agent.MaxSteps` 同理。

**影响:** `config.go` 新增 3 个字段；`main.go` 两行修改；`Chunker` 和 `Agent` 源码不变。
