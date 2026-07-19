(function ingestionMonitorModule(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root) root.IngestionMonitorUI = api;
}(typeof globalThis !== "undefined" ? globalThis : this, function createIngestionMonitorUI() {
  "use strict";

  const runStatuses = new Set(["pending", "running", "partial", "success", "failed", "canceled"]);
  const taskStatuses = new Set(["pending", "running", "retry_wait", "success", "failed", "canceled"]);
  const runTypes = new Set(["incremental", "backfill", "repair", "revision"]);
  const triggerTypes = new Set(["scheduler", "manual", "recovery"]);
  const intervals = new Set(["1h", "1d"]);
  const canonicalUUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

  const labels = {
    status: {
      pending: "等待执行", running: "运行中", retry_wait: "等待重试", partial: "部分成功",
      success: "成功", failed: "失败", canceled: "已取消",
    },
    runType: { incremental: "增量采集", backfill: "历史回填", repair: "修复任务", revision: "修订采集" },
    trigger: { scheduler: "调度器", manual: "手动", recovery: "恢复" },
  };

  const errorMessages = {
    INVALID_ARGUMENT: "筛选条件不符合查询约束，请检查后重试。",
    NOT_FOUND: "Run 不存在或已经不可访问。",
    TASK_NOT_FOUND: "Task 不存在或已经不可访问。",
    PERMISSION_DENIED: "当前研究账户没有执行该采集操作的权限。",
    BACKFILL_ALREADY_RUNNING: "相同范围的历史回填正在执行，请直接查看已有 Task。",
    MANUAL_RETRY_ALREADY_RUNNING: "该失败 Task 已经存在执行中的手动重试任务。",
    TASK_STATE_CONFLICT: "Task 状态已经变化，当前操作不再适用，请刷新详情。",
    SUBSCRIPTION_NOT_FOUND: "没有找到匹配且已启用的采集订阅，请先检查订阅设置。",
    CONFLICT: "采集任务当前无法执行该操作，请刷新后重试。",
    MARKET_INFO_UNAVAILABLE: "市场资讯服务暂时不可用，请稍后重试。",
    SERVICE_UNAVAILABLE: "市场资讯服务暂时不可用，请稍后重试。",
    DATABASE_UNAVAILABLE: "市场资讯数据库暂时不可用，请稍后重试。",
  };

  function escapeHtml(value) {
    return String(value ?? "")
      .replaceAll("&", "&amp;")
      .replaceAll("<", "&lt;")
      .replaceAll(">", "&gt;")
      .replaceAll('"', "&quot;")
      .replaceAll("'", "&#039;");
  }

  function formatDateTime(value, locale = "zh-CN") {
    if (!value || Number.isNaN(new Date(value).getTime())) return "—";
    return new Intl.DateTimeFormat(locale, {
      year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false,
    }).format(new Date(value));
  }

  function formatRange(start, end, locale = "zh-CN") {
    return `${formatDateTime(start, locale)} → ${formatDateTime(end, locale)}`;
  }

  function shortID(value) {
    const id = String(value || "");
    return id.length > 13 ? `${id.slice(0, 8)}…${id.slice(-4)}` : id || "—";
  }

  function normalizeTime(value, field, errors) {
    const raw = String(value || "").trim();
    if (!raw) return "";
    const parsed = new Date(raw);
    if (Number.isNaN(parsed.getTime())) {
      errors[field] = "请输入有效时间";
      return "";
    }
    return parsed.toISOString();
  }

  function appendTimeRange(query, filters, errors) {
    const from = normalizeTime(filters.created_from, "created_from", errors);
    const to = normalizeTime(filters.created_to, "created_to", errors);
    if (from && to && new Date(to) <= new Date(from)) errors.created_to = "结束时间必须晚于开始时间";
    if (from) query.set("created_from", from);
    if (to) query.set("created_to", to);
  }

  function addPage(query, cursor, limit) {
    query.set("limit", String(limit));
    if (cursor) query.set("cursor", cursor);
  }

  function buildRunQuery(filters = {}, cursor = "", limit = 20) {
    const query = new URLSearchParams();
    const errors = {};
    const runType = String(filters.run_type || "").trim();
    const trigger = String(filters.trigger_type || "").trim();
    const status = String(filters.status || "").trim();
    const requestedBy = String(filters.requested_by || "").trim();
    if (runType && !runTypes.has(runType)) errors.run_type = "不支持的 Run 类型";
    if (trigger && !triggerTypes.has(trigger)) errors.trigger_type = "不支持的触发方式";
    if (status && !runStatuses.has(status)) errors.status = "不支持的 Run 状态";
    if (requestedBy.length > 128) errors.requested_by = "请求人不能超过 128 个字符";
    if (runType) query.set("run_type", runType);
    if (trigger) query.set("trigger_type", trigger);
    if (status) query.set("status", status);
    if (requestedBy) query.set("requested_by", requestedBy);
    appendTimeRange(query, filters, errors);
    addPage(query, cursor, limit);
    return { errors, query: Object.keys(errors).length ? "" : query.toString() };
  }

  function buildTaskQuery(filters = {}, cursor = "", limit = 20) {
    const query = new URLSearchParams();
    const errors = {};
    const runID = String(filters.run_id || "").trim().toLowerCase();
    const status = String(filters.status || "").trim();
    const provider = String(filters.provider || "").trim();
    const instrument = String(filters.instrument_code || "").trim();
    const interval = String(filters.interval || "").trim();
    if (runID && !canonicalUUID.test(runID)) errors.run_id = "请输入 canonical UUID";
    if (status && !taskStatuses.has(status)) errors.status = "不支持的 Task 状态";
    if (provider && !/^[a-z0-9][a-z0-9._-]*$/.test(provider)) errors.provider = "请输入有效的 Provider 编码";
    if (instrument && !/^instrument\.[a-z0-9][a-z0-9._-]*$/.test(instrument)) errors.instrument_code = "请输入有效的 Instrument code";
    if (interval && !intervals.has(interval)) errors.interval = "周期必须为 1h 或 1d";
    if (runID) query.set("run_id", runID);
    if (status) query.set("status", status);
    if (provider) query.set("provider", provider);
    if (instrument) query.set("instrument_code", instrument);
    if (interval) query.set("interval", interval);
    appendTimeRange(query, filters, errors);
    addPage(query, cursor, limit);
    return { errors, query: Object.keys(errors).length ? "" : query.toString() };
  }

  function requiredTime(value, field, errors) {
    const normalized = normalizeTime(value, field, errors);
    if (!String(value || "").trim()) errors[field] = "请选择时间";
    return normalized;
  }

  function buildBackfillPayload(input = {}, now = new Date()) {
    const errors = {};
    const provider = String(input.provider || "").trim();
    const instrument = String(input.instrument_code || "").trim();
    const interval = String(input.interval || "").trim();
    const reason = String(input.reason || "").trim();
    const start = requiredTime(input.start_time, "start_time", errors);
    const end = requiredTime(input.end_time, "end_time", errors);
    if (!/^[a-z0-9][a-z0-9._-]*$/.test(provider)) errors.provider = "请输入有效的 Provider 编码";
    if (!/^instrument\.[a-z0-9][a-z0-9._-]*$/.test(instrument)) errors.instrument_code = "请输入有效的 Instrument code";
    if (!intervals.has(interval)) errors.interval = "周期必须为 1h 或 1d";
    if (!reason || reason.length > 512) errors.reason = "操作原因必须为 1～512 个字符";
    if (start && end && new Date(end) <= new Date(start)) errors.end_time = "结束时间必须晚于开始时间";
    const current = now instanceof Date ? now : new Date(now);
    if (end && !Number.isNaN(current.getTime()) && new Date(end) > current) errors.end_time = "结束时间不能晚于当前时间";
    return {
      errors,
      payload: Object.keys(errors).length ? null : {
        provider,
        instrument_code: instrument,
        interval,
        start_time: start,
        end_time: end,
        reason,
      },
    };
  }

  function buildTaskCommandPayload(input = {}) {
    const reason = String(input.reason || "").trim();
    const errors = {};
    if (!reason || reason.length > 512) errors.reason = "操作原因必须为 1～512 个字符";
    return { errors, payload: Object.keys(errors).length ? null : { reason } };
  }

  function availableTaskActions(status, canManage) {
    if (!canManage) return [];
    if (status === "failed") return ["retry"];
    if (["pending", "running", "retry_wait"].includes(status)) return ["cancel"];
    return [];
  }

  function normalizePage(payload) {
    if (!payload || !Array.isArray(payload.items)) return { items: [], nextCursor: null };
    return {
      items: payload.items.filter((item) => item && typeof item === "object"),
      nextCursor: typeof payload.next_cursor === "string" && payload.next_cursor ? payload.next_cursor : null,
    };
  }

  function normalizeDetail(payload, key) {
    const detail = payload?.[key];
    return detail && typeof detail === "object" && !Array.isArray(detail) ? detail : null;
  }

  function localizedError(error) {
    return errorMessages[error?.code] || error?.message || "请求失败，请稍后重试。";
  }

  function statusLabel(status) {
    return labels.status[status] || "未知状态";
  }

  function statusClass(status) {
    return runStatuses.has(status) || taskStatuses.has(status) ? status : "unknown";
  }

  function renderCounts(item) {
    const counts = [
      ["等待", item.pending_count], ["运行", item.running_count], ["重试", item.retry_wait_count],
      ["成功", item.success_count], ["失败", item.failed_count], ["取消", item.canceled_count],
    ];
    return counts.map(([label, value]) => `<span><b>${Number.isInteger(value) ? value : 0}</b>${label}</span>`).join("");
  }

  function renderRuns(items, options = {}) {
    if (!items.length) return '<div class="empty ingestion-empty"><strong>没有匹配的 Run</strong><span>调整筛选条件，或等待调度器创建新的采集 Run。</span></div>';
    return items.map((item) => {
      const id = String(item.run_id || "");
      return `
        <article class="ingestion-card ${options.selectedID === id ? "selected" : ""}">
          <header><div><p class="eyebrow">${escapeHtml(labels.runType[item.run_type] || item.run_type || "RUN")} · ${escapeHtml(labels.trigger[item.trigger_type] || item.trigger_type || "—")}</p><h4>${escapeHtml(item.run_key || shortID(id))}</h4><code title="${escapeHtml(id)}">${escapeHtml(shortID(id))}</code></div><span class="ingestion-status ${statusClass(item.status)}">${escapeHtml(statusLabel(item.status))}</span></header>
          <div class="run-counts">${renderCounts(item)}</div>
          <footer><span>${escapeHtml(formatDateTime(item.created_at, options.locale))}</span><span>${item.requested_by ? `请求人 ${escapeHtml(item.requested_by)}` : "系统调度"}</span><button class="text-btn" data-run-detail="${escapeHtml(id)}" type="button">查看详情</button></footer>
        </article>`;
    }).join("");
  }

  function renderRunDetail(run, locale = "zh-CN") {
    if (!run) return '<div class="empty ingestion-detail-empty"><strong>选择一个 Run</strong><span>详情中的状态和计数均按 Task 事实汇总。</span></div>';
    const context = run.context && typeof run.context === "object" ? Object.entries(run.context) : [];
    return `
      <div class="ingestion-detail-head"><div><p class="eyebrow">RUN DETAIL</p><h4>${escapeHtml(run.run_key || shortID(run.run_id))}</h4></div><span class="ingestion-status ${statusClass(run.status)}">${escapeHtml(statusLabel(run.status))}</span></div>
      <dl class="ingestion-detail-grid">
        <div><dt>Run ID</dt><dd><code>${escapeHtml(run.run_id || "—")}</code></dd></div>
        <div><dt>类型 / 触发</dt><dd>${escapeHtml(labels.runType[run.run_type] || run.run_type || "—")} / ${escapeHtml(labels.trigger[run.trigger_type] || run.trigger_type || "—")}</dd></div>
        <div><dt>创建时间</dt><dd>${escapeHtml(formatDateTime(run.created_at, locale))}</dd></div>
        <div><dt>开始 / 结束</dt><dd>${escapeHtml(formatRange(run.started_at, run.finished_at, locale))}</dd></div>
        <div><dt>请求人</dt><dd>${escapeHtml(run.requested_by || "系统调度")}</dd></div>
        <div><dt>Task 汇总</dt><dd>${Number.isInteger(run.task_count) ? run.task_count : 0} 个</dd></div>
      </dl>
      <div class="run-counts detail-counts">${renderCounts(run)}</div>
      ${context.length ? `<div class="safe-context"><strong>公开上下文</strong><dl>${context.map(([key, value]) => `<div><dt>${escapeHtml(key)}</dt><dd>${escapeHtml(value)}</dd></div>`).join("")}</dl></div>` : ""}
      <button class="secondary-btn" data-run-tasks="${escapeHtml(run.run_id || "")}" type="button">查看该 Run 的 Task</button>`;
  }

  function taskTitle(item) {
    return item?.provider_instrument?.provider_symbol || item?.instrument?.instrument_code || shortID(item?.task_id);
  }

  function renderTasks(items, options = {}) {
    if (!items.length) return '<div class="empty ingestion-empty"><strong>没有匹配的 Task</strong><span>调整筛选条件，或从 Run 详情进入对应任务列表。</span></div>';
    return items.map((item) => {
      const id = String(item.task_id || "");
      return `
        <article class="ingestion-card task-card ${options.selectedID === id ? "selected" : ""}">
          <header><div><p class="eyebrow">${escapeHtml(item.provider?.provider_code || "provider")} · ${escapeHtml(item.subscription?.interval || "—")}</p><h4>${escapeHtml(taskTitle(item))}</h4><code title="${escapeHtml(id)}">${escapeHtml(shortID(id))}</code></div><span class="ingestion-status ${statusClass(item.status)}">${escapeHtml(statusLabel(item.status))}</span></header>
          <dl class="task-facts"><div><dt>Instrument</dt><dd>${escapeHtml(item.instrument?.instrument_code || "—")}</dd></div><div><dt>尝试次数</dt><dd>${Number.isInteger(item.attempt_count) ? item.attempt_count : 0} / ${Number.isInteger(item.max_attempts) ? item.max_attempts : 0}</dd></div><div><dt>采集范围</dt><dd>${escapeHtml(formatRange(item.range_start, item.range_end, options.locale))}</dd></div></dl>
          ${item.error_summary ? `<div class="task-safe-error"><strong>${escapeHtml(item.error_code || "TASK_ERROR")}</strong><span>${escapeHtml(item.error_summary)}</span></div>` : ""}
          <footer><span>${escapeHtml(formatDateTime(item.updated_at, options.locale))}</span><button class="text-btn" data-task-detail="${escapeHtml(id)}" type="button">查看详情</button></footer>
        </article>`;
    }).join("");
  }

  function optionalFact(label, value) {
    if (value === null || value === undefined || value === "") return "";
    return `<div><dt>${escapeHtml(label)}</dt><dd>${escapeHtml(value)}</dd></div>`;
  }

  function renderTaskActions(task, options = {}) {
    if (!task) return "";
    if (!options.canManage) return '<div class="task-command read-only"><strong>采集操作</strong><p>当前账户仅可查看任务，不能重试或取消。</p></div>';
    const actions = availableTaskActions(task.status, true);
    if (!actions.length) return '<div class="task-command read-only"><strong>采集操作</strong><p>当前 Task 已进入终态，没有可执行的手动操作。</p></div>';
    const buttons = actions.map((action) => {
      const retry = action === "retry";
      return `<button class="${retry ? "secondary-btn" : "danger-btn"}" data-task-command="${action}" type="submit" ${options.submitting ? "disabled" : ""}>${retry ? "重试失败 Task" : "取消 Task"}</button>`;
    }).join("");
    return `
      <div class="task-command"><strong>采集操作</strong><p>${actions[0] === "retry" ? "重试会创建新的 Run 与 Task，原 Task 保持不变。" : "取消会阻止该 Task 继续写入行情数据。"}</p>
        <form data-task-command-form><label>操作原因<textarea name="reason" maxlength="512" placeholder="说明本次操作原因" required></textarea></label>
          ${options.commandError ? `<div class="form-errors" role="alert">${escapeHtml(options.commandError)}</div>` : ""}
          <div class="task-command-actions">${buttons}</div>
        </form>
      </div>`;
  }

  function renderTaskDetail(task, locale = "zh-CN", options = {}) {
    if (!task) return '<div class="empty ingestion-detail-empty"><strong>选择一个 Task</strong><span>这里仅展示服务端脱敏后的运行信息与错误摘要。</span></div>';
    const errorDetails = task.error_details && typeof task.error_details === "object" ? Object.entries(task.error_details) : [];
    return `
      <div class="ingestion-detail-head"><div><p class="eyebrow">TASK DETAIL</p><h4>${escapeHtml(taskTitle(task))}</h4></div><span class="ingestion-status ${statusClass(task.status)}">${escapeHtml(statusLabel(task.status))}</span></div>
      <dl class="ingestion-detail-grid task-detail-grid">
        <div><dt>Task ID</dt><dd><code>${escapeHtml(task.task_id || "—")}</code></dd></div>
        <div><dt>Run ID</dt><dd><code>${escapeHtml(task.run?.run_id || "—")}</code></dd></div>
        <div><dt>Subscription ID</dt><dd><code>${escapeHtml(task.subscription?.subscription_id || "—")}</code></dd></div>
        <div><dt>Provider</dt><dd>${escapeHtml(task.provider?.provider_code || "—")}<small>${escapeHtml(task.provider?.provider_id || "")}</small></dd></div>
        <div><dt>Instrument</dt><dd>${escapeHtml(task.instrument?.instrument_code || "—")}<small>${escapeHtml(task.instrument?.instrument_id || "")}</small></dd></div>
        <div><dt>ProviderInstrument</dt><dd>${escapeHtml(task.provider_instrument?.provider_instrument_code || "—")}<small>${escapeHtml(task.provider_instrument?.provider_instrument_id || "")}</small></dd></div>
        <div><dt>采集范围</dt><dd>${escapeHtml(formatRange(task.range_start, task.range_end, locale))}</dd></div>
        <div><dt>尝试次数</dt><dd>${Number.isInteger(task.attempt_count) ? task.attempt_count : 0} / ${Number.isInteger(task.max_attempts) ? task.max_attempts : 0}</dd></div>
        <div><dt>创建 / 更新</dt><dd>${escapeHtml(formatRange(task.created_at, task.updated_at, locale))}</dd></div>
        <div><dt>开始 / 结束</dt><dd>${escapeHtml(formatRange(task.started_at, task.finished_at, locale))}</dd></div>
        ${optionalFact("下次尝试", formatDateTime(task.next_attempt_at, locale) === "—" ? "" : formatDateTime(task.next_attempt_at, locale))}
        ${optionalFact("租约", task.locked_by ? `${task.locked_by} · ${formatDateTime(task.locked_until, locale)}` : "")}
        ${optionalFact("Provider Request ID", task.provider_request_id)}
        ${optionalFact("重试来源 Task", task.retry_of_task_id)}
        ${optionalFact("取消信息", task.canceled_by ? `${task.canceled_by} · ${task.cancel_reason || "未填写原因"}` : task.cancel_reason)}
      </dl>
      ${task.error_summary ? `<div class="task-detail-error"><strong>${escapeHtml(task.error_code || "TASK_ERROR")}</strong><p>${escapeHtml(task.error_summary)}</p>${errorDetails.length ? `<dl>${errorDetails.map(([key, value]) => `<div><dt>${escapeHtml(key)}</dt><dd>${escapeHtml(value)}</dd></div>`).join("")}</dl>` : ""}</div>` : ""}
      ${renderTaskActions(task, options)}`;
  }

  return {
    buildRunQuery,
    buildBackfillPayload,
    buildTaskCommandPayload,
    buildTaskQuery,
    availableTaskActions,
    formatDateTime,
    formatRange,
    localizedError,
    normalizeDetail,
    normalizePage,
    renderRunDetail,
    renderRuns,
    renderTaskDetail,
    renderTaskActions,
    renderTasks,
    shortID,
    statusLabel,
  };
}));
