/* Relayward admin page: talks to the existing management API only. */
"use strict";

var state = { token: "", messagesCursor: "", auditCursor: "" };

function $(sel) { return document.querySelector(sel); }
function $$(sel) { return Array.prototype.slice.call(document.querySelectorAll(sel)); }

function toast(message, isError) {
  var el = $("#toast");
  el.textContent = message;
  el.className = isError ? "error" : "";
  clearTimeout(el._t);
  el._t = setTimeout(function () { el.className = "hidden"; }, 3200);
}

function api(method, path, body) {
  var opts = { method: method, headers: {} };
  if (state.token) { opts.headers["Authorization"] = "Bearer " + state.token; }
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  return fetch(path, opts).then(function (resp) {
    if (resp.status === 204) { return { status: 204 }; }
    return resp.json().then(function (data) { return { status: resp.status, data: data }; });
  }).then(function (r) {
    if (r.status === 401) { toast("认证失败：token 缺失或无效", true); throw new Error("unauthorized"); }
    if (r.status === 403) { toast("权限不足", true); throw new Error("forbidden"); }
    if (r.status >= 400) {
      var msg = r.data && r.data.error ? r.data.error.code : r.status;
      toast("请求失败：" + msg, true);
      throw new Error(msg);
    }
    return r;
  });
}

function fmtTime(iso) {
  if (!iso) { return "-"; }
  return iso.replace("T", " ").replace("Z", " UTC");
}

/* ---- views ------------------------------------------------------------- */

function showView(name) {
  $$(".view").forEach(function (v) { v.classList.add("hidden"); });
  $("#view-" + name).classList.remove("hidden");
  $$("nav button[data-view]").forEach(function (b) {
    b.classList.toggle("active", b.dataset.view === name);
  });
  if (name === "stats") { loadStats(); }
  if (name === "messages") { loadMessages(false); }
  if (name === "apps") { loadApps(); }
  if (name === "unsubs") { loadUnsubs(); }
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
    td.textContent = "暂无数据";
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

function msgQuery() {
  var f = $("#msg-filter");
  var q = new URLSearchParams();
  if (f.app.value) { q.set("app", f.app.value); }
  if (f.to.value) { q.set("to", f.to.value); }
  if (f.status.value) { q.set("status", f.status.value); }
  q.set("limit", "50");
  return q;
}

function loadMessages(reset) {
  if (reset) { state.messagesCursor = ""; }
  var q = msgQuery();
  if (state.messagesCursor) { q.set("cursor", state.messagesCursor); }
  api("GET", "/api/messages?" + q).then(function (r) {
    state.messagesCursor = r.data.next_cursor || "";
    fill("#messages-table", r.data.messages || [], function (m) {
      return [fmtTime(m.ts), m.app, m.rcpt_to, m.subject || "-", m.status, m.size, m.message_id || "-"];
    });
  });
}

function loadApps() {
  api("GET", "/api/apps").then(function (r) {
    fill("#apps-table", r.data.apps || [], function (a) {
      var ops = document.createDocumentFragment();
      ops.appendChild(actionButton(a.enabled ? "停用" : "启用", function () {
        api("PATCH", "/api/apps/" + a.name, { enabled: !a.enabled }).then(loadApps);
      }));
      ops.appendChild(actionButton("重置密码", function () {
        api("POST", "/api/apps/" + a.name + "/rotate").then(function (r) {
          showOneTime("新的 SMTP 密码（只显示一次）：" + r.data.smtp_password);
        });
      }));
      ops.appendChild(actionButton(a.unsubscribe ? "关退订" : "开退订", function () {
        api("PATCH", "/api/apps/" + a.name, { unsubscribe: !a.unsubscribe }).then(loadApps);
      }));
      ops.appendChild(actionButton(a.body_injection ? "关注入" : "开注入", function () {
        api("PATCH", "/api/apps/" + a.name, { body_injection: !a.body_injection }).then(loadApps);
      }));
      ops.appendChild(actionButton("删除", function () {
        if (window.confirm("删除程序 " + a.name + "？（日志保留）")) {
          api("DELETE", "/api/apps/" + a.name).then(loadApps);
        }
      }));
      return [a.name, a.display_name || "-", a.enabled ? "启用" : "停用",
        a.unsubscribe ? "开" : "关", a.body_injection ? "开" : "关", a.rate_per_hour, ops];
    });
  });
}

function loadUnsubs() {
  api("GET", "/api/unsubscribes?limit=50").then(function (r) {
    fill("#unsubs-table", r.data.unsubscribes || [], function (u) {
      return [u.app, u.email, u.source, fmtTime(u.ts),
        actionButton("恢复订阅", function () {
          api("DELETE", "/api/unsubscribes/" + u.id).then(loadUnsubs);
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

/* ---- events ------------------------------------------------------------ */

$("#save-token").addEventListener("click", function () {
  state.token = $("#token").value.trim();
  if (!state.token) { toast("请先输入 token", true); return; }
  localStorage.setItem("rw_admin_token", state.token);
  api("GET", "/api/stats").then(function () {
    $("#who").textContent = "已连接";
    toast("已保存 token");
    showView("stats");
  });
});

$$("nav button[data-view]").forEach(function (b) {
  b.addEventListener("click", function () { showView(b.dataset.view); });
});

$("#msg-filter").addEventListener("submit", function (e) { e.preventDefault(); loadMessages(true); });
$("#msg-next").addEventListener("click", function () { loadMessages(false); });
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
    showOneTime("SMTP 密码（只显示一次）：" + r.data.smtp_password + " —— 请立即保存");
    f.reset();
    f.unsubscribe.checked = true;
    f.body_injection.checked = true;
    loadApps();
  });
});

$("#unsub-create").addEventListener("submit", function (e) {
  e.preventDefault();
  var f = e.target;
  api("POST", "/api/unsubscribes", { app: f.app.value.trim(), email: f.email.value.trim() })
    .then(function () { f.reset(); loadUnsubs(); });
});

$("#health").addEventListener("click", function () {
  fetch("/healthz").then(function (r) { return r.json(); }).then(function (d) {
    toast("健康状态：" + d.status);
  });
});

/* ---- boot -------------------------------------------------------------- */

state.token = localStorage.getItem("rw_admin_token") || "";
if (state.token) {
  $("#token").value = state.token;
  api("GET", "/api/stats").then(function () {
    $("#who").textContent = "已连接";
    showView("stats");
  }).catch(function () {
    $("#who").textContent = "token 失效，请重新输入";
  });
} else {
  showView("stats");
}
