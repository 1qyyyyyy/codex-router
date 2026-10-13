package service

import "fmt"

// Manage 根据操作管理当前用户或系统服务。
func Manage(action, executable, configPath string) error {
	switch action {
	case "install", "uninstall", "start", "stop", "status":
	default:
		return fmt.Errorf("不支持的服务操作：%s", action)
	}
	return managePlatform(action, executable, configPath)
}
