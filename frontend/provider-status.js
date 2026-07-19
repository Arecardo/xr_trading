(function providerStatusModule(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root) root.ProviderStatusUI = api;
}(typeof globalThis !== "undefined" ? globalThis : this, function createProviderStatusUI() {
  "use strict";

  const statusLabels = {
    healthy: "健康",
    degraded: "降级",
    unhealthy: "异常",
    unknown: "未知",
  };
  const configuredLabels = { active: "已启用", degraded: "配置降级", disabled: "已停用" };
  const marketLabels = { crypto_spot: "加密货币现货", us_equity: "美股" };
  const freshnessLabels = { fresh: "新鲜", delayed: "延迟", unknown: "未知", not_applicable: "不适用" };
  const sessionLabels = { continuous: "7×24", regular: "常规时段" };

  function escapeHtml(value) {
    return String(value ?? "")
      .replaceAll("&", "&amp;")
      .replaceAll("<", "&lt;")
      .replaceAll(">", "&gt;")
      .replaceAll('"', "&quot;")
      .replaceAll("'", "&#039;");
  }

  function safeStatus(value) {
    return Object.hasOwn(statusLabels, value) ? value : "unknown";
  }

  function formatDateTime(value, locale = "zh-CN") {
    if (!value) return "—";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return "—";
    return new Intl.DateTimeFormat(locale, {
      year: "numeric", month: "2-digit", day: "2-digit",
      hour: "2-digit", minute: "2-digit", second: "2-digit",
      hour12: false,
    }).format(date);
  }

  function formatDelay(value) {
    if (!Number.isFinite(value) || value < 0) return "—";
    const seconds = Math.floor(value);
    if (seconds < 60) return `${seconds} 秒`;
    if (seconds < 3600) return `${Math.floor(seconds / 60)} 分 ${seconds % 60} 秒`;
    const hours = Math.floor(seconds / 3600);
    const minutes = Math.floor((seconds % 3600) / 60);
    return minutes ? `${hours} 小时 ${minutes} 分` : `${hours} 小时`;
  }

  function normalizeItems(payload) {
    if (!payload || !Array.isArray(payload.items)) return [];
    return payload.items.filter((item) => item && typeof item === "object");
  }

  function summarize(items) {
    const summary = { total: items.length, healthy: 0, attention: 0, activeSubscriptions: 0, delayedSubscriptions: 0 };
    items.forEach((provider) => {
      const health = safeStatus(provider.health_status);
      if (health === "healthy") summary.healthy += 1;
      if (health !== "healthy" && provider.configured_status !== "disabled") summary.attention += 1;
      (Array.isArray(provider.scopes) ? provider.scopes : []).forEach((scope) => {
        summary.activeSubscriptions += Number.isFinite(scope.active_subscriptions) ? scope.active_subscriptions : 0;
        summary.delayedSubscriptions += Number.isFinite(scope.delayed_subscriptions) ? scope.delayed_subscriptions : 0;
      });
    });
    return summary;
  }

  function renderSummary(items) {
    const value = summarize(items);
    return `
      <article class="metric-card"><span>数据源</span><strong>${value.total}</strong><small>当前目录中的 Provider</small></article>
      <article class="metric-card"><span>健康</span><strong>${value.healthy}</strong><small>采集与新鲜度正常</small></article>
      <article class="metric-card"><span>需要关注</span><strong>${value.attention}</strong><small>降级、异常或状态未知</small></article>
      <article class="metric-card"><span>活跃 / 延迟订阅</span><strong>${value.activeSubscriptions} / ${value.delayedSubscriptions}</strong><small>全部市场与周期汇总</small></article>
    `;
  }

  function renderScope(scope, locale) {
    const health = safeStatus(scope.health_status);
    const marketState = scope.market_state === "closed" ? "closed" : scope.market_state === "open" ? "open" : "unknown";
    const marketStateLabel = marketState === "closed" ? "休市" : marketState === "open" ? "开市" : "市场状态未知";
    const delay = scope.freshness_status === "not_applicable" ? "停止计算" : formatDelay(scope.data_delay_seconds);
    const nextOpen = scope.next_market_open_at ? `<span>下次开市 <b>${escapeHtml(formatDateTime(scope.next_market_open_at, locale))}</b></span>` : "";
    return `
      <article class="provider-scope">
        <div class="provider-scope-head">
          <div><strong>${escapeHtml(marketLabels[scope.market] || scope.market || "未知市场")}</strong><span>${escapeHtml(scope.interval || "—")} · ${escapeHtml(sessionLabels[scope.session_type] || scope.session_type || "—")}</span></div>
          <div class="status-badges"><span class="status-badge ${health}">${statusLabels[health]}</span><span class="market-badge ${marketState}">${marketStateLabel}</span></div>
        </div>
        <dl class="scope-facts">
          <div><dt>新鲜度</dt><dd>${escapeHtml(freshnessLabels[scope.freshness_status] || "未知")}</dd></div>
          <div><dt>数据延迟</dt><dd>${escapeHtml(delay)}</dd></div>
          <div><dt>活跃订阅</dt><dd>${Number.isFinite(scope.active_subscriptions) ? scope.active_subscriptions : 0}</dd></div>
          <div><dt>延迟订阅</dt><dd>${Number.isFinite(scope.delayed_subscriptions) ? scope.delayed_subscriptions : 0}</dd></div>
        </dl>
        ${nextOpen ? `<div class="scope-next-open">${nextOpen}</div>` : ""}
      </article>
    `;
  }

  function renderProvider(provider, locale) {
    const health = safeStatus(provider.health_status);
    const configured = configuredLabels[provider.configured_status] || "未知配置";
    const scopes = Array.isArray(provider.scopes) ? provider.scopes : [];
    return `
      <article class="panel provider-card">
        <header class="provider-card-head">
          <div class="provider-identity">
            <span class="provider-mark" aria-hidden="true">${escapeHtml(String(provider.provider_code || "?").slice(0, 2).toUpperCase())}</span>
            <div><p class="eyebrow">${escapeHtml(provider.provider_type || "provider")}</p><h3>${escapeHtml(provider.display_name || provider.provider_code || "未命名数据源")}</h3><code>${escapeHtml(provider.provider_code || "—")}</code></div>
          </div>
          <div class="provider-state"><span class="status-badge ${health}">${statusLabels[health]}</span><small>${escapeHtml(configured)}</small></div>
        </header>
        <dl class="provider-facts">
          <div><dt>最近成功</dt><dd>${escapeHtml(formatDateTime(provider.last_success_at, locale))}</dd></div>
          <div><dt>最近失败</dt><dd>${escapeHtml(formatDateTime(provider.last_failure_at, locale))}</dd></div>
          <div><dt>连续失败</dt><dd>${Number.isFinite(provider.consecutive_failures) ? provider.consecutive_failures : 0} 次</dd></div>
          <div><dt>检查时间</dt><dd>${escapeHtml(formatDateTime(provider.checked_at, locale))}</dd></div>
        </dl>
        <section class="provider-scopes">
          ${scopes.length ? scopes.map((scope) => renderScope(scope, locale)).join("") : '<div class="empty compact-empty"><strong>尚无有效采集范围</strong><span>该数据源仍保留在目录中，但目前没有可计算状态的启用订阅。</span></div>'}
        </section>
      </article>
    `;
  }

  function renderProviders(items, locale = "zh-CN") {
    if (!items.length) {
      return '<div class="empty provider-empty"><strong>尚未配置数据源</strong><span>Provider 目录为空；配置完成后，这里会展示采集健康状态与行情新鲜度。</span></div>';
    }
    return items.map((provider) => renderProvider(provider, locale)).join("");
  }

  return { formatDateTime, formatDelay, normalizeItems, renderProviders, renderSummary, safeStatus, summarize };
}));
