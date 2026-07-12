# 领域模型与公共查询 API 实施计划

## 1. 领域与公共组件

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| DOM-001 | TODO | DB-001 | UUIDv7、Code、UTC 时间、Interval、MarketType、Decimal 值对象 | 构造、格式、边界、JSON 往返；领域包不依赖基础设施 |
| DOM-002 | TODO | DOM-001 | Asset、Instrument、Provider、ProviderInstrument 领域类型与校验 | 三层身份、现货 base/quote、默认来源约束单测 |
| DOM-003 | TODO | DOM-001 | Quote、Bar、时间范围和来源语义 | OHLC 合法性、`[start,end)`、闭合状态、decimal 单测 |
| API-001 | READY | ENG-002 | 稳定业务错误、HTTP 映射、Request ID、时间/decimal JSON、游标组件 | 错误 envelope、未知字段、页大小上限、编码往返单测 |
| API-002 | TODO | API-001 | 鉴权与审计上下文接口；首期可用可替换实现 | 401/403、操作者传播、敏感字段不输出 |

DOM-001～003、API-001 可以并行，但共享 JSON 格式和错误码由整合负责人冻结。

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

