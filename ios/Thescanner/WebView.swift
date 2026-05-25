import SwiftUI
import WebKit

struct WebView: UIViewRepresentable {
    let url: URL

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIView(context: Context) -> WKWebView {
        let cfg = WKWebViewConfiguration()
        cfg.websiteDataStore = .default()           // persist localStorage / cookies
        cfg.allowsInlineMediaPlayback = true
        cfg.mediaTypesRequiringUserActionForPlayback = []

        let view = WKWebView(frame: .zero, configuration: cfg)
        view.allowsBackForwardNavigationGestures = true
        view.scrollView.bounces = true
        // Web UI handles safe-area via env(); don't double-inset.
        view.scrollView.contentInsetAdjustmentBehavior = .never
        view.scrollView.contentInset = .zero
        view.scrollView.scrollIndicatorInsets = .zero
        // Transparent so safe-area zones inherit systemBackground.
        view.backgroundColor = .clear
        view.isOpaque = false
        view.scrollView.backgroundColor = .clear

        // Loopback-only navigation; blocked URLs are dropped (not handed off).
        view.navigationDelegate = context.coordinator
        // JS alert/confirm/prompt + window.open are dropped silently
        // unless a UIDelegate is set. Without this, the delete button's
        // confirm() returns false and nothing happens.
        view.uiDelegate = context.coordinator

        view.load(URLRequest(url: url))
        return view
    }

    func updateUIView(_ view: WKWebView, context: Context) {
        if view.url != url {
            view.load(URLRequest(url: url))
        }
    }

    final class Coordinator: NSObject, WKNavigationDelegate, WKUIDelegate {

        // MARK: - navigation policy

        func webView(_ webView: WKWebView,
                     decidePolicyFor navigationAction: WKNavigationAction,
                     decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
            let s = navigationAction.request.url?.absoluteString ?? ""
            if s.hasPrefix("http://127.0.0.1:") || s.hasPrefix("http://localhost:") {
                decisionHandler(.allow)
            } else {
                decisionHandler(.cancel)
            }
        }

        // Server-side export endpoints set Content-Disposition: attachment.
        // Without a download delegate WKWebView cancels the navigation
        // and the user sees a blank page. Instead, fetch the body
        // ourselves and present a system share sheet (Save to Files,
        // copy, AirDrop, …).
        func webView(_ webView: WKWebView,
                     decidePolicyFor navigationResponse: WKNavigationResponse,
                     decisionHandler: @escaping (WKNavigationResponsePolicy) -> Void) {
            if let http = navigationResponse.response as? HTTPURLResponse,
               let cd = http.value(forHTTPHeaderField: "Content-Disposition"),
               cd.lowercased().contains("attachment"),
               let url = http.url {
                decisionHandler(.cancel)
                downloadAndShare(url: url, suggestedName: filenameFrom(cd: cd, url: url))
                return
            }
            decisionHandler(.allow)
        }

        private func filenameFrom(cd: String, url: URL) -> String {
            // Naïve filename= parse — adequate for the curated outputs
            // our own server emits. Falls back to the URL's last path
            // component or "export.txt".
            if let r = cd.range(of: #"filename="?([^"]+)"?"#, options: .regularExpression) {
                let m = String(cd[r])
                if let eq = m.firstIndex(of: "=") {
                    return m[m.index(after: eq)...].trimmingCharacters(in: CharacterSet(charactersIn: "\""))
                }
            }
            let last = url.lastPathComponent
            return last.isEmpty ? "export.txt" : last
        }

        private func downloadAndShare(url: URL, suggestedName: String) {
            URLSession.shared.dataTask(with: url) { data, _, err in
                guard let data = data, err == nil else { return }
                let tmp = FileManager.default.temporaryDirectory
                    .appendingPathComponent(suggestedName)
                try? data.write(to: tmp, options: .atomic)
                DispatchQueue.main.async {
                    self.presentShareSheet(for: tmp)
                }
            }.resume()
        }

        private func presentShareSheet(for fileURL: URL) {
            guard let root = UIApplication.shared.connectedScenes
                .compactMap({ $0 as? UIWindowScene })
                .first?.windows.first(where: { $0.isKeyWindow })?
                .rootViewController else { return }
            let vc = UIActivityViewController(activityItems: [fileURL], applicationActivities: nil)
            // iPad: anchor the popover to the center to avoid a crash.
            vc.popoverPresentationController?.sourceView = root.view
            vc.popoverPresentationController?.sourceRect = CGRect(
                x: root.view.bounds.midX, y: root.view.bounds.midY,
                width: 1, height: 1
            )
            vc.popoverPresentationController?.permittedArrowDirections = []
            root.present(vc, animated: true)
        }

        // MARK: - JS dialogs

        // Bridge JS alert() → UIAlertController with OK.
        func webView(_ webView: WKWebView,
                     runJavaScriptAlertPanelWithMessage message: String,
                     initiatedByFrame frame: WKFrameInfo,
                     completionHandler: @escaping () -> Void) {
            present(title: nil, message: message,
                    actions: [.init(title: "OK", style: .default) { _ in completionHandler() }])
        }

        // Bridge JS confirm() → UIAlertController with OK/Cancel.
        func webView(_ webView: WKWebView,
                     runJavaScriptConfirmPanelWithMessage message: String,
                     initiatedByFrame frame: WKFrameInfo,
                     completionHandler: @escaping (Bool) -> Void) {
            present(title: nil, message: message, actions: [
                .init(title: "Cancel", style: .cancel)   { _ in completionHandler(false) },
                .init(title: "OK",     style: .default)  { _ in completionHandler(true) },
            ])
        }

        // Bridge JS prompt() → UIAlertController with a text field.
        func webView(_ webView: WKWebView,
                     runJavaScriptTextInputPanelWithPrompt prompt: String,
                     defaultText: String?,
                     initiatedByFrame frame: WKFrameInfo,
                     completionHandler: @escaping (String?) -> Void) {
            let alert = UIAlertController(title: nil, message: prompt, preferredStyle: .alert)
            alert.addTextField { $0.text = defaultText }
            alert.addAction(.init(title: "Cancel", style: .cancel) { _ in completionHandler(nil) })
            alert.addAction(.init(title: "OK", style: .default) { _ in
                completionHandler(alert.textFields?.first?.text ?? defaultText)
            })
            topViewController()?.present(alert, animated: true)
        }

        private func present(title: String?, message: String, actions: [UIAlertAction]) {
            let alert = UIAlertController(title: title, message: message, preferredStyle: .alert)
            actions.forEach(alert.addAction)
            topViewController()?.present(alert, animated: true)
        }

        private func topViewController() -> UIViewController? {
            UIApplication.shared.connectedScenes
                .compactMap({ $0 as? UIWindowScene })
                .first?.windows.first(where: { $0.isKeyWindow })?
                .rootViewController
        }
    }
}
