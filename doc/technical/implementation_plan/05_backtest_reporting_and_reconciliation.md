# 回测报告与对账实施计划

> 对应里程碑：M4（回测报告与对账）。
> 依赖：M3（BT-003）。

## 1. BT-006 绩效指标 + 可复现性/对账

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| BT-006 | DONE（2026-08-16） | BT-003 | `backend/application/backtest/metrics.py`（`compute_backtest_metrics`，复用 BE-003 的 `compute_performance_snapshot` 算总收益/最大回撤/年化波动，新增 Sharpe/Sortino（无风险利率默认 0%，可覆盖）、胜率/盈亏比（按资产 FIFO 配对已平仓的买卖手数，毛值不扣手续费）、换手率（成交名义金额/期间平均 NAV）、基准对比（`BacktestResult` 新增 `benchmark_price_series` 字段，由引擎内部已加载的基准序列填充，无需额外网络请求））+ `backend/application/backtest/reconciliation.py`（`reconcile_backtest_result`，独立重放 `trades` 校验现金/NAV 内部一致性/期末持仓/期末现金，与引擎自身的运行时状态无关） | 29 个新单测覆盖正常/边界/失败路径 ✅；相同输入产出相同结果 ✅；`ruff`/`mypy`/`pytest`（380 passed）/`security-check` 全绿 |
| **BT-006 已知缺口** | — | — | M4 退出条件字面要求「逐日对账目标权重/持仓/现金/汇率/净值」五项，但 `BacktestResult` 目前只有离散成交记录 + 逐日聚合的 `positions_value`/`cash_value`/`net_asset_value`，没有逐日的目标权重记录、逐资产持仓/价格明细或逐日汇率——`reconcile_backtest_result` 因此只能独立复算「现金逐日」「NAV 内部一致性（`positions_value+cash_value==net_asset_value`）逐日」「期末持仓」「期末现金」四项，逐日持仓的美元市值/目标权重/汇率未被独立校验（重新拉取同一份行情数据校验价格并非真正独立）。若要满足字面上的五项，需要给 `BacktestResult`/`TradeRecord` 追加字段，留给 BT-007/BT-003a 或后续任务 | — |

## 2. BT-007 回测报告

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| BT-007 | TODO | BT-006 | Markdown/HTML 回测报告（配置、撮合假设、绩效、交易记录、风控事件），落 `reports/backtest/` | 报告含风险指标与撮合假设说明；符合 `09_logging_reports.md` 模板；交易记录中的数量/价格/金额字段均为定点十进制字符串（不使用二进制浮点） |

## 3. M4 退出门禁（同时也是 RM2 整体退出条件）

- 固定输入回测结果可复现。
- 逐日对账通过（目标权重/持仓/现金/汇率/净值）。
- 策略与风控接口已定型并确认可被 paper 复用。
- 报告含完整风险指标，不只展示收益率。

## 4. M5 交付验收（RM1 + RM2 汇合）

M4（本文档）与 M2（`02_portfolio_and_market_integration.md`）全部退出条件满足后，RM1 + RM2 视为交付完成，可进入 RM3（Paper 最小纵切）。汇合前需确认：

- RM1 产出的组合估值模型（`Position`/`ValuationSnapshot`）与 RM2 回测循环内部使用的估值逻辑一致，不存在两套平行实现。
- RM2 定型的 `Strategy`/`RiskPolicy` 接口签名未被 RM1 的领域分包重新定义，二者引用同一份契约（`01_contracts_and_foundation.md` CONTRACT-001/002/003）。
