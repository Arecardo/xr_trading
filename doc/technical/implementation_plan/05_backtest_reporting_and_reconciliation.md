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
| BT-007 | DONE（2026-08-16） | BT-006 | `backend/application/backtest/report.py`（纯函数 `render_backtest_report(result, metrics, reconciliation, *, generated_at) -> str`，不读墙钟、不计算指标、不跑对账，只渲染已算好的三个输入；`backtest_report_filename`/`write_backtest_report` 落 `reports/backtest/{portfolio_id}_{start}_{end}_{生成时间戳}.md`，已加入根 `.gitignore`） | 24 个新单测覆盖正常/边界/失败路径（完整 fixture 全字段校验、`None` 指标渲染为显式 "N/A" 而非空白或虚构 0、`turnover=0` 与 `win_rate=None` 的区分、对账失败逐条列出 `Discrepancy`、拒单/跳过交易带原因展示、Markdown 表格转义、裸时间戳拒绝）✅；`ruff format --check`/`ruff check`/`mypy .`（113 files）/`pytest -q`（404 passed, 33 skipped）/`make security-check` 全绿 |
| **BT-007 已知缺口** | — | — | 报告未附带 HTML 变体（`09_logging_reports.md` §5 允许 Markdown 或 HTML 二选一，判断 HTML 不值得当前引入的维护面）；报告忠实转述 BT-006 已知的对账缺口（见上一行），不新增对账覆盖；报告未落库/未接入任何调度或 API 端点，当前只是可从 `BacktestResult`/`BacktestMetrics`/`ReconciliationResult` 手动调用的纯函数 + 落盘小工具，未来若要「按需」生成需要一个薄的编排入口（CLI 或 API），留给后续任务 | — |

## 3. M4 退出门禁（同时也是 RM2 整体退出条件）

评估时间：2026-08-16（BT-007 完成后）。

- 固定输入回测结果可复现。**已满足**：`BacktestEngine.run()`/`compute_backtest_metrics`/`reconcile_backtest_result`/`render_backtest_report` 全链路无隐式墙钟、无隐式随机源，相同输入产出相同输出（`render_backtest_report` 的 `generated_at` 也是显式参数，非内部读取）。
- 逐日对账通过（目标权重/持仓/现金/汇率/净值）。**部分满足**：`reconcile_backtest_result` 独立复算并逐日校验「现金」「NAV 内部一致性」，外加「期末持仓」「期末现金」两项一次性校验——四项均通过 `reconcile_backtest_result` 的确定性单测验证。**逐日目标权重、逐日汇率、逐日持仓美元市值三项未被独立核对**：`BacktestResult`/`TradeRecord` 当前不携带逐日目标权重记录、逐日汇率或逐日per-asset价格明细，无法在不重新拉取同一份行情数据的前提下做到真正独立的复核。字面上的「五项逐日对账」尚未完全达成，这是 BT-006 已记录、BT-007 忠实转述（未掩盖）的已知缺口，需要给 `BacktestResult`/`TradeRecord` 追加字段才能补齐，留给后续任务（BT-003a 或新任务）。
- 策略与风控接口已定型并确认可被 paper 复用。**已满足**：`domain.strategies`/`domain.risk` 的 `Strategy`/`RiskPolicy` 接口（CONTRACT-002/003）被 `application.backtest.engine` 直接复用，未在回测侧另起一套平行实现；`RiskPolicy.replaced_checks()` 显式标注了 `research`/`backtest` 下被替代的四项检查（`trading_hours`/`manual_confirmation`/`duplicate_open_order_conflict`/`emergency_stop_switch`），BT-007 报告将其提升为独立小节，不再只是数据里的一个字段。
- 报告含完整风险指标，不只展示收益率。**已满足**：BT-007 报告覆盖总收益、最大回撤、年化波动、Sharpe、Sortino、胜率、盈亏比、换手率、基准对比，每项均标注其假设（无风险利率、MAR、毛值口径、换手率定义、基准货币口径），未定义的指标显式渲染为 "N/A"，不用空白或虚构 0 冒充。

**结论：M4 未完全达成**——差在「逐日对账」条目的目标权重/汇率/逐日持仓市值三个子项，其余三条退出条件已满足。是否推进 RM3 取决于这个缺口是否可接受（回测/风控逻辑本身的正确性已由现金/NAV/期末持仓/期末现金四项独立对账 + 各领域单测覆盖，缺口主要影响「审计颗粒度」而非「计算是否正确」）。

## 4. M5 交付验收（RM1 + RM2 汇合）

M4（本文档）与 M2（`02_portfolio_and_market_integration.md`）全部退出条件满足后，RM1 + RM2 视为交付完成，可进入 RM3（Paper 最小纵切）。汇合前需确认：

- RM1 产出的组合估值模型（`Position`/`ValuationSnapshot`）与 RM2 回测循环内部使用的估值逻辑一致，不存在两套平行实现。**已满足**：`application.backtest.engine`/`metrics.py` 直接复用 `domain.valuation.models.ValuationSnapshot`/`domain.valuation.performance.compute_performance_snapshot`（`BacktestResult.equity_curve` 就是 `ValuationSnapshot` 序列本身，不是转换出的另一套结构）。
- RM2 定型的 `Strategy`/`RiskPolicy` 接口签名未被 RM1 的领域分包重新定义，二者引用同一份契约（`01_contracts_and_foundation.md` CONTRACT-001/002/003）。**已满足**（未在本次 BT-007 任务中变更任何 `domain/` 文件，未触及该契约）。

**结论：M2 是否满足需查阅 `02_portfolio_and_market_integration.md` 自身的退出评估（本次任务未重新核实该文档）；就 M4 一侧而言，RM1+RM2 汇合的两项前置条件已满足，但 M4 自身尚未完全达成（见 §3），因此 M5 的整体判定是"部分满足，未就绪"——建议在推进 RM3 前，由维护者决定是否要求先补齐 §3 的逐日对账缺口，还是接受当前的对账颗粒度作为 RM3 起点的已知限制。**
