"""``BacktestConfig`` (BT-003): the event-driven backtest run's configuration.

Field defaults transcribe `doc/technical/08_backtest_engine.md` §4's example
YAML (``initial_cash: 2500``, ``base_currency: USD``, ``commission_pct:
0.001``, ``min_commission: 0``, ``slippage_pct: 0.001``,
``precision_mode: unrestricted``) -- documented defaults a caller can
override, not values this module invents.

``rebalance_threshold_pct`` has no documented value anywhere in ``doc/`` --
this is a genuine BT-003 judgment call (flagged in the task report, mirroring
how ``domain.risk.simple_risk_policy`` flags ``DEFAULT_MAX_HELD_POSITIONS``
the same way): 1% of NAV is picked as a conservative default that avoids
churning on sub-1%-of-NAV noise trades (which the commission floor alone
could easily eat into for this project's small, ~$2,500 example initial
cash) while still being small enough not to meaningfully suppress real
rebalancing decisions.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import date
from decimal import Decimal
from typing import Literal
from uuid import UUID

from .errors import InvalidBacktestConfigError

PrecisionMode = Literal["unrestricted", "restricted"]

_ZERO = Decimal("0")

# doc/technical/08_backtest_engine.md §4 example values -- documented
# defaults, not invented here.
DEFAULT_INITIAL_CASH = Decimal("2500")
DEFAULT_BASE_CURRENCY = "USD"
DEFAULT_COMMISSION_PCT = Decimal("0.001")
DEFAULT_MIN_COMMISSION = Decimal("0")
DEFAULT_SLIPPAGE_PCT = Decimal("0.001")
DEFAULT_PRECISION_MODE: PrecisionMode = "unrestricted"

# Not documented in doc/ -- a BT-003 judgment call, see module docstring.
DEFAULT_REBALANCE_THRESHOLD_PCT = Decimal("0.01")


@dataclass(frozen=True)
class BacktestConfig:
    """Configuration for one event-driven backtest run.

    Validated eagerly at construction (fail-closed, mirrors
    ``domain.risk.simple_risk_policy.SimpleRiskPolicy``'s constructor
    pattern) rather than surfacing a confusing failure partway through the
    day loop.
    """

    portfolio_id: UUID
    start_date: date
    end_date: date
    initial_cash: Decimal = DEFAULT_INITIAL_CASH
    base_currency: str = DEFAULT_BASE_CURRENCY
    commission_pct: Decimal = DEFAULT_COMMISSION_PCT
    min_commission: Decimal = DEFAULT_MIN_COMMISSION
    slippage_pct: Decimal = DEFAULT_SLIPPAGE_PCT
    precision_mode: PrecisionMode = DEFAULT_PRECISION_MODE
    rebalance_threshold_pct: Decimal = DEFAULT_REBALANCE_THRESHOLD_PCT

    def __post_init__(self) -> None:
        if self.start_date > self.end_date:
            raise InvalidBacktestConfigError(
                f"start_date {self.start_date} must not be after end_date {self.end_date}"
            )
        if self.initial_cash <= _ZERO:
            raise InvalidBacktestConfigError(
                f"initial_cash must be positive, got {self.initial_cash}"
            )
        if not self.base_currency:
            raise InvalidBacktestConfigError("base_currency must not be empty")
        for name, value in (
            ("commission_pct", self.commission_pct),
            ("min_commission", self.min_commission),
            ("slippage_pct", self.slippage_pct),
            ("rebalance_threshold_pct", self.rebalance_threshold_pct),
        ):
            if value < _ZERO:
                raise InvalidBacktestConfigError(f"{name} must be non-negative, got {value}")
        if self.precision_mode not in ("unrestricted", "restricted"):
            raise InvalidBacktestConfigError(
                f"precision_mode={self.precision_mode!r} must be 'unrestricted' or 'restricted'"
            )


__all__ = [
    "DEFAULT_BASE_CURRENCY",
    "DEFAULT_COMMISSION_PCT",
    "DEFAULT_INITIAL_CASH",
    "DEFAULT_MIN_COMMISSION",
    "DEFAULT_PRECISION_MODE",
    "DEFAULT_REBALANCE_THRESHOLD_PCT",
    "DEFAULT_SLIPPAGE_PCT",
    "BacktestConfig",
    "PrecisionMode",
]
