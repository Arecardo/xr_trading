"""FastAPI application for the XR-Trading core backend (BE-001 skeleton).

This is the new entrypoint introduced by BE-001. It is deliberately
separate from the legacy stdlib ``http.server`` + SQLite prototype at
``backend/app.py``, which is left untouched and kept only as historical
reference (python-backend-standards.md §3) -- it is not routed through this
new structure.

Only a liveness check is exposed for now; this proves the skeleton boots.
Real business endpoints (portfolios, members, valuation, ...) require
persistence (BE-002) and valuation (BE-003), neither of which exist yet.
"""

from __future__ import annotations

from fastapi import FastAPI

app = FastAPI(
    title="XR-Trading Core Backend",
    description=(
        "Portfolio, risk, execution, and valuation services for the XR-Trading research platform."
    ),
    version="0.1.0",
)


@app.get("/healthz", tags=["system"])
def healthz() -> dict[str, str]:
    """Liveness probe: returns 200 with a static status payload.

    Does not check downstream dependencies (database, market-info-service);
    dependency-aware readiness checks are deferred to BE-002/BE-004.
    """
    return {"status": "ok"}
