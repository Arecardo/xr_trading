# 资产目录、投资组合与数据采集技术文档

## 1. 模块目标

本模块维护跨市场资产主数据、投资组合成员关系和研究数据。原“股票池”仅代表候选标的集合，不能承载现金、持仓、目标权重、策略、风险预算和绩效，因此由 `Portfolio` 与 `PortfolioMember` 替代。

## 2. 资产主数据

`config/assets.yaml` 示例：

```yaml
assets:
  - asset_id: equity:nasdaq:NVDA
    asset_type: STOCK
    symbol: NVDA
    name: NVIDIA
    venue: NASDAQ
    quote_currency: USD
    trading_status: tradable
    provider_symbols:
      longbridge: NVDA.US
  - asset_id: crypto:coinbase:BTC-USD
    asset_type: CRYPTO
    symbol: BTC
    name: Bitcoin
    venue: COINBASE
    quote_currency: USD
    trading_status: watch
    provider_symbols:
      market_data: BTC-USD
  - asset_id: cash:USD
    asset_type: CASH
    symbol: USD
    name: US Dollar
    quote_currency: USD
    trading_status: tradable
```

资产唯一性使用 `asset_id`。同名代码、不同交易所或不同报价币必须是不同资产记录。

## 3. 投资组合配置

`config/portfolios.yaml` 示例：

```yaml
portfolios:
  - portfolio_id: pf_growth_us_crypto
    name: 美股与加密成长组合
    base_currency: USD
    benchmark: benchmark:custom:70QQQ_30BTC
    status: active
    execution_mode: paper
    allowed_asset_types: [STOCK, ETF, CRYPTO, CASH]
    members:
      - asset_id: equity:nasdaq:NVDA
        member_status: approved
        target_weight_min: 0.00
        target_weight_max: 0.20
        added_reason: AI 基础设施核心资产
      - asset_id: crypto:coinbase:BTC-USD
        member_status: candidate
        target_weight_min: 0.00
        target_weight_max: 0.15
        added_reason: 加密资产配置候选
```

### 3.1 组合状态

| status | 含义 | 是否允许生成新交易 |
| --- | --- | --- |
| `draft` | 配置中 | 否 |
| `active` | 正常运行 | 是，仍需风控 |
| `paused` | 暂停 | 仅允许减仓或退出 |
| `archived` | 已归档 | 否 |

### 3.2 成员状态

| member_status | 含义 |
| --- | --- |
| `candidate` | 研究候选，不允许自动买入 |
| `approved` | 允许策略配置 |
| `held` | 当前持有，可与 approved 同步派生 |
| `restricted` | 禁止新增，只允许减仓或退出 |

风控必须同时检查资产 `trading_status`、组合状态和成员状态。

## 4. 数据类型与来源

| 数据 | 美股/ETF | 加密资产 | MVP |
| --- | --- | --- | --- |
| 历史与最新行情 | 长桥或外部数据源 | 加密行情数据源 | 必须 |
| 账户、持仓、订单、成交 | 长桥 | 待选交易所适配器 | 美股必须 |
| 公司基本面 | 外部数据源 | 不适用 | 可低频更新 |
| 新闻与事件 | 通用或证券数据源 | 通用或加密数据源 | 基础接口 |
| 宏观与基准 | SPY、QQQ、利率、波动率 | BTC、稳定币与加密总市值 | 必须 |
| 链上与供应数据 | 不适用 | 待选链上数据源 | 后续 |

数据适配器必须声明能力、市场覆盖、时间粒度和授权范围，业务层不得假设单一供应商覆盖全部数据。

## 5. 标准化行情结构

```json
{
  "asset_id": "crypto:coinbase:BTC-USD",
  "interval": "1d",
  "market_time": "2026-06-28T00:00:00Z",
  "open": 100000.0,
  "high": 103000.0,
  "low": 99000.0,
  "close": 102000.0,
  "volume": 12000.5,
  "quote_currency": "USD",
  "source": "market_data_provider",
  "quality_status": "valid",
  "collected_at": "2026-06-29T08:00:00+08:00"
}
```

股票复权方式、加密成交量单位和供应商时区必须作为元数据保存。

## 6. 本地数据目录

```text
data/
  catalog/
  market/
    equities/
    crypto/
    fx/
  fundamentals/
  news/
  onchain/
  accounts/
  positions/
  orders/
  fills/
  valuations/
  quality/
```

MVP 结构化业务数据可使用 SQLite，批量历史行情优先使用 Parquet；不得将文件路径作为领域对象之间的唯一关联方式。

## 7. 数据采集流程

1. 读取活跃组合及成员。
2. 合并组合资产、持仓资产、基准和风险参考资产。
3. 根据 `asset_id` 和供应商映射生成采集计划。
4. 按美股交易日历或加密 24/7 时间轴采集数据。
5. 标准化时区、精度、币种和公司行动。
6. 执行质量检查并幂等写入。
7. 同步账户、持仓、订单、成交和现金。
8. 输出数据版本与采集状态，供研究和估值使用。

## 8. 数据质量规则

- K 线键使用 `asset_id + interval + market_time + source`。
- OHLC 满足 `low <= open/close <= high`，成交量非负。
- 明确区分“市场休市”“供应商缺失”和“尚未采集”。
- 时间统一存储为 UTC，并保留市场时区信息。
- 数量、价格、费用和汇率使用定点十进制，禁止核心记账使用二进制浮点。
- 股票拆股分红和代码变更必须可追溯。
- 加密资产必须记录交易场所、交易对、报价币和精度。
- 数据修订不得覆盖历史而不留版本或审计记录。

## 9. 对外输出

```json
{
  "run_id": "2026-06-29-daily-001",
  "portfolio_ids": ["pf_growth_us_crypto"],
  "asset_ids": [
    "equity:nasdaq:NVDA",
    "crypto:coinbase:BTC-USD"
  ],
  "dataset_version": "market_20260629_001",
  "account_snapshot_id": "acct_snap_20260629_001",
  "quality_status": "passed",
  "status": "success"
}
```

## 10. 测试点

- 活跃组合没有任何成员时，应提示配置错误但不影响其他组合。
- `candidate` 可采集和分析，但不能自动生成买入订单。
- 相同 symbol、不同 venue 的资产不会错误合并。
- 重复采集同一数据不会产生重复记录。
- 美股休市不会被误报为数据缺失，加密数据中断会触发告警。
- 汇率缺失时不得生成伪精确的组合净值。
- 资产、账户和供应商代码映射失败时必须可定位。
