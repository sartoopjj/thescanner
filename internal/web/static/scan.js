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

  // If a scan is currently running, surface a banner that links straight
  // to that list — users were creating a second list while the first was
  // still going (which the runner rejects, so they got confused error
  // toasts). Better UX: tell them up-front.
  try {
    const lr = await fetch("/api/lists");
    const lj = await lr.json();
    const running = (lj.lists || []).find(m =>
      m.status === "scanning" || m.status === "deep");
    if (running) {
      const banner = document.createElement("div");
      banner.className = "flash flash-warn show";
      banner.style.position = "static";
      banner.style.marginBottom = "1rem";
      const label = tt("scan.in_progress_banner",
        "A scan is currently running.");
      const linkLabel = tt("scan.open_running", "Open it");
      banner.innerHTML = `${label} `;
      const a = document.createElement("a");
      a.href = "/list?id=" + encodeURIComponent(running.id);
      a.textContent = linkLabel + " → " + (running.name || running.id);
      a.style.color = "inherit";
      a.style.textDecoration = "underline";
      banner.appendChild(a);
      const main = document.querySelector("main");
      if (main && main.firstChild) main.insertBefore(banner, main.firstChild);
    }
  } catch (_) { /* non-fatal — banner is informational only */ }

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

  // Out-of-band IP buffer for huge imports. The DOM textarea chokes
  // around 50k–100k lines (paint + selection cost is brutal), so we
  // keep large imports OUT of it: parse the file into this Set, show
  // a count + preview, and stream-upload at submit time via multipart
  // (no JSON.stringify of a 100 MB string). There is no fixed cap —
  // millions of IPs are an intentional use case for this tool.
  let pendingFile = null;     // File handle from <input type=file>
  let pendingCount = 0;       // approximate IP count for the badge

  // Threshold above which we stop mirroring the file into the textarea
  // and route everything through the out-of-band buffer + multipart
  // upload path. Below this the existing inline experience still works.
  const TEXTAREA_THRESHOLD = 5000;

  document.getElementById("resolver-import").addEventListener("change", async (e) => {
    const file = e.target.files && e.target.files[0];
    if (!file) return;
    try {
      // For very small files we can still parse-and-merge into the
      // textarea so deduping with manually-typed IPs works naturally.
      // For anything bigger we keep the parsed IPs out of the DOM and
      // upload the original file directly via multipart at submit.
      if (file.size <= 256 * 1024) {
        const text  = await file.text();
        const lines = extractResolvers(text);
        const added = appendLines(lines);
        if (added === 0 && lines.length === 0) {
          countEl.textContent = tt("scan.no_ips_in_file", "no IPs found in file");
          setTimeout(recount, 2500);
        }
        return;
      }

      // Big import: count IPs without dumping into the DOM. We still
      // need a rough count to display; iterate the regex over the text
      // once, but discard the string immediately after.
      const text = await file.text();
      const lines = extractResolvers(text);
      pendingFile  = file;
      pendingCount = lines.length;
      // Drop the parsed array — we only kept it for the count.
      // The actual upload reads `pendingFile` directly via multipart.
      resolversEl.value = "";
      const note = tt("scan.imported_pending",
        "{n} IPs loaded from {name} — they'll upload when you press Start.")
        .replace("{n}", pendingCount.toLocaleString())
        .replace("{name}", file.name);
      resolversEl.placeholder = note;
      countEl.textContent = `${pendingCount.toLocaleString()}`;
    } catch (err) {
      countEl.textContent = "import error: " + err;
      setTimeout(recount, 3000);
    } finally {
      e.target.value = "";
    }
  });

  // Sample lists dropdown — embedded resolver sets shipped in the
  // binary. Selecting one fetches the file and feeds it through the
  // same pending-file path so big samples don't bloat the textarea.
  (async function loadSamples() {
    const sel = document.getElementById("sample-select");
    if (!sel) return;
    try {
      const r = await fetch("/api/samples");
      const j = await r.json();
      const samples = j.samples || [];
      if (samples.length === 0) { sel.hidden = true; return; }
      const placeholder = document.createElement("option");
      placeholder.value = "";
      placeholder.textContent = tt("scan.sample_picker", "Use sample list…");
      sel.appendChild(placeholder);
      samples.forEach(s => {
        const opt = document.createElement("option");
        opt.value = s.id;
        opt.textContent = s.label;
        sel.appendChild(opt);
      });
      sel.addEventListener("change", async () => {
        const id = sel.value;
        if (!id) return;
        try {
          const r = await fetch("/api/samples/" + encodeURIComponent(id));
          if (!r.ok) throw new Error("fetch failed");
          const blob = await r.blob();
          const file = new File([blob], id, { type: "text/plain" });
          const text = await file.text();
          const lines = extractResolvers(text);
          if (file.size <= 256 * 1024) {
            resolversEl.value = "";
            appendLines(lines);
          } else {
            pendingFile  = file;
            pendingCount = lines.length;
            resolversEl.value = "";
            resolversEl.placeholder = tt("scan.imported_pending",
              "{n} IPs loaded from {name} — they'll upload when you press Start.")
              .replace("{n}", pendingCount.toLocaleString())
              .replace("{name}", file.name);
            countEl.textContent = `${pendingCount.toLocaleString()}`;
          }
        } catch (err) {
          countEl.textContent = "sample load failed: " + err;
          setTimeout(recount, 3000);
        } finally {
          sel.value = "";
        }
      });
    } catch (_) { sel.hidden = true; }
  })();

  resolversEl.addEventListener("input", () => {
    if (pendingFile && resolversEl.value.trim().length > 0) {
      pendingFile = null;
      pendingCount = 0;
      resolversEl.placeholder = "";
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

  let submitting = false;
  async function create(autoStart) {
    if (submitting) return;
    submitting = true;
    const isManual = kindSel.value === "manual";
    const autoStartEffective = autoStart && !isManual;

    const btnStart = document.getElementById("btn-create");
    const btnSave  = document.getElementById("btn-create-only");
    const origStart = btnStart.textContent;
    const origSave  = btnSave.textContent;
    btnStart.disabled = true;
    btnSave.disabled  = true;
    btnStart.textContent = tt("scan.preparing", "Preparing list…");

    let r;
    try {
      if (pendingFile) {
        const fd = new FormData();
        fd.append("kind",       kindSel.value);
        fd.append("name",       nameEl.value.trim());
        fd.append("server",     isManual ? "" : sel.value);
        fd.append("auto_start", autoStartEffective ? "1" : "");
        fd.append("resolvers_file", pendingFile, pendingFile.name);
        r = await fetch("/api/lists", { method: "POST", body: fd });
      } else {
        const body = {
          kind:       kindSel.value,
          name:       nameEl.value.trim(),
          server:     isManual ? "" : sel.value,
          resolvers:  resolversEl.value,
          auto_start: autoStartEffective,
        };
        r = await fetch("/api/lists", {
          method:  "POST",
          headers: { "content-type": "application/json" },
          body:    JSON.stringify(body)
        });
      }
      if (!r.ok) {
        const j = await r.json().catch(() => ({}));
        showError(j);
        return;
      }
      const meta = await r.json();
      location.href = "/list?id=" + encodeURIComponent(meta.id);
    } finally {
      submitting = false;
      btnStart.disabled = false;
      btnSave.disabled  = false;
      btnStart.textContent = origStart;
      btnSave.textContent  = origSave;
    }
  }
  document.getElementById("btn-create").addEventListener("click", () => create(true));
  document.getElementById("btn-create-only").addEventListener("click", () => create(false));
})();
