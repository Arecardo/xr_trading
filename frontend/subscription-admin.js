(function subscriptionAdminModule(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root) root.SubscriptionAdminUI = api;
}(typeof globalThis !== "undefined" ? globalThis : this, function createSubscriptionAdminUI() {
  "use strict";

  const intervals = new Set(["1h", "1d"]);
  const errorMessages = {
    SUBSCRIPTION_ALREADY_EXISTS: "该数据源、标的与周期的订阅已经存在。",
    SUBSCRIPTION_NOT_FOUND: "订阅不存在或已失效，请刷新列表后重试。",
    UNSUPPORTED_INTERVAL: "当前数据源映射不支持所选周期。",
    PERMISSION_DENIED: "当前研究账户没有订阅管理权限。",
    INVALID_ARGUMENT: "提交内容不符合订阅约束，请检查表单。",
    CONFLICT: "数据源映射当前不可用于创建或修改订阅。",
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

  function parseInteger(value, minimum, maximum, field, errors, nullable = false) {
    if (nullable && (value === "" || value === null || value === undefined)) return null;
    if (value === "" || value === null || value === undefined || !/^-?\d+$/.test(String(value))) {
      errors[field] = "必须填写整数";
      return null;
    }
    const parsed = Number(value);
    if (!Number.isSafeInteger(parsed) || parsed < minimum || parsed > maximum) {
      errors[field] = `必须在 ${minimum} 到 ${maximum} 之间`;
      return null;
    }
    return parsed;
  }

  function validateIdentity(input, errors) {
    const provider = String(input.provider || "").trim();
    const instrument = String(input.instrument_code || "").trim();
    if (!/^[a-z0-9][a-z0-9._-]*$/.test(provider)) errors.provider = "请输入有效的 Provider 编码";
    if (!/^instrument\.[a-z0-9][a-z0-9._-]*$/.test(instrument)) errors.instrument_code = "请输入以 instrument. 开头的有效编码";
    if (!intervals.has(input.interval)) errors.interval = "周期必须为 1h 或 1d";
    return { provider, instrument };
  }

  function validateReason(value, errors) {
    const reason = String(value || "").trim();
    if (!reason) errors.reason = "请填写操作原因";
    else if ([...reason].length > 512) errors.reason = "操作原因不能超过 512 个字符";
    return reason;
  }

  function buildCreatePayload(input) {
    const errors = {};
    const identity = validateIdentity(input, errors);
    const priority = parseInteger(input.priority, 0, 32767, "priority", errors);
    const closeDelay = parseInteger(input.close_delay_seconds, 0, 2147483647, "close_delay_seconds", errors);
    const revisionDelay = parseInteger(input.revision_delay_seconds, 0, 2147483647, "revision_delay_seconds", errors, true);
    const reason = validateReason(input.reason, errors);
    return {
      errors,
      payload: Object.keys(errors).length ? null : {
        provider: identity.provider,
        instrument_code: identity.instrument,
        interval: input.interval,
        enabled: Boolean(input.enabled),
        priority,
        close_delay_seconds: closeDelay,
        revision_delay_seconds: revisionDelay,
        reason,
      },
    };
  }

  function buildUpdatePayload(input) {
    const errors = {};
    const priority = parseInteger(input.priority, 0, 32767, "priority", errors);
    const closeDelay = parseInteger(input.close_delay_seconds, 0, 2147483647, "close_delay_seconds", errors);
    const revisionDelay = parseInteger(input.revision_delay_seconds, 0, 2147483647, "revision_delay_seconds", errors, true);
    const reason = validateReason(input.reason, errors);
    return {
      errors,
      payload: Object.keys(errors).length ? null : {
        enabled: Boolean(input.enabled),
        priority,
        close_delay_seconds: closeDelay,
        revision_delay_seconds: revisionDelay,
        reason,
      },
    };
  }

  function buildListQuery(filters = {}, cursor = "", limit = 20) {
    const query = new URLSearchParams();
    ["provider", "instrument_code", "interval", "enabled"].forEach((key) => {
      const value = String(filters[key] ?? "").trim();
      if (value) query.set(key, value);
    });
    query.set("limit", String(limit));
    if (cursor) query.set("cursor", cursor);
    return query.toString();
  }

  function normalizePage(payload) {
    if (!payload || !Array.isArray(payload.items)) return { items: [], nextCursor: null };
    return {
      items: payload.items.filter((item) => item && typeof item === "object"),
      nextCursor: typeof payload.next_cursor === "string" && payload.next_cursor ? payload.next_cursor : null,
    };
  }

  function localizedError(error) {
    return errorMessages[error?.code] || error?.message || "请求失败，请稍后重试。";
  }

  function renderErrors(errors) {
    const entries = Object.entries(errors);
    if (!entries.length) return "";
    return `<ul>${entries.map(([, message]) => `<li>${escapeHtml(message)}</li>`).join("")}</ul>`;
  }

  function formatDateTime(value, locale = "zh-CN") {
    if (!value || Number.isNaN(new Date(value).getTime())) return "—";
    return new Intl.DateTimeFormat(locale, {
      year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false,
    }).format(new Date(value));
  }

  function renderEditForm(item) {
    return `
      <form class="subscription-edit-form" data-subscription-edit data-id="${escapeHtml(item.subscription_id)}">
        <div class="form-grid">
          <label>启用状态<select name="enabled"><option value="true"${item.enabled ? " selected" : ""}>已启用</option><option value="false"${item.enabled ? "" : " selected"}>已停用</option></select></label>
          <label>优先级<input name="priority" type="number" min="0" max="32767" value="${Number.isInteger(item.priority) ? item.priority : 0}" required /></label>
          <label>收盘延迟（秒）<input name="close_delay_seconds" type="number" min="0" max="2147483647" value="${Number.isInteger(item.close_delay_seconds) ? item.close_delay_seconds : 0}" required /></label>
          <label>修订延迟（秒）<input name="revision_delay_seconds" type="number" min="0" max="2147483647" value="${Number.isInteger(item.revision_delay_seconds) ? item.revision_delay_seconds : ""}" placeholder="留空表示关闭" /></label>
        </div>
        <label>操作原因<textarea name="reason" rows="2" maxlength="512" placeholder="说明本次设置变更原因" required></textarea></label>
        <div class="subscription-edit-errors form-errors hidden" role="alert"></div>
        <div class="subscription-edit-actions"><button class="primary-btn" type="submit">保存设置</button><button class="ghost-btn" data-subscription-cancel type="button">取消</button></div>
      </form>`;
  }

  function renderSubscription(item, canManage, editingID, locale) {
    const enabled = Boolean(item.enabled);
    return `
      <article class="subscription-card ${enabled ? "enabled" : "disabled"}" data-subscription-id="${escapeHtml(item.subscription_id)}">
        <header class="subscription-card-head">
          <div><p class="eyebrow">${escapeHtml(item.provider || "provider")} · ${escapeHtml(item.interval || "—")}</p><h4>${escapeHtml(item.provider_symbol || item.instrument_code || "未命名标的")}</h4><code>${escapeHtml(item.instrument_code || "—")}</code></div>
          <div class="subscription-card-state"><span class="status-badge ${enabled ? "healthy" : "unknown"}">${enabled ? "已启用" : "已停用"}</span>${canManage ? `<button class="text-btn" data-subscription-edit-open="${escapeHtml(item.subscription_id)}" type="button">编辑设置</button>` : ""}</div>
        </header>
        <dl class="subscription-facts">
          <div><dt>优先级</dt><dd>${Number.isInteger(item.priority) ? item.priority : "—"}</dd></div>
          <div><dt>收盘延迟</dt><dd>${Number.isInteger(item.close_delay_seconds) ? `${item.close_delay_seconds} 秒` : "—"}</dd></div>
          <div><dt>修订延迟</dt><dd>${Number.isInteger(item.revision_delay_seconds) ? `${item.revision_delay_seconds} 秒` : "关闭"}</dd></div>
          <div><dt>更新时间</dt><dd>${escapeHtml(formatDateTime(item.updated_at, locale))}</dd></div>
        </dl>
        <div class="subscription-source"><span>${escapeHtml(item.provider_instrument_code || "—")}</span><span>${escapeHtml(item.provider_symbol || "—")}</span></div>
        ${editingID === item.subscription_id ? renderEditForm(item) : ""}
      </article>`;
  }

  function renderSubscriptions(items, options = {}) {
    if (!items.length) {
      return '<div class="empty subscription-empty"><strong>没有匹配的采集订阅</strong><span>调整筛选条件，或使用左侧表单创建首个订阅。</span></div>';
    }
    const canManage = Boolean(options.canManage);
    return items.map((item) => renderSubscription(item, canManage, options.editingID || "", options.locale || "zh-CN")).join("");
  }

  return {
    buildCreatePayload,
    buildListQuery,
    buildUpdatePayload,
    formatDateTime,
    localizedError,
    normalizePage,
    renderErrors,
    renderSubscriptions,
  };
}));
