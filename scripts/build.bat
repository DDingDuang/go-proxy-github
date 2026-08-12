@echo off
rem ============================================================
rem Build gateway binary for current platform into project bin\
rem Output: bin\github-gateway-<os>-<arch>.exe
rem Usage: scripts\build.bat
rem ============================================================
cd /d "%~dp0.."

if not exist bin mkdir bin

for /f %%i in ('go env GOOS') do set GOOS=%%i
for /f %%i in ('go env GOARCH') do set GOARCH=%%i
set PLATFORM=%GOOS%-%GOARCH%

echo [build] building github-gateway-%PLATFORM%.exe ...
go build -o "bin\github-gateway-%PLATFORM%.exe" .
echo [build] done: bin\github-gateway-%PLATFORM%.exe
echo [build] run: bin\github-gateway-%PLATFORM%.exe -config config.yaml
