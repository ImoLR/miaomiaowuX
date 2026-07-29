#!/usr/bin/env bash
set -euo pipefail

# Publish a Fork Backend release from this repository's own source.
# This script never downloads or republishes upstream official binaries.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FORK_REPOSITORY="ImoLR/miaomiaowuX"
TAG="${1:-}"
LICENSE_PKG="miaomiaowux/internal/license"

if [[ ! "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-fork\.[0-9]+$ ]]; then
  echo "Usage: $0 vX.Y.Z-fork.N" >&2
  exit 1
fi

cd "$ROOT_DIR"

for command in gh git sha256sum; do
  command -v "$command" >/dev/null 2>&1 || {
    echo "Missing required command: $command" >&2
    exit 1
  }
done

if ! command -v go >/dev/null 2>&1; then
  if [ -x /usr/local/go/bin/go ]; then
    export PATH="/usr/local/go/bin:$PATH"
  else
    echo "Missing required command: go" >&2
    exit 1
  fi
fi

git diff --quiet && git diff --cached --quiet || {
  echo "Working tree must be clean before creating a release." >&2
  exit 1
}
[ "$(git branch --show-current)" = "main" ] || {
  echo "Release must be created from main." >&2
  exit 1
}
! git rev-parse "$TAG" >/dev/null 2>&1 || {
  echo "Tag already exists locally: $TAG" >&2
  exit 1
}
! git ls-remote --exit-code --tags origin "refs/tags/$TAG" >/dev/null 2>&1 || {
  echo "Tag already exists on origin: $TAG" >&2
  exit 1
}
! gh release view "$TAG" --repo "$FORK_REPOSITORY" >/dev/null 2>&1 || {
  echo "Release already exists: $TAG" >&2
  exit 1
}

if [ -z "${LICENSE_PUB_KEY:-}" ]; then
  echo "Warning: LICENSE_PUB_KEY is empty. Built binaries may not validate PRO license responses." >&2
fi

TEMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TEMP_DIR"' EXIT
RELEASE_DIR="$TEMP_DIR/release"
mkdir -p "$RELEASE_DIR"

LDFLAGS="-s -w -X '${LICENSE_PKG}.licenseSignPubKeyB64=${LICENSE_PUB_KEY:-}'"
for arch in amd64 arm64; do
  output="mmwx-backend-linux-$arch"
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -trimpath \
    -ldflags="$LDFLAGS" \
    -o "$RELEASE_DIR/$output" ./cmd/server
  chmod +x "$RELEASE_DIR/$output"
done

pushd "$RELEASE_DIR" >/dev/null
sha256sum mmwx-backend-linux-amd64 mmwx-backend-linux-arm64 > checksums.txt
popd >/dev/null

git tag "$TAG"
git push origin main
git push origin "$TAG"

gh release create "$TAG" \
  --repo "$FORK_REPOSITORY" \
  --title "$TAG" \
  --generate-notes \
  --notes "Fork Backend release built from ImoLR/miaomiaowuX source. Custom UI and Custom API are delivered only by ImoLR/mmwx-custom." \
  "$RELEASE_DIR/mmwx-backend-linux-amd64" \
  "$RELEASE_DIR/mmwx-backend-linux-arm64" \
  "$RELEASE_DIR/checksums.txt"
