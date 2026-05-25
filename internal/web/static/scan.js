(async function () {
  const tt = (k, fb) => (window.tt ? window.tt(k, fb) : fb);

  const cfgR  = await fetch("/api/config");
  const cfg   = await cfgR.json();
  const empty = document.getElementById("empty-servers");
  const card  = document.getElementById("scan-card");
  const sel   = document.getElementById("server-select");
  const kindSel = document.getElementById("scan-kind");
  const serverField = document.getElementById("server-field");
  const resolversEl = document.getElementById("resolvers");
  const countEl     = document.getElementById("resolver-count");
  const nameEl      = document.getElementById("scan-name");

  const servers = cfg.servers || [];

  // Preselect manual kind via ?kind=manual.
  const qs = new URLSearchParams(location.search);
  if (qs.get("kind") === "manual") kindSel.value = "manual";

  function applyKind() {
    const isManual = kindSel.value === "manual";
    serverField.hidden = isManual;
    document.getElementById("btn-create").textContent =
      isManual ? tt("scan.create_manual", "Create list")
               : tt("scan.start", "Start");
    document.getElementById("btn-create-only").hidden = isManual;
  }
  kindSel.addEventListener("change", applyKind);

  if (servers.length === 0 && kindSel.value !== "manual") {
    empty.hidden = false;
    return; // a shallow scan needs a server
  }
  card.hidden = false;

  servers.forEach(s => {
    const opt = document.createElement("option");
    opt.value = s.name;
    const doms = (s.domains || []).join(", ");
    opt.textContent = doms ? `${s.name} (${doms})` : s.name;
    sel.appendChild(opt);
  });
  applyKind();

  function recount() {
    const lines = (resolversEl.value || "").split(/\r?\n/).filter(s => {
      const t = s.trim(); return t && !t.startsWith("#");
    });
    countEl.textContent = lines.length ? `${lines.length}` : "";
  }
  resolversEl.addEventListener("input", recount);
  recount();

  // Open-with / Share Sheet auto-import:
  // The Android wrapper exposes window.AndroidImport.consume() which
  // returns the text content of a file the user picked via "Open with
  // thescanner" (or shared via Share Sheet), and clears the slot on
  // first read so refreshing the page doesn't re-import.
  // (iOS will get an equivalent window.IOSImport later — same shape.)
  try {
    const bridge = window.AndroidImport || window.IOSImport;
    const payload = bridge && typeof bridge.consume === "function"
      ? bridge.consume()
      : "";
    if (payload) {
      const lines = extractResolvers(payload);
      const added = appendLines(lines);
      countEl.textContent = added > 0
        ? `+${added} ` + tt("scan.imported_from_file", "imported from file")
        : tt("scan.no_ips_in_file", "no IPs found in file");
      // Keep the imported-from-file hint visible for a moment, then
      // resume the normal "N resolvers" display.
      setTimeout(recount, 3500);
    }
  } catch (_) { /* bridge missing on desktop / iOS — ignore */ }

  function extractResolvers(text) {
    if (!text) return [];
    const re = /\b(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})(?:\/(\d{1,2}))?\b/g;
    const seen = new Set(); const out = []; let m;
    while ((m = re.exec(text)) !== null) {
      const a=+m[1], b=+m[2], c=+m[3], d=+m[4];
      if (a>255||b>255||c>255||d>255) continue;
      let ip = `${a}.${b}.${c}.${d}`;
      if (m[5] !== undefined) {
        const cidr = +m[5];
        if (cidr<0||cidr>32) continue;
        ip += "/" + cidr;
      }
      if (!seen.has(ip)) { seen.add(ip); out.push(ip); }
    }
    return out;
  }
  function appendLines(lines) {
    if (!lines.length) return 0;
    const have = new Set(extractResolvers(resolversEl.value));
    const fresh = lines.filter(x => !have.has(x));
    if (!fresh.length) return 0;
    const existing = resolversEl.value.replace(/\s*$/, "");
    resolversEl.value = existing ? existing + "\n" + fresh.join("\n") : fresh.join("\n");
    recount();
    return fresh.length;
  }

  document.getElementById("resolver-import").addEventListener("change", async (e) => {
    const file = e.target.files && e.target.files[0];
    if (!file) return;
    try {
      const text  = await file.text();
      const lines = extractResolvers(text);
      const added = appendLines(lines);
      if (added === 0 && lines.length === 0) {
        countEl.textContent = tt("scan.no_ips_in_file", "no IPs found in file");
        setTimeout(recount, 2500);
      }
    } catch (err) {
      countEl.textContent = "import error: " + err;
      setTimeout(recount, 3000);
    } finally {
      e.target.value = "";
    }
  });

  document.getElementById("btn-export-resolvers").addEventListener("click", () => {
    const blob = new Blob([resolversEl.value || ""], { type: "text/plain;charset=utf-8" });
    const url  = URL.createObjectURL(blob);
    const a    = document.createElement("a");
    a.href = url; a.download = "resolvers.txt";
    document.body.appendChild(a); a.click(); a.remove();
    URL.revokeObjectURL(url);
  });
  document.getElementById("btn-clear-resolvers").addEventListener("click", () => {
    if (!resolversEl.value) return;
    const lines = extractResolvers(resolversEl.value).length;
    const msg = tt("scan.confirm_clear", "Clear all {n} resolvers?").replace("{n}", String(lines || ""));
    if (lines > 0 && !confirm(msg)) return;
    resolversEl.value = "";
    recount();
  });

  async function create(autoStart) {
    const body = {
      kind:      kindSel.value,
      name:      nameEl.value.trim(),
      server:    kindSel.value === "shallow" ? sel.value : "",
      resolvers: resolversEl.value,
      auto_start: autoStart && kindSel.value === "shallow",
    };
    const r = await fetch("/api/lists", {
      method:  "POST",
      headers: { "content-type": "application/json" },
      body:    JSON.stringify(body)
    });
    if (!r.ok) {
      const j = await r.json().catch(() => ({}));
      showError(j);
      return;
    }
    const meta = await r.json();
    location.href = "/list?id=" + encodeURIComponent(meta.id);
  }
  document.getElementById("btn-create").addEventListener("click", () => create(true));
  document.getElementById("btn-create-only").addEventListener("click", () => create(false));
})();
