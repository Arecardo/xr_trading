"""Event-driven backtest orchestration (BT-003).

See ``application.backtest.engine`` for ``BacktestEngine`` (the day-loop
orchestrator) and its module docstring for every judgment call this task
made. Pure simulation math (slippage/commission/precision rounding/order
diffing) lives in ``backend.backtest`` (BT-001-BT-003's shared pure-logic
package), not here -- this package only wires those pieces together with the
domain ``Strategy``/``RiskPolicy`` implementations and historical data
loading.
"""

from application.backtest.config import BacktestConfig, PrecisionMode
from application.backtest.engine import BacktestEngine
from application.backtest.execution import BacktestExecutionService
from application.backtest.models import BacktestResult, Side, TradeRecord, TradeStatus

__all__ = [
    "BacktestConfig",
    "BacktestEngine",
    "BacktestExecutionService",
    "BacktestResult",
    "PrecisionMode",
    "Side",
    "TradeRecord",
    "TradeStatus",
]
