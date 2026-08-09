# 契约冻结与领域地基实施计划

> 对应里程碑：M0（契约冻结基线）、M1（后端地基核心）。
> 依赖：RM0 全部决策（DEC-001/002/003/005，已 DONE）。

## 0. 契约冻结（M0）

以下产出是**接口签名/表结构草案**，不是完整实现；冻结后才能进入 W2 及以后的并行任务。

| ID | 状态 | 输入 | 输出与完成条件 | 测试/验证 |
| --- | --- | --- | --- | --- |
| CONTRACT-001 | DONE（已冻结，2026-08-05） | `02_backend_foundation.md` BE-001 子包职责表 | 冻结 `assets`/`portfolios`/`accounts`/`strategies`/`risk`/`execution`/`valuation` 七个子包的对外接口方法签名（不含实现），明确各自输入输出 DTO | 签名评审通过；不引入循环依赖（`execution`→`risk`→`portfolios`/`valuation`；`strategies`→`assets`/`portfolios`/`valuation` 只读） |
| CONTRACT-002 | DONE（已冻结，2026-08-05） | 需求文档 §5.4、BT-004 | 冻结 `Strategy` 接口签名：输入分析结果 + 组合状态，输出资产评分/目标权重/调仓建议；不含下单方法 | 签名评审确认「只输出建议、不提交订单」（§2.3 研究与执行分离） |
| CONTRACT-003 | DONE（已冻结，2026-08-05） | 需求文档、BT-005 | 冻结 `RiskPolicy` 接口签名：组合级/订单级检查方法，返回可审计的结构化风控结果（通过/拒绝/原因） | 签名评审确认可被回测与 paper/live 复用（同一签名，不同环境注入不同上下文） |
| CONTRACT-004 | DONE（已冻结，2026-08-05） | BE-002、需求文档 §5 领域模型 | 冻结 PostgreSQL 初版 DDL 表清单与命名：`assets`/`portfolios`/`portfolio_members`/`account_bindings`/`positions`/`cash_balances`/`valuation_snapshots`/`performance_snapshots`/`orders`/`fills` | DDL 草案评审通过；金额字段为 `numeric`，UUID 为主键，时间为带时区 `timestamptz` |
| CONTRACT-005 | DONE（已冻结，2026-08-05） | DEC-003、BE-004a | 冻结行情服务批量精度查询 API 的请求/响应 schema（按 `instrument_id` 列表批量返回 `price_scale`/`quantity_scale`/`lot_size`/`min_quantity`），详见 `02_portfolio_and_market_integration.md` §0 | 契约评审通过；与现有 `/instruments` 响应风格一致（decimal 字段序列化为字符串） |

DDL 草案在尚无共享环境时可以是一个 migration 版本，但内容必须按 CONTRACT-001 冻结的领域边界组织；进入共享环境后只允许新增 migration，不得修改历史文件（同 `project-conventions.md` §1）。

CONTRACT-001~005 已于 2026-08-05 经确认冻结；此后修改需走变更流程（记录变更原因与影响面），不得直接改签名。

## 0.1 CONTRACT-001 领域子包接口签名（草案）

```python
# --- assets ---
@dataclass(frozen=True)
class Asset:
    asset_id: str  # 例如 "equity:nasdaq:NVDA"，唯一性不依赖 symbol
    asset_type: Literal["STOCK", "ETF", "CRYPTO", "CASH"]
    symbol: str
    venue: str
    quote_currency: str
    provider_symbols: dict[str, str]
    trading_status: Literal["watch", "tradable", "paused", "blacklist"]

class AssetRepository(Protocol):
    def get(self, asset_id: str) -> Asset: ...
    def list_by_ids(self, asset_ids: Sequence[str]) -> list[Asset]: ...


# --- portfolios ---
@dataclass(frozen=True)
class Portfolio:
    portfolio_id: UUID
    name: str
    base_currency: str
    benchmark_asset_id: str | None
    risk_level: str
    execution_mode: Literal["research", "backtest", "paper", "live"]
    status: Literal["draft", "active", "paused", "archived"]

@dataclass(frozen=True)
class PortfolioMember:
    portfolio_id: UUID
    asset_id: str
    member_status: Literal["candidate", "approved", "held", "restricted"]
    target_weight_min: Decimal | None
    target_weight_max: Decimal | None

class PortfolioRepository(Protocol):
    def get(self, portfolio_id: UUID) -> Portfolio: ...
    def list_members(self, portfolio_id: UUID, status: str | None = None) -> list[PortfolioMember]: ...


# --- accounts ---
@dataclass(frozen=True)
class AccountBinding:
    account_binding_id: UUID
    portfolio_id: UUID
    broker_code: str  # "longbridge" | "bybit"
    environment: Literal["research", "backtest", "paper", "live"]
    credential_ref: str  # 仅引用，不含密钥本身

class AccountBindingRepository(Protocol):
    def get_for_portfolio(self, portfolio_id: UUID, environment: str, broker_code: str) -> AccountBinding: ...
    def assert_no_credential_reuse(self, paper_ref: str, live_ref: str) -> None: ...


# --- valuation ---
@dataclass(frozen=True)
class Position:
    portfolio_id: UUID
    asset_id: str
    quantity: Decimal
    average_cost: Decimal

@dataclass(frozen=True)
class CashBalance:
    portfolio_id: UUID
    currency: str
    amount: Decimal

@dataclass(frozen=True)
class ValuationSnapshot:
    portfolio_id: UUID
    valuation_date: date  # UTC 自然日（DEC-002）
    positions_value: Decimal
    cash_value: Decimal
    net_asset_value: Decimal
    base_currency: str
    price_status: Literal["fresh", "stale"]  # 组合级取最保守值；明细见持仓级 price_status

@dataclass(frozen=True)
class PortfolioState:
    """strategies/risk 只读消费的组合当前状态投影，由 valuation 提供。"""
    portfolio: Portfolio
    members: tuple[PortfolioMember, ...]
    positions: tuple[Position, ...]
    cash: tuple[CashBalance, ...]
    latest_snapshot: ValuationSnapshot

class ValuationService(Protocol):
    def generate_snapshot(self, portfolio_id: UUID, as_of: date) -> ValuationSnapshot: ...
    def current_state(self, portfolio_id: UUID) -> PortfolioState: ...


# --- strategies（签名详见 0.2 CONTRACT-002） ---
# --- risk（签名详见 0.3 CONTRACT-003） ---


# --- execution ---
@dataclass(frozen=True)
class Order:
    order_id: UUID
    portfolio_id: UUID
    asset_id: str
    side: Literal["buy", "sell"]
    quantity: Decimal
    order_type: Literal["market", "limit"]
    limit_price: Decimal | None
    status: Literal[
        "pending_risk", "rejected", "submitted",
        "partially_filled", "filled", "cancelled", "unknown",
    ]

@dataclass(frozen=True)
class Fill:
    fill_id: UUID
    order_id: UUID
    quantity: Decimal
    price: Decimal
    commission: Decimal
    filled_at: datetime  # UTC

class ExecutionService(Protocol):
    def submit(self, order: Order, risk_result: RiskCheckResult) -> Order:
        """risk_result.approved 为 False 时必须拒绝，不生成订单（python-backend-standards.md §7）。"""
```

依赖方向确认（修订 `02_backend_foundation.md` BE-001 表格中的表述）：`execution` → `risk` → `portfolios`/`valuation`；`strategies` → `assets`/`portfolios`/`valuation`（只读，通过 `PortfolioState`）；`valuation` → `assets`/`accounts`（汇率、账户维度）。均为领域内部依赖，不依赖 `api`/`adapters`/`repository`。

## 0.2 CONTRACT-002 Strategy 接口签名（草案）

```python
@dataclass(frozen=True)
class AssetScore:
    asset_id: str
    score: Decimal            # 量纲由具体策略定义并在策略文档中说明，接口本身不假设范围
    reasons: tuple[str, ...]

@dataclass(frozen=True)
class StrategyOutput:
    portfolio_id: UUID
    as_of: date
    asset_scores: tuple[AssetScore, ...]
    target_weights: dict[str, Decimal]   # asset_id -> weight，含现金（如 "cash:USD"），之和必须为 1
    rebalance_notes: tuple[str, ...]

class Strategy(Protocol):
    strategy_id: str
    version: str  # 策略版本化（需求文档 §5.4）

    def generate_targets(
        self,
        analysis: AnalysisResult,       # BT-002 指标计算库输出
        portfolio_state: PortfolioState,
    ) -> StrategyOutput:
        """只输出评分/目标权重/调仓建议，不提交订单、不做风控判断。"""
```

约束：
- `target_weights` 之和必须为 1；不满足时策略层直接拒绝生成（`ValueError`/领域异常），不交给风控裁决——策略层输入校验与风控层输出校验是两道独立关卡。
- `member_status == "restricted"` 或 `"blacklist"`（`trading_status`）的资产，其目标权重不得高于当前持仓权重（可维持或降低，不可新增买入）。
- `analysis` 中对应资产的关键技术指标缺失时，不对该资产生成新增买入信号。
- 同一 `Strategy` 实例调用必须无副作用、结果确定：相同 `analysis` + `portfolio_state` 输入产出相同 `StrategyOutput`，这是回测可复现性与「回测/paper 复用同一策略代码」的前提。

## 0.3 CONTRACT-003 RiskPolicy 接口签名（草案）

```python
@dataclass(frozen=True)
class RiskCheckResult:
    approved: bool
    rejection_reasons: tuple[str, ...]
    checked_rules: tuple[str, ...]   # 审计：实际执行了哪些规则
    context: dict[str, Any]          # 可审计上下文（如触发阈值的具体数值），不含敏感信息

@dataclass(frozen=True)
class OrderIntent:
    portfolio_id: UUID
    asset_id: str
    side: Literal["buy", "sell"]
    quantity: Decimal
    estimated_price: Decimal

class RiskPolicy(Protocol):
    def check_order(self, intent: OrderIntent, portfolio_state: PortfolioState) -> RiskCheckResult: ...
    def check_target_weights(
        self, target_weights: dict[str, Decimal], portfolio_state: PortfolioState
    ) -> RiskCheckResult: ...
    def replaced_checks(self) -> tuple[str, ...]:
        """研究/回测环境下用替代实现代替的检查列表（如交易时间、人工确认），须在报告中标注。"""
```

约束：
- `check_order`/`check_target_weights` 在 `backtest`/`paper`/`live` 三个环境下签名完全一致；只允许通过 `portfolio_state`/配置区分环境相关阈值，不允许分环境改变方法签名或新增分支参数。
- `RiskCheckResult.approved is False` 时，`execution.ExecutionService.submit` 必须拒绝生成订单，这是不可绕过的强约束（`python-backend-standards.md` §7）。

## 0.4 CONTRACT-004 PostgreSQL 初版 DDL 表清单（草案）

| 表 | 关键列 | 约束要点 |
| --- | --- | --- |
| `assets` | `asset_id text PK`、`asset_type`、`symbol`、`venue`、`quote_currency`、`provider_symbols jsonb`、`trading_status`、`created_at`/`updated_at timestamptz` | `asset_id` 格式校验：`asset_type=CASH` 用两段 `cash:CURRENCY`（如 `cash:USD`，现金没有交易场所概念）；其余类型用三段 `type:venue:symbol`（如 `equity:nasdaq:NVDA`）。两种格式均已是 `project-conventions.md` §4 的既有约定，非新决策，2026-08-05 补充明确写入 DDL 校验规则，之前只在此处遗漏 |
| `portfolios` | `portfolio_id uuid PK`、`name`、`base_currency`、`benchmark_asset_id text FK→assets NULL`、`risk_level`、`execution_mode`、`status`、时间戳 | `execution_mode`/`status` 用 `CHECK` 约束枚举值 |
| `portfolio_members` | `portfolio_id FK`、`asset_id FK`、`member_status`、`target_weight_min numeric(9,6)`、`target_weight_max numeric(9,6)`、`added_at` | `PRIMARY KEY(portfolio_id, asset_id)` |
| `account_bindings` | `account_binding_id uuid PK`、`portfolio_id FK`、`broker_code`、`environment`、`credential_ref text`、`created_at` | `credential_ref` 只存引用；`UNIQUE(portfolio_id, broker_code, environment)` |
| `positions` | `portfolio_id FK`、`asset_id FK`、`quantity numeric(38,18)`、`average_cost numeric(38,18)`、`updated_at` | `PRIMARY KEY(portfolio_id, asset_id)`；`quantity >= 0`（不支持做空，与非目标一致） |
| `cash_balances` | `portfolio_id FK`、`currency`、`amount numeric(38,18)`、`updated_at` | `PRIMARY KEY(portfolio_id, currency)` |
| `valuation_snapshots` | `valuation_snapshot_id uuid PK`、`portfolio_id FK`、`valuation_date date`、`positions_value`/`cash_value`/`net_asset_value numeric(38,18)`、`base_currency`、`price_status`、`created_at` | `UNIQUE(portfolio_id, valuation_date)`；`valuation_date` 按 UTC 自然日（DEC-002） |
| `performance_snapshots` | `performance_snapshot_id uuid PK`、`portfolio_id FK`、`as_of date`、`total_return_pct`/`max_drawdown_pct`/`annualized_volatility`/`sharpe_ratio`/`sortino_ratio`/`benchmark_return_pct numeric`、`created_at` | `UNIQUE(portfolio_id, as_of)` |
| `orders` | `order_id uuid PK`、`portfolio_id FK`、`asset_id FK`、`side`、`quantity numeric(38,18)`、`order_type`、`limit_price numeric(38,18) NULL`、`status`、`risk_check_result jsonb`、时间戳 | `status` 用 `CHECK` 约束枚举值，含 `unknown`（超时/未知状态，见 python-backend-standards.md §7） |
| `fills` | `fill_id uuid PK`、`order_id FK`、`quantity numeric(38,18)`、`price numeric(38,18)`、`commission numeric(38,18)`、`filled_at timestamptz` | `quantity > 0` |

数值字段统一 `numeric(38,18)`，与 `market-info-service` 的 `lot_size` 精度约定一致（`03_database.md`）；权重类字段用 `numeric(9,6)`（足够表示 0~1 的六位小数精度）。所有金额/数量在应用层用 `Decimal` 读写，禁止 `float`。

## 1. BE-001 领域分包骨架

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| BE-001 | DONE（2026-08-05） | DEC-005、CONTRACT-001（已冻结） | 按 `api/application/domain/adapters/repository/config/observability` 拆分 `backend/`；`app.py` 只保留进程装配；七个领域子包按冻结签名建立骨架（可先返回 `NotImplementedError` 占位） | 现有认证、组合 CRUD、成员管理接口回归通过（**已明确延后到 BE-002/BE-003 落地后**）；目录结构符合 `python-backend-standards.md` §2 ✅；`mypy`/`ruff`/`pytest` 全过（57 tests）✅ |

## 2. BE-002 持久化落地

| ID | 状态 | 依赖 | 输出 | 测试与完成条件 |
| --- | --- | --- | --- | --- |
| BE-002 | DONE（2026-08-05） | DEC-005、BE-001（已完成）、CONTRACT-004（已冻结） | PostgreSQL + Alembic 版本化 migration + 连接池；独立数据库实例/角色，不与行情服务共享表；`backend/compose.yaml` 复用 `market-info-service/compose.yaml` 模式；`AssetRepository`/`PortfolioRepository`/`AccountBindingRepository` 三个已冻结接口的具体实现（`positions`/`cash_balances`/`valuation_snapshots`/`performance_snapshots`/`orders`/`fills` 仅建表，服务层逻辑留给 BE-003/未来的执行任务） | migration 在真实 Postgres（本地 colima）上验证可重复执行 ✅；repository 集成测试针对真实 Postgres（138 passed，无 DB 时 18 个集成测试正确 skip）✅；`domain/` 零 SQLAlchemy/DB 依赖（`test_no_infra_leakage.py` 仍过）✅；金额字段 `Decimal` ✅ |
| **BE-002 遗留的契约问题 — 整合负责人裁定结果（2026-08-05）** | — | — | 1) **同步 vs 异步**：维持 CONTRACT-001 三个 Repository Protocol 为同步方法，不改冻结签名——单用户个人平台，数据量小，FastAPI 对同步端点自动走线程池，不需要为此引入复杂度；已建的异步连接池基础设施予以保留（成本低、BE-003/BE-004 大概率会用到 async httpx 调用行情服务，即使不直接用于 DB 也不亏），但若 BE-003/BE-004 完成后确认用不到，应删除而非无限期闲置。2) **`asset_id` 格式**：非新问题，`project-conventions.md` §4 早已约定 `cash:USD` 两段、`equity:nasdaq:NVDA` 三段并存；已在上表 `assets` 行写清规则，CONTRACT-004 DDL 描述之前遗漏，非需要重新决策。3) **`broker_code` 参数缺失**：**已确认是真实设计缺陷，非边缘情况**——首批标的清单本身就要求同一 `paper` 组合同时绑定 longbridge（NVDA/QQQ）和 bybit（BTC-USDT），"多 broker 绑定"是必然会发生的正常场景，不是极端输入。已修正 `AccountBindingRepository.get_for_portfolio` 签名为 `(portfolio_id, environment, broker_code)`（`backend/domain/accounts/models.py`、`repository/accounts.py`、对应测试同步更新），并在真实 Postgres（本地 colima）上重新验证 138 个测试全过。`AmbiguousBindingError` 语义相应改为"schema 唯一约束被绕过时的兜底报错"，不再是正常业务路径。 | — |

## 3. M1 退出门禁

- 从空库可一条命令启动 PostgreSQL 并执行全部 migration。
- migration 与 CONTRACT-004 冻结的表清单无遗漏。
- 领域层（`domain/`）不依赖 HTTP、DB 驱动或 Provider SDK。
- 现有认证、组合 CRUD、成员管理接口回归通过，行为不倒退。
- `ruff format --check`、`ruff check`、`mypy backend`、`pytest -q` 全部通过。
