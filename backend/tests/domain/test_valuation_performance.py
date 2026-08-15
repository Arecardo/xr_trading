"""Unit tests for domain.valuation.performance.compute_performance_snapshot (BE-003)."""

from __future__ import annotations

from datetime import date
from decimal import Decimal
from uuid import UUID, uuid4

import pytest

from domain.valuation.models import PerformanceSnapshot, ValuationSnapshot
from domain.valuation.performance import compute_performance_snapshot


def _snapshot(
    portfolio_id: UUID, valuation_date: date, nav: str, *, base_currency: str = "USD"
) -> ValuationSnapshot:
    return ValuationSnapshot(
        portfolio_id=portfolio_id,
        valuation_date=valuation_date,
        positions_value=Decimal(nav),
        cash_value=Decimal("0"),
        net_asset_value=Decimal(nav),
        base_currency=base_currency,
        price_status="fresh",
    )


class TestComputePerformanceSnapshot:
    def test_returns_performance_snapshot_dataclass(self) -> None:
        portfolio_id = uuid4()
        snapshots = [
            _snapshot(portfolio_id, date(2026, 8, 1), "1000"),
            _snapshot(portfolio_id, date(2026, 8, 2), "1100"),
        ]

        result = compute_performance_snapshot(portfolio_id, date(2026, 8, 2), snapshots)

        assert isinstance(result, PerformanceSnapshot)
        assert result.portfolio_id == portfolio_id
        assert result.as_of == date(2026, 8, 2)

    def test_total_return_pct_is_simple_first_to_last_return(self) -> None:
        portfolio_id = uuid4()
        snapshots = [
            _snapshot(portfolio_id, date(2026, 8, 1), "1000"),
            _snapshot(portfolio_id, date(2026, 8, 2), "1050"),
            _snapshot(portfolio_id, date(2026, 8, 3), "1100"),
        ]

        result = compute_performance_snapshot(portfolio_id, date(2026, 8, 3), snapshots)

        assert result.total_return_pct == Decimal("0.1")

    def test_total_return_pct_is_none_when_starting_nav_is_zero(self) -> None:
        portfolio_id = uuid4()
        snapshots = [
            _snapshot(portfolio_id, date(2026, 8, 1), "0"),
            _snapshot(portfolio_id, date(2026, 8, 2), "500"),
        ]

        result = compute_performance_snapshot(portfolio_id, date(2026, 8, 2), snapshots)

        assert result.total_return_pct is None

    def test_max_drawdown_pct_is_zero_for_monotonic_increase(self) -> None:
        portfolio_id = uuid4()
        snapshots = [
            _snapshot(portfolio_id, date(2026, 8, 1), "1000"),
            _snapshot(portfolio_id, date(2026, 8, 2), "1100"),
            _snapshot(portfolio_id, date(2026, 8, 3), "1200"),
        ]

        result = compute_performance_snapshot(portfolio_id, date(2026, 8, 3), snapshots)

        assert result.max_drawdown_pct == Decimal("0")

    def test_max_drawdown_pct_captures_largest_peak_to_trough_decline(self) -> None:
        portfolio_id = uuid4()
        # peak 1200 -> trough 900 is a 25% decline; later partial recovery
        # to 1000 must not overwrite the worse drawdown already recorded.
        snapshots = [
            _snapshot(portfolio_id, date(2026, 8, 1), "1000"),
            _snapshot(portfolio_id, date(2026, 8, 2), "1200"),
            _snapshot(portfolio_id, date(2026, 8, 3), "900"),
            _snapshot(portfolio_id, date(2026, 8, 4), "1000"),
        ]

        result = compute_performance_snapshot(portfolio_id, date(2026, 8, 4), snapshots)

        assert result.max_drawdown_pct == Decimal("900") / Decimal("1200") - Decimal("1")

    def test_max_drawdown_raises_on_negative_nav(self) -> None:
        portfolio_id = uuid4()
        snapshots = [
            _snapshot(portfolio_id, date(2026, 8, 1), "1000"),
            _snapshot(portfolio_id, date(2026, 8, 2), "-1"),
        ]

        with pytest.raises(ValueError, match="negative NAV"):
            compute_performance_snapshot(portfolio_id, date(2026, 8, 2), snapshots)

    def test_annualized_volatility_is_none_with_fewer_than_three_snapshots(self) -> None:
        portfolio_id = uuid4()
        snapshots = [
            _snapshot(portfolio_id, date(2026, 8, 1), "1000"),
            _snapshot(portfolio_id, date(2026, 8, 2), "1010"),
        ]

        result = compute_performance_snapshot(portfolio_id, date(2026, 8, 2), snapshots)

        assert result.annualized_volatility is None

    def test_annualized_volatility_is_positive_with_varying_returns(self) -> None:
        portfolio_id = uuid4()
        snapshots = [
            _snapshot(portfolio_id, date(2026, 8, 1), "1000"),
            _snapshot(portfolio_id, date(2026, 8, 2), "1050"),
            _snapshot(portfolio_id, date(2026, 8, 3), "980"),
            _snapshot(portfolio_id, date(2026, 8, 4), "1020"),
        ]

        result = compute_performance_snapshot(portfolio_id, date(2026, 8, 4), snapshots)

        assert result.annualized_volatility is not None
        assert result.annualized_volatility > Decimal("0")

    def test_sharpe_sortino_and_benchmark_return_are_always_none(self) -> None:
        """Explicitly out of scope for BE-003 -- see module docstring."""
        portfolio_id = uuid4()
        snapshots = [
            _snapshot(portfolio_id, date(2026, 8, 1), "1000"),
            _snapshot(portfolio_id, date(2026, 8, 2), "1050"),
            _snapshot(portfolio_id, date(2026, 8, 3), "1100"),
        ]

        result = compute_performance_snapshot(portfolio_id, date(2026, 8, 3), snapshots)

        assert result.sharpe_ratio is None
        assert result.sortino_ratio is None
        assert result.benchmark_return_pct is None

    def test_raises_on_empty_snapshots(self) -> None:
        with pytest.raises(ValueError, match="must not be empty"):
            compute_performance_snapshot(uuid4(), date(2026, 8, 1), [])

    def test_raises_on_mismatched_portfolio_id(self) -> None:
        portfolio_id = uuid4()
        other_id = uuid4()
        snapshots = [
            _snapshot(portfolio_id, date(2026, 8, 1), "1000"),
            _snapshot(other_id, date(2026, 8, 2), "1050"),
        ]

        with pytest.raises(ValueError, match="does not match"):
            compute_performance_snapshot(portfolio_id, date(2026, 8, 2), snapshots)

    def test_raises_on_mixed_base_currency(self) -> None:
        portfolio_id = uuid4()
        snapshots = [
            _snapshot(portfolio_id, date(2026, 8, 1), "1000", base_currency="USD"),
            _snapshot(portfolio_id, date(2026, 8, 2), "900", base_currency="EUR"),
        ]

        with pytest.raises(ValueError, match="mixed base_currency"):
            compute_performance_snapshot(portfolio_id, date(2026, 8, 2), snapshots)

    def test_raises_on_unsorted_snapshots(self) -> None:
        portfolio_id = uuid4()
        snapshots = [
            _snapshot(portfolio_id, date(2026, 8, 2), "1000"),
            _snapshot(portfolio_id, date(2026, 8, 1), "900"),
        ]

        with pytest.raises(ValueError, match="sorted ascending"):
            compute_performance_snapshot(portfolio_id, date(2026, 8, 2), snapshots)
