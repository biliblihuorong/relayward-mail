/* Relayward admin page: talks to the management API only (all 18 routes).
   Login exchanges the admin token for an HttpOnly session cookie via
   POST /api/login (plus a Turnstile verdict when the captcha is enabled),
   so the raw token never lives in this page's JavaScript or storage.
   The UI is bilingual (zh/en) with a built-in per-view guide. */
"use strict";

/* ---- i18n ---------------------------------------------------------------- */

var I18N = {
zh: {
  "title": "Relayward 管理台",
  "login.token.ph": "管理 token（rw_admin_… / rw_…）",
  "login.login": "登录", "login.logout": "退出",
  "login.hint": "第一次使用？初始管理 token 在服务首次启动时打印到日志一次，并同时存放在数据目录的 initial_admin_token 文件里（权限 0600）。丢失可在服务器上运行 relayward admin reset 重新生成。",
  "login.needtoken": "请先输入 token", "login.needcaptcha": "请先完成人机验证",
  "login.ok": "已登录", "login.bye": "已退出", "login.who": "{name}（{role}）",
  "err.unauthorized": "认证失败：token 缺失或无效", "err.forbidden": "权限不足",
  "err.ip_banned": "失败次数过多，IP 已被临时封禁",
  "err.captcha_failed": "人机验证未通过，请重试", "err.captcha_unavailable": "验证服务暂不可用，请稍后再试",
  "err.request": "请求失败：",
  "nav.stats": "统计", "nav.messages": "发送日志", "nav.apps": "程序", "nav.unsubs": "退订",
  "nav.tokens": "Token", "nav.audit": "审计", "nav.help": "帮助", "nav.health": "健康检查",
  "health.toast": "健康状态：",
  "guide.gotit": "知道了，不再自动显示",
  "guide.stats": "<b>快速上手（三步）</b><ol>" +
    "<li>到「程序」页<b>创建发信程序</b>，记下只显示一次的 SMTP 密码。</li>" +
    "<li>在你的应用 SMTP 设置里：主机填<b>本网关地址</b>、端口 <b>587</b>，用户名=程序名，密码=刚生成的密码，发件人必须是该程序的<b>允许发件地址</b>。</li>" +
    "<li>到「发送日志」看每封信的投递状态，到「统计」看汇总。提供商的 key 只存在网关配置里，程序永远接触不到。</li></ol>",
  "guide.messages": "<b>状态含义</b>：sent=已投递给上游；failed=上游拒绝或网络失败；rate_limited=超过该程序每小时限额；suppressed=收件人已退订，跳过发送。<br>支持按程序、收件人、状态、时间段过滤，游标分页。",
  "guide.apps": "<b>每个程序一套独立凭据</b>：名称即 SMTP 用户名；允许发件地址=该程序可用的 MAIL FROM（逗号分隔，发件人不在名单内会被 550 拒收）；密码只显示一次，可随时重置（旧密码立即失效）；限流为每小时上限；删除为软删除，日志保留。",
  "guide.unsubs": "<b>收件人退订记录</b>。来源：link=点击邮件里的退订链接；one_click=邮件客户端一键退订（RFC 8058）；api=管理端手动添加。删除记录即恢复订阅。",
  "guide.tokens": "<b>管理 API 的访问令牌</b>（仅 admin 角色可见本页）。创建后明文只显示一次；吊销立即生效，并会连带销毁用它登录的管理页会话；可设置过期时间。",
  "guide.audit": "<b>所有管理操作留痕</b>：哪个 token、从哪个 IP、对什么对象做了什么——包括每次管理页登录（session.create）。",
  "stats.title": "各程序发送量",
  "stats.col.app": "程序", "stats.col.total": "总数", "stats.col.sent": "成功",
  "stats.col.failed": "失败", "stats.col.limited": "限流", "stats.col.blocked": "拦截",
  "status.sent": "成功", "status.failed": "失败", "status.limited": "限流", "status.blocked": "拦截",
  "messages.title": "发送日志", "messages.ph.app": "程序名", "messages.ph.to": "收件人",
  "messages.status.all": "全部状态", "messages.since": "起", "messages.until": "止", "messages.search": "查询",
  "col.time": "时间", "col.app": "程序", "col.from": "发件人", "col.to": "收件人", "col.subject": "主题",
  "col.status": "状态", "col.size": "大小", "col.msgid": "Message-ID", "col.display": "显示名",
  "col.allowed": "允许发件地址", "col.email": "邮箱", "col.source": "来源", "col.ops": "操作",
  "common.next": "下一页", "common.cancel": "取消", "common.enabled": "启用", "common.disabled": "停用",
  "common.noData": "暂无数据", "common.on": "开", "common.off": "关",
  "apps.title": "程序",
  "apps.ph.name": "名称（SMTP 用户名）", "apps.ph.display": "显示名（退订页给收件人看）",
  "apps.ph.allowed": "允许发件地址，逗号分隔", "apps.ph.rate": "限流/小时",
  "apps.label.unsub": "退订", "apps.label.injection": "正文注入", "apps.create": "创建程序",
  "apps.created.pw": "SMTP 密码（只显示一次）：", "apps.created.save": " —— 请立即保存",
  "apps.rotated.pw": "新的 SMTP 密码（只显示一次）：",
  "apps.col.unsub": "退订", "apps.col.injection": "正文注入", "apps.col.rate": "限流/h",
  "apps.disable": "停用", "apps.enable": "启用", "apps.rotate": "重置密码", "apps.edit": "编辑",
  "apps.delete": "删除", "apps.delete.confirm": "删除程序 {name}？（发送日志保留）",
  "apps.unsub.off": "关退订", "apps.unsub.on": "开退订",
  "apps.inj.off": "关注入", "apps.inj.on": "开注入", "apps.save": "保存修改",
  "unsubs.title": "退订", "unsubs.ph.app": "程序名", "unsubs.ph.email": "邮箱",
  "unsubs.create": "手动添加退订", "unsubs.resub": "恢复订阅",
  "source.link": "邮件链接", "source.one_click": "一键退订", "source.api": "手动添加",
  "tokens.title": "管理 Token",
  "tokens.ph.name": "名称（如 gitea-ops）", "tokens.role.admin": "admin（全部权限）",
  "tokens.role.operator": "operator（程序与退订）", "tokens.role.viewer": "viewer（只读）",
  "tokens.expires": "过期时间（可空）", "tokens.create": "创建 Token",
  "tokens.created": "新 Token 明文（只显示一次，请立即保存）：",
  "tokens.col.role": "角色", "tokens.col.expires": "过期", "tokens.col.lastused": "最近使用",
  "tokens.col.created": "创建时间", "tokens.revoke": "吊销", "tokens.never": "永不过期",
  "audit.title": "审计日志",
  "audit.col.token": "Token", "audit.col.action": "动作", "audit.col.target": "对象", "audit.col.detail": "详情"
},
en: {
  "title": "Relayward Admin",
  "login.token.ph": "Admin token (rw_admin_… / rw_…)",
  "login.login": "Sign in", "login.logout": "Sign out",
  "login.hint": "First time here? The initial admin token is printed to the startup log once and saved to initial_admin_token inside the data directory (mode 0600). If lost, run relayward admin reset on the server to generate a new one.",
  "login.needtoken": "Enter the token first", "login.needcaptcha": "Complete the captcha first",
  "login.ok": "Signed in", "login.bye": "Signed out", "login.who": "{name} ({role})",
  "err.unauthorized": "Authentication failed: token missing or invalid", "err.forbidden": "Insufficient permissions",
  "err.ip_banned": "Too many failures, your IP is temporarily banned",
  "err.captcha_failed": "Captcha failed, please retry", "err.captcha_unavailable": "Captcha service unavailable, try again later",
  "err.request": "Request failed: ",
  "nav.stats": "Stats", "nav.messages": "Send log", "nav.apps": "Apps", "nav.unsubs": "Unsubscribes",
  "nav.tokens": "Tokens", "nav.audit": "Audit", "nav.help": "Help", "nav.health": "Health",
  "health.toast": "Health status: ",
  "guide.gotit": "Got it, don't show again",
  "guide.stats": "<b>Quick start (3 steps)</b><ol>" +
    "<li>Create a <b>sending app</b> on the Apps tab and save the one-time SMTP password.</li>" +
    "<li>In your application's SMTP settings: host = <b>this gateway</b>, port <b>587</b>, username = app name, password = the generated one; the From address must be in the app's <b>allowed senders</b>.</li>" +
    "<li>Watch every message in the Send log and totals in Stats. The provider key lives only in the gateway config — apps never see it.</li></ol>",
  "guide.messages": "<b>Status meanings</b>: sent = delivered upstream; failed = rejected by upstream or network failure; rate_limited = the app exceeded its hourly cap; suppressed = recipient unsubscribed, skipped.<br>Filter by app, recipient, status or time range; cursor pagination.",
  "guide.apps": "<b>One credential set per app</b>: the name is the SMTP username; allowed senders = the MAIL FROM addresses this app may use (comma separated, anything else gets a 550); the password is shown once and can be rotated anytime (old one dies immediately); the rate cap is per hour; deleting is soft — logs are kept.",
  "guide.unsubs": "<b>Recipient opt-outs</b>. Sources: link = clicked the link in the mail footer; one_click = one-click unsubscribe by the mail client (RFC 8058); api = added manually. Deleting a row re-subscribes the recipient.",
  "guide.tokens": "<b>Access tokens for the management API</b> (admin role only). The plaintext is shown once; revoking takes effect immediately and also kills admin-page sessions created with it; expiry is optional.",
  "guide.audit": "<b>Every management action is recorded</b>: which token, from which IP, on which target — including each admin-page login (session.create).",
  "stats.title": "Per-app send totals",
  "stats.col.app": "App", "stats.col.total": "Total", "stats.col.sent": "Sent",
  "stats.col.failed": "Failed", "stats.col.limited": "Rate-limited", "stats.col.blocked": "Suppressed",
  "status.sent": "Sent", "status.failed": "Failed", "status.limited": "Rate-limited", "status.blocked": "Suppressed",
  "messages.title": "Send log", "messages.ph.app": "App name", "messages.ph.to": "Recipient",
  "messages.status.all": "All statuses", "messages.since": "From", "messages.until": "To", "messages.search": "Search",
  "col.time": "Time", "col.app": "App", "col.from": "From", "col.to": "To", "col.subject": "Subject",
  "col.status": "Status", "col.size": "Size", "col.msgid": "Message-ID", "col.display": "Display name",
  "col.allowed": "Allowed senders", "col.email": "Email", "col.source": "Source", "col.ops": "Actions",
  "common.next": "Next page", "common.cancel": "Cancel", "common.enabled": "Enabled", "common.disabled": "Disabled",
  "common.noData": "No data", "common.on": "On", "common.off": "Off",
  "apps.title": "Apps",
  "apps.ph.name": "Name (SMTP username)", "apps.ph.display": "Display name (shown on the unsubscribe page)",
  "apps.ph.allowed": "Allowed senders, comma separated", "apps.ph.rate": "Rate/hour",
  "apps.label.unsub": "Unsubscribe", "apps.label.injection": "Body injection", "apps.create": "Create app",
  "apps.created.pw": "SMTP password (shown once): ", "apps.created.save": " — save it now",
  "apps.rotated.pw": "New SMTP password (shown once): ",
  "apps.col.unsub": "Unsub", "apps.col.injection": "Injection", "apps.col.rate": "Rate/h",
  "apps.disable": "Disable", "apps.enable": "Enable", "apps.rotate": "Rotate password", "apps.edit": "Edit",
  "apps.delete": "Delete", "apps.delete.confirm": "Delete app {name}? (send log is kept)",
  "apps.unsub.off": "Disable unsub", "apps.unsub.on": "Enable unsub",
  "apps.inj.off": "Disable injection", "apps.inj.on": "Enable injection", "apps.save": "Save changes",
  "unsubs.title": "Unsubscribes", "unsubs.ph.app": "App name", "unsubs.ph.email": "Email",
  "unsubs.create": "Add unsubscribe", "unsubs.resub": "Re-subscribe",
  "source.link": "Mail link", "source.one_click": "One-click", "source.api": "Manual",
  "tokens.title": "Admin tokens",
  "tokens.ph.name": "Name (e.g. gitea-ops)", "tokens.role.admin": "admin (full access)",
  "tokens.role.operator": "operator (apps & unsubscribes)", "tokens.role.viewer": "viewer (read-only)",
  "tokens.expires": "Expires (optional)", "tokens.create": "Create token",
  "tokens.created": "New token plaintext (shown once, save it now): ",
  "tokens.col.role": "Role", "tokens.col.expires": "Expires", "tokens.col.lastused": "Last used",
  "tokens.col.created": "Created", "tokens.revoke": "Revoke", "tokens.never": "Never",
  "audit.title": "Audit log",
  "audit.col.token": "Token", "audit.col.action": "Action", "audit.col.target": "Target", "audit.col.detail": "Detail"
}
};

var state = {
  lang: localStorage.getItem("rw_lang") ||
    ((navigator.language || "").toLowerCase().indexOf("zh") === 0 ? "zh" : "en"),
  authed: false, role: "", view: "stats",
  turnstileSiteKey: "", turnstileToken: "", turnstileId: null,
  messagesCursor: "", unsubsCursor: "", auditCursor: ""
};

function t(key, vars) {
  var s = (I18N[state.lang] && I18N[state.lang][key]) || I18N.en[key] || I18N.zh[key] || key;
  if (vars) {
    for (var k in vars) { s = s.split("{" + k + "}").join(vars[k]); }
  }
  return s;
}

function applyI18n() {
  document.documentElement.lang = state.lang === "zh" ? "zh-CN" : "en";
  document.title = t("title");
  document.querySelectorAll("[data-i18n]").forEach(function (el) {
    el.textContent = t(el.getAttribute("data-i18n"));
  });
  document.querySelectorAll("[data-i18n-placeholder]").forEach(function (el) {
    el.placeholder = t(el.getAttribute("data-i18n-placeholder"));
  });
  $("#lang").textContent = state.lang === "zh" ? "English" : "中文";
  $("#login-hint").textContent = t("login.hint");
  if (state.authed) { showView(state.view); }
}

function $(sel) { return document.querySelector(sel); }
function $$(sel) { return Array.prototype.slice.call(document.querySelectorAll(sel)); }

var ERR_TEXT = { unauthorized: "err.unauthorized", forbidden: "err.forbidden", ip_banned: "err.ip_banned",
  captcha_failed: "err.captcha_failed", captcha_unavailable: "err.captcha_unavailable" };

function toast(message, isError) {
  var el = $("#toast");
  el.textContent = message;
  el.className = isError ? "error" : "";
  clearTimeout(el._t);
  el._t = setTimeout(function () { el.className = "hidden"; }, 3200);
}

function api(method, path, body) {
  var opts = { method: method, headers: {} };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  return fetch(path, opts).then(function (resp) {
    if (resp.status === 204) { return { status: 204 }; }
    return resp.json().then(function (data) { return { status: resp.status, data: data }; });
  }).then(function (r) {
    if (r.status >= 400) {
      var code = r.data && r.data.error ? r.data.error.code : String(r.status);
      toast(ERR_TEXT[code] ? t(ERR_TEXT[code]) : t("err.request") + code, true);
      throw new Error(code);
    }
    return r;
  });
}

function fmtTime(iso) {
  if (!iso) { return "-"; }
  return iso.replace("T", " ").replace("Z", " UTC");
}

function isoOrNull(input) {
  return input ? new Date(input).toISOString() : undefined;
}

/* ---- login / logout ------------------------------------------------------ */

function enterApp(role, tokenName) {
  state.authed = true;
  state.role = role;
  document.body.classList.remove("locked");
  $("#token").value = "";
  $("#token").classList.add("hidden");
  $("#login").classList.add("hidden");
  $("#captcha-row").classList.add("hidden");
  $("#logout").classList.remove("hidden");
  $("#who").textContent = t("login.who", { name: tokenName, role: role });
  // admin-only tabs
  $("#nav-tokens").classList.toggle("hidden", role !== "admin");
  $("#nav-audit").classList.toggle("hidden", role !== "admin");
  showView("stats");
  if (!localStorage.getItem("rw_guide_done")) { showGuide(true); }
}

function showLogin() {
  state.authed = false;
  document.body.classList.add("locked");
  $("#token").classList.remove("hidden");
  $("#login").classList.remove("hidden");
  $("#logout").classList.add("hidden");
  $("#who").textContent = "";
  $("#guide").classList.add("hidden");
  resetTurnstile();
}

function doLogin() {
  var token = $("#token").value.trim();
  if (!token) { toast(t("login.needtoken"), true); return; }
  var body = { token: token };
  if (state.turnstileSiteKey) {
    if (!state.turnstileToken) { toast(t("login.needcaptcha"), true); return; }
    body.turnstile = state.turnstileToken;
  }
  api("POST", "/api/login", body).then(function (r) {
    resetTurnstile();
    toast(t("login.ok"));
    enterApp(r.data.role, r.data.token_name);
  }).catch(function () {
    resetTurnstile();
  });
}

/* ---- Turnstile widget ---------------------------------------------------- */

function showCaptcha() {
  $("#captcha-row").classList.remove("hidden");
  if (window.turnstile) { renderTurnstile(); return; }
  var s = document.createElement("script");
  s.src = "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";
  s.async = true;
  s.onload = renderTurnstile;
  document.head.appendChild(s);
}

function renderTurnstile() {
  if (!window.turnstile || state.turnstileId !== null || !state.turnstileSiteKey) { return; }
  state.turnstileId = window.turnstile.render("#turnstile-widget", {
    sitekey: state.turnstileSiteKey,
    callback: function (tok) { state.turnstileToken = tok; },
    "expired-callback": function () { state.turnstileToken = ""; },
    "error-callback": function () { state.turnstileToken = ""; }
  });
}

function resetTurnstile() {
  state.turnstileToken = "";
  if (window.turnstile && state.turnstileId !== null) {
    try { window.turnstile.reset(state.turnstileId); } catch (e) { /* widget gone */ }
  }
}

/* ---- guide ---------------------------------------------------------------- */

var GUIDE_VIEWS = ["stats", "messages", "apps", "unsubs", "tokens", "audit"];

function showGuide(auto) {
  if (GUIDE_VIEWS.indexOf(state.view) < 0) { return; }
  var g = $("#guide");
  g.innerHTML = t("guide." + state.view) +
    '<div><button id="guide-gotit">' + t("guide.gotit") + "</button></div>";
  g.classList.remove("hidden");
  $("#guide-gotit").addEventListener("click", function () {
    localStorage.setItem("rw_guide_done", "1");
    g.classList.add("hidden");
  });
  if (auto) { window.scrollTo({ top: 0, behavior: "smooth" }); }
}

/* ---- views ---------------------------------------------------------------- */

function showView(name) {
  state.view = name;
  $$(".view").forEach(function (v) { v.classList.add("hidden"); });
  $("#view-" + name).classList.remove("hidden");
  $$("nav button[data-view]").forEach(function (b) {
    b.classList.toggle("active", b.dataset.view === name);
  });
  $("#guide").classList.add("hidden");
  if (name === "stats") { loadStats(); }
  if (name === "messages") { loadMessages(false); }
  if (name === "apps") { loadApps(); }
  if (name === "unsubs") { loadUnsubs(false); }
  if (name === "tokens") { loadTokens(); }
  if (name === "audit") { loadAudit(false); }
}

function fill(tbodyId, rows, render) {
  var tbody = $(tbodyId + " tbody");
  tbody.textContent = "";
  if (!rows.length) {
    var tr = document.createElement("tr");
    var td = document.createElement("td");
    td.colSpan = 8;
    td.className = "muted";
    td.textContent = t("common.noData");
    tr.appendChild(td);
    tbody.appendChild(tr);
    return;
  }
  rows.forEach(function (row) {
    var tr = document.createElement("tr");
    render(row).forEach(function (cell) {
      var td = document.createElement("td");
      if (cell instanceof Node) { td.appendChild(cell); }
      else { td.textContent = cell; }
      tr.appendChild(td);
    });
    tbody.appendChild(tr);
  });
}

function actionButton(label, fn) {
  var b = document.createElement("button");
  b.textContent = label;
  b.addEventListener("click", fn);
  return b;
}

function loadStats() {
  api("GET", "/api/stats").then(function (r) {
    fill("#stats-table", r.data.stats || [], function (s) {
      return [s.app, s.total, s.sent, s.failed, s.rate_limited, s.suppressed];
    });
  });
}

function loadMessages(reset) {
  if (reset) { state.messagesCursor = ""; }
  var f = $("#msg-filter");
  var q = new URLSearchParams();
  if (f.app.value) { q.set("app", f.app.value); }
  if (f.to.value) { q.set("to", f.to.value); }
  if (f.status.value) { q.set("status", f.status.value); }
  var since = isoOrNull(f.since.value);
  if (since) { q.set("since", since); }
  var until = isoOrNull(f.until.value);
  if (until) { q.set("until", until); }
  q.set("limit", "50");
  if (state.messagesCursor) { q.set("cursor", state.messagesCursor); }
  api("GET", "/api/messages?" + q).then(function (r) {
    state.messagesCursor = r.data.next_cursor || "";
    fill("#messages-table", r.data.messages || [], function (m) {
      return [fmtTime(m.ts), m.app, m.mail_from || "-", m.rcpt_to, m.subject || "-",
        t("status." + m.status) || m.status, m.size, m.message_id || "-"];
    });
  });
}

function loadApps() {
  api("GET", "/api/apps").then(function (r) {
    fill("#apps-table", r.data.apps || [], function (a) {
      var ops = document.createDocumentFragment();
      ops.appendChild(actionButton(a.enabled ? t("apps.disable") : t("apps.enable"), function () {
        api("PATCH", "/api/apps/" + a.name, { enabled: !a.enabled }).then(loadApps);
      }));
      ops.appendChild(actionButton(t("apps.rotate"), function () {
        api("POST", "/api/apps/" + a.name + "/rotate").then(function (r) {
          showOneTime(t("apps.rotated.pw") + r.data.smtp_password);
        });
      }));
      ops.appendChild(actionButton(t("apps.edit"), function () { editApp(a.name); }));
      ops.appendChild(actionButton(a.unsubscribe ? t("apps.unsub.off") : t("apps.unsub.on"), function () {
        api("PATCH", "/api/apps/" + a.name, { unsubscribe: !a.unsubscribe }).then(loadApps);
      }));
      ops.appendChild(actionButton(a.body_injection ? t("apps.inj.off") : t("apps.inj.on"), function () {
        api("PATCH", "/api/apps/" + a.name, { body_injection: !a.body_injection }).then(loadApps);
      }));
      ops.appendChild(actionButton(t("apps.delete"), function () {
        if (window.confirm(t("apps.delete.confirm", { name: a.name }))) {
          api("DELETE", "/api/apps/" + a.name).then(loadApps);
        }
      }));
      return [a.name, a.display_name || "-", (a.allowed_from || []).join(", "),
        a.enabled ? t("common.enabled") : t("common.disabled"),
        a.unsubscribe ? t("common.on") : t("common.off"),
        a.body_injection ? t("common.on") : t("common.off"), a.rate_per_hour, ops];
    });
  });
}

// GET /api/apps/{name} for fresh detail, then PATCH the editable fields.
function editApp(name) {
  api("GET", "/api/apps/" + encodeURIComponent(name)).then(function (r) {
    var form = $("#app-edit");
    form.classList.remove("hidden");
    $("#app-edit-name").textContent = name;
    form.display_name.value = r.data.app.display_name || "";
    form.allowed_from.value = (r.data.app.allowed_from || []).join(", ");
    form.rate_per_hour.value = r.data.app.rate_per_hour;
    form.scrollIntoView({ behavior: "smooth", block: "center" });
  });
}

function saveAppEdit(e) {
  e.preventDefault();
  var form = $("#app-edit");
  var name = $("#app-edit-name").textContent;
  var rate = parseInt(form.rate_per_hour.value, 10);
  var body = {
    display_name: form.display_name.value.trim(),
    allowed_from: form.allowed_from.value.split(",").map(function (s) { return s.trim(); }).filter(Boolean)
  };
  if (rate > 0) { body.rate_per_hour = rate; }
  api("PATCH", "/api/apps/" + encodeURIComponent(name), body).then(function () {
    form.classList.add("hidden");
    loadApps();
  });
}

function loadUnsubs(reset) {
  if (reset) { state.unsubsCursor = ""; }
  var q = "limit=50" + (state.unsubsCursor ? "&cursor=" + state.unsubsCursor : "");
  api("GET", "/api/unsubscribes?" + q).then(function (r) {
    state.unsubsCursor = r.data.next_cursor || "";
    fill("#unsubs-table", r.data.unsubscribes || [], function (u) {
      return [u.app, u.email, t("source." + u.source) || u.source, fmtTime(u.ts),
        actionButton(t("unsubs.resub"), function () {
          api("DELETE", "/api/unsubscribes/" + u.id).then(function () { loadUnsubs(false); });
        })];
    });
  });
}

function loadTokens() {
  api("GET", "/api/tokens").then(function (r) {
    fill("#tokens-table", r.data.tokens || [], function (tk) {
      return [tk.id, tk.name, tk.role,
        tk.expires_at ? fmtTime(tk.expires_at) : t("tokens.never"),
        tk.last_used ? fmtTime(tk.last_used) : "-",
        fmtTime(tk.created_at),
        actionButton(t("tokens.revoke"), function () {
          api("DELETE", "/api/tokens/" + tk.id).then(loadTokens);
        })];
    });
  });
}

function loadAudit(reset) {
  if (reset) { state.auditCursor = ""; }
  var q = "limit=50" + (state.auditCursor ? "&cursor=" + state.auditCursor : "");
  api("GET", "/api/audit?" + q).then(function (r) {
    state.auditCursor = r.data.next_cursor || "";
    fill("#audit-table", r.data.entries || [], function (e) {
      return [fmtTime(e.ts), e.token_name || "-", e.ip || "-", e.action, e.target || "-", e.detail || "-"];
    });
  });
}

function showOneTime(text) {
  var el = $("#one-time");
  el.textContent = text;
  el.classList.remove("hidden");
}

/* ---- events ---------------------------------------------------------------- */

$("#lang").addEventListener("click", function () {
  state.lang = state.lang === "zh" ? "en" : "zh";
  localStorage.setItem("rw_lang", state.lang);
  applyI18n();
});

$("#login").addEventListener("click", doLogin);
$("#token").addEventListener("keydown", function (e) {
  if (e.key === "Enter") { e.preventDefault(); doLogin(); }
});

$("#logout").addEventListener("click", function () {
  api("POST", "/api/logout").then(function () {
    showLogin();
    toast(t("login.bye"));
  });
});

$("#help").addEventListener("click", function () {
  var g = $("#guide");
  if (g.classList.contains("hidden")) { showGuide(false); }
  else { g.classList.add("hidden"); }
});

$$("nav button[data-view]").forEach(function (b) {
  b.addEventListener("click", function () { showView(b.dataset.view); });
});

$("#msg-filter").addEventListener("submit", function (e) { e.preventDefault(); loadMessages(true); });
$("#msg-next").addEventListener("click", function () { loadMessages(false); });
$("#unsubs-next").addEventListener("click", function () { loadUnsubs(false); });
$("#audit-next").addEventListener("click", function () { loadAudit(false); });

$("#app-create").addEventListener("submit", function (e) {
  e.preventDefault();
  var f = e.target;
  var body = {
    name: f.name.value.trim(),
    display_name: f.display_name.value.trim(),
    allowed_from: f.allowed_from.value.split(",").map(function (s) { return s.trim(); }).filter(Boolean),
    unsubscribe: f.unsubscribe.checked,
    body_injection: f.body_injection.checked
  };
  api("POST", "/api/apps", body).then(function (r) {
    showOneTime(t("apps.created.pw") + r.data.smtp_password + t("apps.created.save"));
    f.reset();
    f.unsubscribe.checked = true;
    f.body_injection.checked = true;
    loadApps();
  });
});

$("#app-edit").addEventListener("submit", saveAppEdit);
$("#app-edit-cancel").addEventListener("click", function () { $("#app-edit").classList.add("hidden"); });

$("#unsub-create").addEventListener("submit", function (e) {
  e.preventDefault();
  var f = e.target;
  api("POST", "/api/unsubscribes", { app: f.app.value.trim(), email: f.email.value.trim() })
    .then(function () { f.reset(); loadUnsubs(false); });
});

$("#token-create").addEventListener("submit", function (e) {
  e.preventDefault();
  var f = e.target;
  var body = { name: f.name.value.trim(), role: f.role.value };
  var exp = isoOrNull(f.expires.value);
  if (exp) { body.expires_at = exp; }
  api("POST", "/api/tokens", body).then(function (r) {
    $("#token-one-time").textContent = t("tokens.created") + r.data.token;
    $("#token-one-time").classList.remove("hidden");
    f.reset();
    loadTokens();
  });
});

$("#health").addEventListener("click", function () {
  fetch("/healthz").then(function (r) { return r.json(); }).then(function (d) {
    toast(t("health.toast") + d.status);
  });
});

/* ---- boot ------------------------------------------------------------------ */
/* GET /api/session tells us whether the browser still holds a live session
   and whether the captcha is enabled; it never reveals token material. */

applyI18n();

api("GET", "/api/session").then(function (r) {
  state.turnstileSiteKey = r.data.turnstile_site_key || "";
  if (state.turnstileSiteKey) { showCaptcha(); }
  if (r.data.authenticated) {
    enterApp(r.data.role, r.data.token_name);
  } else {
    showLogin();
  }
}).catch(function () {
  showLogin();
});
