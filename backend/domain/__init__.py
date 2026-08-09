"""Domain layer for the XR-Trading core backend.

Contains the seven bounded subpackages frozen by CONTRACT-001
(doc/technical/implementation_plan/01_contracts_and_foundation.md §0.1-0.3):
``assets``, ``portfolios``, ``accounts``, ``strategies``, ``risk``,
``execution``, ``valuation``.

Hard constraint (python-backend-standards.md §1, §2; CLAUDE.md 黄金规则):
modules under ``domain/`` MUST NOT import ``fastapi``, ``httpx``, ``psycopg``,
``sqlalchemy``, or any other HTTP/DB/Provider SDK. Dependencies point inward
from ``api``/``adapters``/``repository`` toward ``application``/``domain``,
never the other way.

Allowed dependency directions between subpackages (frozen, see §0.1):
``execution`` -> ``risk`` -> ``portfolios``/``valuation``;
``strategies`` -> ``assets``/``portfolios``/``valuation`` (read-only, via
``PortfolioState``); ``valuation`` -> ``assets``/``accounts`` (currency and
account dimensions). ``assets``, ``portfolios``, and ``accounts`` have no
dependencies on other domain subpackages.
"""
