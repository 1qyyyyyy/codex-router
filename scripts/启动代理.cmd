@echo off
chcp 65001 >nul
setlocal
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
echo 正在检测上游并启动后台代理……
"%~dp0CodexRouter.exe" start --config "%~dp0config.yaml"
set "EXIT_CODE=%ERRORLEVEL%"
if not "%EXIT_CODE%"=="0" echo 代理启动失败，退出代码：%EXIT_CODE%
if "%EXIT_CODE%"=="0" echo 可以关闭此窗口。停止时请双击结束代理。
pause
exit /b %EXIT_CODE%
