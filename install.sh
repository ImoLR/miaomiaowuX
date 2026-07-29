#!/usr/bin/env bash
# miaomiaowuX Fork development-stack installer.
#
# It installs two independently released components:
#   1. the Fork Backend from this Fork's GitHub Release;
#   2. the Custom UI and Custom API from ImoLR/mmwx-custom's GitHub Release.

set -euo pipefail

FORK_RELEASE_REPO="ImoLR/miaomiaowuX"
CUSTOM_RELEASE_REPO="ImoLR/mmwx-custom"
INSTALL_DIR="/usr/local/bin"
APP_DIR="/opt/mmwx-custom"
DATA_DIR="/etc/mmwx-custom"
CONFIG_FILE="$DATA_DIR/mmwx-custom.env"
BACKEND_SERVICE="mmwx-custom-backend"
CUSTOM_SERVICE="mmwx-custom"
BACKEND_BINARY="$INSTALL_DIR/$BACKEND_SERVICE"
CUSTOM_BINARY="$INSTALL_DIR/$CUSTOM_SERVICE"
BACKEND_UNIT="/etc/systemd/system/$BACKEND_SERVICE.service"
CUSTOM_UNIT="/etc/systemd/system/$CUSTOM_SERVICE.service"
DEFAULT_BACKEND_ADDR="127.0.0.1:12891"
DEFAULT_CUSTOM_ADDR="127.0.0.1:12890"

TEMP_DIR=""
BACKUP_DIR=""

info() { printf '[INFO] %s\n' "$*"; }
warn() { printf '[WARN] %s\n' "$*" >&2; }
die() { printf '[ERROR] %s\n' "$*" >&2; exit 1; }

cleanup() {
  if [ -n "$TEMP_DIR" ] && [ -d "$TEMP_DIR" ]; then
    rm -rf "$TEMP_DIR"
  fi
}
trap cleanup EXIT

require_root() {
  [ "${EUID:-$(id -u)}" -eq 0 ] || die "请使用 root 权限运行此脚本。"
}

detect_architecture() {
  case "$(uname -m)" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) die "不支持的架构: $(uname -m)" ;;
  esac
}

install_dependencies() {
  local missing=0
  for command in curl jq sha256sum tar systemctl mv; do
    command -v "$command" >/dev/null 2>&1 || missing=1
  done
  if [ "$missing" -eq 0 ]; then
    return
  fi
  command -v apt-get >/dev/null 2>&1 || die "缺少 curl、jq、tar、sha256sum 或 systemctl，且当前系统没有 apt-get。"
  info "安装下载和校验依赖..."
  apt-get update -qq
  apt-get install -y ca-certificates curl jq tar coreutils systemd
}

latest_release_tag() {
  local repository="$1"
  local tag
  tag="$(curl -fsSL "https://api.github.com/repos/$repository/releases/latest" | jq -er '.tag_name')" || die "无法获取 $repository 的最新 Release。"
  printf '%s\n' "$tag"
}

download_asset() {
  local repository="$1"
  local tag="$2"
  local asset="$3"
  local destination="$4"
  curl -fL --retry 3 --retry-delay 2 \
    "https://github.com/$repository/releases/download/$tag/$asset" \
    -o "$destination" || die "下载失败: $repository $tag/$asset"
  [ -s "$destination" ] || die "下载内容为空: $asset"
}

verify_asset() {
  local checksum_file="$1"
  local asset_path="$2"
  local asset_name
  local expected
  asset_name="$(basename "$asset_path")"
  expected="$(awk -v name="$asset_name" '$2 == name || $2 == "*" name { print $1; exit }' "$checksum_file")"
  [ -n "$expected" ] || die "校验文件未包含 $asset_name"
  printf '%s  %s\n' "$expected" "$asset_path" | sha256sum -c - >/dev/null || die "校验失败: $asset_name"
}

download_releases() {
  TEMP_DIR="$(mktemp -d)"
  FORK_TAG="${FORK_VERSION:-$(latest_release_tag "$FORK_RELEASE_REPO")}"
  CUSTOM_TAG="${CUSTOM_VERSION:-$(latest_release_tag "$CUSTOM_RELEASE_REPO")}"
  BACKEND_ASSET="mmwx-backend-linux-$ARCH"
  CUSTOM_ASSET="mmwx-custom-linux-$ARCH.tar.gz"

  info "下载 Fork Release: $FORK_TAG"
  download_asset "$FORK_RELEASE_REPO" "$FORK_TAG" "$BACKEND_ASSET" "$TEMP_DIR/$BACKEND_ASSET"
  download_asset "$FORK_RELEASE_REPO" "$FORK_TAG" "checksums.txt" "$TEMP_DIR/fork-checksums.txt"
  verify_asset "$TEMP_DIR/fork-checksums.txt" "$TEMP_DIR/$BACKEND_ASSET"

  info "下载 mmwx-custom Release: $CUSTOM_TAG"
  download_asset "$CUSTOM_RELEASE_REPO" "$CUSTOM_TAG" "$CUSTOM_ASSET" "$TEMP_DIR/$CUSTOM_ASSET"
  download_asset "$CUSTOM_RELEASE_REPO" "$CUSTOM_TAG" "checksums.txt" "$TEMP_DIR/custom-checksums.txt"
  verify_asset "$TEMP_DIR/custom-checksums.txt" "$TEMP_DIR/$CUSTOM_ASSET"

  mkdir -p "$TEMP_DIR/custom-package"
  tar -xzf "$TEMP_DIR/$CUSTOM_ASSET" -C "$TEMP_DIR/custom-package"
  [ -x "$TEMP_DIR/custom-package/mmwx-custom" ] || die "mmwx-custom Release 缺少可执行文件。"
  [ -f "$TEMP_DIR/custom-package/frontend/dist/index.html" ] || die "mmwx-custom Release 缺少 Custom UI。"
}

ensure_config_defaults() {
  mkdir -p "$DATA_DIR"
  chmod 700 "$DATA_DIR"
  if [ -f "$CONFIG_FILE" ]; then
    cp -a "$CONFIG_FILE" "$DATA_DIR/mmwx-custom.env.bak.$(date +%Y%m%d-%H%M%S)"
  fi
  touch "$CONFIG_FILE"
  chmod 600 "$CONFIG_FILE"

  ensure_config_value PORT "${PORT:-12891}"
  ensure_config_value DATABASE_PATH "${DATABASE_PATH:-$DATA_DIR/mmwx.db}"
  ensure_config_value LOG_LEVEL "${LOG_LEVEL:-info}"
  ensure_config_value MMWXC_API_LISTEN_ADDR "${MMWXC_API_LISTEN_ADDR:-127.0.0.1:12890}"
  ensure_config_value MMWXC_FRONTEND_DIR "$APP_DIR/frontend/dist"
  ensure_config_value MMWX_API_TARGET "${MMWX_API_TARGET:-http://127.0.0.1:${PORT:-12891}}"
  ensure_config_value MMWXC_ALLOWED_ORIGINS "${MMWXC_ALLOWED_ORIGINS:-http://127.0.0.1:12890,http://localhost:12890,https://mmwxc.imgamer.top}"
  migrate_development_defaults
}

ensure_config_value() {
  local name="$1"
  local value="$2"
  if ! grep -q "^${name}=" "$CONFIG_FILE"; then
    printf '%s=%s\n' "$name" "$value" >> "$CONFIG_FILE"
  fi
}

set_config_value() {
  local name="$1"
  local value="$2"
  if grep -q "^${name}=" "$CONFIG_FILE"; then
    sed -i "s|^${name}=.*|${name}=${value}|" "$CONFIG_FILE"
  else
    printf '%s=%s\n' "$name" "$value" >> "$CONFIG_FILE"
  fi
}

config_value() {
  local name="$1"
  sed -n "s/^${name}=//p" "$CONFIG_FILE" | tail -n 1
}

migrate_development_defaults() {
  local port
  local target
  port="$(config_value PORT)"
  target="$(config_value MMWX_API_TARGET)"

  if [ -z "${PORT:-}" ] && { [ -z "$port" ] || [ "$port" = "12889" ]; }; then
    set_config_value PORT "12891"
  fi
  if [ -z "${MMWX_API_TARGET:-}" ] && { [ -z "$target" ] || [ "$target" = "http://127.0.0.1:12889" ] || [ "$target" = "http://localhost:12889" ]; }; then
    set_config_value MMWX_API_TARGET "http://127.0.0.1:12891"
  fi
}

listen_addr_from_port() {
  printf '127.0.0.1:%s\n' "$(config_value PORT)"
}

host_from_addr() {
  printf '%s\n' "${1%:*}"
}

port_from_addr() {
  printf '%s\n' "${1##*:}"
}

port_is_open() {
  local addr="$1"
  local host
  local port
  host="$(host_from_addr "$addr")"
  port="$(port_from_addr "$addr")"
  if command -v ss >/dev/null 2>&1; then
    ss -lnt "sport = :$port" 2>/dev/null | awk 'NR > 1 {print $4}' | grep -Eq "(^|:)$port$" && return 0
  fi
  timeout 1 bash -c "</dev/tcp/$host/$port" >/dev/null 2>&1
}

assert_ports_available() {
  local backend_addr
  local custom_addr
  backend_addr="$(listen_addr_from_port)"
  custom_addr="$(config_value MMWXC_API_LISTEN_ADDR)"
  [ -n "$custom_addr" ] || custom_addr="$DEFAULT_CUSTOM_ADDR"

  if port_is_open "$backend_addr"; then
    die "开发 Fork Backend 端口被占用: $backend_addr"
  fi
  if port_is_open "$custom_addr"; then
    die "开发 mmwx-custom 端口被占用: $custom_addr"
  fi
}

write_systemd_units() {
  cat > "$BACKEND_UNIT" <<EOF
[Unit]
Description=miaomiaowuX Fork backend for the Custom development stack
After=network.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=$DATA_DIR
EnvironmentFile=$CONFIG_FILE
ExecStart=$BACKEND_BINARY
Restart=always
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

  cat > "$CUSTOM_UNIT" <<EOF
[Unit]
Description=miaomiaowuX Custom UI and API
After=$BACKEND_SERVICE.service network.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=$APP_DIR
EnvironmentFile=$CONFIG_FILE
ExecStart=$CUSTOM_BINARY
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
}

backup_current_installation() {
  BACKUP_DIR="$APP_DIR/backups/$(date +%Y%m%d-%H%M%S)"
  mkdir -p "$BACKUP_DIR"
  [ ! -f "$BACKEND_BINARY" ] || cp -a "$BACKEND_BINARY" "$BACKUP_DIR/backend"
  [ ! -f "$CUSTOM_BINARY" ] || cp -a "$CUSTOM_BINARY" "$BACKUP_DIR/custom"
  [ ! -f "$BACKEND_UNIT" ] || cp -a "$BACKEND_UNIT" "$BACKUP_DIR/backend.service"
  [ ! -f "$CUSTOM_UNIT" ] || cp -a "$CUSTOM_UNIT" "$BACKUP_DIR/custom.service"
  [ ! -d "$APP_DIR/frontend" ] || mv "$APP_DIR/frontend" "$BACKUP_DIR/frontend"
}

atomic_install() {
  local src="$1"
  local dst="$2"
  local tmp="${dst}.new.$$"
  install -m 0755 "$src" "$tmp"
  mv "$tmp" "$dst"
}

restore_current_installation() {
  warn "恢复更新前的程序文件..."
  if [ -f "$BACKUP_DIR/backend" ]; then
    install -m 0755 "$BACKUP_DIR/backend" "$BACKEND_BINARY"
  else
    rm -f "$BACKEND_BINARY"
  fi
  if [ -f "$BACKUP_DIR/custom" ]; then
    install -m 0755 "$BACKUP_DIR/custom" "$CUSTOM_BINARY"
  else
    rm -f "$CUSTOM_BINARY"
  fi
  if [ -d "$BACKUP_DIR/frontend" ]; then
    rm -rf "$APP_DIR/frontend"
    mv "$BACKUP_DIR/frontend" "$APP_DIR/frontend"
  fi
  if [ -f "$BACKUP_DIR/backend.service" ]; then
    cp -a "$BACKUP_DIR/backend.service" "$BACKEND_UNIT"
  else
    rm -f "$BACKEND_UNIT"
  fi
  if [ -f "$BACKUP_DIR/custom.service" ]; then
    cp -a "$BACKUP_DIR/custom.service" "$CUSTOM_UNIT"
  else
    rm -f "$CUSTOM_UNIT"
  fi
  systemctl daemon-reload
}

stop_stack() {
  systemctl stop "$CUSTOM_SERVICE.service" 2>/dev/null || true
  systemctl stop "$BACKEND_SERVICE.service" 2>/dev/null || true
}

start_stack() {
  local backend_addr
  local custom_addr
  backend_addr="$(listen_addr_from_port)"
  custom_addr="$(config_value MMWXC_API_LISTEN_ADDR)"

  systemctl daemon-reload
  systemctl enable "$BACKEND_SERVICE.service" "$CUSTOM_SERVICE.service" >/dev/null
  systemctl restart "$BACKEND_SERVICE.service"
  wait_for_url "http://$backend_addr/api/setup/status"
  systemctl restart "$CUSTOM_SERVICE.service"
  wait_for_url "http://$custom_addr/healthz"
  curl -fsS --max-time 5 "http://$custom_addr/api/custom/dashboard/system" >/dev/null
  curl -fsS --max-time 5 "http://$custom_addr/api/setup/status" >/dev/null
  systemctl is-active --quiet "$BACKEND_SERVICE.service"
  systemctl is-active --quiet "$CUSTOM_SERVICE.service"
}

wait_for_url() {
  local url="$1"
  local i
  for i in $(seq 1 40); do
    if curl -fsS --max-time 3 "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.5
  done
  warn "服务健康检查失败: $url"
  return 1
}

install_or_update() {
  local mode="$1"
  if [ "$mode" = "update" ] && [ ! -f "$BACKEND_BINARY" ] && [ ! -f "$CUSTOM_BINARY" ] && [ ! -f "$CUSTOM_UNIT" ]; then
    die "未检测到已安装的开发栈，请先运行 install.sh。"
  fi
  if [ "$mode" = "update" ] && [ ! -f "$BACKEND_BINARY" ] && { [ -f "$CUSTOM_BINARY" ] || [ -f "$CUSTOM_UNIT" ]; }; then
    info "检测到旧单服务部署，执行双服务迁移..."
  fi

  require_root
  detect_architecture
  install_dependencies
  download_releases
  ensure_config_defaults
  mkdir -p "$APP_DIR"
  backup_current_installation
  stop_stack
  assert_ports_available

  if ! atomic_install "$TEMP_DIR/$BACKEND_ASSET" "$BACKEND_BINARY" || \
     ! atomic_install "$TEMP_DIR/custom-package/mmwx-custom" "$CUSTOM_BINARY" || \
     ! mv "$TEMP_DIR/custom-package/frontend" "$APP_DIR/frontend"; then
    restore_current_installation
    start_stack || true
    die "安装程序文件失败，已恢复原版本。"
  fi

  write_systemd_units
  if ! start_stack; then
    restore_current_installation
    start_stack || true
    die "新版本启动失败，已恢复原版本。"
  fi

  printf '%s\n' "$FORK_TAG" > "$DATA_DIR/fork-version"
  printf '%s\n' "$CUSTOM_TAG" > "$DATA_DIR/custom-version"
  info "完成：Fork Backend $FORK_TAG + mmwx-custom $CUSTOM_TAG"
  info "数据和配置保存在 $DATA_DIR"
}

uninstall_stack() {
  local purge="${1:-}"
  require_root
  stop_stack
  systemctl disable "$CUSTOM_SERVICE.service" "$BACKEND_SERVICE.service" 2>/dev/null || true
  rm -f "$CUSTOM_UNIT" "$BACKEND_UNIT" "$CUSTOM_BINARY" "$BACKEND_BINARY"
  systemctl daemon-reload

  [ "$APP_DIR" = "/opt/mmwx-custom" ] || die "拒绝删除非预期的程序目录。"
  rm -rf "$APP_DIR"

  if [ "$purge" = "--purge" ]; then
    warn "根据当前安全策略，--purge 不会删除数据库、配置或日志。"
  else
    :
  fi
  info "已删除开发程序、开发目录和 systemd 服务，数据库、配置和日志仍保留在 $DATA_DIR。"
}

case "${1:-install}" in
  install)
    install_or_update install
    ;;
  update)
    install_or_update update
    ;;
  reinstall)
    install_or_update install
    ;;
  uninstall)
    uninstall_stack "${2:-}"
    ;;
  *)
    echo "Usage: $0 [install|update|reinstall|uninstall [--purge]]" >&2
    exit 1
    ;;
esac
