#!/usr/bin/env bash
# miaomiaowuX Fork development-stack installer.
#
# It installs two independently released components:
#   1. the official-style mmwx backend from this Fork's GitHub Release;
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
LEGACY_CUSTOM_SERVICE="mmwx-custom-api"

TEMP_DIR=""
BACKUP_DIR=""
LEGACY_CUSTOM_WAS_ACTIVE=0

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
  for command in curl jq sha256sum tar systemctl; do
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
  BACKEND_ASSET="mmwx-linux-$ARCH"
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
  touch "$CONFIG_FILE"
  chmod 600 "$CONFIG_FILE"

  ensure_config_value PORT "${PORT:-12889}"
  ensure_config_value DATABASE_PATH "${DATABASE_PATH:-$DATA_DIR/mmwx.db}"
  ensure_config_value LOG_LEVEL "${LOG_LEVEL:-info}"
  ensure_config_value MMWXC_API_LISTEN_ADDR "${MMWXC_API_LISTEN_ADDR:-127.0.0.1:12890}"
  ensure_config_value MMWXC_FRONTEND_DIR "$APP_DIR/frontend/dist"
  ensure_config_value MMWX_API_TARGET "${MMWX_API_TARGET:-http://127.0.0.1:${PORT:-12889}}"
  ensure_config_value MMWXC_ALLOWED_ORIGINS "${MMWXC_ALLOWED_ORIGINS:-http://127.0.0.1:12890,http://localhost:12890,https://mmwxc.imgamer.top}"
}

ensure_config_value() {
  local name="$1"
  local value="$2"
  if ! grep -q "^${name}=" "$CONFIG_FILE"; then
    printf '%s=%s\n' "$name" "$value" >> "$CONFIG_FILE"
  fi
}

config_value() {
  local name="$1"
  sed -n "s/^${name}=//p" "$CONFIG_FILE" | tail -n 1
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
  [ ! -d "$APP_DIR/frontend" ] || mv "$APP_DIR/frontend" "$BACKUP_DIR/frontend"
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
  if [ "$LEGACY_CUSTOM_WAS_ACTIVE" -eq 1 ]; then
    systemctl restart "$LEGACY_CUSTOM_SERVICE.service" || true
  fi
}

stop_stack() {
  if systemctl is-active --quiet "$LEGACY_CUSTOM_SERVICE.service" 2>/dev/null; then
    LEGACY_CUSTOM_WAS_ACTIVE=1
    systemctl stop "$LEGACY_CUSTOM_SERVICE.service"
  fi
  systemctl stop "$CUSTOM_SERVICE.service" 2>/dev/null || true
  systemctl stop "$BACKEND_SERVICE.service" 2>/dev/null || true
}

start_stack() {
  systemctl daemon-reload
  systemctl enable "$BACKEND_SERVICE.service" "$CUSTOM_SERVICE.service" >/dev/null
  systemctl restart "$BACKEND_SERVICE.service"
  systemctl restart "$CUSTOM_SERVICE.service"
  systemctl is-active --quiet "$BACKEND_SERVICE.service"
  systemctl is-active --quiet "$CUSTOM_SERVICE.service"
  local listen_addr
  listen_addr="$(config_value MMWXC_API_LISTEN_ADDR)"
  curl -fsS --max-time 5 "http://$listen_addr/healthz" >/dev/null
}

install_or_update() {
  local mode="$1"
  if [ "$mode" = "update" ] && [ ! -f "$BACKEND_BINARY" ]; then
    die "未检测到已安装的开发栈，请先运行 install.sh。"
  fi

  require_root
  detect_architecture
  install_dependencies
  download_releases
  ensure_config_defaults
  mkdir -p "$APP_DIR"
  backup_current_installation
  stop_stack

  if ! install -m 0755 "$TEMP_DIR/$BACKEND_ASSET" "$BACKEND_BINARY" || \
     ! install -m 0755 "$TEMP_DIR/custom-package/mmwx-custom" "$CUSTOM_BINARY" || \
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

  if [ "$LEGACY_CUSTOM_WAS_ACTIVE" -eq 1 ]; then
    systemctl disable "$LEGACY_CUSTOM_SERVICE.service" >/dev/null 2>&1 || true
    info "已将旧服务 $LEGACY_CUSTOM_SERVICE.service 迁移到 $CUSTOM_SERVICE.service"
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
    [ "$DATA_DIR" = "/etc/mmwx-custom" ] || die "拒绝删除非预期的数据目录。"
    rm -rf "$DATA_DIR"
    info "已彻底卸载程序、数据库和配置。"
  else
    info "已删除程序，数据库和配置仍保留在 $DATA_DIR。"
  fi
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
