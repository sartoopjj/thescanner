## thescanner

Resolver-scanning tool. The client sends authenticated DNS queries through public resolvers to an authoritative server you control, and reports which resolvers cleanly forward them.

Wire format: [PROTOCOL.md](PROTOCOL.md). فارسی: [README-FA.md](README-FA.md).

```
[scanner-client] --DNS-→ [public resolver] --recursive-→ [your scanner-server]
       ↑                                                          │
       └────────── authenticated response over TXT ───────────────┘
```

### Install server

On a Linux host where you control a domain's NS records:

```
curl -fsSL https://raw.githubusercontent.com/sartoopjj/thescanner/main/scripts/install.sh | sudo bash
```

Installer prompts for domains, DNS listen port, admin port, admin token, optional TLS cert/key. Writes `/opt/thescanner/config.json` (chmod 600). Installs and starts `thescanner-server.service`. Re-run for a menu (update binary, edit domains, reinstall, uninstall).

Or via Docker:

```
git clone https://github.com/sartoopjj/thescanner && cd thescanner
mkdir -p data && $EDITOR data/config.json    # see docs/config.sample.json
docker compose up -d
```

If `systemd-resolved` holds :53, redirect external 53 → 5300:

```bash
IFACE=eth0
sudo iptables -t nat -I PREROUTING -i "$IFACE" -p udp --dport 53 -j REDIRECT --to-ports 5300
sudo iptables -t nat -I PREROUTING -i "$IFACE" -p tcp --dport 53 -j REDIRECT --to-ports 5300
sudo iptables -I INPUT -p udp --dport 5300 -j ACCEPT
sudo iptables -I INPUT -p tcp --dport 5300 -j ACCEPT
```

### Install client

Grab a binary from the latest release and run `./thescanner-client`. UI opens at `http://127.0.0.1:8080`. Paste a resolver list (one IP/CIDR per line), pick a server (or import a `thescanner://server?...` URI), hit Start. Everything is configured through the UI — don't hand-edit `config.json`.

### Scan modes

- **Shallow scan**: broad first pass over a big list, with retries and optional /24 expansion of OK IPs.
- **Deep scan**: scoring pass that re-tests each candidate many times for success rate + p95 RTT + composite score. Run on a shallow list's OK IPs, or directly on a list of IPs you already trust.

Every scan is its own persisted list under `<data-dir>/lists/`. Open, rename, rescan, deep-scan, or bulk-delete from the **Lists** tab.

### Admin panel

`http(s)://<host>:8053/<admin_path>/` — sign in with the admin token from `config.json`. The `admin_path` is a per-install random 128-bit URL prefix; any request that doesn't start with it returns a bare 404 (no body, no banner). The sign-in page carries no product branding, so a probe of `/admin`, `/healthz`, `/login` etc. leaks nothing.

Inside: per-token counters, share URIs, and an editor for listen addresses, TLS cert/key, domains, and tokens. Saves are atomic (`.tmp` + rename + chmod 600). Restart the server to apply token/domain changes.

### TLS

Set `server.tls_cert` + `server.tls_key` (PEM paths) — or pass `-tls-cert / -tls-key` — to serve the panel over HTTPS. Both required together. Prompted at install time too.

### Security

- ~256 bits combined entropy (random panel path + admin token) — no rate limit by design.
- Constant-time token compare. Wrong token → bare 404, same as wrong URL.
- `X-Frame-Options: DENY`, strict CSP with `frame-ancestors 'none'`, `nosniff`, `Cache-Control: no-store`.
- POST `<panel>/config` capped at 256 KiB. HTTP timeouts 5s/30s/30s/120s.
- WebView (Android + iOS) refuses navigation off the loopback origin.
- Use HTTPS for any non-localhost panel access. Scrub the systemd journal of the startup URL once bookmarked. Treat `config.json` and `thescanner://` URIs as secrets.

### Admin token vs. shared-secret token

- **`server.admin_token`** signs you in to the panel.
- **`tokens[].secret`** is what clients use to sign DNS queries. Distributed via `thescanner://server?...` URIs from the panel.

They are not the same value. The `make run-server` dev recipe seeds a config with `admin_token: "adminpass"` (panel password) and a `dev` token whose secret is `"clientkey"` (the client signing key).

### Build from source

```
make test                # unit tests with -race
make server / make client
make build-all           # cross-compile linux/darwin/freebsd/windows + android
make gomobile-aar android   # gomobile AAR + signed APK
make ios-bind ios-build     # iOS via gomobile + xcodebuild
make mac-dmg                # universal Intel + Apple Silicon .dmg
```

### CLI flags

Server:

```
thescanner-server \
  -config /opt/thescanner/config.json  -data-dir /opt/thescanner/data \
  -listen 0.0.0.0:5300                 -stats-listen 0.0.0.0:8053 \
  -admin-token XXXX                    -admin-path  abcdef0123456789 \
  -tls-cert /etc/.../fullchain.pem     -tls-key  /etc/.../privkey.pem \
  -domain v.example.com,x.example.com  -token-name alice -token-secret SECRET
```

Client:

```
thescanner-client -data-dir ~/.config/thescanner -listen 127.0.0.1:8080 -no-browser
```

### Tests

```
make test    # full suite with -race
make lint    # vet + gofmt + golangci-lint (if installed)
```

Coverage targets: ≥80% on `internal/protocol`, ≥60% elsewhere.

### Out of scope for v1

NULL-record encoding (v2), DoH/DoT, IPv6 resolvers, ARQ across queries. See PROTOCOL.md §11.

### License

MIT — see [LICENSE](LICENSE).
