# 管理 API、前端与可观测性实施计划

## 1. 管理 API

| ID | 状态 | 依赖 | 输出 | 核心测试与完成条件 |
| --- | --- | --- | --- | --- |
| ADM-001 | DONE | DB-011、API-002 | 已实现订阅联表列表、创建和部分修改 API，按读写权限隔离，并追加持久审计 | 唯一性、唯一有效来源、historical/interval capability、不可变身份、PATCH 三态、启停语义、严格 JSON 和真实 PostgreSQL HTTP 契约通过；不支持 delete/backfill_from |
| ADM-002 | DONE | ING-006、API-002 | 已实现单范围 backfill API，复用既有 BackfillService/Worker 生命周期 | 单 Instrument/Provider/interval/range；严格 JSON；`ingestion.manage`；审计上下文不可伪造；202；重复活动范围 409；真实 PostgreSQL HTTP 契约通过 |
| ADM-003 | DONE | DB-013、ING-005 | 已实现 Run/Task 列表与详情读模型及鉴权 API | Run status 筛选/响应基于 Task 真相；来源、范围和时间筛选；绑定筛选条件的 UUIDv7 游标；固定错误摘要和白名单详情脱敏；真实 PostgreSQL HTTP 契约通过 |
| ADM-004 | DONE | ING-003～005、API-002 | 已实现带权限和持久审计的 retry/cancel API，手工重试创建独立 repair/manual Run/Task，取消复用协作式行锁与 RunService | 原 failed Task 不变；活动重试唯一；失效来源、各状态取消冲突、Worker 零污染契约及真实 PostgreSQL HTTP 测试通过 |
| ADM-005 | DONE | SCH-002、DB-013 | 已实现只读 Provider 状态聚合 API，按有效订阅 scope 动态计算配置状态、任务失败和行情新鲜度 | configured 与 health 分离；连续市场与美股日历复用；休市 not_applicable；日历越界 unhealthy；真实 PostgreSQL HTTP 契约通过；请求不调用 Provider |

所有写 API 必须记录操作者、reason 和 Request ID，且不得直接修改行情记录。

ADM-001 冻结以下实现契约：

- 列表是一次联表读模型，返回 Subscription UUID、Provider/Instrument/ProviderInstrument 可读编码和外部 symbol；默认 50、最大 100，UUIDv7 游标绑定全部筛选条件。
- 创建按 `provider + instrument_code + 当前时刻` 解析启用且有效的唯一 ProviderInstrument；零个或多个候选均拒绝，不猜测来源。映射必须声明 `historical=true` 且 interval 位于 capabilities 中。
- POST/PATCH 需要 `subscriptions.manage`，GET 需要 `operations.read`。静态 Bearer 仅是首期运行时实现，Application/Handler 不依赖具体认证机制。
- PATCH 请求不包含身份字段，只允许四个设置；`revision_delay_seconds` 保留 absent/null/integer 三态。禁用只阻止 Scheduler 后续建任务，不取消已运行 Task，也不触发 backfill。
- 每次成功创建/修改都在同一订阅行的 `metadata.audit_log` 追加 action、操作者、actor type、Request ID、reason 和 UTC 时间；不新增 migration，管理响应不公开内部审计 JSON。
- 首期没有 DELETE、`backfill_from` 或批量创建；重复 `provider_instrument_id + interval` 返回 `409 SUBSCRIPTION_ALREADY_EXISTS`。

ADM-002 冻结以下实现契约：

- `POST /api/market-info/v1/ingestion-runs/backfill` 一次只接受一个 Provider、Instrument、`1h/1d` interval 和 `[start_time,end_time)` 历史范围，不接受数组、批量字段或隐式 `backfill_from`。
- 端点要求 `ingestion.manage`。操作者、actor type 和 Request ID 从认证上下文注入，客户端只负责提供非空、去除首尾空白且不超过 512 字符的 reason。
- 成功只表示一个 pending Run/Task 已原子持久化，返回 `202` 及 `run_id/task_id/status/created_at`；接口不调用 Provider，也不等待 Worker。
- 相同 Subscription 和完全相同范围已有活动 backfill 时返回 `409 BACKFILL_ALREADY_RUNNING`；终态后仍允许再次创建以产生 revision。缺少匹配的启用订阅返回 `404 SUBSCRIPTION_NOT_FOUND`。
- ADM-002 不新增 migration，继续使用 ING-006 的事务级 advisory lock、单 Run/Task 写入和 Task 内 Adapter 分页。

ADM-003 冻结以下实现契约：

- 四个 GET 端点统一要求 `operations.read`；Run/Task 列表默认 50、最大 100，按 UUIDv7 降序，游标绑定全部筛选条件，创建时间范围采用 `[created_from,created_to)`。
- Run Repository 在单一聚合查询中读取 Task 各状态计数，并使用 derived status 做列表筛选；Application 复用 ING-005 `SummarizeRun` 生成最终状态和计数，不信任可能滞后的 Run 缓存。
- Task Repository 一次联表返回 Run、Subscription、Provider、Instrument、ProviderInstrument 身份与操作字段；列表和详情不调用 Provider，不产生 N+1 目录查询。
- Run context 只公开运维白名单字符串；Task 不公开原始 `error_message`，错误摘要由标准化 code 固定映射，未知 code 降级为 `internal_error`，details 首期只允许合法 `provider_code`。
- 详情不内嵌无界 Task/重试后继数组；页面通过 Task 列表 `run_id` 筛选追踪。ADM-003 不新增 migration，专用管理索引留待实际查询计划证明需要后再添加。

ADM-004 冻结以下实现契约：

- retry/cancel 都要求 `ingestion.manage`，只接受严格 JSON `reason`；操作者、actor type 和 Request ID 来自认证上下文。
- retry 只接受 `failed` Task，新建 `repair/manual` Run 和 pending Task，复用原订阅与范围并写 `retry_of_task_id`；原 Task 不修改。活动后继由原 Task 行锁和部分唯一索引双重防重。
- retry 在创建事务中检查订阅、ProviderInstrument capability/有效期、Provider、Instrument 和 Asset 当前可用；失效来源和状态冲突使用稳定 409 code。
- cancel 在同一 Task 行锁事务中提交状态、取消字段和父 Run 内部 audit operation；成功后 best-effort 调用统一 RunService，查询仍以 Task 真值纠偏。
- 真实 PostgreSQL HTTP 契约覆盖 202、重复 retry、失效来源、pending cancel、重复/终态 cancel、审计持久化及 Run 状态刷新；不新增 migration。

ADM-005 冻结以下实现契约：

- `GET /api/market-info/v1/providers/status` 要求 `operations.read`，不接受筛选、分页或即时 probe；全部 Provider 按 code 返回，disabled/空订阅仍可见。
- Repository 使用一次 CTE 聚合 Provider、当前有效订阅、checkpoint 和 Task 最近成功/失败事实；Application 按 `provider + market + interval` 计算 scope，不持久化第二套 health 真值。
- 连续市场复用 SCH-001 UTC 窗口，美股复用 SCH-002 日历；休市强制 `closed + not_applicable + null delay` 且不因休市降级，日历越界明确投影为 unhealthy/unknown。
- 连续失败阈值为 3 次，严重延迟阈值为 3 个 interval；部分失败/延迟为 degraded，全部缺少成功事实为 unknown。Provider 配置 degraded 为健康上限，disabled 不参与健康判断。
- 单元测试覆盖连续/休市/失败/延迟/disabled/degraded/越界；真实 PostgreSQL HTTP 测试覆盖 fresh、unhealthy、disabled、鉴权和来源聚合。接口不调用 Provider，不新增 migration。

## 2. 前端纵向切片

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| UI-001 | DONE | ADM-005 契约 | 已在现有 XR-Trading Web 页面实现 Provider 状态面板，并通过服务端只读同源代理连接状态 API | healthy/degraded/unhealthy/unknown/closed、scope/无 scope、加载/错误/空数据、刷新和移动端展示通过；前端展示模块行/函数覆盖率 100%、分支 88.89%；浏览器真实 API、503 与重试恢复通过 |
| UI-002 | DONE | ADM-001 契约 | 已在数据采集页实现订阅查询、游标续页、创建和设置修改，并由同源 BFF 隔离读写权限 | 表单校验、重复订阅冲突、本地化 error.code、只读/管理视图、桌面/移动端及真实 API 创建/停用/筛选通过；无 delete/backfill_from；订阅展示模块行/函数覆盖率 100%、分支 80% |
| UI-003 | DONE | ADM-003 契约 | 已实现 Run/Task 双视图、筛选、游标续页、详情和手工状态刷新，并由同源 BFF 提供精确只读代理 | Task 真相汇总、Run→Task 跳转、时间/状态/身份筛选、空态/校验、脱敏错误、桌面/移动端通过；UI-003 模块行/函数覆盖率 100%、分支 88.24% |
| UI-004 | DONE | ADM-002/004 契约 | 已实现单范围 backfill、失败 Task retry 和活动 Task cancel，并由独立权限与精确 BFF 写代理保护 | 页面内二次确认、请求期间重复点击防护、稳定错误本地化、202 后按 run_id 筛选并打开新 task_id；真实 PostgreSQL/API/浏览器三条操作链路通过；展示模块行/函数覆盖率 100%、分支 88.14% |
| UI-005 | DONE | QRY-001～003 契约 | 已在资产研究页实现 Asset→Instrument→Provider→Interval 联动、多来源最新行情和 K 线查询，并由精确同源只读代理连接公共 API | 默认 Instrument/Provider/1h 可见且 K 线显式发送三项；时间/排序/游标、空态、错误、桌面与移动端及真实 API 浏览器链路通过；展示模块行/函数覆盖率 100%、分支不低于 80% |

前端可以在 API 契约冻结后使用 mock 并行开发；统一 API client 与错误映射文件由唯一负责人维护。

UI-001 冻结以下实现契约：

- 复用仓库已有的原生 HTML/CSS/JavaScript 页面与 Python Web 入口，不另建第二套前端工程；“数据采集”作为一级导航，UI-004 继续在同一页面下扩展写操作。
- 浏览器只携带 XR-Trading 登录会话，调用同源 `GET /api/market-info/v1/providers/status`；Python 入口校验登录后，使用 `MARKET_INFO_SERVICE_URL` 与服务端环境变量 `MARKET_INFO_READ_BEARER_TOKEN` 转发到市场资讯服务。市场资讯 Bearer Token 不进入 HTML、JavaScript、Local Storage 或响应。
- 同源代理首期只开放这一条精确 GET 路由，不提供通配转发，也不转发写接口；上游结构化错误和 `X-Request-ID` 原样保留，网络故障映射为可重试且脱敏的 `MARKET_INFO_UNAVAILABLE`。
- 面板显示 Provider 总数、健康数、需要关注数、活跃/延迟订阅汇总，以及配置状态、健康状态、最近成功/失败、连续失败和检查时间；scope 继续展示市场、周期、session、新鲜度、延迟、订阅数和下次开市。
- 美股 `closed` 独立显示“休市”，`not_applicable` 显示“停止计算”，不伪装成 unhealthy；disabled、空 Provider 列表、Provider 无 scope 和非法/未知枚举均有安全退化展示。
- 首期进入页面时按需加载，之后由用户手工刷新，不自动轮询；请求期间按钮禁用。错误态提供重试，恢复后原地回到状态列表。
- 展示模板统一 HTML 转义，不公开 Provider 凭据、内部错误或数据库信息。纯格式化/汇总/模板函数由 Node 内置测试覆盖，并设置行、函数、分支均不低于 80% 的门禁。

UI-002 冻结以下实现契约：

- “数据采集”页使用“数据源状态 / 采集订阅”二级页签；订阅列表支持 Provider、Instrument code、interval、enabled 筛选及 cursor“加载更多”，不在浏览器内自行拼接数据库条件。
- 创建表单显式提交 Provider、Instrument code、interval、enabled、priority、close delay、可空 revision delay 和 reason；编辑只允许修改四个可变设置并要求 reason。留空 revision delay 显式发送 `null`，表示关闭 revision pass。
- 页面不提供 DELETE，也不把 `backfill_from` 混入订阅表单。创建或启用订阅只影响后续调度，不隐式发起历史回填，也不取消已运行 Task。
- XR-Trading BFF 对所有登录用户授予 `operations.read`；`subscriptions.manage` 只授予服务端 `XR_TRADING_SUBSCRIPTION_MANAGERS` 白名单中的用户。白名单以稳定用户 ID、用户名或邮箱匹配，比较时忽略大小写。
- 同源代理只开放订阅 collection 的精确 GET/POST 路由和 canonical UUID 的精确 PATCH 路由。GET 使用 `MARKET_INFO_READ_BEARER_TOKEN`，POST/PATCH 使用 `MARKET_INFO_MANAGE_BEARER_TOKEN`；无管理权限时在 BFF 直接返回 403，不向市场资讯服务发请求。
- 管理 UI 根据当前用户 permissions 显示创建和编辑入口；普通研究用户仍可筛选和查看订阅。账号退出时同步清空订阅数据及筛选/创建表单，避免跨账号残留。
- 客户端按稳定 `error.code` 本地化校验、重复订阅、来源无效、权限和服务不可用错误；模板统一 HTML 转义，上游 `X-Request-ID` 保留用于排障。

UI-003 冻结以下实现契约：

- “数据采集”页增加“任务运行”二级页签，内部使用 Runs/Tasks 双视图。首期按需加载并允许用户手工刷新当前视图，不自动轮询；刷新详情时同时刷新当前筛选下的列表。
- Run 列表支持 run type、trigger type、聚合状态、requested_by 和创建时间范围筛选；Task 列表支持 run ID、Task 状态、Provider、Instrument code、interval 和创建时间范围筛选。两者均显式发送 limit 并使用后端 cursor“加载更多”。
- 浏览器的 `datetime-local` 输入在发送前转换成 RFC3339 UTC；结束时间必须晚于开始时间。Task 的 run ID 在客户端先验证 canonical UUID，其他枚举和可读编码也先执行轻量校验，服务端仍是最终契约校验者。
- Run 卡片和详情展示按 Task 真相聚合的状态、六类 Task 计数、执行时间与公开 context；点击“查看该 Run 的 Task”切换到 Task 视图并自动设置精确 run ID 筛选，不在 Run 详情中无限内嵌 Task。
- Task 列表和详情展示可读 Provider/Instrument/ProviderInstrument 身份、范围、attempt、retry/lease、时间、取消信息及标准化错误。前端只读取 API 返回的 `error_summary/error_details`，不请求、推断或展示原始 `error_message`。
- XR-Trading BFF 只为 GET 开放 Run/Task collection 路由和 canonical UUID item 路由，使用 `MARKET_INFO_READ_BEARER_TOKEN`；不开放 `/backfill`、retry、cancel 或任意通配写代理，写操作留给 UI-004 的独立权限设计。
- 页面统一转义 Run key、context、身份、取消原因和错误字段，并保留上游 Request ID。账号退出时 Run/Task 列表、详情、筛选与错误状态一并清空。

UI-004 冻结以下实现契约：

- “任务运行”内部增加“手动回填”视图；一次只提交一个 Provider、Instrument code、`1h/1d`、`[start_time,end_time)` 与 reason，不提供批量参数或 `backfill_from`，结束时间不得晚于当前时间。
- Task 详情严格按状态展示操作：`failed` 可 retry，`pending/running/retry_wait` 可 cancel，`success/canceled` 等终态只显示不可操作说明。前端校验只用于及时反馈，服务端状态机仍是最终真相。
- backfill、retry、cancel 均先进入页面内二次确认；确认请求期间锁定触发按钮、表单和弹层关闭操作，状态 guard 同时拒绝重复提交。失败后弹层保留本地化错误，允许操作者核对后重试。
- backfill/retry 的 `202` 响应必须同时提供 run ID 和 task ID；页面自动切换到 Tasks，设置精确 run ID 筛选并打开新 Task。cancel 成功后刷新列表和原 Task 详情，不依赖浏览器乐观写状态。
- XR-Trading BFF 只开放 `POST /ingestion-runs/backfill` 与 canonical UUID 的 `/ingestion-tasks/{id}/retry|cancel`，并使用服务端管理 Token；任意其他写路径不匹配。调用前要求 `ingestion.manage`，无权限时直接返回 403。
- `XR_TRADING_INGESTION_MANAGERS` 是独立白名单；未设置时首期回退到 `XR_TRADING_SUBSCRIPTION_MANAGERS`，显式设置后两种管理权限分离。账号退出同步清空未提交表单、确认状态和错误。
- 页面按稳定错误码本地化 `BACKFILL_ALREADY_RUNNING`、`MANUAL_RETRY_ALREADY_RUNNING`、`TASK_STATE_CONFLICT`、`SUBSCRIPTION_NOT_FOUND`、`PERMISSION_DENIED` 与基础设施错误；所有摘要和 reason 在渲染前转义。

UI-005 冻结以下实现契约：

- 公共行情查询位于既有“资产研究”页面，不与采集管理混合。Asset 入口复用 XR-Trading 资产目录：优先读取 `metadata.market_info_asset_code`；首期旧种子数据按资产类型与 symbol 生成文档约定的可读编码作为兼容桥接。页面始终显示实际发送的 `asset_code`，未知映射由市场资讯服务以 `ASSET_NOT_FOUND` 明确拒绝。
- Asset 变化后以 `enabled=true&limit=100` 请求 `/instruments`。Instrument 选择第一项；Provider 优先 API 声明的 `is_default=true`，否则选择第一项；Interval 优先 `1h`，不支持时选择 Provider 返回的第一个周期。Provider 或 Instrument 变化时重新计算下游选项，不保留已经失效的旧选择。
- 最新行情只按明确 `asset_code` 请求 `/quotes/latest`，保留并逐卡展示所有来源的 Instrument、ProviderInstrument、价格、买卖价、24 小时高低、市场时间和质量状态，不在浏览器合并同一资产的不同来源。
- K 线请求始终显式发送当前 `instrument_code + provider + interval`，另支持可选本地时间到 UTC RFC3339 转换、`asc/desc`、1～1000 limit 和后端绑定游标续页。页面不实现或依赖隐式 Provider 默认值，只渲染当前 revision 的 API 结果。
- XR-Trading BFF 只为登录用户开放 `/instruments`、`/quotes/latest`、`/bars` 三条精确 GET 同源代理，并继续使用服务端只读凭据；其他公共查询路径或写方法不匹配。浏览器不接触 Bearer Token。
- 页面覆盖加载、空 Asset、无可用 Instrument/Provider、无行情、无 K 线、稳定错误码、Request ID、重复刷新保护、桌面与移动端展示；所有 API 文本统一转义。账号退出时清空联动、行情和游标状态。

## 3. 可观测性与运维

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| OPS-001 | DONE | ENG-005 | 已实现基于 `slog` 的 JSON 日志、HTTP 访问日志/恢复中间件及 request/run/task/provider/instrument 关联字段 | 字段合并、分组与预绑定属性脱敏、query/原始错误/panic 值不泄漏、正常 404=INFO、4xx=WARN、5xx/panic=ERROR 测试通过 |
| OPS-002 | DONE | QRY-004、ING-005 | 已实现 Prometheus `/metrics`：API、任务、Provider、延迟、积压、readiness 和 snapshot 状态 | 真实空库抓取成功；并发计数、直方图、持久事实聚合、失败降级测试通过；UUID、symbol、错误全文不作标签 |
| OPS-003 | DONE | ADM-005、OPS-002 | 已实现连续失败、数据延迟、任务积压和 ready 失败的首期 Prometheus 规则及固定时间判定模型 | 临界值/低于阈值测试通过；美股休市和 `not_applicable` 不输出延迟指标且不告警 |
| OPS-004 | DONE | DB-006、ENG-004 | 已实现非 root 多阶段 Dockerfile、PostgreSQL→migration→service Compose 依赖、healthcheck 和操作说明 | 独立空 volume 冷启动 health/ready=200；migration=5；重启数据保留；SIGTERM 退出码 0、无 OOM |
| OPS-005 | DONE | DB-005 | 已实现 custom archive+SHA-256 备份、只恢复至新库的恢复脚本、runtime 授权 migration 与角色说明 | 隔离空库恢复演练通过；migration=5、runtime 查询权限及指定演练资产可查询 |

OPS-001 冻结以下实现契约：

- 进程日志使用 Go 标准库 `slog` 的 JSON handler，生产最低级别首期为 INFO；日志上下文可逐层合并 `request_id`、`run_id`、`task_id`、`provider`、`instrument_id` 和 `instrument_code`，UUID 仍是数据库关联身份，可读 code 用于检索和排障。
- HTTP 中间件顺序固定为 Request ID 最外层、访问日志与 panic recovery 位于其内、业务路由位于最内层。正常响应、统一错误 envelope、panic 恢复和访问日志必须共享同一 Request ID。
- 每个 HTTP 请求只记录一条 `http request completed` 访问事件，字段包括 method、路由模板、status、duration 和响应字节数；不记录原始 URL、query、请求/响应 body、headers、Cookie 或 Authorization。未匹配路由使用固定 `unmatched`，避免任意 path 污染日志字段。
- 日志级别固定为：2xx/3xx 和正常 404 为 INFO，除 404 外的 4xx 为 WARN，5xx 与 recovered panic 为 ERROR。panic 只记录已恢复及响应是否已提交，不记录 panic value 或堆栈；未提交响应返回统一 `INTERNAL_ERROR` envelope。
- backfill、retry 和 cancel 成功后记录独立的业务事件，以同一 Request ID 串联新 Run/Task；只记录稳定 ID、Provider/Instrument code 和 interval，不记录 reason、Bearer token、Provider 原始错误或响应体。
- redacting handler 对预绑定属性、普通属性和嵌套 group 统一兜底过滤 token、secret、credential、password、signature、authorization、cookie、数据库连接串及原始 error/cause 字段。该兜底不能替代调用方约束：日志 message 必须是固定文本，不允许直接使用 `err.Error()` 或供应商消息。
- OPS-001 不引入集中日志基础设施，也不实现 Prometheus 指标；指标端点与低基数标签由 OPS-002 单独完成。

OPS-002 冻结以下实现契约：

- `GET /metrics` 使用 Prometheus text exposition 0.0.4，不要求行情管理 Bearer；部署时必须仅在内部网络或监控入口暴露。scrape 自身继续经过 Request ID、访问日志和 API metrics 中间件，本次请求在下一次 scrape 可见。
- API 指标为 `market_info_http_requests_total` 与 `market_info_http_request_duration_seconds`，标签只允许 method、ServeMux 路由模板和状态类；未知 method 收敛为 `OTHER`，未匹配路由收敛为 `unmatched`，不使用 path、query、状态全文或 Request ID。
- Task gauge 每次 scrape 从持久化 `ingestion_tasks` 单次聚合读取六类状态和最老 pending/retry_wait 创建时间；不依赖 Worker 进程内回调，不以 Run 缓存替代 Task 事实。
- Provider gauge 复用 ADM-005 持久事实投影，输出 bounded Provider code、health、连续失败、最后成功、scope 活跃/延迟订阅和适用数据延迟；`closed/not_applicable` scope 不输出 data delay sample。
- 每次 scrape 独立检查 readiness 并输出 `market_info_readiness_status`；持久事实查询失败时 `/metrics` 仍返回 200，输出 `market_info_operational_snapshot_success=0` 和累计失败次数，不输出原始错误或上一次 gauge 假数据。
- 指标标签禁止 UUID、asset/instrument symbol、provider request ID、游标、URL、用户身份和错误全文。Provider、market、session、interval、status 与 route 必须来自受控目录或枚举。

OPS-003 冻结以下实现契约：

- 首期规则文件为 `deploy/prometheus/market-info-alerts.yml`：ready 失败 2 分钟、Provider 连续失败达到 3 次并持续 5 分钟、数据延迟达到 3 个 interval 并持续 10 分钟、pending+retry_wait 达到 100 或最老积压达到 1 小时并持续 10 分钟。
- `1h` 延迟阈值为 10800 秒，`1d` 为 259200 秒。美股休市由指标层直接省略 delay sample，因此 PromQL 不需要自行维护交易日历，也不会在周末/休市/提前收市后误报。
- Go 固定时间判定模型与 Prometheus 瞬时表达式共享上述阈值，用于边界回归；Prometheus `for` 状态由监控系统维护，不写入业务数据库。

OPS-004/005 冻结以下实现契约：

- Docker 镜像分两阶段构建 `market-info` 和 `market-info-migrate`，运行层不包含编译器且使用非 root 用户；Compose 先等待 PostgreSQL healthy，再运行一次性 migration，成功后启动 service。
- migration 使用 `xr_market_data_owner`，service 使用 `xr_market_data_runtime`。新增向前 migration `00005_grant_runtime_permissions.sql` 固化 schema USAGE、现有/未来表 SELECT/INSERT/UPDATE，不授予 DELETE、DDL、core 写或角色管理权限。
- Compose healthcheck 使用 `/healthz`，应用 readiness 使用 `/readyz`；`stop_grace_period=15s` 大于默认应用 shutdown timeout。验证环境中 SIGTERM 后退出码为 0，PostgreSQL 重启后指定资产仍存在且 ready 恢复。
- 备份使用 PostgreSQL custom archive 并生成 SHA-256；恢复脚本拒绝覆盖源库或已有目标库，失败时只清理本轮新建目标。archive 不包含 cluster roles，空集群须先执行角色/core bootstrap。
- 恢复完成必须验证 migration 版本、runtime 读取 `core.assets`/`market_data.providers` 的权限，并可选校验指定 asset code。生产 archive 视为完整业务数据，必须外部加密、限制访问和设置保留期。

## 4. M4 退出门禁

- 管理员可完成订阅、单任务回填、重试、取消并追踪状态。
- 普通研究用户只能读取授权状态，不能执行管理写操作。
- Provider 状态不因美股休市降级，不在查询期间探测外部 Provider。
- 管理页面不展示 token、secret、堆栈或数据库信息。
- 日志和页面可通过 Request ID、Run ID、Task ID 串联一次操作；指标只使用受控低基数维度，不把这些 UUID 作为 label。
