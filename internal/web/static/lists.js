(async function () {
  const tt = (k, fb) => (window.tt ? window.tt(k, fb) : fb);
  const empty = document.getElementById("empty-lists");
  const card  = document.getElementById("lists-card");
  const grid  = document.getElementById("list-grid");

  function statusBadge(s) {
    const map = {
      pending:   { c: "neutral", k: "lists.s_pending" },
      scanning:  { c: "running", k: "lists.s_scanning" },
      paused:    { c: "warn",    k: "lists.s_paused" },
      done:      { c: "ok",      k: "lists.s_done" },
      deep:      { c: "running", k: "lists.s_deep" },
      deep_done: { c: "ok",      k: "lists.s_deep_done" },
    };
    const m = map[s] || { c: "neutral", k: s };
    return `<span class="badge badge-${m.c}">${tt(m.k, s)}</span>`;
  }
  function kindLabel(k) {
    return k === "manual"
      ? tt("lists.kind_manual",  "manual")
      : tt("lists.kind_shallow", "shallow scan");
  }
  function fmtDate(iso) {
    const d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    // Persian renders as Jalali (Solar Hijri) via the fa-IR locale —
    // browsers default that locale to the Persian calendar.
    const loc = document.body.dataset.lang === "fa" ? "fa-IR" : undefined;
    return d.toLocaleString(loc);
  }

  function renderCard(m, active) {
    const a = document.createElement("a");
    a.className = "list-card";
    a.href = "/list?id=" + encodeURIComponent(m.id);
    const isActive = m.id === active;
    a.innerHTML = `
      <div class="lc-head">
        <div class="lc-title">${escapeHTML(m.name)}</div>
        ${statusBadge(m.status)}
        ${isActive ? `<span class="badge badge-running">${tt("lists.s_active","active")}</span>` : ""}
      </div>
      <div class="lc-meta">
        <span class="lc-kind">${kindLabel(m.kind)}</span>
        ${m.server ? ` · ${escapeHTML(m.server)}` : ""}
        · ${fmtDate(m.updated)}
      </div>
      <div class="lc-counts">
        <span>${tt("lists.col_total","Total")}: <strong>${m.total}</strong></span>
        <span style="color:var(--ok);">${tt("results.ok","OK")}: <strong>${m.ok}</strong></span>
        <span style="color:var(--fail);">${tt("results.fail","Failed")}: <strong>${m.failed}</strong></span>
        ${m.l2_scored ? `<span>${tt("lists.col_l2","Deep-scored")}: <strong>${m.l2_scored}</strong></span>` : ""}
      </div>
    `;
    return a;
  }
  function escapeHTML(s) {
    return String(s||"").replace(/&/g,"&amp;").replace(/</g,"&lt;").replace(/>/g,"&gt;");
  }

  async function refresh() {
    const r = await fetch("/api/lists");
    const j = await r.json();
    const lists = j.lists || [];
    if (lists.length === 0) {
      empty.hidden = false;
      card.hidden  = true;
      return;
    }
    empty.hidden = true;
    card.hidden  = false;
    grid.innerHTML = "";
    lists.forEach(m => grid.appendChild(renderCard(m, j.active)));
  }
  refresh();
  setInterval(refresh, 3000);

  // ---- bulk delete modal ----
  const modal = document.getElementById("bulk-delete-modal");
  const dateEl = document.getElementById("bulk-date");
  document.getElementById("btn-bulk-delete").onclick = () => {
    const d = new Date(); d.setDate(d.getDate() - 7);
    dateEl.valueAsDate = d;
    modal.hidden = false;
  };
  const close = () => { modal.hidden = true; };
  document.getElementById("bulk-close").onclick  = close;
  document.getElementById("bulk-cancel").onclick = close;
  modal.addEventListener("click", e => { if (e.target === modal) close(); });

  document.getElementById("bulk-confirm").onclick = async () => {
    if (!dateEl.value) return;
    const d = new Date(dateEl.value + "T00:00:00Z");
    const r = await fetch("/api/lists?older_than=" + encodeURIComponent(d.toISOString()), { method: "DELETE" });
    if (!r.ok) {
      const j = await r.json().catch(() => ({}));
      showError(j);
      return;
    }
    const j = await r.json();
    flash(tt("lists.deleted_n", "Deleted {n} list(s).").replace("{n}", j.deleted), "ok");
    close();
    refresh();
  };
})();
