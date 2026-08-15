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
| **BT-005 遗留待办** | — | — | 1) 回撤停止线检查依赖的「历史峰值 NAV」目前只能通过构造函数注入的 `peak_net_asset_value_by_portfolio` 映射提供，`PortfolioState` 只有单一快照、没有序列，缺省时等价于「无回撤」——需要 BT-003 事件循环落地后按日维护真实峰值；2) `check_order` 的单资产权重上限检查对买卖是对称的（不特判方向）：卖出后如果仍然超过上限依然会被拒绝，即使该笔卖出是在降低敞口，这是刻意的设计选择而非疏漏（见模块与测试文档），但存在「组合一旦超上限就无法通过卖出回落」的实际风险，需要用户确认是否符合预期；3) `check_target_weights` 对 restricted 成员的检查范围较窄，只能识别「当前持仓为零、被提议新建仓位」这一种情况，无法在没有单资产价格输入的前提下检测已持有 restricted 资产的权重是否被上调 | — |

## 3. BT-003 事件驱动回测循环

> **由单一负责人实现，不拆给多个 Agent 并行**——集中承载状态机、撮合语义与精度取整逻辑，理由同 `market-info-service` implementation_plan 中 DB-013（任务并发与事务语义）的处理方式。

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| BT-003 | TODO | BT-001、BT-002、DEC-003、BT-004（接口签名即可）、BT-005（接口签名即可） | 按 UTC 自然日时间步：加载行情 → 更新市值 → 生成指标/评分 → 策略信号（`stale` 标价日不生成新美股信号）→ 风控 → 模拟撮合 → 更新持仓现金 → 折算基础货币 → 记录日权益。撮合含手续费、滑点、限价触发规则、最小交易量；成交数量按 `Decimal` 精度记录，是否按标的 `lot_size`/`quantity_scale` 取整由 `precision_mode`（`unrestricted`/`restricted`，2026-08-05 补充，见需求文档 §5.1.1 修订说明）决定；精度规则读自行情服务 Instrument 目录（不硬编码），数据缺失/过期时该标的当期 fail-closed 且记录原因 | 手续费/滑点影响收益；单资产/类别权重与最大持仓数约束生效；固定输入结果可复现；`unrestricted`/`restricted` 两种模式均可跑通且结果可复现；精度数据缺失时对应标的不撮合而非静默使用默认值；撮合规则写入回测报告并注明本次使用的精度模式 |

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
