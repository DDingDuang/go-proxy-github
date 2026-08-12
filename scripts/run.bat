@echo off
rem =====================================================================
rem 项目级启动脚本(Windows 本地开发用): 构建并在前台运行, 产物都在项目内
rem 用法: scripts\run.bat
rem =====================================================================
cd /d "%~dp0.."

if not exist bin mkdir bin
go build -o bin\github-gateway.exe .
bin\github-gateway.exe -config config.yaml
