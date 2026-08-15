"""Fill-price, slippage, and commission simulation (BT-003), pure and infra-free.

Implements `doc/technical/08_backtest_engine.md` §6's matching rules:
"买入：使用下一交易日开盘价或当日收盘价，加上滑点" / "卖出：...减去滑点" and the
commission floor (`commission_pct` of notional, or `min_commission`,
whichever is larger).

**Fill-price timing choice (documented per the task brief): next-day open,
not same-day close.** Using the same day's close both to *decide* a trade
(from that day's indicators/strategy signal) and to *execute* it is a
textbook look-ahead bias -- the decision is only actually knowable after
that day's market close, so the earliest realistic execution price is the
next trading day's open. ``application.backtest.engine`` is responsible for
supplying `next_day_open` as the ``base_price`` here (this module has no
day-sequencing knowledge of its own, it only applies slippage/commission to
whatever base price it is given); see that module's docstring for how it
threads a decision made on day D into an execution on day D+1, and the
explicit last-day-of-range edge case (a decision with no next day to execute
against is never silently dropped -- it is recorded as a skipped order with
an explicit reason).
"""

from __future__ import annotations

from decimal import Decimal
from typing import Literal

Side = Literal["buy", "sell"]


def apply_slippage(base_price: Decimal, *, side: Side, slippage_pct: Decimal) -> Decimal:
    """Add slippage for a buy, subtract it for a sell (§6).

    Raises ``ValueError`` if ``base_price`` is not positive or
    ``slippage_pct`` is negative -- both are caller/configuration bugs, not
    ordinary matching outcomes.
    """
    if base_price <= Decimal("0"):
        raise ValueError(f"base_price must be positive, got {base_price}")
    if slippage_pct < Decimal("0"):
        raise ValueError(f"slippage_pct must be non-negative, got {slippage_pct}")

    if side == "buy":
        return base_price * (Decimal("1") + slippage_pct)
    return base_price * (Decimal("1") - slippage_pct)


def compute_commission(
    *, notional: Decimal, commission_pct: Decimal, min_commission: Decimal
) -> Decimal:
    """``max(notional * commission_pct, min_commission)`` -- the §4 commission floor.

    Raises ``ValueError`` for negative inputs (configuration bugs).
    """
    if notional < Decimal("0"):
        raise ValueError(f"notional must be non-negative, got {notional}")
    if commission_pct < Decimal("0"):
        raise ValueError(f"commission_pct must be non-negative, got {commission_pct}")
    if min_commission < Decimal("0"):
        raise ValueError(f"min_commission must be non-negative, got {min_commission}")

    return max(notional * commission_pct, min_commission)


__all__ = ["Side", "apply_slippage", "compute_commission"]
