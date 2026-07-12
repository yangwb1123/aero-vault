文档已保存至 `docs/requirements/expansion-v129-architecture-blindspots-and-platform-depth.md`（v129，接续前序 v128 的编号序列）。

文件内容包含了 5 个经去重确认的零覆盖方向：

1. **🔴 服务端拷贝协议与存储层 Copy 原语** — 当前 CopyObject 经内存中转，云后端无法利用服务端拷贝 API，缺失 `UploadPartCopy`
2. **🔴 存储后端在线迁移** — 后端变更目前只能停机重搭，无双写/回填/切读机制
3. **🟠 对象级访问审计轨迹** — `WriteAccessLog` 接口已存在但从未被任何 handler/middleware 调用
4. **🟠 不可变存储 & 内容寻址** — WORM 桶、SHA256 内容去重、Append-Only 模式完全缺失
5. **🟠 批量操作框架** — 现有批量方法（BatchDelete 等）同步单线程无进度，缺少统一 BulkJob 抽象

每个方向均包含：现状分析、必要性论证、影响层范围表、边界情况。最后附有三阶段优先级排序与依赖关系。
