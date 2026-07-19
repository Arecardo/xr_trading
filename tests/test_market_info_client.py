import io
import json
import os
import unittest
from http import HTTPStatus
from unittest.mock import patch
from urllib.error import HTTPError, URLError


os.environ.setdefault("XR_TRADING_DB", "/tmp/xr_trading_test_import.sqlite3")

from backend.app import (  # noqa: E402
    AppError,
    MarketInfoClient,
    PERMISSION_INGESTION_MANAGE,
    PERMISSION_OPERATIONS_READ,
    PERMISSION_SUBSCRIPTIONS_MANAGE,
    is_ingestion_command_path,
    is_ingestion_query_path,
    is_market_info_public_query_path,
    permissions_for_user,
)


class FakeResponse:
    def __init__(self, status: int, body: bytes, headers: dict[str, str] | None = None):
        self.status = status
        self.body = body
        self.headers = headers or {}

    def __enter__(self):
        return self

    def __exit__(self, *_args):
        return False

    def read(self) -> bytes:
        return self.body


class MarketInfoClientTest(unittest.TestCase):
    def test_missing_configuration_is_safe_and_retryable(self) -> None:
        status, payload, request_id = MarketInfoClient("", "").provider_status()
        self.assertEqual(status, HTTPStatus.SERVICE_UNAVAILABLE)
        self.assertEqual(payload["error"]["code"], "MARKET_INFO_UNAVAILABLE")
        self.assertTrue(payload["error"]["retryable"])
        self.assertEqual(request_id, "")

    @patch("backend.app.urlopen")
    def test_calls_exact_read_endpoint_with_server_side_credential(self, mocked_urlopen) -> None:
        mocked_urlopen.return_value = FakeResponse(
            HTTPStatus.OK,
            b'{"items":[]}',
            {"X-Request-ID": "req_01900000-0000-7000-8000-000000000001"},
        )
        client = MarketInfoClient("http://market-info:8090/", "read-secret", timeout=1.5)
        status, payload, request_id = client.provider_status()
        request = mocked_urlopen.call_args.args[0]

        self.assertEqual(request.full_url, "http://market-info:8090/api/market-info/v1/providers/status")
        self.assertEqual(request.get_method(), "GET")
        self.assertEqual(request.get_header("Authorization"), "Bearer read-secret")
        self.assertEqual(mocked_urlopen.call_args.kwargs["timeout"], 1.5)
        self.assertEqual((status, payload, request_id), (HTTPStatus.OK, {"items": []}, "req_01900000-0000-7000-8000-000000000001"))

    @patch("backend.app.urlopen")
    def test_preserves_structured_upstream_http_error(self, mocked_urlopen) -> None:
        body = b'{"error":{"code":"PERMISSION_DENIED","message":"denied","retryable":false}}'
        mocked_urlopen.side_effect = HTTPError(
            "http://market-info:8090/api/market-info/v1/providers/status",
            HTTPStatus.FORBIDDEN,
            "Forbidden",
            {"X-Request-ID": "req-denied"},
            io.BytesIO(body),
        )
        status, payload, request_id = MarketInfoClient("http://market-info:8090", "bad-token").provider_status()
        self.assertEqual(status, HTTPStatus.FORBIDDEN)
        self.assertEqual(payload["error"]["code"], "PERMISSION_DENIED")
        self.assertEqual(request_id, "req-denied")

    @patch("backend.app.urlopen")
    def test_management_request_uses_separate_token_and_json_body(self, mocked_urlopen) -> None:
        mocked_urlopen.return_value = FakeResponse(HTTPStatus.CREATED, b'{"subscription":{"enabled":true}}')
        client = MarketInfoClient("http://market-info:8090", "read-secret", "manage-secret")
        status, payload, _ = client.request(
            "POST",
            "/api/market-info/v1/collection-subscriptions",
            payload={"provider": "bybit", "enabled": True},
            manage=True,
        )
        request = mocked_urlopen.call_args.args[0]
        self.assertEqual(request.get_header("Authorization"), "Bearer manage-secret")
        self.assertEqual(request.get_header("Content-type"), "application/json")
        self.assertEqual(json.loads(request.data.decode("utf-8")), {"provider": "bybit", "enabled": True})
        self.assertEqual((status, payload), (HTTPStatus.CREATED, {"subscription": {"enabled": True}}))

    def test_management_request_requires_management_credential(self) -> None:
        status, payload, _ = MarketInfoClient("http://market-info:8090", "read-secret").request(
            "POST", "/api/market-info/v1/collection-subscriptions", payload={}, manage=True
        )
        self.assertEqual(status, HTTPStatus.SERVICE_UNAVAILABLE)
        self.assertEqual(payload["error"]["code"], "MARKET_INFO_UNAVAILABLE")

    @patch("backend.app.urlopen")
    def test_ingestion_read_preserves_scoped_query_and_read_token(self, mocked_urlopen) -> None:
        mocked_urlopen.return_value = FakeResponse(HTTPStatus.OK, b'{"items":[],"next_cursor":null}')
        client = MarketInfoClient("http://market-info:8090", "read-secret", "manage-secret")
        status, payload, _ = client.request(
            "GET",
            "/api/market-info/v1/ingestion-tasks",
            query="status=failed&provider=bybit&limit=20",
        )
        request = mocked_urlopen.call_args.args[0]
        self.assertEqual(
            request.full_url,
            "http://market-info:8090/api/market-info/v1/ingestion-tasks?status=failed&provider=bybit&limit=20",
        )
        self.assertEqual(request.get_header("Authorization"), "Bearer read-secret")
        self.assertEqual((status, payload), (HTTPStatus.OK, {"items": [], "next_cursor": None}))

    def test_ingestion_proxy_only_accepts_exact_get_routes(self) -> None:
        run_id = "019f1452-90f7-7992-a87a-ca272789160f"
        accepted = (
            "/api/market-info/v1/ingestion-runs",
            f"/api/market-info/v1/ingestion-runs/{run_id}",
            "/api/market-info/v1/ingestion-tasks",
            f"/api/market-info/v1/ingestion-tasks/{run_id}",
        )
        for path in accepted:
            self.assertTrue(is_ingestion_query_path("GET", path), path)
        for method, path in (
            ("POST", "/api/market-info/v1/ingestion-runs"),
            ("GET", "/api/market-info/v1/ingestion-runs/backfill"),
            ("GET", "/api/market-info/v1/ingestion-tasks/not-a-uuid"),
            ("DELETE", f"/api/market-info/v1/ingestion-tasks/{run_id}"),
        ):
            self.assertFalse(is_ingestion_query_path(method, path), f"{method} {path}")

    def test_ingestion_proxy_only_accepts_exact_post_command_routes(self) -> None:
        task_id = "019f1452-90f7-7992-a87a-ca272789160f"
        for path in (
            "/api/market-info/v1/ingestion-runs/backfill",
            f"/api/market-info/v1/ingestion-tasks/{task_id}/retry",
            f"/api/market-info/v1/ingestion-tasks/{task_id}/cancel",
        ):
            self.assertTrue(is_ingestion_command_path("POST", path), path)
        for method, path in (
            ("GET", "/api/market-info/v1/ingestion-runs/backfill"),
            ("POST", "/api/market-info/v1/ingestion-runs"),
            ("POST", f"/api/market-info/v1/ingestion-tasks/{task_id}/delete"),
            ("POST", "/api/market-info/v1/ingestion-tasks/not-a-uuid/retry"),
        ):
            self.assertFalse(is_ingestion_command_path(method, path), f"{method} {path}")

    def test_public_market_query_proxy_only_accepts_exact_get_routes(self) -> None:
        for path in (
            "/api/market-info/v1/instruments",
            "/api/market-info/v1/quotes/latest",
            "/api/market-info/v1/bars",
        ):
            self.assertTrue(is_market_info_public_query_path("GET", path), path)
        for method, path in (
            ("POST", "/api/market-info/v1/bars"),
            ("GET", "/api/market-info/v1/assets"),
            ("GET", "/api/market-info/v1/instruments/anything"),
            ("DELETE", "/api/market-info/v1/quotes/latest"),
        ):
            self.assertFalse(is_market_info_public_query_path(method, path), f"{method} {path}")

    @patch("backend.app.urlopen")
    def test_public_market_query_preserves_explicit_query_and_read_token(self, mocked_urlopen) -> None:
        mocked_urlopen.return_value = FakeResponse(HTTPStatus.OK, b'{"items":[],"next_cursor":null}')
        client = MarketInfoClient("http://market-info:8090", "read-secret", "manage-secret")
        status, payload, _ = client.request(
            "GET", "/api/market-info/v1/instruments", query="asset_code=asset.crypto.btc&enabled=true&limit=100"
        )
        request = mocked_urlopen.call_args.args[0]
        self.assertEqual(
            request.full_url,
            "http://market-info:8090/api/market-info/v1/instruments?asset_code=asset.crypto.btc&enabled=true&limit=100",
        )
        self.assertEqual(request.get_header("Authorization"), "Bearer read-secret")
        self.assertEqual((status, payload), (HTTPStatus.OK, {"items": [], "next_cursor": None}))

    @patch("backend.app.urlopen", side_effect=URLError("connection refused; secret must not surface"))
    def test_network_failure_returns_sanitized_unavailable_error(self, _mocked_urlopen) -> None:
        status, payload, _ = MarketInfoClient("http://market-info:8090", "secret").provider_status()
        self.assertEqual(status, HTTPStatus.SERVICE_UNAVAILABLE)
        self.assertNotIn("secret", payload["error"]["message"])

    @patch("backend.app.urlopen")
    def test_invalid_upstream_payload_is_bad_gateway(self, mocked_urlopen) -> None:
        mocked_urlopen.return_value = FakeResponse(HTTPStatus.OK, b"not json")
        with self.assertRaises(AppError) as raised:
            MarketInfoClient("http://market-info:8090", "secret").provider_status()
        self.assertEqual(raised.exception.status, HTTPStatus.BAD_GATEWAY)

    @patch.dict(os.environ, {"XR_TRADING_SUBSCRIPTION_MANAGERS": "42, ADMIN, manager@example.test"}, clear=True)
    def test_subscription_manager_allowlist_accepts_stable_id_username_or_email(self) -> None:
        for user in (
            {"id": 42, "username": "reader", "email": "reader@example.test"},
            {"id": 7, "username": "admin", "email": "reader@example.test"},
            {"id": 8, "username": "reader", "email": "Manager@Example.Test"},
        ):
            self.assertEqual(
                permissions_for_user(user),
                [PERMISSION_OPERATIONS_READ, PERMISSION_SUBSCRIPTIONS_MANAGE, PERMISSION_INGESTION_MANAGE],
            )

    @patch.dict(
        os.environ,
        {
            "XR_TRADING_SUBSCRIPTION_MANAGERS": "subscription-manager",
            "XR_TRADING_INGESTION_MANAGERS": "ingestion-manager",
        },
        clear=True,
    )
    def test_ingestion_manager_allowlist_can_be_separated(self) -> None:
        self.assertEqual(
            permissions_for_user({"id": 7, "username": "ingestion-manager", "email": "reader@example.test"}),
            [PERMISSION_OPERATIONS_READ, PERMISSION_INGESTION_MANAGE],
        )

    @patch.dict(os.environ, {"XR_TRADING_SUBSCRIPTION_MANAGERS": "manager"}, clear=True)
    def test_unlisted_user_remains_read_only(self) -> None:
        self.assertEqual(
            permissions_for_user({"id": 9, "username": "reader", "email": "reader@example.test"}),
            [PERMISSION_OPERATIONS_READ],
        )


if __name__ == "__main__":
    unittest.main()
