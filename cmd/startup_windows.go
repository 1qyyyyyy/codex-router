//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"codex-router/config"
	"golang.org/x/sys/windows"
)

func runDefault() error {
	path, err := prepareDesktopStartup()
	if err != nil {
		return err
	}
	return runDesktop(path)
}

func prepareDesktopStartup() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("获取程序路径失败：%w", err)
	}
	return prepareDesktopStartupForExecutable(executable)
}

func prepareDesktopStartupForExecutable(executable string) (string, error) {
	absolute, err := filepath.Abs(executable)
	if err != nil {
		return "", fmt.Errorf("解析程序路径失败：%w", err)
	}
	path := filepath.Join(filepath.Dir(absolute), "config.yaml")
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("检查程序目录配置失败：%w", err)
		}
		if err := config.WriteDefault(path); err != nil {
			return "", err
		}
	}
	return path, nil
}

func showStartupError(err error) bool {
	getConsole := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow")
	if getConsole.Find() == nil {
		if window, _, _ := getConsole.Call(); window != 0 {
			return false
		}
	}
	message := desktopString("启动失败：" + err.Error())
	title := desktopString("路由助手")
	result := desktopCall("MessageBoxW", 0, uintptr(unsafe.Pointer(message)), uintptr(unsafe.Pointer(title)), 0x10)
	return result != 0
}
