"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const view = require("../../frontend/provider-status.js");

function scope(overrides = {}) {
  return {
    market: "crypto_spot",
    session_type: "continuous",
    interval: "1h",
    market_state: "open",
    health_status: "healthy",
    freshness_status: "fresh",
    data_delay_seconds: 3,
    active_subscriptions: 2,
    delayed_subscriptions: 0,
    next_market_open_at: null,
    ...overrides,
  };
}

function provider(overrides = {}) {
  return {
    provider_code: "bybit",
    display_name: "Bybit",
    provider_type: "exchange",
    configured_status: "active",
    health_status: "healthy",
    last_success_at: "2026-07-02T08:00:03Z",
    last_failure_at: null,
    consecutive_failures: 0,
    checked_at: "2026-07-02T08:00:10Z",
    scopes: [scope()],
    ...overrides,
  };
}

test("normalizes API payload and unknown states safely", () => {
  assert.deepEqual(view.normalizeItems(null), []);
  assert.deepEqual(view.normalizeItems({ items: "bad" }), []);
  assert.deepEqual(view.normalizeItems({ items: [null, provider()] }).length, 1);
  assert.equal(view.safeStatus("healthy"), "healthy");
  assert.equal(view.safeStatus("degraded"), "degraded");
  assert.equal(view.safeStatus("unhealthy"), "unhealthy");
  assert.equal(view.safeStatus("unexpected"), "unknown");
});

test("formats time and delay boundaries", () => {
  assert.equal(view.formatDateTime(null), "—");
  assert.equal(view.formatDateTime("not-a-date"), "—");
  assert.match(view.formatDateTime("2026-07-02T08:00:10Z", "en-GB"), /02\/07\/2026/);
  assert.equal(view.formatDelay(null), "—");
  assert.equal(view.formatDelay(-1), "—");
  assert.equal(view.formatDelay(3.9), "3 秒");
  assert.equal(view.formatDelay(125), "2 分 5 秒");
  assert.equal(view.formatDelay(3600), "1 小时");
  assert.equal(view.formatDelay(7380), "2 小时 3 分");
});

test("summarizes providers and subscriptions", () => {
  const items = [
    provider(),
    provider({ health_status: "degraded", scopes: [scope({ active_subscriptions: 3, delayed_subscriptions: 1 })] }),
    provider({ health_status: "unhealthy", scopes: [] }),
    provider({ health_status: "not-valid", scopes: [{ active_subscriptions: "2", delayed_subscriptions: null }] }),
    provider({ health_status: "unknown", configured_status: "disabled", scopes: [] }),
  ];
  assert.deepEqual(view.summarize(items), {
    total: 5,
    healthy: 1,
    attention: 3,
    activeSubscriptions: 5,
    delayedSubscriptions: 1,
  });
  const html = view.renderSummary(items);
  assert.match(html, /需要关注/);
  assert.match(html, />3<\/strong>/);
  assert.match(html, /5 \/ 1/);
});

test("renders all health states, closed market, and escaped content", () => {
  const items = [
    provider(),
    provider({ provider_code: "longbridge", display_name: "Longbridge", health_status: "degraded" }),
    provider({ health_status: "unhealthy", consecutive_failures: 3 }),
    provider({
      provider_code: "unsafe<script>",
      display_name: "<Unsafe>",
      health_status: "unknown",
      configured_status: "disabled",
      last_success_at: "bad",
      scopes: [scope({
        market: "us_equity",
        session_type: "regular",
        market_state: "closed",
        health_status: "unknown",
        freshness_status: "not_applicable",
        data_delay_seconds: null,
        next_market_open_at: "2026-07-06T13:30:00Z",
      })],
    }),
  ];
  const html = view.renderProviders(items, "en-GB");
  assert.match(html, /status-badge healthy/);
  assert.match(html, /status-badge degraded/);
  assert.match(html, /status-badge unhealthy/);
  assert.match(html, /status-badge unknown/);
  assert.match(html, /market-badge closed/);
  assert.match(html, /休市/);
  assert.match(html, /停止计算/);
  assert.match(html, /下次开市/);
  assert.doesNotMatch(html, /<Unsafe>|unsafe<script>/);
  assert.match(html, /&lt;Unsafe&gt;/);
});

test("renders provider and scope empty states", () => {
  assert.match(view.renderProviders([]), /尚未配置数据源/);
  const html = view.renderProviders([provider({ scopes: [], provider_type: "", provider_code: "", display_name: "", configured_status: "mystery" })]);
  assert.match(html, /尚无有效采集范围/);
  assert.match(html, /未命名数据源/);
  assert.match(html, /未知配置/);
});

test("renders delayed and unknown scope fallbacks", () => {
  const html = view.renderProviders([provider({ scopes: [scope({
    market: "custom_market",
    session_type: "auction",
    market_state: "unexpected",
    health_status: "unexpected",
    freshness_status: "delayed",
    data_delay_seconds: 65,
    active_subscriptions: null,
    delayed_subscriptions: undefined,
  })] })]);
  assert.match(html, /custom_market/);
  assert.match(html, /auction/);
  assert.match(html, /市场状态未知/);
  assert.match(html, /1 分 5 秒/);
});
