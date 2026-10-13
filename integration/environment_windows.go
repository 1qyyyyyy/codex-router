//go:build windows

package integration

import (
	"encoding/json"
	"fmt"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"net/url"
	"unsafe"
)

var proxyEnvironmentNames = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"}

func VerifySystemProxy(proxyURL string) error {
	flags, err := queryConnectionFlags()
	if err != nil {
		return err
	}
	if flags != 3 {
		return fmt.Errorf("系统连接没有采用手动代理，可能被自动配置覆盖")
	}
	address, err := url.Parse(proxyURL)
	if err != nil {
		return err
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsPath, registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	enabled, _, err := key.GetIntegerValue("ProxyEnable")
	if err != nil || enabled != 1 {
		return fmt.Errorf("系统代理未启用，可能被其他代理工具覆盖")
	}
	server, _, err := key.GetStringValue("ProxyServer")
	if err != nil || server != address.Host {
		return fmt.Errorf("系统代理指向其他地址，当前后台代理未接管")
	}
	values, err := captureProxyEnvironment()
	if err != nil {
		return err
	}
	for _, name := range proxyEnvironmentNames[:3] {
		var text string
		if err := json.Unmarshal(values[name].Data, &text); err != nil || text != proxyURL {
			return fmt.Errorf("用户代理环境变量未指向本地入口：%s", name)
		}
	}
	return nil
}

func captureProxyEnvironment() (map[string]registryValue, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE)
	result := make(map[string]registryValue)
	if err == registry.ErrNotExist {
		for _, name := range proxyEnvironmentNames {
			result[name] = registryValue{}
		}
		return result, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取用户代理环境变量失败：%w", err)
	}
	defer key.Close()
	for _, name := range proxyEnvironmentNames {
		value, err := readRegistryValue(key, name)
		if err != nil {
			return nil, err
		}
		result[name] = value
	}
	return result, nil
}

func setProxyEnvironment(proxyURL string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, "Environment", registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	for _, name := range proxyEnvironmentNames {
		value := proxyURL
		if name == "NO_PROXY" {
			value = "localhost,127.0.0.1,::1"
		}
		if err := key.SetStringValue(name, value); err != nil {
			return err
		}
	}
	notifyEnvironment()
	return nil
}

func restoreProxyEnvironment(values map[string]registryValue) error {
	if len(values) == 0 {
		return nil
	}
	for _, name := range proxyEnvironmentNames {
		if _, ok := values[name]; !ok {
			return fmt.Errorf("代理环境变量备份不完整")
		}
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, "Environment", registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	for _, name := range proxyEnvironmentNames {
		if err := writeRegistryValue(key, name, values[name]); err != nil {
			return err
		}
	}
	notifyEnvironment()
	return nil
}

func notifyEnvironment() {
	text, _ := windows.UTF16PtrFromString("Environment")
	user32 := windows.NewLazySystemDLL("user32.dll")
	_, _, _ = user32.NewProc("SendMessageTimeoutW").Call(0xffff, 0x001a, 0, uintptr(unsafe.Pointer(text)), 2, 2000, 0)
}
