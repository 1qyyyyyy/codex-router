//go:build !windows

package service

import "fmt"

func managePlatform(action, executable, configPath string) error {
	return fmt.Errorf("当前操作系统暂不支持服务管理：%s", action)
}
