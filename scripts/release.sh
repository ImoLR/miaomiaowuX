#!/usr/bin/env bash
set -euo pipefail

# Publish a Fork installer release without rebuilding the official embedded UI.
# The release assets are verified copies of an official upstream Release; the
# Custom UI/API is delivered separately by ImoLR/mmwx-custom.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FORK_REPOSITORY="ImoLR/miaomiaowuX"
UPSTREAM_REPOSITORY="iluobei/miaomiaowuX"
BUNDLE_TAG="${1:-}"
UPSTREAM_TAG="${2:-v0.3.8}"

if [[ ! "$BUNDLE_TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Usage: $0 vX.Y.Z [official-upstream-tag]" >&2
  exit 1
fi

cd "$ROOT_DIR"
git diff --quiet && git diff --cached --quiet || {
  echo "Working tree must be clean before creating a release." >&2
  exit 1
}
[ "$(git branch --show-current)" = "main" ] || {
  echo "Release must be created from main." >&2
  exit 1
}
! git rev-parse "$BUNDLE_TAG" >/dev/null 2>&1 || {
  echo "Tag already exists locally: $BUNDLE_TAG" >&2
  exit 1
}
! git ls-remote --exit-code --tags origin "refs/tags/$BUNDLE_TAG" >/dev/null 2>&1 || {
  echo "Tag already exists on origin: $BUNDLE_TAG" >&2
  exit 1
}
! gh release view "$BUNDLE_TAG" --repo "$FORK_REPOSITORY" >/dev/null 2>&1 || {
  echo "Release already exists: $BUNDLE_TAG" >&2
  exit 1
}

TEMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TEMP_DIR"' EXIT
gh release download "$UPSTREAM_TAG" --repo "$UPSTREAM_REPOSITORY" \
  --pattern 'mmwx-linux-amd64' \
  --pattern 'mmwx-linux-arm64' \
  --pattern 'checksums.txt' \
  --dir "$TEMP_DIR"

for asset in mmwx-linux-amd64 mmwx-linux-arm64; do
  expected="$(awk -v name="$asset" '$2 == name || $2 == "*" name { print $1; exit }' "$TEMP_DIR/checksums.txt")"
  [ -n "$expected" ] || { echo "Missing checksum for $asset" >&2; exit 1; }
  printf '%s  %s\n' "$expected" "$TEMP_DIR/$asset" | sha256sum -c -
done

git tag "$BUNDLE_TAG"
git push origin main
git push origin "$BUNDLE_TAG"

gh release create "$BUNDLE_TAG" \
  --repo "$FORK_REPOSITORY" \
  --title "$BUNDLE_TAG" \
  --notes "Fork installer bundle. Backend assets are verified copies of the official ${UPSTREAM_REPOSITORY} ${UPSTREAM_TAG} Release. Custom UI and Custom API are downloaded separately from ImoLR/mmwx-custom." \
  "$TEMP_DIR/mmwx-linux-amd64" \
  "$TEMP_DIR/mmwx-linux-arm64" \
  "$TEMP_DIR/checksums.txt"
