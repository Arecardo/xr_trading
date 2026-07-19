function newIngestionState() {
  return {
    view: "runs",
    runs: {
      items: [], nextCursor: null, loading: false, loaded: false, error: "", requestId: "", selectedID: "", detail: null, detailLoading: false, detailError: "", detailRequestId: "",
      filters: { run_type: "", trigger_type: "", status: "", requested_by: "", created_from: "", created_to: "" },
    },
    tasks: {
      items: [], nextCursor: null, loading: false, loaded: false, error: "", requestId: "", selectedID: "", detail: null, detailLoading: false, detailError: "", detailRequestId: "",
      filters: { run_id: "", status: "", provider: "", instrument_code: "", interval: "", created_from: "", created_to: "" },
    },
    backfill: { submitting: false, errors: {} },
    command: { kind: "", taskID: "", payload: null, summary: "", submitting: false, error: "", requestId: "" },
  };
}

function newMarketQueryState() {
  return {
    assetCode: "", options: [], nextOptionsCursor: null, optionsLoading: false, optionsLoaded: false, optionsError: "", optionsRequestId: "", generation: 0,
    selection: { instrument_code: "", provider_code: "", interval: "" },
    quotes: { payload: null, loading: false, error: "", requestId: "" },
    bars: { items: [], payload: null, nextCursor: null, loading: false, error: "", requestId: "", errors: {}, filters: { start_time: "", end_time: "", order: "desc", limit: "200" } },
  };
}

const state = {
  token: localStorage.getItem("xr_token") || "",
  theme: localStorage.getItem("xr_theme") || "light",
  authTab: "login",
  page: "overview",
  user: null,
  portfolios: [],
  assets: [],
  members: [],
  selectedPortfolioId: null,
  userMenuOpen: false,
  marketDataView: "providers",
  providerStatus: { items: [], loading: false, loaded: false, error: "", requestId: "" },
  subscriptions: {
    items: [], nextCursor: null, loading: false, loaded: false, error: "", requestId: "", editingID: "",
    filters: { provider: "", instrument_code: "", interval: "", enabled: "" },
  },
  ingestion: newIngestionState(),
  marketQuery: newMarketQueryState(),
};

const $ = (selector) => document.querySelector(selector);
const $$ = (selector) => [...document.querySelectorAll(selector)];

const labels = {
  status: { draft: "草稿", active: "运行中", paused: "已暂停", archived: "已归档" },
  mode: { research: "研究", backtest: "回测", paper: "模拟盘", live: "实盘" },
  risk: { conservative: "稳健", moderate: "均衡", growth: "成长", aggressive: "进取" },
  member: { candidate: "研究候选", approved: "允许配置", restricted: "禁止新增" },
  asset: { STOCK: "美股", ETF: "ETF", CRYPTO: "加密资产", CASH: "现金" },
};

function showToast(message) {
  const toast = $("#toast");
  toast.textContent = message;
  toast.classList.remove("hidden");
  window.clearTimeout(showToast.timer);
  showToast.timer = window.setTimeout(() => toast.classList.add("hidden"), 2800);
}

function applyTheme() {
  const isLight = state.theme === "light";
  document.body.classList.toggle("theme-dark", !isLight);
  $("#themeToggleBtn").textContent = isLight ? "☾" : "☀";
  $("#themeToggleBtn").title = isLight ? "切换到暗色模式" : "切换到明亮模式";
  $("#themeToggleBtn").setAttribute("aria-label", $("#themeToggleBtn").title);
}

function toggleTheme() {
  state.theme = state.theme === "light" ? "dark" : "light";
  localStorage.setItem("xr_theme", state.theme);
  applyTheme();
}

async function api(path, options = {}) {
  const headers = { "Content-Type": "application/json", ...(options.headers || {}) };
  if (state.token) headers.Authorization = `Bearer ${state.token}`;
  const response = await fetch(path, { ...options, headers });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) {
    const detail = payload.error && typeof payload.error === "object" ? payload.error : {};
    const error = new Error(detail.message || payload.error || "请求失败");
    error.code = detail.code || "REQUEST_FAILED";
    error.requestId = detail.request_id || response.headers.get("X-Request-ID") || "";
    error.details = detail.details || {};
    throw error;
  }
  return payload;
}

function setSession(token, user) {
  state.token = token;
  state.user = user;
  if (token) localStorage.setItem("xr_token", token);
  else localStorage.removeItem("xr_token");
  renderShell();
}

function clearWorkspace() {
  state.portfolios = [];
  state.assets = [];
  state.members = [];
  state.selectedPortfolioId = null;
  state.providerStatus = { items: [], loading: false, loaded: false, error: "", requestId: "" };
  state.subscriptions = {
    items: [], nextCursor: null, loading: false, loaded: false, error: "", requestId: "", editingID: "",
    filters: { provider: "", instrument_code: "", interval: "", enabled: "" },
  };
  state.ingestion = newIngestionState();
  state.marketQuery = newMarketQueryState();
  const subscriptionFilterForm = $("#subscriptionFilterForm");
  if (subscriptionFilterForm) subscriptionFilterForm.reset();
  const subscriptionCreateForm = $("#subscriptionCreateForm");
  if (subscriptionCreateForm) {
    resetSubscriptionCreateForm(subscriptionCreateForm);
    showSubscriptionFormErrors($("#subscriptionCreateErrors"), {});
  }
  ["#runFilterForm", "#taskFilterForm", "#backfillForm"].forEach((selector) => {
    const form = $(selector);
    if (form) form.reset();
  });
  showIngestionFilterErrors($("#runFilterErrors"), {});
  showIngestionFilterErrors($("#taskFilterErrors"), {});
  showIngestionFilterErrors($("#backfillErrors"), {});
  closeIngestionConfirmation(true);
}

function setAuthTab(tabName) {
  state.authTab = tabName;
  const isLogin = tabName === "login";
  $("#loginTabBtn").classList.toggle("active", isLogin);
  $("#registerTabBtn").classList.toggle("active", !isLogin);
  $("#loginTabBtn").setAttribute("aria-pressed", String(isLogin));
  $("#registerTabBtn").setAttribute("aria-pressed", String(!isLogin));
  $("#loginForm").classList.toggle("active", isLogin);
  $("#registerForm").classList.toggle("active", !isLogin);
}

function setUserMenu(open) {
  state.userMenuOpen = open;
  $("#userMenu").classList.toggle("hidden", !open);
  $("#userMenuButton").setAttribute("aria-expanded", String(open));
}

function setPage(page) {
  const target = $(`#page-${page}`);
  if (!target) return;
  state.page = page;
  $$(".page").forEach((item) => item.classList.toggle("active", item === target));
  $$(".nav-item").forEach((item) => item.classList.toggle("active", item.dataset.page === page));
  setUserMenu(false);
  if (page === "market-data" && state.user) {
    if (state.marketDataView === "providers" && !state.providerStatus.loaded) loadProviderStatus();
    if (state.marketDataView === "subscriptions" && !state.subscriptions.loaded) loadSubscriptions();
    if (state.marketDataView === "operations") loadCurrentIngestionIfNeeded();
  }
  if (page === "assets" && state.user && state.assets.length && !state.marketQuery.optionsLoaded && !state.marketQuery.optionsLoading) initializeMarketQuery();
}

function canManageSubscriptions() {
  return Boolean(state.user?.permissions?.includes("subscriptions.manage"));
}

function canManageIngestion() {
  return Boolean(state.user?.permissions?.includes("ingestion.manage"));
}

function setMarketDataView(view) {
  if (!new Set(["providers", "subscriptions", "operations"]).has(view)) return;
  state.marketDataView = view;
  $$('[data-market-data-view]').forEach((button) => {
    const active = button.dataset.marketDataView === view;
    button.classList.toggle("active", active);
    button.setAttribute("aria-selected", String(active));
  });
  $("#marketDataProvidersPanel").classList.toggle("active", view === "providers");
  $("#marketDataSubscriptionsPanel").classList.toggle("active", view === "subscriptions");
  $("#marketDataOperationsPanel").classList.toggle("active", view === "operations");
  if (view === "providers" && state.user && !state.providerStatus.loaded) loadProviderStatus();
  if (view === "subscriptions" && state.user && !state.subscriptions.loaded) loadSubscriptions();
  if (view === "operations" && state.user) loadCurrentIngestionIfNeeded();
}

function renderUser() {
  const user = state.user;
  if (!user) return;
  $("#sessionName").textContent = user.username;
  $("#dropdownUserName").textContent = user.username;
  $("#dropdownUserEmail").textContent = user.email;
  $("#dropdownUserInfo").innerHTML = `
    <dt>用户 ID</dt><dd>${user.id}</dd>
    <dt>状态</dt><dd>${escapeHtml(user.status)}</dd>
    <dt>采集订阅</dt><dd>${canManageSubscriptions() ? "可管理" : "只读"}</dd>
    <dt>采集操作</dt><dd>${canManageIngestion() ? "可管理" : "只读"}</dd>
    <dt>创建时间</dt><dd>${formatDate(user.created_at)}</dd>
    <dt>更新时间</dt><dd>${formatDate(user.updated_at)}</dd>
  `;
  $("#settingsUserName").textContent = user.username;
  $("#settingsUserEmail").textContent = user.email;
}

function renderShell() {
  const loggedIn = Boolean(state.token && state.user);
  $("#authPanel").classList.toggle("hidden", loggedIn);
  $("#appPanel").classList.toggle("hidden", !loggedIn);
  $("#userMenuShell").classList.toggle("hidden", !loggedIn);
  $("#workspaceMode").classList.toggle("hidden", !loggedIn);
  if (!loggedIn) {
    setUserMenu(false);
    $("#sessionName").textContent = "未登录";
    return;
  }
  renderUser();
  setUserMenu(false);
}

function renderOverview() {
  $("#overviewPortfolioCount").textContent = state.portfolios.length;
  $("#overviewActiveCount").textContent = state.portfolios.filter((item) => item.status === "active").length;
  $("#overviewAssetCount").textContent = state.assets.length;
  const container = $("#overviewPortfolioList");
  if (!state.portfolios.length) {
    container.innerHTML = `<div class="empty"><strong>尚未创建投资组合</strong><span>从投资目标和风险边界开始，而不是从一笔交易开始。</span></div>`;
    return;
  }
  container.innerHTML = state.portfolios.slice(0, 4).map((portfolio) => `
    <button class="compact-row" data-overview-portfolio="${portfolio.id}" type="button">
      <span><strong>${escapeHtml(portfolio.name)}</strong><small>${escapeHtml(labels.risk[portfolio.risk_level])} · ${escapeHtml(labels.mode[portfolio.execution_mode])}</small></span>
      <span class="row-meta"><b>${portfolio.member_count}</b> 个资产 <i class="status-dot ${portfolio.status}"></i></span>
    </button>
  `).join("");
}

function renderPortfolios() {
  $("#portfolioCountBadge").textContent = state.portfolios.length;
  const container = $("#portfolioList");
  if (!state.portfolios.length) {
    container.innerHTML = `<div class="empty"><strong>组合库还是空的</strong><span>创建第一个组合，定义允许的资产范围与运行模式。</span></div>`;
    $("#portfolioDetail").classList.add("hidden");
    return;
  }
  container.innerHTML = state.portfolios.map((portfolio) => {
    const selected = portfolio.id === state.selectedPortfolioId;
    return `
      <article class="portfolio-card ${selected ? "selected" : ""}">
        <button class="portfolio-select" data-action="select" data-id="${portfolio.id}" type="button">
          <span class="portfolio-card-top"><span class="asset-monogram">${escapeHtml(portfolio.base_currency)}</span><span><strong>${escapeHtml(portfolio.name)}</strong><small>${escapeHtml(portfolio.description || "尚未填写投资目标")}</small></span></span>
          <span class="portfolio-tags">
            <i class="status-tag ${portfolio.status}">${escapeHtml(labels.status[portfolio.status])}</i>
            <i>${escapeHtml(labels.risk[portfolio.risk_level])}</i>
            <i>${escapeHtml(labels.mode[portfolio.execution_mode])}</i>
            <i>基准 ${escapeHtml(portfolio.benchmark)}</i>
          </span>
          <span class="portfolio-footer"><span>${portfolio.allowed_asset_types.map((type) => labels.asset[type]).join(" · ")}</span><b>${portfolio.member_count} 个资产</b></span>
        </button>
        <button class="archive-btn" data-action="archive" data-id="${portfolio.id}" type="button" aria-label="归档 ${escapeHtml(portfolio.name)}">归档</button>
      </article>
    `;
  }).join("");
}

function renderPortfolioDetail() {
  const portfolio = state.portfolios.find((item) => item.id === state.selectedPortfolioId);
  const panel = $("#portfolioDetail");
  if (!portfolio) {
    panel.classList.add("hidden");
    return;
  }
  panel.classList.remove("hidden");
  $("#portfolioDetailTitle").textContent = `${portfolio.name} · 组合资产`;
  $("#portfolioDetailMeta").textContent = `${labels.mode[portfolio.execution_mode]} · ${labels.risk[portfolio.risk_level]} · ${portfolio.base_currency}`;

  const available = state.assets.filter((asset) => portfolio.allowed_asset_types.includes(asset.asset_type) && !state.members.some((member) => member.asset_id === asset.id));
  $("#memberAssetSelect").innerHTML = available.length
    ? available.map((asset) => `<option value="${escapeHtml(asset.id)}">${escapeHtml(asset.symbol)} · ${escapeHtml(asset.name)} · ${escapeHtml(labels.asset[asset.asset_type])}</option>`).join("")
    : `<option value="">没有可添加的资产</option>`;
  $("#memberForm").querySelector("button").disabled = !available.length;

  const list = $("#memberList");
  if (!state.members.length) {
    list.innerHTML = `<div class="empty"><strong>尚无组合资产</strong><span>候选资产用于研究；只有“允许配置”资产才能进入目标权重。</span></div>`;
    return;
  }
  list.innerHTML = state.members.map((member) => `
    <article class="member-row">
      <span class="asset-type-mark ${member.asset_type.toLowerCase()}">${member.asset_type === "CRYPTO" ? "₿" : member.symbol.slice(0, 1)}</span>
      <span><strong>${escapeHtml(member.symbol)} <small>${escapeHtml(member.name)}</small></strong><em>${escapeHtml(member.venue || member.quote_currency)} · ${escapeHtml(labels.asset[member.asset_type])}</em></span>
      <span class="member-weight">上限 <b>${formatPct(member.target_weight_max)}</b><small>${escapeHtml(labels.member[member.member_status])}</small></span>
      <button class="remove-member" data-asset-id="${escapeHtml(member.asset_id)}" type="button" aria-label="移除 ${escapeHtml(member.symbol)}">×</button>
    </article>
  `).join("");
}

function renderAssets(filter = "") {
  const needle = filter.trim().toLowerCase();
  const assets = state.assets.filter((asset) => !needle || asset.symbol.toLowerCase().includes(needle) || asset.name.toLowerCase().includes(needle));
  $("#assetCatalogCount").textContent = assets.length;
  $("#assetCatalog").innerHTML = assets.map((asset) => `
    <article class="asset-card">
      <span class="asset-type-mark ${asset.asset_type.toLowerCase()}">${asset.asset_type === "CRYPTO" ? "₿" : asset.symbol.slice(0, 1)}</span>
      <div><p class="eyebrow">${escapeHtml(labels.asset[asset.asset_type])} · ${escapeHtml(asset.venue || asset.quote_currency)}</p><h3>${escapeHtml(asset.symbol)}</h3><p>${escapeHtml(asset.name)}</p></div>
      <span class="catalog-status ${asset.trading_status}">${asset.trading_status === "tradable" ? "可配置" : "观察"}</span>
      <code>${escapeHtml(asset.id)}</code>
    </article>
  `).join("");
}

function marketQueryErrorMarkup(message, requestId, retryAttribute, title) {
  return `<div class="provider-error market-query-error" role="alert"><span aria-hidden="true">!</span><div><strong>${escapeHtml(title)}</strong><p>${escapeHtml(message)}</p>${requestId ? `<code>Request ID: ${escapeHtml(requestId)}</code>` : ""}</div><button class="secondary-btn" ${retryAttribute} type="button">重试</button></div>`;
}

function renderMarketAssetOptions() {
  const options = MarketQueryUI.catalogOptions(state.assets);
  const select = $("#marketAssetSelect");
  select.innerHTML = options.length
    ? options.map((item) => `<option value="${escapeHtml(item.code)}">${escapeHtml(item.label)} · ${escapeHtml(item.type)}</option>`).join("")
    : '<option value="">暂无可查询资产</option>';
  if (!options.some((item) => item.code === state.marketQuery.assetCode)) state.marketQuery.assetCode = options[0]?.code || "";
  select.value = state.marketQuery.assetCode;
  select.disabled = !options.length || state.marketQuery.optionsLoading || state.marketQuery.quotes.loading || state.marketQuery.bars.loading;
  $("#selectedMarketAssetCode").textContent = state.marketQuery.assetCode || "—";
}

function renderMarketLinkage() {
  const view = state.marketQuery;
  renderMarketAssetOptions();
  const instrumentSelect = $("#marketInstrumentSelect");
  const providerSelect = $("#marketProviderSelect");
  const intervalSelect = $("#marketIntervalSelect");
  const selectedInstrument = view.options.find((item) => item.instrument_code === view.selection.instrument_code);
  const selectedProvider = selectedInstrument?.providers.find((item) => item.provider_code === view.selection.provider_code);
  instrumentSelect.innerHTML = view.options.length
    ? view.options.map((item) => `<option value="${escapeHtml(item.instrument_code)}">${escapeHtml(item.display_name)} · ${escapeHtml(item.instrument_code)}</option>`).join("")
    : '<option value="">暂无可用 Instrument</option>';
  providerSelect.innerHTML = selectedInstrument?.providers.length
    ? selectedInstrument.providers.map((item) => `<option value="${escapeHtml(item.provider_code)}">${escapeHtml(item.display_name)}${item.is_default ? " · 默认" : ""}</option>`).join("")
    : '<option value="">暂无可用 Provider</option>';
  intervalSelect.innerHTML = selectedProvider?.supported_intervals.length
    ? selectedProvider.supported_intervals.map((item) => `<option value="${escapeHtml(item)}">${escapeHtml(item)}${item === "1h" ? " · 默认" : ""}</option>`).join("")
    : '<option value="">暂无可用周期</option>';
  instrumentSelect.value = view.selection.instrument_code;
  providerSelect.value = view.selection.provider_code;
  intervalSelect.value = view.selection.interval;
  const disabled = view.optionsLoading || view.bars.loading || !selectedInstrument;
  instrumentSelect.disabled = disabled;
  providerSelect.disabled = disabled || !selectedProvider;
  intervalSelect.disabled = disabled || !view.selection.interval;
  $("#refreshMarketQueryBtn").disabled = view.optionsLoading || view.quotes.loading || view.bars.loading || !view.assetCode;
  $("#refreshMarketQueryBtn").textContent = view.optionsLoading || view.quotes.loading || view.bars.loading ? "正在刷新…" : "刷新行情";
  const status = $("#marketOptionsStatus");
  if (view.optionsLoading) status.innerHTML = '<span class="loading-pulse" aria-hidden="true"></span><span>正在加载 Instrument 与来源选项…</span>';
  else if (view.optionsError) status.innerHTML = marketQueryErrorMarkup(view.optionsError, view.optionsRequestId, "data-market-options-retry", "无法加载联动选项");
  else if (!view.options.length) status.innerHTML = '<div class="empty"><strong>该资产暂无可用行情来源</strong><span>请先配置启用、有效且具备行情能力的 ProviderInstrument。</span></div>';
  else status.innerHTML = `<span class="market-query-ready">已明确选择 <b>${escapeHtml(view.selection.instrument_code)}</b> · <b>${escapeHtml(view.selection.provider_code)}</b> · <b>${escapeHtml(view.selection.interval)}</b>${view.nextOptionsCursor ? "；当前展示前 100 个 Instrument" : ""}</span>`;
}

function renderLatestQuotes() {
  const view = state.marketQuery.quotes;
  const list = $("#latestQuoteList");
  list.setAttribute("aria-busy", String(view.loading));
  $("#quoteSourceCount").textContent = Array.isArray(view.payload?.quotes) ? view.payload.quotes.length : 0;
  if (view.loading) list.innerHTML = '<div class="subscription-loading"><span class="loading-pulse" aria-hidden="true"></span><div><strong>正在读取最新行情</strong><p>每个 ProviderInstrument 将独立返回，不会互相覆盖…</p></div></div>';
  else if (view.error) list.innerHTML = marketQueryErrorMarkup(view.error, view.requestId, "data-market-quotes-retry", "无法读取最新行情");
  else list.innerHTML = MarketQueryUI.renderQuotes(view.payload);
}

function renderBars() {
  const view = state.marketQuery.bars;
  const result = $("#barQueryResult");
  result.setAttribute("aria-busy", String(view.loading));
  $("#barCountBadge").textContent = view.items.length;
  $("#loadMoreBarsBtn").disabled = view.loading;
  $("#loadMoreBarsBtn").classList.toggle("hidden", !view.nextCursor || Boolean(view.error));
  showIngestionFilterErrors($("#barQueryErrors"), view.errors);
  $("#barQueryForm").querySelector('button[type="submit"]').disabled = view.loading || !state.marketQuery.selection.interval;
  if (view.loading && !view.items.length) result.innerHTML = '<div class="subscription-loading"><span class="loading-pulse" aria-hidden="true"></span><div><strong>正在读取 K 线</strong><p>查询当前 revision 的已落库行情…</p></div></div>';
  else if (view.error) result.innerHTML = marketQueryErrorMarkup(view.error, view.requestId, "data-market-bars-retry", "无法读取 K 线");
  else result.innerHTML = MarketQueryUI.renderBars({ bars: view.items });
}

async function loadLatestQuotes(generation = state.marketQuery.generation) {
  const view = state.marketQuery.quotes;
  const query = MarketQueryUI.buildQuotesQuery(state.marketQuery.assetCode);
  if (!query || view.loading) return;
  view.loading = true;
  view.error = "";
  view.requestId = "";
  renderLatestQuotes();
  renderMarketLinkage();
  try {
    const payload = await api(`/api/market-info/v1/quotes/latest?${query}`);
    if (generation === state.marketQuery.generation) view.payload = payload;
  } catch (error) {
    if (generation === state.marketQuery.generation) {
      view.payload = null;
      view.error = MarketQueryUI.localizedError(error);
      view.requestId = error.requestId || "";
    }
  } finally {
    if (generation === state.marketQuery.generation) {
      view.loading = false;
      renderLatestQuotes();
      renderMarketLinkage();
    }
  }
}

async function loadBars({ append = false, generation = state.marketQuery.generation } = {}) {
  const view = state.marketQuery.bars;
  if (view.loading || (append && !view.nextCursor)) return;
  const built = MarketQueryUI.buildBarsQuery(state.marketQuery.selection, view.filters, append ? view.nextCursor : "");
  view.errors = built.errors;
  renderBars();
  if (!built.query) return;
  view.loading = true;
  view.error = "";
  view.requestId = "";
  if (!append) {
    view.items = [];
    view.payload = null;
    view.nextCursor = null;
  }
  renderBars();
  renderMarketLinkage();
  try {
    const payload = await api(`/api/market-info/v1/bars?${built.query}`);
    if (generation === state.marketQuery.generation) {
      const pageItems = Array.isArray(payload?.bars) ? payload.bars : [];
      view.items = append ? [...view.items, ...pageItems] : pageItems;
      view.payload = payload;
      view.nextCursor = typeof payload?.next_cursor === "string" && payload.next_cursor ? payload.next_cursor : null;
    }
  } catch (error) {
    if (generation === state.marketQuery.generation) {
      view.error = MarketQueryUI.localizedError(error);
      view.requestId = error.requestId || "";
    }
  } finally {
    if (generation === state.marketQuery.generation) {
      view.loading = false;
      renderBars();
      renderMarketLinkage();
    }
  }
}

async function loadMarketOptions() {
  const view = state.marketQuery;
  if (!view.assetCode || view.optionsLoading) return;
  const generation = ++view.generation;
  view.optionsLoading = true;
  view.optionsLoaded = false;
  view.optionsError = "";
  view.optionsRequestId = "";
  view.options = [];
  view.selection = { instrument_code: "", provider_code: "", interval: "" };
  view.bars = { ...view.bars, items: [], payload: null, nextCursor: null, error: "", requestId: "", errors: {} };
  renderMarketLinkage();
  renderBars();
  const quotesPromise = loadLatestQuotes(generation);
  try {
    const query = new URLSearchParams({ asset_code: view.assetCode, enabled: "true", limit: "100" });
    const payload = await api(`/api/market-info/v1/instruments?${query}`);
    if (generation !== view.generation) return;
    const page = MarketQueryUI.normalizeInstrumentPage(payload);
    view.options = page.items;
    view.nextOptionsCursor = page.nextCursor;
    view.selection = MarketQueryUI.resolveSelection(view.options);
    view.optionsLoaded = true;
  } catch (error) {
    if (generation !== view.generation) return;
    view.optionsError = MarketQueryUI.localizedError(error);
    view.optionsRequestId = error.requestId || "";
  } finally {
    if (generation === view.generation) {
      view.optionsLoading = false;
      renderMarketLinkage();
    }
  }
  await quotesPromise;
  if (generation === view.generation && view.selection.interval) await loadBars({ generation });
}

function initializeMarketQuery() {
  renderMarketAssetOptions();
  renderMarketLinkage();
  renderLatestQuotes();
  renderBars();
  if (state.marketQuery.assetCode) loadMarketOptions();
}

function updateMarketSelection(changes) {
  state.marketQuery.selection = MarketQueryUI.resolveSelection(state.marketQuery.options, { ...state.marketQuery.selection, ...changes });
  state.marketQuery.bars.items = [];
  state.marketQuery.bars.nextCursor = null;
  state.marketQuery.bars.error = "";
  renderMarketLinkage();
  loadBars();
}

function renderProviderStatus() {
  const view = state.providerStatus;
  const content = $("#providerStatusContent");
  const summary = $("#providerStatusSummary");
  const refresh = $("#refreshProviderStatusBtn");
  content.setAttribute("aria-busy", String(view.loading));
  refresh.disabled = view.loading;
  refresh.textContent = view.loading ? "正在刷新…" : "刷新状态";

  if (view.loading && !view.loaded) {
    summary.innerHTML = "";
    content.innerHTML = `
      <article class="panel provider-loading" role="status">
        <span class="loading-pulse" aria-hidden="true"></span>
        <div><strong>正在读取数据源状态</strong><p>汇总已落库的任务结果与行情新鲜度…</p></div>
      </article>`;
    return;
  }
  if (view.error) {
    summary.innerHTML = "";
    content.innerHTML = `
      <article class="panel provider-error" role="alert">
        <span aria-hidden="true">!</span>
        <div><strong>暂时无法读取数据源状态</strong><p>${escapeHtml(view.error)}</p>${view.requestId ? `<code>Request ID: ${escapeHtml(view.requestId)}</code>` : ""}</div>
        <button class="secondary-btn" data-provider-retry type="button">重试</button>
      </article>`;
    $("#providerCheckedAt").textContent = "检查失败";
    return;
  }

  summary.innerHTML = ProviderStatusUI.renderSummary(view.items);
  content.innerHTML = ProviderStatusUI.renderProviders(view.items);
  const checkedAt = view.items.find((item) => item.checked_at)?.checked_at;
  $("#providerCheckedAt").textContent = checkedAt ? `检查于 ${ProviderStatusUI.formatDateTime(checkedAt)}` : "尚无检查记录";
}

async function loadProviderStatus() {
  if (state.providerStatus.loading) return;
  state.providerStatus.loading = true;
  state.providerStatus.error = "";
  state.providerStatus.requestId = "";
  renderProviderStatus();
  try {
    const payload = await api("/api/market-info/v1/providers/status");
    state.providerStatus.items = ProviderStatusUI.normalizeItems(payload);
    state.providerStatus.loaded = true;
  } catch (error) {
    state.providerStatus.error = error.message;
    state.providerStatus.requestId = error.requestId || "";
  } finally {
    state.providerStatus.loading = false;
    renderProviderStatus();
  }
}

function renderSubscriptions() {
  const view = state.subscriptions;
  const content = $("#subscriptionList");
  const loadMore = $("#loadMoreSubscriptionsBtn");
  const canManage = canManageSubscriptions();
  $("#subscriptionPermissionBadge").textContent = canManage ? "可管理" : "只读";
  $("#subscriptionCreatePanel").classList.toggle("hidden", !canManage);
  $("#marketDataSubscriptionsPanel").classList.toggle("read-only", !canManage);
  $("#subscriptionCountBadge").textContent = view.items.length;
  content.setAttribute("aria-busy", String(view.loading));
  loadMore.disabled = view.loading;
  loadMore.classList.toggle("hidden", !view.nextCursor || Boolean(view.error));

  if (view.loading && !view.loaded) {
    content.innerHTML = '<div class="subscription-loading" role="status"><span class="loading-pulse" aria-hidden="true"></span><div><strong>正在读取采集订阅</strong><p>加载 Provider、Instrument 与周期设置…</p></div></div>';
    return;
  }
  if (view.error) {
    content.innerHTML = `
      <div class="provider-error subscription-error" role="alert">
        <span aria-hidden="true">!</span><div><strong>暂时无法读取订阅</strong><p>${escapeHtml(view.error)}</p>${view.requestId ? `<code>Request ID: ${escapeHtml(view.requestId)}</code>` : ""}</div>
        <button class="secondary-btn" data-subscription-retry type="button">重试</button>
      </div>`;
    return;
  }
  content.innerHTML = SubscriptionAdminUI.renderSubscriptions(view.items, { canManage, editingID: view.editingID });
}

async function loadSubscriptions({ append = false } = {}) {
  const view = state.subscriptions;
  if (view.loading) return;
  view.loading = true;
  view.error = "";
  view.requestId = "";
  if (!append) {
    view.loaded = false;
    view.items = [];
    view.nextCursor = null;
    view.editingID = "";
  }
  renderSubscriptions();
  try {
    const query = SubscriptionAdminUI.buildListQuery(view.filters, append ? view.nextCursor : "");
    const payload = await api(`/api/market-info/v1/collection-subscriptions?${query}`);
    const page = SubscriptionAdminUI.normalizePage(payload);
    view.items = append ? [...view.items, ...page.items] : page.items;
    view.nextCursor = page.nextCursor;
    view.loaded = true;
  } catch (error) {
    view.error = SubscriptionAdminUI.localizedError(error);
    view.requestId = error.requestId || "";
  } finally {
    view.loading = false;
    renderSubscriptions();
  }
}

function subscriptionFormInput(form) {
  const values = formJson(form);
  return {
    ...values,
    enabled: form.elements.enabled.type === "checkbox" ? form.elements.enabled.checked : values.enabled === "true",
  };
}

function showSubscriptionFormErrors(container, errors) {
  container.innerHTML = SubscriptionAdminUI.renderErrors(errors);
  container.classList.toggle("hidden", !Object.keys(errors).length);
}

function resetSubscriptionCreateForm(form) {
  form.reset();
  form.elements.interval.value = "1h";
  form.elements.priority.value = "100";
  form.elements.close_delay_seconds.value = "120";
  form.elements.enabled.checked = true;
}

function showIngestionFilterErrors(container, errors) {
  if (!container) return;
  const messages = Object.values(errors);
  container.innerHTML = messages.length ? `<ul>${messages.map((message) => `<li>${escapeHtml(message)}</li>`).join("")}</ul>` : "";
  container.classList.toggle("hidden", !messages.length);
}

function ingestionErrorMarkup(message, requestId, retryAttribute, label) {
  return `
    <div class="provider-error ingestion-error" role="alert">
      <span aria-hidden="true">!</span><div><strong>${escapeHtml(label)}</strong><p>${escapeHtml(message)}</p>${requestId ? `<code>Request ID: ${escapeHtml(requestId)}</code>` : ""}</div>
      <button class="secondary-btn" ${retryAttribute} type="button">重试</button>
    </div>`;
}

function renderIngestionRefresh() {
  const isBackfill = state.ingestion.view === "backfill";
  $("#refreshIngestionBtn").classList.toggle("hidden", isBackfill);
  if (isBackfill) return;
  const view = state.ingestion[state.ingestion.view];
  const loading = view.loading || view.detailLoading;
  $("#refreshIngestionBtn").disabled = loading;
  $("#refreshIngestionBtn").textContent = loading ? "正在刷新…" : "刷新当前视图";
}

function renderRunDetail() {
  const view = state.ingestion.runs;
  const detail = $("#runDetail");
  detail.setAttribute("aria-busy", String(view.detailLoading));
  if (view.detailLoading) {
    detail.innerHTML = '<div class="subscription-loading" role="status"><span class="loading-pulse" aria-hidden="true"></span><div><strong>正在读取 Run 详情</strong><p>根据 Task 事实重新汇总状态…</p></div></div>';
  } else if (view.detailError) {
    detail.innerHTML = ingestionErrorMarkup(view.detailError, view.detailRequestId, "data-run-detail-retry", "暂时无法读取 Run 详情");
  } else {
    detail.innerHTML = IngestionMonitorUI.renderRunDetail(view.detail);
  }
}

function renderTaskDetail() {
  const view = state.ingestion.tasks;
  const detail = $("#taskDetail");
  detail.setAttribute("aria-busy", String(view.detailLoading));
  if (view.detailLoading) {
    detail.innerHTML = '<div class="subscription-loading" role="status"><span class="loading-pulse" aria-hidden="true"></span><div><strong>正在读取 Task 详情</strong><p>加载执行范围、租约和脱敏错误…</p></div></div>';
  } else if (view.detailError) {
    detail.innerHTML = ingestionErrorMarkup(view.detailError, view.detailRequestId, "data-task-detail-retry", "暂时无法读取 Task 详情");
  } else {
    detail.innerHTML = IngestionMonitorUI.renderTaskDetail(view.detail, "zh-CN", {
      canManage: canManageIngestion(),
      submitting: state.ingestion.command.submitting,
      commandError: view.commandError || "",
    });
  }
}

function renderBackfill() {
  const canManage = canManageIngestion();
  const view = state.ingestion.backfill;
  $("#backfillPermissionBadge").textContent = canManage ? "可操作" : "只读";
  $("#backfillForm").querySelector("fieldset").disabled = !canManage || view.submitting;
  $("#backfillPermissionNote").textContent = canManage
    ? "一次请求只创建一个有界时间范围的 Task；提交后将自动跳转到 Task 追踪。"
    : "当前账户缺少 ingestion.manage 权限，仅可查看 Run 与 Task。";
  showIngestionFilterErrors($("#backfillErrors"), view.errors);
  renderIngestionRefresh();
}

function renderRuns() {
  const view = state.ingestion.runs;
  const content = $("#runList");
  const loadMore = $("#loadMoreRunsBtn");
  $("#runCountBadge").textContent = view.items.length;
  content.setAttribute("aria-busy", String(view.loading));
  loadMore.disabled = view.loading;
  loadMore.classList.toggle("hidden", !view.nextCursor || Boolean(view.error));
  if (view.loading && !view.loaded) {
    content.innerHTML = '<div class="subscription-loading" role="status"><span class="loading-pulse" aria-hidden="true"></span><div><strong>正在读取 Run</strong><p>汇总 Task 状态与执行计数…</p></div></div>';
  } else if (view.error) {
    content.innerHTML = ingestionErrorMarkup(view.error, view.requestId, "data-run-retry", "暂时无法读取 Run 列表");
  } else {
    content.innerHTML = IngestionMonitorUI.renderRuns(view.items, { selectedID: view.selectedID });
  }
  renderRunDetail();
  renderIngestionRefresh();
}

function renderTasks() {
  const view = state.ingestion.tasks;
  const content = $("#taskList");
  const loadMore = $("#loadMoreTasksBtn");
  $("#taskCountBadge").textContent = view.items.length;
  content.setAttribute("aria-busy", String(view.loading));
  loadMore.disabled = view.loading;
  loadMore.classList.toggle("hidden", !view.nextCursor || Boolean(view.error));
  if (view.loading && !view.loaded) {
    content.innerHTML = '<div class="subscription-loading" role="status"><span class="loading-pulse" aria-hidden="true"></span><div><strong>正在读取 Task</strong><p>加载来源身份与执行状态…</p></div></div>';
  } else if (view.error) {
    content.innerHTML = ingestionErrorMarkup(view.error, view.requestId, "data-task-retry", "暂时无法读取 Task 列表");
  } else {
    content.innerHTML = IngestionMonitorUI.renderTasks(view.items, { selectedID: view.selectedID });
  }
  renderTaskDetail();
  renderIngestionRefresh();
}

function setIngestionView(viewName) {
  if (!new Set(["runs", "tasks", "backfill"]).has(viewName)) return;
  state.ingestion.view = viewName;
  $$('[data-ingestion-view]').forEach((button) => {
    const active = button.dataset.ingestionView === viewName;
    button.classList.toggle("active", active);
    button.setAttribute("aria-selected", String(active));
  });
  $("#ingestionRunsView").classList.toggle("active", viewName === "runs");
  $("#ingestionTasksView").classList.toggle("active", viewName === "tasks");
  $("#ingestionBackfillView").classList.toggle("active", viewName === "backfill");
  if (viewName === "backfill") {
    renderBackfill();
  } else if (state.user && !state.ingestion[viewName].loaded) {
    if (viewName === "runs") loadRuns();
    else loadTasks();
  } else {
    if (viewName === "runs") renderRuns();
    else renderTasks();
  }
}

function loadCurrentIngestionIfNeeded() {
  const view = state.ingestion.view;
  if (view === "backfill") renderBackfill();
  else if (!state.ingestion[view].loaded) {
    if (view === "runs") loadRuns();
    else loadTasks();
  } else if (view === "runs") renderRuns();
  else renderTasks();
}

async function loadRuns({ append = false } = {}) {
  const view = state.ingestion.runs;
  if (view.loading || (append && !view.nextCursor)) return;
  const built = IngestionMonitorUI.buildRunQuery(view.filters, append ? view.nextCursor : "");
  showIngestionFilterErrors($("#runFilterErrors"), built.errors);
  if (!built.query) return;
  view.loading = true;
  view.error = "";
  view.requestId = "";
  if (!append) {
    view.loaded = false;
    view.items = [];
    view.nextCursor = null;
  }
  renderRuns();
  try {
    const payload = await api(`/api/market-info/v1/ingestion-runs?${built.query}`);
    const page = IngestionMonitorUI.normalizePage(payload);
    view.items = append ? [...view.items, ...page.items] : page.items;
    view.nextCursor = page.nextCursor;
    view.loaded = true;
  } catch (error) {
    view.error = IngestionMonitorUI.localizedError(error);
    view.requestId = error.requestId || "";
  } finally {
    view.loading = false;
    renderRuns();
  }
}

async function loadTasks({ append = false } = {}) {
  const view = state.ingestion.tasks;
  if (view.loading || (append && !view.nextCursor)) return;
  const built = IngestionMonitorUI.buildTaskQuery(view.filters, append ? view.nextCursor : "");
  showIngestionFilterErrors($("#taskFilterErrors"), built.errors);
  if (!built.query) return;
  view.loading = true;
  view.error = "";
  view.requestId = "";
  if (!append) {
    view.loaded = false;
    view.items = [];
    view.nextCursor = null;
  }
  renderTasks();
  try {
    const payload = await api(`/api/market-info/v1/ingestion-tasks?${built.query}`);
    const page = IngestionMonitorUI.normalizePage(payload);
    view.items = append ? [...view.items, ...page.items] : page.items;
    view.nextCursor = page.nextCursor;
    view.loaded = true;
  } catch (error) {
    view.error = IngestionMonitorUI.localizedError(error);
    view.requestId = error.requestId || "";
  } finally {
    view.loading = false;
    renderTasks();
  }
}

async function loadRunDetail(id) {
  const view = state.ingestion.runs;
  view.selectedID = id;
  view.detail = null;
  view.detailLoading = true;
  view.detailError = "";
  view.detailRequestId = "";
  renderRuns();
  try {
    const payload = await api(`/api/market-info/v1/ingestion-runs/${encodeURIComponent(id)}`);
    if (view.selectedID === id) {
      view.detail = IngestionMonitorUI.normalizeDetail(payload, "run");
      if (!view.detail) throw new Error("市场资讯服务返回了无效 Run 详情");
    }
  } catch (error) {
    if (view.selectedID === id) {
      view.detailError = IngestionMonitorUI.localizedError(error);
      view.detailRequestId = error.requestId || "";
    }
  } finally {
    if (view.selectedID === id) {
      view.detailLoading = false;
      renderRuns();
    }
  }
}

async function loadTaskDetail(id) {
  const view = state.ingestion.tasks;
  view.selectedID = id;
  view.detail = null;
  view.detailLoading = true;
  view.detailError = "";
  view.detailRequestId = "";
  renderTasks();
  try {
    const payload = await api(`/api/market-info/v1/ingestion-tasks/${encodeURIComponent(id)}`);
    if (view.selectedID === id) {
      view.detail = IngestionMonitorUI.normalizeDetail(payload, "task");
      if (!view.detail) throw new Error("市场资讯服务返回了无效 Task 详情");
    }
  } catch (error) {
    if (view.selectedID === id) {
      view.detailError = IngestionMonitorUI.localizedError(error);
      view.detailRequestId = error.requestId || "";
    }
  } finally {
    if (view.selectedID === id) {
      view.detailLoading = false;
      renderTasks();
    }
  }
}

async function refreshCurrentIngestion() {
  const viewName = state.ingestion.view;
  if (viewName === "backfill") return;
  const selectedID = state.ingestion[viewName].selectedID;
  if (viewName === "runs") {
    await loadRuns();
    if (selectedID) await loadRunDetail(selectedID);
  } else {
    await loadTasks();
    if (selectedID) await loadTaskDetail(selectedID);
  }
}

function closeIngestionConfirmation(force = false) {
  if (!force && state.ingestion.command.submitting) return;
  state.ingestion.command = { kind: "", taskID: "", payload: null, summary: "", submitting: false, error: "", requestId: "" };
  const dialog = $("#ingestionConfirmDialog");
  if (dialog) dialog.classList.add("hidden");
}

function renderIngestionConfirmation() {
  const command = state.ingestion.command;
  const copy = {
    backfill: ["确认发起历史回填", "确认后将创建一个新的 Run 与 Task。"],
    retry: ["确认重试失败 Task", "确认后将创建新的 Run 与 Task，原失败记录不会被修改。"],
    cancel: ["确认取消 Task", "取消后服务将阻止该 Task 继续写入行情数据。"],
  }[command.kind] || ["确认采集操作", "请核对本次操作。"];
  $("#ingestionConfirmTitle").textContent = copy[0];
  $("#ingestionConfirmMessage").textContent = `${copy[1]} ${command.summary}`;
  $("#ingestionConfirmError").textContent = command.error;
  $("#ingestionConfirmError").classList.toggle("hidden", !command.error);
  $("#confirmIngestionCommandBtn").disabled = command.submitting;
  $("#dismissIngestionCommandBtn").disabled = command.submitting;
  $("#confirmIngestionCommandBtn").textContent = command.submitting ? "正在提交…" : "确认执行";
}

function openIngestionConfirmation({ kind, taskID = "", payload, summary }) {
  if (state.ingestion.command.submitting) return;
  state.ingestion.command = { kind, taskID, payload, summary, submitting: false, error: "", requestId: "" };
  renderIngestionConfirmation();
  $("#ingestionConfirmDialog").classList.remove("hidden");
  $("#confirmIngestionCommandBtn").focus();
}

async function trackCreatedTask(runID, taskID) {
  const view = state.ingestion.tasks;
  view.filters = { run_id: runID, status: "", provider: "", instrument_code: "", interval: "", created_from: "", created_to: "" };
  view.loaded = false;
  view.items = [];
  view.nextCursor = null;
  view.selectedID = taskID;
  view.detail = null;
  const form = $("#taskFilterForm");
  form.reset();
  form.elements.run_id.value = runID;
  setIngestionView("tasks");
  await loadTaskDetail(taskID);
}

async function executeIngestionCommand() {
  const command = state.ingestion.command;
  if (!command.kind || command.submitting) return;
  command.submitting = true;
  command.error = "";
  state.ingestion.backfill.submitting = command.kind === "backfill";
  renderIngestionConfirmation();
  renderTaskDetail();
  if (state.ingestion.view === "backfill") renderBackfill();
  try {
    const path = command.kind === "backfill"
      ? "/api/market-info/v1/ingestion-runs/backfill"
      : `/api/market-info/v1/ingestion-tasks/${encodeURIComponent(command.taskID)}/${command.kind}`;
    const result = await api(path, { method: "POST", body: JSON.stringify(command.payload) });
    const kind = command.kind;
    command.submitting = false;
    state.ingestion.backfill.submitting = false;
    closeIngestionConfirmation(true);
    if (kind === "cancel") {
      await loadTasks();
      await loadTaskDetail(result.task_id);
      showToast("Task 已取消");
    } else {
      if (kind === "backfill") $("#backfillForm").reset();
      await trackCreatedTask(result.run_id, result.task_id);
      showToast(kind === "backfill" ? "历史回填 Task 已创建" : "重试 Task 已创建");
    }
  } catch (error) {
    command.submitting = false;
    state.ingestion.backfill.submitting = false;
    command.error = IngestionMonitorUI.localizedError(error);
    command.requestId = error.requestId || "";
    renderIngestionConfirmation();
    renderTaskDetail();
    if (state.ingestion.view === "backfill") renderBackfill();
  }
}

function showTasksForRun(runID) {
  const view = state.ingestion.tasks;
  view.filters = { run_id: runID, status: "", provider: "", instrument_code: "", interval: "", created_from: "", created_to: "" };
  view.loaded = false;
  view.items = [];
  view.nextCursor = null;
  view.selectedID = "";
  view.detail = null;
  const form = $("#taskFilterForm");
  form.reset();
  form.elements.run_id.value = runID;
  setIngestionView("tasks");
}

async function loadWorkspace() {
  const [portfolioPayload, assetPayload] = await Promise.all([api("/api/portfolios"), api("/api/assets")]);
  state.portfolios = portfolioPayload.portfolios;
  state.assets = assetPayload.assets;
  if (state.selectedPortfolioId && !state.portfolios.some((item) => item.id === state.selectedPortfolioId)) {
    state.selectedPortfolioId = null;
    state.members = [];
  }
  renderOverview();
  renderPortfolios();
  renderAssets($("#assetSearch").value);
  renderPortfolioDetail();
  renderMarketAssetOptions();
  if (state.page === "assets" && !state.marketQuery.optionsLoaded && !state.marketQuery.optionsLoading) initializeMarketQuery();
}

async function selectPortfolio(portfolioId) {
  state.selectedPortfolioId = Number(portfolioId);
  const payload = await api(`/api/portfolios/${state.selectedPortfolioId}/members`);
  state.members = payload.members;
  renderPortfolios();
  renderPortfolioDetail();
}

async function loadMe() {
  if (!state.token) {
    renderShell();
    return;
  }
  try {
    const payload = await api("/api/users/me");
    state.user = payload.user;
    renderShell();
    await loadWorkspace();
  } catch (error) {
    clearWorkspace();
    setSession("", null);
    showToast(error.message);
  }
}

function formJson(form) {
  return Object.fromEntries(new FormData(form).entries());
}

function escapeHtml(value) {
  return String(value ?? "").replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll('"', "&quot;").replaceAll("'", "&#039;");
}

function formatDate(value) {
  if (!value) return "-";
  return new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "2-digit", day: "2-digit" }).format(new Date(value));
}

function formatPct(value) {
  const number = Number(value);
  return Number.isFinite(number) ? `${Math.round(number * 100)}%` : "-";
}

async function authenticate(form, endpoint, successMessage) {
  const payload = await api(endpoint, { method: "POST", body: JSON.stringify(formJson(form)) });
  form.reset();
  setSession(payload.token, payload.user);
  await loadWorkspace();
  setPage("overview");
  showToast(successMessage);
}

$("#loginForm").addEventListener("submit", async (event) => {
  event.preventDefault();
  try { await authenticate(event.currentTarget, "/api/auth/login", "欢迎回来，研究工作区已就绪"); }
  catch (error) { showToast(error.message); }
});

$("#registerForm").addEventListener("submit", async (event) => {
  event.preventDefault();
  try { await authenticate(event.currentTarget, "/api/auth/register", "研究账户已创建"); }
  catch (error) { showToast(error.message); }
});

$("#portfolioForm").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = event.currentTarget;
  const payload = formJson(form);
  payload.allowed_asset_types = [...form.querySelectorAll('input[name="asset_type"]:checked')].map((input) => input.value);
  try {
    const result = await api("/api/portfolios", { method: "POST", body: JSON.stringify(payload) });
    form.reset();
    form.querySelectorAll('input[name="asset_type"]').forEach((input) => { input.checked = true; });
    await loadWorkspace();
    await selectPortfolio(result.portfolio.id);
    showToast("投资组合已创建");
  } catch (error) { showToast(error.message); }
});

$("#portfolioList").addEventListener("click", async (event) => {
  const button = event.target.closest("[data-action]");
  if (!button) return;
  try {
    if (button.dataset.action === "select") {
      await selectPortfolio(button.dataset.id);
      return;
    }
    if (!window.confirm("确认归档这个投资组合？历史记录会保留。")) return;
    await api(`/api/portfolios/${button.dataset.id}`, { method: "DELETE" });
    await loadWorkspace();
    showToast("投资组合已归档");
  } catch (error) { showToast(error.message); }
});

$("#memberForm").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!state.selectedPortfolioId) return;
  const form = event.currentTarget;
  try {
    await api(`/api/portfolios/${state.selectedPortfolioId}/members`, { method: "POST", body: JSON.stringify(formJson(form)) });
    form.reset();
    form.querySelector('[name="target_weight_max"]').value = "0.15";
    await selectPortfolio(state.selectedPortfolioId);
    await loadWorkspace();
    showToast("资产已加入投资组合");
  } catch (error) { showToast(error.message); }
});

$("#memberList").addEventListener("click", async (event) => {
  const button = event.target.closest(".remove-member");
  if (!button || !state.selectedPortfolioId) return;
  if (!window.confirm("确认从组合中移除该资产？")) return;
  try {
    await api(`/api/portfolios/${state.selectedPortfolioId}/members/${encodeURIComponent(button.dataset.assetId)}`, { method: "DELETE" });
    await selectPortfolio(state.selectedPortfolioId);
    await loadWorkspace();
    showToast("资产已从组合移除");
  } catch (error) { showToast(error.message); }
});

$("#overviewPortfolioList").addEventListener("click", async (event) => {
  const row = event.target.closest("[data-overview-portfolio]");
  if (!row) return;
  setPage("portfolios");
  try { await selectPortfolio(row.dataset.overviewPortfolio); }
  catch (error) { showToast(error.message); }
});

$("#assetSearch").addEventListener("input", (event) => renderAssets(event.currentTarget.value));
$("#marketAssetSelect").addEventListener("change", (event) => {
  state.marketQuery.assetCode = event.currentTarget.value;
  state.marketQuery.quotes = { payload: null, loading: false, error: "", requestId: "" };
  loadMarketOptions();
});
$("#marketInstrumentSelect").addEventListener("change", (event) => updateMarketSelection({ instrument_code: event.currentTarget.value, provider_code: "", interval: "" }));
$("#marketProviderSelect").addEventListener("change", (event) => updateMarketSelection({ provider_code: event.currentTarget.value, interval: "" }));
$("#marketIntervalSelect").addEventListener("change", (event) => updateMarketSelection({ interval: event.currentTarget.value }));
$("#refreshMarketQueryBtn").addEventListener("click", loadMarketOptions);
$("#barQueryForm").addEventListener("submit", (event) => {
  event.preventDefault();
  state.marketQuery.bars.filters = formJson(event.currentTarget);
  loadBars();
});
$("#loadMoreBarsBtn").addEventListener("click", () => loadBars({ append: true }));
$("#marketOptionsStatus").addEventListener("click", (event) => {
  if (event.target.closest("[data-market-options-retry]")) loadMarketOptions();
});
$("#latestQuoteList").addEventListener("click", (event) => {
  if (event.target.closest("[data-market-quotes-retry]")) loadLatestQuotes();
});
$("#barQueryResult").addEventListener("click", (event) => {
  if (event.target.closest("[data-market-bars-retry]")) loadBars();
});
$("#refreshProviderStatusBtn").addEventListener("click", loadProviderStatus);
$("#providerStatusContent").addEventListener("click", (event) => {
  if (event.target.closest("[data-provider-retry]")) loadProviderStatus();
});
$$('[data-market-data-view]').forEach((button) => button.addEventListener("click", () => setMarketDataView(button.dataset.marketDataView)));
$("#subscriptionFilterForm").addEventListener("submit", (event) => {
  event.preventDefault();
  state.subscriptions.filters = formJson(event.currentTarget);
  loadSubscriptions();
});
$("#resetSubscriptionFiltersBtn").addEventListener("click", () => {
  $("#subscriptionFilterForm").reset();
  state.subscriptions.filters = { provider: "", instrument_code: "", interval: "", enabled: "" };
  loadSubscriptions();
});
$("#loadMoreSubscriptionsBtn").addEventListener("click", () => loadSubscriptions({ append: true }));
$("#subscriptionCreateForm").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!canManageSubscriptions()) return;
  const form = event.currentTarget;
  const errors = $("#subscriptionCreateErrors");
  const result = SubscriptionAdminUI.buildCreatePayload(subscriptionFormInput(form));
  showSubscriptionFormErrors(errors, result.errors);
  if (!result.payload) return;
  const submit = form.querySelector('button[type="submit"]');
  submit.disabled = true;
  try {
    await api("/api/market-info/v1/collection-subscriptions", { method: "POST", body: JSON.stringify(result.payload) });
    resetSubscriptionCreateForm(form);
    showSubscriptionFormErrors(errors, {});
    state.providerStatus.loaded = false;
    await loadSubscriptions();
    showToast("采集订阅已创建");
  } catch (error) {
    const message = SubscriptionAdminUI.localizedError(error);
    showSubscriptionFormErrors(errors, { request: message });
    showToast(message);
  } finally {
    submit.disabled = false;
  }
});
$("#subscriptionList").addEventListener("click", (event) => {
  if (event.target.closest("[data-subscription-retry]")) {
    loadSubscriptions();
    return;
  }
  const open = event.target.closest("[data-subscription-edit-open]");
  if (open) {
    state.subscriptions.editingID = open.dataset.subscriptionEditOpen;
    renderSubscriptions();
    return;
  }
  if (event.target.closest("[data-subscription-cancel]")) {
    state.subscriptions.editingID = "";
    renderSubscriptions();
  }
});
$("#subscriptionList").addEventListener("submit", async (event) => {
  const form = event.target.closest("[data-subscription-edit]");
  if (!form) return;
  event.preventDefault();
  if (!canManageSubscriptions()) return;
  const errors = form.querySelector(".subscription-edit-errors");
  const result = SubscriptionAdminUI.buildUpdatePayload(subscriptionFormInput(form));
  showSubscriptionFormErrors(errors, result.errors);
  if (!result.payload) return;
  const submit = form.querySelector('button[type="submit"]');
  submit.disabled = true;
  try {
    const payload = await api(`/api/market-info/v1/collection-subscriptions/${encodeURIComponent(form.dataset.id)}`, { method: "PATCH", body: JSON.stringify(result.payload) });
    state.subscriptions.items = state.subscriptions.items.map((item) => item.subscription_id === form.dataset.id ? payload.subscription : item);
    state.subscriptions.editingID = "";
    state.providerStatus.loaded = false;
    renderSubscriptions();
    showToast("订阅设置已更新");
  } catch (error) {
    const message = SubscriptionAdminUI.localizedError(error);
    showSubscriptionFormErrors(errors, { request: message });
    showToast(message);
    submit.disabled = false;
  }
});
$$('[data-ingestion-view]').forEach((button) => button.addEventListener("click", () => setIngestionView(button.dataset.ingestionView)));
$("#refreshIngestionBtn").addEventListener("click", refreshCurrentIngestion);
$("#runFilterForm").addEventListener("submit", (event) => {
  event.preventDefault();
  const view = state.ingestion.runs;
  view.filters = formJson(event.currentTarget);
  view.selectedID = "";
  view.detail = null;
  view.detailError = "";
  loadRuns();
});
$("#resetRunFiltersBtn").addEventListener("click", () => {
  $("#runFilterForm").reset();
  const view = state.ingestion.runs;
  view.filters = { run_type: "", trigger_type: "", status: "", requested_by: "", created_from: "", created_to: "" };
  view.selectedID = "";
  view.detail = null;
  view.detailError = "";
  showIngestionFilterErrors($("#runFilterErrors"), {});
  loadRuns();
});
$("#taskFilterForm").addEventListener("submit", (event) => {
  event.preventDefault();
  const view = state.ingestion.tasks;
  view.filters = formJson(event.currentTarget);
  view.selectedID = "";
  view.detail = null;
  view.detailError = "";
  loadTasks();
});
$("#resetTaskFiltersBtn").addEventListener("click", () => {
  $("#taskFilterForm").reset();
  const view = state.ingestion.tasks;
  view.filters = { run_id: "", status: "", provider: "", instrument_code: "", interval: "", created_from: "", created_to: "" };
  view.selectedID = "";
  view.detail = null;
  view.detailError = "";
  showIngestionFilterErrors($("#taskFilterErrors"), {});
  loadTasks();
});
$("#backfillForm").addEventListener("submit", (event) => {
  event.preventDefault();
  if (!canManageIngestion() || state.ingestion.backfill.submitting) return;
  const result = IngestionMonitorUI.buildBackfillPayload(formJson(event.currentTarget));
  state.ingestion.backfill.errors = result.errors;
  renderBackfill();
  if (!result.payload) return;
  openIngestionConfirmation({
    kind: "backfill",
    payload: result.payload,
    summary: `${result.payload.provider} · ${result.payload.instrument_code} · ${result.payload.interval} · ${IngestionMonitorUI.formatRange(result.payload.start_time, result.payload.end_time)}`,
  });
});
$("#loadMoreRunsBtn").addEventListener("click", () => loadRuns({ append: true }));
$("#loadMoreTasksBtn").addEventListener("click", () => loadTasks({ append: true }));
$("#runList").addEventListener("click", (event) => {
  if (event.target.closest("[data-run-retry]")) {
    loadRuns();
    return;
  }
  const button = event.target.closest("[data-run-detail]");
  if (button) loadRunDetail(button.dataset.runDetail);
});
$("#taskList").addEventListener("click", (event) => {
  if (event.target.closest("[data-task-retry]")) {
    loadTasks();
    return;
  }
  const button = event.target.closest("[data-task-detail]");
  if (button) loadTaskDetail(button.dataset.taskDetail);
});
$("#runDetail").addEventListener("click", (event) => {
  if (event.target.closest("[data-run-detail-retry]") && state.ingestion.runs.selectedID) {
    loadRunDetail(state.ingestion.runs.selectedID);
    return;
  }
  const button = event.target.closest("[data-run-tasks]");
  if (button) showTasksForRun(button.dataset.runTasks);
});
$("#taskDetail").addEventListener("click", (event) => {
  if (event.target.closest("[data-task-detail-retry]") && state.ingestion.tasks.selectedID) {
    loadTaskDetail(state.ingestion.tasks.selectedID);
  }
});
$("#taskDetail").addEventListener("submit", (event) => {
  const form = event.target.closest("[data-task-command-form]");
  if (!form) return;
  event.preventDefault();
  if (!canManageIngestion() || state.ingestion.command.submitting) return;
  const kind = event.submitter?.dataset.taskCommand;
  const task = state.ingestion.tasks.detail;
  if (!task || !IngestionMonitorUI.availableTaskActions(task.status, true).includes(kind)) return;
  const result = IngestionMonitorUI.buildTaskCommandPayload(formJson(form));
  state.ingestion.tasks.commandError = result.errors.reason || "";
  renderTaskDetail();
  if (!result.payload) return;
  openIngestionConfirmation({
    kind,
    taskID: task.task_id,
    payload: result.payload,
    summary: `Task ${IngestionMonitorUI.shortID(task.task_id)} · 原因：${result.payload.reason}`,
  });
});
$("#dismissIngestionCommandBtn").addEventListener("click", () => closeIngestionConfirmation());
$("#confirmIngestionCommandBtn").addEventListener("click", executeIngestionCommand);
$("#themeToggleBtn").addEventListener("click", toggleTheme);
$("#loginTabBtn").addEventListener("click", () => setAuthTab("login"));
$("#registerTabBtn").addEventListener("click", () => setAuthTab("register"));
$("#userMenuButton").addEventListener("click", (event) => { event.stopPropagation(); setUserMenu(!state.userMenuOpen); });
$("#userMenu").addEventListener("click", (event) => event.stopPropagation());
$("#openAccountMenuBtn").addEventListener("click", (event) => { event.stopPropagation(); setUserMenu(true); });

$$(".nav-item").forEach((button) => button.addEventListener("click", () => setPage(button.dataset.page)));
$$('[data-go-page]').forEach((button) => button.addEventListener("click", () => setPage(button.dataset.goPage)));

$("#logoutBtn").addEventListener("click", async () => {
  try { await api("/api/auth/logout", { method: "POST", body: "{}" }); } catch { /* stale sessions still clear locally */ }
  clearWorkspace();
  setSession("", null);
  showToast("已退出研究平台");
});

$("#passwordForm").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = event.currentTarget;
  try {
    await api("/api/users/password", { method: "POST", body: JSON.stringify(formJson(form)) });
    form.reset();
    clearWorkspace();
    setSession("", null);
    showToast("密码已修改，请重新登录");
  } catch (error) { showToast(error.message); }
});

$("#deleteAccountBtn").addEventListener("click", async () => {
  if (!window.confirm("确认注销账户？组合将停止使用，当前账户也会被停用。")) return;
  try {
    await api("/api/users/me", { method: "DELETE" });
    clearWorkspace();
    setSession("", null);
    showToast("账户已注销");
  } catch (error) { showToast(error.message); }
});

document.addEventListener("click", () => { if (state.userMenuOpen) setUserMenu(false); });
document.addEventListener("keydown", (event) => { if (event.key === "Escape" && state.userMenuOpen) setUserMenu(false); });

setAuthTab(state.authTab);
applyTheme();
setPage(state.page);
loadMe();
