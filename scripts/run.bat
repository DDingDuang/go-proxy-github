@echo off
rem =====================================================================
rem 项目级启动脚本(Windows 本地开发): 自动检测平台并构建/运行
rem 用法: scripts\run.bat
rem =====================================================================
cd /d "%~dp0.."

if not exist bin mkdir bin

for /f %%i in ('go env GOOS') do set GOOS=%%i
for /f %%i in ('go env GOARCH') do set GOARCH=%%i
set PLATFORM=%GOOS%-%GOARCH%

if not exist "bin\github-gateway-%PLATFORM%.exe" (
  echo [run] 构建 github-gateway-%PLATFORM%.exe ...
  go build -o "bin\github-gateway-%PLATFORM%.exe" .
)

if not exist config.yaml (
  echo 未找到 config.yaml, 请先: copy config.example.yaml config.yaml 并填写上游代理
  exit /b 1
)

"bin\github-gateway-%PLATFORM%.exe" -config config.yaml
