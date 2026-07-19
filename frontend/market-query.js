(function marketQueryModule(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root) root.MarketQueryUI = api;
}(typeof globalThis !== "undefined" ? globalThis : this, function createMarketQueryUI() {
  const ASSET_CODE = /^asset\.[a-z0-9][a-z0-9._-]*$/;
  const INSTRUMENT_CODE = /^instrument\.[a-z0-9][a-z0-9._-]*$/;
  const PROVIDER_CODE = /^[a-z0-9][a-z0-9._-]*$/;

  function escapeHtml(value) {
    return String(value ?? "").replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll('"', "&quot;").replaceAll("'", "&#039;");
  }

  function assetCode(asset) {
    const configured = String(asset?.metadata?.market_info_asset_code || "").trim().toLowerCase();
    if (ASSET_CODE.test(configured)) return configured;
    const symbol = String(asset?.symbol || "").trim().toLowerCase().replaceAll(/[^a-z0-9_-]/g, "-");
    if (!symbol) return "";
    const prefix = { STOCK: "asset.equity.us", ETF: "asset.etf.us", CRYPTO: "asset.crypto", CASH: "asset.cash" }[asset?.asset_type];
    return prefix ? `${prefix}.${symbol}` : "";
  }

  function catalogOptions(assets) {
    return (Array.isArray(assets) ? assets : []).map((asset) => ({
      code: assetCode(asset),
      label: `${asset?.symbol || "—"} · ${asset?.name || "未命名资产"}`,
      type: asset?.asset_type || "",
    })).filter((item) => item.code);
  }

  function normalizeInstrumentPage(payload) {
    const items = Array.isArray(payload?.items) ? payload.items : [];
    return {
      items: items.map((item) => ({
        instrument_id: String(item?.instrument_id || ""),
        instrument_code: String(item?.instrument_code || ""),
        display_name: String(item?.display_name || item?.instrument_code || "未命名 Instrument"),
        providers: (Array.isArray(item?.providers) ? item.providers : []).map((provider) => ({
          provider_code: String(provider?.provider_code || ""),
          display_name: String(provider?.display_name || provider?.provider_code || "未命名 Provider"),
          is_default: provider?.is_default === true,
          priority: Number.isInteger(provider?.priority) ? provider.priority : 0,
          supported_intervals: (Array.isArray(provider?.supported_intervals) ? provider.supported_intervals : []).map(String),
        })).filter((provider) => PROVIDER_CODE.test(provider.provider_code) && provider.supported_intervals.length),
      })).filter((item) => INSTRUMENT_CODE.test(item.instrument_code) && item.providers.length),
      nextCursor: typeof payload?.next_cursor === "string" && payload.next_cursor ? payload.next_cursor : null,
    };
  }

  function resolveSelection(items, current = {}) {
    const instruments = Array.isArray(items) ? items : [];
    const instrument = instruments.find((item) => item.instrument_code === current.instrument_code) || instruments[0] || null;
    const providers = instrument?.providers || [];
    const provider = providers.find((item) => item.provider_code === current.provider_code)
      || providers.find((item) => item.is_default)
      || providers[0]
      || null;
    const intervals = provider?.supported_intervals || [];
    const interval = intervals.includes(current.interval) ? current.interval : (intervals.includes("1h") ? "1h" : (intervals[0] || ""));
    return {
      instrument_code: instrument?.instrument_code || "",
      provider_code: provider?.provider_code || "",
      interval,
    };
  }

  function localTimeToUTC(value) {
    if (!value) return "";
    const parsed = new Date(value);
    return Number.isNaN(parsed.getTime()) ? "" : parsed.toISOString();
  }

  function buildQuotesQuery(assetCodeValue) {
    const code = String(assetCodeValue || "").trim().toLowerCase();
    return ASSET_CODE.test(code) ? new URLSearchParams({ asset_code: code }).toString() : "";
  }

  function buildBarsQuery(selection, filters = {}, cursor = "") {
    const errors = {};
    const instrument = String(selection?.instrument_code || "").trim();
    const provider = String(selection?.provider_code || "").trim();
    const interval = String(selection?.interval || "").trim();
    if (!INSTRUMENT_CODE.test(instrument)) errors.instrument_code = "请选择有效的 Instrument";
    if (!PROVIDER_CODE.test(provider)) errors.provider = "请选择有效的 Provider";
    if (!interval) errors.interval = "请选择 K 线周期";
    const start = localTimeToUTC(filters.start_time);
    const end = localTimeToUTC(filters.end_time);
    if (filters.start_time && !start) errors.start_time = "开始时间格式无效";
    if (filters.end_time && !end) errors.end_time = "结束时间格式无效";
    if (start && end && Date.parse(end) <= Date.parse(start)) errors.end_time = "结束时间必须晚于开始时间";
    const order = filters.order || "desc";
    if (!new Set(["asc", "desc"]).has(order)) errors.order = "排序方式无效";
    const limit = Number(filters.limit || 200);
    if (!Number.isInteger(limit) || limit < 1 || limit > 1000) errors.limit = "每页数量必须为 1–1000 的整数";
    if (Object.keys(errors).length) return { query: "", errors };
    const query = new URLSearchParams({ instrument_code: instrument, provider, interval, order, limit: String(limit) });
    if (start) query.set("start_time", start);
    if (end) query.set("end_time", end);
    if (cursor) query.set("cursor", cursor);
    return { query: query.toString(), errors };
  }

  function formatNumber(value) {
    if (value === null || value === undefined || value === "") return "—";
    const number = Number(value);
    return Number.isFinite(number) ? new Intl.NumberFormat("zh-CN", { maximumSignificantDigits: 12 }).format(number) : String(value);
  }

  function formatTime(value) {
    if (!value) return "—";
    const parsed = new Date(value);
    return Number.isNaN(parsed.getTime()) ? String(value) : new Intl.DateTimeFormat("zh-CN", { dateStyle: "medium", timeStyle: "medium" }).format(parsed);
  }

  function renderQuotes(payload) {
    const quotes = Array.isArray(payload?.quotes) ? payload.quotes : [];
    if (!quotes.length) return '<div class="empty market-query-empty"><strong>暂无最新行情</strong><span>该资产当前没有已落库且可查询的来源快照。</span></div>';
    return quotes.map((quote) => `
      <article class="quote-card">
        <header><div><p class="eyebrow">${escapeHtml(quote.provider || "provider")}</p><h4>${escapeHtml(quote.provider_symbol || quote.instrument_code || "—")}</h4></div><strong>${escapeHtml(formatNumber(quote.price))}</strong></header>
        <code>${escapeHtml(quote.instrument_code || "—")}</code>
        <dl><div><dt>买 / 卖</dt><dd>${escapeHtml(formatNumber(quote.bid_price))} / ${escapeHtml(formatNumber(quote.ask_price))}</dd></div><div><dt>24h 高 / 低</dt><dd>${escapeHtml(formatNumber(quote.high_24h))} / ${escapeHtml(formatNumber(quote.low_24h))}</dd></div><div><dt>计价</dt><dd>${escapeHtml(quote.quote_currency || "—")}</dd></div><div><dt>市场时间</dt><dd>${escapeHtml(formatTime(quote.market_time))}</dd></div></dl>
        <footer><span>${escapeHtml(quote.provider_instrument_code || "—")}</span><span>${escapeHtml(quote.quality_status || "unknown")}</span></footer>
      </article>`).join("");
  }

  function renderBars(payload) {
    const bars = Array.isArray(payload?.bars) ? payload.bars : [];
    if (!bars.length) return '<div class="empty market-query-empty"><strong>暂无 K 线</strong><span>当前来源、周期与时间范围没有已落库数据。</span></div>';
    return `
      <div class="bar-table-wrap"><table class="bar-table"><thead><tr><th>开盘时间</th><th>开</th><th>高</th><th>低</th><th>收</th><th>成交量</th><th>修订</th></tr></thead><tbody>${bars.map((bar) => `
        <tr><td>${escapeHtml(formatTime(bar.open_time))}</td><td>${escapeHtml(formatNumber(bar.open))}</td><td>${escapeHtml(formatNumber(bar.high))}</td><td>${escapeHtml(formatNumber(bar.low))}</td><td>${escapeHtml(formatNumber(bar.close))}</td><td>${escapeHtml(formatNumber(bar.volume))}</td><td>${escapeHtml(bar.revision ?? "—")}</td></tr>`).join("")}</tbody></table></div>`;
  }

  const errorMessages = {
    ASSET_NOT_FOUND: "市场资讯目录中不存在该资产",
    INSTRUMENT_NOT_FOUND: "市场资讯目录中不存在该 Instrument",
    UNSUPPORTED_INTERVAL: "该 Provider 不支持所选周期",
    INVALID_TIME_RANGE: "K 线时间范围无效",
    INVALID_ARGUMENT: "行情查询参数无效",
    MARKET_INFO_UNAVAILABLE: "市场资讯服务暂时不可用",
  };

  function localizedError(error) {
    return errorMessages[error?.code] || error?.message || "行情查询失败";
  }

  return {
    assetCode, buildBarsQuery, buildQuotesQuery, catalogOptions, escapeHtml, formatNumber, formatTime,
    localizedError, normalizeInstrumentPage, renderBars, renderQuotes, resolveSelection,
  };
}));
