# 领域模型与公共查询 API 实施计划

## 0. 范围与冻结契约

本专题负责领域值对象、领域实体校验、只读查询用例和对应 HTTP API。它不实现 Provider 网络调用、Worker、Scheduler 或管理写接口；这些工作分别由后续专题承接。

依赖方向固定为：`api -> application -> domain <- repository/provider implementations`。领域包不得依赖 HTTP、pgx、SQL、配置包或具体 Provider SDK；application 负责用例编排和事务边界，不直接编写 SQL。

DOM-001 冻结以下公共值约束：

- `ID`：应用生成 UUIDv7，JSON 使用规范 UUID 字符串。
- `Code`：1～192 个 ASCII 字符，只允许小写字母、数字、点和连字符；点/连字符不得连续、不得位于首尾。Asset、Instrument、Provider、ProviderInstrument 分别按表结构限制为 128、160、64、192 个字符。
- `UTCInstant`：入口接受 RFC3339，内部规范化为 UTC，JSON 输出 RFC3339Nano 且使用 `Z`。
- `BarInterval`：第一阶段仅允许 `1h`、`1d`；日线不能简单等同固定 24 小时，市场日切由 Scheduler 处理。
- `AssetType`：`STOCK`、`ETF`、`CRYPTO`、`CASH`；`InstrumentType`：`EQUITY`、`ETF`、`SPOT`。
- `Decimal`：使用精确十进制，JSON 必须是字符串；不接受 JSON number，防止客户端先经过二进制浮点而损失精度。
- 对外 `asset_code` / `instrument_code` 使用数据库中的稳定小写点分 `code`；`canonical_symbol`、`symbol` 与 `external_symbol` 是独立展示/供应商字段，不得混用。

应用服务按查询纵向切片交付，每个切片包含 input/output DTO、领域校验、Repository 接口调用、业务错误与单元测试；HTTP Handler 只做协议转换。

## 1. 领域与公共组件

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| DOM-001 | DONE | DB-001 | 已实现 UUIDv7、Code、UTCInstant、BarInterval、AssetType、InstrumentType、Decimal 值对象 | 构造、格式、边界、严格 JSON 往返单测通过；领域包不依赖基础设施 |
| DOM-002 | DONE | DOM-001 | 已实现 Asset、Instrument、Provider、ProviderInstrument 领域类型、构造与关系校验 | 三层身份、现货 base/quote、精度、能力、有效期与默认来源约束单测通过；Repository 读写边界统一执行领域构造 |
| DOM-003 | DONE | DOM-001 | 已实现 Quote、Bar、QualityStatus、TimeRange 和 MarketDataSource 领域类型与构造校验 | OHLCV、`[start,end)`、闭合状态、精确 decimal、多来源身份及 Repository 边界单测通过 |
| API-001 | DONE | ENG-002 | 已实现稳定 Application 错误、HTTP 映射与安全 envelope、Request ID 中间件、严格 JSON、页大小和版本化 scope 游标；复用 UTC/decimal JSON 值对象 | 错误脱敏、未知字段/多 JSON/超限 body、页大小上限、游标跨 scope 拒绝及编码往返单测通过；服务入口已装配 Request ID |
| API-002 | DONE | API-001 | 已实现可替换 Authenticator、Principal/Permission、AuditContext、严格 Bearer 与权限中间件，以及只保存 token SHA-256 摘要的首期静态实现 | 缺失/无效凭证 401、权限不足 403、认证服务不可用 503、操作者与 Request ID 传播、Authorization/token 不进入 Handler/响应单测通过 |

DOM-001～003、API-001 可以并行，但共享 JSON 格式和错误码由整合负责人冻结。

DOM-002 冻结以下实体约束：

- `Asset`、`Instrument`、`ProviderInstrument` 分别使用 `asset.`、`instrument.`、`provider.` 代码前缀；Provider 使用不带固定前缀的稳定代码。
- `Instrument.asset_id` 是现货的 base asset；`quote_asset_id` 可以为空，但存在时必须非零且不能与 base asset 相同，`quote_currency` 始终必填。
- Instrument 精度范围为 0～18；`lot_size`、`min_quantity` 存在时必须为正数；市场时区必须是有效的 IANA 时区。
- `ProviderCapabilities` 采用结构化的 `quote`、`historical`、`intervals`，严格拒绝未知字段、非法 interval 和重复 interval。
- 默认 ProviderInstrument 必须启用且 `valid_to` 为空；同一 Instrument 只能有一个默认来源仍由 PostgreSQL 部分唯一索引保证。
- 三层实体分别校验自身不变量；`ValidateAssetInstrument` 与 `ValidateProviderMapping` 校验层间 ID 和类型关系，不把 Provider 误当成资产或交易品种。

DOM-003 冻结以下行情语义：

- `MarketDataSource` 必须同时包含 `instrument_id` 和 `provider_instrument_id`；后者是持久化来源身份，查询层通过 Provider 关联生成 `source` 展示值，不在行情表重复保存可能漂移的自由文本来源。
- Quote 与 Bar 使用 `UTCInstant`、`Decimal` 和 `QualityStatus`；decimal 对外仍严格编码为 JSON string。
- Quote 的价格、数量和成交量不得为负；同时存在 bid/ask 时 `bid <= ask`，24h open/high/low 必须保持合法区间。
- Bar 满足 `low <= open/close <= high`，价格和成交量不得为负；持久化版本 revision 必须大于零。
- `is_closed` 表示市场 K 线是否闭合，`is_current` 表示数据库修订版本是否为当前版本，两者语义独立；闭合 K 线的 `collected_at` 不得早于 `close_time`。
- `TimeRange` 支持单侧或双侧边界；起点包含、终点排除。K 线 Repository 使用 `open_time >= start AND open_time < end`，相邻窗口不会重复边界数据。

API-001 冻结以下协议约束：

- Application 错误码不依赖 HTTP；HTTP 层集中映射状态码并统一输出 `error` envelope。未知错误、非法应用错误码和不可序列化 details 均安全降级为 `500 INTERNAL_ERROR`。
- Request ID 格式为 `req_<canonical UUIDv7>`。合法客户端 ID 可以沿用；其他输入重新生成。服务入口统一装配，因此健康检查和业务 API 都返回 `X-Request-ID`。
- JSON 请求体首期默认最大 1 MiB，必须只包含一个 JSON 值并拒绝未知字段；字段类型错误使用 `INVALID_ARGUMENT`。
- 页大小由各端点提供安全默认值和最大值，`limit` 必须是 1～最大值之间的十进制整数。
- 游标使用 URL-safe Base64 编码的版本化 payload，并绑定 endpoint/query scope 与固定 value 数量；调用方不得解析或跨查询复用游标。
- UTC 和 Decimal 的 JSON 格式直接复用 DOM-001/003：时间输出 UTC RFC3339Nano，decimal 只使用字符串。

API-002 冻结以下安全上下文：

- `Authenticator` 只接收会自动脱敏显示的 BearerToken，返回经过校验的 Principal；JWT/OIDC、网关认证或其他机制通过替换该接口接入，Handler 不解析 token。
- Principal 只包含稳定 subject、`user/service` actor type 和显式 permissions。首期权限为 `operations.read`、`subscriptions.manage`、`ingestion.manage`，不使用含义模糊的全局 admin 布尔值。
- 公共行情查询及 `/healthz`、`/readyz` 不要求认证；采集状态和配置读取要求 `operations.read`；订阅写操作要求 `subscriptions.manage`；backfill/retry/cancel 要求 `ingestion.manage`。
- 缺失、格式错误或无效凭证统一返回 `401 UNAUTHENTICATED`；身份有效但权限不足返回 `403 PERMISSION_DENIED`；认证后端不可用返回可重试的 `503 SERVICE_UNAVAILABLE`。
- 认证成功后删除下游请求中的 `Authorization` header，并在 context 中传播 Principal 和只含 `requested_by`、actor type、Request ID 的 AuditContext。reason 仍由具体写请求显式提供。
- 首期 `StaticBearerAuthenticator` 适用于小规模部署，构造后只保留 token 的 SHA-256 摘要并使用定长比较；明文 token 不进入 Principal、审计上下文、错误响应或日志字段。后续生产认证实现不得改变 Handler/Application 契约。

## 2. 公共查询纵向切片

| ID | 状态 | 依赖 | 输出 | 核心测试与完成条件 |
| --- | --- | --- | --- | --- |
| QRY-001 | DONE | DOM-002、DB-010 | 已实现 Instrument/Provider 选项 Query Service、PostgreSQL 联表读模型与 `GET /instruments`，并完成运行时路由装配 | 联动列表只含启用有效来源；`is_default` 优先，否则 priority；interval 来自 capability；单元和真实 PostgreSQL 集成测试通过 |
| QRY-002 | DONE | DOM-003、DB-012 | 已实现 LatestQuote Query Service、只读 PostgreSQL 联表 projection、`GET /quotes/latest` 与运行时路由装配 | Asset/Instrument 查询返回多来源列表且不合并；精确 Provider 过滤；空结果 200；非法组合 400；单元和真实 PostgreSQL 集成测试通过 |
| QRY-003 | DONE | DOM-003、DB-012 | 已实现 Bar Query Service、来源解析与只读 PostgreSQL keyset 查询、`GET /bars` 和运行时路由装配 | 强制 instrument/provider/interval；UTC `[start,end)`；查询绑定游标；升降序；当前 revision；decimal 字符串；单元和真实 PostgreSQL 集成测试通过 |
| QRY-004 | TODO | QRY-001～003 | 路由装配、API 契约样例与数据库集成测试 | 查询只读且不创建任务；响应与 `07_api_and_admin_ui.md` 一致 |

Service 与 Handler 可先基于 fake Repository 实现，PostgreSQL Repository 完成后再做契约测试，避免让 API 工作被 DDL 串行阻塞。

QRY-001 冻结以下查询契约：

- 公共选项接口只返回当前可选数据：Asset 与 Instrument 必须为 `active`，Provider 为 `active` 或 `degraded`，ProviderInstrument 必须启用；`disabled` Provider 不进入下拉选项。
- Instrument 与 ProviderInstrument 均按请求时刻执行 `[valid_from, valid_to)` 判断，未来生效或已经失效的记录不会返回。
- `enabled` 省略时默认为 `true`，首期显式传 `false` 返回 `400 INVALID_ARGUMENT`；该公共接口不承担禁用配置的管理查询职责。
- 查询使用面向用例的扁平读模型和一次 CTE 联表查询。CTE 先按 Instrument code 分页，再关联全部 Provider，避免在分页边界拆开一个 Instrument 的 Provider 列表，也避免 N+1 查询。
- Instrument 按 code 升序；Provider 按 `is_default DESC, priority ASC, provider_code ASC, provider_instrument_code ASC` 排序。同一 Provider 存在多条有效映射时只展示排序最优的一条。
- `supported_intervals` 原样来自被选中 ProviderInstrument 的结构化 capabilities，不根据 Provider 类型猜测；展示名优先使用 Instrument `metadata.display_name`，否则使用 `symbol`。
- 默认页大小 50、上限 100。HTTP 游标绑定 `asset_code` 和最后一个 Instrument code，跨资产复用或篡改均返回 `400`；未知 Asset 返回 `404 ASSET_NOT_FOUND`，非 active Asset 返回空列表。
- endpoint 是公开只读接口，不经过 API-002 管理权限中间件；查询只读已落库配置，不会隐式创建采集任务。

QRY-002 冻结以下查询契约：

- `asset_code` 与 `instrument_code` 至少提供一个；两者同时提供时必须存在归属关系。Provider 只能作为附加过滤条件，未知 Provider 与非法组合返回 `400 INVALID_ARGUMENT`。
- 未知 Asset/Instrument 分别返回 `404 ASSET_NOT_FOUND` / `404 INSTRUMENT_NOT_FOUND`；资源存在但没有已落库行情、资源当前不可用或显式 Provider 为 `disabled` 时返回 `200` 和空 `quotes`。
- Service 先解析可读编码，再把 Asset、可选 Instrument、可选 Provider UUID 交给查询端口；Instrument-only 查询通过 `asset_id` 补全顶层 Asset，不由 Handler 拼接关系。
- PostgreSQL 使用一次只读联表查询补全 Instrument code、quote currency、Provider code、ProviderInstrument code 与外部 symbol；不会逐 Instrument 调用写侧 `ListLatestQuotes`，避免 N+1。
- 查询只包含 active 且当前有效的 Instrument、启用且当前有效并具有 quote capability 的 ProviderInstrument，以及 `active/degraded` Provider。有效期统一采用 `[valid_from, valid_to)`。
- 每条 `latest_quotes` 继续以 `instrument_id + provider_instrument_id` 隔离。同一 Provider 下多条 ProviderInstrument 也分别返回，不按 Provider code 去重；结果按 Instrument、Provider、ProviderInstrument code 稳定升序。
- 响应返回完整来源身份、精确 decimal 字符串、UTC 时间与 `quality_status`。最新快照即使为 `unchecked/warning/invalid` 也原样返回，不静默选择更旧记录。
- 最新行情集合受当前 Asset/Instrument 映射数量自然约束，且用户已明确多来源列表整体返回；首期不使用 limit/cursor。该公开只读 endpoint 不鉴权、不实时调用 Provider，也不创建采集任务。

QRY-003 冻结以下查询契约：

- `instrument_code`、`provider`、`interval` 全部必填，不接受 `asset_code` 替代。未知 Instrument 返回 `404 INSTRUMENT_NOT_FOUND`；未知 Provider、不可用来源组合和非法字段返回 `400 INVALID_ARGUMENT`。
- Service 按 `is_default DESC, priority ASC, provider_instrument_code ASC` 解析同一 Instrument/Provider 下唯一选中的当前有效映射，与 QRY-001 的 Provider 下拉选择一致。
- 选中映射必须启用、位于 `[valid_from, valid_to)`、Provider 为 `active/degraded` 且 capabilities 声明 `historical = true`。interval 不在该映射 capabilities 中返回 `400 UNSUPPORTED_INTERVAL`，不得切换到同 Provider 的另一条映射。
- `start_time` / `end_time` 为可选 UTC 范围，语义固定为 `[start,end)`；`start >= end` 返回 `400 INVALID_TIME_RANGE`。order 默认 `desc`，limit 默认 200、最大 1000。
- PostgreSQL 只查询 `is_current = true` 的 revision。升序游标条件为 `open_time > cursor`，降序为 `open_time < cursor`，并采用相同方向 ORDER BY；Service 读取 `limit + 1` 判断下一页。
- HTTP 游标绑定 Instrument、Provider、interval、order、规范化后的 start/end 以及上一页最后一个 open_time。跨范围、跨排序复用、篡改或范围外位置均返回 `400`。
- 响应保留 ProviderInstrument ID/code/symbol、Asset code、quote currency、revision、OHLCV、trade count、质量状态及采集时间；decimal 使用字符串，时间使用 UTC RFC3339Nano。
- Handler、Service 与 query repository 均为只读，不调用 Adapter、不创建采集任务；当前 revision 的 `unchecked/warning/invalid` 状态原样返回。

## 3. M2 验收场景

1. BTC 同时存在 Bybit 与聚合来源时，最新行情返回两项，且保留各自 Instrument、ProviderInstrument 和 source。
2. K 线只给 `asset_code` 时拒绝；给 `instrument_code + provider + interval` 时成功。
3. 前端选项返回明确默认 Provider 和默认 `1h` 所需数据，客户端仍必须显式发送最终选择。
4. 所有时间为 UTC RFC3339，所有 decimal 以字符串输出。
5. 大范围查询受 page size 和游标限制，执行计划不出现非预期全表扫描。
6. Handler、Service、Repository 契约与集成测试通过，覆盖率不低于 80%。
