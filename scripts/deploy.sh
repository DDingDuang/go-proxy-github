#!/usr/bin/env sh
# =====================================================================
# 项目级部署打包脚本:
#   产出自包含的 tar.gz(二进制 + 配置 + 运行脚本 + README),
#   拷到目标服务器任意目录解压即可运行, 不安装到系统, 零环境污染。
#   用法: ./scripts/deploy.sh
# =====================================================================
set -eu

cd "$(dirname "$0")/.."

VERSION="$(git describe --tags --always 2>/dev/null || echo dev)"
OUT="dist/github-gateway-${VERSION}.tar.gz"
STAGE="dist/stage"

mkdir -p dist
echo "[deploy] building binary ..."
CGO_ENABLED=0 go build -ldflags="-s -w" -o dist/github-gateway .

rm -rf "$STAGE"
mkdir -p "$STAGE"
cp dist/github-gateway "$STAGE/"
cp config.yaml "$STAGE/"
cp scripts/run.sh "$STAGE/"
cp README.md "$STAGE/"

tar -czf "$OUT" -C "$STAGE" .
rm -rf "$STAGE"

echo "[deploy] package: $OUT"
echo "[deploy] 服务器上执行:"
echo "    tar xzf $(basename "$OUT")"
echo "    ./run.sh start        # 启动(日志写 logs/, PID 写 logs/)"
echo "    ./run.sh status|stop|restart"
