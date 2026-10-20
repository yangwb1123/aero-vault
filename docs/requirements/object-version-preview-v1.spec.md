# 历史版本预览与下载隔离 v1

## 范围

在对象详情“对象版本”页签增加固定 Version ID 的内容预览、关闭预览及刷新版本列表，复用既有版本列表、`GET /v1/files/{key}?version=ID` 与版本化缩略图端点。加固当前版本下载的异步隔离，并补正既有 REST 历史 GET 忽略 Range 的缺口。不新增 REST 路由、版本回滚、历史版本恢复/覆盖、分享或权限管理模型。

## 契约

- 用户从已加载版本列表选择具体版本；预览明确显示完整 Version ID。Key 与 Version ID 都是不透明值，不 trim、不推断底层 StorageKey。非空历史 pin 优先于 tags/versions/acl/metadata 后缀分发，防止把真实同名 Key 当成子资源；thumbnail 保留已有精确对象优先/派生规则。
- 文本通过 Range 前 4 KiB 安全预览，按文本渲染而非执行 HTML/脚本。REST 直接切分 FileService 已打开并授权的精确版本流，不用会定位当前对象的 GetRange 重开；支持单范围、suffix/open 范围、416 与 If-Range，304/412 优先于 Range。JPEG 使用既有受限缩略图；初始 Range 请求与后续缩略图请求都带同一 Version ID，JPEG 初始响应体不再使用时释放。
- 空 Version ID 不允许历史读取；后端 403/404/503 等错误直接显示，不自动回退到当前/最新版本。未提供 Version ID 的普通对象预览维持既有行为。
- 版本列表新增可选 `is_delete_marker`，按服务已有 IsDeleteMarker 语义提供事实。真正删除标记或无 Version ID 的行不提供预览/下载；普通软删除版本不等于删除标记，继续按既有历史读取授权提供操作；锁定版本仍可读取。此禁用只是 UX，授权、租户状态、保护与损坏检查仍由既有后端执行。
- 刷新版本列表、关闭预览或退出页签后，旧预览不能继续展示；切换对象/client 时清除选择、下载状态与错误。预览请求和版本下载在上下文变化或卸载时尝试 abort，transport 忽略 abort 时仍丢弃迟到结果。
- 后续复查的取消补强见 [`read-cancellation-boundaries-v1.spec.md`](read-cancellation-boundaries-v1.spec.md)：迟到 JPEG 首读不再启动后续缩略图，历史流在每次 Read 前后检查取消；已阻塞的底层 Read 不承诺可中断。
- 版本下载一次只发送一个请求；同步防重入，所有下载按钮在处理期间禁用。Blob 到达后、交给浏览器下载前再次检查当前对象/client/请求 epoch，旧响应不能生成下载链接或触发下载。
- 版本列表刷新不改变服务器对象；进行中的下载期间禁用列表刷新。选取其他版本预览不撤销用户已明确发起的同对象版本下载。
- 所有请求继续使用当前 VaultClient 凭据。不能把已有列表、缓存或预览 Blob 当作后端授权替代；取消/切换不能撤回已交给浏览器的文件，也不证明服务器未记访问事实。

## 验收

前端覆盖固定 ID 与完整特殊 Key、JPEG 双请求固定 ID、错误无回退、文本安全渲染、关闭/刷新/版本切换/client/对象/卸载隔离、下载防重入与迟到响应、删除标记与锁定行及 StrictMode。使用真实 VaultClient 加 mock fetch 验证 Header、Range、signal 和路径契约。

HTTP 使用 SQLite/local 验证新版本写入后历史 Range 字节不变、条件请求/416、错误 ID 不回退、删除标记区分、显式 Deny、租户隔离/停用及既有版本化缩略图回归。后端只补 REST 范围适配、列表事实和运行时 OpenAPI；不修改 Storage/Repository/AI/SDK/迁移。

执行 `make web-check` → `make build` → `make web-smoke`、定向 HTTP/race、`make check` + `python3 cli.py accept`。浏览器冒烟只确认嵌入应用加载；外部身份/存储联测、完整大文件下载、跨实例与撤权后浏览器已获字节回收不在本轮承诺内。
