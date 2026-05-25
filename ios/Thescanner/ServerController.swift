import Combine     // ObservableObject + @Published
import Foundation
import Mobile      // gomobile-generated framework
import UIKit

/// Owns the embedded gomobile-backed HTTP server.
///
/// iOS doesn't allow long-lived background network listeners, so we stop
/// the server on backgrounding and start it again on foregrounding.
///
/// We pick a port from a fixed range (39000..39099) — same range as the
/// Android client. The previously-used port is remembered in
/// UserDefaults so the WebView origin (and therefore its localStorage /
/// cookies) survives app restarts.
final class ServerController: ObservableObject {
    @Published private(set) var url: URL?
    @Published private(set) var lastError: String?

    private var app: MobileApp?
    private var observers: [NSObjectProtocol] = []

    /// Reserved port range. Distinct from thefeed's 38000..38099 so the
    /// two apps can coexist on the same device.
    private let portRange: ClosedRange<Int> = 39000...39099
    private let savedPortKey = "ts.lastPort"

    init() {
        let center = NotificationCenter.default
        observers.append(center.addObserver(
            forName: UIApplication.didEnterBackgroundNotification,
            object: nil, queue: .main
        ) { [weak self] _ in self?.stop() })
        observers.append(center.addObserver(
            forName: UIApplication.willEnterForegroundNotification,
            object: nil, queue: .main
        ) { [weak self] _ in self?.start() })
    }

    deinit {
        observers.forEach(NotificationCenter.default.removeObserver)
        app?.stop()
    }

    func start() {
        guard app == nil else { return }
        do {
            let dir   = try Self.dataDir()
            let port  = pickPort()
            guard let inst = MobileNewApp() else {
                lastError = "MobileNewApp returned nil"
                return
            }
            try inst.start(dir.path, listen: "127.0.0.1:\(port)")

            // Read back the real URL — Go might have picked a different
            // port if our chosen one lost a race with another process.
            let actual = inst.address()
            if let u = URL(string: actual) {
                url = u
                if let p = u.port { UserDefaults.standard.set(p, forKey: savedPortKey) }
            }
            app = inst
            lastError = nil
        } catch let e as NSError {
            lastError = e.localizedDescription
        } catch {
            lastError = "\(error)"
        }
    }

    func stop() {
        app?.stop()
        app = nil
        url = nil
    }

    // ---- port selection ----

    /// Try the previously-saved port first (so the WebView origin stays
    /// stable across launches), then walk the reserved range. Fall back
    /// to the kernel's "any free port" only if every reserved slot is
    /// busy — that path loses localStorage continuity.
    private func pickPort() -> Int {
        let saved = UserDefaults.standard.integer(forKey: savedPortKey)
        if portRange.contains(saved), Self.tryBind(port: saved) { return saved }
        for p in portRange where Self.tryBind(port: p) { return p }
        return 0 // let the OS pick (ServerController will read back via .url)
    }

    private static func tryBind(port: Int) -> Bool {
        let sock = socket(AF_INET, SOCK_STREAM, 0)
        guard sock >= 0 else { return false }
        defer { close(sock) }
        var on: Int32 = 1
        setsockopt(sock, SOL_SOCKET, SO_REUSEADDR, &on, socklen_t(MemoryLayout<Int32>.size))
        var addr = sockaddr_in()
        addr.sin_family      = sa_family_t(AF_INET)
        addr.sin_port        = UInt16(port).bigEndian
        addr.sin_addr.s_addr = inet_addr("127.0.0.1")
        let bound = withUnsafePointer(to: &addr) { ptr -> Bool in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sa in
                bind(sock, sa, socklen_t(MemoryLayout<sockaddr_in>.size)) == 0
            }
        }
        return bound
    }

    private static func dataDir() throws -> URL {
        let docs = try FileManager.default.url(
            for: .documentDirectory, in: .userDomainMask,
            appropriateFor: nil, create: true
        )
        let dir = docs.appendingPathComponent("thescannerdata", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir
    }
}
