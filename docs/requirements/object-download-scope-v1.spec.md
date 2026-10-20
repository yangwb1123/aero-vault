# 对象下载上下文隔离与准备取消 v1

## 范围与动机

工作台普通下载目前把 Blob 交给浏览器后才检查操作是否仍属于当前上下文；切换身份、目录或视图期间的迟到响应仍可触发下载。复用历史版本下载已验证的请求 epoch/fence 思路，收敛为一个对象下载 hook，普通/版本读取继续走既有 VaultClient/FileService。

本轮增加普通文件“取消下载准备”，不新增 API/后台任务、SDK、存储/数据库接口或配置，不实现刷新续传、批量下载、字节进度或直接落盘。

## 契约

- 一个面板上下文同一时间只准备一个下载；同步防同一轮重复点击，目标 Key/可选 Version ID 在开始时捕获，不 trim、不推断物理路径。
- 工作台上下文绑定 client、目录、all/mine 和回收站状态；版本面板绑定 client 与对象 Key。切换上下文/身份或卸载时尝试 abort 请求，清除旧提示；即使 transport 忽略 abort，迟到 Blob 在交付浏览器前也必须被丢弃。A→B→A 不使旧请求重新有效，旧 finally 不能解锁新请求。
- 使用当前 client 的既有 GET，普通下载不自动带版本 pin；版本下载仍带确切 Version ID。403/404/provider 错误直接显示，不回退、不自动重试、不生成 URL。列表可见不替代后端读取授权。
- 用户取消只发生在 Blob 尚未交付浏览器的准备阶段，AbortSignal 同时用于请求和读取响应体；停止尚未结束时保持本轮锁定，transport 不响应 abort 时保持“正在停止”。取消后迟到成功不得触发下载。
- 进入浏览器交付阶段前同步关闭取消能力，防止旧按钮回调把已交付文件伪装成取消成功。已交给浏览器的下载无法撤回；成功提示只说明“文件已交给浏览器处理”，不证明用户已保存、浏览器下载完成或服务端未产生访问事实。
- 普通下载是读取操作，不再为其自动刷新文件/目录列表、清除分页或分享面板。正常刷新/上传完成刷新不撤销同一上下文中的显式下载；目录、视图、回收站或 client 改变才使请求失效。
- 原有列表、目录及批量操作在下载准备期间沿用 busy 禁用；目录/视图切换仍允许并使旧下载失效。回收站不新增普通读取入口。
- 不把 `response.blob()` 或加密存储读取宣称为端到端有界内存流式；取消不是服务器未读取/未审计的保证。普通与精确版本均复用已授权对象下载实现，不引入通用任务框架。

## 验收

真实 VaultClient + mock fetch 覆盖凭据、特殊 Key、GET/signal、403/404、取消后迟到成功、同一轮防重入、目录/视图/回收站/client/卸载切换、A→B→A、旧 finally 对新请求的隔离、StrictMode，以及交付阶段取消无效。已有普通/版本下载 URL 生命周期与错误测试持续通过；下载不额外刷新列表。

执行 `make web-check` → `make build` → `make web-smoke`、既有 HTTP 对象/版本授权及定向 race、`make check` + `python3 cli.py accept`。本轮不改生产后端；浏览器冒烟仅加载应用，不代替登录后真实存储、外部身份、大文件或服务端取消时点专项验收。

## 下载轮验证记录（当时）

- 已复现旧 client 的迟到 Blob 仍触发下载；新增回归在修复后通过。
- `make web-check`（235 项测试）→ `make build` → `make web-smoke` 通过，嵌入产物同步；既有对象/版本读取授权的定向 REST/service race 通过。新增 TypeScript 函数 AST 核对 ≤50 行，decision-count 复杂度 ≤10。
- **完整交付门禁尚未通过。** `GOMAXPROCS=4 GOFLAGS=-p=1 make check` 两次在既有 `internal/thumbnail/semaphore_test.go:129` 的 `TestGenerateSemaphoreBoundsConcurrentAllocation` 失败；同配置独立运行该单例通过，不足以推翻整包失败。
- 同配置独立执行 `python3 cli.py accept` 亦在该测试失败。整包观察到峰值 1,949,099,416 / 1,411,755,576 / 2,217,616,992 bytes，校准后的测试上限约 1.21 GB；根因未确认，不将其直接称为测试误报。
- 本轮未改缩略图实现/测试、阈值或门禁范围，未提交、合入或部署。日志保存在 `/tmp/aero-file-download-{check,check-retry,accept,thumbnail-retry}.log`；缩略图需另行定位后再执行完整门禁。

## 后续门禁修复验证

缩略图采样问题在独立需求 [`thumbnail-memory-bound-validation-v1.spec.md`](thumbnail-memory-bound-validation-v1.spec.md) 中定位并修复，先前失败记录保留。完成修复后，按相同 `GOMAXPROCS=4 GOFLAGS=-p=1` 完整执行 `make check` 与 `python3 cli.py accept` 均通过；前端交付三步重新执行，235 项前端测试通过，嵌入产物未变。此结果不代表进程 RSS、大文件或外部存储验收；尚未提交、合入或部署。
