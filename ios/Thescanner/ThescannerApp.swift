import SwiftUI

@main
struct ThescannerApp: App {
    @StateObject private var server = ServerController()

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environmentObject(server)
                .onAppear { server.start() }
                .onDisappear { server.stop() }
        }
    }
}
