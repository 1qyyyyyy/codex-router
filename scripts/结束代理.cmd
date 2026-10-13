@echo off
chcp 65001 >nul
setlocal
cd /d "%~dp0"
if not exist "%~dp0CodexRouter.exe" (
    echo 找不到主程序，请将此脚本放在发布目录中运行。
    pause
    exit /b 2
)
echo 正在结束后台代理并恢复原网络设置……
"%~dp0CodexRouter.exe" stop --config "%~dp0config.yaml"
set "EXIT_CODE=%ERRORLEVEL%"
if not "%EXIT_CODE%"=="0" echo 恢复失败，请保留状态目录和日志。退出代码：%EXIT_CODE%
pause
exit /b %EXIT_CODE%
