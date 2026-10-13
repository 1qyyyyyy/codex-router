//go:build windows

package service

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func managePlatform(action, executable, configPath string) error {
	m, err := mgr.Connect()
	if err != nil {
		return windowsServiceError("连接服务管理器", err)
	}
	defer m.Disconnect()

	switch action {
	case "install":
		if executable == "" {
			return fmt.Errorf("安装 Windows 服务需要代理可执行文件路径")
		}
		if configPath == "" {
			return fmt.Errorf("安装 Windows 服务需要配置文件路径")
		}
		executable, err := filepath.Abs(executable)
		if err != nil {
			return fmt.Errorf("解析代理可执行文件路径失败：%w", err)
		}
		configPath, err = filepath.Abs(configPath)
		if err != nil {
			return fmt.Errorf("解析配置文件路径失败：%w", err)
		}
		s, err := m.CreateService(serviceName, executable, mgr.Config{DisplayName: "应用出口路由器代理服务", StartType: mgr.StartAutomatic}, windowsServiceArguments(configPath)...)
		if err != nil {
			return windowsServiceError("安装系统服务", err)
		}
		return s.Close()
	case "uninstall":
		s, err := m.OpenService(serviceName)
		if err != nil {
			return windowsServiceError("打开系统服务", err)
		}
		defer s.Close()
		if err := stopWindowsService(s); err != nil {
			return err
		}
		if err := s.Delete(); err != nil {
			return windowsServiceError("卸载系统服务", err)
		}
		return nil
	case "start":
		s, err := m.OpenService(serviceName)
		if err != nil {
			return windowsServiceError("打开系统服务", err)
		}
		defer s.Close()
		if err := s.Start(); err != nil {
			return windowsServiceError("启动系统服务", err)
		}
		return nil
	case "stop":
		s, err := m.OpenService(serviceName)
		if err != nil {
			return windowsServiceError("打开系统服务", err)
		}
		defer s.Close()
		return stopWindowsService(s)
	case "status":
		s, err := m.OpenService(serviceName)
		if err != nil {
			return windowsServiceError("打开系统服务", err)
		}
		defer s.Close()
		status, err := s.Query()
		if err != nil {
			return windowsServiceError("查询系统服务状态", err)
		}
		fmt.Printf("系统服务状态：%s\n", windowsServiceState(status.State))
		return nil
	default:
		return fmt.Errorf("不支持的服务操作：%s", action)
	}
}

func stopWindowsService(s *mgr.Service) error {
	status, err := s.Query()
	if err != nil {
		return windowsServiceError("查询系统服务状态", err)
	}
	if status.State == svc.Stopped {
		return nil
	}
	if status.State != svc.StopPending {
		if _, err := s.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return windowsServiceError("停止系统服务", err)
		}
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		status, err = s.Query()
		if err != nil {
			return windowsServiceError("等待系统服务停止", err)
		}
		if status.State == svc.Stopped {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("等待系统服务停止超时")
}

func windowsServiceError(action string, err error) error {
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return fmt.Errorf("%s权限不足，请以管理员身份重试：%w", action, err)
	}
	return fmt.Errorf("%s失败：%w", action, err)
}

func windowsCommandLine(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` }

func windowsServiceArguments(configPath string) []string {
	return []string{"run", "--config", filepath.Clean(configPath)}
}

func windowsServiceState(state svc.State) string {
	switch state {
	case 1:
		return "已停止"
	case 2:
		return "正在启动"
	case 3:
		return "正在停止"
	case 4:
		return "正在运行"
	case 5:
		return "正在继续"
	case 6:
		return "正在暂停"
	case 7:
		return "已暂停"
	default:
		return "未知"
	}
}
