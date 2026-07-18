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
| UI-001 | TODO | ADM-005 契约 | Provider 状态面板 | healthy/degraded/unhealthy/unknown/closed 展示与空态测试 |
| UI-002 | TODO | ADM-001 契约 | 订阅查询、创建、启停页面 | 表单校验、冲突错误、本地化 error.code、无 delete/backfill_from |
| UI-003 | TODO | ADM-003 契约 | Run/Task 列表与详情 | 筛选、分页、状态刷新、脱敏错误展示 |
| UI-004 | TODO | ADM-002/004 契约 | backfill、retry、cancel 操作 | 二次确认、重复点击防护、202 后跳转追踪任务 |
| UI-005 | TODO | QRY-001～003 契约 | Asset→Instrument→Provider→Interval 联动 | 必须展示默认值并显式发送；bars 不依赖后端隐式默认来源 |

前端可以在 API 契约冻结后使用 mock 并行开发；统一 API client 与错误映射文件由唯一负责人维护。

## 3. 可观测性与运维

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| OPS-001 | TODO | ENG-005 | JSON 日志及 request/run/task/provider/instrument 关联字段 | 日志脱敏和字段传播测试；正常 404 不记 ERROR |
| OPS-002 | TODO | QRY-004、ING-005 | Prometheus `/metrics`：API、任务、Provider、延迟与积压 | 指标抓取测试；UUID、symbol、错误全文不得作标签 |
| OPS-003 | TODO | ADM-005、OPS-002 | 首期告警建议：连续失败、数据延迟、任务积压、ready 失败 | 使用固定时间验证阈值；休市不误报 |
| OPS-004 | TODO | DB-006、ENG-004 | Dockerfile、Compose 服务、操作说明 | 冷启动 health=200；迁移后 ready=200；重启保留数据；优雅关闭 |
| OPS-005 | TODO | DB-005 | 最小备份恢复与数据库角色说明 | 空环境恢复演练可查询 seed 数据 |

## 4. M4 退出门禁

- 管理员可完成订阅、单任务回填、重试、取消并追踪状态。
- 普通研究用户只能读取授权状态，不能执行管理写操作。
- Provider 状态不因美股休市降级，不在查询期间探测外部 Provider。
- 管理页面不展示 token、secret、堆栈或数据库信息。
- 日志、指标和页面可通过 Request ID、Run ID、Task ID 串联一次操作。
