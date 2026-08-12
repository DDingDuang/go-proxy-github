#!/usr/bin/env sh
# =====================================================================
# 项目级运行脚本(零环境污染, 平台自适应):
#   - 自动检测当前平台(OS/ARCH), 查找并运行匹配的二进制
#   - 二进制按平台命名: bin/github-gateway-<os>-<arch>[.exe]
#   - 找不到匹配二进制时按目标平台现场构建(需 Go 工具链)
#   - 日志写到项目 logs/, PID 记录在项目内, 不安装到系统
#   用法: ./scripts/run.sh {start|stop|restart|status}
#   环境变量:
#     REBUILD=1          强制重新构建
#     RUN_OS / RUN_ARCH  覆盖平台检测(调试/测试用)
# =====================================================================
set -eu

# 项目根目录(支持 scripts/run.sh 与部署包根目录 run.sh 两种布局)
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
case "$SCRIPT_DIR" in
  */scripts) PROJECT_DIR=$(dirname "$SCRIPT_DIR") ;;
  *)         PROJECT_DIR=$SCRIPT_DIR ;;
esac
cd "$PROJECT_DIR"

APP=github-gateway
CONFIG="config.yaml"
LOGDIR="logs"
PIDFILE="${LOGDIR}/${APP}.pid"
LOGFILE="${LOGDIR}/${APP}.log"
BIN_DIR="bin"

# ── 平台检测 ──
RAW_OS="${RUN_OS:-$(uname -s)}"
RAW_ARCH="${RUN_ARCH:-$(uname -m)}"
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

mkdir -p "$LOGDIR" "$BIN_DIR"

# ── 二进制查找与验证 ──
# binary_ok <path>: 通过 -version 探测, 验证二进制与当前平台匹配且可执行
binary_ok() {
  [ -n "$1" ] && [ -x "$1" ] || return 1
  "$1" -version >/dev/null 2>&1
}

# find_binary: 按优先级查找匹配当前平台的二进制, 成功时设置全局 BIN
find_binary() {
  if binary_ok "$BIN_DIR/$APP-$PLATFORM$EXE"; then
    BIN="$BIN_DIR/$APP-$PLATFORM$EXE"; return 0
  fi
  if binary_ok "./$APP-$PLATFORM$EXE"; then
    BIN="./$APP-$PLATFORM$EXE"; return 0
  fi
  if binary_ok "./$APP"; then
    BIN="./$APP"; return 0
  fi
  return 1
}

# build: 按目标平台(当前检测到的 OS/ARCH)现场构建
build() {
  echo "[run] 构建 ${APP}-${PLATFORM} ..."
  if ! command -v go >/dev/null 2>&1; then
    echo "[run] 错误: 未找到与当前平台(${PLATFORM})匹配的二进制, 且本机没有 Go 工具链。"
    echo "[run] 请任选其一:"
    echo "  1. 安装 Go 工具链后重试;"
    echo "  2. 在其他机器运行 ./scripts/deploy.sh 打包, 将包拷到本机解压运行(包内含 ${APP}-linux-amd64 / -linux-arm64 等)。"
    echo "[run] 现有二进制:"
    ls -1 "$BIN_DIR" 2>/dev/null | sed 's/^/    /' || true
    exit 1
  fi
  CGO_ENABLED=0 GOOS="$OS" GOARCH="$ARCH" go build -ldflags="-s -w" -o "$BIN_DIR/$APP-$PLATFORM$EXE" .
  BIN="$BIN_DIR/$APP-$PLATFORM$EXE"
}

# resolve_binary: 定位或构建匹配当前平台的二进制(设置全局 BIN)
resolve_binary() {
  if [ -n "${REBUILD:-}" ]; then
    build
  elif find_binary; then
    :
  else
    build
  fi
}

# ── 生命周期 ──
start() {
  [ -f "$CONFIG" ] || {
    echo "[run] 未找到 config.yaml, 请先执行: cp config.example.yaml config.yaml 并填写上游代理"
    exit 1
  }
  resolve_binary

  if [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
    echo "[run] already running (pid $(cat "$PIDFILE"))"
    return 1
  fi
  nohup "$BIN" -config "$CONFIG" >>"$LOGFILE" 2>&1 &
  echo $! >"$PIDFILE"
  echo "[run] started pid=$(cat "$PIDFILE") binary=$BIN log=$LOGFILE"
}

stop() {
  if [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
    kill "$(cat "$PIDFILE")"
    rm -f "$PIDFILE"
    echo "[run] stopped"
  else
    rm -f "$PIDFILE"
    echo "[run] not running"
  fi
}

status() {
  if [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
    echo "[run] running (pid $(cat "$PIDFILE"))"
  else
    echo "[run] stopped"
  fi
}

case "${1:-start}" in
  start)   start ;;
  stop)    stop ;;
  restart) stop; sleep 1; start ;;
  status)  status ;;
  *) echo "usage: $0 {start|stop|restart|status}"; exit 1 ;;
esac
