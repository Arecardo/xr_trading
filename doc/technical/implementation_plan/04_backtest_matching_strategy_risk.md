# 回测撮合、策略与风控实施计划

> 对应里程碑：M3（回测撮合、策略与风控）。
> 依赖：M2'（BT-001、BT-002）、CONTRACT-002、CONTRACT-003。

## 1. BT-004 策略接口 + 简单规则策略

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| BT-004 | DONE（2026-08-15） | BT-002、CONTRACT-002 | `SimpleRuleStrategy`（`backend/domain/strategies/simple_rule_strategy.py`）：6 项打分因子（MA5/20 方向、收盘价 vs MA20/50、RSI 超买、20 日新低、相对基准强弱），阈值 ±2 分买卖决策；`AnalysisResult` 从 BE-001 的占位改为 BT-002 `IndicatorBar` 的真实包装 | 权重和恰为 1（现金兜底残差 + 运行时校验）✅；`restricted`/`blacklist` 只能维持或降低权重 ✅；指标不足维持现状不新增买入 ✅；纯函数无副作用（重复调用结果一致）✅；20 个单测 |
| **BT-004 遗留待办** | — | — | 1) `domain.strategies` 依赖 `backend.backtest.IndicatorBar`，架构上 domain 依赖了回测引擎包，等 paper/live 分析管线建好后应把 `IndicatorBar` 挪到中立位置；2) `AnalysisResult` 里 `trading_status` 字段目前没有任何地方真正填充（谁组装 `AnalysisResult` 就得自己去 join `Asset.trading_status`）；3) `candidate` 状态的成员目前和 `approved`/`held` 一样可以被买入，如果只想让 `approved`/`held` 触发买入信号需要额外决策；4) 默认单资产权重上限 20%、20 日窗口、±2 分阈值都是本任务的判断，非文档钉死的值 | — |

## 2. BT-005 风控接口（回测复用）

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| BT-005 | DONE（2026-08-15） | BT-004、CONTRACT-003 | `domain/risk/simple_risk_policy.py`（`SimpleRiskPolicy`）：`check_order`/`check_target_weights` 覆盖单资产/加密类别敞口、最大持仓数、单笔风险预算、现金下限、回撤停止线、restricted 成员禁止新增买入；`replaced_checks()` 按环境返回 `research`/`backtest` 下结构性无法运行的检查（交易时间/人工确认/重复挂单/紧急停止），并明确注明 `paper`/`live` 返回空元组不代表这四项已实现 | 74 个参数化单测覆盖正常/边界/失败路径 ✅；同一 `SimpleRiskPolicy` 实例跨环境行为一致，仅阈值可配置 ✅；`ruff format/check`、`mypy`、`pytest` 全绿 ✅ |
| **BT-005 遗留待办** | — | — | 1) 回撤停止线检查依赖的「历史峰值 NAV」目前只能通过构造函数注入的 `peak_net_asset_value_by_portfolio` 映射提供，`PortfolioState` 只有单一快照、没有序列，缺省时等价于「无回撤」——需要 BT-003 事件循环落地后按日维护真实峰值；2) **（2026-08-15 已修正）** `check_order` 的单资产权重上限检查原先对买卖对称，卖出即使在降低敞口也可能被拒绝；已改为仅在 `side=="buy"` 时评估该检查——卖出永远不会因为这条规则被拒绝，遵循「组合超过上限时只允许减仓、不允许加仓」原则；`check_target_weights` 因缺少单资产实时价格，无法判断某个 `asset_id` 的目标权重相对当前持仓是增是减，该检查暂维持原样（只要目标权重超过上限就拒绝，不区分方向）——这不是本次修正遗漏，而是数据不足以安全区分方向，留待 BT-003 或未来行情接入后重新评估；3) `check_target_weights` 对 restricted 成员的检查范围较窄，只能识别「当前持仓为零、被提议新建仓位」这一种情况，无法在没有单资产价格输入的前提下检测已持有 restricted 资产的权重是否被上调；4) **（2026-08-16 已修正）** `max_order_risk_pct_of_nav` 原按字面（仓位名义金额/NAV）实现，与单资产权重上限结构性冲突（见需求文档 §4.3 澄清、BT-003 遗留问题）；已改为止损资金风险口径（`仓位名义金额占比 × assumed_stop_loss_pct`），新增 `assumed_stop_loss_pct_equity`（默认 8%）/`assumed_stop_loss_pct_crypto`（默认 15%）构造参数，均为假设值（系统无真实止损单机制），可覆盖；`cash`/`other` 类别无假设止损距离，该检查对其跳过（与单资产权重上限的既有类别范围一致）。修正后仍不能让策略一次性买到权重上限（这是止损风险预算制度的正常行为，应分批建仓，非缺陷）——是否要让下单数量本身按风险预算收缩（而非二元通过/拒绝整笔目标差额）是一个更大的、留给后续任务的独立设计问题 | — |

## 3. BT-003 事件驱动回测循环

> **由单一负责人实现，不拆给多个 Agent 并行**——集中承载状态机、撮合语义与精度取整逻辑，理由同 `market-info-service` implementation_plan 中 DB-013（任务并发与事务语义）的处理方式。

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| BT-003 | DONE（2026-08-16） | BT-001、BT-002、DEC-003、BT-004（接口签名即可）、BT-005（接口签名即可） | `backend/application/backtest/`（`BacktestEngine`/`BacktestConfig`/`BacktestExecutionService`/`BacktestResult`）+ `backend/backtest/{instrument_ids,precision,matching,rebalance}.py`（纯逻辑，infra-free）。按 UTC 自然日时间步：日 D 的收盘数据决策 → 日 D+1 开盘价+滑点成交（避免同日收盘决策+成交的前视偏差，最后一天的已批准决策记为 `skipped_no_next_day` 而非静默丢弃）；`stale` 标价日不生成新美股信号但仍计入估值；每笔 `PlannedOrder` 经 `SimpleRiskPolicy.check_order`（非 `check_target_weights`，理由见 `engine.py` 模块文档）+ `BacktestExecutionService`（`approved=False` 不可绕过）；`precision_mode=restricted` 时按标的真实 `lot_size` 下取整，低于 `min_quantity` 不成交且记录原因，精度缺失按标的（非整个回测）fail-closed；订单 ID 用 `uuid.uuid5` 确定性派生，不用 `uuid4`/墙钟 | 35+7 个单测（4 个纯模块 + 引擎集成测试）✅；固定输入可复现 ✅；手续费/滑点影响收益 ✅；风控拒绝的订单不成交 ✅；`restricted` 模式低于 `min_quantity` 不成交 ✅；`ruff`/`mypy`/`pytest`/`security-check` 全绿 |
| **BT-003 遗留待办 / 发现的问题** | — | — | 1) **需要用户裁决**：`SimpleRiskPolicy` 默认 `max_order_risk_pct_of_nav`（0.5%，取自需求文档 §4.3「单笔交易最大组合风险：0.5%-1%」的保守端字面值）与 `SimpleRuleStrategy` 默认单资产权重上限（20%）结构性冲突——任何从零建仓到 20% 目标权重的买单，其名义金额天然是 NAV 的 20%，远超 0.5% 的单笔预算，默认配置下策略事实上永远无法建仓。BT-003 的集成测试为此专门用了放宽后的风控实例（见 `test_engine.py` 的 `_permissive_risk_policy()`），不代表默认配置可用。根因很可能是需求文档「单笔交易最大组合风险」的表述本身有歧义：按交易风控行业惯例，这通常指「假设触发止损后的资金损失」（仓位金额 × 止损距离 / NAV），而不是仓位金额本身占 NAV 的比例——本系统目前没有止损距离/止损单机制，`SimpleRiskPolicy` 按字面（仓位名义金额 / NAV）实现是一种合理但可能偏离原意的解读。2) FX/BTC-USDT 折算路径已实现但集成测试只覆盖了 NVDA/QQQ（均为 USD 计价），未被自动化测试实际跑过；3)「日 D 收盘价决策、日 D+1 开盘价成交」之间存在价格漂移，可能导致现金下限/单笔风险检查在决策时刚好通过、成交时因价格变动而轻微超出，视为可接受的 MVP 级限制，与真实经纪商的下单-成交时间差风险一致，未修复；4) `restricted` 模式下整仓卖出可能因取整残留不足一手的余量（`precision.py` 已有文档记录），未受本任务影响但仍未解决 | — |

## 3.1 BT-003a 碎股跟踪误差对比验证

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| BT-003a | TODO | BT-003、BT-006 | 用同一组历史数据、同一策略，分别以 `unrestricted`/`restricted` 精度模式跑 NVDA/QQQ 回测，对比目标权重跟踪误差、收益率、回撤等指标 | 两轮回测均可复现；产出书面结论——是否需提高初始资金/调整标的/接受跟踪误差，写回需求文档 §5.1.1 与 `roadmap/01_decisions.md` DEC-003 |

## 4. M3 退出门禁

- 固定输入下回测结果可重复（同一输入产出同一输出，逐位一致）。
- 策略与风控接口已定型，签名不再变化，且可被 paper（RM3）直接复用。
- 精度取整、fail-closed 行为在撮合层验证通过，不静默使用默认值；`unrestricted`/`restricted` 两种精度模式均已验证。
- 权重、持仓数、敞口等风控约束在回测中确认生效。
- BT-003a 的碎股跟踪误差对比结论已产出，供 RM3 决策参考。
