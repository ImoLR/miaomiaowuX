#!/bin/bash
# Fork Backend build script.
set -e

BUILD_DIR="build"
LICENSE_PKG="miaomiaowux/internal/license"

echo "========================================"
echo "开始构建 Fork Backend"
echo "========================================"

if [ -z "$LICENSE_PUB_KEY" ]; then
    echo "⚠ 警告: 未设置 LICENSE_PUB_KEY 环境变量"
    echo "  构建出的二进制可能无法验证许可证响应，PRO 功能不可用。"
    echo "  正式发布请先: export LICENSE_PUB_KEY=\"...\""
    echo ""
fi

echo "[0/2] 同步版本号..."
bash scripts/sync-version.sh
echo "版本号同步完成 ✓"

mkdir -p "$BUILD_DIR"
LDFLAGS="-s -w -X '${LICENSE_PKG}.licenseSignPubKeyB64=${LICENSE_PUB_KEY}'"

echo "[1/2] 构建 Linux Backend..."
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="${LDFLAGS}" -o "${BUILD_DIR}/mmwx-backend-linux-amd64" ./cmd/server
echo "Linux Backend 构建完成 ✓"

echo "[2/2] 构建 Windows Backend..."
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="${LDFLAGS}" -o "${BUILD_DIR}/mmwx-backend-windows-amd64.exe" ./cmd/server
echo "Windows Backend 构建完成 ✓"

echo ""
echo "========================================"
echo "构建完成！"
echo "========================================"
echo "输出文件:"
echo "  - Linux:   ${BUILD_DIR}/mmwx-backend-linux-amd64"
echo "  - Windows: ${BUILD_DIR}/mmwx-backend-windows-amd64.exe"
