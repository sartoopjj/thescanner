#!/bin/bash
# thescanner server installer.
# Re-run safe:
#   - first run: prompts for domains + ports + admin token, installs binary,
#                writes config, sets up systemd, optionally configures iptables
#                redirect when listening on an unprivileged port.
#   - subsequent runs: offers to update the binary, edit domains, or both.

set -euo pipefail

red='\033[0;31m'
green='\033[0;32m'
yellow='\033[0;33m'
blue='\033[0;34m'
plain='\033[0m'

GITHUB_REPO="sartoopjj/thescanner"
INSTALL_DIR="/opt/thescanner"
BIN_DIR="${INSTALL_DIR}/bin"
DATA_DIR="${INSTALL_DIR}/data"
CONFIG_FILE="${INSTALL_DIR}/config.json"
SERVICE_FILE="/etc/systemd/system/thescanner-server.service"
BINARY_NAME="thescanner-server"

# Defaults — overridable via env or interactively.
DEFAULT_DNS_PORT="${DEFAULT_DNS_PORT:-5300}"
DEFAULT_STATS_PORT="${DEFAULT_STATS_PORT:-8053}"

LOCAL_BUILD=0
ACTION="auto"
for arg in "$@"; do
  case "$arg" in
    --local)        LOCAL_BUILD=1 ;;
    --update)       ACTION="update" ;;
    --edit-domains) ACTION="edit-domains" ;;
    --uninstall)    ACTION="uninstall" ;;
    --help|-h)
      cat <<EOF
Usage: install.sh [--local] [--update | --edit-domains | --uninstall]

  (no flag)         First install, or re-run menu if already installed
  --local           Install from ./bin/ (dev mode) instead of GitHub release
  --update          Re-download latest binary, keep config
  --edit-domains    Re-prompt for domains, keep binary
  --uninstall       Remove binary, service, and config

Environment overrides:
  DEFAULT_DNS_PORT   default UDP port for DNS (default: ${DEFAULT_DNS_PORT})
  DEFAULT_STATS_PORT default TCP port for stats (default: ${DEFAULT_STATS_PORT})
EOF
      exit 0 ;;
  esac
done

[[ $EUID -ne 0 ]] && { echo -e "${red}error:${plain} run as root (sudo)"; exit 1; }

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    aarch64|arm64) echo arm64 ;;
    *) echo -e "${red}unsupported arch: $(uname -m)${plain}" >&2; exit 1 ;;
  esac
}
ARCH="$(detect_arch)"
OS=linux
[[ "$(uname -s)" == Darwin ]]  && OS=darwin
[[ "$(uname -s)" == FreeBSD ]] && OS=freebsd
echo -e "host: ${green}${OS}/${ARCH}${plain}"

install_base() {
  if [[ -f /etc/os-release ]]; then
    . /etc/os-release
    case "${ID:-}" in
      ubuntu|debian|armbian)
        apt-get update -q >/dev/null && apt-get install -y -q curl tar ca-certificates iptables jq >/dev/null 2>&1 || true ;;
      fedora|rhel|centos|rocky|almalinux|ol|amzn)
        dnf install -y -q curl tar ca-certificates iptables jq >/dev/null 2>&1 || yum install -y curl tar ca-certificates iptables jq >/dev/null 2>&1 || true ;;
      arch|manjaro)
        pacman -Sy --noconfirm curl tar ca-certificates iptables jq >/dev/null 2>&1 || true ;;
      alpine)
        apk add curl tar ca-certificates bash iptables jq >/dev/null 2>&1 || true ;;
    esac
  fi
}

# ---- JSON helpers (jq if present, sed fallback for the few fields we touch) ----

config_get() {
  local key="$1"
  [[ -f "$CONFIG_FILE" ]] || return 1
  if command -v jq >/dev/null 2>&1; then
    jq -r "$key // empty" "$CONFIG_FILE" 2>/dev/null
  else
    return 1
  fi
}

# Build a config.json from scratch given the prompted values.
write_config() {
  local listen="$1" stats_listen="$2" admin_token="$3" tls_cert="$4" tls_key="$5" admin_path="$6"
  shift 6
  local domains=("$@")

  local doms_json="["
  for i in "${!domains[@]}"; do
    [[ $i -gt 0 ]] && doms_json+=","
    doms_json+="{\"name\":\"${domains[$i]}\"}"
  done
  doms_json+="]"

  # TLS fields only appear when both are set.
  local tls_block=""
  if [[ -n "$tls_cert" && -n "$tls_key" ]]; then
    tls_block=",
    \"tls_cert\":    \"${tls_cert}\",
    \"tls_key\":     \"${tls_key}\""
  fi

  cat > "$CONFIG_FILE" <<JSON
{
  "server": {
    "listen":       "${listen}",
    "stats_listen": "${stats_listen}",
    "admin_token":  "${admin_token}",
    "admin_path":   "${admin_path}"${tls_block}
  },
  "domains": ${doms_json},
  "tokens": [
    { "name": "default", "secret": "CHANGE-ME-SHARED-SECRET" }
  ]
}
JSON
  chmod 0600 "$CONFIG_FILE"
}

# Generate a 32-hex-char URL-safe random sub-path for the admin panel.
gen_admin_path() {
  if command -v openssl >/dev/null; then
    openssl rand -hex 16
  else
    head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' | head -c 32
  fi
}

# Replace just the domains array in an existing config.
patch_domains() {
  local domains=("$@")
  if ! command -v jq >/dev/null 2>&1; then
    echo -e "${red}jq required to edit domains in place${plain}" >&2
    exit 1
  fi
  local doms_json="["
  for i in "${!domains[@]}"; do
    [[ $i -gt 0 ]] && doms_json+=","
    doms_json+="{\"name\":\"${domains[$i]}\"}"
  done
  doms_json+="]"
  local tmp
  tmp="$(mktemp)"
  jq ".domains = ${doms_json}" "$CONFIG_FILE" > "$tmp"
  mv "$tmp" "$CONFIG_FILE"
  chmod 0600 "$CONFIG_FILE"
}

# ---- prompts ----

prompt_domains() {
  echo "" >&2
  echo -e "${blue}Domain setup${plain} — enter one or more authoritative DNS suffixes" >&2
  echo "this server will answer for (e.g. v.example.com)." >&2
  echo "Leave the prompt empty to finish." >&2
  local -a out=()
  local i=1 d
  while true; do
    read -rp "  domain #$i: " d
    d="${d:-}"
    [[ -z "$d" ]] && break
    out+=("$d")
    ((i++))
  done
  if [[ ${#out[@]} -eq 0 ]]; then
    echo -e "${red}at least one domain is required${plain}" >&2
    exit 1
  fi
  printf '%s\n' "${out[@]}"
}

prompt_ports_and_token() {
  local cur_listen="${1:-0.0.0.0:${DEFAULT_DNS_PORT}}"
  local cur_stats="${2:-0.0.0.0:${DEFAULT_STATS_PORT}}"
  local cur_admin="${3:-}"

  echo "" >&2
  echo -e "${blue}Ports${plain}" >&2
  read -rp "  DNS listen address [${cur_listen}]: " listen
  listen="${listen:-$cur_listen}"
  read -rp "  Admin panel listen address [${cur_stats}]: " stats
  stats="${stats:-$cur_stats}"

  if [[ -z "$cur_admin" ]]; then
    if command -v openssl >/dev/null; then
      cur_admin="$(openssl rand -hex 32)"
    else
      cur_admin="$(head -c 64 /dev/urandom | base64 | tr -d '\n/+=' | head -c 64)"
    fi
  fi
  echo "" >&2
  echo -e "${blue}Admin token${plain} — used to sign in to the web panel at" >&2
  echo "  http(s)://<host>:${stats##*:}/admin" >&2
  echo "Distinct from the client-facing token shared secret (configured later in the panel)." >&2
  read -rp "  Admin token (Enter = use a random one): " admin
  admin="${admin:-$cur_admin}"

  echo "$listen|$stats|$admin"
}

prompt_tls() {
  echo "" >&2
  echo -e "${blue}TLS for the admin panel${plain} — optional but strongly recommended" >&2
  echo "if you're going to access the panel from anywhere other than localhost." >&2
  echo "Provide absolute paths to a PEM cert + key (e.g. from Let's Encrypt /" >&2
  echo "certbot). Press Enter at both prompts to serve over plain HTTP." >&2
  read -rp "  TLS cert path (Enter to skip): " tls_cert
  if [[ -n "$tls_cert" ]]; then
    read -rp "  TLS key  path: " tls_key
    if [[ -z "$tls_key" ]]; then
      echo -e "${red}TLS cert given but key missing — both required${plain}" >&2
      exit 1
    fi
    if [[ ! -r "$tls_cert" ]]; then
      echo -e "${yellow}warning: cannot read ${tls_cert} as root — make sure the path is correct${plain}" >&2
    fi
    if [[ ! -r "$tls_key" ]]; then
      echo -e "${yellow}warning: cannot read ${tls_key} as root${plain}" >&2
    fi
  else
    tls_key=""
  fi
  echo "$tls_cert|$tls_key"
}

# ---- port-53 redirect (iptables) ----
# Many distros run systemd-resolved on :53 and won't let us bind there.
# We listen on an unprivileged port (default 5300) and ask the kernel to
# redirect external 53/udp + 53/tcp traffic to it. Safe: PREROUTING only
# touches packets arriving on the external interface, so local DNS keeps
# working. To remove later, see README.md (Firewall section).

iptables_redirect() {
  local internal_port="$1"
  command -v iptables >/dev/null 2>&1 || {
    echo -e "${yellow}iptables not installed — skipping port-redirect step${plain}"
    return 0
  }
  # Pick the default-route interface for the PREROUTING rule.
  local iface
  iface="$(ip route 2>/dev/null | awk '/default/ {print $5; exit}')"
  [[ -z "$iface" ]] && iface="eth0"

  echo -e "${blue}Redirecting external 53 → ${internal_port} on ${iface}${plain}"
  iptables  -t nat -C PREROUTING -i "$iface" -p udp --dport 53 -j REDIRECT --to-ports "$internal_port" 2>/dev/null \
    || iptables  -t nat -I PREROUTING -i "$iface" -p udp --dport 53 -j REDIRECT --to-ports "$internal_port"
  iptables  -t nat -C PREROUTING -i "$iface" -p tcp --dport 53 -j REDIRECT --to-ports "$internal_port" 2>/dev/null \
    || iptables  -t nat -I PREROUTING -i "$iface" -p tcp --dport 53 -j REDIRECT --to-ports "$internal_port"
  iptables  -C INPUT -p udp --dport "$internal_port" -j ACCEPT 2>/dev/null \
    || iptables  -I INPUT -p udp --dport "$internal_port" -j ACCEPT
  iptables  -C INPUT -p tcp --dport "$internal_port" -j ACCEPT 2>/dev/null \
    || iptables  -I INPUT -p tcp --dport "$internal_port" -j ACCEPT

  # IPv6 best-effort.
  if command -v ip6tables >/dev/null 2>&1; then
    ip6tables -t nat -C PREROUTING -i "$iface" -p udp --dport 53 -j REDIRECT --to-ports "$internal_port" 2>/dev/null \
      || ip6tables -t nat -I PREROUTING -i "$iface" -p udp --dport 53 -j REDIRECT --to-ports "$internal_port" 2>/dev/null || true
    ip6tables -t nat -C PREROUTING -i "$iface" -p tcp --dport 53 -j REDIRECT --to-ports "$internal_port" 2>/dev/null \
      || ip6tables -t nat -I PREROUTING -i "$iface" -p tcp --dport 53 -j REDIRECT --to-ports "$internal_port" 2>/dev/null || true
  fi

  # Persistence hint.
  if command -v netfilter-persistent >/dev/null 2>&1; then
    netfilter-persistent save >/dev/null 2>&1 || true
    echo -e "  ${green}rules persisted via netfilter-persistent${plain}"
  else
    echo -e "  ${yellow}note:${plain} install ${blue}iptables-persistent${plain} (or run ${blue}iptables-save${plain}) to survive reboot"
  fi
}

# ---- binary fetch + install ----

install_binary() {
  mkdir -p "$BIN_DIR" "$DATA_DIR"
  chmod 0750 "$DATA_DIR"
  if [[ $LOCAL_BUILD -eq 1 ]]; then
    local src="bin/${BINARY_NAME}-${OS}-${ARCH}"
    [[ -f "$src" ]] || src="bin/${BINARY_NAME}"
    [[ -f "$src" ]] || { echo -e "${red}no local build at bin/; run 'make server' first${plain}" >&2; exit 1; }
    install -m 0755 "$src" "${BIN_DIR}/${BINARY_NAME}"
  else
    echo -e "fetching latest release from github.com/${GITHUB_REPO}…"
    local tag
    tag="$(curl -fsSL "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" | grep -m1 tag_name | cut -d'"' -f4)"
    [[ -n "$tag" ]] || { echo -e "${red}could not resolve latest tag${plain}" >&2; exit 1; }
    local url="https://github.com/${GITHUB_REPO}/releases/download/${tag}/${BINARY_NAME}-${OS}-${ARCH}"
    local tmp; tmp="$(mktemp)"
    curl -fsSL "$url" -o "$tmp"
    install -m 0755 "$tmp" "${BIN_DIR}/${BINARY_NAME}"
    rm -f "$tmp"
  fi
  echo -e "installed: ${green}${BIN_DIR}/${BINARY_NAME}${plain}"
}

write_systemd_unit() {
  cat > "$SERVICE_FILE" <<UNIT
[Unit]
Description=thescanner authoritative DNS server
After=network.target

[Service]
Type=simple
ExecStart=${BIN_DIR}/${BINARY_NAME} -config ${CONFIG_FILE} -data-dir ${DATA_DIR}
WorkingDirectory=${INSTALL_DIR}
Restart=on-failure
RestartSec=3
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true
PrivateTmp=true

# High-concurrency DNS workloads chew through file descriptors fast (each
# in-flight UDP/TCP query, the stats HTTP socket, the listening sockets).
# The kernel hard cap is plenty; just stop systemd from defaulting low.
LimitNOFILE=1048576
LimitNPROC=infinity
TasksMax=infinity

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable thescanner-server.service >/dev/null
  systemctl restart thescanner-server.service
  echo -e "systemd: ${green}thescanner-server.service started${plain}"
}

# ---- actions ----

do_first_install() {
  install_base
  install_binary

  local -a domains
  mapfile -t domains < <(prompt_domains)

  local prompt_out listen stats admin
  prompt_out="$(prompt_ports_and_token "0.0.0.0:${DEFAULT_DNS_PORT}" "0.0.0.0:${DEFAULT_STATS_PORT}" "")"
  IFS='|' read -r listen stats admin <<<"$prompt_out"

  local tls_out tls_cert tls_key
  tls_out="$(prompt_tls)"
  IFS='|' read -r tls_cert tls_key <<<"$tls_out"

  # Random per-installation admin URL prefix. The panel ONLY responds at
  # this path — every other URL returns a bare 404 with no banner. We
  # generate it ourselves rather than prompting because a human-chosen
  # path will be too short or too memorable.
  local admin_path
  admin_path="$(gen_admin_path)"

  mkdir -p "$INSTALL_DIR"
  write_config "$listen" "$stats" "$admin" "$tls_cert" "$tls_key" "$admin_path" "${domains[@]}"
  echo -e "wrote config: ${green}${CONFIG_FILE}${plain}"

  # Decide whether to set up the port-redirect.
  # listen format is "host:port"; if the port is not 53, offer the redirect.
  local listen_port="${listen##*:}"
  if [[ "$listen_port" != "53" ]]; then
    echo ""
    echo -e "${yellow}You picked port ${listen_port}, not 53.${plain}"
    echo "On hosts where systemd-resolved or another service holds :53, the standard"
    echo "fix is to listen on an unprivileged port and have iptables redirect external"
    echo "53→${listen_port}."
    read -rp "Set up the iptables redirect now? [Y/n]: " yn
    if [[ "${yn:-Y}" =~ ^[Yy] ]]; then
      iptables_redirect "$listen_port"
    fi
  fi

  [[ -d /run/systemd/system ]] && write_systemd_unit \
    || echo -e "${yellow}systemd not detected — start the binary manually${plain}"

  print_next_steps "$listen" "$stats" "$admin" "$tls_cert" "$admin_path" "${domains[@]}"
}

do_update_binary() {
  install_base
  install_binary
  systemctl daemon-reload >/dev/null 2>&1 || true
  systemctl restart thescanner-server.service >/dev/null 2>&1 || true
  echo -e "${green}binary updated; config untouched${plain}"
}

do_edit_domains() {
  [[ -f "$CONFIG_FILE" ]] || { echo -e "${red}no existing config — run installer without --edit-domains first${plain}"; exit 1; }
  local -a domains
  mapfile -t domains < <(prompt_domains)
  patch_domains "${domains[@]}"
  systemctl restart thescanner-server.service >/dev/null 2>&1 || true
  echo -e "${green}domains updated; service restarted${plain}"
}

do_uninstall() {
  systemctl stop thescanner-server.service 2>/dev/null || true
  systemctl disable thescanner-server.service 2>/dev/null || true
  rm -f "$SERVICE_FILE"
  systemctl daemon-reload 2>/dev/null || true
  echo -e "${yellow}leaving config + data at ${INSTALL_DIR} (rm manually if you really want it gone)${plain}"
  rm -f "${BIN_DIR}/${BINARY_NAME}"
  echo -e "${green}uninstalled${plain}"
}

print_next_steps() {
  local listen="$1" stats="$2" admin="$3" tls_cert="$4" admin_path="$5"
  shift 5
  local domains=("$@")
  local listen_port="${listen##*:}"
  local stats_port="${stats##*:}"
  local scheme="http"
  [[ -n "$tls_cert" ]] && scheme="https"

  echo ""
  echo -e "${green}════════════════════════════════════════════════════════════════${plain}"
  echo "thescanner-server is up."
  echo ""
  echo "  bin   : ${BIN_DIR}/${BINARY_NAME}"
  echo "  conf  : ${CONFIG_FILE}"
  echo "  data  : ${DATA_DIR}"
  echo "  ports : ${listen_port}/udp+tcp (DNS), ${stats_port}/tcp (${scheme} admin)"
  echo ""
  echo "domains:"
  for d in "${domains[@]}"; do echo "  • ${d}"; done
  echo ""
  echo "next steps:"
  echo "  1. point an NS record at this host for each domain above"
  echo "  2. open the admin panel — the URL contains a random per-install"
  echo "     prefix, so any other path returns a bare 404:"
  echo "       ${blue}${scheme}://<host>:${stats_port}/${admin_path}/${plain}"
  echo "     sign in with the admin token (saved in ${CONFIG_FILE})"
  echo "  3. inside the panel, add real shared-secret tokens and copy each"
  echo "     thescanner://server?... URI to hand to a client user"
  if [[ "$listen_port" != "53" ]]; then
    echo "  4. firewall rule:  external 53 → ${listen_port} (set up above; see README Firewall section to remove)"
  fi
  echo ""
  echo "remember: the ADMIN TOKEN signs in to the panel; the SHARED SECRET"
  echo "(under \"tokens\" in config.json) is what clients use to authenticate"
  echo "their DNS queries. They are not the same value."
  echo ""
  echo "the panel URL above is ALSO a secret — write it down (or bookmark"
  echo "it) before closing this terminal. The config file holds both values"
  echo "but only root can read it."
  echo -e "${green}════════════════════════════════════════════════════════════════${plain}"
}

# ---- dispatch ----

case "$ACTION" in
  update)       do_update_binary ;;
  edit-domains) do_edit_domains ;;
  uninstall)    do_uninstall ;;
  auto)
    if [[ -f "$CONFIG_FILE" ]]; then
      echo ""
      echo -e "${yellow}Existing install detected at ${INSTALL_DIR}.${plain}"
      echo "  1) update binary (keep config)"
      echo "  2) edit domains"
      echo "  3) re-run full install (overwrites config!)"
      echo "  4) uninstall"
      echo "  q) quit"
      read -rp "Choose: " choice
      case "${choice:-1}" in
        1) do_update_binary ;;
        2) do_edit_domains ;;
        3) do_first_install ;;
        4) do_uninstall ;;
        *) echo "bye" ;;
      esac
    else
      do_first_install
    fi ;;
esac
