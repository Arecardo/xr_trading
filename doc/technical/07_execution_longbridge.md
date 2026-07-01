# 交易执行与交易通道适配技术文档

## 1. 模块目标

交易执行模块负责在风控通过后，将组合调仓意图转换为具体通道订单，并持续跟踪订单状态、成交、费用、现金和持仓变化。美股 MVP 使用长桥模拟盘；加密资产先支持研究、回测与组合模拟，交易适配器在供应商确认后接入。

## 2. 模块边界

执行模块负责：

- 查询账户。
- 查询持仓。
- 提交订单。
- 撤销订单。
- 查询订单状态。
- 查询成交记录。
- 查询现金、费用和交易通道能力。
- 记录订单生命周期。

执行模块不负责：

- 生成交易信号。
- 判断是否应该买卖。
- 绕过风控下单。
- 自动开启实盘。

## 3. 订单类型

MVP 支持：

- 市价单。
- 限价单。

默认建议使用限价单，减少小资金账户被价差和滑点侵蚀。

## 4. 订单请求结构

```json
{
  "run_id": "2026-05-31-pre_market-001",
  "portfolio_id": "pf_growth_us_crypto",
  "account_id": "acct_longbridge_paper",
  "asset_id": "equity:nasdaq:NVDA",
  "symbol": "NVDA",
  "side": "buy",
  "order_type": "limit",
  "quantity": 2,
  "limit_price": 102.5,
  "time_in_force": "day",
  "env": "paper",
  "broker": "longbridge",
  "strategy_signal_id": "sig_20260531_NVDA_001",
  "risk_check_id": "risk_20260531_NVDA_001"
}
```

## 5. 订单生命周期

```text
created
  -> submitted
  -> accepted
  -> partially_filled
  -> filled
  -> canceled
  -> rejected
  -> expired
```

每次状态变化必须记录时间、券商订单 ID、原始响应和归一化状态。

## 6. 幂等控制

为避免重复下单，提交订单前必须检查：

- 同一 `strategy_signal_id` 是否已生成订单。
- 同一 `symbol + side` 是否存在未完成订单。
- 同一 `run_id` 是否已经执行过。
- 上一次提交失败是否处于未知状态。

建议生成客户端订单 ID：

```text
client_order_id = {env}-{run_id}-{symbol}-{side}-{seq}
```

## 7. 长桥适配层接口

建议定义统一交易通道接口，长桥作为美股市场的一个实现；各适配器通过 capabilities 声明市场、订单类型、碎股、最小数量和模拟盘能力：

```go
type BrokerAdapter interface {
    GetCapabilities(ctx context.Context) (Capabilities, error)
    GetAccount(ctx context.Context) (AccountSnapshot, error)
    GetPositions(ctx context.Context) ([]Position, error)
    SubmitOrder(ctx context.Context, req OrderRequest) (OrderResponse, error)
    CancelOrder(ctx context.Context, brokerOrderID string) error
    GetOrder(ctx context.Context, brokerOrderID string) (OrderStatus, error)
    ListFills(ctx context.Context, filter FillFilter) ([]Fill, error)
}
```

## 8. 执行流程

1. 接收包含 `portfolio_id`、`account_id` 和 `asset_id` 的风控通过请求。
2. 检查是否需要人工确认。
3. 生成订单请求。
4. 执行幂等检查。
5. 根据资产、账户和市场路由到对应适配器。
6. 记录订单提交结果。
7. 定时同步订单状态。
8. 记录成交。
9. 同步费用、现金并更新本地持仓快照。
10. 对账组合估值并触发报告或通知。

## 9. 异常处理

| 异常 | 处理 |
| --- | --- |
| API 超时 | 查询订单是否存在，禁止盲目重试下单 |
| 下单失败 | 记录失败原因，停止该订单 |
| 状态未知 | 标记为 `unknown` 并触发人工检查 |
| 部分成交 | 记录成交数量，继续跟踪剩余数量 |
| 撤单失败 | 查询最终状态并记录 |

## 10. 测试点

- 风控未通过的建议不能进入下单函数。
- 同一信号重复执行不会重复下单。
- API 超时不会直接再次提交同一订单。
- 模拟盘和实盘端点必须隔离。
- 订单状态变化必须完整落地。
- 不支持目标资产或订单能力的适配器必须在提交前拒绝请求。
- 数量和价格必须符合市场精度、最小交易量及最小名义金额规则。
