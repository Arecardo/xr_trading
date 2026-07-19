const test = require("node:test");
const assert = require("node:assert/strict");
const ui = require("../../frontend/ingestion-monitor.js");

const runID = "019f1452-90f7-7992-a87a-ca272789160f";
const taskID = "019f1452-90f7-7992-a87a-ca2727891610";

const run = {
  run_id: runID,
  run_key: "backfill.manual.test",
  run_type: "backfill",
  trigger_type: "manual",
  status: "partial",
  scheduled_at: "2026-07-18T20:00:00Z",
  started_at: "2026-07-18T20:01:00Z",
  finished_at: "2026-07-18T20:03:00Z",
  requested_by: "admin@example.test",
  task_count: 6,
  pending_count: 0,
  running_count: 0,
  retry_wait_count: 1,
  success_count: 4,
  failed_count: 1,
  canceled_count: 0,
  context: { reason: "repair <bars>", provider: "bybit" },
  created_at: "2026-07-18T20:00:00Z",
};

const task = {
  task_id: taskID,
  run: { run_id: runID, run_type: "backfill", trigger_type: "manual" },
  subscription: { subscription_id: "019f1452-90f7-7992-a87a-ca2727891611", interval: "1h" },
  provider: { provider_id: "019f1452-90f7-7992-a87a-ca2727891612", provider_code: "bybit" },
  instrument: { instrument_id: "019f1452-90f7-7992-a87a-ca2727891613", instrument_code: "instrument.bybit.spot.btc-usdt" },
  provider_instrument: { provider_instrument_id: "019f1452-90f7-7992-a87a-ca2727891614", provider_instrument_code: "provider.bybit.btcusdt", provider_symbol: "BTCUSDT" },
  retry_of_task_id: "019f1452-90f7-7992-a87a-ca2727891615",
  range_start: "2026-07-18T18:00:00Z",
  range_end: "2026-07-18T19:00:00Z",
  status: "retry_wait",
  attempt_count: 2,
  max_attempts: 5,
  next_attempt_at: "2026-07-18T20:05:00Z",
  locked_by: "worker-1",
  locked_until: "2026-07-18T20:06:00Z",
  started_at: "2026-07-18T20:01:00Z",
  finished_at: null,
  provider_request_id: "request-<safe>",
  error_code: "network",
  error_summary: "provider network request failed",
  error_details: { provider_code: "bybit<script>" },
  canceled_by: "operator",
  cancel_reason: "stop <task>",
  created_at: "2026-07-18T20:00:00Z",
  updated_at: "2026-07-18T20:02:00Z",
};

test("builds canonical Run query with UTC time range and cursor", () => {
  const result = ui.buildRunQuery({
    run_type: "backfill", trigger_type: "manual", status: "partial", requested_by: " admin ",
    created_from: "2026-07-18T20:00:00Z", created_to: "2026-07-19T20:00:00Z",
  }, "cursor-token", 50);
  assert.deepEqual(result.errors, {});
  const query = new URLSearchParams(result.query);
  assert.equal(query.get("run_type"), "backfill");
  assert.equal(query.get("requested_by"), "admin");
  assert.equal(query.get("created_from"), "2026-07-18T20:00:00.000Z");
  assert.equal(query.get("cursor"), "cursor-token");
  assert.equal(query.get("limit"), "50");
});

test("validates Run filters and exclusive time range", () => {
  const invalid = ui.buildRunQuery({
    run_type: "other", trigger_type: "cron", status: "retry_wait", requested_by: "x".repeat(129),
    created_from: "bad", created_to: "also-bad",
  });
  assert.equal(invalid.query, "");
  assert.deepEqual(Object.keys(invalid.errors).sort(), ["created_from", "created_to", "requested_by", "run_type", "status", "trigger_type"]);
  const reversed = ui.buildRunQuery({ created_from: "2026-07-20T00:00:00Z", created_to: "2026-07-19T00:00:00Z" });
  assert.equal(reversed.errors.created_to, "结束时间必须晚于开始时间");
});

test("builds and validates Task queries", () => {
  const result = ui.buildTaskQuery({
    run_id: runID.toUpperCase(), status: "failed", provider: "bybit", instrument_code: "instrument.bybit.spot.btc-usdt",
    interval: "1h", created_from: "2026-07-18T00:00:00Z", created_to: "2026-07-19T00:00:00Z",
  }, "next", 25);
  assert.deepEqual(result.errors, {});
  const query = new URLSearchParams(result.query);
  assert.equal(query.get("run_id"), runID);
  assert.equal(query.get("status"), "failed");
  assert.equal(query.get("provider"), "bybit");
  assert.equal(query.get("limit"), "25");
  const invalid = ui.buildTaskQuery({ run_id: "bad", status: "partial", provider: "BYBIT!", instrument_code: "asset.btc", interval: "5m" });
  assert.equal(invalid.query, "");
  assert.deepEqual(Object.keys(invalid.errors).sort(), ["instrument_code", "interval", "provider", "run_id", "status"]);
});

test("builds one bounded backfill payload and rejects invalid ranges", () => {
  const result = ui.buildBackfillPayload({
    provider: " bybit ", instrument_code: " instrument.bybit.spot.btc-usdt ", interval: "1h",
    start_time: "2026-07-18T18:00:00Z", end_time: "2026-07-18T19:00:00Z", reason: " history repair ",
  }, new Date("2026-07-18T20:00:00Z"));
  assert.deepEqual(result.errors, {});
  assert.deepEqual(result.payload, {
    provider: "bybit", instrument_code: "instrument.bybit.spot.btc-usdt", interval: "1h",
    start_time: "2026-07-18T18:00:00.000Z", end_time: "2026-07-18T19:00:00.000Z", reason: "history repair",
  });
  const invalid = ui.buildBackfillPayload({
    provider: "BYBIT!", instrument_code: "asset.btc", interval: "5m", start_time: "",
    end_time: "2099-01-01T00:00:00Z", reason: " ",
  }, new Date("2026-07-18T20:00:00Z"));
  assert.equal(invalid.payload, null);
  assert.deepEqual(Object.keys(invalid.errors).sort(), ["end_time", "instrument_code", "interval", "provider", "reason", "start_time"]);
  assert.equal(ui.buildBackfillPayload({
    provider: "bybit", instrument_code: "instrument.bybit.btc", interval: "1d",
    start_time: "2026-07-19T00:00:00Z", end_time: "2026-07-18T00:00:00Z", reason: "history",
  }, new Date("2026-07-20T00:00:00Z")).errors.end_time, "结束时间必须晚于开始时间");
});

test("validates task command reasons and derives status-safe actions", () => {
  assert.deepEqual(ui.buildTaskCommandPayload({ reason: " retry after key rotation " }), { errors: {}, payload: { reason: "retry after key rotation" } });
  assert.equal(ui.buildTaskCommandPayload({ reason: "" }).payload, null);
  assert.equal(ui.buildTaskCommandPayload({ reason: "x".repeat(513) }).errors.reason, "操作原因必须为 1～512 个字符");
  assert.deepEqual(ui.availableTaskActions("failed", true), ["retry"]);
  assert.deepEqual(ui.availableTaskActions("retry_wait", true), ["cancel"]);
  assert.deepEqual(ui.availableTaskActions("success", true), []);
  assert.deepEqual(ui.availableTaskActions("failed", false), []);
});

test("normalizes list pages and details safely", () => {
  assert.deepEqual(ui.normalizePage(null), { items: [], nextCursor: null });
  assert.deepEqual(ui.normalizePage({ items: [run, null, "bad"], next_cursor: "next" }), { items: [run], nextCursor: "next" });
  assert.equal(ui.normalizePage({ items: [], next_cursor: "" }).nextCursor, null);
  assert.equal(ui.normalizePage({ items: [], next_cursor: 7 }).nextCursor, null);
  assert.deepEqual(ui.normalizeDetail({ run }, "run"), run);
  assert.equal(ui.normalizeDetail({ run: [] }, "run"), null);
  assert.equal(ui.normalizeDetail(null, "task"), null);
});

test("supports unfiltered queries and sparse API projections", () => {
  assert.equal(new URLSearchParams(ui.buildRunQuery().query).get("limit"), "20");
  assert.equal(new URLSearchParams(ui.buildTaskQuery().query).get("limit"), "20");
  const sparseRun = ui.renderRuns([{ run_id: "", run_key: "", status: "pending" }]);
  assert.match(sparseRun, /等待执行/);
  assert.match(sparseRun, /系统调度/);
  const sparseTask = ui.renderTasks([{
    task_id: "", status: "pending", provider: null, subscription: null, instrument: null,
    provider_instrument: null, attempt_count: null, max_attempts: null, range_start: null, range_end: null, updated_at: null,
  }]);
  assert.match(sparseTask, /等待执行/);
  const canceled = ui.renderTaskDetail({ ...task, canceled_by: null, cancel_reason: "policy", error_details: {}, next_attempt_at: "bad" });
  assert.match(canceled, /policy/);
});

test("formats identifiers, dates, ranges, statuses and errors", () => {
  assert.match(ui.formatDateTime("2026-07-18T20:00:00Z", "en-GB"), /2026/);
  assert.equal(ui.formatDateTime("bad"), "—");
  assert.match(ui.formatRange("2026-07-18T20:00:00Z", null), /→ —/);
  assert.equal(ui.shortID("short"), "short");
  assert.equal(ui.shortID(""), "—");
  assert.match(ui.shortID(runID), /…/);
  assert.equal(ui.statusLabel("retry_wait"), "等待重试");
  assert.equal(ui.statusLabel("mystery"), "未知状态");
  assert.match(ui.localizedError({ code: "TASK_NOT_FOUND" }), /Task/);
  assert.match(ui.localizedError({ code: "BACKFILL_ALREADY_RUNNING" }), /正在执行/);
  assert.match(ui.localizedError({ code: "TASK_STATE_CONFLICT" }), /状态已经变化/);
  assert.equal(ui.localizedError({ code: "OTHER", message: "safe fallback" }), "safe fallback");
  assert.match(ui.localizedError(null), /请求失败/);
});

test("renders Run empty, list and escaped detail states", () => {
  assert.match(ui.renderRuns([]), /没有匹配的 Run/);
  const list = ui.renderRuns([run, { ...run, run_id: taskID, status: "mystery", run_type: "unknown", trigger_type: "unknown", run_key: "" }], { selectedID: runID, locale: "en-GB" });
  assert.match(list, /selected/);
  assert.match(list, /部分成功/);
  assert.match(list, /未知状态/);
  assert.doesNotMatch(list, /<script>/);
  assert.match(ui.renderRunDetail(null), /选择一个 Run/);
  const detail = ui.renderRunDetail(run, "en-GB");
  assert.match(detail, /查看该 Run 的 Task/);
  assert.match(detail, /repair &lt;bars&gt;/);
  assert.doesNotMatch(detail, /repair <bars>/);
  assert.match(ui.renderRunDetail({ ...run, context: null, requested_by: null, started_at: null, finished_at: null }), /系统调度/);
});

test("renders Task empty, list and fully sanitized detail", () => {
  assert.match(ui.renderTasks([]), /没有匹配的 Task/);
  const list = ui.renderTasks([task, { ...task, task_id: runID, error_summary: null, provider_instrument: {}, instrument: {}, subscription: {}, provider: {}, status: "success" }], { selectedID: taskID, locale: "en-GB" });
  assert.match(list, /selected/);
  assert.match(list, /等待重试/);
  assert.match(list, /provider network request failed/);
  assert.match(ui.renderTaskDetail(null), /选择一个 Task/);
  const detail = ui.renderTaskDetail(task, "en-GB", { canManage: true });
  assert.match(detail, /provider_code/);
  assert.match(detail, /bybit&lt;script&gt;/);
  assert.match(detail, /request-&lt;safe&gt;/);
  assert.match(detail, /stop &lt;task&gt;/);
  assert.doesNotMatch(detail, /<script>/);
  assert.match(detail, /取消 Task/);
  assert.match(ui.renderTaskDetail({ ...task, status: "failed" }, "zh-CN", { canManage: true, commandError: "bad <reason>" }), /重试失败 Task/);
  assert.match(ui.renderTaskDetail({ ...task, status: "success" }, "zh-CN", { canManage: true }), /已进入终态/);
  assert.match(ui.renderTaskDetail(task, "zh-CN", { canManage: false }), /仅可查看任务/);
  assert.doesNotMatch(ui.renderTaskActions({ ...task, status: "failed" }, { canManage: true, submitting: true }), /type="submit" >/);
  const minimal = ui.renderTaskDetail({
    ...task, next_attempt_at: null, locked_by: null, provider_request_id: null, retry_of_task_id: null,
    canceled_by: null, cancel_reason: null, error_summary: null, error_details: null,
  });
  assert.doesNotMatch(minimal, /task-detail-error/);
});
