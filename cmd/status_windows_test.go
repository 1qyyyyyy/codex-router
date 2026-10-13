//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopStoppedWarnsPendingRestore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	_, backup := backgroundPaths(path)
	if err := os.MkdirAll(filepath.Dir(backup), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	status := desktopStatus(path)
	if !strings.Contains(status, "请执行停止代理") || strings.Contains(status, "上游健康：健康") {
		t.Fatalf("未运行状态缺少恢复提示：%s", status)
	}
}
