#!/usr/bin/env sh
# =====================================================================
# 构建当前平台的网关二进制(输出到项目 bin/, 不安装到系统)
# 二进制名带平台后缀: bin/github-gateway-<os>-<arch>[.exe]
# 用法: ./scripts/build.sh
#   可选: GOOS=linux GOARCH=arm64 ./scripts/build.sh  # 交叉编译指定平台
# =====================================================================
set -eu

cd "$(dirname "$0")/.."

RAW_OS="${GOOS:-$(uname -s)}"
RAW_ARCH="${GOARCH:-$(uname -m)}"
case "$RAW_OS" in
  MINGW*|MSYS*|CYGWIN*) OS=windows ;;
  Linux)                OS=linux ;;
  Darwin)               OS=darwin ;;
  *) OS=$(echo "$RAW_OS" | tr '[:upper:]' '[:lower:]') ;;
esac
case "$RAW_ARCH" in
  x86_64|amd64)          ARCH=amd64 ;;
  aarch64|arm64)         ARCH=arm64 ;;
  i386|i686|i586)        ARCH=386 ;;
  *) ARCH="$RAW_ARCH" ;;
esac
PLATFORM="${OS}-${ARCH}"
[ "$OS" = "windows" ] && EXE=".exe" || EXE=""

mkdir -p bin
echo "[build] 构建 github-gateway-${PLATFORM} ..."
CGO_ENABLED=0 GOOS="$OS" GOARCH="$ARCH" go build -ldflags="-s -w" -o "bin/github-gateway-${PLATFORM}${EXE}" .
echo "[build] 完成: bin/github-gateway-${PLATFORM}${EXE}"
echo "[build] 运行: ./bin/github-gateway-${PLATFORM}${EXE} -config config.yaml"
