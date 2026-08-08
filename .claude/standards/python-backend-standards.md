# 核心后端 Python 开发规范

> 适用范围：`backend/` 及后续从中拆分的 Python 领域模块与研究/回测代码。
> 规范状态：第一版，填补此前 Python 侧规范缺口，随架构升级修订。
> 最近更新：2026-08-04
> 参照：Go 服务规范 `doc/technical/market_information_service/09_go_development_standards.md`（同源原则）。

## 1. 目标与基本原则

- 与 Go 服务同源的原则：领域边界、正确性、可测试性、可观测性优先于抽象复用。
- 依赖方向从入口/基础设施指向应用与领域接口；领域层不依赖 HTTP、DB 驱动或 Provider SDK。
- 外部网络、时间、UUID、数据库、Provider、Broker 适配器等非确定性能力通过接口/参数注入，便于测试替换。
- 时间内部统一 UTC；**金额、价格、数量、权重、汇率一律用 `decimal.Decimal`，禁止用 `float` 做记账**。

## 2. 目标目录结构（架构升级方向）

`backend/app.py` 当前是历史单体原型（单文件、`http.server`、SQLite）。升级时按领域拆分：

```text
backend/
  api/                  # HTTP 路由、请求解析、响应编码
  application/          # 用例编排、事务边界
  domain/
    assets/
    portfolios/
    accounts/
    strategies/
    risk/
    execution/
    valuation/
  adapters/
    market_data/        # 调 market-info-service 的客户端
    brokers/            # 长桥等交易适配器（与行情适配器分离）
  repository/           # SQLite/PostgreSQL 数据访问
  config/               # 配置加载与校验
  observability/        # 结构化日志、指标
  app.py                # 仅进程装配与启动，保持轻薄
```

依赖约束同 Go 规范：`api` 只调 `application`，不直接写 SQL 或调 Broker SDK；`domain` 不依赖基础设施包。

## 3. 技术栈与升级路径

- **已决策（RM0 DEC-005，2026-08-05）：RM1 起点即升级到 FastAPI + PostgreSQL + Alembic**，不再继续在 stdlib `http.server` + SQLite 原型上累加代码。原 `backend/app.py`（stdlib + SQLite）保留作为历史参考，新领域代码一律基于新技术栈按 §2 目录结构展开。
- 决策依据：RM1 自身退出条件（组合估值/持仓/现金/汇率快照骨架 + 行情服务集成）已构成"正式模块"，命中下方升级触发条件，非预判性提前投入；且 `backend/app.py` 当时无 `requirements.txt`/`pyproject.toml` 等可保留资产，是切换成本最低的时间点。详见 `doc/technical/roadmap/01_decisions.md` DEC-005。
- **升级触发条件**（历史记录，已全部或部分命中并触发上述决策）：需并发写入、需与 Go 服务共用 PostgreSQL（各自独立实例/角色，不共享表）、需引入策略/风控/执行/回测正式模块、需稳定 API 契约给前端。
- **技术栈**：Web 框架 FastAPI（异步、pydantic 校验、OpenAPI）；持久化 PostgreSQL，独立数据库实例/角色，不与行情服务共享表（`project-conventions.md` §2）；迁移采用 Alembic 版本化 migration。Postgres 部署复用 `market-info-service/compose.yaml` 已验证的服务模式（独立 `POSTGRES_DB`/角色、健康检查、独立 volume）。
- 出站调用 `market-info-service` 的批量/精度查询等客户端使用 `httpx.AsyncClient`，封装在 `adapters/market_data/`。
- 保持 API 契约向后兼容，旧接口按需保留只读代理。

## 4. 代码风格

- 统一用 `ruff format`（或 `black`）+ `ruff`/`flake8` 静态检查；提交前执行。
- 全量类型注解，`mypy` 静态类型检查（核心领域包必须通过）。
- 包/模块名小写、语义化，不用 `util`、`common`、`base` 等含糊名。
- 公共函数/类有 docstring，说明输入、输出、副作用、失败路径。
- 函数单一职责；不用异常表达可预期业务分支，业务错误用显式错误类型/错误码。

## 5. 错误处理

- 自定义领域异常层次（如 `DomainError` 基类 + 稳定错误码），HTTP 层统一映射到状态码与 error envelope。
- 对外错误脱敏，不返回 SQL、堆栈、连接串、Provider token、secret、签名。
- 重试性由明确错误分类决定，不靠字符串匹配。
- 日志在可采取动作的边界记录一次，避免逐层重复。

## 6. 数据、时间与标识

- 主数据身份用内部 `asset_id`/UUID，不用可变 `symbol` 作唯一键。
- 时间用带时区的 `datetime`（UTC），API 用 RFC 3339/ISO 8601。
- decimal 数据在领域层用 `Decimal`，JSON 序列化为字符串。
- 数据库写入靠唯一约束 + 事务保证幂等，不以「先查后插」作唯一并发保护。
- SQL 一律参数绑定，禁止拼接用户输入。

## 7. 交易与风控安全约束（强制）

- **任何 Agent/自动流程不得触发真实下单、撤资、转账**；`live` 操作只由用户本人确认执行。
- 订单提交前必须先过组合级与订单级风控，并生成可审计的风控结果。
- 执行模块不生成信号、不判断买卖、不绕过风控、不自动开启实盘。
- API 超时/未知订单状态进入安全状态，禁止盲目重试下单。
- 模拟与实盘凭据隔离；`live` 需满足环境隔离约定的全部双重确认条件。

## 8. 可观测性与审计

- 结构化日志字段至少含：`timestamp`、`level`、`service`、`env`、`run_id`、`portfolio_id`、`asset_id`、`strategy_version`、`config_version`。
- 审计链路可串联：分析结果 → 策略信号 → 目标权重 → 调仓计划 → 风控 → 人工确认 → 订单 → 成交 → 持仓快照 → 估值快照 → 复盘。
- 禁止日志输出凭证、签名原文、未限制的 Provider 响应体。

## 9. 测试规范

- 每个新增/修改的非平凡函数覆盖正常、边界、主要失败路径；优先参数化测试（`pytest.mark.parametrize`）。
- 领域计算（评分、目标权重、风控判定、单笔风险、回测撮合、对账）必须有确定性单测，注入固定时间/UUID/随机源。
- 单测不访问真实网络、真实 Provider/Broker、开发者数据库；用 fake/`responses`/`httpx.MockTransport`/内存 SQLite。
- 回测必须**可复现**：相同输入产出相同结果，并可逐日对账资金/持仓/成交/权益。
- 修缺陷先写复现回归测试。
- 核心领域包（portfolios、risk、execution、valuation、backtest）目标 statement coverage >= 80%；一次性研究脚本可豁免。

## 10. 提交前质量门禁

```bash
ruff format --check .
ruff check .
mypy backend            # 核心领域包必须通过
pytest -q               # 全部通过
make security-check     # 仓库安全门禁
```

进入主分支前须满足：格式化/静态检查/类型检查通过、测试全绿、核心领域覆盖率达标、API/migration/配置变更同步更新文档、不含凭证/临时数据库/构建产物/编辑器文件。
