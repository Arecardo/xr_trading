"""Smoke test for the BE-001 FastAPI skeleton: /healthz boots and returns 200."""

from __future__ import annotations

from fastapi.testclient import TestClient

from api.main import app


def test_healthz_returns_200_with_ok_status() -> None:
    client = TestClient(app)

    response = client.get("/healthz")

    assert response.status_code == 200
    assert response.json() == {"status": "ok"}
