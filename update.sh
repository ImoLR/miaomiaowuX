#!/usr/bin/env bash
set -euo pipefail

REPOSITORY="ImoLR/miaomiaowuX"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ -f "$SCRIPT_DIR/install.sh" ]; then
  exec bash "$SCRIPT_DIR/install.sh" update "$@"
fi

exec bash <(curl -fsSL "https://raw.githubusercontent.com/$REPOSITORY/main/install.sh") update "$@"
