//go:build !windows

package integration

import (
	"encoding/json"
	"fmt"
	"os/exec"
)

const platformName = "unsupported"

func captureSystemProxy() (json.RawMessage, error) {
	return nil, fmt.Errorf("当前操作系统不支持系统代理备份")
}
func applySystemProxy(string) error {
	return fmt.Errorf("当前操作系统不支持系统代理设置")
}
func restoreSystemProxy(json.RawMessage) error {
	return fmt.Errorf("当前操作系统不支持系统代理恢复")
}
func resolvePlatformApplication(target string) (string, error) {
	if p := existingPath(target); p != "" {
		return p, nil
	}
	return "", fmt.Errorf("应用可执行文件不存在：%s", target)
}
func discoverApplication() string {
	for _, name := range []string{"chatgpt", "codex"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}
