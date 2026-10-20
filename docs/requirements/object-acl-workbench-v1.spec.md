# 对象详情直接 ACL 管理 v1

## 范围

活动对象详情新增“权限规则”页签，复用既有资源 ACL 的读取、添加/更新、删除接口。只做对象直接规则的操作入口；不新增权限模型、API、迁移、SDK 或身份主数据接口，不实现完整有效权限视图或 ACL 模板。

## 契约

- 页签绑定 default bucket、当前完整 Key 和 object 类型，定位只读，不裁剪 Key 空白或 `%` 等合法字符；打开时读取当前对象的直接 ACL。已删除对象禁用该页签。独立“访问与发布”页面保留手动 object/folder/bucket 目标。
- 只展示 API 返回的直接规则，明确说明上级继承、归属、角色、scope、桶策略等并未完整列出；空列表不代表公开、可读或无访问限制。对象固定模式不提供继承选项，提交 inherit=false。
- 保留 user/department/group/role/authenticated/everyone 与显式 allow/deny。主体 ID 来自用户显式输入，不从姓名或邮箱推导组织身份。拒绝规则提交前确认：Deny 优先，可能令自己失去访问或管理能力，需要有权主体协助恢复；确认不是新增授权规则。
- 操作仍由后端 ACL 管理权限、租户绑定与状态检查决定；读取文件不等于管理 ACL。403/不可用/故障显示错误，不能转为空列表或继续无鉴权写入；普通删除权限不继承 write、owner 或通用 `*` 的原有契约不变。
- 多 action 的现有 PUT 是逐项写入而非跨规则事务。前端去重 action，空 action 不发送；错误或刷新失败后说明变更可能已生效，允许重新读取，不自动回滚、删除或重试写入。
- 对象/bucket/type/client 改变、退出页签或卸载后清除旧结果与提示；延迟 list/put/delete 不能展示到新目标或启动旧目标后续刷新。进行中的请求可能执行，切换不等于取消或回滚。以同步 request fence 防止同一目标重复并行提交。
- 保留自然键幂等更新与 ACL ID 绑定既有资源的后端边界。删除行只使用当前目标列表的 ID；本轮不改写原有 CreatedBy 删除授权语义。

## 验收

前端覆盖固定/手动目标、完整 Key、自动读取、原 API 参数、拒绝确认/取消、无权限错误、空 action、操作期间防重入、对象/client 切换和卸载隔离、回收站禁用与失败重读。SQLite/local HTTP 覆盖 read≠manage_acl、Deny 对读与分享生效、删除规则恢复访问、直接列表不含上级规则、tenant 隔离/停用及多 action 非事务边界。

交付执行 `make web-check` → `make build` → `make web-smoke`，以及 `make check` + `python3 cli.py accept` 和定向 race。受测试环境 I/O 影响时可用 Go 标准 `GOFLAGS=-p=1` 限制包并行，不调整超时或筛除测试。嵌入加载冒烟不等于外部身份/真实存储联测。
