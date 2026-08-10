"""Technical indicator calculation library (BT-002).

Computes the eight raw indicators listed in `doc/technical/04_analysis_modules.md`
§2 ("技术分析" -> "指标") from a BT-001 `AlignedSeries`:

日线收益率, MA5/20/50, 均线趋势, RSI, ATR, 成交量变化,
价格相对20日高低点位置, 相对基准强弱.

Scope note (per the BT-002 task brief): §2 of `04_analysis_modules.md` also
describes a *qualitative* scoring/classification layer on top of these raw
numbers (`trend_state`/`momentum_state`/`overheated`/`breakdown`/
`technical_score`, and the "评分建议" table). That layer is explicitly **out
of scope** here -- it belongs to a later strategy/signal task that decides
how to interpret these numbers. Every value this module produces is a plain
numeric (or a bare directional literal for `ma_trend`), never a score or a
buy/sell judgement.

Design decisions (documented here, not left implicit, per the task brief):

1. **Calendar-day rolling windows, stale days included as zero-movement
   observations.** `AlignedSeries.bars` is already a gapless one-entry-per-
   UTC-calendar-day sequence (BT-001, RM0 DEC-002): US equity non-trading
   days carry the prior close forward flat (open=high=low=close=prior close,
   volume=0) and are marked `price_status="stale"`; crypto is fresh every
   day. Rather than re-deriving a second "trading-days-only" view of the
   series, every rolling window in this module (MA, RSI, ATR, 20-day
   high/low, relative-strength lookback) walks `bars` positionally,
   calendar day by calendar day, exactly as the rest of the backtest engine
   values the portfolio (§5 of `08_backtest_engine.md` runs one `time_step`
   per UTC calendar day). A stale day contributes a real zero-movement
   observation: zero price change (its close equals the prior day's close,
   so `daily_return`/gain/loss/true-range are all exactly zero that day) and
   zero volume (so `volume_change` on a stale day is a real -100% versus a
   fresh prior day, correctly reflecting "no trading happened").
   Trade-off knowingly accepted: a 5-calendar-day window spanning a weekend
   only contains 3 distinct trading observations (the other 2 are repeats of
   Friday's close), which pulls the average closer to the most recent
   trading price than a 5-*trading*-day MA would. This mirrors the same
   calendar-day-cadence choice BT-001/DEC-002 already made for equity
   valuation, so indicators stay on the same clock as the equity curve; it
   is a reasonable default but should be revisited if it produces
   surprising results once BT-004 strategies consume it.

2. **RSI and ATR use a simple rolling-window (SMA-style, "Cutler's RSI")
   formula, not Wilder's recursive exponential smoothing.** Wilder's
   original RSI/ATR smooth over the *entire* history back to the series
   start (each day's value depends on the previous day's smoothed value),
   which makes them path-dependent on data outside the stated window and
   harder to hand-verify with a small fixed sample. The rolling-window
   variant recomputes each day's value from only the last N observations
   ending on that day, which is simpler, independently testable per day,
   and is what `doc/technical/roadmap/03_backtest_engine.md`'s "K 线不足 50
   日时降级处理" requirement implies (a pure per-day window-size check,
   no accumulated state).

3. **RSI period = 14, ATR period = 14.** Neither `03_backtest_engine.md`
   nor `04_analysis_modules.md` specifies a period; 14 is the universal
   industry-standard default for both and is used here as an explicit,
   documented choice rather than an invented number.

4. **`ma_trend` compares MA5 to MA20** (`"up"` if MA5 > MA20, `"down"` if
   MA5 < MA20, `"flat"` if equal), not a slope/regression computation. This
   is the classic "golden/death cross" direction signal, reuses the
   already-computed MA5/MA20 values, needs no extra lookback window beyond
   MA20's, and is deliberately a bare 3-way directional literal -- distinct
   from and not to be confused with the qualitative `trend_state`
   (`"uptrend"`/`"downtrend"`/...) in the scoring layer this module does not
   implement.

5. **成交量变化 (volume_change) is a plain day-over-day percentage change**
   (mirroring how `daily_return` is a day-over-day price change), not a
   change versus an N-day average volume. When the prior day's volume is
   zero (e.g. it was a stale day) and today's volume is also zero, the
   change is defined as zero (no change). When the prior day's volume is
   zero and today's is not, the percentage change is mathematically
   undefined (division by zero) -- returned as `None` rather than an
   invented sentinel (e.g. `+inf`), consistent with this module's "never a
   fabricated value" rule for insufficient/undefined data.

6. **价格相对20日高低点位置 (price_position_20d)** is
   `(close - low_20) / (high_20 - low_20)` over a 20-calendar-day window
   that includes the current day, clamped implicitly to `[0, 1]` (always
   satisfied since `close` participates in computing `high_20`/`low_20`).
   When `high_20 == low_20` (a fully flat window -- possible near the very
   start of a series or during an extended stale run), the ratio is
   `0/0`; defined as `Decimal("0.5")` (the midpoint) since the price
   coincides with both the window high and low.

7. **相对基准强弱 (relative_strength_20d)** is the asset's 20-calendar-day
   return minus the benchmark's 20-calendar-day return over the same
   window (excess return over the benchmark), using the same 20-day period
   as `price_position_20d` for consistency across the "medium-term" raw
   indicators. `compute_relative_strength`/`compute_indicators` require the
   asset and benchmark `AlignedSeries` to cover the exact same
   `[start_date, end_date]` (e.g. produced by two `HistoricalDataLoader.load`
   calls with identical date bounds) -- a mismatch raises `ValueError`
   rather than guessing an alignment.

8. **Insufficient history never produces a computed-but-meaningless value.**
   Every indicator that needs a minimum window returns `None` for any day
   where fewer than the required number of calendar-day bars precede (and
   include) it -- e.g. `ma50` is `None` for the first 49 days of a series,
   `rsi14`/`atr14` are `None` for the first 14 days. This is the module-wide
   mechanism for the roadmap's "K 线不足 50 日时降级处理，不报错、不产出伪造值"
   requirement.

All price/volume/indicator arithmetic uses `decimal.Decimal`
(python-backend-standards.md §6) -- never `float`.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import date
from decimal import Decimal
from typing import Literal

from .models import AlignedBar, AlignedSeries

MaTrend = Literal["up", "down", "flat"]
"""Direction of MA5 relative to MA20 -- see module docstring, decision 4.
Not to be confused with the (out-of-scope, later-task) qualitative
`trend_state` classification in `04_analysis_modules.md`."""

_RSI_PERIOD = 14
_ATR_PERIOD = 14
_MEDIUM_TERM_WINDOW = 20  # shared by price_position_20d and relative_strength_20d
_HALF = Decimal("0.5")
_ZERO = Decimal(0)
_HUNDRED = Decimal(100)


def compute_daily_returns(series: AlignedSeries) -> tuple[Decimal | None, ...]:
    """Day-over-day close-to-close return: `(close_t - close_{t-1}) / close_{t-1}`.

    `None` for the first bar (no prior day) and for any day whose prior
    close is zero (would divide by zero; not expected for real price data
    but guarded defensively rather than raising).
    """
    bars = series.bars
    out: list[Decimal | None] = [None] * len(bars)
    for i in range(1, len(bars)):
        prev_close = bars[i - 1].close
        if prev_close == _ZERO:
            out[i] = None
            continue
        out[i] = (bars[i].close - prev_close) / prev_close
    return tuple(out)


def compute_moving_average(series: AlignedSeries, window: int) -> tuple[Decimal | None, ...]:
    """Simple moving average of `close` over the trailing `window` calendar days
    (inclusive of the current day). `None` until `window` bars are available.
    """
    if window < 1:
        raise ValueError(f"window must be >= 1, got {window}")
    bars = series.bars
    out: list[Decimal | None] = [None] * len(bars)
    for i in range(len(bars)):
        if i + 1 < window:
            continue
        total = sum((bars[j].close for j in range(i - window + 1, i + 1)), start=_ZERO)
        out[i] = total / window
    return tuple(out)


def compute_ma_trend(
    ma5: tuple[Decimal | None, ...], ma20: tuple[Decimal | None, ...]
) -> tuple[MaTrend | None, ...]:
    """Direction of MA5 relative to MA20 for each day -- see module docstring, decision 4.

    `ma5` and `ma20` must be the same length (as returned by
    `compute_moving_average(series, 5)` / `compute_moving_average(series, 20)`
    for the same series). `None` wherever either input is `None`.
    """
    if len(ma5) != len(ma20):
        raise ValueError(f"ma5 length {len(ma5)} != ma20 length {len(ma20)}")
    out: list[MaTrend | None] = []
    for short, long_ in zip(ma5, ma20, strict=True):
        if short is None or long_ is None:
            out.append(None)
        elif short > long_:
            out.append("up")
        elif short < long_:
            out.append("down")
        else:
            out.append("flat")
    return tuple(out)


def compute_rsi(series: AlignedSeries, period: int = _RSI_PERIOD) -> tuple[Decimal | None, ...]:
    """Rolling-window RSI over the trailing `period` daily changes (Cutler's RSI,
    not Wilder's recursive smoothing -- see module docstring, decisions 2-3).

    `None` until `period` daily changes are available (i.e. `period + 1` bars).
    A window with zero net gain and zero net loss (e.g. all-stale, perfectly
    flat) yields `Decimal("50")`; a window with gains but no losses yields
    `Decimal("100")` (avoids division by zero, standard RSI convention).
    """
    if period < 1:
        raise ValueError(f"period must be >= 1, got {period}")
    bars = series.bars
    out: list[Decimal | None] = [None] * len(bars)
    for i in range(len(bars)):
        if i - period < 0:
            continue
        gains = _ZERO
        losses = _ZERO
        for j in range(i - period + 1, i + 1):
            change = bars[j].close - bars[j - 1].close
            if change > _ZERO:
                gains += change
            elif change < _ZERO:
                losses += -change
        avg_gain = gains / period
        avg_loss = losses / period
        if avg_gain == _ZERO and avg_loss == _ZERO:
            out[i] = _HALF * _HUNDRED  # Decimal("50") -- flat window, no movement either way
        elif avg_loss == _ZERO:
            out[i] = _HUNDRED
        else:
            rs = avg_gain / avg_loss
            out[i] = _HUNDRED - (_HUNDRED / (Decimal(1) + rs))
    return tuple(out)


def compute_atr(series: AlignedSeries, period: int = _ATR_PERIOD) -> tuple[Decimal | None, ...]:
    """Rolling-window ATR over the trailing `period` true ranges (SMA of True
    Range, not Wilder's recursive smoothing -- see module docstring, decisions 2-3).

    `True Range_t = max(high_t - low_t, |high_t - close_{t-1}|, |low_t - close_{t-1}|)`.
    `None` until `period` true ranges are available (i.e. `period + 1` bars).
    On a `stale` day, high=low=close=prior close, so that day's True Range
    is exactly zero (module docstring, decision 1).
    """
    if period < 1:
        raise ValueError(f"period must be >= 1, got {period}")
    bars = series.bars
    out: list[Decimal | None] = [None] * len(bars)
    for i in range(len(bars)):
        if i - period < 0:
            continue
        total = _ZERO
        for j in range(i - period + 1, i + 1):
            prev_close = bars[j - 1].close
            true_range = max(
                bars[j].high - bars[j].low,
                abs(bars[j].high - prev_close),
                abs(bars[j].low - prev_close),
            )
            total += true_range
        out[i] = total / period
    return tuple(out)


def compute_volume_change(series: AlignedSeries) -> tuple[Decimal | None, ...]:
    """Day-over-day percentage change in volume -- see module docstring, decision 5.

    `None` for the first bar (no prior day) and when the prior day's volume
    is zero while today's is not (undefined ratio). Zero-to-zero is defined
    as `Decimal("0")` (no change).
    """
    bars = series.bars
    out: list[Decimal | None] = [None] * len(bars)
    for i in range(1, len(bars)):
        prev_volume = bars[i - 1].volume
        curr_volume = bars[i].volume
        if prev_volume == _ZERO:
            out[i] = _ZERO if curr_volume == _ZERO else None
            continue
        out[i] = (curr_volume - prev_volume) / prev_volume
    return tuple(out)


def compute_price_position(
    series: AlignedSeries, window: int = _MEDIUM_TERM_WINDOW
) -> tuple[Decimal | None, ...]:
    """Position of `close` within the trailing `window`-calendar-day high/low
    range: `(close - low_window) / (high_window - low_window)`, in `[0, 1]`.

    `None` until `window` bars are available. `Decimal("0.5")` when the
    window's high equals its low (module docstring, decision 6).
    """
    if window < 1:
        raise ValueError(f"window must be >= 1, got {window}")
    bars = series.bars
    out: list[Decimal | None] = [None] * len(bars)
    for i in range(len(bars)):
        if i + 1 < window:
            continue
        window_bars = bars[i - window + 1 : i + 1]
        window_high = max(b.high for b in window_bars)
        window_low = min(b.low for b in window_bars)
        if window_high == window_low:
            out[i] = _HALF
        else:
            out[i] = (bars[i].close - window_low) / (window_high - window_low)
    return tuple(out)


def compute_relative_strength(
    asset_series: AlignedSeries,
    benchmark_series: AlignedSeries,
    window: int = _MEDIUM_TERM_WINDOW,
) -> tuple[Decimal | None, ...]:
    """Asset's trailing `window`-calendar-day return minus the benchmark's
    trailing `window`-calendar-day return over the same window (excess
    return vs. benchmark) -- see module docstring, decision 7.

    Requires `asset_series` and `benchmark_series` to cover the exact same
    `[start_date, end_date]`; raises `ValueError` otherwise rather than
    guessing a date alignment. `None` until `window` calendar days of
    return history are available (i.e. `window + 1` bars) for either series,
    or when either series' return-anchor close is zero.
    """
    if window < 1:
        raise ValueError(f"window must be >= 1, got {window}")
    if (
        asset_series.start_date != benchmark_series.start_date
        or asset_series.end_date != benchmark_series.end_date
    ):
        raise ValueError(
            "asset_series and benchmark_series must cover the exact same date range: "
            f"asset=[{asset_series.start_date}, {asset_series.end_date}], "
            f"benchmark=[{benchmark_series.start_date}, {benchmark_series.end_date}]"
        )
    asset_bars = asset_series.bars
    benchmark_bars = benchmark_series.bars
    if len(asset_bars) != len(benchmark_bars):
        raise ValueError(
            f"asset_series has {len(asset_bars)} bars but benchmark_series has "
            f"{len(benchmark_bars)} -- both must be gapless over the same date range"
        )

    out: list[Decimal | None] = [None] * len(asset_bars)
    for i in range(len(asset_bars)):
        if i - window < 0:
            continue
        asset_anchor = asset_bars[i - window].close
        benchmark_anchor = benchmark_bars[i - window].close
        if asset_anchor == _ZERO or benchmark_anchor == _ZERO:
            continue
        asset_return = (asset_bars[i].close - asset_anchor) / asset_anchor
        benchmark_return = (benchmark_bars[i].close - benchmark_anchor) / benchmark_anchor
        out[i] = asset_return - benchmark_return
    return tuple(out)


@dataclass(frozen=True)
class IndicatorBar:
    """The eight BT-002 raw indicators for one `trading_day`, aligned 1:1 with
    an `AlignedSeries.bars` entry. Every field is `None` when its
    minimum-history requirement is not yet met (module docstring, decision 8)
    -- never a fabricated value.
    """

    trading_day: date
    daily_return: Decimal | None
    ma5: Decimal | None
    ma20: Decimal | None
    ma50: Decimal | None
    ma_trend: MaTrend | None
    rsi14: Decimal | None
    atr14: Decimal | None
    volume_change: Decimal | None
    price_position_20d: Decimal | None
    relative_strength_20d: Decimal | None


def compute_indicators(
    asset_series: AlignedSeries, benchmark_series: AlignedSeries
) -> tuple[IndicatorBar, ...]:
    """Compute all eight BT-002 raw indicators for `asset_series`, one
    `IndicatorBar` per `asset_series.bars` entry (same length, same
    `trading_day` order).

    `benchmark_series` (e.g. QQQ, per the RM0 asset universe) must cover the
    exact same `[start_date, end_date]` as `asset_series` -- see
    `compute_relative_strength`. Pass `asset_series` as its own
    `benchmark_series` if a caller genuinely wants relative-strength-vs-self
    (always zero); there is no implicit default benchmark here, callers must
    supply one explicitly.
    """
    daily_returns = compute_daily_returns(asset_series)
    ma5 = compute_moving_average(asset_series, 5)
    ma20 = compute_moving_average(asset_series, 20)
    ma50 = compute_moving_average(asset_series, 50)
    ma_trend = compute_ma_trend(ma5, ma20)
    rsi14 = compute_rsi(asset_series)
    atr14 = compute_atr(asset_series)
    volume_change = compute_volume_change(asset_series)
    price_position_20d = compute_price_position(asset_series)
    relative_strength_20d = compute_relative_strength(asset_series, benchmark_series)

    bars: tuple[AlignedBar, ...] = asset_series.bars
    return tuple(
        IndicatorBar(
            trading_day=bars[i].trading_day,
            daily_return=daily_returns[i],
            ma5=ma5[i],
            ma20=ma20[i],
            ma50=ma50[i],
            ma_trend=ma_trend[i],
            rsi14=rsi14[i],
            atr14=atr14[i],
            volume_change=volume_change[i],
            price_position_20d=price_position_20d[i],
            relative_strength_20d=relative_strength_20d[i],
        )
        for i in range(len(bars))
    )
