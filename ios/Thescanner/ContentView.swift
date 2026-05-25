import SwiftUI

struct ContentView: View {
    @EnvironmentObject var server: ServerController
    // Follow the OS appearance so the safe-area zones (notch +
    // home-indicator) match light/dark mode. The web UI's own theme
    // can still be overridden by the user inside the app, but the
    // initial paint matches what they'd expect.
    @Environment(\.colorScheme) private var colorScheme

    var body: some View {
        ZStack {
            // Background fills the notch / home-indicator area. We use
            // systemBackground so it auto-flips between light and dark
            // without us hard-coding two colors. The web UI's html/body
            // background paints on top of the WebView itself.
            Color(.systemBackground).ignoresSafeArea()

            if let u = server.url {
                // Let the WebView paint edge-to-edge — the page's CSS
                // uses env(safe-area-inset-*) (via viewport-fit=cover)
                // to keep its own chrome out of the system zones.
                WebView(url: u).ignoresSafeArea()
            } else if let err = server.lastError {
                VStack(spacing: 12) {
                    Text("startup failed").font(.headline)
                    Text(err).font(.caption).foregroundColor(.secondary)
                        .multilineTextAlignment(.center)
                    Button("retry") { server.start() }
                        .buttonStyle(.borderedProminent)
                }
                .padding()
            } else {
                ProgressView()
            }
        }
    }
}
