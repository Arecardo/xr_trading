# 市场资讯服务 API 与前端管理页面设计

> 来源：拆分自 `../11_market_information_database.md` 与 `../10_market_information_service.md`。

## 1. 前端采集任务管理页面

为了方便观察和运维，前端应增加“市场数据采集”管理页面。该页面属于市场资讯服务的管理能力，不属于交易服务。

首期建议包含四块能力：

| 区域 | 主要功能 |
| --- | --- |
| 数据源状态 | 查看 longbridge、bybit 的启用状态、最近成功时间、失败次数、延迟和健康状态 |
| 采集订阅 | 查看和设置 `collection_subscriptions`，包括 provider instrument、周期、启停、优先级、回填起点和延迟参数 |
| 任务运行 | 查看 `ingestion_runs` 和 `ingestion_tasks`，支持按状态、provider、instrument、interval、时间范围筛选 |
| 手动操作 | 发起手动回填、修复缺口、重试失败任务、取消未执行任务 |

页面只应调用市场资讯服务提供的管理 API，不直接访问数据库。

首期 API 能力占位：

```text
GET  /api/market-info/providers/status
GET  /api/market-info/collection-subscriptions
POST /api/market-info/collection-subscriptions
PATCH /api/market-info/collection-subscriptions/{id}

GET  /api/market-info/ingestion-runs
GET  /api/market-info/ingestion-runs/{id}
GET  /api/market-info/ingestion-tasks

POST /api/market-info/ingestion-runs/backfill
POST /api/market-info/ingestion-tasks/{id}/retry
POST /api/market-info/ingestion-tasks/{id}/cancel
```

权限边界：

- 查看采集状态可以给普通研究用户开放只读权限。
- 修改订阅、发起回填、重试和取消任务需要管理员权限。
- 页面不得展示供应商 token、secret、签名材料或账户权限信息。
- 所有手动操作写入 `requested_by` 和 `context`，便于审计。

## 2. API 设计占位

待补充：最新价格、K 线、数据状态、采集任务管理、手动回填、健康检查 API 及错误码。
