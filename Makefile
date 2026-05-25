.PHONY: all build server client run-server run-client reset-dev-config test lint vet fmt clean install android gomobile-aar \
	ios-bind ios-bind-catalyst ios-build ios-list-sims ios-clean ios-reset \
	mac-app mac-dmg mac-clean \
	build-all build-linux-amd64 build-linux-arm64 build-darwin-amd64 build-darwin-arm64 \
	build-freebsd-amd64 build-freebsd-arm64 build-android-arm64 build-android-arm

BINARY_SERVER = thescanner-server
BINARY_CLIENT = thescanner-client
BIN_DIR       = bin

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS = -s -w \
	-X github.com/sartoopjj/thescanner/internal/version.Version=$(VERSION) \
	-X github.com/sartoopjj/thescanner/internal/version.Commit=$(COMMIT) \
	-X github.com/sartoopjj/thescanner/internal/version.Date=$(DATE)

GOFLAGS = -trimpath -ldflags="$(LDFLAGS)"
export CGO_ENABLED = 0

all: test build

build: server client

server:
	@mkdir -p $(BIN_DIR)
	go build $(GOFLAGS) -o $(BIN_DIR)/$(BINARY_SERVER) ./cmd/server

client:
	@mkdir -p $(BIN_DIR)
	go build $(GOFLAGS) -o $(BIN_DIR)/$(BINARY_CLIENT) ./cmd/client

test:
	go test -race -count=1 ./...

# ---- local dev: run the binaries against a tmp data dir ----------------
#   make run-server   — binds on 0.0.0.0:53 via sudo (needed for real
#                       public-resolver round-trips). Admin panel on
#                       :8053 at  http://localhost:8053/devpanel/
#                       Sign in with  adminpass  (panel password).
#                       Client signs DNS queries with  clientkey
#                       (paired with token-name "dev").
#                       Distinct values living in different config.json
#                       fields — one is not a fallback for the other.
#                       Any URL other than /devpanel/* returns a bare 404.
#                       Data under ./tmp/run/server.
#                       The binary detects SUDO_USER and chowns every
#                       file it writes back to you, so your IDE can
#                       still read tmp/run/server/* directly.
#   make run-client   — listens on 127.0.0.1:8080, data under
#                       ./tmp/run/client. Auto-opens the browser.

RUN_DATA_DIR   ?= ./tmp/run
RUN_DNS_LISTEN ?= 0.0.0.0:53
RUN_CFG         = $(RUN_DATA_DIR)/server/config.json

# Seed a dev config file on first run, then run with -config pointing at
# it. No override flags — the panel becomes the single source of truth,
# so anything you save (tokens, domains, ...) persists across restarts.
# Wipe with: rm $(RUN_CFG)  (or  make reset-dev-config).
$(RUN_CFG):
	@mkdir -p $(RUN_DATA_DIR)/server
	@printf '%s\n' \
		'{' \
		'  "server": {' \
		'    "listen":       "$(RUN_DNS_LISTEN)",' \
		'    "stats_listen": "0.0.0.0:8053",' \
		'    "admin_token":  "adminpass",' \
		'    "admin_path":   "devpanel"' \
		'  },' \
		'  "domains": [' \
		'    { "name": "v.example.com" },' \
		'    { "name": "x.example.com" },' \
		'    { "name": "y.example.com" }' \
		'  ],' \
		'  "tokens": [' \
		'    { "name": "dev", "secret": "clientkey" }' \
		'  ]' \
		'}' > $(RUN_CFG)
	@chmod 600 $(RUN_CFG)
	@echo "Seeded $(RUN_CFG)"

run-server: server $(RUN_CFG)
	@echo ""
	@echo "Panel:    http://localhost:8053/devpanel/"
	@echo "Sign in:  adminpass     (panel password)"
	@echo "Client:   clientkey     (token-name 'dev', shared secret for DNS queries)"
	@echo ""
	sudo $(BIN_DIR)/$(BINARY_SERVER) \
		-config $(RUN_CFG) \
		-data-dir $(RUN_DATA_DIR)/server

# `sudo` so root-owned leftovers from before the DropRoot fix can be
# cleaned out. New runs write files owned by you, so this stays cheap.
reset-dev-config:
	sudo rm -rf $(RUN_DATA_DIR)/server
	@echo "Wiped $(RUN_DATA_DIR)/server — next make run-server will reseed."

run-client: client
	@mkdir -p $(RUN_DATA_DIR)/client
	$(BIN_DIR)/$(BINARY_CLIENT) \
		-data-dir $(RUN_DATA_DIR)/client \
		-listen 127.0.0.1:8080

lint: vet
	@if [ -n "$$(gofmt -l . 2>/dev/null)" ]; then \
		echo "gofmt -l found unformatted files:"; \
		gofmt -l .; \
		exit 1; \
	fi
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run ./... || echo "golangci-lint not installed, skipping"

vet:
	go vet ./...

fmt:
	gofmt -s -w .

clean:
	rm -rf $(BIN_DIR) android/app/build android/build

install:
	@sudo bash scripts/install.sh --local

# ---- cross builds ----

build-all: build-linux-amd64 build-linux-arm64 build-darwin-amd64 build-darwin-arm64 \
	build-freebsd-amd64 build-freebsd-arm64 build-android-arm64 build-android-arm

build-linux-amd64:
	@mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -o $(BIN_DIR)/$(BINARY_SERVER)-linux-amd64 ./cmd/server
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -o $(BIN_DIR)/$(BINARY_CLIENT)-linux-amd64 ./cmd/client

build-linux-arm64:
	@mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -o $(BIN_DIR)/$(BINARY_SERVER)-linux-arm64 ./cmd/server
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -o $(BIN_DIR)/$(BINARY_CLIENT)-linux-arm64 ./cmd/client

build-darwin-amd64:
	@mkdir -p $(BIN_DIR)
	GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -o $(BIN_DIR)/$(BINARY_SERVER)-darwin-amd64 ./cmd/server
	GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -o $(BIN_DIR)/$(BINARY_CLIENT)-darwin-amd64 ./cmd/client

build-darwin-arm64:
	@mkdir -p $(BIN_DIR)
	GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -o $(BIN_DIR)/$(BINARY_SERVER)-darwin-arm64 ./cmd/server
	GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -o $(BIN_DIR)/$(BINARY_CLIENT)-darwin-arm64 ./cmd/client

build-freebsd-amd64:
	@mkdir -p $(BIN_DIR)
	GOOS=freebsd GOARCH=amd64 go build $(GOFLAGS) -o $(BIN_DIR)/$(BINARY_SERVER)-freebsd-amd64 ./cmd/server
	GOOS=freebsd GOARCH=amd64 go build $(GOFLAGS) -o $(BIN_DIR)/$(BINARY_CLIENT)-freebsd-amd64 ./cmd/client

build-freebsd-arm64:
	@mkdir -p $(BIN_DIR)
	GOOS=freebsd GOARCH=arm64 go build $(GOFLAGS) -o $(BIN_DIR)/$(BINARY_SERVER)-freebsd-arm64 ./cmd/server
	GOOS=freebsd GOARCH=arm64 go build $(GOFLAGS) -o $(BIN_DIR)/$(BINARY_CLIENT)-freebsd-arm64 ./cmd/client

build-android-arm64:
	@mkdir -p $(BIN_DIR)
	GOOS=android GOARCH=arm64 go build $(GOFLAGS) -o $(BIN_DIR)/$(BINARY_CLIENT)-android-arm64 ./cmd/client

build-android-arm:
	@mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=arm GOARM=7 go build $(GOFLAGS) -o $(BIN_DIR)/$(BINARY_CLIENT)-android-arm ./cmd/client

# ---- Android (release-only, signed) ----
# Keystore must exist at android/app/thescanner.jks (see tmp/android-keystore-howto.md).

ANDROID_AAR = android/app/libs/scanner.aar
ANDROID_API ?= 21

# gomobile-aar: produce the .aar consumed by android/app/. Requires:
#   go install golang.org/x/mobile/cmd/gomobile@latest
#   gomobile init
# Modern gomobile also requires `golang.org/x/mobile` in the module
# dependency graph (not just on PATH), so we go get + go mod tidy
# before binding — otherwise it errors with "missing golang.org/x/mobile
# dependency". Re-runs are cheap if it's already there.
gomobile-aar:
	@command -v gomobile >/dev/null 2>&1 || { echo "gomobile not found. Run: go install golang.org/x/mobile/cmd/gomobile@latest && gomobile init"; exit 1; }
	go get golang.org/x/mobile/bind
	go mod tidy
	@mkdir -p android/app/libs
	gomobile bind -target=android -androidapi $(ANDROID_API) -ldflags='$(LDFLAGS)' \
		-o $(ANDROID_AAR) github.com/sartoopjj/thescanner/mobile

android: gomobile-aar
	@test -f android/app/thescanner.jks || (echo "missing android/app/thescanner.jks — see tmp/android-keystore-howto.md" && exit 1)
	@test -f android/app/keystore.properties || (echo "missing android/app/keystore.properties — see tmp/android-keystore-howto.md" && exit 1)
	cd android && ./gradlew assembleRelease

# ---- iOS (gomobile xcframework + Xcode project) ----
# Requires: Xcode + gomobile.

IOS_FRAMEWORK = ios/Mobile.xcframework
IOS_PROJECT   = ios/Thescanner.xcodeproj
IOS_SCHEME    = Thescanner
# Strip leading 'v' so MARKETING_VERSION accepts both v0.1.0 and 0.1.0.
IOS_MARKETING_VERSION = $(patsubst v%,%,$(VERSION))
IOS_BUILD_NUMBER ?= $(shell git rev-list --count HEAD 2>/dev/null || echo 1)
IOS_LDFLAGS = -X github.com/sartoopjj/thescanner/internal/version.Version=$(VERSION) \
              -X github.com/sartoopjj/thescanner/internal/version.Commit=$(COMMIT) \
              -X github.com/sartoopjj/thescanner/internal/version.Date=$(DATE)
IOS_SIM_NAME ?= $(shell xcrun simctl list devices available 2>/dev/null | awk -F'[()]' '/-- iOS [0-9]/{ios=1;next} /^-- /{ios=0} ios && /iPhone/{print $$1; exit}' | sed 's/^[[:space:]]*//;s/[[:space:]]*$$//')
IOS_XCODE_VERSIONS = MARKETING_VERSION="$(IOS_MARKETING_VERSION)" CURRENT_PROJECT_VERSION="$(IOS_BUILD_NUMBER)"

ios-bind:
	@command -v gomobile >/dev/null 2>&1 || { echo "gomobile not found. Run: go install golang.org/x/mobile/cmd/gomobile@latest && gomobile init"; exit 1; }
	go get golang.org/x/mobile/bind
	go mod tidy
	gomobile bind -iosversion=15.0 -target=ios,iossimulator \
		-ldflags='$(IOS_LDFLAGS)' \
		-o $(IOS_FRAMEWORK) github.com/sartoopjj/thescanner/mobile

ios-bind-catalyst:
	@command -v gomobile >/dev/null 2>&1 || { echo "gomobile not found"; exit 1; }
	go get golang.org/x/mobile/bind
	go mod tidy
	gomobile bind -iosversion=15.0 -target=ios,iossimulator,maccatalyst \
		-ldflags='$(IOS_LDFLAGS)' \
		-o $(IOS_FRAMEWORK) github.com/sartoopjj/thescanner/mobile

ios-list-sims:
	xcrun simctl list devices available

ios-build: $(IOS_FRAMEWORK)
	# Force a stable terminal width before invoking clang/Swift. Xcode
	# hashes -fmessage-length=<COLUMNS> into the ModuleCache, so a build
	# in an 80-col terminal and another in a 53-col terminal generate
	# two incompatible PCMs for the same SDK module and the dependency
	# scanner refuses to pick one ("Unexpected variant during dependency
	# scanning on module 'Foundation'"). Pin it to 80 unconditionally.
	COLUMNS=80 xcodebuild -project $(IOS_PROJECT) -scheme $(IOS_SCHEME) \
		-destination 'platform=iOS Simulator,name=$(IOS_SIM_NAME)' \
		$(IOS_XCODE_VERSIONS) build

$(IOS_FRAMEWORK):
	$(MAKE) ios-bind

ios-clean:
	rm -rf $(IOS_FRAMEWORK) ios/build ios/DerivedData

# Nuclear option for the "Unexpected variant during dependency scanning"
# error: wipes the global Xcode ModuleCache + this project's DerivedData
# so the next ios-build rebuilds every PCM from scratch.
ios-reset: ios-clean
	rm -rf $(HOME)/Library/Developer/Xcode/DerivedData/ModuleCache.noindex
	rm -rf $(HOME)/Library/Developer/Xcode/DerivedData/Thescanner-*
	@echo "Module cache + Thescanner DerivedData wiped. Run: make ios-build"

# ---- macOS .app + .dmg (universal Intel + Apple Silicon) ----
# macOS-only. Needs lipo, hdiutil, sips, iconutil.

BUILD_DIR        ?= build
MAC_APP           = $(BUILD_DIR)/Thescanner.app
MAC_DMG           = $(BUILD_DIR)/thescanner-macos-$(VERSION).dmg
MAC_ICONSET       = $(BUILD_DIR)/Thescanner.iconset
MAC_SHORT_VER     = $(patsubst v%,%,$(VERSION))
MAC_ICON_PNG     ?= ios/Thescanner/Assets.xcassets/AppIcon.appiconset/image.png

mac-app:
	@command -v lipo >/dev/null 2>&1 || { echo "lipo not found — macOS-only"; exit 1; }
	@mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -o $(BUILD_DIR)/thescanner-client-darwin-amd64 ./cmd/client
	GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -o $(BUILD_DIR)/thescanner-client-darwin-arm64 ./cmd/client
	rm -rf $(MAC_APP)
	mkdir -p $(MAC_APP)/Contents/MacOS $(MAC_APP)/Contents/Resources
	lipo -create -output $(MAC_APP)/Contents/MacOS/thescanner-client \
		$(BUILD_DIR)/thescanner-client-darwin-amd64 \
		$(BUILD_DIR)/thescanner-client-darwin-arm64
	# Cocoa launcher. Finder runs Contents/MacOS/<CFBundleExecutable>
	# (= ./Thescanner). A bash shim works to exec the Go binary, but
	# without an NSApplication event loop macOS bounces the Dock icon
	# forever (treats it as "still launching") and never paints the
	# running-dot. mac/Thescanner.swift is a tiny NSApplication that
	# spawns thescanner-client as a child and adds a menu-bar Open/Quit
	# item — compile per-arch and lipo into a universal launcher.
	@command -v swiftc >/dev/null 2>&1 || { echo "swiftc not found — install Xcode Command Line Tools"; exit 1; }
	swiftc -O -target x86_64-apple-macos11 -o $(BUILD_DIR)/Thescanner-launcher-amd64 mac/Thescanner.swift
	swiftc -O -target arm64-apple-macos11  -o $(BUILD_DIR)/Thescanner-launcher-arm64 mac/Thescanner.swift
	lipo -create -output $(MAC_APP)/Contents/MacOS/Thescanner \
		$(BUILD_DIR)/Thescanner-launcher-amd64 \
		$(BUILD_DIR)/Thescanner-launcher-arm64
	@printf '%s\n' \
		'<?xml version="1.0" encoding="UTF-8"?>' \
		'<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">' \
		'<plist version="1.0">' \
		'<dict>' \
		'    <key>CFBundleName</key><string>Thescanner</string>' \
		'    <key>CFBundleDisplayName</key><string>Thescanner</string>' \
		'    <key>CFBundleExecutable</key><string>Thescanner</string>' \
		'    <key>CFBundleIdentifier</key><string>com.sartoopjj.thescanner</string>' \
		'    <key>CFBundleVersion</key><string>$(MAC_SHORT_VER)</string>' \
		'    <key>CFBundleShortVersionString</key><string>$(MAC_SHORT_VER)</string>' \
		'    <key>CFBundlePackageType</key><string>APPL</string>' \
		'    <key>CFBundleIconFile</key><string>AppIcon</string>' \
		'    <key>LSMinimumSystemVersion</key><string>11.0</string>' \
		'    <key>NSHighResolutionCapable</key><true/>' \
		'</dict>' \
		'</plist>' \
		> $(MAC_APP)/Contents/Info.plist
	@if [ -f "$(MAC_ICON_PNG)" ] && command -v sips >/dev/null 2>&1 && command -v iconutil >/dev/null 2>&1; then \
		rm -rf $(MAC_ICONSET); mkdir -p $(MAC_ICONSET); \
		for sz in 16 32 64 128 256 512 1024; do \
			sips -z $$sz $$sz "$(MAC_ICON_PNG)" --out "$(MAC_ICONSET)/icon_$${sz}x$${sz}.png" >/dev/null; \
		done; \
		iconutil -c icns "$(MAC_ICONSET)" -o "$(MAC_APP)/Contents/Resources/AppIcon.icns"; \
		rm -rf $(MAC_ICONSET); \
	else \
		echo "sips/iconutil unavailable — shipping .app without custom icon"; \
	fi
	@echo "Built $(MAC_APP)"

mac-dmg: mac-app
	@command -v hdiutil >/dev/null 2>&1 || { echo "hdiutil not found — macOS-only"; exit 1; }
	@rm -f $(MAC_DMG)
	@staging=$(BUILD_DIR)/dmg-staging; \
	rm -rf $$staging; mkdir -p $$staging; \
	cp -R $(MAC_APP) $$staging/; \
	ln -s /Applications $$staging/Applications; \
	hdiutil create -volname "Thescanner $(MAC_SHORT_VER)" -srcfolder $$staging -ov -format UDZO $(MAC_DMG); \
	rm -rf $$staging
	@echo "Built $(MAC_DMG)"

mac-clean:
	rm -rf $(MAC_APP) $(BUILD_DIR)/dmg-staging $(MAC_ICONSET)
	rm -f  $(BUILD_DIR)/thescanner-macos-*.dmg
