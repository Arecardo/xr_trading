"""Strategy domain subpackage. See ``domain.strategies.models`` for the frozen interface."""

from domain.strategies.errors import (
    InconsistentInputError,
    InvalidTargetWeightsError,
    MissingPriceDataError,
    StrategyError,
)
from domain.strategies.models import (
    AnalysisResult,
    AssetAnalysis,
    AssetScore,
    Strategy,
    StrategyOutput,
)
from domain.strategies.simple_rule_strategy import SimpleRuleStrategy

__all__ = [
    "AnalysisResult",
    "AssetAnalysis",
    "AssetScore",
    "InconsistentInputError",
    "InvalidTargetWeightsError",
    "MissingPriceDataError",
    "SimpleRuleStrategy",
    "Strategy",
    "StrategyError",
    "StrategyOutput",
]
