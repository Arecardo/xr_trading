# 市场资讯服务文档集

> 文档状态：设计草案持续完善  
> 原始入口：[市场资讯服务设计文档](../10_market_information_service.md)、[市场资讯服务数据库详细设计](../11_market_information_database.md)  
> 最近更新：2026-07-05

本目录集中存放 `market-info-service` 的设计文档。服务第一阶段负责美股/ETF 与加密货币现货行情的采集、标准化、存储、查询和采集任务管理。

## 阅读顺序

1. [服务总览](./01_overview.md)
2. [领域模型](./02_domain_model.md)
3. [数据库设计](./03_database.md)
4. [Provider 适配器](./04_provider_adapter.md)
5. [采集流程](./05_ingestion_flow.md)
6. [任务生命周期](./06_task_lifecycle.md)
7. [API 与前端管理页面](./07_api_and_admin_ui.md)
8. [运维、安全与可观测性](./08_operations.md)
9. [Go 开发规范](./09_go_development_standards.md)
10. [数据库技术决策](./10_database_technology_decisions.md)
11. [实施计划](./implementation_plan/README.md)

## 当前已确认重点

- 第一阶段只做行情，不包含账户、订单、成交、资金流水。
- 美股/ETF 使用长桥 API，加密货币使用 Bybit OpenAPI，且加密市场只覆盖现货。
- 领域模型采用 `Asset + Instrument + ProviderInstrument`。
- 数据库关系使用 UUIDv7 主键，同时保留可读 `code`。
- 行情数据必须保留 `source`、`instrument_id` 和 `provider_instrument_id`，不同来源价格不能互相覆盖。
- Worker 可以复用底层 adapter，但必须通过 `MarketDataAdapter` 接口和 `AdapterRegistry`。
- 采集任务状态机、重试、租约和取消规则已纳入设计。
- Go 实现要求新增函数具备单元测试，项目 statement coverage 不低于 80%。
