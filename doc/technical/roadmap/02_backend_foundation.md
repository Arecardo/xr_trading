# P1 核心后端架构地基（RM1）

> 目的：把 `backend/` 单体原型升级为按领域分包的结构，落地组合估值与行情服务集成，消除「核心弱于外围」。
> 依赖：RM0（尤其 DEC-005 架构方向、DEC-001 标的、DEC-002 日切）。
> 参照：`doc/technical/01_architecture.md`、`doc/technical/stock_pool_app/README.md`、`.claude/standards/python-backend-standards.md`。
> 状态：TODO。最近更新：2026-08-05。

## 任务清单

### BE-001 领域分包骨架 — P1
- 依赖：DEC-005
- 输出：按 `api/application/domain/adapters/repository/config/observability` 拆分 `backend/`；`app.py` 只保留进程装配。

  领域子包职责划分：

  | 子包 | 职责 | 明确不做 |
  | --- | --- | --- |
  | `assets` | `Asset` 实体（`asset_id`/`asset_type`/`symbol`/`venue`/`quote_currency`/`provider_symbols`/`trading_status`）及唯一性规则 | 不涉及资产与组合的关系（属于 `portfolios`） |
  | `portfolios` | `Portfolio`（基础货币/基准/风险等级/执行模式/状态机）、`PortfolioMember`（`candidate`/`approved`/`held`/`restricted` 状态机、目标权重区间）、再平衡预览的领域规则 | 不做具体策略评分/目标权重计算（属于 `strategies`） |
  | `accounts` | `AccountBinding`——组合与 Broker/Exchange 账户的绑定、凭据引用（不存密钥本身）、环境（research/backtest/paper/live）与凭据匹配校验（模拟盘/实盘凭据不可复用规则） | 不做下单，只做绑定关系与环境校验 |
  | `strategies` | `Strategy` 版本化规则、资产评分、目标权重生成、再平衡建议 | 只输出建议，不提交订单、不绕过风控（需求文档 §2.3「研究与执行分离」） |
  | `risk` | `RiskPolicy`——组合级/订单级风控规则、风险预算、权重上限校验，生成可审计的风控结果 | 不生成信号、不决定买卖 |
  | `execution` | `Order`/`Fill`——订单生成、状态机、`paper` 模拟撮合、`live` 下单适配（凭据/环境校验委托给 `accounts`） | 不生成信号、不判断买卖、不绕过风控、不自动开启实盘（`python-backend-standards.md` §7） |
  | `valuation` | `Position`、`CashBalance`、`ValuationSnapshot`（按 DEC-002 日切规则）、`PerformanceSnapshot`（收益/回撤/波动率/基准贡献度）、汇率折算 | 不做策略/风控判断 |

  依赖方向：`execution` → `risk` → `portfolios`/`valuation`；`strategies` → `assets`/`portfolios`（只读）；`valuation` → `assets`/`accounts`（汇率、账户维度）。均为领域内部依赖，不依赖 `api`/`adapters`/`repository`。
- 测试：现有认证、组合 CRUD、成员管理接口回归通过。
- 完成条件：目录符合 python 规范 §2；旧行为不回归。

### BE-002 持久化落地 — P1
- 依赖：DEC-005、BE-001
- 输出：按 DEC-005 已定结论落地——**PostgreSQL + Alembic 版本化 migration + 连接池**，独立数据库实例/角色，不与行情服务共享表（`project-conventions.md` §2）；Postgres 部署复用 `market-info-service/compose.yaml` 已验证的服务模式。
- 测试：migration 可重复执行；repository 读写有隔离数据库集成测试。
- 完成条件：领域层不泄漏驱动特有类型；金额字段用 decimal。

### BE-003 组合估值与快照模型 — P1
- 依赖：BE-002、DEC-002
- 输出：`Position`、`CashBalance`、`ValuationSnapshot`、`PerformanceSnapshot` 落地；支持原币值+汇率+基础货币折算；按 DEC-002 日切生成净值快照。
- 测试：给定持仓/现金/汇率/价格，逐项对账净值；汇率缺失时不产出伪精确净值。
- 完成条件：可生成组合当前配置、现金比例、净值。

### BE-004 行情服务集成契约 — P1
- 依赖：BE-001；行情服务查询 API（**部分具备，需先补缺口，见 BE-004a**）
- 输出：`adapters/market_data` 客户端调用 `market-info-service` 的最新价/K 线/instrument 查询；采集资产范围由**组合成员/持仓/基准**驱动并同步给行情服务的订阅。
- 测试：契约测试（对 fake/录制响应）；采集范围随组合成员变化正确更新。
- 完成条件：两服务通过 API 契约解耦，不共享数据库表（见项目规范 §2）。

#### BE-004a 行情服务补齐精度字段与批量查询 — P1（BE-004 前置，market-info-service 侧改动）
- 依赖：无（可与 BE-001~003 并行，属于行情服务的最小必要改动，不是「深化外围」）
- 背景：核对 `07_api_and_admin_ui.md` 发现现有 `GET /instruments` 按单个 `asset_code` 查询，且响应示例未包含 `price_scale`/`quantity_scale`/`lot_size`/`min_quantity`——这些字段已在 `02_domain_model.md` §6.2 的 `Instrument` 领域模型中定义，但未确认已通过 API 暴露，也不支持按 `instrument_id` 批量查询，不满足 DEC-003 的「批量端点 + fail-closed」要求。
- 输出：
  1. `/instruments` 响应补充 `price_scale`/`quantity_scale`/`lot_size`/`min_quantity` 字段（数据已在库中建模，只需暴露）。
  2. 新增按 `instrument_id` 列表批量查询精度字段的端点（或扩展现有端点支持多值查询参数），供 `backend` 一次性获取 NVDA/QQQ/BTC-USDT 三者的精度规则。
  3. 采集并写入 NVDA、QQQ、BTC-USDT 三个 Instrument 的真实 `price_scale`/`quantity_scale`/`lot_size`/`min_quantity` 值（DEC-003 遗留待办）。
- 测试：API 契约测试覆盖批量查询与字段完整性；三个标的的精度值可查询到且非空。
- 完成条件：`backend` 的 BE-004 客户端能一次调用拿到三个标的的完整精度规则，无需在 Python 侧硬编码或多次往返。

## 退出条件（RM1）

领域骨架就位且旧接口不回归；组合估值/持仓/现金/汇率快照可生成；行情采集范围由组合驱动；核心领域包具备单测；BE-004a 完成后 NVDA/QQQ/BTC-USDT 三个标的的精度规则可通过行情服务 API 一次性查询到。
