# 投资组合应用重构与历史股票池迁移说明

## 1. 当前状态与目标

当前应用已经完成第一轮组合化重构：用户认证保持兼容，新增资产目录、投资组合、组合成员、账户绑定、持仓与估值快照模型。`stock_pools` 作为早期 MVP 历史表保留，只用于幂等迁移和旧 API 兼容。

产品与页面文案已统一为“XR-Trading 量化投资研究平台”。新前端只调用投资组合与资产 API，旧接口继续代理到组合服务，避免已有客户端立即失效。

## 2. 现有技术实现

- 后端：Python 标准库 `http.server`。
- 数据库：SQLite。
- 前端：原生 HTML、CSS、JavaScript。
- 当前入口：`backend/app.py`。
- 当前核心数据：`users`、`sessions`、`assets`、`portfolios`、`portfolio_members`、`strategy_assignments`、`account_bindings`、`positions`、`portfolio_snapshots`。
- 历史兼容数据：`stock_pools`。

## 3. 目标领域模型

```text
users
  └── portfolios
        ├── portfolio_members ── assets
        ├── strategy_assignments
        ├── account_bindings
        ├── positions
        └── portfolio_snapshots
```

### assets

| 字段 | 说明 |
| --- | --- |
| id | 内部 `asset_id` |
| asset_type | `STOCK`、`ETF`、`CRYPTO`、`CASH` |
| symbol | 展示代码 |
| name | 资产名称 |
| venue | 交易场所 |
| quote_currency | 报价币种 |
| trading_status | 交易状态 |
| metadata_json | 供应商代码等扩展信息 |

### portfolios

| 字段 | 说明 |
| --- | --- |
| id | 投资组合 ID |
| user_id | 所属用户 |
| name | 组合名称 |
| description | 投资目标与说明 |
| base_currency | 基础货币，默认 USD |
| benchmark_asset_id | 基准或组合基准 |
| risk_level | 风险等级 |
| execution_mode | `research`、`backtest`、`paper`、`live` |
| status | `draft`、`active`、`paused`、`archived` |
| created_at / updated_at | 创建与更新时间 |

### portfolio_members

| 字段 | 说明 |
| --- | --- |
| portfolio_id | 投资组合 ID |
| asset_id | 资产 ID |
| member_status | `candidate`、`approved`、`restricted` |
| target_weight_min / max | 权重范围 |
| priority | 研究优先级 |
| added_reason | 加入理由 |
| note | 研究备注 |

持仓、现金和目标权重必须独立建模，不能继续塞入 `stock_pools` 或 `portfolio_members`。

## 4. 目标 API

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/portfolios` | 查询当前用户组合 |
| POST | `/api/portfolios` | 创建组合 |
| GET | `/api/portfolios/{id}` | 查询组合详情 |
| PATCH | `/api/portfolios/{id}` | 更新组合配置 |
| DELETE | `/api/portfolios/{id}` | 归档组合，默认不物理删除 |
| GET | `/api/portfolios/{id}/members` | 查询组合成员 |
| POST | `/api/portfolios/{id}/members` | 添加资产 |
| PATCH | `/api/portfolios/{id}/members/{asset_id}` | 更新成员配置 |
| DELETE | `/api/portfolios/{id}/members/{asset_id}` | 移除未持有资产 |
| GET | `/api/assets` | 搜索资产目录 |
| GET | `/api/portfolios/{id}/positions` | 查询持仓和现金 |
| GET | `/api/portfolios/{id}/performance` | 查询净值与绩效 |

除注册、登录和健康检查外，所有接口需要鉴权并验证资源所属用户。金额、数量和权重输入必须使用十进制校验。

## 5. 兼容迁移策略

### 阶段 A：扩展数据库（已完成）

- 新增 `assets`、`portfolios` 和 `portfolio_members`。
- 保留 `stock_pools`，暂不删除历史数据。
- 所有新增功能只写入新表。

### 阶段 B：迁移历史数据（已完成基础迁移）

- 每条 `stock_pools` 记录迁移成一个 `portfolio`。
- 历史 `theme` 写入组合描述或标签。
- 无法推导的基础货币默认 `USD`，状态先标记为 `draft`，由用户确认。
- 迁移脚本必须可重复运行，并保存旧 ID 与新 ID 的映射。

### 阶段 C：API 兼容（已完成）

- 新前端只调用 `/api/portfolios`。
- `/api/stock-pools` 在过渡期保持只读或代理到组合接口。
- 兼容响应添加弃用提示和迁移目标，不再扩展旧接口。

### 阶段 D：移除旧模型（待完成）

- 确认无旧客户端且完成数据校验后，停止旧接口。
- 数据库备份和迁移审计完成后，再决定是否删除旧表。

## 6. 目标前端结构

产品抬头和登录页统一使用“XR-Trading 量化投资研究平台”。主导航建议为：

- 总览
- 投资组合
- 资产研究
- 策略实验室
- 回测
- 交易与订单
- 风险中心
- 报告
- 设置

投资组合页面首期包含：组合列表、组合创建、组合概览、成员资产、当前配置、目标权重、风险状态和近期表现。旧“股票池信息”页面不再作为长期产品入口。

## 7. 本地运行

```bash
python3 backend/app.py
```

默认访问 `http://127.0.0.1:8080`，SQLite 数据库默认写入 `data/app/xr_trading.sqlite3`。可通过 `XR_TRADING_DB` 和 `PORT` 环境变量覆盖。

## 8. 迁移验收

- 旧用户、会话和密码能力不受影响。
- 每条历史股票池均有且仅有一个迁移后的组合。
- 用户只能访问自己的组合、成员和持仓。
- 新 API 不再使用“股票池”作为产品概念。
- 组合归档不会误删持仓、订单或历史净值。
- 迁移前后记录数、所属用户和 ID 映射可以核对。
