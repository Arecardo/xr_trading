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
- 各标的精度规则（`price_scale`/`quantity_scale`/`lot_size`/`min_quantity`），来源于 `market-info-service` 的 Instrument 目录，不得在回测引擎内硬编码（RM0 DEC-003）；缺失或过期时该标的当期 fail-closed，不生成撮合，需在回测报告中标记。
- `precision_mode`（`unrestricted` | `restricted`，2026-08-05 补充）：`unrestricted` 时成交数量按 `Decimal` 任意精度记录，忽略目录 `lot_size`/`quantity_scale`；`restricted` 时按目录真实精度取整，模拟 `paper`/`live` 走长桥/Bybit 真实下单接口时的实际约束（长桥美股「暂不支持碎股交易」，`paper` 与 `live` 同一套下单接口，因此需要用 `restricted` 模式验证）。两种模式需能对同一输入分别跑通，用于对比碎股跟踪误差（见 `doc/technical/roadmap/03_backtest_engine.md` BT-003a）。

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
  valuation_cutoff: "00:00"
  precision_mode: unrestricted  # unrestricted | restricted，见 §3 输入说明
```

## 5. 事件驱动流程

**`time_step` 为 UTC 自然日，全周期 365 天/年推进，不因美股非交易日跳过**（RM0 DEC-002，与 `02_config_environment.md` 的 `valuation_cutoff` 定义一致）：美股非交易日沿用上一交易日收盘价并标记 `price_status: stale`，不生成新的交易信号（可评估现有持仓但不产生新的美股调仓建议）；加密资产照常取当日新数据、正常参与信号与撮合。此规则确保权益曲线连续反映加密资产周末波动对组合风险的影响。

```text
for each utc_calendar_day:
  load market data (equity: carry-forward + stale flag on non-trading days; crypto: fresh daily data)
  update portfolio market value
  generate indicators
  generate analysis scores
  generate strategy signals (skip new equity signals when underlying price is stale)
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
- 成交数量按 `Decimal` 精度记录；是否按标的 `lot_size`/`quantity_scale` 取整到最小步长由 `precision_mode` 决定（`unrestricted` 不取整，`restricted` 取整，见 §3、§4；2026-08-05 修订，理由见需求文档 §5.1.1）。`restricted` 模式下取整后数量低于 `min_quantity` 时该笔交易不成交并记录原因。

具体规则必须写入回测报告，避免回测与实盘执行假设不一致。

## 7. 输出指标

```json
{
  "start_date": "2024-01-01",
  "end_date": "2026-05-31",
  "initial_cash": "2500.00",
  "final_equity": "2875.00",
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
  "quantity": "2.438",
  "fill_price": "102.50",
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

## 9a. 单笔订单风险预算与仓位拆分（2026-08-16 补充）

`RiskPolicy.check_order`（CONTRACT-003）是逐笔硬性门禁：一笔买单若使组合超出单笔风险预算（`max_order_risk_pct_of_nav`，以止损资金风险解读，见需求文档 §4.3 澄清）或触及单资产/加密类别权重上限，会被整单拒绝。若目标权重与当前持仓的差距（调仓缺口）本身较大——例如策略希望一次性把某标的加到默认 20% 上限——在保守的默认风险预算下几乎必然被拒绝，导致策略永远无法建仓到目标权重。

**处理方式：回测引擎在买单被拒绝时，对订单数量做二分收缩重试**，而不是直接记为拒绝：

- 只对 `side == "buy"` 生效——卖出永不因权重上限被拒绝（2026-08-16 已有修正：组合超上限时只许减仓不许加仓），因此没有收缩的必要。
- 引擎把 `RiskPolicy` 当作不透明的「批准/拒绝」黑盒：按 `quantity /= 2` 逐次收缩，每次收缩后重新调用一次 `check_order`，直到获批或达到收缩次数上限（8 次，约 1/256 分辨率）。**不检查 `rejection_reasons` 文本、不复制风控内部阈值算法**——这样任何随订单规模缩小而更容易通过的风控规则（权重上限、现金下限、单笔风险预算）都能被这个通用机制处理，无需引擎逐条了解具体规则；与订单规模无关的规则（如最大持仓数量、回撤停止线）在收缩到底后依然会被拒绝，这是正确的语义，不是缺陷。
- 获批的（可能被收缩的）数量成为该笔订单实际提交执行的数量；`TradeRecord.reason` 中会记录一条说明（原始缺口数量 → 实际提交数量），供报告和复盘查阅，`TradeRecord.planned_quantity`/`quantity` 字段语义不变（无需新增字段）。
- 由于目标权重差距是每天基于当前持仓重新计算的（`backtest.rebalance.diff_target_weights_to_orders`），当某天的买单因收缩只成交了部分缺口后，次日会自动基于「更接近目标但仍未到位」的持仓重新算出一个更小的缺口并再次尝试——多笔逐日建仓天然形成，无需引擎额外维护一个「拆单计划」的调度状态，也就天然具备类似 TWAP（分批建仓、随时间摊薄单笔风险）的效果，同时比固定分片数的严格 TWAP 更能适应组合状态变化（例如 NAV 波动导致预算容量变化）。

具体实现见 `application.backtest.engine.BacktestEngine._shrink_buy_to_approved`。

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
