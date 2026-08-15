"""Minimal ``PerformanceSnapshot`` computation from a ``ValuationSnapshot`` series (BE-003).

Pure function, zero infrastructure imports (enforced by
``tests/domain/test_no_infra_leakage.py``) -- it only consumes already-loaded
``ValuationSnapshot`` dataclasses and returns a ``PerformanceSnapshot``. BE-003
was asked to make the ``performance_snapshots`` table writable and the
dataclass exist, *not* to build a full performance-report generator (that is
explicitly BT-006/BT-007's job per the roadmap); this module intentionally
computes only the two metrics that need no external assumptions:

- ``total_return_pct``: simple (not annualized) return from the first to the
  last snapshot's ``net_asset_value`` in the series.
- ``max_drawdown_pct``: the largest peak-to-trough decline (as a
  non-positive fraction) across the series, using a running peak.

``annualized_volatility`` is computed too (stdev of day-over-day NAV returns,
annualized by ``sqrt(365)`` to match DEC-002's UTC-calendar-day, 365-day/year
snapshot cadence rather than a 252-trading-day convention) whenever at least
two return observations (three snapshots) are available; below that it is
``None`` rather than a meaningless single-sample "0".

``sharpe_ratio``, ``sortino_ratio``, and ``benchmark_return_pct`` are always
``None`` from this function -- deliberately left unimplemented, not
half-implemented with an invented assumption:

- ``sharpe_ratio``/``sortino_ratio`` need a risk-free-rate (and, for Sortino,
  a minimum-acceptable-return) input that does not exist anywhere in this
  codebase yet; guessing one (e.g. hardcoding 0%) would be exactly the kind
  of fabricated financial precision value the project's decision-workflow
  guidance forbids.
- ``benchmark_return_pct`` needs a second ``ValuationSnapshot`` series for
  the portfolio's benchmark asset, which this function's signature does not
  accept (BE-003's ``ValuationService`` only produces snapshots for a single
  portfolio at a time; wiring a benchmark comparison is a reporting-layer
  concern for BT-006/BT-007).

All series comparisons require the snapshots to share the same
``base_currency`` and be sorted by ``valuation_date`` -- callers with a mixed-
currency or unsorted series get a ``ValueError`` rather than a silently wrong
result.
"""

from __future__ import annotations

from collections.abc import Sequence
from datetime import date
from decimal import Decimal
from statistics import StatisticsError, stdev
from uuid import UUID

from .models import PerformanceSnapshot, ValuationSnapshot

_ZERO = Decimal("0")
_MIN_RETURNS_FOR_VOLATILITY = 2
_CALENDAR_DAYS_PER_YEAR = Decimal("365")


def compute_performance_snapshot(
    portfolio_id: UUID,
    as_of: date,
    snapshots: Sequence[ValuationSnapshot],
) -> PerformanceSnapshot:
    """Compute a minimal ``PerformanceSnapshot`` from a chronological snapshot series.

    ``snapshots`` must be non-empty, sorted ascending by ``valuation_date``,
    all belong to ``portfolio_id``, and share one ``base_currency`` --
    violations raise ``ValueError`` rather than silently ignoring the
    offending entries. ``as_of`` is caller-supplied (not necessarily the
    last snapshot's date) so a caller can label the computation with today's
    date even if the series itself lags behind.
    """
    if not snapshots:
        raise ValueError("snapshots must not be empty")

    for snap in snapshots:
        if snap.portfolio_id != portfolio_id:
            raise ValueError(
                f"snapshot for portfolio_id={snap.portfolio_id} does not match "
                f"requested portfolio_id={portfolio_id}"
            )
    base_currency = snapshots[0].base_currency
    for snap in snapshots:
        if snap.base_currency != base_currency:
            raise ValueError(
                f"mixed base_currency in snapshots series: {base_currency!r} vs "
                f"{snap.base_currency!r}; cannot compare NAVs across currencies"
            )
    for earlier, later in zip(snapshots, snapshots[1:], strict=False):
        if later.valuation_date < earlier.valuation_date:
            raise ValueError(
                "snapshots must be sorted ascending by valuation_date, got "
                f"{earlier.valuation_date} before {later.valuation_date}"
            )

    navs = [snap.net_asset_value for snap in snapshots]

    total_return_pct = _total_return(navs)
    max_drawdown_pct = _max_drawdown(navs)
    annualized_volatility = _annualized_volatility(navs)

    return PerformanceSnapshot(
        portfolio_id=portfolio_id,
        as_of=as_of,
        total_return_pct=total_return_pct,
        max_drawdown_pct=max_drawdown_pct,
        annualized_volatility=annualized_volatility,
        sharpe_ratio=None,
        sortino_ratio=None,
        benchmark_return_pct=None,
    )


def _total_return(navs: Sequence[Decimal]) -> Decimal | None:
    """``(last / first) - 1``, or ``None`` if the starting NAV is 0 (undefined return)."""
    first, last = navs[0], navs[-1]
    if first == _ZERO:
        return None
    return (last / first) - Decimal("1")


def _max_drawdown(navs: Sequence[Decimal]) -> Decimal | None:
    """Largest peak-to-trough decline as a non-positive fraction (0 if never declined)."""
    running_peak: Decimal | None = None
    worst: Decimal = _ZERO
    for nav in navs:
        if nav < _ZERO:
            # Not expected (NAV should never be negative for a long-only,
            # no-margin portfolio -- CONTRACT-004's `quantity >= 0`
            # constraint), but drawdown against a non-positive peak is
            # mathematically undefined, so fail loud rather than emit a
            # nonsensical ratio.
            raise ValueError(f"cannot compute drawdown against a negative NAV: {nav}")
        if running_peak is None or nav > running_peak:
            running_peak = nav
        if running_peak == _ZERO:
            continue
        drawdown = (nav / running_peak) - Decimal("1")
        if drawdown < worst:
            worst = drawdown
    return worst


def _annualized_volatility(navs: Sequence[Decimal]) -> Decimal | None:
    """Stdev of day-over-day NAV returns, annualized by sqrt(365) (DEC-002 calendar-day cadence).

    ``None`` when fewer than ``_MIN_RETURNS_FOR_VOLATILITY`` return
    observations are available (i.e. fewer than 3 snapshots), or when any
    day-over-day base NAV is 0 (return undefined).
    """
    returns: list[Decimal] = []
    for earlier, later in zip(navs, navs[1:], strict=False):
        if earlier == _ZERO:
            return None
        returns.append((later / earlier) - Decimal("1"))

    if len(returns) < _MIN_RETURNS_FOR_VOLATILITY:
        return None

    try:
        # statistics.stdev operates on floats internally regardless of
        # Decimal input; acceptable here because volatility is an estimate,
        # not a ledger amount -- the repo's "Decimal, never float for money"
        # rule (python-backend-standards.md §1) governs amounts/prices, and
        # this is neither: it is re-quantized back to Decimal on return.
        daily_stdev = Decimal(str(stdev(float(r) for r in returns)))
    except StatisticsError:
        return None

    return daily_stdev * (_CALENDAR_DAYS_PER_YEAR.sqrt())


__all__ = ["compute_performance_snapshot"]
