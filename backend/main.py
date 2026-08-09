"""Process assembly for the new FastAPI-based backend skeleton (BE-001).

Run with ``uvicorn main:app --reload`` from within ``backend/``, or execute
this module directly. This file is intentionally new and separate from the
legacy ``backend/app.py`` (stdlib ``http.server`` + SQLite prototype), which
remains completely untouched as historical reference
(python-backend-standards.md §3) -- it is not refactored, deleted, or
routed through this new structure.
"""

from __future__ import annotations

import uvicorn

from api.main import app

__all__ = ["app"]

if __name__ == "__main__":
    uvicorn.run(app, host="127.0.0.1", port=8001)
