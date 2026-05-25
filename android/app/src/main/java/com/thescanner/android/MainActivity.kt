package com.thescanner.android

import android.app.DownloadManager
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.os.Environment
import android.os.Handler
import android.os.Looper
import android.view.View
import android.webkit.JavascriptInterface
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.activity.ComponentActivity
import androidx.activity.result.ActivityResultLauncher
import androidx.activity.result.contract.ActivityResultContracts
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import mobile.App
import mobile.Mobile
import java.io.ByteArrayOutputStream
import java.net.HttpURLConnection
import java.net.InetSocketAddress
import java.net.ServerSocket
import java.net.URL

/**
 * Thin wrapper around the gomobile-built `mobile` library. We pick a port
 * from a fixed range (39000..39099) so the WebView origin — and therefore
 * its localStorage / cookies — stays the same across launches.
 *
 * Random "any free port" binding would change the origin every launch and
 * blow away any per-user state the UI persists client-side.
 */
class MainActivity : ComponentActivity() {

    private lateinit var webView: WebView
    private var goApp: App? = null
    private val handler = Handler(Looper.getMainLooper())

    // WebView hands us a ValueCallback when the page opens <input type="file">.
    // We stash it here until the system file picker returns, then deliver the
    // selected URI(s) back to the WebView. Cleared on cancel / failure.
    private var fileChooserCallback: ValueCallback<Array<Uri>>? = null

    // ActivityResult launcher for the system file picker. Registered in
    // onCreate (mandatory before onStart per the Activity Result API
    // contract). Receives the result and forwards it to the WebView.
    private lateinit var fileChooserLauncher: ActivityResultLauncher<Intent>

    // When the app is launched (or already-running) via an "Open with
    // thescanner" / Share Sheet intent carrying a text file or text blob,
    // we stash the parsed content here. scan.js pulls it on page load via
    // the AndroidImport JavaScript bridge and populates the resolver
    // textarea automatically.
    @Volatile private var pendingImport: String? = null

    /**
     * Tiny @JavascriptInterface exposed to the web UI as `window.AndroidImport`.
     * scan.js calls `consume()` on load — it returns the pending file content
     * (or "" if none) and clears the slot so re-loading the page doesn't
     * re-import. Safe to expose to the loopback page because the only thing
     * it does is hand back data we already collected from a system intent.
     */
    inner class ImportBridge {
        @JavascriptInterface
        fun consume(): String {
            val v = pendingImport
            pendingImport = null
            return v ?: ""
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        // Must be registered before setContentView so the WebView's first
        // file-input click (if any during layout) doesn't race the launcher.
        fileChooserLauncher = registerForActivityResult(
            ActivityResultContracts.StartActivityForResult()
        ) { result ->
            val uris: Array<Uri>? = if (result.resultCode == RESULT_OK) {
                WebChromeClient.FileChooserParams.parseResult(result.resultCode, result.data)
            } else null
            fileChooserCallback?.onReceiveValue(uris)
            fileChooserCallback = null
        }

        setContentView(R.layout.activity_main)

        val rootView = findViewById<View>(android.R.id.content)
        ViewCompat.setOnApplyWindowInsetsListener(rootView) { v, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            val ime  = insets.getInsets(WindowInsetsCompat.Type.ime())
            v.setPadding(0, bars.top, 0, maxOf(bars.bottom, ime.bottom))
            insets
        }
        ViewCompat.requestApplyInsets(rootView)

        webView = findViewById(R.id.webView)
        configureWebView(webView)

        // Capture the launching intent (cold-start case). Must happen
        // BEFORE startGoAndLoad so the URL we navigate to can be set to
        // /scan when there's something to import.
        consumeIncomingIntent(intent)

        startGoAndLoad()
    }

    // Hot-start case: launchMode=singleTask means an "Open with" tap on a
    // text file while the app is already running re-enters MainActivity
    // via onNewIntent instead of onCreate. Capture the new intent and
    // navigate the WebView to /scan to surface the import.
    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        if (!consumeIncomingIntent(intent)) return
        val addr = goApp?.address() ?: return
        webView.loadUrl(addr.trimEnd('/') + "/scan")
    }

    /**
     * Extract a text payload from an incoming intent (file-open or share).
     * On success, stores it in `pendingImport` and returns true so callers
     * can decide whether to navigate. Returns false for non-import intents
     * (the regular LAUNCHER intent path) so we don't disturb the default
     * landing page.
     */
    private fun consumeIncomingIntent(intent: Intent?): Boolean {
        if (intent == null) return false
        val text: String? = when (intent.action) {
            Intent.ACTION_VIEW -> intent.data?.let { readUriContent(it) }
            Intent.ACTION_SEND -> {
                // Share Sheet path. EXTRA_TEXT wins when the source app
                // shared a plain string (e.g. selected text in another
                // app); EXTRA_STREAM is the URI of a shared file.
                val extraText = intent.getStringExtra(Intent.EXTRA_TEXT)
                if (!extraText.isNullOrBlank()) {
                    extraText
                } else {
                    @Suppress("DEPRECATION")
                    val streamUri: Uri? = intent.getParcelableExtra(Intent.EXTRA_STREAM)
                    streamUri?.let { readUriContent(it) }
                }
            }
            else -> null
        }
        if (text.isNullOrEmpty()) return false
        pendingImport = text
        return true
    }

    /**
     * Read a content:// or file:// URI as UTF-8 text, capped at 5 MB.
     * Beyond that the resolver textarea becomes a UI hog (the rest of the
     * file's IPs would still parse, but the editor would lag); the user
     * should split their list.
     */
    private fun readUriContent(uri: Uri): String? {
        return try {
            contentResolver.openInputStream(uri)?.use { input ->
                val maxBytes = 5 * 1024 * 1024
                val baos = ByteArrayOutputStream()
                val buf = ByteArray(64 * 1024)
                var total = 0
                while (true) {
                    val n = input.read(buf)
                    if (n <= 0) break
                    val room = maxBytes - total
                    if (room <= 0) break
                    val take = if (n > room) room else n
                    baos.write(buf, 0, take)
                    total += take
                    if (take < n) break
                }
                baos.toString(Charsets.UTF_8.name())
            }
        } catch (_: Exception) {
            null
        }
    }

    private fun configureWebView(wv: WebView) {
        wv.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            cacheMode         = WebSettings.LOAD_DEFAULT
            allowFileAccess   = false
            allowContentAccess              = false
            allowFileAccessFromFileURLs     = false
            allowUniversalAccessFromFileURLs = false
            mixedContentMode  = WebSettings.MIXED_CONTENT_NEVER_ALLOW
        }
        // Expose the "intent → page" bridge. Safe to add unconditionally:
        // it just hands back the pending import buffer we captured from
        // an Open-with / Share-with intent. The WebView only loads the
        // loopback Go server, so no third-party JS can call it.
        wv.addJavascriptInterface(ImportBridge(), "AndroidImport")
        wv.webViewClient = object : WebViewClient() {
            // Loopback-only; everything else dropped (not handed off).
            override fun shouldOverrideUrlLoading(view: WebView?, url: String?): Boolean {
                if (url == null) return true
                if (url.startsWith("http://127.0.0.1:") || url.startsWith("http://localhost:")) {
                    return false
                }
                return true
            }
        }
        // WebChromeClient unlocks default native dialogs for JS
        // alert/confirm/prompt. Without it the WebView silently
        // returns false from every confirm() call — which is why
        // the "delete this list?" button appeared to do nothing.
        //
        // We also override onShowFileChooser so <input type="file">
        // actually opens the system picker. The default impl returns
        // false and the tap silently disappears.
        wv.webChromeClient = object : WebChromeClient() {
            override fun onShowFileChooser(
                webView: WebView?,
                filePathCallback: ValueCallback<Array<Uri>>?,
                fileChooserParams: FileChooserParams?
            ): Boolean {
                // Replace any in-flight callback (e.g. user double-tapped).
                // Resolving the old one with null tells WebView the previous
                // pick was cancelled, so it doesn't leak.
                fileChooserCallback?.onReceiveValue(null)
                fileChooserCallback = filePathCallback

                // Prefer the intent crafted by WebView (respects accept=""
                // and multiple attributes); fall back to a generic picker
                // if the params are absent.
                val intent = fileChooserParams?.createIntent()
                    ?: Intent(Intent.ACTION_GET_CONTENT).apply {
                        addCategory(Intent.CATEGORY_OPENABLE)
                        type = "*/*"
                    }
                return try {
                    fileChooserLauncher.launch(intent)
                    true
                } catch (_: Exception) {
                    // No picker installed (rare — but e.g. headless test
                    // images). Hand the WebView a null result so it
                    // doesn't sit forever waiting for the callback.
                    fileChooserCallback = null
                    filePathCallback?.onReceiveValue(null)
                    false
                }
            }
        }

        // Wire WebView "download a resource" notifications to the
        // platform DownloadManager, so the result-export buttons
        // actually save files to the Downloads folder instead of
        // showing a blank page.
        wv.setDownloadListener { url, _, contentDisposition, mimeType, _ ->
            try {
                val req = DownloadManager.Request(Uri.parse(url))
                req.setMimeType(mimeType)
                req.addRequestHeader("User-Agent", wv.settings.userAgentString)
                req.setNotificationVisibility(
                    DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED
                )
                val name = guessDownloadName(url, contentDisposition, mimeType)
                req.setDestinationInExternalPublicDir(Environment.DIRECTORY_DOWNLOADS, name)
                (getSystemService(Context.DOWNLOAD_SERVICE) as DownloadManager).enqueue(req)
            } catch (_: Exception) {
                // Best-effort — if DownloadManager is missing (rare /
                // sandboxed), the user can re-run the export from a
                // browser instead.
            }
        }
    }

    private fun guessDownloadName(url: String, cd: String?, mime: String?): String {
        // Parse `filename=` out of Content-Disposition first.
        cd?.let {
            val m = Regex("filename=\"?([^\";]+)\"?").find(it)
            if (m != null) return m.groupValues[1]
        }
        val last = Uri.parse(url).lastPathSegment
        if (!last.isNullOrBlank()) return last
        val ext = when (mime) {
            "text/csv" -> "csv"
            else       -> "txt"
        }
        return "thescanner-export.$ext"
    }

    private fun startGoAndLoad() {
        // pick stable port, then start Go on it
        val port = pickPort()
        savePort(port)

        val app = Mobile.newApp()
        try {
            app.start(filesDir.absolutePath, "127.0.0.1:$port")
            goApp = app
        } catch (e: Exception) {
            // If start failed (e.g. port lost the race), try ":0" — the
            // localStorage will be on a different origin this run, but at
            // least the app boots.
            app.start(filesDir.absolutePath, "127.0.0.1:0")
            goApp = app
        }

        // If we already captured a pending import in onCreate (cold start
        // via "Open with" or Share Sheet), skip the default landing page
        // and go straight to /scan — scan.js will pull the IPs out of
        // window.AndroidImport.consume() on init.
        val base = app.address().trimEnd('/')
        val url = if (pendingImport != null) "$base/scan" else "$base/"
        waitForHealthThenLoad(url)
    }

    /**
     * The HTTP listener is already accepting before app.start() returns,
     * but the runner setup may still be wiring up. Poll a known route
     * briefly before pointing the WebView at it.
     */
    private fun waitForHealthThenLoad(baseUrl: String) {
        Thread {
            val deadline = System.currentTimeMillis() + 5_000
            while (System.currentTimeMillis() < deadline) {
                if (probe(baseUrl)) break
                try { Thread.sleep(80) } catch (_: InterruptedException) { }
            }
            handler.post { webView.loadUrl(baseUrl) }
        }.start()
    }

    private fun probe(baseUrl: String): Boolean {
        return try {
            val u = URL(baseUrl.trimEnd('/') + "/scan")
            val c = u.openConnection() as HttpURLConnection
            c.connectTimeout = 250
            c.readTimeout    = 250
            c.requestMethod  = "GET"
            val code = c.responseCode
            c.disconnect()
            code in 200..399
        } catch (_: Exception) {
            false
        }
    }

    /**
     * Try the saved port first (so the WebView origin survives restarts),
     * then walk the range. Fall back to "ask the kernel" only if every
     * slot is busy — see the class-level comment on why we prefer stable.
     */
    private fun pickPort(): Int {
        val prefs = getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        val saved = prefs.getInt(PREF_PORT, -1)
        if (saved in PORT_RANGE_MIN..PORT_RANGE_MAX && tryBind(saved)) return saved
        for (p in PORT_RANGE_MIN..PORT_RANGE_MAX) {
            if (tryBind(p)) return p
        }
        return ServerSocket().use {
            it.reuseAddress = true
            it.bind(InetSocketAddress("127.0.0.1", 0))
            it.localPort
        }
    }

    private fun savePort(port: Int) {
        getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            .edit().putInt(PREF_PORT, port).apply()
    }

    private fun tryBind(port: Int): Boolean = try {
        ServerSocket().use {
            it.reuseAddress = true
            it.bind(InetSocketAddress("127.0.0.1", port))
        }
        true
    } catch (_: Exception) {
        false
    }

    override fun onDestroy() {
        super.onDestroy()
        goApp?.stop()
        goApp = null
    }

    companion object {
        private const val PREFS = "thescanner"
        private const val PREF_PORT = "ui_port"

        // Reserved port range for thescanner's local UI. Distinct from
        // thefeed's 38000..38099 so the two apps can coexist.
        const val PORT_RANGE_MIN = 39000
        const val PORT_RANGE_MAX = 39099
    }
}
