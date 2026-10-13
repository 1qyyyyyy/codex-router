@echo off
chcp 65001 >nul
setlocal DisableDelayedExpansion
cd /d "%~dp0"
if not exist "%~dp0CodexRouter.exe" (
    echo 找不到 CodexRouter.exe，请将此脚本放在发布目录中运行。
    pause
    exit /b 2
)
if not exist "%~dp0config.yaml" (
    echo 找不到 config.yaml，请将配置文件放在此脚本旁边。
    pause
    exit /b 2
)
set "ROUTER_PANEL_EXE=%~dp0CodexRouter.exe"
set "ROUTER_PANEL_CONFIG=%~dp0config.yaml"
set "ROUTER_PANEL_DIRECTORY=%~dp0"
powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "try { $quote = [char]34; $arguments = 'tray --config ' + $quote + $env:ROUTER_PANEL_CONFIG + $quote; Start-Process -FilePath $env:ROUTER_PANEL_EXE -ArgumentList $arguments -WorkingDirectory $env:ROUTER_PANEL_DIRECTORY -WindowStyle Hidden -ErrorAction Stop } catch { Write-Host '无法打开状态面板，请检查发布文件和配置路径。'; exit 2 }"
if errorlevel 1 (
    pause
    exit /b 2
)
exit /b 0
