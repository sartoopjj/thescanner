(async function () {
  const tt = (k, fb) => (window.tt ? window.tt(k, fb) : fb);
  const qs = new URLSearchParams(location.search);
  const id = qs.get("id");
  if (!id) { location.href = "/lists"; return; }

  // Config (for default-server + the settings-summary panel).
  const cfg = await (await fetch("/api/config")).json();
  const servers = cfg.servers || [];
  const defaultServer = servers[0] ? servers[0].name : "";

  // ---- helpers --------------------------------------------------------
  function statusBadge(s) {
    const map = {
      pending:   ["neutral", "lists.s_pending"],
      scanning:  ["running", "lists.s_scanning"],
      paused:    ["warn",    "lists.s_paused"],
      done:      ["ok",      "lists.s_done"],
      deep:      ["running", "lists.s_deep"],
      deep_done: ["ok",      "lists.s_deep_done"],
    };
    const [c, k] = map[s] || ["neutral", s];
    return `<span class="badge badge-${c}">${tt(k, s)}</span>`;
  }
  function fmtDate(iso) {
    const d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    const loc = document.body.dataset.lang === "fa" ? "fa-IR" : undefined;
    return d.toLocaleString(loc);
  }
  function fmtDuration(ms) {
    if (!ms || ms < 0) return "—";
    const s = Math.floor(ms / 1000);
    const h = Math.floor(s / 3600);
    const m = Math.floor((s % 3600) / 60);
    const sec = s % 60;
    if (h > 0) return `${h}h ${m}m`;
    if (m > 0) return `${m}m ${sec}s`;
    return `${sec}s`;
  }
  function kindLabel(k) {
    return k === "manual"
      ? tt("lists.kind_manual","manual")
      : tt("lists.kind_shallow","shallow scan");
  }
  function show(elId, on) { document.getElementById(elId).hidden = !on; }

  // ---- meta + progress ------------------------------------------------
  let meta = null;
  let startedAt = null;

  // Pending-transition state machine: when the user clicks Pause / Resume
  // / Deep, the API returns immediately but the actual goroutine cleanup
  // and status save can take 1–5 seconds. Without this, applyMeta() on
  // the next refresh sees the OLD status and re-shows the original
  // button, making the "Stopping…" feedback flash and disappear before
  // the scan actually stops. We freeze button state into the pending
  // shape until either (a) status matches the expected target or (b)
  // the 10 s deadline passes (so a stuck/crashed runner doesn't lock
  // the UI permanently).
  let pending = null; // { target: "paused" | "scanning" | "deep", deadline: ms, label: i18n key }

  function applyMeta(m) {
    meta = m;
    document.getElementById("m-name").textContent = m.name;
    document.getElementById("m-status-badge").outerHTML =
      `<span id="m-status-badge">${statusBadge(m.status)}</span>`;
    document.getElementById("m-kind").textContent    = kindLabel(m.kind);
    document.getElementById("m-server").textContent  = m.server || "—";
    document.getElementById("m-created").textContent = fmtDate(m.created);
    document.getElementById("m-total").textContent   = m.total;
    document.getElementById("m-ok").textContent      = m.ok;
    document.getElementById("m-failed").textContent  = m.failed;
    document.getElementById("m-l2").textContent      = m.l2_scored || 0;

    // Resolve the pending transition: cleared when status reaches the
    // target OR the deadline expires (so the UI eventually unfreezes
    // even if the server never confirms).
    //
    // Subtle: for pause we accept ANY non-running terminal state
    // (paused / done / deep_done) — not just "paused" — because the
    // scan might have completed naturally at the same moment the user
    // clicked Pause. Without this the button stayed stuck on
    // "Stopping…" until the 10 s deadline elapsed even though the
    // scan was already done.
    if (pending) {
      const stillRunning = (m.status === "scanning" || m.status === "deep");
      const reached =
        (pending.target === "paused"   && !stillRunning) ||
        (pending.target === "scanning" &&  stillRunning) ||
        (pending.target === "deep"     && (m.status === "deep" || m.status === "deep_done"));
      if (reached || Date.now() > pending.deadline) {
        pending = null;
      }
    }

    const running = (m.status === "scanning" || m.status === "deep");
    const hasOK   = m.ok > 0 || m.kind === "manual";

    if (pending) {
      // Force the appropriate "in-flight" button visible, disabled,
      // with a localized "Stopping…/Resuming…/Starting…" label. Hide
      // the others so the user can't fire a conflicting action.
      const isStop = pending.target === "paused";
      const stopBtn   = document.getElementById("btn-pause");
      const startBtn  = document.getElementById("btn-resume");
      const deepBtn2  = document.getElementById("btn-deep");
      if (isStop) {
        show("btn-pause",  true);  stopBtn.disabled = true;
        stopBtn.textContent = tt(pending.label, "Stopping…");
        show("btn-resume", false);
        show("btn-deep",   false);
      } else {
        // Resume/Deep — borrow whichever button matches.
        show("btn-pause",  false);
        if (pending.target === "deep") {
          show("btn-deep",   true);  deepBtn2.disabled = true;
          deepBtn2.textContent = tt(pending.label, "Starting…");
          show("btn-resume", false);
        } else {
          show("btn-resume", true);  startBtn.disabled = true;
          startBtn.textContent = tt(pending.label, "Resuming…");
          show("btn-deep",   false);
        }
      }
      show("btn-rescan-ok",  false);
      show("btn-rescan-all", false);
    } else {
      show("btn-pause",      running);
      show("btn-resume",     !running && m.status === "paused");
      show("btn-deep",       !running && hasOK && m.status !== "deep");
      show("btn-rescan-ok",  !running && m.kind === "shallow" && m.ok > 0);
      show("btn-rescan-all", !running && m.kind === "shallow");
    }

    document.getElementById("export-txt").href = `/api/lists/${id}/export?format=txt&status=ok`;
    document.getElementById("export-csv").href = `/api/lists/${id}/export?format=csv`;
    // Top-N download is wired once below (not per applyMeta) — it reads the
    // input live, so we don't need to refresh the href here.

    updateProgress(m);
  }

  function updateProgress(m) {
    const card = document.getElementById("progress-card");
    // Show the progress panel for anything that's running OR has been
    // running, so the user can review counts post-hoc too.
    const interesting = m.kind === "shallow" || m.kind === "manual";
    card.hidden = !interesting;
    if (!interesting) return;

    // ----- shallow row -----
    const total  = Math.max(0, m.total);
    const done   = (m.ok || 0) + (m.failed || 0);
    const pct    = total > 0 ? Math.floor((done / total) * 100) : 0;
    const bar    = document.getElementById("shallow-bar");
    bar.max      = total > 0 ? total : 1;
    bar.value    = done;
    document.getElementById("shallow-pct").textContent = pct + "%";
    document.getElementById("p-ok").textContent     = m.ok || 0;
    document.getElementById("p-failed").textContent = m.failed || 0;
    document.getElementById("p-total").textContent  = m.total || 0;

    // Manual lists skip the shallow phase; relabel the row.
    document.getElementById("shallow-title").textContent =
      m.kind === "manual"
        ? tt("lists.kind_manual", "manual")
        : tt("lists.shallow_progress", "Shallow scan");

    // Elapsed + ETA for the SHALLOW phase only. Including m.status==="deep"
    // here was a bug — the deep row has its own progress and the shallow
    // row's elapsed/ETA shouldn't keep ticking after the shallow pass
    // is done. Now: shows live while shallow is running, hides once we
    // transition to paused/done/deep/deep_done.
    const shallowRunning = (m.status === "scanning");
    if (shallowRunning) {
      if (!startedAt) startedAt = Date.now();
      const elapsed = Date.now() - startedAt;
      show("p-elapsed-wrap", true);
      document.getElementById("p-elapsed").textContent = fmtDuration(elapsed);
      if (done > 0 && total > done) {
        const remain = ((elapsed / done) * (total - done)) | 0;
        show("p-eta-wrap", true);
        document.getElementById("p-eta").textContent = fmtDuration(remain);
      } else {
        show("p-eta-wrap", false);
      }
    } else {
      startedAt = null;
      show("p-elapsed-wrap", false);
      show("p-eta-wrap", false);
    }

    // ----- deep row -----
    const elig   = m.kind === "manual" ? (m.total || 0) : (m.ok || 0);
    const scored = m.l2_scored || 0;
    // Query-level progress (counter incremented per-query in the Go
    // side via atomic.AddInt64). Drives the progress bar smoothly,
    // because per-IP `scored` only ticks once every QueriesPerResolver
    // queries and the bar would sit at 0% for many minutes otherwise.
    const qDone  = m.l2_queries_done  || 0;
    const qTotal = m.l2_queries_total || 0;
    const deepRunningOrDone =
      m.status === "deep" || m.status === "deep_done" || scored > 0 || qDone > 0;
    show("deep-row", deepRunningOrDone && elig > 0);
    if (deepRunningOrDone && elig > 0) {
      const dbar = document.getElementById("deep-bar");
      if (qTotal > 0) {
        dbar.max   = qTotal;
        dbar.value = Math.min(qDone, qTotal);
        document.getElementById("deep-pct").textContent =
          Math.floor((dbar.value / qTotal) * 100) + "%";
      } else {
        // Fallback for old saved lists without the query counters.
        dbar.max   = elig;
        dbar.value = scored;
        document.getElementById("deep-pct").textContent =
          (elig > 0 ? Math.floor((scored / elig) * 100) : 0) + "%";
      }
      document.getElementById("p-l2-scored").textContent = scored;
      document.getElementById("p-l2-elig").textContent   = elig;
      const qEl = document.getElementById("p-l2-queries");
      if (qEl) {
        // dir="ltr" so the "X / Y" pair flows left-to-right even on
        // the Persian/RTL page — without it bidi flips digits around
        // the slash and "1,234 / 5,000" renders as "5,000 / 1,234"
        // or worse, with the slash itself looking like ">".
        qEl.dir = "ltr";
        qEl.textContent = qTotal > 0
          ? qDone.toLocaleString() + " / " + qTotal.toLocaleString()
          : "";
      }
    }

    populateSettingsSummary();
  }

  function populateSettingsSummary() {
    const body = document.getElementById("settings-summary-body");
    if (!body || body.dataset.filled === "1") return;
    body.dataset.filled = "1";
    const sc = cfg.scan   || {};
    const l2 = cfg.level2 || {};
    const isFa = (document.body.dataset.lang || "en") === "fa";
    const onTxt  = tt("common.on",  "on");
    const offTxt = tt("common.off", "off");
    const fa = (n) => isFa ? String(n).replace(/[0-9]/g, d => "۰۱۲۳۴۵۶۷۸۹"[+d]) : n;
    const num = (n) => n == null ? "—" : fa(n);
    const bool = (b) => b ? onTxt : offTxt;
    const rows = [
      ["scan.min_query",       num(sc.min_query)],
      ["scan.max_query",       num(sc.max_query)],
      ["scan.min_response",    num(sc.min_response)],
      ["scan.max_response",    num(sc.max_response)],
      ["scan.parallel",        num(sc.parallel)],
      ["scan.duplicate",       num(sc.duplicate)],
      ["scan.timeout_seconds", num(sc.timeout_seconds)],
      ["scan.retries",         num(sc.retries)],
      ["scan.edns0",           bool(sc.edns0)],
      ["scan.subnet_expand",   sc.subnet_expand ? "/" + fa(sc.subnet_mask) : offTxt],
      ["l2.queries_per_resolver", num(l2.queries_per_resolver)],
      ["l2.parallel",          num(l2.parallel)],
    ];
    body.innerHTML = rows.map(([k, v]) => {
      const label = tt("settings_summary." + k, k);
      return `<span class="k">${label}</span><span class="v">${v}</span>`;
    }).join("");
  }

  // ---- lifecycle ------------------------------------------------------
  // Submitting a lifecycle action (pause / resume / deep) installs a
  // "pending transition" that applyMeta honors on every subsequent
  // refresh — the button stays in the "Stopping…/Resuming…/Starting…"
  // shape until status reaches the expected target OR a 10 s deadline
  // passes. This is what stops Pause from "flashing and going back to
  // Pause" while the goroutine is still finishing in-flight queries.
  let actionInFlight = false;
  async function action(name) {
    if (actionInFlight) return;
    actionInFlight = true;
    // Predict the next status. For Resume we look at l2_scored / kind
    // to decide whether the runner will resume as shallow or deep.
    const target = name === "pause" ? "paused"
                 : name === "deep"  ? "deep"
                 : (name === "resume"
                    ? ((meta && (meta.l2_scored > 0 || meta.kind === "manual")) ? "deep" : "scanning")
                    : null);
    const labelKey = name === "pause" ? "scan.stopping"
                   : name === "deep"  ? "scan.starting"
                   : "scan.resuming";
    if (target) {
      pending = { target, label: labelKey, deadline: Date.now() + 10000 };
    }
    try {
      const r = await fetch(`/api/lists/${id}/${name}`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ server: meta && meta.server ? meta.server : defaultServer }),
      });
      if (!r.ok) {
        const j = await r.json().catch(() => ({}));
        showError(j);
        pending = null; // API failed — release the freeze immediately.
      }
      await refresh();
    } finally {
      actionInFlight = false;
    }
  }
  document.getElementById("btn-pause" ).onclick = () => action("pause");
  document.getElementById("btn-resume").onclick = () => action("resume");
  document.getElementById("btn-deep"  ).onclick = () => action("deep");

  // Rescan: create a new list seeded from this one.
  async function rescan(okOnly) {
    const body = {
      rescan_from: id,
      rescan_ok_only: okOnly,
      server: meta && meta.server ? meta.server : defaultServer,
      auto_start: true,
    };
    const r = await fetch("/api/lists", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body),
    });
    if (!r.ok) {
      const j = await r.json().catch(() => ({}));
      showError(j);
      return;
    }
    const nm = await r.json();
    location.href = "/list?id=" + encodeURIComponent(nm.id);
  }
  document.getElementById("btn-rescan-ok" ).onclick = () => rescan(true);
  document.getElementById("btn-rescan-all").onclick = () => rescan(false);

  // ---- rename ---------------------------------------------------------
  const renameModal = document.getElementById("rename-modal");
  const renameInput = document.getElementById("rename-input");
  document.getElementById("btn-rename").onclick = () => {
    renameInput.value = (meta && meta.name) || "";
    renameModal.hidden = false;
    setTimeout(() => renameInput.focus(), 30);
  };
  const closeRename = () => { renameModal.hidden = true; };
  document.getElementById("rename-cancel").onclick = closeRename;
  document.getElementById("rename-close" ).onclick = closeRename;
  renameModal.addEventListener("click", e => { if (e.target === renameModal) closeRename(); });
  document.getElementById("rename-ok").onclick = async () => {
    const name = renameInput.value.trim();
    if (!name) return;
    const r = await fetch(`/api/lists/${id}/rename`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ name }),
    });
    if (!r.ok) {
      const j = await r.json().catch(() => ({}));
      showError(j);
      return;
    }
    closeRename();
    refresh();
  };

  // ---- delete ---------------------------------------------------------
  // Top-N export — downloads (or copies to clipboard) just the best-
  // scored OK IPs. Uses the server's ?sort=score&top=N&status=ok
  // query so the sort happens on the full result set (the table
  // pagination's view is irrelevant). Click → download .txt; Shift-
  // click → copy to clipboard instead (handy on mobile WebViews
  // where downloads ask for permissions).
  const topNBtn   = document.getElementById("export-topn-txt");
  const topNInput = document.getElementById("export-topn-input");
  if (topNBtn && topNInput) {
    topNBtn.addEventListener("click", async (ev) => {
      const n = Math.max(1, parseInt(topNInput.value, 10) || 100);
      const url = `/api/lists/${id}/export?format=txt&status=ok&sort=score&top=${n}`;
      if (ev.shiftKey) {
        // Shift-click: fetch + copy to clipboard, no download.
        try {
          const r = await fetch(url);
          const t = await r.text();
          await navigator.clipboard.writeText(t);
          topNBtn.textContent = tt("results.copied", "Copied");
          setTimeout(() => {
            topNBtn.textContent = tt("results.export_topn", "Copy/Download top N");
          }, 1500);
        } catch (_) {
          // Clipboard may not be available (insecure context) — fall
          // back to a download so the user still gets the file.
          window.location.href = url;
        }
        return;
      }
      // Regular click → trigger a normal download.
      window.location.href = url;
    });
  }

  document.getElementById("btn-delete").onclick = async () => {
    if (!confirm(tt("lists.confirm_delete", "Delete this list? This cannot be undone."))) return;
    const r = await fetch(`/api/lists/${id}`, { method: "DELETE" });
    if (!r.ok) {
      const j = await r.json().catch(() => ({}));
      showError(j);
      return;
    }
    location.href = "/lists";
  };

  // ---- results table + pagination ------------------------------------
  const filterEl = document.getElementById("filter");
  const searchEl = document.getElementById("search");
  const tbody    = document.querySelector("#results-table tbody");
  const prevBtn  = document.getElementById("prev-page");
  const nextBtn  = document.getElementById("next-page");
  const pageInfo = document.getElementById("page-info");
  const pageSize = document.getElementById("page-size");

  let offset = 0;
  let limit  = parseInt(pageSize.value, 10) || 100;
  let total  = 0;
  let lastSearchAt = 0;

  function fmt(n) { return (n == null || n === "") ? "" : n; }

  async function loadResults() {
    const params = new URLSearchParams();
    if (filterEl.value) params.set("status", filterEl.value);
    const q = searchEl.value.trim();
    if (q) params.set("q", q);
    params.set("offset", String(offset));
    params.set("limit",  String(limit));

    let j = {};
    try {
      const r = await fetch(`/api/lists/${id}/results?` + params.toString());
      j = await r.json();
    } catch (e) { return; }

    if (j.meta) applyMeta(j.meta);
    total = j.count || 0;
    if (offset >= total) offset = Math.max(0, total - limit);

    tbody.innerHTML = "";
    const listKind = (meta && meta.kind) || "";
    const subLT1 = tt("results.sub_ms", "<1ms");

    // Render RTT: 0 means "sub-millisecond" when we DID measure (shallow
    // ran or this is a deep-tested row), and "not measured" otherwise.
    function rttCell(row) {
      if (row.status !== "ok") return "";
      if (row.rtt_ms > 0) return row.rtt_ms;
      // Status=ok with rtt_ms=0: distinguish "sub-ms loopback" from
      // "shallow never ran" (manual list, no deep scan either).
      if (listKind === "manual" && !(row.l2_total > 0)) return "";
      return subLT1;
    }
    function p95Cell(row) {
      if (!(row.l2_total > 0)) return "";
      return row.l2_p95_ms > 0 ? row.l2_p95_ms : subLT1;
    }
    function l2OkCell(row) {
      if (!(row.l2_total > 0)) return "";
      return `${row.l2_ok} / ${row.l2_total}`;
    }
    function scoreCell(row) {
      if (!(row.l2_total > 0)) return "";
      return row.l2_score.toFixed(2);
    }

    (j.results || []).forEach(row => {
      const tr = document.createElement("tr");
      // dir="ltr" on each numeric cell so "44 / 270" doesn't get bidi-
      // reordered into "270 / 44" (or render the slash as ">") on the
      // Persian/RTL page. IP, RTT, L2-ok, p95, score are all numeric +
      // ASCII so LTR is always correct.
      tr.innerHTML = `
        <td dir="ltr">${row.ip}${row.source === "subnet" ? ' <span class="tag-subnet" title="discovered via /24 expand">+</span>' : ""}</td>
        <td class="status-${row.status}">${row.status}${row.reason ? " ("+row.reason+")" : ""}</td>
        <td dir="ltr">${rttCell(row)}</td>
        <td dir="ltr">${l2OkCell(row)}</td>
        <td dir="ltr">${p95Cell(row)}</td>
        <td dir="ltr">${scoreCell(row)}</td>
        <td>${row.source || ""}</td>
      `;
      tbody.appendChild(tr);
    });
    const startIdx = total === 0 ? 0 : (offset + 1);
    const endIdx   = Math.min(offset + limit, total);
    pageInfo.textContent = total === 0 ? "0 / 0" : `${startIdx}–${endIdx} / ${total}`;
    prevBtn.disabled = (offset <= 0);
    nextBtn.disabled = (offset + limit >= total);
  }

  prevBtn.onclick = () => { offset = Math.max(0, offset - limit); loadResults(); };
  nextBtn.onclick = () => { offset = offset + limit; loadResults(); };
  filterEl.onchange = () => { offset = 0; loadResults(); };
  pageSize.onchange = () => {
    limit  = parseInt(pageSize.value, 10) || 100;
    offset = 0;
    loadResults();
  };
  searchEl.addEventListener("input", () => {
    const now = Date.now(); lastSearchAt = now;
    setTimeout(() => { if (lastSearchAt === now) { offset = 0; loadResults(); } }, 250);
  });

  async function refresh() { await loadResults(); }
  refresh();
  setInterval(refresh, 2000);

  // Confirm-on-leave: if a scan is actively running on THIS list, warn
  // the user before they accidentally navigate away. Browsers ignore
  // custom messages now (`returnValue` is the only thing they honor),
  // but the prompt itself still appears. We only attach this when a
  // scan is running, so reading other lists doesn't trigger nags.
  window.addEventListener("beforeunload", (e) => {
    const s = meta && meta.status;
    if (s === "scanning" || s === "deep") {
      e.preventDefault();
      e.returnValue = tt("scan.confirm_leave",
        "A scan is running. Leave the page anyway?");
    }
  });

  // Log-panel wiring lives in log.js and is loaded by every page via
  // layout.html's footer template. list.html sets window.__logListID
  // before log.js loads so the stream is filtered to this list only.
})();
