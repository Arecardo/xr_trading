"""Use-case orchestration and transaction boundaries.

BE-001 left this package empty (no persistence or valuation implementation
existed yet to orchestrate). BE-003 adds the first use case: ``valuation``
(``SqlValuationService``, the concrete ``domain.valuation.models.
ValuationService``). ``application`` may call into ``domain``; it must not
be called from ``domain``, and it must not talk to a DB driver or Broker SDK
directly (that belongs in ``adapters``/``repository``,
python-backend-standards.md §2) -- ``application.valuation`` itself does not
import SQLAlchemy/httpx directly either, only the ``repository``/``adapters``
Protocol-shaped dependencies it is constructed with.
"""
