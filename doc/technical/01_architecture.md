# XR-Trading 系统架构技术文档

## 1. 架构目标

XR-Trading 以投资组合为核心，统一支持美股、ETF、数字加密货币和现金。系统形成“资产数据 → 投资研究 → 策略 → 组合构建 → 风控 → 执行 → 估值与复盘”的闭环。

架构首先服务个人量化研究和模拟验证，同时为经过审批的小资金实盘保留严格隔离的执行能力。

## 2. 核心领域边界

| 领域 | 核心对象 | 职责 |
| --- | --- | --- |
| 用户与权限 | User、Session | 身份、数据隔离、审批权限 |
| 资产 | Asset、Venue、ProviderSymbol | 跨市场资产主数据和代码映射 |
| 投资组合 | Portfolio、PortfolioMember | 目标、基准、成员、状态和执行模式 |
| 账户与记账 | Account、CashBalance、Position | 账户绑定、现金、持仓和对账 |
| 研究 | MarketData、AnalysisResult | 数据标准化和按资产类别分析 |
| 策略 | Strategy、StrategyAssignment、Signal | 版本化规则、评分和建议 |
| 组合构建 | TargetAllocation、RebalancePlan | 风险预算、目标权重和调仓计划 |
| 风控 | RiskPolicy、RiskCheck | 组合级与订单级强制约束 |
| 执行 | Order、Fill、Adapter | 可靠下单、状态同步和幂等 |
| 估值与报告 | ValuationSnapshot、PerformanceSnapshot | 净值、绩效、归因和报告 |

依赖方向遵循：策略可以读取研究与组合状态；执行只接受通过风控的交易意图；交易通道不得反向承载策略逻辑。

## 3. 推荐代码结构

```text
xr-trading/
  backend/
    api/
    domain/
      assets/
      portfolios/
      accounts/
      strategies/
      risk/
      execution/
      valuation/
    adapters/
      market_data/
      brokers/
      exchanges/
    app.py
  frontend/
  research/
    analysis/
    strategy_lab/
    backtest/
    reports/
  config/
    assets.yaml
    portfolios.yaml
    strategies.yaml
    risk.yaml
    providers.yaml
    env.yaml
  data/
    market/
    fundamentals/
    news/
    onchain/
    accounts/
    signals/
    orders/
    fills/
    valuations/
  doc/
  reports/
```

MVP 可以保持单体部署，但代码必须按领域边界组织。只有在运行压力或团队规模需要时才拆分服务。

## 4. 逻辑架构

```text
Web UI / API
      │
      ├── Identity & Access
      ├── Asset Catalog
      └── Portfolio Service
                  │
Market Data ──> Research & Analysis
                  │
              Strategy Service
                  │
          Portfolio Construction
                  │
               Risk Service
                  │
            Execution Service ──> Broker / Exchange Adapters
                  │
       Orders / Fills / Positions
                  │
        Valuation / Performance / Reports
```

## 5. 跨市场抽象

### 5.1 统一资产标识

所有记录使用内部 `asset_id`，不以 `symbol` 作为唯一键：

```json
{
  "asset_id": "crypto:coinbase:BTC-USD",
  "asset_type": "CRYPTO",
  "symbol": "BTC",
  "venue": "COINBASE",
  "quote_currency": "USD",
  "provider_symbols": {
    "market_data_provider": "BTC-USD"
  }
}
```

### 5.2 统一组合记账

- 所有持仓归属于 `portfolio_id + account_id + asset_id`。
- 金额同时记录原币值、汇率和基础货币折算值。
- 股票公司行动、加密小数精度、费用和最小交易量由市场规则组件处理。
- 美股按交易日历运行，加密市场按 24/7 时间轴运行；组合估值使用统一日切规则。

### 5.3 通道适配器

统一接口定义账户、持仓、订单和成交能力，具体适配器声明自己的市场与能力。美股 MVP 使用长桥模拟盘；加密交易适配器在供应商确定后接入。

## 6. 核心数据流

1. 调度器加载环境、组合、策略、风险和数据源配置。
2. 资产服务解析组合成员和数据源代码映射。
3. 数据适配器按市场日历采集行情、新闻、基本面或加密数据。
4. 分析模块按资产类型生成标准化研究结果。
5. 策略服务输出资产评分和建议目标权重。
6. 组合构建模块生成 `TargetAllocation` 与 `RebalancePlan`。
7. 风控服务检查组合、资产类别、订单和环境约束。
8. 执行服务将通过审批的交易意图路由到对应适配器。
9. 订单与成交同步后更新持仓、现金、组合估值和绩效。
10. 报告服务生成组合日报、风险报告和审计记录。

## 7. 一致性与审计

所有可追踪记录至少包含：

```json
{
  "run_id": "2026-06-29-daily-001",
  "portfolio_id": "pf_growth_us_crypto",
  "asset_id": "equity:nasdaq:NVDA",
  "environment": "paper",
  "strategy_version": "growth_v0.2.0",
  "config_version": "sha256:4c9a...",
  "created_at": "2026-06-29T08:00:00+08:00"
}
```

订单、成交、持仓、现金和估值必须支持定期对账。外部订单状态未知时进入安全状态，不得盲目重试。

## 8. 技术选型原则

- Python 优先用于研究、分析、回测和报告。
- Go 在需要长期运行、并发同步和强类型接口时用于交易与风控服务。
- C++ 仅在性能数据证明存在瓶颈时引入。
- SQLite 可用于个人单机 MVP；并发写入、长周期时序数据或多服务部署后评估 PostgreSQL 与 Parquet。

## 9. MVP 验收标准

- 品牌统一为“XR-Trading 量化投资研究平台”。
- 用户可创建多个投资组合并管理跨市场组合成员。
- 资产目录支持 `STOCK`、`ETF`、`CRYPTO` 和 `CASH`。
- 可生成持仓、现金、目标权重和组合估值快照。
- 可采集美股和加密历史行情并生成基础研究结果。
- 可运行组合级回测、风控和再平衡预览。
- 可连接长桥模拟盘并追踪完整订单生命周期。
- 每个订单可追溯到组合、策略、风控和审批记录。
- 可生成组合净值、回撤、配置和基准对比报告。
