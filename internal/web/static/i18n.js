(function () {
  // The bundle is inlined by the server template (layout.html) so it's
  // already on window before this script runs. Falls back to fetch
  // only if something is wildly wrong (no template render).
  const bundle = window.__i18nBundle || {};
  const chosen = window.__i18nChosen !== false;

  function apply() {
    document.querySelectorAll("[data-i18n]").forEach(el => {
      const k = el.getAttribute("data-i18n");
      if (bundle[k]) el.textContent = bundle[k];
    });
    document.querySelectorAll("[data-i18n-placeholder]").forEach(el => {
      const k = el.getAttribute("data-i18n-placeholder");
      if (bundle[k]) el.setAttribute("placeholder", bundle[k]);
    });
    const infoLabel = bundle["common.info"] || "info";
    document.querySelectorAll("button.i").forEach(btn => {
      if (!btn.dataset.i18n) btn.textContent = infoLabel;
    });
    document.documentElement.classList.remove("i18n-pending");
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", apply, { once: true });
  } else {
    apply();
  }

  // tt() may have been declared inline already; keep this assignment as
  // a no-op compatibility shim in case anyone calls it directly here.
  window.tt = window.tt || function (k, fb) { return bundle[k] || fb || k; };

  // iOS Safari/WebView still triggers double-tap zoom on some
  // builds even with the viewport meta + CSS touch-action hints.
  // Catch a second tap inside 300 ms and preventDefault it. pinch-
  // zoom (two-finger) isn't affected — only the gesture that
  // implicitly translates "tap, tap" into "zoom in".
  (function blockDoubleTapZoom() {
    let lastTouchEnd = 0;
    document.addEventListener("touchend", (e) => {
      const now = Date.now();
      if (now - lastTouchEnd <= 300) e.preventDefault();
      lastTouchEnd = now;
    }, { passive: false });
    // Same defence against the synthesised gesturestart event Safari
    // fires when it's about to zoom on a tap target. Cheap, idempotent.
    ["gesturestart", "gesturechange", "gestureend"].forEach((ev) => {
      document.addEventListener(ev, (e) => e.preventDefault(), { passive: false });
    });
  })();

  // Mobile browser chrome (URL bar + system UI) follows the page bg.
  syncMobileChromeColor();
  if (window.matchMedia) {
    window.matchMedia("(prefers-color-scheme: dark)")
      .addEventListener?.("change", syncMobileChromeColor);
  }

  // ---- first-run language picker -----------------------------------
  if (!chosen) {
    showLanguagePicker();
  }

  // ---- "newer version available" banner ----------------------------
  checkLatestVersion();
})();

async function checkLatestVersion() {
  try {
    const r = await fetch("/api/version");
    if (!r.ok) return;
    const v = await r.json();
    if (v.up_to_date || !v.latest) return;
    const msg = (window.tt || ((_,f)=>f))(
      "common.update_available",
      "Version " + v.latest + " is available. You're on " + v.current + "."
    );
    const bar = document.createElement("div");
    bar.className = "update-bar";
    bar.innerHTML = `<span></span>
      <a target="_blank" rel="noopener" href="${v.url}"></a>
      <button type="button" aria-label="dismiss">×</button>`;
    bar.querySelector("span").textContent = msg
      .replace("{latest}", v.latest).replace("{current}", v.current);
    bar.querySelector("a").textContent =
      (window.tt || ((_,f)=>f))("common.download", "Download");
    bar.querySelector("a").href = v.url;
    bar.querySelector("button").onclick = () => bar.remove();
    document.body.prepend(bar);
  } catch (_) {}
}

// showError replaces alert(j.error || r.statusText). Accepts the
// JSON body the API returned. Resolution order:
//   1. specific error_code (other than "unknown") → tt("err."+code)
//   2. raw j.error from the server (the actual diagnostic)
//   3. caller-supplied fallback
//   4. generic "err.unknown" line
// Step 1 used to fire for error_code="unknown" too, which made every
// uncategorized server error surface as a meaningless "Something went
// wrong" — even when j.error carried the real cause. Now an "unknown"
// code falls through to step 2 so users see what actually broke.
function showError(j, fallback) {
  let msg;
  if (j && j.error_code && j.error_code !== "unknown") {
    msg = (window.tt || ((_, f) => f))("err." + j.error_code, j.error || "");
  } else if (j && j.error) {
    msg = j.error;
  } else if (fallback) {
    msg = fallback;
  } else {
    msg = (window.tt || ((_, f) => f))("err.unknown", "Something went wrong.");
  }
  flash(msg, "fail");
}

// flash shows a top-of-page banner with the message. Auto-dismisses
// after 6 s (configurable). Use kind="ok" for success, "fail" for
// errors, "warn" for restart-needed style.
function flash(text, kind) {
  let el = document.getElementById("flash");
  if (!el) {
    el = document.createElement("div");
    el.id = "flash";
    document.body.appendChild(el);
  }
  el.className = "flash flash-" + (kind || "fail") + " show";
  el.textContent = text;
  clearTimeout(flash._t);
  flash._t = setTimeout(() => { el.classList.remove("show"); }, 6000);
}
window.showError = showError;
window.flash = flash;

// Sync <meta theme-color> with the resolved --bg so the mobile
// browser chrome / system UI follows the active theme.
function syncMobileChromeColor() {
  try {
    const bg = getComputedStyle(document.documentElement).getPropertyValue("--bg").trim();
    if (!bg) return;
    let m = document.querySelector('meta[name="theme-color"]');
    if (!m) {
      m = document.createElement("meta");
      m.setAttribute("name", "theme-color");
      document.head.appendChild(m);
    }
    m.setAttribute("content", bg);
  } catch (_) {}
}

function showLanguagePicker() {
  const overlay = document.createElement("div");
  overlay.className = "lang-picker-overlay";
  overlay.setAttribute("role", "dialog");
  overlay.setAttribute("aria-modal", "true");
  overlay.innerHTML = `
    <div class="lang-picker-box">
      <h1 class="lp-en">Choose your language</h1>
      <h1 class="lp-fa" dir="rtl">یک زبان انتخاب کنید</h1>
      <div class="lang-picker-buttons">
        <button type="button" data-lang="en">English</button>
        <button type="button" data-lang="fa" dir="rtl">فارسی</button>
      </div>
      <div class="lang-picker-err" id="lp-err"></div>
    </div>
  `;
  document.body.appendChild(overlay);
  document.documentElement.style.overflow = "hidden";

  overlay.addEventListener("click", async (e) => {
    const btn = e.target.closest("[data-lang]");
    if (!btn) return;
    btn.disabled = true;
    overlay.querySelectorAll("[data-lang]").forEach(b => b.disabled = true);
    const errEl = overlay.querySelector("#lp-err");
    errEl.textContent = "";
    try {
      // Fetch current config, set just the language, post it back.
      const r   = await fetch("/api/config");
      const cfg = await r.json();
      cfg.ui = cfg.ui || {};
      cfg.ui.language = btn.dataset.lang;
      const post = await fetch("/api/config", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify(cfg)
      });
      if (post.ok) {
        location.reload();
      } else {
        const j = await post.json().catch(() => ({}));
        errEl.textContent = "Save failed: " + (j.error || post.statusText);
        overlay.querySelectorAll("[data-lang]").forEach(b => b.disabled = false);
      }
    } catch (err) {
      errEl.textContent = String(err);
      overlay.querySelectorAll("[data-lang]").forEach(b => b.disabled = false);
    }
  });
}
