package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteBackupExclusiveDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy-backup.json")
	first := systemProxyBackup{Version: 1, OS: "test", Data: json.RawMessage(`{"原始":"一"}`)}
	if err := writeBackupExclusive(path, first); err != nil {
		t.Fatal(err)
	}
	second := systemProxyBackup{Version: 2, OS: "other", Data: json.RawMessage(`{"原始":"二"}`)}
	if err := writeBackupExclusive(path, second); err == nil {
		t.Fatal("已有备份时应拒绝覆盖")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved systemProxyBackup
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.OS != "test" || saved.Version != 1 {
		t.Fatalf("已有备份被覆盖：%+v", saved)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Fatalf("备份权限过宽：%o", info.Mode().Perm())
	}
}

func TestRestoreRejectsMalformedBackupWithoutDeleting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(path, []byte("不是 JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreSystemProxy(path); err == nil {
		t.Fatal("无效备份应返回错误")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("失败恢复不应删除备份：%v", err)
	}
}

func TestEnableFailureRollsBackAndKeepsBackup(t *testing.T) {
	oldCapture, oldEnable, oldRestore := captureProxySnapshot, enablePlatformProxy, restorePlatformProxy
	t.Cleanup(func() {
		captureProxySnapshot, enablePlatformProxy, restorePlatformProxy = oldCapture, oldEnable, oldRestore
	})
	backupData := json.RawMessage(`{"原始":"设置"}`)
	captureProxySnapshot = func() (json.RawMessage, error) { return backupData, nil }
	applyErr := os.ErrPermission
	enablePlatformProxy = func(string) error { return applyErr }
	rolledBack := false
	restorePlatformProxy = func(data json.RawMessage) error {
		rolledBack = string(data) == string(backupData)
		return nil
	}
	path := filepath.Join(t.TempDir(), "rollback.json")
	if err := EnableSystemProxy("http://127.0.0.1:17890", path); err == nil {
		t.Fatal("启用失败时应返回错误")
	}
	if !rolledBack {
		t.Fatal("启用失败后没有尝试恢复原设置")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("启用失败后应保留原始备份：%v", err)
	}
}

func TestRestoreDeletesBackupOnlyAfterSuccess(t *testing.T) {
	oldRestore := restorePlatformProxy
	t.Cleanup(func() { restorePlatformProxy = oldRestore })
	path := filepath.Join(t.TempDir(), "restore.json")
	snapshot := systemProxyBackup{Version: 1, OS: platformName, Data: json.RawMessage(`{"快照":true}`)}
	if err := writeBackupExclusive(path, snapshot); err != nil {
		t.Fatal(err)
	}
	restorePlatformProxy = func(json.RawMessage) error { return os.ErrPermission }
	if err := RestoreSystemProxy(path); err == nil {
		t.Fatal("恢复失败时应返回错误")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("恢复失败时不应删除备份：%v", err)
	}
	restorePlatformProxy = func(json.RawMessage) error { return nil }
	if err := RestoreSystemProxy(path); err != nil {
		t.Fatalf("恢复成功时应删除备份：%v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("恢复成功后备份仍存在：%v", err)
	}
}
