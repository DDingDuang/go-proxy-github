#!/usr/bin/env sh
# =====================================================================
# 项目级运行脚本(零环境污染):
#   - 构建产物输出到项目 bin/, 日志写到项目 logs/, PID 记录在项目内
#   - 不安装到系统, 不修改全局/系统配置
#   用法: ./scripts/run.sh {start|stop|restart|status}
# =====================================================================
set -eu

# 支持两种布局: 仓库内 scripts/run.sh(项目根 = ../) 或部署包根目录 run.sh(项目根 = 本目录)
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
case "$SCRIPT_DIR" in
  */scripts) PROJECT_DIR=$(dirname "$SCRIPT_DIR") ;;
  *)         PROJECT_DIR=$SCRIPT_DIR ;;
esac
cd "$PROJECT_DIR"

APP=github-gateway
BIN="bin/${APP}"
CONFIG="config.yaml"
LOGDIR="logs"
PIDFILE="${LOGDIR}/${APP}.pid"
LOGFILE="${LOGDIR}/${APP}.log"

mkdir -p "$LOGDIR"

# 构建策略: 强制重建(REBUILD=1) → 项目 bin/ 产物 → 部署包根目录二进制 → 现场构建
if [ -n "${REBUILD:-}" ]; then
  echo "[run] rebuilding ${BIN} ..."
  CGO_ENABLED=0 go build -ldflags="-s -w" -o "$BIN" .
elif [ ! -x "$BIN" ] && [ -x "./$APP" ]; then
  BIN="./$APP"   # 部署包布局: 二进制位于包根目录
elif [ ! -x "$BIN" ]; then
  echo "[run] building ${BIN} ..."
  CGO_ENABLED=0 go build -ldflags="-s -w" -o "$BIN" .
fi

start() {
  if [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
    echo "[run] already running (pid $(cat "$PIDFILE"))"
    return 1
  fi
  nohup "$BIN" -config "$CONFIG" >>"$LOGFILE" 2>&1 &
  echo $! >"$PIDFILE"
  echo "[run] started pid=$(cat "$PIDFILE") log=$LOGFILE"
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
