const test = require("node:test");
const assert = require("node:assert/strict");
const ui = require("../../frontend/market-query.js");

const instrumentsPayload = {
  items: [
    {
      instrument_id: "019f1452-90f7-7992-a87a-ca2727891111",
      instrument_code: "instrument.bybit.spot.btc-usdt",
      display_name: "BTC/USDT",
      providers: [
        { provider_code: "coingecko", display_name: "CoinGecko", is_default: false, priority: 20, supported_intervals: ["1d"] },
        { provider_code: "bybit", display_name: "Bybit", is_default: true, priority: 10, supported_intervals: ["1h", "1d"] },
      ],
    },
    {
      instrument_id: "019f1452-90f7-7992-a87a-ca2727892222",
      instrument_code: "instrument.longbridge.equity.btc",
      display_name: "BTC Equity",
      providers: [{ provider_code: "longbridge", display_name: "Longbridge", is_default: false, priority: 30, supported_intervals: ["1d"] }],
    },
  ],
  next_cursor: "next-token",
};

test("catalog asset codes prefer explicit mapping and derive known first-stage types", () => {
  assert.equal(ui.assetCode({ symbol: "Ignored", asset_type: "STOCK", metadata: { market_info_asset_code: " Asset.Crypto.BTC " } }), "asset.crypto.btc");
  assert.equal(ui.assetCode({ symbol: "NVDA", asset_type: "STOCK" }), "asset.equity.us.nvda");
  assert.equal(ui.assetCode({ symbol: "QQQ", asset_type: "ETF" }), "asset.etf.us.qqq");
  assert.equal(ui.assetCode({ symbol: "BTC", asset_type: "CRYPTO" }), "asset.crypto.btc");
  assert.equal(ui.assetCode({ symbol: "USD", asset_type: "CASH" }), "asset.cash.usd");
  assert.equal(ui.assetCode({ symbol: "", asset_type: "CRYPTO" }), "");
  assert.equal(ui.assetCode({ symbol: "BTC", asset_type: "OPTION" }), "");
  assert.deepEqual(ui.catalogOptions([{ symbol: "BTC", name: "Bitcoin", asset_type: "CRYPTO" }, null]), [
    { code: "asset.crypto.btc", label: "BTC · Bitcoin", type: "CRYPTO" },
  ]);
});

test("instrument page normalization keeps usable providers and pagination", () => {
  const page = ui.normalizeInstrumentPage(instrumentsPayload);
  assert.equal(page.items.length, 2);
  assert.equal(page.items[0].providers[1].is_default, true);
  assert.deepEqual(page.items[0].providers[1].supported_intervals, ["1h", "1d"]);
  assert.equal(page.nextCursor, "next-token");
  assert.deepEqual(ui.normalizeInstrumentPage({ items: [{ instrument_code: "bad", providers: [] }] }), { items: [], nextCursor: null });
  assert.deepEqual(ui.normalizeInstrumentPage(null), { items: [], nextCursor: null });
  const sparse = ui.normalizeInstrumentPage({ items: [{ instrument_code: "instrument.crypto.sparse", providers: [
    { provider_code: "BAD PROVIDER", supported_intervals: ["1h"] },
    { provider_code: "bybit", supported_intervals: [] },
    { provider_code: "fallback", supported_intervals: ["1d"] },
  ] }] });
  assert.equal(sparse.items[0].display_name, "instrument.crypto.sparse");
  assert.equal(sparse.items[0].providers[0].display_name, "fallback");
  assert.equal(sparse.items[0].providers[0].priority, 0);
});

test("linked defaults select first instrument, declared default provider, and 1h", () => {
  const items = ui.normalizeInstrumentPage(instrumentsPayload).items;
  assert.deepEqual(ui.resolveSelection(items), {
    instrument_code: "instrument.bybit.spot.btc-usdt",
    provider_code: "bybit",
    interval: "1h",
  });
  assert.deepEqual(ui.resolveSelection(items, {
    instrument_code: "instrument.longbridge.equity.btc", provider_code: "longbridge", interval: "1h",
  }), {
    instrument_code: "instrument.longbridge.equity.btc",
    provider_code: "longbridge",
    interval: "1d",
  });
  assert.deepEqual(ui.resolveSelection([]), { instrument_code: "", provider_code: "", interval: "" });
});

test("quote and bar queries send stable readable identities explicitly", () => {
  assert.equal(ui.buildQuotesQuery(" asset.crypto.btc "), "asset_code=asset.crypto.btc");
  assert.equal(ui.buildQuotesQuery("BTC"), "");
  const built = ui.buildBarsQuery(
    { instrument_code: "instrument.bybit.spot.btc-usdt", provider_code: "bybit", interval: "1h" },
    { start_time: "2026-07-01T08:00", end_time: "2026-07-02T08:00", order: "asc", limit: "250" },
    "cursor-token",
  );
  const query = new URLSearchParams(built.query);
  assert.equal(query.get("instrument_code"), "instrument.bybit.spot.btc-usdt");
  assert.equal(query.get("provider"), "bybit");
  assert.equal(query.get("interval"), "1h");
  assert.equal(query.get("order"), "asc");
  assert.equal(query.get("limit"), "250");
  assert.equal(query.get("start_time"), new Date("2026-07-01T08:00").toISOString());
  assert.equal(query.get("end_time"), new Date("2026-07-02T08:00").toISOString());
  assert.equal(query.get("cursor"), "cursor-token");
  assert.deepEqual(built.errors, {});
  const defaults = new URLSearchParams(ui.buildBarsQuery(
    { instrument_code: "instrument.bybit.spot.btc-usdt", provider_code: "bybit", interval: "1d" }, {},
  ).query);
  assert.equal(defaults.get("order"), "desc");
  assert.equal(defaults.get("limit"), "200");
  assert.equal(defaults.has("start_time"), false);
  assert.equal(defaults.has("cursor"), false);
});

test("bar query rejects missing linkage, malformed ranges, ordering, and page sizes", () => {
  const invalid = ui.buildBarsQuery(
    { instrument_code: "asset.crypto.btc", provider_code: "Bad Provider", interval: "" },
    { start_time: "bad", end_time: "also-bad", order: "sideways", limit: "1001" },
  );
  assert.deepEqual(Object.keys(invalid.errors).sort(), ["end_time", "instrument_code", "interval", "limit", "order", "provider", "start_time"]);
  const reversed = ui.buildBarsQuery(
    { instrument_code: "instrument.bybit.btc-usdt", provider_code: "bybit", interval: "1d" },
    { start_time: "2026-07-02T08:00", end_time: "2026-07-01T08:00", order: "desc", limit: "200" },
  );
  assert.equal(reversed.errors.end_time, "结束时间必须晚于开始时间");
});

test("quote and bar rendering preserves sources, revisions, empty states, and escapes text", () => {
  const quoteHtml = ui.renderQuotes({ quotes: [{
    provider: "bybit<script>", provider_symbol: "BTCUSDT", instrument_code: "instrument.bybit.btc-usdt",
    provider_instrument_code: "provider.bybit.btcusdt", price: "62350.12", bid_price: "62349", ask_price: "62351",
    high_24h: "63000", low_24h: "61000", quote_currency: "USDT", market_time: "2026-07-02T08:00:00Z", quality_status: "valid",
  }] });
  assert.match(quoteHtml, /bybit&lt;script&gt;/);
  assert.match(quoteHtml, /62,350\.12/);
  assert.doesNotMatch(quoteHtml, /bybit<script>/);
  assert.match(ui.renderQuotes({ quotes: [] }), /暂无最新行情/);

  const barHtml = ui.renderBars({ bars: [{ open_time: "2026-07-02T08:00:00Z", open: "1", high: "3", low: "0.5", close: "2", volume: "100", revision: "<2>" }] });
  assert.match(barHtml, /&lt;2&gt;/);
  assert.match(barHtml, /<table/);
  assert.match(ui.renderBars(null), /暂无 K 线/);
});

test("formatters and stable errors degrade safely", () => {
  assert.equal(ui.formatNumber(null), "—");
  assert.equal(ui.formatNumber("not-a-number"), "not-a-number");
  assert.equal(ui.formatTime(""), "—");
  assert.equal(ui.formatTime("not-a-time"), "not-a-time");
  assert.notEqual(ui.formatTime("2026-07-02T08:00:00Z"), "2026-07-02T08:00:00Z");
  assert.equal(ui.localizedError({ code: "UNSUPPORTED_INTERVAL" }), "该 Provider 不支持所选周期");
  assert.equal(ui.localizedError({ code: "OTHER", message: "safe message" }), "safe message");
  assert.equal(ui.localizedError(null), "行情查询失败");
  assert.equal(ui.escapeHtml("<&\"'>"), "&lt;&amp;&quot;&#039;&gt;");
});
