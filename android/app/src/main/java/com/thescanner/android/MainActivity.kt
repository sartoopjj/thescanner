package com.thescanner.android

import android.content.Context
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.View
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.activity.ComponentActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import mobile.App
import mobile.Mobile
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

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        WindowCompat.setDecorFitsSystemWindows(window, false)
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

        startGoAndLoad()
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

        val url = app.address() // e.g. "http://127.0.0.1:39000/"
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
