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
  const logAuto   = document.getElementById("log-auto");
  if (!logBody || !logPanel) return;

  const MAX_LINES = 500;
  let logLines = 0;

  // Auto-scroll preference — persisted across reloads. Default ON so a
  // user who opens the panel after a flood of events sees the latest
  // line, not whatever scroll position the panel was at. When OFF we
  // fall back to the old "scroll only if user is already near bottom"
  // heuristic so a user reading older events doesn't get yanked away.
  let autoScroll = true;
  try {
    const saved = localStorage.getItem("log.autoScroll");
    if (saved === "0") autoScroll = false;
  } catch (_) {}
  if (logAuto) {
    logAuto.classList.toggle("is-on", autoScroll);
    logAuto.setAttribute("aria-pressed", autoScroll ? "true" : "false");
    logAuto.addEventListener("click", (e) => {
      e.stopPropagation();
      autoScroll = !autoScroll;
      try { localStorage.setItem("log.autoScroll", autoScroll ? "1" : "0"); } catch (_) {}
      logAuto.classList.toggle("is-on", autoScroll);
      logAuto.setAttribute("aria-pressed", autoScroll ? "true" : "false");
      // If user just enabled it, jump straight to the bottom so they
      // catch up immediately — that's the whole point of toggling it
      // back on when the panel has scrolled up.
      if (autoScroll) logBody.scrollTop = logBody.scrollHeight;
    });
  }

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
    // dir="ltr" on the numeric spans so the bidi algorithm doesn't
    // reorder "270→44B" into "44→270B" / "270>44" in Persian RTL
    // pages. IP + RTT + lens are all numeric/Latin and always need
    // to flow left-to-right regardless of page direction.
    tr.innerHTML = `
      <span class="lt" dir="ltr">${fmtClock(e.time)}</span>
      <span class="li-status">${e.status || e.kind || ""}</span>
      <span class="li-ip" dir="ltr">${e.ip || ""}</span>
      <span class="li-rtt" dir="ltr">${e.rtt_ms ? e.rtt_ms + "ms" : ""}</span>
      <span class="li-len" dir="ltr">${lens}</span>
      <span class="li-extra" title="${e.qname || ""}">${extra}</span>
    `;
    logBody.appendChild(tr);
    logLines++;
    while (logBody.childElementCount > MAX_LINES) logBody.removeChild(logBody.firstChild);
    if (logCount) logCount.textContent = String(logLines);
    // Two scroll modes:
    //   auto-scroll ON  → always pin to bottom (matches user expectation
    //                     of a "live" feed; default).
    //   auto-scroll OFF → only follow when the user is already near the
    //                     bottom (so reading older entries isn't
    //                     disrupted by new arrivals).
    if (autoScroll) {
      logBody.scrollTop = logBody.scrollHeight;
    } else {
      const nearBottom = logBody.scrollHeight - logBody.scrollTop - logBody.clientHeight < 40;
      if (nearBottom) logBody.scrollTop = logBody.scrollHeight;
    }
  }

  // Tap anywhere on the header (not on Clear / Auto-scroll) to
  // expand/collapse. When expanding, also pull the body to the bottom
  // if auto-scroll is on, so opening the panel mid-stream catches you
  // up to "now" instead of leaving you at the top of whatever was
  // last rendered before collapse.
  const logHead = document.querySelector("#log-panel .log-head");
  if (logHead) {
    logHead.addEventListener("click", (e) => {
      if (e.target.closest("#log-clear")) return;
      if (e.target.closest("#log-auto"))  return;
      const c = logPanel.dataset.collapsed === "true";
      logPanel.dataset.collapsed = c ? "false" : "true";
      if (c && autoScroll) {
        // We just expanded — jump to bottom on next frame so the new
        // body layout has happened.
        requestAnimationFrame(() => {
          logBody.scrollTop = logBody.scrollHeight;
        });
      }
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
