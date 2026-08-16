# 回测报告与对账实施计划

> 对应里程碑：M4（回测报告与对账）。
> 依赖：M3（BT-003）。

## 1. BT-006 绩效指标 + 可复现性/对账

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| BT-006 | DONE（2026-08-16） | BT-003 | `backend/application/backtest/metrics.py`（`compute_backtest_metrics`，复用 BE-003 的 `compute_performance_snapshot` 算总收益/最大回撤/年化波动，新增 Sharpe/Sortino（无风险利率默认 0%，可覆盖）、胜率/盈亏比（按资产 FIFO 配对已平仓的买卖手数，毛值不扣手续费）、换手率（成交名义金额/期间平均 NAV）、基准对比（`BacktestResult` 新增 `benchmark_price_series` 字段，由引擎内部已加载的基准序列填充，无需额外网络请求））+ `backend/application/backtest/reconciliation.py`（`reconcile_backtest_result`，独立重放 `trades` 校验现金/NAV 内部一致性/期末持仓/期末现金，与引擎自身的运行时状态无关） | 29 个新单测覆盖正常/边界/失败路径 ✅；相同输入产出相同结果 ✅；`ruff`/`mypy`/`pytest`（380 passed）/`security-check` 全绿 |
| **BT-006 已知缺口** | ~~TODO~~ 已于 BT-006a（2026-08-16）补齐，见下一行 | — | M4 退出条件字面要求「逐日对账目标权重/持仓/现金/汇率/净值」五项，但 `BacktestResult` 当时只有离散成交记录 + 逐日聚合的 `positions_value`/`cash_value`/`net_asset_value`，没有逐日的目标权重记录、逐资产持仓/价格明细或逐日汇率——`reconcile_backtest_result` 因此只能独立复算「现金逐日」「NAV 内部一致性（`positions_value+cash_value==net_asset_value`）逐日」「期末持仓」「期末现金」四项，逐日持仓的美元市值/目标权重/汇率未被独立校验。若要满足字面上的五项，需要给 `BacktestResult`/`TradeRecord` 追加字段 | — |

## 1a. BT-006a 逐日对账缺口补齐（2026-08-16）

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| BT-006a | DONE（2026-08-16） | BT-006 | `backend/application/backtest/models.py` 新增 `DailyPortfolioDetail`（`target_weights`/`position_values`/`fx_rates_applied`，逐日一条）+ `BacktestResult.daily_detail`（新增可选字段，默认 `()` 保持向后兼容）；`application/backtest/engine.py` 在既有日循环（估值步骤 (b)、策略步骤 (c)）已经计算出的局部变量基础上组装每日 `DailyPortfolioDetail`，无新增计算、无新增网络调用；`application/backtest/reconciliation.py` 新增 4 项逐日检查（对应模块文档条目 5-8）：per-asset 持仓市值之和与聚合 `positions_value` 一致性、独立重放的逐日持仓量与逐资产市值的零值一致性交叉校验、`target_weights` 边界（`[0,1]`）与总和恰为 1、`fx_rates_applied` 严格为正；`application/backtest/report.py` §5 对账小节据实改写检查范围说明，新增 §7「Daily Detail」小节把逐日目标权重/持仓市值/汇率渲染为人类可读表格（`daily_detail` 为空时显式说明「不可用」，不是空白表格） | 10 个新单测覆盖正常/边界/失败路径（`daily_detail` 缺失时的向后兼容路径、4 项新检查各自的正/负例、报告 §7 有数据/无数据两条渲染路径）✅；相同输入产出相同结果 ✅；`ruff format --check`/`ruff check`/`mypy .`（113 files）/`pytest -q`（414 passed, 33 skipped）/`make security-check` 全绿 |
| **BT-006a 仍未闭合的缺口（诚实记录，非掩盖）** | — | — | (1) 4 项新检查中，持仓市值分解/目标权重边界/汇率为正三项是**内部一致性 + 合理性检查**，不是独立于第三方数据源的数值复核——要做到后者需要重新拉取 `BacktestEngine` 已用过的同一份行情数据，而这本身就不算真正独立；(2) `BacktestResult` 不携带逐资产 `quote_currency`，因此无法检查「每个非本币资产当天是否都记录了汇率」，只能检查已记录的汇率是否为正；(3) 未做「目标权重→交易方向」因果校验（需要逐资产逐日原生价格，`daily_detail` 目前只有折算后的美元市值），刻意推迟而非用一个不严谨的替代实现凑数。三项均记录在 `reconciliation.py` 模块文档「Genuine, documented remaining gaps」 | — |

## 2. BT-007 回测报告

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| BT-007 | DONE（2026-08-16） | BT-006 | `backend/application/backtest/report.py`（纯函数 `render_backtest_report(result, metrics, reconciliation, *, generated_at) -> str`，不读墙钟、不计算指标、不跑对账，只渲染已算好的三个输入；`backtest_report_filename`/`write_backtest_report` 落 `reports/backtest/{portfolio_id}_{start}_{end}_{生成时间戳}.md`，已加入根 `.gitignore`） | 24 个新单测覆盖正常/边界/失败路径（完整 fixture 全字段校验、`None` 指标渲染为显式 "N/A" 而非空白或虚构 0、`turnover=0` 与 `win_rate=None` 的区分、对账失败逐条列出 `Discrepancy`、拒单/跳过交易带原因展示、Markdown 表格转义、裸时间戳拒绝）✅；`ruff format --check`/`ruff check`/`mypy .`（113 files）/`pytest -q`（404 passed, 33 skipped）/`make security-check` 全绿 |
| **BT-007 已知缺口** | 对账覆盖缺口已于 BT-006a（2026-08-16）部分补齐，见 §1a | — | 报告未附带 HTML 变体（`09_logging_reports.md` §5 允许 Markdown 或 HTML 二选一，判断 HTML 不值得当前引入的维护面）；~~报告忠实转述 BT-006 已知的对账缺口（见上一行），不新增对账覆盖~~ BT-007 当时不新增对账覆盖，BT-006a 后续在 §5/§7 补充了逐日目标权重/持仓市值/汇率的展示与检查；报告未落库/未接入任何调度或 API 端点，当前只是可从 `BacktestResult`/`BacktestMetrics`/`ReconciliationResult` 手动调用的纯函数 + 落盘小工具，未来若要「按需」生成需要一个薄的编排入口（CLI 或 API），留给后续任务 | — |

## 3. M4 退出门禁（同时也是 RM2 整体退出条件）

评估时间：2026-08-16（BT-006a 完成后，取代 BT-007 完成后的上一版评估）。

- 固定输入回测结果可复现。**已满足**：`BacktestEngine.run()`/`compute_backtest_metrics`/`reconcile_backtest_result`/`render_backtest_report` 全链路无隐式墙钟、无隐式随机源，相同输入产出相同输出（`render_backtest_report` 的 `generated_at` 也是显式参数，非内部读取）。
- 逐日对账通过（目标权重/持仓/现金/汇率/净值）。**基本满足**：五个维度现在都有逐日记录与检查覆盖，但独立性强度不完全一致——
  - 「现金」「期末持仓」「期末现金」：`reconcile_backtest_result` 从 `trades` 独立重放，与引擎自身运行时状态无关，是最严格意义上的独立对账。
  - 「净值」：`positions_value + cash_value == net_asset_value` 逐日内部一致性校验。
  - 「目标权重」「汇率」「逐日持仓美元市值」（BT-006a，2026-08-16 新增）：`BacktestResult.daily_detail` 逐日记录三者，`reconcile_backtest_result` 对其做边界/总和/正值/内部一致性检查，`render_backtest_report` §7 将其渲染为人类可读表格——但这三项检查**不是**独立于第三方数据源的数值复核（例如汇率只检查「是否为正」，不检查「该记录的资产是否都有对应汇率」；持仓市值只检查「逐资产之和与聚合值一致」，不重新拉取行情验证价格本身）。完全独立的第三方数值复核会要求重新拉取 `BacktestEngine` 已用过的同一份行情数据，这本身就不算真正独立，见 `reconciliation.py` 模块文档「Genuine, documented remaining gaps」。
  - 字面上「五项逐日对账」现在都有对应检查，但严格程度不一——是否视为「完全达成」取决于对「独立」二字的要求高低。
- 策略与风控接口已定型并确认可被 paper 复用。**已满足**：`domain.strategies`/`domain.risk` 的 `Strategy`/`RiskPolicy` 接口（CONTRACT-002/003）被 `application.backtest.engine` 直接复用，未在回测侧另起一套平行实现；`RiskPolicy.replaced_checks()` 显式标注了 `research`/`backtest` 下被替代的四项检查（`trading_hours`/`manual_confirmation`/`duplicate_open_order_conflict`/`emergency_stop_switch`），BT-007 报告将其提升为独立小节，不再只是数据里的一个字段。
- 报告含完整风险指标，不只展示收益率。**已满足**：BT-007 报告覆盖总收益、最大回撤、年化波动、Sharpe、Sortino、胜率、盈亏比、换手率、基准对比，每项均标注其假设（无风险利率、MAR、毛值口径、换手率定义、基准货币口径），未定义的指标显式渲染为 "N/A"，不用空白或虚构 0 冒充。

**结论：M4 基本达成**——四条退出条件中，可复现性/策略风控可复用/完整风险指标三条已严格满足；逐日对账条目的五个维度现在都有逐日记录与检查，但目标权重/汇率/逐日持仓市值三项是内部一致性+合理性检查而非独立于第三方数据源的数值复核，是否算「完全达成」取决于对「独立对账」严格程度的要求。回测/风控逻辑本身的正确性已由现金/NAV/期末持仓/期末现金四项完全独立对账 + 各领域单测覆盖，剩余缺口主要影响「审计颗粒度」而非「计算是否正确」。

## 4. M5 交付验收（RM1 + RM2 汇合）

M4（本文档）与 M2（`02_portfolio_and_market_integration.md`）全部退出条件满足后，RM1 + RM2 视为交付完成，可进入 RM3（Paper 最小纵切）。汇合前需确认：

- RM1 产出的组合估值模型（`Position`/`ValuationSnapshot`）与 RM2 回测循环内部使用的估值逻辑一致，不存在两套平行实现。**已满足**：`application.backtest.engine`/`metrics.py` 直接复用 `domain.valuation.models.ValuationSnapshot`/`domain.valuation.performance.compute_performance_snapshot`（`BacktestResult.equity_curve` 就是 `ValuationSnapshot` 序列本身，不是转换出的另一套结构）。
- RM2 定型的 `Strategy`/`RiskPolicy` 接口签名未被 RM1 的领域分包重新定义，二者引用同一份契约（`01_contracts_and_foundation.md` CONTRACT-001/002/003）。**已满足**（未在本次 BT-007 任务中变更任何 `domain/` 文件，未触及该契约）。

**结论：M2 是否满足需查阅 `02_portfolio_and_market_integration.md` 自身的退出评估（本次任务未重新核实该文档）；就 M4 一侧而言，RM1+RM2 汇合的两项前置条件已满足，M4 自身经 BT-006a 后为「基本达成」（见 §3，五个对账维度均有覆盖，但独立性强度不一）。因此 M5 的整体判定由「部分满足，未就绪」上调为「基本满足，可推进 RM3」——建议维护者知悉 §3 中仍标注的窄口径缺口（无第三方数值复核、无逐资产汇率存在性检查、无目标权重→交易方向因果校验），作为已知限制接受即可推进，或视为后续任务继续收窄。**
