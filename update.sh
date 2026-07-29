#!/usr/bin/env bash
set -euo pipefail

REPOSITORY="ImoLR/miaomiaowuX"
INSTALL_URL="${MMWX_INSTALL_SCRIPT_URL:-https://raw.githubusercontent.com/$REPOSITORY/main/install.sh}"

is_safe_script_path() {
  local source_path="${BASH_SOURCE[0]:-}"
  local resolved
  case "$source_path" in
    ""|/dev/stdin|/dev/fd/*|/proc/*/fd/*) return 1 ;;
  esac
  [ -f "$source_path" ] || return 1
  resolved="$(cd "$(dirname "$source_path")" && pwd -P)/$(basename "$source_path")" || return 1
  case "$resolved" in
    /dev/stdin|/dev/fd/*|/proc/*/fd/*) return 1 ;;
  esac
  [ -f "$resolved" ] || return 1
  SCRIPT_DIR="$(dirname "$resolved")"
}

run_downloaded_install() {
  local tmp_script
  local status
  tmp_script="$(mktemp)" || exit 1
  trap 'rm -f "$tmp_script"' EXIT
  curl -fL --retry 3 --retry-delay 2 "$INSTALL_URL" -o "$tmp_script"
  bash "$tmp_script" update "$@"
  status=$?
  exit "$status"
}

if is_safe_script_path && [ -f "$SCRIPT_DIR/install.sh" ]; then
  exec bash "$SCRIPT_DIR/install.sh" update "$@"
fi

run_downloaded_install "$@"
