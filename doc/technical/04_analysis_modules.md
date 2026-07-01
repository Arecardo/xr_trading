# 分析模块技术文档

## 1. 模块目标

分析模块负责将原始数据转换为可解释研究结果，包括技术分析、新闻舆情、资产类型专属分析和市场环境分析。分析结果供策略与组合构建模块使用。

## 2. 技术分析

### 输入

- 美股或加密资产的标准化 K 线。
- 对应基准的 K 线，例如 QQQ、SPY 或 BTC。
- 成交量数据。

### 指标

- 日线收益率。
- 5 日、20 日、50 日均线。
- 均线趋势。
- RSI。
- ATR。
- 成交量变化。
- 价格相对 20 日高低点位置。
- 相对 QQQ 或 SPY 的强弱。

### 输出示例

```json
{
  "symbol": "NVDA",
  "asset_id": "equity:nasdaq:NVDA",
  "asset_type": "STOCK",
  "trade_date": "2026-05-29",
  "trend_state": "uptrend",
  "momentum_state": "positive",
  "volatility_state": "normal",
  "overheated": false,
  "breakdown": false,
  "relative_strength": 0.12,
  "technical_score": 78
}
```

### 评分建议

| 条件 | 分数影响 |
| --- | --- |
| 收盘价高于 20 日和 50 日均线 | 加分 |
| 5 日均线高于 20 日均线 | 加分 |
| RSI 过高 | 降低追涨评分 |
| ATR 显著升高 | 降低仓位建议 |
| 跌破 20 日低点 | 大幅扣分 |
| 强于对应资产类别基准 | 加分 |

## 3. 新闻舆情分析

### 输入

- 标的相关新闻。
- 新闻标题、正文、发布时间、来源。

### 处理流程

1. 按标题、URL、发布时间和正文指纹去重。
2. 分类事件类型。
3. 判断情绪方向。
4. 判断重要性。
5. 生成结构化摘要。
6. 汇总到标的级新闻分。

### 事件类型

- earnings
- product
- regulation
- lawsuit
- analyst_rating
- merger_acquisition
- macro
- management
- other

### 输出示例

```json
{
  "symbol": "NVDA",
  "headline": "...",
  "event_type": "earnings",
  "sentiment": "positive",
  "importance": "high",
  "summary": "...",
  "score": 82,
  "published_at": "2026-05-29T21:30:00+08:00"
}
```

## 4. 资产类型专属分析

分析器必须按 `asset_type` 路由。美股与 ETF 使用公司或基金基本面；加密资产使用市场结构、供应、流动性、网络与监管风险数据。缺少不适用字段不应被填成中性值后机械参与评分。

### 4.1 美股与 ETF 基本面

### 输入

- 营收增长率。
- 毛利率。
- 净利润率。
- 自由现金流。
- 市销率。
- 市盈率。
- 未来指引。
- 最近财报日期。
- 分析师预期变化。

### 输出示例

```json
{
  "symbol": "NVDA",
  "as_of_date": "2026-05-29",
  "revenue_growth_score": 90,
  "margin_score": 85,
  "valuation_score": 55,
  "guidance_score": 80,
  "analyst_revision_score": 70,
  "fundamental_score": 76
}
```

### 约束

基本面评分不单独触发交易，只作为过滤和加权因素。

### 4.2 加密资产分析

建议输入：市值、现货成交量、交易所流动性、价差、供应变化、网络活跃度、资金流、稳定币或托管风险以及监管事件。

```json
{
  "asset_id": "crypto:coinbase:BTC-USD",
  "as_of_time": "2026-06-29T00:00:00Z",
  "liquidity_score": 90,
  "market_structure_score": 82,
  "network_score": 76,
  "event_risk": "normal",
  "crypto_score": 81
}
```

链上数据不可用时应标记覆盖率，不得假造精确评分。稳定币脱锚、交易所中断或严重流动性下降可直接触发风险标记。

## 5. 市场环境分析

### 输入

- QQQ、SPY 与美股市场宽度。
- BTC、加密总市值、稳定币和主要交易场所状态。
- 纳斯达克市场宽度。
- VIX 或波动率代理。
- 半导体板块相对强弱。
- FOMC、CPI、非农等宏观事件日期。

### 输出

```json
{
  "trade_date": "2026-05-29",
  "regime": "risk_on",
  "market_score": 82,
  "reasons": [
    "QQQ above 20d and 50d moving average",
    "VIX proxy stable",
    "semiconductor relative strength positive"
  ]
}
```

### 交易影响

| regime | 策略影响 |
| --- | --- |
| risk_on | 正常买入、正常仓位 |
| neutral | 降低评分或降低目标仓位 |
| risk_off | 禁止新增或显著降低仓位 |

## 6. 统一输出

分析模块最终输出统一外壳，并通过 `components` 承载不同资产类型的评分：

```json
{
  "asset_id": "equity:nasdaq:NVDA",
  "asset_type": "STOCK",
  "technical_score": 78,
  "sentiment_score": 82,
  "components": {
    "fundamental_score": 76
  },
  "market_score": 82,
  "analysis_version": "analysis_v0.1.0",
  "created_at": "2026-05-31T08:00:00+08:00"
}
```

## 7. 测试点

- K 线不足 50 日时，技术评分应降级处理。
- RSI、ATR、均线计算应有固定样本测试。
- 新闻重复输入时只保留一条。
- 缺少基本面数据时，不应阻断全局流程。
- `risk_off` 市场环境必须影响策略评分或仓位。
- 加密资产不会因缺少市盈率等股票字段而被错误扣分。
- 美股休市和加密数据中断必须采用不同的数据质量判断。
