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
  if (!response.ok) throw new Error(payload.error || "请求失败");
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
