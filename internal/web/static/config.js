(async function () {
  const form = document.getElementById("config-form");
  const serverList = document.getElementById("server-list");
  const msg = document.getElementById("config-saved-msg");
  const saveBar = document.getElementById("save-bar");
  const saveBtn = document.getElementById("save-btn");
  const inlineSaveBtn = document.getElementById("inline-save");

  // ---- info-button modal (bilingual help) ----------------------------
  const help = await (await fetch("/api/help")).json();
  const modal = document.getElementById("help-modal");
  const modalTitle = document.getElementById("help-title");
  const modalEn = document.getElementById("help-en-text");
  const modalFa = document.getElementById("help-fa-text");
  function showHelp(key) {
    const entry = help[key];
    if (!entry) return;
    modalTitle.textContent = key;
    modalEn.textContent = entry.en || "";
    modalFa.textContent = entry.fa || "";
    modal.hidden = false;
  }
  modal.querySelector(".help-close").onclick = () => { modal.hidden = true; };
  modal.addEventListener("click", e => { if (e.target === modal) modal.hidden = true; });
  document.addEventListener("keydown", e => {
    if (e.key === "Escape") { modal.hidden = true; uriModal.hidden = true; }
  });
  document.body.addEventListener("click", e => {
    const btn = e.target.closest(".i");
    if (btn) { e.preventDefault(); showHelp(btn.dataset.help); }
  });

  // ---- dirty-state tracking ------------------------------------------
  function markDirty() {
    saveBar.classList.add("is-dirty");
    msg.textContent = "";
  }

  // ---- server-URI codec ----------------------------------------------
  // Format:
  //   thescanner://server?name=NAME&token=TOK&domains=D1,D2
  function serverToURI(s) {
    const enc = encodeURIComponent;
    const doms = (s.domains || []).map(d => d.trim()).filter(Boolean).join(",");
    const parts = [];
    if (s.name)  parts.push("name="  + enc(s.name));
    if (s.token) parts.push("token=" + enc(s.token));
    if (doms)    parts.push("domains=" + enc(doms));
    return "thescanner://server" + (parts.length ? "?" + parts.join("&") : "");
  }
  function uriToServer(uri) {
    if (!uri) return null;
    const m = /^thescanner:\/\/[^?#]*(?:\?([^#]*))?/i.exec(uri.trim());
    if (!m) return null;
    const params = new URLSearchParams(m[1] || "");
    const domains = (params.get("domains") || "")
      .split(",").map(x => x.trim()).filter(Boolean);
    return {
      name:    (params.get("name")  || "").trim(),
      token:   (params.get("token") || "").trim(),
      domains: domains
    };
  }
  async function copyText(text) {
    try {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        await navigator.clipboard.writeText(text); return true;
      }
    } catch (_) {}
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed"; ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    let ok = false;
    try { ok = document.execCommand("copy"); } catch (_) {}
    document.body.removeChild(ta);
    return ok;
  }
  function maskToken(t) {
    if (!t) return "—";
    if (t.length <= 8) return "•".repeat(t.length);
    return t.slice(0, 3) + "•••" + t.slice(-3);
  }

  // ---- escape helpers ------------------------------------------------
  function escapeAttr(s) {
    return String(s).replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  }
  function escapeText(s) {
    return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  }
  function tt(key, fallback) { return (window.tt ? window.tt(key, fallback) : fallback); }

  // ---- server rows (compact line + expandable editor) ----------------
  function readRow(row) {
    const rawDoms = row.querySelector(".s-domains").value || "";
    return {
      name:    row.querySelector(".s-name").value.trim(),
      token:   row.querySelector(".s-token").value.trim(),
      domains: rawDoms.split(/\r?\n/).map(s => s.trim()).filter(Boolean)
    };
  }
  function refreshSummary(row) {
    const s = readRow(row);
    row.querySelector(".sum-name").textContent     = s.name || "(unnamed)";
    const doms = s.domains;
    const sumDoms = doms.length === 0
      ? "—"
      : doms.length === 1
        ? doms[0]
        : `${doms[0]} +${doms.length - 1}`;
    row.querySelector(".sum-domains").textContent  = sumDoms;
    row.querySelector(".sum-token").textContent    = maskToken(s.token);
  }
  function serverRow(s) {
    const div = document.createElement("div");
    div.className = "server-row";
    div.dataset.expanded = s ? "false" : "true"; // new rows start open
    const domains = (s?.domains || []).join("\n");
    div.innerHTML = `
      <button type="button" class="server-summary">
        <span class="sum-chevron">▸</span>
        <span class="sum-name"></span>
        <span class="sum-domains muted"></span>
        <span class="sum-token muted"></span>
      </button>
      <div class="server-editor">
        <div class="field">
          <div class="field-head"><label data-i18n="field.server.name">Name</label></div>
          <input class="s-name" type="text" value="${escapeAttr(s?.name || "")}">
        </div>
        <div class="field">
          <div class="field-head">
            <label data-i18n="field.server.domains">Domains (one per line)</label>
            <button type="button" class="i" data-help="servers.domains">i</button>
          </div>
          <textarea class="s-domains" rows="3">${escapeText(domains)}</textarea>
        </div>
        <div class="field">
          <div class="field-head">
            <label data-i18n="field.server.token">Token</label>
            <button type="button" class="i" data-help="servers.token">i</button>
          </div>
          <input class="s-token" type="text" value="${escapeAttr(s?.token || "")}">
        </div>
        <div class="row">
          <button type="button" class="ghost s-copy" data-i18n="config.copy_uri">Copy URI</button>
          <button type="button" class="danger s-del" data-i18n="config.remove">Remove</button>
          <span class="ok-msg s-copy-msg" style="font-size:.8rem;"></span>
        </div>
      </div>
    `;
    // Wire row controls.
    div.querySelector(".s-del").onclick = () => { div.remove(); markDirty(); };
    div.querySelector(".server-summary").addEventListener("click", e => {
      e.preventDefault();
      div.dataset.expanded = div.dataset.expanded === "true" ? "false" : "true";
    });
    div.querySelector(".s-copy").onclick = async () => {
      const uri = serverToURI(readRow(div));
      const ok  = await copyText(uri);
      const m = div.querySelector(".s-copy-msg");
      m.textContent = ok ? tt("config.uri_copied", "Copied.") : uri;
      setTimeout(() => { m.textContent = ""; }, 2500);
    };
    // Live-update the summary line as the user edits.
    ["s-name", "s-domains", "s-token"].forEach(cls => {
      div.querySelector("." + cls).addEventListener("input", () => refreshSummary(div));
    });
    refreshSummary(div);
    // Apply i18n inside the freshly-inserted row.
    div.querySelectorAll("[data-i18n]").forEach(el => {
      const k = el.getAttribute("data-i18n");
      const v = tt(k, null);
      if (v) el.textContent = v;
    });
    // Wrap any number inputs (none in server-row currently, but the
    // helper is cheap and idempotent).
    wrapNumberInputs(div);
    return div;
  }

  document.getElementById("add-server").onclick = () => {
    serverList.appendChild(serverRow(null));
    markDirty();
  };

  // ---- URI import modal ----------------------------------------------
  const uriModal     = document.getElementById("uri-import-modal");
  const uriInput     = document.getElementById("uri-import-input");
  const uriErrorEl   = document.getElementById("uri-import-error");
  document.getElementById("import-uri").onclick = () => {
    uriInput.value = "";
    uriErrorEl.textContent = "";
    uriModal.hidden = false;
    setTimeout(() => uriInput.focus(), 30);
  };
  document.getElementById("uri-import-cancel").onclick   = () => { uriModal.hidden = true; };
  document.getElementById("uri-import-cancel-x").onclick = () => { uriModal.hidden = true; };
  uriModal.addEventListener("click", e => { if (e.target === uriModal) uriModal.hidden = true; });
  document.getElementById("uri-import-ok").onclick = () => {
    const parsed = uriToServer(uriInput.value);
    if (!parsed || (!parsed.name && !parsed.token && parsed.domains.length === 0)) {
      uriErrorEl.textContent = tt("config.import_uri_bad", "Invalid URI — expected thescanner://...");
      return;
    }
    serverList.appendChild(serverRow(parsed));
    markDirty();
    uriModal.hidden = true;
  };

  // ---- custom +/- buttons on number inputs ---------------------------
  function wrapNumberInputs(scope) {
    scope.querySelectorAll('input[type=number]').forEach(inp => {
      if (inp.parentElement && inp.parentElement.classList.contains("num-input")) return;
      const wrap = document.createElement("div");
      wrap.className = "num-input";
      inp.parentNode.insertBefore(wrap, inp);
      const dec = document.createElement("button");
      dec.type = "button"; dec.className = "num-btn num-dec"; dec.textContent = "−";
      const inc = document.createElement("button");
      inc.type = "button"; inc.className = "num-btn num-inc"; inc.textContent = "+";
      wrap.appendChild(dec); wrap.appendChild(inp); wrap.appendChild(inc);
      const step = (dir) => {
        if (dir > 0) inp.stepUp(); else inp.stepDown();
        inp.dispatchEvent(new Event("input", { bubbles: true }));
      };
      dec.addEventListener("click", () => step(-1));
      inc.addEventListener("click", () => step(+1));
    });
  }

  // ---- form mapping ---------------------------------------------------
  function setVal(name, v) {
    const el = form.querySelector(`[name="${name}"]`);
    if (!el) return;
    if (el.type === "checkbox") el.checked = !!v;
    else el.value = (v == null ? "" : v);
  }

  function fillForm(d) {
    serverList.innerHTML = "";
    (d.servers || []).forEach(s => {
      const row = serverRow(s);
      row.dataset.expanded = "false"; // existing servers start collapsed
      serverList.appendChild(row);
    });
    const sc = d.scan || {};
    setVal("scan.min_query", sc.min_query);
    setVal("scan.max_query", sc.max_query);
    setVal("scan.min_response", sc.min_response);
    setVal("scan.max_response", sc.max_response);
    setVal("scan.parallel", sc.parallel);
    setVal("scan.duplicate", sc.duplicate);
    setVal("scan.timeout_seconds", sc.timeout_seconds);
    setVal("scan.retries", sc.retries);
    setVal("scan.edns0", sc.edns0);
    setVal("scan.noise_enabled", sc.noise_enabled);
    setVal("scan.noise_every",   sc.noise_every);
    setVal("scan.subnet_expand", sc.subnet_expand);
    setVal("scan.subnet_mask", sc.subnet_mask);
    const l2 = d.level2 || {};
    setVal("level2.queries_per_resolver", l2.queries_per_resolver);
    setVal("level2.parallel", l2.parallel);
    const ui = d.ui || {};
    setVal("ui.listen", ui.listen);
    setVal("ui.language", ui.language);
    setVal("ui.theme", ui.theme || "auto");
  }

  function readForm() {
    const out = { servers: [], scan: {}, level2: {}, ui: {} };
    serverList.querySelectorAll(".server-row").forEach(row => {
      const s = readRow(row);
      if (!s.name && !s.token && s.domains.length === 0) return;
      out.servers.push(s);
    });
    function n(name) { const v = form.querySelector(`[name="${name}"]`).value; return v === "" ? 0 : Number(v); }
    function b(name) { return form.querySelector(`[name="${name}"]`).checked; }
    out.scan = {
      min_query: n("scan.min_query"),
      max_query: n("scan.max_query"),
      min_response: n("scan.min_response"),
      max_response: n("scan.max_response"),
      parallel: n("scan.parallel"),
      duplicate: n("scan.duplicate"),
      timeout_seconds: n("scan.timeout_seconds"),
      retries: n("scan.retries"),
      edns0: b("scan.edns0"),
      noise_enabled: b("scan.noise_enabled"),
      noise_every:   n("scan.noise_every"),
      subnet_expand: b("scan.subnet_expand"),
      subnet_mask: n("scan.subnet_mask")
    };
    out.level2 = {
      queries_per_resolver: n("level2.queries_per_resolver"),
      parallel: n("level2.parallel")
    };
    out.ui = {
      listen:   form.querySelector('[name="ui.listen"]').value,
      language: form.querySelector('[name="ui.language"]').value,
      theme:    form.querySelector('[name="ui.theme"]').value
    };
    return out;
  }

  // Form-wide change listener marks dirty.
  form.addEventListener("input", markDirty);
  form.addEventListener("change", markDirty);

  // Without EDNS0, the wire cap is 512 bytes — clamp max_response live
  // so the user can't accidentally set a value that won't fit.
  function reflectEDNS0Cap() {
    const ednsEl = form.querySelector('[name="scan.edns0"]');
    const maxEl  = form.querySelector('[name="scan.max_response"]');
    if (!ednsEl || !maxEl) return;
    if (ednsEl.checked) {
      maxEl.max = 4096;
    } else {
      maxEl.max = 480;
      if (Number(maxEl.value) > 480) {
        maxEl.value = 480;
        markDirty();
      }
    }
  }
  form.addEventListener("change", reflectEDNS0Cap);

  // ---- load + save ----------------------------------------------------
  const r = await fetch("/api/config");
  fillForm(await r.json());
  // Wrap all top-level number fields (server rows are wrapped when built).
  wrapNumberInputs(document);
  reflectEDNS0Cap();
  saveBar.classList.remove("is-dirty");

  async function save() {
    const body = readForm();
    const r = await fetch("/api/config", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body)
    });
    if (r.ok) {
      saveBar.classList.remove("is-dirty");
      // Keep the bar visible for ~2.5s after save so "Saved." can show.
      saveBar.classList.add("show");
      msg.style.color = "var(--ok)";
      msg.textContent = tt("config.saved", "Saved.");
      setTimeout(() => {
        msg.textContent = "";
        saveBar.classList.remove("show");
      }, 2500);
      // Apply theme immediately + persist for the early-boot script.
      try {
        const t = body.ui.theme || "auto";
        if (t === "light" || t === "dark") {
          document.documentElement.dataset.theme = t;
          localStorage.setItem("theme", t);
        } else {
          delete document.documentElement.dataset.theme;
          localStorage.removeItem("theme");
        }
        // Keep the mobile browser-chrome bar in sync with the new theme.
        if (typeof syncMobileChromeColor === "function") syncMobileChromeColor();
      } catch (e) {}
      const newLang = body.ui.language;
      if (newLang !== document.body.dataset.lang) {
        location.reload();
      }
    } else {
      const j = await r.json().catch(() => ({}));
      msg.style.color = "var(--fail)";
      msg.textContent = "Error: " + (j.error || r.statusText);
    }
  }
  saveBtn.addEventListener("click", save);
  if (inlineSaveBtn) inlineSaveBtn.addEventListener("click", save);
  form.addEventListener("submit", (e) => { e.preventDefault(); save(); });

  // Warn on unload if dirty.
  window.addEventListener("beforeunload", (e) => {
    if (saveBar.classList.contains("is-dirty")) {
      e.preventDefault();
      e.returnValue = "";
    }
  });
})();
