#!/bin/bash
# 前后端统一打包脚本 (Linux/Mac)
set -e

echo "========================================"
echo "开始构建前后端项目"
echo "========================================"

# 设置变量
BUILD_DIR="build"
FRONTEND_DIR="frontend"
OUTPUT_DIR="${BUILD_DIR}/release"

# 许可证响应验签公钥 — 从环境变量取,源码里默认空(防 fork 自编译能验签)。
# 正式发布前 export LICENSE_PUB_KEY="base64 公钥" 再 ./build.sh
# 没设时 build 仍能成功,但 release 出来的二进制 PRO 功能用不了(所有许可证响应都验签失败)
if [ -z "$LICENSE_PUB_KEY" ]; then
    echo "⚠ 警告: 未设置 LICENSE_PUB_KEY 环境变量"
    echo "  构建出的二进制将无法验证许可证响应,PRO 功能不可用。"
    echo "  正式发布请先: export LICENSE_PUB_KEY=\"...\""
    echo ""
fi
LICENSE_PKG="miaomiaowux/internal/license"
LDFLAGS="-s -w -X '${LICENSE_PKG}.licenseSignPubKeyB64=${LICENSE_PUB_KEY}'"

# 0. 同步版本号
echo ""
echo "[0/3] 同步版本号..."
bash scripts/sync-version.sh
echo "版本号同步完成 ✓"

# 清理旧的构建目录
if [ -d "$BUILD_DIR" ]; then
    echo "清理旧的构建文件..."
    rm -rf "$BUILD_DIR"
fi

# 1. 构建前端
echo ""
echo "[1/3] 构建前端项目..."

# 本 fork 维护新的前端源码,位于 $FRONTEND_DIR/。
# 已有 internal/web/dist/(从 release artifact 拉的 / CI 跑过的)+ SKIP_FRONTEND=1 → 跳过 npm,直接用现有 dist。
if [ "${SKIP_FRONTEND:-0}" = "1" ]; then
    echo "SKIP_FRONTEND=1, 跳过前端 build,沿用 internal/web/dist 现有产物"
    if [ ! -d "internal/web/dist" ] || [ -z "$(ls -A internal/web/dist 2>/dev/null)" ]; then
        echo "❌ internal/web/dist 为空,无法跳过 build"
        echo "   要么 unset SKIP_FRONTEND 跑完整流程,要么先拉一份 dist 产物放进去"
        exit 1
    fi
elif [ ! -d "$FRONTEND_DIR" ]; then
    echo "❌ 前端源码不存在: $FRONTEND_DIR/"
    echo ""
    echo "或仅 build 后端(沿用现有 dist):"
    echo "  SKIP_FRONTEND=1 ./build.sh"
    exit 1
else
    cd "$FRONTEND_DIR"
    if [ ! -d "node_modules" ]; then
        echo "安装前端依赖..."
        npm install
    fi

    echo "编译前端代码..."
    npm run build
    cd ..
fi
echo "前端构建完成 ✓"

# 2. 构建 Go 后端 (Linux)
echo ""
echo "[2/3] 构建 Linux 版本后端..."
GOOS=linux GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o "${BUILD_DIR}/mmwx-linux-amd64" cmd/server/main.go cmd/server/cors.go
echo "Linux 后端构建完成 ✓"

# 3. 构建 Go 后端 (Windows)
echo ""
echo "[3/3] 构建 Windows 版本后端..."
GOOS=windows GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o "${BUILD_DIR}/mmwx-windows-amd64.exe" cmd/server/main.go cmd/server/cors.go
echo "Windows 后端构建完成 ✓"

# 4. 准备发布文件
echo ""
echo "准备发布文件..."
mkdir -p "${OUTPUT_DIR}/linux"
mkdir -p "${OUTPUT_DIR}/windows"
mkdir -p "${BUILD_DIR}/data"
mkdir -p "${BUILD_DIR}/subscribes"

# 复制 Linux 版本到 release 目录
cp "${BUILD_DIR}/mmwx-linux-amd64" "${OUTPUT_DIR}/linux/"
chmod +x "${OUTPUT_DIR}/linux/mmwx-linux-amd64"
if [ -d "data" ]; then
    cp -r "data" "${OUTPUT_DIR}/linux/"
fi
if [ -d "subscribes" ]; then
    cp -r "subscribes" "${OUTPUT_DIR}/linux/"
fi
if [ -d "config" ]; then
    cp -r "config" "${OUTPUT_DIR}/linux/"
fi

# 复制 Windows 版本到 release 目录
cp "${BUILD_DIR}/mmwx-windows-amd64.exe" "${OUTPUT_DIR}/windows/"
if [ -d "data" ]; then
    cp -r "data" "${OUTPUT_DIR}/windows/"
fi
if [ -d "subscribes" ]; then
    cp -r "subscribes" "${OUTPUT_DIR}/windows/"
fi
if [ -d "config" ]; then
    cp -r "config" "${OUTPUT_DIR}/windows/"
fi

# 复制必要的配置文件到 build 根目录
if [ -d "data" ]; then
    cp -r "data" "${BUILD_DIR}/"
fi
if [ -d "subscribes" ]; then
    cp -r "subscribes" "${BUILD_DIR}/"
fi

echo ""
echo "========================================"
echo "构建完成！"
echo "========================================"
echo ""
echo "输出文件:"
echo "  - Linux:   ${BUILD_DIR}/mmwx-linux-amd64"
echo "  - Windows: ${BUILD_DIR}/mmwx-windows-amd64.exe"
echo "  - Release: ${OUTPUT_DIR}/"
echo ""
