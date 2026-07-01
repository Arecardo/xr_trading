# 日志、审计与报告技术文档

## 1. 模块目标

日志、审计与报告模块负责记录系统运行全过程，并生成组合日报、调仓计划、风险报告、回测报告、模拟运行报告和实盘切换评估报告。

## 2. 日志类型

系统必须记录：

- 原始行情数据更新时间。
- 新闻分析结果。
- 技术指标结果。
- 基本面评分。
- 市场环境判断。
- 策略信号。
- 组合目标权重与调仓计划。
- 风控检查结果。
- 订单请求。
- 订单状态变化。
- 成交记录。
- 现金、持仓和组合估值快照。
- 人工确认记录。
- 异常和错误。

## 3. 结构化日志字段

```json
{
  "timestamp": "2026-05-31T08:00:00+08:00",
  "level": "info",
  "service": "strategy_service",
  "env": "paper",
  "run_id": "2026-05-31-pre_market-001",
  "portfolio_id": "pf_growth_us_crypto",
  "asset_id": "equity:nasdaq:NVDA",
  "strategy_version": "strategy_v0.1.0",
  "config_version": "sha256:4c9a...",
  "message": "strategy signal generated",
  "symbol": "NVDA"
}
```

## 4. 审计链路

交易相关记录必须可以串联：

```text
analysis_result
  -> strategy_signal
  -> target_allocation
  -> rebalance_plan
  -> risk_check
  -> manual_confirm
  -> order_request
  -> broker_order
  -> fill
  -> position_snapshot
  -> valuation_snapshot
  -> portfolio_review
```

建议所有对象包含：

- `run_id`
- `portfolio_id`
- `account_id`
- `asset_id`
- `env`
- `symbol`
- `strategy_version`
- `config_version`
- `created_at`

## 5. 报告类型

| 报告 | 频率 | 格式 |
| --- | --- | --- |
| 组合日报 | 每个组合估值日 | Markdown 或 HTML |
| 调仓计划 | 每次策略运行 | Markdown 或 HTML |
| 风险与敞口报告 | 每日或异常触发 | Markdown 或 HTML |
| 每周策略表现 | 每周 | Markdown 或 HTML |
| 回测报告 | 按需 | Markdown 或 HTML |
| 模拟盘运行报告 | 每周或每月 | Markdown |
| 实盘切换评估报告 | 实盘前 | Markdown |

## 6. 盘前计划模板

```markdown
# 投资组合调仓计划

## 组合概览

- 组合：美股与加密成长组合
- 当前净值：
- 现金比例：
- 美股 / ETF / 加密配置：

## 市场环境

- 状态：risk_on
- 原因：
  - QQQ 趋势向上
  - 波动率稳定

## 今日建议

| 资产 | 当前权重 | 目标权重 | 动作 | 风控状态 | 原因 |
| --- | --- | --- | --- | --- | --- |
| NVDA | 12% | 18% | buy | passed | 技术趋势向上，新闻利好 |

## 禁止交易

| 标的 | 原因 |
| --- | --- |
| TSLA | 组合成员状态为 restricted |
```

## 7. 盘后复盘模板

```markdown
# 投资组合日报

## 当日摘要

- 当日盈亏：
- 账户权益：
- 现金比例：
- 最大持仓：
- 组合净值与基准：
- 最大回撤 / 波动率 / Sharpe：

## 信号与执行

| 标的 | 信号 | 风控 | 订单 | 成交 | 滑点 |
| --- | --- | --- | --- | --- | --- |
| NVDA | buy | passed | submitted | filled | 0.08% |

## 风控事件

| 事件 | 标的 | 处理 |
| --- | --- | --- |
| max_position_pct_exceeded | NVDA | 拦截 |

## 复盘结论

- 策略命中：
- 偏差原因：
- 明日关注：
```

## 8. 通知规则

以下事件应触发通知：

- 每日任务失败。
- API 连续失败。
- 风控拦截。
- 订单被拒绝。
- 订单状态未知。
- 总回撤接近警戒线。
- 实盘环境启动。

## 9. 存储路径

```text
reports/
  portfolio_daily/
  rebalance_plan/
  risk_exposure/
  weekly/
  backtest/
  paper_run/
  live_readiness/
```

## 10. 测试点

- 每条订单记录能追溯到策略信号和风控记录。
- 每日报告包含配置、信号、订单、成交、持仓、现金、净值和基准对比。
- 风控拦截必须出现在复盘报告中。
- 异常日志必须包含 `run_id` 和服务名。
- 实盘切换评估报告必须检查需求文档中的全部实盘条件。
