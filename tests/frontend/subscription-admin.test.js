"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const ui = require("../../frontend/subscription-admin.js");

function subscription(overrides = {}) {
  return {
    subscription_id: "019f1452-90f7-7992-a87a-ca2727897401",
    provider: "bybit",
    instrument_code: "instrument.bybit.spot.btc-usdt",
    provider_instrument_code: "provider.bybit.spot.btcusdt",
    provider_symbol: "BTCUSDT",
    interval: "1h",
    enabled: true,
    priority: 100,
    close_delay_seconds: 120,
    revision_delay_seconds: null,
    updated_at: "2026-07-02T08:00:00Z",
    ...overrides,
  };
}

test("builds a canonical create payload", () => {
  const result = ui.buildCreatePayload({
    provider: " bybit ",
    instrument_code: " instrument.bybit.spot.btc-usdt ",
    interval: "1h",
    enabled: false,
    priority: "100",
    close_delay_seconds: "120",
    revision_delay_seconds: "",
    reason: "  initial collection  ",
  });
  assert.deepEqual(result.errors, {});
  assert.deepEqual(result.payload, {
    provider: "bybit",
    instrument_code: "instrument.bybit.spot.btc-usdt",
    interval: "1h",
    enabled: false,
    priority: 100,
    close_delay_seconds: 120,
    revision_delay_seconds: null,
    reason: "initial collection",
  });
});

test("validates every create constraint", () => {
  const result = ui.buildCreatePayload({
    provider: "Bad Provider",
    instrument_code: "asset.btc",
    interval: "5m",
    enabled: true,
    priority: "32768",
    close_delay_seconds: "-1",
    revision_delay_seconds: "not-number",
    reason: " ",
  });
  assert.equal(result.payload, null);
  assert.deepEqual(Object.keys(result.errors).sort(), [
    "close_delay_seconds", "instrument_code", "interval", "priority", "provider", "reason", "revision_delay_seconds",
  ]);

  const tooLong = ui.buildCreatePayload({
    provider: "bybit", instrument_code: "instrument.bybit.btc", interval: "1d", enabled: true,
    priority: "", close_delay_seconds: null, revision_delay_seconds: undefined, reason: "x".repeat(513),
  });
  assert.equal(tooLong.errors.priority, "必须填写整数");
  assert.equal(tooLong.errors.close_delay_seconds, "必须填写整数");
  assert.match(tooLong.errors.reason, /512/);
});

test("builds update payload with explicit null and validates bounds", () => {
  assert.deepEqual(ui.buildUpdatePayload({
    enabled: true, priority: "0", close_delay_seconds: "2147483647", revision_delay_seconds: "300", reason: " adjust ",
  }), {
    errors: {},
    payload: { enabled: true, priority: 0, close_delay_seconds: 2147483647, revision_delay_seconds: 300, reason: "adjust" },
  });
  assert.deepEqual(ui.buildUpdatePayload({
    enabled: false, priority: "1", close_delay_seconds: "0", revision_delay_seconds: "", reason: "disable revision",
  }).payload.revision_delay_seconds, null);
  const invalid = ui.buildUpdatePayload({ enabled: false, priority: "1.2", close_delay_seconds: "999999999999999999999", revision_delay_seconds: "-2", reason: "ok" });
  assert.equal(invalid.payload, null);
  assert.deepEqual(Object.keys(invalid.errors).sort(), ["close_delay_seconds", "priority", "revision_delay_seconds"]);
});

test("builds scoped list query and normalizes pages", () => {
  const query = new URLSearchParams(ui.buildListQuery({ provider: " bybit ", instrument_code: "", interval: "1h", enabled: "false" }, "cursor-token", 30));
  assert.equal(query.get("provider"), "bybit");
  assert.equal(query.has("instrument_code"), false);
  assert.equal(query.get("interval"), "1h");
  assert.equal(query.get("enabled"), "false");
  assert.equal(query.get("limit"), "30");
  assert.equal(query.get("cursor"), "cursor-token");
  assert.equal(new URLSearchParams(ui.buildListQuery()).has("cursor"), false);

  assert.deepEqual(ui.normalizePage(null), { items: [], nextCursor: null });
  assert.deepEqual(ui.normalizePage({ items: "bad" }), { items: [], nextCursor: null });
  const normalized = ui.normalizePage({ items: [null, subscription()], next_cursor: "next" });
  assert.equal(normalized.items.length, 1);
  assert.equal(normalized.nextCursor, "next");
  assert.equal(ui.normalizePage({ items: [], next_cursor: 3 }).nextCursor, null);
});

test("localizes stable API errors and safely renders form errors", () => {
  assert.match(ui.localizedError({ code: "SUBSCRIPTION_ALREADY_EXISTS" }), /已经存在/);
  assert.match(ui.localizedError({ code: "PERMISSION_DENIED" }), /没有订阅管理权限/);
  assert.equal(ui.localizedError({ code: "NEW_CODE", message: "safe fallback" }), "safe fallback");
  assert.match(ui.localizedError({}), /请求失败/);
  assert.equal(ui.renderErrors({}), "");
  const html = ui.renderErrors({ provider: "<bad>" });
  assert.match(html, /&lt;bad&gt;/);
  assert.doesNotMatch(html, /<bad>/);
});

test("renders empty, read-only, enabled, disabled, and edit states", () => {
  assert.match(ui.renderSubscriptions([]), /没有匹配的采集订阅/);
  const readOnly = ui.renderSubscriptions([subscription()]);
  assert.match(readOnly, /BTCUSDT/);
  assert.doesNotMatch(readOnly, /编辑设置/);
  assert.match(readOnly, /修订延迟<\/dt><dd>关闭/);

  const disabled = subscription({
    subscription_id: "019f1452-90f7-7992-a87a-ca2727897402",
    enabled: false,
    revision_delay_seconds: 300,
    provider_symbol: "<SCRIPT>",
    updated_at: "bad-time",
  });
  const editable = ui.renderSubscriptions([disabled], { canManage: true, editingID: disabled.subscription_id, locale: "en-GB" });
  assert.match(editable, /已停用/);
  assert.match(editable, /编辑设置/);
  assert.match(editable, /data-subscription-edit/);
  assert.match(editable, /300 秒/);
  assert.match(editable, />—<\/dd>/);
  assert.match(editable, /&lt;SCRIPT&gt;/);
  assert.doesNotMatch(editable, /<SCRIPT>/);
});

test("formats valid and invalid times", () => {
  assert.equal(ui.formatDateTime(null), "—");
  assert.equal(ui.formatDateTime("bad"), "—");
  assert.match(ui.formatDateTime("2026-07-02T08:00:00Z", "en-GB"), /02\/07\/2026/);
});
