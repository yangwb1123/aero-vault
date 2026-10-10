# “我的文件”视图 v1

## 用例与边界

在现有文件工作台增加“可访问文件 / 我的文件”切换，复用 FileService、可信归属字段和授权分页，不建立个人空间、群组空间、新服务或新表。现有 AI、Storage、Repository、SDK 与迁移不改。

“我的文件”是**归属与读取权限的交集**，不是私有空间或授权规则。上传仍沿用现有桶、Key、配额与 ACL；切换视图不改变文件权限。“可访问文件”保留既有列表行为，访问控制关闭时不因此新增隔离承诺。

## REST 契约

- `GET /v1/files?view=all|mine`；省略或空值等同 `all`，其他值返回 400。
- `GET /v1/folders?path=...&view=all|mine` 使用同一归属与读取权限规则，派生子目录只来自该视图内可见对象。
- `mine` 从服务端 Principal 获取 SubjectID，只接受有效 user/service 身份；其租户必须匹配请求租户，operator 的 `*` 租户仍允许选择请求租户。无身份、空 SubjectID、跨租户或匿名/分享/公开/系统身份返回 403。
- 不接受客户端指定归属主体；筛选依据保留的 `_aero_owner`，没有可信归属的历史对象不归入任何人的 `mine`，不自动补归属。
- 桶 ListBucket policy、显式 ACL Deny、租户停用及授权 provider 故障继续生效；筛选不能代替或绕过这些检查。
- `prefix`、`marker`、`limit`、`deleted=true` 保持现有语义。文件分页先筛选归属、再授权，收集 limit+1 个匹配项；`next_marker` 只来自当前返回页，不泄露被筛掉对象的 Key。客户端切换视图时必须重置游标。
- 列表响应 `Cache-Control: no-store`，保留 CORS Vary 并追加凭据/租户变化维度。运行时 OpenAPI 同步描述参数、身份及错误契约。

## 工作台

- 初始保留“可访问文件”，可切换到“我的文件”；文件、目录和回收站请求携带同一 view。
- 切换时清空已加载页、选择集及分页状态；旧请求结果不得混入新视图，403 不得伪装成空列表。
- “我的文件”不提供递归删除目录操作：同一 Key 前缀可能包含其他归属的对象，现有递归删除不会按 mine 筛选。单对象操作继续由后端独立授权。
- 创建空目录仍写零字节 marker 并沿用可信归属；使用非 nil 空 reader，避免存储读取 nil body。

## 验收与已知边界

覆盖混合归属、历史无归属、显式 Deny、稀疏分页、租户/桶隔离、停用租户、service 身份、匿名拒绝、非法 view、桶 policy、空目录、回收站、OpenAPI、浏览器请求参数与旧请求隔离。

执行 `make web-check` → `make build` → `make web-smoke`，并通过 `make check`、`python3 cli.py accept`；提交对应嵌入产物。

本轮不新增完整“分享给我”、团队空间或默认私有 ACL，不建设文件处理/RAG。目录单页仍最多读取 1000 个可见对象，现已支持继续列举（见 [目录分页 v1](folder-pagination-v1.spec.md)）；mine 使用现有分页扫描，没有新增 owner 查询索引，超大租户性能需专项验证。浏览器冒烟验证嵌入应用加载，身份与归属的行为由 SQLite/local HTTP 测试及前端交互测试覆盖，不代表外部身份系统联测通过。
