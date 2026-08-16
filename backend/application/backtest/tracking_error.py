"""Target-weight tracking error (BT-003a).

Quantifies how far a backtest's *actual* per-asset weight drifted from the
strategy's *target* weight, day over day -- the concrete metric
`doc/technical/implementation_plan/04_backtest_matching_strategy_risk.md`
§3.1 (BT-003a) exists to compute, to compare `precision_mode="unrestricted"`
against `precision_mode="restricted"` (whole-share rounding) for the same
historical run.

Built entirely from ``BacktestResult.daily_detail`` (2026-08-16, BT-006a) and
``.equity_curve`` -- both already carry everything needed (``target_weights``,
per-asset ``position_values``, ``net_asset_value``); this module adds no new
computation elsewhere and makes no market-data access itself, matching
``metrics.py``/``reconciliation.py``'s existing "downstream consumer of the
not-frozen ``BacktestResult`` contract" pattern -- it lives alongside them in
``application.backtest``, not in the lower-level, infra-free ``backtest``
package, since ``backtest.*`` modules must not depend on ``application.*``
(the dependency runs the other way).

Definition
----------
For each UTC calendar day and each asset present in that day's
``target_weights`` (excluding the cash key, which has no "position" to
compare against) or ``position_values``:

    actual_weight = position_values.get(asset_id, 0) / net_asset_value
    error         = actual_weight - target_weights.get(asset_id, 0)

``mean_absolute_error`` per asset is the mean of ``abs(error)`` across every
day in the run (days where NAV is not positive are skipped -- there is no
meaningful weight to compare against a non-positive NAV). This is a simple,
standard tracking-error definition (mean absolute deviation of actual vs.
target allocation) -- not annualized, not risk-adjusted; it answers "on a
typical day, how far off was this asset's weight from what the strategy
wanted," which is exactly what whole-share rounding at a small account size
is expected to worsen.
"""

from __future__ import annotations

from dataclasses import dataclass
from decimal import Decimal

from .models import BacktestResult

_ZERO = Decimal("0")
_CASH_PREFIX = "cash:"


@dataclass(frozen=True)
class AssetTrackingError:
    """One asset's tracking-error summary across a full backtest run."""

    asset_id: str
    mean_absolute_error: Decimal
    max_absolute_error: Decimal
    days_considered: int


def compute_tracking_error(result: BacktestResult) -> dict[str, AssetTrackingError]:
    """Per-asset mean/max absolute weight tracking error across ``result``'s full run.

    Returns an empty dict if ``result.daily_detail`` is empty (nothing to
    compute from -- see ``DailyPortfolioDetail``'s backward-compatibility
    note) rather than raising, since an older/hand-built ``BacktestResult``
    legitimately may not carry this data.
    """
    nav_by_date = {s.valuation_date: s.net_asset_value for s in result.equity_curve}

    errors_by_asset: dict[str, list[Decimal]] = {}
    for detail in result.daily_detail:
        nav = nav_by_date.get(detail.valuation_date)
        if nav is None or nav <= _ZERO:
            continue

        asset_ids = {
            asset_id
            for asset_id in (*detail.target_weights, *detail.position_values)
            if not asset_id.startswith(_CASH_PREFIX)
        }
        for asset_id in asset_ids:
            actual_weight = detail.position_values.get(asset_id, _ZERO) / nav
            target_weight = detail.target_weights.get(asset_id, _ZERO)
            errors_by_asset.setdefault(asset_id, []).append(abs(actual_weight - target_weight))

    return {
        asset_id: AssetTrackingError(
            asset_id=asset_id,
            mean_absolute_error=sum(errors, start=_ZERO) / Decimal(len(errors)),
            max_absolute_error=max(errors),
            days_considered=len(errors),
        )
        for asset_id, errors in errors_by_asset.items()
    }


__all__ = ["AssetTrackingError", "compute_tracking_error"]
