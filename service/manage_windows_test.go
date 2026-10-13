//go:build windows

package service

import (
	"reflect"
	"testing"
)

func TestWindowsServiceArguments(t *testing.T) {
	want := []string{"run", "--config", `C:\Codex Router\config.yaml`}
	got := windowsServiceArguments(`C:\Codex Router\config.yaml`)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("服务启动参数不正确：%v", got)
	}
}
