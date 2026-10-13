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
echo 正在分步诊断上游端口、协议、认证、目标连接、本地入口和系统代理……
"%~dp0CodexRouter.exe" diagnose --config "%~dp0config.yaml"
set "EXIT_CODE=%ERRORLEVEL%"
if "%EXIT_CODE%"=="0" (
    echo 诊断完成，请查看六步结果；跳过的步骤不代表通过。
) else (
    echo 诊断未全部通过，请按失败步骤下方的建议处理。退出代码：%EXIT_CODE%
)
pause
exit /b %EXIT_CODE%
