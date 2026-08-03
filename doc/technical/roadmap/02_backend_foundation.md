# P1 核心后端架构地基（RM1）

> 目的：把 `backend/` 单体原型升级为按领域分包的结构，落地组合估值与行情服务集成，消除「核心弱于外围」。
> 依赖：RM0（尤其 DEC-005 架构方向、DEC-001 标的、DEC-002 日切）。
> 参照：`doc/technical/01_architecture.md`、`doc/technical/stock_pool_app/README.md`、`.claude/standards/python-backend-standards.md`。
> 状态：TODO。最近更新：2026-08-04。

## 任务清单

### BE-001 领域分包骨架 — P1
- 依赖：DEC-005
- 输出：按 `api/application/domain/adapters/repository/config/observability` 拆分 `backend/`；`app.py` 只保留进程装配。领域子包：assets、portfolios、accounts、strategies、risk、execution、valuation。
- 测试：现有认证、组合 CRUD、成员管理接口回归通过。
- 完成条件：目录符合 python 规范 §2；旧行为不回归。

### BE-002 持久化落地 — P1
- 依赖：DEC-005、BE-001
- 输出：按 DEC-005 结论落地。若升级：PostgreSQL + 版本化 migration（Alembic）+ 连接池；若保留：SQLite 但引入迁移机制与 repository 层隔离。
- 测试：migration 可重复执行；repository 读写有隔离数据库集成测试。
- 完成条件：领域层不泄漏驱动特有类型；金额字段用 decimal。

### BE-003 组合估值与快照模型 — P1
- 依赖：BE-002、DEC-002
- 输出：`Position`、`CashBalance`、`ValuationSnapshot`、`PerformanceSnapshot` 落地；支持原币值+汇率+基础货币折算；按 DEC-002 日切生成净值快照。
- 测试：给定持仓/现金/汇率/价格，逐项对账净值；汇率缺失时不产出伪精确净值。
- 完成条件：可生成组合当前配置、现金比例、净值。

### BE-004 行情服务集成契约 — P1
- 依赖：BE-001，行情服务查询 API（已具备）
- 输出：`adapters/market_data` 客户端调用 `market-info-service` 的最新价/K 线/instrument 查询；采集资产范围由**组合成员/持仓/基准**驱动并同步给行情服务的订阅。
- 测试：契约测试（对 fake/录制响应）；采集范围随组合成员变化正确更新。
- 完成条件：两服务通过 API 契约解耦，不共享数据库表（见项目规范 §2）。

## 退出条件（RM1）

领域骨架就位且旧接口不回归；组合估值/持仓/现金/汇率快照可生成；行情采集范围由组合驱动；核心领域包具备单测。
