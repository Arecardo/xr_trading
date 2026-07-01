# 配置与环境隔离技术文档

## 1. 模块目标

配置模块负责统一管理资产目录、投资组合、策略权重、风险预算、数据源、交易通道和运行环境。环境隔离模块确保 `research`、`backtest`、`paper`、`live` 不会误用凭据或误触发实盘交易。

## 2. 配置文件

推荐配置文件：

```text
config/
  assets.yaml
  portfolios.yaml
  risk.yaml
  strategies.yaml
  providers.yaml
  env.yaml
```

敏感信息不得写入上述文件，应通过环境变量或本地密钥文件注入。

## 3. 环境定义

| 环境 | 用途 | 是否允许下单 |
| --- | --- | --- |
| research | 数据探索、策略实验 | 否 |
| backtest | 历史回测 | 否 |
| paper | 模拟组合与模拟交易 | 是，仅模拟通道 |
| live | 小资金真实交易 | 是，必须按组合授权并人工确认 |

默认环境必须是 `paper` 或 `backtest`。不允许将 `live` 设置为默认环境。

## 4. 环境配置示例

```yaml
environment:
  default: paper
  timezone: Asia/Kuala_Lumpur
  markets: [US, CRYPTO]
  valuation_timezone: UTC
  valuation_cutoff: "00:00"
  report_language: zh-CN
  data_root: data
  report_root: reports
```

## 5. 交易配置示例

```yaml
trading:
  mode: paper
  allow_live_trading: false
  require_manual_confirm: true
  adapters:
    US: longbridge
    CRYPTO: disabled
```

## 6. 实盘启用双重确认

进入 `live` 环境必须同时满足：

- `trading.mode == live`
- `trading.allow_live_trading == true`
- 目标投资组合已显式设置 `execution_mode: live`。
- 对应交易通道存在独立实盘 API 凭据。
- 模拟盘 API 凭据和实盘 API 凭据不可复用。
- `require_manual_confirm == true`
- 用户完成明确确认记录。

建议启动时执行防呆检查：

```text
if env == live:
  assert allow_live_trading == true
  assert require_manual_confirm == true
  assert live_api_key exists
  assert paper_api_key != live_api_key
else:
  block live broker endpoint
```

## 7. 配置版本

每次运行应记录配置版本。MVP 可使用配置文件内容哈希：

```json
{
  "config_version": "sha256:4c9a...",
  "files": [
    "config/assets.yaml",
    "config/portfolios.yaml",
    "config/risk.yaml",
    "config/strategies.yaml",
    "config/providers.yaml",
    "config/env.yaml"
  ]
}
```

## 8. 配置校验

启动时必须校验：

- 策略权重之和是否为 1。
- 风控阈值是否在合理范围。
- 活跃组合是否至少包含一个有效成员。
- `asset_id`、交易场所和供应商代码映射是否有效。
- 组合允许的资产类型与成员是否一致。
- 基础货币、估值时区和汇率数据是否可用。
- `live` 环境是否满足双重确认。
- 数据目录和报告目录是否可写。
- 时区和交易市场是否一致。

## 9. 测试点

- `live` 未显式开启时，所有实盘下单应失败。
- 策略权重不等于 1 时启动失败。
- 启用美股交易但缺少长桥凭据时，对应适配器启动失败。
- 未启用加密交易适配器时，加密资产只能研究、回测或组合模拟。
- `paper` 和 `live` 凭据相同时启动失败。
- 配置变更后，生成新的 `config_version`。
