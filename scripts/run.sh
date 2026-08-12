#!/usr/bin/env sh
# =====================================================================
# 项目级运行脚本(零环境污染, 平台自适应):
#   - 自动检测当前平台(OS/ARCH), 查找并运行匹配的二进制
#   - 二进制按平台命名: bin/github-gateway-<os>-<arch>[.exe]
#   - 找不到匹配二进制时按目标平台现场构建(需 Go 工具链)
#   - 停止时跨平台强杀: TERM → KILL → 端口兜底 → taskkill 兜底
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

# ── 监听端口(从 config.yaml 提取, 用于端口兜底杀进程) ──
listen_port() {
  sed -n 's/^[[:space:]]*listen:[[:space:]]*"\{0,1\}[^":]*:\([0-9][0-9]*\)"\{0,1\}.*/\1/p' \
    "$CONFIG" 2>/dev/null | head -1
}
PORT="$(listen_port)"
[ -z "$PORT" ] && PORT=38018

mkdir -p "$LOGDIR" "$BIN_DIR"

# ── 进程工具(跨平台) ──
# kill_pid <pid>: 先 TERM, 等待退出, 未退再 KILL
kill_pid() {
  pid="$1"
  kill "$pid" 2>/dev/null || true
  i=0
  while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 15 ]; do
    sleep 0.2
    i=$((i + 1))
  done
  if kill -0 "$pid" 2>/dev/null; then
    kill -9 "$pid" 2>/dev/null || true
    sleep 0.3
  fi
}

# pids_on_port: 列出监听 $PORT 的进程 PID(跨平台 netstat)
pids_on_port() {
  netstat -ano 2>/dev/null | grep ":$PORT" | grep -i listening | awk '{print $NF}' | sort -u
}

# stop_processes: 尽力终止网关进程, 返回 0 表示确实终止了至少一个
stop_processes() {
  stopped=0
  # 1) PID 文件记录的进程
  if [ -f "$PIDFILE" ]; then
    pid=$(cat "$PIDFILE" 2>/dev/null || true)
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
      kill_pid "$pid"
      stopped=1
    fi
    rm -f "$PIDFILE"
  fi
  # 2) 端口兜底: MSYS 下 kill 常失效, 直接按监听端口的真实 PID 处理
  for pid in $(pids_on_port); do
    if [ -n "$pid" ] && [ "$pid" != "0" ]; then
      case "$OS" in
        windows) taskkill //F //PID "$pid" >/dev/null 2>&1 || true ;;
        *) kill_pid "$pid" ;;
      esac
      stopped=1
    fi
  done
  # 3) Windows 兜底: 按进程名(防止 PID 文件与端口都失效的场景)
  if [ "$OS" = "windows" ] && command -v taskkill >/dev/null 2>&1; then
    taskkill //F //IM "${APP}.exe" >/dev/null 2>&1 && stopped=1
  fi
  return 0
}

# process_alive: PID 文件中的进程是否还活着
process_alive() {
  [ -f "$PIDFILE" ] || return 1
  pid=$(cat "$PIDFILE" 2>/dev/null || true)
  [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null
}

# ── 二进制查找与验证 ──
# binary_ok <path>: 通过 -version 探测, 验证二进制与当前平台匹配且可执行
binary_ok() {
  [ -n "$1" ] && [ -x "$1" ] || return 1
  "$1" -version >/dev/null 2>&1
}

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
  # 若旧进程未死(端口仍占用), 先清理, 避免端口冲突
  if [ -n "$(pids_on_port)" ]; then
    echo "[run] 检测到端口 ${PORT} 仍有旧进程占用, 正在清理..."
    stop_processes
    sleep 1
  fi
  resolve_binary

  nohup "$BIN" -config "$CONFIG" >>"$LOGFILE" 2>&1 &
  echo $! >"$PIDFILE"
  sleep 1
  if process_alive; then
    echo "[run] started pid=$(cat "$PIDFILE") binary=$BIN log=$LOGFILE"
  else
    echo "[run] 启动失败, 请查看日志: $LOGFILE"
    exit 1
  fi
}

stop() {
  stop_processes
  # 确认端口确实释放
  sleep 1
  if [ -n "$(pids_on_port)" ]; then
    echo "[run] 警告: 仍有进程占用端口 ${PORT}, 请手动检查: netstat -ano | grep :${PORT}"
    exit 1
  fi
  echo "[run] stopped"
}

status() {
  if process_alive; then
    echo "[run] running (pid $(cat "$PIDFILE"))"
  elif [ -n "$(pids_on_port)" ]; then
    echo "[run] 进程存活但 PID 文件缺失(可能由 stop 失效导致), 请执行 ./run.sh stop 清理"
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
