// Live-query log panel — present on every page so you can see what
// scans are doing even before you've opened a list. Subscribes to
// /api/log/stream globally; the /list page narrows the stream to one
// list by exposing the ID via <body data-list-id="..."> which we read
// below. (We avoid an inline <script>window.__logListID = {{...}}</script>
// because the Go template syntax confuses JS linters.)
(function () {
  const logBody   = document.getElementById("log-body");
  const logCount  = document.getElementById("log-count");
  const logPanel  = document.getElementById("log-panel");
  const logToggle = document.getElementById("log-toggle");
  const logClear  = document.getElementById("log-clear");
  if (!logBody || !logPanel) return;

  const MAX_LINES = 500;
  let logLines = 0;

  function fmtClock(iso) {
    const d = new Date(iso);
    if (isNaN(d.getTime())) return "--:--:--";
    return d.toLocaleTimeString([], { hour12: false });
  }

  function appendLog(e) {
    const tr = document.createElement("div");
    const ok = e.status === "ok";
    tr.className = "log-line " + (ok ? "ok" : (e.status === "fail" ? "fail" : ""));
    const extra = e.qname ? e.qname
                : e.domain ? `→ ${e.domain}`
                : e.reason ? e.reason
                : (e.message || "");
    const lens = (e.q_len || e.resp_len)
      ? `${e.q_len || 0}→${e.resp_len || 0}B`
      : "";
    tr.innerHTML = `
      <span class="lt">${fmtClock(e.time)}</span>
      <span class="li-status">${e.status || e.kind || ""}</span>
      <span class="li-ip">${e.ip || ""}</span>
      <span class="li-rtt">${e.rtt_ms ? e.rtt_ms + "ms" : ""}</span>
      <span class="li-len">${lens}</span>
      <span class="li-extra" title="${e.qname || ""}">${extra}</span>
    `;
    logBody.appendChild(tr);
    logLines++;
    while (logBody.childElementCount > MAX_LINES) logBody.removeChild(logBody.firstChild);
    if (logCount) logCount.textContent = String(logLines);
    const nearBottom = logBody.scrollHeight - logBody.scrollTop - logBody.clientHeight < 40;
    if (nearBottom) logBody.scrollTop = logBody.scrollHeight;
  }

  // Tap anywhere on the header (not on Clear) to expand/collapse.
  const logHead = document.querySelector("#log-panel .log-head");
  if (logHead) {
    logHead.addEventListener("click", (e) => {
      if (e.target.closest("#log-clear")) return;
      const c = logPanel.dataset.collapsed === "true";
      logPanel.dataset.collapsed = c ? "false" : "true";
    });
    logHead.style.cursor = "pointer";
  }
  if (logClear) {
    logClear.onclick = (e) => {
      e.stopPropagation();
      logBody.innerHTML = "";
      logLines = 0;
      if (logCount) logCount.textContent = "0";
    };
  }

  // Per-list filter when set. list.html exposes the ID via
  //   <body data-list-id="...">
  // window.__logListID is honored as a fallback so any other caller
  // that still sets it the old way keeps working.
  const listID =
    (document.body && document.body.dataset && document.body.dataset.listId) ||
    window.__logListID ||
    "";
  const url = listID
    ? `/api/log/stream?list=${encodeURIComponent(listID)}`
    : `/api/log/stream`;
  const es = new EventSource(url);
  es.onmessage = (m) => { try { appendLog(JSON.parse(m.data)); } catch (_) {} };
  es.onerror = () => { /* browser auto-reconnects */ };
  window.addEventListener("beforeunload", () => es.close());
})();
