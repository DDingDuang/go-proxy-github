#!/usr/bin/env sh
# =====================================================================
# 项目级部署打包脚本:
#   构建多平台静态二进制(默认 linux/amd64 + linux/arm64, 可用 PLATFORMS 覆盖)
#   + 配置样例模板 + 平台自适应 run.sh, 产出自包含 tar.gz。
#   服务器解压后 run.sh 会自动匹配当前平台的二进制, 无需 Go 工具链。
#   用法: ./scripts/deploy.sh
#   环境变量: PLATFORMS="linux/amd64 linux/arm64 windows/amd64"
# =====================================================================
set -eu

cd "$(dirname "$0")/.."

VERSION="$(git describe --tags --always 2>/dev/null || echo dev)"
PLATFORMS="${PLATFORMS:-linux/amd64 linux/arm64}"
OUT="dist/github-gateway-${VERSION}.tar.gz"
STAGE="dist/stage"

mkdir -p dist
rm -rf "$STAGE"
mkdir -p "$STAGE"

for p in $PLATFORMS; do
  os="${p%/*}"
  arch="${p#*/}"
  echo "[deploy] 构建 ${os}/${arch} ..."
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -ldflags="-s -w" \
    -o "$STAGE/github-gateway-${os}-${arch}" .
done

cp config.example.yaml "$STAGE/"
cp scripts/run.sh "$STAGE/"
cp README.md "$STAGE/"

tar -czf "$OUT" -C "$STAGE" .
rm -rf "$STAGE"

echo "[deploy] 包: $OUT"
echo "[deploy] 服务器上执行:"
echo "    tar xzf $(basename "$OUT")"
echo "    cp config.example.yaml config.yaml   # 填写上游代理账号密码"
echo "    ./run.sh start                       # 自动匹配当前平台二进制并启动"
