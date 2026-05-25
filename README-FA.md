## thescanner — فارسی

ابزار اسکن ریزالورهای عمومی DNS. کلاینت اسکنر، کوئری‌های احراز‌هویت‌شده‌ی DNS را از طریق ریزالورهای عمومی به سرور مرجعی که خودتان کنترل می‌کنید می‌فرستد و گزارش می‌دهد کدام ریزالورها به‌درستی پاسخ‌ها را برمی‌گردانند.

قالب روی سیم: [PROTOCOL.md](PROTOCOL.md). انگلیسی: [README.md](README.md).

<!-- ASCII diagram kept in English: Markdown renderers (GitHub, IDEs)
     mangle the RTL/LTR boundaries around the Persian variant, so the
     arrows and box edges don't line up. -->

```
[scanner-client] --DNS-→ [public resolver] --recursive-→ [your scanner-server]
       ↑                                                          │
       └────────── authenticated response over TXT ───────────────┘
```

### نصب سرور

روی یک هاست لینوکسی که رکوردهای NS یک دامنه را در اختیار دارید:

```
curl -fsSL https://raw.githubusercontent.com/sartoopjj/thescanner/main/scripts/install.sh | sudo bash
```

نصب‌کننده دامنه‌ها، پورت‌های DNS و پنل، توکن ادمین، و گواهی/کلید TLS اختیاری را می‌پرسد. فایل `/opt/thescanner/config.json` (chmod 600) را می‌نویسد و سرویس `thescanner-server.service` را راه می‌اندازد. اجرای مجدد منوی به‌روزرسانی/ویرایش دامنه/حذف می‌دهد.

با Docker:

```
git clone https://github.com/sartoopjj/thescanner && cd thescanner
mkdir -p data && $EDITOR data/config.json    # نمونه: docs/config.sample.json
docker compose up -d
```

اگر `systemd-resolved` پورت ۵۳ را گرفته، ریدایرکت کنید:

```bash
IFACE=eth0
sudo iptables -t nat -I PREROUTING -i "$IFACE" -p udp --dport 53 -j REDIRECT --to-ports 5300
sudo iptables -t nat -I PREROUTING -i "$IFACE" -p tcp --dport 53 -j REDIRECT --to-ports 5300
sudo iptables -I INPUT -p udp --dport 5300 -j ACCEPT
sudo iptables -I INPUT -p tcp --dport 5300 -j ACCEPT
```

### نصب کلاینت

باینری مناسب سیستم‌عامل‌تان را از آخرین ریلیز بگیرید و `./thescanner-client` را اجرا کنید. رابط کاربری روی `http://127.0.0.1:8080` باز می‌شود. لیست ریزالور (یک IP یا CIDR در هر خط) بدهید، سرور را انتخاب کنید (یا یک URI با شکل `thescanner://server?...` ایمپورت کنید) و Start را بزنید. کانفیگ از طریق UI مدیریت می‌شود — دستی ویرایش نکنید.

### حالت‌های اسکن

- **اسکن سطحی**: عبور سریع از کل لیست با ریترای و گسترش اختیاری /24 برای IPهای OK.
- **اسکن عمیق**: هر کاندید را چند بار آزمایش می‌کند تا نرخ موفقیت، p95 RTT و امتیاز ترکیبی محاسبه شود. هم روی نتایج OK اسکن سطحی، هم مستقیماً روی لیست دستیِ مورد اعتمادتان قابل اجراست.

هر اسکن به‌صورت یک لیست جدا در `<data-dir>/lists/` ذخیره می‌شود. باز کردن، تغییر نام، اسکن مجدد، اسکن عمیق و حذف انبوه از تب **Lists**.

### پنل مدیریت

`http(s)://<host>:8053/<admin_path>/` — با توکن ادمین از `config.json` وارد شوید. `admin_path` یک پیشوند URL تصادفی ۱۲۸ بیتی به‌ازای هر نصب است؛ هر درخواستی که با آن شروع نشود یک ۴۰۴ خشک می‌گیرد (بدون بدنه، بدون بنر). صفحه‌ی ورود هیچ نشانی از نام محصول ندارد، پس پراب روی `/admin`, `/healthz`, `/login` و … چیزی بیرون نمی‌دهد.

داخل پنل: شمارنده‌ها به‌ازای هر توکن، URIهای اشتراک، و ویرایشگر آدرس‌های Listen، گواهی/کلید TLS، دامنه‌ها و توکن‌ها. ذخیره‌سازی اتمیک است (`.tmp` + rename + chmod 600). برای اعمال تغییر توکن‌ها/دامنه‌ها سرور را ری‌استارت کنید.

### TLS

مقدارهای `server.tls_cert` و `server.tls_key` (مسیر PEM) را ست کنید — یا با فلگ‌های `-tls-cert / -tls-key` — تا پنل روی HTTPS اجرا شود. هر دو باید با هم باشند. نصب‌کننده هم می‌پرسد.

### امنیت

- مجموع آنتروپی ≈ ۲۵۶ بیت (پیشوند رندوم پنل + توکن ادمین) — بدون نیاز به Rate Limit.
- مقایسه‌ی توکن در زمان ثابت. توکن اشتباه → همان ۴۰۴ خشک URL اشتباه.
- هدرهای دفاعی: `X-Frame-Options: DENY`، CSP سخت‌گیر با `frame-ancestors 'none'`، `nosniff`، `Cache-Control: no-store`.
- POST روی `<panel>/config` به ۲۵۶ KiB محدود است. تایم‌اوت‌های HTTP: 5s/30s/30s/120s.
- WebView (اندروید و iOS) ناوبری به هر مبدأی غیر از Loopback را رد می‌کند.
- برای دسترسی غیر-localhost حتماً HTTPS بزنید. بعد از ذخیره‌ی URL، خط ورود به ژورنال systemd را پاک کنید. `config.json` و URIهای `thescanner://` را به‌عنوان راز نگه دارید.

### تفاوت توکن ادمین و توکن اشتراکی

- **`server.admin_token`**: رمز ورود به پنل.
- **`tokens[].secret`**: کلیدی که کلاینت‌ها برای امضای کوئری DNS استفاده می‌کنند. توزیع از طریق URIهای `thescanner://server?...` که پنل تولید می‌کند.

این دو یکی نیستند. در `make run-server`، فایل کانفیگ توسعه با `admin_token: "adminpass"` (رمز پنل) و یک توکن `dev` با سکرت `"clientkey"` (کلید امضای کلاینت) ساخته می‌شود.

### ساخت از سورس

```
make test                # تست‌های واحد با -race
make server / make client
make build-all           # کراس‌کامپایل لینوکس/مک/فری‌بی‌اس‌دی/ویندوز + اندروید
make gomobile-aar android   # AAR + APK امضاشده
make ios-bind ios-build     # iOS با gomobile + xcodebuild
make mac-dmg                # دی‌ام‌جی یونیورسال Intel + Apple Silicon
```

### فلگ‌های CLI

سرور:

```
thescanner-server \
  -config /opt/thescanner/config.json  -data-dir /opt/thescanner/data \
  -listen 0.0.0.0:5300                 -stats-listen 0.0.0.0:8053 \
  -admin-token XXXX                    -admin-path  abcdef0123456789 \
  -tls-cert /etc/.../fullchain.pem     -tls-key  /etc/.../privkey.pem \
  -domain v.example.com,x.example.com  -token-name alice -token-secret SECRET
```

کلاینت:

```
thescanner-client -data-dir ~/.config/thescanner -listen 127.0.0.1:8080 -no-browser
```

### تست‌ها

```
make test    # کل تست‌ها با -race
make lint    # vet + gofmt + golangci-lint (در صورت نصب)
```

### خارج از محدوده‌ی v1

کدینگ NULL-record، DoH/DoT، ریزالورهای IPv6، ARQ بین کوئری‌ها. به PROTOCOL.md §۱۱ مراجعه کنید.

### مجوز

MIT — به فایل [LICENSE](LICENSE) مراجعه کنید.
