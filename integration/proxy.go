package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type systemProxyBackup struct {
	Version int             `json:"版本"`
	OS      string          `json:"操作系统"`
	Data    json.RawMessage `json:"设置"`
}

var captureProxySnapshot = captureSystemProxy
var enablePlatformProxy = applySystemProxy
var restorePlatformProxy = restoreSystemProxy

// EnableSystemProxy 先独占保存当前设置，再启用本地代理。
func EnableSystemProxy(proxyURL, backupPath string) error {
	if proxyURL == "" {
		return fmt.Errorf("代理地址不能为空")
	}
	if backupPath == "" {
		return fmt.Errorf("代理设置备份路径不能为空")
	}
	data, err := captureProxySnapshot()
	if err != nil {
		return err
	}
	if err := writeBackupExclusive(backupPath, systemProxyBackup{Version: 1, OS: platformName, Data: data}); err != nil {
		return err
	}
	if err := enablePlatformProxy(proxyURL); err != nil {
		rollbackErr := restorePlatformProxy(data)
		if rollbackErr != nil {
			return fmt.Errorf("启用系统代理失败：%v；自动恢复原设置也失败：%w；原设置备份保留在 %s", err, rollbackErr, backupPath)
		}
		return fmt.Errorf("启用系统代理失败，已尝试恢复原设置：%w；备份保留在 %s", err, backupPath)
	}
	return nil
}

// RestoreSystemProxy 仅在设置恢复成功后删除备份。
func RestoreSystemProxy(backupPath string) error {
	if backupPath == "" {
		return fmt.Errorf("代理设置备份路径不能为空")
	}
	b, err := os.ReadFile(backupPath)
	if err != nil {
		return fmt.Errorf("读取代理设置备份失败：%w", err)
	}
	var saved systemProxyBackup
	if err := json.Unmarshal(b, &saved); err != nil {
		return fmt.Errorf("代理设置备份格式无效：%w", err)
	}
	if saved.Version != 1 || saved.OS != platformName || len(saved.Data) == 0 {
		return fmt.Errorf("代理设置备份与当前系统不匹配或版本不受支持")
	}
	if err := restorePlatformProxy(saved.Data); err != nil {
		return err
	}
	if err := os.Remove(backupPath); err != nil {
		return fmt.Errorf("原设置已恢复，但删除备份失败：%w", err)
	}
	return nil
}

func writeBackupExclusive(path string, backup systemProxyBackup) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("创建代理备份目录失败：%w", err)
	}
	b, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		return fmt.Errorf("编码代理设置备份失败：%w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("代理设置备份已存在，为防止覆盖旧设置而停止：%s", path)
		}
		return fmt.Errorf("独占创建代理设置备份失败：%w", err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("写入代理设置备份失败：%w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("同步代理设置备份失败：%w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("关闭代理设置备份失败：%w", err)
	}
	return nil
}
