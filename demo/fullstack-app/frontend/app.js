// Tiny vanilla JS frontend. No framework — just fetch + DOM.
// nginx proxies /api/* to the backend Service (see nginx.conf).

(function () {
  "use strict";

  const statusEl = document.getElementById("status");
  const messagesEl = document.getElementById("messages");
  const refreshBtn = document.getElementById("refresh-btn");

  async function checkHealth() {
    statusEl.textContent = "checking...";
    statusEl.className = "";
    try {
      const r = await fetch("/api/health", { cache: "no-store" });
      if (r.ok) {
        const j = await r.json();
        statusEl.textContent = "ok (" + j.status + ")";
        statusEl.className = "ok";
      } else {
        statusEl.textContent = "degraded (HTTP " + r.status + ")";
        statusEl.className = "warn";
      }
    } catch (e) {
      statusEl.textContent = "unreachable: " + e.message;
      statusEl.className = "err";
    }
  }

  async function loadMessages() {
    messagesEl.innerHTML = '<li class="loading">loading...</li>';
    try {
      const r = await fetch("/api/messages", { cache: "no-store" });
      if (!r.ok) {
        messagesEl.innerHTML = '<li class="err">HTTP ' + r.status + '</li>';
        return;
      }
      const rows = await r.json();
      if (!rows || rows.length === 0) {
        messagesEl.innerHTML = '<li class="empty">(no messages)</li>';
        return;
      }
      messagesEl.innerHTML = "";
      for (const m of rows) {
        const li = document.createElement("li");
        const created = new Date(m.created_at).toISOString().replace("T", " ").slice(0, 19);
        li.innerHTML =
          '<span class="msg-id">#' + m.id + '</span>' +
          '<span class="msg-body"></span>' +
          '<span class="msg-time">' + created + '</span>';
        li.querySelector(".msg-body").textContent = m.body; // safe insert
        messagesEl.appendChild(li);
      }
    } catch (e) {
      messagesEl.innerHTML = '<li class="err">fetch failed: ' + e.message + '</li>';
    }
  }

  refreshBtn.addEventListener("click", () => {
    checkHealth();
    loadMessages();
  });

  // initial load
  checkHealth();
  loadMessages();
})();
