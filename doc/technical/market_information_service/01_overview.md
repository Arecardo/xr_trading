# 市场资讯服务总览

> 来源：拆分自 `../10_market_information_service.md`。

> 文档状态：架构草案  
> 当前阶段：已拆分专题文档，详细设计持续补充  
> 最近更新：2026-07-19

## 1. 建设目标

市场资讯服务（`market-info-service`）使用 Go 实现，负责持续采集、标准化、存储和提供 XR-Trading 所需的市场行情数据。

第一阶段覆盖：

- 通过长桥 API 获取美股与 ETF 行情。
- 通过 Bybit OpenAPI 获取加密货币现货行情。
- 获取最新价格、小时 K 线和日 K 线。
- 支持历史回填、定时增量采集、断点续采和数据质量检查。
- 向研究、回测、策略和组合估值模块提供统一的行情查询能力。

“市场资讯”是面向后续扩展的服务名称。第一阶段仅实现行情，未来可按实际需求增加公司基本面、新闻事件、宏观数据和加密市场指标。

## 2. 已确认的设计决策

| 事项 | 当前决策 |
| --- | --- |
| 开发语言 | Go |
| 美股与 ETF 数据源 | 长桥 API |
| 加密数据源 | Bybit OpenAPI |
| 加密市场范围 | 仅现货，不采集永续合约、交割合约和期权 |
| 第一阶段粒度 | 最新价格、`1h`、`1d` |
| 服务职责 | 仅市场行情，不包含账户与交易数据 |
| 资产领域模型 | 统一采用 `Asset + Instrument + ProviderInstrument` 三层模型 |
| 实体标识 | UUIDv7 主键 + 唯一可读 `code`，数据库关系只使用 UUID |
| Instrument 现货模型 | 使用 `asset_id` 表示基础资产，不重复保存 `base_asset_id` |
| 别名模型 | 使用 `asset_aliases` 与 `instrument_aliases` 分表保证外键完整性 |
| K 线主键 | 使用业务复合主键，不为每条 K 线生成 UUID |
| 主存储 | PostgreSQL |
| 部署方式 | 初期使用 Docker Compose，保持单一部署单元和内部模块化 |
| 基础设施策略 | 暂不引入 Redis、Kafka、Nacos 和业务自管 etcd |

## 3. 服务边界

### 3.1 服务负责

- 管理行情采集所需的供应商交易品种映射。
- 根据资产范围生成采集计划。
- 调用长桥和 Bybit 行情接口。
- 标准化交易品种、时间、时区、周期、价格、成交量和数据来源。
- 幂等写入最新价格与 K 线。
- 记录采集运行、重试、断点和数据质量问题。
- 提供面向内部模块的行情查询 API。
- 输出健康状态、采集状态、日志和监控指标。

### 3.2 服务不负责

- 账户余额、可用资金和资产托管信息。
- 持仓、订单、成交、费用和资金流水。
- 下单、撤单和交易通道状态管理。
- 技术指标、资产评分、策略信号和目标权重计算。
- 组合风控和交易风控。

账户和交易相关数据由独立的交易服务维护。即使行情服务与交易服务使用相同供应商，也必须使用不同的适配器接口、权限范围和凭据配置。

## 4. 逻辑架构

```text
                        ┌─────────────────────┐
                        │  资产目录 / 组合范围  │
                        └──────────┬──────────┘
                                   │
                                   ▼
┌──────────────┐          ┌─────────────────────┐          ┌──────────────┐
│ 长桥行情 API  │ ───────> │ market-info-service │ <─────── │ Bybit OpenAPI│
└──────────────┘          │                     │          └──────────────┘
                          │  Scheduler          │
                          │  Ingestion          │
                          │  Normalization      │
                          │  Quality            │
                          │  Query API          │
                          └──────────┬──────────┘
                                     │
                                     ▼
                            ┌─────────────────┐
                            │   PostgreSQL    │
                            └────────┬────────┘
                                     │
             ┌───────────────────────┼───────────────────────┐
             ▼                       ▼                       ▼
       研究与分析模块             回测模块              组合估值模块
```

## 5. 推荐代码结构

第一阶段采用一个代码库、一个部署单元，按内部模块划分职责：

```text
market-info-service/
  cmd/
    market-info/
  internal/
    api/
    scheduler/
    ingestion/
    providers/
      longbridge/
      bybit/
    normalization/
    quality/
    repository/
    domain/
    config/
    observability/
  migrations/
  config/
```

建议预留三种启动模式：

- `serve`：只运行查询 API。
- `worker`：只运行调度和采集任务。
- `all`：同时运行 API 与采集任务，作为 MVP 默认模式。

代码结构预留拆分能力，但当前不拆成多个微服务。

## 7. 数据范围与采集计划

### 7.1 第一阶段数据范围

| 市场 | 数据源 | 市场类型 | 数据内容 | 周期 |
| --- | --- | --- | --- | --- |
| 美股/ETF | 长桥 | 证券现货 | 最新价格、OHLCV | `1h`、`1d` |
| 加密货币 | Bybit | Spot | 最新价格、OHLCV | `1h`、`1d` |

采集资产范围建议来自以下集合的并集：

- 活跃投资组合成员。
- 当前持仓资产。
- 组合基准。
- 风险和市场环境参考资产。
- 显式配置的长期观察资产。

### 7.2 调度原则

- 美股小时线仅在相应交易日和交易时段采集。
- 美股日线在正式收盘后延迟采集，并安排后续修订检查。
- 加密小时线按 24/7 时间轴，在 K 线闭合后采集。
- 加密日线初步建议以 UTC 00:00 为日切边界。
- 调度时间应包含适当延迟，避免采集尚未闭合的 K 线。
- 服务重启后根据 checkpoint 发现并补齐缺失区间。
- 历史回填与日常增量是不同任务类型，但复用标准化、质量检查和存储流程。

具体交易日历来源、延迟时间、修订窗口、失败退避和 API 限流策略留待详细设计。

## 10. 存储与数据所有权

第一阶段使用 PostgreSQL。少量和中等规模行情直接存储在 PostgreSQL；当历史数据量和回测吞吐确实需要时，再引入 Parquet 作为历史归档或数据快照。

可以共用一个 PostgreSQL 实例，但建议按领域隔离 schema 和数据库用户：

```text
PostgreSQL
  core          用户、组合、Asset 和 Instrument 主数据
  market_data   行情、采集任务和质量记录
  trading       未来的账户、订单、成交和资金流水
```

各服务只写自己拥有的 schema。跨服务读取优先通过 API；MVP 若需要数据库只读访问，必须显式限制权限并记录为待消除的耦合。

已确认的核心主数据表：

- `assets`
- `instruments`
- `asset_aliases`
- `instrument_aliases`

`assets` 已存在于当前核心模型，后续需按三层模型调整其字段和现有数据。

已确认的市场资讯服务表：

- `providers`
- `provider_instruments`
- `collection_subscriptions`
- `market_bars`
- `latest_quotes`
- `ingestion_runs`
- `ingestion_tasks`
- `ingestion_checkpoints`
- `data_quality_issues`

完整字段、约束、索引、任务流程和权限设计见 [数据库设计](./03_database.md)。第一阶段不对 `market_bars` 分区，达到数千万行或维护成本明显上升时再按时间评估分区。

## 14. 后续详细设计占位

以下内容将在后续讨论后逐项补充：

### 14.1 领域模型

已确认并补充 `Asset`、`Instrument` 和 `ProviderInstrument` 三层模型，见 [领域模型](./02_domain_model.md)。

待补充：`Bar`、`Quote`、`ReferenceSeries`（后续聚合行情需要时）和采集任务模型。

### 14.2 数据库设计

已补充独立的 [数据库设计](./03_database.md)，包括核心主数据、供应商映射、行情、采集任务、质量问题、索引、权限和分区策略。

待补充：现有 SQLite 数据迁移脚本、容量实测结果和生产数据保留期限。

### 14.3 Provider 接口

已补充 Provider 适配器接口、能力声明、统一数据格式、错误分类和注册表设计，见 [Provider 适配器](./04_provider_adapter.md)。

Bybit Spot 的实现约束已随 ADP-003 补充到 [Provider 适配器](./04_provider_adapter.md#811-bybit-spot-adapter-第一阶段实现)；Longbridge 美股/ETF 的 SDK 隔离、symbol、常规时段、分页、错误码和凭据约束已随 ADP-004 补充到 [Provider 适配器](./04_provider_adapter.md#812-longbridge-美股etf-adapter-第一阶段实现)。SCH-002 已让 Longbridge、Scheduler 与 freshness 复用同一美股交易日历，统一 DST、完整休市和提前收市的 K 线关闭时间。

Worker 原子认领循环、固定租约、进程内并发限制、空队列轮询和协作式停止已随 ING-001 落地；K 线 Provider 分页、结构质量校验、稳定修订 hash、checkpoint 单调推进以及带 fencing token 的最终原子事务已随 ING-002 落地；稳定错误分类、分段退避、最大尝试次数和带 fencing 的失败事务已随 ING-003 落地；同任务行锁上的协作式取消、过期租约原子恢复、恢复重试上限和旧 Worker 零污染已随 ING-004 落地；以 Task 为事实、带并发快照校验的 Run Service 汇总已随 ING-005 落地；显式单范围请求、并发防重和 Task 内 Provider 分页的 backfill 已随 ING-006 落地；Bybit 7×24 时间窗口已随 SCH-001 落地；美股官方交易日历、DST/提前收市和休市不累计 freshness 已随 SCH-002 落地；启用订阅分页扫描、close/revision 最新窗口、稳定 run_key 和跨实例 Run/Task 原子创建已随 SCH-003 落地；checkpoint 验证、行情事实缺口合并、单轮追赶上限和周期租约恢复已随 SCH-004 落地；带权限隔离、可读身份、capability 校验、不可变身份和持久审计的订阅管理 API 已随 ADM-001 落地；需要 `ingestion.manage`、从认证上下文写入审计信息且返回 `202` 的单范围 backfill API 已随 ADM-002 落地；基于 Task 真相汇总 Run 状态、联表返回可读来源身份并在 Service 层脱敏错误的 Run/Task 列表与详情 API 已随 ADM-003 落地；保留原失败任务、创建独立 repair/manual Run/Task 的手工重试，以及同 Task 行锁上的协作式取消和持久审计已随 ADM-004 落地；只读取持久化采集事实、区分 configured/health、复用连续窗口与美股日历的 Provider 状态 API 已随 ADM-005 落地；复用现有 Web 页面、通过服务端只读同源代理隐藏市场资讯凭据，并覆盖状态、休市、空态和错误重试的 Provider 状态面板已随 UI-001 落地；带精确读写代理、白名单权限、筛选/游标、创建/修改、本地化错误和只读视图的订阅管理页已随 UI-002 落地；Run/Task 双视图、Task 真相状态、筛选/游标、详情跳转、手工刷新和脱敏错误展示已随 UI-003 落地；独立 `ingestion.manage` 权限、单范围 backfill、失败 Task retry、活动 Task cancel、二次确认、重复提交保护和创建后自动追踪已随 UI-004 落地；资产研究页的多来源最新行情、Asset→Instrument→Provider→Interval 明确默认值和显式来源 K 线查询已随 UI-005 落地。具体边界见 [采集流程](./05_ingestion_flow.md#76-执行线流程)、[任务生命周期](./06_task_lifecycle.md#82-租约概念) 与 [API 设计](./07_api_and_admin_ui.md#3-provider-状态-api)。下一项进入 OPS-001 结构化日志与关联字段。

### 14.4 调度与补数算法

已在 [采集流程](./05_ingestion_flow.md)、[任务生命周期](./06_task_lifecycle.md) 和 [API 与前端管理页面](./07_api_and_admin_ui.md) 中补充采集数据流、服务组件层次、Scheduler 与 Worker 职责、Worker 通过 `MarketDataAdapter` 复用底层适配器、checkpoint 与缺口检测原则、任务生命周期状态机、重试规则、租约概念、Run 汇总规则，以及前端采集任务管理页面的首期能力。

待补充：长任务租约续期和修订流程；交易日历需按官方公告逐年扩展支持范围。

### 14.5 查询 API

待补充：最新价格、K 线、数据状态、采集任务管理、手动回填、健康检查 API 及错误码。

### 14.6 配置设计

待补充：环境配置、供应商配置、采集范围、限流参数和凭据注入方式。

### 14.7 数据质量与告警

待补充：质量规则等级、阻断条件、告警渠道、恢复通知和人工修复流程。

### 14.8 测试方案

待补充：适配器契约测试、时间边界测试、幂等测试、故障恢复测试和供应商沙箱测试。

### 14.9 部署与运维

待补充：Compose 文件、数据库备份恢复、升级回滚、容量预估和生产部署方式。

## 15. 待讨论事项

- UUIDv7 的具体 Go 实现库、生成边界和异常处理方式。
- 可读 `code` 的字符集、最大长度和受控变更流程。
- 现有场所相关资产 ID 向三层模型迁移的映射方式。
- 首批美股、ETF 和 Bybit 现货交易品种清单。
- 最新行情采用定时轮询还是 WebSocket，及其与 K 线采集的关系。
- 美股盘前、盘后行情是否纳入小时线。
- 数据修订历史保留方式和期限。
- 市场资讯服务与现有业务后端之间的资产范围同步方式。
- 长时间历史回填任务是否需要 Worker 心跳续租。
