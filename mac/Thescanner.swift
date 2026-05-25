// Cocoa launcher for the macOS .app bundle.
//
// thescanner-client is a Go HTTP server with no native UI — it
// listens on 127.0.0.1:8080 and the user interacts via their browser.
// Shipping the Go binary raw as CFBundleExecutable means there's no
// NSApplication event loop, so macOS bounces the Dock icon forever
// (treating the app as "still launching") and never paints the
// running-dot under the icon. Cmd+Q does nothing, the user can't tell
// whether the server is up, and reopening it from Finder spawns a
// duplicate.
//
// This launcher fixes it:
//   1. Runs a real NSApplication so macOS sees a proper app.
//   2. Spawns thescanner-client as a child from inside the same bundle
//      (Contents/MacOS/thescanner-client).
//   3. Menu-bar status item with Open / Quit affordances + Dock-icon
//      reopen handling, so the user can reopen the browser tab after
//      closing it.
//   4. SIGTERMs the child on Cmd+Q so the Go server's signal handler
//      runs its shutdown (flushes stats.json, closes lists cleanly).
//
// Build: see Makefile target mac-app. swiftc is compiled per-arch
// (x86_64 + arm64) and lipo'd into a universal binary placed at
// Contents/MacOS/Thescanner.

import Cocoa
import Darwin // kill(2), SIGKILL fallback after SIGTERM in applicationWillTerminate

final class AppDelegate: NSObject, NSApplicationDelegate {
    private var child: Process?
    private var statusItem: NSStatusItem?
    // Matches thescanner-client's default -listen flag below.
    private let port = 8080

    func applicationDidFinishLaunching(_ notification: Notification) {
        let bundleDir = Bundle.main.bundlePath + "/Contents/MacOS"
        let binary = bundleDir + "/thescanner-client"

        // Stable per-user data dir. Finder launches the app with cwd=/,
        // so thescanner-client's default -data-dir would otherwise land
        // wherever the Go default points (filesystem root).
        let dataDir = NSHomeDirectory() + "/Library/Application Support/Thescanner"
        try? FileManager.default.createDirectory(
            atPath: dataDir, withIntermediateDirectories: true
        )

        // Funnel child stdout+stderr into a log file — Finder discards
        // the parent's standard streams, so without this any crash in
        // thescanner-client is invisible to the user.
        let logURL = URL(fileURLWithPath: dataDir).appendingPathComponent("launcher.log")
        if !FileManager.default.fileExists(atPath: logURL.path) {
            FileManager.default.createFile(atPath: logURL.path, contents: nil)
        }
        let logHandle = try? FileHandle(forWritingTo: logURL)
        logHandle?.seekToEndOfFile()

        let task = Process()
        task.executableURL = URL(fileURLWithPath: binary)
        task.arguments = [
            "-data-dir", dataDir,
            "-listen", "127.0.0.1:\(port)",
        ]
        if let handle = logHandle {
            task.standardOutput = handle
            task.standardError = handle
        }
        // Child exited → quit the launcher too, otherwise the user
        // sees a Dock icon with no server behind it. terminationHandler
        // runs on a background thread, so hop to main before touching
        // NSApp.
        task.terminationHandler = { _ in
            DispatchQueue.main.async {
                NSApp.terminate(nil)
            }
        }

        do {
            try task.run()
            child = task
        } catch {
            NSLog("Thescanner launcher: failed to spawn \(binary): \(error)")
            NSApp.terminate(nil)
            return
        }

        // Menu-bar status item — a visible affordance for Open / Quit
        // since the .app has no main window of its own. Without this
        // the only way to quit cleanly would be Dock right-click,
        // which doesn't deliver Cmd+Q semantics.
        let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        item.button?.title = "Thescanner"
        let menu = NSMenu()
        let openItem = NSMenuItem(title: "Open Thescanner",
                                  action: #selector(openInBrowser),
                                  keyEquivalent: "")
        openItem.target = self
        menu.addItem(openItem)
        menu.addItem(NSMenuItem.separator())
        menu.addItem(NSMenuItem(title: "Quit Thescanner",
                                action: #selector(NSApplication.terminate(_:)),
                                keyEquivalent: "q"))
        item.menu = menu
        statusItem = item
    }

    @objc private func openInBrowser() {
        if let url = URL(string: "http://127.0.0.1:\(port)") {
            NSWorkspace.shared.open(url)
        }
    }

    // Dock-icon click after the browser tab was closed: re-open it.
    func applicationShouldHandleReopen(_ sender: NSApplication,
                                       hasVisibleWindows flag: Bool) -> Bool {
        openInBrowser()
        return true
    }

    func applicationWillTerminate(_ notification: Notification) {
        // SIGTERM the child so Go's signal handler runs its cleanup
        // (HTTP shutdown, lists.json + stats.json flush). If the child
        // already exited (terminationHandler path), .isRunning is
        // false, so we drop through.
        guard let c = child, c.isRunning else { return }
        c.terminate()
        // Poll for graceful exit. macOS gives the app ~5s during
        // shutdown before force-quitting it; a 2s budget here leaves
        // headroom for NSApplication teardown after we return.
        let deadline = Date().addingTimeInterval(2.0)
        while c.isRunning && Date() < deadline {
            Thread.sleep(forTimeInterval: 0.05)
        }
        if c.isRunning {
            // SIGTERM ignored — fall through to SIGKILL so the .app
            // doesn't leave an orphan thescanner-client lingering
            // after the Dock icon disappears.
            kill(c.processIdentifier, SIGKILL)
        }
    }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
// .regular = appears in Dock with running dot, gets a top menu bar.
// .accessory would hide the Dock icon entirely (status-bar-only app),
// which contradicts the "see that it's running" expectation.
app.setActivationPolicy(.regular)
app.run()
