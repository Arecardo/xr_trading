import os
import tempfile
import unittest
from pathlib import Path


os.environ.setdefault("XR_TRADING_DB", "/tmp/xr_trading_test_import.sqlite3")

from backend.app import AssetService, Database, PortfolioService, now_iso  # noqa: E402


class PortfolioModelTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.db = Database(Path(self.temp_dir.name) / "test.sqlite3")
        self.assets = AssetService(self.db)
        self.portfolios = PortfolioService(self.db, self.assets)
        with self.db.connect() as conn:
            timestamp = now_iso()
            cursor = conn.execute(
                """INSERT INTO users (username, email, password_hash, created_at, updated_at)
                   VALUES ('researcher', 'researcher@example.com', 'unused', ?, ?)""",
                (timestamp, timestamp),
            )
            self.user_id = int(cursor.lastrowid)

    def tearDown(self) -> None:
        self.temp_dir.cleanup()

    def test_portfolio_and_member_lifecycle(self) -> None:
        portfolio = self.portfolios.create_portfolio(
            self.user_id,
            {
                "name": "跨市场成长组合",
                "description": "美股与加密资产研究",
                "risk_level": "growth",
                "execution_mode": "research",
                "allowed_asset_types": ["STOCK", "ETF", "CRYPTO", "CASH"],
                "status": "active",
            },
        )
        member = self.portfolios.add_member(
            self.user_id,
            portfolio["id"],
            {
                "asset_id": "crypto:coinbase:BTC-USD",
                "member_status": "candidate",
                "target_weight_max": "0.15",
                "added_reason": "配置候选",
            },
        )
        self.assertEqual(member["symbol"], "BTC")
        self.assertEqual(member["target_weight_max"], "0.15")
        self.assertEqual(self.portfolios.get_portfolio(self.user_id, portfolio["id"])["member_count"], 1)

        self.portfolios.remove_member(self.user_id, portfolio["id"], member["asset_id"])
        self.portfolios.archive_portfolio(self.user_id, portfolio["id"])
        self.assertEqual(self.portfolios.list_portfolios(self.user_id), [])

    def test_legacy_stock_pool_migrates_once(self) -> None:
        timestamp = now_iso()
        with self.db.connect() as conn:
            conn.execute(
                """INSERT INTO stock_pools (user_id, name, theme, description, created_at, updated_at)
                   VALUES (?, 'AI 观察池', 'AI', '历史记录', ?, ?)""",
                (self.user_id, timestamp, timestamp),
            )
        self.db.init_schema()
        self.db.init_schema()
        portfolios = self.portfolios.list_portfolios(self.user_id)
        self.assertEqual(len(portfolios), 1)
        self.assertEqual(portfolios[0]["status"], "draft")
        self.assertEqual(portfolios[0]["allowed_asset_types"], ["STOCK", "ETF"])


if __name__ == "__main__":
    unittest.main()
