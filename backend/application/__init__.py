"""Use-case orchestration and transaction boundaries.

Intentionally empty scaffolding for BE-001: no use cases exist yet because
there is no persistence (BE-002) or valuation implementation (BE-003) to
orchestrate. ``application`` may call into ``domain``; it must not be
called from ``domain``, and it must not talk to a DB driver or Broker SDK
directly (that belongs in ``adapters``/``repository``,
python-backend-standards.md §2).
"""
