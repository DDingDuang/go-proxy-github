#!/usr/bin/env sh
# =====================================================================
# 构建脚本(单入口, 两种模式):
#
#   1. 单平台模式(默认): 构建当前平台二进制到项目 bin/
#        ./scripts/build.sh
#        可选: GOOS=linux GOARCH=arm64 ./scripts/build.sh   # 交叉编译指定平台
#
#   2. 打包模式: 构建多个平台并打成部署包 tar.gz 到 dist/
#        PLATFORMS="linux/amd64 linux/arm64" ./scripts/build.sh
#
# 二进制名带平台后缀: bin/github-gateway-<os>-<arch>[.exe]
# =====================================================================
set -eu

cd "$(dirname "$0")/.."

APP=github-gateway

# ── 打包模式: 多平台构建 + tar.gz ──
if [ -n "${PLATFORMS:-}" ]; then
  VERSION="$(git describe --tags --always 2>/dev/null || echo dev)"
  OUT="dist/${APP}-${VERSION}.tar.gz"
  STAGE="dist/stage"
  mkdir -p dist
  rm -rf "$STAGE"
  mkdir -p "$STAGE"
  for p in $PLATFORMS; do
    os="${p%/*}"
    arch="${p#*/}"
    echo "[build] 构建 ${os}/${arch} ..."
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -ldflags="-s -w" \
      -o "$STAGE/${APP}-${os}-${arch}" .
  done
  cp config.example.yaml "$STAGE/"
  cp README.md "$STAGE/"
  tar -czf "$OUT" -C "$STAGE" .
  rm -rf "$STAGE"
  echo "[build] 部署包: $OUT"
  echo "[build] 服务器上执行:"
  echo "    tar xzf $(basename "$OUT")"
  echo "    cp config.example.yaml config.yaml    # 填写上游代理账号密码"
  echo "    ./${APP}-linux-amd64 -config config.yaml   # 按服务器架构选择对应二进制"
  exit 0
fi

# ── 单平台模式: 构建当前(或指定)平台二进制到 bin/ ──
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
echo "[build] 构建 ${APP}-${PLATFORM} ..."
CGO_ENABLED=0 GOOS="$OS" GOARCH="$ARCH" go build -ldflags="-s -w" -o "bin/${APP}-${PLATFORM}${EXE}" .
echo "[build] 完成: bin/${APP}-${PLATFORM}${EXE}"
echo "[build] 运行: ./bin/${APP}-${PLATFORM}${EXE} -config config.yaml"
