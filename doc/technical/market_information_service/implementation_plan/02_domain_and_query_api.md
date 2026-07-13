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
| DOM-003 | TODO | DOM-001 | Quote、Bar、时间范围和来源语义 | OHLC 合法性、`[start,end)`、闭合状态、decimal 单测 |
| API-001 | READY | ENG-002 | 稳定业务错误、HTTP 映射、Request ID、时间/decimal JSON、游标组件 | 错误 envelope、未知字段、页大小上限、编码往返单测 |
| API-002 | TODO | API-001 | 鉴权与审计上下文接口；首期可用可替换实现 | 401/403、操作者传播、敏感字段不输出 |

DOM-001～003、API-001 可以并行，但共享 JSON 格式和错误码由整合负责人冻结。

DOM-002 冻结以下实体约束：

- `Asset`、`Instrument`、`ProviderInstrument` 分别使用 `asset.`、`instrument.`、`provider.` 代码前缀；Provider 使用不带固定前缀的稳定代码。
- `Instrument.asset_id` 是现货的 base asset；`quote_asset_id` 可以为空，但存在时必须非零且不能与 base asset 相同，`quote_currency` 始终必填。
- Instrument 精度范围为 0～18；`lot_size`、`min_quantity` 存在时必须为正数；市场时区必须是有效的 IANA 时区。
- `ProviderCapabilities` 采用结构化的 `quote`、`historical`、`intervals`，严格拒绝未知字段、非法 interval 和重复 interval。
- 默认 ProviderInstrument 必须启用且 `valid_to` 为空；同一 Instrument 只能有一个默认来源仍由 PostgreSQL 部分唯一索引保证。
- 三层实体分别校验自身不变量；`ValidateAssetInstrument` 与 `ValidateProviderMapping` 校验层间 ID 和类型关系，不把 Provider 误当成资产或交易品种。

## 2. 公共查询纵向切片

| ID | 状态 | 依赖 | 输出 | 核心测试与完成条件 |
| --- | --- | --- | --- | --- |
| QRY-001 | TODO | DOM-002、DB-010 | Instrument/Provider 选项 Query Service 与 `GET /instruments` | 联动列表只含启用有效来源；`is_default` 优先，否则 priority；interval 来自 capability |
| QRY-002 | TODO | DOM-003、DB-012 | LatestQuote Query Service 与 `GET /quotes/latest` | asset 查询返回多来源列表且不合并；精确过滤；空结果 200；非法组合 400 |
| QRY-003 | TODO | DOM-003、DB-012 | Bar Query Service 与 `GET /bars` | 强制 instrument/provider/interval；UTC 范围、稳定游标、升降序、decimal 字符串 |
| QRY-004 | TODO | QRY-001～003 | 路由装配、API 契约样例与数据库集成测试 | 查询只读且不创建任务；响应与 `07_api_and_admin_ui.md` 一致 |

Service 与 Handler 可先基于 fake Repository 实现，PostgreSQL Repository 完成后再做契约测试，避免让 API 工作被 DDL 串行阻塞。

## 3. M2 验收场景

1. BTC 同时存在 Bybit 与聚合来源时，最新行情返回两项，且保留各自 Instrument、ProviderInstrument 和 source。
2. K 线只给 `asset_code` 时拒绝；给 `instrument_code + provider + interval` 时成功。
3. 前端选项返回明确默认 Provider 和默认 `1h` 所需数据，客户端仍必须显式发送最终选择。
4. 所有时间为 UTC RFC3339，所有 decimal 以字符串输出。
5. 大范围查询受 page size 和游标限制，执行计划不出现非预期全表扫描。
6. Handler、Service、Repository 契约与集成测试通过，覆盖率不低于 80%。
