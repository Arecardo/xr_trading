# 回测模块技术文档

## 1. 模块目标

回测模块用于验证策略与投资组合在历史数据上的表现，输出收益、风险、资产配置和基准对比。回测结果不得只展示收益率。

## 2. 回测范围

MVP 支持：

- 美股与 ETF 日线级事件驱动回测。
- 加密资产日线级回测，后续可扩展小时级。
- 跨市场交易日历与加密 24/7 时间轴。
- 手续费估算。
- 滑点估算。
- 最大持仓数量约束。
- 单资产和资产类别最大权重约束。
- 止损和减仓规则。
- 策略信号复用或重新计算。

暂不支持：

- 高频 tick 回测。
- 期权。
- 融资融券。
- 做空。
- 杠杆。

## 3. 输入

- 历史日线 K 线。
- 历史投资组合成员、资产目录和基准。
- 策略配置。
- 风控配置。
- 交易成本配置。
- 起始资金。

## 4. 回测配置示例

```yaml
backtest:
  initial_cash: 2500
  base_currency: USD
  start_date: "2024-01-01"
  end_date: "2026-05-31"
  commission_pct: 0.001
  min_commission: 0
  slippage_pct: 0.001
  benchmarks: [QQQ, BTC-USD]
  valuation_timezone: UTC
```

## 5. 事件驱动流程

```text
for each time_step:
  load market data
  update portfolio market value
  generate indicators
  generate analysis scores
  generate strategy signals
  run risk checks
  simulate orders and fills
  update positions and cash
  convert values to base currency
  record daily equity
```

## 6. 成交价格模拟

MVP 可采用以下规则：

- 买入：使用下一交易日开盘价或当日收盘价，加上滑点。
- 卖出：使用下一交易日开盘价或当日收盘价，减去滑点。
- 限价单：若价格区间触及限价，则视为成交；否则未成交。

具体规则必须写入回测报告，避免回测与实盘执行假设不一致。

## 7. 输出指标

```json
{
  "start_date": "2024-01-01",
  "end_date": "2026-05-31",
  "initial_cash": 2500,
  "final_equity": 2875,
  "total_return_pct": 0.15,
  "max_drawdown_pct": 0.08,
  "annualized_volatility": 0.18,
  "sharpe_ratio": 0.83,
  "sortino_ratio": 1.10,
  "win_rate": 0.54,
  "profit_loss_ratio": 1.35,
  "trade_count": 42,
  "benchmark_return_pct": 0.12
}
```

## 8. 交易记录

每笔模拟交易应记录：

```json
{
  "trade_id": "bt_20240501_NVDA_001",
  "portfolio_id": "pf_growth_us_crypto",
  "asset_id": "equity:nasdaq:NVDA",
  "symbol": "NVDA",
  "side": "buy",
  "quantity": 3,
  "fill_price": 102.5,
  "commission": 0.31,
  "slippage": 0.10,
  "signal_score": 78,
  "reason": ["technical trend positive"],
  "trade_date": "2024-05-01"
}
```

## 9. 风控复用

回测必须尽量复用风控规则，避免出现回测能交易、模拟盘被拦截的偏差。对于无法完全复用的实时检查，应在回测中使用替代实现并标记：

- 交易时间检查：回测默认通过。
- 人工确认：回测默认通过。
- API 订单冲突：回测使用模拟订单簿检查。
- 重大事件窗口：如果有历史事件数据则启用，否则标记未启用。

## 10. 测试点

- 固定输入下回测结果应可重复。
- 资金、持仓、成交和权益曲线应能对账。
- 单资产权重不能超过对应配置阈值。
- 最大持仓数量不能超过配置阈值。
- 手续费和滑点必须影响收益。
- 回测报告必须展示风险指标。
- 美股休市期间不得错误成交，加密资产应按 24/7 时间轴估值。
- 组合目标权重、持仓、现金、汇率和净值必须可逐日对账。
- 不得使用未来数据、未来成分或未在当时可得的修订数据。
