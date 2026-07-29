#!/usr/bin/env bash
# Backward-compatible entrypoint. The managed installer now owns the service
# lifecycle and downloads both the Fork Backend and mmwx-custom Release.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -f "$SCRIPT_DIR/install.sh" ]; then
  exec bash "$SCRIPT_DIR/install.sh" "$@"
fi

exec bash <(curl -fsSL "https://raw.githubusercontent.com/ImoLR/miaomiaowuX/main/install.sh") "$@"
