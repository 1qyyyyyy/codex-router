//go:build windows

package integration

import (
	"encoding/json"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestValidateRegistrySnapshotPreservesLargeInteger(t *testing.T) {
	value := registryValue{Exists: true, Type: registry.QWORD, Data: json.RawMessage("18446744073709551615")}
	if err := validateRegistryValue(value); err != nil {
		t.Fatalf("有效 QWORD 备份被拒绝：%v", err)
	}
	malformed := registryValue{Exists: true, Type: registry.LINK, Data: json.RawMessage(`"链接"`)}
	if err := validateRegistryValue(malformed); err == nil {
		t.Fatal("无法无损恢复的注册表类型必须被拒绝")
	}
}
