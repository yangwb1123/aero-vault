现在我已经掌握了完整的背景信息，可以开始提供建议了。以下是基于实际代码库对文档的评估，以及我的实施计划。

---

## 评估摘要

v46 分析整体上准确反映了代码库的现状。我对文档中的一些小问题做了交叉验证，但整体方向是正确的。**最关键且立即可行的工作**是 Web UI 硬化、OpenAPI 契约测试和 DX 基础建设。

我已经准备好立即开始实施**当前冲刺阶段**的项目。以下是我的具体计划：

---

### Sprint N（立即开始）

**1. 🔴 Web UI XSS 防护 + 错误处理 + 加载状态**

文档中提到的 `innerHTML` 问题在代码中实际上已经通过 `textContent` 和 `el()`（基于文本节点的 DOM 创建）得到了缓解，但确实缺失错误处理和加载状态。我将添加：
- 所有 `fetch()` 调用的 `.catch()` 错误处理 + 全局 toast 通知层
- 上传/搜索的加载指示器（CSS spinner）
- 空状态提示
- 所有 CSS 文件中使用 `nonce` 的 CSP 响应头

**2. 🟢 OpenAPI 路由一致性测试**

创建一个测试，验证 `openapi.json` 中声明的所有路由是否与 chi 路由器匹配：

```go
// internal/api/rest/openapi_contract_test.go
func TestOpenAPIRoutesMatchHandlers(t *testing.T) {
    // 解析 openapi.json，提取路径列表
    // 构建测试 chi 路由器
    // 验证 spec 中的每条路径 + 方法都已在路由器中注册
}
```

**3. 🟢 创建 `internal/testutil` 包**

将所有测试中反复出现的 `t.TempDir()` + `repository.Open` + `Migrate` + `storage.NewLocal` 样板代码集中到一处。

**4. 🟢 从 `-ldflags` 动态注入版本号**

消除硬编码的 `"0.1.0"`，改为通过 `git describe --tags` 注入。

---

你希望我从哪个方向开始？是先从**Web UI 硬化**开始（最大用户可见影响），还是先从**基础设施**（OpenAPI 测试 + testutil 包 + 动态版本）开始？
