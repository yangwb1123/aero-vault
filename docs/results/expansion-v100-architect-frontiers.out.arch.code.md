感谢您提供的角色说明。我已完整阅读了项目上下文，当前状态如下：

**当前项目状态：**
- **最新 Commit:** `fb43973 [pi-batch] Stage: technical-design` (已生成大量 `.arch.md` 和 `.impl-plan.md` 设计文档)
- **工作树:** 103 个文件已修改但未提交
- **Sprint 状态:** 端到端集成验证 — ✅ 全部完成
- **当前 TASK:** 上下文链建立 — ✅ 全部完成

`docs/results/` 下有多份架构设计和实现计划文档，最大的几份如：
- `expansion-v104-architect-systemic-gaps.out.arch.md` (39.7KB)
- `expansion-v91-storage-tiering-metadata-query-events-cache.out.arch.md` (39.7KB)
- `expansion-v114-s3-protocol-completeness-and-operational-gaps.out.arch.md` (36.7KB)
- `strategic-extensions.out.impl-plan.md` (47.6KB)
- `expansion-v80-systemic-production-gaps.out.impl-plan.md` (45.2KB)
- `expansion-directions-v3.out.arch.md` (25.7KB) + `.impl-plan.md` (36.9KB)

---

**请指定需要实现的目标：**

1. **哪份架构设计文档 (`.arch.md`)** 需要实现？
2. **对应的实现计划文档 (`.impl-plan.md`)** 是哪一份？
3. 或者您有新的功能需求直接描述？

一旦您提供具体的设计文档，我将：
1. 📖 完整阅读架构设计和实现计划
2. 🔍 分析现有代码结构
3. ✏️ 严格按照设计编写代码
4. ✅ 确保通过 `make check` (gofmt, go build, go vet, go test)
5. 📋 输出完整代码实现报告
